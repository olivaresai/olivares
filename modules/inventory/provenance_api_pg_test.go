// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// The PostgreSQL 16 leg of D08-C3: both read indexes exist on a real isolated
// database, the distinct-receipt page is exact over HTTP against the real store,
// the three focal statements the reader issues are answered from indexes with a
// fixture large enough that the planner has a genuine choice (no planner
// settings are forced), and the elapsed time of a maximum page is measured and
// reported for THIS fixture — not as a general performance claim.
//
// It fails, rather than skips, when the PostgreSQL fixture is unavailable: a
// required witness that could not be taken is not a pass.

const (
	c3PGEntityReceipts   = 30  // + one shared-member receipt = 31 distinct
	c3PGNoiseReceipts    = 400 // other entity, same tenant, each with a conflict
	c3PGOtherTenantRcpts = 50  // same names, other tenant
)

// c3PGFixture seeds the PostgreSQL store through the C1 writer: the entity under
// test with 31 distinct receipts (the shared-member one straddling the 25-row
// boundary as in c3PagerFixture), heavy same-tenant noise on another entity with a
// retained conflict per receipt, and another tenant carrying the same names.
func c3PGFixture(t *testing.T, m *Module, st store.Store, tenant, other model.TenantID) (model.ID, []string, string) {
	t.Helper()
	var events []string
	deliver := func(id string, edge sdkmodel.EdgeObservation) {
		c3Deliver(t, m, c3Event(tenant, id, "label", c3Reg("source-a", 1, "env-a"), edge), nil)
		events = append(events, id)
	}
	for i := 0; i < 24; i++ {
		deliver(fmt.Sprintf("pg-pager-%02d", i), c3AgentEdge("pg-pager", fmt.Sprintf("/f/%02d", i), baseTime))
	}
	deliver("pg-pager-shared", mkEdge("agent", "pg-pager", rkA2AAgent, "pg-pager", sdkmodel.ModeUnknown, sdkmodel.SignalOTEL, "", baseTime))
	for i := 24; i < c3PGEntityReceipts; i++ {
		deliver(fmt.Sprintf("pg-pager-%02d", i), c3AgentEdge("pg-pager", fmt.Sprintf("/f/%02d", i), baseTime))
	}
	for i := 0; i < c3PGNoiseReceipts; i++ {
		id := fmt.Sprintf("pg-noise-%03d", i)
		c3Deliver(t, m, c3Event(tenant, id, "label", nil, c3AgentEdge("pg-noise", fmt.Sprintf("/n/%03d", i), baseTime)), nil)
		// a conflicting redelivery per noise receipt populates the conflict table
		c3Deliver(t, m, c3Event(tenant, id, "label", nil, c3AgentEdge("pg-noise", fmt.Sprintf("/n/%03d/other", i), baseTime)), ErrObservationReceiptConflict)
	}
	for i := 0; i < c3PGOtherTenantRcpts; i++ {
		c3Deliver(t, m, c3Event(other, fmt.Sprintf("pg-pager-%02d", i), "label", nil, c3AgentEdge("pg-pager", fmt.Sprintf("/f/%02d", i), baseTime)), nil)
	}
	agent := c3EntityID(t, st, tenant, kindAgent, "pg-pager")
	ids := make([]string, 0, len(events))
	for _, id := range events {
		ids = append(ids, c3ReceiptID(t, st, tenant, id))
	}
	sort.Strings(ids)
	return agent, ids, c3ReceiptID(t, st, tenant, "pg-pager-shared")
}

// c3PGConn opens one raw application-role connection pinned to the tenant the way
// the product pins it (the RLS GUC), so every plan below carries the same policy
// predicate the real reads carry.
func c3PGConn(t *testing.T, dsn string, tenant model.TenantID) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL plan connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), "SELECT set_config('app.tenant_id', $1, false)", tenant.String()); err != nil {
		t.Fatalf("pin the plan tenant: %v", err)
	}
	return db
}

