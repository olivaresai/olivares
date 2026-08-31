#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-07-ar-remeasure.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2; pwd)"
CHECK="$ROOT/scripts/check-c13-07-ar-remeasure.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1307ar.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c13-07-ar-remeasure.sh"
	cp "$ROOT/design/PRICING-CANON.md" "$TMP/tree/design/"
	cp "$ROOT/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" "$TMP/tree/design/"
	cp "$ROOT/design/C13-07-AR-HOLDS-REMEASURE-2026-08-21.md" "$TMP/tree/design/"
	cp "$ROOT/design/c13-07-ar-holds-remeasure-2026-08-21.json" "$TMP/tree/design/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$ROOT" \
		OLIVARES_C1307_JSON="$TMP/tree/design/c13-07-ar-holds-remeasure-2026-08-21.json" \
		OLIVARES_C1307_DOC="$TMP/tree/design/C13-07-AR-HOLDS-REMEASURE-2026-08-21.md" \
		OLIVARES_C1307_CANON="$TMP/tree/design/PRICING-CANON.md" \
		OLIVARES_C1307_HOLD="$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" \
		bash "$CHECK" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live HOLD remasure is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace("| AR-2 |", "| XX-2 |", 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: AR-2 row removed is FAIL"
else
	bad "AR-2 gone should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
# leak a growth slug into modules_day_one (unique vs growth-slugs CHECK)
python3 - "$TMP/tree/design/PRICING-CANON.md" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
needle = "    modules_day_one:                # los dos con profundidad probada\n      - content-firewall\n      - hook-firewall\n"
repl = needle + "      - retrieval-scan\n"
if needle not in s:
    raise SystemExit("day_one block not found")
p.write_text(s.replace(needle, repl, 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: growth leaked into day_one is FAIL"
else
	bad "day_one leak should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '%s\n' \
	'All five AIRS growth modules' \
	'are delivered in the day-one payload.' \
	>>"$TMP/tree/design/C13-07-AR-HOLDS-REMEASURE-2026-08-21.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims unmeasured day-one delivery is FAIL"
else
	bad "delivery claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '%s\n' \
	'All five AIRS growth modules are delivered in the day-one payload; no caveats.' \
	>>"$TMP/tree/design/C13-07-AR-HOLDS-REMEASURE-2026-08-21.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: no-caveats decoy cannot hide a delivery claim"
else
	bad "no-caveats delivery claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c13-07-ar-holds-remeasure-2026-08-21.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["claims_growth_modules_delivered"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: machine copy claims growth delivery is FAIL"
else
	bad "machine delivery claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# B2 counterfactual: comparison scope is material evidence, not decorative JSON.
stage
python3 - "$TMP/tree/design/c13-07-ar-holds-remeasure-2026-08-21.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["unique_vs"] = []
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: empty comparison scope is FAIL"
else
	bad "empty comparison scope should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# B2 counterfactual: a baseline identity cannot be validated by hexadecimal shape alone.
stage
python3 - "$TMP/tree/design/c13-07-ar-holds-remeasure-2026-08-21.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["hub"] = "0" * 40
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: forty-zero baseline is FAIL"
else
	bad "forty-zero baseline should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c13-07-ar-holds-remeasure-2026-08-21.json" <<'PY'
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
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: duplicate JSON hub is FAIL"
else
	bad "duplicate JSON hub should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '%s\n' \
	'Conflicting attribution: Remasure against current `origin/main` `0000000000000000000000000000000000000000`.' \
	>>"$TMP/tree/design/C13-07-AR-HOLDS-REMEASURE-2026-08-21.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: a second visible baseline is FAIL"
else
	bad "duplicate baseline should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# Cross-file agreement is insufficient when both assertions disagree with the Git-derived parent.
stage
python3 - \
  "$TMP/tree/design/c13-07-ar-holds-remeasure-2026-08-21.json" \
  "$TMP/tree/design/C13-07-AR-HOLDS-REMEASURE-2026-08-21.md" <<'PY'
from pathlib import Path
import json, re, sys

json_path = Path(sys.argv[1])
data = json.loads(json_path.read_text(encoding="utf-8"))
data["hub"] = "0" * 40
json_path.write_text(json.dumps(data), encoding="utf-8")
doc_path = Path(sys.argv[2])
doc_path.write_text(re.sub(r"Remasure against current `origin/main` `[0-9a-f]{40}`", "Remasure against current `origin/main` `" + "0" * 40 + "`", doc_path.read_text(encoding="utf-8")), encoding="utf-8")
PY
OLIVARES_C1307_DERIVED_HUB="0000000000000000000000000000000000000000" run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc and JSON cannot share a false baseline"
else
	bad "mutually false baseline should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# B2 counterfactual: four labels with empty criterion/evidence cells are not four written criteria.
stage
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
from pathlib import Path
import re, sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
for ar in ("AR-1", "AR-2", "AR-3", "AR-4"):
    s, count = re.subn(r"^\|\s*" + re.escape(ar) + r"\s*\|.*$", f"| {ar} | | |", s, count=1, flags=re.M)
    if count != 1:
        raise SystemExit(f"could not empty {ar}")
p.write_text(s, encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: semantically empty AR rows are FAIL"
else
	bad "empty AR rows should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# Markdown comments preserve source text but remove the rows from the rendered contract.
stage
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
from pathlib import Path
import re, sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
matches = list(re.finditer(r"^\|\s*AR-[1-4]\s*\|.*$", text, flags=re.M))
if len(matches) != 4:
    raise SystemExit("could not locate the four AR rows")
start, end = matches[0].start(), matches[-1].end()
p.write_text(text[:start] + "<!--\n" + text[start:end] + "\n-->" + text[end:], encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: AR rows hidden in a Markdown comment are FAIL"
else
	bad "comment-hidden AR rows should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
from pathlib import Path
import re, sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
match = re.search(r"^\|\s*AR-1\s*\|", text, flags=re.M)
if match is None:
    raise SystemExit("could not locate AR-1")
p.write_text(text[:match.start()] + "<!--\n" + text[match.start():], encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: unterminated Markdown comment cannot hide AR rows"
else
	bad "unterminated comment should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# JSON/doc identities cannot float independently from the canonical list, even if HOLD is changed
# to agree with a forged canon.
stage
python3 - \
  "$TMP/tree/design/PRICING-CANON.md" \
  "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" <<'PY'
from pathlib import Path
import sys

old = [
    "server-tool-egress", "elicitation-mediator", "computer-use-gate",
    "render-inspector", "retrieval-scan",
]
new = ["fake-growth-one", "fake-growth-two", "fake-growth-three", "fake-growth-four", "fake-growth-five"]
for raw in sys.argv[1:]:
    path = Path(raw)
    text = path.read_text(encoding="utf-8")
    for before, after in zip(old, new):
        text = text.replace(before, after)
    path.write_text(text, encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: canon+HOLD cannot drift from declared growth identities"
else
	bad "joint canon/HOLD drift should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# A duplicated YAML-like key is ambiguous; taking the first match would hide a later overlap.
stage
python3 - "$TMP/tree/design/PRICING-CANON.md" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
needle = "    modules_growth:                 # entran al pasar AR-1..AR-4; mismo precio, el add-on CRECE;\n"
insert = "    modules_day_one:\n      - retrieval-scan\n"
if text.count(needle) != 1:
    raise SystemExit("canonical growth key changed")
p.write_text(text.replace(needle, insert + needle), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: duplicate modules_day_one key is FAIL"
else
	bad "duplicate day_one key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# Text outside the sole yaml fence is not canon authority, even if it has the expected shape.
stage
python3 - "$TMP/tree/design/PRICING-CANON.md" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
offer = "  self_hosted.business.addons.ai-runtime-security:\n"
if text.count(offer) != 1:
    raise SystemExit("authoritative AIRS offer changed")
text = text.replace(offer, "  self_hosted.business.addons.ai-runtime-security-renamed:\n", 1)
outside = '''

  self_hosted.business.addons.ai-runtime-security:
    modules_day_one:
      - content-firewall
      - hook-firewall
    modules_growth:
      - server-tool-egress
      - elicitation-mediator
      - computer-use-gate
      - render-inspector
      - retrieval-scan
'''
p.write_text(text + outside, encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: text outside the yaml fence cannot replace canon authority"
else
	bad "outside-fence authority should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/PRICING-CANON.md" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
needle = "    decision_status: decided\n    public_name: AI Runtime Security\n"
if text.count(needle) != 1:
    raise SystemExit("AIRS decision status changed")
p.write_text(text.replace(needle, "    decision_status: [\n    public_name: AI Runtime Security\n", 1), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: malformed canonical YAML is FAIL"
else
	bad "malformed canonical YAML should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

# Semantic no-fire: JSON order/indentation and harmless Markdown pipe spacing do not change the
# four material contracts.
stage
python3 - \
  "$TMP/tree/design/c13-07-ar-holds-remeasure-2026-08-21.json" \
  "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md" \
  "$TMP/tree/design/C13-07-AR-HOLDS-REMEASURE-2026-08-21.md" <<'PY'
from pathlib import Path
import json, sys

json_path = Path(sys.argv[1])
data = json.loads(json_path.read_text(encoding="utf-8"))
json_path.write_text(json.dumps(data, indent=4, sort_keys=True) + "\n", encoding="utf-8")
hold_path = Path(sys.argv[2])
lines = []
for line in hold_path.read_text(encoding="utf-8").splitlines():
    if line.strip().startswith("| AR-") and line.strip().endswith("|"):
        cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
        line = "|  " + "  |  ".join(cells) + "  |"
    lines.append(line)
hold_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
doc_path = Path(sys.argv[3])
doc = doc_path.read_text(encoding="utf-8")
needle = "have material criterion/evidence cells; five growth slugs"
if needle not in doc:
    raise SystemExit("remasure HOLD sentence not found")
doc_path.write_text(doc.replace(needle, "have material criterion/evidence cells;\nfive growth slugs", 1), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: semantic Markdown/JSON formatting stays CLEAN"
else
	bad "semantic formatting should stay CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '%s\n' '<!-- FIRMA A claimed -->' \
	>>"$TMP/tree/design/C13-07-AR-HOLDS-REMEASURE-2026-08-21.md"
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: hidden Markdown comment has no semantic authority"
else
	bad "hidden comment should stay CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c13-07-ar-holds-remeasure-2026-08-21.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON LOOK 2"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c13-07-ar-holds-remeasure-2026-08-21.json" <<'PY'
from pathlib import Path
import sys
Path(sys.argv[1]).write_text("{\n", encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "malformed JSON LOOK 2"
else
	bad "malformed JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing HOLD LOOK 2"
else
	bad "missing HOLD should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "test-c13-07-ar-remeasure: $pass/$((pass + fail))"
[ "$fail" -eq 0 ]
