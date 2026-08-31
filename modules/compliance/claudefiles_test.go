// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// stubFileEraser is a programmable FileStoreEraser: tests set the store contents and inspect
// what was deleted, proving the GOVERNED decision (hold gate, dual-control, receipt) gates the
// connector I/O.
type stubFileEraser struct {
	mu      sync.Mutex
	wired   bool
	files   []FileRef
	deleted []string
	listErr error
	delErr  error
	confID  string
}

func (s *stubFileEraser) Wired() bool { return s.wired }

func (s *stubFileEraser) ListFiles(context.Context, model.TenantID, string) ([]FileRef, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.files, nil
}

func (s *stubFileEraser) DeleteFile(_ context.Context, _ model.TenantID, id string) (string, error) {
	if s.delErr != nil {
		return "", s.delErr
	}
	s.mu.Lock()
	s.deleted = append(s.deleted, id)
	s.mu.Unlock()
	if s.confID != "" {
		return s.confID, nil
	}
	return id, nil
}

func (s *stubFileEraser) deletedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleted...)
}

func filesTestSetup(t *testing.T, gate *stubApprovalGate, fe FileStoreEraser) (*harness, string, model.TenantID, map[string]string) {
	t.Helper()
	opts := []Option{}
	if gate != nil {
		opts = append(opts, WithApprovalGate(gate))
	}
	if fe != nil {
		opts = append(opts, WithFileStoreEraser(fe))
	}
	h := newHarness(t, opts...)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "files")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	return h, owner, tenant, tenantHdr(tenant)
}

func TestClaudeFilesInventoryAndPosture(t *testing.T) {
	fe := &stubFileEraser{wired: true, files: []FileRef{
		{ID: "file_1", MimeType: "application/pdf", SizeBytes: 10},
		{ID: "file_2", MimeType: "text/plain", SizeBytes: 20, ScopeID: "sess_9"},
	}}
	h, owner, _, hdr := filesTestSetup(t, nil, fe)

	r := h.do("GET", "/v1/m/compliance/claude-files", owner, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("inventory = %d %s", r.code, r.raw)
	}
	if r.body["wired"] != true || intOf(r.body["count"]) != 2 || intOf(r.body["total_bytes"]) != 30 {
		t.Fatalf("inventory body = %s", r.raw)
	}
	if d, _ := r.body["disclosure"].(string); !strings.Contains(d, "NOT zero-data-retention") {
		t.Errorf("inventory must disclose the store's non-ZDR nature; got %q", d)
	}
	// A posture finding (the continuous attestation) reached the bus.
	h.waitFindings()
	var posture bool
	for _, f := range h.deliveredFindings() {
		if f.Kind == findingFilesPosture {
			posture = true
		}
	}
	if !posture {
		t.Error("inventory must emit a Files-store posture finding")
	}
}

func TestClaudeFilesInventoryNotWired(t *testing.T) {
	h, owner, _, hdr := filesTestSetup(t, nil, nil) // no FileStoreEraser wired
	r := h.do("GET", "/v1/m/compliance/claude-files", owner, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("inventory = %d %s", r.code, r.raw)
	}
	if r.body["wired"] != false {
		t.Errorf("an un-wired plane must report wired=false; got %s", r.raw)
	}
	if d, _ := r.body["disclosure"].(string); d == "" {
		t.Error("the store disclosure must be present even when the plane is not wired")
	}
}

