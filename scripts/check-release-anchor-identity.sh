#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-release-anchor-identity.sh — the anchors in EFFECT must be the anchors we REVIEWED.
#
# ⛔ WHY THIS EXISTS, and it is not a hypothetical: measured 2026-08-28.
#
# Commit 9354dd555 (2026-08-23) rotated both production public anchors "after a traced command
# exposed the private halves". The rotation landed in the tree. It never landed in the GitHub
# repository variables of olivaresai/olivares, whose `updated_at` still read 2026-07-27 — four
# weeks EARLIER. For five days the release pipeline was configured to bake the anchors whose
# private halves were known to be exposed, and NOTHING went red:
#
#   · scripts/check-release-pubkey.sh validates FORM — base64-std, 32 bytes, the two distinct.
#     A compromised anchor is perfectly well-formed, so it passes.
#   · scripts/release-preflight.sh §C.4.8 computes a fingerprint and PRINTS it. Printing a
#     fingerprint is not comparing it. Measured with the pre-rotation OTA anchor injected:
#     `release-preflight: OK — production profile validated`, rc=0. The mutant SURVIVED.
#
# ⇒ Form was gated, identity was not, and the pair (tree, variables) had no reader at all. This
# script is that reader. It answers exactly one question: does the anchor that will be BUILT IN
# equal the anchor that was reviewed and committed?
#
# ⛔ AND IT LIVES IN THE HUB ON PURPOSE. `design/` does NOT travel to the exported tree (measured:
# `export-public.sh --manifest` lists 0 paths under `design/`), so the public repo cannot read
# `an internal design note (not shipped)*.pub` and cannot check its own anchors against them. A control over the
# pair has to run where BOTH halves are visible, and that is here.
#
# Modes:
#   (default)  compare $OLIVARES_LICENSE_PUBKEY / $OLIVARES_OTA_PUBKEY against the tree.
#              This is the form a release job can call.
#   --live     fetch the two variables from the release repository with `gh` and compare those.
#              This is the form that catches configuration drift, which is what actually happened.
#   --selftest run the battery in scripts/test-release-anchor-identity.sh.
#
# Exit codes, and the distinction is the whole point:
#   0  the anchors in effect ARE the reviewed anchors.
#   1  MISMATCH — a specific anchor differs. Named, with both fingerprints.
#   2  NO HE PODIDO MIRAR — missing tool, missing file, missing credential, empty value.
#      "I could not look" is never reported as "it is fine".
set -u

REPO="${OLIVARES_RELEASE_REPO:-olivaresai/olivares}"
ROOT="${OLIVARES_ANCHOR_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)}"
DIR="${OLIVARES_ANCHOR_DIR:-$ROOT/design/claves-o03}"

# ⛔ CONCIENCIA DE PERFIL (2026-08-30, medido). Hasta hoy este control comparaba SIEMPRE
# contra `prod-*.pub` y contra la única fila de la tabla, que lleva las anclas de PROD. Un release
# con perfil `preprod` embarca por diseño las de SANDBOX —su control hermano lo dice en su verde:
# «both are the sandbox keys»—, así que bajo `preprod` este gate daba MISMATCH SIEMPRE. Medido en
# el paso 10 del release del espejo: in effect 0e73e1a0/ae8ae956 (sandbox) contra reviewed
# 5144ae08/1eee9d76 (prod), rc=1. Y el daño no es el rojo: es un rojo de seguridad FALSO en el
# único gate cuya virtud entera es que nadie lo ignore.
# ⛔ DENY-CLOSED, y la forma importa. `${VAR-default}` y NO `${VAR:-default}`: la forma con dos
# puntos sustituye tambien con valor VACIO, asi que una expresion de CI que resuelva a "" —una
# variable de repositorio sin definir, un typo en `vars.`— comprobaria en silencio las anclas de
# PROD para una build que pidio otra cosa. Sin definir = production, porque todos los llamadores
# existentes corren sin la variable; definida-pero-vacia es una mala configuracion y se rehusa.
# Un perfil desconocido tampoco cae a un lado por defecto: las anclas deciden que licencia acepta
# un binario y de quien se fia para recibir codigo nuevo, y adivinar cual comprobar no es un
# defecto seguro. (La forma la fija `ent:scripts/check-release-anchor-domains.sh`, que resolvio
# esto antes y mejor; aqui se copia su contrato a proposito para que los dos controles hermanos
# no discrepen sobre que perfiles existen.)
PROFILE="${OLIVARES_RELEASE_PROFILE-production}"
case "$PROFILE" in
production) ANCHOR_PREFIX=prod ;;
preprod) ANCHOR_PREFIX=sandbox ;;
*)
	echo "check-release-anchor-identity: perfil de release desconocido '$PROFILE'." >&2
	echo "  Perfiles conocidos: production, preprod. Fija OLIVARES_RELEASE_PROFILE a uno de ellos." >&2
	exit 2
	;;
