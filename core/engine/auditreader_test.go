// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package engine_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAuditReaderExistingLedgerIsReadOnlyAndTenantPinned(t *testing.T) {
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "ledger.db")}
	if _, err := engine.OpenAuditReader(t.Context(), cfg); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing store: %v", err)
	}
	if _, err := os.Stat(cfg.DSN); !os.IsNotExist(err) {
		t.Fatalf("reader created a missing store: %v", err)
	}
	st, err := engine.Open(t.Context(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tenants []model.TenantID
	if err := st.System(t.Context(), func(sc store.SystemScope) error {
		for _, slug := range []string{"reader-one", "reader-two"} {
			o, err := sc.CreateOrg(t.Context(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			tenants = append(tenants, o.TenantID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range tenants {
		if err := st.Custody(t.Context(), tenant, func(sc store.CustodyScope) error {
			_, err := sc.Audit().Append(t.Context(), model.AuditDraft{Actor: "reader-test", ActorKind: model.ActorSystem, Action: "reader.test"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := engine.OpenAuditReader(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, tenant := range tenants {
		if err := reader.ViewAudit(t.Context(), tenant, func(log store.AuditLog) error {
			if _, err := log.Append(t.Context(), model.AuditDraft{}); !errors.Is(err, store.ErrReadOnly) {
				t.Fatalf("reader accepted append: %v", err)
			}
			rep, err := log.Verify(t.Context(), 0)
			if err != nil || !rep.OK || rep.Checked != 2 {
				t.Fatalf("tenant chain: %+v %v", rep, err)
			}
			return log.Walk(t.Context(), 0, func(ev model.AuditEvent) error {
				if ev.TenantID != tenant {
					t.Fatal("reader crossed a tenant boundary")
				}
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDRReaderInventoryIncludesSuspendedTenantsWithoutWrites(t *testing.T) {
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "ledger.db")}
	st, err := engine.Open(t.Context(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var tenants []model.TenantID
	if err := st.System(t.Context(), func(sc store.SystemScope) error {
		for _, slug := range []string{"dr-active", "dr-suspended"} {
			org, err := sc.CreateOrg(t.Context(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			tenants = append(tenants, org.TenantID)
		}
		_, err := sc.SetOrgStatus(t.Context(), tenants[1], model.StatusSuspended)
		return err
	}); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := engine.OpenDRReader(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	orgs, err := reader.ListOrgs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(orgs) != len(tenants) {
		t.Fatalf("inventory: %d orgs, want %d", len(orgs), len(tenants))
	}
	for _, tenant := range tenants {
		found := false
		for _, org := range orgs {
			found = found || org.TenantID == tenant
		}
		if !found {
			t.Fatalf("inventory omitted %s", tenant)
		}
		if err := reader.ViewAudit(t.Context(), tenant, func(log store.AuditLog) error {
			_, err := log.Append(t.Context(), model.AuditDraft{})
			if !errors.Is(err, store.ErrReadOnly) {
				t.Fatalf("reader accepted append: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("offline inventory changed the snapshot bytes")
	}
	if _, err := engine.OpenDRReader(t.Context(), store.Config{Engine: store.EnginePostgres}); err == nil {
		t.Fatal("SQLite reader accepted PostgreSQL without its DR preflight")
	}
}
