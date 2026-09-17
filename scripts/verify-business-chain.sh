#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# WALK THE COMMERCIAL CHAIN THE WAY A CUSTOMER WALKS IT — up to the signed manifest.
#
# WHY THIS EXISTS, and it is not "one more check". Everything we already had verifies the
# chain from OUR side: `test:license-worker` drives the Worker with fakes, `e2e/live-e2e.mjs`
# drives it over real HTTP into workerd, `check-commerce-preflight.sh` asks whether an
# environment can deliver what it is about to charge, `verify-release.sh` verifies the release
# artefacts. All correct, all about the SERVER. On 2026-09-03 v26.8.0 was declared "verified
# end to end" while `olivares upgrade` could not verify the community channel at all: the
# client asks for `<channel>-manifest.json.sig` (core/release/channelurl.go:98) and the
# published release has no such asset. Every leg verified a signature; none verified THE ONE
# THE CLIENT ASKS FOR.
#
# So the instrument here is the CLIENT ITSELF. Not a curl that imitates it — the binary. A
# probe that re-derives the client's rules can drift from them (and did: a `curl` without -L
# reported a false red on a healthy channel because Go's http follows the 302 that
# `releases/latest/download` answers with, and the probe did not). The only probe that cannot
# drift from the client is the client.
#
# WHAT IT DOES NOT DO, said rather than implied:
#   - ⛔ IT DOES NOT FETCH OR VERIFY THE ARTEFACT BYTES. Every client call carries `--check`, and
#     the client RETURNS on that branch before `fetchArtifact` (cmd/olivares/cmd_upgrade.go). So a
#     channel whose manifest and signature are valid while the tarball is missing or corrupt comes
#     out clean here. What this walks is: licence installs · signed channel manifest verifies ·
#     anonymous download is refused. Calling that "pay-to-update, end to end" would be a label
#     wider than the measurement — the external contrast named it and it is corrected here rather
#     than argued with;
#   - it does not buy anything, ever. The enterprise client leg needs a licence and a download
#     token you already have; this script never touches money, and never writes to production;
#   - it does not replace the server-side legs above; it is the half they cannot cover;
#   - the LIVE legs are opt-in (--live) because their subject is a deployed environment. But
#     an OFF live leg is announced LOUDLY and the summary says the run was PARTIAL. That is
#     deliberate: the gate that would have caught the unsigned channel
#     (check-community-updates-prep.sh) has had a live half since d18c078f9 and it is opt-in
#     too — and nothing said so, so an offline green read as "the channel is fine".
#
# THREE ANSWERS, BY EXIT CODE, never by prose (canon §1.5):
#   0  clean          every leg that ran, passed
#   1  finding        a leg failed: the chain is broken where it says
#   2  could not look a precondition is missing (no binary, no network, no python3...)
#
# USAGE
#   scripts/verify-business-chain.sh --binary ./olivares [--live] [--endpoint URL]
#                                    [--enterprise-endpoint URL] [--license FILE]
#                                    [--download-token FILE] [--revoked-token FILE]
#                                    [--ota-pubkey B64]
#                                    [--current-version V] [--only LEG]
#
#   --revoked-token FILE  a credential whose entitlement was REVOKED or refunded. The brief asks
#         for three negative controls and this is the third: without it the run says so out loud
#         and exits 2, because "we did not try a revoked credential" is not "revoked credentials
#         are refused". In the sandbox it is the token of the refunded test purchase.
#
#   LEGs: channel-hermetic | channel-live | enterprise-denyclosed | enterprise-gate |
#         enterprise-client
set -u -o pipefail

RC_CLEAN=0
RC_FINDING=1
RC_BLIND=2

# Bench state, cleaned on EXIT (see the note in leg_channel_hermetic). Declared here so the trap
# below can read them under `set -u` even when the bench never ran.
VBC_BENCH_DIR=""
VBC_SRV_PID=""
# ⛔ UNA LISTA, NO UNA VARIABLE. `enterprise-denyclosed` y `enterprise-client` guardaban su temporal
# en la MISMA variable, así que un paseo completo que pasara por las dos dejaba el primer directorio
# sin borrar: el trap sólo veía el segundo. Una fuga por corrida completa.
VBC_TMPDIRS=()
vbc_cleanup() {
	[ -n "${VBC_SRV_PID}" ] && kill "${VBC_SRV_PID}" 2>/dev/null
	[ -n "${VBC_BENCH_DIR}" ] && rm -rf "${VBC_BENCH_DIR}"
	local d
	for d in "${VBC_TMPDIRS[@]:-}"; do
		[ -n "$d" ] && rm -rf "$d"
	done
	return 0
}
trap vbc_cleanup EXIT

say() { printf 'verify-business-chain: %s\n' "$*"; }
die_blind() { say "NO HE PODIDO MIRAR — $*"; exit "$RC_BLIND"; }

BINARY=""
LIVE=0
ENDPOINT=""                                    # empty = the client's own compiled default
ENT_ENDPOINT="https://licenses.olivares.ai"
LICENSE=""
DOWNLOAD_TOKEN_SRC=""
REVOKED_TOKEN_SRC=""
OTA_PUBKEY="${OLIVARES_OTA_PUBKEY:-}"
CURRENT_VERSION=""
ONLY=""

