#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/verify-business-chain.sh — and it is a MUTATION battery, because a
# chain-walker that only ever sees a healthy chain proves nothing about what it would do with
# a broken one. Each case replaces the CLIENT with a faithful fake, breaks exactly one thing
# in it, and requires the walker to go red NAMING THAT LEG. A mutant that survives means the
# corresponding assertion is decorative.
#
# HERMETIC: no network, no real binary, no deployment. The fake client speaks the four
# sub-commands the walker uses (`version`, `release manifest`, `upgrade --check`,
# `upgrade --enterprise --check`) against the walker's own local channel, so the file
# manipulation being tested (tamper the manifest, take the .sig away, swap the key) is real
# even though the cryptography is modelled.
#
# WHY A FAKE AND NOT THE REAL BINARY: the real one is exercised by the walker itself in every
# live run and by hand in the census; here the subject under test is the WALKER'S JUDGEMENT,
# and a real binary cannot be made to lie on demand. The fake can — that is the whole point.
#
# Exit: 0 all cases behaved · 1 a mutant survived (or a control failed) · 2 could not look.
set -u -o pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
SUT="$ROOT/scripts/verify-business-chain.sh"
[ -f "$SUT" ] || { echo "test-verify-business-chain: NO HE PODIDO MIRAR: no encuentro $SUT" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "test-verify-business-chain: NO HE PODIDO MIRAR: falta python3" >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "test-verify-business-chain: NO HE PODIDO MIRAR: falta curl" >&2; exit 2; }

T="$(mktemp -d "${TMPDIR:-/tmp}/tvbc.XXXXXX")" || exit 2
[ -d "$T" ] || exit 2
trap 'rm -rf "$T"' EXIT

fails=0
cases=0

# ── The fake client ──────────────────────────────────────────────────────────────────────
# It models the contract the walker depends on. OLIVARES_FAKE_MUTANT names the single lie.
cat > "$T/fake-olivares" <<'FAKE'
#!/usr/bin/env bash
set -u -o pipefail
MUT="${OLIVARES_FAKE_MUTANT:-none}"
sub="${1:-}"; shift || true

if [ "$sub" = "version" ]; then
	echo "olivares dev (commit none, built unknown, linux/amd64)"
	exit 0
fi

if [ "$sub" = "release" ] && [ "${1:-}" = "manifest" ]; then
	shift
	out=""; ver="26.8.0"
	while [ $# -gt 0 ]; do
		case "$1" in
		--out) out="$2"; shift 2 ;;
		--version) ver="$2"; shift 2 ;;
		*) shift ;;
		esac
	done
	[ -n "$out" ] || { echo "fake: no --out" >&2; exit 1; }
	mkdir -p "$(dirname "$out")"
	printf '{"channel":"stable","version":"%s"}\n' "$ver" > "$out"
	# The "signature" is the digest of the bytes it signs, which is enough to model the only
	# property the walker asserts: it stops matching when the manifest changes.
	sha256sum < "$out" | cut -d' ' -f1 > "$out.sig"
	echo "wrote $out"
	exit 0
fi

# `license install` es parte del contrato que el walker usa en la pata enterprise, así que el falso
# lo habla: sin esto, el caso de transporte moría en la instalación y no llegaba a medir lo que
# quiere medir. Acepta cualquier fichero legible; lo que ese caso ejercita es el TRANSPORTE.
if [ "$sub" = "license" ] && [ "${1:-}" = "install" ]; then
	# ⛔ Y DEJA CONSTANCIA EN SU `--data-dir`, que es lo que le faltaba: sin eso el falso decía
	# «no license installed» AUNQUE se acabara de instalar, así que la mitad positiva de la pata
	# enterprise no podía salir verde con este banco y la batería no podía ejercerla. El cliente de
	# verdad guarda la licencia ahí; el falso guarda una marca, que es lo que el walker observa.
	fdd=""
	while [ $# -gt 0 ]; do
		case "$1" in --data-dir) fdd="$2"; shift 2 ;; *) shift ;; esac
	done
	[ -n "$fdd" ] && { mkdir -p "$fdd" && : > "$fdd/installed.marker"; }
	echo '{"status":"valid"}'
	exit 0
