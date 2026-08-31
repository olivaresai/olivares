// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package capabilities_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// TestServerCatalogFromIntrospection is the headline integration test: the
// connectors' introspection + cooperative edges flow through the real runtime and
// bus; the inventory module materializes the core entities and the capabilities
// module builds the wiring/health overlays; the live catalog then surfaces MCP
// servers with transport/tools/UNTRUSTED annotations, the agent→capability wiring
// graph, and basic connection health — never trusting an annotation.
func TestServerCatalogFromIntrospection(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	t1, t2, t3 := base, base.Add(time.Minute), base.Add(2*time.Minute)
	src := newFakeSource([]sdkmodel.Observation{
		// MCP introspection (UNTRUSTED declared capability): github exposes tools +
		// a prompt. readOnlyHint=true ⇒ Mode=read; false/absent ⇒ Mode=readwrite.
		edge("mcp_server", "github", "mcp.tool", "github/create_issue", sdkmodel.ModeReadWrite, sdkmodel.SignalMCPAnnotation, sdkmodel.ConfidenceApproximate, "create_issue", t1),
		edge("mcp_server", "github", "mcp.tool", "github/get_issue", sdkmodel.ModeRead, sdkmodel.SignalMCPAnnotation, sdkmodel.ConfidenceApproximate, "get_issue", t1),
		edge("mcp_server", "github", "mcp.prompt", "github/triage", sdkmodel.ModeUnknown, sdkmodel.SignalMCPAnnotation, sdkmodel.ConfidenceApproximate, "", t1),
		// Cooperative (OTEL): session sess-1 uses create_issue and connects to github.
		edge("session", "sess-1", "mcp.tool", "github/create_issue", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "create_issue", t2),
		edge("session", "sess-1", "mcp.server", "github", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "", t2),
		// flaky: a connection, then a later health-down finding ⇒ ends down.
		edge("session", "sess-1", "mcp.server", "flaky", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "", t1),
		healthFinding("mcp.server", "flaky", "introspection failed", sdkmodel.SeverityMedium, t3),
	})

	bus, stats := newTrackedBus(t)
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(h.inv, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(h.cap, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddSource(src, sdk.Config{}, tenant.String()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = rt.Stop(ctx)
		_ = bus.Close()
	}()

	h.waitObservations(src, stats, len(src.obs))
	h.waitServers(tenant, 2) // github + flaky materialized by inventory
	h.waitWiring(tenant, 6)  // capabilities built the connection graph

	// --- live server catalog ---
	r := h.do("GET", "/v1/m/capabilities/servers", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("servers = %d %s", r.code, r.raw)
	}
	servers := map[string]map[string]any{}
	for _, it := range items(r) {
		m := it.(map[string]any)
		servers[m["name"].(string)] = m
	}
	gh, ok := servers["github"]
	if !ok {
		t.Fatalf("github not in catalog: %s", r.raw)
	}
	if gh["connection"] != "connected" {
		t.Errorf("github connection = %v, want connected", gh["connection"])
	}
	if gh["tool_count"].(float64) < 2 {
		t.Errorf("github tool_count = %v, want >= 2", gh["tool_count"])
	}
	if gh["has_config"] != false {
		t.Errorf("github has_config = %v, want false", gh["has_config"])
	}
	if fl, ok := servers["flaky"]; !ok {
		t.Error("flaky not in catalog")
	} else if fl["connection"] != "down" {
		t.Errorf("flaky connection = %v, want down (a health-down finding postdates the connection)", fl["connection"])
	}

	// --- tools: annotations surfaced as UNTRUSTED, never as truth ---
	r = h.do("GET", "/v1/m/capabilities/tools", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("tools = %d %s", r.code, r.raw)
	}
	tools := map[string]map[string]any{}
	for _, it := range items(r) {
		m := it.(map[string]any)
		tools[m["name"].(string)] = m
		if m["annotation_trust"] != "untrusted" {
			t.Errorf("tool %v annotation_trust = %v, want untrusted", m["name"], m["annotation_trust"])
		}
	}
	if ci, ok := tools["create_issue"]; !ok {
		t.Error("create_issue tool missing")
	} else if ci["read_only_hint"] != false {
		t.Errorf("create_issue read_only_hint = %v, want false", ci["read_only_hint"])
	}
	if gi, ok := tools["get_issue"]; !ok {
		t.Error("get_issue tool missing")
	} else if gi["read_only_hint"] != true {
		t.Errorf("get_issue read_only_hint = %v, want true (readOnlyHint annotation, still UNTRUSTED)", gi["read_only_hint"])
	}

	// --- server detail: tools/skills/consumers + health ---
	githubID := gh["id"].(string)
	r = h.do("GET", "/v1/m/capabilities/servers/"+githubID, viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("server detail = %d %s", r.code, r.raw)
	}
	if got := len(r.body["tools"].([]any)); got < 2 {
		t.Errorf("detail tools = %d, want >= 2", got)
	}
	if got := len(r.body["skills"].([]any)); got != 1 {
		t.Errorf("detail skills = %d, want 1 (triage)", got)
	}
	consumers := r.body["consumers"].([]any)
	foundSess := false
	for _, c := range consumers {
		cm := c.(map[string]any)
		if cm["kind"] == "session" && cm["ref"] == "sess-1" {
			foundSess = true
		}
	}
	if !foundSess {
		t.Errorf("github consumers missing session sess-1: %v", consumers)
	}
	if hm, ok := r.body["health"].(map[string]any); !ok || hm["status"] != "connected" {
		t.Errorf("github health = %v, want connected", r.body["health"])
	}

	// --- wiring graph (distinct from the R/RW access graph) ---
	r = h.do("GET", "/v1/m/capabilities/wiring?capability_kind=mcp_server&capability_ref=github", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("wiring = %d %s", r.code, r.raw)
	}
	edges := r.body["edges"].([]any)
	sawSessionToServer := false
	for _, e := range edges {
		em := e.(map[string]any)
		if em["origin_kind"] == "session" && em["origin_ref"] == "sess-1" && em["capability_ref"] == "github" {
			sawSessionToServer = true
		}
	}
	if !sawSessionToServer {
		t.Errorf("wiring graph missing session→github edge: %s", r.raw)
	}
}

// runEdges starts a runtime with inventory + capabilities and a fake source
// emitting obs for the tenant, then waits for every delivery to complete and
// checks the expected wiring cardinality.
func (h *harness) runEdges(t *testing.T, tenant model.TenantID, obs []sdkmodel.Observation, wantWiring int) func() {
	t.Helper()
	bus, stats := newTrackedBus(t)
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(h.inv, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(h.cap, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	src := newFakeSource(obs)
	if err := rt.AddSource(src, sdk.Config{}, tenant.String()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	stop := func() {
		_ = rt.Stop(ctx)
		_ = bus.Close()
	}
	t.Cleanup(stop)
	h.waitObservations(src, stats, len(obs))
	h.waitWiring(tenant, wantWiring)
	return stop
}

// TestHealthForwardOnlyOrdering proves the forward-only health invariant in both
// directions: a stale "connected" arriving after a newer "down" must NOT resurrect
// the server, while a "connected" newer than the last "down" recovers it. Bus
// delivery is at-least-once and unordered, so this is a real condition.
func TestHealthForwardOnlyOrdering(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	t0, t1, t3 := base, base.Add(time.Minute), base.Add(3*time.Minute)
	obs := []sdkmodel.Observation{
		// zombie: connect, then DOWN at t3, then a STALE connect at t1 → stays down.
		edge("session", "s", "mcp.server", "zombie", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "", t0),
		healthFinding("mcp.server", "zombie", "down", sdkmodel.SeverityMedium, t3),
		edge("session", "s", "mcp.server", "zombie", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "", t1),
		// phoenix: connect, DOWN at t1, then a NEWER connect at t3 → recovers.
		edge("session", "s", "mcp.server", "phoenix", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "", t0),
		healthFinding("mcp.server", "phoenix", "down", sdkmodel.SeverityMedium, t1),
		edge("session", "s", "mcp.server", "phoenix", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "", t3),
	}
	stop := h.runEdges(t, tenant, obs, 2) // session→zombie, session→phoenix
	defer stop()
	h.waitServers(tenant, 2)

	r := h.do("GET", "/v1/m/capabilities/servers", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("servers = %d %s", r.code, r.raw)
	}
	conn := map[string]any{}
	for _, it := range items(r) {
		m := it.(map[string]any)
		conn[m["name"].(string)] = m["connection"]
	}
	if conn["zombie"] != "down" {
		t.Errorf("zombie connection = %v, want down (a stale 'connected' must not resurrect it)", conn["zombie"])
	}
	if conn["phoenix"] != "connected" {
		t.Errorf("phoenix connection = %v, want connected (a newer 'connected' recovers it)", conn["phoenix"])
	}
}

// TestWiringDistinctAndToolCollision proves (1) resource-access edges
// (file/http.url) produce NO capability wiring — that graph is , not this
// module's; and (2) two same-named tools on different servers stay distinct nodes
// in the wiring graph (no collapse on the bare name).
func TestWiringDistinctAndToolCollision(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)

	at := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	obs := []sdkmodel.Observation{
		// Two same-named tools "deploy" on different servers via one session.
		edge("session", "s", "mcp.tool", "srvA/deploy", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "deploy", at),
		edge("session", "s", "mcp.tool", "srvB/deploy", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "deploy", at),
		// Resource-access edges — must NOT become capability wiring.
		edge("session", "s", "http.url", "https://api.example.com/x", sdkmodel.ModeRead, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "WebFetch", at),
		edge("session", "s", "file", "/etc/passwd", sdkmodel.ModeRead, sdkmodel.SignalOTEL, sdkmodel.ConfidenceAttributed, "Read", at),
	}
	// Each mcp.tool edge yields 2 wiring rows (session→tool, session→server): 4 total.
	stop := h.runEdges(t, tenant, obs, 4)
	defer stop()

	r := h.do("GET", "/v1/m/capabilities/wiring", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("wiring = %d %s", r.code, r.raw)
	}
	toolRefs := map[string]bool{}
	for _, e := range r.body["edges"].([]any) {
		em := e.(map[string]any)
		ref := em["capability_ref"].(string)
		if em["capability_kind"] == "tool" {
			toolRefs[ref] = true
		}
		// No resource-access edge may have leaked into the capability graph.
		if ref == "https://api.example.com/x" || ref == "/etc/passwd" {
			t.Errorf("resource-access edge leaked into capability wiring: %v", em)
		}
	}
	if !toolRefs["srvA/deploy"] || !toolRefs["srvB/deploy"] {
		t.Errorf("same-named tools collapsed: tool refs = %v, want both srvA/deploy and srvB/deploy", toolRefs)
	}
}

// TestServersRequirePermission proves the catalog reads are RBAC-gated and tenant
// resolution is enforced.
func TestServersRequirePermission(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// No token ⇒ 401.
	if r := h.do("GET", "/v1/m/capabilities/servers", "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("unauth servers = %d, want 401", r.code)
	}
	// A viewer of the tenant ⇒ 200 (the read permission is granted by verb tier).
	viewer := h.roleToken(admin, tenant, "v@acme.com", auth.RoleViewer)
	if r := h.do("GET", "/v1/m/capabilities/servers", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer servers = %d %s", r.code, r.raw)
	}
}
