// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// BR1-W1 group behavior (ROOT-CONSTRUCTION-REVISION-1 and ROOT-RATIFICATION
// R4.1/R4.2). The qualified ceilings reach at most two payload groups on
// either engine, so these engine tests lower the reader's private ceiling to
// reach first, middle and final groups through the same factory interface.
// The real-ceiling reads are bounded_reader_wide_test.go.

// boundedSplitEntity puts NULL, empty and present TEXT and BYTES values in the
// first, a middle and the final group at boundedSplitColumns. Ordinals follow
// AllColumns: id, tenant_id, created_at, updated_at, version, deleted_at = 0-5.
var boundedSplitEntity = model.EntityDescriptor{
	Kind: "brt.split", Table: "brt_split", SoftDelete: true,
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden,
	},
	Fields: []model.FieldSpec{
		{Name: "t_first", Kind: model.KindText, Nullable: true},  // 6
		{Name: "b_first", Kind: model.KindBytes, Nullable: true}, // 7
		{Name: "workspace_id", Kind: model.KindUUID},             // 8
		{Name: "tag", Kind: model.KindText},                      // 9
		{Name: "t_mid", Kind: model.KindText, Nullable: true},    // 10
		{Name: "b_mid", Kind: model.KindBytes, Nullable: true},   // 11
		{Name: "f1", Kind: model.KindInt},                        // 12
		{Name: "f2", Kind: model.KindInt},                        // 13
		{Name: "f3", Kind: model.KindInt},                        // 14
		{Name: "f4", Kind: model.KindInt},                        // 15
		{Name: "t_last", Kind: model.KindText, Nullable: true},   // 16
		{Name: "b_last", Kind: model.KindBytes, Nullable: true},  // 17
	},
}

const (
	boundedSplitColumns = 12
	boundedSplitLong    = 64 // the long row's t_last octets
)

// boundedGroupSpan is one group: [start, end) and its inspection expression
// count or its payload nullable-column count.
type boundedGroupSpan struct {
	start, end int
	count      uint64
}

// The plans at boundedSplitColumns, derived by hand from revision 1 section 2
// rather than from the planner under test.
var (
	boundedSplitInspection = []boundedGroupSpan{{0, 6, 11}, {6, 12, 12}, {12, 18, 8}}
	boundedSplitPayload    = []boundedGroupSpan{{0, 8, 3}, {8, 16, 2}, {16, 18, 2}}
	boundedSplitOneInspect = []boundedGroupSpan{{0, 18, 31}}
	boundedSplitOnePayload = []boundedGroupSpan{{0, 18, 7}}
)

var errBoundedGroupRollback = errors.New("bounded group test rollback")

func registerBoundedGroupEntities(reg store.ExtensionRegistry) error {
	if err := registerBoundedTestEntities(reg); err != nil {
		return err
	}
	return reg.Register(boundedSplitEntity)
}

func boundedSpans(arity int, next func(int) (int, uint64)) []boundedGroupSpan {
	var spans []boundedGroupSpan
	for start := 0; start < arity; {
		end, n := next(start)
		spans = append(spans, boundedGroupSpan{start, end, n})
		if end == start {
			break
		}
		start = end
	}
	return spans
}

func TestBoundedReaderGroupPlans(t *testing.T) {
	check := func(name string, desc model.EntityDescriptor, columns uint64, wantInspect, wantPayload []boundedGroupSpan) {
		t.Helper()
		r := &boundedReader{columns: columns}
		arity := len(desc.AllColumns())
		inspect := boundedSpans(arity, func(s int) (int, uint64) { return r.nextInspectionGroup(desc, arity, s) })
		payload := boundedSpans(arity, func(s int) (int, uint64) { return r.nextPayloadGroup(desc, arity, s) })
		if !reflect.DeepEqual(inspect, wantInspect) || !reflect.DeepEqual(payload, wantPayload) {
			t.Errorf("%s: inspection %v payload %v, want %v and %v", name, inspect, payload, wantInspect, wantPayload)
		}
	}
	check("split", boundedSplitEntity, boundedSplitColumns, boundedSplitInspection, boundedSplitPayload)
	// Without soft delete the base is five columns: ordinals and both plans move.
	hard := boundedSplitEntity
	hard.SoftDelete = false
	check("split without soft delete", hard, boundedSplitColumns,
		[]boundedGroupSpan{{0, 6, 11}, {6, 13, 12}, {13, 17, 6}},
		[]boundedGroupSpan{{0, 9, 2}, {9, 16, 3}, {16, 17, 1}})
	check("split at the SQLite ceiling", boundedSplitEntity, boundedSQLiteResultColumns, boundedSplitOneInspect, boundedSplitOnePayload)

	plan, err := (&boundedReader{columns: boundedSplitColumns}).plan(boundedSplitEntity)
	if err != nil || plan != (boundedPlan{arity: 18, inspectionGroups: 3, inspectionExprs: 31, payloadGroups: 3}) {
		t.Errorf("split plan %+v err=%v", plan, err)
	}
	if units, ok := plan.metadataUnits(); !ok || units != 16*3+17*31 {
		t.Errorf("split metadata units %d ok=%v", units, ok)
	}
	if _, err := (&boundedReader{columns: 2}).plan(boundedSplitEntity); !errors.Is(err, store.ErrBoundedReadUnavailable) {
		t.Errorf("a ceiling below one column's metadata: %v", err)
	}

	// A class per column, a length per variable kind, and a range flag per
	// SQLite boolean; PostgreSQL booleans are native.
	for _, c := range []struct {
		kind model.SQLKind
		pg   bool
		want uint64
	}{
		{model.KindBool, false, 2}, {model.KindBool, true, 1}, {model.KindInt, false, 1}, {model.KindFloat, true, 1},
		{model.KindText, false, 2}, {model.KindBytes, true, 2}, {model.KindTimestamp, true, 2}, {model.KindUUID, false, 2},
	} {
		if got := boundedInspectionExprs(c.kind, c.pg); got != c.want {
			t.Errorf("inspection expressions kind %v pg=%v = %d, want %d", c.kind, c.pg, got, c.want)
		}
	}

	// The qualified SQLite ceiling on the real-width regression descriptors.
	sqlite := &boundedReader{columns: boundedSQLiteResultColumns}
	for _, c := range []struct {
		desc             model.EntityDescriptor
		inspect, payload uint64
	}{{boundedWideText, 1, 1}, {boundedWideIntNull, 1, 2}, {boundedSQLiteMax, 2, 2}} {
		p, err := sqlite.plan(c.desc)
		if err != nil || p.inspectionGroups != c.inspect || p.payloadGroups != c.payload {
			t.Errorf("%s SQLite plan %+v err=%v", c.desc.Kind, p, err)
		}
	}

	// Property over deterministic descriptors: every group fits, is maximal,
	// keeps a column's metadata together and partitions the ordinals once.
	rng := rand.New(rand.NewPCG(97, 1))
	kinds := []model.SQLKind{model.KindText, model.KindBytes, model.KindInt, model.KindFloat, model.KindBool,
		model.KindJSON, model.KindTimestamp, model.KindUUID}
	for trial := 0; trial < 300; trial++ {
		desc := boundedWideDescriptor("brp.x", "brp_x", rng.IntN(2) == 0, rng.IntN(2200),
			func(int) (model.SQLKind, bool) { return kinds[rng.IntN(len(kinds))], rng.IntN(2) == 0 })
		columns := []uint64{3, 4, 5, 9, 64, 1664, 2000}[rng.IntN(7)]
		r := &boundedReader{columns: columns}
		all := desc.AllColumns()
		arity := len(all)
		for i, col := range all {
			gotCol, gotKind, gotNullable := boundedColumnAt(desc, i)
			wantKind, _ := desc.KindOfColumn(col)
			if gotCol != col || gotKind != wantKind || gotNullable != desc.NullableColumn(col) {
				t.Fatalf("trial %d ordinal %d: %s/%v/%v, want %s", trial, i, gotCol, gotKind, gotNullable, col)
			}
		}
		exprsOf := func(i int) uint64 { _, k, _ := boundedColumnAt(desc, i); return boundedInspectionExprs(k, false) }
		plan, err := r.plan(desc)
		if err != nil {
			t.Fatalf("trial %d plan: %v", trial, err)
		}
		pos, sum := 0, uint64(0)
		inspect := boundedSpans(arity, func(s int) (int, uint64) { return r.nextInspectionGroup(desc, arity, s) })
		for _, s := range inspect {
			want := uint64(0)
			for i := s.start; i < s.end; i++ {
				want += exprsOf(i)
			}
			if s.start != pos || s.end <= s.start || s.count != want || want > columns ||
				(s.end < arity && want+exprsOf(s.end) <= columns) {
				t.Fatalf("trial %d C=%d inspection span %+v (expressions %d)", trial, columns, s, want)
			}
			pos, sum = s.end, sum+want
		}
		if pos != arity || plan.inspectionGroups != uint64(len(inspect)) || plan.inspectionExprs != sum {
			t.Fatalf("trial %d inspection partition ends %d of %d, plan %+v", trial, pos, arity, plan)
		}
		pos = 0
		payload := boundedSpans(arity, func(s int) (int, uint64) { return r.nextPayloadGroup(desc, arity, s) })
		for _, s := range payload {
			g, v := uint64(s.end-s.start), uint64(0)
			for i := s.start; i < s.end; i++ {
				if _, _, nullable := boundedColumnAt(desc, i); nullable {
					v++
				}
			}
			fits := func(g, v uint64) bool { return g+1 <= columns && g+1+v <= columns }
			nextV := v
			if s.end < arity {
				if _, _, nullable := boundedColumnAt(desc, s.end); nullable {
					nextV++
				}
			}
			if s.start != pos || g == 0 || s.count != v || !fits(g, v) || (s.end < arity && fits(g+1, nextV)) {
				t.Fatalf("trial %d C=%d payload span %+v (g=%d v=%d)", trial, columns, s, g, v)
			}
			pos = s.end
		}
		if pos != arity || plan.payloadGroups != uint64(len(payload)) {
			t.Fatalf("trial %d payload partition ends %d of %d, plan %+v", trial, pos, arity, plan)
		}
	}
}

