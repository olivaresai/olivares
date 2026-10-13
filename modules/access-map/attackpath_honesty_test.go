// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	accessmap "github.com/olivaresai/olivares/modules/access-map"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// An attack-path answer is either complete, or it says it is not. These tests pin
// that: a failed store read is an error, never a shorter list, and a cap that cut
// the answer is reported on the wire.

var errBoom = errors.New("store read failed")

// faultStore fails chosen AccessEdge reads so the failure is the thing under test.
type faultStore struct {
	store.Store
	neighbors error
	// listOrigin, when set, fails the List whose origin_id filter is this id.
	listOrigin model.ID
	listErr    error
	// getID, when set, fails the Get of that agent, resource or tool row.
	getID  model.ID
	getErr error
}

func (f faultStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return f.Store.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(faultScope{Scope: sc, f: f}) })
}

type faultScope struct {
	store.Scope
	f faultStore
}

func (s faultScope) AccessEdges() store.AccessEdgeRepo {
	return faultEdges{AccessEdgeRepo: s.Scope.AccessEdges(), f: s.f}
}

func (s faultScope) Agents() store.Repository[model.Agent] {
	return faultGet[model.Agent]{Repository: s.Scope.Agents(), f: s.f}
}

func (s faultScope) Tools() store.Repository[model.Tool] {
	return faultGet[model.Tool]{Repository: s.Scope.Tools(), f: s.f}
}

func (s faultScope) Resources() store.ResourceRepo {
	return faultResources{ResourceRepo: s.Scope.Resources(), f: s.f}
}

// faultGet fails Get for one id and passes everything else through.
type faultGet[T any] struct {
	store.Repository[T]
	f faultStore
}

func (g faultGet[T]) Get(ctx context.Context, id model.ID) (T, error) {
	if g.f.getErr != nil && id == g.f.getID {
		var zero T
		return zero, g.f.getErr
	}
	return g.Repository.Get(ctx, id)
}

type faultResources struct {
	store.ResourceRepo
	f faultStore
}

func (r faultResources) Get(ctx context.Context, id model.ID) (model.Resource, error) {
	if r.f.getErr != nil && id == r.f.getID {
		return model.Resource{}, r.f.getErr
	}
	return r.ResourceRepo.Get(ctx, id)
}

type faultEdges struct {
	store.AccessEdgeRepo
	f faultStore
}

func (e faultEdges) Neighbors(ctx context.Context, n model.NodeRef, d model.Direction) ([]model.AccessEdge, error) {
	if e.f.neighbors != nil {
		return nil, e.f.neighbors
	}
	return e.AccessEdgeRepo.Neighbors(ctx, n, d)
}

func (e faultEdges) List(ctx context.Context, q model.Query) ([]model.AccessEdge, model.Page, error) {
	if e.f.listErr != nil {
		for _, f := range q.Filters {
			if f.Column == "origin_id" && fmt.Sprint(f.Value) == e.f.listOrigin.String() {
				return nil, model.Page{}, e.f.listErr
			}
		}
	}
	return e.AccessEdgeRepo.List(ctx, q)
}

func seedAgent(t *testing.T, ctx context.Context, sc store.Scope, name string) model.ID {
	t.Helper()
	a, err := sc.Agents().Create(ctx, model.Agent{Name: name, ExternalID: name, Status: model.StatusActive})
	if err != nil {
		t.Fatalf("seed agent %s: %v", name, err)
	}
	return a.ID
}

func seedResource(t *testing.T, ctx context.Context, sc store.Scope, name, sensitivity string) model.ID {
	t.Helper()
	r, err := sc.Resources().Create(ctx, model.Resource{Name: name, Kind: "postgres.table", URI: name, Sensitivity: sensitivity})
	if err != nil {
		t.Fatalf("seed resource %s: %v", name, err)
	}
	return r.ID
}

func seedEdge(t *testing.T, ctx context.Context, sc store.Scope, agent, resource model.ID, mode sdkmodel.AccessMode) {
	t.Helper()
	if _, err := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
		OriginKind: "agent", OriginID: agent, ResourceID: resource, Mode: mode,
		SignalSource: sdkmodel.SignalPGAudit, Confidence: sdkmodel.ConfidenceAttributed, Observed: true,
	}); err != nil {
		t.Fatalf("seed edge: %v", err)
	}
}

