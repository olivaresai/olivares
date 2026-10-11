#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Test check-c05-37-closure.sh with 37 cases and all three answers.
# Include acceptance controls: a gate rejecting everything passes rejection-only tests.
# The original 13 cases left eleven mutants alive in sol max's review
# (an internal design note (not shipped) §2). Each new case isolates one
# property: the old method case also failed its route; bad host/id values did not
# contain the valid substrings, letting `in` survive; 401 was the sole negative status.
# Prose cases deliberately use two forbidden claims in different case: one claim lets
# claims[:1] survive, while matching case lets removal of .lower() survive.
# Assert exact exit codes: an environmental rc 2 must not satisfy a finding's rc 1.
set -uo pipefail

SUT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-c05-37-closure.sh"
[ -r "$SUT" ] || { echo "test-c05-37-closure: COULD NOT LOOK: cannot read $SUT" >&2; exit 2; }

T="$(mktemp -d "${TMPDIR:-/tmp}/c0537.XXXXXX")" || { echo "no mktemp" >&2; exit 2; }
[ -d "$T" ] || { echo "mktemp did not return a directory" >&2; exit 2; }
trap 'rm -rf "$T"' EXIT

pass=0; failn=0
caso(){
  local nombre="$1" want="$2" got
  ( cd "$T" && OLIVARES_ROOT="$T" OLIVARES_C0537_JSON="$T/design/c05-37-closure.json" \
      bash "$SUT" ) >"$T/out" 2>&1
  got=$?
  if [ "$got" = "$want" ]; then
    pass=$((pass+1)); printf '  ok   %-62s rc=%s\n' "$nombre" "$got"
  else
    failn=$((failn+1)); printf '  FAIL %-62s rc=%s (expected %s)\n' "$nombre" "$got" "$want"
    sed 's/^/         /' "$T/out"
  fi
}

# sembrar <sandbox_green> <production_green> <traza|SIN|NOFILE> [prosa] [webhook_id_declarado]
sembrar(){
  rm -rf "$T/design" "$T/ev"; mkdir -p "$T/design" "$T/ev"
  local wid="${5:-msg_REAL}"
  local ev='null' ruta="${6:-ev/001.json}"
  [ "$3" = "NOEVID" ] || ev="{\"trace\":\"$ruta\",\"webhook_id\":\"$wid\"}"
  cat > "$T/design/c05-37-closure.json" <<JSON
{ "schema": "${OLV_SCHEMA:-c05-37-closure/v1}",
  "sandbox_green": $1, "production_green": $2,
  "required_host_sandbox": "licenses-sandbox.olivares.ai",
  "required_host_production": "licenses.olivares.ai",
  "required_path": "/webhooks/dodo", "required_method": "POST", "required_status": 202,
  "required_user_agent_substring": "Svix-Webhooks/",
  "sender_webhook_id_prefix": "msg_", "own_probe_webhook_id_marker": "replay",
  "trace_dir": "ev", "runbook": "design/RB.md",
  "sandbox_evidence": $ev, "production_evidence": $ev,
  "doc_must_not_claim_while_false": ["C05-37 VERDE", "cadena de entrega CERRADA", "ya recibe de Svix", "sin bloqueos pendientes"] }
JSON
  printf '# runbook de prueba\n%s\n' "${4:-}" > "$T/design/RB.md"
  case "$3" in SIN|NOEVID) : ;; NOFILE) rm -f "$T/ev/001.json" ;; *) mkdir -p "$T/$(dirname "$ruta")"; printf '%s\n' "$3" > "$T/$ruta" ;; esac
}

ev(){ # ev <host> <metodo> <url> <status> <wid> <ua>
  printf '[{"ts_utc":"2026-08-28T17:24:20.742Z","method":"%s","url":"%s","status":%s,"headers":{"host":"%s","webhook-id":"%s","user-agent":"%s"}}]' \
    "$2" "$3" "$4" "$1" "$5" "$6"
}
H=licenses-sandbox.olivares.ai
U=https://licenses-sandbox.olivares.ai/webhooks/dodo
SVIX='Svix-Webhooks/1.96.1'

