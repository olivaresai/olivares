#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-ensure-bsdtar.sh — finite offline battery for scripts/ensure-bsdtar.sh.
#
# WHY IT PATCHES A PRIVATE COPY. The helper's value is its pin block: a fixed table of Ubuntu
# snapshot artifacts and the digests of the binary and libraries they must yield. A test that
# could reach those pins would either need the network or would have to teach the PRODUCTION
# script to accept test material — a trust door in the shipped tool, which is worse than the
# defect it closes. So this battery copies the helper, asserts the pin block is delimited by
# exactly one sentinel pair, replaces everything between the markers with fixture pins, and
# then asserts the production digests are GONE from the copy. The production script keeps no
# test hook at all, and this file never reads OLIVARES_BSDTAR or any other environment pin.
#
# The fixtures are real: real .deb built with dpkg-deb, a real ELF (a copy of the running
# bash, whose single non-glibc dependency is libtinfo) and a real loader resolution. The
# stubs are only the two ports that must not act for real — `curl`, which would leave the
# box, and `dpkg-deb`, which is watched so it is only ever asked for control fields and `-x`.
#
# exit 0  every case held
# exit 1  a case failed — the helper's behaviour changed
# exit 2  could not look: a tool this battery needs is absent, or the host is not the amd64
#         layout the helper pins. NEVER reported as a pass.
set -euo pipefail
LC_ALL=C
export LC_ALL

me=test-ensure-bsdtar
root_dir="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
helper="$root_dir/scripts/ensure-bsdtar.sh"

could_not_look() {
	printf '%s: NO HE PODIDO MIRAR — %s\n' "$me" "$*" >&2
	exit 2
}
pass=0
fail=0
ok() {
	pass=$((pass + 1))
	printf 'ok - %s\n' "$*"
}
bad() {
	fail=$((fail + 1))
	printf 'not ok - %s\n' "$*"
}

[[ -f "$helper" ]] || could_not_look "the helper under test is absent: $helper"
for tool in dpkg-deb sha256sum ldd mktemp sed grep cp mv stat getconf uname head truncate cut find; do
	command -v "$tool" >/dev/null 2>&1 || could_not_look "missing $tool, needed by this battery"
done
# The helper pins the amd64 multiarch layout; on any other host this battery would be
# measuring something the helper does not claim to support.
host_arch="$(uname -m)"
[[ "$host_arch" == x86_64 ]] ||
	could_not_look "the helper pins the x86_64 layout and this host is $host_arch"
host_os="$(uname -s)"
[[ "$host_os" == Linux ]] || could_not_look "this battery needs Linux; this host is $host_os"
host_libc="$(getconf GNU_LIBC_VERSION 2>/dev/null || true)"
[[ "$host_libc" == 'glibc '* ]] || could_not_look "this battery needs glibc; got '${host_libc:-nothing}'"
host_glibc="${host_libc#glibc }"
triplet=x86_64-linux-gnu
libtinfo_host="$(ldd "$BASH" 2>/dev/null | sed -n 's/^[[:space:]]*libtinfo\.so\.6 => \([^ ]*\).*/\1/p' | head -n 1)"
[[ -n "$libtinfo_host" && -f "$libtinfo_host" ]] ||
	could_not_look "could not find the libtinfo this bash links against; the ELF fixture needs it"

real_dpkg_deb="$(command -v dpkg-deb)"

