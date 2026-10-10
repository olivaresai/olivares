#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# CENSUS-SUBJECT: external
# The subject is host processes, not repository files; an empty tree can pass correctly.
#
# Do not wire this into pre-push (the planner, 2026-08-25T23:50Z): every hook invocation
# would detect its own active push and reject legitimate work. Invoke manually before
# editing, or as layer 0 of a push harness.
# Invoke with bash; never gate with [ -x ]. Measured 2026-08-26: noexec /tmp defeats
# access(X_OK) despite executable mode, so [ -x ] silently disabled this guard in a
# nohup harness. Use [ -r "$g" ] && bash "$g" … and fail closed on unreadability.
#
# check-worktree-push-free.sh <worktree>: 0 free to edit/commit · 1 active push,
# read-only until its ls-remote declaration · 2 could not check.
# The rule was broken three times after being written and shared: check-tree-untouched
# blamed the edit on a gate and rejected the push; git could transfer the ref resolved
# at transfer time rather than the tested commit, carrying a later commit without lints.
# Identify the push through the olivares-prepush.* hook's cwd, not git push.
# A hook with PPID 1 is orphaned and does not block editing, though it burns a core.
set -uo pipefail
W="${1:-}"
[ -n "$W" ] || { echo "check-worktree-push-free: COULD NOT CHECK: missing <worktree>" >&2; exit 2; }
[ -d "$W" ] || { echo "check-worktree-push-free: COULD NOT CHECK: '$W' is not a directory" >&2; exit 2; }
real="$(cd -- "$W" && pwd -P)" || { echo "check-worktree-push-free: COULD NOT CHECK: cannot resolve '$W'" >&2; exit 2; }

vivos=0; huerfanos=0
for h in $(ps -eo pid,args --no-headers 2>/dev/null | grep '[o]livares-prepush' | awk '{print $1}'); do
	d="$(readlink "/proc/$h/cwd" 2>/dev/null)" || continue
	[ "$d" = "$real" ] || continue
	pp="$(ps -o ppid= -p "$h" 2>/dev/null | tr -d ' ')"
	if [ "$pp" = "1" ]; then
		huerfanos=$((huerfanos + 1))
		echo "check-worktree-push-free: ⚠ orphaned hook $h (ppid=1); its push ended. It does not block edits but consumes a CPU core; stop it." >&2
	else
		vivos=$((vivos + 1))
		echo "check-worktree-push-free: ⛔ ACTIVE push in $real — hook $h, parent $pp." >&2
	fi
done

if [ "$vivos" -gt 0 ]; then
	echo "check-worktree-push-free: DO NOT EDIT. $vivos push(es) in progress on this worktree." >&2
	echo "               A new commit could be pushed without checks; edits could make check-tree-untouched blame a check." >&2
	exit 1
fi
echo "check-worktree-push-free: available ($real)${huerfanos:+ · $huerfanos orphaned hook(s) that should be stopped)}"
exit 0
