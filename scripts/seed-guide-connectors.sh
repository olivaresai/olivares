#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# seed-guide-connectors.sh — the three connectors the integration guides name.
#
# The guides for Claude Code, Codex and Grok Build each open on a screenshot of
# Control console > Connectors showing THEIR connector already created. Those three
# names — claude-code-prod, codex-enterprise, grok-demo — exist nowhere in the tree:
# they are prose examples, and `--seed-demo` has never created them. Without this the
# console has no row to photograph and the guides' first hole cannot be filled.
#
# ⛔ WHY THE CLI AND NOT THE CONSOLE API. `PUT /v1/console/connectors` is superadmin +
#    AAL3 gated, so a harness token may or may not carry the step-up; `sources set`
#    writes the same durable roster offline and then the engine is told to reload. The
#    engine itself prints that path — "reload a running engine to apply: POST
#    /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)" —
#    so seeding while it runs is the supported flow, not a trick.
#
# ⛔ AND THE THREE FLAGS ARE NOT DECORATION. `sources set` refuses without --tenant
#    ("a source must name the business tenant its observations belong to") and without
#    --actor/--reason ("a privileged offline operation must record who and why"). It is
#    the same governance the guides document, so the seed obeys it rather than bypassing
#    it. `validate` needs neither, because it writes nothing.
set -euo pipefail

BIN="${1:?usage: seed-guide-connectors.sh <binary> <data-dir> <tenant> [port]}"
DATA="${2:?data dir}"
TENANT="${3:?tenant id}"
PORT="${4:-}"

# kind values verified against the binary (`sources validate`, rc=0 for all three):
# claude runs OUT-OF-PROCESS, so its identity is not decided offline — that is a note,
# not a failure, and it is why its line reads differently from the other two.
# ⛔ `--poll-seconds` NO ES ADORNO: ES LA DIFERENCIA ENTRE «Running» Y «Stopped» EN LA FOTO.
#    Medido: con `poll 0s` la fuente se abre, corre UNA vez y termina — la consola la pinta
#    «Stopped», que es la captura que the planner rechazo para las guias de Codex y Grok. `otlp_http`
#    solo NO basta (comprobado: el campo se aplica, `config.otlp_http: - → true`, y la fila
#    seguia parada). Una fuente con intervalo queda registrada y vuelve, que es lo que la
#    consola llama «Running».
#    Y el valor no me lo invento: la propia guia de Grok documenta `interval 60`, y la de Codex
#    habla del «batch interval». La foto y el texto dicen lo mismo por construccion.
seed() {
  local name="$1" kind="$2" poll="$3"; shift 3
  local cfg=()
  for kv in "$@"; do cfg+=(--config "$kv"); done
  echo "==> seeding connector $name ($kind, poll ${poll}s)"
  "$BIN" sources set --name "$name" --kind "$kind" --tenant "$TENANT" \
    --poll-seconds "$poll" \
    --actor docs-captures --reason "seed the connector the $kind integration guide photographs" \
    "${cfg[@]}" --data-dir "$DATA"
}

# ⛔ NO claude SOURCE HERE, AND THE REASON IS MEASURED, NOT STYLISTIC. Seeding one made the
#    engine reject the harness's OWN claude source on reload — "connector identity
#    \"olivares.claude\" is already used by source \"claude-code-prod\" (only one instance per
#    connector identity)" — so the OTLP receiver never came up and /adoption photographed at
#    ZERO for the whole 71-view set. The harness registers exactly one claude source, further
#    down, WITH the OTLP config; it now carries the guide's name so one row serves both.
#    codex and grok collide with nothing: zero rejections for either.
# ⛔ CON RECEPTOR OTLP VIVO, Y ESA ES LA DIFERENCIA ENTRE «Running» Y «Stopped» EN LA FOTO.
#    La primera pasada los sembro sin el y la captura que ABRE las guias de Codex y Grok salio
#    con sus conectores en «Stopped» — una guia de integracion cuya imagen dice «parado»
#    transmite lo contrario de lo que ensena (veredicto de the planner, 2026-08-31).
#    Tres identidades DISTINTAS (`olivares.codex`, `olivares.grok`, `olivares.claude`) no
#    colisionan entre si: lo que el motor rechaza es repetir UNA, que es justo el fallo que
#    cometi sembrando un segundo `claude`. Cada receptor toma su propio puerto.
CODEX_OTLP="${OTLP_BASE:-8496}"
seed codex-enterprise codex 60 \
  workspace_id=ws_demo_enterprise \
  otlp_http=true \
  "otlp_http_addr=127.0.0.1:$((CODEX_OTLP + 1))"
seed grok-demo grok 60 \
  config_path=/etc/grok/config.toml \
  requirements_path=/etc/grok/requirements.toml \
  disabled_hooks_path=/etc/grok/disabled-hooks \
  otlp_http=true \
  "otlp_http_addr=127.0.0.1:$((CODEX_OTLP + 2))"

# Apply to the RUNNING engine. Without this the roster is on disk and the console still
# shows the pre-seed state, which is the failure that looks like "the seed did nothing".
# ⛔ EL SIGHUP ES LO QUE HACE NACER LOS RECEPTORES, no una cortesia. El endpoint de recarga
#    esta gatead0 por AAL3 y desde aqui puede contestar `step_up_required`; la senal no. Sin
#    ella los conectores quedan en el roster pero SIN escuchar, que en la consola se lee
#    «Stopped» — exactamente la foto que the planner rechazo.
if [ -n "$PORT" ]; then
  echo "==> reloading the running engine so the console sees them"
  curl -sf -X POST "http://127.0.0.1:$PORT/v1/console/runtime/reload" \
    -H "Authorization: Bearer ${OLIVARES_TOKEN:-}" >/dev/null 2>&1 \
    || echo "   (reload endpoint refused; falling back to SIGHUP)" >&2
fi
if [ -n "${OLIVARES_ENGINE_PID:-}" ]; then
  kill -HUP "$OLIVARES_ENGINE_PID" 2>/dev/null || true
  # ⛔ AQUI HABIA UN BUCLE QUE NO PODIA ACERTAR NUNCA: esperaba «Running» en la salida de
  #    `sources ls`, y esa tabla imprime NAME/KIND/TENANT/MODE/POLL/ENABLED — no hay columna de
  #    estado. Running/Stopped es concepto de la CONSOLA, no del CLI. El bucle agotaba sus 20 s
  #    en todas las corridas y no informaba de nada: un «no» que en realidad era «no puedo
  #    mirar». Se sustituye por una espera fija y honesta a que el motor termine de recargar.
  sleep 3
  echo "   roster reloaded (status is a console concept; the capture is what verifies it)"
fi
