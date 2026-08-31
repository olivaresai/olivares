#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# quickstart-smoke.sh — runs the EXACT commands the quickstart documents
# (docs-site/src/content/docs/start/quickstart.md) against the real single binary
# and asserts the numbers the page prints. It is the time-to-value reproducibility
# contract (G10): if the quickstart's hero stops being true — the
# install→value path breaks, or the R/RW drift count drifts from the doc — this
# fails, so the page can never quietly lie.
#
# It exercises BOTH honest hero paths end to end:
#   A) the instant demo estate (serve --seed-demo): synthetic observations through
#      the REAL bus/graph — the value-at-minute-one on-ramp.
#   B) the real connector path: a stock serve with the pgAudit connector pointed at a
#      PostgreSQL audit log, proving the hero runs on genuinely-parsed data, not a
#      demo — including the create-org → restart-with-OLIVARES_SOURCES_CONFIG flow
#      every operator walks.
#
# Usage:  scripts/quickstart-smoke.sh
# Requires: go (to build if ./bin/olivares is absent), curl, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# The engine binary is built ONE way outside a release: see scripts/lib/build-bin.sh.
# Writing the flags out longhand here is what let five scripts drift from build:bin.
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/build-bin.sh"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
PORT="${SMOKE_PORT:-8951}"
GRPC_PORT="$((PORT + 1))"
# ⛔ EL TRAP SE ARMA ANTES DEL PRIMER TEMPORAL (the reviewer, A-02). Estaba DESPUES en los cuatro, y
#    un fallo entre el `mktemp` y el `trap` —medido con rc 7— dejaba los cuatro temporales
#    vivos, sin dueño y sin nadie que los borrara. La funcion de limpieza tolera que sus
#    variables aun no existan (todas se leen con `${VAR:-}`), asi que armarla temprano no
#    cuesta nada y cierra la ventana entera.
cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null || true
  [ -n "$PID" ] && wait "$PID" 2>/dev/null || true
  rm -rf "$WORK" ${EXEC_TMP:+"$EXEC_TMP"}
}
trap cleanup EXIT

WORK="$(mktemp -d)"
BASE="http://127.0.0.1:$PORT"
PID=""


# ⛔ ESTO VA DESPUES DEL `trap`, y no es orden estetico (the reviewer, A-02). El scratch se crea
#    aqui; si se creara ANTES de que la limpieza este armada, cualquier fallo en el setup
#    intermedio —otro `mktemp`, un `curl`— lo dejaria colgado sin dueño. La ventana era de
#    18 a 41 lineas segun el guion. Ahora no hay ventana.
# ⛔ EL MOTOR EXTRAE SUS PLUGINS A `$TMPDIR` Y LOS EJECUTA (`cmd/olivares/boot.go:1423`), y en estos
#    contenedores `/tmp` esta montado noexec, asi que el `fork/exec` falla con el bit puesto y la
#    fuente se rechaza en la recarga. Se SONDEA un directorio que ejecute —no se deduce de la ruta—
#    y se le pasa al motor. Si ninguno ejecuta se DICE, porque el silencio aqui se lee como «no
#    hacia falta». Detalle y alcance honesto, en la cabecera de la libreria.
# shellcheck source=/dev/null
. "$ROOT/scripts/lib/exec-tmpdir.sh"
# ⛔ SE REHUSA, NO SE AVISA (the reviewer, A-01). La version anterior imprimia el aviso, vaciaba
#    `EXEC_TMP` y ARRANCABA IGUAL con `/tmp`: es decir, el seguro no aseguraba nada — el motor
#    volvia a extraer sus plugins donde no puede ejecutarlos y la fuente se rechazaba en la
#    recarga, en silencio y deny-closed, que es exactamente el fallo que este guion existe para
#    cerrar. Un control que avisa y sigue no es un control: es un comentario con `echo`.
EXEC_TMP="$(olivares_exec_tmpdir)" || {
  echo "$(basename "$0"): ⛔ NO ARRANCO: ningun directorio temporal EJECUTA." >&2
  echo "   El motor extrae sus plugins de conector a \$TMPDIR y los LANZA" >&2
  echo "   (cmd/olivares/boot.go), y aqui ninguno de los candidatos permite execve —" >&2
  echo "   tipicamente porque /tmp esta montado noexec. Arrancar igual dejaria el plano de" >&2
  echo "   conectores muerto SIN decirlo." >&2
  echo "   Remedio: exporta OLIVARES_EXEC_TMPDIR a un directorio que ejecute." >&2
  exit 2
}

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "==> $*"; }

