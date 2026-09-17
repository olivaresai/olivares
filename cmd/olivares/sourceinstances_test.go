// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Several REGISTERED SOURCES of one connector kind, each with its own life.
//
// Scope, said plainly because the difference matters: these prove that two roster
// rows of one kind are two independent SOURCES — separately opened, configured,
// rotated, failed, removed and attributed. They prove nothing about two isolated
// provider SESSIONS or two config homes being told apart end to end; that needs
// a canonical provider-instance identity, which is separate, later work.

// --- acceptance 1-3: two rows of one kind, live, independent ------------------

// TestTwoRosterRowsOfOneKindLiveIndependently drives the reconciler with fake
// connectors that SHARE a descriptor (the harness maps every kind to
// "olivares.<kind>", exactly as two real rows of one kind do) and checks the
// whole life of the pair: both wire, a rotate touches one, a rejected apply
// leaves the running instance AND the sibling alone, and a delete stops one.
func TestTwoRosterRowsOfOneKindLiveIndependently(t *testing.T) {
	sr, store, rt := newReconcilerHarness(t)
	ctx := context.Background()

	putRow(t, store, model.SourceDef{
		Name: "grok-home-a", Kind: "grok", Tenant: "acme", Enabled: true,
		Config: map[string]string{"config_path": "/fixtures/a/config.toml"},
	})
	putRow(t, store, model.SourceDef{
		Name: "grok-home-b", Kind: "grok", Tenant: "acme", Enabled: true,
		Config: map[string]string{"config_path": "/fixtures/b/config.toml"},
	})

	rep, err := sr.reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(rep.Added) != 2 || len(rep.Rejected) != 0 {
		t.Fatalf("two rows of one kind must both be added: %+v", rep)
	}
	live := liveNames(rt)
	if live["grok-home-a"] != runtime.StatusRunning || live["grok-home-b"] != runtime.StatusRunning {
		t.Fatalf("both rows must run: %v", live)
	}
	if comp := liveComponents(rt); comp["grok-home-a"] != comp["grok-home-b"] {
		t.Fatalf("the two rows should share ONE connector descriptor: %v", comp)
	}

	// Status is reported per NAME, not per connector.
	statusByName := func() map[string]string {
		t.Helper()
		list, lerr := sr.ListSources(ctx)
		if lerr != nil {
			t.Fatalf("ListSources: %v", lerr)
		}
		out := map[string]string{}
		for _, e := range list {
			out[e.Name] = e.Status
			if e.Status == string(runtime.StatusRunning) && e.Component == "" {
				t.Errorf("a wired source must report the connector serving it: %+v", e)
			}
		}
		return out
	}
	if got := statusByName(); got["grok-home-a"] != "running" || got["grok-home-b"] != "running" {
		t.Fatalf("ListSources status by name = %v", got)
	}

	// Rotating A rotates only A.
	putRow(t, store, model.SourceDef{
		Name: "grok-home-a", Kind: "grok", Tenant: "acme", Enabled: true,
		Config: map[string]string{"config_path": "/fixtures/a2/config.toml"},
	})
	rep, _ = sr.reconcile(ctx)
	if len(rep.Rotated) != 1 || rep.Rotated[0] != "grok-home-a" || rep.Unchanged != 1 {
		t.Fatalf("rotate of one row = %+v, want only grok-home-a rotated", rep)
	}
	if live := liveNames(rt); live["grok-home-b"] != runtime.StatusRunning {
		t.Errorf("rotating A disturbed B: %v", live)
	}

	// A rejected apply for A (its connector refuses Open) leaves A's running
	// instance in place and never touches B — deny-closed, per source.
	res, perr := sr.PutSource(ctx, recAdmin(), api.SourceRosterInput{
		Name: "grok-home-a", Kind: "openfail", Tenant: "acme", Enabled: true,
	})
	if perr != nil {
		t.Fatalf("PutSource: %v", perr)
	}
	if !res.Persisted || res.Applied {
		t.Fatalf("a failed Open must persist and NOT apply: %+v", res)
	}
	live = liveNames(rt)
	if live["grok-home-a"] != runtime.StatusRunning {
		t.Errorf("the rejected replacement took down the running source: %v", live)
	}
	if live["grok-home-b"] != runtime.StatusRunning {
		t.Errorf("a rejected apply for A disturbed B: %v", live)
	}

	// Deleting A stops exactly A.
	if _, derr := sr.DeleteSource(ctx, recAdmin(), "grok-home-a"); derr != nil {
		t.Fatalf("DeleteSource: %v", derr)
	}
	live = liveNames(rt)
	if _, present := live["grok-home-a"]; present {
		t.Error("the deleted row is still wired")
	}
	if live["grok-home-b"] != runtime.StatusRunning {
		t.Errorf("deleting A stopped B: %v", live)
	}
	if got := statusByName(); got["grok-home-b"] != "running" {
		t.Errorf("B's reported status after A's delete = %v", got)
	}
}

