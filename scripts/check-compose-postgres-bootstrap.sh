#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Static, network-free, Docker-free contract for the FRESH Postgres Compose bootstrap.
#
# ⛔ WHY IT EXISTS, with the measurement that put it here (2026-09-11). The Postgres
# Compose sample provisioned ONLY olivares_app and started the engine with no
# --admin-dsn. Measured on an owned PostgreSQL 16.15 cluster running the exact shipped
# SQL and the real binary: `GET /readyz` answered 503 setup_blocked and `POST /v1/setup`
# answered 501 cross_tenant_admin_pool_not_configured. The sample was a topology that
# COULD NEVER BE SET UP, and nothing in the tree said so — the two files that had to
# agree (a compose command line and a .sql role file) are read by no compiler and no
# test. That is the class this leg covers.
#
# It asserts the four things that must stay true together, and NOTHING about Docker:
#   1. the sample wires a SECOND, DISTINCT pool (--admin-dsn) with its own credential;
#   2. the administrative role is NOSUPERUSER BYPASSRLS and READ-ONLY, with no
#      membership with the application role in either direction;
#   3. the application role keeps NOSUPERUSER NOBYPASSRLS — the FORCE-RLS backstop;
#   4. the bootstrap SQL never DROPs or ALTERs an existing principal, and its grants
#      are the SAME ONES THE PRODUCT ITSELF RENDERS (`RenderProvisionSQL` in
#      core/internal/store/sqlstore/dbsetup.go). Cross-checking the .sql against the Go
#      renderer is the point: a hand-written bootstrap that drifts from the binary's
#      own provisioning is how an operator ends up with a role the engine refuses.
#
# Verdicts: 0 = contract holds · 1 = violation (named) · 2 = NO HE PODIDO MIRAR.
set -euo pipefail

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
for tool in grep python3; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'compose-postgres-bootstrap: NO HE PODIDO MIRAR — missing %s\n' "$tool" >&2
		exit 2
	}
done

for path in \
	deploy/compose/docker-compose.postgres.yml \
	deploy/compose/initdb/10-app-role.sh \
	deploy/compose/README.md \
	deploy/postgres/01-app-role.sql \
	deploy/postgres/02-admin-role.sql \
	core/internal/store/sqlstore/dbsetup.go \
	Taskfile.yml .githooks/pre-push .github/workflows/mainline-ci.yml \
	scripts/check-compose-postgres-bootstrap.sh scripts/test-compose-postgres-bootstrap.sh; do
	[[ -f "$root/$path" ]] || {
		printf 'compose-postgres-bootstrap: NO HE PODIDO MIRAR — missing %s\n' "$path" >&2
		exit 2
	}
done
bash -n "$root/scripts/check-compose-postgres-bootstrap.sh"
bash -n "$root/scripts/test-compose-postgres-bootstrap.sh"

python3 - "$root" <<'PY'
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
read = lambda p: (root / p).read_text(encoding="utf-8")

override = read("deploy/compose/docker-compose.postgres.yml")
hook = read("deploy/compose/initdb/10-app-role.sh")
readme = read("deploy/compose/README.md")
app_sql = read("deploy/postgres/01-app-role.sql")
admin_sql = read("deploy/postgres/02-admin-role.sql")
dbsetup = read("core/internal/store/sqlstore/dbsetup.go")
taskfile = read("Taskfile.yml")
prepush = read(".githooks/pre-push")
mainline = read(".github/workflows/mainline-ci.yml")

fail = []


def need(cond, msg):
    if not cond:
        fail.append(msg)


# Statements only: the SQL file explains itself at length, and a bare substring search
# would happily find "DROP ROLE" inside the paragraph that promises never to run one.
def statements(sql):
    out = []
    for line in sql.splitlines():
        stripped = line.lstrip()
        # psql meta-commands (\connect, \if, \else, \endif) are not SQL statements and
        # must not be glued onto the statement that follows them: without this the
        # grant right after `\connect olivares` never matches the renderer's text.
        if stripped.startswith("--") or stripped.startswith("\\"):
            continue
        line = line.split("--", 1)[0].strip()
        if line:
            out.append(line)
    return "\n".join(out)


admin_stmts = statements(admin_sql)
app_stmts = statements(app_sql)

