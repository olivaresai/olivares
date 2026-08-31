#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c02-12-boot-seams.sh — C02-12. Boot invariant not closed.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-12-boot-seams: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-12-boot-seams: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0212_JSON:-design/c02-12-boot-seams.json}"
DOC="${OLIVARES_C0212_DOC:-design/C02-12-BOOT-SEAMS-2026-08-19.md}"
WIRE="${OLIVARES_C0212_WIRE:-cmd/olivares/wire_noenterprise.go}"
ART="${OLIVARES_C0212_ART:-design/ARTEFACTOS-POR-PACK-2026-08-08.md}"
BACKLOG="${OLIVARES_C0212_BACKLOG:-design/BACKLOG-COMPLETITUD-2026-08-16.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$WIRE" ] || cannot "missing $WIRE"
[ -f "$ART" ] || cannot "missing $ART"
[ -f "$BACKLOG" ] || cannot "missing $BACKLOG"

grep -q 'NOT CLOSED' "$DOC" || fail "$DOC lost NOT CLOSED"
if grep -qiE 'invariant closed|boot abort gone|44 seams all inert' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi
grep -q 'C02-12' "$BACKLOG" || fail "$BACKLOG lost the C02-12 row"
grep -q 'preserved_on_every_lapse\|ningún seam retirado por tag' "$ART" \
	|| fail "$ART lost the boot-contract invariant"

python3 - "$JSON" "$WIRE" <<'PY' || fail "JSON/wire failed the C02-12 contract"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
wire = open(sys.argv[2], encoding="utf-8").read()

if data.get("schema") != "c02-12-boot-seams/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("invariant_closed") is not False:
    raise SystemExit("invariant_closed must stay false")
if data.get("executed") is not False:
    raise SystemExit("executed must stay false")
if data.get("constructors") != 46:
    raise SystemExit("constructors must stay 46")
if data.get("backlog_claimed") != 44:
    raise SystemExit("backlog_claimed must stay 44 (the stale figure)")
if data.get("boot_aborting") != ["newDurableBus"]:
    raise SystemExit("boot_aborting must stay [newDurableBus]")
if data.get("boot_aborting_count") != 1:
    raise SystemExit("boot_aborting_count must stay 1")
if data.get("fmt_errorf_count") != 1:
    raise SystemExit("fmt_errorf_count must stay 1")
if data.get("caep_returns_error_tuple") is not True:
    raise SystemExit("CAEP still returns an error tuple")
if data.get("caep_always_nil_nil") is not True:
    raise SystemExit("CAEP still always returns nil, nil")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

funcs = re.findall(r"^func (\w+)", wire, flags=re.M)
if len(funcs) != data["constructors"]:
    raise SystemExit("live constructor count %d != pinned %d" % (len(funcs), data["constructors"]))
if "newDurableBus" not in funcs:
    raise SystemExit("wire lost newDurableBus")
if "newCAEPTransmitter" not in funcs:
    raise SystemExit("wire lost newCAEPTransmitter")
n_err = len(re.findall(r"fmt\.Errorf", wire))
if n_err != data["fmt_errorf_count"]:
    raise SystemExit("live fmt.Errorf count %d != pinned %d" % (n_err, data["fmt_errorf_count"]))
if "return nil, fmt.Errorf" not in wire:
    raise SystemExit("wire lost the boot-aborting error return")

def body(src, name):
    m = re.search(r"^func %s\b" % re.escape(name), src, flags=re.M)
    if not m:
        return ""
    rest = src[m.start():]
    nxt = re.search(r"^func ", rest[5:], flags=re.M)
    return rest if not nxt else rest[: nxt.start() + 5]

caep = body(wire, "newCAEPTransmitter")
if "(caepTransmitter, error)" not in caep:
    raise SystemExit("newCAEPTransmitter lost its error tuple")
if "return nil, nil" not in caep:
    raise SystemExit("newCAEPTransmitter lost the nil, nil return")
durable = body(wire, "newDurableBus")
if "return nil, fmt.Errorf" not in durable:
    raise SystemExit("newDurableBus lost the boot-aborting error return")
PY

say "check-c02-12-boot-seams: CLEAN — 46 constructors; newDurableBus still aborts boot; invariant open."
exit 0
