// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// recFakeSource is a minimal streaming source for the reconciler tests: it blocks
// in Gather until its ctx is canceled, so its lifecycle is fully driven by the
// runtime's per-source ctx (the realistic shape).
type recFakeSource struct {
	name    string
	openErr error
}

func (f *recFakeSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: f.name, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (f *recFakeSource) Open(context.Context, sdk.Config) error { return f.openErr }
func (f *recFakeSource) Gather(ctx context.Context, _ sdk.Sink) error {
	<-ctx.Done()
	return ctx.Err()
}
func (f *recFakeSource) Close(context.Context) error { return nil }

// newReconcilerHarness builds a started runtime + a real SourceStore + a reconciler
// whose prepare seam yields fake connectors keyed by kind: kind "boom" is always
// refused (deny-closed), and every other kind maps to the connector identity
// "olivares.<kind>" — so two sources of the same kind collide on identity (the
// one-instance-per-kind reality), exactly as real in-proc connectors do.
func newReconcilerHarness(t *testing.T) (*sourceReconciler, *auth.SourceStore, *runtime.Runtime) {
	t.Helper()
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
		t.Fatalf("start runtime: %v", err)
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
		if def.Kind == "boom" {
			return nil, sdk.Config{}, "the boom kind is always refused"
		}
		fake := &recFakeSource{name: "olivares." + def.Kind}
		if def.Kind == "openfail" {
			// Open fails with a message that echoes a (fake) live credential — the
			// reconciler must NOT surface this verbatim.
			fake.openErr = errors.New("dial failed for token hvs.SUPERSECRETVALUE")
		}
		return sr.rt.PrepareInProcSource(fake), sdk.Config{Settings: def.Config}, ""
	}
	return sr, srcStore, rt
}

func putRow(t *testing.T, store *auth.SourceStore, def model.SourceDef) {
	t.Helper()
	if def.Scope.IsZero() {
		def.Scope = auth.GlobalSourceScope
	}
	if _, err := store.Put(context.Background(), recAdmin(), def); err != nil {
		t.Fatalf("seed row %q: %v", def.Name, err)
	}
}

func recAdmin() auth.Principal {
	return auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true, DisplayName: "test-admin"}
}

func liveNames(rt *runtime.Runtime) map[string]runtime.Status {
	out := map[string]runtime.Status{}
	for _, s := range rt.LiveSourceInventory() {
		out[s.Name] = s.Status
	}
	return out
}

func TestReconcileAddRotateRemove(t *testing.T) {
	sr, store, rt := newReconcilerHarness(t)
	ctx := context.Background()

	// Two enabled rows → both added.
	putRow(t, store, model.SourceDef{Name: "vault-prod", Kind: "vault", Tenant: "acme", Enabled: true, Config: map[string]string{"addr": "v1"}})
	putRow(t, store, model.SourceDef{Name: "claude", Kind: "claudeapi", Tenant: "acme", Enabled: true})

	rep, err := sr.reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(rep.Added) != 2 || len(rep.Rejected) != 0 {
		t.Fatalf("first reconcile = %+v, want 2 added 0 rejected", rep)
	}
	if len(rep.RequiresRestart) == 0 {
		t.Error("reload report must always state the requires-restart domains")
	}
	if live := liveNames(rt); live["olivares.vault"] != runtime.StatusRunning || live["olivares.claudeapi"] != runtime.StatusRunning {
		t.Fatalf("sources not running after reconcile: %v", live)
	}

	// Re-reconcile with no change → all unchanged.
	rep, _ = sr.reconcile(ctx)
	if rep.Unchanged != 2 || len(rep.Added) != 0 {
		t.Fatalf("no-op reconcile = %+v, want 2 unchanged", rep)
	}

	// Change vault-prod's config (fingerprint changes) → rotated.
	putRow(t, store, model.SourceDef{Name: "vault-prod", Kind: "vault", Tenant: "acme", Enabled: true, Config: map[string]string{"addr": "v2"}})
	rep, _ = sr.reconcile(ctx)
	if len(rep.Rotated) != 1 || rep.Rotated[0] != "vault-prod" || rep.Unchanged != 1 {
		t.Fatalf("rotate reconcile = %+v, want vault-prod rotated", rep)
	}
	if liveNames(rt)["olivares.vault"] != runtime.StatusRunning {
		t.Error("rotated source should still be running")
	}

	// Disable claude → removed from the live engine.
	putRow(t, store, model.SourceDef{Name: "claude", Kind: "claudeapi", Tenant: "acme", Enabled: false})
	rep, _ = sr.reconcile(ctx)
	if len(rep.Removed) != 1 || rep.Removed[0] != "claude" {
		t.Fatalf("disable reconcile = %+v, want claude removed", rep)
	}
	if _, present := liveNames(rt)["olivares.claudeapi"]; present {
		t.Error("disabled source must not be running")
	}

	// Delete vault-prod from the roster → removed.
	if err := store.Delete(ctx, recAdmin(), auth.GlobalSourceScope, "vault-prod"); err != nil {
		t.Fatal(err)
	}
	rep, _ = sr.reconcile(ctx)
	if len(rep.Removed) != 1 || rep.Removed[0] != "vault-prod" {
		t.Fatalf("delete reconcile = %+v, want vault-prod removed", rep)
	}
	if len(liveNames(rt)) != 0 {
		t.Errorf("engine should have no sources left, got %v", liveNames(rt))
	}
}

