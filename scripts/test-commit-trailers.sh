#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Exercise real commits through commit-msg and the CI range checker.
set -euo pipefail
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
. "$HERE/lib/git-env.sh"
ROOT="$(cd "$HERE/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-$ROOT}/.commit-trailers.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
export LC_ALL=C
git init -q "$WORK/repo"
cd "$WORK/repo"
git config user.name 'Test'
git config user.email 'test@example.invalid'
git config commit.gpgsign false
git -c core.hooksPath=/dev/null commit -q --allow-empty -m 'chore: seed'
DCO='Signed-off-by: Test <test@example.invalid>'
fails=0
check() {
  local name="$1" want="$2" message="$3" rc=0
  local cleanup=(--cleanup=verbatim)
  if [[ "${4:-}" == edited ]]; then
    cleanup=(--edit)
  elif [[ $# -gt 3 ]]; then
    cleanup=("${@:4}")
  fi
  printf '%s\n' "$message" > "$WORK/msg"
  GIT_EDITOR=true git -c core.hooksPath="${TEST_HOOKS:-$ROOT/.githooks}" commit -q --allow-empty -F "$WORK/msg" "${cleanup[@]}" >"$WORK/hook.log" 2>&1 || rc=$?
  if [[ "$rc" != "$want" ]]; then
    echo "FAIL hook: $name (expected $want, got $rc)"; cat "$WORK/hook.log"; fails=$((fails + 1))
  fi
  if [[ "$rc" == 0 ]] && ! bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD >"$WORK/ci.log" 2>&1; then
    echo "FAIL stored hook message: $name"; cat "$WORK/ci.log"; fails=$((fails + 1))
  fi
  if [[ "$want" == 1 && "$rc" == 1 ]]; then
    diagnostic='DCO trailer is required'
    if printf '%s\n' "$message" | grep -qiE '^[[:space:]]*(co-(authored|developed)-by|assisted-by|generated-(by|with))[[:space:]]*:'; then
      diagnostic='attribution trailers are refused'
    fi
    if ! grep -q "$diagnostic" "$WORK/hook.log"; then
      echo "FAIL hook diagnostic: $name"; cat "$WORK/hook.log"; fails=$((fails + 1))
    fi
  fi
  # Bypass only in the disposable repo to simulate a web/API commit reaching CI.
  GIT_EDITOR=true git -c core.hooksPath=/dev/null commit -q --allow-empty -F "$WORK/msg" "${cleanup[@]}"
  rc=0
  bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD >"$WORK/ci.log" 2>&1 || rc=$?
  if [[ "$rc" != "$want" ]]; then
    echo "FAIL CI: $name (expected $want, got $rc)"; fails=$((fails + 1))
  fi

}
check 'DCO only' 0 "fix(hooks): valid

$DCO"
# Git accepts negated flags even when its help only lists the positive form.
for flag in --no-quiet --no-all --no-reset-author; do
  check "supported negative option $flag" 0 "fix(hooks): supported option

$DCO" --edit "$flag"
done
# Exercise the real hook with only parent-argument discovery made unavailable.
mkdir -p "$WORK/no-procfs/.githooks" "$WORK/no-procfs/scripts"
sed 's/--commit-msg "$PPID"/--commit-msg 999999999/' "$ROOT/.githooks/commit-msg" > "$WORK/no-procfs/.githooks/commit-msg"
chmod +x "$WORK/no-procfs/.githooks/commit-msg"
ln -s "$HERE/check-commit-trailers.sh" "$WORK/no-procfs/scripts/check-commit-trailers.sh"
TEST_HOOKS="$WORK/no-procfs/.githooks"
for char in '#' ';' auto; do
  git config core.commentChar "$char"
  # Both hook and bypass control need an actual staged diff: allow-empty
  # commits cannot reproduce the verbose appendix that broke portability.
  for status in --status --no-status; do
    for hooks in "$TEST_HOOKS" /dev/null; do
      printf '%s\n' "$char $status $hooks" >> portable-verbose.txt
      git add portable-verbose.txt
      rc=0
      GIT_EDITOR=true git -c core.hooksPath="$hooks" commit -q -s --edit --verbose \
        "$status" -m 'fix(hooks): verbose portability' \
        -m '# body selects a different automatic marker' >"$WORK/hook.log" 2>&1 || rc=$?
      if [[ "$rc" != 0 ]]; then
        echo "FAIL hook: unavailable parent verbose marker=$char status=$status hooks=$hooks"
        cat "$WORK/hook.log"; fails=$((fails+1))
      elif ! bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD >"$WORK/ci.log" 2>&1; then
        echo "FAIL CI: unavailable parent verbose marker=$char status=$status"
        cat "$WORK/ci.log"; fails=$((fails+1))
      fi
    done
  done
  check "unavailable parent edited DCO marker=$char" 0 "fix(hooks): portable

# body selects a different automatic marker

$DCO" --edit
  check "unavailable parent no-status marker=$char" 0 "fix(hooks): portable

# body comment
; another body comment

$DCO" --edit --no-status
  check "unavailable parent retained prose marker=$char" 1 "fix(hooks): portable

$DCO

; retained prose" --no-edit --cleanup=verbatim
  check "unavailable parent edited unsigned marker=$char" 1 'fix(hooks): unsigned' --edit
  check "unavailable parent retained attribution marker=$char" 1 "fix(hooks): attribution

$DCO
Generated-With: Example" --edit
done
# An imitated editor footer is indistinguishable from Git's real footer.
# Portable normalization must preserve it and leave a genuine final DCO even
# when Git retains every byte; it must never hide attribution or invent DCO.
git config core.commentChar ';'
footer="; Please enter the commit message for your changes. Lines starting
; with ';' will be ignored, and an empty message aborts the commit.
;
; Preserved editor comment"
commit_footer=$footer
for footer in "$commit_footer" "; Please enter a commit message to explain why this merge is necessary,
; especially if it merges an updated upstream into a topic branch.
;
; Lines starting with ';' will be ignored, and an empty message aborts
; the commit."; do
  for suffix in '' 'Generated-With: Example' 'More prose after the signoff.'; do
    printf 'fix(hooks): portable footer\n\nBody stays intact.\n\n%s\n\n%s\n%s\n' "$DCO" "$suffix" "$footer" > "$WORK/msg"
    cp "$WORK/msg" "$WORK/original"
    rc=0
    GIT_EDITOR=: git -c core.hooksPath="$TEST_HOOKS" commit -q --allow-empty --no-edit --cleanup=verbatim -F "$WORK/msg" >"$WORK/hook.log" 2>&1 || rc=$?
    if [[ -z "$suffix" ]]; then
      if [[ "$rc" != 0 ]]; then
        echo 'FAIL hook: portable verbatim footer'; fails=$((fails+1))
      else
        git show -s --format=%B HEAD > "$WORK/stored"
        if ! bash "$HERE/check-commit-trailers.sh" "$WORK/stored"; then
          echo 'FAIL CI: portable verbatim footer'; fails=$((fails+1))
        fi
        # Ordering and blank space may change; every content line must survive.
        sed '/^[[:space:]]*$/d' "$WORK/original" | sort > "$WORK/before-lines"
        sed '/^[[:space:]]*$/d' "$WORK/stored" | sort > "$WORK/after-lines"
        if ! cmp -s "$WORK/before-lines" "$WORK/after-lines" ||
           [[ "$(grep -c '^Signed-off-by:' "$WORK/stored")" != 1 ]] ||
           ! grep -q '^Body stays intact.$' "$WORK/stored"; then
          echo 'FAIL: portable normalization changed body, footer or DCO count'; fails=$((fails+1))
        fi
      fi
    elif [[ "$rc" != 1 ]]; then
      echo 'FAIL hook: portable footer hides invalid message'; fails=$((fails+1))
    elif ! cmp -s "$WORK/original" .git/COMMIT_EDITMSG; then
      echo 'FAIL: refused portable message was rewritten'; fails=$((fails+1))
    fi
  done
done
footer=$commit_footer
# Portable normalization must keep DCO before a possible scissors cut while
# retaining the appendix if the caller uses verbatim cleanup without verbose.
printf 'fix(hooks): portable scissors\n\n%s\n\n%s\n; ------------------------ >8 ------------------------\ndiff --git a/file b/file\n--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n' "$DCO" "$footer" > "$WORK/msg"
cp "$WORK/msg" "$WORK/original"
if ! bash "$HERE/check-commit-trailers.sh" --commit-msg 999999999 "$WORK/msg" >"$WORK/hook.log" 2>&1; then
  echo 'FAIL: portable scissors footer rejected'; fails=$((fails+1))
elif ! bash "$HERE/check-commit-trailers.sh" "$WORK/msg" ||
     ! sed '/^; ------------------------ >8 ------------------------/,$d' "$WORK/msg" | bash "$HERE/check-commit-trailers.sh"; then
  echo 'FAIL: portable footer moved DCO below scissors'; fails=$((fails+1))
fi
sed '/^---$/d' "$WORK/msg" > "$WORK/without-divider"
if ! cmp -s "$WORK/original" "$WORK/without-divider"; then
  echo 'FAIL: portable scissors normalization changed original content'; fails=$((fails+1))
fi
appendix_message="$(cat "$WORK/original")"
printf '%s\n' "$appendix_message" > "$WORK/msg"
if ! git -c core.hooksPath="$TEST_HOOKS" commit -q --allow-empty --no-edit --cleanup=verbatim \
    -F "$WORK/msg" >"$WORK/hook.log" 2>&1; then
  echo 'FAIL hook: portable copied verbose appendix retained'; fails=$((fails+1))
else
  git show -s --format=%B HEAD > "$WORK/stored"
  if ! bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD; then
    echo 'FAIL CI: normalized copied verbose appendix'; fails=$((fails+1))
  fi
  sed '/^[[:space:]]*$/d' "$WORK/original" > "$WORK/before-lines"
  sed '/^---$/d; /^[[:space:]]*$/d' "$WORK/stored" > "$WORK/after-lines"
  if ! cmp -s "$WORK/before-lines" "$WORK/after-lines"; then
    echo 'FAIL: copied verbose appendix lost content or duplicated DCO'; fails=$((fails+1))
  fi
fi
check 'portable copied verbose appendix attribution' 1 "$appendix_message
Generated-With: Example" --no-edit --cleanup=verbatim
check 'portable copied verbose appendix unsigned' 1 "${appendix_message/"$DCO"/}" --no-edit --cleanup=verbatim
check 'portable copied verbose appendix misplaced DCO' 1 "${appendix_message/"$DCO"/}

$DCO" --no-edit --cleanup=verbatim
# Git combines verbose/scissors truncation with strip cleanup. Each transform
# alone would keep one of these sign-offs, but together they remove both.
git config core.commentChar s
check 'portable composed scissors and strip' 1 "fix(hooks): combined cleanup

signed-off-by: Test <test@example.invalid>
s ------------------------ >8 ------------------------

$DCO" --edit --verbose --cleanup=strip
git config --unset core.commentChar
unset TEST_HOOKS
# A comment-looking paragraph is still prose when Git retains comments.
git config core.commentChar ';'
for mode in verbatim whitespace strip; do
  want=1
  [[ "$mode" != strip ]] || want=0
  check "cleanup=$mode retained prose" "$want" "fix(hooks): cleanup

$DCO

; retained prose" "--cleanup=$mode"
  check "cleanup=$mode DCO only" 0 "fix(hooks): cleanup

$DCO" "--cleanup=$mode"
  check "cleanup=$mode edited retained prose" "$want" "fix(hooks): cleanup

$DCO

; retained prose" "--cleanup=$mode" --edit --no-status
  git config commit.cleanup "$mode"
  check "configured cleanup=$mode retained prose" "$want" "fix(hooks): cleanup

$DCO

; retained prose" --no-edit
done
check 'CLI whitespace overrides configured strip' 1 "fix(hooks): cleanup

$DCO

; retained prose" --cleanup whitespace
check 'abbreviated cleanup overrides configured strip' 1 "fix(hooks): cleanup

$DCO

; retained prose" --cl=verbatim
git config commit.cleanup verbatim
check 'last CLI cleanup overrides configured verbatim' 0 "fix(hooks): cleanup

$DCO

; retained prose" --cleanup=whitespace --cleanup strip
git config --unset commit.cleanup
check 'default nonedited cleanup retains comments' 1 "fix(hooks): cleanup

$DCO

; retained prose" --no-edit
# A value that looks like an option must not become a cleanup override.
rc=0
git -c core.hooksPath="$ROOT/.githooks" commit -q --allow-empty --cleanup=whitespace \
  -m 'fix(hooks): option text' --mes --cleanup=strip -m "$DCO" -m '; retained prose' >"$WORK/hook.log" 2>&1 || rc=$?
[[ "$rc" == 1 ]] || { echo 'FAIL hook: message text treated as cleanup option'; fails=$((fails+1)); }
git -c core.hooksPath=/dev/null commit -q --allow-empty --cleanup=whitespace \
  -m 'fix(hooks): option text' -m --cleanup=strip -m "$DCO" -m '; retained prose'
if bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD >"$WORK/ci.log" 2>&1; then
  echo 'FAIL CI: option-text control'; fails=$((fails+1))
fi
touch -- --cleanup=strip
git add -- --cleanup=strip
check 'pathspec is not a cleanup override' 1 "fix(hooks): cleanup

$DCO

; retained prose" --cleanup=whitespace -- --cleanup=strip
# ':' can also be the selected editor; it does not by itself mean --no-edit.
for hooks in "$ROOT/.githooks" /dev/null; do
  if ! GIT_EDITOR=: git -c core.hooksPath="$hooks" commit -q -s --allow-empty --amend >"$WORK/hook.log" 2>&1; then
    echo 'FAIL: default edited commit with colon editor'; fails=$((fails+1))
  elif ! bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD >"$WORK/ci.log" 2>&1; then
    echo 'FAIL CI: default edited commit with colon editor'; fails=$((fails+1))
  fi
done
git config --unset core.commentChar
# Edited messages include Git's comment footer until the hook returns.
for comment_char in '#' ';' auto; do
  for body in 'ordinary prose' '# meaningful comment in the body' $'# hash\n; semicolon'; do
    rc=0
    GIT_EDITOR=true git -c core.hooksPath="$ROOT/.githooks" -c core.commentChar="$comment_char" \
      commit -q -s --allow-empty -m 'fix(hooks): edited' -m "$body" --edit >"$WORK/hook.log" 2>&1 || rc=$?
    if [[ "$rc" != 0 ]]; then
      echo "FAIL hook: edited DCO with commentChar=$comment_char and body=$body (got $rc)"
      cat "$WORK/hook.log"
      fails=$((fails + 1))
    fi
    GIT_EDITOR=true git -c core.hooksPath=/dev/null -c core.commentChar="$comment_char" \
      commit -q -s --allow-empty -m 'fix(hooks): edited' -m "$body" --edit
    if ! bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD >"$WORK/ci.log" 2>&1; then
      echo "FAIL CI: edited DCO with commentChar=$comment_char"
      fails=$((fails + 1))
    fi
  done
done
git config core.commentChar auto
check 'auto scissors uses selected marker' 0 "fix(hooks): scissors

# body forces another marker

$DCO" --edit --cleanup=scissors
check 'auto strip without editor uses selected marker' 0 "fix(hooks): strip

# body forces another marker

$DCO" --no-edit --cleanup=strip
check 'auto quoted prose is not an editor hint' 1 "fix(hooks): misplaced

$DCO

; says ';'"
check 'auto edited missing DCO' 1 'fix(hooks): unsigned

# meaningful comment in the body' edited
check 'auto edited misplaced DCO' 1 "fix(hooks): misplaced

$DCO

More prose after the signoff.
# meaningful comment in the body" edited
check 'auto edited attribution' 1 "fix(hooks): attribution

# meaningful comment in the body

$DCO
Generated-With: Example" edited
git config --unset core.commentChar
# Verbose diffs are discarded by Git even with verbatim cleanup. Each path
# gets its own staged change so the hook and stored-message checks see a diff.
for verbose in cli short config overridden; do
  for mode in default verbatim whitespace scissors; do
    for hooks in "$ROOT/.githooks" /dev/null; do
      args=(--verbose)
      git config commit.verbose false
      case "$verbose" in
        short) args=(-vv) ;;
        config) git config commit.verbose 2; args=() ;;
        overridden) git config commit.verbose true; args=(--no-verbose) ;;
      esac
      printf '%s\n' "$verbose $mode $hooks" >> verbose.txt
      git add verbose.txt
      if ! GIT_EDITOR=true git -c core.hooksPath="$hooks" commit -q -s --edit \
        "${args[@]}" "--cleanup=$mode" -m 'fix(hooks): verbose' >"$WORK/hook.log" 2>&1; then
        echo "FAIL hook: verbose=$verbose cleanup=$mode hooks=$hooks"
        fails=$((fails+1))
      elif ! bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD >"$WORK/ci.log" 2>&1; then
        echo "FAIL CI: verbose=$verbose cleanup=$mode"; fails=$((fails+1))
      fi
    done
  done
