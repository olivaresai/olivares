// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The records-management tests (contract §11): the approval-gated purge
// schedule, the hold-checked sweep with its append-only certificates, the legal
// hold custody/release flow (CRITICAL dual-control, re-verified in-module), the
// hold-gate HTTP face and the §7 provider-floor annotation.

const s138Base = "/v1/m/compliance"

func (h *harness) putPolicy(tok string, tenant model.TenantID, class string, body map[string]any) resp {
	h.t.Helper()
	return h.do("PUT", s138Base+"/retention/policies/"+class, tok, body, tenantHdr(tenant))
}

func (h *harness) sweepNow(tok string, tenant model.TenantID) resp {
	h.t.Helper()
	r := h.do("POST", s138Base+"/retention/sweep", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("sweep = %d %s", r.code, r.raw)
	}
	return r
}

func (h *harness) listRuns(tok string, tenant model.TenantID, class string) []map[string]any {
	h.t.Helper()
	path := s138Base + "/retention/runs"
	if class != "" {
		path += "?class=" + class
	}
	r := h.do("GET", path, tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("runs = %d %s", r.code, r.raw)
	}
	return itemsOf(h.t, r)
}

func (h *harness) createHold(tok string, tenant model.TenantID, body map[string]any) string {
	h.t.Helper()
	r := h.do("POST", s138Base+"/holds", tok, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create hold = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

func (h *harness) holdEvents(tok string, tenant model.TenantID, id string) []map[string]any {
	h.t.Helper()
	r := h.do("GET", s138Base+"/holds/"+id+"/events", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		h.t.Fatalf("hold events = %d %s", r.code, r.raw)
	}
	return itemsOf(h.t, r)
}

func itemsOf(t *testing.T, r resp) []map[string]any {
	t.Helper()
	raw, _ := r.body["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		m, _ := it.(map[string]any)
		out = append(out, m)
	}
	return out
}

// classResult plucks one class's result out of a sweep summary body.
func classResult(t *testing.T, r resp, class string) map[string]any {
	t.Helper()
	raw, _ := r.body["classes"].([]any)
	for _, it := range raw {
		m, _ := it.(map[string]any)
		if m["data_class"] == class {
			return m
		}
	}
	return nil
}

func contains(actions []string, want string) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

// TestRetentionSweepPurgeCutoffCertificate is the §11 "retención purga" case: an
// approved purge schedule deletes only rows OLDER than the cutoff, seals an
// append-only retention_run certificate + the "compliance.retention.purge"
// self-audit, and a second pass is idempotent (no new deletions, no new cert).
func TestRetentionSweepPurgeCutoffCertificate(t *testing.T) {
	clock := &movableClock{t: time.Now().UTC()}
	gate := &stubApprovalGate{status: GateStatusApproved, ref: "ap-ret-1", approvers: []string{"user:approver"}}
	h := newHarness(t, WithClock(clock), WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	h.seedExtRows(tenant, voiceSessionStandInKind, 3, model.Record{
		"session_ref": "vs", "agent_ref": "ag-1", "duration_ms": int64(900),
	})

	r := h.putPolicy(adm, tenant, "voice.session", map[string]any{
		"retention_days": 30, "disposition": "purge", "basis": "ops retention schedule",
	})
	if r.code != http.StatusOK || r.body["enabled"] != true || r.body["approval_ref"] != "ap-ret-1" {
		t.Fatalf("enable purge = %d %s", r.code, r.raw)
	}

	// Rows are ~now and the cutoff is now-30d: nothing is old enough — the young
	// rows are respected and an empty pass seals NO certificate.
	sw := h.sweepNow(adm, tenant)
	if intOf(sw.body["purged"]) != 0 || h.countExtRows(tenant, voiceSessionStandInKind) != 3 {
		t.Fatalf("young rows must survive: %s", sw.raw)
	}
	if runs := h.listRuns(adm, tenant, ""); len(runs) != 0 {
		t.Fatalf("empty pass sealed a certificate: %v", runs)
	}

	// Age the rows past the window by moving the MODULE clock.
	clock.advance(40 * 24 * time.Hour)
	sw = h.sweepNow(adm, tenant)
	if intOf(sw.body["examined"]) != 3 || intOf(sw.body["purged"]) != 3 {
		t.Fatalf("aged sweep = %s", sw.raw)
	}
	if h.countExtRows(tenant, voiceSessionStandInKind) != 0 {
		t.Fatal("aged rows must be purged")
	}
	runs := h.listRuns(adm, tenant, "voice.session")
	if len(runs) != 1 {
		t.Fatalf("want 1 certificate, got %v", runs)
	}
	cert := runs[0]
	if intOf(cert["examined"]) != 3 || intOf(cert["purged"]) != 3 ||
		cert["skipped_class_hold"] == true || cert["trigger"] != "manual" {
		t.Fatalf("certificate = %v", cert)
	}
	if cert["manifest_hash"] == "" || intOf(cert["ledger_seq"]) <= 0 || cert["policy_id"] == "" {
		t.Fatalf("certificate must be ledger-anchored with a manifest hash: %v", cert)
	}
	acts := h.auditActions(tenant)
	if !contains(acts, "compliance.retention.policy.put") || !contains(acts, "compliance.retention.purge") {
		t.Fatalf("missing self-audits in %v", acts)
	}

	// Idempotent second pass: nothing left under the cutoff ⇒ no deletions, no cert.
	sw = h.sweepNow(adm, tenant)
	if intOf(sw.body["purged"]) != 0 {
		t.Fatalf("second pass must be idempotent: %s", sw.raw)
	}
	if runs := h.listRuns(adm, tenant, "voice.session"); len(runs) != 1 {
		t.Fatalf("second empty pass sealed another certificate: %v", runs)
	}
}

// TestRetentionEnablePurgeApprovalFlow is the §6 defensible-deletion pillar 2:
// pending ⇒ 202 + the policy persists DISABLED; an identical re-PUT after approval
// finds the grant and enables with the approval_ref; an approved decision without
// approver evidence or with a foreign plan hash is denied (defense in depth);
// retain never consults the gate; un-wired gate ⇒ deny-closed 503.
func TestRetentionEnablePurgeApprovalFlow(t *testing.T) {
	gate := &stubApprovalGate{status: GateStatusPending, ref: "ap-1"}
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	body := map[string]any{"retention_days": 100, "disposition": "purge", "basis": "records schedule"}
	r := h.putPolicy(adm, tenant, "agent.memory", body)
	if r.code != http.StatusAccepted || r.body["status"] != "pending_approval" || r.body["approval_ref"] != "ap-1" {
		t.Fatalf("pending put = %d %s", r.code, r.raw)
	}
	lst := h.do("GET", s138Base+"/retention/policies", adm, nil, tenantHdr(tenant))
	items := itemsOf(t, lst)
	if len(items) != 1 || items[0]["enabled"] != false {
		t.Fatalf("pending policy must persist disabled: %v", items)
	}

	gate.set(GateStatusApproved, "ap-1", "user:boss")
	r = h.putPolicy(adm, tenant, "agent.memory", body)
	if r.code != http.StatusOK || r.body["enabled"] != true || r.body["approval_ref"] != "ap-1" {
		t.Fatalf("approved re-put = %d %s", r.code, r.raw)
	}

	// Approved with ZERO approver evidence: the module's independent re-check denies.
	gate.set(GateStatusApproved, "ap-2")
	r = h.putPolicy(adm, tenant, "voice.session", map[string]any{"retention_days": 30, "disposition": "purge"})
	if r.code != http.StatusForbidden {
		t.Fatalf("no-approver-evidence put = %d %s", r.code, r.raw)
	}

	// Approved but bound to a DIFFERENT plan: anti-TOCTOU denies.
	gate.set(GateStatusApproved, "ap-3", "user:boss")
	gate.mu.Lock()
	gate.planHash = "stale"
	gate.mu.Unlock()
	r = h.putPolicy(adm, tenant, "voice.session", map[string]any{"retention_days": 30, "disposition": "purge"})
	if r.code != http.StatusForbidden {
		t.Fatalf("stale-plan put = %d %s", r.code, r.raw)
	}
	gate.mu.Lock()
	gate.planHash = ""
	gate.err = errors.New("bridge down")
	gate.mu.Unlock()
	r = h.putPolicy(adm, tenant, "voice.session", map[string]any{"retention_days": 30, "disposition": "purge"})
	if r.code != http.StatusInternalServerError {
		t.Fatalf("gate error must deny: %d %s", r.code, r.raw)
	}
	gate.mu.Lock()
	gate.err = nil
	gate.mu.Unlock()

	// Validation (§2): unknown class, bad window, non-purgeable class, bad verb.
	for _, bad := range []struct {
		class string
		body  map[string]any
	}{
		{"nope.class", map[string]any{"retention_days": 10, "disposition": "retain"}},
		{"voice.session", map[string]any{"retention_days": 0, "disposition": "retain"}},
		{"voice.session", map[string]any{"retention_days": 99999, "disposition": "retain"}},
		{"audit.ledger", map[string]any{"retention_days": 2555, "disposition": "purge"}},
		{"voice.session", map[string]any{"retention_days": 10, "disposition": "shred"}},
	} {
		if r := h.putPolicy(adm, tenant, bad.class, bad.body); r.code != http.StatusBadRequest {
			t.Fatalf("bad policy %v = %d %s", bad, r.code, r.raw)
		}
	}

	// Retain (and a purge kept disabled) never consults the gate.
	before := len(gate.requests())
	if r := h.putPolicy(adm, tenant, "audit.ledger", map[string]any{"retention_days": 2555, "disposition": "retain", "basis": "7y + WORM archival"}); r.code != http.StatusOK {
		t.Fatalf("retain put = %d %s", r.code, r.raw)
	}
	if r := h.putPolicy(adm, tenant, "finops.cost_sample", map[string]any{"retention_days": 730, "disposition": "purge", "enabled": false}); r.code != http.StatusOK {
		t.Fatalf("disabled purge put = %d %s", r.code, r.raw)
	}
	if len(gate.requests()) != before {
		t.Fatal("retain/disabled puts must not consult the approval gate")
	}

	// DELETE is ungated (stopping a purge is the safe direction) and self-audited.
	if r := h.do("DELETE", s138Base+"/retention/policies/finops.cost_sample", adm, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", s138Base+"/retention/policies/finops.cost_sample", adm, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("second delete = %d %s", r.code, r.raw)
	}
	if !contains(h.auditActions(tenant), "compliance.retention.policy.delete") {
		t.Fatal("delete must self-audit")
	}

	// Un-wired gate: deny-closed 503, the schedule persists DISABLED.
	h2 := newHarness(t)
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "beta")
	adm2 := h2.roleToken(admin2, tenant2, "a@x.io", "admin")
	r = h2.putPolicy(adm2, tenant2, "voice.session", map[string]any{"retention_days": 30, "disposition": "purge"})
	if r.code != http.StatusServiceUnavailable {
		t.Fatalf("unwired gate put = %d %s", r.code, r.raw)
	}
	items = itemsOf(t, h2.do("GET", s138Base+"/retention/policies", adm2, nil, tenantHdr(tenant2)))
	if len(items) != 1 || items[0]["enabled"] != false {
		t.Fatalf("unwired-deny policy must persist disabled: %v", items)
	}
}

// TestRetentionSweepHoldSemantics is the §11 "hold bloquea purga" matrix: a tenant
// hold skips every class (certified); a data_class hold skips that class; a mapped
// subject hold excludes fine-grained (counted); a RELATED subject kind without a
// mapping skips the whole class conservatively; an UNRELATED subject hold blocks
// nothing.
func TestRetentionSweepHoldSemantics(t *testing.T) {
	clock := &movableClock{t: time.Now().UTC()}
	gate := &stubApprovalGate{status: GateStatusApproved, ref: "ap-ret", approvers: []string{"user:approver"}}
	h := newHarness(t, WithClock(clock), WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	h.seedExtRows(tenant, knowledgeMemoryStandInKind, 2, model.Record{"agent_ref": "a-1", "mkey": "k1", "content": "x"})
	h.seedExtRows(tenant, knowledgeMemoryStandInKind, 2, model.Record{"agent_ref": "a-2", "mkey": "k2", "content": "x"})
	h.seedExtRows(tenant, sessionsLiveStandInKind, 2, model.Record{"session_ref": "s-1", "agent_ref": "a-1", "event_count": int64(1)})
	h.seedExtRows(tenant, sessionsTimelineStandInKind, 2, model.Record{"session_ref": "s-1", "at": model.NewTimestamp(time.Now()).String(), "kind": "tool"})
	h.seedExtRows(tenant, voiceSessionStandInKind, 2, model.Record{"session_ref": "v-1", "agent_ref": "a-1", "duration_ms": int64(5)})
	h.seedCostSample(tenant, 2)

	for _, class := range []string{"agent.memory", "session.timeline", "voice.session", "finops.cost_sample"} {
		if r := h.putPolicy(adm, tenant, class, map[string]any{"retention_days": 30, "disposition": "purge", "basis": "test schedule"}); r.code != http.StatusOK {
			t.Fatalf("enable %s = %d %s", class, r.code, r.raw)
		}
	}

	// (a) A tenant-wide hold vetoes EVERYTHING: whole-class skips, certified.
	tenantHold := h.createHold(adm, tenant, map[string]any{
		"matter_ref": "case-1", "reason": "litigation", "scope_kind": "tenant",
	})
	clock.advance(60 * 24 * time.Hour)
	sw := h.sweepNow(adm, tenant)
	if intOf(sw.body["purged"]) != 0 || intOf(sw.body["skipped_class_holds"]) != 4 {
		t.Fatalf("tenant hold sweep = %s", sw.raw)
	}
	if h.countExtRows(tenant, knowledgeMemoryStandInKind) != 4 || h.countExtRows(tenant, costSampleStandInKind) != 2 {
		t.Fatal("tenant hold must preserve every row")
	}
	for _, run := range h.listRuns(adm, tenant, "") {
		if run["skipped_class_hold"] != true || intOf(run["examined"]) != 0 {
			t.Fatalf("tenant-hold certificate = %v", run)
		}
	}

	// Release the tenant hold under dual-control, then the fine-grained matrix.
	gate.set(GateStatusApproved, "ap-rel", "user:a", "user:b")
	if r := h.do("POST", s138Base+"/holds/"+tenantHold+"/release", adm, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("release tenant hold = %d %s", r.code, r.raw)
	}

	// (b) class hold on voice.session; mapped subject hold (agent a-1) — fine
	// exclusion on agent.memory, conservative whole-class skip on session.timeline
	// (related kind, no column mapping); a user subject hold (u-9) is MAPPED on
	// agent.memory since (user_ref, scoped kind only) — it excludes nothing
	// here because no row carries that ref, and stays unrelated everywhere else.
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": "case-2", "reason": "voice preservation", "scope_kind": "data_class", "data_class": "voice.session",
	})
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": "case-3", "reason": "agent under investigation", "scope_kind": "subject", "subject_kind": "agent", "subject_ref": "a-1",
	})
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": "case-4", "reason": "unrelated subject", "scope_kind": "subject", "subject_kind": "user", "subject_ref": "u-9",
	})

	sw = h.sweepNow(adm, tenant)
	mem := classResult(t, sw, "agent.memory")
	if intOf(mem["examined"]) != 4 || intOf(mem["purged"]) != 2 || intOf(mem["excluded_held"]) != 2 {
		t.Fatalf("agent.memory result = %v", mem)
	}
	if st := classResult(t, sw, "session.timeline"); st["skipped_class_hold"] != true {
		t.Fatalf("session.timeline must skip conservatively (related agent hold, no mapping): %v", st)
	}
	if vs := classResult(t, sw, "voice.session"); vs["skipped_class_hold"] != true {
		t.Fatalf("voice.session must skip under its class hold: %v", vs)
	}
	if fc := classResult(t, sw, "finops.cost_sample"); intOf(fc["purged"]) != 2 {
		t.Fatalf("finops must purge despite the unrelated user hold: %v", fc)
	}
	if h.countExtRows(tenant, knowledgeMemoryStandInKind) != 2 {
		t.Fatal("held agent a-1's memory rows must survive")
	}
	if h.countExtRows(tenant, sessionsLiveStandInKind) != 2 || h.countExtRows(tenant, sessionsTimelineStandInKind) != 2 {
		t.Fatal("conservatively-skipped session rows must survive")
	}
	if h.countExtRows(tenant, voiceSessionStandInKind) != 2 {
		t.Fatal("class-held voice rows must survive")
	}
	if h.countExtRows(tenant, costSampleStandInKind) != 0 {
		t.Fatal("unheld finops rows must purge")
	}
	memRuns := h.listRuns(adm, tenant, "agent.memory")
	last := memRuns[len(memRuns)-1]
	if intOf(last["excluded_held"]) != 2 || intOf(last["purged"]) != 2 {
		t.Fatalf("agent.memory certificate must count the held exclusion: %v", last)
	}

	// Idempotence under a standing subject hold: the next pass deletes nothing.
	sw = h.sweepNow(adm, tenant)
	if intOf(sw.body["purged"]) != 0 {
		t.Fatalf("second pass must purge nothing: %s", sw.raw)
	}
}

