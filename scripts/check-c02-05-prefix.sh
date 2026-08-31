#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c02-05-prefix.sh — C02-05. Basename prefix aligned; delivery open.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-05-prefix: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-05-prefix: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0205_JSON:-design/c02-05-prefix.json}"
DOC="${OLIVARES_C0205_DOC:-design/C02-05-PREFIX-2026-08-19.md}"
ART="${OLIVARES_C0205_ART:-commercial/license-worker/src/download/artifacts.ts}"
ENGINE="${OLIVARES_C0205_ENGINE:-core/release/manifest.go}"
BACKLOG="${OLIVARES_C0205_BACKLOG:-design/BACKLOG-COMPLETITUD-2026-08-16.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ART" ] || cannot "missing $ART"
[ -f "$ENGINE" ] || cannot "missing $ENGINE"
[ -f "$BACKLOG" ] || cannot "missing $BACKLOG"

grep -q 'PREFIX ALIGNED' "$DOC" || fail "$DOC lost PREFIX ALIGNED"
grep -q 'delivery NOT CLOSED' "$DOC" || fail "$DOC lost delivery NOT CLOSED"
if grep -qiE 'delivery closed|V-09 closed|404 gone' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi
grep -q 'C02-05' "$BACKLOG" || fail "$BACKLOG lost the C02-05 row"

python3 - "$JSON" "$ART" "$ENGINE" <<'PY' || fail "JSON/artifacts failed the C02-05 contract"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
art = open(sys.argv[2], encoding="utf-8").read()
eng = open(sys.argv[3], encoding="utf-8").read()

if data.get("schema") != "c02-05-prefix/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("prefix") != "olivares":
    raise SystemExit("prefix must stay olivares")
if data.get("wrong_prefix") != "olivares-enterprise":
    raise SystemExit("wrong_prefix must stay olivares-enterprise")
if data.get("prefix_mismatch_on_hub_main") is not False:
    raise SystemExit("prefix_mismatch_on_hub_main must stay false")
if data.get("delivery_404_closed") is not False:
    raise SystemExit("delivery_404_closed must stay false")
if data.get("r2_objects_verified") is not False:
    raise SystemExit("r2_objects_verified must stay false")
if data.get("variant_axis_in_worker") is not False:
    raise SystemExit("variant_axis_in_worker must stay false on this SHA")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

assigns = re.findall(r'ARTIFACT_BASENAME_PREFIX\s*=\s*"([^"]+)"', art)
if assigns != ["olivares"]:
    raise SystemExit("ARTIFACT_BASENAME_PREFIX assignment is %r" % assigns)
if re.search(r'ARTIFACT_BASENAME_PREFIX\s*=\s*"olivares-enterprise"', art):
    raise SystemExit("Worker prefix assignment is the wrong basename")
if "olivares_%s_%s_%s.tar.gz" not in eng and 'return fmt.Sprintf("olivares_%s_%s_%s.tar.gz"' not in eng:
    raise SystemExit("engine ExpectedArtifactName lost the olivares_ basename")
PY

say "check-c02-05-prefix: CLEAN — prefix is olivares_; delivery still open."
exit 0
