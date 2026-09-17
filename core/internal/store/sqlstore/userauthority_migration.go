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
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The v7 dialect statements remain immutable. Only v10 and its exact current
// shape verifier consume this wrapper; no historical migration renders H or the
// new protocol column through its original descriptor/statement list.
type userAuthorityControlDialect struct{ dialect.Dialect }

func (d userAuthorityControlDialect) DirectoryWriterControlStmts() []string {
	stmts := append([]string(nil), d.Dialect.DirectoryWriterControlStmts()...)
	if d.Name() == store.EnginePostgres {
		stmts[0] = strings.Replace(stmts[0], "expected_generation pg_catalog.int8 NOT NULL,", "expected_generation pg_catalog.int8 NOT NULL,\n  coverage_protocol pg_catalog.text COLLATE pg_catalog.\"C\" NOT NULL,", 1)
		stmts[0] = strings.TrimSuffix(stmts[0], "\n)") + ",\n  CHECK (coverage_protocol OPERATOR(pg_catalog.=) 'membership-union-v1' OR coverage_protocol OPERATOR(pg_catalog.=) 'user-authority-v1')\n)"
	} else {
		stmts[0] = strings.Replace(stmts[0], "expected_generation INTEGER NOT NULL,", "expected_generation INTEGER NOT NULL,\n  coverage_protocol TEXT COLLATE BINARY NOT NULL,", 1)
		stmts[0] = strings.TrimSuffix(stmts[0], "\n)") + ",\n  CHECK (coverage_protocol IN ('membership-union-v1', 'user-authority-v1'))\n)"
		stmts[2] = strings.Replace(stmts[2], "generation INTEGER NOT NULL,", "generation INTEGER NOT NULL,\n  coverage_protocol TEXT COLLATE BINARY NOT NULL,", 1)
		stmts[2] = strings.TrimSuffix(stmts[2], "\n)") + ",\n  CHECK (coverage_protocol IN ('membership-union-v1', 'user-authority-v1'))\n)"
	}
	return stmts
}

func verifyDirectoryWriterControl(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) (directoryWriterControlState, error) {
	columns, err := dia.TableColumns(ctx, tx, coreTrackingTable)
	if err != nil {
		return directoryWriterControlState{}, err
	}
	if len(columns) != 0 {
		tracked, err := coreVersionIsTracked(ctx, tx, dia, coreUserAuthorityMigrationVersion)
		if err != nil {
			return directoryWriterControlState{}, err
		}
		if tracked {
			return verifyUserAuthorityControl(ctx, tx, dia)
		}
	}
	return verifyDirectoryWriterControlShape(ctx, tx, dia)
}

func verifyUserAuthorityControl(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) (directoryWriterControlState, error) {
	if _, err := verifyDirectoryWriterControlShape(ctx, tx, userAuthorityControlDialect{dia}); err != nil {
		return directoryWriterControlState{}, err
	}
	return readDirectoryWriterControlState(ctx, tx, dia)
}

func readDirectoryWriterControlState(ctx context.Context, q directoryTenantEnumerator, dia dialect.Dialect) (directoryWriterControlState, error) {
	state, err := readLegacyDirectoryWriterControlState(ctx, q, dia)
	if err != nil {
		return state, err
	}
	query := "SELECT coverage_protocol FROM " + directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)
	if dia.Name() == store.EngineSQLite {
		query = "SELECT coverage_protocol, typeof(coverage_protocol) FROM " + directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)
	}
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return state, directoryUnavailable("read directory coverage protocol", err)
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		count++
		if dia.Name() == store.EngineSQLite {
			var storageClass string
			err = rows.Scan(&state.CoverageProtocol, &storageClass)
			if err == nil && storageClass != "text" {
				err = fmt.Errorf("protocol is not text")
			}
		} else {
			err = rows.Scan(&state.CoverageProtocol)
		}
		if err != nil {
			return state, directoryUnavailable("decode directory coverage protocol", err)
		}
	}
	if err := rows.Err(); err != nil {
		return state, err
	}
	if count != 1 || (state.CoverageProtocol != coverageProtocolLegacy && state.CoverageProtocol != coverageProtocolTarget) {
		return state, directoryUnavailable("directory coverage protocol is not a closed singleton", nil)
	}
	if state.CoverageProtocol == coverageProtocolTarget && state.Mode != directoryWriterEnforced {
		return state, directoryUnavailable("target directory coverage requires enforced mode", nil)
	}
	return state, nil
}

