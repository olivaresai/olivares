# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# git-env.sh — SOURCE THIS before any script builds a throwaway git repository.
# Not executable, not a program: it exists to be `.`-sourced, and it unsets the
# ambient git environment in the CALLER's shell.
#
# THE DEFECT IT CLOSES, measured 2026-08-06 (reported by another lane, reproduced
# here in a faithful topology before a line of this was written).
#
#   git exports GIT_DIR to its hooks — but ONLY from a LINKED worktree
#   (`git worktree add`), never from the main checkout. Every parallel session in
#   this repository works in a linked worktree; the hub does not. That is why the
#   hub's own pushes were clean while sessions' pushes were corrupting themselves,
#   and why the first attempt to refute the vector — run in a flat repo, the one
#   topology where GIT_DIR is not exported — came back negative and proved nothing.
#
#   GIT_DIR OUTRANKS `-C`. `git -C "$tmpdir" config user.name t` does NOT act on
#   $tmpdir when GIT_DIR is set: it acts on GIT_DIR. So a self-test that builds a
#   disposable repo under a temp dir and drives it with `git -C` drives the REAL
#   repository that is being pushed. `git init "$tmpdir"` is worse still: it
#   initialises GIT_DIR, which has no worktree, so it stamps core.bare=true on the
#   live repo.
#
#   MEASURED DAMAGE, in a sandbox reproducing the session-numbers battery
#   mkrepo() verbatim with GIT_DIR exported (before -> after):
#     user.name  REAL -> t          user.email  real@… -> t@example.invalid
#     core.bare  false -> true      tags        (none) -> base
#     refs       +refs/heads/sidelane +refs/tags/base
#     the linked worktree's HEAD    feature/real-lane -> sidelane
#   In production this left the branch of PR #526 pointing at a fixture commit.
#   The same run WITHOUT GIT_DIR exported leaves the repo untouched: the variable
#   is the whole vector.
#
# WHY UNSET IS THE CORRECT FIX AND NOT A BLUNT ONE. After the unset, git resolves
# the repository by DISCOVERY from the working directory. A hook runs with its cwd
# at the top of the worktree that invoked it, so scripts that legitimately mean
# "the repository I was run from" resolve to exactly the same repository as before,
# through a mechanism that a temp-directory argument can override. Scripts that
# mean "this throwaway repo" finally get it. Both readings become true; neither
# script has to remember which one it is.
#
# Usage:
#   HERE=$(cd -- "$(dirname -- "$0")" && pwd)
#   . "$HERE/lib/git-env.sh"        # or ../scripts/lib/git-env.sh from .githooks
#
# The gate that keeps this honest is the git-env isolation gate
# (`task lint:git-env`): a textual ratchet over every script that pairs `mktemp -d`
# with `git`, plus a BEHAVIOURAL leg that runs those scripts for real under an
# exported GIT_DIR and fails if the sandbox repository moved by one byte.

# The canonical list, WRITTEN ONCE. Every one of these redirects where git reads or
# writes; leaving any of them set is what lets a temp-dir command reach the live
# repository. Read by the gate via `olivares_git_env_vars` so the list and its
# enforcement cannot drift apart.
olivares_git_env_vars() {
	cat <<'EOF'
GIT_DIR
GIT_COMMON_DIR
GIT_WORK_TREE
GIT_INDEX_FILE
GIT_OBJECT_DIRECTORY
GIT_ALTERNATE_OBJECT_DIRECTORIES
GIT_NAMESPACE
GIT_PREFIX
GIT_QUARANTINE_PATH
GIT_CEILING_DIRECTORIES
GIT_CONFIG
GIT_CONFIG_GLOBAL
GIT_CONFIG_SYSTEM
GIT_CONFIG_COUNT
EOF
}

# Unset them in the CALLER's shell. Deliberately not `local`, deliberately not a
# subshell: a subshell would sanitise nothing the caller can see.
olivares_git_env_isolate() {
	unset GIT_DIR GIT_COMMON_DIR GIT_WORK_TREE GIT_INDEX_FILE \
		GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES \
		GIT_NAMESPACE GIT_PREFIX GIT_QUARANTINE_PATH \
		GIT_CEILING_DIRECTORIES \
		GIT_CONFIG GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT
	# GIT_CONFIG_COUNT gates GIT_CONFIG_KEY_<n>/GIT_CONFIG_VALUE_<n>; with the count
	# gone git ignores the pairs, so they need no enumeration of an unbounded index.
}

# Sourcing IS the request. A caller that sources this file and then still finds the
# environment set has been lied to, so do it now rather than making every caller
# remember a second line.
olivares_git_env_isolate
