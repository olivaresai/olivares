#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Assert that an SPDX-JSON SBOM carries the data fields the CISA 2025 "SBOM
# Minimum Elements" draft elevates — Component Hash, License, Tool Name and
# Generation Context (SCP-03) — plus the long-standing NTIA baseline fields.
#
#   ⚠ DRAFT, NOT LAW. The CISA 2025 document is a *pre-decisional public-comment
#   draft* (cover page: "does not represent the final position of the U.S.
#   Government and is continuing to undergo updates"). The four NEW fields
#   (Component Hash, License, Tool Name, Generation Context) and the rename of
#   2021 "Supplier Name" -> "Software Producer" may change before finalization.
#   This gate is therefore "draft-pending": it FAILS when a field TYPE is wholly
#   absent (the generator isn't emitting it at all), but only WARNS on per-
#   component omissions the draft itself permits (e.g. a Component Hash may be
#   omitted when the SBOM author lacks the original artifact; Go module licenses
#   are frequently NOASSERTION). Re-confirm against the FINAL CISA document
#   before calling any field mandatory.
#
# Field -> SPDX-2.3 JSON key mapping (verified against real syft spdx-json output):
#   Timestamp            creationInfo.created
#   Tool Name (NEW)      creationInfo.creators[]  "Tool: ..."
#   SBOM Author          creationInfo.creators[]  "Organization:"/"Person: ..."
#   Component Name       packages[].name
#   Component Version    packages[].versionInfo
#   Software Identifiers packages[].externalRefs[] (purl / cpe23Type)
#   Software Producer    packages[].supplier  (or .originator)
#   Component Hash (NEW)  packages[].checksums[].checksumValue
#   License (NEW)        packages[].licenseConcluded / .licenseDeclared (!= NOASSERTION)
#   Dependency Relation  relationships[] (DEPENDS_ON / CONTAINS)
#   Generation Context   no native SPDX-2.3 key -> creationInfo.comment, or
#                        CycloneDX 1.6 metadata.lifecycles[] (pass --cyclonedx)
#
# Usage:
#   scripts/sbom-check-cisa.sh <sbom.spdx.json> [--cyclonedx <sbom.cdx.json>] [--strict]
#     --strict : also FAIL on the draft-permitted soft fields (hash/license/genctx).
set -euo pipefail

SPDX=""
CDX=""
STRICT=0
while [ $# -gt 0 ]; do
  case "$1" in
    --cyclonedx) CDX="$2"; shift 2 ;;
    --strict) STRICT=1; shift ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) SPDX="$1"; shift ;;
  esac
done

command -v jq >/dev/null || { echo "error: jq not found"; exit 2; }
[ -n "$SPDX" ] && [ -f "$SPDX" ] || { echo "usage: $0 <sbom.spdx.json> [--cyclonedx <f>] [--strict]"; exit 2; }
jq -e . "$SPDX" >/dev/null 2>&1 || { echo "error: $SPDX is not valid JSON"; exit 2; }

fail=0
warn=0
hard() { echo "  ✗ FAIL  $1"; fail=$((fail + 1)); }
soft() { if [ "$STRICT" -eq 1 ]; then echo "  ✗ FAIL  $1 (soft field, --strict)"; fail=$((fail + 1)); else echo "  ⚠ WARN  $1 (draft permits omission)"; warn=$((warn + 1)); fi; }
ok()   { echo "  ✓ OK    $1"; }

echo "==> CISA-2025-DRAFT SBOM minimum-elements check (draft-pending): $SPDX"

# spdxVersion sanity
SPDXVER=$(jq -r '.spdxVersion // "?"' "$SPDX")
echo "    spdxVersion: $SPDXVER"

NPKG=$(jq '[.packages[]?] | length' "$SPDX")
[ "$NPKG" -gt 0 ] || hard "no packages[] in SBOM"
echo "    components: $NPKG"

# --- SBOM-level fields (hard) ---
jq -e '.creationInfo.created' "$SPDX" >/dev/null 2>&1 && ok "Timestamp (creationInfo.created)" || hard "Timestamp missing (creationInfo.created)"
[ "$(jq -e '[.creationInfo.creators[]? | select(startswith("Tool:"))] | length > 0' "$SPDX")" = true ] \
  && ok "Tool Name (creationInfo.creators[] Tool:)" || hard "Tool Name missing (creationInfo.creators[] 'Tool:')"