func sqliteDirectoryWriterGuardBody(needsGeneration string) string {
	body := legacySQLiteDirectoryWriterGuardBody(needsGeneration)
	body = strings.Replace(body, "AND expected_generation > 0)", "AND expected_generation > 0\n           AND typeof(coverage_protocol) = 'text'\n           AND coverage_protocol COLLATE BINARY IN ('membership-union-v1', 'user-authority-v1'))", 1)
	return strings.Replace(body, "AND m.generation = c.expected_generation)", "AND m.generation = c.expected_generation\n            AND typeof(m.coverage_protocol) = 'text'\n            AND m.coverage_protocol COLLATE BINARY = c.coverage_protocol COLLATE BINARY)", 1)
}

func postgresUserAuthorityWriterGuardBody() string {
	body := strings.Replace(postgresDirectoryWriterGuardBody, "  stored_generation bigint;", "  stored_generation bigint;\n  stored_protocol text COLLATE pg_catalog.\"C\";\n  presented_protocol text COLLATE pg_catalog.\"C\";", 1)
	body = strings.Replace(body, "'agent_group_members', 'orgs'", "'agent_group_members', 'orgs', 'auth_sessions', 'core_user_authority'", 1)
	body = strings.Replace(body, "pg_catalog.min(c.expected_generation)\n    INTO control_rows, stored_key, stored_mode, stored_generation", "pg_catalog.min(c.expected_generation), pg_catalog.min(c.coverage_protocol)\n    INTO control_rows, stored_key, stored_mode, stored_generation, stored_protocol", 1)
	body = strings.Replace(body, "OR stored_generation <= 0 THEN", "OR stored_generation <= 0\n     OR stored_protocol IS NULL\n     OR stored_protocol NOT IN ('membership-union-v1', 'user-authority-v1') THEN", 1)
	return strings.Replace(body, "    presented_generation := pg_catalog.current_setting", "    presented_protocol := pg_catalog.current_setting('app.directory_coverage_protocol', true);\n    IF presented_protocol IS DISTINCT FROM stored_protocol COLLATE pg_catalog.\"C\" THEN\n      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'directory coverage protocol required';\n    END IF;\n    presented_generation := pg_catalog.current_setting", 1)
}