scratch_parent="${TMPDIR:-/tmp}"
case "$scratch_parent" in
/*) ;;
*) could_not_look "TMPDIR must be absolute, got '$scratch_parent'" ;;
esac
[[ -d "$scratch_parent" ]] || could_not_look "TMPDIR is absent: $scratch_parent"
# The scratch is checked EXPLICITLY, before the trap is installed and before ANY subpath is
# derived from it. This is not defensive decoration: a guard swallowed into an `if` condition
# list does not fire errexit, so a failing mktemp would leave $scratch empty and the very next
# assignment would point at /fixtures — outside every directory this battery may touch. The
# four checks are «it ran», «it said something», «it is the path WE asked for» and «it is a
# real directory», in that order.
scratch="$(mktemp -d "$scratch_parent/test-ensure-bsdtar.XXXXXX" 2>/dev/null)" ||
	could_not_look "mktemp could not create a scratch directory under $scratch_parent"
[[ -n "$scratch" ]] ||
	could_not_look "mktemp exited 0 but produced no scratch path under $scratch_parent"
case "$scratch" in
"$scratch_parent"/test-ensure-bsdtar.??????) ;;
*) could_not_look "mktemp produced a path this battery does not own: '$scratch'" ;;
esac
[[ -d "$scratch" && ! -L "$scratch" ]] ||
	could_not_look "the scratch path is not a plain directory: $scratch"
trap 'rm -rf -- "$scratch"' EXIT

# --- the sentinel contract -----------------------------------------------------
opens="$(grep -c '^# >>> PINS$' "$helper" || true)"
closes="$(grep -c '^# <<< PINS$' "$helper" || true)"
if [[ "$opens" == 1 && "$closes" == 1 ]]; then
	ok "the helper carries exactly one pin sentinel pair"
else
	bad "the helper must carry exactly one pin sentinel pair, found $opens open and $closes close"
	printf '%s: without the sentinel this battery cannot isolate its fixtures; stopping\n' "$me" >&2
	exit 1
fi
prod_bin_sha="$(sed -n 's/^BIN_SHA256=//p' "$helper" | head -n 1)"
[[ "$prod_bin_sha" =~ ^[0-9a-f]{64}$ ]] ||
	bad "the helper's production BIN_SHA256 is not a sha256: '${prod_bin_sha:-nothing}'"

# mkpins FILE — writes a fixture pin block from the P_* variables. Each case sets only what
# it bends, so no case has to patch a row whose own separator is a pipe.
mkpins() {
	cat >"$1" <<PINS
SNAPSHOT=FIXTURESNAPSHOT
BASE_URL="https://fixture.invalid/ubuntu/\$SNAPSHOT"
OS_SUPPORTED=$P_OS
ARCH_SUPPORTED=$P_ARCH
ABI_MIN_GLIBC=$P_ABI
LIB_RELDIRS=(usr/lib/$triplet lib/$triplet)
BIN_RELPATH=usr/bin/bsdtar
BIN_SHA256=$P_BINSHA
EXPECT_VERSION_RE='$P_VERRE'
ARTIFACTS='
$P_ARTIFACTS
'
LIBS='
$P_LIBS
'
PINS
}

# make_helper NAME PINSFILE — a private copy of the helper whose pin block is PINSFILE.
# The production block is spliced out at its sentinels and never reaches a fixture run.
make_helper() {
	local name=$1 pins=$2
	local out="$scratch/helpers/$name.sh"
	mkdir -p "$scratch/helpers"
	{
		sed '/^# >>> PINS$/,$d' "$helper"
		printf '# >>> PINS\n'
		cat "$pins"
		printf '# <<< PINS\n'
		sed '1,/^# <<< PINS$/d' "$helper"
	} >"$out"
	# The whole point of the sentinel: no production digest may survive into the copy.
	if grep -q "$prod_bin_sha" "$out"; then
		bad "$name still carries the production binary digest after the pin swap"
	fi
	bash -n "$out" || could_not_look "the patched copy $name is not valid shell"
	printf '%s\n' "$out"
}

# --- fixture packages ----------------------------------------------------------
# build_deb DIR PACKAGE VERSION ARCH -> path of the .deb. No maintainer scripts, ever.
build_deb() {
	local stage=$1 pkg=$2 ver=$3 arch=$4
	mkdir -p "$stage/DEBIAN"
	{
		printf 'Package: %s\n' "$pkg"
		printf 'Version: %s\n' "$ver"
		printf 'Architecture: %s\n' "$arch"
		printf 'Maintainer: Olivares fixture <fixture@example.invalid>\n'
		printf 'Description: offline fixture for test-ensure-bsdtar\n'
	} >"$stage/DEBIAN/control"
	"$real_dpkg_deb" --build --root-owner-group "$stage" "$stage.deb" >/dev/null 2>&1 ||
		could_not_look "dpkg-deb could not build the fixture package $pkg"
	printf '%s\n' "$stage.deb"
}

fx="$scratch/fixtures"
mkdir -p "$fx"
# Package one: the executable. A real ELF so --version really runs and ldd really resolves.
mkdir -p "$fx/tool/usr/bin"
cp -- "$BASH" "$fx/tool/usr/bin/bsdtar"
chmod 0755 "$fx/tool/usr/bin/bsdtar"
deb_tool="$(build_deb "$fx/tool" fixture-tool 1.0-1 amd64)"
# Package two: the one non-glibc library that executable needs.
mkdir -p "$fx/lib/usr/lib/$triplet"
cp -- "$libtinfo_host" "$fx/lib/usr/lib/$triplet/libtinfo.so.6"
chmod 0644 "$fx/lib/usr/lib/$triplet/libtinfo.so.6"
deb_lib="$(build_deb "$fx/lib" fixture-lib 2.0-1 amd64)"

fx_bin_sha="$(sha256sum "$fx/tool/usr/bin/bsdtar" | cut -d' ' -f1)"
fx_libtinfo_sha="$(sha256sum "$fx/lib/usr/lib/$triplet/libtinfo.so.6" | cut -d' ' -f1)"
fx_version_line="$("$fx/tool/usr/bin/bsdtar" --version 2>/dev/null | head -n 1 || true)"
[[ -n "$fx_version_line" ]] || could_not_look "the ELF fixture does not answer --version on this host"
# Anchor on the literal first line, escaped, so the case measures the helper's check and not
# a regex this battery invented.
fx_version_re="^$(printf '%s' "$fx_version_line" | sed 's/[][\.^$*+?(){}|\/]/\\&/g')$"

pool="$scratch/artifacts"
mkdir -p "$pool"
cp -- "$deb_tool" "$pool/fixture-tool_1.0-1_amd64.deb"
cp -- "$deb_lib" "$pool/fixture-lib_2.0-1_amd64.deb"
tool_sha="$(sha256sum "$pool/fixture-tool_1.0-1_amd64.deb" | cut -d' ' -f1)"
tool_size="$(stat -c '%s' "$pool/fixture-tool_1.0-1_amd64.deb")"
lib_sha="$(sha256sum "$pool/fixture-lib_2.0-1_amd64.deb" | cut -d' ' -f1)"
lib_size="$(stat -c '%s' "$pool/fixture-lib_2.0-1_amd64.deb")"

# Defaults every case starts from; a case overrides only the pin it is about.
P_OS=Linux
P_ARCH=x86_64
P_ABI=$host_glibc
P_BINSHA=$fx_bin_sha
P_VERRE=$fx_version_re
P_ARTIFACTS="fixture-tool|1.0-1|amd64|pool/main/f/fixture/fixture-tool_1.0-1_amd64.deb|$tool_size|$tool_sha
fixture-lib|2.0-1|amd64|pool/main/f/fixture/fixture-lib_2.0-1_amd64.deb|$lib_size|$lib_sha"
P_LIBS="libtinfo.so.6|$fx_libtinfo_sha"
D_OS=$P_OS D_ARCH=$P_ARCH D_ABI=$P_ABI D_BINSHA=$P_BINSHA D_VERRE=$P_VERRE
D_ARTIFACTS=$P_ARTIFACTS D_LIBS=$P_LIBS
reset_pins() {
	P_OS=$D_OS P_ARCH=$D_ARCH P_ABI=$D_ABI P_BINSHA=$D_BINSHA P_VERRE=$D_VERRE
	P_ARTIFACTS=$D_ARTIFACTS P_LIBS=$D_LIBS
}
# newhelper NAME — build the pin file from the current P_* and splice a private copy.
newhelper() {
	mkdir -p "$scratch/pins"
	mkpins "$scratch/pins/$1.sh"
	make_helper "$1" "$scratch/pins/$1.sh"
}

# --- ports ---------------------------------------------------------------------
# curl stub: serves ONLY from the fixture pool, records argv, and writes nowhere else.
stub="$scratch/stub"
mkdir -p "$stub"
cat >"$stub/curl" <<STUBEOF
#!/usr/bin/env bash
set -uo pipefail
printf '%s\n' "\$*" >>"$scratch/curl.argv"
out=; url=
while [[ \$# -gt 0 ]]; do
	case "\$1" in
	-o) out=\$2; shift 2 ;;
	https://*) url=\$1; shift ;;
	*) shift ;;
	esac
done
[[ -n "\$out" && -n "\$url" ]] || exit 90
case "\$out" in "$scratch"/*) ;; *) printf 'curl stub refused to write outside the scratch: %s\n' "\$out" >&2; exit 91 ;; esac
src="$pool/\${url##*/}"
[[ -f "\$src" ]] || exit 22
cp -- "\$src" "\$out"
STUBEOF
chmod +x "$stub/curl"
# dpkg-deb watchdog: records argv, allows exactly `--field` and `-x`, refuses anything that
# would install, run a maintainer script or touch a path outside the scratch.
cat >"$stub/dpkg-deb" <<STUBEOF
#!/usr/bin/env bash
set -uo pipefail
printf '%s\n' "\$*" >>"$scratch/dpkg-deb.argv"
for a in "\$@"; do
	case "\$a" in
	-i | --install | --unpack | --control | --fsys-tarfile | --raw-extract | -X)
		printf 'dpkg-deb stub refused a forbidden operation: %s\n' "\$a" >&2
		exit 92
		;;
	/*)
		case "\$a" in
		"$scratch"/*) ;;
		*)
			printf 'dpkg-deb stub refused a path outside the scratch: %s\n' "\$a" >&2
			exit 93
			;;
		esac
		;;
	esac
done
case "\${1:-}" in
--field | -x) ;;
*)
	printf 'dpkg-deb stub refused an unexpected mode: %s\n' "\${1:-<none>}" >&2
	exit 94
	;;
esac
exec "$real_dpkg_deb" "\$@"
STUBEOF
chmod +x "$stub/dpkg-deb"
# canary: any network at all is fatal, and it records that it was reached.
canary="$scratch/canary"
mkdir -p "$canary"
cat >"$canary/curl" <<STUBEOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$scratch/canary.argv"
printf 'network canary: curl must not be invoked in this mode\n' >&2
exit 97
STUBEOF
chmod +x "$canary/curl"
cp -- "$stub/dpkg-deb" "$canary/dpkg-deb"

# run_helper LABEL PORTDIR HELPER ARGS... — records rc, stdout and stderr separately.
run_helper() {
	local label=$1 portdir=$2
	shift 2
	local o="$scratch/out/$label.stdout" e="$scratch/out/$label.stderr"
	mkdir -p "$scratch/out"
	set +e
	env -u OLIVARES_BSDTAR -u OLIVARES_BSDTAR_LIB -u OLIVARES_GORELEASER -u LD_LIBRARY_PATH \
		PATH="$portdir:$PATH" TMPDIR="$scratch" bash "$@" >"$o" 2>"$e"
	rc=$?
	set -e
	rc_out="$(cat "$o")"
	rc_err="$(cat "$e")"
}
# no_residue DEST — nothing but the published root may remain under a destination.
no_residue() {
	local dest=$1
	[[ -d "$dest" ]] || return 0
	local leftover
	leftover="$(find "$dest" -maxdepth 1 -name '.ensure-bsdtar.*' -print -quit)"
	[[ -z "$leftover" ]]
}

# --- 1. the whole path, with the network stub ----------------------------------
h_ok="$(newhelper good)"
d1="$scratch/dest1"
run_helper c1 "$stub" "$h_ok" --dest "$d1"
if [[ "$rc" == 0 && "$rc_out" == "$d1/root" && -z "$rc_err" ]]; then
	ok "correct artifacts: rc 0 and exactly the published root on stdout, stderr silent"
else
	bad "correct artifacts: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi
if [[ -n "$rc_out" && "$(printf '%s\n' "$rc_out" | grep -c '')" == 1 ]]; then
	ok "stdout is a single non-empty line"
else
	bad "stdout was not a single non-empty line: '$rc_out'"
fi
if [[ -x "$d1/root/usr/bin/bsdtar" ]]; then
	ok "the published root holds the executable"
else
	bad "no executable under the published root"
fi
if no_residue "$d1"; then
	ok "no staging directory survived a successful run"
else
	bad "a .ensure-bsdtar.* staging directory survived under $d1"
fi
# Publication happens under the destination and nowhere else.
if [[ "$(find "$d1" -maxdepth 1 -mindepth 1 | wc -l)" == 1 ]]; then
	ok "the destination holds exactly the root"
else
	bad "the destination holds more than the root: $(find "$d1" -maxdepth 1 -mindepth 1)"
fi
# Every download was HTTPS and bounded.
if grep -q 'https://fixture.invalid/' "$scratch/curl.argv" &&
	grep -q -- "--proto =https" "$scratch/curl.argv" &&
	grep -q -- "--max-time" "$scratch/curl.argv" &&
	grep -q -- "--retry" "$scratch/curl.argv"; then
	ok "the fetch port was called over HTTPS with bounds and retries"
else
	bad "the fetch port was not called with HTTPS, bounds and retries: $(cat "$scratch/curl.argv")"
fi
# The package port was asked for control fields and extraction, and nothing else.
if grep -qE '^--field .* (Package|Version|Architecture)$' "$scratch/dpkg-deb.argv" &&
	grep -qE '^-x ' "$scratch/dpkg-deb.argv" &&
	! grep -qE '(^| )(-i|--install|--unpack|--control|--fsys-tarfile)( |$)' "$scratch/dpkg-deb.argv"; then
	ok "the package port saw only field queries and -x, never an install"
else
	bad "the package port saw an unexpected call: $(cat "$scratch/dpkg-deb.argv")"
fi

# --- 2. republication is a revalidation, not a reuse ---------------------------
: >"$scratch/canary.argv"
run_helper c2 "$canary" "$h_ok" --dest "$d1"
if [[ "$rc" == 0 && "$rc_out" == "$d1/root" && ! -s "$scratch/canary.argv" ]]; then
	ok "an already published root is revalidated offline and reused"
else
	bad "revalidation: rc=$rc stdout='$rc_out' canary='$(cat "$scratch/canary.argv")'"
fi
# ...and a divergent one is refused without being deleted.
printf 'tampered\n' >>"$d1/root/usr/bin/bsdtar"
run_helper c3 "$canary" "$h_ok" --dest "$d1"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"does not satisfy the pinned manifest"* && -e "$d1/root/usr/bin/bsdtar" ]]; then
	ok "a divergent published root is refused with rc 2 and left in place"
else
	bad "divergent root: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi

# --- 3. offline mode, with the network as a fatal canary -----------------------
: >"$scratch/canary.argv"
d4="$scratch/dest4"
run_helper c4 "$canary" "$h_ok" --dest "$d4" --artifact-dir "$pool"
if [[ "$rc" == 0 && "$rc_out" == "$d4/root" && ! -s "$scratch/canary.argv" ]]; then
	ok "--artifact-dir publishes without ever reaching the network"
else
	bad "--artifact-dir: rc=$rc stdout='$rc_out' canary='$(cat "$scratch/canary.argv")' stderr='$rc_err'"
fi

# --- 4. an altered byte stops before extraction --------------------------------
bad_pool="$scratch/artifacts-bad"
cp -r -- "$pool" "$bad_pool"
# Same length, different content: this case is about the DIGEST, so it must not be caught by
# the cheaper size check standing in front of it.
bad_deb="$bad_pool/fixture-tool_1.0-1_amd64.deb"
bad_size="$(stat -c '%s' "$bad_deb")"
truncate -s $((bad_size - 1)) "$bad_deb"
printf 'Z' >>"$bad_deb"
if [[ "$(sha256sum "$bad_deb" | cut -d' ' -f1)" == "$tool_sha" ]]; then
	truncate -s $((bad_size - 1)) "$bad_deb"
	printf 'Y' >>"$bad_deb"
fi
[[ "$(stat -c '%s' "$bad_deb")" == "$tool_size" ]] ||
	could_not_look "the altered fixture changed size; this case could not measure the digest check"
d5="$scratch/dest5"
# The package port is watched from here on, so «before extraction» is a measurement and not
# a claim: the run must add no -x call at all.
before_x="$(grep -cE '^-x ' "$scratch/dpkg-deb.argv" || true)"
run_helper c5 "$canary" "$h_ok" --dest "$d5" --artifact-dir "$bad_pool"
after_x="$(grep -cE '^-x ' "$scratch/dpkg-deb.argv" || true)"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"does not match its pinned sha256"* && ! -e "$d5/root" ]]; then
	ok "an altered artifact is refused with rc 2 and nothing is published"
else
	bad "altered artifact: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi
if [[ "$before_x" == "$after_x" ]]; then
	ok "the altered artifact was never handed to extraction"
else
	bad "extraction ran on an artifact that failed its digest ($before_x -> $after_x)"
fi

# ...and a short artifact is caught by the size check, before its digest is even computed.
short_pool="$scratch/artifacts-short"
cp -r -- "$pool" "$short_pool"
truncate -s $((tool_size - 16)) "$short_pool/fixture-tool_1.0-1_amd64.deb"
d5b="$scratch/dest5b"
run_helper c5b "$canary" "$h_ok" --dest "$d5b" --artifact-dir "$short_pool"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"pinned $tool_size"* && ! -e "$d5b/root" ]]; then
	ok "a short artifact is refused by the size check"
else
	bad "short artifact: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi

# --- 5. a correct digest is not a correct package ------------------------------
mkdir -p "$fx/wrong/usr/bin"
cp -- "$BASH" "$fx/wrong/usr/bin/bsdtar"
deb_wrong="$(build_deb "$fx/wrong" fixture-tool 9.9-9 amd64)"
wrong_pool="$scratch/artifacts-wrongfields"
mkdir -p "$wrong_pool"
cp -- "$deb_wrong" "$wrong_pool/fixture-tool_1.0-1_amd64.deb"
cp -- "$pool/fixture-lib_2.0-1_amd64.deb" "$wrong_pool/"
wrong_sha="$(sha256sum "$wrong_pool/fixture-tool_1.0-1_amd64.deb" | cut -d' ' -f1)"
wrong_size="$(stat -c '%s' "$wrong_pool/fixture-tool_1.0-1_amd64.deb")"
reset_pins
P_ARTIFACTS="fixture-tool|1.0-1|amd64|pool/main/f/fixture/fixture-tool_1.0-1_amd64.deb|$wrong_size|$wrong_sha
fixture-lib|2.0-1|amd64|pool/main/f/fixture/fixture-lib_2.0-1_amd64.deb|$lib_size|$lib_sha"
h_wrong="$(newhelper wrongfields)"
d6="$scratch/dest6"
run_helper c6 "$canary" "$h_wrong" --dest "$d6" --artifact-dir "$wrong_pool"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"declares"* && ! -e "$d6/root" ]]; then
	ok "a matching digest with the wrong Package/Version is still refused"
else
	bad "wrong control fields: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi

# --- 6. the extracted tree must actually contain the binary --------------------
nobin_pool="$scratch/artifacts-nobin"
mkdir -p "$nobin_pool"
cp -- "$pool/fixture-lib_2.0-1_amd64.deb" "$nobin_pool/"
mkdir -p "$fx/empty/usr/share/fixture"
printf 'no binary here\n' >"$fx/empty/usr/share/fixture/note"
deb_empty="$(build_deb "$fx/empty" fixture-tool 1.0-1 amd64)"
cp -- "$deb_empty" "$nobin_pool/fixture-tool_1.0-1_amd64.deb"
empty_sha="$(sha256sum "$nobin_pool/fixture-tool_1.0-1_amd64.deb" | cut -d' ' -f1)"
empty_size="$(stat -c '%s' "$nobin_pool/fixture-tool_1.0-1_amd64.deb")"
reset_pins
P_ARTIFACTS="fixture-tool|1.0-1|amd64|pool/main/f/fixture/fixture-tool_1.0-1_amd64.deb|$empty_size|$empty_sha
fixture-lib|2.0-1|amd64|pool/main/f/fixture/fixture-lib_2.0-1_amd64.deb|$lib_size|$lib_sha"
h_nobin="$(newhelper nobin)"
d7="$scratch/dest7"
run_helper c7 "$canary" "$h_nobin" --dest "$d7" --artifact-dir "$nobin_pool"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"usr/bin/bsdtar is not a regular file"* && ! -e "$d7/root" ]]; then
	ok "an extraction without the binary is refused"
else
	bad "missing binary: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi

# --- 7. a different binary digest ----------------------------------------------
reset_pins
P_BINSHA="$(printf '%064d' 0)"
h_bindigest="$(newhelper bindigest)"
d8="$scratch/dest8"
run_helper c8 "$canary" "$h_bindigest" --dest "$d8" --artifact-dir "$pool"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"digest is"* && ! -e "$d8/root" ]]; then
	ok "a binary whose digest is not the pinned one is refused"
else
	bad "binary digest: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi

# --- 8. a --version that does not identify the pinned build --------------------
reset_pins
P_VERRE='^bsdtar 0\.0\.0 '
h_version="$(newhelper version)"
d9="$scratch/dest9"
run_helper c9 "$canary" "$h_version" --dest "$d9" --artifact-dir "$pool"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"not the pinned version"* && ! -e "$d9/root" ]]; then
	ok "a binary that does not report the pinned version is refused"
else
	bad "version identity: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi

# --- 9. a pinned library that the root does not carry --------------------------
reset_pins
P_LIBS="libfixture-absent.so.9|$fx_libtinfo_sha"
h_misslib="$(newhelper misslib)"
d10="$scratch/dest10"
run_helper c10 "$canary" "$h_misslib" --dest "$d10" --artifact-dir "$pool"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"is absent from the root"* && ! -e "$d10/root" ]]; then
	ok "a pinned library missing from the root is refused"
else
	bad "missing library: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi

# --- 10. a dependency that silently falls through to the host ------------------
# The library package is dropped and the pin list points at a marker that IS shipped, so the
# digest stage passes and only the loader can tell: libtinfo then resolves from the host.
mkdir -p "$fx/marker/usr/lib/$triplet"
printf 'fixture marker\n' >"$fx/marker/usr/lib/$triplet/libfixture-marker.so"
deb_marker="$(build_deb "$fx/marker" fixture-lib 2.0-1 amd64)"
host_pool="$scratch/artifacts-hostfall"
mkdir -p "$host_pool"
cp -- "$pool/fixture-tool_1.0-1_amd64.deb" "$host_pool/"
cp -- "$deb_marker" "$host_pool/fixture-lib_2.0-1_amd64.deb"
marker_sha="$(sha256sum "$host_pool/fixture-lib_2.0-1_amd64.deb" | cut -d' ' -f1)"
marker_size="$(stat -c '%s' "$host_pool/fixture-lib_2.0-1_amd64.deb")"
marker_lib_sha="$(sha256sum "$fx/marker/usr/lib/$triplet/libfixture-marker.so" | cut -d' ' -f1)"
reset_pins
P_ARTIFACTS="fixture-tool|1.0-1|amd64|pool/main/f/fixture/fixture-tool_1.0-1_amd64.deb|$tool_size|$tool_sha
fixture-lib|2.0-1|amd64|pool/main/f/fixture/fixture-lib_2.0-1_amd64.deb|$marker_size|$marker_sha"
P_LIBS="libfixture-marker.so|$marker_lib_sha"
h_hostfall="$(newhelper hostfall)"
d11="$scratch/dest11"
run_helper c11 "$canary" "$h_hostfall" --dest "$d11" --artifact-dir "$host_pool"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"outside the root"* && ! -e "$d11/root" ]]; then
	ok "a library that silently resolves to the host is refused"
else
	bad "host fallback: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi

# --- 11. arguments, platform and ABI -------------------------------------------
run_helper c12 "$canary" "$h_ok" --dest relative/dir
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"absolute"* ]]; then
	ok "a relative --dest is refused"
else
	bad "relative dest: rc=$rc stderr='$rc_err'"
fi
run_helper c13 "$canary" "$h_ok"
if [[ "$rc" == 2 && -z "$rc_out" ]]; then
	ok "a missing --dest is refused"
else
	bad "missing dest: rc=$rc stderr='$rc_err'"
fi
run_helper c14 "$canary" "$h_ok" --dest "$scratch/dest14" --artifact-dir relative
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"absolute"* ]]; then
	ok "a relative --artifact-dir is refused"
else
	bad "relative artifact-dir: rc=$rc stderr='$rc_err'"
fi
ln -s "$scratch/dest1" "$scratch/dest-link"
run_helper c15 "$canary" "$h_ok" --dest "$scratch/dest-link"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"symlink"* ]]; then
	ok "a symlinked --dest is refused"
else
	bad "symlink dest: rc=$rc stderr='$rc_err'"
fi
reset_pins
P_ARCH=riscv64
h_arch="$(newhelper arch)"
run_helper c16 "$canary" "$h_arch" --dest "$scratch/dest16" --artifact-dir "$pool"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"architecture"* ]]; then
	ok "an unsupported architecture is refused"
else
	bad "arch gate: rc=$rc stderr='$rc_err'"
fi
reset_pins
P_OS=Plan9
h_os="$(newhelper os)"
run_helper c17 "$canary" "$h_os" --dest "$scratch/dest17" --artifact-dir "$pool"
if [[ "$rc" == 2 && -z "$rc_out" ]]; then
	ok "an unsupported operating system is refused"
else
	bad "os gate: rc=$rc stderr='$rc_err'"
fi
reset_pins
P_ABI=99.0
h_abi="$(newhelper abi)"
reset_pins
: >"$scratch/canary.argv"
run_helper c18 "$canary" "$h_abi" --dest "$scratch/dest18"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"glibc >= 99.0"* && ! -e "$scratch/dest18" && ! -s "$scratch/canary.argv" ]]; then
	ok "an insufficient glibc is refused before anything is fetched or created"
else
	bad "abi gate: rc=$rc stdout='$rc_out' stderr='$rc_err'"
fi
run_helper c19 "$canary" "$h_ok" --dest "$scratch/dest19" --unknown-flag
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *usage* ]]; then
	ok "an unknown flag is refused with the usage line"
else
	bad "unknown flag: rc=$rc stderr='$rc_err'"
fi
# --- 12. the producer failing is not a pass ------------------------------------
empty_dir="$scratch/artifacts-empty"
mkdir -p "$empty_dir"
run_helper c20 "$canary" "$h_ok" --dest "$scratch/dest20" --artifact-dir "$empty_dir"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"absent from --artifact-dir"* ]]; then
	ok "an artifact missing from --artifact-dir is refused"
else
	bad "absent artifact: rc=$rc stderr='$rc_err'"
fi
fail_port="$scratch/failport"
mkdir -p "$fail_port"
printf '#!/usr/bin/env bash\nexit 7\n' >"$fail_port/curl"
chmod +x "$fail_port/curl"
cp -- "$stub/dpkg-deb" "$fail_port/dpkg-deb"
run_helper c21 "$fail_port" "$h_ok" --dest "$scratch/dest21"
if [[ "$rc" == 2 && -z "$rc_out" && "$rc_err" == *"could not download"* && ! -e "$scratch/dest21/root" ]]; then
	ok "a failing fetch port is refused, not worked around"
else
	bad "producer failure: rc=$rc stderr='$rc_err'"
fi
# --- 13. a scratch that cannot be created stops before anything is touched -----
# R1, found in independent review on 2026-09-08. Until then the scratch guard sat inside an
# `if` CONDITION LIST — the `if` opened at the helper-presence guard and its `then` did not
# arrive until an assertion three hundred lines later — so `mktemp` failing did not fire
# errexit. $scratch stayed empty and the very next assignment, `fx="$scratch/fixtures"`,
# pointed at /fixtures: outside every directory this battery is allowed to touch. `bash -n`
# and shellcheck both exited 0 on that shape, which is exactly why this case exists and why
# a parse check is not an acceptance of control flow.
#
# The four variants below break the scratch producer in the four ways the guard now names,
# and prove the battery reaches NONE of mkdir, cp or dpkg-deb. Those three ports are
# canaries: they record their argv and write nothing at all, so the regression is OBSERVED
# and never performed. The subject is a private copy of this file, so a future edit that
# re-displaces the boundary is caught here and not in a runner.
selfcopy="$scratch/selfcopy.sh"
cp -- "${BASH_SOURCE[0]}" "$selfcopy"
scratch_canary="$scratch/scratch-canary"
mkdir -p "$scratch_canary" "$scratch/out"
for port in mkdir cp dpkg-deb; do
	{
		printf '#!/usr/bin/env bash\n'
		printf 'printf "%s %%s\\n" "$*" >>"%s"\n' "$port" "$scratch/scratch-canary.log"
		printf 'exit 0\n'
	} >"$scratch_canary/$port"
	chmod +x "$scratch_canary/$port"
done
# mktemp_variant BODY — rewrite the scratch producer; BODY decides how it misbehaves.
mktemp_variant() {
	{
		printf '#!/usr/bin/env bash\n'
		printf 'printf "mktemp %%s\\n" "$*" >>"%s"\n' "$scratch/scratch-canary.mktemp.log"
		printf '%s\n' "$1"
	} >"$scratch_canary/mktemp"
	chmod +x "$scratch_canary/mktemp"
}
# run_scratch_case LABEL EXPECTED-CAUSE — the copy must exit 2, name the cause, print no
# assertion at all, and leave the canary log empty.
run_scratch_case() {
	local label=$1 want=$2 src err out touched
	: >"$scratch/scratch-canary.log"
	local o="$scratch/out/$label.stdout" e="$scratch/out/$label.stderr"
	set +e
	env -u OLIVARES_BSDTAR -u OLIVARES_BSDTAR_LIB -u OLIVARES_GORELEASER -u LD_LIBRARY_PATH \
		PATH="$scratch_canary:$PATH" TMPDIR="$scratch" OLIVARES_ROOT="$root_dir" \
		bash "$selfcopy" >"$o" 2>"$e"
	src=$?
	set -e
	err="$(cat "$e")"
	out="$(cat "$o")"
	touched="$(cat "$scratch/scratch-canary.log")"
	if [[ "$src" == 2 && "$err" == *"$want"* && -z "$out" && -z "$touched" ]]; then
		ok "$label: rc 2, cause named, and no mkdir/cp/dpkg-deb outside the scratch"
	else
		bad "$label: rc=$src stderr='$err' stdout='$out' touched='$touched'"
	fi
}
mktemp_variant 'exit 73'
run_scratch_case scratch-producer-fails 'could not create a scratch directory'
mktemp_variant 'exit 0'
run_scratch_case scratch-producer-silent 'produced no scratch path'
mktemp_variant 'printf "/fixtures\n"; exit 0'
run_scratch_case scratch-producer-foreign 'does not own'
mktemp_variant "printf '%s\\n' '$scratch/test-ensure-bsdtar.ZZZZZZ'; exit 0"
run_scratch_case scratch-producer-absent 'not a plain directory'

printf '\n%s: %d ok, %d not ok\n' "$me" "$pass" "$fail"
[[ "$fail" == 0 ]] || exit 1
