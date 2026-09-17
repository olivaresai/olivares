// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// BR1-W1 real-width regressions (ROOT-CONSTRUCTION-REVISION-1, section 7.1).
// They use only the public factory interface, so the same file reproduces the
// pre-grouping failures on the single-statement reader.
const (
	// A = 997: the measured first SQLite failure of the single-statement
	// reader, payload row_ok expression depth (r97 boundary measurement 2.1).
	boundedWideTextFields = 992
	// A = 1003 nullable numeric: one payload statement needs 2002 result columns.
	boundedWideIntNullFields = 998
	// SoftDelete plus 1994 fields: A = 2000, the SQLite build's column maximum.
	boundedSQLiteMaxFields = 1994
	// A = 1600, PostgreSQL's table maximum.
	boundedPGMaxFields = 1595
)

func boundedWideDescriptor(kind, table string, softDelete bool, fields int, spec func(int) (model.SQLKind, bool)) model.EntityDescriptor {
	d := model.EntityDescriptor{Kind: model.Kind(kind), Table: table, SoftDelete: softDelete}
	d.Fields = make([]model.FieldSpec, fields)
	for i := range d.Fields {
		k, nullable := spec(i)
		d.Fields[i] = model.FieldSpec{Name: fmt.Sprintf("f%04d", i), Kind: k, Nullable: nullable}
	}
	return d
}

var (
	boundedWideText = boundedWideDescriptor("brw.text", "brw_text", false, boundedWideTextFields,
		func(int) (model.SQLKind, bool) { return model.KindText, false })
	boundedWideIntNull = boundedWideDescriptor("brw.intnull", "brw_intnull", false, boundedWideIntNullFields,
		func(int) (model.SQLKind, bool) { return model.KindInt, true })
	// boundedSQLiteMax mixes every kind; field i%6: TEXT, nullable TEXT,
	// nullable BYTES, nullable INT, BOOL, nullable FLOAT.
	boundedSQLiteMax = boundedWideDescriptor("brw.max", "brw_max", true, boundedSQLiteMaxFields, boundedMixedKind)
	boundedPGText    = boundedWideDescriptor("brw.pgtext", "brw_pgtext", false, boundedPGMaxFields,
		func(int) (model.SQLKind, bool) { return model.KindText, false })
	boundedPGIntNull = boundedWideDescriptor("brw.pgintnull", "brw_pgintnull", false, boundedPGMaxFields,
		func(int) (model.SQLKind, bool) { return model.KindInt, true })
)

func boundedMixedKind(i int) (model.SQLKind, bool) {
	switch i % 6 {
	case 0:
		return model.KindText, false
	case 1:
		return model.KindText, true
	case 2:
		return model.KindBytes, true
	case 3:
		return model.KindInt, true
	case 4:
		return model.KindBool, false
	default:
		return model.KindFloat, true
	}
}

func registerBoundedWideEntities(descs ...model.EntityDescriptor) func(store.ExtensionRegistry) error {
	return func(reg store.ExtensionRegistry) error {
		for _, d := range descs {
			if err := reg.Register(d); err != nil {
				return err
			}
		}
		return nil
	}
}

func boundedWideLimits() store.BoundedReadLimits {
	limits := boundedTestLimits()
	limits.MaxQueryBytes = 1 << 22
	return limits
}

// boundedWideRows seeds two physically valid rows per descriptor through the
// ordinary repository and returns their IDs in ascending order.
func boundedWideRows(ctx context.Context, sc store.Scope, desc model.EntityDescriptor, value func(row, i int, f model.FieldSpec) any) ([]model.ID, error) {
	repo, err := sc.Ext(desc.Kind)
	if err != nil {
		return nil, err
	}
	ids := make([]model.ID, 0, 2)
	for row := 0; row < 2; row++ {
		rec := model.Record{}
		for i, f := range desc.Fields {
			if v := value(row, i, f); v != nil {
				rec[f.Name] = v
			}
		}
		created, err := repo.Create(ctx, rec)
		if err != nil {
			return nil, fmt.Errorf("create %s row %d: %w", desc.Kind, row, err)
		}
		ids = append(ids, model.ID(created.String(model.ColID)))
	}
	if ids[1].String() < ids[0].String() {
		ids[0], ids[1] = ids[1], ids[0]
	}
	return ids, nil
}