done
git config --unset commit.verbose
for char in '#' ';'; do
  git config core.commentChar "$char"
  check 'scissors discards attribution' 0 "fix(hooks): scissors

$DCO

$char ------------------------ >8 ------------------------
Generated-With: Example" --edit --cleanup=scissors
  check 'scissors retains attribution before marker' 1 "fix(hooks): scissors

$DCO
Generated-With: Example

$char ------------------------ >8 ------------------------
Discarded prose" --edit --cleanup=scissors
  check 'verbatim retains attribution after marker' 1 "fix(hooks): scissors

$DCO

$char ------------------------ >8 ------------------------
Generated-With: Example" --cleanup=verbatim
done
git config --unset core.commentChar
git config commit.verbose true
check 'verbose discards attribution below diff marker' 0 "fix(hooks): verbose

$DCO

# ------------------------ >8 ------------------------
Generated-With: Example" --edit --cleanup=strip
check 'no-verbose retains attribution below marker' 1 "fix(hooks): verbose

$DCO

# ------------------------ >8 ------------------------
Generated-With: Example" --edit --cleanup=strip --no-verbose
git config --unset commit.verbose
# Real merges invoke commit-msg directly, with different options from commit.
merge_check() (
  name=$1 want=$2; shift 2
  repo=$(mktemp -d "$WORK/merge.XXXXXX") || exit 2
  git init -q -b main "$repo" || exit 2
  cd "$repo" || exit 2
  git config user.name Test || exit 2
  git config user.email test@example.invalid || exit 2
  git config commit.gpgsign false || exit 2
  git config core.commentChar "${MERGE_COMMENT_CHAR:-#}" || exit 2
  # The bypass control changes branches; both must use identical defaults.
  git config branch.main.mergeOptions "${MERGE_OPTIONS:-}" || exit 2
  git config branch.control.mergeOptions "${MERGE_OPTIONS:-}" || exit 2
  git -c core.hooksPath=/dev/null commit -q -s --allow-empty -m 'chore: seed' || exit 2
  git checkout -q -b topic || exit 2
  git -c core.hooksPath=/dev/null commit -q -s --allow-empty -m 'chore: topic' || exit 2
  git checkout -q main || exit 2
  result=0
  rc=0
  GIT_EDITOR=true git -c core.hooksPath="${TEST_HOOKS:-$ROOT/.githooks}" merge -q --no-ff --signoff "$@" topic >"$WORK/hook.log" 2>&1 || rc=$?
  [[ "$rc" == "$want" ]] || { echo "FAIL hook: merge $name (expected $want, got $rc)"; cat "$WORK/hook.log"; result=1; }
  if [[ "$want" == 1 && "$rc" == 1 ]]; then
    diagnostic='DCO trailer is required'
    [[ "$*" != *Generated-With:* ]] || diagnostic='attribution trailers are refused'
    if ! grep -q "$diagnostic" "$WORK/hook.log"; then
      echo "FAIL hook diagnostic: merge $name"; cat "$WORK/hook.log"; result=1
    fi
  fi
  if [[ "$rc" == 0 ]] && ! bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD >"$WORK/ci.log" 2>&1; then
    echo "FAIL stored hook message: merge $name"; cat "$WORK/ci.log"; result=1
  fi
  if [[ "$rc" != 0 ]]; then git merge --abort || exit 2; else git checkout -q -B control HEAD^ || exit 2; fi
  GIT_EDITOR=true git -c core.hooksPath=/dev/null merge -q --no-ff --signoff "$@" topic >"$WORK/merge.log" 2>&1 || exit 2
  rc=0
  bash "$HERE/check-commit-trailers.sh" --range HEAD^..HEAD >"$WORK/ci.log" 2>&1 || rc=$?
  [[ "$rc" == "$want" ]] || { echo "FAIL CI: merge $name (expected $want, got $rc)"; result=1; }
  exit "$result"
)
for char in '#' ';' auto; do
  for mode in default strip scissors; do
    MERGE_COMMENT_CHAR=$char merge_check "edited $mode marker=$char" 0 --edit "--cleanup=$mode" || fails=$((fails+1))
  done
