#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for scripts/addon-sets.sh. HALF OF IT IS THE NON-FIRING DIRECTION, on purpose: a deriver
# that refuses every canon passes every "it refuses" case while making the cut impossible, and one
# that accepts everything passes every "it derives" case while shipping HOLD-gated modules to
# buyers who did not pay for them.
set -u

SCRIPT="${SCRIPT:-scripts/addon-sets.sh}"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/addon-sets-test.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0

ok()   { pass=$((pass+1)); printf '  ok    %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL  %s\n     %s\n' "$1" "$2"; }
skip() { printf '  SKIP  %s\n     %s\n' "$1" "$2"; }   # scoped, never counted as a pass

# A minimal canon carrying exactly the four add-ons, each with a plain `modules:` list.
base_canon() {
  cat <<'EOF'
# fixture
offers:
  self_hosted.business.addons.regulated:
    modules:
      - rtbf-depth
  self_hosted.business.addons.ai-runtime-security:
    modules:
      - content-firewall
  self_hosted.business.addons.compliance-packs:
    modules:
      - iso42001
  self_hosted.business.addons.identity-scale:
    modules:
      - durablebus
EOF
}

# ⛔ EVERY FIXTURE IS WRAPPED IN A ```yaml FENCE, because the real canon is fenced markdown and a
# fixture that is raw YAML is not a smaller canon — it is a different document. The assertions below
# were written against measured defects and NONE of them changes here: only the wrapper does, in one
# place, so every case is faithful to the shape of the thing it claims to test.
#
# It also buys coverage of both appendix paths for free: inside a fixture the appendix is a sibling
# of the offers INSIDE the fence, while in the real canon it deliberately sits AFTER the fence
# (the canon is cited by line number in ~200 places, so those entries go at the end). Both are read.
run() {
  # A canon that is ALREADY fenced markdown — the real one — is passed through untouched. Wrapping
  # it again produced a document with two fences, which the loader correctly refuses; that read as
  # "the deriver lost every module" when it was the harness handing it a broken file.
  if [ -r "$1" ] && ! grep -qE '^```ya?ml[[:space:]]*$' "$1"; then
    fenced="$TMP/fenced-$(basename "$1")"
    { printf '# canon fixture\n\n'; printf '```yaml\n'; cat "$1"; printf '```\n'; } > "$fenced"
    bash "$SCRIPT" "$fenced" 2>"$TMP/err"
  else
    bash "$SCRIPT" "$1" 2>"$TMP/err"
  fi
}

# ---- THE NON-FIRING DIRECTION: a well-formed canon must DERIVE, not refuse ------------------
base_canon > "$TMP/base.md"
out="$(run "$TMP/base.md")"; rc=$?
if [ "$rc" -eq 0 ] && [ "$(printf '%s\n' "$out" | wc -l)" -eq 4 ]; then
  ok "a well-formed canon derives all four sets"
else
  bad "a well-formed canon derives all four sets" "rc=$rc out=$(printf '%s' "$out" | tr '\n' ' ')"
fi

# The growth key ships in the same artifact (DISENO-ENTITLEMENT-POR-MODULO 2.1), so it must be
# INCLUDED — the refusals below must not be achieved by refusing everything unfamiliar.
{ base_canon | sed '/  self_hosted.business.addons.ai-runtime-security:/,/^  self_hosted.business.addons.compliance-packs:/ s/^    modules:$/    modules_day_one:/'; } > "$TMP/growth.md"
printf 'offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n  self_hosted.business.addons.ai-runtime-security:\n    modules_day_one:\n      - content-firewall\n    modules_growth:\n      - render-inspector\n  self_hosted.business.addons.compliance-packs:\n    modules:\n      - iso42001\n  self_hosted.business.addons.identity-scale:\n    modules:\n      - durablebus\n' > "$TMP/growth.md"
out="$(run "$TMP/growth.md")"; rc=$?
if [ "$rc" -eq 0 ] && printf '%s\n' "$out" | grep -q 'airs.render-inspector'; then
  ok "modules_day_one AND modules_growth both ship in the add-on"
