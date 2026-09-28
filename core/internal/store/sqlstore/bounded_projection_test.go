// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The bounded reader's projections, ProjectBounded and ProjectBoundedOne, and
// its bounded policy-artifact read, exercised through the factory on real
// SQLite. Every limit here is an explicit fixture input, not a product budget.

// projectionKind is the lineage-bearing test kind the projections read.
var projectionKind = boundedTestEntity.Kind

func projectionOf(filters []model.Filter, columns ...store.BoundedColumn) store.BoundedProjection {
	return store.BoundedProjection{Kind: projectionKind, Query: model.Query{Filters: filters}, Columns: columns}
}

func labelIs(label string) []model.Filter {
	return []model.Filter{{Column: "label", Op: model.OpEq, Value: label}}
}

func projectionStatements(p *boundedProbe) string {
	return fmt.Sprintf("representation=%d keys=%d lock=%d inspection=%d payload=%d",
		p.counts["representation"], p.counts["keys"], p.counts["lock"], p.counts["inspection"], p.counts["payload"])
}

// assertStoredClass fails the test unless SQLite really stores the row's
// column value in class: an assignment converted by column affinity is not a
// wrong-class fixture.
func assertStoredClass(ctx context.Context, t *testing.T, sc store.Scope, row model.ID, column, class string) {
	t.Helper()
	var got string
	err := sc.(*tenantScope).tx.QueryRowContext(ctx, "SELECT typeof("+column+") FROM brt_item WHERE id = ?", row.String()).Scan(&got)
	if err != nil || got != class {
		t.Fatalf("stored class of %s is %q (%v), want %q", column, got, err, class)
	}
}

// projectionCapture records the exact text of every statement a reader
// issues and forwards it to the reader's own transaction.
type projectionCapture struct {
	next    boundedQuerier
	queries []string
}

func (c *projectionCapture) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.queries = append(c.queries, query)
	return c.next.QueryContext(ctx, query, args...)
}

func capturedReader(t *testing.T, sc store.Scope, limits store.BoundedReadLimits) (store.BoundedReader, *projectionCapture) {
	t.Helper()
	reader := newBoundedTestReader(t, sc, limits)
	br := reader.(*boundedReader)
	capture := &projectionCapture{next: br.q}
	br.q = capture
	return reader, capture
}

// denormalizedArtifactColumns are the eight artifact columns the bounded
// artifact read never selects: decodePolicyArtifact does not consume them.
var denormalizedArtifactColumns = []string{
	"authority_id", "surface", "engine", "artifact_digest", "origin", "availability",
	"governance_surface", "governance_revision",
}

func assertNoDenormalizedArtifactColumns(t *testing.T, capture *projectionCapture) {
	t.Helper()
	if len(capture.queries) == 0 {
		t.Fatal("no statement was captured")
	}
	for _, column := range denormalizedArtifactColumns {
		word := regexp.MustCompile(`(^|[^a-z_])` + column + `([^a-z_]|$)`)
		for _, query := range capture.queries {
			if word.MatchString(query) {
				t.Errorf("the bounded artifact read selected %s: %s", column, query)
			}
		}
	}
}

// projectionArtifactBounds admit the fixture artifacts: their content columns
// and their metadata (UUIDs, timestamps, digests, producer names) fit.
var projectionArtifactBounds = store.PolicyArtifactBounds{ContentBytes: 4096, MetadataBytes: 128}

// tamperedArtifactCopy inserts a copy of an artifact whose record digest no
// longer verifies: its source event id changes and its stored digest does not.
func tamperedArtifactCopy(ctx context.Context, sc store.Scope, artifact model.ID) (model.ID, error) {
	copyID := model.NewID()
	columns := "id, tenant_id, created_at, updated_at, version, schema_version, producer_instance, source_event_id, " +
		"event_type, adapter_version, occurred_at, recorded_at, record_digest, ledger_ref, content, authority_id, " +
		"surface, engine, artifact_digest, origin, availability, governance_surface, governance_revision"
	err := boundedExecIn(ctx, sc, "INSERT INTO policy_artifacts ("+columns+") SELECT ?, tenant_id, created_at, "+
		"updated_at, version, schema_version, producer_instance, source_event_id || '-tampered', event_type, "+
		"adapter_version, occurred_at, recorded_at, record_digest, ledger_ref, content, authority_id, surface, "+
		"engine, artifact_digest, origin, availability, governance_surface, governance_revision "+
		"FROM policy_artifacts WHERE id = ?", copyID.String(), artifact.String())
	return copyID, err
}

