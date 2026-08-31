#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# a repository gate unique leftover unique vs check-cli-coverage.sh (named on
# main, CHECK not in lint:addon-sets). 0 CLEAN · 1 finding · 2 could
# not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cli-coverage-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cli-coverage-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_CLICOVP_JSON:-design/gat-12-cli-coverage-prep-2026-08-20.json}"
DOC="${OLIVARES_CLICOVP_DOC:-design/GAT-12-CLI-COVERAGE-PREP-2026-08-20.md}"
ORIG="${OLIVARES_CLICOVP_ORIG:-scripts/check-cli-coverage.sh}"
TF="${OLIVARES_CLICOVP_TF:-Taskfile.yml}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$ORIG" ] || cannot "missing $ORIG"
[ -r "$TF" ] || cannot "missing $TF"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-cli-coverage.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original cli-coverage check"
grep -F -q 'Does not write docs-site' "$DOC" \
  || fail "prepare doc lost docs-site HOLD"
grep -F -q 'Does not close the five undocumented verbs' "$DOC" \
  || fail "prepare doc lost undocumented-verbs HOLD"
if grep -qiE 'docs-site rewritten|100 % closed|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi

grep -q 'OLIVARES_CLI_DOC_FLOOR' "$ORIG" \
  || fail "original check-cli-coverage.sh lost the ratchet floor"
grep -F -q 'OLIVARES_CLI_DOC_FLOOR=${OLIVARES_CLI_DOC_FLOOR:-12}' "$TF" \
  || fail "named lint:cli-coverage lost floor 12"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-cli-coverage-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-cli-coverage-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

want_undoc = ["l3-render-probe", "olivares", "probe", "sig", "x"]
if data.get("schema") != "gat-12-cli-coverage-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("documented") != 321:
    fail("documented drifted from live 321")
if data.get("n_cmd") != 326:
    fail("n_cmd drifted from live 326")
if data.get("named_floor") != 12:
    fail("named_floor must stay 12")
if data.get("ratchet_floor") != 321:
    fail("ratchet_floor must stay 321")
if data.get("undocumented") != want_undoc:
    fail("undocumented drifted from live five tokens")
if data.get("docs_site_rewritten") is not False:
    fail("docs_site_rewritten must stay false")
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

say "check-cli-coverage-prep: CLEAN — live 321/326 pinned; named floor 12; docs-site not rewritten."
exit 0
