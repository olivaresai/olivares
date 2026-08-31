#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-list-truncation-witness.sh — una feature de consola que consulta una LISTA VIVA y no
# publica en ninguna vista si esa lista viene RECORTADA.
#
# POR QUE EXISTE, y no es estilo: el motor pagina por defecto a 100
# (core/internal/store/sqlstore/generic.go), publica `has_more` en el envoltorio, y una vista que
# lee `items` y nada mas ENSENA UNA LISTA COMPLETA QUE NO LO ES. No falla, no avisa, y quien la
# usa para dar un estate por revisado se lleva las primeras 100 filas creyendo que son todas.
#
# ⛔ Y POR QUE ES UN GUION Y NO UNA CIFRA EN UN BUZON. El 2026-08-23 el censo de esta clase se
# publico TRES veces con tres numeros distintos —«11 features / 30 rutas», luego «7 / 26», luego
# «19»— y las tres eran prosa tecleada a mano. La particion de la segunda no sumaba (11 menos 2
# da 9, no 7; 30 menos 6 menos 1 da 23, no 26), y NADIE lo vio porque una cifra en prosa no tiene
# quien la recalcule. Ademas una de las exclusiones —`rate-limits`, retirada con la razon «su
# unica ruta de lista ya pedia techo»— la REFUTA su propio fichero: `web/src/features/rate-limits/`
# no contiene un solo `limit`. Un censo que no se puede volver a correr envejece en silencio y se
# hereda con su error puesto.
#
# EL DISCRIMINANTE, y es deliberadamente GROSERO para que se pueda comprobar a mano:
#
#   tiene lista viva  := algun .ts de PRODUCCION de la feature declara `ListResponse<`
#   declara el recorte := algun .tsx de PRODUCCION de la MISMA feature menciona has_more/hasMore
#
# Una feature en la tercera fila —tiene lista viva y NINGUNA vista la declara— es un hallazgo.
#
# ⚠ LO QUE ESTE GUION **NO** PRUEBA, dicho aqui para que nadie lo cite de mas:
#
#   · Es un censo POR FEATURE, no por llamada. Una feature cuenta como «declara» aunque solo UNA
#     de sus quince vistas lo haga, asi que las cubiertas esconden vistas sin cubrir. ES UN SUELO.
#   · Es un censo de FUENTE. No prueba ALCANZABILIDAD: `{false && <aviso/>}` lo satisface entero.
#     El testigo de vista montada es otra cosa y hace falta igual.
#   · NO mide el motor. Un handler que drena con `listAll` no tiene recorte que declarar, asi que
#     parte de los hallazgos pueden caerse al medir el handler. Eso se mide ruta a ruta y no se
#     estima: la lista que imprime este guion es el PUNTO DE PARTIDA de esa medida, no su resultado.
#
# TRES RESPUESTAS: 0 limpio / 1 hay features sin testigo / 2 NO HE PODIDO MIRAR.
# El 2 no es el 0: «no he podido mirar» jamas se reporta como limpio.
set -uo pipefail
export LC_ALL=C

no_puedo() { printf 'check-list-truncation-witness: 2 NO HE PODIDO MIRAR — %s\n' "$*" >&2; exit 2; }

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)" || no_puedo "no resuelvo la raiz"

