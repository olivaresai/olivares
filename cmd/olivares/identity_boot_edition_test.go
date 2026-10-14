//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestCommunityStoredCAEPStartsAndPreservesExport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	prepareCompositionTestBoot(t)
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: false})
	if err != nil {
		t.Fatal(err)
	}
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Retained identity", Slug: "retained-identity", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	original := map[string]any{"enabled": true, "publisher_id": "retained-publisher"}
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.SetOrgSettings(ctx, map[string]any{"caep_set": original})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	prepareCompositionTestBoot(t)
	eng, err = boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
	if err != nil {
		t.Fatalf("Community upgrade must start with retained CAEP: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	prepareCompositionTestBoot(t)
	offline, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: false})
	if err != nil {
		t.Fatalf("offline backup store unavailable: %v", err)
	}
	defer offline.Close()
	if err := offline.store.Export(ctx, tenant, func(sc store.ExportScope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		got, ok := org.Settings["caep_set"].(map[string]any)
		if !ok || got["enabled"] != true || got["publisher_id"] != "retained-publisher" {
			t.Fatalf("stored CAEP changed: %#v", org.Settings)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCommunityWIFOptInCannotFallBackToStaticCredentials(t *testing.T) {
	broker := newWIFCredentialBroker(func(string) string { return "" }, nil, nil, nil)
	if broker == nil {
		t.Fatal("nil broker permits caller static-credential fallback")
	}
	path := filepath.Join(t.TempDir(), "static-token")
	const fixtureToken = "LABELLED-LOCAL-STATIC-TOKEN"
	if err := os.WriteFile(path, []byte(fixtureToken), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{envSessionTokenFile: path, envSessionWIFRule: "anthropic"}
	getenv := func(name string) string { return settings[name] }
	compat, _ := sessionCredentialSource(getenv, broker)
	credential, err := compat.Mint(context.Background(), sessions.CredentialRequest{})
	if err != nil || credential.Token != fixtureToken {
		t.Fatal("labelled static-token compatibility path did not mint")
	}
	settings[envSessionRuntimeWIF] = "true"
	source, kind := sessionCredentialSource(getenv, broker)
	if source == nil || !strings.HasPrefix(kind, "wif ") {
		t.Fatalf("WIF selector chose %q", kind)
	}
	credential, err = source.Mint(context.Background(), sessions.CredentialRequest{})
	if !errors.Is(err, errIdentityRequiresBusiness) || credential.Token != "" {
		t.Fatalf("Community WIF selector did not refuse: %v", err)
	}
}

type unavailableCAEPInventoryStore struct{ store.Store }

func (s unavailableCAEPInventoryStore) System(ctx context.Context, fn func(store.SystemScope) error) error {
	return s.Store.System(ctx, func(sys store.SystemScope) error { return fn(unavailableCAEPInventoryScope{sys}) })
}

type unavailableCAEPInventoryScope struct{ store.SystemScope }

func (unavailableCAEPInventoryScope) ListOrgs(context.Context) ([]model.Org, error) {
	return nil, store.ErrEnumerationNotAuthoritative
}
func (unavailableCAEPInventoryScope) ListOrgsVisible(context.Context) ([]model.Org, bool, error) {
	return nil, false, nil
}
func TestCommunityCAEPStartupRefusesIncompleteTenantInventory(t *testing.T) {
	prepareCompositionTestBoot(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := editionIdentityStartup(context.Background(), unavailableCAEPInventoryStore{eng.store}); !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
		t.Fatalf("partial tenant inventory cause did not propagate: %v", err)
	}
}

func TestCommunityStoredNHIClockPolicyStartsAndPreservesExport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   model.Record
		inactive bool
	}{
		{"inherited rotation window", model.Record{"rotated_at": model.NewTimestamp(time.Now()).String()}, true},
		{"explicit rotation window", model.Record{"max_age_seconds": int64(60)}, true},
		{"pending escalation", model.Record{"block_after": model.NewTimestamp(time.Now().Add(time.Hour)).String()}, true},
		{"ordinary retained row", model.Record{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			prepareCompositionTestBoot(t)
			eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test"})
			if err != nil {
				t.Fatal(err)
			}
			var tenant model.TenantID
			if err := eng.store.System(ctx, func(sys store.SystemScope) error {
				org, err := sys.CreateOrg(ctx, model.Org{Name: "Retained NHI", Slug: "retained-nhi", Status: model.StatusActive})
				tenant = org.TenantID
				return err
			}); err != nil {
				t.Fatal(err)
			}
			rec := model.Record{"identity_ref": "retained-agent", "source": "test", "criticality": "critical", "max_age_seconds": int64(0), "staleness_status": "unknown", "enforcement": "monitor", "orphaned": false, "offboard_state": "none"}
			for key, value := range tc.policy {
				rec[key] = value
			}
			var stored model.Record
			if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("governance.nhi_lifecycle")
				if err != nil {
					return err
				}
				stored, err = repo.Create(ctx, rec)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := eng.Close(); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			previousLog := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLog) })
			prepareCompositionTestBoot(t)
			serving, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
			if err != nil {
				t.Fatalf("Community upgrade must start with retained NHI: %v", err)
			}
			wantLogs := 0
			if tc.inactive {
				wantLogs = 1
			}
			if got := strings.Count(logs.String(), "inactive"); got != wantLogs {
				t.Fatalf("startup inactive logs = %d, want %d: %s", got, wantLogs, logs.String())
			}
			if err := serving.Close(); err != nil {
				t.Fatal(err)
			}
			prepareCompositionTestBoot(t)
			offline, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test"})
			if err != nil {
				t.Fatalf("offline backup store unavailable: %v", err)
			}
			defer offline.Close()
			if err := offline.store.Export(ctx, tenant, func(sc store.ExportScope) error {
				repo, err := sc.Ext("governance.nhi_lifecycle")
				if err != nil {
					return err
				}
				rows, _, err := repo.List(ctx, model.Query{Limit: 10})
				if err != nil {
					return err
				}
				if len(rows) != 1 || !reflect.DeepEqual(rows[0], stored) {
					t.Fatalf("retained policy changed: %#v", rows)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Seed the published 0.1 settings/row shape, reopen through production serve boot,
// and read portability data before and after the unavailable routes are called.
func TestCommunityStoredIdentityUpgradeStartsRoutesRefuseAndExportsUnchanged(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	prepareCompositionTestBoot(t)
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if eng != nil {
			_ = eng.Close()
		}
	})
	if _, err := eng.authr.BootstrapSuperadmin(ctx, "upgrade@example.invalid", "labelled-upgrade-password"); err != nil {
		t.Fatal(err)
	}
	tenants := []model.TenantID{
		createOrganizationFixture(t, eng.store, "Upgrade one", "upgrade-one").TenantID,
		createOrganizationFixture(t, eng.store, "Upgrade two", "upgrade-two").TenantID,
	}
	for _, tenant := range tenants {
		if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
			if _, err := sc.SetOrgSettings(ctx, map[string]any{"caep_set": map[string]any{"enabled": true, "publisher_id": "retained-publisher"}}); err != nil {
				return err
			}
			repo, err := sc.Ext("governance.nhi_lifecycle")
			if err != nil {
				return err
			}
			_, err = repo.Create(ctx, model.Record{"identity_ref": "retained-agent", "source": "test", "criticality": "critical", "max_age_seconds": int64(60), "staleness_status": "unknown", "enforcement": "monitor", "orphaned": false, "offboard_state": "none", "block_after": "2020-01-01T00:00:00.000000000Z"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func(st store.Store, tenant model.TenantID) []byte {
		t.Helper()
		var raw []byte
		if err := st.Export(ctx, tenant, func(sc store.ExportScope) error {
			org, err := sc.Org(ctx)
			if err != nil {
				return err
			}
			repo, err := sc.Ext("governance.nhi_lifecycle")
			if err != nil {
				return err
			}
			rows, _, err := repo.List(ctx, model.Query{Limit: 10})
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				t.Fatalf("export lifecycle rows = %d", len(rows))
			}
			raw, err = json.Marshal(struct {
				Settings map[string]any
				Rows     []model.Record
			}{org.Settings, rows})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := [][]byte{snapshot(eng.store, tenants[0]), snapshot(eng.store, tenants[1])}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previousLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLog) })
	prepareCompositionTestBoot(t)
	eng, err = boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", ServeMode: true})
	if err != nil {
		t.Fatalf("Community 0.1-shaped upgrade refused to start: %v", err)
	}
	if strings.Count(logs.String(), "inactive") != 1 || !strings.Contains(logs.String(), "CAEP receiver") || !strings.Contains(logs.String(), "NHI rotation and escalation protection") || !strings.Contains(logs.String(), "Business Identity & Scale") {
		t.Fatalf("want one startup line naming both inactive features and edition: %s", logs.String())
	}
	admin, _, err := eng.authr.Login(ctx, "upgrade@example.invalid", "labelled-upgrade-password", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := eng.authr.Authenticate(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for i, tenant := range tenants {
		bound, _, err := eng.authr.IssueToken(ctx, principal, auth.TokenSpec{Name: "upgrade", BoundTenant: tenant, Role: auth.RoleAdmin})
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range []struct{ method, path string }{
			{"POST", "/v1/ssf/events"}, {"GET", "/v1/m/governance/nhi"}, {"POST", "/v1/m/governance/nhi/sweep"},
		} {
			for _, token := range []string{bound, ""} {
				want := http.StatusNotImplemented
				if token == "" {
					want = http.StatusUnauthorized
				}
				code, _, raw := doDemoViewJSON(t, eng.api.Handler(), route.method, route.path, token, tenant.String(), nil)
				if code != want {
					t.Fatalf("%s %s = %d, want %d: %s", route.method, route.path, code, want, raw)
				}
			}
		}
		if after := snapshot(eng.store, tenant); !bytes.Equal(before[i], after) {
			t.Fatalf("stored/exported protection changed: before=%s after=%s", before[i], after)
		}
	}
}
