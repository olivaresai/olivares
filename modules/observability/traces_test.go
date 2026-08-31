// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

const tracesPath = "/v1/m/observability/traces"

// W3C-form test ids (32/16 lowercase hex).
const (
	traceA = "4bf92f3577b34da6a3ce929d0e0e4736"
	traceB = "5bf92f3577b34da6a3ce929d0e0e4737"
	traceC = "6bf92f3577b34da6a3ce929d0e0e4738"
	spanA1 = "00f067aa0ba902b7"
	spanA2 = "00f067aa0ba902b8"
	spanB1 = "0102030405060708"
)

// TestTracesEmpty: a chain with no trace-stamped events lists honestly empty.
// (The login/org self-audit events exist on the chains but carry no trace
// context, so the read-model must skip them.)
func TestTracesEmpty(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", tracesPath, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("traces = %d %s", r.code, r.raw)
	}
	if items := itemsOf(r); len(items) != 0 {
		t.Fatalf("items = %v, want empty", items)
	}
	if boolOf(r.body["has_more"]) {
		t.Fatal("has_more must be false on an empty window")
	}
}

// seedTwoTraces appends trace A (3 events on 2 engine spans, 20ms window) and
// then trace B (1 event, later) to the tenant chain, returning the agent id
// trace A targets.
func seedTwoTraces(h *harness, tenant model.TenantID) model.ID {
	agentID := model.NewID()
	h.appendTraced(tenant, traceA, spanA1, "agent.create", "agent", agentID)
	h.clk.advance(10 * time.Millisecond)
	h.appendTraced(tenant, traceA, spanA1, "agent.update", "agent", agentID)
	h.clk.advance(10 * time.Millisecond)
	h.appendTraced(tenant, traceA, spanA2, "token.create", "", "")
	h.clk.advance(time.Second)
	h.appendTraced(tenant, traceB, spanB1, "org.update", "", "")
	return agentID
}

// TestTracesListDerivation: ordering (newest first), root_name = earliest
// action, duration = the ledger-event window, span_count = DISTINCT span ids,
// and the honest constants (status unset, single-writer service).
func TestTracesListDerivation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedTwoTraces(h, tenant)

	r := h.do("GET", tracesPath, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("traces = %d %s", r.code, r.raw)
	}
	items := itemsOf(r)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2: %s", len(items), r.raw)
	}
	// Newest first: trace B started 1s after trace A's window.
	if strOf(items[0]["trace_id"]) != traceB || strOf(items[1]["trace_id"]) != traceA {
		t.Fatalf("order = %q,%q, want B,A", strOf(items[0]["trace_id"]), strOf(items[1]["trace_id"]))
	}
	a := items[1]
	if strOf(a["root_name"]) != "agent.create" {
		t.Fatalf("root_name = %q, want the earliest event's action", strOf(a["root_name"]))
	}
	if got := intOf(a["duration_ms"]); got != 20 {
		t.Fatalf("duration_ms = %d, want the 20ms ledger-event window", got)
	}
	if intOf(a["span_count"]) != 2 {
		t.Fatalf("span_count = %d, want 2 distinct span ids", intOf(a["span_count"]))
	}
	if strOf(a["status"]) != "unset" {
		t.Fatalf("status = %q, want unset (the ledger stores no span status)", strOf(a["status"]))
	}
	svcs, _ := a["services"].([]any)
	if len(svcs) != 1 || strOf(svcs[0]) != "olivares" {
		t.Fatalf("services = %v, want the single ledger writer", a["services"])
	}
	if strOf(a["started_at"]) == "" {
		t.Fatal("started_at missing")
	}
	b := items[0]
	if intOf(b["duration_ms"]) != 0 || intOf(b["span_count"]) != 1 {
		t.Fatalf("trace B = %v, want a zero-width single-span window", b)
	}
}