fi

if [ "$sub" = "upgrade" ]; then
	ent=0; base=""; pubkey=""; ddir=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--enterprise) ent=1; shift ;;
		--endpoint) base="$2"; shift 2 ;;
		--pubkey) pubkey="$2"; shift 2 ;;
		--data-dir) ddir="$2"; shift 2 ;;
		--check|--current-version|--token) [ "$1" = "--check" ] && shift || shift 2 ;;
		*) shift ;;
		esac
	done
	if [ "$ent" -eq 1 ]; then
		if [ "$MUT" = "enterprise-open" ]; then
			echo "check OK: manifest verifies"; exit 0
		fi
		# El falso sabe simular un endpoint caído, porque el walker tiene que distinguir «no pude
		# mirar» de «el canal está roto» también en esta pata. Se pide por sentinela y no por puerto:
		# la lección del mutante live.
		if [ -n "${OLIVARES_FAKE_DEAD_ENDPOINT:-}" ] && [ "${OLIVARES_FAKE_DEAD_ENDPOINT}" = "$base" ]; then
			echo "Error: fetch manifest: dial tcp 127.0.0.1:1: connect: connection refused" >&2
			exit 1
		fi
		if [ -z "$ddir" ] || [ ! -f "$ddir/installed.marker" ]; then
			echo "Error: no license installed: run \`olivares license install <file>\` first" >&2
			exit 1
		fi
		# Con licencia instalada el falso hace lo que hace el cliente: pedir el manifiesto del canal
		# a su endpoint y verificar su firma. Cae al camino común de abajo a propósito, para que la
		# pata enterprise se mida con el MISMO código que la community y no con una rama piadosa.
	fi
	# El mutante LIVE contesta ANTES de mirar nada: un cliente roto en producción acepta sin
	# comprobar, y si se ejecutara después del `curl` moriría por el transporte en vez de por su
	# propia permisividad — que es justo el defecto que la pata `channel-live` tiene que cazar.
	# ⛔ SENTINELA EXPLÍCITO, NO UN RANGO DE PUERTOS. La primera versión discriminaba con
	# `:8[0-9][0-9][0-9][0-9]*/` creyendo reconocer «el puerto alto del banco hermético», y ese patrón
	# sólo casa 80000-89999: puertos imposibles en TCP. O sea que el mutante también aceptaba en el
	# banco, y la batería no lo veía porque sólo lo ejercía con `--only channel-live`. Lo cazó el
	# contraste externo: «no cumple la afirmación de romper exactamente una cosa».
	if [ "$MUT" = "live-accepts-anything" ] && [ "${OLIVARES_FAKE_LIVE_SENTINEL:-}" = "$base" ]; then
		echo "check OK: manifest verifies"; exit 0; fi
	murl="${base%/}/stable/manifest.json"
	body="$(curl -fsS -m 5 "$murl" 2>/dev/null)" || { echo "Error: fetch manifest $murl" >&2; exit 1; }
	sig="$(curl -fsS -m 5 "$murl.sig" 2>/dev/null)" || {
		if [ "$MUT" = "accept-unsigned" ]; then echo "check OK: manifest verifies"; exit 0; fi
		echo "Error: fetch manifest signature: $murl.sig: GET $murl.sig returned 404" >&2
		exit 1; }
	want="$(printf '%s\n' "$body" | sha256sum | cut -d' ' -f1)"
	# The key is modelled as a domain separator: a different key cannot produce this signature.
	# `accept-anything` is the mutant that makes verification decorative.
	if [ "$MUT" = "accept-anything" ]; then echo "check OK: manifest verifies"; exit 0; fi
	if [ "$MUT" = "refuse-everything" ]; then
		echo "Error: REFUSING to upgrade: release: signature does not verify against the OTA key" >&2; exit 1; fi
	if [ "$want" != "$sig" ]; then
		echo "Error: REFUSING to upgrade: release: signature does not verify against the OTA key" >&2; exit 1; fi
	if [ "$MUT" = "ignore-key" ]; then echo "check OK: manifest verifies"; exit 0; fi
	if [ -n "${OLIVARES_FAKE_KEY:-}" ] && [ "$pubkey" != "${OLIVARES_FAKE_KEY}" ]; then
		echo "Error: REFUSING to upgrade: release: signature does not verify against the OTA key" >&2; exit 1; fi
	echo "check OK: manifest verifies and an upgrade is available"
	exit 0
