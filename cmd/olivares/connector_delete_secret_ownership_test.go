// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

func TestDeleteConnectorPreservesNestedSourceCredentialAndName(t *testing.T) {
	sr, srcStore, secretStore := newOnboardHarness(t)
	ctx := context.Background()
	for _, in := range []api.ConnectorOnboardInput{
		{
			Name: "a/b", Kind: "vault", Tenant: "acme", Enabled: true,
			Config:  map[string]string{"base_url": "https://b.invalid:8200"},
			Secrets: map[string]string{"token": "synthetic-owned-b"},
		},
		{
			Name: "a", Kind: "vault", Tenant: "acme", Enabled: true,
			Config: map[string]string{
				"base_url": "https://a.invalid:8200", "extra_ref": "store:source/a/b/token",
				"b/token": "store:source/a/b/token",
			},
			Secrets: map[string]string{"token": "synthetic-owned-a"},
		},
	} {
		res, err := sr.PutConnector(ctx, recAdmin(), in)
		if err != nil || !res.Persisted || !res.Applied {
			t.Fatalf("onboard %q = %+v, %v", in.Name, res, err)
		}
	}
	// The existing db alias and reference whitespace must name the same sealed key.
	const nestedRef = " DB : source/a/b/token "
	if _, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{
		Name: "a/b", Kind: "vault", Tenant: "acme", Enabled: true,
		Config:  map[string]string{"base_url": "https://b.invalid:8200"},
		Secrets: map[string]string{"token": nestedRef},
	}); err != nil {
		t.Fatal(err)
	}
	if value, err := secretStore.Resolve(ctx, auth.GlobalSecretScope, "source/a/b/token"); err != nil || string(value) != "synthetic-owned-b" {
		t.Fatalf("a/b credential was not usable before deletion: value=%q, err=%v", value, err)
	}

	res, err := sr.DeleteConnector(ctx, recAdmin(), "a")
	if err != nil || !res.Persisted || !res.Applied || res.Name != "a" || res.Action != "removed" {
		t.Fatalf("delete a = %+v, %v", res, err)
	}
	if _, found, err := srcStore.Get(ctx, auth.GlobalSourceScope, "a"); err != nil || found {
		t.Fatalf("deleted source a remains: found=%v, err=%v", found, err)
	}
	if _, found, err := secretStore.Get(ctx, auth.GlobalSecretScope, "source/a/token"); err != nil || found {
		t.Errorf("owned a credential remains: found=%v, err=%v", found, err)
	}
	if value, err := secretStore.Resolve(ctx, auth.GlobalSecretScope, "source/a/b/token"); err != nil || string(value) != "synthetic-owned-b" {
		t.Errorf("a/b credential must survive deleting a: value=%q, err=%v", value, err)
	}
	def, found, err := srcStore.Get(ctx, auth.GlobalSourceScope, "a/b")
	if err != nil || !found {
		t.Fatalf("get a/b = found=%v, err=%v", found, err)
	}
	if def.Name != "a/b" || def.Config["token"] != nestedRef || def.Config["base_url"] != "https://b.invalid:8200" {
		t.Errorf("a/b stored name/config changed: %+v", def)
	}
	roster, err := sr.ListSources(ctx)
	if err != nil || len(roster) != 1 {
		t.Fatalf("remaining public roster = %+v, %v", roster, err)
	}
	if roster[0].Name != "a/b" || roster[0].Status != "running" || roster[0].Config["token"] != nestedRef {
		t.Errorf("a/b must retain its public name, credential reference and running state: %+v", roster[0])
	}

	// The nested name still owns its own credential when it is explicitly deleted.
	res, err = sr.DeleteConnector(ctx, recAdmin(), "a/b")
	if err != nil || !res.Persisted || !res.Applied {
		t.Fatalf("delete a/b = %+v, %v", res, err)
	}
	if _, found, err := secretStore.Get(ctx, auth.GlobalSecretScope, "source/a/b/token"); err != nil || found {
		t.Errorf("owned a/b credential remains after its own deletion: found=%v, err=%v", found, err)
	}
}

