#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# OpenSCAP (OSCAP) STIG-evaluation harness for the STIG-hardened image (SCP-09).
#
# This RUNS a scan against the AUTHORITATIVE upstream DISA STIG SCAP content
# (ComplianceAsCode / scap-security-guide, shipped by the base OS). It does NOT
# ship, embed, or fabricate a passing result — it gives you the harness to PRODUCE
# the evidence yourself, so the report you read is one you generated, not one we
# pre-baked. See oscap/README.md and docs/SCP-09-FIPS-STIG.md.
#
# Two modes:
#   --image <ref>   scan a built image's rootfs offline (oscap-podman / atomic scan)
#   --host          scan the running host/OS directly (oscap xccdf eval)
#
# Profile + datastream default to the DISA STIG profile from the OS's installed
# scap-security-guide. Override with --profile / --datastream for a pinned version
# or a tailoring file (see oscap/tailoring.xml).
#
# Requires: openscap-scanner (`oscap`). For --image also: openscap-utils
# (`oscap-podman`) or `podman`/`docker`. Install on the SCANNING host, not in the
# product image (the image stays lean; the scanner is operator-side tooling).
#
# Usage:
#   oscap/scan.sh --host
#   oscap/scan.sh --image olivares:stig
#   oscap/scan.sh --image olivares:stig \
#       --profile xccdf_org.ssgproject.content_profile_stig \
#       --datastream /usr/share/xml/scap/ssg/content/ssg-rhel9-ds.xml \
#       --tailoring oscap/tailoring.xml
set -euo pipefail

MODE=""
IMAGE=""
# Default profile id is the upstream ComplianceAsCode DISA STIG profile id for RHEL9
# (the UBI base). `oscap info <datastream>` lists every available profile id.
PROFILE="${OSCAP_PROFILE:-xccdf_org.ssgproject.content_profile_stig}"
# The datastream that ships with scap-security-guide on the scanning host. RHEL/UBI9:
DATASTREAM="${OSCAP_DATASTREAM:-/usr/share/xml/scap/ssg/content/ssg-rhel9-ds.xml}"
TAILORING="${OSCAP_TAILORING:-}"
OUTDIR="${OSCAP_OUTDIR:-./oscap-results}"

usage() { sed -n '2,40p' "$0"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --image) MODE="image"; IMAGE="$2"; shift 2 ;;
    --host) MODE="host"; shift ;;
    --profile) PROFILE="$2"; shift 2 ;;
    --datastream) DATASTREAM="$2"; shift 2 ;;
    --tailoring) TAILORING="$2"; shift 2 ;;
    --outdir) OUTDIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 2 ;;
  esac
done

[ -n "$MODE" ] || { echo "error: pass --host or --image <ref>" >&2; usage; exit 2; }
command -v oscap >/dev/null 2>&1 || {
  echo "error: 'oscap' not found. Install openscap-scanner (dnf install openscap-scanner scap-security-guide)." >&2
  exit 1
}
[ -f "$DATASTREAM" ] || {
  echo "error: SCAP datastream not found at: $DATASTREAM" >&2
  echo "       Install scap-security-guide, or pass --datastream <path>. List profiles with:" >&2
  echo "         oscap info <datastream>" >&2
  exit 1
}

mkdir -p "$OUTDIR"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
RESULTS="$OUTDIR/results-$STAMP.xml"
REPORT="$OUTDIR/report-$STAMP.html"
ARF="$OUTDIR/arf-$STAMP.xml"

# Common eval args. --fetch-remote-resources is intentionally OMITTED (offline,
# auditable). A non-zero oscap exit means "some rules failed" — that is a legitimate
# scan outcome, NOT a harness error, so we capture it and surface the report path.
EVAL_ARGS=(xccdf eval --profile "$PROFILE")
[ -n "$TAILORING" ] && { [ -f "$TAILORING" ] || { echo "error: tailoring file not found: $TAILORING" >&2; exit 1; }; EVAL_ARGS+=(--tailoring-file "$TAILORING"); }
EVAL_ARGS+=(--results "$RESULTS" --results-arf "$ARF" --report "$REPORT" "$DATASTREAM")

echo "==> OSCAP STIG evaluation"
echo "    mode:       $MODE${IMAGE:+ ($IMAGE)}"
echo "    profile:    $PROFILE"
echo "    datastream: $DATASTREAM"
[ -n "$TAILORING" ] && echo "    tailoring:  $TAILORING"
echo "    report:     $REPORT"
echo

set +e
if [ "$MODE" = "host" ]; then
  oscap "${EVAL_ARGS[@]}"
  rc=$?
else
  if command -v oscap-podman >/dev/null 2>&1; then
    oscap-podman "$IMAGE" "${EVAL_ARGS[@]}"
    rc=$?
  else
    echo "note: 'oscap-podman' not found — falling back to mounting the image rootfs." >&2
    runtime=""
    command -v podman >/dev/null 2>&1 && runtime=podman
    [ -z "$runtime" ] && command -v docker >/dev/null 2>&1 && runtime=docker
    [ -n "$runtime" ] || { echo "error: need oscap-podman, or podman/docker to mount the image." >&2; exit 1; }
    cid="$($runtime create "$IMAGE")"
    root="$(mktemp -d)"
    trap '$runtime rm -f "$cid" >/dev/null 2>&1; rm -rf "$root"' EXIT
    $runtime export "$cid" | tar -C "$root" -xf -
    # oscap-chroot evaluates an unpacked rootfs offline.
    if command -v oscap-chroot >/dev/null 2>&1; then
      oscap-chroot "$root" "${EVAL_ARGS[@]}"
      rc=$?
    else
      echo "error: oscap-chroot not found (package openscap-utils) — cannot scan a mounted rootfs." >&2
      exit 1
    fi
  fi
fi
set -e

echo
case "$rc" in
  0) echo "✅ oscap: all selected rules PASSED — report: $REPORT" ;;
  2) echo "⚠️  oscap: completed, SOME rules FAILED (expected for an un-remediated host) — review: $REPORT" ;;
  *) echo "❌ oscap: scanner error (exit $rc) — not a compliance result. Check args/datastream." >&2; exit "$rc" ;;
esac
echo "   results: $RESULTS"
echo "   arf:     $ARF"
echo
echo "Generate a remediation script from the results (does NOT auto-apply):"
echo "  oscap xccdf generate fix --fix-type bash --profile $PROFILE --result-id '' $ARF > remediate.sh"
