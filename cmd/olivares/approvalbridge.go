// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/deploy"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/modules/voice"
)

// This file is the OUTBOUND HALF of the HITL round-trip and the common precondition
// of all of Phase K: the in-process adapter that implements the four ApprovalGate
// seams ({deploy,orchestration,voice,security}.ApprovalGate) by opening a GOVERNED
// approval against the engine. It is the bridge wire.go names as missing —
// "governance exposes no in-process Go approval API (only HTTP routes), so [the
// gates] stay deny-closed until a composition-root HTTP bridge ... is built". The
// INBOUND half (turning an approve/deny click into a decision) lives in hitl.go;
// this is its mirror image — it OPENS the approval the inbound half later resolves.
//
// It lives in the AGPL wiring (cmd/olivares), NEVER in a connector, for the same
// reason hitl.go does: it consumes the engine's governed API in-process, and a
// connector may not import /core. It reuses captureWriter (hitl.go) to call the
// engine's own handler without opening a socket to itself.
//
// The contract is the 2026 governed-actuation research one — PROPOSE → CLASSIFY →
// GATE → (HITL queue) → ACTUATE → RECEIPT — mapped onto the Claude Agent SDK
// permission model (allow | deny | ask | defer):
//
//   - PROPOSE   The actuating module asks the gate (Request/Authorize). The PROPOSER
//               is a SERVICE identity, never a human: an operator-provisioned,
//               editor-scoped API token (governance:approval:{read,write}, NOT admin)
//               — so the bridge can OPEN and READ approvals but can never DECIDE one,
//               and separation-of-duty keys on a principal that never approves.
//   - GATE      A new approval starts pending → the gate reports a DENY ("ask"); the
//               actuation blocks. The approval is bound to the EXACT PlanHash a human
//               will see (anti-TOCTOU): a re-plan changes the hash and voids it.
//   - ACTUATE   Only an effective "approved" status bound to the matching PlanHash
//               authorizes the mutation; the module re-checks at apply (phase 2).
//   - RECEIPT self-audits the create and the decision to the append-only
//               action→human ledger; the bridge logs a non-secret receipt line.
//
// Safety posture (docs/SECURITY-HARDENING.md/§1). DENY-CLOSED at every edge:
//   - zero value           a zero GateDecision is StatusNoGate → Allowed()==false.
//   - any error            surfaced to the caller, which treats it as a denial.
//   - unconfigured tenant  no service credential → deny (mirrors the module denyGate
//                          exactly, "no-gate:<hash>"), warned once, never opened.
//   - handler not yet bound a boot race → deny (the gate refuses rather than guess).
//   - time-box             every opened approval carries an expiry, so a forgotten
//                          request lapses to "expired" (deny) rather than lingering.
//   - idempotency          a repeated Request for the same (tenant, action, subject,
//                          plan) reuses the open approval instead of spamming the
//                          queue — in-process memo + a durable pending-scan recovery.
//
// It NEVER decides and opens no path around: SoD, the duplicate-decider guard,
// the approval threshold and lazy expiry are all enforced by the engine on the
// inbound decision. This half only PROPOSES, time-boxed and bound to the plan.
//
// PROPOSER IDENTITY (operator responsibility, NOT enforced here). The bridge proposes
// as the configured token stamps the approval's requested_by_user with that
// token's OWNER (a bound token inherits its minter's user id — core/auth/accounts.go).
// separation-of-duty then forbids that owner from DECIDING the approval. So the
// service token MUST be owned by a DEDICATED service account that is never in the
// approver pool — otherwise that human is locked out of approving the bridge's
// requests (a liveness failure, still fail-closed). The editor scope only guarantees
// the token cannot itself decide; it does NOT make the owner disjoint from approvers.
// This is the same operator-provisioning contract as the inbound approver tokens.

// --- configuration (operator-provisioned, secret-bearing, out of the store) -------

// loadApprovalBridgeConfig reads the optional OUTBOUND-gate provisioning file named by
// OLIVARES_APPROVAL_BRIDGE_CONFIG (a JSON approvalBridgeConfig), the same operator-
// secret pattern as OLIVARES_HITL_CONFIG / OLIVARES_NOTIFY_CONFIG: the service tokens
// the bridge proposes as live here by value, never in the store. A missing path yields
// an empty config (the gate is simply not wired and the modules keep their deny-closed
// defaults); a supplied path must be readable and contain valid JSON or startup fails.
func loadApprovalBridgeConfig(_ *slog.Logger) (approvalBridgeConfig, error) {
	path := os.Getenv("OLIVARES_APPROVAL_BRIDGE_CONFIG")
	if path == "" {
		return approvalBridgeConfig{}, nil
	}
	var cfg approvalBridgeConfig
	if err := loadOperatorJSONConfig("OLIVARES_APPROVAL_BRIDGE_CONFIG", path, &cfg); err != nil {
		return approvalBridgeConfig{}, err
	}
	return cfg, nil
}

// approvalBridgeConfig is the operator's OUTBOUND-gate provisioning. Per-tenant
// service tokens (a business deployment is multi-tenant; each tenant's governance is
// its own), with global time-box defaults a tenant entry may override.
type approvalBridgeConfig struct {
	Tenants []approvalBridgeTenant `json:"tenants"`
	// ExpiresInSeconds / EscalateInSeconds are the global defaults for the approval
	// time-box (a tenant entry overrides them). A matching approval POLICY is
	// authoritative over both — these are the floor when no policy matches.
	ExpiresInSeconds  int64 `json:"expires_in_seconds,omitempty"`
	EscalateInSeconds int64 `json:"escalate_in_seconds,omitempty"`
}

// approvalBridgeTenant maps one business tenant to the SERVICE credential the bridge
// proposes approvals as. The token MUST be an API token issued to a DEDICATED non-human
// service account (one never granted an approver role — see the proposer-identity note
// in the file header) and scoped to governance:approval:{read,write} (the editor role)
// — never admin: it can open and read approvals but can never decide one. The bridge
// trusts this provisioning contract (it cannot introspect the token's scope/owner at
// boot); a too-privileged or approver-owned token is an operator misconfiguration, not
// a hole the bridge can close. It is a secret (operator config, never the store, never
// logged).
type approvalBridgeTenant struct {
	Tenant            string `json:"tenant"`
	Token             string `json:"token"`
	ExpiresInSeconds  int64  `json:"expires_in_seconds,omitempty"`
	EscalateInSeconds int64  `json:"escalate_in_seconds,omitempty"`
}

// time-box defaults / cap (the cap mirrors governance's maxSeconds = one year).
const (
	defaultApprovalExpirySeconds   = int64(24 * 3600) // a forgotten request lapses in 24h
	defaultApprovalEscalateSeconds = int64(0)         // no escalation unless configured
	maxApprovalWindowSeconds       = int64(365 * 24 * 3600)
)