func TestReconcileDenyClosedAndCollision(t *testing.T) {
	sr, store, rt := newReconcilerHarness(t)
	ctx := context.Background()

	// A source that cannot be built is rejected, not wired; the engine is undisturbed.
	putRow(t, store, model.SourceDef{Name: "good", Kind: "vault", Tenant: "acme", Enabled: true})
	putRow(t, store, model.SourceDef{Name: "broken", Kind: "boom", Tenant: "acme", Enabled: true})
	rep, _ := sr.reconcile(ctx)
	if len(rep.Added) != 1 || rep.Added[0] != "good" {
		t.Fatalf("expected only 'good' added: %+v", rep)
	}
	if len(rep.Rejected) != 1 || rep.Rejected[0].Name != "broken" {
		t.Fatalf("expected 'broken' rejected: %+v", rep)
	}
	if liveNames(rt)["olivares.vault"] != runtime.StatusRunning {
		t.Error("the good source must run despite a sibling's rejection")
	}

	// Two sources of the SAME kind collide on connector identity → the second is
	// rejected honestly (never silently rotates the first out). ('broken' stays
	// rejected too — it is still an enabled, unbuildable row.)
	putRow(t, store, model.SourceDef{Name: "vault-two", Kind: "vault", Tenant: "acme", Enabled: true})
	rep, _ = sr.reconcile(ctx)
	if !rejectedBy(rep, "vault-two") {
		t.Fatalf("expected identity collision to reject vault-two: %+v", rep)
	}
	if len(rep.Added) != 0 || len(rep.Rotated) != 0 {
		t.Fatalf("a collision must not add or rotate anything: %+v", rep)
	}
	if liveNames(rt)["olivares.vault"] != runtime.StatusRunning {
		t.Error("the original source must keep running through a collision")
	}
}

