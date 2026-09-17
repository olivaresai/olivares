#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ensure-bsdtar.sh — resolve a bsdtar PINNED BY DIGEST, with its whole non-glibc library
# closure, and print the absolute root it was extracted into.
#
# WHY. scripts/test-nfpm-openrc.sh and scripts/nfpm-apk-postinstall-runtime.sh read real
# .deb/.rpm/.apk with bsdtar. Until 2026-09-08 the CI step obtained it with
# `sudo apt-get install libarchive-tools`, and in mainline-ci run 34167328609 (job
# control-plane, id 101880875672, runner actions-runner-9) that install ended rc 100 because
# open(2) of /var/lib/dpkg/lock-frontend returned ENOENT — the host's dpkg layout, not a
# permission, a token or contention. Two steps then went red for ONE missing tool: the
# battery answered exit 2 («could not look») at scripts/test-nfpm-openrc.sh, and no
# .deb/.rpm/.apk assertion was ever measured. The tool is now provisioned inside the job's
# own scratch, from a fixed signed snapshot, and NOTHING here touches host state: no
# apt/dpkg database, no sudo, no maintainer scripts, no install, no loader replacement and
# no fallback to whatever bsdtar happens to be on PATH. Same shape as ensure-goreleaser.sh,
# with the one difference that matters: bsdtar is not a single static binary, so the pin
# covers its complete dynamic closure, not only the executable.
#
# PROVENANCE OF THE PINS. Every row below was derived on 2026-09-08 UTC from the primary
# Ubuntu metadata chain, never transcribed from a listing page:
#   1. https://snapshot.ubuntu.com/ubuntu/20260901T000000Z/dists/{noble,noble-updates,
#      noble-security}/InRelease — each verified with gpgv against the Ubuntu Archive
#      Automatic Signing Key (2018), RSA 4096, fingerprint
#      F6ECB3762474EDA9D21B7022871920D1991BC93C. That key was obtained independently from
#      https://archive.ubuntu.com/ubuntu/project/ubuntu-archive-keyring.gpg and from
#      https://keyserver.ubuntu.com, and both agree on fingerprint and user id.
#   2. main/ and universe/ binary-amd64 Packages.xz, fetched by-hash and checked against the
#      SHA-256 and size carried in those signed InRelease files.
#   3. The dependency closure of libarchive-tools computed from those indexes — Depends only,
#      no Recommends and no Suggests — excluding exactly the glibc/loader ABI boundary.
#   4. Sizes and SHA-256 taken from the index stanzas; the extracted binary and library
#      digests computed after each .deb matched its pinned size and digest.
# The full receipt (InRelease digests, index digests, per-package rows, the DT_NEEDED walk)
# lives outside Git with the implementation evidence, as the acquisition metadata is not a
# repository artifact.
#
# MEASURED ABI. The pinned set declares libc6 (>= 2.38) — the strongest constraint in the
# closure, from libarchive-tools, libarchive13t64, libacl1, libxml2, libicu74 and libstdc++6 —
# and the pinned bsdtar itself references GLIBC_2.38 symbol versions. A host below that
# cannot run it, so this script refuses instead of publishing a root that would fail later
# inside the battery. Adding another architecture means another signed manifest and its own
# acceptance run; nothing here is extrapolated to arm64.
#
# USAGE:  ensure-bsdtar.sh --dest ABSOLUTE_DIR [--artifact-dir ABSOLUTE_DIR]
#   Prints ONE line on stdout: the absolute root that was published.
#   exit 0  the root satisfies the complete manifest
#   exit 2  could not provide one — bad argument, unsupported OS/architecture/ABI, missing
#           tool, absent artifact, network error, wrong size/digest/control field, incomplete
#           extraction, a library that does not resolve inside the root, or a binary that does
#           not execute and report the pinned version. Never a fallback to another bsdtar.
#   --artifact-dir is the offline mode: the SAME pinned .deb set is read from that directory
#   by file name and verified identically. It substitutes neither digests nor URLs, and in
#   that mode the network is never touched.
set -euo pipefail
LC_ALL=C
export LC_ALL

me=ensure-bsdtar

