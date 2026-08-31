// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Behavior contracts: the crypto-shred primitive (a shredded token is
// permanently unintelligible), the DSR workflow's two deny-closed gates in fixed
// order (hold-gate 423 BEFORE the CRITICAL dual-control approval), the physical
// erasure of every registered target, the post-erasure LIVE chain verification
// (the bar: the ledger never breaks), the honest receipt (gaps recorded,
// provider floor disclosed, reconciliation embedded) and the RBAC tiers.

// stubAccountEraser is a programmable account leg.
type stubAccountEraser struct {
	mu      sync.Mutex
	outcome AccountEraseOutcome
	err     error
	calls   [][]string
}

func (s *stubAccountEraser) EraseAccount(_ context.Context, _ model.TenantID, refs []string, _, _ string) (AccountEraseOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]string(nil), refs...))
	return s.outcome, s.err
}

// stubProviderEraser is a programmable provider leg.
type stubProviderEraser struct {
	mu         sync.Mutex
	outcome    ProviderEraseOutcome
	err        error
	reqs       []ProviderEraseRequest
	afterErase func(context.Context, model.TenantID) error
}

func (s *stubProviderEraser) EraseProviderContent(ctx context.Context, tenant model.TenantID, req ProviderEraseRequest) (ProviderEraseOutcome, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	outcome, err, afterErase := s.outcome, s.err, s.afterErase
	s.mu.Unlock()
	if err != nil {
		return outcome, err
	}
	if afterErase != nil {
		if err := afterErase(ctx, tenant); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

type stubCryptoShredCoordinator struct {
	readiness      CryptoShredReadiness
	verify         CryptoShredVerification
	err            error
	readinessCalls int
	notifyCalls    int
	verifyCalls    int
	// v2 contract evidence: what the module actually handed the coordinator.
	tenantSeen    string
	probeKeyGone  bool
	probeScanned  int
	probeResidues []string
	probeErr      error
}

func (s *stubCryptoShredCoordinator) ValidateShredReadiness(_ context.Context, tenant, _, _ string) (CryptoShredReadiness, error) {
	s.readinessCalls++
	s.tenantSeen = tenant
	if s.err != nil {
		return CryptoShredReadiness{}, s.err
	}
	if !s.readiness.Ready && len(s.readiness.Blockers) == 0 {
		return CryptoShredReadiness{Ready: true, PolicyApplied: "test"}, nil
	}
	return s.readiness, nil
}

func (s *stubCryptoShredCoordinator) NotifyWORMSinks(context.Context, string, time.Time) error {
	s.notifyCalls++
	return s.err
}

func (s *stubCryptoShredCoordinator) VerifyShredCompleteness(ctx context.Context, _ string, _ []string, probes CryptoShredProbes) (CryptoShredVerification, error) {
	s.verifyCalls++
	// Execute the evidence probes the module bound over its shred transaction —
	// the test then asserts they observed a REAL destroyed key and a REAL scan.
	if probes.KeyGone != nil {
		gone, err := probes.KeyGone(ctx)
		s.probeKeyGone, s.probeErr = gone, err
	}
	if probes.ResidualScan != nil {
		residues, scanned, err := probes.ResidualScan(ctx)
		s.probeResidues, s.probeScanned = residues, scanned
		if err != nil && s.probeErr == nil {
			s.probeErr = err
		}
	}
	if s.err != nil {
		return CryptoShredVerification{}, s.err
	}
	if !s.verify.Complete && !s.verify.KeyDestroyed && !s.verify.WORMNotified && s.verify.ResidualScan.ScanDepth == "" {
		clean := s.probeErr == nil && len(s.probeResidues) == 0
		return CryptoShredVerification{
			Complete: s.probeKeyGone && clean, KeyDestroyed: s.probeKeyGone, WORMNotified: true,
			ResidualScan: CryptoShredResidualScan{
				ScanDepth: "test", TargetsScanned: s.probeScanned,
				ResiduesFound: len(s.probeResidues), Residues: append([]string(nil), s.probeResidues...), Clean: clean,
			},
			PolicyApplied: "test",
		}, nil
	}
	return s.verify, nil
}

type reflectCryptoShredCoordinator struct {
	readinessCalls int
	notifyCalls    int
	verifyCalls    int
}

type reflectShredReadiness struct {
	Ready         bool
	Blockers      []reflectShredBlocker
	Warnings      []string
	PolicyApplied string
}

type reflectShredBlocker struct {
	Kind   string
	Detail string
}

type reflectShredVerification struct {
	Complete      bool
	KeyDestroyed  bool
	WORMNotified  bool
	ResidualScan  reflectResidualScanResult
	Unverified    []string
	PolicyApplied string
}

type reflectResidualScanResult struct {
	ScanDepth      string
	TargetsScanned int
	ResiduesFound  int
	Residues       []string
	Clean          bool
}

func (s *reflectCryptoShredCoordinator) ValidateShredReadiness(_ context.Context, tenant, _, _ string) (*reflectShredReadiness, error) {
	s.readinessCalls++
	if tenant == "" {
		return &reflectShredReadiness{
			Blockers:      []reflectShredBlocker{{Kind: "config", Detail: "no tenant supplied to readiness check"}},
			PolicyApplied: "AES-256-GCM/deep",
		}, nil
	}
	return &reflectShredReadiness{
		Ready:         true,
		Warnings:      []string{"reflect adapter warning"},
		PolicyApplied: "AES-256-GCM/deep",
	}, nil
}

func (s *reflectCryptoShredCoordinator) NotifyWORMSinks(context.Context, string, time.Time) error {
	s.notifyCalls++
	return nil
}

func (s *reflectCryptoShredCoordinator) VerifyShredCompleteness(ctx context.Context, _ string, _ []string, probes CryptoShredProbes) (*reflectShredVerification, error) {
	s.verifyCalls++
	// The reflect adapter must deliver WORKING probes: verify against them for
	// real instead of assuming (the coordinator contract this fake mirrors).
	keyGone := false
	if probes.KeyGone != nil {
		var err error
		if keyGone, err = probes.KeyGone(ctx); err != nil {
			return nil, err
		}
	}
	scan := reflectResidualScanResult{ScanDepth: "deep"}
	if probes.ResidualScan != nil {
		residues, scanned, err := probes.ResidualScan(ctx)
		if err != nil {
			return nil, err
		}
		scan.TargetsScanned = scanned
		scan.ResiduesFound = len(residues)
		scan.Residues = residues
		scan.Clean = len(residues) == 0
	}
	return &reflectShredVerification{
		Complete:      keyGone && scan.Clean,
		KeyDestroyed:  keyGone,
		WORMNotified:  true,
		ResidualScan:  scan,
		PolicyApplied: "AES-256-GCM/deep",
	}, nil
}

// seedSubjectRows plants PII-bearing rows across the registered erasure targets for
// an agent-kind subject plus unrelated rows that must SURVIVE the erasure.
func seedSubjectRows(h *harness, tenant model.TenantID, agentRef string) {
	h.mutate(tenant, func(sc store.Scope) error {
		mem, err := sc.Ext(knowledgeMemoryStandInKind)
		if err != nil {
			return err
		}
		if _, err := mem.Create(context.Background(), model.Record{
			"agent_ref": agentRef, "mkey": "pref", "content": "maria@example.com likes terse answers",
		}); err != nil {
			return err
		}
		if _, err := mem.Create(context.Background(), model.Record{
			"agent_ref": "agent-other", "mkey": "pref", "content": "unrelated",
		}); err != nil {
			return err
		}
		live, err := sc.Ext(sessionsLiveStandInKind)
		if err != nil {
			return err
		}
		if _, err := live.Create(context.Background(), model.Record{
			"session_ref": "sess-1", "agent_ref": agentRef, "event_count": int64(3),
		}); err != nil {
			return err
		}
		if _, err := live.Create(context.Background(), model.Record{
			"session_ref": "sess-2", "agent_ref": "agent-other", "event_count": int64(1),
		}); err != nil {
			return err
		}
		voice, err := sc.Ext(voiceSessionStandInKind)
		if err != nil {
			return err
		}
		_, err = voice.Create(context.Background(), model.Record{
			"session_ref": "vsess-1", "agent_ref": agentRef, "duration_ms": int64(900),
		})
		return err
	})
}

func TestErasureTokenShredUnrecoverable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "shred")

	var key subjectKey
	var token string
	h.mutate(tenant, func(sc store.Scope) error {
		var err error
		key, err = mintSubjectKey(context.Background(), sc, "user", "maria@example.com", []string{"user:abc"}, "user:test")
		if err != nil {
			return err
		}
		token, err = sealSubjectToken(tenant, key, "maria@example.com")
		return err
	})
	if !strings.HasPrefix(token, piiTokenPrefix) {
		t.Fatalf("token %q lacks the pii prefix", token)
	}
	if strings.Contains(token, "maria") {
		t.Fatalf("token leaks the plaintext subject: %q", token)
	}

	// Opens while the key lives; a second seal of the same plaintext differs (fresh nonce).
	h.mutate(tenant, func(sc store.Scope) error {
		pt, err := openSubjectToken(context.Background(), sc, tenant, token)
		if err != nil || pt != "maria@example.com" {
			t.Fatalf("open = %q, %v", pt, err)
		}
		again, err := sealSubjectToken(tenant, key, "maria@example.com")
		if err != nil {
			return err
		}
		if again == token {
			t.Fatal("tokens must be non-deterministic (fresh nonce per seal)")
		}
		return nil
	})

	// Shred ⇒ the token is permanently unintelligible, by key destruction alone.
	h.mutate(tenant, func(sc store.Scope) error {
		return shredSubjectKey(context.Background(), sc, key.ID)
	})
	h.mutate(tenant, func(sc store.Scope) error {
		_, err := openSubjectToken(context.Background(), sc, tenant, token)
		if !errors.Is(err, ErrKeyShredded) {
			t.Fatalf("post-shred open = %v, want ErrKeyShredded", err)
		}
		return nil
	})
}

