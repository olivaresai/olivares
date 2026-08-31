#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# smoke.sh — runs the EXACT commands this example's README documents against the
# real single binary and asserts that a vendor-neutral OpenTelemetry GenAI span
# (gen_ai.*) sent over OTLP/HTTP materializes as attributed cost in FinOps. It is
# the example's reproducibility contract: if the OTel GenAI ingest path
# breaks — the receiver, the dual-name parsing, the cost pipeline — this fails.
#
# What it proves, end to end, against `olivares serve` with the Claude/OTEL
# source wired (semconv_opt_in=gen_ai_latest_experimental):
#   1. an OTLP/HTTP JSON trace export with one gen_ai.* span is accepted;
#   2. its token usage surfaces in /v1/m/finops/spend/summary (samples + tokens);
#   3. the provider is attributed (openai), proving it is parsed, not faked.
#
# Surface: connectors/claude (OTLP receiver + gen_ai profile), modules/finops.
#
# Usage:  examples/otel-genai-ingest/smoke.sh
# Requires: go-task (to build with the embedded source plugin if ./bin/olivares
#           is absent), curl, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="${OLIVARES_BIN:-$ROOT/bin/olivares}"
# SMOKE_PORT pins the trio (PORT, PORT+1, OTLP) when you need determinism. The old
# fixed defaults (8961/8962/4328) are a latent collision on shared CI hosts — three
# runner installs share one box, so two concurrent examples jobs would fight for the
# same port and the loser reads as "engine never became healthy". Default to asking
# the kernel for three free ports instead (held open together so they cannot repeat).
OTLP_PORT_PIN="${OTLP_PORT:-}"   # an explicit OTLP_PORT wins over either branch
if [ -n "${SMOKE_PORT:-}" ]; then
  PORT="$SMOKE_PORT"
  GRPC_PORT="$((PORT + 1))"
  OTLP_PORT="$((PORT + 2))"
else
  read -r PORT GRPC_PORT OTLP_PORT < <(python3 - <<'PY'
import socket
socks = [socket.socket() for _ in range(3)]
for s in socks:
    s.bind(("127.0.0.1", 0))
print(*[s.getsockname()[1] for s in socks])
for s in socks:
    s.close()
PY
)
fi
if [ -n "$OTLP_PORT_PIN" ]; then OTLP_PORT="$OTLP_PORT_PIN"; fi
# The engine fork/execs the embedded Claude/OTEL source plugin from $TMPDIR, so it
# must be exec-capable. The dev container's /tmp is tmpfs+noexec; default to a
# repo-local scratch dir on the workspace disk. On a normal host (CI, a laptop)
# /tmp is fine — set OLIVARES_SMOKE_TMPDIR=/tmp to use it.
TMPBASE="${OLIVARES_SMOKE_TMPDIR:-$ROOT/.examples-tmp}"
mkdir -p "$TMPBASE"
WORK="$(mktemp -d "$TMPBASE/otel.XXXXXX")"
# mktemp -d crea 0700. El motor bajo prueba, si corre como root, lanza cada plugin de
# conector bajo un uid DEDICADO no-root, y ese hijo tiene que atravesar TODA la cadena
# hasta su binario. Un solo eslabon en 0700 lo para con EACCES, que se lee como noexec.
chmod 711 "$WORK"
export TMPDIR="$WORK"
BASE="http://127.0.0.1:$PORT"
OTLP="http://127.0.0.1:$OTLP_PORT"
PID=""

cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null || true
  [ -n "$PID" ] && wait "$PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
note() { echo "==> $*"; }

assert_eq() {
  if [ "$2" != "$3" ]; then fail "$1: got '$2', want '$3'"; fi
  echo "    ok: $1 = $2"
}

wait_health() {
  # 120s, not 20s: this asserts "the engine becomes healthy", not "the engine boots
  # fast on an idle box". Measured on the shared CI host (run 30645091384, job
  # 91204480202): the sibling govern-claude-code smoke needed 59s to reach the same
  # point in that very job, while this one gave up at 20s with a healthy engine
  # seconds away — cold boot (key generation + migrations) under concurrent Go test
  # jobs simply costs more than the old budget allowed.
  for _ in $(seq 1 240); do
    curl -sf "$BASE/healthz" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  return 1
}

api_get() { curl -sf "$BASE$1" -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"; }

# The Claude/OTEL source ships embedded as an out-of-process plugin binary, so the
# binary must be built with `task build:bin` (which runs build:connectors). A plain
# `go build` of cmd/olivares would omit the plugin.
if [ ! -x "$BIN" ]; then
  note "building $BIN with embedded connectors (task build:bin)"
  ( cd "$ROOT" && task build:bin )
fi

# ---------------------------------------------------------------------------
# Step 1 — fresh install: setup, login, create the tenant the source feeds.
# ---------------------------------------------------------------------------
note "fresh install: serve, one-time setup, login, create the tenant"
"$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/data" >"$WORK/boot1.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/boot1.log" >&2; fail "engine never became healthy"; }