done
for char in '#' ';' auto; do
  for mode in default strip scissors; do
    TEST_HOOKS="$WORK/no-procfs/.githooks" MERGE_COMMENT_CHAR=$char \
      merge_check "unavailable parent edited $mode marker=$char" 0 --edit "--cleanup=$mode" || fails=$((fails+1))
    TEST_HOOKS="$WORK/no-procfs/.githooks" MERGE_COMMENT_CHAR=$char \
      merge_check "unavailable parent unsigned $mode marker=$char" 1 --edit "--cleanup=$mode" --no-signoff || fails=$((fails+1))
    TEST_HOOKS="$WORK/no-procfs/.githooks" MERGE_COMMENT_CHAR=$char \
      merge_check "unavailable parent attribution $mode marker=$char" 1 --edit "--cleanup=$mode" \
        -m $'Merge topic\n\nGenerated-With: Example' || fails=$((fails+1))
  done
done
merge_check 'abbreviated edit and cleanup' 0 --ed --cle=strip -s ort || fails=$((fails+1))
merge_check 'message value is not an option' 0 --no-edit --cleanup=whitespace -m 'Merge topic' -m --cleanup=strip || fails=$((fails+1))
merge_check 'nonedited' 0 --no-edit || fails=$((fails+1))
MERGE_COMMENT_CHAR=';' MERGE_OPTIONS='--cleanup=verbatim' \
  merge_check 'configured verbatim edited footer' 1 --edit -m 'Merge topic' || fails=$((fails+1))
