// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// TestWorkflowRunDecisionsLiveOnlyInTheTenantLedger pins the reachability fact the
// console's decision surface depends on.
//
// `schedule_ref` is NULLABLE (schema.go) and written with setIf, so a decision that
// belongs to no schedule is a normal row, not an anomaly. Every workflow-run decision
// is one of those: they carry subject_kind="workflow" and never set scheduleRef
// (workflow_run.go). And the console reaches decisions ONLY through
// GET /schedules/{id}/decisions, one schedule at a time.
//
// ⇒ no workflow-run decision is reachable through the per-schedule route, so the
// tenant-wide GET /decisions is the only surface that returns the LEDGER ROW itself.
//
// ⚠ ALCANCE, y la primera versión de este comentario lo afirmaba de más: eso NO significa
// que el hecho fuera invisible. Una denegación por kill switch escribe además
// `orchestration.workflow.run.killswitch_denied` en auditoría, un bloqueo escribe
// `orchestration.workflow.run.blocked`, el caso `no_gate` levanta un finding de seguridad
// y el detalle del run marca el step como `blocked`. Lo que no había en ninguna pantalla
// es la FILA del ledger —con su `op_status`, su `plan_hash` y su `gate_status`—, y la ruta
// también la consumían ya el CLI y los SDK generados. Este test prueba la alcanzabilidad
// por ruta, no la ausencia en toda la consola: para eso haría falta mirar cada pantalla.
//
// Control POSITIVO in the same run: a schedule-scoped decision IS returned by the
// per-schedule route, so the "not reachable" assertion below is about the data and not
// about a probe that cannot see anything.
func TestWorkflowRunDecisionsLiveOnlyInTheTenantLedger(t *testing.T) {
	g := newRoutedGate()
	h, _ := newHarness(t, WithApprovalGate(g))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// ── A workflow run, stopped at phase 1: it records a decision with NO schedule.
	wf := h.createWorkflow(admin, tenant, "nightly-report", []map[string]any{emitStep("a")})
	wfID := wf["id"].(string)
	if r := h.do("POST", "/v1/m/orchestration/workflows/"+wfID+"/run", admin, nil, tenantHdr(tenant)); r.code != http.StatusAccepted {
		t.Fatalf("run phase 1 = %d %s, want 202", r.code, r.raw)
	}

	// ── A schedule with its own decision, as the positive control.
	schedID := h.createAgentSchedule(admin, tenant, "sweep", "cleanup-bot")
	if r := h.do("POST", "/v1/m/orchestration/schedules/"+schedID+"/fire", admin, nil, tenantHdr(tenant)); r.code != http.StatusAccepted && r.code != http.StatusOK {
		t.Fatalf("schedule fire = %d %s", r.code, r.raw)
	}

	// ── The tenant ledger carries BOTH, and the workflow one has no schedule_ref.
	todo := tenantLedger(t, h, admin, tenant)
	var run *decisionDTO
	var sched *decisionDTO
	for i := range todo {
		switch {
		case todo[i].SubjectKind == "workflow" && todo[i].SubjectRef == wfID:
			run = &todo[i]
		case todo[i].ScheduleRef == schedID:
			sched = &todo[i]
		}
	}
	if run == nil {
		t.Fatalf("the tenant ledger does not carry the workflow-run decision: %+v", todo)
	}
	if run.ScheduleRef != "" {
		t.Fatalf("premise wrong: the workflow-run decision carries schedule_ref=%q", run.ScheduleRef)
	}
	if sched == nil {
		t.Fatalf("control: the tenant ledger must carry the schedule decision too: %+v", todo)
	}

	// ── CONTROL POSITIVO: the per-schedule route DOES return its own decision.
	desde := scheduleLedger(t, h, admin, tenant, schedID)
	if len(desde) == 0 {
		t.Fatal("the per-schedule route returned nothing for a schedule that HAS a decision — the probe proves nothing below")
	}

	// ── Y EL HECHO: ninguna ruta por schedule puede devolver la del workflow.
	for _, id := range []string{schedID, wfID} {
		for _, d := range scheduleLedger(t, h, admin, tenant, id) {
			if d.ID == run.ID {
				t.Fatalf("the workflow-run decision IS reachable through /schedules/%s/decisions", id)
			}
		}
	}
}

