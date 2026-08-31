// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// This file declares the OPERATE seams for — the governed Claude Code
// session runtime (FASE V). They live in modules/sessions (module II), which
// Chose to EXTEND rather than fork into a new module: the observe overlay
// (sessions.live/timeline) and the operate runtime share the "sessions" plane.
//
// Every seam is declared in the module's OWN terms and defaults DENY-CLOSED for
// the load-bearing ones (Runner, CredentialSource — without them nothing
// launches) and permissive-no-op for the additive governance gates
// (LaunchGate/StopGate/Recorder), exactly as the rest of the product does
// (deploy.StopGate, the budget gates): the runtime works standalone, and the
// composition root late-binds the real PEP/budget/kill-switch/recording adapters from the
// composition root WITHOUT this module ever importing governance.

// ---------------------------------------------------------------------------
// Vocabulary: transport, isolation, state.
// ---------------------------------------------------------------------------

// Transport is how Olivares drives the launched `claude` process.
type Transport string

const (
	// TransportStreamJSON is the GOVERNED default: `claude --input-format
	// stream-json --output-format stream-json`, a bidirectional NDJSON channel
	// over stdin/stdout that Olivares bridges and governs in full.
	TransportStreamJSON Transport = "stream-json"
	// TransportRemoteControl is LIFECYCLE-ONLY: `claude --remote-control` relays
	// its I/O to Anthropic's cloud (claude.ai / mobile), so Olivares manages the
	// process lifecycle but cannot bridge the I/O (honestly declared, never faked).
	TransportRemoteControl Transport = "remote-control"
)

// ValidTransport reports whether t is a supported transport.
func ValidTransport(t Transport) bool {
	return t == TransportStreamJSON || t == TransportRemoteControl
}

// Isolation is the launched process's containment posture.
type Isolation string

const (
	// IsolationNative runs `claude` as a host process (the v1 default, fully
	// implemented and tested's choice for self-hosted single-tenant).
	IsolationNative Isolation = "native"
	// IsolationContainer is a FORWARD-COMPAT seam value: a per-session hardened
	// container via core/runtime/executor with a docker attach/hijack stdio bridge.
	// It is NOT wired this release — Chose the socket-free combined-image +
	// native procRunner; the container Runner is a documented follow-up. The native
	// runner REFUSES this value rather than run unisolated (procrunner.go).
	IsolationContainer Isolation = "container"
	// IsolationSandbox is a FORWARD-COMPAT seam value (gVisor/Firecracker + egress
	// control via core/runtime/sandboxrt). NOT wired this release — same as container.
	IsolationSandbox Isolation = "sandbox"
)

// ValidIsolation reports whether i is a supported isolation posture.
func ValidIsolation(i Isolation) bool {
	return i == IsolationNative || i == IsolationContainer || i == IsolationSandbox
}

// Run lifecycle states (the stored state machine — the OPPOSITE of the observe
// overlay, which stores no state because the cooperative stream carries no
// end/failure signal; here the runtime OWNS the process so it knows).
const (
	statePending = "pending" // row created/resume reserved, process not yet started
	stateRunning = "running" // process alive, I/O flowing
	stateIdle    = "idle"    // alive but no activity within the idle window (derived)
	stateStopped = "stopped" // terminated by request or clean exit; Claude transcript persists → resumable
	stateFailed  = "failed"  // process died non-zero or launch failed
	stateCleaned = "cleaned" // Claude session state released → not resumable
)

// Permission modes accepted by Claude Code's --permission-mode (verified against
// the deployed claude binary, 2026-06-16: default|acceptEdits|plan|auto|dontAsk|
// bypassPermissions). bypassPermissions is accepted but the container/sandbox
// boundary — not the flag — is the real safety frontier under it.
var validPermissionModes = map[string]bool{
	"default": true, "acceptEdits": true, "plan": true,
	"auto": true, "dontAsk": true, "bypassPermissions": true,
}

// Effort levels accepted by --effort (GA set; matches connectors/claude-api
// OutputConfig.Effort). "ultracode" is a session-orchestration mode, NOT an
// --effort value, so it is deliberately absent.
var validEffortLevels = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// ---------------------------------------------------------------------------
// Runner — the injectable process launcher (DENY-CLOSED default).
// ---------------------------------------------------------------------------

// errNoRunner is the deny-closed sentinel: no runner is wired, so no session may
// launch. The composition root wires a concrete runner (native procRunner, or a
// container runner over core/runtime/executor); absent it, every launch fails
// closed rather than pretending a session started.
var errNoRunner = errors.New("sessions: no session runner wired; launch denied")