for options in '--cleanup=strip' '--cle strip' '--cleanup="strip"' '--cleanup=verbatim --cleanup=strip'; do
  MERGE_COMMENT_CHAR=';' MERGE_OPTIONS=$options \
    merge_check "configured strip trailing prose ($options)" 0 --no-edit --no-signoff \
      -m "Merge topic

$DCO

; discarded prose" || fails=$((fails+1))
done
MERGE_COMMENT_CHAR=';' MERGE_OPTIONS='--cleanup=verbatim' \
  merge_check 'CLI strip overrides branch verbatim' 0 --edit --cleanup=strip || fails=$((fails+1))
MERGE_COMMENT_CHAR=';' MERGE_OPTIONS='--cleanup=strip' \
  merge_check 'CLI verbatim overrides branch strip' 1 --edit --cleanup=verbatim || fails=$((fails+1))
MERGE_COMMENT_CHAR=';' MERGE_OPTIONS='--no-edit --cleanup=default' \
  merge_check 'CLI edit overrides branch no-edit' 0 --edit || fails=$((fails+1))
MERGE_COMMENT_CHAR=';' MERGE_OPTIONS='--edit --cleanup=default' \
  merge_check 'CLI no-edit overrides branch edit' 0 --no-edit || fails=$((fails+1))
