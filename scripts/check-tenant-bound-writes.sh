#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-tenant-bound-writes.sh — en un fichero de cliente que YA ata el inquilino, una escritura
# que NO lo ata es un superviviente, y sin trinquete la cifra solo puede empeorar en silencio.
#
# QUE DEFECTO CIERRA, y la ventana es estrecha a proposito. La peticion normal ya queda acotada:
# el cliente FIJA el inquilino al entrar (web/src/lib/api/client.ts). Lo que queda abierto es la
# MUTACION PAUSADA — offline, reintento, red caida — que se reanuda DESPUES de un cambio de
# inquilino y sale con el nuevo. `#1613` lo demuestra con un testigo ejecutable en
# web/src/features/knowledge/scans-view.test.tsx: offline, clic, cambio de inquilino, online, y
# exige que la mutacion salga con el inquilino que la INICIO.
#
# POR QUE ESTE ALCANCE Y NO «TODAS LAS ESCRITURAS». Medido el 2026-08-23 sobre la punta de #1613:
# el arbol tiene ~363 llamadas de escritura en produccion y solo ~30 atadas. Un trinquete sobre
# esas 363 naceria con ~330 exenciones, y una lista de trescientas excepciones no es un trinquete:
# es una lista de la compra que nadie relee. El alcance que SI funciona es «donde el trabajo ya se
# hizo»: NUEVE ficheros importan el mecanismo y dentro de ellos hay 165 escrituras. Ahi el defecto
# no es teorico -- `finops/api.ts` ata `ingestOutcome` y deja `createBudget`, `updateBudget` y
# `deleteBudget` sin atar, tres lineas mas abajo, porque la poblacion del arreglo salio de un censo
# de CLAVES DE CACHE y no de la clase del defecto.
#
# Y CRECE SOLO, que es la propiedad que lo hace barato: el dia que alguien ate la primera escritura
# de otro fichero, ese fichero entra en el alcance y sus hermanas quedan cubiertas por la misma
# regla. El alcance lo amplia el TRABAJO, no una lista que alguien tiene que acordarse de tocar.
#
# LA REGLA OPERATIVA, UNICA PARA TODA LA CONSOLA (adjudicada por PLAN, 2026-08-23T10:54Z):
#
#     NINGUNA REANUDACION ATERRIZA EN UN INQUILINO DISTINTO DEL QUE LA PIDIO.
#
# ⚠ Y se cumple por DOS caminos distintos, que no hay que unificar -- unificarlos seria una
# regresion, y estuvo a punto de adjudicarse:
#
#   · si el inquilino VIAJA CON LA PETICION (`TenantRequestOptions`), la mutacion pausada lo lleva
#     consigo y REANUDA: aterriza donde se pidio, sin molestar al operador.
#   · si la reanudacion es una LLAMADA NUEVA que solo conserva `vars` -- el caso del gancho
#     privilegiado, `web/src/lib/hooks/use-privileged-mutation.ts:160-165` -- el inquilino de origen
#     NO viaja, asi que la unica salida correcta es RECHAZAR Y AVISAR (`:236-247`).
#
# No son dos politicas para el mismo hecho: son dos CAPACIDADES. Un componente que rehusa no esta
# eligiendo rehusar; no puede hacer otra cosa. Quien lea solo una de las dos mitades concluira que
# la consola se contradice -- lo concluyo este carril, se publico, y hubo que retractarlo.
#
# LA REGLA DE ESTE GATE, que es como se mide la de arriba en el primer camino: en un fichero que
# importa `TenantRequestOptions`, toda escritura lo lleva -- o esta en EXENTAS con su razon escrita.
#
# TRES RESPUESTAS: 0 limpio / 1 hay supervivientes / 2 NO HE PODIDO MIRAR.
set -uo pipefail
export LC_ALL=C

