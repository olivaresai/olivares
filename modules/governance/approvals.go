// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Approval lifecycle states. pending is the only non-terminal state; the other
// four are terminal (no transition leaves them).
const (
	statusPending  = "pending"
	statusApproved = "approved"
	statusRejected = "rejected"
	statusCanceled = "canceled"
	statusExpired  = "expired"
)

// Decision values recorded in the immutable trail.
const (
	decisionApprove = "approve"
	decisionReject  = "reject"
)

// maxNoteLen bounds an approval reason / decision note (operator prose, served
// back on read; bounded, never secret-scanned, never echoed into audit Meta).
const maxNoteLen = 4096

// maxDecisionRetries bounds the optimistic-concurrency retry of a decision: a
// concurrent threshold crossing makes the loser's version-checked update conflict;
// it reloads and re-evaluates. A handful of approvers never needs more than this.
const maxDecisionRetries = 6

// createApprovalRequest opens a human-in-the-loop request. A matching approval
// POLICY (if any) is authoritative for the threshold/timeouts — a requester cannot
// lower their own approval bar; only when no policy matches do the request's own
// values (or the default of one approval) apply.
type createApprovalRequest struct {
	SubjectKind       string `json:"subject_kind"`
	SubjectRef        string `json:"subject_ref"`
	Action            string `json:"action"`
	Reason            string `json:"reason,omitempty"`
	RequiredApprovals int    `json:"required_approvals,omitempty"`
	ExpiresInSeconds  int64  `json:"expires_in_seconds,omitempty"`
	EscalateInSeconds int64  `json:"escalate_in_seconds,omitempty"`
}

// approvalDTO is the request view. Status is the EFFECTIVE status (expiry derived
// at read), so every read surface agrees regardless of whether a sweep has yet
// materialized the expiry into storage. RiskTier is likewise DERIVED at read from
// the live classification (policy ∨ built-in default, risktier.go) — never a
// stored snapshot a later policy change could invalidate.
type approvalDTO struct {
	ID                string `json:"id"`
	SubjectKind       string `json:"subject_kind,omitempty"`
	SubjectRef        string `json:"subject_ref,omitempty"`
	Action            string `json:"action,omitempty"`
	RequestedBy       string `json:"requested_by,omitempty"`
	Status            string `json:"status"`
	RiskTier          string `json:"risk_tier"`
	RequiredApprovals int64  `json:"required_approvals"`
	ApproveCount      int64  `json:"approve_count"`
	RejectCount       int64  `json:"reject_count"`
	Reason            string `json:"reason,omitempty"`
	PolicyRef         string `json:"policy_ref,omitempty"`
	ExpiresAt         string `json:"expires_at,omitempty"`
	EscalateAt        string `json:"escalate_at,omitempty"`
	Escalated         bool   `json:"escalated"`
	DecidedAt         string `json:"decided_at,omitempty"`
}

func toApprovalDTO(rec model.Record, now model.Timestamp, tier ActionRiskTier) approvalDTO {
	return approvalDTO{
		ID: rec.String(model.ColID), SubjectKind: rec.String(colSubjectKind), SubjectRef: rec.String(colSubjectRef),
		Action: rec.String(colAction), RequestedBy: rec.String(colRequestedBy), Status: effectiveStatus(rec, now),
		RiskTier:          string(tier),
		RequiredApprovals: rec.Int(colRequiredApproval), ApproveCount: rec.Int(colApproveCount), RejectCount: rec.Int(colRejectCount),
		Reason: rec.String(colReason), PolicyRef: rec.String(colPolicyRef),
		ExpiresAt: rec.String(colExpiresAt), EscalateAt: rec.String(colEscalateAt),
		Escalated: rec.String(colEscalatedAt) != "", DecidedAt: rec.String(colDecidedAt),
	}
}

// effectiveStatus is the authoritative status: the stored status unless the
// request is still pending past its expiry, in which case it is "expired". Every
// security decision (decide/cancel) re-derives this, so a request shown expired at
// read can never receive a binding decision even before a sweep persists it.
func effectiveStatus(rec model.Record, now model.Timestamp) string {
	stored := rec.String(colStatus)
	if stored != statusPending {
		return stored
	}
	if exp, ok := tsValue(rec, colExpiresAt); ok && !now.Before(exp) {
		return statusExpired
	}
	return statusPending
}

