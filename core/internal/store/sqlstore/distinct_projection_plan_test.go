// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file measures the catalog projection's PLAN and EXAMINED-ROW cost on
// both engines over a sparse estate, through the statement the engine really
// renders, with the keys it really seeded. It exists because the first plan
// fixture explained literal keys that matched no row and, on PostgreSQL, a scan
// node that never executed (independent review F1). The correctness oracle is
// computed in Go from the seed; the engine plan is evidence of COST, never the
// sole correctness assertion.
//
// The mirrored relation dpt_grant carries the same index shape as the product's
// sessions_channel_grant_catalog; the sessions module pins that column order in
// TestChannelGrantCatalogIndexPinsTheMeasuredShape, and this file pins it too.

// catalogProjectionIndexColumns is the measured index shape. It is a literal on
// purpose: the module that declares the product index pins the same literal.
var catalogProjectionIndexColumns = []string{
	"tenant_id", "workspace_id", "subject_kind", "subject_ref", "state", "can_read", "channel_id", "expires_at",
}

// catalogProjectionSubject is one closure subject with its oracle: the Channel
// IDs a current, active read grant makes visible (sorted, distinct) and the rows
// that sit INSIDE the subject's index range without contributing a value —
// expired grants and duplicate grants — which is the only noise a bounded arm
// may legitimately examine.
type catalogProjectionSubject struct {
	kind, ref string
	visible   []string
	noise     int
}

func (s catalogProjectionSubject) alternative() []model.Filter {
	return []model.Filter{
		{Column: "subject_kind", Op: model.OpEq, Value: s.kind},
		{Column: "subject_ref", Op: model.OpEq, Value: s.ref},
	}
}

// catalogProjectionEstate is the seeded sparse estate: almost every grant of the
// confined workspace belongs to strangers; three closure subjects have known
// grant sets (overlapping, disjoint, absent); a second workspace of the same
// tenant and a second tenant hold grants of the SAME reader subject that must
// stay invisible.
type catalogProjectionEstate struct {
	tenant               model.TenantID
	workspace            model.ID
	now                  model.Timestamp
	strangerRows         int
	workspaceRows        int
	reader, group, other catalogProjectionSubject
	absent               catalogProjectionSubject
	foreignWorkspaceRows int
	foreignTenantRows    int
}

func sortedIDs(n int) []string {
	out := make([]string, n)
	for index := range out {
		out[index] = model.NewID().String()
	}
	sort.Strings(out)
	return out
}

