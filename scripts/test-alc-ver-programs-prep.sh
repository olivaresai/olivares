#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2; pwd)"
CHECK="$ROOT/scripts/check-alc-ver-programs-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/alcverprep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/alc-ver-programs-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/ALC-VER-PROGRAMS-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/design/PROGRAMA-ALC-FIABILIDAD-CLOUD-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/design/ALC-03-WORM-LIVE-CRITERIOS-2026-08-18.md" "$TMP/tree/design/"
  cp "$ROOT/design/VER-ALC-MEDIDO-2026-08-18.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-alc-ver-programs-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$ROOT" \
    OLIVARES_ALCVERP_JSON="$TMP/tree/design/alc-ver-programs-prep-2026-08-20.json" \
    OLIVARES_ALCVERP_DOC="$TMP/tree/design/ALC-VER-PROGRAMS-PREP-2026-08-20.md" \
    OLIVARES_ALCVERP_SCIM="$TMP/tree/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md" \
    OLIVARES_ALCVERP_FIA="$TMP/tree/design/PROGRAMA-ALC-FIABILIDAD-CLOUD-2026-08-18.md" \
    OLIVARES_ALCVERP_WORM="$TMP/tree/design/ALC-03-WORM-LIVE-CRITERIOS-2026-08-18.md" \
    OLIVARES_ALCVERP_MED="$TMP/tree/design/VER-ALC-MEDIDO-2026-08-18.md" \
    OLIVARES_ALCVERP_CTR19="$TMP/tree/design/ALC-01-S2-MANAGED-SCIM-CONTRACT-2026-08-19.md" \
    OLIVARES_ALCVERP_ORIG="$TMP/tree/scripts/check-alc-ver-programs.sh" \
    bash "$CHECK" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe ALC/VER pin is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/alc-ver-programs-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["remainder_applied"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (remainder-applied) is killed"
else bad "remainder-applied stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '# SPDX-FileCopyrightText: 2026 Olivares.AI\n# leftover contract from #956\n' > \
  "$TMP/tree/design/ALC-01-S2-MANAGED-SCIM-CONTRACT-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (2026-08-19 contract landed) is killed"
else bad "2026-08-19 contract stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/alc-ver-programs-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["overlay_remeasured_in_this_gate"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (overlay remasure leaked) is killed"
else bad "overlay remasure stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# B2 counterfactual: the old checker accepted one keyword per document as the complete
# program/measurement contract. These are readable Markdown files, so LOOK (2) would be wrong;
# semantically empty evidence must be a finding (1).
stage
python3 - \
  "$TMP/tree/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md" \
  "$TMP/tree/design/PROGRAMA-ALC-FIABILIDAD-CLOUD-2026-08-18.md" \
  "$TMP/tree/design/ALC-03-WORM-LIVE-CRITERIOS-2026-08-18.md" \
  "$TMP/tree/design/VER-ALC-MEDIDO-2026-08-18.md" <<'PY'
from pathlib import Path
import sys

for path, text in zip(sys.argv[1:], ("SCIM\n", "DR\n", "LOCKED\n", "NO HE PODIDO MIRAR\n")):
    Path(path).write_text(text, encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (four keyword-only evidence docs) is killed"
else bad "keyword-only docs stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# Keeping the cautious phrase cannot excuse an incompatible productive-success claim beside it.
stage
python3 - "$TMP/tree/design/VER-ALC-MEDIDO-2026-08-18.md" <<'PY'
from pathlib import Path
import re, sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
section = re.search(r"^##\s+VER-09\b.*?(?=^##\s+|\Z)", text, flags=re.I | re.M | re.S)
if section is None:
    raise SystemExit("live VER-09 section not found")
p.write_text(
    text[:section.end()]
    + "\nVER-09 productive buy-to-bytes path in the live environment\n"
    + "verified successfully.\n"
    + text[section.end():],
    encoding="utf-8",
)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -F -q 'productive-success' "$TMP/err"; then
  ok "mutant (full VER-09 plus productive-success contradiction) is killed specifically"
else
  bad "productive contradiction did not reach its detector rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/VER-ALC-MEDIDO-2026-08-18.md" <<'PY'
from pathlib import Path
import re, sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
section = re.search(r"^##\s+VER-09\b.*?(?=^##\s+|\Z)", text, flags=re.I | re.M | re.S)
if section is None:
    raise SystemExit("live VER-09 section not found")
p.write_text(
    text[:section.end()]
    + "\nProductive buy-to-bytes path verified successfully in production; no caveats.\n"
    + text[section.end():],
    encoding="utf-8",
)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ] && grep -F -q 'productive-success' "$TMP/err"; then
  ok "mutant (productive success plus no-caveats decoy) is killed specifically"
else
  bad "no-caveats contradiction survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

# Render-invisible table rows are not material program evidence.
stage
python3 - \
  "$TMP/tree/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md" \
  "$TMP/tree/design/PROGRAMA-ALC-FIABILIDAD-CLOUD-2026-08-18.md" \
  "$TMP/tree/design/ALC-03-WORM-LIVE-CRITERIOS-2026-08-18.md" <<'PY'
from pathlib import Path
import sys

groups = (
    {f"ALC-01-S{i}" for i in range(1, 5)},
    {f"ALC-02-F{i}" for i in range(1, 5)},
    {f"W-{i}" for i in range(1, 5)},
)
for raw, wanted in zip(sys.argv[1:], groups):
    path = Path(raw)
    out = []
    seen = set()
    for line in path.read_text(encoding="utf-8").splitlines():
        cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
        if cells and cells[0] in wanted:
            out.extend(("<!--", line, "-->"))
            seen.add(cells[0])
        else:
            out.append(line)
    if seen != wanted:
        raise SystemExit(f"could not hide all rows in {path}")
    path.write_text("\n".join(out) + "\n", encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "mutant (program rows hidden in Markdown comments) is killed"
else
  bad "comment-hidden rows stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - \
  "$TMP/tree/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md" \
  "$TMP/tree/design/PROGRAMA-ALC-FIABILIDAD-CLOUD-2026-08-18.md" \
  "$TMP/tree/design/ALC-03-WORM-LIVE-CRITERIOS-2026-08-18.md" <<'PY'
from pathlib import Path
import re, sys

patterns = (r"^\| ALC-01-S1 \|", r"^\| ALC-02-F1 \|", r"^\| W-1 \|")
for raw, pattern in zip(sys.argv[1:], patterns):
    path = Path(raw)
    text = path.read_text(encoding="utf-8")
    match = re.search(pattern, text, flags=re.M)
    if match is None:
        raise SystemExit(f"could not locate first row in {path}")
    path.write_text(text[:match.start()] + "<!--\n" + text[match.start():], encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "mutant (rows hidden by unterminated Markdown comments) is killed"
else
  bad "unterminated comments stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

# The baseline is an identity, not merely forty hexadecimal characters.
stage
python3 - "$TMP/tree/design/alc-ver-programs-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["hub"] = "0" * 40
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (forty-zero baseline) is killed"
else bad "forty-zero baseline stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/alc-ver-programs-prep-2026-08-20.json" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
p.write_text(
    text.replace("{", '{"hub":"0000000000000000000000000000000000000000",', 1),
    encoding="utf-8",
)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (duplicate JSON hub) is killed"
else bad "duplicate JSON hub stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '%s\n' \
  'Conflicting attribution: Remasured on hub `0000000000000000000000000000000000000000`.' \
  >>"$TMP/tree/design/ALC-VER-PROGRAMS-PREP-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (second visible baseline) is killed"
else bad "duplicate baseline stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# Two mutually consistent assertions still cannot replace the baseline derived from Git history.
stage
python3 - \
  "$TMP/tree/design/alc-ver-programs-prep-2026-08-20.json" \
  "$TMP/tree/design/ALC-VER-PROGRAMS-PREP-2026-08-20.md" <<'PY'
from pathlib import Path
import json, re, sys

json_path = Path(sys.argv[1])
data = json.loads(json_path.read_text(encoding="utf-8"))
data["hub"] = "0" * 40
json_path.write_text(json.dumps(data), encoding="utf-8")
doc_path = Path(sys.argv[2])
doc_path.write_text(re.sub(r"Remasured on hub `[0-9a-f]{40}`", "Remasured on hub `" + "0" * 40 + "`", doc_path.read_text(encoding="utf-8")), encoding="utf-8")
PY
OLIVARES_ALCVERP_DERIVED_HUB="0000000000000000000000000000000000000000" run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc and JSON share false baseline) is killed"
else bad "mutually false baseline stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# Semantic no-fire: JSON property order/indentation and Markdown table whitespace are not evidence.
stage
python3 - \
  "$TMP/tree/design/alc-ver-programs-prep-2026-08-20.json" \
  "$TMP/tree/design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md" \
  "$TMP/tree/design/PROGRAMA-ALC-FIABILIDAD-CLOUD-2026-08-18.md" \
  "$TMP/tree/design/ALC-03-WORM-LIVE-CRITERIOS-2026-08-18.md" <<'PY'
from pathlib import Path
import json, sys

json_path = Path(sys.argv[1])
data = json.loads(json_path.read_text(encoding="utf-8"))
json_path.write_text(json.dumps(data, indent=4, sort_keys=True) + "\n", encoding="utf-8")
for raw in sys.argv[2:]:
    path = Path(raw)
    lines = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.strip().startswith("|") and line.strip().endswith("|"):
            cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
            line = "|  " + "  |  ".join(cells) + "  |"
        lines.append(line)
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: semantic Markdown/JSON formatting stays CLEAN"
else bad "semantic formatting should stay CLEAN ($(cat "$TMP/err"))"; fi

stage
printf '%s\n' '<!-- remainder applied on origin/main -->' \
  >>"$TMP/tree/design/ALC-VER-PROGRAMS-PREP-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: hidden Markdown comment has no semantic authority"
else bad "hidden comment fired rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/alc-ver-programs-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/ALC-VER-PROGRAMS-PREP-2026-08-20.md"
mkdir "$TMP/tree/design/ALC-VER-PROGRAMS-PREP-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "directory in place of Markdown is COULD NOT LOOK"
else bad "directory input rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/alc-ver-programs-prep-2026-08-20.json" <<'PY'
from pathlib import Path
import sys
Path(sys.argv[1]).write_text("{\n", encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "malformed JSON is COULD NOT LOOK"
else bad "malformed JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-alc-ver-programs-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
