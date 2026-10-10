// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/governance/effectgate"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Config is the operator provisioning for the governed hooks PEP: a per-tenant
// governed policy (the allow/ask/deny disposition + rewrite/redaction the control plane
// imposes) and the loopback bind. The PDP overlay (Cedar/ABAC) and the HITL bridge are
// the engine's already-wired ones — this config does NOT re-declare them.
type Config struct {
	Listen  string         `json:"listen"`
	Tenants []TenantConfig `json:"tenants"`
}

// TenantConfig binds one business tenant to its governed hook policy. RequireFirm makes
// the deny-closed posture explicit: when set, a tool-call whose firm identity
// the PEP cannot resolve (approximate/unknown attribution) is denied rather than
// enforced on a guessed principal.
type TenantConfig struct {
	Tenant      string `json:"tenant"`
	RequireFirm bool   `json:"require_firm_identity"`
	// EnforceNHILifecycle opts the tenant into the NHI risk-conditional deny:
	// a tool-call by an agent whose bound NHI is blocked (stale-escalated / offboarded)
	// is denied — the offboarding cascade and the staleness block reaching the actuation
	// surface. Off by default so it never breaks day-1 operations; a blocked
	// NHI is the only thing it denies, and only an opted-in tenant's.
	EnforceNHILifecycle bool `json:"enforce_nhi_lifecycle"`
	// Enforcement selects the mode for this tenant's AUTHORED business policy:
	// "enforce" (default) applies every disposition; "observe" SHADOWS a would-be deny/ask
	// on an authored (ClassPolicy) rule — the call is allowed but the would-be verdict is
	// recorded — while EVERY platform invariant (identity, tenancy confinement, kill
	// switch, firewall/DLP, evidence, fail-closed errors) still enforces. An empty or
	// UNKNOWN value resolves to "enforce" (fail-safe: a typo never silently disables policy).
	Enforcement string `json:"enforcement,omitempty"`
	// ObserveUntil (E3) TIME-BOXES an observe grant: observe is active only while it is
	// "enforcement":"observe" AND now < ObserveUntil (RFC3339). It is REQUIRED for observe —
	// an absent, empty, unparseable OR already-past value resolves the tenant to ENFORCE
	// (deny-closed: a security RELAXATION never persists open-ended or on a typo; note
	// json.Unmarshal drops an unknown key, so "observe_until" reads as absent ⇒ enforce). The
	// window auto-reverts to enforce at ObserveUntil with NO restart (evaluated per decision);
	// EARLY revocation is a config edit + restart (the operator config is a boot snapshot).
	ObserveUntil string    `json:"observe_until,omitempty"`
	Policy       PolicyDoc `json:"policy"`
}

// hookEnforcementMode is the per-tenant mode. Its zero value is modeEnforce (fail-safe:
// an unset/unresolved mode enforces authored policy, never silently observes).
type hookEnforcementMode int

const (
	modeEnforce hookEnforcementMode = iota // 0 = zero-value = ENFORCE authored policy (default)
	modeObserve                            // shadow authored (ClassPolicy) denies/asks; invariants still enforce
)

// resolveEnforcementMode maps the operator string to the mode. ONLY the exact "observe"
// selects observe; "", "enforce" and any unknown value fall back to ENFORCE (deny-closed:
// a governed surface never drops policy enforcement on a typo).
func resolveEnforcementMode(s string) hookEnforcementMode {
	if strings.ToLower(strings.TrimSpace(s)) == "observe" {
		return modeObserve
	}
	return modeEnforce
}

// observeGrant is a validated, active observe time-box. bootMono is the boot clock reading (in
// production a MONOTONIC-bearing time.Now()) and window is the wall duration from boot to expiry;
// together they let observeGrantActive expire on monotonic ELAPSED time, immune to a wall-clock
// rollback WITHIN THE PROCESS LIFETIME even when no request arrived during the window. id is the
// content-digest grant id.
//
// RESIDUAL (documented, follow-up): the monotonic anchor is in-memory, so a wall-clock rolled back
// BEFORE observe_until AND a process restart re-derives a fresh (long) window from the delayed
// clock — the boot check below trusts the wall clock. Full closure needs a PERSISTENT trusted-time
// floor (e.g. the ledger's high-water OccurredAt) compared against observe_until at boot. This is a
// host-clock-compromise + restart threat on a business-rule relaxation (every platform invariant
// still enforces), mitigated operationally by monotonic NTP / no manual clock rollback on the
// control plane. Tracked for a persistent-time hardening pass.
type observeGrant struct {
	until    time.Time
	bootMono time.Time
	window   time.Duration
	id       string
}

// resolveObserveGrant validates a tenant's observe grant at boot. observe is honored ONLY
// when the RESOLVED mode is observe AND observe_until parses to a FUTURE RFC3339 instant;
// otherwise the tenant is ENFORCE (deny-closed) and the reason is logged. The returned grant
// carries the expiry, a monotonic anchor + window (rollback-immune expiry), and a content-digest
// grant id over the FULL policy (not just its version, so rule-different configs never collide).
func resolveObserveGrant(tid model.TenantID, tc TenantConfig, now time.Time, log *slog.Logger) (hookEnforcementMode, observeGrant) {
	if resolveEnforcementMode(tc.Enforcement) != modeObserve {
		return modeEnforce, observeGrant{}
	}
	until, err := parseObserveUntil(tc.ObserveUntil)
	if err != nil {
		if log != nil {
			log.Warn("hook-pep: tenant is 'observe' but observe_until is missing/invalid; resolving to ENFORCE (deny-closed)", "tenant", tid.String(), "observe_until", tc.ObserveUntil, "err", err)
		}
		return modeEnforce, observeGrant{}
	}
	if !until.After(now) {
		if log != nil {
			log.Warn("hook-pep: tenant 'observe' window is already past at boot; resolving to ENFORCE (deny-closed)", "tenant", tid.String(), "observe_until", until.UTC().Format(time.RFC3339))
		}
		return modeEnforce, observeGrant{}
	}
	return modeObserve, observeGrant{
		until:    until,
		bootMono: now,
		window:   until.Sub(now),
		id:       observeGrantID(tid, until, hookPolicyFingerprint(tc.Policy)),
	}
}

// hookPolicyFingerprint is a stable digest of the FULL governed policy document (default, rules,
// path precedence, rewrites). Two windows of the same tenant with the same expiry but DIFFERENT
// rules get DIFFERENT grant ids, so the promotion report never merges distinct policies.
func hookPolicyFingerprint(pol PolicyDoc) string {
	b, err := json.Marshal(pol)
	if err != nil { // this struct always marshals; the fallback keeps the id defined regardless
		return firstNonEmptyStr(pol.Version, hookPEPPolicyVersionFallback)
	}
	sum := sha256.Sum256(b)
	return "pf-" + hex.EncodeToString(sum[:12])
}

// parseObserveUntil requires a non-empty RFC3339 timestamp; empty/absent is an error (observe is
// never open-ended). An unknown JSON key like "observe_until" is dropped by json.Unmarshal, so it
// reaches here as "" ⇒ error ⇒ enforce — the safe side for a security relaxation.
func parseObserveUntil(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("observe_until is required for observe mode (a time-box is mandatory)")
	}
	return time.Parse(time.RFC3339, s)
}

// observeGrantID is the stable content digest of an observe grant's terms. It changes when the
// tenant, window or policy fingerprint changes, so historical windows never blur together in the
// promotion report. Truncated to 96 bits — collision-irrelevant (it only groups a report).
func observeGrantID(tid model.TenantID, until time.Time, policyFingerprint string) string {
	h := sha256.New()
	writeLenPrefixed(h, []byte("olivares.hook.observe.grant.v1"))
	writeLenPrefixed(h, []byte(tid.String()))
	writeLenPrefixed(h, []byte(until.UTC().Format(time.RFC3339Nano)))
	writeLenPrefixed(h, []byte(policyFingerprint))
	return "obsgrant-" + hex.EncodeToString(h.Sum(nil)[:12])
}

// PolicyDoc is the governed disposition policy for a tenant's tool-calls. Default is
// the disposition when no rule matches; an EMPTY default is treated as "deny"
// (deny-closed: a governed surface with no explicit allowlist denies). Operators set
// default:"allow" for an allowlist-of-denies posture (block specific tools, allow the
// rest) or default:"ask" to route everything through HITL.
type PolicyDoc struct {
	Version          string       `json:"version"`
	Default          string       `json:"default"` // allow | ask | deny ("" ⇒ deny)
	PathPrecedence   string       `json:"path_precedence,omitempty"`
	OnUnresolvedPath string       `json:"on_unresolved_path,omitempty"`
	Rules            []PolicyRule `json:"rules"`
}

// PolicyRule matches a hook by event + tool glob / resource kind / mode and imposes a
// disposition. Event ("" = any tool-gating event: PreToolUse/PermissionRequest) targets a
// SPECIFIC lifecycle event (e.g. "ConfigChange", "UserPromptSubmit") so a tool rule never
// accidentally gates a non-tool event; an event-targeted rule needs no tool/kind/mode.
// Rewrite (PreToolUse) supplies a governed input override merged over the original tool
// input; Block (PostToolUse) blocks post-processing of a flagged output.
type PolicyRule struct {
	Event        string         `json:"event,omitempty"`         // "" = tool-gating events; else exact (ConfigChange, …)
	Tool         string         `json:"tool,omitempty"`          // "", "*", "Bash", "mcp__*"
	ResourceKind string         `json:"resource_kind,omitempty"` // file | shell | http.url | …
	Mode         string         `json:"mode,omitempty"`          // read | write | unknown
	Paths        []string       `json:"paths,omitempty"`
	Subtree      string         `json:"subtree,omitempty"`
	Decision     string         `json:"decision"` // allow | ask | deny
	Reason       string         `json:"reason,omitempty"`
	Rewrite      map[string]any `json:"rewrite,omitempty"`
	Block        bool           `json:"block,omitempty"`
}

// ResolvedTenant is the validated per-tenant config the decider holds.
type ResolvedTenant struct {
	tenant      model.TenantID
	requireFirm bool
	enforceNHI  bool                // consult the NHI lifecycle block state for this tenant
	mode        hookEnforcementMode // enforce (default) | observe
	// observeUntil (E3) is the parsed, validated WALL expiry of an active observe grant; a
	// zero value means no active grant (mode is then never observe). observeBootMono + observeWindow
	// are the monotonic anchor and duration captured at boot: observeGrantActive expires on elapsed
	// MONOTONIC time, so a wall-clock rollback cannot resurrect a lapsed grant even with no traffic
	// during the window. observeGrantID is the content digest of the grant terms (full policy),
	// stamped on every shadow so the promotion report separates distinct observe windows.
	observeUntil    time.Time
	observeBootMono time.Time
	observeWindow   time.Duration
	observeGrantID  string
	policy          PolicyDoc
}

// hookPEPPolicyVersionFallback labels a decision when the policy declares no version.
const hookPEPPolicyVersionFallback = "olivares.hookpep/v1"

// ActionCapability is the governance action a gated tool-call opens an approval for
// and the verb root the PDP request carries. Kept short and stable (it is bounded by the
// engine's subject_ref guard and is the audit capability).
const ActionCapability = "claude.tool.use"

// hookDecisionDomain separates governed hook decision commitments from every other
// length-prefixed hash written to the audit ledger.
const hookDecisionDomain = "olivares.hook.tool.decision.v1"

// firm-attribution tiers.
const (
	TierFirm        = "firm"
	TierApproximate = "approximate"
	TierUnknown     = "unknown"
)

