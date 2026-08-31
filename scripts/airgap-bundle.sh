#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Build a self-contained, OFFLINE-verifiable air-gap install bundle (SCP-06).
# High-security / disconnected buyers (the explicit target) cannot reach Rekor or
# a public registry. This produces a single tarball they can carry across the gap and verify
# with NO network, then mirror into their private registry (scripts/airgap-mirror.sh).
#
# Everything is PINNED BY DIGEST (a tag is mutable, a digest is the bytes we
# signed). The bundle contains:
#   images/<name>/        cosign-saved image + its signatures/attestations (offline)
#   chart/<chart>.tgz     packaged Helm chart  (+ .tgz.sig cosign, + .prov if gpg)
#   sbom/  vex/  prov/     SBOM, OpenVEX and SLSA provenance for the release
#   cosign.pub            the public key to verify everything offline (key mode)
#   digests.txt           the pinned digest of every image (the manifest of record)
#   VERIFY.md             the exact offline verification + mirror walkthrough
#
# Usage:
#   scripts/airgap-bundle.sh \
#     --version v1.2.3 \
#     --image docker.io/olivaresai/olivares:1.2.3-amd64 \
#     --chart deploy/helm/olivares \
#     --cosign-key cosign.key \
#     [--collector-image <ref>] [--out dist/airgap] [--gpg-key <id>]
#
# docker.io is the official registry; ghcr.io/olivaresai/olivares is the always-published
# fallback, identical by digest — either works as the --image source (ghcr.io does not
# rate-limit anonymous pulls of public images, which can matter on an unauthenticated
# build host).
#
# Requires: crane, cosign, helm, jq, tar. Optional: gpg (Helm-native .prov). No
# network is needed to VERIFY the result; building it pulls the pinned images once.
set -euo pipefail

VERSION=""
IMAGES=()
CHART=""
COSIGN_KEY=""
GPG_KEY=""
OUT="dist/airgap"
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --image) IMAGES+=("$2"); shift 2 ;;
    --collector-image) IMAGES+=("$2"); shift 2 ;;
    --chart) CHART="$2"; shift 2 ;;
    --cosign-key) COSIGN_KEY="$2"; shift 2 ;;
    --gpg-key) GPG_KEY="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    -h|--help) sed -n '2,38p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

for t in crane cosign helm jq tar; do command -v "$t" >/dev/null || { echo "error: $t not found"; exit 2; }; done
[ -n "$VERSION" ] || { echo "error: --version required"; exit 2; }
[ "${#IMAGES[@]}" -gt 0 ] || { echo "error: at least one --image required"; exit 2; }
[ -n "$COSIGN_KEY" ] && [ -f "$COSIGN_KEY" ] || { echo "error: --cosign-key <cosign.key> required"; exit 2; }

STAGE="${OUT}/olivares-airgap-${VERSION}"
rm -rf "$STAGE"; mkdir -p "$STAGE/images" "$STAGE/chart" "$STAGE/sbom" "$STAGE/vex" "$STAGE/prov"
: > "$STAGE/digests.txt"

# Ship the verification public key derived from the signing key (offline trust root).
cosign public-key --key "$COSIGN_KEY" > "$STAGE/cosign.pub" 2>/dev/null || cp "${COSIGN_KEY%.key}.pub" "$STAGE/cosign.pub"

echo "==> pinning + saving images by digest"
i=0
for img in "${IMAGES[@]}"; do
  # Resolve to an immutable digest reference.
  case "$img" in
    *@sha256:*) ref="$img" ;;
    *) digest="$(crane digest "$img")"; repo="${img%%:*}"; ref="${repo}@${digest}" ;;
  esac
  name="img$(printf '%02d' "$i")"
  echo "    $img  ->  $ref"
  echo "$ref" >> "$STAGE/digests.txt"
  # cosign save captures the image AND its signatures/attestations into a dir,
  # loadable + verifiable offline (cosign verify --local-image).
  cosign save "$ref" --dir "$STAGE/images/$name"
  echo "$ref" > "$STAGE/images/$name/ref.txt"
  i=$((i + 1))
done

