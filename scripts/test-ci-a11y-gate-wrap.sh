#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Puerta de salida del wrapper a11y (corrida 33926027726, f1f530e96).
#
# Extrae el `run:` REAL de `id: a11y-gate` en mainline-ci.yml y lo ejecuta con
# `pnpm` sustituido, para que lo probado sea el script que corre en CI y no una
# copia que puede derivar. El shell es el de GitHub Actions
# (`bash --noprofile --norc -e -o pipefail`): sin pipefail el defecto no existe.
#
# Contrato, medido:
#   · un log SIN la línea REL-69 («N de M parejas miden fg == bg») y rc=0
#     → el paso sale VERDE.
#   · el mutante «quitar el guard» (el `if rc=0; exit 0` deja de ir ANTES de
#     `grep | head`) + el mismo log SIN la línea → el paso sale ROJO.
#     Eso es 33926027726: at:gate limpio, wrapper rojo porque grep no encuentra
#     una línea que un log limpio no trae.
#
# Un log CON la línea no es el falso rojo: grep tiene coincidencia y el wrapper
# viejo alcanzaba el `if rc=0`. Ese camino se mide aparte (REL-69, rc=3).
set -uo pipefail

NAME='test-ci-a11y-gate-wrap'
RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WF="$RAIZ/.github/workflows/mainline-ci.yml"
fallos=0
ok() { printf '  ok    %s\n' "$1"; }
mal() { printf '  FAIL  %s — %s\n' "$1" "$2"; fallos=$((fallos + 1)); }

[ -r "$WF" ] || { echo "$NAME: 2 COULD NOT LOOK — missing $WF" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "$NAME: 2 COULD NOT LOOK — missing python3" >&2; exit 2; }
command -v bash >/dev/null 2>&1 || { echo "$NAME: 2 COULD NOT LOOK — missing bash" >&2; exit 2; }

PASO="$(python3 - "$WF" <<'PASOEOF'
import io, os, sys
ruta = sys.argv[1]
lineas = io.open(ruta, encoding="utf-8").read().split("\n")

def por_yaml():
    if os.environ.get("OLIVARES_CI_TIMEOUTS_NO_YAML") == "1":
        return None
    try:
        import yaml
    except Exception:
        return None
    d = yaml.safe_load(io.open(ruta, encoding="utf-8"))
    for st in d["jobs"]["a11y"]["steps"]:
        if st.get("id") == "a11y-gate":
            return st["run"]
    return ""

def por_lectura_plana():
    idx = None
    for i, l in enumerate(lineas):
        if l == "        id: a11y-gate":
            if idx is not None:
                return ""
            idx = i
    if idx is None:
        return ""
    run = None
    for i in range(idx + 1, len(lineas)):
        l = lineas[i]
        if l.startswith("      - "):
            break
        if l == "        run: |":
            run = i
            break
    if run is None:
        return ""
    cuerpo = []
    for i in range(run + 1, len(lineas)):
        l = lineas[i]
        if l.strip() == "":
            cuerpo.append("")
            continue
        if not l.startswith("          "):
            break
        cuerpo.append(l[10:])
    return "\n".join(cuerpo) + "\n" if cuerpo else ""

texto = por_yaml()
if texto is None:
    texto = por_lectura_plana()
sys.stdout.write(texto or "")
PASOEOF
)" || {
	echo "$NAME: 2 COULD NOT LOOK — could not EXECUTE the workflow reader (interpreter or environment)" >&2
	exit 2
}
[ -n "$PASO" ] || {
	echo "$NAME: 2 COULD NOT LOOK — reader RAN but did not find the a11y-gate step" >&2
	exit 2
}

case "$PASO" in
*"PIPESTATUS"*) : ;;
*) echo "$NAME: 2 COULD NOT LOOK — extracted step does not read PIPESTATUS" >&2; exit 2 ;;
esac
case "$PASO" in
*'if [ "$rc" = "0" ]'*) : ;;
*) echo "$NAME: 2 COULD NOT LOOK — extracted step lacks the rc=0 guard" >&2; exit 2 ;;
esac

MUTANTE="$(PASO="$PASO" python3 - <<'MUTEOF'
import os, sys
paso = os.environ["PASO"]
old_body = (
    'if [ "$rc" = "0" ]; then\n'
    '  rm -f "$RUNNER_TEMP/ci-fail-a11y.log"\n'
    '  exit 0\n'
    'fi\n'
)
if old_body not in paso:
    sys.stderr.write("mutant: cannot find the rc=0 block in the extracted step\n")
    sys.exit(2)
mut = paso.replace(old_body, "", 1)
src = (
    'degenerado=$(grep -oE \'[0-9]+ de [0-9]+ parejas miden fg == bg\' '
    '"$RUNNER_TEMP/ci-fail-a11y.log" || true)\n'
    "degenerado=${degenerado%%$'\\n'*}"
)
dst = (
    'degenerado=$(grep -oE \'[0-9]+ de [0-9]+ parejas miden fg == bg\' '
    '"$RUNNER_TEMP/ci-fail-a11y.log" | head -1)'
)
if src not in mut:
    sys.stderr.write("mutant: cannot find the safe degenerado line in the extracted step\n")
    sys.exit(2)