// --- the bridge -------------------------------------------------------------------

// serviceCred is the resolved per-tenant proposer credential plus its time-box.
type serviceCred struct {
	tenant     model.TenantID
	tenantStr  string
	token      string
	expiresIn  int64
	escalateIn int64
}

// approvalBridge implements the four ApprovalGate seams against over the engine's
// own in-process handler. It is constructed in buildModules (before the API server
// exists) and has its handler late-bound by boot() via useHandler once api.New
// returns — the gate is only ever CALLED at request time, long after boot, so the
// late binding is safe and a call before binding fails closed.
type approvalBridge struct {
	creds map[model.TenantID]serviceCred
	log   *slog.Logger
	// clock backs the approved-grant freshness window (tests inject a fixed clock).
	clock func() time.Time

	// handler is the engine's API handler, late-bound after api.New (boot.go). Reads
	// before it is set fail closed.
	handlerMu sync.RWMutex
	handler   http.Handler

	// memo gives idempotency within a process lifetime: identity → approval id, so a
	// repeated Request reuses the open approval rather than opening a duplicate. It is
	// a cache, not the source of truth — a cold miss falls back to the durable scan.
	// It grows with distinct (tenant, action, subject, plan) identities; governance
	// approvals are low-frequency, so it stays small in practice.
	memoMu sync.Mutex
	memo   map[string]string

	// locks serializes find-or-create PER IDENTITY WITHIN THIS PROCESS, so two
	// concurrent Requests for the same plan cannot both open an approval. Has no
	// unique constraint to dedupe, so cross-PROCESS idempotency is best-effort: a
	// second control-plane writing the same plan concurrently could open a duplicate
	// pending approval (benign — the module persists one ref and the extra lapses at
	// its time-box). The control plane is single-writer by deployment (one process
	// owns the store), which the in-process lock+memo+pending-scan fully covers.
	locks keyedMutex

	// warned tracks the tenants for which the "no credential" warning has fired, so a
	// missing-config tenant is visible exactly once, not on every actuation.
	warned sync.Map // model.TenantID -> struct{}
}

// newApprovalBridge builds the bridge from the operator config. A tenant entry with an
// invalid tenant id or no token is skipped with a warning (a visible misconfiguration,
// never a silently-open gate). It returns nil when no usable tenant is configured —
// the honest absence that leaves every module's deny-closed default in place
// (mirroring newHITLReceiver's nil-when-empty contract).
func newApprovalBridge(cfg approvalBridgeConfig, log *slog.Logger) *approvalBridge {
	creds := map[model.TenantID]serviceCred{}
	for _, tc := range cfg.Tenants {
		tid, present, err := parseBusinessTenant("approval-bridge config: tenant", tc.Tenant)
		if err != nil || !present {
			log.Warn("approval-bridge: tenant entry has an invalid tenant id; skipped", "tenant", tc.Tenant)
			continue
		}
		if strings.TrimSpace(tc.Token) == "" {
			log.Warn("approval-bridge: tenant entry has no service token; skipped (cannot open governed approvals)", "tenant", tc.Tenant)
			continue
		}
		if _, dup := creds[tid]; dup {
			log.Warn("approval-bridge: duplicate tenant entry; later definition ignored", "tenant", tc.Tenant)
			continue
		}
		creds[tid] = serviceCred{
			tenant:     tid,
			tenantStr:  tid.String(),
			token:      tc.Token,
			expiresIn:  resolveWindow(tc.ExpiresInSeconds, cfg.ExpiresInSeconds, defaultApprovalExpirySeconds),
			escalateIn: resolveWindow(tc.EscalateInSeconds, cfg.EscalateInSeconds, defaultApprovalEscalateSeconds),
		}
	}
	if len(creds) == 0 {
		return nil
	}
	log.Info("approval-bridge: OUTBOUND ApprovalGate wired", "tenants", len(creds))
	return &approvalBridge{creds: creds, log: log, clock: time.Now, memo: map[string]string{}}
}

// resolveWindow picks the tenant override, else the global default, else the built-in
// default, clamped to [0, one year]. A positive value wins at each tier; the built-in
// default applies only when neither override is set.
func resolveWindow(tenant, global, builtin int64) int64 {
	v := builtin
	if global > 0 {
		v = global
	}
	if tenant > 0 {
		v = tenant
	}
	if v < 0 {
		return 0
	}
	if v > maxApprovalWindowSeconds {
		return maxApprovalWindowSeconds
	}
	return v
}

// useHandler late-binds the engine's API handler (boot.go, after api.New).
func (b *approvalBridge) useHandler(h http.Handler) {
	b.handlerMu.Lock()
	b.handler = h
	b.handlerMu.Unlock()
}

func (b *approvalBridge) currentHandler() http.Handler {
	b.handlerMu.RLock()
	defer b.handlerMu.RUnlock()
	return b.handler
}

func (b *approvalBridge) cred(tenant model.TenantID) (serviceCred, bool) {
	c, ok := b.creds[tenant]
	return c, ok
}

// warnUnconfigured emits the "no credential for this tenant" warning once per tenant.
func (b *approvalBridge) warnUnconfigured(tenant model.TenantID) {
	if _, loaded := b.warned.LoadOrStore(tenant, struct{}{}); loaded {
		return
	}
	b.log.Warn("approval-bridge: no service credential configured for this tenant; governed actuation DENIED (deny-by-default) — add it to OLIVARES_APPROVAL_BRIDGE_CONFIG to enable governed actuation here", "tenant", tenant.String())
}

// --- the neutral lifecycle the bridge speaks --------------------------------------

// Neutral status strings. The first five are own stored/effective status values
// (so they double as the list-filter values); nbNoGate is the bridge's deny-closed
// "no usable gate for this tenant"; nbBreakGlass is an authorization under an
// ACTIVE break-glass emergency grant — it maps to "approved" at every module seam, but
// the distinct status + the "breakglass:" reference keep it permanently distinguishable
// from a quorum approval in every receipt, log line and ledger event.
const (
	nbPending    = "pending"
	nbApproved   = "approved"
	nbRejected   = "rejected"
	nbCanceled   = "canceled"
	nbExpired    = "expired"
	nbNoGate     = "no_gate"
	nbBreakGlass = "break_glass"
)

// noGateRefPrefix matches the module denyGate's reference format exactly, so an
// unconfigured tenant is indistinguishable from "no gate wired" to the module (it
// records the same kind of reference and reports the same ungoverned gap).
const noGateRefPrefix = "no-gate:"