# ⛔ CADA OPCIÓN CON VALOR EXIGE SU VALOR, Y ESO NO ES CORTESÍA: con `${2:-}` la variable quedaba
# vacía sin violar `set -u`, pero `shift 2` FALLA cuando sólo queda un argumento y, sin `set -e`, el
# `while` no consumía nada y GIRABA PARA SIEMPRE. Medido por el contraste: `timeout 1s … --binary`
# terminó en 124. Un guion de verificación que se cuelga con una opción truncada es peor que uno que
# rehúsa: el que rehúsa dice qué falta.
need_value() { [ "$1" -ge 2 ] || die_blind "the option $2 needs a value and none followed it."; }
while [ $# -gt 0 ]; do
	case "$1" in
	--binary) need_value $# "$1"; BINARY="$2"; shift 2 ;;
	--live) LIVE=1; shift ;;
	--endpoint) need_value $# "$1"; ENDPOINT="$2"; shift 2 ;;
	--enterprise-endpoint) need_value $# "$1"; ENT_ENDPOINT="$2"; shift 2 ;;
	--license) need_value $# "$1"; LICENSE="$2"; shift 2 ;;
	--download-token) need_value $# "$1"; DOWNLOAD_TOKEN_SRC="$2"; shift 2 ;;
	--revoked-token) need_value $# "$1"; REVOKED_TOKEN_SRC="$2"; shift 2 ;;
	--ota-pubkey) need_value $# "$1"; OTA_PUBKEY="$2"; shift 2 ;;
	--current-version) need_value $# "$1"; CURRENT_VERSION="$2"; shift 2 ;;
	--only) need_value $# "$1"; ONLY="$2"; shift 2 ;;
	# ⛔ EL FINAL DE LA AYUDA SE DERIVA, NO SE NUMERA. Era `6,52p`, y al crecer la cabecera con el
	# tercer control negativo el corte cayó justo delante de la lista de PATAS: la ayuda dejaba de
	# nombrar lo que el guion mide. Es exactamente el defecto que ya se curó una vez en este fichero
	# —la ayuda no nombraba la opción que su pata enterprise exige— reaparecido por el otro extremo,
	# porque un número de línea envejece con el fichero que describe. Ahora termina donde termina la
	# cabecera: en la primera línea que ya es código.
	-h|--help) sed -n '6,/^set -u/p' "$0" | sed '$d'; exit 0 ;;
	*) die_blind "unknown argument: $1" ;;
	esac
done

[ -n "$BINARY" ] || die_blind "--binary is required: this script measures with the CLIENT, so there is nothing to measure without one."
[ -x "$BINARY" ] || die_blind "--binary $BINARY is not executable."
BINARY="$(cd "$(dirname "$BINARY")" && pwd)/$(basename "$BINARY")"

# A source-built binary has no version stamp, and `upgrade` REFUSES rather than guess (the
# anti-rollback and min-version guards are claims about the installed version). Declaring one
# keeps both guards armed; inventing a default here would hide the very refusal we want.
# ⛔ EL rc DE `version` SE OBSERVA. Al pasar de tubería a sustitución de comando el status se perdía,
# así que un binario que imprimiera « dev » y saliera distinto de cero se clasificaba como build de
# desarrollo y recibía una versión inventada. No poder identificar el binario es «no he podido
# mirar», no «es dev».
BINARY_STAMPED=1
_ver_line="$("$BINARY" version 2>/dev/null)" || die_blind "\`$BINARY version\` salió con error: no puedo identificar el binario que uso como instrumento."
if grep -q ' dev ' <<<"$_ver_line"; then
	BINARY_STAMPED=0
	if [ -z "$CURRENT_VERSION" ]; then
		CURRENT_VERSION="26.0.0"
		say "note: the binary is an unstamped dev build, so --current-version defaults to ${CURRENT_VERSION} (both upgrade guards stay armed)."
	fi
fi
# ⛔ A UN BINARIO SELLADO NO SE LE DECLARA LA VERSIÓN, y el cliente tiene razón al rehusarlo:
# «--current-version says X but <binario> reports Y — refusing to act on a declaration the target
# contradicts». Medido ejerciendo la pata enterprise contra sandbox: pasar la bandera «por si acaso»
# convertía un canal SANO en un rojo que parecía del despliegue. La bandera existe para el binario
# que no sabe su versión, no para contradecir al que sí.
if [ "$BINARY_STAMPED" -eq 1 ] && [ -n "$CURRENT_VERSION" ]; then
	say "note: the binary reports its own version, so --current-version is dropped (the client refuses a declaration that contradicts the target)."
	CURRENT_VERSION=""
fi

runs=0
findings=0
partial=0
FAILED_LEGS=""

want_leg() { [ -z "$ONLY" ] || [ "$ONLY" = "$1" ]; }
pass() { runs=$((runs + 1)); say "  ok   · $1"; }
fail() { runs=$((runs + 1)); findings=$((findings + 1)); FAILED_LEGS="${FAILED_LEGS} $1"; say "  FAIL · $1 — $2"; }
skip_loud() { partial=$((partial + 1)); say "  SKIPPED (not measured) · $1 — $2"; }

client() {
	# The client, with the declared version and the key under test. stdout+stderr together:
	# the refusals we assert on are printed on stderr.
	local args=("upgrade" "--check")
	[ -n "$CURRENT_VERSION" ] && args+=("--current-version" "$CURRENT_VERSION")
	[ -n "$OTA_PUBKEY_ARG" ] && args+=("--pubkey" "$OTA_PUBKEY_ARG")
	"$BINARY" "${args[@]}" "$@" 2>&1
}

