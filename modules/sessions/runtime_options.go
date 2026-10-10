// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// Option configures the OPERATE runtime at module construction. The composition
// root (cmd/olivares) wires the concrete native/container runner, the WIF
// credential source and the governance gates. A nil argument is ignored so a
// partial wiring keeps the deny-closed/no-op default for that seam.

// Option mutates the module's runtime configuration.
type Option func(*Module)

// WithRunner wires the concrete session runner (native procRunner or a
// container/sandbox runner). Without it, every launch fails closed.
func WithRunner(r Runner) Option {
	return func(m *Module) {
		if r != nil {
			m.rt.Runner = r
		}
	}
}

// WithCredentialSource wires the secret-less inference credential source (a WIF
// exchange). Without it, a stream-json launch fails closed.
func WithCredentialSource(c CredentialSource) Option {
	return func(m *Module) {
		if c != nil {
			m.rt.Creds = c
		}
	}
}

// WithLaunchGate wires the PEP/budget/model-governance pre-flight. The
// default allows so the runtime works standalone.
func WithLaunchGate(g LaunchGate) Option {
	return func(m *Module) {
		if g != nil {
			m.rt.LaunchGate = g
		}
	}
}

// WithStopGate wires the kill-switch pre-flight. DENY-CLOSED BY
// CONTRACT: a wired gate that errors blocks the launch.
func WithStopGate(g StopGate) Option {
	return func(m *Module) {
		if g != nil {
			m.rt.StopGate = g
		}
	}
}

// WithRecorder wires governed I/O recording. The default records
// nothing.
func WithRecorder(rec Recorder) Option {
	return func(m *Module) {
		if rec != nil {
			m.rt.Recorder = rec
		}
	}
}

// WithClassifier wires the DLP classifier used on governed file READS.
// Without it, a `label`-mode read returns no sensitivity labels and a `deny`-mode read
// fails closed (it cannot prove the content safe). cmd/olivares wraps the
// catalog behind this seam; the module never imports security/knowledge.
func WithClassifier(c Classifier) Option {
	return func(m *Module) {
		if c != nil {
			m.rt.Classifier = c
		}
	}
}

// WithSessionWorkspaceRoot names the directory under which a session with NO
// registered workspace gets a working directory of its OWN (<root>/<run_ref>).
// The composition root derives it from the engine's data directory.
//
// Unwired, such a launch is REFUSED rather than started in the engine's own
// working directory. A run that names a registered workspace is unaffected and
// keeps using it.
//
// The value is validated here, not where it is used. A release removes a
// direct child of this directory recursively, so a root of "/" would make every
// top-level directory of the host a candidate. An Option cannot return an error,
// so a refused value leaves the node UNWIRED — every unworkspaced launch is then
// deny-closed and reports the reason and its remedy, which is the honest outcome
// for a wiring mistake. UseSessionWorkspaceRoot below says the same thing to a
// caller that can hear it.
func WithSessionWorkspaceRoot(dir string) Option {
	return func(m *Module) {
		_ = m.setSessionWorkspaceRoot(dir)
	}
}

// SessionWorkspaceRootConfigured reports whether this node can give a session
// with NO registered workspace a directory of its own. It is the composition
// root's observation seam: a boot that offered a root reports the state the
// module is actually IN — deny-closed or serving — instead of the value it hoped
// to wire.
func (m *Module) SessionWorkspaceRootConfigured() bool {
	return m.sessionWorkspaceRootConfigured()
}

// UseSessionWorkspaceRoot late-binds the same root, for the composition roots
// that resolve the data directory after the module is constructed. It returns
// the refusal (runtime_workspace_dir.go, validateSessionWorkspaceRoot) so a late
// binder learns immediately instead of at the first launch; an empty value is
// ignored and is not an error, so a partial wiring cannot erase a configured
// root by passing "".
func (m *Module) UseSessionWorkspaceRoot(dir string) error {
	return m.setSessionWorkspaceRoot(dir)
}

// WithSessionCostSink wires the port that posts a governed turn's cost to the
// tenant's spend ledger. Unwired, the run's own counters are still recorded and
// the module says once per session that its cost reaches no ledger.
func WithSessionCostSink(sink SessionCostSink) Option {
	return func(m *Module) {
		if sink != nil {
			m.rt.CostSink = sink
		}
	}
}

// WithListPricer wires the declared list prices a turn is priced at when its tool
// reported tokens and no money (runtime_usage.go). Unwired, such a turn's cost
// stays unknown.
func WithListPricer(p ListPricer) Option {
	return func(m *Module) {
		if p != nil {
			m.rt.ListPricer = p
		}
	}
}

// WithProgram overrides the launched executable (default "claude"); used by tests
// to inject a fake claude binary.
func WithProgram(program string) Option {
	return func(m *Module) {
		if program != "" {
			m.rt.program = program
			m.rt.programPinned = true
		}
	}
}

