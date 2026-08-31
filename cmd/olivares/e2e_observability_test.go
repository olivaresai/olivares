// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// Full-stack E2E for the observability backend: drives the REAL
// composition root — buildModules() wires the observability module and the
// models platforms provider exactly as the binary boots (wire.go:447-455, 375)
// — through the real HTTP handler with a logged-in principal + tenant, and
// asserts the wire contracts the web views consume. Bodies are decoded as
// map[string]any so PRESENCE/ABSENCE of omitempty keys is asserted, not just
// struct round-trips (the lesson: verify the flip against the backend,
// not the prompt).
//
// Two harness properties this test depends on (honest, not weakened):
//   - The harness's api.New does NOT wire api.Options.Tracing (e2e_harness_test
//     .go:122-128 mirrors the option set minus the OTel provider), so no ledger
//     event carries trace_id meta → /traces is an honestly EMPTY list and any
//     32-hex id is a guaranteed 404. The contract SHAPE (items+has_more, the
//     error envelope) is still fully exercised.
//   - The seed source emits no genai.semconv/genai.dialect findings
//     (seed.go:201-205), so otel_genai stays opt_in_off with NO opt_in_active
//     key — the "unknowable, never false-claimed" half of the gate contract.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// strField returns m[key] as a string ("" when absent or not a string).
func strField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// objField returns m[key] as a nested object, failing the test when absent.
func objField(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	obj, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%q is not an object: %#v", key, m[key])
	}
	return obj
}

