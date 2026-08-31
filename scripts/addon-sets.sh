#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Derive the four self-hosted business ADD-ON SETS, and the modules each one ships, from
# an internal design note (not shipped) — which the canon itself declares the single source of truth for plans.
#
# WHY THIS EXISTS. The commercial binary is about to be cut by build tag
# (`enterprise && addon_{reg,airs,cp,ids}`) so a buyer never receives the bytes of an add-on they
# did not pay for. That cut needs ONE machine-checked answer to "which modules belong to which
# add-on", and an internal design note (not shipped) 2.1 is explicit that the answer comes from
# the CANON and NOT from enterprise/activation/catalog.go, which is cumulative by tier and does not
# even contain the compliance-packs modules. Two conventions for one fact is how the sets drift.
#
# ⛔ WHY IT REFUSES INSTEAD OF GUESSING. The canon carries SIX different `modules*` keys and they do
# NOT mean the same thing (measured 2026-08-10):
#
#   modules            plain list of what the offer ships
#   modules_day_one    ships now
#   modules_growth     ships in the same artifact, same price — the add-on GROWS (2.1)
#   modules_on         a CLOUD tier's list, not a self-hosted add-on's
#   modules_manifest   a SCALAR (e.g. cloud-standard-v1), not a list at all
#   modules_hold_gated ⛔ modules that enter ONE BY ONE as each passes its gate
#
# A parser that globbed `modules*` would swallow the last two: it would parse a scalar as a module
# name, and it would project HOLD-gated modules as shipped — which is precisely the thing the whole
# cut exists to prevent, arrived at from the inside. So the allowed keys are ENUMERATED, and any
# other `modules*` key inside an add-on block is a REFUSAL that names the key.
#
# Three answers, never two: the sets, a named refusal, or "could not read the canon". An empty set
# is never an answer — a build tag derived from an empty set excludes nothing.
#
# Output (stdout), one line per module, stable order:  <set-code>\t<module-slug>
set -eu

CANON="${1:-design/PRICING-CANON.md}"

if [ ! -r "$CANON" ]; then
  echo "addon-sets: cannot read ${CANON} — refusing to report sets I could not derive" >&2
  exit 2
fi

python3 - "$CANON" <<'PY'
import re, sys

path = sys.argv[1]
try:
    lines = open(path, encoding="utf-8").read().splitlines()
except OSError as e:
    sys.exit(f"addon-sets: cannot read {path}: {e}")

# The four self-hosted business add-ons and the short code each build tag uses.
CODES = {
    "regulated": "reg",
    "ai-runtime-security": "airs",
    "compliance-packs": "cp",
    "identity-scale": "ids",
}
# Keys whose entries SHIP INSIDE this add-on's artifact. Enumerated, never globbed.
SHIPS = {"modules", "modules_day_one", "modules_growth"}

# ⛔ NESTING IS JUDGED BY RELATIVE DEPTH, NOT BY A FIXED COLUMN, AND THAT IS THE WHOLE POINT.
#
# The first version of this parser pinned every shape to an exact indent: the add-on at two spaces,
# the key at four, the items at six. A `sol max` contrast measured what that costs, and it is not a
# style complaint. Re-indent the canon — a legitimate, invisible YAML edit — and `modules_hold_gated`
# stops matching the key pattern, so it is NEITHER refused NOR does it reset the current key. Its
# items, sitting one level deeper, are then collected under the PREVIOUS allowed key. That is
# hold-gated modules projected as delivered: precisely the defect the set cut exists to prevent,
# arrived at from the inside, and reached by re-indenting a file.
#
# Case and quoting were the same hole from a different side: `[a-z_]*` does not see
# `Modules_hold_gated:` or `"modules_hold_gated":`, so neither was refused either.
#
# So: an add-on opens a block, anything at its depth or shallower closes it, keys are recognised at
# any deeper depth, and items belong to the key that is still open above them.
ADDON = re.compile(r"^(\s*)self_hosted\.business\.addons\.([a-z0-9-]+):\s*$")
KEYLINE = re.compile(r"""^(\s*)(['"]?)([A-Za-z0-9_.-]+)\2\s*:\s*(.*)$""")
# The trailing-comment branch is not decoration: the canon annotates most entries
# (`- login-enforcement           # require-SSO`). Anchoring the item at end-of-line without it
# dropped six real modules from ids and reg — under-delivery, the mirror of the defect above, and it
# is why the counterfactual against the real canon is run before anything else is believed.
ITEM = re.compile(r"""^(\s*)-\s*(['"]?)([A-Za-z0-9./_-]+)\2\s*(?:#.*)?$""")
INLINE = re.compile(r"^\[(.*)\]$")


def depth(s):
    return len(s) - len(s.lstrip(" \t"))


sets, refusals = {}, []
cur = None          # add-on slug of the block we are inside
cur_depth = -1      # indent of the line that opened it
key = None          # the modules* key currently open
key_depth = -1
seen_keys = set()   # delivering keys already declared in THIS add-on block

