// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	coremodel "github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ---- fakes -------------------------------------------------------------------

// fakeSource emits `count` edge observations on Gather, then returns.
type fakeSource struct {
	name  string
	count int
}

func (f *fakeSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: f.name, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (f *fakeSource) Open(context.Context, sdk.Config) error { return nil }

// Gather emits count observations, then behaves like a streaming source: it
// blocks until the runtime cancels ctx (so its status stays "running" after the
// initial burst, the realistic case for a tail/receiver).
func (f *fakeSource) Gather(ctx context.Context, sink sdk.Sink) error {
	for i := 0; i < f.count; i++ {
		if err := sink.Emit(ctx, model.EdgeObservation{
			OriginRef: "claude", ResourceRef: "public.t", Mode: model.ModeRead, ObservedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}
func (f *fakeSource) Close(context.Context) error { return nil }

// fakeModule subscribes to edge events and counts them.
type fakeModule struct {
	name        string
	got         chan event.Event
	initErr     error
	initPanics  bool
	subscribed  atomic.Bool
	startCalled atomic.Bool
	stopCalled  atomic.Bool
}

func (m *fakeModule) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: m.name, Type: sdk.TypeModule, APIVersion: sdk.APIVersion}
}
func (m *fakeModule) Init(_ context.Context, host sdk.Host) error {
	if m.initPanics {
		panic("init boom")
	}
	if m.initErr != nil {
		return m.initErr
	}
	_, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, func(_ context.Context, e event.Event) error {
		m.got <- e
		return nil
	})
	if err == nil {
		m.subscribed.Store(true)
	}
	return err
}
func (m *fakeModule) Start(context.Context) error { m.startCalled.Store(true); return nil }
func (m *fakeModule) Stop(context.Context) error  { m.stopCalled.Store(true); return nil }

// fakeOutput records the notifications it receives.
type fakeOutput struct {
	name        string
	got         chan sdk.Notification
	closeCalled atomic.Bool
}

func (o *fakeOutput) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: o.name, Type: sdk.TypeOutput, APIVersion: sdk.APIVersion}
}
func (o *fakeOutput) Open(context.Context, sdk.Config) error { return nil }
func (o *fakeOutput) Notify(_ context.Context, n sdk.Notification) error {
	o.got <- n
	return nil
}
func (o *fakeOutput) Close(context.Context) error { o.closeCalled.Store(true); return nil }

// ---- tests -------------------------------------------------------------------