func tenantLedger(t *testing.T, h *harness, tok string, tenant model.TenantID) []decisionDTO {
	t.Helper()
	r := h.do("GET", "/v1/m/orchestration/decisions", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("tenant ledger = %d %s", r.code, r.raw)
	}
	var body listResponse[decisionDTO]
	if err := json.Unmarshal([]byte(r.raw), &body); err != nil {
		t.Fatalf("tenant ledger body: %v — %s", err, r.raw)
	}
	return body.Items
}

func scheduleLedger(t *testing.T, h *harness, tok string, tenant model.TenantID, id string) []decisionDTO {
	t.Helper()
	r := h.do("GET", "/v1/m/orchestration/schedules/"+id+"/decisions", tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		return nil
	}
	var body listResponse[decisionDTO]
	_ = json.Unmarshal([]byte(r.raw), &body)
	return body.Items
}

// ledgerClock is a settable model.Clock: occurred_at comes from the module clock, so the
// order can only be tested by controlling it. Real wall time would put three rows inside
// the same millisecond and the assertion would be about luck.
type ledgerClock struct{ at model.Timestamp }

func (c *ledgerClock) Now() model.Timestamp { return c.at }

// TestTenantLedgerIsNewestFirstAndTruncatesTheOldest pins WHICH rows survive the limit
// IN THE OPT-IN MODE.
// Same property as the voice ledger and for the same reason: with no Sort the store
// orders by the UUIDv7 id, which is ascending in time, so a truncated page was the
// OLDEST page — in a governance ledger, exactly the rows nobody is looking for.
func TestTenantLedgerIsNewestFirstAndTruncatesTheOldest(t *testing.T) {
	base := model.NewTimestamp(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC))
	clk := &ledgerClock{at: base}
	g := newRoutedGate()
	h, _ := newHarness(t, WithApprovalGate(g), WithClock(clk))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	nombres := []string{"viejo", "medio", "nuevo"}
	for i, n := range nombres {
		clk.at = model.NewTimestamp(base.Time().Add(time.Duration(i+1) * time.Minute))
		wf := h.createWorkflow(admin, tenant, "wf-"+n, []map[string]any{emitStep("a")})
		if r := h.do("POST", "/v1/m/orchestration/workflows/"+wf["id"].(string)+"/run", admin, nil, tenantHdr(tenant)); r.code != http.StatusAccepted {
			t.Fatalf("run %s = %d %s", n, r.code, r.raw)
		}
	}

	// CONTROL POSITIVO: la página entera llega de más reciente a más antigua.
	todo := ledgerSubjects(t, h, admin, tenant, "?order=newest")
	if len(todo) != 3 {
		t.Fatalf("ledger = %d filas, quiero 3: %v", len(todo), todo)
	}
	// Y EL CASO QUE DECIDE: recortado a dos, sobreviven las DOS MÁS RECIENTES.
	dos := ledgerOccurred(t, h, admin, tenant, "?order=newest&limit=2")
	if len(dos) != 2 {
		t.Fatalf("página recortada = %d filas, quiero 2", len(dos))
	}
	if !(dos[0] > dos[1]) {
		t.Fatalf("la página recortada no viene de más reciente a más antigua: %v", dos)
	}
	todas := ledgerOccurred(t, h, admin, tenant, "?order=newest")
	if dos[0] != todas[0] || dos[1] != todas[1] {
		t.Fatalf("la página recortada no es la CABEZA de la lista completa: %v vs %v", dos, todas)
	}
}

func ledgerSubjects(t *testing.T, h *harness, tok string, tenant model.TenantID, q string) []string {
	t.Helper()
	out := []string{}
	for _, d := range ledgerPage(t, h, tok, tenant, q) {
		out = append(out, d.SubjectRef)
	}
	return out
}

func ledgerOccurred(t *testing.T, h *harness, tok string, tenant model.TenantID, q string) []string {
	t.Helper()
	out := []string{}
	for _, d := range ledgerPage(t, h, tok, tenant, q) {
		out = append(out, d.OccurredAt)
	}
	return out
}

