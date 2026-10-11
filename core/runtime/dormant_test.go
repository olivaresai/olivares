// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"context"
	"testing"
	"time"

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
