#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-release-finalize-stable.sh — battery for scripts/release-finalize-stable.sh, the
# operation that turns a verified draft into a published stable release (QA07).
#
# ⛔ WHAT MAKES THIS BATTERY WORTH ANYTHING, said first because it is the only interesting
# design decision in it.
#
# The thing under test decides whether bytes become public. A fixture that answered "yes" to
# every verification would let every case pass while proving nothing, so NOTHING here fakes
# a verifier:
#
#   · the OTA signatures are REAL Ed25519 signatures over the REAL manifest bytes, produced
#     by the REAL `olivares release sign-manifest` with throwaway keys generated per run;
#   · the verification is performed by a REAL build of cmd/olivares with one of those public
#     keys linked in as its EMBEDDED OTA anchor (-X core/release.artifactVerifyKeyB64), so
#     the `--pubkey`-less direction is the same code path a user's own binary runs — the
#     shape TestReleaseVerifyManifestSignatureUsesEmbeddedAnchor establishes at the Go seam
#     (cmd/olivares/cmd_release_manifest_test.go:242);
#   · a SECOND, independent key exists for the refusal direction, so "it refused" is a
#     cryptographic fact and not a missing file;
#   · the private halves never leave the run's temporary directory, are never printed, and
#     are never written to the repository.
#
# What IS stubbed is the infrastructure, not the OTA judgement: `gh` is a recording GitHub
# adapter that serves fixture bytes and remembers every request, `curl` serves the same
# bytes, and `go` execs the binary that was already built. The GitHub adapter RECORDS, so
# "was a publish PATCH issued" is measured rather than inferred from an exit code — which
# matters, because almost every case here asserts that no PATCH happened.
#
# TWO ADAPTERS ARE WIRING ONLY, AND THIS IS THE LABEL. `cosign` and `slsa-verifier` return a
# configured exit code: Sigstore needs a network and a Fulcio identity this battery has no
# business obtaining, and SLSA provenance signatures need the same trust material. Rows that
# pass through them prove WHICH checks the finalizer runs, in what order, and what it does
# when one is absent or rejects. They prove nothing cryptographic and are never evidence
# that a cosign or SLSA signature is valid. The cryptography that IS real here is the OTA
# Ed25519 path described above, end to end.
#
# BOTH DIRECTIONS, ALWAYS. A finalizer that refused everything would satisfy every "it
# refuses" row. Case A is the complete candidate and it must PUBLISH; the mutation section
# at the end removes a verification and a publication guard from a COPY and requires this
# battery to go red, which is what establishes that the refusals above are produced by the
# real effect path.
#
# Hermetic: no network, no repository writes, nothing signed with a real key, no release
# mutated anywhere but in this fixture's own state directory.
#
# NO `set -e` (battery reports through check(); see test-pg-test-env.sh).
set -uo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# GIT ISOLATION — this battery builds a throwaway git tree inside what may be a LINKED
# WORKTREE, where git exports GIT_DIR and GIT_DIR outranks `-C`.
_olivares_git_env="${ROOT}/scripts/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

TAG="v26.9.0"
VERSION="26.9.0"
REPO="olivaresai/olivares"
REPO_ID="987654321"
RUN_ID="5150515051"
RUN_ATTEMPT="1"
RELEASE_ID="42424242"

blind() {
	echo "test-release-finalize-stable: NO HE PODIDO MIRAR: $*" >&2
	exit 2
}

for tool in git jq openssl tar sha256sum go cmp base64; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done

WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-finalize.XXXXXX")" || blind "cannot create a work directory"
# ${TMPDIR:-/tmp} may be mounted noexec (this dev container's /tmp is) and this battery runs
# PATH-stubbed binaries AND a freshly built product binary: there, execve returns EACCES and
# every case that reaches them fails while the rest pass — the exact half-green that made
# three sibling batteries unrunnable on this host until they grew this probe.
printf '#!/bin/sh\nexit 0\n' >"$WORK/.execprobe" && chmod +x "$WORK/.execprobe"
if ! "$WORK/.execprobe" >/dev/null 2>&1; then
	rm -rf "$WORK"
	WORK="$(mktemp -d "$ROOT/.tmpexec.XXXXXX")" || blind "cannot create an executable work directory"
fi
rm -f "$WORK/.execprobe"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0
failed_names=()
# The FAIL line starts at COLUMN 0, like test-release-ota-channel.sh: the mutation round
# below attributes a kill by matching `^FAIL` in this output, and an indented word would
# report a battery that reddens correctly as one that never reddened.
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok  %-62s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		failed_names+=("$1")
		printf 'FAIL  %-62s %s\n' "$1" "$2"
	fi
}

echo "release finalization — the real verifier, real signatures, a recording GitHub adapter"

# --- throwaway keys ------------------------------------------------------------------------
# TWO INDEPENDENT KEYS, generated per run and never leaving this directory. The first becomes
# the shipped binary's embedded anchor and the configured public anchor; the second exists so
# "the signature does not verify" is a cryptographic refusal rather than an absent file.
KEYS="$WORK/keys"
mkdir -p "$KEYS" || blind "cannot create the key directory"
chmod 700 "$KEYS" 2>/dev/null || true
make_key() { # make_key <name>  -> writes <name>.seed.b64 and <name>.pub.b64
	openssl genpkey -algorithm ed25519 -out "$KEYS/$1.pem" 2>/dev/null || return 1
	openssl pkey -in "$KEYS/$1.pem" -outform DER -out "$KEYS/$1.priv.der" 2>/dev/null || return 1
	openssl pkey -in "$KEYS/$1.pem" -pubout -outform DER -out "$KEYS/$1.pub.der" 2>/dev/null || return 1
	# An Ed25519 PKCS#8 DER is 48 bytes ending in the 32-byte seed; the SPKI is 44 bytes
	# ending in the 32-byte public key. Sliced rather than parsed, and the lengths are
	# asserted so a different OpenSSL encoding fails loudly instead of producing 32 bytes of
	# something else.
	[ "$(wc -c <"$KEYS/$1.priv.der")" -eq 48 ] || return 1
	[ "$(wc -c <"$KEYS/$1.pub.der")" -eq 44 ] || return 1
	tail -c 32 "$KEYS/$1.priv.der" | base64 -w0 >"$KEYS/$1.seed.b64" || return 1
	tail -c 32 "$KEYS/$1.pub.der" | base64 -w0 >"$KEYS/$1.pub.b64" || return 1
	chmod 600 "$KEYS/$1.seed.b64" 2>/dev/null || true
	return 0
}
make_key ota || blind "could not generate the throwaway OTA key"
make_key other || blind "could not generate the independent refusal key"
OTA_PUB="$(cat "$KEYS/ota.pub.b64")"
[ -n "$OTA_PUB" ] || blind "the throwaway OTA public key is empty"
[ "$OTA_PUB" != "$(cat "$KEYS/other.pub.b64")" ] || blind "the two throwaway keys are identical"

# --- the product binary, with the throwaway anchor LINKED IN --------------------------------
# This is the only expensive step and it is the reason the battery means anything: the
# `--pubkey`-less verification below is the code path that runs on a user's machine, and it
# can only be exercised by a build that carries an anchor.
BIN="$WORK/olivares"
if ! (cd "$ROOT" && go build \
	-ldflags "-s -w -X github.com/olivaresai/olivares/core/release.artifactVerifyKeyB64=${OTA_PUB}" \
	-o "$BIN" ./cmd/olivares) >"$WORK/build.log" 2>&1; then
	echo "test-release-finalize-stable: NO HE PODIDO MIRAR: the anchored product build failed." >&2
	tail -20 "$WORK/build.log" >&2
	exit 2
fi
# THE ANCHOR IS ASSERTED, NOT ASSUMED. A build that silently embedded nothing would make
# every `--pubkey`-less case fail with ErrNoKey and read as "the finalizer refuses", which is
# the wrong diagnosis from the right symptom.
"$BIN" version 2>/dev/null | command grep 'ota-key=release/' >/dev/null ||
	blind "the built binary reports no embedded OTA anchor; the shipped-anchor direction would be vacuous"

# --- the fixture checkout -------------------------------------------------------------------
# `git archive HEAD` is the COMMITTED tree, so the scripts under test are overlaid from the
# working copy: without that, this battery would measure whatever was last committed.
TREE="$WORK/tree"
mkdir -p "$TREE" || blind "cannot create the fixture tree"
(cd "$ROOT" && git archive HEAD) | tar -x -C "$TREE" || blind "cannot populate the fixture tree"
for _s in release-finalize-stable.sh verify-release.sh cosign-verified.sh \
	assert-cosign-binary.sh release-ota-channel.sh; do
	cp "$ROOT/scripts/$_s" "$TREE/scripts/$_s" || blind "cannot overlay scripts/$_s"
done
rm -f "$TREE/release/advisories/${VERSION}.txt"
(
	cd "$TREE" &&
		git init -q . &&
		git config user.email fixture@example.invalid &&
		git config user.name fixture &&
		git config commit.gpgsign false &&
		git add -A >/dev/null 2>&1 &&
		git commit -q -m fixture &&
		git tag -f "$TAG" >/dev/null 2>&1
) || blind "cannot initialise the fixture git tree"
COMMIT="$(git -C "$TREE" rev-parse HEAD)" || blind "cannot read the fixture commit"
[ "${#COMMIT}" -eq 40 ] || blind "the fixture commit is not a full OID"

# THE SCRIPT UNDER TEST IS A COPY WITH ONE LINE REDIRECTED. `TRUSTED_BIN="/usr/bin"` is
# absolute and has no environment override on purpose — a control must not be configurable by
# the thing it guards against — so the harness rewrites a copy rather than asking production
# for a seam. That the real file HAS no seam is asserted, because a copy that needed no
# rewrite would mean the seam had reappeared.
SUT="$TREE/scripts/release-finalize-stable.sh"
# ⛔ AND IT IS RE-APPLIED AFTER ANY `git reset --hard` ON THE FIXTURE. The redirected copy is
# a WORKING-TREE modification of a tracked file, so a reset restores the committed
# `/usr/bin` line and every later case silently resolves the REAL gh — which fails in a way
# that looks like thirty defects in the finalizer. Measured on this battery's second run:
# one `git reset --hard` in the moved-tag case turned 32 subsequent rows red.
restore_sut() {
	cp "$ROOT/scripts/release-finalize-stable.sh" "$SUT" || return 1
	sed -i 's#^TRUSTED_BIN="/usr/bin"$#TRUSTED_BIN="'"$WORK"'/trusted"#' "$SUT"
}
restore_sut || blind "cannot install the redirected copy"
command grep -q "TRUSTED_BIN=\"$WORK/trusted\"" "$SUT"
check "the battery runs a MUTATED COPY of the finalizer" "no env seam in production" $?
_seam="$(command grep -vE '^[[:space:]]*#' "$ROOT/scripts/release-finalize-stable.sh" |
	command grep -cE 'OLIVARES_(TRUSTED_BIN|FINALIZE|SKIP|FORCE)' || true)"
[ "${_seam:-0}" -eq 0 ]
check "production has no trust-bypass or executable override" "not configurable by the attacker" $?

# ============================================================================================
# RECIPE DRIFT. The finalizer's required inventory is DERIVED from the release recipe, which
# means the derivation is only true while the recipe is what it was. These rows read the two
# files that own the recipe and compare them against the constants the finalizer carries: add
# a platform to .goreleaser.yaml, rename a sidecar in release.yml, drop a package format, and
# the finalizer would start demanding — or stop demanding — the wrong set, silently. Nothing
# here needs a fixture, so it runs first and costs nothing.
# ============================================================================================
GR="$ROOT/.goreleaser.yaml"
WF="$ROOT/.github/workflows/release.yml"
FIN="$ROOT/scripts/release-finalize-stable.sh"

recipe_const() { # recipe_const <NAME>  -> the array's values, space-separated
	command sed -n "s/^$1=(\(.*\))\$/\1/p" "$FIN" | head -1
}
yaml_flow_set() { # yaml_flow_set <file> <key>  -> sorted unique flow-sequence values
	command grep -hoE "^[[:space:]]*$2:[[:space:]]*\[[^]]*\]" "$1" |
		command sed -E "s/^[[:space:]]*$2:[[:space:]]*\[//; s/\]//; s/,/ /g" |
		tr ' ' '\n' | command grep -v '^$' | LC_ALL=C sort -u | tr '\n' ' ' | command sed 's/ $//'
}
sorted_words() { printf '%s\n' $1 | LC_ALL=C sort -u | tr '\n' ' ' | command sed 's/ $//'; }