fi

echo "fake: unsupported: $sub $*" >&2
exit 64
FAKE
chmod +x "$T/fake-olivares"
# ⛔ UN `TMPDIR` NOEXEC ES «NO HE PODIDO MIRAR», NO NUEVE FALLOS. En este contenedor `/tmp` está
# montado noexec: el `chmod` funciona, el fichero NO se puede ejecutar, y el guion bajo prueba lo
# rechazaba como «no ejecutable» — la batería salía 1 con 9 de 11 casos rojos por una propiedad del
# MONTAJE, no del sujeto. Lo destapó el contraste externo corriéndola con el TMPDIR por defecto.
"$T/fake-olivares" version >/dev/null 2>&1 || {
	echo "test-verify-business-chain: NO HE PODIDO MIRAR: no puedo EJECUTAR ficheros bajo ${TMPDIR:-/tmp} (¿montado noexec?); exporta un TMPDIR ejecutable" >&2
	exit 2; }
for _tool in node tar sha256sum cut timeout; do
	command -v "$_tool" >/dev/null 2>&1 || {
		echo "test-verify-business-chain: NO HE PODIDO MIRAR: falta $_tool" >&2; exit 2; }
done

# The walker mints a fresh key per run and passes it to the client; the fake pins the FIRST
# key it is shown, so the wrong-key case can differ from it. That pinning happens in a wrapper
# so the fake itself stays stateless.
cat > "$T/olivares" <<WRAP
#!/usr/bin/env bash
set -u
KEYFILE="$T/pinned.key"
for a in "\$@"; do :; done
prev=""
for a in "\$@"; do
	if [ "\$prev" = "--pubkey" ] && [ ! -s "\$KEYFILE" ]; then printf '%s' "\$a" > "\$KEYFILE"; fi
	prev="\$a"
done
[ -s "\$KEYFILE" ] && export OLIVARES_FAKE_KEY="\$(cat "\$KEYFILE")"
exec "$T/fake-olivares" "\$@"
WRAP
chmod +x "$T/olivares"

run_walker() { # $1 = mutant, rest = extra args
	local mut="$1"; shift
	rm -f "$T/pinned.key"
	OLIVARES_FAKE_MUTANT="$mut" bash "$SUT" --binary "$T/olivares" --only channel-hermetic "$@" 2>&1
}

check() { # name, expected-rc, actual-rc, output, must-contain
	local name="$1" want="$2" got="$3" out="$4" needle="${5:-}"
	cases=$((cases + 1))
	if [ "$got" != "$want" ]; then
		echo "FAIL · $name: expected rc=$want, got rc=$got"
		printf '%s\n' "$out" | sed 's/^/       /' | tail -6
		fails=$((fails + 1)); return
	fi
	if [ -n "$needle" ] && ! grep -q -- "$needle" <<<"$out"; then
		echo "FAIL · $name: rc was right but the reason was not named (missing: $needle)"
		printf '%s\n' "$out" | sed 's/^/       /' | tail -6
		fails=$((fails + 1)); return
	fi
	echo "ok   · $name"
}

# ── CONTROL POSITIVO: a faithful client must come out CLEAN ─────────────────────────────
# Without this every mutant below could be passing for the wrong reason (a walker that always
# says "finding" catches every mutant and is worthless).
out="$(run_walker none)"; rc=$?
check "positive control: a faithful client passes the four hermetic cases" 0 "$rc" "$out" "4 check(s)"
check "and a scoped run says so instead of claiming the chain" 0 "$rc" "$out" "THE CHAIN WAS NOT EVALUATED"