func TestDeleteConnectorCleansOwnedCredentialReferencedUnderAnotherField(t *testing.T) {
	sr, _, secretStore := newOnboardHarness(t)
	ctx := context.Background()
	if _, err := secretStore.Put(ctx, recAdmin(), auth.GlobalSecretScope, "shared-cred", "synthetic-shared", "operator-owned"); err != nil {
		t.Fatal(err)
	}
	if _, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{
		Name: "a", Kind: "vault", Tenant: "acme", Enabled: true,
		Config:  map[string]string{"base_url": "https://a.invalid:8200"},
		Secrets: map[string]string{"token": "synthetic-owned-a", "b/token": "synthetic-owned-slash-field"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sr.PutConnector(ctx, recAdmin(), api.ConnectorOnboardInput{
		Name: "a", Kind: "vault", Tenant: "acme", Enabled: true,
		Config: map[string]string{
			"base_url": "https://a.invalid:8200", "extra_ref": "store:source/a/token",
		},
		Secrets: map[string]string{"token": "store:shared-cred", "b/token": ""},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := sr.DeleteConnector(ctx, recAdmin(), "a")
	if err != nil || !res.Persisted || !res.Applied {
		t.Fatalf("delete a = %+v, %v", res, err)
	}
	if _, found, err := secretStore.Get(ctx, auth.GlobalSecretScope, "source/a/token"); err != nil || found {
		t.Errorf("owned credential under another field remains: found=%v, err=%v", found, err)
	}
	if _, found, err := secretStore.Get(ctx, auth.GlobalSecretScope, "source/a/b/token"); err != nil || found {
		t.Errorf("owned slash-field credential remains: found=%v, err=%v", found, err)
	}
	if value, err := secretStore.Resolve(ctx, auth.GlobalSecretScope, "shared-cred"); err != nil || string(value) != "synthetic-shared" {
		t.Errorf("operator shared credential changed: value=%q, err=%v", value, err)
	}
}

func TestDeleteConnectorPreservesNestedSourceCredentialAfterReferenceChanges(t *testing.T) {
	for _, tc := range []struct {
		name, owner, field, deleted string
	}{
		{"nested", "a/b", "token", "a"},
		{"parent", "a", "b/token", "a/b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr, srcStore, secretStore := newOnboardHarness(t)
			ctx := context.Background()
			if _, err := secretStore.Put(ctx, recAdmin(), auth.GlobalSecretScope, "shared-cred", "synthetic-shared", "operator-owned"); err != nil {
				t.Fatal(err)
			}
			for _, in := range []api.ConnectorOnboardInput{
				{
					Name: tc.owner, Kind: "vault", Tenant: "acme", Enabled: true,
					Config:  map[string]string{"base_url": "https://owner.invalid:8200"},
					Secrets: map[string]string{tc.field: "synthetic-original-owner"},
				},
				{
					Name: tc.owner, Kind: "vault", Tenant: "acme", Enabled: true,
					Config:  map[string]string{"base_url": "https://owner.invalid:8200"},
					Secrets: map[string]string{tc.field: "store:shared-cred"},
				},
				{
					Name: tc.deleted, Kind: "vault", Tenant: "acme", Enabled: true,
					Config: map[string]string{
						"base_url": "https://deleted.invalid:8200", "token": "store:shared-cred",
						"extra_ref": "store:source/a/b/token",
					},
					Secrets: map[string]string{"delete_token": "synthetic-owned-deleted"},
				},
			} {
				if res, err := sr.PutConnector(ctx, recAdmin(), in); err != nil || !res.Persisted || !res.Applied {
					t.Fatalf("onboard %q = %+v, %v", in.Name, res, err)
				}
			}
			if value, err := secretStore.Resolve(ctx, auth.GlobalSecretScope, "source/a/b/token"); err != nil || string(value) != "synthetic-original-owner" {
				t.Fatalf("original credential is unusable before deletion: value=%q, err=%v", value, err)
			}
			if res, err := sr.DeleteConnector(ctx, recAdmin(), tc.deleted); err != nil || !res.Persisted || !res.Applied {
				t.Fatalf("delete %s = %+v, %v", tc.deleted, res, err)
			}
			if value, err := secretStore.Resolve(ctx, auth.GlobalSecretScope, "source/a/b/token"); err != nil || string(value) != "synthetic-original-owner" {
				t.Errorf("%s's original credential must survive its reference change: value=%q, err=%v", tc.owner, value, err)
			}
			def, found, err := srcStore.Get(ctx, auth.GlobalSourceScope, tc.owner)
			if err != nil || !found || def.Name != tc.owner || def.Config[tc.field] != "store:shared-cred" {
				t.Errorf("%s's current stored name/reference changed: %+v, found=%v, err=%v", tc.owner, def, found, err)
			}
		})
	}
}
