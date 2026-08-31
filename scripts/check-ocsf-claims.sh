#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# regression guard: a CODE surface may not promise Amazon Security Lake
# ingest while the OCSF emitter speaks a version Security Lake does not accept.
#
# WHY THIS EXISTS. On 2026-08-06 the public reference page said the honest thing
# ("a Security Lake custom source caps at OCSF 1.3 in Parquet … a declared gap,
# not an oversight", in all seven locales) while FIVE code comments promised the
# opposite — native Security Lake ingest, "without a bespoke parser", three of
# them citing OBS-02 as if that were the evidence. A maintainer reads the comment
# in front of them, not the reference page. This is the inverted failure: the
# public surface honest, the code lying to the next reader.
#
# THE RULE, and it is deliberately not "ban the words". The corrected comments
# still NAME Security Lake — they must, to name the gap. What the five defects
# had in common is that not one of them carried the limit. So: any window of a
# code file that mentions Security Lake MUST also name the limit (1.3 AND
# Parquet). Same shape as the FIPS_CONTEXT_PATTERN rule in check-docs-honesty.sh
# — a claim is allowed when its anchor travels with it.
#
# A window is one contiguous comment block (so a claim split across newlines is
# judged whole — that is exactly how three of the five hid from a line-oriented
# grep), or one non-comment line on its own.
#
# THE PIN IS READ, NOT ASSUMED, and it is judged in BOTH directions:
#   - unreadable  -> exit 2, the claims are UNVERIFIED (never a silent pass);
#   - <= 1.3      -> FAIL: a Security-Lake-shaped emitter may now exist, so the
#                   comments that say "NOT Security Lake" have themselves gone
#                   stale and must be re-decided rather than left to rot;
#   - >  1.3      -> the co-mention rule applies.
# Note the rule survives that future: a writer that DOES emit for Security Lake
# will say "OCSF 1.3 in Parquet" in its own comment, and pass.
#
# LIMITS, stated rather than discovered later:
#   - It is a prose gate. "Security Lake ingests this natively (though it caps at
#     1.3 in Parquet)" satisfies the anchor rule and is still wrong. What the gate
#     guarantees is that the limit never goes missing — the failure that happened.
#   - Its universe is source extensions (see EXTS). Markdown is check-docs-honesty
#     and check-format-docs territory; docs-site/ is excluded on purpose.
#   - Pure content lint, no network. `--selftest` proves both directions and
#     writes ONLY under a mktemp dir — it never touches the host repo.
set -euo pipefail

# The ambient git environment OUTRANKS `-C`: with GIT_DIR exported — which git does from
# every LINKED worktree, i.e. from every parallel session — this script's throwaway
# repositories would be driven into the LIVE repository instead. Fail closed.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

# The needle is split so this script does not match itself if the universe ever
# grows to include shell — the convention check-docs-honesty.sh already uses.
NEEDLE_RE='Secu''rity[[:space:]]+Lake'
# The anchor: the two facts that make the claim honest, in either order.
# The backslash is DOUBLED on purpose: awk -v processes escape sequences, so a
# single `\.` reaches the regex engine as a bare `.` — "any character" — and
# "1x3" would satisfy the version anchor. Measured, not assumed.
ANCHOR_V_RE='(^|[^0-9.])1\\.3([^0-9]|$)'
ANCHOR_P_RE='[Pp]arquet'

EXTS='go ts tsx js jsx mjs cjs java py rs sql proto tmpl templ'

fail() { echo "ocsf-claims: FAIL — $1" >&2; exit 1; }
unverified() { echo "ocsf-claims: UNVERIFIED — $1" >&2; exit 2; }