// v10 verifies the predecessor before replacing only its authored control and
// guards. This is not a staged repair of arbitrary drift. H backfill and the
// protocol cutover belong to the separate maintenance transaction.
func coreUserAuthorityMigration(dia dialect.Dialect, authorityRoles ...guardRoles) migrate.Migration {
	return migrate.Migration{Version: coreUserAuthorityMigrationVersion, Name: "user_authority", Exec: func(ctx context.Context, tx *sql.Tx) error {
		if err := requireCoreVersionTracked(ctx, tx, dia, coreAccessEvidenceMigrationVersion); err != nil {
			return err
		}
		state, err := verifyDirectoryWriterControlShape(ctx, tx, dia)
		if err != nil {
			return err
		}
		if err := verifyLegacyDirectoryWriterGuards(ctx, tx, dia, state); err != nil {
			return err
		}
		if columns, err := dia.TableColumns(ctx, tx, userAuthorityDescriptor.Table); err != nil || len(columns) != 0 {
			return directoryUnavailable("untracked User authority relation already exists", err)
		}
		// Remove only the previously verified trigger identities, inside the DDL
		// transaction. An enforced predecessor stays enforced in the new control.
		if dia.Name() == store.EngineSQLite {
			for _, spec := range sqliteDirectoryWriterGuardSpecsFor(legacyDirectoryWriterSourceTables, legacySQLiteDirectoryWriterGuardBody) {
				if _, err := tx.ExecContext(ctx, "DROP TRIGGER IF EXISTS main."+quoteIdent(spec.Name)); err != nil {
					return err
				}
			}
		} else {
			for _, table := range legacyDirectoryWriterSourceTables {
				if _, err := tx.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+quoteIdent(table+"_directory_writer_guard")+" ON public."+quoteIdent(table)); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, "DROP FUNCTION IF EXISTS public."+dialect.DirectoryWriterGuardFunction+"()"); err != nil {
				return err
			}
		}
		const predecessor = "directory_writer_control_v9_predecessor"
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+directoryWriterRelation(dia, dialect.DirectoryWriterControlTable)+" RENAME TO "+quoteIdent(predecessor)); err != nil {
			return err
		}
		// Drop the verified predecessor within this atomic migration before
		// recreating its exact names (PostgreSQL retains the old primary index
		// name across RENAME). The captured tuple is reinserted unchanged below.
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+directoryWriterRelation(dia, predecessor)); err != nil {
			return err
		}
		stmts := (userAuthorityControlDialect{dia}).DirectoryWriterControlStmts()
		if _, err := tx.ExecContext(ctx, stmts[0]); err != nil {
			return err
		}
		query := dia.Rebind("INSERT INTO " + directoryWriterRelation(dia, dialect.DirectoryWriterControlTable) + "(control_key,mode,expected_generation,coverage_protocol) VALUES (?,?,?,?)")
		if _, err := tx.ExecContext(ctx, query, directoryWriterLockKey, string(state.Mode), state.ExpectedGeneration, coverageProtocolLegacy); err != nil {
			return err
		}
		if dia.Name() == store.EngineSQLite {
			if _, err := tx.ExecContext(ctx, "DROP TABLE "+directoryWriterRelation(dia, dialect.DirectoryWriterMarkerTable)); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, stmts[2]); err != nil {
			return err
		}
		for _, stmt := range dia.CreateTableStmts(userAuthorityDescriptor) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if dia.Name() == store.EnginePostgres {
			if _, err := tx.ExecContext(ctx, postgresUserAuthorityRetentionDDL); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "REVOKE ALL ON FUNCTION public.olivares_retain_user_authority() FROM PUBLIC"); err != nil {
				return err
			}
		}
		for _, stmt := range userAuthorityRetentionDDL(dia) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if dia.Name() == store.EngineSQLite {
			for _, spec := range sqliteDirectoryWriterGuardSpecs() {
				if _, err := tx.ExecContext(ctx, spec.CreateStatement); err != nil {
					return err
				}
			}
		} else {
			if _, err := tx.ExecContext(ctx, postgresDirectoryWriterFunctionDDL()); err != nil {
				return err
			}
			for _, table := range directoryWriterSourceTables {
				if _, err := tx.ExecContext(ctx, postgresDirectoryWriterTriggerDDL(table)); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, postgresDirectoryWriterTriggerAlwaysDDL(table)); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, postgresUserAuthorityLockDDL); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "REVOKE ALL ON FUNCTION public.olivares_lock_core_user_authority(text) FROM PUBLIC"); err != nil {
				return err
			}
			if len(authorityRoles) != 0 {
				roles := authorityRoles[0]
				if roles.App.bindable() == "" {
					return directoryUnavailable("User authority migration app role is unresolved", nil)
				}
				if _, err := tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION public.olivares_lock_core_user_authority(text) TO "+quoteIdent(roles.App.Role)); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "GRANT EXECUTE ON FUNCTION public.olivares_retain_user_authority() TO "+quoteIdent(roles.App.Role)); err != nil {
					return err
				}
				if err := verifyPostgresUserAuthorityLock(ctx, tx, roles); err != nil {
					return err
				}
			}
		}
		after, err := verifyUserAuthorityControl(ctx, tx, dia)
		if err != nil {
			return err
		}
		if after.Mode != state.Mode || after.ExpectedGeneration != state.ExpectedGeneration || after.CoverageProtocol != coverageProtocolLegacy {
			return directoryUnavailable("User authority migration changed predecessor mode/generation", nil)
		}
		return verifyUserAuthorityRelation(ctx, tx, dia)
	}}
}

