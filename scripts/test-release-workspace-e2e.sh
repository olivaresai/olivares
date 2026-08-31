#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-release-workspace-e2e.sh — the release steps IN ORDER, in a tree built from this
# repository's own committed contents, under this repository's own `.gitignore`.
#
# WHY IT EXISTS, and it is the most expensive lesson of this branch. The unit batteries build
# their fixture and hand it a `.gitignore` chosen by the battery. One of them wrote
# `ota-staging/ ota-dist/ dist/` and called it "exactly as the real repository ignores its
# build directories". The real file has one matching line, `/dist/`. That invented ignore made
# 71/71 and 28/28 pass over a workflow that could not complete a single release:
#
#   phase 1 writes release-commit.txt at the root, then the producer's own check answers
#     `?? release-commit.txt` and refuses — every security release denied;
#   phase 2 downloads into ota-dist/, then the ceremony answers `?? ota-dist/` and refuses —
#     every stable ceremony denied.
#
# Both were measured against the real ignore file (the model, #638@1fc91ed8). A fixture more
# forgiving than production is not a fixture, so this battery takes the tree from
# `git archive HEAD` — real `.gitignore`, real paths — and runs the REAL sequence: the writes
# and downloads the workflow performs, in the order it performs them, before each guard.
#
# It asserts both directions of the contract:
#   · the exact generated artifacts are ALLOWED — the positive flow completes;
#   · anything else untracked, and any modified or staged TRACKED file, is DENIED.
#
# Hermetic: stub `go`/`gh`, a throwaway tree, no network, nothing uploaded.
#
# NO `set -e` (battery reports through check()).
set -uo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
_olivares_git_env="${ROOT}/scripts/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

# OLIVARES_WSE2E_WORKFLOW / OLIVARES_WSE2E_GORELEASER override the two versioned files this
# battery reads, exactly like OLIVARES_SIGPROD_WORKFLOW and OLIVARES_SIGGUARD_WORKFLOW in the
# sibling batteries: the red-first proof points them at the pre-fix tree, and a case that
# cannot be shown RED against the defect it names is not a witness of anything. They are read
# ONLY as the text under test; nothing the guards execute is configurable through them.
WF_REAL="${OLIVARES_WSE2E_WORKFLOW:-${ROOT}/.github/workflows/release.yml}"
GORELEASER_CFG="${OLIVARES_WSE2E_GORELEASER:-${ROOT}/.goreleaser.yaml}"
# THE HARNESS MUTATES A COPY, it does not open a seam in production. The guards name their
# tools by absolute path with no override — an env override would be exactly the hole they
# exist to close, since an earlier action can set it through GITHUB_ENV. So the battery
# rewrites `/usr/bin` to its own stub directory in a COPY of the workflow and extracts the
# blocks from that; production keeps the literal path.
WF=""
_ln() { command grep -n "$1" "$WF_REAL" | head -1 | cut -d: -f1; }
# ⛔ EL RESPALDO DE UN SOLO SALTO NO BASTA, y costo un rojo mal atribuido. Esta bateria ya probaba
# la ejecucion y caia a `$ROOT/.tmpexec`; en un runner donde **/tmp Y el checkout** estan montados
# noexec —ambos bajo `_work`— los dos candidatos fallan. El sintoma fue
#   ERROR: could not read a version from /tmp/olivares-wse2e.XXXX/trusted/gh: ''
# que se lee como «la logica del suelo de version de gh esta rota». No lo estaba: el stub no se
# podia EJECUTAR. Lo destapo el volcado del ultimo paso que esta misma bateria imprime ahora al
# fallar; sin el, el rojo señalaba al sitio equivocado.
#
# lib/exec-workdir.sh prueba SEIS candidatos creando y ejecutando un binario de verdad, y si
# ninguno sirve contesta 2 —«no he podido mirar»— en vez de correr midiendo el montaje.
# shellcheck source=lib/exec-workdir.sh
. "$ROOT/scripts/lib/exec-workdir.sh" || {
	echo "release-workspace-e2e: NO HE PODIDO MIRAR: falta scripts/lib/exec-workdir.sh" >&2
	exit 2
}
WORK="$(olivares_pick_exec_workdir olivares-wse2e)" || {
	echo "release-workspace-e2e: NO HE PODIDO MIRAR: ningun candidato permite crear y EJECUTAR un binario" >&2
	echo "                       (probados OLIVARES_GATE_BINDIR, RUNNER_TEMP, TMPDIR, /tmp, HOME y el scratch)" >&2
	exit 2
}
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0
failed_names=()
check() {
	# ⛔ SE CONSUME AL PRINCIPIO, pase o falle. La primera version limpiaba `out_fresco` solo en
	# la rama de fallo, asi que una casilla que PASA dejaba la marca puesta y la siguiente
	# —aunque no ejecutase ningun paso— volvia a adornarse con la salida ajena. Es el mismo
	# defecto que venia a arreglar, un eslabon mas abajo.
	local fresco="${out_fresco:-0}"
	out_fresco=0
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok  %-58s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		failed_names+=("$1")
		printf 'FAIL  %-58s %s\n' "$1" "$2"
		# Y lo que fallo, DICHO. Esta bateria dio "pass=123 fail=1" en un runner de CI el
		# 2026-08-18 sin una sola linea del porque, mientras en local daba 124/0: un rojo que
		# no nombra a su culpable obliga a reproducir el entorno entero para averiguarlo, y
		# el entorno era justo lo que diferia. Volcamos el codigo y la salida del ultimo paso
		# ejecutado — es lo unico que distingue "el codigo esta mal" de "aqui falta un binario".
		# ⛔ SOLO SI ESA SALIDA ES DE ESTA COMPROBACION. La primera version volcaba `$out`
		# siempre, y `$out` es del ULTIMO run_step — que para las casillas que hacen `grep`
		# sobre un fichero versionado no tiene NADA que ver. El 2026-08-19 eso me costo una
		# investigacion: un fallo de `grep` sobre el workflow salio adornado con
		# «ERROR: the build left the checkout modified. M scripts/release-ota-channel.sh»,
		# de un paso anterior, y mande a mirar un fichero que no pintaba nada.
		#
		# Un contexto que no es del fallo es PEOR que no dar contexto: el que no lo hay se
		# nota, y el ajeno se cree.
		if [ "$fresco" = "1" ]; then
			printf '      ultimo paso: rc=%s\n' "${rc:-<sin rc>}"
			if [ -n "${out:-}" ]; then
				printf '%s\n' "$out" | tail -n 12 | sed 's/^/      | /'
			else
				printf '      | (el paso no escribio nada)\n'
			fi
		else
			printf '      (esta casilla no ejecuto ningun paso: no hay salida que ensenar,\n'
			printf '       y la del paso anterior seria de otra cosa)\n'
		fi
	fi
}

# Literal output assertions must not feed `grep -q` through a pipe. With `pipefail`, grep closes
# the reader as soon as it finds the text and the producer can then return 141: the assertion says
# "missing" precisely when the text was present. Small strings only make that race rarer; this
# repository has measured it with 94 bytes. The fifth main red on 2026-08-29 landed on one of these
# predicates (the post-build dirty guard), while isolated reruns passed. Keep rc and message as two
# explicit claims, but decide the message in-process so scheduler timing cannot change the answer.
contains_literal() { # contains_literal <haystack> <literal needle>
	case "$1" in
	*"$2"*) return 0 ;;
	*) return 1 ;;
	esac
}

echo "release workspace E2E — the real sequence, under the repository's own .gitignore"

# WITNESS FOR THE HARNESS, not for the workflow: putting any output assertion back behind a
# `grep -q` pipe must fail deterministically here, instead of waiting for the scheduler to make the
# race fire. Join a continued pipeline to its next line so formatting cannot hide the old shape.
_oracle_pipes="$(awk '
	/^[[:space:]]*#/ { next }
	{
		line = carry $0
		if (line ~ /\|[[:space:]]*(command[[:space:]]+)?grep[[:space:]]+-[[:alpha:]]*q/) n++
		if ($0 ~ /\|[[:space:]]*$/) carry = $0 " "; else carry = ""
	}
	END { print n + 0 }
' "$ROOT/scripts/test-release-workspace-e2e.sh")"
[ "$_oracle_pipes" -eq 0 ]
check "the battery has no timing-sensitive grep-q oracle" "zero boolean pipes" $?

make_wf_copy() { # rewrite the trusted-bin literal to the battery's stub dir
	WF="$WORK/release-under-test.yml"
	sed -e 's#TRUSTED_BIN="/usr/bin"#TRUSTED_BIN="'"$WORK"'/trusted"#' \
		-e 's#/opt/hostedtoolcache/\* | /usr/local/bin/\*#'"$WORK"'/bin/* | /usr/local/bin/*#' \
		"$WF_REAL" >"$WF"
}
extract() { # extract <step name>
	awk -v step="      - name: $1" '
		$0 == step { instep = 1; next }
		instep && /^      - name: / { instep = 0 }
		instep && $0 == "        run: |" { inrun = 1; next }
		inrun {
			if ($0 == "") { print ""; next }
			if ($0 !~ /^          /) { inrun = 0; instep = 0; next }
			print substr($0, 11)
		}
	' "$WF"
}
mkdir -p "$WORK/trusted"
make_wf_copy
command grep -q "TRUSTED_BIN=\"$WORK/trusted\"" "$WF"
check "the battery runs a MUTATED COPY of the workflow" "no env seam in production" $?
_seam="$(command grep -vE '^[[:space:]]*#' "$WF_REAL" | command grep -c 'OLIVARES_TRUSTED_BIN' || true)"
[ "${_seam:-0}" -eq 0 ]
check "production names its tools with no override" "not configurable by the attacker" $?

