// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file is the RIGHT-TO-ERASURE workflow: a per-
// subject, per-tenant GDPR/CCPA RTBF over the control plane's OWN stores, built to
// be legally defensible without ever breaking the append-only hash-chained ledger:
//
//   request (mint subject key + tokenize)
//   → execute: hold-gate (Held OR error ⇒ no erasure, BEFORE the
//     approval gate; the Sedona both-must-clear rule)
//   → approval gate "compliance.subject.erase" (the .erase suffix puts it in
//     governance's default CRITICAL set ⇒ two distinct human approvers, no
//     break-glass; PlanHash anti-TOCTOU; quorum re-verified HERE, never trusted)
//   → physical erasure of the mutable stores (the in-code target registry,
//     bounded batches, holds re-checked inside every destructive transaction)
//   → account + provider legs through their honest seams
//   → crypto-shred (HARD-delete the subject key row — mapping and DEK die together;
//     every pii token in append-only/WORM media is now permanently unintelligible)
//   → verify (residual scan + LIVE ledger chain verification — /v1/audit/verify
//     semantics must hold AFTER the erasure, the bar)
//   → seal the append-only, ledger-anchored erasure RECEIPT with the documented
//     erasure↔retention reconciliation and the §7 provider-floor disclosure.
//
// The ledger is never rewritten: events referencing the subject do so through
// pseudonymous actors ("user:<id>") and pii tokens; erasure destroys every mapping
// (mutable rows, the account email, the key ring) that could re-identify them —
// crypto-erasure (NIST SP 800-88) + Art. 17(3)(b)/(e) retention for the audit trail
// itself, documented per receipt. See docs/RIGHT-TO-ERASURE.md.

// actionSubjectErase is the governed action an erasure execute opens an approval
// for. The ".erase" suffix is in governance's data-deletion CRITICAL suffix set
// (modules/governance/risktier.go), so the engine floors its threshold at two
// distinct human approvers; the cmd adapter runs over gateOnceNoBreakGlass — no
// emergency path can skip the second human.
const actionSubjectErase = "compliance.subject.erase"

// erasureQuorum is the dual-control floor re-verified INDEPENDENTLY of the gate
// (the erase-gate pattern): an approved decision with fewer distinct approver
// principals is denied.
const erasureQuorum = 2

// Erasure request status vocabulary. blocked_hold, denied, failed and executing
// (a crash mid-run leaves it; a fresh execute resumes) are re-executable;
// completed and completed_with_gaps are TERMINAL — setErasureStatus refuses to
// overwrite them, so a straggling concurrent attempt can never downgrade a sealed
// receipt's status.
const (
	erasureStatusReceived  = "received"
	erasureStatusPending   = "pending_approval"
	erasureStatusExecuting = "executing"
	erasureStatusBlocked   = "blocked_hold"
	erasureStatusDenied    = "denied"
	erasureStatusFailed    = "failed"
	erasureStatusCompleted = "completed"
	erasureStatusGaps      = "completed_with_gaps"
)

// errErasureTerminal reports an attempt to transition a request whose receipt is
// already sealed — the loser of a duplicate execute, answered as a conflict.
var errErasureTerminal = errors.New("compliance: erasure already completed; its status is immutable")

// Custody event vocabulary (erasure_event rows).
const (
	erasureEventReceived    = "received"
	erasureEventHoldBlock   = "hold_blocked"
	erasureEventCoordinator = "coordinator_blocked"
	erasureEventApprovalRq  = "approval_requested"
	erasureEventExecuted    = "executed"
	erasureEventAccount     = "account"
	erasureEventProvider    = "provider"
	erasureEventFiles       = "files_store" // Files-store RTBF disclosure (read-only)
	erasureEventShredded    = "key_shredded"
	erasureEventSealed      = "sealed"
	erasureEventFailed      = "failed"
)

// Finding kind for a completed erasure (the routing key deliver).
const findingErasureCompleted = "compliance_erasure_completed"

// retainedRecord documents ONE class of records the erasure deliberately retains,
// with its legal basis — the per-receipt erasure↔retention reconciliation.
type retainedRecord struct {
	Records string `json:"records"`
	Basis   string `json:"basis"`
}

// retainedReconciliation is the fixed, documented reconciliation every receipt
// carries (docs/RIGHT-TO-ERASURE.md §4): what stays, and why that is lawful.
var retainedReconciliation = []retainedRecord{
	{
		Records: "audit ledger events (pseudonymous actors/targets, hash-chained, WORM-archived)",
		Basis:   "GDPR Art. 17(3)(b) legal obligation + 17(3)(e) defense of legal claims; rendered anonymous by this erasure (Recital 26): the account/key-ring mappings that could link them to the person are destroyed",
	},
	{
		Records: "append-only compliance evidence (hold custody, disposition certificates, this receipt)",
		Basis:   "Art. 17(3)(b)/(e); minimal-data by construction (ids, counts, hashes); the subject is referenced only by a crypto-shredded token",
	},
	{
		Records: "knowledge lineage / pii_scan / dlp_event evidence rows",
		Basis:   "Art. 17(3)(b); append-only ids/hashes/counts, never content",
	},
	{
		Records: "governance append-only trails (NHI lifecycle events, approval decisions, break-glass uses) referencing historic owner/sponsor/actor ids",
		Basis:   "Art. 17(3)(b)/(e) accountability evidence; the live overlay rows are scrubbed by this erasure — the historic references no longer resolve to a person once the roster anchor and account mappings are destroyed",
	},
	{
		Records: "provider-retained model I/O until the Covered-Models floor expires",
		Basis:   "processor-forced retention (≥30 days, no ZDR); disclosed via provider_floor_days — deleting our copy does not delete the provider's",
	},
}

// ---- DTOs --------------------------------------------------------------------

type erasureRequestDTO struct {
	ID           string   `json:"id"`
	SubjectKind  string   `json:"subject_kind"`
	SubjectToken string   `json:"subject_token"`
	Subject      string   `json:"subject,omitempty"` // detokenized while the key lives; "[ERASED]" after
	Aliases      []string `json:"aliases,omitempty"` // detokenized while the key lives
	DataClasses  []string `json:"data_classes"`
	CaseRef      string   `json:"case_ref"`
	Reason       string   `json:"reason"`
	RequestedBy  string   `json:"requested_by"`
	Status       string   `json:"status"`
	ApprovalRef  string   `json:"approval_ref,omitempty"`
	CreatedAt    string   `json:"created_at"`
}

type erasureEventDTO struct {
	ErasureID   string   `json:"erasure_id"`
	Event       string   `json:"event"`
	Actor       string   `json:"actor"`
	ActorKind   string   `json:"actor_kind"`
	Note        string   `json:"note,omitempty"`
	ApprovalRef string   `json:"approval_ref,omitempty"`
	Approvers   []string `json:"approvers,omitempty"`
	LedgerSeq   int64    `json:"ledger_seq"`
	LedgerHash  string   `json:"ledger_hash,omitempty"`
	OccurredAt  string   `json:"occurred_at"`
}

type erasureReceiptDTO struct {
	ErasureID    string           `json:"erasure_id"`
	SubjectKind  string           `json:"subject_kind"`
	SubjectToken string           `json:"subject_token"`
	Targets      []targetOutcome  `json:"targets"`
	Account      string           `json:"account_outcome"`
	Provider     string           `json:"provider_outcome"`
	KeyShredded  bool             `json:"key_shredded"`
	VerifyOK     bool             `json:"verify_ok"`
	VerifyN      int64            `json:"verify_checked"`
	VerifyWhy    string           `json:"verify_reason,omitempty"`
	Retained     []retainedRecord `json:"retained"`
	CaseRef      string           `json:"case_ref"`
	ApprovalRef  string           `json:"approval_ref,omitempty"`
	LedgerSeq    int64            `json:"ledger_seq"`
	LedgerHash   string           `json:"ledger_hash,omitempty"`
	ManifestHash string           `json:"manifest_hash"`
	OccurredAt   string           `json:"occurred_at"`
	providerFloor
}

