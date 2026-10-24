// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/eventbus"
	coremodel "github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
)

// A dormant module is one this node does not run. Its schema stays declared (its
// tables, guards and retirement stay). It is initialized for direct calls, but it
// receives no event and is never started or stopped.
func TestDormantModuleKeepsItsSchemaAndRunsNoWork(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	var kinds []coremodel.Kind
	src := &fakeSource{name: "test.source", count: 3}
	active := &fakeModule{name: "active", got: make(chan event.Event, 8)}
	dormant := &schemaModule{fakeModule: fakeModule{name: "dormant", got: make(chan event.Event, 8)}, registered: &kinds}
	if err := rt.AddSource(src, sdk.Config{}, "tenant-x"); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(active, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddDormantModule(dormant, sdk.Config{}); err != nil {
		t.Fatal(err)
	}

	reg := &recordingRegistry{}
	if err := rt.RegisterSchema(reg); err != nil {
		t.Fatal(err)
	}
	if len(reg.kinds) != 1 || reg.kinds[0] != "demo.thing" {
		t.Fatalf("dormant module schema = %v, want [demo.thing]", reg.kinds)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-active.got:
		case <-time.After(2 * time.Second):
			t.Fatalf("active module did not receive event %d", i)
		}
	}
	select {
	case e := <-dormant.got:
		t.Fatalf("dormant module received an event: %+v", e)
	case <-time.After(200 * time.Millisecond):
	}
	if dormant.startCalled.Load() {
		t.Fatal("dormant module was started")
	}
	if !active.startCalled.Load() {
		t.Fatal("active module was not started")
	}
	want := map[string]runtime.Status{"active": runtime.StatusRunning, "dormant": runtime.StatusDormant}
	for _, cs := range rt.Status() {
		if w, ok := want[cs.Name]; ok && cs.Status != w {
			t.Errorf("%s status = %q, want %q", cs.Name, cs.Status, w)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if dormant.stopCalled.Load() {
		t.Fatal("dormant module was stopped although it never started")
	}
	if !active.stopCalled.Load() {
		t.Fatal("active module was not stopped")
	}
	for _, cs := range rt.Status() {
		if cs.Name == "dormant" && cs.Status != runtime.StatusDormant {
			t.Errorf("dormant status after stop = %q, want %q", cs.Status, runtime.StatusDormant)
		}
	}
}

type publishingModule struct {
	fakeModule
	host sdk.Host
}

func (m *publishingModule) Init(_ context.Context, host sdk.Host) error {
	m.host = host
	return nil
}

func TestDormantModulePublishDoesNotDeliver(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	active := &publishingModule{fakeModule: fakeModule{name: "active"}}
	dormant := &publishingModule{fakeModule: fakeModule{name: "dormant"}}
	consumer := &fakeModule{name: "consumer", got: make(chan event.Event, 2)}
	for _, mod := range []sdk.Module{active, consumer} {
		if err := rt.AddModule(mod, sdk.Config{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.AddDormantModule(dormant, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rt.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := dormant.host.Publish(t.Context(), event.Event{ID: "dormant", Type: event.TypeEdgeObserved}); err != nil {
		t.Fatalf("dormant Publish = %v, want nil", err)
	}
	if err := active.host.Publish(t.Context(), event.Event{ID: "active", Type: event.TypeEdgeObserved}); err != nil {
		t.Fatal(err)
	}
	// Both publications use the same FIFO subscription. Receiving the active
	// event proves the earlier dormant publication delivered nothing, without
	// waiting for an arbitrary silence interval.
	select {
	case e := <-consumer.got:
		if e.ID != "active" || e.Source != "active" {
			t.Fatalf("subscriber received ID=%q source=%q, want active publication", e.ID, e.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active publication did not reach subscriber")
	}
}

// A running node can turn a module on and off without a restart: the event
// subscriptions the module made in Init follow its state, Start runs once, and
// Stop still runs at shutdown for a module that was ever started.
func TestModuleActivityChangesWhileTheRuntimeRuns(t *testing.T) {
	bus := eventbus.NewInProc(eventbus.Options{Logger: quiet()})
	t.Cleanup(func() { _ = bus.Close() })
	rt := runtime.New(runtime.Options{Logger: quiet(), Bus: bus})
	later := &fakeModule{name: "later", got: make(chan event.Event, 8)}
	always := &fakeModule{name: "always", got: make(chan event.Event, 8)}
	if err := rt.AddDormantModule(later, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddModule(always, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetModuleActive(t.Context(), "later", true); !errors.Is(err, runtime.ErrNotRunning) {
		t.Fatalf("SetModuleActive before Start = %v, want ErrNotRunning", err)
	}
	if err := rt.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	publish := func() {
		if err := bus.Publish(t.Context(), event.Event{Type: event.TypeEdgeObserved}); err != nil {
			t.Fatal(err)
		}
	}
	delivered := func(m *fakeModule) bool {
		select {
		case <-m.got:
			return true
		case <-time.After(300 * time.Millisecond):
			return false
		}
	}
	status := func(name string) runtime.Status {
		for _, cs := range rt.Status() {
			if cs.Name == name {
				return cs.Status
			}
		}
		t.Fatalf("no component %q", name)
		return ""
	}

	publish()
	if delivered(later) || !delivered(always) {
		t.Fatal("before activation: the dormant module must get nothing and the running one must")
	}
	for _, step := range []struct {
		name   string
		active bool
	}{{"later", true}, {"later", false}, {"later", true}, {"always", false}, {"always", true}} {
		if err := rt.SetModuleActive(t.Context(), step.name, step.active); err != nil {
			t.Fatalf("SetModuleActive(%s, %v) = %v", step.name, step.active, err)
		}
		want := runtime.StatusDormant
		if step.active {
			want = runtime.StatusRunning
		}
		if got := status(step.name); got != want {
			t.Fatalf("%s active=%v: status %s, want %s", step.name, step.active, got, want)
		}
		publish()
		for _, m := range []*fakeModule{later, always} {
			wantDelivery := status(m.name) == runtime.StatusRunning
			if got := delivered(m); got != wantDelivery {
				t.Fatalf("after %s active=%v: %s delivered=%v, want %v", step.name, step.active, m.name, got, wantDelivery)
			}
		}
	}
	if err := rt.SetModuleActive(t.Context(), "nope", true); err == nil {
		t.Fatal("an unknown module must be refused")
	}
	if err := rt.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !later.stopCalled.Load() || !always.stopCalled.Load() {
		t.Fatal("a module that ran must be stopped at shutdown")
	}
}

// failingBus refuses the nth subscription, so a module with several subscriptions can be
// turned on only partway.
type failingBus struct {
	eventbus.Bus
	calls, failAt int
}

func (b *failingBus) Subscribe(types []event.Type, h event.Handler) (eventbus.Subscription, error) {
	b.calls++
	if b.calls == b.failAt {
		return nil, errors.New("bus refused the subscription")
	}
	return b.Bus.Subscribe(types, h)
}

// twoSubscriptions asks the bus for two subscriptions in Init and records its Start context.
type twoSubscriptions struct {
	fakeModule
	startCtx atomic.Pointer[context.Context]
	starts   atomic.Int32
	startErr atomic.Pointer[error]
}

func (m *twoSubscriptions) Init(_ context.Context, host sdk.Host) error {
	for range 2 {
		if _, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, func(context.Context, event.Event) error {
			m.got <- event.Event{}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (m *twoSubscriptions) Start(ctx context.Context) error {
	m.starts.Add(1)
	m.startCtx.Store(&ctx)
	if e := m.startErr.Load(); e != nil {
		return *e
	}
	return nil
}

// A module that cannot be turned on is left with no subscription, not with the first one
// that attached; and the module is not reported running.
func TestFailedActivationLeavesNoSubscriptionBehind(t *testing.T) {
	inner := eventbus.NewInProc(eventbus.Options{Logger: quiet()})
	t.Cleanup(func() { _ = inner.Close() })
	bus := &failingBus{Bus: inner, failAt: 2}
	rt := runtime.New(runtime.Options{Logger: quiet(), Bus: bus})
	m := &twoSubscriptions{fakeModule: fakeModule{name: "two", got: make(chan event.Event, 8)}}
	if err := rt.AddDormantModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })
	if err := rt.SetModuleActive(t.Context(), "two", true); err == nil {
		t.Fatal("turning on a module whose second subscription is refused must fail")
	}
	if err := inner.Publish(t.Context(), event.Event{Type: event.TypeEdgeObserved}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.got:
		t.Fatal("a module that failed to turn on still receives events through the subscription that attached")
	case <-time.After(300 * time.Millisecond):
	}
	if m.starts.Load() != 0 {
		t.Fatal("a module that failed to attach was started")
	}
}

// Start runs once however often the module is turned off and on, gets a context that
// outlives the call that turned it on, and a Start that fails is tried again next time.
func TestLiveStartUsesTheRuntimeContextAndIsRetriedAfterAFailure(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	m := &twoSubscriptions{fakeModule: fakeModule{name: "late", got: make(chan event.Event, 8)}}
	if err := rt.AddDormantModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	boom := errors.New("start failed")
	m.startErr.Store(&boom)
	req, cancel := context.WithCancel(context.Background())
	if err := rt.SetModuleActive(req, "late", true); err == nil {
		t.Fatal("a module whose Start fails must not be reported turned on")
	}
	m.startErr.Store(nil)
	if err := rt.SetModuleActive(req, "late", true); err != nil {
		t.Fatalf("turning the module on again after a failed Start = %v", err)
	}
	cancel() // the request that turned it on is over
	if ctx := m.startCtx.Load(); ctx == nil || (*ctx).Err() != nil {
		t.Fatal("Start was given the request's context: it ends with the request")
	}
	for _, active := range []bool{false, true, false, true} {
		if err := rt.SetModuleActive(context.Background(), "late", active); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.starts.Load(); got != 2 { // the failed attempt and the one that worked
		t.Fatalf("Start ran %d times, want 2 (the failure and the success)", got)
	}
}
