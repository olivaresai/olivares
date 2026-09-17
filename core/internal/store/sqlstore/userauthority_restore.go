// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// postgresRestoreAuthorityCommitTestHook runs after the ACL writes and full
// attestation. Returning an error injects a precommit failure; nil proceeds to
// Commit so tests can also exercise an ambiguous commit error.
var postgresRestoreAuthorityCommitTestHook func(*sql.Tx) error

// postgresRestoreAuthorityLegacyRollbackTestHook runs immediately before the
// explicit close of a recognized legacy transaction. It lets PostgreSQL tests
// make that transaction's own backend unavailable without exposing a production
// SQL or callback capability.
var postgresRestoreAuthorityLegacyRollbackTestHook func(*sql.Tx) error

// RestorePostgresUserAuthorityPrivileges closes the privileges a logical
// pg_restore --no-privileges cannot carry, in ONE transaction, before any Store can
// be published: the two compiled H functions, and — from core v13 — the login
// capability relation's ACL. It publishes no Store, runs no migrations and never
// creates or replaces a function. A recognized pre-H predecessor is a read-only
// no-op, preserving restore→upgrade. AdminDSN and AllowPrivilegedRole confer no
// authority on this operation.
//
// THE NAME IS NARROWER THAN THE JOB, kept because it is the exported symbol
// core/engine and cmd/olivares already call. What the operation IS is "re-establish
// the privileges the dump could not describe": pg_dump runs with --no-privileges, so
// every restored object arrives with the DESTINATION's default privileges and none of
// the source's grants. For the H functions that showed up as a stripped ACL; for core
// v13 it showed up as the opposite — the destination's ALTER DEFAULT PRIVILEGES handed
// the application role SELECT, INSERT, UPDATE and DELETE on a relation whose contract
// allows it SELECT, INSERT and three columns of UPDATE, and the boot that follows
// refuses the estate it just restored (measured 2026-09-15: `core v13 login capability
// relation drift: relation ACL grants DELETE to "app_dst_…"` in the owner/app split).
// A restore is the relation's BIRTH in a new database, so the boundary is established
// here with the SAME statements the birth migration uses — not repaired at boot, where
// v13's rule is to refuse drift rather than heal it.
func RestorePostgresUserAuthorityPrivileges(ctx context.Context, cfg store.Config) error {
	if cfg.Engine != store.EnginePostgres {
		return fmt.Errorf("sqlstore: logical restore authority closure requires postgres")
	}
	dia, _ := dialect.New(cfg.Engine)
	appDB, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer appDB.Close()
	ownerDB := appDB
	if strings.TrimSpace(cfg.OwnerDSN) != "" && cfg.OwnerDSN != cfg.DSN {
		ownerDB, err = openOwnerPool(ctx, dia, cfg, cfg.OwnerDSN)
		if err != nil {
			return err
		}
		defer ownerDB.Close()
	}
	return withMigrationLock(ctx, ownerDB, dia, func(mdb dialect.Execer) (resultErr error) {
		tx, err := mdb.BeginTx(ctx, directoryWriterTxOptions(dia))
		if err != nil {
			return err
		}
		commitAttempted := false
		defer func() {
			// A failed Commit has an ambiguous outcome. Do not replace it with a
			// rollback claim: database/sql does not promise that rollback remains
			// available after Commit has been attempted.
			if commitAttempted {
				return
			}
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				resultErr = errors.Join(resultErr, fmt.Errorf(
					"logical restore User authority rollback could not be confirmed: %w", rollbackErr,
				))
			}
		}()
		// Take the same global writer lock without assuming the post-v10 decoder:
		// absence of H is legitimate only after the predecessor census below.
		if _, err := tx.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`, directoryWriterLockKey); err != nil {
			return err
		}
		witnesses := directoryActivationWitnesses{}
		defer witnesses.close()
		if ownerDB != appDB {
			witnesses.app, err = appDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return err
			}
		}
		ownerPosture, err := dia.ConnRolePosture(ctx, tx)
		if err != nil {
			return err
		}
		appPosture, err := dia.ConnRolePosture(ctx, witnesses.appAuthority(tx))
		if err != nil {
			return err
		}
		roles := guardRoles{
			App:             guardRoleFact{Role: appPosture.Role, Known: true},
			Owner:           guardRoleFact{Role: ownerPosture.Role, Known: true},
			OwnerConfigured: strings.TrimSpace(cfg.OwnerDSN) != "",
		}
		for _, witness := range []struct {
			label   string
			q       directoryWriterACLQuerier
			posture dialect.RolePosture
			role    guardRoleFact
		}{
			{"owner", tx, ownerPosture, roles.Owner}, {"application", witnesses.appAuthority(tx), appPosture, roles.App},
		} {
			if err := verifyDirectoryActivationWriterPosture(witness.label, witness.posture, witness.role); err != nil {
				return err
			}
			if err := verifyDirectoryActivationPinnedSearchPath(ctx, witness.q, witness.label); err != nil {
				return err
			}
		}
		if err := verifyDirectoryActivationDatabaseIdentity(ctx, tx, witnesses); err != nil {
			return err
		}
		if _, err := resolveGuardMetadataPosture(ctx, tx, dia, roles); err != nil {
			return err
		}
		version, err := postgresRestoreAuthorityVersion(ctx, tx, dia)
		if err != nil {
			return err
		}
		if version < coreUserAuthorityMigrationVersion {
			if postgresRestoreAuthorityLegacyRollbackTestHook != nil {
				if err := postgresRestoreAuthorityLegacyRollbackTestHook(tx); err != nil {
					return err
				}
			}
			if err := tx.Rollback(); err != nil {
				return fmt.Errorf("close legacy logical restore authority transaction: %w", err)
			}
			return nil
		}
		owner, app := roles.Owner.Role, roles.App.Role
		// PUBLIC EXECUTE obscures the inherited privilege closure before REVOKE.
		// Compute the closure of exactly the intended grants, without issuing them.
		var unsafeClosure bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS (
   SELECT 1 FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_roles o CROSS JOIN pg_catalog.pg_roles a
   WHERE o.rolname=$1 AND a.rolname=$2 AND NOT r.rolsuper AND r.oid NOT IN (o.oid,a.oid)
   AND (pg_catalog.pg_has_role(r.oid,o.oid,'USAGE') OR pg_catalog.pg_has_role(r.oid,a.oid,'USAGE')))`, owner, app).Scan(&unsafeClosure)
		if err != nil {
			return err
		}
		if unsafeClosure {
			return directoryUnavailable("logical restore authority role closure is not closed", nil)
		}
		functions := []struct{ name, signature string }{
			{"olivares_lock_core_user_authority", "public.olivares_lock_core_user_authority(text)"},
			{"olivares_retain_user_authority", "public.olivares_retain_user_authority()"},
		}
		// Validate BOTH definitions and ACL prestates before making either change.
		for _, f := range functions {
			present, err := verifyPostgresAuthorityFunctionDefinition(ctx, tx, f.name, owner, false)
			if err != nil {
				return err
			}
			if !present {
				return directoryUnavailable("logical restore authority function is absent", nil)
			}
			acl, err := readPostgresAuthorityFunctionACL(ctx, tx, f.name, owner, app)
			if err != nil {
				return err
			}
			if !acl.null && (!acl.closed() || acl.badGrantor) {
				return directoryUnavailable("logical restore authority ACL is neither stripped nor closed", nil)
			}
		}
		for _, f := range functions {
			if _, err := tx.ExecContext(ctx, "REVOKE ALL ON FUNCTION "+f.signature+" FROM PUBLIC"); err != nil {
				return err
			} // #nosec G202 -- closed compiled signatures
			if _, err := tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION "+f.signature+" TO "+quoteIdent(owner)+", "+quoteIdent(app)); err != nil {
				return err
			} // #nosec G202 -- compiled signatures and quoted catalog roles
		}
		for _, f := range functions {
			present, err := verifyPostgresAuthorityFunction(ctx, tx, f.name, owner, app, false)
			if err != nil {
				return err
			}
			if !present {
				return directoryUnavailable("logical restore authority function disappeared before commit", nil)
			}
		}
		// Core v13's relation ACL, in the same transaction and under the same lock: an
		// estate whose tracking says v13 carries a relation the dump described without
		// any of its grants. Below v13 there is no such relation and nothing to close.
		if version >= coreLoginCapabilityMigrationVersion {
			if err := restoreLoginCapabilityACL(ctx, tx, roles); err != nil {
				return err
			}
		}
		if postgresRestoreAuthorityCommitTestHook != nil {
			if err := postgresRestoreAuthorityCommitTestHook(tx); err != nil {
				return err
			}
		}
		commitAttempted = true
		return tx.Commit()
	})
}