esac

fail2() { echo "check-release-anchor-identity: ⛔ NO HE PODIDO MIRAR: $1" >&2; exit 2; }

if [ "${1:-}" = "--selftest" ]; then
	exec sh "$ROOT/scripts/test-release-anchor-identity.sh"
fi

for _t in base64 sha256sum tr; do
	command -v "$_t" >/dev/null 2>&1 || fail2 "'$_t' is not on this host, so nothing was compared."
done

LIVE=0
[ "${1:-}" = "--live" ] && LIVE=1

# fingerprint of the RAW 32 bytes: the same form commercial/license-worker/src/license/sign.ts:39
# documents for key_id, so a fingerprint printed here can be matched against an issued licence.
fp() { printf '%s' "$1" | base64 -d 2>/dev/null | sha256sum 2>/dev/null | cut -c1-16; }
trim() { tr -d ' \t\r\n'; }

# The reviewed anchor has TWO homes and the gate must work in both trees:
#   · docs/RELEASE-VERIFICATION.md — the PUBLISHED contract. It travels to the exported tree, so
#     this is the half release-preflight.sh can read when it runs from the public checkout.
#   · an internal design note (not shipped)*.pub — the ceremony record. Hub only; `design/` is not exported.
# When BOTH are readable they must agree: two homes that disagree is itself the defect, and
# silently preferring one would hide it.
TABLE="${OLIVARES_ANCHOR_TABLE:-$ROOT/docs/RELEASE-VERIFICATION.md}"

# Pull the base64 value for a domain out of the published table. The row is
# `| vX.Y.Z | license | \`<key>\` | <sha256> | <prefix> |`; we take the third cell of the last
# matching row so a newer release supersedes an older one.
from_table() { # <license|ota>
	[ -r "$TABLE" ] || return 1
	awk -v dom="$2" -F'|' '
		$0 ~ /^\|/ {
			d = $3; gsub(/^[ \t]+|[ \t]+$/, "", d)
			if (d == dom) { v = $4; gsub(/^[ \t]*`?|`?[ \t]*$/, "", v); last = v }
		}
		END { if (last != "") print last }
	' "$1" 2>/dev/null
}

