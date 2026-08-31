#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(
  cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
  pwd
)" || exit 2
CHECK="$ROOT/scripts/check-c02-r2-set-key-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c02r2prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
if [ ! -r "$ROOT/scripts/publish-enterprise-artifacts.sh" ]; then
  printf 'SKIP %s: publish-enterprise-artifacts.sh es hub-only y no esta en este arbol\n' \
    "$(basename "${BASH_SOURCE[0]}")"
  exit 0
fi

pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts" \
    "$TMP/tree/commercial/license-worker/src/download"
  cp "$ROOT/design/c02-r2-set-key-prep-2026-08-20.json" "$TMP/tree/design/"
  cp "$ROOT/design/C02-R2-SET-KEY-PREP-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/commercial/license-worker/src/download/artifacts.ts" \
    "$TMP/tree/commercial/license-worker/src/download/"
  cp "$ROOT/commercial/license-worker/src/download/sets.ts" \
    "$TMP/tree/commercial/license-worker/src/download/"
  cp "$ROOT/commercial/license-worker/src/download/gate.ts" \
    "$TMP/tree/commercial/license-worker/src/download/"
  # export-closure: hub-only scripts/publish-enterprise-artifacts.sh — el publicador de artefactos enterprise NO viaja
  # en el arbol publicado, y este test lo COPIA a su arbol de pruebas. Alli el `cp`
  # moriria por `set -e` y el rojo no seria del test. Guarda EN EL SITIO DE LLAMADA —no
  # basta una salida temprana, que alguien puede retirar dejando el cp desnudo— y con
  # `if`, nunca `[ -f X ] && cp`: una lista que acaba en `&&` con el lado izquierdo
  # falso devuelve 1 y `set -e` mata el guion.
  if [ -f "$ROOT/scripts/publish-enterprise-artifacts.sh" ]; then
    cp "$ROOT/scripts/publish-enterprise-artifacts.sh" "$TMP/tree/scripts/"
  fi
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-c02-r2-set-key-prep.sh"
}
run() {
  local rc=0
  unset OLIVARES_ENT_DIR || true
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-r2-set-key-prep.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "hub-safe C02 R2 pin is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = '  return `enterprise/${version}/${set}/${artifactFilename(version, os, arch)}`;'
new = '''  const scoped = `enterprise/${version}/${set}/${artifactFilename(version, os, arch)}`;
  return scoped.replace(`/${set}/`, "/all/");'''
if s.count(old) != 1:
    raise SystemExit("artifactKey mutant fixture did not match exactly once")
p.write_text(s.replace(old, new), encoding="utf-8")
PY
mutant_key="$(node --input-type=module - \
  "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'NODE'
import { pathToFileURL } from "node:url";

const { artifactKey } = await import(pathToFileURL(process.argv[2]).href);
console.log(artifactKey("v26.8.0", "linux", "amd64", "biz"));
NODE
)"
if [ "$mutant_key" != "enterprise/v26.8.0/all/olivares_v26.8.0_linux_amd64.tar.gz" ]; then
  bad "set-key mutant is not executable ($mutant_key)"
fi
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "mutant (artifactKey returns all while retaining set-key decoy) is killed"
else
  bad "executable set-key mutant survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = '  return `enterprise/${version}/${set}/${artifactFilename(version, os, arch)}`;'
new = '''  const destinationSet = set === "ent" ? "all" : set;
  return `enterprise/${version}/${destinationSet}/${artifactFilename(version, os, arch)}`;'''
if s.count(old) != 1:
    raise SystemExit("enterprise-only artifactKey mutant fixture did not match exactly once")
p.write_text(s.replace(old, new), encoding="utf-8")
PY
mutant_ent_key="$(node --input-type=module - \
  "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'NODE'
import { pathToFileURL } from "node:url";

const { artifactKey } = await import(pathToFileURL(process.argv[2]).href);
console.log(artifactKey("v26.8.0", "linux", "amd64", "ent"));
NODE
)"
if [ "$mutant_ent_key" != "enterprise/v26.8.0/all/olivares_v26.8.0_linux_amd64.tar.gz" ]; then
  bad "enterprise-only mutant is not executable ($mutant_ent_key)"
