#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# claim-lector.sh — BORRADOR. Hace VISIBLE que alguien esta leyendo un claim. NO lo impone.
#
# ⛔ LO PRIMERO, PORQUE DECIDE COMO SE USA: esto NO es un cerrojo, y no puede serlo.
# `git push --force-with-lease=<ref>:<esperado>` compara SIEMPRE el ref que empuja consigo mismo:
# su gramatica no permite decir «empuja A solo si B vale X». Asi que nada impide que alguien mueva
# `<claim>` mientras `<claim>.lector` existe. Lo que este guion da es SEÑAL: el que lee lo publica,
# el que iba a mover mira antes, y el vigia de claims lo lista. Si alguien se lo salta, se ve —
# que es exactamente lo que no se veia la noche del 2026-08-30, cuando movi un claim dos veces
# con un lector dentro y el coste lo iba a pagar el integrador.
#
# POR QUE `.lector` Y NO `.lock`, medido y no elegido por gusto:
#   · `git check-ref-format refs/integration-claims/x.lock`  -> INVALIDO (rc 1). Git reserva ese
#     sufijo para sus propios cerrojos de ref, asi que el nombre no existe como ref.
#   · `refs/integration-claims/x.lector` -> VALIDO, y `scripts/prepush-refclass.sh` lo clasifica
#     `skip` («signalling, never content»), igual que el claim base: publicar la señal no paga gate.
#     Verificado el 2026-08-30 pasandole el protocolo real por stdin.
#
# POR QUE EL REF APUNTA A UN OBJETO DE ETIQUETA Y NO AL SHA PELADO — la unica desviacion del
# encargo, y va dicha: si `.lector` apuntase al SHA leido, `libre` podria decir QUE se lee pero no
# DESDE CUANDO, porque la unica fecha disponible seria la del commit del claim, que es de otro
# momento y de otra persona. Ese es, palabra por palabra, el defecto que el canon ya tiene fichado
# del mutex `refs/gate-locks/heavy` («la edad del lock es la fecha del commit»). Un objeto de
# etiqueta anotada trae SU PROPIA fecha de creacion y su mensaje, asi que la edad es la de la
# TOMA y el lector se nombra. El SHA leido no se pierde: es el objetivo de la etiqueta.
#   Cavea honesta: esa fecha sigue siendo el reloj de quien toma. Un reloj atrasado da una toma
#   mas vieja de lo que es. No se puede arreglar desde aqui; se dice.
#
# VERBOS
#   tomar  <claim> [<sha>]  publica la señal. Sin <sha>, el que tenga el claim en el servidor.
#                           Rehusa si ya hay lector (lease contra ref inexistente).
#   soltar <claim>          borra la señal. Idempotente: si no habia, lo dice y sale 0.
#   libre  <claim>          0 si no hay lector · 1 con lector, SHA y edad · 2 si no pude mirar.
#
# Contrato de salida: 0 limpio · 1 hallazgo · 2 NO HE PODIDO MIRAR.

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ⛔ AISLAMIENTO DE ENTORNO GIT. Este guion habla con `origin` desde donde lo llamen, y su banco
# construye repositorios desechables: git EXPORTA `GIT_DIR` a los hooks desde todo worktree
# ENLAZADO y `GIT_DIR` MANDA SOBRE `-C`. Falla cerrado.
_olivares_git_env="$ROOT/scripts/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
olivares_git_env_isolate

REMOTO="${OLIVARES_CLAIM_REMOTE:-origin}"
LECTOR="${OLIVARES_LECTOR:-$(git config user.name 2>/dev/null)}"

uso() {
	cat >&2 <<'USO'
uso: claim-lector.sh tomar|soltar|libre <claim> [<sha>]
     <claim> es el nombre bajo refs/integration-claims/, sin prefijo ni sufijo.
USO
	exit 2
}

