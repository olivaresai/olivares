// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file is the LEGAL-HOLD plane (contract §4/§5): preservation
// orders that VETO every destruction path (the retention sweep here, knowledge's
// deletes erasure — the Sedona both-must-clear rule). Setting a hold is
// immediate and ungated (the duty to preserve); releasing one is the dangerous
// verb: CRITICAL, dual-control (≥2 distinct humans, independently re-verified
// here), no break-glass. Every transition leaves an append-only hold_event
// anchored to the ledger head AND a semantic self-audit in the same transaction
// — chain-of-custody that cannot be silently rewritten (docs/SECURITY-HARDENING.md). Minimal
// data throughout: matter/subject references, ids, counts and hashes — never
// customer content (docs/SECURITY-HARDENING.md).

// Hold scope, status and custody-event vocabularies (§1/§4).
const (
	holdScopeTenant  = "tenant"
	holdScopeClass   = "data_class"
	holdScopeSubject = "subject"

	holdStatusActive   = "active"
	holdStatusReleased = "released"

	holdEventSet              = "set"
	holdEventReleaseRequested = "release_requested"
	holdEventReleased         = "released"
)

// actionHoldRelease is the governed action a release opens an approval for. It is
// in governance's default CRITICAL set (modules/governance/risktier.go), so the
// Engine floors its threshold at two distinct human approvers.
const actionHoldRelease = "compliance.hold.release"

// holdReleaseQuorum is the dual-control floor the module re-verifies INDEPENDENTLY
// of the gate (defense in depth, the erase-gate pattern): an approved decision
// carrying fewer distinct approver principals is denied.
const holdReleaseQuorum = 2

// Finding kinds for hold transitions (core Finding row + bus FindingReport — the
// routing keys deliver).
const (
	findingHoldSet      = "compliance_hold_set"
	findingHoldReleased = "compliance_hold_released"
)

// ---- the Go hold-gate (contract §5 and the knowledge adapter consume it) --

// HoldSubject identifies what a caller wants to destroy/erase: an optional
// (kind, ref) subject pair and/or an optional §2 data-class id.
type HoldSubject struct {
	Kind      string // e.g. "user", "agent", "kb", "session" ("" if class-only)
	Ref       string
	DataClass string // §2 registry id ("" if subject-only)
}

// HoldRef identifies one covering hold — enough for a 423 body and a follow-up
// GET /holds/{id}; ids and references only, never content.
type HoldRef struct {
	ID        string `json:"id"`
	MatterRef string `json:"matter_ref"`
	ScopeKind string `json:"scope_kind"`
}

// HoldDecision is the gate's answer: whether ANY active hold covers the subject,
// and which (the Sedona both-must-clear rule: disposition proceeds only when no
// hold covers it).
type HoldDecision struct {
	Held  bool      `json:"held"`
	Holds []HoldRef `json:"holds,omitempty"`
}

// CheckHold evaluates tenant-wide + class + subject holds in ONE call (the single
// §4 matching rule shared with the sweep and the HTTP face). The CONSUMER must
// treat (err != nil) as DENY (fail closed): a hold that cannot be ruled out blocks
// the destruction.
func (m *Module) CheckHold(ctx context.Context, tenant model.TenantID, sub HoldSubject) (HoldDecision, error) {
	if m.data == nil {
		return HoldDecision{}, errors.New("compliance: no data handle; cannot evaluate holds")
	}
	var dec HoldDecision
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var verr error
		dec, verr = evalHolds(ctx, sc, sub)
		return verr
	})
	if err != nil {
		return HoldDecision{}, err
	}
	return dec, nil
}

// evalHolds runs the §4 matching rule against the tenant's ACTIVE holds inside an
// existing scope (shared by CheckHold, the HTTP face and the sweep's class check).
func evalHolds(ctx context.Context, sc store.Scope, sub HoldSubject) (HoldDecision, error) {
	repo, err := sc.Ext(legalHoldKind)
	if err != nil {
		return HoldDecision{}, err
	}
	recs, err := listAll(ctx, repo, eq(colStatus, holdStatusActive))
	if err != nil {
		return HoldDecision{}, err
	}
	var dec HoldDecision
	for _, rec := range recs {
		if !holdCovers(rec, sub) {
			continue
		}
		dec.Held = true
		dec.Holds = append(dec.Holds, holdRefOf(rec))
	}
	return dec, nil
}

