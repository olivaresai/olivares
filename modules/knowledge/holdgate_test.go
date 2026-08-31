// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// stubHoldGate is a controllable HoldGate: held / error / open per configuration.
// It records every check so tests assert the exact (subject, class) the module
// hands the gate (the docs/contracts enforcement points). A decide func
// (setDecide) makes the answer per-call — a SELECTIVE gate holding one subject
// while clearing the rest, which is what the per-row/per-document fixes need.
type stubHoldGate struct {
	held  bool
	holds []HoldRef
	err   error

	mu     sync.Mutex
	decide func(c holdCheckCall) (bool, []HoldRef, error)
	calls  []holdCheckCall
}

type holdCheckCall struct{ subjectKind, subjectRef, dataClass string }

func (g *stubHoldGate) Check(_ context.Context, _ model.TenantID, subjectKind, subjectRef, dataClass string) (bool, []HoldRef, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := holdCheckCall{subjectKind, subjectRef, dataClass}
	g.calls = append(g.calls, c)
	if g.decide != nil {
		return g.decide(c)
	}
	if g.err != nil {
		return false, nil, g.err
	}
	return g.held, g.holds, nil
}

// setDecide swaps the per-call decision function (mutex-guarded for the -race gate).
func (g *stubHoldGate) setDecide(fn func(c holdCheckCall) (bool, []HoldRef, error)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.decide = fn
}

func (g *stubHoldGate) lastCall(t *testing.T) holdCheckCall {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.calls) == 0 {
		t.Fatal("hold gate was never called")
	}
	return g.calls[len(g.calls)-1]
}

// callsFor counts the recorded checks for one exact subject.
func (g *stubHoldGate) callsFor(kind, ref string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if c.subjectKind == kind && c.subjectRef == ref {
			n++
		}
	}
	return n
}

// snapshot returns a copy of the recorded checks.
func (g *stubHoldGate) snapshot() []holdCheckCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]holdCheckCall, len(g.calls))
	copy(out, g.calls)
	return out
}

// openDecide clears every subject (an all-open selective gate).
func openDecide(holdCheckCall) (bool, []HoldRef, error) { return false, nil, nil }

// heldStub is a gate reporting one active matter-scoped hold.
func heldStub() *stubHoldGate {
	return &stubHoldGate{held: true, holds: []HoldRef{{ID: "h-1", MatterRef: "matter-42", ScopeKind: "tenant"}}}
}

// assertLegalHold423 asserts the blocked rendering: 423 Locked, code legal_hold,
// and the blocking holds listed in the body.
func assertLegalHold423(t *testing.T, r resp) {
	t.Helper()
	if r.code != http.StatusLocked {
		t.Fatalf("blocked delete = %d %s, want 423", r.code, r.raw)
	}
	errObj, _ := r.body["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("423 body has no error envelope: %s", r.raw)
	}
	if errObj["code"] != "legal_hold" {
		t.Errorf("code = %v, want legal_hold", errObj["code"])
	}
	holds, _ := errObj["holds"].([]any)
	if len(holds) != 1 {
		t.Fatalf("holds = %v, want the 1 blocking hold", errObj["holds"])
	}
	h, _ := holds[0].(map[string]any)
	if h["id"] != "h-1" || h["matter_ref"] != "matter-42" || h["scope_kind"] != "tenant" {
		t.Errorf("blocking hold = %v", h)
	}
}

