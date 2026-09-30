#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-publish-appliance-release.sh — the appliance publisher's bindings and routes, driven
# against stubbed gh, cosign and curl, with a stubbed R2 leg beside them.
#
# Hermetic: no network, no repository, nothing signed. The cosign stub records the identity
# regexp it was handed and answers from a marker file, which is what lets the battery assert
# that the publisher derives the identity from the RUNNING repository and ref, not a literal;
# the gh stub keeps the release's assets in a state file the curl stub then serves, so the
# create-only upload and the public-delivery postcondition are observed, not assumed.
#
# exit 0 all rows green; exit 1 any row red (FAIL at column 0).
set -uo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/publish-appliance-release.sh"

blind() { echo "test-publish-appliance-release: UNABLE TO LOOK: $*" >&2; exit 2; }
for tool in bash jq sha256sum; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tpar.XXXXXX")" || blind "cannot allocate a work directory"
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
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok  %-64s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf 'FAIL  %-64s %s\n' "$1" "$2"
	fi
}

# The fixture tree: the publisher reads scripts/publish-appliance-images.sh beside itself, so
# both the script under test and a RECORDING stub for the R2 leg live in $TREE/scripts.
TREE="$WORK/tree"
mkdir -p "$TREE/scripts"
cp "$SCRIPT" "$TREE/scripts/publish-appliance-release.sh"
cat >"$TREE/scripts/publish-appliance-images.sh" <<'R2STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${R2_LOG:?}"
exit 0
R2STUB
chmod +x "$TREE/scripts/publish-appliance-images.sh"

mkdir -p "$WORK/bin"
# gh stub: release view answers from $GH_STATE (assets listed one name per line, URL derived);
# release upload appends the names to the state and honors $GH_UPLOAD_RC / $GH_UPLOAD_FAIL_NAMES.
cat >"$WORK/bin/gh" <<'GHSTUB'
#!/usr/bin/env bash
set -uo pipefail
printf '%s\n' "$*" >>"${GH_LOG:?}"
state="${GH_STATE:?}"
case "$1 $2" in
"release view")
	tag="$3"
	json=""
	while [ "$#" -gt 0 ]; do
		case "$1" in
		--json) json="${2:?}" ; shift ;;
		esac
		shift
	done
	case "$json" in
	isDraft,tagName | isDraft,tagName,url)
		printf '{"isDraft":%s,"tagName":"%s","url":"https://github.example.test/releases/tag/%s"}\n' "${GH_IS_DRAFT:-false}" "$tag" "$tag"
		;;
	assets)
		printf '{"assets":['
		first=1
		while IFS= read -r name; do
			[ -n "$name" ] || continue
			[ "$first" -eq 1 ] || printf ','
			first=0
			printf '{"name":"%s","browser_download_url":"https://github.example.test/releases/download/%s/%s"}' "$name" "$tag" "$name"
		done <"$state"
		printf ']}\n'
		;;
	*) echo "gh stub: unmodelled --json $json" >&2; exit 2 ;;
	esac
	exit 0
	;;
"release upload")
	tag="$2"; shift 2
	for name in "$@"; do
		if [ -n "${GH_UPLOAD_FAIL_NAMES:-}" ] && printf ' %s ' " $name " | grep -q " $name "; then
			if [ ",$GH_UPLOAD_FAIL_NAMES," = ",ALL," ] || printf '%s' "$GH_UPLOAD_FAIL_NAMES" | grep -qxF -- "$name"; then
				echo "gh stub: simulated upload failure for $name" >&2
				exit 1
			fi
		fi
		printf '%s\n' "$name" >>"$state"
	done
	exit "${GH_UPLOAD_RC:-0}"
	;;
esac
echo "gh stub: unmodelled call: $*" >&2
exit 2
GHSTUB
chmod +x "$WORK/bin/gh"
# cosign stub: record every argument and require the actual certificate/signature files.
cat >"$WORK/bin/cosign" <<'COSIGNSTUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${COSIGN_LOG:?}"
printf '%s\n' "$@" >>"${COSIGN_ARGV_LOG:?}"
[ "$1" = "verify-blob" ] || exit 2
shift
certificate="" signature=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--certificate) certificate="${2:?}" ; shift 2 ;;
	--signature) signature="${2:?}" ; shift 2 ;;
	*) shift ;;
	esac
done
for required in "$certificate" "$signature"; do
	[ -f "$required" ] || { printf 'cosign stub: missing file: %s\n' "$required" >&2; exit 1; }