func TestErasureRequestTokenizesAndDedupes(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "dsr")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-7", "reason": "ccpa request",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if got := r.body["subject"]; got != "agent-1" {
		t.Fatalf("subject (while key lives) = %v", got)
	}
	if tok := r.body["subject_token"].(string); !strings.HasPrefix(tok, piiTokenPrefix) || strings.Contains(tok, "agent-1") {
		t.Fatalf("subject_token = %q", tok)
	}

	// The request row carries no plaintext subject also encrypts the subject
	// key row's data-plane payload, leaving only a lookup digest in subject_ref.
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(erasureRequestKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(context.Background(), model.ID(id))
		if err != nil {
			return err
		}
		for col, v := range rec {
			if s, ok := v.(string); ok && col != colERKeyID && strings.Contains(s, "agent-1") {
				t.Fatalf("request column %s leaks the subject: %q", col, s)
			}
		}
		keyRepo, err := sc.Ext(subjectKeyKind)
		if err != nil {
			return err
		}
		keyRec, err := keyRepo.Get(context.Background(), model.ID(rec.String(colERKeyID)))
		if err != nil {
			return err
		}
		if got := keyRec.String(colSKSubjectRef); strings.Contains(got, "agent-1") || !strings.HasPrefix(got, "sha256:") {
			t.Fatalf("subject key lookup = %q, want digest without plaintext", got)
		}
		for col, v := range keyRec {
			if s, ok := v.(string); ok && strings.Contains(s, "agent-1") {
				t.Fatalf("subject key column %s leaks the subject: %q", col, s)
			}
		}
		return nil
	})

	// A second DSR for the same live subject reuses the workflow: 409.
	if r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-8",
	}, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("duplicate create = %d %s", r.code, r.raw)
	}

	// Identity refs are rejected over-length, never clamped.
	if r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": strings.Repeat("x", maxRefLen+1), "case_ref": "DSR-9",
	}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("oversized ref = %d %s", r.code, r.raw)
	}
}

func TestErasureHoldBlocksExecute(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-1", "alice", "bob")
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "heldrtbf")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	// A subject-scope hold on the data subject.
	if r := h.do("POST", "/v1/m/compliance/holds", owner, map[string]any{
		"matter_ref": "M-1", "scope_kind": "subject", "subject_kind": "agent", "subject_ref": "agent-1",
		"reason": "litigation",
	}, hdr); r.code != http.StatusCreated {
		t.Fatalf("hold = %d %s", r.code, r.raw)
	}
	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-1",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	// Execute under the hold: the EXACT 423 body, BEFORE any approval.
	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	if r.code != http.StatusLocked {
		t.Fatalf("execute under hold = %d %s", r.code, r.raw)
	}
	errBody, _ := r.body["error"].(map[string]any)
	if errBody == nil || errBody["code"] != "legal_hold" || errBody["message"] != "blocked by an active legal hold" {
		t.Fatalf("423 body = %s", r.raw)
	}
	if holds, _ := errBody["holds"].([]any); len(holds) != 1 {
		t.Fatalf("423 holds = %s", r.raw)
	}
	if len(gate.requests()) != 0 {
		t.Fatal("the approval gate must not be consulted while a hold covers the subject (hold-gate FIRST)")
	}
	if r := h.do("GET", "/v1/m/compliance/erasure/"+id, owner, nil, hdr); r.body["status"] != erasureStatusBlocked {
		t.Fatalf("status = %v", r.body["status"])
	}

	// A class-scope hold blocks the same way (the registry maps agent ⇒ agent.memory).
	h2 := newHarness(t, WithApprovalGate(gate))
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "heldcls")
	owner2 := h2.roleToken(admin2, tenant2, "owner@x.io", "owner")
	hdr2 := tenantHdr(tenant2)
	if r := h2.do("POST", "/v1/m/compliance/holds", owner2, map[string]any{
		"matter_ref": "M-2", "scope_kind": "data_class", "data_class": "agent.memory", "reason": "audit",
	}, hdr2); r.code != http.StatusCreated {
		t.Fatalf("class hold = %d %s", r.code, r.raw)
	}
	r = h2.do("POST", "/v1/m/compliance/erasure", owner2, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-9", "case_ref": "DSR-2",
	}, hdr2)
	id2 := r.body["id"].(string)
	if r := h2.do("POST", "/v1/m/compliance/erasure/"+id2+"/execute", owner2, nil, hdr2); r.code != http.StatusLocked {
		t.Fatalf("execute under class hold = %d %s", r.code, r.raw)
	}
}

