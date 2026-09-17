// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/firstparty"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// sourceReconciler is the live connector/source reconfiguration engine. It
// is the single place the durable source roster (the sealed store) meets the
// running runtime: it diffs the desired roster against what is wired and
// adds/removes/rotates INDIVIDUAL sources in flight — no process restart. It
// implements api.SourceRoster, so the console/CLI drive it through the
// superadmin+AAL3 endpoints, and the SIGHUP handler and boot call it directly.
//
// Construction mirrors the boot wireSources path EXACTLY (the three wiring paths,
// the secret-reference resolution, the S142 external-plugin admission), so a
// source wired live is identical to one wired at boot. Every operation is
// deny-closed (a source that cannot be built/validated is rejected with a reason,
// the others untouched) and honest (a persisted change whose live apply is
// rejected says so; configuration outside the source roster is reported as
// requiring a restart, never faked).
//
// Reconciliation is NODE-LOCAL: each node reconciles its OWN runtime from the
// shared roster. It is deliberately NOT leader-gated (every node must run its own
// sources), so a write applies on the receiving node immediately; other nodes
// pick the change up on their next reload/SIGHUP/restart. A cross-node live push
// is a follow-on, not part of this cut.
type sourceReconciler struct {
	rt       *runtime.Runtime
	store    *auth.SourceStore
	resolver *secret.Resolver
	// secrets is the sealed secret store, used by the console
	// connector-onboarding surface to SEAL an inline credential (and store only a
	// `store:<name>` reference on the source row). nil leaves onboarding's secret
	// sealing fail-closed (Put returns ErrNoSecretSealer) — the live reconcile path
	// never touches it (it resolves references via resolver only).
	secrets  *auth.SecretStore
	embedDir string              // connector scratch dir for firstparty.Extract
	trust    *connectorTrustSpec // external-plugin trust policy (from the boot file)
	scope    model.TenantID
	log      *slog.Logger

	// prepare builds (but does not wire) the connector for a definition and resolves
	// its config. It defaults to defaultPrepare (the real three-path construction);
	// it is a field so a test can drive the diff/apply engine with fake connectors
	// without depending on real connector Open behavior.
	prepare func(ctx context.Context, def model.SourceDef) (*runtime.PreparedSource, sdk.Config, string)

	mu sync.Mutex
	// applied maps each spec name to the runtime registration it is currently wired
	// as + the config fingerprint it was wired from. It is the reconciler's view of
	// "what is running", seeded by the first reconcile at boot and updated on every
	// change. Guarded by mu (which also single-flights the reconciler's logic; the
	// runtime serializes its own mutations independently via reloadMu).
	applied map[string]appliedSource
	// environmentRef is this node's persistent execution-environment identity
	// (B1); "" registers sources without a roster snapshot.
	environmentRef string
}

// appliedSource records the TWO identities of a wired source separately, because
// they are two different things and conflating them is what stopped an operator
// from running two sources of one connector kind:
//
//   - registrationName is the source's identity in the runtime. It is the roster
//     row's own name, so add / rotate / remove / status all address exactly this
//     source and never a sibling that happens to share a connector.
//   - componentName is the connector's Descriptor name — WHICH connector serves the
//     row. Two rows of one kind share it. It is kept for inspection and diagnosis
//     and nothing keys on it.
type appliedSource struct {
	registrationName string // the source's runtime identity (the roster row's name)
	componentName    string // the connector's descriptor name (its type identity)
	fingerprint      string // hash of the identity-affecting definition fields
	// sourceID and sourceRevision are the durable roster row's persistent id and
	// the version that was SUCCESSFULLY applied (B1). They are the identity the
	// session plane binds to — a rename keeps them, a delete-and-recreate under the
	// same name is another id, and a failed rotation leaves the previous revision
	// here because the previous instance is what keeps running.
	sourceID       model.ID
	sourceRevision int64
}

// registrationFor builds the roster snapshot the runtime stamps on every event of
// a reconciled source. Empty when this node has no execution-environment identity:
// the source still runs, its events simply stay unattributed, as they always were.
func (sr *sourceReconciler) registrationFor(def model.SourceDef) (event.SourceRegistration, bool) {
	if sr.environmentRef == "" || def.ID.IsZero() || def.Version < 1 {
		return event.SourceRegistration{}, false
	}
	return event.SourceRegistration{SourceID: def.ID.String(), SourceRevision: def.Version, EnvironmentRef: sr.environmentRef}, true
}

