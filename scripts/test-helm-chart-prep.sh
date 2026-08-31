#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail

# Aislamiento de git: este guion empareja `mktemp -d` con `git`, y sin sanear el
# entorno un GIT_DIR envenenado lo apunta al repo real. Fallar cerrado: no poder
# sanear es «no he podido aislar», nunca «no hacia falta aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-helm-chart-prep.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/helm-prep.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/deploy/helm/olivares" \
           "$TMP/tree/.github/workflows" \
           "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/deploy/helm/olivares/Chart.yaml" "$TMP/tree/deploy/helm/olivares/"
  cp "$ROOT/.github/workflows/release-chart.yml" "$TMP/tree/.github/workflows/"
  cp "$ROOT/design/CFG-18-HELM-CHART-PREP-2026-08-19.md" "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-helm-chart-prep.sh"
  # Isolated git so live repo tags cannot leak into the fixture, and so a
  # planted chart-v* tag is a real mutant.
  git -C "$TMP/tree" init -q
  git -C "$TMP/tree" config user.email t@t
  git -C "$TMP/tree" config user.name t
  git -C "$TMP/tree" add -A
  git -C "$TMP/tree" -c core.hooksPath=/dev/null commit -q --allow-empty -m init
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" OLIVARES_HELM_PREP_LOCAL_TAGS=1 \
    bash "$TMP/tree/scripts/check-helm-chart-prep.sh" >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
  return "$rc"
}

stage
if run; then ok "live prepare tree is CLEAN"; else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

stage
python3 - "$TMP/tree/deploy/helm/olivares/Chart.yaml" <<'PY'
import re, sys
p=sys.argv[1]
t=open(p,encoding="utf-8").read()
t, changed = re.subn(r"(?m)^version:\s*[^\n]+$", "version: not-semver", t, count=1)
if changed != 1:
    raise SystemExit("Chart.yaml has no unique version field to mutate")
open(p,"w",encoding="utf-8").write(t)
PY
if run; then bad "broken SemVer stayed CLEAN"; else ok "mutant (non-SemVer version) is killed"; fi

stage
printf '\n  workflow_dispatch:\n' >> "$TMP/tree/.github/workflows/release-chart.yml"
if run; then bad "workflow_dispatch stayed CLEAN"; else ok "mutant (dispatch publish path) is killed"; fi

stage
sed -i 's/NO publicado/ya publicado/' "$TMP/tree/design/CFG-18-HELM-CHART-PREP-2026-08-19.md"
if run; then bad "doc claiming published stayed CLEAN"; else ok "mutant (doc drops NO publicado) is killed"; fi

stage
git -C "$TMP/tree" tag chart-v0.2.4
if run; then bad "planted chart-v* tag stayed CLEAN"; else ok "mutant (this lote cut the tag) is killed"; fi

stage
rm -f "$TMP/tree/deploy/helm/olivares/Chart.yaml"
if run; then bad "missing Chart.yaml stayed CLEAN"; else
  if grep -q 'COULD NOT LOOK' "$TMP/err"; then ok "missing Chart.yaml is COULD NOT LOOK"
  else bad "missing Chart.yaml should be exit 2 ($(cat "$TMP/err"))"; fi
fi

printf 'check-helm-chart-prep selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