// breakGlassRefPrefix marks a gate reference that was authorized under a break-glass
// emergency grant rather than an approval quorum: "breakglass:<grant-id>", plus
// the same "#plan=" binding suffix an approval subject carries when the actuation is
// plan-bound. The prefix is load-bearing evidence — it lands in the module's persisted
// operation record, so an actuation that proceeded under emergency access can never be
// mistaken for an approved one.
const breakGlassRefPrefix = "breakglass:"

// planBindingMarker binds the exact plan hash into the approval's subject_ref (has
// no plan_hash column; the bridge owns this field end-to-end). It is not a credential
// pattern, so it passes the engine's inline-credential guard. A normal subject (a
// deployment label, agent/schedule id, guardrail class) never contains it; decoding
// takes the LAST occurrence and the appended hash is always last, so the round-trip is
// unambiguous.
const planBindingMarker = "#plan="

// maxReasonChars mirrors governance maxNoteLen (the bound on reason/subject_ref).
const maxReasonChars = 4096

var errBridgeUnavailable = errors.New("approval-bridge: governed approval API not available")

// request opens (or idempotently finds) a governed approval for a two-phase actuation
// and returns its reference, effective status and the bound plan hash. An unconfigured
// tenant denies exactly like the module's own denyGate.
func (b *approvalBridge) request(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy string) (ref, status, boundHash string, err error) {
	cred, ok := b.cred(tenant)
	if !ok {
		b.warnUnconfigured(tenant)
		return noGateRefPrefix + planHash, nbNoGate, planHash, nil
	}
	return b.ensureApproval(ctx, cred, action, subjectKind, subjectRef, planHash, reason, requestedBy, false)
}

// gateOnce is the ONE-SHOT, plan-bound gate the hooks PEP uses: it opens (or
// idempotently finds) a governed approval bound to the exact plan hash AND reuses an
// already-APPROVED grant within its time-box. Unlike request (the two-phase open the
// deploy/orchestration gates pair with a later status() at apply time), the hook PEP is
// re-issued on every agent retry of the same tool-call and holds no ref between calls, so
// it must re-derive the decision from the plan hash each time: a fresh call opens pending
// (→ ask), and once a human approves, the next identical call finds the approved grant
// (within its window) and allows. It mirrors the security posture gate's reuse-approved
// flow (securityApprovalAdapter), but binds the plan hash for anti-TOCTOU. An
// unconfigured tenant denies exactly like the module's own denyGate.
func (b *approvalBridge) gateOnce(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy string) (ref, status, boundHash string, err error) {
	cred, ok := b.cred(tenant)
	if !ok {
		b.warnUnconfigured(tenant)
		return noGateRefPrefix + planHash, nbNoGate, planHash, nil
	}
	ref, status, boundHash, err = b.ensureApproval(ctx, cred, action, subjectKind, subjectRef, planHash, reason, requestedBy, true)
	if err != nil || status != nbPending {
		return ref, status, boundHash, err
	}
	// Break-glass: an approval still awaiting its human quorum may proceed
	// under an ACTIVE emergency grant. The engine records the use (append-only
	// trail + ledger + finding) in the same transaction that grants it — never a
	// silent path — and the underlying approval stays pending in the queue for the
	// humans to see. A consume miss/error changes nothing (the normal deny-closed
	// pending stands).
	if grant, ok := b.breakGlassConsumeUnlessRejected(ctx, cred, action, subjectKind, encodeSubjectRef(subjectRef, planHash)); ok {
		b.log.Warn("approval-bridge: action authorized under BREAK-GLASS emergency access",
			"tenant", cred.tenantStr, "action", action, "subject_kind", subjectKind,
			"grant", grant, "approval_ref", ref, "plan_bound", planHash != "")
		return breakGlassRef(grant, planHash), nbBreakGlass, planHash, nil
	}
	return ref, status, boundHash, nil
}

// status reports the effective decision for a previously-opened approval, plus the plan
// hash it was BOUND to (read from storage, never echoed from the caller) so the module
// can enforce anti-TOCTOU. An unconfigured tenant or a denyGate-style reference denies.
//
// Break-glass, both directions:
//   - a "breakglass:" reference (handed out by gateOnce/status under an active grant)
//     keeps authorizing ONLY while its grant is still effectively active — revoke or
//     expiry turns it into a deny, and the bound plan hash is decoded from the
//     REFERENCE the module persisted, never echoed from the caller;
//   - an approval reference whose effective status is pending (quorum not reached) or
//     expired (lapsed undecided) may proceed under an active grant: the consume is
//     recorded engine-side against the approval's STORED action/subject/plan. An
//     explicit human rejection or cancel is NEVER overridden by break-glass.
func (b *approvalBridge) status(ctx context.Context, tenant model.TenantID, ref, planHash string) (status, boundHash string, err error) {
	cred, ok := b.cred(tenant)
	if !ok || strings.HasPrefix(ref, noGateRefPrefix) {
		return nbNoGate, planHash, nil
	}
	if grantID, bound, isBG := decodeBreakGlassRef(ref); isBG {
		if b.breakGlassActive(ctx, cred, grantID) {
			return nbBreakGlass, bound, nil
		}
		return nbExpired, bound, nil
	}
	v, rerr := b.readApproval(ctx, cred, ref)
	if rerr != nil {
		return "", "", rerr
	}
	// The two-phase gates (deploy/orchestration/voice) re-open a FRESH pending
	// approval whenever they re-enter phase 1 without a ref — so a plan two humans
	// already rejected can reappear as a new pending row. The rejection guard keys
	// on the STORED action/subject (which carries the plan binding), so a prior
	// rejection of THIS exact identity still blocks break-glass even though it
	// lives on a different (terminal) approval row than the one being polled here.
	if (v.status == nbPending || v.status == nbExpired) && v.action != "" {
		if grant, ok := b.breakGlassConsumeUnlessRejected(ctx, cred, v.action, v.subjectKind, v.subjectRef); ok {
			b.log.Warn("approval-bridge: actuation authorized under BREAK-GLASS emergency access",
				"tenant", cred.tenantStr, "action", v.action, "subject_kind", v.subjectKind,
				"grant", grant, "approval_ref", ref)
			return nbBreakGlass, v.boundHash, nil
		}
	}
	return v.status, v.boundHash, nil
}

