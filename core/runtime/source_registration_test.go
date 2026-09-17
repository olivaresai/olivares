// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// B1 — the host stamps the roster snapshot; nothing else can.

// emitSource emits one edge observation per Emit call requested through `fire`
// and then blocks like a streaming source.
type emitSource struct {
	name string
	fire chan struct{}
}

func (s *emitSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: s.name, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (s *emitSource) Open(context.Context, sdk.Config) error { return nil }
func (s *emitSource) Gather(ctx context.Context, sink sdk.Sink) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.fire:
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind: "session", OriginRef: "ext-1", ResourceRef: "public.t", Mode: model.ModeRead, ObservedAt: time.Now().UTC(),
			}); err != nil {
				return err
			}
		}
	}
}
func (s *emitSource) Close(context.Context) error { return nil }

func recvEvent(t *testing.T, ch <-chan event.Event) event.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no event delivered")
	}
	return event.Event{}
}

func TestSourceRegistration_StampedByHostOnly(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "test.module", got: make(chan event.Event, 16)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	ctx := context.Background()

	// A registered roster source: every event carries the snapshot the HOST was
	// given at registration, and Source is the registration name.
	regA := event.SourceRegistration{SourceID: "row-a", SourceRevision: 2, EnvironmentRef: "env-1"}
	srcA := &emitSource{name: "olivares.grok", fire: make(chan struct{}, 4)}
	if err := rt.AddPreparedSourceRegistered(ctx, "grok-home-a", rt.PrepareInProcSource(srcA), sdk.Config{}, "tn-1", 0, regA); err != nil {
		t.Fatalf("add registered: %v", err)
	}
	srcA.fire <- struct{}{}
	e := recvEvent(t, mod.got)
	if e.Source != "grok-home-a" || e.SourceRegistration == nil || *e.SourceRegistration != regA {
		t.Fatalf("registered source event = source %q registration %+v", e.Source, e.SourceRegistration)
	}
	// The stamped value is a COPY: a later mutation of the caller's struct does
	// not reach events already produced or the registration itself.
	regA.SourceRevision = 99
	srcA.fire <- struct{}{}
	if e := recvEvent(t, mod.got); e.SourceRegistration == nil || e.SourceRegistration.SourceRevision != 2 {
		t.Fatalf("registration must be copied at registration time: %+v", e.SourceRegistration)
	}

	// A merely NAMED source (a collector's file config, a legacy caller) carries
	// nothing: the host stamps only what it was given.
	srcB := &emitSource{name: "olivares.grok", fire: make(chan struct{}, 4)}
	if err := rt.AddPreparedSourceNamed(ctx, "grok-home-b", rt.PrepareInProcSource(srcB), sdk.Config{}, "tn-1", 0); err != nil {
		t.Fatalf("add named: %v", err)
	}
	srcB.fire <- struct{}{}
	if e := recvEvent(t, mod.got); e.Source != "grok-home-b" || e.SourceRegistration != nil {
		t.Fatalf("named source event = source %q registration %+v, want nil registration", e.Source, e.SourceRegistration)
	}

	// The remote push boundary: an observation PUSHED by a collector under the
	// very same source name as a registered source still arrives unattributed —
	// the push envelope has no snapshot and the receiving node authenticates only
	// the tenant. This is the boundary a collector cannot cross, however it labels
	// itself.
	if err := rt.Ingest(ctx, "tn-1", "grok-home-a", model.EdgeObservation{
		OriginKind: "session", OriginRef: "ext-1", ResourceRef: "public.t", Mode: model.ModeRead, ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if e := recvEvent(t, mod.got); e.Source != "grok-home-a" || e.SourceRegistration != nil {
		t.Fatalf("pushed observation = source %q registration %+v, want nil registration", e.Source, e.SourceRegistration)
	}

	// Inventory reports the snapshot beside name and component, as a copy.
	var seen int
	for _, s := range rt.LiveSourceInventory() {
		switch s.Name {
		case "grok-home-a":
			seen++
			if s.Registration == nil || s.Registration.SourceID != "row-a" || s.Registration.SourceRevision != 2 || s.Component != "olivares.grok" {
				t.Fatalf("inventory for a = %+v", s)
			}
		case "grok-home-b":
			seen++
			if s.Registration != nil {
				t.Fatalf("inventory for b carries a registration: %+v", s.Registration)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("inventory saw %d of 2 sources", seen)
	}
}

func TestSourceRegistration_RotationKeepsInFlightRevision(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "test.module", got: make(chan event.Event, 16)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	ctx := context.Background()
	rev1 := event.SourceRegistration{SourceID: "row-a", SourceRevision: 1, EnvironmentRef: "env-1"}
	first := &emitSource{name: "olivares.grok", fire: make(chan struct{}, 4)}
	if err := rt.AddPreparedSourceRegistered(ctx, "grok-home-a", rt.PrepareInProcSource(first), sdk.Config{}, "tn-1", 0, rev1); err != nil {
		t.Fatal(err)
	}
	first.fire <- struct{}{}
	if e := recvEvent(t, mod.got); e.SourceRegistration == nil || e.SourceRegistration.SourceRevision != 1 {
		t.Fatalf("revision 1 event = %+v", e.SourceRegistration)
	}

	// A rotation whose Open FAILS leaves revision 1 running and stamped.
	failing := newLiveSource("olivares.grok")
	failing.openErr = errors.New("refused")
	err := rt.ReplacePreparedSourceRegistered(ctx, "grok-home-a", rt.PrepareInProcSource(failing), sdk.Config{}, "tn-1", 0,
		event.SourceRegistration{SourceID: "row-a", SourceRevision: 2, EnvironmentRef: "env-1"})
	if !errors.Is(err, runtime.ErrSourceOpenFailed) {
		t.Fatalf("failed rotation = %v", err)
	}
	first.fire <- struct{}{}
	if e := recvEvent(t, mod.got); e.SourceRegistration == nil || e.SourceRegistration.SourceRevision != 1 {
		t.Fatalf("after a failed rotation the running source must still stamp revision 1: %+v", e.SourceRegistration)
	}

	// A successful rotation stamps revision 2 from then on.
	second := &emitSource{name: "olivares.grok", fire: make(chan struct{}, 4)}
	if err := rt.ReplacePreparedSourceRegistered(ctx, "grok-home-a", rt.PrepareInProcSource(second), sdk.Config{}, "tn-1", 0,
		event.SourceRegistration{SourceID: "row-a", SourceRevision: 2, EnvironmentRef: "env-1"}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	second.fire <- struct{}{}
	if e := recvEvent(t, mod.got); e.SourceRegistration == nil || e.SourceRegistration.SourceRevision != 2 {
		t.Fatalf("revision 2 event = %+v", e.SourceRegistration)
	}

	// A partial snapshot is refused outright and does not touch the running source.
	third := &emitSource{name: "olivares.grok", fire: make(chan struct{}, 4)}
	for _, bad := range []event.SourceRegistration{
		{SourceRevision: 3, EnvironmentRef: "env-1"},
		{SourceID: "row-a", EnvironmentRef: "env-1"},
		{SourceID: "row-a", SourceRevision: 3},
	} {
		if err := rt.ReplacePreparedSourceRegistered(ctx, "grok-home-a", rt.PrepareInProcSource(third), sdk.Config{}, "tn-1", 0, bad); !errors.Is(err, runtime.ErrInvalidSourceRegistration) {
			t.Fatalf("partial snapshot %+v = %v", bad, err)
		}
		if err := rt.AddPreparedSourceRegistered(ctx, "grok-home-c", rt.PrepareInProcSource(third), sdk.Config{}, "tn-1", 0, bad); !errors.Is(err, runtime.ErrInvalidSourceRegistration) {
			t.Fatalf("partial snapshot add %+v = %v", bad, err)
		}
	}
	if rt.SourceIsRegistered("grok-home-c") {
		t.Fatal("a refused registration was wired")
	}
	second.fire <- struct{}{}
	if e := recvEvent(t, mod.got); e.SourceRegistration == nil || e.SourceRegistration.SourceRevision != 2 {
		t.Fatalf("refused rotations disturbed the running source: %+v", e.SourceRegistration)
	}
}

func TestSourceRegistration_BindingDecisionIsFreshPerObservation(t *testing.T) {
	var calls int
	rt := runtime.New(runtime.Options{Logger: quiet(), SourceRegistrationAdmission: func(_ context.Context, tenant string, reg event.SourceRegistration) (event.SourceRegistration, error) {
		if tenant != "tn-1" || reg.BindingRef != "" {
			return reg, errors.New("reused observation decision")
		}
		calls++
		if calls == 1 {
			reg.BindingRef = "first-approved-decision"
		}
		return reg, nil
	}})
	mod := &fakeModule{name: "test.binding", got: make(chan event.Event, 4)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	source := &emitSource{name: "olivares.grok", fire: make(chan struct{}, 2)}
	reg := event.SourceRegistration{SourceID: "source-a", SourceRevision: 1, EnvironmentRef: "env-1", BindingRef: "must-not-be-reused"}
	if err := rt.AddPreparedSourceRegistered(context.Background(), "grok", rt.PrepareInProcSource(source), sdk.Config{}, "tn-1", 0, reg); err != nil {
		t.Fatal(err)
	}
	source.fire <- struct{}{}
	first := recvEvent(t, mod.got)
	source.fire <- struct{}{}
	second := recvEvent(t, mod.got)
	if first.SourceRegistration.BindingRef != "first-approved-decision" || second.SourceRegistration.BindingRef != "" || first.ID == second.ID {
		t.Fatal("source reused an earlier decision or changed an immutable envelope")
	}
}
