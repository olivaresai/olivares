#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-phone-home-claims.sh — a repository gate, el trinquete sobre la promesa absoluta de «zero phone-home».
#
# ⛔ QUÉ VIGILA Y QUÉ NO, porque la diferencia es toda la fila. El modelo de derechos está DECIDIDO
# y firmado: la suscripción es acceso a REPOSITORIOS y **el phone-home está APROBADO** para emisión
# de licencias y updates. Con eso firmado, una página viva que promete «zero phone-home» afirma lo
# contrario de lo que el producto hace.
#
# ⛔ AQUÍ DECÍA «el licenciamiento offline se RECHAZÓ» A SECAS, y esa frase aislada tiene la
# polaridad al revés — la señaló el contraste `sol max` (F8). Lo que se rechazó el 2026-08-15 fue el
# modelo de DISTRIBUCIÓN totalmente offline por insuficiente; la **validación** de licencia offline
# con Ed25519 es justo lo que `LICENSING.md:170-172` firma y sigue siendo cierta. Confundirlas hace
# que quien lea esta cabecera crea que hay que retirar también la promesa de validación offline.
#
# ⛔ AQUÍ DECÍA «retirar las que ya existen NO es mío: la redacción la decide». **Eso caducó el
# 2026-08-20 y se ejecutó el 2026-08-28.** La redacción de sustitución existe, está firmada
# y no hay nada que preguntar: `LICENSING.md:166-176` — *«Verifying a licence never calls anyone.
# Downloading what you paid for does.»* Community conserva Ed25519 offline, sin kill switch y sin
# llamada de licencia del kernel AGPL; la línea comercial dice que la suscripción es la credencial
# con la que se descargan módulos, updates y parches, y que no hay telemetría obligatoria ni egress
# al plano de control por defecto.
#
# Esto es un TRINQUETE, no una prohibición: el suelo es lo que hay hoy, y lo único que impide es que
# la cifra SUBA. Cuando una página se reescribe, la línea base BAJA y el trinquete lo aplaude — un
# suelo que sólo puede bajar es lo contrario de congelar el texto.
#
# ⛔ EL ARCHIVO CONGELADO NO CUENTA. `docs-site/src/content/docs/2026-06/` es una instantánea
# histórica declarada: contarla haría que la línea base incluyera texto que NADIE va a reescribir, y
# el número dejaría de significar «promesas vivas».
#
# Salida: 0 no sube · 1 hay una promesa NUEVA (la nombra) · 2 NO HE PODIDO MIRAR. Nunca un verde.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-phone-home-claims: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2
	exit 2
}
BASE="${OLIVARES_PHONEHOME_BASELINE:-docs/phone-home-claims-baseline.txt}"