# THE DIGEST IS PINNED IN THE VERSIONED FILE, not in a repo variable. `gh variable get`
# answered NOT FOUND for OLIVARES_COSIGN_SHA256, so the ceremony denied itself; and an
# admin-mutable variable would not pin anything anyway — whoever can swap the binary can swap
# the value that vouches for it.
_var_use="$(command grep -vE '^[[:space:]]*#' "$WF_REAL" | command grep -c 'vars.OLIVARES_COSIGN_SHA256' || true)"
[ "${_var_use:-0}" -eq 0 ]
check "the cosign digest does not come from a repo variable" "no admin-mutable pin" $?
_pin="$(command grep -oE 'COSIGN_EXPECTED_SHA256: [0-9a-f]{64}' "$WF_REAL" | head -1)"
[ -n "$_pin" ]
check "a 64-hex cosign digest is pinned inline" "versioned precondition" $?
# and it is the digest this repository already approved for that artefact
_pinval="${_pin##* }"
command grep -q "^${_pinval}  cosign-linux-amd64\$" "$ROOT/scripts/assert-cosign-binary.sh"
check "the inline pin is the repository's approved digest" "one source of truth" $?

# EVERY WINDOW HAS A GUARD BEFORE ITS FIRST `run`. The single "before any script" guard sat
# after setup-go, setup-node, pnpm/action-setup, `install go-task` and two preflight steps
# that already read .node-version, the lockfile and the Taskfile and executed against the
# checkout. Asserted on the YAML, since running a block cannot see what precedes it.
ck="$(_ln '      - uses: actions/checkout@')"
gg1="$(_ln '      - name: phase 1 guard 1')"
sg="$(_ln '      - uses: actions/setup-go@')"
gg2="$(_ln '      - name: phase 1 guard 2')"
fr="$(_ln '      - name: install go-task (pinned)')"
[ -n "$ck" ] && [ -n "$gg1" ] && [ "$ck" -lt "$gg1" ] && [ "$gg1" -lt "$sg" ]
check "a guard sits between checkout and the first action that reads it" "window 0" $?
[ -n "$gg2" ] && [ -n "$fr" ] && [ "$sg" -lt "$gg2" ] && [ "$gg2" -lt "$fr" ]
check "a guard sits between the setup actions and the first run" "window 1" $?
# and the identity is pinned in the file, not in a repo variable
_idvar="$(command grep -vE '^[[:space:]]*#' "$WF_REAL" | command grep -c 'vars.OLIVARES_CERT_IDENTITY' || true)"
[ "${_idvar:-0}" -eq 0 ]
check "the signing identity does not come from a repo variable" "no admin-mutable identity" $?

extract "generate unsigned security OTA channel manifest (deny-closed)" >"$WORK/producer.sh"
extract "attach verified OTA signature to the draft release" >"$WORK/guard.sh"
extract "assert the checkout is untouched before anything consumes it (phase 1)" >"$WORK/early1.sh"
extract "assert the checkout is untouched before phase 1 runs any script" >"$WORK/prescript1.sh"
extract "phase 1 guard 1 — straight after checkout" >"$WORK/g1.sh"
extract "phase 1 guard 2 — after the setup actions, before the first script" >"$WORK/g2.sh"
extract "bind this checkout to phase 1 before any tracked code runs" >"$WORK/bind2.sh"
extract "download draft manifest, signed checksums and every published archive" >"$WORK/download2.sh"
extract "assert GoReleaser left the checkout intact before anything is uploaded" >"$WORK/postbuild1.sh"
extract "assert the checkout is untouched before anything consumes it (phase 2)" >"$WORK/early2.sh"
[ -s "$WORK/producer.sh" ] && [ -s "$WORK/guard.sh" ] && [ -s "$WORK/early1.sh" ] &&
	[ -s "$WORK/early2.sh" ] && [ -s "$WORK/prescript1.sh" ] && [ -s "$WORK/bind2.sh" ]
check "all six real blocks were extracted" "not empty payloads" $?

# EVERY WINDOW, not just the last one. Phase 1 runs two TRACKED scripts between the setup
# actions and the docker actions, so "after the last uses:" is a property of a window and
# there must be a guard per window; phase 2 must BIND identity before any tracked code runs.
s1="$(_ln '      - name: assert the checkout is untouched before phase 1 runs any script')"
c1="$(_ln 'run: bash scripts/assert-cosign-binary.sh --isolate')"
[ -n "$s1" ] && [ -n "$c1" ] && [ "$s1" -lt "$c1" ]
check "phase 1: a guard precedes the FIRST tracked script" "one guard per window" $?
bd="$(_ln '      - name: bind this checkout to phase 1 before any tracked code runs')"
# The FIRST occurrence after the binding step — the script appears four times across the
# workflow's jobs, and picking "the second one" landed in a different job entirely.
c2="$(command grep -n 'run: bash scripts/assert-cosign-binary.sh --isolate' "$WF_REAL" |
	awk -F: -v b="$bd" '$1 > b { print $1; exit }')"
[ -n "$bd" ] && [ -n "$c2" ] && [ "$bd" -lt "$c2" ]
check "phase 2: identity is bound BEFORE the first tracked script" "no unbound execution" $?

# THE EARLY GUARD MUST COME BEFORE THE FIRST CONSUMER, or it guards nothing. Asserted on the
# YAML line order, because no amount of running the blocks can see where they sit.
e1="$(_ln '      - name: assert the checkout is untouched before anything consumes it (phase 1)')"
b1="$(_ln 'uses: goreleaser/goreleaser-action')"
p1="$(_ln '      - name: generate unsigned security OTA channel manifest')"
e2="$(_ln '      - name: assert the checkout is untouched before anything consumes it (phase 2)')"
d2="$(_ln '      - name: download draft manifest')"
g2="$(_ln '      - name: attach verified OTA signature')"
[ -n "$e1" ] && [ -n "$b1" ] && [ "$e1" -lt "$b1" ] && [ "$b1" -lt "$p1" ]
check "phase 1: early guard precedes the build, which precedes the producer" "two windows" $?
[ -n "$e2" ] && [ -n "$d2" ] && [ "$e2" -lt "$d2" ] && [ "$d2" -lt "$g2" ]
check "phase 2: early guard precedes the download, which precedes the guard" "two windows" $?

# --- the tree: this repository's committed contents, with its own .gitignore ---------------
TREE="$WORK/tree"
mkdir -p "$TREE" || exit 1
(cd "$ROOT" && git archive HEAD) | tar -x -C "$TREE" || exit 1
# `git archive HEAD` is the COMMITTED tree, so a script that exists only in the working copy
# would be missing here and section I would test a file production does not have. The two the
# release path now runs from the checkout are overlaid from the working copy — the bytes under
# test — and committed with the fixture below, which is also what keeps that tree clean.
for _s in release-commit-evidence.sh release-attach-stable-pair.sh; do
	cp "$ROOT/scripts/$_s" "$TREE/scripts/$_s" || exit 1
done
[ -f "$TREE/.gitignore" ] && ! command grep -qE '^/?(ota-dist|ota-staging|release-commit)' "$TREE/.gitignore"
check "the fixture uses the REAL .gitignore, which ignores none of these" "no invented rule" $?
(
	cd "$TREE" || exit 1
	git init -q .
	git config user.email e2e@example.invalid
	git config user.name e2e
	git config commit.gpgsign false
	mkdir -p release/advisories dist
	: >dist/checksums.txt
	git add -A >/dev/null 2>&1
	git commit -q -m "release commit"
	git tag v26.8.0
) || exit 1
OID="$(git -C "$TREE" rev-parse HEAD)"
[ -z "$(git -C "$TREE" status --porcelain)" ]
check "the fixture starts clean under the real ignore rules" "baseline" $?

mkdir -p "$WORK/bin" || exit 1
cat >"$WORK/bin/go" <<'EOF'
#!/usr/bin/env bash
printf 'go %s\n' "$*" >>"${STUB_LOG}"
exit "${STUB_GO_RC:-0}"
EOF
cat >"$WORK/bin/gh" <<'EOF'
#!/usr/bin/env bash
# ⛔ LA TRAZA NO PUEDE MATAR AL STUB. Esto era `>>"${STUB_LOG}"` a secas, y con STUB_LOG sin
# definir bash responde «ambiguous redirect» y SALE — antes de imprimir la version. El sintoma es
# entonces `could not read a version from …/gh: ''`, que se lee como que la logica del suelo de
# version esta rota; el suelo nunca llego a recibir nada que juzgar. El bloque del workflow corre
# con el entorno fregado y reenvia solo variables concretas, asi que «STUB_LOG no llega» es una
# condicion normal alli y no un accidente. Un stub cuya SALIDA depende de que su TRAZA funcione no
# es un stub: es un segundo modo de fallo.
if [ -n "${STUB_LOG:-}" ]; then printf 'gh %s\n' "$*" >>"${STUB_LOG}" 2>/dev/null || true; fi
case "${1:-} ${2:-}" in
"--version "*|"--version")
	# MODELLED because the ceremony now READS it. gh is identified by a version floor
	# (INT-22, M78), so a stub that cannot answer --version makes every guarded step
	# refuse — which is exactly what happened the first time this landed, and it took
	# the whole binding section down with it. The shape is the real one, first line
	# included, because the parse is what the floor depends on.
	printf 'gh version %s (2026-07-31)\n' "${STUB_GH_VERSION:-2.97.0}"
	printf 'https://github.com/cli/cli/releases/tag/v%s\n' "${STUB_GH_VERSION:-2.97.0}"
	exit 0
	;;
