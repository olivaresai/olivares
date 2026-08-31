// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// claudefiles.go is the GOVERNED plane over Anthropic's persistent Files store —
// the workspace-scoped, shared-across-keys, NOT-ZDR file store the inference connector can
// upload to. It closes capability-gaps #10 (Files API upload-only): an OBSERVE inventory
// that lists the store and emits a posture (persistent / no-ZDR / shared-across-keys), and a
// GOVERNED point DELETE that — like the legal-hold release — passes the Sedona
// both-must-clear hold gate, the CRITICAL dual-control approval (no break-glass; the
// ".erase" suffix tiers it in risktier.go), and seals a tamper-evident receipt. The actual
// HTTP delete is the Apache connector's (the FileStoreEraser seam); this module owns the
// DECISION and the receipt — the connector never decides (the open-core boundary, LICENSING.md).
//
// HONESTY (docs/SECURITY-HARDENING.md): the Anthropic Files store carries NO data-subject metadata (only
// id/mime/size/created/scope). A per-subject RTBF therefore CANNOT select a subject's files
// from the store alone — so the RTBF erasure leg (erasure.go) DISCLOSES the store's presence
// and nature rather than fabricate a subject match or blind-purge unrelated data. The
// inventory DESCRIBES state; it does not promise instantaneous server-side erasure.

package compliance

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// errFileStoreNotWired is the honest default-seam error (the governed Files plane is inert).
var errFileStoreNotWired = errors.New("compliance: Files-store eraser not wired")

const (
	// actionClaudeFilesErase is the governed delete capability. The ".erase" suffix makes it
	// CRITICAL in risktier.go ⇒ a dual-control floor of 2 distinct humans, no break-glass.
	actionClaudeFilesErase = "compliance.claude_files.erase"
	// claudeFileSubjectKind is the hold/audit subject kind for one Files-store object.
	claudeFileSubjectKind = "claude.file"
	// claudeFileTargetKind is the ledger target kind of an erasure receipt.
	claudeFileTargetKind model.Kind = "claude.file"

	// findingFilesPosture is the standing posture the inventory emits about the store.
	findingFilesPosture = "claude_files_store_posture"

	// filesStoreDisclosure is the honest, non-sensitive description of the Anthropic Files
	// store the inventory + RTBF disclosure carry (verified jun-2026).
	filesStoreDisclosure = "Anthropic Files store: workspace-scoped, SHARED across the workspace's API keys, " +
		"PERSISTENT (retained until explicitly deleted), and NOT zero-data-retention. It carries no " +
		"data-subject metadata, so per-subject RTBF cannot select a subject's files from the store alone — " +
		"use the governed point delete (or a scope-resolved purge) for files linked to a subject."
)

// filesInventoryDTO is the observe inventory response (minimal-data: refs + counts, never
// content or filenames). Wired=false ⇒ the plane is not configured (the disclosure still
// describes the store's nature for the auditor).
type filesInventoryDTO struct {
	Wired      bool      `json:"wired"`
	Count      int       `json:"count"`
	TotalBytes int64     `json:"total_bytes"`
	Disclosure string    `json:"disclosure"`
	Files      []FileRef `json:"files,omitempty"`
}

// fileEraseResultDTO is the governed-delete outcome. Status ∈ deleted | held | pending |
// denied | failed | not_wired | error; the HTTP status mirrors it (200/423/202/403/502/503).
type fileEraseResultDTO struct {
	Status         string    `json:"status"`
	FileID         string    `json:"file_id"`
	ConfirmationID string    `json:"confirmation_id,omitempty"`
	ApprovalRef    string    `json:"approval_ref,omitempty"`
	Holds          []HoldRef `json:"holds,omitempty"`
	Detail         string    `json:"detail,omitempty"`
}