// escalationFixture: reader reads the shared resource; each of n others reads it
// and writes an outbox, so the reader has n escalation paths (each other agent
// holds more write edges on a resource the reader also touches).
type escalationFixture struct {
	reader, other model.ID
	shared, dest  model.ID
	tool          model.ID
	tenant        model.TenantID
	editor        string // a token allowed the privileged attack-path reads
}

func seedEscalation(t *testing.T, h *harness, others int) escalationFixture {
	t.Helper()
	admin := h.adminLogin()
	fx := escalationFixture{tenant: h.createOrg(admin, "acme")}
	fx.editor = h.editorToken(admin, fx.tenant, "e@acme.com")
	ctx := context.Background()
	if err := h.st.Mutate(ctx, fx.tenant, func(sc store.Scope) error {
		fx.shared = seedResource(t, ctx, sc, "customers", "high")
		fx.dest = seedResource(t, ctx, sc, "outbox", "")
		dest := fx.dest
		fx.reader = seedAgent(t, ctx, sc, "reader")
		tool, err := sc.Tools().Create(ctx, model.Tool{Name: "psql", Kind: "cli"})
		if err != nil {
			t.Fatalf("seed tool: %v", err)
		}
		fx.tool = tool.ID
		if _, err := sc.AccessEdges().Upsert(ctx, model.AccessEdge{
			OriginKind: "agent", OriginID: fx.reader, ResourceID: fx.shared, Mode: sdkmodel.ModeRead, ToolID: tool.ID,
			SignalSource: sdkmodel.SignalPGAudit, Confidence: sdkmodel.ConfidenceAttributed, Observed: true,
		}); err != nil {
			t.Fatalf("seed tool edge: %v", err)
		}
		for i := 0; i < others; i++ {
			o := seedAgent(t, ctx, sc, fmt.Sprintf("writer-%03d", i))
			if i == 0 {
				fx.other = o
			}
			seedEdge(t, ctx, sc, o, fx.shared, sdkmodel.ModeRead)
			seedEdge(t, ctx, sc, o, dest, sdkmodel.ModeWrite)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed escalation: %v", err)
	}
	return fx
}

func faulty(h *harness, f faultStore) *accessmap.Module {
	f.Store = h.st
	m := accessmap.New()
	m.UseData(api.NewModuleData(f))
	return m
}

func TestEscalationPaths_ReadErrorIsReturned(t *testing.T) {
	cases := []struct {
		name  string
		fault func(fx escalationFixture) faultStore
	}{
		{"neighbors of a shared resource", func(escalationFixture) faultStore { return faultStore{neighbors: errBoom} }},
		{"edges of the other agent", func(fx escalationFixture) faultStore {
			return faultStore{listOrigin: fx.other, listErr: errBoom}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fx := seedEscalation(t, h, 1)
			m := faulty(h, tc.fault(fx))
			_, err := m.EscalationPaths(context.Background(), fx.tenant, "user:auditor", model.ActorUser, fx.reader)
			if !errors.Is(err, errBoom) {
				t.Fatalf("EscalationPaths err = %v, want the store error (a failed read is not an empty answer)", err)
			}
		})
	}
}

func TestEscalationPaths_UnknownAgentIsNotFound(t *testing.T) {
	h := newHarness(t)
	fx := seedEscalation(t, h, 1)
	m := faulty(h, faultStore{})
	_, err := m.EscalationPaths(context.Background(), fx.tenant, "user:auditor", model.ActorUser, model.NewID())
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("EscalationPaths for an unknown agent err = %v, want not found, as Reachability answers", err)
	}
}

func TestExfilRoutes_ReadErrorIsReturned(t *testing.T) {
	cases := []struct {
		name  string
		fault func(fx escalationFixture) faultStore
	}{
		{"readers of the resource", func(escalationFixture) faultStore { return faultStore{neighbors: errBoom} }},
		{"write edges of a reader", func(fx escalationFixture) faultStore {
			return faultStore{listOrigin: fx.other, listErr: errBoom}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fx := seedEscalation(t, h, 1)
			m := faulty(h, tc.fault(fx))
			_, err := m.ExfilRoutes(context.Background(), fx.tenant, "user:auditor", model.ActorUser, fx.shared)
			if !errors.Is(err, errBoom) {
				t.Fatalf("ExfilRoutes err = %v, want the store error (a failed read is not an empty answer)", err)
			}
		})
	}
}

// A dangling edge (its agent row is gone) is data, not a failure: the path is
// still reported, with no name. Only a real store error aborts the analysis.
func TestEscalationPaths_DanglingAgentIsStillReported(t *testing.T) {
	h := newHarness(t)
	fx := seedEscalation(t, h, 0)
	ctx := context.Background()
	if err := h.st.Mutate(ctx, fx.tenant, func(sc store.Scope) error {
		ghost := model.NewID()
		dest := seedResource(t, ctx, sc, "ghost-outbox", "")
		seedEdge(t, ctx, sc, ghost, fx.shared, sdkmodel.ModeRead)
		seedEdge(t, ctx, sc, ghost, dest, sdkmodel.ModeWrite)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := faulty(h, faultStore{})
	got, err := m.EscalationPaths(ctx, fx.tenant, "user:auditor", model.ActorUser, fx.reader)
	if err != nil {
		t.Fatalf("EscalationPaths with a dangling edge: %v", err)
	}
	if len(got.Paths) != 1 || len(got.Paths[0].Steps) != 3 || got.Paths[0].Steps[2].NodeName != "" {
		t.Fatalf("paths = %+v, want one path ending at the unnamed dangling agent", got.Paths)
	}
}

func attackGET(t *testing.T, h *harness, editor, path string, tenant model.TenantID) resp {
	t.Helper()
	r := h.do("GET", path, editor, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, r.code, r.raw)
	}
	return r
}

func TestAttackPaths_PathCapIsReportedAsTruncated(t *testing.T) {
	cases := []struct {
		name          string
		others        int
		wantPaths     int
		wantTruncated bool
	}{
		{"cap exactly met is complete", 100, 100, false},
		{"one past the cap is cut and says so", 101, 100, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fx := seedEscalation(t, h, tc.others)
			r := attackGET(t, h, fx.editor, "/v1/m/accessmap/attack-paths/escalation?agent_id="+fx.reader.String(), fx.tenant)
			if paths, _ := r.body["paths"].([]any); len(paths) != tc.wantPaths {
				t.Fatalf("escalation paths = %d, want %d: %s", len(paths), tc.wantPaths, r.raw)
			}
			if got, _ := r.body["truncated"].(bool); got != tc.wantTruncated {
				t.Errorf("truncated = %v, want %v: the cap cut the answer and the response must say so", got, tc.wantTruncated)
			}
			x := attackGET(t, h, fx.editor, "/v1/m/accessmap/attack-paths/exfil?resource_id="+fx.shared.String(), fx.tenant)
			if got, _ := x.body["truncated"].(bool); got != tc.wantTruncated {
				t.Errorf("exfil truncated = %v, want %v (%d routes through the cap): %s", got, tc.wantTruncated, tc.others, x.raw)
			}
		})
	}
}

func TestReachability_StoreRowCapIsReportedAsTruncated(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.editorToken(admin, tenant, "e@acme.com")
	ctx := context.Background()
	var agent model.ID
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		agent = seedAgent(t, ctx, sc, "wide")
		for i := 0; i < 1001; i++ {
			seedEdge(t, ctx, sc, agent, seedResource(t, ctx, sc, fmt.Sprintf("t-%04d", i), ""), sdkmodel.ModeRead)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := attackGET(t, h, editor, "/v1/m/accessmap/attack-paths/reachability?agent_id="+agent.String(), tenant)
	if got, _ := r.body["truncated"].(bool); !got {
		t.Errorf("1001 edges read through a 1000-row store cap, truncated = false: the answer is a floor and must say so")
	}
}

func TestAttackPathSummary_CountsEscalationAndExfil(t *testing.T) {
	h := newHarness(t)
	fx := seedEscalation(t, h, 1) // reader -> customers <- writer -> outbox
	r := attackGET(t, h, fx.editor, "/v1/m/accessmap/attack-paths/summary", fx.tenant)
	if got := jsonNum(r.body["escalation_paths"]); got != 1 {
		t.Errorf("escalation_paths = %d, want 1 (reader reaches the writer through customers): %s", got, r.raw)
	}
	if got := jsonNum(r.body["exfil_routes"]); got != 1 {
		t.Errorf("exfil_routes = %d, want 1 (customers is sensitive; the writer carries it to outbox): %s", got, r.raw)
	}
	if got, _ := r.body["truncated"].(bool); got {
		t.Errorf("a two-agent estate reported truncated: %s", r.raw)
	}
}

// A name looked up for a path step is part of the answer: a failed lookup is the
// store error, not an unnamed node. Only a row that is gone (ErrNotFound) is data.
func TestAnalyses_NameLookupErrorIsReturned(t *testing.T) {
	type call func(m *accessmap.Module, fx escalationFixture) error
	escalation := func(m *accessmap.Module, fx escalationFixture) error {
		_, err := m.EscalationPaths(context.Background(), fx.tenant, "user:auditor", model.ActorUser, fx.reader)
		return err
	}
	exfil := func(m *accessmap.Module, fx escalationFixture) error {
		_, err := m.ExfilRoutes(context.Background(), fx.tenant, "user:auditor", model.ActorUser, fx.shared)
		return err
	}
	reachability := func(m *accessmap.Module, fx escalationFixture) error {
		_, err := m.Reachability(context.Background(), fx.tenant, "user:auditor", model.ActorUser, fx.reader)
		return err
	}
	cases := []struct {
		name string
		run  call
		id   func(fx escalationFixture) model.ID
	}{
		{"escalation: the other agent", escalation, func(fx escalationFixture) model.ID { return fx.other }},
		{"escalation: the shared resource", escalation, func(fx escalationFixture) model.ID { return fx.shared }},
		{"exfil: the reading agent", exfil, func(fx escalationFixture) model.ID { return fx.other }},
		{"exfil: the destination resource", exfil, func(fx escalationFixture) model.ID { return fx.dest }},
		{"reachability: the resource", reachability, func(fx escalationFixture) model.ID { return fx.shared }},
		{"reachability: the tool", reachability, func(fx escalationFixture) model.ID { return fx.tool }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fx := seedEscalation(t, h, 1)
			m := faulty(h, faultStore{getID: tc.id(fx), getErr: errBoom})
			if err := tc.run(m, fx); !errors.Is(err, errBoom) {
				t.Fatalf("err = %v, want the store error", err)
			}
		})
	}
}

// Every analysis is a privileged, self-audited read. The record is written in the
// same transaction as the answer, and a read that fails leaves none.
func TestAnalyses_AreAuditedAndAFailedReadLeavesNoEvent(t *testing.T) {
	h := newHarness(t)
	fx := seedEscalation(t, h, 1)
	ctx := context.Background()
	m := faulty(h, faultStore{})

	// The ledger blinds event metadata, so events are told apart by action and
	// counted: one per analysis.
	reads := func() int {
		n := 0
		if err := h.st.View(ctx, fx.tenant, func(sc store.Scope) error {
			return sc.Audit().Walk(ctx, 1, func(e model.AuditEvent) error {
				if e.Action == "access_map.attack_path.read" {
					n++
				}
				return nil
			})
		}); err != nil {
			t.Fatalf("walk audit: %v", err)
		}
		return n
	}
	if n := reads(); n != 0 {
		t.Fatalf("precondition: %d attack-path audit events before any analysis", n)
	}
	for name, run := range map[string]func() error{
		"reachability": func() error {
			_, err := m.Reachability(ctx, fx.tenant, "user:auditor", model.ActorUser, fx.reader)
			return err
		},
		"escalation": func() error {
			_, err := m.EscalationPaths(ctx, fx.tenant, "user:auditor", model.ActorUser, fx.reader)
			return err
		},
		"exfil": func() error {
			_, err := m.ExfilRoutes(ctx, fx.tenant, "user:auditor", model.ActorUser, fx.shared)
			return err
		},
	} {
		before := reads()
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if after := reads(); after != before+1 {
			t.Errorf("%s left %d audit event(s), want exactly 1", name, after-before)
		}
	}

	before := reads()
	failing := faulty(h, faultStore{neighbors: errBoom})
	if _, err := failing.EscalationPaths(ctx, fx.tenant, "user:auditor", model.ActorUser, fx.reader); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want the store error", err)
	}
	if after := reads(); after != before {
		t.Errorf("a failed read left %d audit event(s), want none", after-before)
	}
}

