// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// Several sources of ONE connector kind, each with its own life.
//
// The defect these cover is not subtle: the runtime keyed a source by its
// connector's Descriptor name, so a second roster row of the same kind could not
// be registered at all — it collided with the first and was refused. The fix is to
// give the composition root an EXPLICIT registration name and keep the descriptor
// beside it as what it always was: the connector's type. Everything here is about
// the two staying apart — reservation, lookup, rotation, removal, config and the
// provenance stamped on the events.
//
// What these do NOT show, and the delivery must not claim: two isolated end-to-end
// provider sessions. Two INDEPENDENT SOURCES is the whole result.

// twinSource is two-of-a-kind on purpose: every instance reports the SAME
// Descriptor name, reproducing the shared connector identity that used to make a
// sibling impossible. Instances are otherwise completely separate objects.
type twinSource struct {
	descriptor string
	label      string // which instance this is; rides on the observation
	openErr    error

	opens   atomic.Int32
	closes  atomic.Int32
	gathers atomic.Int32

	mu      sync.Mutex
	openCfg map[string]string

	entered chan struct{}
	once    sync.Once
}

func newTwinSource(descriptor, label string) *twinSource {
	return &twinSource{descriptor: descriptor, label: label, entered: make(chan struct{})}
}

func (s *twinSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: s.descriptor, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}

func (s *twinSource) Open(_ context.Context, cfg sdk.Config) error {
	if s.openErr != nil {
		return s.openErr
	}
	s.mu.Lock()
	s.openCfg = cfg.Settings
	s.mu.Unlock()
	s.opens.Add(1)
	return nil
}

// Gather emits ONE edge carrying this instance's label, then streams (blocks
// until its own ctx is canceled) so the source stays running and a live
// remove/rotate exercises the real cancel → drain → Close sequence.
func (s *twinSource) Gather(ctx context.Context, sink sdk.Sink) error {
	s.gathers.Add(1)
	s.once.Do(func() { close(s.entered) })
	err := sink.Emit(ctx, model.EdgeObservation{
		OriginKind: "agent", OriginRef: s.label, ResourceRef: "public.t",
		Mode: model.ModeRead, ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (s *twinSource) Close(context.Context) error { s.closes.Add(1); return nil }

// seenConfig reports what THIS instance was Opened with.
func (s *twinSource) seenConfig(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openCfg[key]
}

func (s *twinSource) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-s.entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("source %q never ran Gather", s.label)
	}
}

// collectEdges reads n edge events, returning event.Source → the edge's OriginRef
// (which identifies WHICH connector instance produced it).
func collectEdges(t *testing.T, got <-chan event.Event, n int) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i := 0; i < n; i++ {
		select {
		case e := <-got:
			edge, ok := event.EdgeOf(e)
			if !ok {
				t.Fatalf("event %d is not an edge: %+v", i, e)
			}
			if e.Tenant != "acme" {
				t.Errorf("event from %q carries tenant %q, want acme", e.Source, e.Tenant)
			}
			// EdgeObservation.Source is the class of SIGNAL, a different dimension:
			// the registration name must not have leaked into it.
			if edge.Source != "" {
				t.Errorf("EdgeObservation.Source was rewritten to %q; the source label must not touch the signal dimension", edge.Source)
			}
			out[e.Source] = edge.OriginRef
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d edges arrived; have %v", i, n, out)
		}
	}
	return out
}

func inventoryOf(rt *runtime.Runtime, name string) (runtime.SourceInfo, bool) {
	for _, s := range rt.LiveSourceInventory() {
		if s.Name == name {
			return s, true
		}
	}
	return runtime.SourceInfo{}, false
}

