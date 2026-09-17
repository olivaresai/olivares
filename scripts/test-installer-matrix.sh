#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Hermetic DIST-24-05 battery.  It exercises the six decisions with fixtures;
# Docker userlands, the public release network path and hosted macOS stay CI-only.
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
lib="$root/scripts/installer-matrix-lib.sh"
scratch_parent="${OLIVARES_TEST_SCRATCH_ROOT:-${TMPDIR:-$(dirname -- "$root")}}"
mkdir -p "$scratch_parent"
scratch="$(mktemp -d "$scratch_parent/olivares-installer-matrix-test.XXXXXX")"
trap 'chmod -R u+w "$scratch" 2>/dev/null || true; rm -rf -- "$scratch"' EXIT
passes=0

ok() {
	passes=$((passes + 1))
	printf 'ok %d - %s\n' "$passes" "$1"
}

expect_rc() {
	want="$1"
	label="$2"
	shift 2
	set +e
	"$@" >"$scratch/out" 2>"$scratch/err"
	got=$?
	set -e
	if [[ "$got" -ne "$want" ]]; then
		printf 'not ok - %s (wanted rc=%s, got rc=%s)\n' "$label" "$want" "$got" >&2
		sed -n '1,120p' "$scratch/out" >&2
		sed -n '1,120p' "$scratch/err" >&2
		exit 1
	fi
	ok "$label"
}

# The first four pairs execute the production installer. curl and cosign are
# deterministic test doubles, but archive parsing, checksum selection, destination
# mutation and every fail-closed branch belong to scripts/install.sh itself.
fixture="$scratch/release"
payload="$scratch/payload"
fakebin="$scratch/fakebin"
mkdir -p "$fixture" "$payload" "$fakebin" "$scratch/tmp" "$scratch/home"
archive=olivares_26.8.0_linux_amd64.tar.gz
release_identity='https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/v26.8.0'
sudo_sentinel="$scratch/sudo-ran"

cat >"$fakebin/curl" <<'FAKECURL'
#!/bin/sh
set -eu
url=""
dest=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		-o) dest="$2"; shift 2 ;;
		https://*) url="$1"; shift ;;
		*) shift ;;
	esac
done
[ -n "$url" ] && [ -n "$dest" ]
cp "$INSTALLER_FIXTURE/${url##*/}" "$dest"
FAKECURL
cat >"$fakebin/cosign" <<'FAKECOSIGN'
#!/bin/sh
set -eu
certificate=""
identity=""
issuer=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		--certificate) certificate="$2"; shift 2 ;;
		--certificate-identity-regexp) identity="$2"; shift 2 ;;
		--certificate-oidc-issuer) issuer="$2"; shift 2 ;;
		*) shift ;;
	esac
done
[ -r "$certificate" ] && [ -n "$identity" ] && [ "$issuer" = 'https://token.actions.githubusercontent.com' ]
if ! grep -Eq -- "$identity" "$certificate"; then
	printf '%s\n' 'cosign fixture: certificate identity does not match' >&2
	exit 1
fi
FAKECOSIGN
cat >"$fakebin/sudo" <<'FAKESUDO'
#!/bin/sh
: >"$SUDO_SENTINEL"
exit 97
FAKESUDO
chmod 0755 "$fakebin/curl" "$fakebin/cosign" "$fakebin/sudo"

write_checksums() {
	digest="$(sha256sum "$fixture/$archive" | awk '{print $1}')"
	printf '%s  %s\n' "$digest" "$archive" >"$fixture/checksums.txt"
}
write_good_release() {
	cat >"$payload/olivares" <<'FAKEOLIVARES'
#!/bin/sh
if [ "${1:-}" = version ]; then
	printf '%s\n' 'olivares 26.8.0 fixture'
	exit 0
fi
exit 2
FAKEOLIVARES
	chmod 0755 "$payload/olivares"
	tar -C "$payload" -czf "$fixture/$archive" olivares
	write_checksums
	printf '%s\n' "$release_identity" >"$fixture/checksums.txt.pem"
	printf '%s\n' 'fixture-signature' >"$fixture/checksums.txt.sig"
}
common_env=(
	HOME="$scratch/home"
	TMPDIR="$scratch/tmp"
	PATH="$fakebin:/usr/bin:/bin"
	INSTALLER_FIXTURE="$fixture"
	SUDO_SENTINEL="$sudo_sentinel"
	OLIVARES_OS=linux
	OLIVARES_ARCH=amd64
	OLIVARES_GITHUB_URL=https://fixture.invalid
)
run_installer() {
	env "${common_env[@]}" /bin/sh "$root/scripts/install.sh" --version v26.8.0 "$@"
}