rc=0
for name in LICENSE OTA; do
	case "$name" in
	LICENSE) file="$DIR/${ANCHOR_PREFIX}-license.pub"; dom=license ;;
	OTA) file="$DIR/${ANCHOR_PREFIX}-ota.pub"; dom=OTA ;;
	esac

	want=""; src=""
	if [ -r "$file" ]; then
		want="$(trim <"$file")"
		[ -n "$want" ] || fail2 "$file is empty — an empty reviewed anchor would make every value 'match'."
		src="$file"
	fi
	# La tabla publicada NO tiene columna de perfil: sus filas son las de PROD (`| v26.8.0 | license |
	# …`). Consultarla bajo otro perfil compara sandbox contra prod, que es el falso rojo de arriba.
	# Bajo un perfil no-prod el ancla revisada sale SÓLO del registro de ceremonia, y si no está,
	# se rehúsa con 2 — que es lo honesto: «no he podido mirar» nunca se reporta como «está bien».
	tbl=""
	if [ "$ANCHOR_PREFIX" = prod ]; then
		tbl="$(from_table "$TABLE" "$dom" | trim)"
	fi
	if [ -n "$tbl" ]; then
		if [ -n "$want" ] && [ "$tbl" != "$want" ]; then
			echo "check-release-anchor-identity: ⛔ ${name}: the two REVIEWED homes disagree." >&2
			echo "    ceremony record ($file):  sha256/raw32 $(fp "$want")" >&2
			echo "    published table ($TABLE): sha256/raw32 $(fp "$tbl")" >&2
			echo "    Fix the disagreement before trusting either; this gate refuses to pick a winner." >&2
			rc=1
			continue
		fi
		[ -n "$want" ] || { want="$tbl"; src="$TABLE"; }
	fi
	[ -n "$want" ] || fail2 "no reviewed ${name} anchor is readable — neither $file nor a row in $TABLE. Without the reviewed half there is nothing to compare against."

	if [ "$LIVE" -eq 1 ]; then
		command -v gh >/dev/null 2>&1 || fail2 "--live needs 'gh' and it is not on this host."
		# ⛔ ORDEN DELIBERADO AL RESOLVER (rebase, 2026-08-31): mi guarda de coherencia
		# perfil/repositorio va PRIMERO porque es una PRECONDICION —si los entornos no casan,
		# la comparacion no significa nada y no hay que gastar la llamada—; la lectura de abajo
		# es la de `main` (040e4c207), NO la mia. La mia era la version de UNA linea cuyo `| trim`
		# se comia el codigo de salida de `gh`, que es exactamente el defecto que ese commit cura:
		# quedarme con mi lado entero habria REVERTIDO su arreglo mientras el fichero parecia mio.
		# ⛔ EL PERFIL Y EL REPOSITORIO TIENEN QUE CASAR, y este guard existe por un defecto que
		# introduje yo al dar conciencia de perfil (2026-08-30). `REPO` cae por defecto al
		# repo de PRODUCCION y `PROFILE` cae por defecto a `production` — coherente mientras nadie
		# toque uno solo. Fijar OLIVARES_RELEASE_PROFILE=preprod SIN fijar OLIVARES_RELEASE_REPO
		# leeria las variables vivas de PRODUCCION y las juzgaria contra las anclas de SANDBOX:
		# mismatch garantizado y falso. Antes de mi cambio no podia pasar, porque el perfil no
		# existia para este guion. No se adivina cual de los dos quiso decir: se rehusa.
		if [ "$ANCHOR_PREFIX" != prod ] && [ "$REPO" = "olivaresai/olivares" ]; then
			fail2 "--live con perfil '$PROFILE' contra el repositorio de produccion ($REPO). Las variables vivas y las anclas revisadas serian de entornos distintos, y la comparacion no significaria nada. Fija OLIVARES_RELEASE_REPO al repositorio de ese perfil."
		fi

		# ⛔ NOT `gh ... | trim` IN ONE GO, and this is measured, not stylistic (2026-08-29).
		#
		# On an HTTP error `gh api` prints the JSON error BODY to STDOUT and exits non-zero. Piped
		# straight into `trim`, three things happen at once and each one hides the next: the pipe's
		# status is `trim`'s, so gh's failure is lost; `$got` is NON-EMPTY (172 bytes of
		# `{"message":"Resource not accessible by personal access token",...}`), so the emptiness
		# guard below never fires; and `fp()` base64-decodes that JSON, `base64 -d` fails into
		# /dev/null, and sha256 of nothing yields e3b0c44298fc1c14 — the digest of the EMPTY STRING.
		#
		# Measured against olivaresai/olivares with a personal PAT that cannot read its variables:
		# the gate answered `⛔ LICENSE MISMATCH … in effect: e3b0c44298fc1c14` and exited **1**,
		# telling the operator to "fix the deployment, not this gate" when the deployment was never
		# read. That is the third answer reported as the second — and this gate is entry condition
		# C3 of the public act (docs/RELEASE-GO-LIVE-RUNBOOK.md:512-527).
		#
		# Two guards, because the first alone closes only the instance:
		#   1 · gh's own status, captured OUT of a pipeline.
		#   2 · the SHAPE of what came back. An anchor is base64 of exactly 32 bytes; anything else
		#       — an error body, a proxy's HTML, a truncated value — is "I could not look", never
		#       "it does not match". A real mismatch is between two WELL-FORMED keys, so this
		#       cannot mask one.
		got_raw="$(gh api "repos/$REPO/actions/variables/OLIVARES_${name}_PUBKEY" --jq .value 2>/dev/null)" \
			|| fail2 "gh could not read OLIVARES_${name}_PUBKEY from $REPO (HTTP error, or the credential cannot see this repository's variables). An unreadable variable is not a matching one."
		got="$(printf '%s' "$got_raw" | trim)"
		[ -n "$got" ] || fail2 "could not read OLIVARES_${name}_PUBKEY from $REPO (absent, or the credential cannot see it). An unreadable variable is not a matching one."
		[ "$(printf '%s' "$got" | base64 -d 2>/dev/null | wc -c)" -eq 32 ] \
			|| fail2 "OLIVARES_${name}_PUBKEY from $REPO is not base64 of 32 bytes, so it is not an anchor at all — nothing was compared. (An HTTP error body reaches stdout and looks like a value.)"
		where="$REPO variable"
	else
		eval "got=\"\${OLIVARES_${name}_PUBKEY:-}\""
		got="$(printf '%s' "$got" | trim)"
		[ -n "$got" ] || fail2 "OLIVARES_${name}_PUBKEY is unset/empty. The caller must declare the anchor; an absent value never means 'unchanged'."
		where="environment"
	fi

	if [ "$got" = "$want" ]; then
		echo "check-release-anchor-identity: ${name} anchor matches the reviewed one (sha256/raw32 $(fp "$want"))"
	else
		echo "check-release-anchor-identity: ⛔ ${name} MISMATCH — the anchor in effect is not the reviewed one." >&2
		echo "    in effect ($where):  sha256/raw32 $(fp "$got")" >&2
		echo "    reviewed  ($src):   sha256/raw32 $(fp "$want")" >&2
		echo "    A well-formed anchor is not a correct anchor. If the reviewed half is the newer one," >&2
		echo "    the deployment never received the rotation; fix the deployment, not this gate." >&2
		rc=1
	fi
