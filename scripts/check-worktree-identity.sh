#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-worktree-identity.sh — does Git operate on the tree this script is STANDING IN?
#
# ⛔ EXISTS BECAUSE IT HAS HAPPENED THREE TIMES, and the third blocked the hub for hours.
# On 2026-08-16 at 03:36 the shared clone's `.git/config` acquired
#
#     core.worktree = <a sibling session's worktree directory>
#
# so every Git command run in the shared clone described ANOTHER SESSION'S files.
# `git status` reported 54 modified, 194 deleted and 214 untracked paths; the measurement that
# closed it is symmetric and brutal — **all 194 "deleted" existed, and NONE of the 214
# "untracked" did**. A probe file created in the clone was invisible to Git. Nothing was dirty;
# the lens was.
#
# The cost is not the confusion, it is what the obvious remedies do under it: a `reset --hard`
# or a `checkout -- .` run from the hub would have destroyed WORKING TREE, not the hub's.
# That is the exact class of the two losses of 2026-08-08, and the hub's own config still carries
# the branch `rescue/s441-canonical-worktree-2026-08-05` from an earlier round.
#
# ── WHY IT COMPARES PATHS INSTEAD OF READING THE CONFIG KEY ─────────────────────────────────
# The obvious guard —`git config --get core.worktree` and fail if set— is wrong in BOTH
# directions. Measured on git 2.39.5, a clean double dissociation:
#
#                                    `config --get core.worktree`      the tree git ACTUALLY uses
#   GIT_CONFIG_PARAMETERS='core.worktree=X'   prints X                   the real root (ignored)
#   GIT_WORK_TREE=X                           prints nothing             X          (honoured)
#
# So the key-reading guard raises a FALSE ALARM on the variable git ignores, and is BLIND to the
# one that actually redirects the tree. This file physically lives at <root>/scripts/, so its own
# path IS the true root: comparing it against `rev-parse --show-toplevel` measures the property
# — "does git operate here?" — and therefore catches any mechanism, including ones nobody has
# thought of yet.
#
# (That table also corrects `an internal design note (not shipped)`, which
# reports `GIT_CONFIG_PARAMETERS='core.worktree=…'` "still overriding core.worktree" after the
# git-env source. On this git it does not override anything; `--get` merely echoes it back. The
# audit's wider point stands — that list is not derived from `git rev-parse --local-env-vars` —
# and `GIT_WORK_TREE`, which git-env DOES unset, is the vector that would have worked.)
#
# DECLARED LIMITS, because a guard that hides its edges is worse than none:
#   · It answers for the tree it is RUN FROM. Running it inside worktree A cannot tell you that
#     worktree B is mis-pointed; the fast lints run it in whatever tree is pushing, which is the
#     tree whose commands would do the damage.
#   · Per-invocation environment poisoning is `lint:git-env`'s job. This one is about state that
#     PERSISTS after the shell that set it is gone — the kind nobody re-checks.
#
# Exit 0 = Git and this file agree on the working tree.
# Exit 1 = they disagree; the offender is named with both paths.
# Exit 2 = could not look (no git, not a repository). "I could not look" is never "it is clean".
set -uo pipefail

# ⛔ The git/scratch isolation the repo mandates for any script pairing `mktemp -d` with git (the
# self-test below does): without it an inherited GIT_DIR makes the decoy repositories operate on
# the REAL one — this file's own incident, one level down.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

verdict() { # <dir> -> "<ok|mismatch|unmeasurable>\t<detail>"
	local here git_root
	here="$(cd -- "$1" 2>/dev/null && pwd -P)" || { printf 'unmeasurable\tcannot resolve %s\n' "$1"; return; }
	command -v git >/dev/null 2>&1 || { printf 'unmeasurable\tgit is not on PATH\n'; return; }
	git_root="$(git -C "$here" rev-parse --show-toplevel 2>/dev/null)" || {
		printf 'unmeasurable\t%s is not inside a Git repository\n' "$here"; return; }
	[ -n "$git_root" ] || { printf 'unmeasurable\tgit named no working tree (bare?)\n'; return; }
	# The resolved side must be canonicalised too, or a symlinked path reads as a mismatch.
	# A target that does not exist is itself the finding, not an excuse to stay quiet.
	git_root="$(cd -- "$git_root" 2>/dev/null && pwd -P)" || {
		printf 'mismatch\tgit points at a working tree that does not exist\n'; return; }
	if [ "$here" = "$git_root" ]; then
		printf 'ok\t%s\n' "$here"
	else
		printf 'mismatch\tstanding in %s but git operates on %s\n' "$here" "$git_root"
	fi
}

