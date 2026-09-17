#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Batería de report-prep-gate-pins.sh. Cada casilla nombra el defecto que reproduce, y los cuatro
# mutantes son los cuatro que se pagaron a mano el 2026-08-21 antes de que existiera el guion.
set -euo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
export LC_ALL=C

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GUION="$RAIZ/scripts/report-prep-gate-pins.sh"
[ -f "$GUION" ] || { echo "test-prep-gate-pins: ⛔ no encuentro el sujeto"; exit 2; }
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pins-bat.XXXXXX")" || exit 2
trap 'rm -rf "$TMP"' EXIT
pasa=0; falla=0

# `cd ""` SALE 0 y trabajaria en el repo real: el guardia es la primera casilla, no una nota.
banco() {
	local d="$TMP/$1"
	case "$d" in "$TMP"/*) : ;; *) echo "⛔ banco fuera del sandbox"; exit 2 ;; esac
	mkdir -p "$d/scripts" || return 1
	git -c init.defaultBranch=main init -q "$d" >/dev/null 2>&1 || return 1
	git -C "$d" config user.email t@t; git -C "$d" config user.name t
	git -C "$d" config commit.gpgsign false
	printf '%s' "$d"
}

# Un gate de preparacion de juguete con la MISMA forma que los reales: variable con DIGITOS en el
# nombre, ruta de codigo por defecto, y una afirmacion de cada polaridad.
gate() { # $1 dir · $2 nombre · $3 ruta · $4 simbolo prohibido
	cat > "$1/scripts/check-$2-prep.sh" <<GATE
#!/usr/bin/env bash
set -euo pipefail
ROOT="\${OLIVARES_ROOT:-\$(cd "\$(dirname "\${BASH_SOURCE[0]}")/.." && pwd)}"
SRC1X="\${OLIVARES_$(printf '%s' "$2" | tr 'a-z-' 'A-Z_')1X_SRC:-$3}"
cd "\$ROOT"
fail() { printf 'FAIL %s\n' "\$1" >&2; exit 1; }
grep -q 'anchor_must_stay' "\$SRC1X" || fail "source lost anchor_must_stay"
grep -q '$4' "\$SRC1X" && fail "$4 landed — this HOLD lote does not apply $2"
exit 0
GATE
}

corre() { # $1 dir · resto: argumentos
	local d="$1"; shift
	(cd "$d" && OLIVARES_ROOT="$d" timeout 300 bash "$GUION" "$@" 2>&1)
}

comprueba() { # $1 nombre · $2 rc esperado · $3 patron (vacio = no se mira) · $4 dir · resto args
	local n="$1" rc_esp="$2" pat="$3" d="$4"; shift 4
	local out rc
	out="$(corre "$d" "$@")" && rc=0 || rc=$?
	local ok=1
	[ "$rc" = "$rc_esp" ] || ok=0
	# HERE-STRING, NO TUBERIA: `printf | grep -q` devuelve 141 EN EXITO bajo pipefail.
	if [ -n "$pat" ] && ! grep -q -- "$pat" <<<"$out"; then ok=0; fi
	if [ "$ok" = 1 ]; then printf '  ok   %-56s rc=%s\n' "$n" "$rc"; pasa=$((pasa+1))
	else printf '  FALLA %-55s rc=%s (esperaba %s) pat=%s\n' "$n" "$rc" "$rc_esp" "${pat:-<ninguno>}"
		printf '%s\n' "$out" | sed 's/^/        /' | head -8; falla=$((falla+1)); fi
}

echo "report-prep-gate-pins: batería"

# ── 1. Censo: la variable del gate lleva DIGITOS. Con `[A-Z_]+` este gate no se ve, y ese fue el
#    defecto que hizo publicar 15 donde hay 32.
d="$(banco censo)"
mkdir -p "$d/cmd"; printf 'anchor_must_stay\n' > "$d/cmd/thing.go"
gate "$d" "c99-01-digits" "cmd/thing.go" "forbidden_symbol"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -qm base --no-verify >/dev/null 2>&1
# (el censo se comprueba abajo leyendo la salida; una llamada silenciada aqui sumaba un fallo
#  invisible al contador — medido al escribir esta bateria)
out="$(OLIVARES_PINS_NO_NET=1 corre "$d")" && rc=0 || rc=$?
if grep -q '1 anclan CODIGO' <<<"$out" && [ "$rc" = 0 ]; then
	printf '  ok   %-56s rc=0\n' "un gate con DIGITOS en la variable entra en el censo"; pasa=$((pasa+1))
else printf '  FALLA %-55s rc=%s\n' "un gate con DIGITOS en la variable entra en el censo" "$rc"
	printf '%s\n' "$out" | sed 's/^/        /' | head -6; falla=$((falla+1)); fi

# ── 2. Polaridad: un gate que SOLO dice «lost …» (debe-seguir) NO puede bloquear un aterrizaje y
#    no debe contarse. Sumarlo daba 31 donde hay 20.
d="$(banco polaridad)"
mkdir -p "$d/cmd"; printf 'anchor_must_stay\n' > "$d/cmd/thing.go"
# ⛔ LA EXPLICACION VA AQUI Y NO DENTRO DEL HEREDOC, y no es estilo: el sujeto CLASIFICA ESTE
# FICHERO POR SU TEXTO. Mi primera version puso el comentario dentro, y el comentario citaba la
# frase que el filtro busca — asi que el gate de juguete PASABA el filtro que debia excluirlo y la
# casilla medía lo contrario de lo que dice. Una anotacion dentro de la cadena comparada.
#
# Que prueba: «lost HOLD» casa el filtro de CONGELADA; la frase de polaridad NO aparece, que es lo
# unico que separa un DEBE-SEGUIR de un NO-DEBE-ATERRIZAR. Antes escribia «anchor_must_stay», con
# guion bajo, que ni siquiera casa el filtro de congelada: la casilla se caia un paso antes y la
# polaridad no se probaba. Lo destapo el mutante M2 al SOBREVIVIR.
cat > "$d/scripts/check-c99-02-only-stay-prep.sh" <<'G2'
#!/usr/bin/env bash
set -euo pipefail
ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
SRC2X="${OLIVARES_C99_02_2X_SRC:-cmd/thing.go}"
cd "$ROOT"
grep -q 'anchor_must_stay' "$SRC2X" || { echo "FAIL source lost HOLD" >&2; exit 1; }
exit 0
G2
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -qm base --no-verify >/dev/null 2>&1
out="$(OLIVARES_PINS_NO_NET=1 corre "$d" || true)"
if grep -q '0 anclan CODIGO' <<<"$out" || grep -q 'NO HE PODIDO MIRAR' <<<"$out"; then
	printf '  ok   %-56s\n' "un gate SOLO debe-seguir NO se cuenta como bloqueante"; pasa=$((pasa+1))
else printf '  FALLA %-55s\n' "un gate SOLO debe-seguir NO se cuenta como bloqueante"
	printf '%s\n' "$out" | sed 's/^/        /' | head -6; falla=$((falla+1)); fi

# ── 3. Linea base: un gate YA ROJO sobre el arbol limpio se NOMBRA, y su rojo no acusa a nadie.
d="$(banco base)"
mkdir -p "$d/cmd"; printf 'forbidden_symbol\nanchor_must_stay\n' > "$d/cmd/thing.go"
gate "$d" "c99-03-alreadyred" "cmd/thing.go" "forbidden_symbol"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -qm base --no-verify >/dev/null 2>&1
# NO-NET a proposito: sin cola que consultar el guion sale 2, y eso es CORRECTO — lo que esta
# casilla mide es la LINEA BASE, no la fase de PRs. Mi primera version esperaba 0 y acusaba al
# sujeto de un acierto suyo.
out="$(OLIVARES_PINS_NO_NET=1 corre "$d")" && rc=0 || rc=$?
if [ "$rc" = 0 ] && grep -q 'YA ROJO sobre el arbol limpio' <<<"$out" && grep -q 'linea base — 1' <<<"$out"; then
	printf '  ok   %-56s rc=0\n' "un gate YA ROJO en la base se nombra y no acusa a una PR"; pasa=$((pasa+1))
else printf '  FALLA %-55s rc=%s\n' "un gate YA ROJO en la base se nombra y no acusa a una PR" "$rc"
	printf '%s\n' "$out" | sed 's/^/        /' | head -6; falla=$((falla+1)); fi

# ── 4. Fail-closed: sin PRs y sin `gh` no se dice «cero clavadas», se dice que no se pudo mirar.
d="$(banco sinred)"
mkdir -p "$d/cmd"; printf 'anchor_must_stay\n' > "$d/cmd/thing.go"
gate "$d" "c99-04-nonet" "cmd/thing.go" "forbidden_symbol"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -qm base --no-verify >/dev/null 2>&1
out="$( (cd "$d" && OLIVARES_ROOT="$d" PATH=/usr/bin:/bin timeout 120 bash "$GUION" 2>&1) || true)"
if grep -q 'NO HE PODIDO MIRAR' <<<"$out"; then
	printf '  ok   %-56s\n' "sin cola consultable dice NO HE PODIDO MIRAR, no «cero»"; pasa=$((pasa+1))
else printf '  FALLA %-55s\n' "sin cola consultable dice NO HE PODIDO MIRAR, no «cero»"
	printf '%s\n' "$out" | sed 's/^/        /' | head -6; falla=$((falla+1)); fi

# ── 5. El arbol sucio se RECHAZA: la medida escribe y restaura, y sobre trabajo sin commitear eso
#    es la Regla Cero al reves.
d="$(banco sucio)"
mkdir -p "$d/cmd"; printf 'anchor_must_stay\n' > "$d/cmd/thing.go"
gate "$d" "c99-05-dirty" "cmd/thing.go" "forbidden_symbol"
git -C "$d" add -A >/dev/null 2>&1; git -C "$d" commit -qm base --no-verify >/dev/null 2>&1
printf 'sin commitear\n' > "$d/cmd/otro.go"
out="$( (cd "$d" && OLIVARES_ROOT="$d" timeout 120 bash "$GUION" 999 2>&1) || true)"
if grep -q 'NO esta limpio' <<<"$out"; then
	printf '  ok   %-56s\n' "un arbol SUCIO se rechaza antes de escribir nada"; pasa=$((pasa+1))
else printf '  FALLA %-55s\n' "un arbol SUCIO se rechaza antes de escribir nada"
	printf '%s\n' "$out" | sed 's/^/        /' | head -6; falla=$((falla+1)); fi

# ── 6. Control positivo del guardia del sandbox.
if ( case "/etc" in "$TMP"/*) exit 0;; *) exit 2;; esac ) 2>/dev/null; then
	echo "  FALLA el guardia acepto /etc"; falla=$((falla+1))
else printf '  ok   %-56s\n' "el guardia rechaza una ruta fuera del sandbox"; pasa=$((pasa+1)); fi

printf 'report-prep-gate-pins: %s passed, %s failed\n' "$pasa" "$falla"
[ "$falla" -eq 0 ]