done

# ⛔ UN VERDE DE preprod NO SE PUEDE SOSTENER, y el defecto es MIO: lo introduje al dar perfiles a
# este control. `preprod` resuelve a an internal design note (not shipped) (huella 11a7693c), que
# es el REGISTRO de la ceremonia O03 y NO la clave con la que firma el worker de sandbox
# desplegado — un tercer par independiente (key id 0e73e1a0) cuya mitad publica ni siquiera vive
# en el arbol. Medido por contra el despliegue vivo y escrito al lado del propio fichero,
# en an internal design note (not shipped): `license verify` con el registro da rc=1
# «signature invalid», y con la clave real rc=0.
#
# Asi que casar con el registro NO prueba que el ancla sea la correcta: es un VERDE FALSO. Y no
# se arregla apuntando a la otra, porque cual de los dos registros manda es una pregunta de la
# CEREMONIA y no de la medida (por eso ese LEEME no toca el .pub: falsear un acta seria peor que
# el desacuerdo). Este control dice entonces lo unico que puede sostener — que no ha podido
# mirar — en vez de bendecir un ancla que no ha comparado con quien firma.
#
# Se rehusa AQUI, y no en el `case` de perfil de arriba, a proposito: alli cortaria antes de la
# guarda de coherencia de --live y antes de detectar las anclas de PROD embarcadas en un preprod,
# que son DOS hallazgos verdaderos. Un rojo real vale mas que un «no he podido mirar».
if [ "$rc" -eq 0 ] && [ "$ANCHOR_PREFIX" != prod ]; then
	echo "check-release-anchor-identity: ⛔ NO HE PODIDO MIRAR: las anclas de '$PROFILE' casan con el" >&2
	echo "    registro de la ceremonia O03 ($DIR/${ANCHOR_PREFIX}-license.pub), que NO es la clave con" >&2
	echo "    la que firma el worker de sandbox (key id 0e73e1a0). Casar con el registro no prueba" >&2
	echo "    identidad. Ver design/claves-o03/LEEME-SANDBOX-LICENSE.md; cual de las dos manda lo" >&2
	echo "    decide la ceremonia, no este gate." >&2
	exit 2
fi
[ "$rc" -eq 0 ] && echo "check-release-anchor-identity: OK — both anchors in effect are the reviewed ones."
exit "$rc"