// putMemory writes one memory entry and returns its id.
func (h *harness) putMemory(token string, tenant model.TenantID, agentRef, key string) string {
	h.t.Helper()
	r := h.do("POST", "/v1/m/knowledge/memory", token, map[string]any{
		"agent_ref": agentRef, "key": key, "content": "remember this",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("put memory = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// putMemoryTTL writes one short-lived memory entry (purge candidates once the
// fake clock advances past ttl).
func (h *harness) putMemoryTTL(token string, tenant model.TenantID, agentRef, key string, ttlSeconds int64) {
	h.t.Helper()
	r := h.do("POST", "/v1/m/knowledge/memory", token, map[string]any{
		"agent_ref": agentRef, "key": key, "content": "remember this", "ttl_seconds": ttlSeconds,
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("put memory ttl = %d %s", r.code, r.raw)
	}
}

// countingEmbedder counts Embed calls over the local zero-egress embedder, so a
// test can prove a DENIED ingest never reached the embed phase (the two-phase
// discipline: deny before any embed/write).
type countingEmbedder struct {
	LocalHashEmbedder
	mu    sync.Mutex
	calls int
}

func (e *countingEmbedder) Embed(ctx context.Context, tenant model.TenantID, texts []string) ([][]float32, string, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return e.LocalHashEmbedder.Embed(ctx, tenant, texts)
}

func (e *countingEmbedder) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestHoldGateBlocksKBDelete(t *testing.T) {
	gate := heldStub()
	h := newHarnessWith(t, WithHoldGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	kbID := h.mustKB(adminTok, tenant, map[string]any{"name": "held-kb"})

	r := h.do("DELETE", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant))
	assertLegalHold423(t, r)
	// The gate saw the contract's subject + class for a KB delete.
	if c := gate.lastCall(t); c != (holdCheckCall{holdSubjectKB, kbID, holdClassKnowledgeContent}) {
		t.Errorf("gate call = %+v", c)
	}
	// Nothing was destroyed.
	if g := h.do("GET", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("held KB must survive the delete, got %d", g.code)
	}
}

func TestHoldGateBlocksMemoryPurge(t *testing.T) {
	gate := heldStub()
	h := newHarnessWith(t, WithHoldGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")

	// Unfiltered purge under a tenant-wide hold: the upfront class-only call
	// (which the §4 rule matches against tenant and class scopes) blocks before
	// any row work. Agent subject-holds, which that call cannot see, are
	// excluded per row instead — TestUnfilteredPurgeExcludesHeldAgentRows.
	r := h.do("POST", "/v1/m/knowledge/memory/purge", adminTok, nil, tenantHdr(tenant))
	assertLegalHold423(t, r)
	if c := gate.lastCall(t); c != (holdCheckCall{"", "", holdClassAgentMemory}) {
		t.Errorf("unfiltered purge gate call = %+v", c)
	}

	// Agent-filtered purge adds the ("agent", ref) subject.
	r = h.do("POST", "/v1/m/knowledge/memory/purge?agent_ref=a1", adminTok, nil, tenantHdr(tenant))
	assertLegalHold423(t, r)
	if c := gate.lastCall(t); c != (holdCheckCall{holdSubjectAgent, "a1", holdClassAgentMemory}) {
		t.Errorf("filtered purge gate call = %+v", c)
	}
}

func TestHoldGateBlocksMemoryDelete(t *testing.T) {
	gate := heldStub()
	h := newHarnessWith(t, WithHoldGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	memID := h.putMemory(editor, tenant, "a7", "pref")

	r := h.do("DELETE", "/v1/m/knowledge/memory/"+memID, editor, nil, tenantHdr(tenant))
	assertLegalHold423(t, r)
	// The subject is the ROW's agent ref, read before the gate call.
	if c := gate.lastCall(t); c != (holdCheckCall{holdSubjectAgent, "a7", holdClassAgentMemory}) {
		t.Errorf("memory delete gate call = %+v", c)
	}
	if g := h.do("GET", "/v1/m/knowledge/memory/"+memID, editor, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("held memory entry must survive the delete, got %d", g.code)
	}

	// A missing row is 404, not 423: there is nothing to preserve, and the gate
	// is not consulted without a subject row.
	if r := h.do("DELETE", "/v1/m/knowledge/memory/00000000-0000-4000-8000-000000000099", editor, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("missing row delete = %d, want 404", r.code)
	}
}

func TestHoldGateErrorDenies(t *testing.T) {
	gate := &stubHoldGate{err: errors.New("hold ledger unreachable")}
	h := newHarnessWith(t, WithHoldGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	kbID := h.mustKB(adminTok, tenant, map[string]any{"name": "kb1"})
	memID := h.putMemory(adminTok, tenant, "a1", "k1")

	// Fail closed on every destructive endpoint: 503, nothing destroyed.
	if r := h.do("DELETE", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("kb delete under gate error = %d %s, want 503", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/knowledge/memory/"+memID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("memory delete under gate error = %d %s, want 503", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/knowledge/memory/purge", adminTok, nil, tenantHdr(tenant)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("memory purge under gate error = %d %s, want 503", r.code, r.raw)
	}
	if g := h.do("GET", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("KB must survive a gate error, got %d", g.code)
	}
}

func TestHoldGateOpenAllowsDelete(t *testing.T) {
	// A wired gate reporting NO hold lets destruction proceed unchanged.
	gate := &stubHoldGate{held: false}
	h := newHarnessWith(t, WithHoldGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	kbID := h.mustKB(adminTok, tenant, map[string]any{"name": "kb1"})
	memID := h.putMemory(adminTok, tenant, "a1", "k1")

	if r := h.do("DELETE", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("kb delete with open gate = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/knowledge/memory/"+memID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("memory delete with open gate = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/knowledge/memory/purge", adminTok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("memory purge with open gate = %d %s", r.code, r.raw)
	}
}

func TestNilHoldGatePreservesBehavior(t *testing.T) {
	// Unwired = the feature is absent (the posture): deletes behave as
	// before. KB delete and purge are also covered by the pre tests;
	// memory delete is asserted here explicitly.
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	kbID := h.mustKB(adminTok, tenant, map[string]any{"name": "kb1"})
	memID := h.putMemory(adminTok, tenant, "a1", "k1")

	if r := h.do("DELETE", "/v1/m/knowledge/memory/"+memID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("memory delete without a gate = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/knowledge/memory/purge", adminTok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("memory purge without a gate = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("kb delete without a gate = %d %s", r.code, r.raw)
	}
}

func TestUnfilteredPurgeExcludesHeldAgentRows(t *testing.T) {
	// F1 FINDING 0 regression: dropping ?agent_ref must NOT bypass an agent
	// subject-hold. The unfiltered purge excludes the held agent's expired rows
	// per row (counted as excluded_held in the response), deletes the others,
	// and checks each DISTINCT agent exactly once (the per-request cache).
	hold := HoldRef{ID: "h-1", MatterRef: "matter-42", ScopeKind: "subject"}
	gate := &stubHoldGate{}
	gate.setDecide(func(c holdCheckCall) (bool, []HoldRef, error) {
		if c.subjectKind == holdSubjectAgent && c.subjectRef == "held-agent" {
			return true, []HoldRef{hold}, nil
		}
		return false, nil, nil
	})
	fc := newFakeClock()
	h := newHarnessWith(t, WithClock(fc), WithHoldGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")

	// Two expired rows for the held agent, one for a free agent.
	h.putMemoryTTL(adminTok, tenant, "held-agent", "k1", 60)
	h.putMemoryTTL(adminTok, tenant, "held-agent", "k2", 60)
	h.putMemoryTTL(adminTok, tenant, "free-agent", "k1", 60)
	fc.advance(2 * time.Minute)

	r := h.do("POST", "/v1/m/knowledge/memory/purge", adminTok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("unfiltered purge = %d %s", r.code, r.raw)
	}
	if p, _ := r.body["purged"].(float64); p != 1 {
		t.Errorf("purged = %v, want 1 (only the free agent's row)", r.body["purged"])
	}
	if e, _ := r.body["excluded_held"].(float64); e != 2 {
		t.Errorf("excluded_held = %v, want 2 (the held agent's rows preserved)", r.body["excluded_held"])
	}
	if n := gate.callsFor(holdSubjectAgent, "held-agent"); n != 1 {
		t.Errorf("held-agent checked %d times, want exactly 1 (cached per request)", n)
	}
	if n := gate.callsFor(holdSubjectAgent, "free-agent"); n != 1 {
		t.Errorf("free-agent checked %d times, want exactly 1", n)
	}
	// The held rows really survived: once the hold clears, a second purge
	// removes exactly them.
	gate.setDecide(openDecide)
	r2 := h.do("POST", "/v1/m/knowledge/memory/purge", adminTok, nil, tenantHdr(tenant))
	if p, _ := r2.body["purged"].(float64); r2.code != http.StatusOK || p != 2 {
		t.Fatalf("post-release purge = %d purged=%v, want 200/2 (the held rows must have survived)", r2.code, r2.body["purged"])
	}
}

func TestUnfilteredPurgeGateErrorDeniesAll(t *testing.T) {
	// F1 FINDING 0: a gate ERROR during the per-agent exclusion loop denies the
	// WHOLE purge (503, fail closed) — every subject check concludes BEFORE any
	// delete, so even rows whose own agent already checked clean are untouched.
	gate := &stubHoldGate{}
	gate.setDecide(func(c holdCheckCall) (bool, []HoldRef, error) {
		if c.subjectKind == holdSubjectAgent && c.subjectRef == "agent-b" {
			return false, nil, errors.New("hold ledger unreachable")
		}
		return false, nil, nil
	})
	fc := newFakeClock()
	h := newHarnessWith(t, WithClock(fc), WithHoldGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")

	h.putMemoryTTL(adminTok, tenant, "agent-a", "k1", 60)
	h.putMemoryTTL(adminTok, tenant, "agent-b", "k1", 60)
	fc.advance(2 * time.Minute)

	if r := h.do("POST", "/v1/m/knowledge/memory/purge", adminTok, nil, tenantHdr(tenant)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("purge under a per-agent gate error = %d %s, want 503", r.code, r.raw)
	}
	// NOTHING was deleted by the failed purge: a clean re-run purges both rows.
	gate.setDecide(openDecide)
	r := h.do("POST", "/v1/m/knowledge/memory/purge", adminTok, nil, tenantHdr(tenant))
	if p, _ := r.body["purged"].(float64); r.code != http.StatusOK || p != 2 {
		t.Fatalf("post-error purge = %d purged=%v, want 200/2 (the failed purge must not have deleted anything)", r.code, r.body["purged"])
	}
}

func TestKBDeleteBlockedByDocumentHold(t *testing.T) {
	// F1 FINDING 1 regression: a ("document", <id>) subject-hold must veto the
	// KB delete cascade even when the ("kb", id) subject itself is clear.
	hold := HoldRef{ID: "h-doc", MatterRef: "matter-7", ScopeKind: "subject"}
	gate := &stubHoldGate{}
	gate.setDecide(func(c holdCheckCall) (bool, []HoldRef, error) {
		if c.subjectKind == holdSubjectDocument {
			return true, []HoldRef{hold}, nil
		}
		return false, nil, nil
	})
	h := newHarnessWith(t, WithHoldGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	kbID := h.mustKB(adminTok, tenant, map[string]any{"name": "kb1"})
	h.mustIngest(adminTok, tenant, kbID, []map[string]any{
		{"source_doc_id": "d1", "body": "doc one"},
		{"source_doc_id": "d2", "body": "doc two"},
	})

	r := h.do("DELETE", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant))
	if r.code != http.StatusLocked {
		t.Fatalf("kb delete with held documents = %d %s, want 423", r.code, r.raw)
	}
	errObj, _ := r.body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "legal_hold" {
		t.Fatalf("423 body = %s", r.raw)
	}
	// BOTH documents are covered by the same hold: listed ONCE (deduped).
	holds, _ := errObj["holds"].([]any)
	if len(holds) != 1 {
		t.Fatalf("holds = %v, want the 1 distinct blocking hold", errObj["holds"])
	}
	if hh, _ := holds[0].(map[string]any); hh["id"] != "h-doc" || hh["matter_ref"] != "matter-7" {
		t.Errorf("blocking hold = %v", holds[0])
	}
	// Every document was gated with the contract subject + class.
	docChecks := 0
	for _, c := range gate.snapshot() {
		if c.subjectKind != holdSubjectDocument {
			continue
		}
		docChecks++
		if c.dataClass != holdClassKnowledgeContent {
			t.Errorf("document check carried class %q, want %q", c.dataClass, holdClassKnowledgeContent)
		}
	}
	if docChecks != 2 {
		t.Errorf("document checks = %d, want 2 (one per document)", docChecks)
	}
	// Nothing was destroyed.
	if g := h.do("GET", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("KB with held documents must survive the delete, got %d", g.code)
	}
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", adminTok, nil, tenantHdr(tenant))
	if items, _ := docs.body["items"].([]any); len(items) != 2 {
		t.Fatalf("documents after the blocked delete = %d, want 2", len(items))
	}
}

func TestKBDeleteDocumentHoldGateErrorDenies(t *testing.T) {
	// F1 FINDING 1: an error from the per-document gate denies the KB delete
	// (503, fail closed) — a clean ("kb", id) check alone is not enough.
	gate := &stubHoldGate{}
	gate.setDecide(func(c holdCheckCall) (bool, []HoldRef, error) {
		if c.subjectKind == holdSubjectDocument {
			return false, nil, errors.New("hold ledger unreachable")
		}
		return false, nil, nil
	})
	h := newHarnessWith(t, WithHoldGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	kbID := h.mustKB(adminTok, tenant, map[string]any{"name": "kb1"})
	h.mustIngest(adminTok, tenant, kbID, []map[string]any{{"source_doc_id": "d1", "body": "doc one"}})

	if r := h.do("DELETE", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("kb delete under a per-document gate error = %d %s, want 503", r.code, r.raw)
	}
	if g := h.do("GET", "/v1/m/knowledge/kbs/"+kbID, adminTok, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("KB must survive a per-document gate error, got %d", g.code)
	}
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", adminTok, nil, tenantHdr(tenant))
	if items, _ := docs.body["items"].([]any); len(items) != 1 {
		t.Fatalf("documents after the denied delete = %d, want 1", len(items))
	}
}

func TestReingestBlockedByDocumentHold(t *testing.T) {
	// F1 FINDING 1-bis regression: re-ingest REPLACES an existing document's
	// chunks — destruction. With the document held, the WHOLE request is denied
	// (423) BEFORE any embed call or write; brand-new documents pass unchecked.
	hold := HoldRef{ID: "h-doc", MatterRef: "matter-7", ScopeKind: "subject"}
	gate := &stubHoldGate{}
	gate.setDecide(func(c holdCheckCall) (bool, []HoldRef, error) {
		if c.subjectKind == holdSubjectDocument {
			return true, []HoldRef{hold}, nil
		}
		return false, nil, nil
	})
	emb := &countingEmbedder{}
	h := newHarnessWith(t, WithHoldGate(gate), WithEmbedder(emb))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})

	// First ingest: d1 is NEW (no prior row) — no document check, no denial.
	h.mustIngest(editor, tenant, kbID, []map[string]any{{"source_doc_id": "d1", "body": "original body"}})
	embedsAfterFirst := emb.count()
	if embedsAfterFirst == 0 {
		t.Fatal("first ingest should have embedded")
	}
	if n := len(gate.snapshot()); n != 0 {
		t.Fatalf("first ingest of new documents made %d gate calls, want 0", n)
	}

	// Re-ingest d1 (held) alongside a NEW d2: the whole batch is denied.
	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{
			{"source_doc_id": "d1", "body": "replacement body"},
			{"source_doc_id": "d2", "body": "new doc"},
		},
	}, tenantHdr(tenant))
	if r.code != http.StatusLocked {
		t.Fatalf("re-ingest of a held document = %d %s, want 423", r.code, r.raw)
	}
	errObj, _ := r.body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "legal_hold" {
		t.Fatalf("423 body = %s", r.raw)
	}
	if holds, _ := errObj["holds"].([]any); len(holds) != 1 {
		t.Fatalf("holds = %v, want the 1 blocking hold", errObj["holds"])
	}
	// Only the EXISTING d1 was checked, with the contract subject + class.
	docChecks := 0
	for _, c := range gate.snapshot() {
		if c.subjectKind != holdSubjectDocument {
			continue
		}
		docChecks++
		if c.dataClass != holdClassKnowledgeContent {
			t.Errorf("document check carried class %q, want %q", c.dataClass, holdClassKnowledgeContent)
		}
	}
	if docChecks != 1 {
		t.Errorf("document checks = %d, want 1 (only the existing document)", docChecks)
	}
	// The denial happened BEFORE any embed (two-phase: no egress, no write) …
	if n := emb.count(); n != embedsAfterFirst {
		t.Errorf("embed calls after the denied re-ingest = %d, want %d (none for the denied request)", n, embedsAfterFirst)
	}
	// … and nothing changed: d1 keeps its original content, d2 was never created.
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	items, _ := docs.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("documents after the denied re-ingest = %d, want 1", len(items))
	}
	if d, _ := items[0].(map[string]any); d["content_hash"] != hashHex("original body") {
		t.Errorf("held document content changed: hash = %v", d["content_hash"])
	}
}

func TestReingestDocumentHoldGateErrorDenies(t *testing.T) {
	// F1 FINDING 1-bis: an error from the per-document gate on a re-ingest
	// denies the whole request (503, fail closed) before any embed or write.
	gate := &stubHoldGate{}
	gate.setDecide(func(c holdCheckCall) (bool, []HoldRef, error) {
		if c.subjectKind == holdSubjectDocument {
			return false, nil, errors.New("hold ledger unreachable")
		}
		return false, nil, nil
	})
	emb := &countingEmbedder{}
	h := newHarnessWith(t, WithHoldGate(gate), WithEmbedder(emb))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")
	kbID := h.mustKB(editor, tenant, map[string]any{"name": "kb1"})
	h.mustIngest(editor, tenant, kbID, []map[string]any{{"source_doc_id": "d1", "body": "original body"}})
	embedsAfterFirst := emb.count()

	r := h.do("POST", "/v1/m/knowledge/kbs/"+kbID+"/ingest", editor, map[string]any{
		"documents": []map[string]any{{"source_doc_id": "d1", "body": "replacement body"}},
	}, tenantHdr(tenant))
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("re-ingest under a gate error = %d %s, want 503", r.code, r.raw)
	}
	if n := emb.count(); n != embedsAfterFirst {
		t.Errorf("embed calls after the denied re-ingest = %d, want %d", n, embedsAfterFirst)
	}
	docs := h.do("GET", "/v1/m/knowledge/kbs/"+kbID+"/documents", editor, nil, tenantHdr(tenant))
	items, _ := docs.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("documents after the denied re-ingest = %d, want 1", len(items))
	}
	if d, _ := items[0].(map[string]any); d["content_hash"] != hashHex("original body") {
		t.Errorf("held document content changed under a gate error: hash = %v", d["content_hash"])
	}
}
