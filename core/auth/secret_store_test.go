// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// secretTestSealer is an AAD-binding stand-in for the engine sealer.
type secretTestSealer struct{}

func (secretTestSealer) Seal(_ context.Context, scope model.TenantID, pt []byte) (string, error) {
	return "s:" + scope.String() + ":" + string(pt), nil
}

func (secretTestSealer) Open(_ context.Context, scope model.TenantID, sealed string) ([]byte, error) {
	pre := "s:" + scope.String() + ":"
	if len(sealed) < len(pre) || sealed[:len(pre)] != pre {
		return nil, errors.New("secretTestSealer: wrong scope")
	}
	return []byte(sealed[len(pre):]), nil
}

func adminActor() auth.Principal {
	return auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true, DisplayName: "admin"}
}

func TestSecretStorePutGetResolve(t *testing.T) {
	st := testStore(t)
	svc := auth.NewSecretStore(st, secretTestSealer{})
	ctx := context.Background()
	scope := auth.GlobalSecretScope

	view, err := svc.Put(ctx, adminActor(), scope, "gdrive/token", "the-secret-value", "GDrive ingest token")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if view.Name != "gdrive/token" || view.Hint == "" || view.Description != "GDrive ingest token" {
		t.Fatalf("view = %+v", view)
	}

	// Resolve returns the live plaintext.
	val, err := svc.Resolve(ctx, scope, "gdrive/token")
	if err != nil || !bytes.Equal(val, []byte("the-secret-value")) {
		t.Fatalf("Resolve = (%q,%v)", val, err)
	}

	// Get never returns the value.
	got, ok, err := svc.Get(ctx, scope, "gdrive/token")
	if err != nil || !ok || got.Hint != view.Hint {
		t.Fatalf("Get = (%+v,%v,%v)", got, ok, err)
	}

	// Update the value; hint changes, description editable; empty value keeps it.
	v2, err := svc.Put(ctx, adminActor(), scope, "gdrive/token", "rotated-value", "GDrive ingest token")
	if err != nil {
		t.Fatalf("Put update: %v", err)
	}
	if v2.Hint == view.Hint {
		t.Error("hint should change when the value rotates")
	}
	val2, _ := svc.Resolve(ctx, scope, "gdrive/token")
	if !bytes.Equal(val2, []byte("rotated-value")) {
		t.Fatalf("after rotate Resolve = %q", val2)
	}

	// Empty value keeps the stored secret (description-only edit).
	if _, err := svc.Put(ctx, adminActor(), scope, "gdrive/token", "", "new description"); err != nil {
		t.Fatalf("Put empty-value edit: %v", err)
	}
	val3, _ := svc.Resolve(ctx, scope, "gdrive/token")
	if !bytes.Equal(val3, []byte("rotated-value")) {
		t.Fatalf("empty-value edit should keep the secret, got %q", val3)
	}
}

func TestSecretStoreListAndDelete(t *testing.T) {
	st := testStore(t)
	svc := auth.NewSecretStore(st, secretTestSealer{})
	ctx := context.Background()
	scope := auth.GlobalSecretScope
	for _, n := range []string{"b-name", "a-name", "c-name"} {
		if _, err := svc.Put(ctx, adminActor(), scope, n, "v-"+n, ""); err != nil {
			t.Fatal(err)
		}
	}
	list, err := svc.List(ctx, scope)
	if err != nil || len(list) != 3 {
		t.Fatalf("List = (%d,%v)", len(list), err)
	}
	if list[0].Name != "a-name" || list[2].Name != "c-name" {
		t.Errorf("list not sorted: %v", list)
	}
	if err := svc.Delete(ctx, adminActor(), scope, "a-name"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Resolve(ctx, scope, "a-name"); !errors.Is(err, auth.ErrSecretNotFound) {
		t.Errorf("deleted secret should resolve to not-found, got %v", err)
	}
	if err := svc.Delete(ctx, adminActor(), scope, "a-name"); !errors.Is(err, auth.ErrSecretNotFound) {
		t.Errorf("double delete = %v, want ErrSecretNotFound", err)
	}
}

func TestSecretStoreFailClosed(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	scope := auth.GlobalSecretScope

	// No sealer: writes/resolves fail closed, never cleartext.
	noSealer := auth.NewSecretStore(st, nil)
	if _, err := noSealer.Put(ctx, adminActor(), scope, "x", "v", ""); !errors.Is(err, auth.ErrNoSecretSealer) {
		t.Errorf("Put with no sealer = %v, want ErrNoSecretSealer", err)
	}

	svc := auth.NewSecretStore(st, secretTestSealer{})
	// New secret with empty value is refused.
	if _, err := svc.Put(ctx, adminActor(), scope, "y", "", ""); !errors.Is(err, auth.ErrEmptySecretValue) {
		t.Errorf("new empty secret = %v, want ErrEmptySecretValue", err)
	}
	// Bad name refused.
	if _, err := svc.Put(ctx, adminActor(), scope, "bad name!", "v", ""); !errors.Is(err, auth.ErrBadSecretName) {
		t.Errorf("bad name = %v, want ErrBadSecretName", err)
	}
	// Missing secret resolves not-found.
	if _, err := svc.Resolve(ctx, scope, "absent"); !errors.Is(err, auth.ErrSecretNotFound) {
		t.Errorf("absent resolve = %v", err)
	}
}
