#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ci-postgres-service.sh — shared resolve/provision for the race-rest Postgres
# service and the manual three-package profile. Two operations only.
#
# resolve    PGPORT_HOST from job.services.postgres.ports['5432'] → GITHUB_ENV
#            and, when the job owns a second cluster, PGPORT_OTHER from
#            job.services.postgres_other.ports['5432'] → OLIVARES_TEST_POSTGRES_OTHER_DSN
#            of THIS job. Numeric + this kernel's real ephemeral range. Never
#            synthesises 5432, never accepts a dispatch DSN, never retargets
#            maintenance /postgres to /olivares.
# provision  literal race-rest role sequence: psql client, 01-app-role.sql,
#            admin NOSUPERUSER BYPASSRLS, CONNECT/schema/defaults, vector on
#            the app database. ON_ERROR_STOP=1; first failure aborts. Client
#            install fallback is the existing apt-get line, not a new bootstrap.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

usage() {
	echo "usage: $0 resolve|provision" >&2
	exit 2
}

[ "$#" -eq 1 ] || usage

resolve() {
	[ -n "${GITHUB_ENV:-}" ] || {
		echo "::error::ci-postgres-service resolve: GITHUB_ENV is unset; refusing to write DSNs into an unknown command file" >&2
		exit 1
	}
	case "${PGPORT_HOST:-}" in
		'' | *[!0-9]*)
			echo "::error::mapped host port for services.postgres:5432 is empty or non-numeric (got '${PGPORT_HOST:-}')" >&2
			exit 1 ;;
	esac
	# The port MUST come from THIS runner's kernel ephemeral range. Not a
	# cosmetic assertion: if Docker ever handed back 5432, the helper
	# scripts/pg-test-env.sh would reject it further down (its anti-5432
	# guard), so an out-of-range port has to die HERE, with a message that
	# says why, and not six steps later with an opaque error.
	[ -r /proc/sys/net/ipv4/ip_local_port_range ] || {
		echo "::error::ci-postgres-service resolve: cannot read this kernel's ephemeral port range" >&2
		exit 1
	}
	read -r LOPORT HIPORT < /proc/sys/net/ipv4/ip_local_port_range
	if [ "$PGPORT_HOST" -lt "$LOPORT" ] || [ "$PGPORT_HOST" -gt "$HIPORT" ]; then
		echo "::error::docker published Postgres on ${PGPORT_HOST}, outside this host's ephemeral range ${LOPORT}-${HIPORT}; refusing to continue" >&2
		exit 1
	fi
	# SECOND, job-owned cluster (OPTIONAL). Only the jobs that run
	# TestDRDestinationUnrelatedCluster declare services.postgres_other and bind PGPORT_OTHER;
	# every other job keeps behaving exactly as before, so this cannot make them require a
	# server they do not have. When it IS bound it is validated exactly like the primary, plus
	# a distinctness check: equal ports would be one server behind two names, and the test's
	# pg_control_system() system_identifier comparison would then fail with a confusing
	# message instead of this one.
	#
	# PRESENCE, NOT EMPTINESS, and the difference is the whole guard. A job that declares
	# services.postgres_other BINDS this variable; when the mapping is missing GitHub hands it
	# over as the EMPTY STRING, not as unset. `-n` reads those two states the same way, so an
	# absent mapping fell through to the primary DSNs, exited 0, and the failure surfaced much
	# later as the test's own "a second owned real cluster is required" - the exact confusion
	# this resolver exists to prevent. Measured at e51125f4e3: exit 0, primary DSNs written.
	#   UNSET          the calling job owns no second service  -> skip, previous behaviour
	#   PRESENT EMPTY  a required mapping is missing           -> refuse, BEFORE any write
	if [ -n "${PGPORT_OTHER+set}" ]; then
		if [ -z "$PGPORT_OTHER" ]; then
			echo "::error::PGPORT_OTHER is set but empty: this job binds services.postgres_other and its port mapping is missing; refusing before writing the job environment" >&2
			exit 1
		fi
		case "$PGPORT_OTHER" in
			*[!0-9]*)
				echo "::error::mapped host port for services.postgres_other:5432 is non-numeric (got '${PGPORT_OTHER}')" >&2
				exit 1 ;;
		esac
		if [ "$PGPORT_OTHER" -lt "$LOPORT" ] || [ "$PGPORT_OTHER" -gt "$HIPORT" ]; then
			echo "::error::docker published the SECOND Postgres on ${PGPORT_OTHER}, outside this host's ephemeral range ${LOPORT}-${HIPORT}; refusing to continue" >&2
			exit 1
		fi
		if [ "$PGPORT_OTHER" = "$PGPORT_HOST" ]; then
			echo "::error::both Postgres services resolved to host port ${PGPORT_HOST}; they must be two distinct servers" >&2
			exit 1
		fi
	fi
	{
		echo "PGHOSTPORT=127.0.0.1:${PGPORT_HOST}"
		# app = NOSUPERUSER NOBYPASSRLS owner of olivares (FORCE-RLS genuinely
		# exercised); admin = NOSUPERUSER BYPASSRLS for cross-tenant reads. The
		# superuser (maintenance) DSN targets /postgres, NOT /olivares: pgtest
		# only CREATE/DROP DATABASEs through it, and PostgreSQL refuses to drop
		# the database the session is connected to (scripts/pg-test-env.sh:64).
		echo "OLIVARES_TEST_POSTGRES_DSN=postgres://olivares_app:apppw@127.0.0.1:${PGPORT_HOST}/olivares?sslmode=disable"
		echo "OLIVARES_TEST_POSTGRES_ADMIN_DSN=postgres://olivares_admin:adminpw@127.0.0.1:${PGPORT_HOST}/olivares?sslmode=disable"
		echo "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=postgres://postgres:postgres@127.0.0.1:${PGPORT_HOST}/postgres?sslmode=disable"
		# The SECOND cluster, emitted ONLY when this job owns one. Maintenance DSN targets
		# /postgres because pgtest CREATEs and DROPs databases through it. The primary
		# least-privilege app/admin fixtures above are untouched.
		# The SAME presence test as the validation above, deliberately: if the two ever
		# disagree, one describes a state the other does not and the resolver stops being a
		# single decision. Reaching here with PGPORT_OTHER set means it is also valid.
		if [ -n "${PGPORT_OTHER+set}" ]; then
			echo "OLIVARES_TEST_POSTGRES_OTHER_DSN=postgres://postgres:postgres@127.0.0.1:${PGPORT_OTHER}/postgres?sslmode=disable"
		fi
		# This job HAS a Postgres service, so an unreachable server here is a broken
		# job — not an absence to skip past. `pgtest.Available` only learns that from the
		# RUN (core/internal/pgtest/pgtest.go:88-96): without this line the reachability
		# probe answers `false` and the whole Postgres leg vanishes QUIETLY, which is the
		# failure that probe was added to end. The declaration belongs to whoever
		# provisioned the server, and that is this job.
		echo "OLIVARES_TEST_POSTGRES_REQUIRED=1"
		# B5 (2026-07-30): the REAL pgvector integration test runs in this job —
		# the service image ships the extension and provisioning creates it. Docs
		# may call pgvector the self-host default only while this stays wired.
		echo "OLIVARES_TEST_VECTOR_DSN=postgres://olivares_app:apppw@127.0.0.1:${PGPORT_HOST}/olivares?sslmode=disable"
	} >> "$GITHUB_ENV"
	echo "resolved Postgres host port: ${PGPORT_HOST} (ephemeral range ${LOPORT}-${HIPORT})"
}

