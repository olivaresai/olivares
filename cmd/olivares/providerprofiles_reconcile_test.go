// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// B1 acceptance row 6, at the composition seam: the reconciler records the roster
// row's persistent id and the revision it SUCCESSFULLY applied, registers the
// source with that snapshot, and a failed rotation leaves the previous revision
// both applied and stamped. Delete-and-recreate under one name is another id.

type regEmitSource struct {
	name    string
	openErr error
	fire    chan struct{}
}

func (s *regEmitSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: s.name, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (s *regEmitSource) Open(context.Context, sdk.Config) error { return s.openErr }
func (s *regEmitSource) Gather(ctx context.Context, sink sdk.Sink) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.fire:
			if err := sink.Emit(ctx, sdkmodel.EdgeObservation{
				OriginKind: "session", OriginRef: "ext-1", ResourceRef: "public.t", Mode: sdkmodel.ModeRead, ObservedAt: time.Now().UTC(),
			}); err != nil {
				return err
			}
		}
	}
}
func (s *regEmitSource) Close(context.Context) error { return nil }

type regCaptureModule struct{ got chan event.Event }

func (m *regCaptureModule) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.capture", Type: sdk.TypeModule, APIVersion: sdk.APIVersion}
}
func (m *regCaptureModule) Init(_ context.Context, host sdk.Host) error {
	_, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, func(_ context.Context, e event.Event) error {
		m.got <- e
		return nil
	})
	return err
}
func (m *regCaptureModule) Start(context.Context) error { return nil }
func (m *regCaptureModule) Stop(context.Context) error  { return nil }