_r_goos="$(sorted_words "$(recipe_const RECIPE_GOOS)")"
_y_goos="$(yaml_flow_set "$GR" goos)"
[ -n "$_r_goos" ] && [ "$_r_goos" = "$_y_goos" ]
check "the recipe's GOOS set matches .goreleaser.yaml" "drift: '$_r_goos' vs '$_y_goos'" $?

_r_goarch="$(sorted_words "$(recipe_const RECIPE_GOARCH)")"
_y_goarch="$(yaml_flow_set "$GR" goarch)"
[ -n "$_r_goarch" ] && [ "$_r_goarch" = "$_y_goarch" ]
check "the recipe's GOARCH set matches .goreleaser.yaml" "drift: '$_r_goarch' vs '$_y_goarch'" $?

_r_fmt="$(sorted_words "$(recipe_const RECIPE_PACKAGE_FORMATS)")"
_y_fmt="$(yaml_flow_set "$GR" formats | tr ' ' '\n' | command grep -v 'tar\.gz' | tr '\n' ' ' | command sed 's/ $//')"
[ -n "$_r_fmt" ] && [ "$_r_fmt" = "$_y_fmt" ]
check "the recipe's package formats match nfpms" "drift: '$_r_fmt' vs '$_y_fmt'" $?

# The archive NAMES the finalizer builds must be the templates GoReleaser renders. The base
# entry and the `_fips_` variant are asserted separately: a collapsed pair would make the
# FIPS archives invisible to the completeness check while every digest still agreed.
command grep -qF 'name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"' "$GR"
check "the base archive template is the name the finalizer derives" "olivares_<v>_<os>_<arch>.tar.gz" $?
command grep -qF 'name_template: "{{ .ProjectName }}_{{ .Version }}_fips_{{ .Os }}_{{ .Arch }}"' "$GR"
check "the FIPS archive template carries the _fips_ segment" "a separate, required variant" $?
command grep -qF '{{ .ArtifactName }}.spdx.sbom.json' "$GR" &&
	command grep -qF '{{ .ArtifactName }}.cdx.sbom.json' "$GR"
check "both checksummed SBOM sidecars are declared per archive" "two documents, one recipe" $?
command grep -qF 'dist/olivares-install-*.sh' "$GR"
check "the versioned installer is a declared extra file" "the finalizer requires it by name" $?

# The sidecars the WORKFLOW uploads after goreleaser has written checksums.txt. These are the
# members the signed set deliberately does not cover, and the finalizer requires each by name.
command grep -qF '"${a}.sbom.sigstore.json"' "$WF"
check "release.yml uploads a per-archive SBOM attestation" "required, not optional" $?
command grep -qF '"${a}.vex.sigstore.json" "${a}.vex.openvex.json"' "$WF"
check "release.yml uploads both per-archive VEX sidecars" "required, not optional" $?
command grep -qF 'gh release upload "${RELEASE_TAG}" olivares.vex.openvex.json image.spdx.sbom.json' "$WF"
check "release.yml uploads the two top-level documents" "required, not optional" $?
# And every one of those names appears in the finalizer's required list, which is the other
# half of the comparison: a sidecar the workflow produces and the finalizer never asks for is
# a member nobody would notice missing.
for _nm in sbom.sigstore.json vex.sigstore.json vex.openvex.json olivares.vex.openvex.json image.spdx.sbom.json; do
	command grep -qF "$_nm" "$FIN" || { _drift="$_nm"; break; }
done
[ -z "${_drift:-}" ]
check "the finalizer requires every sidecar release.yml produces" "${_drift:-all present}" $?

# --- the recording GitHub adapter and the other stubs ---------------------------------------
mkdir -p "$WORK/bin" "$WORK/trusted" "$WORK/runnertmp" || blind "cannot create the stub directories"
STATE="$WORK/state"
ASSETS="$STATE/assets"
ARCHIVE_CACHE="$WORK/archive-cache"
# ⛔ WHICH FIXTURE CARRIES THE REAL PRODUCT, AND WHY IT IS NOT ALWAYS.
#
# The linux/amd64 archive is the one the finalizer extracts and RUNS, so it must hold the
# real anchored binary for every case that reaches that step. It is also ~120 MB, and the
# finalizer legitimately downloads and re-hashes it on every run: at forty-odd fixtures that
# is minutes of gzip and sha256 for cases that refuse long before the binary is touched, and
# a battery slow enough to be resented is a battery that gets moved out of the gate.
#
# So sections D, E and F — which all refuse during source/run binding or inventory admission,
# strictly BEFORE the OTA verification — use a tiny stand-in, and THE CHOICE GUARDS ITSELF:
# every one of those rows asserts a SPECIFIC refusal message, so a regression that let the
# flow reach the binary would produce the shipped-anchor message instead and redden the row.
# The row immediately below proves that stand-in cannot pass on its own.
FIXTURE_BINARY="real"

cat >"$WORK/bin/gh" <<'GHEOF'
#!/usr/bin/env bash
# A RECORDING adapter, not a yes-man. Every invocation is logged argument by argument (a
# joined "$*" erases where one argument ends and the next begins), every route is matched
# explicitly, and an unrecognised call is an error rather than a silent success — a stub that
# answers whatever it is asked cannot see a caller that asked the wrong question.
{
	printf 'ARGV %s\n' "$#"
	for _a in "$@"; do printf 'ARG %s\n' "$_a"; done
} >>"${GH_LOG}"

route=""
jqexpr=""
method="GET"
prev=""
for a in "$@"; do
	case "$prev" in
	--jq) jqexpr="$a" ;;
	--method) method="$a" ;;
	esac
	case "$a" in
	repos/*) route="$a" ;;
	esac
	prev="$a"
done

emit() { # emit <file>
	if [ -n "$jqexpr" ]; then jq -c -r "$jqexpr" "$1"; else cat "$1"; fi
}

case "${1:-}" in
api) ;;
release)
	# `gh release view <tag> --json isDraft --jq ...` — the workflow's reconciliation step.
	case "${2:-}" in
	view)
		draft="$(jq -r '.draft' "${GH_STATE}/release.json")"
		printf 'DRAFT %s\n' "$draft"
		exit "${GH_VIEW_RC:-0}"
		;;
	esac
	echo "gh stub: unsupported release subcommand: ${2:-}" >&2
	exit 97
	;;
*)
	echo "gh stub: unsupported command: ${1:-}" >&2
	exit 97
	;;
esac

case "$route" in
*/releases\?per_page=*)
	# THE LIST, which is the only route that can see a DRAFT. GH_RELEASES_LIST_FILE serves a
	# hand-made inventory (two releases for one tag, none, or a non-array); by default the
	# fixture release is the single element, rebuilt on every call so the reconciliation
	# rows see the PATCH that already happened.
	[ "${GH_RELEASE_RC:-0}" -eq 0 ] || { echo "gh: HTTP 503 (stub)" >&2; exit "${GH_RELEASE_RC}"; }
	if [ -n "${GH_RELEASES_LIST_FILE:-}" ]; then
		emit "${GH_RELEASES_LIST_FILE}"
	else
		jq -s '.' "${GH_STATE}/release.json" >"${GH_STATE}/releases-list.json" &&
			emit "${GH_STATE}/releases-list.json"
	fi
	;;
*/releases/tags/*)
	# ⛔ GITHUB ANSWERS 404 BY TAG FOR A DRAFT, and this stub says so instead of serving the
	# object. That route resolves through the git ref and a draft has none — the measurement
	# that made the finalizer read the list (2026-09-17, v26.9.0). A revert to the tag
	# endpoint must redden the nominal publication row, not quietly keep passing here.
	[ "${GH_RELEASE_RC:-0}" -eq 0 ] || { echo "gh: HTTP 503 (stub)" >&2; exit "${GH_RELEASE_RC}"; }
	if [ "$(jq -r '.draft' "${GH_STATE}/release.json")" = "true" ]; then
		echo "gh: Not Found (HTTP 404) (stub: a draft has no tag to resolve)" >&2
		exit 1
	fi
	emit "${GH_STATE}/release.json"
	;;
*/releases/latest)
	[ "${GH_LATEST_RC:-0}" -eq 0 ] || { echo "gh: HTTP 404 (stub)" >&2; exit "${GH_LATEST_RC}"; }
	emit "${GH_STATE}/latest.json"
	;;
*/releases/assets/*)
	id="${route##*/}"
	name="$(jq -r --arg i "$id" 'map(select((.id|tostring) == $i)) | .[0].name // empty' "${GH_STATE}/assets.json")"
	[ -n "$name" ] || { echo "gh: HTTP 404 no asset id $id (stub)" >&2; exit 1; }
	[ "${GH_FETCH_RC:-0}" -eq 0 ] || { echo "gh: HTTP 500 (stub)" >&2; exit "${GH_FETCH_RC}"; }
	# TRANSIENT failures, per asset name: the first N attempts drop the transfer
	# (GH_FETCH_DROP_FIRST) or deliver a short read (GH_FETCH_SHORT_FIRST), and the attempt
	# after that succeeds. A link that dies mid-download is what the retry loop exists for,
	# and a stub that only ever fails permanently could not tell a retry from a refusal.
	if [ -n "${GH_FETCH_FLAKY_NAME:-}" ] && [ "$name" = "${GH_FETCH_FLAKY_NAME}" ]; then
		tries="$(cat "${GH_STATE}/fetch-tries-${name}" 2>/dev/null || echo 0)"
		tries=$((tries + 1))
		printf '%s' "$tries" >"${GH_STATE}/fetch-tries-${name}"
		if [ "$tries" -le "${GH_FETCH_DROP_FIRST:-0}" ]; then
			echo "gh: connection reset by peer (stub)" >&2
			exit 1
		fi
		if [ "$tries" -le "${GH_FETCH_SHORT_FIRST:-0}" ]; then
			head -c 3 "${GH_STATE}/assets/$name"
			exit 0
		fi
	fi
	cat "${GH_STATE}/assets/$name"
	;;
*/releases/*/assets*)
	[ "${GH_ASSETS_RC:-0}" -eq 0 ] || { echo "gh: HTTP 503 (stub)" >&2; exit "${GH_ASSETS_RC}"; }
	# A SECOND READ MAY DIFFER, which is the whole point of the re-read before publishing.
	if [ -n "${GH_ASSETS_SWAP_AFTER:-}" ]; then
		reads="$(cat "${GH_STATE}/asset-reads" 2>/dev/null || echo 0)"
		reads=$((reads + 1))
		printf '%s' "$reads" >"${GH_STATE}/asset-reads"
		if [ "$reads" -gt "${GH_ASSETS_SWAP_AFTER}" ] && [ -f "${GH_STATE}/assets-swapped.json" ]; then
			emit "${GH_STATE}/assets-swapped.json"
			exit 0
		fi
	fi
	emit "${GH_STATE}/assets.json"
	;;
*/actions/runs/*/attempts/*)
	[ "${GH_RUN_RC:-0}" -eq 0 ] || { echo "gh: HTTP 404 (stub)" >&2; exit "${GH_RUN_RC}"; }
	emit "${GH_STATE}/run.json"
	;;
*/releases/*)
	if [ "$method" = "PATCH" ]; then
		printf 'PATCH %s\n' "$route" >>"${GH_STATE}/patches"
		for a in "$@"; do printf 'PATCHARG %s\n' "$a" >>"${GH_STATE}/patches"; done
		[ "${GH_PATCH_RC:-0}" -eq 0 ] || { echo "gh: HTTP 502 (stub)" >&2; exit "${GH_PATCH_RC}"; }
		jq '.draft = false' "${GH_STATE}/release.json" >"${GH_STATE}/release.next" &&
			mv "${GH_STATE}/release.next" "${GH_STATE}/release.json"
		cp "${GH_STATE}/release.json" "${GH_STATE}/latest.json"
		emit "${GH_STATE}/release.json"
		exit 0
	fi
	# ⛔ ONLY AFTER THE PATCH. The finalizer reads this same route BEFORE publishing, to
	# confirm the release did not move; failing that read would produce "could not look"
	# and no PATCH at all — a different case entirely. The shape under test is "the write
	# applied and the confirmation did not come back".
	if [ "${GH_READBACK_RC:-0}" -ne 0 ] && command grep -q '^PATCH ' "${GH_STATE}/patches" 2>/dev/null; then
		echo "gh: HTTP 500 (stub)" >&2
		exit "${GH_READBACK_RC}"
	fi
	if [ -n "${GH_READBACK_FILE:-}" ]; then emit "${GH_READBACK_FILE}"; else emit "${GH_STATE}/release.json"; fi
	;;
*)
	echo "gh stub: unexpected route: '${route}'" >&2
	exit 97
	;;
esac
exit 0
GHEOF

cat >"$WORK/bin/curl" <<'CURLEOF'
#!/usr/bin/env bash
# Serves the same fixture bytes the adapter does, from the URL's basename: the finalizer's
# delivery postcondition is "a consumer can fetch exactly these bytes", and a stub that
# invented its own would be answering a different question.
out=""
url=""
prev=""
for a in "$@"; do
	case "$prev" in --output) out="$a" ;; esac
	case "$a" in http*) url="$a" ;; esac
	prev="$a"
done
[ "${CURL_RC:-0}" -eq 0 ] && [ -z "${CURL_FAIL_FOR:-}" ] || {
	case "${CURL_FAIL_FOR:-}" in
	"") echo "curl: (22) stub failure" >&2; exit "${CURL_RC:-22}" ;;
	*) case "$url" in *"${CURL_FAIL_FOR}") echo "curl: (22) stub failure" >&2; exit 22 ;; esac ;;
	esac
}
name="${url##*/}"
src="${GH_STATE}/assets/${name}"
if [ -n "${CURL_SERVE_DIR:-}" ] && [ -f "${CURL_SERVE_DIR}/${name}" ]; then src="${CURL_SERVE_DIR}/${name}"; fi
[ -f "$src" ] || { echo "curl: (22) 404 for ${url}" >&2; exit 22; }
if [ -n "$out" ]; then cp "$src" "$out"; else cat "$src"; fi
exit 0
CURLEOF

