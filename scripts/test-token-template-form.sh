#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for check-token-template-form.sh, by MUTATION in both directions.
#
# The catching half uses the REAL shape that killed the v26.8.0 release on 2026-09-01. If case 1
# ever goes green, the gate has stopped seeing the defect it exists for.
#
# The NOT-catching half is what keeps it from being a nuisance someone disables. `index .Env` is
# legitimate outside token fields -- the config's own `skip_upload` uses it in a conditional -- so
# a gate that flagged the function instead of the FIELD would send people to un-fix correct code.
# Cases 3 and 4 are that half, and they are the reason this is a gate and not a grep.
#
# Each case builds a throwaway git repo, because the subject resolves its root through git.
set -uo pipefail

# The ambient git env wins over cd: every case runs `git init` inside a mktemp dir, and under a
# pre-push hook -- which is where this battery's gate runs it -- an inherited GIT_DIR would point
# these at the LIVE repository. Fail-closed: not being able to isolate is never "did not need to".
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "test-token-template-form: FATAL: cannot load $_olivares_git_env" >&2
	exit 2
}
unset _olivares_git_env

export TMPDIR="${TMPDIR:-/workspace/.kernel-tmpdir}"
SUBJ="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-token-template-form.sh"
pass=0; fail=0

caso() {
	nombre="$1"; esperado="$2"; contenido="$3"
	d="$(mktemp -d)" || { echo "cannot mktemp"; exit 2; }
	( cd "$d" && git init -q . && printf '%s\n' "$contenido" > .goreleaser.yaml && git add -A 2>/dev/null
	  bash "$SUBJ" >"$d/out.txt" 2>&1 ); rc=$?
	if [ "$rc" = "$esperado" ]; then
		pass=$((pass+1)); printf '  ok   %-52s rc=%s\n' "$nombre" "$rc"
	else
		fail=$((fail+1)); printf '  FAIL %-52s rc=%s (wanted %s)\n' "$nombre" "$rc" "$esperado"
		sed 's/^/         /' "$d/out.txt" | head -5
	fi
	rm -rf "$d"
}

# --- 1 - THE REAL SHAPE that killed the release. Must be caught. ------------------------
caso "the v26.8.0 shape: index .Env in a token" 1 'brews:
  - repository:
      token: '"'"'{{ index .Env "HOMEBREW_TAP_GITHUB_TOKEN" }}'"'"''

# --- 2 - the cured form. Must pass. -----------------------------------------------------
caso "the literal .Env form" 0 'brews:
  - repository:
      token: '"'"'{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}'"'"''

# --- 3 - NOT-CATCHING: index .Env OUTSIDE a token field. Must stay green. ---------------
# This is the half that makes it usable. The real config does exactly this in skip_upload.
caso "index .Env outside a token field stays green" 0 'brews:
  - repository:
      token: '"'"'{{ .Env.TAP }}'"'"'
    skip_upload: '"'"'{{ if eq (index .Env "PUBLISH") "true" }}false{{ else }}true{{ end }}'"'"''

# --- 4 - NOT-CATCHING: a token that is not templated at all. --------------------------
caso "a literal token is not this defect" 0 'brews:
  - repository:
      token: not-a-template'

# --- 5 - a second rejected spelling, to prove the rule is the FORM and not one string ---
caso "another non-literal spelling is caught too" 1 'brews:
  - repository:
      token: '"'"'{{ printf "%s" .Env.TAP }}'"'"''

# --- 6 - several fields, one broken: must be caught and NAMED --------------------------
caso "one broken among several is still caught" 1 'brews:
  - repository:
      token: '"'"'{{ .Env.A }}'"'"'
scoops:
  - repository:
      token: '"'"'{{ index .Env "B" }}'"'"''

# --- 7 - COULD NOT LOOK: a config with no token field at all. rc 2, never 0. -----------
# Silence is not cleanliness: with no field, the gate asserts nothing and must say so.
caso "no token field at all -> 2, not 0" 2 'builds:
  - id: x'

# --- 8 - COULD NOT LOOK: no config. ---------------------------------------------------
d="$(mktemp -d)"; ( cd "$d" && git init -q . && bash "$SUBJ" >/dev/null 2>&1 ); rc=$?
if [ "$rc" = 2 ]; then pass=$((pass+1)); printf '  ok   %-52s rc=2\n' "no config at all -> 2"
else fail=$((fail+1)); printf '  FAIL %-52s rc=%s (wanted 2)\n' "no config at all -> 2" "$rc"; fi
rm -rf "$d"

echo "test-token-template-form: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