func TestBoundedReaderGroupAdmissionArithmeticIsChecked(t *testing.T) {
	unlimited := store.BoundedReadLimits{
		MaxCellBytes: math.MaxUint64, MaxRowUnits: math.MaxUint64, MaxPageUnits: math.MaxUint64,
		MaxRowsPerPage: math.MaxUint64, MaxRows: math.MaxUint64, MaxUnits: math.MaxUint64,
		MaxFilters: math.MaxUint64, MaxParameterBytes: math.MaxUint64, MaxQueryBytes: math.MaxUint64,
	}
	overflow := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "overflows") {
			t.Errorf("%s overflow accepted: %v", name, err)
		}
	}
	c := &boundedCall{r: &boundedReader{lim: unlimited}, pageUnits: 1}
	overflow("page envelope sum", c.admitPage(math.MaxUint64, 1))
	overflow("metadata page sum", c.admitMetadata(math.MaxUint64))
	c = &boundedCall{r: &boundedReader{lim: unlimited, reserved: math.MaxUint64}}
	overflow("traversal envelope sum", c.admitPage(1, 0))
	overflow("traversal metadata sum", c.admitMetadata(1))
	c = &boundedCall{r: &boundedReader{lim: unlimited, rowsRes: math.MaxUint64}}
	if err := c.admitPage(0, 1); !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "MaxRows") {
		t.Errorf("logical row sum overflow: %v", err)
	}

	// Boundaries are calculations: equality passes, one more fails, nothing is charged.
	limits := unlimited
	limits.MaxPageUnits, limits.MaxUnits, limits.MaxRows = 100, 1000, 2
	r := &boundedReader{lim: limits, reserved: 900}
	c = &boundedCall{r: r, pageUnits: 40}
	for _, check := range []struct {
		name, dimension string
		err             error
	}{
		{"page equality", "", c.admitPage(60, 2)},
		{"page one over", "MaxPageUnits", c.admitPage(61, 2)},
		{"logical rows one over", "MaxRows", c.admitPage(60, 3)},
		{"metadata equality", "", c.admitMetadata(60)},
		{"metadata one over", "MaxPageUnits", c.admitMetadata(61)},
	} {
		if (check.dimension == "") != (check.err == nil) ||
			(check.err != nil && (!errors.Is(check.err, store.ErrBoundedReadLimit) || !strings.Contains(check.err.Error(), check.dimension))) {
			t.Errorf("%s: %v", check.name, check.err)
		}
	}
	r.reserved = 941
	c = &boundedCall{r: r}
	if err := c.admitPage(60, 1); !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "MaxUnits") {
		t.Errorf("traversal one over: %v", err)
	}
	if err := c.admitMetadata(59); err != nil {
		t.Errorf("traversal equality: %v", err)
	}
	if r.reserved != 941 || r.rowsRes != 0 || c.pageUnits != 0 {
		t.Errorf("an admission calculation charged units: reserved=%d rows=%d page=%d", r.reserved, r.rowsRes, c.pageUnits)
	}

	for _, p := range []boundedPlan{
		{inspectionGroups: math.MaxUint64},
		{inspectionGroups: 1, inspectionExprs: math.MaxUint64/17 + 1},
		{inspectionGroups: 1, inspectionExprs: math.MaxUint64 / 17}, // 17 * exprs is exactly MaxUint64; + 16 overflows
	} {
		if _, ok := p.metadataUnits(); ok {
			t.Errorf("metadata units overflow accepted for %+v", p)
		}
	}
	for _, a := range []boundedAdmission{{charge: math.MaxUint64, nullable: true}, {charge: math.MaxUint64 - 20}} {
		p := boundedPayload{row: boundedRow{admissions: []boundedAdmission{a}}}
		if _, ok := p.groupUnits(0, 1); ok {
			t.Errorf("group units overflow accepted for %+v", a)
		}
	}

	// The balanced guard: 3999 conjunctions over 4000 leaves nest at most
	// ceil(log2(4000)) = 12 levels, and counting matches building.
	admissions := make([]boundedAdmission, 4000)
	for i := range admissions {
		admissions[i] = boundedAdmission{column: "c", kind: model.KindInt, class: sqliteClassInteger}
	}
	var count boundedSQL
	writeRowOK(&count, admissions, false)
	var built strings.Builder
	writeRowOK(&boundedSQL{b: &built}, admissions, false)
	depth, deepest := 0, 0
	for _, ch := range built.String() {
		switch ch {
		case '(':
			depth++
			deepest = max(deepest, depth)
		case ')':
			depth--
		}
	}
	// One more level is the leaf's own typeof(c).
	if uint64(built.Len()) != count.n || strings.Count(built.String(), " AND ") != 3999 || deepest > 13 || depth != 0 {
		t.Errorf("balanced guard: len %d counted %d, ANDs %d, depth %d", built.Len(), count.n,
			strings.Count(built.String(), " AND "), deepest)
	}
}

// boundedProbe wraps a reader's statement port on a real engine. It records
// every statement exactly as QueryContext receives it, never normalized, and
// lets a test act immediately before one: run SQL in the same transaction,
// inject a failure, cancel that statement's context before or after
// QueryContext, or replay the engine's own row through rows whose Close fails.
type boundedProbe struct {
	next       boundedQuerier
	counts     map[string]int
	sizes      map[string][]int // exact QueryContext statement bytes
	statements []boundedProbeStatement
	replays    []*sql.DB
	before     func(kind string, n int) boundedProbeAction
}

type boundedProbeStatement struct {
	kind string
	size int
}

type boundedProbeAction struct {
	fail         error
	cancelBefore bool
	cancelAfter  bool
	// closeFailure: the statement runs on the engine, its row is copied, and
	// the reader receives those values through rows whose Close returns this.
	closeFailure error
}

func boundedStatementKind(query string) string {
	switch {
	case strings.Contains(query, "pragma_encoding") || strings.Contains(query, "current_setting('server_encoding')"):
		return "representation"
	case strings.HasSuffix(query, " FOR UPDATE"):
		return "lock"
	case strings.Contains(query, " ORDER BY id ASC LIMIT "):
		return "keys"
	case strings.HasSuffix(query, ") AS b LIMIT 1"):
		return "payload"
	default:
		return "inspection"
	}
}

func (p *boundedProbe) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	kind := boundedStatementKind(query)
	p.counts[kind]++
	p.sizes[kind] = append(p.sizes[kind], len(query))
	p.statements = append(p.statements, boundedProbeStatement{kind: kind, size: len(query)})
	var act boundedProbeAction
	if p.before != nil {
		act = p.before(kind, p.counts[kind])
	}
	switch {
	case act.fail != nil:
		return nil, act.fail
	case act.closeFailure != nil:
		return p.replay(ctx, query, args, act.closeFailure)
	case !act.cancelBefore && !act.cancelAfter:
		return p.next.QueryContext(ctx, query, args...)
	}
	qctx, cancel := context.WithCancel(ctx)
	if act.cancelBefore {
		cancel()
		return p.next.QueryContext(qctx, query, args...)
	}
	rows, err := p.next.QueryContext(qctx, query, args...)
	cancel()
	if err == nil {
		// database/sql records the context error on the rows asynchronously;
		// wait for that state so Next and close observe it deterministically.
		deadline := time.Now().Add(10 * time.Second)
		for rows.Err() == nil && time.Now().Before(deadline) {
			runtime.Gosched()
		}
	}
	return rows, err
}

