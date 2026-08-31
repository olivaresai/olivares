#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-07 unique leftover unique vs original OPEN #1027.
# Independently dated 2026-08-21. Does not copy check-c13-07-holds.sh.
# Unique vs leftover #1424 and vs check-c13-07-growth-slugs.sh (day_one ∩ growth).
# 0 CLEAN · 1 finding · 2 LOOK.
#
# ⛔ CAMINO SIN PyYAML, misma clase que taskfile-shape.py:14-27 y
# check-ci-timeout-arithmetic.sh:19-28. Ningún contenedor de este
# proyecto tiene PyYAML, ni pip, ni ensurepip. Con `import yaml` como
# única vía este guion salía 2 (LOOK) y el canario lint:addon-sets
# enrojecía lote 77 en control-plane — medido 2026-08-25T19:41Z sobre
# hub/r25-lote77 ea139ceb2. El parser de PyYAML se usa SI ESTÁ; si no,
# una lectura de indentación del oferta AIRS que rehusa duplicados y
# no adivina anclas/flujo. La lectura plana puede REHUSAR de más,
# nunca absuelve un duplicado que PyYAML cazaría en esa oferta.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-07-ar-remeasure: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-07-ar-remeasure: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2; pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1307_JSON:-design/c13-07-ar-holds-remeasure-2026-08-21.json}"
DOC="${OLIVARES_C1307_DOC:-design/C13-07-AR-HOLDS-REMEASURE-2026-08-21.md}"
CANON="${OLIVARES_C1307_CANON:-design/PRICING-CANON.md}"
HOLD="${OLIVARES_C1307_HOLD:-design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"
[ -f "$HOLD" ] || cannot "missing $HOLD"
command -v python3 >/dev/null || cannot "python3 is missing"

command -v git >/dev/null || cannot "git is missing; cannot derive the remasure baseline"
git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1 \
	|| cannot "$ROOT is not a Git worktree"
DERIVED_HUB="$(git -C "$ROOT" log --follow --diff-filter=A --format=%P -- \
	design/c13-07-ar-holds-remeasure-2026-08-21.json)" \
	|| cannot "cannot derive the introducing commit parent"
[[ "$DERIVED_HUB" =~ ^[0-9a-f]{40}$ ]] \
	|| cannot "derived baseline is not exactly one commit identity"

python3 - "$JSON" "$DOC" "$CANON" "$HOLD" "$DERIVED_HUB" <<'PY' || exit $?
import json, re, sys

def cannot(message):
    print("check-c13-07-ar-remeasure: COULD NOT LOOK — " + message, file=sys.stderr)
    sys.exit(2)

yaml = None
try:
    import yaml as yaml_mod
except Exception:
    yaml_mod = None
else:
    yaml = yaml_mod

class DuplicateKeyError(ValueError):
    pass