func sortedDistinct(groups ...[]string) []string {
	set := map[string]struct{}{}
	for _, group := range groups {
		for _, value := range group {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func seedCatalogProjectionEstate(t *testing.T, st store.Store) catalogProjectionEstate {
	t.Helper()
	tenant := provisionTenant(t, st, "dpt-plan")
	workspace, otherWorkspace := distinctProjectionWorkspaces(t, st, tenant)
	foreignTenant := provisionTenant(t, st, "dpt-plan-foreign")
	_, foreignWorkspace := distinctProjectionWorkspaces(t, st, foreignTenant)
	now := model.NewTimestamp(time.Now().UTC())
	past := now.Time().Add(-time.Hour)
	future := now.Time().Add(time.Hour)

	var rows []distinctProjectionRow
	row := func(ws model.ID, channel, kind, ref, state string, read bool, expires *time.Time) {
		rows = append(rows, distinctProjectionRow{
			workspace: ws, channel: channel, kind: kind, ref: ref, state: state, canRead: read, expires: expires,
		})
	}
	// Strangers: 40 subjects with 100 grants each, some write-only, some expired.
	const strangers, perStranger = 40, 100
	for subject := 0; subject < strangers; subject++ {
		for channel := 0; channel < perStranger; channel++ {
			var expires *time.Time
			if channel%5 == 0 {
				expires = &past
			}
			row(workspace, model.NewID().String(), "user", fmt.Sprintf("stranger-%02d", subject),
				"active", channel%7 != 0, expires)
		}
	}
	// The reader: 30 visible Channels; three of them ALSO carry a second active
	// read grant (a later generation still current), six expired read grants,
	// four write-only grants and three revoked grants. Only the duplicates and the
	// expired rows sit inside the (active, can_read) index range.
	readerVisible := sortedIDs(30)
	reader := catalogProjectionSubject{kind: "user", ref: "reader-1", visible: readerVisible}
	for _, channel := range readerVisible {
		row(workspace, channel, reader.kind, reader.ref, "active", true, nil)
	}
	for _, channel := range readerVisible[:3] {
		row(workspace, channel, reader.kind, reader.ref, "active", true, &future)
		reader.noise++
	}
	for _, channel := range sortedIDs(6) {
		row(workspace, channel, reader.kind, reader.ref, "active", true, &past)
		reader.noise++
	}
	for _, channel := range sortedIDs(4) {
		row(workspace, channel, reader.kind, reader.ref, "active", false, nil)
	}
	for _, channel := range sortedIDs(3) {
		row(workspace, channel, reader.kind, reader.ref, "revoked", true, nil)
	}
	// A group the reader belongs to: ten Channels shared with the reader's own
	// grants (overlap) plus ten of its own, and two expired grants.
	groupOwn := sortedIDs(10)
	group := catalogProjectionSubject{
		kind: "user_group", ref: "group-1", visible: sortedDistinct(readerVisible[10:20], groupOwn),
	}
	for _, channel := range group.visible {
		row(workspace, channel, group.kind, group.ref, "active", true, nil)
	}
	for _, channel := range sortedIDs(2) {
		row(workspace, channel, group.kind, group.ref, "active", true, &past)
		group.noise++
	}
	// A disjoint subject with fifteen Channels of its own and no noise.
	other := catalogProjectionSubject{kind: "user", ref: "reader-2", visible: sortedIDs(15)}
	for _, channel := range other.visible {
		row(workspace, channel, other.kind, other.ref, "active", true, nil)
	}
	absent := catalogProjectionSubject{kind: "user", ref: "nobody"}
	workspaceRows := len(rows)
	// The same reader subject in another workspace of the same tenant.
	const foreignWorkspaceRows = 50
	for _, channel := range sortedIDs(foreignWorkspaceRows) {
		row(otherWorkspace, channel, reader.kind, reader.ref, "active", true, nil)
	}
	seedDistinctProjectionRows(t, st, tenant, rows)
	// And in another tenant altogether.
	const foreignTenantRows = 50
	var foreign []distinctProjectionRow
	for _, channel := range sortedIDs(foreignTenantRows) {
		foreign = append(foreign, distinctProjectionRow{
			workspace: foreignWorkspace, channel: channel, kind: reader.kind, ref: reader.ref,
			state: "active", canRead: true,
		})
	}
	seedDistinctProjectionRows(t, st, foreignTenant, foreign)
	return catalogProjectionEstate{
		tenant: tenant, workspace: workspace, now: now,
		strangerRows: strangers * perStranger, workspaceRows: workspaceRows,
		reader: reader, group: group, other: other, absent: absent,
		foreignWorkspaceRows: foreignWorkspaceRows, foreignTenantRows: foreignTenantRows,
	}
}

// expect is the Go oracle: the ordered distinct union of the subjects' visible
// Channels strictly after the anchor, cut at limit with the lookahead.
func (e catalogProjectionEstate) expect(after string, limit int, subjects ...catalogProjectionSubject) store.DistinctPage {
	var groups [][]string
	for _, subject := range subjects {
		groups = append(groups, subject.visible)
	}
	var values []string
	for _, value := range sortedDistinct(groups...) {
		if value > after {
			values = append(values, value)
		}
	}
	page := store.DistinctPage{Values: values}
	if len(values) > limit {
		page.Values = values[:limit]
		page.HasMore = true
	}
	if len(page.Values) == 0 {
		page.Values = nil
	}
	return page
}

// moduleProjection is the projection the sessions module builds for one subject
// batch (colCommState, colCommCanRead, colCommExpiresAt unset-or-after the
// observed database time, one alternative per closure subject, the exclusive
// anchor), BEFORE workspace confinement forces its lineage predicate.
func (e catalogProjectionEstate) moduleProjection(after string, limit int, subjects ...catalogProjectionSubject) store.DistinctProjection {
	p := store.DistinctProjection{
		Column: "channel_id", Limit: limit, After: after,
		Filters: []model.Filter{
			{Column: "state", Op: model.OpEq, Value: "active"},
			{Column: "can_read", Op: model.OpEq, Value: true},
			{Column: "expires_at", Op: model.OpUnsetOrGt, Value: e.now.String()},
		},
	}
	for _, subject := range subjects {
		p.AnyOf = append(p.AnyOf, subject.alternative())
	}
	return p
}

// confinedProjection is moduleProjection after store.ConfineWorkspace forced the
// lineage predicate: confinedGenericRepo.projectDistinct appends
// workspaceBoundary.filterFor (OpEq on the lineage column for an ID-encoded,
// unset-hidden lineage) through forceQuery, which places it LAST. The plan is
// explained over exactly this shape, and projectConfined asserts the confined
// path and this shape answer identically.
func (e catalogProjectionEstate) confinedProjection(after string, limit int, subjects ...catalogProjectionSubject) store.DistinctProjection {
	p := e.moduleProjection(after, limit, subjects...)
	p.Filters = append(p.Filters, model.Filter{Column: "workspace_id", Op: model.OpEq, Value: e.workspace.String()})
	return p
}

// bound is the examined-row ceiling the cost contract promises for a projection
// with arms: every arm may read its limit+1 values plus the noise rows inside its
// own index range, and nothing that belongs to another subject.
func (e catalogProjectionEstate) bound(limit int, subjects ...catalogProjectionSubject) int {
	total := 0
	for _, subject := range subjects {
		total += limit + 1 + subject.noise
	}
	return total
}

// projectConfined runs the projection through the real confined path (tenant
// View, store.ConfineWorkspace, the confined repository's DistinctProjector) and
// through the raw repository with the forced filter spelled out, and requires
// both answers to be identical before returning the confined one.
func projectConfined(t *testing.T, st store.Store, e catalogProjectionEstate, p store.DistinctProjection) store.DistinctPage {
	t.Helper()
	ctx := context.Background()
	var confined, spelled store.DistinctPage
	if err := st.View(ctx, e.tenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, e.workspace)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		projector, ok := repo.(store.DistinctProjector)
		if !ok {
			return errors.New("confined repository lost the distinct projector")
		}
		confined, err = projector.ProjectDistinct(ctx, p)
		if err != nil {
			return err
		}
		rawRepo, err := raw.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		spelledProjection := p
		spelledProjection.Filters = append(append([]model.Filter(nil), p.Filters...),
			model.Filter{Column: "workspace_id", Op: model.OpEq, Value: e.workspace.String()})
		spelled, err = rawRepo.(store.DistinctProjector).ProjectDistinct(ctx, spelledProjection)
		return err
	}); err != nil {
		t.Fatalf("project: %v", err)
	}
	if fmt.Sprint(confined) != fmt.Sprint(spelled) {
		t.Fatalf("confined path %+v differs from the spelled-out forced filter %+v", confined, spelled)
	}
	return confined
}

// renderCatalogStatement renders the exact statement the engine executes for a
// confined projection, so EXPLAIN measures the production shape and nothing
// hand-written.
func renderCatalogStatement(t *testing.T, st store.Store, e catalogProjectionEstate, p store.DistinctProjection) (distinctProjectionStatement, string) {
	t.Helper()
	var statement distinctProjectionStatement
	var relation string
	if err := st.View(context.Background(), e.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		generic, ok := repo.(*genericRepo)
		if !ok {
			return fmt.Errorf("tenant scope yielded %T, not the generic repository", repo)
		}
		relation = generic.relation()
		statement, err = generic.renderDistinctProjection(p)
		return err
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return statement, relation
}

// legacyORStatement is the SUPERSEDED single-statement shape — one ORed group of
// subject alternatives — rendered by hand with the same bound values. It is kept
// as the equivalence witness (both shapes must answer identically) and as the
// cost contrast the measurement records.
func legacyORStatement(relation string, e catalogProjectionEstate, p store.DistinctProjection) (string, []any) {
	where := []string{"tenant_id = ?", "channel_id IS NOT NULL"}
	args := []any{e.tenant.String()}
	for _, f := range p.Filters {
		switch f.Op {
		case model.OpUnsetOrGt:
			where = append(where, "("+f.Column+" IS NULL OR "+f.Column+" > ?)")
		default:
			where = append(where, f.Column+" = ?")
		}
		args = append(args, f.Value)
	}
	if len(p.AnyOf) > 0 {
		var alternatives []string
		for _, alternative := range p.AnyOf {
			var parts []string
			for _, f := range alternative {
				parts = append(parts, f.Column+" = ?")
				args = append(args, f.Value)
			}
			alternatives = append(alternatives, "("+strings.Join(parts, " AND ")+")")
		}
		where = append(where, "("+strings.Join(alternatives, " OR ")+")")
	}
	if p.After != "" {
		where = append(where, "channel_id > ?")
		args = append(args, p.After)
	}
	return fmt.Sprintf("SELECT DISTINCT channel_id FROM %s WHERE %s ORDER BY channel_id ASC LIMIT %d",
		relation, strings.Join(where, " AND "), p.Limit+1), args
}

// queryValues executes a projection-shaped statement on a raw connection and
// folds the rows into a DistinctPage with the same limit rule as the engine.
func queryValues(t *testing.T, ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, text string, args []any, limit int) store.DistinctPage {
	t.Helper()
	rows, err := q.QueryContext(ctx, text, args...)
	if err != nil {
		t.Fatalf("query %s: %v", text, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	page := store.DistinctPage{Values: out}
	if len(out) > limit {
		page.Values = out[:limit]
		page.HasMore = true
	}
	return page
}

type catalogProjectionCase struct {
	name     string
	subjects []catalogProjectionSubject
	after    string
	limit    int
}

func catalogProjectionCases(e catalogProjectionEstate) []catalogProjectionCase {
	union := sortedDistinct(e.reader.visible, e.group.visible)
	return []catalogProjectionCase{
		{name: "single subject", subjects: []catalogProjectionSubject{e.reader}, limit: 10},
		{name: "two overlapping subjects", subjects: []catalogProjectionSubject{e.reader, e.group}, limit: 10},
		{name: "two disjoint subjects", subjects: []catalogProjectionSubject{e.reader, e.other}, limit: 10},
		{name: "three subjects, one absent", subjects: []catalogProjectionSubject{e.reader, e.other, e.absent}, limit: 10},
		{name: "overlapping subjects after an anchor", subjects: []catalogProjectionSubject{e.reader, e.group}, after: union[14], limit: 10},
		{name: "anchor past every value", subjects: []catalogProjectionSubject{e.reader, e.group}, after: union[len(union)-1], limit: 10},
		{name: "page wider than the closure", subjects: []catalogProjectionSubject{e.other}, limit: 200},
	}
}

// requireCatalogEstateSeeded is the control the first fixture lacked: the keys
// the plan is explained with must match rows, and the reader must project a
// nonzero page, before any plan is recorded.
func requireCatalogEstateSeeded(t *testing.T, st store.Store, e catalogProjectionEstate) {
	t.Helper()
	ctx := context.Background()
	var counted int
	if err := st.View(ctx, e.tenant, func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, e.workspace)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(distinctProjectionEntity.Kind)
		if err != nil {
			return err
		}
		// Count through the generic List in pages so the control is independent
		// of the projection under measurement.
		var cursor string
		for {
			records, page, err := repo.List(ctx, model.Query{Limit: 1000, Cursor: cursor})
			if err != nil {
				return err
			}
			counted += len(records)
			if !page.HasMore {
				return nil
			}
			cursor = page.Cursor
		}
	}); err != nil {
		t.Fatalf("count seeded rows: %v", err)
	}
	if counted != e.workspaceRows {
		t.Fatalf("confined workspace holds %d rows, seeded %d: the explained keys do not match the estate", counted, e.workspaceRows)
	}
	page := projectConfined(t, st, e, e.moduleProjection("", 10, e.reader))
	if len(page.Values) == 0 {
		t.Fatal("the reader projects zero Channels: the estate is not the one the plan will describe")
	}
	if !samePage(e.expect("", 10, e.reader), page) {
		t.Fatalf("reader projection = %+v, want %+v", page, e.expect("", 10, e.reader))
	}
	t.Logf("estate: %d rows in the confined workspace (%d of strangers), reader visible=%d noise=%d, group visible=%d noise=%d, other visible=%d, %d rows in another workspace, %d in another tenant",
		e.workspaceRows, e.strangerRows, len(e.reader.visible), e.reader.noise, len(e.group.visible), e.group.noise,
		len(e.other.visible), e.foreignWorkspaceRows, e.foreignTenantRows)
}

func samePage(a, b store.DistinctPage) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

var sqliteTableScan = regexp.MustCompile(`\bSCAN ` + regexp.QuoteMeta(distinctProjectionEntity.Table) + `\b`)

// explainSQLite records EXPLAIN QUERY PLAN for the rendered statement and
// asserts its cost shape: every access to the relation is a SEARCH through the
// covering catalog index constrained on the alternative's subject columns (and
// the anchor when set), one per arm, and there is no table SCAN.
func explainSQLite(t *testing.T, st store.Store, statement distinctProjectionStatement, anchored bool) []string {
	t.Helper()
	s := st.(*sqlStore)
	rows, err := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+statement.text, statement.args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var lines []string
	searches := 0
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		lines = append(lines, fmt.Sprintf("%d %d %s", id, parent, detail))
		if sqliteTableScan.MatchString(detail) {
			t.Fatalf("sqlite plan scans the relation instead of searching the catalog index: %s", detail)
		}
		if strings.HasPrefix(detail, "SEARCH "+distinctProjectionEntity.Table+" ") {
			searches++
			if !strings.Contains(detail, "USING COVERING INDEX dpt_grant_catalog (") {
				t.Fatalf("sqlite search does not use the covering catalog index: %s", detail)
			}
			// Without alternatives (the logged contrast, not the catalog's shape)
			// the index can only be bounded on the tenant and workspace prefix.
			if statement.arms == 0 {
				continue
			}
			if !strings.Contains(detail, "subject_kind=? AND subject_ref=?") {
				t.Fatalf("sqlite arm dropped the subject bound: %s", detail)
			}
			if !strings.Contains(detail, "state=? AND can_read=?") {
				t.Fatalf("sqlite arm dropped the state/read bound: %s", detail)
			}
			if anchored && !strings.Contains(detail, "channel_id>?") {
				t.Fatalf("sqlite arm dropped the keyset anchor: %s", detail)
			}
		}
	}
	want := statement.arms
	if want == 0 {
		want = 1 // the single common arm
	}
	if searches != want {
		t.Fatalf("sqlite plan has %d index searches, want one per arm (%d): %v", searches, want, lines)
	}
	return lines
}

// postgresMeasure is what EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) says the
// engine did: rows the scan nodes examined (rows they produced plus rows their
// filter removed, per loop), buffers touched, wall time, and the shape facts the
// assertions need.
type postgresMeasure struct {
	examinedRows     int
	sharedBlocks     int
	executionMs      float64
	planningMs       float64
	scanNodes        int
	indexNames       map[string]int
	neverExecuted    int
	armsWithoutBound int
	nodeTypes        map[string]int
	rendered         []string
}

func walkPostgresPlan(node map[string]any, depth int, visit func(map[string]any, int)) {
	visit(node, depth)
	children, _ := node["Plans"].([]any)
	for _, child := range children {
		if m, ok := child.(map[string]any); ok {
			walkPostgresPlan(m, depth+1, visit)
		}
	}
}

func planNumber(node map[string]any, key string) float64 {
	value, _ := node[key].(float64)
	return value
}

func planString(node map[string]any, key string) string {
	value, _ := node[key].(string)
	return value
}

// explainPostgres runs EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) on a connection
// that carries the tenant GUC, so the FORCE-RLS one-time filter is true and the
// scan nodes really execute, and folds the JSON into a postgresMeasure.
func explainPostgres(t *testing.T, conn *sql.Conn, dia interface{ Rebind(string) string }, text string, args []any) postgresMeasure {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	if err := conn.QueryRowContext(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+dia.Rebind(text), args...).Scan(&raw); err != nil {
		t.Fatalf("explain analyze: %v", err)
	}
	var explained []map[string]any
	if err := json.Unmarshal(raw, &explained); err != nil || len(explained) != 1 {
		t.Fatalf("decode explain json (%d entries): %v\n%s", len(explained), err, raw)
	}
	root, ok := explained[0]["Plan"].(map[string]any)
	if !ok {
		t.Fatalf("explain json has no Plan: %s", raw)
	}
	measure := postgresMeasure{
		executionMs: planNumber(explained[0], "Execution Time"),
		planningMs:  planNumber(explained[0], "Planning Time"),
		indexNames:  map[string]int{},
		nodeTypes:   map[string]int{},
	}
	measure.sharedBlocks = int(planNumber(root, "Shared Hit Blocks") + planNumber(root, "Shared Read Blocks"))
	walkPostgresPlan(root, 0, func(node map[string]any, depth int) {
		nodeType := planString(node, "Node Type")
		loops := planNumber(node, "Actual Loops")
		measure.nodeTypes[nodeType]++
		if loops == 0 {
			measure.neverExecuted++
		}
		line := fmt.Sprintf("%s%s", strings.Repeat("  ", depth), nodeType)
		if name := planString(node, "Index Name"); name != "" {
			line += " using " + name
		}
		if relation := planString(node, "Relation Name"); relation != "" {
			line += " on " + relation
		}
		line += fmt.Sprintf(" (actual rows=%.0f loops=%.0f", planNumber(node, "Actual Rows"), loops)
		if removed, ok := node["Rows Removed by Filter"]; ok {
			line += fmt.Sprintf(" removed_by_filter=%.0f", removed)
		}
		if fetches, ok := node["Heap Fetches"]; ok {
			line += fmt.Sprintf(" heap_fetches=%.0f", fetches)
		}
		line += fmt.Sprintf(" shared_hit=%.0f)", planNumber(node, "Shared Hit Blocks"))
		if cond := planString(node, "Index Cond"); cond != "" {
			line += "\n" + strings.Repeat("  ", depth+1) + "Index Cond: " + cond
		}
		if filter := planString(node, "Filter"); filter != "" {
			line += "\n" + strings.Repeat("  ", depth+1) + "Filter: " + filter
		}
		measure.rendered = append(measure.rendered, line)
		isScan := strings.HasSuffix(nodeType, "Scan") &&
			(planString(node, "Relation Name") == distinctProjectionEntity.Table || nodeType == "Bitmap Index Scan")
		if !isScan {
			return
		}
		measure.scanNodes++
		measure.indexNames[planString(node, "Index Name")]++
		measure.examinedRows += int(math.Round(loops * (planNumber(node, "Actual Rows") + planNumber(node, "Rows Removed by Filter"))))
		if !strings.Contains(planString(node, "Index Cond"), "subject_ref") {
			measure.armsWithoutBound++
		}
	})
	return measure
}

// requireBoundedPostgresPlan asserts the cost contract on a measured plan with
// arms: every scan node is an executed index scan of the catalog index carrying
// the subject bound, there is one per arm, and the examined rows stay within the
// closure bound although the workspace holds thousands of other grants.
func requireBoundedPostgresPlan(t *testing.T, name string, statement distinctProjectionStatement, measure postgresMeasure, bound, strangerRows int) {
	t.Helper()
	plan := strings.Join(measure.rendered, "\n")
	if measure.neverExecuted > 0 {
		t.Fatalf("%s: %d plan nodes never executed — the measurement is not of the index:\n%s", name, measure.neverExecuted, plan)
	}
	if measure.scanNodes != statement.arms {
		t.Fatalf("%s: %d scan nodes for %d arms:\n%s", name, measure.scanNodes, statement.arms, plan)
	}
	for indexName, count := range measure.indexNames {
		if indexName != "dpt_grant_catalog" {
			t.Fatalf("%s: %d scan nodes use %q instead of the catalog index:\n%s", name, count, indexName, plan)
		}
	}
	for nodeType := range measure.nodeTypes {
		if nodeType == "Seq Scan" || nodeType == "Bitmap Heap Scan" || nodeType == "Bitmap Index Scan" {
			t.Fatalf("%s: plan contains a %s:\n%s", name, nodeType, plan)
		}
	}
	if measure.armsWithoutBound > 0 {
		t.Fatalf("%s: %d arms lost subject_ref from their Index Cond:\n%s", name, measure.armsWithoutBound, plan)
	}
	if measure.examinedRows > bound {
		t.Fatalf("%s: examined %d rows, bound %d (closure arms × (limit+1) + in-range noise); %d stranger rows share the workspace:\n%s",
			name, measure.examinedRows, bound, strangerRows, plan)
	}
}

// refreshPostgresStatistics runs VACUUM (ANALYZE) as the relation OWNER: the
// application role does not own the relation in the split-owner posture, and
// PostgreSQL silently skips a VACUUM or ANALYZE issued by a non-owner (WARNING,
// not error), which would leave the planner on default statistics while the
// evidence claimed otherwise. VACUUM also sets the visibility map, so heap
// fetches stop hiding how many index entries an index-only scan visited.
func refreshPostgresStatistics(t *testing.T, ownerDSN, relation string) {
	t.Helper()
	ownerDB, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		t.Fatalf("open owner connection: %v", err)
	}
	defer ownerDB.Close()
	ctx := context.Background()
	if _, err := ownerDB.ExecContext(ctx, "VACUUM (ANALYZE) "+relation); err != nil {
		t.Fatalf("vacuum analyze as owner: %v", err)
	}
	var reltuples float64
	if err := ownerDB.QueryRowContext(ctx,
		"SELECT reltuples FROM pg_class WHERE oid = $1::regclass", relation).Scan(&reltuples); err != nil {
		t.Fatalf("read reltuples: %v", err)
	}
	if reltuples <= 0 {
		t.Fatalf("statistics not refreshed: reltuples=%v", reltuples)
	}
	t.Logf("postgres statistics refreshed as owner: reltuples=%.0f", reltuples)
}

