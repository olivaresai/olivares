#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Test format-ratchet.sh with injected listings and disposable source trees.
# Production-path cases use the repository's real formatter.
set -uo pipefail

# Create the unavailable-measurement marker before any fixture can write it.
RATCHET_SELFTEST_UNAVAILABLE="${TMPDIR:-/tmp}/.ratchet-selftest-unavailable.$$"
rm -f "$RATCHET_SELFTEST_UNAVAILABLE"

# Clear inherited Git variables so temporary repositories cannot resolve against the
# source tree.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

HERE=$(cd "$(dirname "$0")" && pwd)

# Require a temporary directory that executes the fake formatter. Probe execution;
# permission bits alone cannot detect noexec. If none works, exit 2 rather than
# letting formatter resolution fall back to the real tool.
_temp_execs() { # directory: return 0 if a created script executes
	[ -d "$1" ] && [ -w "$1" ] || return 1
	local _p="$1/.ratchet-selftest-exec.$$"
	printf '#!/usr/bin/env bash\nexit 7\n' > "$_p" 2>/dev/null || return 1
	chmod +x "$_p" 2>/dev/null || { rm -f "$_p"; return 1; }
	"$_p" >/dev/null 2>&1
	local _rc=$?
	rm -f "$_p"
	[ "$_rc" -eq 7 ]
}
_choose_temp_base() {
	local _c
	for _c in "${TMPDIR:-}" "${HOME:-}" "$(cd "$(dirname "$0")/.." && pwd)/.selftest-tmp"; do
		[ -n "$_c" ] || continue
		mkdir -p "$_c" 2>/dev/null || continue
		if _temp_execs "$_c"; then printf '%s\n' "$_c"; return 0; fi
	done
	return 1
}
_TMP_BASE=$(_choose_temp_base) || {
	echo "format-ratchet self-test: UNAVAILABLE: no temporary directory permits script execution." >&2
	echo "   Checked TMPDIR='${TMPDIR:-}', HOME='${HOME:-}', and <repo>/.selftest-tmp." >&2
	echo "   The self-test needs executable fixtures to use its fake formatter." >&2
	echo "   Use a temporary directory mounted without 'noexec'." >&2
	exit 2
}
[ "$_TMP_BASE" = "${TMPDIR:-}" ] || echo "format-ratchet self-test: TMPDIR does not permit execution; fixtures use ${_TMP_BASE}" >&2
export TMPDIR="$_TMP_BASE"

SUT="${HERE}/format-ratchet.sh"
ok=0; failures=0

# BAD is unformatted; GOOD is conforming. Keep both controls so rejecting all files
# cannot pass.
BAD='const    x   =  {a:1,b:2}
export default x'
GOOD='const x = { a: 1, b: 2 };
export default x;'

# Report exit 2 as an unavailable measurement, separately from added formatting debt.
check_result() { # expected status, actual status, description, output, optional required fragment
  local expected="$1" got="$2" desc="$3" out="$4" frag="${5:-}" frag_ok=0
  if [ -n "$frag" ]; then
    case "$out" in *"$frag"*) frag_ok=0 ;; *) frag_ok=1 ;; esac
  fi
  if [ "$got" = "$expected" ] && [ "$frag_ok" -eq 0 ]; then
    ok=$((ok+1)); printf '  ok    %-58s rc=%s\n' "$desc" "$got"
  elif [ "$expected" != "2" ] && [ "$got" = "2" ]; then
    failures=$((failures+1))
    printf '  FAIL %-58s rc=2; UNAVAILABLE: %s\n' "$desc" \
      "$(printf '%s' "$out" | grep -iE 'UNAVAILABLE|cannot find the formatter' | head -1 | cut -c1-58)"
  else
    failures=$((failures+1))
    if [ "$frag_ok" -ne 0 ]; then
      printf '  FAIL %-58s expected=%s actual=%s; missing "%s"\n' "$desc" "$expected" "$got" "$frag"
      # Include captured subject output when a required diagnostic fragment is missing.
      printf '%s\n' "$out" | sed -n '1,6p' | sed 's/^/          SUT| /'
    else
      printf '  FAIL %-58s expected=%s actual=%s\n' "$desc" "$expected" "$got"
    fi
    printf '%s\n' "$out" | tail -3 | sed 's/^/        /'
  fi
}

