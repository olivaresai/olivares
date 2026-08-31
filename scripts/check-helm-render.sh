#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Local/CI Helm render matrix for the storage and backup topology. Every valid
# render must be Kubernetes-schema-valid and free of dangling PVC references;
# invalid safety postures must be rejected by olivares.validate.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CHART="$ROOT/deploy/helm/olivares"
KUBE_VERSION=${KUBE_VERSION:-1.29.0}
TMP_ROOT=${TMPDIR:-/tmp}

command -v helm >/dev/null 2>&1 || { echo "check-helm-render: helm not found on PATH" >&2; exit 2; }
command -v kubeconform >/dev/null 2>&1 || { echo "check-helm-render: kubeconform not found on PATH" >&2; exit 2; }

RUN_DIR=$(mktemp -d "$TMP_ROOT/olivares-helm-render.XXXXXX")
trap 'rm -rf -- "$RUN_DIR"' EXIT HUP INT TERM

render() {
	name=$1
	shift
	out="$RUN_DIR/$name.yaml"
	helm template s398 "$CHART" --kube-version "$KUBE_VERSION" --namespace olivares-system "$@" >"$out"
	# ⛔ UN ESQUEMA QUE NO SE PUDO DESCARGAR NO ES UN CHART ROTO. `kubeconform` ya distingue las dos
	# cosas en su propio resumen —`Invalid: 0, Errors: 1`— y este gate las aplastaba en el mismo
	# `exit 1`, así que un parpadeo de red se leía como «el CronJob no valida».
	#
	# Medido el 2026-08-17 en `mainline-ci`: *«failed downloading schema at
	# raw.githubusercontent.com/... giving up after 3 attempt(s)»*, con `Valid: 4, Invalid: 0,
	# Errors: 1`. El chart estaba bien; lo que faltó fue el esquema.
	#
	# Es el canon §1.5 aplicado: la tercera respuesta la da el CÓDIGO DE SALIDA. 1 = hallazgo,
	# 2 = no he podido mirar. Y deny-closed: si el fallo NO casa una causa de red conocida, se cobra
	# como hallazgo, porque una rama dudosa es un hallazgo.
	kc_rc=0
	kc_out="$(kubeconform -strict -kubernetes-version "$KUBE_VERSION" -summary "$out" 2>&1)" || kc_rc=$?
	printf '%s\n' "$kc_out"
	if [ "$kc_rc" -ne 0 ]; then
		case "$kc_out" in
		*"failed downloading schema"* | *"giving up after"* | *"no such host"* | *"connection refused"* | *"i/o timeout"* | *"TLS handshake timeout"*)
			echo "check-helm-render: ⛔ NO HE PODIDO MIRAR $name: kubeconform no pudo DESCARGAR el" >&2
			echo "  esquema, así que no ha validado nada. Esto NO dice que el chart esté roto." >&2
			exit 2
			;;
		esac
		exit 1
	fi
	sh "$ROOT/scripts/check-helm-claims.sh" "$out"
	echo "check-helm-render: OK $name"
}

expect_reject() {
	name=$1
	want=$2
	shift 2
	if helm template s398 "$CHART" --kube-version "$KUBE_VERSION" --namespace olivares-system "$@" >"$RUN_DIR/$name.yaml" 2>"$RUN_DIR/$name.err"; then
		echo "check-helm-render: FAIL $name rendered successfully; expected rejection" >&2
		exit 1
	fi
	if ! grep -F "$want" "$RUN_DIR/$name.err" >/dev/null; then
		echo "check-helm-render: FAIL $name rejected for the wrong reason" >&2
		cat "$RUN_DIR/$name.err" >&2
		exit 1
	fi
	echo "check-helm-render: OK rejected $name"
}

# Every supported engine/topology/backup state. SQLite HA is deliberately absent:
# it is an invalid topology and is asserted as a rejection below.
render sqlite-single-backup-off
render sqlite-single-backup-on \
	--set backup.enabled=true --set backup.kekSecret=dr-kek
render postgres-single-backup-off \
	--set core.engine=postgres --set postgres.dsnSecret=postgres-dsn
