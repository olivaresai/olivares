// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// sessiongov.go is the composition-root wiring that turns an OPERATED Claude
// Code session (the runtime, module II) into a GOVERNED one: every launch passes
// the existing controls and every tool-call the session makes is policy-checked in
// line. It is the differentiator — Olivares does not merely run `claude`, it governs
// what `claude` executes (something a loose `claude` has no way to do).
//
// It bridges module II's deny-closed seams (sessions.LaunchGate / StopGate / Recorder)
// to the controls already in the engine — none of which module II may import:
//
//   - StopGate  → the estate kill-switch (sessionStopGate, the sibling of the
//     orch/voice/deploy adapters in killswitchgate.go). Checked FIRST in the module's
//     preflight (stop > break-glass) and again, continuously, by the module's active
//     termination sweep so a stop also PARA running sessions, not only IMPEDE new ones.
//   - LaunchGate → the budget pre-flight (deny 402/429 at the cap) + the
//     CRITICAL-launch HITL (privileged launches need two-person approval) + the
//     PEP env provisioning (so the launched session's managed PreToolUse hook reaches
//     the governed PEP) + the per-run I/O-recording decision.
//   - Recorder governed I/O evidence: each bridged frame is folded into a
//     hash chain anchored to the SAME signed audit ledger uses (PayloadHash;
//     minimal-data — commitments, never raw I/O; WalkCanonical + Verify proves it).
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): only references cross these seams; no tool arguments,
// payloads, prompts or I/O bytes. The per-session PEP bearer is held for the launch and
// never persisted; recorded I/O is a one-way digest, never the bytes.

// ---------------------------------------------------------------------------
// Constants.
// ---------------------------------------------------------------------------

const (
	// sessionLaunchAction is the governed action a privileged launch opens an approval
	// for — pre-classified CRITICAL in governance/risktier.go so the two-person
	// floor + AAL3 step-up apply. An approval is opened ONLY for a privileged launch.
	sessionLaunchAction      = "sessions.run.launch"
	sessionLaunchSubjectKind = "sessions.run"

	// The managed PEP-hook env the launched `claude` carries so its PreToolUse hook
	// (`olivares claude-hook`) reaches the governed PEP (cmd_claudehook.go reads these).
	envHookPEPURL    = "OLIVARES_HOOK_PEP_URL"
	envHookPEPToken  = "OLIVARES_HOOK_PEP_TOKEN"
	envHookPEPTenant = "OLIVARES_HOOK_PEP_TENANT"
	envHookPEPAgent  = "OLIVARES_HOOK_PEP_AGENT"

	// Advisory managed-settings for the launched session's context/compaction policy
	//. These carry only the effective cap/strategy, never prompt or payload data.
	envContextMaxTokens = "OLIVARES_CONTEXT_MAX_TOKENS"
	envContextStrategy  = "OLIVARES_CONTEXT_STRATEGY"

	// sessionIOAnchorEvery is how many bridged frames fold into the chain before a
	// batch is anchored to the ledger (bounded ledger writes; the tail is flushed at
	// Finalize). Mirrors recording.anchorEvery's "tamper-evident before its seal".
	sessionIOAnchorEvery = 64
	// sessionIOAction is the audit verb each I/O evidence anchor carries.
	sessionIOAction = "sessions.io.recorded"

	// sessionSignalSource labels the bus findings the session launch gate emits.
	sessionSignalSource = "sessions_governance"
	// unroutedLaunchFindingKind is the posture finding raised when an operated session
	// launches without inference routed through the governing proxy.
	unroutedLaunchFindingKind = "session_launch_ungoverned_model"
)

// sessionIOTargetKind is the audit target kind for an I/O evidence anchor.
const sessionIOTargetKind model.Kind = "sessions.run"

// sessionIOFrameDomain domain-separates the I/O frame chain hash (length-prefix
// pattern) so a frame hash can never collide with another hashed structure.
var sessionIOFrameDomain = []byte("olivares.sessions.io.frame.v1")

// Operator provisioning for OPERATE session governance (out of the store, env-driven,
// mirroring sessionruntime.go's OLIVARES_SESSION_RUNTIME_* keys).
const (
	// envSessionPEPURL is the governed PEP endpoint the operated session's managed
	// PreToolUse hook calls (the loopback the hooks-PEP server binds). UNSET ⇒ the
	// PEP provisioner is not wired ⇒ the managed hook is deny-closed and every tool-call
	// the session makes is denied (2026-06-16: launch, deny per-tool).
	envSessionPEPURL = "OLIVARES_SESSION_PEP_URL"
	// envSessionPEPTokenFile is a rotated per-session PEP bearer the operator's identity
	// broker writes (the engine authenticator resolves it to the session's principal).
	// UNSET ⇒ no bearer is injected (the hook authenticates with the agent hint only;
	// a require_firm_identity tenant then denies — deny-closed).
	envSessionPEPTokenFile = "OLIVARES_SESSION_PEP_TOKEN_FILE"
	// envSessionKillSwitchSweep is the active-termination sweep cadence (a Go duration;
	// "" ⇒ defaultStopSweepInterval; "0" ⇒ disabled — block-on-launch only).
	envSessionKillSwitchSweep = "OLIVARES_SESSION_KILLSWITCH_SWEEP"
	// envSessionBudgetAvailability controls whether an unreadable budget control
	// fails open or closed at the session launch gate.
	envSessionBudgetAvailability = "OLIVARES_SESSION_BUDGET_AVAILABILITY"
	// envSessionContextAvailability controls whether an unreadable context-policy
	// control fails open or closed at the session launch gate.
	envSessionContextAvailability = "OLIVARES_SESSION_CONTEXT_AVAILABILITY"
)