else
  bad "modules_day_one AND modules_growth both ship in the add-on" "rc=$rc err=$(cat "$TMP/err")"
fi

# ---- ⛔ THE SECOND PLACE THE CANON ASSIGNS MODULES -------------------------------------------
# Appendix A assigns modules to an offer WITHOUT touching that offer's own list, because the canon
# is cited by line number in ~200 places and inserting mid-document would re-point all of them. A
# deriver that reads only the body lists UNDER-delivers: the buyer pays for a module the tag then
# excludes. That is the mirror of shipping unpaid code and just as wrong.
printf 'offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n  self_hosted.business.addons.ai-runtime-security:\n    modules:\n      - content-firewall\n  self_hosted.business.addons.compliance-packs:\n    modules:\n      - iso42001\n  self_hosted.business.addons.identity-scale:\n    modules:\n      - durablebus\n  modules_assigned_2026_08_08:\n    decision_status: decided\n    circuit-breaker:\n      line: self_hosted.business.addons.ai-runtime-security\n      evidence: cmd/olivares/circuitbreakerwiring.go:7-8\n      was: unsold\n' > "$TMP/assigned.md"
out="$(run "$TMP/assigned.md")"; rc=$?
if [ "$rc" -eq 0 ] && printf '%s\n' "$out" | grep -q "airs.circuit-breaker"; then
  ok "an appendix assignment is INCLUDED in its add-on"
else
  bad "an appendix assignment is INCLUDED in its add-on" "rc=$rc out=$(printf '%s' "$out" | tr '\n' ' ') err=$(cat "$TMP/err")"
fi

# ...but an assignment aimed at something that is NOT one of the four add-ons must not be quietly
# folded into one of them.
printf 'offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n  self_hosted.business.addons.ai-runtime-security:\n    modules:\n      - content-firewall\n  self_hosted.business.addons.compliance-packs:\n    modules:\n      - iso42001\n  self_hosted.business.addons.identity-scale:\n    modules:\n      - durablebus\n  modules_assigned_2026_08_08:\n    decision_status: decided\n    some-module:\n      line: cloud.standard\n' > "$TMP/assigned-bad.md"
run "$TMP/assigned-bad.md" >/dev/null; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'cloud.standard' "$TMP/err"; then
  ok "an appendix assignment to a non-add-on line REFUSES and names the target"
else
  bad "an appendix assignment to a non-add-on line REFUSES and names the target" "rc=$rc err=$(cat "$TMP/err")"
fi

# ---- ⛔ THE KEY THAT MUST NEVER BE SWALLOWED -------------------------------------------------
# HOLD-gated modules enter one by one as each passes its gate. Projecting them as shipped is the
# very defect the cut exists to prevent, reached from the inside.
printf 'offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n    modules_hold_gated:\n      - not-yet-allowed\n  self_hosted.business.addons.ai-runtime-security:\n    modules:\n      - content-firewall\n  self_hosted.business.addons.compliance-packs:\n    modules:\n      - iso42001\n  self_hosted.business.addons.identity-scale:\n    modules:\n      - durablebus\n' > "$TMP/hold.md"
out="$(run "$TMP/hold.md")"; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'modules_hold_gated' "$TMP/err"; then
  ok "modules_hold_gated REFUSES and names the key"
elif [ "$rc" -eq 0 ] && printf '%s\n' "$out" | grep -q 'not-yet-allowed'; then
  bad "modules_hold_gated REFUSES" "it SHIPPED a hold-gated module — this is the defect itself"
else
  bad "modules_hold_gated REFUSES and names the key" "rc=$rc err=$(cat "$TMP/err")"
fi

# modules_manifest is a SCALAR. Parsed as a list it yields garbage; globbed, it yields a module
# named after a manifest.
printf 'offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n    modules_manifest: cloud-standard-v1\n  self_hosted.business.addons.ai-runtime-security:\n    modules:\n      - content-firewall\n  self_hosted.business.addons.compliance-packs:\n    modules:\n      - iso42001\n  self_hosted.business.addons.identity-scale:\n    modules:\n      - durablebus\n' > "$TMP/scalar.md"
run "$TMP/scalar.md" >/dev/null; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'modules_manifest' "$TMP/err"; then
  ok "modules_manifest (a scalar) REFUSES and names the key"
