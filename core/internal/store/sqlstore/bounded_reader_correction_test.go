// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// boundedAliasEntity declares valid field names that spell the renderer's
// former and current private aliases and its subquery alias (BR1-SQL-F1).
var boundedAliasEntity = model.EntityDescriptor{
	Kind:  "brt.alias",
	Table: "brt_alias",
	Fields: []model.FieldSpec{
		{Name: "olivares_br_row_ok", Kind: model.KindInt},
		{Name: "olivares_br_ok", Kind: model.KindInt},
		{Name: "olivares_br_c0", Kind: model.KindText},
		{Name: "olivares_br_c1", Kind: model.KindInt, Nullable: true},
		{Name: "b", Kind: model.KindText, Nullable: true},
	},
}

const boundedWideFields = 1000

// boundedWideEntity is a valid large descriptor for renderer/envelope budget
// refusals (BR1-SQL-F2).
var boundedWideEntity = func() model.EntityDescriptor {
	d := model.EntityDescriptor{Kind: "brt.wide", Table: "brt_wide"}
	for i := 0; i < boundedWideFields; i++ {
		d.Fields = append(d.Fields, model.FieldSpec{
			Name: fmt.Sprintf("wide_%04d_%s", i, strings.Repeat("w", 50)), Kind: model.KindText,
		})
	}
	return d
}()

// boundedWideOKFields keeps the inspection target list (base columns + fields
// + TEXT lengths) under both engines' result-column limits (SQLite 2000,
// PostgreSQL 1664), which the 1000-field refusal fixture exceeds.
const boundedWideOKFields = 600

var boundedWideOKEntity = func() model.EntityDescriptor {
	d := model.EntityDescriptor{Kind: "brt.wideok", Table: "brt_wideok"}
	for i := 0; i < boundedWideOKFields; i++ {
		d.Fields = append(d.Fields, model.FieldSpec{
			Name: fmt.Sprintf("wide_%04d_%s", i, strings.Repeat("w", 50)), Kind: model.KindText,
		})
	}
	return d
}()

func registerBoundedCorrectionEntities(reg store.ExtensionRegistry) error {
	if err := registerBoundedTestEntities(reg); err != nil {
		return err
	}
	if err := reg.Register(boundedAliasEntity); err != nil {
		return err
	}
	if err := reg.Register(boundedWideOKEntity); err != nil {
		return err
	}
	return reg.Register(boundedWideEntity)
}

func openCorrectionPG(t *testing.T, pg pgtest.DSNs, mode pgExecModeFact) store.Store {
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
	}, registerBoundedCorrectionEntities)
	if err != nil {
		t.Fatalf("open postgres store (%s): %v", mode, err)
	}
	return st
}

type correctionFixture struct {
	tenant                      model.TenantID
	alias, wide, wideOK         model.ID
	payloadNull, payloadEmpty   model.ID
	payloadFull, payloadMissing model.ID
}