// ResolveTenant validates one operator tenant entry and resolves its enforcement mode and
// observe grant. The caller has already parsed and de-duplicated the tenant id.
func ResolveTenant(tid model.TenantID, tc TenantConfig, now time.Time, log *slog.Logger) (ResolvedTenant, error) {
	if err := validateHookPolicy(tc.Policy); err != nil {
		return ResolvedTenant{}, fmt.Errorf("hook PEP tenant policy: %w", err)
	}
	mode, grant := resolveObserveGrant(tid, tc, now, log)
	return ResolvedTenant{tenant: tid, requireFirm: tc.RequireFirm, enforceNHI: tc.EnforceNHILifecycle, mode: mode, observeUntil: grant.until, observeBootMono: grant.bootMono, observeWindow: grant.window, observeGrantID: grant.id, policy: tc.Policy}, nil
}

// Handler serves the governed hooks PEP: the Claude Code hook endpoint at "/", the Agent
// SDK permission prompt and, when sessionMCP is non-nil, the existing local session edge.
func (d *Decider) Handler(sessionMCP http.Handler) http.Handler {
	pep := claude.NewHookPEP(d, claudeHookAuditor{log: d.Log, decider: d}, time.Now)

	mux := http.NewServeMux()
	mux.Handle("/", d.withHookProof(pep))
	// Reuse the existing local session edge. The same handler is mounted on the
	// public API; neither edge creates an issuer, registry or child listener.
	if sessionMCP != nil {
		mux.Handle("/session/mcp", sessionMCP)
	}
	// the Agent SDK permissionPromptToolName route — point a customer SDK program's
	// permissionPromptToolName here and every permission request it raises runs through the
	// SAME governed decider (deny-closed). The more specific pattern wins over "/".
	mux.Handle("/permission-prompt", d.withHookProof(http.HandlerFunc(pep.ServePermissionPrompt)))
	return mux
}

func validateHookPolicy(pol PolicyDoc) error {
	for ri, r := range pol.Rules {
		for gi, glob := range r.Paths {
			if !filepath.IsAbs(glob) {
				return fmt.Errorf("rule %d path glob %d is relative", ri, gi)
			}
		}
		if subtree := strings.TrimSpace(r.Subtree); subtree != "" && !filepath.IsAbs(subtree) {
			return fmt.Errorf("rule %d subtree is relative", ri)
		}
	}
	return nil
}

// PrincipalAuthenticator resolves an inbound bearer to a real principal. *auth.Authenticator
// satisfies it; tests inject a fake. It is the firm-identity + audit-actor source.
type PrincipalAuthenticator interface {
	Authenticate(ctx context.Context, token string) (auth.Principal, error)
}

// ApprovalOpener is the subset of the bridge the PEP uses: open (or idempotently
// find/reuse) a governed approval bound to the plan hash and report its effective status
// in one shot, plus SPEND that approval single-use once a human approved it. The
// composition root adapts its *approvalBridge to it. Status is a governance.GateStatus* value.
type ApprovalOpener = effectgate.Bridge

// KillSwitchGuard reads the estate kill-switch state. *governance.Module satisfies it.
type KillSwitchGuard interface {
	KillSwitchState(ctx context.Context, tenant model.TenantID) (governance.StopState, error)
}

// NHIEnforcer reports whether an agent's bound NHI is blocked by its lifecycle.
type NHIEnforcer interface {
	NHIEnforcementForAgentRef(ctx context.Context, tenant model.TenantID, agentRef string) (blocked bool, reason string, err error)
}

