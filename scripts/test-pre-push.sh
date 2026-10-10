#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-pre-push.sh — .githooks/pre-push on a throwaway repository with two bare remotes and a
# one-module Go workspace under core/. The hook is fed the stdin git gives it. gitleaks and task are shims
# that record what they were asked and fail on demand; go is the real toolchain behind a shim
# that records each vet; the SPDX check is real (a scripts/*.sh file without a header is the
# planted defect). The identity leg is the task shim too: it records the push protocol and
# destination the hook hands it, because the guard it invokes reads exactly those; what the
# guard ANSWERS is the subject of scripts/test-check-commit-identity.sh, not of this wiring.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# A hook exports GIT_DIR; without this, `git init` below would write to the caller's repository.
. "$ROOT/scripts/lib/git-env.sh"
REAL_GO="$(command -v go)" || { echo "test-pre-push: go is not on PATH" >&2; exit 2; }
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
pass=0
fail=0
check() {
	if [ "$2" = "$3" ]; then
		pass=$((pass + 1))
	else
		fail=$((fail + 1))
		printf 'FAIL %s\n  want: %s\n  got:  %s\n' "$1" "$3" "$2"
	fi
}

mkdir -p "$WORK/bin"
cat >"$WORK/bin/gitleaks" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$PRE_PUSH_TEST_LOG"
exit "${PRE_PUSH_TEST_GITLEAKS_RC:-0}"
EOF
cat >"$WORK/bin/task" <<'EOF'
#!/usr/bin/env bash
printf 'task %s\n' "$*" >>"$PRE_PUSH_TEST_CALLS"
# The identity leg is wired in the hook with the push protocol and the destination, because
# the guard it runs reads exactly those three (the refs file names what is being pushed; the
# remote pair is what it checks its exclusion base against). Recording them is instrumentation
# of the wiring, not a verdict: what the guard ANSWERS is its own battery's subject.
[ "$1" != lint:commit-identity ] ||
	printf 'refs=%s remote=%s url=%s\n' "${OLIVARES_PUSH_REFS_FILE:+set}" \
		"${OLIVARES_PUSH_REMOTE_NAME:-unset}" "${OLIVARES_PUSH_REMOTE_URL:-unset}" \
		>>"$PRE_PUSH_TEST_IDENTITY_ENV"
exit "${PRE_PUSH_TEST_TASK_RC:-0}"
EOF
cat >"$WORK/bin/go" <<EOF
#!/usr/bin/env bash
[ "\$1" != vet ] || printf 'go %s\n' "\$*" >>"\$PRE_PUSH_TEST_CALLS"
exec "$REAL_GO" "\$@"
EOF
chmod +x "$WORK/bin/gitleaks" "$WORK/bin/task" "$WORK/bin/go"
unset GOWORK GOFLAGS
export PATH="$WORK/bin:$PATH" PRE_PUSH_TEST_LOG="$WORK/gitleaks.log" PRE_PUSH_TEST_CALLS="$WORK/calls.log" \
	PRE_PUSH_TEST_IDENTITY_ENV="$WORK/identity-env.log"

R="$WORK/repo"
git init -q -b main "$R"
cd "$R"
git config user.email t@example.invalid
git config user.name test
mkdir -p .githooks scripts/lib core
cp "$ROOT/.githooks/pre-push" .githooks/
cp "$ROOT/scripts/check-spdx.sh" "$ROOT/scripts/changed-go-packages.sh" scripts/
cp "$ROOT/scripts/check-web-bundle-freshness.sh" scripts/
cp "$ROOT/scripts/lib/git-env.sh" scripts/lib/
cp "$ROOT/.gitleaks.toml" .
gohdr='// SPDX-FileCopyrightText: 2026 Olivares.AI\n// SPDX-License-Identifier: AGPL-3.0-only\n\n'
printf 'go 1.26\n\nuse ./core\n' >go.work
printf 'module example.invalid/core\n\ngo 1.26\n' >core/go.mod
printf "${gohdr}package core\n\nfunc A() int { return 1 }\n" >core/a.go
printf "${gohdr}package core\n\nvar B = A()\n" >core/b.go
mkdir -p core/internal/webui/dist/assets
printf 'old\n' >core/internal/webui/dist/assets/old.js
git add -A
git commit -qm base
for r in origin other; do
	git init -q --bare "$WORK/$r.git"
	git remote add "$r" "$WORK/$r.git"
	git push -q "$r" main