# ─────────────────────────────────────────────────────────────────────────────────────────
# LEG 1 · channel-hermetic — the client over a channel WE build, so the four verdicts are
# known in advance. This is the leg that can run anywhere, with no network and no deployment,
# and it is the one that pins the client's contract: a valid pair verifies, a tampered
# manifest is refused, a MISSING SIGNATURE is refused (the exact shape of the v26.8.0 defect),
# and a wrong key is refused. Without the last two, "it verifies" proves nothing: a client
# that accepts everything passes the first case too.
# ─────────────────────────────────────────────────────────────────────────────────────────
leg_channel_hermetic() {
	command -v python3 >/dev/null 2>&1 || die_blind "python3 is needed to serve the hermetic channel."
	command -v node >/dev/null 2>&1 || die_blind "node is needed to mint the throwaway OTA key."
	command -v tar >/dev/null 2>&1 || die_blind "tar is needed to build the throwaway artefact."

	local B; B="$(mktemp -d "${TMPDIR:-/tmp}/vbc.XXXXXX")" || die_blind "mktemp failed."
	[ -d "$B" ] || die_blind "mktemp returned no directory."
	# ⛔ EL LIMPIADOR VA EN `EXIT`, NO EN `RETURN`, y la diferencia no es de estilo: esta función
	# llama a `die_blind`, que hace `exit 2`, y un `trap … RETURN` NO se dispara cuando se sale del
	# guion — se dispara al RETORNAR de la función. Con el limpiador en RETURN, cualquier «no he
	# podido mirar» a mitad de la pata dejaba vivo un servidor HTTP y su directorio temporal, una
	# fuga por cada corrida fallida. El estado va en variables del guion (no `local`) para que el
	# trap de EXIT las vea.
	VBC_BENCH_DIR="$B"
	VBC_SRV_PID=""

	mkdir -p "$B/dist" "$B/mirror/stable" || die_blind "cannot write under $B."
	printf 'not a real binary\n' > "$B/dist/olivares"
	# ⛔ LA VERSIÓN DEL BANCO ES DELIBERADAMENTE ALTA. Con 26.8.0 y un binario ENTREGADO que también
	# es 26.8.0, el cliente contesta «already on 26.8.0 — nothing to do»: el manifiesto verifica y la
	# aserción, que exigía «manifest verifies and an upgrade is available», salía roja por la versión
	# del binario y no por el canal. Una versión superior hace el caso determinista sin aflojar el
	# texto exigido. Respeta el CalVer del proyecto ((2[6-9]|[3-9][0-9]).(1-12).N).
	tar -czf "$B/dist/olivares_29.12.0_linux_amd64.tar.gz" -C "$B/dist" olivares 2>/dev/null ||
		die_blind "tar could not build the throwaway artefact."

	# Key material is FRESH per run, on purpose: a committed key would make the green
	# reproducible from a secret in the tree, which is the opposite of what this proves.
	node -e '
const c=require("crypto");const {privateKey}=c.generateKeyPairSync("ed25519");
const j=privateKey.export({format:"jwk"});
const seed=Buffer.from(j.d,"base64url"), pub=Buffer.from(j.x,"base64url");
process.stdout.write(Buffer.concat([seed,pub]).toString("base64")+"\n"+pub.toString("base64")+"\n");
' > "$B/keys.txt" || die_blind "node could not mint an Ed25519 key."
	# 0600 aunque sea efímera: una clave privada legible por todo el contenedor enseña la costumbre
	# equivocada, y la copia quien escriba el siguiente banco.
	( umask 077; sed -n '1p' "$B/keys.txt" > "$B/priv.key" )
	local pub; pub="$(sed -n '2p' "$B/keys.txt")"
	[ -n "$pub" ] || die_blind "the minted key has no public half."

	"$BINARY" release manifest --version 29.12.0 --dir "$B/dist" --channel stable \
		--out "$B/mirror/stable/manifest.json" --sign-key "@$B/priv.key" >"$B/mk.log" 2>&1 ||
		die_blind "the client could not build its own signed manifest: $(tail -1 "$B/mk.log")"
	[ -s "$B/mirror/stable/manifest.json.sig" ] || die_blind "no signature was written beside the manifest."

	local port; port="$(python3 - <<'PY'
import socket
s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()
PY
)" || die_blind "could not pick a free port."
	( cd "$B/mirror" && exec python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1 ) &
	VBC_SRV_PID=$!
	local base="http://127.0.0.1:${port}/"
	local i=0
	while [ "$i" -lt 50 ]; do
		curl -fsS -m 2 -o /dev/null "${base}stable/manifest.json" 2>/dev/null && break
		i=$((i + 1)); sleep 0.1
	done
	[ "$i" -lt 50 ] || die_blind "the local channel never came up on port ${port}."

	local out rc
	OTA_PUBKEY_ARG="$pub"

	# A · a valid pair MUST verify. Without this the three refusals below prove only that the
	# client refuses everything.
	out="$(client --endpoint "$base")"; rc=$?
	if [ "$rc" -eq 0 ] && grep -q 'manifest verifies' <<<"$out"; then
		pass "channel-hermetic/valid: the client verifies a well-formed signed channel"
	else
		fail "channel-hermetic/valid" "a correctly signed channel was NOT accepted (rc=$rc): $(printf '%s' "$out" | tail -1)"
	fi

	# B · a TAMPERED manifest must be refused by signature, not by shape.
	cp "$B/mirror/stable/manifest.json" "$B/manifest.orig"
	python3 - "$B/mirror/stable/manifest.json" <<'PY'