cat >"$WORK/bin/cosign" <<'COSIGNEOF'
#!/usr/bin/env bash
# Sigstore verification needs a network and a Fulcio identity this battery has no business
# obtaining. What it CAN do honestly is record the arguments, so the identity anchor the
# finalizer builds is measurable, and refuse when the case asks it to.
#
# ⛔ THE ATTESTATION CALLS ARE RECORDED TOO (root return 02, F4). They were not, and they are
# the ones scripts/verify-release.sh makes: while they went unlogged, the anchor the DELEGATED
# verification used was invisible to this battery, and it was a production literal that no
# other profile could ever satisfy.
#
# COSIGN_EXPECT_IDENTITY models the one negative control real cosign performs and a bare exit
# code cannot: a certificate whose identity is not the expected one does not verify. Without
# it every row below would pass against any anchor at all.
identity_of() {
	local prev="" a got=""
	for a in "$@"; do
		[ "$prev" = "--certificate-identity-regexp" ] && got="$a"
		prev="$a"
	done
	printf '%s' "$got"
}
case "${1:-}" in
version) printf 'GitVersion:    %s\n' "v2.6.4"; exit 0 ;;
verify-blob | verify-blob-attestation)
	{ printf 'COSIGN'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; } >>"${COSIGN_LOG:-/dev/null}"
	if [ -n "${COSIGN_EXPECT_IDENTITY:-}" ]; then
		got="$(identity_of "$@")"
		if [ "$got" != "${COSIGN_EXPECT_IDENTITY}" ]; then
			echo "cosign: no matching signatures: certificate identity '${got}' is not '${COSIGN_EXPECT_IDENTITY}'" >&2
			exit 1
		fi
	fi
	case "${1}" in
	verify-blob) exit "${COSIGN_RC:-0}" ;;
	*) exit "${COSIGN_ATT_RC:-0}" ;;
	esac
	;;
esac
exit 0
COSIGNEOF

cat >"$WORK/bin/slsa-verifier" <<'SLSAEOF'
#!/usr/bin/env bash
# Records its argv for the same reason the cosign adapter does: --source-uri is half of the
# identity the delegated verification is made under, and it was a production literal too.
{ printf 'SLSA'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; } >>"${SLSA_LOG:-/dev/null}"
exit "${SLSA_RC:-0}"
SLSAEOF

# `go run ./cmd/olivares <args>` execs the binary built above, with the SAME arguments. It is
# not a verifier stub: the product's own code decides every verdict.
cat >"$WORK/bin/go" <<GOEOF
#!/usr/bin/env bash
if [ "\${1:-}" = "run" ] && [ "\${2:-}" = "./cmd/olivares" ]; then
	shift 2
	exec "$BIN" "\$@"
fi
echo "go stub: unsupported invocation: \$*" >&2
exit 97
GOEOF

chmod +x "$WORK/bin/gh" "$WORK/bin/curl" "$WORK/bin/cosign" "$WORK/bin/slsa-verifier" "$WORK/bin/go" ||
	blind "cannot make the stubs executable"
for t in jq tar base64 cmp; do
	ln -sf "$(command -v "$t")" "$WORK/bin/$t" || blind "cannot link $t"
done
# THE PHASE-2 LAYOUT, AS A SECOND BIN DIRECTORY. `assert-cosign-binary.sh --isolate` moves
# the authenticated binary to a private directory and refuses to report success while the bare
# name still resolves, so in the supported job `cosign` is NOT on PATH and OLIVARES_COSIGN_BIN
# names the bytes. This directory is $WORK/bin with that one name removed — no more, because a
# PATH narrowed further than the job narrows it would measure a different environment.
mkdir -p "$WORK/bin-noc" || blind "cannot create the isolated bin directory"
for _b in "$WORK"/bin/*; do
	case "${_b##*/}" in cosign) continue ;; esac
	ln -sf "$_b" "$WORK/bin-noc/${_b##*/}" || blind "cannot link ${_b##*/} into the isolated bin"
done
ln -sf /usr/bin/git "$WORK/trusted/git"
ln -sf /usr/bin/sha256sum "$WORK/trusted/sha256sum"
ln -sf "$WORK/bin/gh" "$WORK/trusted/gh"

# THE STUBS MUST START, and that is checked before anything is measured with them. A stub
# that cannot execve produces "could not read a version" style failures that read as defects
# in the code under test — the wrong diagnosis from the right symptom, and a sibling battery
# lost two runs to exactly this on two different runners.
"$WORK/trusted/gh" --version >/dev/null 2>&1
[ "$?" -ne 126 ] || blind "the gh stub cannot be executed in ${WORK} (noexec?)"
"$WORK/bin/go" 2>/dev/null
[ "$?" -ne 126 ] || blind "the go stub cannot be executed in ${WORK} (noexec?)"

# --- building a release state ---------------------------------------------------------------
# Everything a real phase 1 + the signing step would have left on the release, derived from
# the same recipe the finalizer derives its expectations from. Built as FILES so a case can
# remove, corrupt or duplicate exactly one member and change nothing else.
BASE_ARCHIVES=()
FIPS_ARCHIVES=()
for goos in linux darwin; do
	for goarch in amd64 arm64; do
		BASE_ARCHIVES+=("olivares_${VERSION}_${goos}_${goarch}.tar.gz")
		FIPS_ARCHIVES+=("olivares_${VERSION}_fips_${goos}_${goarch}.tar.gz")
	done
done

# CTX_EVENT is what the fixture's build context RECORDS. It is `push` for every row but the
# two that check the consumer's refusal of anything else: since root return 02 F2 the producer
# records the true event, so refusing a rehearsal's artifact set is the finalizer's job.
CTX_EVENT="push"
build_context_bytes() {
	printf '{"schema":"olivares.ai/release-build-context/v1","schema_version":1,"repository_id":"%s","repository":"%s","event":"%s","ref":"%s","commit":"%s","run_id":"%s","run_attempt":"%s"}\n' \
		"$REPO_ID" "$REPO" "$CTX_EVENT" "refs/tags/${TAG}" "$COMMIT" "$RUN_ID" "$RUN_ATTEMPT"
}

rebuild_assets_json() { # every file in $ASSETS becomes an asset with a deterministic id
	local i=1000
	: >"$STATE/assets.ndjson"
	while IFS= read -r f; do
		i=$((i + 1))
		local n sz d
		n="${f##*/}"
		sz="$(wc -c <"$f" | tr -d ' ')"
		d="$(sha256sum "$f")"
		d="${d%% *}"
		jq -nc --arg n "$n" --argjson id "$i" --argjson size "$sz" \
			--arg url "https://example.invalid/download/${TAG}/${n}" --arg dg "sha256:${d}" \
			'{name:$n,id:$id,size:$size,state:"uploaded",browser_download_url:$url,digest:$dg}' \
			>>"$STATE/assets.ndjson"
	done < <(find "$ASSETS" -maxdepth 1 -type f | LC_ALL=C sort)
	jq -s '.' "$STATE/assets.ndjson" >"$STATE/assets.json"
}

