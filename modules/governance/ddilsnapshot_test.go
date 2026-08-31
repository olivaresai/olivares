// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestActivePolicySnapshot(t *testing.T) {
	ctx := context.Background()
	gov := New()
	st, err := engine.Open(ctx, store.Config{
		Engine: store.EngineSQLite,
		DSN:    ":memory:",
		Debug:  true,
	}, gov.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant, emptyTenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if err != nil {
			return err
		}
		tenant = org.TenantID
		emptyOrg, err := sys.CreateOrg(ctx, model.Org{Name: "Empty", Slug: "empty", Status: model.StatusActive})
		if err == nil {
			emptyTenant = emptyOrg.TenantID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	free := `permit(principal, action == Action::"agent:read", resource);`
	managed := `permit(principal in Role::"viewer", action == Action::"agent:write", resource);`
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, _, err := appendRevision(ctx, sc, surfaceCedar, free, "test", true, true, ""); err != nil {
			return err
		}
		_, _, err := appendRevision(ctx, sc, surfaceCedarManaged, managed, "test", true, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	want := free + "\n\n" + managed
	snapshot, revision, ok, err := ActivePolicySnapshot(ctx, st, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("active Cedar surfaces must produce a snapshot")
	}
	if snapshot != want {
		t.Fatalf("snapshot mismatch\n got: %q\nwant: %q", snapshot, want)
	}
	wantSum := sha256.Sum256([]byte(want))
	wantRevision := "sha256:" + hex.EncodeToString(wantSum[:])
	if revision != wantRevision {
		t.Fatalf("revision = %q, want %q", revision, wantRevision)
	}

	if snapshot, revision, ok, err := ActivePolicySnapshot(ctx, st, emptyTenant); err != nil {
		t.Fatal(err)
	} else if ok || snapshot != "" || revision != "" {
		t.Fatalf("empty tenant = (%q, %q, %v), want empty snapshot/revision and ok=false", snapshot, revision, ok)
	}

	adopted := `forbid(principal, action == Action::"agent:delete", resource);`
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, _, err := appendRevision(ctx, sc, surfaceCedarDDIL, adopted, "ddil-import", true, true, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want = want + "\n\n" + adopted
	snapshot, revision, ok, err = ActivePolicySnapshot(ctx, st, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || snapshot != want {
		t.Fatalf("snapshot with cedar-ddil = (%q, %v), want (%q, true)", snapshot, ok, want)
	}
	wantSum = sha256.Sum256([]byte(want))
	wantRevision = "sha256:" + hex.EncodeToString(wantSum[:])
	if revision != wantRevision {
		t.Fatalf("cedar-ddil revision = %q, want %q", revision, wantRevision)
	}
}