// EnvVar is one explicit environment entry for the launched process. The value
// is held in memory for the launch and discarded; only a credential's
// NON-SENSITIVE id is ever persisted (never the token).
type EnvVar struct {
	Name  string
	Value string
}

// LaunchSpec is the neutral, transport-agnostic launch request the module builds
// from a run's parameters and hands to the Runner. The module owns argv
// construction (a proper []string, never a shell-split string); the Runner only
// spawns and bridges.
type LaunchSpec struct {
	// Program is the executable ("claude").
	Program string
	// Args is the fully-formed argv (no shell, no quoting hazards).
	Args []string
	// Dir is the working directory (the workspace).
	Dir string
	// Env is the EXPLICIT environment (the governed token by value) the runner sets
	// on the child; it is authoritative over any host value.
	Env []EnvVar
	// EnvAllow is the operator-chosen ALLOWLIST of host environment variable NAMES
	// to pass through to the child, ON TOP of the runner's minimal safe base. Nothing
	// else is inherited — the control-plane's OLIVARES_* secrets never reach the child.
	EnvAllow []string
	// Isolation is the containment posture.
	Isolation Isolation
	// WaitDelay bounds how long a graceful stop waits before SIGKILL.
	WaitDelay time.Duration
	// Workspace is the RESOLVED, governed workspace mount. It is nil for a
	// session with no workspace and for the NATIVE runner (which uses Dir directly).
	// A container/sandbox runner binds Workspace.HostPath at
	// Workspace.ContainerTarget (read-only when Workspace.ReadOnly), with the rest of
	// the container hardened read-only. The HostPath is already canonicalized/jailed
	// by the module — the runner never resolves a path.
	Workspace *WorkspaceMount
}

// WorkspaceMount is one resolved workspace bind for a containerized launch.
// HostPath is the canonical host root, ContainerTarget is where it mounts inside the
// container, and ReadOnly reflects the workspace's mount_mode. The native runner
// ignores it (it uses LaunchSpec.Dir)'s container runner consumes it.
type WorkspaceMount struct {
	HostPath        string
	ContainerTarget string
	ReadOnly        bool
}

// OutputFrame is one chunk of the process's output stream, tagged with which
// stream it came from. For stream-json the Data is one NDJSON line.
type OutputFrame struct {
	// Stream is "stdout" or "stderr".
	Stream string
	// Data is the raw bytes of the frame (one line for stdout NDJSON).
	Data []byte
}

const (
	streamStdout = "stdout"
	streamStderr = "stderr"
)

// Process is a launched, live `claude` process. It is the long-running,
// streamed handle that core/runtime/executor (detached reconcile) and
// core/runtime/sandboxrt (one-shot capture) deliberately do NOT provide — this
// is the net-new surface adds, adapted from the connectors/mcp stdio
// transport pattern.
type Process interface {
	// Send writes one line to the process's stdin (a stream-json user/control
	// message). It is a no-op error for a transport with no bridged input.
	Send(ctx context.Context, line []byte) error
	// Output returns the channel of output frames; it is closed when the process
	// exits and the output pumps drain.
	Output() <-chan OutputFrame
	// Wait blocks until the process exits and returns its exit code.
	Wait() (exitCode int, err error)
	// Stop gracefully terminates the process group (close stdin, SIGTERM, then
	// SIGKILL after WaitDelay) so grandchildren holding the pipe cannot wedge it.
	Stop(ctx context.Context) error
	// PID is the non-sensitive process id (0 when not applicable, e.g. a fake).
	PID() int
}

// Runner launches a `claude` process and returns a live handle. The default
// (unwiredRunner) denies; the composition root wires a real one.
type Runner interface {
	Launch(ctx context.Context, spec LaunchSpec) (Process, error)
}

// unwiredRunner is the deny-closed default.
type unwiredRunner struct{}

func (unwiredRunner) Launch(context.Context, LaunchSpec) (Process, error) {
	return nil, errNoRunner
}

// ---------------------------------------------------------------------------
// CredentialSource — the secret-less inference credential (DENY-CLOSED default).
// ---------------------------------------------------------------------------

// errNoCredential is the deny-closed sentinel: no credential source is wired, so
// a stream-json launch (which needs a governed inference credential) fails
// closed. There is no default and no long-lived static key anywhere here.
var errNoCredential = errors.New("sessions: no inference credential source wired; launch denied (no static key)")