// identityRewrite replays the engine's first payload row with one value
// replaced by another of the same length, as an engine returning a row other
// than the one its predicate selected would. The replacement fits every byte
// bound, so only the load's identity comparison can refuse it. err records a
// row it could not rewrite, so the test never mistakes that for a refusal.
type identityRewrite struct {
	next     boundedQuerier
	column   int
	value    string
	replaced bool
	err      error
	replays  []*sql.DB
}

func (q *identityRewrite) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if q.replaced || boundedStatementKind(query) != "payload" {
		return q.next.QueryContext(ctx, query, args...)
	}
	q.replaced = true
	engineRows, err := q.next.QueryContext(ctx, query, args...)
	if err != nil {
		q.err = err
		return nil, err
	}
	columns, err := engineRows.Columns()
	connector := &boundedReplayConnector{columns: columns}
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
		q.err = err
		return nil, err
	}
	if q.column >= len(connector.row) {
		q.err = fmt.Errorf("identity rewrite: the payload row has no column %d", q.column)
		return nil, q.err
	}
	var width int
	switch v := connector.row[q.column].(type) {
	case string:
		width = len(v)
	case []byte:
		width = len(v)
	default:
		q.err = fmt.Errorf("identity rewrite: column %d holds a %T", q.column, v)
		return nil, q.err
	}
	if width != len(q.value) {
		q.err = fmt.Errorf("identity rewrite: column %d is %d bytes, the replacement %d", q.column, width, len(q.value))
		return nil, q.err
	}
	connector.row[q.column] = q.value
	db := sql.OpenDB(connector)
	q.replays = append(q.replays, db)
	return db.QueryContext(ctx, "replay")
}

// identityRewriteReader creates a reader whose first payload row has value in
// its column-th result column.
func identityRewriteReader(t *testing.T, sc store.Scope, column int, value string) (store.BoundedReader, *identityRewrite) {
	t.Helper()
	reader := newBoundedTestReader(t, sc, boundedTestLimits())
	br := reader.(*boundedReader)
	rewrite := &identityRewrite{next: br.q, column: column, value: value}
	br.q = rewrite
	t.Cleanup(func() {
		for _, db := range rewrite.replays {
			_ = db.Close()
		}
	})
	return reader, rewrite
}

// assertIdentityDiffersAtLoad reads artifact through a reader whose first
// payload row names another id, then another tenant, of the same length. Each
// is a consistency failure that ends the reader, never metadata or absence.
func assertIdentityDiffersAtLoad(ctx context.Context, t *testing.T, sc store.Scope, artifact model.ID,
	other model.TenantID, bounds store.PolicyArtifactBounds) {
	t.Helper()
	for _, c := range []struct {
		name   string
		column int
		value  string
	}{
		{"id", 0, model.NewID().String()},
		{"tenant", 1, other.String()},
	} {
		reader, rewrite := identityRewriteReader(t, sc, c.column, c.value)
		_, err := reader.GetPolicyArtifact(ctx, artifact, bounds)
		if rewrite.err != nil || !rewrite.replaced {
			t.Fatalf("could not give the payload row another %s: %v", c.name, rewrite.err)
		}
		if !errors.Is(err, store.ErrBoundedReadConsistency) || errors.Is(err, store.ErrBoundedReadMetadata) ||
			errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "row identity differs") ||
			!reader.Usage().Terminal {
			t.Errorf("an artifact whose %s differs at load: err=%v usage=%+v; want a terminal consistency failure",
				c.name, err, reader.Usage())
		}
	}
}