# ⛔ LAS SUPERFICIES SON LA MITAD DEL GATE, Y ESTA LISTA DEJABA FUERA LA QUE VE EL CLIENTE.
# Hasta el 2026-08-28 decía `docs-site/src/content/docs docs/trust web/src` — la documentación y la
# consola. **El correo de licencia y el portal de cliente NO estaban**, y ahí vivía la promesa que
# recibió por correo el 2026-08-28 a las 16:53:29Z: `email/copy/en.json` («never phones home,
# and validates fully offline») y `commercial/license-worker/src/portal/pages/{licenses,trust}.ts`.
# El trinquete no es que no la contara: **es que no la miraba**, así que llevaba desde el 08-15
# diciendo OK sobre la superficie más cara que tenemos. Se añaden las dos, y los tres ficheros de
# raíz que un comprador lee antes de instalar.
#
# Se derivan por BÚSQUEDA dentro de cada superficie, no por lista de ficheros: una lista de rutas
# caduca en cuanto alguien añade una página.
# ⛔ Y `docs/trust` A SECAS TAMPOCO BASTABA — hallazgo del contraste `sol max` del 2026-08-28 (F7):
# este mismo lote reescribió `ARCHITECTURE.md-ARCHITECTURE.md:104` y `docs/RELEASE-VERIFICATION.md:103`, y
# NINGUNO estaba vigilado. Vigilar `docs` entero cuesta **cuatro** filas de línea base (medido:
# `POLAR-COMMERCIAL-SETUP.md`, `07-LICENSE-AND-OPEN-CORE.md`, `contracts-site.md`,
# `ai-context/PROJECT-CONTEXT.md`, una mención acotada cada uno). `docs/trust` se quita de la lista
# porque `grep -r` lo contaría DOS veces al estar dentro de `docs`.
SUPERFICIES="${OLIVARES_PHONEHOME_DIRS:-docs-site/src/content/docs docs web/src email/copy commercial/license-worker/src}"
# Ficheros sueltos de la raíz: no son directorios, así que van por su propia lista.
# ⛔ SUPPORTERS.md ENTRA EL 2026-08-29 PORQUE ESE MISMO DIA NACE EL FICHERO, no por simetria.
#    Es una pagina PUBLICA de raiz —viaja en el export, TOP_ALLOW— y llego a decir «nothing phones
#    home». Sin esta linea ningun gate la mira: las superficies de arriba son DIRECTORIOS y un
#    fichero de raiz no cae en ninguno. La lista sigue siendo TECLEADA, que es su limite conocido:
#    las seis traducciones del README (README.de.md … README.zh.md) tampoco estan, y el patron ya
#    sabe aleman, frances y castellano. Medido hoy: las seis limpias. Cerrarlo pide un glob, y eso
#    es un cambio de forma que no se hace la vispera de un corte.
FICHEROS="${OLIVARES_PHONEHOME_FILES:-INSTALL.md README.md LICENSING.md SUPPORTERS.md}"
# ⛔ TRES RESPUESTAS PARA UNA SUPERFICIE AUSENTE, y la del medio es la que faltaba.
#
#    Medido el 2026-08-29 sobre un export real de 8 890 ficheros: este gate salia **rc=2
#    ⛔ Y el credito al carril que lo midio va en el commit y en el bus, NO aqui: este fichero
#       VIAJA, y un token de sesion en el arbol publico dispara el trinquete de vocabulario del
#       export. Lo acabo de pisar escribiendo justo esta linea.
#    POR CONSTRUCCION** en el arbol publicado. `commercial` esta en el TOP_BLOCK del exportador
#    («Internal infra, never part of the open-core product»), asi que `commercial/license-worker/src`
#    NUNCA viaja; y el 2026-08-28 `fb63b2ff3` la anadio a las superficies. Un cambio correcto en el
#    hub volvio el gate INCONTESTABLE en el export — y como 2 es la tercera respuesta, no es un falso
#    verde: es peor de leer, porque nadie puede decir si el arbol publicado esta limpio.
#
#    El remedio NO se disena aqui: `ef08ea62d` (2026-08-01) ya resolvio ESTA MISMA CLASE con
#    `scripts/private-leg.sh`, para legs cuyo *directorio de trabajo* es privado. Aquel no encaja
#    tal cual —este gate BARRE la ruta, no hace `cd`— pero su principio es el que vale, y su
#    comentario lo dice mejor de lo que yo lo diria: *«The tree test is a POSITIVE marker stamped by
#    the export, not the absence of the directory itself — absence-as-marker is exactly the fail-open
#    this script closes.»*
#
#    ⇒ El marcador es de FIAR porque el propio exportador se niega a correr si `.olivares-public-export`
#      esta trackeado en el arbol fuente (`export-public.sh:1028`): solo puede existir en un export.
#
#      1. presente ............................. se barre
#      2. ausente Y hay marcador de export ..... NO APLICA, se dice en voz alta, se sigue
#      3. ausente y NO hay marcador ............ rc=2, igual que siempre: en el hub falta algo
#
#    En el hub el comportamiento NO cambia. Y si en el export faltaran TODAS, el control positivo de
#    mas abajo (cero coincidencias con linea base poblada) sigue dando 2: no se puede aprobar el vacio.
ES_EXPORT=0
[ -f PUBLIC-EXPORT.md ] && [ -f .olivares-public-export ] && ES_EXPORT=1

SUPERFICIES_VIVAS=""
while IFS= read -r d; do
	[ -n "$d" ] || continue
	if [ -d "$d" ]; then
		SUPERFICIES_VIVAS="$SUPERFICIES_VIVAS $d"
	elif [ "$ES_EXPORT" -eq 1 ]; then
		echo "check-phone-home-claims: NO APLICA — $d no forma parte del arbol publico (PUBLIC-EXPORT.md); no se barre." >&2
	else
		echo "check-phone-home-claims: ⛔ NO HE PODIDO MIRAR: no existe la superficie $d" >&2
		exit 2
	fi