# >>> PINS
# One sentinel-delimited block. scripts/test-ensure-bsdtar.sh replaces everything between
# these two markers in a PRIVATE copy so its fixtures never reach this production pin set.
SNAPSHOT=20260901T000000Z
BASE_URL="https://snapshot.ubuntu.com/ubuntu/$SNAPSHOT"
OS_SUPPORTED=Linux
ARCH_SUPPORTED=x86_64
ABI_MIN_GLIBC=2.38
LIB_RELDIRS=(usr/lib/x86_64-linux-gnu lib/x86_64-linux-gnu)
BIN_RELPATH=usr/bin/bsdtar
BIN_SHA256=9de9e4edd807090fa570eb91431fdf6a1c8b54fa2aae546be7bdc13fefbe26a5
EXPECT_VERSION_RE='^bsdtar 3\.7\.2 - libarchive 3\.7\.2([[:space:]]|$)'
# package|version|architecture|pool path|bytes|sha256
ARTIFACTS='
libarchive-tools|3.7.2-2ubuntu0.8|amd64|pool/universe/liba/libarchive/libarchive-tools_3.7.2-2ubuntu0.8_amd64.deb|72488|ca4f763c2b35a49b9d37a19cd0d3b6625c04c0b81fb4986dd3b95a6ed9de1b77
libarchive13t64|3.7.2-2ubuntu0.8|amd64|pool/main/liba/libarchive/libarchive13t64_3.7.2-2ubuntu0.8_amd64.deb|382584|ba9684092d71656a3bfd909518c76cc15eefeef9cbac0a36a8cef5632c62f56f
libacl1|2.3.2-1build1.1|amd64|pool/main/a/acl/libacl1_2.3.2-1build1.1_amd64.deb|16792|f2bfd3f8f00413d5f1f04fc723063803c56ac0f1e0efae3bc41f2d7276972ec3
libbz2-1.0|1.0.8-5.1ubuntu0.1|amd64|pool/main/b/bzip2/libbz2-1.0_1.0.8-5.1ubuntu0.1_amd64.deb|34562|36ee08b00b1ee5018b2fce02881b7d2d73c9b4e32990bde3388400d86c48126a
liblz4-1|1.9.4-1build1.1|amd64|pool/main/l/lz4/liblz4-1_1.9.4-1build1.1_amd64.deb|63062|319331270d5cc52d5ebffe51c941d7b01b432bc402c2924b557209a64d4ecbad
liblzma5|5.6.1+really5.4.5-1ubuntu0.3|amd64|pool/main/x/xz-utils/liblzma5_5.6.1+really5.4.5-1ubuntu0.3_amd64.deb|127352|d2eabd41ca77d2c2dd9d5d4ef478cccb64ffde6279c47cf4699a857d46785a52
libnettle8t64|3.9.1-2.2build1.1|amd64|pool/main/n/nettle/libnettle8t64_3.9.1-2.2build1.1_amd64.deb|181636|6d97fbc1972633083f08f51ccab433606c97bbceb897c631c66495117ca3406f
libxml2|2.9.14+dfsg-1.3ubuntu3.8|amd64|pool/main/libx/libxml2/libxml2_2.9.14+dfsg-1.3ubuntu3.8_amd64.deb|763972|bfd07c01d6e5ab3e327f3ca5819409b1914bbfb3f1a016d53e4dabd5f96143bb
libzstd1|1.5.5+dfsg2-2build1.1|amd64|pool/main/libz/libzstd/libzstd1_1.5.5+dfsg2-2build1.1_amd64.deb|299472|dfcf25061e07aad7efd3f4f880ba5ad4d4d09ebe7fc8cc77ab6b8a161d6d4727
zlib1g|1:1.3.dfsg-3.1ubuntu2.2|amd64|pool/main/z/zlib/zlib1g_1.3.dfsg-3.1ubuntu2.2_amd64.deb|62988|84b9cf5752b29c9f92c27cd4c4ba9bbcc70b5ccf9b1b515421a28ae23212e273
libicu74|74.2-1ubuntu3.1|amd64|pool/main/i/icu/libicu74_74.2-1ubuntu3.1_amd64.deb|10860078|c9a70989678660eed9a1e904c74fa043da8bec8e2036856fc16e31ced79b04f8
libgcc-s1|14.2.0-4ubuntu2~24.04.1|amd64|pool/main/g/gcc-14/libgcc-s1_14.2.0-4ubuntu2~24.04.1_amd64.deb|78392|aa7fadbe33b78bcf99885318040601c550c208929565b179891d9a3cc2aa68cd
libstdc++6|14.2.0-4ubuntu2~24.04.1|amd64|pool/main/g/gcc-14/libstdc++6_14.2.0-4ubuntu2~24.04.1_amd64.deb|792064|a51f8de7829211db961a31f02158058ad1a95f92ac6d0a5dff6350e2821c54c0
gcc-14-base|14.2.0-4ubuntu2~24.04.1|amd64|pool/main/g/gcc-14/gcc-14-base_14.2.0-4ubuntu2~24.04.1_amd64.deb|51014|b95c172411a7fdae70307cf33a9f5320ba5e056b556454543dd5b679d5ce1c4f
'
# soname|sha256 of the file the soname resolves to, inside the published root
LIBS='
libarchive.so.13|6de6959b1a1d0c4bbe0c8fadc3d410060f579e0cc671c9d3dc9ad20f5de91e47
libacl.so.1|1f5247285409ec89990e9fe384d78dfb2ae6ec8e210a2657073babc8d6b7e54f
libbz2.so.1.0|cc08c9f50a8009ffd6391e0116a100369b11ca9238fa52392c019e6645b122a1
liblz4.so.1|40bffd0a098387368b16b992abd5f7cf43c0fa2f05cabe5a6d483719554adfda
liblzma.so.5|696e868dd0700a19a6d65fc01608ec2d70d3cb91f65710e89180cd2e688f30cb
libnettle.so.8|a245ed916229583d6fe0b07657b3ed945bc148c28cc08b5266295f620d2b91cc
libxml2.so.2|8b6f68c242929aebe49eb28f800dc3e8ee7147298c94bdabc834f61eff1ac177
libzstd.so.1|0a2128bc10841fb29e76d08d945864dfb0b6a66da5df6df5d8299197439e54bb
libz.so.1|86200da370f20476a2507e9097a789b5ef97269b4ca8d5e164ad82dab9d99892
libicuuc.so.74|7560aadde38e5f4237a47a1ddd5891f9b36768a77a60faae30beee003ac01901
libicudata.so.74|ddbb3718b8bd9cbd780e5ab08b4503c30a6c4fa0706ebe5d074ed6b596c1714e
libgcc_s.so.1|d93224d2b0dab4247598be683adca02f5cf00586f99c187579cd7e92058fb7cb
libstdc++.so.6|1fd75fe70354a416d75aef22bcae68c47bd25d20e2d0568c30b1a9838cf62f11
'
# <<< PINS