func ledgerPage(t *testing.T, h *harness, tok string, tenant model.TenantID, q string) []decisionDTO {
	t.Helper()
	r := h.do("GET", "/v1/m/orchestration/decisions"+q, tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ledger%s = %d %s", q, r.code, r.raw)
	}
	var body listResponse[decisionDTO]
	if err := json.Unmarshal([]byte(r.raw), &body); err != nil {
		t.Fatalf("ledger body: %v — %s", err, r.raw)
	}
	return body.Items
}

// TestTenantLedgerDefaultStaysChronologicalAndPaginable pins the half that the opt-in
// exists to protect, and it is the direction I did not test first: making `newest` the
// default looked like a pure improvement and silently broke the public contract, because
// the store issues NO cursor for a custom sort and still answers `has_more: true` — a
// first page that announces more with no way to ask for it. There are consumers with
// `--cursor` today (the CLI, the generated SDKs and the docs).
func TestTenantLedgerDefaultStaysChronologicalAndPaginable(t *testing.T) {
	base := model.NewTimestamp(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC))
	clk := &ledgerClock{at: base}
	g := newRoutedGate()
	h, _ := newHarness(t, WithApprovalGate(g), WithClock(clk))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	for i, n := range []string{"viejo", "medio", "nuevo"} {
		clk.at = model.NewTimestamp(base.Time().Add(time.Duration(i+1) * time.Minute))
		wf := h.createWorkflow(admin, tenant, "wf-"+n, []map[string]any{emitStep("a")})
		if r := h.do("POST", "/v1/m/orchestration/workflows/"+wf["id"].(string)+"/run", admin, nil, tenantHdr(tenant)); r.code != http.StatusAccepted {
			t.Fatalf("run %s = %d %s", n, r.code, r.raw)
		}
	}

	// Sin `order`: cronológico, como siempre.
	porDefecto := ledgerOccurred(t, h, admin, tenant, "")
	if len(porDefecto) != 3 || !(porDefecto[0] < porDefecto[2]) {
		t.Fatalf("el default dejó de ser cronológico: %v", porDefecto)
	}

	// Y RECORTADO SIGUE SIENDO PAGINABLE: si dice que hay más, da el cursor para pedirlo.
	page := ledgerRaw(t, h, admin, tenant, "?limit=2")
	if !page.HasMore {
		t.Fatalf("con 3 filas y limit=2 el motor tiene que decir has_more: %+v", page)
	}
	if page.Cursor == "" {
		t.Fatal("has_more=true con cursor VACÍO: la primera página anuncia más y no hay forma de pedirla")
	}
	// CONTROL POSITIVO: el cursor de verdad continúa la lista, no devuelve lo mismo.
	segunda := ledgerRaw(t, h, admin, tenant, "?limit=2&cursor="+url.QueryEscape(page.Cursor))
	if len(segunda.Items) == 0 {
		t.Fatal("la segunda página vino vacía: el cursor no continúa nada")
	}
	if segunda.Items[0].OccurredAt <= page.Items[len(page.Items)-1].OccurredAt {
		t.Fatalf("la segunda página no avanza: %s tras %s", segunda.Items[0].OccurredAt, page.Items[len(page.Items)-1].OccurredAt)
	}

	// Y el modo opt-in, por contraste, NO promete cursor: es un top-N declarado.
	top := ledgerRaw(t, h, admin, tenant, "?order=newest&limit=2")
	if top.Cursor != "" {
		t.Fatalf("el modo newest no debe emitir cursor (no es paginable), dio %q", top.Cursor)
	}
}

func ledgerRaw(t *testing.T, h *harness, tok string, tenant model.TenantID, q string) listResponse[decisionDTO] {
	t.Helper()
	r := h.do("GET", "/v1/m/orchestration/decisions"+q, tok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("ledger%s = %d %s", q, r.code, r.raw)
	}
	var body listResponse[decisionDTO]
	if err := json.Unmarshal([]byte(r.raw), &body); err != nil {
		t.Fatalf("ledger body: %v — %s", err, r.raw)
	}
	return body
}