// TestHoldCustodyAndReleaseFlow is the §11 "custodia del release" case: 202 while
// pending (custody release_requested sealed ONCE per approval), released only with
// ≥2 re-verified distinct approvers, the full set→release_requested→released trail
// with ledger anchors, findings and self-audits; <2 approvers and an un-wired gate
// deny.
func TestHoldCustodyAndReleaseFlow(t *testing.T) {
	gate := &stubApprovalGate{status: GateStatusPending, ref: "ap-h1"}
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	id := h.createHold(adm, tenant, map[string]any{
		"matter_ref": "case-7", "title": "Smith v. Acme", "reason": "litigation hold",
		"scope_kind": "subject", "subject_kind": "user", "subject_ref": "u-1",
	})
	if !contains(h.auditActions(tenant), "compliance.hold.set") {
		t.Fatal("hold set must self-audit")
	}
	h.waitFindings()
	foundSet := false
	for _, f := range h.deliveredFindings() {
		if f.Kind == "compliance_hold_set" {
			foundSet = true
		}
	}
	if !foundSet {
		t.Fatal("hold set must emit the compliance_hold_set finding")
	}
	ev := h.holdEvents(adm, tenant, id)
	if len(ev) != 1 || ev[0]["event"] != "set" || intOf(ev[0]["ledger_seq"]) <= 0 || ev[0]["ledger_hash"] == "" {
		t.Fatalf("custody after set = %v", ev)
	}

	// Pending: 202, the hold stays active, ONE release_requested custody event even
	// when polled twice.
	for i := 0; i < 2; i++ {
		r := h.do("POST", s138Base+"/holds/"+id+"/release", adm, map[string]any{"reason": "matter closed"}, tenantHdr(tenant))
		if r.code != http.StatusAccepted || r.body["status"] != "pending_approval" || r.body["approval_ref"] != "ap-h1" {
			t.Fatalf("pending release = %d %s", r.code, r.raw)
		}
	}
	if r := h.do("GET", s138Base+"/holds/"+id, adm, nil, tenantHdr(tenant)); r.body["status"] != "active" {
		t.Fatalf("hold must stay active while pending: %s", r.raw)
	}
	ev = h.holdEvents(adm, tenant, id)
	if len(ev) != 2 || ev[1]["event"] != "release_requested" {
		t.Fatalf("custody after pending polls = %v", ev)
	}
	if !contains(h.auditActions(tenant), "compliance.hold.release.request") {
		t.Fatal("release request must self-audit")
	}

	// Approved with ONE approver: the module's independent quorum re-check denies.
	gate.set(GateStatusApproved, "ap-h1", "user:only")
	if r := h.do("POST", s138Base+"/holds/"+id+"/release", adm, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("single-approver release = %d %s", r.code, r.raw)
	}
	// Duplicated principals are ONE distinct approver — still denied.
	gate.set(GateStatusApproved, "ap-h1", "user:only", "user:only")
	if r := h.do("POST", s138Base+"/holds/"+id+"/release", adm, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("duplicate-approver release = %d %s", r.code, r.raw)
	}

	// Two distinct humans: released, with the full custody evidence.
	gate.set(GateStatusApproved, "ap-h1", "user:alice", "user:bob")
	r := h.do("POST", s138Base+"/holds/"+id+"/release", adm, map[string]any{"reason": "matter closed"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["status"] != "released" || r.body["release_approval_ref"] != "ap-h1" {
		t.Fatalf("release = %d %s", r.code, r.raw)
	}
	ev = h.holdEvents(adm, tenant, id)
	if len(ev) != 3 || ev[2]["event"] != "released" || intOf(ev[2]["ledger_seq"]) <= 0 {
		t.Fatalf("custody after release = %v", ev)
	}
	apprs, _ := ev[2]["approvers"].([]any)
	if len(apprs) != 2 {
		t.Fatalf("released custody event must carry the 2 approvers: %v", ev[2])
	}
	if !contains(h.auditActions(tenant), "compliance.hold.release") {
		t.Fatal("release must self-audit")
	}
	h.waitFindings()
	foundRel := false
	for _, f := range h.deliveredFindings() {
		if f.Kind == "compliance_hold_released" && string(f.Severity) == "high" {
			foundRel = true
		}
	}
	if !foundRel {
		t.Fatal("release must emit the high compliance_hold_released finding")
	}

	// A released hold no longer covers its subject, and cannot be re-released.
	chk := h.do("GET", s138Base+"/holds/check?subject_kind=user&subject_ref=u-1", adm, nil, tenantHdr(tenant))
	if chk.code != http.StatusOK || chk.body["held"] != false {
		t.Fatalf("released hold must not cover: %s", chk.raw)
	}
	if r := h.do("POST", s138Base+"/holds/"+id+"/release", adm, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("re-release = %d %s", r.code, r.raw)
	}

	// Rejected gate: deny, the hold stays active.
	id2 := h.createHold(adm, tenant, map[string]any{
		"matter_ref": "case-8", "reason": "second matter", "scope_kind": "tenant",
	})
	gate.set(GateStatusRejected, "ap-h2")
	if r := h.do("POST", s138Base+"/holds/"+id2+"/release", adm, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("rejected release = %d %s", r.code, r.raw)
	}

	// Un-wired gate: deny-closed 503 (no_gate) — no emergency path exists.
	h2 := newHarness(t)
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "beta")
	adm2 := h2.roleToken(admin2, tenant2, "a@x.io", "admin")
	id3 := h2.createHold(adm2, tenant2, map[string]any{
		"matter_ref": "case-9", "reason": "hold", "scope_kind": "tenant",
	})
	if r := h2.do("POST", s138Base+"/holds/"+id3+"/release", adm2, nil, tenantHdr(tenant2)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("unwired release = %d %s", r.code, r.raw)
	}
}

// TestHoldOversizedIdentityFieldsRejected reproduces the clamp-truncation bug:
// clamping an over-length identity field rewrote it into a DIFFERENT identity
// (ellipsis appended), persisting an active hold that holdCovers' exact equality
// could never match — silent under-preservation. Identity fields (matter_ref,
// subject_kind, subject_ref) are REJECTED with 400 when over-length, never
// clamped; the exact-limit boundary still works end to end (stored verbatim and
// matched by the gate); display-only fields (title) keep clamping.
func TestHoldOversizedIdentityFieldsRejected(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")

	long := func(n int) string { return strings.Repeat("x", n) }

	for _, bad := range []map[string]any{
		{"matter_ref": "m", "reason": "r", "scope_kind": "subject", "subject_kind": "user", "subject_ref": long(maxRefLen + 1)},
		{"matter_ref": long(maxNameLen + 1), "reason": "r", "scope_kind": "tenant"},
		{"matter_ref": "m", "reason": "r", "scope_kind": "subject", "subject_kind": long(maxNameLen + 1), "subject_ref": "u-1"},
	} {
		if r := h.do("POST", s138Base+"/holds", adm, bad, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("oversized identity field must be rejected, got %d %s", r.code, r.raw)
		}
	}
	if items := itemsOf(t, h.do("GET", s138Base+"/holds", adm, nil, tenantHdr(tenant))); len(items) != 0 {
		t.Fatalf("a rejected hold must not persist: %v", items)
	}

	// Boundary pin: EXACTLY max-length identity fields are accepted, stored
	// verbatim and matched by the hold gate (no ellipsis rewrite).
	ref := long(maxRefLen)
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": long(maxNameLen), "reason": "r", "scope_kind": "subject",
		"subject_kind": "user", "subject_ref": ref,
	})
	r := h.do("GET", s138Base+"/holds/check?subject_kind=user&subject_ref="+ref, adm, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["held"] != true {
		t.Fatalf("exact-limit subject_ref must match its own hold: %d %s", r.code, r.raw)
	}

	// Display-only fields keep clamping: an oversized title is accepted and
	// truncated to maxNameLen runes + the ellipsis, never rejected.
	id := h.createHold(adm, tenant, map[string]any{
		"matter_ref": "m-t", "title": long(maxNameLen + 50), "reason": "r", "scope_kind": "tenant",
	})
	g := h.do("GET", s138Base+"/holds/"+id, adm, nil, tenantHdr(tenant))
	title, _ := g.body["title"].(string)
	if got := len([]rune(title)); got != maxNameLen+1 {
		t.Fatalf("title must clamp to %d runes + ellipsis, got %d", maxNameLen, got)
	}
}