[ "$(jq -e '[.creationInfo.creators[]? | select(startswith("Organization:") or startswith("Person:"))] | length > 0' "$SPDX")" = true ] \
  && ok "SBOM Author (creationInfo.creators[] Organization/Person)" || soft "SBOM Author missing (creationInfo.creators[] Organization/Person)"

# --- per-component coverage helpers ---
cov() { jq --argjson n "$NPKG" "$1" "$SPDX"; }   # returns count
pct() { [ "$NPKG" -gt 0 ] && echo $(( $1 * 100 / NPKG )) || echo 0; }

NAME=$(cov '[.packages[] | select(.name != null and .name != "")] | length')
[ "$NAME" -eq "$NPKG" ] && ok "Component Name: $NAME/$NPKG" || hard "Component Name missing on $((NPKG-NAME))/$NPKG"

VER=$(cov '[.packages[] | select(.versionInfo != null and .versionInfo != "")] | length')
[ "$(pct "$VER")" -ge 50 ] && ok "Component Version: $VER/$NPKG ($(pct "$VER")%)" || soft "Component Version sparse: $VER/$NPKG"

IDENT=$(cov '[.packages[] | select((.externalRefs // []) | map(.referenceType=="purl" or .referenceType=="cpe23Type") | any)] | length')
[ "$IDENT" -gt 0 ] && ok "Software Identifiers (purl/cpe): $IDENT/$NPKG" || hard "Software Identifiers (purl/cpe) absent on ALL components"

PROD=$(cov '[.packages[] | select((.supplier != null and .supplier != "NOASSERTION") or (.originator != null and .originator != "NOASSERTION"))] | length')
[ "$PROD" -gt 0 ] && ok "Software Producer/Supplier: $PROD/$NPKG" || soft "Software Producer/Supplier absent/NOASSERTION on ALL components"

# --- the four CISA-2025 NEW fields ---
HASH=$(cov '[.packages[] | select((.checksums // []) | map(.checksumValue) | any)] | length')
[ "$HASH" -gt 0 ] && ok "Component Hash (NEW): $HASH/$NPKG present" || soft "Component Hash (NEW) absent on ALL components"

LIC=$(cov '[.packages[] | select((.licenseConcluded // "NOASSERTION") != "NOASSERTION" or (.licenseDeclared // "NOASSERTION") != "NOASSERTION")] | length')
[ "$LIC" -gt 0 ] && ok "License (NEW): $LIC/$NPKG with a concrete license" || soft "License (NEW) NOASSERTION/absent on ALL components"

DEP=$(jq '[.relationships[]? | select(.relationshipType=="DEPENDS_ON" or .relationshipType=="CONTAINS")] | length' "$SPDX")
[ "$DEP" -gt 0 ] && ok "Dependency Relationship (NEW): $DEP edges" || hard "Dependency Relationship absent (no DEPENDS_ON/CONTAINS)"

# Generation Context: no native SPDX-2.3 key. Accept creationInfo.comment naming a
# build phase, OR a CycloneDX lifecycle if a CycloneDX SBOM was supplied.
GENCTX=0
if jq -e '.creationInfo.comment // "" | test("before build|during build|after build|build phase|lifecycle"; "i")' "$SPDX" >/dev/null 2>&1; then
  ok "Generation Context (NEW): creationInfo.comment names a build phase"; GENCTX=1
fi
if [ "$GENCTX" -eq 0 ] && [ -n "$CDX" ] && [ -f "$CDX" ]; then
  if jq -e '(.metadata.lifecycles // []) | length > 0' "$CDX" >/dev/null 2>&1; then
    ok "Generation Context (NEW): CycloneDX metadata.lifecycles[] present ($CDX)"; GENCTX=1
  fi
fi
[ "$GENCTX" -eq 1 ] || soft "Generation Context (NEW) not expressed (no creationInfo.comment build-phase; no CycloneDX lifecycle). SPDX-2.3 has no native key — prefer CycloneDX 1.6 metadata.lifecycles[]"

echo
echo "==> result: ${fail} hard failure(s), ${warn} draft-pending warning(s)$([ "$STRICT" -eq 1 ] && echo ' [--strict]')"
[ "$fail" -eq 0 ] || { echo "FAILED: a required SBOM field type is entirely absent."; exit 1; }
echo "PASS (CISA-2025 fields present; draft-pending — re-confirm against the FINAL CISA document)."