LAST_OUTPUT=""
run_case() { # expected status, description, baseline, listing output, optional listing status
  local expected="$1" desc="$2" baseline_text="$3" output="$4" rc_list="${5:-1}"
  local t basef got_out got
  t=$(mktemp -d)
  if [ "$baseline_text" = "__MISSING_FILE__" ]; then
    basef="$t/missing"                       # Deliberately missing baseline.
  else
    basef="$t/base"; printf '%s\n' "$baseline_text" > "$basef"
  fi
  printf '%s\n' "$output" > "$t/output"
  # Without arguments, return the fixture listing. With a filename, verify that file
  # as conforming so the subject can check an apparent improvement.
  {
    printf '#!/usr/bin/env bash\n'
    printf '[ $# -gt 0 ] && exit 0\n'
    printf "cat '%s'\n" "$t/output"
    printf 'exit %s\n' "$rc_list"
  } > "$t/l.sh"
  got_out=$(FORMAT_RATCHET_BASELINE="$basef" \
            FORMAT_RATCHET_CMD="bash '$t/l.sh'" \
            bash "$SUT" 2>&1)
  got=$?
  LAST_OUTPUT="$got_out"
  if [ "$got" = "$expected" ]; then
    ok=$((ok+1)); printf '  ok    %-58s rc=%s\n' "$desc" "$got"
  else
    failures=$((failures+1)); printf '  FAIL %-58s expected=%s actual=%s\n' "$desc" "$expected" "$got"
    printf '%s\n' "$got_out" | tail -4 | sed 's/^/        /'
  fi
  rm -rf "$t"
}

LISTING_OUTPUT='[warn] src/features/a.tsx
[warn] src/features/b.tsx
[warn] Code style issues found in 2 files. Run Prettier with --write to fix.'

echo "Passing cases: formatting debt does not increase"
run_case 0 'offenders exactly match the baseline' \
  'src/features/a.tsx
src/features/b.tsx' "$LISTING_OUTPUT"
run_case 0 'one fixed file passes and allows a smaller baseline' \
  'src/features/a.tsx
src/features/b.tsx
src/features/c.tsx' "$LISTING_OUTPUT"
case "$LAST_OUTPUT" in
  *'can shrink by 1'*) ok=$((ok+1)); printf '  ok    %-58s\n' 'reports the verified improvement' ;;
  *) failures=$((failures+1)); printf '  FAIL %-58s\n' 'baseline reduction was not reported' ;;
esac
run_case 0 'a clean tree passes with an empty baseline' '' '' 0

echo "Failing cases: name files that add formatting debt"
run_case 1 'a new offender fails the ratchet' \
  'src/features/a.tsx' "$LISTING_OUTPUT"
case "$LAST_OUTPUT" in
  *'src/features/b.tsx'*) ok=$((ok+1)); printf '  ok    %-58s\n' 'reports the new filename' ;;
  *) failures=$((failures+1)); printf '  FAIL %-58s\n' 'new offender was not named' ;;
esac
# Replacing one offender with another must fail even when the count stays equal.
run_case 1 'replacing an offender fails even when the count stays equal' \
  'src/features/a.tsx
src/features/z.tsx' "$LISTING_OUTPUT"

echo "Unavailable measurements must not pass"
run_case 2 'a missing baseline prevents measurement' '__MISSING_FILE__' "$LISTING_OUTPUT"
run_case 2 'formatter cannot start (rc=127)' \
  'src/features/a.tsx' 'npx: command not found' 127
run_case 2 'rc=1 without filenames is unreadable' \
  'src/features/a.tsx' 'Something went wrong' 1
# The warning summary is not an offending file.
run_case 0 'the summary is not counted as an offender' \
  'src/features/a.tsx