// CredentialRequest scopes a mint to one launch.
type CredentialRequest struct {
	Tenant    model.TenantID
	RunRef    string
	Transport Transport
}

// Credential is a SHORT-LIVED inference credential. Token is the bearer material
// (injected as ANTHROPIC_AUTH_TOKEN, used, discarded — NEVER persisted/logged);
// ID and Scheme are non-sensitive and the only parts that reach the ledger.
type Credential struct {
	ID       string
	Token    string
	Scheme   string
	NotAfter time.Time
}

// Expired reports whether the credential is past its lifetime (a zero NotAfter is
// treated as expired — fail-closed, mirroring executor.Credential).
func (c Credential) Expired(now time.Time) bool {
	return c.NotAfter.IsZero() || !now.Before(c.NotAfter)
}

// CredentialSource mints a short-lived inference credential at launch time. The
// production source is a WIF/OIDC exchange (connectors/claude-wif) wired in
// cmd/olivares; a source that cannot attest MUST return an error, never a
// default credential.
type CredentialSource interface {
	Mint(ctx context.Context, req CredentialRequest) (Credential, error)
}

// denyCredentialSource is the deny-closed default.
type denyCredentialSource struct{}

func (denyCredentialSource) Mint(context.Context, CredentialRequest) (Credential, error) {
	return Credential{}, errNoCredential
}

// CredentialSourceFunc adapts a function to a CredentialSource (the cmd/olivares
// adapter wraps claude-wif; tests inject a short-lived mock).
type CredentialSourceFunc func(ctx context.Context, req CredentialRequest) (Credential, error)

// Mint calls the wrapped function.
func (f CredentialSourceFunc) Mint(ctx context.Context, req CredentialRequest) (Credential, error) {
	return f(ctx, req)
}

// WorkSessionCredentialRequest is the server-proven launch identity used to
// mint an Olivares API bearer for the exact canonical SID. The module calls this
// only after admission acquired the live Claim.
type WorkSessionCredentialRequest struct {
	Tenant     model.TenantID
	SessionRef string
	RunRef     string
	AgentRef   string
	ClaimFence int64
}

// WorkSessionCredential is a short-lived, purpose-restricted kernel bearer.
// ID is a non-sensitive revocation handle; Token is held only in LaunchSpec.
type WorkSessionCredential struct {
	ID         model.ID
	Token      string
	Tenant     model.TenantID
	SessionRef string
	RunRef     string
	AgentRef   string
	ClaimFence int64
	NotAfter   time.Time
}

// Expired reports whether the credential has any usable lifetime.
func (c WorkSessionCredential) Expired(now time.Time) bool {
	return c.NotAfter.IsZero() || !now.Before(c.NotAfter)
}

// WorkSessionCredentialSource is implemented by the composition root over
// core/auth. Mint and Revoke are deliberately paired so process death removes
// authority immediately; token expiry is the crash/outage backstop.
type WorkSessionCredentialSource interface {
	Mint(context.Context, WorkSessionCredentialRequest) (WorkSessionCredential, error)
	Renew(context.Context, model.ID, WorkSessionCredentialRequest) (time.Time, error)
	Revoke(context.Context, model.ID, WorkSessionCredentialRequest) error
}

// CommunicationSessionCredentialRequest is the complete server-proven binding
// for the second runtime bearer. WorkspaceID is the core authorization
// workspace resolved from the canonical session identity; it is deliberately
// unrelated to WorkspaceRef, which names a filesystem mount.
type CommunicationSessionCredentialRequest struct {
	Tenant      model.TenantID
	WorkspaceID model.ID
	SessionRef  string
	RunRef      string
	AgentRef    string
	ClaimFence  int64
}

// CommunicationSessionCredential is the purpose-restricted bearer used only by
// an admitted session process to reach the K3 communication surface. Token is
// show-once launch material; ID and NotAfter are non-sensitive recovery data.
type CommunicationSessionCredential struct {
	ID          model.ID
	Token       string
	Tenant      model.TenantID
	WorkspaceID model.ID
	SessionRef  string
	RunRef      string
	AgentRef    string
	ClaimFence  int64
	NotAfter    time.Time
}

// Expired reports whether the credential has any usable lifetime.
func (c CommunicationSessionCredential) Expired(now time.Time) bool {
	return c.NotAfter.IsZero() || !now.Before(c.NotAfter)
}