// availabilityPosture is how the launch gate treats a dependency it cannot READ (budget
// ledger, context policy). It is NOT about a definitive deny (a cap/forbid always denies);
// it is the fail-open vs fail-closed choice when the control is UNREADABLE.
type availabilityPosture int

const (
	availabilityFailOpen   availabilityPosture = iota // read error ⇒ allow (availability over evidence)
	availabilityFailClosed                            // read error ⇒ deny (evidence over availability)
)

func (p availabilityPosture) String() string {
	if p == availabilityFailClosed {
		return "fail-closed"
	}
	return "fail-open"
}

// resolveAvailabilityPosture picks the posture: an explicit env value wins; unset defaults to
// fail-closed on the enterprise edition (evidence-grade availability) and fail-open elsewhere
// (preserve the community default). An invalid value is fail-closed + LOUD (a typo must never
// silently weaken the gate — the same stance as the audit-spool mode loader).
func resolveAvailabilityPosture(raw, edition string, log *slog.Logger) availabilityPosture {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "fail-open":
		return availabilityFailOpen
	case "fail-closed":
		return availabilityFailClosed
	case "":
		if edition == "enterprise" {
			return availabilityFailClosed
		}
		return availabilityFailOpen
	default:
		if log != nil {
			log.Error("session launch-gate: invalid availability posture; using fail-closed", "value", raw)
		}
		return availabilityFailClosed
	}
}

// defaultStopSweepInterval is the default active kill-switch sweep cadence: how soon a
// running session is reclaimed after an emergency stop (its tool-calls are denied by the
// inline PEP immediately; the process is terminated within this window).
const defaultStopSweepInterval = 15 * time.Second

// ---------------------------------------------------------------------------
// StopGate — the kill-switch over operated sessions.
// ---------------------------------------------------------------------------

// sessionStopGate adapts the kill-switch state to module II's launch/sweep StopGate.
// FAIL CLOSED: the module treats a returned error as stopped (an unreadable stop
// state never means "go"). It records the kill-switch launch denial to the
// tamper-evident ledger (throttled) — a denied launch creates no run row, so this is
// its only audit leg, matching the hooks-PEP / MCP-gateway deny evidence.
type sessionStopGate struct {
	guard killSwitchGuard
	rec   *stopDenyRecorder
}

var _ sessions.StopGate = sessionStopGate{}

func (g sessionStopGate) Check(ctx context.Context, tenant model.TenantID, dims sessions.StopDims) (sessions.StopDecision, error) {
	st, err := g.guard.KillSwitchState(ctx, tenant)
	if err != nil {
		return sessions.StopDecision{}, err // module fails CLOSED on error
	}
	if id, stopped := st.Stopped(strings.TrimSpace(dims.AgentRef)); stopped {
		subject := firstNonEmptyStr(strings.TrimSpace(dims.RunRef), strings.TrimSpace(dims.AgentRef))
		g.rec.record(ctx, tenant, id, "sessions-launch", subject, strings.TrimSpace(dims.AgentRef))
		return sessions.StopDecision{Stopped: true, StopRef: id.String(), Scope: stopScopeOf(st, id)}, nil
	}
	return sessions.StopDecision{}, nil
}

// ---------------------------------------------------------------------------
// LaunchGate — budget + CRITICAL HITL + PEP provisioning + record decision.
// ---------------------------------------------------------------------------

// sessionLaunchGate composes the launch-time governance for an operated session.
// Order: budget → CRITICAL determination → privileged-recording check → CRITICAL HITL
// (deny-closed) → PEP env provisioning → context policy → record decision. Budget and
// context-policy read errors follow their explicit availability posture; definitive
// cap/forbid decisions always deny. The kill-switch is the SEPARATE StopGate, checked
// by the module BEFORE this gate (stop > break-glass), so a stopped estate denies
// before any approval is opened.
type sessionLaunchGate struct {
	fin             budgetChecker          // Budget pre-flight (nil ⇒ no budget gate)
	bridge          hookApprovalOpener     // HITL (nil ⇒ a CRITICAL launch is denied)
	pep             *sessionPEPProvisioner // PEP env (nil ⇒ no env ⇒ deny-closed per-tool)
	contextPolicy   contextPolicyResolver  // Context/compaction launch policy (nil ⇒ no context policy)
	budgetPosture   availabilityPosture    // read-error posture for the budget control
	contextPosture  availabilityPosture    // read-error posture for the context-policy control
	recordAvailable bool                   // a recorder is wired (a CRITICAL launch needs it)
	// model-layer governance posture. inferenceRouted reports whether operated
	// sessions' inference is routed through Olivares' own governing proxy (the same
	// OLIVARES_SESSION_RUNTIME_BASE_URL the runtime reads). When it is NOT, the launched
	// `claude` talks to the model provider directly — model-access/budget/DLP are never
	// enforced in-band — so each launch emits a loud, evidenced posture finding on `sink`
	// rather than the gap staying silent. The in-band enforcement itself lives in the proxy
	// (inferenceproxy.go) and is on the path only for a routed session.
	inferenceRouted bool
	sink            observationSink // bus for the un-routed posture finding (nil ⇒ no emit)
	clock           func() time.Time
	log             *slog.Logger
}

