// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
)

// Core v18: TOTP second factor for local accounts . Expand, forward only.
//
// Like v15, this version executes no statement: the three relations it covers —
// core.totp_credential, core.totp_recovery_code and the core.auth_policy
// singleton — are additive descriptors that v2 does not render and the
// reconcile creates whole on every database, fresh or upgraded. What the
// record adds is the refusal boundary: a binary that predates it refuses to
// start on a store that holds any of them (preflightCoreMigrationVersion),
// because it would mint sessions without enforcing the factor those rows
// declare.
const (
	coreTOTPMigrationVersion = 18
	coreTOTPMigrationName    = "totp_second_factor_v1"
)

// totpRelation reports whether kind is a relation v18 adds. v2 keeps rendering
// the auth relations as they were before it (the historical DDL never changes),
// and the reconcile creates these tables from their descriptors on every
// database, fresh or upgraded — exactly the v15 credential-binding pattern.
func totpRelation(kind model.Kind) bool {
	return kind == totpCredentialDescriptor.Kind || kind == totpRecoveryCodeDescriptor.Kind ||
		kind == authPolicyDescriptor.Kind
}

// coreTOTPMigration builds the v18 record. It executes no statement.
func coreTOTPMigration() migrate.Migration {
	return migrate.Migration{
		Version: coreTOTPMigrationVersion,
		Name:    coreTOTPMigrationName,
		Phase:   migrate.Expand,
	}
}
