#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# The Arch Linux package as a release member, without a product build:
#   K1  render-release-index.sh classifies olivares_<v>_linux_amd64.pkg.tar.zst as a package.
#   C1-C5  release-finalize-stable.sh's package recipe block (read from the script between its
#          markers and run alone): every format covers exactly its architectures; the Arch
#          package is amd64 only, and an arm64 or aarch64 Arch member is refused by name.
# Exit 0: all hold. Exit 1: a case failed. Exit 2: could not look.
set -uo pipefail
LC_ALL=C
export LC_ALL

could_not_look() {
	printf 'test-release-archlinux-members: NO HE PODIDO MIRAR — %s\n' "$*" >&2
	exit 2
}
root="$(unset CDPATH; cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
for tool in bash jq sha256sum awk sed grep mktemp; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
tmp_root="${TMPDIR:-}"
[[ "$tmp_root" == /* && -d "$tmp_root" ]] || could_not_look 'TMPDIR must be an existing absolute directory'
W="$(mktemp -d "$tmp_root/test-release-archlinux.XXXXXX")" || could_not_look 'cannot allocate scratch'
trap 'rm -rf -- "$W"' EXIT
passed=0 failed=0
ok() { passed=$((passed + 1)); printf 'ok - %s\n' "$1"; }
not_ok() { failed=$((failed + 1)); printf 'not ok - %s\n' "$1"; }

# --- K1: the release index kind -----------------------------------------------------------
VER=26.10.0
DIST="$W/dist"
mkdir -p "$DIST" "$W/tmp"
printf '0123456789abcdef0123456789abcdef01234567\n' >"$W/release-commit.txt"
names=("olivares_${VER}_linux_amd64.tar.gz" "olivares_${VER}_linux_amd64.deb" "olivares_${VER}_linux_amd64.pkg.tar.zst")
: >"$DIST/checksums.txt"
for name in "${names[@]}"; do
	printf 'fixture %s\n' "$name" >"$DIST/$name"
	printf '%s  %s\n' "$(sha256sum "$DIST/$name" | awk '{print $1}')" "$name" >>"$DIST/checksums.txt"
done
printf '%s  release-commit.txt\n' "$(sha256sum "$W/release-commit.txt" | awk '{print $1}')" >>"$DIST/checksums.txt"
digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
TMPDIR="$W/tmp" bash "$root/scripts/render-release-index.sh" --checksums "$DIST/checksums.txt" --artifact-dir "$DIST" \
	--commit-file "$W/release-commit.txt" --repository olivaresai/olivares --version "$VER" --channel stable \
	--state candidate --image "ghcr.io/olivaresai/olivares@sha256:$digest" --image "docker.io/olivaresai/olivares@sha256:$digest" \
	--surface github-release=staged --surface ota-stable=staged --surface homebrew=staged --surface ghcr=published \
	--surface docker-hub=staged --surface helm=not-published --out "$W/index.json" >"$W/render.out" 2>&1
kind="$(jq -r --arg n "olivares_${VER}_linux_amd64.pkg.tar.zst" '.artifacts[] | select(.name == $n) | .kind' "$W/index.json" 2>/dev/null)"
if [[ "$kind" == package ]]; then ok "K1 the Arch package is indexed as kind package"
else not_ok "K1 the Arch package is indexed as kind '${kind:-absent}' ($(tail -1 "$W/render.out"))"; fi

# --- C: the finalizer's package recipe ------------------------------------------------------
fin="$root/scripts/release-finalize-stable.sh"
awk '/^# --- package recipe: begin/{f=1} f{print} /^# --- package recipe: end/{f=0}' "$fin" >"$W/recipe.sh"
if ! grep -q '^package_member_findings()' "$W/recipe.sh"; then
	not_ok "C0 release-finalize-stable.sh has no package recipe block with package_member_findings"
	printf 'test-release-archlinux-members: %d/%d green\n' "$passed" "$((passed + failed))"
	exit 1
fi
# findings NAME...: the recipe's findings for a signed member list.
findings() {
	printf '%s\n' "$@" >"$W/names"
	bash -c '. "$1"; package_member_findings "$2"' _ "$W/recipe.sh" "$W/names"
}
V=26.10.0
base=(
	"olivares_${V}_linux_amd64.deb" "olivares_${V}_linux_arm64.deb"
	"olivares-${V}-1.x86_64.rpm" "olivares-${V}-1.aarch64.rpm"
	"olivares_${V}_x86_64.apk" "olivares_${V}_aarch64.apk"
)
out="$(findings "${base[@]}" "olivares_${V}_linux_amd64.pkg.tar.zst")"
if [[ -z "$out" ]]; then ok "C1 deb, rpm and apk for both architectures and Arch for amd64: no finding"
else not_ok "C1 a complete member set has findings: $out"; fi
for arm in "olivares_${V}_linux_arm64.pkg.tar.zst" "olivares-${V}-1-aarch64.pkg.tar.zst"; do
	out="$(findings "${base[@]}" "olivares_${V}_linux_amd64.pkg.tar.zst" "$arm")"
	if grep -Fq "$arm" <<<"$out" && grep -Fq 'amd64 only' <<<"$out"; then ok "C2 an Arch member for arm64 is refused by name: $arm"
	else not_ok "C2 an Arch member for arm64 ($arm) is not refused by name: '${out}'"; fi
done
out="$(findings "${base[@]}")"
if grep -Fxq 'no archlinux package for amd64 in the signed checksums' <<<"$out"; then ok "C3 a missing Arch package for amd64 is named"
else not_ok "C3 a missing Arch package for amd64 is not named: '$out'"; fi
out="$(findings "${base[@]}" "olivares_${V}_linux_amd64.pkg.tar.zst" "olivares-${V}-1-x86_64.pkg.tar.zst")"
if grep -Fq '2 archlinux packages match amd64' <<<"$out"; then ok "C4 two Arch packages for amd64 are ambiguous"
else not_ok "C4 two Arch packages for amd64 are not reported ambiguous: '$out'"; fi
out="$(findings "olivares_${V}_linux_amd64.deb" "olivares-${V}-1.x86_64.rpm" "olivares-${V}-1.aarch64.rpm" \
	"olivares_${V}_x86_64.apk" "olivares_${V}_aarch64.apk" "olivares_${V}_linux_amd64.pkg.tar.zst")"
if grep -Fxq 'no deb package for arm64 in the signed checksums' <<<"$out"; then ok "C5 deb still requires arm64"
else not_ok "C5 a missing arm64 deb is not named: '$out'"; fi

printf 'test-release-archlinux-members: %d/%d green\n' "$passed" "$((passed + failed))"
[[ "$failed" -eq 0 ]]