func TestErasureApprovalFlowDenyClosed(t *testing.T) {
	gate := &stubApprovalGate{}
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "gates")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-3",
	}, hdr)
	id := r.body["id"].(string)
	exec := func() resp { return h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr) }

	// Pending: 202 + ONE custody event per approval, idempotent under re-poll.
	gate.set(GateStatusPending, "apr-1")
	if r := exec(); r.code != http.StatusAccepted {
		t.Fatalf("pending = %d %s", r.code, r.raw)
	}
	if r := exec(); r.code != http.StatusAccepted {
		t.Fatalf("pending re-poll = %d %s", r.code, r.raw)
	}
	if n := h.countCustody(tenant, id, erasureEventApprovalRq); n != 1 {
		t.Fatalf("approval_requested custody events = %d, want 1", n)
	}

	// Approved with ONE approver: denied (quorum re-verified independently).
	gate.set(GateStatusApproved, "apr-1", "alice")
	if r := exec(); r.code != http.StatusForbidden || !strings.Contains(r.raw, "quorum") {
		t.Fatalf("single approver = %d %s", r.code, r.raw)
	}

	// Approved but bound to a DIFFERENT plan: denied (anti-TOCTOU).
	gate.mu.Lock()
	gate.planHash = "deadbeef"
	gate.mu.Unlock()
	gate.set(GateStatusApproved, "apr-1", "alice", "bob")
	if r := exec(); r.code != http.StatusForbidden || !strings.Contains(r.raw, "plan hash") {
		t.Fatalf("plan mismatch = %d %s", r.code, r.raw)
	}
	gate.mu.Lock()
	gate.planHash = ""
	gate.mu.Unlock()

	// No gate wired ⇒ 503 deny-closed (the default-module path is exercised in
	// TestS137RBACTiers; here the stub reports no_gate).
	gate.set(GateStatusNoGate, "")
	if r := exec(); r.code != http.StatusServiceUnavailable {
		t.Fatalf("no gate = %d %s", r.code, r.raw)
	}

	// Rejected ⇒ 403 + denied status.
	gate.set(GateStatusRejected, "apr-2")
	if r := exec(); r.code != http.StatusForbidden {
		t.Fatalf("rejected = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/compliance/erasure/"+id, owner, nil, hdr); r.body["status"] != erasureStatusDenied {
		t.Fatalf("status after reject = %v", r.body["status"])
	}
}

func TestErasureExecuteEndToEndChainIntact(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "rtbf")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	seedSubjectRows(h, tenant, "agent-1")

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-E2E", "reason": "gdpr art 17",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}

	// PII is gone; unrelated rows survive.
	h.mutate(tenant, func(sc store.Scope) error {
		for _, kind := range []model.Kind{knowledgeMemoryStandInKind, sessionsLiveStandInKind, voiceSessionStandInKind} {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			gone, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq("agent_ref", "agent-1")}})
			if err != nil {
				return err
			}
			if len(gone) != 0 {
				t.Fatalf("%s still holds %d subject rows post-erasure", kind, len(gone))
			}
		}
		mem, _ := sc.Ext(knowledgeMemoryStandInKind)
		other, _, err := mem.List(context.Background(), model.Query{Filters: []model.Filter{eq("agent_ref", "agent-other")}})
		if err != nil {
			return err
		}
		if len(other) != 1 {
			t.Fatalf("unrelated memory rows = %d, want 1 (over-deletion)", len(other))
		}
		// The key ring is shredded.
		keys, err := sc.Ext(subjectKeyKind)
		if err != nil {
			return err
		}
		left, _, err := keys.List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(left) != 0 {
			t.Fatalf("subject keys remaining = %d, want 0", len(left))
		}
		// THE BAR: the hash chain verifies AFTER the erasure.
		rep, err := sc.Audit().Verify(context.Background(), 0)
		if err != nil {
			return err
		}
		if !rep.OK {
			t.Fatalf("post-erasure chain verify broke: %+v", rep)
		}
		if rep.Checked == 0 {
			t.Fatal("post-erasure verify checked 0 events (no chain to attest)")
		}
		return nil
	})

	// The receipt: honest gaps (account/provider unwired), verified, floor honest,
	// reconciliation embedded, subject only as a (now dead) token.
	r = h.do("GET", "/v1/m/compliance/erasure/"+id+"/receipt", owner, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("receipt = %d %s", r.code, r.raw)
	}
	if r.body["verify_ok"] != true {
		t.Fatalf("receipt verify_ok = %v (%s)", r.body["verify_ok"], r.raw)
	}
	if r.body["key_shredded"] != true {
		t.Fatalf("receipt key_shredded = %v", r.body["key_shredded"])
	}
	if got := r.body["provider_outcome"].(string); !strings.HasPrefix(got, "not_wired") {
		t.Fatalf("provider_outcome = %q", got)
	}
	if r.body["provider_floor_known"] != false {
		t.Fatalf("provider_floor_known = %v, want false (no adapter wired — honest)", r.body["provider_floor_known"])
	}
	if retained, _ := r.body["retained"].([]any); len(retained) != len(retainedReconciliation) {
		t.Fatalf("retained reconciliation entries = %d, want %d", len(retained), len(retainedReconciliation))
	}
	if tok := r.body["subject_token"].(string); strings.Contains(tok, "agent-1") {
		t.Fatalf("receipt token leaks the subject: %q", tok)
	}

	// The request now renders the subject as the permanent stand-in.
	if r := h.do("GET", "/v1/m/compliance/erasure/"+id, owner, nil, hdr); r.body["subject"] != erasedTokenDisplay {
		t.Fatalf("post-shred subject = %v", r.body["subject"])
	}
	if r := h.do("GET", "/v1/m/compliance/erasure/"+id, owner, nil, hdr); r.body["status"] != erasureStatusGaps {
		t.Fatalf("status = %v, want %s (account+provider unwired)", r.body["status"], erasureStatusGaps)
	}

	// Re-execution of a completed erasure is refused.
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr); r.code != http.StatusConflict {
		t.Fatalf("re-execute = %d %s", r.code, r.raw)
	}

	// Self-audits landed; the completion finding was published after commit.
	actions := strings.Join(h.auditActions(tenant), ",")
	for _, want := range []string{"compliance.erasure.request", "compliance.erasure.shred", "compliance.erasure.execute"} {
		if !strings.Contains(actions, want) {
			t.Fatalf("missing self-audit %s in %s", want, actions)
		}
	}
	foundFinding := false
	for _, f := range h.deliveredFindings() {
		if f.Kind == findingErasureCompleted {
			foundFinding = true
		}
	}
	if !foundFinding {
		t.Fatal("no compliance_erasure_completed finding published")
	}

	// The art_17 control flips to satisfied on REAL operational evidence.
	r = h.do("GET", "/v1/m/compliance/frameworks/gdpr/status", owner, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("gdpr status = %d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "rtbf_erasure") {
		t.Fatal("gdpr status does not consume the rtbf_erasure capability")
	}
}