// replay runs the statement on the engine through the real port, copies its
// row, and returns the same columns and values from a one-result test driver
// whose Rows.Close fails. The reader's Scan therefore succeeds on the engine's
// own values and only its close reports the distinct failure.
func (p *boundedProbe) replay(ctx context.Context, query string, args []any, closeFailure error) (*sql.Rows, error) {
	engineRows, err := p.next.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	columns, err := engineRows.Columns()
	connector := &boundedReplayConnector{columns: columns, closeErr: closeFailure}
	if err == nil && engineRows.Next() {
		values := make([]any, len(columns))
		dests := make([]any, len(columns))
		for i := range values {
			dests[i] = &values[i]
		}
		if err = engineRows.Scan(dests...); err == nil {
			connector.row = make([]driver.Value, len(values))
			for i, v := range values {
				connector.row[i] = v
			}
		}
	}
	if err = errors.Join(err, engineRows.Err(), engineRows.Close()); err != nil {
		return nil, err
	}
	if connector.row == nil {
		return nil, errors.New("replay: the engine returned no row")
	}
	db := sql.OpenDB(connector)
	p.replays = append(p.replays, db)
	return db.QueryContext(ctx, "replay")
}

func (p *boundedProbe) closeReplays() {
	for _, db := range p.replays {
		_ = db.Close()
	}
}

// boundedReplayConnector is a one-result test driver: its rows return the
// copied engine row once, and their Close returns closeErr.
type boundedReplayConnector struct {
	columns  []string
	row      []driver.Value
	closeErr error
}

func (c *boundedReplayConnector) Connect(context.Context) (driver.Conn, error) {
	return boundedReplayConn{c: c}, nil
}

func (c *boundedReplayConnector) Driver() driver.Driver { return boundedReplayDriver{c: c} }

type boundedReplayDriver struct{ c *boundedReplayConnector }

func (d boundedReplayDriver) Open(string) (driver.Conn, error) { return boundedReplayConn{c: d.c}, nil }

type boundedReplayConn struct{ c *boundedReplayConnector }

func (boundedReplayConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("replay driver prepares no statements")
}

func (boundedReplayConn) Close() error { return nil }

func (boundedReplayConn) Begin() (driver.Tx, error) {
	return nil, errors.New("replay driver has no transactions")
}

func (c boundedReplayConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &boundedReplayRows{c: c.c}, nil
}

type boundedReplayRows struct {
	c    *boundedReplayConnector
	done bool
}

func (r *boundedReplayRows) Columns() []string { return r.c.columns }

func (r *boundedReplayRows) Close() error { return r.c.closeErr }

func (r *boundedReplayRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.c.row)
	return nil
}

// boundedGroupReader creates a reader through the factory, optionally lowers
// its private ceiling, and installs a probe on its statement port.
func boundedGroupReader(t *testing.T, sc store.Scope, limits store.BoundedReadLimits, columns uint64) (store.BoundedReader, *boundedProbe) {
	t.Helper()
	reader := newBoundedTestReader(t, sc, limits)
	br := reader.(*boundedReader)
	if columns != 0 {
		br.columns = columns
	}
	probe := &boundedProbe{next: br.q, counts: map[string]int{}, sizes: map[string][]int{}}
	br.q = probe
	return reader, probe
}

func boundedExecIn(ctx context.Context, sc store.Scope, query string, args ...any) error {
	ts := sc.(*tenantScope)
	_, err := ts.tx.ExecContext(ctx, ts.s.dia.Rebind(query), args...)
	return err
}

type boundedGroupEngine struct {
	pg       bool
	bytesSQL func(hex string) string
	rep      uint64 // representation statement: once per SQLite reader, per PostgreSQL method
	lock     uint64 // observed PostgreSQL Mutate row-lock statement, 0 on SQLite
}

var (
	boundedGroupSQLite   = boundedGroupEngine{bytesSQL: sqliteBytesLiteral, rep: 8 + 8 + 17}
	boundedGroupPostgres = boundedGroupEngine{pg: true, bytesSQL: postgresBytesLiteral, rep: 8 + 8 + 5*17,
		lock: 8 + 8 + 17 + 9 + 36}
)

type boundedSplitFixture struct {
	tenant                  model.TenantID
	ws, otherWS             model.ID
	null, empty, full, long model.ID // ascending, all in ws
	foreign                 model.ID // in otherWS
}