// statusScoped is status() PLUS the item 2 authorization-substitution guard:
// it verifies the approval (or break-glass grant) authorized THIS exact action +
// subject, not merely a matching plan hash. Without it, a governance writer can
// mint a low-risk approval whose subject_ref encodes the target plan hash and
// submit it to a privileged fire (Status returns the matching hash), and a
// break-glass reference — parsed entirely from client input — authorizes any
// action while its grant is active regardless of the grant's real scope.
func (b *approvalBridge) statusScoped(ctx context.Context, tenant model.TenantID, ref, planHash, wantAction, wantSubjectKind, wantSubjectRef string) (status, boundHash string, err error) {
	cred, ok := b.cred(tenant)
	if !ok || strings.HasPrefix(ref, noGateRefPrefix) {
		return nbNoGate, planHash, nil
	}
	encodedWant := encodeSubjectRef(wantSubjectRef, planHash)
	if grantID, bound, isBG := decodeBreakGlassRef(ref); isBG {
		_ = grantID
		// A break-glass reference authorizes ONLY if its grant actually covers the
		// EXPECTED action + subject (the forged-ref bypass): scope-check it through
		// the same engine-side consume the standard path uses (which authorizes
		// only within the grant's declared scope), never a bare "grant is active".
		if grant, granted := b.breakGlassConsumeUnlessRejected(ctx, cred, wantAction, wantSubjectKind, encodedWant); granted {
			b.log.Warn("approval-bridge: actuation authorized under BREAK-GLASS (scope-verified)",
				"tenant", cred.tenantStr, "action", wantAction, "subject_kind", wantSubjectKind, "grant", grant)
			return nbBreakGlass, bound, nil
		}
		return nbExpired, bound, nil
	}
	v, rerr := b.readApproval(ctx, cred, ref)
	if rerr != nil {
		return "", "", rerr
	}
	// Scope guard (DENY-CLOSED): the STORED approval must have authorized
	// THIS exact action + subject. A mismatch — OR an empty/incomplete stored
	// scope (a legacy/corrupt/not-found row) — is refused; the comparison is NEVER
	// skipped. Previously the guard only ran when v.action != "", so a partial
	// approved row with an empty action but a valid subject/plan slipped through.
	if v.action == "" || v.action != wantAction || v.subjectKind != wantSubjectKind || v.subjectRef != encodedWant {
		b.log.Warn("approval-bridge: DENIED — approval scope missing or does not match the requested actuation (substitution guard)",
			"tenant", cred.tenantStr, "want_action", wantAction, "stored_action", v.action,
			"want_subject_kind", wantSubjectKind, "stored_subject_kind", v.subjectKind)
		return nbExpired, "", nil
	}
	if (v.status == nbPending || v.status == nbExpired) && v.action != "" {
		if grant, granted := b.breakGlassConsumeUnlessRejected(ctx, cred, v.action, v.subjectKind, v.subjectRef); granted {
			b.log.Warn("approval-bridge: actuation authorized under BREAK-GLASS emergency access",
				"tenant", cred.tenantStr, "action", v.action, "subject_kind", v.subjectKind, "grant", grant, "approval_ref", ref)
			return nbBreakGlass, v.boundHash, nil
		}
	}
	return v.status, v.boundHash, nil
}

// breakGlassConsumeUnlessRejected is the single, shared break-glass gate for both
// the one-shot (gateOnce) and two-phase (status) paths: it authorizes a use under
// an active emergency grant ONLY when no explicit human REJECTION of this exact
// (action, subjectKind, encoded-subject-with-plan) identity is on record. Without
// this guard the emergency valve would re-authorize a change two humans examined
// and refused — a re-entrant gate mints a fresh pending approval for the identical
// plan, which break-glass would otherwise consume. The rejected record blocks the
// exact plan; a re-plan changes the encoded hash and is a new question.
//
// Fails CLOSED for break-glass: a rejection-scan error skips the consume (the
// normal pending/expired deny stands), so a flaky read can never widen access.
func (b *approvalBridge) breakGlassConsumeUnlessRejected(ctx context.Context, cred serviceCred, action, subjectKind, encodedSubjectRef string) (grantID string, granted bool) {
	if rejRef, found, _, serr := b.scanForApproval(ctx, cred, action, subjectKind, encodedSubjectRef, nbRejected, 0); serr != nil || found {
		if found {
			b.log.Info("approval-bridge: break-glass not consulted — this exact plan was explicitly rejected",
				"tenant", cred.tenantStr, "action", action, "rejected_ref", rejRef)
		}
		return "", false
	}
	return b.consumeBreakGlass(ctx, cred, action, subjectKind, encodedSubjectRef)
}

// breakGlassRef encodes a break-glass authorization reference, binding the plan hash
// exactly like an approval subject does so a later status() read returns the hash the
// module's persisted reference carries (anti-TOCTOU: a re-plan changes the queried hash
// and the mismatch denies).
func breakGlassRef(grantID, planHash string) string {
	if planHash == "" {
		return breakGlassRefPrefix + grantID
	}
	return breakGlassRefPrefix + grantID + planBindingMarker + planHash
}