func TestDataSubjectEraseEndpointRunsWorkflowAndStatus(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ds", "alice", "bob")
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "datasubject")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	seedSubjectRows(h, tenant, "agent-ds")

	r := h.do("POST", "/v1/m/compliance/data-subjects/agent-ds/erase", owner, map[string]any{
		"subject_kind": "agent", "case_ref": "DSR-FACADE", "reason": "gdpr art 17",
	}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("data-subject erase = %d %s", r.code, r.raw)
	}
	if r.body["key_shredded"] != true || r.body["verify_ok"] != true {
		t.Fatalf("erase response should be a verified receipt, got %s", r.raw)
	}

	st := h.do("GET", "/v1/m/compliance/data-subjects/agent-ds/erasure-status?subject_kind=agent", owner, nil, hdr)
	if st.code != http.StatusOK {
		t.Fatalf("data-subject status = %d %s", st.code, st.raw)
	}
	if st.body["state"] != "verified" || st.body["key_shredded"] != true || st.body["verified"] != true {
		t.Fatalf("status = %s, want verified/key_shredded", st.raw)
	}
}

func TestErasureEnterpriseCoordinatorBlocksReadiness(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-block", "alice", "bob")
	coord := &stubCryptoShredCoordinator{readiness: CryptoShredReadiness{
		Ready:         false,
		Blockers:      []CryptoShredBlocker{{Kind: "worm_lock", Detail: "archive sink has not acknowledged invalidation"}},
		PolicyApplied: "AES-256-GCM/deep",
	}}
	h := newHarness(t, WithApprovalGate(gate), WithCryptoShredCoordinator(coord))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "coordblock")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-block", "case_ref": "DSR-BLOCK",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	if r.code != http.StatusLocked {
		t.Fatalf("coordinator block = %d %s", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "rtbf_coordinator") || !strings.Contains(r.raw, "worm_lock") {
		t.Fatalf("coordinator block body missing detail: %s", r.raw)
	}
	if len(gate.requests()) != 0 {
		t.Fatal("enterprise coordinator readiness must run before approval gate")
	}
	if coord.readinessCalls == 0 || coord.notifyCalls != 0 || coord.verifyCalls != 0 {
		t.Fatalf("coordinator calls readiness=%d notify=%d verify=%d", coord.readinessCalls, coord.notifyCalls, coord.verifyCalls)
	}
}

func TestErasureEnterpriseCoordinatorReflectAdapterCompletes(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-reflect", "alice", "bob")
	coord := &reflectCryptoShredCoordinator{}
	h := newHarness(t, WithApprovalGate(gate), WithCryptoShredCoordinator(coord))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "coordreflect")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	seedSubjectRows(h, tenant, "agent-reflect")

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-reflect", "case_ref": "DSR-REFLECT",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if coord.readinessCalls == 0 || coord.notifyCalls != 1 || coord.verifyCalls != 1 {
		t.Fatalf("coordinator calls readiness=%d notify=%d verify=%d", coord.readinessCalls, coord.notifyCalls, coord.verifyCalls)
	}
	if r.body["verify_ok"] != true || !strings.Contains(r.raw, "reflect adapter warning") {
		t.Fatalf("reflect coordinator receipt missing verification/warning: %s", r.raw)
	}
}

// TestErasureCoordinatorReceivesWorkingEvidenceProbes proves the v2 seam
// contract from the module's side: the execute path hands the coordinator (a)
// the tenant on the readiness check and (b) evidence probes bound to the SHRED
// TRANSACTION — the KeyGone probe observes the just-destroyed key row and the
// ResidualScan probe re-runs the real registry scan (clean, with a real target
// count) — so an enterprise coordinator can verify instead of assume.
func TestErasureCoordinatorReceivesWorkingEvidenceProbes(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-probes", "alice", "bob")
	coord := &stubCryptoShredCoordinator{}
	h := newHarness(t, WithApprovalGate(gate), WithCryptoShredCoordinator(coord))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "coordprobes")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	seedSubjectRows(h, tenant, "agent-probes")

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-probes", "case_ref": "DSR-PROBES",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if coord.tenantSeen != tenant.String() {
		t.Fatalf("readiness tenant = %q, want %q", coord.tenantSeen, tenant)
	}
	if coord.probeErr != nil {
		t.Fatalf("evidence probes errored: %v", coord.probeErr)
	}
	if !coord.probeKeyGone {
		t.Fatal("KeyGone probe did not observe the destroyed subject key inside the shred transaction")
	}
	if coord.probeScanned == 0 {
		t.Fatalf("ResidualScan probe scanned 0 targets; want the real registry count")
	}
	if len(coord.probeResidues) != 0 {
		t.Fatalf("ResidualScan probe found unexpected residues after a full erasure: %v", coord.probeResidues)
	}
}