done <<EOF
$(printf '%s\n' $SUPERFICIES)
EOF
SUPERFICIES="${SUPERFICIES_VIVAS# }"
[ -n "$SUPERFICIES" ] || {
	echo "check-phone-home-claims: ⛔ NO HE PODIDO MIRAR: no queda NINGUNA superficie que barrer." >&2
	exit 2
}

FICHEROS_VIVOS=""
while IFS= read -r f; do
	[ -n "$f" ] || continue
	if [ -f "$f" ]; then
		FICHEROS_VIVOS="$FICHEROS_VIVOS $f"
	elif [ "$ES_EXPORT" -eq 1 ]; then
		echo "check-phone-home-claims: NO APLICA — el fichero vigilado $f no viaja en el arbol publico." >&2
	else
		echo "check-phone-home-claims: ⛔ NO HE PODIDO MIRAR: no existe el fichero vigilado $f" >&2
		exit 2
	fi
done <<EOF
$(printf '%s\n' $FICHEROS)
EOF
FICHEROS="${FICHEROS_VIVOS# }"
# ⚠ Aqui NO se exige que quede alguno: los cuatro vigilados son de raiz y TODOS viajan hoy
#   (TOP_ALLOW). Si el dia de manana uno dejara de viajar, el export lo dice en voz alta y el
#   barrido sigue con los demas; la salvaguarda de "no aprobar el vacio" la pone el control
#   positivo de mas abajo, que mira el conjunto entero y no esta lista.

# Rutas EXCLUIDAS del conteo, cada una con su razón — no hay exclusión sin dueño:
#   · `docs-site/src/content/docs/2026-06/`  archivo congelado (arriba).
#   · `…/email/templates.generated.ts`       ARTEFACTO de `node email/build.mjs`. Contarlo mide la
#     misma promesa 56 veces (7 locales × 4 variantes × texto/html) y hace que un cambio de UNA
#     cadena mueva el contador en decenas. Lo que garantiza que el artefacto dice lo que dice su
#     fuente es `lint:email-brand`, que RE-EJECUTA el build y falla si difiere: para esa pregunta
#     concreta es un gate más fuerte que éste.
#   · ficheros de TEST                       el sujeto de este gate es el texto PUBLICADO, y un test
#     no lo lee ningún cliente. Y no es teórico: `web/src/features/attestation/attestation.test.tsx`
#     contiene la promesa retirada **dentro de la aserción que exige que NO esté**
#     (`expect(screen.queryByText(/nothing phones home/i)).toBeNull()`). Contarla convierte al
#     guardián en infractor — el propio `components.tsx:634` ya avisaba de que este gate «busca el
#     literal y no distingue una CITA de una afirmación», y al ensanchar el patrón el 2026-08-28 la
#     trampa se disparó en la primera pasada.
#     ⚠ LO QUE ESTA EXCLUSIÓN DEJA SIN CUBRIR, dicho en vez de tapado: un test que fije la promesa
#     VIEJA como esperada es invisible aquí. Ésa es la clase que costó `check-meta-12-phone-home.sh`
#     —cuya batería mataba el mutante «aplicado al docs-site», o sea el trabajo correcto— y se
#     resuelve leyendo el test, no contando cadenas.
# ⚠ Y SE FILTRAN LÍNEAS `ruta:cuenta`, NO RUTAS: anclar con `$` aquí no casa nunca, porque después
# de la ruta viene `:` y el número. La primera versión de esta lista lo hizo y la exclusión de los
# tests fue INERTE — el gate siguió contando el fichero que asegura que la promesa NO está. Se ancla
# con `:` (grep -rc siempre emite `ruta:cuenta`).
EXCLUIR='docs-site/src/content/docs/2026-06/|/email/templates\.generated\.ts:|^docs/contracts/'
EXCLUIR="$EXCLUIR"'|\.(test|spec)\.(ts|tsx|js|mjs):|_test\.go:|(^|/)tests?/'
#   · `docs/contracts/`                      el export lo bloquea **al por mayor**
#     (the export curation script, línea 212, con sólo TRES contratos exceptuados en `:234-238`), y sus
#     ficheros llevan el número de sesión en el NOMBRE por convención (`docs/contracts/SNNN-*`).
#     Vigilarlo metía `docs/contracts-site.md` en la línea base, que SÍ se publica ⇒
#     `lint:export` cortó el push con `LEAK[raw] …-site.md` en dos líneas. Medido el
#     2026-08-28: 26 minutos de carril rápido tirados. No es una superficie viva —no se publica—,
#     así que dejarlo fuera no pierde cobertura de nada que un cliente lea.
#     ⚠ `LICENSING.md-LICENSE-AND-OPEN-CORE.md` y `docs/POLAR-COMMERCIAL-SETUP.md` TAMBIÉN son privados
#     y SÍ se quedan vigilados a propósito: son donde miran las sesiones, sus nombres no llevan
#     token, y el encargo de C09-09 los nombra explícitamente.
#   · la propia LÍNEA BASE                   vive bajo `docs/`, que pasó a vigilarse hoy, y su
#     cabecera CITA las cuatro menciones acotadas para explicarlas. El gate se contaba a sí mismo:
#     `docs/phone-home-claims-baseline.txt: 0 → 4` en la primera pasada tras documentarla. Es la
#     misma clase que el fichero de test — un registro sobre el sujeto no es el sujeto.
EXCLUIR="$EXCLUIR"'|'"$(printf '%s' "$BASE" | sed 's/[.[\*^$]/\\&/g')"':'

