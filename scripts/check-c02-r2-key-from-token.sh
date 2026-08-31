#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C02-04 remasure: binary key is f(token, os, arch, grants.set).
# Query ?set= / ?variant= stay 400. Delivery NOT CLOSED.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-r2-key-from-token: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-r2-key-from-token: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C02KEY_JSON:-design/c02-r2-key-from-token.json}"
DOC="${OLIVARES_C02KEY_DOC:-design/C02-R2-KEY-FROM-TOKEN-2026-08-19.md}"
ART="${OLIVARES_C02KEY_ART:-commercial/license-worker/src/download/artifacts.ts}"
GATE="${OLIVARES_C02KEY_GATE:-commercial/license-worker/src/download/gate.ts}"
TEST="${OLIVARES_C02KEY_TEST:-commercial/license-worker/test/download.test.ts}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ART" ] || cannot "missing $ART"
[ -f "$GATE" ] || cannot "missing $GATE"
[ -f "$TEST" ] || cannot "missing $TEST"

grep -q 'delivery NOT CLOSED' "$DOC" || fail "$DOC lost delivery NOT CLOSED"
if grep -qiE 'delivery_404_closed stays true|bytes are real|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" "$ART" "$GATE" "$TEST" <<'PY' || fail "JSON/worker failed the C02-04 remasure"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
art = open(sys.argv[2], encoding="utf-8").read()
gate = open(sys.argv[3], encoding="utf-8").read()
test = open(sys.argv[4], encoding="utf-8").read()

if data.get("schema") != "c02-r2-key-from-token/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("binary_key_includes_set") is not True:
    raise SystemExit("binary_key_includes_set must stay true")
if data.get("binary_key_includes_variant") is not False:
    raise SystemExit("binary_key_includes_variant must stay false")
if data.get("derivation") != "f(token.version, os, arch, grants.set)":
    raise SystemExit("derivation must stay grants.set")
if data.get("set_on_binary_query") != "refused":
    raise SystemExit("set_on_binary_query must stay refused")
if data.get("variant_on_binary_path") != "refused":
    raise SystemExit("variant_on_binary_path must stay refused")
if data.get("producer_on_overlay_main") is not False:
    raise SystemExit("producer must stay off overlay main")
if data.get("delivery_404_closed") is not False:
    raise SystemExit("delivery_404_closed must stay false")
if data.get("r2_objects_verified") is not False:
    raise SystemExit("r2_objects_verified must stay false")
if data.get("overlay_main_blobs_directory") != "enterprise/{{ .Version }}":
    raise SystemExit("overlay blobs directory pin drifted")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

sig = re.search(r"export function artifactKey\(([^)]*)\)", art)
if not sig:
    raise SystemExit("artifactKey signature missing")
params = [p.strip() for p in sig.group(1).split(",") if p.strip()]
if len(params) != 4:
    raise SystemExit("artifactKey must take four params, got %s" % params)
if "enterprise/${version}/${set}/" not in art:
    raise SystemExit("artifacts.ts lost the grants-derived set path")
if re.search(r"export function artifactKey\(version: string, os: string, arch: string\): string", art):
    raise SystemExit("artifacts.ts reverted to the three-argument key")
if "variant is not a binary download query" not in gate:
    raise SystemExit("gate lost the variant refusal")
if "set is the manifest axis, not a binary key" not in gate:
    raise SystemExit("gate lost the set-on-binary refusal")
if 'url.searchParams.has("variant")' not in gate:
    raise SystemExit("gate does not test has(variant)")
if 'url.searchParams.has("set")' not in gate:
    raise SystemExit("gate does not test has(set) on the binary path")
if "set is the manifest axis" not in test:
    raise SystemExit("download tests lost the set-on-binary mutant")
if "variant is not a binary download query" not in test:
    raise SystemExit("download tests lost the variant mutant")
if "engine-shaped query" not in test:
    raise SystemExit("download tests lost the engine-shaped no-fire")
PY

say "check-c02-r2-key-from-token: CLEAN — 4-arg grants.set key; query set/variant refused; delivery open."
exit 0