// WithClaudeHookPEP gives the runtime what ConfigureClaudeHookPEP needs to write a
// Claude session's hook settings: the engine's data directory and the absolute
// path of the olivares binary its hooks call.
func WithClaudeHookPEP(dataDir, olivaresBinary string) Option {
	return func(m *Module) {
		m.rt.hookDataDir, m.rt.hookBinary = dataDir, olivaresBinary
	}
}

// WithProgramResolver finds a driver's installed executable at launch time when
// no explicit program is pinned for it (WithProgram, WithDriverProgram): the
// engine's newest verified managed install, then its PATH. An empty answer keeps
// the official program name, which the runner's own inspection reports as
// missing with the action that installs it.
func WithProgramResolver(resolve func(driver string) string) Option {
	return func(m *Module) {
		m.rt.ProgramResolver = resolve
	}
}

// WithProviderDriver registers an internal provider driver. Registration is what
// makes that driver's profiles LAUNCHABLE on this node — readiness is per driver,
// and there is deliberately no single flag that turns several on at once. An
// invalid or duplicate registration panics at construction because a half-wired
// driver would be discovered as a failed launch instead of a failed boot.
func WithProviderDriver(d ProviderDriver) Option {
	return func(m *Module) {
		if d == nil {
			return
		}
		if err := m.rt.registerDriver(d); err != nil {
			panic(err)
		}
	}
}

// WithDriverProgram overrides the executable a registered driver spawns (the
// operator's pinned official binary). Absent, the driver's own program name is
// used and resolved through PATH.
func WithDriverProgram(driver, program string) Option {
	return func(m *Module) {
		key, err := normalizeDriverKey(driver)
		if err != nil || program == "" {
			return
		}
		m.rt.driverPrograms[key] = program
	}
}

// WithProviderCredentialSource wires the GOVERNED managed-injection adapter of one
// driver. There is no default and no shared adapter: a managed launch of a driver
// with no adapter is refused by name, never served by another provider's issuer.
//
// Deprecated: no engine composition wires a managed-injection adapter; a managed launch
// takes its credential from a provider record. It still works and is kept until a later
// release removes it.
func WithProviderCredentialSource(driver string, src ProviderCredentialSource) Option {
	return func(m *Module) {
		key, err := normalizeDriverKey(driver)
		if err != nil || src == nil {
			return
		}
		m.rt.providerCreds[key] = src
	}
}

// WithProviderApprovalGate wires the authority that answers provider approval
// requests. The default is DENY-CLOSED: every approval is refused with its own
// method's refusal codec.
func WithProviderApprovalGate(g ProviderApprovalGate) Option {
	return func(m *Module) {
		if g != nil {
			m.rt.ApprovalGate = g
		}
	}
}

// WithProductVersion sets the version Olivares presents to an official CLI in its
// handshake. The composition root supplies the build's value; the module invents
// none and never reports a version it cannot source.
func WithProductVersion(v string) Option {
	return func(m *Module) {
		if v != "" {
			m.rt.productVersion = v
		}
	}
}

// WithDriverTimeouts bounds one client→server protocol request and one
// server→client approval. Both must be bounded: an unanswered request stalls a
// provider turn, and an unbounded wait turns that into a stalled launch.
func WithDriverTimeouts(call, approval time.Duration) Option {
	return func(m *Module) {
		if call > 0 {
			m.rt.driverCallTimeout = call
		}
		if approval > 0 {
			m.rt.driverApprovalDeadline = approval
		}
	}
}

// UseProviderCredentialSource late-binds one driver's governed managed adapter.
//
// Deprecated: no engine composition wires a managed-injection adapter; a managed launch
// takes its credential from a provider record. It still works and is kept until a later
// release removes it.
func (m *Module) UseProviderCredentialSource(driver string, src ProviderCredentialSource) error {
	key, err := normalizeDriverKey(driver)
	if err != nil {
		return err
	}
	if src == nil {
		return nil
	}
	m.rt.providerCreds[key] = src
	return nil
}