# La FORMA de la promesa absoluta, no la palabra suelta: «phone-home» aparece legítimamente en
# documentación que EXPLICA el mecanismo. Lo que se persigue es la promesa de que no existe.
#
# ⛔ Y HASTA EL 2026-08-28 ESTA FORMA ERA SÓLO INGLESA (con «sin» de propina), así que **el gate
# medía UN locale de los siete que publicamos**. Medido ese día sobre `origin/main`: la línea base
# traía `docs-site/.../how-to/air-gap-install.md` con 2, y sus seis traducciones —que llevaban la
# MISMA promesa en su idioma: `Null Phone-Home`, `Cero phone-home`, `Zéro phone-home`,
# `フォンホームゼロ`, `Никаких обращений «домой»`, `零外呼`— daban **0 y no estaban en la base**.
# No era un fallo del conteo: era el alcance del patrón. Un gate dice lo que su mecanismo de
# DESCUBRIMIENTO alcanza, y éste alcanzaba el inglés.
#
# EL ALCANCE DE HOY, declarado para que nadie lea un verde como «no hay promesas en ningún sitio»:
# se reconocen las formas MEDIDAS en este árbol para los siete locales publicados. Una forma nueva
# —otro idioma, otra manera de decirlo— NO se ve hasta que se añada aquí, y cada una lleva su caso
# en `scripts/test-phone-home-claims.sh`: una alternativa sin control positivo es una conjetura.
#
# ⚠ Y UNA ALTERNATIVA QUE SE PROBÓ Y SE RETIRA, porque enseña dónde está el filo: `llama a casa`
# (verbo) casaba con *«El motor **no llama a casa**: no hay canal de telemetría hacia el
# proveedor…»* — que es la traducción exacta del inglés *«The engine does not phone home»*, o sea
# una afirmación ACOTADA y CIERTA, la que este gate NO persigue. `llamada a casa` (sustantivo) se
# queda, porque «sin llamada a casa» sí es la forma absoluta. Un idioma se añade con la forma
# ABSOLUTA, no con el verbo.
PATRON='(zero|no|never|nothing|without|null|kein|keine|ohne|sin|cero|nada|ningún|ningun|sans|aucun|aucune|zéro|rien)[[:space:]]+(phone[- ]?home|llamada a casa)'
PATRON="$PATRON"'|phone[- ]home[[:space:]]*[:=][[:space:]]*(false|none|nunca)'
PATRON="$PATRON"'|(never|nothing)[[:space:]]+phones?[[:space:]]+home'
PATRON="$PATRON"'|nichts[[:space:]]+nach[[:space:]]+Hause[[:space:]]+telefoniert'
PATRON="$PATRON"'|nada[[:space:]]+(hace[[:space:]]+phone[- ]?home|llama[[:space:]]+de[[:space:]]+vuelta)'
PATRON="$PATRON"'|rien[[:space:]]+ne[[:space:]]+(fait[[:space:]]+de[[:space:]]+phone[- ]?home|rappelle)'
PATRON="$PATRON"'|フォンホーム(ゼロ|は一切なし|は発生しません|はありません)'
PATRON="$PATRON"'|零外呼|不会回拨'
# ⛔ Y AQUÍ HABÍA UN `回拨。` PELADO, QUE CASA LA AFIRMACIÓN CONTRARIA. Hallazgo del contraste
# `sol max` (F3): «升级命令会回拨。» —«el comando upgrade SÍ llama»— es una frase CIERTA y el patrón
# la marcaba como promesa. Retirado; queda `不会回拨`, que lleva la negación dentro.
# ⛔ DOS TRAMPAS EN LA MISMA LÍNEA, Y LAS DOS LAS CAZÓ LA BATERÍA, NO YO.
#
# 1 · `LC_ALL=C` + `grep -i` **sólo pliega ASCII**, así que una alternativa cirílica en minúsculas
#     NO casa con la misma frase capitalizada — y en ruso la promesa era un ENCABEZADO:
#     `## Никаких обращений «домой»`. Primera corrida de la batería: `positivo ru` → rc 0 con la
#     frase literal del árbol dentro de la página.
# 2 · El arreglo obvio, `[Нн]`, **es PEOR que el defecto**: bajo `LC_ALL=C` una clase de corchetes
#     es un conjunto de BYTES, y `Н` = D0 9D, `н` = D0 BD. `[Нн]` casa UN byte de {D0,9D,BD} y
#     parte la secuencia UTF-8 — medido: `grep -ciE '[Нн]икаких'` sobre el literal da **0**,
#     mientras `grep -ciE 'Никаких'` da 1. Un arreglo que deja el gate igual de ciego y encima
#     parece resuelto. Va con ALTERNANCIA de cadenas completas, que es byte-segura.
#     Lo mismo con `«?`: el `?` cuantifica el ÚLTIMO BYTE (AB) y deja el C2 obligatorio, así que
#     la comilla pasa a ser exigida a medias. Se sustituye por `.{0,6}`, que bajo C son bytes.
# (El alemán no lo necesita: es ASCII y `-i` sí lo pliega. El japonés y el chino no tienen caja.
#  El francés `Zéro`/español `Cero` sí pliegan, porque lo único que cambia es la inicial ASCII.)
# ⚠ Y LA MISMA PODA QUE EN ESPAÑOL, encontrada por el gate de META-12 en su primera corrida:
# `не звонит домой` (verbo, sujeto = el motor) es la traducción exacta de *«The engine does not
# phone home»* — ACOTADA y cierta, la que este gate NO persigue. La forma ABSOLUTA en ruso es
# `Никаких обращений «домой»` (encabezado) y `ничто не …`. Se conserva sólo ésa.
PATRON="$PATRON"'|(Н|н)икаких[[:space:]]+обращений.{0,6}(Д|д)омой'
PATRON="$PATRON"'|(Н|н)ет[[:space:]]+обращений.{0,6}(Д|д)омой'
# Y `ничто не обращается` A SECAS TAMPOCO VALE: casa con *«Внутри изоляции ничто не обращается
# наружу»* —«dentro de la brecha nada llama al exterior»—, que es la frase CIERTA con la que
# sustituyó la promesa. La forma absoluta lleva DESTINO: «домой» o `Olivares`. Tercera poda
# del mismo día y la tercera la encontró un gate corriendo, no una lectura.
PATRON="$PATRON"'|(Н|н)ичто[[:space:]]+не[[:space:]]+(обращается|звонит).{0,40}(домой|Olivares)'
PATRON="$PATRON"'|обратных[[:space:]]+вызовов[^.]{0,60}нет'
# ⛔ «phones BACK» — la forma que ni el censo ni el patrón anterior veían, y estaba en las SIETE.
# Encontrada el 2026-08-28 sólo porque las traducciones la dicen con otro verbo: el censo de
# buscaba `phones? home` y `explanation/security/security-model.md` decía *«nothing phones BACK to
# Olivares AI»*. La inglesa no casaba con nada; las de `es`/`fr` sí, con `nada llama de vuelta` y
# `rien ne rappelle`. **Fue el idioma el que delató al inglés**, que es justo el argumento de por
# qué este patrón dejó de ser monolingüe.
PATRON="$PATRON"'|(nothing|never)[[:space:]]+phones?[[:space:]]+back'
PATRON="$PATRON"'|nichts[[:space:]]+funkt[[:space:]]+.{0,24}zurück'
# ⛔⛔ LAS SIETE FORMAS QUE ESTE MISMO LOTE RETIRÓ Y QUE EL PATRÓN NO VEÍA. Hallazgo ALTO del
# contraste `sol max` del 2026-08-28 (F1), y es el más caro de los ocho: **restaurar seis de las
# siete traducciones defectuosas del correo no habría subido ninguna cuenta.** El patrón se
# construyó desde la familia «zero phone-home», que es la que yo tenía delante — no desde las
# cadenas que de verdad estaba retirando. Cada una lleva su caso en la batería, con el literal
# exacto de `origin/main`.
PATRON="$PATRON"'|zero[[:space:]]+(telemetry|callbacks?)'
PATRON="$PATRON"'|never[[:space:]]+contacts?[[:space:]]+(the[[:space:]]+vendor|Olivares|an?[[:space:]]+external)'
PATRON="$PATRON"'|niemals[[:space:]]+Kontakt'
PATRON="$PATRON"'|nunca[[:space:]]+se[[:space:]]+comunica'
PATRON="$PATRON"'|ne[[:space:]]+contacte[[:space:]]+jamais[[:space:]]+(de[[:space:]]+serveur|aucun)'
PATRON="$PATRON"'|外部サーバーへの通信は一切行'
PATRON="$PATRON"'|никогда[[:space:]]+не[[:space:]]+связывается'
PATRON="$PATRON"'|绝不联网回传'
# ⚠ NINGUNA de estas ocho incluye el sujeto ACOTADO que la redacción firmada usa: «verifying it
# never calls **us**», «nimmt keinen Kontakt zu uns auf», «su verificación no se comunica». La
# frontera es el SUJETO, y un ERE no lo lee — por eso las alternativas nombran el objeto absoluto
# (`the vendor`, `an external server`, `externen Servern`) y no el verbo a secas.

