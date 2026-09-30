#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-appliance-release-delivery.sh — the release-delivery assembly's bindings, before any
# tool runs: the version contract, the pinned-key contract and the digest binding that makes
# the delivery the release's own bytes.
#
# The assembly's TAIL (nfpm, the builder container, header signing, repodata) is exercised by
# the publish workflow itself against the real tools; this battery pins the decisions that
# must refuse BEFORE those tools are touched, with go/docker/gpg stubbed on PATH so the rows
# that pass the early gates fail loudly at the stub instead of silently running a build.
#
# exit 0 all rows green; exit 1 any row red (FAIL at column 0).
set -uo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/appliance-release-delivery.sh"

blind() { echo "test-appliance-release-delivery: UNABLE TO LOOK: $*" >&2; exit 2; }
for tool in bash jq sha256sum; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tard.XXXXXX")" || blind "cannot allocate a work directory"
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

# The fixture source root: the script resolves its siblings from its own path, so the tree
# carries the kiwi files it names and the PINNED key anchor with a battery-local fingerprint.
TREE="$WORK/tree"
mkdir -p "$TREE/scripts" "$TREE/appliance/images/kiwi" "$TREE/packaging/nfpm" \
	"$TREE/appliance/selinux" "$TREE/appliance/images/toolchain/fedora44" "$TREE/bin"
cp "$SCRIPT" "$TREE/scripts/appliance-release-delivery.sh"
PIN="AA11BB22CC33DD44EE55FF6677889900AABBCCDD"
printf '{"schema":"x","release_fingerprint":"%s"}\n' "$PIN" >"$TREE/appliance/images/kiwi/package-repository-key.json"
for f in "packaging/nfpm/olivares-appliance-base.yaml" \
	"appliance/selinux/olivares-selinux.spec" \
	"appliance/images/toolchain/fedora44/Containerfile" \
	"appliance/images/kiwi/write_delivery.py" \
	"appliance/images/kiwi/delivery_check.py" \
	"scripts/rpm-payload-sign.py" \
	"scripts/render-rpm-repodata.py"; do
	: >"$TREE/$f"
done
# Tool stubs: the real jq/python3/sha256sum serve; go, docker and the gpg pair are stubs that
# log and fail, because every row that reaches them must be a RED row in this battery.
for tool in go docker gpg gpgconf; do
	cat >"$TREE/bin/$tool" <<STUB
#!/usr/bin/env bash
echo "$tool stub: reached" >&2
exit 3
STUB
	chmod +x "$TREE/bin/$tool"
done

# The release rpm fixture and the key directory.
mkdir -p "$WORK/key"
printf 'rpm bytes\n' >"$WORK/olivares_26.10.0_linux_amd64.rpm"
RPM_SHA="$(sha256sum "$WORK/olivares_26.10.0_linux_amd64.rpm" | cut -d' ' -f1)"
jq -n --arg fp "$PIN" '{openpgp_fingerprint:$fp, environment:"production"}' >"$WORK/key/descriptor.json"
for f in openpgp-secret.asc passphrase olivares-packages.asc; do printf 'x\n' >"$WORK/key/$f"; done

run_asm() { # run_asm [VAR=VAL…] -- <script arguments…>
	local -a vars=()
	while [ "$#" -gt 0 ]; do
		case "$1" in
		*=*) vars+=("$1") ; shift ;;
		*) break ;;
		esac
	done
	(
		cd "$TREE" &&
			env -i PATH="$TREE/bin:/usr/bin:/bin" HOME="$WORK" TMPDIR="$WORK" \
				"${vars[@]}" bash "$TREE/scripts/appliance-release-delivery.sh" "$@"
	) >"$WORK/out" 2>"$WORK/err"
}

echo "appliance-release-delivery — version, key and digest bindings (stubbed tail)"

run_asm
[ "$?" -eq 2 ]
check "no arguments is a refusal to run" "usage" $?
run_asm --version 0.0.0-dev --release-rpm "$WORK/olivares_26.10.0_linux_amd64.rpm" \
	--expected-sha256 "$RPM_SHA" --key-dir "$WORK/key" --out "$WORK/out-dir"
[ "$?" -eq 2 ] && grep -q "release's X.Y.Z" "$WORK/err"
check "the qualification version is refused on purpose" "release only" $?
run_asm --version v26.10.0 --release-rpm "$WORK/olivares_26.10.0_linux_amd64.rpm" \
	--expected-sha256 "$RPM_SHA" --key-dir "$WORK/key" --out "$WORK/out-dir2"