collect_files() { # collect_files <root>
  root="$1"
  if [ -e "$root/.git" ] && git -C "$root" rev-parse --git-dir >/dev/null 2>&1; then
    # Tracked files only: the working tree carries build output and scratch dirs.
    git -C "$root" ls-files -z | tr '\0' '\n'
  else
    ( cd "$root" && find . -type f \
        ! -path './.git/*' ! -path '*/node_modules/*' ! -path '*/dist/*' \
        ! -path '*/vendor/*' ! -path './.export-tmp/*' | sed 's|^\./||' )
  fi | awk -v exts="$EXTS" '
    BEGIN { n = split(exts, a, " "); for (i = 1; i <= n; i++) ok["." a[i]] = 1 }
    # docs-site is the public documentation surface; it has its own guards and is
    # excluded here so this gate can never be the thing that edits a translation.
    /^docs-site\// { next }
    {
      p = index($0, ".")
      if (p == 0) next
      ext = $0; sub(/^.*(\.|\/)/, "", ext); ext = "." ext
      if (ext in ok) print
    }'
}

# scan_tree <root> -> prints "file:line" per offending window, one per line.
scan_tree() {
  root="$1"
  collect_files "$root" | while IFS= read -r rel; do
    [ -f "$root/$rel" ] || continue
    awk -v file="$rel" -v needle="$NEEDLE_RE" -v av="$ANCHOR_V_RE" -v ap="$ANCHOR_P_RE" '
      function flush(   t) {
        if (buf == "") return
        if (buf ~ needle && !(buf ~ av && buf ~ ap)) print file ":" start
        buf = ""; start = 0
      }
      {
        line = $0
        trimmed = line; sub(/^[ \t]*/, "", trimmed)
        is_comment = 0
        if (inblock) { is_comment = 1; if (line ~ /\*\//) inblock = 0 }
        else if (trimmed ~ /^\/\*/) { is_comment = 1; if (!(trimmed ~ /\*\//)) inblock = 1 }
        else if (trimmed ~ /^\/\//) is_comment = 1
        else if (trimmed ~ /^#/) is_comment = 1

        if (is_comment) {
          if (buf == "") start = NR
          # Strip the comment markers so a phrase broken across lines rejoins as
          # ordinary prose: "…AWS Security\n// Lake / Athena…" becomes one string.
          sub(/^[ \t]*(\/\/+|#+|\/\*+|\*+)[ \t]?/, "", line)
          sub(/\*\/[ \t]*$/, "", line)
          buf = (buf == "" ? line : buf " " line)
        } else {
          flush()
          if (line ~ needle && !(line ~ av && line ~ ap)) print file ":" NR
        }
      }
      END { flush() }
    ' "$root/$rel"
  done
}

run_gate() { # run_gate <root> [quiet]
  root="$1"; quiet="${2:-}"
  pin_file="$root/sdk/siemwire/ocsf.go"
  [ -f "$pin_file" ] || unverified "no OCSF pin at $pin_file; the emitted version is unknown, so no claim about it can be judged"
  pin="$(sed -n 's/^const[ \t]\+OCSFVersion[ \t]*=[ \t]*"\([^"]*\)".*/\1/p' "$pin_file" | head -1)"
  [ -n "$pin" ] || unverified "could not read 'const OCSFVersion' from $pin_file — the pin is the whole premise of this gate"

  major="${pin%%.*}"; rest="${pin#*.}"; minor="${rest%%.*}"
  case "$major$minor" in *[!0-9]*|'') unverified "OCSF pin '$pin' is not a numeric major.minor; refusing to guess" ;; esac

  if [ "$major" -lt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -le 3 ]; }; then
    fail "the OCSF pin dropped to $pin, which Amazon Security Lake CAN accept (<= 1.3).
  The five corrected comments state the opposite ('NOT native Security Lake ingest'),
  so they are now the stale claim. Re-decide them — and this gate — before shipping;
  do not leave prose that was true at 1.8.0 asserting a limit that no longer holds."
  fi

  hits="$(scan_tree "$root" || true)"
  if [ -n "$hits" ]; then
    fail "code surfaces mention Security Lake without naming the limit it imposes
  (OCSF 1.3 in Parquet), while the emitter is pinned at $pin. Each window below must
  carry BOTH '1.3' and 'Parquet', the same limit the public reference page states:

$(printf '%s\n' "$hits" | sed 's/^/    /')
"
  fi
  [ -n "$quiet" ] || echo "ocsf-claims: OK — OCSF pin $pin; every Security Lake mention in code names the 1.3/Parquet limit"
}

# ---- self-test: both directions, on throwaway files, never the host repo -----
selftest() {
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/ocsf-claims-selftest.XXXXXX")"
  trap 'rm -rf "$tmp"' EXIT HUP INT TERM
  mkdir -p "$tmp/sdk/siemwire" "$tmp/core/audit"
  printf 'package siemwire\n\nconst OCSFVersion = "1.8.0"\n' > "$tmp/sdk/siemwire/ocsf.go"
  rc=0

  # (1) RED: the exact defect, split across a newline the way three of the five
  #     were — a line-oriented grep for the phrase does not see this.
  cat > "$tmp/core/audit/export.go" <<'RED'
package audit

// FormatOCSF is an OCSF v1.8.0 projection so an AWS Security
// Lake / Athena SOC ingests the tamper-evident chain natively.
const FormatOCSF = "ocsf"
RED
  if out="$("$0" --root "$tmp" 2>&1)"; then
    echo "selftest: FAIL — the split-phrase defect was NOT caught (gate passed)" >&2; rc=1
  else
    echo "$out" | grep -q 'core/audit/export.go:3' \
      || { echo "selftest: FAIL — caught, but did not name core/audit/export.go:3; got: $out" >&2; rc=1; }
    echo "selftest: red  OK — split-phrase claim rejected, window named"
  fi
  # Control: a line-oriented grep really is blind to it, which is why this exists.
  if grep -q 'Secu''rity Lake' "$tmp/core/audit/export.go"; then
    echo "selftest: FAIL — control broken: the phrase was NOT split" >&2; rc=1
  else
    echo "selftest: ctrl OK — a line-oriented grep does not see the split phrase"
  fi

  # (2) GREEN: same mention, limit named -> passes.
  cat > "$tmp/core/audit/export.go" <<'GREEN'
package audit

// FormatOCSF is an OCSF v1.8.0 JSON projection for a SOC that accepts 1.8.0.
// It is NOT native Amazon Security Lake ingest: a custom source caps at OCSF
// 1.3 in Parquet. A declared gap, not an oversight.
const FormatOCSF = "ocsf"
GREEN
  if "$0" --root "$tmp" >/dev/null 2>&1; then
    echo "selftest: green OK — the corrected wording passes"
  else
    echo "selftest: FAIL — the corrected wording was rejected (false positive)" >&2; rc=1
  fi

  # (3) The pin gate, both failure modes.
  printf 'package siemwire\n\nconst OCSFVersion = "1.3.0"\n' > "$tmp/sdk/siemwire/ocsf.go"
  if "$0" --root "$tmp" >/dev/null 2>&1; then
    echo "selftest: FAIL — a pin of 1.3.0 did not trigger the stale-comment refusal" >&2; rc=1
  else
    echo "selftest: pin  OK — dropping the pin to 1.3.0 refuses, not passes"
  fi
  printf 'package siemwire\n\n// no pin here\n' > "$tmp/sdk/siemwire/ocsf.go"
  set +e; "$0" --root "$tmp" >/dev/null 2>&1; st=$?; set -e
  [ "$st" -eq 2 ] && echo "selftest: unv  OK — an unreadable pin exits 2 (UNVERIFIED), not 0" \
    || { echo "selftest: FAIL — unreadable pin exited $st, expected 2" >&2; rc=1; }

  [ "$rc" -eq 0 ] && echo "ocsf-claims selftest: OK (red + control + green + pin + unverified)"
  return "$rc"
}

case "${1:-}" in
  --selftest) selftest ;;
  --root) [ $# -ge 2 ] || unverified "--root needs a directory"; run_gate "$2" ;;
  '') run_gate "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)" ;;
  *) echo "usage: $0 [--selftest | --root <dir>]" >&2; exit 2 ;;
esac
