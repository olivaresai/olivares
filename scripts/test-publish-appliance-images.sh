#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-publish-appliance-images.sh — the R2 image route's bindings, gates and order, driven
# against a stateful wrangler stub that really stores and returns bytes.
#
# Hermetic: no network, no bucket, nothing installed. The stub keeps a bucket directory and
# a LOG of every operation in argv order, which is what lets the battery assert that the
# index is written LAST and that a partial failure removes exactly what the run created.
#
# exit 0 all rows green; exit 1 any row red (printed as FAIL at column 0).
set -uo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/publish-appliance-images.sh"

blind() { echo "test-publish-appliance-images: UNABLE TO LOOK: $*" >&2; exit 2; }
for tool in bash jq sha256sum curl awk sort tac; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tpai.XXXXXX")" || blind "cannot allocate a work directory"
# ${TMPDIR:-/tmp} may be mounted noexec (this dev container's /tmp is) and this battery runs
# a PATH-stubbed wrangler: there, bash's -x test and execve both refuse the stub and every
# row fails while the gates themselves are fine. The same probe the finalize battery uses.
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

# A stateful wrangler stub: r2 object put/get/delete against $STUB_BUCKET, every call logged
# to $STUB_LOG. get of a missing object fails with the wording the real one uses.
mkdir -p "$WORK/bin" "$WORK/bucket"
cat >"$WORK/bin/wrangler" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
[ "$1" = "--version" ] && { printf '4.100.0\n'; exit 0; }
printf '%s\n' "$*" >>"${STUB_LOG:?}"
case "$1 $2 $3" in
"r2 object put")
	# wrangler r2 object put <bucket/key> --remote --force --file F ...
	key="" file=""
	while [ "$#" -gt 0 ]; do
		case "$1" in
		olivares-appliance/*) key="${1#olivares-appliance/}" ;;
		--file) file="${2:?}" ; shift ;;
		esac
		shift
	done
	[ -n "$key" ] && [ -n "$file" ] || { echo "stub: no key or file" >&2; exit 2; }
	mkdir -p "${STUB_BUCKET:?}/$(dirname "$key")"
	cp "$file" "$STUB_BUCKET/$key" || exit 2
	exit 0
	;;
"r2 object get")
	key="" file=""
	while [ "$#" -gt 0 ]; do
		case "$1" in
		olivares-appliance/*) key="${1#olivares-appliance/}" ;;
		--file) file="${2:?}" ; shift ;;
		esac
		shift
	done
	if [ -s "$STUB_BUCKET/$key" ]; then cp "$STUB_BUCKET/$key" "$file" && exit 0; fi
	echo " ✘  [ERROR] Object with key '$key' does not exist (10007)" >&2
	exit 1
	;;
"r2 object delete")
	key=""
	while [ "$#" -gt 0 ]; do
		case "$1" in olivares-appliance/*) key="${1#olivares-appliance/}" ;; esac
		shift
	done
	rm -f -- "$STUB_BUCKET/$key"
	exit 0
	;;
esac
echo "stub: unmodelled call: $*" >&2
exit 2
STUB
chmod +x "$WORK/bin/wrangler"
printf '#!/bin/sh\n# a wrangler of the wrong review\n' >"$WORK/bin/wrangler-wrong"
printf '4.9.0\n' >"$WORK/bin/wrangler-wrong-output"

run_pub() { # run_pub [VAR=VAL…] -- <script arguments…>
	# Leading VAR=VAL pairs override the stub environment; everything else is the SCRIPT's
	# own argv. They cannot share one env command line: `env --version …` would answer as
	# ENV's option, run nothing, and report 0 — exactly the false green this split exists
	# to make impossible.
	local -a vars=()
	while [ "$#" -gt 0 ]; do
		case "$1" in
		*=*) vars+=("$1") ; shift ;;
		*) break ;;
		esac
	done
	(
		cd "$WORK" &&
			env -i PATH="$WORK/bin:$WORK/curlbin:/usr/bin:/bin" HOME="$WORK" \
				STUB_BUCKET="$WORK/bucket" STUB_LOG="$WORK/log" \
				OLIVARES_APPLIANCE_PUBLISH_APPROVED=1 CLOUDFLARE_API_TOKEN=stub-token \
				CLOUDFLARE_ACCOUNT_ID=8763fa9c803b42fb43ffe6e5ad64815e \
				OLIVARES_WRANGLER_BIN="$WORK/bin/wrangler" TMPDIR="$WORK" \
				"${vars[@]}" bash "$SCRIPT" "$@"
	) >"$WORK/out" 2>"$WORK/err"
}

echo "publish-appliance-images — bindings, gates, order and rollback (stubbed R2)"

# --- gates ---------------------------------------------------------------------------------
run_pub --version 26.10.0 --dir "$WORK/files" --origin https://appliance.olivares.ai
[ "$?" -eq 2 ]
check "a missing --dir is a refusal to run" "usage" $?

mkdir -p "$WORK/files"
printf 'image bytes\n' >"$WORK/files/olivares-appliance-server-amd64.iso"

run_pub --version v26.10.0 --dir "$WORK/files" --origin https://appliance.olivares.ai
[ "$?" -eq 2 ]
check "a v-prefixed version is a refusal to run" "X.Y.Z" $?

run_pub --version 26.10.0 --dir "$WORK/files" --origin http://appliance.olivares.ai
[ "$?" -eq 2 ]
check "a non-https origin is a refusal to run" "https only" $?

(
	cd "$WORK" && env -i PATH="/usr/bin:/bin" HOME="$WORK" \
		OLIVARES_APPLIANCE_PUBLISH_APPROVED=1 CLOUDFLARE_API_TOKEN=stub-token \
		CLOUDFLARE_ACCOUNT_ID=8763fa9c803b42fb43ffe6e5ad64815e TMPDIR="$WORK" \
		bash "$SCRIPT" --version 26.10.0 --dir "$WORK/files" --origin https://appliance.olivares.ai
) >"$WORK/out" 2>"$WORK/err"
[ "$?" -eq 2 ] && grep -q 'wrangler is not an absolute executable' "$WORK/err"
check "a missing wrangler is a refusal to run" "no fallback" $?

run_pub OLIVARES_APPLIANCE_PUBLISH_APPROVED=0 --version 26.10.0 --dir "$WORK/files" --origin https://appliance.olivares.ai
[ "$?" -eq 2 ] && grep -q 'OLIVARES_APPLIANCE_PUBLISH_APPROVED' "$WORK/err"
check "publication without the approved environment is a refusal" "env gate" $?

run_pub OLIVARES_WRANGLER_BIN="$WORK/bin/wrangler-wrong" --version 26.10.0 --dir "$WORK/files" --origin https://appliance.olivares.ai
[ "$?" -eq 2 ]
check "a wrangler outside the reviewed pin is a refusal" "4.100.0" $?

printf 'bad/name\n' >"$WORK/files/bad-name.tmp" 2>/dev/null || true
mkdir -p "$WORK/unsafe" && printf 'x\n' >"$WORK/unsafe/a:b"
run_pub --version 26.10.0 --dir "$WORK/unsafe" --origin https://appliance.olivares.ai
[ "$?" -eq 1 ]
check "an unsafe file name is refused" "basename" $?

: >"$WORK/empty.iso" 2>/dev/null
mkdir -p "$WORK/empties" && : >"$WORK/empties/olivares-appliance-server-amd64.iso"
run_pub --version 26.10.0 --dir "$WORK/empties" --origin https://appliance.olivares.ai
[ "$?" -eq 1 ]
check "an empty file is refused" "no zero-byte objects" $?

# A file over the single-PUT cap (sparse, so the row costs nothing): named refusal.
mkdir -p "$WORK/huge" && truncate -s 5363466241 "$WORK/huge/olivares-appliance-desktop-amd64.ova"
run_pub --version 26.10.0 --dir "$WORK/huge" --origin https://appliance.olivares.ai
[ "$?" -eq 1 ] && grep -q 'multipart follow-up' "$WORK/err"
check "a file over the single-PUT cap is refused naming multipart" "5 GiB less 5 MiB" $?
rm -rf "$WORK/huge"

# --- the happy path: order, readback, index last -------------------------------------------
mkdir -p "$WORK/pub"
printf 'iso bytes\n' >"$WORK/pub/olivares-appliance-server-amd64.iso"
printf 'qcow2 bytes\n' >"$WORK/pub/olivares-appliance-server-amd64.qcow2"
printf 'sums\n' >"$WORK/pub/SHA256SUMS"
mkdir -p "$WORK/curlbin"
cat >"$WORK/curlbin/curl" <<'CURLSTUB'
#!/usr/bin/env bash
# curl stub: serves the object the origin would serve, from the stub bucket.
out="" rest=()
while [ "$#" -gt 0 ]; do
	case "$1" in
	--output) out="${2:?}" ; shift ;;
	-*) ;;
	http*) rest+=("$1") ;;
	esac
	shift
done
url="${rest[0]:-}"
key="appliance/${url#*/appliance/}"
[ -n "$out" ] && [ -s "${STUB_BUCKET:?}/$key" ] && cp "$STUB_BUCKET/$key" "$out" && exit 0
echo "curl stub: no object for $url" >&2
exit 22
CURLSTUB
chmod +x "$WORK/curlbin/curl"
: >"$WORK/log"
run_pub --version 26.10.0 --dir "$WORK/pub" --origin https://appliance.olivares.ai --run-id 42
rc=$?
[ "$rc" -eq 0 ]
check "the publication completes against the stub" "exit 0" $?
cmp -s "$WORK/pub/olivares-appliance-server-amd64.iso" "$WORK/bucket/appliance/26.10.0/olivares-appliance-server-amd64.iso"
check "the object in the bucket is the verified bytes" "readback" $?
jq -e '.schema == "olivares.ai/appliance-index/v1" and .version == "26.10.0" and (.files | length == 3)' \
	"$WORK/bucket/appliance/26.10.0/index.json" >/dev/null
