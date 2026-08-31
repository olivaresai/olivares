// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Option configures the OPERATE runtime at module construction. The composition
// root (cmd/olivares) wires the concrete native/container runner and the WIF
// credential source wires the governance gates. A nil argument is ignored
// so a partial wiring keeps the deny-closed/no-op default for that seam.

// Option mutates the module's runtime configuration.
type Option func(*Module)

// WithRunner wires the concrete session runner (native procRunner or a
// container/sandbox runner). Without it, every launch fails closed.
func WithRunner(r Runner) Option {
	return func(m *Module) {
		if r != nil {
			m.rt.runner = r
		}
	}
}

// WithCredentialSource wires the secret-less inference credential source (a WIF
// exchange). Without it, a stream-json launch fails closed.
func WithCredentialSource(c CredentialSource) Option {
	return func(m *Module) {
		if c != nil {
			m.rt.creds = c
		}
	}
}

// WithLaunchGate wires the PEP/budget/model-governance pre-flight. The
// default allows so the runtime works standalone.
func WithLaunchGate(g LaunchGate) Option {
	return func(m *Module) {
		if g != nil {
			m.rt.launchGate = g
		}
	}
}

// WithStopGate wires the kill-switch pre-flight. DENY-CLOSED BY
// CONTRACT: a wired gate that errors blocks the launch.
func WithStopGate(g StopGate) Option {
	return func(m *Module) {
		if g != nil {
			m.rt.stopGate = g
		}
	}
}

// WithRecorder wires governed I/O recording. The default records
// nothing.
func WithRecorder(rec Recorder) Option {
	return func(m *Module) {
		if rec != nil {
			m.rt.recorder = rec
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
			m.rt.classifier = c
		}
	}
}

// WithProgram overrides the launched executable (default "claude"); used by tests
// to inject a fake claude binary.
func WithProgram(program string) Option {
	return func(m *Module) {
		if program != "" {
			m.rt.program = program
		}
	}
}

// WithInferenceBaseURL sets ANTHROPIC_BASE_URL on launched sessions so their
// inference routes through Olivares' own gateway (PEP/budget/model-gov).
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

// WithRuntimeCredentialHeartbeatInterval overrides the independent K3 liveness
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
			m.workIdentity = r
		}
	}
}

// WithWorkContentGuard wires secret/content inspection before durable content
// is written. An absent guard is not equivalent to an allow decision.
func WithWorkContentGuard(g WorkContentGuard) Option {
	return func(m *Module) {
		if g != nil {
			m.workContent = g
		}
	}
}

// WithWorkEventSink wires durable Eventing ingestion. A nil sink leaves the
// sessions outbox pending for a later boot rather than discarding an event.
func WithWorkEventSink(s WorkEventSink) Option {
	return func(m *Module) {
		if s != nil {
			m.workEventSink = s
		}
	}
}

// WithWorkAuthorizer wires command-dependent authorization for shared routes.
// Without it an admin-only command fails closed unless an in-process caller
// supplies an already-authorized WorkPrincipal.
func WithWorkAuthorizer(a WorkAuthorizer) Option {
	return func(m *Module) {
		if a != nil {
			m.workAuthz = a
		}
	}
}

// UseWorkSessionCredentialSource late-binds the core/auth-backed issuer after
// the store and Authenticator exist. Production calls it before Module.Start.
// A nil source preserves the historical runtime, which cannot act as a kernel
// holder; once wired, an issuance failure denies the launch.
func (m *Module) UseWorkSessionCredentialSource(source WorkSessionCredentialSource) {
	if source != nil {
		m.rt.workSessionCreds = source
	}
}

// UseCommunicationSessionCredentialSource late-binds the dedicated K3 issuer.
// Binding is deliberately separate from activation: G can land before the E/F
// store and WP2 route surface, while the product remains OFF and emits no inert
// bearer. The rollout owner calls EnableCommunicationSessionCredentials only
// when the complete K3 posture is effective.
func (m *Module) UseCommunicationSessionCredentialSource(source CommunicationSessionCredentialSource) {
	m.rt.communicationSessionCreds = source
}

// EnableCommunicationSessionCredentials activates the indivisible dual runtime
// posture. Once enabled, a missing issuer is a 503; it never falls back to work
// only. Standalone tests and the eventual K3 rollout gate call this explicitly.
func (m *Module) EnableCommunicationSessionCredentials() {
	m.rt.communicationCredentialsEnabled = true
}

// CommunicationSessionCredentialsEnabled reports the boot-time rollout state
// so composition can avoid a cross-tenant recovery ceremony while K3 is OFF.
func (m *Module) CommunicationSessionCredentialsEnabled() bool {
	return m.rt.communicationCredentialsEnabled
}

// UseCommunicationContentSealer late-binds the dedicated K3 payload sealer.
// Nil or a typed nil deliberately removes the witness and leaves communication
// OFF.
func (m *Module) UseCommunicationContentSealer(sealer CommunicationContentSealer) {
	if !communicationPortBound(sealer) {
		m.communicationSealer = nil
		return
	}
	m.communicationSealer = sealer
}

