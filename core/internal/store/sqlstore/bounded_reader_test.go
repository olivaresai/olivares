// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var boundedTestEntity = model.EntityDescriptor{
	Kind:  "brt.item",
	Table: "brt_item",
	WorkspaceLineage: model.WorkspaceLineageSpec{
		Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden,
	},
	Fields: []model.FieldSpec{
		{Name: "workspace_id", Kind: model.KindUUID},
		{Name: "label", Kind: model.KindText},
		{Name: "note", Kind: model.KindText, Nullable: true},
		{Name: "payload", Kind: model.KindBytes, Nullable: true},
		{Name: "amount", Kind: model.KindInt},
		{Name: "ratio", Kind: model.KindFloat, Nullable: true},
		{Name: "active", Kind: model.KindBool},
		{Name: "doc", Kind: model.KindJSON, Nullable: true},
	},
}

var boundedPlainEntity = model.EntityDescriptor{
	Kind:   "brt.plain",
	Table:  "brt_plain",
	Fields: []model.FieldSpec{{Name: "label", Kind: model.KindText}},
}

func registerBoundedTestEntities(reg store.ExtensionRegistry) error {
	if err := reg.Register(boundedTestEntity); err != nil {
		return err
	}
	return reg.Register(boundedPlainEntity)
}

func boundedTestLimits() store.BoundedReadLimits {
	return store.BoundedReadLimits{
		MaxCellBytes: 1 << 20, MaxRowUnits: 1 << 22, MaxPageUnits: 1 << 26,
		MaxRowsPerPage: 100, MaxRows: 10000, MaxUnits: 1 << 30,
		MaxFilters: 8, MaxParameterBytes: 4096, MaxQueryBytes: 1 << 16,
	}
}

func newBoundedTestReader(t *testing.T, sc store.Scope, limits store.BoundedReadLimits) store.BoundedReader {
	t.Helper()
	factory, ok := sc.(store.BoundedReaderFactory)
	if !ok {
		t.Fatal("scope lacks BoundedReaderFactory")
	}
	reader, err := factory.NewBoundedReader(store.BoundedReadOptions{Limits: limits})
	if err != nil {
		t.Fatalf("new bounded reader: %v", err)
	}
	return reader
}