import json,sys
p=sys.argv[1]; d=json.load(open(p)); d["version"]="26.9.9"; open(p,"w").write(json.dumps(d))
PY
	out="$(client --endpoint "$base")"; rc=$?
	if [ "$rc" -ne 0 ] && grep -q 'signature does not verify' <<<"$out"; then
		pass "channel-hermetic/tampered: a rewritten manifest is refused against the OTA key"
	else
		fail "channel-hermetic/tampered" "a manifest whose version was rewritten was NOT refused for its signature (rc=$rc)"
	fi
	cp "$B/manifest.orig" "$B/mirror/stable/manifest.json"

	# C · THE v26.8.0 SHAPE: manifest present, signature absent. This is the case that was
	# live in production while every other leg was green.
	mv "$B/mirror/stable/manifest.json.sig" "$B/sig.away"
	out="$(client --endpoint "$base")"; rc=$?
	if [ "$rc" -ne 0 ] && grep -q 'fetch manifest signature' <<<"$out"; then
		pass "channel-hermetic/unsigned: a channel serving a manifest with NO .sig is refused"
	else
		fail "channel-hermetic/unsigned" "a channel with a 404 signature was NOT refused (rc=$rc) — this is exactly the v26.8.0 shape"
	fi
	mv "$B/sig.away" "$B/mirror/stable/manifest.json.sig"

	# D · the RIGHT signature under the WRONG key. Presence is not verification.
	local other; other="$(node -e '
const c=require("crypto");const {privateKey}=c.generateKeyPairSync("ed25519");
process.stdout.write(Buffer.from(privateKey.export({format:"jwk"}).x,"base64url").toString("base64"));')"
	OTA_PUBKEY_ARG="$other"
	out="$(client --endpoint "$base")"; rc=$?
	if [ "$rc" -ne 0 ] && grep -q 'signature does not verify' <<<"$out"; then
		pass "channel-hermetic/wrong-key: a valid signature under another key is refused"
	else
		fail "channel-hermetic/wrong-key" "a signature made by a DIFFERENT key was accepted (rc=$rc)"
	fi
	# Y el estado compartido se DEVUELVE limpio al salir de la pata, por la misma razón por la que
	# `channel-live` acuña su clave en una local: lo que una pata deja puesto lo hereda la siguiente,
	# y ese acoplamiento sólo se ve en el paseo completo.
	OTA_PUBKEY_ARG=""
}

# ─────────────────────────────────────────────────────────────────────────────────────────
# LEG 2 · channel-live — the same client against the real community channel. This is the leg
# whose absence let an unsigned channel ship.
# ─────────────────────────────────────────────────────────────────────────────────────────
leg_channel_live() {
	if [ "$LIVE" -ne 1 ]; then
		skip_loud "channel-live" "--live not given, so the DEPLOYED community channel was not measured at all"
		return
	fi
	# WITHOUT THE REAL KEY THIS LEG STILL SAYS SOMETHING — but only one thing, and it says
	# which. The client fetches the manifest AND its signature BEFORE verifying either, so a
	# MISSING SIGNATURE is a verdict that does not depend on the key at all. What does depend
	# on it is the opposite verdict: "the channel is good" cannot be reached with a throwaway
	# key, and claiming it would be the false green this whole script exists to prevent.
	# ⛔ LA CLAVE DE ESTA PATA ES **LOCAL**. La primera versión la acuñaba en la variable GLOBAL
	# `OTA_PUBKEY`, así que la pata `enterprise-client`, que corre DESPUÉS, se la pasaba al cliente y
	# salía «signature does not verify against the OTA key» — verde con `--only enterprise-client` y
	# rojo en el paseo completo. Una pata que contamina el estado de la siguiente sólo se ve
	# corriendo la cadena entera, que es exactamente para lo que existe este guion.
	local keyed=1 livekey="$OTA_PUBKEY"
	if [ -z "$livekey" ]; then
		keyed=0
		command -v node >/dev/null 2>&1 || die_blind "node is needed to mint the throwaway key for the live channel leg."
		livekey="$(node -e '
const c=require("crypto");const {privateKey}=c.generateKeyPairSync("ed25519");
process.stdout.write(Buffer.from(privateKey.export({format:"jwk"}).x,"base64url").toString("base64"));')" ||
			die_blind "node could not mint a throwaway key."
		say "  note: no --ota-pubkey given, so this leg can only prove ABSENCE (a missing signature), never presence."
	fi
	OTA_PUBKEY_ARG="$livekey"
	local out rc args=()
	[ -n "$ENDPOINT" ] && args+=(--endpoint "$ENDPOINT")
	out="$(client "${args[@]}")"; rc=$?
	if [ "$rc" -eq 0 ] && [ "$keyed" -eq 0 ]; then
		# ⛔ ESTA RAMA ERA UN VERDE FALSO Y ES LA MÁS CARA DEL GUION. Con una clave ACUÑADA AL AZAR
		# ningún canal sano puede verificar, así que un rc=0 aquí no dice «el canal está bien»: dice
		# que el cliente aceptó bajo una clave que no firmó nada. Lo reprodujo el contraste con
		# `--binary /bin/true`: un cliente que acepta todo convertía un endpoint muerto en verde.
		fail "channel-live/unkeyed-rc0-is-a-finding" "the client returned 0 under a RANDOM key: it is not verifying the channel signature"
	elif [ "$rc" -eq 0 ]; then
		pass "channel-live: the deployed community channel verifies for the client"
	elif grep -q 'fetch manifest signature' <<<"$out"; then
		fail "channel-live" "the channel serves a manifest with NO signature under the name the client composes: $(grep -m1 -o 'GET [^ ]* returned [0-9]*' <<<"$out")"
	elif grep -q 'fetch manifest http' <<<"$out"; then
		# ⛔ EL MANIFIESTO MISMO NO ESTÁ, que es OTRO defecto que la firma ausente y hay que nombrarlo
		# aparte: el cliente está pidiendo el canal en el endpoint que lleva COMPILADO, así que un 404
		# aquí dice «este binario apunta a un canal que no existe», no «el canal está sin firmar».
		# Medido con el artefacto de sandbox (build de ensayo del 2026-08-27): pide
		# `https://olivares.ai/updates/stable/manifest.json`, el endpoint community RETIRADO ese mismo
		# día, y por tanto su canal está muerto aunque el de hoy esté sano.
		fail "channel-live/no-manifest" "the channel does not exist at the endpoint this binary carries: $(grep -m1 -o 'GET [^ ]* returned [0-9]*' <<<"$out")"
	elif grep -q 'signature does not verify' <<<"$out"; then
		if [ "$keyed" -eq 1 ]; then
			fail "channel-live" "the channel's signature does not verify against the key given"
		else
			skip_loud "channel-live" "the channel serves BOTH files, but with a throwaway key nothing can be verified — re-run with --ota-pubkey to get a verdict"
		fi
	# La lista de firmas de transporte es ABIERTA y eso es un límite conocido, no un descuido: un
	# mensaje nuevo del cliente caería como HALLAZGO en vez de como «no he podido mirar». Mientras el
	# cliente no exponga una clase de error legible por máquina, esto es lo que hay, y se dice.
	elif grep -qiE 'no such host|connection refused|network is unreachable|i/o timeout|timeout|EOF|tls: |certificate' <<<"$out"; then
		# Misma razón que en `enterprise-gate`: un `exit 2` aquí entierra los hallazgos de las patas
		# que ya corrieron. Se cuenta como no medida y la corrida sigue.
		skip_loud "channel-live" "the endpoint was unreachable, which is not the same as broken"
	else
		fail "channel-live" "the client refused for another reason (rc=$rc): $(printf '%s' "$out" | tail -1)"
	fi
}

