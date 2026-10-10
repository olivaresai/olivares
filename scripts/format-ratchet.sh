#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Compare the current formatting offenders with a committed list, not just a count.
# Exit 0 for unchanged or reduced debt, 1 for new or hidden debt, and 2 when the
# measurement is unavailable. Always print the measured counts.
set -uo pipefail

# Production --gate rejects all test overrides. Self-tests call without --gate.
GATE=0
for _a in "$@"; do [ "$_a" = "--gate" ] && GATE=1; done
if [ "$GATE" -eq 1 ]; then
  _overrides=""
  for _v in FORMAT_RATCHET_CMD FORMAT_RATCHET_ROOT FORMAT_RATCHET_GLOB FORMAT_RATCHET_BASELINE FORMAT_RATCHET_BASE_REF; do
    eval "[ -n \"\${${_v}:-}\" ]" && _overrides="${_overrides} ${_v}"
  done
  if [ -n "$_overrides" ]; then
    echo "format-ratchet: UNAVAILABLE: --gate rejects all overrides;" >&2
    echo "format-ratchet:    the environment sets:${_overrides}." >&2
    echo "format-ratchet:    Clear these variables before measuring the tree." >&2
    exit 2
  fi
fi

ROOT=${FORMAT_RATCHET_ROOT:-web}
BASELINE=${FORMAT_RATCHET_BASELINE:-web/format-ratchet-baseline.txt}
# Match package.json format:check: measure all of web/, including files outside src/.
GLOB=${FORMAT_RATCHET_GLOB:-.}

# Resolve the formatter once from this repository, independently of the measured
# tree. Fall back to npx --no-install; missing tooling returns 2.
_REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
if [ -x "$_REPO_ROOT/web/node_modules/.bin/prettier" ]; then
  PRETTIER="$_REPO_ROOT/web/node_modules/.bin/prettier"
elif npx --no-install prettier --version >/dev/null 2>&1; then
  PRETTIER="npx --no-install prettier"
else
  PRETTIER=""
fi

# Tests can inject a listing. Report the override because it is not a tree measurement.
LISTER=${FORMAT_RATCHET_CMD:-}

if [ -n "$LISTER" ]; then
  echo "format-ratchet: warning: injected listing (\$FORMAT_RATCHET_CMD); tree not measured"
fi

if [ ! -r "$BASELINE" ]; then
  echo "format-ratchet: UNAVAILABLE: cannot read baseline ${BASELINE}" >&2
  echo "format-ratchet: A missing baseline prevents measurement." >&2
  exit 2
fi

# Collect the formatter listing.
if [ -n "$LISTER" ]; then
  output=$(eval "$LISTER" 2>&1); rc=$?
else
  if [ -z "$LISTER" ] && [ -z "$PRETTIER" ]; then
    echo "format-ratchet: UNAVAILABLE: cannot find the formatter:" >&2
    echo "  ${_REPO_ROOT}/web/node_modules/.bin/prettier or npx --no-install prettier." >&2
    exit 2
  fi
  if [ ! -d "$ROOT" ]; then
    echo "format-ratchet: UNAVAILABLE: directory does not exist: ${ROOT}" >&2; exit 2
  fi
  output=$(cd "$ROOT" && NO_COLOR=1 timeout 900 $PRETTIER --check "$GLOB" 2>&1); rc=$?
fi

# Strip ANSI escapes from real and injected output before matching warning records.
# NO_COLOR only covers the real formatter branch; this filter covers both branches.
# Construct ESC with printf so the sed pattern contains the actual byte.
_ESC=$(printf '\033')
output=$(printf '%s\n' "$output" | sed "s/${_ESC}\[[0-9;]*[a-zA-Z]//g")

# Formatter status: 0 means conforming, 1 means offenders; other values are unavailable.
if [ "$rc" -ne 0 ] && [ "$rc" -ne 1 ]; then
  echo "format-ratchet: UNAVAILABLE: formatter exited with status $rc" >&2
  printf '%s\n' "$output" | tail -5 >&2
  exit 2
fi

# A real offender must be a file, even if its name resembles a formatter summary.
# Summary records are accepted separately. Other records return 2: newline filenames
# or files removed during measurement must not silently disappear from the census.
# The production census uses existence only. The injected branch uses text because
# it has no source tree. The two independent acceptances below do not depend on order.
_unclassified_records() { # [warn] payloads, one per line: print unclassified records
  local _l
  while IFS= read -r _l; do
    [ -n "$_l" ] || continue
    [ -f "${ROOT}/${_l}" ] && continue
    case "$_l" in 'Code style issues'*|'Forgot to run'*) continue ;; esac
    printf '%s\n' "$_l"
  done <<EOF_UNCLASSIFIED
$1
EOF_UNCLASSIFIED
}

