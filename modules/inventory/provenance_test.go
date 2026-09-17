// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func c1Event(tenant model.TenantID, id, sourceID string) event.Event {
	e := event.FromObservation(tenant.String(), "editable-name", mkEdge("agent", "shared-external",
		"file", "/data/shared", sdkmodel.ModeRead, sdkmodel.SignalOTEL, "", baseTime))
	e.ID = id
	e.SourceRegistration = &event.SourceRegistration{SourceID: sourceID, SourceRevision: 1, EnvironmentRef: "env-a"}
	return e
}

func c1Rows(t *testing.T, st store.Store, tenant model.TenantID, kind model.Kind) []model.Record {
	t.Helper()
	var rows []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rows, _, err = repo.List(context.Background(), model.Query{Limit: listCap})
		return err
	}); err != nil {
		t.Fatalf("read %s projection: %v", kind, err)
	}
	return rows
}

func TestC1SourceInstances(t *testing.T) {
	m, st, tenant := newInv(t)
	for _, source := range []string{"registered-a", "registered-b"} {
		if err := m.onEvent(context.Background(), c1Event(tenant, "event-"+source, source)); err != nil {
			t.Fatal(err)
		}
	}
	if got := countKind(t, st, tenant, kindAgent); got != 1 {
		t.Fatalf("legacy core alias count = %d, want 1", got)
	}
	rows := c1Rows(t, st, tenant, "inventory.observation_member")
	identities, aliases := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if row.String("entity_kind") == kindAgent {
			identities[row.String("observation_key")] = true
			aliases[row.String("entity_id")] = true
		}
	}
	if len(identities) != 2 || identities[""] || len(aliases) != 1 {
		t.Fatalf("source-qualified identities=%v core aliases=%v; want two observations sharing one legacy alias", identities, aliases)
	}
	other := c1Tenant(t, st, "other")
	if err := m.onEvent(context.Background(), c1Event(other, "event-registered-a", "registered-a")); err != nil {
		t.Fatal(err)
	}
	for _, row := range c1Rows(t, st, other, observationMemberKind) {
		if identities[row.String(colObservationKey)] || aliases[row.String(colEntityID)] {
			t.Fatal("cross-tenant identity/alias collision")
		}
	}
	if len(c1Rows(t, st, tenant, observationReceiptKind)) != 2 {
		t.Fatal("other tenant changed receipts")
	}
}

func c1Tenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sc store.SystemScope) error {
		if _, err := sc.EnsureSystemTenant(context.Background()); err != nil {
			return err
		}
		org, err := sc.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return tenant
}

func c1Open(t *testing.T, path string, legacy bool) (*Module, store.Store) {
	t.Helper()
	m := New(WithClock(pinnedClock{at: baseTime.Add(time.Hour)}))
	register := m.RegisterSchema
	if legacy {
		register = func(reg store.ExtensionRegistry) error { return m.RegisterSchema(c1CatalogRegistry{reg}) }
	}
	st, err := engine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: path}, register)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m.UseData(api.NewModuleData(st))
	return m, st
}

// The old schema is the unchanged catalog descriptor, with no C1 registrations.
type c1CatalogRegistry struct{ store.ExtensionRegistry }

func (r c1CatalogRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == catalogEntryKind {
		return r.ExtensionRegistry.Register(d)
	}
	return nil
}

func c1Receipt(t *testing.T, st store.Store, tenant model.TenantID, eventID string) model.Record {
	t.Helper()
	for _, row := range c1Rows(t, st, tenant, observationReceiptKind) {
		if row.String(colEventID) == eventID {
			return row
		}
	}
	t.Fatalf("receipt %q missing", eventID)
	return nil
}