// useEnvironmentRef binds this node's persistent execution-environment identity
// so reconciled sources are registered WITH their roster snapshot. Called once at
// boot, before the first reconcile.
func (sr *sourceReconciler) useEnvironmentRef(ref string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.environmentRef = ref
}

// appliedRevision reports the persistent id and the successfully applied revision
// of the source registered under name, or false when this node has not applied
// it. It is what the provider-profile binding port answers "the revision this
// environment applied" with — the reconciler's own record of what it wired, which
// a failed Open never advances.
func (sr *sourceReconciler) appliedRevision(name string) (model.ID, int64, bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	app, ok := sr.applied[name]
	if !ok || app.sourceID.IsZero() || app.sourceRevision < 1 {
		return "", 0, false
	}
	return app.sourceID, app.sourceRevision, true
}

// requiresRestartDomains is the honest, always-stated list of configuration this
// live reload does NOT cover — read once at boot, changed only by a restart. The
// source roster is hot-reconfigurable; everything else here is not.
var requiresRestartDomains = []string{
	"identity / roster providers (OLIVARES_SOURCES_CONFIG.identity)",
	"knowledge document sources (OLIVARES_SOURCES_CONFIG.documents)",
	"external connector trust policy (OLIVARES_SOURCES_CONFIG.connector_trust)",
	"HTTP/gRPC listeners and TLS",
	"database DSN, the event bus, and the sealer key",
}

func newSourceReconciler(rt *runtime.Runtime, store *auth.SourceStore, resolver *secret.Resolver, secrets *auth.SecretStore, embedDir string, trust *connectorTrustSpec, log *slog.Logger) *sourceReconciler {
	sr := &sourceReconciler{
		rt: rt, store: store, resolver: resolver, secrets: secrets, embedDir: embedDir, trust: trust,
		scope: auth.GlobalSourceScope, log: log, applied: map[string]appliedSource{},
	}
	sr.prepare = sr.defaultPrepare
	return sr
}

var _ api.SourceRoster = (*sourceReconciler)(nil)

// ListSources returns the persisted roster annotated with live status.
func (sr *sourceReconciler) ListSources(ctx context.Context) ([]api.SourceRosterEntry, error) {
	defs, err := sr.store.List(ctx, sr.scope)
	if err != nil {
		return nil, err
	}
	// Keyed by REGISTRATION name — the same name the row was registered under — so
	// two rows of one kind get their own status instead of sharing the connector's.
	live := map[string]runtime.Status{}
	for _, s := range sr.rt.LiveSourceInventory() {
		live[s.Name] = s.Status
	}
	sr.mu.Lock()
	applied := make(map[string]appliedSource, len(sr.applied))
	for k, v := range sr.applied {
		applied[k] = v
	}
	sr.mu.Unlock()

	out := make([]api.SourceRosterEntry, 0, len(defs))
	for _, d := range defs {
		e := api.SourceRosterEntry{
			Name: d.Name, Kind: d.Kind, Tenant: d.Tenant, PollSeconds: d.PollSeconds,
			Enabled: d.Enabled, Config: d.Config, Status: sourceStatus(d, applied, live),
			SourceMode: sourceModeFromConfig(d.Config),
			// The connector that serves this row, reported BESIDE its name rather
			// than in place of it. Empty until the row is wired (an external plugin
			// self-describes only once launched).
			Component: applied[d.Name].componentName,
			// The persistent id the session plane binds to (B1), beside the name.
			ID: d.ID.String(),
		}
		// The revision this node applied, from the reconciler's own record and only
		// when that record is for THIS row: a name whose applied entry belongs to a
		// recreated row (another id) has nothing applied under this id. This is the
		// same answer appliedRevision gives the binding port, so a console that
		// names it cannot name a revision the port would refuse as stale.
		if app, ok := applied[d.Name]; ok && !app.sourceID.IsZero() && app.sourceID == d.ID && app.sourceRevision >= 1 {
			e.AppliedRevision = app.sourceRevision
		}
		if d.Plugin != nil {
			e.Plugin = &api.SourcePluginInput{Path: d.Plugin.Path, SHA256: d.Plugin.SHA256, Bundle: d.Plugin.Bundle, PredicateTypes: d.Plugin.PredicateTypes}
		}
		out = append(out, e)
	}
	return out, nil
}