BUENA=$(ev $H POST $U 202 msg_REAL "$SVIX")
SOLO_GET=$(ev $H GET https://$H/health 200 '' "$SVIX")
GET_RUTA_BUENA=$(ev $H GET $U 202 msg_REAL "$SVIX")
OTRO_HOST=$(ev hooks-sandbox.olivaresai.dev POST https://hooks-sandbox.olivaresai.dev/webhooks/dodo 202 msg_REAL "$SVIX")
HOST_QUE_CONTIENE=$(ev licenses-sandbox.olivares.ai.ajeno.example POST https://licenses-sandbox.olivares.ai.ajeno.example/webhooks/dodo 202 msg_REAL "$SVIX")
RUTA_QUE_CONTIENE=$(ev $H POST https://$H/otro/webhooks/dodo 202 msg_REAL "$SVIX")
RUTA_MALA=$(ev $H POST https://$H/health 202 msg_REAL "$SVIX")
SONDA_PROPIA=$(ev $H POST $U 202 msg_s1012replay_dead "$SVIX")
SIN_PREFIJO=$(ev $H POST $U 202 probe-mia "$SVIX")
PREFIJO_DENTRO=$(ev $H POST $U 202 x-msg_REAL "$SVIX")
RECHAZADA_401=$(ev $H POST $U 401 msg_REAL "$SVIX")
ERROR_500=$(ev $H POST $U 500 msg_REAL "$SVIX")
UA_PROPIO=$(ev $H POST $U 202 msg_REAL curl/7.88.1)
OTRO_WID=$(ev $H POST $U 202 msg_OTRO "$SVIX")
STATUS_CADENA='[{"ts_utc":"t","method":"POST","url":"https://licenses-sandbox.olivares.ai/webhooks/dodo","status":"202","headers":{"host":"licenses-sandbox.olivares.ai","webhook-id":"msg_REAL","user-agent":"Svix-Webhooks/1.96.1"}}]'
NO_ES_LISTA='{"eventos":[]}'
HEADERS_RAROS='[{"ts_utc":"t","method":"POST","url":"https://licenses-sandbox.olivares.ai/webhooks/dodo","status":202,"headers":"no soy un objeto"}]'
EVENTO_RARO='["no soy un objeto"]'
SIN_STATUS='[{"ts_utc":"t","method":"POST","url":"https://licenses-sandbox.olivares.ai/webhooks/dodo","headers":{"host":"licenses-sandbox.olivares.ai","webhook-id":"msg_REAL","user-agent":"Svix-Webhooks/1.96.1"}}]'
URL_CONTRADICE='[{"ts_utc":"t","method":"POST","url":"https://ajeno.example/webhooks/dodo","status":202,"headers":{"host":"licenses-sandbox.olivares.ai","webhook-id":"msg_REAL","user-agent":"Svix-Webhooks/1.96.1"}}]'
OTRA_SONDA_PROPIA=$(ev $H POST $U 202 msg_s1056replay_mia "$SVIX")
BUENA_PROD=$(ev licenses.olivares.ai POST https://licenses.olivares.ai/webhooks/dodo 202 msg_PROD "$SVIX")
LISTA_ANIDADA_DESPUES="[${BUENA#[}"; LISTA_ANIDADA_DESPUES="${LISTA_ANIDADA_DESPUES%]},[]]"
# La llegada buena DETRAS de un senuelo: sin esto, `for ev in data[:1]` es indistinguible
# del bucle entero, porque todos los demas fixtures traen UN solo evento.
SENUELO='{"ts_utc":"t","method":"GET","url":"https://licenses-sandbox.olivares.ai/health",'
SENUELO="$SENUELO"'"status":200,"headers":{"host":"licenses-sandbox.olivares.ai","user-agent":"control"}}'
BUENA_SEGUNDA="[$SENUELO,${BUENA#[}"

echo "test-c05-37-closure: 37 cases"

# ── NO-DISPARO ───────────────────────────────────────────────────────────────────────────────
sembrar false false SIN;        caso "no trigger · contract false, no trace" 0
sembrar false false "$BUENA";   caso "no trigger · false with an extra trace" 0
sembrar true false "$BUENA";    caso "no trigger · green WITH the receipt it NAMES" 0
sembrar true false "$BUENA_SEGUNDA"; caso "no trigger · valid receipt comes AFTER a decoy" 0

# ── DISPARO ──────────────────────────────────────────────────────────────────────────────────
sembrar true false "$SOLO_GET";          caso "green with a trace containing only GET /health" 1
sembrar true false "$GET_RUTA_BUENA";    caso "GET to the CORRECT path — isolates the method" 1
sembrar true false "$OTRO_HOST";         caso "the trace is from ANOTHER host" 1
sembrar true false "$HOST_QUE_CONTIENE"; caso "host that CONTAINS the correct host — isolates equality" 1
sembrar true false "$RUTA_QUE_CONTIENE"; caso "path that CONTAINS the correct path — isolates equality" 1
sembrar true false "$RUTA_MALA";         caso "wrong path" 1
sembrar true false "$SONDA_PROPIA" "" msg_s1012replay_dead; caso "SELF PROBE (replay) — the failure from the 27th" 1
sembrar true false "$SIN_PREFIJO" "" probe-mia; caso "webhook-id without the sender prefix" 1
sembrar true false "$PREFIJO_DENTRO" "" x-msg_REAL; caso "webhook-id that CONTAINS the prefix without starting with it" 1
sembrar true false "$RECHAZADA_401";     caso "REAL receipt rejected by the Worker with 401" 1
sembrar true false "$ERROR_500";         caso "receipt with 500 — verifies that more than 401 is checked" 1
sembrar true false "$UA_PROPIO";         caso "sender ID but OUR user-agent" 1
sembrar true false "$OTRO_WID";          caso "valid receipt for a DIFFERENT webhook-id than the named one" 1
sembrar true false "$STATUS_CADENA";     caso "status as STRING '202', not an integer" 1
sembrar false false SIN "C05-37 verde";  caso "premature claim · lowercase (isolates .lower())" 1
sembrar true false "$BUENA" "cadena de entrega CERRADA"; caso "premature claim · second phrase with sandbox ALREADY green" 1
sembrar true true "$BUENA";              caso "production green with SANDBOX evidence" 1

# ── NO HE PODIDO MIRAR ───────────────────────────────────────────────────────────────────────
sembrar true false "$OTRA_SONDA_PROPIA" "" msg_s1056replay_mia; caso "self probe with ANOTHER naming convention" 1
sembrar true false "$URL_CONTRADICE";  caso "correct host header but URL for another host" 1
sembrar true false "$BUENA" "" msg_REAL ev/otra/002.json; caso "no trigger · trace is at another path under trace_dir" 0
# ⛔ CONTROL POSITIVO DE PRODUCCIÓN. Sin él, un mutante que hiciera fallar SIEMPRE con
# production_green=true sobrevive, y la batería celebra que producción no pueda cerrar nunca.
sembrar false true "$BUENA_PROD" "" msg_PROD; caso "no trigger · PRODUCTION green with its own evidence" 0
sembrar true false "$BUENA" "ya recibe de Svix"; caso "premature claim · THIRD phrase in the list" 1
sembrar true false "$BUENA" "sin bloqueos pendientes"; caso "premature claim · FOURTH phrase in the list" 1
sembrar true false NOFILE;      caso "green and the NAMED trace does not exist" 2
sembrar true false "$SIN_STATUS";      caso "otherwise valid event WITHOUT status — incomplete trace" 2
OLV_SCHEMA="c05-37-closure/v99" sembrar true false "$BUENA"; caso "unknown contract schema" 2
sembrar true false "$BUENA" "" msg_REAL fuera/001.json; caso "the NAMED trace is OUTSIDE trace_dir" 2
sembrar true false "$BUENA"; sed -i 's/"sandbox_green": true/"sandbox_green": "si"/' "$T/design/c05-37-closure.json"; caso "sandbox_green is not boolean" 2
sembrar true false "$LISTA_ANIDADA_DESPUES"; caso "invalid shape AFTER the valid event — order does not excuse it" 2
sembrar true false NOEVID;      caso "green WITHOUT named evidence" 2
sembrar true false "$NO_ES_LISTA";    caso "the trace is not a list" 2
sembrar true false "$HEADERS_RAROS";  caso "unexpected headers type — no exception" 2
sembrar true false "$EVENTO_RARO";    caso "event that is not an object — no exception" 2

printf 'test-c05-37-closure: %d passed, %d failed\n' "$pass" "$failn"
[ "$failn" -eq 0 ] || exit 1