// TestRosterRowNamedLikeAModuleIsRefusedWithoutReplacingIt: the registration name
// space is shared with modules and outputs on purpose. A row that claims a name a
// module already owns is refused honestly — and the module keeps its name, its
// type and its status. A source that could displace a module would be a much worse
// bug than the one this lot fixes.
func TestRosterRowNamedLikeAModuleIsRefusedWithoutReplacingIt(t *testing.T) {
	ctx := context.Background()
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	mod := &instanceFakeModule{name: "governance"}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatalf("add module: %v", err)
	}
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, rt.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
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
		return sr.rt.PrepareInProcSource(&recFakeSource{name: "olivares." + def.Kind}), sdk.Config{Settings: def.Config}, ""
	}

	putRow(t, srcStore, model.SourceDef{Name: "governance", Kind: "vault", Tenant: "acme", Enabled: true})
	rep, rerr := sr.reconcile(ctx)
	if rerr != nil {
		t.Fatalf("reconcile: %v", rerr)
	}
	if !rejectedBy(rep, "governance") {
		t.Fatalf("a row claiming a module's name must be rejected: %+v", rep)
	}
	if len(rep.Added) != 0 {
		t.Fatalf("nothing may be wired under a name a module owns: %+v", rep)
	}
	for _, cs := range rt.Status() {
		if cs.Name != "governance" {
			continue
		}
		if cs.Type != sdk.TypeModule {
			t.Fatalf("the name %q is now a %q: the source replaced the module", cs.Name, cs.Type)
		}
		if cs.Status != runtime.StatusRunning {
			t.Errorf("the module lost its status to a refused source: %v", cs.Status)
		}
	}
	if !mod.started {
		t.Error("the module was disturbed by the refused source")
	}
}

// instanceFakeModule is a module that only needs to exist, start and hold a name.
type instanceFakeModule struct {
	name    string
	started bool
}

func (m *instanceFakeModule) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: m.name, Type: sdk.TypeModule, APIVersion: sdk.APIVersion}
}
func (m *instanceFakeModule) Init(context.Context, sdk.Host) error { return nil }
func (m *instanceFakeModule) Start(context.Context) error          { m.started = true; return nil }
func (m *instanceFakeModule) Stop(context.Context) error           { return nil }

// --- acceptance 4: the persisted registry, across a restart -------------------

