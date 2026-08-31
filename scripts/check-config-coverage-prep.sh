#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# a repository gate unique leftover unique vs check-config-coverage.sh (named on
# main, CHECK not in lint:addon-sets) and unique leftover unique vs
# #1460. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-config-coverage-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-config-coverage-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_CFGCOVP_JSON:-design/gat-13-config-coverage-prep-2026-08-20.json}"
DOC="${OLIVARES_CFGCOVP_DOC:-design/GAT-13-CONFIG-COVERAGE-PREP-2026-08-20.md}"
ORIG="${OLIVARES_CFGCOVP_ORIG:-scripts/check-config-coverage.sh}"
TF="${OLIVARES_CFGCOVP_TF:-Taskfile.yml}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$ORIG" ] || cannot "missing $ORIG"
[ -r "$TF" ] || cannot "missing $TF"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-config-coverage.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original config-coverage check"
grep -F -q 'Unique leftover unique vs `#1460`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1460"
grep -F -q 'Does not write docs-site' "$DOC" \
  || fail "prepare doc lost docs-site HOLD"
grep -F -q 'Does not close the twelve undocumented variables' "$DOC" \
  || fail "prepare doc lost undocumented-vars HOLD"
if grep -qiE 'docs-site rewritten|100 % closed|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi

grep -q 'OLIVARES_CONFIG_DOC_FLOOR' "$ORIG" \
  || fail "original check-config-coverage.sh lost the ratchet floor"
grep -F -q 'OLIVARES_CONFIG_DOC_FLOOR=${OLIVARES_CONFIG_DOC_FLOOR:-7}' "$TF" \
  || fail "named lint:config-coverage lost floor 7"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-config-coverage-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-config-coverage-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

want_undoc = [
    "OLIVARES_A2A_CONFORMANCE_JWKS",
    "OLIVARES_A2A_CONFORMANCE_URL",
    "OLIVARES_CODEX_LIVE",
    "OLIVARES_CODEX_LIVE_HOME",
    "OLIVARES_DEFINITELY_UNSET_KEY_XYZ",
    "OLIVARES_ERROR_MAPPER_SCAN",
    "OLIVARES_GATE_BINDIR",
    "OLIVARES_MCP_CONFORMANCE_URL",
    "OLIVARES_TEST_PG_BINDIR",
    "OLIVARES_TEST_POSTGRES_DSN",
    "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN",
    "OLIVARES_TEST_VECTOR_DSN",
]
if data.get("schema") != "gat-13-config-coverage-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("documented") != 53:
    fail("documented drifted from live 53")
if data.get("n_read") != 65:
    fail("n_read drifted from live 65")
if data.get("named_floor") != 7:
    fail("named_floor must stay 7")
if data.get("ratchet_floor") != 53:
    fail("ratchet_floor must stay 53")
if data.get("undocumented") != want_undoc:
    fail("undocumented drifted from live twelve tokens")
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

say "check-config-coverage-prep: CLEAN — live 53/65 pinned; named floor 7; docs-site not rewritten."
exit 0
