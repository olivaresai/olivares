#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-release-snapshot-checksum.sh — the SNAPSHOT / REAL-RELEASE boundary of GoReleaser's checksum stage.
#
# WHY IT EXISTS (measured). Run 36357280245 (job 108727412693, 2026-09-27) took the `task release:snapshot` route:
# every binary, archive, package and SBOM built in 14m8s, then "calculating checksums" stopped with
#   globbing failed for pattern release-commit.txt: matching "./release-commit.txt": file does not exist
# checksum.extra_files named the two RELEASE-ONLY evidence files unconditionally (since 8a5e2cbb, 2026-08-15), while
# the evidence hook deliberately writes nothing for a snapshot (scripts/release-commit-evidence.sh, pinned by
# test-release-workspace-e2e.sh "a snapshot build writes no evidence at all"). From that commit on, no snapshot finished.
#
# WHAT IT EVALUATES, AND HOW FAITHFULLY. It never runs goreleaser. It reads checksum.extra_files and release.extra_files
# from .goreleaser.yaml, renders each glob with Go's text/template (the engine GoReleaser's templates are built on;
# missingkey=error and only .IsSnapshot supplied, so a glob that needs any other field is NOT EVALUABLE here, exit 2,
# never a pass), and applies the extra-files rule of the pinned GoReleaser 2.17.0 as its binary carries it
# (internal/extrafiles/extra_files.go, extrafiles.Find): the glob is templated; a glob that renders EMPTY is skipped
# ("ignoring empty glob"); a non-empty glob that matches nothing FAILS ("globbing failed for pattern"). The trees the
# rule is applied to come from the real hooks — release-commit-evidence.sh and render-release-installer.sh — so the
# snapshot side is what those scripts actually leave, not what a fixture assumes.
#
# WHAT IT IS NOT: a completed snapshot build. The cross-build and its checksum stage are qualified on the admitted
# runner; this proves the configuration's decision at the boundary, in both directions.
#
#   S  a snapshot renders no release-only evidence entry, every entry it keeps matches the snapshot tree, and the hook
#      leaves no evidence file behind;
#   R  a real release renders both evidence files for checksum AND declares both for upload, by their literal names;
#   M  a real release missing either evidence file FAILS the rule, in both blocks (and passes with both present);
#   C  the snapshot's checksum set is not narrowed: checksum is not disabled, has no ids filter, is not split, and every
#      non-evidence extra file renders identically for a snapshot and a release.
#
# usage: test-release-snapshot-checksum.sh [CASES]   CASES is any of the letters SRMC (default: all four), so each named
#        control can be run, and can fail, on its own.
# Exit: 0 every case holds · 1 a case failed · 2 NO HE PODIDO MIRAR (a tool is missing or a glob is not evaluable).
set -uo pipefail
export LC_ALL=C

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
CFG="${OLIVARES_SNAPSHOT_CHECKSUM_CFG:-${ROOT}/.goreleaser.yaml}"
EVIDENCE=(release-commit.txt release-build-context.json)
CASES="${1:-SRMC}"
case "$CASES" in *[!SRMC]* | "") echo "usage: $0 [SRMC]" >&2; exit 2 ;; esac
want_case() { case "$CASES" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
OID=0123456789abcdef0123456789abcdef01234567

cannot_look() {
	echo "test-release-snapshot-checksum: NO HE PODIDO MIRAR: $*" >&2
	exit 2
}
[ -f "$CFG" ] || cannot_look "no configuration at $CFG"
command -v go >/dev/null 2>&1 || cannot_look "go is absent; the globs cannot be rendered with text/template"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/snapshot-checksum.XXXXXX")" || cannot_look "mktemp failed"
trap 'rm -rf -- "$WORK"' EXIT

pass=0
fail=0
failed_names=()
check() { # check <name> <why> <status>
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf 'ok   - %s (%s)\n' "$1" "$2"
	else
		fail=$((fail + 1))
		failed_names+=("$1")
		printf 'FAIL - %s (%s)\n' "$1" "$2"
	fi
}

# THE RENDERER: stdlib text/template, one glob per input line, one "R:<rendered>" per output line (so an empty
# rendering is a visible line, not a missing one). Built once, outside any module, with no toolchain download.
cat >"$WORK/render.go" <<'GO'
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/template"
)

func main() {
	data := map[string]any{"IsSnapshot": os.Args[1] == "true"}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		t, err := template.New("glob").Option("missingkey=error").Parse(sc.Text())
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %q: %v\n", sc.Text(), err)
			os.Exit(2)
		}
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			fmt.Fprintf(os.Stderr, "render %q: %v\n", sc.Text(), err)
			os.Exit(2)
		}
		fmt.Printf("R:%s\n", b.String())
	}
}
GO
(cd "$WORK" && GOWORK=off GOFLAGS='' GOTOOLCHAIN=local go build -o "$WORK/render" render.go) >"$WORK/build.out" 2>&1 ||
	cannot_look "the renderer did not build: $(head -c 300 "$WORK/build.out")"