src/features/b.tsx' "$LISTING_OUTPUT"
case "$LAST_OUTPUT" in
  *'2 offender(s)'*) ok=$((ok+1)); printf '  ok    %-58s\n' 'the listing counts two files' ;;
  *) failures=$((failures+1)); printf '  FAIL %-58s\n' "the listing counted the summary: $LAST_OUTPUT" ;;
esac

echo "Hidden debt: leaving the listing does not prove a fix"
# Removing a file from the listing must not count as formatting it. The fixture
# answers both the listing request and the per-file verification request.
hidden_fixture() { # status for b.tsx: print fixture directory
  local rc_b="$1" t; t=$(mktemp -d)
  {
    printf '#!/usr/bin/env bash\n'
    printf 'if [ $# -eq 0 ]; then\n'
    printf "  printf '[warn] a.tsx\\n[warn] Code style issues found in 1 files.\\n'\n"
    printf '  exit 1\n'
    printf 'fi\n'
    printf 'case "$1" in b.tsx) exit %s ;; *) exit 0 ;; esac\n' "$rc_b"
  } > "$t/l.sh"
  printf 'a.tsx\nb.tsx\n' > "$t/base"
  printf '%s' "$t"
}
t1=$(hidden_fixture 1)
out=$(FORMAT_RATCHET_BASELINE="$t1/base" FORMAT_RATCHET_CMD="bash $t1/l.sh" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && case "$out" in *"without being formatted"*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=1, offender named\n' 'hiding formatting debt fails'
else
  failures=$((failures+1)); printf '  FAIL %-58s rc=%s\n' 'hidden formatting debt should fail' "$rc"
fi
rm -rf "$t1"
# A verified improvement must still allow the baseline to shrink.
t2=$(hidden_fixture 0)
out=$(FORMAT_RATCHET_BASELINE="$t2/base" FORMAT_RATCHET_CMD="bash $t2/l.sh" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 0 ] && case "$out" in *"can shrink by 1"*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=0\n' 'a verified fix allows the baseline to shrink'
else
  failures=$((failures+1)); printf '  FAIL %-58s rc=%s\n' 'a verified fix should pass' "$rc"
fi
rm -rf "$t2"

echo "Summary-like filenames stay in the offender set"
# Existing files named like formatter summaries must remain in the offender set.
# Use the real formatter path to exercise the existence filter; missing tooling is a
# failure.
tr=$(mktemp -d)
for f in 'ordinary.ts' 'Code style issues hidden.ts' 'Forgot to run hidden.ts' '[diagnostic] hidden.ts'; do
  printf 'const    x   =  {a:1,b:2}\nexport default x\n' > "$tr/$f"
done
# Resolve the formatter from the subject repository, independently of the disposable
# tree.
printf 'ordinary.ts\n' > "$tr/base"
out=$(FORMAT_RATCHET_ROOT="$tr" FORMAT_RATCHET_BASELINE="$tr/base" FORMAT_RATCHET_GLOB='*.ts' \
      bash "$SUT" 2>&1); rc=$?
# Keep tool unavailability distinct from a filename-filtering defect.
if [ "$rc" -eq 1 ] && case "$out" in *'new 3'*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=1, new 3\n' 'three summary-like filenames remain in the listing'
elif [ "$rc" -eq 2 ]; then
  failures=$((failures+1))
  printf '  FAIL %-58s rc=2; %s\n' 'UNAVAILABLE: the subject did not produce a listing' \
    "$(printf '%s' "$out" | grep -iE 'UNAVAILABLE|formatter' | head -1 | cut -c1-70)"
  # Print the whole subject output when the formatter could not be measured.
  printf '        Subject output:\n'
  printf '%s\n' "$out" | sed 's/^/        /'
else
  failures=$((failures+1)); printf '  FAIL %-58s rc=%s; %s\n' 'summary-like filenames escaped the check' "$rc" \
    "$(printf '%s' "$out" | grep -o '[0-9]* offender[^;]*' | head -1)"
fi
rm -rf "$tr"

echo "ANSI-colored output keeps the offender set"
# Inject decorated listing bytes so the ANSI filter is exercised even when Prettier
# receives NO_COLOR=1. The uncolored case also rejects a filter that drops valid lines.
d=$(mktemp -d)
printf 'a.ts\n' > "$d/base"
_E=$(printf '\033')
_cmd="printf '[${_E}[33mwarn${_E}[39m] a.ts\n[${_E}[33mwarn${_E}[39m] b.ts\n[${_E}[33mwarn${_E}[39m] c.ts\n[${_E}[33mwarn${_E}[39m] Code style issues found in 3 files.\n'; exit 1"
out=$(FORMAT_RATCHET_CMD="$_cmd" FORMAT_RATCHET_BASELINE="$d/base" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && case "$out" in *'new 2'*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=1, new 2\n' 'the listing survives ANSI-colored output'
elif [ "$rc" -eq 2 ]; then
  failures=$((failures+1))
  printf '  FAIL %-58s rc=2; the ANSI filter did not preserve records\n' \
    'ANSI-colored output emptied the listing'
  printf '%s\n' "$out" | sed 's/^/        /' >&2
else
  failures=$((failures+1)); printf '  FAIL %-58s rc=%s; %s\n' 'unexpected result for ANSI-colored output' "$rc" \
    "$(printf '%s' "$out" | grep -o '[0-9]* offender[^;]*' | head -1)"
fi
rm -rf "$d"

echo "Trusted inputs: reject overrides and baseline growth"
# Production --gate must reject injected environment overrides.
out=$(FORMAT_RATCHET_CMD="printf '[warn] x\n'; exit 1" bash "$SUT" --gate 2>&1); rc=$?
if [ "$rc" -eq 2 ] && case "$out" in *'rejects all overrides'*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=2\n' '--gate rejects environment overrides'
else
  failures=$((failures+1)); printf '  FAIL %-58s rc=%s\n' '--gate should reject the override' "$rc"
fi

# The baseline may shrink but must not grow relative to its committed reference.
d=$(mktemp -d)
(
  # Isolate Git configuration and templates when constructing the committed fixture.
  cd "$d" && \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TEMPLATE_DIR= \
    git init -q -b main . && printf 'a.ts\n' > base.txt
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
    git -C "$d" add -A >/dev/null 2>&1
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
  GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t \
    git -C "$d" commit -qm base >/dev/null 2>&1
) >/dev/null 2>&1
# Require the fixture to create HEAD; otherwise baseline comparison is unmeasured
# and the unchanged-baseline case could pass without exercising the rule.
if ! git -C "$d" rev-parse --verify -q HEAD >/dev/null 2>&1; then
  printf 'test-format-ratchet: UNAVAILABLE: baseline fixture did not create HEAD in %s.\n' "$d" >&2
  : > "${RATCHET_SELFTEST_UNAVAILABLE:?}"
fi
printf 'a.ts\nb.ts\n' > "$d/base.txt"          # Expand only the working baseline.
out=$(cd "$d" && FORMAT_RATCHET_BASELINE=base.txt FORMAT_RATCHET_BASE_REF=HEAD \
      FORMAT_RATCHET_CMD="printf '[warn] a.ts\n'; exit 1" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 1 ] && case "$out" in *'baseline grew'*) true ;; *) false ;; esac; then
  ok=$((ok+1)); printf '  ok    %-58s rc=1, path named\n' 'baseline growth fails'