func TestDeleteSourceTrimsName(t *testing.T) {
	sr, _, rt := newReconcilerHarness(t)
	ctx := context.Background()
	actor := recAdmin()

	if _, err := sr.PutSource(ctx, actor, api.SourceRosterInput{Name: "s1", Kind: "vault", Tenant: "acme", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if liveNames(rt)["olivares.vault"] != runtime.StatusRunning {
		t.Fatal("source not wired")
	}
	// A whitespace-padded name must still stop the LIVE source (regression: the
	// applied map is keyed by the trimmed name).
	res, err := sr.DeleteSource(ctx, actor, "  s1  ")
	if err != nil {
		t.Fatalf("DeleteSource(padded): %v", err)
	}
	if !res.Applied || res.Action != "removed" {
		t.Fatalf("DeleteSource(padded) = %+v, want applied removed", res)
	}
	if _, present := liveNames(rt)["olivares.vault"]; present {
		t.Error("a padded-name delete left the connector running (the HIGH bug)")
	}
}

func TestApplyOpenErrorIsGenericized(t *testing.T) {
	sr, _, _ := newReconcilerHarness(t)
	ctx := context.Background()
	res, err := sr.PutSource(ctx, recAdmin(), api.SourceRosterInput{Name: "x", Kind: "openfail", Tenant: "acme", Enabled: true})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if res.Applied {
		t.Fatal("a failed Open must not report applied")
	}
	if strings.Contains(res.Note, "SUPERSECRETVALUE") || strings.Contains(res.Note, "hvs.") {
		t.Errorf("the connector Open error (with a live secret) leaked into the API note: %q", res.Note)
	}
	if !strings.Contains(res.Note, "could not be opened") {
		t.Errorf("expected a generic open-failure note, got %q", res.Note)
	}
}

func TestSeedSourceRosterIfEmpty(t *testing.T) {
	_, store, _ := newReconcilerHarness(t)
	ctx := context.Background()

	cfg := sourcesConfig{Sources: []sourceSpec{
		{Name: "a", Kind: "vault", Tenant: "acme", Config: map[string]string{"addr": "v"}},
		{Name: "b", Kind: "claudeapi", Tenant: "acme", PollSeconds: 60},
	}}

	// Empty roster → the file sources are imported as enabled rows.
	seedSourceRosterIfEmpty(ctx, store, cfg, quietLog())
	rows, _ := store.List(ctx, auth.GlobalSourceScope)
	if len(rows) != 2 {
		t.Fatalf("seed imported %d rows, want 2", len(rows))
	}
	for _, r := range rows {
		if !r.Enabled {
			t.Errorf("seeded row %q should be enabled", r.Name)
		}
	}

	// Non-empty roster → a second seed is a no-op (the table is authoritative).
	seedSourceRosterIfEmpty(ctx, store, sourcesConfig{Sources: []sourceSpec{{Name: "c", Kind: "vault", Tenant: "x"}}}, quietLog())
	rows, _ = store.List(ctx, auth.GlobalSourceScope)
	if len(rows) != 2 {
		t.Fatalf("second seed changed the roster (%d rows); it must be a no-op", len(rows))
	}
}

func rejectedBy(rep api.SourceReloadReport, name string) bool {
	for _, r := range rep.Rejected {
		if r.Name == name {
			return true
		}
	}
	return false
}

func TestPutAndDeleteSourceApplyLive(t *testing.T) {
	sr, _, rt := newReconcilerHarness(t)
	ctx := context.Background()
	actor := recAdmin()

	// PutSource persists AND applies live.
	res, err := sr.PutSource(ctx, actor, api.SourceRosterInput{Name: "s1", Kind: "vault", Tenant: "acme", Enabled: true})
	if err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if !res.Persisted || !res.Applied || res.Action != "added" {
		t.Fatalf("PutSource result = %+v, want persisted+applied+added", res)
	}
	if liveNames(rt)["olivares.vault"] != runtime.StatusRunning {
		t.Fatal("PutSource did not wire the source live")
	}

	// Editing it rotates live.
	res, _ = sr.PutSource(ctx, actor, api.SourceRosterInput{Name: "s1", Kind: "vault", Tenant: "acme", Enabled: true, Config: map[string]string{"x": "y", "mode": "live"}})
	if res.Action != "rotated" || !res.Applied {
		t.Fatalf("edit result = %+v, want rotated+applied", res)
	}

	// ListSources reports it running.
	list, _ := sr.ListSources(ctx)
	if len(list) != 1 || list[0].Status != string(runtime.StatusRunning) || list[0].SourceMode != "live" {
		t.Fatalf("ListSources = %+v, want one running live source", list)
	}

	// A persisted-but-rejected live apply is honest: persisted true, applied false.
	res, _ = sr.PutSource(ctx, actor, api.SourceRosterInput{Name: "s2", Kind: "boom", Tenant: "acme", Enabled: true})
	if !res.Persisted || res.Applied || res.Note == "" {
		t.Fatalf("rejected live apply = %+v, want persisted+!applied+note", res)
	}

	// DeleteSource removes it live.
	res, err = sr.DeleteSource(ctx, actor, "s1")
	if err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	if !res.Applied || res.Action != "removed" {
		t.Fatalf("DeleteSource = %+v", res)
	}
	if _, present := liveNames(rt)["olivares.vault"]; present {
		t.Error("DeleteSource did not stop the live source")
	}
}