done
exit "${COSIGN_RC:-0}"
COSIGNSTUB
chmod +x "$WORK/bin/cosign"
# curl stub: serves $WORK/pub/<name> for any URL ending in that name.
cat >"$WORK/bin/curl" <<'CURLSTUB'
#!/usr/bin/env bash
out="" rest=()
while [ "$#" -gt 0 ]; do
	case "$1" in
	--output) out="${2:?}" ; shift ;;
	-*) ;;
	http*) rest+=("$1") ;;
	esac
	shift
done
name="$(basename "${rest[0]:-}")"
[ -n "$out" ] && [ -s "${SERVE_DIR:?}/$name" ] && cat "$SERVE_DIR/$name" >"$out" && exit 0
echo "curl stub: no bytes for ${rest[0]:-<none>}" >&2
exit 22
CURLSTUB
chmod +x "$WORK/bin/curl"

run_pub() { # run_pub [VAR=VAL…] -- <script arguments…>
	local -a vars=()
	while [ "$#" -gt 0 ]; do
		case "$1" in
		*=*) vars+=("$1") ; shift ;;
		*) break ;;
		esac
	done
	(
		cd "$TREE" &&
			env -i PATH="$WORK/bin:/usr/bin:/bin" HOME="$WORK" \
				GH_TOKEN=stub GITHUB_REPOSITORY=olivaresai/olivares GITHUB_REF=refs/heads/main \
				OLIVARES_COSIGN_BIN="$WORK/bin/cosign" TMPDIR="$WORK" \
				GH_STATE="$WORK/assets.state" GH_LOG="$WORK/gh.log" COSIGN_LOG="$WORK/cosign.log" \
				COSIGN_ARGV_LOG="$WORK/cosign-argv.log" \
				R2_LOG="$WORK/r2.log" SERVE_DIR="$WORK/pub" \
				OLIVARES_APPLIANCE_ORIGIN=https://appliance.olivares.ai \
				"${vars[@]}" bash "$TREE/scripts/publish-appliance-release.sh" "$@"
	) >"$WORK/out" 2>"$WORK/err"
}

# A publication set whose SHA256SUMS is genuinely the files' digests.
mkdir -p "$WORK/pub"
printf 'iso bytes\n' >"$WORK/pub/olivares-appliance-server-amd64.iso"
printf 'manifest\n' >"$WORK/pub/olivares-appliance-server-amd64.iso.manifest.json"
( cd "$WORK/pub" && sha256sum olivares-appliance-server-amd64.iso olivares-appliance-server-amd64.iso.manifest.json >SHA256SUMS )
printf 'sig\n' >"$WORK/pub/SHA256SUMS.sig"
printf 'pem\n' >"$WORK/pub/SHA256SUMS.pem"

echo "publish-appliance-release — bindings, signature gate and both routes (stubbed)"

# --- could-not-run gates -------------------------------------------------------------------
run_pub --version v26.10.0 --dir "$WORK/pub"
rc=$?
check "a v-prefixed version is a refusal to run" "X.Y.Z" "$((rc != 2))"
run_pub --version 26.10.0 --dir "$WORK/absent"
rc=$?
check "a missing directory is a refusal to run" "usage" "$((rc != 2))"
(
	cd "$TREE" && env -i PATH="$WORK/bin:/usr/bin:/bin" HOME="$WORK" \
		GH_TOKEN=stub GITHUB_REPOSITORY=olivaresai/olivares GITHUB_REF=refs/heads/main TMPDIR="$WORK" \
		bash "$TREE/scripts/publish-appliance-release.sh" --version 26.10.0 --dir "$WORK/pub"
) >"$WORK/out" 2>"$WORK/err"
[ "$?" -eq 2 ] && grep -q 'OLIVARES_COSIGN_BIN' "$WORK/err"
check "a missing verified cosign is a refusal to run" "no bare name" $?

# --- the signature gate --------------------------------------------------------------------
printf 'tampered\n' >"$WORK/pub/olivares-appliance-server-amd64.iso"
: >"$WORK/assets.state"
run_pub --version 26.10.0 --dir "$WORK/pub"
[ "$?" -eq 1 ] && grep -q 'do not match the SHA256SUMS' "$WORK/err"
check "files that do not match the signed table are refused" "binding" $?
printf 'iso bytes\n' >"$WORK/pub/olivares-appliance-server-amd64.iso"

: >"$WORK/assets.state"; : >"$WORK/cosign.log"
run_pub COSIGN_RC=1 --version 26.10.0 --dir "$WORK/pub"
[ "$?" -eq 1 ] && grep -q 'is not signed by this workflow' "$WORK/err"
check "a SHA256SUMS the workflow did not sign is refused" "signature gate" $?
grep -qF -- '--certificate-identity-regexp ^https://github\.com/olivaresai/olivares/\.github/workflows/appliance-image\.yml@refs/heads/main$' "$WORK/cosign.log"
check "the identity is derived from the RUNNING repository, workflow and ref" "no literal" $?

