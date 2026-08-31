// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// verifyCoreDirectoryRelationContract verifies every durable object
// CreateTableStmts declares for one directory relation, except PostgreSQL ACLs.
// ACL convergence deliberately occurs later in Open; accepting a relation that
// lacks a CHECK, uniqueness, tenant guard or append-only guard here would be a
// different matter, because none of those can be inferred from its columns.
func verifyCoreDirectoryRelationContract(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	desc model.EntityDescriptor,
) error {
	switch dia.Name() {
	case store.EngineSQLite:
		return verifySQLiteCoreDirectoryRelationContract(ctx, tx, dia, desc)
	case store.EnginePostgres:
		return verifyPostgresCoreDirectoryRelationContract(ctx, tx, dia, desc)
	default:
		return fmt.Errorf("unsupported engine %q", dia.Name())
	}
}

// SQLite preserves the CREATE text in sqlite_master. Comparing the complete
// object set in both directions is therefore its exact oracle: table text
// covers columns and CHECKs, while the separately stored index and trigger
// texts cover uniqueness, tenant tripwires and append-only guards. Implicit
// sqlite_autoindex objects have NULL sql and are already represented by the
// PRIMARY KEY/UNIQUE clauses in the table text.
func verifySQLiteCoreDirectoryRelationContract(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	desc model.EntityDescriptor,
) error {
	want := make(map[string]string)
	for _, stmt := range dia.CreateTableStmts(desc) {
		name, ok := sqliteCoreDirectoryObjectName(stmt)
		if !ok {
			return fmt.Errorf("cannot name rendered SQLite object %.80q", stmt)
		}
		if previous, duplicate := want[name]; duplicate {
			return fmt.Errorf("rendered SQLite object %q is duplicated (%q and %q)",
				name, previous, stmt)
		}
		want[name] = stmt
	}

	rows, err := tx.QueryContext(ctx, `SELECT name, sql FROM sqlite_master
WHERE (tbl_name = ? OR name = ?) AND sql IS NOT NULL
ORDER BY name`, desc.Table, desc.Table)
	if err != nil {
		return fmt.Errorf("read sqlite_master: %w", err)
	}
	got := make(map[string]string)
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read sqlite_master: %w", err)
		}
		got[name] = definition
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return fmt.Errorf("read sqlite_master: %w", err)
	}

	for _, name := range sortedKeys(want) {
		definition, exists := got[name]
		if !exists {
			return fmt.Errorf("missing declared SQLite object %q", name)
		}
		if definition != want[name] {
			return fmt.Errorf("SQLite object %q is stored as %q, want %q",
				name, definition, want[name])
		}
	}
	for _, name := range sortedKeys(got) {
		if _, declared := want[name]; !declared {
			return fmt.Errorf("unexpected SQLite object %q is attached to %q", name, desc.Table)
		}
	}
	return nil
}

func sqliteCoreDirectoryObjectName(stmt string) (string, bool) {
	fields := strings.Fields(stmt)
	if len(fields) >= 4 && strings.EqualFold(fields[0], "CREATE") &&
		strings.EqualFold(fields[1], "UNIQUE") && strings.EqualFold(fields[2], "INDEX") {
		return fields[3], fields[3] != ""
	}
	return sqliteObjectName(stmt)
}

// PostgreSQL stores deparsed catalog state rather than the submitted CREATE
// text. The exact, server-version-local oracle is a short-lived probe relation
// rendered from the same descriptor on the same server. Target and probe are
// projected through the same catalog queries and compared after normalizing
// their generated object names. A savepoint rollback removes the probe before
// the v7 continuation runs, so the fresh branch issues no DDL against a target
// relation and leaves no durable object besides the v7 tracking/guard records.
func verifyPostgresCoreDirectoryRelationContract(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	desc model.EntityDescriptor,
) error {
	const probeSavepoint = "olv_k3_directory_contract_probe"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+probeSavepoint); err != nil {
		return fmt.Errorf("create contract-probe savepoint: %w", err)
	}

	// The probe carries an append-only trigger. Once the K2 event fence is
	// installed, DROP TABLE would be (correctly) refused because its cascade
	// drops that *_immutable trigger. Rollback is the only cleanup operation
	// that both removes every probe object and does not ask the fence to permit
	// guard removal. projectPostgresCoreDirectoryContract closes each *sql.Rows
	// before returning, so no live portal crosses this rollback boundary.
	contractErr := comparePostgresCoreDirectoryRelationContract(ctx, tx, dia, desc)
	_, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+probeSavepoint)
	_, releaseErr := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+probeSavepoint)
	var cleanupErr error
	if rollbackErr != nil {
		cleanupErr = errors.Join(cleanupErr,
			fmt.Errorf("rollback contract-probe savepoint: %w", rollbackErr))
	}
	if releaseErr != nil {
		cleanupErr = errors.Join(cleanupErr,
			fmt.Errorf("release contract-probe savepoint: %w", releaseErr))
	}
	return errors.Join(contractErr, cleanupErr)
}

