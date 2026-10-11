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
. "$_olivares_git_env" || { echo "FATAL: cannot source $_olivares_git_env" >&2; exit 2; }
olivares_git_env_isolate

GUION="$ROOT/scripts/claim-lector.sh"
[ -x "$GUION" ] || { echo "CANNOT INSPECT: missing $GUION"; exit 2; }

_base="${TMPDIR:-/workspace/.olivares-tmptest}"
case "$_base" in "$ROOT" | "$ROOT"/*) _base=/workspace/.olivares-tmptest ;; esac
mkdir -p "$_base"
WORK="$(mktemp -d "$_base/claimlector.XXXXXX")" || { echo "CANNOT INSPECT: mktemp"; exit 2; }
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
[ "$RC" = 0 ] && casa 'free' "$OUT" \
	&& ok "libre on a claim without a reader: rc 0 and reports it" \
	|| no "libre without a reader should return rc 0 (rc=$RC): $OUT"

# --- 2. tomar
corre ana tomar demo
if [ "$RC" = 0 ]; then ok "tomar publishes the signal (rc 0)"; else no "tomar failed (rc=$RC): $OUT"; fi
T="$(ref_remoto demo)"
[ -n "$T" ] && ok "the signal exists on the remote" || no "the signal did not appear on the remote"
if [ -n "$T" ]; then
	git -C "$REPO" fetch -q banco "refs/integration-claims/demo.lector:refs/tmp/l" 2>/dev/null
	TIPO="$(git -C "$REPO" cat-file -t "$T" 2>/dev/null)"
	OBJ="$(git -C "$REPO" cat-file tag "$T" 2>/dev/null | awk '/^object /{print $2; exit}')"
	[ "$TIPO" = tag ] && ok "the signal is a tag object (carries ITS timestamp)" || no "the signal is not a tag ($TIPO)"
	[ "$OBJ" = "$SHA" ] && ok "and points to the SHA being read" || no "points to $OBJ rather than $SHA"
fi

# --- 3. libre con lector
corre bea libre demo
if [ "$RC" = 1 ]; then ok "libre with a reader: rc 1 (finding, not error)"; else no "occupied libre should return rc 1 (rc=$RC)"; fi
casa 'ana' "$OUT" && ok "names the reader" || no "does not name the reader: $OUT"
casa "${SHA:0:12}" "$OUT" && ok "and reports WHICH SHA is being read" || no "does not report the SHA: $OUT"
casaE 'hace [0-9]+ min' "$OUT" && ok "and age comes from the CLAIM ACQUISITION, not the claim commit" || no "no readable age: $OUT"

# --- 4. un segundo lector no puede pisar
corre bea tomar demo
if [ "$RC" = 1 ]; then ok "a second reader is rejected (rc 1)"; else no "the second reader was not rejected (rc=$RC): $OUT"; fi
casa 'ana' "$OUT" && ok "and reports who holds it" || no "rejects without naming who holds it"

# --- 5. soltar
corre ana soltar demo
[ "$RC" = 0 ] && [ -z "$(ref_remoto demo)" ] && ok "soltar removes the signal" || no "soltar did not remove it (rc=$RC)"
corre ana libre demo
[ "$RC" = 0 ] && ok "and is free again afterward" || no "still occupied after soltar (rc=$RC)"

# --- 6. soltar lo que no esta tomado es idempotente
corre ana soltar demo
[ "$RC" = 0 ] && casa 'had no reader' "$OUT" && ok "soltar without a reader: rc 0 and reports it" || no "idempotent soltar failed (rc=$RC)"

# --- 7. tomar un claim que no existe
corre ana tomar noexiste
[ "$RC" = 1 ] && ok "tomar on a missing claim is a finding, not success" || no "missing claim returned rc=$RC"

# --- 8. no poder mirar es 2, no 0 ni 1
OUT="$( cd "$REPO" && OLIVARES_CLAIM_REMOTE="$WORK/no-hay-nada.git" bash "$GUION" libre demo 2>&1 )"; RC=$?
[ "$RC" = 2 ] && ok "an unreachable remote returns 2 (cannot inspect)" || no "unreachable remote returned rc=$RC: $OUT"

# --- 9. nombres invalidos
for malo in 'con/barra' 'demo.lector' ''; do
	OUT="$( cd "$REPO" && bash "$GUION" libre "$malo" 2>&1 )"; RC=$?
	[ "$RC" = 2 ] || no "invalid name '$malo' did not exit 2 (rc=$RC)"
done
ok "invalid names exit 2 and do not touch the remote"

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
if [ "$RC" = 0 ]; then ok "M1: without the lease, the second reader OVERWRITES — the lease enforces rejection"; else no "M1 survives: still rejects without a lease (rc=$RC)"; fi
restaurar
git -C "$REPO" push -q banco --delete refs/integration-claims/demo.lector 2>/dev/null

# M2 — libre que miente: devuelve 0 aunque haya lector
corre ana tomar demo >/dev/null
mutar '/^cmd_libre()/,/^}$/ s/^\treturn 1$/\treturn 0/'
corre bea libre demo
if [ "$RC" = 0 ]; then ok "M2: a 'free' always returning 0 is distinguishable (the test requires it above)"; else no "M2 is not distinguishable (rc=$RC)"; fi
restaurar
corre ana soltar demo >/dev/null

# CONTROL sobre el propio banco: si el sujeto no existe, no puedo mirar.
OUT="$( cd "$REPO" && bash "$WORK/no-existe.sh" libre demo 2>&1 )"; RC=$?
[ "$RC" != 0 ] && ok "CONTROL: with a missing script, the test does not accept absence as success" \
	|| no "CONTROL: a missing script exited 0"

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
{ [ "$RC" = 1 ] && casa 'MOVED WITH READER' "$OUT" && casa "${OTRO:0:12}" "$OUT"; } &&
	ok "moved while being read: rc 1, NAMES it and reports the current value" ||
	no "did not detect movement while being read (rc=$RC): $OUT"
git -C "$REPO" push -q -f banco "$SHA:refs/integration-claims/demo"

# 2 · SEÑAL SIN FUENTE. Un lector sobre un claim que ya no existe no es «libre» ni «ocupado»:
#     es un estado que este guion no sabe interpretar, y contestar 0 o 1 seria inventarselo.
git -C "$REPO" push -q banco --delete refs/integration-claims/demo
corre bea libre demo
{ [ "$RC" = 2 ] && casa 'COULD NOT LOOK' "$OUT"; } &&
	ok "reader on a missing claim: 2, not free" ||
	no "orphan signal did not return 2 (rc=$RC): $OUT"
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
	ok "signal without a declared version: 2 and reports what is missing" ||
	no "unversioned signal did not return 2 (rc=$RC): $OUT"
git -C "$REPO" push -q banco --delete refs/integration-claims/demo.lector

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
	ok "lector-v10 is NOT accepted as lector-v1: 2" ||
	no "the marker was compared as a substring (rc=$RC): $OUT"
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
{ [ "$RC" = 2 ] && casa 'COULD NOT LOOK' "$OUT"; } &&
	ok "signal appearing BETWEEN the two reads: 2, not «free»" ||
	no "the race was not detected (rc=$RC): $OUT"
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
		ok "CONTROL: the test cannot use https and fails because of the PROTOCOL" ;;
	*)
		no "the test could attempt https or failed for another cause: ${_err%%$'\n'*}" ;;
esac

printf '\ntest-claim-lector: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
