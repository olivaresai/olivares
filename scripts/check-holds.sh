#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# check-holds.sh — INT-23. Que ningún lote pise un HOLD o un veto VIGENTE.
#
# ⛔ POR QUÉ ES UN GATE Y NO UN DOCUMENTO. La fila pedía «registrar los HOLDs para que ningún lote
# los pise». Un registro que sólo se lee no impide nada, y este proyecto ya tiene nombrado que una
# promesa en un comentario no es un control. La fuente es an internal design note (not shipped) y este
# guion la ejecuta.
#
# ⛔ QUÉ SE CONSIDERA «PISAR». DOS FASES, y la segunda existe porque la primera SOLA daba un
# VERDE FALSO medido: un lote de 26 PRs montado con `git merge <ref>` —cuyos asuntos NO llevan
# número de PR— incorporó #675, que está en HOLD, y este gate dijo «4 vetos vigentes, 26 merges,
# ninguno los pisa» y salió 0. No falló: la fase 1 no podía verlo.
#
# FASE 1 · POR MENSAJE. Un commit que sea un MERGE DE VERDAD —dos o más padres— Y que nombre el PR
# vetado en su mensaje. Mencionar `#709` en prosa NO bloquea: hoy mismo hay commits de este árbol
# que lo citan al explicar por qué no se mergea, y un gate que los rechazara enseñaría a saltárselo.
#
# FASE 2 · POR CONTENCIÓN. Resuelve `refs/pull/<N>/head` y pregunta si esa punta está contenida en
# lo que el push VA A PUBLICAR y no en la base. Su detalle, y las cuatro cegueras que una primera
# versión suya introdujo y un contraste `sol max` reprodujo, están escritos donde empieza la fase.
#
# Salidas: 0 = limpio · 1 = un lote pisa un veto · 2 = NO HE PODIDO MIRAR (sin registro legible o
# sin poder enumerar los commits). Sin el registro, «no hay vetos pisados» sería cierto por vacuidad.
set -euo pipefail

RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$RAIZ" ] || { echo "check-holds: ⛔ NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
REG="${OLIVARES_HOLDS_FILE:-$RAIZ/design/HOLDS-Y-VETOS-VIGENTES.md}"
[ -r "$REG" ] || { echo "check-holds: ⛔ NO HE PODIDO MIRAR: no puedo leer $REG." >&2; exit 2; }

# Filas con número de PR y veredicto bloqueante. Una fila con `—` en la columna PR es informativa
# para el humano y NO ejecutable: se ignora a propósito, no por descuido.
vetados="$(sed -n 's/^| \*\*#\([0-9]\{1,\}\)\*\* |.*/\1/p' "$REG" | sort -u)"
n_vet="$(printf '%s\n' "$vetados" | grep -c . || true)"
if [ "${n_vet:-0}" -eq 0 ]; then
	echo "check-holds: ⛔ NO HE PODIDO MIRAR: el registro no rindió ni un PR vetado." >&2
	echo "             Si de verdad no hay ninguno, la tabla lo diría con una fila; cero filas" >&2
	echo "             legibles es un formato que cambió, y eso NO es un árbol limpio." >&2
	exit 2
fi

# ⛔ EL RANGO ERA `HEAD --not --remotes`, Y ESO HACE INVISIBLE UN VETO EN CUANTO SE PUBLICA.
# Contraste Codex `sol max` del 2026-08-20 (ALTA, VERIFICADO) y re-medido aqui en un fixture: un
# merge cuyo mensaje es `Merge pull request #709 …` da rc 1 mientras es solo local y **rc 0 en
# cuanto `git update-ref refs/remotes/origin/staging <merge>` lo hace alcanzable**. No hace falta
# tocar HEAD ni el contenido — y publicar la rama antes de promocionarla es el camino NORMAL aqui,
# asi que el veto dejaba de aplicarse justo cuando el merge empezaba a existir para los demas.
#
# La pregunta que este gate contesta es «¿lo que voy a empujar mete en `main` un merge vetado?»,
# y eso es `HEAD --not origin/main`, no «lo que aun no esta en ningun remoto». Medido antes de
# cambiarlo: en el fixture pasa de rc 0 a rc 1, y en el repo real da rc 0 sobre cuatro ramas
# (`origin/main` incluida), o sea CERO falsos positivos. Si `origin/main` no resuelve, se vuelve
# al rango antiguo y se dice — un gate que no sabe contra que comparar no debe inventarselo.
if [ -n "${OLIVARES_HOLDS_RANGE:-}" ]; then
	RANGO="$OLIVARES_HOLDS_RANGE"