func sourceStatus(d model.SourceDef, applied map[string]appliedSource, live map[string]runtime.Status) string {
	if !d.Enabled {
		return "disabled"
	}
	if app, ok := applied[d.Name]; ok {
		if st, ok := live[app.registrationName]; ok {
			return string(st)
		}
	}
	return "not_wired"
}

// PutSource persists a source definition and applies it to the running engine.
func (sr *sourceReconciler) PutSource(ctx context.Context, actor auth.Principal, in api.SourceRosterInput) (api.SourceApplyResult, error) {
	def := defFromInput(in)
	if err := checkInlineSecrets(def); err != nil {
		return api.SourceApplyResult{}, err
	}
	saved, err := sr.store.Put(ctx, actor, def)
	if err != nil {
		return api.SourceApplyResult{}, err
	}

	sr.mu.Lock()
	defer sr.mu.Unlock()
	res := api.SourceApplyResult{Name: saved.Name, Persisted: true}

	if !saved.Enabled {
		res.Action = "disabled"
		if app, ok := sr.applied[saved.Name]; ok {
			if rerr := sr.rt.RemoveSourceLive(ctx, app.registrationName); rerr != nil {
				res.Note = "persisted (disabled), but could not stop the running source live; it stops on next reload/restart: " + rerr.Error()
				return res, nil
			}
			delete(sr.applied, saved.Name)
		}
		res.Applied = true
		return res, nil
	}

	if app, ok := sr.applied[saved.Name]; ok && app.fingerprint == fingerprintDef(saved) {
		res.Action, res.Applied = "unchanged", true
		return res, nil
	}
	action, reject := sr.applyLocked(ctx, saved)
	res.Action = action
	if reject != "" {
		res.Applied = false
		res.Note = "persisted, but the live apply was rejected (takes effect on next reload/restart): " + reject
		return res, nil
	}
	res.Applied = true
	return res, nil
}

