#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C02-11 — one embedded console dist; HOLD until the dist owner splits it.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-11-console-dist: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-11-console-dist: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0211_JSON:-design/c02-11-console-dist-hold.json}"
DOC="${OLIVARES_C0211_DOC:-design/C02-11-CONSOLE-DIST-HOLD-2026-08-20.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'NOT EXECUTED' "$DOC" || fail "$DOC lost NOT EXECUTED"
if grep -qiE 'FIRMA A claimed|FIRMA A is met|console split shipped|per-set dist landed' "$DOC"; then
	fail "$DOC claims a split this lote does not have"
fi

python3 - "$JSON" "$DOC" <<'PY' || fail "JSON/doc failed the C02-11 HOLD contract"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
doc = open(sys.argv[2], encoding="utf-8").read()
if data.get("schema") != "c02-11-console-dist-hold/v1":
    raise SystemExit("unknown schema")
if data.get("executed") is not False:
    raise SystemExit("executed must stay false")
if data.get("one_web_build_on_overlay_main") is not True:
    raise SystemExit("overlay main must still be one web build")
if data.get("one_web_build_on_overlay_pr75") is not True:
    raise SystemExit("producer PR must still be one web build")
if data.get("dist_split_is_orquestador_p") is not True:
    raise SystemExit("dist split owner must stay P")
if data.get("land_key_before_producer") is not True:
    raise SystemExit("land_key_before_producer is the measured half-stitch")
if data.get("overlay_pr_producer") != 75:
    raise SystemExit("producer PR pin drifted")
if data.get("u_f") != "UNKNOWN" or data.get("u_d") != "UNKNOWN":
    raise SystemExit("U_f/U_d must stay UNKNOWN")
for key in ("overlay_main_sha", "pr75_sha", "hub_sha"):
    val = data.get(key)
    if not isinstance(val, str) or not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
if data["overlay_main_sha"] == data["pr75_sha"]:
    raise SystemExit("overlay main and producer PR cannot share an object id")
if "HOLD" not in doc:
    raise SystemExit("doc lost HOLD")
PY

count_web_builds() {
	local f="$1"
	grep -cE 'pnpm --dir web run build' "$f" || true
}

ENT="${OLIVARES_ENT_DIR:-}"
if [ -n "$ENT" ]; then
	[ -f "$ENT/.goreleaser.yaml" ] || cannot "OLIVARES_ENT_DIR has no goreleaser file"
	n="$(count_web_builds "$ENT/.goreleaser.yaml")"
	[ "$n" = "1" ] || fail "overlay goreleaser web builds = $n, want 1 (C02-11 is still one dist)"
fi

say "check-c02-11-console-dist: CLEAN — one embedded console dist; HOLD; not executed."
exit 0