elif git -C "$RAIZ" rev-parse -q --verify origin/main >/dev/null 2>&1; then
	RANGO="HEAD --not origin/main"
else
	RANGO="HEAD --not --remotes"
	echo "check-holds: aviso — no hay origin/main; comparo contra los remotos locales." >&2
fi
# ⛔ `|| true` AQUI CONVIERTE UN RANGO ILEGIBLE EN «no hay merges que mirar», y este guarda es
# el que hace cumplir los HOLD — hoy, #709 «NO SE MERGEA». Medido el 2026-08-20:
# `OLIVARES_HOLDS_RANGE=rama-que-no-existe` daba **rc 0 en silencio**. El resto del fichero
# rehusa bien —registro ilegible y registro que no rinde vetos son los dos NO HE PODIDO MIRAR—,
# asi que este era el unico camino por el que la ceguera salia limpia.
# Y la forma importa: con `set -e`, `commits="$(git …)"` a secas mata el guion con el 128 de git
# ANTES de poder decir por que. Un 128 mudo no es ninguna de las tres respuestas.
# shellcheck disable=SC2086
if ! commits="$(git -C "$RAIZ" rev-list --merges $RANGO 2>/dev/null)"; then
	echo "check-holds: ⛔ NO HE PODIDO MIRAR: el rango '$RANGO' no es legible para git." >&2
	exit 2
fi
n_merges="$(printf '%s\n' "$commits" | grep -c . || true)"

hallazgos=0
for c in $commits; do
	# ⛔ `|| true` AQUI ES LA MISMA CEGUERA QUE ARRIBA, Y YO AFIRME QUE EL RANGO ERA «el unico
	# camino por el que la ceguera salia limpia». Falso, y lo probo el contraste Codex `sol max`
	# del 2026-08-20 (ALTA, VERIFICADO) con un doble de `git` que dejaba correr `rev-list` y hacia
	# fallar solo `log`: con un merge real que nombra #709, el gate dijo «1 merge, ninguno los
	# pisa» y salio 0. Un mensaje que no se puede leer no es un mensaje que no nombra nada.
	if ! msg="$(git -C "$RAIZ" log -1 --format='%B' "$c" 2>/dev/null)"; then
		echo "check-holds: ⛔ NO HE PODIDO MIRAR: no pude leer el mensaje del merge ${c}." >&2
		exit 2
	fi
	for pr in $vetados; do
		case "$msg" in
			*"#${pr}"*)
				motivo="$(sed -n "s/^| \*\*#${pr}\*\* | \([^|]*\) | \([^|]*\) |.*/\1 → \2/p" "$REG" | head -1)"
				echo "check-holds: ⛔ ${c:0:9} es un MERGE y nombra #${pr}, que está vetado."
				echo "             ${motivo}"
				hallazgos=$((hallazgos + 1))
				;;
		esac
	done
done

# ── FASE 2 · CONTENCIÓN ──────────────────────────────────────────────────────────────────────
# ⛔ REESCRITA el 2026-08-25 tras un contraste `sol max` que declaró NO APTA la primera versión
# con CUATRO caminos de `rc 0` reproducidos. Lo que aquella hacía mal, y por qué esto es distinto:
#
#   1. EL SUJETO ERA `HEAD`. El hook captura los OID que se van a PUBLICAR y los deja en
#      `OLIVARES_PUSH_REFS_FILE` (.githooks/pre-push:206-251). Mirar `HEAD` certifica el CHECKOUT,
#      no lo que aterriza: con `HEAD` limpio y `git push origin lote:main`, el gate daba VERDE.
#   2. `if git merge-base --is-ancestor` TRAGABA EL 128. Bash no distingue el `rc 1` de «no es
#      ancestro» de un error de git, así que un fallo salía por el camino del verde.
#   3. La base se daba por buena aunque `origin/main` no resolviera.
#
# Los tres veredictos se respetan: 0 limpio · 1 un veto se pisa · 2 NO HE PODIDO MIRAR.

