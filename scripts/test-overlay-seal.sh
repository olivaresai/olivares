#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Banco de a repository gate: el sellador y el lector, con sus mutantes EJECUTABLES.
#
# ⛔ POR QUE ESTE FICHERO EXISTE, dicho sin adornos: la primera version de este lote **no tenia
# banco**. Publique una tabla de resultados (0/7, 7/7...) en la ficha y la presente como si fuera
# un testigo. El contraste `sol max` lo caza (A-03): cero ficheros de test tocados, cero tests que
# mencionen el sello. Peor: mis corridas manuales pasaban **porque yo habia dejado un sello a mano**
# en el arbol. Es la clase que yo mismo documente esta noche —«una bateria que hereda su entorno»—
# cometida dentro del arreglo de un problema de frescura. Un resultado que no se puede volver a
# correr no es un testigo: es una afirmacion.
#
# Cada mutante mueve UNA sola dimension, y cada uno lleva su control de que la movio.

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ⛔ AISLAMIENTO DE ENTORNO GIT, y aqui no es ceremonia: la linea de abajo hace `git init "$ENT"`.
# Git EXPORTA `GIT_DIR` a los hooks desde todo worktree ENLAZADO —o sea, desde cualquier sesion en
# paralelo— y `GIT_DIR` MANDA SOBRE `-C`: sin sanear, ese `git init` inicializa el repositorio VIVO
# y los tres `git -C "$ENT" commit` de las lineas siguientes aterrizan sus commits de fixture en la
# rama de quien este trabajando.
#
# MEDIDO el 2026-08-30 contra un repositorio desechable, con este mismo fichero y sin esta linea:
# la bateria pasa de 17/0 a 13/4 y el repositorio envenenado pasa de UN commit a TRES, con HEAD
# movido. Con la linea puesta: 17/0 y el repositorio intacto.
#
# ⛔ Y NO LO CAZABA EL RATCHET, que es la mitad que hay que saber: `lint:git-env` daba este fichero
# por bueno —«isolates by REFUSING a poisoned environment (probed, sandbox untouched)»— porque su
# detector de daño mira `core.bare` y `ls -A` del sandbox, y ninguno de los dos cambia cuando lo
# unico que pasa es que te escriben commits dentro. El commit siguiente le da ojos a ese detector.
# Falla cerrado: no poder aislar es «no he podido», nunca «no hacia falta».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
olivares_git_env_isolate
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
case "$_tmp_base" in "$ROOT" | "$ROOT"/*) _tmp_base=/workspace/.olivares-tmptest ;; esac
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/ovlseal.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

# ── fixture: un overlay de juguete con su origin/main, y un arbol que solo tiene lo necesario ──
ENT="$TMP/ent"
git init -q "$ENT"
git -C "$ENT" -c user.email=t@t -c user.name=t commit -q --allow-empty -m one
OLD="$(git -C "$ENT" rev-parse HEAD)"
git -C "$ENT" -c user.email=t@t -c user.name=t commit -q --allow-empty -m two
NEW="$(git -C "$ENT" rev-parse HEAD)"
git -C "$ENT" update-ref refs/remotes/origin/main "$NEW"
git -C "$ENT" remote add origin "$ENT" 2>/dev/null || true

TREE="$TMP/tree"
mkdir -p "$TREE/scripts/lib"
cp "$ROOT/scripts/lib/overlay-seal.sh" "$TREE/scripts/lib/"
SEAL="$TREE/.overlay-fetch-seal"

# Un lector de juguete: hace lo que hacen los siete — resuelve el ref y exige el sello.
cat >"$TREE/scripts/check-toy-reader.sh" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
ROOT="${OLIVARES_ROOT:?}"
cannot() { echo "check-toy-reader: COULD NOT LOOK — $*" >&2; exit 2; }
ENT="${OLIVARES_ENT_DIR:?}"
. "$ROOT/scripts/lib/overlay-seal.sh" || cannot "cannot load the seal lib"
overlay_seal_require "$ENT" || cannot "$OVERLAY_SEAL_WHY"
echo "check-toy-reader: CLEAN"
EOF
chmod +x "$TREE/scripts/check-toy-reader.sh"

lee() { # $1 = act id que dice tener este acto
	local rc=0
	OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_ACT_ID="${1:-ACT1}" \
		OLIVARES_OVERLAY_SEAL="$SEAL" \
		bash "$TREE/scripts/check-toy-reader.sh" >"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc"
}
sello() { printf '%s\n' "$1" >"$SEAL"; }

# ── POSITIVO ───────────────────────────────────────────────────────────────────────────────────
sello "$(date -u +%s) ACT1 $NEW rc=0"
[ "$(lee ACT1)" = 0 ] && ok "positive: seal from this operation, matching ref -> compares (0)" \
	|| bad "positive case should return 0 ($(cat "$TMP/err"))"

# ── MUTANTE 1 · sello VIEJO (mueve SOLO la edad) ───────────────────────────────────────────────
sello "$(( $(date -u +%s) - 100000 )) ACT1 $NEW rc=0"
[ "$(lee ACT1)" = 2 ] && ok "age mutant: an old seal returns 2" || bad "old seal should return 2"

# ── MUTANTE 2 · otro ACTO (mueve SOLO el id; edad y SHA correctos) ─────────────────────────────
sello "$(date -u +%s) ACT0 $NEW rc=0"
r="$(lee ACT1)"
if [ "$r" = 2 ] && grep -q 'ANOTHER act' "$TMP/err"; then
	ok "operation mutant: a FRESH seal from the previous operation returns 2 through its own check"
else
	bad "seal from another operation should return 2 through its own check ($r $(cat "$TMP/err"))"
fi

# ── MUTANTE 3 · ref DISTINTO (mueve SOLO el SHA) ───────────────────────────────────────────────
sello "$(date -u +%s) ACT1 $OLD rc=0"
r="$(lee ACT1)"
if [ "$r" = 2 ] && grep -q 'does NOT describe the clone' "$TMP/err"; then
	ok "ref mutant: fresh seal from THIS operation naming another ref returns 2 through its own check"
else
	bad "different ref should return 2 through its own check ($r $(cat "$TMP/err"))"
fi

# ── MUTANTE 4 · fetch FALLIDO sellado (mueve SOLO el rc) ───────────────────────────────────────
sello "$(date -u +%s) ACT1 $NEW rc=128"
[ "$(lee ACT1)" = 2 ] && ok "rc mutant: a seal recording a failed fetch returns 2" \
	|| bad "rc!=0 should return 2"

# ── MUTANTE 5 · SIN sello ──────────────────────────────────────────────────────────────────────
rm -f "$SEAL"
[ "$(lee ACT1)" = 2 ] && ok "absence mutant: no seal returns 2" || bad "no seal should return 2"

# ── MUTANTE 6 · sello MALFORMADO ───────────────────────────────────────────────────────────────
sello "esto no es un sello"
[ "$(lee ACT1)" = 2 ] && ok "shape mutant: malformed seal returns 2" || bad "malformed seal should return 2"

# ── EL SELLADOR: exito, y que TODO fallo queda sellado ─────────────────────────────────────────
cp "$ROOT/scripts/fetch-overlay-seal.sh" "$TREE/scripts/"
rm -f "$SEAL"
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_ACT_ID=ACT1 OLIVARES_OVERLAY_SEAL="$SEAL" \
	bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1
if [ -s "$SEAL" ] && grep -q " ACT1 .* rc=0" "$SEAL"; then
	ok "sealer: writes a seal for this operation with rc=0"
else
	bad "sealer did not leave a valid seal ($(cat "$SEAL" 2>/dev/null))"
fi
[ "$(lee ACT1)" = 0 ] && ok "sealer + reader: the pair works end to end" \
	|| bad "after sealing, the reader should return 0"

# ⛔ El control de A-02: un clon INVALIDO no puede dejar el sello ANTERIOR en pie.
sello "$(date -u +%s) ACT1 $NEW rc=0"
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$TMP/no-existe" OLIVARES_ACT_ID=ACT1 \
	OLIVARES_OVERLAY_SEAL="$SEAL" bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1
rc=$?
if [ "$rc" = 2 ] && ! grep -q 'rc=0' "$SEAL"; then
	ok "A-02: invalid clone exits 2 and SEALS the failure (does not retain the previous good seal)"
else
	bad "A-02: previous seal survived a failure ($rc · $(cat "$SEAL"))"
fi

# ⛔ MUTANTE DE RUTA AUSENTE (A-02, segunda vuelta del contraste). Hasta ahora el banco solo
# mutaba la VARIABLE; si el DOC nombra un clon hermano y esa ruta no existe, la version anterior lo
# trataba como «no nombrado», salia 0 y dejaba el sello ANTERIOR en pie. Nombrar es nombrar, lo haga
# la variable o el documento.
mkdir -p "$TREE/design"
printf 'sibling-clone-dir: no-existe-este-clon\n' >"$TREE/design/INT-12-NO-LAND-ENT58-2026-08-19.md"
sello "$(date -u +%s) ACT1 $NEW rc=0"
rc=0
OLIVARES_ROOT="$TREE" OLIVARES_ACT_ID=ACT1 OLIVARES_OVERLAY_SEAL="$SEAL" \
	env -u OLIVARES_ENT_DIR bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1 || rc=$?
if [ "$rc" = 2 ] && ! grep -q 'rc=0' "$SEAL"; then
	ok "A-02 path: missing clone NAMED BY THE DOC seals failure (does not retain the good seal)"
else
	bad "A-02 path: previous seal survived a missing path ($rc · $(cat "$SEAL"))"
fi
rm -f "$TREE/design/INT-12-NO-LAND-ENT58-2026-08-19.md"

# ⛔ MUTANTE DE ACTO REPETIDO (A-04, segunda vuelta). El caso que la EDAD no distingue: DOS actos
# consecutivos con el MISMO HEAD. Si el id se derivara del commit, el sello del primero pasaria por
# fresco en el segundo y el ref no se habria vuelto a traer. Con un nonce por corrida, el segundo
# acto NO acepta el sello del primero hasta que resella.
rm -f "$SEAL"
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_ACT_ID="HEADX-pid1-1000" \
	OLIVARES_OVERLAY_SEAL="$SEAL" bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1
if [ "$(lee 'HEADX-pid1-1000')" = 0 ]; then
	ok "operation 1: seals and its own reader accepts it"
else
	bad "operation 1 should accept its seal"
fi
# Segundo acto, MISMO HEAD, nonce distinto — y sin resellar.
if [ "$(lee 'HEADX-pid2-2000')" = 2 ]; then
	ok "operation 2 with the SAME HEAD does not inherit freshness from operation 1 (nonce, not commit)"
else
	bad "two operations with the same HEAD shared freshness: the ID is not a nonce"
fi

# Y sin id en el entorno, ni escritor ni lector inventan uno.
rm -f "$SEAL"
rc=0
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_OVERLAY_SEAL="$SEAL" \
	env -u OLIVARES_ACT_ID bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] && ok "without an operation ID, the sealer refuses (2)" || bad "without an ID, should refuse ($rc)"
sello "$(date -u +%s) ACT1 $NEW rc=0"
rc=0
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_OVERLAY_SEAL="$SEAL" \
	env -u OLIVARES_ACT_ID bash "$TREE/scripts/check-toy-reader.sh" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] && ok "without an operation ID, the reader refuses (2)" || bad "without an ID, the reader should refuse ($rc)"

# ⛔ MUTANTE DE ENTORNO HEREDADO (A-04, TERCERA vuelta del contraste). Este es el que faltaba y el
# que explica por que faltaba: mis mutantes de acto asignaban DOS IDS DISTINTOS A MANO, asi que
# probaban al LECTOR y jamas al GENERADOR. Con `${OLIVARES_ACT_ID:-...}` en el gancho, un id
# heredado del entorno sobrevivia y dos corridas compartian acto — y mi banco salia verde igual.
# Ahora se ejerce el generador REAL con un id ya puesto en el entorno.
. "$ROOT/scripts/lib/act-id.sh" || bad "cannot load scripts/lib/act-id.sh"
(
	export OLIVARES_ACT_ID="ACTO_VIEJO_HEREDADO"
	id1="$(olivares_nuevo_act_id)"
	id2="$(olivares_nuevo_act_id)"
	[ "$id1" != "ACTO_VIEJO_HEREDADO" ] || exit 11
	[ "$id2" != "ACTO_VIEJO_HEREDADO" ] || exit 12
	[ "$id1" != "$id2" ] || exit 13
)
case $? in
0) ok "A-04 environment: generator IGNORES the inherited ID and does not repeat across runs" ;;
11 | 12) bad "A-04 environment: generator returned the INHERITED ID" ;;
13) bad "A-04 environment: consecutive runs returned the SAME ID (not a nonce)" ;;
*) bad "A-04 environment: could not exercise the generator" ;;
esac

# Y la consecuencia que de verdad importa: con dos ids de corrida distintos, la segunda NO hereda
# la frescura de la primera aunque el HEAD sea el mismo.
rm -f "$SEAL"
_i1="$(olivares_nuevo_act_id)"; _i2="$(olivares_nuevo_act_id)"
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_ACT_ID="$_i1" \
	OLIVARES_OVERLAY_SEAL="$SEAL" bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1
if [ "$(lee "$_i1")" = 0 ] && [ "$(lee "$_i2")" = 2 ]; then
	ok "A-04 environment: run 2 does not inherit freshness from run 1 (generator IDs)"
else
	bad "A-04 environment: two generator runs shared freshness"
fi

echo
echo "test-overlay-seal: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