// assertBoundedWideReads compares bounded Get and List with the ordinary
// repository for every non-byte column and with want for byte columns, and
// checks that logical row counters never count groups.
func assertBoundedWideReads(
	ctx context.Context,
	t *testing.T,
	sc store.Scope,
	desc model.EntityDescriptor,
	ids []model.ID,
	wantBytes map[model.ID]map[string][]byte,
) {
	t.Helper()
	repo, err := sc.Ext(desc.Kind)
	if err != nil {
		t.Fatalf("ext %s: %v", desc.Kind, err)
	}
	reader := newBoundedTestReader(t, sc, boundedWideLimits())
	got := make([]model.Record, 0, len(ids))
	for _, id := range ids {
		rec, err := reader.GetExtension(ctx, desc.Kind, id)
		if err != nil {
			t.Fatalf("%s bounded Get (A=%d): %v", desc.Kind, len(desc.AllColumns()), err)
		}
		ordinary, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatalf("%s ordinary Get: %v", desc.Kind, err)
		}
		if len(rec) != len(desc.AllColumns()) {
			t.Errorf("%s bounded Get returned %d columns, want %d", desc.Kind, len(rec), len(desc.AllColumns()))
		}
		for _, col := range desc.AllColumns() {
			if kind, _ := desc.KindOfColumn(col); kind == model.KindBytes {
				want := wantBytes[id][col]
				b, isBytes := rec[col].([]byte)
				switch {
				case want == nil && rec[col] != nil:
					t.Errorf("%s %s: SQL NULL returned %#v", desc.Kind, col, rec[col])
				case want != nil && (!isBytes || b == nil || !reflect.DeepEqual(b, want)):
					t.Errorf("%s %s: got %#v, want non-nil %#v", desc.Kind, col, rec[col], want)
				}
				continue
			}
			if !reflect.DeepEqual(rec[col], ordinary[col]) {
				t.Errorf("%s %s: bounded %#v, ordinary %#v", desc.Kind, col, rec[col], ordinary[col])
			}
		}
		got = append(got, rec)
	}
	u := reader.Usage()
	if u.PayloadRowsReserved != uint64(len(ids)) || u.PayloadRowsObserved != uint64(len(ids)) ||
		!u.ObservedComplete || u.Terminal || u.ObservedUnits != u.ReservedUnits {
		t.Errorf("%s Get usage counts groups or lost observations: %+v", desc.Kind, u)
	}

	lister := newBoundedTestReader(t, sc, boundedWideLimits())
	list, page, err := lister.ListExtensions(ctx, desc.Kind, model.Query{Limit: len(ids)})
	if err != nil {
		t.Fatalf("%s bounded List: %v", desc.Kind, err)
	}
	if page.HasMore || !reflect.DeepEqual(list, got) {
		t.Errorf("%s bounded List differs from bounded Get (rows=%d more=%v)", desc.Kind, len(list), page.HasMore)
	}
	if u := lister.Usage(); u.PayloadRowsReserved != uint64(len(ids)) || u.PayloadRowsObserved != uint64(len(ids)) {
		t.Errorf("%s List logical rows: %+v", desc.Kind, u)
	}
	first, page, err := lister.ListExtensions(ctx, desc.Kind, model.Query{Limit: 1})
	if err != nil || len(first) != 1 || !page.HasMore || page.Cursor != ids[0].String() {
		t.Errorf("%s bounded List page 1: rows=%d page=%+v err=%v", desc.Kind, len(first), page, err)
	}
}

// TestBoundedReaderWideSQLite reads descriptors that crossed the single
// statement's expression depth and result-column ceilings, up to the widest
// table this SQLite build can create.
func TestBoundedReaderWideSQLite(t *testing.T) {
	ctx := context.Background()

	// Engine width, measured rather than assumed: 2000 columns are creatable
	// and 2001 are not.
	raw, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("raw sqlite: %v", err)
	}
	defer func() { _ = raw.Close() }()
	for _, c := range []struct {
		columns int
		ok      bool
	}{{2000, true}, {2001, false}} {
		cols := make([]string, c.columns)
		for i := range cols {
			cols[i] = fmt.Sprintf("c%d", i)
		}
		_, err := raw.ExecContext(ctx, fmt.Sprintf("CREATE TABLE probe_%d (%s)", c.columns, strings.Join(cols, ", ")))
		if (err == nil) != c.ok || (!c.ok && !strings.Contains(err.Error(), "too many columns")) {
			t.Fatalf("CREATE TABLE with %d columns: %v", c.columns, err)
		}
	}

	st := openSQLiteTest(t, registerBoundedWideEntities(boundedWideText, boundedWideIntNull, boundedSQLiteMax))
	tenant := provisionTenant(t, st, "bounded-wide")
	ids := map[model.Kind][]model.ID{}
	wantBytes := map[model.ID]map[string][]byte{}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if ids[boundedWideText.Kind], err = boundedWideRows(ctx, sc, boundedWideText, func(row, _ int, _ model.FieldSpec) any {
			return []string{"v", ""}[row]
		}); err != nil {
			return err
		}
		if ids[boundedWideIntNull.Kind], err = boundedWideRows(ctx, sc, boundedWideIntNull, func(row, i int, _ model.FieldSpec) any {
			if row == 1 {
				return nil
			}
			return int64(i) - 500
		}); err != nil {
			return err
		}
		maxIDs, err := boundedWideRows(ctx, sc, boundedSQLiteMax, func(row, i int, f model.FieldSpec) any {
			switch i % 6 {
			case 0:
				return "t"
			case 4:
				return row == 0 && i%12 == 4
			}
			if row == 1 {
				return nil // every nullable column is SQL NULL
			}
			switch f.Kind {
			case model.KindText:
				return "x"
			case model.KindBytes:
				return []byte{7}
			case model.KindInt:
				return int64(i)
			default:
				return 0.5
			}
		})
		if err != nil {
			return err
		}
		ids[boundedSQLiteMax.Kind] = maxIDs
		// Present empty values in the first and the last payload group of the
		// present row; its other byte columns hold X'07'.
		present := maxIDs[0]
		for _, id := range maxIDs {
			var check sql.NullString
			if err := sc.(*tenantScope).tx.QueryRowContext(ctx, "SELECT f0001 FROM brw_max WHERE id = ?", id.String()).Scan(&check); err != nil {
				return err
			}
			if check.Valid {
				present = id
			}
		}
		rawBoundedExec(ctx, t, sc, "UPDATE brw_max SET f0001 = '', f0002 = X'', f1993 = '', f1988 = X'' WHERE id = ?", present.String())
		wantBytes[present] = map[string][]byte{}
		for i, f := range boundedSQLiteMax.Fields {
			if f.Kind == model.KindBytes {
				wantBytes[present][f.Name] = []byte{7}
				if i == 2 || i == 1988 {
					wantBytes[present][f.Name] = []byte{}
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed wide rows: %v", err)
	}
	for _, desc := range []model.EntityDescriptor{boundedWideText, boundedWideIntNull, boundedSQLiteMax} {
		t.Run(string(desc.Kind), func(t *testing.T) {
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				assertBoundedWideReads(ctx, t, sc, desc, ids[desc.Kind], wantBytes)
				return nil
			}); err != nil {
				t.Fatalf("view: %v", err)
			}
		})
	}
}

