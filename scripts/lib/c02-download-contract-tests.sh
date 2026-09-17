# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

# Shared runtime regressions. Caller supplies isolated stage/run/ok/bad functions and TMP;
# stage includes that checker's own JSON/doc/publisher inputs. No checker calls another.
c02_download_contract_mutants() {
  local mode reason
  # Behavioural mutants retain plausible source decoys. A syntax/import error does not
  # count as a kill: each case must reach the named assertion in the executable contract.
  for mode in constant-set cached-grants set-query variant-query no-license no-grants wrong-holder; do
    stage
    python3 - "$TMP/tree/commercial/license-worker/src/download" "$mode" <<'PYMUTANT'
from pathlib import Path
import sys
root = Path(sys.argv[1])
mode = sys.argv[2]
gate = root / "gate.ts"
entitlement = root / "entitlement.ts"
def replace(path, old, new):
    text = path.read_text()
    if text.count(old) != 1:
        raise SystemExit(f"{mode}: mutation anchor must match exactly once")
    path.write_text(text.replace(old, new))
if mode == "constant-set":
    replace(gate, '  const purchased = live.set;',
        '  const purchased = "biz"; // decoy: const purchased = live.set;')
elif mode == "cached-grants":
    replace(entitlement, 'export async function liveEntitlement(',
        'let cachedCodes: string[] | undefined;\nexport async function liveEntitlement(')
    replace(entitlement, 'const codes = await store.listActiveGrantCodes(holderId, nowISO);',
        'const codes = cachedCodes ??= await store.listActiveGrantCodes(holderId, nowISO);')
elif mode in ("set-query", "variant-query"):
    field = mode.split("-")[0]
    replace(gate, f'if (url.searchParams.has("{field}")) {{',
        f'if (false && url.searchParams.has("{field}")) {{')
    replace(gate, '  const purchased = live.set;',
        f'  const purchased = url.searchParams.get("{field}") || live.set;')
elif mode == "no-license":
    replace(entitlement, '  if (!license) {', '  if (false && !license) {')
elif mode == "no-grants":
    replace(entitlement, 'const codes = await store.listActiveGrantCodes(holderId, nowISO);',
        'const observedCodes = await store.listActiveGrantCodes(holderId, nowISO);\n'
        '  const codes = observedCodes.length ? observedCodes : ["biz"];')
elif mode == "wrong-holder":
    replace(gate, 'liveEntitlement(store, verified.holderId as string, nowISO);',
        'liveEntitlement(store, "another-holder", nowISO);')
PYMUTANT
    case "$mode" in
      constant-set | cached-grants) reason='live biz+airs: exact grant-derived key' ;;
      set-query) reason='set query ""' ;;
      variant-query) reason='variant query ""' ;;
      no-license) reason='license removed while token remains valid' ;;
      no-grants) reason='no valid live grants []' ;;
      wrong-holder) reason='license must read the authenticated holder' ;;
    esac
    run
    if [ "$(cat "$TMP/rc")" = 1 ] && grep -Fq "$reason" "$TMP/err"; then
      ok "mutant ($mode) reaches and fails its behavioural assertion"
    else
      bad "$mode not killed by its behaviour rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"
    fi
  done

  stage
  sed -i 's/artifactKey(version, os, arch, purchased)/artifactKey(version, os, arch)/' \
    "$TMP/tree/commercial/license-worker/src/download/gate.ts"
  run
  if [ "$(cat "$TMP/rc")" = 1 ] && grep -Fq 'set undefined is not allowlisted' "$TMP/err"; then ok "mutant (gate dropped purchased set) is killed"
  else bad "purchased set stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

  stage
  sed -i 's/downloadAuditLabel(version, purchased, os, arch)/`${version} ${os}\/${arch}`/' \
    "$TMP/tree/commercial/license-worker/src/download/gate.ts"
  run
  if [ "$(cat "$TMP/rc")" = 1 ] && grep -Fq 'audit must name the actual holder, set and token' "$TMP/err"; then ok "mutant (audit label helper dropped) is killed"
  else bad "audit helper stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

  for dependency in entitlement.ts tokens.ts; do
    stage
    rm -f "$TMP/tree/commercial/license-worker/src/download/$dependency"
    run
    if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing runtime $dependency is COULD NOT LOOK"
    else bad "missing $dependency rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi
  done

  stage
  python3 - "$TMP/tree/commercial/license-worker/src/download/gate.ts" <<'PYREFORMAT'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
old = '  const purchased = live.set;'
assert s.count(old) == 1
p.write_text(s.replace(old, '  const purchased = (await Promise.resolve(live)).set;'))
PYREFORMAT
  run
  if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: equivalent live entitlement expression stays CLEAN"
  else bad "equivalent live entitlement should stay CLEAN ($(cat "$TMP/err"))"; fi
}

# Exercise every caller's existing input override, including independently located
# ART/GATE files whose relative module graphs remain intact. No checker-specific JSON
# or historical HOLD is imported by the other checker.
c02_download_contract_overrides() {
  local assignment input_key relative destination rc
  local -a overrides=()
  stage
  for assignment in "$@"; do
    input_key="${assignment%%=*}"
    relative="${assignment#*=}"
    destination="$TMP/tree/$(dirname "$relative")/override-$(basename "$relative")"
    cp "$TMP/tree/$relative" "$destination"
    overrides+=("$input_key=$destination")
  done
  rc=0
  env "${overrides[@]}" OLIVARES_ROOT="$TMP/tree" \
    bash "$TMP/tree/scripts/$(basename "$CHECK")" >"$TMP/out" 2>"$TMP/err" || rc=$?
  if [ "$rc" = 0 ]; then ok "no-fire: all input overrides preserve the real module graph"
  else bad "complete overrides should stay CLEAN rc=$rc ($(cat "$TMP/err"))"; fi
  for assignment in "$@"; do
    input_key="${assignment%%=*}"
    stage
    rc=0
    env "$input_key=$TMP/tree/missing-input" OLIVARES_ROOT="$TMP/tree" \
      bash "$TMP/tree/scripts/$(basename "$CHECK")" >"$TMP/out" 2>"$TMP/err" || rc=$?
    if [ "$rc" = 2 ]; then ok "missing overridden $input_key is COULD NOT LOOK"
    else bad "missing overridden $input_key rc=$rc want 2 ($(cat "$TMP/err"))"; fi
  done
}
