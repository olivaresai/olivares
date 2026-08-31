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
[ "$(lee ACT1)" = 0 ] && ok "positivo: sello de este acto, ref igual -> compara (0)" \
	|| bad "el positivo deberia ser 0 ($(cat "$TMP/err"))"

# ── MUTANTE 1 · sello VIEJO (mueve SOLO la edad) ───────────────────────────────────────────────
sello "$(( $(date -u +%s) - 100000 )) ACT1 $NEW rc=0"
[ "$(lee ACT1)" = 2 ] && ok "mutante edad: un sello viejo es 2" || bad "sello viejo deberia ser 2"

# ── MUTANTE 2 · otro ACTO (mueve SOLO el id; edad y SHA correctos) ─────────────────────────────
sello "$(date -u +%s) ACT0 $NEW rc=0"
r="$(lee ACT1)"
if [ "$r" = 2 ] && grep -q 'ANOTHER act' "$TMP/err"; then
	ok "mutante acto: un sello FRESCO del acto anterior es 2, por su pata"
else
	bad "el sello de otro acto deberia ser 2 en su pata ($r $(cat "$TMP/err"))"
fi

# ── MUTANTE 3 · ref DISTINTO (mueve SOLO el SHA) ───────────────────────────────────────────────
sello "$(date -u +%s) ACT1 $OLD rc=0"
r="$(lee ACT1)"
if [ "$r" = 2 ] && grep -q 'does NOT describe the clone' "$TMP/err"; then
	ok "mutante ref: sello fresco de ESTE acto que nombra otro ref es 2, por su pata"
else
	bad "el ref distinto deberia ser 2 en su pata ($r $(cat "$TMP/err"))"
fi

# ── MUTANTE 4 · fetch FALLIDO sellado (mueve SOLO el rc) ───────────────────────────────────────
sello "$(date -u +%s) ACT1 $NEW rc=128"
[ "$(lee ACT1)" = 2 ] && ok "mutante rc: un sello que registra fetch fallido es 2" \
	|| bad "rc!=0 deberia ser 2"

# ── MUTANTE 5 · SIN sello ──────────────────────────────────────────────────────────────────────
rm -f "$SEAL"
[ "$(lee ACT1)" = 2 ] && ok "mutante ausencia: sin sello es 2" || bad "sin sello deberia ser 2"

# ── MUTANTE 6 · sello MALFORMADO ───────────────────────────────────────────────────────────────
sello "esto no es un sello"
[ "$(lee ACT1)" = 2 ] && ok "mutante forma: un sello malformado es 2" || bad "malformado deberia ser 2"

# ── EL SELLADOR: exito, y que TODO fallo queda sellado ─────────────────────────────────────────
cp "$ROOT/scripts/fetch-overlay-seal.sh" "$TREE/scripts/"
rm -f "$SEAL"
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_ACT_ID=ACT1 OLIVARES_OVERLAY_SEAL="$SEAL" \
	bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1
if [ -s "$SEAL" ] && grep -q " ACT1 .* rc=0" "$SEAL"; then
	ok "sellador: escribe un sello de este acto con rc=0"
else
	bad "el sellador no dejo un sello valido ($(cat "$SEAL" 2>/dev/null))"
fi
[ "$(lee ACT1)" = 0 ] && ok "sellador + lector: el par funciona de punta a punta" \
	|| bad "tras sellar, el lector deberia dar 0"

# ⛔ El control de A-02: un clon INVALIDO no puede dejar el sello ANTERIOR en pie.
sello "$(date -u +%s) ACT1 $NEW rc=0"
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$TMP/no-existe" OLIVARES_ACT_ID=ACT1 \
	OLIVARES_OVERLAY_SEAL="$SEAL" bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1
rc=$?
if [ "$rc" = 2 ] && ! grep -q 'rc=0' "$SEAL"; then
	ok "A-02: un clon invalido sale 2 y SELLA el fallo (no deja el sello bueno anterior)"
else
	bad "A-02: el sello anterior sobrevivio a un fallo ($rc · $(cat "$SEAL"))"
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
	ok "A-02 ruta: un clon NOMBRADO POR EL DOC y ausente sella fallo (no deja el sello bueno)"
else
	bad "A-02 ruta: el sello anterior sobrevivio a una ruta ausente ($rc · $(cat "$SEAL"))"
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
	ok "acto 1: sella y su propio lector lo acepta"
else
	bad "el acto 1 deberia aceptar su sello"
fi
# Segundo acto, MISMO HEAD, nonce distinto — y sin resellar.
if [ "$(lee 'HEADX-pid2-2000')" = 2 ]; then
	ok "acto 2 con el MISMO HEAD no hereda la frescura del acto 1 (nonce, no commit)"
else
	bad "dos actos con el mismo HEAD compartieron frescura: el id no es un nonce"
fi

# Y sin id en el entorno, ni escritor ni lector inventan uno.
rm -f "$SEAL"
rc=0
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_OVERLAY_SEAL="$SEAL" \
	env -u OLIVARES_ACT_ID bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] && ok "sin id de acto, el sellador se niega (2)" || bad "sin id deberia negarse ($rc)"
sello "$(date -u +%s) ACT1 $NEW rc=0"
rc=0
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_OVERLAY_SEAL="$SEAL" \
	env -u OLIVARES_ACT_ID bash "$TREE/scripts/check-toy-reader.sh" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] && ok "sin id de acto, el lector se niega (2)" || bad "sin id el lector deberia negarse ($rc)"