else
  failures=$((failures+1)); printf '  FAIL %-58s rc=%s\n' 'baseline growth should fail' "$rc"
fi
# An unchanged baseline must pass; rejecting all edits cannot satisfy the positive case.
printf 'a.ts\n' > "$d/base.txt"                 # Match the committed baseline.
out=$(cd "$d" && FORMAT_RATCHET_BASELINE=base.txt FORMAT_RATCHET_BASE_REF=HEAD \
      FORMAT_RATCHET_CMD="printf '[warn] a.ts\n'; exit 1" bash "$SUT" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then
  ok=$((ok+1)); printf '  ok    %-58s rc=0\n' 'an unchanged baseline passes'
else
  failures=$((failures+1)); printf '  FAIL %-58s rc=%s\n' 'an unchanged baseline should pass' "$rc"
fi
rm -rf "$d"

echo "Scope matches format:check"
# Compare default scope with package.json format:check. Resolve the package path
# from the script so the caller's working directory cannot change the result.
_TFR_ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)"
PKG=${FORMAT_RATCHET_PKG:-$_TFR_ROOT/web/package.json}
if [ -r "$PKG" ]; then
  declared=$(sed -n 's/.*"format:check"[[:space:]]*:[[:space:]]*"prettier --check \(.*\)".*/\1/p' "$PKG" | head -1)
  script_scope=$(sed -n 's/^GLOB=\${FORMAT_RATCHET_GLOB:-\(.*\)}$/\1/p' "$SUT" | head -1 | tr -d "'")
  if [ -z "$declared" ]; then
    failures=$((failures+1)); printf '  FAIL %-58s\n' 'cannot read format:check from package.json'
  elif [ "$declared" = "$script_scope" ]; then
    ok=$((ok+1)); printf '  ok    %-58s %s\n' 'scope matches format:check' "$script_scope"
  else
    failures=$((failures+1))
    printf '  FAIL %-58s package.json=%s script=%s\n' \
      'ratchet scope differs from format:check' "$declared" "$script_scope"
  fi
