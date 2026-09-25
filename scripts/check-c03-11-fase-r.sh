#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C03-11 — FASE R stays HOLD until a customer binary exists to replace.
# Production runs the 2026-09-17 order (an internal design note (not shipped)).
# Exit 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT"

DOC="$(find design -maxdepth 1 -name 'C03-11-FASE-R-HOLD-*.md' -print 2>/dev/null | sort | tail -n 1 || true)"
SEAT="$ROOT/core/auth/seatcap.go"
WRANGLER="$ROOT/commercial/license-worker/wrangler.jsonc"

_cls=""
if [ -f "$ROOT/scripts/hub-leg.sh" ]; then
	_cls="$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null || true)"
fi
_public_partial=0
if [[ -z "$DOC" || ! -f "$DOC" ]]; then
	if [ "$_cls" = "public" ]; then
		echo "C03-11: SCOPED — public export; design/ HOLD doc is curated out of the published tree."
		_public_partial=1
	else
		echo "C03-11: COULD NOT LOOK — no FASE R HOLD prep doc under design/" >&2
		exit 2
	fi
fi
if [[ ! -f "$SEAT" ]]; then
	echo "C03-11: COULD NOT LOOK — core/auth/seatcap.go missing" >&2
	exit 2
fi
if [[ ! -f "$WRANGLER" ]]; then
	if [ "$_cls" = "public" ]; then
		echo "C03-11: SCOPED — public export; commercial/license-worker/wrangler.jsonc is curated out."
		_public_partial=1
	else
		echo "C03-11: COULD NOT LOOK — wrangler.jsonc missing" >&2
		exit 2
	fi
fi
if [ "$_public_partial" -eq 1 ]; then
	# Overlay/commercial witnesses do not exist here. The seat-cap no-op still
	# lives in Community core and is graded below; missing wrangler/doc is not a finding.
	DOC=""
	WRANGLER=""
fi

fail=0

if [[ -n "$DOC" ]]; then
	if ! grep -q 'HOLD' "$DOC"; then
		echo "C03-11: HOLD marker missing from $DOC" >&2
		fail=1
	fi
	if ! grep -q 'unlimitedSeatPolicy' "$DOC"; then
		echo "C03-11: overlay seats measurement missing from $DOC" >&2
		fail=1
	fi
	# Do not match "does not claim FIRMA A" — that is the HOLD sentence.
	if grep -qiE 'binaries replaced|FASE R complete|substitution is done' "$DOC"; then
		echo "C03-11: prep doc claims a completed substitution" >&2
		fail=1
	fi
fi

if ! grep -q 'func (a \*Authenticator) enforceSeatCapTx' "$SEAT"; then
	echo "C03-11: COULD NOT LOOK — enforceSeatCapTx not in seatcap.go" >&2
	exit 2
fi
# The function body must stay a bare return nil (the no-op). A later
# session that counts seats here re-opens the cap this HOLD is about.
body="$(awk '/func \(a \*Authenticator\) enforceSeatCapTx/,/^}/' "$SEAT")"
if ! grep -q 'return nil' <<<"$body"; then
	echo "C03-11: enforceSeatCapTx no longer returns nil" >&2
	fail=1
fi
if grep -qE 'MaxUsers|countSeats|limit' <<<"$body"; then
	echo "C03-11: enforceSeatCapTx body reads a seat figure" >&2
	fail=1
fi

# Production: the current contract (an internal design note (not shipped)), parsed as JSON
# from env.production.vars, so another environment's value can never answer for production.
# Production fulfilment is on since the 2026-09-17 order, the second of the three steps the HOLD
# doc names; this gate does not decide the other two, and FASE R stays HOLD.
if [[ -n "$WRANGLER" ]]; then
	STATE="$ROOT/design/PRODUCTION-STATE-2026-09-24.json"
	OFFERS="$ROOT/commercial/license-worker/config/production/dodo-checkout-offers.json"
	for f in "$STATE" "$OFFERS"; do
		if [[ ! -f "$f" ]]; then
			echo "C03-11: COULD NOT LOOK — ${f#"$ROOT"/} missing" >&2
			exit 2
		fi
	done
	crc=0
	python3 - C03-11 "$WRANGLER" "$STATE" "$OFFERS" <<'PY' || crc=$?
# THE CURRENT PRODUCTION CONTRACT, the same block in every gate that reads production state
# (an internal design note (not shipped)): the 2026-09-17 order's sale switch and version, the
# four Business configurations by id in every sold place, and no Cloud id in any of them.
import json, sys

gate, wf, state_path, offers_path = sys.argv[1:5]
found = []