# ─────────────────────────────────────────────────────────────────────────────────────────
# LEG 3 · enterprise-denyclosed — no licence, no enterprise upgrade, and the refusal happens
# BEFORE the network. Offline by construction.
# ─────────────────────────────────────────────────────────────────────────────────────────
leg_enterprise_denyclosed() {
	local out rc key
	# A KEY IS NEEDED TO GET AS FAR AS THE LICENCE CHECK, and it does not have to be the real
	# one: a build with no embedded key refuses for THAT first ("no OTA-verification key"),
	# which would make this leg red for the wrong reason. So a throwaway key is minted unless
	# the caller gave one. The assertion is unchanged — it is about the LICENCE — and if the
	# client ever refused for the key instead, the message would not match and this leg fails.
	key="$OTA_PUBKEY"
	if [ -z "$key" ]; then
		command -v node >/dev/null 2>&1 || die_blind "node is needed to mint the throwaway key for the deny-closed leg."
		key="$(node -e '
const c=require("crypto");const {privateKey}=c.generateKeyPairSync("ed25519");
process.stdout.write(Buffer.from(privateKey.export({format:"jwk"}).x,"base64url").toString("base64"));')" ||
			die_blind "node could not mint a throwaway key."
	fi
	# ⛔ CON UN DATA-DIR EXPLÍCITO Y VACÍO. Sin él la pata leía el data-dir por defecto del operador:
	# en una máquina con licencia instalada el cliente pasa el control local y sale a la red con el
	# token ficticio, así que la pata medía la máquina en vez del escenario que rotula. No daba verde
	# falso, daba ROJO falso — y contradecía su propio «offline by construction».
	local empty; empty="$(mktemp -d "${TMPDIR:-/tmp}/vbc-empty.XXXXXX")" || die_blind "mktemp failed."
	VBC_TMPDIRS+=("$empty")
	out="$("$BINARY" upgrade --enterprise --check --endpoint "$ENT_ENDPOINT" --token "not-a-real-token" \
		--data-dir "$empty" --pubkey "$key" 2>&1)"; rc=$?
	if [ "$rc" -ne 0 ] && grep -q 'no license installed' <<<"$out"; then
		pass "enterprise-denyclosed: without an installed licence the client refuses, even WITH a token"
	else
		fail "enterprise-denyclosed" "the client did not refuse an enterprise upgrade without a licence (rc=$rc)"
	fi
}