func comparePostgresCoreDirectoryRelationContract(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	desc model.EntityDescriptor,
) error {
	probe, err := postgresCoreDirectoryProbeDescriptor(desc)
	if err != nil {
		return err
	}
	for statement, stmt := range dia.CreateTableStmts(probe) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create contract probe %q statement %d: %w",
				probe.Table, statement+1, err)
		}
	}

	want, wantErr := projectPostgresCoreDirectoryContract(ctx, tx, probe.Table, desc.Table)
	got, gotErr := projectPostgresCoreDirectoryContract(ctx, tx, desc.Table, desc.Table)
	switch {
	case wantErr != nil:
		return fmt.Errorf("project contract probe %q: %w", probe.Table, wantErr)
	case gotErr != nil:
		return fmt.Errorf("project target contract %q: %w", desc.Table, gotErr)
	}
	return postgresCoreDirectoryContractDifference(want, got)
}

func postgresCoreDirectoryProbeDescriptor(
	desc model.EntityDescriptor,
) (model.EntityDescriptor, error) {
	probeNames := map[string]string{
		"core_directory_epoch":     "olv_k3p_epoch",
		"core_directory_tombstone": "olv_k3p_dt",
		"core_user_tombstone":      "olv_k3p_ut",
	}
	probeName, ok := probeNames[desc.Table]
	if !ok {
		return model.EntityDescriptor{}, fmt.Errorf("no PostgreSQL contract probe name for %q", desc.Table)
	}
	probe := desc
	probe.Table = probeName
	probe.Fields = append([]model.FieldSpec(nil), desc.Fields...)
	probe.Checks = append([]string(nil), desc.Checks...)
	probe.Indexes = make([]model.IndexSpec, len(desc.Indexes))
	for i, index := range desc.Indexes {
		probe.Indexes[i] = index
		probe.Indexes[i].Columns = append([]string(nil), index.Columns...)
		if !strings.Contains(index.Name, desc.Table) {
			return model.EntityDescriptor{}, fmt.Errorf(
				"directory index %q does not carry table name %q, so its probe name cannot be normalized exactly",
				index.Name, desc.Table)
		}
		probe.Indexes[i].Name = strings.ReplaceAll(index.Name, desc.Table, probeName)
	}
	return probe, nil
}

type postgresCoreDirectoryContract struct {
	Relation    string
	Columns     []string
	Constraints []string
	Indexes     []string
	Policies    []string
	Rules       []string
	Triggers    []string
}