"release view")
	printf 'DRAFT true\nIMMUTABLE false\n'
	# El par que la fase 1 firma sobre el manifiesto (ordenes 36/37): sin el, la fase 2 no
	# puede probar que los bytes que va a firmar con la clave OTA son los que ella produjo.
	printf 'ASSET stable-manifest.json\nASSET stable-manifest.json.sig\n'
	printf 'ASSET stable-manifest.json.pipeline.sig\nASSET stable-manifest.json.pipeline.pem\n'
	exit 0
	;;
"release download")
	# THE REAL REFUSAL, modelled. `gh release download` will not overwrite an existing
	# destination unless --clobber is given, and the ceremony asked for the four evidence
	# files twice into the same directory — the second time without it. A stub that always
	# succeeds hides that, which is exactly how it survived: the fixture's own `cp` made the
	# files appear without the tool ever objecting.
	_dir=""; _prev=""; _clobber=0; _pats=""
	for _a in "$@"; do
		[ "$_prev" = "--dir" ] && _dir="$_a"
		[ "$_a" = "--clobber" ] && _clobber=1
		[ "$_prev" = "--pattern" ] && _pats="$_pats $_a"
		_prev="$_a"
	done
	if [ -n "$_dir" ]; then
		mkdir -p "$_dir"
		for _pat in $_pats; do
			case "$_pat" in *"*"*) continue ;; esac
			if [ -e "$_dir/$_pat" ] && [ "$_clobber" -ne 1 ]; then
				echo "gh: ${_pat} already exists (use --clobber to overwrite)" >&2
				exit 1
			fi
		done
		# A download FETCHES the release's bytes; in this fixture the pre-seeded files ARE
		# those bytes, so an existing one is left alone. Truncating it here made the step
		# "succeed" while destroying the evidence it had just authenticated.
		for _pat in $_pats; do
			case "$_pat" in *"*"*) continue ;; esac
			[ -e "$_dir/$_pat" ] || : >"$_dir/$_pat"
		done
		[ -f "$_dir/stable-manifest.json.sig" ] || : >"$_dir/stable-manifest.json.sig"
		# ⛔ ESTOS DOS SE SIEMBRAN CON CONTENIDO, y no es un detalle del fixture: la fase 2 los
		# comprueba con `[ -s ]`, no con `[ -e ]`, porque un fichero de firma VACIO es
		# exactamente lo que produce un `gh release download` de un asset que no existe — y
		# aceptarlo dejaria pasar un borrador sin atadura de bytes diciendo que la tiene.
		# Sembrarlos vacios aqui haria rojo el caso por la razon equivocada.
		# ⛔ SOLO SI SE PIDIERON, y esto lo aprendi rompiendolo: sembrarlos en TODA descarga
		# los dejaba puestos ya en la primera —que no los pedia—, y entonces la segunda, que si
		# los pide, chocaba contra lo que yo mismo habia dejado: «already exists (use --clobber)».
		# El fixture modela `gh`, y `gh` solo escribe lo que se le pide.
		for _f in stable-manifest.json.pipeline.sig stable-manifest.json.pipeline.pem; do
			case " $_pats " in
			*" $_f "*) [ -s "$_dir/$_f" ] || printf 'fixture-%s\n' "$_f" >"$_dir/$_f" ;;
			esac
		done
	fi
	exit 0
	;;
"release upload")
	# THE REAL REFUSAL, MODELLED — the same reason the download leg above is modelled.
	# `gh release upload` refuses a name the release already carries unless --clobber is
	# given, and the ceremony used to pass --clobber at every upload. A stub that always
	# succeeds cannot tell create-only from overwrite, so it would report the campaign
	# green whether the flag was there or not: blind to the very property it holds.
	#
	# Uploaded names accumulate in a file, so a SECOND upload of the same asset is refused
	# exactly as the real tool refuses it.
	_up_clobber=0
	_seen="${STUB_LOG%/*}/uploaded-assets.txt"
	: >>"$_seen"
	for _a in "$@"; do
		[ "$_a" = "--clobber" ] && _up_clobber=1
	done
	for _a in "$@"; do
		case "$_a" in release|upload|--*|v[0-9]*) continue ;; esac
		_base="${_a##*/}"
		if command grep -qxF "$_base" "$_seen" && [ "$_up_clobber" -ne 1 ]; then
			echo "gh: ${_base} already exists (use --clobber to overwrite)" >&2
			exit 1
		fi
	done
	for _a in "$@"; do
		case "$_a" in release|upload|--*|v[0-9]*) continue ;; esac
		printf '%s\n' "${_a##*/}" >>"$_seen"
	done
	exit 0
	;;
esac
exit "${STUB_GH_RC:-0}"
EOF
cat >"$WORK/bin/cosign" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
version) printf 'GitVersion:    %s\n' "${STUB_COSIGN_VERSION:-v2.6.4}"; exit 0 ;;
verify-blob) printf 'cosign %s\n' "$*" >>"${STUB_LOG}"; exit "${STUB_COSIGN_RC:-0}" ;;
esac
exit 0
EOF
chmod +x "$WORK/bin/go" "$WORK/bin/gh" "$WORK/bin/cosign" || exit 1
# A "trusted bin" the block can be pointed at: git and sha256sum are the real ones, gh is the
# stub. The block resolves its tools there by absolute path, so this is the only way to run
# the REAL block without a network or a live gh.
mkdir -p "$WORK/trusted" || exit 1
ln -sf /usr/bin/git "$WORK/trusted/git"
ln -sf /usr/bin/sha256sum "$WORK/trusted/sha256sum"
ln -sf /usr/bin/wc "$WORK/trusted/wc"
ln -sf "$WORK/bin/gh" "$WORK/trusted/gh"

# ⛔ Y SE COMPRUEBA QUE EL SEÑUELO ARRANCA, AQUI, ANTES DE MEDIR NADA CON EL. Medido el
# 2026-08-19 en ci-runner-2: el bloque real respondio
#
#   ERROR: could not read a version from …/trusted/gh: ''
#
# y eso se lee como «la logica del suelo de version de gh esta rota». La cabecera de esta
# bateria YA documenta que ese sintoma significa lo contrario —que el stub no se pudo
# EJECUTAR— y aun asi el rojo volvio a salir con el rotulo equivocado, porque nadie lo
# comprobaba: se comprobaba el DIRECTORIO (lib/exec-workdir.sh prueba su propio binario) y
# no ESTE fichero, que llega por un enlace simbolico y por una copia distinta.
#
# «Mi propio señuelo no arranca» es NO HE PODIDO MIRAR (2), no un hallazgo (1). Un 1 manda a
# arreglar codigo que esta bien; un 2 manda a mirar la maquina, que es donde esta el problema.
# Y dice POR QUE, con las tres preguntas separadas, porque «no arranca» tiene tres causas
# distintas y solo una se ve en el modo del fichero.
if [ ! -x "$WORK/bin/gh" ]; then
	echo "release-workspace-e2e: NO HE PODIDO MIRAR: el stub $WORK/bin/gh no tiene bit de" >&2
	echo "                       ejecucion tras chmod +x (modo $(stat -c '%a' "$WORK/bin/gh" 2>/dev/null))." >&2
	exit 2
fi
# Se ejercita la invocacion POR DEFECTO **y la del override**, porque no son el mismo camino:
# el stub imprime `${STUB_GH_VERSION:-2.97.0}`, y el 2026-08-19 en ci-runner-4 la que fallo fue
# EXACTAMENTE la del override —la casilla del suelo de version, con STUB_GH_VERSION=2.39.0—
# mientras la de por defecto pasaba en la casilla siguiente. Comprobar solo una deja el rojo
# diciendo «could not read a version», que se lee como que la logica del suelo esta rota.
_vov="$(STUB_GH_VERSION=2.39.0 "$WORK/trusted/gh" --version 2>&1)"; _vovrc=$?
case "$_vov" in
*"gh version 2.39.0"*) : ;;
*)
	echo "release-workspace-e2e: NO HE PODIDO MIRAR: el stub gh no honra STUB_GH_VERSION en este" >&2
	echo "                       runner, asi que la casilla del suelo de version no mide el suelo." >&2
	echo "                       rc=$_vovrc  salida='$_vov'" >&2
	echo "                       por defecto: $("$WORK/trusted/gh" --version 2>&1 | head -1)" >&2
	echo "                       STUB_LOG='${STUB_LOG:-<sin definir>}'" >&2
	echo "                       interprete: $(head -1 "$WORK/bin/gh")" >&2
	exit 2
	;;
esac
_v="$("$WORK/trusted/gh" --version 2>&1)"; _vrc=$?
case "$_v" in
*"gh version"*) : ;;
*)
	echo "release-workspace-e2e: NO HE PODIDO MIRAR: el stub gh de ESTA bateria no arranca en" >&2
	echo "                       este runner, asi que nada de lo que mida con el seria un veredicto" >&2
	echo "                       sobre el codigo de release." >&2
	echo "                       rc=$_vrc  salida='$_v'" >&2
	echo "                       directo:   $("$WORK/bin/gh" --version 2>&1 | head -1) (rc=$?)" >&2
	echo "                       enlace:    $(readlink -f "$WORK/trusted/gh" 2>&1)" >&2
	echo "                       modos:     $(stat -c '%a %n' "$WORK" "$WORK/bin" "$WORK/bin/gh" "$WORK/trusted" 2>&1 | tr '\n' ' ')" >&2
	echo "                       montaje:   $(findmnt -no TARGET,OPTIONS --target "$WORK" 2>&1 | head -1)" >&2
	exit 2
	;;
