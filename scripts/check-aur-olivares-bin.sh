#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-aur-olivares-bin.sh [--dir DIR] [--checksums FILE] [--makepkg]
#
# Contract of the AUR olivares-bin package definition in packaging/aur/olivares-bin.
#   - Policy: x86_64 only; provides olivares=<pkgver> and conflicts with olivares; no
#     replaces and no install scriptlet; the tarball source is the published release
#     URL for pkgver; package() installs the binary, the rendered unit, sysusers.d,
#     tmpfiles.d and the licence texts, and never enables or starts the service.
#   - Every local source matches its sha256sums entry.
#   - .SRCINFO equals what makepkg 7.1.0 --printsrcinfo writes for this PKGBUILD
#     (write_srcinfo_content in scripts/libmakepkg/srcinfo.sh.in), emulated here.
#   - --checksums FILE: FILE.sig and FILE.pem, the release's cosign signature pair, verify
#     FILE for the identity .github/workflows/release.yml@refs/tags/<pkgver> (through
#     scripts/cosign-verified.sh, which needs the asserted OLIVARES_COSIGN_BIN); then the
#     tarball sha256 equals FILE's row. Without the pair or cosign it cannot look (exit 2).
#   - --makepkg: makepkg itself prints the .SRCINFO and verifies the sources
#     (downloads the tarball). Needs an Arch Linux host and a non-root user.
# Exit 0 when every check holds, 1 on a finding, 2 when it could not look.
set -euo pipefail

# A release version is its bare tag.
release_tag() {
	printf '%s' "${1:?}"
}
LC_ALL=C
export LC_ALL

could_not_look() {
	printf 'check-aur-olivares-bin: COULD NOT CHECK — %s\n' "$*" >&2
	exit 2
}
finding() {
	printf 'FINDING — %s\n' "$*" >&2
	exit 1
}
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
dir="$root/packaging/aur/olivares-bin"
checksums=""
use_makepkg=0
while [[ "$#" -gt 0 ]]; do
	case "$1" in
	--dir) dir="${2:-}"; shift 2 ;;
	--checksums) checksums="${2:-}"; shift 2 ;;
	--makepkg) use_makepkg=1; shift ;;
	*) could_not_look "unknown argument: $1" ;;
	esac
done
for tool in awk bash cmp grep sha256sum; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
for file in PKGBUILD .SRCINFO olivares-sysusers.conf olivares-tmpfiles.conf; do
	[[ -f "$dir/$file" && ! -L "$dir/$file" ]] || finding "missing $file in $dir"
done

# print_srcinfo PKGBUILD: makepkg's write_srcinfo_content for a single-package
# PKGBUILD whose package() overrides nothing.
print_srcinfo() {
	bash -c '
		set -eu
		# shellcheck source=/dev/null
		. "$1"
		out() { local name="$1" value; shift; for value in "$@"; do printf "\t%s = %s\n" "$name" "$value"; done; }
		sums=(cksums md5sums sha1sums sha224sums sha256sums sha384sums sha512sums b2sums)
		printf "pkgbase = %s\n" "${pkgbase:-$pkgname}"
		for attr in pkgdesc pkgver pkgrel epoch url install changelog; do
			if declare -p "$attr" >/dev/null 2>&1; then out "$attr" "${!attr}"; fi
		done
		for attr in arch groups license checkdepends makedepends depends optdepends provides \
			conflicts replaces noextract options backup source validpgpkeys "${sums[@]}"; do
			if declare -p "$attr" >/dev/null 2>&1; then eval "out \"\$attr\" \"\${$attr[@]}\""; fi
		done
		for machine in "${arch[@]}"; do
			[[ "$machine" == any ]] && continue
			for attr in source provides conflicts depends replaces optdepends makedepends checkdepends "${sums[@]}"; do
				attr="${attr}_$machine"
				if declare -p "$attr" >/dev/null 2>&1; then eval "out \"\$attr\" \"\${$attr[@]}\""; fi
			done
		done
		printf "\npkgname = %s\n" "$pkgname"
	' _ "$1"
}

