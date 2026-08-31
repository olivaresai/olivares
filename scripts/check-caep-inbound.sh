#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ECO-04: the inbound CAEP receiver is the open-core Authenticator
# method, not a sold add-on. Three answers: 0 clean · 1 finding · 2
# could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-caep-inbound: FAIL — $*" >&2; exit 1; }
cannot() { say "check-caep-inbound: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

AUTH="core/auth/caep_events.go"
GATE="cmd/olivares/caeptransmitgate.go"
[ -r "$AUTH" ] || cannot "missing $AUTH"
[ -r "$GATE" ] || cannot "missing $GATE"

grep -q 'func (a \*Authenticator) CAEPReceiveEvent' "$AUTH" \
  || fail "CAEPReceiveEvent is gone — inbound receiver missing"
grep -q 'ErrCAEPSetDisabled' "$AUTH" \
  || fail "ErrCAEPSetDisabled is gone — deny-closed inbound door missing"
grep -q 'open-core (core/auth/caep_events.go)' "$GATE" \
  || fail "transmitter gate no longer names the inbound receiver as open-core"
# DOS defectos en la linea original, medidos el 2026-08-20:
#   grep -n 'CAEPReceiveEvent' commercial modules 2>/dev/null | grep -q .
#
#   1. Sin `-r`, grep sobre DIRECTORIOS no recorre nada: sale 1 con «Is a directory»
#      silenciado por el 2>/dev/null, asi que `grep -q .` es SIEMPRE falso y esta
#      comprobacion NO MIRA NADA. Un gate que no puede fallar no es un gate.
#   2. `<lista> | grep -q` bajo `pipefail` devuelve 141 EN EXITO cuando grep cierra
#      el tubo antes de que el productor termine de escribir.
#
# Se recorre de verdad y se captura sin tuberia.
otro="$(grep -rn 'CAEPReceiveEvent' commercial modules 2>/dev/null || true)"
if [ -n "$otro" ]; then
  fail "a second CAEPReceiveEvent appeared outside core/auth — product line split"
fi

say "check-caep-inbound: CLEAN — inbound receiver is CAEPReceiveEvent in core/auth (AGPL), not a sold add-on."
exit 0