[ "$?" -eq 2 ]
check "a v-prefixed version is refused" "bare CalVer" $?
run_asm --version 26.10 --release-rpm "$WORK/olivares_26.10.0_linux_amd64.rpm" \
	--expected-sha256 "$RPM_SHA" --key-dir "$WORK/key" --out "$WORK/out-dir3"
[ "$?" -eq 2 ]
check "a two-part version is refused" "X.Y.Z" $?
run_asm --version 26.10.0 --release-rpm "$WORK/olivares_26.10.0_linux_amd64.rpm" \
	--expected-sha256 "nothex" --key-dir "$WORK/key" --out "$WORK/out-dir4"
[ "$?" -eq 2 ]
check "a non-hex digest is refused before anything runs" "grammar" $?

# A DIFFERENT key than the pinned one: the delivery would be a qualification, and publishing
# it as the release route is the silent downgrade this refuses.
mkdir -p "$WORK/key2"
jq -n '{openpgp_fingerprint:"FFFFFFFFFF0000000000000000000000000000FF", environment:"production"}' >"$WORK/key2/descriptor.json"
for f in openpgp-secret.asc passphrase olivares-packages.asc; do printf 'x\n' >"$WORK/key2/$f"; done
run_asm --version 26.10.0 --release-rpm "$WORK/olivares_26.10.0_linux_amd64.rpm" \
	--expected-sha256 "$RPM_SHA" --key-dir "$WORK/key2" --out "$WORK/out-dir5"
[ "$?" -eq 1 ] && grep -q 'not the pinned release key' "$WORK/err"
check "a key other than the pinned release key is refused" "no silent qualification" $?

mkdir -p "$WORK/key3"
jq -n --arg fp "$PIN" '{openpgp_fingerprint:$fp, environment:"preprod"}' >"$WORK/key3/descriptor.json"
for f in openpgp-secret.asc passphrase olivares-packages.asc; do printf 'x\n' >"$WORK/key3/$f"; done
run_asm --version 26.10.0 --release-rpm "$WORK/olivares_26.10.0_linux_amd64.rpm" \
	--expected-sha256 "$RPM_SHA" --key-dir "$WORK/key3" --out "$WORK/out-dir6"
[ "$?" -eq 1 ] && grep -q 'not the production environment' "$WORK/err"
check "a non-production key environment is refused" "production only" $?

printf 'other bytes\n' >"$WORK/olivares_26.10.0_linux_arm64.rpm"
run_asm --version 26.10.0 --release-rpm "$WORK/olivares_26.10.0_linux_arm64.rpm" \
	--expected-sha256 "$RPM_SHA" --key-dir "$WORK/key" --out "$WORK/out-dir7"
[ "$?" -eq 1 ] && grep -q 'must be named olivares_26.10.0_linux_amd64.rpm' "$WORK/err"
check "a release rpm of the wrong name is refused" "the arch it builds" $?

mkdir -p "$WORK/wrong"
cp "$WORK/olivares_26.10.0_linux_amd64.rpm" "$WORK/wrong/olivares_26.10.0_linux_amd64.rpm"
printf 'x' >>"$WORK/wrong/olivares_26.10.0_linux_amd64.rpm"
run_asm --version 26.10.0 --release-rpm "$WORK/wrong/olivares_26.10.0_linux_amd64.rpm" \
	--expected-sha256 "$RPM_SHA" --key-dir "$WORK/key" --out "$WORK/out-dir8"
[ "$?" -eq 1 ] && grep -q 'the bytes differ from what checksums.txt signed' "$WORK/err"
check "a release rpm whose digest is not the signed one is refused" "binding" $?

mkdir -p "$WORK/out-exists"
run_asm --version 26.10.0 --release-rpm "$WORK/olivares_26.10.0_linux_amd64.rpm" \
	--expected-sha256 "$RPM_SHA" --key-dir "$WORK/key" --out "$WORK/out-exists"
[ "$?" -eq 2 ] && grep -q 'must not exist' "$WORK/err"
check "an existing OUT_DIR is refused" "no republish in place" $?

# Every binding held: the assembly proceeds until the first real tool, and the STUB answers.
# Reaching the stub is the positive control that the gates above admit the legitimate input.
run_asm --version 26.10.0 --release-rpm "$WORK/olivares_26.10.0_linux_amd64.rpm" \
	--expected-sha256 "$RPM_SHA" --key-dir "$WORK/key" --out "$WORK/out-ok"
rc=$?
[ "$rc" -ne 0 ] && grep -q 'go stub: reached' "$WORK/err"
check "a legitimate input passes every gate and reaches the build tools" "positive control" $?
[ -d "$WORK/out-ok" ] || [ -d "$WORK/out-ok" ] 2>/dev/null
check "the delivery directory was created for the tool stage" "progress" $?

printf 'appliance-release-delivery battery: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