// TestHoldReleaseRequestedCustodyDedupeRace reproduces the duplicate-seal race:
// the pending branch's read-then-insert guard cannot exclude a concurrent twin
// (two polls of the same pending approval can both pass findOne before either
// inserts), so without a schema backstop both sealed the append-only
// release_requested custody event. The unique (tenant_id, hold_id, event,
// approval_ref) index now makes the loser's insert surface store.ErrConflict —
// exactly what handleReleaseHold maps to already-sealed — and the trail holds
// ONE event per approval. A NEW approval ref stays a legitimate new event.
func TestHoldReleaseRequestedCustodyDedupeRace(t *testing.T) {
	gate := &stubApprovalGate{status: GateStatusPending, ref: "ap-race"}
	h := newHarness(t, WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")
	id := h.createHold(adm, tenant, map[string]any{
		"matter_ref": "case-race", "reason": "litigation", "scope_kind": "tenant",
	})

	// The race with the findOne guard bypassed: seal the same (hold, event,
	// approval_ref) custody row twice straight through the store — what two
	// interleaved handler transactions would do. The second MUST fail with the
	// unique-violation store.ErrConflict the handler treats as already-sealed.
	seal := func() error {
		return h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			return appendHoldEvent(context.Background(), sc, model.ID(id), holdEventReleaseRequested,
				"user:racer", "user", "", "matter closed", "ap-race", nil)
		})
	}
	if err := seal(); err != nil {
		t.Fatalf("first seal: %v", err)
	}
	if err := seal(); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate seal must hit the custody dedupe index (store.ErrConflict), got %v", err)
	}

	// The handler stays idempotent alongside the sealed event: polling the same
	// pending approval answers 202 and seals nothing new.
	r := h.do("POST", s138Base+"/holds/"+id+"/release", adm, map[string]any{"reason": "matter closed"}, tenantHdr(tenant))
	if r.code != http.StatusAccepted || r.body["approval_ref"] != "ap-race" {
		t.Fatalf("pending poll = %d %s", r.code, r.raw)
	}
	countReq := func(ref string) int {
		n := 0
		for _, e := range h.holdEvents(adm, tenant, id) {
			if e["event"] == holdEventReleaseRequested && e["approval_ref"] == ref {
				n++
			}
		}
		return n
	}
	if got := countReq("ap-race"); got != 1 {
		t.Fatalf("custody trail must hold exactly one release_requested per approval, got %d", got)
	}

	// A different approval (expiry ⇒ re-request ⇒ new ref) seals a new event.
	gate.set(GateStatusPending, "ap-race-2")
	if r := h.do("POST", s138Base+"/holds/"+id+"/release", adm, nil, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("new-approval poll = %d %s", r.code, r.raw)
	}
	if got := countReq("ap-race-2"); got != 1 {
		t.Fatalf("a new approval ref must seal its own release_requested, got %d", got)
	}
}