// ---- custody -------------------------------------------------------------------

// appendErasureEvent seals one append-only custody event anchored to the CURRENT
// ledger head (the appendHoldEvent pattern). note is bounded prose with counts and
// target labels — never a subject identifier.
func appendErasureEvent(ctx context.Context, sc store.Scope, erasureID model.ID, evt, actor, actorKind, note, approvalRef string, approvers []string) error {
	head, ok, err := sc.Audit().Head(ctx)
	if err != nil {
		return err
	}
	repo, err := sc.Ext(erasureEventKind)
	if err != nil {
		return err
	}
	var seq int64
	hash := ""
	if ok {
		seq, hash = head.Seq, hex.EncodeToString(head.Hash)
	}
	_, err = repo.Create(ctx, model.Record{
		colEEErasureID: erasureID.String(),
		colEEEvent:     evt,
		colEEActor:     actor,
		colEEActorKind: actorKind,
		colEENote:      nullableText(clamp(note, maxNoteLen)),
		colApprovalRef: approvalRef,
		colEEApprovers: encodeJSON(approvers),
		colLedgerSeq:   seq,
		colLedgerHash:  nullableText(hash),
	})
	return err
}

// errErasureEventSealed mirrors errHoldEventSealed: a concurrent poll already
// sealed this exact custody event (the unique index fired); the losing transaction
// aborts and the handler answers like the winner.
var errErasureEventSealed = errors.New("erasure custody event already sealed by a concurrent request")

// ---- handlers: request lifecycle -------------------------------------------------

type createErasureRequest struct {
	SubjectKind string   `json:"subject_kind"`
	SubjectRef  string   `json:"subject_ref"`
	Aliases     []string `json:"aliases"`
	DataClasses []string `json:"data_classes"`
	CaseRef     string   `json:"case_ref"`
	Reason      string   `json:"reason"`
}

// handleCreateErasure registers a DSR: it mints (or reuses) the subject's key-ring
// row, tokenizes the subject reference, derives the affected §2 classes and seals
// the "received" custody event + self-audit in one transaction. Identity fields
// follow the rule: rejected when over-length, never clamped (a truncated ref
// is a DIFFERENT subject — silent under-erasure).
func (m *Module) handleCreateErasure(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req createErasureRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	kind := strings.TrimSpace(req.SubjectKind)
	ref := strings.TrimSpace(req.SubjectRef)
	caseRef := strings.TrimSpace(req.CaseRef)
	reason := clamp(strings.TrimSpace(req.Reason), maxNoteLen)
	if kind == "" || ref == "" || caseRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_kind, subject_ref and case_ref are required"))
		return
	}
	if !validErasureSubjectKind(kind) {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_kind must be one of "+strings.Join(erasureSubjectKinds, ", ")))
		return
	}
	switch {
	case tooLong(ref, maxRefLen):
		writeJSON(w, http.StatusBadRequest, errorBody("subject_ref exceeds "+itoa(maxRefLen)+" characters; identity references are rejected, never truncated"))
		return
	case tooLong(caseRef, maxNameLen):
		writeJSON(w, http.StatusBadRequest, errorBody("case_ref exceeds "+itoa(maxNameLen)+" characters; identity references are rejected, never truncated"))
		return
	}
	var aliases []string
	for _, a := range req.Aliases {
		a = strings.TrimSpace(a)
		if a == "" || a == ref {
			continue
		}
		if tooLong(a, maxRefLen) {
			writeJSON(w, http.StatusBadRequest, errorBody("an alias exceeds "+itoa(maxRefLen)+" characters; identity references are rejected, never truncated"))
			return
		}
		aliases = append(aliases, a)
	}
	classes := affectedClasses(kind)
	if len(req.DataClasses) > 0 {
		// An explicit class list NARROWS the hold surface the operator attests; every
		// entry must be a registered §2 id.
		classes = nil
		for _, c := range req.DataClasses {
			c = strings.TrimSpace(c)
			if _, ok := dataClassByID[c]; !ok {
				writeJSON(w, http.StatusBadRequest, errorBody("unknown data_class "+c+" (see GET /retention/classes)"))
				return
			}
			classes = append(classes, c)
		}
		classes = dedupeStrings(classes)
	}

	var dto erasureRequestDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		key, found, err := findSubjectKey(r.Context(), sc, kind, ref)
		if err != nil {
			return err
		}
		if !found {
			key, err = mintSubjectKey(r.Context(), sc, kind, ref, aliases, mc.Principal.Actor())
			if err != nil {
				return err
			}
		}
		// One ACTIONABLE request per subject: a live (non-terminal) request for the
		// same key is reused — re-POSTing a DSR must not fork the workflow.
		reqRepo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			return err
		}
		existing, err := listAll(r.Context(), reqRepo, eq(colERKeyID, key.ID.String()))
		if err != nil {
			return err
		}
		for _, rec := range existing {
			switch rec.String(colERStatus) {
			case erasureStatusCompleted, erasureStatusGaps:
				continue
			default:
				return store.ErrConflict
			}
		}
		token, err := sealSubjectToken(sc.Tenant(), key, ref)
		if err != nil {
			return err
		}
		rec, err := reqRepo.Create(r.Context(), model.Record{
			colERSubjectKind:   kind,
			colERToken:         token,
			colERSubjectLookup: subjectLookupDigest(mc.Tenant, kind, ref),
			colERKeyID:         key.ID.String(),
			colERClasses:       encodeJSON(classes),
			colCaseRef:         caseRef,
			colERReason:        nullableText(reason),
			colERRequestedBy:   mc.Principal.Actor(),
			colERStatus:        erasureStatusReceived,
		})
		if err != nil {
			return err
		}
		dto = m.requestDTO(r.Context(), sc, rec)
		id := model.ID(dto.ID)
		if err := appendErasureEvent(r.Context(), sc, id, erasureEventReceived, mc.Principal.Actor(), mc.Principal.ActorKind(),
			"DSR registered (case "+caseRef+", "+itoa(int64(len(classes)))+" data class(es))", "", nil); err != nil {
			return err
		}
		// Liveness re-read: on Postgres read-committed a CONCURRENT execute could
		// have shredded a reused key between our read and this point — a request
		// sealed under a dead key would be born unworkable. The per-statement
		// snapshot sees the committed delete; aborting here is the safe outcome.
		if _, err := getSubjectKey(r.Context(), sc, key.ID); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "compliance.erasure.request", erasureRequestKind, id, map[string]any{
			"case_ref": caseRef, "subject_kind": kind, "data_classes": strings.Join(classes, ","),
		})
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeJSON(w, http.StatusConflict, errorBody("an actionable erasure request for this subject already exists"))
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

func validErasureSubjectKind(kind string) bool {
	for _, k := range erasureSubjectKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// requestDTO renders a request row, detokenizing the subject while its key lives
// (the operator working the DSR needs to see WHO it is about); after the shred the
// token renders as the permanent "[ERASED]" stand-in.
func (m *Module) requestDTO(ctx context.Context, sc store.Scope, rec model.Record) erasureRequestDTO {
	dto := erasureRequestDTO{
		ID:           rec.String(model.ColID),
		SubjectKind:  rec.String(colERSubjectKind),
		SubjectToken: rec.String(colERToken),
		DataClasses:  decodeStrings(rec.String(colERClasses)),
		CaseRef:      rec.String(colCaseRef),
		Reason:       rec.String(colERReason),
		RequestedBy:  rec.String(colERRequestedBy),
		Status:       rec.String(colERStatus),
		CreatedAt:    rec.String(model.ColCreatedAt),
	}
	key, err := getSubjectKey(ctx, sc, model.ID(rec.String(colERKeyID)))
	switch {
	case err == nil:
		dto.Subject = key.Ref
		dto.Aliases = key.Aliases
	case errors.Is(err, ErrKeyShredded):
		dto.Subject = erasedTokenDisplay
	}
	return dto
}

func (m *Module) handleListErasure(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var filters []model.Filter
	if st := strings.TrimSpace(r.URL.Query().Get("status")); st != "" {
		filters = append(filters, eq(colERStatus, st))
	}
	items := []erasureRequestDTO{}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo, filters...)
		for _, rec := range recs {
			items = append(items, m.requestDTO(r.Context(), sc, rec))
		}
		return lerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[erasureRequestDTO]{Items: items})
}