MERGE_COMMENT_CHAR=';' MERGE_OPTIONS='--cleanup=strip' \
  merge_check 'branch cleanup keeps attribution refusal' 1 --edit \
    -m $'Merge topic\n\nGenerated-With: Example' || fails=$((fails+1))
MERGE_COMMENT_CHAR=';' MERGE_OPTIONS='--cleanup=verbatim --' \
  merge_check 'branch terminator does not hide CLI cleanup' 0 --edit --cleanup=strip || fails=$((fails+1))
MERGE_COMMENT_CHAR=';' MERGE_OPTIONS='--cleanup=strip --edit' \
  merge_check 'branch edit overrides message default' 0 -m 'Merge topic' || fails=$((fails+1))
MERGE_COMMENT_CHAR=';' MERGE_OPTIONS='--cleanup=whitespace --message "--cleanup=strip"' \
  merge_check 'branch message value is not a cleanup option' 1 --no-edit --no-signoff \
    -m "Merge topic

$DCO

; retained prose" || fails=$((fails+1))
# Git option values are data, even when they contain shell syntax and quotes.
MERGE_COMMENT_CHAR=';' \
  MERGE_OPTIONS="--cleanup=strip --into-name \"literal'quote;\$(touch '$WORK/merge-options-executed')\"" \
  merge_check 'branch option shell syntax stays literal' 0 --edit || fails=$((fails+1))