var _ sessions.LaunchGate = (*sessionLaunchGate)(nil)

func (g *sessionLaunchGate) Authorize(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (sessions.LaunchDecision, error) {
	// 1. Budget pre-flight. A read error follows the configured availability
	//    posture; a definitive cap always denies with 402 (block) / 429 (throttle) so
	//    a session/identity at its limit does not start.
	if g.fin != nil {
		chk, err := g.fin.CheckBudget(ctx, tenant, finops.SpendDims{
			AgentRef: intent.AgentRef, SessionRef: intent.RunRef, ModelRef: intent.Model,
			// The session's workspace is its FinOps WorkspaceRef dimension, so a
			// workspace-scoped enforcing budget also caps the launch. CheckBudget resolves
			// the firm identity itself from AgentRef when an identity budget exists.
			WorkspaceRef: intent.WorkspaceRef,
		})
		switch {
		case err != nil:
			if g.log != nil {
				g.log.Error("session launch-gate: budget check failed", "posture", g.budgetPosture.String(), "err", err)
			}
			if g.budgetPosture == availabilityFailClosed {
				return sessions.LaunchDecision{
					Allowed:      false,
					Reason:       "session budget control unavailable (deny-closed)",
					DeniedStatus: http.StatusServiceUnavailable,
				}, nil
			}
		case !chk.Allowed:
			return sessions.LaunchDecision{
				Allowed:      false,
				Reason:       "session budget " + budgetActionLabel(chk.Action), // money-free (docs/SECURITY-HARDENING.md)
				DeniedStatus: budgetStatus(chk.Action),
			}, nil
		}
	}

	// 2. CRITICAL determination (2026-06-16): a bypassPermissions/dontAsk launch, or
	//    a read-write mount of a classified workspace.
	critical, why := isCriticalLaunch(intent)

	// 3. A CRITICAL/privileged session MUST be recordable, or it is not launched
	//    (deny-closed privileged recording — the break-glass posture: no evidence,
	//    no privileged action).
	if critical && !g.recordAvailable {
		return denyLaunch("a privileged session must be recorded but no recorder is wired (deny-closed)"), nil
	}

	// 4. CRITICAL ⇒ governed human approval: time-boxed, two-person floor + AAL3.
	//    The bridge is async — a pending approval DENIES the launch with its ref; the
	//    operator approves out-of-band and retries (gateOnce then finds it approved).
	var approvalRef string
	if critical {
		ref, dec, ok := g.gateCriticalLaunch(ctx, tenant, intent, why)
		if !ok {
			return dec, nil
		}
		approvalRef = ref
	}

	// 5. PEP env provisioning: the managed PreToolUse hook the launched session
	//    runs reaches the governed PEP with this env. No provisioner ⇒ no env ⇒ the
	//    managed hook is deny-closed and EVERY tool-call is denied (Q4: the session
	//    launches and is observable, but cannot act until PEP-provisioned).
	var injectEnv []sessions.EnvVar
	if g.pep != nil {
		env, err := g.pep.provision(ctx, tenant, intent.AgentRef)
		if err != nil {
			if g.log != nil {
				g.log.Warn("session launch-gate: PEP provisioning failed; the session's tool-calls stay deny-closed per-tool", "err", err)
			}
		} else {
			injectEnv = env
		}
	}

	var ctxSummary string
	if g.contextPolicy != nil {
		ctxPol, cperr := g.contextPolicy.Apply(ctx, tenant, knowledge.ContextPolicyQuery{
			AgentRef:     intent.AgentRef,
			WorkspaceRef: intent.WorkspaceRef,
			Model:        intent.Model,
		})
		if cperr != nil {
			if g.log != nil {
				g.log.Error("session launch-gate: context policy check failed", "posture", g.contextPosture.String(), "err", cperr)
			}
			if g.contextPosture == availabilityFailClosed {
				return sessions.LaunchDecision{
					Allowed:      false,
					Reason:       "context policy control unavailable (deny-closed)",
					DeniedStatus: http.StatusServiceUnavailable,
				}, nil
			}
		} else if ctxPol.Deny {
			return sessions.LaunchDecision{
				Allowed:      false,
				Reason:       "context policy forbids this agent",
				DeniedStatus: http.StatusForbidden,
			}, nil
		} else {
			maxTokens := strconv.FormatInt(ctxPol.MaxContextTokens, 10)
			injectEnv = append(injectEnv,
				sessions.EnvVar{Name: envContextMaxTokens, Value: maxTokens},
				sessions.EnvVar{Name: envContextStrategy, Value: ctxPol.Strategy},
			)
			ctxSummary = "ctx max=" + maxTokens + " strategy=" + ctxPol.Strategy + " scope=" + ctxPol.WinningScope
		}
	}

	// the launch is authorized. If its inference is NOT routed through the
	// governing proxy, the model layer runs un-governed (model-access/budget/DLP are not
	// enforced in-band) — raise a loud, evidenced posture finding so the gap is never silent
	// (deduped per tenant downstream by DetailHash).
	if !g.inferenceRouted {
		g.emitUnroutedFinding(ctx, tenant)
	}

	return sessions.LaunchDecision{
		Allowed:              true,
		InjectEnv:            injectEnv,
		ContextPolicySummary: ctxSummary,
		RecordIO:             critical || intent.RecordRequested,
		Critical:             critical,
		ApprovalRef:          approvalRef,
	}, nil
}

// emitUnroutedFinding publishes the "operated session launched un-routed" posture finding.
// Best-effort (a bus failure never blocks a launch) and deduped per tenant by DetailHash, so
// a tenant running operated sessions without inference routing surfaces exactly one active
// finding. No caller identity is needed — the gap is a process/tenant posture, not a
// per-principal event.
func (g *sessionLaunchGate) emitUnroutedFinding(ctx context.Context, tenant model.TenantID) {
	if g.sink == nil {
		return
	}
	now := time.Now()
	if g.clock != nil {
		now = g.clock()
	}
	obs := sdkmodel.FindingReport{
		Kind:        unroutedLaunchFindingKind,
		Severity:    sdkmodel.SeverityMedium,
		SubjectKind: string(sessionIOTargetKind),
		SubjectRef:  "inference-routing",
		Title:       "Operated Claude Code session launched without inference routed through the governing proxy — model-access/budget/DLP are not enforced in-band (model-layer governance gap)",
		DetailHash:  hexSHA("session_launched_unrouted|" + tenant.String()),
		OccurredAt:  now.UTC(),
	}
	if err := g.sink.Publish(ctx, event.FromObservation(tenant.String(), sessionSignalSource, obs)); err != nil && g.log != nil {
		g.log.Warn("session launch-gate: un-routed posture finding publish failed (best-effort)", "err", err)
	}
}

// gateCriticalLaunch opens (or idempotently finds/reuses) the governed approval for a
// privileged launch and maps its status. ok=true ⇒ approved/break-glass (proceed);
// ok=false ⇒ the returned decision is the denial/pending verdict. Deny-closed: no
// bridge, an error, or any non-approved status denies.
// It returns the approval ref alongside the verdict so the gate can surface it on the
// LaunchDecision (the run persists it for the portal's HITL deep-link) — on approve,
// pending and reject alike (the ref is a non-sensitive reference, useful in every case).
func (g *sessionLaunchGate) gateCriticalLaunch(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent, why string) (string, sessions.LaunchDecision, bool) {
	if g.bridge == nil {
		return "", denyLaunch("privileged session launch requires human approval but the HITL bridge is not wired (deny-closed)"), false
	}
	planHash := sessionLaunchPlanHash(intent)
	reason := "Privileged Claude Code session launch (" + why + "): " + describeLaunch(intent)
	ref, status, _, err := g.bridge.gateOnce(ctx, tenant, sessionLaunchAction, sessionLaunchSubjectKind, launchSubjectRef(intent), planHash, reason, intent.Actor)
	if err != nil {
		// The bridge broke; it did not decide. Still denied — but as an outage the
		// caller may retry, not as a verdict about this launch (P1-R6-01).
		return "", denyUnavailable("could not open a governed approval for the privileged launch (deny-closed)"), false
	}
	switch status {
	case nbApproved:
		// F-02 single-use: a human approval of a PRIVILEGED (bypassPermissions/dontAsk)
		// launch authorizes ONE launch, not a 24h-reusable pass — this is a MORE privileged
		// surface than the tool-call PEP with the same replay root. SPEND the approval so a
		// second launch reusing the same still-approved grant within its window is a
		// would-replay DENY. A launch has no non-forgeable transport correlation id (unlike a
		// tool_use_id), so it is STRICTLY single-use: a fresh server-side nonce binds the first
		// consume and any later consume is a replay. Trade-off (documented-FIX §F4): a
		// re-launch/replay within the window — including a genuine retry after a failed launch —
		// needs a fresh human approval; for a privileged bypassPermissions launch that is the
		// correct posture. (break-glass is handled below: the engine already recorded its
		// one-shot use at grant time, so it is NOT double-consumed here.)
		granted, replay, cerr := g.bridge.consumeApproval(ctx, tenant, ref, newSingleUseConsumerID(), "")
		if cerr != nil {
			return ref, denyUnavailable("could not spend the governed launch approval (deny-closed)"), false
		}
		if replay {
			return ref, denyLaunch("privileged launch approval already consumed; a fresh human approval is required (" + ref + ")"), false
		}
		if !granted {
			return ref, denyLaunch("governed launch approval is no longer valid to spend (deny-closed)"), false
		}
		return ref, sessions.LaunchDecision{}, true
	case nbBreakGlass:
		return ref, sessions.LaunchDecision{}, true
	case nbPending:
		return ref, denyLaunch("privileged session launch requires human approval (pending: " + ref + ")"), false
	default:
		return ref, denyLaunch("human review did not approve the privileged launch (status=" + status + ")"), false
	}
}

// denyLaunch is a 403 launch denial (DeniedStatus 0 ⇒ the module maps it to Forbidden).
func denyLaunch(reason string) sessions.LaunchDecision {
	return sessions.LaunchDecision{Allowed: false, Reason: reason}
}

// denyUnavailable is the same DENIAL — Allowed stays false — published as an outage
// rather than as a verdict.
//
// It is for the bridge's ERROR channel, which is not where a human or a policy says
// no: a refusal arrives as a status or a flag, and every one of those keeps its 403
// below. The error channel carries a transport failure, ANY non-200, or a malformed
// body (approvalbridge.go).
//
// DECLARED LIMIT of that taxonomy, rather than a claim it does not earn: a non-200 can
// be a 401 or a 403 from misconfiguration, and retrying that unchanged will fail
// again. 503 is still the right answer against 403, because none of these is a verdict
// about THIS launch — but it does not promise the retry will succeed. Splitting
// configuration from outage needs the bridge to stop folding every status into one
// error, which is its own change.
func denyUnavailable(reason string) sessions.LaunchDecision {
	return sessions.LaunchDecision{
		Allowed: false, Reason: reason, DeniedStatus: http.StatusServiceUnavailable,
	}
}

// isCriticalLaunch classifies a launch as privileged (HITL + recording floor):
// bypassPermissions/dontAsk, or a read-write mount of a classified workspace.
func isCriticalLaunch(intent sessions.LaunchIntent) (bool, string) {
	switch strings.TrimSpace(intent.PermissionMode) {
	case "bypassPermissions", "dontAsk":
		return true, "permission-mode " + strings.TrimSpace(intent.PermissionMode)
	}
	if intent.WorkspaceClassified && intent.WorkspaceReadWrite {
		return true, "read-write mount of a classified workspace"
	}
	return false, ""
}

// sessionLaunchPlanHash binds the launch's governed parameters: a re-plan (different
// transport/mode/model/workspace/actor) voids any prior approval (anti-TOCTOU).
// References only — no secrets enter the hash.
func sessionLaunchPlanHash(intent sessions.LaunchIntent) string {
	h := sha256.New()
	fields := []string{
		string(intent.Action), string(intent.Transport), intent.PermissionMode,
		intent.Model, intent.WorkspaceRef, intent.Actor,
	}
	// the workspace template's identity, REVISION and merged tool allowlist join the
	// hash, because without them the anti-TOCTOU boundary this hash draws had a hole the
	// width of the template plane. A template is mutable and is re-read on every launch, so
	// an approval opened while it allowed only `Read` could be spent, unchanged in every
	// other field, on a revision that had since been widened to `Read, Bash` — the approver
	// having seen the narrow one. The version alone would close it; the allowlist is hashed
	// too so the binding survives any future path that reaches this gate without one.
	fields = append(fields, intent.TemplateRef, strconv.FormatInt(intent.TemplateVersion, 10))
	fields = append(fields, intent.AllowedTools...)
	for _, s := range fields {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// launchSubjectRef is the approval subject (the plan hash is appended by the bridge).
func launchSubjectRef(intent sessions.LaunchIntent) string {
	if r := strings.TrimSpace(intent.WorkspaceRef); r != "" {
		return "workspace:" + r
	}
	return "session:no-workspace"
}

// describeLaunch is the money-free, secret-free human summary on the approval receipt.
func describeLaunch(intent sessions.LaunchIntent) string {
	parts := []string{"mode=" + intent.PermissionMode, "transport=" + string(intent.Transport)}
	if intent.Model != "" {
		parts = append(parts, "model="+intent.Model)
	}
	if intent.WorkspaceRef != "" {
		parts = append(parts, "workspace="+intent.WorkspaceRef)
	}
	// what the approver is actually approving. A privileged launch under a workspace
	// template is a launch under THAT REVISION's terms, and a receipt that named only the
	// mode and the workspace asked a human to approve a tool authority they could not see.
	if intent.TemplateRef != "" {
		parts = append(parts, "template="+intent.TemplateRef+"@v"+strconv.FormatInt(intent.TemplateVersion, 10))
	}
	if len(intent.AllowedTools) > 0 {
		parts = append(parts, "tools="+strings.Join(intent.AllowedTools, ","))
	}
	return strings.Join(parts, " ")
}

// budgetActionLabel maps a FinOps action to a money-free reason fragment.
func budgetActionLabel(action string) string {
	switch action {
	case "block":
		return "hard cap reached"
	case "throttle":
		return "throttled"
	default:
		return "exceeded"
	}
}

// budgetStatus maps a FinOps action to the HTTP status the launch denial carries.
func budgetStatus(action string) int {
	if action == "throttle" {
		return http.StatusTooManyRequests // 429
	}
	return http.StatusPaymentRequired // 402 (block / default)
}

// ---------------------------------------------------------------------------
// PEP provisioner — the OLIVARES_HOOK_PEP_* the managed hook reads.
// ---------------------------------------------------------------------------

// sessionPEPProvisioner builds the managed PEP-hook environment the launched session
// carries so its PreToolUse hook reaches the governed PEP. The bearer is minted per
// launch and discarded after the launch (never persisted). Built (and wired) only when
// the operator configured a PEP URL + a bearer source; otherwise the gate injects no
// env and the managed hook is deny-closed per-tool.
type sessionPEPProvisioner struct {
	url  string
	mint func(ctx context.Context, tenant model.TenantID, agentRef string) (string, error)
	log  *slog.Logger
}

func (p *sessionPEPProvisioner) provision(ctx context.Context, tenant model.TenantID, agentRef string) ([]sessions.EnvVar, error) {
	token := ""
	if p.mint != nil {
		t, err := p.mint(ctx, tenant, agentRef)
		if err != nil {
			return nil, err
		}
		token = t
	}
	env := []sessions.EnvVar{
		{Name: envHookPEPURL, Value: p.url},
		{Name: envHookPEPTenant, Value: tenant.String()},
	}
	if agentRef != "" {
		env = append(env, sessions.EnvVar{Name: envHookPEPAgent, Value: agentRef})
	}
	if token != "" {
		env = append(env, sessions.EnvVar{Name: envHookPEPToken, Value: token})
	}
	return env, nil
}

// ---------------------------------------------------------------------------
// I/O recorder — governed evidence anchored to the signed ledger.
// ---------------------------------------------------------------------------

// sessionIORecorder backs module II's Recorder seam with tamper-evident I/O evidence.
// It folds each bridged frame's REDACTED descriptor (a one-way content digest + length,
// never the bytes — minimal data) into a per-run hash chain and anchors the chain tip
// to the SAME signed, hash-chained audit ledger uses (PayloadHash). Bounded ledger
// writes: a batch is anchored every sessionIOAnchorEvery frames and the tail is flushed
// + sealed at Finalize. Verification is the standard ledger WalkCanonical + Verify: the
// chain commits to every frame in order, so an auditor holding the I/O can prove it is
// the exact, unaltered stream, and the ledger proves the commitments were not tampered.
type sessionIORecorder struct {
	st  store.Store
	log *slog.Logger

	mu     sync.Mutex
	chains map[string]*sessionIOChain
}

// sessionIOChain is one run's rolling evidence chain (accessed only by that run's single
// bridge goroutine; the map is the only shared state).
type sessionIOChain struct {
	tip        []byte // 32-byte rolling chain hash over every folded frame
	firstSeq   int64  // first frame seq in the current unanchored batch
	lastSeq    int64  // last frame seq folded
	batchCount int    // frames in the current unanchored batch
	batchBytes int64  // bytes in the current unanchored batch
	total      int64  // total frames folded over the run
}

func newSessionIORecorder(st store.Store, log *slog.Logger) *sessionIORecorder {
	return &sessionIORecorder{st: st, log: log, chains: map[string]*sessionIOChain{}}
}

var _ sessions.Recorder = (*sessionIORecorder)(nil)

func (r *sessionIORecorder) chainFor(tenant model.TenantID, runRef string) *sessionIOChain {
	key := tenant.String() + "|" + runRef
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := r.chains[key]
	if ch == nil {
		ch = &sessionIOChain{tip: make([]byte, sha256.Size)}
		r.chains[key] = ch
	}
	return ch
}

// Record folds one bridged frame into the run's chain and anchors a full batch.
func (r *sessionIORecorder) Record(ctx context.Context, tenant model.TenantID, runRef string, frame sessions.RecordedFrame) error {
	ch := r.chainFor(tenant, runRef)
	contentSHA := sha256.Sum256(frame.Data) // a one-way digest, never the bytes
	ch.tip = sessionIOFrameHash(ch.tip, runRef, frame.Seq, frame.Stream, int64(len(frame.Data)), contentSHA[:])
	if ch.batchCount == 0 {
		ch.firstSeq = frame.Seq
	}
	ch.lastSeq = frame.Seq
	ch.batchCount++
	ch.batchBytes += int64(len(frame.Data))
	ch.total++
	if ch.batchCount >= sessionIOAnchorEvery {
		return r.anchor(ctx, tenant, runRef, ch, false)
	}
	return nil
}

// Finalize flushes the run's tail batch, seals the chain, and drops it from the map.
func (r *sessionIORecorder) Finalize(ctx context.Context, tenant model.TenantID, runRef string) error {
	key := tenant.String() + "|" + runRef
	r.mu.Lock()
	ch := r.chains[key]
	delete(r.chains, key)
	r.mu.Unlock()
	if ch == nil || ch.total == 0 {
		return nil
	}
	return r.anchor(ctx, tenant, runRef, ch, true)
}

// anchor seals the current batch's chain tip into the ledger. The Meta is a money-free,
// content-free batch summary (the canonical meta the chain hash commits to via the
// ledger); the PayloadHash is the rolling tip. Best-effort and LOUD on failure (the gap
// is evident as anchored < total).
//
// A seal whose batch is EMPTY (Finalize landing exactly on a periodic-anchor boundary —
// total a multiple of sessionIOAnchorEvery) is still emitted as the end-of-recording
// marker, but it carries NO from_seq/to_seq (frames=0): the prior periodic anchor
// already committed this exact tip, so an empty seal must not claim a stale frame range.
func (r *sessionIORecorder) anchor(ctx context.Context, tenant model.TenantID, runRef string, ch *sessionIOChain, sealed bool) error {
	if ch.batchCount == 0 && !sealed {
		return nil
	}
	tip := append([]byte(nil), ch.tip...)
	meta := map[string]any{
		"run_ref":      runRef,
		"frames":       ch.batchCount,
		"bytes":        ch.batchBytes,
		"total_frames": ch.total,
	}
	if ch.batchCount > 0 {
		meta["from_seq"] = ch.firstSeq
		meta["to_seq"] = ch.lastSeq
	}
	if sealed {
		meta["sealed"] = true
	}
	err := r.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: sessionIOAction, TargetKind: sessionIOTargetKind, TargetID: model.ID(runRef),
			PayloadHash: tip, Meta: meta,
		})
		return aerr
	})
	if err != nil {
		if r.log != nil {
			r.log.Error("session I/O recorder: ledger anchor failed (gap evidence: anchored<total)", "run_ref", runRef, "err", err)
		}
		return err
	}
	ch.batchCount = 0
	ch.batchBytes = 0
	ch.firstSeq = 0
	ch.lastSeq = 0
	return nil
}