func verifyLegacyDirectoryWriterGuards(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, state directoryWriterControlState) error {
	if dia.Name() == store.EngineSQLite {
		missing, err := inspectSQLiteDirectoryWriterGuards(ctx, tx, dia, sqliteDirectoryWriterGuardSpecsFor(legacyDirectoryWriterSourceTables, legacySQLiteDirectoryWriterGuardBody))
		if err != nil {
			return err
		}
		if state.Mode == directoryWriterEnforced && len(missing) != 0 {
			return directoryUnavailable("enforced legacy writer guards are missing", nil)
		}
		return nil
	}
	want := canonicalPostgresDirectoryWriterDefinition()
	want.Function.Src = "\n" + postgresDirectoryWriterGuardBody + "\n"
	function, present, err := projectGuardFunction(ctx, tx, dialect.EngineSchema, dialect.DirectoryWriterGuardFunction)
	if err != nil {
		return err
	}
	if present {
		if diff := guardFunctionDiff(want.Function, function); len(diff) != 0 {
			return fmt.Errorf("legacy writer function drift: %v", diff)
		}
		if err := verifyPostgresDirectoryWriterFunctionConfig(ctx, tx); err != nil {
			return err
		}
	} else if state.Mode == directoryWriterEnforced {
		return directoryUnavailable("enforced legacy writer function missing", nil)
	}
	if extras, err := unexpectedPostgresDirectoryWriterAttachments(ctx, tx); err != nil || len(extras) != 0 {
		return directoryUnavailable(fmt.Sprintf("unexpected legacy writer attachments %v", extras), err)
	}
	for _, table := range directoryWriterSourceTables {
		row, err := projectGuardCatalogRow(ctx, tx, guardKey{Schema: dialect.EngineSchema, Relation: table, Trigger: table + "_directory_writer_guard"})
		if err != nil {
			return err
		}
		legacy := table != "auth_sessions" && table != userAuthorityDescriptor.Table
		if !legacy {
			if row.GuardExists {
				return directoryUnavailable("untracked writer guard on "+table, nil)
			}
			continue
		}
		if !row.GuardExists && state.Mode == directoryWriterStaged {
			continue
		}
		if !row.RelationExists || !row.GuardExists || !row.FunctionExists || row.EnableState != string(dialect.TriggerFiresAlways) {
			return directoryUnavailable("legacy writer guard unavailable on "+table, nil)
		}
		if diff := guardDefinitionDiff(want, row.definition()); len(diff) != 0 {
			return fmt.Errorf("legacy writer trigger drift on %s: %v", table, diff)
		}
	}
	return nil
}

func verifyUserAuthorityRelation(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) error {
	shape, found, err := inspectCoreDirectoryRelation(ctx, tx, dia, userAuthorityDescriptor.Table)
	if err != nil || !found {
		return directoryUnavailable("User authority relation unavailable", err)
	}
	if err := verifyCoreDirectoryRelationShape(dia, userAuthorityDescriptor, shape); err != nil {
		return err
	}
	if err := verifyCoreDirectoryRelationContract(ctx, tx, userAuthorityContractDialect{dia}, userAuthorityDescriptor); err != nil {
		return err
	}
	live, err := dia.SchemaTriggers(ctx, tx)
	if err != nil {
		return err
	}
	schema := "main"
	if dia.Name() == store.EnginePostgres {
		schema = "public"
	}
	posture, err := dia.ConnRolePosture(ctx, tx)
	if err != nil {
		return err
	}
	return schemaInvariantViolation(schema, posture, live, []registeredSchemaTrigger{{namespace: coreSchemaInvariantNamespace, SchemaTrigger: userAuthoritySchemaInvariants()[dia.Name()][0]}}, nil, dia.Name(), false)
}

// Keep the exact relation oracle, including all additional authored H guards.
// The PostgreSQL probe substitutes only the relation's generated names.
type userAuthorityContractDialect struct{ dialect.Dialect }

