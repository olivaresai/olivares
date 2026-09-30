#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/check-aur-olivares-bin.sh: the real package definition passes,
# and each mutant is refused for its own reason — a wrong tarball sha256 against the
# release checksums, a tampered local source, a stale .SRCINFO, an enabled service,
# an aarch64 claim and a missing conflict. Mutants that change metadata get a
# regenerated .SRCINFO, so each is refused by its target check and not by drift.
# Offline: the release checksums row is a fixture derived from the real PKGBUILD.
set -uo pipefail
LC_ALL=C
export LC_ALL

could_not_look() {
	printf 'test-aur-olivares-bin: NO HE PODIDO MIRAR — %s\n' "$*" >&2
	exit 2
}
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_root="${TMPDIR:-}"
[[ "$tmp_root" == /* && -d "$tmp_root" ]] || could_not_look 'TMPDIR must be an existing absolute directory'
for tool in awk bash cp grep mktemp rm sed; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool"
done
cleanup() {
	case "$scratch" in "$tmp_root"/aur-olivares-bin-test.*) rm -rf -- "$scratch" ;; *) ;; esac
}
scratch="$(mktemp -d "$tmp_root/aur-olivares-bin-test.XXXXXX")" || could_not_look 'cannot allocate scratch'
trap cleanup EXIT INT TERM
check="$root/scripts/check-aur-olivares-bin.sh"
real="$root/packaging/aur/olivares-bin"
# shellcheck source=/dev/null
source <(sed -n '/^print_srcinfo() {/,/^}/p' "$check")
declare -F print_srcinfo >/dev/null || could_not_look 'cannot load the .SRCINFO printer from the check'
source <(sed -n '/^release_tag() {/,/^}/p' "$check")
declare -F release_tag >/dev/null || could_not_look 'cannot load the era tag helper from the check'

pkgver="$(bash -c '. "$1"; printf %s "$pkgver"' _ "$real/PKGBUILD")"
tarball_sha="$(bash -c '. "$1"; printf %s "${sha256sums_x86_64[0]}"' _ "$real/PKGBUILD")"
printf '%s  olivares_%s_linux_amd64.tar.gz\n' "$tarball_sha" "$pkgver" >"$scratch/checksums.txt"
# The release's signature pair, as the release publishes it beside checksums.txt. The test
# verifier stands in for cosign: it accepts a .sig whose text is the SHA-256 of the file it
# signs and a .pem naming the identity the check passes, and nothing else.
sha256sum "$scratch/checksums.txt" | cut -d' ' -f1 >"$scratch/checksums.txt.sig"
printf 'https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/%s\n' "$(release_tag "$pkgver")" >"$scratch/checksums.txt.pem"
cat >"$scratch/verify-blob" <<'VERIFY'
#!/usr/bin/env bash
# verify-blob CHECKSUMS SIG PEM IDENTITY ISSUER
[[ "$(sha256sum "$1" | cut -d' ' -f1)" == "$(cat "$2")" && "$(cat "$3")" == "$4" &&
	"$5" == https://token.actions.githubusercontent.com ]]
VERIFY
chmod 0755 "$scratch/verify-blob"

passed=0 failed=0
# expect RC NEEDLE LABEL DIR [CHECKSUMS [VERIFIER]]: CHECKSUMS "-" omits --checksums;
# VERIFIER "none" runs without the test verifier (and without an asserted cosign).
expect() {
	local want="$1" needle="$2" label="$3" dir="$4" sums="${5:-$scratch/checksums.txt}" verifier="${6:-test}" rc
	local -a args=(--dir "$dir")
	[[ "$sums" == - ]] || args+=(--checksums "$sums")
	if [[ "$verifier" == test ]]; then
		env -u OLIVARES_COSIGN_BIN OLIVARES_AUR_TEST_ONLY=1 OLIVARES_AUR_TEST_VERIFY_BLOB="$scratch/verify-blob" \
			bash "$check" "${args[@]}" >"$scratch/out" 2>&1
	else
		env -u OLIVARES_COSIGN_BIN -u OLIVARES_AUR_TEST_ONLY -u OLIVARES_AUR_TEST_VERIFY_BLOB \
			bash "$check" "${args[@]}" >"$scratch/out" 2>&1
	fi
	rc=$?
	if [[ "$rc" -eq "$want" ]] && grep -F -q -- "$needle" "$scratch/out"; then
		passed=$((passed + 1)); printf 'ok - %s\n' "$label"
	else
		failed=$((failed + 1)); printf 'not ok - %s (rc=%d)\n' "$label" "$rc"
		sed 's/^/    /' "$scratch/out"
	fi
}
# mutant NAME SED [regenerate]: a copy of the package definition with SED applied to PKGBUILD.
mutant() {
	local dir="$scratch/$1"
	cp -r "$real" "$dir"
	sed -i "$2" "$dir/PKGBUILD"
	if [[ "${3:-}" == regenerate ]]; then print_srcinfo "$dir/PKGBUILD" >"$dir/.SRCINFO"; fi
	printf '%s\n' "$dir"
}

expect 0 'check-aur-olivares-bin: OK' 'the real package definition passes, sha256 bound to the release row' "$real"
expect 0 'check-aur-olivares-bin: OK — olivares-bin' 'the real package definition passes without release checksums' "$real" -
# A forged checksums.txt that matches a forged pin, under the release's real signature pair.
forged="$(printf '%s' "$tarball_sha" | tr '0-9a-f' 'a-f0-9')"
mkdir -p "$scratch/forged"
printf '%s  olivares_%s_linux_amd64.tar.gz\n' "$forged" "$pkgver" >"$scratch/forged/checksums.txt"
cp "$scratch/checksums.txt.sig" "$scratch/forged/checksums.txt.sig"
cp "$scratch/checksums.txt.pem" "$scratch/forged/checksums.txt.pem"
expect 1 'signature does not verify' 'a forged checksums.txt that matches a forged pin is refused' \
	"$(mutant forged-pin "s/$tarball_sha/$forged/" regenerate)" "$scratch/forged/checksums.txt"
mkdir -p "$scratch/unsigned"
cp "$scratch/checksums.txt" "$scratch/unsigned/checksums.txt"
expect 2 'checksums.txt.sig' 'checksums.txt without its signature pair cannot be verified' "$real" "$scratch/unsigned/checksums.txt"
expect 2 'OLIVARES_COSIGN_BIN' 'without an asserted cosign the checksums cannot be verified' "$real" "$scratch/checksums.txt" none
wrong_sha="$(printf '%s' "$tarball_sha" | tr '0-9a-f' '1-9a-f0')"
expect 1 "refused: PKGBUILD sha256 $wrong_sha" 'a PKGBUILD with a wrong tarball sha256 is refused' \
	"$(mutant wrong-sha "s/$tarball_sha/$wrong_sha/" regenerate)"
dir="$scratch/tampered-source"
cp -r "$real" "$dir"
printf '# tampered\n' >>"$dir/olivares-sysusers.conf"
expect 1 'sha256 of olivares-sysusers.conf' 'a tampered local source is refused' "$dir"
expect 1 '.SRCINFO differs from the PKGBUILD' 'a stale .SRCINFO is refused' \
	"$(mutant stale-srcinfo 's/^pkgrel=1$/pkgrel=2/')"
expect 1 'package() must not enable or start the service' 'a package() that enables the service is refused' \
	"$(mutant enable 's|^  install -Dm0755 "${srcdir}/olivares" "${pkgdir}/usr/bin/olivares"$|&\n  systemctl enable olivares|' regenerate)"
expect 1 'arch must be exactly x86_64' 'an aarch64 claim is refused' \
	"$(mutant aarch64 "s/^arch=('x86_64')$/arch=('x86_64' 'aarch64')/" regenerate)"
expect 1 'conflicts must name olivares' 'a missing conflict with olivares is refused' \
	"$(mutant no-conflict "s/^conflicts=('olivares')$/conflicts=()/" regenerate)"

# The check's own temporary files come from mktemp, never a name derived from its pid.
if grep -q 'check-aur-srcinfo\.\$\$' "$check" || ! grep -q 'mktemp' "$check"; then
	failed=$((failed + 1)); printf 'not ok - the check writes a predictable temporary file\n'
else
	passed=$((passed + 1)); printf 'ok - the check writes no predictable temporary file\n'
fi

printf 'test-aur-olivares-bin: %d/%d green\n' "$passed" "$((passed + failed))"
[[ "$failed" -eq 0 ]]