// tsValue parses a stored timestamp column, returning ok=false when null/empty/malformed.
func tsValue(rec model.Record, col string) (model.Timestamp, bool) {
	s := rec.String(col)
	if s == "" {
		return model.Timestamp{}, false
	}
	ts, err := model.ParseTimestamp(s)
	if err != nil {
		return model.Timestamp{}, false
	}
	return ts, true
}

// consumedFresh reports whether an approval's single-use consume is still inside the short
// transport-retry idempotency window (F-02). A missing/unparseable consumed_at is NOT
// fresh (fail-closed: no evidence of a recent consume ⇒ the grant is spent, deny-closed).
func consumedFresh(rec model.Record, now model.Timestamp) bool {
	consumedAt, ok := tsValue(rec, colConsumedAt)
	if !ok {
		return false
	}
	return now.Before(model.NewTimestamp(consumedAt.Time().Add(consumeIdempotencyWindow)))
}

// handleCreateApproval opens an approval request. Write-tier (any editor may
// request); self-audited.
func (m *Module) handleCreateApproval(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in createApprovalRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.SubjectKind = strings.TrimSpace(in.SubjectKind)
	in.SubjectRef = strings.TrimSpace(in.SubjectRef)
	in.Action = strings.TrimSpace(in.Action)
	if in.Action == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("action is required"))
		return
	}
	// action and subject_kind ride the immutable audit Meta (below), so they must be
	// bounded short identifiers and carry no credential — the same minimal-data
	// guard policy specs get (docs/SECURITY-HARDENING.md); subject_ref is bounded and scanned too.
	if len(in.Action) > maxMatchLen || len(in.SubjectKind) > maxMatchLen {
		writeJSON(w, http.StatusBadRequest, errorBody("action and subject_kind must be short identifiers"))
		return
	}
	if len(in.SubjectRef) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_ref too long"))
		return
	}
	if containsInlineCredential(in.Action) || containsInlineCredential(in.SubjectKind) || containsInlineCredential(in.SubjectRef) {
		writeJSON(w, http.StatusBadRequest, errorBody("action and subject fields must not contain a credential"))
		return
	}
	if len(in.Reason) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("reason too long"))
		return
	}
	if in.RequiredApprovals < 0 || in.RequiredApprovals > maxApprovalCount ||
		in.ExpiresInSeconds < 0 || in.ExpiresInSeconds > maxSeconds ||
		in.EscalateInSeconds < 0 || in.EscalateInSeconds > maxSeconds {
		writeJSON(w, http.StatusBadRequest, errorBody("approval window or count out of range"))
		return
	}
	now := m.clock.Now()
	var out approvalDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		var ierr error
		out, ierr = m.openApprovalRecord(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(), mc.Principal.UserID.String(), in, 0, now)
		return ierr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Post-commit: announce the pending request on the bus so eventing
	// subscribers authorized for governance:approval:read can react.
	m.emitApprovalRequested(r.Context(), mc.Tenant, out)
	writeJSON(w, http.StatusCreated, out)
}

