// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// TestDeniedOpenIsReachableOnlyFromTheTenantLedger pins the asymmetry that decides
// where a denial can be READ from.
//
// A governed open creates the session row (markGovernedOpen, policies.go — "created
// here if telemetry has not yet arrived"). Every denial returns BEFORE that call, so a
// denied open writes a decision row and no session row. Consequence, and this is the
// whole point of the test: the per-session ledger route is only reachable through a
// session that exists, so a denial is readable ONLY from the tenant-wide
// GET /v1/m/voice/decisions.
//
// Control POSITIVO in the same run: an approved open of another agent DOES produce a
// session row through the same probe. Without it, the 404 below would equally prove a
// broken probe — which is the failure mode this file exists to rule out.
//
// ⚠ ALCANCE, porque el comentario anterior generalizaba y el test no: aquí se ejercita
// UNA denegación, la de «ninguna política lo permite». Las otras tres ya tienen testigo
// propio y las tres comprueban su fila en el mismo ledger del tenant: kill switch en
// `killswitch_test.go` (las dos fases, `op_status=blocked`) y presupuesto en
// `budget_test.go` (`ledgerOpStatus`, que lee `GET /v1/m/voice/decisions`). El
// plan-hash que no casa lo cubre `TestOpenPlanHashMismatch` en `voice_test.go`.
//
// ⚠ Y UN LÍMITE MÁS, medido por el contraste y que esta prueba NO cubre: la fila de
// sesión puede existir por OTRA vía —la telemetría llama a `upsertSession`
// (`sessions.go`), que la crea si no está—, así que lo que se demuestra es que la
// denegación no la crea, no que no pueda haberla.
func TestDeniedOpenIsReachableOnlyFromTheTenantLedger(t *testing.T) {
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(fakeDispatcher{ref: "rt-9"}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")

	// ── Denied: no policy permits this tuple, so the open is refused default-deny.
	if r := h.open(tok, tenant, "s-denied", "ghost-agent", "gpt-realtime", "openai", ""); r.code != http.StatusForbidden {
		t.Fatalf("default-deny open = %d %s, want 403", r.code, r.raw)
	}

	// ── Control POSITIVO: an allowed, approved open reaches dispatch and IS listed.
	h.setPolicy(tok, tenant, "ok-agent", "gpt-realtime", "openai", 0)
	if r := h.open(tok, tenant, "s-allowed", "ok-agent", "gpt-realtime", "openai", "appr-1"); r.code != http.StatusOK {
		t.Fatalf("approved open = %d %s, want 200", r.code, r.raw)
	}
	if _, code := h.getSession(tok, tenant, "s-allowed"); code != http.StatusOK {
		t.Fatalf("the session probe cannot see a session that DOES exist (code %d) — it proves nothing below", code)
	}

	// ── The denied ref has no session row: nothing in the console can link to it.
	if _, code := h.getSession(tok, tenant, "s-denied"); code != http.StatusNotFound {
		t.Fatalf("denied open left a session row (code %d): the premise of the tenant ledger view is wrong", code)
	}

	// ── And yet its decision IS in the tenant-wide ledger, with the denial recorded.
	dr := h.do("GET", "/v1/m/voice/decisions", tok, nil, tenantHdr(tenant))
	if dr.code != http.StatusOK {
		t.Fatalf("tenant ledger = %d %s, want 200", dr.code, dr.raw)
	}
	var ledger listResponse[decisionDTO]
	if err := json.Unmarshal([]byte(dr.raw), &ledger); err != nil {
		t.Fatalf("tenant ledger body: %v — %s", err, dr.raw)
	}
	var denied *decisionDTO
	for i := range ledger.Items {
		if ledger.Items[i].SessionRef == "s-denied" {
			denied = &ledger.Items[i]
			break
		}
	}
	if denied == nil {
		t.Fatalf("the denial is in NO readable surface at all: %s", dr.raw)
	}
	if denied.OpStatus != opStatusBlocked || denied.PolicyVerdict != verdictDenied {
		t.Fatalf("denial recorded as %s/%s, want %s/%s", denied.Op, denied.OpStatus, opStatusBlocked, verdictDenied)
	}

	// ── And the per-session route, the only one the console can reach today, is empty
	//    for that ref only because the session it hangs off does not exist.
	sr := h.do("GET", "/v1/m/voice/sessions/s-denied/decisions", tok, nil, tenantHdr(tenant))
	var scoped listResponse[decisionDTO]
	_ = json.Unmarshal([]byte(sr.raw), &scoped)
	if sr.code == http.StatusOK && len(scoped.Items) == 0 {
		t.Fatalf("the scoped route answered 200 with zero rows for a ref that HAS a decision: %s", sr.raw)
	}
}

// ledgerClock is a settable model.Clock: the ledger stamps occurred_at from the module
// clock, so ordering can only be tested by controlling it. Real wall time would leave
// three rows inside the same millisecond and the assertion would be about luck.
type ledgerClock struct{ at model.Timestamp }

func (c *ledgerClock) Now() model.Timestamp { return c.at }

// TestTenantLedgerReturnsNewestFirstAndTruncatesTheOldest pins the property that decides
// WHICH rows survive the page limit.
//
// The store has no default ordering by time: with no Sort it orders by id, and ids are
// UUIDv7 — time-ordered ASCENDING. So the unsorted ledger answered with the OLDEST rows
// first, and a truncated page was the oldest page: an operator opening the ledger of a
// tenant with history would see a full table and not one recent denial. Sorting in the
// browser cannot fix that; the truncation already happened on the wrong slice.
func TestTenantLedgerReturnsNewestFirstAndTruncatesTheOldest(t *testing.T) {
	base := model.NewTimestamp(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC))
	clk := &ledgerClock{at: base}
	h, _ := newHarness(t, WithClock(clk))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")

	// Three denials, oldest to newest. Default-deny: no policy permits any of them.
	refs := []string{"ref-viejo", "ref-medio", "ref-nuevo"}
	for i, ref := range refs {
		clk.at = model.NewTimestamp(base.Time().Add(time.Duration(i+1) * time.Minute))
		if r := h.open(tok, tenant, ref, "ghost-agent", "gpt-realtime", "openai", ""); r.code != http.StatusForbidden {
			t.Fatalf("open %s = %d %s, want 403", ref, r.code, r.raw)
		}
	}

	// CONTROL POSITIVO: the whole page comes back newest first.
	todo := ledgerRefs(t, h, tok, tenant, "")
	want := []string{"ref-nuevo", "ref-medio", "ref-viejo"}
	if len(todo) != 3 || todo[0] != want[0] || todo[1] != want[1] || todo[2] != want[2] {
		t.Fatalf("ledger order = %v, want %v", todo, want)
	}

	// Y EL CASO QUE DECIDE: recortado a dos, sobreviven las DOS MÁS RECIENTES.
	dos := ledgerRefs(t, h, tok, tenant, "?limit=2")
	if len(dos) != 2 || dos[0] != "ref-nuevo" || dos[1] != "ref-medio" {
		t.Fatalf("truncated page = %v, want the two newest [ref-nuevo ref-medio]", dos)
	}
}

func ledgerRefs(t *testing.T, h *harness, tok string, tenant model.TenantID, query string) []string {
	t.Helper()
	r := h.do("GET", "/v1/m/voice/decisions"+query, tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ledger%s = %d %s", query, r.code, r.raw)
	}
	var body listResponse[decisionDTO]
	if err := json.Unmarshal([]byte(r.raw), &body); err != nil {
		t.Fatalf("ledger body: %v — %s", err, r.raw)
	}
	out := make([]string, 0, len(body.Items))
	for _, d := range body.Items {
		out = append(out, d.SessionRef)
	}
	return out
}