# ---------------------------------------------------------------- 1 · the sample wires
# a second pool, with a credential of its own.
need("--admin-dsn=postgres://olivares_admin:${OLIVARES_ADMIN_PASSWORD:?" in override,
     "the Postgres override does not pass --admin-dsn for olivares_admin: a fresh install "
     "cannot complete POST /v1/setup (501 cross_tenant_admin_pool_not_configured)")
need("--dsn=postgres://olivares_app:${OLIVARES_DB_PASSWORD:?" in override,
     "the override's --dsn no longer names olivares_app with OLIVARES_DB_PASSWORD")
need("OLIVARES_ADMIN_PASSWORD: ${OLIVARES_ADMIN_PASSWORD:?" in override,
     "the postgres service does not receive OLIVARES_ADMIN_PASSWORD, so the initdb hook "
     "cannot set the administrative role's password")
# Distinct credentials. Three principals, three variables, no reuse.
need("--admin-dsn=postgres://olivares_admin:${OLIVARES_DB_PASSWORD" not in override
     and "--admin-dsn=postgres://olivares_admin:${POSTGRES_SUPERUSER_PASSWORD" not in override,
     "the administrative DSN reuses the application or superuser credential; the three "
     "principals must have three distinct passwords")
need("--dsn=postgres://olivares_app:${POSTGRES_SUPERUSER_PASSWORD" not in override,
     "the application DSN reuses the superuser credential")
need("postgres://postgres:" not in override,
     "the override points an engine pool at the postgres superuser")
need("--allow-privileged-db-role" not in override,
     "the override passes --allow-privileged-db-role: the bootstrap must produce roles the "
     "engine accepts on their merits, never an override of its guard")
need("../postgres/02-admin-role.sql:/sql/02-admin-role.sql:ro" in override,
     "the override does not mount deploy/postgres/02-admin-role.sql read-only at /sql")
need("../postgres/01-app-role.sql:/sql/01-app-role.sql:ro" in override,
     "the override no longer mounts deploy/postgres/01-app-role.sql read-only at /sql")

# ---------------------------------------------------------------- 2 · the initdb hook
# runs BOTH canonical files, app role first, and refuses without either credential.
need('"${OLIVARES_DB_PASSWORD:?' in hook,
     "the initdb hook no longer requires OLIVARES_DB_PASSWORD")
need('"${OLIVARES_ADMIN_PASSWORD:?' in hook,
     "the initdb hook does not require OLIVARES_ADMIN_PASSWORD, so a stack could initialise "
     "with no administrative role and no complaint")
i_app = hook.find("01-app-role.sql -v app_password")
i_admin = hook.find("02-admin-role.sql -v admin_password")
need(i_app != -1, "the initdb hook does not run 01-app-role.sql with -v app_password")
need(i_admin != -1, "the initdb hook does not run 02-admin-role.sql with -v admin_password")
need(i_app != -1 and i_admin != -1 and i_app < i_admin,
     "the initdb hook runs the administrative role file before the application role file; "
     "02-admin-role.sql depends on the app role and the database existing")
need('${OLIVARES_INITDB_SQL_DIR:-/sql}' in hook,
     "the initdb hook's SQL directory no longer defaults to the container's /sql mount")

# ---------------------------------------------------------------- 3 · role authority.
need(re.search(r"CREATE ROLE olivares_admin LOGIN PASSWORD %L NOSUPERUSER BYPASSRLS "
               r"NOCREATEROLE NOCREATEDB NOREPLICATION", admin_stmts),
     "02-admin-role.sql does not create olivares_admin as NOSUPERUSER BYPASSRLS "
     "NOCREATEROLE NOCREATEDB NOREPLICATION")
need("NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION" in app_stmts
     and "CREATE ROLE olivares_app LOGIN PASSWORD" in app_stmts,
     "01-app-role.sql no longer creates olivares_app as NOSUPERUSER NOBYPASSRLS: the "
     "FORCE-RLS tenant backstop is inert on a privileged application role")
need("DROP ROLE" not in admin_stmts,
     "02-admin-role.sql executes DROP ROLE: a bootstrap must not be able to delete a "
     "principal that may already own grants, sessions or an audit trail")
need("ALTER ROLE" not in admin_stmts,
     "02-admin-role.sql executes ALTER ROLE: an existing principal's authority is verified "
     "and refused, never silently changed")
