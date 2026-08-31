#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Que el bloque de la FOTO DEL ARBOL del pre-push no deje temporales en /tmp.
#
# Por que existe: el 2026-08-22 habia 9.691 ficheros `prepush-treesnap.*` en /tmp, TODOS de 0
# bytes. El `mktemp` estaba por ENCIMA de su guarda `[ -x scripts/check-tree-untouched.sh ]`, y esa
# guarda mira el arbol de LA RAMA QUE SE EMPUJA: ese dia las 40 ramas abiertas eran anteriores al
# script, asi que fallaba en todas y el `if` caia sin `else` que recogiera. Un fichero por push y
# por carril.
#
# Como se prueba: se EXTRAE el bloque del hook y se ejecuta aislado, con TMPDIR propio, en las tres
# situaciones que decide la guarda. Y con CONTROL NEGATIVO: se restaura la forma vieja y el caso 1
# tiene que volver a fugar. Si no fuga, este test no ve el defecto que existe para ver, y sale 1.
set -uo pipefail

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOOK="$RAIZ/.githooks/pre-push"
fallos=0
ok() { printf '  ok   %s\n' "$1"; }
mal() { printf '  MAL  %s — %s\n' "$1" "$2"; fallos=$((fallos + 1)); }

[ -r "$HOOK" ] || { echo "test-prepush-treesnap: 2 NO PUDE MIRAR — sin $HOOK" >&2; exit 2; }

# --- extraccion del bloque -------------------------------------------------------------------
# Del primer `OLIVARES_TREE_SNAP=` hasta el `fi` que precede a `task lint:mid-operation`.
fin=$(grep -n '^task lint:mid-operation$' "$HOOK" | head -1 | cut -d: -f1)
ini=$(grep -n '^OLIVARES_TREE_SNAP=' "$HOOK" | head -1 | cut -d: -f1)
if [ -z "${fin:-}" ] || [ -z "${ini:-}" ] || [ "$ini" -ge "$fin" ]; then
	echo "test-prepush-treesnap: 2 NO PUDE MIRAR — no localizo el bloque (ini='${ini:-}' fin='${fin:-}')" >&2
	exit 2
fi
BLOQUE="$(sed -n "${ini},$((fin - 1))p" "$HOOK")"
case "$BLOQUE" in
*prepush-treesnap*) : ;;
*) echo "test-prepush-treesnap: 2 NO PUDE MIRAR — el bloque extraido no crea la foto" >&2; exit 2 ;;
esac

# --- arnes -----------------------------------------------------------------------------------
# Ejecuta el bloque en un directorio de trabajo propio y devuelve cuantos ficheros quedan en su
# TMPDIR DESPUES de que el subshell salga (o sea, con los traps ya disparados).
corre() { # $1=bloque  $2=modo del script (falta|ok|falla)
	local bloque="$1" modo="$2" caja tmp n
	caja="$(mktemp -d)" || return 99
	tmp="$caja/tmp"
	mkdir -p "$tmp" "$caja/scripts"
	case "$modo" in
	ok) printf '#!/usr/bin/env bash\ncase "${1:-}" in --snapshot) printf "foto\\n" > "$2";; --compare) : ;; esac\nexit 0\n' > "$caja/scripts/check-tree-untouched.sh"; chmod +x "$caja/scripts/check-tree-untouched.sh" ;;
	falla) printf '#!/usr/bin/env bash\nexit 1\n' > "$caja/scripts/check-tree-untouched.sh"; chmod +x "$caja/scripts/check-tree-untouched.sh" ;;
	falta) : ;;
	esac
	{
		printf 'set -euo pipefail\npush_refs_file=""\ntrap %s EXIT\n' "'rm -f \"\${push_refs_file:-}\"'"
		printf '%s\n' "$bloque"
	} > "$caja/prueba.sh"
	# ⛔ Un mutante que NO COMPILA tampoco fuga, y saldria "ok" por la razon equivocada.
	if ! bash -n "$caja/prueba.sh" 2>/dev/null; then rm -rf -- "$caja"; printf 'NOAPLICA'; return 0; fi
	( cd "$caja" && TMPDIR="$tmp" bash "$caja/prueba.sh" >/dev/null 2>&1 )
	n=$(find "$tmp" -maxdepth 1 -name 'prepush-treesnap*' 2>/dev/null | wc -l)
	rm -rf -- "$caja"
	printf '%s' "$n"
}

echo "test-prepush-treesnap: el bloque de la foto no deja temporales"

n=$(corre "$BLOQUE" falta)
[ "$n" = "0" ] && ok "sin check-tree-untouched.sh (toda rama anterior al 08-19): 0 temporales" \
	|| mal "sin check-tree-untouched.sh" "quedan $n temporales — es la fuga de los 9.691"

n=$(corre "$BLOQUE" ok)
[ "$n" = "0" ] && ok "con el script y foto correcta: el trap recoge la foto" \
	|| mal "con el script y foto correcta" "quedan $n temporales"

n=$(corre "$BLOQUE" falla)
[ "$n" = "0" ] && ok "con el script y foto fallida: lo recoge el else" \
	|| mal "con el script y foto fallida" "quedan $n temporales"

# --- CONTROL NEGATIVO ------------------------------------------------------------------------
# La forma vieja: el mktemp por encima de la guarda. Tiene que fugar en el primer caso.
VIEJO="$(printf '%s\n' "$BLOQUE" | awk '
	$0 == "OLIVARES_TREE_SNAP=\"\"" { next }
	$0 == "if [ -x scripts/check-tree-untouched.sh ]; then" { dentro = 1; next }
	dentro == 1 && $0 ~ /mktemp/ { sub(/^\t/, "", $0); print; next }
	dentro == 1 && $0 == "fi" { dentro = 0; next }
	$0 == "if [ -n \"$OLIVARES_TREE_SNAP\" ]; then" {
		print "if [ -n \"$OLIVARES_TREE_SNAP\" ] && [ -x scripts/check-tree-untouched.sh ]; then"
		next
	}
	{ print }
')"
case "$VIEJO" in
*'&& [ -x scripts/check-tree-untouched.sh ]'*) : ;;
*) echo "test-prepush-treesnap: 2 NO PUDE MIRAR — no he sabido reconstruir la forma vieja" >&2; exit 2 ;;
esac

n=$(corre "$VIEJO" falta)
[ "$n" = "1" ] && ok "CONTROL NEGATIVO: la forma vieja fuga 1 fichero — el test SI ve el defecto" \
	|| mal "CONTROL NEGATIVO" "la forma vieja dejo $n (esperaba 1): este test no ve el defecto que existe para ver"

if [ "$fallos" -eq 0 ]; then
	echo "test-prepush-treesnap: 0 CLEAN — 4 casos"
	exit 0
fi
echo "test-prepush-treesnap: 1 — $fallos caso(s) mal"
exit 1