// OperableProviderDrivers reports the drivers this node can launch, beside the
// historical Claude path. It exists so a boot log and an operator can read the
// per-driver readiness instead of inferring it.
func (m *Module) OperableProviderDrivers() []string {
	out := make([]string, 0, len(m.rt.drivers))
	for key := range m.rt.drivers {
		out = append(out, key)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// WithInferenceBaseURL sets ANTHROPIC_BASE_URL on launched sessions so their
// inference routes through Olivares' own gateway (PEP/budget/model governance).
func WithInferenceBaseURL(url string) Option {
	return func(m *Module) { m.rt.baseURL = url }
}

// WithRuntimeIdleWindow overrides the window after which a quiet running session
// is DERIVED as idle.
func WithRuntimeIdleWindow(d time.Duration) Option {
	return func(m *Module) {
		if d > 0 {
			m.rt.idleWindow = d
		}
	}
}

// WithStopWaitDelay bounds a graceful stop before SIGKILL escalation.
func WithStopWaitDelay(d time.Duration) Option {
	return func(m *Module) {
		if d > 0 {
			m.rt.waitDelay = d
		}
	}
}

// WithRuntimeCredentialHeartbeatInterval overrides the independent runtime liveness
// heartbeat. Production defaults below half the Claim TTL; tests use a short
// interval to prove a silent process is fenced without emitting stdout.
func WithRuntimeCredentialHeartbeatInterval(d time.Duration) Option {
	return func(m *Module) {
		if d > 0 {
			m.rt.credentialHeartbeatInterval = d
		}
	}
}

// WithKillSwitchSweep enables the active kill-switch termination sweep: every
// interval the runtime re-checks live runs against the StopGate and terminates any now
// under an emergency stop (the "para" half of the estate kill-switch — sessions is the
// only module that owns its long-running process, so it can stop running work, not just
// block new launches). 0 (the default) disables it.
func WithKillSwitchSweep(interval time.Duration) Option {
	return func(m *Module) {
		if interval > 0 {
			m.rt.stopSweepInterval = interval
		}
	}
}

// WithClock overrides the module clock (tests inject a controllable clock to
// drive the derived idle state deterministically).
func WithClock(c model.Clock) Option {
	return func(m *Module) {
		if c != nil {
			m.clock = c
		}
	}
}

// WithWorkIdentityResolver wires the authoritative participant lookup used by
// the durable work kernel. Without it owner-bearing commands fail closed.
func WithWorkIdentityResolver(r WorkIdentityResolver) Option {
	return func(m *Module) {
		if r != nil {
			m.WorkIdentity = r
		}
	}
}

// WithWorkContentGuard wires secret/content inspection before durable content
// is written. An absent guard is not equivalent to an allow decision.
func WithWorkContentGuard(g WorkContentGuard) Option {
	return func(m *Module) {
		if g != nil {
			m.WorkContent = g
		}
	}
}

// WithWorkEventSink wires durable Eventing ingestion. A nil sink leaves the
// sessions outbox pending for a later boot rather than discarding an event.
func WithWorkEventSink(s WorkEventSink) Option {
	return func(m *Module) {
		if s != nil {
			m.WorkEventSink = s
		}
	}
}

// WithWorkAuthorizer wires command-dependent authorization for shared routes.
// Without it an admin-only command fails closed unless an in-process caller
// supplies an already-authorized WorkPrincipal.
func WithWorkAuthorizer(a WorkAuthorizer) Option {
	return func(m *Module) {
		if a != nil {
			m.WorkAuthorizer = a
		}
	}
}

// EnableCommunicationSessionCredentials activates the indivisible dual runtime
// posture. Once enabled, a missing issuer is a 503; it never falls back to work
// only. Standalone tests and the eventual communication rollout gate call this explicitly.
func (m *Module) EnableCommunicationSessionCredentials() {
	m.rt.CommunicationCredentialsEnabled = true
}

// CommunicationSessionCredentialsEnabled reports the boot-time rollout state
// so composition can avoid a cross-tenant recovery ceremony while communication is OFF.
func (m *Module) CommunicationSessionCredentialsEnabled() bool {
	return m.rt.CommunicationCredentialsEnabled
}

// ---------------------------------------------------------------------------
// Late-binding. The composition root constructs the sessions module FIRST
// (its live read-model backs the evals monitor + sandbox replay), but the
// governance dependencies (FinOps, the approval bridge, the audit store) are
// constructed AFTER it — so the governance gates are late-bound here, exactly like
// gov.UseLifecycleGate / evBudget.bind elsewhere. A nil argument is ignored so a
// partial wiring keeps the deny-closed/no-op default for that seam. These run before
// Start (single-threaded boot), so they need no lock.
// ---------------------------------------------------------------------------

// EnableProfiledLaunches is retained for compatibility and has no effect.
//
// Deprecated: Profiled launches are always enabled.
func (m *Module) EnableProfiledLaunches() {}

// ProfiledLaunchesEnabled reports that profiled launches are always enabled.
//
// Deprecated: Profiled launches are always enabled.
func (m *Module) ProfiledLaunchesEnabled() bool { return true }

// WithProviderSecretVault wires the port that seals provider-record credentials
// outside this module's partition (v26.10). Unwired, registering a provider is
// refused with the wiring named: the engine never stores a credential in the
// clear, and "I have nowhere safe to put this" is an answer, not a failure.
func WithProviderSecretVault(v ProviderSecretVault) Option {
	return func(m *Module) {
		if v != nil {
			m.rt.ProviderVault = v
		}
	}
}

// WithProviderProbe wires the non-spending connection test (v26.10). Unwired, the
// test is refused and says that launching is unaffected.
func WithProviderProbe(p ProviderProbe) Option {
	return func(m *Module) {
		if p != nil {
			m.rt.ProviderProbe = p
		}
	}
}

// ProviderVaultWired reports whether a sealing port is available, so a console or
// a CLI can present an honest "provider registration is disabled on this
// deployment" posture instead of discovering it one refused request at a time.
func (m *Module) ProviderVaultWired() bool { return m.rt.ProviderVault != nil }

// ProviderProbeWired reports whether the connection test is available.
func (m *Module) ProviderProbeWired() bool { return m.rt.ProviderProbe != nil }