def cannot(msg):
    print(f"{gate}: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

def strip_jsonc(s):
    out, i, n, in_str, esc = [], 0, len(s), False, False
    while i < n:
        c = s[i]
        if in_str:
            out.append(c)
            if esc:
                esc = False
            elif c == "\\":
                esc = True
            elif c == '"':
                in_str = False
            i += 1
            continue
        if c == '"':
            in_str = True
            out.append(c)
            i += 1
            continue
        if c == "/" and i + 1 < n and s[i + 1] == "/":
            while i < n and s[i] != "\n":
                i += 1
            continue
        out.append(c)
        i += 1
    return "".join(out)

def load(path, jsonc=False):
    try:
        text = open(path, encoding="utf-8").read()
        return json.loads(strip_jsonc(text) if jsonc else text)
    except Exception as e:
        cannot(f"{path} is not readable JSON: {e}")

state = load(state_path)
configs = state.get("configurations") or {}
cloud_ids = {i for fam in (state.get("not_sold") or {}).values() for i in (fam.get("ids") or [])}
if state.get("schema") != "production-state/v1" or len(configs) != 4 or not cloud_ids:
    cannot(f"{state_path} does not name four configurations and the not-sold Cloud ids")

prod = (((load(wf, jsonc=True).get("env") or {}).get("production") or {}).get("vars") or {})
for key, value in (("FULFILLMENT_ENABLED", "true"), ("ENTERPRISE_VERSION", "26.9.0")):
    if prod.get(key) != value:
        found.append(f"production {key} is {prod.get(key)!r}, want {value!r} (the 2026-09-17 order)")

def var(name):
    raw = prod.get(name)
    if not raw:
        found.append(f"production {name} is missing")
        return {}
    try:
        return json.loads(raw)
    except Exception as e:
        cannot(f"production {name} is not JSON: {e}")

catalog = var("DODO_CATALOG")
product_map = var("PRODUCT_MAP").get("products") or {}
offer_sets = [(offers_path, load(offers_path).get("offers") or {})]
if "DODO_CHECKOUT" in prod:
    offer_sets.append(("production DODO_CHECKOUT", var("DODO_CHECKOUT").get("offers") or {}))

places = {f"DODO_CATALOG {k}": set(catalog.get(k) or {})
          for k in ("products", "addons", "cadence_months", "bundles", "set_codes")}
places["PRODUCT_MAP"] = set(product_map)

for name, conf in sorted(configs.items()):
    for cadence in ("monthly", "annual"):
        pid = conf.get(cadence)
        wanted = ["DODO_CATALOG products", "DODO_CATALOG cadence_months", "PRODUCT_MAP"]
        if conf.get("kind") == "bundle":
            wanted.append("DODO_CATALOG bundles")
        for where in wanted:
            if pid not in places[where]:
                found.append(f"configuration {name} {cadence} {pid} is missing from production {where}")
        if pid in product_map and (product_map[pid] or {}).get("edition") != "Business":
            found.append(f"configuration {name} {cadence} {pid} is not edition Business in production PRODUCT_MAP")
        for where, offers in offer_sets:
            if ((offers.get(name) or {}).get(cadence) or {}).get("product_id") != pid:
                found.append(f"configuration {name} {cadence} {pid} is missing from {where}")

catalogue = "production sells no Cloud, whatever the flags"
for pid in catalog.get("cloud_products") or []:
    found.append(f"CFG-12 catalogue: production DODO_CATALOG cloud_products lists {pid} — {catalogue}")
for where, ids in places.items():
    for pid in sorted(ids & cloud_ids):
        found.append(f"CFG-12 catalogue: Cloud id {pid} is in production {where} — {catalogue}")
for pid, entry in sorted(product_map.items()):
    entry = entry or {}
    if pid not in cloud_ids and (entry.get("edition") == "Cloud" or str(entry.get("plan", "")).startswith("cloud-")):
        found.append(f"CFG-12 catalogue: production PRODUCT_MAP maps {pid} to Cloud {entry.get('plan')!r} — {catalogue}")
for where, offers in offer_sets:
    for key, cadences in sorted(offers.items()):
        ids = {(c or {}).get("product_id") for c in (cadences or {}).values() if isinstance(c, dict)}
        if key in (state.get("not_sold") or {}) or key.startswith("cloud") or ids & cloud_ids:
            found.append(f"CFG-12 catalogue: {where} offers {key!r} — {catalogue}")

for line in found:
    print(f"{gate}: FAIL — {line}", file=sys.stderr)
sys.exit(1 if found else 0)
PY
	case "$crc" in
		0) ;;
		1) fail=1 ;;
		*) exit 2 ;;
	esac
fi

if [[ "$fail" -ne 0 ]]; then
	echo "C03-11: $fail finding(s)" >&2
	exit 1
fi
if [ "$_public_partial" -eq 1 ]; then
	echo "C03-11: PARTIALLY APPLICABLE — seat seam still no-op; overlay HOLD doc and"
	echo "  the production contract were not graded (curated out of this tree)."
	exit 0
fi
echo "C03-11: CLEAN — FASE R remains HOLD; seat seam still no-op; production runs the 2026-09-17 order: four Business configurations, no Cloud"
exit 0