esac

n=0
rc=0
out=""
run_step() { # run_step <script> [VAR=VAL …]
	n=$((n + 1))
	local script="$1"
	shift
	(cd "$TREE" && env -i PATH="${PATH_OVERRIDE:-$WORK/trusted:$WORK/bin:/usr/bin:/bin}" HOME="$WORK" \
		GITHUB_WORKSPACE="$TREE" GITHUB_REPOSITORY="olivaresai/olivares" \
		RUNNER_TOOL_CACHE="$WORK/toolcache" \
		RELEASE_TAG="v26.8.0" RELEASE_VERSION="26.8.0" RELEASE_COMMIT="$OID" \
		MANIFEST_EXPIRES_IN="2160h" GH_TOKEN="stub" STUB_LOG="$WORK/log.$n" \
		COSIGN_EXPECTED_VERSION="v2.6.4" \
		CERT_IDENTITY_REGEXP='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
		CERT_OIDC_ISSUER="https://token.actions.githubusercontent.com" \
		COSIGN_EXPECTED_SHA256="$(sha256sum "$WORK/bin/cosign" | cut -d' ' -f1)" \
		GITHUB_SHA="$OID" "$@" \
		bash "$script") >"$WORK/out.$n" 2>&1
	rc=$?
	out="$(cat "$WORK/out.$n")"
	out_fresco=1
}
# What phase 1 really leaves in the tree before the producer step. The bytes are written by
# GoReleaser's own first `before` hook (scripts/release-commit-evidence.sh) — NOT by a workflow
# step any more, which is what P0-A was: written before the action, the file made the tree dirty
# and GoReleaser refused to release at all. Section I below runs the real hook and the real
# ordering; here the file is simply seeded, because these cases are about the guards.
write_phase1_evidence() { printf '%s\n' "$OID" >"$TREE/release-commit.txt"; }
# what phase 2 really does before the guard step
seed_phase2_download() {
	mkdir -p "$TREE/ota-dist"
	printf '%s\n' "$OID" >"$TREE/ota-dist/release-commit.txt"
	printf '%s  release-commit.txt\n' \
		"$(sha256sum "$TREE/ota-dist/release-commit.txt" | cut -d' ' -f1)" \
		>"$TREE/ota-dist/checksums.txt"
	# NOT stable-manifest.json: the binding step does not fetch it, so at this point in the
	# real sequence it is not in ota-dist yet. Pre-creating it made the second download
	# collide on a file production would not have had — the fixture inventing the very
	# condition under test, in the wrong place.
}
reset_tree() {
	rm -rf "$TREE/ota-dist" "$TREE/ota-staging" "$TREE/release-commit.txt"
	(cd "$TREE" && /usr/bin/git checkout -q -- . 2>/dev/null; /usr/bin/git reset -q 2>/dev/null)
	rm -f "$TREE/scripts/injected-helper.sh"
}

# --- A · THE POSITIVE FLOW, in order -------------------------------------------------------
reset_tree
printf 'CVE-2026-0001\n' >"$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"
write_phase1_evidence
[ -n "$(git -C "$TREE" status --porcelain)" ]
check "phase 1's own write really does dirty the tree" "the trap is present" $?
run_step "$WORK/producer.sh"
[ "$rc" -eq 0 ]
check "the PRODUCER completes after phase 1 wrote its evidence" "generated exact allow" $?
command grep -q 'release manifest' "$WORK/log.$n"
check "and it actually produced" "not a silent skip" $?

reset_tree
rm -f "$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"
seed_phase2_download
[ -n "$(git -C "$TREE" status --porcelain)" ]
check "phase 2's download really does dirty the tree" "the trap is present" $?
run_step "$WORK/guard.sh"
[ "$rc" -eq 0 ]
check "the CEREMONY completes after phase 2 downloaded" "generated exact allow" $?

# --- B · ANYTHING ELSE IS STILL DENIED -----------------------------------------------------
# The allow-list must not become a hole. A foreign untracked file, a modified tracked file and
# a staged change all still refuse — in both blocks.
reset_tree
printf 'CVE-2026-0001\n' >"$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"
write_phase1_evidence
printf 'x\n' >"$TREE/scripts/injected-helper.sh"
run_step "$WORK/producer.sh"
[ "$rc" -ne 0 ]
check "a FOREIGN untracked file still denies the producer" "no hole for others" $?
rm -f "$TREE/scripts/injected-helper.sh"

printf '\n# injected\n' >>"$TREE/scripts/release-ota-channel.sh"
run_step "$WORK/producer.sh"
[ "$rc" -ne 0 ]
check "a MODIFIED tracked file still denies the producer" "tampering still caught" $?
(cd "$TREE" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)

printf '\n# staged\n' >>"$TREE/scripts/release-ota-channel.sh"
(cd "$TREE" && /usr/bin/git add -A >/dev/null 2>&1)
run_step "$WORK/producer.sh"
[ "$rc" -ne 0 ]
check "a STAGED change still denies the producer" "index still counts" $?
reset_tree
rm -f "$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"

seed_phase2_download
printf 'x\n' >"$TREE/scripts/injected-helper.sh"
run_step "$WORK/guard.sh"
[ "$rc" -ne 0 ]
check "a FOREIGN untracked file still denies the ceremony" "no hole for others" $?
rm -f "$TREE/scripts/injected-helper.sh"

printf '\n# injected\n' >>"$TREE/scripts/release-ota-channel.sh"
run_step "$WORK/guard.sh"
[ "$rc" -ne 0 ]
check "a MODIFIED tracked file still denies the ceremony" "tampering still caught" $?
(cd "$TREE" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)

# a file smuggled INSIDE an allowed directory is still a file this job did not generate;
# `-uall` is what keeps it visible instead of collapsing into `?? ota-dist/`.
printf 'x\n' >"$TREE/ota-dist/../ota-evil.txt"
run_step "$WORK/guard.sh"
[ "$rc" -ne 0 ]
check "an untracked file beside the allowed dirs denies" "prefix, not blanket" $?
rm -f "$TREE/ota-evil.txt"
reset_tree

# --- C · THE TWO WINDOWS ------------------------------------------------------------------
# A late refusal does not unbind what already happened: by the time the producer runs, this
# job has built, signed and uploaded. The early guard exists so a tampered checkout stops the
# phase BEFORE any of that, and the late guard exists so tampering that lands AFTER the early
# one still dies before the release rule is read. Both windows are exercised.
reset_tree
printf 'CVE-2026-0001\n' >"$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"

# window 1 — tampering BEFORE the consumer: the early guard stops the phase, and nothing runs
printf '\n# injected\n' >>"$TREE/scripts/release-ota-channel.sh"
run_step "$WORK/early1.sh" GITHUB_SHA="$OID"
[ "$rc" -ne 0 ]
check "phase 1 early guard refuses a tampered checkout" "before the build" $?
[ ! -s "$WORK/log.$n" ]
check "and nothing was built, generated or uploaded" "no side effects survive" $?
(cd "$TREE" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)

# clean control — the early guard must let the real flow through
run_step "$WORK/early1.sh" GITHUB_SHA="$OID"
[ "$rc" -eq 0 ]
check "phase 1 early guard passes a clean checkout" "non-firing direction" $?

# window 2 — tampering BETWEEN the early guard and the producer: the early guard already
# passed, so only the late one can still catch it.
write_phase1_evidence
printf '\n# injected after the early guard\n' >>"$TREE/scripts/release-ota-channel.sh"
run_step "$WORK/producer.sh"
[ "$rc" -ne 0 ]
check "tampering AFTER the early guard still dies at the producer" "the late window" $?
[ ! -s "$WORK/log.$n" ]
check "and the producer produced nothing" "no side effects survive" $?
(cd "$TREE" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)
reset_tree
rm -f "$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"

# the same two windows in phase 2
printf '\n# injected\n' >>"$TREE/scripts/release-ota-channel.sh"
run_step "$WORK/early2.sh"
[ "$rc" -ne 0 ]
check "phase 2 early guard refuses a tampered checkout" "before the download" $?
[ ! -s "$WORK/log.$n" ]
check "and nothing was downloaded or verified" "no side effects survive" $?
(cd "$TREE" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)
run_step "$WORK/early2.sh"
[ "$rc" -eq 0 ]
check "phase 2 early guard passes a clean checkout" "non-firing direction" $?
seed_phase2_download
printf '\n# injected after the early guard\n' >>"$TREE/scripts/release-ota-channel.sh"
run_step "$WORK/guard.sh"
[ "$rc" -ne 0 ]
check "tampering AFTER the early guard still dies at the ceremony" "the late window" $?
(cd "$TREE" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)
reset_tree