// TestTwoSourcesOfOneKindAtBootAreIndependent is the boot half of acceptance 1:
// two distinct connectors that SHARE a descriptor, registered before Start under
// two names, both open with their own configuration, both run, and each stamps its
// own registration name on the events it publishes.
func TestTwoSourcesOfOneKindAtBootAreIndependent(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 16)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatalf("add module: %v", err)
	}

	a := newTwinSource("olivares.twin", "home-a")
	b := newTwinSource("olivares.twin", "home-b")
	// Fictional credential REFERENCES, never values: the point is that each
	// registration resolves its own, not that a literal travels anywhere.
	cfgA := map[string]string{"config_path": "/fixtures/a/config.toml", "token": "store:twin-a"}
	cfgB := map[string]string{"config_path": "/fixtures/b/config.toml", "token": "store:twin-b"}

	if err := rt.AddPollSourceNamed("twin-home-a", a, sdk.Config{Settings: cfgA}, "acme", 0); err != nil {
		t.Fatalf("register twin-home-a: %v", err)
	}
	if err := rt.AddPollSourceNamed("twin-home-b", b, sdk.Config{Settings: cfgB}, "acme", 0); err != nil {
		t.Fatalf("register twin-home-b: a second source of one connector kind must register: %v", err)
	}

	// Mutating the CALLER's map after registration must reach neither source: each
	// registration owns its own copy, and A and B never share one.
	cfgA["config_path"] = "/fixtures/hijacked/config.toml"

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	a.waitEntered(t)
	b.waitEntered(t)

	if got := a.seenConfig("config_path"); got != "/fixtures/a/config.toml" {
		t.Errorf("source A opened with config_path %q, want its own /fixtures/a/config.toml", got)
	}
	if got := b.seenConfig("config_path"); got != "/fixtures/b/config.toml" {
		t.Errorf("source B opened with config_path %q, want its own /fixtures/b/config.toml", got)
	}
	if a.seenConfig("token") == b.seenConfig("token") {
		t.Error("both sources resolved the same credential reference; the kind must never select a credential")
	}

	byName := collectEdges(t, mod.got, 2)
	want := map[string]string{"twin-home-a": "home-a", "twin-home-b": "home-b"}
	for src, origin := range want {
		if byName[src] != origin {
			t.Errorf("event.Source %q carried origin %q, want %q (have %v)", src, byName[src], origin, byName)
		}
	}

	// Inspection keeps both identities: distinct names, one shared component.
	ia, oka := inventoryOf(rt, "twin-home-a")
	ib, okb := inventoryOf(rt, "twin-home-b")
	if !oka || !okb {
		t.Fatalf("inventory lost a source: %+v", rt.LiveSourceInventory())
	}
	if ia.Component != "olivares.twin" || ib.Component != "olivares.twin" {
		t.Errorf("the connector descriptor must be retained separately, got %q/%q", ia.Component, ib.Component)
	}
	if ia.Status != runtime.StatusRunning || ib.Status != runtime.StatusRunning {
		t.Errorf("both sources must run: %v / %v", ia.Status, ib.Status)
	}

	var sources int
	for _, cs := range rt.Status() {
		if cs.Type != sdk.TypeSource {
			continue
		}
		sources++
		if cs.Component != "olivares.twin" {
			t.Errorf("Status()[%q].Component = %q, want the descriptor", cs.Name, cs.Component)
		}
	}
	if sources != 2 {
		t.Errorf("Status() reported %d sources, want 2", sources)
	}
}

