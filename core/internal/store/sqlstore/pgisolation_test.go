// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
)

// postgresBootBudget bounds a PostgreSQL test context that covers a store boot
// (Open), ApplyMigrations or ActivateDirectoryWriter, together with the
// assertions that follow it. The scheduled runner is shared and can be several
// times slower than a quiet one; a boot phase that normally takes 10-28s once
// exceeded its old 15-30s test-wide deadline, and the store correctly reported
// the caller's deadline as the boot error. Three minutes keeps ordinary runner
// variance from failing a test while a hung boot still fails it.
//
// A context that covers more than one boot takes twice this budget
// (2*postgresBootBudget). The extra share is headroom for the additional
// phases, not a per-boot allowance.
const postgresBootBudget = 3 * time.Minute

// isolatedPG provisions a private Postgres database for t — dropped in
// t.Cleanup — whose APPLICATION role owns it, matching the posture CI used to
// provision for the single shared database. It skips t when no Postgres is
// configured.
//
// These tests are IN-PACKAGE (`package sqlstore`; they reach the unexported
// pool), so they cannot import core/engine or anything else that imports this
// package. pgtest takes the provisioner as a PARAMETER precisely so this call
// site works: pgtest is a leaf, and ProvisionPostgres is passed in from here.
func isolatedPG(t testing.TB) pgtest.DSNs {
	t.Helper()
	return pgtest.Isolate(t, ProvisionPostgres, pgtest.SingleRole)
}

// isolatedPGSplit is isolatedPG with a SEPARATE owner role that owns the
// database and runs DDL — the store.Config.OwnerDSN topology.
func isolatedPGSplit(t testing.TB) pgtest.DSNs {
	t.Helper()
	return pgtest.Isolate(t, ProvisionPostgres, pgtest.SplitOwner)
}