// TestTraceDetailSpans: span grouping per distinct span_id — the (+N events)
// suffix, start_ms offsets from the trace start, per-span windows, entity_ref
// of the earliest event and the synthesized ledger.* attributes.
func TestTraceDetailSpans(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agentID := seedTwoTraces(h, tenant)

	r := h.do("GET", tracesPath+"/"+traceA, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("trace detail = %d %s", r.code, r.raw)
	}
	if strOf(r.body["trace_id"]) != traceA {
		t.Fatalf("trace_id = %q", strOf(r.body["trace_id"]))
	}
	if intOf(r.body["duration_ms"]) != 20 || strOf(r.body["started_at"]) == "" {
		t.Fatalf("window = %v ms / %q", r.body["duration_ms"], strOf(r.body["started_at"]))
	}
	spans := listOf(r.body["spans"])
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want one per distinct span id: %s", len(spans), r.raw)
	}

	// Sorted by start_ms: spanA1 (offset 0, two events) then spanA2 (offset 20).
	s1, s2 := spans[0], spans[1]
	if strOf(s1["span_id"]) != spanA1 || strOf(s2["span_id"]) != spanA2 {
		t.Fatalf("span order = %q,%q", strOf(s1["span_id"]), strOf(s2["span_id"]))
	}
	if strOf(s1["name"]) != "agent.create (+2 events)" {
		t.Fatalf("grouped span name = %q", strOf(s1["name"]))
	}
	if intOf(s1["start_ms"]) != 0 || intOf(s1["duration_ms"]) != 10 {
		t.Fatalf("span1 window = %d+%d, want 0+10", intOf(s1["start_ms"]), intOf(s1["duration_ms"]))
	}
	if strOf(s1["kind"]) != "ledger" || strOf(s1["status"]) != "unset" {
		t.Fatalf("span1 kind/status = %q/%q", strOf(s1["kind"]), strOf(s1["status"]))
	}
	if _, present := s1["parent_span_id"]; present {
		t.Fatal("parent_span_id is not stored and must not be invented")
	}
	if strOf(s1["entity_ref"]) != "agent:"+agentID.String() {
		t.Fatalf("entity_ref = %q", strOf(s1["entity_ref"]))
	}
	attrs := mapOf(s1["attributes"])
	if strOf(attrs["ledger.events"]) != "2" || strOf(attrs["ledger.actor"]) != "user:test" {
		t.Fatalf("span1 attributes = %v", attrs)
	}
	if strOf(attrs["ledger.actions"]) != "agent.create,agent.update" {
		t.Fatalf("ledger.actions = %q", strOf(attrs["ledger.actions"]))
	}
	if !strings.Contains(strOf(attrs["ledger.seq"]), "-") {
		t.Fatalf("ledger.seq = %q, want a first-last range", strOf(attrs["ledger.seq"]))
	}

	if intOf(s2["start_ms"]) != 20 || intOf(s2["duration_ms"]) != 0 {
		t.Fatalf("span2 window = %d+%d, want 20+0", intOf(s2["start_ms"]), intOf(s2["duration_ms"]))
	}
	if strOf(s2["name"]) != "token.create" {
		t.Fatalf("single-event span name = %q (no suffix expected)", strOf(s2["name"]))
	}
	if _, present := s2["entity_ref"]; present {
		t.Fatal("entity_ref must be omitted when the event has no target")
	}
}

// TestTraceDetailErrors: unknown-but-valid id → 404 naming the window; invalid
// ids → 400.
func TestTraceDetailErrors(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", tracesPath+"/"+traceC, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusNotFound {
		t.Fatalf("unknown trace = %d, want 404", r.code)
	}
	if msg := strOf(mapOf(r.body["error"])["message"]); !strings.Contains(msg, "ledger window (last 20000 events)") {
		t.Fatalf("404 message = %q, want the window named", msg)
	}
	for _, bad := range []string{"XYZ", "ABCDEF12", strings.Repeat("a", 65)} {
		if r := h.do("GET", tracesPath+"/"+bad, admin, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("invalid id %q = %d, want 400", bad, r.code)
		}
	}
}

