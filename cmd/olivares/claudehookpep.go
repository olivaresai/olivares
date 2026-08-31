// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// claudehookpep.go wires the GOVERNED Claude Code hooks PEP in the composition
// root: it binds the connector's protocol shell (connectors/claude.HookPEP) to the AGPL
// plane the connector may not import — the live PDP (governance Cedar/ABAC), the
// firm-identity plane (via the authenticated principal), the ApprovalGate→HITL
// bridge, and the tamper-evident ledger. It is the sibling of the inline MCP PEP
// (mcpgateway.go): same pattern — connector owns the protocol + deny-closed
// defaults, this file owns the governed decision.
//
// Transport (by design): HTTP local to the engine on a
// dedicated loopback socket, exactly like the HITL receiver and the agent gateway.
// The managed hook command POSTs the (already-redacted) tool-call to this endpoint; the
// endpoint runs the governed decision SERVER-SIDE and returns the Claude Code
// hookSpecificOutput the agent enforces. Rationale: coherent with server-side
// PEP, keeps /core out of the connector, and keeps the agent isolated from engine
// internals (it sees only a localhost decision endpoint). No genuinely-open design
// question remained, so per the prompt we follow the pattern.
//
// Loaded from OLIVARES_HOOK_PEP_CONFIG (operator-provisioned, out of the store, same
// pattern as OLIVARES_HITL_CONFIG / OLIVARES_AGENT_GATEWAY_CONFIG). Absent/invalid ⇒
// nothing mounted (the safe default: an un-wired node serves no enforcement surface and
// the agent runs ungoverned-but-observed exactly as before). Every governance seam stays
// deny-closed.

// loadHookPEPConfig reads the optional OLIVARES_HOOK_PEP_CONFIG JSON. A missing path
// yields an empty config (nothing mounted); a supplied path must be readable and contain
// valid JSON or startup fails closed.
func loadHookPEPConfig(_ *slog.Logger) (hookPEPConfig, error) {
	path := os.Getenv("OLIVARES_HOOK_PEP_CONFIG")
	if path == "" {
		return hookPEPConfig{}, nil
	}
	var cfg hookPEPConfig
	if err := loadOperatorJSONConfig("OLIVARES_HOOK_PEP_CONFIG", path, &cfg); err != nil {
		return hookPEPConfig{}, err
	}
	return cfg, nil
}

// hookPEPConfig is the operator provisioning for the governed hooks PEP: a per-tenant
// governed policy (the allow/ask/deny disposition + rewrite/redaction the control plane
// imposes) and the loopback bind. The PDP overlay (Cedar/ABAC) and the HITL bridge are
// the engine's already-wired ones — this config does NOT re-declare them.
type hookPEPConfig struct {
	Listen  string          `json:"listen"`
	Tenants []hookPEPTenant `json:"tenants"`
}

