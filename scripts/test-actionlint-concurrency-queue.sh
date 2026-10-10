#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-actionlint-concurrency-queue.sh — compatibility battery for lint:actions.
#
# GitHub documented `queue:` under `concurrency:` on 2026-05-07 (concurrency queues). Every
# actionlint release this repo can pin — through v1.7.12, 2026-03-30 — predates the field and
# rejects it as an unknown key (rhysd/actionlint#657; fix #654 unreleased), so `task lint:actions`
# was red on main over workflows GitHub accepts (issue #792; reproduced on main b0e8a9e0 and
# 67ce5c22 with the pinned v1.7.7 and the pinned shellcheck on PATH). The guard now hands
# actionlint one -ignore scoped to that exact message, and this battery holds its scope:
#
#   1. DRIFT      the pin and the -ignore pattern are READ from Taskfile.yml, so this battery
#                 always tests the guard's own configuration, never a copy of it. The pattern
#                 is also frozen textually: editing it, deleting it or broadening it past the
#                 canonical literal turns this battery red until it is re-aimed on purpose.
#   2. RED        the pinned tool WITHOUT the ignore rejects `queue: max` — the defect this
#                 configuration exists for, kept as an executable record so it cannot be
#                 forgotten. When the pin moves past a release containing rhysd/actionlint#654,
#                 this leg flips red→green on its own: that is the signal to drop the -ignore
#                 and this battery together.
#   3. GREEN      with the guard's own pattern, the same workflow is clean.
#   4. HOLE SHUT  an INVALID concurrency key — `quee: max`, the one-typo neighbour of the
#                 suppressed key, the exact variant a loose pattern could mute — stays red.
#   5. SCOPED     `queue:` in the WRONG place (directly under a job, not under concurrency)
#                 stays red: the pattern matches the concurrency section's message only.
#
# Known ceiling, recorded in the Taskfile comment too: until upstream validates the field, the
# VALUE of `queue` is accepted as-is. That validation is rhysd/actionlint#654's own contribution
# and arrives with the pin bump this battery's red leg waits for.
#
# Dispatch inputs must follow GitHub's supported limit too: 10, 11 and 25 are accepted,
# while 26 must retain the input-limit diagnostic. Use the guard's pinned ShellCheck
# for these fixtures as well as the real workflows.
#
# exit 0  every leg holds · exit 1  a leg failed · exit 2  could not look.
set -euo pipefail
LC_ALL=C; export LC_ALL
me=test-actionlint-concurrency-queue
root_dir="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
taskfile="$root_dir/Taskfile.yml"
[ -f "$taskfile" ] || { printf '%s: could not look — %s missing\n' "$me" "$taskfile" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { printf '%s: could not look — no go toolchain to build the pinned actionlint\n' "$me" >&2; exit 2; }
SC="$(bash "$root_dir/scripts/ensure-shellcheck.sh")" || exit 2
PATH="$(dirname "$SC"):$PATH"; export PATH
pass=0; fail=0
ok() { pass=$((pass + 1)); printf 'ok - %s\n' "$*"; }
bad() { fail=$((fail + 1)); printf 'not ok - %s\n' "$*"; }
bench="$(mktemp -d "${TMPDIR:-/tmp}/actionlint-queue-test.XXXXXX")" || { printf '%s: could not look — no temp dir\n' "$me" >&2; exit 2; }
trap 'rm -rf "$bench"' EXIT

# --- 1. DRIFT: the guard's own pin and pattern, read from the one source of truth -------------------
# Both reads are scoped to the lint:actions task block (review finding): Taskfile.yml is
# ~2900 lines, and a future unrelated `-ignore "..."` earlier in the file must not re-aim
# this battery at the wrong configuration while the guard's own pattern goes untested.
# `|| true` twice below: under set -e + pipefail a finding-less grep is a report ("absent"),
# not a crash. The pin charset admits pre-release pins (v1.8.0-rc1), so a bump to a release
# candidate still points this battery at the version the guard actually runs.
block="$(sed -n '/^  lint:actions:/,/^  [a-zA-Z][a-zA-Z0-9:.-]*:$/p' "$taskfile" | sed '$d')"
pin="$(printf '%s\n' "$block" | grep -o 'actionlint/cmd/actionlint@v[0-9][0-9A-Za-z.+-]*' | head -1 | sed 's/.*@//' || true)"
if [ -n "$pin" ]; then ok "drift: pinned actionlint $pin read from Taskfile.yml's lint:actions"; else bad "drift: lint:actions carries no actionlint pin"; fi
pat_line="$(printf '%s\n' "$block" | grep -o -- '-ignore "[^"]*"' | head -1 || true)"
pat="${pat_line#-ignore \"}"; pat="${pat%\"}"
if [ -n "$pat" ]; then ok "drift: -ignore pattern read from Taskfile.yml's lint:actions ($pat)"; else bad "drift: lint:actions carries no -ignore for the queue false positive — either it was removed before its upstream fix landed, or it moved somewhere this battery does not read"; fi
# The pattern is FROZEN textually as well as behaviourally: the legs below prove what it does
# to three probed messages, not that it suppresses only those — a broadened pattern (an
# alternation, a dropped anchor) that happens to leave the three probes untouched would
# otherwise ride a green run. Any edit to the pattern must re-aim this literal on purpose.
if [ "$pat" = 'unexpected key .queue. for .concurrency. section' ]; then
	ok "frozen: the -ignore pattern is the canonical literal"
else
	bad "frozen: the -ignore pattern is not the canonical literal 'unexpected key .queue. for .concurrency. section' — re-aim this battery deliberately, not in passing"
fi
[ "$fail" = 0 ] || { printf '%s: %d ok, %d failed\n' "$me" "$pass" "$fail"; exit 1; }

# Could-not-look gate: build and run the pinned tool ONCE before any verdict, so a toolchain
# broken from the start is a "could not look" (rc 2). A tool that breaks MID-run below still
# lands in a red leg — loudly, with go's error text in the message — never a silent pass.
warm="$(go run "github.com/rhysd/actionlint/cmd/actionlint@$pin" -version 2>&1)" || {
	printf '%s: could not look — pinned actionlint %s did not build or run: %s\n' "$me" "$pin" "$warm" >&2
	exit 2
}

# --- fixtures ---------------------------------------------------------------------------------------
valid_wf="$bench/queue-valid.yml"
cat >"$valid_wf" <<'YAML'
name: queue-fixture
on: push
concurrency:
  group: fixture-group
  cancel-in-progress: false
  queue: max
jobs:
  probe:
    runs-on: ubuntu-latest
    steps:
      - run: echo fixture
YAML
typo_wf="$bench/queue-typo.yml"
sed 's/^  queue: max$/  quee: max/' "$valid_wf" >"$typo_wf"
wrong_wf="$bench/queue-wrong-section.yml"
cat >"$wrong_wf" <<'YAML'
name: queue-wrong-section-fixture
on: push
jobs:
  probe:
    queue: max
    runs-on: ubuntu-latest
    steps:
      - run: echo fixture
YAML

alint() { # alint [flags...] file... → actionlint's rc in $?, its output in $out
	rc=0
	out="$(go run "github.com/rhysd/actionlint/cmd/actionlint@$pin" "$@" 2>&1)" || rc=$?
}

# --- 2. RED: the reproducer this whole configuration exists for -------------------------------------
alint "$valid_wf"
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -qF 'unexpected key "queue" for "concurrency" section'; then
	ok "red reproducer: pinned $pin without -ignore rejects queue: max"
else
	bad "red reproducer: rc=$rc out=$out — if the pinned tool now ACCEPTS the field, its pin passed a release with rhysd/actionlint#654: remove the Taskfile -ignore and this battery"
fi

# --- 3. GREEN: the guard's own pattern admits the documented field -----------------------------------
alint -ignore "$pat" "$valid_wf"
if [ "$rc" -eq 0 ]; then
	ok "green: with the guard's -ignore the queue: max workflow is clean"
else
	bad "green: rc=$rc out=$out"
fi

# --- 4. HOLE SHUT: a typo'd concurrency key is still refused ----------------------------------------
alint -ignore "$pat" "$typo_wf"
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -qF 'unexpected key "quee"'; then
	ok "hole shut: the one-typo neighbour quee: max stays red with the ignore in place"
else
	bad "hole shut: rc=$rc out=$out"
fi

# --- 5. SCOPED: queue in the wrong section is still refused -----------------------------------------
alint -ignore "$pat" "$wrong_wf"
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -qF 'unexpected key "queue"'; then
	ok "scoped: queue: under a job, outside concurrency, stays red"
else
	bad "scoped: rc=$rc out=$out"
fi

# --- 6. DISPATCH: the platform's allowed counts and first rejected count --------------------------
# https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#providing-inputs
for count in 10 11 25 26; do
	dispatch_wf="$bench/dispatch-$count.yml"
	{
		printf 'name: dispatch-fixture\non:\n  workflow_dispatch:\n    inputs:\n'
		for ((i=1; i<=count; i++)); do
			printf '      input_%d: {description: "Fixture input", type: string}\n' "$i"
		done
		printf 'jobs:\n  probe:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo fixture\n'
	} >"$dispatch_wf"
	alint -ignore "$pat" "$dispatch_wf"
	if [ "$count" -le 25 ]; then
		if [ "$rc" -eq 0 ]; then
			ok "dispatch: $count inputs accepted with pinned $pin and ShellCheck"
		else
			bad "dispatch: $count supported inputs rejected: rc=$rc out=$out"
		fi
	elif [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -qF 'maximum number of inputs for "workflow_dispatch" event is 25 but 26 inputs are provided'; then
		ok "dispatch: 26 inputs rejected by the input-limit diagnostic"
	else
		bad "dispatch: 26 inputs must be rejected at the 25-input limit: rc=$rc out=$out"
	fi
done

printf '%s: %d ok, %d failed\n' "$me" "$pass" "$fail"
[ "$fail" = 0 ] || exit 1