// TestPersistedRowsOfOneKindRebuildAfterRestart stops the runtime, CLOSES the
// database, reopens it from the same file with a fresh runtime and reconciler,
// and requires both rows to come back — reconstructed from the rows, not from
// anything the first process remembered. It also re-runs the boot-file seed with
// conflicting content to prove an existing roster is authoritative.
func TestPersistedRowsOfOneKindRebuildAfterRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "source-instances.db")

	open := func() (*runtime.Runtime, *auth.SourceStore, *sourceReconciler, store.Store) {
		t.Helper()
		rt := runtime.New(runtime.Options{Logger: quietLog()})
		st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dbPath}, rt.RegisterSchema)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
			t.Fatalf("ensure system tenant: %v", err)
		}
		if err := rt.Start(ctx); err != nil {
			t.Fatalf("start runtime: %v", err)
		}
		srcStore := auth.NewSourceStore(st)
		sr := newSourceReconciler(rt, srcStore, nil, nil, t.TempDir(), nil, quietLog())
		sr.prepare = func(_ context.Context, def model.SourceDef) (*runtime.PreparedSource, sdk.Config, string) {
			return sr.rt.PrepareInProcSource(&recFakeSource{name: "olivares." + def.Kind}), sdk.Config{Settings: def.Config}, ""
		}
		return rt, srcStore, sr, st
	}
	shut := func(rt *runtime.Runtime, st store.Store) {
		t.Helper()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rt.Stop(sctx); err != nil {
			t.Fatalf("stop runtime: %v", err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	}

	rt1, store1, sr1, st1 := open()
	if _, err := sr1.PutSource(ctx, recAdmin(), api.SourceRosterInput{
		Name: "grok-home-a", Kind: "grok", Tenant: "acme", Enabled: true,
		Config: map[string]string{"config_path": "/fixtures/a/config.toml", "token": "store:grok-a"},
	}); err != nil {
		t.Fatalf("persist A: %v", err)
	}
	if _, err := sr1.PutSource(ctx, recAdmin(), api.SourceRosterInput{
		Name: "grok-home-b", Kind: "grok", Tenant: "acme", Enabled: true,
		Config: map[string]string{"config_path": "/fixtures/b/config.toml", "token": "store:grok-b"},
	}); err != nil {
		t.Fatalf("persist B: %v", err)
	}
	if live := liveNames(rt1); live["grok-home-a"] != runtime.StatusRunning || live["grok-home-b"] != runtime.StatusRunning {
		t.Fatalf("both rows must run before the restart: %v", live)
	}
	rowsBefore, err := store1.List(ctx, auth.GlobalSourceScope)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}
	shut(rt1, st1)

	// Restart: new runtime, new reconciler (empty memory), same database file.
	rt2, store2, sr2, st2 := open()
	t.Cleanup(func() { shut(rt2, st2) })

	// The boot-file seed must NOT touch a roster that already has rows, even when
	// the file claims the same names with different configuration.
	seedSourceRosterIfEmpty(ctx, store2, sourcesConfig{Sources: []sourceSpec{
		{Name: "grok-home-a", Kind: "grok", Tenant: "other", Config: map[string]string{"config_path": "/fixtures/from-the-old-file.toml"}},
	}}, quietLog())

	rep, rerr := sr2.reconcile(ctx)
	if rerr != nil {
		t.Fatalf("reconcile after restart: %v", rerr)
	}
	if len(rep.Added) != 2 || len(rep.Rejected) != 0 {
		t.Fatalf("both rows must be rebuilt from the registry: %+v", rep)
	}
	if live := liveNames(rt2); live["grok-home-a"] != runtime.StatusRunning || live["grok-home-b"] != runtime.StatusRunning {
		t.Fatalf("both rows must run after the restart: %v", live)
	}

	list, lerr := sr2.ListSources(ctx)
	if lerr != nil {
		t.Fatalf("ListSources after restart: %v", lerr)
	}
	byName := map[string]api.SourceRosterEntry{}
	for _, e := range list {
		byName[e.Name] = e
	}
	if len(byName) != 2 {
		t.Fatalf("roster after restart has %d rows, want 2", len(byName))
	}
	for name, wantPath := range map[string]string{
		"grok-home-a": "/fixtures/a/config.toml",
		"grok-home-b": "/fixtures/b/config.toml",
	} {
		e, ok := byName[name]
		if !ok {
			t.Fatalf("row %q did not survive the restart", name)
		}
		if e.Status != string(runtime.StatusRunning) {
			t.Errorf("row %q status after restart = %q, want running", name, e.Status)
		}
		if e.Config["config_path"] != wantPath {
			t.Errorf("row %q config_path = %q, want the persisted %q (the old file seed overwrote it)", name, e.Config["config_path"], wantPath)
		}
		if e.Tenant != "acme" {
			t.Errorf("row %q tenant = %q; the old file seed changed a persisted row", name, e.Tenant)
		}
	}
	if byName["grok-home-a"].Config["token"] != "store:grok-a" || byName["grok-home-b"].Config["token"] != "store:grok-b" {
		t.Errorf("each row must keep its OWN credential reference: %q / %q",
			byName["grok-home-a"].Config["token"], byName["grok-home-b"].Config["token"])
	}

	// The rows themselves are untouched, including their identifiers: A keys the
	// runtime by NAME, and the persisted row keeps its own id across the restart.
	rowsAfter, err := store2.List(ctx, auth.GlobalSourceScope)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(rowsAfter) != len(rowsBefore) {
		t.Fatalf("the restart changed the number of rows: %d → %d", len(rowsBefore), len(rowsAfter))
	}
	for i := range rowsBefore {
		if rowsBefore[i].ID != rowsAfter[i].ID || rowsBefore[i].Name != rowsAfter[i].Name {
			t.Errorf("row %d changed identity across the restart: %v/%v → %v/%v",
				i, rowsBefore[i].Name, rowsBefore[i].ID, rowsAfter[i].Name, rowsAfter[i].ID)
		}
	}
}