// ContentInspector is the hooks-hardening content firewall.
type ContentInspector interface {
	Inspect(ctx context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision
}

// SessionTokenPrefix marks a session hook credential minted by auth.SessionCredentials.
const SessionTokenPrefix = "olvsess_"

// SessionCredentials is the session-bound credential plane: the engine's session hook
// credentials resolve a session bearer to its principal and launch scope.
type SessionCredentials interface {
	Resolve(ctx context.Context, token string) (auth.Principal, auth.SessionScope, error)
	ResolveRun(ctx context.Context, tenant model.TenantID, runRef string) (auth.Principal, auth.SessionScope, error)
}

// Decider is the GOVERNED brain: firm-identity gate → policy disposition
// → live PDP hard-deny overlay → ask→HITL → rewrite. Every edge is
// deny-closed.
type Decider struct {
	// The engine planes, set by the composition root before the first decision and never
	// changed after it. A nil plane disables the gate it feeds, as documented per field.
	Tenants       map[model.TenantID]ResolvedTenant
	DefaultPolicy *PolicyDoc
	Authr         PrincipalAuthenticator
	Eval          auth.PolicyEvaluator // nil ⇒ no external overlay (the disposition still governs)
	// Scoped is the central scoped grant/forbid engine (F-03). The hook consults its
	// FORBID contribution as a FURTHER-RESTRICT overlay so a central scoped forbid that
	// targets the projected tool-call resource (an mcp_server) or the principal denies the
	// call at the hook too — the same forbid-overrides-allow algebra REST/model/MCP run.
	// nil ⇒ no scoped overlay (behavior unchanged). It never widens a disposition.
	Scoped auth.ScopedAuthorizer
	// Authz is the engine authorizer that admitted the launch. Nil denies a session tool-call.
	Authz       *auth.Authorizer
	Bridge      effectgate.Bridge // legacy external-hook compatibility
	Approvals   *governance.EngineApprovals
	NHIEnforcer NHIEnforcer     // nil ⇒ no NHI-lifecycle deny gate
	Stops       KillSwitchGuard // nil ⇒ no kill-switch gate (boot always wires it)
	// StopDeny records throttled tamper-evident deny evidence (nil ⇒ not recorded).
	StopDeny func(ctx context.Context, tenant model.TenantID, stopID model.ID, surface, subject, actor string)
	Store    store.Store // terminal allow/deny evidence ledger
	// SubjectRef is the bridge's plan-bound subject_ref encoding (nil ⇒ no session review).
	SubjectRef func(subjectRef, planHash string) string

	// The shared human queue projects only an active wait onto the supervised run.
	ApprovalWait       func(context.Context, auth.Principal, string, time.Time) (func(), error)
	RedactSecrets      func(model.TenantID, string, []byte) ([]byte, []sessions.SecretMaskSpan, bool)
	SessionObservation func(context.Context, auth.Principal, sdkmodel.Observation) error

	// Inspector is the OPTIONAL commercial hooks-hardening DLP firewall (enterprise/hookhardening). nil in the default AGPL build ⇒ no deep tool_input
	// inspection (the PEP behaves exactly as before — no rug-pull); under -tags enterprise
	// WITH a config it runs DLP + structural detection over the tool arguments as a
	// further-restrict overlay (claudehookfirewall.go).
	Inspector ContentInspector
	// Bus carries the firewall's posture findings + per-inspection metering (nil ⇒ no-op).
	Bus   eventbus.Bus
	Clock func() time.Time
	Log   *slog.Logger
	// observeExpired (E3) latches a tenant whose observe window has passed: once the live
	// clock has reached observeUntil, the grant is permanently inactive for this process, so a
	// clock ROLLBACK can never resurrect an expired relaxation. Keyed by model.TenantID.
	observeExpired sync.Map

	// tracer and sessionTrace (#429) emit the GenAI execute_tool span of each
	// governed decision, parented on the caller session's invoke_agent span when
	// the bearer is a live run's. Telemetry only: a nil tracer or an unresolvable
	// session changes no decision.
	Tracer       *obstrace.Provider
	SessionTrace func(model.TenantID, string) (oteltrace.SpanContext, string, bool)
}

// observeGrantActive reports whether the tenant's observe grant is live RIGHT NOW. Fail-safe:
// a nil clock, a zero grant window, an already-latched-expired tenant, or now >= observeUntil
// all return false (⇒ the caller enforces). Reaching the window latches the tenant expired so a
// backward clock jump can never re-activate it. Callers gate observe on this AND rt.mode.
//
// A zero clock reading (now.IsZero()) is treated as expiry and LATCHES the tenant to enforce for
// the process — deliberately the safe direction (over-enforce, never relax). It is unreachable from
// production time.Now(); the latch is the guarantee (observe self-reverts and stays reverted), not
// a gap.
//
// The expiry test is the STRICTER of a wall-clock check (now >= observeUntil) and a MONOTONIC
// elapsed check (now.Sub(bootMono) >= window). In production both d.Clock() and observeBootMono
// carry a monotonic reading, so the elapsed test cannot shrink under a backward system-clock jump —
// it expires the grant at the right real instant even if NO request arrived during the window (the
// wall test alone would be fooled by a rollback with no intervening traffic to latch it). A forward
// wall jump is caught by the wall test. Reaching either bound latches the tenant expired.
func (d *Decider) observeGrantActive(rt ResolvedTenant) bool {
	if rt.observeUntil.IsZero() || d.Clock == nil {
		return false // no valid grant / no clock ⇒ never observe (deny-closed)
	}
	if _, latched := d.observeExpired.Load(rt.tenant); latched {
		return false // once expired, never active again (clock-rollback guard)
	}
	now := d.Clock()
	expired := now.IsZero() || !now.Before(rt.observeUntil) // wall: now >= until (inclusive, mirrors approvals)
	if !expired && !rt.observeBootMono.IsZero() {
		expired = now.Sub(rt.observeBootMono) >= rt.observeWindow // monotonic elapsed ≥ window (rollback-immune)
	}
	if expired {
		d.observeExpired.Store(rt.tenant, struct{}{})
		return false
	}
	return true
}

var _ claude.HookDecider = (*Decider)(nil)

// Decide is the full governed decision. It NEVER returns an ALLOW without (a) a resolved,
// policy-sufficient firm identity, (b) a disposition that is not deny, and (c) the live
// PDP not forbidding the call; the ask path requires an effective approval bound to
// the exact plan hash. Any error path is a DENY (the connector also treats a returned
// error as deny, so this is belt-and-suspenders).
func (d *Decider) Decide(ctx context.Context, in claude.HookDecisionInput, bearer string) (claude.HookDecisionResult, error) {
	// Resolve once and carry the same authenticated principal through authorization
	// and evidence attribution. Re-authenticating only for the anchor could observe a
	// different revocation state and mislabel the decision that was actually made.

	var principal auth.Principal
	var scope auth.SessionScope
	var authErr error
	if credentials, ok := d.Authr.(SessionCredentials); ok && strings.HasPrefix(bearer, SessionTokenPrefix) {
		proof, _ := ctx.Value(claudeHookProofKey{}).(*claudeHookProof)
		if proof != nil && proof.bearer == bearer {
			principal, scope, authErr = proof.principal, proof.scope, proof.err
		} else {
			principal, scope, authErr = credentials.Resolve(ctx, bearer)
			proof = &claudeHookProof{bearer: bearer, principal: principal, scope: scope, err: authErr}
			defer proof.closeRetention()
			ctx = context.WithValue(ctx, claudeHookProofKey{}, proof)
		}
		if scope.TenantID.IsZero() || (in.Identity.Tenant != "" && in.Identity.Tenant != scope.TenantID.String()) {
			authErr = auth.ErrUnauthenticated
		}
		if !scope.TenantID.IsZero() {
			in.Identity.Tenant = scope.TenantID.String()
			in.SessionID = scope.RunRef
		}
	} else {
		proof, _ := ctx.Value(claudeHookProofKey{}).(*claudeHookProof)
		if proof != nil && proof.bearer == bearer {
			principal, authErr = proof.principal, proof.err
		} else {
			principal, authErr = d.Authr.Authenticate(ctx, bearer)
		}
	}

	if !scope.TenantID.IsZero() {
		resolved := auth.Principal{}
		if authErr == nil {
			resolved = principal
		}
		ctx = context.WithValue(ctx, sessionObservationPrincipalKey{}, resolved)
	}

	if authErr == nil && principal.SessionIdentity != "" {
		ctx = context.WithValue(ctx, sessionHookScopeKey{}, scope)
	}

	// One execute_tool span per governed decision (#429), parented on the
	// session's invoke_agent span when the bearer names a live run (the
	// normalization above already made in.SessionID the run reference). A
	// non-session caller keeps its own conversation id and a standalone root.
	// The span ends with the decision; the outcome rides as an attribute, and a
	// decision that could not be MADE (below) is the only error span.
	toolSpan := d.startToolSpan(authErr, scope, in)
	defer toolSpan.End()

	// A launched session's resolve refuses its bearer once a stop scopes it, so
	// no tenant would resolve while deciding. Its server-side scope still names
	// the tenant and the agent: the stop is then the reason and leaves its
	// evidence. An unreadable stop state keeps the ordinary refusal; both deny.
	var res claude.HookDecisionResult
	var tenant model.TenantID
	var shadow *shadowVerdict
	stopped := false
	if authErr != nil && !scope.TenantID.IsZero() && d.Stops != nil && claude.HookEnforcementFor(in.Event).Enforceable {
		actor := "session:" + scope.SessionRef
		reason, err := d.stopReason(ctx, scope.TenantID, scope.AgentRef, in, actor)
		if err == nil && reason != "" {
			res, tenant, stopped = deny(reason, actor, TierUnknown, ""), scope.TenantID, true
		} else if err != nil && d.Log != nil {
			d.Log.Warn("hook-pep: stop state unreadable for a refused session bearer; the refusal stands", "tenant", scope.TenantID.String(), "err", err)
		}
	}
	if !stopped {
		var err error
		res, tenant, shadow, err = d.decide(ctx, in, principal, authErr)
		if err != nil {
			toolSpan.RecordFailure()
			return res, err // the error from the inner decider is deny-closed in the connector
		}
	}
	if res.Permission == claude.DecisionAllow && principal.SessionIdentity != "" {
		if credentials, ok := d.Authr.(SessionCredentials); ok {
			if _, _, err := credentials.Resolve(ctx, bearer); err != nil {
				res = deny("session credential changed while deciding this tool-call", res.PrincipalActor, res.IdentityTier, res.PolicyVersion)
				shadow = nil
			}
		}
	}
	if tenant.IsZero() && !scope.TenantID.IsZero() {
		tenant = scope.TenantID
	}
	actAs, delegated := principal.ActAs()
	if authErr != nil {
		actAs, delegated = "", false
	}
	if res.PrincipalActor == "" && scope.SessionRef != "" {
		// A known revoked bearer still has an immutable identity for attribution;
		// the failed resolution conveys no permission to perform the tool call.
		res.PrincipalActor = "session:" + scope.SessionRef
	}
	var projectedRefs []string
	if projected, ok := projectHookScopedRequest(principal, tenant, in); ok {
		projectedRefs = append(projectedRefs, projected.Resource.ID)
	}
	retain := d.hookEvidenceRedactor(ctx, in, projectedRefs...)
	if !stopped { // the stop reason is server text; a refused proof withholds any other
		res.Reason = retain(res.Reason)
	}
	res.AdditionalContext = retain(res.AdditionalContext)
	if shadow != nil {
		retained := *shadow
		retained.reason = retain(shadow.reason)
		shadow = &retained
	}
	out := d.anchorDecision(ctx, tenant, in, res, shadow, AuditDelegation{
		IsDelegated:  delegated,
		ActAs:        actAs.String(),
		SessionScope: scope,
	})
	// B-05: the decision is now SAID, not only made. It is emitted after the
	// anchor and after `out` is final, so a slow, full or absent bus can never turn
	// an allow into a deny or the other way round — the verdict is already decided
	// and already recorded before anything is published.
	d.publishDecisionSignal(ctx, tenant, in, out.Permission == claude.DecisionAllow, out.Reason)
	toolSpan.SetDecision(out.Permission)
	return out, nil
}

// stopReason names the active stop (estate-wide or agentRef's) that scopes a
// tool-call and records its throttled tamper-evident deny evidence; "" when none.
func (d *Decider) stopReason(ctx context.Context, tenant model.TenantID, agentRef string, in claude.HookDecisionInput, actor string) (string, error) {
	st, err := d.Stops.KillSwitchState(ctx, tenant)
	if err != nil {
		return "", err
	}
	stopID, stopped := st.Stopped(agentRef)
	if !stopped {
		return "", nil
	}
	if d.StopDeny != nil {
		d.StopDeny(ctx, tenant, stopID, "hooks-pep", firstNonEmptyStr(agentRef, d.retainedHookText(ctx, in.Tool)), actor)
	}
	return "emergency stop active (kill switch " + stopID.String() + "); all governed tool-calls are denied until a dual-control re-enable", nil
}

// startToolSpan opens the execute_tool span of one governed decision. Nil-safe:
// with no tracer wired (or tracing disabled) every method of the returned span
// is a no-op, so the decision path is unchanged. A caller that did not
// authenticate gets no span: its tool name and session id are request input
// nobody vouched for, and must not name spans or forge a session's conversation.
// The conversation id is the authenticated scope's run reference, never the
// payload's.
func (d *Decider) startToolSpan(authErr error, scope auth.SessionScope, in claude.HookDecisionInput) *obstrace.AgentSpan {
	if d.Tracer == nil || authErr != nil {
		return nil
	}
	var parent oteltrace.SpanContext
	if !scope.TenantID.IsZero() && d.SessionTrace != nil {
		if sc, _, ok := d.SessionTrace(scope.TenantID, scope.RunRef); ok {
			parent = sc
		}
	}
	return d.Tracer.ExecuteTool(parent, scope.AgentRef, in.Tool, scope.RunRef)
}

// sessionRunAdmitted asks the authorizer that admits a launch whether p still holds perm
// on the session's run: RBAC or a scoped grant, then scoped forbids and the deny-overlay.
// A nil authorizer refuses.
func SessionRunAdmitted(ctx context.Context, authz *auth.Authorizer, p auth.Principal, tenant model.TenantID, perm auth.Permission, runRef string, workspace model.ID) auth.Decision {
	if authz == nil {
		return auth.Decision{Reason: "session run authorizer is not wired"}
	}
	return authz.Authorize(ctx, auth.Request{Principal: p, Tenant: tenant, Permission: perm,
		Resource: auth.ResourceAttrs{Kind: perm.Resource(), ID: runRef, WorkspaceID: workspace}})
}

// shadowVerdict is the record of a would-be AUTHORED-policy verdict that observe mode
// SHADOWED (allowed but recorded). It is an internal decider carrier — it never
// enters the connector's HookDecisionResult nor the wire, only the tamper-evident ledger via
// anchorDecision, so a shadowed call is evidence-complete for the operator's promotion report
// while remaining a clean allow to the running agent. decision is "deny" | "ask".
type shadowVerdict struct {
	decision string // "deny" | "ask" — the would-be verdict enforce would have returned
	source   string // shadowSource* — the PRODUCER of the would-be verdict (aggregation axis)
	reason   string // human-readable context (free text; NOT the aggregation key)
	grantID  string // observe grant content id (groups a promotion report by observe window)
}

// E3 ledger meta contract for a constrained-observe shadow. Defined ONCE and shared by
// the writer (addShadowMeta / the anchor) and the reader (the `audit observe-report` CLI), so
// a key rename can never silently desync the two and vacuously zero the promotion report.
const (
	MetaEnforcementMode    = "enforcement_mode"    // "observe" on a shadowed decision
	MetaShadowedDecision   = "shadowed_decision"   // "deny" | "ask" — the would-be verdict
	MetaShadowSource       = "shadow_source"       // shadowSource* — producer of the would-be verdict
	MetaShadowReason       = "shadow_reason"       // free-text context
	MetaObserveScope       = "observe_scope"       // ObserveScopeTenant (the grant granularity)
	MetaObserveGrantID     = "observe_grant_id"    // content digest of the grant terms (distinguishes windows)
	MetaEffectiveDowngrade = "effective_downgrade" // true when a shadowed allow fail-closed to deny at the anchor
	MetaDecisionAttemptID  = "decision_attempt_id" // per-decision nonce (dedupe an ambiguous-commit double-write)

	EnforcementModeObserve = "observe"
	ObserveScopeTenant     = "tenant"
)

// shadowSource* name the PRODUCER of a shadowable (ClassPolicy) deny/ask. Orthogonal to the
// shadowed_decision (deny|ask): the same producer can emit either. Stable strings (the
// promotion report aggregates on them).
const (
	ShadowSourcePDP          = "pdp"           // live PDP hard-deny overlay (Cedar/ABAC forbid)
	ShadowSourceScoped       = "scoped"        // central scoped-forbid overlay (authored, non-confinement)
	ShadowSourceLocalRule    = "local_rule"    // a matched authored policy rule
	ShadowSourceLocalDefault = "local_default" // the explicit authored default disposition
	ShadowSourceBashPath     = "bash_path"     // an authored Bash path/subtree rule
)

// decide computes the governed disposition and returns the tenant resolved for it. A
// zero tenant marks a rejection that happened before a trustworthy tenant was resolved.
func (d *Decider) decide(ctx context.Context, in claude.HookDecisionInput, principal auth.Principal, authErr error) (claude.HookDecisionResult, model.TenantID, *shadowVerdict, error) {
	// 2. Resolve the tenant: the declared hint, else the principal's sole grant. Ambiguous
	//    or unconfigured ⇒ deny-closed (no governed policy to apply).
	tenant, tok := d.resolveTenant(in.Identity.Tenant, principal, authErr)
	if !tok.found {
		return deny(tok.reason, "", TierUnknown, ""), model.TenantID(""), nil, nil
	}
	rt, configured := d.Tenants[tenant]
	if !configured && d.DefaultPolicy != nil {
		rt = ResolvedTenant{tenant: tenant, policy: *d.DefaultPolicy}
	}
	tier := firmnessTier(principal, tenant, authErr)
	actor := ""
	if authErr == nil {
		actor = principal.Actor()
		if principal.SessionIdentity != "" {
			actor = "session:" + principal.SessionIdentity
		}
	}
	version := firstNonEmptyStr(rt.policy.Version, hookPEPPolicyVersionFallback)
	agent := resolveHookAgent(principal, authErr, in.Identity.Agent)
	if agent.contradicted() && d.Log != nil {
		d.Log.Warn("hook-pep: declared agent differs from the agent the credential proves; the credential's agent governs",
			"tenant", tenant.String(), "agent", agent.proven, "declared_agent", ellipsis(d.retainedHookText(ctx, agent.hint), maxLoggedHookAgentHint))
	}

	// 2b. Non-enforceable events: context/observe events and the INVERTED
	//     Stop/SubagentStop cannot be blocked by a hook return — a "block" on Stop would KEEP
	//     the agent running (the opposite of safe). The PEP returns a NEUTRAL, audited
	//     pass-through for them (no firm-identity gate, no HITL, no kill-switch deny — there
	//     is nothing to gate, and letting a Stop stop is the safe direction under any state).
	//     An UNKNOWN event is enforceable (deny-closed), so it does NOT short-circuit here.
	if !claude.HookEnforcementFor(in.Event).Enforceable {
		return neutralHookVerdict(in.Event, actor, tier, version), tenant, nil, nil
	}

	// A launched agent cannot keep editing after its launcher loses that authority.
	// The launch's authorizer answers, so a scoped grant that admitted the launch admits
	// the tool-call. Tool policy may further restrict that authority; it cannot widen it.
	if authErr == nil && principal.SessionIdentity != "" {
		verb := auth.VerbWrite
		if in.Mode == "read" {
			verb = auth.VerbRead
		}
		if admitted := SessionRunAdmitted(ctx, d.Authz, principal, tenant, auth.Permission("sessions:run:"+verb), principal.SessionRunRef, principal.SessionWorkspaceID); !admitted.Allow {
			// The agent gets the fixed reason; the operator gets the authorizer's own,
			// so an unwired authorizer or a policy outage is not read as lost authority.
			if d.Log != nil {
				d.Log.Warn("hook-pep: launcher's run authority refused", "tenant", tenant.String(), "run", principal.SessionRunRef, "reason", admitted.Reason)
			}
			return deny("launcher's current authority does not permit this tool-call", actor, tier, version), tenant, nil, nil
		}
	}

	// 3. Firm-identity gate: a policy that requires firm attribution denies a
	//    tool-call the PEP can only attribute approximately or not at all.
	if rt.requireFirm && tier != TierFirm {
		return deny("firm identity required by policy but attribution is "+tier+"", actor, tier, version), tenant, nil, nil
	}

	// 3b. NHI-lifecycle deny gate: when the tenant opts in, deny a tool-call by an
	//     agent whose bound NHI is BLOCKED (stale-escalated past its window, or offboarded)
	//     — the offboarding cascade and the staleness block reaching the actuation surface.
	//     Fail-closed on a lookup error, consistent with the governed PEP's posture; a clean
	//     "not blocked" (or an unmanaged/unresolvable agent) proceeds, so it never breaks
	//     day-1 operations. The agent is the credential's; the declared hint stands in only
	//     when the credential binds none (hookAgent.restrictRef).
	if agentRef := agent.restrictRef(); rt.enforceNHI && d.NHIEnforcer != nil && agentRef != "" {
		blocked, why, nerr := d.NHIEnforcer.NHIEnforcementForAgentRef(ctx, tenant, agentRef)
		if nerr != nil {
			return deny("NHI lifecycle enforcement check failed (deny-closed)", actor, tier, version), tenant, nil, nil
		}
		if blocked {
			return deny("NHI blocked by lifecycle policy: "+why, actor, tier, version), tenant, nil, nil
		}
	}

	// 3c. Estate kill switch: while an emergency stop scopes this call
	//     (estate-wide, or the declared agent), EVERY tool-call is denied —
	//     deliberately BEFORE the disposition AND the HITL/break-glass path, so
	//     an active break-glass grant can never re-authorize tool-calls during a
	//     stop (the stop outranks the emergency valve; recovery is the governed
	//     dual-control re-enable). Fail-closed on a state read error; the deny
	//     is recorded to the tamper-evident ledger (throttled) — the evidence
	//     pack's "PEP decisions" leg. The agent is resolved as for the NHI gate.
	//     A session principal's resolve has just read its stop history (the
	//     same governance rows, eng.killSwitch), which refuses the bearer under
	//     any active stop of the estate or its agent. Only a hint declared by a
	//     credential that proves no agent is outside that history; it reads here.
	sessionStopRead := false
	if credentials, ok := d.Authr.(interface{ ResolvesStopHistory() bool }); ok && authErr == nil && principal.SessionIdentity != "" {
		sessionStopRead = credentials.ResolvesStopHistory() && agent.restrictRef() == agent.proven
	}
	if d.Stops != nil && !sessionStopRead {
		reason, err := d.stopReason(ctx, tenant, agent.restrictRef(), in, actor)
		if err != nil {
			return deny("kill-switch state unreadable (deny-closed)", actor, tier, version), tenant, nil, nil
		}
		if reason != "" {
			return deny(reason, actor, tier, version), tenant, nil, nil
		}
	}

	// 4. Disposition from the governed policy. A matching rule wins; with NO rule the default
	//    is event-class-aware (2026-06-19 D3): a classic permission gate
	//    (PreToolUse/PermissionRequest/UNKNOWN) honors the operator's policy default
	//    (deny-closed); a state-MUTATING gating event (ConfigChange) defaults DENY; the
	//    UX/agent-loop gating events (UserPromptSubmit, PreCompact, TaskCreated, …) default
	//    NEUTRAL — blocking them without an explicit rule would break the session, never a
	//    blind allow either (the pass-through is audited).
	disp, matched := evalHookPolicy(rt.policy, in)
	if !matched {
		disp = defaultHookDisposition(in.Event, claude.HookEnforcementFor(in.Event), rt.policy)
	}

	// 4b.: Bash path/subtree enforcement. Bash's ResourceRef is the program only
	//     (the connector discards argv), so a path deny/ask cannot come from evalHookPolicy;
	//     inspect the raw command and FURTHER-RESTRICT the disposition (never widen).
	if in.ResourceKind == ResourceKindShell {
		disp = bashPathScan(rt.policy, in, "" /*root intentionally empty: unresolved traversal asks*/, disp)
	}

	// The person's launch preset can only restrict the tenant disposition. It is
	// bound to the engine's credential, never supplied by the hook payload.
	if authErr == nil && principal.SessionIdentity != "" {
		var err error
		disp, err = restrictSessionHookPreset(ctx, principal, tenant, in, disp)
		if err != nil {
			return deny("session permission preset is unavailable", actor, tier, version), tenant, nil, nil
		}
	}

	// Constrained-observe. In observe mode an AUTHORED (ClassPolicy) deny/ask is
	// SHADOWED — recorded and allowed — while EVERY platform invariant still enforces:
	// invariant denials (identity, tenancy confinement, kill switch, firewall, fail-closed
	// errors, unclassified/unknown-class) short-circuit exactly as in enforce; only a deny/ask
	// whose class == ClassPolicy is turned into a recorded allow, and only after ALL invariant
	// controls below have run (invariant dominates). enforce — the default and fail-safe mode —
	// leaves every branch below byte-for-byte identical (observe is false, every guard falls
	// through to the original early-return). Shadowability is keyed EXACTLY on == ClassPolicy,
	// so a zero/unknown class is never shadowable.
	// observe requires BOTH the tenant mode AND a live (unexpired) grant; an expired/invalid
	// grant, or a rolled-back clock, deny-closes to enforce (observeGrantActive). Sampled once
	// per decision (snapshot: a call authorized just before observeUntil may finish after it).
	observe := rt.mode == modeObserve && d.observeGrantActive(rt)
	var shadow *shadowVerdict
	recordShadow := func(decision, source, reason string) {
		if shadow == nil { // first would-be verdict wins (mirrors enforce's first-deny reason)
			shadow = &shadowVerdict{decision: decision, source: source, reason: reason, grantID: rt.observeGrantID}
		}
	}

	// 5. Live PDP hard-deny overlay (Cedar/ABAC): further-restrict only. A forbid's
	//    class is the evaluator's own (invariant-dominates chain, E1a/E1b); only an AUTHORED
	//    policy forbid is shadowable, an eval error / invariant forbid denies even in observe.
	if authErr == nil && d.Eval != nil {
		if pdpDenied, reason, class := d.pdpForbids(ctx, principal, tenant, in); pdpDenied {
			full := "PDP policy forbids this tool-call: " + reason
			if observe && class == auth.ClassPolicy {
				recordShadow(claude.DecisionDeny, ShadowSourcePDP, full)
			} else {
				return deny(full, actor, tier, version), tenant, nil, nil
			}
		}
	}

	// 5a. F-03: central scoped-forbid overlay (forbid-overrides-allow). A WORKSPACE
	//     CONFINEMENT or fail-closed forbid is ClassInvariant and denies even in
	//     observe; only an AUTHORED scoped forbid is shadowable. The class is taken from the
	//     engine (scopedForbids), never assumed, so confinement can never be shadowed.
	if authErr == nil && d.Scoped != nil {
		if scopedDenied, reason, class := d.scopedForbids(ctx, principal, tenant, in); scopedDenied {
			full := "central scoped policy forbids this tool-call: " + reason
			if observe && class == auth.ClassPolicy {
				recordShadow(claude.DecisionDeny, ShadowSourceScoped, full)
			} else {
				return deny(full, actor, tier, version), tenant, nil, nil
			}
		}
	}

	// 5b. Hook content firewall (commercial add-on): deep DLP + structural inspection over
	//     the tool_input ARGUMENTS. It is an INVARIANT further-restrict control that ALWAYS
	//     enforces (a secret/PII value, exfil sink, unsafe command or un-inspectable argument
	//     DENIES; it never widens). nil inspector (the default AGPL build) ⇒ inert pass-through.
	//     Run it whenever the call MIGHT proceed: not a local deny, OR a local deny that observe
	//     will shadow (so the DLP inspection covers the call that will actually run). Keep the
	//     enforce skip for a local deny we will honor regardless (an INVARIANT local deny, or
	//     enforce mode): running the (regex) inspection, billing its meter and emitting findings
	//     for a call we deny anyway is wasted work and a spurious billable event. Its agent-scoped
	//     policy is selected by the credential's agent only, never by the declared hint.
	if disp.decision != claude.DecisionDeny || (observe && disp.class == auth.ClassPolicy) {
		if fw := d.runHookFirewall(ctx, tenant, actor, agent, in); !fw.Forward {
			return deny(firstNonEmptyStr(fw.Reason, "blocked by Olivares hook content firewall"), actor, tier, version), tenant, nil, nil
		}
	}

	// Stored tool-use review gates before acting. PostToolUse remains a classic
	// gate for output policies, but must not request a second tool-use approval.
	// Hard denies above still win, and post/lifecycle events retain their audit.
	beforeToolUse := claude.HookEnforcementFor(in.Event).ClassicGate && in.Event != "PostToolUse"
	if beforeToolUse && principal.SessionIdentity != "" && d.Approvals != nil && disp.decision == claude.DecisionAllow {
		ref, err := d.Approvals.ReviewPolicy(ctx, tenant, ActionCapability, "claude.tool")
		if err != nil {
			return deny("approval policy is unavailable", actor, tier, version), tenant, nil, nil
		}
		if ref != "" {
			disp.decision = claude.DecisionAsk
			disp.reason = "human approval required by policy"
			disp.class = auth.ClassPolicy
		}
	}

	// Review can gate a tool before it runs, never its completed output or a
	// lifecycle notification. Retain output block/rewrite rules and their audit.
	if !beforeToolUse && disp.decision == claude.DecisionAsk {
		disp.decision = claude.DecisionAllow
	}

	// 6. Map the disposition to the governed verdict.
	switch disp.decision {
	case claude.DecisionDeny:
		if observe && disp.class == auth.ClassPolicy {
			// Business deny shadowed: record and fall through to the shadow tail (clean allow).
			recordShadow(claude.DecisionDeny, firstNonEmptyStr(disp.source, ShadowSourceLocalRule), firstNonEmptyStr(disp.reason, "blocked by Olivares governance policy"))
		} else {
			return deny(firstNonEmptyStr(disp.reason, "blocked by Olivares governance policy"), actor, tier, version), tenant, nil, nil
		}

	case claude.DecisionAsk:
		if grant, ok := ctx.Value(claudeReviewedCallKey{}).(claudeReviewedCall); ok && grant.run == in.SessionID && grant.plan == in.PlanHash {
			res := claude.HookDecisionResult{Permission: claude.DecisionAllow, PolicyVersion: version, PrincipalActor: actor, IdentityTier: tier, Reason: "human review approved this tool-call"}
			applyRewrite(&res, in, disp)
			return res, tenant, shadow, nil
		}
		if observe && disp.class == auth.ClassPolicy {
			// Business ask shadowed: record and fall through — NOT queued to HITL, so the pilot
			// never consumes an approval nor perturbs the F-02 single-use state.
			recordShadow(claude.DecisionAsk, firstNonEmptyStr(disp.source, ShadowSourceLocalRule), firstNonEmptyStr(disp.reason, "human approval required by policy"))
		} else {
			// ask ⇒ open (or idempotently find) a governed approval bound to the exact plan
			// hash, then map its effective status. Pending ⇒ ask (HITL queued); approved ⇒
			// allow; everything else ⇒ deny. No bridge wired ⇒ deny-closed. An INVARIANT ask
			// (unresolved path / Bash ambiguity) still gates. An earlier overlay ClassPolicy
			// shadow rides ONLY a terminal ALLOW — the call actually proceeds despite the
			// shadowed policy. A pending ask is anchored by the HITL bridge (not here) and a
			// gated deny is a plain deny; attaching a shadow to a non-allow would record a
			// would-be-allowed policy on a call that did not proceed.
			var res claude.HookDecisionResult
			if principal.SessionIdentity != "" {
				res = d.gateViaSessionApproval(ctx, tenant, principal, in, disp, actor, tier, version)
			} else {
				res = d.gateViaHITL(ctx, tenant, in, disp, actor, tier, version)
			}
			if res.Permission != claude.DecisionAllow {
				shadow = nil
			}
			return res, tenant, shadow, nil
		}

	default: // allow (with an optional governed rewrite)
		res := claude.HookDecisionResult{
			Permission:     claude.DecisionAllow,
			Reason:         firstNonEmptyStr(disp.reason, "permitted by Olivares governance policy"),
			PolicyVersion:  version,
			PrincipalActor: actor,
			IdentityTier:   tier,
			Block:          disp.block,
		}
		applyRewrite(&res, in, disp)
		// A local ALLOW keeps its governed Block/rewrite even when an OVERLAY policy (PDP/scoped)
		// was shadowed: the call proceeds with the local allow's effects; the shadow rides in the
		// carrier for the ledger, and the agent sees the ordinary local-allow reason.
		return res, tenant, shadow, nil
	}

	// Shadow tail: reached ONLY when a local ClassPolicy deny/ask was shadowed (fell through
	// the switch). A clean allow with NO Block/rewrite (those belonged to the would-be deny/ask,
	// which no human approved) and an EMPTY reason: it is NOT a policy permit — the policy would
	// have denied — so we surface no claim to the agent (a clean observe measurement), while the
	// ledger carries the full shadow via the carrier. shadow is guaranteed non-nil here.
	return claude.HookDecisionResult{
		Permission:     claude.DecisionAllow,
		Reason:         "",
		PolicyVersion:  version,
		PrincipalActor: actor,
		IdentityTier:   tier,
	}, tenant, shadow, nil
}

// anchorDecision anchors the TERMINAL governed disposition (allow/deny) to the hash-chain
// ledger — the tamper-evident, offline-verifiable evidence the product promises. ASK is anchored
// by the HITL bridge, so it is skipped here. Fail-closed per the F9 evidence-or-refuse law
// (sdk/evidence.go): an ALLOW whose EvidenceReceipt is not AnchoredFor its binding is downgraded
// to DENY (no evidence, no action); a DENY that cannot be recorded stands + LOUD gap log.
// Carries NO raw args/bearer/secrets (docs/SECURITY-HARDENING.md) — the PayloadHash commits to the salient
// fields and Meta holds only non-sensitive descriptors.
func (d *Decider) anchorDecision(ctx context.Context, tenant model.TenantID, in claude.HookDecisionInput, res claude.HookDecisionResult, shadow *shadowVerdict, delegation AuditDelegation) claude.HookDecisionResult {
	decision := firstNonEmptyStr(res.Permission, claude.DecisionDeny)
	if decision == claude.DecisionAsk {
		return res // the HITL bridge anchors the ask/approval leg
	}
	if tenant.IsZero() {
		// Pre-tenant reject (e.g. malformed request) — no tenant context to anchor to.
		return res
	}
	// One nonce per decision, carried on BOTH the original anchor and any downgrade re-anchor,
	// so a reader can correlate them and detect an ambiguous-commit double-write (the ALLOW
	// commit landed but its confirmation was lost, then the DENY re-anchor also lands).
	attemptID := newDecisionAttemptID()
	actor := firstNonEmpty(res.PrincipalActor, model.ActorSystem)
	phase := hookPhaseTerminalDeny
	if decision == claude.DecisionAllow {
		phase = hookPhaseTerminalAllow
	}
	ph := hookDecisionHash(in.Event, in.Tool, in.ResourceKind, in.ResourceRef, in.Mode, in.PlanHash, decision, res.PrincipalActor, res.PolicyVersion)
	binding := hookEvidenceBinding(tenant, attemptID, phase, ph, hookEffectiveResultDigest(res))
	anchorCtx := ctx
	if decision != claude.DecisionAllow {
		// A cancelled tool-call is still a governed denial. Its evidence must
		// survive the helper disconnect, with a bounded store operation.
		var cancel context.CancelFunc
		anchorCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), ReanchorTimeout)
		defer cancel()
	}
	receipt := d.anchor(anchorCtx, tenant, in, res, decision, actor, ph, shadow, delegation, attemptID, false, phase, binding)
	if !receipt.MustRefuse(binding) {
		return res
	}
	// A DENY that reached here was already the terminal verdict; it could not be anchored
	// (an evidence gap) but it STANDS — deny-closed — with a loud gap log. Under a DEGRADE
	// drop the loss accounting is already durably committed (fault=spool_degraded), so the
	// gap is counted and will seal as a signed in-chain marker.
	if decision != claude.DecisionAllow {
		if d.Log != nil {
			d.Log.Error("hook-pep: ledger anchor refused on DENY (evidence gap)", "fault", string(receipt.Fault), "tool", d.retainedHookText(ctx, in.Tool))
		}
		return res
	}
	// Fail-closed downgrade: an ALLOW with no evidence becomes a DENY (no evidence, no action).
	// Re-anchor the DOWNGRADED deny — a hash recomputed for decision=deny, the SAME
	// decision_attempt_id, a phase-differentiated binding (downgraded-deny, so the compensating
	// record never reads as a same-operation different-digest replay of the allow attempt), any
	// shadow, and effective_downgrade — so the promotion report still sees the would-be verdict
	// under a transient ledger failure. The re-anchor runs on a DECOUPLED context (WithoutCancel
	// + a bounded timeout): the caller's ctx may already be canceled (the agent hung up) — and
	// if the original ALLOW commit was AMBIGUOUS (persisted but its confirmation lost), skipping
	// the re-anchor would leave a bare ALLOW that misreads as "proceeded" though the effective
	// verdict was DENY. It is a SINGLE best-effort attempt; the deny STANDS unconditionally
	// whether or not it lands — NEVER an allow.
	if d.Log != nil {
		d.Log.Error("hook-pep: ledger anchor refused on ALLOW; downgrading to DENY (deny-closed)", "fault", string(receipt.Fault), "tool", d.retainedHookText(ctx, in.Tool))
	}
	downgraded := deny("evidence unavailable (deny-closed)", res.PrincipalActor, res.IdentityTier, res.PolicyVersion)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ReanchorTimeout)
	defer cancel()
	dph := hookDecisionHash(in.Event, in.Tool, in.ResourceKind, in.ResourceRef, in.Mode, in.PlanHash, claude.DecisionDeny, downgraded.PrincipalActor, downgraded.PolicyVersion)
	dbinding := hookEvidenceBinding(tenant, attemptID, hookPhaseDowngradedDeny, dph, hookEffectiveResultDigest(downgraded))
	if rreceipt := d.anchor(rctx, tenant, in, downgraded, claude.DecisionDeny, actor, dph, shadow, delegation, attemptID, true, hookPhaseDowngradedDeny, dbinding); rreceipt.MustRefuse(dbinding) && d.Log != nil {
		d.Log.Error("hook-pep: re-anchor of downgraded DENY refused (evidence gap); deny stands", "fault", string(rreceipt.Fault), "tool", d.retainedHookText(ctx, in.Tool))
	}
	return downgraded
}