no_puedo() { printf 'check-tenant-bound-writes: 2 NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)" || no_puedo "no resuelvo la raiz"

MECANISMO='TenantRequestOptions'
ESCRITURA='http\.(post|put|patch|delete|putRaw|postWithMeta|putWithMeta|patchWithMeta|deleteWithMeta)'

# Escrituras EXENTAS, con su razon. La forma es `fichero:nombre`.
# Una exencion sin razon es un agujero con nombre bonito: el gate la rechaza (ver el selftest).
exenta() {
	case "$1" in
	# (vacio hoy a proposito: el primer barrido no exime nada. Cada exencion que se anada
	#  aqui lleva su razon en el `case`, y el selftest comprueba que la lista no admite entradas mudas.)
	*) return 1 ;;
	esac
}

# Recorre un directorio de fuentes y lista `fichero:linea:nombre` de cada escritura sin atar
# dentro de un fichero que YA usa el mecanismo.
censar() {
	local raiz="$1"
	[ -d "$raiz" ] || { printf 'no existe %s\n' "$raiz" >&2; return 2; }
	local f rel n linea nombre hallazgos=0 vistos=0 ficheros_con_mecanismo=0
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		case "$f" in *.test.ts|*.test.tsx|*/client.ts) continue ;; esac
		# solo ficheros que YA atan
		grep -q "$MECANISMO" "$f" 2>/dev/null || continue
		ficheros_con_mecanismo=$((ficheros_con_mecanismo + 1))
		rel="${f#"$raiz"/}"
		# cada linea de escritura, con el nombre de la propiedad que la contiene mas arriba
		while IFS= read -r linea; do
			[ -n "$linea" ] || continue
			n="${linea%%:*}"
			vistos=$((vistos + 1))
			# la firma de la funcion: desde su declaracion hasta la linea de la escritura.
			# Si en ese tramo aparece el mecanismo, esta atada.
			# ⛔ DOS ESPACIOS LITERALES, NO `{2}`. El awk de esta caja es mawk y NO soporta
			# expresiones de intervalo: `/^[[:space:]]{2}…/` no casa NADA, `nom` se queda vacio,
			# el `[ -n "$nombre" ] || continue` de abajo se traga TODAS las escrituras y el gate
			# imprime CLEAN sobre un arbol lleno de supervivientes. Lo hizo: la primera version
			# dijo «CLEAN» sobre la punta de #1613, donde `createBudget` esta sin atar y verificado.
			nombre="$(awk -v fin="$n" '
				NR<=fin && /^  [a-zA-Z_][a-zA-Z0-9_]*:/ { linea=NR; nom=$0 }
				END { if (nom!="") { sub(/^[[:space:]]*/,"",nom); sub(/:.*/,"",nom); print linea ":" nom } }
			' "$f")"
			[ -n "$nombre" ] || continue
			local ini="${nombre%%:*}" prop="${nombre##*:}" firma
			# ⛔ SIN TUBERIA hacia `grep -q`: bajo pipefail devuelve 141 EN EXITO cuando el
			# productor sigue escribiendo. Lo caza `lint:sigpipe-booleans`, y me lo cazo A MI
			# en este mismo fichero: era la unica que quedaba, y la escribi despues de haber
			# arreglado la misma clase dos veces hoy en el guion hermano.
			firma="$(sed -n "${ini},${n}p" "$f" 2>/dev/null)"
			case "$firma" in
			*"$MECANISMO"*) continue ;;
			esac
			exenta "${rel}:${prop}" && continue
			printf '%s:%s:%s\n' "$rel" "$n" "$prop"
			hallazgos=$((hallazgos + 1))
		done < <(grep -nE "$ESCRITURA" "$f" 2>/dev/null)
	done < <(find "$raiz" -type f \( -name '*.ts' -o -name '*.tsx' \) 2>/dev/null | LC_ALL=C sort)
	# ⛔ TRES ESTADOS, NO DOS, y la diferencia es la razon de ser de este bloque.
	# `TenantRequestOptions` NACE en #1613: en `main` HOY no existe en ningun fichero. Un gate que
	# no encuentra el mecanismo y contesta «limpio» es la clase «un gate ciego no falla: CERTIFICA»
	# — se leeria como «toda escritura ata» justo en el arbol donde ninguna lo hace. Asi que:
	#   · el mecanismo no esta en el arbol  -> 3, NO APLICA TODAVIA (el llamante lo traduce a 0
	#     con su razon impresa; se arma solo el dia que #1613 aterrice)
	#   · el mecanismo esta pero no vi escrituras -> 2, NO HE PODIDO MIRAR
	if [ "$ficheros_con_mecanismo" -eq 0 ]; then
		return 3
	fi
	if [ "$vistos" -eq 0 ]; then
		printf 'no vi ni una escritura en %s\n' "$raiz" >&2
		return 2
	fi
	[ "$hallazgos" -eq 0 ] && return 0 || return 1
}