func TestAttackPathSummary_TruncationFollowsTheCaps(t *testing.T) {
	cases := []struct {
		name          string
		others        int
		wantTruncated bool
	}{
		{"counts that reach the path cap exactly are complete", 100, false},
		{"counts cut by the path cap are a floor and say so", 101, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fx := seedEscalation(t, h, tc.others)
			r := attackGET(t, h, fx.editor, "/v1/m/accessmap/attack-paths/summary", fx.tenant)
			if got := jsonNum(r.body["escalation_paths"]); got != 100 {
				t.Errorf("escalation_paths = %d, want 100: %s", got, r.raw)
			}
			if got := jsonNum(r.body["exfil_routes"]); got != 100 {
				t.Errorf("exfil_routes = %d, want 100: %s", got, r.raw)
			}
			if got, _ := r.body["truncated"].(bool); got != tc.wantTruncated {
				t.Errorf("truncated = %v, want %v: %s", got, tc.wantTruncated, r.raw)
			}
		})
	}
}

// The store's Neighbors has no page token, so a read that fills the 1000-row cap
// is reported as possibly cut; one row short of it is complete.
func TestExfilRoutes_ReaderReadCapIsReportedAsTruncated(t *testing.T) {
	for _, tc := range []struct {
		readers       int
		wantTruncated bool
	}{{999, false}, {1000, true}} {
		t.Run(fmt.Sprintf("%d readers", tc.readers), func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			editor := h.editorToken(admin, tenant, "e@acme.com")
			ctx := context.Background()
			var hot model.ID
			if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
				hot = seedResource(t, ctx, sc, "hot", "high")
				for i := 0; i < tc.readers; i++ {
					seedEdge(t, ctx, sc, model.NewID(), hot, sdkmodel.ModeRead)
				}
				return nil
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			r := attackGET(t, h, editor, "/v1/m/accessmap/attack-paths/exfil?resource_id="+hot.String(), tenant)
			if got, _ := r.body["truncated"].(bool); got != tc.wantTruncated {
				t.Errorf("truncated = %v, want %v with %d readers: %s", got, tc.wantTruncated, tc.readers, r.raw)
			}
		})
	}
}