func seedCorrection(t *testing.T, st store.Store, bytesSQL func(hex string) string) correctionFixture {
	t.Helper()
	ctx := context.Background()
	var f correctionFixture
	f.tenant = provisionTenant(t, st, "bounded-correction")
	if err := st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		alias, err := sc.Ext(boundedAliasEntity.Kind)
		if err != nil {
			return err
		}
		rec, err := alias.Create(ctx, model.Record{
			"olivares_br_row_ok": int64(7), "olivares_br_ok": int64(5), "olivares_br_c0": "zero",
			"olivares_br_c1": int64(-3), "b": "bee",
		})
		if err != nil {
			return err
		}
		f.alias = model.ID(rec.String(model.ColID))
		wide, err := sc.Ext(boundedWideEntity.Kind)
		if err != nil {
			return err
		}
		wideRec := model.Record{}
		for _, field := range boundedWideEntity.Fields {
			wideRec[field.Name] = "v"
		}
		if rec, err = wide.Create(ctx, wideRec); err != nil {
			return err
		}
		f.wide = model.ID(rec.String(model.ColID))
		wideOK, err := sc.Ext(boundedWideOKEntity.Kind)
		if err != nil {
			return err
		}
		wideOKRec := model.Record{}
		for _, field := range boundedWideOKEntity.Fields {
			wideOKRec[field.Name] = "v"
		}
		if rec, err = wideOK.Create(ctx, wideOKRec); err != nil {
			return err
		}
		f.wideOK = model.ID(rec.String(model.ColID))
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		f.payloadNull = seedBoundedItem(ctx, t, sc, ws.ID, "bytes-null")
		f.payloadEmpty = seedBoundedItem(ctx, t, sc, ws.ID, "bytes-empty")
		f.payloadFull = seedBoundedItem(ctx, t, sc, ws.ID, "bytes-full")
		f.payloadMissing = seedBoundedItem(ctx, t, sc, ws.ID, "bytes-other")
		tx := sc.(*tenantScope).tx
		for id, hex := range map[model.ID]string{f.payloadEmpty: "", f.payloadFull: "000102", f.payloadMissing: "ff"} {
			if _, err := tx.ExecContext(ctx, sc.(*tenantScope).s.dia.Rebind(
				"UPDATE brt_item SET payload = "+bytesSQL(hex)+" WHERE id = ?"), id.String()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed correction fixture: %v", err)
	}
	return f
}

func sqliteBytesLiteral(hex string) string   { return "X'" + hex + "'" }
func postgresBytesLiteral(hex string) string { return "'\\x" + hex + "'::bytea" }

// runCorrectionContract is the engine-neutral F1/F2/F3 discrimination.
func runCorrectionContract(t *testing.T, st store.Store, f correctionFixture, firstEnvelope uint64) {
	t.Helper()
	ctx := context.Background()

	t.Run("F1 aliases", func(t *testing.T) {
		if err := st.View(ctx, f.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(boundedAliasEntity.Kind)
			if err != nil {
				return err
			}
			want, err := repo.Get(ctx, f.alias)
			if err != nil {
				return err
			}
			if want["olivares_br_row_ok"] != int64(7) {
				t.Fatalf("ordinary Get fixture = %#v", want)
			}
			reader := newBoundedTestReader(t, sc, boundedTestLimits())
			got, err := reader.GetExtension(ctx, boundedAliasEntity.Kind, f.alias)
			if err != nil {
				t.Fatalf("bounded Get with alias-named fields: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("bounded Get %#v, ordinary %#v", got, want)
			}
			list, _, err := reader.ListExtensions(ctx, boundedAliasEntity.Kind, model.Query{Limit: 5})
			if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], want) {
				t.Errorf("bounded List rows=%d err=%v", len(list), err)
			}
			return nil
		}); err != nil {
			t.Fatalf("view: %v", err)
		}
	})

	t.Run("F2 renderer and envelope before construction", func(t *testing.T) {
		for _, c := range []struct {
			dimension string
			mutate    func(*store.BoundedReadLimits)
		}{
			{"MaxQueryBytes", func(l *store.BoundedReadLimits) { l.MaxQueryBytes = 4096 }},
			// The renderer fits; only the inspection envelope does not.
			{"MaxPageUnits", func(l *store.BoundedReadLimits) { l.MaxQueryBytes = 1 << 21; l.MaxPageUnits = 2000 }},
		} {
			if err := st.View(ctx, f.tenant, func(sc store.Scope) error {
				limits := boundedTestLimits()
				c.mutate(&limits)
				reader := newBoundedTestReader(t, sc, limits)
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				_, err := reader.GetExtension(ctx, boundedWideEntity.Kind, f.wide)
				runtime.ReadMemStats(&after)
				if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), c.dimension) {
					t.Errorf("%s wide inspection: %v", c.dimension, err)
				}
				u := reader.Usage()
				if !u.Terminal || u.ReservedUnits != firstEnvelope || u.PayloadRowsReserved != 0 {
					t.Errorf("%s refusal usage: %+v (want only the %d-unit representation statement)", c.dimension, u, firstEnvelope)
				}
				// The rendered inspection statement alone exceeds 256 KiB; refusing
				// it must not construct it.
				if delta := after.TotalAlloc - before.TotalAlloc; delta > 256<<10 {
					t.Errorf("%s refusal allocated %d bytes before refusing", c.dimension, delta)
				}
				return nil
			}); err != nil {
				t.Fatalf("view: %v", err)
			}
		}
		if err := st.View(ctx, f.tenant, func(sc store.Scope) error {
			limits := boundedTestLimits()
			limits.MaxQueryBytes = 1 << 21 // the wide inspection and payload statements exceed 64 KiB
			reader := newBoundedTestReader(t, sc, limits)
			rec, err := reader.GetExtension(ctx, boundedWideOKEntity.Kind, f.wideOK)
			if err != nil || len(rec) != 5+boundedWideOKFields {
				t.Errorf("wide descriptor within budget: fields=%d err=%v", len(rec), err)
			}
			return nil
		}); err != nil {
			t.Fatalf("view: %v", err)
		}
	})

	t.Run("F3 byte filter parity", func(t *testing.T) {
		if err := st.View(ctx, f.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(boundedTestEntity.Kind)
			if err != nil {
				return err
			}
			reader := newBoundedTestReader(t, sc, boundedTestLimits())
			for name, value := range map[string]any{
				"nil interface":    nil,
				"typed nil bytes":  []byte(nil),
				"present empty":    []byte{},
				"present nonempty": []byte{0, 1, 2},
			} {
				for _, op := range []model.Op{model.OpEq, model.OpNe} {
					q := model.Query{Limit: 50, Filters: []model.Filter{{Column: "payload", Op: op, Value: value}}}
					ordinary, _, err := repo.List(ctx, q)
					if err != nil {
						return fmt.Errorf("ordinary %s %s: %w", name, op, err)
					}
					bounded, _, err := reader.ListExtensions(ctx, boundedTestEntity.Kind, q)
					if err != nil {
						return fmt.Errorf("bounded %s %s: %w", name, op, err)
					}
					if a, b := boundedIDs(ordinary), boundedIDs(bounded); !reflect.DeepEqual(a, b) {
						t.Errorf("%s %s: ordinary %v bounded %v", name, op, a, b)
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("view: %v", err)
		}
	})
}

func boundedIDs(recs []model.Record) []string {
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.String(model.ColID))
	}
	sort.Strings(out)
	return out
}