// TestErasureHTTPAdversarialResidueAndRealCounts drives the public HTTP execute
// path twice. The first run simulates a provider race that reintroduces subject
// memory after target deletion; the live residual probes must name it and refuse
// a complete receipt. The clean run must report evidence derived from real rows
// and a non-empty ledger, rather than constants.
func TestErasureHTTPAdversarialResidueAndRealCounts(t *testing.T) {
	run := func(t *testing.T, reintroduce bool) (resp, *stubCryptoShredCoordinator) {
		t.Helper()
		gate := &stubApprovalGate{}
		gate.set(GateStatusApproved, "apr-adversarial", "alice", "bob")
		coord := &stubCryptoShredCoordinator{}
		provider := &stubProviderEraser{outcome: ProviderEraseOutcome{Wired: true}}
		h := newHarness(t, WithApprovalGate(gate), WithProviderEraser(provider), WithCryptoShredCoordinator(coord))
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "rtbf-adversarial")
		owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
		hdr := tenantHdr(tenant)
		const subject = "agent-adversarial"
		seedSubjectRows(h, tenant, subject)
		if reintroduce {
			provider.afterErase = func(ctx context.Context, tenant model.TenantID) error {
				return h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
					memory, err := sc.Ext(knowledgeMemoryStandInKind)
					if err != nil {
						return err
					}
					_, err = memory.Create(ctx, model.Record{
						"agent_ref": subject, "mkey": "reintroduced", "content": "maria@example.com survived",
					})
					return err
				})
			}
		}
		created := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
			"subject_kind": "agent", "subject_ref": subject, "case_ref": "DSR-ADVERSARIAL",
		}, hdr)
		if created.code != http.StatusCreated {
			t.Fatalf("create = %d %s", created.code, created.raw)
		}
		result := h.do("POST", "/v1/m/compliance/erasure/"+created.body["id"].(string)+"/execute", owner, nil, hdr)
		if result.code != http.StatusOK {
			t.Fatalf("execute = %d %s", result.code, result.raw)
		}
		return result, coord
	}

	t.Run("reintroduced_memory_is_not_complete", func(t *testing.T) {
		result, coord := run(t, true)
		if result.body["verify_ok"] == true {
			t.Fatalf("receipt rounded a planted residue up to complete: %s", result.raw)
		}
		if !strings.Contains(result.raw, "knowledge.memory.agent_ref") {
			t.Fatalf("receipt did not name the residue class: %s", result.raw)
		}
		if len(coord.probeResidues) == 0 || coord.probeScanned == 0 {
			t.Fatalf("coordinator evidence = residues %v across %d targets, want a real hit", coord.probeResidues, coord.probeScanned)
		}
	})

	t.Run("clean_erase_uses_real_counts", func(t *testing.T) {
		result, coord := run(t, false)
		if result.body["verify_ok"] != true || intOf(result.body["verify_checked"]) == 0 {
			t.Fatalf("clean receipt lacks non-vacuous verification: %s", result.raw)
		}
		if !coord.probeKeyGone || coord.probeScanned == 0 || len(coord.probeResidues) != 0 {
			t.Fatalf("coordinator evidence key_gone=%v scanned=%d residues=%v", coord.probeKeyGone, coord.probeScanned, coord.probeResidues)
		}
		var memoryCounted bool
		for _, raw := range result.body["targets"].([]any) {
			target := raw.(map[string]any)
			if target["target"] == "knowledge.memory" {
				memoryCounted = intOf(target["examined"]) == 1 && intOf(target["erased"]) == 1
			}
		}
		if !memoryCounted {
			t.Fatalf("receipt did not carry real knowledge.memory erase counts: %s", result.raw)
		}
	})
}

// TestErasureCoordinatorUnverifiedForcesGaps proves the receipt side of the
// deny-closed contract: a coordinator that reports an explicitly UNVERIFIED
// claim (Complete=false) turns the erasure status into gaps and the receipt's
// verification summary carries the unverified reason — never a silent success.
func TestErasureCoordinatorUnverifiedForcesGaps(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-unv", "alice", "bob")
	coord := &stubCryptoShredCoordinator{verify: CryptoShredVerification{
		Complete:     false,
		KeyDestroyed: true,
		WORMNotified: false,
		ResidualScan: CryptoShredResidualScan{ScanDepth: "deep", TargetsScanned: 4, Clean: true},
		Unverified:   []string{"worm notification unrecorded for key (test)"},
	}}
	h := newHarness(t, WithApprovalGate(gate), WithCryptoShredCoordinator(coord))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "coordunv")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	seedSubjectRows(h, tenant, "agent-unv")

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-unv", "case_ref": "DSR-UNV",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if r.body["verify_ok"] == true {
		t.Fatalf("an unverified coordinator verdict must fail verification: %s", r.raw)
	}
	if !strings.Contains(r.raw, "worm notification unrecorded for key (test)") {
		t.Fatalf("receipt is missing the explicit unverified reason: %s", r.raw)
	}
}

func TestErasureUserSubjectScrubsAndLegsRun(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	acc := &stubAccountEraser{outcome: AccountEraseOutcome{Attempted: true, Erased: 1, Detail: "ok"}}
	prov := &stubProviderEraser{outcome: ProviderEraseOutcome{Wired: true, Enumerated: 2, Erased: 2}}
	h := newHarness(t, WithApprovalGate(gate), WithAccountEraser(acc), WithProviderEraser(prov))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "userdsr")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	// The raw developer email lives in finops cost samples (actor) and duplicates
	// into the linked canonical cost_records metadata.
	var crID model.ID
	h.mutate(tenant, func(sc store.Scope) error {
		cr, err := sc.Costs().Create(context.Background(), model.CostRecord{
			InputTokens: 10, OutputTokens: 5, CostMicroUSD: 42, Currency: "USD",
			OccurredAt: h.mod.clock.Now(),
			Metadata:   map[string]any{"actor": "maria@example.com", "team": "core"},
		})
		if err != nil {
			return err
		}
		crID = cr.ID
		repo, err := sc.Ext(costSampleStandInKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"input_tokens": int64(10), "cost_micro_usd": int64(42),
			"actor": "maria@example.com", "cost_record_id": crID.String(),
		})
		return err
	})

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "user", "subject_ref": "maria@example.com",
		"aliases":  []string{"user:11111111-2222-3333-4444-555555555555"},
		"case_ref": "DSR-USER",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner,
		map[string]any{"provider_user_ids": []string{"claude-user-1"}}, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if r.body["account_outcome"] != "erased 1 account(s); ok" {
		t.Fatalf("account_outcome = %v", r.body["account_outcome"])
	}

	// Both legs ran with the right inputs.
	if len(acc.calls) != 1 || acc.calls[0][0] != "maria@example.com" {
		t.Fatalf("account leg calls = %+v", acc.calls)
	}
	if len(prov.reqs) != 1 || prov.reqs[0].SubjectUserIDs[0] != "claude-user-1" || prov.reqs[0].CaseRef != "DSR-USER" {
		t.Fatalf("provider leg reqs = %+v", prov.reqs)
	}

	// The scrub: actor nulled on the sample, the metadata duplicate removed, the
	// row's operational value retained.
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(costSampleStandInKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("cost samples = %d, want 1 (scrub must retain the row)", len(rows))
		}
		if got := rows[0].String("actor"); got != "" {
			t.Fatalf("sample actor post-scrub = %q", got)
		}
		if rows[0].Int("cost_micro_usd") != 42 {
			t.Fatal("scrub destroyed the sample's operational value")
		}
		cr, err := sc.Costs().Get(context.Background(), crID)
		if err != nil {
			return err
		}
		if _, has := cr.Metadata["actor"]; has {
			t.Fatal("cost_records metadata still carries the actor email")
		}
		if cr.Metadata["team"] != "core" {
			t.Fatal("scrub destroyed unrelated cost metadata")
		}
		return nil
	})

	// Fully wired + verified ⇒ completed, not completed_with_gaps.
	if r := h.do("GET", "/v1/m/compliance/erasure/"+id, owner, nil, hdr); r.body["status"] != erasureStatusCompleted {
		t.Fatalf("status = %v, want %s", r.body["status"], erasureStatusCompleted)
	}
}