def unique_object(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise DuplicateKeyError("duplicate JSON key %r" % key)
        out[key] = value
    return out

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"), object_pairs_hook=unique_object)
    doc = open(sys.argv[2], encoding="utf-8").read()
    canon = open(sys.argv[3], encoding="utf-8").read()
    hold = open(sys.argv[4], encoding="utf-8").read()
except DuplicateKeyError as exc:
    raise SystemExit("ambiguous machine evidence: %s" % exc)
except Exception as exc:
    cannot("inputs are not parseable: %s" % exc)

def visible_markdown(text):
    """Return rendered prose/table source, excluding HTML comments and fenced code."""
    text = re.sub(r"<!--.*?(?:-->|\Z)", "", text, flags=re.S)
    visible = []
    fence = None
    for line in text.splitlines():
        marker = re.match(r"^\s*(`{3,}|~{3,})", line)
        if marker:
            kind = marker.group(1)[0]
            if fence is None:
                fence = kind
            elif fence == kind:
                fence = None
            continue
        if fence is None:
            visible.append(line)
    return "\n".join(visible)

doc_visible = visible_markdown(doc)
hold_visible = visible_markdown(hold)
if "Unique leftover unique vs original OPEN" not in doc_visible:
    raise SystemExit("remasure doc lost visible uniqueness vs original OPEN")
if re.search(r"FIRMA A claimed|overlay remasured live", doc_visible, flags=re.I):
    raise SystemExit("remasure doc visibly claims a close this lote does not have")

if data.get("schema") != "c13-07-ar-holds-remeasure/v2":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("claims_growth_modules_delivered") is not False:
    raise SystemExit("claims_growth_modules_delivered must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    raise SystemExit("overlay_remeasured_in_this_gate must stay false")
if data.get("panel_executed") is not False:
    raise SystemExit("panel_executed must stay false")
if data.get("dated") != "2026-08-21":
    raise SystemExit("dated must stay 2026-08-21")
baselines = re.findall(
    r"Remasure against current\s+`origin/main`\s+`([0-9a-f]{40})`", doc_visible
)
if len(baselines) != 1:
    raise SystemExit("remasure doc must carry exactly one full baseline identity")
if data.get("hub") != baselines[0]:
    raise SystemExit("JSON hub does not equal the exact baseline in the remasure doc")
if data.get("hub") != sys.argv[5]:
    raise SystemExit("recorded hub does not equal the Git-derived introducing parent")
want_ar = ["AR-1", "AR-2", "AR-3", "AR-4"]
if data.get("ar_rows") != want_ar:
    raise SystemExit("ar_rows drifted")
want_growth = [
    "server-tool-egress",
    "elicitation-mediator",
    "computer-use-gate",
    "render-inspector",
    "retrieval-scan",
]
if data.get("growth") != want_growth:
    raise SystemExit("growth drifted")
want_unique = ["#1027", "#1424", "check-c13-07-growth-slugs.sh"]
if data.get("unique_vs") != want_unique:
    raise SystemExit("declared comparison scope drifted")
for comparison in want_unique:
    if comparison not in doc_visible:
        raise SystemExit("remasure doc lost declared comparison %s" % comparison)

doc_flat = re.sub(r"\s+", " ", doc_visible).casefold()
for phrase in (
    "hold. ar-1..ar-4 have material criterion/evidence cells",
    "five growth slugs match canon",
    "remain outside day-one delivery",
):
    if phrase not in doc_flat:
        raise SystemExit("remasure doc lost material HOLD boundary: %s" % phrase)

for clause in re.split(r"[.!?;]+", doc_flat):
    names_five = re.search(r"\b(?:five|5)\b", clause)
    names_growth = re.search(r"\bgrowth\b", clause)
    delivered = re.search(r"\b(?:delivered|day-one)\b", clause)
    bounded = re.search(
        r"\boutside\b.{0,40}\bday-one\b"
        r"|\b(?:does not|do not|not|never)\b.{0,60}\b(?:claim|deliver|delivered|day-one)\b"
        r"|\b(?:forbid|forbids|forbidden)\b.{0,60}\b(?:claim|deliver|delivered)\b"
        r"|\bno\s+(?:claim|delivery)\b",
        clause,
    )
    if names_five and names_growth and delivered and not bounded:
        raise SystemExit("remasure doc claims unmeasured growth-module delivery")

yaml_blocks = re.findall(r"^```yaml\s*\n(.*?)^```\s*$", canon, flags=re.M | re.S)
if len(yaml_blocks) != 1:
    raise SystemExit("canon must carry exactly one authoritative yaml fence")
canon_yaml = yaml_blocks[0]

def canonical_list_from(mapping, label):
    values = mapping.get(label)
    if not isinstance(values, list) or not values or not all(isinstance(v, str) for v in values):
        raise SystemExit("AIRS %s must be a non-empty string list" % label)
    if len(values) != len(set(values)):
        raise SystemExit("AIRS %s repeats a module" % label)
    return values

def parse_airs_lists_by_indent(canon_yaml):
    """Conservative AIRS-offer reader. Duplicate 4-space keys FAIL.
    Tabs, anchors, merge keys, or flow style on the two lists → LOOK."""
    offer = "self_hosted.business.addons.ai-runtime-security:"
    lines = canon_yaml.splitlines()
    start = None
    for i, line in enumerate(lines):
        if line.startswith("  " + offer):
            if start is not None:
                raise SystemExit("canonical yaml repeats the AIRS offer key")
            start = i
    if start is None:
        raise SystemExit("canonical yaml has no AIRS offer mapping")
    end = len(lines)
    for j in range(start + 1, len(lines)):
        line = lines[j]
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if line.startswith("  ") and not line.startswith("    "):
            end = j
            break
        if line and not line.startswith(" "):
            end = j
            break
    block = lines[start + 1 : end]
    keys = {}
    i = 0
    while i < len(block):
        line = block[i]
        if "\t" in line:
            cannot("AIRS block uses tabs; the indent reader will not guess")
        if re.search(r"(?:^|\s)[&*]|^.*<<:", line):
            cannot("AIRS block uses YAML anchors/merge keys; the indent reader will not guess")
        if line.count("[") != line.count("]") or line.count("{") != line.count("}"):
            raise SystemExit("canonical yaml is invalid or ambiguous: unclosed flow")
        m_list = re.match(r"^    ([A-Za-z0-9_]+):\s*(?:#.*)?$", line)
        m_scalar = re.match(r"^    ([A-Za-z0-9_]+):\s+\S", line)
        if m_list:
            key = m_list.group(1)
            if key in keys:
                raise SystemExit("canonical YAML repeats key %r" % key)
            vals = []
            k = i + 1
            while k < len(block):
                item = block[k]
                if item.strip() == "" or item.lstrip().startswith("#"):
                    k += 1
                    continue
                if re.match(r"^      - ", item):
                    if "{" in item or "[" in item:
                        cannot("AIRS list %s uses flow style" % key)
                    slug = re.sub(r"\s+#.*$", "", item[8:]).strip()
                    if not slug:
                        cannot("AIRS list %s has an empty item" % key)
                    vals.append(slug)
                    k += 1
                    continue
                break
            keys[key] = vals
            i = k
            continue
        if m_scalar:
            key = m_scalar.group(1)
            if key in keys:
                raise SystemExit("canonical YAML repeats key %r" % key)
            keys[key] = None
            i += 1
            continue
        i += 1
    growth = canonical_list_from(keys, "modules_growth")
    day_one_values = canonical_list_from(keys, "modules_day_one")
    return growth, day_one_values

if yaml is not None:
    class UniqueKeyLoader(yaml.SafeLoader):
        pass

    def unique_mapping(loader, node, deep=False):
        mapping = {}
        for key_node, value_node in node.value:
            key = loader.construct_object(key_node, deep=deep)
            if key in mapping:
                raise ValueError("canonical YAML repeats key %r" % key)
            mapping[key] = loader.construct_object(value_node, deep=deep)
        return mapping

    UniqueKeyLoader.add_constructor(
        yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG,
        unique_mapping,
    )
    try:
        canonical = yaml.load(canon_yaml, Loader=UniqueKeyLoader)
    except Exception as exc:
        raise SystemExit("canonical yaml is invalid or ambiguous: %s" % exc)
    if not isinstance(canonical, dict) or not isinstance(canonical.get("offers"), dict):
        raise SystemExit("canonical yaml has no offers mapping")
    airs = canonical["offers"].get("self_hosted.business.addons.ai-runtime-security")
    if not isinstance(airs, dict):
        raise SystemExit("canonical yaml has no AIRS offer mapping")
    growth = canonical_list_from(airs, "modules_growth")
    day_one_values = canonical_list_from(airs, "modules_day_one")
else:
    growth, day_one_values = parse_airs_lists_by_indent(canon_yaml)
day_one = set(day_one_values)
if growth != want_growth:
    raise SystemExit("canon modules_growth drifted from the declared growth identity")
named = re.findall(r"^hold-growth-slug:\s*([a-z0-9-]+)\s*$", hold_visible, flags=re.M)
if len(named) != len(set(named)) or set(named) != set(growth):
    raise SystemExit(
        "hold-growth-slug must equal canon modules_growth (canon=%s hold=%s)"
        % (sorted(growth), sorted(named))
    )
overlap = sorted(set(growth) & day_one)
if overlap:
    raise SystemExit("growth slugs leaked into modules_day_one: %s" % ", ".join(overlap))
def material(cell):
    plain = re.sub(r"[`*_#\[\]()>|]", " ", cell)
    words = re.findall(r"[A-Za-zÀ-ÿ0-9][A-Za-zÀ-ÿ0-9_./+-]*", plain)
    return len(words) >= 3 and plain.strip().casefold() not in {
        "tbd", "todo", "unknown", "n/a", "no medido", "-", "—"
    }

rows = {}
for line in hold_visible.splitlines():
    if not line.lstrip().startswith("|"):
        continue
    cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
    if cells and cells[0] in want_ar:
        if cells[0] in rows:
            raise SystemExit("HOLD doc duplicates the %s row" % cells[0])
        rows[cells[0]] = cells
for ar in want_ar:
    cells = rows.get(ar)
    if cells is None:
        raise SystemExit("HOLD doc is missing the %s row" % ar)
    if len(cells) != 3 or not all(cells[1:]):
        raise SystemExit("HOLD doc %s must have non-empty criterion and evidence cells" % ar)
    if not material(cells[1]) or not material(cells[2]):
        raise SystemExit("HOLD doc %s criterion/evidence is not material" % ar)
    if not re.search(r"(?:`|\btest\b|\bgate\b|\bevidencia\b|\bjson\b|\bcheck)", cells[2], flags=re.I):
        raise SystemExit("HOLD doc %s evidence cell names no executable or durable witness" % ar)
if len({re.sub(r"\s+", " ", cells[1]).casefold() for cells in rows.values()}) != len(want_ar):
    raise SystemExit("HOLD doc repeats one criterion across multiple AR rows")
PY

say "check-c13-07-ar-remeasure: CLEAN — four material AR contracts; five growth slugs outside day_one; exact baseline and declared comparison scope agree; no delivery claim."
exit 0
