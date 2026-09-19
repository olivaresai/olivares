// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// B1 — the profiled launch path of the operate runtime.
//
// A profiled launch differs from a legacy one in exactly three places: the profile
// is resolved server-side into a home snapshot before anything durable happens
// (resolveLaunchProfileInto), the child is started under that snapshot's homes
// (runtime_bridge.go buildLaunchSpec), and the provider's session id announced by
// the child is bound under the profile's scope inside one transaction with the run
// row (captureProfiledSessionID).
//
// ⛔ THE CONFIGURATION HOME IS THE DRIVER'S OWN VARIABLE, NOT ONE NAME FOR ALL.
// HOME is always HOME; the configuration home travels in whatever variable that
// driver declares — CLAUDE_CONFIG_DIR for the historical Claude runner, CODEX_HOME
// for the registered Codex driver (configHomeEnvForDriver). Writing another
// provider's variable would point a child at a home nobody selected, which is why
// providerHomeEnvName refuses the WHOLE family from a caller's env_allow or a launch
// gate's injection rather than only the current driver's two.
//
// ⛔ AND A HOME IS NOT AN AUTHENTICATION. The paths are configuration and storage
// identity: they hold no credential value, none is persisted on the profile, and
// finding a login inside one is not proof that this launch was authorized to use it.
// WHAT authorizes the child's provider identity is a separate, explicit field —
// ProviderProfile.AuthSource — with exactly two values and no fallback between them
// and no default: `provider_account_home`, the saved login inside the profile's own
// homes, or `managed_injection`, a provider-compatible credential minted at launch by
// that driver's governed adapter. Empty authorizes neither and refuses every launch
// whose driver requires one (requireAuthSourceForDriver), and the run persists the
// source it was launched under before the spawn, so a later re-authorization is a new
// launch decision rather than a silent continuation.
//
// Everything else about credentials is unchanged: WIF, the work and communication
// bearers, the PEP env, templates, budgets, the stop gate, the claim and the fence
// all apply exactly as before, and a profile never stores a credential of its own.

// The environment variables a profile owns on the child. All are explicit launch
// values, never inherited for a profiled launch and never accepted from a
// caller's env_allow, a gate's injection or a credential adapter.
const (
	envUserHome              = "HOME"
	envClaudeConfigDir       = "CLAUDE_CONFIG_DIR"
	envGrokHome              = "GROK_HOME"
	envOpenCodeConfigDir     = "OPENCODE_CONFIG_DIR"
	envOpenCodeConfig        = "OPENCODE_CONFIG"
	envOpenCodeConfigContent = "OPENCODE_CONFIG_CONTENT"
	envOpenCodeTUIConfig     = "OPENCODE_TUI_CONFIG"
	envXDGConfigHome         = "XDG_CONFIG_HOME"
	envXDGDataHome           = "XDG_DATA_HOME"
	envXDGStateHome          = "XDG_STATE_HOME"
	envXDGCacheHome          = "XDG_CACHE_HOME"
	envXDGRuntimeDir         = "XDG_RUNTIME_DIR"
)

// providerHomeEnvName reports whether name selects a provider HOME on the child.
//
// The set is the whole family, not just the current driver's, and that is the
// point: a Claude launch that accepted CODEX_HOME from a caller would be handing
// the child a home nobody authorized for a provider nobody selected. GROK_HOME is
// here for the same reason ahead of its driver — refusing a variable costs
// nothing, and the refusal is what has to exist BEFORE the driver does.
func providerHomeEnvName(name string) bool {
	switch name {
	case envUserHome, envClaudeConfigDir, envCodexHome, envGrokHome,
		envOpenCodeConfigDir, envOpenCodeConfig, envOpenCodeConfigContent, envOpenCodeTUIConfig:
		return true
	default:
		return false
	}
}

// openCodeReservedEnvName is the XDG family reserved for OpenCode profiled
// launches only. It is not a global home name: existing drivers may still set
// explicit XDG values of their own.
func openCodeReservedEnvName(name string) bool {
	switch name {
	case envXDGConfigHome, envXDGDataHome, envXDGStateHome, envXDGCacheHome, envXDGRuntimeDir:
		return true
	default:
		return false
	}
}

func openCodeXDGMapping(userHome string) []EnvVar {
	return []EnvVar{
		{Name: envXDGConfigHome, Value: filepath.Join(userHome, ".config")},
		{Name: envXDGDataHome, Value: filepath.Join(userHome, ".local", "share")},
		{Name: envXDGStateHome, Value: filepath.Join(userHome, ".local", "state")},
		{Name: envXDGCacheHome, Value: filepath.Join(userHome, ".cache")},
	}
}