# Se cuentan los ficheros con al menos una promesa **y ademas** todas las rutas que la linea base
# ya vigila, aunque hoy den 0. Dos razones: (1) `check-baseline-shrink.sh` rechaza —con razon— que
# una linea base pierda entradas, porque quitarlas es indistinguible de silenciar el gate; y (2)
# una ruta vigilada que baja a 0 y vuelve a subir tiene que enrojecer, y sin su fila de 0 la
# subida se leeria como una entrada nueva… o como nada.
ACTUALES="$( {
	# shellcheck disable=SC2086 # $SUPERFICIES/$FICHEROS son listas de rutas sin espacios, a propósito.
	grep -rEic "$PATRON" $SUPERFICIES $FICHEROS 2>/dev/null |
		grep -vE "$EXCLUIR" |
		awk -F: '$NF > 0 { printf "%s\t%s\n", $NF, substr($0, 1, length($0) - length($NF) - 1) }'
	if [ -r "$BASE" ]; then
		while IFS= read -r linea; do
			case "$linea" in ''|\#*) continue ;; esac
			ruta="${linea#*	}"
			[ -f "$ruta" ] || continue
			c="$(grep -Eic "$PATRON" "$ruta" 2>/dev/null || true)"
			[ "${c:-0}" -eq 0 ] && printf '0\t%s\n' "$ruta"
		done < "$BASE"
	fi
} | LC_ALL=C sort -u)"
N="$(printf '%s\n' "$ACTUALES" | grep -c . || true)"

