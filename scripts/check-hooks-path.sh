#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-hooks-path.sh — the gate that runs every other gate has to be reachable, and
# nothing was watching whether it still was.
#
# WHY THIS EXISTS (2026-08-07). Another lane reported that `core.hooksPath` held an
# ABSOLUTE path to the hub's working tree, and drew the consequence: a branch would not
# run ITS hooks, it would run whatever `main` had checked out in the hub, while the TASKS
# that hook invokes resolve against the branch's own older Taskfile — so every in-flight
# branch's push breaks the moment a new task enters the hook on main, with a diff that has
# nothing to do with it.
#
# THE MECHANISM DID NOT REPRODUCE, measured here with a probe rather than argued: a
# throwaway linked worktree carrying a marked `.githooks/pre-push`, pushed to a local bare
# remote, ran ITS OWN hook — from the worktree root and from a subdirectory alike, because
# git chdirs to the top of the working tree before invoking a hook, so a RELATIVE hooksPath
# is resolved per-worktree and is not lost when you push from `scripts/`. What the value
# was at the time of P's measurement CANNOT be recovered: `.git/config` is not versioned.
#
# WHAT IS REAL, AND IS WHY THIS FILE EXISTS ANYWAY. `core.hooksPath` lives in
# `.git/config`, which every linked worktree SHARES — 39 of them in the hub as this is
# written. `task setup` writes it relative and asserts it, but that assertion runs ONCE, at
# install time. A single `git config core.hooksPath /abs/path`, by a person or a tool,
# converts the whole clone to P's failure mode for every worktree at once, and until now
# NOTHING would have noticed. That the value is correct today is a photograph, not a
# guarantee — and the thing it guards is the gate that guards everything else.
#
# It is placed so it fires in both worlds. Relative: each worktree runs its own hook, which
# carries this check. Absolute: the branch runs the hub's hook, which also carries it.
# There is no arrangement of the value that hides the check from the push it governs.
#
# THE FOUR WAYS THE HOOKS GO SILENT, all of which look identical from outside — a push that
# sails through — and all of which this refuses:
#
#   unset          git falls back to $GIT_COMMON_DIR/hooks, which is SHARED by every
#                  worktree and is NOT this repository's .githooks. The gate never runs.
#   absolute       every worktree runs one fixed tree's hooks; see above.
#   missing        the path resolves to nothing. git runs no hook and says nothing.
#   not executable git SKIPS a hook it cannot execute, SILENTLY. No error, no exit code —
#                  the push simply succeeds ungated. This is the quietest of the four.
#
# ⚠ THE LIMIT OF THIS GATE, STATED BECAUSE AN OVERCLAIMED GATE IS WORSE THAN A MODEST ONE.
# Run FROM the pre-push hook it protects, it can only fire in the states where that hook
# still runs at all. Of the four failures above, exactly one has that property — and it is,
# by luck rather than design, the one that was actually reported:
#
#   absolute        the hub's hook DOES run for every worktree, so this check runs with it
#                   and fires. COVERED where it matters most, because this is the failure
#                   that keeps the machinery working while silently changing whose rules apply.
#   unset           git runs no hook. Nothing local can observe this from inside a hook,
#                   BY CONSTRUCTION.
#   missing         same.
#   not executable  same.
#
# The three uncoverable-from-inside states are caught the moment the gate is invoked
# DIRECTLY — `task lint:hooks-path`, the full local gate, or a CI job checking a runner's own
# checkout — which is why this ships as a task and not only as a hook line. Anyone reasoning
# about coverage here should assume the three are unguarded on a developer's box between
# `task setup` and the next explicit run. Closing them for real needs a signal that does not
# depend on the hook firing (the shape of what LANDS, checked server-side); that is not built.
#
# Exit 0 = CLEAN:       hooks are configured so that each worktree runs its own, and they run.
# Exit 1 = BROKEN:      named above, with the consequence spelled out and the one-line repair.
# Exit 2 = COULD NOT LOOK: no git, or not inside a working tree. NOT a clean verdict.
set -u
set -o pipefail

