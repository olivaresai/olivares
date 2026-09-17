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
)

const directoryInventoryOwner = "olivares_directory_inventory_owner"
const directoryInventoryFunction = "olivares_directory_inventory_v1"
const postgresDirectoryInventoryBody = `
  SELECT inventory.object_kind, inventory.id, inventory.tenant_id, inventory.version
  FROM (
    SELECT 'org'::pg_catalog.text AS object_kind,
           o.id::pg_catalog.text AS id,
           o.tenant_id::pg_catalog.text AS tenant_id,
           NULL::pg_catalog.int8 AS version
    FROM public.orgs AS o
    UNION ALL
    SELECT 'directory_epoch'::pg_catalog.text,
           e.id::pg_catalog.text,
           e.tenant_id::pg_catalog.text,
           e.version::pg_catalog.int8
    FROM public.core_directory_epoch AS e
  ) AS inventory
  ORDER BY inventory.object_kind COLLATE pg_catalog."C",
           inventory.tenant_id COLLATE pg_catalog."C",
           inventory.id COLLATE pg_catalog."C"
`
const postgresDirectoryInventoryDDL = `CREATE FUNCTION public.olivares_directory_inventory_v1()
RETURNS TABLE(object_kind text, id text, tenant_id text, version bigint)
LANGUAGE sql STABLE SECURITY DEFINER PARALLEL RESTRICTED
SET search_path = pg_catalog
AS $inventory$` + postgresDirectoryInventoryBody + `$inventory$`

// verifyPostgresAuthorityFunction compares the compiled definition, full
// signature, pinned path, owner and explicit/effective EXECUTE. Superusers are
// the administrative trust boundary and inherently bypass every object ACL.
func verifyPostgresAuthorityFunction(ctx context.Context, q rowQuerier, name, owner, app string, inventory bool) (bool, error) {
	present, err := verifyPostgresAuthorityFunctionDefinition(ctx, q, name, owner, inventory)
	if err != nil || !present {
		return present, err
	}
	acl, err := readPostgresAuthorityFunctionACL(ctx, q, name, owner, app)
	if err != nil {
		return true, err
	}
	if !acl.closed() {
		return true, directoryUnavailable(fmt.Sprintf("authority function %s signature/owner/ACL is not closed", name), nil)
	}
	return true, nil
}

// Definition and ACL are separate only so the explicit logical-restore ceremony
// can recognize an exact compiled function whose ACL pg_restore stripped. Open
// continues to require both, without making any repair.
func verifyPostgresAuthorityFunctionDefinition(ctx context.Context, q rowQuerier, name, owner string, inventory bool) (bool, error) {
	form, present, err := projectGuardFunction(ctx, q, dialect.EngineSchema, name)
	if err != nil || !present {
		return present, err
	}
	retention := name == "olivares_retain_user_authority"
	want := guardFunctionForm{
		Schema: dialect.EngineSchema, Name: name, Kind: "f", ReturnTypeSchema: "pg_catalog", ReturnTypeName: "int8",
		Language: "plpgsql", NArgs: 1, ArgTypesCount: 1, Variadic: "0", AllArgTypesNull: true, ArgModesNull: true,
		ArgDefaultsNull: true, Src: postgresUserAuthorityLockBody, SecurityDefiner: true, Volatile: "v", Parallel: "u",
		Cost: 100, Support: "0", TransformsNull: true,
	}
	if inventory {
		want.ReturnTypeName, want.ReturnsSet, want.Language = "record", true, "sql"
		want.NArgs, want.ArgTypesCount, want.AllArgTypesNull, want.ArgModesNull = 0, 0, false, false
		want.Src, want.Volatile, want.Parallel, want.Rows = postgresDirectoryInventoryBody, "s", "r", 1000
	} else if retention {
		want.ReturnTypeName, want.NArgs, want.ArgTypesCount, want.SecurityDefiner = "trigger", 0, 0, false
		want.ArgNamesNull = true
		want.Src = strings.Split(postgresUserAuthorityRetentionDDL, "$retention$")[1]
	}
	if diff := guardFunctionDiff(want, form); len(diff) != 0 {
		return true, directoryUnavailable(fmt.Sprintf("authority function %s definition drift: %v", name, diff), nil)
	}
	var signature bool
	// The projection established the single expected overload.
	err = q.QueryRowContext(ctx, `SELECT
 p.proowner = o.oid AND p.proconfig = ARRAY['search_path=pg_catalog']::text[]
 AND CASE WHEN $3 THEN
   p.proargtypes::text = '' AND p.proallargtypes = ARRAY['text'::regtype,'text'::regtype,'text'::regtype,'int8'::regtype]::oid[]
   AND p.proargmodes = ARRAY['t','t','t','t']::"char"[]
   AND p.proargnames = ARRAY['object_kind','id','tenant_id','version']::text[]
 WHEN $4 THEN p.proargtypes::text = '' AND p.proargnames IS NULL
 ELSE p.proargtypes = ARRAY['text'::regtype]::oidvector
   AND p.proargnames = ARRAY['target_user']::text[] END
 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
 CROSS JOIN pg_catalog.pg_roles o
 WHERE n.nspname='public' AND p.proname=$1 AND o.rolname=$2`, name, owner, inventory, retention).Scan(&signature)
	if err != nil {
		return true, directoryUnavailable("attest authority function signature/ACL", err)
	}
	if !signature {
		return true, directoryUnavailable(fmt.Sprintf("authority function %s signature/owner/ACL is not closed", name), nil)
	}
	return true, nil
}

