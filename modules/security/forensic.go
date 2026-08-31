// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the incident-response / forensic edge (docs/SECURITY-HARDENING.md, the EC-Council
// differential): a CASE groups an incident; its TIMELINE is reconstructed from the
// append-only, hash-chained ledger and VERIFIED (chain + signed checkpoints) so the
// evidence handed to an auditor is provably unaltered; the case exports to WORM/SIEM
// for an external immutable copy. The module never trusts the ledger — it proves it.

// The case lifecycle states.
var caseStatuses = map[string]bool{"open": true, "investigating": true, "contained": true, "closed": true}

// The link kinds that may attach evidence to a case's append-only chain of custody.
var linkKinds = map[string]bool{"finding": true, "audit_seq": true, "anomaly": true, "note": true}

// caseDTO is the wire shape of a forensic case (the contract).
type caseDTO struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	Severity        string `json:"severity"`
	SubjectKind     string `json:"subject_kind"`
	SubjectRef      string `json:"subject_ref"`
	Summary         string `json:"summary,omitempty"`
	OpenedBy        string `json:"opened_by"`
	IntegrityOK     bool   `json:"integrity_ok"`
	IntegrityReason string `json:"integrity_reason,omitempty"`
	AttestedSeq     int64  `json:"attested_seq"`
	OpenedAt        string `json:"opened_at"`
	ClosedAt        string `json:"closed_at,omitempty"`
}

func toCaseDTO(rec model.Record) caseDTO {
	return caseDTO{
		ID: rec.String(model.ColID), Title: rec.String(colTitle), Status: rec.String(colStatus),
		Severity: rec.String(colSeverity), SubjectKind: rec.String(colSubjectKind), SubjectRef: rec.String(colSubjectRef),
		Summary: rec.String(colSummary), OpenedBy: rec.String(colOpenedBy), IntegrityOK: rec.Bool(colIntegrityOK),
		IntegrityReason: rec.String(colIntegrityReason), AttestedSeq: rec.Int(colAttestedSeq),
		OpenedAt: rec.String(colOpenedAt), ClosedAt: rec.String(colClosedAt),
	}
}

// ---- case CRUD ------------------------------------------------------------------