done

good() { printf '#!/bin/sh\n# SPDX-FileCopyrightText: 2026 Olivares.AI\n# SPDX-License-Identifier: AGPL-3.0-only\n' >"$1"; }
bad() { printf '#!/bin/sh\necho no header\n' >"$1"; }
commit() { git add -A && git commit -qm "$1" && git rev-parse HEAD; }
zero=0000000000000000000000000000000000000000
# push <remote-arg> <local-sha> <remote-sha> [<local-sha> <remote-sha>...]: run the hook as git
# would, one stdin line per ref; prints its exit code.
push() {
	: >"$PRE_PUSH_TEST_LOG"
	: >"$PRE_PUSH_TEST_CALLS"
	: >"$PRE_PUSH_TEST_IDENTITY_ENV"
	local rc=0 remote=$1
	shift
	while [ "$#" -gt 0 ]; do
		printf 'refs/heads/t %s refs/heads/t %s\n' "$1" "$2"
		shift 2
	done | bash .githooks/pre-push "$remote" "${PRE_PUSH_TEST_URL:-url}" >"$WORK/out" 2>&1 || rc=$?
	echo "$rc"
}
calls() { paste -sd, "$PRE_PUSH_TEST_CALLS"; }
worktrees() { git worktree list | grep -c .; }
base="$(git rev-parse HEAD)"

git switch -qc clean
good scripts/good.sh
clean="$(commit clean)"
check "a clean push passes" "$(push origin "$clean" "$zero")" 0
check "it scans what origin lacks" "$(cat "$PRE_PUSH_TEST_LOG")" \
	"git --no-banner --redact --config $R/.gitleaks.toml --log-opts=$base..$clean ."
# The identity leg runs once per push, after the per-ref checks, whatever the trees look
# like: the guard judges what the push INTRODUCES (authors, committers, taggers, trailers),
# which is a property of the protocol, not of any one ref's tree. The 2026-10-05 rewrite of
# the hook kept the tree checks and dropped the guard's only caller, and a commit authored by
# a private address reached main the same week — these checks are the wiring's reproducer.
check "a change without Go runs the identity leg only" "$(calls)" "task lint:commit-identity"
check "the identity leg receives the push protocol and destination" "$(cat "$PRE_PUSH_TEST_IDENTITY_ENV")" \
	"refs=set remote=origin url=url"
check "a red identity verdict refuses the push" "$(PRE_PUSH_TEST_TASK_RC=1 push origin "$clean" "$zero")" 1
check "a push to a URL with no remote checks everything" \
	"$(PRE_PUSH_TEST_URL=https://example.invalid/x.git push https://example.invalid/x.git "$clean" "$zero")" 0
check "it scans the whole pushed history" "$(cat "$PRE_PUSH_TEST_LOG")" \
	"git --no-banner --redact --config $R/.gitleaks.toml --log-opts=$clean ."
check "the identity leg receives the URL the push names, not a constant" "$(tail -1 "$PRE_PUSH_TEST_IDENTITY_ENV")" \
	"refs=set remote=https://example.invalid/x.git url=https://example.invalid/x.git"
# git always passes a URL, but a hook that crashed without one would turn a wiring detail
# into a refused push: ${2:-} keeps the guard's URL cross-check simply absent (its designed
# answer, named in its own verdict), never an unbound variable.
: >"$PRE_PUSH_TEST_CALLS"
: >"$PRE_PUSH_TEST_IDENTITY_ENV"
_nourl_rc=0
printf 'refs/heads/t %s refs/heads/t %s\n' "$clean" "$zero" |
	bash .githooks/pre-push origin >"$WORK/out" 2>&1 || _nourl_rc=$?
