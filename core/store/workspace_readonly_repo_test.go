// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// roRecorder is the raw repository's observable side: every delegated call is
// noted, so a refused write can be proven never to have reached it.
type roRecorder struct {
	desc        model.EntityDescriptor
	rows        map[model.ID]model.Record
	calls       []string
	queries     []model.Query
	projections []DistinctProjection
}

func (r *roRecorder) note(call string) { r.calls = append(r.calls, call) }

type roRawBase struct{ rec *roRecorder }

func (b roRawBase) Descriptor() model.EntityDescriptor { b.rec.note("Descriptor"); return b.rec.desc }
func (b roRawBase) Get(_ context.Context, id model.ID) (model.Record, error) {
	b.rec.note("Get")
	row, ok := b.rec.rows[id]
	if !ok {
		return nil, ErrNotFound
	}
	return row, nil
}
func (b roRawBase) List(_ context.Context, q model.Query) ([]model.Record, model.Page, error) {
	b.rec.note("List")
	b.rec.queries = append(b.rec.queries, q)
	return nil, model.Page{}, nil
}
func (b roRawBase) Create(context.Context, model.Record) (model.Record, error) {
	b.rec.note("Create")
	return model.Record{}, nil
}
func (b roRawBase) CreateWithID(context.Context, model.ID, model.Record) (model.Record, error) {
	b.rec.note("CreateWithID")
	return model.Record{}, nil
}
func (b roRawBase) Update(context.Context, model.Record) (model.Record, error) {
	b.rec.note("Update")
	return model.Record{}, nil
}
func (b roRawBase) Delete(context.Context, model.ID) error { b.rec.note("Delete"); return nil }

type roRawStamped struct{ rec *roRecorder }

func (s roRawStamped) CreateAtTransactionTime(context.Context, model.Record) (model.Record, error) {
	s.rec.note("CreateAtTransactionTime")
	return model.Record{}, nil
}
func (s roRawStamped) CreateWithIDAtTransactionTime(
	context.Context, model.ID, model.Record,
) (model.Record, error) {
	s.rec.note("CreateWithIDAtTransactionTime")
	return model.Record{}, nil
}
func (s roRawStamped) UpdateAtTransactionTime(context.Context, model.Record) (model.Record, error) {
	s.rec.note("UpdateAtTransactionTime")
	return model.Record{}, nil
}

type roRawLocker struct{ rec *roRecorder }

func (l roRawLocker) Lock(context.Context, model.ID) (model.Record, error) {
	l.rec.note("Lock")
	return model.Record{}, nil
}

type roRawProjector struct{ rec *roRecorder }

func (p roRawProjector) ProjectDistinct(_ context.Context, proj DistinctProjection) (DistinctPage, error) {
	p.rec.note("ProjectDistinct")
	p.rec.projections = append(p.rec.projections, proj)
	return DistinctPage{}, nil
}

// The eight raw capability combinations (stamped × row lock × projector).
type (
	roRaw  struct{ roRawBase }
	roRawS struct {
		roRawBase
		roRawStamped
	}
	roRawL struct {
		roRawBase
		roRawLocker
	}
	roRawP struct {
		roRawBase
		roRawProjector
	}
	roRawSL struct {
		roRawBase
		roRawStamped
		roRawLocker
	}
	roRawSP struct {
		roRawBase
		roRawStamped
		roRawProjector
	}
	roRawLP struct {
		roRawBase
		roRawLocker
		roRawProjector
	}
	roRawSLP struct {
		roRawBase
		roRawStamped
		roRawLocker
		roRawProjector
	}
)

type roCombo struct {
	name                       string
	raw                        GenericRepo
	stamped, locker, projector bool
}

func roCombos(rec *roRecorder) []roCombo {
	b, s, l, p := roRawBase{rec}, roRawStamped{rec}, roRawLocker{rec}, roRawProjector{rec}
	return []roCombo{
		{name: "plain", raw: roRaw{b}},
		{name: "stamped", raw: roRawS{b, s}, stamped: true},
		{name: "locker", raw: roRawL{b, l}, locker: true},
		{name: "projector", raw: roRawP{b, p}, projector: true},
		{name: "stamped+locker", raw: roRawSL{b, s, l}, stamped: true, locker: true},
		{name: "stamped+projector", raw: roRawSP{b, s, p}, stamped: true, projector: true},
		{name: "locker+projector", raw: roRawLP{b, l, p}, locker: true, projector: true},
		{name: "stamped+locker+projector", raw: roRawSLP{b, s, l, p}, stamped: true, locker: true, projector: true},
	}
}