# assert_eq LABEL GOT WANT
assert_eq() {
  if [ "$2" != "$3" ]; then fail "$1: got '$2', want '$3'"; fi
  echo "    ok: $1 = $2"
}

# wait_health: poll /healthz until the engine answers (or give up).
wait_health() {
  for _ in $(seq 1 80); do
    curl -sf "$BASE/healthz" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  return 1
}

# graph_field FIELD: prints a python-computed scalar over /graph (TOKEN+TENANT in env).
api_get() { curl -sf "$BASE$1" -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"; }

if [ ! -x "$BIN" ]; then
  note "building $BIN (the quickstart's 'task build')"
  build_olivares_bin "$BIN"
fi

START="$(date +%s)"

# ---------------------------------------------------------------------------
# PATH A — instant demo estate (the value-at-minute-one on-ramp)
# ---------------------------------------------------------------------------
note "PATH A: boot the demo estate (serve --insecure --seed-demo)"
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
"$BIN" serve --insecure --seed-demo \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/demo" >"$WORK/demo.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/demo.log" >&2; fail "demo engine never became healthy"; }

# The quickstart's exact login + tenant-resolve commands.
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
[ -n "$TOKEN" ] || fail "demo login returned no token"
TENANT="$(curl -sf "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"
[ -n "$TENANT" ] || fail "could not resolve the demo tenant"
note "demo tenant: $TENANT"

note "PATH A: assert the access graph (the hero) matches the quickstart"
GRAPH="$(api_get '/v1/m/accessmap/graph?limit=200')"
read -r A_NODES A_EDGES A_FIRM A_APPROX <<EOF
$(printf '%s' "$GRAPH" | python3 -c '
import sys,json
from collections import Counter
g=json.load(sys.stdin); E=g.get("edges",[])
t=Counter(e.get("attribution_tier") for e in E)
print(len(g.get("nodes",[])), len(E), t.get("firm",0), t.get("approximate",0))')
EOF
# ⛔ THE FOUR NUMBERS COME FROM THE README, NOT FROM LITERALS HERE.
#
# README.md's Quickstart, step 3, publishes "20 nodes / 13 edges, with 8 unexpected accesses and
# 2 unused grants", and the paragraph that closes Quickstart says THIS SCRIPT "asserts the
# access-map and drift counts listed above, so this section cannot quietly drift from the code".
# (Cited by SECTION, not by line: the line numbers this comment used to carry were already two
# rewrites stale, and a stale pointer sends the next reader to the wrong paragraph.)
# It could drift: the four values lived here as literals of
# their own, and nothing compared them with the sentence. Change the prose and this stayed green;
# change these and the prose stayed wrong. Two declarations of one fact, with the guarantee written
# on top of the pair.
#
# Parsed from the canonical English README — the six translations are held to the same numbers by
# their own parity gate, and duplicating the parse per locale would recreate the defect one level up.
readme_counts() {
    local readme="${ROOT}/README.md"
    [ -f "$readme" ] || fail "cannot read $readme to take the published counts from it"
    python3 - "$readme" <<'PYEOF'
import re, sys
src = open(sys.argv[1], encoding="utf-8").read()
m = re.search(
    r"\((\d+) nodes / (\d+) edges, with (\d+) unexpected accesses and (\d+) unused grants\)",
    src,
)
if not m:
    sys.stderr.write(
        "quickstart-smoke: the README no longer states the counts in the shape this script reads.\n"
        "  Expected: (N nodes / N edges, with N unexpected accesses and N unused grants)\n"
        "  NOT a licence to skip the assertion: if the sentence changed shape, decide deliberately.\n")
    sys.exit(1)
print(" ".join(m.groups()))
PYEOF
}
# ⛔ This read used to be written as:
#
#     read -r R_NODES R_EDGES R_UNEXP R_UNUSED <<EOF
#     $(readme_counts) || exit 1
#     EOF
#
# Inside a heredoc `|| exit 1` is TEXT, not an operator, and one line carried two defects.
# The one that showed: `read` puts the leftover of the line into the LAST field, so R_UNUSED
# became "2 || exit 1" and the drift assertion went red. The one that mattered: the refusal
# above — the whole point of parsing rather than hardcoding — could not abort anything. If the
# README changed shape, python exited 1, the heredoc still produced a line, and the "guard" was
# decoration.
#
# Measured, both forms against the same mutant (readme_counts returns 1):
#   old:  R_NODES="||"  — NOT empty, so the [ -n ] below did not catch it either; the script
#         ran on and went red three assertions later on "demo graph nodes (README says ||)",
#         blaming the demo graph for a defect in the README parse
#   new:  aborts at this line, with python's own message
#
# And it was visible at all only because the corrupted field had an assertion on it. Had the
# leftover landed on a field nobody compares, this passes green while measuring nothing.
readme_line="$(readme_counts)" || exit 1
read -r R_NODES R_EDGES R_UNEXP R_UNUSED <<EOF
$readme_line
EOF
[ -n "$R_UNUSED" ] || fail "could not take the four published counts from README.md"
echo "    ok: README publishes ${R_NODES} nodes / ${R_EDGES} edges, ${R_UNEXP} unexpected, ${R_UNUSED} unused"

assert_eq "demo graph nodes (README says $R_NODES)" "$A_NODES" "$R_NODES"
assert_eq "demo graph edges (README says $R_EDGES)" "$A_EDGES" "$R_EDGES"
# attribution_tier is live in every edge (firm vs approximate, honestly split).
[ "$A_FIRM" -ge 1 ] || fail "expected at least one firm-attributed edge, got $A_FIRM"
[ "$A_APPROX" -ge 1 ] || fail "expected at least one approximate-attributed edge, got $A_APPROX"
echo "    ok: demo attribution_tier = ${A_FIRM} firm / ${A_APPROX} approximate"

note "PATH A: assert the PERMITTED-vs-OBSERVED drift (the differentiator)"
DRIFT="$(api_get '/v1/m/accessmap/drift')"
read -r A_UNEXP A_UNUSED A_SECRETS_TIER A_LOGS_TIER <<EOF
$(printf '%s' "$DRIFT" | python3 -c '
import sys,json
d=json.load(sys.stdin)
def tier(ref):
    for e in d.get("unexpected_accesses",[]):
        ed=e.get("edge",{})
        if ed.get("resource_ref")==ref: return ed.get("attribution_tier")
    return "MISSING"
print(d.get("unexpected_count",0), d.get("unused_count",0),
      tier("appdb.public.secrets"), tier("appdb.public.logs"))')
EOF
assert_eq "demo drift unexpected_accesses (README says $R_UNEXP)" "$A_UNEXP" "$R_UNEXP"
assert_eq "demo drift unused_grants (README says $R_UNUSED)" "$A_UNUSED" "$R_UNUSED"
# The doc's narrative: a firmly-attributed unexpected read (secrets) and an honestly
# approximate shared-pool write (logs). These guard the exact claims on the page.
assert_eq "demo secrets-read attribution_tier" "$A_SECRETS_TIER" "firm"
assert_eq "demo logs-write attribution_tier" "$A_LOGS_TIER" "approximate"

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""

# ---------------------------------------------------------------------------
# PATH B — real connector (the hero runs on genuinely-parsed data, not a demo)
# ---------------------------------------------------------------------------
note "PATH B: a real PostgreSQL pgAudit log (the connector parses this verbatim)"
python3 - "$WORK/postgresql.csv" <<'PY'
import csv, sys
def row(ts, user, db, msg, app):
    r = [''] * 26
    r[0], r[1], r[2] = ts, user, db
    r[11] = 'LOG'; r[13] = msg; r[22] = app; r[23] = 'client backend'
    return r
rows = [
    row("2026-06-09 09:00:01.001 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers", "billing-agent"),
    row("2026-06-09 09:00:02.002 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders", "billing-agent"),
    row("2026-06-09 09:00:03.003 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,3,1,READ,SELECT,TABLE,public.secrets", "billing-agent"),
]
with open(sys.argv[1], 'w', newline='') as f:
    csv.writer(f).writerows(rows)
PY

note "PATH B: fresh install — setup, login, create org (no default credentials)"
TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" \
"$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/real" >"$WORK/real1.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/real1.log" >&2; fail "real engine never became healthy"; }

SETUP_TOKEN="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/real1.log" | head -1)"
[ -n "$SETUP_TOKEN" ] || fail "no one-time setup token on stdout"
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP_TOKEN\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}" >/dev/null \
  || fail "setup failed"
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
[ -n "$TOKEN" ] || fail "real login returned no token"
TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
[ -n "$TENANT" ] || fail "could not create the production tenant"
note "production tenant: $TENANT"

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""

note "PATH B: restart with the pgAudit connector wired (OLIVARES_SOURCES_CONFIG)"
cat >"$WORK/sources.json" <<JSON
{"sources":[{"name":"salesdb-pgaudit","kind":"pgaudit","tenant":"$TENANT","config":{"log_path":"$WORK/postgresql.csv","format":"csvlog"}}]}
JSON
OLIVARES_SOURCES_CONFIG="$WORK/sources.json" \
  TMPDIR="${EXEC_TMP:-${TMPDIR:-/tmp}}" "$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/real" >"$WORK/real2.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/real2.log" >&2; fail "real engine never came back after restart (regression: second-boot deadlock?)"; }