// TestTwoSourcesOfOneKindLiveAddAreIndependent is the live half of acceptance 1:
// the same guarantee through AddSourceLiveNamed, after Start.
func TestTwoSourcesOfOneKindLiveAddAreIndependent(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 16)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatalf("add module: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	ctx := liveCtx(t)

	a := newTwinSource("olivares.twin", "home-a")
	b := newTwinSource("olivares.twin", "home-b")
	shared := map[string]string{"config_path": "/fixtures/a/config.toml"}
	if err := rt.AddSourceLiveNamed(ctx, "twin-home-a", a, sdk.Config{Settings: shared}, "acme", 0); err != nil {
		t.Fatalf("live add A: %v", err)
	}
	// Deliberately REUSE the caller's map for B after changing it: the two
	// registrations must not end up sharing it.
	shared["config_path"] = "/fixtures/b/config.toml"
	if err := rt.AddSourceLiveNamed(ctx, "twin-home-b", b, sdk.Config{Settings: shared}, "acme", 0); err != nil {
		t.Fatalf("live add B (a second source of one kind must be accepted): %v", err)
	}

	a.waitEntered(t)
	b.waitEntered(t)
	if got := a.seenConfig("config_path"); got != "/fixtures/a/config.toml" {
		t.Errorf("A's settings were mutated through a shared map: %q", got)
	}
	if got := b.seenConfig("config_path"); got != "/fixtures/b/config.toml" {
		t.Errorf("B opened with %q, want its own", got)
	}

	byName := collectEdges(t, mod.got, 2)
	if byName["twin-home-a"] != "home-a" || byName["twin-home-b"] != "home-b" {
		t.Errorf("live-added sources did not stamp their own registration names: %v", byName)
	}
}

// TestRotateFailAndRemoveIsolateTheSibling is acceptance 2 for in-process
// sources: rotating A touches only A, a failed Open leaves A's previous instance
// AND B running, and removing A drains and closes exactly A while B keeps its
// activity, its status and its own configuration.
func TestRotateFailAndRemoveIsolateTheSibling(t *testing.T) {
	rt := startedRuntime(t)
	ctx := liveCtx(t)

	a1 := newTwinSource("olivares.twin", "home-a1")
	b := newTwinSource("olivares.twin", "home-b")
	cfgA := sdk.Config{Settings: map[string]string{"config_path": "/fixtures/a/config.toml"}}
	cfgB := sdk.Config{Settings: map[string]string{"config_path": "/fixtures/b/config.toml"}}
	if err := rt.AddSourceLiveNamed(ctx, "twin-home-a", a1, cfgA, "acme", 0); err != nil {
		t.Fatalf("live add A: %v", err)
	}
	if err := rt.AddSourceLiveNamed(ctx, "twin-home-b", b, cfgB, "acme", 0); err != nil {
		t.Fatalf("live add B: %v", err)
	}
	a1.waitEntered(t)
	b.waitEntered(t)

	// A failed rotate of A is deny-closed: A's running instance stays, B is not touched.
	bad := newTwinSource("olivares.twin", "bad")
	bad.openErr = errors.New("dial failed for token hvs.FICTIONALVALUE")
	err := rt.ReplaceSourceLiveNamed(ctx, "twin-home-a", bad, cfgA, "acme", 0)
	if !errors.Is(err, runtime.ErrSourceOpenFailed) {
		t.Fatalf("failed rotate error = %v, want ErrSourceOpenFailed", err)
	}
	if a1.closes.Load() != 0 {
		t.Error("a rejected rotate closed the source it failed to replace")
	}
	if b.closes.Load() != 0 || b.opens.Load() != 1 {
		t.Error("a rejected rotate of A disturbed B")
	}
	if info, ok := inventoryOf(rt, "twin-home-a"); !ok || info.Status != runtime.StatusRunning {
		t.Errorf("A must keep running after a rejected rotate: %+v", info)
	}

	// A successful rotate replaces ONLY A.
	a2 := newTwinSource("olivares.twin", "home-a2")
	if err := rt.ReplaceSourceLiveNamed(ctx, "twin-home-a", a2, cfgA, "acme", 0); err != nil {
		t.Fatalf("rotate A: %v", err)
	}
	a2.waitEntered(t)
	if a1.closes.Load() != 1 {
		t.Errorf("the rotated-out instance was closed %d times, want exactly 1", a1.closes.Load())
	}
	if b.closes.Load() != 0 {
		t.Error("rotating A closed B")
	}
	if got := b.seenConfig("config_path"); got != "/fixtures/b/config.toml" {
		t.Errorf("B's configuration changed when A rotated: %q", got)
	}

	// Removing A drains and closes exactly A; B keeps running with its counters.
	gathersB := b.gathers.Load()
	if err := rt.RemoveSourceLive(ctx, "twin-home-a"); err != nil {
		t.Fatalf("remove A: %v", err)
	}
	if a2.closes.Load() != 1 {
		t.Errorf("removing A closed it %d times, want 1", a2.closes.Load())
	}
	if b.closes.Load() != 0 {
		t.Error("removing A closed B")
	}
	if b.gathers.Load() != gathersB {
		t.Error("removing A restarted B's Gather")
	}
	if _, ok := inventoryOf(rt, "twin-home-a"); ok {
		t.Error("the removed source is still in the inventory")
	}
	if info, ok := inventoryOf(rt, "twin-home-b"); !ok || info.Status != runtime.StatusRunning {
		t.Errorf("B must survive A's removal: %+v", info)
	}
}

