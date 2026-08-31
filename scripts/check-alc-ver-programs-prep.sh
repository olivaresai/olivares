#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ALC/VER programs unique leftover unique vs #956 and
# check-alc-ver-programs.sh (original OPEN CHECK would LOOK 2 on
# origin/main). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-ver-programs-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-ver-programs-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2; pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALCVERP_JSON:-design/alc-ver-programs-prep-2026-08-20.json}"
DOC="${OLIVARES_ALCVERP_DOC:-design/ALC-VER-PROGRAMS-PREP-2026-08-20.md}"
SCIM="${OLIVARES_ALCVERP_SCIM:-design/PROGRAMA-ALC-SCIM-GESTIONADO-2026-08-18.md}"
FIA="${OLIVARES_ALCVERP_FIA:-design/PROGRAMA-ALC-FIABILIDAD-CLOUD-2026-08-18.md}"
WORM="${OLIVARES_ALCVERP_WORM:-design/ALC-03-WORM-LIVE-CRITERIOS-2026-08-18.md}"
MED="${OLIVARES_ALCVERP_MED:-design/VER-ALC-MEDIDO-2026-08-18.md}"
CTR19="${OLIVARES_ALCVERP_CTR19:-design/ALC-01-S2-MANAGED-SCIM-CONTRACT-2026-08-19.md}"
ORIG="${OLIVARES_ALCVERP_ORIG:-scripts/check-alc-ver-programs.sh}"

for f in "$JSON" "$DOC" "$SCIM" "$FIA" "$WORM" "$MED"; do
  [ -f "$f" ] && [ -r "$f" ] || cannot "missing or non-file input $f"
done
command -v python3 >/dev/null || cannot "no python3"

command -v git >/dev/null || cannot "no git to derive the remasure baseline"
git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1 \
  || cannot "$ROOT is not a Git worktree"
DERIVED_HUB="$(git -C "$ROOT" log --follow --diff-filter=A --format=%P -- \
  design/alc-ver-programs-prep-2026-08-20.json)" \
  || cannot "cannot derive the introducing commit parent"
[[ "$DERIVED_HUB" =~ ^[0-9a-f]{40}$ ]] \
  || cannot "derived baseline is not exactly one commit identity"

if [ -e "$CTR19" ]; then
  fail "ALC-01-S2 contract 2026-08-19 landed — this HOLD lote does not apply #956"
fi
if [ -e "$ORIG" ]; then
  fail "original check-alc-ver-programs.sh landed — LOOK 2 CHECK stays out of lint:addon-sets"
fi
if [ -d public/updates ]; then
  fail "public/updates/ is in this tree — VER-10/CFG-06 must not publish"
fi

python3 - "$JSON" "$DOC" "$SCIM" "$FIA" "$WORM" "$MED" "$DERIVED_HUB" <<'PY' || exit $?
import json, re, sys

def fail(msg):
    print(f"check-alc-ver-programs-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-alc-ver-programs-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

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
    prep, scim, fia, worm, med = [open(p, encoding="utf-8").read() for p in sys.argv[2:7]]
except DuplicateKeyError as e:
    fail("ambiguous machine evidence: %s" % e)
except Exception as e:
    cannot(f"inputs not readable: {e}")

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

prep, scim, fia, worm, med = [visible_markdown(text) for text in (prep, scim, fia, worm, med)]

if data.get("schema") != "alc-ver-programs-prep/v2":
    fail("unknown schema %r" % data.get("schema"))
for phrase in (
    "Unique leftover unique vs `#956`",
    "Unique leftover unique vs `check-alc-ver-programs.sh`",
    "HOLD. NOT APPLIED.",
    "ALC-01-S2 contract 2026-08-19 not landed.",
    "Does not copy `#956`",
):
    if phrase not in prep:
        fail("prepare doc lost visible boundary: %s" % phrase)
if re.search(
    r"FIRMA A claimed|remainder applied on origin/main|ALC-01-S2 contract 2026-08-19 landed",
    prep,
    flags=re.I,
):
    fail("prepare doc visibly claims an application this lote does not have")
for k in (
    "scim_program_present",
    "fiabilidad_program_present",
    "worm_criterios_present",
    "ver_alc_medido_cannot_look",
):
    if data.get(k) is not True:
        fail("%s must stay true" % k)
for k in (
    "alc_01_s2_contract_2026_08_19_landed",
    "original_check_landed",
    "remainder_applied",
    "overlay_remeasured_in_this_gate",
):
    if data.get(k) is not False:
        fail("%s must stay false" % k)
baselines = re.findall(r"Remasured on hub\s+`([0-9a-f]{40})`", prep)
if len(baselines) != 1:
    fail("prepare doc must carry exactly one full remasure baseline")
hub = data.get("hub")
if hub != baselines[0]:
    fail("JSON hub does not equal the exact remasure baseline in the prepare doc")
if hub != sys.argv[7]:
    fail("recorded hub does not equal the Git-derived introducing parent")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)