# ─────────────────────────────────────────────────────────────────────────────────────────
# LEG 4 · enterprise-gate — the licensed download gate, unauthenticated, from outside. The
# positive control matters more than the 403: a deployment that answered 403 to EVERYTHING
# would pass a bare "is it 403" check while serving nothing at all.
# ─────────────────────────────────────────────────────────────────────────────────────────
# ⛔ ESTA PATA SALÍA DEL GUION ENTERO CON `exit 2` EN SUS TRES RAMAS DE TRANSPORTE, y eso ENTIERRA
# los hallazgos de las patas anteriores: `findings` ya podía valer 1 —un canal que no verifica, por
# ejemplo— y una caída de red aquí convertía la corrida en «no he podido mirar», que es un veredicto
# más suave sobre algo que YA se había medido. Un hallazgo es un hecho medido y la ceguera posterior
# no lo borra. Las tres ramas usan ahora `skip_loud` + `return`, así que la corrida termina y el
# recuento final decide en el orden correcto: primero los hallazgos, después la parcialidad.
leg_enterprise_gate() {
	if [ "$LIVE" -ne 1 ]; then
		skip_loud "enterprise-gate" "--live not given, so the deployed download gate was not measured"
		return
	fi
	command -v curl >/dev/null 2>&1 || die_blind "curl is needed for the gate leg."
	local code_health code_dl code_ghost
	# -L because the client follows redirects; a probe that does not follow them measures
	# something other than the client (measured 2026-09-03: a 302 read as a false red).
	code_health="$(curl -sS -L -m 20 -o /dev/null -w '%{http_code}' "${ENT_ENDPOINT%/}/health" 2>/dev/null)" || code_health="000"
	code_dl="$(curl -sS -L -m 20 -o /dev/null -w '%{http_code}' "${ENT_ENDPOINT%/}/download?os=linux&arch=amd64" 2>/dev/null)" || code_dl="000"
	[ "$code_dl" = "000" ] && { skip_loud "enterprise-gate/no-credential" "/download did not answer at all (transport)"; return; }
	code_ghost="$(curl -sS -L -m 20 -o /dev/null -w '%{http_code}' "${ENT_ENDPOINT%/}/this-route-does-not-exist" 2>/dev/null)" || code_ghost="000"

	if [ "$code_health" = "000" ]; then
		skip_loud "enterprise-gate" "${ENT_ENDPOINT} did not answer at all (transport)"
		return
	fi
	if [ "$code_health" != "200" ]; then
		fail "enterprise-gate/health" "the worker's /health answered ${code_health}, so the rest of this leg would measure a dead deployment"
		return
	fi
	pass "enterprise-gate/health: the deployment answers (200)"

	if [ "$code_dl" = "403" ]; then
		pass "enterprise-gate/no-credential: an unauthenticated download is refused (403)"
	else
		fail "enterprise-gate/no-credential" "an unauthenticated /download answered ${code_dl}, not 403 — without a credential there must be no bytes"
	fi

	# El control exige el 404 DOCUMENTADO (`src/index.ts`, ruta no encontrada), no «cualquier cosa
	# salvo 403»: con esa forma laxa, un fallo de TRANSPORTE (000) pasaba por control válido y
	# anunciaba que la puerta discrimina cuando en realidad no se había medido nada.
	if [ "$code_ghost" = "000" ]; then
		skip_loud "enterprise-gate/control" "the invented route did not answer at all (transport), so the 403 above is unproven"
	elif [ "$code_ghost" = "404" ]; then
		pass "enterprise-gate/control: an invented route answers 404, so the 403 is the gate and not a blanket"
	else
		fail "enterprise-gate/control" "an invented route answers ${code_ghost} (want 404): the 403 above does not prove the gate discriminates"
	fi
}

