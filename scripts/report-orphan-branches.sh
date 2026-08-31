#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# report-orphan-branches.sh — remote branches carrying commits outside main with NO pull request.
#
# ⛔ THIS IS A REPORT, NOT A GATE, and the distinction is deliberate rather than lazy.
#
# A branch with commits and no PR is not a defect: it is what work in progress looks like on
# the second day. Nobody can say from here whether a given one is alive or abandoned — the
# author can. So this prints and refuses to grade, and it is invoked ON PURPOSE
# (`task report:orphan-branches`) rather than riding the push gate.
#
# The alternative was worse and this repository has now measured it twice: a check that only
# WARNS is a number nobody acts on. The nightly submodule drift detector printed the
# commits-behind count every night as a `::warning`, inside a run that was red for another
# reason, while the artefact being sold compiled with vulnerabilities already closed. Adding a
# second permanent warning to the push path would buy the same nothing.
#
# WHY IT EXISTS AT ALL. On 2026-08-15, cleaning worktrees for disk, three branches turned up
# with commits outside main and no pull request — 60 commits, 5 commits, 2 commits. They were
# found BY ACCIDENT. A pushed branch with no PR is the worst way to hold work: it outlives the
# worktree that made it, appears in no queue, is read by no gate, and the next relay has no way
# to learn it exists. Two of the three turned out superseded; that is not the point. The point
# is that nobody could have known without looking, and nothing was looking.
#
# Exit 0 report produced (whatever it contains) · 2 could not look. There is no exit 1: this
# does not have a verdict to give.
set -uo pipefail

blind() { printf 'report-orphan-branches: UNVERIFIED — %s\n' "$*" >&2; exit 2; }

command -v git >/dev/null 2>&1 || blind "git is not on PATH"
command -v gh  >/dev/null 2>&1 || blind "gh is not on PATH; the PR side of the question cannot be asked,
  and a list of branches without it would be a list of every branch — which answers nothing"

REMOTE="${OLIVARES_REPORT_REMOTE:-origin}"
TRUNK="${OLIVARES_REPORT_TRUNK:-main}"

git rev-parse --git-dir >/dev/null 2>&1 || blind "not inside a git repository"
git fetch -q "$REMOTE" "$TRUNK" 2>/dev/null \
	|| blind "could not fetch $REMOTE/$TRUNK; measuring against a stale local ref would report
  branches as ahead that were merged hours ago"
BASE="$(git rev-parse --verify --quiet FETCH_HEAD)" \
	|| blind "could not resolve $REMOTE/$TRUNK after fetching"

# The PR set comes from the forge, once, rather than one call per branch: a per-branch loop over
# a hundred refs is slow enough that somebody runs this with a filter and then believes the
# filtered answer.
prs="$(gh pr list --state open --limit 300 --json headRefName --jq '.[].headRefName' 2>/dev/null)" \
	|| blind "gh pr list failed; without the open-PR set every branch would look orphaned"

# Remote refs, read from the remote itself. `git branch -r` reads this clone's cached view, and
# a clone that has not fetched a deleted branch still lists it — a report naming branches that
# no longer exist teaches its reader to ignore it.
refs="$(git ls-remote --heads "$REMOTE" 2>/dev/null)" || blind "git ls-remote failed"
[ -n "$refs" ] || blind "the remote reports no branches at all, which is a broken query rather than an empty repository"