// CommunicationSessionCredentialSource is the narrow composition seam over
// core/auth's communication-session issuer. It cannot mint a general role token
// or widen the credential's immutable four-permission ceiling.
type CommunicationSessionCredentialSource interface {
	Mint(context.Context, CommunicationSessionCredentialRequest) (CommunicationSessionCredential, error)
	Renew(context.Context, model.ID, CommunicationSessionCredentialRequest) (time.Time, error)
	Revoke(context.Context, model.ID, CommunicationSessionCredentialRequest) error
}

// ---------------------------------------------------------------------------
// LaunchGate — the PEP/budget/model-governance pre-flight (additive).
// ---------------------------------------------------------------------------

// LaunchAction is what the gate is consulted for.
type LaunchAction string

const (
	// LaunchActionCreate gates a fresh launch.
	LaunchActionCreate LaunchAction = "create"
	// LaunchActionResume gates a resume of a stopped session.
	LaunchActionResume LaunchAction = "resume"
)

// LaunchIntent is the references-only attribution the launch gate scopes on. It
// carries NO secrets/payloads (docs/SECURITY-HARDENING.md): only the actor/agent references the
// budget/HITL/kill-switch decisions key on and the launch parameters the CRITICAL
// determination reads. The workspace flags are derived by the module from the
// RESOLVED workspace so the gate need not re-read the workspace table.
type LaunchIntent struct {
	Action         LaunchAction
	RunRef         string
	Transport      Transport
	PermissionMode string
	Model          string
	WorkspaceRef   string
	// Actor / ActorKind is the RBAC principal that requested the launch (the audit
	// actor + the budget identity + the HITL requester).
	Actor     string
	ActorKind string
	// AgentRef is the agent dimension the kill-switch / budget scope on (the actor
	// when it is an agent NHI; empty ⇒ only the estate graduation applies).
	AgentRef string
	// WorkspaceClassified is true when the resolved workspace declares a DLP posture
	// (dlp_mode != off) — i.e. it is marked as holding classifiable/sensitive content.
	WorkspaceClassified bool
	// WorkspaceReadWrite is true when the resolved workspace mounts read-write.
	// Classified && read-write is a CRITICAL launch signal (2026-06-16).
	WorkspaceReadWrite bool
	// RecordRequested is the operator's per-launch opt-in to full I/O recording for a
	// non-CRITICAL session (a CRITICAL session is recorded regardless).
	RecordRequested bool

	// TemplateRef / TemplateVersion / AllowedTools are the workspace template's governed
	// terms as MERGED by the module before this gate ran. They are here so a gate
	// that opens a human approval can bind it to what was approved: a template is mutable
	// and is re-read on every launch, so an approval keyed only on transport/mode/model/
	// workspace could be opened for one tool allowlist and spent on a wider one. References
	// and a version — never the template body, never instructions (minimal-data, docs/SECURITY-HARDENING.md).
	TemplateRef     string
	TemplateVersion int64
	AllowedTools    []string

	// --- SG-02-b: the admission plane's three references. ---
	//
	// A launch ACQUIRES the claim on the session it is about to drive; it does not
	// verify one the caller presents, because until the claim routes exist (SG-02-c)
	// no caller has a token to present. These three carry the acquisition's outcome to
	// the gate, and the same holder/fence pair is STAMPED on the run row so every later
	// governed write compares against something durable rather than against a value the
	// server looked up for itself at the moment of asking.

	// Holder is WHO is launching, in the admission plane's terms. Over HTTP it is the
	// authenticated principal (runtime_api.go:69) and the request body has no way to
	// reach it — the create body carries no such field and the decoder rejects unknown
	// ones. It is NOT unforgeable to an in-process Go caller that builds this struct by
	// hand: LaunchIntent is exported and a gate takes it whole.
	Holder string
	// Fence is the authority token of the claim acquired for this launch, minted by the
	// store inside Claim. No caller-supplied value ever reaches it.
	Fence int64
	// ClaimSID is the canonical session the claim is over, resolved by the runtime so
	// the gate need not resolve it again — which also stops a DENIED launch from minting
	// identity as a side effect of the gate's own lookup.
	ClaimSID string
}

