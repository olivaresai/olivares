// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// finopscustodymigration.go is the callable constructor for the prospective core v12
// migration `finops_custody_control_v1`, per
// `an internal design note (not shipped)` §1 and §4.3.1
// and the source split of `ROOT-CORRECTION-3.md`.
//
// WHAT THIS SLICE IS, stated before the code so nothing here reads as more than it is.
// It is the RELATION AND GUARD FOUNDATION: the concrete statements, the topology refusal
// that must precede them, and the split-topology establishment of a SELECT-only application
// role. The constructor is callable and it is exercised against both real engines.
//
// WHAT THIS SLICE IS NOT, and each omission is deliberate rather than pending:
//
//   - It is NOT appended to buildCoreMigrationPlan, and coreSupportedMigrationVersion is
//     NOT re-aliased. W5 owns the v11 plan change, and SCHEMA-V12 §1 requires one plan and
//     one ceiling owner; a second writer moving the ceiling would be exactly the collision
//     that rule exists to prevent. Nothing in a production boot reaches this constructor
//     yet, which is why no existing database is affected by this commit.
//   - There is NO After hook. SCHEMA-V12 §6.1 specifies After as exact-shape verification
//     plus the owner ACL attestation (A1–A5), and neither is in this slice. A partial After
//     that verified the PUBLIC leg and returned nil for the rest would be a verifier that
//     reports success for checks it never ran, which is worse than an absent one: the
//     absent one cannot be cited as evidence. The constructor therefore exposes the
//     statements and the establishment honestly, and the verifier arrives with its own
//     tests.
//   - There is no witness capture, no ceremony, no keyring, no maintenance path and no
//     broad new authority API. The guards and the establishment are the whole surface.
//
// The ONE precondition that is here is the topology refusal, and it is here because it
// cannot be anywhere else: the statements this constructor emits DEPEND on the resolved
// topology, so a constructor that could not classify it would have to guess which ACL to
// establish. Guessing is what §4.3.2 calls unverifiable, and unverifiable is a refusal.

// coreFinOpsCustodyControlMigrationVersion is the version SCHEMA-V12 §1 reserves.
//
// It is declared here and consumed by nothing else in this slice. That is not a dangling
// constant: it is the single place the plan owner will read when v12 is appended, so the
// constructor and the plan cannot disagree about which version this is.
const coreFinOpsCustodyControlMigrationVersion = 12

// coreFinOpsCustodyControlMigrationName is the tracked name. preflightCoreMigrationVersion
// will require the v12 row to be the exact active `expand` record under this name, so the
// literal lives once.
const coreFinOpsCustodyControlMigrationName = "finops_custody_control_v1"

// errFinOpsCustodyTopology is the refusal when the deployment configured a separate owner
// and this boot could not resolve both roles.
//
// It wraps store.ErrAppendOnlyACLUnverifiable rather than introducing a new sentinel,
// because the condition is the one that sentinel already names: a boundary this engine was
// asked to establish and could not establish. Moving a refusal earlier must not rename it.
var errFinOpsCustodyTopology = fmt.Errorf("sqlstore: finops custody control cannot establish its ACL without both resolved roles: %w", store.ErrAppendOnlyACLUnverifiable)

// coreFinOpsCustodyControlMigration builds the v12 migration for one dialect and the roles
// boot resolved.
//
// IT IS VARIADIC FOR ONE REASON, and the reason is the failure mode it makes unreachable:
// the plan's call site forwards whatever guardRolesForBoot produced, and a non-variadic
// signature taking a zero guardRoles would make "boot supplied no roles" indistinguishable
// from "boot supplied an empty pair". SCHEMA-V12 §1 requires exactly one value, and Before
// refuses anything else — including two, which would mean two call sites disagreeing about
// whose roles these are.
//
// THE STATEMENTS ARE ASSEMBLED EAGERLY, here, not inside the transaction. A migration whose
// statement list depends on what a callback observes is a migration whose applied shape
// cannot be predicted from its inputs, and §6 exists to compare the applied shape against a
// predicted one.
func coreFinOpsCustodyControlMigration(dia dialect.Dialect, roles ...guardRoles) migrate.Migration {
	m := migrate.Migration{
		Version: coreFinOpsCustodyControlMigrationVersion,
		Name:    coreFinOpsCustodyControlMigrationName,
		Phase:   migrate.Expand,
		Stmts:   dia.FinOpsCustodyControlStmts(),
	}
	// SQLite has no role model, so there is nothing to classify and nothing to
	// establish. Refusing a SQLite boot for an unresolvable PostgreSQL topology would
	// be a refusal about a question the engine cannot be asked.
	if dia.Name() == store.EngineSQLite {
		return m
	}
	m.Before = func(ctx context.Context, tx *sql.Tx) error {
		return finOpsCustodyRequireTopology(roles)
	}
	// The split-topology pair, appended only when the topology IS split. REVOKE before
	// GRANT, in that order, because the provisioner's ALTER DEFAULT PRIVILEGES may already
	// have granted the application role INSERT/UPDATE/DELETE on every future table the
	// owner creates: granting SELECT without first revoking would leave those in place and
	// the relations would be born writable by runtime traffic.
	//
	// Appended eagerly from the roles the constructor was given, and Before refuses any
	// roles value this append could have misread — so the emitted statements and the
	// verified topology cannot diverge.
	if len(roles) == 1 && guardMetadataTopologyOf(roles[0]) == guardTopologySplit {
		m.Stmts = append(m.Stmts, finOpsCustodySplitACLStmts(roles[0].App.bindable())...)
	}
	return m
}

// finOpsCustodyRequireTopology is the §1 Before precondition: exactly one roles value, and
// a topology this engine can name.
//
// IT REFUSES AN UNKNOWN TOPOLOGY RATHER THAN FALLING BACK TO SINGLE-ROLE, which is the
// distinction the three-valued topology type exists to preserve. "The operator configured
// one role" and "the operator configured two and this boot could not read one of them" are
// different answers, and treating the second as the first would create the relations with
// no application-role revoke at all while logging that none was needed.
func finOpsCustodyRequireTopology(roles []guardRoles) error {
	if len(roles) != 1 {
		return fmt.Errorf("sqlstore: finops custody control requires exactly one guard roles value, got %d: %w",
			len(roles), store.ErrAppendOnlyACLUnverifiable)
	}
	switch guardMetadataTopologyOf(roles[0]) {
	case guardTopologySingleRole, guardTopologySplit:
		return nil
	default:
		return fmt.Errorf("%w: could not resolve %s", errFinOpsCustodyTopology, describeUnresolvedGuardRoles(roles[0]))
	}
}

// finOpsCustodySplitACLStmts exposes, for the tests and for a later verifier, exactly the
// establishment statements the split topology adds.
//
// It exists so an assertion about the established posture is made against the SAME text the
// migration runs, rather than against a copy of it in a test. A copy is how a test ends up
// proving that its own literal is well-formed.
func finOpsCustodySplitACLStmts(app string) []string {
	quoted := quoteIdent(app)
	targets := strings.Join(dialect.FinOpsCustodyControlTables(), ", ")
	return []string{
		"REVOKE ALL ON TABLE " + targets + " FROM " + quoted,
		"GRANT SELECT ON TABLE " + targets + " TO " + quoted,
	}
}
