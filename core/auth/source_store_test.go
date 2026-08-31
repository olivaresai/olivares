// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestSourceStoreCRUDRoundTrip(t *testing.T) {
	st := testStore(t)
	svc := auth.NewSourceStore(st)
	ctx := context.Background()
	scope := auth.GlobalSourceScope

	// Create an in-process source whose config carries a secret REFERENCE.
	in := model.SourceDef{
		Name: "vault-prod", Kind: "vault", Tenant: "acme", PollSeconds: 300, Enabled: true,
		Config: map[string]string{"addr": "https://vault.acme", "token": "store:vault/token"},
	}
	saved, err := svc.Put(ctx, adminActor(), in)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if saved.ID.IsZero() || saved.Name != "vault-prod" {
		t.Fatalf("saved = %+v", saved)
	}

	// Get round-trips the config map (references intact) and metadata.
	got, ok, err := svc.Get(ctx, scope, "vault-prod")
	if err != nil || !ok {
		t.Fatalf("Get = (%+v,%v,%v)", got, ok, err)
	}
	if got.Kind != "vault" || got.Tenant != "acme" || got.PollSeconds != 300 || !got.Enabled {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Config["token"] != "store:vault/token" || got.Config["addr"] != "https://vault.acme" {
		t.Errorf("config not preserved: %+v", got.Config)
	}

	// Update (rotate config + disable); identity preserved.
	in2 := got
	in2.Enabled = false
	in2.Config["token"] = "store:vault/token-v2"
	upd, err := svc.Put(ctx, adminActor(), in2)
	if err != nil {
		t.Fatalf("Put update: %v", err)
	}
	if upd.ID != saved.ID {
		t.Errorf("update changed the row identity: %v != %v", upd.ID, saved.ID)
	}
	re, _, _ := svc.Get(ctx, scope, "vault-prod")
	if re.Enabled || re.Config["token"] != "store:vault/token-v2" {
		t.Errorf("update not persisted: %+v", re)
	}

	// An external-plugin source round-trips its Plugin spec (JSON column).
	plug := model.SourceDef{
		Name: "acme-edr", Tenant: "acme",
		Plugin: &model.SourcePluginRef{Path: "/opt/conn/edr", SHA256: "abc123", Bundle: "/opt/conn/edr.bundle"},
		Config: map[string]string{"endpoint": "https://edr"},
	}
	if _, err := svc.Put(ctx, adminActor(), plug); err != nil {
		t.Fatalf("Put plugin: %v", err)
	}
	gp, ok, _ := svc.Get(ctx, scope, "acme-edr")
	if !ok || gp.Plugin == nil || gp.Plugin.Path != "/opt/conn/edr" || gp.Plugin.SHA256 != "abc123" {
		t.Fatalf("plugin spec not round-tripped: %+v", gp.Plugin)
	}

	// List is sorted by name and holds both.
	all, err := svc.List(ctx, scope)
	if err != nil || len(all) != 2 || all[0].Name != "acme-edr" || all[1].Name != "vault-prod" {
		t.Fatalf("List = (%d rows,%v): %+v", len(all), err, all)
	}

	// Delete removes one; the other survives.
	if err := svc.Delete(ctx, adminActor(), scope, "acme-edr"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := svc.Get(ctx, scope, "acme-edr"); ok {
		t.Error("deleted source still present")
	}
	if err := svc.Delete(ctx, adminActor(), scope, "acme-edr"); !errors.Is(err, auth.ErrSourceDefNotFound) {
		t.Errorf("re-delete = %v, want ErrSourceDefNotFound", err)
	}
}

func TestSourceStoreSeedAllAtomic(t *testing.T) {
	st := testStore(t)
	svc := auth.NewSourceStore(st)
	ctx := context.Background()

	// One invalid entry (no tenant) → the WHOLE seed rolls back, nothing persists.
	bad := []model.SourceDef{
		{Name: "a", Kind: "vault", Tenant: "acme", Enabled: true},
		{Name: "b", Kind: "vault"}, // no tenant → invalid
		{Name: "c", Kind: "vault", Tenant: "acme", Enabled: true},
	}
	if n, err := svc.SeedAll(ctx, adminActor(), bad); err == nil || n != 0 {
		t.Fatalf("SeedAll with an invalid entry = (%d,%v), want (0, error)", n, err)
	}
	rows, _ := svc.List(ctx, auth.GlobalSourceScope)
	if len(rows) != 0 {
		t.Fatalf("a failed atomic seed must persist NOTHING, found %d rows", len(rows))
	}

	// All valid → all created in one shot.
	good := []model.SourceDef{
		{Name: "a", Kind: "vault", Tenant: "acme", Enabled: true},
		{Name: "c", Kind: "claudeapi", Tenant: "acme", Enabled: true},
	}
	if n, err := svc.SeedAll(ctx, adminActor(), good); err != nil || n != 2 {
		t.Fatalf("SeedAll all-valid = (%d,%v), want (2,nil)", n, err)
	}
	rows, _ = svc.List(ctx, auth.GlobalSourceScope)
	if len(rows) != 2 {
		t.Fatalf("after a clean seed: %d rows, want 2", len(rows))
	}
}

// TestSourceStoreRejectsPlaintextConnectorCredentialsAtRest attacks the durable
// roster at its lowest public write seam. Descriptor-aware API/CLI validation is
// intentionally bypassed here: neither Put nor the atomic bootstrap SeedAll may
// serialize a literal credential into source_defs.config.
func TestSourceStoreRejectsPlaintextConnectorCredentialsAtRest(t *testing.T) {
	st := testStore(t)
	svc := auth.NewSourceStore(st)
	ctx := context.Background()
	const plaintext = "AUDIT-PLAINTEXT-SASL-PASSWORD"

	t.Run("put", func(t *testing.T) {
		_, err := svc.Put(ctx, adminActor(), model.SourceDef{
			Name: "kafka-prod", Kind: "kafka", Tenant: "acme", Enabled: true,
			Config: map[string]string{"brokers": "kafka.internal:9092", "sasl_password": plaintext},
		})
		if !errors.Is(err, auth.ErrBadSourceDef) {
			t.Fatalf("Put plaintext connector credential = %v, want ErrBadSourceDef", err)
		}
		if strings.Contains(err.Error(), plaintext) {
			t.Fatalf("credential rejection echoed the plaintext value: %v", err)
		}
		if _, found, getErr := svc.Get(ctx, auth.GlobalSourceScope, "kafka-prod"); getErr != nil || found {
			t.Fatalf("plaintext connector credential reached storage: found=%v err=%v", found, getErr)
		}
	})

	t.Run("credential in endpoint", func(t *testing.T) {
		_, err := svc.Put(ctx, adminActor(), model.SourceDef{
			Name: "postgres-prod", Kind: "postgres", Tenant: "acme", Enabled: true,
			Config: map[string]string{"endpoint": "postgres://alice:" + plaintext + "@db.internal/app"},
		})
		if !errors.Is(err, auth.ErrBadSourceDef) {
			t.Fatalf("Put endpoint with inline credential = %v, want ErrBadSourceDef", err)
		}
		if strings.Contains(err.Error(), plaintext) {
			t.Fatalf("endpoint rejection echoed the plaintext value: %v", err)
		}
		if _, found, getErr := svc.Get(ctx, auth.GlobalSourceScope, "postgres-prod"); getErr != nil || found {
			t.Fatalf("endpoint credential reached storage: found=%v err=%v", found, getErr)
		}
	})

	t.Run("atomic seed", func(t *testing.T) {
		n, err := svc.SeedAll(ctx, adminActor(), []model.SourceDef{
			{Name: "safe", Kind: "vault", Tenant: "acme", Enabled: true},
			{
				Name: "amqp-prod", Kind: "amqp", Tenant: "acme", Enabled: true,
				Config: map[string]string{"endpoint": "amqps://mq.internal", "sasl_password": plaintext},
			},
		})
		if !errors.Is(err, auth.ErrBadSourceDef) || n != 0 {
			t.Fatalf("SeedAll plaintext connector credential = (%d,%v), want (0,ErrBadSourceDef)", n, err)
		}
		rows, listErr := svc.List(ctx, auth.GlobalSourceScope)
		if listErr != nil || len(rows) != 0 {
			t.Fatalf("failed secret-bearing seed must persist nothing: rows=%d err=%v", len(rows), listErr)
		}
	})
}

func TestSourceStoreValidation(t *testing.T) {
	st := testStore(t)
	svc := auth.NewSourceStore(st)
	ctx := context.Background()

	cases := []struct {
		name string
		def  model.SourceDef
		want error
	}{
		{"empty name", model.SourceDef{Kind: "vault", Tenant: "t"}, auth.ErrBadSourceName},
		{"no tenant", model.SourceDef{Name: "x", Kind: "vault"}, auth.ErrBadSourceDef},
		{"neither kind nor plugin", model.SourceDef{Name: "x", Tenant: "t"}, auth.ErrBadSourceDef},
		{"both kind and plugin", model.SourceDef{Name: "x", Tenant: "t", Kind: "vault", Plugin: &model.SourcePluginRef{Path: "/p"}}, auth.ErrBadSourceDef},
		{"plugin without path", model.SourceDef{Name: "x", Tenant: "t", Plugin: &model.SourcePluginRef{}}, auth.ErrBadSourceDef},
		{"negative poll", model.SourceDef{Name: "x", Tenant: "t", Kind: "vault", PollSeconds: -1}, auth.ErrBadSourceDef},
		{"bad name chars", model.SourceDef{Name: "a b:c", Tenant: "t", Kind: "vault"}, auth.ErrBadSourceName},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := svc.Put(ctx, adminActor(), c.def); !errors.Is(err, c.want) {
				t.Errorf("Put(%s) = %v, want %v", c.name, err, c.want)
			}
		})
	}
}