// LaunchDecision is the gate's verdict AND the governance instructions the module
// applies to the launch. DENY-CLOSED BY CONTRACT when wired: a gate error or
// Allowed=false blocks the launch.
type LaunchDecision struct {
	Allowed bool
	Reason  string
	// InjectEnv is the governance environment the gate wants set on the launched
	// child ON TOP of the module's own env (e.g. the OLIVARES_HOOK_PEP_* the managed
	// PreToolUse hook reads to reach the governed PEP). It is authoritative over any
	// host value and is held in memory for the launch only — a per-session PEP bearer
	// is used and discarded, never persisted (docs/SECURITY-HARDENING.md). Empty for the no-op default.
	InjectEnv []EnvVar
	// ContextPolicySummary is a short, non-sensitive description of the effective
	// launch context policy. The runtime records it in the lifecycle ledger's launch
	// event detail. Empty means no context policy was applied.
	ContextPolicySummary string
	// RecordIO instructs the runtime to record this run's bridged I/O as governed
	// ledger evidence (the gate sets it for CRITICAL/privileged or opted-in runs).
	RecordIO bool
	// DeniedStatus is the HTTP status the API should return for a denial (Allowed
	// false). 0 ⇒ 403 Forbidden (the deny-closed default). The budget gate sets 402
	// (PaymentRequired, hard cap) or 429 (TooManyRequests, throttle) so a session at
	// its budget cap fails the launch with the right code (2026-06-16).
	DeniedStatus int
	// Critical marks a privileged launch — bypassPermissions/dontAsk, or a read-write
	// mount of a classified workspace — the launch that drove the CRITICAL HITL + the
	// mandatory I/O-recording floor. The runtime persists it on the run (non-sensitive)
	// so the portal can explain the session's governance posture.
	Critical bool
	// ApprovalRef is the governed HITL approval opened for a CRITICAL launch, so the
	// portal can deep-link the approval's live status. Empty when no approval was opened
	// (a non-CRITICAL launch). A reference only — never the approval's contents.
	ApprovalRef string
}

// LaunchGate authorizes a launch/resume and returns the governance instructions
// (PEP env to inject, whether to record I/O). Wires budget + the
// CRITICAL-launch HITL + the PEP provisioning behind this single seam; the
// default allows with no instructions so the runtime works standalone.
type LaunchGate interface {
	Authorize(ctx context.Context, tenant model.TenantID, intent LaunchIntent) (LaunchDecision, error)
}

// allowLaunchGate is the unwired default (no policy → allow, no governance env, no
// recording).
type allowLaunchGate struct{}

func (allowLaunchGate) Authorize(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
	return LaunchDecision{Allowed: true}, nil
}

// ---------------------------------------------------------------------------
// StopGate — the estate kill-switch pre-flight (additive).
// ---------------------------------------------------------------------------

// StopDims is the references-only attribution the stop check scopes on.
type StopDims struct {
	RunRef   string
	AgentRef string
}

// StopDecision is the kill-switch verdict. DENY-CLOSED BY CONTRACT when wired:
// callers treat a gate ERROR as stopped (an unreadable stop state never means go).
type StopDecision struct {
	Stopped bool
	StopRef string
	Scope   string
}

// StopGate reports whether an active emergency stop freezes a launch/resume.
type StopGate interface {
	Check(ctx context.Context, tenant model.TenantID, dims StopDims) (StopDecision, error)
}

// allowStopGate is the unwired default (no stop state → allow).
type allowStopGate struct{}

func (allowStopGate) Check(context.Context, model.TenantID, StopDims) (StopDecision, error) {
	return StopDecision{}, nil
}

// ---------------------------------------------------------------------------
// Recorder — governed I/O recording (additive).
// ---------------------------------------------------------------------------

// RecordedFrame is one bridged I/O frame offered to the governed recorder. The
// default sink drops it; recording, when wired, anchors a PayloadHash
// and applies its own redaction — this seam never decides retention itself.
type RecordedFrame struct {
	Seq    int64
	Stream string
	Data   []byte
	At     time.Time
}

// Recorder receives the live I/O of an operated session for governed recording.
// Record is offered one bridged frame; Finalize is called once when the session's
// I/O ends (the process exited and the output drained) so the recorder can flush and
// seal its evidence chain. Both are best-effort from the bridge's perspective — a
// recorder failure must not corrupt the live stream — and the recorder emits its own
// loud gap evidence; the deny-closed posture for privileged sessions is enforced at
// the LaunchGate (a CRITICAL session that cannot be recorded is not launched).
type Recorder interface {
	Record(ctx context.Context, tenant model.TenantID, runRef string, frame RecordedFrame) error
	Finalize(ctx context.Context, tenant model.TenantID, runRef string) error
}

// noopRecorder is the unwired default: it records nothing.
type noopRecorder struct{}

func (noopRecorder) Record(context.Context, model.TenantID, string, RecordedFrame) error {
	return nil
}

func (noopRecorder) Finalize(context.Context, model.TenantID, string) error { return nil }