func refuseOpenCodeUnsupportedControls(p CreateRunParams) error {
	if len(p.AllowedTools) > 0 {
		return badRequest("this OpenCode driver has no mapping for template tool restrictions; refusing the launch rather than discarding them")
	}
	if strings.TrimSpace(p.Instructions) != "" {
		return badRequest("this OpenCode driver has no mapping for template instructions; refusing the launch rather than discarding them")
	}
	if mode := strings.TrimSpace(p.PermissionMode); mode != "" && mode != "default" {
		return badRequest("this OpenCode driver has no mapping for permission mode " + mode + "; refusing the launch rather than discarding it")
	}
	return nil
}

func validateOpenCodeReservedInjection(driver string, env []EnvVar) error {
	if driver != providerDriverOpenCode {
		return nil
	}
	for _, item := range env {
		if openCodeReservedEnvName(item.Name) {
			return forbiddenErr("launch denied: " + item.Name + " is reserved for the OpenCode profiled launch mapping")
		}
	}
	return nil
}

func validateOpenCodeEnvAllow(driver string, names []string) error {
	if driver != providerDriverOpenCode {
		return nil
	}
	for _, name := range names {
		if openCodeReservedEnvName(name) {
			return badRequest("env_allow may not name " + name + " for an OpenCode profiled launch: the profile owns the XDG mapping")
		}
	}
	return nil
}

