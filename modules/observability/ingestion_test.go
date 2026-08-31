// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"net/http"
	"testing"
	"time"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

const ingestionPath = "/v1/m/observability/ingestion-health"

// TestOTelGenAIVersionPin pins this module's standards-table constants.
// otelGenAIVersion is an unexported MIRROR of connectors/claude
// genAISemconvVersion and modules/recording semconvVersion — all three must equal
// "1.41.1", the last VERSIONED GenAI vocabulary label. The upstream pair mirrors
// the semconv-genai repo/ref re-verified 2026-07-05. A drift in any mirror is caught
// by the pin test in its own package.
func TestOTelGenAIVersionPin(t *testing.T) {
	const want = "1.41.1"
	if otelGenAIVersion != want {
		t.Fatalf("otelGenAIVersion = %q, want %q (mirror of genAISemconvVersion / recording semconvVersion)", otelGenAIVersion, want)
	}
	if otelGenAIUpstreamRepo != "open-telemetry/semantic-conventions-genai" {
		t.Fatalf("otelGenAIUpstreamRepo = %q", otelGenAIUpstreamRepo)
	}
	if otelGenAIUpstreamRef != "main@c321d7e, verified 2026-07-05" {
		t.Fatalf("otelGenAIUpstreamRef = %q", otelGenAIUpstreamRef)
	}
	if otelGenAIGate != "semconv_opt_in=gen_ai_latest_experimental" {
		t.Fatalf("otelGenAIGate = %q, want the opt-in gate mirror", otelGenAIGate)
	}
}

// TestIngestionHealthStandards asserts the full static table: all seven
// standards with their exact verified pins and TRUE statuses, engine_scope,
// the empty sources slice (never null) and the since anchor.
func TestIngestionHealthStandards(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("GET", ingestionPath, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ingestion-health = %d %s", r.code, r.raw)
	}
	if !boolOf(r.body["engine_scope"]) {
		t.Fatal("engine_scope must be true (counters are process-global)")
	}
	if since := strOf(r.body["since"]); since == "" {
		t.Fatal("since must carry the module-start instant")
	}
	if items, ok := r.body["sources"].([]any); !ok || len(items) != 0 {
		t.Fatalf("sources = %v, want empty array", r.body["sources"])
	}

	rows := listOf(r.body["standards"])
	if len(rows) != 7 {
		t.Fatalf("standards = %d rows, want 7", len(rows))
	}
	type want struct {
		id, label, direction, maturity, version, status string
	}
	wants := []want{
		{"otel_genai", "OpenTelemetry GenAI semconv", "in", "development", "1.41.1", "opt_in_off"},
		{"ocsf", "OCSF (ai_operation profile)", "out", "ga", "1.8.0", "available"},
		{"asim_agentevent", "Microsoft Sentinel ASIM AgentEvent", "out", "pre_1_0", "0.1.0", "available"},
		{"siem_unified", "SIEM unified (CEF / LEEF / syslog / OTLP)", "out", "stable", "—", "available"},
		{"ledger_push", "Ledger push transport", "out", "development", "—", "blocked"},
		{"prometheus_text", "Prometheus text exposition", "out", "stable", "0.0.4", "active"},
		{"w3c_trace_context", "W3C Trace Context (ledger correlation)", "in", "stable", "—", "active"},
	}
	for i, wantRow := range wants {
		got := rows[i]
		if strOf(got["id"]) != wantRow.id || strOf(got["label"]) != wantRow.label ||
			strOf(got["direction"]) != wantRow.direction || strOf(got["maturity"]) != wantRow.maturity ||
			strOf(got["version"]) != wantRow.version || strOf(got["status"]) != wantRow.status {
			t.Fatalf("standards[%d] = %v, want %+v", i, got, wantRow)
		}
	}

	// The gated profile: gate string present, but no activity claim of any kind
	// until bus evidence exists — no opt_in_active false-claim, no counters.
	genai := rows[0]
	if strOf(genai["opt_in_gate"]) != "semconv_opt_in=gen_ai_latest_experimental" {
		t.Fatalf("otel_genai opt_in_gate = %q", strOf(genai["opt_in_gate"]))
	}
	if strOf(genai["upstream_repo"]) != "open-telemetry/semantic-conventions-genai" {
		t.Fatalf("otel_genai upstream_repo = %q", strOf(genai["upstream_repo"]))
	}
	if strOf(genai["upstream_ref"]) != "main@c321d7e, verified 2026-07-05" {
		t.Fatalf("otel_genai upstream_ref = %q", strOf(genai["upstream_ref"]))
	}
	for _, k := range []string{"opt_in_active", "records_total", "last_seen"} {
		if _, present := genai[k]; present {
			t.Fatalf("otel_genai must omit %s before bus evidence", k)
		}
	}
	for i, row := range rows[1:] {
		if _, present := row["upstream_repo"]; present {
			t.Fatalf("standards[%d] must omit upstream_repo", i+1)
		}
		if _, present := row["upstream_ref"]; present {
			t.Fatalf("standards[%d] must omit upstream_ref", i+1)
		}
	}
	// No standard fabricates a records_total: none is soundly attributable.
	for i, row := range rows {
		if _, present := row["records_total"]; present {
			t.Fatalf("standards[%d] carries records_total; no count is attributable", i)
		}
	}
}

// TestIngestionHealthRBAC: the route is read-tier (viewer can read) and
// unauthenticated calls are rejected.
func TestIngestionHealthRBAC(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	if r := h.do("GET", ingestionPath, "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d, want 401", r.code)
	}
	if r := h.do("GET", ingestionPath, viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer = %d, want 200 %s", r.code, r.raw)
	}
}