else
  failures=$((failures+1)); printf '  FAIL %-58s\n' "cannot read ${PKG}; scope could not be checked"
fi

echo "Default execution checks files outside src/"
# Exercise default scope with an unformatted file outside a present, conforming src/
# tree.
# Do not inject a listing or glob; a declaration alone cannot prove the executed scope.
tA=$(mktemp -d); mkdir -p "$tA/root/src"
printf '%s\n' "$BAD"  > "$tA/root/top.ts"
printf '%s\n' "$GOOD" > "$tA/root/src/clean.ts"
: > "$tA/base"                                   # Baseline outside the measured tree.
out=$(FORMAT_RATCHET_ROOT="$tA/root" FORMAT_RATCHET_BASELINE="$tA/base" bash "$SUT" 2>&1); rc=$?
check_result 1 "$rc" 'an offender outside src/ appears in the listing' "$out" 'top.ts'
rm -rf "$tA"

echo "Verify hidden debt with the real formatter"
# Use the real formatter to verify an unformatted file hidden by .prettierignore.
# The injected-listing cases cannot cover this production branch.
tB=$(mktemp -d); mkdir -p "$tB/root"
printf '%s\n' "$BAD" > "$tB/root/keep.ts"        # Keep a visible offender in the listing.
printf '%s\n' "$BAD" > "$tB/root/hidden.ts"      # Unformatted and excluded.
printf 'hidden.ts\n' > "$tB/root/.prettierignore"
printf 'hidden.ts\nkeep.ts\n' > "$tB/base"
out=$(FORMAT_RATCHET_ROOT="$tB/root" FORMAT_RATCHET_BASELINE="$tB/base" bash "$SUT" 2>&1); rc=$?
check_result 1 "$rc" 'an excluded unformatted file fails' "$out" 'without being formatted'
rm -rf "$tB"

# A genuinely conforming excluded file must pass and allow the baseline to shrink.
tC=$(mktemp -d); mkdir -p "$tC/root"
printf '%s\n' "$BAD"  > "$tC/root/keep.ts"
printf '%s\n' "$GOOD" > "$tC/root/ok.ts"         # Excluded and conforming.
printf 'ok.ts\n' > "$tC/root/.prettierignore"
printf 'keep.ts\nok.ts\n' > "$tC/base"
out=$(FORMAT_RATCHET_ROOT="$tC/root" FORMAT_RATCHET_BASELINE="$tC/base" bash "$SUT" 2>&1); rc=$?
check_result 0 "$rc" 'a conforming excluded file allows a smaller baseline' "$out" 'can shrink by 1'
rm -rf "$tC"

