// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func noEnv(string) string { return "" }

// The node identity is generated once, atomically, privately, and read back on
// every later boot; an explicit override wins; a malformed override refuses; an
// unusable file is never overwritten; no local state means no identity.
func TestResolveExecutionEnvironmentRef(t *testing.T) {
	dir := t.TempDir()
	first, err := resolveExecutionEnvironmentRef(dir, true, noEnv, quietLog())
	if err != nil || !strings.HasPrefix(first, executionEnvironmentPrefix) {
		t.Fatalf("first = %q %v", first, err)
	}
	st, err := os.Stat(filepath.Join(dir, executionEnvironmentFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("identity file mode = %v %v, want 0600", st, err)
	}
	second, err := resolveExecutionEnvironmentRef(dir, true, noEnv, quietLog())
	if err != nil || second != first {
		t.Fatalf("second boot = %q %v, want the same %q", second, err, first)
	}
	// Concurrent first boots of one node converge on ONE identity.
	race := t.TempDir()
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, err := resolveExecutionEnvironmentRef(race, true, noEnv, quietLog())
			mu.Lock()
			defer mu.Unlock()
			if err != nil || ref == "" {
				t.Errorf("concurrent resolve: %q %v", ref, err)
			}
			seen[ref] = true
		}()
	}
	wg.Wait()
	if len(seen) != 1 {
		t.Fatalf("concurrent first boots minted %d identities: %v", len(seen), seen)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(race, "."+executionEnvironmentFile+".*")); len(leftovers) != 0 {
		t.Fatalf("staging files left behind: %v", leftovers)
	}

	// Explicit override: applied verbatim, file untouched.
	withEnv := func(v string) func(string) string {
		return func(k string) string {
			if k == envExecutionEnvironmentID {
				return v
			}
			return ""
		}
	}
	if ref, err := resolveExecutionEnvironmentRef(dir, true, withEnv("node-blue-7"), quietLog()); err != nil || ref != "node-blue-7" {
		t.Fatalf("override = %q %v", ref, err)
	}
	if ref, _ := resolveExecutionEnvironmentRef(dir, true, noEnv, quietLog()); ref != first {
		t.Fatal("the override rewrote the persisted identity")
	}
	for _, bad := range []string{"has space", "a:b", "a|b", strings.Repeat("x", 300)} {
		if _, err := resolveExecutionEnvironmentRef(dir, true, withEnv(bad), quietLog()); err == nil {
			t.Fatalf("malformed override %q accepted", bad)
		}
	}
	// No node-local state: deny-closed, no error, nothing written.
	if ref, err := resolveExecutionEnvironmentRef("", false, noEnv, quietLog()); err != nil || ref != "" {
		t.Fatalf("no local state = %q %v", ref, err)
	}
	// An unusable file is reported, not overwritten.
	corrupt := t.TempDir()
	if err := os.WriteFile(filepath.Join(corrupt, executionEnvironmentFile), []byte("not valid: id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ref, err := resolveExecutionEnvironmentRef(corrupt, true, noEnv, quietLog()); err != nil || ref != "" {
		t.Fatalf("corrupt file = %q %v, want deny-closed", ref, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(corrupt, executionEnvironmentFile)); string(raw) != "not valid: id\n" {
		t.Fatal("the corrupt file was overwritten")
	}
}

// The binding port answers with the revision THIS node applied, for an actor
// that may administer the roster, and refuses everything else.
func TestProviderSourceResolverAnswersAppliedRevisionOnly(t *testing.T) {
	sr, srcStore, _ := newReconcilerHarness(t)
	sr.useEnvironmentRef("env-1")
	ctx := context.Background()
	tenant := model.NewID().String()
	applied, err := srcStore.Put(ctx, recAdmin(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: "claude-home-a", Kind: "claude", Tenant: tenant, Enabled: true, Config: map[string]string{"gen": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	refused, err := srcStore.Put(ctx, recAdmin(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: "never-opens", Kind: "openfail", Tenant: tenant, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sr.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	authz := auth.NewAuthorizer(nil)
	r := &providerSourceResolver{store: srcStore, sr: sr, authz: authz, env: "env-1"}

	got, err := r.ResolveAppliedSource(ctx, recAdmin(), model.TenantID(tenant), applied.ID)
	if err != nil || got.ID != applied.ID || got.Version != applied.Version || got.Name != "claude-home-a" || got.Tenant != tenant || got.EnvironmentRef != "env-1" || got.Kind != "claude" {
		t.Fatalf("resolved = %+v %v", got, err)
	}
	// A rotation that the runtime applied moves the answer; one it refused does not.
	rotated, err := srcStore.Put(ctx, recAdmin(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: "claude-home-a", Kind: "claude", Tenant: tenant, Enabled: true, Config: map[string]string{"gen": "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sr.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ResolveAppliedSource(ctx, recAdmin(), model.TenantID(tenant), applied.ID); err != nil || got.Version != rotated.Version {
		t.Fatalf("after rotation = %+v %v, want version %d", got, err, rotated.Version)
	}
	broken, err := srcStore.Put(ctx, recAdmin(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: "claude-home-a", Kind: "openfail", Tenant: tenant, Enabled: true, Config: map[string]string{"gen": "3"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sr.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ResolveAppliedSource(ctx, recAdmin(), model.TenantID(tenant), applied.ID); err != nil || got.Version != rotated.Version || got.Version == broken.Version {
		t.Fatalf("after a refused rotation = %+v %v, want the applied version %d", got, err, rotated.Version)
	}

	// Refusals: not an admin, unknown id, a row this node never applied, no environment.
	member := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID()}
	if _, err := r.ResolveAppliedSource(ctx, member, model.TenantID(tenant), applied.ID); !errors.Is(err, sessions.ErrSourceForbidden) {
		t.Fatalf("non-admin = %v", err)
	}
	if _, err := r.ResolveAppliedSource(ctx, recAdmin(), model.TenantID(tenant), model.NewID()); !errors.Is(err, sessions.ErrSourceNotFound) {
		t.Fatalf("unknown id = %v", err)
	}
	if _, err := r.ResolveAppliedSource(ctx, recAdmin(), model.TenantID(tenant), refused.ID); !errors.Is(err, sessions.ErrSourceNotFound) {
		t.Fatalf("never-applied row = %v", err)
	}
	if _, err := (&providerSourceResolver{store: srcStore, sr: sr, authz: authz}).ResolveAppliedSource(ctx, recAdmin(), model.TenantID(tenant), applied.ID); !errors.Is(err, sessions.ErrNoSourceResolver) {
		t.Fatalf("no environment = %v", err)
	}
	// Delete and recreate under the same name: the OLD id no longer resolves.
	if err := srcStore.Delete(ctx, recAdmin(), auth.GlobalSourceScope, "claude-home-a"); err != nil {
		t.Fatal(err)
	}
	recreated, err := srcStore.Put(ctx, recAdmin(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: "claude-home-a", Kind: "claude", Tenant: tenant, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sr.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveAppliedSource(ctx, recAdmin(), model.TenantID(tenant), applied.ID); !errors.Is(err, sessions.ErrSourceNotFound) {
		t.Fatalf("old id after recreate = %v", err)
	}
	if got, err := r.ResolveAppliedSource(ctx, recAdmin(), model.TenantID(tenant), recreated.ID); err != nil || got.ID != recreated.ID {
		t.Fatalf("recreated id = %+v %v", got, err)
	}
}