# ─────────────────────────────────────────────────────────────────────────────────────────────
# SEGUNDA PREGUNTA: LAS IRREVERSIBLES, y se hace FUERA del alcance de adopcion a proposito.
#
# ⛔ EL ALCANCE DE ARRIBA TIENE UN PUNTO CIEGO Y ES EL PEOR POSIBLE. `censar` solo mira ficheros
# que YA importan el mecanismo (linea del `grep -q "$MECANISMO"`), y eso es correcto para el
# trinquete -- crece con el trabajo y no nace con 330 exenciones. Pero significa que un fichero
# que NUNCA lo adopte es invisible PARA SIEMPRE, y medido el 2026-08-24 ahi es justo donde viven
# las escrituras que no se pueden deshacer:
#
#   web/src/features/compliance/api.ts   ->  CERO menciones de TenantRequestOptions
#     releaseHold        POST .../holds/{id}/release      libera una retencion legal
#     executeErasure     POST .../erasure/{id}/execute    ejecuta un borrado
#     eraseDataSubject   POST .../data-subjects/{id}/erase borra a una persona
#
# Las tres usan `postWithMeta` -- son escrituras de doble control, con el ESTADO como contrato --
# y ninguna ata el inquilino. Una reanudacion pausada de cualquiera de las tres aterrizando en
# otro inquilino no es una lista incompleta: es una retencion legal levantada donde no tocaba, o
# un borrado ejecutado sobre el sujeto equivocado. No hay pantalla que lo deshaga.
#
# Por eso esta seccion NO filtra por adopcion: filtra por CONSECUENCIA. Es una lista corta y
# elegida, no un barrido -- exactamente lo contrario de las 330 exenciones que el alcance de
# arriba evita.
IRREVERSIBLE='/release|/execute|/erase'