else
  bad "modules_manifest (a scalar) REFUSES and names the key" "rc=$rc err=$(cat "$TMP/err")"
fi

# ---- THE REFUSAL MUST NOT DEPEND ON HOW THE KEY IS WRITTEN ------------------------------------
#
# A `sol max` contrast found that the first parser pinned every shape to an exact column, so the
# refusal turned on indentation, case and quoting. The first case below is not a style complaint: it
# is the measured counterexample in which a HOLD-GATED module is emitted as SHIPPING, rc=0, no
# warning. Re-indenting a file is enough to reach it.
TAIL='  self_hosted.business.addons.ai-runtime-security:\n    modules:\n      - content-firewall\n  self_hosted.business.addons.compliance-packs:\n    modules:\n      - iso42001\n  self_hosted.business.addons.identity-scale:\n    modules:\n      - durablebus\n'

# The one that shipped: `modules_hold_gated` nested DEEPER than the key above it. The old key
# pattern missed it, the old reset pattern missed it too, so the key stayed open and the gated
# entries were collected under `modules:`.
printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n      modules_hold_gated:\n      - dora-register\n$TAIL" > "$TMP/gated-deeper.md"
out="$(run "$TMP/gated-deeper.md")"; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'modules_hold_gated' "$TMP/err"; then
  ok "a deeper-indented modules_hold_gated REFUSES (it used to SHIP dora-register)"
else
  bad "a deeper-indented modules_hold_gated REFUSES (it used to SHIP dora-register)" "rc=$rc out=$out err=$(cat "$TMP/err")"
fi

printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n    Modules_Hold_Gated:\n      - dora-register\n$TAIL" > "$TMP/gated-case.md"
run "$TMP/gated-case.md" >/dev/null; rc=$?
if [ "$rc" -ne 0 ] && grep -qi 'hold_gated' "$TMP/err"; then
  ok "a differently-cased Modules_Hold_Gated REFUSES instead of passing unrecognised"
else
  bad "a differently-cased Modules_Hold_Gated REFUSES instead of passing unrecognised" "rc=$rc err=$(cat "$TMP/err")"
fi

printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n    \"modules_hold_gated\":\n      - dora-register\n$TAIL" > "$TMP/gated-quoted.md"
run "$TMP/gated-quoted.md" >/dev/null; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'modules_hold_gated' "$TMP/err"; then
  ok "a quoted \"modules_hold_gated\" REFUSES"
else
  bad "a quoted \"modules_hold_gated\" REFUSES" "rc=$rc err=$(cat "$TMP/err")"
fi

# ---- AND THE NON-FIRING DIRECTION, WHICH IS HALF THE VALUE ------------------------------------
#
# A parser that refuses everything passes every "it refuses" assertion above while deriving nothing.
# These two say what must still be READ. The trailing comment is not hypothetical: anchoring the
# item at end-of-line while hardening the above dropped six real modules from ids and reg, and only
# the counterfactual against the real canon caught it.
printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth           # annotated, like most of the canon\n$TAIL" > "$TMP/commented.md"
out="$(run "$TMP/commented.md")"; rc=$?
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'rtbf-depth'; then
  ok "an item with a trailing comment is still READ (the canon annotates most entries)"
else
  bad "an item with a trailing comment is still READ (the canon annotates most entries)" "rc=$rc out=$out err=$(cat "$TMP/err")"
fi

printf "offers:\n  self_hosted.business.addons.regulated:\n    modules: [rtbf-depth, incident-loop]\n$TAIL" > "$TMP/inline.md"
out="$(run "$TMP/inline.md")"; rc=$?
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'incident-loop'; then
  ok "an inline list is still a list: missing it would UNDER-deliver"
else
  bad "an inline list is still a list: missing it would UNDER-deliver" "rc=$rc out=$out err=$(cat "$TMP/err")"
fi