nombre_ok() { # un claim no lleva barras ni el sufijo: el sufijo lo pone este guion
	case "$1" in
	'' | *' '* | */* | *.lector) return 1 ;;
	esac
	git check-ref-format "refs/integration-claims/$1.lector"
}

ref_de() { printf 'refs/integration-claims/%s.lector' "$1"; }
ref_claim() { printf 'refs/integration-claims/%s' "$1"; }

remoto_lee() { # <ref> -> imprime el SHA, vacio si no esta; rc 2 si no pude mirar
	local out
	out="$(git ls-remote "$REMOTO" "$1" 2>/dev/null)" || return 2
	printf '%s' "$out" | awk 'NR==1{print $1}'
}

cmd_tomar() {
	local claim="$1" sha="${2:-}"
	if [ -z "$sha" ]; then
		sha="$(remoto_lee "$(ref_claim "$claim")")" || {
			echo "no he podido mirar el claim en $REMOTO" >&2
			return 2
		}
		[ -n "$sha" ] || {
			echo "el claim '$claim' no existe en $REMOTO: no hay nada que leer" >&2
			return 1
		}
	fi
	git cat-file -e "$sha^{commit}" 2>/dev/null || {
		echo "no tengo el objeto $sha; haz fetch antes de tomarlo" >&2
		return 2
	}
	local tag
	tag="$(GIT_COMMITTER_NAME="${LECTOR:-lector}" GIT_COMMITTER_EMAIL=lector@invalid \
		git mktag <<-EOT 2>/dev/null || true
			object $sha
			type commit
			tag lector
			tagger ${LECTOR:-lector} <lector@invalid> $(date +%s) +0000

			lector-v1
			leyendo $claim
			sha-leido $sha
		EOT
	)"
	[ -n "$tag" ] || {
		echo "no pude construir el objeto de señal" >&2
		return 2
	}
	# El lease contra cadena VACIA = «este ref no debe existir todavia». Es lo que impide que dos
	# lectores se pisen; NO impide que alguien mueva el claim, que no se puede (ver cabecera).
	if git push --force-with-lease="$(ref_de "$claim"):" "$REMOTO" "$tag:$(ref_de "$claim")" >/dev/null 2>&1; then
		printf 'tomado: %s lee %s en %s\n' "${LECTOR:-lector}" "$claim" "$sha"
		return 0
	fi
	local ya
	ya="$(remoto_lee "$(ref_de "$claim")")" || return 2
	if [ -n "$ya" ]; then
		echo "ya hay un lector en '$claim' — suelta el suyo o espera:" >&2
		cmd_libre "$claim" >&2
		return 1
	fi
	echo "el push de la señal fallo y el ref sigue vacio: no he podido tomarlo" >&2
	return 2
}

cmd_soltar() {
	local claim="$1" ya
	ya="$(remoto_lee "$(ref_de "$claim")")" || return 2
	[ -n "$ya" ] || {
		printf 'no habia lector en %s\n' "$claim"
		return 0
	}
	git push "$REMOTO" --delete "$(ref_de "$claim")" >/dev/null 2>&1 || {
		echo "no pude borrar la señal de '$claim'" >&2
		return 2
	}
	printf 'soltado: %s\n' "$claim"
}

cmd_libre() {
	local claim="$1" sha_senal sha_claim
	sha_senal="$(remoto_lee "$(ref_de "$claim")")" || {
		echo "no he podido mirar $REMOTO" >&2
		return 2
	}
	sha_claim="$(remoto_lee "$(ref_claim "$claim")")" || {
		echo "no he podido mirar el claim en $REMOTO" >&2
		return 2
	}
	if [ -z "$sha_senal" ]; then
		# Relectura por la MISMA razon: decir «libre» cuando ya hay un lector es la respuesta
		# falsa mas cara de este guion, porque autoriza a mover.
		local segunda
		segunda="$(remoto_lee "$(ref_de "$claim")")" || return 2
		if [ -n "$segunda" ]; then
			printf 'NO HE PODIDO MIRAR: aparecio un lector en %s mientras miraba\n' "$claim" >&2
			return 2
		fi
		printf 'libre: %s no tiene lector\n' "$claim"
		return 0
	fi
	# ⛔ SEÑAL SIN FUENTE = NO HE PODIDO MIRAR, nunca «libre» ni «ocupado». Un lector sobre un claim
	#    que no existe es un estado que este guion no sabe interpretar: puede ser un claim retirado
	#    con su lector dentro (y entonces alguien esta leyendo humo) o una señal huerfana. Las dos
	#    piden intervencion, y ninguna se parece a «adelante».
	if [ -z "$sha_claim" ]; then
		printf 'NO HE PODIDO MIRAR: %s tiene lector pero el claim NO existe en %s\n' "$claim" "$REMOTO" >&2
		return 2
	fi
	# ⛔ LA VENTANA ENTRE LAS DOS LECTURAS, Y NO SE TAPA CON PROSA. Entre leer la señal y leer el
	#    claim cabe que alguien TOME la señal: entonces la primera lectura vio vacio y este guion
	#    contestaria «libre» con un lector dentro — una respuesta FALSA, que es peor que ninguna.
	#    No se puede cerrar (dos `ls-remote` no son una operacion), pero SI se puede convertir en
	#    fail-closed: se relee la señal al final y, si cambio bajo los pies, la respuesta es 2.
	local senal_final
	senal_final="$(remoto_lee "$(ref_de "$claim")")" || return 2
	if [ "$senal_final" != "$sha_senal" ]; then
		printf 'NO HE PODIDO MIRAR: la señal de %s cambio mientras la leia\n' "$claim" >&2
		return 2
	fi
	git cat-file -e "$sha_senal" 2>/dev/null || git fetch -q "$REMOTO" "$(ref_de "$claim")" >/dev/null 2>&1
	local cuerpo quien cuando objetivo edad
	cuerpo="$(git cat-file tag "$sha_senal" 2>/dev/null)" || {
		printf 'NO HE PODIDO MIRAR: %s tiene lector (%s) y no puedo leer la señal\n' "$claim" "$sha_senal" >&2
		return 2
	}
	# La autoridad va VERSIONADA: una señal de un formato que este guion no conoce no se interpreta
	# a medias. Sin esto, añadir un campo mañana haria que un lector viejo leyera basura como dato.
	# ⛔ LINEA EXACTA, NO SUBCADENA. `*"lector-v1"*` acepta `lector-v10` como si fuera v1 —una
	#    version FUTURA leida por un guion viejo, que es justo lo que el versionado existe para
	#    impedir— mientras `lector-v2` si daba 2. El marcador se compara contra la LINEA entera.
	case "$cuerpo" in
	*$'\n'"lector-v1"$'\n'* | "lector-v1"$'\n'*) ;;
	*)
		printf 'NO HE PODIDO MIRAR: la señal de %s no declara lector-v1\n' "$claim" >&2
		return 2 ;;
	esac
	objetivo="$(printf '%s' "$cuerpo" | awk '/^object /{print $2; exit}')"
	quien="$(printf '%s' "$cuerpo" | sed -n 's/^tagger \(.*\) <.*/\1/p' | head -1)"
	cuando="$(printf '%s' "$cuerpo" | sed -n 's/^tagger .*> \([0-9][0-9]*\) .*/\1/p' | head -1)"
	if [ -n "$cuando" ]; then
		edad="$(( ( $(date +%s) - cuando ) / 60 ))"
	else
		edad="?"
	fi
	# ⛔ Y LA COMPROBACION QUE DA SENTIDO A TODO: la señal PINCHA el SHA que se estaba leyendo. Si el
	#    claim ya no vale eso, alguien lo movio con un lector dentro. El guion no puede impedirlo
	#    —eso lo intenta el gancho— pero SI puede hacerlo visible despues, que es lo que ninguna
	#    version anterior hacia: leian «ocupado» y nadie comparaba con la fuente.
	if [ -n "$objetivo" ] && [ "$objetivo" != "$sha_claim" ]; then
		printf 'MOVIDO BAJO LECTOR: %s lo lee %s desde hace %s min sobre %s, y el claim vale AHORA %s\n' \
			"$claim" "${quien:-?}" "$edad" "$objetivo" "$sha_claim"
		return 1
	fi
	printf 'ocupado: %s lo lee %s desde hace %s min (SHA leido %s)\n' \
		"$claim" "${quien:-?}" "$edad" "${objetivo:-?}"
	return 1
}

[ $# -ge 2 ] || uso
verbo="$1"
claim="$2"
nombre_ok "$claim" || {
	echo "nombre de claim invalido: '$claim'" >&2
	exit 2
}
case "$verbo" in
tomar) cmd_tomar "$claim" "${3:-}" ;;
soltar) cmd_soltar "$claim" ;;
libre) cmd_libre "$claim" ;;
*) uso ;;
esac
