#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Hermetic mutation battery for scripts/check-compose-postgres-bootstrap.sh.
#
# It copies the exact files that leg reads into a scratch tree, proves the UNMUTATED
# copy passes FIRST (so a kill is the mutant and not the harness), then injects one
# real defect at a time and requires a red with the leg's own named message. Each
# mutant is a defect somebody could plausibly ship: the ones that would put a
# never-settable-up Postgres stack back in the box, or hand the administrative pool
# authority it must not have.
#
# No Docker, no network, no Postgres server. Verdicts: 0 = every mutant killed ·
# 1 = a mutant survived · 2 = NO HE PODIDO MIRAR.
set -euo pipefail
LC_ALL=C
export LC_ALL

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
scratch_parent="${TMPDIR:-}"
case "$scratch_parent" in /*) ;; *)
	printf '%s\n' 'test-compose-postgres-bootstrap: NO HE PODIDO MIRAR — TMPDIR must be absolute' >&2
	exit 2
	;;
esac
[[ -d "$scratch_parent" ]] || {
	printf 'test-compose-postgres-bootstrap: NO HE PODIDO MIRAR — TMPDIR is absent: %s\n' "$scratch_parent" >&2
	exit 2
}
for tool in bash cp mkdir mktemp python3 rm; do
	command -v "$tool" >/dev/null 2>&1 || {
		printf 'test-compose-postgres-bootstrap: NO HE PODIDO MIRAR — missing %s\n' "$tool" >&2
		exit 2
	}
done

scratch="$(mktemp -d "$scratch_parent/compose-pg-boot.XXXXXX")"
cleanup() {
	case "$scratch" in "$scratch_parent"/compose-pg-boot.*) rm -rf -- "$scratch" ;; esac
}
trap cleanup EXIT INT TERM

# The closed set of inputs the leg reads. Copied, never edited in place: the battery
# must not be able to leave a mutation behind in the working tree.
FILES=(
	deploy/compose/docker-compose.postgres.yml
	deploy/compose/initdb/10-app-role.sh
	deploy/compose/README.md
	deploy/postgres/01-app-role.sql
	deploy/postgres/02-admin-role.sql
	core/internal/store/sqlstore/dbsetup.go
	Taskfile.yml
	.githooks/pre-push
	.github/workflows/mainline-ci.yml
	scripts/check-compose-postgres-bootstrap.sh
	scripts/test-compose-postgres-bootstrap.sh
)

seed() { # seed <dir>
	local dest="$1" f
	for f in "${FILES[@]}"; do
		mkdir -p "$dest/$(dirname "$f")"
		cp "$root/$f" "$dest/$f"
	done
}

run_leg() { # run_leg <dir> -> exit code of the leg, output on stdout
	OLIVARES_ROOT="$1" bash "$1/scripts/check-compose-postgres-bootstrap.sh" 2>&1
}

seed "$scratch/control"
if ! control_out="$(run_leg "$scratch/control")"; then
	printf 'test-compose-postgres-bootstrap: NO HE PODIDO MIRAR — the UNMUTATED control is already red:\n%s\n' \
		"$control_out" >&2
	exit 2
fi
printf 'ok 1 - unmutated control passes through the same path the mutants take\n'

n=1
kill_mutant() { # kill_mutant <label> <file> <python-expression-body>
	local label="$1" file="$2" body="$3" dir out rc
	n=$((n + 1))
	dir="$scratch/m$n"
	rm -rf -- "$dir"
	seed "$dir"
	python3 - "$dir/$file" "$body" <<'PY'
import pathlib
import sys
p = pathlib.Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
ns = {"s": s}
exec("s = " + sys.argv[2], ns)          # noqa: - fixture mutation, local scratch only
if ns["s"] == s:
    print(f"MUTATION ANCHOR MISSING in {p}", file=sys.stderr)
    raise SystemExit(3)
p.write_text(ns["s"], encoding="utf-8")
PY
	set +e
	out="$(run_leg "$dir")"
	rc=$?
	set -e
	if [[ "$rc" -eq 0 ]]; then
		printf 'not ok %d - %s: the mutant SURVIVED\n' "$n" "$label" >&2
		exit 1
	fi
	if [[ "$rc" -ne 1 ]]; then
		printf 'not ok %d - %s: expected a named violation (rc 1), got rc %d:\n%s\n' \
			"$n" "$label" "$rc" "$out" >&2
		exit 1
	fi
	printf 'ok %d - %s (rc=%d: %s)\n' "$n" "$label" "$rc" \
		"$(printf '%s' "$out" | grep -m1 'FAIL —' | cut -c1-120)"
}

kill_mutant "the sample loses --admin-dsn (the defect this bootstrap repairs)" \
	deploy/compose/docker-compose.postgres.yml \
	's.replace("      - --admin-dsn=postgres://olivares_admin:${OLIVARES_ADMIN_PASSWORD:?set OLIVARES_ADMIN_PASSWORD in .env}@postgres:5432/olivares?sslmode=disable\n", "")'

kill_mutant "the administrative DSN reuses the application credential" \
	deploy/compose/docker-compose.postgres.yml \
	's.replace("--admin-dsn=postgres://olivares_admin:${OLIVARES_ADMIN_PASSWORD:?set OLIVARES_ADMIN_PASSWORD in .env}", "--admin-dsn=postgres://olivares_admin:${OLIVARES_DB_PASSWORD:?set OLIVARES_DB_PASSWORD in .env}")'

kill_mutant "the refusal is overridden with --allow-privileged-db-role" \
	deploy/compose/docker-compose.postgres.yml \
	's.replace("      - --engine=postgres\n", "      - --engine=postgres\n      - --allow-privileged-db-role\n")'

kill_mutant "the administrative role loses BYPASSRLS (the engine would refuse it)" \
	deploy/postgres/02-admin-role.sql \
	's.replace("LOGIN PASSWORD %L NOSUPERUSER BYPASSRLS", "LOGIN PASSWORD %L NOSUPERUSER NOBYPASSRLS")'

kill_mutant "the bootstrap starts DROPping an existing principal" \
	deploy/postgres/02-admin-role.sql \
	's.replace("SELECT pg_catalog.format(", "DROP ROLE IF EXISTS olivares_admin;\nSELECT pg_catalog.format(")'

kill_mutant "the bootstrap starts ALTERing an existing principal" \
	deploy/postgres/02-admin-role.sql \
	's.replace("GRANT CONNECT ON DATABASE olivares TO olivares_admin;", "ALTER ROLE olivares_admin BYPASSRLS;\nGRANT CONNECT ON DATABASE olivares TO olivares_admin;")'

kill_mutant "the application role is made a member of the administrative role" \
	deploy/postgres/02-admin-role.sql \
	's.replace("GRANT CONNECT ON DATABASE olivares TO olivares_admin;", "GRANT olivares_admin TO olivares_app;\nGRANT CONNECT ON DATABASE olivares TO olivares_admin;")'

kill_mutant "the read-only pool is granted a write privilege" \
	deploy/postgres/02-admin-role.sql \
	's.replace("GRANT SELECT ON ALL TABLES IN SCHEMA public TO olivares_admin;", "GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA public TO olivares_admin;")'

kill_mutant "future app-created tables lose their default SELECT" \
	deploy/postgres/02-admin-role.sql \
	's.replace("ALTER DEFAULT PRIVILEGES FOR ROLE olivares_app IN SCHEMA public\n\tGRANT SELECT ON TABLES TO olivares_admin;", "")'

kill_mutant "the posture refusal for a pre-existing role is removed" \
	deploy/postgres/02-admin-role.sql \
	's.replace("rolcanlogin AND rolbypassrls", "rolcanlogin")'

kill_mutant "the initdb hook stops running the administrative role file" \
	deploy/compose/initdb/10-app-role.sh \
	's.replace("run_sql 02-admin-role.sql -v admin_password=\"${OLIVARES_ADMIN_PASSWORD}\"\n", "")'

kill_mutant "the initdb hook stops requiring the administrative credential" \
	deploy/compose/initdb/10-app-role.sh \
	's.replace(": \"${OLIVARES_ADMIN_PASSWORD:?", ": \"${OLIVARES_ADMIN_PASSWORD:-")'

kill_mutant "the APPLICATION role is quietly given BYPASSRLS" \
	deploy/postgres/01-app-role.sql \
	's.replace("CREATE ROLE olivares_app LOGIN PASSWORD :\x27app_password\x27\n  NOSUPERUSER NOBYPASSRLS", "CREATE ROLE olivares_app LOGIN PASSWORD :\x27app_password\x27\n  NOSUPERUSER BYPASSRLS")'

kill_mutant "the retained-volume lifecycle stops being documented" \
	deploy/compose/README.md \
	's.replace("`docker-entrypoint-initdb.d` runs **only when the data directory is empty**", "The initdb hook runs")'

kill_mutant "the leg is unwired from the push hook" \
	.githooks/pre-push \
	's.replace("task lint:compose-postgres-bootstrap\n", "")'

# ── correction-01 (2026-09-11): the read-only claim, which the file used to make without
# checking. Each of these four is a shape MEASURED to produce a false read-only success on
# an owned PostgreSQL 16.15 cluster before the guard existed.
kill_mutant "the reachable-write guard moves after the grants it protects" \
	deploy/postgres/02-admin-role.sql \
	's[:s.index("-- BEFORE granting anything here:")] + s[s.index("GRANT USAGE ON SCHEMA public TO olivares_admin;"):]'

kill_mutant "a grant to PUBLIC stops counting as reaching the pool" \
	deploy/postgres/02-admin-role.sql \
	's.replace("acl.grantee = 0 THEN true", "acl.grantee = -1 THEN true")'

kill_mutant "a write inherited through a group role stops counting" \
	deploy/postgres/02-admin-role.sql \
	's.replace("pg_catalog.pg_has_role(\x27olivares_admin\x27, acl.grantee, \x27MEMBER\x27)", "false")'

kill_mutant "a default privilege set globally stops being inspected" \
	deploy/postgres/02-admin-role.sql \
	's.replace("d.defaclnamespace = 0 OR ", "")'

# ── correction-02 (2026-09-11): three write routes an ACL-only guard cannot see. Each was
# measured on an owned PostgreSQL 16.15 cluster reaching a real write while the file still
# reported a read-only pool.
kill_mutant "membership of pg_write_all_data stops being checked" \
	deploy/postgres/02-admin-role.sql \
	's.replace("pg_write_all_data", "pg_read_all_data")'

kill_mutant "column-level privileges (attacl) stop being inspected" \
	deploy/postgres/02-admin-role.sql \
	's.replace("aclexplode(a.attacl)", "aclexplode(NULL::aclitem[])")'

kill_mutant "owning a public relation stops counting as write authority" \
	deploy/postgres/02-admin-role.sql \
	's.replace("pg_catalog.pg_has_role(\x27olivares_admin\x27, c.relowner, \x27MEMBER\x27)", "false")'

# ── correction-03: the CREATE route had its test only in the post-condition, so it refused
# after three grants had landed. Removing the pre-grant copy must stay red on the ordering.
kill_mutant "the schema CREATE check falls back to the post-condition only" \
	deploy/postgres/02-admin-role.sql \
	's[:s.index("\t-- CREATE on the schema is write authority of its own:")] + s[s.index("END\n$guard$;\n\nGRANT USAGE ON SCHEMA public"):]'

printf '1..%d\n' "$n"
printf '%s\n' 'NOT COVERED HERE: a real `docker compose up` against a real Postgres container. This battery is static and hermetic; the end-to-end evidence is SQL/API execution against an owned PostgreSQL 16 cluster.'