build_release_state() { # build_release_state [sign-key-name] [manifest-version] [manifest-channel]
	local keyname="${1:-ota}" mver="${2:-$VERSION}" mchan="${3:-stable}"
	# A security manifest that lists no advisories is refused by the product's own policy
	# bounds at SIGNING time — "stripping the advisory list is how a substituted manifest
	# hides WHAT it claims to fix". So the wrong-channel fixture has to be a WELL-FORMED
	# security manifest, which is exactly the interesting case: everything is valid and the
	# channel is still not the one this finalizer publishes.
	local chanargs=()
	[ "$mchan" = "security" ] && chanargs=(--security --advisory CVE-2026-0001)
	rm -rf "$STATE"
	mkdir -p "$ASSETS" "$STATE/stage" || return 1
	local stage="$STATE/stage"

	# ⛔ THE ARCHIVES ARE BUILT ONCE AND COPIED, not rebuilt per case. The linux/amd64 one
	# carries the REAL product binary — the finalizer extracts and RUNS it, which is the
	# embedded-anchor direction — and compressing it is the single most expensive thing this
	# battery does. Rebuilding it for each of the forty-odd fixtures cost minutes of pure
	# gzip for bytes that never change, and a slow battery is one that gets moved out of the
	# gate. The copy is byte-identical, so nothing the cases measure changes.
	local a cache="$ARCHIVE_CACHE/$FIXTURE_BINARY"
	if [ ! -f "$cache/olivares_${VERSION}_linux_amd64.tar.gz" ]; then
		mkdir -p "$cache" "$stage/pack" "$stage/tiny" || return 1
		if [ "$FIXTURE_BINARY" = "real" ]; then
			cp "$BIN" "$stage/pack/olivares" || return 1
			(cd "$stage/pack" && tar --use-compress-program='gzip -1' \
				-cf "$cache/olivares_${VERSION}_linux_amd64.tar.gz" olivares) || return 1
		else
			printf 'not a binary\n' >"$stage/tiny/olivares"
			(cd "$stage/tiny" && tar -czf "$cache/olivares_${VERSION}_linux_amd64.tar.gz" olivares) || return 1
		fi
		for a in "${BASE_ARCHIVES[@]}" "${FIPS_ARCHIVES[@]}"; do
			[ "$a" = "olivares_${VERSION}_linux_amd64.tar.gz" ] && continue
			printf 'fixture archive %s\n' "$a" >"$stage/tiny/olivares"
			(cd "$stage/tiny" && tar -czf "$cache/$a" olivares) || return 1
		done
		rm -rf "$stage/pack" "$stage/tiny"
	fi
	for a in "${BASE_ARCHIVES[@]}" "${FIPS_ARCHIVES[@]}"; do
		cp "$cache/$a" "$ASSETS/$a" || return 1
	done

	# Native packages: three formats over two architectures, in GoReleaser's conventional
	# spellings. The finalizer never asserts the spelling — it asserts format×architecture
	# coverage over the signed members — so these are representative, not authoritative.
	printf 'deb\n' >"$ASSETS/olivares_${VERSION}_linux_amd64.deb"
	printf 'deb\n' >"$ASSETS/olivares_${VERSION}_linux_arm64.deb"
	printf 'rpm\n' >"$ASSETS/olivares-${VERSION}-1.x86_64.rpm"
	printf 'rpm\n' >"$ASSETS/olivares-${VERSION}-1.aarch64.rpm"
	printf 'apk\n' >"$ASSETS/olivares_${VERSION}_x86_64.apk"
	printf 'apk\n' >"$ASSETS/olivares_${VERSION}_aarch64.apk"

	for a in "${BASE_ARCHIVES[@]}" "${FIPS_ARCHIVES[@]}"; do
		printf '{"spdx":"%s"}\n' "$a" >"$ASSETS/${a}.spdx.sbom.json"
		printf '{"cdx":"%s"}\n' "$a" >"$ASSETS/${a}.cdx.sbom.json"
	done
	printf '%s\n' "$COMMIT" >"$ASSETS/release-commit.txt"
	build_context_bytes >"$ASSETS/release-build-context.json"
	printf '#!/bin/sh\n# installer %s\n' "$VERSION" >"$ASSETS/olivares-install-${VERSION}.sh"

	# checksums.txt covers exactly the members GoReleaser checksums: archives, SBOM
	# sidecars, packages, the commit evidence, the build context and the installer.
	(
		cd "$ASSETS" &&
			sha256sum $(printf '%s\n' "${BASE_ARCHIVES[@]}" "${FIPS_ARCHIVES[@]}" | LC_ALL=C sort) \
				*.spdx.sbom.json *.cdx.sbom.json \
				olivares_${VERSION}_linux_amd64.deb olivares_${VERSION}_linux_arm64.deb \
				olivares-${VERSION}-1.x86_64.rpm olivares-${VERSION}-1.aarch64.rpm \
				olivares_${VERSION}_x86_64.apk olivares_${VERSION}_aarch64.apk \
				release-commit.txt release-build-context.json "olivares-install-${VERSION}.sh" \
				>"$stage/checksums.txt"
	) || return 1
	cp "$stage/checksums.txt" "$ASSETS/checksums.txt"
	printf 'stub-cosign-signature\n' >"$ASSETS/checksums.txt.sig"
	printf 'stub-cosign-certificate\n' >"$ASSETS/checksums.txt.pem"

	# The manifest is produced and signed by the REAL product, over the REAL archive bytes.
	"$BIN" release manifest --dir "$ASSETS" --channel "$mchan" --version "$mver" \
		"${chanargs[@]+"${chanargs[@]}"}" \
		--expires-in 2160h --out "$ASSETS/stable-manifest.json" >/dev/null 2>&1 || return 1
	"$BIN" release sign-manifest --manifest "$ASSETS/stable-manifest.json" \
		--checksums "$ASSETS/checksums.txt" --sign-key "@$KEYS/${keyname}.seed.b64" \
		--out "$ASSETS/stable-manifest.json.sig" >/dev/null 2>&1 || return 1
	printf 'stub-pipeline-signature\n' >"$ASSETS/stable-manifest.json.pipeline.sig"
	printf 'stub-pipeline-certificate\n' >"$ASSETS/stable-manifest.json.pipeline.pem"

	for a in "${BASE_ARCHIVES[@]}" "${FIPS_ARCHIVES[@]}"; do
		printf '{"bundle":"sbom"}\n' >"$ASSETS/${a}.sbom.sigstore.json"
		printf '{"bundle":"vex"}\n' >"$ASSETS/${a}.vex.sigstore.json"
		printf '{"openvex":true}\n' >"$ASSETS/${a}.vex.openvex.json"
	done
	printf '{"openvex":"top"}\n' >"$ASSETS/olivares.vex.openvex.json"
	printf '{"spdx":"image"}\n' >"$ASSETS/image.spdx.sbom.json"

	# A real DSSE envelope whose in-toto subjects are the archive digests. The finalizer
	# decodes the payload and requires every released archive to be a subject, so an envelope
	# that named other bytes would not pass — which is what makes this fixture a fixture and
	# not a rubber stamp.
	: >"$stage/subjects.ndjson"
	for a in "${BASE_ARCHIVES[@]}" "${FIPS_ARCHIVES[@]}"; do
		local d
		d="$(sha256sum "$ASSETS/$a")"
		d="${d%% *}"
		jq -nc --arg n "$a" --arg d "$d" '{name:$n,digest:{sha256:$d}}' >>"$stage/subjects.ndjson"
	done
	jq -s '{_type:"https://in-toto.io/Statement/v1",subject:.,predicateType:"https://slsa.dev/provenance/v1"}' \
		"$stage/subjects.ndjson" >"$stage/statement.json" || return 1
	# THE CARRIER THE PINNED GENERATOR ACTUALLY EMITS IS THE DEFAULT. Established against the
	# real upstream corpus: generator_generic_slsa3.yml@v2.1.0 produces a Sigstore bundle whose
	# in-toto statement is nested under .dsseEnvelope, not a bare DSSE envelope. A fixture that
	# modelled only the bare shape is why step 7 refused our own releases.
	case "${PROVENANCE_SHAPE:-bundle}" in
	bundle)
		jq -nc --arg p "$(base64 -w0 <"$stage/statement.json")" \
			'{mediaType:"application/vnd.dev.sigstore.bundle.v0.3+json",
			  verificationMaterial:{tlogEntries:[]},
			  dsseEnvelope:{payloadType:"application/vnd.in-toto+json",payload:$p,
			                signatures:[{sig:"stub"}]}}' \
			>"$ASSETS/multiple.intoto.jsonl" || return 1
		;;
	dsse)
		jq -nc --arg p "$(base64 -w0 <"$stage/statement.json")" \
			'{payloadType:"application/vnd.in-toto+json",payload:$p,signatures:[{sig:"stub"}]}' \
			>"$ASSETS/multiple.intoto.jsonl" || return 1
		;;
	*) return 1 ;;
	esac

	jq -nc --argjson id "$RELEASE_ID" --arg tag "$TAG" --arg tc "main" \
		'{id:$id,tag_name:$tag,draft:true,prerelease:false,immutable:false,target_commitish:$tc}' \
		>"$STATE/release.json"
	jq -nc --argjson id "$RELEASE_ID" --arg tag "$TAG" '{id:$id,tag_name:$tag,draft:false}' >"$STATE/latest.json"
	jq -nc --arg repo "$REPO" --argjson rid "$REPO_ID" --arg sha "$COMMIT" \
		--argjson attempt "$RUN_ATTEMPT" --arg ev "$CTX_EVENT" \
		'{repository:{full_name:$repo,id:$rid},path:".github/workflows/release.yml",
		  event:$ev,head_sha:$sha,run_attempt:$attempt,status:"completed",conclusion:"success"}' \
		>"$STATE/run.json"
	: >"$STATE/patches"
	rm -f "$STATE/asset-reads" "$STATE/assets-swapped.json"
	rebuild_assets_json
	stage_attached_pair
}

# THE ARCHIVE THE FINALIZER EXTRACTS AND RUNS, with a hostile member in SECOND position. The
# position IS the finding (root return 02, F7): the guard matched the newline-joined listing
# with `/*`, which `case` anchors at the start of the WHOLE string, so it only ever inspected
# the first member. Built as an archive cache so build_release_state picks it up and re-seals
# the checksums, manifest and provenance over it — a fixture whose archive did not match its
# own signed digests would be refused for an entirely different reason.
make_hostile_cache() { # make_hostile_cache <cache-name> <abs|dotdot>
	local name="$1" kind="$2" a
	local cache="$ARCHIVE_CACHE/$name" stage="$WORK/hostile/$name"
	rm -rf "$cache" "$stage"
	mkdir -p "$cache" "$stage/pack" || return 1
	cp "$BIN" "$stage/pack/olivares" || return 1
	printf 'payload\n' >"$stage/victim" || return 1
	# `-P` is mandatory in both directions: without it GNU tar strips the leading `/` and the
	# leading `../` AT CREATION, and the fixture would quietly stop being the case it claims.
	case "$kind" in
	abs)
		(cd "$stage/pack" && tar -P --use-compress-program='gzip -1' \
			-cf "$cache/olivares_${VERSION}_linux_amd64.tar.gz" olivares "$stage/victim") || return 1
		;;
	dotdot)
		(cd "$stage/pack" && tar -P --use-compress-program='gzip -1' \
			-cf "$cache/olivares_${VERSION}_linux_amd64.tar.gz" olivares ../victim) || return 1
		;;
	*) return 1 ;;
	esac
	for a in "${BASE_ARCHIVES[@]}" "${FIPS_ARCHIVES[@]}"; do
		[ "$a" = "olivares_${VERSION}_linux_amd64.tar.gz" ] && continue
		cp "$ARCHIVE_CACHE/real/$a" "$cache/$a" || return 1
	done
}

# The pair the signing step left in the workspace, which the finalizer compares the remote
# bytes against. Kept in sync by default so a case that wants them to differ has to say so.
stage_attached_pair() {
	mkdir -p "$TREE/ota-dist" || return 1
	cp "$ASSETS/stable-manifest.json" "$TREE/ota-dist/stable-manifest.json" 2>/dev/null || return 1
	cp "$ASSETS/stable-manifest.json.sig" "$TREE/ota-dist/stable-manifest.json.sig" 2>/dev/null || return 1
}

n=0
rc=0
out=""
# FIN_BIN selects which bin directory is on PATH ("bin" = cosign present, "bin-noc" = the
# job's isolated layout). FIN_COSIGN_BIN overrides OLIVARES_COSIGN_BIN; "@unset" leaves the
# variable out of the environment entirely. Both are reset to their defaults by every caller
# that sets them, so a case cannot leak its layout into the next one.
FIN_BIN="bin"
FIN_COSIGN_BIN=""
run_finalizer() { # run_finalizer [VAR=VAL …]
	n=$((n + 1))
	: >"$WORK/gh.log.$n"
	local cosignenv=()
	case "$FIN_COSIGN_BIN" in
	"@unset") ;;
	"") cosignenv=(OLIVARES_COSIGN_BIN="$WORK/bin/cosign") ;;
	*) cosignenv=(OLIVARES_COSIGN_BIN="$FIN_COSIGN_BIN") ;;
	esac
	(
		cd "$TREE" &&
			env -i PATH="$WORK/trusted:$WORK/$FIN_BIN:/usr/bin:/bin" HOME="$WORK" \
				GITHUB_WORKSPACE="$TREE" GITHUB_REPOSITORY="$REPO" \
				GH_TOKEN="stub-token" RUNNER_TEMP="$WORK/runnertmp" \
				OLIVARES_OTA_PUBKEY="$OTA_PUB" \
				"${cosignenv[@]+"${cosignenv[@]}"}" OLIVARES_COSIGN_ALLOW_UNOFFICIAL=1 \
				GH_STATE="$STATE" GH_LOG="$WORK/gh.log.$n" COSIGN_LOG="$WORK/cosign.log.$n" \
				SLSA_LOG="$WORK/slsa.log.$n" \
				"$@" \
				bash scripts/release-finalize-stable.sh "$TAG" "$COMMIT"
	) >"$WORK/out.$n" 2>&1
	rc=$?
	out="$(cat "$WORK/out.$n")"
}

# `grep -c` PRINTS 0 AND EXITS 1 when nothing matches, so `|| echo 0` emitted TWO lines and
# every `[ "$(patch_count)" -eq 0 ]` died with "integer expression expected" — a harness bug
# that reads as a defect in the code under test.
patch_count() {
	local c
	c="$(command grep -c '^PATCH ' "$STATE/patches" 2>/dev/null)" || c=0
	printf '%s' "${c:-0}"
}
# Drain the producer to preserve diagnostic matches under pipefail.
says() { printf '%s' "$out" | command grep -F -- "$1" >/dev/null; }
show() { printf '   last: rc=%s\n' "$rc"; printf '%s\n' "$out" | tail -6 | sed 's/^/   | /'; }

build_release_state || blind "could not build the nominal release fixture"

# ============================================================================================
# A · THE COMPLETE CANDIDATE PUBLISHES. Without this row every refusal below is satisfied by
# a script that refuses unconditionally.
# ============================================================================================
run_finalizer
[ "$rc" -eq 0 ]
check "a complete candidate PUBLISHES and exits zero" "the non-firing direction" $?
[ "$rc" -eq 0 ] || show
[ "$(patch_count)" -eq 1 ]
check "exactly ONE publish PATCH was issued" "no repeated effect" $?
command grep -q "^PATCH repos/${REPO}/releases/${RELEASE_ID}\$" "$STATE/patches"
check "the PATCH names the RETAINED release id" "never a re-resolved tag" $?
command grep -q '^PATCHARG draft=false$' "$STATE/patches" &&
	command grep -q '^PATCHARG make_latest=true$' "$STATE/patches"
check "it sets only draft=false and the reviewed latest policy" "no other field" $?
! command grep -qE '^PATCHARG (tag_name|target_commitish|body|name)=' "$STATE/patches"
check "and it does not touch identity or destination fields" "publication, not repair" $?
says 'PUBLISHED:'
check "the run reports the publication it performed" "evidence, not silence" $?
says '"outcome": "PUBLISHED"'
check "an evidence document records the outcome" "reconstructable decision" $?
# The two OTA directions are separate runs of the real verifier; the shipped one is the
# archive's own binary with no --pubkey.
says 'OTA signature verifies under the configured public anchor'
check "the configured public anchor verified the signature" "direction one" $?
says "the shipped binary's embedded anchor accepts the published bytes"
check "the SHIPPED binary's embedded anchor verified it too" "direction two" $?
says "the signed manifest covers exactly the 4-platform base matrix"
check "the complete platform matrix was required" "not just the named artifacts" $?
# The certificate anchor must name THIS tag, not any SemVer tag.
# ⛔ THE FINALIZER'S OWN CALLS, NOT EVERY COSIGN CALL IN THE RUN. The first version of this
# row grepped the whole log and failed — correctly: scripts/verify-release.sh also invokes
# cosign, and its CONSUMER identity is the any-SemVer default, which is right for a consumer
# verifying a download and would be wrong here. The finalizer's calls are the ones that carry
# `--certificate-github-workflow-repository`, so the row is scoped to those and the battery
# stops conflating two different callers' contracts.
_tagpat="$(printf '@refs/tags/v26\\.9\\.0$')"
_fincalls="$(command grep -F -- '--certificate-github-workflow-repository' "$WORK/cosign.log.$n" 2>/dev/null || true)"
[ -n "$_fincalls" ] && printf '%s\n' "$_fincalls" | command grep -F -- "$_tagpat" >/dev/null
check "the certificate identity pins THIS exact tag" "not any SemVer release" $?
[ -n "$_fincalls" ] && ! printf '%s\n' "$_fincalls" | command grep -F -- '@refs/tags/v[0-9]+' >/dev/null
check "and no finalizer call uses the any-SemVer pattern" "the tightening is real" $?
[ "$(printf '%s\n' "$_fincalls" | command grep -c 'verify-blob')" -eq 2 ]
check "both the checksums and the manifest signature were re-verified" "two subjects, one identity" $?
# EVERY asset was fetched by id, never by a mutable selector.
! command grep -q '^ARG latest$' "$WORK/gh.log.$n"
check "no request selected a candidate by 'latest'" "immutable identity only" $?