total=0; orphan=0
# Rows are BUFFERED rather than streamed, because the SHARED column below cannot be known until
# every branch has been read. Streaming and then correcting would print two answers to one
# question, which is how a reader learns to trust the first number they see.
rows=()
SHARE_TSV="$(mktemp)"; trap 'rm -f "$SHARE_TSV"' EXIT
while IFS= read -r line; do
	sha="${line%%	*}"; ref="${line#*	}"; name="${ref#refs/heads/}"
	[ -n "$name" ] || continue
	[ "$name" = "$TRUNK" ] && continue
	total=$((total + 1))
	case $'\n'"$prs"$'\n' in *$'\n'"$name"$'\n'*) continue ;; esac   # has an open PR
	# Merged branches are not orphans; they are finished. Ancestry, not a date.
	git merge-base --is-ancestor "$sha" "$BASE" 2>/dev/null && continue
	# The object may be absent locally (never fetched). Fetch just this one, shallowly enough to
	# count — and if that fails, SAY the branch could not be measured rather than dropping it.
	if ! git cat-file -e "$sha^{commit}" 2>/dev/null; then
		git fetch -q "$REMOTE" "$name" 2>/dev/null || {
			rows+=("$name"$'\t''?'$'\t''?'$'\t''?'$'\t''could not fetch — NOT measured')
			orphan=$((orphan + 1)); continue
		}
	fi
	# ⛔ AND THEN BY PATCH-ID, because ancestry does not see a SQUASH MERGE. Corrected 2026-08-15
	# by the console lane, with a counter-example rather than an argument: a branch listed here as
	# orphan with one commit carried a commit patch-equivalent to one already in main -- content
	# inside, ancestry broken. Every squash-merged branch nobody deleted inflated this list, so the
	# first run's "125" was a CEILING, not a count.
	#
	# `git cherry` marks '-' for what is already in by content and '+' for what is not. A branch
	# whose every commit comes back '-' is finished, not orphaned, and saying otherwise sends its
	# author looking for work that landed.
	cherry="$(git cherry "$BASE" "$sha" 2>/dev/null)" || cherry=""
	if [ -n "$cherry" ]; then
		printf '%s\n' "$cherry" | grep -q '^+' || continue
	fi
	ahead="$(git rev-list --count "$BASE".."$sha" 2>/dev/null || echo '?')"
	when="$(git show -s --format='%cs' "$sha" 2>/dev/null || echo '?')"
	# ⛔ AND THE COLUMN THAT DECIDES WHETHER IT MAY BE DELETED: files this branch has that the
	# trunk does NOT. Learned by getting it wrong three times on 2026-08-15, each time with a
	# predicate that sounded sufficient:
	#
	#   ancestry     — a squash merge breaks it while putting the content in
	#   patch-id     — counts patches, so work that landed rebased shows as pending
	#   line counts  — "main is 758 commits ahead with more lines" is VOLUME; the question is
	#                  EXISTENCE, and they are different questions
	#
	# The third one nearly deleted a v3-credential-envelope conformance test and a 398-line
	# work-kernel composition test, and a sibling branch held 3393 lines of a legal-entity
	# migration that were in neither the trunk nor its own open PR. A branch with NEW>0 must not
	# be deleted by anyone, whatever the other columns say.
	#
	# Generated artefacts are excluded: a rebuilt bundle is not new work, and counting it would
	# mark every stale console branch as precious and teach the reader to ignore the column.
	new=0
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		case "$f" in sessions/*|design/*|core/internal/webui/dist/*) continue ;; esac
		git cat-file -e "$BASE:$f" 2>/dev/null || { new=$((new + 1)); printf '%s\t%s\n' "$f" "$name" >> "$SHARE_TSV"; }
	done <<INNER
$(git diff --name-only "$BASE...$sha" 2>/dev/null)
INNER
	rows+=("$name"$'\t'"$ahead"$'\t'"$new"$'\t'"$when")
	orphan=$((orphan + 1))
done <<EOF
$refs
EOF

# ⛔ THE SHARED COLUMN, and it exists because the NEW rule above was too blunt on its first day.
# NEW>0 was written as "must not be deleted by anyone", which is right when the files are this
# branch's own work — and useless when they are not. Measured 2026-08-15: FIVE files appeared as
# NEW in ELEVEN separate branches. They were not eleven inventions. They were the predecessors
# that replaced in the trunk (features/agentops/agentops-view.tsx and the sessions views;
# e2e/agentops.spec.ts, whose only test() is named identically to the one in main's
# sessions-unified.spec.ts). Every branch cut before that restructure carries them, so a rule
# reading NEW alone froze eleven branches over five dead files, permanently.
#
# So the report says how many OTHER branches carry each NEW file. It does NOT exclude anything and
# it does not grade: a file eleven branches have and the trunk lacks is far more likely to be
# something the trunk REPLACED than eleven independent inventions — but "far more likely" is a
# reading, and the reader makes it. An exclusion list here would become a place to silence, which
# is the failure this file already refuses elsewhere.
shared_of() { # count how many of a branch's NEW files are carried by >=2 branches
	awk -F'\t' -v b="$1" '{ n[$1]++; if ($2 == b) mine[$1] = 1 }
		END { c = 0; for (f in mine) if (n[f] > 1) c++; print c + 0 }' "$SHARE_TSV"
}
printf '%-52s %8s %5s %7s  %s\n' 'BRANCH' 'AHEAD' 'NEW' 'SHARED' 'LAST COMMIT'
printf '%-52s %8s %5s %7s  %s\n' '----------------------------------------------------' '--------' '-----' '-------' '-----------'
for r in "${rows[@]:-}"; do
	[ -n "$r" ] || continue
	IFS=$'\t' read -r rname rahead rnew rwhen <<< "$r"
	if [ "$rnew" = '?' ]; then printf '%-52s %8s %5s %7s  %s\n' "$rname" '?' '?' '?' "$rwhen"; continue; fi
	printf '%-52s %8s %5s %7s  %s\n' "$rname" "$rahead" "$rnew" "$(shared_of "$rname")" "$rwhen"
done

printf '\n%d of %d remote branch(es) carry commits outside %s and have NO open pull request.\n' \
	"$orphan" "$total" "$TRUNK"
printf 'NEW = files this branch has that %s does not (generated bundles excluded).\n' "$TRUNK"
printf 'SHARED = how many of those NEW files ANOTHER listed branch also has. NEW minus SHARED is the\n'
printf 'part that is this branch alone, and that part must not be deleted by anyone: three predicates\n'
printf 'that sounded sufficient -- ancestry, patch-id and line counts -- each nearly lost real work\n'
printf 'here on 2026-08-15. A high SHARED is a HINT that the trunk replaced those files rather than\n'
printf 'losing them; it is not a verdict, and this report does not give one.\n\n'
if [ -s "$SHARE_TSV" ]; then
	dupes="$(awk -F'\t' '{ n[$1]++ } END { for (f in n) if (n[f] > 1) printf "%d\t%s\n", n[f], f }' "$SHARE_TSV" | sort -rn | head -12)"
	if [ -n "$dupes" ]; then
		printf 'NEW files carried by more than one branch (most-shared first) -- check these against the\n'
		printf 'trunk for a SUCCESSOR before reading any of them as work in danger:\n'
		printf '%s\n' "$dupes" | while IFS=$'\t' read -r c f; do printf '  %2s branches  %s\n' "$c" "$f"; done
		printf '\n'
	fi
fi
printf 'This is a REPORT: none of these is necessarily wrong. A branch is alive or abandoned by a\n'
printf 'judgement its author makes, not by anything measurable from here. What it buys is that\n'
printf 'nobody has to find them by accident again.\n'
exit 0