# ─────────────────────────────────────────────────────────────────────────────────────────
# LEG 5 · enterprise-client — the paying customer's own path, end to end. Needs a licence
# that ALREADY exists (this script never buys anything).
# ─────────────────────────────────────────────────────────────────────────────────────────
leg_enterprise_client() {
	if [ "$LIVE" -ne 1 ] || [ -z "$LICENSE" ] || [ -z "$DOWNLOAD_TOKEN_SRC" ]; then
		skip_loud "enterprise-client" "needs --live, --license <file> AND --download-token <file>; the customer path with a real entitlement was NOT exercised"
		return
	fi
	[ -r "$LICENSE" ] || die_blind "--license $LICENSE is not readable."
	# ⛔ EL TOKEN ES OBLIGATORIO AUNQUE LA LICENCIA ESTÉ INSTALADA, y la primera versión de esta pata
	# no lo pasaba: `upgrade --enterprise` rehúsa con «--token is required for --enterprise (it is in
	# your license/fulfillment email)» ANTES de mirar la licencia (`cmd/olivares/cmd_upgrade.go`), así
	# que la ÚNICA pata positiva del camino enterprise no podía salir verde NUNCA. Un rojo permanente
	# se aprende a ignorar, que es peor que no tener la pata. Medido ejerciendo el camino a mano.
	#
	# Se lee de un FICHERO, y eso mantiene el token fuera del argv de ESTE guion y del historial del
	# shell. ⚠ NO lo mantiene fuera de todo: abajo viaja en `--token` del cliente y en la URL de curl,
	# los dos visibles en `ps` y la segunda además en cualquier registro de acceso. Se dice porque el
	# rótulo anterior prometía secreto frente a `ps` y no lo daba; cerrarlo de verdad exige que el
	# cliente acepte el token por fichero o por entrada estándar, y eso es un cambio del cliente.
	[ -r "$DOWNLOAD_TOKEN_SRC" ] || die_blind "--download-token takes the PATH of a file holding the token; $DOWNLOAD_TOKEN_SRC is not readable."
	local token; token="$(tr -d '\n\r' < "$DOWNLOAD_TOKEN_SRC")"
	[ -n "$token" ] || die_blind "--download-token $DOWNLOAD_TOKEN_SRC is empty."

	local home; home="$(mktemp -d "${TMPDIR:-/tmp}/vbc-home.XXXXXX")" || die_blind "mktemp failed."
	VBC_TMPDIRS+=("$home")
	local out rc
	out="$("$BINARY" license install "$LICENSE" --data-dir "$home" 2>&1)"; rc=$?
	if [ "$rc" -ne 0 ]; then
		fail "enterprise-client/install" "the licence did not install: $(printf '%s' "$out" | tail -1)"
		return
	fi
	pass "enterprise-client/install: the licence installs and verifies against this build's own key"
	# ⛔ LOS ARGUMENTOS OPCIONALES SE MONTAN EN UN ARRAY, NO CON `${VAR:+--flag "$VAR"}`. Dentro de
	# esa expansión las comillas son CARACTERES, así que el valor llegaba entrecomillado y el cliente
	# lo rechazaba pidiendo justo la bandera que se le acababa de pasar — un rojo que parecía del
	# despliegue y era del guion. Medido ejerciendo la pata contra sandbox.
	local eargs=(upgrade --enterprise --check --data-dir "$home" --endpoint "$ENT_ENDPOINT" --token "$token")
	[ -n "$CURRENT_VERSION" ] && eargs+=(--current-version "$CURRENT_VERSION")
	[ -n "$OTA_PUBKEY" ] && eargs+=(--pubkey "$OTA_PUBKEY")
	out="$("$BINARY" "${eargs[@]}" 2>&1)"; rc=$?
	# ⛔ Y UNA POSITIVA AUTENTICADA CONTRA LA PUERTA, que es lo que le faltaba a todo el guion: hasta
	# aquí sólo se probaba que SIN credencial no hay bytes. Un despliegue que respondiera 403 a TODO
	# cumplía esa mitad. Se pide un RANGO de 1 KiB con el token del comprador —no los 99 MB: lo que
	# esta pata puede afirmar es que la puerta SIRVE a quien tiene derecho, no que los bytes casen con
	# el manifiesto, y eso último exige la descarga entera y no se finge aquí.
	# ⛔ TRES PETICIONES IDÉNTICAS QUE SÓLO SE DIFERENCIAN EN LA CREDENCIAL, y la primera versión de
	# esto no lo era: mandaba `Range` SÓLO en la autenticada y ninguna credencial en la otra, así que
	# «206 con token y 403 sin él» lo satisfacía un servidor que enrutara por `Range` e ignorara el
	# token por completo. El contraste lo reprodujo con un servidor que devolvía 206 VACÍO a cualquier
	# petición con rango y 403 a cualquiera sin él: tres `ok` y salida 0, sin autorizar ni entregar.
	#
	# Ahora varía UNA cosa: el token. Y la positiva exige lo que un 206 significa —`Content-Range` y
	# 1024 bytes servidos—, porque un 206 vacío no es una entrega. El 200 deja de aceptarse: a un
	# rango se responde 206, y admitir 200 era admitir «una página cualquiera».
	local q="os=linux&arch=amd64"
	local gate="${ENT_ENDPOINT%/}/download"
	local auth_meta anon_code bogus_code auth_code auth_size auth_range
	auth_meta="$(curl -sS -L -m 60 -r 0-1023 -o /dev/null -D - -w '\n%{http_code} %{size_download}' \
		"${gate}?token=${token}&${q}" 2>/dev/null)" || auth_meta=""
	anon_code="$(curl -sS -L -m 30 -r 0-1023 -o /dev/null -w '%{http_code}' "${gate}?${q}" 2>/dev/null)" || anon_code="000"
	bogus_code="$(curl -sS -L -m 30 -r 0-1023 -o /dev/null -w '%{http_code}' "${gate}?token=not-this-holders-token&${q}" 2>/dev/null)" || bogus_code="000"
	auth_code="$(printf '%s' "$auth_meta" | tail -1 | awk '{print $1}')"
	auth_size="$(printf '%s' "$auth_meta" | tail -1 | awk '{print $2}')"
	auth_range="$(grep -ci '^content-range:' <<<"$auth_meta" || true)"
	if [ -z "$auth_code" ] || [ "$auth_code" = "000" ] || [ "$anon_code" = "000" ] || [ "$bogus_code" = "000" ]; then
		skip_loud "enterprise-client/bytes" "the gate did not answer one of the three requests (transport): auth=${auth_code:-000} anon=${anon_code} bogus=${bogus_code}"
	elif [ "$auth_code" != "206" ]; then
		fail "enterprise-client/bytes" "the entitled ranged request answered ${auth_code} (want 206): a paid holder is not being served its bytes"
	elif [ "$auth_size" != "1024" ] || [ "$auth_range" -eq 0 ]; then
		fail "enterprise-client/bytes" "the 206 carried ${auth_size} byte(s) and ${auth_range} Content-Range header(s): a 206 without the range or without bytes is not a delivery"
	elif [ "$anon_code" != "403" ] || [ "$bogus_code" != "403" ]; then
		fail "enterprise-client/bytes" "the SAME request without a token answered ${anon_code} and with a bogus token ${bogus_code} (want 403 both): the credential is not what decides"
	else
		pass "enterprise-client/bytes: same ranged request — real token 206 with 1024 bytes and Content-Range, no token 403, bogus token 403"
	fi
	# ⛔ EL TERCER CONTROL NEGATIVO QUE EL ENCARGO PIDE, y que faltaba: «licencia revocada → rechazo».
	# Los otros dos estaban —sin derecho no hay bytes (arriba), manifiesto manipulado se rehúsa
	# (`channel-hermetic/tampered`)— y éste no, así que el guion decía «controles negativos» siendo
	# dos de tres.
	#
	# ⚠ Y NO SE FINGE CON UN TOKEN INVENTADO. `bogus_code` de arriba prueba otra cosa: que una cadena
	# que NUNCA fue una credencial se rechaza. Una credencial REVOCADA fue válida y dejó de serlo, que
	# es el camino donde vive el defecto interesante — una caché, un registro que no se re-consulta,
	# una firma que sigue verificando. Sin un token de esa clase, esto NO se ha medido: se dice y la
	# corrida sale 2, en vez de dejar que un `bogus` haga de coartada.
	if [ -z "$REVOKED_TOKEN_SRC" ]; then
		skip_loud "enterprise-client/revoked" "no --revoked-token: 'a revoked entitlement is refused' was NOT exercised (a bogus token proves something else)"
	else
		[ -r "$REVOKED_TOKEN_SRC" ] || die_blind "--revoked-token takes the PATH of a file holding the token; $REVOKED_TOKEN_SRC is not readable."
		local revoked; revoked="$(tr -d '\n\r' < "$REVOKED_TOKEN_SRC")"
		[ -n "$revoked" ] || die_blind "--revoked-token $REVOKED_TOKEN_SRC is empty."
		if [ "$revoked" = "$token" ]; then
			die_blind "--revoked-token and --download-token hold the SAME credential: this control would compare a token with itself."
		fi
		# El CUERPO se guarda además del código, y no por adorno: un 403 que no dice nada y un 403 que
		# dice «purchase refunded <instante>» son el mismo número y dos productos distintos para quien
		# lo recibe. No se exige el texto —un despliegue deny-closed puede rehusar en seco sin estar
		# roto— pero sí se IMPRIME, porque es la evidencia de POR QUÉ se rehusó, y sin ella la pata
		# no distingue una revocación de una caducidad ni de un token que nunca existió.
		local rev_body rev_code
		rev_body="$(curl -sS -L -m 30 -r 0-1023 -w '\n%{http_code}' "${gate}?token=${revoked}&${q}" 2>/dev/null)" || rev_body=""
		rev_code="$(printf '%s' "$rev_body" | tail -1)"
		[ -n "$rev_code" ] || rev_code="000"
		local rev_why; rev_why="$(printf '%s' "$rev_body" | sed '$d' | head -1 | cut -c1-160)"
		if [ "$rev_code" = "000" ]; then
			skip_loud "enterprise-client/revoked" "the gate did not answer the revoked-credential request (transport), so the revocation was NOT measured"
		elif [ "$rev_code" = "403" ]; then
			pass "enterprise-client/revoked: the SAME ranged request with a REVOKED credential is refused (403)${rev_why:+ — ${rev_why}}"
		else
			fail "enterprise-client/revoked" "a REVOKED entitlement answered ${rev_code} to the same request the live one answers 206: revocation is not reaching the download gate"
		fi
	fi
	if [ "$rc" -eq 0 ]; then
		pass "enterprise-client/check: the licensed client reads and VERIFIES its channel manifest"
	elif grep -qiE 'no such host|connection refused|network is unreachable|i/o timeout|timeout|EOF|tls: |certificate' <<<"$out"; then
		# ⛔ ESTA RAMA FALTABA, y su ausencia convertía un endpoint caído en «el cliente no pudo
		# verificar el canal» — un HALLAZGO contra el despliegue por un fallo de transporte. La pata
		# `channel-live` ya distinguía las dos cosas; ésta no, y era la misma clase de error que este
		# guion existe para no cometer.
		skip_loud "enterprise-client/check" "the endpoint did not answer (transport), so the licensed path was NOT measured: $(printf '%s' "$out" | tail -1)"
	elif grep -q 'purchase refunded' <<<"$out"; then
		# Un derecho reembolsado NO es una cadena rota: es la puerta funcionando. Se nombra para que
		# una compra de limpieza no se lea como rojo.
		skip_loud "enterprise-client/check" "this entitlement is REFUNDED and the gate cut it, which is the gate working: $(printf '%s' "$out" | tail -1)"
	else
		fail "enterprise-client/check" "the licensed client could not verify the enterprise channel (rc=$rc): $(printf '%s' "$out" | tail -1)"
	fi
}

