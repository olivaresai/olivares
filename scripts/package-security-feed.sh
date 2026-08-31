#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# package-security-feed.sh — package and verify the signed out-of-band PSIRT
# advisory feed. This script is deliberately local-only: it never publishes,
# deploys, or accesses the network.
#
# Usage:
#   package-security-feed.sh [--draft <draft.json>] [--sign-key <keyfile>]
#     [--pubkey <b64|@file>] [--out-dir <dir>] [--olivares <bin>] [--selftest]

# The documented task invokes this file through `sh`; re-enter Bash before using
# pipefail and the Bash cleanup array below.
[ -n "${BASH_VERSION:-}" ] || exec bash "$0" "$@"
set -euo pipefail

DRAFT=""
SIGN_KEY=""
PUBKEY=""
OUT_DIR="dist/security-feed"
OLIVARES=""
SELFTEST=0
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TEMP_PATHS=()
SCRIPT_BASHPID="$BASHPID"

die() { echo "error: $*" >&2; exit 1; }

cleanup() {
  local path
  [ "$BASHPID" = "$SCRIPT_BASHPID" ] || return 0
  for path in "${TEMP_PATHS[@]:-}"; do
    [ -n "$path" ] && rm -rf "$path"
  done
}
trap cleanup EXIT

need_value() {
  [ $# -ge 2 ] && [ -n "$2" ] || die "$1 requires a value"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --draft) need_value "$@"; DRAFT="$2"; shift 2;;
    --sign-key) need_value "$@"; SIGN_KEY="$2"; shift 2;;
    --pubkey) need_value "$@"; PUBKEY="$2"; shift 2;;
    --out-dir) need_value "$@"; OUT_DIR="$2"; shift 2;;
    --olivares) need_value "$@"; OLIVARES="$2"; shift 2;;
    --selftest) SELFTEST=1; shift;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0;;
    *) die "unknown flag: $1";;
  esac
done

resolve_olivares() {
  if [ -z "$OLIVARES" ]; then
    local build_dir
    # The development container mounts /tmp noexec, so the throwaway binary must
    # live under the executable workspace. It is removed by the EXIT trap.
    build_dir="$(mktemp -d "$ROOT/.security-feed-build.XXXXXX")"
    TEMP_PATHS+=("$build_dir")
    OLIVARES="$build_dir/olivares"
    echo "==> building olivares (security-feed producer and consumer) ..."
    ( cd "$ROOT/cmd/olivares" && \
      GOWORK="${GOWORK:-}" GOPROXY=off GOSUMDB=off go build -o "$OLIVARES" . )
  fi
  command -v "$OLIVARES" >/dev/null 2>&1 || [ -x "$OLIVARES" ] || \
    die "olivares binary not runnable: $OLIVARES"
}

if [ "$SELFTEST" -eq 1 ]; then
  [ -z "$DRAFT" ] || die "--draft cannot be combined with --selftest"
  [ -z "$SIGN_KEY" ] || die "--sign-key cannot be combined with --selftest"
  [ -z "$PUBKEY" ] || die "--pubkey cannot be combined with --selftest"
  [ "$OUT_DIR" = "dist/security-feed" ] || die "--out-dir cannot be combined with --selftest"

  resolve_olivares
  selftest_dir="$(mktemp -d)"
  TEMP_PATHS+=("$selftest_dir")
  private_key="$selftest_dir/security-feed-test.key"
  public_key="$selftest_dir/security-feed-test.pub"
  selftest_out="$selftest_dir/dist/security-feed"

  echo "==> generating ephemeral TEST signing keys ..."
  "$OLIVARES" license keygen --out-private "$private_key" --out-public "$public_key"
  bash "$0" \
    --draft "$ROOT/cmd/olivares/fixtures/security-drill/draft-advisories.json" \
    --sign-key "$private_key" \
    --pubkey "@$public_key" \
    --out-dir "$selftest_out" \
    --olivares "$OLIVARES"

  for output in advisories.json advisories.json.sig SHA256SUMS feed-metadata.json; do
    [ -f "$selftest_out/$output" ] || die "selftest output missing: $output"
  done
  echo "security-feed packaging selftest PASSED"
  exit 0
fi

[ -n "$DRAFT" ] || die "--draft is required"
[ -n "$SIGN_KEY" ] || die "--sign-key is required (base64 Ed25519 private-key file)"
[ -f "$DRAFT" ] || die "draft file $DRAFT does not exist"
[ -f "$SIGN_KEY" ] || die "sign-key file $SIGN_KEY does not exist"
command -v base64 >/dev/null 2>&1 || die "base64 not found"
command -v jq >/dev/null 2>&1 || die "jq not found"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum not found"

