// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// the tool-pin operator surface. The enterprise verifier detects MCP
// rug-pulls (fingerprint drift) but had NO reachable actuator: RecordPin had
// zero production callers, so with require_pin_approval=true approving a tool
// was literally impossible. These routes list pins with live drift and actuate
// approve/unpin against the verifier through the connector's ToolPinAdmin
// seam. The community build wires no admin and answers 501 honestly.

// WithToolPinAdmin injects the verifier's operator surface (nil = community).
func WithToolPinAdmin(admin mcpc.ToolPinAdmin) Option {
	return func(m *Module) { m.toolPins = admin }
}

type toolPinDTO struct {
	Tool        string `json:"tool"`
	Fingerprint string `json:"fingerprint"`
	PinnedAt    string `json:"pinned_at"`
	UpdatedAt   string `json:"updated_at"`
	PinCount    int    `json:"pin_count"`
	// Version is the CAS base version the caller MUST echo back as expected_version on
	// approve/unpin. It is NEVER omitempty, and the reason is the failure mode, not the
	// value: with omitempty a version of 0 drops the key, the client sends `undefined`,
	// and the write earns the very 400 this field exists to prevent. Whether 0 can occur
	// is up to the verifier — the engine's own persistence starts a fresh row at 1
	// (sqlstore assigns 1 on Create), but this DTO must not encode an assumption about an
	// implementation it does not own.
	Version          int64  `json:"version"`
	DriftFingerprint string `json:"drift_fingerprint,omitempty"`
	DriftAt          string `json:"drift_at,omitempty"`
}

type toolPinActionInput struct {
	Tool        string `json:"tool"`
	Fingerprint string `json:"fingerprint"`
	FromDrift   bool   `json:"from_drift"`
	// ExpectedVersion is the D-09 base-version CAS precondition the operator read from GET.
	// The durable apply proceeds only if the pin row is still at this version, so a racing
	// operator write cannot be silently overwritten. Required on every mutation (a nil
	// pointer is a 400) — an unconditional "latest wins" write is exactly the lost-update
	// hazard this closes. It is compared against the DURABLE row inside the apply
	// transaction (never an in-memory snapshot).
	ExpectedVersion *int64 `json:"expected_version"`
	// ExpectedDriftFingerprint is the D-09 drift CAS precondition: the exact drifted
	// fingerprint the operator reviewed (from GET). A from_drift approve REQUIRES it; the
	// apply pins this fingerprint only if the tool's CURRENT durable drift still equals it,
	// so a rug-pull racing the approval cannot legitimate a definition the operator never
	// saw. It is NOT re-derived from an in-memory Pins() snapshot (that read is the TOCTOU).
	ExpectedDriftFingerprint string `json:"expected_drift_fingerprint,omitempty"`
}

