// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/sdk"
)

// Knowledge document sources share the durable source roster with observation
// sources. A row whose kind is a document connector is not registered with the
// runtime: the reconciler opens it and registers it on the knowledge module, which
// pulls it on ingest and sync. Its "kb" setting names the knowledge base of its
// tenant that the engine syncs it into on save and then every poll_seconds.

// documentPreviewSize is how many documents the console's test previews.
const documentPreviewSize = 10

// isDocumentKind reports whether kind is a knowledge document connector this
// build constructs.
func isDocumentKind(kind string) bool {
	_, ok := buildContentSource(kind, nil)
	return ok
}

// documentCredentialMissing reports a document source that reads its live service
// with none of the credential fields its connector declares, as an imported
// definition does until the operator sets the credential on this server.
func documentCredentialMissing(def model.SourceDef) bool {
	src, ok := buildContentSource(def.Kind, nil)
	if !ok || sourceModeFromConfig(def.Config) != "live" {
		return false
	}
	declared := false
	for _, f := range src.Descriptor().ConfigFields {
		if !f.Secret {
			continue
		}
		declared = true
		if strings.TrimSpace(def.Config[f.Key]) != "" {
			return false
		}
	}
	return declared
}

// checkDocumentSettings validates the settings the engine itself reads on a
// document source.
func checkDocumentSettings(cfg map[string]string) error {
	if v := strings.TrimSpace(cfg["max_delete_percent"]); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 100 {
			return fmt.Errorf("%w: max_delete_percent must be a whole number from 0 to 100", auth.ErrBadSourceDef)
		}
	}
	if v := strings.TrimSpace(cfg["kb"]); v != "" && model.ID(v).IsZero() {
		return fmt.Errorf("%w: kb must be a knowledge base id", auth.ErrBadSourceDef)
	}
	return nil
}

// useKnowledge binds the module document rows are wired into and the extractor
// the file-server connector uses. Without it a document row is rejected with a
// reason, never silently dropped.
func (sr *sourceReconciler) useKnowledge(mod *knowledge.Module, richDoc contentsource.RichDocExtractor) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.knowledge, sr.richDoc = mod, richDoc
	sr.schedules, sr.stopSchedules = context.WithCancel(context.Background())
}

// stopDocumentSchedules ends every scheduled document sync (engine shutdown).
func (sr *sourceReconciler) stopDocumentSchedules() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.stopSchedules != nil {
		sr.stopSchedules()
	}
}

// openDocumentSource builds a document connector for def, resolves its secret
// references and opens it. A non-empty reason is safe to show: it never carries
// the connector's own error, which ran against the resolved credential.
func (sr *sourceReconciler) openDocumentSource(ctx context.Context, def model.SourceDef) (contentsource.Source, string) {
	if documentCredentialMissing(def) {
		return nil, "credential missing: set this source's credential on this server"
	}
	src, ok := buildContentSource(def.Kind, sr.richDoc)
	if !ok {
		return nil, unsupportedSourceKindReason(def.Kind)
	}
	cfg, err := resolveConfig(ctx, sr.resolver, src.Descriptor(), sdk.Config{Settings: def.Config})
	if err != nil {
		return nil, "a secret reference could not be resolved"
	}
	if err := src.Open(ctx, cfg); err != nil {
		_ = src.Close(ctx)
		sr.log.Debug("knowledge: document source open failed", "source", def.Name, "err", err)
		return nil, "the connector could not be opened with the supplied configuration"
	}
	return wrapContentSourceConfig(src, def.Config), ""
}

// applyDocumentLocked opens def and registers it on the knowledge module under its
// name, replacing and closing the instance it rotates. sr.mu MUST be held. A
// rejected apply leaves the previous instance running, as for observation sources.
func (sr *sourceReconciler) applyDocumentLocked(ctx context.Context, def model.SourceDef) (action, reject string) {
	if sr.knowledge == nil {
		return "", "the knowledge module is off, so document sources cannot be wired"
	}
	src, reject := sr.openDocument(ctx, def)
	if reject != "" {
		return "", reject
	}
	prev, hadPrev := sr.applied[def.Name]
	if hadPrev && prev.stopSchedule != nil {
		prev.stopSchedule()
	}
	if old := sr.knowledge.AddSource(def.Name, src); old != nil {
		_ = old.Close(ctx)
	}
	sr.applied[def.Name] = appliedSource{
		registrationName: def.Name, componentName: "knowledge:" + def.Kind, fingerprint: fingerprintDef(def),
		sourceID: def.ID, sourceRevision: def.Version, document: true,
		stopSchedule: sr.scheduleDocumentSync(def),
	}
	if hadPrev {
		return "rotated", ""
	}
	return "added", ""
}