# Print unclassified records and exit 2.
_fail_unclassified_records() { # unclassified records, measurement description
  echo "format-ratchet: UNAVAILABLE: formatter records could not be classified:" >&2
  echo "format-ratchet:    not files under ${ROOT} or summary records; measurement: ${2}" >&2
  printf '%s\n' "$1" | sed 's/^/  /' >&2
  echo "format-ratchet:    Possible causes include a newline in a filename or a changing source tree." >&2
  echo "format-ratchet:    Prettier splits a newline filename into separate records." >&2
  echo "format-ratchet:    A file can also disappear before the existence check." >&2
  echo "format-ratchet:    Rename the file or measure a stable tree." >&2
  exit 2
}

raw_records=$(printf '%s\n' "$output" | sed -n 's/^\[warn\] \(.*\)$/\1/p')
if [ -n "$LISTER" ]; then
  # An injected listing has no tree, so this branch uses the summary text filter.
  current=$(printf '%s\n' "$raw_records" | grep -vE '^Code style issues|^Forgot to run' | LC_ALL=C sort -u)
else
  _unclassified=$(_unclassified_records "$raw_records")
  [ -n "$_unclassified" ] && _fail_unclassified_records "$_unclassified" "the tree"
  current=$(while IFS= read -r _l; do
    # A directory named like a diagnostic is not an offending file.
    [ -n "$_l" ] && [ -f "${ROOT}/${_l}" ] && printf '%s\n' "$_l"
  done <<EOF_CURRENT | LC_ALL=C sort -u
$raw_records
EOF_CURRENT
  )
fi
current_count=$(printf '%s' "$current" | grep -c . || true)

# Status 1 without any readable offender is unavailable, not a clean measurement.
if [ "$rc" -eq 1 ] && [ "$current_count" -eq 0 ]; then
  echo "format-ratchet: UNAVAILABLE: formatter reported failure without naming a file" >&2
  printf '%s\n' "$output" | tail -5 >&2
  exit 2
fi

# The working baseline may shrink but cannot grow relative to its trusted reference.
# A baseline absent from that reference is an initial landing; report it explicitly.
BASE_REF=${FORMAT_RATCHET_BASE_REF:-origin/main}
# This comparison also runs with injected listings; it needs Git, not the formatter.
if command -v git >/dev/null 2>&1; then
  if committed_baseline=$(git show "${BASE_REF}:${BASELINE}" 2>/dev/null); then
    _before=$(printf '%s\n' "$committed_baseline" | grep -vE '^\s*#|^\s*$' | LC_ALL=C sort -u)
    _after=$(grep -vE '^\s*#|^\s*$' "$BASELINE" | LC_ALL=C sort -u)
    _added=$(LC_ALL=C comm -13 <(printf '%s\n' "$_before") <(printf '%s\n' "$_after") | grep -c . || true)
    if [ "${_added:-0}" -gt 0 ]; then
      echo "format-ratchet: baseline grew by ${_added} path(s) relative to ${BASE_REF}."
      echo "format-ratchet:    The baseline can only shrink."
      echo "format-ratchet:    Added paths:"
      LC_ALL=C comm -13 <(printf '%s\n' "$_before") <(printf '%s\n' "$_after") | sed 's/^/  /'
      exit 1
    fi
  else
    echo "format-ratchet: ${BASELINE} is absent from ${BASE_REF}; initial landing, no baseline to compare."
  fi
fi

expected=$(grep -vE '^\s*#|^\s*$' "$BASELINE" | LC_ALL=C sort -u)
expected_count=$(printf '%s' "$expected" | grep -c . || true)

new_count=$(LC_ALL=C comm -23 <(printf '%s\n' "$current") <(printf '%s\n' "$expected") | grep -c . || true)
fixed_count=$(LC_ALL=C comm -13 <(printf '%s\n' "$current") <(printf '%s\n' "$expected") | grep -c . || true)