// An agent that reaches nothing answers an empty list, never null.
func TestAttackPaths_NoEdgesIsAnEmptyList(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.editorToken(admin, tenant, "e@acme.com")
	ctx := context.Background()
	var agent model.ID
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		agent = seedAgent(t, ctx, sc, "idle")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"reachability", "escalation"} {
		r := attackGET(t, h, editor, "/v1/m/accessmap/attack-paths/"+kind+"?agent_id="+agent.String(), tenant)
		if paths, ok := r.body["paths"].([]any); !ok || len(paths) != 0 {
			t.Errorf("%s of an idle agent = %s, want paths: []", kind, r.raw)
		}
	}
}

// Unknown ids are 404 over the wire, as Reachability always answered.
func TestAttackPaths_UnknownSubjectIsNotFoundOverHTTP(t *testing.T) {
	h := newHarness(t)
	fx := seedEscalation(t, h, 1)
	for _, path := range []string{
		"/v1/m/accessmap/attack-paths/escalation?agent_id=" + model.NewID().String(),
		"/v1/m/accessmap/attack-paths/reachability?agent_id=" + model.NewID().String(),
		"/v1/m/accessmap/attack-paths/exfil?resource_id=" + model.NewID().String(),
	} {
		if r := h.do("GET", path, fx.editor, tenantHdr(fx.tenant)); r.code != http.StatusNotFound {
			t.Errorf("GET %s = %d %s, want 404", path, r.code, r.raw)
		}
	}
}
