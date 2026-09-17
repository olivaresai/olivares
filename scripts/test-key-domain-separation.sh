#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# End-to-end proof: build a community release binary with two throwaway
# anchors, issue one license with the license key, issue one OTA manifest with the
# OTA key, and prove neither signature verifies in the other trust domain.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BINS="$ROOT/cmd/olivares/firstparty/bins"
cleanup_probe=0

if [ "$#" -gt 0 ]; then
  if [ "$#" -ne 2 ] || [ "$1" != "--selftest-cleanup" ]; then
    echo "usage: $0 [--selftest-cleanup <bins-dir>]" >&2
    exit 2
  fi
  if ! cleanup_probe_base="$(cd "${TMPDIR:-/tmp}" 2>/dev/null && pwd -P)"; then
    echo "FATAL: cleanup self-test TMPDIR is unavailable: ${TMPDIR:-/tmp}" >&2
    exit 2
  fi
  if [ "$cleanup_probe_base" = / ]; then
    echo "FATAL: cleanup self-test refuses a filesystem-root TMPDIR" >&2
    exit 2
  fi
  if ! BINS="$(cd "$2" 2>/dev/null && pwd -P)"; then
    echo "FATAL: cleanup self-test bins directory is unavailable: $2" >&2
    exit 2
  fi
  if [ "$BINS" = "$cleanup_probe_base" ]; then
    echo "FATAL: cleanup self-test bins cannot be TMPDIR itself: $BINS" >&2
    exit 2
  fi
  case "$BINS/" in
    "$cleanup_probe_base"/*) ;;
    *) echo "FATAL: cleanup self-test bins must be below TMPDIR: $BINS" >&2; exit 2 ;;
  esac
  cleanup_probe=1
fi

[ -d "$BINS" ] || { echo "FATAL: first-party bins directory is missing: $BINS" >&2; exit 2; }
if [ "$cleanup_probe" -eq 1 ] \
    && { [ ! -f "$BINS/PLACEHOLDER" ] || [ ! -f "$BINS/.olivares-key-domain-cleanup-selftest" ]; }; then
  echo "FATAL: cleanup self-test directory lacks its PLACEHOLDER or safety marker: $BINS" >&2
  exit 2
fi

WORK=""

restore_firstparty_bins() {
  # build:release populates these ignored files before this test in CI. They are
  # inputs to the release-tag builds below, but not repository content, so never
  # let them contaminate the next command run from this worktree.
  find "$BINS" -type f ! -name PLACEHOLDER -delete
}

cleanup() {
  local rc=$?
  trap - EXIT
  if ! restore_firstparty_bins; then
    echo "FATAL: could not restore first-party bins: $BINS" >&2
    [ "$rc" -ne 0 ] || rc=1
  fi
  if [ -n "$WORK" ] && ! rm -rf "$WORK"; then
    echo "FATAL: could not remove key-domain scratch: $WORK" >&2
    [ "$rc" -ne 0 ] || rc=1
  fi
  exit "$rc"
}

trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-key-domains.XXXXXX")"

if [ "$cleanup_probe" -eq 1 ]; then
  : > "$BINS/KEY-DOMAIN-CLEANUP-PROBE"
  exit 0
fi

cd "$ROOT"

# ⛔ LA PRECONDICION SE COMPRUEBA, NO SE ESCRIBE EN UN COMENTARIO. Doce lineas mas arriba dice
# «build:release populates these ignored files before this test in CI», y eso era todo lo que la
# sostenia. Medido sobre la corrida 33926027726: el job `hook-only-legs` adopto esta bateria SIN
# ese paso, y lo que se vio a los seis minutos fue
#
#     cmd/olivares/firstparty/embed_binaries_release.go:18:21: pattern bins/claude-source: no matching files found
#
# — un error de `go:embed` que no nombra la causa ni el remedio, y que se lee como codigo roto.
# El fichero es un GUARDA a proposito (`all:bins` solo casaria PLACEHOLDER y dejaria salir una
# release con cero conectores), asi que su ausencia es una precondicion ausente, no un defecto:
# la respuesta correcta es NO HE PODIDO MIRAR, diciendo que falta y como se consigue.
if [ ! -e "$BINS/claude-source" ]; then
	echo "test-key-domain-separation: ⛔ NO HE PODIDO MIRAR: falta $BINS/claude-source" >&2
	echo "                 Las compilaciones de abajo llevan \`-tags release\`, y ese fichero es una" >&2
	echo "                 guarda de compilacion: sin el, \`go build\` muere en el \`go:embed\` sin decir" >&2
	echo "                 por que. Lo puebla \`task build:release\` (scripts/build-connectors.sh)." >&2
	exit 2
fi

go run ./cmd/olivares license keygen \
  --out-private "$WORK/license.key" --out-public "$WORK/license.pub" >/dev/null
go run ./cmd/olivares license keygen \
  --out-private "$WORK/ota.key" --out-public "$WORK/ota.pub" >/dev/null

license_pub="$(tr -d '[:space:]' < "$WORK/license.pub")"
ota_pub="$(tr -d '[:space:]' < "$WORK/ota.pub")"
license_fp="$(printf '%s' "$license_pub" | base64 -d | sha256sum | cut -c1-8)"
ota_fp="$(printf '%s' "$ota_pub" | base64 -d | sha256sum | cut -c1-8)"

echo "==> validating two distinct release anchors"
OLIVARES_LICENSE_PUBKEY="$license_pub" OLIVARES_OTA_PUBKEY="$ota_pub" \
  sh scripts/check-release-pubkey.sh
if OLIVARES_LICENSE_PUBKEY="$license_pub" OLIVARES_OTA_PUBKEY="" \
    sh scripts/check-release-pubkey.sh >/dev/null 2>&1; then
  echo "FATAL: an empty OTA anchor passed the release-build gate" >&2
  exit 1
fi
if OLIVARES_LICENSE_PUBKEY="" OLIVARES_OTA_PUBKEY="$ota_pub" \
    sh scripts/check-release-pubkey.sh >/dev/null 2>&1; then
  echo "FATAL: an empty license anchor passed the release-build gate" >&2
  exit 1
fi
if OLIVARES_LICENSE_PUBKEY="$license_pub" OLIVARES_OTA_PUBKEY="$license_pub" \
    sh scripts/check-release-pubkey.sh >/dev/null 2>&1; then
  echo "FATAL: identical license and OTA anchors passed the release-build gate" >&2
  exit 1
fi
if OLIVARES_RELEASE_PUBKEY="$license_pub" \
    OLIVARES_LICENSE_PUBKEY="$license_pub" OLIVARES_OTA_PUBKEY="$ota_pub" \
    sh scripts/check-release-pubkey.sh >/dev/null 2>&1; then
  echo "FATAL: the obsolete shared-key variable was silently accepted" >&2
  exit 1
fi

echo "==> building a community release binary with both embedded anchors"
CGO_ENABLED=0 go build -tags release \
  -ldflags "-X main.version=26.8.0 -X github.com/olivaresai/olivares/core/license.releasePublicKeyB64=$license_pub -X github.com/olivaresai/olivares/core/release.artifactVerifyKeyB64=$ota_pub" \
  -o "$WORK/olivares" ./cmd/olivares
version_out="$("$WORK/olivares" version)"
case "$version_out" in
  *"license-key=release/$license_fp"*"ota-key=release/$ota_fp"*) ;;
  *) echo "FATAL: binary did not report both expected fingerprints: $version_out" >&2; exit 1;;
esac

echo "==> proving license signatures stay in the license domain"
license_blob="$("$WORK/olivares" license sign --licensee test-license-domain \
  --key "$(tr -d '[:space:]' < "$WORK/license.key")")"
"$WORK/olivares" license verify "$license_blob" >/dev/null
ota_signed_license="$("$WORK/olivares" license sign --licensee test-wrong-domain \
  --key "$(tr -d '[:space:]' < "$WORK/ota.key")")"
if "$WORK/olivares" license verify "$ota_signed_license" >/dev/null 2>&1; then
  echo "FATAL: the embedded license anchor accepted a license signed by the OTA key" >&2
  exit 1
fi

echo "==> proving OTA signatures stay in the OTA domain"
mkdir -p "$WORK/channel"
printf 'synthetic community archive\n' > "$WORK/channel/olivares_26.9.0_linux_amd64.tar.gz"
"$WORK/olivares" release manifest --dir "$WORK/channel" --channel stable \
  --version v26.9.0 --out "$WORK/channel/manifest.json" \
  --sign-key "@$WORK/ota.key" >/dev/null
"$WORK/olivares" upgrade --bundle "$WORK/channel" --check \
  --target "$WORK/olivares" --data-dir "$WORK/data" --os linux --arch amd64 >/dev/null
cp "$WORK/channel/manifest.json.sig" "$WORK/channel/manifest.ota.sig"
# The cross-check is not what this case exercises (it proves the OTA anchor
# rejects a LICENSE-key signature), and this synthetic channel carries no
# checksums.txt, so opt out of the belt explicitly rather than fabricate one.
"$WORK/olivares" release sign-manifest --manifest "$WORK/channel/manifest.json" \
  --sign-key "@$WORK/license.key" --out "$WORK/channel/manifest.json.sig" \
  --unsafe-no-crosscheck >/dev/null
if "$WORK/olivares" upgrade --bundle "$WORK/channel" --check \
    --target "$WORK/olivares" --data-dir "$WORK/data" --os linux --arch amd64 >/dev/null 2>&1; then
  echo "FATAL: the embedded OTA anchor accepted a manifest signed by the online license key" >&2
  exit 1
fi
mv "$WORK/channel/manifest.ota.sig" "$WORK/channel/manifest.json.sig"

echo "==> proving a binary with no OTA anchor fails verification closed"
CGO_ENABLED=0 go build -tags release \
  -ldflags "-X main.version=26.8.0 -X github.com/olivaresai/olivares/core/license.releasePublicKeyB64=$license_pub" \
  -o "$WORK/olivares-no-ota" ./cmd/olivares
if "$WORK/olivares-no-ota" upgrade --bundle "$WORK/channel" --check \
    --target "$WORK/olivares-no-ota" --data-dir "$WORK/no-ota-data" \
    --os linux --arch amd64 >/dev/null 2>&1; then
  echo "FATAL: a binary with no embedded OTA anchor verified an update" >&2
  exit 1
fi

# The build gate above rejects equal anchors, but it only guards the goreleaser /
# `task build:repro` paths. A DIRECT -ldflags build — the recipe documented in both
# key packages and used by scripts/fips-verify.sh — bypasses it entirely, so the
# collision has to be visible on the artifact itself at RUNTIME.
echo "==> proving a key-reusing binary built around the gate warns at runtime"
CGO_ENABLED=0 go build -tags release \
  -ldflags "-X main.version=26.8.0 -X github.com/olivaresai/olivares/core/license.releasePublicKeyB64=$license_pub -X github.com/olivaresai/olivares/core/release.artifactVerifyKeyB64=$license_pub" \
  -o "$WORK/olivares-shared-anchor" ./cmd/olivares
shared_warning="$("$WORK/olivares-shared-anchor" version 2>&1 >/dev/null || true)"
case "$shared_warning" in
  *"WARNING"*"SAME key"*) ;;
  *) echo "FATAL: a binary reusing one key across both trust domains did not warn: ${shared_warning:-<no stderr>}" >&2; exit 1;;
esac
distinct_warning="$("$WORK/olivares" version 2>&1 >/dev/null || true)"
case "$distinct_warning" in
  *"WARNING"*) echo "FATAL: a correctly-built binary emitted a key-domain warning: $distinct_warning" >&2; exit 1;;
esac

# M-01: the OTA manifest is signed off-box while checksums.txt is signed in CI by
# cosign. Nothing forces the two to agree unless the ceremony cross-checks them, so
# a manifest substituted on the draft release would be signed blind.
echo "==> proving a substituted manifest cannot pass the ceremony cross-check"
archive="$WORK/channel/olivares_26.9.0_linux_amd64.tar.gz"
real_digest="$(sha256sum "$archive" | cut -d' ' -f1)"
printf '%s  olivares_26.9.0_linux_amd64.tar.gz\n' "$real_digest" > "$WORK/channel/checksums.txt"
"$WORK/olivares" release verify-manifest --manifest "$WORK/channel/manifest.json" \
  --sig "$WORK/channel/manifest.json.sig" --checksums "$WORK/channel/checksums.txt" \
  --dir "$WORK/channel" --expect-channel stable --expect-version 26.9.0 >/dev/null
printf 'malicious archive the attacker uploaded\n' > "$WORK/evil.tar.gz"
evil_digest="$(sha256sum "$WORK/evil.tar.gz" | cut -d' ' -f1)"
sed "s/$real_digest/$evil_digest/" "$WORK/channel/manifest.json" > "$WORK/channel/swapped-manifest.json"
if "$WORK/olivares" release verify-manifest --manifest "$WORK/channel/swapped-manifest.json" \
    --checksums "$WORK/channel/checksums.txt" >/dev/null 2>&1; then
  echo "FATAL: a manifest whose digests contradict the signed checksums passed the cross-check" >&2
  exit 1
fi
# The freshness bound is now what you get by FORGETTING: the manifest above was
# generated with no --expires-in and must still carry one (the air-gap path forwards
# the flag only when the caller sets it, so an opt-in default shipped unbounded bundles).
if ! "$WORK/olivares" release verify-manifest --manifest "$WORK/channel/manifest.json" \
    --checksums "$WORK/channel/checksums.txt" >/dev/null 2>&1; then
  echo "FATAL: a manifest generated without --expires-in must still carry a freshness bound" >&2
  exit 1
fi
# ...and producing an unbounded one takes an explicit opt-out, which the verifier then
# refuses BY DEFAULT. Requiring the bound used to be opt-in: forget the flag and you
# got 'anti-freeze DISABLED' followed by a reassuring 'OK:'.
"$WORK/olivares" release manifest --dir "$WORK/channel" --channel stable \
  --version v26.9.0 --no-expiry --out "$WORK/channel/unbounded-manifest.json" >/dev/null
if "$WORK/olivares" release verify-manifest --manifest "$WORK/channel/unbounded-manifest.json" \
    --checksums "$WORK/channel/checksums.txt" >/dev/null 2>&1; then
  echo "FATAL: a manifest with no freshness bound was accepted BY DEFAULT" >&2
  exit 1
fi
if ! "$WORK/olivares" release verify-manifest --manifest "$WORK/channel/unbounded-manifest.json" \
    --checksums "$WORK/channel/checksums.txt" --allow-no-expiry >/dev/null 2>&1; then
  echo "FATAL: --allow-no-expiry is the explicit opt-out and must be honoured" >&2
  exit 1
fi

# checksums.txt binds DIGESTS and nothing else. A manifest whose digests are all honest
# but whose POLICY locks the whole fleet out of every future upgrade must not pass.
echo "==> proving a hostile POLICY cannot pass the ceremony cross-check either"
sed 's/"version": "26.9.0",/"version": "26.9.0",\n  "min_version": "99.0.0",/' \
  "$WORK/channel/manifest.json" > "$WORK/channel/policy-manifest.json"
if "$WORK/olivares" release verify-manifest --manifest "$WORK/channel/policy-manifest.json" \
    --checksums "$WORK/channel/checksums.txt" >/dev/null 2>&1; then
  echo "FATAL: a min_version above the release (nobody may ever upgrade) passed the cross-check" >&2
  exit 1
fi

echo "PASS: license and OTA key domains are distinct; empty/equal anchors are rejected by the"
echo "      build gate AND flagged at runtime when the gate is bypassed; a manifest that"
echo "      contradicts the signed checksums — in its digests OR in its policy — cannot"
echo "      pass the ceremony cross-check"