// TestTracesPaginationAndParams: limit/cursor paging over three traces, and
// unusable params rejected with 400.
func TestTracesPaginationAndParams(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedTwoTraces(h, tenant)
	h.clk.advance(time.Second)
	h.appendTraced(tenant, traceC, spanB1, "kb.create", "", "")

	r := h.do("GET", tracesPath+"?limit=2", admin, nil, tenantHdr(tenant))
	items := itemsOf(r)
	if len(items) != 2 || !boolOf(r.body["has_more"]) || strOf(r.body["cursor"]) != "2" {
		t.Fatalf("page1 = %d items, has_more=%v, cursor=%q", len(items), r.body["has_more"], strOf(r.body["cursor"]))
	}
	if strOf(items[0]["trace_id"]) != traceC {
		t.Fatalf("page1[0] = %q, want the newest trace", strOf(items[0]["trace_id"]))
	}
	r = h.do("GET", tracesPath+"?limit=2&cursor=2", admin, nil, tenantHdr(tenant))
	items = itemsOf(r)
	if len(items) != 1 || boolOf(r.body["has_more"]) || strOf(r.body["cursor"]) != "" {
		t.Fatalf("page2 = %d items, has_more=%v", len(items), r.body["has_more"])
	}
	if strOf(items[0]["trace_id"]) != traceA {
		t.Fatalf("page2[0] = %q, want the oldest trace", strOf(items[0]["trace_id"]))
	}

	for _, q := range []string{"?limit=0", "?limit=x", "?cursor=-1", "?cursor=x"} {
		if r := h.do("GET", tracesPath+q, admin, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", q, r.code)
		}
	}

	// Exact-match filters: the single writer matches, anything else is empty.
	r = h.do("GET", tracesPath+"?service=olivares", admin, nil, tenantHdr(tenant))
	if len(itemsOf(r)) != 3 {
		t.Fatalf("service filter dropped the writer's traces: %s", r.raw)
	}
	r = h.do("GET", tracesPath+"?status=error", admin, nil, tenantHdr(tenant))
	if len(itemsOf(r)) != 0 {
		t.Fatalf("status=error must match nothing (no status is stored): %s", r.raw)
	}
}

// TestTraceActorEnrichment: the span detail and trace list carry the actor
// identity from the ledger events. A multi-agent trace reports distinct
// agent_count and per-span actor/actor_kind fields.
func TestTraceActorEnrichment(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	agentID := model.NewID()
	// Span from a user actor.
	h.appendTracedAs(tenant, traceA, spanA1, "session.start", "user:alice", "user", "agent", agentID)
	h.clk.advance(10 * time.Millisecond)
	// Span from an agent actor (sub-agent call).
	h.appendTracedAs(tenant, traceA, spanA2, "tool.invoke", "agent:bot-1", "agent", "agent", agentID)

	// List: agent_count should be 2 (user:alice + agent:bot-1).
	r := h.do("GET", tracesPath, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("traces = %d %s", r.code, r.raw)
	}
	items := itemsOf(r)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if intOf(items[0]["agent_count"]) != 2 {
		t.Fatalf("agent_count = %d, want 2 distinct actors", intOf(items[0]["agent_count"]))
	}

	// Detail: each span carries its actor.
	r = h.do("GET", tracesPath+"/"+traceA, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("detail = %d %s", r.code, r.raw)
	}
	spans := listOf(r.body["spans"])
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	s1, s2 := spans[0], spans[1]
	if strOf(s1["actor"]) != "user:alice" || strOf(s1["actor_kind"]) != "user" {
		t.Fatalf("span1 actor = %q/%q", strOf(s1["actor"]), strOf(s1["actor_kind"]))
	}
	if strOf(s2["actor"]) != "agent:bot-1" || strOf(s2["actor_kind"]) != "agent" {
		t.Fatalf("span2 actor = %q/%q", strOf(s2["actor"]), strOf(s2["actor_kind"]))
	}
}