// validOpenProviderTerm bounds a model or effort name a provider validates for
// itself: printable, non-empty and short. It is a SHAPE, deliberately not a set —
// the set belongs to the provider, and a closed one here would be this runtime
// deciding which of another product's values are real.
func validOpenProviderTerm(s string) bool {
	if s == "" || len(s) > 64 || s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

// configHomeEnvForDriver is the configuration-home variable of one driver. The
// registered driver declares its own; the historical Claude path keeps
// CLAUDE_CONFIG_DIR.
func (m *Module) configHomeEnvForDriver(driver string) string {
	if d, ok := m.driverFor(driver); ok {
		return d.ConfigHomeEnv()
	}
	return envClaudeConfigDir
}

// resolveLaunchProfileInto turns p.ProviderProfileRef into p.ProviderHome, the
// SERVER's snapshot. B2 refuses a missing profile before any launch effect. It refuses,
// deny-closed, an unknown/disabled/retired/foreign/non-operable profile, a home
// that no longer resolves, and an env_allow that names a variable the profile
// owns — resolving that by order would be exactly the accident this refuses.
func (m *Module) resolveLaunchProfileInto(ctx context.Context, tenant model.TenantID, p *CreateRunParams) error {
	p.ProviderProfileRef = strings.TrimSpace(p.ProviderProfileRef)
	if p.ProviderProfileRef == "" {
		p.ProviderHome = nil // never trusted from the caller
		if m.rt.profiledLaunchesEnabled {
			return badRequest("select a provider profile before launching a session")
		}
		return nil
	}
	for _, name := range p.EnvAllow {
		if providerHomeEnvName(name) {
			return badRequest("env_allow may not name " + name + " for a profiled launch: the profile owns it")
		}
	}
	snap, policy, err := m.resolveLaunchProfile(ctx, tenant, p.ProviderProfileRef)
	if err != nil {
		return err
	}
	if err := applySessionPolicy(p, snap.Driver, policy); err != nil {
		return err
	}
	if snap.Driver == providerDriverClaude && p.Effort != "" && !validEffortLevels[p.Effort] {
		// The Claude enum applies to the Claude driver, and only once the SERVER has
		// resolved which driver this launch runs under.
		return badRequest("invalid effort (want low|medium|high|xhigh|max)")
	}
	if p.Transport == TransportRemoteControl && snap.Driver != providerDriverClaude {
		// `--remote-control` is a Claude Code flag with Claude Code's meaning (its
		// I/O is relayed to Anthropic's cloud). Accepting it for another provider
		// would be promising a lifecycle this driver has no way to produce.
		return badRequest("remote-control is a Claude Code transport and is not available for driver " + snap.Driver)
	}
	if err := validateOpenCodeEnvAllow(snap.Driver, p.EnvAllow); err != nil {
		return err
	}
	if snap.Driver == providerDriverOpenCode {
		if err := refuseOpenCodeUnsupportedControls(*p); err != nil {
			return err
		}
	}
	p.ProviderHome = &snap
	return nil
}

// validateProfiledInjectedEnv refuses a gate injection that names a variable the
// profile owns. The gate's env is otherwise authoritative over the host's; for a
// profiled launch the profile is authoritative over the gate for these two names,
// and the conflict is refused rather than ordered.
func validateProfiledInjectedEnv(env []EnvVar) error {
	for _, item := range env {
		if providerHomeEnvName(item.Name) {
			// A decision, not an outage: the launch is refused with the status the
			// other deny-closed verdicts carry, and denyClosedErr passes it through.
			return forbiddenErr("launch denied: the launch gate injected " + item.Name + ", which the provider profile owns")
		}
	}
	return nil
}

// setProfileSnapshot writes (or clears) the run row's profile snapshot columns.
func setProfileSnapshot(rec model.Record, snap *ProviderHomeSnapshot) {
	if snap == nil {
		rec[colRunProfileID] = nil
		rec[colRunProfileDriver] = nil
		rec[colRunProfileEnvRef] = nil
		rec[colRunProfileConfigHome] = nil
		rec[colRunProfileUserHome] = nil
		setOrNull(rec, colRunProviderAuthSource, "")
		setOrNull(rec, colRunProviderRecordRef, "")
		return
	}
	rec[colRunProfileID] = snap.ProfileID
	rec[colRunProfileDriver] = snap.Driver
	rec[colRunProfileEnvRef] = snap.EnvironmentRef
	rec[colRunProfileConfigHome] = snap.ConfigHome
	rec[colRunProfileUserHome] = snap.UserHome
	// The authorized source is persisted with the rest of the snapshot, BEFORE the
	// spawn, so what a run was launched under is durable rather than re-derived
	// from a profile row that may have been re-authorized since.
	setOrNull(rec, colRunProviderAuthSource, snap.AuthSource)
	// Persisted for the same reason and at the same moment: which registered
	// credential this run was launched under is a fact about the launch, not a
	// property of the profile row as it reads today.
	setOrNull(rec, colRunProviderRecordRef, snap.ProviderRecordRef)
}

// captureProfiledSessionID binds the provider's session id under the run's
// profile scope. It is the profiled twin of captureSessionID and differs in what
// "captured" means: the in-memory flag is set only after the transaction that
// wrote the scoped alias AND the run row committed. A store failure leaves the
// flag clear so a later frame retries idempotently; a scoped alias already bound
// to another canonical session is a reported discrepancy, never a rewrite, and
// never a capture.
func (m *Module) captureProfiledSessionID(ctx context.Context, lr *liveRun, sessionID string, at time.Time) {
	lr.mu.Lock()
	captured := lr.sessionIDCaptured
	lr.mu.Unlock()
	if captured {
		m.touchActivity(ctx, lr, at)
		return
	}
	err := m.withRegisteredProfiledRun(lr, func() error {
		m.captureRegisteredProfiledSessionID(ctx, lr, sessionID, at)
		return nil
	})
	if err == nil {
		lr.mu.Lock()
		captured = lr.sessionIDCaptured
		lr.mu.Unlock()
		if captured {
			// Credential ports may trigger teardown; the committed publication is
			// complete, so do not hold the registry pin across those callbacks.
			m.renewLaunchClaim(ctx, lr)
		}
	}
}

func (m *Module) captureRegisteredProfiledSessionID(ctx context.Context, lr *liveRun, sessionID string, at time.Time) {
	lr.mu.Lock()
	if lr.sessionIDCaptured {
		lr.mu.Unlock()
		return
	}
	lr.mu.Unlock()
	if sessionID == "" || lr.claim.SID == "" || lr.profile == nil {
		return
	}
	in := managedAliasInput{
		profileID: lr.profile.ProfileID, provider: lr.profile.Driver, externalID: sessionID,
		sid: lr.claim.SID, runRef: lr.runRef, launchID: lr.launchID, claimFence: lr.claim.Fence,
	}
	var managedSnap *liveSnapshot
	bind := func() error {
		managedSnap = nil
		return m.mutateProfiledRun(ctx, lr, func(sc store.Scope, _ model.Record) error {
			if err := m.bindManagedProviderAliasWithin(ctx, sc, in); err != nil {
				return err
			}
			// B2: the plane's own live row for this run, proven in the SAME
			// transaction as the alias. It is keyed by canonical sid, so a cooperative
			// observation that copies the external id lands elsewhere.
			managedRec, err := m.upsertManagedLive(ctx, sc, in, lr.profile.EnvironmentRef, at)
			if err != nil {
				return err
			}
			snap := m.snapshot(managedRec, lr.tenant)
			managedSnap = &snap
			// Activity moves in the same transaction: the frame that announced the id is
			// activity, and the write that proves the id is the natural place for it.
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			rec, err := findRunRec(ctx, repo, lr.runRef)
			if err != nil {
				return err
			}
			antes := rec.String(colLastActivityAt)
			rec[colLastActivityAt] = model.NewTimestamp(at).String()
			conservaElSelloMasNuevo(rec, antes)
			// The run→row half of the managed join, persisted with the proof (B2).
			rec[colRunLiveRef] = managedRec.String(model.ColID)
			_, err = repo.Update(ctx, rec)
			return err
		})
	}
	err := bind()
	if errors.Is(err, store.ErrConflict) {
		// The whole losing transaction rolled back; a fresh attempt re-reads the
		// committed state (the SG-00 pattern proven on Postgres). Either the same pair
		// is now bound (idempotent success) or the row moved on (a real refusal).
		err = bind()
	}
	switch {
	case err == nil:
		lr.mu.Lock()
		lr.sessionIDCaptured = true
		lr.lastActivityWrite = at
		lr.mu.Unlock()
		if managedSnap != nil {
			m.broker.publish(*managedSnap)
		}
	case errors.Is(err, ErrScopedAliasBound):
		lr.mu.Lock()
		already := lr.sessionIDConflictReported
		lr.sessionIDConflictReported = true
		lr.mu.Unlock()
		if !already {
			m.warnf("sessions: the provider session id already resolves to a DIFFERENT canonical session within this profile; the run stays controllable by its run_ref and its external link is unconfirmed",
				"run_ref", lr.runRef, "profile", lr.profile.ProfileID)
		}
	case isRunConflict(err):
		// A successor incarnation, a moved claim or a mismatched profile: this frame
		// carries no authority over the row and binds nothing. Not worth a warning per
		// frame — assertRuntimeIncarnation filters the ordinary case, the guard closes
		// the window inside the transaction.
		m.debugf("sessions: profiled session id not bound: the run row moved on", "run_ref", lr.runRef)
	default:
		// The store did not confirm. Memory stays "not captured" so a later frame can
		// retry; nothing here pretends the database holds proof it does not.
		m.warnf("sessions: could not bind the provider session id under the profile; will retry on a later frame",
			"run_ref", lr.runRef, "err", redactErr(err))
	}
}

// applySessionPolicy imposes the profile's DECLARED session policy on a launch
// (provider_profile_policy.go), and is the point at which "the product governs
// which profile launches and then narrows nothing" stops being true.
//
// Two rules, and both are decisions rather than conveniences:
//
//  1. THE TOOL SURFACE IS DENY-CLOSED. A profiled launch whose profile declares
//     nothing gets an EMPTY surface, which the Claude form emits as `--tools ""`.
//     The measured alternative is what the golden path found: 34 tools including
//     Bash, Write and Edit, handed to a child in a directory nobody chose.
//
//  2. THE DECLARED PERMISSION MODE DOES NOT OVERRIDE A TEMPLATE. A workspace
//     template's terms are approval-bound and re-resolved per launch; a profile is
//     an identity. Letting the identity rewrite an approved term would let a
//     profile WIDEN a restriction somebody approved, so the profile's mode applies
//     only to a launch that carries no template, and a template's own mode stands.
//
// A driver whose owned launch form cannot express a tool surface never receives
// one: the declaration is refused when it is MADE (validateSessionPolicyInput), so
// reaching here with one is a stored policy from before that check — it is
// dropped, loudly, rather than silently ignored.
func applySessionPolicy(p *CreateRunParams, driver string, policy sessionPolicy) error {
	if policy.PermissionMode != "" {
		if !validPermissionModes[policy.PermissionMode] {
			return &runErr{http.StatusUnprocessableEntity,
				"the provider profile declares a permission mode this runtime does not accept"}
		}
		if p.TemplateID == "" {
			p.PermissionMode = policy.PermissionMode
		}
	}
	if !driverExpressesToolSurface(driver) {
		if policy.ToolsDeclared {
			return &runErr{http.StatusUnprocessableEntity,
				"the provider profile declares a tool policy, and driver " + driver +
					" negotiates its tool surface in its own protocol: the launch is refused rather than started under a policy nobody applies"}
		}
		return nil
	}
	p.ToolSurface, p.ToolSurfaceDeclared = policy.effectiveTools(), true
	return nil
}