// roExtScope is a Scope whose only reachable member is Ext; the confined Ext
// under test calls nothing else on its raw scope.
type roExtScope struct {
	Scope
	repo GenericRepo
}

func (s roExtScope) Ext(model.Kind) (GenericRepo, error) { return s.repo, nil }

const roKind model.Kind = "rrw.readonly_identity"

func roDescriptor(readOnly bool) model.EntityDescriptor {
	return model.EntityDescriptor{
		Kind:  roKind,
		Table: "rrw_readonly_identity",
		Fields: []model.FieldSpec{
			{Name: "sid", Kind: model.KindText},
			{Name: "workspace_id", Kind: model.KindUUID, Nullable: true},
		},
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column: "workspace_id", Encoding: model.WorkspaceLineageID,
			Unset: model.WorkspaceUnsetMeansDefault,
		},
		WorkspaceConfinedReadOnly: readOnly,
	}
}

type capabilities struct{ stamped, locker, projector bool }

func capabilitiesOf(repo GenericRepo) capabilities {
	_, stamped := repo.(TransactionStampedGenericRepo)
	_, locker := repo.(RowLocker[model.Record])
	_, projector := repo.(DistinctProjector)
	return capabilities{stamped: stamped, locker: locker, projector: projector}
}

// assertNoEmbeddedHandle walks the concrete wrapper: the only embedded field it
// may carry is the read-only reader itself, and nothing below that may be
// embedded. A raw or writable repository reachable by embedding would promote
// its methods.
func assertNoEmbeddedHandle(t *testing.T, repo GenericRepo) {
	t.Helper()
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.Anonymous {
				continue
			}
			if field.Type != reflect.TypeOf(confinedReadOnlyGenericRepo{}) {
				t.Fatalf("%s embeds %s", typ, field.Type)
			}
			walk(field.Type)
		}
	}
	walk(reflect.TypeOf(repo))
}

