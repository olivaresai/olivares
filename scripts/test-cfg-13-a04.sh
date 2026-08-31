#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-cfg-13-a04.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/cfg13.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/.gitleaks.toml" "$TMP/tree/"
  cp "$ROOT/design/CFG-13-A04-PATH-EXEMPT-GONE-2026-08-20.md" "$TMP/tree/design/"
  cp "$ROOT/scripts/test-check-secrets.sh" "$TMP/tree/scripts/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-cfg-13-a04.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-cfg-13-a04.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "A-04 path exemption gone is CLEAN"
else bad "live pin should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/.gitleaks.toml" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text(encoding="utf-8")
needle = "paths = ["
i = t.find(needle)
if i < 0:
    raise SystemExit("fixture lost paths = [")
# Insert the historical whole-detector exemption as the first path.
insert = "paths = [\n  '''.*_test.go$''',"
t = t[:i] + insert + t[i + len(needle):]
p.write_text(t, encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (restore _test.go path exemption) is killed"
else bad "restored _test.go exemption stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/.gitleaks.toml" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
t = p.read_text(encoding="utf-8")
needle = "paths = ["
i = t.find(needle)
if i < 0:
    raise SystemExit("fixture lost paths = [")
insert = "paths = [\n  '''.*/testdata/.*''',"
t = t[:i] + insert + t[i + len(needle):]
p.write_text(t, encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (restore testdata path exemption) is killed"
else bad "restored testdata exemption stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
sed -i 's/a04-path-exemption: gone/a04-path-exemption: live/' \
  "$TMP/tree/design/CFG-13-A04-PATH-EXEMPT-GONE-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (doc claims exemption live) is killed"
else bad "doc live stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
rm -f "$TMP/tree/.gitleaks.toml"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then ok "missing gitleaks.toml is COULD NOT LOOK"
else bad "missing toml rc=$(cat "$TMP/rc") want 2 ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live pin stays CLEAN"
else bad "no-fire should stay CLEAN ($(cat "$TMP/err"))"; fi

echo "check-cfg-13-a04 selftest: $pass passed, $fail failed"
if [ "$fail" -ne 0 ]; then exit 1; fi
