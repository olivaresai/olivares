#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# check-publisher-contract.sh — el contrato del publicador tiene que ser lo que su generador
# produce HOY de sus fuentes, byte a byte.
#
# ⛔ POR QUE (REL-109-bis). `scripts/publish-enterprise-artifacts.sh` leia sus tres autoridades
# de `commercial/license-worker/src/**.ts`, y `commercial/` NO VIAJA ni al export ni al overlay:
# tras el re-apunte de D-06 el publicador moria. Ahora lee un JSON generado que SI viaja — y un
# JSON generado es una COPIA. Una copia sin quien la compare es la clase que este repositorio ya
# ha pagado: la clave del manifiesto vivio copiada en tres sitios, se movio en uno, y los otros
# dos siguieron de acuerdo ENTRE ELLOS mientras el comprador recibia 404.
#
# Esta pata es ese comparador: REGENERA y exige identidad byte a byte. No compara campos
# «importantes» ni normaliza: si el generador produce otra cosa, es un hallazgo, porque el
# publicador lee el fichero ENTERO y no una seleccion.
#
# Veredictos: 0 = identico · 1 = difiere (se dice como) · 2 = NO HE PODIDO MIRAR.
set -uo pipefail
export LC_ALL=C

# ⛔ AISLAMIENTO DEL ENTORNO GIT. Esta pata ejecuta `git archive` para materializar los tips del
# push, y con las GIT_* del entorno heredadas eso puede resolver contra OTRO repositorio sin
# decirlo: mediria un arbol que no es el que se empuja y daria un veredicto plausible y falso. Lo
# exige `lint:git-env` para toda pata que junte `mktemp -d` con git, y la exigencia es correcta.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
W="$ROOT/commercial/license-worker"
CONTRATO="$W/contracts/publisher.gen.json"
GEN="$W/scripts/gen-publisher-contract.ts"

cannot() { echo "check-publisher-contract: ⛔ NO HE PODIDO MIRAR: $*" >&2; exit 2; }

# ⛔ LAS TRES RUTAS QUE ESTA PATA INVOCA SON HUB-ONLY, Y VAN DECLARADAS. Este guion SI viaja al
# arbol publico —tiene que poder decir alli «SKIP, sin commercial/»— pero lo que llama no viaja, y
# `lint:export-closure` exige que eso sea una decision escrita en el sitio de la llamada y no un
# descubrimiento de quien lea el rojo. Las tres llamadas estan guardadas por presencia (`[ -r … ]`
# / `[ -d … ]`), asi que en el arbol publico no se ejecuta ninguna: el veredicto alli es SKIP 0.
#
# export-closure: hub-only commercial/license-worker/scripts/gen-publisher-contract.ts — el generador
#   vive bajo `commercial/`, que no viaja; sin el no hay nada que regenerar y la pata sale SKIP.
# export-closure: hub-only scripts/publish-enterprise-artifacts.sh — el publicador esta en
#   SCRIPTS_BLOCK a proposito (nombra nuestros buckets y la credencial que los escribe), asi que la
#   comprobacion del consumidor solo corre donde el consumidor existe.

# ⛔ SI EL SUJETO NO ESTA, SE SALTA Y SE DICE — el arbol EXPORTADO no lleva `commercial/`, que es
# justo el hecho que origina todo esto. Convertir su ausencia en «no he podido mirar» pondria
# rojo el gate en el unico arbol donde su ausencia es CORRECTA.
#
# ⛔ Y NO SE SUPONE CUAL ES EL ARBOL: SE CLASIFICA. «No esta commercial/» tambien es lo que se ve
# en un hub a medio clonar, en un checkout parcial o si alguien borra el directorio — y tal cual
# estaba, esos tres casos salian SKIP 0: la pata callaba exactamente cuando mas falta hacia. Lo
# levanto el contraste sol max como F-04. Se pregunta al clasificador de la casa, que ya sabe
# distinguir un export de un hub por la marca que el export estampa; una TERCERA definicion de
# «arbol publico» aqui seria la copia que este contrato entero vino a cerrar.
if [ ! -d "$W" ]; then
	CLASIFICADOR="$ROOT/scripts/hub-leg.sh"
	[ -x "$CLASIFICADOR" ] || cannot "sin commercial/license-worker y sin $CLASIFICADOR: no se que arbol es esto"
	CLASE="$("$CLASIFICADOR" --classify 2>/dev/null)" || CLASE=""
	case "$CLASE" in
	public)
		echo "check-publisher-contract: SKIP — arbol publico clasificado (sin commercial/, correcto)"
		exit 0
		;;
	hub)
		echo "check-publisher-contract: FAIL — arbol clasificado como HUB y sin commercial/license-worker." >&2
		echo "  En un hub el sujeto TIENE que estar: o el checkout esta incompleto, o alguien lo borro." >&2
		echo "  Antes esto salia SKIP 0 y la pata callaba justo cuando hacia falta (F-04 del contraste)." >&2
		exit 1
		;;
	*)
		cannot "sin commercial/license-worker y el clasificador no sabe que arbol es esto (dijo '${CLASE:-nada}')"
		;;
	esac