echo "Reject unclassified warning records"
# A newline in a real filename produces split warning records. Reject unclassified
# records instead of silently discarding the hidden debt.
tD=$(mktemp -d); mkdir -p "$tD/root"
printf '%s\n' "$BAD" > "$tD/root/old.ts"
printf 'old.ts\n' > "$tD/base"
# The same tree without the split filename must pass before testing the refusal.
out=$(FORMAT_RATCHET_ROOT="$tD/root" FORMAT_RATCHET_BASELINE="$tD/base" bash "$SUT" 2>&1); rc=$?
check_result 0 "$rc" 'the same tree passes without the split filename' "$out" 'formatting debt did not increase'
printf '%s\n' "$BAD" > "$tD/root/new"$'\n'"line.ts"
out=$(FORMAT_RATCHET_ROOT="$tD/root" FORMAT_RATCHET_BASELINE="$tD/base" bash "$SUT" 2>&1); rc=$?
check_result 2 "$rc" 'a newline filename prevents measurement' "$out" 'UNAVAILABLE'
rm -rf "$tD"

echo "Reject files removed during measurement"
# Reject a listed file that disappears before existence filtering. Copy the current
# subject so formatter resolution can reach the fake npx without changing the source
# tree.
_fake_npx() { # executable path, delete new.ts before filtering: yes or no
  cat > "$1" <<'FAKE_FORMATTER'
#!/usr/bin/env bash
for a in "$@"; do [ "$a" = "--version" ] && { echo 3.9.6; exit 0; }; done
printf 'Checking formatting...\n[warn] old.ts\n[warn] new.ts\n[warn] Code style issues found in 2 files. Run Prettier with --write to fix.\n'
FAKE_FORMATTER
  [ "$2" = "yes" ] && printf 'rm -f new.ts\n' >> "$1"
  printf 'exit 1\n' >> "$1"
  chmod +x "$1"
  # Check that the fake formatter executes before judging the subject; noexec would
  # otherwise fall through to the real formatter and measure a different case.
  local _v
  _v=$("$1" --version 2>/dev/null)
  if [ "$_v" != "3.9.6" ]; then
    echo "format-ratchet self-test: UNAVAILABLE: fake formatter $1 cannot execute" >&2
    echo "   (--version returned '${_v}', rc=$?); executable fake formatter required." >&2
    exit 2
  fi
}
tE=$(mktemp -d); mkdir -p "$tE/scripts" "$tE/bin" "$tE/root"
cp "$SUT" "$tE/scripts/format-ratchet.sh"
printf '%s\n' "$BAD" > "$tE/root/old.ts"
printf '%s\n' "$BAD" > "$tE/root/new.ts"
# The same fixture without deletion must pass so an unrelated formatter failure cannot
# satisfy the case.
_fake_npx "$tE/bin/npx" no
printf 'new.ts\nold.ts\n' > "$tE/base"
out=$(PATH="$tE/bin:$PATH" FORMAT_RATCHET_ROOT="$tE/root" FORMAT_RATCHET_BASELINE="$tE/base" \
      bash "$tE/scripts/format-ratchet.sh" 2>&1); rc=$?
check_result 0 "$rc" 'the same harness passes without deletion' "$out" 'formatting debt did not increase'
_fake_npx "$tE/bin/npx" yes
printf 'old.ts\n' > "$tE/base"
out=$(PATH="$tE/bin:$PATH" FORMAT_RATCHET_ROOT="$tE/root" FORMAT_RATCHET_BASELINE="$tE/base" \
      bash "$tE/scripts/format-ratchet.sh" 2>&1); rc=$?
check_result 2 "$rc" 'a file removed during measurement is reported' "$out" 'new.ts'
rm -rf "$tE"