# ---- THE REFUSAL IS EXHAUSTIVE, NOT A LIST OF THE TWO WE THOUGHT OF ---------------------------
#
# A mutant that refused ONLY modules_hold_gated and modules_manifest survived the whole battery,
# because every refusal case named one of those two. The canon can grow a seventh modules* key
# tomorrow, and the answer to a key we have never seen must be "I do not understand this canon",
# not "it ships nothing".
printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n    modules_preview:\n      - futuro\n$TAIL" > "$TMP/unknown-key.md"
run "$TMP/unknown-key.md" >/dev/null; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'modules_preview' "$TMP/err"; then
  ok "a modules* key we have NEVER seen REFUSES and names it (the refusal is exhaustive)"
else
  bad "a modules* key we have NEVER seen REFUSES and names it (the refusal is exhaustive)" "rc=$rc err=$(cat "$TMP/err")"
fi

# ---- A REFUSAL PRINTS NOTHING, AND THAT IS A SEPARATE PROMISE ---------------------------------
#
# A mutant that wrote one row to stdout BEFORE exiting 3 survived too: every case checked the exit
# code and none checked the output. A consumer that pipes this into a build and trusts the stream
# would take that row. Refusing is only safe if nothing escapes with it.
out="$(run "$TMP/unknown-key.md" 2>/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && [ -z "$out" ]; then
  ok "a refusal writes NOTHING to stdout (a consumer reading the stream gets no partial answer)"
else
  bad "a refusal writes NOTHING to stdout (a consumer reading the stream gets no partial answer)" "rc=$rc out=[$out]"
fi

# ---- AND THE REAL CANON, BY MEMBERSHIP -- NOT JUST "IT EXITED ZERO" ---------------------------
#
# Two mutants that DROPPED a module survived every synthetic case: one omitted retrieval-scan, the
# other stopped after the first entry of the appendix block. Both still exited 0 on fixtures, and
# the sets were simply one row shorter. Under-delivery does not announce itself, so the anchors
# below name the entries whose loss those mutants cause -- caeptransmit and circuit-breaker come
# from the SECOND assignment place, which is exactly the one a naive reader misses.
# The canon lives in a root the public export curates out ON PURPOSE, so in an exported
# tree its absence is sanctioned, not a finding — the marker written by the curation
# pipeline (never tracked in the hub) is the discriminator, and :343 below already guards
# the same file. Without this the case answered FAIL from an exported tree.
if [ ! -r design/PRICING-CANON.md ] && [ "$(bash scripts/hub-leg.sh --classify 2>/dev/null)" = public ]; then
  skip "the real canon derives its known members" "design/PRICING-CANON.md curated out of the public export"
else
out="$(run design/PRICING-CANON.md 2>/dev/null)"; rc=$?
missing=""
for anchor in "airs	caeptransmit" "airs	circuit-breaker" "airs	retrieval-scan"; do
  printf '%s' "$out" | grep -qx "$anchor" || missing="$missing [$anchor]"
done
rows="$(printf '%s' "$out" | grep -c .)"
# A LOWER BOUND, not an equality: the canon is allowed to grow, and a growing set is somebody
# adding a module deliberately. SHRINKING is the failure mode, and it needs a human to look.
if [ "$rc" -eq 0 ] && [ -z "$missing" ] && [ "$rows" -ge 23 ]; then
  ok "the real canon derives its known members (rows=$rows, floor 23)"
else
  bad "the real canon derives its known members" "rc=$rc rows=$rows missing:$missing"
fi
fi

# ---- WIDER IS THE DANGEROUS DIRECTION ---------------------------------------------------------
#
# A set that is too SMALL loses a buyer modules they paid for, which is wrong and visible. A set
# that is too WIDE puts bytes in the artifact nobody bought, which is the thing the cut exists to
# prevent and is invisible: everything still exits 0.
#
# YAML forbids duplicate keys in a mapping, so a canon carrying `modules:` twice is malformed and
# every reader disagrees: a typed parser errors, most take the last, some the first. Reading line by
# line UNIONS them by accident, which is wider than EITHER reading.
printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n    modules:\n      - extra-from-duplicate\n$TAIL" > "$TMP/dup-key.md"
out="$(run "$TMP/dup-key.md" 2>/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'more than once' "$TMP/err"; then
  ok "a modules: key declared TWICE refuses (the union is wider than either reading)"
