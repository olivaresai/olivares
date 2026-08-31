#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Deterministic, zero-network Claude stand-in for the disposable documentation estate.
#
# The capture harness must create a REAL sessions.run through the product API so /agentops can
# render its launched-only tabs. Pointing that launch at the operator's real `claude` would be
# non-deterministic and could consume an external account. This tiny process speaks the minimum
# stream-json envelope the real procRunner already understands and stays alive until the engine
# closes stdin. It is selected only by scripts/docs-captures.sh through
# OLIVARES_SESSION_RUNTIME_CLAUDE_BIN; product deployments never select it.

trap 'exit 0' TERM

# This is the observed live session seeded by cmd/olivares/seed/seed.go. The provider session id is
# the join key between observations and operated runs (modules/sessions/runtime_api.go:89-108).
# scripts/test-demo-agent.sh fails before the expensive build if the three copies drift.
# ⛔ EL ID FIJO ES LA RAZON DE QUE /sessions NO PASE DE UNA FILA. Cada lanzamiento emitia
#    ESTE MISMO `session_id`, asi que el motor colapsaba TODOS los runs en una sola sesion:
#    medido con cinco lanzamientos que dieron 6 runs y 1 fila, con el nombre de la ultima.
#
# ⛔ Y NO SE PUEDE CAMBIAR POR LAS BUENAS. La red que lo sujeta es `scripts/test-demo-agent.sh`
#    (`lint:demo-agent`), y NO fija el literal: exige que TRES sitios digan lo mismo —
#    `SessionLive` en `cmd/olivares/seed/seed.go`, el `SID=` de ESTE guion, y
#    `DEMO_LIVE_SESSION` en `scripts/seed-demo-work.py` (`test-demo-agent.sh:28-30,41`).
#    Cambiar el valor aqui a secas rompe esa concordancia.
#
#    ⚠ AQUI DECIA `web/e2e/console-func-l6.spec.ts`, Y ERA FALSO. Lo midio (`c275f1da4`):
#      esa spec NO menciona `demo-agent` ni `DEMO_SESSION` **ni una vez** — su `sess-coder-7a3f`
#      viene del SEED (`seed/seed.go:72`), no de este guion, asi que tocar esto no la movia.
#      Un comentario que nombra la guarda equivocada es PEOR que ninguno: el siguiente que lo
#      lea se creera cubierto por donde no lo esta. La correccion se queda escrita, no se borra.
#
#    ⚠ Y ojo al editar: la extraccion es `sed -n 's/^SID="..."/'` ANCLADA A PRINCIPIO DE LINEA
#      (`test-demo-agent.sh:29`). Si `SID=` deja de empezar la linea, el gate no lo encuentra y
#      lo reporta como AUSENTE, no como distinto.
#    ⇒ La unicidad es OPT-IN: sin la variable, el comportamiento es exactamente el de antes.
#    ⛔ Y el NOMBRE no puede llevar prefijo reservado: `forbiddenInheritedEnvName`
#       (`modules/sessions/procrunner.go:322-327`) rechaza OLIVARES_/ANTHROPIC_/CLAUDE_CODE_,
#       asi que la sesion NO puede recibir una variable con esos prefijos. Se llama
#       DEMO_SESSION_UNIQUE por eso, no por gusto.
SID="sess-coder-7a3f"
if [ "${DEMO_SESSION_UNIQUE:-0}" = "1" ]; then
  SID="sess-coder-$(od -An -N3 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n')"
  [ "$SID" = "sess-coder-" ] && SID="sess-coder-$$"
fi

# Match the real CLI's resume precedence so the fixture remains useful if the harness later resumes
# a different seeded session.
while [ "$#" -gt 0 ]; do
  case "$1" in
    --resume) SID="${2:-$SID}"; shift 2 ;;
    *) shift ;;
  esac
done

printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$SID"

while IFS= read -r _line; do
  printf '{"type":"assistant","subtype":"demo","session_id":"%s"}\n' "$SID"
done

exit 0