// handleListToolPins is GET /toolpins: this tenant's pins, drift included.
func (m *Module) handleListToolPins(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.toolPins == nil {
		writeError(w, http.StatusNotImplemented, "tool pinning is an enterprise add-on (no verifier wired)")
		return
	}
	tenant := mc.Tenant.String()
	out := []toolPinDTO{}
	for _, p := range m.toolPins.Pins() {
		if p.Server != tenant {
			continue
		}
		out = append(out, toToolPinDTO(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool < out[j].Tool })
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleApproveToolPin is POST /toolpins/approve: evidence-gated, CAS-guarded, idempotent
// approve for this tenant. FromDrift approves the fingerprint the operator reviewed via GET
// (supplied as expected_drift_fingerprint and confirmed against the durable row); otherwise
// the caller supplies the fingerprint explicitly. Requires an Idempotency-Key header and
// expected_version. Returns 202 (the durable apply/settle is authoritative).
func (m *Module) handleApproveToolPin(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.toolPins == nil {
		writeError(w, http.StatusNotImplemented, "tool pinning is an enterprise add-on (no verifier wired)")
		return
	}
	var in toolPinActionInput
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&in); err != nil || dec.More() || in.Tool == "" {
		writeError(w, http.StatusBadRequest, "invalid JSON body (tool is required)")
		return
	}
	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idemKey == "" {
		writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
		return
	}
	if in.ExpectedVersion == nil {
		writeError(w, http.StatusBadRequest, "expected_version is required")
		return
	}
	fingerprint := in.Fingerprint
	expectedDrift := in.ExpectedDriftFingerprint
	if in.FromDrift {
		// The operator approves EXACTLY the drifted fingerprint they reviewed via GET; the
		// durable CAS confirms the tool still shows it. The value comes from the client, not
		// an in-memory Pins() re-read (that read-then-write gap is the D-09 TOCTOU).
		if expectedDrift == "" {
			writeError(w, http.StatusBadRequest, "expected_drift_fingerprint is required for from_drift")
			return
		}
		fingerprint = expectedDrift
	}
	if fingerprint == "" {
		writeError(w, http.StatusBadRequest, "fingerprint or from_drift is required")
		return
	}
	m.applyPinChange(w, r, mc, idemKey, "approve", "capabilities.tool_pin.approve",
		in.Tool, fingerprint, *in.ExpectedVersion, expectedDrift, in.FromDrift)
}

// handleUnpinToolPin is POST /toolpins/unpin: revoke this tenant's pin.
func (m *Module) handleUnpinToolPin(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.toolPins == nil {
		writeError(w, http.StatusNotImplemented, "tool pinning is an enterprise add-on (no verifier wired)")
		return
	}
	var in toolPinActionInput
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&in); err != nil || dec.More() || in.Tool == "" {
		writeError(w, http.StatusBadRequest, "invalid JSON body (tool is required)")
		return
	}
	idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idemKey == "" {
		writeError(w, http.StatusBadRequest, "Idempotency-Key header is required")
		return
	}
	if in.ExpectedVersion == nil {
		writeError(w, http.StatusBadRequest, "expected_version is required")
		return
	}
	m.applyPinChange(w, r, mc, idemKey, "unpin", "capabilities.tool_pin.unpin",
		in.Tool, "", *in.ExpectedVersion, "", false)
}