check "the hook with no URL argument at all still pushes" "$_nourl_rc/$(calls)" "0/task lint:commit-identity"
check "and the leg records the absent URL as absent" "$(tail -1 "$PRE_PUSH_TEST_IDENTITY_ENV")" \
	"refs=set remote=origin url=unset"
check "a gitleaks finding refuses the push" "$(PRE_PUSH_TEST_GITLEAKS_RC=1 push origin "$clean" "$zero")" 1
check "a deletion pushes nothing to check" "$(push origin "$zero" "$clean")" 0
check "a deletion push still runs the identity leg" "$(calls)" "task lint:commit-identity"
git push -q origin clean
git fetch -q origin
check "a tip the destination holds needs no check" "$(push origin "$clean" "$zero")/$(grep -c . "$PRE_PUSH_TEST_LOG" || true)" 0/0

# An update is checked from the destination's tip, not from the branch's start.
good scripts/next.sh
next="$(commit next)"
check "an update push passes" "$(push origin "$next" "$clean")" 0
check "it scans only the update" "$(cat "$PRE_PUSH_TEST_LOG")" \
	"git --no-banner --redact --config $R/.gitleaks.toml --log-opts=$clean..$next ."

# Generated console output belongs to the build, never to a source push; deleting it is how an
# old tree migrates it out of Git, so a net deletion is not a finding.
git switch -qc distPush "$base"
mkdir -p core/internal/webui/dist/assets
printf 'app\n' >core/internal/webui/dist/assets/app.js
dist="$(commit dist)"
check "a push that adds generated console output is refused" "$(push origin "$dist" "$zero")" 1
rc=0
grep -q 'do not commit generated console output' "$WORK/out" || rc=$?
check "and the generated file is named" "$rc" 0
printf 'app2\n' >core/internal/webui/dist/assets/app2.js
dist2="$(commit dist2)"
check "an update push that adds generated console output is refused" "$(push origin "$dist2" "$dist")" 1
git switch -qc distRm "$base"
git rm -rq core/internal/webui/dist
rm_dist="$(commit remove-dist)"
check "a push that only deletes generated output passes" "$(push origin "$rm_dist" "$zero")" 0

# A Go change runs the boundary check and vets its package.
git switch -qc goChange "$base"
printf "${gohdr}package core\n\nvar B = A() + 1\n" >core/b.go
gochange="$(commit go)"
check "a Go change passes" "$(push origin "$gochange" "$zero")" 0
check "it runs the boundary check, vets the package and then the identity leg" "$(calls)" \
	"task lint:boundary,go vet ./core,task lint:commit-identity"
check "a failing boundary check refuses the push" "$(PRE_PUSH_TEST_TASK_RC=1 push origin "$gochange" "$zero")" 1

# A deletion alone can break what used the deleted code.
git switch -qc deleteOnly "$base"
git rm -q core/a.go
deleted="$(git commit -qm 'delete A' && git rev-parse HEAD)"
check "a push that only deletes Go code is vetted and refused" "$(push origin "$deleted" "$zero")" 1
rc=0
grep -q 'undefined: A' "$WORK/out" || rc=$?
check "and vet names what broke" "$rc" 0

# A package Go cannot load refuses the push instead of selecting nothing.
git switch -qc unloadable "$base"
printf "${gohdr}package other\n" >core/c.go
unloadable="$(commit unloadable)"
check "a package that cannot load refuses the push" "$(push origin "$unloadable" "$zero")" 2

# A ref whose tree has none of the scripts (a data ref such as a status board, or a branch older
# than this hook) is checked with this checkout's scripts.
blob="$(printf '# status\n' | git hash-object -w --stdin)"
data="$(git commit-tree "$(printf '100644 blob %s\tstatus.md\n' "$blob" | git mktree)" -m data)"
check "a ref without the scripts in its tree is checked with this checkout's" "$(push origin "$data" "$zero")" 0
check "it scans the data ref's history" "$(cat "$PRE_PUSH_TEST_LOG")" \
	"git --no-banner --redact --config $R/.gitleaks.toml --log-opts=$data ."