// holdCovers is the ONE matching rule (§4): a tenant-scope hold covers everything;
// a data_class-scope hold covers the exact queried class; a subject-scope hold
// covers the exact (kind, ref) pair.
func holdCovers(rec model.Record, sub HoldSubject) bool {
	switch rec.String(colLHScopeKind) {
	case holdScopeTenant:
		return true
	case holdScopeClass:
		return sub.DataClass != "" && rec.String(colDataClass) == sub.DataClass
	case holdScopeSubject:
		return sub.Kind != "" && rec.String(colSubjectKind) == sub.Kind && rec.String(colSubjectRef) == sub.Ref
	default:
		return false
	}
}

func holdRefOf(rec model.Record) HoldRef {
	return HoldRef{
		ID:        rec.String(model.ColID),
		MatterRef: rec.String(colLHMatterRef),
		ScopeKind: rec.String(colLHScopeKind),
	}
}

// ---- custody ---------------------------------------------------------------

// appendHoldEvent seals one append-only custody event anchored to the CURRENT
// ledger head (seq + hash read before this operation's own self-audit lands — the
// evidence-package anchoring pattern). approvers and note are bounded; nothing in
// the row can carry customer content.
func appendHoldEvent(ctx context.Context, sc store.Scope, holdID model.ID, evt, actor, actorKind, onBehalfOf, note, approvalRef string, approvers []string) error {
	head, ok, err := sc.Audit().Head(ctx)
	if err != nil {
		return err
	}
	repo, err := sc.Ext(holdEventKind)
	if err != nil {
		return err
	}
	var seq int64
	hash := ""
	if ok {
		seq, hash = head.Seq, hex.EncodeToString(head.Hash)
	}
	_, err = repo.Create(ctx, model.Record{
		colHEHoldID:    holdID.String(),
		colHEEvent:     evt,
		colHEActor:     actor,
		colHEActorKind: actorKind,
		colHEOnBehalf:  nullableText(clamp(onBehalfOf, maxNameLen)),
		colHENote:      nullableText(clamp(note, maxNoteLen)),
		// Verbatim text, "" when ungated — never NULL: the (tenant, hold, event,
		// approval_ref) dedupe index compares plain equality (schema.go).
		colApprovalRef: approvalRef,
		colHEApprovers: encodeJSON(approvers),
		colLedgerSeq:   seq,
		colLedgerHash:  nullableText(hash),
	})
	return err
}

// errHoldEventSealed signals, inside handleReleaseHold's pending branch, that a
// CONCURRENT poll already sealed this exact release_requested custody event: the
// compliance_hold_event_uniq dedupe index fired on our Create. The losing
// transaction must ABORT — a failed INSERT poisons a Postgres transaction, so it
// can never skip ahead and commit — and the handler answers 202 exactly like the
// winner: the custody trail already holds the event.
var errHoldEventSealed = errors.New("hold custody event already sealed by a concurrent request")

// ---- DTOs --------------------------------------------------------------------

type legalHoldDTO struct {
	ID                 string `json:"id"`
	MatterRef          string `json:"matter_ref"`
	Title              string `json:"title,omitempty"`
	ScopeKind          string `json:"scope_kind"`
	DataClass          string `json:"data_class,omitempty"`
	SubjectKind        string `json:"subject_kind,omitempty"`
	SubjectRef         string `json:"subject_ref,omitempty"`
	Reason             string `json:"reason"`
	Status             string `json:"status"`
	CreatedBy          string `json:"created_by"`
	CreatedAt          string `json:"created_at"`
	ReleasedBy         string `json:"released_by,omitempty"`
	ReleasedAt         string `json:"released_at,omitempty"`
	ReleaseApprovalRef string `json:"release_approval_ref,omitempty"`
}

func recordToHoldDTO(rec model.Record) legalHoldDTO {
	return legalHoldDTO{
		ID:                 rec.String(model.ColID),
		MatterRef:          rec.String(colLHMatterRef),
		Title:              rec.String(colTitle),
		ScopeKind:          rec.String(colLHScopeKind),
		DataClass:          rec.String(colDataClass),
		SubjectKind:        rec.String(colSubjectKind),
		SubjectRef:         rec.String(colSubjectRef),
		Reason:             rec.String(colLHReason),
		Status:             rec.String(colStatus),
		CreatedBy:          rec.String(colLHCreatedBy),
		CreatedAt:          rec.String(model.ColCreatedAt),
		ReleasedBy:         rec.String(colLHReleasedBy),
		ReleasedAt:         rec.String(colLHReleasedAt),
		ReleaseApprovalRef: rec.String(colLHReleaseRef),
	}
}