# THE LIGHT FIXTURE CANNOT PASS ON ITS OWN, which is what makes it safe to use in sections D,
# E and F. Everything else about this candidate is correct; only the shipped binary is a
# stand-in, and the ceremony refuses at the one step that runs it.
FIXTURE_BINARY="tiny"
build_release_state || blind "fixture"
run_finalizer
[ "$rc" -ne 0 ] && [ "$(patch_count)" -eq 0 ] && says "embedded OTA anchor does not accept"
check "a candidate whose shipped binary is a stand-in REFUSES" "the light fixture is not a pass" $?
FIXTURE_BINARY="real"

# ============================================================================================
# B · THE OTA SIGNATURE ITSELF. These are the rows QA2334 measured in production: a manifest
# that is served while its REQUIRED detached signature is not.
# ============================================================================================
build_release_state || blind "fixture"
rm -f "$ASSETS/stable-manifest.json.sig"
rm -f "$TREE/ota-dist/stable-manifest.json.sig"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && says 'stable-manifest.json.sig'
check "a manifest with NO detached OTA signature refuses" "the exact QA07 shape" $?
[ "$(patch_count)" -eq 0 ]
check "and nothing was published" "refusal precedes every effect" $?

build_release_state || blind "fixture"
: >"$ASSETS/stable-manifest.json.sig"
rebuild_assets_json
run_finalizer
[ "$rc" -ne 0 ] && [ "$(patch_count)" -eq 0 ]
check "an EMPTY signature file refuses" "a name is not a signature" $?

build_release_state || blind "fixture"
cp "$ASSETS/checksums.txt.sig" "$ASSETS/stable-manifest.json.sig"
stage_attached_pair
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'signature'
check "cosign bytes renamed to .sig refuse" "cryptographic, not nominal" $?

build_release_state other || blind "fixture"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] &&
	says 'does not verify under the configured public anchor'
check "a signature from an INDEPENDENT key refuses" "the refusal is cryptographic" $?

# ============================================================================================
# C · IDENTITY. A perfectly valid signature over the wrong thing.
# ============================================================================================
# A manifest for ANOTHER version, correctly generated and correctly signed. It needs its own
# archive names, so it is produced in a side directory and swapped in: the point is that
# every cryptographic check passes and the IDENTITY check is what refuses.
build_release_state || blind "fixture"
OTHERV="$WORK/otherversion"
rm -rf "$OTHERV"; mkdir -p "$OTHERV"
for _a in "${BASE_ARCHIVES[@]}"; do
	cp "$ASSETS/$_a" "$OTHERV/${_a/${VERSION}/26.8.0}" || blind "fixture"