// TestRegistrationNameRefusals is acceptance 3 at the runtime seam: a duplicate
// REGISTRATION name is still refused, a shared descriptor under two names is not,
// a name a module already owns is refused WITHOUT disturbing that module, and an
// empty name is an error rather than a silent fall back to the descriptor.
func TestRegistrationNameRefusals(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "governance", got: make(chan event.Event, 4)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatalf("add module: %v", err)
	}

	// Same descriptor, different names: accepted.
	if err := rt.AddPollSourceNamed("twin-a", newTwinSource("olivares.twin", "a"), sdk.Config{}, "acme", 0); err != nil {
		t.Fatalf("first named source: %v", err)
	}
	if err := rt.AddPollSourceNamed("twin-b", newTwinSource("olivares.twin", "b"), sdk.Config{}, "acme", 0); err != nil {
		t.Fatalf("second source of the same kind must be accepted: %v", err)
	}

	// Duplicate registration name: refused, with the historical wording.
	err := rt.AddPollSourceNamed("twin-a", newTwinSource("olivares.other", "c"), sdk.Config{}, "acme", 0)
	if err == nil || !strings.Contains(err.Error(), "duplicate component name") {
		t.Fatalf("duplicate registration name error = %v, want a duplicate refusal", err)
	}

	// A name a MODULE owns: refused, and the module is left exactly as it was.
	err = rt.AddPollSourceNamed("governance", newTwinSource("olivares.twin", "impostor"), sdk.Config{}, "acme", 0)
	if err == nil || !strings.Contains(err.Error(), "duplicate component name") {
		t.Fatalf("collision with a module name = %v, want a refusal", err)
	}

	// Empty / whitespace-only names are an error on every named entry point.
	for _, name := range []string{"", "   "} {
		if err := rt.AddPollSourceNamed(name, newTwinSource("olivares.twin", "x"), sdk.Config{}, "acme", 0); !errors.Is(err, runtime.ErrEmptyRegistrationName) {
			t.Errorf("AddPollSourceNamed(%q) = %v, want ErrEmptyRegistrationName", name, err)
		}
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	// The module the impostor tried to displace is still the owner of its name,
	// still initialized, still started and still reported as a module.
	if !mod.subscribed.Load() || !mod.startCalled.Load() {
		t.Error("the refused source disturbed the module that owns the name")
	}
	var moduleRows, sourceRows int
	for _, cs := range rt.Status() {
		if cs.Name != "governance" {
			if cs.Type == sdk.TypeSource {
				sourceRows++
			}
			continue
		}
		if cs.Type != sdk.TypeModule {
			t.Errorf("the name %q is reported as %q; the source replaced its owner", cs.Name, cs.Type)
		}
		moduleRows++
		if cs.Status != runtime.StatusRunning {
			t.Errorf("the module lost its status to a refused source: %v", cs.Status)
		}
	}
	if moduleRows != 1 {
		t.Errorf("the module appears %d times in Status(), want once", moduleRows)
	}
	if sourceRows != 2 {
		t.Errorf("%d sources registered, want exactly the two accepted ones", sourceRows)
	}

	// Live entry points refuse an empty name too, and a prepared source handed to
	// one is discarded rather than leaked.
	ctx := liveCtx(t)
	if err := rt.AddSourceLiveNamed(ctx, "", newTwinSource("olivares.twin", "x"), sdk.Config{}, "acme", 0); !errors.Is(err, runtime.ErrEmptyRegistrationName) {
		t.Errorf("AddSourceLiveNamed(\"\") = %v, want ErrEmptyRegistrationName", err)
	}
	if err := rt.ReplaceSourceLiveNamed(ctx, "", newTwinSource("olivares.twin", "x"), sdk.Config{}, "acme", 0); !errors.Is(err, runtime.ErrEmptyRegistrationName) {
		t.Errorf("ReplaceSourceLiveNamed(\"\") = %v, want ErrEmptyRegistrationName", err)
	}
	ps := rt.PrepareInProcSource(newTwinSource("olivares.twin", "x"))
	if err := rt.AddPreparedSourceNamed(ctx, "", ps, sdk.Config{}, "acme", 0); !errors.Is(err, runtime.ErrEmptyRegistrationName) {
		t.Errorf("AddPreparedSourceNamed(\"\") = %v, want ErrEmptyRegistrationName", err)
	}
	if err := rt.ReplacePreparedSourceNamed(ctx, "", ps, sdk.Config{}, "acme", 0); !errors.Is(err, runtime.ErrEmptyRegistrationName) {
		t.Errorf("ReplacePreparedSourceNamed(\"\") = %v, want ErrEmptyRegistrationName", err)
	}
	// The plugin loaders refuse before they touch the filesystem: the path below
	// does not exist, and the error must still be the empty-name one.
	if err := rt.LoadSourcePluginNamed("", "/nonexistent/plugin", sdk.Config{}, "acme"); !errors.Is(err, runtime.ErrEmptyRegistrationName) {
		t.Errorf("LoadSourcePluginNamed(\"\") = %v, want ErrEmptyRegistrationName", err)
	}
	if err := rt.LoadSourcePluginVerifiedNamed("", "/nonexistent/plugin", sdk.Config{}, "acme", "not-a-digest"); !errors.Is(err, runtime.ErrEmptyRegistrationName) {
		t.Errorf("LoadSourcePluginVerifiedNamed(\"\") = %v, want ErrEmptyRegistrationName", err)
	}
}