func seedBoundedSplit(t *testing.T, st store.Store, eng boundedGroupEngine) boundedSplitFixture {
	t.Helper()
	ctx := context.Background()
	var f boundedSplitFixture
	f.tenant = provisionTenant(t, st, "bounded-groups")
	f.ws, f.otherWS = distinctProjectionWorkspaces(t, st, f.tenant)
	if err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(boundedSplitEntity.Kind)
		if err != nil {
			return err
		}
		for _, target := range []*model.ID{&f.null, &f.empty, &f.full, &f.long, &f.foreign} {
			ws := f.ws
			if target == &f.foreign {
				ws = f.otherWS
			}
			rec, err := repo.Create(ctx, model.Record{
				"t_first": "a", "workspace_id": ws.String(), "tag": "keep", "t_mid": "a",
				"f1": int64(1), "f2": int64(-2), "f3": int64(3), "f4": int64(4), "t_last": "a",
			})
			if err != nil {
				return err
			}
			*target = model.ID(rec.String(model.ColID))
		}
		one, empty := eng.bytesSQL("01"), eng.bytesSQL("")
		for _, stmt := range []struct {
			query string
			args  []any
		}{
			{"UPDATE brt_split SET b_first = " + one + ", b_mid = " + one + ", b_last = " + one + " WHERE tenant_id = ?",
				[]any{f.tenant.String()}},
			{"UPDATE brt_split SET t_first = NULL, b_first = NULL, t_mid = NULL, b_mid = NULL, t_last = NULL, b_last = NULL WHERE id = ?",
				[]any{f.null.String()}},
			{"UPDATE brt_split SET t_first = '', b_first = " + empty + ", t_mid = '', b_mid = " + empty +
				", t_last = '', b_last = " + empty + " WHERE id = ?", []any{f.empty.String()}},
			{"UPDATE brt_split SET t_last = ? WHERE id = ?", []any{strings.Repeat("l", boundedSplitLong), f.long.String()}},
		} {
			if err := boundedExecIn(ctx, sc, stmt.query, stmt.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed split fixture: %v", err)
	}
	if ids := []string{f.null.String(), f.empty.String(), f.full.String(), f.long.String(), f.foreign.String()}; !sort.StringsAreSorted(ids) {
		t.Fatalf("fixture IDs are not ascending: %v", ids)
	}
	return f
}

func boundedExpectedCell(v any, kind model.SQLKind, pg bool) uint64 {
	switch {
	case v == nil:
		return boundedNullUnits
	case kind == model.KindBytes:
		return boundedVarUnits + uint64(len(v.([]byte)))
	case textLikeKind(kind):
		return boundedVarUnits + uint64(len(v.(string)))
	case kind == model.KindBool && pg:
		return boundedBoolUnits
	default:
		return boundedFixedUnits
	}
}

// boundedSplitEnvelopes is revision 1 section 4's arithmetic for one row:
// each payload statement envelope 8 + R_j, R_j = 8 + 17 + 17 v_j + cells.
func boundedSplitEnvelopes(rec model.Record, spans []boundedGroupSpan, pg bool) (rowUnits uint64, envelopes []uint64) {
	cols := boundedSplitEntity.AllColumns()
	for _, s := range spans {
		units := boundedRowUnits + boundedFixedUnits + boundedFixedUnits*s.count
		for _, col := range cols[s.start:s.end] {
			kind, _ := boundedSplitEntity.KindOfColumn(col)
			units += boundedExpectedCell(rec[col], kind, pg)
		}
		rowUnits += units
		envelopes = append(envelopes, boundedResultUnits+units)
	}
	return rowUnits, envelopes
}

func boundedSum(values ...uint64) uint64 {
	var sum uint64
	for _, v := range values {
		sum += v
	}
	return sum
}

// boundedMetadataExpected is the sum of 16 + 17 e_j over inspection groups.
func boundedMetadataExpected(spans []boundedGroupSpan) uint64 {
	var units uint64
	for _, s := range spans {
		units += boundedResultUnits + boundedRowUnits + boundedFixedUnits*s.count
	}
	return units
}

// runBoundedGroupContract is the engine-neutral multi-group discrimination.
func runBoundedGroupContract(t *testing.T, st store.Store, f boundedSplitFixture, eng boundedGroupEngine) {
	t.Helper()
	ctx := context.Background()
	kind := boundedSplitEntity.Kind
	inWS := []model.Filter{{Column: "workspace_id", Op: model.OpEq, Value: f.ws.String()}}
	view := func(t *testing.T, fn func(sc store.Scope)) {
		t.Helper()
		if err := st.View(ctx, f.tenant, func(sc store.Scope) error { fn(sc); return nil }); err != nil {
			t.Fatalf("view: %v", err)
		}
	}
	rollback := func(t *testing.T, fn func(sc store.Scope)) {
		t.Helper()
		if err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error { fn(sc); return errBoundedGroupRollback }); !errors.Is(err, errBoundedGroupRollback) {
			t.Fatalf("mutate: %v", err)
		}
	}
	// faulted runs a callback whose injected statement fault may end the
	// transaction; the finalization error is logged, it is not the oracle.
	faulted := func(t *testing.T, fn func(sc store.Scope)) {
		t.Helper()
		if err := st.View(ctx, f.tenant, func(sc store.Scope) error { fn(sc); return nil }); err != nil {
			t.Logf("view finalization after an injected fault: %v", err)
		}
	}
	fullRecord := func(t *testing.T, sc store.Scope) model.Record {
		t.Helper()
		rec, err := newBoundedTestReader(t, sc, boundedTestLimits()).GetExtension(ctx, kind, f.full)
		if err != nil {
			t.Fatalf("single-group read of the full row: %v", err)
		}
		return rec
	}
	metadata := boundedMetadataExpected(boundedSplitInspection)

	t.Run("plan and exact accounting", func(t *testing.T) {
		view(t, func(sc store.Scope) {
			reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
			plan, err := reader.(*boundedReader).plan(boundedSplitEntity)
			if err != nil || plan != (boundedPlan{arity: 18, inspectionGroups: 3, inspectionExprs: 31, payloadGroups: 3}) {
				t.Fatalf("plan %+v err=%v", plan, err)
			}
			rec, err := reader.GetExtension(ctx, kind, f.full)
			if err != nil {
				t.Fatalf("grouped Get: %v", err)
			}
			rowUnits, envelopes := boundedSplitEnvelopes(rec, boundedSplitPayload, eng.pg)
			want := eng.rep + metadata + boundedSum(envelopes...)
			u := reader.Usage()
			if u.ReservedUnits != want || u.ObservedUnits != want || u.PayloadRowsReserved != 1 ||
				u.PayloadRowsObserved != 1 || !u.ObservedComplete || u.Terminal {
				t.Errorf("grouped Get usage %+v, want reserved = observed = %d", u, want)
			}
			if probe.counts["inspection"] != 3 || probe.counts["payload"] != 3 {
				t.Errorf("grouped Get statements %v", probe.counts)
			}

			single := newBoundedTestReader(t, sc, boundedTestLimits())
			whole, err := single.GetExtension(ctx, kind, f.full)
			if err != nil || !reflect.DeepEqual(whole, rec) {
				t.Errorf("grouped Record differs from the single-group Record: err=%v", err)
			}
			singleRow, singleEnvelopes := boundedSplitEnvelopes(whole, boundedSplitOnePayload, eng.pg)
			wantSingle := eng.rep + boundedMetadataExpected(boundedSplitOneInspect) + boundedSum(singleEnvelopes...)
			if got := single.Usage().ReservedUnits; got != wantSingle {
				t.Errorf("single-group Get reserved %d, want %d", got, wantSingle)
			}
			// Grouping frames are charged, never erased: 16 per extra inspection
			// group, and 8 + 8 + 17 per extra payload group.
			if want-wantSingle != 2*16+2*33 || rowUnits-singleRow != 2*25 {
				t.Errorf("group framing: reserved +%d, row units +%d", want-wantSingle, rowUnits-singleRow)
			}

			limits := boundedTestLimits()
			limits.MaxRows = 4
			lister, lprobe := boundedGroupReader(t, sc, limits, boundedSplitColumns)
			recs, page, err := lister.ListExtensions(ctx, kind, model.Query{Limit: 4, Filters: inWS})
			if err != nil || len(recs) != 4 || page.HasMore {
				t.Fatalf("grouped List: rows=%d more=%v err=%v", len(recs), page.HasMore, err)
			}
			if lu := lister.Usage(); lu.PayloadRowsReserved != 4 || lu.PayloadRowsObserved != 4 || lu.RemainingRows != 0 ||
				lprobe.counts["payload"] != 12 || lprobe.counts["inspection"] != 12 {
				t.Errorf("MaxRows counts logical rows: usage %+v statements %v", lu, lprobe.counts)
			}
		})
	})

	t.Run("NULL empty and present values across first middle and final groups", func(t *testing.T) {
		view(t, func(sc store.Scope) {
			reader, _ := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
			var gets []model.Record
			for _, c := range []struct {
				id    model.ID
				text  any
				bytes []byte
			}{{f.null, nil, nil}, {f.empty, "", []byte{}}, {f.full, "a", []byte{1}}} {
				rec, err := reader.GetExtension(ctx, kind, c.id)
				if err != nil {
					t.Fatalf("Get %v: %v", c.text, err)
				}
				gets = append(gets, rec)
				for _, col := range []string{"t_first", "t_mid", "t_last"} {
					if !reflect.DeepEqual(rec[col], c.text) {
						t.Errorf("%s = %#v, want %#v", col, rec[col], c.text)
					}
				}
				for _, col := range []string{"b_first", "b_mid", "b_last"} {
					b, isBytes := rec[col].([]byte)
					if c.bytes == nil && rec[col] != nil || c.bytes != nil && (!isBytes || b == nil || !bytes.Equal(b, c.bytes)) {
						t.Errorf("%s = %#v, want %#v", col, rec[col], c.bytes)
					}
				}
			}
			lister, _ := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
			recs, _, err := lister.ListExtensions(ctx, kind, model.Query{Limit: 3, Filters: inWS})
			if err != nil || !reflect.DeepEqual(recs, gets) {
				t.Errorf("grouped List differs from grouped Get: err=%v", err)
			}
		})
	})

	t.Run("a final inspection group refusal blocks every payload", func(t *testing.T) {
		view(t, func(sc store.Scope) {
			limits := boundedTestLimits()
			limits.MaxCellBytes = 48
			reader, probe := boundedGroupReader(t, sc, limits, boundedSplitColumns)
			_, err := reader.GetExtension(ctx, kind, f.long)
			if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "MaxCellBytes") {
				t.Errorf("final-group oversize: %v", err)
			}
			if u := reader.Usage(); probe.counts["inspection"] != 3 || probe.counts["payload"] != 0 ||
				u.PayloadRowsReserved != 0 || !u.Terminal {
				t.Errorf("final-group oversize statements %v usage %+v", probe.counts, u)
			}
		})
		if eng.pg {
			return // PostgreSQL column types are fixed by the descriptor DDL
		}
		rollback(t, func(sc store.Scope) {
			if err := boundedExecIn(ctx, sc, "UPDATE brt_split SET f4 = 'x' WHERE id = ?", f.full.String()); err != nil {
				t.Fatalf("plant invalid class: %v", err)
			}
			reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
			_, err := reader.GetExtension(ctx, kind, f.full)
			if !errors.Is(err, store.ErrBoundedReadMetadata) {
				t.Errorf("final-group invalid class: %v", err)
			}
			if u := reader.Usage(); probe.counts["inspection"] != 3 || probe.counts["payload"] != 0 || u.PayloadRowsReserved != 0 {
				t.Errorf("final-group invalid class statements %v usage %+v", probe.counts, u)
			}
		})
	})

	t.Run("complete page admission precedes the first payload", func(t *testing.T) {
		q := model.Query{Limit: 2, Cursor: f.empty.String(), Filters: inWS} // full, then long
		view(t, func(sc store.Scope) {
			reference, probe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
			recs, _, err := reference.ListExtensions(ctx, kind, q)
			if err != nil || len(recs) != 2 || len(probe.sizes["payload"]) != 6 {
				t.Fatalf("reference List: rows=%d statements %v err=%v", len(recs), probe.counts, err)
			}
			total := reference.Usage().ReservedUnits
			fit := 0
			for statement, sizes := range probe.sizes {
				for i, n := range sizes {
					if statement != "payload" || i < 3 {
						fit = max(fit, n)
					}
				}
			}
			longest := 0
			for _, n := range probe.sizes["payload"][3:] {
				longest = max(longest, n)
			}
			if longest <= fit {
				t.Fatalf("the later row's payload is not the longest statement: %d <= %d", longest, fit)
			}
			refused := func(name string, limits store.BoundedReadLimits, run func(store.BoundedReader) error) {
				t.Helper()
				r, p := boundedGroupReader(t, sc, limits, boundedSplitColumns)
				err := run(r)
				if u := r.Usage(); !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), name) ||
					p.counts["payload"] != 0 || u.PayloadRowsReserved != 0 || !u.Terminal {
					t.Errorf("%s one below: err=%v statements %v usage %+v", name, err, p.counts, u)
				}
			}
			accepted := func(name string, limits store.BoundedReadLimits, run func(store.BoundedReader) error) {
				t.Helper()
				r, _ := boundedGroupReader(t, sc, limits, boundedSplitColumns)
				if err := run(r); err != nil {
					t.Errorf("%s at the exact bound: %v", name, err)
				}
			}
			list := func(r store.BoundedReader) error { _, _, err := r.ListExtensions(ctx, kind, q); return err }

			limits := boundedTestLimits()
			limits.MaxQueryBytes = uint64(fit)
			refused("MaxQueryBytes", limits, list)
			limits.MaxQueryBytes = uint64(longest)
			accepted("MaxQueryBytes", limits, list)
			for name, set := range map[string]func(*store.BoundedReadLimits, uint64){
				"MaxPageUnits": func(l *store.BoundedReadLimits, v uint64) { l.MaxPageUnits = v },
				"MaxUnits":     func(l *store.BoundedReadLimits, v uint64) { l.MaxUnits = v },
				"MaxRows":      func(l *store.BoundedReadLimits, v uint64) { l.MaxRows = v },
			} {
				bound := total
				if name == "MaxRows" {
					bound = 2 // six payload statements, two logical rows
				}
				limits := boundedTestLimits()
				set(&limits, bound-1)
				refused(name, limits, list)
				limits = boundedTestLimits()
				set(&limits, bound)
				accepted(name, limits, list)
			}
			rowUnits, _ := boundedSplitEnvelopes(recs[1], boundedSplitPayload, eng.pg)
			get := func(r store.BoundedReader) error { _, err := r.GetExtension(ctx, kind, f.long); return err }
			limits = boundedTestLimits()
			limits.MaxRowUnits = rowUnits - 1
			refused("MaxRowUnits", limits, get)
			limits.MaxRowUnits = rowUnits
			accepted("MaxRowUnits", limits, get)
		})
	})

	t.Run("MaxQueryBytes bounds the exact QueryContext statement of every kind", func(t *testing.T) {
		// Ten placeholders per statement (tenant, eight filters, then the cursor or
		// the ID) cross PostgreSQL's one-to-two-digit transition at $10.
		q := model.Query{Limit: 2, Cursor: f.empty.String(), Filters: []model.Filter{
			{Column: "workspace_id", Op: model.OpEq, Value: f.ws.String()},
			{Column: "tag", Op: model.OpEq, Value: "keep"},
			{Column: "t_first", Op: model.OpEq, Value: "a"},
			{Column: "t_mid", Op: model.OpEq, Value: "a"},
			{Column: "f1", Op: model.OpEq, Value: int64(1)},
			{Column: "f2", Op: model.OpEq, Value: int64(-2)},
			{Column: "f3", Op: model.OpEq, Value: int64(3)},
			{Column: "f4", Op: model.OpEq, Value: int64(4)},
		}}
		list := func(r store.BoundedReader) ([]model.Record, error) {
			recs, _, err := r.ListExtensions(ctx, kind, q)
			return recs, err
		}
		rollback(t, func(sc store.Scope) {
			reference, refProbe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
			refRecs, err := list(reference)
			if err != nil || len(refRecs) != 2 {
				t.Fatalf("reference List: rows=%d err=%v", len(refRecs), err)
			}
			_, fullEnvelopes := boundedSplitEnvelopes(refRecs[0], boundedSplitPayload, eng.pg)
			_, longEnvelopes := boundedSplitEnvelopes(refRecs[1], boundedSplitPayload, eng.pg)
			payloadUnits := boundedSum(fullEnvelopes...) + boundedSum(longEnvelopes...)
			refReserved := reference.Usage().ReservedUnits
			statements := refProbe.statements
			longest := map[string]int{}
			longestPayload, longestPayloadAt, payloads := 0, -1, 0
			for _, s := range statements {
				longest[s.kind] = max(longest[s.kind], s.size)
				if s.kind == "payload" {
					if s.size > longestPayload {
						longestPayload, longestPayloadAt = s.size, payloads
					}
					payloads++
				}
			}
			wantLocks := 0
			if eng.pg {
				wantLocks = 2
			}
			if payloads != 6 || longestPayloadAt < 3 || refProbe.counts["lock"] != wantLocks {
				t.Fatalf("reference plan: payloads=%d longest payload index %d statements %v", payloads, longestPayloadAt, refProbe.counts)
			}
			for statementKind, size := range longest {
				for _, limit := range []int{size, size - 1} {
					// Oracle from the reference order: statements are issued until the
					// first non-payload statement longer than the limit, and every
					// payload is admitted before the first one is issued.
					var issued []boundedProbeStatement
					payloadRefused := false
					for _, s := range statements {
						if s.kind == "payload" && longest["payload"] > limit {
							payloadRefused = true
							break
						}
						if s.kind != "payload" && s.size > limit {
							break
						}
						issued = append(issued, s)
					}
					limits := boundedTestLimits()
					limits.MaxQueryBytes = uint64(limit)
					r, p := boundedGroupReader(t, sc, limits, boundedSplitColumns)
					recs, err := list(r)
					u := r.Usage()
					if !reflect.DeepEqual(p.statements, issued) {
						t.Errorf("%s at MaxQueryBytes %d: QueryContext statements %v, want %v", statementKind, limit, p.statements, issued)
					}
					if len(issued) == len(statements) {
						if err != nil || !reflect.DeepEqual(recs, refRecs) {
							t.Errorf("%s at MaxQueryBytes %d: rows=%d err=%v", statementKind, limit, len(recs), err)
						}
						continue
					}
					if recs != nil || !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "MaxQueryBytes") ||
						!u.Terminal || u.PayloadRowsReserved != 0 {
						t.Errorf("%s one byte short at %d: rows=%d err=%v usage %+v", statementKind, limit, len(recs), err, u)
					}
					if payloadRefused && u.ReservedUnits != refReserved-payloadUnits {
						t.Errorf("payload preflight refusal reserved %d, want every issued charge %d", u.ReservedUnits, refReserved-payloadUnits)
					}
				}
			}
		})
	})

	t.Run("every group carries the complete predicate", func(t *testing.T) {
		// Same transaction, between two statements of one call. Each change is
		// same-length, so only a later statement's WHERE can exclude the row.
		for _, c := range []struct {
			name, statement, sql string
			at                   int
			args                 []any
			confine              bool
			read                 func(store.BoundedReader) error
		}{
			{"caller filter", "payload", "UPDATE brt_split SET tag = 'kept' WHERE id = ?", 2, []any{f.full.String()}, false,
				func(r store.BoundedReader) error {
					_, _, err := r.ListExtensions(ctx, kind, model.Query{Limit: 1, Cursor: f.empty.String(),
						Filters: []model.Filter{{Column: "tag", Op: model.OpEq, Value: "keep"}}})
					return err
				}},
			{"workspace lineage", "payload", "UPDATE brt_split SET workspace_id = ? WHERE id = ?", 3,
				[]any{f.otherWS.String(), f.full.String()}, true,
				func(r store.BoundedReader) error { _, err := r.GetExtension(ctx, kind, f.full); return err }},
			{"soft delete", "inspection", "UPDATE brt_split SET deleted_at = created_at WHERE id = ?", 2,
				[]any{f.full.String()}, false,
				func(r store.BoundedReader) error { _, err := r.GetExtension(ctx, kind, f.full); return err }},
		} {
			t.Run(c.name, func(t *testing.T) {
				rollback(t, func(sc store.Scope) {
					readScope := sc
					if c.confine {
						confined, err := store.ConfineWorkspace(ctx, sc, f.ws)
						if err != nil {
							t.Fatalf("confine: %v", err)
						}
						readScope = confined
					}
					reader, probe := boundedGroupReader(t, readScope, boundedTestLimits(), boundedSplitColumns)
					var hookErr error
					probe.before = func(statement string, n int) boundedProbeAction {
						if statement == c.statement && n == c.at {
							hookErr = boundedExecIn(ctx, sc, c.sql, c.args...)
						}
						return boundedProbeAction{}
					}
					err := c.read(reader)
					if hookErr != nil {
						t.Fatalf("same-transaction change: %v", hookErr)
					}
					u := reader.Usage()
					if !errors.Is(err, store.ErrBoundedReadConsistency) || probe.counts[c.statement] != c.at ||
						(c.statement == "inspection" && probe.counts["payload"] != 0) ||
						u.PayloadRowsObserved != 0 || !u.ObservedComplete || !u.Terminal {
						t.Errorf("err=%v statements %v usage %+v", err, probe.counts, u)
					}
				})
			})
		}
		view(t, func(sc store.Scope) {
			reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
			if _, err := reader.GetExtension(ctx, kind, model.NewID()); !errors.Is(err, store.ErrNotFound) || reader.Usage().Terminal ||
				probe.counts["inspection"] != 1 || probe.counts["payload"] != 0 {
				t.Errorf("first-group NotFound: err=%v statements %v", err, probe.counts)
			}
		})
	})

	t.Run("early and final group rejects", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			at       int
			sql      string
			observed uint64
		}{
			// The full-row guard of group 1 covers t_last, projected by group 3.
			{"early", 1, "UPDATE brt_split SET t_last = 'aa' WHERE id = ?", 0},
			// Group 3's guard covers t_first, already projected by group 1.
			{"final", 3, "UPDATE brt_split SET t_first = 'aa' WHERE id = ?", 1},
		} {
			t.Run(c.name, func(t *testing.T) {
				rollback(t, func(sc store.Scope) {
					full := fullRecord(t, sc)
					_, envelopes := boundedSplitEnvelopes(full, boundedSplitPayload, eng.pg)
					reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
					var hookErr error
					probe.before = func(statement string, n int) boundedProbeAction {
						if statement == "payload" && n == c.at {
							hookErr = boundedExecIn(ctx, sc, c.sql, f.full.String())
						}
						return boundedProbeAction{}
					}
					rec, err := reader.GetExtension(ctx, kind, f.full)
					if hookErr != nil {
						t.Fatalf("same-transaction change: %v", hookErr)
					}
					if rec != nil || !errors.Is(err, store.ErrBoundedReadConsistency) || !strings.Contains(err.Error(), "no longer matches") {
						t.Errorf("reject: rec=%v err=%v", rec != nil, err)
					}
					// A rejected group delivers only its frame, flags and NULL values.
					rejected := boundedResultUnits + boundedRowUnits + boundedFixedUnits +
						boundedFixedUnits*boundedSplitPayload[c.at-1].count +
						boundedNullUnits*uint64(boundedSplitPayload[c.at-1].end-boundedSplitPayload[c.at-1].start)
					wantObserved := eng.rep + eng.lock + metadata + boundedSum(envelopes[:c.at-1]...) + rejected
					// The lock envelope, 8 + 8 + 17 + 9 + 36, equals its complete observation.
					wantReserved := eng.rep + eng.lock + metadata + boundedSum(envelopes[:c.at]...)
					u := reader.Usage()
					if probe.counts["payload"] != c.at || u.PayloadRowsReserved != 1 || u.PayloadRowsObserved != c.observed ||
						!u.ObservedComplete || !u.Terminal || u.ObservedUnits != wantObserved || u.ReservedUnits != wantReserved {
						t.Errorf("statements %v usage %+v, want observed %d reserved %d", probe.counts, u, wantObserved, wantReserved)
					}
				})
			})
		}
	})

	t.Run("later group failures retain every issued charge", func(t *testing.T) {
		injected := errors.New("injected statement failure")
		closeFailure := errors.New("injected rows close failure after a successful scan")
		lastInspection := boundedResultUnits + boundedRowUnits + boundedFixedUnits*boundedSplitInspection[2].count
		for _, c := range []struct {
			name      string
			statement string
			at        int
			action    boundedProbeAction
			cause     error
			rows      uint64
			reserved  func(envelopes []uint64) uint64
			observed  func(envelopes []uint64) uint64 // exact known physical lower bound
		}{
			{"cancel before payload group 2", "payload", 2, boundedProbeAction{cancelBefore: true}, context.Canceled, 1,
				func(e []uint64) uint64 { return metadata + e[0] + e[1] },
				func(e []uint64) uint64 { return metadata + e[0] }},
			// Context cancellation with rows.Err already set: the result frame only.
			{"cancel after payload group 3 was issued", "payload", 3, boundedProbeAction{cancelAfter: true}, context.Canceled, 1,
				func(e []uint64) uint64 { return metadata + boundedSum(e...) },
				func(e []uint64) uint64 { return metadata + e[0] + e[1] + boundedResultUnits }},
			// Scan succeeds and only Rows.Close fails: the scanned values are not
			// counted as observed, and the logical row is not observed (R4.1).
			{"rows close failure after a successful scan of payload group 2", "payload", 2,
				boundedProbeAction{closeFailure: closeFailure}, closeFailure, 1,
				func(e []uint64) uint64 { return metadata + e[0] + e[1] },
				func(e []uint64) uint64 { return metadata + e[0] + boundedResultUnits }},
			{"rows close failure after a successful scan of the final payload group", "payload", 3,
				boundedProbeAction{closeFailure: closeFailure}, closeFailure, 1,
				func(e []uint64) uint64 { return metadata + boundedSum(e...) },
				func(e []uint64) uint64 { return metadata + e[0] + e[1] + boundedResultUnits }},
			{"driver failure in inspection group 3", "inspection", 3, boundedProbeAction{fail: injected}, injected, 0,
				func([]uint64) uint64 { return metadata },
				func([]uint64) uint64 { return metadata - lastInspection }},
		} {
			t.Run(c.name, func(t *testing.T) {
				faulted(t, func(sc store.Scope) {
					_, envelopes := boundedSplitEnvelopes(fullRecord(t, sc), boundedSplitPayload, eng.pg)
					reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
					defer probe.closeReplays()
					probe.before = func(statement string, n int) boundedProbeAction {
						if statement == c.statement && n == c.at {
							return c.action
						}
						return boundedProbeAction{}
					}
					rec, err := reader.GetExtension(ctx, kind, f.full)
					u := reader.Usage()
					wantPayloads := c.at
					if c.statement == "inspection" {
						wantPayloads = 0
					}
					wantReserved, wantObserved := eng.rep+c.reserved(envelopes), eng.rep+c.observed(envelopes)
					if rec != nil || !errors.Is(err, c.cause) || probe.counts[c.statement] != c.at ||
						probe.counts["payload"] != wantPayloads || u.ReservedUnits != wantReserved ||
						u.ObservedUnits != wantObserved || u.PayloadRowsReserved != c.rows ||
						u.PayloadRowsObserved != 0 || u.ObservedComplete || !u.Terminal {
						t.Errorf("rec=%v err=%v statements %v usage %+v, want reserved %d observed %d",
							rec != nil, err, probe.counts, u, wantReserved, wantObserved)
					}
					if _, err := reader.GetExtension(ctx, kind, f.full); !errors.Is(err, store.ErrBoundedReadTerminal) || !errors.Is(err, c.cause) {
						t.Errorf("terminal follow-up: %v", err)
					}
					if after := reader.Usage(); after != u {
						t.Errorf("terminal follow-up changed usage: %+v", after)
					}
				})
			})
		}
		t.Run("driver failure in the later row keeps no earlier row", func(t *testing.T) {
			faulted(t, func(sc store.Scope) {
				whole := newBoundedTestReader(t, sc, boundedTestLimits())
				full, fullErr := whole.GetExtension(ctx, kind, f.full)
				long, longErr := whole.GetExtension(ctx, kind, f.long)
				if fullErr != nil || longErr != nil {
					t.Fatalf("reference reads: %v %v", fullErr, longErr)
				}
				_, fullEnvelopes := boundedSplitEnvelopes(full, boundedSplitPayload, eng.pg)
				_, longEnvelopes := boundedSplitEnvelopes(long, boundedSplitPayload, eng.pg)
				reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
				probe.before = func(statement string, n int) boundedProbeAction {
					if statement == "payload" && n == 5 {
						return boundedProbeAction{fail: injected}
					}
					return boundedProbeAction{}
				}
				recs, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 2, Cursor: f.empty.String(), Filters: inWS})
				// Key selection reserves 8 + 3 x (8 + 9 + 36 + 17) and observes two keys.
				keyRow := boundedRowUnits + boundedVarUnits + 36 + boundedFixedUnits
				wantReserved := eng.rep + boundedResultUnits + 3*keyRow + 2*metadata + boundedSum(fullEnvelopes...) +
					longEnvelopes[0] + longEnvelopes[1]
				wantObserved := eng.rep + boundedResultUnits + 2*keyRow + 2*metadata + boundedSum(fullEnvelopes...) +
					longEnvelopes[0]
				if u := reader.Usage(); recs != nil || !errors.Is(err, injected) || probe.counts["payload"] != 5 ||
					u.PayloadRowsReserved != 2 || u.PayloadRowsObserved != 1 || u.ObservedComplete || !u.Terminal ||
					u.ReservedUnits != wantReserved || u.ObservedUnits != wantObserved {
					t.Errorf("rows=%d err=%v statements %v usage %+v, want reserved %d observed %d",
						len(recs), err, probe.counts, u, wantReserved, wantObserved)
				}
			})
		})
		t.Run("a concurrent call between groups is refused without disturbing the active call", func(t *testing.T) {
			view(t, func(sc store.Scope) {
				reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
				var concurrent error
				var during store.BoundedReadUsage
				probe.before = func(statement string, n int) boundedProbeAction {
					if statement == "payload" && n == 2 {
						during = reader.Usage()
						done := make(chan error, 1)
						go func() { _, err := reader.GetExtension(ctx, kind, f.full); done <- err }()
						concurrent = <-done
					}
					return boundedProbeAction{}
				}
				if _, err := reader.GetExtension(ctx, kind, f.full); err != nil {
					t.Fatalf("active call: %v", err)
				}
				if !errors.Is(concurrent, store.ErrBoundedReadConcurrent) || during.Terminal {
					t.Errorf("concurrent call: %v", concurrent)
				}
				if u := reader.Usage(); u.PayloadRowsObserved != 1 || u.Terminal || probe.counts["payload"] != 3 {
					t.Errorf("active call disturbed: %+v %v", u, probe.counts)
				}
			})
		})
	})

	t.Run("confinement refusals precede every group", func(t *testing.T) {
		view(t, func(sc store.Scope) {
			confined, err := store.ConfineWorkspace(ctx, sc, f.ws)
			if err != nil {
				t.Fatalf("confine: %v", err)
			}
			reader, probe := boundedGroupReader(t, confined, boundedTestLimits(), boundedSplitColumns)
			if _, err := reader.GetPolicySnapshot(ctx, model.NewID()); !errors.Is(err, store.ErrWorkspaceLineageRequired) || len(probe.counts) != 0 {
				t.Errorf("confined policy: err=%v statements %v", err, probe.counts)
			}
			own, err := reader.GetExtension(ctx, kind, f.full)
			if err != nil || !reflect.DeepEqual(own, fullRecord(t, sc)) {
				t.Errorf("confined own row: err=%v", err)
			}
			if _, err := reader.GetExtension(ctx, kind, f.foreign); !errors.Is(err, store.ErrNotFound) || reader.Usage().Terminal ||
				probe.counts["inspection"] != 4 || probe.counts["payload"] != 3 {
				t.Errorf("confined foreign row: err=%v statements %v", err, probe.counts)
			}
		})
	})
}