# ── MUTANTS ─────────────────────────────────────────────────────────────────────────────
out="$(run_walker accept-anything)"; rc=$?
check "mutant accept-anything: a client that verifies nothing is caught" 1 "$rc" "$out" "channel-hermetic/tampered"

out="$(run_walker accept-unsigned)"; rc=$?
check "mutant accept-unsigned: a client that accepts a channel with NO .sig is caught (the v26.8.0 shape)" 1 "$rc" "$out" "channel-hermetic/unsigned"

out="$(run_walker ignore-key)"; rc=$?
check "mutant ignore-key: a client that ignores WHICH key signed is caught" 1 "$rc" "$out" "channel-hermetic/wrong-key"

out="$(run_walker refuse-everything)"; rc=$?
check "mutant refuse-everything: a client that refuses even a valid channel is caught" 1 "$rc" "$out" "channel-hermetic/valid"

rm -f "$T/pinned.key"
out="$(OLIVARES_FAKE_MUTANT=enterprise-open bash "$SUT" --binary "$T/olivares" --only enterprise-denyclosed 2>&1)"; rc=$?
check "mutant enterprise-open: an enterprise upgrade WITHOUT a licence is caught" 1 "$rc" "$out" "enterprise-denyclosed"

# ── EL MUTANTE QUE EL CONTRASTE ECHÓ EN FALTA, y es el que cierra el verde falso más caro ──
# `accept-live-with-random-key`: un cliente correcto en el canal hermético que, en la pata LIVE,
# devuelve 0 pase lo que pase. Antes sobrevivía a toda la batería porque los cuatro mutantes de canal
# se ejercen con `--only channel-hermetic` y nadie ejercía `channel-live`. Muere bajo la aserción
# `channel-live/unkeyed-rc0-is-a-finding`: con una clave ACUÑADA AL AZAR, un rc=0 no puede ser un pass.
rm -f "$T/pinned.key"
out="$(OLIVARES_FAKE_MUTANT=live-accepts-anything OLIVARES_FAKE_LIVE_SENTINEL="http://127.0.0.1:1/" \
	bash "$SUT" --binary "$T/olivares" --live --only channel-live --endpoint "http://127.0.0.1:1/" 2>&1)"; rc=$?
check "mutant accept-live-with-random-key: a client that returns 0 in the LIVE leg under a random key is caught" 1 "$rc" "$out" "unkeyed-rc0-is-a-finding"

# Y el control que faltaba: ese mutante debe dejar INTACTA la pata hermética. Sin él, «rompe
# exactamente una cosa» era una afirmación del rótulo y no una propiedad medida.
rm -f "$T/pinned.key"
out="$(OLIVARES_FAKE_MUTANT=live-accepts-anything OLIVARES_FAKE_LIVE_SENTINEL="http://127.0.0.1:1/" \
	bash "$SUT" --binary "$T/olivares" --only channel-hermetic 2>&1)"; rc=$?
check "y ese mismo mutante NO toca la pata hermética: sigue limpia" 0 "$rc" "$out" "4 check(s)"

# Y su no-disparo, que es la mitad que impide que la guarda de arriba sea «rechaza siempre»: un
# cliente honesto contra un endpoint muerto NO puede salir 0, y eso tiene que leerse como hallazgo
# de canal, nunca como el mutante de arriba.
rm -f "$T/pinned.key"
out="$(bash "$SUT" --binary "$T/olivares" --live --only channel-live --endpoint "http://127.0.0.1:1/" 2>&1)"; rc=$?
cases=$((cases + 1))
if [ "$rc" != "0" ] && ! grep -q "unkeyed-rc0-is-a-finding" <<<"$out"; then
	echo "ok   · no-fire: an honest client against a dead endpoint is NOT reported as the unkeyed mutant"
else
	echo "FAIL · an honest client against a dead endpoint was misread (rc=$rc)"
	printf '%s\n' "$out" | sed 's/^/       /' | tail -4
	fails=$((fails + 1))
fi

