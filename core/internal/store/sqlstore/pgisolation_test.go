// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
)

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
