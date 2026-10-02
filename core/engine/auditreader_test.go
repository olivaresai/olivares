// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package engine_test

import (
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
