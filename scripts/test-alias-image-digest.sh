#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/alias-image-digest.sh (R1 contrast P1-01). Models the manifest
# source and the index source SEPARATELY, because the defect was format-dependent:
# `imagetools create` with a single-manifest source and default `--prefer-index=true`
# mints a NEW index with a NEW digest, so `latest-amd64`/`latest-arm64` stopped being
# the verified object while the log claimed otherwise. The property pinned here:
#
#   - a single-manifest source is created with `--prefer-index=false`;
#   - an index source is created without format coercion;
#   - EVERY source kind must end with digest(alias) == SRC_DIGEST or the script fails
#     before any success line — the assertion, not the flag, is the guarantee;
#   - unknown mediaTypes refuse before anything is created.
#
# Hermetic: a PATH-stubbed `docker` answers inspections from planted files and records
# the create argv. No registry, no network.
#
# NO `set -e` (battery reports through check(); see test-pg-test-env.sh).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/alias-image-digest.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-alias.XXXXXX")" || exit 1
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

SRC="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
WRAPPED="sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

mkdir -p "$WORK/bin" || exit 1
# Stub docker: dispatches on the imagetools subcommand/format. Source mediaType and
# alias digest come from planted files; the create argv is recorded for assertions.
cat >"$WORK/bin/docker" <<EOF
#!/usr/bin/env bash
args="\$*"
case "\$args" in
*"--format {{.Manifest.MediaType}}"*) cat "$WORK/mediatype" ;;
*"imagetools create"*) printf '%s\n' "\$@" >"$WORK/create-argv"; exit "\$(cat "$WORK/create-rc" 2>/dev/null || echo 0)" ;;
*"--format {{.Manifest.Digest}}"*) cat "$WORK/destdigest" ;;
*) echo "stub docker: unexpected args: \$args" >&2; exit 64 ;;
esac
EOF
chmod +x "$WORK/bin/docker" || exit 1

n=0
run_alias() { # run_alias mediatype destdigest [SRC override]
	n=$((n + 1))
	printf '%s\n' "$1" >"$WORK/mediatype"
	printf '%s\n' "$2" >"$WORK/destdigest"
	rm -f "$WORK/create-argv"
	err="$WORK/err.$n"
	env PATH="$WORK/bin:$PATH" OCI_IMAGE_REPO=ghcr.io/foo/bar SRC_DIGEST="${3:-$SRC}" ALIAS_TAG=latest-amd64 \
		bash "$SCRIPT" >"$WORK/stdout.$n" 2>"$err"
	rc=$?
}

echo "alias-image-digest — format preservation and digest identity"

# --- manifest source: the P1-01 case ----------------------------------------------------
run_alias application/vnd.oci.image.manifest.v1+json "$SRC"
[ "$rc" -eq 0 ] && grep -qx -- '--prefer-index=false' "$WORK/create-argv"
check "an OCI single-manifest source is created with --prefer-index=false" "no new index" $?

run_alias application/vnd.docker.distribution.manifest.v2+json "$SRC"
[ "$rc" -eq 0 ] && grep -qx -- '--prefer-index=false' "$WORK/create-argv"
check "a Docker v2 manifest source gets the same treatment" "no new index" $?

# --- index sources: copied as-is --------------------------------------------------------
run_alias application/vnd.oci.image.index.v1+json "$SRC"
[ "$rc" -eq 0 ] && ! grep -q -- '--prefer-index' "$WORK/create-argv"
check "an OCI index source is created WITHOUT format coercion" "as-is copy" $?

run_alias application/vnd.docker.distribution.manifest.list.v2+json "$SRC"
[ "$rc" -eq 0 ] && ! grep -q -- '--prefer-index' "$WORK/create-argv"
check "a Docker manifest list source is created without coercion" "as-is copy" $?

# --- THE assertion: digest(alias) must equal the verified source ------------------------
run_alias application/vnd.oci.image.manifest.v1+json "$WRAPPED"
[ "$rc" -ne 0 ] && grep -q "resolves to ${WRAPPED}, not the verified source ${SRC}" "$WORK/err.$n"
check "an alias resolving to a DIFFERENT digest fails, naming both" "identity assert" $?

run_alias application/vnd.oci.image.index.v1+json "$WRAPPED"
[ "$rc" -ne 0 ]
check "the identity assertion also guards the index path" "no format is exempt" $?

# --- refusals ---------------------------------------------------------------------------
run_alias application/vnd.example.unknown+json "$SRC"
[ "$rc" -ne 0 ] && [ ! -f "$WORK/create-argv" ]
check "an unknown source mediaType refuses BEFORE creating anything" "fail-closed" $?

run_alias application/vnd.oci.image.manifest.v1+json "$SRC" "sha256:short"
[ "$rc" -ne 0 ] && [ ! -f "$WORK/create-argv" ]
check "a malformed SRC_DIGEST refuses before creating anything" "digest grammar" $?

printf '%s\n' 1 >"$WORK/create-rc"
run_alias application/vnd.oci.image.manifest.v1+json "$SRC"
[ "$rc" -ne 0 ]
check "a failed imagetools create fails the script" "no silent half-alias" $?
rm -f "$WORK/create-rc"

echo ""
echo "alias-image-digest battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
