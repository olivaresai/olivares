#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/check-release-anchor-identity.sh.
#
# The case that justifies the whole gate is (2): the pre-rotation OTA anchor. That exact value
# survives `check-release-pubkey.sh` (it is well-formed) AND survives `release-preflight.sh` in
# production mode (measured 2026-08-28: rc=0, "production profile validated"). If it survived here
# too, this gate would be decoration.
#
# Every "must reject" case is paired with a positive control, because a script that always said
# MISMATCH would pass all of them.
set -u

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
SUT="$ROOT/scripts/check-release-anchor-identity.sh"
# El carril `--live` (12)-(15) sustituye `gh` por un DOBLE que esta bateria escribe y EJECUTA, asi
# que su directorio de trabajo tiene que admitir execve. En esta caja `/tmp` esta montado `noexec`
# (medido 2026-08-31), y con el doble inejecutable el gate no puede mirar: (13) y (14) fallaban
# — y, peor, (12) PASABA por el motivo equivocado, porque esperaba 2 y todo salia 2.
# Eso es justo lo que su control positivo existe para destapar, asi que aqui se elige un directorio
# que SI ejecuta, se prueba con un execve de verdad, y se dice en voz alta cuando hay que mudarse.
_mk() { mktemp -d "$1/anchoridt.XXXXXX" 2>/dev/null; }
_ejecuta() { # <dir> — un execve real, no una comprobacion de permisos
	printf '#!/bin/sh\nexit 7\n' >"$1/.probe" 2>/dev/null || return 1
	chmod +x "$1/.probe" 2>/dev/null || return 1
	"$1/.probe" 2>/dev/null; [ $? -eq 7 ] || return 1
	rm -f "$1/.probe"; return 0
}
T="$(_mk "${TMPDIR:-/tmp}")" || exit 2
if ! _ejecuta "$T"; then
	rm -rf "$T"
	T="$(_mk "$ROOT")" || exit 2
	if ! _ejecuta "$T"; then
		rm -rf "$T"
		echo "test-release-anchor-identity: CANNOT INSPECT: neither ${TMPDIR:-/tmp} nor $ROOT allows execve," >&2
		echo "  the --live path needs to execute the gh double. Export TMPDIR pointing to an executable directory." >&2
		exit 2
	fi
	echo "  (note: ${TMPDIR:-/tmp} is mounted noexec; the gh double runs in $T)"
fi
trap 'rm -rf "$T"' EXIT
pass=0; fail=0
check() { # <nombre> <esperado> <obtenido>
	if [ "$2" = "$3" ]; then pass=$((pass + 1)); printf '  ok   %-58s %s\n' "$1" "$3"
	else fail=$((fail + 1)); printf '  FAIL %-58s expected %s, got %s\n' "$1" "$2" "$3"; fi
}

# Anclas de juguete, no las de producción: la batería no debe depender del árbol real, o dejaría de
# correr el día que las anclas roten — que es justamente el día que más falta hace.
# ⛔ Los tres decodifican a EXACTAMENTE 32 bytes, que es lo que es una clave pública Ed25519.
# Hasta el 2026-08-29 decodificaban a 36 y por tanto NO tenían la forma de un ancla real: pasaban
# esta batería y habrían fallado cualquier comprobación de forma. Se cambiaron al añadir el
# carril --live, cuya guarda de forma los destapó — una fixture que no puede existir en producción
# es una fixture que no prueba producción.
NUEVA_LIC="TmV3TGljZW5zZUFuY2hvci0tMzItYnl0ZXMtLTAwMDE="
NUEVA_OTA="TmV3T3RhQW5jaG9yLS0tLS0tMzItYnl0ZXMtLTAwMDI="
VIEJA_OTA="T2xkT3RhQW5jaG9yLS0tLS0tMzItYnl0ZXMtLTAwMDM="

mkdir -p "$T/claves"
printf '%s\n' "$NUEVA_LIC" >"$T/claves/prod-license.pub"
printf '%s\n' "$NUEVA_OTA" >"$T/claves/prod-ota.pub"