done
(cd "$OTHERV" && sha256sum ./*.tar.gz | sed 's#\./##' >checksums.txt) || blind "fixture"
"$BIN" release manifest --dir "$OTHERV" --channel stable --version 26.8.0 \
	--expires-in 2160h --out "$OTHERV/stable-manifest.json" >/dev/null 2>&1 || blind "fixture"
"$BIN" release sign-manifest --manifest "$OTHERV/stable-manifest.json" \
	--checksums "$OTHERV/checksums.txt" --sign-key "@$KEYS/ota.seed.b64" \
	--out "$OTHERV/stable-manifest.json.sig" >/dev/null 2>&1 || blind "fixture"
cp "$OTHERV/stable-manifest.json" "$ASSETS/stable-manifest.json"
cp "$OTHERV/stable-manifest.json.sig" "$ASSETS/stable-manifest.json.sig"
stage_attached_pair
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ]
check "a correctly signed manifest for ANOTHER version refuses" "identity, not signature" $?

build_release_state ota "$VERSION" security || blind "fixture"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ]
check "a correctly signed manifest for another CHANNEL refuses" "expect-channel is enforced" $?

build_release_state || blind "fixture"
# Modified manifest bytes: the signature no longer covers them, and the remote no longer
# equals the pair this ceremony attached. Both must refuse; neither may publish.
printf ' ' >>"$ASSETS/stable-manifest.json"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not the bytes this ceremony attached'
check "manifest bytes rewritten after the attachment refuse" "byte identity, not filename" $?

# ============================================================================================
# D · SOURCE AND RUN PROVENANCE. Authenticated bytes from a run nobody named.
#
# Sections D, E and F refuse before the OTA verification, so they run on the light fixture;
# the row above proves that fixture cannot reach a publication on its own, and every row here
# asserts a specific message, so a refusal that moved later would redden rather than pass.
# ============================================================================================
FIXTURE_BINARY="tiny"
build_release_state || blind "fixture"
(cd "$TREE" && git commit -q --allow-empty -m moved && git tag -f "$TAG" >/dev/null 2>&1)
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not the commit this finalization names'
check "a MOVED tag refuses" "the label is not the object" $?
(cd "$TREE" && git reset -q --hard "$COMMIT" && git tag -f "$TAG" "$COMMIT" >/dev/null 2>&1)
restore_sut || blind "cannot reinstall the redirected copy after the reset"
[ "$(git -C "$TREE" rev-parse HEAD)" = "$COMMIT" ]
check "the fixture tag and checkout are sound again" "no contamination of later rows" $?

build_release_state || blind "fixture"
printf '%040d\n' 1 >"$ASSETS/release-commit.txt"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ]
check "commit evidence that disagrees with the signed checksums refuses" "authenticated first" $?

build_release_state || blind "fixture"
jq '.head_sha = "0000000000000000000000000000000000000000"' "$STATE/run.json" >"$STATE/run.next" &&
	mv "$STATE/run.next" "$STATE/run.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not '"$COMMIT"
check "a run that built ANOTHER commit refuses" "not an unrelated successful run" $?

build_release_state || blind "fixture"
jq '.conclusion = "failure"' "$STATE/run.json" >"$STATE/run.next" && mv "$STATE/run.next" "$STATE/run.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'concluded'
check "a FAILED phase-1 run refuses" "a completed build is a precondition" $?

build_release_state || blind "fixture"
jq '.status = "in_progress"' "$STATE/run.json" >"$STATE/run.next" && mv "$STATE/run.next" "$STATE/run.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not completed'
check "an INCOMPLETE phase-1 run refuses" "unfinished is not success" $?

build_release_state || blind "fixture"
jq '.run_attempt = 7' "$STATE/run.json" >"$STATE/run.next" && mv "$STATE/run.next" "$STATE/run.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'the context says'
check "a run reporting another ATTEMPT refuses" "attempt is part of the identity" $?

build_release_state || blind "fixture"
jq '.repository.id = 111' "$STATE/run.json" >"$STATE/run.next" && mv "$STATE/run.next" "$STATE/run.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ]
check "a run in another REPOSITORY refuses" "the context is cross-checked" $?

build_release_state || blind "fixture"
jq '.path = ".github/workflows/other.yml"' "$STATE/run.json" >"$STATE/run.next" && mv "$STATE/run.next" "$STATE/run.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ]
check "a run of another WORKFLOW refuses" "the path is cross-checked" $?

build_release_state || blind "fixture"
rm -f "$ASSETS/release-build-context.json"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'release-build-context.json'
check "a candidate with no build context refuses" "which run made it is required" $?

build_release_state || blind "fixture"
jq -c '. + {extra:"field"}' "$ASSETS/release-build-context.json" >"$ASSETS/ctx.next" &&
	mv "$ASSETS/ctx.next" "$ASSETS/release-build-context.json"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ]
check "a build context with an extra field refuses" "exact fields, no more" $?

# ============================================================================================
# E · COMPLETENESS. The signed manifest can be honest about everything it names and silent
# about three quarters of the release.
# ============================================================================================
build_release_state || blind "fixture"
rm -f "$ASSETS/olivares_${VERSION}_darwin_arm64.tar.gz"
rm -f "$ASSETS/olivares_${VERSION}_darwin_arm64.tar.gz."*
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says "olivares_${VERSION}_darwin_arm64.tar.gz"
check "a MISSING PLATFORM refuses and names it" "the matrix is derived, not copied" $?

# The package must be missing from the SIGNED SET, not merely from disk: an asset that is
# checksummed and absent is caught earlier, by a different rule, and this row is about the
# format x architecture coverage that no per-file check can see.
build_release_state || blind "fixture"
command grep -v 'aarch64\.rpm$' "$ASSETS/checksums.txt" >"$STATE/cs.next" && mv "$STATE/cs.next" "$ASSETS/checksums.txt"
rm -f "$ASSETS/olivares-${VERSION}-1.aarch64.rpm"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'no rpm package for arm64'
check "a missing native PACKAGE refuses by coverage" "format x architecture" $?

build_release_state || blind "fixture"
rm -f "$ASSETS/olivares_${VERSION}_linux_arm64.tar.gz.vex.sigstore.json"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'vex.sigstore.json'
check "a missing required ATTESTATION refuses" "sidecars are members too" $?

build_release_state || blind "fixture"
rm -f "$ASSETS/multiple.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'no SLSA provenance'
check "a candidate with no PROVENANCE refuses" "RUN_SLSA is a profile constant" $?

build_release_state || blind "fixture"
cp "$ASSETS/multiple.intoto.jsonl" "$ASSETS/second.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'provenance documents'
check "TWO provenance documents refuse" "ambiguity, never first-match" $?

# ⛔ THIS ONE NEEDS THE REAL BINARY, and finding that out is the light fixture's self-guard
# working: the subject binding is checked AFTER the OTA verification, so on the stand-in the
# ceremony refused earlier and this row went red instead of passing for the wrong reason.
FIXTURE_BINARY="real"
build_release_state || blind "fixture"
# A provenance whose subjects are someone else's bytes: present, unambiguous, and wrong.
jq -nc --arg p "$(printf '{"subject":[{"name":"other","digest":{"sha256":"%064d"}}]}' 0 | base64 -w0)" \
	'{payloadType:"application/vnd.in-toto+json",payload:$p,signatures:[{sig:"stub"}]}' \
	>"$ASSETS/multiple.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'is not a subject of'
check "provenance that does not cover the archives refuses" "subject binding, measured" $?
FIXTURE_BINARY="tiny"

build_release_state || blind "fixture"
printf 'tampered\n' >>"$ASSETS/olivares_${VERSION}_linux_arm64.tar.gz"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ]
check "an archive whose bytes changed refuses" "rehashed, not trusted" $?

build_release_state || blind "fixture"
printf 'unsigned extra\n' >"$ASSETS/olivares_${VERSION}_windows_amd64.tar.gz"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] &&
	says 'assets this ceremony does not admit' && says "olivares_${VERSION}_windows_amd64.tar.gz"
check "an EXTRA ordinary artifact on the release refuses" "unsigned bytes where users download" $?

build_release_state || blind "fixture"
jq '. + [(.[0] | .id = 999999)]' "$STATE/assets.json" >"$STATE/assets.next" && mv "$STATE/assets.next" "$STATE/assets.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'duplicate asset names'
check "a DUPLICATE asset name refuses" "two answers to one question" $?

# The same ambiguity one document earlier: two rows for one name inside the SIGNED set. A
# reader that takes the first would be choosing a digest by accident.
build_release_state || blind "fixture"
command grep -m1 'release-commit\.txt$' "$ASSETS/checksums.txt" >>"$ASSETS/cs.dup" &&
	cat "$ASSETS/checksums.txt" "$ASSETS/cs.dup" >"$STATE/cs.next" && rm -f "$ASSETS/cs.dup" &&
	mv "$STATE/cs.next" "$ASSETS/checksums.txt"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'twice'
check "a duplicate ROW in the signed checksums refuses" "one name, one digest" $?

# A page of the inventory that cannot be read is NOT an empty page. A partial enumeration
# would make "the release carries no extra artifacts" true by not looking.
build_release_state || blind "fixture"
run_finalizer GH_ASSETS_RC=1
[ "$rc" -eq 2 ] && [ "$(patch_count)" -eq 0 ] && says 'could not enumerate'
check "an unreadable asset page is NO HE PODIDO MIRAR" "a short list is not a complete one" $?

build_release_state || blind "fixture"
jq '.[0].state = "starter"' "$STATE/assets.json" >"$STATE/assets.next" && mv "$STATE/assets.next" "$STATE/assets.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not in the uploaded state'
check "an asset still uploading refuses" "a pending upload is not a byte" $?

build_release_state || blind "fixture"
jq '.[0].name = "../escape.tar.gz"' "$STATE/assets.json" >"$STATE/assets.next" && mv "$STATE/assets.next" "$STATE/assets.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'unsafe asset basename'
check "an unsafe asset basename refuses" "no path escape, ever" $?

# ============================================================================================
# F · THE SECURITY CHANNEL REFUSAL IS PRESERVED.
# ============================================================================================
build_release_state || blind "fixture"
mkdir -p "$TREE/release/advisories"
printf 'CVE-2026-0001\n' >"$TREE/release/advisories/${VERSION}.txt"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'DECLARED a security release'
check "a tag that DECLARED advisories refuses" "stable channel only, still" $?
rm -f "$TREE/release/advisories/${VERSION}.txt"

build_release_state || blind "fixture"
printf '{}\n' >"$ASSETS/security-manifest.json"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'security-manifest.json'
check "a remote security manifest refuses" "#644 is not implemented here" $?

# ============================================================================================
# F-bis · THE PROVENANCE SIGNATURE CANNOT DEGRADE TO A WARNING (root return 01).
#
# An earlier revision let strict publication accept a missing slsa-verifier, print a LIMIT
# line and publish. A present envelope with matching subject digests is not an authenticated
# provenance, so every way the signature can fail to be established must stop the ceremony
# before it asks GitHub for anything. These rows reach the strict verification step, so they
# run on the real product binary.
# ============================================================================================
FIXTURE_BINARY="real"

# The verifier is not installed. The stub is moved aside rather than the directory removed:
# the rest of $WORK/bin holds jq, tar and go, and this row is about one absent tool.
#
# ⛔ AND THE VERDICT IS EXIT 2, NOT EXIT 1 (root return 02, F1). Root return 01 required this
# to stop the ceremony before any PATCH, and it does; what it must NOT do is tell the operator
# the candidate is wrong. A tool that is not installed is the machine failing to look, which
# is the verdict this script's own header reserves exit 2 for. The finalizer now resolves the
# verifier itself, so the absence is named where it happens instead of arriving as a child's
# non-zero exit.
build_release_state || blind "fixture"
mv "$WORK/bin/slsa-verifier" "$WORK/slsa-verifier.hidden" || blind "cannot hide the verifier stub"
mv "$WORK/bin-noc/slsa-verifier" "$WORK/slsa-verifier-noc.hidden" || blind "cannot hide the isolated link"
run_finalizer
mv "$WORK/slsa-verifier.hidden" "$WORK/bin/slsa-verifier" || blind "cannot restore the verifier stub"
mv "$WORK/slsa-verifier-noc.hidden" "$WORK/bin-noc/slsa-verifier" || blind "cannot restore the isolated link"
[ "$rc" -eq 2 ] && [ "$(patch_count)" -eq 0 ] && says 'slsa-verifier is not installed'
check "an absent provenance verifier is NO HE PODIDO MIRAR, NO publish PATCH" "no signature, no publication" $?
says 'NO HE PODIDO MIRAR' && ! says 'REFUSED —'
check "and it is not reported as a wrong candidate" "exit 1 and exit 2 do not collapse" $?
! says 'strict publication verification passed'
check "and no step reported strict verification as passed" "the waiver path is gone" $?

# The verifier is installed and REJECTS. Distinct from absent: the tool ran and said no.
build_release_state || blind "fixture"
run_finalizer SLSA_RC=1
[ "$rc" -ne 0 ] && [ "$(patch_count)" -eq 0 ] && says 'slsa-verifier rejected'
check "a rejecting provenance verifier refuses, with NO publish PATCH" "the tool ran and said no" $?

# And the non-firing direction for this pair: with the verifier present and accepting, the
# same candidate still publishes. Without this row the two above are satisfied by a
# finalizer that stopped publishing altogether.
build_release_state || blind "fixture"
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "with the verifier present and accepting, the candidate publishes" "not always-red" $?
# The verifier's own count line stays inside $WORK/verify-release.out, which the finalizer
# only surfaces on refusal; that the count is stated is asserted in
# scripts/test-verify-release-summary.sh, against the verifier directly.

# ============================================================================================
# G · STATE AND UNCERTAINTY. The rows where the honest answer is not a verdict. These reach
# publication, so they are back on the real product binary.
# ============================================================================================
FIXTURE_BINARY="real"
build_release_state || blind "fixture"
jq '.[0].size = 1' "$STATE/assets.json" >"$STATE/assets-swapped.json"
run_finalizer GH_ASSETS_SWAP_AFTER=1
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'changed while it was being verified'
check "an inventory that moved during verification refuses" "detected, not locked out" $?

build_release_state || blind "fixture"
jq '.immutable = true' "$STATE/release.json" >"$STATE/release.next" && mv "$STATE/release.next" "$STATE/release.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'immutable'
check "an immutable release refuses" "publication cannot apply" $?

build_release_state || blind "fixture"
jq 'del(.immutable)' "$STATE/release.json" >"$STATE/release.next" && mv "$STATE/release.next" "$STATE/release.json"
jq '.prerelease = true' "$STATE/release.json" >"$STATE/release.next" && mv "$STATE/release.next" "$STATE/release.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'prerelease'
check "a prerelease refuses" "ordinary stable only" $?

build_release_state || blind "fixture"
jq '.draft = false' "$STATE/release.json" >"$STATE/release.next" && mv "$STATE/release.next" "$STATE/release.json"
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 0 ] && says 'PUBLICATION_ALREADY_COMPLETE'
check "an ALREADY PUBLISHED candidate is verified, not re-published" "no repeated effect" $?
says 'reconciliation mode'
check "and it says which mode it is in" "observed state, reported" $?

build_release_state || blind "fixture"
run_finalizer GH_PATCH_RC=1
[ "$rc" -eq 3 ] && says 'PUBLICATION_UNKNOWN'
check "a PATCH transport failure is PUBLICATION_UNKNOWN" "not a refusal" $?
[ "$(patch_count)" -eq 1 ]
check "and it is NOT retried" "one uncertain effect, never two" $?
! says 'Rollback' && ! says 'delete-asset'
check "and nothing is compensated" "no blind undo of an unknown effect" $?

build_release_state || blind "fixture"
run_finalizer GH_READBACK_RC=1
[ "$rc" -eq 3 ] && says 'read-back failed'
check "an unreadable read-back is PUBLICATION_UNKNOWN" "success plus silence is unknown" $?

build_release_state || blind "fixture"
run_finalizer CURL_FAIL_FOR="stable-manifest.json.sig"
[ "$rc" -eq 4 ] && says 'POSTCONDITION FAILED'
check "a failed public delivery is a POSTCONDITION failure" "published, and then a gap" $?
[ "$(patch_count)" -eq 1 ] && says 'The release IS published'
check "and it says the release IS published" "never 'it did not happen'" $?

build_release_state || blind "fixture"
run_finalizer GH_RELEASE_RC=1
[ "$rc" -eq 2 ] && [ "$(patch_count)" -eq 0 ]
check "an unreadable release is NO HE PODIDO MIRAR, not a refusal" "three verdicts, not two" $?

# ============================================================================================
# G-bis · THE CANDIDATE IS READ FROM THE LIST, AND THE BYTES ARE RETRIED. Both come from
# finalizing v26.9.0 on 2026-09-17: `GET /releases/tags/<tag>` answers 404 for a DRAFT, which
# is the only thing this ceremony ever publishes, and `gh api` has no retry of its own, so a
# dropped transfer on a ~120 MB archive ended the whole run. The stub's tag route now answers
# 404 for a draft exactly as GitHub does, so the nominal publication row above is itself the
# proof that the finalizer no longer asks that question.
# ============================================================================================
build_release_state || blind "fixture"
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "a DRAFT candidate is published, read from the release list" "the tag route 404s here" $?
! command grep -q "^ARG repos/${REPO}/releases/tags/" "$WORK/gh.log.$n"
check "and no call resolved the candidate by tag" "the route that cannot see a draft" $?
command grep -q "^ARG repos/${REPO}/releases?per_page=100$" "$WORK/gh.log.$n"
check "the list is read with an explicit page size" "one page, not a default" $?

build_release_state || blind "fixture"
jq -s '[.[0], (.[0] | .id = 999999 | .created_at = "2026-09-17T09:00:00Z")]' "$STATE/release.json" >"$STATE/two-drafts.json"
run_finalizer GH_RELEASES_LIST_FILE="$STATE/two-drafts.json"
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'will not choose one'
check "TWO releases for one tag refuse, with no publication" "the 2026-09-17 shape" $?
says 'id=999999' && says "id=${RELEASE_ID}"
check "and both are named with their id, draft state and asset count" "a human deletes the wrong one" $?

build_release_state || blind "fixture"
jq -s '[.[0] | .tag_name = "v0.0.0-somethingelse"]' "$STATE/release.json" >"$STATE/no-match.json"
run_finalizer GH_RELEASES_LIST_FILE="$STATE/no-match.json"
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says "no release carries the tag ${TAG}"
check "no release for the tag refuses" "absence is not a draft" $?

build_release_state || blind "fixture"
printf '{"message":"Bad credentials"}\n' >"$STATE/not-a-list.json"
run_finalizer GH_RELEASES_LIST_FILE="$STATE/not-a-list.json"
[ "$rc" -eq 2 ] && [ "$(patch_count)" -eq 0 ] && says 'not a JSON array'
check "a list answer that is not an array is NO HE PODIDO MIRAR" "not 'there is no release'" $?

build_release_state || blind "fixture"
run_finalizer GH_FETCH_FLAKY_NAME=checksums.txt GH_FETCH_DROP_FIRST=1
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ] && says 'retrying checksums.txt'
check "a dropped transfer is retried and the candidate publishes" "gh api has no retry" $?
[ "$(cat "$STATE/fetch-tries-checksums.txt" 2>/dev/null)" = "2" ]
check "and it took exactly two attempts" "retried, not re-requested forever" $?

build_release_state || blind "fixture"
run_finalizer GH_FETCH_FLAKY_NAME=release-commit.txt GH_FETCH_SHORT_FIRST=1
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ] && says 'retrying release-commit.txt'
check "a SHORT read is retried, not reported as a size disagreement" "truncation is transport" $?

build_release_state || blind "fixture"
run_finalizer GH_FETCH_FLAKY_NAME=checksums.txt GH_FETCH_DROP_FIRST=9
[ "$rc" -eq 2 ] && [ "$(patch_count)" -eq 0 ] && says 'could not download checksums.txt'
check "when every attempt fails the ceremony publishes nothing" "retry is not tolerance" $?
[ "$(cat "$STATE/fetch-tries-checksums.txt" 2>/dev/null)" = "3" ]
check "and it stopped at the third attempt" "a bounded loop" $?

# ============================================================================================
# H · DESTINATION. The supported operation cannot be pointed somewhere else.
# ============================================================================================
build_release_state || blind "fixture"
run_finalizer GITHUB_REPOSITORY="someone/else"
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'neither the production destination'
check "an unrecognised destination refuses" "no third profile" $?

run_finalizer GITHUB_REPOSITORY="someone/else" OLIVARES_RELEASE_PROFILE=preprod
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'OLIVARES_PREPROD_MAKE_LATEST'
check "preprod without a declared pointer policy refuses" "it must state its own" $?

run_finalizer GITHUB_REPOSITORY="acme/olivares" OLIVARES_RELEASE_PROFILE=preprod OLIVARES_PREPROD_MAKE_LATEST=false
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'production-shaped destination'
check "a preprod run naming a production surface refuses" "the guard is not the variable" $?

build_release_state || blind "fixture"
run_finalizer OLIVARES_OTA_PUBKEY=""
[ "$rc" -eq 2 ] && [ "$(patch_count)" -eq 0 ]
check "no configured anchor is NO HE PODIDO MIRAR" "never a single-check fallback" $?

# A TOOL SUPPLIED BY THE TREE UNDER VERIFICATION CANNOT VERIFY IT. `jq` decides what the
# inventory says, so a `jq` that the checkout itself provides is the checkout answering
# questions about itself. The stubs live outside GITHUB_WORKSPACE for exactly this reason;
# this case puts one inside and requires a refusal.
mkdir -p "$TREE/.fakebin"
printf '#!/bin/sh\nexit 0\n' >"$TREE/.fakebin/jq" && chmod +x "$TREE/.fakebin/jq"
n=$((n + 1))
: >"$WORK/gh.log.$n"
(
	cd "$TREE" &&
		env -i PATH="$TREE/.fakebin:$WORK/trusted:$WORK/bin:/usr/bin:/bin" HOME="$WORK" \
			GITHUB_WORKSPACE="$TREE" GITHUB_REPOSITORY="$REPO" GH_TOKEN="stub-token" \
			RUNNER_TEMP="$WORK/runnertmp" OLIVARES_OTA_PUBKEY="$OTA_PUB" \
			OLIVARES_COSIGN_BIN="$WORK/bin/cosign" OLIVARES_COSIGN_ALLOW_UNOFFICIAL=1 \
			GH_STATE="$STATE" GH_LOG="$WORK/gh.log.$n" \
			bash scripts/release-finalize-stable.sh "$TAG" "$COMMIT"
) >"$WORK/out.$n" 2>&1
rc=$?
out="$(cat "$WORK/out.$n")"
rm -rf "$TREE/.fakebin"
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'resolves inside the checkout'
check "a tool resolving INSIDE the checkout refuses" "the tree cannot vouch for itself" $?

# ============================================================================================
# J · THE JOB'S ACTUAL TOOL LAYOUT (root return 02, F1). The phase-2 job runs
# `assert-cosign-binary.sh --isolate` before the ceremony: it authenticates the binary against
# the upstream published digests, MOVES it to a private directory and refuses to report
# success while the bare name still resolves. Every row above ran with `cosign` ON PATH, which
# is the one property the real job guarantees is false — so a complete, correct candidate was
# refused there and nothing could ever be published. These rows run the same fixture in the
# layout the job actually has.
# ============================================================================================
FIXTURE_BINARY="real"
! (PATH="$WORK/trusted:$WORK/bin-noc:/usr/bin:/bin" command -v cosign >/dev/null 2>&1)
check "the isolated PATH really has no cosign on it" "the layout --isolate leaves behind" $?

build_release_state || blind "fixture"
FIN_BIN="bin-noc"
run_finalizer
FIN_BIN="bin"
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "a COMPLETE candidate PUBLISHES with cosign OFF PATH" "the job's real layout" $?
command grep -q -- '--certificate-identity-regexp' "$WORK/cosign.log.$n"
check "and the isolated binary is what ran" "through the verified wrapper" $?

# TOOL ABSENT IS "COULD NOT LOOK", NOT "THE CANDIDATE IS WRONG". `require_path_tool` already
# existed for exactly this class — jq, tar, curl, go, base64, cmp — and these two were missing
# from it while being the two whose absence a publisher notices last.
build_release_state || blind "fixture"
FIN_BIN="bin-noc"
FIN_COSIGN_BIN="@unset"
run_finalizer
FIN_BIN="bin"
FIN_COSIGN_BIN=""
[ "$rc" -eq 2 ] && [ "$(patch_count)" -eq 0 ] && says 'OLIVARES_COSIGN_BIN is not set'
check "no authenticated cosign at all is NO HE PODIDO MIRAR" "exit 2, never exit 1" $?
! says 'REFUSED —'
check "and it never calls the candidate wrong" "three verdicts, not two" $?

build_release_state || blind "fixture"
FIN_COSIGN_BIN="$WORK/no-such-cosign"
run_finalizer
FIN_COSIGN_BIN=""
[ "$rc" -eq 2 ] && [ "$(patch_count)" -eq 0 ] && says 'not an executable file'
check "a named cosign that is not there is NO HE PODIDO MIRAR" "could not look" $?

# A RELATIVE NAME IS A REFUSAL, and the difference is not pedantry: the environment IS
# readable, and what it says is wrong. "cosign" would be resolved by whatever PATH holds.
build_release_state || blind "fixture"
FIN_COSIGN_BIN="cosign"
run_finalizer
FIN_COSIGN_BIN=""
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not an absolute path'
check "a relative OLIVARES_COSIGN_BIN refuses" "a name is not an authenticated binary" $?

# ============================================================================================
# K · THE DELEGATED IDENTITY (root return 02, F4). The finalizer derives a certificate
# identity pinned to this repository, this workflow and THIS tag, and then handed the strict
# verification to a script that built its own from a production literal accepting ANY SemVer
# tag. The adapter now performs the one negative control real cosign performs — an identity
# that is not the expected one does not verify — so these rows measure the anchor rather than
# an exit code.
# ============================================================================================
PROD_ID='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v26\.9\.0$'
build_release_state || blind "fixture"
run_finalizer COSIGN_EXPECT_IDENTITY="$PROD_ID"
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "the candidate publishes when ONLY the derived identity is accepted" "positive control" $?
_idtotal="$(command grep -c -- '--certificate-identity-regexp' "$WORK/cosign.log.$n")" || _idtotal=0
_idderived="$(command grep -cF -- "--certificate-identity-regexp $PROD_ID" "$WORK/cosign.log.$n")" || _idderived=0
[ "${_idtotal:-0}" -ge 3 ] && [ "$_idtotal" -eq "$_idderived" ]
check "EVERY recorded cosign call carries that identity (${_idderived:-0}/${_idtotal:-0})" "argv, not exit code" $?
command grep -q '^COSIGN verify-blob-attestation ' "$WORK/cosign.log.$n"
check "and the DELEGATED attestation calls are among them" "not only the finalizer's own two" $?
command grep -qF -- "--source-uri github.com/${REPO}" "$WORK/slsa.log.$n" &&
	command grep -qF -- "--source-tag ${TAG}" "$WORK/slsa.log.$n"
check "slsa-verifier receives the derived source repository and tag" "the same identity" $?

# CROSS-TAG. A checksums.txt legitimately signed for another tag of this repository satisfies
# the any-SemVer anchor and must not satisfy this publication.
build_release_state || blind "fixture"
run_finalizer COSIGN_EXPECT_IDENTITY='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v26\.8\.0$'
[ "$rc" -ne 0 ] && [ "$(patch_count)" -eq 0 ]
check "a cosign that accepts only ANOTHER tag refuses" "cross-tag negative control" $?

# THE OLD DEFAULT ITSELF. If any call still fell back to it, this row would publish.
build_release_state || blind "fixture"
run_finalizer COSIGN_EXPECT_IDENTITY='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
[ "$rc" -ne 0 ] && [ "$(patch_count)" -eq 0 ]
check "a cosign that accepts only the any-SemVer default refuses" "the fallback is gone" $?

# THE OTHER SUPPORTED PROFILE. Preprod certificates are issued to the repository that declares that profile's own
# workflow identity, so a production anchor rejects every attestation there and the isolated
# trial the ratification requires could not have run at all. The destination name here is a
# neutral stand-in: this file is exported, and the real preprod surface is not written in it.
PREPROD_REPO="acme/product-preprod"
_saved_repo="$REPO"
REPO="$PREPROD_REPO"
build_release_state || blind "preprod fixture"
run_finalizer GITHUB_REPOSITORY="$PREPROD_REPO" OLIVARES_RELEASE_PROFILE=preprod \
	OLIVARES_PREPROD_MAKE_LATEST=false \
	COSIGN_EXPECT_IDENTITY='^https://github\.com/acme/product-preprod/\.github/workflows/release\.yml@refs/tags/v26\.9\.0$'
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "the PREPROD profile completes under ITS OWN derived identity" "both supported profiles" $?
command grep -qF -- "--source-uri github.com/${PREPROD_REPO}" "$WORK/slsa.log.$n"
check "and slsa-verifier is given the preprod source repository" "not a production literal" $?
! command grep -qF -- 'olivaresai/olivares' "$WORK/cosign.log.$n"
check "and no call names the production repository" "profile negative control" $?
REPO="$_saved_repo"

# ============================================================================================
# L · COMPLETE INVENTORY ADMISSION (root return 02, F5). "Extra ordinary artifact" detection
# asked whether a name ended in one of five archive or package extensions. Everything else on
# the release was published unexamined — and the recipe itself ships a `.sh` installer, so an
# unsigned `.sh` is an ordinary name rather than an exotic one.
# ============================================================================================
add_stray_asset() { # add_stray_asset <name>
	printf 'unsigned bytes\n' >"$ASSETS/$1" || return 1
	rebuild_assets_json
}
for _stray in "olivares-install.sh" "checksums.txt.bak" "release-notes.json" "olivares-extra.tar.gz"; do
	build_release_state || blind "fixture"
	add_stray_asset "$_stray" || blind "cannot add the stray asset"
	run_finalizer
	[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says "$_stray"
	check "an extra unsigned ${_stray} refuses and names it" "no extension blind spot" $?
done

# A SECOND PROVENANCE DOCUMENT is still an ambiguous selection, and the check that says so now
# runs BEFORE the admission that admits exactly one of them.
build_release_state || blind "fixture"
printf '{}\n' >"$ASSETS/second.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'provenance documents'
check "two provenance documents refuse" "never a first match" $?

build_release_state || blind "fixture"
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "and the admitted recipe still publishes untouched" "the rule is not always-red" $?

# ============================================================================================
# M · RECONCILIATION OBSERVES THE PUBLISHED STATE (root return 02, F6). Reconciliation did
# everything the publication path does up to the evidence record and then exited zero, so the
# read-back, the public tag-scoped delivery and the latest pointer were all below that exit —
# and a published release whose required `.sig` answers 404 while every API-side check passes
# is exactly the state QA2334 measured and the mode an operator is told to inspect it with.
# ============================================================================================
publish_fixture() {
	build_release_state || return 1
	jq '.draft = false' "$STATE/release.json" >"$STATE/release.next" || return 1
	mv "$STATE/release.next" "$STATE/release.json"
}
publish_fixture || blind "fixture"
run_finalizer CURL_FAIL_FOR="stable-manifest.json.sig"
[ "$rc" -eq 4 ] && [ "$(patch_count)" -eq 0 ]
check "a published .sig that does not deliver is POSTCONDITION FAILED" "the QA2334 shape" $?
! says 'PUBLICATION_ALREADY_COMPLETE'
check "and it is NOT reported as already complete" "the verdict follows the observation" $?
says 'The release IS published'
check "and it still says the release IS published" "a failed postcondition, not a denial" $?

publish_fixture || blind "fixture"
jq '.id = 999999' "$STATE/latest.json" >"$STATE/latest.next" && mv "$STATE/latest.next" "$STATE/latest.json"
run_finalizer
[ "$rc" -eq 4 ] && [ "$(patch_count)" -eq 0 ] && says 'latest points at release id'
check "a published release the latest pointer does not name FAILS" "the applicable pointer" $?

publish_fixture || blind "fixture"
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 0 ] && says 'PUBLICATION_ALREADY_COMPLETE'
check "a healthy published release still reconciles to zero" "not always-red" $?
says 'public tag-scoped delivery serves the verified manifest'
check "and it exercised the public delivery to say so" "observed, not asserted" $?

# NO ATTACHMENT OF ITS OWN. A later operator inspecting a published release attached nothing,
# and the evidence records that rather than quietly implying the comparison happened.
publish_fixture || blind "fixture"
rm -f "$TREE/ota-dist/stable-manifest.json" "$TREE/ota-dist/stable-manifest.json.sig"
run_finalizer
stage_attached_pair
[ "$rc" -eq 0 ] && says '"attached_pair_compared": false'
check "reconciliation records attached_pair_compared=false" "a declared limit, not a claim" $?
says 'declared limit'
check "and says so in the run output" "honest about what it did not do" $?

# ============================================================================================
# N · EVERY ARCHIVE MEMBER (root return 02, F7). The absolute-path guard tested the
# newline-joined listing with `/*`, which `case` anchors at the start of the whole string — so
# it fired only when the FIRST member was absolute. The block's whole purpose is the INT-22
# symlink contract, and a stated control that does not do what it says is worse than none.
# ============================================================================================
make_hostile_cache abs-member abs || blind "cannot build the absolute-member archive"
tar -tzf "$ARCHIVE_CACHE/abs-member/olivares_${VERSION}_linux_amd64.tar.gz" 2>/dev/null |
	sed -n '2p' | command grep '^/' >/dev/null
check "the fixture's SECOND archive member really is an absolute path" "the case is the case" $?
# AND THE OLD PREDICATE WOULD HAVE MISSED IT, run here rather than asserted in prose.
_members="$(tar -tzf "$ARCHIVE_CACHE/abs-member/olivares_${VERSION}_linux_amd64.tar.gz" 2>/dev/null)"
case "$_members" in /*) false ;; *) true ;; esac
check "the whole-string predicate does NOT see it" "why the loop had to replace it" $?

FIXTURE_BINARY="abs-member"
build_release_state || blind "fixture"
run_finalizer
FIXTURE_BINARY="real"
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'is an absolute path'
check "an absolute path in a LATER member refuses" "every member, not the first" $?
! says 'chmod' && ! says "the extracted olivares"
check "and nothing was extracted" "the refusal is before the extraction" $?

make_hostile_cache dotdot-member dotdot || blind "cannot build the traversal archive"
FIXTURE_BINARY="dotdot-member"
build_release_state || blind "fixture"
run_finalizer
FIXTURE_BINARY="real"
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says "contains '..'"
check "a traversal member in a LATER position refuses" "the existing constraint, per member" $?

build_release_state || blind "fixture"
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "an ordinary archive still extracts and publishes" "the loop is not always-red" $?

# ============================================================================================
# O · THE CONSUMER REFUSES A NON-PUSH CONTEXT (root return 02, F2). The producer now records
# the true event instead of refusing to build at all, so the release rehearsal — the only
# end-to-end exercise of the release mechanics this project has — can run. The refusal that
# matters did not move: it is here, and it is made twice, because the recorded field and the
# Actions API are two independent readings and a candidate must satisfy both.
# ============================================================================================
CTX_EVENT="workflow_dispatch"
build_release_state || blind "fixture"
CTX_EVENT="push"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says "records event 'workflow_dispatch', not push"
check "a workflow_dispatch build context refuses" "a rehearsal set is not a candidate" $?

# THE SECOND READING, ON ITS OWN. A context that SAYS push over a run the API reports as
# something else is refused too, so neither source is trusted alone.
build_release_state || blind "fixture"
jq '.event = "workflow_dispatch"' "$STATE/run.json" >"$STATE/run.next" &&
	mv "$STATE/run.next" "$STATE/run.json"
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says "was triggered by 'workflow_dispatch'"
check "a run the API reports as non-push refuses" "two independent readings" $?

build_release_state || blind "fixture"
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "and a genuine tag-push candidate still publishes" "the event check is not always-red" $?

# ============================================================================================
# P · THE PROVENANCE CARRIER (root return 03). Step 7 required a top-level DSSE payload and
# refused the Sigstore bundle our own pinned generator_generic_slsa3.yml@v2.1.0 emits, so a
# real release died before strict verification ever ran. Both contractually valid carriers are
# accepted; a document presenting both is refused rather than resolved by precedence.
#
# The SHAPES here are real; the SIGNATURES are not. Whether a real verifier accepts these
# documents is measured separately against the upstream corpus, outside this battery.
# ============================================================================================
FIXTURE_BINARY="real"
PROVENANCE_SHAPE="bundle"
build_release_state || blind "fixture"
jq -e -s '.[0] | has("dsseEnvelope") and (has("payload") | not)' "$ASSETS/multiple.intoto.jsonl" >/dev/null
check "the nominal fixture provenance is a Sigstore bundle" "the carrier we actually produce" $?
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "a bundle-carried provenance PUBLISHES" "the format that used to refuse" $?
says 'multiple.intoto.jsonl (bundle) names every released archive'
check "and the run names the carrier it read" "observed, not assumed" $?

PROVENANCE_SHAPE="dsse"
build_release_state || blind "fixture"
run_finalizer
PROVENANCE_SHAPE="bundle"
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "a bare DSSE envelope still PUBLISHES" "no regression on the old carrier" $?
says 'multiple.intoto.jsonl (dsse) names every released archive'
check "and that carrier is named too" "the two are distinguished" $?

# AMBIGUITY IS REFUSED, NOT RESOLVED. Choosing one of two envelopes would decide which claim
# the subject check reads while the verifier authenticates the whole document.
build_release_state || blind "fixture"
jq -s -c '.[0] | {payload: .dsseEnvelope.payload, payloadType: .dsseEnvelope.payloadType,
                  dsseEnvelope: .dsseEnvelope}' "$ASSETS/multiple.intoto.jsonl" >"$STATE/both.jsonl" &&
	mv "$STATE/both.jsonl" "$ASSETS/multiple.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'both a top-level DSSE payload and a nested dsseEnvelope'
check "a document carrying BOTH envelopes refuses" "two competing claims" $?

build_release_state || blind "fixture"
printf '{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}\n' >"$ASSETS/multiple.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not a provenance document this ceremony recognises'
check "a bundle with no dsseEnvelope refuses" "unrecognised, not empty-accepted" $?

build_release_state || blind "fixture"
jq -s -c '.[0] | .dsseEnvelope.payload = 12345' "$ASSETS/multiple.intoto.jsonl" >"$STATE/num.jsonl" &&
	mv "$STATE/num.jsonl" "$ASSETS/multiple.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not a provenance document this ceremony recognises'
check "a non-string payload refuses" "type checked, not presence" $?

build_release_state || blind "fixture"
jq -s -c '.[0] | .dsseEnvelope.payloadType = "application/octet-stream"' "$ASSETS/multiple.intoto.jsonl" \
	>"$STATE/pt.jsonl" && mv "$STATE/pt.jsonl" "$ASSETS/multiple.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not application/vnd.in-toto+json'
check "a payloadType that is not in-toto refuses" "the statement must claim to be one" $?

build_release_state || blind "fixture"
jq -s -c '.[0] | .dsseEnvelope.payload = "!!!not-base64!!!"' "$ASSETS/multiple.intoto.jsonl" \
	>"$STATE/b64.jsonl" && mv "$STATE/b64.jsonl" "$ASSETS/multiple.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'not decodable base64'
check "an undecodable payload refuses" "the decode is real" $?

# THE SUBJECT BINDING STILL HOLDS THROUGH THE NEW CARRIER. Without this the rows above are
# satisfied by a parser that accepts a bundle and then checks nothing inside it.
build_release_state || blind "fixture"
jq -s -c --arg p "$(printf '{"_type":"https://in-toto.io/Statement/v1","predicateType":"https://slsa.dev/provenance/v1","subject":[{"name":"other","digest":{"sha256":"%064d"}}]}' 0 | base64 -w0)" \
	'.[0] | .dsseEnvelope.payload = $p' "$ASSETS/multiple.intoto.jsonl" >"$STATE/sub.jsonl" &&
	mv "$STATE/sub.jsonl" "$ASSETS/multiple.intoto.jsonl"
rebuild_assets_json
run_finalizer
[ "$rc" -eq 1 ] && [ "$(patch_count)" -eq 0 ] && says 'does not cover every released archive'
check "a bundle whose subjects are other bytes refuses" "the carrier did not weaken the binding" $?

build_release_state || blind "fixture"
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "and the nominal bundle candidate still publishes" "the carrier rows are not always-red" $?

# ============================================================================================
# I · MUTATION ROUND. Everything above asserts the finalizer refuses. These two prove the
# assertions are wired to the real effect path: remove a verification, or move the PATCH
# ahead of it, and this battery MUST go red.
# ============================================================================================
mutant_run() { # mutant_run <sed-expression> <fixture-setup-fn>
	local expr="$1" setup="$2"
	restore_sut || return 1
	sed -i "$expr" "$SUT" || return 1
	"$setup" || return 1
	run_finalizer
}
# ⛔ THE MUTANT HAS TO DISABLE THE CHECK, NOT INVERT IT, and the first version of this
# section got that wrong in a way that PASSED: replacing `[ "$va_rc" -eq 0 ] ||` with
# `false && … ||` makes the refusal fire ALWAYS, so "the mutant is still caught" was true
# for the wrong reason — the battery was measuring a script that refuses everything. The
# working form replaces the condition with `true ||`, which is a check that cannot refuse.
setup_wrong_key() { build_release_state other; }

# M1a — the configured-anchor check cannot refuse. The candidate is signed by the
# INDEPENDENT key, so the SHIPPED-anchor check must still catch it on its own.
mutant_run 's#^\[ "\$va_rc" -eq 0 \] ||#true ||#' setup_wrong_key
_m1a_rc="$rc"
_m1a_patches="$(patch_count)"
_m1a_says_shipped=1
says "embedded OTA anchor does not accept" || _m1a_says_shipped=0
[ "$_m1a_rc" -ne 0 ] && [ "$_m1a_patches" -eq 0 ] && [ "$_m1a_says_shipped" -eq 1 ]
check "[mutant] with the configured check disabled, the SHIPPED anchor still refuses" "defence in depth is real" $?

# M1b — and the other way round.
mutant_run 's#^\[ "\$vs_rc" -eq 0 \] ||#true ||#' setup_wrong_key
_m1b_rc="$rc"
_m1b_patches="$(patch_count)"
_m1b_says_anchor=1
says "does not verify under the configured public anchor" || _m1b_says_anchor=0
[ "$_m1b_rc" -ne 0 ] && [ "$_m1b_patches" -eq 0 ] && [ "$_m1b_says_anchor" -eq 1 ]
check "[mutant] with the shipped check disabled, the CONFIGURED anchor still refuses" "two independent verifications" $?

# M2 — BOTH disabled. The wrong-key candidate must then be PUBLISHED, and that is the row
# that makes section B mean something: if this mutant still refused, those refusals would be
# coming from somewhere other than the signature.
mutant_run 's#^\[ "\$va_rc" -eq 0 \] ||#true ||#; s#^\[ "\$vs_rc" -eq 0 \] ||#true ||#' setup_wrong_key
[ "$(patch_count)" -eq 1 ]
check "[mutant] removing BOTH OTA checks DOES publish the wrong-key candidate" "the refusals are causal" $?

# M3 — the publish PATCH moved ahead of verification. A finalizer that publishes first cannot
# be a finalizer, and the fixture that proves it is the one every other row refuses.
setup_missing_sig() {
	build_release_state || return 1
	rm -f "$ASSETS/stable-manifest.json.sig" "$TREE/ota-dist/stable-manifest.json.sig"
	rebuild_assets_json
}
mutant_run 's#^_say "profile \${PROFILE}#PATCH_ISSUED=1; gh_api_early() { :; }; "$GH_BIN" api --method PATCH "repos/${REPOSITORY}/releases/'"$RELEASE_ID"'" -F draft=false >/dev/null 2>\&1 || true\n_say "profile ${PROFILE}#' setup_missing_sig
[ "$(patch_count)" -ge 1 ]
check "[mutant] a PATCH before verification is observable" "the adapter records effects" $?
# M4 — the retry loop removed. The dropped-transfer row above must then fail to publish: a
# row that passes with one attempt was never measuring the retry.
restore_sut || blind "fixture"
sed -i 's/^FETCH_ATTEMPTS=3$/FETCH_ATTEMPTS=1/' "$SUT" || blind "mutant M4 did not apply"
command grep -q '^FETCH_ATTEMPTS=1$' "$SUT"
check "[mutant] the attempt count is a literal the mutation can reach" "mutant applied" $?
build_release_state || blind "fixture"
run_finalizer GH_FETCH_FLAKY_NAME=checksums.txt GH_FETCH_DROP_FIRST=1
[ "$rc" -ne 0 ] && [ "$(patch_count)" -eq 0 ]
check "[mutant] with a single attempt the dropped transfer ends the ceremony" "the retry is causal" $?

# M5 — the candidate read back through the tag route, which is what this change replaced. The
# stub answers 404 for a draft exactly as GitHub does, so the nominal publication must die.
restore_sut || blind "fixture"
sed -i 's#--paginate "repos/${REPOSITORY}/releases?per_page=100"#"repos/${REPOSITORY}/releases/tags/${RELEASE_TAG}"#' "$SUT" || blind "mutant M5 did not apply"
# The CODE line, not the comment above it that also says "releases/tags/": an applied-check
# a failed sed would still satisfy is a check that cannot fail.
command grep -q 'gh_api "repos/${REPOSITORY}/releases/tags/' "$SUT"
check "[mutant] the list read is a literal the mutation can reach" "mutant applied" $?
build_release_state || blind "fixture"
run_finalizer
[ "$rc" -ne 0 ] && [ "$(patch_count)" -eq 0 ]
check "[mutant] reading the candidate by TAG cannot see the draft" "the list read is causal" $?

restore_sut
build_release_state || blind "fixture"
run_finalizer
[ "$rc" -eq 0 ] && [ "$(patch_count)" -eq 1 ]
check "the restored finalizer still publishes the good candidate" "the mutants were undone" $?

echo ""
echo "== summary =="
printf 'pass=%d fail=%d\n' "$pass" "$fail"
if [ "$fail" -ne 0 ]; then
	printf 'failed:'
	for f in "${failed_names[@]}"; do printf ' %s' "$f"; done
	printf '\n'
	echo "test-release-finalize-stable: RED"
	exit 1
fi
echo "test-release-finalize-stable: OK — $pass cases, real signatures and a recording adapter"