provision() {
	# Hosted ubuntu images ship psql; a self-hosted runner may not be
	# Debian-based at all — install the client only when it's missing.
	command -v psql >/dev/null 2>&1 || { sudo apt-get update && sudo apt-get install -y --no-install-recommends postgresql-client; }
	psql "postgres://postgres@${PGHOSTPORT:?PGHOSTPORT unset - the resolver step must run first}/postgres" -v ON_ERROR_STOP=1 \
		-v app_password=apppw -f deploy/postgres/01-app-role.sql
	psql "postgres://postgres@${PGHOSTPORT:?PGHOSTPORT unset - the resolver step must run first}/postgres" -v ON_ERROR_STOP=1 -c \
		"DROP ROLE IF EXISTS olivares_admin; CREATE ROLE olivares_admin LOGIN PASSWORD 'adminpw' NOSUPERUSER BYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION;"
	psql "postgres://postgres@${PGHOSTPORT:?PGHOSTPORT unset - the resolver step must run first}/postgres" -v ON_ERROR_STOP=1 -c \
		"GRANT CONNECT ON DATABASE olivares TO olivares_admin;"
	psql "postgres://postgres@${PGHOSTPORT:?PGHOSTPORT unset - the resolver step must run first}/olivares" -v ON_ERROR_STOP=1 -c \
		"GRANT USAGE ON SCHEMA public TO olivares_admin; ALTER DEFAULT PRIVILEGES FOR ROLE olivares_app IN SCHEMA public GRANT SELECT ON TABLES TO olivares_admin;"
	# pgvector (B5): the extension is not trusted, so the app role cannot create
	# it — the superuser does, once per database; the backend's own
	# CREATE EXTENSION IF NOT EXISTS then no-ops harmlessly.
	psql "postgres://postgres@${PGHOSTPORT:?PGHOSTPORT unset - the resolver step must run first}/olivares" -v ON_ERROR_STOP=1 -c \
		"CREATE EXTENSION IF NOT EXISTS vector;"
}

case "$1" in
	resolve) resolve ;;
	provision) provision ;;
	*) usage ;;
esac