need(not re.search(r"\bGRANT\s+olivares_admin\s+TO\b", admin_stmts, re.I)
     and not re.search(r"\bGRANT\s+olivares_app\s+TO\b", admin_stmts, re.I),
     "02-admin-role.sql grants membership between the application and administrative "
     "roles; membership lets the app role SET ROLE into BYPASSRLS and defeats FORCE RLS")
need(not re.search(r"GRANT[^;]*\b(INSERT|UPDATE|DELETE|TRUNCATE|CREATE)\b[^;]*TO olivares_admin",
                   admin_stmts, re.I | re.S),
     "02-admin-role.sql grants a WRITE privilege to olivares_admin; the administrative "
     "pool is read-only")
need("--allow-privileged-db-role" not in admin_stmts,
     "02-admin-role.sql reaches for --allow-privileged-db-role")
# The refusals are the file's substance, so their absence is a violation, not a style note.
for needle, what in (
    ("rolcanlogin AND rolbypassrls", "the pre-existing-role posture check"),
    ("pg_catalog.pg_has_role('olivares_app', 'olivares_admin', 'MEMBER')", "the app->admin membership refusal"),
    ("pg_catalog.pg_has_role('olivares_admin', 'olivares_app', 'MEMBER')", "the admin->app membership refusal"),
    ("rolsuper OR rolbypassrls", "the application-role posture precondition"),
):
    need(needle in admin_sql,
         f"02-admin-role.sql lost {what}; a bootstrap that cannot refuse is not a guard")

# ⛔ THE READ-ONLY CLAIM HAS TO BE EARNED, and until 2026-09-11 it was not. Measured on an
# owned PostgreSQL 16.15 cluster: a pre-existing olivares_admin that ALREADY held a write
# on an app table kept it — GRANT adds, it never takes away — and the file still exited 0
# with its read-only NOTICE. Seven shapes produced that false success: a direct grant, a
# grant to PUBLIC, a grant to a role the admin belongs to, and the same again as FUTURE
# default privileges, including one set globally (no IN SCHEMA), which a check keyed on
# schema public cannot see at all. Each needle below names one of those MEASURED holes;
# how the file closes it is its own business.
for needle, what in (
    ("acl.privilege_type <> 'SELECT'",
     "any judgement of privileges other than SELECT"),
    ("acl.grantee = 0",
     "the PUBLIC grantee: a grant to PUBLIC reaches the administrative role too"),
    ("pg_catalog.pg_has_role('olivares_admin', acl.grantee, 'MEMBER')",
     "reachability through role membership: a grant to a group the role belongs to"),
    ("d.defaclnamespace = 0",
     "ALTER DEFAULT PRIVILEGES set globally, without IN SCHEMA"),
    # correction-02, each measured on PG 16.15 as a write the ACL-only guard let through:
    ("pg_write_all_data",
     "membership of pg_write_all_data, which confers DML on every table and leaves no "
     "privilege entry on any of them"),
    ("a.attacl",
     "COLUMN-level privileges, which live in pg_attribute.attacl and report FALSE from a "
     "table-level has_table_privilege"),
    ("pg_catalog.pg_has_role('olivares_admin', c.relowner, 'MEMBER')",
     "ownership of a relation, which is standing authority to re-grant what the owner "
     "revoked"),
    # correction-03: has_schema_privilege answers only for privileges a role INHERITS, so a
    # membership granted WITH INHERIT FALSE, SET TRUE is one SET ROLE away from CREATE TABLE
    # and the admin-only form returns false. Measured on PG 16.15.
    ("pg_catalog.has_schema_privilege(r.oid, 'public', 'CREATE')",
     "CREATE on schema public held by any role the admin can SET ROLE into, rather than by "
     "the admin alone"),
):
    need(needle in admin_sql,
         f"02-admin-role.sql does not cover {what}, so it can report a read-only pool "
         "that is able to write")

# And the refusal must come BEFORE the first grant in that scope. A guard that runs after
# them reports on an estate it has already changed.
i_writecheck = admin_sql.find("acl.privilege_type <> 'SELECT'")
i_schemagrant = admin_sql.find("GRANT USAGE ON SCHEMA public")
need(i_writecheck != -1 and i_schemagrant != -1 and i_writecheck < i_schemagrant,
     "02-admin-role.sql does not look for a reachable write BEFORE it grants in schema "
     "public; a refusal that arrives after the change is not a precondition")