type postgresAuthorityFunctionACL struct {
	null, publicExec, badACL, badEffective, badGrantor bool
}

func (a postgresAuthorityFunctionACL) closed() bool {
	return !a.publicExec && !a.badACL && !a.badEffective
}

func readPostgresAuthorityFunctionACL(ctx context.Context, q rowQuerier, name, owner, app string) (postgresAuthorityFunctionACL, error) {
	var acl postgresAuthorityFunctionACL
	err := q.QueryRowContext(ctx, `SELECT p.proacl IS NULL,
 EXISTS (SELECT 1 FROM pg_catalog.aclexplode(COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner))) x WHERE x.grantee=0 AND x.privilege_type='EXECUTE'),
 EXISTS (SELECT 1 FROM pg_catalog.aclexplode(COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner))) x
         WHERE x.grantee NOT IN (o.oid,a.oid) OR x.privilege_type <> 'EXECUTE'
            OR (x.is_grantable AND x.grantee <> o.oid)),
 EXISTS (SELECT 1 FROM pg_catalog.pg_roles r WHERE NOT r.rolsuper AND
    pg_catalog.has_function_privilege(r.oid,p.oid,'EXECUTE') IS DISTINCT FROM (r.oid IN (o.oid,a.oid))),
 EXISTS (SELECT 1 FROM pg_catalog.aclexplode(p.proacl) x WHERE x.grantor <> o.oid)
 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
 CROSS JOIN pg_catalog.pg_roles o CROSS JOIN pg_catalog.pg_roles a
 WHERE n.nspname='public' AND p.proname=$1 AND o.rolname=$2 AND a.rolname=$3`, name, owner, app).
		Scan(&acl.null, &acl.publicExec, &acl.badACL, &acl.badEffective, &acl.badGrantor)
	if err != nil {
		return acl, directoryUnavailable("attest authority function signature/ACL", err)
	}
	return acl, nil
}

func userAuthorityOwnerRole(roles guardRoles) string {
	if roles.OwnerConfigured {
		return roles.Owner.bindable()
	}
	return roles.App.bindable()
}

func verifyPostgresUserAuthorityLock(ctx context.Context, q rowQuerier, roles guardRoles) error {
	owner, app := userAuthorityOwnerRole(roles), roles.App.bindable()
	if owner == "" || app == "" {
		return directoryUnavailable("User authority lock roles are unresolved", nil)
	}
	present, err := verifyPostgresAuthorityFunction(ctx, q, "olivares_lock_core_user_authority", owner, app, false)
	if err != nil {
		return err
	}
	if !present {
		return directoryUnavailable("User authority lock function is absent", nil)
	}
	return nil
}