# ── #13 · una opción truncada tiene que REHUSAR, no girar para siempre ────────────────────
out="$(timeout 10s bash "$SUT" --binary 2>&1)"; rc=$?
check "a value-taking option with no value exits 2 instead of looping" 2 "$rc" "$out" "needs a value"

# ── #1 · UNA MITAD SIN MEDIR DENTRO DE UNA PATA SELECCIONADA TAMBIÉN ES rc=2 ────────────
# El contraste lo reprodujo: `--only enterprise-client` contra un endpoint muerto imprimía «NO HE
# PODIDO MIRAR» y salía 0, porque el retorno de `--only` iba por encima del de parcialidad. El
# fichero de licencia y el de token existen a propósito: lo que debe fallar aquí es el transporte,
# no la precondición.
printf 'no-es-una-licencia\n' > "$T/fake.license"
printf 'no-es-un-token\n' > "$T/fake.token"
out="$(OLIVARES_FAKE_DEAD_ENDPOINT="http://127.0.0.1:1" bash "$SUT" --binary "$T/olivares" --live \
	--only enterprise-client --enterprise-endpoint "http://127.0.0.1:1" --license "$T/fake.license" \
	--download-token "$T/fake.token" 2>&1)"; rc=$?
check "una pata seleccionada con una mitad sin medir sale 2, no 0" 2 "$rc" "$out" "PARTIAL"

# ── EL TERCER CONTROL NEGATIVO: UNA CREDENCIAL REVOCADA ─────────────────────────────────
# El encargo pide tres controles negativos y el guion tenia dos: sin derecho no hay bytes, y un
# manifiesto manipulado se rehusa. Faltaba «licencia revocada -> rechazo», que NO es lo mismo que un
# token inventado: una credencial revocada FUE valida, y ahi es donde viven la cache que no expira,
# el registro que no se re-consulta y la firma que sigue verificando.
#
# Por eso estos casos traen una puerta de descarga hostil de verdad, en vez de solo un endpoint
# muerto: es la primera vez que la mitad POSITIVA de la pata enterprise se ejerce en el banco.
rm -f "$T/pinned.key"
printf 'token-del-comprador\n' > "$T/good.token"
printf 'token-reembolsado\n'  > "$T/revoked.token"
cat > "$T/gate.py" <<'GATE'
import hashlib, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs

GOOD = os.environ["GATE_GOOD"]
REVOKED = os.environ["GATE_REVOKED"]
MODE = os.environ.get("GATE_MODE", "honest")
ARTIFACT = b"x" * 4096
MANIFEST = b'{"channel":"stable","version":"29.12.0"}'
# El cliente falso hace `body="$(curl ...)"` y luego `printf '%s\n' "$body" | sha256sum`, o sea que
# firma el cuerpo MAS un salto de linea. La firma se calcula igual o la verificacion no casaria.
SIG = hashlib.sha256(MANIFEST + b"\n").hexdigest().encode()

class Gate(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, body=b"", headers=()):
        self.send_response(code)
        for k, v in headers:
            self.send_header(k, v)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if body:
            self.wfile.write(body)

    def do_GET(self):
        u = urlparse(self.path)
        if u.path.endswith("/stable/manifest.json"):
            return self._send(200, MANIFEST)
        if u.path.endswith("/stable/manifest.json.sig"):
            return self._send(200, SIG)
        token = (parse_qs(u.query).get("token") or [""])[0]
        entitled = token == GOOD or (token == REVOKED and MODE == "serves-revoked")
        if not entitled:
            return self._send(403)
        if self.headers.get("Range"):
            return self._send(206, ARTIFACT[:1024],
                              (("Content-Range", "bytes 0-1023/%d" % len(ARTIFACT)),))
        return self._send(200, ARTIFACT)

HTTPServer(("127.0.0.1", int(sys.argv[1])), Gate).serve_forever()
GATE