irreversibles() {
	local raiz="$1"
	[ -d "$raiz/features" ] || { printf 'no existe %s/features\n' "$raiz" >&2; return 2; }
	local f rel n linea nombre
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		case "$f" in *.test.ts|*.test.tsx|*/client.ts) continue ;; esac
		while IFS= read -r linea; do
			[ -n "$linea" ] || continue
			n="${linea%%:*}"
			rel="${f#"$raiz"/}"
			nombre="$(awk -v fin="$n" '
				NR<=fin && /^  [a-zA-Z_][a-zA-Z0-9_]*:/ { linea=NR; nom=$0 }
				END { if (nom!="") { sub(/^[[:space:]]*/,"",nom); sub(/:.*/,"",nom); print linea ":" nom } }
			' "$f")"
			[ -n "$nombre" ] || continue
			local ini="${nombre%%:*}" prop="${nombre##*:}" firma
			# ⛔ EL CUERPO, NO LA LINEA. La escritura y su RUTA viven en lineas distintas cuando
			# la llamada es multilinea, que es como estan escritas justo las tres que importan:
			#   releaseHold: (id, body) =>
			#     http.postWithMeta<...>(
			#       `${BASE}/holds/${...}/release`,      <- la ruta, DOS lineas mas abajo
			# Mi primera version grepeaba la MISMA linea buscando la escritura Y la ruta, no casaba
			# ninguna, y el gate salio `rc=0` sin nombrar nada. Un gate ciego no falla: certifica.
			firma="$(sed -n "${ini},$((n+8))p" "$f" 2>/dev/null)"
			case "$firma" in *"$MECANISMO"*) continue ;; esac
			case "$firma" in */release*|*/execute*|*/erase*) ;; *) continue ;; esac
			printf '%s:%s:%s\n' "$rel" "$n" "$prop"
		done < <(grep -nE "$ESCRITURA" "$f" 2>/dev/null || true)
	done < <(
		{
			find "$raiz/features" -type f -name 'api.ts' 2>/dev/null
			# ⛔ Y LA CAPA COMPARTIDA, que es el punto ciego que este guion tenia con
			#    `check-list-truncation-witness.sh`: los DOS recorrian `features/` y ninguno
			#    miraba `lib/api/`. Un punto ciego compartido por las dos unicas sondas no es el
			#    mismo defecto dos veces: es uno que NINGUNA puede cazar, por construccion.
			#
			# ⛔ HOY AQUI NO HAY NADA QUE CAZAR, Y LO DIGO EN VEZ DE APARENTARLO. Medido el
			#    2026-08-24: `lib/api/` tiene TRECE escrituras, CERO usan `*WithMeta` (el
			#    mecanismo de doble control que se puede reanudar) y CERO encajan en el patron de
			#    consecuencia. Asi que esta rama devuelve cero HOY. **Un gate que solo ha
			#    devuelto cero no ha demostrado que sepa devolver uno**, y por eso la celda
			#    `CAPA COMPARTIDA` del selftest planta una escritura irreversible ahi y exige que
			#    salga: sin ella, esto seria un ciego certificando.
			#
			#    Va como TRIPWIRE, no como censo: el dia que alguien monte un `/release` en la
			#    capa compartida —que es justo donde mas se hereda sin decidir— sale nombrado.
			find "$raiz/lib/api" -maxdepth 1 -type f -name '*.ts' 2>/dev/null
		} | LC_ALL=C sort
	)
}

if [ "${1:-}" = "--selftest" ]; then
	fail=0
	corridas=0
	saltadas=0
	T="$(mktemp -d "${TMPDIR:-/tmp}/tbw.XXXXXX")" || no_puedo "mktemp fallo"
	trap 'rm -rf "$T"' EXIT
	mkdir -p "$T/src/f"

	# Un fichero que ATA una y deja otra: la que falta tiene que salir, la atada no.
	cat > "$T/src/f/api.ts" <<'TS'
import { http, type TenantRequestOptions } from '@/lib/api/client'
export const api = {
  atada: (body: X, request: TenantRequestOptions) =>
    http.post<Y>(`${BASE}/atada`, body, request),
  suelta: (body: X) =>
    http.post<Y>(`${BASE}/suelta`, body),
}
TS
	salida="$(censar "$T/src" || true)"
	case $'\n'"$salida"$'\n' in
	*:suelta$'\n'*) corridas=$((corridas+1)); echo "  ok    CONTROL NEGATIVO: la escritura sin atar se caza" ;;
	*) corridas=$((corridas+1)); echo "  FAIL  CONTROL NEGATIVO: no ve la escritura sin atar — es ciego"; fail=1 ;;
	esac
	case $'\n'"$salida"$'\n' in
	*:atada$'\n'*) corridas=$((corridas+1)); echo "  FAIL  CONTROL POSITIVO: caza una escritura que SI esta atada"; fail=1 ;;
	*) corridas=$((corridas+1)); echo "  ok    CONTROL POSITIVO: la escritura atada no es hallazgo" ;;
	esac

	# Un fichero que NO usa el mecanismo queda FUERA del alcance, tenga escrituras sueltas o no.
	mkdir -p "$T/src/g"
	cat > "$T/src/g/api.ts" <<'TS'