# ── enmascarar comentarios, CONSERVANDO EL NUMERO DE LINEAS ───────────────────────────────────
# Vacia el contenido de los comentarios y deja la linea en su sitio, para que un `sed -n "${n}p"`
# posterior siga apuntando a lo mismo. Devuelve el fichero entero por stdout.
#
# ⛔ POR QUE HACE FALTA, y son DOS defectos opuestos medidos en (2026-08-27) por el propio
#    carril que escribia encima de este guion:
#
#    · `censar` descontaba SOLO los bloques `/* … */` que caben en UNA linea, mas la cola tras
#      `//`, mas las lineas que ya empiezan por `*`, `//` o `/*`. Un comentario JSX MULTILINEA
#      cuyas lineas de continuacion empiezan por texto —lo normal al explicar algo— colaba
#      `has_more` EN PROSA y ABSOLVIA a la feature aunque nadie pintara el aviso. Falso NEGATIVO,
#      el que no se nota. La cabecera de este fichero ya predecia la version de una linea y decia
#      que ninguna feature dependia todavia de esa diferencia: la primera que lo hizo fue la que
#      vino a arreglarlo.
#    · `capa_compartida` no descontaba NADA. Una frase que citara el patron que busca se contaba
#      como una llamada: «(sin nombre) endpoints.ts:199», sin techo, gate ROJO. **Documentar el
#      punto ciego lo disparaba** — y un control que castiga a quien documenta su limite es un
#      control que se queda sin documentar.
#
# ⛔ Y SE SALTAN LAS CADENAS, que es lo que separa este enmascarador de un `sed` ingenuo: un `//`
#    dentro de una URL (`'https://…'`) se llevaria por delante el resto de la linea. En `censar`
#    eso solo perderia un testigo (hallazgo de mas, ruidoso); en `capa_compartida` ESCONDERIA una
#    llamada, que es un hallazgo de MENOS. Se reconocen comillas simples, dobles y backticks.
enmascarar_comentarios() {
	LC_ALL=C awk '
	BEGIN { bloque = 0 }
	{
		linea = $0; salida = ""; i = 1; n = length(linea); cita = ""
		while (i <= n) {
			c = substr(linea, i, 1); c2 = substr(linea, i, 2)
			if (bloque) {                      # dentro de /* … */
				if (c2 == "*/") { bloque = 0; i += 2 } else { i += 1 }
				continue
			}
			if (cita != "") {                  # dentro de una cadena: se copia tal cual
				salida = salida c
				if (c == "\\") { salida = salida substr(linea, i + 1, 1); i += 2; continue }
				if (c == cita) { cita = "" }
				i += 1
				continue
			}
			if (c == "\"" || c == "'"'"'" || c == "`") { cita = c; salida = salida c; i += 1; continue }
			if (c2 == "/*") { bloque = 1; i += 2; continue }
			if (c2 == "//") { break }          # cola de linea
			salida = salida c; i += 1
		}
		print salida
	}' "$1" 2>/dev/null
}

# ── los tipos que SON un envoltorio de lista ───────────────────────────────────────────────────
# Devuelve una alternancia ERE: `ListResponse` y todo alias o subtipo suyo declarado en las raices
# que se le pasen.
#
# ⛔ POR QUE, y es el punto ciego que MAS cambia el resultado de los tres. Casar el literal
#    `ListResponse<` deja fuera a quien escribe el ALIAS en la llamada. Medido el 2026-08-27:
#    `web/src/lib/api/endpoints.ts` declara `AuditListResponse extends ListResponse<AuditEventDTO>`
#    y llama `http.get<AuditListResponse>(…)` DOS veces —`/v1/audit` y `/v1/audit/system`—, cuyas
#    respuestas el contrato declara truncables (`listAuditEvents`, `listSystemAuditEvents`). Este
#    guion decia que la capa compartida tenia TRES listas. Tenia CINCO. Las otras dos no estaban
#    exentas: **no se miraban**.
#
#    Y eso no hace ruido ni absuelve a una feature: hace que el sujeto NO EXISTA para el control.
#    Es el unico de los tres puntos ciegos que no se nota nunca, porque un hallazgo que no se
#    imprime se lee igual que un hallazgo que no hay.
#
# Se reconocen las dos formas que el arbol usa: `interface X extends ListResponse<…>` y
# `type X = ListResponse<…>`. Un alias DE UN ALIAS no se persigue en cadena a proposito: hoy no
# existe ninguno (comprobado) y una transitividad sin sujeto es codigo que nadie prueba.
alias_de_lista() {
	local nombres
	nombres="$(grep -rhoE '(interface|type)[[:space:]]+[A-Z][A-Za-z0-9_]*[[:space:]]*(extends[[:space:]]+ListResponse|=[[:space:]]*ListResponse)' \
		"$@" --include='*.ts' --include='*.tsx' 2>/dev/null \
		| sed -E 's/^(interface|type)[[:space:]]+([A-Z][A-Za-z0-9_]*).*/\2/' \
		| LC_ALL=C sort -u || true)"
	local salida="ListResponse" x
	while IFS= read -r x; do
		[ -n "$x" ] || continue
		[ "$x" = "ListResponse" ] && continue
		salida="${salida}|${x}"
	done <<EOF_A
$nombres
EOF_A
	printf '%s\n' "$salida"
}

# ── el censo, sobre un directorio de features cualquiera ──────────────────────────────────────
# Imprime una linea por feature SIN testigo. Devuelve 0 si no hay ninguna, 1 si las hay.
censar() {
	local features="$1"
	[ -d "$features" ] || { printf 'no existe %s\n' "$features" >&2; return 2; }
	# Los envoltorios de lista se derivan DEL ARBOL QUE SE RECORRE, no de una lista fija ni de
	# una ruta absoluta: atarlo a `web/src/lib/api` haria que este censo —y su selftest— dependa
	# del arbol real, y entonces las celdas sinteticas dejarian de ser sinteticas.
	# ⚠ La cota, dicha: un alias declarado FUERA de `$features` no se ve desde aqui. Hoy el unico
	#   del arbol (`AuditListResponse`) vive en `lib/api`, que es justo donde `capa_compartida`
	#   si lo deriva. Una feature que alcanzara ese alias por el api de otra es el «camino
	#   cruzado», y eso lo mide `registry.truncation-notice.test.ts`, no este censo.
	local ALIAS_LISTA
	ALIAS_LISTA="$(alias_de_lista "$features")"
	local d f api tsx tf limpio hallazgos=0
	while IFS= read -r d; do
		[ -n "$d" ] || continue
		f="$(basename "$d")"
		# lista viva: algun .ts de produccion declara ListResponse<  Y ADEMAS la feature LLAMA.
		#
		# ⛔ DECLARAR EL TIPO NO ES CONSULTAR. `executive` declaraba `ListResponse<T>` en su
		# `derive.ts` —como parametro de tipo para datos que le pasan OTRAS features— y en su
		# `fixtures.ts`, y no hace NI UNA llamada de red propia (`http.get`/`http.post`: cero).
		# Sin esta segunda condicion salia como hueco, y no lo es: no tiene lista que recortar
		# porque no consulta ninguna. Lo encontre midiendo el motor, no leyendo el censo.
		api="$(grep -rlE "\b(${ALIAS_LISTA})\b" "$d" --include='*.ts' 2>/dev/null | grep -cv '\.test\.' || true)"
		[ "${api:-0}" -gt 0 ] || continue
		llamadas=0
		while IFS= read -r cf; do
			[ -n "$cf" ] || continue
			case "$cf" in *.test.ts|*.test.tsx|*fixtures.ts) continue ;; esac
			grep -q 'http\.' "$cf" 2>/dev/null && llamadas=$((llamadas + 1))
		done < <(find "$d" -type f \( -name '*.ts' -o -name '*.tsx' \) 2>/dev/null)
		[ "$llamadas" -gt 0 ] || continue
		# declara el recorte: algun .tsx de PRODUCCION menciona has_more/hasMore EN CODIGO.
		# ⛔ LAS LINEAS DE COMENTARIO NO CUENTAN, y no es purismo: `health/incidents.tsx:256` es un
		# COMENTARIO que cita `hasMore:true` justo encima del codigo que si lo usa. Una sonda que no
		# descuenta comentarios da por CUBIERTA a cualquier feature que solo MENCIONE el recorte en
		# prosa: un falso negativo, que es el que no se nota. Hoy ninguna feature del arbol depende de
		# esa diferencia (comprobado); la primera que lo haga saldria mal clasificada y en silencio.
		#
		# ⛔ Y SE RECORRE FICHERO A FICHERO A PROPOSITO. `grep -rh` suprime el nombre, asi que un
		# `grep -v '\.test\.'` detras filtra por CONTENIDO y no por fichero — deja entrar los tests y
		# tira lineas de produccion que mencionen «.test.». Lo hizo aqui, en la primera version.
		tsx=0
		while IFS= read -r tf; do
			[ -n "$tf" ] || continue
			case "$tf" in *.test.tsx) continue ;; esac
			# Se quitan: los bloques /* ... */ que caben en una linea, la cola tras //, y las
			# lineas que ya empiezan por *, // o /*. Un `//` dentro de una cadena (una URL) se lleva
			# por delante el resto de la linea: es aceptable aqui porque solo buscamos una palabra, y
			# el sesgo va del lado seguro — como mucho deja de ver un testigo y la feature sale como
			# hallazgo, que es el error que SI se nota.
			# ⛔ NI UN `grep -q` AL FINAL DE UNA TUBERIA, y aqui costo dos diagnosticos.
			# Bajo `set -o pipefail`, `grep -q` sale al primer casamiento, el `sed` de aguas
			# arriba recibe SIGPIPE (141) y pipefail lo propaga: la comprobacion FALLA JUSTO
			# CUANDO ACIERTA. Y es una CARRERA — si el fichero es pequeno el sed termina antes
			# y no hay SIGPIPE —, asi que el mismo arbol daba 19 features en una corrida y 23 en
			# la siguiente, con `alerting` entrando y saliendo sola. Un gate no determinista es
			# peor que un gate mal: se le cree la vez que acierta.
			# La forma sana es contar sobre una salida ya capturada, sin tuberia que romper.
			# ⛔ ANTES ERA UN `sed` QUE SOLO QUITABA LOS BLOQUES DE UNA LINEA, y ese era el
			# falso NEGATIVO: un comentario multilinea colaba `has_more` en prosa y absolvia a
			# la feature. Ahora enmascara de verdad (ver `enmascarar_comentarios`). Se conserva
			# el filtro de lineas que empiezan por `*` o `/*` porque no estorba.
			limpio="$(enmascarar_comentarios "$tf" | sed 's/^[[:space:]]*//' | grep -vE '^(\*|/\*)')"
			# ⛔ Y CUENTA TAMBIEN EL COMPONENTE COMPARTIDO, o el censo se queda CIEGO justo cuando
			# se hace la convergencia que este mismo guion recomienda (paso 3 de la receta de
			# abajo: «el aviso compartido»). Medido el 2026-08-23 sobre: la feature
			# `automations` quedo cubierta —techo explicito, testigo de transporte y testigo de
			# vista montada, con sus mutantes muertos— y este censo la SEGUIA listando, porque
			# `<ListTruncationBadge/>` no escribe `has_more` en el .tsx: la lectura vive dentro
			# de `_intel/notices.tsx`. Sin esta rama, la cifra NO BAJA segun aterriza el trabajo
			# y cada feature convergida se convierte en un falso positivo permanente.
			# ⛔ Y EL COMPONENTE CUENTA POR SU JSX, NO POR SU NOMBRE. Lo cazo el contraste de Codex
			# (F-02, 2026-08-23) sobre mi propio arreglo de hace unas horas: `limpio` conserva las
			# lineas de `import`, asi que un `view.tsx` con
			#   import { ListTruncationBadge } from '@/features/_intel'
			# y un cuerpo que devuelve `<div />` casaba con el token y RETIRABA la feature del censo
			# sin pintar ningun aviso. Un falso NEGATIVO, que es el que no se nota: el CLEAN llegaba
			# a afirmar que declara el recorte. Mi self-test no podia verlo porque su fixture ponia
			# import Y render juntos; faltaba la celda import-only, que ahora existe.
			sin_import="$(printf '%s\n' "$limpio" | grep -vE '^import[[:space:]]|^[}][[:space:]]*from[[:space:]]')"
			case "$sin_import" in
			*has_more*|*hasMore*|*'<ListTruncationBadge'*) tsx=$((tsx + 1)) ;;
			esac
		done < <(find "$d" -type f -name '*.tsx' 2>/dev/null | LC_ALL=C sort)
		if [ "${tsx:-0}" -eq 0 ]; then
			printf '%s\n' "$f"
			hallazgos=$((hallazgos + 1))
		fi
	done < <(find "$features" -mindepth 1 -maxdepth 1 -type d | LC_ALL=C sort)
	[ "$hallazgos" -eq 0 ] && return 0 || return 1
}


# ── LA CAPA COMPARTIDA: `web/src/lib/api/` ────────────────────────────────────────────────────
# ⛔ POR QUE EXISTE ESTA SEGUNDA PREGUNTA. El censo de arriba es POR FEATURE, asi que recorre
#    `features/*` y NO MIRA `lib/api/`. Y el trinquete de escrituras hace lo mismo
#    (`find "$raiz/features" ... -name api.ts`). Es decir: **las dos sondas de esta casa comparten
#    el mismo punto ciego**, y es justo la capa que usan todas las features. Un falso verde
#    compartido no es dos veces el mismo defecto: es un defecto que NINGUNA de las dos puede
#    cazar, por construccion.
#
# ⛔ Y AQUI LA PREGUNTA ES OTRA, no la misma trasladada. En `lib/api/` no hay vistas, asi que
#    «declara el recorte en algun .tsx» no aplica. Lo que si aplica es el TECHO: una llamada de
#    lista a una ruta que pagina tiene que pedir su limite, o declarar por que no lo necesita.
#
# La exencion lleva RAZON EN LINEA nombrando el handler Go, como en el resto de la casa: una
# exencion sin razon es una lista de perdones, y envejece sin que nadie lo note.
capa_compartida() {
	local dir="$1"
	[ -d "$dir" ] || { printf 'no existe %s\n' "$dir" >&2; return 2; }
	local ALIAS_LISTA
	ALIAS_LISTA="$(alias_de_lista "$dir")"
	local f linea nombre hallazgos=0
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		case "$f" in *.test.ts|*.test.tsx|*openapi.gen.ts) continue ;; esac
		# Se recorre la llamada ENTERA (hasta 3 lineas), porque `http.get<ListResponse<X>>(` y su
		# `{ query: { limit } }` casi nunca caben en una sola.
		# ⛔ SE RECORRE LO ENMASCARADO, NO EL FICHERO. Esta funcion no descontaba NADA, asi
		# que una frase que citara el patron se contaba como una llamada y el gate enrojecia
		# por un comentario. El enmascarador conserva el numero de lineas a proposito: `$n`
		# sigue apuntando a la misma linea del fichero REAL, que es de donde se lee la
		# exencion — porque la exencion VIVE en un comentario y ahi si hay que mirarla.
		local n=0
		while IFS= read -r linea; do
			n=$((n + 1))
			# ⛔ REGEX Y NO `case`, porque el sujeto ya no es un literal: son `ListResponse` y
			# sus alias. `http.get<ListResponse<X>>` y `http.get<AuditListResponse>` son la
			# misma clase de llamada y antes solo se veia la primera.
			# ⛔ Y HERE-STRING, NUNCA `printf | grep -q`: bajo `pipefail` eso sale 141 CUANDO
			# ACIERTA. Es la regla que este mismo fichero predica tres veces mas arriba.
			grep -qE "http\.[a-z]+<[[:space:]]*(${ALIAS_LISTA})[<>]" <<<"$linea" || continue
			nombre="$(sed -n "$((n > 1 ? n - 1 : 1)),${n}p" "$f" | grep -oE '^[[:space:]]*[a-zA-Z][a-zA-Z0-9_]*:' | tail -1 | tr -d ' :')"
			[ -n "$nombre" ] || nombre="(sin nombre) $f:$n"
			# ¿exenta con razon? La razon va en las 6 lineas de encima y tiene que nombrar un .go
			local encima
			encima="$(sed -n "$((n > 6 ? n - 6 : 1)),$((n - 1))p" "$f" 2>/dev/null)"
			case "$encima" in
			*"SIN TECHO A PROPOSITO"*)
				case "$encima" in
				*.go*) continue ;;
				*)
					printf 'EXENCION SIN RAZON: %s (%s:%s) — la exencion debe nombrar el handler .go\n' "$nombre" "$f" "$n" >&2
					return 2
					;;
				esac
				;;
			esac
			# ¿pide techo? `limit` dentro de las 3 lineas de la llamada, y NO como `org_limit`.
			local cuerpo
			# Y el techo, tambien sobre lo enmascarado: un comentario que dijera «limit» en
			# las tres lineas de debajo contaba como techo puesto. Mismo defecto, otra cara.
			cuerpo="$(enmascarar_comentarios "$f" | sed -n "${n},$((n + 3))p" | sed 's/[a-zA-Z_]limit/XX/g')"
			case "$cuerpo" in
			*limit*) ;;
			*) printf '%s (%s:%s)\n' "$nombre" "$f" "$n"; hallazgos=$((hallazgos + 1)) ;;
			esac
		done < <(enmascarar_comentarios "$f")
	done < <(find "$dir" -maxdepth 1 -type f -name '*.ts' 2>/dev/null | LC_ALL=C sort)
	[ "$hallazgos" -eq 0 ] && return 0 || return 1
}

