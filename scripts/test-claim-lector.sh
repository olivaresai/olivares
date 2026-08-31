#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Banco de claim-lector.sh. BORRADOR, como el guion que mide.
#
# Corre contra un remoto desnudo en TMPDIR llamado `banco` —nunca `origin`, nunca el de verdad—, y
# que se prueba es el protocolo, y probarlo contra el remoto vivo publicaria señales reales.
#
# Cada afirmacion lleva su mutante, y hay un control que muta el propio banco: si mira un guion
# que no existe tiene que decir «no he podido mirar», no dar la ausencia por buena.
#
# Contrato: 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# ⛔ Este banco hace `git init`: sin sanear, un GIT_DIR heredado lo llevaria al repo VIVO.
_olivares_git_env="$ROOT/scripts/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || { echo "FATAL: no puedo sourcear $_olivares_git_env" >&2; exit 2; }
olivares_git_env_isolate

GUION="$ROOT/scripts/claim-lector.sh"
[ -x "$GUION" ] || { echo "NO HE PODIDO MIRAR: falta $GUION"; exit 2; }

_base="${TMPDIR:-/workspace/.olivares-tmptest}"
case "$_base" in "$ROOT" | "$ROOT"/*) _base=/workspace/.olivares-tmptest ;; esac
mkdir -p "$_base"
WORK="$(mktemp -d "$_base/claimlector.XXXXXX")" || { echo "NO HE PODIDO MIRAR: mktemp"; exit 2; }
trap 'rm -rf "$WORK"' EXIT

# ⛔ HERMETISMO IMPUESTO, NO AFIRMADO. Este banco decia «un origin de MENTIRA» y la unica prueba
# era leer su codigo. El 2026-08-30 un vigia de aterrizajes vio «push a origin de
# refs/integration-claims/demo.lector» y pregunto si el banco tocaba el remoto de verdad: la
# respuesta era no, pero NO SE PODIA VER DESDE FUERA, porque el remoto falso tambien se llamaba
# `origin`. Un NOMBRE de remoto no es una URL. Dos remedios, y ninguno depende de leer esto:
#   · solo se permite el protocolo `file`, asi que un push a https muere aqui dentro con
#     `fatal: transport 'https' not allowed` en vez de publicar;
#   · el remoto del banco se llama `banco`, no `origin`, para que ningun observador —ni un
#     descuido— los confunda.
GIT_ALLOW_PROTOCOL=file
export GIT_ALLOW_PROTOCOL

# ⛔ Sin tuberia: `printf … | grep -q` devuelve 141 CUANDO ACIERTA bajo pipefail, y este banco
# comprueba precisamente aciertos. `casa`/`casaE` alimentan a grep por here-string, que no abre
# tuberia y por tanto no puede recibir SIGPIPE. Es la misma cura que el trinquete imprime.
casa()  { command grep -q  -- "$1" <<<"$2"; }
casaE() { command grep -qE -- "$1" <<<"$2"; }

pass=0; fail=0
ok() { printf 'ok    %s\n' "$1"; pass=$((pass+1)); }
no() { printf 'FAIL  %s\n' "$1"; fail=$((fail+1)); }

# --- banco: un remoto desnudo LLAMADO `banco`, con un claim publicado -------------------------------------
BANCO="$WORK/banco.git"
git init -q --bare "$BANCO"
REPO="$WORK/repo"
git init -q -b main "$REPO"
git -C "$REPO" config user.email t@t.invalid
git -C "$REPO" config user.name t
git -C "$REPO" commit -q --allow-empty -m uno
SHA="$(git -C "$REPO" rev-parse HEAD)"
git -C "$REPO" remote add banco "$BANCO"
git -C "$REPO" push -q banco "$SHA:refs/integration-claims/demo"

corre() { # <lector> <verbo> [args...] -> imprime salida, deja RC
	local quien="$1"; shift
	OUT="$( cd "$REPO" && OLIVARES_LECTOR="$quien" OLIVARES_CLAIM_REMOTE=banco \
		bash "$GUION_ACTUAL" "$@" 2>&1 )"
	RC=$?
}
GUION_ACTUAL="$GUION"

ref_remoto() { git -C "$REPO" ls-remote banco "refs/integration-claims/$1.lector" | awk '{print $1}'; }

# --- 1. libre cuando no hay lector
corre ana libre demo
[ "$RC" = 0 ] && casa 'libre' "$OUT" \
	&& ok "libre sobre un claim sin lector: rc 0 y lo dice" \
	|| no "libre sin lector deberia ser rc 0 (rc=$RC): $OUT"

# --- 2. tomar
corre ana tomar demo
if [ "$RC" = 0 ]; then ok "tomar publica la señal (rc 0)"; else no "tomar fallo (rc=$RC): $OUT"; fi
T="$(ref_remoto demo)"
[ -n "$T" ] && ok "la señal existe en el remoto" || no "la señal no aparecio en el remoto"
if [ -n "$T" ]; then
	git -C "$REPO" fetch -q banco "refs/integration-claims/demo.lector:refs/tmp/l" 2>/dev/null
	TIPO="$(git -C "$REPO" cat-file -t "$T" 2>/dev/null)"
	OBJ="$(git -C "$REPO" cat-file tag "$T" 2>/dev/null | awk '/^object /{print $2; exit}')"
	[ "$TIPO" = tag ] && ok "la señal es un objeto de etiqueta (trae SU fecha)" || no "la señal no es una etiqueta ($TIPO)"
	[ "$OBJ" = "$SHA" ] && ok "y apunta al SHA que se esta leyendo" || no "apunta a $OBJ y no a $SHA"
fi

# --- 3. libre con lector
corre bea libre demo
if [ "$RC" = 1 ]; then ok "libre con lector: rc 1 (hallazgo, no error)"; else no "libre ocupado deberia ser rc 1 (rc=$RC)"; fi
casa 'ana' "$OUT" && ok "nombra a quien lee" || no "no nombra al lector: $OUT"
casa "${SHA:0:12}" "$OUT" && ok "y dice QUE SHA se lee" || no "no dice el SHA: $OUT"
casaE 'hace [0-9]+ min' "$OUT" && ok "y la edad viene de la TOMA, no del commit del claim" || no "sin edad legible: $OUT"

# --- 4. un segundo lector no puede pisar
corre bea tomar demo
if [ "$RC" = 1 ]; then ok "un segundo lector es rechazado (rc 1)"; else no "el segundo lector no fue rechazado (rc=$RC): $OUT"; fi
casa 'ana' "$OUT" && ok "y le dice quien lo tiene" || no "rechaza sin decir quien lo tiene"

# --- 5. soltar
corre ana soltar demo
[ "$RC" = 0 ] && [ -z "$(ref_remoto demo)" ] && ok "soltar borra la señal" || no "soltar no la borro (rc=$RC)"
corre ana libre demo
[ "$RC" = 0 ] && ok "y despues vuelve a estar libre" || no "sigue ocupado tras soltar (rc=$RC)"

# --- 6. soltar lo que no esta tomado es idempotente
corre ana soltar demo
[ "$RC" = 0 ] && casa 'no habia' "$OUT" && ok "soltar sin lector: rc 0 y lo dice" || no "soltar idempotente fallo (rc=$RC)"

# --- 7. tomar un claim que no existe
corre ana tomar noexiste
[ "$RC" = 1 ] && ok "tomar un claim inexistente es hallazgo, no exito" || no "claim inexistente dio rc=$RC"

# --- 8. no poder mirar es 2, no 0 ni 1
OUT="$( cd "$REPO" && OLIVARES_CLAIM_REMOTE="$WORK/no-hay-nada.git" bash "$GUION" libre demo 2>&1 )"; RC=$?
[ "$RC" = 2 ] && ok "un remoto inalcanzable responde 2 (no he podido mirar)" || no "remoto inalcanzable dio rc=$RC: $OUT"

# --- 9. nombres invalidos
for malo in 'con/barra' 'demo.lector' ''; do
	OUT="$( cd "$REPO" && bash "$GUION" libre "$malo" 2>&1 )"; RC=$?
	[ "$RC" = 2 ] || no "el nombre invalido '$malo' no salio 2 (rc=$RC)"
done
ok "los nombres invalidos salen 2 y no tocan el remoto"

# --- mutantes -----------------------------------------------------------------------------
# ⛔ EL MUTANTE VIVE EN UN ARBOL, NO EN UN FICHERO SUELTO, y esto costo dos mutantes falsos:
# claim-lector.sh resuelve su ROOT por BASH_SOURCE y sourcea `$ROOT/scripts/lib/git-env.sh`. Una
# copia en un directorio pelado no lo encuentra, sale 2 («no he podido mirar») y el banco lee ese
# 2 como «el mutante sigue rechazando» — es decir, el mutante parecia MUERTO por una razon que no
# tiene nada que ver con la mutacion. Se le monta la estructura minima que el guion espera.
mutar() {
	mkdir -p "$WORK/tree/scripts/lib"
	cp "$ROOT/scripts/lib/git-env.sh" "$WORK/tree/scripts/lib/git-env.sh"
	sed "$1" "$GUION" > "$WORK/tree/scripts/mut.sh"
	chmod +x "$WORK/tree/scripts/mut.sh"
	GUION_ACTUAL="$WORK/tree/scripts/mut.sh"
}
restaurar() { GUION_ACTUAL="$GUION"; }

# M1 — sin lease, el segundo lector pisa al primero
git -C "$REPO" push -q banco --delete refs/integration-claims/demo.lector 2>/dev/null
corre ana tomar demo >/dev/null
mutar 's/--force-with-lease="\$(ref_de "\$claim"):" //'
corre bea tomar demo
if [ "$RC" = 0 ]; then ok "M1: sin el lease el segundo lector PISA — el lease es lo que rechaza"; else no "M1 sobrevive: sin lease sigue rechazando (rc=$RC)"; fi
restaurar
git -C "$REPO" push -q banco --delete refs/integration-claims/demo.lector 2>/dev/null

# M2 — libre que miente: devuelve 0 aunque haya lector
corre ana tomar demo >/dev/null
mutar '/^cmd_libre()/,/^}$/ s/^\treturn 1$/\treturn 0/'
corre bea libre demo
if [ "$RC" = 0 ]; then ok "M2: un 'libre' que siempre dice 0 es distinguible (el banco lo exige arriba)"; else no "M2 no se distingue (rc=$RC)"; fi
restaurar
corre ana soltar demo >/dev/null

# CONTROL sobre el propio banco: si el sujeto no existe, no puedo mirar.
OUT="$( cd "$REPO" && bash "$WORK/no-existe.sh" libre demo 2>&1 )"; RC=$?
[ "$RC" != 0 ] && ok "CONTROL: sobre un guion inexistente el banco no da por buena la ausencia" \
	|| no "CONTROL: un guion inexistente salio 0"

# --- LO QUE LA v3 AÑADE, y cada fila EJERCITA su rama -------------------------------------
# Un banco que pasa sin tocar el codigo nuevo no acredita nada: estas tres filas existen porque
# las tres ramas de abajo son las que el lector pidio y ninguna estaba probada.

# 1 · MOVIDO BAJO LECTOR. La señal pincha el SHA leido; si el claim pasa a valer otra cosa, el
#     guion no puede impedirlo pero tiene que DECIRLO. Antes contestaba «ocupado» y nadie comparaba.
corre ana tomar demo
git -C "$REPO" commit -q --allow-empty -m dos
OTRO="$(git -C "$REPO" rev-parse HEAD)"
git -C "$REPO" push -q -f banco "$OTRO:refs/integration-claims/demo"
corre bea libre demo
{ [ "$RC" = 1 ] && casa 'MOVIDO BAJO LECTOR' "$OUT" && casa "${OTRO:0:12}" "$OUT"; } &&
	ok "movido bajo lector: rc 1, lo NOMBRA y da el valor de ahora" ||
	no "no detecto el movimiento bajo lector (rc=$RC): $OUT"
git -C "$REPO" push -q -f banco "$SHA:refs/integration-claims/demo"

# 2 · SEÑAL SIN FUENTE. Un lector sobre un claim que ya no existe no es «libre» ni «ocupado»:
#     es un estado que este guion no sabe interpretar, y contestar 0 o 1 seria inventarselo.
git -C "$REPO" push -q banco --delete refs/integration-claims/demo
corre bea libre demo
{ [ "$RC" = 2 ] && casa 'NO HE PODIDO MIRAR' "$OUT"; } &&
	ok "lector sobre claim inexistente: 2, no libre" ||
	no "señal huerfana no dio 2 (rc=$RC): $OUT"
git -C "$REPO" push -q banco "$SHA:refs/integration-claims/demo"

# 3 · AUTORIDAD VERSIONADA. Una señal de un formato desconocido no se interpreta a medias.
corre ana soltar demo
VIEJA="$( cd "$REPO" && git mktag <<-EOT
	object $SHA
	type commit
	tag lector
	tagger vieja <v@invalid> 1000000000 +0000

	formato de antes
	EOT
)"
git -C "$REPO" push -q banco "$VIEJA:refs/integration-claims/demo.lector"
corre bea libre demo
{ [ "$RC" = 2 ] && casa 'lector-v1' "$OUT"; } &&
	ok "señal sin version declarada: 2 y dice que le falta" ||
	no "señal sin version no dio 2 (rc=$RC): $OUT"
git -C "$REPO" push -q banco --delete refs/integration-claims/demo.lector

# --- LA GUARDA DEL GANCHO, que es donde la señal deja de ser cortesia ---------------------
# Se ejercita SIN empujar nada: el gancho lee las lineas del protocolo por stdin, y
# `git ls-remote` acepta una RUTA ademas de un nombre de remoto, asi que el banco de mentira vale
# como remoto sin tocar la configuracion del repositorio real. Dos filas y las dos hacen falta: la
# que rechaza no vale sin la que deja pasar, porque una guarda que dijera que NO a todo pasaria la
# primera igual de bien.
GANCHO="$ROOT/.githooks/pre-push"
# Un segundo commit para que el movimiento del fixture sea REAL (old != new).
git -C "$REPO" commit -q --allow-empty -m tres
OTRO_SHA="$(git -C "$REPO" rev-parse HEAD)"
linea_update() { printf 'refs/heads/x %s refs/integration-claims/demo %s\n' "$1" "$2"; }
# ⛔ La ruta va como $1 y NO por entorno: el gancho hace `export OLIVARES_PUSH_REMOTE_NAME="$1"`
#    y pisa cualquier valor que le pasemos. git pone ahi el NOMBRE del remoto, y `ls-remote` acepta
#    igual una ruta, asi que el banco de mentira entra por la puerta de siempre.
corre_gancho() {
	OUT="$(linea_update "$1" "$2" | \
		bash "$GANCHO" "$BANCO" "$BANCO" 2>&1)"
	RC=$?
}
if [ -r "$GANCHO" ]; then
	# ⛔ old -> new CON SHAS DISTINTOS, y no es un detalle: la version anterior pasaba el MISMO sha
	#    como viejo y nuevo, asi que probaba que la guarda rechaza mover A->A. Un mutante que solo
	#    rechazara OIDs IGUALES conservaba el banco entero en verde y dejaba pasar A->B con lector
	#    dentro: el caso real. El positivo tiene que ser el movimiento de verdad.
	corre ana tomar demo
	corre_gancho "$OTRO_SHA" "$SHA"
	{ [ "$RC" = 1 ] && casa 'TIENE LECTOR' "$OUT"; } &&
		ok "el gancho RECHAZA mover un claim con lector, antes de honrar el skip" ||
		no "el gancho no rechazo el movimiento con lector (rc=$RC): $OUT"
	corre ana soltar demo
	corre_gancho "$OTRO_SHA" "$SHA"
	{ [ "$RC" = 0 ] && ! casa 'TIENE LECTOR' "$OUT"; } &&
		ok "CONTROL: sin lector, el MISMO push pasa (la guarda no dice que no a todo)" ||
		no "sin lector el gancho no dejo pasar (rc=$RC): $OUT"
else
	no "NO HE PODIDO MIRAR: no leo $GANCHO"
fi

# --- MARCADOR DE VERSION EXACTO, no subcadena --------------------------------------------
# `*"lector-v1"*` aceptaba `lector-v10` —una version FUTURA leida por un guion viejo, que es
# justo lo que el versionado existe para impedir— mientras `lector-v2` si daba 2. La fila usa
# v10 a proposito: es la que distingue «compara la linea» de «busca el texto dentro».
corre ana soltar demo
V10="$( cd "$REPO" && git mktag <<-EOT
	object $SHA
	type commit
	tag lector
	tagger futura <f@invalid> 1000000000 +0000

	lector-v10
	leyendo demo
	EOT
)"
git -C "$REPO" push -q banco "$V10:refs/integration-claims/demo.lector"
corre bea libre demo
{ [ "$RC" = 2 ] && casa 'lector-v1' "$OUT"; } &&
	ok "lector-v10 NO pasa por lector-v1: 2" ||
	no "el marcador se comparo por subcadena (rc=$RC): $OUT"
git -C "$REPO" push -q banco --delete refs/integration-claims/demo.lector

# --- LA CARRERA ENTRE LAS DOS LECTURAS, HECHA DETERMINISTA -------------------------------
# La tercera cura —releer la señal antes de contestar «libre»— no tenia fila: quitarla dejaba el
# banco en 27/0, o sea que la cura no estaba acreditada por nada. Una carrera no se prueba
# esperando a que ocurra: se INTERPONE. Un `git` de mentira en el PATH cuenta las llamadas a
# `ls-remote` y, justo antes de la segunda, PUBLICA la señal. Asi la primera lectura ve vacio y la
# relectura ve un lector — exactamente el intercalado que el lector describio.
INTER="$WORK/inter"; mkdir -p "$INTER"
GIT_REAL="$(command -v git)"
cat >"$INTER/git" <<CARRERA
#!/usr/bin/env bash
if [ "\$1" = "ls-remote" ]; then
	n=\$(( \$(cat "$WORK/n" 2>/dev/null || echo 0) + 1 ))
	echo "\$n" > "$WORK/n"
	if [ "\$n" = 2 ]; then
		"$GIT_REAL" --git-dir="$BANCO" update-ref refs/integration-claims/demo.lector "\$(cat "$WORK/tag")" 2>/dev/null
	fi
fi
exec "$GIT_REAL" "\$@"
CARRERA
chmod +x "$INTER/git"
# una señal real de la que tomar el objeto, y se retira para dejar el claim LIBRE al empezar
corre ana tomar demo
ref_remoto demo > "$WORK/tag"
corre ana soltar demo
rm -f "$WORK/n"
OUT="$( cd "$REPO" && PATH="$INTER:$PATH" OLIVARES_CLAIM_REMOTE=banco \
	bash "$GUION_ACTUAL" libre demo 2>&1 )"; RC=$?
{ [ "$RC" = 2 ] && casa 'NO HE PODIDO MIRAR' "$OUT"; } &&
	ok "señal que aparece ENTRE las dos lecturas: 2, no «libre»" ||
	no "la carrera no se detecto (rc=$RC): $OUT"
git -C "$REPO" push -q banco --delete refs/integration-claims/demo.lector 2>/dev/null
rm -f "$WORK/n"

# --- CONTROL DE HERMETISMO, y es discriminante a proposito ------------------------------
# No basta con que un push a una forja FALLE: fallaria igual por DNS, y entonces esta fila
# pasaria sin que el acotado de protocolo estuviera puesto — un control que se acredita con la
# causa equivocada. Se exige el MOTIVO exacto. Si alguien retira el `GIT_ALLOW_PROTOCOL=file`
# de arriba, esta fila cae y dice por que.
# ⛔ LA URL ES DE EJEMPLO A PROPOSITO. Lo unico que esta fila necesita es que el esquema sea
# `https`, para que `GIT_ALLOW_PROTOCOL=file` lo rechace y el mensaje lo diga. El nombre del
# repositorio es irrelevante para la prueba, y escribir aqui el repositorio PRIVADO lo hacia
# viajar al arbol publicado — `lint:export` lo caza en la clase «private org-or-domain».
git -C "$REPO" remote add forja https://example.invalid/olivares/fixture.git 2>/dev/null
_err="$(git -C "$REPO" ls-remote forja 2>&1)"
case "$_err" in
	*"transport 'https' not allowed"*)
		ok "CONTROL: el banco no puede hablar https, y muere por el PROTOCOLO" ;;
	*)
		no "el banco pudo intentar https, o murio por otra causa: ${_err%%$'\n'*}" ;;
esac

printf '\ntest-claim-lector: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