else
  bad "a modules: key declared TWICE refuses (the union is wider than either reading)" "rc=$rc out=$out err=$(cat "$TMP/err")"
fi

# A block scalar under a delivering key is not a list. Read as one it yields a module named after
# the prose inside it -- a phantom entry in a shipping set.
printf "offers:\n  self_hosted.business.addons.regulated:\n    modules: |\n      phantom-scalar\n      - rtbf-depth\n$TAIL" > "$TMP/block-scalar.md"
run "$TMP/block-scalar.md" >/dev/null; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'scalar value' "$TMP/err"; then
  ok "a block scalar under modules: refuses instead of deriving a phantom module"
else
  bad "a block scalar under modules: refuses instead of deriving a phantom module" "rc=$rc err=$(cat "$TMP/err")"
fi

# And the non-firing side of the same rule: DIFFERENT delivering keys are not duplicates. modules,
# modules_day_one and modules_growth legitimately coexist -- refusing those would break every
# add-on that grows, which is the canon's own 2.1.
printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n    modules_growth:\n      - later-module\n$TAIL" > "$TMP/two-keys.md"
out="$(run "$TMP/two-keys.md" 2>/dev/null)"; rc=$?
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'later-module'; then
  ok "modules and modules_growth together are NOT a duplicate (both ship, 2.1)"
else
  bad "modules and modules_growth together are NOT a duplicate (both ship, 2.1)" "rc=$rc out=$out err=$(cat "$TMP/err")"
fi

# ---- THE APPENDIX: THREE WIDENINGS THAT ALL EXITED ZERO ---------------------------------------
#
# The appendix assigns modules to an offer WITHOUT touching that offer's own list, so it is read
# separately -- and that reader had three independent ways to widen a set, every one of them silent:
# an UNDECIDED assignment shipped, a metadata block became a module, and the target was matched by
# its LAST SEGMENT so `cloud.ai-runtime-security` fed the SELF-HOSTED set.
APX='  modules_assigned_2026_08_08:\n'
printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n$TAIL$APX    decision_status: pending\n    future-module:\n      line: self_hosted.business.addons.ai-runtime-security\n      evidence: somewhere.go:1\n" > "$TMP/apx-pending.md"
out="$(run "$TMP/apx-pending.md" 2>/dev/null)"
if ! printf '%s' "$out" | grep -q 'future-module'; then
  ok "an appendix assignment with decision_status != decided is NOT projected"
else
  bad "an appendix assignment with decision_status != decided is NOT projected" "out=$out"
fi

printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n$TAIL$APX    decision_status: decided\n    wrong-line-module:\n      line: cloud.ai-runtime-security\n      evidence: somewhere.go:1\n" > "$TMP/apx-target.md"
out="$(run "$TMP/apx-target.md" 2>/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'cloud.ai-runtime-security' "$TMP/err"; then
  ok "an appendix target is matched WHOLE: cloud.<slug> does not feed the self-hosted set"
else
  bad "an appendix target is matched WHOLE: cloud.<slug> does not feed the self-hosted set" "rc=$rc out=$out err=$(cat "$TMP/err")"
fi

printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n$TAIL$APX    decision_status: decided\n    notes:\n      line: self_hosted.business.addons.ai-runtime-security\n" > "$TMP/apx-meta.md"
out="$(run "$TMP/apx-meta.md" 2>/dev/null)"; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'cites no evidence' "$TMP/err"; then
  ok "an appendix entry with no evidence refuses (that is how 'notes' became a module)"
else
  bad "an appendix entry with no evidence refuses (that is how 'notes' became a module)" "rc=$rc out=$out err=$(cat "$TMP/err")"
fi

printf "offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n$TAIL$APX    some-module:\n      line: self_hosted.business.addons.ai-runtime-security\n      evidence: somewhere.go:1\n" > "$TMP/apx-nostatus.md"
out="$(run "$TMP/apx-nostatus.md" 2>/dev/null)"; rc=$?
if [ "$rc" -ne 0 ]; then
  ok "an appendix block with NO decision_status refuses, including when it runs to EOF"