def normalized(text):
    return re.sub(r"\s+", " ", text).strip()

def require(condition, message):
    if not condition:
        fail(message)

def material(cell):
    plain = re.sub(r"[`*_#\[\]()>|]", " ", cell)
    words = re.findall(r"[A-Za-zÀ-ÿ0-9][A-Za-zÀ-ÿ0-9_./+-]*", plain)
    return len(words) >= 3 and plain.strip().casefold() not in {
        "tbd", "todo", "unknown", "n/a", "no medido", "-", "—"
    }

def rows(text, ids, minimum_cells, name):
    found = {}
    for line in text.splitlines():
        if not line.lstrip().startswith("|"):
            continue
        cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
        if cells and cells[0] in ids:
            if cells[0] in found:
                fail(f"{name} duplicates row {cells[0]}")
            found[cells[0]] = cells
    for row_id in ids:
        cells = found.get(row_id)
        require(cells is not None, f"{name} is missing material row {row_id}")
        require(len(cells) >= minimum_cells, f"{name} row {row_id} lost columns")
        require(all(cell.strip() for cell in cells[1:]), f"{name} row {row_id} has an empty cell")
        require(material(cells[1]), f"{name} row {row_id} has no material description")
        require(material(cells[-1]), f"{name} row {row_id} has no material completion evidence")

require(re.search(r"^#\s+.*SCIM", scim, flags=re.I | re.M), "SCIM program lost its heading")
scim_flat = normalized(scim)
for phrase in ("No es el motor", "Managed SCIM", "sin paquete"):
    require(phrase.casefold() in scim_flat.casefold(), f"SCIM program lost boundary: {phrase}")
rows(scim, [f"ALC-01-S{i}" for i in range(1, 5)], 5, "SCIM program")

require(re.search(r"^#\s+.*fiabilidad.*(?:DR|multi-region|CMEK)", fia, flags=re.I | re.M),
        "fiabilidad program lost its scoped heading")
fia_flat = normalized(fia)
for phrase in ("Roadmap declarado, no construido", "C04", "DR", "multi-region", "CMEK"):
    require(phrase.casefold() in fia_flat.casefold(), f"fiabilidad program lost boundary: {phrase}")
rows(fia, [f"ALC-02-F{i}" for i in range(1, 5)], 4, "fiabilidad program")

require(re.search(r"^#\s+.*WORM", worm, flags=re.I | re.M), "WORM criteria lost their heading")
worm_flat = normalized(worm)
for phrase in (
    "Esos tests no hablan con Azure ni GCS",
    "NO HE PODIDO MIRAR el live",
    "LOCKED",
    "Bucket-Lock",
):
    require(phrase.casefold() in worm_flat.casefold(), f"WORM criteria lost boundary: {phrase}")
rows(worm, [f"W-{i}" for i in range(1, 5)], 3, "WORM criteria")

section = re.search(r"^##\s+VER-09\b.*?(?=^##\s+|\Z)", med, flags=re.I | re.M | re.S)
require(section is not None, "VER measurement lost its VER-09 section")
ver09 = normalized(section.group(0))
require("no he podido mirar" in ver09.casefold(), "VER-09 lost its cannot-look result")
require("eso no es la compra real" in ver09.casefold(),
        "VER-09 no longer distinguishes the hermetic selftest from a real purchase")

productive = re.compile(r"\b(?:productive|production|producci[oó]n|buy.{0,20}bytes|compra.{0,20}bytes)\b", re.I)
success = re.compile(r"\b(?:pass(?:ed)?|green|verde|success(?:ful(?:ly)?)?|verified|verificad[oa]|cerrad[oa])\b", re.I)
for clause in re.split(r"[.!?;]+", normalized(section.group(0))):
    denied = re.search(
        r"\b(?:not|never|nunca)\b.{0,60}"
        r"\b(?:pass(?:ed)?|green|verde|success(?:ful(?:ly)?)?|verified|verificad[oa]|cerrad[oa])\b"
        r"|\bno\s+(?:se\s+)?(?:ha\s+)?(?:verificad[oa]|validado|cerrad[oa])\b",
        clause,
        flags=re.I,
    )
    if productive.search(clause) and success.search(clause) and not denied:
        fail("VER-09 combines cannot-look with an unmeasured productive-success claim")
print("json-ok")
PY

say "check-alc-ver-programs-prep: CLEAN — four material document contracts derived; VER-09 remains cannot-look; exact baseline and HOLD identities agree."
exit 0