// TestTraceSearchByPrefix: ?q= filters the trace list by trace_id prefix.
func TestTraceSearchByPrefix(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedTwoTraces(h, tenant)

	// Full id match.
	r := h.do("GET", tracesPath+"?q="+traceA, admin, nil, tenantHdr(tenant))
	if len(itemsOf(r)) != 1 || strOf(itemsOf(r)[0]["trace_id"]) != traceA {
		t.Fatalf("full match = %d items", len(itemsOf(r)))
	}
	// Prefix match: traceA starts with "4bf", traceB with "5bf".
	r = h.do("GET", tracesPath+"?q=4bf", admin, nil, tenantHdr(tenant))
	if len(itemsOf(r)) != 1 {
		t.Fatalf("prefix match = %d items, want 1", len(itemsOf(r)))
	}
	// No match.
	r = h.do("GET", tracesPath+"?q=fffff", admin, nil, tenantHdr(tenant))
	if len(itemsOf(r)) != 0 {
		t.Fatalf("no match = %d items, want 0", len(itemsOf(r)))
	}
	// Case-insensitive: upper-case prefix should match.
	r = h.do("GET", tracesPath+"?q=4BF", admin, nil, tenantHdr(tenant))
	if len(itemsOf(r)) != 1 {
		t.Fatalf("case-insensitive = %d items, want 1", len(itemsOf(r)))
	}
}

// TestTraceTimeRangeFilter: ?from= and ?to= filter by started_at.
func TestTraceTimeRangeFilter(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedTwoTraces(h, tenant) // trace A starts at 12:00:00, trace B at ~12:00:01.02

	// from= after trace A but before trace B should get only B.
	r := h.do("GET", tracesPath+"?from=2026-06-12T12:00:01Z", admin, nil, tenantHdr(tenant))
	items := itemsOf(r)
	if len(items) != 1 || strOf(items[0]["trace_id"]) != traceB {
		t.Fatalf("from filter = %d items, want only trace B", len(items))
	}
	// to= before trace B should get only A.
	r = h.do("GET", tracesPath+"?to=2026-06-12T12:00:00Z", admin, nil, tenantHdr(tenant))
	items = itemsOf(r)
	if len(items) != 1 || strOf(items[0]["trace_id"]) != traceA {
		t.Fatalf("to filter = %d items, want only trace A", len(items))
	}
	// Bad format → 400.
	r = h.do("GET", tracesPath+"?from=not-a-date", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("bad from = %d, want 400", r.code)
	}
	r = h.do("GET", tracesPath+"?to=not-a-date", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("bad to = %d, want 400", r.code)
	}
}