func (d userAuthorityContractDialect) CreateTableStmts(desc model.EntityDescriptor) []string {
	stmts := append([]string(nil), d.Dialect.CreateTableStmts(desc)...)
	for _, stmt := range userAuthorityRetentionDDL(d.Dialect) {
		stmt = strings.ReplaceAll(stmt, userAuthorityDescriptor.Table, desc.Table)
		if d.Name() == store.EngineSQLite {
			stmt = strings.Replace(stmt, "CREATE TRIGGER main.", "CREATE TRIGGER ", 1)
		}
		stmts = append(stmts, stmt)
	}
	if d.Name() == store.EngineSQLite {
		for _, spec := range sqliteDirectoryWriterGuardSpecsFor([]string{desc.Table}, sqliteDirectoryWriterGuardBody) {
			stmts = append(stmts, spec.Definition)
		}
	} else {
		stmts = append(stmts, postgresDirectoryWriterTriggerDDL(desc.Table), postgresDirectoryWriterTriggerAlwaysDDL(desc.Table))
	}
	return stmts
}

func verifyUserAuthorityPerBoot(ctx context.Context, db dialect.Execer, dia dialect.Dialect) error {
	tx, err := db.BeginTx(ctx, directoryWriterTxOptions(dia))
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // read-only verifier
	return verifyUserAuthorityRelation(ctx, tx, dia)
}

const postgresUserAuthorityLockBody = `
DECLARE
  observed pg_catalog.int8;
BEGIN
  IF pg_catalog.current_setting('app.tenant_id', true) IS DISTINCT FROM 'ffffffff-ffff-ffff-ffff-ffffffffffff'
     OR target_user IS NULL
     OR target_user !~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' THEN
    RAISE EXCEPTION 'invalid User authority lock scope';
  END IF;
  SELECT h.version INTO STRICT observed
    FROM public.core_user_authority h
    WHERE h.id = target_user AND h.tenant_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
    FOR UPDATE;
  IF observed IS NULL OR observed < 1 THEN
    RAISE EXCEPTION 'invalid User authority version';
  END IF;
  RETURN observed;
END;
`

const postgresUserAuthorityLockDDL = `CREATE FUNCTION public.olivares_lock_core_user_authority(target_user text)
RETURNS bigint LANGUAGE plpgsql VOLATILE SECURITY DEFINER PARALLEL UNSAFE
SET search_path = pg_catalog AS $user_authority$` + postgresUserAuthorityLockBody + `$user_authority$`

// The exact identity of the core retention invariant, written once because the comparator
// gate, the declaration and the DDL below must name the SAME three strings. A gate that
// re-derived them from whatever happens to be registered would accept any future declaration
// that borrowed the name; these are the literals root ratified.
const (
	userAuthorityRetentionTable       = "core_user_authority"
	userAuthorityRetentionTriggerName = "core_user_authority_no_delete"
	userAuthorityRetentionFunction    = "olivares_retain_user_authority"
)

// A distinct handler is intentional: H is mutable, while the existing catalog
// discovery treats every relation using olivares_block_mutation as append-only
// and revokes UPDATE. Retention is a separate invariant from immutability.
const postgresUserAuthorityRetentionDDL = `CREATE FUNCTION public.olivares_retain_user_authority()
RETURNS trigger LANGUAGE plpgsql VOLATILE SECURITY INVOKER
SET search_path = pg_catalog AS $retention$
BEGIN
  RAISE EXCEPTION 'User authority is permanent';
END;
$retention$`

func userAuthorityRetentionDDL(dia dialect.Dialect) []string {
	if dia.Name() == store.EnginePostgres {
		return []string{
			"CREATE TRIGGER core_user_authority_no_delete BEFORE DELETE OR TRUNCATE ON public.core_user_authority FOR EACH STATEMENT EXECUTE FUNCTION public.olivares_retain_user_authority()",
			"ALTER TABLE ONLY public.core_user_authority ENABLE ALWAYS TRIGGER core_user_authority_no_delete",
		}
	}
	return []string{"CREATE TRIGGER main.core_user_authority_no_delete BEFORE DELETE ON core_user_authority\nBEGIN\n  SELECT RAISE(ABORT, 'User authority is permanent');\nEND"}
}

