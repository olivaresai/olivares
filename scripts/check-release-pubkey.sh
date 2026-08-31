#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-release-pubkey.sh — validate both public anchors before a release build.
#
# The release pipeline injects two independent base64-std Ed25519 PUBLIC keys:
#   OLIVARES_LICENSE_PUBKEY -> core/license.releasePublicKeyB64
#   OLIVARES_OTA_PUBKEY     -> core/release.artifactVerifyKeyB64
# goreleaser hard-fails when a variable is UNSET, but would happily build with an
# EMPTY or malformed value. This guard closes that hole BEFORE the build runs and
# rejects equal anchors so cross-domain key reuse cannot return through config.
#
# It must agree bit-for-bit with the runtime decoders (strings.TrimSpace then
# base64.StdEncoding.DecodeString, len == ed25519.PublicKeySize). Two
# divergences from a naive `base64 -d | wc -c` are handled explicitly:
#   - GNU `base64 -d` silently TRUNCATES at the first non-alphabet byte, so a valid
#     prefix with trailing garbage would decode to 32 bytes and pass — Go rejects it.
#     The strict-alphabet check below forbids any non-base64-std character.
#   - Go trims only LEADING/TRAILING whitespace; INTERNAL whitespace is invalid. We
#     trim only the ends (awk), so the alphabet check rejects embedded whitespace too.
#
# It validates PUBLIC keys only — neither private half may touch the build host.
#
# ⛔ TRES RESPUESTAS, NO DOS. Every exit 1 below is a REAL defect of the key or the
#    configuration. But this guard decodes with `base64`, trims with `awk` and counts with `wc`,
#    and if any of the three is missing from the build host the decode pipeline fails and the
#    message accuses the KEY:
#
#      "check-release-pubkeys: OLIVARES_LICENSE_PUBKEY is not valid padded base64-std"
#
#    That is a blind spot reported as a defect, and it points at the wrong thing: someone would
#    go and regenerate a key that was correct all along. "I could not look" is never "it is
#    broken", and it is never "it is clean" either — so it exits 2, names the missing tool, and
#    says nothing about the keys.
set -eu

# Preflight: the tools this guard reads the world through. Missing → 2, never 1.
for _tool in base64 awk wc; do
	if ! command -v "$_tool" >/dev/null 2>&1; then
		echo "check-release-pubkeys: ⛔ NO HE PODIDO MIRAR: '$_tool' is not on this host, so the keys were never decoded. This says NOTHING about them — install $_tool and re-run." >&2
		exit 2
	fi
done

if [ -n "${OLIVARES_RELEASE_PUBKEY:-}" ]; then
	echo "check-release-pubkeys: OLIVARES_RELEASE_PUBKEY is obsolete — set distinct OLIVARES_LICENSE_PUBKEY and OLIVARES_OTA_PUBKEY anchors" >&2
	exit 1
fi

decoded="$(mktemp "${TMPDIR:-/tmp}/olivares-pubkey.XXXXXX")"
trap 'rm -f "$decoded"' EXIT HUP INT TERM

validate_key() {
	name="$1"
	key="$2"

	# Trim only leading/trailing whitespace (mirrors Go strings.TrimSpace); keep any
	# interior characters so the strict-alphabet check rejects them exactly as Go does.
	key="$(printf '%s' "$key" | awk '{ gsub(/^[ \t\r\n]+|[ \t\r\n]+$/, ""); printf "%s", $0 }')"
	if [ -z "$key" ]; then
		echo "check-release-pubkeys: $name is empty — set it to a base64-std Ed25519 public key" >&2
		exit 1
	fi
	case "$key" in
	*[!A-Za-z0-9+/=]*)
		echo "check-release-pubkeys: $name contains non-base64-std characters (whitespace/garbage)" >&2
		exit 1
		;;
	esac
	if ! printf '%s' "$key" | base64 -d >"$decoded" 2>/dev/null; then
		echo "check-release-pubkeys: $name is not valid padded base64-std" >&2
		exit 1
	fi
	bytes="$(wc -c <"$decoded" | tr -d '[:space:]')"
	if [ "$bytes" != "32" ]; then
		echo "check-release-pubkeys: $name must decode to exactly 32 bytes (an Ed25519 public key); got $bytes" >&2
		exit 1
	fi
	printf '%s' "$key"
}

license_key="$(validate_key OLIVARES_LICENSE_PUBKEY "${OLIVARES_LICENSE_PUBKEY:-}")"
ota_key="$(validate_key OLIVARES_OTA_PUBKEY "${OLIVARES_OTA_PUBKEY:-}")"

if [ "$license_key" = "$ota_key" ]; then
	echo "check-release-pubkeys: OLIVARES_LICENSE_PUBKEY and OLIVARES_OTA_PUBKEY are identical — trust domains require two independent keypairs" >&2
	exit 1
fi