// applyPinChange is the shared D-09 actuator for approve and unpin. It enforces
// evidence-or-refuse (anchor operator-attributed evidence-intent BEFORE any effect) and then
// hands the CAS-guarded, idempotent change to the enterprise durable applier. It NEVER
// returns 200-applied: the durable apply/settle is authoritative and asynchronous, so a
// success is 202-pending and the caller polls GET for the terminal apply_state.
//
// LIMITATION (community two-seam split): the evidence-intent commits in mc.Data (the engine
// ledger) and the durable state/operation/outbox commit in the applier's own store txn — two
// transactions, not the ONE the frozen contract's ideal names. The fully-atomic
// claim (dedup+CAS+anchor+desired-pending+outbox in a single transaction, so a retry never
// re-anchors) requires the pin durable base to live in this module's store alongside the
// applier+verifier, which are the ABSENT enterprise overlay. This ordering is still
// deny-closed (evidence before effect; a failed apply leaves an intent the verifier denies
// while pending/divergent), and the enterprise landing MUST fold the two into one txn — see
// sessions-d09-*.md for the required durable design and its UNVERIFIED harness.
func (m *Module) applyPinChange(w http.ResponseWriter, r *http.Request, mc api.ModuleContext,
	idemKey, action, auditAction, tool, desiredFingerprint string, expectedVersion int64, expectedDrift string, fromDrift bool) {

	// OperationID is server-minted, namespaced from {tenant, surface, action, Idempotency-
	// Key} — never the raw key. A retry with the SAME key yields the SAME OperationID so the
	// durable applier dedups it to the original outcome (sdk/evidence.go idempotency law).
	opID := m.deriveToolPinOperationID(mc, action, idemKey)
	binding := sdk.EvidenceBinding{
		OperationID:  opID,
		EffectDigest: m.toolPinEffectDigest(mc, action, tool, desiredFingerprint, expectedVersion, expectedDrift),
	}

	// Evidence-or-refuse (D-09, sdk/evidence.go): a pin change alters an authorization
	// baseline (docs/SECURITY-HARDENING.md), so operator-attributed evidence MUST anchor durably BEFORE the
	// effect. No anchor ⇒ no effect (deny-closed). This replaces the historical order (mutate
	// the pin, then DISCARD the audit error) that returned 200 on a failed/dropped append
	// while the baseline had already moved.
	receipt := m.auditToolPin(r, mc, binding, auditAction, tool, map[string]any{
		"action": action, "fingerprint": desiredFingerprint, "from_drift": fromDrift,
		"expected_version":           expectedVersion,
		"expected_drift_fingerprint": expectedDrift,
		"operation_id":               string(opID),
		"effect_digest":              string(binding.EffectDigest),
	})
	if receipt.MustRefuse(binding) {
		status, msg := statusForEvidenceFault(receipt.Fault)
		writeError(w, status, msg)
		return
	}

	// Evidence anchored → hand the change to the durable applier, CAS-guarded on the base
	// version (and, for from_drift, the exact reviewed drift) against the DURABLE row.
	res, err := m.toolPins.ApplyPinChange(r.Context(), mcpc.ToolPinChange{
		OperationID: string(opID), EffectDigest: string(binding.EffectDigest),
		Server: mc.Tenant.String(), Tool: tool, Action: action,
		DesiredFingerprint: desiredFingerprint, ExpectedVersion: expectedVersion,
		ExpectedDriftFingerprint: expectedDrift, EvidenceRef: receipt.EvidenceRef,
	})
	if err != nil {
		// State/policy denials (NOT evidence faults): map to 409 so the console refetches
		// and re-reviews. A different-digest replay of the same OperationID is FailureReplay.
		// Three DIFFERENT answers under one status, so each carries its own stable code:
		// the first two are concurrency the console resolves by refetching, the third is a
		// rebound idempotency key — a replay or a client bug — which must never be
		// presented to an operator as "somebody else moved the state".
		switch {
		case errors.Is(err, mcpc.ErrPinDriftChanged):
			writeErrorCode(w, http.StatusConflict, "pin_drift_changed",
				"tool drifted again since review; re-review the current fingerprint")
		case errors.Is(err, mcpc.ErrPinVersionConflict):
			writeErrorCode(w, http.StatusConflict, "pin_version_conflict",
				"pin state changed since your read; refetch and retry")
		case errors.Is(err, mcpc.ErrPinReplay):
			writeErrorCode(w, http.StatusConflict, "idempotency_key_reused",
				"idempotency key reused for a different change")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	// 202, not 200: the durable apply is authoritative once settled; the verifier denies the
	// tool while apply_state is pending/divergent.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"tool": tool, "operation_id": res.OperationID,
		"apply_state": res.AppliedState, "version": res.StateVersion,
		"evidence_ref": receipt.EvidenceRef,
	})
}

// auditToolPin durably anchors the operator's pin-change INTENT to the tenant ledger with
// the REAL actor and returns the evidence receipt for the exact effect. It is the D-09
// evidence-or-refuse gate: pin changes alter an authorization baseline (docs/SECURITY-HARDENING.md), so the
// caller MUST refuse the effect unless the returned receipt AnchoredFor(binding).
//
// It follows the F9 anchoring discipline (sdk/evidence.go): append INSIDE the
// transaction, but NEVER return a sentinel from inside on a degrade drop — that would roll
// back the loss accounting the store just committed (audit_spool_gaps), so the gap counter
// never advances and its signed marker never seals. Capture the drop, commit (return nil),
// and classify AFTER. On a real (block-mode / write) error the callback returns it, rolling
// back — nothing durable, deny-closed. The ledger error is never discarded (the historical
// `_ = mc.Data.Mutate(...)` fail-open this replaces).
func (m *Module) auditToolPin(r *http.Request, mc api.ModuleContext, binding sdk.EvidenceBinding, action, tool string, meta map[string]any) sdk.EvidenceReceipt {
	if meta == nil {
		meta = map[string]any{}
	}
	var appendDropped bool
	var evidenceRef string
	txErr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		ev, err := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			Action: action, TargetKind: model.Kind("mcp.tool"), TargetID: model.ID(tool),
			Meta: meta,
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
	return sdk.ClassifyAnchor(binding, evidenceRef, appendDropped, classifyStoreFault(txErr))
}