// openEncodedSQLiteTest creates a real SQLite database file in encoding before
// the store opens it, as openUTF16SQLiteTest does, with the bounded-reader
// test kinds registered.
func openEncodedSQLiteTest(t *testing.T, encoding string) store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bounded-projection.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	for _, statement := range []string{
		"PRAGMA encoding = '" + encoding + "'",
		"CREATE TABLE bounded_encoding_seed (x)",
		"DROP TABLE bounded_encoding_seed",
	} {
		if _, err := raw.Exec(statement); err != nil {
			_ = raw.Close()
			t.Fatalf("prepare %s database: %v", encoding, err)
		}
	}
	var got string
	if err := raw.QueryRow("PRAGMA encoding").Scan(&got); err != nil || got != encoding {
		_ = raw.Close()
		t.Fatalf("raw database encoding %q, want %q: %v", got, encoding, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: path, Debug: true},
		registerBoundedTestEntities)
	if err != nil {
		t.Fatalf("open %s store: %v", encoding, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestBoundedProjectionRefusesRequestsBeforeIO: every malformed, denied or
// oversized request is refused before any statement, consumes nothing and
// leaves the reader usable.
func TestBoundedProjectionRefusesRequestsBeforeIO(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "projection-preio")
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		limits := boundedTestLimits()
		limits.MaxParameterBytes = 64
		reader, probe := boundedGroupReader(t, sc, limits, 0)
		amount := store.BoundedColumn{Name: "amount"}
		label := store.BoundedColumn{Name: "label", MaxBytes: 16}
		with := func(q model.Query, columns ...store.BoundedColumn) store.BoundedProjection {
			return store.BoundedProjection{Kind: projectionKind, Query: q, Columns: columns}
		}
		one := func(p store.BoundedProjection) error { _, err := reader.ProjectBoundedOne(ctx, p); return err }
		page := func(p store.BoundedProjection) error { _, _, err := reader.ProjectBounded(ctx, p); return err }
		artifact := func(id model.ID, b store.PolicyArtifactBounds) error {
			_, err := reader.GetPolicyArtifact(ctx, id, b)
			return err
		}
		// One more column than the descriptor can supply (every column but id).
		many := make([]store.BoundedColumn, boundedBaseCount(boundedTestEntity)+len(boundedTestEntity.Fields))
		for i := range many {
			many[i] = amount
		}
		for _, c := range []struct {
			name string
			want error
			run  func() error
		}{
			{"no column", store.ErrInvalidBoundedRead, func() error { return one(with(model.Query{})) }},
			{"more columns than the descriptor", store.ErrInvalidBoundedRead, func() error { return one(with(model.Query{}, many...)) }},
			{"repeated column", store.ErrInvalidBoundedRead, func() error { return one(with(model.Query{}, amount, amount)) }},
			{"id listed", store.ErrInvalidBoundedRead, func() error {
				return one(with(model.Query{}, store.BoundedColumn{Name: model.ColID, MaxBytes: 36}))
			}},
			{"deleted_at listed", store.ErrInvalidBoundedRead, func() error {
				return one(with(model.Query{}, store.BoundedColumn{Name: model.ColDeletedAt, MaxBytes: 64}))
			}},
			{"unknown column", store.ErrUnknownEntity, func() error {
				return one(with(model.Query{}, store.BoundedColumn{Name: "nope", MaxBytes: 1}))
			}},
			{"variable column without a bound", store.ErrInvalidBoundedRead, func() error {
				return one(with(model.Query{}, store.BoundedColumn{Name: "label"}))
			}},
			{"bound above MaxCellBytes", store.ErrInvalidBoundedRead, func() error {
				return one(with(model.Query{}, store.BoundedColumn{Name: "label", MaxBytes: limits.MaxCellBytes + 1}))
			}},
			{"fixed-width column with a bound", store.ErrInvalidBoundedRead, func() error {
				return one(with(model.Query{}, store.BoundedColumn{Name: "amount", MaxBytes: 8}))
			}},
			{"column names over MaxParameterBytes", store.ErrBoundedReadLimit, func() error {
				return one(with(model.Query{}, store.BoundedColumn{Name: strings.Repeat("n", 80), MaxBytes: 1}))
			}},
			{"single projection with a limit", store.ErrInvalidBoundedRead, func() error { return one(with(model.Query{Limit: 1}, amount)) }},
			{"single projection with a cursor", store.ErrInvalidBoundedRead, func() error {
				return one(with(model.Query{Cursor: model.NewID().String()}, amount))
			}},
			{"page without a limit", store.ErrInvalidBoundedRead, func() error { return page(with(model.Query{}, amount)) }},
			{"page above MaxRowsPerPage", store.ErrInvalidBoundedRead, func() error { return page(with(model.Query{Limit: 101}, amount)) }},
			{"custom sort", store.ErrInvalidBoundedRead, func() error {
				return page(with(model.Query{Limit: 1, Sort: []model.Sort{{Column: "label"}}}, amount))
			}},
			{"deleted rows", store.ErrInvalidBoundedRead, func() error { return page(with(model.Query{Limit: 1, IncludeDeleted: true}, amount)) }},
			{"core kind", store.ErrUnknownEntity, func() error {
				return one(store.BoundedProjection{Kind: model.PolicyArtifactKind, Columns: []store.BoundedColumn{label}})
			}},
			{"artifact content bound missing", store.ErrInvalidBoundedRead, func() error {
				return artifact(model.NewID(), store.PolicyArtifactBounds{MetadataBytes: 64})
			}},
			{"artifact metadata bound missing", store.ErrInvalidBoundedRead, func() error {
				return artifact(model.NewID(), store.PolicyArtifactBounds{ContentBytes: 64})
			}},
			{"artifact bound above MaxCellBytes", store.ErrInvalidBoundedRead, func() error {
				return artifact(model.NewID(), store.PolicyArtifactBounds{ContentBytes: limits.MaxCellBytes + 1, MetadataBytes: 64})
			}},
			{"artifact id", store.ErrInvalidBoundedRead, func() error {
				return artifact("not-an-id", store.PolicyArtifactBounds{ContentBytes: 64, MetadataBytes: 64})
			}},
		} {
			if err := c.run(); !errors.Is(err, c.want) {
				t.Errorf("%s: err=%v, want %v", c.name, err, c.want)
			}
		}
		// The column count is refused by its own rule. A list as long as many but
		// of distinct names, the first unknown, would otherwise be refused at that
		// name as an unknown column.
		distinct := make([]store.BoundedColumn, len(many))
		for i := range distinct {
			distinct[i] = store.BoundedColumn{Name: fmt.Sprintf("absent_%d", i), MaxBytes: 1}
		}
		if err := one(with(model.Query{}, distinct...)); !errors.Is(err, store.ErrInvalidBoundedRead) ||
			errors.Is(err, store.ErrUnknownEntity) || !strings.Contains(err.Error(), "more columns than its descriptor") {
			t.Errorf("distinct columns past the descriptor: err=%v, want the column-count refusal", err)
		}
		assertBoundedUsageUntouched(t, reader)
		if len(probe.statements) != 0 {
			t.Errorf("a refused request issued SQL: %s", projectionStatements(probe))
		}
		if _, err := reader.ProjectBoundedOne(ctx, projectionOf(labelIs("absent"), amount)); err != store.ErrNotFound {
			t.Fatalf("reader not reusable after refusals: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestBoundedProjectionCopiesOnlyAfterAdmission: a request far larger than
// any descriptor is refused by its column count before any request-sized
// copy, so its allocation does not grow with the request.
func TestBoundedProjectionCopiesOnlyAfterAdmission(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "projection-alloc")
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		reader := newBoundedTestReader(t, sc, boundedTestLimits())
		huge := make([]store.BoundedColumn, 1<<16)
		for i := range huge {
			huge[i] = store.BoundedColumn{Name: "label", MaxBytes: 16}
		}
		request := projectionOf(nil, huge...)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err := reader.ProjectBoundedOne(ctx, request)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, store.ErrInvalidBoundedRead) {
			t.Fatalf("oversized column list: %v", err)
		}
		// A copy of 65,536 shape columns would allocate megabytes; the refusal
		// allocates only its error.
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 256<<10 {
			t.Errorf("a refused request allocated %d bytes before admission", grew)
		}
		assertBoundedUsageUntouched(t, reader)
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestBoundedProjectionPublishesIdAndRequestedColumns: a projection returns
// exactly id and the requested columns, selects only the Scope tenant's rows,
// and charges its private tenant witness in every envelope.
func TestBoundedProjectionPublishesIdAndRequestedColumns(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	foreign := provisionTenant(t, st, "projection-foreign")
	tenant := provisionTenant(t, st, "projection-shape")
	seed := func(target model.TenantID, labels ...string) []model.ID {
		var ids []model.ID
		if err := st.Mutate(ctx, target, func(sc store.Scope) error {
			ws, err := sc.DefaultWorkspace(ctx)
			if err != nil {
				return err
			}
			for _, label := range labels {
				ids = append(ids, seedBoundedItem(ctx, t, sc, ws.ID, label))
			}
			return nil
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		return ids
	}
	// Another tenant holds a row with the same label; only the tenant
	// predicate keeps it out of the selection.
	seed(foreign, "own")
	own := seed(tenant, "own", "page", "page")
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		amount := store.BoundedColumn{Name: "amount"}
		reader := newBoundedTestReader(t, sc, boundedTestLimits())
		rec, err := reader.ProjectBoundedOne(ctx, projectionOf(labelIs("own"), amount))
		if err != nil {
			return err
		}
		if want := (model.Record{model.ColID: own[0].String(), "amount": int64(3)}); !reflect.DeepEqual(rec, want) {
			t.Errorf("published %#v, want %#v", rec, want)
		}
		// Envelopes by hand from the ratified unit table: representation
		// 8+(8+17) = 33; keys 8+2x(8+9+36+17) = 148; inspection of id, the
		// witness and amount 8+(8+17x5) = 101; payload 8+(8+17+45+45+17) = 140.
		// Observed: 33, keys 8+70, inspection 101, payload 140.
		u := reader.Usage()
		if u.ReservedUnits != 422 || u.ObservedUnits != 352 || u.PayloadRowsReserved != 1 || u.PayloadRowsObserved != 1 ||
			u.LookaheadSlotsReserved != 1 || u.LookaheadRowsObserved != 0 || u.Terminal || !u.ObservedComplete {
			t.Errorf("projection usage %+v", u)
		}

		pages := newBoundedTestReader(t, sc, boundedTestLimits())
		seen := map[string]bool{}
		cursor := ""
		for n := 0; ; n++ {
			recs, next, err := pages.ProjectBounded(ctx, store.BoundedProjection{
				Kind: projectionKind, Query: model.Query{Filters: labelIs("page"), Limit: 1, Cursor: cursor},
				Columns: []store.BoundedColumn{amount},
			})
			if err != nil {
				return err
			}
			if len(recs) != 1 || len(recs[0]) != 2 || recs[0]["amount"] != int64(4) {
				t.Fatalf("page %d: %#v", n, recs)
			}
			seen[recs[0].String(model.ColID)] = true
			if !next.HasMore {
				if n != 1 || next.Cursor != "" {
					t.Errorf("final page %d cursor %q", n, next.Cursor)
				}
				break
			}
			if next.Cursor != recs[0].String(model.ColID) {
				t.Errorf("page cursor %q is not its last id", next.Cursor)
			}
			cursor = next.Cursor
		}
		if len(seen) != 2 || !seen[own[1].String()] || !seen[own[2].String()] {
			t.Errorf("pages returned %v", seen)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestBoundedProjectionOneDecidesBeforeRowStatements: a single projection
// decides absence and ambiguity from its keys alone, before any inspection or
// payload statement, whatever the first duplicate holds; a selected row that
// disappears later is a consistency failure.
func TestBoundedProjectionOneDecidesBeforeRowStatements(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "projection-one")
	var classFirst, vanishing model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		ordered := func(label string) (model.ID, model.ID) {
			a := seedBoundedItem(ctx, t, sc, ws.ID, label)
			b := seedBoundedItem(ctx, t, sc, ws.ID, label)
			if b.String() < a.String() {
				return b, a
			}
			return a, b
		}
		// pair-a: the first key's row is small, the second oversized; pair-b
		// the reverse; pair-c: the first key's row holds TEXT in an INTEGER
		// column.
		first, second := ordered("pair-a")
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = 'ok' WHERE id = ?", first.String())
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = 'far over the note bound' WHERE id = ?", second.String())
		first, second = ordered("pair-b")
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = 'far over the note bound' WHERE id = ?", first.String())
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = 'ok' WHERE id = ?", second.String())
		classFirst, _ = ordered("pair-c")
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET amount = 'not a number' WHERE id = ?", classFirst.String())
		vanishing = seedBoundedItem(ctx, t, sc, ws.ID, "vanishing")
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	note := store.BoundedColumn{Name: "note", MaxBytes: 4}
	amount := store.BoundedColumn{Name: "amount"}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		assertStoredClass(ctx, t, sc, classFirst, "amount", "text")
		for _, c := range []struct {
			name   string
			label  string
			column store.BoundedColumn
		}{
			{"first small, second oversized", "pair-a", note},
			{"first oversized, second small", "pair-b", note},
			{"first of the wrong storage class", "pair-c", amount},
		} {
			reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), 0)
			_, err := reader.ProjectBoundedOne(ctx, projectionOf(labelIs(c.label), c.column))
			if !errors.Is(err, store.ErrBoundedReadMetadata) || errors.Is(err, store.ErrBoundedReadLimit) ||
				!strings.Contains(err.Error(), "selection is not unique") {
				t.Errorf("%s: err=%v, want ambiguity", c.name, err)
			}
			if probe.counts["keys"] != 1 || probe.counts["lock"] != 0 || probe.counts["inspection"] != 0 || probe.counts["payload"] != 0 {
				t.Errorf("%s: ambiguity decided after a row statement: %s", c.name, projectionStatements(probe))
			}
			if u := reader.Usage(); u.PayloadRowsReserved != 0 || u.LookaheadRowsObserved != 1 || !u.Terminal {
				t.Errorf("%s: usage %+v", c.name, u)
			}
		}

		// The wrong-class row alone is malformed, never absent or empty.
		reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), 0)
		byID := []model.Filter{{Column: model.ColID, Op: model.OpEq, Value: classFirst.String()}}
		_, err := reader.ProjectBoundedOne(ctx, projectionOf(byID, amount))
		if !errors.Is(err, store.ErrBoundedReadMetadata) || !strings.Contains(err.Error(), "inadmissible storage class") ||
			probe.counts["inspection"] != 1 || probe.counts["payload"] != 0 {
			t.Errorf("single wrong-class row: err=%v %s", err, projectionStatements(probe))
		}

		// Absence is ordinary: the same reader then reads a present row.
		reader = newBoundedTestReader(t, sc, boundedTestLimits())
		if _, err := reader.ProjectBoundedOne(ctx, projectionOf(labelIs("absent"), amount)); err != store.ErrNotFound {
			t.Errorf("absent: %v", err)
		}
		if reader.Usage().Terminal {
			t.Error("absence ended the reader")
		}
		if rec, err := reader.ProjectBoundedOne(ctx, projectionOf(labelIs("vanishing"), amount)); err != nil ||
			rec.String(model.ColID) != vanishing.String() {
			t.Errorf("present row after absence: rec=%v err=%v", rec, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}

	// The selected row disappears inside the same transaction between key
	// selection and inspection: consistency, not absence.
	var readErr, hookErr error
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), 0)
		probe.before = func(kind string, n int) boundedProbeAction {
			if kind == "inspection" && n == 1 {
				hookErr = boundedExecIn(ctx, sc, "DELETE FROM brt_item WHERE id = ?", vanishing.String())
			}
			return boundedProbeAction{}
		}
		_, readErr = reader.ProjectBoundedOne(ctx, projectionOf(labelIs("vanishing"), amount))
		return errBoundedGroupRollback
	})
	if !errors.Is(err, errBoundedGroupRollback) || hookErr != nil {
		t.Fatalf("disappearance fixture: tx=%v hook=%v", err, hookErr)
	}
	if !errors.Is(readErr, store.ErrBoundedReadConsistency) || errors.Is(readErr, store.ErrNotFound) {
		t.Errorf("selected row disappeared: %v", readErr)
	}
}