# Moved env-config sources through the durable-roster reconcile, whose boot
# line is "ingest: sources wired from the durable roster added=N" (boot.go); the
# legacy per-source "ingest: wired source" line is kept as an alternate so the
# assertion survives either wiring path. A deny-closed rejection is an explicit
# failure, not a silent pass. (Assertion drifted from the binary since —
# caught and fixed in the launch dry-run.)
grep -qE 'ingest: (wired source|sources wired from the durable roster.*added=[1-9])' "$WORK/real2.log" \
  || { cat "$WORK/real2.log" >&2; fail "pgAudit source was not wired"; }
grep -q 'ingest: source not wired (deny-closed)' "$WORK/real2.log" \
  && { cat "$WORK/real2.log" >&2; fail "pgAudit source was rejected (deny-closed)"; }

TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

note "PATH B: assert the real pgAudit edges materialize in the R/RW graph"
B_EDGES=0
for _ in $(seq 1 40); do
  B_EDGES="$(api_get '/v1/m/accessmap/graph?limit=200' \
    | python3 -c 'import sys,json;print(len(json.load(sys.stdin).get("edges",[])))' 2>/dev/null || echo 0)"
  [ "$B_EDGES" -ge 3 ] 2>/dev/null && break
  sleep 0.25
done
GRAPH="$(api_get '/v1/m/accessmap/graph?limit=200')"
read -r B_EDGES B_CLEAN B_PGAUDIT <<EOF
$(printf '%s' "$GRAPH" | python3 -c '
import sys,json
from collections import Counter
g=json.load(sys.stdin); E=g.get("edges",[])
ct=Counter(e.get("coverage_tier") for e in E)
ss=Counter(e.get("signal_source") for e in E)
print(len(E), ct.get("clean",0), ss.get("pg_audit",0))')
EOF
assert_eq "real pgAudit edges" "$B_EDGES" "3"
assert_eq "real pgAudit clean-tier edges" "$B_CLEAN" "3"
assert_eq "real pg_audit-signalled edges" "$B_PGAUDIT" "3"

note "PATH B: assert the drift flags every observed-but-unpermitted access"
B_UNEXP="$(api_get '/v1/m/accessmap/drift' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin).get("unexpected_count",0))')"
assert_eq "real pgAudit drift unexpected_accesses" "$B_UNEXP" "3"

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""

ELAPSED="$(( $(date +%s) - START ))"
echo
echo "PASS — quickstart hero reproduced on the real binary in ${ELAPSED}s of wall clock."
echo "  Path A (demo):  20 nodes / 13 edges, drift 8 unexpected + 2 unused, attribution_tier live."
echo "  Path B (real):  3 pgAudit edges (clean tier), drift 3 unexpected — genuinely parsed, not seeded."
