#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02 producer-by-set R2 unique leftover unique vs #944/#928/#889 and
# check-r2-set-key.sh (original OPEN CHECK would FAIL on origin/main).
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-r2-set-key-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-r2-set-key-prep: COULD NOT LOOK — $*" >&2; exit 2; }

if [ -n "${OLIVARES_ROOT:-}" ]; then
  ROOT="$OLIVARES_ROOT"
else
  ROOT="$(
    cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
    pwd
  )" || cannot "cannot resolve repository root"
fi
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C02R2P_JSON:-design/c02-r2-set-key-prep-2026-08-20.json}"
DOC="${OLIVARES_C02R2P_DOC:-design/C02-R2-SET-KEY-PREP-2026-08-20.md}"
ART="${OLIVARES_C02R2P_ART:-commercial/license-worker/src/download/artifacts.ts}"
SETS="${OLIVARES_C02R2P_SETS:-commercial/license-worker/src/download/sets.ts}"
GATE="${OLIVARES_C02R2P_GATE:-commercial/license-worker/src/download/gate.ts}"
PUB="${OLIVARES_C02R2P_PUB:-scripts/publish-enterprise-artifacts.sh}"

for f in "$JSON" "$DOC" "$ART" "$SETS" "$GATE" "$PUB"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"
command -v node >/dev/null || cannot "no node"

grep -F -q 'Unique leftover unique vs `#944`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #944"
grep -F -q 'Unique leftover unique vs `#928`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #928"
grep -F -q 'Unique leftover unique vs `#889`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #889"
grep -F -q 'Unique leftover unique vs `check-r2-set-key.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original CHECK"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Remainder is legacyMonolithKey/setOrErr/isFullCommercialSet — not applied.' "$DOC" \
  || fail "prepare doc lost remainder HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|legacyMonolithKey landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

node --input-type=module - "$ART" "$SETS" <<'NODE' \
  || fail "artifactKey executable contract failed"
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const moduleURL = pathToFileURL(resolve(process.argv[2])).href;
const setsURL = pathToFileURL(resolve(process.argv[3])).href;
const { artifactKey } = await import(`${moduleURL}?c02-r2-check=${Date.now()}`);
const { ALLOWED_SET_SLUGS, isAllowedSetSlug } = await import(
  `${setsURL}?c02-r2-check=${Date.now()}`
);
if (typeof artifactKey !== "function") {
  throw new Error("artifactKey is not exported as a function");
}
if (artifactKey.length !== 4) {
  throw new Error(`artifactKey arity is ${artifactKey.length}, want 4`);
}
// This is an independent contract oracle on purpose. Deriving the expected
// universe from ALLOWED_SET_SLUGS would let removal of one paid set make both
// the implementation and this check agree on the same regression.
const expected = [
  "biz",
  "biz+airs",
  "biz+cp",
  "biz+ids",
  "biz+reg",
  "biz+airs+cp",
  "biz+airs+ids",
  "biz+airs+reg",
  "biz+cp+ids",
  "biz+cp+reg",
  "biz+ids+reg",
  "biz+airs+cp+ids",
  "biz+airs+cp+reg",
  "biz+airs+ids+reg",
  "biz+cp+ids+reg",
  "biz+airs+cp+ids+reg",
  "ent",
];
const allowed = [...ALLOWED_SET_SLUGS];
if (
  allowed.length !== expected.length ||
  expected.some((set) => !ALLOWED_SET_SLUGS.has(set)) ||
  allowed.some((set) => !expected.includes(set))
) {
  throw new Error(
    `ALLOWED_SET_SLUGS=${JSON.stringify(allowed)}, want ${JSON.stringify(expected)}`,
  );
}
for (const set of expected) {
  if (!isAllowedSetSlug(set)) {
    throw new Error(`isAllowedSetSlug rejected paid set ${JSON.stringify(set)}`);
  }
  const got = artifactKey("v26.8.0", "linux", "amd64", set);
  const want = `enterprise/v26.8.0/${set}/olivares_v26.8.0_linux_amd64.tar.gz`;
  if (got !== want) {
    throw new Error(`artifactKey(${set})=${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
}
for (const invalid of [
  "",
  "not-a-set",
  "attacker",
  "all",
  "biz+unknown",
  "reg+biz",
  "biz+reg+reg",
  "ent+biz",
]) {
  if (isAllowedSetSlug(invalid)) {
    throw new Error(`isAllowedSetSlug accepted ${JSON.stringify(invalid)}`);
  }
  let rejected = false;
  try {
    artifactKey("v26.8.0", "linux", "amd64", invalid);
  } catch {
    rejected = true;
  }
  if (!rejected) {
    throw new Error(`artifactKey accepted non-allowlisted set ${JSON.stringify(invalid)}`);
  }
}
NODE
if grep -q 'function legacyMonolithKey' "$ART"; then
  fail "legacyMonolithKey landed — this HOLD lote does not apply #944"
fi
if grep -q 'setOrErr' "$GATE"; then
  fail "setOrErr landed — this HOLD lote does not apply #944"
fi
if grep -q 'isFullCommercialSet' "$GATE"; then
  fail "isFullCommercialSet landed — this HOLD lote does not apply #944"
fi
grep -q 'const purchased = setSlug(live);' "$GATE" \
  || fail "gate no longer derives purchased from setSlug(live)"
grep -q 'artifactKey(version, os, arch, purchased)' "$GATE" \
  || fail "gate no longer passes purchased into artifactKey"
grep -Fq 'downloadAuditLabel(version, purchased, os, arch)' "$GATE" \
  || fail "binary download audit no longer records the purchased set"
grep -Fq 'enterprise/${VERSION}/${SET}/$(basename' "$PUB" \
  || fail "publisher tarball key lost /<set>/"
if grep -Fq 'enterprise/${VERSION}/$(basename' "$PUB"; then
  fail "publisher still plans an unscoped enterprise/\${VERSION}/ tarball"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c02-r2-set-key-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c02-r2-set-key-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c02-r2-set-key-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("artifact_key_set_keyed") is not True:
    fail("artifact_key_set_keyed must stay true")
if data.get("gate_passes_purchased_set") is not True:
    fail("gate_passes_purchased_set must stay true")
if data.get("publisher_set_path") is not True:
    fail("publisher_set_path must stay true")
if data.get("legacy_monolith_key_landed") is not False:
    fail("legacy_monolith_key_landed must stay false")
if data.get("set_or_err_landed") is not False:
    fail("set_or_err_landed must stay false")
if data.get("is_full_commercial_set_landed") is not False:
    fail("is_full_commercial_set_landed must stay false")
if data.get("remainder_applied") is not False:
    fail("remainder_applied must stay false")
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

say "check-c02-r2-set-key-prep: CLEAN — set-keyed key+gate+publisher already on main; legacyMonolithKey HOLD; overlay remasure not in this gate."
exit 0