// TestIngestionSourcesCounters publishes bus observations from two sources and
// asserts the per-source counters: totals, kind breakdown, the edges-only
// signal breakdown, first/last seen and name ordering.
func TestIngestionSourcesCounters(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	t0 := h.clk.nowTime()
	h.publish(tenant, "claude-prod", sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "a1", ResourceKind: "http.api", ResourceRef: "api.x",
		Mode: sdkmodel.ModeRead, Source: sdkmodel.SignalOTEL, ObservedAt: t0,
	})
	h.publish(tenant, "claude-prod", sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "a1", ResourceKind: "postgres.table", ResourceRef: "public.t",
		Mode: sdkmodel.ModeWrite, Source: sdkmodel.SignalPGAudit, ObservedAt: t0.Add(5 * time.Second),
	})
	h.publish(tenant, "claude-prod", sdkmodel.CostSample{
		ProviderRef: "anthropic", ModelRef: "claude-sonnet-4-6", CostMicroUSD: 42,
		OccurredAt: t0.Add(10 * time.Second),
	})
	h.publish(tenant, "audit-src", sdkmodel.FindingReport{
		Kind: "guardrail", Severity: sdkmodel.SeverityInfo, SubjectKind: "agent", SubjectRef: "a1",
		Title: "t", OccurredAt: t0.Add(2 * time.Second),
	})

	var sources []map[string]any
	h.waitUntil("both sources counted", func() bool {
		r := h.do("GET", ingestionPath, admin, nil, tenantHdr(tenant))
		sources = listOf(r.body["sources"])
		return len(sources) == 2 && intOf(sources[1]["records_total"]) == 3
	})

	// Sorted by name: audit-src < claude-prod.
	if strOf(sources[0]["name"]) != "audit-src" || strOf(sources[1]["name"]) != "claude-prod" {
		t.Fatalf("source order = %q,%q", strOf(sources[0]["name"]), strOf(sources[1]["name"]))
	}
	claude := sources[1]
	kinds := mapOf(claude["kinds"])
	if intOf(kinds["edge"]) != 2 || intOf(kinds["cost"]) != 1 {
		t.Fatalf("claude-prod kinds = %v", kinds)
	}
	signals := mapOf(claude["signals"])
	if intOf(signals["otel"]) != 1 || intOf(signals["pg_audit"]) != 1 {
		t.Fatalf("claude-prod signals = %v", signals)
	}
	if strOf(claude["first_seen"]) == "" || strOf(claude["last_seen"]) == "" {
		t.Fatalf("claude-prod seen stamps missing: %v", claude)
	}
	if strOf(claude["first_seen"]) >= strOf(claude["last_seen"]) {
		t.Fatalf("first_seen %q must precede last_seen %q", strOf(claude["first_seen"]), strOf(claude["last_seen"]))
	}
	// Findings carry no edge signal: the finding-only source has no signals map.
	if _, present := sources[0]["signals"]; present {
		t.Fatalf("audit-src must omit signals (no edges): %v", sources[0])
	}
	if kinds := mapOf(sources[0]["kinds"]); intOf(kinds["finding"]) != 1 {
		t.Fatalf("audit-src kinds = %v", kinds)
	}
}

// TestIngestionGenAIEvidence: the two gen_ai finding kinds prove DIFFERENT
// facts. The semconv posture finding fires at Gather start gated only on the
// profile (connectors/claude/claude.go:166-171) — it flips the row to active
// with opt_in_active=true but must NOT feed last_seen ("most recent record"):
// no record has flowed yet. A dialect finding is dropped per ingested run, so
// only it sets last_seen. records_total stays omitted throughout (gen_ai
// records are not distinguishable from claude_code on the bus).
func TestIngestionGenAIEvidence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	at := h.clk.nowTime().Add(3 * time.Second)
	h.publish(tenant, "claude-prod", sdkmodel.FindingReport{
		Kind: "posture", Severity: sdkmodel.SeverityInfo,
		SubjectKind: "genai.semconv", SubjectRef: "1.41.1",
		Title: "gen_ai multi-dialect ingest active", OccurredAt: at,
	})

	var genai map[string]any
	h.waitUntil("otel_genai flips active", func() bool {
		r := h.do("GET", ingestionPath, admin, nil, tenantHdr(tenant))
		rows := listOf(r.body["standards"])
		if len(rows) != 7 {
			return false
		}
		genai = rows[0]
		return strOf(genai["status"]) == "active"
	})
	if v, present := genai["opt_in_active"]; !present || !boolOf(v) {
		t.Fatalf("opt_in_active = %v, want true on gate evidence", genai["opt_in_active"])
	}
	if v, present := genai["last_seen"]; present {
		t.Fatalf("last_seen = %v, want omitted: the posture finding proves the gate, not a record", v)
	}
	if _, present := genai["records_total"]; present {
		t.Fatal("records_total must stay omitted: gen_ai records are not attributable on the bus")
	}

	// A dialect-drift finding proves a RECORD flowed — last_seen appears.
	recAt := at.Add(2 * time.Second)
	h.publish(tenant, "claude-prod", sdkmodel.FindingReport{
		Kind: "posture", Severity: sdkmodel.SeverityInfo,
		SubjectKind: "genai.dialect", SubjectRef: "openllmetry",
		Title: "gen_ai dialect drift", OccurredAt: recAt,
	})
	h.waitUntil("otel_genai last_seen carries the record evidence", func() bool {
		r := h.do("GET", ingestionPath, admin, nil, tenantHdr(tenant))
		rows := listOf(r.body["standards"])
		if len(rows) != 7 {
			return false
		}
		genai = rows[0]
		return strOf(genai["last_seen"]) != ""
	})
}
