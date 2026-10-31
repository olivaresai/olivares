#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ensure-pitr-pg-binaries.sh — provide the PostgreSQL 15-18 server binaries that
# `task dr:pitr` needs, from PGDG packages PINNED BY DIGEST, into a scratch root.
#
# WHY. The drills-nightly `pitr` job installed postgresql-15..18 with apt only on GitHub-hosted
# runners. The self-hosted runners (build-host, build-host) have no /usr/lib/postgresql, so every
# PITR test failed in 0.00 s with "no PostgreSQL 15 server binaries" (issue #3507, runs 40670247352
# and 40776002136). Installing on the hosts would make the drill depend on one host's package state;
# this helper follows the mainline-ci bsdtar pattern instead: fixed packages, digest-checked, unpacked
# with dpkg-deb -x into the job's own scratch. No apt, no sudo, no dpkg database, no maintainer script.
#
# PROVENANCE. Rows below were read on 2026-10-30 from https://apt.postgresql.org/pub/repos/apt/dists/
# noble-pgdg/main/binary-amd64/Packages.gz, whose InRelease was verified with gpgv against the
# PostgreSQL Debian Repository key B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8 (downloaded from
# https://www.postgresql.org/media/keys/ACCC4CF8.asc). Each .deb's size and SHA-256 match the index.
#
# ABI. The pinned packages require glibc >= 2.38 (noble). The other libraries the binaries load
# (libicu74, libssl3t64, libxml2, libldap, libgssapi-krb5, libpam, libsystemd, libzstd, liblz4) come
# from the runner image; a missing one is a refusal here, not a late failure in the drill. The JIT
# provider (llvmjit.so, needs libllvm19) is not pinned: PITR does not enable JIT.
#
# USAGE:  ensure-pitr-pg-binaries.sh --dest ABSOLUTE_DIR [--artifact-dir ABSOLUTE_DIR]
#   On success prints exactly two lines, for the workflow to append to $GITHUB_ENV:
#     OLIVARES_TEST_PITR_BINDIRS=15=<dir>,16=<dir>,17=<dir>,18=<dir>
#     LD_LIBRARY_PATH=<dir>            (libpq.so.5, which the pinned binaries need and do not RUNPATH)
#   exit 0  the root satisfies the manifest and every major's binaries run and report their version
#   exit 2  could not provide one: bad argument, missing tool, absent or wrong artifact, network
#           error, unexpected control field, a binary that does not run, or an unresolved library.
#           Nothing is published on exit 2.
#   --artifact-dir reads the SAME pinned debs by file name, offline. It substitutes no digest.
set -euo pipefail
LC_ALL=C
export LC_ALL

me=ensure-pitr-pg-binaries
refuse() {
	printf '%s: %s\n' "$me" "$*" >&2
	exit 2
}

# >>> PINS
# One block, one row per package: name|version|pool path|bytes|sha256
BASE_URL=https://apt.postgresql.org/pub/repos/apt
ROWS='
libpq5|18.6-1.pgdg24.04+2|pool/main/p/postgresql-18/libpq5_18.6-1.pgdg24.04+2_amd64.deb|264072|b487c5ed2ceb9244c6a9d6ae65818ed6707c3c44a2e14488394ae4194c52c53b
postgresql-15|15.19-1.pgdg24.04+2|pool/main/p/postgresql-15/postgresql-15_15.19-1.pgdg24.04+2_amd64.deb|15337096|c3753999a186bd7b03a4083373a7d94e79f2d3b0e71a4a3acceec4bbb36e82fa
postgresql-16|16.15-1.pgdg24.04+2|pool/main/p/postgresql-16/postgresql-16_16.15-1.pgdg24.04+2_amd64.deb|16424836|abe560fc574383f520753dfaef79ab24081cda488293d5c991a9f77ac98ef18f
postgresql-17|17.11-1.pgdg24.04+2|pool/main/p/postgresql-17/postgresql-17_17.11-1.pgdg24.04+2_amd64.deb|16949784|d5b58058a82a3f344f832aea2cc54badc4f6265c25c960752cb1f5df5d2180ee
postgresql-18|18.6-1.pgdg24.04+2|pool/main/p/postgresql-18/postgresql-18_18.6-1.pgdg24.04+2_amd64.deb|7576708|c0773357df8769abb70e92722196d9b53422d71c0f29a4f39c834b94276cfa3d
postgresql-client-15|15.19-1.pgdg24.04+2|pool/main/p/postgresql-15/postgresql-client-15_15.19-1.pgdg24.04+2_amd64.deb|1745356|7f6b149a8163e7538d600afcf0eef08b9596fc5a8ad0910f6e48cf016b88de8b
postgresql-client-16|16.15-1.pgdg24.04+2|pool/main/p/postgresql-16/postgresql-client-16_16.15-1.pgdg24.04+2_amd64.deb|1948516|d7258aaa5c4ea092d98730b7d27e54dbcc5493d3b56b4f526c9ddec4b71135e6
postgresql-client-17|17.11-1.pgdg24.04+2|pool/main/p/postgresql-17/postgresql-client-17_17.11-1.pgdg24.04+2_amd64.deb|2052656|b3b071b67a814a382516d6f6241b529c52f909672457e2f48083acce047b634f
postgresql-client-18|18.6-1.pgdg24.04+2|pool/main/p/postgresql-18/postgresql-client-18_18.6-1.pgdg24.04+2_amd64.deb|2114236|b9d10d99a73bf7aa375be2fe36626c40499cfdf553d0ad174674a88a1b256b43
'
# <<< PINS

MAJORS=(15 16 17 18)
LIBDIR=usr/lib/x86_64-linux-gnu