// UseCommunicationRequestAuthority late-binds the exact credential resolver
// and the deployment's composed authorizer as one indivisible pair. This
// preparatory seam does not make the legacy service authorizers ready and does
// not activate K3. Composition must call it before Module.Start; concurrent
// runtime rebind is outside this seam's contract.
func (m *Module) UseCommunicationRequestAuthority(
	resolver *auth.Authenticator,
	source *auth.Authorizer,
) {
	m.useCommunicationRequestAuthoritySources(resolver, source)
}

// UseCommunicationDirectorySnapshotResolver late-binds the authoritative,
// tri-state core-directory resolver used by publication and protected reads.
func (m *Module) UseCommunicationDirectorySnapshotResolver(resolver DirectorySnapshotResolver) {
	m.communicationDirectoryResolver = resolver
}

// UseCommunicationPublicationAudienceAttestor late-binds the resolver that
// combines the core directory snapshot with current K3 subscriptions/routes.
func (m *Module) UseCommunicationPublicationAudienceAttestor(attestor PublicationAudienceAttestor) {
	m.communicationAudienceAttestor = attestor
}

// UseCommunicationChannelGrantSubjectClosureResolver late-binds the server-side
// direct/group/session subject closure. A request body never supplies it.
func (m *Module) UseCommunicationChannelGrantSubjectClosureResolver(
	resolver ChannelGrantSubjectClosureResolver,
) {
	m.communicationGrantClosure = resolver
}

// UseCommunicationCoreEntityReadAuthorizer late-binds the core half of C5.
func (m *Module) UseCommunicationCoreEntityReadAuthorizer(authorizer CoreEntityReadAuthorizer) {
	m.communicationReadAuthorizer = authorizer
}

// UseCommunicationCoreEntityOperationAuthorizer late-binds action-specific
// core authority for Ack/seen/send/handoff response and other K3 writes.
func (m *Module) UseCommunicationCoreEntityOperationAuthorizer(
	authorizer CoreEntityOperationAuthorizer,
) {
	m.communicationOperationAuthorizer = authorizer
}

// UseCommunicationGuardReconciliationData late-binds the narrow tenant/workspace
// bootstrap handle used by the composition root during leadership promotion.
// The handle retains closures only and exposes exactly guard/channel/delivery
// repositories inside one workspace transaction; it cannot surface store.Scope.
func (m *Module) UseCommunicationGuardReconciliationData(
	data *CommunicationGuardReconciliationData,
) {
	if data == nil {
		m.communicationGuardData = nil
		return
	}
	m.communicationGuardData = data
}

// UseCommunicationStoreReadinessWitness late-binds the composition-root proof
// that the complete K3 store phase is ready. The module does not inspect a
// Store or infer readiness from one available repository.
func (m *Module) UseCommunicationStoreReadinessWitness(witness CommunicationStoreReadinessWitness) {
	m.communicationStoreReadiness = witness
}

// UseCommunicationPumpReadinessWitness late-binds WP-3's pump proof. WP-2
// production intentionally leaves it nil, keeping the effective K3 gate OFF.
func (m *Module) UseCommunicationPumpReadinessWitness(witness CommunicationPumpReadinessWitness) {
	m.communicationPumpReadiness = witness
}

// UseRuntimeCredentialRecoverySources binds revocation-only adapters backed by
// the residency-guarded store before service suspension. Recover is their sole
// caller; normal mint/renew/revoke continues through the ordinary adapters.
func (m *Module) UseRuntimeCredentialRecoverySources(
	work WorkSessionCredentialSource,
	communication CommunicationSessionCredentialSource,
) {
	m.rt.recoveryWorkSessionCreds = work
	m.rt.recoveryCommunicationSessionCreds = communication
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

// UseLaunchGate late-binds the PEP/budget/HITL launch gate.
func (m *Module) UseLaunchGate(g LaunchGate) {
	if g != nil {
		m.rt.launchGate = g
	}
}

// UseStopGate late-binds the kill-switch pre-flight. DENY-CLOSED BY CONTRACT.
func (m *Module) UseStopGate(g StopGate) {
	if g != nil {
		m.rt.stopGate = g
	}
}

// UseRecorder late-binds the governed I/O recorder.
func (m *Module) UseRecorder(r Recorder) {
	if r != nil {
		m.rt.recorder = r
	}
}

// UseKillSwitchSweep late-binds the active-termination sweep interval; 0 keeps
// it disabled.
func (m *Module) UseKillSwitchSweep(interval time.Duration) {
	if interval > 0 {
		m.rt.stopSweepInterval = interval
	}
}

// UseWorkIdentityResolver late-binds identity after the backing store exists.
func (m *Module) UseWorkIdentityResolver(r WorkIdentityResolver) {
	if r != nil {
		m.workIdentity = r
	}
}

// UseWorkContentGuard late-binds the content scanner during single-threaded boot.
func (m *Module) UseWorkContentGuard(g WorkContentGuard) {
	if g != nil {
		m.workContent = g
	}
}

// UseWorkEventSink late-binds Eventing before module Start.
func (m *Module) UseWorkEventSink(s WorkEventSink) {
	if s != nil {
		m.workEventSink = s
	}
}

// UseWorkAuthorizer late-binds the composed RBAC+policy authorizer at boot.
func (m *Module) UseWorkAuthorizer(a WorkAuthorizer) {
	if a != nil {
		m.workAuthz = a
	}
}
