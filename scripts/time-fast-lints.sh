#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# time-fast-lints.sh — how long the fast lane ACTUALLY costs, per lint, today.
#
# WHY: CLAUDE.md carried "4m15s, measured 2026-08-02" until 2026-08-15, when timing it produced
# 10m25s over 55 lints. Nobody lied; the number simply aged, in prose, in silence, while every
# lane planned around it. A duration written down is a claim with a shelf life, and this is the
# thing that renews it.
#
# It is a REPORT, not a gate: how long the lane may cost is a judgement, and a threshold invented
# here would be a number nobody can defend. It prints and refuses to grade.
#
# The list is DERIVED from the hook with `^[[:space:]]*task`, not retyped and not anchored at
# column zero — the hook indents one call inside a subshell, and an anchor at column zero misses
# it. That exact defect put four red cases in another battery for an hour on 2026-08-15.
set -uo pipefail
blind() { printf 'time-fast-lints: UNVERIFIED — %s\n' "$*" >&2; exit 2; }
ROOT="${OLIVARES_TIMING_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || blind "cannot enter $ROOT"
[ -f .githooks/pre-push ] || blind "no .githooks/pre-push at $ROOT; there is no lane to time"
command -v task >/dev/null 2>&1 || blind "go-task is not on PATH"

list="$(awk '/^[[:space:]]*task (lint:[a-z0-9:-]+|vet)$/ { sub(/^[[:space:]]*task /,""); print }
             /^[[:space:]]*task lint:prepush-refclass$/ { exit }' .githooks/pre-push)"
n="$(printf '%s\n' "$list" | grep -c . || true)"
[ "${n:-0}" -ge 10 ] || blind "derived only ${n:-0} lint(s) from the hook; 55 were called on 2026-08-15.
  A collapse that large means the pattern broke, not that the lane shrank."

# ⛔ THE LOAD IS RECORDED WITH THE TIMES, and that is not decoration. The first run of this
# script measured lint:disk-headroom at 39s; re-measured minutes later on a quieter box it was
# 5s, while lint:spdx (17->16) and lint:prepush-refclass (25->26) barely moved. A duration
# without the conditions it was taken under is the same kind of number this script exists to
# replace -- and this seat published one into the rules before noticing.
LOAD="$(cut -d' ' -f1-3 /proc/loadavg 2>/dev/null || echo '?')"
# ⛔ LA CUOTA, no `nproc`. Esta linea viaja en CADA duracion que publicamos («cite these WITH
#    the numbers below») y el canon las cita: con 16 en el divisor, una carga de 9,4 se lee
#    como 0,59x de la caja cuando es 2,35x de SOBRECARGA. Las cifras ya publicadas no quedan
#    invalidadas —llevan sus condiciones— pero su lectura se invierte.
CPUS="$(bash "$(dirname "$0")/cpu-quota.sh" 2>/dev/null || nproc 2>/dev/null || echo '?')"
printf '==> timing %s lint(s), sequentially, as the hook calls them\n' "$n"
printf '==> load %s on %s cpu(s) at start — cite these WITH the numbers below\n\n' "$LOAD" "$CPUS"
total=0; failed=0
while IFS= read -r t; do
	[ -n "$t" ] || continue
	s=$(date +%s); timeout 900 task "$t" >/dev/null 2>&1; rc=$?; e=$(date +%s)
	d=$((e - s)); total=$((total + d))
	[ "$rc" -eq 0 ] || failed=$((failed + 1))
	printf '%5ss  %-32s %s\n' "$d" "$t" "$([ "$rc" -eq 0 ] && echo ok || echo "rc=$rc")"
done <<EOF
$list
EOF

printf '\ntotal %ss (%dm%02ds) sequential over %s lint(s); %s did not exit 0.\n' \
	"$total" "$((total / 60))" "$((total % 60))" "$n" "$failed"
printf 'load was %s on %s cpu(s) at start, %s at end.\n' "$LOAD" "$CPUS" "$(cut -d' ' -f1-3 /proc/loadavg 2>/dev/null || echo '?')"
printf 'A REPORT: what the lane may cost is a judgement, and a threshold invented here would be a\n'
printf 'number nobody can defend. Cite this run, with its date, rather than a figure from prose.\n'
exit 0