func TestBoundedReaderGroupsSQLite(t *testing.T) {
	st := openSQLiteTest(t, registerBoundedGroupEntities)
	runBoundedGroupContract(t, st, seedBoundedSplit(t, st, boundedGroupSQLite), boundedGroupSQLite)
}

func openBoundedGroupPG(t *testing.T, pg pgtest.DSNs, mode pgExecModeFact) store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sep := "?"
	if strings.Contains(pg.App, "?") {
		sep = "&"
	}
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App + sep + "default_query_exec_mode=" + mode.String(),
		OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 4,
	}, registerBoundedGroupEntities)
	if err != nil {
		t.Fatalf("open postgres store (%s): %v", mode, err)
	}
	if got := st.(*sqlStore).pgExecMode; got != mode {
		t.Fatalf("store recorded mode %s, want %s", got, mode)
	}
	return st
}

func TestBoundedReaderGroupsPostgres(t *testing.T) {
	pg := isolatedPGSplit(t)
	seed := openBoundedGroupPG(t, pg, pgExecModeCacheStatement)
	f := seedBoundedSplit(t, seed, boundedGroupPostgres)
	_ = seed.Close()
	for _, mode := range []pgExecModeFact{pgExecModeCacheStatement, pgExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			st := openBoundedGroupPG(t, pg, mode)
			defer func() { _ = st.Close() }()
			runBoundedGroupContract(t, st, f, boundedGroupPostgres)
			// The qualified PostgreSQL ceiling on the widest regression descriptors.
			if err := st.View(context.Background(), f.tenant, func(sc store.Scope) error {
				r := newBoundedTestReader(t, sc, boundedTestLimits()).(*boundedReader)
				for _, c := range []struct {
					desc             model.EntityDescriptor
					inspect, payload uint64
				}{{boundedPGText, 2, 1}, {boundedPGIntNull, 1, 2}} {
					if p, err := r.plan(c.desc); err != nil || p.inspectionGroups != c.inspect || p.payloadGroups != c.payload {
						t.Errorf("%s PostgreSQL plan %+v err=%v", c.desc.Kind, p, err)
					}
				}
				return nil
			}); err != nil {
				t.Fatalf("view: %v", err)
			}
		})
	}
}