dest=
artifact_dir=
while (($#)); do
	case "$1" in
	--dest) dest=${2-}; shift 2 ;;
	--artifact-dir) artifact_dir=${2-}; shift 2 ;;
	*) refuse "unknown argument: $1" ;;
	esac
done
[[ "$dest" == /* ]] || refuse "--dest must be an absolute directory"
if [[ -n "$artifact_dir" && "$artifact_dir" != /* ]]; then
	refuse "--artifact-dir must be an absolute directory"
fi

for tool in sha256sum stat mktemp mkdir mv cp cut dpkg-deb ldd; do
	command -v "$tool" >/dev/null 2>&1 || refuse "missing $tool"
done
if [[ -z "$artifact_dir" ]]; then
	command -v curl >/dev/null 2>&1 || refuse "missing curl, needed to fetch the pinned packages"
fi

# print_env ROOT: the two lines the workflow appends to $GITHUB_ENV.
print_env() {
	local root=$1 m list=
	for m in "${MAJORS[@]}"; do
		list="${list:+$list,}$m=$root/usr/lib/postgresql/$m/bin"
	done
	printf 'OLIVARES_TEST_PITR_BINDIRS=%s\n' "$list"
	printf 'LD_LIBRARY_PATH=%s\n' "$root/$LIBDIR"
}

# verify_root ROOT: every major's binaries exist, run and report their own major, and every
# library they load resolves. Prints the first problem and returns 1 on failure.
verify_root() {
	local root=$1 m bindir out missing b
	local libpath="$root/$LIBDIR"
	for m in "${MAJORS[@]}"; do
		bindir="$root/usr/lib/postgresql/$m/bin"
		for b in initdb pg_ctl postgres pg_basebackup; do
			[[ -x "$bindir/$b" ]] || { printf 'missing %s/%s' "$bindir" "$b"; return 1; }
		done
		# LD_LIBRARY_PATH is scoped to each of these invocations; it is exported to the test step only
		# after success, through print_env.
		out="$(env LD_LIBRARY_PATH="$libpath" "$bindir/pg_ctl" --version 2>/dev/null || true)"
		[[ "$out" =~ ^pg_ctl\ \(PostgreSQL\)\ $m\. ]] ||
			{ printf 'pg_ctl of %s reported "%s"' "$m" "${out:-nothing}"; return 1; }
		for b in initdb postgres pg_basebackup; do
			missing="$(env LD_LIBRARY_PATH="$libpath" ldd "$bindir/$b" 2>/dev/null | grep 'not found' || true)"
			[[ -z "$missing" ]] || { printf '%s of %s: %s' "$b" "$m" "$missing"; return 1; }
		done
	done
	return 0
}

if [[ -e "$dest/root" ]]; then
	if why="$(verify_root "$dest/root")"; then
		print_env "$dest/root"
		exit 0
	fi
	refuse "the existing root $dest/root does not satisfy the pinned manifest ($why); it is left untouched"
fi

command mkdir -p -- "$dest"
stage="$(command mktemp -d "$dest/.ensure-pitr-pg.XXXXXX")" ||
	refuse "could not create a staging directory under $dest"
# Cleanup is limited to this script's own staging directory, never to $dest or to a root.
trap 'rm -rf -- "$stage"' EXIT
command mkdir -p -- "$stage/debs" "$stage/root"

while IFS='|' read -r p_pkg p_ver p_path p_size p_sha; do
	[[ -n "$p_pkg" ]] || continue
	base="${p_path##*/}"
	deb="$stage/debs/$base"
	if [[ -n "$artifact_dir" ]]; then
		src="$artifact_dir/$base"
		[[ -f "$src" ]] || refuse "$base is absent from --artifact-dir $artifact_dir"
		command cp -- "$src" "$deb" || refuse "could not read $src"
	else
		command curl -sS --proto '=https' --proto-redir '=https' --tlsv1.2 \
			--location --max-redirs 3 --connect-timeout 20 --max-time 600 --retry 2 --retry-delay 2 \
			--max-filesize "$p_size" -o "$deb" "$BASE_URL/$p_path" ||
			refuse "could not download $base"
	fi
	got_size="$(command stat -c '%s' -- "$deb" 2>/dev/null || printf 'unknown')"
	[[ "$got_size" == "$p_size" ]] || refuse "$base is $got_size bytes, pinned $p_size"
	got_sha="$(command sha256sum -- "$deb" | command cut -d' ' -f1)"
	[[ "$got_sha" == "$p_sha" ]] || refuse "$base does not match its pinned sha256 (obtained $got_sha)"
	# Only AFTER size and digest match is dpkg-deb allowed to read the file: two control fields and
	# the data member. dpkg is never run and no maintainer script is executed.
	f_pkg="$(command dpkg-deb --field "$deb" Package 2>/dev/null || true)"
	f_ver="$(command dpkg-deb --field "$deb" Version 2>/dev/null || true)"
	[[ "$f_pkg" == "$p_pkg" && "$f_ver" == "$p_ver" ]] ||
		refuse "$base declares ${f_pkg:-?}/${f_ver:-?}, pinned $p_pkg/$p_ver"
	command dpkg-deb -x "$deb" "$stage/root" || refuse "could not extract $base"
done <<<"$ROWS"

if ! why="$(verify_root "$stage/root")"; then
	refuse "the staged root does not satisfy the pinned manifest ($why)"
fi

# Published only now, by rename inside $dest, so no consumer can observe a partial root.
command mv -- "$stage/root" "$dest/root" || refuse "could not publish the verified root at $dest/root"
print_env "$dest/root"