// ReanchorTimeout bounds the single decoupled re-anchor of a downgraded deny so a wedged store
// cannot block the response indefinitely; the deny stands regardless of the outcome.
const ReanchorTimeout = 5 * time.Second

// AuditDelegation names both sides of an on-behalf-of decision for the ledger.
type AuditDelegation struct {
	IsDelegated  bool
	ActAs        string
	SessionScope auth.SessionScope
}

// AddAuditDelegationMeta names both sides of an on-behalf-of decision. The fields
// intentionally live in Meta, not PayloadHash: the ledger's canonical MetaDigest is
// already part of the event hash and Ed25519 signature, while keeping the v1 decision
// PayloadHash preimage stable avoids a second, redundant commitment format.
func AddAuditDelegationMeta(meta map[string]any, delegation AuditDelegation) {
	if !delegation.IsDelegated {
		return
	}
	meta["is_delegated"] = true
	meta["act_as"] = delegation.ActAs
}

// addShadowMeta records an constrained-observe SHADOW on the anchored decision: the call
// was ALLOWED (the effective "decision" already in meta) but an authored policy WOULD have
// denied/asked in enforce mode. The keys are distinct from "mode" (which holds the access mode
// read|write|unknown) so neither overwrites the other. nil ⇒ a normal enforce decision, no-op.
func addShadowMeta(meta map[string]any, shadow *shadowVerdict) {
	if shadow == nil {
		return
	}
	meta[MetaEnforcementMode] = EnforcementModeObserve
	meta[MetaShadowedDecision] = shadow.decision
	meta[MetaShadowSource] = shadow.source
	meta[MetaShadowReason] = shadow.reason
	meta[MetaObserveScope] = ObserveScopeTenant
	if shadow.grantID != "" {
		meta[MetaObserveGrantID] = shadow.grantID
	}
}