block() { # block <top-level key> — the lines under that column-0 key
	awk -v k="$1:" '$0 == k { on = 1; next } on && /^[a-z_]+:/ { exit } on { print }' "$CFG"
}
top() { # top <top-level key> <field> — a two-space field of that block, comments stripped
	block "$1" | awk -v f="  $2:" 'index($0, f) == 1 { sub(/^[^:]*:[[:space:]]*/, ""); sub(/[[:space:]]+#.*$/, ""); print; exit }'
}
globs() { # globs <top-level key> — that block's extra_files globs, YAML-unquoted, one per line; an unsupported form
	# prints ERR:<value> so the caller refuses in the MAIN shell (an exit inside a pipeline would only leave a subshell).
	block "$1" | awk '
		/^[[:space:]]*#/ { next }
		/^  extra_files:[[:space:]]*$/ { on = 1; next }
		on && /^  [a-z_]+:/ { exit }
		on && /^    - glob:/ {
			sub(/^    - glob:[[:space:]]*/, "")
			if ($0 ~ /^'\''.*'\''$/) { $0 = substr($0, 2, length($0) - 2); gsub(/'\'''\''/, "'\''"); print; next }
			if ($0 ~ /^"/ || $0 ~ / #/) { print "ERR:" $0; next }
			print
		}
	'
}
# Every rendering this battery reads is computed HERE, once, in the main shell, and a failure stops the run with 2.
for _key in checksum release; do
	globs "$_key" >"$WORK/globs.$_key"
	! grep -q '^ERR:' "$WORK/globs.$_key" || cannot_look "a $_key glob is in a form this battery does not parse: $(grep -m1 '^ERR:' "$WORK/globs.$_key")"
	[ -s "$WORK/globs.$_key" ] || cannot_look "no $_key.extra_files globs were read from $CFG"
	for _snap in true false; do
		"$WORK/render" "$_snap" <"$WORK/globs.$_key" >"$WORK/r.$_key.$_snap" 2>"$WORK/r.$_key.$_snap.err" ||
			cannot_look "a $_key glob is not evaluable with IsSnapshot=$_snap: $(head -c 300 "$WORK/r.$_key.$_snap.err")"
		[ "$(wc -l <"$WORK/r.$_key.$_snap")" -eq "$(wc -l <"$WORK/globs.$_key")" ] ||
			cannot_look "the $_key rendering lost a line"
	done
done
# extra_files_rule <tree> <top-level key> <true|false> — GoReleaser 2.17.0's extrafiles.Find decision; prints one line per
# glob and returns 1 when any glob would stop the stage.
extra_files_rule() {
	local tree="$1" line g rc=0
	while IFS= read -r line; do
		g="${line#R:}"
		if [ -z "$g" ]; then
			echo "  skip: ignoring empty glob"
			continue
		fi
		# A MATCH IS A PATH THAT EXISTS. A pattern with no wildcard (release-commit.txt) expands to itself in bash even
		# under nullglob, so counting the expansion would call a missing literal a match — the very case GoReleaser
		# stops on ("matching ./release-commit.txt: file does not exist"). Only existing paths count.
		if (cd "$tree" && shopt -s nullglob && n=0 && for x in $g; do if [ -e "$x" ] || [ -L "$x" ]; then n=$((n + 1)); fi; done && [ "$n" -ge 1 ]); then
			echo "  match: $g"
		else
			echo "  STOP: globbing failed for pattern $g"
			rc=1
		fi
	done <"$WORK/r.$2.$3"
	return "$rc"
}
is_evidence() { local e; for e in "${EVIDENCE[@]}"; do [ "$1" = "$e" ] && return 0; done; return 1; }

# The two trees, each produced by the real scripts.
SNAP="$WORK/snapshot-tree"
REL="$WORK/release-tree"
mkdir -p "$SNAP" "$REL"
(cd "$SNAP" && env -i PATH="/usr/bin:/bin" HOME="$WORK" GITHUB_SHA="$OID" \
	bash "$ROOT/scripts/release-commit-evidence.sh" true) >"$WORK/hook-snapshot.out" 2>&1
snap_hook=$?
bash "$ROOT/scripts/render-release-installer.sh" 0.0.0-SNAPSHOT-0123456789 \
	"$SNAP/dist/olivares-install-0.0.0-SNAPSHOT-0123456789.sh" true >"$WORK/render-snapshot.out" 2>&1 ||
	cannot_look "render-release-installer.sh did not render the snapshot installer: $(head -c 300 "$WORK/render-snapshot.out")"
(cd "$REL" && env -i PATH="/usr/bin:/bin" HOME="$WORK" GITHUB_SHA="$OID" \
	GITHUB_REPOSITORY_ID=123456789 GITHUB_REPOSITORY=olivaresai/olivares GITHUB_EVENT_NAME=push \
	GITHUB_REF=refs/tags/v26.10.0 GITHUB_RUN_ID=36357280245 GITHUB_RUN_ATTEMPT=1 \
	bash "$ROOT/scripts/release-commit-evidence.sh" false) >"$WORK/hook-release.out" 2>&1 ||
	cannot_look "the evidence hook did not write a real release's evidence: $(head -c 300 "$WORK/hook-release.out")"
bash "$ROOT/scripts/render-release-installer.sh" 26.10.0 "$REL/dist/olivares-install-26.10.0.sh" false \
	>"$WORK/render-release.out" 2>&1 || cannot_look "render-release-installer.sh did not render the release installer"

if want_case S; then
echo "== S: a snapshot =="
if [ "$snap_hook" -eq 0 ] && [ ! -e "$SNAP/release-commit.txt" ] && [ ! -e "$SNAP/release-build-context.json" ]; then _s=0; else _s=1; fi
check "S1 the snapshot hook leaves no evidence file in the tree" "no untracked file, no dummy attestation" "$_s"
_leak=""
while IFS= read -r line; do is_evidence "${line#R:}" && _leak="$_leak ${line#R:}"; done <"$WORK/r.checksum.true"
if [ -z "$_leak" ]; then _s=0; else _s=1; fi
check "S2 a snapshot renders no release-only evidence entry for checksum" "${_leak:-none rendered}" "$_s"
extra_files_rule "$SNAP" checksum true
check "S3 every checksum entry a snapshot keeps matches the snapshot tree" "the checksum stage does not stop" $?

fi
if want_case R; then
echo "== R: a real release =="
_want="$(printf '%s\n' "${EVIDENCE[@]}" | sort)"
_have="$(sed 's/^R://' "$WORK/r.checksum.false" | while IFS= read -r g; do is_evidence "$g" && echo "$g"; done | sort)"
# The status is taken BEFORE the message is built: a command substitution in check's own arguments runs first and
# would reset $? to its own 0, and R1 could then never fail (caught by the discrimination mutants, 2026-09-28).
if [ "$_have" = "$_want" ]; then _s=0; else _s=1; fi
check "R1 a real release checksums both evidence files" "$(echo "$_have" | tr '\n' ' ')" "$_s"
_have="$(sed 's/^R://' "$WORK/r.release.false" | while IFS= read -r g; do is_evidence "$g" && echo "$g"; done | sort)"
if [ "$_have" = "$_want" ]; then _s=0; else _s=1; fi
check "R2 a real release uploads both evidence files" "$(echo "$_have" | tr '\n' ' ')" "$_s"
extra_files_rule "$REL" checksum false && extra_files_rule "$REL" release false
check "R3 with both evidence files present, both blocks resolve" "positive control for M" $?

fi
if want_case M; then
echo "== M: a real release missing its evidence =="
for _e in "${EVIDENCE[@]}"; do
	mv -- "$REL/$_e" "$WORK/$_e.held"
	! extra_files_rule "$REL" checksum false >/dev/null
	check "M1 checksum stops a release with no $_e" "no permissive missing-glob rule" $?
	! extra_files_rule "$REL" release false >/dev/null
	check "M2 release stops a release with no $_e" "the upload half fails closed too" $?
	mv -- "$WORK/$_e.held" "$REL/$_e"
done

fi
if want_case C; then
echo "== C: the snapshot's checksum set is not narrowed =="
_disable="$(top checksum disable)"
if [ -z "$_disable" ] || [ "$_disable" = false ]; then _s=0; else _s=1; fi
check "C1 checksum is not disabled" "${_disable:-absent}" "$_s"
# A HERE-STRING, NOT A PIPE: under pipefail `producer | grep -q` can exit 141 when grep stops early, and the `!` would
# turn that into a pass.
[ -z "$(top checksum ids)" ] && ! grep -qE '^  ids:' <<<"$(block checksum)"
check "C2 checksum has no ids filter" "every binary, archive, package and SBOM stays in the set" $?
_split="$(top checksum split)"
[ -z "$_split" ] || [ "$_split" = false ]
check "C3 checksum is not split" "one checksums.txt, as a release has" $?
paste -d '\t' "$WORK/r.checksum.false" "$WORK/r.checksum.true" >"$WORK/pairs"
_narrowed=""
while IFS=$'\t' read -r r s; do
	r="${r#R:}"
	s="${s#R:}"
	is_evidence "$r" && continue
	[ "$r" = "$s" ] || _narrowed="$_narrowed [$r -> ${s:-<empty>}]"
done <"$WORK/pairs"
[ -z "$_narrowed" ] && [ "$(wc -l <"$WORK/pairs")" -ge 1 ]
check "C4 every non-evidence extra file renders the same for a snapshot" "${_narrowed:-the installer and all others unchanged}" $?
fi

echo
printf 'pass=%d fail=%d\n' "$pass" "$fail"
if [ "$fail" -ne 0 ]; then
	printf 'failed: %s\n' "${failed_names[*]}"
	echo "test-release-snapshot-checksum: RED"
	exit 1
fi
[ "$pass" -ge 1 ] || cannot_look "no case ran"
echo "test-release-snapshot-checksum: OK — ${pass} cases [$CASES]"