// TestLegacyRegistrationKeepsItsIdentityAndProvenance pins the compatibility half
// of the contract: the name-less entry points still register under the connector's
// Descriptor name, still refuse a second source of the same kind with the same
// duplicate error as before, and still stamp the descriptor on Event.Source.
func TestLegacyRegistrationKeepsItsIdentityAndProvenance(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	mod := &fakeModule{name: "counter", got: make(chan event.Event, 8)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatalf("add module: %v", err)
	}
	if err := rt.AddSource(newTwinSource("olivares.twin", "legacy"), sdk.Config{}, "acme"); err != nil {
		t.Fatalf("legacy add: %v", err)
	}
	err := rt.AddSource(newTwinSource("olivares.twin", "legacy-2"), sdk.Config{}, "acme")
	if err == nil || !strings.Contains(err.Error(), `duplicate component name "olivares.twin"`) {
		t.Fatalf("legacy duplicate error = %v, want the historical duplicate refusal", err)
	}
	if err := rt.AddSource(&emptyDescriptorSource{}, sdk.Config{}, "acme"); err == nil ||
		!strings.Contains(err.Error(), "component descriptor has empty Name") {
		t.Fatalf("legacy empty-descriptor error = %v, want the historical wording", err)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	if got := collectEdges(t, mod.got, 1); got["olivares.twin"] != "legacy" {
		t.Errorf("a legacy source must keep publishing under its descriptor, got %v", got)
	}
	if info, ok := inventoryOf(rt, "olivares.twin"); !ok || info.Component != "olivares.twin" {
		t.Errorf("legacy inventory entry = %+v, want name and component both the descriptor", info)
	}
}

// emptyDescriptorSource has no Descriptor name, so the legacy path must refuse it
// with the historical message rather than register something unnamed.
type emptyDescriptorSource struct{}

func (emptyDescriptorSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (emptyDescriptorSource) Open(context.Context, sdk.Config) error { return nil }
func (emptyDescriptorSource) Gather(context.Context, sdk.Sink) error { return nil }
func (emptyDescriptorSource) Close(context.Context) error            { return nil }

// TestConcurrentNamedSourcesOfOneKind is the focused race case: many sources of
// ONE connector kind are added, inspected and removed concurrently. Run under
// -race it exercises the shared name namespace, the index and the per-source
// lifecycle at once. The runtime serializes the mutations itself, so the invariant
// asserted here is the outcome, not the ordering: every name that was added and
// not removed is registered exactly once, under its own name.
func TestConcurrentNamedSourcesOfOneKind(t *testing.T) {
	rt := startedRuntime(t)
	const n = 12

	var wg sync.WaitGroup
	added := make([]atomic.Bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			name := fmt.Sprintf("twin-%02d", i)
			if err := rt.AddSourceLiveNamed(ctx, name, newTwinSource("olivares.twin", name), sdk.Config{Settings: map[string]string{"n": name}}, "acme", 0); err != nil {
				t.Errorf("add %s: %v", name, err)
				return
			}
			added[i].Store(true)
			// Read the inspection surfaces while others mutate.
			_ = rt.LiveSourceInventory()
			_ = rt.Status()
			_ = rt.SourceIsRegistered(name)
			if i%2 == 0 {
				if err := rt.RemoveSourceLive(ctx, name); err != nil {
					t.Errorf("remove %s: %v", name, err)
					return
				}
				added[i].Store(false)
			}
		}(i)
	}
	wg.Wait()

	live := inventoryNames(rt)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("twin-%02d", i)
		_, present := live[name]
		if want := added[i].Load(); present != want {
			t.Errorf("source %s present=%v, want %v", name, present, want)
		}
	}
	for _, s := range rt.LiveSourceInventory() {
		if s.Component != "olivares.twin" {
			t.Errorf("source %q lost its component identity: %q", s.Name, s.Component)
		}
	}
}

