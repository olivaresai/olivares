#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/resolve-bare-manifest-digest.sh — the exact-selection replacement
# for `head -1` (design §E.1.5, finding P1-SUSPECT). The property under test: the digest
# that provenance and attestations bind to comes from the ONE artifact named
# ${OCI_IMAGE_REPO}:${RELEASE_VERSION}, and every ambiguous or impossible selection
# REFUSES instead of guessing. The fixture deliberately lists image artifacts and the
# FIPS manifest BEFORE the bare manifest: a first-match implementation would pick those,
# which is precisely the defect this script replaces.
#
# The registry half runs against a PATH-stubbed `docker` (hermetic, no network): the stub
# prints whatever the fixture plants, so the format assertion and the push-vs-registry
# digest cross-check are both exercised, including their refusals.
#
# NO `set -e` (battery reports through check(); see test-pg-test-env.sh).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/resolve-bare-manifest-digest.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-resolve.XXXXXX")" || exit 1
# ${TMPDIR:-/tmp} may be mounted noexec (the dev container's /tmp is), and this battery
# runs PATH-stubbed binaries: a stub there dies at execve with EACCES, so the rows that
# exercise the stub fail while the rows that do not, pass. Measured 2026-08-01: this file
# reported "9 passed, 5 failed" on /tmp and "14 passed, 0 failed" with an exec TMPDIR —
# and it had never passed on this host, so `lint:release-mechanics` blocked every push
# the moment it reached main. Same probe test-cosign-guard.sh already carried; this
# battery landed without it.
printf '#!/bin/sh\nexit 0\n' >"$WORK/.execprobe" && chmod +x "$WORK/.execprobe"
if ! "$WORK/.execprobe" >/dev/null 2>&1; then
	rm -rf "$WORK"
	WORK="$(mktemp -d "$ROOT/.tmpexec.XXXXXX")" || exit 1
fi
rm -f "$WORK/.execprobe"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-62s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-62s %s\n' "$1" "$2"
	fi
}

GOOD="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
OTHER="sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

# The head-1 trap fixture: images first, fips manifest next, bare manifest LAST.
ART='[
  {"type":"Published Docker Image","name":"ghcr.io/foo/bar:1.2.3-amd64"},
  {"type":"Published Docker Image","name":"ghcr.io/foo/bar:1.2.3-arm64"},
  {"type":"Docker Manifest","name":"ghcr.io/foo/bar:1.2.3-fips","extra":{"Digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
  {"type":"Docker Manifest","name":"ghcr.io/foo/bar:1.2.3","extra":{"Digest":"'"$GOOD"'"}}
]'

n=0
run_resolve() { # run_resolve ARTIFACTS VERSION extra-env…
	artifacts="$1"
	version="$2"
	shift 2
	n=$((n + 1))
	out="$WORK/out.$n"
	err="$WORK/err.$n"
	: >"$out"
	env ARTIFACTS="$artifacts" OCI_IMAGE_REPO=ghcr.io/foo/bar RELEASE_VERSION="$version" \
		GITHUB_OUTPUT="$out" "$@" bash "$SCRIPT" >"$WORK/stdout.$n" 2>"$err"
	rc=$?
}

echo "resolve-bare-manifest-digest — exact selection, never first-match"

# --- selection half (no registry) -------------------------------------------------------
run_resolve "$ART" 1.2.3 OLIVARES_RESOLVE_SELECT_ONLY=1
[ "$rc" -eq 0 ] && grep -qx "manifest=ghcr.io/foo/bar:1.2.3" "$out" && grep -qx "digest=$GOOD" "$out"
check "the bare manifest is selected although it is listed LAST" "not first-match" $?

run_resolve "$ART" 9.9.9 OLIVARES_RESOLVE_SELECT_ONLY=1
[ "$rc" -ne 0 ] && grep -q 'found 0' "$err" && grep -q '1.2.3-fips' "$err"
check "zero matches refuse and NAME the manifests that exist" "no guess" $?

DUP="$(printf '%s' "$ART" | jq '. + [.[3]]')"
run_resolve "$DUP" 1.2.3 OLIVARES_RESOLVE_SELECT_ONLY=1
[ "$rc" -ne 0 ] && grep -q 'found 2' "$err"
check "duplicate matches refuse" "ambiguity fails" $?

BADMETA="$(printf '%s' "$ART" | jq '.[3].extra.Digest = "not-a-digest"')"
run_resolve "$BADMETA" 1.2.3 OLIVARES_RESOLVE_SELECT_ONLY=1
[ "$rc" -ne 0 ]
check "a malformed recorded digest refuses" "sha256 grammar" $?

run_resolve "not json" 1.2.3 OLIVARES_RESOLVE_SELECT_ONLY=1
[ "$rc" -ne 0 ]
check "unparseable ARTIFACTS refuses" "jq error" $?

n=$((n + 1))
env ARTIFACTS="$ART" OCI_IMAGE_REPO= RELEASE_VERSION=1.2.3 OLIVARES_RESOLVE_SELECT_ONLY=1 \
	bash "$SCRIPT" >"$WORK/stdout.$n" 2>"$WORK/err.$n"
[ $? -ne 0 ]
check "a missing OCI_IMAGE_REPO refuses" "no target guess" $?

# MANDATORY metadata under the exact pin (P2-01): v2.17.0 always records extra.Digest,
# so its absence means the artifact list is not from the pinned engine — never fall
# back to trusting the live registry alone.
NOMETA="$(printf '%s' "$ART" | jq 'del(.[3].extra)')"
run_resolve "$NOMETA" 1.2.3 OLIVARES_RESOLVE_SELECT_ONLY=1
[ "$rc" -ne 0 ] && grep -q 'no extra.Digest' "$err"
check "a MISSING extra.Digest refuses (pinned-engine contract)" "P2-01" $?

# --- registry half (PATH-stubbed docker) ------------------------------------------------
mkdir -p "$WORK/bin" || exit 1
cat >"$WORK/bin/docker" <<EOF
#!/usr/bin/env bash
case "\$*" in
*"--format {{.Manifest.Digest}}"*) cat "$WORK/stub-digest" ;;
*"--format {{json .Manifest}}"*) cat "$WORK/stub-manifest" ;;
*) echo "stub docker: unexpected args: \$*" >&2; exit 64 ;;
esac
EOF
chmod +x "$WORK/bin/docker" || exit 1