func (m *Module) handleGetErasure(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto erasureRequestDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		dto = m.requestDTO(r.Context(), sc, rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleListErasureEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	items := []erasureEventDTO{}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		reqRepo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			return err
		}
		if _, err := reqRepo.Get(r.Context(), id); err != nil {
			return err
		}
		repo, err := sc.Ext(erasureEventKind)
		if err != nil {
			return err
		}
		recs, lerr := listAll(r.Context(), repo, eq(colEEErasureID, id.String()))
		for _, rec := range recs {
			items = append(items, erasureEventDTO{
				ErasureID:   rec.String(colEEErasureID),
				Event:       rec.String(colEEEvent),
				Actor:       rec.String(colEEActor),
				ActorKind:   rec.String(colEEActorKind),
				Note:        rec.String(colEENote),
				ApprovalRef: rec.String(colApprovalRef),
				Approvers:   decodeStrings(rec.String(colEEApprovers)),
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
	writeJSON(w, http.StatusOK, listResponse[erasureEventDTO]{Items: items})
}

func (m *Module) handleGetErasureReceipt(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto erasureReceiptDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(erasureReceiptKind)
		if err != nil {
			return err
		}
		rec, ok, err := findOne(r.Context(), repo, eq(colRCErasureID, id.String()))
		if err != nil || !ok {
			return err
		}
		found = true
		dto = receiptDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("no receipt sealed for this erasure yet"))
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func receiptDTO(rec model.Record) erasureReceiptDTO {
	dto := erasureReceiptDTO{
		ErasureID:    rec.String(colRCErasureID),
		SubjectKind:  rec.String(colRCSubject),
		SubjectToken: rec.String(colRCToken),
		Account:      rec.String(colRCAccount),
		Provider:     rec.String(colRCProvider),
		KeyShredded:  rec.Bool(colRCShredded),
		VerifyOK:     rec.Bool(colRCVerifyOK),
		VerifyN:      rec.Int(colRCVerifyN),
		VerifyWhy:    rec.String(colRCVerifyWhy),
		CaseRef:      rec.String(colCaseRef),
		ApprovalRef:  rec.String(colApprovalRef),
		LedgerSeq:    rec.Int(colLedgerSeq),
		LedgerHash:   rec.String(colLedgerHash),
		ManifestHash: rec.String(colManifestHash),
		OccurredAt:   rec.String(model.ColCreatedAt),
		providerFloor: providerFloor{
			ProviderFloorDays:   int(rec.Int(colRCFloorDays)),
			ProviderFloorKnown:  rec.Bool(colRCFloorKnown),
			ProviderFloorSource: rec.String(colRCFloorSrc),
		},
	}
	_ = jsonUnmarshal(rec.String(colRCTargets), &dto.Targets)
	_ = jsonUnmarshal(rec.String(colRCRetained), &dto.Retained)
	return dto
}

// ---- execute: the governed destructive verb --------------------------------------

type executeErasureRequest struct {
	Reason         string   `json:"reason"`
	ProviderUserID []string `json:"provider_user_ids"`
}

// executeResultDTO is the 202 envelope while the dual-control approval is pending.
type executeResultDTO struct {
	Status      string `json:"status"`
	ApprovalRef string `json:"approval_ref,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

type dataSubjectEraseRequest struct {
	SubjectKind     string   `json:"subject_kind"`
	Aliases         []string `json:"aliases"`
	DataClasses     []string `json:"data_classes"`
	CaseRef         string   `json:"case_ref"`
	Reason          string   `json:"reason"`
	ProviderUserIDs []string `json:"provider_user_ids"`
}

type dataSubjectErasureStatusDTO struct {
	SubjectID    string             `json:"subject_id"`
	SubjectKind  string             `json:"subject_kind"`
	State        string             `json:"state"`
	Request      erasureRequestDTO  `json:"request,omitempty"`
	Receipt      *erasureReceiptDTO `json:"receipt,omitempty"`
	KeyShredded  bool               `json:"key_shredded"`
	Verified     bool               `json:"verified"`
	VerifyReason string             `json:"verify_reason,omitempty"`
	ApprovalRef  string             `json:"approval_ref,omitempty"`
	Disclaimer   string             `json:"disclaimer"`
}

// erasurePlanHash binds the approval to THIS request erasing THIS subject with THIS
// scope (anti-TOCTOU). The subject rides as a keyed digest (HMAC under the
// subject's own DEK) — deterministic across approval polls while the key lives,
// non-recomputable for any candidate identity after the shred, and never raw PII in
// the governance approval row.
func erasurePlanHash(rec model.Record, key subjectKey, classes []string) string {
	sorted := append([]string(nil), classes...)
	sort.Strings(sorted)
	return hashHex("erasure|v1|" + rec.String(model.ColID) + "|" + rec.String(colERSubjectKind) + "|" +
		subjectPlanDigest(key) + "|" + strings.Join(sorted, ",") + "|" + rec.String(colCaseRef))
}

// handleExecuteErasure runs the governed erasure for one request. Gate order is the
// contract §6.1: the legal-hold gate FIRST (Held=true OR error ⇒ no erasure),
// THEN the CRITICAL dual-control approval — two independent deny-closed gates,
// neither subsumes the other. Both gates run OUTSIDE store transactions; every
// destructive batch re-checks holds INSIDE its own transaction.
func (m *Module) handleExecuteErasure(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req executeErasureRequest
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}

	// Load the request + its subject key OUTSIDE any gate call.
	var reqRec model.Record
	var key subjectKey
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			return err
		}
		reqRec, err = repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		key, err = getSubjectKey(r.Context(), sc, model.ID(reqRec.String(colERKeyID)))
		return err
	}); err != nil {
		if errors.Is(err, ErrKeyShredded) {
			writeJSON(w, http.StatusConflict, errorBody("this subject's key is already shredded; the erasure cannot re-run"))
			return
		}
		writeStoreError(w, err)
		return
	}
	switch reqRec.String(colERStatus) {
	case erasureStatusCompleted, erasureStatusGaps:
		writeJSON(w, http.StatusConflict, errorBody("erasure already completed (see the receipt)"))
		return
	}
	classes := decodeStrings(reqRec.String(colERClasses))

	// ---- gate 1: the legal-hold gate (BEFORE the approval gate, §6.1).
	// Tenant-wide and subject holds in one call per identifier, plus one call per
	// affected class; ANY error is a DENY we cannot prove either way ⇒ 503, never a
	// 423 that would assert a hold we did not see (the knowledge ports.go wording).
	var covering []HoldRef
	for _, ref := range key.identifiers() {
		dec, err := m.CheckHold(r.Context(), mc.Tenant, HoldSubject{Kind: key.Kind, Ref: ref})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("legal-hold check unavailable; erasure denied (fail closed)"))
			return
		}
		covering = append(covering, dec.Holds...)
	}
	for _, class := range classes {
		dec, err := m.CheckHold(r.Context(), mc.Tenant, HoldSubject{DataClass: class})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("legal-hold check unavailable; erasure denied (fail closed)"))
			return
		}
		covering = append(covering, dec.Holds...)
	}
	if len(covering) > 0 {
		covering = dedupeHoldRefs(covering)
		m.markErasure(r.Context(), mc, id, erasureStatusBlocked, erasureEventHoldBlock,
			"blocked by "+itoa(int64(len(covering)))+" active legal hold(s)", "", nil)
		// The EXACT 423 body of the contract §2.4 — adopted verbatim.
		writeJSON(w, http.StatusLocked, map[string]any{"error": map[string]any{
			"code":    "legal_hold",
			"message": "blocked by an active legal hold",
			"holds":   covering,
		}})
		return
	}
	var coordWarnings []string
	for _, ref := range key.identifiers() {
		ready, wired, err := m.validateCryptoShredReadiness(r.Context(), mc.Tenant, key.Kind, ref)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("enterprise RTBF coordinator unavailable; erasure denied (fail closed)"))
			return
		}
		if !wired {
			break
		}
		coordWarnings = append(coordWarnings, ready.Warnings...)
		if !ready.Ready {
			m.markErasure(r.Context(), mc, id, erasureStatusBlocked, erasureEventCoordinator,
				"blocked by enterprise RTBF coordinator", "", nil)
			writeJSON(w, http.StatusLocked, map[string]any{"error": map[string]any{
				"code":           "rtbf_coordinator",
				"message":        "blocked by enterprise RTBF coordinator",
				"blockers":       ready.Blockers,
				"warnings":       ready.Warnings,
				"policy_applied": ready.PolicyApplied,
			}})
			return
		}
	}

	// ---- gate 2: the CRITICAL dual-control approval (no break-glass).
	plan := erasurePlanHash(reqRec, key, classes)
	reason := clamp(strings.TrimSpace(req.Reason), maxNoteLen)
	if reason == "" {
		reason = "irreversible RTBF erasure (case " + reqRec.String(colCaseRef) + ")"
	}
	dec, err := m.gate.Authorize(r.Context(), mc.Tenant, GateRequest{
		Action: actionSubjectErase, SubjectKind: "erasure_request", SubjectRef: id.String(),
		PlanHash: plan, Reason: reason, RequestedBy: mc.Principal.Actor(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not consult the approval gate"))
		return
	}
	switch dec.Status {
	case GateStatusPending:
		// Custody ONCE per approval (read-then-insert + the unique-index backstop,
		// the handleReleaseHold pattern), then 202.
		err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			evRepo, err := sc.Ext(erasureEventKind)
			if err != nil {
				return err
			}
			_, exists, err := findOne(r.Context(), evRepo,
				eq(colEEErasureID, id.String()), eq(colEEEvent, erasureEventApprovalRq), eq(colApprovalRef, dec.ApprovalRef))
			if err != nil || exists {
				return err
			}
			if err := appendErasureEvent(r.Context(), sc, id, erasureEventApprovalRq, mc.Principal.Actor(),
				mc.Principal.ActorKind(), reason, dec.ApprovalRef, nil); err != nil {
				if errors.Is(err, store.ErrConflict) {
					return errErasureEventSealed
				}
				return err
			}
			if err := setErasureStatus(r.Context(), sc, id, erasureStatusPending, plan); err != nil {
				return err
			}
			return auditEvent(r.Context(), sc, mc, "compliance.erasure.execute.request", erasureRequestKind, id, map[string]any{
				"case_ref": reqRec.String(colCaseRef), "approval_ref": dec.ApprovalRef,
			})
		})
		if err != nil && !errors.Is(err, errErasureEventSealed) {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, executeResultDTO{Status: "pending_approval", ApprovalRef: dec.ApprovalRef,
			Detail: "erasure awaiting dual-control approval (2 distinct humans; no break-glass)"})
		return

	case GateStatusApproved:
		// Anti-TOCTOU: the approval must be bound to THIS exact plan.
		if dec.PlanHash != plan {
			writeJSON(w, http.StatusForbidden, errorBody("approval is not bound to this erasure plan (plan hash mismatch)"))
			return
		}
		// Independent quorum re-verification (defense in depth; a break-glass grant
		// carries no approvers and can never pass). It counts PEOPLE, never the
		// credentials: one human holding a session and a token renders two actor
		// strings, and counting those would clear a two-human bar on one human's say-so
		// for an IRREVERSIBLE deletion.
		if dec.Quorum() < erasureQuorum {
			writeJSON(w, http.StatusForbidden, errorBody("approval lacks dual-control quorum evidence (need 2 distinct human approvers)"))
			return
		}

	case GateStatusExpired:
		writeJSON(w, http.StatusConflict, executeResultDTO{Status: dec.Status, ApprovalRef: dec.ApprovalRef,
			Detail: "the erasure approval expired; execute again to open a fresh one"})
		return
	case GateStatusNoGate:
		writeJSON(w, http.StatusServiceUnavailable, executeResultDTO{Status: dec.Status,
			Detail: "no approval gate is wired; an irreversible erasure is denied (deny-closed)"})
		return
	default: // rejected / unknown vocabulary
		m.markErasure(r.Context(), mc, id, erasureStatusDenied, erasureEventFailed,
			"erasure denied by governance ("+dec.Status+")", dec.ApprovalRef, dec.Approvers)
		writeJSON(w, http.StatusForbidden, executeResultDTO{Status: dec.Status, ApprovalRef: dec.ApprovalRef,
			Detail: "erasure denied"})
		return
	}

	// ---- both gates clear: execute.
	outcome := m.executeErasure(r.Context(), mc, id, reqRec, key, classes, dec, req.ProviderUserID, coordWarnings)
	writeJSON(w, outcome.code, outcome.body)
}

func (m *Module) handleDataSubjectErase(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	subjectID := strings.TrimSpace(chi.URLParam(r, "id"))
	if subjectID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("data subject id is required"))
		return
	}
	var req dataSubjectEraseRequest
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	kind := strings.TrimSpace(req.SubjectKind)
	if kind == "" {
		kind = erasureSubjectUser
	}
	caseRef := strings.TrimSpace(req.CaseRef)
	if caseRef == "" {
		caseRef = "data-subject:" + hashHex(subjectID)[:16]
	}
	createBody, _ := json.Marshal(createErasureRequest{
		SubjectKind: kind,
		SubjectRef:  subjectID,
		Aliases:     req.Aliases,
		DataClasses: req.DataClasses,
		CaseRef:     caseRef,
		Reason:      req.Reason,
	})
	createReq := r.Clone(r.Context())
	createReq.Body = ioNopCloser{Reader: bytes.NewReader(createBody)}
	createReq.ContentLength = int64(len(createBody))
	createRec := newCaptureResponse()
	m.handleCreateErasure(createRec, createReq, mc)

	var erasureID string
	if createRec.status == http.StatusCreated {
		var dto erasureRequestDTO
		_ = json.Unmarshal(createRec.body.Bytes(), &dto)
		erasureID = dto.ID
	} else if createRec.status == http.StatusConflict {
		id, found, err := m.findDataSubjectErasureID(r.Context(), mc, kind, subjectID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if !found {
			copyCaptured(w, createRec)
			return
		}
		erasureID = id.String()
	} else {
		copyCaptured(w, createRec)
		return
	}

	execBody, _ := json.Marshal(executeErasureRequest{Reason: req.Reason, ProviderUserID: req.ProviderUserIDs})
	execReq := r.Clone(r.Context())
	execReq.Body = ioNopCloser{Reader: bytes.NewReader(execBody)}
	execReq.ContentLength = int64(len(execBody))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", erasureID)
	execReq = execReq.WithContext(context.WithValue(execReq.Context(), chi.RouteCtxKey, rctx))
	execRec := newCaptureResponse()
	m.handleExecuteErasure(execRec, execReq, mc)
	copyCaptured(w, execRec)
}

func (m *Module) handleDataSubjectErasureStatus(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	subjectID := strings.TrimSpace(chi.URLParam(r, "id"))
	if subjectID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("data subject id is required"))
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("subject_kind"))
	if kind == "" {
		kind = erasureSubjectUser
	}
	id, found, err := m.findDataSubjectErasureID(r.Context(), mc, kind, subjectID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("no erasure request found for this data subject"))
		return
	}
	var out dataSubjectErasureStatusDTO
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		reqRepo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			return err
		}
		reqRec, err := reqRepo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		reqDTO := m.requestDTO(r.Context(), sc, reqRec)
		out = dataSubjectErasureStatusDTO{
			SubjectID:   subjectID,
			SubjectKind: kind,
			State:       dataSubjectState(reqDTO.Status, nil),
			Request:     reqDTO,
			ApprovalRef: reqDTO.ApprovalRef,
			Disclaimer:  "RTBF status is derived from the governed erasure workflow; the audit ledger is retained under GDPR Art. 17(3)(b)/(e).",
		}
		receiptRepo, err := sc.Ext(erasureReceiptKind)
		if err != nil {
			return err
		}
		rec, ok, err := findOne(r.Context(), receiptRepo, eq(colRCErasureID, id.String()))
		if err != nil || !ok {
			return err
		}
		receipt := receiptDTO(rec)
		out.Receipt = &receipt
		out.State = dataSubjectState(reqDTO.Status, &receipt)
		out.KeyShredded = receipt.KeyShredded
		out.Verified = receipt.KeyShredded && receipt.VerifyOK
		out.VerifyReason = receipt.VerifyWhy
		out.ApprovalRef = receipt.ApprovalRef
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) findDataSubjectErasureID(ctx context.Context, mc api.ModuleContext, kind, subjectID string) (model.ID, bool, error) {
	var out model.ID
	found := false
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			return err
		}
		if directID, ok := idParam(subjectID); ok {
			if _, err := repo.Get(ctx, directID); err == nil {
				out, found = directID, true
				return nil
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		lookup := subjectLookupDigest(mc.Tenant, kind, subjectID)
		rows, err := listAll(ctx, repo, eq(colERSubjectKind, kind), eq(colERSubjectLookup, lookup))
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			if key, ok, err := findSubjectKey(ctx, sc, kind, subjectID); err != nil {
				return err
			} else if ok {
				rows, err = listAll(ctx, repo, eq(colERKeyID, key.ID.String()))
				if err != nil {
					return err
				}
			}
		}
		if len(rows) == 0 {
			return nil
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].String(model.ColCreatedAt) > rows[j].String(model.ColCreatedAt)
		})
		out = model.ID(rows[0].String(model.ColID))
		found = true
		return nil
	})
	return out, found, err
}

func dataSubjectState(status string, receipt *erasureReceiptDTO) string {
	if receipt != nil {
		if receipt.KeyShredded && receipt.VerifyOK {
			return "verified"
		}
		return "completed"
	}
	switch status {
	case erasureStatusBlocked:
		return "blocked"
	case erasureStatusDenied:
		return "denied"
	case erasureStatusFailed:
		return "failed"
	case erasureStatusCompleted, erasureStatusGaps:
		return "completed"
	default:
		return "pending"
	}
}

type captureResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newCaptureResponse() *captureResponse {
	return &captureResponse{header: make(http.Header), status: http.StatusOK}
}

func (r *captureResponse) Header() http.Header         { return r.header }
func (r *captureResponse) WriteHeader(status int)      { r.status = status }
func (r *captureResponse) Write(b []byte) (int, error) { return r.body.Write(b) }

func copyCaptured(w http.ResponseWriter, r *captureResponse) {
	for k, vals := range r.header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(r.status)
	_, _ = w.Write(r.body.Bytes())
}

type ioNopCloser struct{ *bytes.Reader }

func (c ioNopCloser) Close() error { return nil }

// dedupeHoldRefs collapses duplicate covering holds (the same hold can cover both
// the subject and a class).
func dedupeHoldRefs(in []HoldRef) []HoldRef {
	seen := map[string]struct{}{}
	var out []HoldRef
	for _, h := range in {
		if _, dup := seen[h.ID]; dup {
			continue
		}
		seen[h.ID] = struct{}{}
		out = append(out, h)
	}
	return out
}

// setErasureStatus updates the request's mutable lifecycle columns inside an
// existing transaction (full read-modify-write — GenericRepo.Update rewrites every
// declared field). It REFUSES to move off a terminal status: a sealed receipt's
// completed state can never be downgraded by a straggling concurrent attempt.
func setErasureStatus(ctx context.Context, sc store.Scope, id model.ID, status, planHash string) error {
	repo, err := sc.Ext(erasureRequestKind)
	if err != nil {
		return err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		return err
	}
	switch rec.String(colERStatus) {
	case erasureStatusCompleted, erasureStatusGaps:
		return errErasureTerminal
	}
	rec[colERStatus] = status
	if planHash != "" {
		rec[colERPlanHash] = planHash
	}
	_, err = repo.Update(ctx, rec)
	return err
}

// claimExecution is the single-flight entry CAS: it transitions the request to
// "executing" (persisting the merged provider ids and the bound plan hash) under
// the store's optimistic version check, so two concurrent approved executes
// serialize — the loser's Update surfaces store.ErrConflict. A crashed run leaves
// "executing" at a NEW version, which a later (non-concurrent) execute claims
// again cleanly.
func (m *Module) claimExecution(ctx context.Context, mc api.ModuleContext, id model.ID, planHash string, providerIDs []string) error {
	return mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		switch rec.String(colERStatus) {
		case erasureStatusCompleted, erasureStatusGaps:
			return errErasureTerminal
		}
		rec[colERStatus] = erasureStatusExecuting
		rec[colERPlanHash] = planHash
		merged := dedupeStrings(append(decodeStrings(rec.String(colERProviderIDs)), providerIDs...))
		rec[colERProviderIDs] = encodeJSON(merged)
		_, err = repo.Update(ctx, rec)
		return err
	})
}

// markErasure is the state+custody+audit transition helper for the deny/block/fail
// paths. The custody insert can hit the dedupe index (a re-attempt under the SAME
// approval, e.g. a second hold-block after an intervening pending poll): the
// poisoned transaction aborts, and the STATUS transition — which must still land —
// retries in its own transaction. note must be a fixed, identifier-free string
// (custody is append-only and outlives the shred).
func (m *Module) markErasure(ctx context.Context, mc api.ModuleContext, id model.ID, status, event, note, approvalRef string, approvers []string) {
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		if err := setErasureStatus(ctx, sc, id, status, ""); err != nil {
			return err
		}
		if err := appendErasureEvent(ctx, sc, id, event, mc.Principal.Actor(), mc.Principal.ActorKind(),
			note, approvalRef, approvers); err != nil {
			if errors.Is(err, store.ErrConflict) {
				// A twin already sealed this custody event; the failed INSERT
				// poisons the tx, so it must abort — the status retry below
				// re-applies the transition alone.
				return errErasureEventSealed
			}
			return err
		}
		return auditEvent(ctx, sc, mc, "compliance.erasure."+event, erasureRequestKind, id, map[string]any{"note": clamp(note, maxNameLen)})
	})
	if errors.Is(err, errErasureEventSealed) {
		err = mc.Data.Mutate(ctx, func(sc store.Scope) error {
			return setErasureStatus(ctx, sc, id, status, "")
		})
	}
	if err != nil && !errors.Is(err, errErasureTerminal) {
		m.debugf("compliance: erasure transition not recorded", "erasure", id.String(), "event", event, "err", err)
	}
}

// erasureHTTPOutcome is what executeErasure renders.
type erasureHTTPOutcome struct {
	code int
	body any
}

// executeErasure runs the physical erasure after both gates cleared: claim →
// targets → account → provider → residual scan → ONE ATOMIC transaction holding
// {hold re-check, crypto-shred, chain verify, receipt, status, custody, finding,
// self-audit}. Failures are honest: every failure before the final transaction
// marks the request failed (re-executable) and NOTHING is shredded — the key must
// outlive every retry; the final transaction is all-or-nothing, so the shred can
// never commit without its receipt (no permanently unrecoverable half-state).
// Un-wired seams complete WITH GAPS, recorded on the receipt. Custody notes are
// fixed, identifier-free strings (custody is append-only and outlives the shred);
// raw errors go to the operator log and the ephemeral HTTP response only.
func (m *Module) executeErasure(ctx context.Context, mc api.ModuleContext, id model.ID, reqRec model.Record, key subjectKey, classes []string, dec GateDecision, providerUserIDs []string, coordWarnings []string) erasureHTTPOutcome {
	// 0) Single-flight claim: CAS the lifecycle to "executing" (terminal statuses
	// refuse; a concurrent claim loses on the version check) and persist the merged
	// provider ids so a later re-execute cannot silently skip the provider leg.
	if err := m.claimExecution(ctx, mc, id, erasurePlanHash(reqRec, key, classes), providerUserIDs); err != nil {
		switch {
		case errors.Is(err, errErasureTerminal):
			return erasureHTTPOutcome{http.StatusConflict, errorBody("erasure already completed (see the receipt)")}
		case errors.Is(err, store.ErrConflict):
			return erasureHTTPOutcome{http.StatusConflict, errorBody("another execute is already in progress for this request")}
		}
		return erasureHTTPOutcome{http.StatusInternalServerError, errorBody("could not claim the request: " + err.Error())}
	}
	providerIDs := dedupeStrings(append(decodeStrings(reqRec.String(colERProviderIDs)), providerUserIDs...))

	// 1) Physical erasure over the in-code target registry (+ the subject-kind
	// cascades), scoped to the request's classes. A transport/store error stops
	// everything before the shred.
	outcomes, err := m.runErasureTargets(ctx, mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), key, classes)
	if err != nil {
		if errors.Is(err, errErasureHeld) {
			m.markErasure(ctx, mc, id, erasureStatusBlocked, erasureEventHoldBlock,
				"a legal hold appeared mid-run; erasure stopped, nothing further destroyed", dec.ApprovalRef, dec.Approvers)
			return erasureHTTPOutcome{http.StatusConflict, errorBody("a legal hold appeared mid-run; erasure stopped (re-execute once it is released)")}
		}
		m.debugf("compliance: erasure target run failed", "erasure", id.String(), "err", err)
		m.markErasure(ctx, mc, id, erasureStatusFailed, erasureEventFailed,
			"target erasure failed; see the operator log", dec.ApprovalRef, dec.Approvers)
		return erasureHTTPOutcome{http.StatusConflict, errorBody("target erasure failed (re-execute to continue): " + err.Error())}
	}
	truncated := false
	for _, o := range outcomes {
		if o.Truncated {
			truncated = true
		}
	}
	if truncated {
		m.markErasure(ctx, mc, id, erasureStatusFailed, erasureEventFailed,
			"erasure truncated at the batch cap; re-execute to continue", dec.ApprovalRef, dec.Approvers)
		return erasureHTTPOutcome{http.StatusConflict, errorBody("erasure truncated at the batch cap; re-execute to continue")}
	}

	// 2) The account leg (engine users — only meaningful for "user" subjects). The
	// pre-flight hold-gate cleared minutes ago at worst; re-check at the leg
	// boundary — the auth partition cannot see tenant holds, so the module checks
	// HERE, immediately before the destructive call.
	accountOutcome := AccountEraseOutcome{Attempted: false, Detail: "not applicable to subject kind " + key.Kind}
	if key.Kind == erasureSubjectUser {
		if out, blocked := m.recheckHoldsForLeg(ctx, mc, id, key, classes, dec); blocked {
			return out
		}
		accountOutcome, err = m.accounts.EraseAccount(ctx, mc.Tenant, key.identifiers(), mc.Principal.Actor(), mc.Principal.ActorKind())
		if err != nil {
			m.debugf("compliance: erasure account leg failed", "erasure", id.String(), "err", err)
			m.markErasure(ctx, mc, id, erasureStatusFailed, erasureEventFailed,
				"account erasure failed; see the operator log", dec.ApprovalRef, dec.Approvers)
			return erasureHTTPOutcome{http.StatusConflict, errorBody("account erasure failed (re-execute to continue): " + err.Error())}
		}
		m.appendCustodyBestEffort(ctx, mc, id, erasureEventAccount, accountSummary(accountOutcome), dec.ApprovalRef, nil)
	}

	// 3) The provider leg (passthrough; runs its own dual-control PEP per
	// deletion — pending approvals there are honest partial progress, not failure;
	// HARD failures veto the shred exactly like a local failure). Hold re-check at
	// the leg boundary: the paced fan-out can run for minutes.
	if out, blocked := m.recheckHoldsForLeg(ctx, mc, id, key, classes, dec); blocked {
		return out
	}
	providerOutcome, err := m.providerEraser.EraseProviderContent(ctx, mc.Tenant, ProviderEraseRequest{
		SubjectUserIDs: providerIDs,
		CaseRef:        reqRec.String(colCaseRef),
		RequestedBy:    mc.Principal.Actor(),
	})
	if err != nil {
		// The raw error can embed provider URLs/user ids: operator log + ephemeral
		// response only — never the append-only custody note.
		m.debugf("compliance: erasure provider leg failed", "erasure", id.String(), "err", err)
		m.markErasure(ctx, mc, id, erasureStatusFailed, erasureEventFailed,
			"provider erasure failed; see the operator log", dec.ApprovalRef, dec.Approvers)
		return erasureHTTPOutcome{http.StatusConflict, errorBody("provider erasure failed (re-execute to continue): " + err.Error())}
	}
	m.appendCustodyBestEffort(ctx, mc, id, erasureEventProvider, providerSummary(providerOutcome), dec.ApprovalRef, nil)
	if providerOutcome.Wired && providerOutcome.Failed > 0 {
		// A wired leg reporting hard failures means provider-side content SURVIVES:
		// shredding now would seal under-erasure as success and destroy the only
		// re-execution path. The key outlives the retry.
		m.markErasure(ctx, mc, id, erasureStatusFailed, erasureEventFailed,
			"provider deletions failed: "+itoa(int64(providerOutcome.Failed))+" of "+itoa(int64(providerOutcome.Enumerated))+"; re-execute to retry", dec.ApprovalRef, dec.Approvers)
		return erasureHTTPOutcome{http.StatusConflict, errorBody(
			itoa(int64(providerOutcome.Failed)) + " provider deletion(s) failed; nothing was shredded — re-execute to retry")}
	}
	if providerOutcome.Wired && providerOutcome.Pending > 0 {
		// Provider deletions await their own dual-control approvals: do NOT shred
		// yet (a re-execute must still know the subject), report honest progress.
		// The provider custody event above already records the counts — only the
		// lifecycle column moves here.
		if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
			return setErasureStatus(ctx, sc, id, erasureStatusPending, "")
		}); err != nil {
			m.debugf("compliance: erasure provider-pending transition not recorded", "erasure", id.String(), "err", err)
		}
		return erasureHTTPOutcome{http.StatusAccepted, executeResultDTO{Status: "provider_pending", ApprovalRef: dec.ApprovalRef,
			Detail: providerSummary(providerOutcome) + "; re-execute once the provider approvals are granted"}}
	}

	// Files-store disclosure leg (read-only). The Anthropic Files store has NO
	// data-subject metadata, so per-subject RTBF cannot SELECT a subject's files — rather
	// than fabricate a match or blind-purge unrelated data, the receipt DISCLOSES the store's
	// presence + size and points the operator at the governed point delete. Skipped when the
	// seam is not wired (then the receipt's provider leg already covers the not-wired posture).
	if m.fileEraser.Wired() {
		m.appendCustodyBestEffort(ctx, mc, id, erasureEventFiles, m.fileStoreDisclosureNote(ctx, mc.Tenant), dec.ApprovalRef, nil)
	}

	// 4) Residual scan (read-only, pre-shred): any surviving identifier occurrence
	// fails the receipt's verification honestly.
	residues, err := m.residualScan(ctx, mc.Tenant, key, classes)
	if err != nil {
		m.debugf("compliance: erasure residual scan failed", "erasure", id.String(), "err", err)
		m.markErasure(ctx, mc, id, erasureStatusFailed, erasureEventFailed,
			"post-erasure verification failed; see the operator log", dec.ApprovalRef, dec.Approvers)
		return erasureHTTPOutcome{http.StatusInternalServerError, errorBody("post-erasure verification failed: " + err.Error())}
	}

	// 5) The point of no return, ALL-OR-NOTHING: holds re-checked in-tx, the
	// crypto-shred, the LIVE chain verification (the bar: /v1/audit/verify
	// semantics must hold AFTER the erasure), the receipt, the status, the custody
	// events, the finding and the self-audit commit together — a failure anywhere
	// rolls the shred back too, so the key can never die without its receipt.
	floor := m.floorFor(ctx, anyModelIOClass(classes))
	var toPublish []sdkmodel.FindingReport
	var receipt erasureReceiptDTO
	err = mc.Data.Mutate(ctx, func(sc store.Scope) error {
		for _, ref := range key.identifiers() {
			hdec, herr := evalHolds(ctx, sc, HoldSubject{Kind: key.Kind, Ref: ref})
			if herr != nil {
				return herr
			}
			if hdec.Held {
				return errErasureHeld
			}
		}
		for _, class := range classes {
			hdec, herr := evalHolds(ctx, sc, HoldSubject{DataClass: class})
			if herr != nil {
				return herr
			}
			if hdec.Held {
				return errErasureHeld
			}
		}
		if err := shredSubjectKey(ctx, sc, key.ID); err != nil {
			return err
		}
		if err := appendErasureEvent(ctx, sc, id, erasureEventShredded, mc.Principal.Actor(), mc.Principal.ActorKind(),
			"subject key destroyed; every pii token sealed for this subject is permanently unintelligible",
			dec.ApprovalRef, dec.Approvers); err != nil {
			return err
		}
		if err := auditEvent(ctx, sc, mc, "compliance.erasure.shred", subjectKeyKind, key.ID, map[string]any{
			"erasure_id": id.String(),
		}); err != nil {
			return err
		}
		if _, err := m.notifyCryptoShredWORM(ctx, key.ID.String(), m.clock.Now().Time()); err != nil {
			return err
		}
		// the coordinator's verdict must come from EXECUTED checks, so the
		// module hands it evidence probes bound to THIS transaction — the only
		// scope that can observe the shred before the receipt seals it. KeyGone
		// re-probes the row shredSubjectKey just deleted; ResidualScan re-runs
		// the registry scan post-shred (the pre-shred scan above cannot see
		// rows that survived the shred transaction itself).
		probes := CryptoShredProbes{
			KeyGone: func(pctx context.Context) (bool, error) {
				_, err := getSubjectKey(pctx, sc, key.ID)
				if errors.Is(err, ErrKeyShredded) {
					return true, nil
				}
				if err != nil {
					return false, err
				}
				return false, nil
			},
			ResidualScan: func(pctx context.Context) ([]string, int, error) {
				return residualScanIn(pctx, sc, key.Kind, key.identifiers(), classes)
			},
		}
		coordVerify, coordWired, err := m.verifyCryptoShredCompleteness(ctx, key.ID.String(), erasureTargetLabels(outcomes), probes)
		if err != nil {
			return err
		}
		verify, verr := sc.Audit().Verify(ctx, 0)
		if verr != nil {
			return verr
		}
		verifyOK := verify.OK && verify.Checked > 0 && len(residues) == 0
		verifyWhy := verify.Reason
		if verify.Checked == 0 && verifyWhy == "" {
			verifyWhy = "no-events"
		}
		if len(residues) > 0 {
			verifyWhy = strings.TrimSpace(verifyWhy + " residual identifiers at: " + strings.Join(residues, ", "))
		}
		if coordWired && !coordVerify.Complete {
			verifyOK = false
			verifyWhy = strings.TrimSpace(verifyWhy + " enterprise RTBF coordinator incomplete: " + coordinatorVerificationSummary(coordVerify))
		}
		if len(coordWarnings) > 0 {
			verifyWhy = strings.TrimSpace(verifyWhy + " enterprise RTBF coordinator warnings: " + strings.Join(dedupeStrings(coordWarnings), "; "))
		}
		gaps := !accountAttemptedOrNA(key.Kind, accountOutcome) || !providerOutcome.Wired || !verifyOK
		status := erasureStatusCompleted
		if gaps {
			status = erasureStatusGaps
		}
		head, ok, err := sc.Audit().Head(ctx)
		if err != nil {
			return err
		}
		var seq int64
		hash := ""
		if ok {
			seq, hash = head.Seq, hex.EncodeToString(head.Hash)
		}
		manifest := erasureManifest(id.String(), key.Kind, outcomes, accountOutcome, providerOutcome, verifyOK, classes)
		repo, err := sc.Ext(erasureReceiptKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colRCErasureID:  id.String(),
			colRCSubject:    key.Kind,
			colRCToken:      reqRec.String(colERToken),
			colRCTargets:    encodeJSON(outcomes),
			colRCAccount:    accountSummary(accountOutcome),
			colRCProvider:   providerSummary(providerOutcome),
			colRCFloorDays:  int64(floor.ProviderFloorDays),
			colRCFloorKnown: floor.ProviderFloorKnown,
			colRCFloorSrc:   nullableText(floor.ProviderFloorSource),
			colRCShredded:   true,
			colRCVerifyOK:   verifyOK,
			colRCVerifyN:    verify.Checked,
			colRCVerifyWhy:  nullableText(verifyWhy),
			colRCRetained:   encodeJSON(retainedReconciliation),
			colCaseRef:      reqRec.String(colCaseRef),
			colApprovalRef:  dec.ApprovalRef,
			colLedgerSeq:    seq,
			colLedgerHash:   nullableText(hash),
			colManifestHash: hashHex(manifest),
		})
		if err != nil {
			return err
		}
		receipt = receiptDTO(rec)
		if err := setErasureStatus(ctx, sc, id, status, ""); err != nil {
			return err
		}
		if err := appendErasureEvent(ctx, sc, id, erasureEventSealed, mc.Principal.Actor(), mc.Principal.ActorKind(),
			"receipt sealed ("+status+")", dec.ApprovalRef, dec.Approvers); err != nil {
			return err
		}
		rep, err := m.createErasureFinding(ctx, sc, id, status, reqRec.String(colCaseRef))
		if err != nil {
			return err
		}
		toPublish = append(toPublish, rep)
		return auditEvent(ctx, sc, mc, "compliance.erasure.execute", erasureRequestKind, id, map[string]any{
			// approvers is the count of distinct HUMANS (the quorum), not of credentials;
			// unattributed_approvals reports approvals with no person behind them, so the
			// ledger can never read as "two humans" when it was one human twice.
			"case_ref": reqRec.String(colCaseRef), "status": status, "approval_ref": dec.ApprovalRef,
			"approvers": dec.Quorum(), "unattributed_approvals": dec.UnattributedApprovals, "verify_ok": verifyOK,
		})
	})
	if err != nil {
		if errors.Is(err, errErasureHeld) {
			m.markErasure(ctx, mc, id, erasureStatusBlocked, erasureEventHoldBlock,
				"a legal hold appeared before the crypto-shred; nothing was shredded", dec.ApprovalRef, dec.Approvers)
			return erasureHTTPOutcome{http.StatusConflict, errorBody("a legal hold appeared before the crypto-shred; nothing was shredded (re-execute once it is released)")}
		}
		m.debugf("compliance: erasure shred+seal transaction failed", "erasure", id.String(), "err", err)
		m.markErasure(ctx, mc, id, erasureStatusFailed, erasureEventFailed,
			"crypto-shred + receipt transaction failed and rolled back; see the operator log", dec.ApprovalRef, dec.Approvers)
		return erasureHTTPOutcome{http.StatusInternalServerError, errorBody("crypto-shred transaction failed and rolled back (re-execute to continue): " + err.Error())}
	}
	m.publishFindings(ctx, mc.Tenant, toPublish)
	return erasureHTTPOutcome{http.StatusOK, receipt}
}

// recheckHoldsForLeg re-runs the full pre-flight hold check at a leg boundary
// (account/provider — destructive paths the in-batch evalHolds discipline cannot
// reach). Held or error ⇒ the leg does not run; deny-closed.
func (m *Module) recheckHoldsForLeg(ctx context.Context, mc api.ModuleContext, id model.ID, key subjectKey, classes []string, dec GateDecision) (erasureHTTPOutcome, bool) {
	for _, ref := range key.identifiers() {
		hdec, err := m.CheckHold(ctx, mc.Tenant, HoldSubject{Kind: key.Kind, Ref: ref})
		if err != nil {
			return erasureHTTPOutcome{http.StatusServiceUnavailable, errorBody("legal-hold check unavailable; erasure stopped (fail closed)")}, true
		}
		if hdec.Held {
			m.markErasure(ctx, mc, id, erasureStatusBlocked, erasureEventHoldBlock,
				"a legal hold appeared mid-run; erasure stopped before the next leg", dec.ApprovalRef, dec.Approvers)
			return erasureHTTPOutcome{http.StatusConflict, errorBody("a legal hold appeared mid-run; erasure stopped (re-execute once it is released)")}, true
		}
	}
	for _, class := range classes {
		hdec, err := m.CheckHold(ctx, mc.Tenant, HoldSubject{DataClass: class})
		if err != nil {
			return erasureHTTPOutcome{http.StatusServiceUnavailable, errorBody("legal-hold check unavailable; erasure stopped (fail closed)")}, true
		}
		if hdec.Held {
			m.markErasure(ctx, mc, id, erasureStatusBlocked, erasureEventHoldBlock,
				"a legal hold appeared mid-run; erasure stopped before the next leg", dec.ApprovalRef, dec.Approvers)
			return erasureHTTPOutcome{http.StatusConflict, errorBody("a legal hold appeared mid-run; erasure stopped (re-execute once it is released)")}, true
		}
	}
	return erasureHTTPOutcome{}, false
}

// appendCustodyBestEffort seals a mid-run custody event, tolerating the dedupe
// conflict of an idempotent re-execute under the same approval.
func (m *Module) appendCustodyBestEffort(ctx context.Context, mc api.ModuleContext, id model.ID, event, note, approvalRef string, approvers []string) {
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return appendErasureEvent(ctx, sc, id, event, mc.Principal.Actor(), mc.Principal.ActorKind(), note, approvalRef, approvers)
	})
	if err != nil && !errors.Is(err, store.ErrConflict) {
		m.debugf("compliance: erasure custody event not recorded", "erasure", id.String(), "event", event, "err", err)
	}
}

func accountSummary(o AccountEraseOutcome) string {
	if !o.Attempted {
		return "not_attempted: " + o.Detail
	}
	return "erased " + itoa(int64(o.Erased)) + " account(s); " + o.Detail
}

func providerSummary(o ProviderEraseOutcome) string {
	if !o.Wired {
		return "not_wired: " + o.Detail
	}
	s := "enumerated " + itoa(int64(o.Enumerated)) + ", erased " + itoa(int64(o.Erased)) +
		", pending " + itoa(int64(o.Pending)) + ", failed " + itoa(int64(o.Failed))
	if o.Detail != "" {
		s += "; " + o.Detail
	}
	return s
}

// accountAttemptedOrNA reports whether the account leg leaves NO gap: either it ran,
// or it does not apply to this subject kind.
func accountAttemptedOrNA(kind string, o AccountEraseOutcome) bool {
	if kind != erasureSubjectUser {
		return true
	}
	return o.Attempted
}

// anyModelIOClass reports whether any affected class carries model I/O — the §7
// provider-floor disclosure then applies to the receipt.
func anyModelIOClass(classes []string) bool {
	for _, c := range classes {
		if dc, ok := dataClassByID[c]; ok && dc.ModelIO {
			return true
		}
	}
	return false
}

// erasureManifest is the canonical, order-stable summary the receipt's
// manifest_hash commits to (the sealRetentionRun pattern).
func erasureManifest(id, subjectKind string, outcomes []targetOutcome, acc AccountEraseOutcome, prov ProviderEraseOutcome, verifyOK bool, classes []string) string {
	parts := []string{"erasure-receipt|v1", id, subjectKind, strings.Join(classes, ",")}
	for _, o := range outcomes {
		parts = append(parts, o.Target+":"+o.Mode+":"+itoa(o.Examined)+":"+itoa(o.Erased)+":"+itoa(o.Scrubbed)+":"+o.Status)
	}
	parts = append(parts, accountSummary(acc), providerSummary(prov))
	if verifyOK {
		parts = append(parts, "verify:ok")
	} else {
		parts = append(parts, "verify:failed")
	}
	return strings.Join(parts, "|")
}

func erasureTargetLabels(outcomes []targetOutcome) []string {
	out := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		if strings.TrimSpace(o.Target) != "" {
			out = append(out, o.Target)
		}
	}
	return out
}

func coordinatorVerificationSummary(v CryptoShredVerification) string {
	parts := []string{
		"complete=" + boolText(v.Complete),
		"key_destroyed=" + boolText(v.KeyDestroyed),
		"worm_notified=" + boolText(v.WORMNotified),
	}
	if v.ResidualScan.ScanDepth != "" {
		parts = append(parts, "scan_depth="+v.ResidualScan.ScanDepth)
	}
	if v.ResidualScan.ResiduesFound > 0 {
		parts = append(parts, "residues_found="+itoa(int64(v.ResidualScan.ResiduesFound)))
	}
	if len(v.Unverified) > 0 {
		parts = append(parts, "unverified=["+strings.Join(dedupeStrings(v.Unverified), "; ")+"]")
	}
	if v.PolicyApplied != "" {
		parts = append(parts, "policy="+v.PolicyApplied)
	}
	return strings.Join(parts, ", ")
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// createErasureFinding records the completion Finding in the caller's transaction
// and returns the bus report to publish AFTER commit (the createHoldFinding
// pattern). Severity medium: an erasure is a notable, governed mutation — not an
// incident.
func (m *Module) createErasureFinding(ctx context.Context, sc store.Scope, id model.ID, status, caseRef string) (sdkmodel.FindingReport, error) {
	title := clamp("RTBF erasure "+status+" (case "+caseRef+")", maxNameLen)
	detail := id.String() + "|" + status + "|" + caseRef
	now := m.clock.Now()
	if _, err := sc.Findings().Create(ctx, model.Finding{
		Kind: findingErasureCompleted, Severity: model.Severity(sdkmodel.SeverityMedium), Status: model.FindingOpen,
		Source: Name, SubjectKind: "erasure_request", SubjectID: id,
		Title: title, DetailHash: hashBytes(detail), OccurredAt: now,
	}); err != nil {
		return sdkmodel.FindingReport{}, err
	}
	return sdkmodel.FindingReport{
		Kind: findingErasureCompleted, Severity: sdkmodel.SeverityMedium,
		SubjectKind: "erasure_request", SubjectRef: clamp(id.String(), maxRefLen),
		Title: title, DetailHash: hashHex(detail), OccurredAt: now.Time(),
	}, nil
}