// --- the connector must still name itself, on every path ----------------------

// namelessSource is a well-formed connector in every respect EXCEPT that its
// Descriptor does not name it. It records whether it was ever Opened, because the
// contract is not merely "refused" but "refused before anything happened to it".
type namelessSource struct {
	opens  atomic.Int32
	closes atomic.Int32
}

func (s *namelessSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (s *namelessSource) Open(context.Context, sdk.Config) error { s.opens.Add(1); return nil }
func (s *namelessSource) Gather(ctx context.Context, _ sdk.Sink) error {
	<-ctx.Done()
	return ctx.Err()
}
func (s *namelessSource) Close(context.Context) error { s.closes.Add(1); return nil }

const emptyDescriptorErr = "runtime: component descriptor has empty Name"

// TestMalformedConnectorDescriptorIsRefusedBeforeStart covers the PRE-START paths.
//
// ⛔ THIS IS A REGRESSION BANK, and the regression was real. Separating the
// registration name from the connector descriptor removed the only place that ever
// checked the descriptor: every path reserved the REGISTRATION name, and "" was just
// a name nobody had taken. An independent overlay caught it on the live prepared
// path — the connector was Opened and registered with an empty Name and Component.
// The bank asserts the whole surface, not the one entry point that was reported.
func TestMalformedConnectorDescriptorIsRefusedBeforeStart(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})

	// Legacy, name-less: the historical error, verbatim.
	if err := rt.AddSource(&namelessSource{}, sdk.Config{}, "acme"); err == nil ||
		!strings.Contains(err.Error(), emptyDescriptorErr) {
		t.Fatalf("AddSource(nameless) = %v, want %q", err, emptyDescriptorErr)
	}
	if err := rt.AddPollSource(&namelessSource{}, sdk.Config{}, "acme", time.Second); err == nil ||
		!strings.Contains(err.Error(), emptyDescriptorErr) {
		t.Fatalf("AddPollSource(nameless) = %v, want %q", err, emptyDescriptorErr)
	}

	// NAMED: an explicit registration identity does not excuse a component without
	// one. Same error, and — the part that matters — the registration name is NOT
	// consumed by the refusal.
	if err := rt.AddPollSourceNamed("boot-slot", &namelessSource{}, sdk.Config{}, "acme", 0); err == nil ||
		!strings.Contains(err.Error(), emptyDescriptorErr) {
		t.Fatalf("AddPollSourceNamed(nameless) = %v, want %q", err, emptyDescriptorErr)
	}
	if err := rt.AddSourceNamed("boot-slot", newTwinSource("olivares.twin", "good"), sdk.Config{}, "acme"); err != nil {
		t.Fatalf("the refused connector reserved %q anyway: %v", "boot-slot", err)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	// Exactly the one good source is registered, and nothing is registered under "".
	inv := inventoryNames(rt)
	if len(inv) != 1 {
		t.Fatalf("inventory = %v, want only the one accepted source", inv)
	}
	if _, present := inv[""]; present {
		t.Fatal("a source is registered under the empty name")
	}
}