# Read the PKGBUILD's values in a child bash; PKGBUILD is bash by definition.
values="$(bash -c '
	set -eu
	# shellcheck source=/dev/null
	. "$1"
	printf "pkgname=%s\n" "$pkgname"
	printf "pkgver=%s\n" "$pkgver"
	printf "arch=%s\n" "${arch[*]}"
	printf "provides=%s\n" "${provides[*]:-}"
	printf "conflicts=%s\n" "${conflicts[*]:-}"
	printf "replaces=%s\n" "$(declare -p replaces >/dev/null 2>&1 && echo set || echo unset)"
	printf "install=%s\n" "$(declare -p install >/dev/null 2>&1 && echo set || echo unset)"
	printf "source_count=%s\n" "${#source[@]}"
	printf "sums_count=%s\n" "${#sha256sums[@]}"
	for i in "${!source[@]}"; do printf "source=%s %s\n" "${sha256sums[$i]}" "${source[$i]}"; done
	printf "tarball_count=%s\n" "${#source_x86_64[@]}"
	printf "tarball_sums_count=%s\n" "${#sha256sums_x86_64[@]}"
	printf "tarball=%s\n" "${source_x86_64[0]}"
	printf "tarball_sha256=%s\n" "${sha256sums_x86_64[0]}"
	printf "package_body<<\n"
	declare -f package
' _ "$dir/PKGBUILD")" || finding 'PKGBUILD does not source cleanly in bash'
value() { awk -F= -v key="$1" '$1 == key {sub(/^[^=]*=/, ""); print; exit}' <<<"$values"; }
package_body="$(sed -n '/^package_body<<$/,$p' <<<"$values" | sed '1d')"