// TestHoldsCheckHTTP locks the §4/§5 hold-gate semantics on the HTTP face: the
// single matching rule (tenant ⇒ everything; data_class ⇒ exact class; subject ⇒
// exact pair) and the scope validation of POST /holds.
func TestHoldsCheckHTTP(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	check := func(query string) resp {
		t.Helper()
		return h.do("GET", s138Base+"/holds/check"+query, viewer, nil, tenantHdr(tenant))
	}

	// Subject hold: exact (kind, ref) pair only.
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": "m-1", "reason": "r", "scope_kind": "subject", "subject_kind": "agent", "subject_ref": "a-1",
	})
	if r := check("?subject_kind=agent&subject_ref=a-1"); r.code != http.StatusOK || r.body["held"] != true {
		t.Fatalf("subject check = %d %s", r.code, r.raw)
	}
	if r := check("?subject_kind=agent&subject_ref=a-2"); r.body["held"] != false {
		t.Fatalf("other subject must not be held: %s", r.raw)
	}
	if r := check("?data_class=agent.memory"); r.body["held"] != false {
		t.Fatalf("a subject hold must not cover a class-only check: %s", r.raw)
	}

	// Class hold: the exact class (and the §5 combined subject+class call).
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": "m-2", "reason": "r", "scope_kind": "data_class", "data_class": "agent.memory",
	})
	if r := check("?data_class=agent.memory"); r.body["held"] != true {
		t.Fatalf("class check = %s", r.raw)
	}
	if r := check("?data_class=voice.session"); r.body["held"] != false {
		t.Fatalf("other class must not be held: %s", r.raw)
	}
	r := check("?subject_kind=agent&subject_ref=a-2&data_class=agent.memory")
	if r.body["held"] != true {
		t.Fatalf("combined check must match the class hold: %s", r.raw)
	}
	holds, _ := r.body["holds"].([]any)
	if len(holds) != 1 {
		t.Fatalf("combined check must list exactly the class hold: %s", r.raw)
	}
	if hr, _ := holds[0].(map[string]any); hr["matter_ref"] != "m-2" || hr["scope_kind"] != "data_class" || hr["id"] == "" {
		t.Fatalf("hold ref shape = %v", holds[0])
	}

	// Tenant hold: covers everything, including the bare (tenant-only) check.
	h.createHold(adm, tenant, map[string]any{"matter_ref": "m-3", "reason": "r", "scope_kind": "tenant"})
	if r := check(""); r.body["held"] != true {
		t.Fatalf("tenant-wide check = %s", r.raw)
	}
	if r := check("?data_class=voice.session"); r.body["held"] != true {
		t.Fatalf("tenant hold must cover every class: %s", r.raw)
	}

	// Validation: a dangling subject pair, and the POST scope rules.
	if r := check("?subject_kind=agent"); r.code != http.StatusBadRequest {
		t.Fatalf("dangling subject_kind = %d", r.code)
	}
	for _, bad := range []map[string]any{
		{"matter_ref": "", "reason": "r", "scope_kind": "tenant"},
		{"matter_ref": "m", "reason": "", "scope_kind": "tenant"},
		{"matter_ref": "m", "reason": "r", "scope_kind": "weird"},
		{"matter_ref": "m", "reason": "r", "scope_kind": "data_class", "data_class": "nope.class"},
		{"matter_ref": "m", "reason": "r", "scope_kind": "subject", "subject_kind": "agent"},
		{"matter_ref": "m", "reason": "r", "scope_kind": "tenant", "data_class": "agent.memory"},
	} {
		if r := h.do("POST", s138Base+"/holds", adm, bad, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("bad hold %v = %d %s", bad, r.code, r.raw)
		}
	}

	// The status filter on the list face.
	lst := h.do("GET", s138Base+"/holds?status=active", viewer, nil, tenantHdr(tenant))
	if len(itemsOf(t, lst)) != 3 {
		t.Fatalf("active holds = %s", lst.raw)
	}
}