render postgres-single-backup-on \
	--set core.engine=postgres --set postgres.dsnSecret=postgres-dsn \
	--set postgres.adminDsnKey=admin-dsn \
	--set backup.enabled=true --set backup.kekSecret=dr-kek
render postgres-ha-backup-off \
	--set core.engine=postgres --set core.replicaCount=2 \
	--set core.auditSigningKeySecret=audit-key --set postgres.dsnSecret=postgres-dsn
render postgres-ha-backup-on \
	--set core.engine=postgres --set core.replicaCount=2 \
	--set core.auditSigningKeySecret=audit-key --set postgres.dsnSecret=postgres-dsn \
	--set postgres.adminDsnKey=admin-dsn \
	--set backup.enabled=true --set backup.kekSecret=dr-kek

# F-09/F-10 regression contract on the exact supported HA+Postgres+backup render.
# The only PVC reference is the rendered destination claim; pg_dump must resolve
# the admin key and must never receive the RLS-scoped application DSN.
HA_BACKUP="$RUN_DIR/postgres-ha-backup-on.yaml"
PG_DUMP_BLOCK="$RUN_DIR/postgres-ha-pg-dump.yaml"
sed -n '/- name: pg-dump/,/- name: dr-backup/p' "$HA_BACKUP" >"$PG_DUMP_BLOCK"
grep -F -- '--dbname="$OLIVARES_ADMIN_DSN"' "$PG_DUMP_BLOCK" >/dev/null || {
	echo "check-helm-render: FAIL pg_dump does not use OLIVARES_ADMIN_DSN" >&2
	exit 1
}
if grep -F -- '--dbname="$OLIVARES_DSN"' "$PG_DUMP_BLOCK" >/dev/null; then
	echo "check-helm-render: FAIL pg_dump still uses the NOBYPASSRLS app DSN" >&2
	exit 1
fi
grep -F -- 'key: admin-dsn' "$PG_DUMP_BLOCK" >/dev/null || {
	echo "check-helm-render: FAIL pg_dump does not resolve postgres.adminDsnKey" >&2
	exit 1
}
if grep -F -- 'claimName: data-' "$HA_BACKUP" >/dev/null; then
	echo "check-helm-render: FAIL HA backup references the suppressed core data PVC" >&2
	exit 1
fi
if grep -F -- 'podAffinity:' "$HA_BACKUP" >/dev/null; then
	echo "check-helm-render: FAIL backup CronJob still carries core podAffinity" >&2
	exit 1
fi
echo "check-helm-render: OK HA backup uses BYPASSRLS dump DSN + emptyDir data"

# HA is intentionally independent of the per-node persistence toggle: its store
# and keys are external, and both core and backup data volumes render as emptyDir.
render postgres-ha-backup-on-persistence-off \
	--set core.engine=postgres --set core.replicaCount=2 \
	--set core.auditSigningKeySecret=audit-key --set core.persistence.enabled=false \
	--set postgres.dsnSecret=postgres-dsn --set postgres.adminDsnKey=admin-dsn \
	--set backup.enabled=true --set backup.kekSecret=dr-kek

expect_reject sqlite-ha "core.replicaCount > 1 requires core.engine=postgres" \
	--set core.replicaCount=2 --set core.auditSigningKeySecret=audit-key
expect_reject postgres-backup-without-admin "requires postgres.adminDsnKey" \
	--set core.engine=postgres --set postgres.dsnSecret=postgres-dsn \
	--set backup.enabled=true --set backup.kekSecret=dr-kek
expect_reject ha-backup-without-shared-audit-key "backup.enabled in HA requires core.auditSigningKeySecret" \
	--set core.engine=postgres --set core.replicaCount=2 \
	--set postgres.dsnSecret=postgres-dsn --set postgres.adminDsnKey=admin-dsn \
	--set backup.enabled=true --set backup.kekSecret=dr-kek
expect_reject sqlite-backup-without-persistence "backup.enabled requires core.persistence.enabled outside HA" \
	--set core.persistence.enabled=false \
	--set backup.enabled=true --set backup.kekSecret=dr-kek

echo "check-helm-render: PASS (7 valid renders; 4 invalid postures rejected)"
