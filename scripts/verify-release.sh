#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Verify an Olivares AI release the way a distrustful sysadmin should. For a
# security product the build pipeline is part of the trust model (docs/SECURITY-HARDENING.md) —
# never run an unverified binary. This checks, in order, whatever is present:
#
#   1. cosign signature over checksums.txt  (always)
#   2. SHA-256 of every downloaded artifact  (always)
#   3. SBOM in-toto attestation per archive   (SCP-03, if *.sbom.sigstore.json present)
#   4. OpenVEX attestation per archive        (SCP-04, if *.vex.sigstore.json present)
#   5. SLSA build provenance per archive      (SCP-01, if *.intoto.jsonl present)
#
# Steps 3–5 are skipped with a clear note when their files (or the verifier tool)
# are absent, so this works on a minimal release AND fully verifies a complete one.
#
# Usage (run from the directory holding the downloaded release files):
#   verify-release.sh                      # keyless / Sigstore (default; network for Rekor)
#   verify-release.sh --key cosign.pub     # key-based
#   verify-release.sh --key cosign.pub --offline   # key-based, transparency log ignored
#   verify-release.sh --source-tag v1.2.3  # pin the SLSA provenance source tag
#   verify-release.sh --provenance FILE    # name the SLSA provenance explicitly (required
#                                          # when several *.intoto.jsonl files are present:
#                                          # an ambiguous selection FAILS, it never guesses)
#
# WHAT "OFFLINE" HERE DOES AND DOES NOT MEAN (corrected 2026-07-25). `--offline` sets
# `--insecure-ignore-tlog` and, on the keyless path, cosign's own `--offline`. That removes
# the REKOR lookup. It is not by itself a promise that no socket is opened:
#   * KEY-BASED (`--key`) verification is genuinely disconnected, and this is MEASURED —
#     scripts/check-cosign-contract.sh performs exactly this `verify-blob --key` round trip
#     behind a proxy that refuses every connection, and it passes.
#   * KEYLESS verification still needs Sigstore TUF trusted-root material, which cosign
#     fetches unless it is already cached. On a genuinely air-gapped machine use `--key`.
#     (This script has NO `--trusted-root` option — an earlier draft of this note told
#     readers to pass one, which would simply have been rejected as an unknown argument.
#     Seeding TUF and driving cosign directly is the workaround; wiring `--trusted-root`
#     through here is an open follow-up.)
#   * The keyless disconnected path has NOT been measured here and is UNCERTAIN.
# The earlier wording called this "air-gap: no Rekor/tlog network at all", which was an
# unverified claim about someone else's tool.
#
# Expected in the current directory:
#   checksums.txt(.sig/.pem)             signed SHA-256 manifest (.pem keyless only)
#   <artifacts listed in checksums.txt>
#   <archive>.sbom.sigstore.json         (optional) signed SPDX SBOM attestation
#   <archive>.vex.sigstore.json          (optional) signed OpenVEX attestation
#   multiple.intoto.jsonl                (optional) SLSA provenance (slsa-github-generator)
#
# Requires: cosign, sha256sum. Optional: slsa-verifier (step 5). Keyless needs network
# (Rekor, and TUF trusted-root material); `--key` is the measured disconnected path — see
# the note above, which supersedes any shorter summary of this.
set -euo pipefail

KEY=""
OFFLINE=0
SOURCE_TAG=""
PROVENANCE=""
# The keyless identity the release workflow signs as (GitHub OIDC). Override with
# OLIVARES_CERT_IDENTITY / OLIVARES_CERT_OIDC_ISSUER for a fork.
# The keyless Sigstore identity the release workflow signs as. FULLY ANCHORED: cosign
# matches --certificate-identity-regexp UNANCHORED, so the previous
# '^https://github.com/olivaresai/olivares' also accepted `.../olivares-anything/...`
# and any workflow file on any branch -- i.e. far more identities than the one that
# actually signs a release.
DEFAULT_CERT_IDENTITY='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
CERT_IDENTITY_REGEXP="${OLIVARES_CERT_IDENTITY:-$DEFAULT_CERT_IDENTITY}"
CERT_OIDC_ISSUER="${OLIVARES_CERT_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"
SOURCE_URI="${OLIVARES_SOURCE_URI:-github.com/olivaresai/olivares}"

while [ $# -gt 0 ]; do
  case "$1" in
    --key) KEY="$2"; shift 2 ;;
    --offline) OFFLINE=1; shift ;;
    --source-tag) SOURCE_TAG="$2"; shift 2 ;;
    --provenance) PROVENANCE="$2"; shift 2 ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

command -v cosign >/dev/null || { echo "error: cosign not found (https://docs.sigstore.dev/cosign/installation)"; exit 1; }
command -v sha256sum >/dev/null || { echo "error: sha256sum not found"; exit 1; }
[ -f checksums.txt ] || { echo "error: checksums.txt not found in $(pwd)"; exit 1; }
[ -f checksums.txt.sig ] || { echo "error: checksums.txt.sig not found"; exit 1; }

# Key-based (air-gap) signatures are produced WITHOUT a Rekor transparency-log
# entry, so their verification must ignore the tlog. Keyless signatures always
# carry a tlog entry; --offline tells cosign to use the entry bundled offline.
TLOG_IGNORE=0
{ [ -n "$KEY" ] || [ "$OFFLINE" -eq 1 ]; } && TLOG_IGNORE=1