// TestBoundedReaderGroupsPostgresRowLocks proves both interleavings of
// revision 1 section 6 on PostgreSQL 16 READ COMMITTED: an earlier writer's
// committed change is seen by a later group after the reader's lock wait, and
// a later writer stays blocked while the reader holds the row lock across
// groups, so no group observes an inter-group replacement.
func TestBoundedReaderGroupsPostgresRowLocks(t *testing.T) {
	pg := isolatedPGSplit(t)
	seedStore := openBoundedGroupPG(t, pg, pgExecModeCacheStatement)
	item := seedBoundedPG(t, seedStore)
	split := seedBoundedSplit(t, seedStore, boundedGroupPostgres)
	_ = seedStore.Close()
	monitor, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	defer func() { _ = monitor.Close() }()
	lockWaiters := func(ctx context.Context, like string) (bool, error) {
		var waiting int
		err := monitor.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_stat_activity "+
			"WHERE datname = pg_catalog.current_database() AND wait_event_type = 'Lock' "+
			"AND wait_event IN ('transactionid', 'tuple') AND query LIKE $1", like).Scan(&waiting)
		return waiting > 0, err
	}

	for _, mode := range []pgExecModeFact{pgExecModeCacheStatement, pgExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			st := openBoundedGroupPG(t, pg, mode)
			defer func() { _ = st.Close() }()

			t.Run("wait: a later group reads the post-wait state", func(t *testing.T) {
				kind := boundedTestEntity.Kind
				setLabel := func(label string) {
					t.Helper()
					if _, err := monitor.ExecContext(ctx, "UPDATE public.brt_item SET label = $1 WHERE id = $2", label, item.row.String()); err != nil {
						t.Fatalf("set label: %v", err)
					}
				}
				lr := boundedLockRace{monitor: monitor, st: st, tenant: item.tenant, row: item.row, budget: 60 * time.Second}
				observe := func(qctx context.Context) (bool, error) { return lockWaiters(qctx, "%FOR UPDATE") }
				// brt_item at ceiling 4: label is ordinal 6, in inspection group 4 of 6.
				grouped := func(r store.BoundedReader, read func() error) error {
					br := r.(*boundedReader)
					br.columns = 4
					if p, err := br.plan(boundedTestEntity); err != nil || p.inspectionGroups != 6 || p.payloadGroups != 6 {
						return fmt.Errorf("unexpected plan %+v: %v", p, err)
					}
					probe := &boundedProbe{next: br.q, counts: map[string]int{}, sizes: map[string][]int{}}
					br.q = probe
					err := read()
					return fmt.Errorf("%w (inspection statements %d, payload statements %d)",
						err, probe.counts["inspection"], probe.counts["payload"])
				}

				setLabel("match")
				readErr, err := lr.run(ctx, strings.Repeat("g", 2000), observe, func(rctx context.Context, r store.BoundedReader) error {
					return grouped(r, func() error { _, err := r.GetExtension(rctx, kind, item.row); return err })
				})
				if err != nil {
					t.Fatalf("growth race: %v", err)
				}
				if !errors.Is(readErr, store.ErrBoundedReadLimit) || !strings.Contains(readErr.Error(), "MaxCellBytes") ||
					!strings.Contains(readErr.Error(), "(inspection statements 4, payload statements 0)") {
					t.Errorf("growth while waiting: %v", readErr)
				}

				setLabel("match")
				readErr, err = lr.run(ctx, "moved", observe, func(rctx context.Context, r store.BoundedReader) error {
					return grouped(r, func() error {
						_, _, err := r.ListExtensions(rctx, kind, model.Query{Limit: 10, Filters: []model.Filter{{
							Column: "label", Op: model.OpEq, Value: "match",
						}}})
						return err
					})
				})
				if err != nil {
					t.Fatalf("filter race: %v", err)
				}
				if !errors.Is(readErr, store.ErrBoundedReadConsistency) {
					t.Errorf("filter change while waiting: %v", readErr)
				}
				setLabel("match")
			})

			t.Run("hold: a later writer stays blocked across groups", func(t *testing.T) {
				kind := boundedSplitEntity.Kind
				writerCtx, cancelWriter := context.WithTimeout(ctx, time.Minute)
				defer cancelWriter()
				writerDone := make(chan error, 1)
				writerStarted, writerJoined := false, false
				joinWriter := func() error {
					timer := time.NewTimer(10 * time.Second)
					defer timer.Stop()
					select {
					case err := <-writerDone:
						writerJoined = true
						return err
					case <-timer.C:
						return errors.New("writer did not finish within its bounded join")
					}
				}
				// Teardown precedes every later return: cancel and join the writer.
				defer func() {
					if writerStarted && !writerJoined {
						cancelWriter()
						if err := joinWriter(); err != nil && !errors.Is(err, context.Canceled) {
							t.Logf("writer teardown: %v", err)
						}
					}
				}()

				var rec model.Record
				var readErr, hookErr error
				err := st.Mutate(ctx, split.tenant, func(sc store.Scope) error {
					reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), boundedSplitColumns)
					probe.before = func(statement string, n int) boundedProbeAction {
						if statement != "payload" || n != 3 {
							return boundedProbeAction{}
						}
						writerStarted = true
						go func() {
							res, err := monitor.ExecContext(writerCtx, "UPDATE public.brt_split SET t_last = 'z' WHERE id = $1", split.full.String())
							if err == nil {
								if affected, rowsErr := res.RowsAffected(); rowsErr != nil || affected != 1 {
									err = fmt.Errorf("writer updated %d rows: %v", affected, rowsErr)
								}
							}
							writerDone <- err
						}()
						deadline := time.NewTimer(20 * time.Second)
						defer deadline.Stop()
						for {
							qctx, qcancel := context.WithTimeout(ctx, 5*time.Second)
							waiting, err := lockWaiters(qctx, "UPDATE public.brt_split%")
							qcancel()
							if err != nil || waiting {
								hookErr = err
								return boundedProbeAction{}
							}
							select {
							case err := <-writerDone:
								writerJoined = true
								hookErr = fmt.Errorf("writer finished without waiting on the reader's row lock: %v", err)
								return boundedProbeAction{}
							case <-deadline.C:
								hookErr = errors.New("writer never waited on the reader's row lock within 20s")
								return boundedProbeAction{}
							case <-time.After(20 * time.Millisecond):
							}
						}
					}
					rec, readErr = reader.GetExtension(ctx, kind, split.full)
					select {
					case err := <-writerDone:
						writerJoined = true
						hookErr = errors.Join(hookErr, fmt.Errorf("writer finished before the reader's transaction ended: %v", err))
					default:
					}
					if probe.counts["payload"] != 3 || probe.counts["lock"] != 1 {
						hookErr = errors.Join(hookErr, fmt.Errorf("statements %v", probe.counts))
					}
					return nil
				})
				if err != nil || hookErr != nil || readErr != nil {
					t.Fatalf("hold race: tx=%v hook=%v read=%v", err, hookErr, readErr)
				}
				bLast, _ := rec["b_last"].([]byte)
				if rec["t_last"] != "a" || rec["t_first"] != "a" || !bytes.Equal(bLast, []byte{1}) {
					t.Errorf("reader observed an inter-group replacement: t_last=%#v", rec["t_last"])
				}
				if err := joinWriter(); err != nil {
					t.Fatalf("writer after the reader committed: %v", err)
				}
				var tLast string
				if err := monitor.QueryRowContext(ctx, "SELECT t_last FROM public.brt_split WHERE id = $1", split.full.String()).Scan(&tLast); err != nil || tLast != "z" {
					t.Errorf("writer's committed value: %q err=%v", tLast, err)
				}
				if _, err := monitor.ExecContext(ctx, "UPDATE public.brt_split SET t_last = 'a' WHERE id = $1", split.full.String()); err != nil {
					t.Fatalf("restore: %v", err)
				}
			})

			// No test-owned transaction, lock wait or writer remains.
			var open int
			if err := monitor.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_stat_activity "+
				"WHERE datname = pg_catalog.current_database() AND pid <> pg_catalog.pg_backend_pid() "+
				"AND (wait_event_type = 'Lock' OR state LIKE 'idle in transaction%' "+
				"OR (state = 'active' AND query LIKE 'UPDATE public.brt_%'))").Scan(&open); err != nil || open != 0 {
				t.Errorf("leaked test owners: %d err=%v", open, err)
			}
		})
	}
}