# The defect only another remote holds is still new to origin.
git switch -qc twoRemotes "$base"
bad scripts/bad.sh
commit bad >/dev/null
git push -q other twoRemotes
git fetch -q other
good scripts/later.sh
on_top="$(commit later)"
check "a commit known only to another remote is checked for this one" "$(push origin "$on_top" "$zero")" 1
rc=0
grep -q 'MISSING  scripts/bad.sh' "$WORK/out" || rc=$?
check "and its missing header is named" "$rc" 0
check "every ref of a push is checked" "$(push origin "$gochange" "$zero" "$on_top" "$zero")" 1

# The pushed commit, not the working tree, is what is checked.
git switch -qc tree "$base"
bad scripts/fixed-later.sh
pushed="$(commit bad)"
good scripts/fixed-later.sh
check "a committed defect with an uncommitted fix is refused" "$(push origin "$pushed" "$zero")" 1
fixed="$(commit fixed)"
check "the fixed commit passes" "$(push origin "$fixed" "$zero")" 0
# Once per PUSH, not once per ref: the guard judges the protocol whole, and a regression
# moving the call inside the per-ref loop would double every push's identity cost silently.
check "a two-ref push runs the identity leg exactly once" \
	"$(push origin "$gochange" "$zero" "$fixed" "$zero")"/"$(awk '/lint:commit-identity/{c++} END{print c+0}' "$PRE_PUSH_TEST_CALLS")" "0/1"
check "an older defective commit pushed from a fixed checkout is refused" "$(push origin "$pushed" "$zero")" 1
check "a passing push of another commit passes" "$(push origin "$gochange" "$zero")" 0
check "and leaves no temporary checkout" "$(worktrees)" 1
printf "${gohdr}package core\n\nvar C = D()\n" >core/c.go
uses_d="$(git add core/c.go && git commit -qm 'uses D' && git rev-parse HEAD)"
printf "${gohdr}package core\n\nfunc D() int { return 4 }\n" >core/d.go
check "a commit that needs an untracked file is refused" "$(push origin "$uses_d" "$zero")" 1
rm core/d.go
check "a refused push leaves no temporary checkout" "$(worktrees)" 1

# A rebased branch is checked for its own change, not for what it was rebased onto.
git switch -qc topic "$base"
good scripts/topic.sh
topic="$(commit topic)"
git push -q origin topic
git switch -q main
printf "${gohdr}package core\n\nvar B = A() + 2\n" >core/b.go
moved="$(commit main-moved)"
git push -q origin main
git fetch -q origin
git switch -q topic
git rebase -q main
rebased="$(git rev-parse HEAD)"
check "a rebased branch passes" "$(push origin "$rebased" "$topic")" 0
check "it is checked from the newest commit the destination holds" "$(cat "$PRE_PUSH_TEST_LOG")/$(calls)" \
	"git --no-banner --redact --config $R/.gitleaks.toml --log-opts=$moved..$rebased ./task lint:commit-identity"

# After a merge of main, the newest commit the destination holds is main's, so the push is
# checked for the branch's own change. Commit dates are fixed: newest means by commit time.
git switch -qc merged "$moved"
good scripts/merged.sh
own="$(export GIT_COMMITTER_DATE=2026-01-01T00:00:00Z; commit merged)"
git push -q origin merged
git switch -q main
printf "${gohdr}package core\n\nvar B = A() + 3\n" >core/b.go
again="$(export GIT_COMMITTER_DATE=2026-01-02T00:00:00Z; commit main-again)"
git push -q origin main
git fetch -q origin
git switch -q merged
GIT_COMMITTER_DATE=2026-01-03T00:00:00Z git merge -q --no-edit main
mergetip="$(git rev-parse HEAD)"
check "a branch that merged main passes" "$(push origin "$mergetip" "$own")" 0
check "it is checked from main, the newest commit the destination holds" "$(cat "$PRE_PUSH_TEST_LOG")/$(calls)" \
	"git --no-banner --redact --config $R/.gitleaks.toml --log-opts=$again..$mergetip ./task lint:commit-identity"

echo "test-pre-push: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