// removeAppliedLocked stops a wired source of either class. sr.mu MUST be held.
func (sr *sourceReconciler) removeAppliedLocked(ctx context.Context, app appliedSource) error {
	if !app.document {
		return sr.rt.RemoveSourceLive(ctx, app.registrationName)
	}
	if app.stopSchedule != nil {
		app.stopSchedule()
	}
	if sr.knowledge != nil {
		if src := sr.knowledge.RemoveSource(app.registrationName); src != nil {
			_ = src.Close(ctx)
		}
	}
	return nil
}

// scheduleDocumentSync syncs def into its knowledge base now and then every
// poll_seconds (once when 0) until the returned stop is called. Each later run
// first reopens the source, so it reads the source as it is then: the file server
// indexes its tree when it is opened, and a rotated credential is read again. No
// "kb" setting means no schedule: the source is pulled only by an explicit ingest
// or sync.
func (sr *sourceReconciler) scheduleDocumentSync(def model.SourceDef) context.CancelFunc {
	kb := strings.TrimSpace(def.Config["kb"])
	if kb == "" || sr.schedules == nil {
		return nil
	}
	actor, err := auth.NewSystemOperator("sources/schedule", "scheduled sync of a document source")
	if err != nil {
		sr.log.Error("knowledge: cannot attribute the scheduled sync; source not scheduled", "source", def.Name, "err", err)
		return nil
	}
	ctx, stop := context.WithCancel(sr.schedules)
	every := time.Duration(def.PollSeconds) * time.Second
	go func() {
		for {
			sr.runDocumentSync(ctx, actor, def, model.ID(kb))
			if every <= 0 {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
			if !sr.reopenDocument(ctx, def) {
				return
			}
		}
	}()
	return stop
}

// reopenDocument opens def again and swaps it in for the running instance. False
// when def is no longer the applied revision (it was rotated, removed or the
// schedule stopped): that schedule then ends.
func (sr *sourceReconciler) reopenDocument(ctx context.Context, def model.SourceDef) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if app, ok := sr.applied[def.Name]; ctx.Err() != nil || !ok || !app.document || app.fingerprint != fingerprintDef(def) {
		return false
	}
	src, reject := sr.openDocument(ctx, def)
	if reject != "" {
		sr.log.Warn("knowledge: could not reopen the document source for its scheduled sync; syncing the open instance", "source", def.Name, "reason", reject)
		return true
	}
	if old := sr.knowledge.AddSource(def.Name, src); old != nil {
		_ = old.Close(ctx)
	}
	return true
}

func (sr *sourceReconciler) runDocumentSync(ctx context.Context, actor auth.Principal, def model.SourceDef, kb model.ID) {
	res, err := sr.knowledge.SyncSource(ctx, model.TenantID(def.Tenant), actor, kb, def.Name)
	switch {
	case ctx.Err() != nil:
	case err != nil:
		sr.log.Warn("knowledge: scheduled document sync failed", "source", def.Name, "kb", kb.String(), "err", err)
	case res.DeletesWithheld != "":
		sr.log.Warn("knowledge: scheduled document sync withheld its deletions", "source", def.Name, "kb", kb.String(), "reason", res.DeletesWithheld, "synced", res.DocsSynced)
	default:
		sr.log.Info("knowledge: scheduled document sync", "source", def.Name, "kb", kb.String(),
			"synced", res.DocsSynced, "deleted", res.DocsDeleted, "skipped", res.DocsSkipped, "errors", len(res.Errors))
	}
}