// filesInventory enumerates the store (minimal-data) and emits a posture finding. It NEVER
// fabricates: an un-wired plane reports Wired=false with the disclosure intact.
func (m *Module) filesInventory(ctx context.Context, tenant model.TenantID) (filesInventoryDTO, error) {
	dto := filesInventoryDTO{Disclosure: filesStoreDisclosure}
	if !m.fileEraser.Wired() {
		return dto, nil
	}
	refs, err := m.fileEraser.ListFiles(ctx, tenant, "")
	if err != nil {
		return filesInventoryDTO{}, err
	}
	dto.Wired = true
	dto.Count = len(refs)
	dto.Files = refs
	for _, r := range refs {
		dto.TotalBytes += r.SizeBytes
	}
	// Posture (INFO): a continuous attestation of the store's nature + size, on the bus/SIEM.
	m.publishFindings(ctx, tenant, []sdkmodel.FindingReport{{
		Kind:        findingFilesPosture,
		Severity:    sdkmodel.SeverityInfo,
		SubjectKind: "anthropic.files",
		SubjectRef:  "workspace-store",
		Title:       "Claude Files store inventory: persistent, non-ZDR, shared across keys",
		DetailHash:  hashHex(filesStoreDisclosure),
		OccurredAt:  m.clock.Now().Time(),
	}})
	return dto, nil
}

// eraseClaudeFile performs the GOVERNED delete of one file: hold gate (deny-closed,
// re-checked at the destructive boundary) → CRITICAL dual-control approval (no break-glass,
// independently quorum-verified) → connector delete → sealed receipt. It returns the result
// and the HTTP status that mirrors it.
func (m *Module) eraseClaudeFile(ctx context.Context, mc api.ModuleContext, fileID, reason string) (fileEraseResultDTO, int) {
	res := fileEraseResultDTO{FileID: fileID}
	if !m.fileEraser.Wired() {
		res.Status, res.Detail = "not_wired", "the governed Files plane is not configured (deny-closed)"
		return res, http.StatusServiceUnavailable
	}

	// 1. Legal-hold gate (Sedona both-must-clear). A tenant-wide OR this-file hold blocks
	//    the delete; a read error is DENY-CLOSED (a hold that cannot be ruled out preserves).
	if dec, err := m.CheckHold(ctx, mc.Tenant, HoldSubject{Kind: claudeFileSubjectKind, Ref: fileID}); err != nil {
		res.Status, res.Detail = "error", "legal-hold check failed (deny-closed)"
		return res, http.StatusServiceUnavailable
	} else if dec.Held {
		res.Status, res.Holds, res.Detail = "held", dec.Holds, "file is under an active legal hold; deletion denied"
		return res, http.StatusLocked
	}

	// 2. CRITICAL dual-control approval (no break-glass). The gate reaches another module,
	//    so it runs OUTSIDE any store transaction. PlanHash binds the approval to THIS file id.
	gdec, gerr := m.gate.Authorize(ctx, mc.Tenant, GateRequest{
		Action: actionClaudeFilesErase, SubjectKind: claudeFileSubjectKind, SubjectRef: fileID,
		PlanHash: filePlanHash(fileID), Reason: firstNonEmpty(reason, "delete Claude Files object "+fileID),
		RequestedBy: mc.Principal.Actor(),
	})
	if gerr != nil {
		res.Status, res.Detail = "error", "could not consult the approval gate"
		return res, http.StatusInternalServerError
	}
	res.ApprovalRef = gdec.ApprovalRef
	switch gdec.Status {
	case GateStatusApproved:
		// Anti-TOCTOU: the approval must be bound to THIS file's plan. The adapter returns the
		// hash the approval was ACTUALLY stored against (never the echoed request), so a
		// mismatch is a re-scoped / un-approved change — deny (the holds/retention/erasure
		// sibling guard: holds.go, retention.go, erasure.go all re-verify the plan hash here).
		if gdec.PlanHash != filePlanHash(fileID) {
			res.Status, res.Detail = "denied", "approval is not bound to this file (plan hash mismatch)"
			return res, http.StatusForbidden
		}
		// Defense in depth: re-verify ≥2 distinct approvers even though the gate floored it.
		if !filesQuorumOK(gdec) {
			res.Status, res.Detail = "denied", "approval lacks dual-control quorum evidence"
			return res, http.StatusForbidden
		}
	case GateStatusPending:
		res.Status, res.Detail = "pending", "awaiting dual-control approval"
		return res, http.StatusAccepted
	default:
		res.Status, res.Detail = "denied", "approval not granted (status="+gdec.Status+")"
		return res, http.StatusForbidden
	}

	// 3. Re-check the hold at the DESTRUCTIVE boundary (a hold may have been placed during the
	//    approval-gathering window — the both-must-clear rule, the recheckHoldsForLeg pattern).
	if dec, err := m.CheckHold(ctx, mc.Tenant, HoldSubject{Kind: claudeFileSubjectKind, Ref: fileID}); err != nil {
		res.Status, res.Detail = "error", "legal-hold re-check failed (deny-closed)"
		return res, http.StatusServiceUnavailable
	} else if dec.Held {
		res.Status, res.Holds, res.Detail = "held", dec.Holds, "file became legally held during approval; deletion denied"
		return res, http.StatusLocked
	}

	// 4. Execute the delete (the connector via the seam) and seal the receipt.
	confID, derr := m.fileEraser.DeleteFile(ctx, mc.Tenant, fileID)
	if derr != nil {
		res.Status, res.Detail = "failed", "provider deletion failed"
		return res, http.StatusBadGateway
	}
	res.ConfirmationID = confID
	m.sealFileEraseReceipt(ctx, mc, fileID, confID, gdec.ApprovalRef)
	res.Status = "deleted"
	return res, http.StatusOK
}