# Una tabla de juguete con la MISMA forma que docs/RELEASE-VERIFICATION.md. La batería nunca lee
# la real: si lo hiciera, cada rotación futura la pondría roja y alguien la desactivaría.
tabla() { # <fichero> <lic> <ota>
	{
		printf '| Release | Domain | Public key (base64-std) | SHA-256 fingerprint | `version` prefix |\n'
		printf '|---|---|---|---|---|\n'
		printf '| 1.0 | license | `%s` | x | x |\n' "$2"
		printf '| 1.0 | OTA | `%s` | x | x |\n' "$3"
	} >"$1"
}
tabla "$T/tabla.md" "$NUEVA_LIC" "$NUEVA_OTA"
: >"$T/sin-tabla.md"

run() { # <lic> <ota> [dir] [tabla]
	env OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="${3:-$T/claves}" \
		OLIVARES_ANCHOR_TABLE="${4:-$T/tabla.md}" \
		OLIVARES_LICENSE_PUBKEY="$1" OLIVARES_OTA_PUBKEY="$2" \
		sh "$SUT" >"$T/out" 2>&1
	echo $?
}

echo "== check-release-anchor-identity =="

# (1) CONTROL POSITIVO. Sin él, un guion que siempre rechazara pasaría toda la batería.
check "(1) both reviewed anchors -> 0" 0 "$(run "$NUEVA_LIC" "$NUEVA_OTA")"

# (2) EL MUTANTE QUE MOTIVA EL GATE: el ancla OTA pre-rotación. Bien formada, y por eso invisible
#     para check-release-pubkey.sh y para el preflight. Aquí TIENE que morir.
check "(2) old OTA anchor (the mutant) -> 1" 1 "$(run "$NUEVA_LIC" "$VIEJA_OTA")"
grep -q 'OTA MISMATCH' "$T/out" && d=si || d=no
check "(2) names WHICH anchor, not a generic error" si "$d"
grep -q 'LICENSE MISMATCH' "$T/out" && d=si || d=no
check "(2) does NOT flag the matching anchor" no "$d"

# (3) La otra mitad del par: un gate que sólo mirara OTA dejaría pasar una licencia comprometida.
check "(3) different LICENSE anchor -> 1" 1 "$(run "$VIEJA_OTA" "$NUEVA_OTA")"
grep -q 'LICENSE MISMATCH' "$T/out" && d=si || d=no
check "(3) names LICENSE" si "$d"

# (4) Vacío es «no he podido mirar» (2), NUNCA «coincide» (0) ni «no coincide» (1).
check "(4) empty OLIVARES_OTA_PUBKEY -> 2" 2 "$(run "$NUEVA_LIC" "")"
check "(4) empty OLIVARES_LICENSE_PUBKEY -> 2" 2 "$(run "" "$NUEVA_OTA")"

# (5) Partial table: only the license row, with no an internal design note (not shipped) The readable half must
# not clear the missing OTA anchor; otherwise checking only the first row returns 0.
{
	printf '| Release | Domain | Public key (base64-std) | SHA-256 fingerprint | `version` prefix |\n'
	printf '|---|---|---|---|---|\n'
	printf '| 1.0 | license | `%s` | x | x |\n' "$NUEVA_LIC"
} >"$T/tabla-parcial.md"
check "(5) table without OTA row and without design/ -> 2" 2 "$(run "$NUEVA_LIC" "$NUEVA_OTA" "$T/no-existe" "$T/tabla-parcial.md")"
grep -q 'no reviewed OTA anchor' "$T/out" && d=si || d=no
check "(5) reports that OTA is the missing anchor" si "$d"

# (6) Un ancla revisada VACÍA haría que todo «coincidiera» si se comparase a ciegas.
mkdir -p "$T/vacio"; : >"$T/vacio/prod-license.pub"; printf '%s\n' "$NUEVA_OTA" >"$T/vacio/prod-ota.pub"
check "(6) empty reviewed file -> 2, not 0" 2 "$(run "$NUEVA_LIC" "$NUEVA_OTA" "$T/vacio")"

