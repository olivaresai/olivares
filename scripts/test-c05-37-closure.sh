#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Batería de scripts/check-c05-37-closure.sh — 37 casillas, con las TRES respuestas.
#
# ⛔ UN CONTROL QUE SOLO SE PRUEBA CON EL CASO QUE RECHAZA NO ESTA PROBADO: un gate que rechaza
# TODO pasa cualquier batería de «rechaza». Por eso hay casillas de NO-DISPARO.
#
# ⛔ LA MITAD DE ESTAS CASILLAS LAS ESCRIBIO UN CONTRASTE, NO YO, y es el motivo de que existan.
# La primera versión tenía 13 y `sol max` derivó ONCE mutantes del gate que sobrevivían a ella
# (an internal design note (not shipped) §2). El patrón era siempre el mismo: **una
# casilla que falla por DOS motivos a la vez no aísla ninguno**. El caso del método fallaba
# también por ruta; el host malo no contenía al bueno, así que un `in` pasaba; el id malo no
# contenía `msg_`, así que un `in` pasaba; el único estado negativo era `401`, así que rechazar
# sólo `401` pasaba. Cada casilla nueva de abajo aísla exactamente una propiedad.
#
# ⛔ Y las casillas de PROSA llevan a propósito DOS frases prohibidas y distinta caja: con una
# sola frase, un `claims[:1]` sobrevive; con la misma caja, quitar `.lower()` sobrevive.
#
# Cada caso comprueba el CODIGO DE SALIDA EXACTO, no «distinto de cero»: un rc=2 de entorno
# pasaría cualquier aserción de «!= 0» y el gate podría estar roto y salir verde.
set -uo pipefail

SUT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-c05-37-closure.sh"
[ -r "$SUT" ] || { echo "test-c05-37-closure: NO HE PODIDO MIRAR: no leo $SUT" >&2; exit 2; }

T="$(mktemp -d "${TMPDIR:-/tmp}/c0537.XXXXXX")" || { echo "no mktemp" >&2; exit 2; }
[ -d "$T" ] || { echo "mktemp no devolvió directorio" >&2; exit 2; }
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
    failn=$((failn+1)); printf '  FAIL %-62s rc=%s (esperado %s)\n' "$nombre" "$got" "$want"
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

echo "test-c05-37-closure: 37 casillas"

# ── NO-DISPARO ───────────────────────────────────────────────────────────────────────────────
sembrar false false SIN;        caso "no-disparo · contrato en false, sin traza" 0
sembrar false false "$BUENA";   caso "no-disparo · false con traza de sobra" 0
sembrar true false "$BUENA";    caso "no-disparo · verde CON la llegada que NOMBRA" 0
sembrar true false "$BUENA_SEGUNDA"; caso "no-disparo · la llegada buena va DETRAS de un señuelo" 0

# ── DISPARO ──────────────────────────────────────────────────────────────────────────────────
sembrar true false "$SOLO_GET";          caso "verde y la traza sólo tiene GET a /health" 1
sembrar true false "$GET_RUTA_BUENA";    caso "GET a la ruta BUENA — aísla el método" 1
sembrar true false "$OTRO_HOST";         caso "la traza es de OTRO host" 1
sembrar true false "$HOST_QUE_CONTIENE"; caso "host que CONTIENE al bueno — aísla la igualdad" 1
sembrar true false "$RUTA_QUE_CONTIENE"; caso "ruta que CONTIENE a la buena — aísla la igualdad" 1
sembrar true false "$RUTA_MALA";         caso "ruta equivocada" 1
sembrar true false "$SONDA_PROPIA" "" msg_s1012replay_dead; caso "SONDA PROPIA (replay) — el fallo del 27" 1
sembrar true false "$SIN_PREFIJO" "" probe-mia; caso "webhook-id sin el prefijo del remitente" 1
sembrar true false "$PREFIJO_DENTRO" "" x-msg_REAL; caso "webhook-id que CONTIENE el prefijo sin empezar por él" 1
sembrar true false "$RECHAZADA_401";     caso "llegada REAL que el Worker rechazó 401" 1
sembrar true false "$ERROR_500";         caso "llegada con 500 — aísla que no se mira sólo el 401" 1
sembrar true false "$UA_PROPIO";         caso "id del remitente pero user-agent NUESTRO" 1
sembrar true false "$OTRO_WID";          caso "llegada buena de OTRO webhook-id que el nombrado" 1
sembrar true false "$STATUS_CADENA";     caso "status como CADENA '202', no entero" 1
sembrar false false SIN "C05-37 verde";  caso "prosa adelantada · minúsculas (aísla el .lower())" 1
sembrar true false "$BUENA" "cadena de entrega CERRADA"; caso "prosa adelantada · 2.ª frase con sandbox YA verde" 1
sembrar true true "$BUENA";              caso "producción verde con evidencia de SANDBOX" 1

# ── NO HE PODIDO MIRAR ───────────────────────────────────────────────────────────────────────
sembrar true false "$OTRA_SONDA_PROPIA" "" msg_s1056replay_mia; caso "sonda propia con OTRA convención de nombre" 1
sembrar true false "$URL_CONTRADICE";  caso "cabecera del host bueno pero URL de otro host" 1
sembrar true false "$BUENA" "" msg_REAL ev/otra/002.json; caso "no-disparo · la traza vive en otra ruta bajo trace_dir" 0
# ⛔ CONTROL POSITIVO DE PRODUCCIÓN. Sin él, un mutante que hiciera fallar SIEMPRE con
# production_green=true sobrevive, y la batería celebra que producción no pueda cerrar nunca.
sembrar false true "$BUENA_PROD" "" msg_PROD; caso "no-disparo · PRODUCCIÓN verde con su propia evidencia" 0
sembrar true false "$BUENA" "ya recibe de Svix"; caso "prosa adelantada · TERCERA frase de la lista" 1
sembrar true false "$BUENA" "sin bloqueos pendientes"; caso "prosa adelantada · CUARTA frase de la lista" 1
sembrar true false NOFILE;      caso "verde y la traza NOMBRADA no existe" 2
sembrar true false "$SIN_STATUS";      caso "evento por lo demás válido SIN status — traza incompleta" 2
OLV_SCHEMA="c05-37-closure/v99" sembrar true false "$BUENA"; caso "esquema desconocido en el contrato" 2
sembrar true false "$BUENA" "" msg_REAL fuera/001.json; caso "la traza NOMBRADA vive FUERA de trace_dir" 2
sembrar true false "$BUENA"; sed -i 's/"sandbox_green": true/"sandbox_green": "si"/' "$T/design/c05-37-closure.json"; caso "sandbox_green no es booleano" 2
sembrar true false "$LISTA_ANIDADA_DESPUES"; caso "forma mala DESPUÉS del evento bueno — el orden no perdona" 2
sembrar true false NOEVID;      caso "verde SIN evidencia nombrada" 2
sembrar true false "$NO_ES_LISTA";    caso "la traza no es una lista" 2
sembrar true false "$HEADERS_RAROS";  caso "headers de tipo inesperado — no una excepción" 2
sembrar true false "$EVENTO_RARO";    caso "evento que no es un objeto — no una excepción" 2

printf 'test-c05-37-closure: %d pasadas, %d fallos\n' "$pass" "$failn"
[ "$failn" -eq 0 ] || exit 1