// deriveToolPinOperationID mints the D-09 OperationID: a stable, single-use idempotency
// identity namespaced from {tenant, module surface, action, Idempotency-Key} — never the raw
// key and never the effect digest (sdk/evidence.go). The SAME key on the same action/tenant
// yields the SAME OperationID, so a retry dedups to the original outcome at the durable
// applier; a new operator action supplies a new key and gets a new OperationID.
func (m *Module) deriveToolPinOperationID(mc api.ModuleContext, action, idemKey string) sdk.OperationID {
	h := sha256.New()
	h.Write([]byte("toolpin-op-v1\n"))
	for _, s := range []string{mc.Tenant.String(), Namespace, action, idemKey} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return sdk.OperationID(hex.EncodeToString(h.Sum(nil)))
}

// toolPinEffectDigest is the D-09 EffectDigest: a domain-separated digest over the FULL
// effect binding — tenant, module surface, action, tool, desired fingerprint, expected base
// version, expected drift, and the resolved actor — so a receipt minted for one pin change
// can never green-light a different one, and reusing an Idempotency-Key for a DIFFERENT
// change is detected as a replay (different digest under the same OperationID; sdk/evidence.go
// confused-deputy + replay defenses).
func (m *Module) toolPinEffectDigest(mc api.ModuleContext, action, tool, fingerprint string, expectedVersion int64, expectedDrift string) sdk.EffectDigest {
	h := sha256.New()
	h.Write([]byte("toolpin-effect-v1\n"))
	for _, s := range []string{
		mc.Tenant.String(), Namespace, action, tool, fingerprint,
		strconv.FormatInt(expectedVersion, 10), expectedDrift,
		mc.Principal.Actor(), mc.Principal.ActorKind(),
	} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return sdk.EffectDigest(hex.EncodeToString(h.Sum(nil)))
}

// classifyStoreFault maps a raw ledger transaction error to the evidence fault taxonomy
// (sdk/evidence.go). Called only when the transaction FAILED (a nil error classifies as
// EvidenceFaultNone via the default), so ClassifyAnchor can build the refused receipt.
func classifyStoreFault(err error) sdk.EvidenceFault {
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

// statusForEvidenceFault maps a refused evidence receipt to the HTTP response. The transient
// ledger faults (spool exhausted/degraded, non-leader) are 503 — deny-closed and retryable
// once capacity/leadership is restored, matching core/api's audit_spool_full mapping. A
// genuine write fault is a 500 (an unexpected bug, not a transient capacity state).
func statusForEvidenceFault(f sdk.EvidenceFault) (int, string) {
	switch f {
	case sdk.EvidenceFaultSpoolFull, sdk.EvidenceFaultSpoolDegraded, sdk.EvidenceFaultLedgerUnavailable:
		return http.StatusServiceUnavailable, "evidence ledger unavailable; pin change refused (deny-closed)"
	default:
		return http.StatusInternalServerError, "pin change refused: evidence could not be anchored"
	}
}

// toToolPinDTO projects the seam snapshot onto the wire. Version travels with it because
// the GET is the only place a client can learn the CAS precondition the two POSTs demand
// (toolPinActionInput.ExpectedVersion): a required precondition the read never hands back
// is unobtainable, and both verbs then answer 400 no matter what the caller does.
func toToolPinDTO(p mcpc.PinSnapshot) toolPinDTO {
	dto := toolPinDTO{
		Tool: p.Tool, Fingerprint: p.Fingerprint,
		PinnedAt:  p.PinnedAt.UTC().Format(time.RFC3339),
		UpdatedAt: p.UpdatedAt.UTC().Format(time.RFC3339),
		PinCount:  p.PinCount,
		Version:   p.Version,
	}
	if p.DriftFingerprint != "" {
		dto.DriftFingerprint = p.DriftFingerprint
		dto.DriftAt = p.DriftAt.UTC().Format(time.RFC3339)
	}
	return dto
}