echo "Expanded exclusions use the committed-exclusion measurement"
# Commit an empty .prettierignore, then expand it only in the working tree.
# Use a real Git reference and formatter to cover conforming files, hidden debt and
# unclassified records during comparison with the committed exclusions.
ignore_fixture() { # clean, hidden or split: print disposable repository
  local fixture_case="$1" t; t=$(mktemp -d); mkdir -p "$t/root/hid"
  printf '%s\n' "$BAD" > "$t/root/keep.ts"        # Visible offender in the baseline.
  printf 'keep.ts\n' > "$t/base.txt"
  : > "$t/root/.prettierignore"
  case "$fixture_case" in
    clean)  printf '%s\n' "$GOOD" > "$t/root/hid/x.ts" ;;
    hidden)    printf '%s\n' "$BAD"  > "$t/root/hid/x.ts" ;;
    split) printf '%s\n' "$BAD"  > "$t/root/hid/new"$'\n'"line.ts" ;;
  esac
  (
    # Keep fixture Git operations independent of host configuration, hooks and
    # templates.
    cd "$t" && \
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
    GIT_TEMPLATE_DIR= \
      git init -q -b main .
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
      git add -A >/dev/null 2>&1
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
    GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t \
      git commit -qm base >/dev/null 2>&1
  ) >/dev/null 2>&1
  # Require both HEAD and the committed .prettierignore before judging exclusion growth.
  # Read ls-tree output without a pipeline to an early-exiting consumer under pipefail.
  _ignore_in_head="$(git -C "$t" ls-tree HEAD -- root/.prettierignore 2>/dev/null)"
  if git -C "$t" rev-parse --verify -q HEAD >/dev/null 2>&1 && [ -z "$_ignore_in_head" ]; then
    printf 'test-format-ratchet: UNAVAILABLE: HEAD does not contain root/.prettierignore in %s.\n' "$t" >&2
    printf '                     The fixture cannot measure exclusion growth without the committed file.\n' >&2
    : > "${RATCHET_SELFTEST_UNAVAILABLE:?}"
  fi
  if ! git -C "$t" rev-parse --verify -q HEAD >/dev/null 2>&1; then
    printf 'test-format-ratchet: UNAVAILABLE: fixture did not create HEAD in %s.\n' "$t" >&2
    printf '                     git init/commit failed; this case is unavailable.\n' >&2
    rm -rf "$t"
    # A command-substitution exit cannot end the parent. Persist the unavailable
    # measurement marker so the final verdict returns 2 rather than a substantive
    # failure.
    : > "${RATCHET_SELFTEST_UNAVAILABLE:?}"
    printf ''
    return 2
  fi
  printf 'hid/\n' > "$t/root/.prettierignore"     # Expand exclusions only in the working tree.
  printf '%s' "$t"
}
run_ignore() { # fixture directory: print subject output and retain its status
  ( cd "$1" && FORMAT_RATCHET_ROOT=root FORMAT_RATCHET_BASELINE=base.txt \
      FORMAT_RATCHET_BASE_REF=HEAD bash "$SUT" 2>&1 )
}
# An expanded exclusion over conforming files must pass; rejecting every exclusion edit
# is insufficient.
tF=$(ignore_fixture clean); out=$(run_ignore "$tF"); rc=$?
check_result 0 "$rc" 'expanded exclusions over conforming files pass' "$out" 'formatting debt did not increase'
rm -rf "$tF"
tG=$(ignore_fixture hidden); out=$(run_ignore "$tG"); rc=$?
check_result 1 "$rc" 'a new exclusion hiding debt fails' "$out" 'hid/x.ts'
rm -rf "$tG"
# Require the committed-exclusion diagnostic for split records hidden by the new
# exclusion, so the ordinary-tree check cannot satisfy this separate case.
tH=$(ignore_fixture split); out=$(run_ignore "$tH"); rc=$?
check_result 2 "$rc" 'committed-exclusion measurement rejects unclassified records' "$out" 'with .prettierignore from HEAD'
rm -rf "$tH"

printf '\nformat-ratchet self-test: %d passed, %d failed\n' "$ok" "$failures"
if [ -e "$RATCHET_SELFTEST_UNAVAILABLE" ]; then
  rm -f "$RATCHET_SELFTEST_UNAVAILABLE"
  printf 'test-format-ratchet: UNAVAILABLE (2): at least one fixture could not be created.\n' >&2
  printf '                     Its cases cannot verify the subject.\n' >&2
  exit 2
fi
[ "$failures" -eq 0 ]