// openApprovalRecord opens a pending approval row plus its create self-audit
// inside the caller's transaction, applying the same policy match +
// CRITICAL floor as the HTTP create. It is the in-module entry the kill-switch
// re-enable and the guardian HITL loop use — the engine derives the
// threshold; a caller can never lower its own bar. minRequired is the CALLER'S
// own non-negotiable floor on top (0 = none): the kill-switch re-enable opens
// at two distinct humans even when an operator approval policy downgrades the
// security.killswitch.* tier (the tier — and with it the AAL3 decision bar —
// IS operator-tunable per the two-human quorum of a re-enable is not).
// The caller emits approval.requested AFTER its transaction commits.
func (m *Module) openApprovalRecord(ctx context.Context, sc store.Scope, actor, actorKind, userID string, in createApprovalRequest, minRequired int64, now model.Timestamp) (approvalDTO, error) {
	// A matching approval policy is authoritative for threshold + windows, and
	// for the action's explicit risk tier.
	required := int64(in.RequiredApprovals)
	expiresIn, escalateIn := in.ExpiresInSeconds, in.EscalateInSeconds
	policyRef := ""
	spec, matched := approvalSpec{}, false
	if pid, s, ok, err := matchApprovalPolicy(ctx, sc, in.Action, in.SubjectKind); err != nil {
		return approvalDTO{}, err
	} else if ok {
		policyRef, spec, matched = pid, s, true
		required = int64(spec.RequiredApprovals)
		expiresIn, escalateIn = spec.ExpiresInSeconds, spec.EscalateInSeconds
	}
	if required < 1 {
		required = 1
	}
	// Dual-authorization floor (NIST AC-3(2)): a CRITICAL action starts at
	// two distinct human approvers — neither the requester nor a matching policy
	// can open it lower (deny-closed; handleDecide re-derives the same floor).
	tier := resolveRiskTier(spec, matched, in.Action)
	required = floorRequiredApprovals(required, tier)
	if required < minRequired {
		required = minRequired
	}
	rec := model.Record{
		colSubjectKind: in.SubjectKind, colSubjectRef: in.SubjectRef, colAction: in.Action,
		colRequestedBy: actor, colRequestedByUser: userID,
		colStatus: statusPending, colRequiredApproval: required, colApproveCount: int64(0), colRejectCount: int64(0),
		colReason: in.Reason, colPolicyRef: policyRef,
	}
	if expiresIn > 0 {
		rec[colExpiresAt] = model.NewTimestamp(now.Time().Add(time.Duration(expiresIn) * time.Second)).String()
	}
	if escalateIn > 0 {
		rec[colEscalateAt] = model.NewTimestamp(now.Time().Add(time.Duration(escalateIn) * time.Second)).String()
	}
	repo, err := sc.Ext(approvalKind)
	if err != nil {
		return approvalDTO{}, err
	}
	created, err := repo.Create(ctx, rec)
	if err != nil {
		return approvalDTO{}, err
	}
	out := toApprovalDTO(created, now, tier)
	// risk_tier rides the immutable audit Meta: the ledger snapshots the
	// classification under which this request was opened.
	_, err = sc.Audit().Append(ctx, model.AuditDraft{
		Actor: actor, ActorKind: actorKind,
		Action: "governance.approval.create", TargetKind: approvalKind, TargetID: model.ID(created.String(model.ColID)),
		Meta: map[string]any{
			"action": in.Action, "subject_kind": in.SubjectKind, "required_approvals": required, "risk_tier": string(tier),
		},
	})
	return out, err
}

// loadApprovalPolicies returns every enabled approval policy in store order
// (id), the deterministic match order matchApprovalSpec applies.
func loadApprovalPolicies(ctx context.Context, sc store.Scope) ([]model.Policy, error) {
	var out []model.Policy
	q := model.Query{Filters: []model.Filter{eq("kind", policyKindApproval), eq("enabled", true)}, Limit: listCap}
	for {
		pols, page, err := sc.Policies().List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, pols...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// matchApprovalSpec returns the first policy of pols that governs the
// (action, subjectKind): an empty match field is a wildcard, a set field must
// equal. pols must be in store order (id) for determinism.
func matchApprovalSpec(pols []model.Policy, action, subjectKind string) (string, approvalSpec, bool) {
	for _, p := range pols {
		spec, perr := parseApprovalSpec(p.Spec)
		if perr != nil {
			continue // a corrupt approval policy is skipped here (it cannot grant); abac corruption is the fail-closed path
		}
		if (spec.Match.Action == "" || spec.Match.Action == action) &&
			(spec.Match.SubjectKind == "" || spec.Match.SubjectKind == subjectKind) {
			return p.ID.String(), spec, true
		}
	}
	return "", approvalSpec{}, false
}

// matchApprovalPolicy returns the first enabled approval policy that governs the
// (action, subjectKind) — loadApprovalPolicies + matchApprovalSpec in one call.
func matchApprovalPolicy(ctx context.Context, sc store.Scope, action, subjectKind string) (string, approvalSpec, bool, error) {
	pols, err := loadApprovalPolicies(ctx, sc)
	if err != nil {
		return "", approvalSpec{}, false, err
	}
	id, spec, ok := matchApprovalSpec(pols, action, subjectKind)
	return id, spec, ok, nil
}

// liveRiskTier re-derives an approval's CURRENT risk tier from the live policy
// set + the built-in default. Every security decision and every read derives the
// tier this way — a stored snapshot could be invalidated by a later policy
// change, and the live derivation is the deny-closed one.
func liveRiskTier(pols []model.Policy, rec model.Record) ActionRiskTier {
	_, spec, matched := matchApprovalSpec(pols, rec.String(colAction), rec.String(colSubjectKind))
	return resolveRiskTier(spec, matched, rec.String(colAction))
}

// handleListApprovals lists requests, optionally filtered by status/action. The
// stored status is filtered; a request that is stored-pending but past expiry is
// reported with effective status "expired" in its DTO (the sweep materializes it).
func (m *Module) handleListApprovals(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colStatus, v))
	}
	if v := r.URL.Query().Get("action"); v != "" {
		q.Filters = append(q.Filters, eq(colAction, v))
	}
	now := m.clock.Now()
	out := listResponse[approvalDTO]{Items: []approvalDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		// One policy load per list call; the live tier is derived per item.
		pols, err := loadApprovalPolicies(r.Context(), sc)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toApprovalDTO(rec, now, liveRiskTier(pols, rec)))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetApproval returns one request with its effective status.
