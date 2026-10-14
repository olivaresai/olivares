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

# --- (24)-(31) THE `--worker` MODE ----------------------------------------
# The production Worker publishes its license key id on `GET /` (`license_key_id`, the
# sha256 of the 32 raw bytes). Before, nothing read-only could tell whether the deployed secret
# was the key the release binaries accept: the first buyer would have been the test.
# `curl` is replaced by a double on PATH, like `gh` above: the battery never talks to the network.
# The two expected ids are an INDEPENDENT ORACLE (python3 hashlib over the 32 bytes of the
# toy anchor), not the SUT's own computation: a SUT that hashed wrongly would agree with itself.
KID_NUEVA_LIC="sha256:843e7deacad76299f462830702246d04ddeafe2d36923f6a53a01998c6fe9114"
KID_VIEJA="sha256:8294d7676cd7c09bdcfd0166dfc862f6163894811c75c73e0b2a319623c7e594"
curl_doble() { # <exit-code> <stdout>; records its argv in $T/curl.args, one invocation per line
	rm -f "$T/curl.args"
	cat >"$T/bin/curl" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>'$T/curl.args'
printf '%s' '$2'
exit $1
EOF
	chmod +x "$T/bin/curl"
}
run_worker() { # [url...]; usa el curl doble ya escrito. WURL=<v> fija OLIVARES_LICENSE_WORKER_URL (vacio incluido)
	[ -n "${WURL+x}" ] && export OLIVARES_LICENSE_WORKER_URL="$WURL"
	env PATH="$T/bin:$PATH" OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="$T/claves" \
		OLIVARES_ANCHOR_TABLE="$T/tabla.md" \
		sh "$SUT" --worker "$@" >"$T/out" 2>&1
	echo $?
}
doc() { # <license_key_id JSON literal>
	printf '{"service":"olivares-license-worker","enabled":true,"provider":"dodo","license_key_id":%s,"endpoints":["GET /health"]}' "$1"
}

# (24) POSITIVE CONTROL: the Worker publishes the reviewed anchor's id. Without it, a gate that
#      always refused would pass (25)-(30).
curl_doble 0 "$(doc "\"$KID_NUEVA_LIC\"")"
check "(24) Worker publishes the reviewed key id -> 0" 0 "$(run_worker https://worker.test)"
grep -q 'OK — the Worker publishes the reviewed LICENSE key id' "$T/out" && d=si || d=no
check "(24) says OK for the Worker, not for both anchors" si "$d"
# The seam the double would otherwise hide: exactly one read, of exactly the Worker's GET /, with
# `-f` (an HTTP error must fail), `-q` (no .curlrc) and a size cap.
check "(24) one request" 1 "$(wc -l <"$T/curl.args" | tr -d ' ')"
grep -qE '(^| )-q ' "$T/curl.args" && d=si || d=no
check "(24) curl runs with -q" si "$d"
grep -qE ' -[a-zA-Z]*f' "$T/curl.args" && d=si || d=no
check "(24) curl runs with -f" si "$d"
grep -q -- '--max-filesize ' "$T/curl.args" && d=si || d=no
check "(24) curl bounds the body size" si "$d"
check "(24) URL is the Worker's GET / exactly" "https://worker.test/" "$(tail -n 1 "$T/curl.args" | awk '{print $NF}')"

# (25) THE DEFECT THIS MODE EXISTS FOR: the deployed secret is ANOTHER, well-formed key. That is
#      what the first buyer would have found: «license does not verify against this deployment».
curl_doble 0 "$(doc "\"$KID_VIEJA\"")"
check "(25) Worker publishes another well-formed key id -> 1" 1 "$(run_worker https://worker.test)"
grep -q 'LICENSE MISMATCH' "$T/out" && d=si || d=no
check "(25) names LICENSE MISMATCH" si "$d"
grep -q "$KID_VIEJA" "$T/out" && grep -q "$KID_NUEVA_LIC" "$T/out" && d=si || d=no
check "(25) prints BOTH ids so an operator can see which side is stale" si "$d"

# (26) `null` means «looked, and the Worker has no usable key»: a finding (1), not «could not look».
curl_doble 0 "$(doc null)"
check "(26) Worker reports license_key_id null -> 1" 1 "$(run_worker https://worker.test)"
grep -q 'NO usable signing key' "$T/out" && d=si || d=no
check "(26) says the Worker has no usable key" si "$d"

# (27) An OLDER deployment does not publish the field: nothing was compared -> 2, never 0 or 1.
curl_doble 0 '{"service":"olivares-license-worker","enabled":true,"provider":"dodo","endpoints":[]}'
check "(27) Worker without the field (older deployment) -> 2" 2 "$(run_worker https://worker.test)"
grep -q 'publishes no well-formed license_key_id' "$T/out" && d=si || d=no
check "(27) names the missing field" si "$d"