# --- D · nothing tracked runs before its window is proven ---------------------------------
# Phase 1: a prior action rewrites each tracked script; the pre-script guard must stop the
# phase with neither of them executed.
reset_tree
for victim in scripts/assert-cosign-binary.sh scripts/check-cosign-contract.sh; do
	[ -f "$TREE/$victim" ] || continue
	printf '\n# injected by a prior uses: step\n' >>"$TREE/$victim"
	run_step "$WORK/prescript1.sh" GITHUB_SHA="$OID"
	[ "$rc" -ne 0 ]
	check "phase 1 refuses with $victim rewritten" "before any script runs" $?
	contains_literal "$out" 'No tracked script has been executed'
	check "and it says no tracked script ran" "zero side effects" $?
	(cd "$TREE" && /usr/bin/git checkout -q -- "$victim")
done
run_step "$WORK/prescript1.sh" GITHUB_SHA="$OID"
[ "$rc" -eq 0 ]
check "phase 1 pre-script guard passes a clean checkout" "non-firing direction" $?

# Phase 2: the tag AND the checkout move together and the input matches — the shape the early
# guard alone accepts. The binding step must refuse on phase 1's evidence, with zero tracked
# code executed.
reset_tree
# A REAL second commit, made here. `git rev-parse HEAD~1` prints its own argument on failure,
# so a one-commit fixture handed the case the literal string "HEAD~1" and it refused on the
# OID shape check instead of on the identity binding it meant to exercise.
(
	cd "$TREE" || exit 1
	printf '# later\n' >>scripts/release-ota-channel.sh
	/usr/bin/git add -A -- scripts >/dev/null 2>&1
	/usr/bin/git commit -q -m "a later commit" >/dev/null 2>&1
	/usr/bin/git rev-parse HEAD >"$WORK/other-oid"
	/usr/bin/git checkout -q "$OID" 2>/dev/null
) || :
OTHER="$(cat "$WORK/other-oid" 2>/dev/null)"
[ -n "$OTHER" ] && [ "${#OTHER}" -eq 40 ] && [ "$OTHER" != "$OID" ]
check "the fixture has a second real commit for the moved-tag case" "the case is real" $?
if [ -n "$OTHER" ] && [ "${#OTHER}" -eq 40 ]; then
	seed_phase2_download
	(cd "$TREE" && /usr/bin/git checkout -q "$OTHER" 2>/dev/null && /usr/bin/git tag -f v26.8.0 "$OTHER" >/dev/null 2>&1)
	run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OTHER"
	[ "$rc" -ne 0 ] && contains_literal "$out" 'not the commit phase 1 built'
	check "a tag+checkout+input moved together is refused at binding" "identity before execution" $?
	contains_literal "$out" 'No tracked script has been executed'
	check "and zero tracked code ran" "no unbound execution" $?
	(cd "$TREE" && /usr/bin/git checkout -q "$OID" 2>/dev/null && /usr/bin/git tag -f v26.8.0 "$OID" >/dev/null 2>&1)
	seed_phase2_download
	run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID"
	[ "$rc" -eq 0 ]
	check "the real commit binds and lets the phase continue" "non-firing direction" $?
fi
reset_tree

# --- E · the signature is verified BEFORE any tracked code runs ---------------------------
# The previous binding compared a digest against a checksums.txt nobody had authenticated, and
# said so in its own comment. A contents-writer who moves tag, checkout and input together AND
# replaces the evidence pair satisfies it — and the job then executes checkout code. Here the
# cosign verification fails, and nothing tracked may run.
reset_tree
seed_phase2_download
run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID" STUB_COSIGN_RC=1
[ "$rc" -ne 0 ] && contains_literal "$out" 'not signed by the release identity'
check "an unsigned/forged checksums.txt is refused at binding" "signature before digest" $?
contains_literal "$out" 'No tracked script has been executed'
check "and zero tracked code ran" "tracked counter stays at 0" $?
# A SHIM WITH THE RIGHT ANSWERS. It reports the reviewed version and returns 0 from
# verify-blob; only its BYTES differ. Pinning the version would accept it — the digest does
# not, which is the whole point of identifying the artefact instead of asking it.
cp "$WORK/bin/cosign" "$WORK/bin/cosign.real"
printf '#!/usr/bin/env bash\ncase "${1:-}" in version) printf "GitVersion:    v2.6.4\\n";; esac\nexit 0\n' >"$WORK/bin/cosign"
chmod +x "$WORK/bin/cosign"
run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID" \
	COSIGN_EXPECTED_SHA256="$(sha256sum "$WORK/bin/cosign.real" | cut -d' ' -f1)"
[ "$rc" -ne 0 ] && contains_literal "$out" 'not the reviewed artefact'
check "a cosign shim with the right version but wrong bytes refuses" "identified, not asked" $?
contains_literal "$out" 'No tracked code has run'
check "and zero tracked code ran" "tracked counter stays at 0" $?
cp "$WORK/bin/cosign.real" "$WORK/bin/cosign"
run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID" COSIGN_EXPECTED_SHA256=""
[ "$rc" -ne 0 ] && contains_literal "$out" 'not pinned'
check "an unpinned cosign digest refuses" "could-not-check is not fine" $?
run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID" CERT_IDENTITY_REGEXP='https://github.com/'
[ "$rc" -ne 0 ]
check "an unanchored identity pattern refuses" "no theatre" $?
run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID"
[ "$rc" -eq 0 ] && command grep -q '^cosign verify-blob' "$WORK/log.$n"
check "the signed path binds and verifies with cosign" "non-firing direction" $?
reset_tree

# --- F · the build itself can dirty the tree, and then nothing may be uploaded -------------
reset_tree
printf 'CVE-2026-0001\n' >"$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"
write_phase1_evidence
printf '\n# written by the build\n' >>"$TREE/scripts/release-ota-channel.sh"
run_step "$WORK/postbuild1.sh"
[ "$rc" -ne 0 ] && contains_literal "$out" 'build left the checkout modified'
check "a build that dirties a tracked file blocks every upload" "post-build window" $?
[ ! -s "$WORK/log.$n" ]
check "and nothing was generated or uploaded" "zero side effects" $?
(cd "$TREE" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)
run_step "$WORK/postbuild1.sh"
[ "$rc" -eq 0 ]
check "the legitimate build artefacts pass the post-build guard" "non-firing direction" $?
reset_tree

# --- F-bis · THE TAG CONTRACT IS DENY-CLOSED (INT-22, M61-M66) ----------------------------
# Until 2026-08-18 the phase-1 post-build guard read the tag as
#   tag_oid="$(git rev-parse --verify "refs/tags/$TAG^{commit}" 2>/dev/null)" || tag_oid=""
# and an empty tag_oid SKIPPED the comparison. That writes "I could not read it" exactly the
# same way as "it agrees": a corrupt ref, an unreadable object, any git failure at all, and the
# guard waved the build through to every producer and upload below it.
#
# THE DISCRIMINATOR HAD TO BE MEASURED. For refs/tags/<T>, broken vs absent:
#   rev-parse --verify --quiet   1 / 1        (identical)
#   rev-parse --verify           128 / 128    (identical)
#   tag --list stderr            "warning: ignoring broken ref" / (empty)   <-- the only signal
# So the guard reads that stderr. A first attempt used `tag --list` STDOUT, which is empty for a
# broken ref, so a corrupt tag read as "absent" and sailed through — wrong in the dangerous
# direction, and case (4) below is what caught it.
#
# THE BROKEN CASE GOES LAST ON PURPOSE: a corrupt loose ref survives reset_tree, so it is
# created immediately before the check that needs it and removed immediately after.
reset_tree
printf 'CVE-2026-0001\n' >"$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"
write_phase1_evidence

# (1) THE MATCHING TAG PASSES. The baseline: nothing about deny-closed may cost the normal path.
(cd "$TREE" && /usr/bin/git tag -f v26.8.0 "$OID" >/dev/null 2>&1)
run_step "$WORK/postbuild1.sh"
[ "$rc" -eq 0 ]
check "a tag that MATCHES the built commit passes" "permit direction" $?

# (2) THE ABSENT TAG STILL PASSES. This is why the old code tolerated an empty read at all:
# before the tag is cut there is nothing to compare. Deny-closed must not turn a legitimate
# absence into a refusal, and this is the case that would catch it if it did.
(cd "$TREE" && /usr/bin/git tag -d v26.8.0 >/dev/null 2>&1)
run_step "$WORK/postbuild1.sh"
[ "$rc" -eq 0 ]
check "an ABSENT tag still passes the post-build guard" "permit direction" $?

# (3) THE MOVED TAG STILL REFUSES. The property the old code DID catch has to survive the
# rewrite: a guard fixed for one direction that loses the other is a regression, not a fix.
OTHER="$(cd "$TREE" && /usr/bin/git commit -q --allow-empty -m other >/dev/null 2>&1; /usr/bin/git rev-parse HEAD)"
(cd "$TREE" && /usr/bin/git reset -q --hard "$OID" >/dev/null 2>&1 && /usr/bin/git tag -f v26.8.0 "$OTHER" >/dev/null 2>&1)
[ -n "$OTHER" ] && [ "$OTHER" != "$OID" ]
check "the fixture really has a second commit to move the tag to" "the case is real" $?
run_step "$WORK/postbuild1.sh"
[ "$rc" -ne 0 ] && contains_literal "$out" 'The tag moved while this job was building'
check "a MOVED tag still refuses after the rewrite" "no lost property" $?