echo "==> packaging + signing the Helm chart"
if [ -n "$CHART" ]; then
  helm lint "$CHART" >/dev/null
  helm package "$CHART" --destination "$STAGE/chart" >/dev/null
  TGZ="$(ls -1 "$STAGE/chart"/*.tgz | head -1)"
  # cosign over the chart tarball (key mode, air-gap; no Rekor). Offline-verifiable.
  cosign sign-blob --key "$COSIGN_KEY" --tlog-upload=false -y \
    --output-signature "${TGZ}.sig" "$TGZ" >/dev/null 2>&1
  # The signature is the whole point of the bundle: scripts/airgap-mirror.sh refuses to
  # push a chart without it. Assert the file exists rather than announcing it from the
  # fact that the command returned.
  [ -s "${TGZ}.sig" ] || { echo "error: cosign produced no ${TGZ##*/}.sig" >&2; exit 1; }
  echo "    chart: $(basename "$TGZ") (+ .sig)"
  # Optional Helm-native GPG provenance (.prov) — what `helm install --verify` checks.
  # Optional to REQUEST; not optional to obtain. `--sign` used to run with its output
  # and status discarded (`>/dev/null 2>&1 &&`, where set -e does not fire), and the
  # closing summary then advertised "signed (cosign + GPG .prov)" purely because
  # $GPG_KEY was non-empty — announcing provenance that was never written.
  if [ -n "$GPG_KEY" ]; then
    command -v gpg >/dev/null || { echo "error: --gpg-key given but gpg is not installed" >&2; exit 2; }
    helm package "$CHART" --sign --key "$GPG_KEY" --keyring "${GNUPGHOME:-${HOME:-/nonexistent-home}/.gnupg}/secring.gpg" \
      --destination "$STAGE/chart" >/dev/null 2>&1 || {
      echo "error: helm package --sign failed for GPG key '${GPG_KEY}'; no .prov was written." >&2
      echo "       A requested guarantee that could not be produced is not an optional extra." >&2
      exit 1
    }
    ls -1 "$STAGE/chart"/*.prov >/dev/null 2>&1 || {
      echo "error: helm package --sign returned success but wrote no .prov" >&2
      exit 1
    }
    echo "    chart .prov (GPG) written"
  fi
fi

# Gather any SBOM / VEX / provenance produced alongside the release (best-effort:
# the caller drops them next to the script, or passes a dist dir via env).
for f in ${OLIVARES_SBOM_FILES:-} ; do [ -f "$f" ] && cp "$f" "$STAGE/sbom/"; done
for f in ${OLIVARES_VEX_FILES:-} ; do [ -f "$f" ] && cp "$f" "$STAGE/vex/"; done
for f in ${OLIVARES_PROV_FILES:-} ; do [ -f "$f" ] && cp "$f" "$STAGE/prov/"; done

cat > "$STAGE/VERIFY.md" <<EOF
# Offline verification — Olivares AI ${VERSION}

NO network required. Verify, then mirror into your private registry.

## 1. Verify every image offline (no Rekor)
\`\`\`sh
for d in images/*/; do
  ref="\$(cat "\$d/ref.txt")"
  cosign verify --local-image "\$d" --insecure-ignore-tlog --key cosign.pub
done
\`\`\`
The pinned digests are in \`digests.txt\` — compare against what you mirror.

## 2. Verify the Helm chart offline
\`\`\`sh
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \\
  --signature chart/*.tgz.sig chart/*.tgz
# (if a .prov is present, additionally: helm verify chart/*.tgz  — needs the GPG pubkey in your keyring)
\`\`\`

## 3. Mirror into your private registry + install
\`\`\`sh
scripts/airgap-mirror.sh --bundle olivares-airgap-${VERSION}.tar.gz --registry registry.internal:5000
helm install olivares oci://registry.internal:5000/charts/olivares --version <chart-version> \\
  --set image.repository=registry.internal:5000/olivares \\
  --set image.digest=<digest-from-digests.txt>
\`\`\`
Deploy by DIGEST, never by tag.
EOF

cp scripts/airgap-mirror.sh "$STAGE/" 2>/dev/null || true
cp scripts/verify-release.sh "$STAGE/" 2>/dev/null || true

TARBALL="${OUT}/olivares-airgap-${VERSION}.tar.gz"
tar -czf "$TARBALL" -C "$OUT" "olivares-airgap-${VERSION}"
echo "==> air-gap bundle: $TARBALL"
echo "    images: ${#IMAGES[@]} (pinned by digest in digests.txt)"
# Read the state off DISK, not off the flags that were requested. The line used to be
# derived from $CHART and $GPG_KEY being non-empty, so it described what was ASKED FOR.
chart_state="(none)"
if [ -n "$CHART" ]; then
	chart_state="packaged"
	[ -s "${TGZ:-/nonexistent}.sig" ] && chart_state="signed (cosign)"
	ls -1 "$STAGE/chart"/*.prov >/dev/null 2>&1 && chart_state="${chart_state} + GPG .prov"
fi
echo "    chart:  ${chart_state}"
echo "OK — carry $TARBALL across the gap; verify with the steps in VERIFY.md."