// hookPEPTenant binds one business tenant to its governed hook policy. RequireFirm makes
// the deny-closed posture explicit: when set, a tool-call whose firm identity
// the PEP cannot resolve (approximate/unknown attribution) is denied rather than
// enforced on a guessed principal.
type hookPEPTenant struct {
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
	ObserveUntil string        `json:"observe_until,omitempty"`
	Policy       hookPolicyDoc `json:"policy"`
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
func resolveObserveGrant(tid model.TenantID, tc hookPEPTenant, now time.Time, log *slog.Logger) (hookEnforcementMode, observeGrant) {
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
func hookPolicyFingerprint(pol hookPolicyDoc) string {
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

// hookPolicyDoc is the governed disposition policy for a tenant's tool-calls. Default is
// the disposition when no rule matches; an EMPTY default is treated as "deny"
// (deny-closed: a governed surface with no explicit allowlist denies). Operators set
// default:"allow" for an allowlist-of-denies posture (block specific tools, allow the
// rest) or default:"ask" to route everything through HITL.
type hookPolicyDoc struct {
	Version          string           `json:"version"`
	Default          string           `json:"default"` // allow | ask | deny ("" ⇒ deny)
	PathPrecedence   string           `json:"path_precedence,omitempty"`
	OnUnresolvedPath string           `json:"on_unresolved_path,omitempty"`
	Rules            []hookPolicyRule `json:"rules"`
}

// hookPolicyRule matches a hook by event + tool glob / resource kind / mode and imposes a
// disposition. Event ("" = any tool-gating event: PreToolUse/PermissionRequest) targets a
// SPECIFIC lifecycle event (e.g. "ConfigChange", "UserPromptSubmit") so a tool rule never
// accidentally gates a non-tool event; an event-targeted rule needs no tool/kind/mode.
// Rewrite (PreToolUse) supplies a governed input override merged over the original tool
// input; Block (PostToolUse) blocks post-processing of a flagged output.
type hookPolicyRule struct {
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

// resolvedTenant is the validated per-tenant config the decider holds.
type resolvedTenant struct {
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
	policy          hookPolicyDoc
}

// defaultHookPEPListen is the loopback-default bind (secure default).
const defaultHookPEPListen = "127.0.0.1:8447"

// hookPEPPolicyVersionFallback labels a decision when the policy declares no version.
const hookPEPPolicyVersionFallback = "olivares.hookpep/v1"

// hookActionCapability is the governance action a gated tool-call opens an approval for
// and the verb root the PDP request carries. Kept short and stable (it is bounded by the
// engine's subject_ref guard and is the audit capability).
const hookActionCapability = "claude.tool.use"

// hookDecisionDomain separates governed hook decision commitments from every other
// length-prefixed hash written to the audit ledger.
const hookDecisionDomain = "olivares.hook.tool.decision.v1"

// firm-attribution tiers.
const (
	tierFirm        = "firm"
	tierApproximate = "approximate"
	tierUnknown     = "unknown"
)

// buildClaudeHookPEPServer constructs the governed hooks PEP server on its own socket, or
// nil when no tenant is configured. It is built AFTER boot (in cmd_serve.go), so the
// engine's API handler, authenticator, PDP evaluator and approval bridge are all live —
// no late binding needed (unlike the bridge, which predates the API server).
func buildClaudeHookPEPServer(eng *engine, log *slog.Logger) (*http.Server, error) {
	cfg, err := loadHookPEPConfig(log)
	if err != nil {
		return nil, fmt.Errorf("load hook PEP operator config: %w", err)
	}
	tenants := map[model.TenantID]resolvedTenant{}
	for _, tc := range cfg.Tenants {
		tid, present, err := parseBusinessTenant("hook-PEP config: tenant", tc.Tenant)
		if err != nil || !present {
			log.Warn("hook-pep: tenant entry has an invalid tenant id; skipped", "tenant", tc.Tenant)
			continue
		}
		if _, dup := tenants[tid]; dup {
			log.Warn("hook-pep: duplicate tenant entry; later definition ignored", "tenant", tc.Tenant)
			continue
		}
		if err := validateHookPolicy(tc.Policy); err != nil {
			log.Warn("hook-pep: tenant policy has a relative path/subtree pattern; path patterns MUST be absolute — tenant NOT mounted (deny-closed)", "tenant", tc.Tenant, "err", err)
			continue
		}
		mode, grant := resolveObserveGrant(tid, tc, time.Now(), log)
		tenants[tid] = resolvedTenant{tenant: tid, requireFirm: tc.RequireFirm, enforceNHI: tc.EnforceNHILifecycle, mode: mode, observeUntil: grant.until, observeBootMono: grant.bootMono, observeWindow: grant.window, observeGrantID: grant.id, policy: tc.Policy}
	}
	if len(tenants) == 0 {
		return nil, nil
	}

	dec := &claudeHookDecider{
		tenants:     tenants,
		authr:       eng.authr,
		eval:        eng.policyEval,
		scoped:      eng.scopedGrants,
		nhiEnforcer: eng.nhiEnforcer,
		stops:       eng.killSwitch,
		stopRec:     eng.stopDeny,
		store:       eng.store,
		// Hook content firewall: the real inspector under -tags enterprise WITH a config,
		// else nil (no deep inspection, unchanged). Findings/metering ride the engine bus.
		hookInspector: newHookContentInspector(os.Getenv, log),
		bus:           eng.bus,
		clock:         time.Now,
		log:           log,
	}
	// Assign the bridge only when one is configured: storing a nil *approvalBridge in the
	// interface would make d.bridge != nil (typed-nil) and defeat the deny-closed guard.
	if eng.approvalBridge != nil {
		dec.bridge = eng.approvalBridge
	}
	pep := claude.NewHookPEP(dec, claudeHookAuditor{log: log}, time.Now)

	mux := http.NewServeMux()
	mux.Handle("/", pep)
	// the Agent SDK permissionPromptToolName route — point a customer SDK program's
	// permissionPromptToolName here and every permission request it raises runs through the
	// SAME governed decider (deny-closed). The more specific pattern wins over "/".
	mux.HandleFunc("/permission-prompt", pep.ServePermissionPrompt)
	addr := strings.TrimSpace(cfg.Listen)
	if addr == "" {
		addr = defaultHookPEPListen
	}
	if !hostIsLoopback(addr) {
		log.Warn("hook-pep: bound to a NON-loopback address; front it with your ingress — its security is fail-closed token verification + the governed decision, not network isolation", "addr", addr)
	}
	srv := eng.api.NewHTTPServer(addr)
	srv.Handler = mux
	log.Info("hook-pep: governed Claude Code hooks PEP mounted",
		"addr", addr, "tenants", len(tenants), "hitl_bridge", eng.approvalBridge != nil, "pdp", eng.policyEval != nil)
	return srv, nil
}

func validateHookPolicy(pol hookPolicyDoc) error {
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

// principalAuthenticator resolves an inbound bearer to a real principal. *auth.Authenticator
// satisfies it; tests inject a fake. It is the firm-identity + audit-actor source.
type principalAuthenticator interface {
	Authenticate(ctx context.Context, token string) (auth.Principal, error)
}

// hookApprovalOpener is the subset of the bridge the PEP uses: open (or idempotently
// find/reuse) a governed approval bound to the plan hash and report its effective status
// in one shot, plus SPEND that approval single-use once a human approved it. *approvalBridge
// satisfies it (the unexported methods keep the seam in-package).
type hookApprovalOpener interface {
	gateOnce(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy string) (ref, status, boundHash string, err error)
	// consumeApproval spends an APPROVED approval exactly once, keyed to the exact
	// caller (consumerID = the tool_use_id): granted for the first/idempotent-same
	// consumer, replay=true for a NEW caller reusing an already-spent grant (F-02).
	consumeApproval(ctx context.Context, tenant model.TenantID, ref, consumerID, policyVersion string) (granted, replay bool, err error)
}

// claudeHookDecider is the GOVERNED brain: firm-identity gate → policy disposition
// → live PDP hard-deny overlay → ask→HITL → rewrite. Every edge is
// deny-closed.
type claudeHookDecider struct {
	tenants map[model.TenantID]resolvedTenant
	authr   principalAuthenticator
	eval    auth.PolicyEvaluator // nil ⇒ no external overlay (the disposition still governs)
	// scoped is the central scoped grant/forbid engine (F-03). The hook consults its
	// FORBID contribution as a FURTHER-RESTRICT overlay so a central scoped forbid that
	// targets the projected tool-call resource (an mcp_server) or the principal denies the
	// call at the hook too — the same forbid-overrides-allow algebra REST/model/MCP run.
	// nil ⇒ no scoped overlay (behavior unchanged). It never widens a disposition.
	scoped      auth.ScopedAuthorizer
	bridge      hookApprovalOpener // nil ⇒ the ask path denies (no HITL wired)
	nhiEnforcer nhiEnforcer        // nil ⇒ no NHI-lifecycle deny gate
	stops       killSwitchGuard    // nil ⇒ no kill-switch gate (boot always wires it)
	stopRec     *stopDenyRecorder  // throttled tamper-evident deny evidence
	store       store.Store        // terminal allow/deny evidence ledger
	// hookInspector is the OPTIONAL commercial hooks-hardening DLP firewall (enterprise/hookhardening). nil in the default AGPL build ⇒ no deep tool_input
	// inspection (the PEP behaves exactly as before — no rug-pull); under -tags enterprise
	// WITH a config it runs DLP + structural detection over the tool arguments as a
	// further-restrict overlay (claudehookfirewall.go).
	hookInspector contentInspector
	// bus carries the firewall's posture findings + per-inspection metering (nil ⇒ no-op).
	bus   eventbus.Bus
	clock func() time.Time
	// observeExpired (E3) latches a tenant whose observe window has passed: once the live
	// clock has reached observeUntil, the grant is permanently inactive for this process, so a
	// clock ROLLBACK can never resurrect an expired relaxation. Keyed by model.TenantID.
	observeExpired sync.Map
	log            *slog.Logger
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
// elapsed check (now.Sub(bootMono) >= window). In production both d.clock() and observeBootMono
// carry a monotonic reading, so the elapsed test cannot shrink under a backward system-clock jump —
// it expires the grant at the right real instant even if NO request arrived during the window (the
// wall test alone would be fooled by a rollback with no intervening traffic to latch it). A forward
// wall jump is caught by the wall test. Reaching either bound latches the tenant expired.
func (d *claudeHookDecider) observeGrantActive(rt resolvedTenant) bool {
	if rt.observeUntil.IsZero() || d.clock == nil {
		return false // no valid grant / no clock ⇒ never observe (deny-closed)
	}
	if _, latched := d.observeExpired.Load(rt.tenant); latched {
		return false // once expired, never active again (clock-rollback guard)
	}
	now := d.clock()
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

var _ claude.HookDecider = (*claudeHookDecider)(nil)

// Decide is the full governed decision. It NEVER returns an ALLOW without (a) a resolved,
// policy-sufficient firm identity, (b) a disposition that is not deny, and (c) the live
// PDP not forbidding the call; the ask path requires an effective approval bound to
// the exact plan hash. Any error path is a DENY (the connector also treats a returned
// error as deny, so this is belt-and-suspenders).
func (d *claudeHookDecider) Decide(ctx context.Context, in claude.HookDecisionInput, bearer string) (claude.HookDecisionResult, error) {
	// Resolve once and carry the same authenticated principal through authorization
	// and evidence attribution. Re-authenticating only for the anchor could observe a
	// different revocation state and mislabel the decision that was actually made.
	principal, authErr := d.authr.Authenticate(ctx, bearer)
	res, tenant, shadow, err := d.decide(ctx, in, principal, authErr)
	if err != nil {
		return res, err // the error from the inner decider is deny-closed in the connector
	}
	actAs, delegated := principal.ActAs()
	if authErr != nil {
		actAs, delegated = "", false
	}
	out := d.anchorDecision(ctx, tenant, in, res, shadow, auditDelegation{
		isDelegated: delegated,
		actAs:       actAs.String(),
	})
	// B-05: the decision is now SAID, not only made. It is emitted after the
	// anchor and after `out` is final, so a slow, full or absent bus can never turn
	// an allow into a deny or the other way round — the verdict is already decided
	// and already recorded before anything is published.
	d.publishDecisionSignal(ctx, tenant, in, out.Permission == claude.DecisionAllow, out.Reason)
	return out, nil
}

// shadowVerdict is the record of a would-be AUTHORED-policy verdict that observe mode
// SHADOWED (allowed but recorded). It is an internal composition-root carrier — it never
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
	metaEnforcementMode    = "enforcement_mode"    // "observe" on a shadowed decision
	metaShadowedDecision   = "shadowed_decision"   // "deny" | "ask" — the would-be verdict
	metaShadowSource       = "shadow_source"       // shadowSource* — producer of the would-be verdict
	metaShadowReason       = "shadow_reason"       // free-text context
	metaObserveScope       = "observe_scope"       // observeScopeTenant (the grant granularity)
	metaObserveGrantID     = "observe_grant_id"    // content digest of the grant terms (distinguishes windows)
	metaEffectiveDowngrade = "effective_downgrade" // true when a shadowed allow fail-closed to deny at the anchor
	metaDecisionAttemptID  = "decision_attempt_id" // per-decision nonce (dedupe an ambiguous-commit double-write)

	enforcementModeObserve = "observe"
	observeScopeTenant     = "tenant"
)

// shadowSource* name the PRODUCER of a shadowable (ClassPolicy) deny/ask. Orthogonal to the
// shadowed_decision (deny|ask): the same producer can emit either. Stable strings (the
// promotion report aggregates on them).
const (
	shadowSourcePDP          = "pdp"           // live PDP hard-deny overlay (Cedar/ABAC forbid)
	shadowSourceScoped       = "scoped"        // central scoped-forbid overlay (authored, non-confinement)
	shadowSourceLocalRule    = "local_rule"    // a matched authored policy rule
	shadowSourceLocalDefault = "local_default" // the explicit authored default disposition
	shadowSourceBashPath     = "bash_path"     // an authored Bash path/subtree rule
)

// decide computes the governed disposition and returns the tenant resolved for it. A
// zero tenant marks a rejection that happened before a trustworthy tenant was resolved.
func (d *claudeHookDecider) decide(ctx context.Context, in claude.HookDecisionInput, principal auth.Principal, authErr error) (claude.HookDecisionResult, model.TenantID, *shadowVerdict, error) {
	// 2. Resolve the tenant: the declared hint, else the principal's sole grant. Ambiguous
	//    or unconfigured ⇒ deny-closed (no governed policy to apply).
	tenant, tok := d.resolveTenant(in.Identity.Tenant, principal, authErr)
	if !tok.found {
		return deny(tok.reason, "", tierUnknown, ""), model.TenantID(""), nil, nil
	}
	rt := d.tenants[tenant]
	tier := firmnessTier(principal, tenant, authErr)
	actor := ""
	if authErr == nil {
		actor = principal.Actor()
	}
	version := firstNonEmptyStr(rt.policy.Version, hookPEPPolicyVersionFallback)

	// 2b. Non-enforceable events: context/observe events and the INVERTED
	//     Stop/SubagentStop cannot be blocked by a hook return — a "block" on Stop would KEEP
	//     the agent running (the opposite of safe). The PEP returns a NEUTRAL, audited
	//     pass-through for them (no firm-identity gate, no HITL, no kill-switch deny — there
	//     is nothing to gate, and letting a Stop stop is the safe direction under any state).
	//     An UNKNOWN event is enforceable (deny-closed), so it does NOT short-circuit here.
	if !claude.HookEnforcementFor(in.Event).Enforceable {
		return neutralHookVerdict(in.Event, actor, tier, version), tenant, nil, nil
	}

	// 2c. Governance-overlay dependency: a live PDP overlay is wired but the principal
	//     could not be resolved — a store/dependency error, NOT a clean unauthenticated (both
	//     yield authErr != nil, undistinguished; authenticator.go returns the store error
	//     verbatim). Deny-closed: the further-restrict overlay we depend on cannot run without a
	//     principal, so we must not fall through to a policy allow default (the lesson).
	//     Placed AFTER the non-enforceable short-circuit so Stop/context events still pass neutral
	//     (a deny on the inverted Stop would keep the agent running — the opposite of safe).
	if d.eval != nil && authErr != nil {
		return deny("governance overlay configured but principal unresolved (deny-closed)", actor, tier, version), tenant, nil, nil
	}

	// 3. Firm-identity gate: a policy that requires firm attribution denies a
	//    tool-call the PEP can only attribute approximately or not at all.
	if rt.requireFirm && tier != tierFirm {
		return deny("firm identity required by policy but attribution is "+tier+"", actor, tier, version), tenant, nil, nil
	}

	// 3b. NHI-lifecycle deny gate: when the tenant opts in, deny a tool-call by an
	//     agent whose bound NHI is BLOCKED (stale-escalated past its window, or offboarded)
	//     — the offboarding cascade and the staleness block reaching the actuation surface.
	//     Fail-closed on a lookup error, consistent with the governed PEP's posture; a clean
	//     "not blocked" (or an unmanaged/unresolvable agent) proceeds, so it never breaks
	//     day-1 operations. The agent ref is advisory unless requireFirm validated it.
	if rt.enforceNHI && d.nhiEnforcer != nil && strings.TrimSpace(in.Identity.Agent) != "" {
		blocked, why, nerr := d.nhiEnforcer.NHIEnforcementForAgentRef(ctx, tenant, in.Identity.Agent)
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
	//     pack's "PEP decisions" leg.
	if d.stops != nil {
		st, kerr := d.stops.KillSwitchState(ctx, tenant)
		if kerr != nil {
			return deny("kill-switch state unreadable (deny-closed)", actor, tier, version), tenant, nil, nil
		}
		agentRef := strings.TrimSpace(in.Identity.Agent)
		if stopID, stopped := st.Stopped(agentRef); stopped {
			d.stopRec.record(ctx, tenant, stopID, "hooks-pep", firstNonEmptyStr(agentRef, in.Tool), actor)
			return deny("emergency stop active (kill switch "+stopID.String()+"); all governed tool-calls are denied until a dual-control re-enable", actor, tier, version), tenant, nil, nil
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
	if in.ResourceKind == hookResourceKindShell {
		disp = bashPathScan(rt.policy, in, "" /*root intentionally empty: unresolved traversal asks*/, disp)
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
	if authErr == nil && d.eval != nil {
		if pdpDenied, reason, class := d.pdpForbids(ctx, principal, tenant, in); pdpDenied {
			full := "PDP policy forbids this tool-call: " + reason
			if observe && class == auth.ClassPolicy {
				recordShadow(claude.DecisionDeny, shadowSourcePDP, full)
			} else {
				return deny(full, actor, tier, version), tenant, nil, nil
			}
		}
	}

	// 5a. F-03: central scoped-forbid overlay (forbid-overrides-allow). A WORKSPACE
	//     CONFINEMENT or fail-closed forbid is ClassInvariant and denies even in
	//     observe; only an AUTHORED scoped forbid is shadowable. The class is taken from the
	//     engine (scopedForbids), never assumed, so confinement can never be shadowed.
	if authErr == nil && d.scoped != nil {
		if scopedDenied, reason, class := d.scopedForbids(ctx, principal, tenant, in); scopedDenied {
			full := "central scoped policy forbids this tool-call: " + reason
			if observe && class == auth.ClassPolicy {
				recordShadow(claude.DecisionDeny, shadowSourceScoped, full)
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
	//     for a call we deny anyway is wasted work and a spurious billable event.
	if disp.decision != claude.DecisionDeny || (observe && disp.class == auth.ClassPolicy) {
		if fw := d.runHookFirewall(ctx, tenant, actor, in); !fw.Forward {
			return deny(firstNonEmptyStr(fw.Reason, "blocked by Olivares hook content firewall"), actor, tier, version), tenant, nil, nil
		}
	}

	// 6. Map the disposition to the governed verdict.
	switch disp.decision {
	case claude.DecisionDeny:
		if observe && disp.class == auth.ClassPolicy {
			// Business deny shadowed: record and fall through to the shadow tail (clean allow).
			recordShadow(claude.DecisionDeny, firstNonEmptyStr(disp.source, shadowSourceLocalRule), firstNonEmptyStr(disp.reason, "blocked by Olivares governance policy"))
		} else {
			return deny(firstNonEmptyStr(disp.reason, "blocked by Olivares governance policy"), actor, tier, version), tenant, nil, nil
		}

	case claude.DecisionAsk:
		if observe && disp.class == auth.ClassPolicy {
			// Business ask shadowed: record and fall through — NOT queued to HITL, so the pilot
			// never consumes an approval nor perturbs the F-02 single-use state.
			recordShadow(claude.DecisionAsk, firstNonEmptyStr(disp.source, shadowSourceLocalRule), firstNonEmptyStr(disp.reason, "human approval required by policy"))
		} else {
			// ask ⇒ open (or idempotently find) a governed approval bound to the exact plan
			// hash, then map its effective status. Pending ⇒ ask (HITL queued); approved ⇒
			// allow; everything else ⇒ deny. No bridge wired ⇒ deny-closed. An INVARIANT ask
			// (unresolved path / Bash ambiguity) still gates. An earlier overlay ClassPolicy
			// shadow rides ONLY a terminal ALLOW — the call actually proceeds despite the
			// shadowed policy. A pending ask is anchored by the HITL bridge (not here) and a
			// gated deny is a plain deny; attaching a shadow to a non-allow would record a
			// would-be-allowed policy on a call that did not proceed.
			res := d.gateViaHITL(ctx, tenant, in, disp, actor, tier, version)
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
func (d *claudeHookDecider) anchorDecision(ctx context.Context, tenant model.TenantID, in claude.HookDecisionInput, res claude.HookDecisionResult, shadow *shadowVerdict, delegation auditDelegation) claude.HookDecisionResult {
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
	receipt := d.anchor(ctx, tenant, in, res, decision, actor, ph, shadow, delegation, attemptID, false, phase, binding)
	if !receipt.MustRefuse(binding) {
		return res
	}
	// A DENY that reached here was already the terminal verdict; it could not be anchored
	// (an evidence gap) but it STANDS — deny-closed — with a loud gap log. Under a DEGRADE
	// drop the loss accounting is already durably committed (fault=spool_degraded), so the
	// gap is counted and will seal as a signed in-chain marker.
	if decision != claude.DecisionAllow {
		if d.log != nil {
			d.log.Error("hook-pep: ledger anchor refused on DENY (evidence gap)", "fault", string(receipt.Fault), "tool", in.Tool)
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
	if d.log != nil {
		d.log.Error("hook-pep: ledger anchor refused on ALLOW; downgrading to DENY (deny-closed)", "fault", string(receipt.Fault), "tool", in.Tool)
	}
	downgraded := deny("evidence unavailable (deny-closed)", res.PrincipalActor, res.IdentityTier, res.PolicyVersion)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reanchorTimeout)
	defer cancel()
	dph := hookDecisionHash(in.Event, in.Tool, in.ResourceKind, in.ResourceRef, in.Mode, in.PlanHash, claude.DecisionDeny, downgraded.PrincipalActor, downgraded.PolicyVersion)
	dbinding := hookEvidenceBinding(tenant, attemptID, hookPhaseDowngradedDeny, dph, hookEffectiveResultDigest(downgraded))
	if rreceipt := d.anchor(rctx, tenant, in, downgraded, claude.DecisionDeny, actor, dph, shadow, delegation, attemptID, true, hookPhaseDowngradedDeny, dbinding); rreceipt.MustRefuse(dbinding) && d.log != nil {
		d.log.Error("hook-pep: re-anchor of downgraded DENY refused (evidence gap); deny stands", "fault", string(rreceipt.Fault), "tool", in.Tool)
	}
	return downgraded
}

// reanchorTimeout bounds the single decoupled re-anchor of a downgraded deny so a wedged store
// cannot block the response indefinitely; the deny stands regardless of the outcome.
const reanchorTimeout = 5 * time.Second

type auditDelegation struct {
	isDelegated bool
	actAs       string
}

// addAuditDelegationMeta names both sides of an on-behalf-of decision. The fields
// intentionally live in Meta, not PayloadHash: the ledger's canonical MetaDigest is
// already part of the event hash and Ed25519 signature, while keeping the v1 decision
// PayloadHash preimage stable avoids a second, redundant commitment format.
func addAuditDelegationMeta(meta map[string]any, delegation auditDelegation) {
	if !delegation.isDelegated {
		return
	}
	meta["is_delegated"] = true
	meta["act_as"] = delegation.actAs
}

// addShadowMeta records an constrained-observe SHADOW on the anchored decision: the call
// was ALLOWED (the effective "decision" already in meta) but an authored policy WOULD have
// denied/asked in enforce mode. The keys are distinct from "mode" (which holds the access mode
// read|write|unknown) so neither overwrites the other. nil ⇒ a normal enforce decision, no-op.
func addShadowMeta(meta map[string]any, shadow *shadowVerdict) {
	if shadow == nil {
		return
	}
	meta[metaEnforcementMode] = enforcementModeObserve
	meta[metaShadowedDecision] = shadow.decision
	meta[metaShadowSource] = shadow.source
	meta[metaShadowReason] = shadow.reason
	meta[metaObserveScope] = observeScopeTenant
	if shadow.grantID != "" {
		meta[metaObserveGrantID] = shadow.grantID
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
func (d *claudeHookDecider) anchor(ctx context.Context, tenant model.TenantID, in claude.HookDecisionInput, res claude.HookDecisionResult, decision, actor string, ph []byte, shadow *shadowVerdict, delegation auditDelegation, attemptID string, downgraded bool, phase string, binding sdk.EvidenceBinding) sdk.EvidenceReceipt {
	if d.store == nil {
		return sdk.ClassifyAnchor(binding, "", false, sdk.EvidenceFaultLedgerUnwired)
	}
	// A resolved principal is the agent's PEP credential — attribute the decision to the
	// agent, not the system, so the evidence names who acted (docs/SECURITY-HARDENING.md).
	actorKind := model.ActorSystem
	if strings.TrimSpace(res.PrincipalActor) != "" {
		actorKind = model.ActorAgent
	}
	meta := map[string]any{
		"event": in.Event, "tool": in.Tool, "resource_kind": in.ResourceKind,
		"resource_ref": in.ResourceRef, "mode": in.Mode, "plan_hash": in.PlanHash,
		"decision": decision, "policy_version": res.PolicyVersion, "identity_tier": res.IdentityTier,
		// Commit the evidence binding INTO the event's canonical meta (hashed by MetaDigest,
		// Ed25519-signed): the ledger record itself proves which {operation, effect} it
		// anchors, mirroring toolpins' operation_id/effect_digest commitment. The legacy v1
		// PayloadHash preimage stays untouched (compatibility).
		metaEvidenceOperationID:  string(binding.OperationID),
		metaEvidencePhase:        phase,
		metaEvidenceEffectDigest: string(binding.EffectDigest),
	}
	if attemptID != "" {
		meta[metaDecisionAttemptID] = attemptID
	}
	if downgraded {
		// This DENY is the fail-closed downgrade of a shadowed ALLOW whose evidence write
		// failed; the shadow rides so the promotion report still counts the would-be verdict.
		meta[metaEffectiveDowngrade] = true
	}
	addAuditDelegationMeta(meta, delegation)
	addShadowMeta(meta, shadow)
	var appendDropped bool
	var evidenceRef string
	txErr := d.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:       actor,
			ActorKind:   actorKind,
			Action:      "hook.tool." + decision,
			TargetKind:  hookActionCapability,
			TargetID:    model.ID(firstNonEmptyStr(in.ToolUseID, in.PlanHash)),
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

func (d *claudeHookDecider) resolveTenant(hint string, p auth.Principal, authErr error) (model.TenantID, tenantResolution) {
	if authErr == nil && p.IsPurposeRestricted() {
		return model.TenantID(""), tenantResolution{reason: "purpose-restricted credential is not a hook credential; deny-closed"}
	}
	if h := strings.TrimSpace(hint); h != "" {
		tid, err := model.ParseTenantID(h)
		if err != nil || tid.IsZero() {
			return model.TenantID(""), tenantResolution{reason: "invalid tenant in request"}
		}
		if _, ok := d.tenants[tid]; !ok {
			return model.TenantID(""), tenantResolution{reason: "no governed hook policy configured for tenant"}
		}
		// F-01: the hint is client-supplied (header X-Olivares-Hook-Tenant).
		// It may only SELECT the governing tenant when the caller is authenticated
		// AND actually a member of it (or superadmin); otherwise a member of tenant
		// A could name tenant B and borrow B's (possibly permissive) hook policy —
		// a cross-tenant governance-boundary escape. Mirrors the already-correct
		// inferenceProxyDecider.resolveTenant / appsGatewayHandler.resolveTenant.
		// Deny-closed on ambiguity.
		// staticcheck QF1001 propone De Morgan aquí. NO se aplica, y la razón es de seguridad, no de
		// gusto: esta guarda es deny-closed y la forma actual —una disyunción POSITIVA bajo una sola
		// negación— refleja literalmente la política («hay que ser superadmin O miembro»). La forma
		// `!A && !B` es equivalente hoy y pierde esa forma: quien añada una tercera condición mañana
		// tiene que acordarse de negarla también. Cero cambio de comportamiento a cambio de un riesgo
		// real en la comprobación de pertenencia.
		//nolint:staticcheck // QF1001: la disyunción positiva refleja la política; ver arriba
		if authErr != nil || !(p.Superadmin || p.IsMember(tid)) {
			return model.TenantID(""), tenantResolution{reason: "principal is not a member of the declared tenant; deny-closed"}
		}
		return tid, tenantResolution{found: true}
	}
	// No hint: fall back to the principal's sole grant when unambiguous.
	if authErr == nil {
		if ts := p.Tenants(); len(ts) == 1 {
			if _, ok := d.tenants[ts[0]]; ok {
				return ts[0], tenantResolution{found: true}
			}
		}
	}
	return model.TenantID(""), tenantResolution{reason: "tenant not declared and not inferable; deny-closed"}
}

// gateViaHITL opens/finds a governed approval bound to the plan hash and maps its status.
func (d *claudeHookDecider) gateViaHITL(ctx context.Context, tenant model.TenantID, in claude.HookDecisionInput, disp hookDisposition, actor, tier, version string) claude.HookDecisionResult {
	if d.bridge == nil {
		return deny("human approval required but the HITL bridge is not wired (deny-closed)", actor, tier, version)
	}
	reason := "Claude Code tool-call requires human approval: " + in.Tool + " (" + in.ResourceKind + ")"
	subjectRef := in.ResourceRef
	if subjectRef == "" {
		subjectRef = in.Tool
	}
	ref, status, _, err := d.bridge.gateOnce(ctx, tenant, hookActionCapability, "claude.tool", subjectRef, in.PlanHash, reason, actor)
	if err != nil {
		return deny("could not open governed approval (deny-closed)", actor, tier, version)
	}
	switch status {
	case nbApproved:
		// F-02 single-use: a human approval authorizes ONE execution, not a 24h-reusable
		// pass. SPEND the approval keyed to the exact caller so a NEW tool-call reusing an
		// already-consumed approval is a would-replay DENY.
		//
		// The consumer is the tool_use_id ONLY when the call actually carries one — that is
		// the sole non-forgeable transport correlation id, so a genuine transport RETRY of the
		// SAME tool-call re-obtains its grant (result-idempotency, bounded engine-side to a
		// short retry window). tool_use_id is a caller-controlled OPTIONAL body field the CLI
		// OMITS by design (connectors/claude/hooks.go:267-269,275); it is NEVER a trustworthy
		// single-use key, so when it is absent we do NOT fall back to the (constant) plan hash
		// — that degraded single-use to per-plan reuse and reopened F-02 under the CLI. Instead
		// we mint a fresh, unforgeable, server-side nonce: the FIRST consume binds it and ANY
		// later consume (a different minted nonce) is a would-replay DENY. Strict single-use,
		// chosen deliberately (SECURITY over convenience): without a non-forgeable transport id
		// we cannot tell a legitimate retry from a replay, so a transport-id-less retry is
		// denied and a fresh human approval is required — the documented trade-off.
		consumer := strings.TrimSpace(in.ToolUseID)
		if consumer == "" {
			consumer = newSingleUseConsumerID()
		}
		granted, replay, cerr := d.bridge.consumeApproval(ctx, tenant, ref, consumer, version)
		if cerr != nil {
			return deny("could not spend governed approval (deny-closed)", actor, tier, version)
		}
		if replay {
			// The approval was already spent by a different tool-call: this is a replay of a
			// single-use human decision. Deny-closed; the engine recorded a would-replay event
			// to the signed ledger and anchorDecision anchors this deny too.
			return deny("human approval already consumed by another tool-call; replay denied — a new human decision is required ("+ref+")", actor, tier, version)
		}
		if !granted {
			return deny("governed approval is no longer valid to spend (deny-closed)", actor, tier, version)
		}
		res := claude.HookDecisionResult{
			Permission: claude.DecisionAllow, PolicyVersion: version, PrincipalActor: actor, IdentityTier: tier,
			Reason: "approved by human review, single-use grant spent (" + ref + ")",
		}
		applyRewrite(&res, in, disp)
		return res
	case nbBreakGlass:
		// authorized under an ACTIVE break-glass emergency grant. The engine
		// recorded the use (append-only trail + ledger + finding) when it granted;
		// the reason makes the emergency nature explicit in the agent's own record.
		res := claude.HookDecisionResult{
			Permission: claude.DecisionAllow, PolicyVersion: version, PrincipalActor: actor, IdentityTier: tier,
			Reason: "authorized by BREAK-GLASS emergency access (" + ref + ") — audited; post-review required",
		}
		applyRewrite(&res, in, disp)
		return res
	case nbPending:
		return claude.HookDecisionResult{
			Permission: claude.DecisionAsk, PolicyVersion: version, PrincipalActor: actor, IdentityTier: tier,
			Reason: firstNonEmptyStr(disp.reason, "pending human approval ("+ref+")"),
		}
	default: // rejected | canceled | expired | no_gate | unknown ⇒ deny-closed
		return deny("human review did not approve (status="+status+")", actor, tier, version)
	}
}

// pdpForbids consults the live composed PDP (native ABAC + external/authored Cedar) for
// the tool-call, mapped onto an auth.Request. A non-allow decision is a hard deny. The
// evaluator is restrict-only, so this can only further-restrict the disposition.
// It returns the deny's provenance class so observe can shadow an AUTHORED policy
// forbid but never an invariant: a fail-closed evaluation error is ClassInvariant, while a
// clean forbid carries the evaluator's own dec.Class (propagated verbatim from the
// invariant-dominates chain — E1a/E1b — never hardcoded here).
func (d *claudeHookDecider) pdpForbids(ctx context.Context, p auth.Principal, tenant model.TenantID, in claude.HookDecisionInput) (bool, string, auth.DecisionClass) {
	req := auth.Request{
		Principal:  p,
		Permission: auth.Permission(hookActionCapability + ":" + modeVerb(in.Mode)),
		Tenant:     tenant,
		Resource: auth.ResourceAttrs{
			Kind: in.ResourceKind,
			ID:   in.ResourceRef,
			Extra: map[string]string{
				"tool": in.Tool,
				"mode": in.Mode,
			},
		},
	}
	dec, err := d.eval.Evaluate(ctx, req)
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
func (d *claudeHookDecider) scopedForbids(ctx context.Context, p auth.Principal, tenant model.TenantID, in claude.HookDecisionInput) (bool, string, auth.DecisionClass) {
	req, projectable := projectHookScopedRequest(p, tenant, in)
	if !projectable {
		return false, "", auth.ClassInvariant
	}
	sd, err := d.scoped.Scoped(ctx, req)
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
	if in.ResourceKind != hookResourceKindMCP {
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
		Resource:   auth.ResourceAttrs{Kind: "mcp_server", ID: server},
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
	// can aggregate by origin (shadowSourceLocalRule/LocalDefault/BashPath). It is an
	// ORTHOGONAL axis to shadowed_decision (deny|ask); empty for a non-shadowed disposition.
	source string
}

// evalHookPolicy returns the matching rule's disposition and matched=true, or a zero
// disposition and matched=false when no rule applies (the caller picks the event-class-aware
// default). Path-scoped policies never silently fall through to allow on an unresolved file
// path: they ask by default, or deny when the operator requested it.
func evalHookPolicy(pol hookPolicyDoc, in claude.HookDecisionInput) (hookDisposition, bool) {
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
			disp := hookDisposition{decision: dec, reason: r.Reason, rewrite: r.Rewrite, block: r.Block, class: class, source: shadowSourceLocalRule}
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
	if pathScoped && in.ResourceKind == hookResourceKindFile {
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

func hookPolicyPathScoped(pol hookPolicyDoc) bool {
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

const (
	hookResourceKindFile  = "file"
	hookResourceKindShell = "shell"
	// hookResourceKindMCP is the connector's resource kind for an mcp__server__tool call
	// (connectors/claude/resource.go resMCP). It is the ONLY tool kind with a sound
	// scope-grantable catalog projection today (→ mcp_server), so the F-03 central
	// scoped-forbid overlay targets it (projectHookScopedRequest).
	hookResourceKindMCP = "mcp.tool"
)

// defaultHookDisposition is the disposition for an enforceable event with NO matching rule.
// A classic permission gate (PreToolUse / PermissionRequest / UNKNOWN) honors the operator's
// policy default (deny-closed unless default:"allow"); a state-mutating gating event denies;
// every other gating event is neutral (audited).
func defaultHookDisposition(event string, enf claude.HookEnforcement, pol hookPolicyDoc) hookDisposition {
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
		return hookDisposition{decision: def, reason: reason, class: class, source: shadowSourceLocalDefault}
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
func hookRuleMatches(r hookPolicyRule, in claude.HookDecisionInput) bool {
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
		if in.ResourceKind != hookResourceKindFile {
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

func hookRulePathScoped(r hookPolicyRule) bool {
	return len(r.Paths) > 0 || strings.TrimSpace(r.Subtree) != ""
}

func hookRulePathMatches(r hookPolicyRule, abs, root string) bool {
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
		return tierUnknown
	}
	if p.Superadmin {
		return tierApproximate
	}
	if p.IsMember(tenant) {
		return tierFirm
	}
	return tierApproximate
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

// singleUseSeq is a strictly-increasing tail mixed into every minted single-use id, so
// uniqueness holds even if the entropy source is momentarily unavailable — the single-use
// guarantee must never hinge on the RNG being up.
var singleUseSeq atomic.Uint64

// newSingleUseConsumerID mints an unforgeable, server-side, unique consumer id for a
// governed tool-call that carries no tool_use_id. It is generated in the composition root
// (NEVER from caller-controlled input), so an attacker cannot make two consumes share it,
// and it is unique per call (128 bits of entropy + a monotonic counter), so the FIRST
// consume binds it and any later consume is a would-replay DENY. Predictability is
// irrelevant here — the caller never supplies the value — so a rand read error is tolerated
// (the counter tail keeps ids distinct regardless).
func newSingleUseConsumerID() string {
	var b [24]byte
	_, _ = rand.Read(b[:16])                                // 128 bits of entropy (best-effort)
	binary.BigEndian.PutUint64(b[16:], singleUseSeq.Add(1)) // monotonic tail: uniqueness never depends on the RNG
	return singleUseConsumerPrefix + hex.EncodeToString(b[:])
}

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
type claudeHookAuditor struct{ log *slog.Logger }

func (a claudeHookAuditor) Record(_ context.Context, in claude.HookDecisionInput, res claude.HookDecisionResult, denyClosed bool) {
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
