#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-phone-home-claims.sh — batería de `scripts/check-phone-home-claims.sh` (patrón lint:boundary).
#
# ⛔ POR QUÉ EXISTE, y las tres cosas que prueba son las tres que fallaron de verdad:
#
#  1 · **El alcance del patrón era el INGLÉS.** El 2026-08-28 la línea base traía la página EN de
#      air-gap con 2 promesas y sus SEIS traducciones —con la misma promesa en su idioma— daban 0.
#      Un patrón multilingüe sin control positivo POR IDIOMA es una conjetura, así que cada
#      alternativa que se añade al patrón trae aquí su caso: si el idioma no enrojece, el caso falla.
#  2 · **Una cuenta que BAJA se anunciaba como «PROMESA NUEVA»** y devolvía rc 1, es decir el
#      trinquete rechazaba exactamente el trabajo que dice esperar. El caso 'la retirada NO es una
#      promesa nueva' fija la dirección de no-disparo.
#  3 · **La exclusión de los tests estaba anclada con `$`** sobre líneas `ruta:cuenta`, así que era
#      INERTE: el gate contaba el propio test que asegura que la promesa NO está. Se fija aquí.
#
# Salida: 0 todos los casos pasan · 1 algún caso falla.
set -uo pipefail
ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"
CHECK="$ROOT/scripts/check-phone-home-claims.sh"
[ -r "$CHECK" ] || { echo "test-phone-home-claims: ⛔ no encuentro $CHECK" >&2; exit 2; }