// sessionIOFrameHash folds one redacted frame descriptor into the rolling chain hash
// (length-prefixed + domain-separated, the injective encoding). prev is the prior
// 32-byte tip (zero for the first frame); contentSHA is the frame's content digest.
func sessionIOFrameHash(prev []byte, runRef string, seq int64, stream string, byteLen int64, contentSHA []byte) []byte {
	h := sha256.New()
	writeLenPrefixed(h, sessionIOFrameDomain)
	writeLenPrefixed(h, []byte(runRef))
	h.Write(prev) // fixed 32 bytes
	writeInt64(h, seq)
	writeLenPrefixed(h, []byte(stream))
	writeInt64(h, byteLen)
	h.Write(contentSHA) // fixed 32 bytes
	return h.Sum(nil)
}

func writeLenPrefixed(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}

func writeInt64(h hash.Hash, v int64) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(v))
	_, _ = h.Write(n[:])
}

// ---------------------------------------------------------------------------
// Composition-root wiring (called by boot() before rt.Start, after the store /
// bridge / FinOps are live — module II was constructed first, so its OPERATE
// governance gates late-bind here, like gov.UseLifecycleGate / evBudget.bind).
// ---------------------------------------------------------------------------

// wireSessionGovernance late-binds the OPERATE governance onto module II: the
// kill-switch StopGate + active-termination sweep, the budget/HITL/PEP
// LaunchGate, and the I/O Recorder. The StopGate and Recorder
// are ALWAYS wired (governance + the audit ledger are in-process); the HITL bridge and
// the PEP provisioner are wired only when configured (else CRITICAL launches and tool
// governance stay deny-closed). stopDeny is the shared throttled deny recorder (reused
// from the engine so launch denials and hooks-PEP/MCP denials share one throttle).
func wireSessionGovernance(set moduleSet, st store.Store, stopDeny *stopDenyRecorder, bus observationSink, getenv func(string) string, log *slog.Logger) {
	if set.sessions == nil {
		return
	}
	// kill-switch pre-flight (fail-closed) + the active-termination sweep ("para").
	set.sessions.UseStopGate(sessionStopGate{guard: set.gov, rec: stopDeny})
	set.sessions.UseKillSwitchSweep(loadKillSwitchSweepInterval(getenv, log))

	// governed I/O evidence anchored to the signed ledger (always wired).
	set.sessions.UseRecorder(newSessionIORecorder(st, log))

	// + +: the launch gate. The bridge is typed-nil-safe (an untyped nil
	// interface keeps the deny-closed CRITICAL path; a typed nil *approvalBridge would
	// defeat it). recordAvailable is true because the recorder above is always wired.
	var bridge hookApprovalOpener
	if set.approvalBridge != nil {
		bridge = set.approvalBridge
	}
	var contextPolicy contextPolicyResolver
	if set.knowledge != nil {
		contextPolicy = set.knowledge
	}
	budgetPosture := resolveAvailabilityPosture(getenv(envSessionBudgetAvailability), buildEdition, log)
	contextPosture := resolveAvailabilityPosture(getenv(envSessionContextAvailability), buildEdition, log)
	// mirror the runtime's routing decision (sessionruntime.go reads the SAME env to
	// set ANTHROPIC_BASE_URL). Un-routed ⇒ the launch gate emits a posture finding per launch.
	inferenceRouted := strings.TrimSpace(getenv(envSessionBaseURL)) != ""
	set.sessions.UseLaunchGate(buildSessionLaunchGate(set, &sessionLaunchGate{
		fin:             set.finops,
		bridge:          bridge,
		pep:             loadSessionPEPProvisioner(getenv, log),
		contextPolicy:   contextPolicy,
		budgetPosture:   budgetPosture,
		contextPosture:  contextPosture,
		recordAvailable: true,
		inferenceRouted: inferenceRouted,
		sink:            bus,
		clock:           time.Now,
		log:             log,
	}))
	if log != nil {
		log.Info("session governance wired",
			"stop_gate", true, "budget_gate", set.finops != nil,
			"budget_posture", budgetPosture.String(), "context_posture", contextPosture.String(),
			"hitl_bridge", bridge != nil, "context_policy", contextPolicy != nil, "io_recording", true,
			"inference_routed", inferenceRouted, "claim_admission", true)
		// A FAIL-OPEN POSTURE IS NOT PART OF "WIRED" (2026-08-06). The line above is true and
		// stays INFO: the gate IS composed. But two of its nine key/value pairs carry the
		// opposite of good news — a posture of fail-open means that when this process cannot
		// READ the budget ledger or the context policy, the launch is ALLOWED. On the
		// community edition that is the default (resolveAvailabilityPosture), so the operator
		// most likely to be affected is the one who set nothing.
		//
		// Announcing it as a pair inside a message that reads "governance wired" is the
		// fourth answer wearing the first one's clothes. The log-level rule this repository
		// already applies elsewhere (modules/models: a 503 is WARN, a 200 with an honest body
		// is INFO) puts a control that will decline to enforce at WARN, on its own line,
		// naming the control, the consequence and the switch that changes it.
		for _, p := range []struct {
			control string
			posture availabilityPosture
			env     string
			effect  string
		}{
			{"budget", budgetPosture, envSessionBudgetAvailability, "a session launches even when its spend cannot be read"},
			{"context policy", contextPosture, envSessionContextAvailability, "a session launches even when its context policy cannot be read"},
		} {
			if p.posture == availabilityFailOpen {
				log.Warn("session launch gate is FAIL-OPEN for an unreadable control",
					"control", p.control, "on_read_error", "allow", "effect", p.effect,
					"set_to_fail_closed_with", p.env+"=fail-closed")
			}
		}
	}
}