check "the index names every published file" "discovery" $?
last_put="$(grep -n '^r2 object put' "$WORK/log" | tail -1)"
grep -q 'put olivares-appliance/appliance/26.10.0/index.json' <<<"$last_put"
check "the index is the LAST object written" "roots last" $?

# --- idempotence and the immutable-prefix fence --------------------------------------------
run_pub --version 26.10.0 --dir "$WORK/pub" --origin https://appliance.olivares.ai --run-id 43
[ "$?" -eq 0 ] && grep -q 'already matches' "$WORK/out"
check "a rerun with identical bytes skips every object" "immutable prefix" $?

printf 'different bytes\n' >"$WORK/pub2.iso" 2>/dev/null || true
mkdir -p "$WORK/pub2" && printf 'tampered\n' >"$WORK/pub2/olivares-appliance-server-amd64.iso"
run_pub --version 26.10.0 --dir "$WORK/pub2" --origin https://appliance.olivares.ai
[ "$?" -eq 1 ] && grep -q 'refusing overwrite' "$WORK/err"
check "an existing object with different bytes is a refused overwrite" "never clobber" $?

# --- rollback removes exactly what the run created -----------------------------------------
mkdir -p "$WORK/rollback" "$WORK/bucket2"
printf 'a\n' >"$WORK/rollback/olivares-appliance-server-amd64.iso"
printf 'b\n' >"$WORK/rollback/olivares-appliance-server-amd64.qcow2"
cat >"$WORK/bin/wrangler-failsecond" <<'FAILSTUB'
#!/usr/bin/env bash
[ "$1" = "--version" ] && { printf '4.100.0\n'; exit 0; }
printf '%s\n' "$*" >>"${STUB_LOG:?}"
if [ "$1 $2 $3" = "r2 object put" ] && [ "${OLIVARES_FAIL_ON:-}" = "$*" ]; then
	echo "simulated transport failure" >&2
	exit 1