# `es_ancestro <a> <b>` — 0 sí · 1 no · 2 no he podido mirar. La CLAVE es que el 2 exista:
# `merge-base` devuelve 128 con un objeto ausente o una historia shallow, y eso NO es «no es
# ancestro». Sin esta separación, un repositorio incompleto certifica limpio cualquier lote.
# ⛔ Y LA HISTORIA SHALLOW NO DA ERROR: MIENTE. Medido el 2026-08-25 con git real sobre un clon
# `--depth 1`: preguntar si un commit ANTERIOR al corte es ancestro de HEAD devuelve **1 («no es
# ancestro»)**, no 128 — y en la historia completa SÍ lo es. Un falso negativo con aspecto de
# respuesta legítima, que ninguna clasificación de códigos puede distinguir. Por eso en un
# repositorio shallow un NEGATIVO es PARCIAL: la respuesta afirmativa sigue valiendo (si dice que
# está contenida, lo está), pero «no está» sólo significa «no está en la parte que tengo».
es_shallow() {
	[ "$(git -C "$RAIZ" rev-parse --is-shallow-repository 2>/dev/null)" = "true" ]
}

es_ancestro() {
	git -C "$RAIZ" merge-base --is-ancestor "$1" "$2" 2>/dev/null
	case "$?" in
		0) return 0 ;;
		1) if es_shallow; then return 2; else return 1; fi ;;
		*) return 2 ;;
	esac
}

# El SUJETO: los OID que el push va a publicar, o `HEAD` sólo en modo manual y DICIÉNDOLO.
sujetos=""
modo_sujeto=""
if [ -n "${OLIVARES_PUSH_REFS_FILE:-}" ] && [ -r "${OLIVARES_PUSH_REFS_FILE:-}" ]; then
	modo_sujeto="push"
	# Protocolo pre-push: <local_ref> <local_oid> <remote_ref> <remote_oid>. Un borrado trae el
	# local_oid a ceros y no aterriza nada, así que no es sujeto.
	while read -r _lref loid _rref _roid; do
		[ -z "${loid:-}" ] && continue
		case "$loid" in *[!0]*) ;; *) continue ;; esac
		sujetos="$sujetos $loid"
	done < "$OLIVARES_PUSH_REFS_FILE"
	if [ -z "$sujetos" ]; then
		echo "check-holds: OK — el push no publica ningún OID (borrados o entrada vacía)."
		exit 0
	fi
else
	modo_sujeto="manual"
	sujetos="$(git -C "$RAIZ" rev-parse HEAD 2>/dev/null || true)"
	if [ -z "$sujetos" ]; then
		echo "check-holds: ⛔ NO HE PODIDO MIRAR: sin OLIVARES_PUSH_REFS_FILE y sin HEAD legible." >&2
		exit 2
	fi
	echo "check-holds: modo MANUAL — el sujeto es HEAD, no los OID de un push." >&2
fi

# La BASE. Si no resuelve NO se afirma «no está en el tronco»: se rehúsa.
base_ref=""
if git -C "$RAIZ" rev-parse -q --verify origin/main >/dev/null 2>&1; then
	base_ref="$(git -C "$RAIZ" rev-parse origin/main)"
fi