func TestBoundedReaderCorrectionSQLite(t *testing.T) {
	st := openSQLiteTest(t, registerBoundedCorrectionEntities)
	f := seedCorrection(t, st, sqliteBytesLiteral)
	// SQLite first statement: the encoding observation, 8 + 8 + 17.
	runCorrectionContract(t, st, f, boundedResultUnits+boundedRowUnits+boundedFixedUnits)
}

func TestBoundedReaderCorrectionPostgres(t *testing.T) {
	pg := isolatedPGSplit(t)
	seed := openCorrectionPG(t, pg, pgExecModeCacheStatement)
	f := seedCorrection(t, seed, postgresBytesLiteral)
	_ = seed.Close()
	for _, mode := range []pgExecModeFact{pgExecModeCacheStatement, pgExecModeSimpleProtocol} {
		t.Run(mode.String(), func(t *testing.T) {
			st := openCorrectionPG(t, pg, mode)
			defer func() { _ = st.Close() }()
			// PostgreSQL first statement: the setting classes, 8 + 8 + 5x17.
			runCorrectionContract(t, st, f, boundedResultUnits+boundedRowUnits+boundedFixedUnits*pgSettingsColumns)
		})
	}
}

// TestBoundedReaderInputAdmissionPrecedesCopies proves the complete caller
// input and forced predicate are sized before any value copy or proportional
// slice: a refused request must not copy an earlier 4 MiB byte value or
// allocate its replacement filter slice.
func TestBoundedReaderInputAdmissionPrecedesCopies(t *testing.T) {
	const bigValue = 4 << 20
	big := make([]byte, bigValue)
	forced := model.Filter{Column: "workspace_id", Op: model.OpEq, Value: strings.Repeat("0", 36)}
	for _, c := range []struct {
		name    string
		filters int
		budget  uint64
		lineage *model.Filter
	}{
		// A later caller filter exhausts the aggregate after the large first value.
		{"later caller filter", 200000, bigValue + 64, nil},
		// Every caller value fits; the forced workspace predicate does not.
		{"forced predicate", 2, bigValue + uint64(len("payload")+len("eq")) + uint64(len("label")+len("eq")+1) + 10, &forced},
	} {
		t.Run(c.name, func(t *testing.T) {
			limits := boundedTestLimits()
			limits.MaxFilters = uint64(c.filters)
			limits.MaxParameterBytes = c.budget
			r := &boundedReader{lim: limits}
			target := boundedTarget{desc: boundedTestEntity, lineage: c.lineage}
			filters := make([]model.Filter, c.filters)
			filters[0] = model.Filter{Column: "payload", Op: model.OpEq, Value: big}
			for i := 1; i < len(filters); i++ {
				filters[i] = model.Filter{Column: "label", Op: model.OpEq, Value: "x"}
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			out, err := r.validateFilters(target, filters, &boundedParams{max: limits.MaxParameterBytes})
			runtime.ReadMemStats(&after)
			if !errors.Is(err, store.ErrBoundedReadLimit) || out != nil {
				t.Fatalf("aggregate refusal: out=%d err=%v", len(out), err)
			}
			if delta := after.TotalAlloc - before.TotalAlloc; delta > 64<<10 {
				t.Errorf("refused input allocated %d bytes before refusal", delta)
			}
		})
	}
}