// hook evidence phases (q1-MCP): the phase differentiates the binding of the allow
// ATTEMPT from its compensating downgrade-deny, so the two records of one decision attempt
// never read as a same-OperationID-different-digest replay (sdk/evidence.go rebind rule).
const (
	hookPhaseTerminalAllow  = "terminal-allow"
	hookPhaseTerminalDeny   = "terminal-deny"
	hookPhaseDowngradedDeny = "downgraded-deny"
)

// Domain separators for the hook PEP's evidence binding derivation (length-prefixed
// encoding via writeLenPrefixed — uint64 big-endian length || bytes per field; never
// NUL-delimited joins, which are concatenation-ambiguous).
const (
	hookOperationDomain       = "olivares.hook.operation.v1"
	hookEffectDomain          = "olivares.hook.effect.v1"
	hookEffectiveResultDomain = "olivares.hook.effective-result.v1"
	hookPEPSurface            = "hook-pep"
)

// Evidence-binding commitment keys in the anchored event's canonical Meta (hashed into
// the chain via MetaDigest and Ed25519-signed), mirroring the operation_id/effect_digest
// commitment of modules/capabilities/toolpins.go — the event hash commits the binding,
// so the receipt's EvidenceRef provably came from an event committed FOR that exact
// binding (sdk/evidence.go anchoring discipline). "phase" does not collide with "mode"
// (the access mode) nor any shadow/delegation key.
const (
	metaEvidenceOperationID  = "operation_id"
	metaEvidencePhase        = "phase"
	metaEvidenceEffectDigest = "effect_digest"
)