# If exclusions change, remeasure with the committed .prettierignore. New offenders
# hidden by expanded exclusions must still fail.
IGNORE="${ROOT}/.prettierignore"
if [ -z "$LISTER" ] && [ -f "$IGNORE" ] && command -v git >/dev/null 2>&1; then
  if committed_ignore=$(git show "${BASE_REF}:${IGNORE}" 2>/dev/null); then
    if ! printf '%s\n' "$committed_ignore" | diff -q - "$IGNORE" >/dev/null 2>&1; then
      _ti=$(mktemp); printf '%s\n' "$committed_ignore" > "$_ti"
      _sb=$(cd "$ROOT" && NO_COLOR=1 timeout 900 $PRETTIER --ignore-path "$_ti" --check "$GLOB" 2>&1)
      _rcb=$?
      # Apply the same ANSI filter to the committed-exclusion measurement.
      _sb=$(printf '%s\n' "$_sb" | sed "s/${_ESC}\[[0-9;]*[a-zA-Z]//g")
      rm -f "$_ti"
      if [ "$_rcb" -eq 0 ] || [ "$_rcb" -eq 1 ]; then
        # Reject unclassified records here too; dropping them would hide debt.
        _previous_records=$(printf '%s\n' "$_sb" | sed -n 's/^\[warn\] \(.*\)$/\1/p')
        _previous_unclassified=$(_unclassified_records "$_previous_records")
        [ -n "$_previous_unclassified" ] && _fail_unclassified_records "$_previous_unclassified" "with .prettierignore from ${BASE_REF}"
        _previous_files=$(printf '%s\n' "$_previous_records" \
          | while IFS= read -r _l; do [ -n "$_l" ] && [ -f "${ROOT}/${_l}" ] && printf '%s\n' "$_l"; done \
          | LC_ALL=C sort -u)
        # Status 1 with no warning records means parsing failed, not zero hidden debt.
        if [ "${_rcb}" -eq 1 ] && [ -z "$(printf '%s\n' "$_previous_records" | grep -c . | grep -v '^0$')" ]; then
          echo "format-ratchet: UNAVAILABLE: measurement with .prettierignore from ${BASE_REF}" >&2
          echo "                returned rc=1 but no warning records could be read; parsing failed." >&2
          echo "                Hidden formatting debt could not be measured." >&2
          exit 2
        fi
        _previous_hidden_count=$(LC_ALL=C comm -23 <(printf '%s\n' "$_previous_files") <(printf '%s\n' "$expected") | grep -c . || true)
        if [ "${_previous_hidden_count:-0}" -gt 0 ]; then
          echo "format-ratchet: expanded exclusions hide ${_previous_hidden_count} offender(s)"
          echo "format-ratchet:    outside the baseline. Expanding .prettierignore does not fix these files:"
          LC_ALL=C comm -23 <(printf '%s\n' "$_previous_files") <(printf '%s\n' "$expected") | sed 's/^/  /'
          exit 1
        else
          # Report a completed measurement separately from an unavailable or skipped one.
          printf 'format-ratchet: measured with .prettierignore from %s; no hidden debt (rc=%s; warnings=%s; files=%s; expected=%s)\n' \
            "${BASE_REF}" "${_rcb}" \
            "$(printf '%s\n' "$_previous_records" | grep -c . || true)" \
            "$(printf '%s\n' "$_previous_files" | grep -c . || true)" \
            "$(printf '%s\n' "$expected" | grep -c . || true)" >&2
        fi
      else
        echo "format-ratchet: measurement with .prettierignore from ${BASE_REF} unavailable (rc=$_rcb)" >&2
      fi
    fi
  else
    # Git requires a repository-relative path. Report an unreadable committed ignore file.
    echo "format-ratchet: measurement skipped: git show cannot read '${BASE_REF}:${IGNORE}'" >&2
  fi
else
  # Report why the committed-exclusion measurement was skipped.
  echo "format-ratchet: measurement skipped: listing='${LISTER:-}'; ${IGNORE} exists=$([ -f "$IGNORE" ] && echo yes || echo no); git=$(command -v git >/dev/null 2>&1 && echo yes || echo no)" >&2
fi

# Print counts for both passing and failing measurements.
printf 'format-ratchet: %s offender(s); baseline %s; new %s; fixed %s\n' \
  "$current_count" "$expected_count" "$new_count" "$fixed_count"

if [ "$new_count" -gt 0 ]; then
  echo "format-ratchet: formatting debt increased; files outside the baseline:"
  LC_ALL=C comm -23 <(printf '%s\n' "$current") <(printf '%s\n' "$expected") | sed 's/^/  /'
  echo "format-ratchet: Fix them with (cd ${ROOT} && npx prettier --write <file>)."
  echo "format-ratchet: Do not expand the baseline to include a new offender."
  exit 1
fi

if [ "$fixed_count" -gt 0 ]; then
  # Verify every apparent fix without exclusions. A file removed from the listing
  # may still be unformatted. Injected listings also receive this per-file check.
  hidden=""
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    if [ -n "$LISTER" ]; then
      eval "$LISTER" "$f" >/dev/null 2>&1 || hidden="${hidden}${f}"$'\n'
    elif [ -e "${ROOT}/${f}" ] \
      && ! (cd "$ROOT" && timeout 120 $PRETTIER --ignore-path /dev/null --check "$f" >/dev/null 2>&1); then
      hidden="${hidden}${f}"$'\n'
    fi
  done <<EOF_FIXED
$(LC_ALL=C comm -13 <(printf '%s\n' "$current") <(printf '%s\n' "$expected"))
EOF_FIXED
  hidden_count=$(printf '%s' "$hidden" | grep -c . || true)
  if [ "${hidden_count:-0}" -gt 0 ]; then
    echo "format-ratchet: ${hidden_count} file(s) left the listing without being formatted;"
    echo "format-ratchet:    an exclusion hides these unformatted files:"
    printf '%s' "$hidden" | sed 's/^/  /'
    exit 1
  fi
  echo "format-ratchet: baseline can shrink by ${fixed_count}; remove these paths from ${BASELINE}:"
  LC_ALL=C comm -13 <(printf '%s\n' "$current") <(printf '%s\n' "$expected") | sed 's/^/  /'
fi

echo "format-ratchet: formatting debt did not increase"
exit 0