resolve_olivares
mkdir -p "$OUT_DIR"
feed="$OUT_DIR/advisories.json"
signature="$OUT_DIR/advisories.json.sig"

if [ -z "$PUBKEY" ]; then
  decoded_key="$(mktemp)"
  TEMP_PATHS+=("$decoded_key")
  if ! tr -d '[:space:]' < "$SIGN_KEY" | base64 --decode > "$decoded_key" 2>/dev/null; then
    die "sign-key file $SIGN_KEY is not valid base64"
  fi
  key_bytes="$(wc -c < "$decoded_key" | tr -d '[:space:]')"
  case "$key_bytes" in
    64)
      PUBKEY="$(tail -c 32 "$decoded_key" | base64 | tr -d '\n')"
      ;;
    32)
      die "sign-key file contains a 32-byte Ed25519 seed; pass --pubkey <b64|@file>"
      ;;
    *)
      die "sign-key file decodes to $key_bytes bytes; expected a 64-byte Ed25519 private key or 32-byte seed"
      ;;
  esac
fi

echo "==> generating signed security-advisory feed ..."
# ⛔ EL ANCLA VA EN LA FIRMA, NO SOLO EN LA VERIFICACION POSTERIOR. Una clave publica Ed25519 y un
# seed miden los dos 32 bytes: sin declarar contra que clave debe verificar el receptor, el
# productor acepta la mitad publica como seed y firma con un par derivado. Por eso la resolucion
# de PUBKEY sube por delante de esta llamada, donde antes iba detras.
"$OLIVARES" security advisories \
  --in "$DRAFT" \
  --out "$feed" \
  --sign-key "@$SIGN_KEY" \
  --expect-pubkey "$PUBKEY"


echo "==> verifying packaged feed with the consumer ..."
if verify_output="$("$OLIVARES" security check \
  --feed "$feed" \
  --pubkey "$PUBKEY" \
  --product-version 0.0.1 2>&1)"; then
  verify_status=0
else
  verify_status=$?
fi
printf '%s\n' "$verify_output"
if grep -q "did not verify" <<<"$verify_output"; then
  die "packaged feed was refused by the consumer"
fi
if ! grep -Eq "no known advisory|AFFECTED" <<<"$verify_output"; then
  die "consumer output did not prove a verified affected-or-clean result (exit $verify_status)"
fi

tamper_dir="$(mktemp -d)"
TEMP_PATHS+=("$tamper_dir")
tampered_feed="$tamper_dir/advisories.json"
cp "$feed" "$tampered_feed"
first_byte="$(od -An -tu1 -N1 "$tampered_feed" | tr -d '[:space:]')"
[ -n "$first_byte" ] || die "generated feed is empty"
flipped_byte=$((first_byte ^ 1))
printf '%b' "\\$(printf '%03o' "$flipped_byte")" | \
  dd of="$tampered_feed" bs=1 count=1 conv=notrunc 2>/dev/null

echo "==> proving a one-byte-tampered copy is refused ..."
if tamper_output="$("$OLIVARES" security check \
  --feed "$tampered_feed" \
  --sig "$signature" \
  --pubkey "$PUBKEY" \
  --product-version 0.0.1 2>&1)"; then
  tamper_status=0
else
  tamper_status=$?
fi
printf '%s\n' "$tamper_output"
[ "$tamper_status" -ne 0 ] || die "tampered feed unexpectedly exited successfully"
if ! grep -q "did not verify" <<<"$tamper_output"; then
  die "tampered feed refusal was not visible in consumer output"
fi

( cd "$OUT_DIR" && sha256sum advisories.json advisories.json.sig > SHA256SUMS )
feed_sha="$(sha256sum "$feed" | awk '{print $1}')"
signature_sha="$(sha256sum "$signature" | awk '{print $1}')"
jq --arg feed_sha "$feed_sha" --arg signature_sha "$signature_sha" \
  '{
    advisory_ids: [.advisories[].id],
    advisory_count: (.advisories | length),
    modified: .modified,
    sha256: {
      "advisories.json": $feed_sha,
      "advisories.json.sig": $signature_sha
    }
  }' "$feed" > "$OUT_DIR/feed-metadata.json"

advisory_ids="$(jq -r '[.advisories[].id] | join(", ")' "$feed")"
advisory_count="$(jq -r '.advisories | length' "$feed")"
modified="$(jq -r '.modified' "$feed")"
echo "==> packaged and verified $advisory_count advisory(ies) in $OUT_DIR"
echo "    advisory ids: $advisory_ids"
echo "    feed modified: $modified"
echo "    feed sha256: $feed_sha"
echo "    signature sha256: $signature_sha"
echo "==> PUBLISHING stays manual per docs/PSIRT-RUNBOOK.md §3: release channel + embargoed enterprise copy + air-gap bundle fold-in."