// boundedCapture is a statement port that records the exact QueryContext
// text and stops before any driver.
type boundedCapture struct{ queries []string }

var errBoundedCaptured = errors.New("statement captured before the driver")

func (c *boundedCapture) QueryContext(_ context.Context, query string, _ ...any) (*sql.Rows, error) {
	c.queries = append(c.queries, query)
	return nil, errBoundedCaptured
}

// TestBoundedReaderPayloadPreflightCountsEmittedBytes drives the actual
// planner, payload renderer, page preflight and statement path through both
// real dialects with a capture port (the independent review's counterexample,
// generalized). The constructed predicates add PostgreSQL's two-digit
// placeholders and a quoted literal containing '?' that must not be rewritten.
func TestBoundedReaderPayloadPreflightCountsEmittedBytes(t *testing.T) {
	for _, c := range []struct {
		name, where  string
		placeholders int
		literal      string
	}{
		{"three placeholders", "tenant_id = ? AND deleted_at IS NULL AND workspace_id = ? AND tag = ?", 3, ""},
		{"twelve placeholders and a quoted question mark",
			"tenant_id = ? AND tag <> 'a''?''b' AND f1 IN (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", 12, "'a''?''b'"},
	} {
		for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
			t.Run(c.name+"/"+string(engine), func(t *testing.T) {
				dia, ok := dialect.New(engine)
				if !ok {
					t.Fatalf("dialect %s", engine)
				}
				capture := &boundedCapture{}
				r := &boundedReader{sc: &tenantScope{s: &sqlStore{dia: dia}}, lim: boundedTestLimits(),
					columns: boundedSplitColumns, q: capture}
				call := &boundedCall{r: r}
				desc := boundedSplitEntity
				plan, err := r.plan(desc)
				if err != nil {
					t.Fatal(err)
				}
				admissions := make([]boundedAdmission, plan.arity)
				for i := range admissions {
					col, kind, nullable := boundedColumnAt(desc, i)
					a := boundedAdmission{column: col, kind: kind, nullable: nullable, class: pgStorageClass(kind), octets: 36}
					charge, err := call.admit(&a, true, sqliteEncodingUTF8)
					if err != nil {
						t.Fatal(err)
					}
					a.charge = charge
					admissions[i] = a
				}
				args := make([]any, c.placeholders)
				for i := range args {
					args[i] = fmt.Sprintf("fixture-%d", i)
				}
				p := boundedPayload{relation: "brt_split", where: c.where, id: "00000000-0000-7000-8000-000000000001",
					args: args, row: boundedRow{admissions: admissions}, pg: engine == store.EnginePostgres}
				type group struct{ start, end int }
				var groups []group
				for start := 0; start < plan.arity; {
					end, _ := r.nextPayloadGroup(desc, plan.arity, start)
					groups = append(groups, group{start, end})
					start = end
				}
				issue := func(limit uint64, g group) (int, error) {
					r.lim.MaxQueryBytes = limit
					before := len(capture.queries)
					units, _ := p.groupUnits(g.start, g.end)
					_, err := (&boundedCall{r: r}).queryRendered(context.Background(), "payload", p.render(g.start, g.end),
						1, units-boundedRowUnits, 1, 0, p.args, p.id)
					if len(capture.queries) == before {
						return -1, err
					}
					return len(capture.queries[len(capture.queries)-1]), err
				}
				// Emitted bytes of every planned statement, measured at the port.
				emitted, longestAt := make([]int, len(groups)), 0
				for i, g := range groups {
					n, err := issue(1<<30, g)
					if !errors.Is(err, errBoundedCaptured) {
						t.Fatalf("group %d not issued: %v", i, err)
					}
					query := capture.queries[len(capture.queries)-1]
					if c.literal != "" && !strings.Contains(query, c.literal) {
						t.Fatalf("the quoted literal was rewritten: %s", query)
					}
					if engine == store.EnginePostgres &&
						(strings.Count(query, "?") != strings.Count(c.literal, "?") || !strings.Contains(query, fmt.Sprintf("$%d", c.placeholders+1))) {
						t.Fatalf("PostgreSQL placeholders not rebound: %s", query)
					}
					emitted[i] = n
					if n > emitted[longestAt] {
						longestAt = i
					}
				}
				longest := uint64(emitted[longestAt])
				t.Logf("emitted payload statement bytes %v, longest group %d", emitted, longestAt)
				// The exact count renders nothing: no allocation proportional to SQL.
				render := p.render(groups[longestAt].start, groups[longestAt].end)
				var counted uint64
				if allocs := testing.AllocsPerRun(10, func() {
					count := boundedSQL{rb: r.rebinder()}
					render(&count)
					counted = count.n
				}); allocs != 0 || counted != longest {
					t.Errorf("exact count allocated %.0f times and counted %d, emitted %d", allocs, counted, longest)
				}

				r.lim.MaxQueryBytes = longest
				if err := (&boundedCall{r: r}).admitPayloadStatements(boundedTarget{desc: desc}, []boundedPayload{p}); err != nil {
					t.Errorf("page preflight refused the exact emitted length %d: %v", longest, err)
				}
				if n, err := issue(longest, groups[longestAt]); !errors.Is(err, errBoundedCaptured) || uint64(n) != longest {
					t.Errorf("at equality: emitted %d bytes, err=%v", n, err)
				}
				r.lim.MaxQueryBytes = longest - 1
				if err := (&boundedCall{r: r}).admitPayloadStatements(boundedTarget{desc: desc}, []boundedPayload{p}); !errors.Is(err, store.ErrBoundedReadLimit) ||
					!strings.Contains(err.Error(), "MaxQueryBytes") {
					t.Errorf("page preflight admitted a statement one byte over MaxQueryBytes %d: %v", longest-1, err)
				}
				if n, err := issue(longest-1, groups[longestAt]); n != -1 || !errors.Is(err, store.ErrBoundedReadLimit) {
					t.Errorf("one byte short: QueryContext received %d bytes, err=%v", n, err)
				}
			})
		}
	}
}