func seedBoundedItem(ctx context.Context, t *testing.T, sc store.Scope, workspace model.ID, label string) model.ID {
	t.Helper()
	repo, err := sc.Ext(boundedTestEntity.Kind)
	if err != nil {
		t.Fatalf("ext: %v", err)
	}
	rec, err := repo.Create(ctx, model.Record{
		"workspace_id": workspace.String(), "label": label, "amount": int64(len(label)), "active": true,
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	return model.ID(rec.String(model.ColID))
}

func rawBoundedExec(ctx context.Context, t *testing.T, sc store.Scope, query string, args ...any) {
	t.Helper()
	if _, err := sc.(*tenantScope).tx.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("raw exec: %v", err)
	}
}

func assertBoundedUsageUntouched(t *testing.T, reader store.BoundedReader) {
	t.Helper()
	usage := reader.Usage()
	if usage.ReservedUnits != 0 || usage.ObservedUnits != 0 || usage.PayloadRowsReserved != 0 ||
		usage.LookaheadSlotsReserved != 0 || usage.Terminal || !usage.ObservedComplete {
		t.Fatalf("pre-I/O refusal consumed or terminated: %+v", usage)
	}
}

func TestBoundedReaderRejectsLimitsAndRequestsBeforeIO(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "bounded-preio")
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		factory := sc.(store.BoundedReaderFactory)
		base := boundedTestLimits()
		value := reflect.ValueOf(&base).Elem()
		for i := 0; i < value.NumField(); i++ {
			limits := base
			reflect.ValueOf(&limits).Elem().Field(i).SetUint(0)
			if _, err := factory.NewBoundedReader(store.BoundedReadOptions{Limits: limits}); !errors.Is(err, store.ErrInvalidBoundedRead) {
				t.Errorf("zero %s accepted: %v", value.Type().Field(i).Name, err)
			}
		}

		limits := boundedTestLimits()
		limits.MaxFilters = 1
		limits.MaxParameterBytes = 128
		reader := newBoundedTestReader(t, sc, limits)
		kind := boundedTestEntity.Kind
		type stringer struct{ n int }
		for name, call := range map[string]struct {
			err error
			run func() error
		}{
			"zero limit": {store.ErrInvalidBoundedRead, func() error { _, _, err := reader.ListExtensions(ctx, kind, model.Query{}); return err }},
			"sort": {store.ErrInvalidBoundedRead, func() error {
				_, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 1, Sort: []model.Sort{{Column: "label"}}})
				return err
			}},
			"page limit": {store.ErrInvalidBoundedRead, func() error { _, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 101}); return err }},
			"cursor": {store.ErrInvalidBoundedRead, func() error {
				_, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 1, Cursor: "not-an-id"})
				return err
			}},
			"filter count": {store.ErrBoundedReadLimit, func() error {
				_, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 1, Filters: []model.Filter{{Column: "label", Op: model.OpEq, Value: "a"}, {Column: "label", Op: model.OpEq, Value: "b"}}})
				return err
			}},
			"parameter size": {store.ErrBoundedReadLimit, func() error {
				_, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 1, Filters: []model.Filter{{Column: "label", Op: model.OpEq, Value: strings.Repeat("x", 200)}}})
				return err
			}},
			"custom value": {store.ErrInvalidBoundedRead, func() error {
				_, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 1, Filters: []model.Filter{{Column: "label", Op: model.OpEq, Value: stringer{1}}}})
				return err
			}},
			"unknown column": {store.ErrUnknownEntity, func() error {
				_, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 1, Filters: []model.Filter{{Column: "nope", Op: model.OpEq, Value: "a"}}})
				return err
			}},
			"core kind":    {store.ErrUnknownEntity, func() error { _, err := reader.GetExtension(ctx, "core.policy", model.NewID()); return err }},
			"unknown kind": {store.ErrUnknownEntity, func() error { _, err := reader.GetExtension(ctx, "brt.none", model.NewID()); return err }},
			"id":           {store.ErrInvalidBoundedRead, func() error { _, err := reader.GetPolicySnapshot(ctx, "x"); return err }},
		} {
			if err := call.run(); !errors.Is(err, call.err) {
				t.Errorf("%s: err=%v, want %v", name, err, call.err)
			}
		}
		assertBoundedUsageUntouched(t, reader)
		if _, err := reader.GetExtension(ctx, kind, model.NewID()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("reader not reusable after pre-I/O refusals: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestBoundedReaderPolicySnapshotsMatchR0(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "bounded-r0")
	foreign := provisionTenant(t, st, "bounded-r0-foreign")
	specs := []any{nil, "", "null", "a\x00b", strings.Repeat("s", 5000), "{\"z\":1,\"a\":2}", []byte{0xff, 0xfe, 'x'}}
	for _, target := range []model.TenantID{tenant, foreign} {
		if err := st.Mutate(ctx, target, func(sc store.Scope) error {
			for i, spec := range specs {
				if _, err := plantRawPolicy(ctx, sc, "p"+string(rune('a'+i)), spec); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		want, _, err := policySnapshots(t, sc).ListPolicySnapshots(ctx, model.Query{Limit: 1000})
		if err != nil {
			return err
		}
		reader := newBoundedTestReader(t, sc, boundedTestLimits())
		var got []store.PolicySnapshot
		cursor, pages := "", 0
		for {
			page, next, err := reader.ListPolicySnapshots(ctx, model.Query{Limit: 2, Cursor: cursor})
			if err != nil {
				return err
			}
			pages++
			got = append(got, page...)
			if !next.HasMore {
				if next.Cursor != "" {
					t.Errorf("final page carries cursor")
				}
				break
			}
			cursor = next.Cursor
		}
		if len(got) != len(specs) || !reflect.DeepEqual(got, want) {
			t.Fatalf("bounded traversal differs from R0: got %d want %d", len(got), len(want))
		}
		for _, snapshot := range want {
			one, err := reader.GetPolicySnapshot(ctx, snapshot.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(one, snapshot) {
				t.Errorf("bounded Get differs from R0 for one fixture")
			}
		}
		usage := reader.Usage()
		if !usage.ObservedComplete || usage.Terminal || usage.ObservedUnits > usage.ReservedUnits ||
			usage.PayloadRowsObserved != uint64(2*len(specs)) || usage.PayloadRowsReserved != uint64(2*len(specs)) ||
			usage.LookaheadRowsObserved != uint64(pages-1) || usage.LookaheadSlotsReserved != uint64(pages) {
			t.Errorf("usage: %+v pages=%d", usage, pages)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestBoundedReaderExtensionScalarsNullAndEmpty(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "bounded-scalars")
	var nullID, emptyID, fullID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		nullID = seedBoundedItem(ctx, t, sc, ws.ID, "null")
		emptyID = seedBoundedItem(ctx, t, sc, ws.ID, "empty")
		fullID = seedBoundedItem(ctx, t, sc, ws.ID, "full")
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = NULL, payload = NULL, ratio = NULL, doc = NULL WHERE id = ?", nullID.String())
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = '', payload = X'', ratio = 1.5, doc = '{}', active = 0 WHERE id = ?", emptyID.String())
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET note = ?, payload = X'000102', ratio = 2, amount = -9223372036854775808 WHERE id = ?", "x\x00y", fullID.String())
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		reader := newBoundedTestReader(t, sc, boundedTestLimits())
		get := func(id model.ID) model.Record {
			rec, err := reader.GetExtension(ctx, boundedTestEntity.Kind, id)
			if err != nil {
				t.Fatalf("bounded get: %v", err)
			}
			return rec
		}
		null := get(nullID)
		for _, col := range []string{"note", "payload", "ratio", "doc"} {
			if v, ok := null[col]; !ok || v != nil {
				t.Errorf("NULL %s = %#v present=%v", col, v, ok)
			}
		}
		empty := get(emptyID)
		if b, ok := empty["payload"].([]byte); !ok || b == nil || len(b) != 0 {
			t.Errorf("present empty BLOB = %#v", empty["payload"])
		}
		if empty["note"] != "" || empty["ratio"] != 1.5 || empty["doc"] != "{}" || empty["active"] != false {
			t.Errorf("empty row = %#v", empty)
		}
		full := get(fullID)
		if !reflect.DeepEqual(full["payload"], []byte{0, 1, 2}) || full["note"] != "x\x00y" ||
			full["ratio"] != float64(2) || full["amount"] != int64(math.MinInt64) || full["active"] != true {
			t.Errorf("full row = %#v", full)
		}
		// Every non-bytes value equals the ordinary repository's normalization.
		repo, _ := sc.Ext(boundedTestEntity.Kind)
		generic, err := repo.Get(ctx, fullID)
		if err != nil {
			return err
		}
		for col, v := range generic {
			if !reflect.DeepEqual(full[col], v) {
				t.Errorf("column %s: bounded %#v generic %#v", col, full[col], v)
			}
		}
		if len(full) != len(generic) {
			t.Errorf("record arity %d, generic %d", len(full), len(generic))
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestBoundedReaderOversizeRowRefusedBeforePayload(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "bounded-oversize")
	var small, big model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if small, err = plantRawPolicy(ctx, sc, "small", "{}"); err != nil {
			return err
		}
		big, err = plantRawPolicy(ctx, sc, "big", strings.Repeat("b", 1000))
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	limits := boundedTestLimits()
	limits.MaxCellBytes = 500
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		reader := newBoundedTestReader(t, sc, limits)
		if _, err := reader.GetPolicySnapshot(ctx, small.ID); err != nil {
			t.Fatalf("small row: %v", err)
		}
		_, err := reader.GetPolicySnapshot(ctx, big.ID)
		if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "MaxCellBytes") {
			t.Fatalf("oversize err = %v", err)
		}
		usage := reader.Usage()
		if usage.PayloadRowsReserved != 1 || usage.PayloadRowsObserved != 1 || !usage.Terminal {
			t.Errorf("oversize row reached payload or left reader live: %+v", usage)
		}
		_, err = reader.GetPolicySnapshot(ctx, small.ID)
		if !errors.Is(err, store.ErrBoundedReadTerminal) || !errors.Is(err, store.ErrBoundedReadLimit) {
			t.Errorf("terminal reader err = %v", err)
		}

		// One oversized row in a page refuses the whole page before any payload.
		pageReader := newBoundedTestReader(t, sc, limits)
		got, _, err := pageReader.ListPolicySnapshots(ctx, model.Query{Limit: 10})
		if !errors.Is(err, store.ErrBoundedReadLimit) || got != nil {
			t.Fatalf("page with oversize row: rows=%d err=%v", len(got), err)
		}
		if u := pageReader.Usage(); u.PayloadRowsReserved != 0 || u.PayloadRowsObserved != 0 {
			t.Errorf("page payload started: %+v", u)
		}

		rowLimits := boundedTestLimits()
		rowLimits.MaxRowUnits = 200
		rowReader := newBoundedTestReader(t, sc, rowLimits)
		if _, err := rowReader.GetPolicySnapshot(ctx, big.ID); !errors.Is(err, store.ErrBoundedReadLimit) ||
			!strings.Contains(err.Error(), "MaxRowUnits") {
			t.Errorf("row limit err = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// The payload projection itself refuses a row that no longer fits its
// admission: forcing the admitted length one octet below the stored value
// must yield a rejection flag and no value, not a partial Record.
func TestBoundedReaderPayloadProjectionRejectsRowBeyondAdmission(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "bounded-rowok")
	var policy model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		policy, err = plantRawPolicy(ctx, sc, "rowok", strings.Repeat("r", 64))
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		reader := newBoundedTestReader(t, sc, boundedTestLimits()).(*boundedReader)
		call, err := reader.begin()
		if err != nil {
			return err
		}
		defer func() { _ = call.finish(nil) }()
		target, _ := reader.policyTarget()
		enc, err := call.textEncoding(ctx)
		if err != nil {
			return err
		}
		where, args, err := target.where(reader.sc.tenant, nil, false)
		if err != nil {
			return err
		}
		row, found, err := call.inspect(ctx, target, where, args, policy.ID.String(), enc)
		if err != nil || !found {
			t.Fatalf("inspect: found=%v err=%v", found, err)
		}
		admissions := row.admissions
		for i := range admissions {
			if admissions[i].column == "spec" {
				admissions[i].octets--
			}
		}
		before := reader.Usage().ObservedUnits
		rec, err := call.load(ctx, target, boundedPayload{
			relation: target.sql.relation(), where: where, id: policy.ID.String(), args: args, row: row,
		})
		if !errors.Is(err, store.ErrBoundedReadConsistency) || rec != nil {
			t.Fatalf("row beyond admission: rec=%v err=%v", rec != nil, err)
		}
		rejectedShape := boundedRowUnits + boundedFixedUnits + boundedNullUnits*uint64(len(admissions)) +
			boundedFixedUnits*uint64(countNullable(admissions))
		if got := reader.Usage().ObservedUnits - before; got != boundedResultUnits+rejectedShape {
			t.Errorf("rejected row observed %d units, want content-free %d", got, boundedResultUnits+rejectedShape)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

func countNullable(admissions []boundedAdmission) int {
	n := 0
	for _, a := range admissions {
		if a.nullable {
			n++
		}
	}
	return n
}

func TestBoundedReaderPageAndTraversalExhaustion(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "bounded-exhaust")
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		for i := 0; i < 5; i++ {
			seedBoundedItem(ctx, t, sc, ws.ID, "row"+string(rune('a'+i)))
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	kind := boundedTestEntity.Kind
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rowsLimits := boundedTestLimits()
		rowsLimits.MaxRows = 3
		reader := newBoundedTestReader(t, sc, rowsLimits)
		_, page, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 2})
		if err != nil || !page.HasMore {
			t.Fatalf("first page: more=%v err=%v", page.HasMore, err)
		}
		_, _, err = reader.ListExtensions(ctx, kind, model.Query{Limit: 2, Cursor: page.Cursor})
		if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "MaxRows") {
			t.Fatalf("MaxRows err = %v", err)
		}
		if u := reader.Usage(); u.PayloadRowsReserved != 2 || u.RemainingRows != 1 || !u.Terminal {
			t.Errorf("MaxRows usage: %+v", u)
		}

		probe := newBoundedTestReader(t, sc, boundedTestLimits())
		if _, _, err := probe.ListExtensions(ctx, kind, model.Query{Limit: 2}); err != nil {
			return err
		}
		used := probe.Usage().ReservedUnits
		unitLimits := boundedTestLimits()
		unitLimits.MaxUnits = used + used/2
		units := newBoundedTestReader(t, sc, unitLimits)
		if _, _, err := units.ListExtensions(ctx, kind, model.Query{Limit: 2}); err != nil {
			t.Fatalf("within MaxUnits: %v", err)
		}
		_, _, err = units.ListExtensions(ctx, kind, model.Query{Limit: 2})
		if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "MaxUnits") {
			t.Fatalf("MaxUnits err = %v", err)
		}
		if u := units.Usage(); u.ReservedUnits > unitLimits.MaxUnits || !u.Terminal {
			t.Errorf("MaxUnits usage: %+v", u)
		}

		// A first envelope that does not fit is terminal and charges nothing.
		for name, mutate := range map[string]func(*store.BoundedReadLimits){
			"MaxPageUnits":  func(l *store.BoundedReadLimits) { l.MaxPageUnits = 20 },
			"MaxQueryBytes": func(l *store.BoundedReadLimits) { l.MaxQueryBytes = 10 },
		} {
			limits := boundedTestLimits()
			mutate(&limits)
			first := newBoundedTestReader(t, sc, limits)
			_, _, err := first.ListExtensions(ctx, kind, model.Query{Limit: 2})
			if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), name) {
				t.Errorf("%s first envelope err = %v", name, err)
			}
			u := first.Usage()
			if !u.Terminal || u.ReservedUnits != 0 || u.ObservedUnits != 0 || u.PayloadRowsReserved != 0 {
				t.Errorf("%s first envelope usage: %+v", name, u)
			}
		}
		// A key bound above MaxCellBytes is refused after the budgeted encoding
		// observation (8 + 8 + 17 units) and before key selection.
		cellLimits := boundedTestLimits()
		cellLimits.MaxCellBytes = 10
		cell := newBoundedTestReader(t, sc, cellLimits)
		_, _, err = cell.ListExtensions(ctx, kind, model.Query{Limit: 2})
		if !errors.Is(err, store.ErrBoundedReadLimit) || !strings.Contains(err.Error(), "MaxCellBytes") {
			t.Errorf("MaxCellBytes key err = %v", err)
		}
		if u := cell.Usage(); !u.Terminal || u.ReservedUnits != 33 || u.LookaheadSlotsReserved != 0 || u.PayloadRowsReserved != 0 {
			t.Errorf("MaxCellBytes key usage: %+v", u)
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestBoundedReaderArithmeticIsChecked(t *testing.T) {
	if _, ok := boundedEnvelope(math.MaxUint64, []uint64{1}); ok {
		t.Error("envelope row product overflow accepted")
	}
	if _, ok := boundedEnvelope(1, []uint64{math.MaxUint64}); ok {
		t.Error("envelope column sum overflow accepted")
	}
	if _, ok := boundedEnvelope(1, []uint64{math.MaxUint64 - boundedRowUnits - boundedResultUnits + 1}); ok {
		t.Error("envelope result addition overflow accepted")
	}
	if _, ok := mulBounded(math.MaxUint64/2+1, 2); ok {
		t.Error("multiplier overflow accepted")
	}
	limits := boundedTestLimits()
	limits.MaxCellBytes = math.MaxUint64
	call := &boundedCall{r: &boundedReader{lim: limits}}
	a := boundedAdmission{column: "c", kind: model.KindText, class: sqliteClassText, octets: math.MaxUint64/2 + 1}
	if _, err := call.admit(&a, false, sqliteEncodingUTF16LE); !errors.Is(err, store.ErrBoundedReadLimit) {
		t.Errorf("UTF-16 cell bound overflow err = %v", err)
	}
	a = boundedAdmission{column: "c", kind: model.KindText, class: sqliteClassText, octets: math.MaxUint64 - 4}
	if _, err := call.admit(&a, false, sqliteEncodingUTF8); !errors.Is(err, store.ErrBoundedReadLimit) {
		t.Errorf("cell charge overflow err = %v", err)
	}
	// The ratified UTF-16 multiplier doubles the admitted TEXT bound; BLOB is not multiplied.
	call.r.lim.MaxCellBytes = 1 << 20
	text := boundedAdmission{column: "t", kind: model.KindText, class: sqliteClassText, octets: 10}
	blob := boundedAdmission{column: "b", kind: model.KindBytes, class: sqliteClassBlob, octets: 10}
	textCharge, _ := call.admit(&text, false, sqliteEncodingUTF16BE)
	blobCharge, _ := call.admit(&blob, false, sqliteEncodingUTF16BE)
	if text.bound != 20 || textCharge != 29 || blob.bound != 10 || blobCharge != 19 {
		t.Errorf("UTF-16 bounds: text %d/%d blob %d/%d", text.bound, textCharge, blob.bound, blobCharge)
	}
	reserve := &boundedCall{r: &boundedReader{lim: store.BoundedReadLimits{MaxPageUnits: math.MaxUint64, MaxUnits: math.MaxUint64, MaxRows: 1}}}
	reserve.pageUnits = math.MaxUint64
	if err := reserve.reserve(1, 0, 0, "x"); !errors.Is(err, store.ErrBoundedReadLimit) {
		t.Errorf("page accumulation overflow err = %v", err)
	}
}

func TestBoundedReaderStableViewTraversal(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "bounded-stable")
	var ws model.ID
	var want []string
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		def, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		ws = def.ID
		for i := 0; i < 7; i++ {
			want = append(want, seedBoundedItem(ctx, t, sc, ws, "stable").String())
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var got []string
	committed := make(chan error, 1)
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		reader := newBoundedTestReader(t, sc, boundedTestLimits())
		cursor := ""
		for page := 0; ; page++ {
			recs, next, err := reader.ListExtensions(ctx, boundedTestEntity.Kind, model.Query{Limit: 3, Cursor: cursor})
			if err != nil {
				return err
			}
			for _, rec := range recs {
				got = append(got, rec.String(model.ColID))
			}
			if page == 0 {
				go func() {
					committed <- st.Mutate(ctx, tenant, func(w store.Scope) error {
						repo, err := w.Ext(boundedTestEntity.Kind)
						if err != nil {
							return err
						}
						for _, id := range want {
							rec, err := repo.Get(ctx, model.ID(id))
							if err != nil {
								return err
							}
							rec["label"] = strings.Repeat("grown", 50)
							if _, err := repo.Update(ctx, rec); err != nil {
								return err
							}
						}
						_, err = repo.Create(ctx, model.Record{"workspace_id": ws.String(), "label": "late", "amount": int64(1), "active": true})
						return err
					})
				}()
				select {
				case err := <-committed:
					if err != nil {
						t.Logf("concurrent writer failed during View: %v", err)
					}
					committed <- err
				case <-time.After(3 * time.Second):
					t.Log("concurrent writer did not commit during the View")
				}
			}
			for _, rec := range recs {
				if rec.String("label") != "stable" {
					t.Errorf("View observed a later version")
				}
			}
			if !next.HasMore {
				break
			}
			cursor = next.Cursor
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("traversal %v, want %v", got, want)
	}
	select {
	case err := <-committed:
		if err != nil {
			t.Fatalf("writer: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("writer never completed")
	}
}

func TestBoundedReaderTerminalAndNotFoundStates(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "bounded-terminal")
	var id model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		id = seedBoundedItem(ctx, t, sc, ws.ID, "terminal")
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var escaped store.BoundedReader
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		reader := newBoundedTestReader(t, sc, boundedTestLimits())
		if _, err := reader.GetExtension(ctx, boundedTestEntity.Kind, model.NewID()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("absent: %v", err)
		}
		u := reader.Usage()
		if u.Terminal || u.ReservedUnits == 0 || u.PayloadRowsReserved != 0 {
			t.Errorf("NotFound usage: %+v", u)
		}
		if _, err := reader.GetExtension(ctx, boundedTestEntity.Kind, id); err != nil {
			t.Fatalf("after NotFound: %v", err)
		}
		if _, _, err := reader.ListExtensions(ctx, boundedTestEntity.Kind, model.Query{Limit: 5}); err != nil {
			t.Fatalf("Mutate list: %v", err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		cancelReader := newBoundedTestReader(t, sc, boundedTestLimits())
		_, err := cancelReader.GetExtension(cancelled, boundedTestEntity.Kind, id)
		if !errors.Is(err, context.Canceled) || !cancelReader.Usage().Terminal || cancelReader.Usage().ObservedComplete {
			t.Errorf("cancelled: err=%v usage=%+v", err, cancelReader.Usage())
		}
		if _, err := cancelReader.GetExtension(ctx, boundedTestEntity.Kind, id); !errors.Is(err, store.ErrBoundedReadTerminal) ||
			!errors.Is(err, context.Canceled) {
			t.Errorf("terminal cause not retained: %v", err)
		}
		escaped = reader
		return nil
	}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	_, err := escaped.GetExtension(ctx, boundedTestEntity.Kind, id)
	if err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reader outlived its Scope: %v", err)
	}
	if _, err := escaped.GetExtension(ctx, boundedTestEntity.Kind, id); !errors.Is(err, store.ErrBoundedReadTerminal) {
		t.Errorf("closed transaction did not terminate the reader: %v", err)
	}
}

func TestBoundedReaderConcurrentCallRefusal(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "bounded-busy")
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		reader := newBoundedTestReader(t, sc, boundedTestLimits()).(*boundedReader)
		reader.busy.Store(true)
		if _, err := reader.GetExtension(ctx, boundedTestEntity.Kind, model.NewID()); !errors.Is(err, store.ErrBoundedReadConcurrent) {
			t.Fatalf("busy err = %v", err)
		}
		if !reader.busy.Load() {
			t.Fatal("refused call released the active call's gate")
		}
		assertBoundedUsageUntouched(t, reader)
		reader.busy.Store(false)

		// Usage snapshots race only with counters, never with content.
		var wg sync.WaitGroup
		stop := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = reader.Usage()
				}
			}
		}()
		for i := 0; i < 20; i++ {
			if _, _, err := reader.ListExtensions(ctx, boundedTestEntity.Kind, model.Query{Limit: 5}); err != nil {
				close(stop)
				wg.Wait()
				return err
			}
		}
		close(stop)
		wg.Wait()
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestBoundedReaderWorkspaceConfinement(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, registerBoundedTestEntities)
	tenant := provisionTenant(t, st, "bounded-confined")
	defaultWS, otherWS := distinctProjectionWorkspaces(t, st, tenant)
	var own, foreign, wrongType model.ID
	var policy model.Policy
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		own = seedBoundedItem(ctx, t, sc, otherWS, "own")
		foreign = seedBoundedItem(ctx, t, sc, defaultWS, "foreign")
		wrongType = seedBoundedItem(ctx, t, sc, otherWS, "wrong-type")
		rawBoundedExec(ctx, t, sc, "UPDATE brt_item SET workspace_id = CAST(? AS BLOB) WHERE id = ?", otherWS.String(), wrongType.String())
		plain, err := sc.Ext(boundedPlainEntity.Kind)
		if err != nil {
			return err
		}
		if _, err := plain.Create(ctx, model.Record{"label": "plain"}); err != nil {
			return err
		}
		policy, err = plantRawPolicy(ctx, sc, "confined", "{}")
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.View(ctx, tenant, func(raw store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, raw, otherWS)
		if err != nil {
			return err
		}
		reader := newBoundedTestReader(t, confined, boundedTestLimits())
		kind := boundedTestEntity.Kind
		if _, err := reader.GetPolicySnapshot(ctx, policy.ID); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Errorf("confined policy Get: %v", err)
		}
		if _, _, err := reader.ListPolicySnapshots(ctx, model.Query{Limit: 5}); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Errorf("confined policy List: %v", err)
		}
		if _, err := reader.GetExtension(ctx, boundedPlainEntity.Kind, model.NewID()); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
			t.Errorf("confined lineage-less Ext: %v", err)
		}
		if _, err := reader.GetExtension(ctx, "core.policy", policy.ID); !errors.Is(err, store.ErrUnknownEntity) {
			t.Errorf("confined core Ext: %v", err)
		}
		assertBoundedUsageUntouched(t, reader)

		if _, err := reader.GetExtension(ctx, kind, own); err != nil {
			t.Errorf("own row: %v", err)
		}
		for name, id := range map[string]model.ID{"foreign": foreign, "wrong-type lineage": wrongType, "absent": model.NewID()} {
			if _, err := reader.GetExtension(ctx, kind, id); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s Get: %v", name, err)
			}
		}
		recs, _, err := reader.ListExtensions(ctx, kind, model.Query{Limit: 10, Filters: []model.Filter{{
			Column: "workspace_id", Op: model.OpEq, Value: defaultWS.String(),
		}}})
		if err != nil {
			return err
		}
		if len(recs) != 1 || recs[0].String(model.ColID) != own.String() {
			t.Errorf("confined List returned %d rows", len(recs))
		}
		if reader.Usage().Terminal {
			t.Error("confined NotFound terminated the reader")
		}

		// The unconfined reader over the same Scope still sees every row.
		open := newBoundedTestReader(t, raw, boundedTestLimits())
		all, _, err := open.ListExtensions(ctx, kind, model.Query{Limit: 10})
		if err != nil {
			return err
		}
		// Unconfined, the BLOB-stored lineage is an admissible text-kind value
		// (as R0's scan would return it); only the authority predicate hides it.
		if len(all) != 3 {
			t.Errorf("unconfined rows=%d, want 3", len(all))
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestBoundedReaderNotPromotedToNamedWrappers(t *testing.T) {
	ctx := context.Background()
	s, _, _, tenants := f2aFreshTarget(t, store.EngineSQLite)
	if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, ok := as.(store.BoundedReaderFactory); ok {
			t.Error("AuthScope gained the bounded-read factory")
		}
		return nil
	}); err != nil {
		t.Fatalf("auth mutate: %v", err)
	}
	if err := s.Custody(ctx, tenants[0], func(cs store.CustodyScope) error {
		if _, ok := cs.(store.BoundedReaderFactory); ok {
			t.Error("CustodyScope gained the bounded-read factory")
		}
		return nil
	}); err != nil {
		t.Fatalf("custody: %v", err)
	}
	if err := s.View(ctx, tenants[0], func(sc store.Scope) error {
		narrow := struct{ store.Scope }{sc}
		if _, ok := any(narrow).(store.BoundedReaderFactory); ok {
			t.Error("an embedded-Scope wrapper gained the bounded-read factory")
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}