say() { printf '%s\n' "$*"; }
die_cannot_look() {
	say "check-hooks-path: COULD NOT LOOK — $1" >&2
	say "check-hooks-path: this is not a clean verdict. Fix the tooling and run again." >&2
	exit 2
}
broken() {
	say "check-hooks-path: BROKEN — $1" >&2
	say "" >&2
	say "  consequence: $2" >&2
	say "  repair     : $3" >&2
	exit 1
}

command -v git >/dev/null 2>&1 || die_cannot_look "no git on PATH"

TOP="$(git rev-parse --show-toplevel 2>/dev/null)" || TOP=""
[ -n "$TOP" ] || die_cannot_look "not inside a git working tree (a bare repo has no hooks to gate)"

# The ORIGIN matters as much as the value. check-secrets.sh learned this the expensive way:
# a gate that will not say WHOSE configuration it applied leaves a red nobody can act on.
origin_line="$(git config --show-origin --get core.hooksPath 2>/dev/null || true)"
value="$(git config --get core.hooksPath 2>/dev/null || true)"
origin="${origin_line%%$'\t'*}"
[ "$origin" = "$origin_line" ] && origin="(unknown)"

if [ -z "$value" ]; then
	broken "core.hooksPath is NOT SET" \
		"git falls back to \$GIT_COMMON_DIR/hooks, which every linked worktree of this clone shares and which is NOT this repository's .githooks/. Every push in every lane goes completely ungated, and nothing says so." \
		"task setup    (or: git config core.hooksPath .githooks)"
fi

case "$value" in
/*)
	broken "core.hooksPath is ABSOLUTE: $value  [from $origin]" \
		"It lives in .git/config, which EVERY linked worktree shares, so all of them run that one tree's hooks instead of their own. A branch then runs a hook newer than its own checkout while the TASKS that hook invokes resolve against the branch's older Taskfile: its push fails asking for a task its tree does not define, with a diff that has nothing to do with it." \
		"git config core.hooksPath .githooks    (relative: git resolves it from each worktree's top level)"
	;;
esac

# Resolve exactly the way git does: from the top of the working tree, not from the caller's
# cwd. Measured 2026-08-07 — git chdirs to the top level before invoking a hook, which is
# why a push from a subdirectory still finds a relative hooksPath.
resolved="$TOP/$value"

if [ ! -d "$resolved" ]; then
	broken "core.hooksPath points at something that is not a directory: $value -> $resolved  [from $origin]" \
		"git runs no hook at all and reports nothing. Every push in this worktree is ungated and looks exactly like a push that passed." \
		"restore the directory, or: git config core.hooksPath .githooks"
fi

# pre-push is the one this whole gate system hangs from; commit-msg is what keeps the
# Conventional-Commits/DCO contract. Both are checked because a missing OR unexecutable
# hook is silent in exactly the same way.
rc=0
for hook in pre-push commit-msg; do
	h="$resolved/$hook"
	if [ ! -f "$h" ]; then
		say "check-hooks-path: BROKEN — $value/$hook is MISSING  [from $origin]" >&2
		say "" >&2
		say "  consequence: git finds no $hook and runs none. The check it performs is simply absent," >&2
		say "               and a push or commit that should have been refused is accepted in silence." >&2
		say "  repair     : restore $value/$hook from origin/main." >&2
		rc=1
		continue
	fi
	if [ ! -x "$h" ]; then
		say "check-hooks-path: BROKEN — $value/$hook is NOT EXECUTABLE  [from $origin]" >&2
		say "" >&2
		say "  consequence: git SKIPS a hook it cannot execute, silently — no error, no exit code." >&2
		say "               This is the quietest failure of the four: the file is present, the config" >&2
		say "               is right, the push sails through, and the gate never ran." >&2
		say "  repair     : chmod +x $value/$hook" >&2
		rc=1
	fi
done
[ "$rc" -eq 0 ] || exit 1

say "check-hooks-path: CLEAN — core.hooksPath='$value' (relative, from $origin) -> $resolved; pre-push and commit-msg present and executable."
say "check-hooks-path: relative is the property that matters: each of this clone's worktrees runs ITS OWN hooks."
exit 0