func projectPostgresCoreDirectoryContract(
	ctx context.Context,
	tx *sql.Tx,
	table string,
	canonicalTable string,
) (postgresCoreDirectoryContract, error) {
	var out postgresCoreDirectoryContract
	var kind, persistence, replicaIdentity string
	var partition, rowSecurity, forceRowSecurity, hasParent, hasChild bool
	if err := tx.QueryRowContext(ctx, `SELECT c.relkind::text, c.relpersistence::text,
       c.relispartition, c.relrowsecurity, c.relforcerowsecurity, c.relreplident::text,
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid),
       EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhparent = c.oid)
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, table).
		Scan(&kind, &persistence, &partition, &rowSecurity, &forceRowSecurity,
			&replicaIdentity, &hasParent, &hasChild); err != nil {
		return out, err
	}
	out.Relation = fmt.Sprintf(
		"kind=%s|persistence=%s|partition=%t|rls=%t|force_rls=%t|replica_identity=%s|parent=%t|child=%t",
		kind, persistence, partition, rowSecurity, forceRowSecurity, replicaIdentity,
		hasParent, hasChild)

	rows, err := tx.QueryContext(ctx, `SELECT a.attname,
       pg_catalog.format_type(a.atttypid, a.atttypmod), a.attnotnull,
       COALESCE(pg_catalog.pg_get_expr(ad.adbin, ad.adrelid), ''),
       a.attidentity::text, a.attgenerated::text, a.attstorage::text,
       a.attcompression::text, a.attcollation::text, a.atthasmissing, a.attisdropped
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0
LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = c.oid AND ad.adnum = a.attnum
WHERE n.nspname = $1 AND c.relname = $2
ORDER BY a.attnum`, dialect.EngineSchema, table)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var name, typ, defaultExpr, identity, generated, storage, compression, collation string
		var notNull, hasMissing, dropped bool
		if err := rows.Scan(&name, &typ, &notNull, &defaultExpr, &identity, &generated,
			&storage, &compression, &collation, &hasMissing, &dropped); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Columns = append(out.Columns, fmt.Sprintf(
			"name=%q|type=%q|not_null=%t|default=%q|identity=%q|generated=%q|storage=%q|compression=%q|collation=%q|missing=%t|dropped=%t",
			name, typ, notNull, defaultExpr, identity, generated, storage, compression,
			collation, hasMissing, dropped))
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return out, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT con.contype::text, con.convalidated,
       con.condeferrable, con.condeferred, con.connoinherit,
       COALESCE((SELECT pg_catalog.string_agg(a.attname, ',' ORDER BY k.ord)
                 FROM pg_catalog.unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
                 JOIN pg_catalog.pg_attribute a
                   ON a.attrelid = con.conrelid AND a.attnum = k.attnum), ''),
       pg_catalog.pg_get_constraintdef(con.oid, false)
FROM pg_catalog.pg_constraint con
JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, table)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var constraintType, columns, definition string
		var validated, deferrable, deferred, noInherit bool
		if err := rows.Scan(&constraintType, &validated, &deferrable, &deferred,
			&noInherit, &columns, &definition); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Constraints = append(out.Constraints, fmt.Sprintf(
			"type=%s|validated=%t|deferrable=%t|deferred=%t|no_inherit=%t|columns=%q|definition=%q",
			constraintType, validated, deferrable, deferred, noInherit, columns, definition))
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return out, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT ic.relname, i.indisunique, i.indisprimary,
       i.indisexclusion, i.indimmediate, i.indisclustered, i.indisvalid,
       i.indcheckxmin, i.indisready, i.indislive, i.indisreplident,
       i.indnatts, i.indnkeyatts, i.indnullsnotdistinct,
       i.indkey::text, i.indcollation::text, i.indclass::text, i.indoption::text,
       am.amname, pg_catalog.pg_get_indexdef(i.indexrelid, 0, false)
FROM pg_catalog.pg_index i
JOIN pg_catalog.pg_class c ON c.oid = i.indrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
JOIN pg_catalog.pg_am am ON am.oid = ic.relam
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, table)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var name, key, collations, classes, options, method, definition string
		var unique, primary, exclusion, immediate, clustered, valid bool
		var checkXmin, ready, live, replicaIdentity, nullsNotDistinct bool
		var attributes, keyAttributes int
		if err := rows.Scan(&name, &unique, &primary, &exclusion, &immediate,
			&clustered, &valid, &checkXmin, &ready, &live, &replicaIdentity,
			&attributes, &keyAttributes, &nullsNotDistinct, &key, &collations,
			&classes, &options, &method, &definition); err != nil {
			_ = rows.Close()
			return out, err
		}
		name = normalizePostgresDirectoryObject(name, table, canonicalTable)
		definition = normalizePostgresDirectoryObject(definition, table, canonicalTable)
		out.Indexes = append(out.Indexes, fmt.Sprintf(
			"name=%q|unique=%t|primary=%t|exclusion=%t|immediate=%t|clustered=%t|valid=%t|check_xmin=%t|ready=%t|live=%t|replica_identity=%t|attributes=%d|key_attributes=%d|nulls_not_distinct=%t|key=%q|collations=%q|classes=%q|options=%q|method=%q|definition=%q",
			name, unique, primary, exclusion, immediate, clustered, valid, checkXmin,
			ready, live, replicaIdentity, attributes, keyAttributes, nullsNotDistinct,
			key, collations, classes, options, method, definition))
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return out, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT policyname, permissive, roles::text,
       cmd, COALESCE(qual, ''), COALESCE(with_check, '')
FROM pg_catalog.pg_policies
WHERE schemaname = $1 AND tablename = $2`, dialect.EngineSchema, table)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var name, permissive, roles, command, using, withCheck string
		if err := rows.Scan(&name, &permissive, &roles, &command, &using, &withCheck); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Policies = append(out.Policies, fmt.Sprintf(
			"name=%q|permissive=%q|roles=%q|command=%q|using=%q|with_check=%q",
			name, permissive, roles, command, using, withCheck))
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return out, err
	}

	// Rules can replace an INSERT/UPDATE/DELETE before any row trigger sees it.
	// Descriptor-rendered directory tables intentionally have no pg_rewrite
	// entries, but project the complete census in both directions so an
	// unexpected DO INSTEAD rule cannot masquerade as the same table contract.
	rows, err = tx.QueryContext(ctx, `SELECT r.rulename, r.ev_type::text,
	       r.ev_enabled::text, r.is_instead,
       pg_catalog.pg_get_ruledef(r.oid, false)
FROM pg_catalog.pg_rewrite r
JOIN pg_catalog.pg_class c ON c.oid = r.ev_class
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, table)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var name, eventType, enabled, definition string
		var instead bool
		if err := rows.Scan(&name, &eventType, &enabled, &instead, &definition); err != nil {
			_ = rows.Close()
			return out, err
		}
		name = normalizePostgresDirectoryObject(name, table, canonicalTable)
		definition = normalizePostgresDirectoryObject(definition, table, canonicalTable)
		out.Rules = append(out.Rules, fmt.Sprintf(
			"name=%q|event=%q|enabled=%q|instead=%t|definition=%q",
			name, eventType, enabled, instead, definition))
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return out, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT t.tgname,
       pg_catalog.pg_get_triggerdef(t.oid, false)
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2 AND NOT t.tgisinternal`,
		dialect.EngineSchema, table)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			_ = rows.Close()
			return out, err
		}
		name = normalizePostgresDirectoryObject(name, table, canonicalTable)
		definition = normalizePostgresDirectoryObject(definition, table, canonicalTable)
		// The descriptor renderer creates append-only triggers in origin state,
		// while the guard rollout later converges them to ALWAYS. That transition
		// is owned and attested by the guard runner. The directory contract still
		// compares the complete trigger identity and body, but deliberately does
		// not duplicate the runner's enable-state oracle.
		out.Triggers = append(out.Triggers,
			fmt.Sprintf("name=%q|definition=%q", name, definition))
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return out, err
	}

	sort.Strings(out.Constraints)
	sort.Strings(out.Indexes)
	sort.Strings(out.Policies)
	sort.Strings(out.Rules)
	sort.Strings(out.Triggers)
	return out, nil
}