gate_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
GATE_PID=""
start_gate() { # $1 = honest | serves-revoked
	GATE_GOOD="token-del-comprador" GATE_REVOKED="token-reembolsado" GATE_MODE="$1" \
		python3 "$T/gate.py" "$gate_port" >/dev/null 2>&1 &
	GATE_PID=$!
	local i=0
	while [ "$i" -lt 50 ]; do
		curl -fsS -m 2 -o /dev/null "http://127.0.0.1:${gate_port}/stable/manifest.json" 2>/dev/null && return 0
		i=$((i + 1)); sleep 0.1
	done
	return 1
}
stop_gate() { [ -n "${GATE_PID:-}" ] && kill "$GATE_PID" 2>/dev/null; GATE_PID=""; true; }
trap 'stop_gate; rm -rf "$T"' EXIT

run_client_leg() { # $@ = extra args
	bash "$SUT" --binary "$T/olivares" --live --only enterprise-client \
		--enterprise-endpoint "http://127.0.0.1:${gate_port}" \
		--license "$T/fake.license" --download-token "$T/good.token" "$@" 2>&1
}

if ! start_gate honest; then
	echo "FAIL - la puerta falsa no arranco en el puerto ${gate_port}: estos tres casos no se han medido"
	fails=$((fails + 1)); cases=$((cases + 3))
else
	# CONTROL POSITIVO de la pata entera: con una puerta honesta y las cuatro credenciales, la pata
	# enterprise pasa. Sin el, los dos casos de abajo los aprobaria una pata que siempre falla.
	out="$(run_client_leg --revoked-token "$T/revoked.token")"; rc=$?
	check "puerta honesta: la pata enterprise entera pasa (instala, sirve al comprador, rehusa al resto)" 0 "$rc" "$out" "enterprise-client/revoked"

	# EL MUTANTE: la MISMA puerta, cambiando UNA cosa - sigue sirviendo a la credencial revocada.
	stop_gate
	if ! start_gate serves-revoked; then
		echo "FAIL - la puerta mutante no arranco: el caso que de verdad importa no se ha medido"
		fails=$((fails + 1)); cases=$((cases + 1))
	else
		out="$(run_client_leg --revoked-token "$T/revoked.token")"; rc=$?
		check "mutante sirve-al-revocado: entregar bytes a un derecho REVOCADO es HALLAZGO" 1 "$rc" "$out" "enterprise-client/revoked"
	fi
	stop_gate

	# Y SIN LA OPCION, NO SE HA MEDIDO: rc=2 y con nombre, jamas un verde que se apunte un control
	# que nadie ejercio.
	if start_gate honest; then
		out="$(run_client_leg)"; rc=$?
		check "sin --revoked-token: el control de revocacion NO medido es rc=2, no un verde" 2 "$rc" "$out" "enterprise-client/revoked"
		stop_gate
	else
		echo "FAIL - la puerta honesta no rearranco: el caso de la opcion ausente no se ha medido"
		fails=$((fails + 1)); cases=$((cases + 1))
	fi
fi

# ── LA AYUDA NOMBRA LO QUE EL GUION MIDE, Y ESO SE COMPRUEBA ───────────────────────────
# Dos veces se ha roto la ayuda de este walker por el mismo motivo y por extremos opuestos: primero
# no nombraba `--download-token`, sin el cual su pata enterprise no puede salir verde; despues su
# corte era un NUMERO DE LINEA (`6,52p`) y al crecer la cabecera dejo fuera la lista de patas. Un
# numero de linea envejece con el fichero que describe, asi que la ayuda ahora termina donde empieza
# el codigo — y este caso lo comprueba derivando las dos listas del propio guion en vez de copiarlas.
out="$(bash "$SUT" --help 2>&1)"; rc=$?
cases=$((cases + 1))
help_missing=""
for _leg in channel-hermetic channel-live enterprise-denyclosed enterprise-gate enterprise-client; do
	grep -q -- "$_leg" <<<"$out" || help_missing="$help_missing $_leg"
done
# Las opciones con valor salen del propio parser del guion: si manana alguien anade una y no la
# documenta, este caso lo dice sin que nadie mantenga una segunda lista.
while read -r _opt; do
	grep -q -- "$_opt" <<<"$out" || help_missing="$help_missing $_opt"