# (4) THE UNREADABLE TAG REFUSES — the case that did not exist before. The ref is present but
# does not resolve, which is precisely what the old `|| tag_oid=""` swallowed. A corrupt loose
# ref takes precedence over packed-refs, so this reproduces the shape without touching objects.
(cd "$TREE" && /usr/bin/git tag -f v26.8.0 "$OID" >/dev/null 2>&1)
printf 'this-is-not-an-object-id\n' >"$TREE/.git/refs/tags/v26.8.0"
run_step "$WORK/postbuild1.sh"
[ "$rc" -ne 0 ]
check "a tag that LISTS but does not RESOLVE refuses" "not knowing is not agreeing" $?
contains_literal "$out" 'refusal, not an absence'
check "and it says so: a refusal, never an empty read" "the message names the class" $?

# Remove the corrupt ref BY HAND: `git tag -d` cannot delete a broken ref, and reset_tree does
# not touch .git/refs, so leaving it here would contaminate every case that follows.
rm -f "$TREE/.git/refs/tags/v26.8.0"
(cd "$TREE" && /usr/bin/git tag -f v26.8.0 "$OID" >/dev/null 2>&1)
git -C "$TREE" rev-parse --verify --quiet 'refs/tags/v26.8.0^{commit}' >/dev/null
check "the fixture's tag is sound again before the next section" "no contamination" $?
reset_tree

# --- F-ter · THE UPLOAD CEREMONY IS CREATE-ONLY (INT-22, M60/M67-M73) ---------------------
# Every `gh release upload` in this workflow carried --clobber, which OVERWRITES an asset the
# release already has. On a draft that is untidy; on a second pass over a release whose bytes
# somebody may already hold it rewrites delivered evidence, and that is the one property of a
# supply chain that cannot be undone. Five sites, which does not make it milder — it makes it
# systematic.
#
# TWO CHECKS, because either alone is weak. The static one is the guard: no upload may carry the
# flag. The behavioural one CALIBRATES THE STUB, and without it the static check would be the
# only thing holding the property while a stub that always succeeds quietly agreed with
# everything — which is exactly how the download leg's own refusal went unmodelled for so long.
_uploads="$(command grep -cE 'gh release upload' "$WF_REAL" || true)"
[ "${_uploads:-0}" -ge 5 ]
check "the workflow still has the upload sites this case is about ($_uploads)" "denominator is real" $?
_clob="$(command grep -E 'gh release upload' "$WF_REAL" | command grep -c -- '--clobber' || true)"
[ "${_clob:-1}" -eq 0 ]
check "NO release upload carries --clobber" "create-only ceremony" $?
# The download leg legitimately keeps it: fetching into a directory this job owns is not
# publishing, and the ceremony asks for the same evidence twice on purpose.
_dl="$(command grep -cE 'release download.*--clobber' "$WF_REAL" || true)"
[ "${_dl:-0}" -ge 1 ]
check "and the DOWNLOAD --clobber is left alone" "not a blanket ban" $?

# Calibration: the stub must be able to refuse, or the static check above is alone.
: >"$WORK/uploaded-assets.txt"
STUB_LOG="$WORK/log.calib" "$WORK/bin/gh" release upload v26.8.0 dist/stable-manifest.json >/dev/null 2>&1
_first=$?
STUB_LOG="$WORK/log.calib" "$WORK/bin/gh" release upload v26.8.0 dist/stable-manifest.json >"$WORK/calib.out" 2>&1
_second=$?
[ "$_first" -eq 0 ] && [ "$_second" -ne 0 ] && command grep -q 'already exists' "$WORK/calib.out"
check "calibration: the stub ACCEPTS a first upload and REFUSES the second" "the stub can say no" $?
STUB_LOG="$WORK/log.calib" "$WORK/bin/gh" release upload v26.8.0 dist/stable-manifest.json --clobber >/dev/null 2>&1
check "calibration: and --clobber still overwrites, so the refusal is about the flag" "control positive" $?
: >"$WORK/uploaded-assets.txt"

# --- F-quater · THE SYMLINK CONTRACT ON THE PUBLISHED ARCHIVE (INT-22, M79-M81) -----------
# Phase 2 extracts `olivares` from the downloaded archive, chmods it and RUNS it. Verification
# proves the archive's BYTES; it says nothing about the SHAPE of what comes out. tar will
# happily create a symlink, and a crafted archive whose member is a link makes
# ota-dist/community/olivares a name that executes something else.
#
# The DISCRIMINATOR is verified here against real archives, because that is the part that can
# be silently wrong; the WIRING is asserted statically below it.
_sym="$WORK/symtest"; rm -rf "$_sym"; mkdir -p "$_sym/good" "$_sym/evil"
printf '#!/bin/sh\necho real\n' >"$_sym/good/olivares"
( cd "$_sym/good" && tar -czf "$_sym/good.tar.gz" olivares )
( cd "$_sym/evil" && ln -s /usr/bin/env olivares && tar -czhf /dev/null olivares 2>/dev/null; tar -czf "$_sym/evil.tar.gz" olivares )

_good_bad="$(tar -tvzf "$_sym/good.tar.gz" | grep -E '^[hl]' || true)"
[ -z "$_good_bad" ]
check "the discriminator PASSES an archive of regular files" "permit direction" $?
_evil_bad="$(tar -tvzf "$_sym/evil.tar.gz" | grep -E '^[hl]' || true)"
[ -n "$_evil_bad" ]
check "the discriminator CATCHES a symlink member" "tar -tvzf marks links with l" $?

# And the thing the guard is protecting against is real: extracting the evil archive really
# does produce a link, so this is not a hypothetical shape.
rm -rf "$_sym/out"; mkdir -p "$_sym/out"
tar -xzf "$_sym/evil.tar.gz" -C "$_sym/out" olivares 2>/dev/null || true
[ -L "$_sym/out/olivares" ]
check "and extracting it really lands a SYMLINK on disk" "the threat is not hypothetical" $?
rm -rf "$_sym"

# WIRING: the workflow must run the check BEFORE the extraction, not after it.
_tvz_line="$(command grep -n "tar -tvzf" "$WF_REAL" | head -1 | cut -d: -f1)"
_xzf_line="$(command grep -n 'tar -xzf "ota-dist/olivares_' "$WF_REAL" | head -1 | cut -d: -f1)"
[ -n "$_tvz_line" ] && [ -n "$_xzf_line" ] && [ "$_tvz_line" -lt "$_xzf_line" ]
check "the link check runs BEFORE the extraction ($_tvz_line < $_xzf_line)" "order is the guard" $?
command grep -q 'the extracted olivares is not a regular file' "$WF_REAL"
check "and it is re-checked AFTER, on what actually landed" "listing is not landing" $?

# --- F-quinquies · gh IS IDENTIFIED, NOT MERELY LOCATED (INT-22, M78) ---------------------
# cosign is pinned by sha256 AND version under a comment that says "THE BINARY, NOT ITS
# SELF-DESCRIPTION". gh moves the release's assets — the same trust position — and was checked
# only for existence and PATH resolution. These cases hold the asymmetry closed and, just as
# importantly, they record exactly HOW FAR it is closed: a version floor is not a digest pin,
# and a case that pretended otherwise would be the worst outcome here.
command grep -q 'gh_ver_raw=' "$WF_REAL"
check "the ceremony reads a version out of gh" "identified, not just located" $?
command grep -q 'this ceremony requires >= 2.40' "$WF_REAL"
check "and refuses below the floor create-only needs" "the floor has a reason" $?
command grep -q 'sha256sum" "\${GH_BIN}"' "$WF_REAL"
check "and records gh's digest on every run" "the value a pin would need" $?
_ghpin="$(command grep -c 'GH_EXPECTED_SHA256' "$WF_REAL" || true)"
[ "${_ghpin:-0}" -eq 0 ]
check "NOT a digest pin, and this case says so out loud" "no claim beyond the evidence" $?

# The floor discriminates. Parsing is where a version check usually rots, so both directions
# are exercised against the real shapes gh emits rather than against a regex read by eye.
_parse() { # _parse <first line of gh --version> -> prints "major minor" or nothing
	case "$1" in "gh version "*) ;; *) return 1 ;; esac
	_v="${1#gh version }"; _v="${_v%% *}"
	_maj="${_v%%.*}"; _rest="${_v#*.}"; _min="${_rest%%.*}"
	case "${_maj}${_min}" in *[!0-9]*|"") return 1 ;; esac
	printf '%s %s' "$_maj" "$_min"
}
_parse "gh version 2.97.0 (2026-07-31)" >/dev/null
check "the parse accepts the real gh --version first line" "permit direction" $?
[ "$(_parse "gh version 2.97.0 (2026-07-31)")" = "2 97" ]
check "and extracts major and minor correctly" "not just non-empty" $?
_parse "" >/dev/null 2>&1
[ $? -ne 0 ]
check "an EMPTY version is refused, not treated as new enough" "not knowing is not agreeing" $?
_parse "gh version banana" >/dev/null 2>&1
[ $? -ne 0 ]
check "an unparseable version is refused" "deny-closed on the read" $?

# AND THE FLOOR REFUSES FOR REAL, through the extracted step rather than through a copy of its
# arithmetic. The stub takes its version from STUB_GH_VERSION, so the same binding step that
# passes at 2.97 is driven below the floor and must refuse — a floor asserted only by grepping
# the workflow would pass just as happily if the comparison were inverted.
reset_tree
printf 'CVE-2026-0001\n' >"$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"
seed_phase2_download
run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID" STUB_GH_VERSION="2.39.0"
[ "$rc" -ne 0 ] && contains_literal "$out" 'requires >= 2.40'
check "gh BELOW the floor refuses the binding step" "the floor is enforced, not described" $?
seed_phase2_download
run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID" STUB_GH_VERSION="2.97.0"
[ "$rc" -eq 0 ]
check "and gh at 2.97 still binds" "permit direction" $?
reset_tree