func TestClaudeFilesGovernedDeleteApprovedSealsReceipt(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-1", "alice", "bob") // dual-control quorum
	fe := &stubFileEraser{wired: true, confID: "provider-confirmation-7"}
	h, owner, tenant, hdr := filesTestSetup(t, gate, fe)

	r := h.do("POST", "/v1/m/compliance/claude-files/file_x/erase", owner, map[string]any{"reason": "DSR-7"}, hdr)
	if r.code != http.StatusOK || r.body["status"] != "deleted" {
		t.Fatalf("approved delete = %d %s", r.code, r.raw)
	}
	if r.body["confirmation_id"] != "provider-confirmation-7" {
		t.Errorf("confirmation_id = %v, want provider-confirmation-7", r.body["confirmation_id"])
	}
	if got := fe.deletedIDs(); len(got) != 1 || got[0] != "file_x" {
		t.Fatalf("connector delete = %v, want [file_x]", got)
	}
	// The receipt is sealed to the tamper-evident ledger by the COMPLIANCE plane.
	var sealed model.AuditEvent
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 0, func(ev model.AuditEvent) error {
			if ev.Action == "compliance.claude_files.erased" {
				sealed = ev
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk receipt ledger: %v", err)
	}
	if sealed.Seq == 0 {
		t.Error("a governed delete must seal a compliance.claude_files.erased receipt to the ledger")
	}
	// The sealed hash binds the actual file, provider confirmation and approval.
	// A constant-input receipt would survive one of these adversarial mutations.
	want := fileEraseReceiptHash("file_x", "provider-confirmation-7", "apr-1")
	if !bytes.Equal(sealed.PayloadHash, want) {
		t.Fatalf("sealed payload hash = %x, want real-input hash %x", sealed.PayloadHash, want)
	}
	for name, forged := range map[string][]byte{
		"file":         fileEraseReceiptHash("file_y", "provider-confirmation-7", "apr-1"),
		"confirmation": fileEraseReceiptHash("file_x", "conf-forged", "apr-1"),
		"approval":     fileEraseReceiptHash("file_x", "provider-confirmation-7", "apr-forged"),
	} {
		if bytes.Equal(want, forged) {
			t.Fatalf("%s mutation did not change the receipt hash", name)
		}
	}
	// The approval was bound to THIS file (anti-TOCTOU).
	reqs := gate.requests()
	if len(reqs) != 1 || reqs[0].Action != actionClaudeFilesErase || reqs[0].PlanHash != filePlanHash("file_x") {
		t.Errorf("gate request = %+v, want a file_x-bound %s", reqs, actionClaudeFilesErase)
	}
}

func TestClaudeFilesDeleteHoldBlocks(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-1", "alice", "bob")
	fe := &stubFileEraser{wired: true}
	h, owner, _, hdr := filesTestSetup(t, gate, fe)

	// A subject-scope legal hold on the file itself.
	if r := h.do("POST", "/v1/m/compliance/holds", owner, map[string]any{
		"matter_ref": "M-F", "scope_kind": "subject", "subject_kind": claudeFileSubjectKind, "subject_ref": "file_held",
		"reason": "litigation",
	}, hdr); r.code != http.StatusCreated {
		t.Fatalf("hold = %d %s", r.code, r.raw)
	}

	r := h.do("POST", "/v1/m/compliance/claude-files/file_held/erase", owner, nil, hdr)
	if r.code != http.StatusLocked || r.body["status"] != "held" {
		t.Fatalf("delete under hold = %d %s, want 423 held", r.code, r.raw)
	}
	if got := fe.deletedIDs(); len(got) != 0 {
		t.Fatalf("nothing must be deleted under a hold; deleted=%v", got)
	}
	// The hold gate runs FIRST — the approval gate is never consulted under a hold.
	if len(gate.requests()) != 0 {
		t.Error("the approval gate must not be consulted while a hold covers the file (hold-gate first)")
	}
}

func TestClaudeFilesDeletePlanHashMismatchDenied(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-1", "alice", "bob") // quorum OK…
	gate.mu.Lock()
	gate.planHash = "deadbeef" // …but bound to a DIFFERENT plan than this file's (anti-TOCTOU)
	gate.mu.Unlock()
	fe := &stubFileEraser{wired: true}
	h, owner, _, hdr := filesTestSetup(t, gate, fe)

	r := h.do("POST", "/v1/m/compliance/claude-files/file_x/erase", owner, nil, hdr)
	if r.code != http.StatusForbidden || !strings.Contains(r.raw, "plan hash") {
		t.Fatalf("an approval not bound to this file's plan must deny (403 plan hash); got %d %s", r.code, r.raw)
	}
	if got := fe.deletedIDs(); len(got) != 0 {
		t.Fatalf("a mis-bound approval must NOT delete (the file two humans approved is a different one); deleted=%v", got)
	}
}

func TestClaudeFilesDeleteSingleApproverDenied(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-1", "alice") // ONE approver — fails the CRITICAL floor
	fe := &stubFileEraser{wired: true}
	h, owner, _, hdr := filesTestSetup(t, gate, fe)

	r := h.do("POST", "/v1/m/compliance/claude-files/file_x/erase", owner, nil, hdr)
	if r.code != http.StatusForbidden || !strings.Contains(r.raw, "quorum") {
		t.Fatalf("single-approver delete = %d %s, want 403 quorum", r.code, r.raw)
	}
	if got := fe.deletedIDs(); len(got) != 0 {
		t.Fatalf("a sub-quorum approval must not delete; deleted=%v", got)
	}
}

func TestClaudeFilesDeletePendingAndNoGate(t *testing.T) {
	gate := &stubApprovalGate{}
	fe := &stubFileEraser{wired: true}
	h, owner, _, hdr := filesTestSetup(t, gate, fe)

	gate.set(GateStatusPending, "apr-1")
	if r := h.do("POST", "/v1/m/compliance/claude-files/file_x/erase", owner, nil, hdr); r.code != http.StatusAccepted || r.body["status"] != "pending" {
		t.Fatalf("pending delete = %d %s, want 202 pending", r.code, r.raw)
	}
	gate.set(GateStatusNoGate, "")
	if r := h.do("POST", "/v1/m/compliance/claude-files/file_x/erase", owner, nil, hdr); r.code != http.StatusForbidden {
		t.Fatalf("no-gate delete = %d %s, want 403 (deny-closed)", r.code, r.raw)
	}
	if got := fe.deletedIDs(); len(got) != 0 {
		t.Fatalf("nothing must be deleted without an approved quorum; deleted=%v", got)
	}
}

func TestClaudeFilesDeleteNotWired(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-1", "alice", "bob")
	h, owner, _, hdr := filesTestSetup(t, gate, nil) // plane not wired
	r := h.do("POST", "/v1/m/compliance/claude-files/file_x/erase", owner, nil, hdr)
	if r.code != http.StatusServiceUnavailable || r.body["status"] != "not_wired" {
		t.Fatalf("delete on an un-wired plane = %d %s, want 503 not_wired", r.code, r.raw)
	}
}

// TestErasureFilesStoreDisclosureLeg proves the RTBF sweep DISCLOSES the Files store (
// "BOTH"): a full erasure seals a files_store custody event, and — because the store has no
// data-subject metadata — the sweep deletes NOTHING from it (no blind purge of unrelated data;
// the governed point delete is the actuator).
func TestErasureFilesStoreDisclosureLeg(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	fe := &stubFileEraser{wired: true, files: []FileRef{{ID: "file_1", SizeBytes: 5}, {ID: "file_2", SizeBytes: 7}}}
	h := newHarness(t, WithApprovalGate(gate), WithFileStoreEraser(fe))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "rtbf")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-F",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if n := h.countCustody(tenant, id, erasureEventFiles); n != 1 {
		t.Fatalf("files_store disclosure custody events = %d, want 1", n)
	}
	if got := fe.deletedIDs(); len(got) != 0 {
		t.Fatalf("the RTBF sweep must NOT delete Files-store objects (no subject index); deleted=%v", got)
	}
}

func TestClaudeFilesDeleteFailClosedOnDeleteError(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-1", "alice", "bob")
	fe := &stubFileEraser{wired: true, delErr: errors.New("upstream 500")}
	h, owner, tenant, hdr := filesTestSetup(t, gate, fe)
	r := h.do("POST", "/v1/m/compliance/claude-files/file_x/erase", owner, nil, hdr)
	if r.code != http.StatusBadGateway || r.body["status"] != "failed" {
		t.Fatalf("delete with an upstream failure = %d %s, want 502 failed", r.code, r.raw)
	}
	for _, action := range h.auditActions(tenant) {
		if action == "compliance.claude_files.erased" {
			t.Fatal("provider failure sealed a complete Claude-file erasure receipt")
		}
	}
}
