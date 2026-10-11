// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/reporting"
)

// The deployment settings record (core.deployment_settings) is the product state
// an administrator changes: today the edition activation. It is the source of
// truth. Each node keeps a file copy of the activation (activation.go) because
// modules are built before the store opens; boot reconciles the two before any
// listener starts and restarts once when they differ (reconcileActivation).

const productSettingsVersion = "olivares.deployment.settings.v1"

// settingsRestartMarker is written in the data directory before the engine
// restarts itself for the settings, and removed by the next start: a node that
// still differs after one restart serves instead of restarting forever. It is
// the engine's own state, not a setting.
const settingsRestartMarker = "settings-restart.marker"

// productSettingsDoc is the record's document.
type productSettingsDoc struct {
	Tracing    *obstrace.Settings  `json:"tracing,omitempty"`
	Version    string              `json:"version"`
	Activation *ActivationManifest `json:"activation,omitempty"`
	// Modules is the module selection (moduleprofile.go); absent until the
	// first start that records one.
	Modules          *moduleSelectionDoc     `json:"modules,omitempty"`
	ReportingSigning *reporting.SigningState `json:"reporting_signing,omitempty"`
	// PreviewsHidden is recorded with a new installation's first module
	// selection: its console navigation lists the first job only, and every
	// other page keeps its address. An installation that existed before never
	// has it, so its navigation lists everything it did.
	PreviewsHidden bool `json:"previews_hidden,omitempty"`
}

// productSettings reads and writes the record and keeps this node's file copy
// in step with it.
type productSettings struct {
	st      store.Store
	dataDir string
	now     func() time.Time
	// used names the modules whose tables hold rows (usedModules); nil reads
	// none, and then every module an activation change stops is kept.
	used func(context.Context) ([]string, error)
}

func newProductSettings(st store.Store, dataDir string) *productSettings {
	return &productSettings{st: st, dataDir: dataDir, now: time.Now}
}

// load returns the record's document and whether a record exists.
func (p *productSettings) load(ctx context.Context) (productSettingsDoc, bool, error) {
	var doc productSettingsDoc
	var found bool
	err := p.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.DeploymentSettings().List(ctx, model.Query{Filters: []model.Filter{}})
		if err != nil || len(rows) == 0 {
			return err
		}
		found = true
		return json.Unmarshal([]byte(rows[0].Doc), &doc)
	})
	if err != nil {
		return productSettingsDoc{}, false, fmt.Errorf("read the deployment settings: %w", err)
	}
	return doc, found, nil
}

// Activation is the deployment's activation: the record's, or this node's file
// copy when no record exists yet.
func (p *productSettings) Activation(ctx context.Context) (*ActivationManifest, error) {
	doc, found, err := p.load(ctx)
	if err != nil {
		return nil, err
	}
	if found && doc.Activation != nil {
		return doc.Activation, nil
	}
	return LoadActivationManifest(p.dataDir)
}

// SaveActivation records a new activation for the whole deployment, audited,
// then writes this node's file copy. owned, when non-nil, is the purchased set
// every requested module must be in (AdmitActivationModules). The modules change
// at the next start: the caller asks for it with EditionDependencies.RequestRestart.
func (p *productSettings) SaveActivation(ctx context.Context, actor auth.Principal, m *ActivationManifest, owned []string) error {
	if m == nil {
		return errors.New("no activation to save")
	}
	if owned != nil {
		if err := AdmitActivationModules(owned, m.RequestedModules()); err != nil {
			return err
		}
	}
	now := p.now()
	saved := *m
	saved.Version = activationManifestVersion
	saved.UpdatedAt = now.UTC().Format(time.RFC3339)
	saved.UpdatedBy = "" // who changed it is in the audit event, not in the document
	if err := p.write(ctx, actor, "deployment.settings.activation", &saved); err != nil {
		return err
	}
	if err := SaveActivationManifest(p.dataDir, &saved, now); err != nil {
		return fmt.Errorf("the deployment settings were saved, but this node's copy was not (the next start rewrites it): %w", err)
	}
	return nil
}