if [[ -e "$WORK/merge-options-executed" ]]; then
  echo 'FAIL: branch options executed shell syntax'; fails=$((fails+1))
fi
merge_check 'retained attribution' 1 --edit -m "Merge topic

Generated-With: Example" || fails=$((fails+1))
check 'missing DCO' 1 'fix(hooks): unsigned'
check 'empty DCO' 1 'fix(hooks): empty

Signed-off-by:'
for trailer in \
  'Co-Authored-By: Example <noreply@anthropic.com>' \
  'co-authored-by: Example <noreply@openai.com>' \
  'Co-Developed-By: Example' \
  'Assisted-By: Example' \
  'Generated-By: Example' \
  'Generated-With: Example' \
  '  gEnErAtEd-wItH : Example' \
  'Co-Authored-By:'; do
  check "$trailer" 1 "fix(hooks): attribution

$DCO
$trailer"
done
check 'merge cannot bypass policy' 1 "Merge branch 'topic'

$DCO
Generated-By: Example"
check 'body signoff is not a DCO trailer' 1 "fix(hooks): misplaced

$DCO

More prose after the signoff."
check 'ordinary prose' 0 "fix(hooks): prose

Reject Co-Authored-By trailers before export.

$DCO"
# Git recognizes all whitespace after a native patch divider, including tabs.
# Scissors inside that patch must not reach the affected Git 2.39 parser.
check 'tab patch divider before scissors' 0 "fix(hooks): patch