main() {
	local root out state detail
	root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"
	out="$(verdict "$root")"
	state="${out%%$'\t'*}"; detail="${out#*$'\t'}"
	case "$state" in
	ok)
		echo "OK check-worktree-identity: git operates on this tree ($detail)"
		return 0 ;;
	unmeasurable)
		echo "UNVERIFIED check-worktree-identity: NO HE PODIDO MIRAR — $detail" >&2
		return 2 ;;
	*)
		cat >&2 <<EOF
FAIL check-worktree-identity: $detail

Git is describing a DIFFERENT working tree than the one you are in. Until this is fixed:
  · \`git status\` here reports the other tree's files as your changes;
  · \`reset --hard\` / \`checkout -- .\` / \`clean -fdx\` here would destroy THAT tree, not this one.

Find the mechanism before removing it — do not guess, they look nothing alike:
  git -C '$root' config --local --get core.worktree
  env | grep -E '^GIT_(WORK_TREE|DIR)='
  git -C '$root' config --local --unset core.worktree     # the 2026-08-16 case
EOF
		return 1 ;;
	esac
}

# ⛔ GLOBAL on purpose. As a \`local\` of self_test the EXIT trap fires after the function has
# returned, \`set -u\` kills it with "t: unbound variable", and the guard prints its own crash
# among its results. Measured here on 2026-08-16 — and it is the SECOND time this exact shape
# bit in this repo (scripts/census-blind-verdict.sh carries the same note).
t=""
self_test() {
	local ok=0 ko=0
	t="$(mktemp -d "${TMPDIR:-/tmp}/wt-identity.XXXXXX")" || { echo "mktemp failed" >&2; exit 2; }
	trap 'rm -rf "$t"' EXIT HUP INT TERM

	espera() { # <name> <expected-state> <dir> [VAR=value...]
		local name="$1" want="$2" dir="$3"; shift 3
		local got
		got="$(env "$@" bash -c "$(declare -f verdict); verdict '$dir'" 2>/dev/null | cut -f1)"
		if [ "$got" = "$want" ]; then
			ok=$((ok + 1)); printf '  ok    %-48s %s\n' "$name" "$got"
		else
			ko=$((ko + 1)); printf '  FALLO %-48s esperaba %s, dijo %s\n' "$name" "$want" "${got:-<vacío>}"
		fi
	}

	mkdir -p "$t/real" "$t/otro"
	( cd "$t/real" && git init -q -b main . && git commit -q --allow-empty -m x ) >/dev/null 2>&1
	: > "$t/otro/fichero"

	espera "un repositorio sano se aprueba"              ok           "$t/real"
	espera "fuera de todo repositorio no se puede ver"   unmeasurable "$t/otro"

	# (1) the 2026-08-16 mechanism: state that PERSISTS in the repository config.
	git -C "$t/real" config --local core.worktree "$t/otro"
	espera "core.worktree en el config lo caza"          mismatch     "$t/real"
	git -C "$t/real" config --local --unset core.worktree
	espera "y al quitarlo vuelve a aprobar"              ok           "$t/real"

	# (2) a mechanism sharing NOT ONE CHARACTER with the first — and the one a
	#     `config --get core.worktree` guard cannot see at all, because it reports nothing.
	espera "GIT_WORK_TREE lo caza igual"                 mismatch     "$t/real" \
		"GIT_WORK_TREE=$t/otro" "GIT_DIR=$t/real/.git"

	# (3) THE DECLARED LIMIT, pinned as a fixture so nobody re-adds a false alarm for it:
	#     git IGNORES core.worktree from the environment/-c scope, so this must stay GREEN.
	#     A guard reading the config key would fail here — wrongly.
	espera "core.worktree por entorno es INERTE (verde)" ok           "$t/real" \
		"GIT_CONFIG_PARAMETERS='core.worktree=$t/otro'"

	# (4) a LINKED worktree is legitimate and must NOT be flagged: it stands in its own tree
	#     even though its .git is a file pointing into the parent's directory.
	git -C "$t/real" worktree add -q -b rama "$t/enlazado" >/dev/null 2>&1
	if [ -d "$t/enlazado" ]; then
		espera "un worktree ENLAZADO es legítimo"        ok           "$t/enlazado"
	else
		ko=$((ko + 1)); printf '  FALLO %-48s no se pudo crear el worktree enlazado\n' "worktree enlazado"
	fi

	printf 'check-worktree-identity self-test: %d pasan, %d fallan\n' "$ok" "$ko"
	[ "$ko" -eq 0 ]
}

case "${1:-}" in
--selftest) self_test ;;
'') main ;;
*) echo "uso: $0 [--selftest]" >&2; exit 2 ;;
esac