// TestMalformedConnectorDescriptorIsRefusedLive covers every POST-START path —
// legacy and named, add and prepared — and asserts the three properties that make
// the refusal a refusal: the historical error, no Open, and nothing wired.
func TestMalformedConnectorDescriptorIsRefusedLive(t *testing.T) {
	rt := startedRuntime(t)
	ctx := liveCtx(t)

	check := func(what string, bad *namelessSource, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), emptyDescriptorErr) {
			t.Fatalf("%s = %v, want %q", what, err, emptyDescriptorErr)
		}
		if n := bad.opens.Load(); n != 0 {
			t.Fatalf("%s Opened the malformed connector %d times; the refusal must come first", what, n)
		}
	}

	bad := &namelessSource{}
	check("AddSourceLive(nameless)", bad, rt.AddSourceLive(ctx, bad, sdk.Config{}, "acme", 0))

	bad = &namelessSource{}
	check("AddSourceLiveNamed(nameless)", bad, rt.AddSourceLiveNamed(ctx, "slot-a", bad, sdk.Config{}, "acme", 0))

	// The exact case that overlay reproduced: the LEGACY prepared path.
	bad = &namelessSource{}
	check("AddPreparedSource(nameless)", bad, rt.AddPreparedSource(ctx, rt.PrepareInProcSource(bad), sdk.Config{}, "acme", 0))

	bad = &namelessSource{}
	check("AddPreparedSourceNamed(nameless)", bad, rt.AddPreparedSourceNamed(ctx, "slot-a", rt.PrepareInProcSource(bad), sdk.Config{}, "acme", 0))

	if inv := inventoryNames(rt); len(inv) != 0 {
		t.Fatalf("a refused connector was wired: %v", inv)
	}
	// And none of the refusals consumed the registration name they were offered.
	if err := rt.AddSourceLiveNamed(ctx, "slot-a", newTwinSource("olivares.twin", "good"), sdk.Config{}, "acme", 0); err != nil {
		t.Fatalf("a refused connector reserved %q anyway: %v", "slot-a", err)
	}
}

