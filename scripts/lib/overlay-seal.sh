# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# overlay-seal.sh — a repository gate. El sello de frescura del clon hermano del overlay.
#
# ⛔ POR QUE EXISTE: UN REF CONGELADO NO ES UN VEREDICTO. El 2026-08-29 la caja entera leyo un
# `origin/main` RANCIO del overlay durante horas — el clon TRAE por SSH (que falla EN SILENCIO sin
# clave) y EMPUJA por HTTPS —, y los checkers compararon contra el ref viejo y dijeron CLEAN. No
# fallo ningun gate: todos midieron bien contra un ref que llevaba un merge de retraso.
#
# LA FORMA, decidida por r4 el 2026-08-30 sobre el censo de esta sesion: UN fetch por ACTO, no uno
# por checker. Medido: un fetch cuesta 2 307 ms, y los SIETE gobernados haciendo el suyo son ~16 s
# por corrida y siete llamadas de red por push desde cada carril. Con el sello es UN fetch.
#
#   escribe:  scripts/fetch-overlay-seal.sh   (la pata barata del gancho, junto a los registros)
#   leen:     los SIETE checkers que resuelven `origin/main` del clon hermano
#
# CONTRATO DEL LECTOR — exige DOS cosas, no una:
#   1. que el sello sea DEL MISMO ACTO: su ID de acto == el de este acto (por defecto el HEAD que
#      se empuja), `rc=0`, y edad <= OLIVARES_OVERLAY_SEAL_MAX_S (900 s) como segundo cinturon.
#      ⛔ La EDAD SOLA no demuestra «mismo acto»: un sello del push anterior con el mismo SHA y
#      menos de N segundos pasaria. Lo dijo el contraste `sol max` (A-04) y por eso hay ID.
#   2. que el SHA sellado COINCIDA con el que ese checker resuelve AHORA en el clon
# Si no puede establecer las dos, el checker sale 2 — «no he podido mirar» —, nunca 0. La segunda
# no es redundante: un sello fresco de OTRO clon, o de otro worktree, es fresco y no habla de este.

# Devuelve 0 si la frescura queda establecida; 1 si no, con OVERLAY_SEAL_WHY puesto.
overlay_seal_require() {
	local ent="$1" seal age now line srel ssha src
	OVERLAY_SEAL_WHY=""
	seal="${OLIVARES_OVERLAY_SEAL:-${OLIVARES_ROOT:-.}/.overlay-fetch-seal}"

	if [ ! -r "$seal" ]; then
		OVERLAY_SEAL_WHY="no overlay freshness seal at $seal: nobody fetched the sibling clone in"
		OVERLAY_SEAL_WHY="$OVERLAY_SEAL_WHY this act, so its origin/main is not a verdict"
		return 1
	fi
	line="$(head -1 "$seal" 2>/dev/null)" || line=""
	# forma: <epoch> <act-id> <sha40> rc=<n>
	srel="${line%% *}"
	sact="$(printf '%s' "$line" | awk '{print $2}')"
	ssha="$(printf '%s' "$line" | awk '{print $3}')"
	src="$(printf '%s' "$line" | awk '{print $4}')"
	case "$srel" in '' | *[!0-9]*)
		OVERLAY_SEAL_WHY="the seal at $seal is malformed ('$line'): it is not a verdict either"
		return 1
		;;
	esac
	if [ "$src" != "rc=0" ]; then
		OVERLAY_SEAL_WHY="the seal records a FAILED fetch ($src): the ref may be stale and nothing"
		OVERLAY_SEAL_WHY="$OVERLAY_SEAL_WHY here can tell"
		return 1
	fi
	case "$ssha" in
	[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
	*)
		OVERLAY_SEAL_WHY="the seal carries no object id ('$ssha')"
		return 1
		;;
	esac
	# ⛔ A-04 · EL ACTO SE IDENTIFICA, NO SE INFIERE DE LA EDAD. El contraste `sol max` lo dijo con
	# el caso: un sello del push ANTERIOR, con el mismo SHA y menos de N segundos, pasaba — y es
	# otro acto. Se exige que el id coincida con el de ESTE acto (por defecto, el HEAD que se
	# empuja). La edad se conserva como segundo cinturon, no como definicion.
	local act_now
	# ⚠ Sin `OLIVARES_ACT_ID` NO se inventa un id equivalente al del escritor: derivarlo del HEAD
	# aqui haria que dos reintentos del mismo commit compartieran acto (A-04). Si nadie lo fija,
	# esto no puede establecer de que acto es el sello, y eso es «no he podido mirar».
	act_now="${OLIVARES_ACT_ID:-}"
	if [ -z "$act_now" ]; then
		OVERLAY_SEAL_WHY="no act id in the environment: the act that fetched cannot be identified,"
		OVERLAY_SEAL_WHY="$OVERLAY_SEAL_WHY and freshness cannot be established from a timestamp alone"
		return 1
	fi
	if [ -z "$sact" ] || [ "$sact" != "$act_now" ]; then
		OVERLAY_SEAL_WHY="the seal belongs to act ${sact:-<none>} and this is act $act_now: it is"
		OVERLAY_SEAL_WHY="$OVERLAY_SEAL_WHY from ANOTHER act, however recent"
		return 1
	fi
	now="$(date -u +%s)"
	age=$((now - srel))
	if [ "$age" -lt 0 ] || [ "$age" -gt "${OLIVARES_OVERLAY_SEAL_MAX_S:-900}" ]; then
		OVERLAY_SEAL_WHY="the seal is ${age}s old (max ${OLIVARES_OVERLAY_SEAL_MAX_S:-900}s): it is"
		OVERLAY_SEAL_WHY="$OVERLAY_SEAL_WHY from another act, and a stale fetch is not a fresh ref"
		return 1
	fi
	live="$(git -C "$ent" rev-parse origin/main 2>/dev/null || true)"
	if [ -z "$live" ]; then
		OVERLAY_SEAL_WHY="cannot resolve origin/main in $ent to compare against the seal"
		return 1
	fi
	if [ "$live" != "$ssha" ]; then
		OVERLAY_SEAL_WHY="the seal says $ssha but this clone resolves $live: the seal is fresh and"
		OVERLAY_SEAL_WHY="$OVERLAY_SEAL_WHY does NOT describe the clone being read"
		return 1
	fi
	return 0
}