func TestConfinedReadOnlyExtCensusesEveryCapabilityCombination(t *testing.T) {
	ctx := context.Background()
	defaultWS, alpha, bravo := model.NewID(), model.NewID(), model.NewID()
	boundaries := []struct {
		name string
		b    workspaceBoundary
	}{
		{"default", workspaceBoundary{id: defaultWS, slug: "default", defaultID: defaultWS}},
		{"non-default", workspaceBoundary{id: alpha, slug: "alpha", defaultID: defaultWS}},
	}
	for _, boundary := range boundaries {
		for _, probe := range roCombos(&roRecorder{}) {
			t.Run(boundary.name+"/"+probe.name, func(t *testing.T) {
				rec := &roRecorder{desc: roDescriptor(true), rows: map[model.ID]model.Record{}}
				combo := roCombos(rec)[indexOfCombo(t, probe.name)]
				if got := capabilitiesOf(combo.raw); got != (capabilities{combo.stamped, combo.locker, combo.projector}) {
					t.Fatalf("raw fake capabilities = %+v, census row says %+v", got, combo)
				}
				scope := &workspaceConfinedScope{raw: roExtScope{repo: combo.raw}, b: boundary.b}
				repo, err := scope.Ext(roKind)
				if err != nil {
					t.Fatalf("confined read-only Ext: %v", err)
				}
				if got := capabilitiesOf(repo); got != (capabilities{projector: combo.projector}) {
					t.Fatalf("confined capabilities = %+v, want only projector=%t", got, combo.projector)
				}
				for name, present := range map[string]bool{
					"CreateAtTransactionTime":       hasMethod(repo, "CreateAtTransactionTime"),
					"CreateWithIDAtTransactionTime": hasMethod(repo, "CreateWithIDAtTransactionTime"),
					"UpdateAtTransactionTime":       hasMethod(repo, "UpdateAtTransactionTime"),
					"Lock":                          hasMethod(repo, "Lock"),
				} {
					if present {
						t.Fatalf("withheld method %s is reachable on %T", name, repo)
					}
				}
				wantType := reflect.TypeOf(confinedReadOnlyGenericRepo{})
				if combo.projector {
					wantType = reflect.TypeOf(confinedReadOnlyDistinctProjectingGenericRepo{})
				}
				if reflect.TypeOf(repo) != wantType {
					t.Fatalf("confined repo type = %T, want %s", repo, wantType)
				}
				assertNoEmbeddedHandle(t, repo)

				// Required writes refuse before delegation, for a row inside the
				// boundary and for the cross-workspace rewrite alike.
				rec.calls = nil
				inside := model.Record{"sid": "osn_inside", "workspace_id": boundary.b.id.String()}
				foreignID := model.NewID()
				forged := model.Record{
					model.ColID: foreignID.String(), model.ColVersion: int64(3),
					"sid": "osn_foreign", "workspace_id": boundary.b.id.String(),
				}
				writes := map[string]error{}
				_, writes["create"] = repo.Create(ctx, inside)
				_, writes["create with id"] = repo.CreateWithID(ctx, model.NewID(), inside)
				_, writes["update own"] = repo.Update(ctx, inside)
				_, writes["update forged"] = repo.Update(ctx, forged)
				writes["delete"] = repo.Delete(ctx, foreignID)
				for name, err := range writes {
					if !errors.Is(err, ErrWorkspaceConfinement) || errors.Is(err, ErrNotFound) {
						t.Fatalf("%s error = %v, want ErrWorkspaceConfinement only", name, err)
					}
				}
				if len(rec.calls) != 0 {
					t.Fatalf("refused writes reached the raw repository: %v", rec.calls)
				}

				// Reads delegate with the lineage forced over any caller predicate.
				forcedOp := model.OpEq
				if boundary.b.isDefault() {
					forcedOp = model.OpEqOrUnset
				}
				forced := model.Filter{Column: "workspace_id", Op: forcedOp, Value: boundary.b.id.String()}
				other := model.Filter{Column: "sid", Op: model.OpEq, Value: "osn_x"}
				callerWorkspace := model.Filter{Column: "workspace_id", Op: model.OpEq, Value: bravo.String()}
				if _, _, err := repo.List(ctx, model.Query{
					Filters: []model.Filter{callerWorkspace, other}, Limit: 5,
				}); err != nil {
					t.Fatalf("List: %v", err)
				}
				wantFilters := []model.Filter{other, forced}
				if len(rec.queries) != 1 || !reflect.DeepEqual(rec.queries[0].Filters, wantFilters) ||
					rec.queries[0].Limit != 5 {
					t.Fatalf("List delegated %+v, want filters %+v limit 5", rec.queries, wantFilters)
				}
				if projector, ok := repo.(DistinctProjector); ok {
					if _, err := projector.ProjectDistinct(ctx, DistinctProjection{
						Column: "sid", Filters: []model.Filter{callerWorkspace, other}, Limit: 7,
					}); err != nil {
						t.Fatalf("ProjectDistinct: %v", err)
					}
					if len(rec.projections) != 1 ||
						!reflect.DeepEqual(rec.projections[0].Filters, wantFilters) ||
						rec.projections[0].Column != "sid" || rec.projections[0].Limit != 7 {
						t.Fatalf("ProjectDistinct delegated %+v, want filters %+v", rec.projections, wantFilters)
					}
				}
				if got := repo.Descriptor(); !got.WorkspaceConfinedReadOnly || got.Kind != roKind {
					t.Fatalf("Descriptor = %+v", got)
				}
			})
		}
	}
}

func indexOfCombo(t *testing.T, name string) int {
	t.Helper()
	for i, combo := range roCombos(&roRecorder{}) {
		if combo.name == name {
			return i
		}
	}
	t.Fatalf("unknown combo %q", name)
	return -1
}

func hasMethod(v any, name string) bool {
	_, ok := reflect.TypeOf(v).MethodByName(name)
	return ok
}