# --- G · the earliest windows, and byte-exact evidence ------------------------------------
reset_tree
printf 'CVE-2026-0001\n' >"$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"

# a build input rewritten by an earlier action: the post-checkout guard must stop it
for victim in .node-version Taskfile.yml; do
	[ -f "$TREE/$victim" ] || continue
	printf '\n# injected\n' >>"$TREE/$victim"
	run_step "$WORK/g1.sh"
	[ "$rc" -ne 0 ]
	check "guard 1 refuses with $victim rewritten" "before anything reads it" $?
	[ ! -s "$WORK/log.$n" ]
	check "and no tracked script ran" "counter stays at 0" $?
	(cd "$TREE" && /usr/bin/git checkout -q -- "$victim")
done
run_step "$WORK/g1.sh"
[ "$rc" -eq 0 ]
check "guard 1 passes a clean checkout" "non-firing direction" $?
# Guard 2 needs a REFUSING witness of its own: a clean-direction case stays green when the
# guard is neutered, which is exactly how its mutant escaped the first time this ran.
printf '\n# injected between the setup actions and the first script\n' >>"$TREE/Taskfile.yml"
run_step "$WORK/g2.sh"
[ "$rc" -ne 0 ]
check "guard 2 refuses a checkout the setup actions rewrote" "window 1 is real" $?
[ ! -s "$WORK/log.$n" ]
check "and no tracked script ran after it" "counter stays at 0" $?
(cd "$TREE" && /usr/bin/git checkout -q -- Taskfile.yml)
run_step "$WORK/g2.sh"
[ "$rc" -eq 0 ]
check "guard 2 passes a clean checkout" "non-firing direction" $?

# byte-exactness: a tail with no trailing newline satisfied read + wc -l == 1
write_phase1_evidence
printf 'X' >>"$TREE/release-commit.txt"
run_step "$WORK/postbuild1.sh"
[ "$rc" -ne 0 ] && contains_literal "$out" 'not exactly this run'
check "evidence with an appended tail is refused" "byte-exact, not line-shaped" $?
[ ! -s "$WORK/log.$n" ]
check "and nothing was generated or uploaded" "zero side effects" $?
write_phase1_evidence
run_step "$WORK/postbuild1.sh"
[ "$rc" -eq 0 ]
check "the exact evidence passes the post-build guard" "non-firing direction" $?

# THE CLEAN PIVOT. The tree is clean, the evidence still names the commit this run builds, and
# HEAD is somewhere else entirely — nothing here is "dirty", so a guard that only asks about
# `git status` and about the evidence BYTES says yes. Measured on the extracted block before
# this case existed: GITHUB_SHA=A, HEAD=B, clean, release-commit.txt=A gave rc=0 and
# "producers and uploads may run". The battery only ever mutated bytes and status, so it could
# not see it: identity has to be asserted about the CHECKOUT, not only about what it claims.
(cd "$TREE" && /usr/bin/git checkout -q "$OTHER" 2>/dev/null)
write_phase1_evidence            # still names $OID, the commit this run builds
[ "$(git -C "$TREE" rev-parse HEAD)" != "$OID" ]
check "the fixture really is pivoted to another commit" "the case is real" $?
run_step "$WORK/postbuild1.sh"
[ "$rc" -ne 0 ] && contains_literal "$out" 'not the commit this run builds'
check "a CLEAN checkout at another commit is refused" "identity, not just cleanliness" $?
[ ! -s "$WORK/log.$n" ]
check "and nothing was generated or uploaded" "zero side effects" $?
(cd "$TREE" && /usr/bin/git checkout -q "$OID" 2>/dev/null)
write_phase1_evidence
run_step "$WORK/postbuild1.sh"
[ "$rc" -eq 0 ]
check "the un-pivoted checkout still passes" "non-firing direction" $?
reset_tree

# --- H · the two downloads, in the order phase 2 runs them --------------------------------
# The binding step fetches the four evidence files into ota-dist with --clobber. The next step
# then asked for THE SAME FOUR into THE SAME directory without it, and `gh release download`
# refuses an existing destination — so the ceremony denied itself on files it had just
# authenticated. Only the sequence shows it: each step alone is fine.
reset_tree
rm -f "$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"
seed_phase2_download
run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID"
[ "$rc" -eq 0 ]
check "step 1 of 2: the binding fetches and verifies the evidence" "sequence, part one" $?
run_step "$WORK/download2.sh"
[ "$rc" -eq 0 ]
check "step 2 of 2: the next download does NOT collide with it" "no self-denial" $?
# and it asks only for what is still missing
! command grep -qE '^gh release download .*release-commit\.txt' "$WORK/log.$n"
check "the second download does not re-request the evidence" "reuse the verified bytes" $?

# the collision itself, proved present in the tool rather than assumed: asking again for an
# existing file without --clobber fails
run_step "$WORK/bind2.sh" RELEASE_COMMIT="$OID"
_probe="$(cd "$TREE" && env -i PATH="$WORK/trusted:$WORK/bin:/usr/bin:/bin" STUB_LOG=/dev/null \
	"$WORK/bin/gh" release download v26.8.0 --dir ota-dist --pattern checksums.txt 2>&1)" && _prc=0 || _prc=1
[ "$_prc" -ne 0 ] && contains_literal "$_probe" 'already exists'
check "calibration: a second fetch without --clobber really refuses" "the trap is modelled" $?
reset_tree

# --- I · THE GORELEASER DIRTY GATE, AND THE EVIDENCE THAT HAS TO SURVIVE IT ----------------
# THE CONTRAFACTUAL THAT WAS MISSING. Every case above asks "does the guard refuse what it
# should?". None asked "does a release that SHOULD pass actually pass?", and that is precisely
# where P0-A lived: a workflow step wrote release-commit.txt immediately before the goreleaser
# action, the file is neither tracked nor ignored, and GoReleaser REFUSES a dirty tree — so the
# hardening blocked every legitimate release at the build, with nothing produced
# (the model, 2026-08-15, P0-A, NO-LAND). Denial was measured; permission was not.
#
# GoReleaser's test is modelled EXACTLY, not approximated: v2.17.0 runs
# `git status --porcelain` and treats any non-empty output as ErrDirty
# (internal/pipe/git/git.go:217-224), from the git pipe, which runs BEFORE the `before` hooks
# (internal/pipeline/pipeline.go:63-104). That ordering is the whole reason the write is a hook.
reset_tree
rm -f "$TREE/release/advisories/26.8.0.txt"
(cd "$TREE" && git add -A -- scripts release .gitignore >/dev/null 2>&1 && git commit -q --amend --no-edit && git tag -f v26.8.0 >/dev/null 2>&1)
OID="$(git -C "$TREE" rev-parse HEAD)"

goreleaser_dirty_check() { # the pinned engine's own gate, verbatim in behaviour
	[ -z "$(/usr/bin/git -C "$TREE" status --porcelain)" ]
}
# 1 — the pre-build guard passes AND the tree GoReleaser then sees is clean.
run_step "$WORK/early1.sh" GITHUB_SHA="$OID"
[ "$rc" -eq 0 ]
check "phase 1's pre-build guard passes on a legitimate release" "permit direction" $?
goreleaser_dirty_check
check "the tree GoReleaser is handed is CLEAN" "the release can start at all" $?

# 2 — the hook runs INSIDE GoReleaser, after that gate, and writes the evidence.
(cd "$TREE" && env -i PATH="/usr/bin:/bin" HOME="$WORK" GITHUB_SHA="$OID" \
	bash scripts/release-commit-evidence.sh false) >"$WORK/hook.out" 2>&1
[ "$?" -eq 0 ]
check "the before-hook writes the evidence for a real release" "the write still happens" $?
printf '%s\n' "$OID" | cmp -s - "$TREE/release-commit.txt"
check "and it is exactly the run's commit and one newline" "byte-exact evidence" $?
[ "$(/usr/bin/git -C "$TREE" status --porcelain -uall)" = "?? release-commit.txt" ]
check "the only thing it adds is the allow-listed untracked file" "nothing else moved" $?
# 3 — and the post-build guard, which is the next thing to look, accepts that tree.
run_step "$WORK/postbuild1.sh"
[ "$rc" -eq 0 ]
check "the post-build guard accepts the hook's evidence" "the sequence completes" $?

# 4 — THE DENY DIRECTION IS NOT LOST. The same modelled gate still sees real dirt, and the
# guard still refuses it: moving the write did not make either blind.
printf '\n# written by a prior action\n' >>"$TREE/scripts/release-ota-channel.sh"
! goreleaser_dirty_check
check "a genuinely modified tracked file is still DIRTY to GoReleaser" "deny direction" $?
run_step "$WORK/postbuild1.sh"
[ "$rc" -ne 0 ]
check "and the post-build guard still refuses it" "no hole opened" $?
(cd "$TREE" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)
reset_tree

# 5 — the hook's own refusals. A snapshot writes NOTHING (a local `task release:snapshot` must
# not drop an untracked file in a developer's tree), and a real release with no honest commit
# refuses instead of inventing one.
rm -f "$TREE/release-commit.txt"
(cd "$TREE" && env -i PATH="/usr/bin:/bin" HOME="$WORK" GITHUB_SHA="$OID" \
	bash scripts/release-commit-evidence.sh true) >/dev/null 2>&1