// closeCoreDirectoryRows consumes both error channels database/sql exposes.
// Callers invoke it before any probe savepoint rollback, so no cursor or
// deferred row error crosses the subtransaction boundary.
func closeCoreDirectoryRows(rows *sql.Rows) error {
	closeErr := rows.Close()
	return errors.Join(closeErr, rows.Err())
}

func normalizePostgresDirectoryObject(value, table, canonicalTable string) string {
	if table == canonicalTable {
		return value
	}
	return strings.ReplaceAll(value, table, canonicalTable)
}

func postgresCoreDirectoryContractDifference(
	want postgresCoreDirectoryContract,
	got postgresCoreDirectoryContract,
) error {
	for _, part := range []struct {
		name string
		want any
		got  any
	}{
		{"relation form and RLS", want.Relation, got.Relation},
		{"columns", want.Columns, got.Columns},
		{"constraints and CHECKs", want.Constraints, got.Constraints},
		{"indexes and uniqueness", want.Indexes, got.Indexes},
		{"tenant policies", want.Policies, got.Policies},
		{"rewrite rules", want.Rules, got.Rules},
		{"triggers", want.Triggers, got.Triggers},
	} {
		if !reflect.DeepEqual(part.want, part.got) {
			return fmt.Errorf("PostgreSQL %s differ from descriptor-rendered probe: got=%v want=%v",
				part.name, part.got, part.want)
		}
	}
	return nil
}