for ln in lines:
    if not ln.strip():
        continue

    m = ADDON.match(ln)
    if m:
        cur, cur_depth, key, key_depth = m.group(2), depth(m.group(1)), None, -1
        seen_keys = set()
        if cur in CODES:
            sets.setdefault(cur, [])
        continue

    if cur is None:
        continue

    km = KEYLINE.match(ln)
    if km:
        d, name, rest = depth(km.group(1)), km.group(3), km.group(4).strip()
        # Anything at the add-on's depth or shallower has closed its block.
        if d <= cur_depth:
            cur, cur_depth, key, key_depth = None, -1, None, -1
            seen_keys = set()
            continue
        # A key at or above the open key's depth closes that key, whatever it is called.
        if key is not None and d <= key_depth:
            key, key_depth = None, -1
        # ⛔ REFUSAL IS BY MEANING, NOT BY SPELLING. Anything modules-shaped that is not one of the
        # three enumerated delivering keys is refused BY NAME — including a casing or quoting we do
        # not recognise, which is a canon we do not understand rather than a canon that ships
        # nothing. Fail closed and say which key.
        if name.lower().startswith("modules") and cur in CODES:
            if name in SHIPS:
                # ⛔ A REPEATED KEY IS REFUSED, NOT MERGED. YAML forbids duplicate keys in a
                # mapping, so a canon carrying `modules:` twice is malformed and every reader
                # disagrees about it: a typed parser errors, most take the last, some take the
                # first. Unioning them — which is what reading line by line does by accident —
                # produces a set WIDER than either reading, and wider means bytes in the artifact
                # that nobody bought. Refusing is the only answer that does not pick a winner on
                # the buyer's behalf.
                if name in seen_keys:
                    refusals.append((cur, name + " (declared more than once)"))
                    key, key_depth = None, -1
                    continue
                seen_keys.add(name)
                key, key_depth = name, d
                # An inline list on the same line is still a list: `modules: [a, b]`. Missing it
                # would silently UNDER-deliver, which is the mirror of the defect above.
                inl = INLINE.match(rest)
                if inl:
                    for part in inl.group(1).split(","):
                        item = part.strip().strip("'\"")
                        if item:
                            sets[cur].append(item)
                    key, key_depth = None, -1
                elif rest and not rest.startswith("#"):
                    # A delivering key holding a scalar is a shape this does not understand.
                    refusals.append((cur, name + " (scalar value, not a list)"))
                    key, key_depth = None, -1
            else:
                refusals.append((cur, name))
                key, key_depth = None, -1
        continue

    if key and cur in CODES:
        im = ITEM.match(ln)
        if im and depth(im.group(1)) > key_depth:
            sets[cur].append(im.group(3))

# ⛔ THE CANON ASSIGNS MODULES IN TWO PLACES, AND MISSING THE SECOND UNDER-DELIVERS.
# Appendix A (`modules_assigned_<date>:`) assigns modules to an offer line WITHOUT touching that
# offer's `modules:` list, and the canon states why in the block itself: it is cited BY LINE NUMBER
# in ~200 places, including legal documents and external audits already delivered, so inserting
# into a mid-document list would silently re-point all of them. Measured 2026-08-10: that appendix
# carries `circuit-breaker` and `caeptransmit`, both `decision_status: decided`, both assigned to
# ai-runtime-security.
#
# A deriver that reads only the body lists returns 7 for airs instead of 9, and the resulting build
# tag EXCLUDES two modules the buyer paid for. That is the mirror image of shipping unpaid code,
# and just as wrong: the guard above stops us over-delivering, this one stops us under-delivering.
ASSIGN_BLOCK = re.compile(r"^  modules_assigned_[0-9_]+:\s*$")
ASSIGN_MOD = re.compile(r"^    ([a-z0-9-]+):\s*$")
ASSIGN_LINE = re.compile(r"^      line: (\S+)")
NOT_A_MODULE = {"decision_status", "open_to_verify"}

# ⛔ THREE INDEPENDENT WIDENINGS LIVED HERE, all measured by the contrast, all exit 0:
#
#   decision_status: pending      ->  airs<TAB>future-module      an UNDECIDED assignment shipped
#   notes: { line: ... }          ->  airs<TAB>notes              metadata became a module
#   line: cloud.ai-runtime-...    ->  airs<TAB>wrong-line-module  the CLOUD offer fed the SELF-HOSTED set
#
# The last one is the sharpest: the target was validated by its LAST SEGMENT only, so any offer
# family whose leaf happens to be one of our four slugs assigned into our artifact. Wider means
# bytes nobody bought, and none of the three announced itself.
#
# TARGET is now the FULL path, anchored. STATUS is read rather than skipped: `decided` projects,
# any other value does NOT (a canon mid-decision is a legitimate state, not an error), and a block
# with NO status at all is refused -- that is a canon whose meaning we cannot establish.
ASSIGN_TARGET = re.compile(r"^self_hosted\.business\.addons\.([a-z0-9-]+)$")
STATUS_LINE = re.compile(r"^(\s*)decision_status:\s*(\S+)")
# An assignment that ships a module CITES ITS EVIDENCE: both real entries in the canon do, and
# requiring it is what separates an assignment from a metadata block that happens to carry a `line:`.
# Without it, `notes:` with a valid target became a module in a shipping set.
EVIDENCE_LINE = re.compile(r"^\s*evidence:\s*\S")