OTA_PUBKEY_ARG=""

say "binary=$BINARY live=$LIVE endpoint=${ENDPOINT:-<client default>} enterprise=${ENT_ENDPOINT}"
want_leg channel-hermetic && leg_channel_hermetic
want_leg channel-live && leg_channel_live
want_leg enterprise-denyclosed && leg_enterprise_denyclosed
want_leg enterprise-gate && leg_enterprise_gate
want_leg enterprise-client && leg_enterprise_client

[ "$runs" -gt 0 ] || die_blind "no leg ran (--only ${ONLY} matched nothing): a run that measured nothing is not clean."

# Un HALLAZGO gana a una ceguera: es un hecho medido, y que otra pata no se pudiera mirar no lo
# borra. Pero el resumen tiene que decir las DOS cosas, o quien lee un rc=1 supone que la cadena
# entera se midió y que sólo falló eso.
if [ "$findings" -gt 0 ]; then
	say "FINDINGS: ${findings} of ${runs} checks failed —${FAILED_LEGS}"
	[ "$partial" -gt 0 ] && say "  …and ${partial} check(s) were NOT measured either (see SKIPPED above): this run is a finding AND incomplete."
	exit "$RC_FINDING"
fi
# ⛔ UN RESUMEN DE CADENA SÓLO PUEDE IMPRIMIRLO UNA CORRIDA QUE HAYA MIRADO LA CADENA. Con `--only`
# se ejerce UNA pata a propósito: eso es legítimo y sale 0, pero con su propio rótulo. Reutilizar
# «all measured with the client itself» para una pata suelta es exactamente el verde de más que este
# guion existe para no dar.
# ⛔ Y `partial` SE CONSULTA ANTES QUE `--only`, no después. Al revés —que es como estaba— una pata
# seleccionada que declaraba «NO HE PODIDO MIRAR» en una de sus mitades salía 0 con su rótulo de
# «clean», porque el retorno de `--only` estaba por encima del de parcialidad. Lo reprodujo el
# contraste: `--only enterprise-client` contra un endpoint muerto, rc=0.
if [ "$partial" -gt 0 ]; then
	if [ -n "$ONLY" ]; then
		say "PARTIAL: selected leg '${ONLY}' passed ${runs} check(s) and ${partial} were NOT measured (see SKIPPED above): exit 2."
	else
		say "PARTIAL: ${runs} check(s) passed and ${partial} leg(s) were NOT measured (see SKIPPED above). This is not a verified chain: exit 2."
	fi
	exit "$RC_BLIND"
fi
if [ -n "$ONLY" ]; then
	say "OK — selected leg '${ONLY}' clean over ${runs} check(s). THE CHAIN WAS NOT EVALUATED."
	exit "$RC_CLEAN"
fi
say "OK — ${runs} checks, all measured with the client itself."
exit "$RC_CLEAN"