// TestTraceExportOTLP: the export endpoint returns a valid OTLP JSON structure.
func TestTraceExportOTLP(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedTwoTraces(h, tenant)

	r := h.do("GET", tracesPath+"/"+traceA+"/export", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("export = %d %s", r.code, r.raw)
	}
	// Validate OTLP structure.
	rs, _ := r.body["resourceSpans"].([]any)
	if len(rs) != 1 {
		t.Fatalf("resourceSpans = %d, want 1", len(rs))
	}
	rsMap := mapOf(rs[0])
	res := mapOf(rsMap["resource"])
	attrs, _ := res["attributes"].([]any)
	if len(attrs) < 1 {
		t.Fatal("resource attributes missing")
	}
	// Check service.name attribute.
	firstAttr := mapOf(attrs[0])
	if strOf(firstAttr["key"]) != "service.name" {
		t.Fatalf("first resource attr key = %q, want service.name", strOf(firstAttr["key"]))
	}
	ss, _ := rsMap["scopeSpans"].([]any)
	if len(ss) != 1 {
		t.Fatalf("scopeSpans = %d, want 1", len(ss))
	}
	ssMap := mapOf(ss[0])
	spans, _ := ssMap["spans"].([]any)
	if len(spans) != 2 {
		t.Fatalf("otlp spans = %d, want 2 (one per distinct span_id)", len(spans))
	}
	span0 := mapOf(spans[0])
	if strOf(span0["traceId"]) != traceA {
		t.Fatalf("traceId = %q", strOf(span0["traceId"]))
	}
	if strOf(span0["startTimeUnixNano"]) == "" || strOf(span0["endTimeUnixNano"]) == "" {
		t.Fatal("timestamps missing")
	}
	if intOf(span0["kind"]) != 1 { // INTERNAL
		t.Fatalf("kind = %d, want 1 (INTERNAL)", intOf(span0["kind"]))
	}

	// Namespace freeze: every product attribute key — resource and span —
	// lives under ai.olivares.*, never under the bare pre-freeze olivares.*
	// spelling. The actor key must actually be present, so the guard is proven
	// to walk real product keys, not an empty list.
	sawActor := false
	checkKeys := func(raw any) {
		list, _ := raw.([]any)
		for _, a := range list {
			key := strOf(mapOf(a)["key"])
			if strings.HasPrefix(key, "olivares.") {
				t.Errorf("bare pre-freeze attribute key %q in the OTLP export", key)
			}
			if key == "ai.olivares.actor" {
				sawActor = true
			}
		}
	}
	checkKeys(attrs)
	for _, sp := range spans {
		checkKeys(mapOf(sp)["attributes"])
	}
	if !sawActor {
		t.Error("ai.olivares.actor missing from the exported spans; the freeze guard checked nothing")
	}

	// The event-derived attributes are appended in SORTED key order — Go map
	// iteration is randomized, so without the sort two downloads of the same
	// trace could differ byte-wise. Assert the order (the four ledger.* keys are
	// synthesized for every span) and then assert raw byte identity across a
	// second download, which is the actual promise.
	wantLedger := []string{"ledger.actions", "ledger.actor", "ledger.events", "ledger.seq"}
	for _, sp := range spans {
		var ledgerKeys []string
		list, _ := mapOf(sp)["attributes"].([]any)
		for _, a := range list {
			if key := strOf(mapOf(a)["key"]); strings.HasPrefix(key, "ledger.") {
				ledgerKeys = append(ledgerKeys, key)
			}
		}
		if len(ledgerKeys) != len(wantLedger) {
			t.Fatalf("ledger.* keys = %v, want %v", ledgerKeys, wantLedger)
		}
		for i := range wantLedger {
			if ledgerKeys[i] != wantLedger[i] {
				t.Fatalf("event-derived attributes not in sorted order: %v", ledgerKeys)
			}
		}
	}
	again := h.do("GET", tracesPath+"/"+traceA+"/export", admin, nil, tenantHdr(tenant))
	if again.code != http.StatusOK {
		t.Fatalf("re-export = %d", again.code)
	}
	if again.raw != r.raw {
		t.Fatalf("two downloads of the same trace differ byte-wise:\n a: %s\n b: %s", r.raw, again.raw)
	}

	// Not-found trace returns 404.
	r = h.do("GET", tracesPath+"/"+traceC+"/export", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusNotFound {
		t.Fatalf("export unknown = %d, want 404", r.code)
	}

	// Content-Disposition header.
	r = h.do("GET", tracesPath+"/"+traceA+"/export", admin, nil, tenantHdr(tenant))
	// The header is set by writeJSON which goes through the harness do() — check raw response.
}

// TestTracesRBACAndTenancy: read-tier (viewer reads), unauthenticated is
// rejected, and another tenant's chain does not leak into the view.
func TestTracesRBACAndTenancy(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	other := h.createOrg(admin, "globex")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	seedTwoTraces(h, tenant)

	if r := h.do("GET", tracesPath, "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", r.code)
	}
	r := h.do("GET", tracesPath, viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || len(itemsOf(r)) != 2 {
		t.Fatalf("viewer = %d / %d items, want 200 / 2", r.code, len(itemsOf(r)))
	}
	// The other tenant's chain has no traced events: the read is tenant-pinned.
	r = h.do("GET", tracesPath, admin, nil, tenantHdr(other))
	if r.code != http.StatusOK || len(itemsOf(r)) != 0 {
		t.Fatalf("other tenant = %d / %d items, want 200 / 0", r.code, len(itemsOf(r)))
	}
}