// THE TWO COMPLETE FRAMED CATALOG DIGESTS OF THE RETENTION REVISION THAT WAS MEASURED.
//
// They are stored together and they are IMMUTABLE: the pair belongs to ONE revision of the
// retention body — the one measured below — and not to the trigger name, not to the invariant,
// and NOT to whatever the current declaration becomes next. Updating a later revision means
// changing postgresUserAuthorityRetentionDigest, not editing these two values in place.
//
// PostgreSQL renders the SAME tgfoid-bound handler two ways in pg_get_triggerdef. While
// `olivares_retain_user_authority` is an unambiguous zero-argument reference the deparse is
// bare; add any overload of that name that is CALLABLE WITH ZERO ARGUMENTS — a
// `(review text DEFAULT 'review')` is the measured witness — and the same trigger deparses
// SCHEMA-QUALIFIED. Nothing about the trigger moved: tgfoid still points at the routine core
// v10 created, and pg_get_functiondef of that OID is byte-identical across both renderings.
//
// Both values were MEASURED on PostgreSQL 16.15 (Debian 16.15-1.pgdg12+2, server_version_num
// 160015) over the exact catalog bytes this build installs, and both are pinned against a real
// SchemaTriggers read by TestPostgresUserAuthorityRetentionRenderingPinsBothMeasuredForms.
//
// NEITHER digest encodes owner or ACL — pg_get_functiondef does not render them. The measured
// GRANT-to-PUBLIC and changed-owner states hash to exactly the qualified value. The independent
// owner/ACL checks (the app EXECUTE probe here, the inventory helpers in the logical restore
// ceremony) remain the only verification of those, and this pair claims nothing about them.
const (
	// postgresRetentionMeasuredRevisionDigest is the canonical form of the measured revision:
	// the bare handler reference PostgreSQL deparses when that revision's zero-input routine is
	// the only one callable under its name.
	postgresRetentionMeasuredRevisionDigest = "f1002d3a6cff7cd71088c6a663466d39e0b0cf93de1b1dfd483c3071107b8ed5"
	// postgresRetentionMeasuredRevisionQualifiedDigest is the SAME trigger and the SAME bound
	// function body of that SAME revision, deparsed with the `public.` qualifier because the
	// name stopped being an unambiguous zero-argument reference. It is an alias for one
	// rendering of one revision, not a second accepted object and not a standing exception.
	postgresRetentionMeasuredRevisionQualifiedDigest = "9a2fb3dc21ee63d611fd2fa43758358f8bfe0f21f99066ee484991d4c62f6a32"
)

// postgresUserAuthorityRetentionDigest is the CURRENT compiled canonical declaration — the
// digest userAuthoritySchemaInvariants registers, and the ONLY value a new retention revision
// changes.
//
// It is written as a reference to the measured constant rather than as a second copy of the
// same literal, because today's compiled revision IS the measured one. THE REFERENCE IS THE
// WHOLE POINT OF THE SEPARATION, and it is one-directional: the equivalence gate in
// schemaInvariantDefinitionMatches compares this declaration against the IMMUTABLE
// postgresRetentionMeasuredRevisionDigest above, never against itself. Replace this line with
// a future revision's own measured canonical digest and the gate stops matching by itself —
// the qualified companion of the OLD body is not carried forward, and the new declaration
// falls back to ordinary exact equality until its own companion is measured and registered
// deliberately.
//
// Before the correction these were ONE constant, so a revision bump moved the declaration and
// the gate's operand together and the old qualified body stayed accepted. The regression that
// holds the separation open is
// TestUserAuthorityRetentionMeasuredPairIsIndependentOfTheCurrentDeclaration.
const postgresUserAuthorityRetentionDigest = postgresRetentionMeasuredRevisionDigest

// Measured by TestUserAuthorityRetentionCatalog on SQLite and PostgreSQL 16.15.
// The runtime verifier hashes the actual catalog trigger/function composition.
func userAuthoritySchemaInvariants() map[store.Engine][]store.SchemaTrigger {
	return map[store.Engine][]store.SchemaTrigger{
		store.EngineSQLite:   {{Name: "core_user_authority_no_delete", Table: "core_user_authority", DefinitionSHA256: "35b7bb5f52eb69f9775b5529577b1d7a72b8ebce816439e1694e4dac26ad76f8"}},
		store.EnginePostgres: {{Name: userAuthorityRetentionTriggerName, Table: userAuthorityRetentionTable, DefinitionSHA256: postgresUserAuthorityRetentionDigest}},
	}
}