// write stores m as the record's activation and appends one audit event.
//
// A module that stops running because its add-on is no longer active, and that
// holds data, is added to the module selection in the same write (and to this
// node's copy): its data stays served until an administrator deselects it.
func (p *productSettings) write(ctx context.Context, actor auth.Principal, action string, m *ActivationManifest) error {
	keep, err := p.modulesToKeep(ctx, m)
	if err != nil {
		return err
	}
	meta := map[string]any{"modules": m.RequestedModules(), "active": len(m.activeOverlay())}
	if len(keep) > 0 {
		meta["kept_modules"] = keep
	}
	var selected []string
	err = p.update(ctx, actor, action, meta, func(doc *productSettingsDoc) {
		doc.Activation = m
		if len(keep) > 0 && doc.Modules != nil {
			doc.Modules.Selected = sortedUnion(doc.Modules.Selected, keep)
			selected = doc.Modules.Selected
		}
	})
	if err != nil || selected == nil {
		return err
	}
	known, _ := knownModules(selected)
	if err := saveNodeModuleSelection(p.dataDir, known, p.now()); err != nil {
		return fmt.Errorf("the deployment settings were saved, but this node's module profile was not (the next start rewrites it): %w", err)
	}
	return nil
}

// modulesToKeep names the modules the record's activation runs, m no longer
// runs, the selection does not hold, and that hold data. When usage cannot be
// read, every such module is kept.
func (p *productSettings) modulesToKeep(ctx context.Context, m *ActivationManifest) ([]string, error) {
	doc, found, err := p.load(ctx)
	if err != nil || !found || doc.Activation == nil || doc.Modules == nil {
		return nil, err
	}
	known, _ := knownModules(doc.Modules.Selected)
	was, _ := resolveModuleProfileWith(known, activationModules(doc.Activation))
	now, _ := resolveModuleProfileWith(known, activationModules(m))
	var leaving []string
	for _, name := range was.ActiveNames() {
		if !now.Active(name) {
			leaving = append(leaving, name)
		}
	}
	if len(leaving) == 0 || p.used == nil {
		return leaving, nil
	}
	used, err := p.used(ctx)
	if err != nil {
		return leaving, nil
	}
	return slices.DeleteFunc(leaving, func(name string) bool { return !slices.Contains(used, name) }), nil
}

// sortedUnion is a ∪ b, sorted, each name once.
func sortedUnion(a, b []string) []string {
	out := slices.Concat(a, b)
	slices.Sort(out)
	return slices.Compact(out)
}

// writeModules stores the module selection and appends one audit event.
func (p *productSettings) writeModules(ctx context.Context, actor auth.Principal, action string, selected []string) error {
	sel := moduleSelectionDoc{Version: moduleProfileVersion, Selected: slices.Clone(selected), UpdatedAt: p.now().UTC().Format(time.RFC3339)}
	if sel.Selected == nil {
		sel.Selected = []string{}
	}
	meta := map[string]any{"selected": sel.Selected}
	return p.update(ctx, actor, action, meta, func(doc *productSettingsDoc) {
		doc.Modules = &sel
		// An administrator's own selection ends the first run: from the next
		// start the console lists every page.
		delete(meta, "previews_hidden")
		if doc.PreviewsHidden {
			doc.PreviewsHidden = false
			meta["previews_hidden"] = false
		}
	})
}

// update changes one part of the record's document, keeping the others, and
// appends one audit event. A conflict (another node created the record at the
// same moment) is retried once as an update.
func (p *productSettings) update(ctx context.Context, actor auth.Principal, action string, meta map[string]any, change func(*productSettingsDoc)) error {
	return p.updateWithAudit(ctx, actor, action, meta, change, false)
}

func (p *productSettings) updateWithAudit(ctx context.Context, actor auth.Principal, action string, meta map[string]any, change func(*productSettingsDoc), requireEvidence bool) error {
	err := p.updateOnce(ctx, actor, action, meta, change, requireEvidence)
	if errors.Is(err, store.ErrConflict) {
		err = p.updateOnce(ctx, actor, action, meta, change, requireEvidence)
	}
	return err
}

func (p *productSettings) updateOnce(ctx context.Context, actor auth.Principal, action string, meta map[string]any, change func(*productSettingsDoc), requireEvidence bool) error {
	return p.st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.DeploymentSettings().List(ctx, model.Query{Filters: []model.Filter{}})
		if err != nil {
			return err
		}
		var doc productSettingsDoc
		if len(rows) > 0 {
			if err := json.Unmarshal([]byte(rows[0].Doc), &doc); err != nil {
				return fmt.Errorf("read the deployment settings: %w", err)
			}
		}
		doc.Version = productSettingsVersion
		change(&doc)
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			_, err = as.DeploymentSettings().Create(ctx, model.DeploymentSettings{Doc: string(raw)})
		} else {
			row := rows[0]
			row.Doc = string(raw)
			_, err = as.DeploymentSettings().Update(ctx, row)
		}
		if err != nil {
			return err
		}
		event, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: action, TargetKind: "core.deployment_settings",
			Meta: meta,
		})
		if err == nil && requireEvidence && event.Seq <= 0 {
			return errors.New("deployment settings audit evidence was not persisted")
		}
		return err
	})
}