fi
exec "$OLIVARES_GOOD_WRANGLER" "$@"
FAILSTUB
chmod +x "$WORK/bin/wrangler-failsecond"
cp -r "$WORK/bucket" "$WORK/bucket-keep" 2>/dev/null || true
: >"$WORK/log"
run_pub OLIVARES_WRANGLER_BIN="$WORK/bin/wrangler-failsecond" \
	OLIVARES_GOOD_WRANGLER="$WORK/bin/wrangler" \
	OLIVARES_FAIL_ON="r2 object put olivares-appliance/appliance/26.10.1/olivares-appliance-server-amd64.qcow2 --remote --force --file $WORK/rollback/olivares-appliance-server-amd64.qcow2 --content-type application/octet-stream --cache-control public, max-age=31536000, immutable" \
	--version 26.10.1 --dir "$WORK/rollback" --origin https://appliance.olivares.ai
[ "$?" -eq 1 ]
check "a failed put fails the publication" "fail closed" $?
[ ! -e "$WORK/bucket/appliance/26.10.1/olivares-appliance-server-amd64.iso" ]
check "the rollback removed the object the failed run created" "no partial prefix" $?
grep -q 'r2 object delete olivares-appliance/appliance/26.10.1/olivares-appliance-server-amd64.iso' "$WORK/log"
check "the rollback deleted by exact key" "created set only" $?

printf 'publish-appliance-images battery: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