// TestCheckHoldGoSeam covers the exported Go gate (what the composition root
// adapts for knowledge): the same rule as HTTP, and fail-closed without a
// data handle.
func TestCheckHoldGoSeam(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adm := h.roleToken(admin, tenant, "a@x.io", "admin")
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": "m-1", "reason": "r", "scope_kind": "subject", "subject_kind": "kb", "subject_ref": "kb-9",
	})

	dec, err := h.mod.CheckHold(context.Background(), tenant, HoldSubject{Kind: "kb", Ref: "kb-9", DataClass: "knowledge.content"})
	if err != nil || !dec.Held || len(dec.Holds) != 1 || dec.Holds[0].MatterRef != "m-1" {
		t.Fatalf("CheckHold = %+v, %v", dec, err)
	}
	dec, err = h.mod.CheckHold(context.Background(), tenant, HoldSubject{Kind: "kb", Ref: "kb-other"})
	if err != nil || dec.Held {
		t.Fatalf("uncovered subject = %+v, %v", dec, err)
	}

	// No data handle ⇒ error ⇒ the consumer's deny (fail closed).
	if _, err := New().CheckHold(context.Background(), tenant, HoldSubject{Kind: "kb", Ref: "kb-9"}); err == nil {
		t.Fatal("CheckHold without a data handle must error (the consumer denies)")
	}
}