# (28) Network/HTTP: curl -f exits non-zero. An unreadable Worker is not a Worker that matches.
curl_doble 22 ''
check "(28) curl fails (HTTP error, DNS) -> 2" 2 "$(run_worker https://worker.test)"
grep -q 'could not read https://worker.test/' "$T/out" && d=si || d=no
check "(28) says it could not read, not that it differs" si "$d"

# (29) The SHAPE closes the class: curl exits 0 and returns a proxy page, or a truncated id.
curl_doble 0 '<html>502 Bad Gateway</html>'
check "(29) HTML body -> 2" 2 "$(run_worker https://worker.test)"
grep -q 'publishes no well-formed license_key_id' "$T/out" && d=si || d=no
check "(29) says nothing was compared" si "$d"
curl_doble 0 "$(doc '"sha256:843e7dea"')"
check "(29) truncated key id -> 2" 2 "$(run_worker https://worker.test)"

# (30) The mode depends on neither the OTA anchor NOR the environment: with no OTA row or
#      ceremony file, and with a stray OLIVARES_LICENSE_PUBKEY exported that must NOT be read,
#      it still gives 0. An unreadable reviewed LICENSE anchor refuses (2) instead of comparing.
curl_doble 0 "$(doc "\"$KID_NUEVA_LIC\"")"
check "(30) license-only table, no OTA anywhere, stray env anchor -> 0" 0 "$(env PATH="$T/bin:$PATH" OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="$T/no-existe" OLIVARES_ANCHOR_TABLE="$T/tabla-parcial.md" OLIVARES_LICENSE_PUBKEY="$VIEJA_OTA" sh "$SUT" --worker https://worker.test >"$T/out" 2>&1; echo $?)"
check "(30) no reviewed LICENSE anchor readable -> 2" 2 "$(env PATH="$T/bin:$PATH" OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="$T/no-existe" OLIVARES_ANCHOR_TABLE="$T/sin-tabla.md" sh "$SUT" --worker https://worker.test >"$T/out" 2>&1; echo $?)"
# (30c) The SHAPE guard on the reviewed anchor: 3 bytes are not a key. Without it the derived id
#       would be that of garbage and the verdict a MISMATCH (1) instead of «could not look» (2).
tabla "$T/tabla-corta.md" "YWJj" "$NUEVA_OTA"
check "(30c) reviewed anchor of 3 bytes -> 2, never MISMATCH" 2 "$(env PATH="$T/bin:$PATH" OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="$T/no-existe" OLIVARES_ANCHOR_TABLE="$T/tabla-corta.md" sh "$SUT" --worker https://worker.test >"$T/out" 2>&1; echo $?)"
grep -q 'MISMATCH' "$T/out" && d=si || d=no
check "(30c) does not call it a mismatch" no "$d"

# (31) Profile: --worker is production only. Under preprod it refuses before touching the network.
curl_doble 0 "$(doc "\"$KID_NUEVA_LIC\"")"
check "(31) --worker under preprod -> 2" 2 "$(env PATH="$T/bin:$PATH" OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="$T/claves" OLIVARES_ANCHOR_TABLE="$T/tabla.md" OLIVARES_RELEASE_PROFILE=preprod sh "$SUT" --worker https://worker.test >"$T/out" 2>&1; echo $?)"
grep -q 'PRODUCTION Worker' "$T/out" && d=si || d=no
check "(31) says why" si "$d"
[ ! -e "$T/curl.args" ] && d=si || d=no
check "(31) refused before touching the network" si "$d"

# (32) THE DOCUMENT IS PARSED, not searched with grep. A top-level null does not hide behind a
#      nested "license_key_id" that matches; another service does not count even if it carries the
#      field; and the id shape is exact (64 lowercase hex).
curl_doble 0 "{\"service\":\"olivares-license-worker\",\"note\":{\"license_key_id\":\"$KID_NUEVA_LIC\"},\"license_key_id\":null}"
check "(32) nested id does not hide a top-level null -> 1" 1 "$(run_worker https://worker.test)"
curl_doble 0 "{\"service\":\"another-service\",\"license_key_id\":\"$KID_NUEVA_LIC\"}"
check "(32) another service's document -> 2" 2 "$(run_worker https://worker.test)"
curl_doble 0 "$(doc "\"$(printf '%s' "$KID_NUEVA_LIC" | tr 'a-f' 'A-F' | sed 's/^SHA256/sha256/')\"")"
check "(32) uppercase hex -> 2" 2 "$(run_worker https://worker.test)"
curl_doble 0 "$(doc "\"$(printf '%s' "$KID_NUEVA_LIC" | cut -c1-70)\"")"
check "(32) 63 hex digits -> 2" 2 "$(run_worker https://worker.test)"
curl_doble 0 "$(doc "\"${KID_NUEVA_LIC}0\"")"
check "(32) 65 hex digits -> 2" 2 "$(run_worker https://worker.test)"
curl_doble 0 "$(doc 7)"
check "(32) a number -> 2" 2 "$(run_worker https://worker.test)"