// decodeBreakGlassRef parses a break-glass reference into its grant id and bound plan
// hash; isBG is false for any other reference shape.
func decodeBreakGlassRef(ref string) (grantID, boundHash string, isBG bool) {
	if !strings.HasPrefix(ref, breakGlassRefPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(ref, breakGlassRefPrefix)
	if i := strings.LastIndex(rest, planBindingMarker); i >= 0 {
		return rest[:i], rest[i+len(planBindingMarker):], true
	}
	return rest, "", true
}

// consumeBreakGlass asks the engine to authorize ONE action under an active break-glass
// grant — and, when granted, the engine records the use (append-only trail + hash-chain
// ledger event + finding) in the same transaction, so the authorization and its evidence
// are inseparable. Deny-closed: any error, non-2xx or malformed response is simply "not
// granted" and the caller's normal pending/expired deny stands (break-glass can only ADD
// an audited authorization, never remove a denial).
func (b *approvalBridge) consumeBreakGlass(ctx context.Context, cred serviceCred, action, subjectKind, encodedSubjectRef string) (grantID string, granted bool) {
	body := map[string]any{"action": action, "subject_kind": subjectKind, "subject_ref": encodedSubjectRef}
	code, raw := b.do(ctx, cred, http.MethodPost, "/v1/m/governance/breakglass/consume", body)
	if code != http.StatusOK {
		return "", false
	}
	var dto struct {
		Granted bool   `json:"granted"`
		Grant   string `json:"grant"`
	}
	if jerr := json.Unmarshal(raw, &dto); jerr != nil || !dto.Granted || dto.Grant == "" {
		return "", false
	}
	return dto.Grant, true
}

// consumeApproval SPENDS an approved approval exactly once (F-02). It is the
// single-use half the hooks PEP calls the instant an approval is found APPROVED:
// the human decision authorizes ONE execution, not a 24h-reusable pass. consumerID is
// the exact caller claiming the grant (the Claude Code tool_use_id), which lets the
// engine separate the two things F-02 conflated:
//   - result-idempotency: the SAME consumerID re-consumes idempotently (granted, no
//     replay) — a legitimate transport retry re-obtains its grant, never re-authorizing;
//   - permission-reuse: a DIFFERENT consumerID on an already-consumed approval is a
//     would-replay (granted=false, replay=true) — the engine denies it and records the
//     attempt to the signed ledger + a finding.
//
// It mirrors consumeBreakGlass: deny-closed on every edge — an unconfigured tenant, any
// transport error, a non-2xx or a malformed response is "not granted", and the caller's
// deny-closed path stands. A clean would-replay is NOT an error: it returns replay=true so
// the caller can render the honest would-replay deny.
func (b *approvalBridge) consumeApproval(ctx context.Context, tenant model.TenantID, ref, consumerID, policyVersion string) (granted, replay bool, err error) {
	cred, ok := b.cred(tenant)
	if !ok {
		// No service credential for this tenant: the bridge made no call and cannot
		// spend a grant. Deny-closed (never a silent single-use bypass).
		return false, false, nil
	}
	body := map[string]any{"consumer_id": consumerID}
	if policyVersion != "" {
		body["policy_version"] = policyVersion
	}
	code, raw := b.do(ctx, cred, http.MethodPost, "/v1/m/governance/approvals/"+url.PathEscape(ref)+"/consume", body)
	if code == 0 {
		return false, false, errBridgeUnavailable
	}
	if code != http.StatusOK {
		return false, false, fmt.Errorf("approval-bridge: approval consume failed (%d)", code)
	}
	var dto struct {
		Granted bool `json:"granted"`
		Replay  bool `json:"replay"`
	}
	if jerr := json.Unmarshal(raw, &dto); jerr != nil {
		return false, false, fmt.Errorf("approval-bridge: malformed consume response: %w", jerr)
	}
	return dto.Granted, dto.Replay, nil
}

// breakGlassActive reports whether a grant is still effectively active (a read, not a
// consume — the authorizing use was already recorded when the reference was handed out).
// Deny-closed: any error or non-2xx reads as not-active.
func (b *approvalBridge) breakGlassActive(ctx context.Context, cred serviceCred, grantID string) bool {
	code, raw := b.do(ctx, cred, http.MethodGet, "/v1/m/governance/breakglass/"+url.PathEscape(grantID), nil)
	if code != http.StatusOK {
		return false
	}
	var dto struct {
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal(raw, &dto); jerr != nil {
		return false
	}
	return dto.Status == "active"
}

// ensureApproval is the idempotent open: reuse an open (and, for the one-shot security
// gate, an already-approved) approval for this identity, else open a fresh one bound to
// the exact plan hash. The returned boundHash is the plan hash this approval is bound
// to (equal to planHash by construction on every reuse/create path).
func (b *approvalBridge) ensureApproval(ctx context.Context, cred serviceCred, action, subjectKind, subjectRef, planHash, reason, requestedBy string, reuseApproved bool) (ref, status, boundHash string, err error) {
	encoded := encodeSubjectRef(subjectRef, planHash)
	key := idemKey(cred.tenant, action, subjectKind, encoded)
	unlock := b.locks.lock(key)
	defer unlock()

	// 1. In-process memo (fast idempotency within this process lifetime).
	if memoRef := b.memoGet(key); memoRef != "" {
		if v, rerr := b.readApproval(ctx, cred, memoRef); rerr == nil && b.reusable(cred, v, reuseApproved) {
			return memoRef, v.status, v.boundHash, nil
		}
		// Terminal/expired/stale/gone: drop the stale memo and open a fresh request.
		b.memoDel(key)
	}

	// 2. Durable lookup (idempotency across restarts; recover a still-valid approved
	//    posture for the one-shot security gate). Best-effort: a list error opens a
	//    fresh approval rather than blocking a legitimate actuation.
	if foundRef, v, ferr := b.findReusable(ctx, cred, action, subjectKind, encoded, reuseApproved); ferr != nil {
		b.log.Warn("approval-bridge: idempotency lookup failed; opening a fresh approval", "tenant", cred.tenantStr, "action", action, "err", ferr)
	} else if foundRef != "" {
		b.memoSet(key, foundRef)
		return foundRef, v.status, planHash, nil
	}

	// 3. PROPOSE: open a new governed approval, bound to the exact plan hash.
	newRef, newStatus, cerr := b.createApproval(ctx, cred, action, subjectKind, encoded,
		composeReason(action, subjectKind, subjectRef, planHash, requestedBy, reason))
	if cerr != nil {
		return "", "", "", cerr
	}
	b.memoSet(key, newRef)
	b.log.Info("approval-bridge: opened governed approval",
		"tenant", cred.tenantStr, "action", action, "subject_kind", subjectKind,
		"approval_ref", newRef, "plan_bound", planHash != "", "status", newStatus)
	return newRef, newStatus, planHash, nil
}

// approvalView is the bridge's read of one approval: its effective status, the plan
// hash it was bound to (decoded from the stored subject_ref, never the queried one),
// when it was decided (backing the approved-grant freshness window), and the stored
// action/subject identity (what a break-glass consume is recorded against).
type approvalView struct {
	status      string
	action      string
	subjectKind string
	subjectRef  string // the STORED, encoded subject (carries the "#plan=" binding)
	boundHash   string
	decidedAt   string
}

// readApproval GETs one approval as the service principal and returns its view. A
// missing/foreign approval is a lapsed terminal deny (never an authorization).
func (b *approvalBridge) readApproval(ctx context.Context, cred serviceCred, ref string) (approvalView, error) {
	code, raw := b.do(ctx, cred, http.MethodGet, "/v1/m/governance/approvals/"+url.PathEscape(ref), nil)
	switch {
	case code == 0:
		return approvalView{}, errBridgeUnavailable
	case code == http.StatusNotFound:
		// Unknown / foreign-tenant / swept approval: treat as expired (a clean deny),
		// with an empty bound hash so a two-phase plan match also fails.
		return approvalView{status: nbExpired}, nil
	case code < 200 || code >= 300:
		return approvalView{}, fmt.Errorf("approval-bridge: governance status read failed (%d)", code)
	}
	var dto struct {
		Status      string `json:"status"`
		Action      string `json:"action"`
		SubjectKind string `json:"subject_kind"`
		SubjectRef  string `json:"subject_ref"`
		DecidedAt   string `json:"decided_at"`
	}
	if jerr := json.Unmarshal(raw, &dto); jerr != nil {
		return approvalView{}, fmt.Errorf("approval-bridge: malformed status response: %w", jerr)
	}
	return approvalView{
		status: dto.Status, action: dto.Action, subjectKind: dto.SubjectKind,
		subjectRef: dto.SubjectRef, boundHash: decodePlanHash(dto.SubjectRef), decidedAt: dto.DecidedAt,
	}, nil
}

// reusable reports whether an existing approval may satisfy a repeated actuation
// request. A PENDING approval is always reusable (a pending past its expiry reads as
// "expired", not "pending", via the engine's effective status). An APPROVED approval is
// reusable ONLY for the one-shot security gate (reuseApproved) AND only within the
// time-box of its decision — so a human-approved enforcement grant is NOT permanent:
// after the window a re-enable opens a fresh request and needs fresh approval, honoring
// the time-box doctrine. Everything else (rejected/canceled/expired/unknown) is not
// reusable → a fresh request is opened.
func (b *approvalBridge) reusable(cred serviceCred, v approvalView, reuseApproved bool) bool {
	switch v.status {
	case nbPending:
		return true
	case nbApproved:
		return reuseApproved && b.withinGrant(v.decidedAt, cred.expiresIn)
	default:
		return false
	}
}

// withinGrant reports whether an approval decided at decidedAt is still inside its
// reuse window (windowSecs after the decision). It fails closed: an unparseable or
// missing timestamp, or a non-positive window, is NOT within grant (do not reuse).
func (b *approvalBridge) withinGrant(decidedAt string, windowSecs int64) bool {
	if windowSecs <= 0 {
		return false
	}
	ts, err := model.ParseTimestamp(decidedAt)
	if err != nil {
		return false
	}
	return b.clock().Before(ts.Time().Add(time.Duration(windowSecs) * time.Second))
}

// createApproval POSTs a new pending approval as the service principal (write-tier),
// time-boxed. Self-audits the create to the action→human ledger.
func (b *approvalBridge) createApproval(ctx context.Context, cred serviceCred, action, subjectKind, encodedSubjectRef, reason string) (ref, status string, err error) {
	body := map[string]any{
		"subject_kind": subjectKind,
		"subject_ref":  encodedSubjectRef,
		"action":       action,
		"reason":       reason,
	}
	if cred.expiresIn > 0 {
		body["expires_in_seconds"] = cred.expiresIn
	}
	if cred.escalateIn > 0 {
		body["escalate_in_seconds"] = cred.escalateIn
	}
	code, raw := b.do(ctx, cred, http.MethodPost, "/v1/m/governance/approvals", body)
	if code == 0 {
		return "", "", errBridgeUnavailable
	}
	if code != http.StatusCreated {
		return "", "", fmt.Errorf("approval-bridge: governance approval create failed (%d)", code)
	}
	var dto struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if jerr := json.Unmarshal(raw, &dto); jerr != nil || dto.ID == "" {
		return "", "", fmt.Errorf("approval-bridge: malformed create response")
	}
	return dto.ID, dto.Status, nil
}

// findReusable scans for an existing approval that may satisfy this actuation. PENDING
// is always scanned (a small, bounded set of open requests). APPROVED is scanned only
// for the one-shot security gate (reuseApproved) and only within the grant time-box, so
// a stale approved grant is skipped and a fresh approval is required.
func (b *approvalBridge) findReusable(ctx context.Context, cred serviceCred, action, subjectKind, encodedSubjectRef string, reuseApproved bool) (ref string, v approvalView, err error) {
	if foundRef, found, view, serr := b.scanForApproval(ctx, cred, action, subjectKind, encodedSubjectRef, nbPending, 0); serr != nil {
		return "", approvalView{}, serr
	} else if found {
		return foundRef, view, nil
	}
	if reuseApproved {
		if foundRef, found, view, serr := b.scanForApproval(ctx, cred, action, subjectKind, encodedSubjectRef, nbApproved, cred.expiresIn); serr != nil {
			return "", approvalView{}, serr
		} else if found {
			return foundRef, view, nil
		}
	}
	return "", approvalView{}, nil
}

// scanForApproval pages the approvals list filtered by (status, action) and returns the
// FIRST one whose subject_kind and stored subject_ref match this identity (and, when
// freshWindowSecs > 0, was decided within that window). It re-checks the EFFECTIVE
// status equals what was asked, so a stored-pending request already past its expiry is
// never reused as if it were live.
func (b *approvalBridge) scanForApproval(ctx context.Context, cred serviceCred, action, subjectKind, encodedSubjectRef, wantStatus string, freshWindowSecs int64) (ref string, found bool, v approvalView, err error) {
	cursor := ""
	for {
		path := "/v1/m/governance/approvals?status=" + url.QueryEscape(wantStatus) +
			"&action=" + url.QueryEscape(action) + "&limit=200"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		code, raw := b.do(ctx, cred, http.MethodGet, path, nil)
		if code == 0 {
			return "", false, approvalView{}, errBridgeUnavailable
		}
		if code != http.StatusOK {
			return "", false, approvalView{}, fmt.Errorf("approval-bridge: governance list failed (%d)", code)
		}
		var resp struct {
			Items []struct {
				ID          string `json:"id"`
				SubjectKind string `json:"subject_kind"`
				SubjectRef  string `json:"subject_ref"`
				Status      string `json:"status"`
				DecidedAt   string `json:"decided_at"`
			} `json:"items"`
			Cursor  string `json:"cursor"`
			HasMore bool   `json:"has_more"`
		}
		if jerr := json.Unmarshal(raw, &resp); jerr != nil {
			return "", false, approvalView{}, fmt.Errorf("approval-bridge: malformed list response: %w", jerr)
		}
		for _, it := range resp.Items {
			if it.Status != wantStatus || it.SubjectKind != subjectKind || it.SubjectRef != encodedSubjectRef {
				continue
			}
			if freshWindowSecs > 0 && !b.withinGrant(it.DecidedAt, freshWindowSecs) {
				continue // an approved grant outside its time-box is not reusable
			}
			return it.ID, true, approvalView{status: it.Status, boundHash: decodePlanHash(it.SubjectRef), decidedAt: it.DecidedAt}, nil
		}
		if !resp.HasMore || resp.Cursor == "" {
			return "", false, approvalView{}, nil
		}
		cursor = resp.Cursor
	}
}

// do performs one in-process governed API call as the tenant's SERVICE principal — the
// full authenticate → tenant → authorize → handler → audit chain, exactly as an
// external caller would, with zero new code path in (the OUTBOUND mirror of
// hitl.go's apiDecider). A nil handler (boot race) returns status 0, which every caller
// treats as unavailable → deny.
func (b *approvalBridge) do(ctx context.Context, cred serviceCred, method, path string, body any) (int, []byte) {
	h := b.currentHandler()
	if h == nil {
		return 0, nil
	}
	var rdr io.Reader = http.NoBody
	if body != nil {
		bs, err := json.Marshal(body)
		if err != nil {
			return 0, nil
		}
		rdr = bytes.NewReader(bs)
	}
	req, err := http.NewRequestWithContext(loopbackContext(ctx), method, path, rdr)
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Authorization", "Bearer "+cred.token)
	req.Header.Set("X-Olivares-Tenant", cred.tenantStr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &captureWriter{header: http.Header{}, status: http.StatusOK}
	h.ServeHTTP(rec, req)
	return rec.status, rec.body.Bytes()
}

func (b *approvalBridge) memoGet(key string) string {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	return b.memo[key]
}

func (b *approvalBridge) memoSet(key, ref string) {
	b.memoMu.Lock()
	b.memo[key] = ref
	b.memoMu.Unlock()
}

func (b *approvalBridge) memoDel(key string) {
	b.memoMu.Lock()
	delete(b.memo, key)
	b.memoMu.Unlock()
}

// --- encoding helpers -------------------------------------------------------------

// maxSubjectRefChars mirrors the engine's subject_ref bound (governance maxNoteLen): a
// longer subject_ref is rejected (400). The bridge clamps to it so a long subject can
// never turn a governed open into an unexpected deny.
const maxSubjectRefChars = 4096

// encodeSubjectRef binds the plan hash into the subject reference. With no plan hash
// (the security posture gate) the subject is stored verbatim. The result is clamped to
// the engine's subject_ref bound, ALWAYS preserving the load-bearing plan-binding
// suffix (the raw subject is what gets truncated), so a long subject becomes a stored,
// plan-bound approval rather than a 400.
func encodeSubjectRef(subjectRef, planHash string) string {
	if planHash == "" {
		return clampStr(subjectRef, maxSubjectRefChars)
	}
	budget := maxSubjectRefChars - len(planBindingMarker) - len(planHash)
	if budget < 0 {
		budget = 0
	}
	return clampStr(subjectRef, budget) + planBindingMarker + planHash
}

// decodePlanHash recovers the plan hash an approval was bound to (empty when the
// subject carries no binding). It reads the LAST marker, so the appended hash is
// unambiguous regardless of the subject's own content.
func decodePlanHash(encoded string) string {
	if i := strings.LastIndex(encoded, planBindingMarker); i >= 0 {
		return encoded[i+len(planBindingMarker):]
	}
	return ""
}

// idemKey is the in-process idempotency identity for an actuation. The \x1f separators
// cannot appear in any of the parts (tenant id, action, kind, encoded subject), so the
// key is unambiguous.
func idemKey(tenant model.TenantID, action, subjectKind, encodedSubjectRef string) string {
	return tenant.String() + "\x1f" + action + "\x1f" + subjectKind + "\x1f" + encodedSubjectRef
}

// composeReason builds the human + machine receipt the approver sees: a short, non-
// secret prose line carrying the action, subject and plan fingerprint, plus the
// requesting service actor and any caller-supplied reason. Bounded to the engine's
// note limit. The reason field is not credential-scanned by the engine, but every part
// here is an identifier/fingerprint, never a secret.
func composeReason(action, subjectKind, subjectRef, planHash, requestedBy, extra string) string {
	var sb strings.Builder
	sb.WriteString("governed actuation approval opened by the control-plane approval bridge. action=")
	sb.WriteString(action)
	sb.WriteString(" subject=")
	sb.WriteString(subjectKind)
	sb.WriteString(":")
	sb.WriteString(subjectRef)
	if planHash != "" {
		sb.WriteString(" plan=")
		sb.WriteString(planHash)
	}
	if requestedBy != "" {
		sb.WriteString(" requested_by=")
		sb.WriteString(requestedBy)
	}
	if extra != "" {
		sb.WriteString(" | ")
		sb.WriteString(extra)
	}
	return clampStr(sb.String(), maxReasonChars)
}

// clampStr truncates s to at most maxLen bytes, backing off to a valid UTF-8 boundary.
func clampStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	for maxLen > 0 && !utf8.RuneStart(s[maxLen]) {
		maxLen--
	}
	return s[:maxLen]
}

// --- status mapping (every non-approved value is a DENY) --------------------------

// nbBreakGlass maps to each module's StatusApproved: the actuation may proceed, and
// the "breakglass:" reference the module persists (plus the engine-side use trail,
// ledger event and finding) keeps the emergency authorization permanently
// distinguishable from a quorum approval. The module seams stay untouched.
func deployGateStatus(neutral string) deploy.GateStatus {
	switch neutral {
	case nbApproved, nbBreakGlass:
		return deploy.StatusApproved
	case nbPending:
		return deploy.StatusPending
	case nbRejected, nbCanceled:
		return deploy.StatusRejected
	case nbExpired:
		return deploy.StatusExpired
	case nbNoGate:
		return deploy.StatusNoGate
	default:
		return deploy.StatusNoGate // unknown → deny
	}
}

func orchestrationGateStatus(neutral string) orchestration.GateStatus {
	switch neutral {
	case nbApproved, nbBreakGlass:
		return orchestration.StatusApproved
	case nbPending:
		return orchestration.StatusPending
	case nbRejected, nbCanceled:
		return orchestration.StatusRejected
	case nbExpired:
		return orchestration.StatusExpired
	case nbNoGate:
		return orchestration.StatusNoGate
	default:
		return orchestration.StatusNoGate
	}
}

func voiceGateStatus(neutral string) voice.GateStatus {
	switch neutral {
	case nbApproved, nbBreakGlass:
		return voice.StatusApproved
	case nbPending:
		return voice.StatusPending
	case nbRejected, nbCanceled:
		return voice.StatusRejected
	case nbExpired:
		return voice.StatusExpired
	case nbNoGate:
		return voice.StatusNoGate
	default:
		return voice.StatusNoGate
	}
}

// securityDecision maps the neutral status onto the security gate's decision. Only a
// real "approved" authorizes (Governed true); everything else denies. An unconfigured
// tenant is the one ungoverned case (Governed false) — the bridge made no call.
func securityDecision(neutral string) security.ApprovalDecision {
	switch neutral {
	case nbApproved:
		return security.ApprovalDecision{Approved: true, Governed: true, Status: nbApproved}
	case nbBreakGlass:
		// Authorized under an active emergency grant: governed (the engine recorded
		// the audited use) and approved for the actuation, with the distinct status
		// preserved in the module's record.
		return security.ApprovalDecision{Approved: true, Governed: true, Status: nbBreakGlass}
	case nbPending:
		return security.ApprovalDecision{Approved: false, Governed: true, Status: nbPending}
	case nbRejected, nbCanceled:
		return security.ApprovalDecision{Approved: false, Governed: true, Status: nbRejected}
	case nbExpired:
		return security.ApprovalDecision{Approved: false, Governed: true, Status: nbExpired}
	case nbNoGate:
		return security.ApprovalDecision{Approved: false, Governed: false, Status: nbNoGate}
	default:
		return security.ApprovalDecision{Approved: false, Governed: true, Status: neutral}
	}
}

// --- the four ApprovalGate adapters -----------------------------------------------
//
// Each module declares its OWN ApprovalGate/ApprovalRequest/GateDecision types (the
// decoupling convention); a single Go value cannot satisfy four distinct interfaces,
// so the bridge exposes one thin adapter per seam, all delegating to the shared core.

func (b *approvalBridge) deployGate() deploy.ApprovalGate { return deployApprovalAdapter{b: b} }
func (b *approvalBridge) orchestrationGate() orchestration.ApprovalGate {
	return orchestrationApprovalAdapter{b: b}
}
func (b *approvalBridge) voiceGate() voice.ApprovalGate       { return voiceApprovalAdapter{b: b} }
func (b *approvalBridge) securityGate() security.ApprovalGate { return securityApprovalAdapter{b: b} }

var (
	_ deploy.ApprovalGate        = deployApprovalAdapter{}
	_ orchestration.ApprovalGate = orchestrationApprovalAdapter{}
	_ voice.ApprovalGate         = voiceApprovalAdapter{}
	_ security.ApprovalGate      = securityApprovalAdapter{}
)

type deployApprovalAdapter struct{ b *approvalBridge }

func (a deployApprovalAdapter) Request(ctx context.Context, req deploy.ApprovalRequest) (deploy.GateDecision, error) {
	ref, st, bh, err := a.b.request(ctx, req.Tenant, req.Action, req.SubjectKind, req.SubjectRef, req.PlanHash, "", req.RequestedBy)
	if err != nil {
		return deploy.GateDecision{}, err
	}
	return deploy.GateDecision{ApprovalRef: ref, Status: deployGateStatus(st), PlanHash: bh}, nil
}

func (a deployApprovalAdapter) Status(ctx context.Context, tenant model.TenantID, approvalRef, planHash string) (deploy.GateDecision, error) {
	st, bh, err := a.b.status(ctx, tenant, approvalRef, planHash)
	if err != nil {
		return deploy.GateDecision{}, err
	}
	return deploy.GateDecision{ApprovalRef: approvalRef, Status: deployGateStatus(st), PlanHash: bh}, nil
}

type orchestrationApprovalAdapter struct{ b *approvalBridge }

func (a orchestrationApprovalAdapter) Request(ctx context.Context, req orchestration.ApprovalRequest) (orchestration.GateDecision, error) {
	ref, st, bh, err := a.b.request(ctx, req.Tenant, req.Action, req.SubjectKind, req.SubjectRef, req.PlanHash, "", req.RequestedBy)
	if err != nil {
		return orchestration.GateDecision{}, err
	}
	return orchestration.GateDecision{ApprovalRef: ref, Status: orchestrationGateStatus(st), PlanHash: bh}, nil
}

func (a orchestrationApprovalAdapter) Status(ctx context.Context, chk orchestration.ApprovalCheck) (orchestration.GateDecision, error) {
	// Item 2: verify the approval/grant authorized THIS action + subject, not
	// merely a matching plan hash — a substituted low-risk approval (or a
	// break-glass grant for a different scope) whose subject encodes the target
	// plan hash must NOT authorize this fire.
	st, bh, err := a.b.statusScoped(ctx, chk.Tenant, chk.ApprovalRef, chk.PlanHash, chk.Action, chk.SubjectKind, chk.SubjectRef)
	if err != nil {
		return orchestration.GateDecision{}, err
	}
	return orchestration.GateDecision{ApprovalRef: chk.ApprovalRef, Status: orchestrationGateStatus(st), PlanHash: bh}, nil
}

type voiceApprovalAdapter struct{ b *approvalBridge }

func (a voiceApprovalAdapter) Request(ctx context.Context, req voice.ApprovalRequest) (voice.GateDecision, error) {
	ref, st, bh, err := a.b.request(ctx, req.Tenant, req.Action, req.SubjectKind, req.SubjectRef, req.PlanHash, "", req.RequestedBy)
	if err != nil {
		return voice.GateDecision{}, err
	}
	return voice.GateDecision{ApprovalRef: ref, Status: voiceGateStatus(st), PlanHash: bh}, nil
}

func (a voiceApprovalAdapter) Status(ctx context.Context, tenant model.TenantID, approvalRef, planHash string) (voice.GateDecision, error) {
	st, bh, err := a.b.status(ctx, tenant, approvalRef, planHash)
	if err != nil {
		return voice.GateDecision{}, err
	}
	return voice.GateDecision{ApprovalRef: approvalRef, Status: voiceGateStatus(st), PlanHash: bh}, nil
}

type securityApprovalAdapter struct{ b *approvalBridge }

// Authorize maps the security module's one-shot posture gate onto the async approval:
// it finds-or-opens an approval for the (action, subject) and reports the current
// decision. Enabling enforcement only proceeds once a human has approved that exact
// posture change; until then it is denied (pending), and an unconfigured tenant or any
// error denies too (fail-closed for enabling enforcement — the safe state is detective).
//
// A human approval is a TIME-BOXED grant, not a permanent one: an approved enforcement
// posture is reusable only within the configured window of its decision (reusable() /
// withinGrant), so a re-enable after the window opens a fresh request and needs fresh
// approval. (A re-enable WITHIN the window reuses the grant — a deliberate idempotency
// window. Stricter per-toggle governance, and binding min_severity into the approval,
// would require the security module to adopt the two-phase, plan-hash-bound flow the
// deploy/orchestration/voice gates use; tracked as a follow-up.)
func (a securityApprovalAdapter) Authorize(ctx context.Context, tenant model.TenantID, req security.ApprovalRequest) (security.ApprovalDecision, error) {
	// gateOnce is the same find-or-open + reuse-approved flow this adapter always
	// used (it calls ensureApproval with reuseApproved=true), plus the
	// break-glass fallback — an active emergency grant covering the action lets
	// the posture change proceed with the use recorded engine-side.
	_, st, _, err := a.b.gateOnce(ctx, tenant, req.Action, req.SubjectKind, req.SubjectRef, "", req.Reason, req.Actor)
	if err != nil {
		return security.ApprovalDecision{}, err
	}
	return securityDecision(st), nil
}

// --- keyedMutex -------------------------------------------------------------------

// keyedMutex serializes work per key while letting unrelated keys run concurrently. It
// never reclaims a key's mutex; the key space here is the (low-frequency) set of
// distinct actuation identities, so the map stays small.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*sync.Mutex{}
	}
	mu, ok := k.m[key]
	if !ok {
		mu = &sync.Mutex{}
		k.m[key] = mu
	}
	k.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}