SETUP_TOKEN="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/boot1.log" | head -1)"
[ -n "$SETUP_TOKEN" ] || fail "no one-time setup token on stdout"
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP_TOKEN\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}" >/dev/null \
  || fail "setup failed"
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
[ -n "$TOKEN" ] || fail "login returned no token"
TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Agents","slug":"agents"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
[ -n "$TENANT" ] || fail "could not create the tenant"
note "tenant: $TENANT"

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""

# ---------------------------------------------------------------------------
# Step 2 — restart with the Claude/OTEL source wired and the GenAI profile ON.
# The profile is opt-in (the gen_ai.* conventions are Development status): set
# semconv_opt_in to enable the vendor-neutral ingest for ANY OTel agent.
# ---------------------------------------------------------------------------
note "restart with the gen_ai OTLP source (OLIVARES_SOURCES_CONFIG)"
cat >"$WORK/sources.json" <<JSON
{"sources":[{"name":"genai-otlp","kind":"claude","tenant":"$TENANT","config":{
  "semconv_opt_in":"gen_ai_latest_experimental",
  "enable_grpc":"false",
  "enable_http":"true",
  "http_addr":"127.0.0.1:$OTLP_PORT"
}}]}
JSON

OLIVARES_SOURCES_CONFIG="$WORK/sources.json" "$BIN" serve --insecure \
  --listen "127.0.0.1:$PORT" --grpc-listen "127.0.0.1:$GRPC_PORT" \
  --data-dir "$WORK/data" >"$WORK/boot2.log" 2>&1 &
PID=$!
wait_health || { cat "$WORK/boot2.log" >&2; fail "engine never came back after restart"; }
grep -q "wired source" "$WORK/boot2.log" || { cat "$WORK/boot2.log" >&2; fail "the gen_ai source was not wired"; }

# ---------------------------------------------------------------------------
# Step 3 — POST one gen_ai.* span as OTLP/HTTP JSON (what any OTel SDK exports).
# ---------------------------------------------------------------------------
note "POST the gen_ai span to the OTLP/HTTP receiver ($OTLP/v1/traces)"
OK=""
# 60s: the receiver lives in the out-of-process source plugin, which the engine
# fork/execs AFTER /healthz answers — on a loaded runner that gap is not 10s.
for _ in $(seq 1 120); do
  if curl -sf -X POST "$OTLP/v1/traces" \
      -H 'Content-Type: application/json' \
      --data-binary @"$HERE/span.json" >/dev/null 2>&1; then
    OK=1; break
  fi
  sleep 0.25
done
[ -n "$OK" ] || { cat "$WORK/boot2.log" >&2; fail "OTLP receiver never accepted the span (is $OTLP up?)"; }

# ---------------------------------------------------------------------------
# Step 4 — assert the span surfaced as attributed cost in FinOps.
# ---------------------------------------------------------------------------
# summary_fields prints "<samples> <input_tokens> <output_tokens> <provider>"
# from /v1/m/finops/spend/summary (provider is "openai" when present, else NONE).
summary_fields() {
  api_get '/v1/m/finops/spend/summary' 2>/dev/null | python3 -c '
import sys,json
try:
    d=json.load(sys.stdin)
except Exception:
    print("0 0 0 NONE"); sys.exit()
prov="NONE"
for b in d.get("by_provider",[]):
    if b.get("key")=="openai": prov="openai"
print(d.get("samples",0), d.get("input_tokens",0), d.get("output_tokens",0), prov)' 2>/dev/null || echo "0 0 0 NONE"
}

note "assert the GenAI usage surfaces in /v1/m/finops/spend/summary"
SAMPLES=0; IN=0; OUT=0; PROV=NONE
# 60s for the same reason as the receiver wait: ingest crosses the source plugin and
# the cost pipeline, and a busy host stretches that far past the old 10s budget.
for _ in $(seq 1 120); do
  read -r SAMPLES IN OUT PROV <<<"$(summary_fields)"
  [ "${SAMPLES:-0}" != "0" ] && break
  sleep 0.25
done

assert_eq "finops samples"        "$SAMPLES" "1"
assert_eq "finops input_tokens"   "$IN"      "1200"
assert_eq "finops output_tokens"  "$OUT"     "350"
assert_eq "finops provider"       "$PROV"    "openai"

kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; PID=""

echo
echo "PASS — a vendor-neutral OpenTelemetry GenAI span became attributed cost on the real binary:"
echo "  1 sample · 1200 in / 350 out tokens · provider=openai (genuinely parsed, not seeded)."