# ⛔ MUTANTE DE ENTORNO HEREDADO (A-04, TERCERA vuelta del contraste). Este es el que faltaba y el
# que explica por que faltaba: mis mutantes de acto asignaban DOS IDS DISTINTOS A MANO, asi que
# probaban al LECTOR y jamas al GENERADOR. Con `${OLIVARES_ACT_ID:-...}` en el gancho, un id
# heredado del entorno sobrevivia y dos corridas compartian acto — y mi banco salia verde igual.
# Ahora se ejerce el generador REAL con un id ya puesto en el entorno.
. "$ROOT/scripts/lib/act-id.sh" || bad "no puedo cargar scripts/lib/act-id.sh"
(
	export OLIVARES_ACT_ID="ACTO_VIEJO_HEREDADO"
	id1="$(olivares_nuevo_act_id)"
	id2="$(olivares_nuevo_act_id)"
	[ "$id1" != "ACTO_VIEJO_HEREDADO" ] || exit 11
	[ "$id2" != "ACTO_VIEJO_HEREDADO" ] || exit 12
	[ "$id1" != "$id2" ] || exit 13
)
case $? in
0) ok "A-04 entorno: el generador IGNORA el id heredado y no repite entre corridas" ;;
11 | 12) bad "A-04 entorno: el generador devolvio el id HEREDADO" ;;
13) bad "A-04 entorno: dos corridas seguidas dieron el MISMO id (no es un nonce)" ;;
*) bad "A-04 entorno: el generador no se pudo ejercer" ;;
esac

# Y la consecuencia que de verdad importa: con dos ids de corrida distintos, la segunda NO hereda
# la frescura de la primera aunque el HEAD sea el mismo.
rm -f "$SEAL"
_i1="$(olivares_nuevo_act_id)"; _i2="$(olivares_nuevo_act_id)"
OLIVARES_ROOT="$TREE" OLIVARES_ENT_DIR="$ENT" OLIVARES_ACT_ID="$_i1" \
	OLIVARES_OVERLAY_SEAL="$SEAL" bash "$TREE/scripts/fetch-overlay-seal.sh" >/dev/null 2>&1
if [ "$(lee "$_i1")" = 0 ] && [ "$(lee "$_i2")" = 2 ]; then
	ok "A-04 entorno: la corrida 2 no hereda la frescura de la corrida 1 (ids del generador)"
else
	bad "A-04 entorno: dos corridas del generador compartieron frescura"
fi

# ⛔ TESTIGO DEL CAMINO REAL DEL GANCHO (A-04, 4.ª vuelta del contraste). Los mutantes de arriba
# prueban la lib y el sello, pero NINGUNO probaba que el gancho llegue a generar el id **cuando
# corre como corre de verdad**: el hook se COPIA a $TMPDIR y hace `exec bash` desde alli, asi que
# una carga relativa a BASH_SOURCE cae fuera del arbol y la asignacion no se alcanza — sin id,
# `lint:overlay-seal` rehusaria CADA push. Y `test-prepush-refclass` no puede verlo porque cambia
# `task` por un stub siempre verde. Aqui se ejecuta el hook REAL, desde el cwd REAL, con `task`
# stubbeado sólo para que las patas no corran, y se exige que la pata del sello reciba un id.
if [ -r "$ROOT/.githooks/pre-push" ]; then
	HK="$TMP/hook"; mkdir -p "$HK/bin"
	cat >"$HK/bin/task" <<'EOF'
#!/usr/bin/env bash
# stub: no corre nada; sólo delata con qué id de acto llega la pata del sello.
if [ "${1:-}" = "lint:overlay-seal" ]; then
	printf '%s\n' "${OLIVARES_ACT_ID:-<VACIO>}" >>"${OLIVARES_TEST_ACTLOG:?}"
fi
exit 0
EOF
	chmod +x "$HK/bin/task"
	: >"$HK/actlog"
	Z=0000000000000000000000000000000000000000
	_refline="refs/heads/feature/x $(git -C "$ROOT" rev-parse HEAD) refs/heads/feature/x $Z"
	(
		cd "$ROOT" || exit 1
		PATH="$HK/bin:$PATH" OLIVARES_TEST_ACTLOG="$HK/actlog" \
			OLIVARES_ACT_ID="ID_HEREDADO_QUE_NO_DEBE_SOBREVIVIR" \
			TMPDIR="$TMP" timeout 240 bash .githooks/pre-push origin \
			https://example.invalid/x.git <<<"$_refline" >"$HK/out" 2>"$HK/err"
	) || true
	got="$(head -1 "$HK/actlog" 2>/dev/null || true)"
	if [ -z "$got" ]; then
		bad "el gancho real no llego a la pata del sello (no se pudo observar el id)"
	elif [ "$got" = "<VACIO>" ]; then
		bad "camino real: la pata del sello recibio el id VACIO — la carga por cwd no ocurre"
	elif [ "$got" = "ID_HEREDADO_QUE_NO_DEBE_SOBREVIVIR" ]; then
		bad "camino real: sobrevivio el id HEREDADO — falta el unset"
	else
		ok "camino real: el gancho genera el id tras su instantanea y NO hereda el del entorno"
	fi
fi

echo
echo "test-overlay-seal: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