// sealFileEraseReceipt anchors the deletion to the tamper-evident ledger — the receipt is
// the COMPLIANCE plane's, not the connector's. Best-effort + LOUD: a failed seal is an
// evidence gap (the delete already happened), logged, never swallowed.
func (m *Module) sealFileEraseReceipt(ctx context.Context, mc api.ModuleContext, fileID, confID, approvalRef string) {
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: "compliance.claude_files.erased", TargetKind: claudeFileTargetKind, TargetID: model.ID(fileID),
			PayloadHash: fileEraseReceiptHash(fileID, confID, approvalRef),
			Meta:        map[string]any{"file_id": fileID, "confirmation_id": confID, "approval_ref": approvalRef, "store": "anthropic_files"},
		})
		return aerr
	})
	if err != nil && m.log != nil {
		m.log.Error("compliance: Files erasure receipt NOT sealed (evidence gap)", "file_id", fileID, "err", err)
	}
}

// fileStoreDisclosureNote is the minimal-data RTBF disclosure of the Files store (count
// only — never ids/filenames): the store is not subject-indexed, so the per-subject sweep
// cannot select a subject's files; the operator must use the governed point delete.
func (m *Module) fileStoreDisclosureNote(ctx context.Context, tenant model.TenantID) string {
	refs, err := m.fileEraser.ListFiles(ctx, tenant, "")
	if err != nil {
		return "files store present; not subject-indexed (enumeration unavailable); use the governed point delete"
	}
	return "files store present: " + itoa(int64(len(refs))) + " object(s); NOT subject-indexed — per-subject " +
		"RTBF cannot select them; use the governed point delete or a scope-resolved purge"
}

// filePlanHash binds the approval to THIS file id (anti-TOCTOU): the two humans approve the
// deletion of this exact object, not a mutable reference.
func filePlanHash(fileID string) string { return hashHex("claude_file_erase|" + fileID) }

// fileEraseReceiptHash is the receipt's PayloadHash preimage (domain-separated; minimal-data).
func fileEraseReceiptHash(fileID, confID, approvalRef string) []byte {
	h := sha256.Sum256([]byte("olivares.compliance.claude_files.erase.v1|" + fileID + "|" + confID + "|" + approvalRef))
	return h[:]
}

// filesQuorumOK reports ≥2 distinct approving HUMANS (the CRITICAL floor, re-verified).
// It counts people, never the credentials they used: Actor() renders "user:<id>" for a
// session and "token:<id>" for a token, so counting actor strings would let one human
// approve the irreversible deletion of a customer file twice and clear a two-human bar.
func filesQuorumOK(dec GateDecision) bool { return dec.Quorum() >= 2 }

// firstNonEmpty returns a if non-blank, else b.
func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// ---- HTTP face -------------------------------------------------------------------------

// handleFilesInventory serves the observe inventory (read-tier).
func (m *Module) handleFilesInventory(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	dto, err := m.filesInventory(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// eraseFileRequest is the optional reason carried with a governed delete.
type eraseFileRequest struct {
	Reason string `json:"reason"`
}

// handleEraseFile serves the governed point delete (admin-tier; the dual-control gate + hold
// gate + receipt are inside eraseClaudeFile).
func (m *Module) handleEraseFile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	fileID := strings.TrimSpace(chi.URLParam(r, "id"))
	if fileID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("missing file id"))
		return
	}
	var req eraseFileRequest
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	res, status := m.eraseClaudeFile(r.Context(), mc, fileID, clamp(strings.TrimSpace(req.Reason), maxNoteLen))
	writeJSON(w, status, res)
}