# (33) THE URL: the one requested is the one read, with or without a trailing slash; the default
#      is production; the environment variable replaces it; and empty refuses instead of production.
curl_doble 0 "$(doc "\"$KID_NUEVA_LIC\"")"
check "(33) trailing slash -> 0" 0 "$(run_worker https://worker.test/)"
check "(33) trailing slash is not doubled" "https://worker.test/" "$(tail -n 1 "$T/curl.args" | awk '{print $NF}')"
check "(33) default is production -> 0" 0 "$(run_worker)"
check "(33) default URL" "https://licenses.olivares.ai/" "$(tail -n 1 "$T/curl.args" | awk '{print $NF}')"
check "(33) env URL -> 0" 0 "$(WURL=https://env.test run_worker)"
check "(33) env URL is the one read" "https://env.test/" "$(tail -n 1 "$T/curl.args" | awk '{print $NF}')"
check "(33) loopback http for a local Worker -> 0" 0 "$(run_worker http://127.0.0.1:8787)"
check "(33) empty URL argument -> 2" 2 "$(run_worker '')"
grep -q 'empty URL' "$T/out" && d=si || d=no
check "(33) names the empty URL" si "$d"
check "(33) empty env URL -> 2, not production" 2 "$(WURL= run_worker)"
rm -f "$T/curl.args"
check "(33) plaintext http to another host -> 2" 2 "$(run_worker http://worker.test)"
grep -q 'is not https://' "$T/out" && d=si || d=no
check "(33) names the scheme rule" si "$d"
check "(33) credentials in the URL -> 2" 2 "$(run_worker https://user:pass@worker.test)"
grep -q 'characters outside' "$T/out" && d=si || d=no
check "(33) names the character rule" si "$d"
check "(33) option-like value -> 2" 2 "$(run_worker -oX)"
grep -q 'is not https://' "$T/out" && d=si || d=no
check "(33) option-like value fails the scheme rule" si "$d"
check "(33) space in the URL -> 2" 2 "$(run_worker 'https://worker.test/ x')"
grep -q 'characters outside' "$T/out" && d=si || d=no
check "(33) space fails the character rule" si "$d"
[ ! -e "$T/curl.args" ] && d=si || d=no
check "(33) every refused URL stopped before the network" si "$d"

# (34) AN UNKNOWN ARGUMENT IS NOT THE DEFAULT MODE. Before, `--wroker` fell into the environment
#      variable mode and could print «OK — both anchors»: the twin of the profile defect.
check "(34) unknown argument -> 2" 2 "$(env PATH="$T/bin:$PATH" OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="$T/claves" OLIVARES_ANCHOR_TABLE="$T/tabla.md" OLIVARES_LICENSE_PUBKEY="$NUEVA_LIC" OLIVARES_OTA_PUBKEY="$NUEVA_OTA" sh "$SUT" --wroker >"$T/out" 2>&1; echo $?)"
grep -q "unknown argument '--wroker'" "$T/out" && d=si || d=no
check "(34) names the argument" si "$d"

# (35) THE INTERPRETER LOADS NO CODE FROM THE CURRENT DIRECTORY. `python3 -c` puts the cwd first
#      on sys.path: a planted json.py would be imported when the document is read and could
#      print the expected id. With `-I` it is not. The planted file ALWAYS returns the reviewed id;
#      the real document carries another, so the correct verdict is 1, and a 0 is the injection.
mkdir -p "$T/plantado"
cat >"$T/plantado/json.py" <<EOF
def load(_f):
    return {"service": "olivares-license-worker", "license_key_id": "$KID_NUEVA_LIC"}
EOF
curl_doble 0 "$(doc "\"$KID_VIEJA\"")"
check "(35) a json.py planted in the cwd is not imported -> 1" 1 "$(cd "$T/plantado" && env PATH="$T/bin:$PATH" OLIVARES_ANCHOR_ROOT="$ROOT" OLIVARES_ANCHOR_DIR="$T/claves" OLIVARES_ANCHOR_TABLE="$T/tabla.md" sh "$SUT" --worker https://worker.test >"$T/out" 2>&1; echo $?)"

# (36) Argumentos de mas no se ignoran en silencio.
check "(36) --worker URL extra -> 2" 2 "$(run_worker https://worker.test extra)"

printf '\ncheck-release-anchor-identity: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