// --- acceptance 5-6: a real first-party connector, two real config files ------

// TestTwoRealGrokSourcesReadTheirOwnConfig is the acceptance that does not use a
// fake anywhere in the source path: the real first-party `grok` connector, wired
// twice through the real reconciler prepare, each row pointing at its OWN
// config.toml written for this test. The two files say different things, and the
// observations that reach the bus say so per registration name.
//
// It reads local files and starts NO provider: no CLI is launched, no account is
// used and no session exists. A config path is a path on this host — it is not a
// credential and it does not prove anything about an authenticated account.
func TestTwoRealGrokSourcesReadTheirOwnConfig(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewInProc(eventbus.Options{Logger: quietLog()})
	rt := runtime.New(runtime.Options{Logger: quietLog(), Bus: bus})

	var mu sync.Mutex
	titles := map[string][]string{}
	subjects := map[string]map[string]bool{}
	got := make(chan struct{}, 64)
	sub, serr := bus.Subscribe([]event.Type{event.TypeFindingReported}, func(_ context.Context, e event.Event) error {
		f, ok := event.FindingOf(e)
		if !ok {
			return nil
		}
		mu.Lock()
		titles[e.Source] = append(titles[e.Source], f.Title)
		if subjects[e.Source] == nil {
			subjects[e.Source] = map[string]bool{}
		}
		subjects[e.Source][f.SubjectRef] = true
		mu.Unlock()
		got <- struct{}{}
		return nil
	})
	if serr != nil {
		t.Fatalf("subscribe: %v", serr)
	}
	t.Cleanup(sub.Unsubscribe)

	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, rt.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rt.Stop(sctx)
		_ = st.Close()
	})

	srcStore := auth.NewSourceStore(st)
	// The REAL prepare: buildInProcSource("grok") builds a fresh connector per row.
	sr := newSourceReconciler(rt, srcStore, nil, nil, t.TempDir(), nil, quietLog())

	home := func(profile string) map[string]string {
		t.Helper()
		dir := t.TempDir()
		cfg := filepath.Join(dir, "config.toml")
		if werr := os.WriteFile(cfg, []byte("[sandbox]\nprofile = \""+profile+"\"\n"), 0o600); werr != nil {
			t.Fatalf("write %s: %v", cfg, werr)
		}
		return map[string]string{
			"config_path": cfg,
			// Kept inside the fixture directory so the test never reads the host's
			// /etc: absent files are a normal, reported state for this connector.
			"requirements_path":   filepath.Join(dir, "requirements.toml"),
			"disabled_hooks_path": filepath.Join(dir, "disabled-hooks"),
		}
	}
	cfgA, cfgB := home("strict"), home("off")
	cfgA["agent_ref"] = "grok-home-a"
	cfgB["agent_ref"] = "grok-home-b"

	putRow(t, srcStore, model.SourceDef{Name: "grok-home-a", Kind: "grok", Tenant: "acme", Enabled: true, Config: cfgA})
	putRow(t, srcStore, model.SourceDef{Name: "grok-home-b", Kind: "grok", Tenant: "acme", Enabled: true, Config: cfgB})

	// Both rows preview cleanly first: plan and apply must agree about this case.
	for _, def := range []model.SourceDef{
		{Name: "grok-home-a", Kind: "grok", Tenant: "acme", Enabled: true, Config: cfgA},
		{Name: "grok-home-b", Kind: "grok", Tenant: "acme", Enabled: true, Config: cfgB},
	} {
		if c := checkSourceOffline(def, nil); !c.Valid {
			t.Fatalf("plan/validate refused %q before the apply: %#v", def.Name, c.Problems)
		}
	}

	rep, rerr := sr.reconcile(ctx)
	if rerr != nil {
		t.Fatalf("reconcile: %v", rerr)
	}
	if len(rep.Added) != 2 || len(rep.Rejected) != 0 {
		t.Fatalf("both real grok rows must wire: %+v", rep)
	}

	// grok is a batch source: each row runs Gather once and emits several findings.
	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		enough := len(titles["grok-home-a"]) > 0 && len(titles["grok-home-b"]) > 0
		mu.Unlock()
		if enough {
			break
		}
		select {
		case <-got:
		case <-deadline:
			mu.Lock()
			have := map[string]int{}
			for k, v := range titles {
				have[k] = len(v)
			}
			mu.Unlock()
			t.Fatalf("did not receive findings from both sources; have %v", have)
		}
	}
	// Let the rest of each pass land so the profile finding is certainly in.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	names := make([]string, 0, len(titles))
	for k := range titles {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "grok-home-a" || names[1] != "grok-home-b" {
		t.Fatalf("events must be attributed to the two registration names, got %v", names)
	}
	hasTitle := func(source, want string) bool {
		for _, ti := range titles[source] {
			if strings.Contains(ti, want) {
				return true
			}
		}
		return false
	}
	if !hasTitle("grok-home-a", "sandbox profile: strict") {
		t.Errorf("grok-home-a did not report ITS OWN config (strict): %v", titles["grok-home-a"])
	}
	if !hasTitle("grok-home-b", "sandbox profile: off") {
		t.Errorf("grok-home-b did not report ITS OWN config (off): %v", titles["grok-home-b"])
	}
	if hasTitle("grok-home-a", "sandbox profile: off") || hasTitle("grok-home-b", "sandbox profile: strict") {
		t.Error("the two sources read each other's configuration")
	}
	if !subjects["grok-home-a"]["grok-home-a"] || !subjects["grok-home-b"]["grok-home-b"] {
		t.Errorf("each source must carry its own configured subject: %v", subjects)
	}
}