n_ciegos=0
for pr in $vetados; do
	punta=""
	# ⛔ EL REMOTO PRIMERO, y es lo contrario de lo que hacía la v1. Una ref local `refs/pull/N/head`
	# puede estar CONGELADA de un fetch viejo mientras el autor reescribió su rama; preferirla era
	# certificar contra una revisión que ya no existe. El local sólo se usa si el remoto no contesta,
	# y entonces el veredicto lo dice.
	if salida="$(git -C "$RAIZ" ls-remote origin "refs/pull/${pr}/head" 2>/dev/null)"; then
		punta="$(printf '%s' "$salida" | cut -f1)"
	fi
	origen="remoto"
	if [ -z "$punta" ] && git -C "$RAIZ" rev-parse -q --verify "refs/pull/${pr}/head" >/dev/null 2>&1; then
		punta="$(git -C "$RAIZ" rev-parse "refs/pull/${pr}/head")"
		origen="ref local (el remoto no contestó; puede estar desfasada)"
	fi
	if [ -z "$punta" ]; then
		n_ciegos=$((n_ciegos + 1))
		echo "check-holds: ⚠ PARCIAL — no pude resolver refs/pull/${pr}/head ni por el remoto ni en" >&2
		echo "             local; de #${pr} sólo he mirado los MENSAJES. Eso NO es «limpio»." >&2
		continue
	fi
	if ! git -C "$RAIZ" cat-file -e "${punta}^{commit}" 2>/dev/null; then
		git -C "$RAIZ" fetch -q origin "refs/pull/${pr}/head" >/dev/null 2>&1 || true
	fi
	if ! git -C "$RAIZ" cat-file -e "${punta}^{commit}" 2>/dev/null; then
		n_ciegos=$((n_ciegos + 1))
		echo "check-holds: ⚠ PARCIAL — ${punta} (#${pr}) no está en este objeto-store y no pude" >&2
		echo "             traerlo; de #${pr} sólo he mirado los MENSAJES." >&2
		continue
	fi
	for sujeto in $sujetos; do
		# ⛔ `es_ancestro ...; r=$?` A SECAS MATA EL GUION: es una llamada DESNUDA que devuelve
		# no-cero y `set -e` la fulmina antes de que nadie lea `$?`. Es la TERCERA vez que esta
		# forma muerde en este fichero — su cabecera ya avisa de la variante con `$(git ...)`.
		# Con `|| r=$?` el fallo queda TESTADO y `set -e` no dispara.
		r=0; es_ancestro "$punta" "$sujeto" || r=$?
		if [ "$r" -eq 2 ]; then
			n_ciegos=$((n_ciegos + 1))
			if es_shallow; then
				echo "check-holds: ⚠ PARCIAL — este repositorio es SHALLOW y #${pr} sale «no contenida»" >&2
				echo "             en ${sujeto}. Medido: un clon --depth 1 responde 1 (no 128) para un" >&2
				echo "             commit anterior al corte que SÍ es ancestro. «No está» aquí sólo" >&2
				echo "             significa «no está en la parte que tengo»." >&2
			else
				echo "check-holds: ⚠ PARCIAL — merge-base falló al preguntar si #${pr} está en" >&2
				echo "             ${sujeto}. Un error NO es «no es ancestro»." >&2
			fi
			continue
		fi
		[ "$r" -eq 1 ] && continue
		# Está contenida. ¿Es estado del tronco o lo mete ESTE push?
		if [ -z "$base_ref" ]; then
			n_ciegos=$((n_ciegos + 1))
			echo "check-holds: ⚠ PARCIAL — #${pr} está en ${sujeto}, pero sin \`origin/main\` no puedo" >&2
			echo "             decir si ya estaba en el tronco. No afirmo ninguna de las dos cosas." >&2
			continue
		fi
		rb=0; es_ancestro "$punta" "$base_ref" || rb=$?
		if [ "$rb" -eq 2 ]; then
			n_ciegos=$((n_ciegos + 1))
			echo "check-holds: ⚠ PARCIAL — #${pr} está en ${sujeto} y merge-base falló contra la base." >&2
			continue
		fi
		[ "$rb" -eq 0 ] && continue
		motivo="$(sed -n "s/^| \*\*#${pr}\*\* | \([^|]*\) | \([^|]*\) |.*/\1 → \2/p" "$REG" | head -1)"
		echo "check-holds: ⛔ la punta de #${pr} (${punta}) está CONTENIDA en ${sujeto} y no en la base."
		echo "             Sujeto tomado del modo ${modo_sujeto}; punta resuelta por ${origen}."
		echo "             Ningún mensaje la nombra —un 'git merge <ref>' no escribe el número— pero"
		echo "             su árbol entra igual. ${motivo}"
		hallazgos=$((hallazgos + 1))
	done
done

if [ "$hallazgos" -gt 0 ]; then
	echo "check-holds: ⛔ $hallazgos merge(s) pisan un veto vigente. Si el veto ya no vale," >&2
	echo "             EDITA design/HOLDS-Y-VETOS-VIGENTES.md en este mismo commit y di por qué." >&2
	exit 1
fi
if [ "${n_ciegos:-0}" -gt 0 ]; then
	echo "check-holds: PARCIAL — ${n_vet} veto(s), ${n_merges} merge(s) en el rango; ninguno los pisa" >&2
	echo "             POR MENSAJE, y de ${n_ciegos} comprobación(es) de CONTENCIÓN no he podido" >&2
	echo "             responder (arriba). «No he podido mirar la mitad» no es «la mitad está limpia»." >&2
	exit 2
fi
# ⛔ «ninguno los pisa» VA ENTERO EN UNA LÍNEA: la batería hace `grep` de esa cadena y partirla
# rompió dos casos con el rc CORRECTO. El texto de un gate es un contrato con quien lo comprueba.
echo "check-holds: OK — ${n_vet} veto(s) vigente(s), ${n_merges} merge(s) en el rango, ninguno los pisa"
echo "             (ni por mensaje ni por contención de su punta)."
exit 0