# (7) El fichero del árbol acaba en \n y la variable no: si el gate no normalizara, el control
#     positivo (1) sería un falso rojo permanente y alguien lo desactivaría por ruidoso.
mkdir -p "$T/crlf"; printf '%s\r\n' "$NUEVA_LIC" >"$T/crlf/prod-license.pub"; printf '  %s  \n' "$NUEVA_OTA" >"$T/crlf/prod-ota.pub"
check "(7) surrounding spaces/CRLF do not create a MISMATCH" 0 "$(run "$NUEVA_LIC" "$NUEVA_OTA" "$T/crlf")"

# --- las DOS casas del ancla revisada -------------------------------------------------------
# (8) Sólo la tabla publicada: es el caso del árbol EXPORTADO, donde design/ no existe. Sin esto el
#     gate no podría vivir dentro de release-preflight.sh, que corre desde el checkout público.
check "(8) table alone (without design/) -> 0" 0 "$(run "$NUEVA_LIC" "$NUEVA_OTA" "$T/no-existe")"
check "(8) table alone still detects the mutant -> 1" 1 "$(run "$NUEVA_LIC" "$VIEJA_OTA" "$T/no-existe")"

# (9) Las dos casas DISCREPAN. No se elige ganadora en silencio: la discrepancia ES el defecto.
tabla "$T/tabla-vieja.md" "$NUEVA_LIC" "$VIEJA_OTA"
check "(9) ceremony and table disagree -> 1" 1 "$(run "$NUEVA_LIC" "$NUEVA_OTA" "$T/claves" "$T/tabla-vieja.md")"
grep -q 'disagree' "$T/out" && d=si || d=no
check "(9) calls it disagreement, not MISMATCH" si "$d"

# (10) Ninguna casa legible: 2. «No he podido mirar» nunca es «coincide».
check "(10) neither design/ nor table -> 2" 2 "$(run "$NUEVA_LIC" "$NUEVA_OTA" "$T/no-existe" "$T/sin-tabla.md")"

# (11) CONTROL POSITIVO de la tabla: la fila que NO es la suya no debe contaminar. Si el lector
#      cogiera la primera celda en vez de la del dominio, license y OTA saldrían iguales.
check "(11) does not confuse the license row with OTA -> 1" 1 "$(run "$NUEVA_OTA" "$NUEVA_OTA" "$T/no-existe")"

# --- (12)-(15) EL CARRIL `--live`, QUE NO TENÍA NI UN CASO -----------------------------------
# Añadidos el 2026-08-29 después de medir el defecto en vivo: con un PAT que no puede
# leer las variables de `olivaresai/olivares`, el gate contestaba `⛔ LICENSE MISMATCH … in effect
# e3b0c44298fc1c14` y salía **1**. Ese digest es el del string VACÍO: `gh api` escribe el cuerpo
# de error JSON en STDOUT, la tubería `| trim` se comía su código de salida, el valor llegaba
# NO VACÍO (así que la guarda de vacío no disparaba) y `fp()` lo base64-decodificaba a nada.
# ⇒ la tercera respuesta reportada como la segunda, en la entrada C3 del acto público.
#
# `gh` se sustituye por un doble en el PATH: la batería NO habla con GitHub — un caso que dependa
# de la red mide la red, y además caducaría el día que las variables reales cambien.
mkdir -p "$T/bin"
gh_doble() { # <exit-code> <stdout-para-LICENSE> <stdout-para-OTA>
	cat >"$T/bin/gh" <<EOF
#!/bin/sh
case "\$*" in
*OLIVARES_LICENSE_PUBKEY*) printf '%s' '$2' ;;
*OLIVARES_OTA_PUBKEY*)     printf '%s' '$3' ;;
esac
exit $1
EOF
	chmod +x "$T/bin/gh"
}
run_live() { # usa el gh doble ya escrito
	env PATH="$T/bin:$PATH" OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="$T/claves" \
		OLIVARES_ANCHOR_TABLE="$T/tabla.md" \
		sh "$SUT" --live >"$T/out" 2>&1
	echo $?
}