func c1Facts(t *testing.T, row model.Record) inventoryFactsV1 {
	t.Helper()
	var facts inventoryFactsV1
	if err := json.Unmarshal([]byte(row.String(colFacts)), &facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestC1ReplayReopenAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.db")
	m, st := c1Open(t, path, false)
	tenant := c1Tenant(t, st, "replay")
	e := c1Event(tenant, "first", "source-a")
	for range 2 {
		if err := m.onEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	original := c1Receipt(t, st, tenant, "first")
	if original.Int(colDeliveries) != 2 || original.Int(colMemberCount) != 2 {
		t.Fatal(original)
	}
	second := e
	second.ID = "second"
	if err := m.onEvent(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	conflicting := e
	edge, _ := event.EdgeOf(e)
	edge.OriginRef = "different-agent"
	conflicting.Payload = edge
	if err := m.onEvent(context.Background(), conflicting); !errors.Is(err, ErrObservationReceiptConflict) {
		t.Fatalf("conflict = %v", err)
	}
	if got := c1Receipt(t, st, tenant, "first"); got.String(colFactsHash) != original.String(colFactsHash) || got.Int(colDeliveries) != 2 {
		t.Fatal("conflict overwrote original", got)
	}
	if len(c1Rows(t, st, tenant, observationConflictKind)) != 1 || countKind(t, st, tenant, kindAgent) != 1 {
		t.Fatal("conflict not preserved separately")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	m, st = c1Open(t, path, false)
	if err := m.onEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if len(c1Rows(t, st, tenant, observationReceiptKind)) != 2 || len(c1Rows(t, st, tenant, observationMemberKind)) != 4 {
		t.Fatal("reopen replay duplicated evidence")
	}
	if got := c1Receipt(t, st, tenant, "first"); got.Int(colDeliveries) != 3 || got.String(colFacts) != original.String(colFacts) {
		t.Fatal("replay changed facts", got)
	}
	for _, row := range c1Rows(t, st, tenant, catalogEntryKind) {
		if row.Int(colOccurrence) != 4 {
			t.Fatalf("legacy delivery count = %d, want 4", row.Int(colOccurrence))
		}
	}
	// Missing ID is another reception each time, not payload-based dedup.
	e.ID = ""
	for range 2 {
		if err := m.onEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if len(c1Rows(t, st, tenant, observationReceiptKind)) != 4 {
		t.Fatal("missing ID claimed replay identity")
	}
}

func TestC1RegistrationPresenceAndNativeReferences(t *testing.T) {
	m, st, tenant := newInv(t)
	for i, reg := range []*event.SourceRegistration{
		{SourceID: "source-a", SourceRevision: 1, EnvironmentRef: "env-a", BindingRef: "historical-ref"},
		{SourceID: "source-a", SourceRevision: 1, EnvironmentRef: "env-a"},
		nil, {SourceID: "source-a", SourceRevision: 0, EnvironmentRef: "env-a"},
	} {
		e := c1Event(tenant, []string{"bound", "empty-binding", "nil", "invalid"}[i], "unused")
		e.Source = "source-a" // a spoofed label must not complete registration.
		e.SourceRegistration = reg
		edge, _ := event.EdgeOf(e)
		edge.ObservedAt = time.Time{}
		e.Payload = &edge
		e.Time = time.Time{}
		if err := m.onEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		if reg != nil {
			reg.BindingRef = "changed-after-consumption"
		}
	}
	states := map[string]string{"bound": "registered_snapshot", "empty-binding": "registered_snapshot", "nil": "unattributed", "invalid": "invalid"}
	for id, state := range states {
		r := c1Receipt(t, st, tenant, id)
		f := c1Facts(t, r)
		if f.RegistrationState != state || f.SourceLabel != "source-a" || f.Edge.Signal != sdkmodel.SignalOTEL || f.Edge.Confidence != sdkmodel.ConfidenceAttributed || f.Edge.OccurredAt != "" || r.String(colFirstSeen) == "" {
			t.Fatalf("facts %s = %+v", id, f)
		}
		if id == "empty-binding" && f.Registration.BindingRef != "" {
			t.Fatal("empty binding inherited attribution")
		}
		if id == "bound" && f.Registration.BindingRef != "historical-ref" {
			t.Fatal("snapshot was mutable")
		}
		if id == "nil" && f.Registration != nil {
			t.Fatal("nil upgraded to registration")
		}
		if id == "invalid" && (f.Registration == nil || f.Registration.SourceRevision != 0) {
			t.Fatal("invalid presence erased")
		}
		for _, member := range c1Rows(t, st, tenant, observationMemberKind) {
			if member.String(colReceiptID) == r.String(model.ColID) && (id == "nil" || id == "invalid") && member.String(colObservationKey) != "" {
				t.Fatal("unattributed receipt inherited stable source identity")
			}
		}
	}
	for _, payload := range []any{(*sdkmodel.EdgeObservation)(nil), (*sdkmodel.CostSample)(nil)} {
		e := c1Event(tenant, "typed-nil", "source-a")
		e.Payload = payload
		if _, ok := payload.(*sdkmodel.CostSample); ok {
			e.Type = event.TypeCostSampled
		}
		if err := m.onEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if len(c1Rows(t, st, tenant, observationReceiptKind)) != 4 {
		t.Fatal("typed nil created a receipt")
	}
	// One source can name identical tool/model leaves under different native parents.
	for _, server := range []string{"one", "two"} {
		e := c1Event(tenant, "tool-"+server, "source-a")
		e.Payload = mkEdge("mcp_server", server, rkMCPTool, server+"/same", sdkmodel.ModeRead, sdkmodel.SignalMCPAnnotation, "same", baseTime)
		if err := m.onEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		cost := event.FromObservation(tenant.String(), "cost-label", sdkmodel.CostSample{ProviderRef: server, ModelRef: "same", OccurredAt: baseTime})
		cost.ID = "cost-" + server
		cost.SourceRegistration = e.SourceRegistration
		if err := m.onEvent(context.Background(), cost); err != nil {
			t.Fatal(err)
		}
		// A gateway is an explicit deployment namespace, not a billing default.
		gateway := sdkmodel.Gateway("fixture-" + server)
		cost.ID = "gateway-" + server
		cost.Payload = sdkmodel.CostSample{ProviderRef: "shared-provider", ModelRef: "same", Gateway: gateway}
		if err := m.onEvent(context.Background(), cost); err != nil {
			t.Fatal(err)
		}
		if f := c1Facts(t, c1Receipt(t, st, tenant, cost.ID)); f.Cost.Gateway != string(gateway) {
			t.Fatal("lost supplied gateway namespace")
		}
		// Agent-owned declarations name their parent in the edge, unlike a bare
		// operation performed by a session (which does not establish tool ownership).
		e.ID = "agent-tool-" + server
		e.Payload = mkEdge("agent", "agent-"+server, rkCMAAgentTool, "same", sdkmodel.ModeUnknown, sdkmodel.SignalPolicy, "", baseTime)
		if err := m.onEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{kindTool, kindModel} {
		parents := map[string]bool{}
		identities := map[string]bool{}
		for _, row := range c1Rows(t, st, tenant, observationMemberKind) {
			if row.String(colEntityKind) != kind {
				continue
			}
			var member observationMember
			if err := json.Unmarshal([]byte(row.String(colFacts)), &member); err != nil {
				t.Fatal(err)
			}
			parents[member.Native.Parent] = true
			identities[row.String(colObservationKey)] = true
		}
		if !parents["one"] || !parents["two"] || len(identities) != 4 {
			t.Fatalf("%s native parents=%v identities=%v", kind, parents, identities)
		}
	}
}

type c1FailData struct {
	api.ModuleData
	creates int
}
type c1FailScope struct {
	store.Scope
	owner *c1FailData
}
type c1FailRepo struct {
	store.GenericRepo
	owner *c1FailData
}

var c1WriteFailure = errors.New("fixture: second member write failed")

func (d *c1FailData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(c1FailScope{sc, d}) })
}
func (sc c1FailScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := sc.Scope.Ext(kind)
	if err == nil && kind == observationMemberKind {
		return c1FailRepo{repo, sc.owner}, nil
	}
	return repo, err
}
func (r c1FailRepo) Create(ctx context.Context, rec model.Record) (model.Record, error) {
	r.owner.creates++
	if r.owner.creates == 2 {
		return nil, c1WriteFailure
	}
	return r.GenericRepo.Create(ctx, rec)
}

func TestC1AtomicFanoutAndConcurrentReplay(t *testing.T) {
	m, st, tenant := newInv(t)
	data := api.NewModuleData(st)
	failing := &c1FailData{ModuleData: data}
	m.UseData(failing)
	e := c1Event(tenant, "atomic", "source-a")
	if err := m.onEvent(context.Background(), e); !errors.Is(err, c1WriteFailure) {
		t.Fatalf("injected transaction failure = %v", err)
	}
	if failing.creates != 2 {
		t.Fatal("did not reach second member")
	}
	for _, kind := range []model.Kind{catalogEntryKind, observationReceiptKind, observationMemberKind} {
		if len(c1Rows(t, st, tenant, kind)) != 0 {
			t.Fatalf("%s escaped rollback", kind)
		}
	}
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		agents, _, err := sc.Agents().List(context.Background(), model.Query{})
		if len(agents) != 0 {
			t.Fatal("core alias escaped rollback")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(data)
	if err := m.onEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- m.onEvent(context.Background(), e) }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(c1Rows(t, st, tenant, observationReceiptKind)) != 1 || len(c1Rows(t, st, tenant, observationMemberKind)) != 2 {
		t.Fatal("partial or duplicate fanout")
	}
	if c1Receipt(t, st, tenant, "atomic").Int(colDeliveries) != 3 {
		t.Fatal("concurrent replay lost delivery")
	}
}

func TestC1LegacyUpgradeAndConfinement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	old, st := c1Open(t, path, true)
	tenant := c1Tenant(t, st, "legacy")
	var alias model.ID
	writeOld := func(m *Module, st store.Store) {
		t.Helper()
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			var err error
			alias, err = foAgent(ctx, sc, "shared-external")
			if err != nil {
				return err
			}
			return m.upsertCatalogEntry(ctx, sc, kindAgent, alias, "shared-external", "shared-external", "legacy-signal", "", baseTime, time.Time{})
		}); err != nil {
			t.Fatal(err)
		}
	}
	writeOld(old, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	m, st := c1Open(t, path, false)
	if len(c1Rows(t, st, tenant, observationReceiptKind)) != 0 {
		t.Fatal("upgrade invented historical provenance")
	}
	e := c1Event(tenant, "new-writer", "source-a")
	if err := m.onEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	original := c1Receipt(t, st, tenant, "new-writer").String(colFacts)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	old, st = c1Open(t, path, true)
	writeOld(old, st) // actual unchanged catalog writer, schema unaware of receipts.
	for _, row := range c1Rows(t, st, tenant, catalogEntryKind) {
		if row.String(colEntityKind) == kindAgent {
			dto := toEntryDTO(row)
			if dto.EntityID != alias.String() || dto.OccurrenceCount != 3 {
				t.Fatalf("old reader contract changed: %+v", dto)
			}
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	m, st = c1Open(t, path, false)
	e.ID = "nil-after-old-writer"
	e.SourceRegistration = nil
	if err := m.onEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	if c1Receipt(t, st, tenant, "new-writer").String(colFacts) != original || c1Facts(t, c1Receipt(t, st, tenant, e.ID)).Registration != nil {
		t.Fatal("legacy/nil delivery erased or inherited evidence")
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, sc, ws.ID)
		if err != nil {
			return err
		}
		for _, kind := range []model.Kind{catalogEntryKind, observationReceiptKind, observationMemberKind, observationConflictKind} {
			if _, err := confined.Ext(kind); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
				t.Errorf("confined %s = %v", kind, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type c1LiveSource struct {
	input   chan sdkmodel.Observation
	openErr error
}

func (s *c1LiveSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.c1-source", Version: "1", APIVersion: sdk.APIVersion, Type: sdk.TypeSource}
}
func (s *c1LiveSource) Open(context.Context, sdk.Config) error { return s.openErr }
func (s *c1LiveSource) Close(context.Context) error            { return nil }
func (s *c1LiveSource) Gather(ctx context.Context, sink sdk.Sink) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case obs := <-s.input:
			if err := sink.Emit(ctx, obs); err != nil {
				return err
			}
		}
	}
}

type c1CommitData struct {
	api.ModuleData
	committed chan error
}

func (d c1CommitData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := d.ModuleData.Mutate(ctx, tenant, fn)
	d.committed <- err
	return err
}

func TestC1AppliedRevisionThroughRegisteredHost(t *testing.T) {
	m, st, tenant := newInv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	committed := make(chan error, 8)
	m.UseData(c1CommitData{api.NewModuleData(st), committed})
	rt := runtime.New(runtime.Options{})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		if err := rt.Stop(stop); err != nil {
			t.Error(err)
		}
	})
	source := &c1LiveSource{input: make(chan sdkmodel.Observation)}
	reg := event.SourceRegistration{SourceID: "persistent-a", SourceRevision: 1, EnvironmentRef: "env-a", BindingRef: "not-admitted"}
	if err := rt.AddPreparedSourceRegistered(ctx, "old-name", rt.PrepareInProcSource(source), sdk.Config{}, tenant.String(), time.Hour, reg); err != nil {
		t.Fatal(err)
	}
	emit := func(emission int, s *c1LiveSource) {
		t.Helper()
		edge, _ := event.EdgeOf(c1Event(tenant, "", ""))
		select {
		case s.input <- edge:
		case <-ctx.Done():
			t.Fatalf("emission %d: send observation: %v", emission, ctx.Err())
		}
		select {
		case err := <-committed:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatalf("emission %d: wait committed: %v", emission, ctx.Err())
		}
	}
	emit(1, source)
	reg.SourceRevision = 2
	failed := &c1LiveSource{openErr: errors.New("fixture Open failed")}
	if err := rt.ReplacePreparedSourceRegistered(ctx, "old-name", rt.PrepareInProcSource(failed), sdk.Config{}, tenant.String(), time.Hour, reg); err == nil {
		t.Fatal("candidate Open should fail")
	}
	emit(2, source)
	for _, row := range c1Rows(t, st, tenant, observationReceiptKind) {
		f := c1Facts(t, row)
		if f.Registration.SourceRevision != 1 || f.Registration.BindingRef != "" {
			t.Fatal("failed Open or inherited binding attributed", f)
		}
	}
	source2 := &c1LiveSource{input: make(chan sdkmodel.Observation)}
	if err := rt.ReplacePreparedSourceRegistered(ctx, "old-name", rt.PrepareInProcSource(source2), sdk.Config{}, tenant.String(), time.Hour, reg); err != nil {
		t.Fatal(err)
	}
	emit(3, source2)
	// Label rename and recreation are module-input controls; Open failure above
	// uses the real registered runtime sink, bus and committed materializer.
	e := c1Event(tenant, "renamed", "persistent-a")
	e.Source = "new-name"
	e.SourceRegistration.SourceRevision = 2
	if err := m.onEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	e.ID = "recreated"
	e.SourceRegistration.SourceID = "persistent-b"
	if err := m.onEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	versions := map[int64]bool{}
	for _, row := range c1Rows(t, st, tenant, observationMemberKind) {
		if row.String(colEntityKind) == kindAgent {
			keys[row.String(colObservationKey)] = true
		}
	}
	for _, row := range c1Rows(t, st, tenant, observationReceiptKind) {
		versions[c1Facts(t, row).Registration.SourceRevision] = true
	}
	if len(keys) != 2 || !versions[1] || !versions[2] {
		t.Fatalf("identities=%v versions=%v", keys, versions)
	}
}