func TestEndToEndInProc(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})

	src := &fakeSource{name: "test.source", count: 3}
	mod := &fakeModule{name: "test.module", got: make(chan event.Event, 8)}
	out := &fakeOutput{name: "test.output", got: make(chan sdk.Notification, 8)}

	if err := rt.AddSource(src, sdk.Config{}, "tenant-x"); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddOutput(out, sdk.Config{}, []event.Type{event.TypeEdgeObserved}); err != nil {
		t.Fatal(err)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	// The module and the output should each receive all 3 edge observations.
	for i := 0; i < 3; i++ {
		select {
		case e := <-mod.got:
			if e.Tenant != "tenant-x" {
				t.Errorf("module event tenant = %q, want tenant-x", e.Tenant)
			}
			if e.Source != "test.source" {
				t.Errorf("module event source = %q, want test.source", e.Source)
			}
			if e.ID == "" {
				t.Error("runtime should stamp an event ID")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("module did not receive event %d", i)
		}
		select {
		case n := <-out.got:
			if n.Type != string(event.TypeEdgeObserved) || n.Fields["resource"] != "public.t" {
				t.Errorf("unexpected notification: %+v", n)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("output did not receive notification %d", i)
		}
	}

	// Status reflects running components.
	for _, cs := range rt.Status() {
		if cs.Status != runtime.StatusRunning {
			t.Errorf("component %q status = %q, want running (%s)", cs.Name, cs.Status, cs.Err)
		}
	}
}

func TestStopIsCleanAndIdempotent(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "m", got: make(chan event.Event, 1)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !mod.stopCalled.Load() {
		t.Error("module Stop was not called")
	}
	// Idempotent.
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	for _, cs := range rt.Status() {
		if cs.Status != runtime.StatusStopped {
			t.Errorf("after stop %q = %q, want stopped", cs.Name, cs.Status)
		}
	}
}

func TestStopClosesStandaloneOutputPlugin(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	out := &fakeOutput{name: "notify.kafka", got: make(chan sdk.Notification, 1)}
	// The notify path opens the connector itself, then hands the connector/client
	// pair to the runtime. A nil client keeps this unit test hermetic; Close is the
	// lifecycle contract under test.
	rt.TrackOutputPlugin(out, nil)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !out.closeCalled.Load() {
		t.Fatal("standalone output plugin Close was not called before runtime shutdown")
	}
}

func TestFailureIsolationModuleInitPanic(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	bad := &fakeModule{name: "bad", got: make(chan event.Event, 1), initPanics: true}
	good := &fakeModule{name: "good", got: make(chan event.Event, 8)}
	src := &fakeSource{name: "src", count: 1}

	for _, err := range []error{
		rt.AddModule(bad, sdk.Config{}),
		rt.AddModule(good, sdk.Config{}),
		rt.AddSource(src, sdk.Config{}, "t"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start should not fail because one module panicked: %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	// The healthy module still receives the event.
	select {
	case <-good.got:
	case <-time.After(2 * time.Second):
		t.Fatal("healthy module starved by a panicking sibling")
	}

	// The bad module is marked failed; the good one running.
	byName := map[string]runtime.ComponentStatus{}
	for _, cs := range rt.Status() {
		byName[cs.Name] = cs
	}
	if byName["bad"].Status != runtime.StatusFailed {
		t.Errorf("panicking module status = %q, want failed", byName["bad"].Status)
	}
	if byName["good"].Status != runtime.StatusRunning {
		t.Errorf("healthy module status = %q, want running", byName["good"].Status)
	}
}

func TestRegistrationGuards(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	a := &fakeSource{name: "dup", count: 0}
	b := &fakeModule{name: "dup", got: make(chan event.Event, 1)}
	if err := rt.AddSource(a, sdk.Config{}, "t"); err != nil {
		t.Fatal(err)
	}
	// Duplicate Descriptor name across component kinds is rejected.
	if err := rt.AddModule(b, sdk.Config{}); err == nil {
		t.Error("expected duplicate-name rejection")
	}
	// Empty name is rejected.
	if err := rt.AddSource(&fakeSource{name: ""}, sdk.Config{}, "t"); err == nil {
		t.Error("expected empty-name rejection")
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })
	// Registration after Start is rejected.
	if err := rt.AddSource(&fakeSource{name: "late"}, sdk.Config{}, "t"); err != runtime.ErrAlreadyStarted {
		t.Errorf("AddSource after Start = %v, want ErrAlreadyStarted", err)
	}
}

// ---- schema seam -------------------------------------------------------------

// schemaModule is a module that also owns a data-model entity.
type schemaModule struct {
	fakeModule
	registered *[]coremodel.Kind
}

func (m *schemaModule) RegisterSchema(reg store.ExtensionRegistry) error {
	*m.registered = append(*m.registered, "demo.thing")
	return reg.Register(coremodel.EntityDescriptor{
		Kind:   "demo.thing",
		Table:  "demo_thing",
		Fields: []coremodel.FieldSpec{{Name: "label", Kind: coremodel.KindText}},
	})
}

// recordingRegistry is a fake ExtensionRegistry that records what it is asked to
// register, so the seam can be tested without a real store.
type recordingRegistry struct {
	kinds            []coremodel.Kind
	schemaInvariants []string
}

func (r *recordingRegistry) Register(d coremodel.EntityDescriptor) error {
	r.kinds = append(r.kinds, d.Kind)
	return nil
}
func (r *recordingRegistry) Migrations(string, fs.FS) error { return nil }

// SchemaInvariants RECORDS the declaration, and the previous version of this fake
// returned nil while discarding it. That is the failure the method is on the
// interface to prevent, reproduced one layer down: a module could declare its
// security invariants along a tested registration path and the test would see
// nothing, report success, and prove the opposite of what it looks like it proves.
// A compiler proves the method is present; only recording proves it was heard.
func (r *recordingRegistry) SchemaInvariants(ns string, byEngine map[store.Engine][]store.SchemaTrigger) error {
	r.schemaInvariants = append(r.schemaInvariants, ns)
	_ = byEngine
	return nil
}

func (r *recordingRegistry) WorkspaceInitializer(store.WorkspaceInitializer) error { return nil }

// RolloutControl accepts a staged-control declaration (unit G). This fake records
// nothing about it because the code under test declares none; the engine's own registry
// is where a declaration is validated and classified.
func (r *recordingRegistry) RolloutControl(store.RolloutControl) error { return nil }

func TestRegisterSchemaFansOutInOrder(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	var order []coremodel.Kind

	// A plain module (no schema) registered first, then two schema modules.
	plain := &fakeModule{name: "plain", got: make(chan event.Event, 1)}
	s1 := &schemaModule{fakeModule: fakeModule{name: "demo.one", got: make(chan event.Event, 1)}, registered: &order}
	if err := rt.AddModule(plain, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(s1, sdk.Config{}); err != nil {
		t.Fatal(err)
	}

	reg := &recordingRegistry{}
	if err := rt.RegisterSchema(reg); err != nil {
		t.Fatalf("RegisterSchema: %v", err)
	}
	if len(reg.schemaInvariants) != 0 {
		t.Errorf("this module declared schema invariants %v: the fake now RECORDS them "+
			"instead of discarding them, so this line is the decision point — assert what "+
			"they are, do not just raise the number", reg.schemaInvariants)
	}
	if len(reg.kinds) != 1 || reg.kinds[0] != "demo.thing" {
		t.Errorf("registry got %v, want [demo.thing] (plain module contributes nothing)", reg.kinds)
	}
	if len(order) != 1 {
		t.Errorf("schema provider called %d times, want 1", len(order))
	}
}

// TestRegisterSchemaBuildsRealTable drives the seam against a real in-memory
// SQLite store: the runtime's RegisterSchema is the store's register hook, the
// engine builds the module's table with the base columns and tenant guards, and
// a tenant-scoped Ext create round-trips through it. This is the end-to-end
// proof that a module's entity declaration actually materializes.
func TestRegisterSchemaBuildsRealTable(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	var order []coremodel.Kind
	sm := &schemaModule{fakeModule: fakeModule{name: "demo.module", got: make(chan event.Event, 1)}, registered: &order}
	if err := rt.AddModule(sm, sdk.Config{}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, rt.RegisterSchema)
	if err != nil {
		t.Fatalf("open store with module schema: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Provision a tenant.
	var tenant coremodel.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, coremodel.Org{Name: "Demo", Slug: "demo", Status: coremodel.StatusActive})
		if err != nil {
			return err
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}

	// The module-declared table exists and is usable through a tenant scope.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("demo.thing")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, coremodel.Record{"label": "hello"})
		return err
	}); err != nil {
		t.Fatalf("use module-registered entity: %v", err)
	}
}

// TestTheRegistryFakeHearsASchemaInvariant is the mutation-testable half of the fix that
// made the fakes record instead of discard.
//
// The four test registries in this repository used to satisfy SchemaInvariants with `return
// nil`, throwing the declaration away. Their own comments said the method sits on
// store.ExtensionRegistry precisely so that a registry cannot silently drop a module's
// security invariants — and then dropped it. The compiler proves the method exists; only a
// recording fake proves the declaration was heard.
//
// The count assertions added alongside the RegisterSchema calls are TRIPWIRES, not tests:
// every module on those paths declares zero invariants today, so they pass whether the fake
// records or discards. They fire the day one starts declaring. This test is the part that
// fails NOW if the fake goes back to discarding, which is what makes the fix verifiable
// rather than merely plausible.
//
// MUTATION THAT MUST TURN THIS RED: restore `return nil` in recordingRegistry
// .SchemaInvariants without recording the namespace.
func TestTheRegistryFakeHearsASchemaInvariant(t *testing.T) {
	reg := &recordingRegistry{}
	err := reg.SchemaInvariants("demo", map[store.Engine][]store.SchemaTrigger{
		store.EnginePostgres: {{Name: "demo_append_only", Table: "demo_thing"}},
	})
	if err != nil {
		t.Fatalf("SchemaInvariants: %v", err)
	}
	if len(reg.schemaInvariants) != 1 || reg.schemaInvariants[0] != "demo" {
		t.Fatalf("the fake recorded %v, want [demo]: a fake that returns nil and keeps nothing "+
			"reports success for a security declaration nobody received, which is the exact "+
			"failure this method is on the interface to prevent", reg.schemaInvariants)
	}
}