# Same requirement for the CREATE route. It was the one exception: the test lived only in
# the post-condition, so a role that could already create in public got three read grants
# before its named refusal (measured: the default-privilege row was left behind).
i_createcheck = admin_sql.find("pg_catalog.has_schema_privilege(r.oid, 'public', 'CREATE')")
need(i_createcheck != -1 and i_schemagrant != -1 and i_createcheck < i_schemagrant,
     "02-admin-role.sql checks CREATE on schema public only after it grants there; the "
     "refusal must precede the grants, as it does for every other route")

# ---------------------------------------------------------------- 4 · the grants are the
# product's own. This is the anti-drift assertion: the .sql must instantiate exactly the
# statements RenderProvisionSQL emits for the admin role in the single-role posture.
rendered = re.search(
    r'add\("admin role: read-only on the owner\'s future \+ existing tables",\s*'
    r'fmt\.Sprintf\("([^"]+)"',
    dbsetup)
need(rendered is not None,
     "core/internal/store/sqlstore/dbsetup.go no longer renders the admin role's grant "
     "step under its known label; this leg can no longer prove the .sql matches the product")
if rendered:
    template = rendered.group(1).replace("\\n", "\n")
    # The renderer's argument order in the single-role posture: database, admin, admin,
    # admin, owner(=app), admin.
    instantiated = template % ("olivares", "olivares_admin", "olivares_admin",
                               "olivares_admin", "olivares_app", "olivares_admin")
    for stmt in [s.strip() for s in instantiated.split(";") if s.strip()]:
        flat = re.sub(r"\s+", " ", stmt)
        present = any(re.sub(r"\s+", " ", c.strip()) == flat
                      for c in admin_stmts.split(";"))
        need(present,
             "02-admin-role.sql does not carry the grant the product itself renders for "
             f"the administrative role: {flat!r}")

# ---------------------------------------------------------------- 5 · the documentation
# states the consequences it is now responsible for.
for needle, what in (
    ("OLIVARES_ADMIN_PASSWORD", "the administrative password variable"),
    ("cross_tenant_admin_pool_not_configured", "what a stack without the role actually answers"),
    ("02-admin-role.sql", "the canonical file an operator applies"),
    ("docker-entrypoint-initdb.d` runs **only when the data directory is empty**",
     "the initialization-only lifecycle of the initdb hook"),
    ("URI-safe passwords only", "the URI-interpolation restriction on both DSNs"),
):
    need(needle in readme,
         f"deploy/compose/README.md does not document {what}")
need("--allow-privileged-db-role" in readme,
     "deploy/compose/README.md no longer names --allow-privileged-db-role as the thing NOT "
     "to reach for when the refusal appears")

# ---------------------------------------------------------------- 6 · the leg is wired on
# BOTH sides of gate parity, so it can neither poison main nor be discovered after landing.
# ⛔ ANCHORED TO THE WHOLE LINE, and the mutation battery is why. `lint:compose-postgres-
# bootstrap` is a PREFIX of `lint:compose-postgres-bootstrap:selftest`, so a plain
# substring search finds the base leg inside the selftest line: deleting the base leg
# from the hook left this check green, and the mutant survived. A parity assertion that
# cannot see a leg being removed asserts nothing.
for target in ("lint:compose-postgres-bootstrap", "lint:compose-postgres-bootstrap:selftest"):
    t = re.escape(target)
    need(re.search(rf"(?m)^  {t}:$", taskfile),
         f"Taskfile.yml has no {target} target")
    need(re.search(rf"(?m)^[ \t]*(?:[A-Za-z_][A-Za-z0-9_]*=\S+[ \t]+)*task {t}[ \t]*$", prepush),
         f".githooks/pre-push does not run {target}")
    need(re.search(rf"(?m)^[ \t]*run: task {t}[ \t]*$", mainline),
         f"mainline-ci.yml does not run {target}")

if fail:
    for f in fail:
        print(f"compose-postgres-bootstrap: FAIL — {f}", file=sys.stderr)
    sys.exit(1)
PY

printf 'compose-postgres-bootstrap: OK — distinct read-only BYPASSRLS pool wired, app role unchanged, no DROP/ALTER of an existing principal, grants match the product renderer\n'