in_assign = False
assign_depth = -1
pending = None
pending_target = None
pending_evidence = False
status = None


def flush_entry():
    """Decide the entry that just closed. Called on the next entry, on block close, and at EOF."""
    global pending, pending_target, pending_evidence
    if pending and pending_target:
        if not pending_evidence:
            sys.exit(f"addon-sets: appendix entry {pending!r} assigns a module but cites no "
                     f"evidence. Every real assignment in the canon carries one, and without it "
                     f"this cannot tell an assignment from a metadata block that happens to hold "
                     f"a 'line:' -- which is how 'notes' became a module in a shipping set.")
        if status == "decided":
            sets.setdefault(pending_target, []).append(pending)
        else:
            print(f"addon-sets: appendix assignment {pending!r} NOT projected: decision_status is "
                  f"{status!r}, not 'decided'.", file=sys.stderr)
    pending, pending_target, pending_evidence = None, None, False
for ln in lines:
    if not ln.strip():
        continue
    m = ASSIGN_BLOCK.match(ln)
    if m:
        in_assign, pending, status = True, None, None
        assign_depth = len(ln) - len(ln.lstrip(" "))
        continue
    if not in_assign:
        continue

    d = len(ln) - len(ln.lstrip(" "))
    # Anything back at the block's own depth or shallower has closed the appendix.
    if d <= assign_depth and re.match(r"^\s*\S", ln):
        flush_entry()
        if status is None:
            sys.exit("addon-sets: the appendix block carries no decision_status. Whether its "
                     "assignments ship is exactly what that field says, so deriving without it "
                     "would be guessing on the buyer's behalf.")
        in_assign, pending, status = False, None, None
        continue

    sm = STATUS_LINE.match(ln)
    if sm and len(sm.group(1)) > assign_depth:
        status = sm.group(2)
        continue

    am = ASSIGN_MOD.match(ln)
    if am:
        # A new entry closes the previous one, and that is where an assignment is judged --
        # streaming the append on sight of `line:` is what let a metadata block become a module.
        flush_entry()
        pending = am.group(1) if am.group(1) not in NOT_A_MODULE else None
        pending_target = None
        pending_evidence = False
        continue

    if pending and EVIDENCE_LINE.match(ln):
        pending_evidence = True
        continue

    al = ASSIGN_LINE.match(ln)
    if al and pending:
        raw = al.group(1)
        tm = ASSIGN_TARGET.match(raw)
        if not tm or tm.group(1) not in CODES:
            sys.exit(f"addon-sets: appendix assigns {pending!r} to {raw!r}, which is not one of the "
                     f"four self-hosted business add-on offers. The target is matched WHOLE, not by "
                     f"its last segment -- 'cloud.ai-runtime-security' is a different offer that "
                     f"happens to end in one of our slugs, and projecting it here would put its "
                     f"module in an artifact it was never sold with.")
        pending_target = tm.group(1)

missing = [a for a in CODES if a not in sets]
if missing:
    sys.exit("addon-sets: the canon does not declare add-on(s) " + ", ".join(sorted(missing)) +
             " — refusing to emit a partial mapping")

if refusals:
    for a, k in refusals:
        print(f"addon-sets: add-on {a!r} carries key {k!r}, whose meaning is NOT "
              f"'ships in this artifact'. Enumerate it deliberately or leave it out; it will not "
              f"be guessed.", file=sys.stderr)
    sys.exit(3)

# ⛔ EOF CLOSES THE BLOCK TOO. An appendix that runs to the end of the canon never met a line at
# its own depth, so the last entry was never decided and the status was never checked. It failed
# closed by accident, which is not the same as failing closed by design.
if in_assign:
    flush_entry()
    if status is None:
        sys.exit("addon-sets: the appendix block carries no decision_status. Whether its "
                 "assignments ship is exactly what that field says, so deriving without it "
                 "would be guessing on the buyer's behalf.")

empty = [a for a, v in sets.items() if not v]
if empty:
    sys.exit("addon-sets: add-on(s) " + ", ".join(sorted(empty)) +
             " derived ZERO modules. A build tag derived from an empty set excludes nothing, so "
             "this is a refusal, not a result.")

seen = {}
for addon, mods in sets.items():
    for mod in mods:
        if mod in seen:
            sys.exit(f"addon-sets: module {mod!r} appears in BOTH {seen[mod]!r} and {addon!r}. "
                     f"The sets must be orthogonal or a buyer of one receives the other's bytes.")
        seen[mod] = addon

for addon in sorted(sets, key=lambda a: CODES[a]):
    for mod in sorted(sets[addon]):
        print(f"{CODES[addon]}\t{mod}")
PY