// buildSessionLaunchGate wraps the budget/HITL/PEP gate in SG-02-b's claim
// admission, and is the whole of what "the admission control is wired" means. It is
// a named constructor rather than an expression inside wireSessionGovernance so a
// test can assert the composition itself: replace this with `return inner` and the
// control is silently gone, which is precisely the failure the decorator's own
// shipped doc-comment recorded when it went out inert.
//
// Order: admission runs BEFORE the inner gate, so a launch that is not admissible
// never opens a HITL approval nor spends budget quota on its way to being denied.
//
// Honest about its reach. Over HTTP this decorator is a SECOND line, not the first:
// the runtime acquires the claim before building the intent, so a launch that gets
// this far already holds what the decorator checks, and a contended session was
// already refused by the acquisition (ErrClaimHeld). What it still does, and why it
// is composed rather than dropped:
//
//   - it refuses a launch whose intent carries no holder — reachable from any
//     in-process caller that builds CreateRunParams without an actor, and from any
//     future construction path that does not run the runtime's preamble;
//   - it is where a refusal becomes VISIBLE, through SignalUnclaimedActivity, rather
//     than merely being returned to whoever asked;
//   - it keeps the rule expressed at the gate seam, where a second gate composition
//     (a different deployment, a different inner gate) inherits it for free.
func buildSessionLaunchGate(set moduleSet, inner sessions.LaunchGate) sessions.LaunchGate {
	return sessions.NewClaimAdmission(inner, set.sessions, sessions.ProviderOperated, sessions.IntentHolder)
}