fi
[ -r "$CONTRATO" ] || cannot "no encuentro $CONTRATO; generalo con 'npm run contract:publisher'"
[ -r "$GEN" ] || cannot "no encuentro el generador $GEN"
command -v node >/dev/null 2>&1 || cannot "no hay node en el PATH"

TMP="$(mktemp -d "${TMPDIR:-/tmp}/pubcontract.XXXXXX")" || cannot "no puedo crear un temporal"
trap 'rm -rf "$TMP"' EXIT INT TERM

# ────────────────────────────────────────────────────────────────────────────────────────────────
# medir <etiqueta> <raiz> — comprueba UN arbol. Sale 0 limpio, 1 hallazgo, 2 no he podido mirar.
#
# Esta pata mide un ARBOL que le dan, no «el sitio donde estoy». Es lo que permite medir el commit
# que el push va a enviar en vez de los bytes sucios del worktree (F-02 del contraste sol max).
# ────────────────────────────────────────────────────────────────────────────────────────────────
n_medido=0
medir() {
	local etiqueta="$1" raiz="$2"
	local w="$raiz/commercial/license-worker"
	local contrato="$w/contracts/publisher.gen.json"
	local gen="$w/scripts/gen-publisher-contract.ts"
	local pub="$raiz/scripts/publish-enterprise-artifacts.sh"

	if [ ! -d "$w" ]; then
		echo "check-publisher-contract: [$etiqueta] SKIP — sin commercial/license-worker"
		return 0
	fi
	[ -r "$contrato" ] || { echo "check-publisher-contract: [$etiqueta] FAIL — hay commercial/license-worker y NO hay contrato ($contrato)." >&2; return 1; }
	[ -r "$gen" ] || { echo "check-publisher-contract: [$etiqueta] ⛔ NO HE PODIDO MIRAR: no encuentro el generador $gen" >&2; return 2; }

	# ⛔ EL GENERADOR ESCRIBE EN EL TEMPORAL, NO EN EL ARBOL. Hasta el 2026-09-02 esta pata borraba
	# el contrato del worktree, regeneraba EN SU SITIO y lo restauraba despues: durante esos
	# milisegundos el arbol estaba a medias, y si el proceso moria entre medias (un Ctrl-C, un OOM)
	# el contrato se quedaba BORRADO. Lo levanto el contraste como F-02/F-07.
	local nuevo="$TMP/regen-$n_medido.json"
	n_medido=$((n_medido + 1))
	# ⛔ SE INVOCA POR SU RUTA COMPLETA, no con un `cd` delante. Con `cd "$w" && node
	# scripts/gen-publisher-contract.ts`, el verificador de cierre del export resuelve esa cadena
	# desde la RAIZ y ve una ruta que no existe — y declararla hub-only tampoco vale, porque
	# entonces se queja de que la ruta declarada no existe. Nombrar el fichero de verdad arregla
	# las dos cosas. Se puede desde que el generador saca su raiz de `import.meta.url` (F-08): ya
	# no depende del directorio desde el que se le llame.
	if ! node "$gen" "$nuevo" >"$TMP/gen.out" 2>"$TMP/gen.err"; then
		echo "check-publisher-contract: [$etiqueta] ⛔ NO HE PODIDO MIRAR: el generador fallo: $(head -1 "$TMP/gen.err" 2>/dev/null)" >&2
		return 2
	fi
	# La guarda del verde ciego, sin tocar el arbol: un generador que sale 0 y no escribe dejaria la
	# comparacion sin sujeto, y «nada corrio» no puede leerse como «identico». Medido el 2026-09-02
	# sobre esta misma pata: con el fichero comparandose consigo mismo, salia CLEAN con el generador
	# roto.
	if [ ! -s "$nuevo" ]; then
		echo "check-publisher-contract: [$etiqueta] ⛔ NO HE PODIDO MIRAR: el generador salio 0 pero no dejo contrato: no hay nada que comparar" >&2
		return 2
	fi

	# ⛔ Y AHORA EL OTRO LADO DEL CONTRATO: QUE EL CONSUMIDOR SEPA LEERLO. Hasta hoy esta pata
	# comparaba el productor CONSIGO MISMO y decia CLEAN mientras el publicador estaba roto — lo
	# encontro el contraste sol max (F-01) y se reprodujo: el JSON paso a su esquema definitivo
	# (`releaseShape.regexEre`, `setSlugs`) y el lector seguia pidiendo los nombres viejos, asi que
	# TODA invocacion moria en la primera validacion. Un gate que solo mira un lado no vigila un
	# contrato: vigila un fichero.
	#
	# ⛔ LAS CLAVES SE DERIVAN DEL PROPIO PUBLICADOR, no se escriben aqui. Una segunda lista seria
	# la copia que este contrato entero vino a cerrar: si alguien anade una lectura nueva al
	# publicador, esta comprobacion la exige sin que nadie la anote.
	if [ -r "$pub" ]; then
		local claves faltan k
		claves="$(grep -oE 'leer_contrato [A-Za-z_][A-Za-z_.]*' "$pub" | awk '{print $2}' | sort -u)"
		if [ -z "$claves" ]; then
			echo "check-publisher-contract: [$etiqueta] ⛔ NO HE PODIDO MIRAR: no he sabido leer del publicador que claves consume" >&2
			return 2
		fi
		faltan=""
		while IFS= read -r k; do
			[ -n "$k" ] || continue
			python3 -c 'import json,sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
v = d
for parte in sys.argv[2].split("."):
    if not isinstance(v, dict) or parte not in v:
        sys.exit(3)
    v = v[parte]' "$contrato" "$k" || faltan="$faltan $k"
		done <<-EOF
		$claves
		EOF
		if [ -n "$faltan" ]; then
			echo "check-publisher-contract: [$etiqueta] FAIL — el publicador lee claves que el contrato NO trae:$faltan" >&2
			echo "  El contrato y su consumidor han derivado. Claves que el publicador pide:" >&2
			printf '    %s\n' $claves >&2
			echo "  Cura: alinea el esquema en scripts/publish-enterprise-artifacts.sh o regenera el" >&2
			echo "  contrato. Un contrato que su consumidor no sabe leer no es un contrato." >&2
			return 1
		fi
		echo "check-publisher-contract: [$etiqueta] el publicador lee $(printf '%s\n' $claves | wc -l | tr -d ' ') clave(s) y el contrato las trae todas."
	fi

	if cmp -s "$contrato" "$nuevo"; then
		echo "check-publisher-contract: [$etiqueta] CLEAN — el contrato es lo que su generador produce."
		return 0
	fi
	echo "check-publisher-contract: [$etiqueta] FAIL — el contrato commiteado NO es lo que el generador produce." >&2
	echo "  Alguien cambio una de las tres autoridades y no regenero. Diferencia:" >&2
	diff "$contrato" "$nuevo" | head -20 >&2
	cmp "$contrato" "$nuevo" 2>&1 | head -2 | sed 's/^/  /' >&2
	echo "  Cura: cd commercial/license-worker && npm run contract:publisher, y commitea el JSON." >&2
	return 1
}