# ── selftest ──────────────────────────────────────────────────────────────────────────────────
if [ "${1:-}" = "--selftest" ]; then
	fail=0
	T="$(mktemp -d "${TMPDIR:-/tmp}/ltw.XXXXXX")" || no_puedo "mktemp fallo"
	trap 'rm -rf "$T"' EXIT

	# CONTROL NEGATIVO: una feature fabricada CON lista viva y SIN testigo tiene que salir.
	mkdir -p "$T/feats/fuga"
	printf 'export const x = () => http.get<ListResponse<Foo>>("/v1/x")\n' > "$T/feats/fuga/api.ts"
	printf 'export function View(){ const q=useQuery(); return <div>{q.data?.items}</div> }\n' > "$T/feats/fuga/view.tsx"
	# ⛔ SIN TUBERIA hacia `grep -q`, y no es estilo: bajo `set -o pipefail` un `grep -q` sale al
	# primer casamiento, cierra su extremo, el productor recibe SIGPIPE y pipefail propaga 141
	# — la comprobacion FALLA JUSTO CUANDO ACIERTA. Lo caza `lint:sigpipe-booleans`, y este
	# selftest lo cazo en su primera corrida sobre este mismo fichero.

	# ⛔ LOS TOTALES SE DERIVAN. Esta linea decia «9 passed, 0 failed» LITERAL, y bastaron dos
	# casos nuevos para que mintiera: 11 «ok» en pantalla y un resumen que seguia diciendo 9. Un
	# recuento a mano envejece en silencio y el resumen es justo lo que se cita sin releer.
	oks=0
	fails=0
	# `printf`, no `echo`: la sustitucion masiva que creo estos helpers se comio su propio cuerpo
	# y los dejo llamandose a si mismos. Con printf no hay forma de que vuelva a pasar.
	ok() { oks=$((oks + 1)); printf '  ok    %s\n' "$*"; }
	bad() { fails=$((fails + 1)); fail=1; printf '  FAIL  %s\n' "$*"; }

	salida="$(censar "$T/feats" || true)"
	case $'\n'"$salida"$'\n' in
	*$'\n'fuga$'\n'*) ok "CONTROL NEGATIVO: una feature sin testigo se caza" ;;
	*) bad "CONTROL NEGATIVO: el guion NO ve una feature sin testigo — es ciego"; fail=1 ;;
	esac

	# CONTROL POSITIVO: la misma feature, anadiendo el testigo, deja de salir.
	printf 'export function View(){ const q=useQuery(); return <div>{q.data?.has_more && <b/>}</div> }\n' > "$T/feats/fuga/view.tsx"
	salida="$(censar "$T/feats" || true)"
	case $'\n'"$salida"$'\n' in
	*$'\n'fuga$'\n'*) bad "CONTROL POSITIVO: sigue cazandola con el testigo puesto — no discrimina"; fail=1 ;;
	*) ok "CONTROL POSITIVO: con el testigo puesto, deja de ser hallazgo" ;;
	esac

	# Una feature cuyo UNICO has_more vive en un COMENTARIO no esta cubierta.
	printf 'export function View(){ /* aqui iria el aviso de has_more */ return <div/> }\n' > "$T/feats/fuga/view.tsx"
	salida="$(censar "$T/feats" || true)"
	case $'\n'"$salida"$'\n' in
	*$'\n'fuga$'\n'*) ok "un has_more que solo vive en un COMENTARIO no cuenta como testigo" ;;
	*) bad "un comentario que menciona has_more se contó como testigo — falso negativo"; fail=1 ;;
	esac

	# ⛔ UN COMENTARIO MULTILINEA TAMPOCO CUENTA, y esta celda es la que faltaba. La de arriba
	# solo prueba el bloque de UNA linea, que era justo lo que el limpiador viejo sabia quitar.
	# Un comentario JSX de varias lineas cuyas continuaciones empiezan por texto —lo normal al
	# explicar algo— colaba el token EN PROSA y ABSOLVIA a la feature aunque nadie pintara el
	# aviso. Falso NEGATIVO, el que no se nota: lo estreno en quien vino a arreglarlo.
	printf 'export function View(){ return <div>\n{/* aqui iria el aviso de has_more\n    y esta linea sigue hablando de has_more en prosa\n    y esta cita <ListTruncationBadge tambien */}\n</div> }\n' > "$T/feats/fuga/view.tsx"
	salida="$(censar "$T/feats" || true)"
	case $'\n'"$salida"$'\n' in
	*$'\n'fuga$'\n'*) ok "un has_more en un comentario MULTILINEA no cuenta como testigo" ;;
	*) bad "un comentario multilinea colo el token en prosa — falso NEGATIVO"; fail=1 ;;
	esac

	# El COMPONENTE COMPARTIDO cuenta como testigo: es la forma que la receta prescribe.
	printf 'import { ListTruncationBadge } from "@/features/_intel"\nexport function View(){ const q=useQuery(); return <ListTruncationBadge query={q} label="x" hint="y"/> }\n' > "$T/feats/fuga/view.tsx"
	salida="$(censar "$T/feats" || true)"
	case $'\n'"$salida"$'\n' in
	*$'\n'fuga$'\n'*) bad "el aviso COMPARTIDO no se contó como testigo — el censo se ciega al converger"; fail=1 ;;
	*) ok "el aviso COMPARTIDO cuenta como testigo, igual que un has_more en linea" ;;
	esac

	# ⛔ IMPORT SIN RENDER SIGUE SIENDO HALLAZGO (F-02 del contraste). Esta celda es la que
	# faltaba: la de arriba pone import Y render juntos, asi que pasaba con cualquiera de los dos.
	printf 'import { ListTruncationBadge } from "@/features/_intel"\nexport function View(){ const q=useQuery(); return <div/> }\n' > "$T/feats/fuga/view.tsx"
	salida="$(censar "$T/feats" || true)"
	case $'\n'"$salida"$'\n' in
	*$'\n'fuga$'\n'*) ok "importar el aviso SIN pintarlo sigue siendo hallazgo" ;;
	*) bad "un import sin render se contó como testigo — falso NEGATIVO, el que no se nota" ;;
	esac

	# Y su comentario tampoco cuenta: la misma regla que para has_more.
	printf 'export function View(){ /* aqui iria un ListTruncationBadge */ return <div/> }\n' > "$T/feats/fuga/view.tsx"
	salida="$(censar "$T/feats" || true)"
	case $'\n'"$salida"$'\n' in
	*$'\n'fuga$'\n'*) ok "un ListTruncationBadge en COMENTARIO no cuenta como testigo" ;;
	*) bad "un comentario que menciona el componente se contó como testigo"; fail=1 ;;
	esac

	# Una feature que DECLARA el tipo pero no LLAMA a nada no tiene lista que recortar.
	mkdir -p "$T/feats/soloTipo"
	printf 'import type { ListResponse } from "@/lib/api/types"\nexport function d<T>(x: ListResponse<T>) { return x.items }\n' > "$T/feats/soloTipo/derive.ts"
	printf 'export function V(){ return <div/> }\n' > "$T/feats/soloTipo/view.tsx"
	salida="$(censar "$T/feats" || true)"
	case $'\n'"$salida"$'\n' in
	*$'\n'soloTipo$'\n'*) bad "declarar el tipo sin llamar a nada NO deberia ser hallazgo"; fail=1 ;;
	*) ok "declarar ListResponse sin hacer ninguna llamada no es hallazgo" ;;
	esac

	# ⛔ AQUI HUBO UNA CELDA DEL ALIAS PARA `censar` Y SE RETIRO: PASABA POR LA RAZON
	# EQUIVOCADA. Su fixture declaraba `interface MiLista extends ListResponse<Foo>` y llamaba
	# `http.get<MiLista>`; con el conjunto de alias MUTADO al literal seguia en verde, porque
	# **la propia declaracion contiene la palabra `ListResponse`** y el predicado de «lista
	# viva» es por FICHERO, no por llamada. Un mutante que no mata la celda dice que la celda no
	# mide, y una celda que no mide es peor que ninguna: se cita como cobertura.
	#
	# Y al mirarlo salio el limite REAL, que vale mas que la celda: para `censar`, el conjunto de
	# alias **no cambia nada**. Declarar un alias exige nombrar `ListResponse`, asi que si la
	# declaracion vive dentro de la feature el literal ya esta ahi. El caso que SI cambia es el
	# de `capa_compartida` —donde lo que se casa es la LLAMADA, `http.get<AuditListResponse>`— y
	# ese tiene su celda propia, que su mutante SI mata. Una feature que alcance un alias
	# declarado FUERA de ella es el «camino cruzado», y eso lo mide
	# `registry.truncation-notice.test.ts`, no este censo.
	#
	# Se deja `${ALIAS_LISTA}` en el predicado de `censar` de todas formas: no es incorrecto, y
	# si manana aparece una forma de declaracion que no nombre el literal, ahi estara.

	# Una feature SIN lista viva no es asunto de este guion, tenga o no testigo.
	mkdir -p "$T/feats/sinlista"
	printf 'export const y = () => http.get<Foo>("/v1/y")\n' > "$T/feats/sinlista/api.ts"
	printf 'export function V(){ return <div/> }\n' > "$T/feats/sinlista/v.tsx"
	salida="$(censar "$T/feats" || true)"
	case $'\n'"$salida"$'\n' in
	*$'\n'sinlista$'\n'*) bad "una feature sin lista viva no deberia ser hallazgo"; fail=1 ;;
	*) ok "una feature sin lista viva no es hallazgo" ;;
	esac

	# Un directorio que no existe es 2, no 0.
	censar "$T/no-existe" >/dev/null 2>&1; rc=$?
	if [ "$rc" = "2" ]; then
		ok "un directorio ausente es 2 (NO HE PODIDO MIRAR), no 0"
	else
		bad "un directorio ausente devolvio $rc, esperaba 2"; fail=1
	fi

	# Y sobre el arbol REAL: health declara (no sale) y knowledge no (sale).
	REALF="$ROOT/web/src/features"
	if [ -d "$REALF" ]; then
		salida="$(censar "$REALF" || true)"
		case $'\n'"$salida"$'\n' in
		*$'\n'health$'\n'*) bad "arbol real: 'health' sale como hallazgo y SI declara el recorte"; fail=1 ;;
		*) ok "arbol real: 'health' no es hallazgo (declara en 3 vistas)" ;;
		esac
		# ⛔ ESTA CALIBRACION ERA UN LITERAL Y CERTIFICABA LA DERIVA QUE EXISTE PARA CAZAR. Decia
		#    «arbol real: 'knowledge' deberia ser hallazgo», y el dia que #1957 le puso el aviso a
		#    `knowledge` el selftest se puso ROJO por el EXITO: la feature dejo de ser hallazgo, que
		#    es exactamente lo que queriamos. Un ejemplo cableado convierte cada arreglo en un fallo
		#    del control, y entonces alguien borra el caso —y con el, la calibracion positiva—.
		#
		#    Ahora se DERIVA: basta con que el censo del arbol real encuentre ALGO, sea lo que sea.
		#    Eso sigue probando lo que el caso probaba —que el censo no esta ciego sobre el arbol de
		#    verdad, no solo sobre fixtures— y no envejece cuando una feature se arregla. Y si algun
		#    dia no queda ninguna, ese estado es LEGITIMO (la linea base se ha drenado) y se dice,
		#    en vez de fingir un rojo.
		reales="$(printf '%s\n' "$salida" | grep -c . || true)"
		base_n="$(grep -c . "${OLIVARES_LTW_BASELINE:-docs/list-truncation-baseline.txt}" 2>/dev/null || echo 0)"
		if [ "${reales:-0}" -gt 0 ]; then
			ok "arbol real: el censo encuentra $reales feature(s) — no esta ciego sobre el arbol de verdad"
		elif [ "${base_n:-0}" -eq 0 ]; then
			ok "arbol real: cero hallazgos con la linea base vacia — estado legitimo, el trabajo se acabo"
		else
			# Censo vacio con linea base POBLADA: o el censo se ha quedado ciego, o alguien arreglo
			# las features y no encogio la base. Las dos merecen rojo, y ninguna es «todo bien».
			bad "arbol real: CERO hallazgos con $base_n en la linea base — censo ciego, o base sin apretar"
			fail=1
		fi
	else
		bad "no encuentro $REALF — el selftest no puede calibrarse contra el arbol"; fail=1
	fi

	# DETERMINISMO. Va aqui porque este guion YA fue no determinista: un `grep -q` al final de una
	# tuberia bajo pipefail hacia que el mismo arbol diera 19 features en una corrida y 23 en la
	# siguiente, segun si el `sed` de aguas arriba llegaba a recibir SIGPIPE. Un gate que cambia de
	# respuesta sin que cambie el arbol es peor que uno que falla: se le cree la vez que acierta.
	if [ -d "$REALF" ]; then
		a="$(censar "$REALF" || true)"
		b="$(censar "$REALF" || true)"
		c="$(censar "$REALF" || true)"
		if [ "$a" = "$b" ] && [ "$b" = "$c" ]; then
			ok "DETERMINISMO: tres corridas sobre el arbol real dan la misma lista"
		else
			bad "DETERMINISMO: tres corridas dan listas distintas — el gate es una moneda al aire"; fail=1
		fi
	fi


	# ── LA CAPA COMPARTIDA ────────────────────────────────────────────────────────────────
	# Las tres respuestas, porque un gate que no distingue 1 de 2 certifica cuando no puede mirar.
	mkdir -p "$T/libapi"
	printf 'export const a = { l: (p?: P) => http.get<ListResponse<X>>("/v1/x", { query: { ...p } }) }\n' > "$T/libapi/endpoints.ts"
	sc="$(capa_compartida "$T/libapi" || true)"
	case "$sc" in
	*l*) ok "CAPA COMPARTIDA: una lista sin techo en lib/api se caza" ;;
	*) bad "CAPA COMPARTIDA: no ve una lista sin techo — ciega justo donde las dos sondas lo eran" ;;
	esac

	printf 'export const a = { l: (p?: P) => http.get<ListResponse<X>>("/v1/x", { query: { limit: 1000, ...p } }) }\n' > "$T/libapi/endpoints.ts"
	capa_compartida "$T/libapi" >/dev/null 2>&1
	case $? in
	0) ok "CAPA COMPARTIDA: con el techo puesto, deja de ser hallazgo" ;;
	*) bad "CAPA COMPARTIDA: sigue cazandola con el techo puesto — no discrimina" ;;
	esac

	printf 'export const a = { l: (p?: P) => http.get<ListResponse<X>>("/v1/x", { query: { org_limit: 5, ...p } }) }\n' > "$T/libapi/endpoints.ts"
	capa_compartida "$T/libapi" >/dev/null 2>&1
	case $? in
	1) ok "CAPA COMPARTIDA: org_limit no cuela como techo" ;;
	*) bad "CAPA COMPARTIDA: acepta org_limit como techo — falso verde" ;;
	esac

	# ⛔ UN COMENTARIO NO ES UNA LLAMADA, y esta funcion no descontaba NADA. Una frase que
	# citara el patron se contaba como lista sin techo — «(sin nombre) …», gate ROJO por prosa.
	# **Documentar el punto ciego lo disparaba**, que es como se queda un control sin documentar.
	# Se prueban las dos formas de comentario, porque la de bloque multilinea es la que mas se
	# usa para explicar y la que el limpiador viejo no sabia quitar.
	printf '// la sonda busca http.get<ListResponse< y esta frase NO es una llamada\n/*\n  http.get<ListResponse<X>>("/v1/x") dentro de un bloque, tampoco\n*/\nexport const a = { l: (p?: P) => http.get<ListResponse<X>>("/v1/x", { query: { limit: 1000, ...p } }) }\n' > "$T/libapi/endpoints.ts"
	capa_compartida "$T/libapi" >/dev/null 2>&1
	case $? in
	0) ok "CAPA COMPARTIDA: un comentario que CITA el patron no cuenta como llamada" ;;
	*) bad "CAPA COMPARTIDA: una frase en un comentario enrojece el gate — falso POSITIVO" ;;
	esac

	# Y su contrafactual, para que la celda no pase por descontar de mas: la llamada REAL que
	# hay debajo de esos comentarios sigue viendose si le quitamos el techo.
	printf '// la sonda busca http.get<ListResponse< y esta frase NO es una llamada\nexport const a = { l: (p?: P) => http.get<ListResponse<X>>("/v1/x", { query: { ...p } }) }\n' > "$T/libapi/endpoints.ts"
	sc="$(capa_compartida "$T/libapi" || true)"
	case "$sc" in
	*l*) ok "CAPA COMPARTIDA: y la llamada real bajo el comentario SI se sigue viendo" ;;
	*) bad "CAPA COMPARTIDA: descuenta de mas — se ha comido una llamada real" ;;
	esac

	# ⛔ UN ALIAS EN LA CAPA COMPARTIDA: el caso REAL que destapo este arreglo.
	printf 'export interface MiLista extends ListResponse<X> { next_from?: number }\nexport const a = { l: (p?: P) => http.get<MiLista>("/v1/x", { query: { ...p } }) }\n' > "$T/libapi/endpoints.ts"
	sc="$(capa_compartida "$T/libapi" || true)"
	case "$sc" in
	*l*) ok "CAPA COMPARTIDA: una llamada por ALIAS tambien se mira" ;;
	*) bad "CAPA COMPARTIDA: una llamada por alias no existe para el control" ;;
	esac

	printf '// SIN TECHO A PROPOSITO: porque si.\nexport const a = { l: () => http.get<ListResponse<X>>("/v1/x") }\n' > "$T/libapi/endpoints.ts"
	capa_compartida "$T/libapi" >/dev/null 2>&1
	case $? in
	2) ok "CAPA COMPARTIDA: exencion sin razon .go sale 2, no 1" ;;
	*) bad "CAPA COMPARTIDA: una exencion sin razon no sale 2 — un perdon sin motivo pasa por bueno" ;;
	esac

	printf '// SIN TECHO A PROPOSITO: handleListX (core/api/handlers_core.go:1) drena, no acepta consulta.\nexport const a = { l: () => http.get<ListResponse<X>>("/v1/x") }\n' > "$T/libapi/endpoints.ts"
	capa_compartida "$T/libapi" >/dev/null 2>&1
	case $? in
	0) ok "CAPA COMPARTIDA: la exencion CON razon nombrando el .go se honra" ;;
	*) bad "CAPA COMPARTIDA: no honra una exencion bien escrita" ;;
	esac

	# ── EL TRINQUETE, de punta a punta ────────────────────────────────────────────────────
	# Se invoca el guion ENTERO como subproceso con entradas fabricadas: es la unica forma de
	# probar la ruta de la linea base, que vive en la corrida normal y no en una funcion.
	mkdir -p "$T/rat/feats/fuga" "$T/rat/libapi"
	printf 'export const x = () => http.get<ListResponse<Foo>>("/v1/x")\n' > "$T/rat/feats/fuga/api.ts"
	printf 'export function View(){ const q=useQuery(); return <div>{q.data?.items}</div> }\n' > "$T/rat/feats/fuga/view.tsx"
	printf 'export const a = { l: (p?: P) => http.get<ListResponse<X>>("/v1/x", { query: { limit: 1000, ...p } }) }\n' > "$T/rat/libapi/endpoints.ts"

	printf 'fuga\n' > "$T/rat/base.txt"
	OLIVARES_WEB_FEATURES="$T/rat/feats" OLIVARES_WEB_LIBAPI="$T/rat/libapi" \
		OLIVARES_LTW_BASELINE="$T/rat/base.txt" bash "$0" >/dev/null 2>&1
	case $? in
	0) ok "TRINQUETE: una feature YA en la linea base no enrojece" ;;
	*) bad "TRINQUETE: enrojece con la linea base al dia — bloquearia a los cinco carriles" ;;
	esac

	: > "$T/rat/base.txt"
	OLIVARES_WEB_FEATURES="$T/rat/feats" OLIVARES_WEB_LIBAPI="$T/rat/libapi" \
		OLIVARES_LTW_BASELINE="$T/rat/base.txt" bash "$0" >/dev/null 2>&1
	case $? in
	1) ok "TRINQUETE: una feature NUEVA (no en la base) enrojece" ;;
	*) bad "TRINQUETE: no ve una feature nueva — el trinquete no trinca" ;;
	esac

	# Y la capa compartida MANDA aunque la linea base este al dia.
	printf 'fuga\n' > "$T/rat/base.txt"
	printf 'export const a = { l: (p?: P) => http.get<ListResponse<X>>("/v1/x", { query: { ...p } }) }\n' > "$T/rat/libapi/endpoints.ts"
	OLIVARES_WEB_FEATURES="$T/rat/feats" OLIVARES_WEB_LIBAPI="$T/rat/libapi" \
		OLIVARES_LTW_BASELINE="$T/rat/base.txt" bash "$0" >/dev/null 2>&1
	case $? in
	1) ok "TRINQUETE: la capa compartida enrojece aunque la linea base este verde" ;;
	*) bad "TRINQUETE: la linea base verde TAPA una regresion de lib/api" ;;
	esac

	[ "$fail" = "0" ] && { echo "check-list-truncation-witness selftest: ${oks} passed, ${fails} failed"; exit 0; }
	echo "check-list-truncation-witness selftest: ${oks} passed, ${fails} failed"; exit 1