// hookEffectiveResultDigest canonically digests the EFFECTIVE rendered result of a hook
// decision — every field that changes the wire form the connector emits
// (connectors/claude/pep.go renderHookDecision/postToolUseJSON): Permission, Reason,
// AdditionalContext, Block, ContinueOnBlock, and the governed rewrite (UpdatedInput).
// The legacy v1 PayloadHash (hookDecisionHash) deliberately omits these, so without this
// digest two DIFFERENT governed rewrites — or block vs pass-through — would share one
// EffectDigest under a fixed policy version (a version string is not policy content).
// Length-prefixed encoding throughout; an ABSENT UpdatedInput (nil — no rewrite) is
// distinguished from a PRESENT-but-empty one by an explicit presence marker, and a
// present map is committed via its canonical JSON (encoding/json sorts map keys at every
// depth, so the bytes are deterministic for the JSON-derived values a rewrite carries).
// PrincipalActor/PolicyVersion/IdentityTier are deliberately NOT here: the first two are
// already committed in the v1 PayloadHash the EffectDigest also binds, and the tier is
// audit attribution, not rendered effect.
func hookEffectiveResultDigest(res claude.HookDecisionResult) []byte {
	h := sha256.New()
	writeLenPrefixed(h, []byte(hookEffectiveResultDomain))
	writeLenPrefixed(h, []byte(res.Permission))
	writeLenPrefixed(h, []byte(res.Reason))
	writeLenPrefixed(h, []byte(res.AdditionalContext))
	writeLenPrefixed(h, []byte(hookBoolMark(res.Block)))
	writeLenPrefixed(h, []byte(hookBoolMark(res.ContinueOnBlock)))
	if res.UpdatedInput == nil {
		writeLenPrefixed(h, []byte("absent"))
	} else {
		writeLenPrefixed(h, []byte("present"))
		b, err := json.Marshal(res.UpdatedInput)
		if err != nil {
			// Unreachable for a rewrite (its values come from JSON unmarshal + the operator's
			// JSON policy config); if it ever happens the commitment stays deterministic per
			// error and cannot collide with a successful marshal ("!" never starts valid JSON).
			writeLenPrefixed(h, []byte("!marshal-error"))
			writeLenPrefixed(h, []byte(err.Error()))
		} else {
			writeLenPrefixed(h, b)
		}
	}
	return h.Sum(nil)
}

// hookBoolMark encodes a bool as a stable one-byte field for the length-prefixed digests.
func hookBoolMark(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// hookEvidenceBinding derives the sdk.EvidenceBinding for one hook anchor: the
// OperationID is namespaced from {tenant, decision_attempt_id, phase} (the attempt id is
// the per-decision nonce anchorDecision already mints; the phase separates the allow
// attempt from its compensating downgrade-deny), and the EffectDigest commits to
// {tenant, PEP surface, phase, hookDecisionHash, hookEffectiveResultDigest} — the v1
// payload commitment the ledger event carries PLUS the effective rendered result
// (rewrite/block/continue/context/reason), so the receipt is bound to exactly the effect
// that will be emitted, full effective request/effect included (sdk/evidence.go
// EffectDigest contract).
func hookEvidenceBinding(tenant model.TenantID, attemptID, phase string, payloadHash, effectiveResult []byte) sdk.EvidenceBinding {
	oh := sha256.New()
	writeLenPrefixed(oh, []byte(hookOperationDomain))
	writeLenPrefixed(oh, []byte(tenant.String()))
	writeLenPrefixed(oh, []byte(attemptID))
	writeLenPrefixed(oh, []byte(phase))
	eh := sha256.New()
	writeLenPrefixed(eh, []byte(hookEffectDomain))
	writeLenPrefixed(eh, []byte(tenant.String()))
	writeLenPrefixed(eh, []byte(hookPEPSurface))
	writeLenPrefixed(eh, []byte(phase))
	writeLenPrefixed(eh, []byte(hex.EncodeToString(payloadHash)))
	writeLenPrefixed(eh, []byte(hex.EncodeToString(effectiveResult)))
	return sdk.EvidenceBinding{
		OperationID:  sdk.OperationID(hex.EncodeToString(oh.Sum(nil))),
		EffectDigest: sdk.EffectDigest(hex.EncodeToString(eh.Sum(nil))),
	}
}

// classifyHookStoreFault maps a raw ledger transaction error to the evidence fault
// taxonomy (sdk/evidence.go). Called only when the transaction FAILED (a nil error
// classifies as EvidenceFaultNone via the default), so ClassifyAnchor can build the
// refused receipt. Mirrors modules/capabilities.classifyStoreFault.
func classifyHookStoreFault(err error) sdk.EvidenceFault {
	switch {
	case err == nil:
		return sdk.EvidenceFaultNone
	case errors.Is(err, store.ErrAuditSpoolFull):
		return sdk.EvidenceFaultSpoolFull
	case errors.Is(err, store.ErrNotLeader):
		return sdk.EvidenceFaultLedgerUnavailable
	default:
		return sdk.EvidenceFaultWriteError
	}
}

// anchor appends ONE decision event to the tenant ledger and classifies the outcome as an
// sdk.EvidenceReceipt for the given binding. It follows the F9 anchoring discipline
// (sdk/evidence.go): append INSIDE the transaction, but NEVER return a sentinel from
// inside on a degrade drop — that was the historical bug here: returning
// store.ErrAuditSpoolFull on ev.Seq == 0 rolled back the loss accounting the store had
// just committed (audit_spool_gaps), so the gap counter never advanced and its signed
// in-chain marker never sealed. Capture the drop, commit (return nil), classify AFTER.
// On a real (block-mode / write) error the callback returns it, rolling back — nothing
// durable, deny-closed.
func (d *Decider) anchor(ctx context.Context, tenant model.TenantID, in claude.HookDecisionInput, res claude.HookDecisionResult, decision, actor string, ph []byte, shadow *shadowVerdict, delegation AuditDelegation, attemptID string, downgraded bool, phase string, binding sdk.EvidenceBinding) sdk.EvidenceReceipt {
	if d.Store == nil {
		return sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnwired)
	}
	// A resolved principal is the agent's PEP credential — attribute the decision to the
	// agent, not the system, so the evidence names who acted (docs/SECURITY-HARDENING.md).
	actorKind := model.ActorSystem
	if strings.TrimSpace(res.PrincipalActor) != "" {
		actorKind = model.ActorAgent
	}
	retained := d.retainedHookInput(ctx, in)
	meta := map[string]any{
		"event": retained.Event, "tool": retained.Tool, "resource_kind": in.ResourceKind,
		"resource_ref": retained.ResourceRef, "mode": in.Mode, "plan_hash": in.PlanHash,
		"decision": decision, "policy_version": res.PolicyVersion, "identity_tier": res.IdentityTier,
		// Commit the evidence binding INTO the event's canonical meta (hashed by MetaDigest,
		// Ed25519-signed): the ledger record itself proves which {operation, effect} it
		// anchors, mirroring toolpins' operation_id/effect_digest commitment. The legacy v1
		// PayloadHash preimage stays untouched (compatibility).
		metaEvidenceOperationID:  string(binding.OperationID),
		metaEvidencePhase:        phase,
		metaEvidenceEffectDigest: string(binding.EffectDigest),
	}
	if in.SessionID != "" {
		meta["session_ref"] = in.SessionID
	}
	if scope := delegation.SessionScope; scope.SessionRef != "" {
		meta["session_ref"] = scope.SessionRef
		meta["run_ref"] = scope.RunRef
		meta["workspace_id"] = scope.WorkspaceID.String()
	}
	if attemptID != "" {
		meta[MetaDecisionAttemptID] = attemptID
	}
	if downgraded {
		// This DENY is the fail-closed downgrade of a shadowed ALLOW whose evidence write
		// failed; the shadow rides so the promotion report still counts the would-be verdict.
		meta[MetaEffectiveDowngrade] = true
	}
	AddAuditDelegationMeta(meta, delegation)
	addShadowMeta(meta, shadow)
	var appendDropped bool
	var evidenceRef string
	txErr := d.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:       actor,
			ActorKind:   actorKind,
			Action:      "hook.tool." + decision,
			TargetKind:  ActionCapability,
			TargetID:    model.ID(firstNonEmptyStr(retained.ToolUseID, in.PlanHash)),
			PayloadHash: ph,
			Meta:        meta,
		})
		if err != nil {
			return err // block-mode spool-full / write fault ⇒ roll back, deny
		}
		if ev.Seq == 0 {
			appendDropped = true // degrade drop: loss accounting durable; commit, refuse after
			return nil
		}
		evidenceRef = hex.EncodeToString(ev.Hash)
		return nil
	})
	return sdk.ClassifyAnchor(binding, evidenceRef, appendDropped, classifyHookStoreFault(txErr))
}

// resolveTenant resolves the request tenant from the declared hint or the principal's
// single grant, and confirms a governed policy is configured for it.
type tenantResolution struct {
	found  bool
	reason string
}

func (d *Decider) resolveTenant(hint string, p auth.Principal, authErr error) (model.TenantID, tenantResolution) {
	if authErr == nil && p.IsPurposeRestricted() {
		return model.TenantID(""), tenantResolution{reason: "purpose-restricted credential is not a hook credential; deny-closed"}
	}
	if h := strings.TrimSpace(hint); h != "" {
		tid, err := model.ParseTenantID(h)
		if err != nil || tid.IsZero() {
			return model.TenantID(""), tenantResolution{reason: "invalid tenant in request"}
		}
		if _, ok := d.Tenants[tid]; !ok && d.DefaultPolicy == nil {
			return model.TenantID(""), tenantResolution{reason: "no governed hook policy configured for tenant"}
		}
		// F-01: the hint is client-supplied (header X-Olivares-Hook-Tenant).
		// It may only SELECT the governing tenant when the caller is authenticated
		// AND actually a member of it (or superadmin); otherwise a member of tenant
		// A could name tenant B and borrow B's (possibly permissive) hook policy —
		// a cross-tenant governance-boundary escape. Mirrors the already-correct
		// inferenceProxyDecider.resolveTenant / appsGatewayHandler.resolveTenant.
		// Deny-closed on ambiguity.
		// staticcheck QF1001 suggests De Morgan's law here. Keep this deny-closed guard's
		// positive disjunction under a single negation: it directly expresses the policy
		// "must be a superadmin OR a member." The equivalent `!A && !B` form would require
		// anyone adding a third condition to remember to negate it too. That rewrite adds
		// risk to the membership check without changing behavior.
		//nolint:staticcheck // QF1001: the positive disjunction expresses the policy; see above
		if authErr != nil || !(p.Superadmin || p.IsMember(tid)) {
			return model.TenantID(""), tenantResolution{reason: "principal is not a member of the declared tenant; deny-closed"}
		}
		return tid, tenantResolution{found: true}
	}
	// No hint: fall back to the principal's sole grant when unambiguous.
	if authErr == nil {
		if ts := p.Tenants(); len(ts) == 1 {
			if _, ok := d.Tenants[ts[0]]; ok || d.DefaultPolicy != nil {
				return ts[0], tenantResolution{found: true}
			}
		}
	}
	return model.TenantID(""), tenantResolution{reason: "tenant not declared and not inferable; deny-closed"}
}

// maxLoggedHookAgentHint bounds the caller-chosen agent hint a mismatch log line repeats.
const maxLoggedHookAgentHint = 128

// hookAgent is the agent a governed hook request acts as, decided once per request from the
// authenticated credential. proven is the agent the credential binds (Principal.AgentIdentity; "" when
// it binds none); unbindable marks a request an agent-scoped policy cannot be matched to; hint is
// the agent the hook client declared (X-Olivares-Hook-Agent), which is advisory only.
type hookAgent struct {
	proven     string
	unbindable bool
	hint       string
}

// resolveHookAgent derives the request's agent from the credential alone. Every request to the
// hooks PEP is an agent's tool-call, so a credential that proves no agent — an API token with no
// agent binding, a human session, no credential at all — is unbindable whatever the client
// declares: whether the agent is proven must not depend on an optional header its caller can
// simply leave out. That is deliberately stricter than the inline proxy, which treats a human
// session as a person rather than an agent; the hook path therefore takes only the proven agent
// (Principal.AgentIdentity), not its classification. The hint never changes the classification; it
// only names an agent for the restrict-only checks (restrictRef).
func resolveHookAgent(p auth.Principal, authErr error, declared string) hookAgent {
	a := hookAgent{hint: strings.TrimSpace(declared)}
	if authErr == nil {
		a.proven = p.AgentIdentity
	}
	a.unbindable = a.proven == ""
	return a
}

// restrictRef is the agent the checks that can only ADD a denial consult (the agent-scoped
// emergency stop, the NHI lifecycle block): the credential's agent, else the declared hint.
// A hint can never select a policy — only the proven agent does (runHookFirewall).
func (a hookAgent) restrictRef() string {
	if a.proven != "" {
		return a.proven
	}
	return a.hint
}