// TestBoundedProjectionChecksClassBeforeBound: every requested column's
// storage class precedes every bound, so an earlier oversized column never
// hides a later malformed one. The fixture's stored class is verified, not
// assumed.
func TestBoundedProjectionChecksClassBeforeBound(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "projection-class")
	var row model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		row = seedBoundedItem(ctx, t, sc, ws.ID, "class-before-bound")
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET amount = 'not a number' WHERE id = ?", row.String())
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		assertStoredClass(ctx, t, sc, row, "amount", "text")
		label := store.BoundedColumn{Name: "label", MaxBytes: 4} // the stored label has 18 octets
		amount := store.BoundedColumn{Name: "amount"}
		byID := []model.Filter{{Column: model.ColID, Op: model.OpEq, Value: row.String()}}
		for _, c := range []struct {
			name    string
			columns []store.BoundedColumn
			want    error
			text    string
		}{
			{"oversized earlier, malformed later", []store.BoundedColumn{label, amount}, store.ErrBoundedReadMetadata, "column amount has an inadmissible storage class"},
			{"malformed earlier, oversized later", []store.BoundedColumn{amount, label}, store.ErrBoundedReadMetadata, "column amount has an inadmissible storage class"},
			{"oversized alone", []store.BoundedColumn{label}, store.ErrBoundedReadLimit, "cell label admission bound 18 does not fit MaxBytes"},
			{"malformed alone", []store.BoundedColumn{amount}, store.ErrBoundedReadMetadata, "column amount has an inadmissible storage class"},
		} {
			reader, probe := boundedGroupReader(t, sc, boundedTestLimits(), 0)
			_, err := reader.ProjectBoundedOne(ctx, projectionOf(byID, c.columns...))
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.text) {
				t.Errorf("%s: err=%v, want %q", c.name, err, c.text)
			}
			if probe.counts["payload"] != 0 || reader.Usage().PayloadRowsReserved != 0 || !reader.Usage().Terminal {
				t.Errorf("%s: %s usage %+v", c.name, projectionStatements(probe), reader.Usage())
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestBoundedProjectionRepresentationPerEncoding: projected TEXT admits its
// stored octets times the ratified multiplier, BLOB its octets, and id and
// the private tenant witness the encoding-aware canonical key bound, on real
// UTF-8, UTF-16LE and UTF-16BE databases. No product limit is involved.
func TestBoundedProjectionRepresentationPerEncoding(t *testing.T) {
	for _, c := range []struct {
		encoding  string
		text, key uint64 // admitted bounds of "cedar" and of a canonical id
	}{
		{"UTF-8", 5, 36},
		{"UTF-16le", 20, 144},
		{"UTF-16be", 20, 144},
	} {
		t.Run(c.encoding, func(t *testing.T) {
			ctx := context.Background()
			st := openEncodedSQLiteTest(t, c.encoding)
			tenant := provisionTenant(t, st, "projection-encoding")
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				ws, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				row := seedBoundedItem(ctx, t, sc, ws.ID, "cedar")
				rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET payload = X'0102030405' WHERE id = ?", row.String())
				return nil
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				one := func(limits store.BoundedReadLimits, column store.BoundedColumn) (model.Record, store.BoundedReader, error) {
					reader := newBoundedTestReader(t, sc, limits)
					rec, err := reader.ProjectBoundedOne(ctx, projectionOf(labelIs("cedar"), column))
					return rec, reader, err
				}
				limits := boundedTestLimits()
				// len("cedar") is 5 on every encoding; the admission bound is not.
				rec, _, err := one(limits, store.BoundedColumn{Name: "label", MaxBytes: c.text})
				if err != nil || rec["label"] != "cedar" {
					t.Errorf("label at its representation bound: rec=%v err=%v", rec, err)
				}
				_, _, err = one(limits, store.BoundedColumn{Name: "label", MaxBytes: c.text - 1})
				if !errors.Is(err, store.ErrBoundedReadLimit) ||
					!strings.Contains(err.Error(), fmt.Sprintf("admission bound %d does not fit MaxBytes", c.text)) {
					t.Errorf("label one below its representation bound: %v", err)
				}
				// BLOB is never multiplied.
				rec, _, err = one(limits, store.BoundedColumn{Name: "payload", MaxBytes: 5})
				if err != nil || !reflect.DeepEqual(rec["payload"], []byte{1, 2, 3, 4, 5}) {
					t.Errorf("payload at its octet bound: rec=%v err=%v", rec, err)
				}
				_, _, err = one(limits, store.BoundedColumn{Name: "payload", MaxBytes: 4})
				if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "admission bound 5 does not fit MaxBytes") {
					t.Errorf("payload one below its octet bound: %v", err)
				}
				// The key bound refuses before key selection below it and admits
				// id and the witness at it.
				tight := limits
				tight.MaxCellBytes = c.key - 1
				_, reader, err := one(tight, store.BoundedColumn{Name: "amount"})
				if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "MaxCellBytes") ||
					reader.Usage().LookaheadSlotsReserved != 0 {
					t.Errorf("key bound above MaxCellBytes: err=%v usage=%+v", err, reader.Usage())
				}
				exact := limits
				exact.MaxCellBytes = c.key
				rec, reader, err = one(exact, store.BoundedColumn{Name: "amount"})
				if err != nil || rec["amount"] != int64(5) {
					t.Errorf("key bound at MaxCellBytes: rec=%v err=%v", rec, err)
				}
				// Representation 33, keys 8+2x(8+9+k+17), inspection 8+(8+17x5),
				// payload 8+(8+17+2x(9+k)+17): the witness is charged at k.
				k := c.key
				want := 33 + (8 + 2*(8+9+k+17)) + (8 + 8 + 17*5) + (8 + 8 + 17 + 2*(9+k) + 17)
				if got := reader.Usage().ReservedUnits; got != want {
					t.Errorf("reserved %d units, want %d", got, want)
				}
				return nil
			}); err != nil {
				t.Fatalf("view: %v", err)
			}
		})
	}
}