// No read repairs: an existing routine with any drift is always a refusal.
// Absence is a separate fact, usable only for an explicitly incomplete staged boot.
func verifyPostgresDirectoryInventory(ctx context.Context, q directoryWriterACLQuerier, roles guardRoles) (bool, error) {
	owner, app := userAuthorityOwnerRole(roles), roles.App.bindable()
	if owner == "" || app == "" {
		return false, directoryUnavailable("directory inventory roles are unresolved", nil)
	}
	present, err := verifyPostgresAuthorityFunction(ctx, q, directoryInventoryFunction, directoryInventoryOwner, app, true)
	if err != nil || !present {
		return present, err
	}
	var posture bool
	err = q.QueryRowContext(ctx, `SELECT NOT rolcanlogin AND NOT rolinherit AND NOT rolsuper AND rolbypassrls
 AND NOT rolcreaterole AND NOT rolcreatedb AND NOT rolreplication
 FROM pg_catalog.pg_roles WHERE rolname=$1`, directoryInventoryOwner).Scan(&posture)
	if err != nil || !posture {
		return true, directoryUnavailable("directory inventory owner attributes are not closed", err)
	}
	major, err := postgresMajorVia(ctx, q)
	if err != nil {
		return true, err
	}
	for _, from := range []string{app, owner, directoryInventoryOwner} {
		if major < 16 {
			create, err := guardRoleHasCreateRole(ctx, q, from)
			if err != nil {
				return true, err
			}
			if create {
				return true, directoryUnavailable("inventory role isolation defeated by CREATEROLE", nil)
			}
		}
		query := guardReachableCTE(major) + `SELECT r.rolname FROM pg_catalog.pg_roles r WHERE $1::text='public' AND
 (r.rolname=$2 OR ` + guardRoleReachability(major) + `) ORDER BY r.rolname`
		rows, err := q.QueryContext(ctx, query, dialect.EngineSchema, from)
		if err != nil {
			return true, directoryUnavailable("inventory effective role closure", err)
		}
		var reachable []string
		for rows.Next() {
			var role string
			if err := rows.Scan(&role); err != nil {
				_ = rows.Close()
				return true, err
			}
			reachable = append(reachable, role)
		}
		if err := closeCoreDirectoryRows(rows); err != nil {
			return true, err
		}
		for _, role := range reachable {
			if from != directoryInventoryOwner && role == directoryInventoryOwner || from == directoryInventoryOwner && (role == app || role == owner) {
				return true, directoryUnavailable("inventory owner has a live role path to/from app or schema owner", nil)
			}
			if from == directoryInventoryOwner {
				if err := verifyInventoryRolePrivileges(ctx, q, role, role == directoryInventoryOwner); err != nil {
					return true, err
				}
			}
		}
	}
	return true, nil
}