# cosign attestation-verify flags shared by steps 3/4, parameterised by mode.
att_id_flags() {
  if [ -n "$KEY" ]; then
    printf -- '--key\n%s\n' "$KEY"
  else
    printf -- '--certificate-identity-regexp\n%s\n--certificate-oidc-issuer\n%s\n' "$CERT_IDENTITY_REGEXP" "$CERT_OIDC_ISSUER"
  fi
  [ "$TLOG_IGNORE" -eq 1 ] && printf -- '--insecure-ignore-tlog\n'
}

echo "==> 1/5 verifying the cosign signature over checksums.txt"
if [ -n "$KEY" ]; then
  cosign verify-blob --key "$KEY" --signature checksums.txt.sig \
    $([ "$TLOG_IGNORE" -eq 1 ] && echo --insecure-ignore-tlog) checksums.txt
else
  [ -f checksums.txt.pem ] || { echo "error: checksums.txt.pem (cert) required for keyless verify; or pass --key"; exit 1; }
  cosign verify-blob \
    --certificate checksums.txt.pem \
    --signature checksums.txt.sig \
    --certificate-identity-regexp "$CERT_IDENTITY_REGEXP" \
    --certificate-oidc-issuer "$CERT_OIDC_ISSUER" \
    $([ "$OFFLINE" -eq 1 ] && echo --offline) \
    checksums.txt
fi
echo "    signature OK"

echo "==> 2/5 verifying artifact checksums"
present=0
archives=""
while read -r sum name; do
  [ -n "${name:-}" ] || continue
  if [ -f "$name" ]; then
    echo "$sum  $name" | sha256sum -c -
    present=$((present + 1))
    case "$name" in *.tar.gz|*.zip) archives="$archives $name" ;; esac
  fi
done < checksums.txt
[ "$present" -gt 0 ] || { echo "error: none of the artifacts in checksums.txt are present here"; exit 1; }

# --- 3/5 SBOM attestations (SCP-03) ---------------------------------------------
echo "==> 3/5 verifying SBOM in-toto attestations (SPDX)"
sbom_checked=0
for a in $archives; do
  b="$a.sbom.sigstore.json"
  [ -f "$b" ] || continue
  cosign verify-blob-attestation --type spdxjson --bundle "$b" --new-bundle-format \
    --check-claims $(att_id_flags) "$a" >/dev/null
  echo "    SBOM attestation OK: $a"
  sbom_checked=$((sbom_checked + 1))
done
[ "$sbom_checked" -gt 0 ] || echo "    (skipped: no *.sbom.sigstore.json present)"

# --- 4/5 OpenVEX attestations (SCP-04) ------------------------------------------
echo "==> 4/5 verifying OpenVEX attestations"
vex_checked=0
for a in $archives; do
  b="$a.vex.sigstore.json"
  [ -f "$b" ] || continue
  cosign verify-blob-attestation --type openvex --bundle "$b" --new-bundle-format \
    --check-claims $(att_id_flags) "$a" >/dev/null
  echo "    VEX attestation OK: $a"
  vex_checked=$((vex_checked + 1))
done
[ "$vex_checked" -gt 0 ] || echo "    (skipped: no *.vex.sigstore.json present)"

# --- 5/5 SLSA build provenance (SCP-01) -----------------------------------------
echo "==> 5/5 verifying SLSA build provenance"
# EXACT selection, never first-match. The old `ls | head -1` silently picked whichever
# *.intoto.jsonl sorted first: with two provenance files in the directory (say, one from
# a previous download next to the current one) the verifier could "pass" every archive
# against the WRONG release's provenance — the same defect class as the release
# workflow's old first-Docker-artifact attestation target. Zero files skips loudly, one
# file is used, several files REFUSE and demand --provenance.
if [ -n "$PROVENANCE" ]; then
  [ -f "$PROVENANCE" ] || { echo "error: --provenance '$PROVENANCE' does not exist"; exit 1; }
  prov="$PROVENANCE"
else
  prov=""
  prov_n=0
  for f in ./*.intoto.jsonl; do
    [ -f "$f" ] || continue
    prov="$f"
    prov_n=$((prov_n + 1))
  done
  if [ "$prov_n" -gt 1 ]; then
    echo "error: $prov_n *.intoto.jsonl files present — ambiguous provenance. Pass --provenance FILE to name the one for THIS release; refusing to guess." >&2
    exit 1
  fi
fi
prov_checked=0
if [ -z "$prov" ]; then
  echo "    (skipped: no *.intoto.jsonl provenance present)"
elif ! command -v slsa-verifier >/dev/null 2>&1; then
  echo "    (skipped: slsa-verifier not installed — https://github.com/slsa-framework/slsa-verifier)"
else
  slsa_args="--source-uri $SOURCE_URI"
  [ -n "$SOURCE_TAG" ] && slsa_args="$slsa_args --source-tag $SOURCE_TAG"
  for a in $archives; do
    slsa-verifier verify-artifact "$a" --provenance-path "$prov" $slsa_args >/dev/null
    echo "    SLSA provenance OK: $a"
    prov_checked=$((prov_checked + 1))
  done
  [ "$prov_checked" -gt 0 ] || echo "    (provenance present but no archives matched)"
fi

# The summary claims ONLY what step 5 counted: `prov_checked` verifications. The old
# condition (provenance file present + slsa-verifier installed) claimed "+ SLSA
# provenance" even when zero archives were verified against it — a verifier asserting
# success it did not check, the exact shape scripts/check-verifier-truth.sh exists for.
echo "✅ release verified: signature + $present checksum(s)$([ "$sbom_checked" -gt 0 ] && echo " + $sbom_checked SBOM attestation(s)")$([ "$vex_checked" -gt 0 ] && echo " + $vex_checked VEX attestation(s)")$([ "$prov_checked" -gt 0 ] && echo " + SLSA provenance ($prov_checked archive(s))")"