$DCO
"$'---\t'"
# ------------------------ >8 ------------------------
diff --git a/file b/file
--- a/file
+++ b/file"
# A successful commitlint must not short-circuit the trailer policy.
mkdir -p node_modules/.bin
printf '#!/bin/sh\nexit 0\n' > node_modules/.bin/commitlint
chmod +x node_modules/.bin/commitlint
check 'commitlint cannot bypass policy' 1 "fix(hooks): attribution

$DCO
Generated-With: Example"
check 'commitlint with DCO' 0 "fix(hooks): valid

$DCO"
# Repository and environment aliases must not create or hide a DCO trailer.
git config trailer.acked-by.key Signed-off-by
git config trailer.separators '='
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=trailer.reviewed-by.key
export GIT_CONFIG_VALUE_0=Signed-off-by
check 'canonical DCO with custom Git config' 0 "fix(hooks): config

$DCO"
git config --unset trailer.separators
check 'repository alias is not DCO' 1 'fix(hooks): config

Acked-by: Example <example@example.invalid>'
check 'environment alias is not DCO' 1 'fix(hooks): config

Reviewed-by: Example <example@example.invalid>'
unset GIT_CONFIG_COUNT GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0
# Only the oldest commit in this range offends; newer commits are DCO-only controls.
git branch older-offender-base
git -c core.hooksPath=/dev/null commit -q --allow-empty -m "fix(hooks): oldest attribution