refuse() {
	printf '%s: NO HE PODIDO MIRAR — %s\n' "$me" "$*" >&2
	exit 2
}
usage() {
	printf 'usage: %s --dest ABSOLUTE_DIR [--artifact-dir ABSOLUTE_DIR]\n' "$me" >&2
	exit 2
}

dest=
artifact_dir=
while [[ $# -gt 0 ]]; do
	case "$1" in
	--dest)
		[[ $# -ge 2 ]] || usage
		dest=$2
		shift 2
		;;
	--dest=*)
		dest=${1#--dest=}
		shift
		;;
	--artifact-dir)
		[[ $# -ge 2 ]] || usage
		artifact_dir=$2
		shift 2
		;;
	--artifact-dir=*)
		artifact_dir=${1#--artifact-dir=}
		shift
		;;
	*) usage ;;
	esac
done
case "$dest" in
/*) ;;
*) refuse "--dest must be an absolute directory (got '${dest:-<empty>}')" ;;
esac
case "$dest" in
*/) refuse "--dest must not end in a slash (got '$dest')" ;;
esac
if [[ -n "$artifact_dir" ]]; then
	case "$artifact_dir" in
	/*) ;;
	*) refuse "--artifact-dir must be an absolute directory (got '$artifact_dir')" ;;
	esac
	[[ -d "$artifact_dir" ]] || refuse "--artifact-dir is not a directory: $artifact_dir"
fi
# A symlinked destination is refused rather than followed: the published root is meant to be
# the job's own scratch, and following a link is how a shared or hostile path gets written.
if [[ -L "$dest" ]]; then
	refuse "--dest is a symlink, which is never published into: $dest"
fi

# --- platform and measured ABI -------------------------------------------------
# Refused BEFORE anything is fetched: an unsupported host must not spend a download to learn
# that the artifacts it pinned cannot run on it.
os="$(uname -s)"
[[ "$os" == "$OS_SUPPORTED" ]] ||
	refuse "the pinned artifacts are $OS_SUPPORTED builds; this host is $os"
arch="$(uname -m)"
case "$arch" in
"$ARCH_SUPPORTED") ;;
amd64) [[ "$ARCH_SUPPORTED" == x86_64 ]] || refuse "no pinned bsdtar manifest for architecture $arch" ;;
*) refuse "no pinned bsdtar manifest for architecture $arch (only $ARCH_SUPPORTED)" ;;
esac
libc_report="$(getconf GNU_LIBC_VERSION 2>/dev/null || true)"
case "$libc_report" in
'glibc '*) host_glibc=${libc_report#glibc } ;;
*) refuse "the pinned artifacts need glibc >= $ABI_MIN_GLIBC and this host does not report a glibc (got '${libc_report:-nothing}'; musl and other libcs are not supported)" ;;
esac
# Numeric major.minor comparison; a glibc that does not parse is refused, never assumed good.
[[ "$host_glibc" =~ ^([0-9]+)\.([0-9]+) ]] ||
	refuse "could not read a major.minor glibc version from '$libc_report'"
host_major=${BASH_REMATCH[1]} host_minor=${BASH_REMATCH[2]}
[[ "$ABI_MIN_GLIBC" =~ ^([0-9]+)\.([0-9]+) ]] ||
	refuse "the pinned ABI floor '$ABI_MIN_GLIBC' is not a major.minor version"
min_major=${BASH_REMATCH[1]} min_minor=${BASH_REMATCH[2]}
if ((host_major < min_major || (host_major == min_major && host_minor < min_minor))); then
	refuse "the pinned bsdtar needs glibc >= $ABI_MIN_GLIBC and this host has $host_glibc; no loader or glibc is ever substituted to work around that"
fi

# --- pins, parsed once ---------------------------------------------------------
artifact_rows=()
while IFS= read -r row; do
	[[ -n "$row" ]] || continue
	artifact_rows+=("$row")
done <<<"$ARTIFACTS"
((${#artifact_rows[@]} > 0)) || refuse "the pin block declares no artifacts"
lib_rows=()
while IFS= read -r row; do
	[[ -n "$row" ]] || continue
	lib_rows+=("$row")
done <<<"$LIBS"
((${#lib_rows[@]} > 0)) || refuse "the pin block declares no libraries"

# The whole pinned set is small on purpose. A manifest that grew past this bound would be a
# different contract and must be re-reviewed, not silently downloaded.
MAX_TOTAL_BYTES=104857600
total_bytes=0
for row in "${artifact_rows[@]}"; do
	IFS='|' read -r p_pkg p_ver p_arch p_path p_size p_sha <<<"$row"
	[[ -n "$p_pkg" && -n "$p_ver" && -n "$p_arch" && -n "$p_path" && -n "$p_size" && -n "$p_sha" ]] ||
		refuse "malformed artifact pin row: '$row'"
	[[ "$p_size" =~ ^[0-9]+$ ]] || refuse "artifact $p_pkg has a non-numeric pinned size '$p_size'"
	[[ "$p_sha" =~ ^[0-9a-f]{64}$ ]] || refuse "artifact $p_pkg has a malformed pinned sha256"
	case "$p_path" in
	pool/*) ;;
	*) refuse "artifact $p_pkg has a pool path outside pool/: '$p_path'" ;;
	esac
	case "$p_path" in
	*..*) refuse "artifact $p_pkg has a traversing pool path: '$p_path'" ;;
	esac
	total_bytes=$((total_bytes + p_size))
done
((total_bytes <= MAX_TOTAL_BYTES)) ||
	refuse "the pinned set is $total_bytes bytes, over the $MAX_TOTAL_BYTES byte bound"

digest() { command sha256sum -- "$1" 2>/dev/null | command cut -d' ' -f1; }

# verify_root ROOT — the COMPLETE manifest, and the only definition of «good» in this script.
# Used both to accept an already published destination and to clear a freshly staged one.
# Prints the first reason it failed; prints nothing and returns 0 when the root is good.
verify_root() {
	local root=$1
	if [[ ! -d "$root" || -L "$root" ]]; then
		printf 'root is not a plain directory'
		return 1
	fi
	local bin="$root/$BIN_RELPATH"
	if [[ ! -f "$bin" || -L "$bin" ]]; then
		printf '%s is not a regular file' "$BIN_RELPATH"
		return 1
	fi
	if [[ ! -x "$bin" ]]; then
		printf '%s is not executable' "$BIN_RELPATH"
		return 1
	fi
	local got
	got="$(digest "$bin")"
	if [[ "$got" != "$BIN_SHA256" ]]; then
		printf '%s digest is %s, pinned %s' "$BIN_RELPATH" "${got:-nothing}" "$BIN_SHA256"
		return 1
	fi
	# Every library directory the pin declares that exists is handed to the loader, and
	# nothing else is: an absent one must not become a silent search of the host.
	local libpath='' d='' present=0
	for d in "${LIB_RELDIRS[@]}"; do
		if [[ -d "$root/$d" ]]; then
			libpath="${libpath:+$libpath:}$root/$d"
			present=$((present + 1))
		fi
	done
	if ((present == 0)); then
		printf 'no pinned library directory exists under the root'
		return 1
	fi
	local row so sha target
	for row in "${lib_rows[@]}"; do
		IFS='|' read -r so sha <<<"$row"
		if [[ -z "$so" || ! "$sha" =~ ^[0-9a-f]{64}$ ]]; then
			printf 'malformed library pin row: %s' "$row"
			return 1
		fi
		target=
		for d in "${LIB_RELDIRS[@]}"; do
			[[ -e "$root/$d/$so" ]] || continue
			target="$root/$d/$so"
			break
		done
		if [[ -z "$target" ]]; then
			printf 'pinned library %s is absent from the root' "$so"
			return 1
		fi
		got="$(digest "$target")"
		if [[ "$got" != "$sha" ]]; then
			printf 'library %s digest is %s, pinned %s' "$so" "${got:-nothing}" "$sha"
			return 1
		fi
	done
	# It must RUN, and it must be the pinned version. LD_LIBRARY_PATH is scoped to this one
	# process; it is never exported to the job.
	local out
	out="$(env LD_LIBRARY_PATH="$libpath" "$bin" --version 2>/dev/null | command head -n 1 || true)"
	if [[ ! "$out" =~ $EXPECT_VERSION_RE ]]; then
		printf '%s --version reported %s, which is not the pinned version' \
			"$BIN_RELPATH" "${out:-nothing}"
		return 1
	fi
	# And every non-glibc object it loads must come from inside the root. A dependency that
	# silently falls through to a host library is the defect this whole script exists to
	# close, so it is an error even though the binary ran.
	local ldd_out
	ldd_out="$(env LD_LIBRARY_PATH="$libpath" ldd "$bin" 2>/dev/null || true)"
	if [[ -z "$ldd_out" ]]; then
		printf 'could not read the loaded-object list of %s' "$BIN_RELPATH"
		return 1
	fi
	local line name resolved
	while IFS= read -r line; do
		case "$line" in
		*'=>'*) ;;
		*) continue ;;
		esac
		name="${line%%=>*}"
		name="${name#"${name%%[![:space:]]*}"}"
		name="${name%"${name##*[![:space:]]}"}"
		resolved="${line#*=>}"
		resolved="${resolved#"${resolved%%[![:space:]]*}"}"
		resolved="${resolved%% (*}"
		case "$name" in
		libc.so.6 | libm.so.6 | libpthread.so.0 | libdl.so.2 | librt.so.1 | libresolv.so.2 | ld-linux-x86-64.so.2 | linux-vdso.so.1)
			continue
			;;
		esac
		if [[ "$resolved" == *'not found'* ]]; then
			printf 'library %s does not resolve at all' "$name"
			return 1
		fi
		case "$resolved" in
		"$root"/*) ;;
		*)
			printf 'library %s resolves to %s, outside the root' "$name" "$resolved"
			return 1
			;;
		esac
	done <<<"$ldd_out"
	return 0
}

root="$dest/root"

# An already published root is reused ONLY if it still satisfies the whole manifest. Partial
# or divergent content is an error, and nothing outside the staging pattern is ever deleted.
if [[ -e "$root" || -L "$root" ]]; then
	if why="$(verify_root "$root")"; then
		printf '%s\n' "$root"
		exit 0
	fi
	refuse "the existing root $root does not satisfy the pinned manifest ($why); it is left untouched"
fi

for tool in sha256sum cut head stat mktemp mkdir mv cp dpkg-deb ldd getconf uname; do
	command -v "$tool" >/dev/null 2>&1 || refuse "missing $tool, needed to provide bsdtar"
done
if [[ -z "$artifact_dir" ]]; then
	command -v curl >/dev/null 2>&1 || refuse "missing curl, needed to fetch the pinned artifacts"
fi

command mkdir -p -- "$dest"
stage="$(command mktemp -d "$dest/.ensure-bsdtar.XXXXXX")" ||
	refuse "could not create a staging directory under $dest"
# Cleanup is limited to this script's own staging marker, never to $dest or to a root.
trap 'rm -rf -- "$stage"' EXIT
command mkdir -p -- "$stage/debs" "$stage/root"

for row in "${artifact_rows[@]}"; do
	IFS='|' read -r p_pkg p_ver p_arch p_path p_size p_sha <<<"$row"
	base="${p_path##*/}"
	deb="$stage/debs/$base"
	if [[ -n "$artifact_dir" ]]; then
		src="$artifact_dir/$base"
		[[ -f "$src" ]] || refuse "$base is absent from --artifact-dir $artifact_dir"
		command cp -- "$src" "$deb" || refuse "could not read $src"
	else
		url="$BASE_URL/$p_path"
		case "$url" in
		https://*) ;;
		*) refuse "refusing a non-HTTPS artifact URL: $url" ;;
		esac
		command curl -sS --proto '=https' --proto-redir '=https' --tlsv1.2 \
			--location --max-redirs 3 \
			--connect-timeout 20 --max-time 300 --retry 2 --retry-delay 2 \
			--max-filesize "$p_size" \
			-o "$deb" "$url" ||
			refuse "could not download $base from $url"
	fi
	got_size="$(command stat -c '%s' -- "$deb" 2>/dev/null || printf 'unknown')"
	[[ "$got_size" == "$p_size" ]] ||
		refuse "$base is $got_size bytes, pinned $p_size"
	got="$(digest "$deb")"
	if [[ "$got" != "$p_sha" ]]; then
		printf '    expected %s\n    obtained %s\n' "$p_sha" "${got:-nothing}" >&2
		refuse "$base does not match its pinned sha256"
	fi
	# Only AFTER size and digest match is dpkg-deb allowed to look at the file, and only to
	# read three control fields and to extract the data member. dpkg is never run, no
	# maintainer script is executed and no package database is touched.
	f_pkg="$(command dpkg-deb --field "$deb" Package 2>/dev/null || true)"
	f_ver="$(command dpkg-deb --field "$deb" Version 2>/dev/null || true)"
	f_arch="$(command dpkg-deb --field "$deb" Architecture 2>/dev/null || true)"
	[[ "$f_pkg" == "$p_pkg" && "$f_ver" == "$p_ver" && "$f_arch" == "$p_arch" ]] ||
		refuse "$base declares ${f_pkg:-?}/${f_ver:-?}/${f_arch:-?}, pinned $p_pkg/$p_ver/$p_arch"
	command dpkg-deb -x "$deb" "$stage/root" ||
		refuse "could not extract $base into the staging root"
done

if ! why="$(verify_root "$stage/root")"; then
	refuse "the staged root does not satisfy the pinned manifest ($why)"
fi

# Published only now, by rename inside $dest, so no consumer can ever observe a partial root.
command mv -- "$stage/root" "$root" ||
	refuse "could not publish the verified root at $root"
printf '%s\n' "$root"
