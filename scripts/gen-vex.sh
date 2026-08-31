#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Generate an OpenVEX document for a release, driven by govulncheck's call-graph
# REACHABILITY (SCP-04). Go's govulncheck emits OpenVEX natively (-format openvex
# since x/vuln v1.1.2); a dependency CVE that is imported but NOT reachable in the
# call graph becomes status=not_affected with justification
# vulnerable_code_not_in_execute_path — the strongest, evidence-based "not
# affected" because it is real reachability analysis, not a human assertion.
#
# The VEX is then signed + attached as a cosign attestation next to the SBOM in
# the release workflow (cosign attest --type openvex <image@digest>), and
# REPUBLISHED on every scheduled rebuild (patch-velocity, SCP-11) so a buyer's
# scanner suppresses the noise of unreachable transitive CVEs.
#
# Usage:
#   scripts/gen-vex.sh [--mode source|binary] [--target <pkgs|binary>] [--out vex.openvex.json] [--merge extra.vex.json ...]
#     --mode source  (default): analyze the release module's call graph (strongest).
#     --mode binary           : analyze a built binary's symbol table (faithful to
#                               the shipped artifact; coarser than source mode).
#     --target  : source mode => package pattern (default ./cmd/olivares/...);
#                 binary mode => path to the built binary.
#     --merge   : extra hand-authored vexctl statements to merge in (e.g. a fix you
#                 applied, or an inline mitigation govulncheck can't infer).
set -euo pipefail

MODE="source"
TARGET=""
DIR=""
OUT="vex.openvex.json"
AUTHOR="${OLIVARES_VEX_AUTHOR:-Olivares AI <security@olivares.ai>}"
MERGE=()
while [ $# -gt 0 ]; do
  case "$1" in
    --mode) MODE="$2"; shift 2 ;;
    --target) TARGET="$2"; shift 2 ;;
    --dir) DIR="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --author) AUTHOR="$2"; shift 2 ;;
    --merge) MERGE+=("$2"); shift 2 ;;
    -h|--help) sed -n '2,34p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

command -v govulncheck >/dev/null || { echo "error: govulncheck not found (go install golang.org/x/vuln/cmd/govulncheck@latest)"; exit 2; }
command -v jq >/dev/null || { echo "error: jq not found"; exit 2; }

echo "==> govulncheck ($MODE mode) -> OpenVEX"
# Absolute output path so a `cd` into a module dir (source mode) still writes it
# where the caller expects.
case "$OUT" in /*) : ;; *) OUT="$(pwd)/$OUT" ;; esac

# govulncheck exits non-zero when it FINDS reachable vulns; for VEX generation we
# want the document regardless, so capture status and keep the output.
set +e
if [ "$MODE" = "binary" ]; then
  [ -n "$TARGET" ] || { echo "error: --mode binary requires --target <binary>"; exit 2; }
  govulncheck -format openvex -mode binary "$TARGET" > "$OUT.tmp"
else
  # Source mode needs a module context. The repo is a go.work workspace, so run
  # inside the module that builds the release artifact (default cmd/olivares);
  # govulncheck then analyzes that binary's full call graph across linked modules.
  DIR="${DIR:-cmd/olivares}"
  TARGET="${TARGET:-./...}"
  ( cd "$DIR" && govulncheck -format openvex "$TARGET" ) > "$OUT.tmp"
fi
gv=$?
set -e
[ -s "$OUT.tmp" ] || { echo "error: govulncheck produced no output (exit $gv)"; rm -f "$OUT.tmp"; exit 1; }

# Validate it really is OpenVEX before we trust/sign it.
CTX=$(jq -r '."@context" // empty' "$OUT.tmp")
case "$CTX" in
  https://openvex.dev/ns/*) : ;;
  *) echo "error: output is not an OpenVEX document (@context=$CTX)"; rm -f "$OUT.tmp"; exit 1 ;;
esac
# A CLEAN scan (no reachable vulns) emits NO statements key at all — that is a
# valid, and in fact the BEST, result. Normalize it to an explicit empty array so
# the document is well-formed and downstream tooling/attestation is uniform.
if ! jq -e '.statements | type == "array"' "$OUT.tmp" >/dev/null 2>&1; then
  jq '. + {statements: (.statements // [])}' "$OUT.tmp" > "$OUT.tmp2" && mv "$OUT.tmp2" "$OUT.tmp"
fi

# govulncheck stamps author "Unknown Author"; this VEX is OUR assertion — attribute it.
jq --arg a "$AUTHOR" '.author = $a' "$OUT.tmp" > "$OUT.tmp2" && mv "$OUT.tmp2" "$OUT.tmp"

mv "$OUT.tmp" "$OUT"

# Merge any hand-authored supplemental statements (fixes / inline mitigations).
if [ "${#MERGE[@]}" -gt 0 ]; then
  if command -v vexctl >/dev/null; then
    echo "==> merging ${#MERGE[@]} supplemental VEX doc(s) with vexctl"
    vexctl merge "$OUT" "${MERGE[@]}" > "$OUT.merged" && mv "$OUT.merged" "$OUT"
  else
    echo "warn: vexctl not found; skipping --merge (install github.com/openvex/vexctl)" >&2
  fi
fi

# Summary by status (the openvex.dev/ns/* enum: not_affected|affected|fixed|under_investigation).
echo "==> OpenVEX written: $OUT"
echo "    @context : $(jq -r '."@context"' "$OUT")"
echo "    author   : $(jq -r '.author // "?"' "$OUT")"
echo "    statements by status:"
jq -r '[.statements[]?.status] | group_by(.) | map({(.[0]): length}) | add // {} | to_entries[] | "      \(.key): \(.value)"' "$OUT" 2>/dev/null || echo "      (none)"
echo "    not_affected justifications:"
jq -r '[.statements[]? | select(.status=="not_affected") | .justification] | group_by(.) | map({(.[0]): length}) | add // {} | to_entries[] | "      \(.key): \(.value)"' "$OUT" 2>/dev/null || true
echo "OK"