import { http } from '@/lib/api/client'
export const api = { suelta: (b: X) => http.post<Y>(`${BASE}/x`, b) }
TS
	salida="$(censar "$T/src" || true)"
	case $'\n'"$salida"$'\n' in
	*g/api.ts*) corridas=$((corridas+1)); echo "  FAIL  un fichero que aun no ata nada NO deberia entrar en el alcance"; fail=1 ;;
	*) corridas=$((corridas+1)); echo "  ok    un fichero que aun no ata nada queda fuera del alcance" ;;
	esac

	# Un arbol que SI usa el mecanismo pero donde no se ve ni una escritura es 2, no 0:
	# si no vio nada, no puede decir «limpio».
	# ── LAS IRREVERSIBLES: se ven aunque su fichero NO adopte el mecanismo ──────────────
	# Sin estas dos casillas, la seccion nueva podria estar ciega y nadie lo notaria: mi primera
	# version grepeaba la escritura y la ruta en la MISMA linea, no casaba ninguna, y el gate
	# salia rc=0 «sin hallazgos». Un gate ciego no falla: certifica.
	mkdir -p "$T/irr/features/x"
	printf 'export const xApi = {\n  releaseHold: (id: string, body: unknown) =>\n    http.postWithMeta<unknown>(\n      `${BASE}/holds/${id}/release`,\n      body,\n    ),\n}\n' > "$T/irr/features/x/api.ts"
	salida="$(irreversibles "$T/irr" || true)"
	case $'\n'"$salida"$'\n' in
	*releaseHold*) corridas=$((corridas+1)); echo "  ok    IRREVERSIBLE: una release sin atar se caza aunque el fichero no adopte" ;;
	*) corridas=$((corridas+1)); echo "  FAIL  IRREVERSIBLE: no ve una release sin atar — la seccion es ciega"; fail=1 ;;
	esac

	printf 'export const xApi = {\n  releaseHold: (id: string, body: unknown, opts?: TenantRequestOptions) =>\n    http.postWithMeta<unknown>(\n      `${BASE}/holds/${id}/release`,\n      body,\n      opts,\n    ),\n}\n' > "$T/irr/features/x/api.ts"
	salida="$(irreversibles "$T/irr" || true)"
	case $'\n'"$salida"$'\n' in
	*releaseHold*) corridas=$((corridas+1)); echo "  FAIL  IRREVERSIBLE: caza una release que SI ata el inquilino"; fail=1 ;;
	*) corridas=$((corridas+1)); echo "  ok    IRREVERSIBLE: una release atada no es hallazgo" ;;
	esac

	# ── LA CAPA COMPARTIDA: hoy devuelve CERO, y esta celda prueba que no es por ciega ──
	# `lib/api/` no tiene HOY ninguna escritura irreversible (medido: 13 escrituras, 0 con
	# `*WithMeta`, 0 en el patron de consecuencia). Un gate que solo ha devuelto cero no ha
	# demostrado que sepa devolver uno, asi que aqui se planta una y se exige que salga.
	mkdir -p "$T/irr/lib/api"
	printf 'export const sharedApi = {\n  executeErasure: (id: string, body: unknown) =>\n    http.postWithMeta<unknown>(\n      `${BASE}/erasure/${id}/execute`,\n      body,\n    ),\n}\n' > "$T/irr/lib/api/endpoints.ts"
	salida="$(irreversibles "$T/irr" || true)"
	case $'\n'"$salida"$'\n' in
	*executeErasure*) corridas=$((corridas+1)); echo "  ok    CAPA COMPARTIDA: una irreversible en lib/api se caza (el punto ciego de las DOS sondas)" ;;
	*) corridas=$((corridas+1)); echo "  FAIL  CAPA COMPARTIDA: no ve una irreversible en lib/api — sigue ciega ahi"; fail=1 ;;
	esac

	printf 'export const sharedApi = {\n  executeErasure: (id: string, body: unknown, opts?: TenantRequestOptions) =>\n    http.postWithMeta<unknown>(\n      `${BASE}/erasure/${id}/execute`,\n      body,\n      opts,\n    ),\n}\n' > "$T/irr/lib/api/endpoints.ts"
	salida="$(irreversibles "$T/irr" || true)"
	case $'\n'"$salida"$'\n' in
	*executeErasure*) corridas=$((corridas+1)); echo "  FAIL  CAPA COMPARTIDA: caza una irreversible que SI ata el inquilino"; fail=1 ;;
	*) corridas=$((corridas+1)); echo "  ok    CAPA COMPARTIDA: atada, no es hallazgo (discrimina, no lista todo)" ;;
	esac
	rm -f "$T/irr/lib/api/endpoints.ts"

	mkdir -p "$T/mudo"
	printf 'import { type TenantRequestOptions } from "@/lib/api/client"\nexport type Z = TenantRequestOptions\n' > "$T/mudo/api.ts"
	censar "$T/mudo" >/dev/null 2>&1; rc=$?
	if [ "$rc" = "2" ]; then corridas=$((corridas+1)); echo "  ok    con el mecanismo presente y cero escrituras: 2 (NO HE PODIDO MIRAR), no 0"
	else corridas=$((corridas+1)); echo "  FAIL  con el mecanismo y sin escrituras devolvio $rc, esperaba 2"; fail=1; fi

	# Y un arbol donde el mecanismo NO existe es 3 (NO APLICA TODAVIA), que tampoco es 0 a secas.
	mkdir -p "$T/sinmecanismo"
	printf 'export const a = { x: (b) => http.post("/v1/x", b) }\n' > "$T/sinmecanismo/api.ts"
	censar "$T/sinmecanismo" >/dev/null 2>&1; rc=$?
	if [ "$rc" = "3" ]; then corridas=$((corridas+1)); echo "  ok    sin el mecanismo en el arbol: 3 (NO APLICA TODAVIA), distinguible de limpio"
	else corridas=$((corridas+1)); echo "  FAIL  sin el mecanismo devolvio $rc, esperaba 3"; fail=1; fi

	# CALIBRACION CONTRA UN ARBOL REAL QUE TENGA EL MECANISMO.
	# ⛔ Y SI NO SE PUEDE, SE DICE A GRITOS. `TenantRequestOptions` no existe en `main` (nace en
	# #1613), asi que en `main` estas dos casillas NO PUEDEN correr. Un `skip` mudo aqui seria un
	# pase silencioso: la bateria diria «todo verde» sin haber calibrado nunca contra codigo real,
	# que es justo como esta version llego a imprimir CLEAN sobre un arbol lleno de supervivientes.
	REALS="${OLIVARES_SELFTEST_SRC:-$ROOT/web/src}"
	if [ -d "$REALS" ] && grep -rqs "$MECANISMO" "$REALS" --include='*.ts' 2>/dev/null; then
		salida="$(censar "$REALS" || true)"
		case "$salida" in
		*createBudget*) corridas=$((corridas+1)); echo "  ok    arbol real: finops createBudget sale como superviviente" ;;
		*) corridas=$((corridas+1)); echo "  FAIL  arbol real: finops createBudget deberia salir y no sale"; fail=1 ;;
		esac
		case "$salida" in
		*ingestOutcome*) corridas=$((corridas+1)); echo "  FAIL  arbol real: ingestOutcome esta ATADA y no deberia salir"; fail=1 ;;
		*) corridas=$((corridas+1)); echo "  ok    arbol real: ingestOutcome (atada) no sale" ;;
		esac
	else
		echo "  ⚠ SIN CALIBRAR contra arbol real: '$REALS' no contiene '$MECANISMO'."
		echo "    En \`main\` esto es NORMAL — el mecanismo nace en #1613 — pero significa que estas"
		echo "    DOS casillas no se han comprobado. Para calibrarlas, apunta a un arbol que lo tenga:"
		echo "      OLIVARES_SELFTEST_SRC=<worktree-de-1613>/web/src bash \$0 --selftest"
		echo "    (No cuenta como fallo: cuenta como NO MEDIDO, y por eso se dice.)"
		saltadas=$((saltadas + 2))
	fi

	# ⛔ El resumen dice cuantas CORRIERON, no cuantas hay escritas. Un «7 passed» con dos
	# casillas saltadas es la misma mentira que un gate ciego que dice CLEAN.
	#
	# ⛔ Y EL TOTAL SE DERIVA, que es la correccion del 2026-08-24. Aqui decia `/7` LITERAL, y
	#    bastaron las dos casillas nuevas de la CAPA COMPARTIDA para que envejeciera: con nueve
	#    corridas, `$((7 - corridas))` habria impreso «-2 SIN CORRER». Es exactamente el defecto
	#    que `check-list-truncation-witness.sh` ya documenta en su propio resumen — el mismo error,
	#    en la casa de al lado, y por eso se arregla igual: contando las saltadas DONDE se saltan.
	if [ "$fail" = "0" ]; then
		if [ "$saltadas" -gt 0 ]; then
			echo "check-tenant-bound-writes selftest: ${corridas}/$((corridas + saltadas)) passed, 0 failed — ${saltadas} SIN CORRER (ver el aviso de calibracion)"
		else
			echo "check-tenant-bound-writes selftest: ${corridas} passed, 0 failed"
		fi
		exit 0
	fi
	echo "check-tenant-bound-writes selftest: FAILED (${corridas} corridas)"; exit 1