# ────────────────────────────────────────────────────────────────────────────────────────────────
# ⛔ QUE ARBOL SE MIDE. El worktree NO es lo que el push envia: son los COMMITS de los tips. Un
# contrato regenerado y sin commitear daba CLEAN aqui mientras el commit que viajaba llevaba el
# viejo, y al reves — un arbol sucio ponia rojo un push perfectamente sano. Lo levanto el contraste
# sol max como F-02, y la cura que nombra es esta: medir los tips de `OLIVARES_PUSH_REFS_FILE`,
# que es el fichero que el gancho ya exporta y que otras patas de esta casa leen.
#
# Cuando no hay push (una corrida a mano, `task lint:publisher-contract`) no hay tips que leer y se
# mide el worktree, que ahi SI es el sujeto correcto. Se dice cual de los dos se hizo: un veredicto
# que no dice sobre que arbol se tomo no se puede reproducir.
# ────────────────────────────────────────────────────────────────────────────────────────────────
extraer_tip() { # extraer_tip <oid> <destino> -> 0 si dejo algo
	local oid="$1" dest="$2" ruta algo=1
	mkdir -p "$dest" || return 1
	for ruta in commercial/license-worker scripts/publish-enterprise-artifacts.sh; do
		git -C "$ROOT" cat-file -e "$oid:$ruta" 2>/dev/null || continue
		git -C "$ROOT" archive "$oid" "$ruta" 2>/dev/null | tar -x -C "$dest" 2>/dev/null && algo=0
	done
	return $algo
}

peor=0
anota() { [ "$1" -gt "$peor" ] && peor="$1"; return 0; }

if [ -n "${OLIVARES_PUSH_REFS_FILE:-}" ] && [ -r "${OLIVARES_PUSH_REFS_FILE}" ] \
	&& command -v git >/dev/null 2>&1 && command -v tar >/dev/null 2>&1; then
	tips=0
	while read -r _lref loid _rref _roid; do
		[ -n "${loid:-}" ] || continue
		# Un borrado (oid todo ceros) no trae arbol que medir.
		case "$loid" in *[!0]*) ;; *) continue ;; esac
		tips=$((tips + 1))
		d="$TMP/tip-$tips"
		if ! extraer_tip "$loid" "$d"; then
			echo "check-publisher-contract: [${loid:0:12}] SKIP — ese commit no trae ni el worker ni el publicador"
			continue
		fi
		medir "${loid:0:12}" "$d"; anota $?
	done <"$OLIVARES_PUSH_REFS_FILE"
	if [ "$tips" -eq 0 ]; then
		echo "check-publisher-contract: SKIP — el push no lleva ningun tip con arbol (solo borrados)"
		exit 0
	fi
	echo "check-publisher-contract: medidos $tips tip(s) del push, no el worktree."
	exit "$peor"
fi

medir "worktree" "$ROOT"; anota $?
exit "$peor"