fi

# ── corrida normal ────────────────────────────────────────────────────────────────────────────
FEATURES="${OLIVARES_WEB_FEATURES:-$ROOT/web/src/features}"
[ -d "$FEATURES" ] || no_puedo "no existe $FEATURES"

salida="$(censar "$FEATURES")" ; rc=$?
[ "$rc" = "2" ] && no_puedo "el censo no pudo recorrer $FEATURES"

# La capa compartida va DESPUES del censo pero su hallazgo pesa igual: si esta roja, el gate lo
# esta, aunque el censo por feature salga limpio.
LIBAPI="${OLIVARES_WEB_LIBAPI:-$ROOT/web/src/lib/api}"
compartida="$(capa_compartida "$LIBAPI")" ; rc_c=$?
[ "$rc_c" = "2" ] && no_puedo "no pude recorrer $LIBAPI (o hay una exencion sin razon)"
if [ "$rc_c" = "1" ]; then
	echo "check-list-truncation-witness: la CAPA COMPARTIDA tiene listas sin techo (el punto ciego de las dos sondas):" >&2
	printf '%s\n' "$compartida" | sed 's/^/    /' >&2
	echo "    Pon techo, o exime con «SIN TECHO A PROPOSITO» y la razon nombrando el handler .go." >&2
fi