type holdEventDTO struct {
	HoldID      string   `json:"hold_id"`
	Event       string   `json:"event"`
	Actor       string   `json:"actor"`
	ActorKind   string   `json:"actor_kind"`
	OnBehalfOf  string   `json:"on_behalf_of,omitempty"`
	Note        string   `json:"note,omitempty"`
	ApprovalRef string   `json:"approval_ref,omitempty"`
	Approvers   []string `json:"approvers,omitempty"`
	LedgerSeq   int64    `json:"ledger_seq"`
	LedgerHash  string   `json:"ledger_hash,omitempty"`
	OccurredAt  string   `json:"occurred_at"`
}

// ---- handlers ------------------------------------------------------------------

type createHoldRequest struct {
	MatterRef   string `json:"matter_ref"`
	Title       string `json:"title"`
	ScopeKind   string `json:"scope_kind"`
	DataClass   string `json:"data_class"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	Reason      string `json:"reason"`
	OnBehalfOf  string `json:"on_behalf_of"`
}

// handleCreateHold sets an ACTIVE hold immediately — no approval gate, because
// preservation is the safe direction and the duty to preserve admits no waiting
// (§4). It validates the scope, seals the custody "set" event + the self-audit in
// the same transaction, and emits the medium finding after commit.
func (m *Module) handleCreateHold(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req createHoldRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	matter := strings.TrimSpace(req.MatterRef)
	reason := clamp(strings.TrimSpace(req.Reason), maxNoteLen)
	if matter == "" || reason == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("matter_ref and reason are required"))
		return
	}
	scope := strings.TrimSpace(req.ScopeKind)
	class := strings.TrimSpace(req.DataClass)
	subjKind := strings.TrimSpace(req.SubjectKind)
	subjRef := strings.TrimSpace(req.SubjectRef)
	// Identity fields are REJECTED when over-length, never clamped: clamp appends
	// an ellipsis, so a truncated matter/subject reference would persist as a
	// DIFFERENT identity — an active hold the §4 exact-equality rule (holdCovers)
	// could never match, i.e. silent under-preservation. data_class needs no
	// length check: it must equal a §2 registry id exactly or the scope validation
	// below rejects it. Display-only fields (title, reason) keep clamping.
	switch {
	case tooLong(matter, maxNameLen):
		writeJSON(w, http.StatusBadRequest, errorBody("matter_ref exceeds "+itoa(maxNameLen)+" characters; identity references are rejected, never truncated"))
		return
	case tooLong(subjKind, maxNameLen):
		writeJSON(w, http.StatusBadRequest, errorBody("subject_kind exceeds "+itoa(maxNameLen)+" characters; identity references are rejected, never truncated"))
		return
	case tooLong(subjRef, maxRefLen):
		writeJSON(w, http.StatusBadRequest, errorBody("subject_ref exceeds "+itoa(maxRefLen)+" characters; identity references are rejected, never truncated"))
		return
	}
	switch scope {
	case holdScopeTenant:
		if class != "" || subjKind != "" || subjRef != "" {
			writeJSON(w, http.StatusBadRequest, errorBody("a tenant-scope hold takes no data_class or subject"))
			return
		}
	case holdScopeClass:
		if _, ok := dataClassByID[class]; !ok {
			writeJSON(w, http.StatusBadRequest, errorBody("a data_class-scope hold requires a registered data_class (see GET /retention/classes)"))
			return
		}
		if subjKind != "" || subjRef != "" {
			writeJSON(w, http.StatusBadRequest, errorBody("a data_class-scope hold takes no subject"))
			return
		}
	case holdScopeSubject:
		if subjKind == "" || subjRef == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("a subject-scope hold requires subject_kind and subject_ref"))
			return
		}
		if class != "" {
			writeJSON(w, http.StatusBadRequest, errorBody("a subject-scope hold takes no data_class"))
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("scope_kind must be tenant, data_class or subject"))
		return
	}

	var dto legalHoldDTO
	var toPublish []sdkmodel.FindingReport
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(legalHoldKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colLHMatterRef: matter,
			colTitle:       nullableText(clamp(strings.TrimSpace(req.Title), maxNameLen)),
			colLHScopeKind: scope,
			colDataClass:   nullableText(class),
			colSubjectKind: nullableText(subjKind),
			colSubjectRef:  nullableText(subjRef),
			colLHReason:    reason,
			colStatus:      holdStatusActive,
			colLHCreatedBy: mc.Principal.Actor(),
		})
		if err != nil {
			return err
		}
		dto = recordToHoldDTO(rec)
		id := model.ID(dto.ID)
		if err := appendHoldEvent(r.Context(), sc, id, holdEventSet, mc.Principal.Actor(), mc.Principal.ActorKind(),
			strings.TrimSpace(req.OnBehalfOf), reason, "", nil); err != nil {
			return err
		}
		rep, err := m.createHoldFinding(r.Context(), sc, findingHoldSet, sdkmodel.SeverityMedium, dto,
			"Legal hold set — preservation active (scope "+dto.ScopeKind+")")
		if err != nil {
			return err
		}
		toPublish = append(toPublish, rep)
		return auditEvent(r.Context(), sc, mc, "compliance.hold.set", legalHoldKind, id, map[string]any{
			"matter_ref": matter, "scope_kind": scope, "data_class": class, "subject_kind": subjKind,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.publishFindings(r.Context(), mc.Tenant, toPublish)
	writeJSON(w, http.StatusCreated, dto)
}

func (m *Module) handleListHolds(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var filters []model.Filter
	if st := strings.TrimSpace(r.URL.Query().Get("status")); st != "" {
		filters = append(filters, eq(colStatus, st))
	}
	items := []legalHoldDTO{}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(legalHoldKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo, filters...)
		for _, rec := range recs {
			items = append(items, recordToHoldDTO(rec))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[legalHoldDTO]{Items: items})
}

func (m *Module) handleGetHold(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto legalHoldDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(legalHoldKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = recordToHoldDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleListHoldEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	items := []holdEventDTO{}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		holdRepo, err := sc.Ext(legalHoldKind)
		if err != nil {
			return err
		}
		if _, err := holdRepo.Get(r.Context(), id); err != nil {
			return err
		}
		repo, err := sc.Ext(holdEventKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo, eq(colHEHoldID, id.String()))
		for _, rec := range recs {
			items = append(items, holdEventDTO{
				HoldID:      rec.String(colHEHoldID),
				Event:       rec.String(colHEEvent),
				Actor:       rec.String(colHEActor),
				ActorKind:   rec.String(colHEActorKind),
				OnBehalfOf:  rec.String(colHEOnBehalf),
				Note:        rec.String(colHENote),
				ApprovalRef: rec.String(colApprovalRef),
				Approvers:   decodeStrings(rec.String(colHEApprovers)),
				LedgerSeq:   rec.Int(colLedgerSeq),
				LedgerHash:  rec.String(colLedgerHash),
				OccurredAt:  rec.String(model.ColCreatedAt),
			})
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[holdEventDTO]{Items: items})
}

// handleCheckHoldHTTP is the hold-gate HTTP face (§5) — the same §4 rule as the Go
// CheckHold, for consumers outside the process (the eraser with a
// compliance:hold:read service token).
func (m *Module) handleCheckHoldHTTP(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := r.URL.Query()
	sub := HoldSubject{
		Kind:      strings.TrimSpace(q.Get("subject_kind")),
		Ref:       strings.TrimSpace(q.Get("subject_ref")),
		DataClass: strings.TrimSpace(q.Get("data_class")),
	}
	if (sub.Kind == "") != (sub.Ref == "") {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_kind and subject_ref must be provided together"))
		return
	}
	var dec HoldDecision
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var verr error
		dec, verr = evalHolds(r.Context(), sc, sub)
		return verr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dec)
}

// ---- release: the dangerous verb -----------------------------------------------

type releaseHoldRequest struct {
	Reason     string `json:"reason"`
	OnBehalfOf string `json:"on_behalf_of"`
}

// releaseResultDTO is the 202 envelope while the dual-control approval is pending.
type releaseResultDTO struct {
	Status      string `json:"status"`
	ApprovalRef string `json:"approval_ref,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// holdPlanHash binds the approval to THIS hold with THIS exact scope (anti-TOCTOU,
// §4): what the two humans approve is the release of this preservation order, not a
// mutable reference.
func holdPlanHash(rec model.Record) string {
	return hashHex("hold|" + rec.String(model.ColID) + "|" + rec.String(colLHMatterRef) + "|" +
		rec.String(colLHScopeKind) + "|" + rec.String(colDataClass) + "|" +
		rec.String(colSubjectKind) + "|" + rec.String(colSubjectRef))
}

// handleReleaseHold lifts a preservation order under CRITICAL dual-control: the
// gate floors the approval at two distinct humans (risktier.go), the adapter runs
// over gateOnceNoBreakGlass (no emergency path), and this handler INDEPENDENTLY
// re-verifies ≥2 distinct approver principals before mutating anything (defense in
// depth — a gate that says approved without quorum evidence is denied). The pending
// path answers 202 and seals a release_requested custody event once per approval.
func (m *Module) handleReleaseHold(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req releaseHoldRequest
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}

	// Load the hold OUTSIDE the gate call (the gate reaches another module — never
	// inside a store transaction).
	var hold model.Record
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(legalHoldKind)
		if err != nil {
			return err
		}
		hold, err = repo.Get(r.Context(), id)
		return err
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if hold.String(colStatus) != holdStatusActive {
		writeJSON(w, http.StatusConflict, errorBody("hold is already released"))
		return
	}

	planHash := holdPlanHash(hold)
	reason := clamp(strings.TrimSpace(req.Reason), maxNoteLen)
	if reason == "" {
		reason = "release legal hold (matter " + hold.String(colLHMatterRef) + ")"
	}
	dec, err := m.gate.Authorize(r.Context(), mc.Tenant, GateRequest{
		Action: actionHoldRelease, SubjectKind: "legal_hold", SubjectRef: id.String(),
		PlanHash: planHash, Reason: reason, RequestedBy: mc.Principal.Actor(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not consult the approval gate"))
		return
	}

	switch dec.Status {
	case GateStatusPending:
		// Record the request in the custody trail ONCE per approval (a poll while
		// pending must not multiply custody events), then answer 202. The findOne
		// guard catches the sequential re-poll; the compliance_hold_event_uniq
		// index (schema.go) is the ground truth under concurrency — two racing
		// polls can both pass the read, but only one Create commits.
		err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			evRepo, err := sc.Ext(holdEventKind)
			if err != nil {
				return err
			}
			_, exists, err := findOne(r.Context(), evRepo,
				eq(colHEHoldID, id.String()), eq(colHEEvent, holdEventReleaseRequested), eq(colApprovalRef, dec.ApprovalRef))
			if err != nil || exists {
				return err
			}
			if err := appendHoldEvent(r.Context(), sc, id, holdEventReleaseRequested, mc.Principal.Actor(),
				mc.Principal.ActorKind(), strings.TrimSpace(req.OnBehalfOf), reason, dec.ApprovalRef, nil); err != nil {
				// At THIS call site ErrConflict can only be the dedupe index (the
				// only unique constraint a hold-event insert can hit; the store
				// maps a unique violation to ErrConflict and nothing else here
				// writes): a concurrent poll sealed the same release_requested
				// between our read and our insert. Abort the losing tx and answer
				// 202 like the winner — never a blanket error swallow.
				if errors.Is(err, store.ErrConflict) {
					return errHoldEventSealed
				}
				return err
			}
			return auditEvent(r.Context(), sc, mc, "compliance.hold.release.request", legalHoldKind, id, map[string]any{
				"matter_ref": hold.String(colLHMatterRef), "approval_ref": dec.ApprovalRef,
			})
		})
		if err != nil && !errors.Is(err, errHoldEventSealed) {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, releaseResultDTO{Status: "pending_approval", ApprovalRef: dec.ApprovalRef,
			Detail: "release awaiting dual-control approval (2 distinct humans)"})
		return

	case GateStatusApproved:
		// Anti-TOCTOU: the approval must be bound to THIS hold's exact plan.
		if dec.PlanHash != planHash {
			writeJSON(w, http.StatusForbidden, errorBody("approval is not bound to this hold (plan hash mismatch)"))
			return
		}
		// Independent quorum re-verification (defense in depth). It counts PEOPLE, never
		// the credentials: one human holding a session and a token renders two actor
		// strings, and counting those would lift a preservation obligation on one
		// human's say-so.
		if dec.Quorum() < holdReleaseQuorum {
			writeJSON(w, http.StatusForbidden, errorBody("approval lacks dual-control quorum evidence (need 2 distinct human approvers)"))
			return
		}

	case GateStatusExpired:
		writeJSON(w, http.StatusConflict, releaseResultDTO{Status: dec.Status, ApprovalRef: dec.ApprovalRef,
			Detail: "the release approval expired; request again"})
		return
	case GateStatusNoGate:
		writeJSON(w, http.StatusServiceUnavailable, releaseResultDTO{Status: dec.Status,
			Detail: "no approval gate is wired; releasing a legal hold is denied (deny-closed)"})
		return
	default: // rejected / unknown vocabulary
		writeJSON(w, http.StatusForbidden, releaseResultDTO{Status: dec.Status, ApprovalRef: dec.ApprovalRef,
			Detail: "release denied"})
		return
	}

	var dto legalHoldDTO
	var toPublish []sdkmodel.FindingReport
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(legalHoldKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id) // re-read inside the tx (race with a concurrent release)
		if err != nil {
			return err
		}
		if rec.String(colStatus) != holdStatusActive {
			return store.ErrConflict
		}
		now := m.clock.Now()
		rec[colStatus] = holdStatusReleased
		rec[colLHReleasedBy] = mc.Principal.Actor()
		rec[colLHReleasedAt] = now.String()
		rec[colLHReleaseRef] = dec.ApprovalRef
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		dto = recordToHoldDTO(updated)
		if err := appendHoldEvent(r.Context(), sc, id, holdEventReleased, mc.Principal.Actor(),
			mc.Principal.ActorKind(), strings.TrimSpace(req.OnBehalfOf), reason, dec.ApprovalRef, dec.Approvers); err != nil {
			return err
		}
		rep, err := m.createHoldFinding(r.Context(), sc, findingHoldReleased, sdkmodel.SeverityHigh, dto,
			"Legal hold released — preservation lifted under dual-control (scope "+dto.ScopeKind+")")
		if err != nil {
			return err
		}
		toPublish = append(toPublish, rep)
		return auditEvent(r.Context(), sc, mc, "compliance.hold.release", legalHoldKind, id, map[string]any{
			// approvers is the count of distinct HUMANS (the quorum), not of credentials;
			// unattributed_approvals reports approvals with no person behind them.
			"matter_ref": dto.MatterRef, "scope_kind": dto.ScopeKind,
			"approval_ref": dec.ApprovalRef, "approvers": dec.Quorum(),
			"unattributed_approvals": dec.UnattributedApprovals,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.publishFindings(r.Context(), mc.Tenant, toPublish)
	writeJSON(w, http.StatusOK, dto)
}

// createHoldFinding records the core Finding row in the CALLER'S transaction and
// returns the matching bus FindingReport for the caller to publish AFTER commit
// (the residency.go pattern: Never deliver a signal for an uncommitted
// finding). The detail (matter/scope/subject refs) rides DetailHash only — never
// raw in a title or meta (docs/SECURITY-HARDENING.md).
func (m *Module) createHoldFinding(ctx context.Context, sc store.Scope, kind string, sev sdkmodel.Severity, dto legalHoldDTO, title string) (sdkmodel.FindingReport, error) {
	title = clamp(title, maxNameLen)
	detail := dto.ID + "|" + dto.MatterRef + "|" + dto.ScopeKind + "|" + dto.DataClass + "|" + dto.SubjectKind + "|" + dto.SubjectRef
	now := m.clock.Now()
	if _, err := sc.Findings().Create(ctx, model.Finding{
		Kind: kind, Severity: model.Severity(sev), Status: model.FindingOpen,
		Source: Name, SubjectKind: "legal_hold", SubjectID: model.ID(dto.ID),
		Title: title, DetailHash: hashBytes(detail), OccurredAt: now,
	}); err != nil {
		return sdkmodel.FindingReport{}, err
	}
	return sdkmodel.FindingReport{
		Kind: kind, Severity: sev,
		SubjectKind: "legal_hold", SubjectRef: clamp(dto.ID, maxRefLen),
		Title: title, DetailHash: hashHex(detail), OccurredAt: now.Time(),
	}, nil
}

// publishFindings emits bus FindingReports AFTER the transaction that created them
// committed (best-effort; a publish failure is logged, not fatal).
func (m *Module) publishFindings(ctx context.Context, tenant model.TenantID, reports []sdkmodel.FindingReport) {
	if m.host == nil {
		return
	}
	for _, rep := range reports {
		if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, rep)); err != nil {
			m.debugf("compliance: publish finding failed", "kind", rep.Kind, "err", err)
		}
	}
}

// distinctNonEmpty counts the distinct, non-empty principals in a list — the quorum
// the module re-verifies independently of the gate.
func distinctNonEmpty(principals []string) int {
	seen := map[string]struct{}{}
	for _, p := range principals {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		seen[p] = struct{}{}
	}
	return len(seen)
}