func c3PGExplain(t *testing.T, db *sql.DB, statement string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN (ANALYZE, BUFFERS, VERBOSE) "+statement, args...)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, statement)
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out.WriteString("  " + line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func c3PGIndexes(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT indexname FROM pg_indexes WHERE tablename = $1", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	return out
}

func c3Columns(t *testing.T, st store.Store, tenant model.TenantID, kind model.Kind) string {
	t.Helper()
	var cols []string
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		cols = repo.Descriptor().AllColumns()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return strings.Join(cols, ", ")
}

func TestC3PostgresObservationHistoryIndexesPlansAndPage(t *testing.T) {
	cfg, pg := c1PostgresFixture(t)
	m, st := c1PostgresOpen(t, cfg, false)
	tenant := c1Tenant(t, st, "c3-pg")
	other := c1Tenant(t, st, "c3-pg-other")

	// 1. Both C3 read indexes exist on the freshly created schema.
	planner := c3PGConn(t, pg.App, tenant)
	members := c3PGIndexes(t, planner, "inventory_observation_member")
	conflicts := c3PGIndexes(t, planner, "inventory_observation_conflict")
	if !members["inventory_member_entity_receipt"] || !members["inventory_member_uniq"] || !conflicts["inventory_conflict_receipt_page"] || !conflicts["inventory_conflict_uniq"] {
		t.Fatalf("PostgreSQL indexes: member=%v conflict=%v", members, conflicts)
	}
	t.Logf("C3_PG_INDEXES member=%v conflict=%v", members, conflicts)

	// 2. Seed through the C1 writer and prove the page over real HTTP + auth.
	seedStart := time.Now()
	agent, want, shared := c3PGFixture(t, m, st, tenant, other)
	t.Logf("C3_PG_FIXTURE entity_receipts=%d noise_receipts=%d other_tenant_receipts=%d seed_elapsed=%s",
		len(want), c3PGNoiseReceipts, c3PGOtherTenantRcpts, time.Since(seedStart).Round(time.Millisecond))
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()
	viewer := h.memberToken(admin, tenant, "v@c3pg.io", auth.RoleViewer, "")
	path := c3ObservationsPath(kindAgent, agent)
	all, pages := c3Walk(t, h, viewer, tenant, path, 25)
	if strings.Join(all, ",") != strings.Join(want, ",") || len(pages) != 2 || len(pages[0]) != 25 || pages[0][24] != shared {
		t.Fatalf("PostgreSQL pages are not exact:\n got %v\nwant %v", pages, want)
	}
	if r := h.do("GET", path, viewer, nil, other); r.code != 403 {
		t.Fatalf("viewer selecting the other tenant = %d %s, want 403", r.code, r.raw)
	}
	noise := c3EntityID(t, st, tenant, kindAgent, "pg-noise")
	noiseItems, _, more := c3Items(t, h.do("GET", c3ObservationsPath(kindAgent, noise), viewer, nil, tenant))
	if len(noiseItems) != 25 || !more {
		t.Fatalf("noise page = %d items more=%v", len(noiseItems), more)
	}
	for _, it := range noiseItems {
		if it["conflicting_redelivery"] != true {
			t.Fatalf("noise receipt without its retained conflict: %v", it)
		}
	}

	// 3. Elapsed time of a MAXIMUM page (25 receipts, 77 repository reads), three
	// consecutive requests through the real router, on this fixture only.
	var elapsed []time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		items, _, more := c3Items(t, h.do("GET", path+"?limit=25", viewer, nil, tenant))
		elapsed = append(elapsed, time.Since(start))
		if len(items) != 25 || !more {
			t.Fatalf("max page = %d items more=%v", len(items), more)
		}
	}
	t.Logf("C3_PG_MAX_PAGE_ELAPSED runs=%v fixture_member_rows~%d (this fixture, one box, no general claim)",
		elapsed, 2*(len(want)+c3PGNoiseReceipts)+1)

	// 4. Plans of the reader's statements, with fresh statistics and no planner
	// settings forced. The statements are the ones genericRepo renders
	// (SELECT <descriptor columns> ... ORDER BY id ASC LIMIT limit+1; the
	// projection as renderDistinctProjection renders it), bound the same way.
	for _, table := range []string{"inventory_observation_member", "inventory_observation_conflict", "inventory_observation_receipt", "inventory_catalog_entry"} {
		if _, err := planner.ExecContext(context.Background(), "ANALYZE "+table); err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}
	projection := c3PGExplain(t, planner,
		"SELECT DISTINCT receipt_id FROM inventory_observation_member WHERE tenant_id = $1 AND receipt_id IS NOT NULL AND entity_kind = $2 AND entity_id = $3 ORDER BY receipt_id ASC LIMIT 26",
		tenant.String(), kindAgent, agent.String())
	anchored := c3PGExplain(t, planner,
		"SELECT DISTINCT receipt_id FROM inventory_observation_member WHERE tenant_id = $1 AND receipt_id IS NOT NULL AND entity_kind = $2 AND entity_id = $3 AND receipt_id > $4 ORDER BY receipt_id ASC LIMIT 26",
		tenant.String(), kindAgent, agent.String(), want[24])
	group := c3PGExplain(t, planner,
		"SELECT "+c3Columns(t, st, tenant, observationMemberKind)+" FROM inventory_observation_member WHERE tenant_id = $1 AND receipt_id = $2 ORDER BY id ASC LIMIT 6",
		tenant.String(), shared)
	conflict := c3PGExplain(t, planner,
		"SELECT "+c3Columns(t, st, tenant, observationConflictKind)+" FROM inventory_observation_conflict WHERE tenant_id = $1 AND receipt_id = $2 ORDER BY id ASC LIMIT 2",
		tenant.String(), c3ReceiptID(t, st, tenant, "pg-noise-000"))
	receipt := c3PGExplain(t, planner,
		"SELECT "+c3Columns(t, st, tenant, observationReceiptKind)+" FROM inventory_observation_receipt WHERE id = $1 AND tenant_id = $2",
		shared, tenant.String())
	catalog := c3PGExplain(t, planner,
		"SELECT "+c3Columns(t, st, tenant, catalogEntryKind)+" FROM inventory_catalog_entry WHERE tenant_id = $1 AND entity_kind = $2 AND entity_id = $3 ORDER BY id ASC LIMIT 2",
		tenant.String(), kindAgent, agent.String())
	t.Logf("C3_PG_PLAN distinct projection (first page):\n%s", projection)
	t.Logf("C3_PG_PLAN distinct projection (anchored):\n%s", anchored)
	t.Logf("C3_PG_PLAN member group by receipt:\n%s", group)
	t.Logf("C3_PG_PLAN conflict existence by receipt:\n%s", conflict)
	t.Logf("C3_PG_PLAN receipt by primary key:\n%s", receipt)
	t.Logf("C3_PG_PLAN catalog precondition:\n%s", catalog)
	for name, plan := range map[string]string{"projection": projection, "anchored projection": anchored} {
		if !strings.Contains(plan, "inventory_member_entity_receipt") || strings.Contains(plan, "Seq Scan on") {
			t.Errorf("%s does not use inventory_member_entity_receipt without a sequential scan:\n%s", name, plan)
		}
	}
	if strings.Contains(group, "Seq Scan on") {
		t.Errorf("member group read is a sequential scan:\n%s", group)
	}
	if !strings.Contains(conflict, "inventory_conflict_receipt_page") || strings.Contains(conflict, "Seq Scan on") {
		t.Errorf("conflict existence does not use inventory_conflict_receipt_page without a sequential scan:\n%s", conflict)
	}
	for name, plan := range map[string]string{"receipt": receipt, "catalog": catalog} {
		if strings.Contains(plan, "Seq Scan on") {
			t.Errorf("%s lookup is a sequential scan:\n%s", name, plan)
		}
	}

	// 5. Corruption is refused on PostgreSQL exactly as on SQLite: the whole page.
	c3Update(t, st, tenant, observationReceiptKind,
		func(r model.Record) bool { return r.String(model.ColID) == want[3] },
		func(r model.Record) { r[colMemberCount] = int64(0) })
	if r := h.do("GET", path+"?limit=25", viewer, nil, tenant); r.code != 500 || r.body["items"] != nil {
		t.Fatalf("corrupted PostgreSQL page = %d %s, want 500 with no items", r.code, r.raw)
	}
	if items, _, _ := c3Items(t, h.do("GET", path+"?limit=3", viewer, nil, tenant)); len(items) != 3 {
		t.Fatalf("page before the corrupt receipt = %d items, want 3", len(items))
	}
}