_hrc=$?
[ "$_hrc" -eq 0 ] && [ ! -e "$TREE/release-commit.txt" ]
check "a snapshot build writes no evidence at all" "no stray file locally" $?
# EACH REFUSAL IS A DIFFERENT SHAPE, and the SHA in each case is spelled out rather than
# derived, because the first version of this loop discarded its own third field and handed the
# script a PERFECTLY VALID OID while claiming to test a malformed one — a case that could only
# ever have failed. `<arg> <sha>`; @noarg means the script is called with no argument at all.
_upper="$(printf '%s' "$OID" | tr 'a-f' 'A-F')"
_short="${OID%?}"
for _case in "false " "false zz" "false $_upper" "false $_short" "bogus $OID" "@noarg $OID"; do
	_arg="${_case%% *}"
	_sha="${_case#* }"
	rm -f "$TREE/release-commit.txt"
	if [ "$_arg" = "@noarg" ]; then
		(cd "$TREE" && env -i PATH="/usr/bin:/bin" HOME="$WORK" GITHUB_SHA="$_sha" \
			bash scripts/release-commit-evidence.sh) >/dev/null 2>&1
	else
		(cd "$TREE" && env -i PATH="/usr/bin:/bin" HOME="$WORK" GITHUB_SHA="$_sha" \
			bash scripts/release-commit-evidence.sh "$_arg") >/dev/null 2>&1
	fi
	_hrc=$?
	[ "$_hrc" -ne 0 ] && [ ! -e "$TREE/release-commit.txt" ]
	check "the hook refuses [${_case% *} ${_sha:-<empty>}]" "fail-closed, no half file" $?
done
reset_tree

# 6 — THE WIRING, ON THE VERSIONED FILES THEMSELVES. Running the blocks cannot see WHERE the
# write sits, and that position is the defect. Comment lines are stripped first: the fix is
# explained at length exactly where the offending line used to be, so a naive grep for the
# name would match the prose that says it must not be there.
_pre_goreleaser="$(awk '
	/^      - uses: actions\/checkout@/ { on = 1 }
	/^        uses: goreleaser\/goreleaser-action@/ { exit }
	on { print }
' "$WF_REAL" | command grep -vE '^[[:space:]]*#')"
# HERE-STRING, NO TUBERIA: `X | grep -q` bajo `pipefail` devuelve 141 CUANDO ACIERTA, y
# aqui el `!` de delante convertiria ese 141 en un PASE. Un falso verde en la comprobacion
# que impide que P0-A vuelva.
! command grep -q 'release-commit\.txt' <<<"$_pre_goreleaser"
check "no step writes the evidence BEFORE goreleaser runs" "P0-A cannot come back" $?
# AND THE CLASS, NOT ONLY THIS FILENAME. The defect is not "release-commit.txt"; it is "phase 1
# writes a workspace file before the engine that refuses a dirty workspace". Any redirection
# into a plain relative path in that window is that defect under a different name, so the
# window is required to write NOTHING at all. Redirections to /dev/*, to `&2` and to
# "$GITHUB_OUTPUT"/"$GITHUB_ENV"-style variables are not plain paths and never match.
# ⛔ `>` IS NOT ALWAYS A REDIRECTION. Factored into a function on 2026-08-31 so the two cases
# below exercise THE SAME code as the real check — a fix tested on a copy proves nothing about
# the original. The leading `(^|[^=])` is the fix: without it the `>` that CLOSES the `==>` of
# `echo "==> freed 24077 MB"` (release.yml:609) matched, and ` freed` was read as the name of a
# file written before the dirty gate. It reddened `lint:release-mechanics` — and with it the
# whole push queue — while the window wrote nothing at all.
_early_write_tokens() { # <text> -> plain-path redirections in it, arrows excluded
	printf '%s\n' "$1" |
		command grep -oE '(^|[^=])>>?[[:space:]]*[A-Za-z0-9_.][A-Za-z0-9_./-]*' |
		command grep -vE '>>?[[:space:]]*/dev/' | tr '\n' ' '
}
_early_writes="$(_early_write_tokens "$_pre_goreleaser")"
[ -z "$_early_writes" ]
check "nothing at all is written before the dirty gate" "${_early_writes:-the window writes nothing}" $?
# AND ITS OWN CASE, because a regex regrows its bug at the next rewrite and nothing would die.
[ -z "$(_early_write_tokens 'echo "==> freed 10 MB; 20 MB left"')" ]
check "an ==> arrow inside an echo is not a redirection" "release.yml:609, the shape that reddened 92" $?
# POSITIVE CONTROL: without it, a detector that simply went blind would pass the case above.
[ -n "$(_early_write_tokens 'echo hi > release-commit.txt')" ]
check "and a REAL redirection is still caught" "the exclusion did not blind the detector" $?
# and the allow-list that admits it afterwards is still there, still by exact name
# ⛔ ESTA fue la que mordio. El productor NO es una variable pequena que quepa en el bufer
# de 64K: es un `grep -vE` leyendo un FICHERO, asi que `grep -q` puede cerrarle la tuberia
# antes de que termine y el pipeline sale 141 HABIENDO ENCONTRADO la linea. Medido el
# 2026-08-19 sobre este mismo workflow, cinco corridas seguidas: 141 141 0 0 141. En `main`
# puso rojo `release mechanics` — y con el, el job `control-plane` entero — mientras en el
# hub pasaba. El censo de docs/sigpipe-booleans-baseline.txt busco productores grandes por
# su forma (`git ls-files`, `find`, `git log`, `git grep`, `git for-each-ref`) y NO incluyo
# `grep <fichero>`: por eso esta sobrevivio clasificada como inocua.
command grep -q "'?? release-commit.txt') continue ;;" <<<"$(command grep -vE '^[[:space:]]*#' "$WF_REAL")"
check "the post-build guard still allow-lists it BY NAME" "not by an ignore rule" $?

# 7 — .goreleaser.yaml must carry all three halves of the contract: the hook that writes it,
# the checksum entry that puts its digest in the SIGNED set, and the release entry that
# uploads it. In v2.17.0 neither extra_files key implies the other — checksum appends to a
# local list (internal/pipe/checksums/checksums.go:174-198) while release registers an
# UploadableFile (internal/pipe/release/release.go:160-174) — and phase 2 needs both.
_gr_block() { # _gr_block <top-level key> — the block under a column-0 key
	awk -v k="$1:" '
		$0 == k { on = 1; next }
		on && /^[a-z_]+:/ { exit }
		on { print }
	' "$GORELEASER_CFG"
}
command grep -q 'bash scripts/release-commit-evidence.sh {{ .IsSnapshot }}' \
	<<<"$(_gr_block before | command grep -vE '^[[:space:]]*#')"
check "the goreleaser config runs the evidence hook" "written after the dirty gate" $?
command grep -q 'release-commit.txt' <<<"$(_gr_block checksum | command grep -vE '^[[:space:]]*#')"
check "checksum.extra_files declares the evidence" "its digest is inside checksums.txt" $?
command grep -q 'release-commit.txt' <<<"$(_gr_block release | command grep -vE '^[[:space:]]*#')"
check "release.extra_files declares the evidence" "the draft actually carries it" $?

# 8 — THE PLATFORM LIMIT, RATCHETED. P0-B was a `run:` of 21,600 characters against GitHub's
# documented maximum of 21,000 ("Runs command-line programs that do not exceed 21,000
# characters", Workflow syntax reference, read 2026-08-15), and `task lint:actions` passed
# because actionlint does not model it. Measured here on every `run:` scalar of the workflow,
# with the same de-indent the platform sees. The count is in BYTES, which for the non-ASCII
# comments in these blocks is LARGER than the character count — deliberately the conservative
# direction: this can demand a smaller block than GitHub does, never a bigger one.
_max_run=0
_max_step=""
while IFS=$'\t' read -r _sz _nm; do
	[ -n "$_sz" ] || continue
	if [ "$_sz" -gt "$_max_run" ]; then
		_max_run="$_sz"
		_max_step="$_nm"
	fi
done <<<"$(awk '
	/^      - name: / { name = substr($0, 15) }
	/^        run: \|/ { inrun = 1; n = 0; next }
	inrun {
		if ($0 == "") { n += 1; next }
		if ($0 !~ /^          /) { printf "%d\t%s\n", n, name; inrun = 0; next }
		n += length(substr($0, 11)) + 1
	}
	END { if (inrun) printf "%d\t%s\n", n, name }
' "$WF_REAL")"
[ "$_max_run" -gt 0 ]
check "every run: scalar was measured" "not an empty measurement" $?
[ "$_max_run" -le 21000 ]
check "the largest run: is within GitHub's 21,000-character limit" "$_max_run chars — $_max_step" $?
# A LIMIT WITH NO MARGIN IS A LIMIT ABOUT TO BE CROSSED AGAIN: this ratchets at 85% so the
# block cannot creep back to the edge one comment at a time.
[ "$_max_run" -le 17850 ]
check "and it keeps a margin (<=85% of the limit)" "$_max_run of 17850" $?

echo
echo "== summary =="
printf 'pass=%d fail=%d\n' "$pass" "$fail"
if [ "$fail" -ne 0 ]; then
	printf 'failed: %s\n' "${failed_names[*]}"
	echo "test-release-workspace-e2e: RED"
	exit 1
fi
echo "test-release-workspace-e2e: OK — ${pass} cases, real sequence under the real .gitignore"