func TestReconcileRecordsAppliedRevisionAndStampsEvents(t *testing.T) {
	ctx := context.Background()
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	capture := &regCaptureModule{got: make(chan event.Event, 16)}
	if err := rt.AddModule(capture, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, rt.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(sctx)
		_ = st.Close()
	})
	srcStore := auth.NewSourceStore(st)
	sr := newSourceReconciler(rt, srcStore, nil, nil, t.TempDir(), nil, quietLog())
	// One emitting fake per prepared revision; the test fires the CURRENT one.
	var current *regEmitSource
	sr.prepare = func(_ context.Context, def model.SourceDef) (*runtime.PreparedSource, sdk.Config, string) {
		s := &regEmitSource{name: "olivares.claude", fire: make(chan struct{}, 4)}
		if def.Config["mode"] == "openfail" {
			s.openErr = errors.New("refused")
		} else {
			current = s
		}
		return sr.rt.PrepareInProcSource(s), sdk.Config{Settings: def.Config}, ""
	}
	recv := func() event.Event {
		t.Helper()
		select {
		case e := <-capture.got:
			return e
		case <-time.After(3 * time.Second):
			t.Fatal("no event")
		}
		return event.Event{}
	}
	tenant := model.NewID().String()
	put := func(cfg map[string]string) model.SourceDef {
		t.Helper()
		saved, err := srcStore.Put(ctx, recAdmin(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: "claude-home-a", Kind: "claude", Tenant: tenant, Enabled: true, Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	reconcile := func() {
		t.Helper()
		if _, err := sr.reconcile(ctx); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}

	// WITHOUT an environment identity the source runs unattributed and nothing is
	// recorded as an applied revision — the legacy posture, kept honest.
	row := put(map[string]string{"mode": "ok", "gen": "1"})
	reconcile()
	if _, _, ok := sr.appliedRevision("claude-home-a"); ok {
		t.Fatal("applied revision recorded without an environment identity")
	}
	current.fire <- struct{}{}
	if e := recv(); e.SourceRegistration != nil {
		t.Fatalf("unattributed source stamped %+v", e.SourceRegistration)
	}

	// WITH an environment identity, a rotation registers the snapshot: the row's
	// id and the revision that was applied.
	sr.useEnvironmentRef("env-1")
	row = put(map[string]string{"mode": "ok", "gen": "2"})
	reconcile()
	id, rev, ok := sr.appliedRevision("claude-home-a")
	if !ok || id != row.ID || rev != row.Version || rev < 2 {
		t.Fatalf("applied = (%s, %d, %v), want (%s, %d)", id, rev, ok, row.ID, row.Version)
	}
	current.fire <- struct{}{}
	e := recv()
	if e.Source != "claude-home-a" || e.SourceRegistration == nil || e.SourceRegistration.SourceID != row.ID.String() || e.SourceRegistration.SourceRevision != row.Version || e.SourceRegistration.EnvironmentRef != "env-1" {
		t.Fatalf("event = source %q registration %+v", e.Source, e.SourceRegistration)
	}
	applied := *e.SourceRegistration

	// A rotation whose Open FAILS: the previous revision stays applied AND stamped.
	failing := put(map[string]string{"mode": "openfail", "gen": "3"})
	if failing.Version <= row.Version {
		t.Fatal("test premise: the store did not bump the version")
	}
	reconcile()
	id, rev, ok = sr.appliedRevision("claude-home-a")
	if !ok || id != row.ID || rev != applied.SourceRevision {
		t.Fatalf("after a failed rotation applied = (%s, %d, %v), want the previous (%s, %d)", id, rev, ok, row.ID, applied.SourceRevision)
	}
	current.fire <- struct{}{}
	if e := recv(); e.SourceRegistration == nil || *e.SourceRegistration != applied {
		t.Fatalf("after a failed rotation events stamp %+v, want %+v", e.SourceRegistration, applied)
	}

	// Delete and recreate under the SAME name: another id, revision 1 again.
	if err := srcStore.Delete(ctx, recAdmin(), auth.GlobalSourceScope, "claude-home-a"); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if _, _, ok := sr.appliedRevision("claude-home-a"); ok {
		t.Fatal("a removed source still reports an applied revision")
	}
	recreated := put(map[string]string{"mode": "ok", "gen": "4"})
	if recreated.ID == row.ID {
		t.Fatal("test premise: recreate reused the id")
	}
	reconcile()
	id, rev, ok = sr.appliedRevision("claude-home-a")
	if !ok || id != recreated.ID || rev != recreated.Version {
		t.Fatalf("recreated applied = (%s, %d, %v), want (%s, %d)", id, rev, ok, recreated.ID, recreated.Version)
	}
	current.fire <- struct{}{}
	if e := recv(); e.SourceRegistration == nil || e.SourceRegistration.SourceID != recreated.ID.String() || e.SourceRegistration.SourceID == row.ID.String() {
		t.Fatalf("recreated source stamps %+v; it must not inherit the old id", e.SourceRegistration)
	}
}

// The console binds a source by what the roster READ reports — the row's
// persistent id and the revision THIS node applied — so those two fields have to
// be the reconciler's own record, not the stored definition: a failed rotation
// keeps the previous revision applied, a node without an environment identity
// has applied nothing bindable, and a recreated row is another id. A client that
// read a stored version instead would name a revision the binding port refuses.
func TestListSourcesReportsPersistentIDAndAppliedRevision(t *testing.T) {
	ctx := context.Background()
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, rt.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(sctx)
		_ = st.Close()
	})
	srcStore := auth.NewSourceStore(st)
	sr := newSourceReconciler(rt, srcStore, nil, nil, t.TempDir(), nil, quietLog())
	sr.prepare = func(_ context.Context, def model.SourceDef) (*runtime.PreparedSource, sdk.Config, string) {
		s := &regEmitSource{name: "olivares.claude", fire: make(chan struct{}, 1)}
		if def.Config["mode"] == "openfail" {
			s.openErr = errors.New("refused")
		}
		return sr.rt.PrepareInProcSource(s), sdk.Config{Settings: def.Config}, ""
	}
	tenant := model.NewID().String()
	put := func(cfg map[string]string) model.SourceDef {
		t.Helper()
		saved, err := srcStore.Put(ctx, recAdmin(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: "claude-home-b", Kind: "claude", Tenant: tenant, Enabled: true, Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	reconcile := func() {
		t.Helper()
		if _, err := sr.reconcile(ctx); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}
	entry := func() api.SourceRosterEntry {
		t.Helper()
		list, err := sr.ListSources(ctx)
		if err != nil {
			t.Fatalf("ListSources: %v", err)
		}
		for _, e := range list {
			if e.Name == "claude-home-b" {
				return e
			}
		}
		t.Fatalf("ListSources = %+v, want claude-home-b", list)
		return api.SourceRosterEntry{}
	}

	// No environment identity: the id is still the row's, and NOTHING is reported
	// as applied — the console must not offer this source for a binding.
	row := put(map[string]string{"mode": "ok", "gen": "1"})
	reconcile()
	if e := entry(); e.ID != row.ID.String() || e.AppliedRevision != 0 {
		t.Fatalf("without an environment identity entry = id %q rev %d, want id %q rev 0", e.ID, e.AppliedRevision, row.ID)
	}

	// With one, the applied revision is the store's version of the row that was
	// opened — the same answer appliedRevision gives the binding port.
	sr.useEnvironmentRef("env-1")
	row = put(map[string]string{"mode": "ok", "gen": "2"})
	reconcile()
	e := entry()
	_, portRev, ok := sr.appliedRevision("claude-home-b")
	if !ok || e.ID != row.ID.String() || e.AppliedRevision != row.Version || e.AppliedRevision != portRev || e.AppliedRevision < 2 {
		t.Fatalf("applied entry = id %q rev %d (port %d, %v), want id %q rev %d", e.ID, e.AppliedRevision, portRev, ok, row.ID, row.Version)
	}
	applied := e.AppliedRevision

	// A rotation whose Open FAILS: the stored version moved on, the applied one did
	// not — and the roster read says the applied one, because that is the only
	// revision a binding created from it would be accepted at.
	failing := put(map[string]string{"mode": "openfail", "gen": "3"})
	if failing.Version <= applied {
		t.Fatal("test premise: the store did not bump the version")
	}
	reconcile()
	if e := entry(); e.ID != row.ID.String() || e.AppliedRevision != applied {
		t.Fatalf("after a failed rotation entry = id %q rev %d, want id %q rev %d (stored is %d)", e.ID, e.AppliedRevision, row.ID, applied, failing.Version)
	}

	// Delete and recreate under the SAME name: another id, its own revision.
	if err := srcStore.Delete(ctx, recAdmin(), auth.GlobalSourceScope, "claude-home-b"); err != nil {
		t.Fatal(err)
	}
	reconcile()
	recreated := put(map[string]string{"mode": "ok", "gen": "4"})
	if recreated.ID == row.ID {
		t.Fatal("test premise: recreate reused the id")
	}
	reconcile()
	if e := entry(); e.ID != recreated.ID.String() || e.ID == row.ID.String() || e.AppliedRevision != recreated.Version {
		t.Fatalf("recreated entry = id %q rev %d, want id %q rev %d", e.ID, e.AppliedRevision, recreated.ID, recreated.Version)
	}
}