func verifyInventoryRolePrivileges(ctx context.Context, q rowQuerier, role string, root bool) error {
	var dangerous bool
	err := q.QueryRowContext(ctx, `SELECT rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR (rolbypassrls AND NOT $2)
 FROM pg_catalog.pg_roles WHERE rolname=$1`, role, root).Scan(&dangerous)
	if err != nil || dangerous {
		return directoryUnavailable("inventory reachable role has administrative attributes", err)
	}
	// has_column_privilege includes table-wide, PUBLIC and inherited grants.
	// Only five declared columns are readable; table-level SELECT is forbidden.
	rows, err := q.QueryContext(ctx, `SELECT n.nspname,c.relname,a.attname,c.relowner=r.oid,
 pg_catalog.has_table_privilege(r.oid,c.oid,'SELECT'),
 pg_catalog.has_table_privilege(r.oid,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'),
 pg_catalog.has_column_privilege(r.oid,c.oid,a.attnum,'SELECT'),
 pg_catalog.has_column_privilege(r.oid,c.oid,a.attnum,'INSERT,UPDATE,REFERENCES')
 FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
 WHERE r.rolname=$1 AND c.relkind IN ('r','p','v','m','f')
 AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%'
 ORDER BY n.nspname,c.relname,a.attnum`, role)
	if err != nil {
		return directoryUnavailable("inventory effective relation privileges", err)
	}
	var columns int
	for rows.Next() {
		var schema, table, column string
		var owns, tableSelect, dml, read, write bool
		if err := rows.Scan(&schema, &table, &column, &owns, &tableSelect, &dml, &read, &write); err != nil {
			_ = rows.Close()
			return err
		}
		allowed := schema == "public" && (table == "orgs" && (column == "id" || column == "tenant_id") || table == "core_directory_epoch" && (column == "id" || column == "tenant_id" || column == "version"))
		if allowed {
			columns++
		}
		if owns || tableSelect || dml || write || read && !allowed || root && allowed && !read {
			_ = rows.Close()
			return directoryUnavailable(fmt.Sprintf("inventory role %s has invalid effective privileges on %s.%s.%s: owns=%t table_select=%t dml=%t column_select=%t column_write=%t required=%t", role, schema, table, column, owns, tableSelect, dml, read, write, root && allowed), nil)
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return err
	}
	if columns != 5 {
		return directoryUnavailable("inventory declared columns are unresolved", nil)
	}
	var schemaBad, funcBad, sequenceBad bool
	err = q.QueryRowContext(ctx, `SELECT
 EXISTS (SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%'
  AND (n.nspowner=r.oid OR pg_catalog.has_schema_privilege(r.oid,n.oid,'CREATE') OR
       (n.nspname<>'public' AND pg_catalog.has_schema_privilege(r.oid,n.oid,'USAGE')))),
 NOT pg_catalog.has_schema_privilege(r.oid,'public','USAGE') OR EXISTS (
  SELECT 1 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
  WHERE n.nspname NOT IN ('pg_catalog','information_schema')
  AND NOT (n.nspname='public' AND p.proname='olivares_directory_inventory_v1' AND p.pronargs=0)
  AND (p.proowner=r.oid OR p.prosecdef AND pg_catalog.has_function_privilege(r.oid,p.oid,'EXECUTE'))),
 EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE c.relkind='S' AND n.nspname NOT IN ('pg_catalog','information_schema') AND
    (c.relowner=r.oid OR pg_catalog.has_sequence_privilege(r.oid,c.oid,'USAGE,SELECT,UPDATE')))
 FROM pg_catalog.pg_roles r WHERE r.rolname=$1`, role).Scan(&schemaBad, &funcBad, &sequenceBad)
	if err != nil || schemaBad || funcBad || sequenceBad {
		return directoryUnavailable("inventory role has schema, routine or sequence authority outside its closed posture", err)
	}
	return nil
}

// installDirectoryInventoryTx is called only with an explicitly supplied DBA
// credential after product tables exist. Repeated installation verifies an
// existing object; it never overwrites drift or rewrites role memberships.
func installDirectoryInventoryTx(ctx context.Context, tx *sql.Tx, roles guardRoles) error {
	app, owner := roles.App.bindable(), userAuthorityOwnerRole(roles)
	if app == "" || owner == "" || app == directoryInventoryOwner || owner == directoryInventoryOwner {
		return directoryUnavailable("invalid inventory installation roles", nil)
	}
	var present bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)", directoryInventoryOwner).Scan(&present); err != nil {
		return err
	}
	if !present {
		if _, err := tx.ExecContext(ctx, "CREATE ROLE "+directoryInventoryOwner+" NOLOGIN NOINHERIT NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION"); err != nil {
			return err
		}
	}
	if _, present, err := projectGuardFunction(ctx, tx, dialect.EngineSchema, directoryInventoryFunction); err != nil {
		return err
	} else if present {
		_, err := verifyPostgresDirectoryInventory(ctx, tx, roles)
		return err
	}
	for _, stmt := range []string{
		"GRANT USAGE ON SCHEMA public TO " + directoryInventoryOwner,
		"GRANT SELECT(id,tenant_id) ON public.orgs TO " + directoryInventoryOwner,
		"GRANT SELECT(id,tenant_id,version) ON public.core_directory_epoch TO " + directoryInventoryOwner,
		postgresDirectoryInventoryDDL,
		"ALTER FUNCTION public.olivares_directory_inventory_v1() OWNER TO " + directoryInventoryOwner,
		"REVOKE ALL ON FUNCTION public.olivares_directory_inventory_v1() FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION public.olivares_directory_inventory_v1() TO " + quoteIdent(app),
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	_, err := verifyPostgresDirectoryInventory(ctx, tx, roles)
	return err
}