func TestErasureProviderFailureVetoesTheShred(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	prov := &stubProviderEraser{outcome: ProviderEraseOutcome{Wired: true, Enumerated: 3, Erased: 2, Failed: 1}}
	h := newHarness(t, WithApprovalGate(gate), WithProviderEraser(prov))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "provfail")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-F",
	}, hdr)
	id := r.body["id"].(string)
	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner,
		map[string]any{"provider_user_ids": []string{"u1"}}, hdr)
	if r.code != http.StatusConflict || !strings.Contains(r.raw, "provider deletion(s) failed") {
		t.Fatalf("provider-failed execute = %d %s", r.code, r.raw)
	}

	// NOTHING was shredded: provider-side content survives, so the key must too.
	h.mutate(tenant, func(sc store.Scope) error {
		keys, err := sc.Ext(subjectKeyKind)
		if err != nil {
			return err
		}
		left, _, err := keys.List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(left) != 1 {
			t.Fatalf("subject keys = %d, want 1 (a failed provider leg must veto the shred)", len(left))
		}
		return nil
	})
	if r := h.do("GET", "/v1/m/compliance/erasure/"+id, owner, nil, hdr); r.body["status"] != erasureStatusFailed {
		t.Fatalf("status = %v, want %s", r.body["status"], erasureStatusFailed)
	}

	// The retry after the provider failures clear completes and shreds.
	prov.mu.Lock()
	prov.outcome = ProviderEraseOutcome{Wired: true, Enumerated: 3, Erased: 3}
	prov.mu.Unlock()
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("retry = %d %s", r.code, r.raw)
	}
	// The provider ids persisted on the request: the retry above sent NO body ids,
	// yet the leg still received them.
	prov.mu.Lock()
	last := prov.reqs[len(prov.reqs)-1]
	prov.mu.Unlock()
	if len(last.SubjectUserIDs) != 1 || last.SubjectUserIDs[0] != "u1" {
		t.Fatalf("persisted provider ids not replayed on retry: %+v", last.SubjectUserIDs)
	}
}

func TestErasureClassScopeNarrowsExecution(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "scoped")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	seedSubjectRows(h, tenant, "agent-1")

	// Narrow the DSR to agent.memory only: session/voice targets must be SKIPPED
	// (execution never exceeds the approved/hold-checked scope).
	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-N",
		"data_classes": []string{"agent.memory"},
	}, hdr)
	id := r.body["id"].(string)
	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	h.mutate(tenant, func(sc store.Scope) error {
		mem, _ := sc.Ext(knowledgeMemoryStandInKind)
		gone, _, err := mem.List(context.Background(), model.Query{Filters: []model.Filter{eq("agent_ref", "agent-1")}})
		if err != nil {
			return err
		}
		if len(gone) != 0 {
			t.Fatalf("agent.memory rows survive a scoped erasure: %d", len(gone))
		}
		live, _ := sc.Ext(sessionsLiveStandInKind)
		kept, _, err := live.List(context.Background(), model.Query{Filters: []model.Filter{eq("agent_ref", "agent-1")}})
		if err != nil {
			return err
		}
		if len(kept) != 1 {
			t.Fatalf("session rows = %d, want 1 (outside the requested class scope)", len(kept))
		}
		return nil
	})
	rr := h.do("GET", "/v1/m/compliance/erasure/"+id+"/receipt", owner, nil, hdr)
	if !strings.Contains(rr.raw, "skipped") || !strings.Contains(rr.raw, "outside the request's data_class scope") {
		t.Fatalf("receipt does not record the skipped out-of-scope targets: %s", rr.raw)
	}
	// The skipped session rows are NOT residues — verification still passes.
	if rr.body["verify_ok"] != true {
		t.Fatalf("scoped erasure verify_ok = %v (%s)", rr.body["verify_ok"], rr.raw)
	}
}

func TestErasureCostLedgerScrubWithoutSampleLink(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "costledger")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	// An ORPHAN canonical ledger row (its read-model sample was retention-purged):
	// the registry's cost_record_id propagation cannot reach it.
	var crID model.ID
	h.mutate(tenant, func(sc store.Scope) error {
		cr, err := sc.Costs().Create(context.Background(), model.CostRecord{
			InputTokens: 7, CostMicroUSD: 9, Currency: "USD", OccurredAt: h.mod.clock.Now(),
			Metadata: map[string]any{"actor": "maria@example.com"},
		})
		crID = cr.ID
		return err
	})

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "user", "subject_ref": "maria@example.com", "case_ref": "DSR-CL",
	}, hdr)
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	h.mutate(tenant, func(sc store.Scope) error {
		cr, err := sc.Costs().Get(context.Background(), crID)
		if err != nil {
			return err
		}
		if _, has := cr.Metadata["actor"]; has {
			t.Fatal("orphan cost_records row still carries the actor email")
		}
		return nil
	})
}

func TestErasureProviderPendingHoldsTheShred(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	prov := &stubProviderEraser{outcome: ProviderEraseOutcome{Wired: true, Enumerated: 3, Erased: 1, Pending: 2}}
	h := newHarness(t, WithApprovalGate(gate), WithProviderEraser(prov))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "provpend")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-P",
	}, hdr)
	id := r.body["id"].(string)
	r = h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner,
		map[string]any{"provider_user_ids": []string{"u1"}}, hdr)
	if r.code != http.StatusAccepted {
		t.Fatalf("provider-pending execute = %d %s", r.code, r.raw)
	}

	// The shred must NOT have happened: a re-execute still knows the subject.
	h.mutate(tenant, func(sc store.Scope) error {
		keys, err := sc.Ext(subjectKeyKind)
		if err != nil {
			return err
		}
		left, _, err := keys.List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(left) != 1 {
			t.Fatalf("subject keys = %d, want 1 (shred must wait for the provider leg)", len(left))
		}
		return nil
	})

	// Provider approvals land ⇒ the re-execute completes and shreds.
	prov.mu.Lock()
	prov.outcome = ProviderEraseOutcome{Wired: true, Enumerated: 3, Erased: 3}
	prov.mu.Unlock()
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner,
		map[string]any{"provider_user_ids": []string{"u1"}}, hdr); r.code != http.StatusOK {
		t.Fatalf("re-execute = %d %s", r.code, r.raw)
	}
}