n="$(printf '%s\n' "$salida" | grep -c . || true)"

# ── EL TRINQUETE ──────────────────────────────────────────────────────────────────────────────
# ⛔ POR QUE UNA LINEA BASE Y NO UN ROJO A SECAS. Este censo nombra TRABAJO PENDIENTE: hoy son 18
#    features. Cablearlo como rojo directo en el `pre-push` no protegeria nada — bloquearia CADA
#    push de los CINCO carriles hasta que el ultimo aterrice, y el remedio inevitable seria
#    `--no-verify`, que apaga el hook ENTERO. Un gate que obliga a saltarselo es peor que no
#    tenerlo.
#
# ⛔ Y ES UNA LISTA DE NOMBRES, NO UN CONTEO, que es la forma que usa la casa
#    (`docs/translation-drift-baseline.txt`). Con un numero, una feature nueva sin testigo entra
#    gratis en cuanto otra se arregle: el total no sube y nadie se entera. Con nombres, lo que
#    dispara es la APARICION, no la cantidad.
#
# Bajar la linea base es trabajo, no higiene: lo vigila `lint:baseline-shrink`.
BASE_LTW="${OLIVARES_LTW_BASELINE:-docs/list-truncation-baseline.txt}"
nuevas=""
if [ -f "$BASE_LTW" ]; then
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		grep -qxF -- "$f" "$BASE_LTW" 2>/dev/null || nuevas="${nuevas}${f}
"
	done < <(printf '%s\n' "$salida" | grep . || true)
	n_nuevas="$(printf '%s' "$nuevas" | grep -c . || true)"
	# Lo que sobra en la linea base se DICE, pero no enrojece: apretar es una decision, no un efecto.
	sobran=0
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		# ⛔ SIN TUBERIA hacia `grep -q` (141 EN EXITO bajo pipefail) — la regla que este mismo
		#    fichero predica mas arriba y que yo incumpli aqui.
		case $'\n'"$salida"$'\n' in
		*$'\n'"$f"$'\n'*) ;;
		*) sobran=$((sobran + 1)) ;;
		esac
	done < "$BASE_LTW"
	if [ "${n_nuevas:-0}" -gt 0 ]; then
		echo "check-list-truncation-witness: ⛔ ${n_nuevas} feature(s) NUEVA(S) sin testigo de recorte (no estaban en $BASE_LTW):" >&2
		printf '%s' "$nuevas" | sed 's/^/    /' >&2
		echo "    Pon el aviso, o —si el motor no recorta— documenta el handler y añádela a la linea base." >&2
		exit 1
	fi
	if [ "$rc_c" = "0" ]; then
		echo "check-list-truncation-witness: CLEAN — ${n} feature(s) en la linea base, 0 nuevas; capa compartida con techo.$([ "$sobran" -gt 0 ] && printf ' (%s de la linea base ya no salen: se puede apretar)' "$sobran")"
		exit 0
	fi