// loadSessionPEPProvisioner builds the managed PEP-hook env provisioner from the
// environment, or nil when no PEP URL is configured (then operated sessions launch but
// their tool-calls are deny-closed per-tool — Q4 2026-06-16).
func loadSessionPEPProvisioner(getenv func(string) string, log *slog.Logger) *sessionPEPProvisioner {
	url := strings.TrimSpace(getenv(envSessionPEPURL))
	if url == "" {
		if log != nil {
			log.Info("session governance: no PEP URL configured; operated sessions' tool-calls are deny-closed per-tool until provisioned", "set", envSessionPEPURL)
		}
		return nil
	}
	prov := &sessionPEPProvisioner{url: url, log: log}
	if tf := strings.TrimSpace(getenv(envSessionPEPTokenFile)); tf != "" {
		prov.mint = func(_ context.Context, _ model.TenantID, _ string) (string, error) {
			b, err := os.ReadFile(tf)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(b)), nil
		}
	} else if log != nil {
		log.Warn("session governance: PEP URL set but no bearer file; the session hook authenticates by hint only (a require_firm_identity tenant denies)", "set", envSessionPEPTokenFile)
	}
	return prov
}

// loadKillSwitchSweepInterval resolves the active-termination sweep cadence (invalid ⇒
// the default, never a boot failure; "0" disables — block-on-launch only).
func loadKillSwitchSweepInterval(getenv func(string) string, log *slog.Logger) time.Duration {
	raw := strings.TrimSpace(getenv(envSessionKillSwitchSweep))
	if raw == "" {
		return defaultStopSweepInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		if log != nil {
			log.Warn("session governance: invalid kill-switch sweep interval; using the default",
				"env", envSessionKillSwitchSweep, "value", raw, "default", defaultStopSweepInterval.String())
		}
		return defaultStopSweepInterval
	}
	return d
}