func TestCatalogProjectionPlanAndWorkBothEngines(t *testing.T) {
	if got := distinctProjectionEntity.Indexes[0].Columns; fmt.Sprint(got) != fmt.Sprint(catalogProjectionIndexColumns) {
		t.Fatalf("mirrored index %v is not the measured shape %v", got, catalogProjectionIndexColumns)
	}
	ctx := context.Background()

	t.Run("sqlite", func(t *testing.T) {
		st := openSQLiteTest(t, registerDistinctProjectionEntity)
		e := seedCatalogProjectionEstate(t, st)
		if _, err := st.(*sqlStore).db.ExecContext(ctx, "ANALYZE "+distinctProjectionEntity.Table); err != nil {
			t.Fatalf("analyze: %v", err)
		}
		requireCatalogEstateSeeded(t, st, e)
		for _, tc := range catalogProjectionCases(e) {
			t.Run(tc.name, func(t *testing.T) {
				want := e.expect(tc.after, tc.limit, tc.subjects...)
				got := projectConfined(t, st, e, e.moduleProjection(tc.after, tc.limit, tc.subjects...))
				if !samePage(got, want) {
					t.Fatalf("projection = %+v, want %+v", got, want)
				}
				statement, relation := renderCatalogStatement(t, st, e, e.confinedProjection(tc.after, tc.limit, tc.subjects...))
				legacyText, legacyArgs := legacyORStatement(relation, e, e.confinedProjection(tc.after, tc.limit, tc.subjects...))
				if legacy := queryValues(t, ctx, st.(*sqlStore).db, legacyText, legacyArgs, tc.limit); !samePage(legacy, want) {
					t.Fatalf("superseded ORed statement = %+v, union statement/oracle %+v: the shapes are not equivalent", legacy, want)
				}
				plan := explainSQLite(t, st, statement, tc.after != "")
				t.Logf("sqlite plan, %d arms, %d values has_more=%t:\n  %s", statement.arms, len(got.Values), got.HasMore, strings.Join(plan, "\n  "))
			})
		}
		// Contrast, logged not asserted: the projection without alternatives is
		// "every current read grant of the workspace"; its plan cannot use the
		// subject columns and is not the catalog's shape.
		statement, _ := renderCatalogStatement(t, st, e, e.confinedProjection("", 10))
		plan := explainSQLite(t, st, statement, false)
		t.Logf("sqlite plan, no alternatives (not the catalog's shape):\n  %s", strings.Join(plan, "\n  "))
	})

	t.Run("postgres", func(t *testing.T) {
		pg := isolatedPGSplit(t)
		st, err := Open(ctx, store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 4,
		}, registerDistinctProjectionEntity)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		s := st.(*sqlStore)
		e := seedCatalogProjectionEstate(t, st)
		if names := indexNamesOf(t, st, store.EnginePostgres, distinctProjectionEntity.Table); !hasIndex(names, "dpt_grant_catalog") {
			t.Fatalf("estate lacks the declared catalog index: %v", names)
		}
		_, relation := renderCatalogStatement(t, st, e, e.confinedProjection("", 10, e.reader))
		requireCatalogEstateSeeded(t, st, e)

		// One pinned application connection with the tenant GUC bound, so the
		// FORCE-RLS one-time filter is true and the scans execute.
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin connection: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, err := conn.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, false)", e.tenant.String()); err != nil {
			t.Fatalf("bind tenant guc: %v", err)
		}
		var visibleThroughGUC int
		if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM "+relation+" WHERE workspace_id = $1", e.workspace.String()).Scan(&visibleThroughGUC); err != nil {
			t.Fatalf("count through the bound guc: %v", err)
		}
		if visibleThroughGUC != e.workspaceRows {
			t.Fatalf("pinned connection sees %d workspace rows through RLS, seeded %d", visibleThroughGUC, e.workspaceRows)
		}

		// The cases are measured twice: on the planner's DEFAULT statistics (a
		// freshly written relation nobody analyzed, which is the condition the
		// independent review measured the superseded shape under) and after
		// VACUUM (ANALYZE) as the owner. The union shape's bound must hold in both,
		// because its index range is fixed by the equality prefix and owes nothing
		// to statistics; the superseded ORed shape is recorded in both as the
		// contrast and asserted in neither.
		measure := func(phase string) {
			for _, tc := range catalogProjectionCases(e) {
				t.Run(phase+": "+tc.name, func(t *testing.T) {
					want := e.expect(tc.after, tc.limit, tc.subjects...)
					got := projectConfined(t, st, e, e.moduleProjection(tc.after, tc.limit, tc.subjects...))
					if !samePage(got, want) {
						t.Fatalf("projection = %+v, want %+v", got, want)
					}
					confined := e.confinedProjection(tc.after, tc.limit, tc.subjects...)
					statement, relation := renderCatalogStatement(t, st, e, confined)
					legacyText, legacyArgs := legacyORStatement(relation, e, confined)
					if legacy := queryValues(t, ctx, conn, s.dia.Rebind(legacyText), legacyArgs, tc.limit); !samePage(legacy, want) {
						t.Fatalf("superseded ORed statement = %+v, union statement/oracle %+v: the shapes are not equivalent", legacy, want)
					}
					bound := e.bound(tc.limit, tc.subjects...)
					measured := explainPostgres(t, conn, s.dia, statement.text, statement.args)
					requireBoundedPostgresPlan(t, tc.name, statement, measured, bound, e.strangerRows)
					legacy := explainPostgres(t, conn, s.dia, legacyText, legacyArgs)
					t.Logf("MEASURE postgres [%s] %q: arms=%d values=%d has_more=%t examined_rows=%d bound=%d shared_blocks=%d exec_ms=%.3f plan_ms=%.3f | superseded ORed shape: examined_rows=%d shared_blocks=%d exec_ms=%.3f | %d stranger rows in the workspace",
						phase, tc.name, statement.arms, len(got.Values), got.HasMore, measured.examinedRows, bound, measured.sharedBlocks,
						measured.executionMs, measured.planningMs, legacy.examinedRows, legacy.sharedBlocks, legacy.executionMs, e.strangerRows)
					t.Logf("postgres executed plan, union shape:\n  %s", strings.Join(measured.rendered, "\n  "))
					t.Logf("postgres executed plan, superseded ORed shape:\n  %s", strings.Join(legacy.rendered, "\n  "))
				})
			}
		}
		measure("default statistics")
		refreshPostgresStatistics(t, pg.Owner, relation)
		measure("refreshed statistics")

		// Positive control of the instrument: the projection WITHOUT alternatives
		// ("every current read grant of the workspace") has no subject bound to use,
		// so its executed scan must examine the stranger rows the bounded arms never
		// touch. If the counter did not see that, the bounded assertions above would
		// prove nothing.
		t.Run("instrument control without alternatives", func(t *testing.T) {
			statement, _ := renderCatalogStatement(t, st, e, e.confinedProjection("", 10))
			measure := explainPostgres(t, conn, s.dia, statement.text, statement.args)
			if measure.neverExecuted > 0 {
				t.Fatalf("control plan never executed:\n%s", strings.Join(measure.rendered, "\n"))
			}
			if measure.examinedRows < e.strangerRows/2 {
				t.Fatalf("control examined only %d rows over %d stranger rows: the examined-row counter does not see a workspace-wide scan:\n%s",
					measure.examinedRows, e.strangerRows, strings.Join(measure.rendered, "\n"))
			}
			t.Logf("MEASURE postgres control (no alternatives, not the catalog's shape): examined_rows=%d shared_blocks=%d exec_ms=%.3f\n  %s",
				measure.examinedRows, measure.sharedBlocks, measure.executionMs, strings.Join(measure.rendered, "\n  "))
		})

		// The worst admissible shape executes on PostgreSQL too (SQLite is covered
		// by TestDistinctProjectionRendersOneBoundedArmPerAlternative): 256 arms of
		// 8 filters over the full common set, under the 65535 Bind ceiling.
		t.Run("worst admissible shape executes", func(t *testing.T) {
			worst := store.DistinctProjection{Column: "channel_id", Limit: store.DistinctProjectionMaxLimit, After: "0"}
			for index := 0; index < store.DistinctProjectionMaxFilters; index++ {
				worst.Filters = append(worst.Filters, model.Filter{Column: "state", Op: model.OpEq, Value: fmt.Sprintf("s%d", index)})
			}
			for index := 0; index < store.DistinctProjectionMaxAlternatives; index++ {
				var alternative []model.Filter
				for filter := 0; filter < store.DistinctProjectionMaxAlternativeFilters; filter++ {
					alternative = append(alternative, model.Filter{Column: "subject_ref", Op: model.OpEq, Value: fmt.Sprintf("r%d-%d", index, filter)})
				}
				worst.AnyOf = append(worst.AnyOf, alternative)
			}
			if worst.BoundValues() != store.DistinctProjectionWorstCaseBoundValues {
				t.Fatalf("worst shape binds %d values, want %d", worst.BoundValues(), store.DistinctProjectionWorstCaseBoundValues)
			}
			// Through the tenant scope's raw repository: the exact worst admissible
			// shape, 256 arms each repeating the 16 common filters and the anchor.
			var page store.DistinctPage
			started := time.Now()
			if err := st.View(ctx, e.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(distinctProjectionEntity.Kind)
				if err != nil {
					return err
				}
				page, err = repo.(store.DistinctProjector).ProjectDistinct(ctx, worst)
				return err
			}); err != nil {
				t.Fatalf("worst admissible shape did not execute on postgres: %v", err)
			}
			if len(page.Values) != 0 || page.HasMore {
				t.Fatalf("contradictory worst shape returned %+v", page)
			}
			t.Logf("worst admissible shape (%d arms, %d bound values) executed on postgres in %s",
				store.DistinctProjectionMaxAlternatives, store.DistinctProjectionWorstCaseBoundValues, time.Since(started))
			// Under confinement the forced lineage predicate is one more common
			// filter, and the confined statement is validated AFTER it is forced in:
			// a caller that fills the common bound leaves no room and is refused
			// before any SQL is rendered, never rendered over the ceiling.
			err := st.View(ctx, e.tenant, func(raw store.Scope) error {
				sc, err := store.ConfineWorkspace(ctx, raw, e.workspace)
				if err != nil {
					return err
				}
				repo, err := sc.Ext(distinctProjectionEntity.Kind)
				if err != nil {
					return err
				}
				_, err = repo.(store.DistinctProjector).ProjectDistinct(ctx, worst)
				return err
			})
			if !errors.Is(err, store.ErrInvalidProjection) {
				t.Fatalf("confined worst shape with a full common set = %v, want a refusal", err)
			}
			roomy := worst
			roomy.Filters = worst.Filters[:store.DistinctProjectionMaxFilters-1]
			if page := projectConfined(t, st, e, roomy); len(page.Values) != 0 || page.HasMore {
				t.Fatalf("confined worst shape returned %+v", page)
			}
		})
	})
}