# ⛔⛔ CONTROL POSITIVO DE VERDAD: EL CANARIO. Hallazgo del contraste `sol max` (F4): el brazo de
# abajo mira `N`, que cuenta FILAS —y `ACTUALES` añade una fila `0<TAB>ruta` por cada ruta de la
# base que existe—, así que **con una base poblada por ficheros existentes nunca puede disparar**.
# Hoy el árbol tiene CERO coincidencias positivas y el gate imprime «23 ruta(s) … OK»: el brazo era
# inerte y la afirmación de deny-closed, falsa.
#
# Y el arreglo no es contar positivos: con el árbol limpio los positivos son 0 **legítimamente**, y
# un brazo que enrojezca por eso convierte el éxito en «no he podido mirar». Lo que hace falta es un
# control que no dependa del árbol: **el patrón se ejerce contra promesas conocidas antes de juzgar
# nada.** Si una sola no casa, el patrón está roto y la respuesta es 2, nunca 0.
_canario_fallos=0
while IFS= read -r _linea; do
	[ -n "$_linea" ] || continue
	# Here-string, NO tubería: `printf … | grep -q` devuelve 141 CUANDO ENCUENTRA (SIGPIPE al
	# productor + pipefail), o sea el canario fallaría justo al funcionar. Lo cazó
	# `lint:sigpipe-booleans` sobre esta misma línea.
	grep -qEi "$PATRON" <<< "$_linea" || {
		echo "check-phone-home-claims: ⛔ NO HE PODIDO MIRAR: el patrón NO reconoce una promesa" >&2
		echo "                         conocida: «$_linea»" >&2
		_canario_fallos=$((_canario_fallos + 1))
	}
