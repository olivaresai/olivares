#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Import an air-gap bundle (scripts/airgap-bundle.sh) into a private registry and
# verify it offline (SCP-06). Run this INSIDE the air-gapped environment. It loads
# each cosign-saved image into your registry by digest, verifies the offline cosign
# signatures (no Rekor), pushes the signed Helm chart, and prints the exact
# digest-pinned `helm install` to run.
#
# Usage:
#   scripts/airgap-mirror.sh --bundle olivares-airgap-v1.2.3.tar.gz --registry registry.internal:5000 [--insecure]
#
# Requires: cosign, crane, helm, tar. No internet; talks only to your registry.
set -euo pipefail

BUNDLE=""
REGISTRY=""
INSECURE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --bundle) BUNDLE="$2"; shift 2 ;;
    --registry) REGISTRY="$2"; shift 2 ;;
    --insecure) INSECURE="--insecure"; shift ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
for t in cosign crane helm tar; do command -v "$t" >/dev/null || { echo "error: $t not found"; exit 2; }; done
[ -n "$BUNDLE" ] && [ -f "$BUNDLE" ] || { echo "error: --bundle <tarball> required"; exit 2; }
[ -n "$REGISTRY" ] || { echo "error: --registry host[:port] required"; exit 2; }

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
tar -xzf "$BUNDLE" -C "$WORK"
ROOT="$(ls -d "$WORK"/olivares-airgap-* | head -1)"
[ -d "$ROOT" ] || { echo "error: bundle layout unexpected"; exit 2; }
PUB="$ROOT/cosign.pub"
[ -f "$PUB" ] || { echo "error: cosign.pub missing from bundle"; exit 2; }

echo "==> 1/3 verifying images offline (no Rekor) + loading into $REGISTRY"
# digests.txt is the bundle's manifest of record (scripts/airgap-bundle.sh writes one
# line per image). The mirror must import EXACTLY that set: an images/ directory that
# lost entries — or a bundle whose layout changed under us — used to leave DIRS empty,
# skip the verification loop entirely, and still reach "OK — mirrored + verified
# offline". Nothing verified, everything approved.
[ -f "$ROOT/digests.txt" ] || { echo "error: digests.txt missing from bundle — nothing to verify against" >&2; exit 2; }
EXPECTED="$(grep -c . "$ROOT/digests.txt" || true)"
DIRS=()
while IFS= read -r d; do [ -n "$d" ] && DIRS+=("$d"); done < <(ls -d "$ROOT"/images/*/ 2>/dev/null || true)
if [ "${#DIRS[@]}" -eq 0 ]; then
  echo "error: the bundle contains no images/ entries; refusing to report a verified mirror" >&2
  exit 2
fi
if [ "${#DIRS[@]}" -ne "$EXPECTED" ]; then
  echo "error: bundle declares ${EXPECTED} image(s) in digests.txt but carries ${#DIRS[@]} under images/;" >&2
  echo "       the manifest and the payload disagree — refusing to mirror" >&2
  exit 2
fi
for d in "${DIRS[@]}"; do
  ref="$(cat "$d/ref.txt")"                      # repo@sha256:...
  repo="${ref%@*}"; digest="${ref#*@}"
  base="$(basename "$repo")"
  dest="${REGISTRY}/${base}"
  echo "    verify (offline): $ref"
  cosign verify --local-image "$d" --insecure-ignore-tlog --key "$PUB" >/dev/null
  echo "    load -> ${dest}:${digest#sha256:}"
  # cosign load pushes the saved image (and its signatures) into the registry.
  cosign load --dir "$d" "${dest}:imported" >/dev/null 2>&1 || crane push "$d" "${dest}:imported" $INSECURE
  # Re-pin: confirm the digest survived the mirror unchanged. A digest that CHANGED is
  # the exact event the signature exists to detect — a different image now sitting at
  # the reference we are about to tell an operator to install. It used to print
  # "WARN: digest changed" and carry on to "OK — mirrored + verified offline", exit 0.
  got="$(crane digest "${dest}:imported" $INSECURE)"
  if [ "$got" != "$digest" ]; then
    echo "error: digest changed while mirroring ${ref}" >&2
    echo "       expected ${digest}" >&2
    echo "       got      ${got}" >&2
    echo "       the mirrored image is NOT the signed image; refusing to continue" >&2
    exit 1
  fi
  echo "    digest preserved: $got"
done

echo "==> 2/3 verifying + pushing the Helm chart"
# Every bundle carries chart/<name>.tgz and its .tgz.sig (scripts/airgap-bundle.sh).
# Both used to be optional HERE, and the two omissions compose into a hole: the bundler
# signs with `>/dev/null 2>&1` and no status check, so a failed signing produces a
# bundle with no .sig; this script then skipped verification because there was no .sig,
# pushed the chart anyway, and closed with "OK — mirrored + verified offline". The
# ABSENCE of a signature read as success. In an air-gapped install the chart is the
# thing that decides what runs, so both absences are now refusals.
TGZ="$(ls -1 "$ROOT"/chart/*.tgz 2>/dev/null | head -1 || true)"
if [ -z "${TGZ:-}" ]; then
  echo "error: the bundle carries no chart/*.tgz; refusing to report a verified mirror" >&2
  exit 2
fi
if [ ! -f "${TGZ}.sig" ]; then
  echo "error: ${TGZ##*/} has no .sig in the bundle — it was never signed, or the signature" >&2
  echo "       was stripped. An unsigned chart is not verifiable offline; refusing to push it." >&2
  exit 1
fi
cosign verify-blob --key "$PUB" --insecure-ignore-tlog --signature "${TGZ}.sig" "$TGZ" >/dev/null
echo "    chart cosign signature OK (offline)"
# `|| true` here turned a 401, a full disk or a registry that was not there into a
# success line in the log, followed by the OK banner.
if ! helm push "$TGZ" "oci://${REGISTRY}/charts" $INSECURE 2>&1 | sed 's/^/    /'; then
  echo "error: helm push failed; the chart is NOT in ${REGISTRY}" >&2
  exit 1
fi

echo "==> 3/3 done. Install by DIGEST (never tag):"
echo "    digests of record:"; sed 's/^/      /' "$ROOT/digests.txt"
echo "    helm install olivares oci://${REGISTRY}/charts/olivares --version <chart-version> \\"
echo "      --set image.repository=${REGISTRY}/olivares --set image.digest=<digest>"
echo "OK — mirrored + verified offline."