// TestSourceInstancesEmitDistinctProvenance is the narrow provenance assertion,
// kept separate from the connector-specific one above: the sink stamps the
// REGISTRATION name on the events of a named source, and the signal dimension of
// the observation is not touched.
func TestSourceInstancesEmitDistinctProvenance(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewInProc(eventbus.Options{Logger: quietLog()})
	rt := runtime.New(runtime.Options{Logger: quietLog(), Bus: bus})

	seen := make(chan event.Event, 8)
	sub, err := bus.Subscribe([]event.Type{event.TypeEdgeObserved}, func(_ context.Context, e event.Event) error {
		seen <- e
		return nil
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(sub.Unsubscribe)

	for _, name := range []string{"edge-home-a", "edge-home-b"} {
		if aerr := rt.AddPollSourceNamed(name, &oneEdgeSource{origin: name}, sdk.Config{}, "acme", 0); aerr != nil {
			t.Fatalf("register %s: %v", name, aerr)
		}
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(sctx)
	})

	byOrigin := map[string]string{}
	for i := 0; i < 2; i++ {
		select {
		case e := <-seen:
			edge, ok := event.EdgeOf(e)
			if !ok {
				t.Fatalf("not an edge event: %+v", e)
			}
			if edge.Source != "" {
				t.Errorf("EdgeObservation.Source (the SIGNAL class) was overwritten with %q", edge.Source)
			}
			byOrigin[edge.OriginRef] = e.Source
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of 2 edges arrived: %v", i, byOrigin)
		}
	}
	if byOrigin["edge-home-a"] != "edge-home-a" || byOrigin["edge-home-b"] != "edge-home-b" {
		t.Fatalf("event.Source must be the registration name of the emitting source: %v", byOrigin)
	}
}

// oneEdgeSource emits a single edge naming itself, then returns.
type oneEdgeSource struct{ origin string }

func (s *oneEdgeSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "olivares.edgefake", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (s *oneEdgeSource) Open(context.Context, sdk.Config) error { return nil }
func (s *oneEdgeSource) Gather(ctx context.Context, sink sdk.Sink) error {
	return sink.Emit(ctx, sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: s.origin, ResourceRef: "public.t",
		Mode: sdkmodel.ModeRead, ObservedAt: time.Now().UTC(),
	})
}
func (s *oneEdgeSource) Close(context.Context) error { return nil }

// --- the file/collector path: wireSources and wireRoster ----------------------

// TestWireSourcesRegistersEachEntryUnderItsOwnName covers the third registration
// route — the operator's boot file, which is also what `collector` mode runs.
// Two entries of one kind must both wire under their own names, and an entry with
// no name must NOT be wired under the connector's descriptor instead.
func TestWireSourcesRegistersEachEntryUnderItsOwnName(t *testing.T) {
	var buf strings.Builder
	log := newTestLogger(&buf)
	rt := runtime.New(runtime.Options{Logger: quietLog()})

	wireSources(context.Background(), rt, sourcesConfig{Sources: []sourceSpec{
		{Name: "grok-home-a", Kind: "grok", Tenant: "acme", Config: map[string]string{"config_path": "/fixtures/a/config.toml"}},
		{Name: "grok-home-b", Kind: "grok", Tenant: "acme", Config: map[string]string{"config_path": "/fixtures/b/config.toml"}},
		{Name: "", Kind: "grok", Tenant: "acme"},
	}}, t.TempDir(), nil, log)

	inv := map[string]string{}
	for _, s := range rt.LiveSourceInventory() {
		inv[s.Name] = s.Component
	}
	if len(inv) != 2 {
		t.Fatalf("wireSources registered %d sources, want the two named entries: %v", len(inv), inv)
	}
	if inv["grok-home-a"] != "olivares.grok" || inv["grok-home-b"] != "olivares.grok" {
		t.Fatalf("both entries must register under their own name, serving one connector: %v", inv)
	}
	if _, present := inv["olivares.grok"]; present {
		t.Error("a nameless entry was wired under the connector's descriptor (the fallback this lot forbids)")
	}
	if !strings.Contains(buf.String(), "source has no name") {
		t.Errorf("a nameless entry must warn honestly; log = %q", buf.String())
	}
}

// TestWireRosterWiresTwoIdpEntriesAsSeparateSources: okta and entra are two
// entries served by ONE connector (Descriptor olivares.idp). Before, the second
// as_source registration was refused as a duplicate and the operator silently ran
// one directory's grant scan. Now each registers under its entry name.
func TestWireRosterWiresTwoIdpEntriesAsSeparateSources(t *testing.T) {
	var buf strings.Builder
	log := newTestLogger(&buf)
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	gov := governance.New()

	wireRoster(context.Background(), rt, gov, newWifGraphAdapter(log), sourcesConfig{Identity: []identitySpec{
		{Name: "okta-corp", Kind: "idp", Tenant: "acme", AsSource: true, Config: map[string]string{
			"provider": "okta", "base_url": "https://okta.example",
		}},
		{Name: "entra-corp", Kind: "idp", Tenant: "acme", AsSource: true, Config: map[string]string{
			"provider": "entra",
		}},
	}}, nil, log)

	inv := map[string]string{}
	for _, s := range rt.LiveSourceInventory() {
		inv[s.Name] = s.Component
	}
	if len(inv) != 2 {
		t.Fatalf("both idp entries must wire as sources, got %v (log = %q)", inv, buf.String())
	}
	if inv["okta-corp"] != "olivares.idp" || inv["entra-corp"] != "olivares.idp" {
		t.Fatalf("two directories served by one connector must be two named sources: %v", inv)
	}
	if strings.Contains(buf.String(), "could not also be wired as a source") {
		t.Errorf("an entry was refused as a duplicate; log = %q", buf.String())
	}
}

func newTestLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// TestTwoGrokSourcesCollidingOnAPortFailHonestly: independent sources are not
// magically independent about the machine they share. Two receivers cannot both
// bind one address, and when that happens the answer must be the honest one — the
// second is REJECTED with its own reason, the first keeps running, and nothing
// invents a different port or reports the refused source as active.
func TestTwoGrokSourcesCollidingOnAPortFailHonestly(t *testing.T) {
	ctx := context.Background()
	rt := runtime.New(runtime.Options{Logger: quietLog()})
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, rt.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rt.Stop(sctx)
		_ = st.Close()
	})

	// A loopback port of this test's own choosing: bound to learn the number, then
	// released so the first source can take it for real.
	probe, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Skipf("cannot reserve a loopback port here: %v", lerr)
	}
	addr := probe.Addr().String()
	if cerr := probe.Close(); cerr != nil {
		t.Fatalf("release the probe listener: %v", cerr)
	}

	srcStore := auth.NewSourceStore(st)
	sr := newSourceReconciler(rt, srcStore, nil, nil, t.TempDir(), nil, quietLog())

	receiver := func(name string) model.SourceDef {
		dir := t.TempDir()
		return model.SourceDef{
			Name: name, Kind: "grok", Tenant: "acme", Enabled: true,
			Config: map[string]string{
				"agent_ref":           name,
				"config_path":         filepath.Join(dir, "config.toml"),
				"requirements_path":   filepath.Join(dir, "requirements.toml"),
				"disabled_hooks_path": filepath.Join(dir, "disabled-hooks"),
				"otlp_http":           "true",
				"otlp_http_addr":      addr,
			},
		}
	}
	putRow(t, srcStore, receiver("grok-recv-a"))
	putRow(t, srcStore, receiver("grok-recv-b"))

	rep, rerr := sr.reconcile(ctx)
	if rerr != nil {
		t.Fatalf("reconcile: %v", rerr)
	}
	if len(rep.Added) != 1 {
		t.Fatalf("exactly one receiver can hold the address, got added=%v rejected=%v", rep.Added, rep.Rejected)
	}
	winner := rep.Added[0]
	if len(rep.Rejected) != 1 {
		t.Fatalf("the second receiver must be rejected, not silently dropped: %+v", rep)
	}
	loser := rep.Rejected[0].Name
	if winner == loser {
		t.Fatalf("the same source was both added and rejected: %+v", rep)
	}
	if !strings.Contains(rep.Rejected[0].Reason, "could not be opened") {
		t.Errorf("the refusal must name the Open failure, got %q", rep.Rejected[0].Reason)
	}

	// grok is a BATCH source: one Gather pass per registration, so a healthy one
	// reads running or stopped — never failed, and never absent.
	wired := func(st runtime.Status) bool { return st == runtime.StatusRunning || st == runtime.StatusStopped }
	live := liveNames(rt)
	if got, ok := live[winner]; !ok || !wired(got) {
		t.Errorf("the source that took the address must stay wired and healthy: %v", live)
	}
	if _, present := live[loser]; present {
		t.Errorf("the refused source must NOT be wired: %v", live)
	}
	list, lerr2 := sr.ListSources(ctx)
	if lerr2 != nil {
		t.Fatalf("ListSources: %v", lerr2)
	}
	for _, e := range list {
		switch e.Name {
		case winner:
			if !wired(runtime.Status(e.Status)) {
				t.Errorf("%s status = %q, want a wired state", e.Name, e.Status)
			}
		case loser:
			if e.Status != "not_wired" {
				t.Errorf("%s status = %q; a refused source must never read as active", e.Name, e.Status)
			}
		}
	}
}