// contradicted reports a declared agent that differs from the one the credential proves; the
// hint is then ignored and the mismatch logged.
func (a hookAgent) contradicted() bool {
	return a.proven != "" && a.hint != "" && a.hint != a.proven
}

// gateViaHITL opens/finds a governed approval bound to the plan hash and maps its status.
func (d *Decider) gateViaHITL(ctx context.Context, tenant model.TenantID, in claude.HookDecisionInput, disp hookDisposition, actor, tier, version string) claude.HookDecisionResult {
	if d.Bridge == nil {
		return deny("human approval required but the HITL bridge is not wired (deny-closed)", actor, tier, version)
	}
	subjectRef := in.ResourceRef
	if subjectRef == "" {
		subjectRef = in.Tool
	}
	// F-02 single-use: a human approval authorizes ONE execution, not a 24h-reusable
	// pass. The consumer is the tool_use_id ONLY when the call carries one: the sole
	// non-forgeable transport correlation id, so a transport retry of the SAME tool-call
	// re-obtains its grant (bounded engine-side to a short retry window). The CLI omits
	// it by design (connectors/claude/hooks.go:267-269,275) and the constant plan hash
	// would degrade single use to per-plan reuse, so without it the approval module
	// mints a fresh nonce and a transport-id-less retry needs a new human decision.
	a, err := effectgate.Gate(ctx, d.Bridge, effectgate.GatedEffect{
		Tenant: tenant, Action: ActionCapability, SubjectKind: "claude.tool", SubjectRef: subjectRef, PlanHash: in.PlanHash,
		Reason:      "Claude Code tool-call requires human approval: " + in.Tool + " (" + in.ResourceKind + ")",
		RequestedBy: actor, Consumer: strings.TrimSpace(in.ToolUseID), PolicyVersion: version,
	})
	switch {
	case a.Failed == effectgate.Spend:
		return deny("could not spend governed approval (deny-closed)", actor, tier, version)
	case err != nil:
		return deny("could not open governed approval (deny-closed)", actor, tier, version)
	}
	switch a.Outcome {
	case effectgate.Allowed:
		res := claude.HookDecisionResult{
			Permission: claude.DecisionAllow, PolicyVersion: version, PrincipalActor: actor, IdentityTier: tier,
			Reason: "approved by human review, single-use grant spent (" + a.Ref + ")",
		}
		if a.Status == governance.GateStatusBreakGlass {
			// the emergency nature is explicit in the agent's own record.
			res.Reason = "authorized by BREAK-GLASS emergency access (" + a.Ref + ") — audited; post-review required"
		}
		applyRewrite(&res, in, disp)
		return res
	case effectgate.Pending:
		return claude.HookDecisionResult{
			Permission: claude.DecisionAsk, PolicyVersion: version, PrincipalActor: actor, IdentityTier: tier,
			Reason: firstNonEmptyStr(disp.reason, "pending human approval ("+a.Ref+")"),
		}
	case effectgate.Replay:
		// The engine recorded a would-replay event to the signed ledger, and
		// anchorDecision anchors this deny too.
		return deny("human approval already consumed by another tool-call; replay denied — a new human decision is required ("+a.Ref+")", actor, tier, version)
	case effectgate.Unspendable:
		return deny("governed approval is no longer valid to spend (deny-closed)", actor, tier, version)
	default: // rejected | canceled | expired | no_gate | unknown ⇒ deny-closed
		return deny("human review did not approve (status="+a.Status+")", actor, tier, version)
	}
}

// pdpForbids consults the live composed PDP (native ABAC + external/authored Cedar) for
// the tool-call, mapped onto an auth.Request. A non-allow decision is a hard deny. The
// evaluator is restrict-only, so this can only further-restrict the disposition.
// It returns the deny's provenance class so observe can shadow an AUTHORED policy
// forbid but never an invariant: a fail-closed evaluation error is ClassInvariant, while a
// clean forbid carries the evaluator's own dec.Class (propagated verbatim from the
// invariant-dominates chain — E1a/E1b — never hardcoded here).
func (d *Decider) pdpForbids(ctx context.Context, p auth.Principal, tenant model.TenantID, in claude.HookDecisionInput) (bool, string, auth.DecisionClass) {
	req := auth.Request{
		Principal:  p,
		Permission: auth.Permission(ActionCapability + ":" + modeVerb(in.Mode)),
		Tenant:     tenant,
		Resource: auth.ResourceAttrs{
			Kind:        in.ResourceKind,
			ID:          in.ResourceRef,
			WorkspaceID: p.SessionWorkspaceID,
			Extra: map[string]string{
				"tool": in.Tool,
				"mode": in.Mode,
			},
		},
	}
	req.EvidenceRedactor = d.hookEvidenceRedactor(ctx, in, req.Resource.ID)
	dec, err := d.Eval.Evaluate(ctx, req)
	if err != nil {
		return true, "policy evaluation error", auth.ClassInvariant // fail closed
	}
	if !dec.Allow {
		return true, dec.Reason, dec.Class
	}
	return false, "", auth.ClassInvariant
}

// scopedForbids consults the central scoped grant/forbid engine and reports whether a
// scoped FORBID denies this tool-call (F-03, forbid-overrides-allow). It is a
// FURTHER-RESTRICT overlay: ONLY EffectForbid denies; a grant or abstain never widens the
// local disposition. It projects the tool-call onto a scope-grantable catalog resource; a
// tool-call with no sound projection is left to the local disposition + PDP overlay (a
// documented residual — see projectHookScopedRequest). Fail-closed: a scoped-engine error
// denies (an unreadable forbid state must never let the hook approve what the central
// algebra would forbid).
// It returns the forbid's provenance class. A scoped-engine error is fail-closed
// (ClassInvariant); a forbid carries sd.Class PROPAGATED verbatim — a WORKSPACE-CONFINEMENT
// forbid leaves EffectForbid with a zero Class = ClassInvariant, so it can NEVER be
// shadowed in observe. Hardcoding ClassPolicy here would re-open the confinement escape
// closed, so the class is always taken from the engine, never assumed.
func (d *Decider) scopedForbids(ctx context.Context, p auth.Principal, tenant model.TenantID, in claude.HookDecisionInput) (bool, string, auth.DecisionClass) {
	req, projectable := projectHookScopedRequest(p, tenant, in)
	if !projectable {
		return false, "", auth.ClassInvariant
	}
	req.EvidenceRedactor = d.hookEvidenceRedactor(ctx, in, req.Resource.ID)
	sd, err := d.Scoped.Scoped(ctx, req)
	if err != nil {
		return true, "scoped policy evaluation unavailable (deny-closed)", auth.ClassInvariant
	}
	if sd.Effect == auth.EffectForbid {
		return true, firstNonEmptyStr(sd.Reason, "denied by a central scoped forbid"), sd.Class
	}
	return false, "", auth.ClassInvariant
}

// projectHookScopedRequest projects a hook tool-call onto a scope-grantable core catalog
// resource so the engine can decide a scoped forbid against it. Only the MCP tool
// surface has a sound catalog projection today: mcp__<server>__<tool> → the mcp_server
// resource (the connector's ResourceRef is "server/tool", so the server is the resource id).
// This lets a forbid on the mcp_server itself, OR on the principal for the mcp_server:read
// action, bite the hook — the projectable subset.
//
// projectable=false for every other tool kind (shell/file/http/web/agent/generic tool):
// none maps to a scope-grantable catalog resource, so a scoped forbid cannot target them
// through the hook. Two residuals remain for the ADR, for the owner to elevate: (1) a
// forbid scoped to a WORKSPACE or an AGENT-GROUP cannot be projected — HookDecisionInput
// carries no workspace/agent-group ref (only SessionID), and the mcp_server's own
// workspace is not resolved from the store for a bare resource kind; (2) non-MCP tool
// surfaces have no catalog resource to author a scoped forbid against at all.
func projectHookScopedRequest(p auth.Principal, tenant model.TenantID, in claude.HookDecisionInput) (auth.Request, bool) {
	if in.ResourceKind != ResourceKindMCP {
		return auth.Request{}, false
	}
	server, _, _ := strings.Cut(in.ResourceRef, "/")
	if server = strings.TrimSpace(server); server == "" {
		return auth.Request{}, false
	}
	return auth.Request{
		Principal:  p,
		Tenant:     tenant,
		Permission: auth.Permission("mcp_server:" + auth.VerbRead),
		Resource:   auth.ResourceAttrs{Kind: "mcp_server", ID: server, WorkspaceID: p.SessionWorkspaceID},
	}, true
}

// hookDisposition is the policy verdict before HITL/PDP refinement.
type hookDisposition struct {
	decision string // allow | ask | deny
	reason   string
	rewrite  map[string]any
	block    bool
	// class is the deny/ask PROVENANCE: ClassPolicy for an AUTHORED business rule
	// (shadowable in observe), ClassInvariant for a deny-closed fallback (unknown/malformed
	// rule, empty default, unresolved path, Bash tokenization ambiguity). Its zero value is
	// ClassInvariant, so an unclassified disposition is NEVER shadowable (fail-safe). Only
	// meaningful for a deny/ask; an allow is never shadowed.
	class auth.DecisionClass
	// source (E3) names the PRODUCER of a shadowable deny/ask so the promotion report
	// can aggregate by origin (ShadowSourceLocalRule/LocalDefault/BashPath). It is an
	// ORTHOGONAL axis to shadowed_decision (deny|ask); empty for a non-shadowed disposition.
	source string
}

// evalHookPolicy returns the matching rule's disposition and matched=true, or a zero
// disposition and matched=false when no rule applies (the caller picks the event-class-aware
// default). Path-scoped policies never silently fall through to allow on an unresolved file
// path: they ask by default, or deny when the operator requested it.
func evalHookPolicy(pol PolicyDoc, in claude.HookDecisionInput) (hookDisposition, bool) {
	pathScoped := hookPolicyPathScoped(pol)
	first := hookDisposition{}
	firstMatched := false
	deny := hookDisposition{}
	denyMatched := false
	denyOverride := hookPathPrecedence(pol.PathPrecedence) == "deny-overrides"

	for _, r := range pol.Rules {
		if hookRuleMatches(r, in) {
			dec := normalizeDisposition(r.Decision)
			// Provenance: a recognized decision is an AUTHORED business verdict
			// (ClassPolicy, shadowable in observe); an UNKNOWN decision string is a
			// malformed rule that denies deny-closed — NOT authored intent, so it stays
			// ClassInvariant (never shadowable).
			class := auth.ClassPolicy
			if dec == "" {
				dec = claude.DecisionDeny // an unknown decision in a rule denies (never a silent allow)
				class = auth.ClassInvariant
			}
			disp := hookDisposition{decision: dec, reason: r.Reason, rewrite: r.Rewrite, block: r.Block, class: class, source: ShadowSourceLocalRule}
			if !denyOverride {
				return disp, true
			}
			if !firstMatched {
				first = disp
				firstMatched = true
			}
			if dec == claude.DecisionDeny && !denyMatched {
				deny = disp
				denyMatched = true
			}
		}
	}
	if denyMatched {
		return deny, true
	}
	if firstMatched {
		return first, true
	}
	if pathScoped && in.ResourceKind == ResourceKindFile {
		if _, ok := normalizePath(in.ResourceRef, ""); !ok {
			dec := claude.DecisionAsk
			if hookOnUnresolvedPath(pol.OnUnresolvedPath) == claude.DecisionDeny {
				dec = claude.DecisionDeny
			}
			return hookDisposition{
				decision: dec,
				reason:   "path-scoped hook rule could not resolve the file resource; operator review required",
				// An UNRESOLVED path is an inspection failure, not an authored verdict on
				// the real resource → ClassInvariant (always gates/denies, never shadowed).
				class: auth.ClassInvariant,
			}, true
		}
	}
	return hookDisposition{}, false
}