else
  bad "an appendix block with NO decision_status refuses, including when it runs to EOF" "rc=$rc out=$out err=$(cat "$TMP/err")"
fi

# ---- EMPTY IS NEVER AN ANSWER ----------------------------------------------------------------
printf 'offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n  self_hosted.business.addons.ai-runtime-security:\n    prices:\n      monthly: 1\n  self_hosted.business.addons.compliance-packs:\n    modules:\n      - iso42001\n  self_hosted.business.addons.identity-scale:\n    modules:\n      - durablebus\n' > "$TMP/empty.md"
run "$TMP/empty.md" >/dev/null; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'ZERO modules' "$TMP/err"; then
  ok "an add-on with no shipping modules REFUSES instead of deriving an empty tag"
else
  bad "an add-on with no shipping modules REFUSES instead of deriving an empty tag" "rc=$rc err=$(cat "$TMP/err")"
fi

# ---- ORTHOGONALITY ---------------------------------------------------------------------------
printf 'offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - shared-module\n  self_hosted.business.addons.ai-runtime-security:\n    modules:\n      - shared-module\n  self_hosted.business.addons.compliance-packs:\n    modules:\n      - iso42001\n  self_hosted.business.addons.identity-scale:\n    modules:\n      - durablebus\n' > "$TMP/dup.md"
run "$TMP/dup.md" >/dev/null; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'BOTH' "$TMP/err"; then
  ok "a module in two sets REFUSES (a buyer of one would get the other's bytes)"
else
  bad "a module in two sets REFUSES (a buyer of one would get the other's bytes)" "rc=$rc err=$(cat "$TMP/err")"
fi

# ---- A MISSING ADD-ON IS NOT A SMALLER ANSWER ------------------------------------------------
printf 'offers:\n  self_hosted.business.addons.regulated:\n    modules:\n      - rtbf-depth\n  self_hosted.business.addons.ai-runtime-security:\n    modules:\n      - content-firewall\n  self_hosted.business.addons.compliance-packs:\n    modules:\n      - iso42001\n' > "$TMP/partial.md"
run "$TMP/partial.md" >/dev/null; rc=$?
if [ "$rc" -ne 0 ] && grep -q 'identity-scale' "$TMP/err"; then
  ok "a missing add-on REFUSES a partial mapping"
else
  bad "a missing add-on REFUSES a partial mapping" "rc=$rc err=$(cat "$TMP/err")"
fi

# ---- THE THIRD ANSWER: could not measure ------------------------------------------------------
run "$TMP/does-not-exist.md" >/dev/null; rc=$?
if [ "$rc" -eq 2 ]; then
  ok "an unreadable canon exits 2 — 'I could not look' is not 'it is clean'"
else
  bad "an unreadable canon exits 2 — 'I could not look' is not 'it is clean'" "rc=$rc"
fi

# ---- AND THE REAL CANON MUST STILL DERIVE ----------------------------------------------------
if [ -r design/PRICING-CANON.md ]; then
  out="$(run design/PRICING-CANON.md)"; rc=$?
  n="$(printf '%s\n' "$out" | grep -c . )"
  codes="$(printf '%s\n' "$out" | cut -f1 | sort -u | tr '\n' ' ')"
  if [ "$rc" -eq 0 ] && [ "$n" -ge 12 ] && [ "$codes" = "airs cp ids reg " ]; then
    ok "the REAL canon derives all four codes ($n modules)"
  else
    bad "the REAL canon derives all four codes" "rc=$rc n=$n codes='$codes' err=$(cat "$TMP/err")"
  fi
elif [ "$(bash scripts/hub-leg.sh --classify 2>/dev/null)" = public ]; then
  skip "the REAL canon derives all four codes" "design/PRICING-CANON.md curated out of the public export"
else
  bad "the REAL canon derives all four codes" "design/PRICING-CANON.md not readable from $(pwd)"
fi

printf '\ntest-addon-sets: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