// restoreLoginCapabilityACL re-establishes core v13's access boundary on a relation that
// arrived through pg_restore --no-privileges, and then VERIFIES it with the relation's own
// verifier before the caller can commit.
//
// It runs the SAME statements the birth migration runs, taken from the same constants rather
// than copied, so a future change to the boundary cannot apply to new databases and miss
// restored ones — the divergence would be invisible until a restored estate refused to boot,
// which is the failure mode this function exists to remove.
//
// It never repairs a database that merely drifted: the only caller is the logical restore
// path, which has just created every object in this schema.
func restoreLoginCapabilityACL(ctx context.Context, tx *sql.Tx, roles guardRoles) error {
	resolved, err := loginCapabilityTopology([]guardRoles{roles})
	if err != nil {
		return err
	}
	stmts := []string{loginCapabilityBirthRevokeStmt}
	if guardMetadataTopologyOf(resolved) == guardTopologySplit {
		stmts = append(stmts, loginCapabilitySplitACLStmts(resolved.App.bindable())...)
	}
	for _, stmt := range stmts {
		// #nosec G202 -- compiled constants; the only interpolated value is a role name
		// quoted by quoteIdent in loginCapabilitySplitACLStmts.
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlstore: logical restore login capability ACL: %w", err)
		}
	}
	if err := verifyPostgresLoginCapabilityRelation(ctx, tx, resolved); err != nil {
		return fmt.Errorf("sqlstore: logical restore login capability ACL: %w", err)
	}
	return nil
}