// previewDocuments lists up to documentPreviewSize documents of an opened source
// with the ACL each would be stored with. Bodies are fetched to read the ACL and
// then dropped: nothing is stored.
func previewDocuments(ctx context.Context, src contentsource.Source) ([]api.PreviewDocument, error) {
	refs, _, err := src.List(ctx, "")
	if err != nil {
		return nil, err
	}
	if len(refs) > documentPreviewSize {
		refs = refs[:documentPreviewSize]
	}
	out := make([]api.PreviewDocument, 0, len(refs))
	for _, ref := range refs {
		p := api.PreviewDocument{ID: ref.DocID, Title: ref.Title}
		doc, ferr := src.Fetch(ctx, ref.DocID)
		switch {
		case errors.Is(ferr, contentsource.ErrSkipDocument):
			p.Note = "skipped"
		case ferr != nil:
			p.Note = "unreadable"
		default:
			p.ACL = doc.ACL
		}
		out = append(out, p)
	}
	return out, nil
}

// isDocumentRow reports a roster row the reconciler wires on the knowledge module. A row of a
// Business document kind this build does not construct (stored by a Business engine) is a
// document row too, so the roster still holds its documents and the file's are not seeded back.
func isDocumentRow(def model.SourceDef) bool {
	return def.Plugin == nil && (isDocumentKind(def.Kind) || slices.Contains(businessDocumentKinds, def.Kind))
}

// seedDocumentRosterIfEmpty imports the file's first-party documents[] into the
// durable roster once, when it holds no document row, exactly as the observation
// seed does: all or none, retried next boot on failure. Their tenant is the
// system tenant until the operator names the knowledge base they sync into.
// External plugin documents stay file-config.
func seedDocumentRosterIfEmpty(ctx context.Context, store *auth.SourceStore, cfg sourcesConfig, log *slog.Logger) {
	existing, err := store.List(ctx, auth.GlobalSourceScope)
	if err != nil {
		log.Warn("knowledge: could not read the durable source roster; skipping the document seed", "err", err)
		return
	}
	for _, d := range existing {
		if isDocumentRow(d) {
			return
		}
	}
	var defs []model.SourceDef
	for _, d := range cfg.Documents {
		if d.Plugin != nil || strings.TrimSpace(d.Name) == "" || !isDocumentKind(d.Kind) {
			continue
		}
		defs = append(defs, model.SourceDef{
			Scope: auth.GlobalSourceScope, Name: strings.TrimSpace(d.Name), Kind: d.Kind,
			Tenant: model.SystemTenantID.String(), Enabled: true, Config: d.Config,
		})
	}
	if len(defs) == 0 {
		return
	}
	actor, err := auth.NewSystemOperator("boot/seed", "seeding the document sources from the boot config")
	if err != nil {
		log.Error("boot: cannot attribute the document seed; refusing to seed unattributed", "err", err)
		return
	}
	seeded, err := store.SeedAll(ctx, actor, defs)
	if err != nil {
		log.Error("knowledge: could not seed the durable roster from the file's documents[]; they stay file-wired this boot and the seed retries next boot",
			"err", err)
		return
	}
	log.Info("knowledge: seeded the durable roster from the file's documents[] (one-time; the table is now authoritative)", "seeded", seeded)
}

// dropRosterDocuments stops boot from wiring the file's first-party documents once
// the roster holds document rows: the reconciler wires those, so a source deleted
// in the console does not come back from the file.
func (d *deferredSecretWiring) dropRosterDocuments(ctx context.Context, store *auth.SourceStore, log *slog.Logger) {
	if d == nil || len(d.content) == 0 {
		return
	}
	rows, err := store.List(ctx, auth.GlobalSourceScope)
	if err != nil {
		log.Warn("knowledge: could not read the durable source roster; wiring the file's documents[]", "err", err)
		return
	}
	inRoster := false
	for _, r := range rows {
		inRoster = inRoster || isDocumentRow(r)
	}
	if !inRoster {
		return
	}
	kept := d.content[:0]
	for _, p := range d.content {
		if p.plugin != nil {
			kept = append(kept, p)
		}
	}
	if dropped := len(d.content) - len(kept); dropped > 0 {
		log.Info("knowledge: the durable roster is authoritative for document sources; the file's first-party documents[] is ignored (edit via the console)", "file_documents", dropped)
	}
	d.content = kept
}

// documentCatalogKinds is the canonical document kinds this build offers in the
// console catalog (aliases such as filesystem and postgres are not repeated).
func documentCatalogKinds() []string {
	kinds := []string{"fscontent", "gdrive", "notion", "pgcontent", "s3content"}
	for kind := range businessSourceKinds {
		if _, ok := buildContentSource(kind, nil); ok {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}
