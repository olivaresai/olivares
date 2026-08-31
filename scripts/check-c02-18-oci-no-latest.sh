#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c02-18-oci-no-latest.sh — C02-18. Per-set image, no commercial :latest.
# Registry not provisioned. 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-18-oci-no-latest: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-18-oci-no-latest: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0218_JSON:-design/c02-18-oci-no-latest.json}"
DOC="${OLIVARES_C0218_DOC:-design/C02-18-OCI-NO-LATEST-2026-08-19.md}"
HUBGR="${OLIVARES_C0218_HUBGR:-.goreleaser.yaml}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$HUBGR" ] || cannot "missing $HUBGR"

grep -q 'NOT PROVISIONED' "$DOC" || fail "$DOC lost NOT PROVISIONED"
if grep -qiE 'registry provisioned|published :latest as commercial|images pushed' "$DOC"; then
	fail "$DOC claims a publish this lote does not have"
fi
# Hub build already refuses a latest manifest (promotion is a later job).
grep -q 'NO `latest` manifest here' "$HUBGR" \
	|| fail "hub goreleaser lost the no-latest-at-build rule"

python3 - "$JSON" <<'PY' || fail "JSON failed the C02-18 contract"
import json, sys

want = [
    "olivares-enterprise:latest-amd64",
    "olivares-enterprise:latest-arm64",
    "olivares-enterprise:latest",
]
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c02-18-oci-no-latest/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("provisioned") is not False:
    raise SystemExit("provisioned must stay false")
if data.get("commercial_latest_forbidden") is not True:
    raise SystemExit("commercial_latest_forbidden must stay true")
if data.get("publish_per_set") is not True:
    raise SystemExit("publish_per_set must stay true")
if data.get("overlay_main_has_latest") is not True:
    raise SystemExit("overlay_main_has_latest must stay true until the producer lands")
got = data.get("latest_templates")
if not isinstance(got, list):
    raise SystemExit("latest_templates missing")
if set(got) != set(want):
    raise SystemExit("latest_templates must be the three-tag LIST, not a substitute")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
PY

say "check-c02-18-oci-no-latest: CLEAN — constraint written; registry not provisioned."
exit 0
