# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# git-env.sh — SOURCE THIS before any script builds a throwaway git repository.
# Not executable, not a program: it exists to be `.`-sourced, and it unsets the
# ambient git environment in the CALLER's shell.
#
# THE DEFECT IT CLOSES, measured 2026-08-06 (reported by another contributor, reproduced
# here in a faithful topology before a line of this was written).
#
#   git exports GIT_DIR to its hooks — but ONLY from a LINKED worktree
#   (`git worktree add`), never from the main checkout. Every parallel session in
#   this repository works in a linked worktree; the main checkout does not. That is why
#   pushes from the main checkout were clean while those from a linked worktree corrupted
#   themselves,
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

# ---------------------------------------------------------------------------
# OWNED OBJECT STORE — for git commands that WRITE objects nobody will keep.
#
# `git merge-tree --write-tree` writes the merged tree (and, on conflict, the blobs
# with markers) into the repository's object store. Measured with git 2.39.5 in a
# disposable repository: 2 loose objects for a conflicting merge, 1 for a divergent
# clean one, 0 for a fast-forward. A caller that answers "conflict" and exits leaves
# them unreachable. These functions make such writes land in a temporary object
# directory the caller owns, while every read still reaches the repository:
#
#   · The read-through sources are the EXPLICITLY RESOLVED object directory of the
#     repository (`git rev-parse --path-format=absolute --git-path objects`, the common
#     directory from a linked worktree, in git's own path form) and, listed BEFORE it, the
#     alternates GIT ITSELF resolved for that repository. Nothing here decodes an
#     alternates file. `git count-objects -v` prints every store git linked, in the order
#     it linked them (a store before the stores it borrows from), as absolute paths in
#     git's own C quoting where a path needs it; those lines are copied verbatim, and git
#     parses its own quoting when it reads them back. Quoted entries with `\"`, `\\`, `\t`
#     or octal escapes, relative entries and paths with spaces or colons therefore
#     resolve exactly as for plain git: a relative entry against the objects directory
#     that lists it. A verbatim copy of the file resolved relative entries against the
#     owned store instead (measured). The file keeps git's newline separator on every
#     platform; nothing goes through the PATH_SEP-delimited
#     GIT_ALTERNATE_OBJECT_DIRECTORIES. Without an `info/alternates` file nothing runs;
#     with one, count-objects also walks the repository's loose objects (measured:
#     51-60 ms for 20 000 of them at load 11).
#   · The order is not style. git follows nested alternates only a fixed number of levels
#     below the store that lists them, so listing just the repository store costs one
#     level: a chain of six shared clones that plain git reads became unreadable
#     (measured). With git's resolved stores first, each is linked with the budget plain
#     git gives it, and the repository's own recursion finds them already linked.
#   · The repository is only READ: no config, alternates file or ref is written.
#   · The override is per command (`olivares_git_owned`) and NEVER exported: a child of
#     the caller — a runner, another session — must not inherit where objects go.
#   · It refuses instead of guessing. git does not fail on an alternate it cannot follow;
#     it answers over fewer objects. So open refuses when the alternates file cannot be
#     read (git only warns and reads nothing), when resolving it makes git print an
#     `error:` (an entry it cannot find or normalize, which is also where a broken quote
#     ends up, or its nesting limit), and when the objects the caller names are not
#     readable through the store. It returns non-zero leaving nothing behind, and the
#     caller answers "could not look".
#   · The wrapper refuses when no store is open: falling back to plain git would
#     silently restore the write this exists to remove.
#   · An object that already exists is not rewritten, but git refreshes its mtime when
#     a write finds it (measured): the inventory does not change, a timestamp may.
#
# Usage — arm the cleanup BEFORE opening; a SIGKILL runs no cleanup at all:
#   trap olivares_git_owned_store_close EXIT
#   trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM
#   olivares_git_owned_store_open "$BASE" "$TIP" || { …could not look… }
#   tree=$(olivares_git_owned merge-tree --write-tree "$BASE" "$TIP")
#   olivares_git_owned diff --numstat "$BASE" "$tree"   # that tree exists only in the store
# This library must load in Bash and dash, including callers that use /bin/sh.
olivares_git_owned_store_open() { # <object>... ; sets OLIVARES_GIT_OWNED_STORE, not exported
	local objects store object
	olivares_git_owned_store_close
	objects=$(git rev-parse --path-format=absolute --git-path objects 2>/dev/null) || return 1
	# A literal newline preserves this refusal in both shells.
	case "$objects" in
	'' | *'
'*) return 1 ;;
	esac
	[ -d "$objects" ] || return 1
	store=$(mktemp -d "${TMPDIR:-/tmp}/olivares-owned-objects.XXXXXX") || return 1
	[ -n "$store" ] && [ -d "$store" ] || return 1
	OLIVARES_GIT_OWNED_STORE=$store
	if ! mkdir -- "$store/info" || ! : >"$store/info/alternates"; then
		olivares_git_owned_store_close
		return 1
	fi
	# Preserve Git output bytes and check each command before publishing alternates.
	if [ -e "$objects/info/alternates" ]; then
		if [ ! -r "$objects/info/alternates" ] ||
			! LC_ALL=C git count-objects -v >"$store/info/resolve.out" 2>"$store/info/resolve.err" ||
			grep -q '^error: ' "$store/info/resolve.err" ||
			! sed -n 's/^alternate: //p' <"$store/info/resolve.out" >"$store/info/alternates"; then
			olivares_git_owned_store_close
			return 1
		fi
		rm -f -- "$store/info/resolve.out" "$store/info/resolve.err"
	fi
	if ! printf '%s\n' "$objects" >>"$store/info/alternates"; then
		olivares_git_owned_store_close
		return 1
	fi
	for object in "$@"; do
		if ! GIT_OBJECT_DIRECTORY="$store" git cat-file -e "$object" 2>/dev/null; then
			olivares_git_owned_store_close
			return 1
		fi
	done
	return 0
}

# git <args>, with its object writes confined to the open owned store.
olivares_git_owned() {
	if [ -z "${OLIVARES_GIT_OWNED_STORE:-}" ] || [ ! -f "$OLIVARES_GIT_OWNED_STORE/info/alternates" ]; then
		printf 'olivares_git_owned: no owned object store is open; refusing to let git write into the repository store\n' >&2
		return 125
	fi
	GIT_OBJECT_DIRECTORY="$OLIVARES_GIT_OWNED_STORE" git "$@"
}

# Removes only the store this shell opened. Safe to call twice and from a trap.
olivares_git_owned_store_close() {
	local store="${OLIVARES_GIT_OWNED_STORE:-}"
	unset OLIVARES_GIT_OWNED_STORE
	[ -n "$store" ] || return 0
	rm -rf -- "$store"
}

# Sourcing IS the request. A caller that sources this file and then still finds the
# environment set has been lied to, so do it now rather than making every caller
# remember a second line.
olivares_git_env_isolate
# An owned store belongs to the shell that opened it. A value inherited from the
# environment was never created here, so no cleanup may treat it as one.
unset OLIVARES_GIT_OWNED_STORE
