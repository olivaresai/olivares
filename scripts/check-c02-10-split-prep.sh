#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02-10 unique leftover unique vs check-c02-10-split.sh (LOOK 2 on
# origin/main without the split doc) and unique leftover unique vs
# #995. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-10-split-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-10-split-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0210P_JSON:-design/c02-10-split-prep-2026-08-20.json}"
DOC="${OLIVARES_C0210P_DOC:-design/C02-10-SPLIT-CONNECTORS-PREP-2026-08-20.md}"
EMB="${OLIVARES_C0210P_EMB:-cmd/olivares/firstparty/embed.go}"
REL="${OLIVARES_C0210P_REL:-cmd/olivares/firstparty/embed_binaries_release.go}"

for f in "$JSON" "$DOC" "$EMB" "$REL"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c02-10-split.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original split check"
grep -F -q 'Unique leftover unique vs `#995`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #995"
grep -F -q 'Sidecar not landed' "$DOC" \
  || fail "prepare doc lost sidecar HOLD"
grep -F -q 'Community embed stays' "$DOC" \
  || fail "prepare doc lost community-embed HOLD"
grep -F -q 'Does not write core/release' "$DOC" \
  || fail "prepare doc lost core/release HOLD"
if grep -qiE 'sidecar landed|FIRMA A claimed|community embed was removed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

grep -q 'func Extract' "$EMB" \
  || fail "community Extract is gone — C02-10 must not delete the embed"
if grep -q 'OLIVARES_CONNECTOR_BUNDLE' "$EMB"; then
  fail "sidecar env landed — this HOLD lote does not apply C02-10"
fi
if grep -q 'extractFromThenBundle' "$EMB"; then
  fail "sidecar extract landed — this HOLD lote does not apply C02-10"
fi
if grep -qiE 'core/release' "$EMB"; then
  fail "C02-10 wrote core/release — that axis is N's"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c02-10-split-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c02-10-split-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c02-10-split-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("c02_10_applied") is not False:
    fail("c02_10_applied must stay false")
if data.get("sidecar_landed") is not False:
    fail("sidecar_landed must stay false")
if data.get("community_embed_stays") is not True:
    fail("community_embed_stays must stay true")
if data.get("core_release_written") is not False:
    fail("core_release_written must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c02-10-split-prep: CLEAN — sidecar not landed; community embed stays; core/release untouched."
exit 0