_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base" || exit 2
TMP="$(mktemp -d "$_tmp_base/phonehome.XXXXXX")" || exit 2
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()  { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

T="$TMP/tree"

# Un árbol mínimo con TODAS las superficies que el gate exige, y una página viva por cada una.
stage() {
	rm -rf "$T"
	mkdir -p "$T/docs-site/src/content/docs/how-to" \
	         "$T/docs-site/src/content/docs/2026-06" \
	         "$T/docs/trust" \
	         "$T/docs/internals" \
	         "$T/web/src/features" \
	         "$T/email/copy" \
	         "$T/commercial/license-worker/src/portal/pages" \
	         "$T/commercial/license-worker/src/email" \
	         "$T/docs"
	# Texto CORRECTO: la redacción firmada. Ninguna de estas líneas debe casar.
	cat > "$T/docs-site/src/content/docs/how-to/air-gap-install.md" <<'PAGE'
# Air-gap install
Verifying a licence never calls anyone. Downloading what you paid for does.
The engine does not phone home. El motor no llama a casa.
PAGE
	cat > "$T/docs/trust/one-pager.md" <<'PAGE'
| License validation | Offline Ed25519 attestation, verified against the embedded key. |
PAGE
	printf 'export const panel = "no mandatory outbound calls at boot"\n' \
		> "$T/web/src/features/panel.ts"
	printf '{"license":{"attestation":"Verifying it never calls us; downloading does."}}\n' \
		> "$T/email/copy/en.json"
	printf 'const p = "Verifying it never calls us."\n' \
		> "$T/commercial/license-worker/src/portal/pages/licenses.ts"
	# `docs/` entero se vigila desde el 2026-08-28 (F7 del contraste): una página bajo `docs/` que
	# NO sea `docs/trust` tiene que contar. El árbol de prueba no la tenía, y por eso reproducía
	# exactamente la omisión que el gate acababa de cerrar.
	printf 'License validation is offline Ed25519, verified against the embedded key.\n' \
		> "$T/docs/internals/architecture.md"
	printf 'The engine makes no mandatory outbound calls at boot.\n' > "$T/INSTALL.md"
	printf 'There is no mandatory telemetry and no control-plane egress by default.\n' > "$T/README.md"
	printf 'Verifying a licence never calls anyone. Downloading what you paid for does.\n' > "$T/LICENSING.md"
	# ⛔ SUPPORTERS.md ENTRO EN LA LISTA VIGILADA DEL GATE EL 2026-08-29 (`99ad07559`) Y ESTA
	#    BATERIA NO SE ACTUALIZO CON EL. Resultado: **29 de 33 casos en rojo sobre `main` LIMPIO**
	#    —todos con `NO HE PODIDO MIRAR: no existe el fichero vigilado SUPPORTERS.md`— porque el
	#    arbol de fixture sembraba TRES de los CUATRO ficheros que el gate mira. El gate tenia
	#    razon en las 29: no podia mirar.
	#
	#    Entro en una fusion que se verifico contando las patas del gancho (170/176 -> 177,
	#    ninguna perdida) pero SIN ejecutar `lint:addon-sets-gate` sobre el arbol resultante.
	#    La regla que lo habria evitado ya estaba escrita: **un gate que DESCUBRE se re-corre al
	#    ANADIR un fichero**, no solo al editarlo. Contar patas dice que ninguna desaparecio; no
	#    dice que las que quedan pasen.
	#
	#    Esta linea es la mitad que faltaba. La otra mitad —que la lista siga siendo TECLEADA en
	#    los dos ficheros— sigue abierta y el propio gate la declara: cerrarlo pide un glob.
	printf 'Sponsorship funds the work. Mirroring the registry keeps installs self-contained.\n' > "$T/SUPPORTERS.md"
	# La línea base de partida: las dos rutas vigiladas, ambas a cero.
	{
		printf '0\tdocs-site/src/content/docs/how-to/air-gap-install.md\n'
		printf '0\tdocs/trust/one-pager.md\n'
	} > "$T/docs/phone-home-claims-baseline.txt"
}

run() {
	local rc=0
	OLIVARES_CLONE="$T" bash "$CHECK" >"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" > "$TMP/rc"
}
rc() { cat "$TMP/rc"; }
# ⛔ SIN TUBERÍA, y no es estilo. `out | grep -q X` bajo `pipefail` devuelve **141 CUANDO
# ENCUENTRA**: grep sale al primer casamiento, cierra su extremo, el productor recibe SIGPIPE y
# pipefail propaga ese 141 — la comprobación falla justo cuando acierta, y de forma intermitente.
# Lo cazó `lint:sigpipe-booleans` sobre este mismo fichero (6 tuberías) en su primera pasada.
saw() { grep -q -- "$1" "$TMP/out" "$TMP/err"; }
peek() { head -"${1:-4}" "$TMP/out" "$TMP/err"; }

# ── 0 · el árbol limpio pasa ─────────────────────────────────────────────────────────────────
stage; run
if [ "$(rc)" = 0 ]; then ok "árbol con la redacción firmada: rc 0"
else bad "árbol limpio dio rc=$(rc): $(peek 6)"; fi

# ── 1..7 · CONTROL POSITIVO POR IDIOMA ───────────────────────────────────────────────────────
# Cada alternativa del patrón que cubre un locale publicado tiene que poder enrojecer. Se inyecta
# la forma ABSOLUTA tal y como estaba escrita en el árbol antes de C09-05, en la página viva.
idioma_case() {
	local nombre="$1" frase="$2"
	stage
	printf '%s\n' "$frase" >> "$T/docs-site/src/content/docs/how-to/air-gap-install.md"
	run
	if [ "$(rc)" = 1 ] && saw 'air-gap-install.md'; then
		ok "positivo $nombre: «$frase» enrojece y nombra la página"
	else
		bad "positivo $nombre: «$frase» dio rc=$(rc) — $(peek 3)"
	fi
}
idioma_case en-zero  'Mirror them into a private registry and install — with zero phone-home.'
idioma_case en-never 'The licence never phones home, and validates fully offline.'
idioma_case en-nothing 'Nothing phones home; everything is self-hosted.'
idioma_case en-back  'There is no telemetry-home — nothing phones back to Olivares AI.'
idioma_case de       'Registry spiegeln und installieren — mit null Phone-Home.'
idioma_case es       'Refleja en un registro privado e instala — con cero phone-home.'
idioma_case fr       'Mettez-les en miroir dans un registre privé — sans aucun phone-home.'
idioma_case ja       'フォンホームゼロ'
idioma_case ru       'Никаких обращений «домой»'
idioma_case zh       '零外呼'
# ⛔⛔ LAS SIETE FORMAS QUE EL LOTE RETIRÓ Y EL PATRÓN NO VEÍA — hallazgo ALTO del contraste
# `sol max` (F1). Cada cadena de abajo es el LITERAL de `origin/main` que borró: si una de
# ellas volviera al árbol, hasta el 2026-08-28 no habría subido ninguna cuenta. La batería anterior
# no las mataba porque probaba las formas que yo tenía delante («zero phone-home»), no las que
# estaba retirando — que es la distancia exacta entre un control y una conjetura.
idioma_case en-callbacks 'Zero telemetry, zero callbacks. Air-gapped deployments are first-class.'
idioma_case en-contacts  'The product never contacts Olivares AI.'
idioma_case de-kontakt   'Sie nimmt niemals Kontakt zu externen Servern auf und lässt sich offline validieren.'
idioma_case es-comunica  'Nunca se comunica con el proveedor y se valida por completo sin conexión.'
idioma_case fr-serveur   'Elle ne contacte jamais de serveur externe et se valide entièrement hors ligne.'
idioma_case ja-servidor  '外部サーバーへの通信は一切行わず、完全にオフラインで検証できます。'
idioma_case ru-servidor  'Она никогда не связывается с внешними серверами и полностью проверяется без сети.'
idioma_case zh-回传      '绝不联网回传信息，并可完全离线验证。'

# ── 7-bis · el CANARIO: si el patrón deja de reconocer una promesa conocida, la respuesta es 2 ─
# Hallazgo F4 del contraste: el brazo «cero coincidencias + base poblada» miraba `N`, que cuenta
# FILAS —incluidas las `0<TAB>ruta`—, así que con una base poblada por ficheros existentes NUNCA
# podía disparar. El control real no puede depender de que el árbol esté sucio: el patrón se
# ejerce contra promesas conocidas ANTES de juzgar nada. Este caso lo prueba rompiéndolo.
stage
_gutted="$TMP/gutted.sh"
sed "s/|零外呼|不会回拨/|__ALTERNATIVA_RETIRADA__/" "$CHECK" > "$_gutted"
if grep -q '__ALTERNATIVA_RETIRADA__' "$_gutted"; then
	_grc=0
	OLIVARES_CLONE="$T" bash "$_gutted" >"$TMP/out" 2>"$TMP/err" || _grc=$?
	if [ "$_grc" = 2 ] && grep -q '零外呼' "$TMP/err"; then
		ok "canario: romper una alternativa del patrón da rc 2 y nombra la promesa que dejó de ver"
	else
		bad "canario: patrón roto dio rc=$_grc — $(head -4 "$TMP/err")"
	fi
else
	bad "el mutante del canario NO se inyectó — el caso no dice nada del gate"
fi

# ── 7-ter · una promesa bajo `docs/` que NO es `docs/trust` cuenta (F7) ───────────────────────
stage
printf '\nInstall it with zero phone-home, always.\n' >> "$T/docs/internals/architecture.md"
run
if [ "$(rc)" = 1 ] && saw 'docs/internals/architecture.md'; then
	ok "una promesa bajo docs/ fuera de docs/trust cuenta y se nombra"
else bad "docs/ fuera de trust dio rc=$(rc): $(peek 4)"; fi

# ── 7-quater · `docs/contracts/` NO cuenta: el export lo bloquea al por mayor y sus ficheros
# llevan el número de sesión en el nombre. Vigilarlo metía ese token en la línea base, que SÍ se
# publica, y `lint:export` cortó un push a los 26 minutos.
stage
mkdir -p "$T/docs/contracts"
# El nombre del fixture es NEUTRO a propósito: los ficheros reales de `docs/contracts/` llevan
# el número de sesión, y escribirlo aquí lo mete en una CADENA — que el escrubador del export no
# toca (sólo scrubea comentarios). Medido: la primera versión de este caso volvió a cortar
# `lint:export`, esta vez por su propio fixture.
printf 'Install with zero phone-home, always.\n' > "$T/docs/contracts/algun-contrato.md"
run
if [ "$(rc)" = 0 ]; then ok "docs/contracts/ (bloqueado por el export) no cuenta"
else bad "docs/contracts/ contó (rc=$(rc)): $(peek 4)"; fi

# ── 8 · una promesa en un fichero NUEVO (fuera de la base) también enrojece ───────────────────
stage
printf 'Zero phone-home, ever.\n' > "$T/docs/trust/vendor-viability.md"
run
if [ "$(rc)" = 1 ] && saw 'vendor-viability.md'; then ok "fichero nuevo con promesa: rc 1"
else bad "fichero nuevo dio rc=$(rc): $(peek 3)"; fi

# ── 9 · una ruta de la base cuya cuenta SUBE enrojece (la mutación del 2026-08-21) ────────────
stage
printf '0\tdocs-site/src/content/docs/how-to/air-gap-install.md\n0\tdocs/trust/one-pager.md\n' \
	> "$T/docs/phone-home-claims-baseline.txt"
printf 'And there is zero phone-home in the community edition, ever.\n' >> "$T/docs/trust/one-pager.md"
run
if [ "$(rc)" = 1 ] && saw 'one-pager.md: 0 → 1'; then ok "cuenta que SUBE dentro de la base: rc 1 con antes→después"
else bad "subida dio rc=$(rc): $(peek 4)"; fi

# ── 10 · una ruta cuya cuenta BAJA **NO** es una promesa nueva (el defecto de) ──────────
stage
printf '2\tdocs-site/src/content/docs/how-to/air-gap-install.md\n1\tdocs/trust/one-pager.md\n' \
	> "$T/docs/phone-home-claims-baseline.txt"
run
if [ "$(rc)" = 0 ] && saw 'air-gap-install.md: 2 → 0'; then
	ok "retirada (2→0, 1→0): rc 0 y la nombra como BAJA, no como promesa nueva"
else bad "retirada dio rc=$(rc): $(peek 6)"; fi

# ── 11 · NO-DISPARO: la afirmación ACOTADA y cierta no cuenta ─────────────────────────────────
# Las cinco líneas de abajo son TEXTO VIVO de `origin/main` después de C09-05, no invenciones: si
# una de ellas enrojece, el gate está persiguiendo la redacción correcta. Cada una costó una poda
# del patrón el 2026-08-28, y las tres últimas las encontró un gate corriendo, no una lectura.
stage
{
	printf 'The engine does not phone home. There is no telemetry-to-vendor channel.\n'
	printf 'El motor no llama a casa: no se envía nada como efecto de ejecutar.\n'
	printf 'Le moteur ne « rappelle pas à la maison ».\n'
	printf 'Движок не звонит домой: канала телеметрии-к-вендору нет.\n'
	printf '## Внутри изоляции ничто не обращается наружу\n'
	printf '升级命令会回拨。\n'
	printf 'Die Prüfung der Lizenz nimmt keinen Kontakt zu uns auf.\n'
	printf 'ライセンス検証で当社へ通信することはありません。\n'
} >> "$T/docs-site/src/content/docs/how-to/air-gap-install.md"
run
if [ "$(rc)" = 0 ]; then ok "no-disparo: la redacción ACOTADA de C09-05 (en/es/fr/ru) no cuenta"
else bad "la forma ACOTADA enrojeció (rc=$(rc)): $(peek 4)"; fi

# ── 12 · el archivo CONGELADO no cuenta ──────────────────────────────────────────────────────
stage
printf 'Install with zero phone-home.\n' > "$T/docs-site/src/content/docs/2026-06/old.md"
run
if [ "$(rc)" = 0 ]; then ok "docs/2026-06/ (archivo congelado) no cuenta"
else bad "el archivo congelado contó (rc=$(rc)): $(peek 3)"; fi

# ── 13 · un TEST que asegura la AUSENCIA de la promesa no convierte al guardián en infractor ──
stage
printf "expect(screen.queryByText(/nothing phones home/i)).toBeNull()\n" \
	> "$T/web/src/features/attestation.test.tsx"
run
if [ "$(rc)" = 0 ]; then ok "un .test.tsx que cita la promesa retirada no cuenta"
else bad "el fichero de test contó (rc=$(rc)): $(peek 3)"; fi

# ── 14 · el bundle de correo GENERADO no cuenta (lo cubre lint:email-brand) ───────────────────
stage
printf 'export const T = { license: { text: "never phones home" } }\n' \
	> "$T/commercial/license-worker/src/email/templates.generated.ts"
run
if [ "$(rc)" = 0 ]; then ok "templates.generated.ts (artefacto) no cuenta"
else bad "el artefacto generado contó (rc=$(rc)): $(peek 3)"; fi

# ── 15 · sin línea base ⇒ NO HE PODIDO MIRAR ─────────────────────────────────────────────────
stage
rm -f "$T/docs/phone-home-claims-baseline.txt"
printf 'Zero phone-home.\n' >> "$T/docs/trust/one-pager.md"
run
if [ "$(rc)" = 2 ]; then ok "línea base ausente: rc 2 (no «cero promesas»)"
else bad "sin base dio rc=$(rc): $(peek 3)"; fi

# ── 16 · superficie ausente ⇒ NO HE PODIDO MIRAR ─────────────────────────────────────────────
stage
rm -rf "$T/email/copy"
run
if [ "$(rc)" = 2 ] && saw 'email/copy'; then ok "superficie ausente: rc 2 y la nombra"
else bad "superficie ausente dio rc=$(rc): $(peek 3)"; fi

# ── 17 · fichero de raíz vigilado ausente ⇒ NO HE PODIDO MIRAR ────────────────────────────────
stage
rm -f "$T/INSTALL.md"
run
if [ "$(rc)" = 2 ] && saw 'INSTALL.md'; then ok "fichero de raíz ausente: rc 2 y lo nombra"
else bad "fichero ausente dio rc=$(rc): $(peek 3)"; fi

# ── 18 · patrón CIEGO (cero coincidencias con base poblada e inexistente) ⇒ rc 2 ──────────────
stage
printf '3\tdocs-site/src/content/docs/how-to/una-pagina-que-no-existe.md\n' \
	> "$T/docs/phone-home-claims-baseline.txt"
run
if [ "$(rc)" = 2 ]; then ok "cero coincidencias con base poblada: rc 2 (patrón caducado)"
else bad "el conjunto vacío se aprobó (rc=$(rc)): $(peek 4)"; fi

printf 'check-phone-home-claims selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