func (m *Module) handleGetApproval(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	now := m.clock.Now()
	var (
		out   approvalDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		pols, err := loadApprovalPolicies(r.Context(), sc)
		if err != nil {
			return err
		}
		found, out = true, toApprovalDTO(rec, now, liveRiskTier(pols, rec))
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// decisionDTO is one immutable entry of the action→human decision trail. It carries
// BOTH identities of the decider, for the reason core/auth.PersonRef states as a type
// and breakglass.go already stores as a pair (activated_by + activated_by_user): they
// answer different questions and a two-person control needs both. Decider is WHICH
// CREDENTIAL decided — the provenance the trail must show — and DeciderUser is WHO
// decided, the stable person, the ONLY sound basis for counting humans.
//
// Serving only Decider was not a missing detail, it was an interface that made the
// correct check unwritable downstream: Actor() renders "user:<UserID>" for a session and
// "token:<CredID>" for a token, so one human holding both produces two strings, and every
// consumer that deduplicates them reports one person as a two-person quorum. The row has
// carried decider_user since the entity existed (schema.go, NOT NULL); this DTO simply
// stopped dropping it.
//
// What DeciderUser discloses, stated plainly rather than waved away: for a session
// principal it is already embedded in Decider, so nothing is added. For a USER-BOUND API
// token it is not — Decider reads "token:<CredID>" — so this field newly links that
// credential to the person behind it. That case is reachable: a user-bound token is
// refused only on a CRITICAL decision (AAL3, handleDecide below), so it can decide a
// lower-tier approval, which is exactly the "two credentials, one human" shape this trail
// has to make visible. The disclosure is deliberate and bounded: the endpoint is
// governance:approval:read, the same authority that can already list tokens and read the
// ledger, and without it no consumer can count humans at all.
//
// DeciderUser is empty only for a row no sanctioned writer can produce — handleDecide
// and claudeagents.go handleToolConfirmation both refuse a zero UserID. A consumer that
// counts HUMANS must treat that emptiness as "no person" (core/auth.PersonRef.Stable)
// and must never fall back to Decider, which would re-admit the defect.
type decisionDTO struct {
	Decision    string `json:"decision"`
	Decider     string `json:"decider"`
	DeciderUser string `json:"decider_user,omitempty"`
	DecidedAt   string `json:"decided_at,omitempty"`
	Note        string `json:"note,omitempty"`
}

func toDecisionDTO(rec model.Record) decisionDTO {
	return decisionDTO{
		Decision: rec.String(colDecision), Decider: rec.String(colDecider),
		DeciderUser: rec.String(colDeciderUser),
		DecidedAt:   rec.String(colDecidedAt), Note: rec.String(colNote),
	}
}

// handleListDecisions returns the immutable decision trail for a request — the
// action→human traceability view.
func (m *Module) handleListDecisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	out := listResponse[decisionDTO]{Items: []decisionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colApprovalID, id.String()))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDecisionDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDecide records one human decision. Admin-tier and self-audited. It is
// race-safe on Postgres: every decision re-counts within its transaction and
// commits under a version-checked update of the approval row, so a concurrent
// threshold crossing resolves to exactly one winner (the loser retries). SoD and
// the duplicate-decider guard key on the stable Principal.UserID (not the actor
// string a single human could vary across credentials), backed by the unique index.
func (m *Module) handleDecide(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in struct {
		Decision string `json:"decision"`
		Note     string `json:"note,omitempty"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	decision := strings.ToLower(strings.TrimSpace(in.Decision))
	if decision != decisionApprove && decision != decisionReject {
		writeJSON(w, http.StatusBadRequest, errorBody("decision must be one of approve, reject"))
		return
	}
	if len(in.Note) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("note too long"))
		return
	}
	if mc.Principal.UserID.IsZero() {
		writeJSON(w, http.StatusForbidden, errorBody("a stable user identity is required to decide; a system token cannot approve"))
		return
	}
	deciderUser := mc.Principal.UserID.String()

	for attempt := 0; attempt < maxDecisionRetries; attempt++ {
		var (
			out           approvalDTO
			clientErr     string
			clientErrCode string
			clientCode    int
		)
		now := m.clock.Now()
		err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			appRepo, err := sc.Ext(approvalKind)
			if err != nil {
				return err
			}
			decRepo, err := sc.Ext(decisionKind)
			if err != nil {
				return err
			}
			rec, err := appRepo.Get(r.Context(), id)
			if err != nil {
				return err // ErrNotFound -> 404
			}
			if eff := effectiveStatus(rec, now); eff != statusPending {
				clientErr, clientCode = "approval is "+eff+"; no decision can be recorded", http.StatusConflict
				return nil
			}
			// SEPARATION OF DUTY, decided on the PERSON. AcceptWhenUndetermined here and
			// only here: a request opened by automation has no person behind it, and
			// refusing every decision on it would break the ordinary machine-opens /
			// human-approves flow. That is safe ONLY because the two-person guarantee is
			// carried by the QUORUM below, which counts people and refuses to count an
			// unattributable party at all — the two halves have opposite policies for the
			// same undetermined case, and each says why.
			//
			// The guard is `!ok` ALONE, and that is load-bearing. It used to read
			// `!ok && verdict == PersonSame`, which silently narrowed the refusal to one
			// named verdict: when PersonSameCredential was split out of PersonSame, a
			// request whose person was never recorded but whose actor string is the
			// decider's own walked straight past it and self-approved. Under
			// AcceptWhenUndetermined, `!ok` is true for exactly the verdicts that mean
			// "knowably one party", so asking TwoDistinctPeople whether to refuse — rather
			// than re-deciding from the verdict — is the whole rule. The verdict is read
			// only to phrase the message.
			requester := auth.PersonRef{User: rec.String(colRequestedByUser), Actor: rec.String(colRequestedBy)}
			deciderRef := auth.PersonRef{User: deciderUser, Actor: mc.Principal.Actor()}
			if ok, verdict := auth.TwoDistinctPeople(requester, deciderRef, auth.AcceptWhenUndetermined); !ok {
				clientErr = "separation of duty: the requester cannot decide their own request"
				if verdict == auth.PersonSameCredential {
					clientErr = "separation of duty: this request was opened with the same credential you are deciding with, and no person stands behind it"
				}
				clientCode = http.StatusForbidden
				return nil
			}
			if _, dup, err := findOne(r.Context(), decRepo, eq(colApprovalID, id.String()), eq(colDeciderUser, deciderUser)); err != nil {
				return err
			} else if dup {
				clientErr, clientCode = "this user has already decided this request", http.StatusConflict
				return nil
			}
			// re-derive the LIVE risk tier at the decision point, in the
			// same transaction (a pre-check outside it would race a policy
			// change). Derived BEFORE the decision insert so an under-assured
			// decision is refused without leaving a decision row behind.
			pols, err := loadApprovalPolicies(r.Context(), sc)
			if err != nil {
				return err
			}
			tier := liveRiskTier(pols, rec)
			// a CRITICAL decision is a privileged human act — it demands a
			// session whose hardware step-up (WebAuthn/PIV) is fresh. Tokens
			// carry no human assurance (AAL 0) and are equally refused. The
			// floor lives HERE in the engine, not only in ABAC policy: the
			// policy evaluator fails open on a load error, the engine does not.
			if tier == RiskTierCritical && mc.Principal.AAL < auth.AAL3 {
				clientErr = "a critical decision requires a hardware-verified (AAL3) session; complete the WebAuthn/PIV step-up and retry"
				clientErrCode, clientCode = "step_up_required", http.StatusForbidden
				return nil
			}
			// Insert the decision (append-only; the unique index backstops a same-user race).
			if _, err := decRepo.Create(r.Context(), model.Record{
				colApprovalID: id.String(), colDecision: decision, colDecider: mc.Principal.Actor(),
				colDeciderUser: deciderUser, colNote: in.Note, colDecidedAt: now.String(),
			}); err != nil {
				return err // unique-race -> ErrConflict -> retry, where the pre-check returns 409
			}
			// Re-count within the same transaction (sees the just-inserted row).
			decs, err := listAll(r.Context(), decRepo, eq(colApprovalID, id.String()))
			if err != nil {
				return err
			}
			// COUNT PEOPLE, NOT ROWS. This is the counter every other quorum in the
			// estate is downstream of, and it crossed the threshold on row count. The
			// unique index is (tenant, approval, decider_user), which admits ONE row
			// whose decider_user is EMPTY — a credential with no person behind it — and
			// that row counted as an approver. One human plus one system token reached
			// "two distinct approvers" and opened the approval every downstream counter
			// then trusts.
			//
			// RefuseWhenUndetermined is implicit in the shape: DistinctPeople never folds
			// an unattributable party into the total. It is reported separately so the
			// trail can say what was seen rather than quietly dropping it.
			approvers := make([]auth.PersonRef, 0, len(decs))
			reject := 0
			for _, d := range decs {
				switch d.String(colDecision) {
				case decisionApprove:
					approvers = append(approvers, auth.PersonRef{User: d.String(colDeciderUser), Actor: d.String(colDecider)})
				case decisionReject:
					// A rejection is the SAFE direction — it denies. An unattributable
					// party may stop an action; it may not authorize one.
					reject++
				}
			}
			approve, unattributable := auth.DistinctPeople(approvers)
			required := rec.Int(colRequiredApproval)
			if required < 1 {
				required = 1
			}
			// re-apply the CRITICAL dual-authorization floor at the
			// threshold crossing — a request created before this control (or
			// before a policy made its action critical) can never cross to
			// approved with a single human (deny-closed).
			required = floorRequiredApprovals(required, tier)
			newStatus := statusPending
			switch {
			case reject > 0:
				newStatus = statusRejected
			case int64(approve) >= required:
				newStatus = statusApproved
			}
			rec[colApproveCount], rec[colRejectCount], rec[colStatus] = int64(approve), int64(reject), newStatus
			// Materialize the floored threshold so the stored row (and every DTO
			// derived from it) agrees with the bar actually being enforced.
			rec[colRequiredApproval] = required
			if newStatus != statusPending {
				rec[colDecidedAt] = now.String()
			}
			rec, err = appRepo.Update(r.Context(), rec) // version-checked: serializes the threshold crossing
			if err != nil {
				return err // ErrConflict -> retry
			}
			out = toApprovalDTO(rec, now, tier)
			meta := map[string]any{"decision": decision, "status": newStatus, "risk_tier": string(tier), "distinct_approvers": approve}
			if unattributable > 0 {
				// Recorded, not dropped: an approval carrying decisions no person stands
				// behind is a fact an auditor must be able to see, even though those
				// decisions did not count toward the threshold.
				meta["unattributable_decisions"] = unattributable
			}
			return auditEvent(r.Context(), sc, mc, "governance.approval.decision", approvalKind, id, meta)
		})
		if clientErr != "" {
			if clientErrCode != "" {
				writeJSON(w, clientCode, errorBodyCode(clientErrCode, clientErr))
				return
			}
			writeJSON(w, clientCode, errorBody(clientErr))
			return
		}
		if err != nil {
			if isConflict(err) {
				continue // version/unique race: reload and re-evaluate
			}
			writeStoreError(w, err)
			return
		}
		if out.Status != statusPending {
			m.emitApprovalResolved(r.Context(), mc.Tenant, out)
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusConflict, errorBody("decision conflicted repeatedly; please retry"))
}

// handleCancel cancels a pending request. Write-tier; allowed only to the original
// requester (stable UserID) or a tenant admin/owner. Self-audited.
func (m *Module) handleCancel(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	now := m.clock.Now()
	var (
		out        approvalDTO
		clientErr  string
		clientCode int
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if !canCancel(mc, rec) {
			clientErr, clientCode = "only the requester or a tenant admin may cancel this request", http.StatusForbidden
			return nil
		}
		if eff := effectiveStatus(rec, now); eff != statusPending {
			clientErr, clientCode = "approval is "+eff+"; it cannot be canceled", http.StatusConflict
			return nil
		}
		rec[colStatus] = statusCanceled
		rec[colDecidedAt] = now.String()
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		pols, err := loadApprovalPolicies(r.Context(), sc)
		if err != nil {
			return err
		}
		out = toApprovalDTO(rec, now, liveRiskTier(pols, rec))
		return auditEvent(r.Context(), sc, mc, "governance.approval.cancel", approvalKind, id, nil)
	})
	if clientErr != "" {
		writeJSON(w, clientCode, errorBody(clientErr))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.emitApprovalResolved(r.Context(), mc.Tenant, out)
	writeJSON(w, http.StatusOK, out)
}

// findingApprovalReplayDenied is emitted when an already-consumed approval is
// re-presented by a DIFFERENT caller — a would-replay of a single-use human
// decision (F-02).
const findingApprovalReplayDenied = "governance_approval_replay_denied"

// consumeIdempotencyWindow bounds result-idempotency to a SHORT transport-retry horizon
// (F-02). After the first consume, a re-consume by the SAME caller re-obtains its
// grant ONLY within this window — a genuine transport retry (a network hiccup on the hook
// round-trip) happens in seconds. Past it the single-use approval is SPENT and any consume
// (even by the same caller) is a would-replay DENY, so one human approval can never
// re-authorize a DEFERRED re-execution hours later under its ~24h validity — exactly the
// re-execution F-02 must prevent. It is deliberately short; the outer time-box (expires_at)
// and the strict single-use of a transport-id-less call are the other two legs of the
// single-use guarantee.
const consumeIdempotencyWindow = 2 * time.Minute

// consumeApprovalRequest is the bridge's single-use CONSUME of an approved
// request (F-02). ConsumerID is the stable id of the exact caller claiming
// the grant — the Claude Code tool_use_id for a governed tool-call — so the engine
// can tell a legitimate transport retry of the SAME call (idempotent re-grant)
// apart from a NEW call reusing an already-spent approval (would-replay).
type consumeApprovalRequest struct {
	ConsumerID    string `json:"consumer_id"`
	PolicyVersion string `json:"policy_version,omitempty"`
}

// handleConsumeApproval is the single-use spend of an APPROVED request (F-02). A human approval is a ONE-SHOT authorization, not a 24h reusable pass:
// the FIRST caller to consume it (identified by ConsumerID = the tool_use_id) wins
// and is recorded on the row; a re-consume by that SAME caller is idempotent (a
// legitimate transport retry re-obtains its grant — it does NOT re-authorize); a
// consume by ANY OTHER caller is a would-replay DENIED, recorded to the signed
// ledger and surfaced as a finding. This separates result-idempotency (safe) from
// permission-reuse (prohibited), the exact confusion F-02 exploited. Write-tier
// (the bridge's editor-scoped service token). It is deny-closed on every edge: a
// non-approved (pending/expired/rejected/canceled) request cannot be consumed, and
// an atomic version-checked update serializes concurrent first-consumers so exactly
// one wins.
func (m *Module) handleConsumeApproval(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in consumeApprovalRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.ConsumerID = strings.TrimSpace(in.ConsumerID)
	if in.ConsumerID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("consumer_id is required (single-use consume cannot be keyed without it)"))
		return
	}
	// consumer_id rides the immutable audit Meta and the row, so it must be a short,
	// non-credential identifier — the same minimal-data guard the other fields get.
	if len(in.ConsumerID) > maxMatchLen || len(in.PolicyVersion) > maxMatchLen {
		writeJSON(w, http.StatusBadRequest, errorBody("consumer_id and policy_version must be short identifiers"))
		return
	}
	if containsInlineCredential(in.ConsumerID) || containsInlineCredential(in.PolicyVersion) {
		writeJSON(w, http.StatusBadRequest, errorBody("consumer_id must not contain a credential"))
		return
	}

	type consumeResponse struct {
		Granted    bool   `json:"granted"`
		Replay     bool   `json:"replay,omitempty"`
		Status     string `json:"status"`
		ConsumedBy string `json:"consumed_by,omitempty"`
	}

	for attempt := 0; attempt < maxDecisionRetries; attempt++ {
		now := m.clock.Now()
		var (
			resp        consumeResponse
			emitReplay  bool
			replayScope string
		)
		err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(approvalKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), id)
			if err != nil {
				return err // ErrNotFound -> 404
			}
			eff := effectiveStatus(rec, now)
			if eff != statusApproved {
				// Only an effective-approved request is a spendable grant. Pending/
				// expired/rejected/canceled all deny-closed here (never a silent grant).
				resp = consumeResponse{Granted: false, Status: eff}
				return nil
			}
			// F5 freshness re-anchor: effectiveStatus stops applying the time-box once a
			// request is stored-approved, so a stale-but-approved row no sweep has touched could
			// still be spent long after its window. Re-anchor here — an approved grant past its
			// expiry ceiling is NOT spendable (a direct consume must honor the same outer
			// time-box the pending state did). Cheap: one compare on the already-loaded row. The
			// bridge already refuses to reuse a stale grant (approvalbridge.go withinGrant); this
			// is the defense-in-depth backstop for a direct consume that skips it.
			if exp, ok := tsValue(rec, colExpiresAt); ok && !now.Before(exp) {
				resp = consumeResponse{Granted: false, Status: statusExpired}
				return nil
			}
			prior := rec.String(colConsumedBy)
			switch {
			case prior == "":
				// FIRST consume: bind the grant to this caller under a version-checked
				// update that serializes concurrent first-consumers to exactly one winner.
				rec[colConsumedBy] = in.ConsumerID
				rec[colConsumedAt] = now.String()
				if _, uerr := repo.Update(r.Context(), rec); uerr != nil {
					return uerr // ErrConflict -> retry (a concurrent consumer won)
				}
				resp = consumeResponse{Granted: true, Status: eff, ConsumedBy: in.ConsumerID}
				return auditEvent(r.Context(), sc, mc, "governance.approval.consume", approvalKind, id, map[string]any{
					"consumer_id": in.ConsumerID, "policy_version": in.PolicyVersion,
				})
			case prior == in.ConsumerID:
				// Result-idempotency, bounded to a SHORT transport-retry window (F-02):
				// the SAME caller re-obtains its grant only while the retry horizon is open — a
				// genuine transport retry re-reads its grant without re-authorizing (no state
				// change, no new ledger event). Past the window the single-use approval is SPENT:
				// a re-consume (even by the same caller) is a would-replay DENY, so one human
				// approval can never re-authorize a DEFERRED re-execution hours later.
				if consumedFresh(rec, now) {
					resp = consumeResponse{Granted: true, Status: eff, ConsumedBy: prior}
					return nil
				}
				resp = consumeResponse{Granted: false, Replay: true, Status: eff, ConsumedBy: prior}
				emitReplay, replayScope = true, rec.String(colAction)
				return auditEvent(r.Context(), sc, mc, "governance.approval.replay_denied", approvalKind, id, map[string]any{
					"consumer_id": in.ConsumerID, "consumed_by": prior, "policy_version": in.PolicyVersion,
				})
			default:
				// Permission-reuse: a DIFFERENT caller is trying to spend an approval
				// already consumed — a would-replay. Deny-closed + signed-ledger evidence.
				resp = consumeResponse{Granted: false, Replay: true, Status: eff, ConsumedBy: prior}
				emitReplay, replayScope = true, rec.String(colAction)
				return auditEvent(r.Context(), sc, mc, "governance.approval.replay_denied", approvalKind, id, map[string]any{
					"consumer_id": in.ConsumerID, "consumed_by": prior, "policy_version": in.PolicyVersion,
				})
			}
		})
		if err != nil {
			if isConflict(err) {
				continue // version race on the first-consume: reload and re-evaluate
			}
			writeStoreError(w, err)
			return
		}
		if emitReplay {
			m.emitApprovalReplayFinding(r.Context(), mc.Tenant, id.String(), replayScope)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	writeJSON(w, http.StatusConflict, errorBody("approval consume conflicted repeatedly; please retry"))
}

// emitApprovalReplayFinding surfaces a would-replay denial (F-02) on the
// notification rail: an already-consumed single-use human approval was re-presented
// by another caller. Minimal data — the approval id rides SubjectRef, the action
// scope is hashed into DetailHash with the kind (docs/SECURITY-HARDENING.md), mirroring
// emitApprovalFinding.
func (m *Module) emitApprovalReplayFinding(ctx context.Context, tenant model.TenantID, approvalID, action string) {
	if m.host == nil {
		return
	}
	sum := sha256.Sum256([]byte(findingApprovalReplayDenied + "|" + approvalID + "|" + action))
	finding := sdkmodel.FindingReport{
		Kind:        findingApprovalReplayDenied,
		Severity:    sdkmodel.SeverityHigh,
		SubjectKind: "approval",
		SubjectRef:  approvalID,
		Title:       "Would-replay denied — a spent single-use human approval was re-presented",
		DetailHash:  hex.EncodeToString(sum[:]),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, finding)); err != nil {
		m.debugf("governance: emit approval replay finding failed", "err", err)
	}
}

// canCancel reports whether the principal may cancel rec: the original requester
// (stable user id) or a tenant admin/owner.
func canCancel(mc api.ModuleContext, rec model.Record) bool {
	if u := mc.Principal.UserID.String(); u != "" && u == rec.String(colRequestedByUser) {
		return true
	}
	if role, ok := mc.Principal.RoleIn(mc.Tenant); ok && auth.RoleRank(role) >= auth.RoleRank(auth.RoleAdmin) {
		return true
	}
	return false
}