fi

if [ "${n:-0}" -eq 0 ] && [ "$rc_c" = "0" ]; then
	echo "check-list-truncation-witness: CLEAN — toda feature con lista viva declara su recorte, y la capa compartida pide techo."
	exit 0
fi
if [ "${n:-0}" -eq 0 ]; then exit 1; fi

echo "check-list-truncation-witness: ${n} feature(s) con lista viva y NINGUNA vista que declare el recorte:" >&2
printf '%s\n' "$salida" | sed 's/^/    /' >&2
cat >&2 <<'NOTA'

  Que hace falta en cada una, en este orden (la receta rodada de las PRs *-sin-recorte):
    1. MIDE EL MOTOR PRIMERO, y hay TRES respuestas, no dos:
       a) drena con `listAll`      -> no hay recorte que declarar; sale del censo con su file:line
       b) llama a `listQuery(r)`   -> LEE tu `limit`: pon techo (paso 2) y declara (paso 3)
       c) fija `Limit: listCap`    -> IGNORA tu `limit` y aun asi publica `has_more`. Un techo
          en la consola seria DECORATIVO; aqui solo toca el paso 3. Ejemplo medido el
          2026-08-23: `handleListCostCenterMappings` (modules/finops/costcenter.go:334)
          construye `model.Query{Limit: listCap}` con `listCap = 1000` (modules/finops/dto.go:22).
       El caso (a) largo, por si acaso: si el handler drena con `listAll` no hay recorte:
       sale del censo con su file:line. Un aviso que no puede aparecer no protege, solo lo afirma.
    2. Techo explicito `{ ...params, limit: params?.limit ?? X }` — en ESE orden.
       ⛔ AQUI DECIA `{ limit: X, ...params }`, y esa forma tambien fue la respuesta a un defecto
       real: el contraste F-03 encontro que con `{ ...params }` a secas la llamada era TypeScript
       valido y salia SIN `limit`, asi que el techo dependia de la disciplina del llamante en vez
       de ser el valor por defecto. Poner la constante DELANTE arreglaba eso.
       Lo que deja abierto es el otro borde: un `limit: undefined` explicito —TypeScript valido, y
       sale solo de `lista({ limit: filtro.limit })` con el filtro vacio— **borra el techo**, y la
       llamada vuelve a pedir los cien del store sin que nada lo diga. La forma de arriba cierra
       los dos: el techo va siempre, el llamante puede BAJARLO y no puede BORRARLO sin querer.
       Se deja escrito que hubo DOS formas y por que, en vez de sustituir en silencio: quien llegue
       vera que ambas contestaban a defectos medidos y que solo una contesta a los dos.
       ⚠ OJO: salir en esta lista NO significa que falte el techo. Medido el 2026-08-23 sobre
       las 18: DIECISIETE no piden techo ninguno, pero `workspace-dashboard` YA pide
       `limit: 50` y aun asi sale — porque recorta a 50 y no lo dice. Ese caso no necesita el
       paso 2 sino el 3, y ademas una DECISION sobre si 50 es el numero. Mira su `api.ts`
       antes de anadir un techo que ya esta.
       (Y no te fies de un `grep limit`: `limit:` casa dentro de `org_limit:`, que es una cuota
       del dominio, y los `fixtures.ts` estan llenos de ambas. A mi me dio dos falsos positivos.)
    3. El aviso compartido, alimentado por `has_more`, con un hint que diga que NO se puede inferir.
    4. Testigo de TRANSPORTE (mira la URL) y testigo de VISTA MONTADA (una sonda de fuente no
       prueba alcanzabilidad).

  Esta lista es un SUELO: el censo es por FEATURE, asi que una cubierta puede esconder vistas
  sin cubrir. Y es de FUENTE: no prueba que el aviso se pinte.
NOTA
exit 1