pkgver="$(value pkgver)"
[[ "$(value pkgname)" == olivares-bin ]] || finding 'pkgname must be olivares-bin'
[[ "$pkgver" =~ ^[0-9]+\.[0-9]+$ ]] || finding "pkgver is not a release version: $pkgver"
[[ "$(value arch)" == x86_64 ]] || finding 'arch must be exactly x86_64: aarch64 is not a published target'
[[ " $(value provides) " == *" olivares=$pkgver "* ]] || finding "provides must name olivares=$pkgver"
[[ " $(value conflicts) " == *" olivares "* ]] || finding 'conflicts must name olivares'
[[ "$(value replaces)" == unset ]] || finding 'replaces is for renames only (AUR submission guidelines)'
[[ "$(value install)" == unset ]] || finding 'no install scriptlet: sysusers.d and tmpfiles.d create the account and data dir'
[[ "$(value tarball_count)" == 1 && "$(value tarball_sums_count)" == 1 ]] || finding 'exactly one x86_64 source and sum'
published="https://github.com/olivaresai/olivares/releases/download/$(release_tag "${pkgver}")/olivares_${pkgver}_linux_amd64.tar.gz"
[[ "$(value tarball)" == "$published" ]] || finding "x86_64 source is not the published release URL for $pkgver"
tarball_sha="$(value tarball_sha256)"
[[ "$tarball_sha" =~ ^[0-9a-f]{64}$ ]] || finding 'x86_64 sha256 is not a lowercase SHA-256'
[[ "$(value source_count)" == "$(value sums_count)" ]] || finding 'source and sha256sums differ in length'
while read -r want name; do
	[[ "$name" != */* && -f "$dir/$name" ]] || finding "local source is not a file beside the PKGBUILD: $name"
	got="$(sha256sum "$dir/$name" | awk '{print $1}')"
	[[ "$got" == "$want" ]] || finding "sha256 of $name is $got, PKGBUILD says $want"
done < <(awk '/^source=/{sub(/^source=/, ""); print}' <<<"$values")

if grep -Eq '\b(systemctl|enable|start)\b' <<<"$package_body"; then
	finding 'package() must not enable or start the service'
fi
for needle in '/usr/bin/olivares' '/usr/lib/systemd/system/olivares.service' '/usr/lib/sysusers.d/olivares.conf' \
	'/usr/lib/tmpfiles.d/olivares.conf' 'LICENSE NOTICE LICENSING.md DISCLAIMER.md' '/LICENSES' \
	'packaging/service/systemd.service' "grep -q '@[A-Z_]*@'"; do
	grep -Fq -- "$needle" <<<"$package_body" || finding "package() does not carry: $needle"
done

printed="$(mktemp "${TMPDIR:-/tmp}/check-aur-srcinfo.XXXXXX")" || could_not_look 'cannot allocate a temporary file'
print_srcinfo "$dir/PKGBUILD" >"$printed" || { rm -f -- "$printed"; finding 'cannot print .SRCINFO'; }
if ! cmp -s "$printed" "$dir/.SRCINFO"; then
	diff -u "$dir/.SRCINFO" "$printed" >&2 || true
	rm -f -- "$printed"
	finding '.SRCINFO differs from the PKGBUILD (makepkg --printsrcinfo > .SRCINFO)'
fi
rm -f -- "$printed"

# verify_checksums FILE: FILE's cosign signature pair for this release's identity. The
# test verifier is honored only under the explicit test-only latch.
verify_checksums() {
	local sums=$1
	# The signing identity names the exact release tag.
	local identity="https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/$(release_tag "${pkgver}")"
	local issuer=https://token.actions.githubusercontent.com
	[[ -f "$sums.sig" && -f "$sums.pem" ]] ||
		could_not_look "cannot verify $sums: its signature pair $sums.sig and $sums.pem is absent"
	if [[ "${OLIVARES_AUR_TEST_ONLY:-}" == 1 && -n "${OLIVARES_AUR_TEST_VERIFY_BLOB:-}" ]]; then
		"$OLIVARES_AUR_TEST_VERIFY_BLOB" "$sums" "$sums.sig" "$sums.pem" "$identity" "$issuer" ||
			finding "the checksums.txt signature does not verify for $identity"
		return 0
	fi
	[[ "${OLIVARES_COSIGN_BIN:-}" == /* && -x "${OLIVARES_COSIGN_BIN:-}" ]] ||
		could_not_look "cannot verify $sums: no asserted cosign (OLIVARES_COSIGN_BIN, from scripts/assert-cosign-binary.sh)"
	bash "$root/scripts/cosign-verified.sh" verify-blob --certificate "$sums.pem" --signature "$sums.sig" \
		--certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$sums" >/dev/null 2>&1 ||
		finding "the checksums.txt signature does not verify for $identity"
}

if [[ -n "$checksums" ]]; then
	[[ -f "$checksums" ]] || could_not_look "checksums file is absent: $checksums"
	verify_checksums "$checksums"
	asset="olivares_${pkgver}_linux_amd64.tar.gz"
	rows="$(awk -v name="$asset" '$2 == name {print $1}' "$checksums")"
	[[ -n "$rows" ]] || finding "the release checksums do not list $asset"
	[[ "$(wc -l <<<"$rows")" -eq 1 ]] || finding "the release checksums list $asset more than once"
	[[ "$rows" == "$tarball_sha" ]] || finding "refused: PKGBUILD sha256 $tarball_sha for $asset differs from the release checksums ($rows)"
fi

verdict=""
if [[ -n "$checksums" ]]; then verdict="$verdict, tarball sha256 bound to the signed release checksums"; fi
if [[ "$use_makepkg" -eq 1 ]]; then
	command -v makepkg >/dev/null 2>&1 || could_not_look 'makepkg is absent (run on Arch Linux)'
	[[ "$(id -u)" -ne 0 ]] || could_not_look 'makepkg refuses to run as root'
	work="$(mktemp -d "${TMPDIR:-/tmp}/check-aur-makepkg.XXXXXX")" || could_not_look 'cannot allocate scratch'
	cp -- "$dir/PKGBUILD" "$dir/.SRCINFO" "$dir/olivares-sysusers.conf" "$dir/olivares-tmpfiles.conf" "$work/"
	(cd "$work" && makepkg --printsrcinfo) | cmp -s - "$dir/.SRCINFO" || finding 'makepkg --printsrcinfo differs from .SRCINFO'
	(cd "$work" && makepkg --verifysource --noconfirm >/dev/null 2>&1) || finding 'makepkg --verifysource refused the sources'
	verdict="$verdict, makepkg agrees and verified the sources"
fi
printf 'check-aur-olivares-bin: OK — olivares-bin %s%s\n' "$pkgver" "$verdict"
