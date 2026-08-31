// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package evals

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// The ENGINE half of the A/B console contract. The body under test is not written
// here — it is read from testdata/ab_request_console.json, the SAME file the
// console's own test asserts `evalsApi.ab()` produces
// (web/src/features/evals/ab-contract.test.ts). Bytes copied by hand into a Go
// string would only ever prove the engine did not move; a shared fixture is the
// only version of this test that can also fail when the CONSOLE drifts.
//
// Everything below was MEASURED against this handler, not read off the structs.
const abConsoleFixture = "testdata/ab_request_console.json"

// The suite the fixture's outputs are written against: two cases scored `exact`.
// Variant A answers both correctly (score 1.0); variant B misses the greeting
// (0.5). A deterministic, non-tied result — a tie would not distinguish a real
// comparison from the empty one below.
func seedABSuite(t *testing.T, h *harness, admin string, tenant model.TenantID) string {
	t.Helper()
	suite := h.createSuite(admin, tenant, map[string]any{
		"name": "ab-contract", "subject_kind": "agent", "scorer": "exact", "criterion": "matches expected",
	})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "greeting", "input": "greet", "expected": "hello"})
	h.addCase(admin, tenant, suite, map[string]any{"case_key": "farewell", "input": "part", "expected": "bye"})
	return suite
}

func TestABAcceptsTheBodyTheConsoleSends(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suite := seedABSuite(t, h, admin, tenant)

	raw, err := os.ReadFile(abConsoleFixture)
	if err != nil {
		t.Fatalf("read %s: %v", abConsoleFixture, err)
	}
	body := json.RawMessage(strings.Replace(string(raw), "SUITE_REF_PLACEHOLDER", suite, 1))

	r := h.do("POST", "/v1/m/evals/ab", admin, body, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("console A/B body = %d %s, want 200", r.code, r.raw)
	}

	// A real comparison, not a draw between two unscored variants.
	variants, _ := r.body["variants"].([]any)
	if len(variants) != 2 {
		t.Fatalf("variants = %v, want 2", r.body["variants"])
	}
	top, _ := variants[0].(map[string]any)
	if got := top["label"]; got != "v6-concise" {
		t.Errorf("winner label = %v, want v6-concise (it answered both cases)", got)
	}
	if got := floatOf(top["score"]); got != 1 {
		t.Errorf("winning score = %v, want 1", got)
	}
	if r.body["winner"] != "v6-concise" {
		t.Errorf("winner = %v, want v6-concise", r.body["winner"])
	}
	if tie, _ := r.body["tie"].(bool); tie {
		t.Error("tie = true; the two variants scored 1.0 and 0.5")
	}
	// run_ref is what lets the console link to the runs the A/B persisted; the
	// console's AbVariant omitted it entirely.
	if ref, _ := top["run_ref"].(string); ref == "" {
		t.Error("run_ref is empty; the A/B persists a run per variant")
	}
	// The fixture opts into pairwise, so the block MUST be present. With no pair
	// judge wired it is an honest declared skip — never a fabricated winner.
	pw, ok := r.body["pairwise"].(map[string]any)
	if !ok {
		t.Fatalf("no pairwise block for a request with \"pairwise\": true — body %s", r.raw)
	}
	if pw["mode"] != "skipped" {
		t.Errorf("pairwise mode = %v, want skipped (no pair judge is wired here)", pw["mode"])
	}
	if reason, _ := pw["skip_reason"].(string); reason == "" {
		t.Error("a skipped pairwise block must say why")
	}
	if w, _ := pw["winner"].(string); w != "" {
		t.Errorf("skipped pairwise declared a winner %q", w)
	}
}

// The defect, kept red-by-construction so it cannot come back: the body the
// console sent BEFORE this change — two RunInputs under `a`/`b` — does not decode
// at all. `suite_ref` inside `a` is an unknown field and decodeJSON runs
// DisallowUnknownFields (helpers.go:85), so the request dies at the DECODER and
// answers "invalid JSON body". It never reaches the `suite_ref is required`
// message in ab.go:89, which is what a reader of the structs would predict.
func TestABRefusesTheOldConsoleBody(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	suite := seedABSuite(t, h, admin, tenant)

	old := json.RawMessage(`{"a":{"suite_ref":"` + suite + `","subject_kind":"agent","subject_ref":"v6","outputs":{}},` +
		`"b":{"suite_ref":"` + suite + `","subject_kind":"agent","subject_ref":"v5","outputs":{}}}`)
	r := h.do("POST", "/v1/m/evals/ab", admin, old, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Fatalf("old console body = %d %s, want 400", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "invalid JSON body") {
		t.Errorf("old console body answered %q; the decoder rejects it before any field validation", r.raw)
	}

	// And the reason the envelope fix alone is NOT the fix. A well-formed request
	// with EMPTY output sets is accepted, scores nothing, and returns a tie of
	// 0.0 vs 0.0 — while persisting two runs. Turning the 400 above into this
	// would replace a loud refusal with a fabricated draw in the tenant's
	// history, which is why the console refuses an empty output set client-side
	// (evals-view.tsx parseOutputs).
	empty := json.RawMessage(`{"suite_ref":"` + suite + `","a":{"label":"v6","outputs":{}},"b":{"label":"v5","outputs":{}}}`)
	e := h.do("POST", "/v1/m/evals/ab", admin, empty, tenantHdr(tenant))
	if e.code != http.StatusOK {
		t.Fatalf("empty-outputs A/B = %d %s, want 200 (this documents engine behavior)", e.code, e.raw)
	}
	if tie, _ := e.body["tie"].(bool); !tie {
		t.Errorf("empty-outputs A/B did not report a tie: %s", e.raw)
	}
	vs, _ := e.body["variants"].([]any)
	for _, v := range vs {
		m, _ := v.(map[string]any)
		if got := floatOf(m["score"]); got != 0 {
			t.Errorf("empty-outputs variant scored %v, want 0", got)
		}
	}
}