// TestBoundedProjectionConfinementHasNoRawFallback: under a workspace
// boundary a lineage-less kind and access evidence are refused before any
// statement, and a lineage kind is read through the forced lineage only.
func TestBoundedProjectionConfinementHasNoRawFallback(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "projection-confined")
	defaultWS, otherWS := distinctProjectionWorkspaces(t, st, tenant)
	var own model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		seedBoundedItem(ctx, t, sc, defaultWS, "shared")
		own = seedBoundedItem(ctx, t, sc, otherWS, "shared")
		plain, err := sc.Ext(boundedPlainEntity.Kind)
		if err != nil {
			return err
		}
		_, err = plain.Create(ctx, model.Record{"label": "plain"})
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	artifact := retainArtifact(t, st, tenant, "projection-confined", "permit(principal, action, resource);")
	if err := st.View(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, otherWS)
		if err != nil {
			return err
		}
		reader, probe := boundedGroupReader(t, confined, boundedTestLimits(), 0)
		label := store.BoundedColumn{Name: "label", MaxBytes: 64}
		plain := store.BoundedProjection{Kind: boundedPlainEntity.Kind, Columns: []store.BoundedColumn{label}}
		if _, err := reader.ProjectBoundedOne(ctx, plain); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Errorf("confined lineage-less single projection: %v", err)
		}
		plain.Query.Limit = 5
		if _, _, err := reader.ProjectBounded(ctx, plain); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Errorf("confined lineage-less page: %v", err)
		}
		if _, err := reader.GetPolicyArtifact(ctx, artifact.ID, projectionArtifactBounds); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Errorf("confined artifact: %v", err)
		}
		assertBoundedUsageUntouched(t, reader)
		if len(probe.statements) != 0 {
			t.Errorf("a denied read issued SQL: %s", projectionStatements(probe))
		}

		// The caller's own lineage filter is replaced by the forced one.
		filters := append(labelIs("shared"), model.Filter{Column: "workspace_id", Op: model.OpEq, Value: defaultWS.String()})
		rec, err := reader.ProjectBoundedOne(ctx, projectionOf(filters, label))
		if err != nil || rec.String(model.ColID) != own.String() {
			t.Errorf("confined projection: rec=%v err=%v", rec, err)
		}

		// Unconfined, the same artifact is readable: the denial is confinement.
		open := newBoundedTestReader(t, raw, boundedTestLimits())
		if _, err := open.GetPolicyArtifact(ctx, artifact.ID, projectionArtifactBounds); err != nil {
			t.Errorf("unconfined artifact: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestBoundedPolicyArtifactReadsIntegrityShape: the bounded artifact read
// equals the ordinary decode, never selects the eight denormalized columns,
// refuses its content one octet over the bound before any payload, and keeps
// the existing record-digest check as terminal metadata.
func TestBoundedPolicyArtifactReadsIntegrityShape(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	foreign := provisionTenant(t, st, "projection-artifact-foreign")
	tenant := provisionTenant(t, st, "projection-artifact")
	foreignArtifact := retainArtifact(t, st, foreign, "projection-artifact-foreign", "permit(principal, action, resource);")
	// JSON escapes <, & and >, so the stored representation is longer than
	// the logical content.
	artifact := retainArtifact(t, st, tenant, "projection-artifact",
		`permit(principal, action, resource) when { context.note == "<&>" };`)
	var tampered model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		tampered, err = tamperedArtifactCopy(ctx, sc, artifact.ID)
		return err
	}); err != nil {
		t.Fatalf("tampered copy: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var content uint64
		if err := sc.(*tenantScope).tx.QueryRowContext(ctx, "SELECT octet_length(content) FROM policy_artifacts WHERE id = ?",
			artifact.ID.String()).Scan(&content); err != nil {
			return err
		}
		want, err := sc.AccessEvidence().PolicyArtifact(ctx, artifact.ID)
		if err != nil {
			return err
		}
		bounds := projectionArtifactBounds
		bounds.ContentBytes = content
		reader, capture := capturedReader(t, sc, boundedTestLimits())
		got, err := reader.GetPolicyArtifact(ctx, artifact.ID, bounds)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("bounded artifact differs from the ordinary read: err=%v", err)
		}
		assertNoDenormalizedArtifactColumns(t, capture)

		tight := bounds
		tight.ContentBytes = content - 1
		limited, probe := boundedGroupReader(t, sc, boundedTestLimits(), 0)
		if _, err := limited.GetPolicyArtifact(ctx, artifact.ID, tight); !errors.Is(err, store.ErrBoundedReadLimit) ||
			!strings.Contains(err.Error(), fmt.Sprintf("cell content admission bound %d does not fit MaxBytes", content)) {
			t.Errorf("content one octet over its bound: %v", err)
		}
		if probe.counts["payload"] != 0 || !limited.Usage().Terminal {
			t.Errorf("content refusal: %s usage %+v", projectionStatements(probe), limited.Usage())
		}

		tamperReader := newBoundedTestReader(t, sc, boundedTestLimits())
		_, err = tamperReader.GetPolicyArtifact(ctx, tampered, bounds)
		if !errors.Is(err, store.ErrBoundedReadMetadata) || !errors.Is(err, store.ErrAccessEvidenceIntegrity) ||
			!tamperReader.Usage().Terminal || tamperReader.Usage().PayloadRowsObserved != 1 {
			t.Errorf("tampered artifact: err=%v usage=%+v", err, tamperReader.Usage())
		}

		assertIdentityDiffersAtLoad(ctx, t, sc, artifact.ID, foreign, bounds)

		absent := newBoundedTestReader(t, sc, boundedTestLimits())
		for name, id := range map[string]model.ID{"absent": model.NewID(), "foreign": foreignArtifact.ID} {
			if _, err := absent.GetPolicyArtifact(ctx, id, bounds); err != store.ErrNotFound {
				t.Errorf("%s artifact: %v", name, err)
			}
		}
		if absent.Usage().Terminal {
			t.Error("absence ended the reader")
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}