// handleListCases lists the tenant's forensic cases.
func (m *Module) handleListCases(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		q.Filters = append(q.Filters, eq(colStatus, v))
	}
	out := listResponse[caseDTO]{Items: []caseDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(caseKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toCaseDTO(rec))
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

type createCaseRequest struct {
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	Summary     string `json:"summary,omitempty"`
}

// handleCreateCase opens an incident case, snapshotting the ledger integrity at the
// moment of opening (so a later tamper is detectable against the open-time state).
// The open is self-audited to the real principal (docs/SECURITY-HARDENING.md).
func (m *Module) handleCreateCase(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var req createCaseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	title := clamp(strings.TrimSpace(req.Title), maxNameLen)
	if title == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("title is required"))
		return
	}
	sev := model.Severity(strings.TrimSpace(req.Severity))
	if sev == "" {
		sev = model.SeverityMedium
	}
	if !coreAtLeast(sev, model.SeverityLow) {
		writeJSON(w, http.StatusBadRequest, errorBody("severity must be low, medium, high or critical"))
		return
	}

	var out caseDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(caseKind)
		if err != nil {
			return err
		}
		integ, err := m.verifyLedgerIntegrity(r.Context(), sc)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		rec := model.Record{
			colTitle: title, colStatus: "open", colSeverity: string(sev),
			colSubjectKind: clamp(strings.TrimSpace(req.SubjectKind), maxRefLen),
			colSubjectRef:  clamp(strings.TrimSpace(req.SubjectRef), maxRefLen),
			colSummary:     clamp(strings.TrimSpace(req.Summary), maxReasonLen),
			colOpenedBy:    mc.Principal.Actor(),
			colIntegrityOK: integ.ChainOK, colIntegrityReason: integ.ChainReason,
			colAttestedSeq: integ.AttestedSeq, colOpenedAt: now.String(),
		}
		created, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toCaseDTO(created)
		return auditEvent(r.Context(), sc, mc, "security.case.open", caseKind, model.ID(created.String(model.ColID)), map[string]any{
			"severity": string(sev), "subject_kind": req.SubjectKind, "integrity_ok": integ.ChainOK,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleGetCase returns one case.
func (m *Module) handleGetCase(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out caseDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(caseKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		found, out = true, toCaseDTO(rec)
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

type updateCaseRequest struct {
	Status   *string `json:"status,omitempty"`
	Severity *string `json:"severity,omitempty"`
	Summary  *string `json:"summary,omitempty"`
}

// handleUpdateCase updates a case's lifecycle (status/severity/summary), self-audited.
// Moving to "closed" stamps closed_at. The case METADATA is mutable; its chain of
// custody (links) is append-only.
func (m *Module) handleUpdateCase(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req updateCaseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Status != nil && !caseStatuses[*req.Status] {
		writeJSON(w, http.StatusBadRequest, errorBody("status must be open, investigating, contained or closed"))
		return
	}
	if req.Severity != nil && !coreAtLeast(model.Severity(*req.Severity), model.SeverityLow) {
		writeJSON(w, http.StatusBadRequest, errorBody("severity must be low, medium, high or critical"))
		return
	}

	var out caseDTO
	found := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(caseKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		if req.Status != nil {
			rec[colStatus] = *req.Status
			if *req.Status == "closed" && rec.String(colClosedAt) == "" {
				rec[colClosedAt] = m.clock.Now().String()
			}
		}
		if req.Severity != nil {
			rec[colSeverity] = *req.Severity
		}
		if req.Summary != nil {
			rec[colSummary] = clamp(strings.TrimSpace(*req.Summary), maxReasonLen)
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		found, out = true, toCaseDTO(updated)
		return auditEvent(r.Context(), sc, mc, "security.case.update", caseKind, id, map[string]any{
			"status": out.Status, "severity": out.Severity,
		})
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

// ---- chain of custody (append-only links) ---------------------------------------

type caseLinkDTO struct {
	ID       string `json:"id"`
	CaseRef  string `json:"case_ref"`
	LinkKind string `json:"link_kind"`
	LinkRef  string `json:"link_ref"`
	Note     string `json:"note,omitempty"`
	LinkedBy string `json:"linked_by"`
	LinkedAt string `json:"linked_at"`
}

func toCaseLinkDTO(rec model.Record) caseLinkDTO {
	return caseLinkDTO{
		ID: rec.String(model.ColID), CaseRef: rec.String(colCaseRef), LinkKind: rec.String(colLinkKind),
		LinkRef: rec.String(colLinkRef), Note: rec.String(colNote), LinkedBy: rec.String(colLinkedBy),
		LinkedAt: rec.String(colLinkedAt),
	}
}

type linkRequest struct {
	LinkKind string `json:"link_kind"`
	LinkRef  string `json:"link_ref"`
	Note     string `json:"note,omitempty"`
}

// handleLinkCase appends one immutable chain-of-custody link attaching a finding /
// ledger sequence / anomaly / note to a case. The link is APPEND-ONLY evidence and
// the attachment is self-audited (docs/SECURITY-HARDENING.md).
func (m *Module) handleLinkCase(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	caseID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req linkRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !linkKinds[req.LinkKind] {
		writeJSON(w, http.StatusBadRequest, errorBody("link_kind must be finding, audit_seq, anomaly or note"))
		return
	}
	if strings.TrimSpace(req.LinkRef) == "" && req.LinkKind != "note" {
		writeJSON(w, http.StatusBadRequest, errorBody("link_ref is required"))
		return
	}

	var out caseLinkDTO
	notFound := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		caseRepo, err := sc.Ext(caseKind)
		if err != nil {
			return err
		}
		if _, err := caseRepo.Get(r.Context(), caseID); err != nil {
			if isNotFound(err) {
				notFound = true
				return nil
			}
			return err
		}
		linkRepo, err := sc.Ext(caseLinkKind)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		created, err := linkRepo.Create(r.Context(), model.Record{
			colCaseRef: caseID.String(), colLinkKind: req.LinkKind, colLinkRef: clamp(strings.TrimSpace(req.LinkRef), maxRefLen),
			colNote: clamp(strings.TrimSpace(req.Note), maxReasonLen), colLinkedBy: mc.Principal.Actor(), colLinkedAt: now.String(),
		})
		if err != nil {
			return err
		}
		out = toCaseLinkDTO(created)
		return auditEvent(r.Context(), sc, mc, "security.case.link", caseLinkKind, caseID, map[string]any{
			"link_kind": req.LinkKind, "link_ref": clamp(req.LinkRef, maxRefLen),
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleListCaseLinks lists a case's chain of custody.
func (m *Module) handleListCaseLinks(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	caseID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	out := listResponse[caseLinkDTO]{Items: []caseLinkDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(caseLinkKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colCaseRef, caseID.String()))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toCaseLinkDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- the verified, reconstructible timeline -------------------------------------

// handleTimeline reconstructs a case's timeline from the append-only ledger and
// VERIFIES the chain and (where a checkpoint key is wired) the signed checkpoints,
// then enriches it with attribution (identity), least-privilege drift and
// data lineage. It is a PRIVILEGED, self-audited read (docs/SECURITY-HARDENING.md, §4): the
// self-audit append and the chain reads share one transaction.
func (m *Module) handleTimeline(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	caseID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	limit := 500
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 5000 {
			limit = n
		}
	}
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))

	var out timelineResponse
	notFound := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		caseRepo, err := sc.Ext(caseKind)
		if err != nil {
			return err
		}
		rec, err := caseRepo.Get(r.Context(), caseID)
		if err != nil {
			if isNotFound(err) {
				notFound = true
				return nil
			}
			return err
		}
		c := toCaseDTO(rec)

		// Self-audit the privileged reconstruction BEFORE any evidence is returned.
		if err := auditEvent(r.Context(), sc, mc, "security.case.timeline.read", caseKind, caseID, nil); err != nil {
			return err
		}

		// The explicitly attached ledger sequences (chain of custody, kind audit_seq).
		linkRepo, err := sc.Ext(caseLinkKind)
		if err != nil {
			return err
		}
		linkRecs, err := listAll(r.Context(), linkRepo, eq(colCaseRef, caseID.String()))
		if err != nil {
			return err
		}
		linkedSeq := map[int64]bool{}
		for _, lr := range linkRecs {
			if lr.String(colLinkKind) == "audit_seq" {
				if n, perr := strconv.ParseInt(lr.String(colLinkRef), 10, 64); perr == nil {
					linkedSeq[n] = true
				}
			}
		}

		// Verify the chain + checkpoints (the forensic guarantee).
		integ, err := m.verifyLedgerIntegrity(r.Context(), sc)
		if err != nil {
			return err
		}
		out.Integrity = integ

		// Walk the chain, selecting the events relevant to the case subject (or the
		// explicitly linked sequences), bounded.
		events, truncated, err := walkRelevant(r.Context(), sc, c.SubjectRef, linkedSeq, from, to, limit)
		if err != nil {
			return err
		}
		out.Case = c
		out.Events = events
		out.Truncated = truncated
		out.Attribution = resolveAttribution(r.Context(), sc, c.SubjectKind, c.SubjectRef)
		out.Drift = subjectDrift(r.Context(), sc, c.SubjectRef)
		out.Lineage = subjectLineage(r.Context(), sc, c.SubjectRef)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// timelineResponse is the full forensic reconstruction (the contract).
type timelineResponse struct {
	Case        caseDTO         `json:"case"`
	Integrity   integrityDTO    `json:"integrity"`
	Attribution *attributionDTO `json:"attribution,omitempty"`
	Drift       []driftRefDTO   `json:"drift,omitempty"`
	Lineage     []lineageRefDTO `json:"lineage,omitempty"`
	Events      []timelineEvent `json:"events"`
	// Truncated reports that the subject-relevance window exceeded the limit and the
	// OLDEST subject-relevance events were dropped — so a reader is never shown a
	// partial timeline that looks complete. Explicitly-linked chain-of-custody events
	// are NEVER dropped or windowed (docs/SECURITY-HARDENING.md).
	Truncated bool `json:"truncated"`
}

// timelineEvent is one verified ledger event in the reconstruction. It carries the
// chain fields (seq/hash/prev_hash) so a reader can independently re-verify the
// link, and never any payload (the ledger holds none).
type timelineEvent struct {
	Seq        int64  `json:"seq"`
	OccurredAt string `json:"occurred_at"`
	Actor      string `json:"actor"`
	ActorKind  string `json:"actor_kind"`
	Action     string `json:"action"`
	TargetKind string `json:"target_kind,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
	Hash       string `json:"hash"`
	PrevHash   string `json:"prev_hash"`
	Signed     bool   `json:"signed"`
	Linked     bool   `json:"linked"`
}

// walkRelevant streams the ledger in sequence order and returns the events relevant
// to the case subject. Two classes are kept SEPARATE:
//   - LINKED events (explicitly pinned audit_seq chain-of-custody, linkedSeq) are
//     ALWAYS included — never window-filtered, never trimmed (docs/SECURITY-HARDENING.md: pinned
//     custody evidence must appear in the reconstruction).
//   - SUBJECT-RELEVANCE events (matching the subject ref, or the recent window when
//     no subject is set) are filtered by the optional [from,to] window and bounded
//     to the newest `limit`; when more exist the OLDEST are dropped and `truncated`
//     is reported (never a silent partial timeline).
//
// The two sets are merged and returned in ascending sequence order.
func walkRelevant(ctx context.Context, sc store.Scope, subjectRef string, linkedSeq map[int64]bool, from, to string, limit int) ([]timelineEvent, bool, error) {
	subjectRef = strings.TrimSpace(subjectRef)
	var linked, window []timelineEvent
	truncated := false
	err := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
		if linkedSeq[ev.Seq] {
			linked = append(linked, mkTimelineEvent(ev, true))
			return nil
		}
		at := ev.OccurredAt.String()
		if from != "" && at < from {
			return nil
		}
		if to != "" && at > to {
			return nil
		}
		if subjectRef != "" {
			if ev.TargetID.String() != subjectRef && !strings.Contains(ev.Actor, subjectRef) && !metaRefers(ev.Meta, subjectRef) {
				return nil
			}
		}
		window = append(window, mkTimelineEvent(ev, false))
		if len(window) > limit {
			window = window[len(window)-limit:]
			truncated = true
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	out := append(linked, window...)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, truncated, err
}

// mkTimelineEvent projects a sealed ledger event into a verifiable timeline entry —
// chain fields included, never any payload (the ledger holds none).
func mkTimelineEvent(ev model.AuditEvent, linked bool) timelineEvent {
	return timelineEvent{
		Seq: ev.Seq, OccurredAt: ev.OccurredAt.String(), Actor: ev.Actor, ActorKind: ev.ActorKind, Action: ev.Action,
		TargetKind: string(ev.TargetKind), TargetID: ev.TargetID.String(),
		Hash: hex.EncodeToString(ev.Hash), PrevHash: hex.EncodeToString(ev.PrevHash),
		Signed: len(ev.Sig) > 0, Linked: linked,
	}
}

// metaRefers reports whether the event's Meta references ref (as a value of any
// string field) — so an event that names the subject in its context is included.
func metaRefers(meta map[string]any, ref string) bool {
	for _, v := range meta {
		if s, ok := v.(string); ok && s == ref {
			return true
		}
	}
	return false
}

// ---- ledger integrity + export --------------------------------------------------

// integrityDTO reports the verified state of the tenant's evidence ledger.
type integrityDTO struct {
	ChainOK             bool   `json:"chain_ok"`
	ChainChecked        int64  `json:"chain_checked"`
	ChainBreakAt        int64  `json:"chain_break_at,omitempty"`
	ChainReason         string `json:"chain_reason,omitempty"`
	CheckpointsVerified bool   `json:"checkpoints_verified"`
	CheckpointsOK       bool   `json:"checkpoints_ok"`
	Checkpoints         int    `json:"checkpoints"`
	CheckpointBreakAt   int64  `json:"checkpoint_break_at,omitempty"`
	CheckpointReason    string `json:"checkpoint_reason,omitempty"`
	// CheckpointStatus is the three-answer verdict (audit.CheckpointStatus): "ok",
	// "failed", or "pending" when nothing has been attested yet. It exists because
	// CheckpointsOK cannot tell a young ledger from a tampered one — both read
	// false — and a console that paints the young one red teaches operators to
	// ignore the red. Empty ONLY when CheckpointsVerified is false (no key wired:
	// the separate "unavailable" answer).
	CheckpointStatus string `json:"checkpoint_status,omitempty"`
	AttestedSeq      int64  `json:"attested_seq"`
	HeadSeq          int64  `json:"head_seq"`
}

// verifyIntegrity proves the tenant's ledger: the chain is internally consistent
// (AuditLog.Verify) AND, when a checkpoint public key is wired, the signed
// checkpoints are authentic (audit.VerifyCheckpoints). Without a key the chain is
// still verified but the signed-checkpoint attestation is reported as unavailable —
// the product never fakes a guarantee it cannot make (docs/SECURITY-HARDENING.md, §5).
func verifyIntegrity(ctx context.Context, sc store.Scope, pub ed25519.PublicKey) (integrityDTO, error) {
	return verifyIntegrityWith(ctx, sc, pub, nil, false)
}

// checkpointVerifier resolves (and caches) the module's multi-candidate
// checkpoint verifier from the lazy source. The single-flight exists for
// correctness as much as speed: the source may fetch the off-box public key
// over HTTP and the kmssign backends memoize it without a lock, so concurrent
// integrity requests must not race into the source — cpMu serializes it
// (a dedicated lock, so a slow KMS never blocks the module lifecycle). A
// failed source is NOT cached (each request retries), so a transient KMS
// outage heals on the next call. nil means "no verifier available right now".
func (m *Module) checkpointVerifier(ctx context.Context) *audit.CheckpointVerifier {
	if m.cpVerifierSource == nil {
		return nil
	}
	m.cpMu.Lock()
	defer m.cpMu.Unlock()
	if m.cpVerifier != nil {
		return m.cpVerifier
	}
	v, err := m.cpVerifierSource(ctx)
	if err != nil || v == nil || v.Empty() {
		return nil
	}
	m.cpVerifier = v
	return v
}

// verifyLedgerIntegrity is the module's integrity check: chain consistency plus
// the signed-checkpoint attestation under the right key set. With a
// verifier source wired (an off-box HYOK signer), the source's candidate set —
// on-box + off-box keys — is authoritative; if it is UNAVAILABLE (KMS outage),
// the attestation is reported unverified rather than checking off-box-signed
// checkpoints against the on-box key alone, which would misreport a healthy
// custody posture as checkpoint-sig-invalid. Never a faked pass, and never a
// 500 for the chain-consistency half.
func (m *Module) verifyLedgerIntegrity(ctx context.Context, sc store.Scope) (integrityDTO, error) {
	return verifyIntegrityWith(ctx, sc, m.checkpointKey, m.checkpointVerifier(ctx), m.cpVerifierSource != nil)
}

// verifyIntegrityWith runs the checks against an explicit verifier (preferred)
// or the single on-box key. sourceConfigured distinguishes "no off-box signer"
// (fall back to pub) from "off-box signer configured but its verifier is
// unavailable right now" (report unverified — see verifyLedgerIntegrity).
func verifyIntegrityWith(ctx context.Context, sc store.Scope, pub ed25519.PublicKey, verifier *audit.CheckpointVerifier, sourceConfigured bool) (integrityDTO, error) {
	var out integrityDTO
	rep, err := sc.Audit().Verify(ctx, 1)
	if err != nil {
		return out, err
	}
	out.ChainOK, out.ChainChecked, out.ChainBreakAt, out.ChainReason = rep.OK, rep.Checked, rep.BreakAt, rep.Reason
	if head, ok, herr := sc.Audit().Head(ctx); herr == nil && ok {
		out.HeadSeq = head.Seq
	}
	if verifier == nil && !sourceConfigured && len(pub) > 0 {
		verifier = audit.NewCheckpointVerifier().AddEd25519(pub)
	}
	if verifier != nil && !verifier.Empty() {
		crep, cerr := audit.VerifyCheckpointsWith(ctx, sc.Audit(), verifier)
		if cerr != nil {
			return out, cerr
		}
		out.CheckpointsVerified = true
		out.CheckpointsOK, out.Checkpoints = crep.OK, crep.Checkpoints
		out.CheckpointBreakAt, out.CheckpointReason, out.AttestedSeq = crep.FirstBadSeq, crep.Reason, crep.LatestAttestedSeq
		// The verdict the boolean above cannot carry: an unattested young ledger is
		// "pending", not a failure (audit.CheckpointStatus).
		out.CheckpointStatus = string(crep.Status())
	}
	return out, nil
}

// handleVerifyIntegrity is the standalone proof endpoint: verify the whole tenant
// ledger chain and its signed checkpoints. Self-audited (the act of verifying is
// itself recorded into the chain it checks).
func (m *Module) handleVerifyIntegrity(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var out integrityDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := auditEvent(r.Context(), sc, mc, "security.integrity.verify", caseKind, "", nil); err != nil {
			return err
		}
		var verr error
		out, verr = m.verifyLedgerIntegrity(r.Context(), sc)
		return verr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// exportResponse is the SIEM/WORM export of a case's ledger evidence. Each line
// carries the chain-integrity fields (seq/prev_hash/hash/sig) so an external WORM
// store can re-verify the chain offline (docs/SECURITY-HARDENING.md). Ship these lines; the
// module produces the verifiable evidence, it does not implement the transport.
type exportResponse struct {
	Format    string   `json:"format"`
	Count     int      `json:"count"`
	Integrity bool     `json:"integrity_ok"`
	Lines     []string `json:"lines"`
}

// handleExportCase exports a case's relevant ledger events in a SIEM format
// (every format audit.Formats() lists), re-verifiable offline. Self-audited (an evidence
// export is a privileged, recon-relevant action).
func (m *Module) handleExportCase(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	caseID, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	format := audit.Format(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = audit.DefaultFormat()
	}
	if !audit.ValidFormat(format) {
		writeJSON(w, http.StatusBadRequest, errorBody("format must be one of "+audit.FormatList()))
		return
	}

	out := exportResponse{Format: string(format), Lines: []string{}}
	notFound := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		caseRepo, err := sc.Ext(caseKind)
		if err != nil {
			return err
		}
		rec, err := caseRepo.Get(r.Context(), caseID)
		if err != nil {
			if isNotFound(err) {
				notFound = true
				return nil
			}
			return err
		}
		c := toCaseDTO(rec)
		if err := auditEvent(r.Context(), sc, mc, "security.case.export", caseKind, caseID, map[string]any{"format": string(format)}); err != nil {
			return err
		}
		integ, err := m.verifyLedgerIntegrity(r.Context(), sc)
		if err != nil {
			return err
		}
		out.Integrity = integ.ChainOK

		linkRepo, err := sc.Ext(caseLinkKind)
		if err != nil {
			return err
		}
		linkRecs, err := listAll(r.Context(), linkRepo, eq(colCaseRef, caseID.String()))
		if err != nil {
			return err
		}
		linkedSeq := map[int64]bool{}
		for _, lr := range linkRecs {
			if lr.String(colLinkKind) == "audit_seq" {
				if n, perr := strconv.ParseInt(lr.String(colLinkRef), 10, 64); perr == nil {
					linkedSeq[n] = true
				}
			}
		}
		subjectRef := strings.TrimSpace(c.SubjectRef)
		return sc.Audit().Walk(r.Context(), 1, func(ev model.AuditEvent) error {
			relevant := linkedSeq[ev.Seq]
			if !relevant && subjectRef != "" {
				relevant = ev.TargetID.String() == subjectRef || strings.Contains(ev.Actor, subjectRef) || metaRefers(ev.Meta, subjectRef)
			}
			if !relevant {
				return nil
			}
			line, ferr := audit.FormatEvent(ev, format)
			if ferr != nil {
				return ferr
			}
			out.Lines = append(out.Lines, line)
			return nil
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if notFound {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	out.Count = len(out.Lines)
	writeJSON(w, http.StatusOK, out)
}
