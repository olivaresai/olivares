// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// System-scope appends (provisioning, cross-tenant events) go through their own
// auditLog construction (systemScope.auditLogFor). They must carry the SAME
// spool configuration as tenant scopes: otherwise the system chain is a budget
// bypass and the incremental counter drifts from the boot recompute.

func TestAuditSpoolSystemScopeAccounted(t *testing.T) {
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: filepath.Join(t.TempDir(), "system-accounted.db"), AuditSpoolMaxBytes: largeAuditSpoolBudget,
	})
	provisionTenant(t, st, "spool-system") // org.create rides the System path
	usage := readAuditSpoolUsage(t, st)
	recomputed := readSQLiteAuditSpoolSum(t, st)
	if usage == 0 || usage != recomputed {
		t.Fatalf("system-path accounting: usage=%d recompute=%d (drift means the system chain bypasses accounting)", usage, recomputed)
	}
}

func TestAuditSpoolSystemScopeGuarded(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "system-guarded.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	provisionTenant(t, initial, "spool-system-full")
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	// Over budget in block mode, provisioning a NEW tenant must fail deny-closed:
	// its org.create evidence cannot be persisted, so the org must not exist.
	st := openSQLiteSpoolTest(t, store.Config{DSN: dsn, AuditSpoolMaxBytes: 1})
	err := st.System(ctx, func(sys store.SystemScope) error {
		_, err := sys.CreateOrg(ctx, model.Org{Name: "Blocked", Slug: "blocked", Status: model.StatusActive})
		return err
	})
	if !errors.Is(err, store.ErrAuditSpoolFull) {
		t.Fatalf("system-path CreateOrg over budget = %v, want ErrAuditSpoolFull", err)
	}
}