// reconcileActivation is reconcileSettings for the activation alone.
func reconcileActivation(ctx context.Context, p *productSettings, log *slog.Logger) error {
	return reconcileSettings(ctx, p, nil, log)
}

// reconcileSettings makes this node match the deployment settings record before
// any listener starts: its activation and, when mods is given, its module
// profile. It returns a selfRestartError when the modules this start built differ
// from the record, so no request is served with them. It restarts at most once
// for one change: a node that still differs after that restart serves.
func reconcileSettings(ctx context.Context, p *productSettings, mods *moduleReconcile, log *slog.Logger) error {
	marker := filepath.Join(p.dataDir, settingsRestartMarker)
	_, statErr := os.Stat(marker)
	restarted := statErr == nil
	_ = os.Remove(marker) // a later restart is a new one
	var reasons []string
	activationChanged, err := p.reconcileActivation(ctx, log)
	if err != nil {
		return err
	}
	if activationChanged {
		reasons = append(reasons, "the deployment settings changed the active modules")
	}
	if mods != nil {
		modulesChanged, err := p.reconcileModules(ctx, *mods, log)
		switch {
		case err != nil:
			// The selection could not be read or recorded: serve with the modules this
			// start built rather than not at all. The next start tries again.
			log.Error("modules: the module selection could not be reconciled with the deployment settings; serving with the modules this start built",
				"err", err, "running", mods.booted.ActiveNames())
		case modulesChanged:
			reasons = append(reasons, "the deployment settings changed the modules this node runs")
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	if restarted {
		log.Error("settings: this node still differs from the deployment settings after one restart; serving with what it built",
			"reasons", reasons, "activation_file", ActivationManifestPath(p.dataDir), "module_profile", moduleProfilePath(p.dataDir))
		return nil
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		log.Error("settings: cannot mark the restart for the settings, so the engine serves with this node's refreshed files at its next start", "err", err)
		return nil
	}
	log.Info("settings: restarting before serving", "reasons", reasons)
	return &selfRestartError{reason: strings.Join(reasons, "; ")}
}

// reconcileBeforeServing is what serve does with a booted engine before any
// listener starts: bring it in step with the deployment settings
// (reconcileSettings), then tell server-info whether the console navigation
// lists the first job only. An unreadable answer lists every page.
func reconcileBeforeServing(ctx context.Context, eng *engine, log *slog.Logger) error {
	mods := &moduleReconcile{booted: eng.moduleProfile, used: func(ctx context.Context) ([]string, error) { return usedModules(ctx, eng.store, eng.census) }, demo: !eng.demoTenant.IsZero()}
	settings := newProductSettings(eng.store, eng.dataDir)
	settings.used = mods.used
	if err := reconcileSettings(ctx, settings, mods, log); err != nil {
		return err
	}
	doc, _, err := settings.load(ctx)
	if err != nil {
		log.Error("settings: cannot read whether the console hides preview pages; its navigation lists every page until the next start reads it", "err", err)
		return nil
	}
	eng.api.SetPreviewsHidden(doc.PreviewsHidden)
	return nil
}

// reconcileActivation makes this node's activation file match the record and
// reports whether the active entries (what modules are built from) differ from
// what this start built.
//
//   - no record yet (an install from before 26.10.1): the node's file is
//     imported once, and nothing restarts;
//   - a node file newer than the record (changed offline on this node) is
//     recorded;
//   - otherwise the file is rewritten from the record when they differ.
func (p *productSettings) reconcileActivation(ctx context.Context, log *slog.Logger) (bool, error) {
	built, err := LoadActivationManifest(p.dataDir)
	if err != nil {
		return false, fmt.Errorf("read this node's activation file: %w", err)
	}
	doc, found, err := p.load(ctx)
	if err != nil {
		return false, err
	}
	if !found || doc.Activation == nil {
		if len(built.Entries) == 0 && len(built.Modules) == 0 {
			return false, nil
		}
		actor, aerr := auth.NewSystemOperator("boot/activation-import", "import this node's activation file into the deployment settings")
		if aerr != nil {
			return false, aerr
		}
		imported := *built
		imported.UpdatedBy = ""
		if err := p.write(ctx, actor, "deployment.settings.import", &imported); err != nil {
			return false, err
		}
		log.Info("activation: imported this node's activation file into the deployment settings", "active", len(built.activeOverlay()))
		return false, nil
	}
	record := doc.Activation
	if sameActivation(record, built) {
		log.Info("activation: in step with the deployment settings", "active", len(built.activeOverlay()))
		return false, nil
	}
	if activationTime(built).After(activationTime(record)) {
		// Changed on this node after the record (`olivares enterprise enable` while
		// the engine was stopped): this start already built from it, so it becomes
		// the deployment's activation.
		actor, aerr := auth.NewSystemOperator("boot/activation-import", "record this node's newer activation file in the deployment settings")
		if aerr != nil {
			return false, aerr
		}
		if err := p.write(ctx, actor, "deployment.settings.import", built); err != nil {
			return false, err
		}
		log.Info("activation: this node's file is newer than the deployment settings; recorded it", "active", len(built.activeOverlay()))
		return false, nil
	}
	if err := SaveActivationManifest(p.dataDir, record, activationTime(record)); err != nil {
		return false, fmt.Errorf("write this node's activation file from the deployment settings: %w", err)
	}
	if maps.Equal(record.activeOverlay(), built.activeOverlay()) {
		log.Info("activation: this node's file was refreshed from the deployment settings; the active modules are unchanged")
		return false, nil
	}
	log.Info("activation: the deployment settings changed the active modules",
		"was", len(built.activeOverlay()), "now", len(record.activeOverlay()))
	return true, nil
}

// activationTime is when m was written (zero when it does not say).
func activationTime(m *ActivationManifest) time.Time {
	t, err := time.Parse(time.RFC3339, m.UpdatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// settingsRecordingActivation records every activation the console applies in
// the deployment settings, then restarts the engine so its modules follow it.
// The edition's service writes this node's file as before; nothing else changes
// for it.
type settingsRecordingActivation struct {
	api.ActivationService
	settings *productSettings
	restart  func(reason string) error
	log      *slog.Logger
}

// recordingActivation wraps svc; nil (the community build) stays nil, so the
// routes keep answering 501.
func recordingActivation(svc api.ActivationService, settings *productSettings, restart func(string) error, log *slog.Logger) api.ActivationService {
	if svc == nil {
		return nil
	}
	return settingsRecordingActivation{ActivationService: svc, settings: settings, restart: restart, log: log}
}

func (s settingsRecordingActivation) ActivationApply(ctx context.Context, req api.ActivationApplyRequest) (api.ActivationStatusDTO, error) {
	dto, err := s.ActivationService.ActivationApply(ctx, req)
	if err != nil {
		return dto, err
	}
	// Only a durable record plus an accepted restart is success: anything less is
	// reported, so the console never says "applied" for a change that will not run.
	m, err := LoadActivationManifest(s.settings.dataDir)
	if err == nil {
		var actor auth.Principal
		if actor, err = auth.NewSystemOperator("api/activation", "record the console activation in the deployment settings"); err == nil {
			err = s.settings.write(ctx, actor, "deployment.settings.activation", m)
		}
	}
	if err != nil {
		s.log.Error("activation: applied to this node's file but not recorded in the deployment settings", "err", err)
		return api.ActivationStatusDTO{}, fmt.Errorf("%w: %v", api.ErrActivationNotRecorded, err)
	}
	if s.restart == nil {
		return api.ActivationStatusDTO{}, api.ErrActivationRestartUnavailable
	}
	if err := s.restart("license activation: " + req.Action); err != nil {
		s.log.Warn("activation: saved, but the engine cannot restart itself", "err", err)
		return api.ActivationStatusDTO{}, fmt.Errorf("%w: %v", api.ErrActivationRestartUnavailable, err)
	}
	dto.Restarting = true
	return dto, nil
}

// sameActivation compares what the two activations say, ignoring when and by
// whom they were written.
func sameActivation(a, b *ActivationManifest) bool {
	x, y := *a, *b
	for _, m := range []*ActivationManifest{&x, &y} {
		m.UpdatedAt, m.UpdatedBy, m.Version = "", "", ""
		if len(m.Entries) == 0 {
			m.Entries = nil
		}
		if len(m.Modules) == 0 {
			m.Modules = nil
		}
	}
	ja, errA := json.Marshal(x)
	jb, errB := json.Marshal(y)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// doctorActivationSourceCheck says where this node's activation comes from: the
// deployment settings record, of which this node's file is the copy.
func doctorActivationSourceCheck(dataDir string) doctorCheck {
	c := doctorCheck{Name: "activation-source", Required: false}
	m, err := LoadActivationManifest(dataDir)
	if err != nil {
		c.Status, c.Detail = "fail", "this node's activation file cannot be read: "+err.Error()
		c.Remediation = "restore " + ActivationManifestPath(dataDir) + " or remove it; the engine rewrites it from the deployment settings"
		return c
	}
	active := len(m.activeOverlay())
	c.Status = "pass"
	c.Detail = fmt.Sprintf("the deployment settings record; this node's file is its copy (%d active)", active)
	return c
}