// This operation is not a migration or a general schema repair. Require an
// exact active prefix of the compiled migration plan, including the historical
// three-column tracker, then establish H presence/absence independently.
func postgresRestoreAuthorityVersion(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) (int, error) {
	exists, err := coreTrackingRelationExists(ctx, tx, dia)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, directoryUnavailable("logical restore core tracking is absent", nil)
	}
	cols, err := dia.TableColumns(ctx, tx, coreTrackingTable)
	if err != nil {
		return 0, err
	}
	current, err := verifyCoreTrackingRelationShape(ctx, tx, dia, cols)
	if err != nil {
		return 0, err
	}
	versions, err := readCanonicalCoreTrackingVersions(ctx, tx, dia)
	if err != nil {
		return 0, err
	}
	plan := buildCoreMigrationPlan(dia, nil, nil, nil, guardEditionGraph{}, accessEvidenceBootPlan{}, evidenceStateCalibration{})
	// The accepted history is an exact active prefix of this binary's compiled
	// plan, through its supported ceiling. A longer history names a version this
	// binary does not carry and still refuses. The v10 H-introduction constant
	// below keeps selecting legacy no-op versus exact H closure; no version above
	// it runs a migration or repairs an object here.
	if len(versions) == 0 || len(versions) > len(plan) || len(versions) > coreSupportedMigrationVersion {
		return 0, directoryUnavailable("logical restore core tracking is empty or incompatible", nil)
	}
	for i := 0; i < len(versions); i++ {
		migration := plan[i]
		if _, ok := versions[int64(migration.Version)]; !ok {
			return 0, directoryUnavailable("logical restore core tracking is not a recognized prefix", nil)
		}
		var name, applied, phase string
		var reverted sql.NullString
		query := "SELECT name, applied_at, 'expand', NULL FROM public.schema_migrations_core WHERE version=$1"
		if current {
			query = "SELECT name, applied_at, phase, reverted_at FROM public.schema_migrations_core WHERE version=$1"
		}
		if err := tx.QueryRowContext(ctx, query, migration.Version).Scan(&name, &applied, &phase, &reverted); err != nil {
			return 0, err
		}
		if name != migration.Name || strings.TrimSpace(applied) == "" || phase != "expand" || reverted.Valid {
			return 0, directoryUnavailable("logical restore core tracking is not an exact active record", nil)
		}
	}
	version := plan[len(versions)-1].Version
	if version >= coreDirectoryMigrationVersion && !current {
		return 0, directoryUnavailable("logical restore core tracking has an incompatible legacy shape", nil)
	}
	var relation, functions int
	err = tx.QueryRowContext(ctx, `SELECT
  (SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='core_user_authority'),
  (SELECT count(*) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname IN ('olivares_lock_core_user_authority','olivares_retain_user_authority','olivares_directory_inventory_v1'))`).Scan(&relation, &functions)
	if err != nil {
		return 0, err
	}
	controlCols, err := dia.TableColumns(ctx, tx, dialect.DirectoryWriterControlTable)
	if err != nil {
		return 0, err
	}
	if version < coreUserAuthorityMigrationVersion {
		if relation != 0 || functions != 0 || controlCols["coverage_protocol"] {
			return 0, directoryUnavailable("logical restore predecessor contains mixed User authority objects", nil)
		}
		if version >= coreDirectoryMigrationVersion {
			if _, err := readLegacyDirectoryWriterControlState(ctx, tx, dia); err != nil {
				return 0, err
			}
		}
		return version, nil
	}
	if relation != 1 || !controlCols["coverage_protocol"] {
		return 0, directoryUnavailable("logical restore User authority prestate is incomplete", nil)
	}
	if _, err := readDirectoryWriterControlState(ctx, tx, dia); err != nil {
		return 0, err
	}
	shape, found, err := inspectCoreDirectoryRelation(ctx, tx, dia, userAuthorityDescriptor.Table)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, directoryUnavailable("logical restore User authority relation is absent", nil)
	}
	if err := verifyCoreDirectoryRelationShape(dia, userAuthorityDescriptor, shape); err != nil {
		return 0, err
	}
	return version, nil
}