// TestRetentionClassesAndProviderFloor is the §7 annotate-not-reject behavior:
// un-wired ⇒ provider_floor_known=false (honest); wired ⇒ model_io classes carry
// the floor and policies disclose effective_disclosure_days = max(days, floor).
func TestRetentionClassesAndProviderFloor(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	classes := itemsOf(t, h.do("GET", s138Base+"/retention/classes", viewer, nil, tenantHdr(tenant)))
	if len(classes) != len(dataClassRegistry) {
		t.Fatalf("classes = %d, want %d", len(classes), len(dataClassRegistry))
	}
	byID := map[string]map[string]any{}
	for _, c := range classes {
		byID[c["id"].(string)] = c
	}
	if byID["agent.memory"]["provider_floor_known"] != false || byID["agent.memory"]["model_io"] != true {
		t.Fatalf("unwired agent.memory = %v", byID["agent.memory"])
	}
	if byID["audit.ledger"]["purgeable"] != false || intOf(byID["audit.ledger"]["recommended_days"]) != 2555 {
		t.Fatalf("audit.ledger = %v", byID["audit.ledger"])
	}
	if intOf(byID["session.timeline"]["recommended_days"]) != 365 {
		t.Fatalf("session.timeline advisory = %v", byID["session.timeline"])
	}

	// Wired floor source.
	h2 := newHarness(t, WithProviderRetention(stubProviderRetention{days: 30, source: "models.reference"}))
	admin2 := h2.adminLogin()
	tenant2 := h2.createOrg(admin2, "beta")
	adm2 := h2.roleToken(admin2, tenant2, "a@x.io", "admin")
	classes = itemsOf(t, h2.do("GET", s138Base+"/retention/classes", adm2, nil, tenantHdr(tenant2)))
	byID = map[string]map[string]any{}
	for _, c := range classes {
		byID[c["id"].(string)] = c
	}
	am := byID["agent.memory"]
	if am["provider_floor_known"] != true || intOf(am["provider_floor_days"]) != 30 || am["provider_floor_source"] != "models.reference" {
		t.Fatalf("wired agent.memory = %v", am)
	}
	if byID["finops.cost_sample"]["provider_floor_known"] != false {
		t.Fatalf("non-model_io class must not carry the floor: %v", byID["finops.cost_sample"])
	}

	// Policy disclosure: max(retention_days, floor) — only where the floor applies.
	if r := h2.putPolicy(adm2, tenant2, "session.timeline", map[string]any{"retention_days": 10, "disposition": "retain"}); r.code != http.StatusOK || intOf(r.body["effective_disclosure_days"]) != 30 {
		t.Fatalf("short policy disclosure = %s", r.raw)
	}
	if r := h2.putPolicy(adm2, tenant2, "voice.session", map[string]any{"retention_days": 365, "disposition": "retain"}); r.code != http.StatusOK || intOf(r.body["effective_disclosure_days"]) != 365 {
		t.Fatalf("long policy disclosure = %s", r.raw)
	}
	if r := h2.putPolicy(adm2, tenant2, "finops.cost_sample", map[string]any{"retention_days": 10, "disposition": "retain"}); r.code != http.StatusOK || intOf(r.body["effective_disclosure_days"]) != 0 {
		t.Fatalf("non-model_io policy must not disclose a floor: %s", r.raw)
	}
}