// TestBoundedReaderWidePostgres reads the widest PostgreSQL 16 tables: TEXT
// whose single inspection needed 3200 target entries, and nullable numeric
// whose single payload needed 3196, in a binary and a text-result mode.
func TestBoundedReaderWidePostgres(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPGSplit(t)
	superuser, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("superuser: %v", err)
	}
	defer func() { _ = superuser.Close() }()
	for _, c := range []struct {
		columns int
		ok      bool
	}{{1600, true}, {1601, false}} {
		cols := make([]string, c.columns)
		for i := range cols {
			cols[i] = fmt.Sprintf("c%d int8", i)
		}
		table := fmt.Sprintf("public.brw_probe_%d", c.columns)
		_, err := superuser.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s (%s)", table, strings.Join(cols, ", ")))
		var pgErr *pgconn.PgError
		if (err == nil) != c.ok || (!c.ok && (!errors.As(err, &pgErr) || pgErr.Code != "54011")) {
			t.Fatalf("CREATE TABLE with %d columns: %v", c.columns, err)
		}
		if err == nil {
			if _, err := superuser.ExecContext(ctx, "DROP TABLE "+table); err != nil {
				t.Fatalf("drop probe: %v", err)
			}
		}
	}

	register := registerBoundedWideEntities(boundedPGText, boundedPGIntNull)
	open := func(mode pgExecModeFact) store.Store {
		t.Helper()
		sep := "?"
		if strings.Contains(pg.App, "?") {
			sep = "&"
		}
		st, err := Open(ctx, store.Config{
			Engine: store.EnginePostgres, DSN: pg.App + sep + "default_query_exec_mode=" + mode.String(),
			OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 4,
		}, register)
		if err != nil {
			t.Fatalf("open postgres store (%s): %v", mode, err)
		}
		return st
	}
	seed := open(pgExecModeCacheStatement)
	tenant := provisionTenant(t, seed, "bounded-wide-pg")
	ids := map[model.Kind][]model.ID{}
	if err := seed.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if ids[boundedPGText.Kind], err = boundedWideRows(ctx, sc, boundedPGText, func(row, _ int, _ model.FieldSpec) any {
			return []string{"v", ""}[row]
		}); err != nil {
			return err
		}
		// A physically valid row: a quarter of the int8 values present keeps
		// the tuple within one heap page.
		ids[boundedPGIntNull.Kind], err = boundedWideRows(ctx, sc, boundedPGIntNull, func(row, i int, _ model.FieldSpec) any {
			if row == 1 || i%4 != 0 {
				return nil
			}
			return int64(i) - 800
		})
		return err
	}); err != nil {
		t.Fatalf("seed wide rows: %v", err)
	}
	_ = seed.Close()
	for _, mode := range []pgExecModeFact{pgExecModeCacheStatement, pgExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			st := open(mode)
			defer func() { _ = st.Close() }()
			for _, desc := range []model.EntityDescriptor{boundedPGText, boundedPGIntNull} {
				t.Run(string(desc.Kind), func(t *testing.T) {
					for name, run := range map[string]func(context.Context, model.TenantID, func(store.Scope) error) error{
						"View": st.View, "Mutate": st.Mutate,
					} {
						if err := run(ctx, tenant, func(sc store.Scope) error {
							assertBoundedWideReads(ctx, t, sc, desc, ids[desc.Kind], nil)
							return nil
						}); err != nil {
							t.Fatalf("%s: %v", name, err)
						}
					}
				})
			}
		})
	}
}