# (12) EL DEFECTO MEDIDO: gh falla y escribe su cuerpo de error en stdout. Tiene que ser 2.
ERR403='{"message":"Resource not accessible by personal access token","status":"403"}'
gh_doble 1 "$ERR403" "$ERR403"
check "(12) --live gh fails with JSON body on stdout -> 2" 2 "$(run_live)"
grep -q 'e3b0c442' "$T/out" && d=si || d=no
check "(12b) does NOT print the empty digest as 'in effect'" no "$d"

# (13) CONTROL POSITIVO del carril --live. Sin él, (12) lo pasaría un gate que siempre diga 2.
gh_doble 0 "$NUEVA_LIC" "$NUEVA_OTA"
check "(13) --live with reviewed anchors -> 0" 0 "$(run_live)"

# (14) Y la ruta de MISMATCH sigue viva bajo --live: dos anclas bien formadas que difieren -> 1.
gh_doble 0 "$NUEVA_LIC" "$VIEJA_OTA"
check "(14) --live with pre-rotation OTA -> 1" 1 "$(run_live)"

# (15) La guarda de FORMA cierra la clase, no sólo el 403: gh sale 0 y devuelve algo que no es una
#      clave (una página del proxy, un valor truncado). No es un desajuste: es no haber mirado.
gh_doble 0 '<html>502 Bad Gateway</html>' '<html>502 Bad Gateway</html>'
check "(15) --live gh exits 0 with non-base64 garbage -> 2" 2 "$(run_live)"

# ⛔ RENUMERADOS 12-15 -> 20-23 al rebasar: `main` estreno (12)-(15)+(12b) para el carril
#    `--live` mientras yo usaba esos numeros para el PERFIL. Los dos juegos son buenos y
#    ninguno sustituye al otro; solo chocaba la etiqueta. Salto a 20 y no a 16 porque mis
#    propios commits posteriores estrenan 16-18: renumerar a 16 solo mueve la colision.
# ---------------------------------------------------------------------------------------------
# (12-15) CONCIENCIA DE PERFIL (2026-08-30). Hasta este cambio el guion comparaba TODO
# contra `prod-*.pub`, asi que el perfil no existia para el. Estos casos fijan las dos
# direcciones, y la segunda es la que de verdad importa.
SBOX_LIC="U2FuZGJveExpY0FuY2hvcjAxMjM0NTY3ODlBQkNERUZHSEk9"
SBOX_OTA="U2FuZGJveE90YUFuY2hvcjAxMjM0NTY3ODlBQkNERUZHSEk9"
printf '%s\n' "$SBOX_LIC" >"$T/claves/sandbox-license.pub"
printf '%s\n' "$SBOX_OTA" >"$T/claves/sandbox-ota.pub"

runp() { # <perfil> <lic> <ota> [dir] [tabla]
	env OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="${4:-$T/claves}" \
		OLIVARES_ANCHOR_TABLE="${5:-$T/tabla.md}" OLIVARES_RELEASE_PROFILE="$1" \
		OLIVARES_LICENSE_PUBKEY="$2" OLIVARES_OTA_PUBKEY="$3" \
		sh "$SUT" >"$T/out" 2>&1
	echo $?
}

# (20) Before 2026-08-31 this case expected 0, a false success. Matching
# an internal design note (not shipped) proves only the O03 record (11a7693c),
# not the deployed sandbox Worker's signing key (0e73e1a0). The live measurement
# is documented in an internal design note (not shipped) Retain the reason
# here so a later edit does not restore that invalid expectation.
# The positive discrimination still lives in (21), requiring 1 rather than 2 for
# production anchors under preprod, and (22), requiring 0 under production.
# A blanket preprod refusal fails (21); a blanket refusal for all profiles fails (22).
check "(20) preprod against the O03 record is not approved -> 2" 2 "$(runp preprod "$SBOX_LIC" "$SBOX_OTA")"
grep -q "0e73e1a0" "$T/out" && d=si || d=no
check "(20) names WHICH key actually signs, not a generic refusal" si "$d"
grep -q "LEEME-SANDBOX-LICENSE" "$T/out" && d=si || d=no
check "(20) refers to the measurement, not this gate" si "$d"