fi
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "mutant (artifactKey misroutes only ent) is killed"
else
  bad "enterprise-only artifactKey mutant survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/download/sets.ts" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = '  "ent",\n'
if s.count(old) != 1:
    raise SystemExit("enterprise allowlist member is not unique")
p.write_text(s.replace(old, "", 1), encoding="utf-8")
PY
if node --input-type=module - \
  "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" \
  "$TMP/tree/commercial/license-worker/src/download/sets.ts" <<'NODE'
import { pathToFileURL } from "node:url";

const artifacts = await import(pathToFileURL(process.argv[2]).href);
const sets = await import(pathToFileURL(process.argv[3]).href);
if (sets.setSlug(["ent"]) !== "ent") throw new Error("mutant lost the paid set itself");
try {
  artifacts.artifactKey("v26.8.0", "linux", "amd64", "ent");
  throw new Error("mutant still permits ent artifact keys");
} catch (error) {
  if (!String(error).includes("not allowlisted")) throw error;
}
NODE
then
  run
  if [ "$(cat "$TMP/rc")" = 1 ]; then
    ok "mutant (paid ent disappears only from the allowlist) is killed"
  else
    bad "removed ent allowlist member survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
  fi
else
  bad "removed-ent mutant is not executable"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/download/sets.ts" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = '''export function isAllowedSetSlug(s: string): boolean {
  return ALLOWED_SET_SLUGS.has(s);
}'''
new = '''export function isAllowedSetSlug(s: string): boolean {
  return s !== "not-a-set";
}'''
if s.count(old) != 1:
    raise SystemExit("isAllowedSetSlug mutant fixture did not match exactly once")
p.write_text(s.replace(old, new, 1), encoding="utf-8")
PY
mutant_attacker_key="$(node --input-type=module - \
  "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'NODE'
import { pathToFileURL } from "node:url";

const { artifactKey } = await import(pathToFileURL(process.argv[2]).href);
console.log(artifactKey("v26.8.0", "linux", "amd64", "attacker"));
NODE
)"
if [ "$mutant_attacker_key" != \
  "enterprise/v26.8.0/attacker/olivares_v26.8.0_linux_amd64.tar.gz" ]; then
  bad "overbroad allowlist mutant is not executable ($mutant_attacker_key)"
fi
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
  ok "mutant (all strings except one sentinel become allowed) is killed"
else
  bad "overbroad allowlist mutant survived rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-r2-set-key-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["remainder_applied"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (remainder-applied) is killed"
else bad "remainder-applied stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
printf '\nfunction legacyMonolithKey(version: string, os: string, arch: string): string { return ""; }\n' >> \
  "$TMP/tree/commercial/license-worker/src/download/artifacts.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (legacyMonolithKey landed) is killed"
else bad "legacyMonolithKey stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
sed -i 's/artifactKey(version, os, arch, purchased)/artifactKey(version, os, arch)/' \
  "$TMP/tree/commercial/license-worker/src/download/gate.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (gate dropped purchased set) is killed"
else bad "purchased set stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
sed -i 's/downloadAuditLabel(version, purchased, os, arch)/`${version} ${os}\/${arch}`/' \
  "$TMP/tree/commercial/license-worker/src/download/gate.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (audit label helper dropped) is killed"
else bad "audit helper stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/design/c02-r2-set-key-prep-2026-08-20.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["overlay_remeasured_in_this_gate"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (overlay remasure leaked) is killed"
else bad "overlay remasure stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/design/c02-r2-set-key-prep-2026-08-20.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing JSON is COULD NOT LOOK"
else bad "missing JSON rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/commercial/license-worker/src/download/sets.ts"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing artifactKey dependency is COULD NOT LOOK"
else bad "missing sets.ts rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'PY'
from pathlib import Path
import sys

p = Path(sys.argv[1])
s = p.read_text(encoding="utf-8")
old = 'export function artifactKey(version: string, os: string, arch: string, set: string): string {'
new = '''export function artifactKey(
  version: string,
  os: string,
  arch: string,
  set: string,
): string {'''
if s.count(old) != 1:
    raise SystemExit("artifactKey no-fire fixture did not match exactly once")
p.write_text(s.replace(old, new), encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: multiline artifactKey stays CLEAN"
else bad "multiline artifactKey should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-c02-r2-set-key-prep selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