func TestErasureDocumentCascade(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "doccascade")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	var docID, kbID string
	h.mutate(tenant, func(sc store.Scope) error {
		kbs, err := sc.Ext(kbStandInKind)
		if err != nil {
			return err
		}
		kb, err := kbs.Create(context.Background(), model.Record{
			"name": "hr-docs", "doc_count": int64(2), "chunk_count": int64(3),
		})
		if err != nil {
			return err
		}
		kbID = kb.String(model.ColID)
		docs, err := sc.Ext(documentStandInKind)
		if err != nil {
			return err
		}
		doc, err := docs.Create(context.Background(), model.Record{
			"kb_ref": kbID, "title": "maria's performance review", "chunk_count": int64(2),
		})
		if err != nil {
			return err
		}
		docID = doc.String(model.ColID)
		chunks, err := sc.Ext(chunkStandInKind)
		if err != nil {
			return err
		}
		for i := int64(0); i < 2; i++ {
			if _, err := chunks.Create(context.Background(), model.Record{
				"doc_ref": docID, "chunk_index": i, "text": "maria@example.com … review text", "embedding": []byte{1, 2, 3},
			}); err != nil {
				return err
			}
		}
		labels, err := sc.Ext(labelStandInKind)
		if err != nil {
			return err
		}
		_, err = labels.Create(context.Background(), model.Record{
			"subject_kind": "document", "subject_ref": docID, "classes": `[{"class":"pii.contact"}]`, "max_severity": "high",
		})
		return err
	})

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "document", "subject_ref": docID, "case_ref": "DSR-DOC",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}

	h.mutate(tenant, func(sc store.Scope) error {
		docs, _ := sc.Ext(documentStandInKind)
		if _, err := docs.Get(context.Background(), model.ID(docID)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("document survives: %v", err)
		}
		chunks, _ := sc.Ext(chunkStandInKind)
		left, _, err := chunks.List(context.Background(), model.Query{Filters: []model.Filter{eq("doc_ref", docID)}})
		if err != nil {
			return err
		}
		if len(left) != 0 {
			t.Fatalf("chunks survive: %d", len(left))
		}
		labels, _ := sc.Ext(labelStandInKind)
		l, _, err := labels.List(context.Background(), model.Query{Filters: []model.Filter{eq("subject_ref", docID)}})
		if err != nil {
			return err
		}
		if len(l) != 0 {
			t.Fatalf("labels survive: %d", len(l))
		}
		kbs, _ := sc.Ext(kbStandInKind)
		kb, err := kbs.Get(context.Background(), model.ID(kbID))
		if err != nil {
			return err
		}
		if kb.Int("doc_count") != 1 || kb.Int("chunk_count") != 1 {
			t.Fatalf("kb counters = %d docs / %d chunks, want 1/1", kb.Int("doc_count"), kb.Int("chunk_count"))
		}
		return nil
	})

	// A per-document hold vetoes the cascade outright.
	r = h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "document", "subject_ref": "doc-held", "case_ref": "DSR-DOC2",
	}, hdr)
	id2 := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/compliance/holds", owner, map[string]any{
		"matter_ref": "M-DOC", "scope_kind": "subject", "subject_kind": "document", "subject_ref": "doc-held",
		"reason": "ediscovery",
	}, hdr); r.code != http.StatusCreated {
		t.Fatalf("doc hold = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id2+"/execute", owner, nil, hdr); r.code != http.StatusLocked {
		t.Fatalf("held document execute = %d %s", r.code, r.raw)
	}
}

func TestS137RBACTiersAndDenyClosedDefault(t *testing.T) {
	h := newHarness(t) // NO approval gate: the module's denyApprovalGate default
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "rbac137")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	viewer := h.roleToken(admin, tenant, "viewer@x.io", "viewer")
	editor := h.roleToken(admin, tenant, "editor@x.io", "editor")
	hdr := tenantHdr(tenant)

	// Registering and executing are ADMIN-tier.
	body := map[string]any{"subject_kind": "agent", "subject_ref": "agent-1", "case_ref": "DSR-R"}
	if r := h.do("POST", "/v1/m/compliance/erasure", viewer, body, hdr); r.code != http.StatusForbidden {
		t.Fatalf("viewer create = %d", r.code)
	}
	if r := h.do("POST", "/v1/m/compliance/erasure", editor, body, hdr); r.code != http.StatusForbidden {
		t.Fatalf("editor create = %d", r.code)
	}
	r := h.do("POST", "/v1/m/compliance/erasure", owner, body, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("owner create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	// Reading is read-tier (viewer allowed).
	if r := h.do("GET", "/v1/m/compliance/erasure", viewer, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("viewer list = %d", r.code)
	}
	if r := h.do("GET", "/v1/m/compliance/erasure/"+id+"/events", viewer, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("viewer events = %d", r.code)
	}

	// With NO gate wired, an execute is denied 503 (deny-closed) — never destructive.
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr); r.code != http.StatusServiceUnavailable {
		t.Fatalf("ungated execute = %d %s", r.code, r.raw)
	}
}

// countCustody counts erasure custody events of one kind for a request.
func (h *harness) countCustody(tenant model.TenantID, erasureID, event string) int {
	h.t.Helper()
	n := 0
	h.mutate(tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(erasureEventKind)
		if err != nil {
			return err
		}
		recs, err := listAll(context.Background(), repo, eq(colEEErasureID, erasureID), eq(colEEEvent, event))
		if err != nil {
			return err
		}
		n = len(recs)
		return nil
	})
	return n
}