INDEX_OK='{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"'"$GOOD"'","manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}}]}'
printf '%s\n' "$GOOD" >"$WORK/stub-digest"
printf '%s\n' "$INDEX_OK" >"$WORK/stub-manifest"
run_resolve "$ART" 1.2.3 PATH="$WORK/bin:$PATH"
[ "$rc" -eq 0 ] && grep -qx "digest=$GOOD" "$out"
check "matching digest + expected index plan resolves" "cross-check OK" $?

printf '%s\n' "$OTHER" >"$WORK/stub-digest"
run_resolve "$ART" 1.2.3 PATH="$WORK/bin:$PATH"
[ "$rc" -ne 0 ] && grep -q 'moved between push and resolution' "$err"
check "a registry/push digest MISMATCH refuses (tag moved)" "nothing attested" $?

printf '%s\n' "garbage output" >"$WORK/stub-digest"
run_resolve "$ART" 1.2.3 PATH="$WORK/bin:$PATH"
[ "$rc" -ne 0 ]
check "a non-digest registry answer refuses" "sha256 grammar" $?

: >"$WORK/stub-digest"
run_resolve "$ART" 1.2.3 PATH="$WORK/bin:$PATH"
[ "$rc" -ne 0 ]
check "an EMPTY registry answer refuses" "no empty ref downstream" $?

# --- the OCI plan assertion (P2-01): received == intended -------------------------------
printf '%s\n' "$GOOD" >"$WORK/stub-digest"
printf '%s\n' '{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"'"$GOOD"'"}' >"$WORK/stub-manifest"
run_resolve "$ART" 1.2.3 PATH="$WORK/bin:$PATH"
[ "$rc" -ne 0 ] && grep -q 'must be a multi-arch index/list' "$err"
check "a single-manifest bare tag refuses (not the release plan)" "mediaType" $?

printf '%s\n' '{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}' >"$WORK/stub-manifest"
run_resolve "$ART" 1.2.3 PATH="$WORK/bin:$PATH"
[ "$rc" -ne 0 ] && grep -q 'expects exactly' "$err"
check "a MISSING platform child refuses" "incomplete plan" $?

printf '%s\n' '{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}},{"platform":{"os":"unknown","architecture":"unknown"}}]}' >"$WORK/stub-manifest"
run_resolve "$ART" 1.2.3 PATH="$WORK/bin:$PATH"
[ "$rc" -ne 0 ] && grep -q 'expects exactly' "$err"
check "an EXTRA child refuses (nothing smuggled into the index)" "exact plan" $?

echo ""
echo "resolve-bare-manifest-digest battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