mut = mut.replace(src, dst, 1)
if mut == paso:
    sys.stderr.write("mutant: text did not change\n")
    sys.exit(2)
sys.stdout.write(mut)
MUTEOF
)" || {
	echo "$NAME: 2 COULD NOT LOOK — could not construct the mutant (remove the guard)" >&2
	exit 2
}

if [ "$MUTANTE" = "$PASO" ]; then
	echo "$NAME: 2 COULD NOT LOOK — mutant is identical to the step: this test cannot distinguish them" >&2
	exit 2
fi

EXEC_DIR=""
for cand in "${TMPDIR:-}" /workspace/.olivares-tmptest "$RAIZ/.ci-a11y-gate-wrap-tmp"; do
	[ -n "$cand" ] || continue
	mkdir -p "$cand" 2>/dev/null || continue
	sonda="$cand/.sonda-exec-$$"
	printf '#!/usr/bin/env bash\nexit 7\n' > "$sonda" 2>/dev/null || continue
	chmod +x "$sonda" 2>/dev/null || { rm -f "$sonda"; continue; }
	"$sonda" >/dev/null 2>&1
	[ "$?" = "7" ] && { EXEC_DIR="$cand"; rm -f "$sonda"; break; }
	rm -f "$sonda"
done
if [ -z "$EXEC_DIR" ]; then
	echo "$NAME: 2 COULD NOT LOOK — no working directory allows execution;" >&2
	echo "$NAME:   without it, the pnpm substitute is skipped and real at:gate is measured." >&2
	exit 2
fi

LOG_SIN='wrote __at__/at-report-dark.json — 0 blocking issue(s)
  /login          [dark] dark=true h1=1 skips=0 axeBlock=0
wrote __at__/at-report-light.json — 0 blocking issue(s)
'
LOG_CON='NO STYLESHEET APPLIED [dark] — no emito veredicto de contraste.
  motivo: 1247 de 1247 parejas miden fg == bg: la sonda no está leyendo el tema
  /login          [dark] dark=true h1=1 skips=0 axeBlock=0
'

corre() {
	# $1 = script del paso; $2 = rc de pnpm; $3 = cuerpo del log (stdout de pnpm)
	local script="$1" rc_pnpm="$2" log="$3" caja rc
	caja="$(mktemp -d "$EXEC_DIR/ci-a11y-gate-wrap.XXXXXX")"
	mkdir -p "$caja/bin"
	printf '%s\n' "$log" > "$caja/fixture"
	printf '#!/usr/bin/env bash\ncat -- "$OLIVARES_A11Y_FIXTURE"\nexit "${OLIVARES_A11Y_RC}"\n' > "$caja/bin/pnpm"
	chmod +x "$caja/bin/pnpm"
	printf '%s\n' "$script" > "$caja/paso.sh"
	PATH="$caja/bin:$PATH" RUNNER_TEMP="$caja" \
		OLIVARES_A11Y_FIXTURE="$caja/fixture" OLIVARES_A11Y_RC="$rc_pnpm" \
		bash --noprofile --norc -e -o pipefail "$caja/paso.sh" >/dev/null 2>&1
	rc=$?
	rm -rf -- "$caja"
	printf '%s' "$rc"
}

echo "$NAME: a log WITHOUT the line → green step; remove the guard → same log turns red"

r="$(corre "$PASO" 0 "$LOG_SIN")"
[ "$r" = "0" ] && ok "log WITHOUT the line + rc=0 → green" \
	|| mal "log WITHOUT the line" "rc=$r (expected 0): the guard does not preserve a clean gate"

r="$(corre "$MUTANTE" 0 "$LOG_SIN")"
[ "$r" != "0" ] && ok "MUTANT (remove the guard): log WITHOUT the line → red (rc=$r)" \
	|| mal "MUTANT" "removing the guard left rc=0: this test misses 33926027726"

r="$(corre "$PASO" 0 "$LOG_CON")"
[ "$r" = "0" ] && ok "log WITH the line + rc=0 remains green (the false red was caused by its ABSENCE)" \
	|| mal "log WITH the line rc=0" "rc=$r (expected 0)"

r="$(corre "$PASO" 3 "$LOG_CON")"
[ "$r" = "0" ] && ok "REL-69: rc=3 + log WITH the line + axe=0 → green with warnings" \
	|| mal "REL-69" "rc=$r (expected 0: warning, not a product failure)"

r="$(corre "$PASO" 2 "$LOG_SIN")"
[ "$r" = "2" ] && ok "at:gate rc=2 passes through the wrapper (product failure)" \
	|| mal "rc=2" "rc=$r (expected 2): wrapper swallows the verdict"

if [ "$fallos" -eq 0 ]; then
	echo "$NAME: 0 CLEAN — 5 cases"
	exit 0
fi
echo "$NAME: 1 — $fallos failed case(s)"
exit 1