: >"$WORK/assets.state"
run_pub GH_IS_DRAFT=true --version 26.10.0 --dir "$WORK/pub"
[ "$?" -eq 1 ] && grep -q 'still a DRAFT' "$WORK/err"
check "a draft release is refused; the ceremony publishes first" "phase order" $?

# --- the GitHub route, create-only, with postconditions ------------------------------------
: >"$WORK/assets.state"; : >"$WORK/gh.log"; : >"$WORK/cosign.log"
run_pub --version 26.10.0 --dir "$WORK/pub"
rc=$?
check "a complete set publishes to the release and reads back" "exit 0" "$rc"

# --- the edition-named table: --sums drives every binding, as two editions on one release need
rm -f "$WORK/pub/SHA256SUMS.sig" "$WORK/pub/SHA256SUMS.pem"
for edition in server desktop; do
	edition_sums="SHA256SUMS-$edition"
	mv "$WORK/pub/SHA256SUMS" "$WORK/pub/$edition_sums"
	printf 'edition sig\n' >"$WORK/pub/$edition_sums.sig"
	printf 'edition pem\n' >"$WORK/pub/$edition_sums.pem"
	: >"$WORK/assets.state"; : >"$WORK/cosign.log"; : >"$WORK/cosign-argv.log"
	run_pub --version 26.10.0 --sums "$edition_sums" --dir "$WORK/pub"
	rc=$?
	check "the $edition edition publishes under its own table name" "--sums" "$rc"
	grep -qxF -- "$WORK/pub/$edition_sums.pem" "$WORK/cosign-argv.log"
	check "the $edition edition verifies its own certificate" "per-edition certificate" $?
	grep -qxF -- "$WORK/pub/$edition_sums.sig" "$WORK/cosign-argv.log"
	check "the $edition edition verifies its own signature" "per-edition signature" $?
	mv "$WORK/pub/$edition_sums" "$WORK/pub/SHA256SUMS"
	rm -f "$WORK/pub/$edition_sums.sig" "$WORK/pub/$edition_sums.pem"
done
printf 'sig\n' >"$WORK/pub/SHA256SUMS.sig"
printf 'pem\n' >"$WORK/pub/SHA256SUMS.pem"
printf '%s\n' "release upload 26.10.0 olivares-appliance-server-amd64.iso olivares-appliance-server-amd64.iso.manifest.json SHA256SUMS SHA256SUMS.pem SHA256SUMS.sig" >"$WORK/want-upload"
command grep -q '^release upload 26.10.0 ' "$WORK/gh.log"
check "the upload names the whole small-file set, create-only" "gh argv" $?
clobber_status=0
if command grep -q -- '--clobber' "$WORK/gh.log"; then clobber_status=1; fi
check "no clobber flag is ever passed" "delivered bytes stay" "$clobber_status"
small_r2_status=0
if [ -s "$WORK/r2.log" ]; then small_r2_status=1; fi
check "nothing small is routed to R2" "GitHub first" "$small_r2_status"

# --- create-only refusal over an existing asset --------------------------------------------
: >"$WORK/assets.state"
printf 'olivares-appliance-server-amd64.iso\n' >"$WORK/assets.state"
run_pub GH_UPLOAD_FAIL_NAMES=ALL --version 26.10.0 --dir "$WORK/pub"
[ "$?" -eq 1 ] && grep -q 'create-only refuses to replace' "$WORK/err"
check "an upload failure over existing names refuses create-only" "never replace" $?

# --- the R2 leg for what a release asset may not carry -------------------------------------
truncate -s 2147483649 "$WORK/pub/olivares-appliance-desktop-amd64.ova"
( cd "$WORK/pub" && sha256sum olivares-appliance-server-amd64.iso olivares-appliance-server-amd64.iso.manifest.json olivares-appliance-desktop-amd64.ova >SHA256SUMS )
: >"$WORK/assets.state"; : >"$WORK/r2.log"
run_pub --version 26.10.0 --dir "$WORK/pub"
rc=$?
rm -f "$WORK/pub/olivares-appliance-desktop-amd64.ova"
( cd "$WORK/pub" && sha256sum olivares-appliance-server-amd64.iso olivares-appliance-server-amd64.iso.manifest.json >SHA256SUMS )
[ "$rc" -eq 0 ]
check "a set with one over-limit file still completes (the R2 leg owns its own postcondition)" "routed and done" $?
command grep -q -- '--dir' "$WORK/r2.log" && command grep -q -- 'appliance.olivares.ai' "$WORK/r2.log"
check "the over-limit file was routed to the R2 leg with the origin" "2 GiB ceiling" $?

printf 'publish-appliance-release battery: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