// TestS138RBACTiers locks the §3 permission tiers: viewer reads, ONLY admin
// administers (there is no write tier on the records-management plane).
func TestS138RBACTiers(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	for _, ok := range []struct {
		method, path, tok string
	}{
		{"GET", "/retention/classes", viewer},
		{"GET", "/retention/policies", viewer},
		{"GET", "/retention/runs", viewer},
		{"GET", "/holds", viewer},
		{"GET", "/holds/check", viewer},
	} {
		if r := h.do(ok.method, s138Base+ok.path, ok.tok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("%s %s as viewer = %d %s", ok.method, ok.path, r.code, r.raw)
		}
	}
	for _, deny := range []struct {
		method, path, tok string
		body              map[string]any
	}{
		{"PUT", "/retention/policies/voice.session", viewer, map[string]any{"retention_days": 30, "disposition": "retain"}},
		{"PUT", "/retention/policies/voice.session", editor, map[string]any{"retention_days": 30, "disposition": "retain"}},
		{"DELETE", "/retention/policies/voice.session", editor, nil},
		{"POST", "/retention/sweep", editor, nil},
		{"POST", "/holds", viewer, map[string]any{"matter_ref": "m", "reason": "r", "scope_kind": "tenant"}},
		{"POST", "/holds", editor, map[string]any{"matter_ref": "m", "reason": "r", "scope_kind": "tenant"}},
		{"POST", "/holds/" + model.NewID().String() + "/release", editor, nil},
	} {
		if r := h.do(deny.method, s138Base+deny.path, deny.tok, deny.body, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Fatalf("%s %s must be admin-tier; got %d %s", deny.method, deny.path, r.code, r.raw)
		}
	}
}