$DCO
Generated-With: Example"
offender="$(git rev-parse HEAD)"
for subject in newer newest; do
  git -c core.hooksPath=/dev/null commit -q --allow-empty -s -m "fix(hooks): $subject"
done
bash "$HERE/check-commit-trailers.sh" --range HEAD~2..HEAD
if bash "$HERE/check-commit-trailers.sh" --range older-offender-base..HEAD >"$WORK/range.log" 2>&1; then
  echo 'FAIL CI: accepted oldest offending commit'; fails=$((fails + 1))
elif ! grep -q "refused commit $offender" "$WORK/range.log"; then
  echo 'FAIL CI: did not identify oldest offending commit'; fails=$((fails + 1))
fi
# Replacement content must not hide a stored message or its ancestry, even
# when repository config explicitly re-enables replacements over Git's flag.
replacement="$(printf 'fix(hooks): replacement control\n\n%s\n' "$DCO" |
  git commit-tree HEAD^{tree} -p older-offender-base)"
bash "$HERE/check-commit-trailers.sh" --range "older-offender-base..$replacement"
for target in "$offender" HEAD; do
  git replace "$target" "$replacement"
  for enabled in false true; do
    git config core.useReplaceRefs "$enabled"
    rc=0
    bash "$HERE/check-commit-trailers.sh" --range older-offender-base..HEAD >"$WORK/range.log" 2>&1 || rc=$?
    if [[ "$rc" != 1 ]] ||
        ! grep -q 'attribution trailers are refused' "$WORK/range.log" ||
        ! grep -q "refused commit $offender" "$WORK/range.log"; then
      echo "FAIL CI: replacement target=$target useReplaceRefs=$enabled hid stored attribution"
      cat "$WORK/range.log"; fails=$((fails + 1))
    fi
  done
  git replace -d "$target" >/dev/null
done
git config --unset core.useReplaceRefs
# CI must fail closed on an invalid ref.
if bash "$HERE/check-commit-trailers.sh" --range missing-base..HEAD >"$WORK/range.log" 2>&1; then
  echo 'FAIL CI: accepted missing-base..HEAD'; fails=$((fails + 1))
fi
if bash "$HERE/check-commit-trailers.sh" "$WORK/missing-message" >"$WORK/missing.log" 2>&1; then
  echo 'FAIL: accepted missing message file'; fails=$((fails + 1))
fi
# Missing invocation details must not silently become configured cleanup.
printf 'fix(hooks): procfs\n\n%s\n' "$DCO" > "$WORK/msg"
if ! bash "$HERE/check-commit-trailers.sh" --commit-msg 999999999 "$WORK/msg" >"$WORK/missing.log" 2>&1; then
  echo 'FAIL: refused unambiguous message without procfs'; fails=$((fails+1))
fi
echo "commit-trailers: $fails failures"
[[ "$fails" == 0 ]]
