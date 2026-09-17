// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// verifyDRRestoreControl is the closed, read-only interface for every existing
// control consumer. Catalog error, absent family, malformed family and valid row
// remain distinct. No query here creates a probe or repairs a deployed object.
func verifyDRRestoreControl(ctx context.Context, q rowQuerier, dest drDestination, roles restoreControlRoles) drGate {
	oid, owner, err := verifyDRControlFamily(ctx, q, roles)
	if err != nil {
		verdict := drGateUnreadable
		if errors.Is(err, errDRControlShape) {
			verdict = drGateMalformed
		}
		return drGate{Verdict: verdict, Owner: owner, Cause: err}
	}
	if oid == 0 {
		return drGate{Verdict: drGateAbsent}
	}
	g := readDRRestoreControlRow(ctx, q)
	g.Owner = owner
	if g.Verdict == drGateUnreadable || g.Verdict == drGateMalformed {
		return g
	}
	expected := opgate.PostgresDestination{Database: dest.Database, Schema: dest.Schema, SystemIdentifier: dest.SystemIdentifier}
	actual := opgate.PostgresDestination{Database: g.Database, Schema: g.Schema, SystemIdentifier: g.SystemIdentifier}
	if !dest.SystemIdentifierKnown || expected.Validate() != nil || expected != actual {
		return drGate{Verdict: drGateMalformed, Owner: owner, Cause: fmt.Errorf("%w: control destination does not match the measured PostgreSQL destination", errDRControlShape)}
	}
	return g
}

func drShape(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errDRControlShape, fmt.Sprintf(format, args...))
}