fi


FUENTES="${OLIVARES_WEB_SRC:-$ROOT/web/src}"
irrev="$(irreversibles "$FUENTES" || true)"
n_irrev="$(printf '%s\n' "$irrev" | grep -c . || true)"
if [ "${n_irrev:-0}" -gt 0 ]; then
	echo "check-tenant-bound-writes: ⛔ ${n_irrev} escritura(s) IRREVERSIBLE(S) sin atar el inquilino:" >&2
	printf '%s\n' "$irrev" | sed 's/^/    /' >&2
	echo "  Estas se miran FUERA del alcance de adopcion, por consecuencia: una reanudacion en otro" >&2
	echo "  inquilino levanta una retencion legal o ejecuta un borrado donde no tocaba." >&2
	echo >&2
fi

salida="$(censar "$FUENTES")"; rc=$?
[ "$rc" = "2" ] && no_puedo "el censo no pudo recorrer $FUENTES"
if [ "$rc" = "3" ]; then
	echo "check-tenant-bound-writes: NO APLICA TODAVIA — ningun fichero de $FUENTES usa"
	echo "  \`$MECANISMO\`. El mecanismo nace en #1613; hasta que aterrice no hay nada que gatear."
	echo "  NO es CLEAN: es que la clase que este gate protege aun no existe en este arbol."
	[ "${n_irrev:-0}" -gt 0 ] && exit 1
	exit 0
fi
n="$(printf '%s\n' "$salida" | grep -c . || true)"
if [ "${n:-0}" -eq 0 ]; then
	echo "check-tenant-bound-writes: CLEAN — en los ficheros que ya atan, toda escritura ata."
	[ "${n_irrev:-0}" -gt 0 ] && exit 1
	exit 0
fi
echo "check-tenant-bound-writes: ${n} escritura(s) SIN atar en ficheros que YA atan el inquilino:" >&2
printf '%s\n' "$salida" | sed 's/^/    /' >&2
echo >&2
echo "  Cada una: o acepta TenantRequestOptions, o entra en exenta() con su razon escrita." >&2
exit 1