// TestMalformedReplacementLeavesTheRunningSourceAndItsSiblings is the rotate half,
// and it is the one with teeth: a replacement is the only operation that can DESTROY
// a healthy source. A candidate that does not name itself must be refused before it
// is Opened and before the running instance is touched — so the source keeps running,
// keeps its connector, and its siblings never notice.
func TestMalformedReplacementLeavesTheRunningSourceAndItsSiblings(t *testing.T) {
	rt := startedRuntime(t)
	ctx := liveCtx(t)

	a := newTwinSource("olivares.twin", "home-a")
	b := newTwinSource("olivares.twin", "home-b")
	if err := rt.AddSourceLiveNamed(ctx, "twin-home-a", a, sdk.Config{Settings: map[string]string{"n": "a"}}, "acme", 0); err != nil {
		t.Fatalf("add A: %v", err)
	}
	if err := rt.AddSourceLiveNamed(ctx, "twin-home-b", b, sdk.Config{Settings: map[string]string{"n": "b"}}, "acme", 0); err != nil {
		t.Fatalf("add B: %v", err)
	}
	a.waitEntered(t)
	b.waitEntered(t)

	for _, tc := range []struct {
		what string
		call func(bad *namelessSource) error
	}{
		{"ReplaceSourceLiveNamed", func(bad *namelessSource) error {
			return rt.ReplaceSourceLiveNamed(ctx, "twin-home-a", bad, sdk.Config{}, "acme", 0)
		}},
		{"ReplacePreparedSourceNamed", func(bad *namelessSource) error {
			return rt.ReplacePreparedSourceNamed(ctx, "twin-home-a", rt.PrepareInProcSource(bad), sdk.Config{}, "acme", 0)
		}},
	} {
		bad := &namelessSource{}
		err := tc.call(bad)
		if err == nil || !strings.Contains(err.Error(), emptyDescriptorErr) {
			t.Fatalf("%s(nameless) = %v, want %q", tc.what, err, emptyDescriptorErr)
		}
		if n := bad.opens.Load(); n != 0 {
			t.Fatalf("%s Opened the malformed candidate %d times", tc.what, n)
		}
		if a.closes.Load() != 0 {
			t.Fatalf("%s closed the running source it failed to replace", tc.what)
		}
		if b.closes.Load() != 0 {
			t.Fatalf("%s disturbed the sibling", tc.what)
		}
		live := inventoryNames(rt)
		if live["twin-home-a"] != runtime.StatusRunning || live["twin-home-b"] != runtime.StatusRunning {
			t.Fatalf("after %s both sources must still run: %v", tc.what, live)
		}
		if comp := componentOf(rt, "twin-home-a"); comp != "olivares.twin" {
			t.Fatalf("after %s the source's connector changed to %q", tc.what, comp)
		}
	}

	// The LEGACY replace keeps its own historical answer: its lookup key IS the
	// descriptor, so a name-less connector misses and reports "no such source" —
	// the same error it always did, and still without Opening anything.
	legacy := &namelessSource{}
	err := rt.ReplaceSourceLive(ctx, legacy, sdk.Config{}, "acme", 0)
	if !errors.Is(err, runtime.ErrSourceNotFound) {
		t.Fatalf("ReplaceSourceLive(nameless) = %v, want ErrSourceNotFound (the historical answer)", err)
	}
	if legacy.opens.Load() != 0 {
		t.Fatalf("the legacy replace Opened the malformed candidate %d times", legacy.opens.Load())
	}
	if a.closes.Load() != 0 || b.closes.Load() != 0 {
		t.Fatal("the legacy replace disturbed a running source")
	}
}

// componentOf reports the connector descriptor currently serving a registration.
func componentOf(rt *runtime.Runtime, name string) string {
	info, ok := inventoryOf(rt, name)
	if !ok {
		return ""
	}
	return info.Component
}
