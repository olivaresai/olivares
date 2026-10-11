// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

type partialInitModule struct {
	fakeModule
	panicAfterSubscribe bool
}

func (m *partialInitModule) Init(_ context.Context, host sdk.Host) error {
	for _, kind := range []event.Type{event.TypeEdgeObserved, event.TypeCostSampled} {
		if _, err := host.Subscribe([]event.Type{kind}, func(_ context.Context, e event.Event) error {
			m.got <- e
			return nil
		}); err != nil {
			return err
		}
	}
	if m.panicAfterSubscribe {
		panic("initialization failed after subscribing")
	}
	return errors.New("initialization failed after subscribing")
}

func TestFailedModuleInitReleasesSubscriptions(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "error"
		if panics {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			bus := eventbus.NewInProc(eventbus.Options{Logger: quiet()})
			t.Cleanup(func() { _ = bus.Close() })
			rt := runtime.New(runtime.Options{Logger: quiet(), Bus: bus})
			bad := &partialInitModule{
				fakeModule:          fakeModule{name: "bad", got: make(chan event.Event, 2)},
				panicAfterSubscribe: panics,
			}
			good := &fakeModule{name: "good", got: make(chan event.Event, 1)}
			for _, m := range []sdk.Module{bad, good} {
				if err := rt.AddModule(m, sdk.Config{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := rt.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rt.Stop(context.Background()) })

			stats := bus.(eventbus.StatsProvider).BusStats()
			if len(stats.Subscribers) != 1 || stats.Subscribers[0].Name != "good" {
				t.Errorf("subscribers after failed initialization = %+v; want only good", stats.Subscribers)
			}
			if bad.startCalled.Load() {
				t.Error("failed module was started")
			}
			byName := map[string]runtime.ComponentStatus{}
			for _, status := range rt.Status() {
				byName[status.Name] = status
			}
			if byName["bad"].Status != runtime.StatusFailed {
				t.Errorf("failed module status = %q", byName["bad"].Status)
			}
			if !good.startCalled.Load() || byName["good"].Status != runtime.StatusRunning {
				t.Error("healthy module was not started")
			}
			if err := rt.Ingest(context.Background(), "tenant", "source", model.EdgeObservation{
				OriginRef: "agent", ResourceRef: "folder", Mode: model.ModeRead,
			}); err != nil {
				t.Fatal(err)
			}
			if err := rt.Ingest(context.Background(), "tenant", "source", model.CostSample{
				ProviderRef: "provider",
			}); err != nil {
				t.Fatal(err)
			}
			if got := bus.(eventbus.StatsProvider).BusStats().Enqueued; got != 1 {
				t.Errorf("event deliveries = %d; want only the healthy module's delivery", got)
			}
			select {
			case <-good.got:
			case <-time.After(2 * time.Second):
				t.Fatal("healthy module did not receive the event")
			}
			for range 2 {
				if err := rt.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if len(bus.(eventbus.StatsProvider).BusStats().Subscribers) != 0 {
				t.Error("subscriptions remained after shutdown")
			}
			select {
			case e := <-bad.got:
				t.Errorf("failed module received %q after initialization failed", e.Type)
			default:
			}
		})
	}
}
