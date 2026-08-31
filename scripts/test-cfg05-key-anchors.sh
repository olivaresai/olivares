#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Self-test for check-cfg05-key-anchors.sh. Each case names the guard
# it would kill if deleted.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-cfg05-key-anchors.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/cfg05.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/scripts" \
           "$TMP/tree/core/license" \
           "$TMP/tree/core/release" \
           "$TMP/tree/design" \
           "$TMP/tree/.github/workflows"
  cp "$ROOT/.goreleaser.yaml" "$TMP/tree/"
  cp "$ROOT/Taskfile.yml" "$TMP/tree/"
  cp "$ROOT/scripts/check-release-pubkey.sh" "$TMP/tree/scripts/"
  cp "$ROOT/scripts/test-key-domain-separation.sh" "$TMP/tree/scripts/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-cfg05-key-anchors.sh"
  cp "$ROOT/core/license/embedded.go" "$TMP/tree/core/license/"
  cp "$ROOT/core/release/verify.go" "$TMP/tree/core/release/"
  cp "$ROOT/design/CFG-05-KEY-ANCHORS-2026-08-19.md" "$TMP/tree/design/"
  cp -a "$ROOT/.github/workflows/." "$TMP/tree/.github/workflows/"
}

run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-cfg05-key-anchors.sh" \
    >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then
  ok "the live wiring is CLEAN"
else
  bad "the live wiring should be CLEAN ($(cat "$TMP/err"))"
fi

stage
sed -i 's/OLIVARES_LICENSE_PUBKEY/OLIVARES_OTA_PUBKEY/g' "$TMP/tree/.goreleaser.yaml"
if run; then
  bad "LICENSE symbol fed the OTA env stayed CLEAN"
else
  ok "mutant (OTA pubkey in LICENSE symbol) is killed"
fi

stage
sed -i 's/OLIVARES_OTA_PUBKEY/OLIVARES_LICENSE_PUBKEY/g' "$TMP/tree/.goreleaser.yaml"
if run; then
  bad "OTA symbol fed the LICENSE env stayed CLEAN"
else
  ok "mutant (LICENSE pubkey in OTA symbol) is killed"
fi

stage
sed -i '/OLIVARES_LICENSE_PUBKEY and OLIVARES_OTA_PUBKEY are identical/d' \
  "$TMP/tree/scripts/check-release-pubkey.sh"
if run; then
  bad "identical-anchor refusal dropped stayed CLEAN"
else
  ok "mutant (identical anchors accepted) is killed"
fi

stage
sed -i 's/LICENSE verification key/historical verification key/' \
  "$TMP/tree/core/license/embedded.go"
if run; then
  bad "LICENSE comment dropped stayed CLEAN"
else
  ok "mutant (symbol no longer named LICENSE) is killed"
fi

stage
# SUPERSEDED by orden 37: signing in the job is the intended behaviour now, so
# the old mutant («CI signs at all») would test the opposite of the rule. Its
# inverse must stay red: signing from a workflow that is NOT release.yml, which
# no environment protects.
printf '\n      - run: olivares release sign-manifest --sign-key /tmp/ota.key\n' \
  >> "$TMP/tree/.github/workflows/mainline-ci.yml"
if run; then
  bad "sign-manifest in a workflow other than release.yml stayed CLEAN"
else
  ok "mutant (a second workflow signs the manifest) is killed"
fi

stage
# The other half of the mitigation: release.yml may sign, but only with an
# environment declared. Strip every environment: and it must go red.
sed -i '/^[[:space:]]*environment:/d' "$TMP/tree/.github/workflows/release.yml"
if run; then
  bad "release.yml signing with no environment declared stayed CLEAN"
else
  ok "mutant (signing without a protected environment) is killed"
fi

stage
# The LICENSE private half keeps its absolute ban — orden 37 was about releases.
printf '\n      OLIVARES_LICENSE_PRIVATE_KEY: ${{ secrets.LIC }}\n' \
  >> "$TMP/tree/.github/workflows/release.yml"
if run; then
  bad "LICENSE private key in a workflow stayed CLEAN"
else
  ok "mutant (licence private key named in CI) is killed"
fi

stage
# The OTA private half from a repository VARIABLE is exactly what the
# environment secret replaces: readable by any run, editable without review.
printf '\n      OTA_PRIVATE_KEY: ${{ vars.OLIVARES_OTA_PRIVATE_KEY }}\n' \
  >> "$TMP/tree/.github/workflows/release.yml"
if run; then
  bad "OTA private key from a repository variable stayed CLEAN"
else
  ok "mutant (OTA private key from a repo variable) is killed"
fi

stage
# A literal key pasted into the workflow.
printf '\n      OTA_PRIVATE_KEY: MC4CAQAwBQYDK2VwBCIEIexampleexample\n' \
  >> "$TMP/tree/.github/workflows/release.yml"
if run; then
  bad "a literal OTA private key stayed CLEAN"
else
  ok "mutant (literal OTA private key in the workflow) is killed"
fi

stage
# NO-FIRE, and the one that matters most: the real mitigation must stay GREEN.
# Without it every case above passes on a gate that simply refuses everything.
if run; then
  ok "no-fire: the real wiring (environment secret + in-job signing) stays CLEAN"
else
  bad "the real orden-37 wiring is refused — the gate rejects what it must allow"
fi

stage
sed -i 's/NO ceremony/ceremony DONE/' \
  "$TMP/tree/design/CFG-05-KEY-ANCHORS-2026-08-19.md"
if run; then
  bad "doc claiming the ceremony ran stayed CLEAN"
else
  ok "mutant (doc claims ceremony ran) is killed"
fi

stage
# No-fire: a refuse-everything checker would fail the live tree too.
if ! run; then
  bad "no-fire: live wiring should stay CLEAN ($(cat "$TMP/err"))"
else
  ok "no-fire: live two-anchor wiring stays CLEAN"
fi

stage
rm -f "$TMP/tree/.goreleaser.yaml"
if run; then
  bad "missing goreleaser stayed CLEAN"
else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then
    ok "missing goreleaser is COULD NOT LOOK"
  else
    bad "missing goreleaser should be exit 2 ($(cat "$TMP/err"))"
  fi
fi

printf 'check-cfg05-key-anchors selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