func TestConfinedReadOnlyGetAppliesLineageCases(t *testing.T) {
	ctx := context.Background()
	defaultWS, alpha, bravo := model.NewID(), model.NewID(), model.NewID()
	rows := map[string]string{
		"alpha":     alpha.String(),
		"bravo":     bravo.String(),
		"unset":     "",
		"default":   defaultWS.String(),
		"malformed": "not-a-workspace",
		"zero":      model.ID("00000000-0000-0000-0000-000000000000").String(),
	}
	ids := map[string]model.ID{}
	rec := &roRecorder{desc: roDescriptor(true), rows: map[model.ID]model.Record{}}
	for name, value := range rows {
		id := model.NewID()
		ids[name] = id
		row := model.Record{model.ColID: id.String(), "sid": "osn_" + name}
		if value != "" {
			row["workspace_id"] = value
		}
		rec.rows[id] = row
	}
	const (
		visible = "visible"
		hidden  = "hidden"
		fault   = "fault"
	)
	cases := []struct {
		name string
		b    workspaceBoundary
		want map[string]string
	}{
		{
			name: "confined to a non-default workspace",
			b:    workspaceBoundary{id: alpha, slug: "alpha", defaultID: defaultWS},
			want: map[string]string{
				"alpha": visible, "bravo": hidden, "unset": hidden, "default": hidden,
				"malformed": fault, "zero": fault,
			},
		},
		{
			name: "confined to the default workspace",
			b:    workspaceBoundary{id: defaultWS, slug: "default", defaultID: defaultWS},
			want: map[string]string{
				"alpha": hidden, "bravo": hidden, "unset": visible, "default": visible,
				"malformed": fault, "zero": fault,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scope := &workspaceConfinedScope{raw: roExtScope{repo: roRawSLP{
				roRawBase{rec}, roRawStamped{rec}, roRawLocker{rec}, roRawProjector{rec},
			}}, b: tc.b}
			repo, err := scope.Ext(roKind)
			if err != nil {
				t.Fatal(err)
			}
			for row, want := range tc.want {
				got, err := repo.Get(ctx, ids[row])
				switch want {
				case visible:
					if err != nil || got.String("sid") != "osn_"+row {
						t.Fatalf("%s: Get = %v, %v; want the row", row, got, err)
					}
				case hidden:
					if !errors.Is(err, ErrNotFound) || got != nil {
						t.Fatalf("%s: Get = %v, %v; want ErrNotFound", row, got, err)
					}
				case fault:
					if !errors.Is(err, ErrWorkspaceConfinement) || errors.Is(err, ErrNotFound) || got != nil {
						t.Fatalf("%s: Get = %v, %v; want ErrWorkspaceConfinement", row, got, err)
					}
				}
			}
			if _, err := repo.Get(ctx, model.NewID()); !errors.Is(err, ErrNotFound) {
				t.Fatalf("absent row: Get error = %v, want the raw ErrNotFound", err)
			}
		})
	}
}

// Ordinary declared descriptors keep the exact writable confined behavior; the
// flag changes nothing unless set, and never admits an undeclared lineage.
func TestConfinedExtWritableDescriptorsKeepExactCapabilities(t *testing.T) {
	ctx := context.Background()
	defaultWS, alpha := model.NewID(), model.NewID()
	b := workspaceBoundary{id: alpha, slug: "alpha", defaultID: defaultWS}
	for _, probe := range roCombos(&roRecorder{}) {
		t.Run(probe.name, func(t *testing.T) {
			rec := &roRecorder{desc: roDescriptor(false)}
			combo := roCombos(rec)[indexOfCombo(t, probe.name)]
			scope := &workspaceConfinedScope{raw: roExtScope{repo: combo.raw}, b: b}
			repo, err := scope.Ext(roKind)
			if err != nil {
				t.Fatal(err)
			}
			if got := capabilitiesOf(repo); got != (capabilities{combo.stamped, combo.locker, combo.projector}) {
				t.Fatalf("writable confined capabilities = %+v, want %+v", got, combo)
			}
			switch repo.(type) {
			case confinedReadOnlyGenericRepo, confinedReadOnlyDistinctProjectingGenericRepo:
				t.Fatalf("unflagged descriptor selected the read-only wrapper %T", repo)
			}
			rec.calls = nil
			if _, err := repo.Create(ctx, model.Record{"workspace_id": alpha.String()}); err != nil {
				t.Fatalf("writable confined Create: %v", err)
			}
			if !reflect.DeepEqual(rec.calls, []string{"Create"}) {
				t.Fatalf("writable confined Create delegated %v, want [Create]", rec.calls)
			}
		})
	}
	for _, readOnly := range []bool{false, true} {
		desc := roDescriptor(readOnly)
		desc.WorkspaceLineage = model.WorkspaceLineageSpec{}
		rec := &roRecorder{desc: desc}
		scope := &workspaceConfinedScope{raw: roExtScope{repo: roRaw{roRawBase{rec}}}, b: b}
		if repo, err := scope.Ext(roKind); !errors.Is(err, ErrWorkspaceLineageRequired) || repo != nil {
			t.Fatalf("undeclared lineage (read-only=%t): Ext = %T, %v", readOnly, repo, err)
		}
	}
}