# i. Change the downloaded archive after the signed checksum manifest was created.
write_good_release
expect_rc 0 "checksum control: production installer accepts signed bytes" run_installer \
	--bindir "$scratch/bin-checksum"
printf 'checksum mutant\n' >>"$fixture/$archive"
expect_rc 1 "mutant i: production installer rejects changed bytes" run_installer \
	--bindir "$scratch/bin-checksum-mutant"
grep -Fq 'checksum mismatch' "$scratch/err"

# ii. Keep a valid-looking certificate, but give it another workflow identity.
write_good_release
expect_rc 0 "identity control: production installer accepts reviewed workflow" run_installer \
	--bindir "$scratch/bin-identity"
printf '%s\n' 'https://github.com/example/fork/.github/workflows/release.yml@refs/tags/v26.8.0' \
	>"$fixture/checksums.txt.pem"
expect_rc 1 "mutant ii: production installer rejects another cosign identity" run_installer \
	--bindir "$scratch/bin-identity-mutant"
grep -Fq 'certificate identity does not match' "$scratch/err"

# iii. Sign the checksum row for a readable archive that lacks top-level olivares.
write_good_release
expect_rc 0 "archive control: production installer extracts top-level olivares" run_installer \
	--bindir "$scratch/bin-archive"
printf '%s\n' 'not the binary' >"$payload/README"
tar -C "$payload" -czf "$fixture/$archive" README
write_checksums
expect_rc 1 "mutant iii: production installer rejects archive without olivares" run_installer \
	--bindir "$scratch/bin-archive-mutant"
grep -Fq 'does not contain a top-level olivares binary' "$scratch/err"

# iv. /proc is not a writable install destination even for uid 0. A fake sudo in
# PATH records any forbidden escalation attempt.
write_good_release
expect_rc 0 "destination control: production installer accepts writable path" run_installer \
	--bindir "$scratch/bin-destination"
expect_rc 1 "mutant iv: production installer refuses unwritable destination" run_installer \
	--bindir /proc/self
grep -Fq 'choose a writable --bindir' "$scratch/err"
expect_rc 0 "destination control: sudo sentinel was not invoked" bash "$lib" no-sudo "$sudo_sentinel"

# v. The expected available version comes from the trusted signed-channel fixture; changing
# only that field must not retain a green doctor verdict in the matrix assertion.
cat >"$scratch/doctor-good.json" <<'JSON'
{"schema":"olivares.ai/doctor/v1","overall":"healthy","checks":[{"name":"update-channel","status":"pass","detail":"current=26.8.0 available=26.9.0 status=upgrade-available"}]}
JSON
cat >"$scratch/doctor-fake-available.json" <<'JSON'
{"schema":"olivares.ai/doctor/v1","overall":"healthy","checks":[{"name":"update-channel","status":"pass","detail":"current=26.8.0 available=99.0.0 status=upgrade-available"}]}
JSON
expect_rc 0 "OTA control: trusted available version matches" bash "$lib" doctor \
	"$scratch/doctor-good.json" 26.8.0 26.9.0 upgrade-available
expect_rc 1 "mutant v: falsified OTA available version is rejected" bash "$lib" doctor \
	"$scratch/doctor-fake-available.json" 26.8.0 26.9.0 upgrade-available
grep -Fq 'differs from trusted fixture' "$scratch/err"

# vi. Known userlands are measurable; an unknown one is the explicit third answer.
printf 'ID=debian\n' >"$scratch/os-release"
expect_rc 0 "platform control: Debian fixture is measurable" bash "$lib" platform debian "$scratch/os-release"
expect_rc 2 "mutant vi: unsupported distro is not reported green" bash "$lib" platform plan9 "$scratch/os-release"
grep -Fq 'NO HE PODIDO MIRAR' "$scratch/err"

printf 'CI-ONLY: real v26.8.0 cosign/download/install, five Docker userlands, hosted macOS, actual service start and TLS doctor probes.\n'
printf '1..%d\n' "$passes"