// DeleteSource removes a source definition and stops it in the running engine.
func (sr *sourceReconciler) DeleteSource(ctx context.Context, actor auth.Principal, name string) (api.SourceApplyResult, error) {
	// The roster and the applied map are keyed by the TRIMMED name (the store trims
	// on write), so trim here too — otherwise a padded name would delete the durable
	// row (the store trims internally) yet miss the live applied-map lookup below,
	// leaving the connector running while we falsely report it stopped.
	name = strings.TrimSpace(name)
	if err := sr.store.Delete(ctx, actor, sr.scope, name); err != nil {
		return api.SourceApplyResult{}, err
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	res := api.SourceApplyResult{Name: name, Persisted: true, Action: "removed"}
	if app, ok := sr.applied[name]; ok {
		if rerr := sr.rt.RemoveSourceLive(ctx, app.registrationName); rerr != nil {
			res.Note = "deleted from the roster, but could not stop the running source live; it stops on restart: " + rerr.Error()
			return res, nil
		}
		delete(sr.applied, name)
	}
	res.Applied = true
	return res, nil
}

// ReloadSources reconciles the whole durable roster against the running engine.
func (sr *sourceReconciler) ReloadSources(ctx context.Context, actor auth.Principal) (api.SourceReloadReport, error) {
	sr.log.Info("reconfigure: full source reload requested", "actor", actor.DisplayName)
	return sr.reconcile(ctx)
}

// reconcile diffs the desired ENABLED roster against what is applied and adds /
// removes / rotates each drifted source. It is the boot initial-apply and the
// SIGHUP/endpoint full re-sync — one path, so a reload and a fresh boot converge.
func (sr *sourceReconciler) reconcile(ctx context.Context) (api.SourceReloadReport, error) {
	defs, err := sr.store.List(ctx, sr.scope)
	if err != nil {
		return api.SourceReloadReport{}, err
	}

	sr.mu.Lock()
	defer sr.mu.Unlock()

	report := api.SourceReloadReport{RequiresRestart: requiresRestartDomains}

	want := map[string]model.SourceDef{}
	for _, d := range defs {
		if d.Enabled {
			want[d.Name] = d
		}
	}

	// REMOVE first: applied specs that are gone or now disabled (frees their
	// registration names before any re-add). Snapshot the keys so we can delete
	// while iterating.
	for _, specName := range sortedAppliedKeys(sr.applied) {
		if _, ok := want[specName]; ok {
			continue
		}
		app := sr.applied[specName]
		if rerr := sr.rt.RemoveSourceLive(ctx, app.registrationName); rerr != nil {
			report.Rejected = append(report.Rejected, api.SourceRejection{Name: specName, Reason: "remove failed: " + rerr.Error()})
			continue
		}
		delete(sr.applied, specName)
		report.Removed = append(report.Removed, specName)
	}

	// ADD / ROTATE / NOOP, in deterministic order.
	for _, specName := range sortedDefKeys(want) {
		def := want[specName]
		if app, ok := sr.applied[specName]; ok && app.fingerprint == fingerprintDef(def) {
			report.Unchanged++
			continue
		}
		switch action, reject := sr.applyLocked(ctx, def); {
		case reject != "":
			report.Rejected = append(report.Rejected, api.SourceRejection{Name: specName, Reason: reject})
		case action == "added":
			report.Added = append(report.Added, specName)
			// The per-source line is an observable contract — operators grep it and
			// examples/otel-genai-ingest documents+asserts it — that the roster
			// migration dropped (the boot/reload summary only counts). Keep the
			// legacy wireSources "ingest: wired source" wording.
			sr.log.Info("ingest: wired source (durable roster)", "name", def.Name, "kind", def.Kind, "tenant", def.Tenant)
		case action == "rotated":
			report.Rotated = append(report.Rotated, specName)
			sr.log.Info("ingest: wired source (durable roster, rotated in place)", "name", def.Name, "kind", def.Kind, "tenant", def.Tenant)
		}
	}
	return report, nil
}

// applyLocked applies one desired source to the running engine. sr.mu MUST be
// held. It returns the action ("added"/"rotated") and a non-empty reject reason
// when the source could not be applied (in which case any prior instance keeps
// running and sr.applied is unchanged — deny-closed). It never logs the resolver
// error verbatim: a backend error can embed credential material (the wireSources
// rule).
func (sr *sourceReconciler) applyLocked(ctx context.Context, def model.SourceDef) (action, reject string) {
	ps, cfg, rej := sr.prepare(ctx, def)
	if rej != "" {
		return "", rej
	}
	// The row's OWN name is the runtime identity. The store validated and trimmed
	// it; it is passed through byte for byte — never lower-cased, truncated, or
	// derived from the kind, the config or a secret. The connector's descriptor is
	// recorded beside it (component), for inspection only.
	//
	// A same-descriptor sibling is NO LONGER a collision: two rows of one kind are
	// two sources. What is still refused is a duplicate REGISTRATION name, and that
	// refusal comes from the runtime, which owns the namespace — including a name
	// already held by a module or an output, which it declines without disturbing
	// the owner.
	component := ps.ComponentName()
	prev, hadPrev := sr.applied[def.Name]
	poll := time.Duration(def.PollSeconds) * time.Second

	// Rotate-or-add is decided by what is ACTUALLY registered under this name, not
	// by the bookkeeping alone: a prior remove that failed leaves an applied entry
	// with nothing behind it, and rotating that would refuse for ever. A prepared
	// plugin's subprocess is reaped by whichever call takes it, so the choice has to
	// be made before either is called — it cannot be retried with the same prepare.
	// The roster snapshot the runtime stamps on this source's events (B1): the
	// row's persistent id and the revision being applied. It is recorded in
	// sr.applied ONLY after the runtime accepted the registration, so a failed Open
	// leaves the previous revision as "applied" — which it is, because the previous
	// instance is the one still running.
	registration, registered := sr.registrationFor(def)
	record := appliedSource{registrationName: def.Name, componentName: component, fingerprint: fingerprintDef(def)}
	if registered {
		record.sourceID, record.sourceRevision = def.ID, def.Version
	}

	if sr.rt.SourceIsRegistered(def.Name) {
		// Rotate THIS registration in place (deny-closed: the candidate is Opened
		// before the running one is dropped). A kind change takes the same path — the
		// registration name is stable, so only this source is replaced and its
		// previous connector is quiesced and closed by the swap, never orphaned.
		var err error
		if registered {
			err = sr.rt.ReplacePreparedSourceRegistered(ctx, def.Name, ps, cfg, def.Tenant, poll, registration)
		} else {
			err = sr.rt.ReplacePreparedSourceNamed(ctx, def.Name, ps, cfg, def.Tenant, poll)
		}
		if err != nil {
			return "", sr.applyErrReason("rotate", def.Name, err)
		}
		if hadPrev && prev.componentName != component {
			sr.log.Info("reconfigure: source rotated onto a different connector", "source", def.Name, "from", prev.componentName, "to", component)
		}
		sr.applied[def.Name] = record
		return "rotated", ""
	}

	var err error
	if registered {
		err = sr.rt.AddPreparedSourceRegistered(ctx, def.Name, ps, cfg, def.Tenant, poll, registration)
	} else {
		err = sr.rt.AddPreparedSourceNamed(ctx, def.Name, ps, cfg, def.Tenant, poll)
	}
	if err != nil {
		return "", sr.applyErrReason("add", def.Name, err)
	}
	sr.applied[def.Name] = record
	if hadPrev {
		return "rotated", ""
	}
	return "added", ""
}

// defaultPrepare builds (but does not wire) the connector for def and resolves its
// config, mirroring the three boot wiring paths. A non-empty reason means the
// source is refused (deny-closed) and nothing was launched. The reason is safe to
// surface — it never includes a resolver error verbatim.
func (sr *sourceReconciler) defaultPrepare(ctx context.Context, def model.SourceDef) (*runtime.PreparedSource, sdk.Config, string) {
	rawCfg := sdk.Config{Settings: def.Config}

	if def.Plugin != nil {
		spec := externalPluginSpec{Path: def.Plugin.Path, SHA256: def.Plugin.SHA256, Bundle: def.Plugin.Bundle, PredicateTypes: def.Plugin.PredicateTypes}
		digest, refusal := admitExternalPlugin(spec, sr.trust)
		if refusal != "" {
			return nil, sdk.Config{}, "external connector plugin refused (deny-closed): " + refusal
		}
		scfg, rerr := resolveConfig(ctx, sr.resolver, sdk.Descriptor{}, rawCfg)
		if rerr != nil {
			return nil, sdk.Config{}, "a secret reference could not be resolved"
		}
		ps, perr := sr.rt.PrepareSourcePluginVerified(spec.Path, digest)
		if perr != nil {
			return nil, sdk.Config{}, "the external connector plugin could not be launched: " + perr.Error()
		}
		return ps, scfg, ""
	}

	if bin, isPlugin := pluginBinaryForKind[def.Kind]; isPlugin {
		path, eerr := firstparty.Extract(sr.embedDir, bin)
		if eerr != nil {
			return nil, sdk.Config{}, fmt.Sprintf("the %q connector is not embedded in this build (build it with `task build:connectors`, or run it from a collector)", def.Kind)
		}
		scfg, rerr := resolveConfig(ctx, sr.resolver, sdk.Descriptor{}, rawCfg)
		if rerr != nil {
			return nil, sdk.Config{}, "a secret reference could not be resolved"
		}
		ps, perr := sr.rt.PrepareSourcePlugin(path)
		if perr != nil {
			return nil, sdk.Config{}, "the connector plugin could not be launched: " + perr.Error()
		}
		return ps, scfg, ""
	}

	conn, ok := buildInProcSource(def.Kind)
	if !ok {
		return nil, sdk.Config{}, fmt.Sprintf("unknown or unsupported source kind %q", def.Kind)
	}
	scfg, rerr := resolveConfig(ctx, sr.resolver, conn.Descriptor(), rawCfg)
	if rerr != nil {
		return nil, sdk.Config{}, "a secret reference could not be resolved"
	}
	return sr.rt.PrepareInProcSource(conn), scfg, ""
}

// applyErrReason produces a SAFE, surfaceable reason for an apply failure. A
// connector Open failure (runtime.ErrSourceOpenFailed) ran against the RESOLVED
// config — its underlying message can carry a live secret value — so it is
// genericized exactly like an unresolvable reference; the detail is logged only at
// Debug. Other failures (not-running, a duplicate registration name) carry no
// config and are surfaced verbatim.
func (sr *sourceReconciler) applyErrReason(verb, name string, err error) string {
	if errors.Is(err, runtime.ErrSourceOpenFailed) {
		sr.log.Debug("reconfigure: connector open failed", "verb", verb, "source", name, "err", err)
		return verb + " failed: the connector could not be opened with the supplied configuration"
	}
	return verb + " failed: " + err.Error()
}

// fingerprintDef hashes the identity- and behavior-affecting fields of a
// definition so the reconciler re-applies a source only when something actually
// changed. It hashes secret REFERENCES (never values), so rotating a reference is
// detected; rotating the secret VALUE behind an unchanged reference is NOT — to
// force that, toggle the source's enabled flag or change a config field. (A
// dedicated rotate action is a possible follow-on.)
func fingerprintDef(def model.SourceDef) string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s\x00tenant=%s\x00poll=%d\x00", def.Kind, def.Tenant, def.PollSeconds)
	keys := make([]string, 0, len(def.Config))
	for k := range def.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "c:%s=%s\x00", k, def.Config[k])
	}
	if def.Plugin != nil {
		fmt.Fprintf(h, "plugin=%s\x00%s\x00%s\x00", def.Plugin.Path, def.Plugin.SHA256, def.Plugin.Bundle)
		pt := append([]string(nil), def.Plugin.PredicateTypes...)
		sort.Strings(pt)
		for _, p := range pt {
			fmt.Fprintf(h, "pt:%s\x00", p)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// checkInlineSecrets rejects a write whose config puts a LITERAL secret in a field
// the connector declares secret — so a credential never lands in the durable
// roster in plaintext; the operator must store it with `olivares secrets` and
// reference it (store:<name>). It is a pure syntactic check (no resolution). It
// applies only to in-process kinds whose descriptor the host knows; an external or
// out-of-process plugin's secret fields are not known to the host, so its config
// is operator discipline (the same limitation as the file-config model), and the
// apply path still resolves references either way.
func checkInlineSecrets(def model.SourceDef) error {
	if def.Plugin != nil {
		return nil
	}
	if _, isPlugin := pluginBinaryForKind[def.Kind]; isPlugin {
		return nil
	}
	conn, ok := buildInProcSource(def.Kind)
	if !ok {
		return nil // unknown kind: the apply path reports it honestly; not a write concern
	}
	if err := secret.CheckNoInlineSecrets(conn.Descriptor(), sdk.Config{Settings: def.Config}); err != nil {
		return fmt.Errorf("%w: %v — store the value with `olivares secrets` and reference it (e.g. store:<name>)", auth.ErrBadSourceDef, err)
	}
	return nil
}

// defFromInput maps the API edit shape to the persisted model.
func defFromInput(in api.SourceRosterInput) model.SourceDef {
	def := model.SourceDef{
		Scope: auth.GlobalSourceScope, Name: strings.TrimSpace(in.Name), Kind: in.Kind, Tenant: in.Tenant,
		PollSeconds: in.PollSeconds, Enabled: in.Enabled, Config: in.Config,
	}
	if in.Plugin != nil {
		def.Plugin = &model.SourcePluginRef{Path: in.Plugin.Path, SHA256: in.Plugin.SHA256, Bundle: in.Plugin.Bundle, PredicateTypes: in.Plugin.PredicateTypes}
	}
	return def
}

// sourceDefFromSpec maps a boot-file source entry to a roster row, for the
// one-time bootstrap seed (the file becomes a seed; the table is authoritative).
func sourceDefFromSpec(s sourceSpec) model.SourceDef {
	def := model.SourceDef{
		Scope: auth.GlobalSourceScope, Name: s.Name, Kind: s.Kind, Tenant: s.Tenant,
		PollSeconds: s.PollSeconds, Enabled: true, Config: s.Config,
	}
	if s.Plugin != nil {
		def.Plugin = &model.SourcePluginRef{Path: s.Plugin.Path, SHA256: s.Plugin.SHA256, Bundle: s.Plugin.Bundle, PredicateTypes: s.Plugin.PredicateTypes}
	}
	return def
}

func sortedDefKeys(m map[string]model.SourceDef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedAppliedKeys(m map[string]appliedSource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