# (21) ⭐ EL FALSO VERDE QUE ESTE CAMBIO CIERRA, y es el motivo de todo esto. Antes, un release de
#      preprod que embarcara por error las anclas de PROD casaba contra `prod-*.pub` y se
#      declaraba OK: rc=0. Un pipeline de preprod firmando con la clave de produccion es
#      exactamente lo que un gate de anclas existe para impedir.
check "(21) preprod shipping PROD anchors -> 1" 1 "$(runp preprod "$NUEVA_LIC" "$NUEVA_OTA")"

# (22) Y la simetrica: prod sigue juzgandose contra prod, no contra sandbox.
check "(22) prod with prod anchors -> 0" 0 "$(runp production "$NUEVA_LIC" "$NUEVA_OTA")"
check "(22) prod shipping sandbox anchors -> 1" 1 "$(runp production "$SBOX_LIC" "$SBOX_OTA")"

# (23) La tabla publicada NO tiene columna de perfil: sus filas son de PROD. Bajo preprod no se
#      consulta, asi que sin registro de ceremonia se rehusa con 2 en vez de comparar contra
#      filas que no son suyas. «No he podido mirar» nunca es «coincide».
check "(23) preprod without ceremony does not fall back to the prod table -> 2" 2 "$(runp preprod "$SBOX_LIC" "$SBOX_OTA" "$T/no-existe")"

# (16) DENY-CLOSED del perfil, las dos formas de romperlo. Un perfil definido-pero-VACIO es una
#      mala configuracion de CI (`vars.` sin definir, un typo en la expresion), no una peticion de
#      production: con la forma `${VAR:-default}` se colaria comprobando las anclas de PROD para
#      una build que pidio otra cosa. Y un perfil desconocido tampoco elige lado por defecto.
check "(16) empty profile -> 2" 2 "$(runp '' "$SBOX_LIC" "$SBOX_OTA")"
check "(16) unknown profile -> 2" 2 "$(runp staging "$SBOX_LIC" "$SBOX_OTA")"
grep -q 'unknown' "$T/out" && d=si || d=no
check "(16) names the profile rather than a generic error" si "$d"

# (17) COHERENCIA PERFIL/REPO en --live. Solo se prueba el caso que CORTA: devuelve antes de
#      tocar la red, asi que la bateria sigue siendo offline. El caso que pasa haria una llamada
#      a la API y no tiene sitio en el carril rapido.
rc_live=$(env OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="$T/claves" \
	OLIVARES_ANCHOR_TABLE="$T/tabla.md" OLIVARES_RELEASE_PROFILE=preprod \
	OLIVARES_LICENSE_PUBKEY="$SBOX_LIC" OLIVARES_OTA_PUBKEY="$SBOX_OTA" \
	sh "$SUT" --live >"$T/out" 2>&1; echo $?)
check "(17) --live preprod against the prod repository -> 2" 2 "$rc_live"
grep -q 'production repository' "$T/out" && d=si || d=no
check "(17) reports that environments do not match" si "$d"

# (18) PARIDAD DE PERFILES CON EL HERMANO. `ent:check-release-anchor-domains.sh` acepta
#      exactamente `production` y `preprod`. Si este control aceptara ademas un alias como
#      `prod`, un release con ese valor pasaria la identidad y moriria en dominios: dos gates
#      hermanos discrepando sobre que perfiles existen. Lo escribi como intencion en un
#      comentario y NO lo habia implementado; este caso lo ata.
check "(18) alias 'prod' does NOT exist, consistent with the sibling -> 2" 2 "$(runp prod "$NUEVA_LIC" "$NUEVA_OTA")"

printf '\ncheck-release-anchor-identity: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