// TestErasureUserSubjectScopedMemory proves the unlock: an RTBF request for
// a USER (or SESSION) subject erases exactly that namespace's rows in the scoped
// memory target (knowledge.memory_scoped user_ref/session_ref mappings) — which
// the agent-global memory table could never address — while every other
// namespace and the shared agent scope survive.
func TestErasureUserSubjectScopedMemory(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "rtbf-user")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	h.seedExtRows(tenant, scopedMemoryStandInKind, 2, model.Record{
		"agent_ref": "a1", "user_ref": "u-erase", "session_ref": "", "mkey": "pref", "content": "u-erase pii",
	})
	h.seedExtRows(tenant, scopedMemoryStandInKind, 1, model.Record{
		"agent_ref": "a1", "user_ref": "u-keep", "session_ref": "s-9", "mkey": "pref", "content": "other user",
	})
	// The negative control for the SESSION leg below: a row in ANOTHER session,
	// so a session_ref-filtered delete is distinguishable from a delete-all.
	h.seedExtRows(tenant, scopedMemoryStandInKind, 1, model.Record{
		"agent_ref": "a1", "user_ref": "u-keep", "session_ref": "s-other", "mkey": "pref2", "content": "other session",
	})
	h.seedExtRows(tenant, knowledgeMemoryStandInKind, 1, model.Record{
		"agent_ref": "a1", "mkey": "shared", "content": "agent-global, not user-attributable",
	})

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "user", "subject_ref": "u-erase", "case_ref": "DSR-U1", "reason": "gdpr art 17",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}

	h.mutate(tenant, func(sc store.Scope) error {
		scoped, err := sc.Ext(scopedMemoryStandInKind)
		if err != nil {
			return err
		}
		gone, _, err := scoped.List(context.Background(), model.Query{Filters: []model.Filter{eq("user_ref", "u-erase")}})
		if err != nil {
			return err
		}
		if len(gone) != 0 {
			t.Fatalf("u-erase scoped rows post-erasure = %d, want 0", len(gone))
		}
		kept, _, err := scoped.List(context.Background(), model.Query{Filters: []model.Filter{eq("user_ref", "u-keep")}})
		if err != nil {
			return err
		}
		if len(kept) != 2 {
			t.Fatalf("u-keep scoped rows = %d, want 2 (over-deletion)", len(kept))
		}
		return nil
	})
	if n := h.countExtRows(tenant, knowledgeMemoryStandInKind); n != 1 {
		t.Fatalf("agent-global rows = %d, want 1 (a user erasure must not touch the shared scope)", n)
	}
	// Ledger coherence: each hard-deleted memory row left its per-row
	// deletion anchor, so a backup-replayed (resurrected) erased row can never
	// re-verify clean (knowledge POST /memory/verify reads these events).
	if n := countActions(h, tenant, "compliance.erasure.row"); n != 2 {
		t.Fatalf("per-row erasure events = %d, want 2", n)
	}

	// A SESSION-subject erasure addresses the session dimension the same way.
	r = h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "session", "subject_ref": "s-9", "case_ref": "DSR-S1", "reason": "gdpr art 17",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create session erasure = %d %s", r.code, r.raw)
	}
	id2 := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id2+"/execute", owner, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("execute session erasure = %d %s", r.code, r.raw)
	}
	// Exactly s-9's row fell; the s-other row AND the agent-global row survive
	// (a session_ref-filtered delete, never a delete-all).
	if n := h.countExtRows(tenant, scopedMemoryStandInKind); n != 1 {
		t.Fatalf("scoped rows after session erasure = %d, want 1 (s-other survives)", n)
	}
	if n := h.countExtRows(tenant, knowledgeMemoryStandInKind); n != 1 {
		t.Fatalf("agent-global rows after session erasure = %d, want 1", n)
	}
}

// countActions counts the audit events with the given action in the tenant chain.
func countActions(h *harness, tenant model.TenantID, action string) int {
	n := 0
	for _, a := range h.auditActions(tenant) {
		if a == action {
			n++
		}
	}
	return n
}

// TestErasureCrossSubjectHoldExcludesScopedRows pins the fix for the
// irreversible path: a scoped memory row is attributable to THREE hold subjects
// (agent/user/session); an RTBF execute for one subject must PRESERVE — and
// report as excluded_held — a row whose OTHER subject is under an active hold,
// exactly like the sweep and knowledge's own delete/purge do. Releasing the
// hold and re-requesting erases it.
func TestErasureCrossSubjectHoldExcludesScopedRows(t *testing.T) {
	gate := &stubApprovalGate{}
	gate.set(GateStatusApproved, "apr-ok", "alice", "bob")
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "rtbf-held")
	owner := h.roleToken(admin, tenant, "owner@x.io", "owner")
	hdr := tenantHdr(tenant)

	h.seedExtRows(tenant, scopedMemoryStandInKind, 1, model.Record{
		"agent_ref": "a1", "user_ref": "u-x", "session_ref": "s-held", "mkey": "k", "content": "held by session",
	})
	h.seedExtRows(tenant, scopedMemoryStandInKind, 1, model.Record{
		"agent_ref": "a1", "user_ref": "u-x", "session_ref": "s-free", "mkey": "k2", "content": "free",
	})
	holdID := h.createHold(owner, tenant, map[string]any{
		"matter_ref": "case-s", "reason": "session under litigation", "scope_kind": "subject",
		"subject_kind": "session", "subject_ref": "s-held",
	})

	r := h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "user", "subject_ref": "u-x", "case_ref": "DSR-X", "reason": "gdpr art 17",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id+"/execute", owner, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}

	// The session-held row SURVIVED; the free row fell; the receipt says so.
	var heldRows int
	h.mutate(tenant, func(sc store.Scope) error {
		scoped, err := sc.Ext(scopedMemoryStandInKind)
		if err != nil {
			return err
		}
		recs, _, err := scoped.List(context.Background(), model.Query{Filters: []model.Filter{eq("session_ref", "s-held")}})
		if err != nil {
			return err
		}
		heldRows = len(recs)
		return nil
	})
	if heldRows != 1 {
		t.Fatal("the session-held row must survive a user-subject erasure (over-preservation under hold)")
	}
	if n := h.countExtRows(tenant, scopedMemoryStandInKind); n != 1 {
		t.Fatalf("scoped rows = %d, want 1 (free row erased, held row preserved)", n)
	}
	rc := h.do("GET", "/v1/m/compliance/erasure/"+id+"/receipt", owner, nil, hdr)
	if rc.code != http.StatusOK {
		t.Fatalf("receipt = %d %s", rc.code, rc.raw)
	}
	excluded := false
	if targets, ok := rc.body["targets"].([]any); ok {
		for _, ti := range targets {
			tm, _ := ti.(map[string]any)
			if tm["target"] == "knowledge.memory_scoped" {
				if n, _ := tm["excluded_held"].(float64); n == 1 {
					excluded = true
				}
			}
		}
	}
	if !excluded {
		t.Fatalf("receipt must report the held row as excluded_held: %s", rc.raw)
	}

	// Hold released ⇒ a fresh request erases the remaining row.
	gate.set(GateStatusApproved, "apr-rel", "alice", "bob")
	if r := h.do("POST", "/v1/m/compliance/holds/"+holdID+"/release", owner, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("release hold = %d %s", r.code, r.raw)
	}
	gate.set(GateStatusApproved, "apr-ok2", "alice", "bob")
	r = h.do("POST", "/v1/m/compliance/erasure", owner, map[string]any{
		"subject_kind": "user", "subject_ref": "u-x", "case_ref": "DSR-X2", "reason": "gdpr art 17 retry",
	}, hdr)
	if r.code != http.StatusCreated {
		t.Fatalf("re-create = %d %s", r.code, r.raw)
	}
	id2 := r.body["id"].(string)
	if r := h.do("POST", "/v1/m/compliance/erasure/"+id2+"/execute", owner, nil, hdr); r.code != http.StatusOK {
		t.Fatalf("re-execute = %d %s", r.code, r.raw)
	}
	if n := h.countExtRows(tenant, scopedMemoryStandInKind); n != 0 {
		t.Fatalf("scoped rows after release+re-erasure = %d, want 0", n)
	}
}
