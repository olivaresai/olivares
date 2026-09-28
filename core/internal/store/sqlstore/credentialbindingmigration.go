// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "github.com/olivaresai/olivares/core/migrate"

// Core v15: durable credential bindings. Expand, forward only.
//
// The relation it covers, core_credential_bindings, is additive: v2 does not
// render it (credentialBindingRelation) and the reconcile creates it from its
// descriptor on every database, fresh or upgraded, like the v14 relations. What
// this version adds is the record: a binary that predates it refuses to start
// on a store that holds it (preflightCoreMigrationVersion), because it would
// run workflow effects without resolving their runs' bindings. v15 is reserved
// for this relation by the co-signed swap reservation; E12 T2 takes v16.
const (
	coreCredentialBindingMigrationVersion = 15
	coreCredentialBindingMigrationName    = "credential_binding_v1"
)

// coreCredentialBindingMigration builds the v15 record. It executes no statement.
func coreCredentialBindingMigration() migrate.Migration {
	return migrate.Migration{
		Version: coreCredentialBindingMigrationVersion,
		Name:    coreCredentialBindingMigrationName,
		Phase:   migrate.Expand,
	}
}