done < <(grep -oE '^[[:space:]]*--[a-z-]+\) need_value' "$SUT" | grep -oE '\-\-[a-z-]+')
# ⛔ Y LA DERIVACION TIENE QUE DEVOLVER ALGO. Un `grep` que deja de casar convierte este caso en un
# verde vacio: comprobaria cero opciones y diria «la ayuda las nombra todas». Me paso al escribirlo
# —el ancla era `^\t` y no casaba nada—, asi que la ausencia de opciones es un fallo, no un pase.
if [ "$(grep -cE '^[[:space:]]*--[a-z-]+\) need_value' "$SUT")" -lt 5 ]; then
	help_missing="$help_missing (la derivacion de opciones no encontro el parser: caso vacio)"
fi
if [ "$rc" -ne 0 ]; then
	echo "FAIL - --help salio rc=$rc"; fails=$((fails + 1))
elif [ -n "$help_missing" ]; then
	echo "FAIL - la ayuda no nombra:$help_missing"; fails=$((fails + 1))
else
	echo "ok   - la ayuda nombra las cinco patas y todas las opciones con valor del parser"
fi

# ── UN HALLAZGO NO SE ENTIERRA BAJO UNA CEGUERA POSTERIOR ───────────────────────────────
# Tres ramas de transporte de `enterprise-gate` y una de `channel-live` salian del guion entero con
# `exit 2`. Si una pata anterior ya habia encontrado algo, esa salida convertia la corrida en «no he
# podido mirar» — un veredicto MAS SUAVE sobre algo que ya estaba medido. Aqui el cliente miente
# (acepta cualquier canal, o sea HALLAZGO en la pata hermetica) y ademas el endpoint enterprise esta
# muerto (ceguera posterior): el veredicto tiene que ser 1, y el resumen tiene que decir las dos
# cosas.
rm -f "$T/pinned.key"
out="$(OLIVARES_FAKE_MUTANT=accept-anything bash "$SUT" --binary "$T/olivares" --live \
	--enterprise-endpoint "http://127.0.0.1:1" 2>&1)"; rc=$?
check "un hallazgo con una pata ciega detras sale 1, no 2" 1 "$rc" "$out" "FINDINGS"
cases=$((cases + 1))
if grep -q "NOT measured either" <<<"$out"; then
	echo "ok   - el resumen del hallazgo declara ademas lo que no se midio"
else
	echo "FAIL - un rc=1 con patas sin medir no lo dice: el lector supone que se midio todo"
	fails=$((fails + 1))
fi

# ── THE THIRD ANSWER: could-not-look must not be reported as clean ──────────────────────
out="$(bash "$SUT" --binary "$T/does-not-exist" 2>&1)"; rc=$?
check "no binary is rc=2 (could not look), never 0" 2 "$rc" "$out" "NO HE PODIDO MIRAR"

out="$(bash "$SUT" --binary "$T/olivares" --only no-such-leg 2>&1)"; rc=$?
check "a run where NO leg matched is rc=2, not a green" 2 "$rc" "$out" "no leg ran"

out="$(bash "$SUT" 2>&1)"; rc=$?
check "no --binary at all is rc=2" 2 "$rc" "$out" "--binary is required"

# ── The skips must be LOUD, which is the defect this walker was written against ─────────
rm -f "$T/pinned.key"
out="$(bash "$SUT" --binary "$T/olivares" 2>&1)"; rc=$?
check "an offline run with unmeasured legs is rc=2 (could not look), NOT a green" 2 "$rc" "$out" "PARTIAL"
cases=$((cases + 1))
if grep -q "SKIPPED (not measured) · channel-live" <<<"$out"; then
	echo "ok   · the unmeasured live channel is named, not silently dropped"
else
	echo "FAIL · an unmeasured live leg was not announced"; fails=$((fails + 1))
fi

echo
if [ "$fails" -gt 0 ]; then
	echo "test-verify-business-chain: $fails of $cases cases FAILED"
	exit 1
fi
echo "test-verify-business-chain: $cases cases, all behaved (7 mutants: 5 client + 1 enterprise + 1 download gate · 3 no-fire · 6 could-not-look · 6 summary/skip · 1 help-completeness)"
exit 0