// TestRetentionSweepScopedMemorySubjectHolds proves the fine-grained hold
// semantics on the agent.memory class: a USER (or SESSION) subject hold excludes
// exactly the scoped rows carrying that ref — the agent-global kind reads the
// unmapped column as "" and keeps purging (a user hold never freezes the class).
func TestRetentionSweepScopedMemorySubjectHolds(t *testing.T) {
	clock := &movableClock{t: time.Now().UTC()}
	gate := &stubApprovalGate{status: GateStatusApproved, ref: "ap-ret", approvers: []string{"user:approver"}}
	h := newHarness(t, WithClock(clock), WithApprovalGate(gate))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "scoped-holds")
	adm := h.roleToken(admin, tenant, "adm@x.io", "admin")

	h.seedExtRows(tenant, scopedMemoryStandInKind, 2, model.Record{
		"agent_ref": "a1", "user_ref": "u-held", "session_ref": "", "mkey": "k", "content": "held user",
	})
	h.seedExtRows(tenant, scopedMemoryStandInKind, 1, model.Record{
		"agent_ref": "a1", "user_ref": "u-free", "session_ref": "s-held", "mkey": "k", "content": "held session",
	})
	h.seedExtRows(tenant, scopedMemoryStandInKind, 1, model.Record{
		"agent_ref": "a1", "user_ref": "u-free", "session_ref": "", "mkey": "k2", "content": "free",
	})
	h.seedExtRows(tenant, knowledgeMemoryStandInKind, 2, model.Record{
		"agent_ref": "a1", "mkey": "shared", "content": "agent-global",
	})

	if r := h.putPolicy(adm, tenant, "agent.memory", map[string]any{
		"retention_days": 30, "disposition": "purge", "basis": "test schedule",
	}); r.code != http.StatusOK {
		t.Fatalf("enable policy = %d %s", r.code, r.raw)
	}
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": "case-u", "reason": "user under DSAR dispute", "scope_kind": "subject",
		"subject_kind": "user", "subject_ref": "u-held",
	})
	h.createHold(adm, tenant, map[string]any{
		"matter_ref": "case-s", "reason": "session under investigation", "scope_kind": "subject",
		"subject_kind": "session", "subject_ref": "s-held",
	})

	clock.advance(60 * 24 * time.Hour)
	sw := h.sweepNow(adm, tenant)
	mem := classResult(t, sw, "agent.memory")
	if mem["skipped_class_hold"] == true {
		t.Fatalf("user/session holds must exclude finely, never skip the class: %v", mem)
	}
	// 6 rows examined: 2 u-held excluded + 1 s-held excluded; 1 free scoped +
	// 2 agent-global purged.
	if intOf(mem["examined"]) != 6 || intOf(mem["purged"]) != 3 || intOf(mem["excluded_held"]) != 3 {
		t.Fatalf("agent.memory sweep = %v, want examined 6 / purged 3 / excluded 3", mem)
	}
	if n := h.countExtRows(tenant, scopedMemoryStandInKind); n != 3 {
		t.Fatalf("scoped rows surviving = %d, want 3 (the held namespaces)", n)
	}
	if n := h.countExtRows(tenant, knowledgeMemoryStandInKind); n != 0 {
		t.Fatalf("agent-global rows surviving = %d, want 0 (no class freeze)", n)
	}
}