// These projections extend the structural catalog mechanisms in guardshape and
// directorymigration_contract. The oracle is the compiled major-specific dialect
// contract, never a second relation calibrated on the target.
func verifyDRControlFamily(ctx context.Context, q rowQuerier, roles restoreControlRoles) (int64, string, error) {
	var oid, ownerOID int64
	var owner string
	var major int
	var good bool
	err := q.QueryRowContext(ctx, `SELECT c.oid::pg_catalog.int8,c.relowner::pg_catalog.int8,r.rolname,
 pg_catalog.current_setting('server_version_num')::pg_catalog.int4 / 10000
 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_catalog.pg_roles r ON r.oid=c.relowner
 WHERE n.nspname=$1 AND c.relname=$2`, dialect.EngineSchema, dialect.DRRestoreControlTable).Scan(&oid, &ownerOID, &owner, &major)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, owner, err
	}
	contract, err := dialect.DRRestoreControlContract(major)
	if err != nil {
		return oid, owner, drShape("%v", err)
	}
	var observedOID, observedOwnerOID int64
	var observedOwner string
	var observedMajor int
	err = q.QueryRowContext(ctx, contract.RelationSQL, dialect.EngineSchema, dialect.DRRestoreControlTable).Scan(&observedOID, &observedOwnerOID, &observedOwner, &observedMajor, &good)
	if err != nil {
		return oid, owner, err
	}
	if oid != observedOID || ownerOID != observedOwnerOID || owner != observedOwner || major != observedMajor {
		return oid, owner, drShape("relation or owner changed during verification")
	}
	if !good {
		return oid, owner, drShape("relation identity, persistence, inheritance or subordinate family differs")
	}
	if roles.owner == "" || roles.app == "" || roles.ownerOID == 0 || roles.appOID == 0 {
		return oid, owner, drShape("measured control roles are incomplete (observed owner %q)", owner)
	}
	if owner != roles.owner || ownerOID != roles.ownerOID {
		return oid, owner, drShape("observed owner %q (OID %d), expected measured DDL authority %q (OID %d)", owner, ownerOID, roles.owner, roles.ownerOID)
	}
	if err := verifyDRControlColumns(ctx, q, oid, contract); err != nil {
		return oid, owner, err
	}
	if err := verifyDRControlConstraints(ctx, q, oid, contract); err != nil {
		return oid, owner, err
	}
	if err := verifyDRControlIndex(ctx, q, oid, ownerOID, contract); err != nil {
		return oid, owner, err
	}
	if err := verifyDRControlSubordinates(ctx, q, oid, contract); err != nil {
		return oid, owner, err
	}
	if err := verifyDRControlACL(ctx, q, oid, roles, major); err != nil {
		return oid, owner, err
	}
	return oid, owner, nil
}
func verifyDRControlColumns(ctx context.Context, q rowQuerier, oid int64, c dialect.DRControlContract) error {
	rows, err := q.QueryContext(ctx, c.ColumnSQL, oid)
	if err != nil {
		return err
	}
	i := 0
	for rows.Next() {
		var num int
		var name string
		var typ, ns sql.NullString
		var notnull bool
		var good sql.NullBool
		if err := rows.Scan(&num, &name, &typ, &ns, &notnull, &good); err != nil {
			_ = rows.Close()
			return err
		}
		if i >= len(c.Columns) {
			_ = rows.Close()
			return drShape("unexpected attribute %d", num)
		}
		w := c.Columns[i]
		i++
		if num != w.Position || name != w.Name || typ.String != w.Type || ns.String != "pg_catalog" || notnull != w.NotNull || !good.Valid || !good.Bool {
			_ = rows.Close()
			return drShape("attribute %d %q has unexpected type identity, position, default, collation or flags", num, name)
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return err
	}
	if i != len(c.Columns) {
		return drShape("expected %d attributes, observed %d", len(c.Columns), i)
	}
	return nil
}
func verifyDRControlConstraints(ctx context.Context, q rowQuerier, oid int64, c dialect.DRControlContract) error {
	rows, err := q.QueryContext(ctx, c.ConstraintSQL, oid, dialect.DRRestoreControlTable+"_pkey")
	if err != nil {
		return err
	}
	want := map[string]dialect.DRControlCheck{}
	for _, check := range c.Checks {
		want[dialect.DRRestoreControlTable+"_"+check.Suffix] = check
	}
	want[dialect.DRRestoreControlTable+"_pkey"] = dialect.DRControlCheck{Suffix: "pkey", Definition: "PRIMARY KEY (control_key)", Columns: "1"}
	for rows.Next() {
		var name, typ, def, cols string
		var good bool
		if err := rows.Scan(&name, &typ, &def, &cols, &good); err != nil {
			_ = rows.Close()
			return err
		}
		w, ok := want[name]
		delete(want, name)
		expectedType := "c"
		if w.Suffix == "pkey" {
			expectedType = "p"
		}
		if !ok || typ != expectedType || !good || def != w.Definition || cols != w.Columns {
			_ = rows.Close()
			return drShape("constraint %q differs: type=%s flags=%t columns=%q definition=%q", name, typ, good, cols, def)
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return err
	}
	if len(want) != 0 {
		return drShape("missing constraints: %v", drCheckNames(want))
	}
	return nil
}
func verifyDRControlIndex(ctx context.Context, q rowQuerier, oid, ownerOID int64, c dialect.DRControlContract) error {
	rows, err := q.QueryContext(ctx, c.IndexSQL, oid, ownerOID)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var name string
		var good bool
		if err := rows.Scan(&name, &good); err != nil {
			_ = rows.Close()
			return err
		}
		count++
		if count != 1 || name != dialect.DRRestoreControlTable+"_pkey" || !good {
			_ = rows.Close()
			return drShape("primary index %q has unexpected identity or flags", name)
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return err
	}
	if count != 1 {
		return drShape("expected exactly one primary index, observed %d", count)
	}
	// Owned sequences and external constraints are outside the declared family.
	var extra bool
	err = q.QueryRowContext(ctx, c.DependencySQL, oid).Scan(&extra)
	if err != nil {
		return err
	}
	if extra {
		return drShape("undeclared sequence or referencing constraint")
	}
	return nil
}

func verifyDRControlACL(ctx context.Context, q rowQuerier, oid int64, roles restoreControlRoles, major int) error {
	allowed := map[int64]string{roles.ownerOID: roles.owner, roles.appOID: roles.app}
	if roles.admin != "" {
		if roles.adminOID == 0 {
			return drShape("admin identity is unresolved")
		}
		allowed[roles.adminOID] = roles.admin
	}
	if roles.owner == directoryInventoryOwner || roles.app == directoryInventoryOwner || roles.admin == directoryInventoryOwner {
		return drShape("directory inventory cannot be a control role")
	}
	// Explicit ACLs have exactly owner ALL (without grant-option bits) and each
	// distinct app/admin SELECT. Ownership itself conveys grant authority even
	// when those bits are false; single-role is deliberately not owner isolation.
	rows, err := q.QueryContext(ctx, `SELECT a.grantor::pg_catalog.int8,a.grantee::pg_catalog.int8,a.privilege_type,a.is_grantable
 FROM pg_catalog.pg_class c CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl,pg_catalog.acldefault('r',c.relowner))) a
 WHERE c.oid=$1 ORDER BY a.grantee,a.privilege_type`, oid)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var grantor, grantee int64
		var privilege string
		var grantable bool
		if err := rows.Scan(&grantor, &grantee, &privilege, &grantable); err != nil {
			_ = rows.Close()
			return err
		}
		_, ok := allowed[grantee]
		key := fmt.Sprintf("%d/%s", grantee, privilege)
		if !ok || grantee == 0 || grantor != roles.ownerOID || grantable || seen[key] || (grantee != roles.ownerOID && privilege != "SELECT") {
			_ = rows.Close()
			return drShape("explicit ACL is not closed: grantee=%d privilege=%s grantable=%t", grantee, privilege, grantable)
		}
		seen[key] = true
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return err
	}
	for id := range allowed {
		privs := []string{"SELECT"}
		if id == roles.ownerOID {
			privs = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}
		}
		for _, priv := range privs {
			key := fmt.Sprintf("%d/%s", id, priv)
			if !seen[key] {
				return drShape("missing explicit ACL %s", key)
			}
			delete(seen, key)
		}
	}
	if len(seen) != 0 {
		return drShape("extra explicit control privileges")
	}
	var columnACL bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_attribute WHERE attrelid=$1 AND attacl IS NOT NULL AND pg_catalog.cardinality(attacl)>0)`, oid).Scan(&columnACL); err != nil {
		return err
	}
	if columnACL {
		return drShape("unexpected explicit column ACL")
	}
	// Audit all user roles, including NOLOGIN inventory and intermediate groups.
	// Unassigned predefined global administration roles and superusers have inherent
	// server capabilities; they are not object grants. Any user role reaching one
	// still goes through the effective-privilege and membership checks below.
	rows, err = q.QueryContext(ctx, `SELECT oid::pg_catalog.int8,rolname FROM pg_catalog.pg_roles WHERE (NOT rolsuper AND oid>=16384) OR oid=$1 OR oid=$2 OR oid=$3 OR rolname=$4 ORDER BY oid`, roles.ownerOID, roles.appOID, roles.adminOID, directoryInventoryOwner)
	if err != nil {
		return err
	}
	type role struct {
		id   int64
		name string
	}
	var roots []role
	for rows.Next() {
		var r role
		if err := rows.Scan(&r.id, &r.name); err != nil {
			_ = rows.Close()
			return err
		}
		roots = append(roots, r)
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return err
	}
	for _, root := range roots {
		// Reuse the same SET/INHERIT/ADMIN transitive closure as the guard and
		// directory inventory. An inert membership edge cannot create a refusal.
		query := guardReachableCTE(major) + `SELECT r.oid::pg_catalog.int8,r.rolname,r.rolsuper,
   pg_catalog.has_table_privilege(r.oid,$1::pg_catalog.oid,'SELECT'),
   pg_catalog.has_table_privilege(r.oid,$1::pg_catalog.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'),
   pg_catalog.has_table_privilege(r.oid,$1::pg_catalog.oid,'SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION,UPDATE WITH GRANT OPTION,DELETE WITH GRANT OPTION,TRUNCATE WITH GRANT OPTION,REFERENCES WITH GRANT OPTION,TRIGGER WITH GRANT OPTION'),
   EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped AND pg_catalog.has_column_privilege(r.oid,$1::pg_catalog.oid,a.attnum,'SELECT')),
   EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped AND pg_catalog.has_column_privilege(r.oid,$1::pg_catalog.oid,a.attnum,'INSERT,UPDATE,REFERENCES,SELECT WITH GRANT OPTION,INSERT WITH GRANT OPTION,UPDATE WITH GRANT OPTION,REFERENCES WITH GRANT OPTION'))
   FROM pg_catalog.pg_roles r WHERE r.rolname=$2 OR ` + guardRoleReachability(major)
		effective, err := q.QueryContext(ctx, query, oid, root.name)
		if err != nil {
			return err
		}
		_, canRead := allowed[root.id]
		count := 0
		for effective.Next() {
			var id int64
			var name string
			var super, read, write, grant, colRead, colWrite bool
			if err := effective.Scan(&id, &name, &super, &read, &write, &grant, &colRead, &colWrite); err != nil {
				_ = effective.Close()
				return err
			}
			count++
			if root.id == roles.ownerOID {
				continue
			}
			if super || id == roles.ownerOID || write || grant || colWrite || (!canRead && (read || colRead)) {
				_ = effective.Close()
				return drShape("effective table/column or role-membership privilege for %q via %q is outside the control ACL", root.name, name)
			}
			if id == root.id && canRead && (!read || !colRead) {
				_ = effective.Close()
				return drShape("required effective SELECT is absent for %q", root.name)
			}
		}
		if err := closeCoreDirectoryRows(effective); err != nil {
			return err
		}
		if count == 0 {
			return drShape("role %q disappeared during privilege verification", root.name)
		}
	}
	return nil
}

// Resolve actual current-role names/OIDs through bounded, transient pools. These
// facts describe roles only. Retained endpoint agreement is reserved to IR-6.
func resolveDRControlRoles(ctx context.Context, cfg store.Config) (restoreControlRoles, error) {
	var out restoreControlRoles
	ctx, cancel := context.WithTimeout(ctx, drCoordinationTimeout)
	defer cancel()
	resolve := func(dsn string) (string, int64, error) {
		pool, err := openPGPinnedToEngineSchema(dsn, 1)
		if err != nil {
			return "", 0, err
		}
		defer pool.Close() //nolint:errcheck // transient reader
		return readDRControlRole(ctx, pool)
	}
	var err error
	out.app, out.appOID, err = resolve(cfg.DSN)
	if err != nil {
		return out, err
	}
	owner := strings.TrimSpace(cfg.OwnerDSN)
	if owner == "" || owner == strings.TrimSpace(cfg.DSN) {
		out.owner, out.ownerOID = out.app, out.appOID
	} else {
		out.owner, out.ownerOID, err = resolve(owner)
		if err != nil {
			return out, err
		}
	}
	if admin := strings.TrimSpace(cfg.AdminDSN); admin != "" {
		out.admin, out.adminOID, err = resolve(admin)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// A startup SET ROLE must not hide the session authority that RESET ROLE can
// recover. Retained endpoint/session agreement remains the separate IR-6 work.
//
// R63 C2 — the diagnostic NAMES THE TWO POSTGRESQL FUNCTIONS IT COMPARED, and that
// is the whole change. The refusal itself is unchanged: it is taken here, before any
// mutation, and it stays the earliest one on this path. What was wrong was only the
// wording. "configured current role" and "session authority" are this codebase's
// terms; an operator holding the error has to map them onto `current_user` and
// `session_user` to act on it, and the directory-activation tests — which assert on a
// real assumed-role DSN, not a string — could not recognise the refusal they were
// written for. Both names are now in the message, and the established
// "session authority" wording is kept so the DR verifier's own assertion still reads
// the same category. Nothing about authorization moved.
func readDRControlRole(ctx context.Context, q rowQuerier) (string, int64, error) {
	var name, session string
	var oid int64
	err := q.QueryRowContext(ctx, `SELECT r.rolname,r.oid::pg_catalog.int8,SESSION_USER FROM pg_catalog.pg_roles r WHERE r.rolname=CURRENT_USER`).Scan(&name, &oid, &session)
	if err == nil && name != session {
		err = drShape(
			"configured current role (current_user) %q differs from session authority (session_user) %q",
			name, session,
		)
	}
	return name, oid, err
}

func drCheckNames(m map[string]dialect.DRControlCheck) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// The automatically created row/array types and TOAST pair are part of the
// table's declared family too. Their names depend only on the verified parent
// identity; they cannot bring an extra callable/readable subordinate with them.
func verifyDRControlSubordinates(ctx context.Context, q rowQuerier, oid int64, c dialect.DRControlContract) error {
	var good bool
	err := q.QueryRowContext(ctx, c.SubordinateSQL, oid).Scan(&good)
	if err != nil {
		return err
	}
	if !good {
		return drShape("row/array types, TOAST, comments or dependent family identity differs")
	}
	return nil
}