done <<'CANARIO'
Mirror them into a private registry and install — with zero phone-home.
The licence never phones home, and validates fully offline.
Nothing phones home; everything is self-hosted.
There is no telemetry-home — nothing phones back to Olivares AI.
Zero telemetry, zero callbacks.
The product never contacts Olivares AI.
Registry spiegeln und installieren — mit null Phone-Home.
Sie nimmt niemals Kontakt zu externen Servern auf.
Refleja en un registro privado e instala — con cero phone-home.
Nunca se comunica con el proveedor.
Mettez-les en miroir dans un registre privé — sans aucun phone-home.
Elle ne contacte jamais de serveur externe.
フォンホームゼロ
外部サーバーへの通信は一切行わず
Никаких обращений «домой»
Она никогда не связывается с внешними серверами.
零外呼
绝不联网回传信息
CANARIO
if [ "$_canario_fallos" -gt 0 ]; then
	echo "check-phone-home-claims: ⛔ $_canario_fallos promesa(s) conocida(s) sin reconocer — el patrón" >&2
	echo "                         caducó o se rompió al editarlo. No juzgo el árbol con él." >&2
	exit 2
fi

# El brazo heredado, que se conserva por lo que SÍ cubre: superficies movidas o borradas. Ya no es
# «el control positivo» — ese papel lo tiene el canario de arriba.
# ⛔ Y EL CONTROL POSITIVO ERA INERTE. Pedía cero coincidencias **y** una línea base ILEGIBLE, y
# en este repositorio la base siempre se lee, así que el brazo no podía dispararse nunca. El caso
# que importa es el contrario: cero coincidencias con una base que SÍ existe y lista rutas — eso
# no es progreso, es que el patrón caducó o el texto se movió. Medido: reformular las tres
# promesas de una página de «zero phone-home» a «zero phoning home» dejaba las tres en la página y
# el gate imprimía «0 fichero(s) … ✔ 4 retirada(s) … OK», rc 0.
if [ "${N:-0}" -eq 0 ] && [ -r "$BASE" ] && [ -s "$BASE" ]; then
	echo "check-phone-home-claims: ⛔ NO HE PODIDO MIRAR: cero coincidencias y una línea base con" >&2
	echo "                         $(grep -c . "$BASE") ruta(s). Eso no es que las promesas se hayan" >&2
	echo "                         retirado: es que el patrón dejó de casar. Un conjunto vacío" >&2
	echo "                         frente a una base poblada no se aprueba." >&2
	exit 2
fi
if [ "${N:-0}" -eq 0 ] && [ ! -r "$BASE" ]; then
	echo "check-phone-home-claims: ⛔ NO HE PODIDO MIRAR: cero coincidencias y sin línea base." >&2
	echo "                         O el patrón caducó, o las superficies se movieron. Un conjunto" >&2
	echo "                         vacío sin control no se aprueba." >&2
	exit 2