func hookPolicyPathScoped(pol PolicyDoc) bool {
	for _, r := range pol.Rules {
		if hookRulePathScoped(r) {
			return true
		}
	}
	return false
}

func hookPathPrecedence(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "deny-overrides":
		return "deny-overrides"
	default:
		return "first-match"
	}
}

func hookOnUnresolvedPath(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case claude.DecisionDeny:
		return claude.DecisionDeny
	default:
		return claude.DecisionAsk
	}
}

// hookGatingDefaultDeny lists the gating events whose SAFE default — when no governed rule
// matches — is DENY: a state MUTATION it is safe to block (2026-06-19, D3). Every other
// NON-classic gating event defaults NEUTRAL (audited pass-through); blocking it without an
// explicit operator rule would break UX / the agent loop.
var hookGatingDefaultDeny = map[string]bool{
	"ConfigChange": true,
}

// Resource kinds the connector derives for a tool-call (connectors/claude/resource.go).
const (
	ResourceKindFile  = "file"
	ResourceKindShell = "shell"
	// ResourceKindMCP is the connector's resource kind for an mcp__server__tool call
	// (connectors/claude/resource.go resMCP). It is the ONLY tool kind with a sound
	// scope-grantable catalog projection today (→ mcp_server), so the F-03 central
	// scoped-forbid overlay targets it (projectHookScopedRequest).
	ResourceKindMCP = "mcp.tool"
)

// defaultHookDisposition is the disposition for an enforceable event with NO matching rule.
// A classic permission gate (PreToolUse / PermissionRequest / UNKNOWN) honors the operator's
// policy default (deny-closed unless default:"allow"); a state-mutating gating event denies;
// every other gating event is neutral (audited).
func defaultHookDisposition(event string, enf claude.HookEnforcement, pol PolicyDoc) hookDisposition {
	if enf.ClassicGate {
		def := normalizeDisposition(pol.Default)
		// an EXPLICIT, recognized operator default is an authored business choice
		// (ClassPolicy). An empty OR unrecognized (typo) default falls back to deny-closed
		// — a platform fail-safe, NOT authored intent → ClassInvariant (never shadowable,
		// so a typo can't open every unmatched call in observe).
		class := auth.ClassPolicy
		if def == "" {
			def = claude.DecisionDeny
			class = auth.ClassInvariant
		}
		reason := ""
		if def == claude.DecisionDeny {
			reason = "no governed policy rule permits this tool-call (deny-closed default)"
		}
		return hookDisposition{decision: def, reason: reason, class: class, source: ShadowSourceLocalDefault}
	}
	if hookGatingDefaultDeny[event] {
		// A state-mutating event's deny-closed default is a platform safe-default, not an
		// authored per-call verdict → ClassInvariant.
		return hookDisposition{decision: claude.DecisionDeny, reason: "no governed policy rule permits this " + event + " (deny-closed default for a state-mutating event)", class: auth.ClassInvariant}
	}
	return hookDisposition{decision: claude.DecisionAllow, reason: "no governed policy rule applies to " + event + "; neutral (audited)"}
}

// neutralHookVerdict is the audited pass-through for a NON-ENFORCEABLE event (context/observe
// and the inverted Stop/SubagentStop): the PEP cannot block it, so it returns a neutral allow
// with no rewrite and no HITL — the render emits the safe neutral wire form.
func neutralHookVerdict(event, actor, tier, version string) claude.HookDecisionResult {
	return claude.HookDecisionResult{
		Permission: claude.DecisionAllow, PolicyVersion: version, PrincipalActor: actor, IdentityTier: tier,
		Reason: "non-enforceable hook event (" + event + "): observed, not gated",
	}
}

// hookRuleMatches reports whether a rule matches a hook. An Event-targeted rule must match
// the exact event; an event-LESS rule targets only the classic tool-gating events
// (PreToolUse/PermissionRequest and the PermissionPrompt route), so a tool/kind/mode rule
// never accidentally gates a lifecycle event (UserPromptSubmit, ConfigChange, …) that has no
// tool or resource.
func hookRuleMatches(r PolicyRule, in claude.HookDecisionInput) bool {
	if r.Event != "" {
		if r.Event != in.Event {
			return false
		}
	} else if !claude.HookEnforcementFor(in.Event).ClassicGate {
		return false
	}
	if !toolGlob(r.Tool, in.Tool) {
		return false
	}
	if r.ResourceKind != "" && r.ResourceKind != in.ResourceKind {
		return false
	}
	if r.Mode != "" && r.Mode != in.Mode {
		return false
	}
	if hookRulePathScoped(r) {
		if in.ResourceKind != ResourceKindFile {
			return false
		}
		root := ""
		abs, ok := normalizePath(in.ResourceRef, root)
		if !ok {
			return false
		}
		if !hookRulePathMatches(r, abs, root) {
			return false
		}
	}
	return true
}

func hookRulePathScoped(r PolicyRule) bool {
	return len(r.Paths) > 0 || strings.TrimSpace(r.Subtree) != ""
}

func hookRulePathMatches(r PolicyRule, abs, root string) bool {
	for _, glob := range r.Paths {
		if pathGlobMatch(glob, abs) {
			return true
		}
	}
	if strings.TrimSpace(r.Subtree) != "" {
		subAbs, ok := normalizePath(r.Subtree, root)
		if ok && pathInSubtree(abs, subAbs) {
			return true
		}
	}
	return false
}

// toolGlob matches a rule's tool pattern: "" / "*" any; trailing "*" prefix glob; else
// exact.
func toolGlob(pattern, tool string) bool {
	switch {
	case pattern == "" || pattern == "*":
		return true
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(tool, strings.TrimSuffix(pattern, "*"))
	default:
		return pattern == tool
	}
}

// normalizeDisposition maps a policy decision string onto the canonical verdict, or "" if
// unrecognized.
func normalizeDisposition(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case claude.DecisionAllow:
		return claude.DecisionAllow
	case claude.DecisionAsk:
		return claude.DecisionAsk
	case claude.DecisionDeny:
		return claude.DecisionDeny
	default:
		return ""
	}
}

// applyRewrite merges a rule's governed rewrite over the original tool input and sets it
// as the updatedInput, only when the rule supplies one. It applies on the events whose
// wire form carries a governed input rewrite: PreToolUse (hookSpecificOutput.updatedInput)
// and the PermissionPrompt route (PermissionResult.updatedInput) — both VERIFIED.
func applyRewrite(res *claude.HookDecisionResult, in claude.HookDecisionInput, disp hookDisposition) {
	if len(disp.rewrite) == 0 || (in.Event != "PreToolUse" && in.Event != "PermissionPrompt") {
		return
	}
	merged := in.RewriteBase()
	for k, v := range disp.rewrite {
		merged[k] = v
	}
	res.UpdatedInput = merged
	if res.Reason == "" {
		res.Reason = "permitted with governed input rewrite"
	}
}

// modeVerb maps an access mode to the verb the PDP request carries.
func modeVerb(mode string) string {
	switch mode {
	case "read":
		return "read"
	case "write":
		return "write"
	default:
		return "use"
	}
}

// deny builds a deny verdict with the attribution fields populated for the audit trail.
func deny(reason, actor, tier, version string) claude.HookDecisionResult {
	return claude.HookDecisionResult{
		Permission: claude.DecisionDeny, Reason: reason,
		PolicyVersion: version, PrincipalActor: actor, IdentityTier: tier,
	}
}

// firmnessTier classifies how firmly a tool-call is attributed: firm when the
// resolved principal holds a grant in the call's tenant; approximate when it is a real
// but coarse principal (superadmin / no tenant grant); unknown when unauthenticated.
func firmnessTier(p auth.Principal, tenant model.TenantID, authErr error) string {
	if authErr != nil {
		return TierUnknown
	}
	if p.Superadmin {
		return TierApproximate
	}
	if p.IsMember(tenant) {
		return TierFirm
	}
	return TierApproximate
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// singleUseConsumerPrefix labels a server-minted single-use consumer id (F-02): a
// governed tool-call that carries NO tool_use_id is spent under one of these so a replay
// (which mints a DIFFERENT one) can never hit the engine's result-idempotency branch.
const singleUseConsumerPrefix = "singleuse-"

// NewSingleUseConsumerID mints an unforgeable, server-side, unique consumer id for a
// governed tool-call that carries no tool_use_id. It is generated server-side
// (NEVER from caller-controlled input), so an attacker cannot make two consumes share it,
// and it is unique per call (128 bits of entropy + a monotonic counter), so the FIRST
// consume binds it and any later consume is a would-replay DENY. Predictability is
// irrelevant here — the caller never supplies the value — so a rand read error is tolerated
// (the counter tail keeps ids distinct regardless).
func NewSingleUseConsumerID() string { return effectgate.NewSingleUseConsumerID() }

// decisionAttemptSeq is a strictly-increasing tail for the E3 decision_attempt_id, so
// the correlation id is unique even if the entropy source is momentarily unavailable.
var decisionAttemptSeq atomic.Uint64

// newDecisionAttemptID mints a per-decision correlation nonce. It rides both the original
// anchor and any downgrade re-anchor (anchorDecision), so a reader can correlate the two
// commits for one decision and detect an ambiguous-commit double-write. Predictability is
// irrelevant (never caller-supplied), so a rand read error is tolerated — the monotonic tail
// keeps ids distinct regardless.
func newDecisionAttemptID() string {
	var b [24]byte
	_, _ = rand.Read(b[:16])
	binary.BigEndian.PutUint64(b[16:], decisionAttemptSeq.Add(1))
	return "attempt-" + hex.EncodeToString(b[:])
}

func hookDecisionHash(event, tool, resourceKind, resourceRef, mode, planHash, decision, actor, policyVersion string) []byte {
	h := sha256.New()
	writeLenPrefixed(h, []byte(hookDecisionDomain))
	writeLenPrefixed(h, []byte(event))
	writeLenPrefixed(h, []byte(tool))
	writeLenPrefixed(h, []byte(resourceKind))
	writeLenPrefixed(h, []byte(resourceRef))
	writeLenPrefixed(h, []byte(mode))
	writeLenPrefixed(h, []byte(planHash))
	writeLenPrefixed(h, []byte(decision))
	writeLenPrefixed(h, []byte(actor))
	writeLenPrefixed(h, []byte(policyVersion))
	return h.Sum(nil)
}

// claudeHookAuditor records each governed hook decision (minimal data) for the SOC trail.
// The ask/HITL path additionally self-audits to the hash-chain ledger via the bridge
// (the decider opens the approval), so a human-gated action lands on the tamper-evident
// ledger keyed to the real approver; this captures the allow/deny of every call keyed to
// the real agent principal. It carries NO raw tool arguments, no bearer, no secrets
// (docs/SECURITY-HARDENING.md).
type claudeHookAuditor struct {
	log     *slog.Logger
	decider *Decider
}

func (a claudeHookAuditor) Record(ctx context.Context, in claude.HookDecisionInput, res claude.HookDecisionResult, denyClosed bool) {
	if a.decider != nil {
		in = a.decider.retainedHookInput(ctx, in)
	}
	a.log.Info("hook-pep: governed tool-call decision",
		"event", in.Event,
		"tool", in.Tool,
		"resource_kind", in.ResourceKind,
		"resource_ref", in.ResourceRef,
		"mode", in.Mode,
		"plan_hash", in.PlanHash,
		"decision", firstNonEmptyStr(res.Permission, claude.DecisionDeny),
		"deny_closed", denyClosed,
		"rewritten", len(res.UpdatedInput) > 0,
		"policy_version", res.PolicyVersion,
		"principal", res.PrincipalActor,
		"identity_tier", res.IdentityTier,
		"reason", res.Reason,
	)
}