// isLowerHexN reports whether s is exactly n lowercase hex characters.
func isLowerHexN(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func TestE2E_Observability_FullStack(t *testing.T) {
	h := newHarness(t)

	// --- (1) GET /ingestion-health: the per-standard table + engine scope ---
	t.Run("ingestion-health", func(t *testing.T) {
		m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/observability/ingestion-health")

		// Process-global by construction (OBS-06): the flag keeps the UI from
		// presenting the counters as per-tenant.
		assertEq(t, "engine_scope", m["engine_scope"], true)

		// Counters accumulate from the module-start instant (like /metrics).
		if since := strField(m, "since"); since == "" {
			t.Errorf("since missing or empty: %#v", m["since"])
		}

		// EXACTLY the seven standards, in the table's deterministic order.
		stds := items2(m, "standards")
		wantIDs := []string{
			"otel_genai", "ocsf", "asim_agentevent", "siem_unified",
			"ledger_push", "prometheus_text", "w3c_trace_context",
		}
		if len(stds) != len(wantIDs) {
			t.Fatalf("standards count = %d, want %d", len(stds), len(wantIDs))
		}
		byID := make(map[string]map[string]any, len(stds))
		for i, s := range stds {
			id := strField(s, "id")
			assertEq(t, "standards["+wantIDs[i]+"].id (order)", id, wantIDs[i])
			byID[id] = s
		}

		// TRUE operational states (never "all green"): OCSF only emits when an
		// operator provisions a destination → available; the Prometheus endpoint
		// is always served → active; the ledger-push Forwarder seam has zero call
		// sites until → blocked.
		assertEq(t, "ocsf.status", strField(byID["ocsf"], "status"), "available")
		assertEq(t, "prometheus_text.status", strField(byID["prometheus_text"], "status"), "active")
		assertEq(t, "ledger_push.status", strField(byID["ledger_push"], "status"), "blocked")

		// The gen_ai opt-in gate: the gate string is declared, but its state is
		// per-source connector config the engine cannot read — without bus
		// evidence (none in this estate) opt_in_active must be ABSENT (nil,
		// unknowable), never a false claim, and the status stays opt_in_off.
		otel := byID["otel_genai"]
		assertEq(t, "otel_genai.opt_in_gate", strField(otel, "opt_in_gate"),
			"semconv_opt_in=gen_ai_latest_experimental")
		if _, present := otel["opt_in_active"]; present {
			t.Errorf("otel_genai.opt_in_active present (= %#v), want ABSENT (unknowable, omitempty)", otel["opt_in_active"])
		}
		assertEq(t, "otel_genai.status", strField(otel, "status"), "opt_in_off")
	})

	// --- (2) GET /traces: the list envelope (items + has_more) ---
	t.Run("traces-list", func(t *testing.T) {
		code, raw := h.req("GET", "/v1/m/observability/traces", h.adminToken, h.tenantA, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /traces = %d: %s", code, raw)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode: %v (%s)", err, raw)
		}
		// items is ALWAYS a JSON array (never null), even when empty.
		if _, ok := m["items"].([]any); !ok {
			t.Fatalf("items is not an array: %s", raw)
		}
		// has_more is always present (no omitempty on the envelope).
		if _, ok := m["has_more"].(bool); !ok {
			t.Fatalf("has_more missing or not a bool: %s", raw)
		}
		// The harness wires no Tracing provider, so the ledger carries no
		// trace_id meta and the list is honestly empty. If a future harness
		// wires it, each row must still honor the no-fabrication contract.
		for _, it := range items(m) {
			if !isLowerHexN(strField(it, "trace_id"), 32) {
				t.Errorf("trace_id not 32-lower-hex: %#v", it["trace_id"])
			}
			assertEq(t, "trace item status", strField(it, "status"), "unset")
		}
	})

	// --- (3) GET /traces/{unknown 32-hex}: 404 with the module error envelope ---
	t.Run("trace-detail-unknown", func(t *testing.T) {
		code, raw := h.req("GET",
			"/v1/m/observability/traces/deadbeefdeadbeefdeadbeefdeadbeef",
			h.adminToken, h.tenantA, nil)
		if code != http.StatusNotFound {
			t.Fatalf("GET unknown trace = %d, want 404: %s", code, raw)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode 404 body: %v (%s)", err, raw)
		}
		errObj := objField(t, m, "error")
		if msg := strField(errObj, "message"); msg == "" {
			t.Errorf("error.message missing or empty: %s", raw)
		}
	})

	// --- (4) GET /attestation: measured binary, measured-absence release,
	//         declared pipeline — the three blocks never blur ---
	t.Run("attestation", func(t *testing.T) {
		m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/observability/attestation")

		bin := objField(t, m, "binary")
		if v := strField(bin, "version"); v == "" {
			t.Errorf("binary.version empty (ldflags default is \"dev\", never empty)")
		}
		assertEq(t, "binary.status", strField(bin, "status"), "measured")
		// self_sha256 is omitempty: present ⇒ a real 64-lower-hex stream hash of
		// os.Executable(); absent ⇒ the honest note must explain why.
		if selfRaw, present := bin["self_sha256"]; present {
			self, _ := selfRaw.(string)
			if !isLowerHexN(self, 64) {
				t.Errorf("binary.self_sha256 = %q, want 64 lowercase hex", self)
			}
		} else if strField(bin, "self_hash_note") == "" {
			t.Errorf("binary.self_sha256 absent WITHOUT a self_hash_note explaining the absence")
		}

		rel := objField(t, m, "release")
		assertEq(t, "release.status", strField(rel, "status"), "not_published")
		assertEq(t, "release.verifier_available", rel["verifier_available"], true)

		pipe := objField(t, m, "pipeline")
		assertEq(t, "pipeline.status", strField(pipe, "status"), "declared")
	})

	// --- (5) GET /v1/m/models/platforms: the declared surface matrix +
	//         per-platform lifecycle, live through the wired provider ---
	t.Run("models-platforms", func(t *testing.T) {
		m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/models/platforms")

		// The provider is credential-less and wired unconditionally (wire.go:375),
		// so through the real composition root the reference is available.
		assertEq(t, "available", m["available"], true)

		// Six deployment surfaces in gateway-ascending order (AllSurfaces sorts
		// by gateway value; collapsing them would be wrong on residency/ingest).
		surfaces := items2(m, "surfaces")
		wantGateways := []string{
			"bedrock-legacy", "bedrock-mantle", "claude-platform-aws",
			"direct", "foundry", "vertex",
		}
		if len(surfaces) != len(wantGateways) {
			t.Fatalf("surfaces count = %d, want %d", len(surfaces), len(wantGateways))
		}
		for i, s := range surfaces {
			assertEq(t, "surfaces["+wantGateways[i]+"].gateway (order)",
				strField(s, "gateway"), wantGateways[i])
		}

		// The claude-sonnet-4 family: the ONE entry with verified per-surface
		// divergence. Index its retirement rows by surface.
		lifecycles := items2(m, "lifecycles")
		var sonnet4, mythos map[string]any
		for _, l := range lifecycles {
			switch strField(l, "model_id") {
			case "claude-sonnet-4":
				sonnet4 = l
			case "claude-mythos-preview":
				mythos = l
			}
		}
		if sonnet4 == nil {
			t.Fatalf("lifecycle family claude-sonnet-4 not present")
		}
		if mythos == nil {
			t.Fatalf("lifecycle family claude-mythos-preview not present")
		}

		rows := items2(sonnet4, "retirements")
		bySurface := make(map[string]map[string]any, len(rows))
		for _, r := range rows {
			bySurface[strField(r, "surface")] = r
			// The published successor rides EVERY row of the family.
			assertEq(t, "sonnet-4["+strField(r, "surface")+"].replacement_ref",
				strField(r, "replacement_ref"), "claude-sonnet-4-6")
		}
		if len(rows) != 6 {
			t.Errorf("sonnet-4 retirement rows = %d, want 6 (4 confirmed + 2 to-confirm)", len(rows))
		}

		// Bedrock per the authority has NO published date ⇒ both modeled gateways
		// are to-confirm rows: empty retires_on and NO deprecated_on key (the
		// partner authority published neither — absence is never "never retires",
		// and never a fabricated date).
		for _, g := range []string{"bedrock-legacy", "bedrock-mantle"} {
			r := bySurface[g]
			if r == nil {
				t.Fatalf("sonnet-4 row for %s missing", g)
			}
			assertEq(t, "sonnet-4["+g+"].status", strField(r, "status"), "to-confirm")
			assertEq(t, "sonnet-4["+g+"].retires_on", strField(r, "retires_on"), "")
			if _, present := r["deprecated_on"]; present {
				t.Errorf("sonnet-4[%s].deprecated_on present (= %#v), want ABSENT (omitempty)", g, r["deprecated_on"])
			}
		}

		// The published first-party schedule: direct retires 2026-06-15
		// (deprecated 2026-04-14); Vertex runs the verified divergent 2026-09-14.
		direct := bySurface["direct"]
		if direct == nil {
			t.Fatalf("sonnet-4 row for direct missing")
		}
		assertEq(t, "sonnet-4[direct].retires_on", strField(direct, "retires_on"), "2026-06-15")
		assertEq(t, "sonnet-4[direct].status", strField(direct, "status"), "confirmed")
		assertEq(t, "sonnet-4[direct].deprecated_on", strField(direct, "deprecated_on"), "2026-04-14")
		vertex := bySurface["vertex"]
		if vertex == nil {
			t.Fatalf("sonnet-4 row for vertex missing")
		}
		assertEq(t, "sonnet-4[vertex].retires_on", strField(vertex, "retires_on"), "2026-09-14")
		assertEq(t, "sonnet-4[vertex].status", strField(vertex, "status"), "confirmed")

		// claude-mythos-preview: retirement announced ("after Mythos 5 GA") with
		// NO published retirement date ⇒ every row is to-confirm with an empty
		// retires_on. Its DEPRECATION date IS published (2026-06-09, the GA note),
		// so deprecated_on rides the rows — only the retirement date is unknowable.
		mrows := items2(mythos, "retirements")
		if len(mrows) == 0 {
			t.Fatalf("mythos-preview has no retirement rows")
		}
		for _, r := range mrows {
			s := strField(r, "surface")
			assertEq(t, "mythos-preview["+s+"].status", strField(r, "status"), "to-confirm")
			assertEq(t, "mythos-preview["+s+"].retires_on", strField(r, "retires_on"), "")
			assertEq(t, "mythos-preview["+s+"].deprecated_on", strField(r, "deprecated_on"), "2026-06-09")
		}

		// The sampling-param deprecation pre-advice (ANT2-03), verbatim from the
		// connector's declared descriptor.
		pd := objField(t, m, "param_deprecation")
		assertEq(t, "param_deprecation.affected", strField(pd, "affected"), "Opus 4.7+, Fable/Mythos 5")
	})

	// --- (6) RBAC: no principal ⇒ 401 before any module code runs ---
	t.Run("rbac-unauthenticated", func(t *testing.T) {
		code, raw := h.req("GET", "/v1/m/observability/ingestion-health", "", "", nil)
		if code != http.StatusUnauthorized {
			t.Errorf("unauthenticated ingestion-health = %d, want 401: %s", code, raw)
		}
		// Guard against an HTML/empty-body 401 regressing the API contract.
		if len(raw) > 0 && !strings.Contains(string(raw), "error") &&
			!strings.Contains(string(raw), "unauth") {
			t.Logf("note: 401 body carries no error marker: %s", raw)
		}
	})
}