fi

if [ ! -r "$BASE" ]; then
	echo "check-phone-home-claims: ⛔ NO HE PODIDO MIRAR: no leo la línea base $BASE" >&2
	echo "                         Una línea base ausente no es «cero promesas»; es no haber mirado." >&2
	exit 2
fi

# ⛔ AQUÍ SE COMPARABAN LÍNEAS `cuenta<TAB>ruta` CON `grep -vxF`, Y ESO NO DISTINGUE SUBIR DE BAJAR.
# Medido el 2026-08-28 al ejecutar C09-05, que es literalmente el trabajo que este trinquete dice
# esperar: al retirar las promesas, `2 → 0` y `1 → 0` salieron como **«PROMESA NUEVA»** y el gate
# devolvió rc 1. Una fila cuya cuenta BAJA es la retirada que la cabecera de este fichero aplaude;
# anunciarla como promesa nueva convierte el trinquete en un cerrojo sobre el texto viejo, que es
# justo lo que dice no ser. La comparación va ahora por RUTA y por CUENTA, y sólo enrojece el que
# sube — un fichero nuevo con una promesa cuenta como subida de 0 a N, así que el brazo estricto no
# pierde nada.
CMP="$(printf '%s\n' "$ACTUALES" | awk -F'\t' '
	NR == FNR {
		if ($0 ~ /^[[:space:]]*$/ || $0 ~ /^#/) next
		base[$2] = $1 + 0
		next
	}
	{ ahora[$2] = $1 + 0 }
	END {
		for (p in ahora) {
			b = (p in base) ? base[p] : 0
			if (ahora[p] > b) printf "SUBE\t%d\t%d\t%s\n", b, ahora[p], p
		}
		for (p in base) {
			a = (p in ahora) ? ahora[p] : 0
			if (a < base[p]) printf "BAJA\t%d\t%d\t%s\n", base[p], a, p
		}
	}' "$BASE" -)"
SUBEN="$(printf '%s\n' "$CMP" | grep -c '^SUBE	' || true)"
BAJAN="$(printf '%s\n' "$CMP" | grep -c '^BAJA	' || true)"

echo "check-phone-home-claims: $N ruta(s) vigilada(s) · línea base $(grep -cE '^[0-9]+[[:space:]]' "$BASE") · suben $SUBEN · bajan $BAJAN"

if [ "${SUBEN:-0}" -gt 0 ]; then
	echo "check-phone-home-claims: ⛔ FORMA NUEVA que casa una promesa ABSOLUTA de phone-home — y el" >&2
	echo "                         modelo firmado la APRUEBA, así que afirmarla dice lo contrario de" >&2
	echo "                         lo que el producto hace:" >&2
	printf '%s\n' "$CMP" | grep '^SUBE	' |
		awk -F'\t' '{ printf "                           %s: %d → %d\n", $4, $2, $3 }' >&2
	echo "                         La redacción que la sustituye está firmada y escrita:" >&2
	echo "                         LICENSING.md:166-176 — verificar una licencia no llama a nadie;" >&2
	echo "                         descargar lo que has pagado sí. No hay nada que preguntar." >&2
	echo "                         ⚠ Si la frase es ACOTADA y CIERTA (sujeto = la verificación, el" >&2
	echo "                         arranque o el motor), el patrón no lee sujetos: súbela a la línea" >&2
	echo "                         base con su razón, no la reescribas para esquivar un ERE." >&2
	exit 1
fi
if [ "${BAJAN:-0}" -gt 0 ]; then
	echo "check-phone-home-claims: ✔ $BAJAN ruta(s) con menos promesas que la línea base:"
	printf '%s\n' "$CMP" | grep '^BAJA	' |
		awk -F'\t' '{ printf "                           %s: %d → %d\n", $4, $2, $3 }'
	echo "check-phone-home-claims: bájala en el mismo commit. Contenido exacto de $BASE:"
	printf '%s\n' "$ACTUALES" | sed 's/^/                           /'
fi
echo "check-phone-home-claims: OK — la cifra no sube."
exit 0
