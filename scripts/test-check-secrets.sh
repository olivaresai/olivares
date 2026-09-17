#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-check-secrets.sh — the regression battery for scripts/check-secrets.sh.
#
# It builds throwaway repositories in a temporary directory and asserts the answers the gate
# has to keep straight: clean, named finding, could-not-look, a finding outside the ref, the
# redaction canary, and — the actual 2026-08-04 cause — whose configuration it is applying. Nothing here touches this repository, and no case plants a
# string in it: the decoy is an obviously-fake private-key block written into a scratch repo
# that is deleted on exit.
#
# MUTATION RESULTS, MEASURED 2026-08-04 — written down as they came out, not as expected.
# A test is not done until you break the branch it guards and watch it go red FOR ITS OWN
# REASON, and a mutant that does not apply reports "all green", which reads as a pass.
#
#   M1  reachability column always answers "IN"        -> case 4 red, ALONE
#   M2  grade on gitleaks' exit code, not the report   -> cases 2 AND 4 red (both are the
#                                                         naming property; the gate stops
#                                                         naming anything at all)
#   M3  unparseable report reported as CLEAN           -> case 5 red, ALONE
#   M4  drop --redact                                  -> NOTHING goes red. Recorded because
#                                                         it is the honest result: this gate
#                                                         prints RuleID/File/Line/Commit/
#                                                         Author/Date/Fingerprint and never
#                                                         the Secret or Match field, so
#                                                         --redact alone is not load-bearing
#                                                         for the OUTPUT. It stays as defence
#                                                         in depth for the report FILE.
#   M5  drop --redact AND print the Secret field       -> case 6 red, ALONE. This is the real
#                                                         leak path, and case 6 is the canary
#                                                         that stands on it.
#   M6  remove the config-vs-base comparison entirely  -> cases 7 AND 8 red
#   M7  fall back silently when the requested base
#       does not resolve                               -> case 8 red, ALONE
#   M8  restore the `.*_test.go$` path exemption       -> case 9 red, ALONE
#       (A-04: the path used to hide a real private
#       key; the canary is an ephemeral RSA in
#       probe_test.go). A no-fire mutant of case 9
#       is restoring that path: the decoy is still
#       planted, the gate stays green, and that is
#       the defect.
#   M9  restore the `.*/testdata/.*` path exemption    -> case 10 red, ALONE
#
# MUTATION RESULTS, MEASURED 2026-09-06, on the three exact blocks for the four
# non-secret captures of that date (cases 13-15; the blocks are the last three in
# .gitleaks.toml). Recorded as they came out:
#
#   M10 delete the three blocks                        -> cases 13 AND 16 red (13a: the
#                                                         captures are findings again; 13b:
#                                                         the cut marker is gone and the
#                                                         no-fire control refuses to pass;
#                                                         16: the four captures come back as
#                                                         EXTRA dir-mode findings)
#   M11 drop `paths` from the three blocks             -> cases 14 AND 16 red (the same
#                                                         expression on another path becomes
#                                                         exempt; in 16 the unrelated-path
#                                                         witness vanishes)
#   M12 widen the value regexes to their SHAPE         -> case 14 red, ALONE (a changed
#       (`[0-9a-f]{64}`, `olivares_26\.[0-9]+\.[0-9]+`,   digest or coordinate on the same
#       `communicationIncomingHandoff`)                   line becomes exempt)
#   M13 `condition = "AND"` -> `"OR"`                   -> cases 14 AND 16 red (the path alone
#                                                         exempts the rule for the whole file;
#                                                         in 16 every generic-api-key on the
#                                                         three paths and the witness vanish,
#                                                         the private keys stay)
#   M14 drop `targetRules` (the blocks become GLOBAL)  -> case 16 red, ALONE: exactly the six
#                                                         same-path findings vanish (three
#                                                         credential-shaped, three private
#                                                         keys) and the witness stays. Cases
#                                                         13-15 stay green, and that is the
#                                                         honest shape of the defect: over
#                                                         history — the mode the gate runs —
#                                                         a global AND block with regexes is
#                                                         evaluated per finding, like a rule
#                                                         block. In `dir`/`--no-git` mode
#                                                         sources/common.go skips the whole
#                                                         file by path before any regex runs.
#                                                         Case 16 runs the scanner in that
#                                                         mode itself; it was added after an
#                                                         independent review measured M14
#                                                         passing the 15-case battery.
#
# MUTATION RESULTS, MEASURED 2026-09-08, on the one exact block for the pre-verify diagnostic
# canary (cases 19-24; the block is the last one in .gitleaks.toml). Measured with the pinned
# scanner (gitleaks v8.30.1) run directly, in BOTH modes, on the very specimens these cases
# plant — the combined same-path file of case 24 plus the other-path witness — and mapped onto
# the cases by their assertions; the battery itself ran once, as delivered. Recorded as they
# came out, with the identities that vanished:
#
#   M15 cut the block                                  -> 19a AND 19b AND 24 red (the exact
#                                                         capture is a finding again on its
#                                                         path; the cut marker is gone and the
#                                                         no-fire control refuses to pass; the
#                                                         dir-mode set gains the capture)
#   M16 drop `paths`                                   -> 22 AND 24 red (the exact capture on
#                                                         ANOTHER path becomes exempt; in 24
#                                                         the other-path witness vanishes)
#   M17 drop `targetRules` (the block becomes GLOBAL)  -> 24 red, ALONE: over history nothing
#                                                         moves — the same honest shape as M14
#                                                         — and in `dir` mode the near miss,
#                                                         the other PAT and the private key on
#                                                         the path ALL vanish (whole-file skip)
#   M18 `condition = "AND"` -> `"OR"`                   -> 20 AND 21 AND 22 AND 24 red (the path
#                                                         term alone exempts every github-pat
#                                                         in the file, and the regex term alone
#                                                         exempts the capture on any path; the
#                                                         private key stays, so 23 is green)
#   M19 widen the regex to the SHAPE                   -> 20 AND 21 AND 24 red (a different
#       (`^ghp_[0-9A-Za-z]{36}$`)                         PAT and the one-byte near miss on the
#                                                         same path become exempt)
#   M20 drop the `$` anchor, 39-byte prefix            -> 21 AND 24 red, and ONLY those: the
#                                                         capture with its last byte changed is
#                                                         exempt while a different PAT (20) is
#                                                         still reported. The near miss is the
#                                                         one control that sees this mutant,
#                                                         which is why it is a case of its own.
#   M21 anchor the 43-byte SOURCE literal instead of   -> 19a AND 24 red (the block matches
#       the 40-byte capture                               nothing: the `whsec_` lesson, now
#                                                         with a case standing on it)
#
# MUTATION RESULTS, MEASURED 2026-09-10, on the exact route-evidence digest-vector block
# (cases 43-52). Each mutant changes one load-bearing term in a copied configuration:
#
#   M22 drop `targetRules`                             -> case 49 exposes the dir-mode global
#                                                        path prefilter: every target-path
#                                                        finding disappears
#   M23 `condition = "AND"` -> `"OR"`                  -> case 50 exposes both sides: the path
#                                                        hides a near miss and the regex hides
#                                                        the exact declaration on another path
#   M24 drop `paths`                                   -> case 51 exposes the exact declaration
#                                                        on another path
#   M25 widen the final digest nibble                  -> case 52 exposes the one-byte near miss
#
# So case 6 does not prove --redact works; it proves the gate never puts the secret in the
# log by either route. That distinction is the difference between a test and a decoration.
#
# Exit 0 = every case passed. Exit 1 = a case failed (named). Exit 2 = could not run the
# battery at all (no gitleaks, no git) — which is NOT a pass.
set -u

# The ambient git environment OUTRANKS `-C`: with GIT_DIR exported — which git does
# from every LINKED worktree, i.e. from every parallel session — this script's throwaway
# repositories would be driven into the LIVE repository instead. Measured 2026-08-06;
# it left the branch of PR #526 pointing at a fixture commit. Fail closed: a missing
# sanitiser is "I could not isolate", never "isolation was not needed".
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "test-check-secrets: not inside a git repository — could not run" >&2
	exit 2
}
GATE="$ROOT/scripts/check-secrets.sh"
[ -x "$GATE" ] || [ -f "$GATE" ] || {
	echo "test-check-secrets: $GATE not found — could not run" >&2
	exit 2
}
command -v gitleaks >/dev/null 2>&1 || {
	echo "test-check-secrets: gitleaks is not on PATH — could not run the battery." >&2
	echo "test-check-secrets: this is NOT a pass. Install gitleaks (the CI secrets job does)." >&2
	exit 2
}

# The battery needs a scratch directory it can EXECUTE from: case 5 stands up a gitleaks
# shim, and a shim that cannot exec is a case that silently tests nothing. In this project's
# container /tmp is mounted `noexec` (measured 2026-08-04), so the obvious `mktemp -d -t`
# produces a directory where every execve dies EACCES — and case 5 then runs the REAL
# gitleaks and passes for the wrong reason.
#
# The selection used to live here as a private helper. It moved to lib/exec-workdir.sh on
# 2026-08-07, when scripts/test-check-hooks-path.sh turned out to need the same fact and a
# SECOND edge of it: on a noexec mount `test -x` answers false even when the bit is set, so
# the mount breaks permission-bit assertions as well as execve. Two consumers of one
# hard-won environment fact is exactly the case for one file — a fix applied to a copy is a
# fix the other copy does not get.
_olivares_exec_workdir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/exec-workdir.sh"
# shellcheck source=/dev/null
. "$_olivares_exec_workdir" || {
	echo "FATAL: cannot source $_olivares_exec_workdir" >&2
	exit 2
}
unset _olivares_exec_workdir
WORK="$(olivares_pick_exec_workdir check-secrets-tests)" || {
	echo "test-check-secrets: no scratch directory allows execve (tried RUNNER_TEMP, TMPDIR, /tmp, HOME, /workspace/.olivares-tmptest)." >&2
	echo "test-check-secrets: could not run the battery. This is NOT a pass." >&2
	exit 2
}
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

fails=0
pass() { printf '  ok   %s\n' "$1"; }
fail() {
	printf '  FAIL %s\n' "$1" >&2
	printf '       %s\n' "$2" >&2
	fails=$((fails + 1))
}

# An obviously-fake key block, ASSEMBLED AT RUNTIME and never stored as a literal.
#
# The first version of this function wrote the header out in one piece, and the gate this very
# file tests then flagged it — correctly, by rule, file and line: a full-history scan does not
# care that the string lives in a test. Allowlisting the path was the easy fix and the wrong
# one; it would blind the scanner to this file forever. Splitting the marker means no committed
# line matches the rule, while the file WRITTEN by the function still contains the real header
# and still trips the detector, which is the whole point of a decoy.
plant_decoy() {
	local head="-----BEGIN RSA PRI""VATE KEY-----"
	local tail="-----END RSA PRI""VATE KEY-----"
	{
		echo "$head"
		echo "NOTAREALKEYnotarealkeyNOTAREALKEYnotarealkeyNOTAREALKEYnotarealkey"
		echo "$tail"
	} >"$1"
}

setup_fail() {
	printf 'test-check-secrets: invalid fixture setup: %s failed\n' "$1" >&2
	if [ -n "${2:-}" ]; then
		printf 'test-check-secrets: setup subject: %s\n' "$2" >&2
	fi
	return 2
}

# Each step is checked. A printed path is a successful fixture identity.
# Command-substitution callers must test that status; this file does not use set -e.
new_repo() {
	local d="$WORK/$1"
	mkdir -p "$d" || { setup_fail "mkdir" "$1"; return 2; }
	git -C "$d" init -q -b main || { setup_fail "git-init" "$1"; return 2; }
	git -C "$d" config user.email 'battery@example.invalid' || { setup_fail "git-config" "$1"; return 2; }
	git -C "$d" config user.name 'battery' || { setup_fail "git-config" "$1"; return 2; }
	cp "$ROOT/.gitleaks.toml" "$d/.gitleaks.toml" || { setup_fail "copy-config" "$1"; return 2; }
	echo "hello" >"$d/readme.md" || { setup_fail "write-base-file" "$1"; return 2; }
	git -C "$d" add -A || { setup_fail "git-add" "$1"; return 2; }
	git -C "$d" commit -qm "base" || { setup_fail "git-commit" "$1"; return 2; }
	printf '%s' "$d"
}

run_gate() { # run_gate <dir> <outfile> ; echoes the exit code
	local d="$1" out="$2"
	(cd "$d" && bash "$GATE" >"$out" 2>&1)
	echo $?
}

# CFP2: bind intended report bytes to the file the scanner reader actually
# received. Unique invocation path (mktemp), not a reusable stale marker.
# Command substitution must not reuse a sequence counter: mktemp names are
# unique even when this function runs in a subshell. Hashes only; never print
# report bodies. A receipt is written only after a successful copy.
CFP2_LAST_RECEIPT=""

cfp2_sha256() {
	sha256sum "$1" | awk '{print $1}'
}

cfp2_unique_receipt() {
	local rec
	mkdir -p "$WORK/cfp2-delivery" || return 2
	# mktemp creates a unique empty placeholder. The shim treats an existing
	# file as a duplicate, so the placeholder is removed before invocation.
	# That is a checked reset of this unique path, not a reusable marker.
	rec="$(mktemp "$WORK/cfp2-delivery/inv.XXXXXX.receipt")" || return 2
	rm -f "$rec" || return 2
	if [ -e "$rec" ]; then
		return 2
	fi
	printf '%s' "$rec"
}

cfp2_verify_delivery_receipt() {
	local rec="$1" intended="$2" n src_hash dest_hash want
	[ -n "$rec" ] && [ -f "$rec" ] || return 2
	n="$(grep -c '^DELIVERED=1$' "$rec" 2>/dev/null || true)"
	[ "$n" = "1" ] || return 2
	src_hash="$(sed -n 's/^src_sha256=//p' "$rec" | head -n 1)"
	dest_hash="$(sed -n 's/^dest_sha256=//p' "$rec" | head -n 1)"
	[ -n "$src_hash" ] && [ "$src_hash" = "$dest_hash" ] || return 2
	want="$(cfp2_sha256 "$intended")" || return 2
	[ -n "$want" ] && [ "$src_hash" = "$want" ] || return 2
	grep -q '^src=' "$rec" || return 2
	grep -q '^dest=' "$rec" || return 2
	return 0
}

cfp2_no_verdict() {
	! grep -q 'check-secrets: CLEAN' "$1" && ! grep -q 'check-secrets: DIRTY' "$1"
}

cfp2_verify_case5_specimen() {
	# Exact invalid bytes; not a JSON value. Path is argv because this
	# generator is not the 42a python injection target.
	python3 - "$1" <<'PY'
import json, sys
body = open(sys.argv[1], "rb").read()
if body != b"not json at all":
    sys.exit(1)
try:
    json.loads(body.decode("utf-8"))
except Exception:
    sys.exit(0)
sys.exit(2)
PY
}

cfp2_verify_case42a_specimen() {
	# Path in the environment so a 42a.json argv injection cannot satisfy this check.
	CFP2_SPECIMEN="$1" python3 <<'PY'
import json, os, sys
data = json.load(open(os.environ["CFP2_SPECIMEN"], encoding="utf-8"))
if not isinstance(data, list) or len(data) != 1:
    sys.exit(1)
row = data[0]
if not isinstance(row, dict):
    sys.exit(1)
if not isinstance(row.get("Commit"), dict):
    sys.exit(1)
if "oid" not in row["Commit"]:
    sys.exit(1)
sys.exit(0)
PY
}

cfp2_verify_case42b_specimen() {
	# Exact specimen bytes, then the malformed-record intent. Parsed
	# equality alone would accept [true, 2] and [1.0, 2].
	CFP2_SPECIMEN="$1" python3 <<'PY'
import json, os, sys
path = os.environ["CFP2_SPECIMEN"]
body = open(path, "rb").read()
if body != b"[1, 2]":
    sys.exit(1)
data = json.loads(body.decode("utf-8"))
if not isinstance(data, list) or not data:
    sys.exit(1)
if any(isinstance(item, dict) for item in data):
    sys.exit(1)
sys.exit(0)
PY
}

# The scanner shim: copies the intended report to exactly one --report-path,
# then records hashes. It does not write a receipt before a failed copy.
make_report_shim() { # <dir>
	mkdir -p "$1" || { setup_fail "gitleaks-shim-mkdir" "$1"; return 2; }
	cat >"$1/gitleaks" <<'SHIM' || { setup_fail "gitleaks-shim-write" "$1/gitleaks"; return 2; }
#!/usr/bin/env bash
set -u
dest=""
count=0
prev=""
for a in "$@"; do
  if [ "$prev" = "--report-path" ]; then
    dest="$a"
    count=$((count + 1))
  fi
  prev="$a"
done
if [ "$count" -ne 1 ] || [ -z "${dest:-}" ]; then
  echo "report-shim: expected exactly one --report-path destination" >&2
  echo "INF 0 commits scanned."
  exit 2
fi
src="${OLIVARES_TEST_REPORT:-}"
receipt="${OLIVARES_TEST_REPORT_RECEIPT:-}"
if [ -z "$src" ] || [ ! -f "$src" ]; then
  echo "report-shim: missing intended report file" >&2
  echo "INF 0 commits scanned."
  exit 2
fi
if [ -z "$receipt" ]; then
  echo "report-shim: missing delivery receipt path" >&2
  echo "INF 0 commits scanned."
  exit 2
fi
if [ -e "$receipt" ]; then
  echo "report-shim: duplicate delivery receipt" >&2
  echo "INF 0 commits scanned."
  exit 2
fi
if ! cp "$src" "$dest"; then
  echo "report-shim: copy to report destination failed" >&2
  echo "INF 0 commits scanned."
  exit 2
fi
src_hash="$(sha256sum "$src" | awk '{print $1}')"
dest_hash="$(sha256sum "$dest" | awk '{print $1}')"
if [ -z "$src_hash" ] || [ "$src_hash" != "$dest_hash" ]; then
  echo "report-shim: delivered bytes do not match the intended report" >&2
  echo "INF 0 commits scanned."
  exit 2
fi
{
  printf 'DELIVERED=1\n'
  printf 'src=%s\n' "$src"
  printf 'dest=%s\n' "$dest"
  printf 'src_sha256=%s\n' "$src_hash"
  printf 'dest_sha256=%s\n' "$dest_hash"
} >"$receipt" || {
  echo "report-shim: could not write delivery receipt" >&2
  echo "INF 0 commits scanned."
  exit 2
}
echo "INF 3 commits scanned."
exit "${OLIVARES_TEST_GITLEAKS_EXIT:-1}"
SHIM
	chmod +x "$1/gitleaks" || { setup_fail "gitleaks-shim-chmod" "$1/gitleaks"; return 2; }
	if [ ! -x "$1/gitleaks" ]; then
		setup_fail "gitleaks-shim-chmod" "$1/gitleaks"
		return 2
	fi
	return 0
}

run_gate_report() { # <dir> <report.json> <outfile> [extra env assignments...] ; echoes rc
	# Preparation and delivery are checked before a product status is printed.
	# A failed helper does not echo an expected numeric scanner status.
	local d="$1" rep="$2" out="$3" rec rc
	shift 3
	if [ -z "$d" ] || [ ! -d "$d" ]; then
		setup_fail "report-delivery-dir" "${d:-}"
		return 2
	fi
	if [ -z "$rep" ] || [ ! -f "$rep" ]; then
		setup_fail "report-delivery-source" "${rep:-}"
		return 2
	fi
	if [ ! -x "$WORK/report-shim/gitleaks" ]; then
		setup_fail "gitleaks-shim-chmod" "$WORK/report-shim/gitleaks"
		return 2
	fi
	rec="$(cfp2_unique_receipt report)" || {
		setup_fail "report-delivery-receipt" "$rep"
		return 2
	}
	CFP2_LAST_RECEIPT="$rec"
	# Persist the path: command-substitution callers lose shell variables.
	printf '%s' "$rec" >"$WORK/cfp2-delivery/LAST_PATH" || {
		setup_fail "report-delivery-receipt" "$rep"
		return 2
	}
	(
		cd "$d" || exit 2
		env "$@" \
			OLIVARES_TEST_REPORT="$rep" \
			OLIVARES_TEST_REPORT_RECEIPT="$rec" \
			PATH="$WORK/report-shim:$PATH" \
			bash "$GATE" >"$out" 2>&1
	)
	rc=$?
	if ! cfp2_verify_delivery_receipt "$rec" "$rep"; then
		setup_fail "report-delivery-receipt" "$rep"
		return 2
	fi
	printf '%s' "$rc"
	return 0
}

# ── the four 2026-09-06 non-secret captures: helpers shared by cases 13-15 ─────────────
# The private-recovery scan of 2026-09-06 returned four generic-api-key findings on three
# files: the h3n1 token-domain assertion in the sessions handoff test, the two synthetic
# version coordinates the release-version self-test feeds its pin detector, and the SHA-256
# integrity pin of the embedded PUBLIC Claude Code release-signing key. Independent source
# and OpenPGP packet inspection classified all four as non-secret, and .gitleaks.toml closes
# them with three blocks that are rule-specific AND exact-path AND exact-match. Cases 13-15
# pin the SHAPE of that exception — the exact expression on the exact path and nothing
# wider. Every value below is assembled at runtime for the reason plant_decoy gives: a
# literal would be a committed line matching the rule on a path no block names.
# One relative module-fixture directory shared by writers and expected finding identities.
# It is data inside each throwaway repo, not an input read from the root session journal.
fixture_sessions_rel='modules/sessions'
h3n1_ident() { printf '%s%s' 'communicationIncomingHandoff' 'NavigationPrefix'; }
pubkey_digest() { printf '%s%s' 'bd70a5e4a268002704024ceba7f844602' '4114e94f3f0bdd11c23a9e592be81c6'; }
# These are synthetic bundle coordinates, not release pins. Construct the spelling here
# while retaining the exact bytes planted for the scanner and its seven counterexamples.
fixture_coordinate() { printf 'olivares_%s.%s.%s' "$1" "$2" "$3"; }
version_fixture_line() { # <coordinate> <path-arg> — one line of the release-version self-test
	printf "    pins8('const %s = \"%s\"\\\\n', \"%s\")\n" 'key' "$1" "$2"
}
plant_2026_09_06_exact() { # <repo> — the four captures exactly as history carries them
	mkdir -p "$1/$fixture_sessions_rel" "$1/scripts" "$1/cmd/olivares/internal/toolinstall"
	printf 'package sessions\n\n\tif !strings.HasPrefix(%s, %s+".") {\n' 'token' "$(h3n1_ident)" \
		>"$1/$fixture_sessions_rel/communication_handoff_read_test.go"
	{
		echo '#!/usr/bin/env bash'
		version_fixture_line "$(fixture_coordinate 26 9 0)" 'cmd/olivares/cmd_upgrade.go'
		version_fixture_line "$(fixture_coordinate 26 7 0)" 'core/release/x.go'
	} >"$1/scripts/check-release-version.sh"
	printf 'package toolinstall\n\nconst %s = "%s"\n' 'claudeReleaseKeySHA256' "$(pubkey_digest)" \
		>"$1/cmd/olivares/internal/toolinstall/claude_key.go"
}
plant_2026_09_06_controls() { # <repo> — SEVEN captures the three blocks must NOT cover
	local cred hexother
	cred='V7q2Hs8Nx4Rt6Yw1''Bz5Kd9Mg3Pf0Lc'
	hexother='0f1e2d3c4b5a69788796a5b4c3d2e1f0''f0e1d2c3b4a5968778695a4b3c2d1e0f'
	mkdir -p "$1/$fixture_sessions_rel" "$1/scripts" "$1/cmd/olivares/internal/toolinstall"
	# same path, a credential-shaped value where the identifier was
	printf 'package sessions\n\n\tif !strings.HasPrefix(%s, "%s") {\n' 'token' "$cred" \
		>"$1/$fixture_sessions_rel/communication_handoff_read_test.go"
	# the same identifier expression, another path
	printf 'package sessions\n\n\tif !strings.HasPrefix(%s, %s+".") {\n' 'token' "$(h3n1_ident)" \
		>"$1/$fixture_sessions_rel/other_test.go"
	# same path, a coordinate no block names
	{ echo '#!/usr/bin/env bash'; version_fixture_line "$(fixture_coordinate 26 9 1)" 'x.go'; } \
		>"$1/scripts/check-release-version.sh"
	# the named coordinate, another path
	{ echo '#!/usr/bin/env bash'; version_fixture_line "$(fixture_coordinate 26 9 0)" 'x.go'; } \
		>"$1/scripts/other-script.sh"
	# same path: the named constant with a DIFFERENT digest, and the named digest under another name
	{
		printf 'package toolinstall\n\nconst %s = "%s"\n' 'claudeReleaseKeySHA256' "$hexother"
		printf 'var %s = "%s"\n' 'apiKey' "$(pubkey_digest)"
	} >"$1/cmd/olivares/internal/toolinstall/claude_key.go"
	# the exact constant, another path
	printf 'package toolinstall\n\nconst %s = "%s"\n' 'claudeReleaseKeySHA256' "$(pubkey_digest)" \
		>"$1/cmd/olivares/internal/toolinstall/other.go"
}

# Sourced by the owned setup-suite child to reuse helpers. Direct execution
# continues into the battery.
if [ "${BASH_SOURCE[0]}" != "$0" ]; then
	return 0
fi

echo "test-check-secrets: battery"

# ── 1 · clean repository answers CLEAN, exit 0 ────────────────────────────────────────
d="$(new_repo clean)" || exit 2
out="$WORK/1.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "0" ]; then
	fail "1 clean repo -> exit 0" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
elif ! grep -q "CLEAN" "$out"; then
	fail "1 clean repo says CLEAN" "exit 0 but the word CLEAN is not in the output"
else
	pass "1 clean repo -> exit 0 and says CLEAN"
fi

# ── 2 · a finding in the merged history is NAMED, not merely counted ──────────────────
d="$(new_repo dirty)" || exit 2
plant_decoy "$d/deploy.pem"
git -C "$d" add -A
git -C "$d" commit -qm "adds the decoy"
sha="$(git -C "$d" rev-parse HEAD)"
out="$WORK/2.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "2 finding -> exit 1" "got exit $rc"
else
	missing=""
	grep -q "private-key" "$out" || missing="$missing rule"
	grep -q "deploy.pem" "$out" || missing="$missing file"
	grep -q "${sha:0:12}" "$out" || missing="$missing commit"
	grep -q "IN what you are merging" "$out" || missing="$missing reachability"
	if [ -n "$missing" ]; then
		fail "2 finding is NAMED" "the output never says:$missing — this is the '\''leaks found: 1'\'' defect"
	else
		pass "2 finding -> exit 1, and names rule + file + commit + reachability"
	fi
fi

# ── 3 · a missing config is COULD NOT LOOK (2), never a finding (1) ───────────────────
# This is the whole point of the wrapper: gitleaks answers 1 to BOTH, measured 2026-08-04.
d="$(new_repo noconfig)" || exit 2
rm -f "$d/.gitleaks.toml"
out="$WORK/3.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" = "1" ]; then
	fail "3 missing config -> exit 2, not 1" "got exit 1: the gate is reporting 'could not look' as a finding"
elif [ "$rc" != "2" ]; then
	fail "3 missing config -> exit 2" "got exit $rc"
elif ! grep -q "COULD NOT LOOK" "$out"; then
	fail "3 missing config says COULD NOT LOOK" "exit 2 but the phrase is absent"
else
	pass "3 missing config -> exit 2 and says COULD NOT LOOK"
fi

# ── 4 · a finding NOT reachable from HEAD is called out as such ───────────────────────
# The 2026-08-04 case, reproduced: the object exists in the clone, on another ref, and is
# not part of what you would merge. A gate that cannot say this accuses the wrong branch.
d="$(new_repo stale-ref)" || exit 2
git -C "$d" checkout -q -b abandoned
plant_decoy "$d/leftover.pem"
git -C "$d" add -A
git -C "$d" commit -qm "a branch nobody is merging"
git -C "$d" checkout -q main
out="$WORK/4.out"
# El caso ejercita el MODO BARRIDO (--all-refs), que es el que mira todos los refs. El gate de push
# va deliberadamente acotado a HEAD: medido el 2026-08-16, el barrido cuesta 345 s contra 239 s y
# añade 9 hallazgos de los buzones en refs/remotes/origin/status* — nueve avisos por push y por
# carril sobre los que nadie actuaría. Dos modos, no un compromiso.
rc="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$out")"
# ⛔ ESPERA 0, NO 1, DESDE EL 2026-08-16 — y el cambio es de VEREDICTO, no de vista. El hallazgo se
# sigue encontrando y se sigue NOMBRANDO con su ref (las dos comprobaciones de abajo no se tocan);
# lo que cambia es que no se COBRA a un push que no lo introduce. Medido ese día: el checkout del
# runner self-hosted persiste entre jobs, así que `main` salia roja por 12 hallazgos de la rama de
# otro PR — 834 de 6.284 commits escaneados no pertenecian al ref. El informe ya decia "NOT
# reachable from HEAD" mientras el gate fallaba igual: sabia la respuesta y no la usaba.
if [ "$rc" != "0" ]; then
	fail "4 finding on another ref -> exit 0" "got exit $rc (no lo introduce este push: se informa, no se cobra)"
elif ! grep -q "NOT reachable from HEAD" "$out"; then
	fail "4 names it as unreachable" "the gate found it but never says it is outside what you are merging"
elif ! grep -q "abandoned" "$out"; then
	fail "4 names the ref carrying it" "it says unreachable but does not say which ref carries the commit"
else
	pass "4 finding on another ref -> exit 0, named as NOT reachable, with the ref"
fi

# ── 5 · an unreadable report is COULD NOT LOOK, never CLEAN ───────────────────────────
# Simulated with a gitleaks shim that exits 0 and writes garbage where the report goes: the
# fail-open shape this gate exists to forbid. Preparation, delivery hashes and the exact
# parse cause are required; an empty leftover report file is also "not a JSON array".
d="$(new_repo badreport)" || exit 2
printf 'not json at all' >"$WORK/5.intended" || {
	setup_fail "write-malformed-report-5" "$WORK/5.intended"
	exit 2
}
cfp2_verify_case5_specimen "$WORK/5.intended" || {
	setup_fail "malformed-report-5-shape" "$WORK/5.intended"
	exit 2
}
make_report_shim "$WORK/report-shim" || exit 2
out="$WORK/5.out"
rc="$(run_gate_report "$d" "$WORK/5.intended" "$out" OLIVARES_TEST_GITLEAKS_EXIT=0)" || exit 2
if [ "$rc" = "0" ]; then
	fail "5 unreadable report -> exit 2, not 0" "got exit 0: the gate called an unparseable report CLEAN"
elif [ "$rc" != "2" ]; then
	fail "5 unreadable report -> exit 2" "got exit $rc"
elif ! grep -q 'COULD NOT LOOK' "$out"; then
	fail "5 unreadable report says COULD NOT LOOK" "exit 2 but the phrase is absent"
elif ! grep -q 'is not a JSON array' "$out"; then
	fail "5 unreadable report names is not a JSON array" "exit 2 but the intended parse cause is absent"
elif ! cfp2_no_verdict "$out"; then
	fail "5 unreadable report is not a verdict" "a CLEAN or DIRTY line was printed"
else
	pass "5 unreadable report -> exit 2, is not a JSON array, not a verdict"
fi

# ── 6 · the printed finding never carries the secret ──────────────────────────────────
# --redact is what makes naming a finding safe. If it is dropped, the gate publishes in the
# CI log exactly the thing it is defending. The decoy body is the canary.
d="$(new_repo redaction)" || exit 2
plant_decoy "$d/creds.pem"
git -C "$d" add -A
git -C "$d" commit -qm "decoy for the redaction check"
out="$WORK/6.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "6 redaction case finds the decoy" "got exit $rc, expected 1"
elif grep -q "NOTAREALKEYnotarealkey" "$out"; then
	fail "6 output is redacted" "the decoy's body appears in the output — --redact is not in effect"
else
	pass "6 the named finding is redacted (the secret never reaches the log)"
fi

# ── 7 · the branch's config differs from the base's, and the gate SAYS SO ─────────────
# THE ACTUAL 2026-08-04 CAUSE. gitleaks reads .gitleaks.toml out of the checkout, so a branch
# that has not rebased is judged by its own older rules: an exception added on the base does
# not exist for it. Ten clean scans were run against the base's config while the job used the
# branch's, and nothing in any output ever mentioned the config. This case is that line.
d="$(new_repo config-lag)" || exit 2
git -C "$d" checkout -q -b base-branch
printf '\n[[allowlist.regexes]]\n# added on the base only\nregex = "does-not-matter"\n' >>"$d/.gitleaks.toml"
git -C "$d" add -A
git -C "$d" commit -qm "base gains an exception the branch never sees"
git -C "$d" checkout -q main
out="$WORK/7.out"
rc="$( (cd "$d" && OLIVARES_SECRETS_BASE_REF=base-branch bash "$GATE" >"$out" 2>&1); echo $?)"
if ! grep -q "DIFFERS from base-branch" "$out"; then
	fail "7 says the config differs from the base" "the gate never mentions that it is applying a different .gitleaks.toml than the base (exit $rc)"
elif ! grep -q "sha256=" "$out"; then
	fail "7 identifies the config it used" "no config hash in the output"
else
	pass "7 config lag -> the gate names it, so a lag is not read as a finding"
fi

# ── 8 · no base to compare against is COULD NOT COMPARE, never silence ────────────────
d="$(new_repo no-base)" || exit 2
out="$WORK/8.out"
rc="$( (cd "$d" && OLIVARES_SECRETS_BASE_REF=refs/heads/definitely-not-here bash "$GATE" >"$out" 2>&1); echo $?)"
if ! grep -q "COULD NOT COMPARE" "$out"; then
	fail "8 unresolvable base is stated" "the gate was asked for a base that does not exist and said nothing (exit $rc)"
elif grep -q "DIFFERS from" "$out"; then
	fail "8 does not fall back silently" "it answered about a DIFFERENT base than the one requested"
else
	pass "8 unresolvable base -> says COULD NOT COMPARE, and does not silently pick another"
fi

# ── 9 · A-04: a private key in *_test.go is a FINDING, not a path exemption ───────────
# 2026-08-06 licencias-sweep-2 A-04: `.*_test.go$` exempted the entire detector.
# The same ephemeral RSA was findings=0 in a test file and findings=1 in a
# production path. The canary plants the decoy under the name that used to hide
# it. A mutant that restores the path exemption makes THIS case go green.
d="$(new_repo a04-testgo)" || exit 2
plant_decoy "$d/probe_test.go"
git -C "$d" add -A
git -C "$d" commit -qm "ephemeral RSA under a _test.go name"
out="$WORK/9.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "9 private key in _test.go -> exit 1" "got exit $rc — the path still exempts the detector (A-04)"
elif ! grep -q "probe_test.go" "$out"; then
	fail "9 names the _test.go file" "exit 1 but probe_test.go is not in the output"
else
	pass "9 private key in _test.go -> exit 1 (path no longer exempts the detector)"
fi

# ── 10 · A-04: a private key in testdata/ is a FINDING, not a path exemption ──────────
d="$(new_repo a04-testdata)" || exit 2
mkdir -p "$d/testdata"
plant_decoy "$d/testdata/probe.pem"
git -C "$d" add -A
git -C "$d" commit -qm "ephemeral RSA under testdata/"
out="$WORK/10.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "10 private key in testdata/ -> exit 1" "got exit $rc — testdata still exempts the detector (A-04)"
elif ! grep -q "testdata/probe.pem" "$out"; then
	fail "10 names the testdata file" "exit 1 but testdata/probe.pem is not in the output"
else
	pass "10 private key in testdata/ -> exit 1 (path no longer exempts the detector)"
fi

# ── 11 · a credential in a TRANSLATED README is a FINDING ────────────────────
# 2026-08-29. Both exist because a class-shaped allowlist — path
# `README\.[a-z]{2}\.md` plus the shape of a German compound — was written for
# this gate and RETIRED before landing when a contrast measured it instead of
# reading it.
#
# ⛔ WHICH OF THE TWO ACTUALLY WITNESSES AGAINST THAT CLASS, measured by putting
# the retired entry back: case 12 goes RED, case 11 stays GREEN. Case 11 is NOT
# a witness here and its name must not claim otherwise. The reason is worth
# keeping: in the mode this gate runs — over history — `condition = "AND"`
# composes correctly, so an `api_key = "..."` whose match does not have the
# `API <Compound>` shape fails the AND and is reported anyway. The class entry's
# third defect (in `--no-git`/`dir`, `paths` acts as a PRE-FILTER and the AND
# never applies, so that credential vanishes) is real but belongs to a mode this
# gate does not run, so it cannot be pinned from here. Case 11 stays as a plain
# regression guard: a credential in a translated README must be reported.
#
# The decoy values are ASSEMBLED AT RUNTIME for the reason plant_decoy already
# documents: a literal here would be a committed line matching the rule, and a
# full-history scan does not care that the string lives in a test.
d="$(new_repo readme-cred)" || exit 2
{
	echo "# Doc"
	printf '%s%s = "%s%s"\n' 'api' '_key' 'V7q2Hs8Nx4Rt6Yw1' 'Bz5Kd9Mg3Pf0Lc'
} >"$d/README.es.md"
git -C "$d" add -A && git -C "$d" commit -qm "translated readme with a credential"
out="$WORK/11.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "11 credential in README.es.md -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
elif ! grep -q "README.es.md" "$out"; then
	fail "11 names README.es.md" "exit 1 but README.es.md is not in the output"
else
	pass "11 credential in a translated README -> exit 1 (regression guard; case 12 is the class witness)"
fi

# ── 12 · a namespaced token after the API keyword is a FINDING ───────────────
# The exact shape the retired class regex could not tell apart from prose:
# there is no reliable syntactic boundary between "long compound word" and
# "token with a namespace and a hyphen", which is why the exception that
# shipped is the EXACT historical one and not a class.
d="$(new_repo readme-nstoken)" || exit 2
{
	echo "# Doc"
	printf 'Service access via %s, %s%s as the identifier.\n' 'API' 'PROD-Zk93Qv7Lm2' 'XpR8dTn4Wb'
} >"$d/README.fr.md"
git -C "$d" add -A && git -C "$d" commit -qm "translated readme with a namespaced token"
out="$WORK/12.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "12 namespaced token in README.fr.md -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
elif ! grep -q "README.fr.md" "$out"; then
	fail "12 names README.fr.md" "exit 1 but README.fr.md is not in the output"
else
	pass "12 API-adjacent namespaced token -> exit 1 (the class regex would have hidden it)"
fi

# ── 13 · the four 2026-09-06 captures are exempt EXACTLY, and it is their blocks that do it ──
# Half a: the exact lines on the exact paths answer CLEAN. Half b, the no-fire control: the
# SAME specimen judged by a copy of the config with the three blocks cut off is DIRTY with
# exactly four findings — so the specimen does fire, and the blocks are what exempt it. A
# case that only showed half a would pass just as well if the specimen had a typo the rule
# never matched.
d="$(new_repo fp-2026-09-06-exact)" || exit 2
plant_2026_09_06_exact "$d"
git -C "$d" add -A
git -C "$d" commit -qm "the four classified non-secret captures, on their exact paths"
out="$WORK/13a.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "0" ]; then
	fail "13a exact captures on exact paths -> exit 0" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 400)"
elif ! grep -q "CLEAN" "$out"; then
	fail "13a says CLEAN" "exit 0 but the word CLEAN is not in the output"
else
	pass "13a the four 2026-09-06 captures on their exact paths -> exit 0 (exempt)"
fi
d="$(new_repo fp-2026-09-06-nofire)" || exit 2
plant_2026_09_06_exact "$d"
cut_rc=0
python3 - "$d/.gitleaks.toml" <<'PY' || cut_rc=$?
import pathlib, sys
p = pathlib.Path(sys.argv[1]); t = p.read_text(encoding='utf-8')
i = t.find('# ── 2026-09-06 · FOUR historical generic-api-key findings')
if i < 0:
    sys.exit(3)
p.write_text(t[:i], encoding='utf-8')
PY
if [ "$cut_rc" != "0" ]; then
	fail "13b could cut the three blocks out of the copied config" "marker not found (rc $cut_rc) — the no-fire control cannot run, and that is not a pass"
else
	git -C "$d" add -A
	git -C "$d" commit -qm "the same specimen, judged without the three blocks"
	out="$WORK/13b.out"
	rc="$(run_gate "$d" "$out")"
	if [ "$rc" != "1" ]; then
		fail "13b without the blocks -> exit 1" "got exit $rc — the specimen never fired, so 13a proves nothing"
	elif ! grep -q " 4 finding(s)" "$out"; then
		fail "13b without the blocks -> exactly 4 findings" "output: $(grep -o 'DIRTY.*' "$out" | head -1)"
	else
		pass "13b the same specimen without the blocks -> exit 1 with exactly 4 findings (no-fire control)"
	fi
fi

# ── 14 · exact means EXACT: a changed value, or the same value on another path, is a FINDING ──
# Seven captures on the same three files and their neighbours: a credential-shaped value
# where the identifier was, a coordinate no block names, a different digest under the named
# constant, the named digest under another name, and each exact expression on a path the
# block does not name. A block that had widened to a path, a shape or a rule would swallow
# at least one of them.
d="$(new_repo fp-2026-09-06-controls)" || exit 2
plant_2026_09_06_controls "$d"
git -C "$d" add -A
git -C "$d" commit -qm "seven captures the exact blocks must not cover"
out="$WORK/14.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "14 controls beside the exact captures -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 400)"
elif ! grep -q " 7 finding(s)" "$out"; then
	fail "14 all seven controls are findings" "output: $(grep -o 'DIRTY.*' "$out" | head -1) — a block swallowed a neighbour"
else
	missing=""
	for f in "$fixture_sessions_rel/communication_handoff_read_test.go" "$fixture_sessions_rel/other_test.go" \
		scripts/check-release-version.sh scripts/other-script.sh \
		cmd/olivares/internal/toolinstall/claude_key.go cmd/olivares/internal/toolinstall/other.go; do
		grep -q "$f" "$out" || missing="$missing $f"
	done
	if [ -n "$missing" ]; then
		fail "14 names every file carrying a control" "never named:$missing"
	else
		pass "14 changed value / other path on the same three files -> exit 1, all seven named"
	fi
fi

# ── 15 · another rule on the same three paths is a FINDING ──────────────────────────────
# The blocks name ONE rule. A private key planted on each of the three paths must still be
# reported by its own rule; a whole-file path exemption would silence it. What this case
# does NOT see, measured: dropping `targetRules`. Over history a global AND block with
# regexes is still evaluated per finding, so these three private keys survive that mutant
# here. The mode where they do not survive it is `dir`, and that is case 16.
d="$(new_repo fp-2026-09-06-other-rule)" || exit 2
mkdir -p "$d/$fixture_sessions_rel" "$d/scripts" "$d/cmd/olivares/internal/toolinstall"
plant_decoy "$d/$fixture_sessions_rel/communication_handoff_read_test.go"
plant_decoy "$d/scripts/check-release-version.sh"
plant_decoy "$d/cmd/olivares/internal/toolinstall/claude_key.go"
git -C "$d" add -A
git -C "$d" commit -qm "a private key on each of the three exempted paths"
out="$WORK/15.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "15 private key on the three paths -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 400)"
elif ! grep -q " 3 finding(s)" "$out" || ! grep -q "private-key" "$out"; then
	fail "15 three private-key findings" "output: $(grep -o 'DIRTY.*' "$out" | head -1)"
else
	pass "15 another rule on the same three paths -> exit 1, three private-key findings"
fi

# ── 16 · the DIR-mode witness: the blocks are RULE allowlists, never a whole-file skip ────
# gitleaks has two modes with two different pre-filters. Over history — the mode this gate
# runs — a global AND block with regexes is evaluated per finding, so dropping `targetRules`
# from the three blocks changes nothing that cases 13-15 can see (measured 2026-09-06: M14
# left them 15/15 green, found by the independent review). In `dir`/`--no-git` mode
# sources/common.go (gitleaks v8.30.1) skips a whole file by path for every GLOBAL block,
# before any regex runs and regardless of `condition`. That is the mode `gitleaks dir` and a
# downstream `--no-git` sweep use, and it is where a dropped `targetRules` would silence every
# credential and every private key on the three paths.
#
# So this case runs the scanner in that mode ITSELF, against the same config the gate reads,
# and requires exact identities rather than a count: on each of the three paths one
# generic-api-key (a credential-shaped value) and one private-key stay, the four exact
# captures do not, and one exact capture on an unrelated path stays as the witness that the
# detector ran at all. Under M14 the six same-path findings disappear and only the witness
# remains — the case then names what vanished. A missing or unparseable report is reported
# as COULD NOT SCAN, distinctly, never as a pass. The specimen is the reviewer's synthetic
# fixture of 2026-09-06, rebuilt at runtime.
append_dir_negatives() { # <file> — a credential-shaped value and a fake private key AFTER the capture
	local head="-----BEGIN RSA PRI""VATE KEY-----"
	local tail="-----END RSA PRI""VATE KEY-----"
	{
		printf '%s%s = "%s%s"\n' 'api' '_key' 'V7q2Hs8Nx4Rt6Yw1' 'Bz5Kd9Mg3Pf0Lc'
		echo "$head"
		echo "NOTAREALKEYnotarealkeyNOTAREALKEYnotarealkey"
		echo "$tail"
	} >>"$1"
}
d="$(new_repo fp-2026-09-06-dir-mode)" || exit 2
plant_2026_09_06_exact "$d"
for f in "$fixture_sessions_rel/communication_handoff_read_test.go" scripts/check-release-version.sh \
	cmd/olivares/internal/toolinstall/claude_key.go; do
	append_dir_negatives "$d/$f"
done
printf 'package toolinstall\n\nconst %s = "%s"\n' 'claudeReleaseKeySHA256' "$(pubkey_digest)" \
	>"$d/cmd/olivares/internal/toolinstall/other.go"
report="$WORK/16.json"
out="$WORK/16.out"
rc="$( (cd "$d" && gitleaks dir . --no-banner --redact --no-color -c "$d/.gitleaks.toml" \
	--report-format json --report-path "$report" >"$out" 2>&1); echo $?)"
got="$(python3 - "$report" <<'PY' 2>/dev/null
import json, sys
try:
    with open(sys.argv[1], encoding='utf-8') as fh:
        data = json.load(fh)
except Exception:
    sys.exit(3)
if not isinstance(data, list):
    sys.exit(3)
for f in data:
    path = str(f.get('File', ''))
    if path.startswith('./'):
        path = path[2:]
    print(f"{f.get('RuleID', '')}@{path}")
PY
)"
pyrc=$?
want="generic-api-key@cmd/olivares/internal/toolinstall/claude_key.go
generic-api-key@cmd/olivares/internal/toolinstall/other.go
generic-api-key@${fixture_sessions_rel}/communication_handoff_read_test.go
generic-api-key@scripts/check-release-version.sh
private-key@cmd/olivares/internal/toolinstall/claude_key.go
private-key@${fixture_sessions_rel}/communication_handoff_read_test.go
private-key@scripts/check-release-version.sh"
if [ "$pyrc" != "0" ]; then
	fail "16 COULD NOT SCAN in dir mode" "gitleaks dir exited $rc and left no parseable report — this is not a pass; output: $(tr '\n' ' ' <"$out" | head -c 300)"
elif [ "$rc" != "1" ]; then
	fail "16 dir-mode scan reports findings -> exit 1" "got exit $rc with report lines: $(printf '%s' "$got" | tr '\n' ' ')"
elif [ "$(printf '%s\n' "$got" | sort)" != "$want" ]; then
	vanished="$(comm -23 <(printf '%s\n' "$want") <(printf '%s\n' "$got" | sort) | tr '\n' ' ')"
	extra="$(comm -13 <(printf '%s\n' "$want") <(printf '%s\n' "$got" | sort) | tr '\n' ' ')"
	fail "16 dir mode keeps exactly the six same-path findings plus the witness" "vanished:${vanished:- none} extra:${extra:- none} — private keys gone means a whole-file path skip (targetRules dropped); the witness gone means path or condition widened; extras mean the blocks no longer match their captures"
else
	pass "16 dir mode: exact captures exempt, six same-path credential/private-key findings and the witness remain"
fi


# 17: actual current classifier source is clean in a fresh Git history. No commit
# fingerprint or renamed product state is needed to clear these two captures.
evidence_rel='core/internal/store/sqlstore/access_evidence_migration.go'
if [ ! -r "$ROOT/$evidence_rel" ]; then
	setup_fail "missing-classifier-source" "$evidence_rel"
	exit 2
fi
d="$(new_repo state-labels-exact)" || exit 2
mkdir -p "$d/$(dirname "$evidence_rel")" || { setup_fail "classifier-mkdir" "$evidence_rel"; exit 2; }
cp "$ROOT/$evidence_rel" "$d/$evidence_rel" || { setup_fail "classifier-copy" "$evidence_rel"; exit 2; }
git -C "$d" add -A || { setup_fail "classifier-add" "$evidence_rel"; exit 2; }
git -C "$d" commit -qm "current classifier in independent history" || { setup_fail "classifier-commit" "$evidence_rel"; exit 2; }
git -C "$d" cat-file blob "HEAD:$evidence_rel" >"$WORK/17.blob" || { setup_fail "classifier-blob" "$evidence_rel"; exit 2; }
cmp -s "$ROOT/$evidence_rel" "$WORK/17.blob" || { setup_fail "classifier-blob" "$evidence_rel"; exit 2; }
out="$WORK/17.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" = "0" ] && grep -q 'CLEAN' "$out"; then
	pass "17 exact current classifier states are clean in fresh history"
else
	fail "17 exact current classifier states are clean" "got exit $rc"
fi

# 18: a retained value under another identifier, a changed value in the same
# declaration, the original expression on another path, and another rule on the
# original path all remain findings. Run both scanner modes: a global allowlist
# can hide the whole file in dir mode even if a history-mode control passes.
other_rel='core/internal/store/sqlstore/other.go'
cp "$d/$evidence_rel" "$d/$other_rel" || { setup_fail "case18-copy" "$other_rel"; exit 2; }
{
	printf '\nconst %s = "%s%s"\n' 'api_key' 'direct-v6-' 'pending-v7'
	printf '\n%s %s = "%s%s"\n' 'accessEvidenceStartOther' \
		'accessEvidenceStartClass' 'direct-v6-' 'pending-v7'
	printf '\n%s %s = "%s%s"\n' 'accessEvidenceStartDirectPendingV7' \
		'accessEvidenceStartClass' 'V7q2Hs8Nx4Rt6Yw1' 'Bz5Kd9Mg3Pf0Lc'
} >>"$d/$evidence_rel" || { setup_fail "case18-write" "$evidence_rel"; exit 2; }
plant_decoy "$WORK/state-label-decoy"
if [ ! -s "$WORK/state-label-decoy" ]; then
	setup_fail "case18-write" "decoy"
	exit 2
fi
cat "$WORK/state-label-decoy" >>"$d/$evidence_rel" || { setup_fail "case18-write" "$evidence_rel"; exit 2; }
git -C "$d" add -A || { setup_fail "case18-add" "$evidence_rel"; exit 2; }
git -C "$d" commit -qm "detector controls on the classifier path and another path" || { setup_fail "case18-commit" "$evidence_rel"; exit 2; }
for mode in history dir; do
	report="$WORK/18-$mode.json"
	out="$WORK/18-$mode.out"
	if [ "$mode" = history ]; then
		rc="$( (cd "$d" && gitleaks detect --log-opts=HEAD --no-banner --redact --no-color \
			-c "$d/.gitleaks.toml" --report-format json --report-path "$report" >"$out" 2>&1); echo $?)"
	else
		rc="$( (cd "$d" && gitleaks dir . --no-banner --redact --no-color \
			-c "$d/.gitleaks.toml" --report-format json --report-path "$report" >"$out" 2>&1); echo $?)"
	fi
	if [ "$rc" != "1" ]; then
		fail "18 $mode detector controls remain findings" "got exit $rc"
	elif python3 - "$report" "$evidence_rel" "$other_rel" <<'PYCONTROL'
import collections, json, sys
try:
    data = json.load(open(sys.argv[1], encoding='utf-8'))
    actual = collections.Counter((row['RuleID'], row['File'].removeprefix('./')) for row in data)
except (OSError, ValueError, TypeError, KeyError):
    print('no parseable finding identities')
    raise SystemExit(1)
expected = collections.Counter({
    ('generic-api-key', sys.argv[2]): 3,
    ('private-key', sys.argv[2]): 1,
    ('generic-api-key', sys.argv[3]): 2,
})
if actual != expected:
    print('missing:', sorted((expected-actual).items()))
    print('extra:', sorted((actual-expected).items()))
    raise SystemExit(1)
PYCONTROL
	then
		pass "18 $mode keeps six exact same-path/other-path detector controls"
	else
		fail "18 $mode keeps all detector identities" "finding set differs; no credential values printed"
	fi
done

# ── the 2026-09-08 pre-verify diagnostic canary: helpers shared by cases 19-24 ─────────────
# The R18 secrets job named exactly ONE finding reachable from HEAD: `github-pat` on the
# `SENUELO_DIAG` line of scripts/test-pre-verify-tanda.sh — a single-quoted, PAT-shaped
# literal that the pre-verify self-test feeds its diagnostic redactor and asserts is ABSENT
# from the redactor's output. Triaged as a synthetic fixture and ratified as such by root;
# .gitleaks.toml closes it with ONE block that is rule-specific AND exact-path AND
# exact-captured-Secret, deliberately without a commit pin (the line is live at HEAD, was
# cherry-picked from another ref, and the export re-SHAs it). Cases 19-24 pin that SHAPE:
# the exact capture on the exact path under the one rule, and nothing wider.
#
# The literal is ASSEMBLED FROM FRAGMENTS, for the reason plant_decoy gives: written whole,
# it would be a committed `ghp_` + 36 line on a path the block does not name, and this file
# would be the next finding. What the detector captures is `ghp_` plus the FIRST 36 of the
# 39 that follow — measured with gitleaks v8.30.1 on a disposable repository: a 40-byte
# Secret at columns 15-54 of the 58-byte line — so the fixture's trailing three bytes ride
# along in every specimen and never in the capture. The near miss changes byte 40, the last
# byte of the capture, and nothing else.
fixture_tanda_rel='scripts/test-pre-verify-tanda.sh'
senuelo_diag_literal() { printf '%s%s%s%s' 'ghp_' 'SENUELOdelDIA' 'GNOSTICOqueno' 'debeSALIR0009'; }
senuelo_diag_line() { printf "SENUELO_DIAG='%s'\n" "$(senuelo_diag_literal)"; } # the 58-byte line history carries
senuelo_diag_near_miss_line() { # the same line with the LAST byte of the capture changed
	local lit repl=9
	lit="$(senuelo_diag_literal)"
	[ "${lit:39:1}" != 9 ] || repl=8
	printf "SENUELO_DIAG='%s%s%s'\n" "${lit:0:39}" "$repl" "${lit:40}"
}
other_pat_line() { printf "OTRO_SENUELO='%s%s%s'\n" 'ghp_' 'Zk93Qv7Lm2XpR8dTn4Wb' 'Hs8Nx4Rt6Yw1Bz5K'; } # a different ghp_ + 36
plant_tanda() { # <repo> <relpath> <line-producer>... — a shebang followed by the produced lines
	local d="$1" rel="$2" fn
	shift 2
	mkdir -p "$d/$(dirname "$rel")"
	{
		echo '#!/usr/bin/env bash'
		for fn in "$@"; do "$fn"; done
	} >"$d/$rel"
}

# ── 19 · the exact canary line on its exact path is exempt, and it is the block that does it ──
# Half a: the line history carries, on the path the block names, answers CLEAN. Half b, the
# no-fire control: the SAME specimen judged by a copy of the config with the block cut off is
# DIRTY with exactly one github-pat on that path — so the specimen does fire, and the block is
# what exempts it. Half a alone would pass just as well if the fragments had a typo.
d="$(new_repo canary-exact)" || exit 2
plant_tanda "$d" "$fixture_tanda_rel" senuelo_diag_line
git -C "$d" add -A
git -C "$d" commit -qm "the pre-verify canary line on its exact path"
out="$WORK/19a.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "0" ]; then
	fail "19a exact canary line on its path -> exit 0" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 400)"
elif ! grep -q "CLEAN" "$out"; then
	fail "19a says CLEAN" "exit 0 but the word CLEAN is not in the output"
else
	pass "19a the exact SENUELO_DIAG line on scripts/test-pre-verify-tanda.sh -> exit 0 (exempt)"
fi
d="$(new_repo canary-nofire)" || exit 2
plant_tanda "$d" "$fixture_tanda_rel" senuelo_diag_line
cut_rc=0
python3 - "$d/.gitleaks.toml" <<'PY' || cut_rc=$?
import pathlib, sys
p = pathlib.Path(sys.argv[1]); t = p.read_text(encoding='utf-8')
i = t.find('# ── 2026-09-08 · the pre-verify diagnostic canary')
if i < 0:
    sys.exit(3)
p.write_text(t[:i], encoding='utf-8')
PY
if [ "$cut_rc" != "0" ]; then
	fail "19b could cut the canary block out of the copied config" "marker not found (rc $cut_rc) — the no-fire control cannot run, and that is not a pass"
else
	git -C "$d" add -A
	git -C "$d" commit -qm "the same specimen, judged without the block"
	out="$WORK/19b.out"
	rc="$(run_gate "$d" "$out")"
	if [ "$rc" != "1" ]; then
		fail "19b without the block -> exit 1" "got exit $rc — the specimen never fired, so 19a proves nothing"
	elif ! grep -q " 1 finding(s)" "$out" || ! grep -q "github-pat" "$out" || ! grep -q "$fixture_tanda_rel" "$out"; then
		fail "19b without the block -> exactly one github-pat on that path" "output: $(grep -o 'DIRTY.*' "$out" | head -1)"
	else
		pass "19b the same specimen without the block -> exit 1, one github-pat on that path (no-fire control)"
	fi
fi

# ── 20 · a DIFFERENT ghp_ + 36 on the same path is a FINDING ─────────────────────────────
# The block names one value, not a shape. A rule-shaped regex (M19) swallows this one.
d="$(new_repo canary-other-pat)" || exit 2
plant_tanda "$d" "$fixture_tanda_rel" other_pat_line
git -C "$d" add -A
git -C "$d" commit -qm "a different PAT-shaped value on the exempted path"
out="$WORK/20.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "20 different PAT on the same path -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 400)"
elif ! grep -q " 1 finding(s)" "$out" || ! grep -q "github-pat" "$out" || ! grep -q "$fixture_tanda_rel" "$out"; then
	fail "20 one github-pat on that path" "output: $(grep -o 'DIRTY.*' "$out" | head -1)"
else
	pass "20 a different ghp_ + 36 on the same path -> exit 1, github-pat named"
fi

# ── 21 · the capture ONE BYTE apart on the same path is a FINDING ────────────────────────
# Exact means exact to the last byte: this is the only control that sees a regex which lost
# its `$` anchor (M20) — a different PAT (case 20) is still reported under that mutant.
d="$(new_repo canary-near-miss)" || exit 2
plant_tanda "$d" "$fixture_tanda_rel" senuelo_diag_near_miss_line
git -C "$d" add -A
git -C "$d" commit -qm "the capture with its last byte changed, on the exempted path"
out="$WORK/21.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "21 near-miss capture on the same path -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 400)"
elif ! grep -q " 1 finding(s)" "$out" || ! grep -q "github-pat" "$out" || ! grep -q "$fixture_tanda_rel" "$out"; then
	fail "21 one github-pat on that path" "output: $(grep -o 'DIRTY.*' "$out" | head -1)"
else
	pass "21 the capture one byte apart on the same path -> exit 1, github-pat named"
fi

# ── 22 · the exact capture on ANOTHER path is a FINDING ──────────────────────────────────
# The block names one file. Without `paths` (M16), or with `OR` (M18), these bytes would be
# exempt everywhere — the A-04 global shape this block deliberately is not.
d="$(new_repo canary-other-path)" || exit 2
plant_tanda "$d" scripts/other-script.sh senuelo_diag_line
git -C "$d" add -A
git -C "$d" commit -qm "the exact canary line on a path the block does not name"
out="$WORK/22.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "22 exact capture on another path -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 400)"
elif ! grep -q " 1 finding(s)" "$out" || ! grep -q "github-pat" "$out" || ! grep -q "scripts/other-script.sh" "$out"; then
	fail "22 one github-pat on the other path" "output: $(grep -o 'DIRTY.*' "$out" | head -1)"
else
	pass "22 the exact capture on scripts/other-script.sh -> exit 1, github-pat named there"
fi

# ── 23 · another rule on the same path is a FINDING ──────────────────────────────────────
# The block names ONE rule. A private key on the path must still be reported by its own rule.
# Over history this survives a dropped `targetRules` too (per-finding evaluation, as case 15
# records); the mode where it does not survive is `dir`, and that is case 24.
d="$(new_repo canary-other-rule)" || exit 2
mkdir -p "$d/scripts"
plant_decoy "$d/$fixture_tanda_rel"
git -C "$d" add -A
git -C "$d" commit -qm "a private key on the exempted path"
out="$WORK/23.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ]; then
	fail "23 private key on the same path -> exit 1" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 400)"
elif ! grep -q " 1 finding(s)" "$out" || ! grep -q "private-key" "$out" || ! grep -q "$fixture_tanda_rel" "$out"; then
	fail "23 one private-key finding on that path" "output: $(grep -o 'DIRTY.*' "$out" | head -1)"
else
	pass "23 another rule on the same path -> exit 1, private-key named"
fi

# ── 24 · the DIR-mode witness: a RULE allowlist with the same narrow scope, never a file skip ──
# The same reasoning as case 16: in `dir`/`--no-git` mode every GLOBAL block skips a whole file
# by path before any regex runs, so a dropped `targetRules` (M17) is invisible to cases 19-23
# and visible only here. The path carries the exact line, the near miss, a different PAT and a
# private key; another path carries the exact line as the witness that the detector ran.
# Required identities, counted: on the path TWO github-pat (near miss + other PAT) and ONE
# private-key; on the other path ONE github-pat. The exact capture on its path is the only
# thing absent. A missing or unparseable report is COULD NOT SCAN, distinctly, never a pass.
d="$(new_repo canary-dir-mode)" || exit 2
plant_tanda "$d" "$fixture_tanda_rel" senuelo_diag_line senuelo_diag_near_miss_line other_pat_line
plant_decoy "$WORK/canary-decoy"
cat "$WORK/canary-decoy" >>"$d/$fixture_tanda_rel"
plant_tanda "$d" scripts/other-script.sh senuelo_diag_line
report="$WORK/24.json"
out="$WORK/24.out"
rc="$( (cd "$d" && gitleaks dir . --no-banner --redact --no-color -c "$d/.gitleaks.toml" \
	--report-format json --report-path "$report" >"$out" 2>&1); echo $?)"
verdict="$(python3 - "$report" "$fixture_tanda_rel" scripts/other-script.sh <<'PYCONTROL' 2>/dev/null
import collections, json, sys
try:
    data = json.load(open(sys.argv[1], encoding='utf-8'))
    actual = collections.Counter((row['RuleID'], row['File'].removeprefix('./')) for row in data)
except (OSError, ValueError, TypeError, KeyError):
    sys.exit(3)
expected = collections.Counter({
    ('github-pat', sys.argv[2]): 2,
    ('private-key', sys.argv[2]): 1,
    ('github-pat', sys.argv[3]): 1,
})
if actual == expected:
    print('exact')
else:
    print('missing:', sorted((expected - actual).items()), 'extra:', sorted((actual - expected).items()))
PYCONTROL
)"
pyrc=$?
if [ "$pyrc" != "0" ]; then
	fail "24 COULD NOT SCAN in dir mode" "gitleaks dir exited $rc and left no parseable report — this is not a pass; output: $(tr '\n' ' ' <"$out" | head -c 300)"
elif [ "$rc" != "1" ]; then
	fail "24 dir-mode scan reports findings -> exit 1" "got exit $rc; identities: $verdict"
elif [ "$verdict" != "exact" ]; then
	fail "24 dir mode keeps exactly the near miss, the other PAT, the private key and the witness" "$verdict — the private key or the near miss gone means a whole-file path skip (targetRules dropped); the witness gone means path or condition widened; an extra github-pat on the path means the block no longer matches its capture"
else
	pass "24 dir mode: exact capture exempt on its path; near miss, other PAT, private key and the other-path witness remain"
fi

# ══ 25-36 · THE REPORTING PHASE: one classification per commit, one ref census ═════════
#
# WHY THESE TWELVE EXIST. On 2026-09-09 the `secrets` job (run 34299976570, job
# 102304614840) finished its scan at 03:03:45.373Z and was killed at 03:03:57.526Z, inside
# the phase that NAMES what the scan found. That phase classified every finding twice and ran
# `git for-each-ref --contains <commit>` once per unreachable finding — a full sweep of the
# clone's 11 834 refs per finding, measured at 22 558 ms each on this box. Twelve findings,
# ~4,06 s apiece, 11,88 s of budget left: the gate died mute and the run has no verdict.
#
# The cure is `scripts/secrets-report-attribution.py`: freeze HEAD, take ONE ref census, walk
# the commit graph ONCE, and hand back a state per finding. These cases stand on the property
# that had to survive it — the answer must be the SAME answer, down to which refs are named
# and in which order — and on the property that had to change: the cost must stop following
# the number of findings.
#
# They drive the phase directly, with a scanner shim that writes a prepared report, for the
# same reason case 5 does: planting twelve real decoys on twelve branches would test gitleaks'
# detection, which cases 1-24 already cover, instead of the attribution that killed the job.
# Every case that names a carrier compares against the OLD method, run here as an oracle.

REALGIT="$(command -v git)"

# The graph every case below is judged against. Four abandoned chains of three commits are
# the twelve findings on twelve commits; chain one carries six extra refs so its oldest commit
# has SEVEN carriers and the "first three" is a truncation, not the whole list.
build_attrib_repo() { # <name> ; echoes the path
	local d
	d="$(new_repo "$1")" || return 2
	local base n k
	base="$(git -C "$d" rev-parse HEAD)"
	echo m1 >"$d/m1.txt"; git -C "$d" add m1.txt; git -C "$d" commit -qm m1
	echo m2 >"$d/m2.txt"; git -C "$d" add m2.txt; git -C "$d" commit -qm m2
	for n in 1 2 3 4; do
		git -C "$d" checkout -q -b "chain$n" "$base"
		for k in 1 2 3; do
			mkdir -p "$d/chain$n"
			echo "c$n-$k" >"$d/chain$n/$k.txt"
			git -C "$d" add "chain$n/$k.txt"
			git -C "$d" commit -qm "chain$n c$k"
		done
	done
	git -C "$d" checkout -q main
	local tip
	tip="$(git -C "$d" rev-parse chain1)"
	for n in aaa bbb ccc mmm yyy zzz; do
		git -C "$d" update-ref "refs/heads/extra-$n" "$tip"
	done
	git -C "$d" tag -a -m annotated annotated "$(git -C "$d" rev-parse chain2~1)"
	local inner nested blob
	inner="$(git -C "$d" rev-parse chain3~2)"
	git -C "$d" tag -a -m inner inner "$inner"
	nested="$(printf 'object %s\ntype tag\ntag nested\ntagger battery <battery@example.invalid> 0 +0000\n\nnested\n' \
		"$(git -C "$d" rev-parse inner)" | git -C "$d" mktag)"
	git -C "$d" update-ref refs/tags/nested "$nested"
	blob="$(printf 'not a commit\n' | git -C "$d" hash-object -w --stdin)"
	git -C "$d" update-ref refs/tags/blob-light "$blob"
	# a commit no ref carries: committed on a detached HEAD and abandoned
	git -C "$d" checkout -q --detach main
	echo dangling >"$d/dangling.txt"
	git -C "$d" add dangling.txt
	git -C "$d" commit -qm dangling
	git -C "$d" rev-parse HEAD >"$d/.dangling-oid"
	git -C "$d" checkout -q main
	printf '%s' "$d"
}

# A report with one finding per commit listed in <commits> (one per line; an empty line is a
# working-tree finding). The list is a FILE, not stdin: `python3 -` reads its program from
# stdin, so a heredoc and a piped list cannot share that descriptor — the first draft did, and
# every report came out as `[]`, which the gate correctly refused as "the scan did not
# complete". Nothing here resembles a credential: every field is an identity.
write_report() { # <outfile> <commits file>
	if ! python3 - "$1" "$2" <<'PY'
import json, sys
with open(sys.argv[2], encoding='utf-8') as fh:
    rows = fh.read().split('\n')
if rows and rows[-1] == '':
    rows.pop()
out = []
for i, commit in enumerate(rows):
    out.append({
        'RuleID': 'battery-rule',
        'File': 'fixture/finding-%d.txt' % i,
        'StartLine': i + 1,
        'Commit': commit,
        'Author': 'battery',
        'Email': 'battery@example.invalid',
        'Date': '2026-09-09T00:00:0%dZ' % (i % 10),
        'Fingerprint': '%s:fixture/finding-%d.txt:battery-rule:%d' % (commit, i, i + 1),
    })
with open(sys.argv[1], 'w', encoding='utf-8') as fh:
    json.dump(out, fh)
PY
	then
		setup_fail "write-report" "$1"
		exit 2
	fi
	if [ ! -s "$1" ]; then
		setup_fail "write-report" "$1"
		exit 2
	fi
}

# The method P2 replaced, transcribed from check-secrets.sh as it stood at ccb8ec133c.
oracle_where() { # <dir> <commit>
	local d="$1" c="$2" refs
	if [ -z "$c" ]; then printf '%s' "WORKING TREE (uncommitted)"; return; fi
	if ! git -C "$d" cat-file -e "${c}^{commit}" 2>/dev/null; then
		printf '%s' "NOT IN THIS REPOSITORY (the scanner saw it, this clone does not have it)"
		return
	fi
	if git -C "$d" merge-base --is-ancestor "$c" HEAD 2>/dev/null; then
		printf '%s' "IN what you are merging (reachable from HEAD)"
		return
	fi
	refs="$(git -C "$d" for-each-ref --contains "$c" --format='%(refname)' 2>/dev/null | head -3 | tr '\n' ' ')"
	printf '%s' "NOT reachable from HEAD${refs:+ — carried by: $refs}"
}

reachability_lines() { # <gate output>
	sed -n 's/^  reachability: //p' "$1"
}

make_report_shim "$WORK/report-shim" || exit 2
AD="$(build_attrib_repo attribution)" || exit 2
DANGLING="$(cat "$AD/.dangling-oid")"
ABSENT="$(printf '0%.0s' $(seq 40))"
TWELVE=""
for _n in 1 2 3 4; do
	for _k in 0 1 2; do
		TWELVE="$TWELVE$(git -C "$AD" rev-parse "chain$_n~$_k")
"
	done
done

# ── 25 · twelve findings on twelve commits: all named, in order, and none is yours ─────
printf '%s' "$TWELVE" >"$WORK/25.commits"
write_report "$WORK/25.json" "$WORK/25.commits"
out="$WORK/25.out"
rc="$(run_gate_report "$AD" "$WORK/25.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
got_n="$(reachability_lines "$out" | wc -l)"
named_n="$(reachability_lines "$out" | grep -c 'carried by: refs/')"
if [ "$rc" != "0" ]; then
	fail "25 twelve unreachable findings -> exit 0" "got exit $rc; $(tr '\n' ' ' <"$out" | tail -c 300)"
elif [ "$got_n" != "12" ]; then
	fail "25 all twelve findings survive the phase" "printed $got_n reachability lines, expected 12"
elif [ "$named_n" != "12" ]; then
	fail "25 every unreachable finding names a carrier" "only $named_n of 12 name a ref"
elif ! grep -q "12 hallazgo(s), NINGUNO alcanzable desde HEAD" "$out"; then
	fail "25 the count reaches the cut" "the summary does not say twelve unreachable"
else
	pass "25 twelve findings on twelve commits -> exit 0, all twelve named with their ref"
fi

# ── 26 · the same commit repeated is classified once and answered identically ──────────
{
	git -C "$AD" rev-parse chain1
	git -C "$AD" rev-parse chain1
	git -C "$AD" rev-parse chain2~2
	git -C "$AD" rev-parse chain1
	git -C "$AD" rev-parse chain2~2
} >"$WORK/26.commits"
write_report "$WORK/26.json" "$WORK/26.commits"
out="$WORK/26.out"
rc="$(run_gate_report "$AD" "$WORK/26.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
reachability_lines "$out" >"$WORK/26.got"
{
	oracle_where "$AD" "$(git -C "$AD" rev-parse chain1)"; echo
	oracle_where "$AD" "$(git -C "$AD" rev-parse chain1)"; echo
	oracle_where "$AD" "$(git -C "$AD" rev-parse chain2~2)"; echo
	oracle_where "$AD" "$(git -C "$AD" rev-parse chain1)"; echo
	oracle_where "$AD" "$(git -C "$AD" rev-parse chain2~2)"; echo
} >"$WORK/26.want"
if [ "$rc" != "0" ]; then
	fail "26 duplicated commits -> exit 0" "got exit $rc"
elif ! diff -u "$WORK/26.want" "$WORK/26.got" >"$WORK/26.diff" 2>&1; then
	fail "26 a repeated commit gets the same answer every time" "$(head -12 "$WORK/26.diff" | tr '\n' ' ')"
else
	pass "26 five findings on three commits -> every occurrence answered identically"
fi

# ── 27 · a mixture of HEAD and another ref: one reachable finding still fails the gate ──
{
	git -C "$AD" rev-parse HEAD
	git -C "$AD" rev-parse chain1
	git -C "$AD" rev-parse HEAD~1
	git -C "$AD" rev-parse chain4~2
} >"$WORK/27.commits"
write_report "$WORK/27.json" "$WORK/27.commits"
out="$WORK/27.out"
rc="$(run_gate_report "$AD" "$WORK/27.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
in_n="$(reachability_lines "$out" | grep -c '^IN what you are merging')"
not_n="$(reachability_lines "$out" | grep -c '^NOT reachable from HEAD')"
if [ "$rc" != "1" ]; then
	fail "27 a reachable finding -> exit 1" "got exit $rc — the cut no longer charges what HEAD carries"
elif [ "$in_n" != "2" ] || [ "$not_n" != "2" ]; then
	fail "27 the mixture is split two and two" "IN=$in_n NOT=$not_n, expected 2 and 2"
else
	pass "27 HEAD and another ref in one report -> exit 1, each named for what it is"
fi

# ── 28 · a commit this clone does not carry stays 'NOT IN THIS REPOSITORY' ─────────────
printf '%s\n' "$ABSENT" >"$WORK/28.commits"
write_report "$WORK/28.json" "$WORK/28.commits"
out="$WORK/28.out"
rc="$(run_gate_report "$AD" "$WORK/28.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
if [ "$rc" != "0" ]; then
	fail "28 an absent commit -> exit 0" "got exit $rc"
elif ! grep -q "reachability: NOT IN THIS REPOSITORY" "$out"; then
	fail "28 an absent commit is named as absent" "$(reachability_lines "$out" | tr '\n' ' ')"
elif grep -q "carried by" "$out"; then
	fail "28 an absent commit gets no carrier" "the gate invented a ref for a commit it does not have"
else
	pass "28 a commit absent from the clone -> reported as absent, no ref invented"
fi

# ── 29 · a commit that IS here but no ref carries keeps its state without a carrier ────
printf '%s\n' "$DANGLING" >"$WORK/29.commits"
write_report "$WORK/29.json" "$WORK/29.commits"
out="$WORK/29.out"
rc="$(run_gate_report "$AD" "$WORK/29.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
want="$(oracle_where "$AD" "$DANGLING")"
got="$(reachability_lines "$out")"
if [ "$rc" != "0" ]; then
	fail "29 a dangling commit -> exit 0" "got exit $rc"
elif [ "$got" != "NOT reachable from HEAD" ] || [ "$got" != "$want" ]; then
	fail "29 present, unreachable, carried by nobody" "got [$got] oracle [$want]"
else
	pass "29 a commit no ref carries -> NOT reachable from HEAD, and no ref is invented"
fi

# ── 30 · a finding with no commit is the WORKING TREE, and its columns do not shift ────
# THE COUNTEREXAMPLE THIS CASE STANDS ON, measured 2026-09-09 with bash 5.2. The rows used
# to be tab-joined and read with `IFS=$'\t' read`, and a tab is IFS WHITESPACE: consecutive
# tabs collapse. A row with no Commit — `rule<TAB>file<TAB>line<TAB><TAB><TAB><TAB><TAB>fp` —
# was therefore read as four fields, the FINGERPRINT landed in `commit`, and the branch
# `check-secrets.sh` documented as "working tree = yours" was dead code: the finding came out
# as NOT IN THIS REPOSITORY and was counted against somebody else. The rows now use US
# (0x1f), which is not IFS whitespace and keeps empty fields.
#
# This gate does not produce such a row today, and that is measured too: `gitleaks detect`
# runs in git mode in BOTH scopes, and an uncommitted decoy yields zero findings there while
# `gitleaks dir` on the same tree yields one with `Commit: ""`. So the state was unreachable
# in production and wrong on arrival — which is exactly the shape a case has to pin before
# the next mode change makes it reachable.
printf '\n' >"$WORK/30.commits"
write_report "$WORK/30.json" "$WORK/30.commits"
out="$WORK/30.out"
rc="$(run_gate_report "$AD" "$WORK/30.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
if [ "$rc" != "1" ]; then
	fail "30 an uncommitted finding is YOURS -> exit 1" "got exit $rc"
elif ! grep -q "reachability: WORKING TREE (uncommitted)" "$out"; then
	fail "30 a finding with no commit is the working tree" "$(reachability_lines "$out" | tr '\n' ' ')"
elif ! grep -q "^  commit     : <none>" "$out"; then
	fail "30 the columns do not shift on an empty field" "the commit column shows $(grep '^  commit' "$out")"
elif ! grep -q "^  fingerprint: :fixture/finding-0.txt:battery-rule:1" "$out"; then
	fail "30 the fingerprint stays in its own column" "$(grep '^  fingerprint' "$out")"
else
	pass "30 a finding with no commit -> WORKING TREE, counted as yours, columns intact"
fi

# ── 31 · an annotated tag is a carrier, and it is named ────────────────────────────────
git -C "$AD" rev-parse chain2~1 >"$WORK/31.commits"
write_report "$WORK/31.json" "$WORK/31.commits"
out="$WORK/31.out"
rc="$(run_gate_report "$AD" "$WORK/31.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
want="$(oracle_where "$AD" "$(git -C "$AD" rev-parse chain2~1)")"
got="$(reachability_lines "$out")"
if [ "$rc" != "0" ]; then
	fail "31 annotated tag carrier -> exit 0" "got exit $rc"
elif [ "$got" != "$want" ]; then
	fail "31 the annotated tag is named exactly as before" "got [$got] oracle [$want]"
elif ! printf '%s' "$got" | grep -q 'refs/tags/annotated'; then
	fail "31 the annotated tag appears at all" "got [$got]"
else
	pass "31 an annotated tag carrying the commit is named, exactly as the old method named it"
fi

# ── 32 · a tag on a tag is a carrier; a ref on a blob never is ─────────────────────────
git -C "$AD" rev-parse chain3~2 >"$WORK/32.commits"
write_report "$WORK/32.json" "$WORK/32.commits"
out="$WORK/32.out"
rc="$(run_gate_report "$AD" "$WORK/32.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
want="$(oracle_where "$AD" "$(git -C "$AD" rev-parse chain3~2)")"
got="$(reachability_lines "$out")"
if [ "$rc" != "0" ]; then
	fail "32 nested tag carrier -> exit 0" "got exit $rc"
elif [ "$got" != "$want" ]; then
	fail "32 a tag on a tag is resolved like git resolves it" "got [$got] oracle [$want]"
elif printf '%s' "$got" | grep -q 'blob'; then
	fail "32 a ref on a blob is never an ancestor" "got [$got]"
else
	pass "32 a tag on a tag on a commit is a carrier; a ref on a blob is not"
fi

# ── 33 · seven carriers, and the answer is the first THREE by refname, in order ────────
git -C "$AD" rev-parse chain1~2 >"$WORK/33.commits"
write_report "$WORK/33.json" "$WORK/33.commits"
out="$WORK/33.out"
rc="$(run_gate_report "$AD" "$WORK/33.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
want="$(oracle_where "$AD" "$(git -C "$AD" rev-parse chain1~2)")"
got="$(reachability_lines "$out")"
carriers="$(git -C "$AD" for-each-ref --contains "$(git -C "$AD" rev-parse chain1~2)" --format='%(refname)' | wc -l)"
if [ "$rc" != "0" ]; then
	fail "33 truncated carrier list -> exit 0" "got exit $rc"
elif [ "$carriers" -le 3 ]; then
	fail "33 the fixture must have MORE than three carriers" "it has $carriers — the truncation is not exercised"
elif [ "$got" != "$want" ]; then
	fail "33 the first three carriers, in the same order" "got [$got] oracle [$want]"
else
	pass "33 a commit with $carriers carriers -> exactly the first three, in refname order"
fi

# ── 34 · THE ORACLE: every finding, compared against the method P2 replaced ────────────
# The case that would go red if the new attribution were merely self-consistent: each line is
# recomputed with `cat-file -e` / `merge-base --is-ancestor` / `for-each-ref --contains`, the
# three commands the phase no longer runs, and the two answers must agree byte for byte —
# trailing space of the carrier list included.
{
	cat "$WORK/25.commits"
	git -C "$AD" rev-parse HEAD
	git -C "$AD" rev-parse chain1~2
	printf '%s\n' "$DANGLING"
	printf '%s\n' "$ABSENT"
	git -C "$AD" rev-parse chain2~1
	git -C "$AD" rev-parse chain3~2
	git -C "$AD" rev-parse HEAD~2
	git -C "$AD" rev-parse chain1
} >"$WORK/34.commits"
write_report "$WORK/34.json" "$WORK/34.commits"
out="$WORK/34.out"
rc="$(run_gate_report "$AD" "$WORK/34.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
: >"$WORK/34.want"
while IFS= read -r c; do
	oracle_where "$AD" "$c" >>"$WORK/34.want"
	echo >>"$WORK/34.want"
done <"$WORK/34.commits"
reachability_lines "$out" >"$WORK/34.got"
if [ "$rc" != "1" ]; then
	fail "34 the oracle report -> exit 1 (it carries HEAD findings)" "got exit $rc"
elif ! diff -u "$WORK/34.want" "$WORK/34.got" >"$WORK/34.diff" 2>&1; then
	fail "34 the new attribution equals the old method, finding by finding" \
		"$(head -16 "$WORK/34.diff" | tr '\n' ' ')"
else
	pass "34 twenty findings: every reachability line equals the previous method's, byte for byte"
fi

# ── 35 · COST: the ref sweep no longer follows the number of findings ──────────────────
# Counted with GIT_TRACE, which logs one line per git process. The claim under test is not
# "it is faster" — that is measured in the delivery — but that twelve findings and sixty cost
# the SAME git invocations, and that `--contains`, `name-rev` and `merge-base` are gone.
: >"$WORK/35.sixty"
for _i in 1 2 3 4 5; do cat "$WORK/25.commits" >>"$WORK/35.sixty"; done
write_report "$WORK/35a.json" "$WORK/25.commits"
write_report "$WORK/35b.json" "$WORK/35.sixty"
rm -f "$WORK/35a.trace" "$WORK/35b.trace"
rc_a="$(run_gate_report "$AD" "$WORK/35a.json" "$WORK/35a.out" OLIVARES_SECRETS_SCOPE=all GIT_TRACE="$WORK/35a.trace")"
rc_b="$(run_gate_report "$AD" "$WORK/35b.json" "$WORK/35b.out" OLIVARES_SECRETS_SCOPE=all GIT_TRACE="$WORK/35b.trace")"
# `grep -c` PRINTS 0 and EXITS 1 when it matches nothing, so the obvious `|| echo 0` emits
# the count TWICE and every comparison below becomes a two-line string. Measured here.
count_git() { local n; n="$(grep -c 'trace: built-in: git ' "$1" 2>/dev/null)"; printf '%s' "${n:-0}"; }
match_git() { local n; n="$(grep -c "trace: built-in: git .*$2" "$1" 2>/dev/null)"; printf '%s' "${n:-0}"; }
n_a="$(count_git "$WORK/35a.trace")"
n_b="$(count_git "$WORK/35b.trace")"
sweeps="$(match_git "$WORK/35b.trace" '[-][-]contains')"
mbase="$(match_git "$WORK/35b.trace" 'merge-base')"
nrev="$(match_git "$WORK/35b.trace" 'name-rev')"
census="$(match_git "$WORK/35b.trace" 'for-each-ref')"
walks="$(match_git "$WORK/35b.trace" 'rev-list --topo-order')"
if [ "$rc_a" != "0" ] || [ "$rc_b" != "0" ]; then
	fail "35 both cost runs produce a verdict" "twelve findings exit $rc_a, sixty exit $rc_b"
elif [ "$n_a" -lt 1 ] || [ "$n_b" -lt 1 ]; then
	fail "35 GIT_TRACE recorded nothing" "twelve -> $n_a lines, sixty -> $n_b; the count proves nothing"
elif [ "$sweeps" != "0" ] || [ "$mbase" != "0" ] || [ "$nrev" != "0" ]; then
	fail "35 no per-finding history walk survives" \
		"--contains=$sweeps merge-base=$mbase name-rev=$nrev, all must be 0"
elif [ "$census" -gt 1 ] || [ "$walks" -gt 1 ]; then
	fail "35 one ref census and one graph walk, at most" "for-each-ref=$census rev-list --topo-order=$walks"
elif [ "$n_a" != "$n_b" ]; then
	fail "35 five times the findings costs the same git invocations" \
		"twelve findings -> $n_a, sixty -> $n_b"
else
	pass "35 twelve and sixty findings both cost $n_a git invocations; 1 census, 1 walk, 0 sweeps"
fi

# ── 36 · a git that cannot answer is COULD NOT LOOK (2), never a verdict ───────────────
# Fail-closed is the whole reason this wrapper exists (see its header): "I could not attribute
# it" must never leave as 0 or 1. The fault is injected where the phase actually asks git —
# the ref census — with a shim that passes everything else through to the real git.
mkdir -p "$WORK/broken-git"
cat >"$WORK/broken-git/git" <<SHIM
#!/usr/bin/env bash
if [ "\${1:-}" = "for-each-ref" ]; then
  echo "fatal: simulated ref-store failure" >&2
  exit 128
fi
exec "$REALGIT" "\$@"
SHIM
chmod +x "$WORK/broken-git/git"
cp "$WORK/report-shim/gitleaks" "$WORK/broken-git/gitleaks"
out="$WORK/36.out"
rc="$( (cd "$AD" && OLIVARES_TEST_REPORT="$WORK/25.json" OLIVARES_SECRETS_SCOPE=all \
	PATH="$WORK/broken-git:$PATH" bash "$GATE" >"$out" 2>&1); echo $?)"
if [ "$rc" = "0" ] || [ "$rc" = "1" ]; then
	fail "36 a broken ref store -> exit 2, not a verdict" "got exit $rc — a gate that could not look answered anyway"
elif [ "$rc" != "2" ]; then
	fail "36 a broken ref store -> exit 2" "got exit $rc"
elif ! grep -q "COULD NOT LOOK" "$out"; then
	fail "36 it says COULD NOT LOOK" "exit 2 but the phrase is absent: $(tr '\n' ' ' <"$out" | tail -c 200)"
else
	pass "36 a git failure during attribution -> exit 2 and says COULD NOT LOOK"
fi

# 36b · the helper's own battery. It carries the checks this file cannot make from outside:
# the three git properties the equivalence rests on, the top-three merge, and the states.
if python3 "$ROOT/scripts/test-secrets-report-attribution.py" >"$WORK/36b.out" 2>&1; then
	pass "36b the attributor's own unit battery is green ($(tail -1 "$WORK/36b.out"))"
else
	# Name the check that failed, not the ones that passed after it. The battery prints every
	# `ok` line, so a bare tail showed five greens and hid the FAIL (CI run 34731376655, job
	# 103654672479: the red was 1d, far above the tail). The tail stays as the fallback for a
	# battery that could not run and printed no FAIL line. Not a grep after-context window:
	# lint:engine-output treats that form as a first-boot banner capture (R98).
	fail "36b the attributor's own unit battery" \
		"$({ awk '
			{
				is_fail = /^  FAIL /
				if (was_fail || is_fail || /check[(]s[)] failed$/ || /could not run/) {
					print
					found = 1
				}
				was_fail = is_fail
			}
			END { if (!found) exit 1 }
		' "$WORK/36b.out" || tail -6 "$WORK/36b.out"; } | tr '\n' ' ')"
fi

# ══ 37-42 · RECORD BOUNDARIES: a valid Git path is not a separator ════════════════════
#
# WHY THESE SIX EXIST. The first version of this phase (ac6ca0fef5) joined the finding fields
# with US (0x1f) because a TAB collapses under `IFS=$'\t' read`. Root returned it the same day
# with the counterexample that matters: **a valid Git path may contain 0x1f**. One finding at
# HEAD whose File is `odd<US>path.txt` shifted every field by one — the Commit column received
# the StartLine — and the gate answered `0 / NOT IN THIS REPOSITORY` about a finding that IS in
# HEAD. Reproduced and sealed against that exact commit in
# `assessments/implementation/secrets-report-attribution/framing-correction/red/`.
#
# The cure is not a rarer byte. Every field is escaped reversibly (`\t \n \r \xNN`, and `\\`
# for a literal backslash) and framed BY POSITION, eight lines per finding, read with
# `IFS= read -r`, which splits by nothing. There is no separator left to collide with.
#
# These cases stand on the three properties that follow from it: the authority is the JSON
# Commit field and never a display column; a control byte in a path can never forge a record or
# a verdict; and a report the reader cannot represent is COULD NOT LOOK, never a verdict.

# write_report_fields <out.json> <specfile> — Commit|File|Author|Email|Date|Fingerprint per line.
# `\U \T \R \N \E` expand to US, TAB, CR, LF and a literal backslash, so this file plants the
# bytes without containing them. The spec is a FILE: `python3 -` reads its program from stdin.
write_report_fields() {
	if ! python3 - "$1" "$2" <<'PY'
import json, sys
def expand(s):
    out, i = [], 0
    while i < len(s):
        if s[i] == '\\' and i + 1 < len(s):
            m = {'U': '\x1f', 'T': '\t', 'R': '\r', 'N': '\n', 'E': '\\'}.get(s[i+1])
            if m is not None:
                out.append(m); i += 2; continue
        out.append(s[i]); i += 1
    return ''.join(out)
rows = []
with open(sys.argv[2], encoding='utf-8') as fh:
    spec = fh.read()
for line in spec.split('\n'):
    if not line:
        continue
    parts = (line.split('|') + [''] * 6)[:6]
    commit, path, author, email, date, fp = (expand(p) for p in parts)
    rows.append({'RuleID': 'framing-rule', 'File': path, 'StartLine': len(rows) + 1,
                 'Commit': commit, 'Author': author, 'Email': email, 'Date': date,
                 'Fingerprint': fp})
json.dump(rows, open(sys.argv[1], 'w', encoding='utf-8'))
PY
	then
		setup_fail "write-report-fields" "$1"
		exit 2
	fi
	if [ ! -s "$1" ]; then
		setup_fail "write-report-fields" "$1"
		exit 2
	fi
}
frame_records() { local n; n="$(grep -c '^  rule       : ' "$1" 2>/dev/null)"; printf '%s' "${n:-0}"; }
frame_verdicts() { local n; n="$(grep -c '^  reachability: ' "$1" 2>/dev/null)"; printf '%s' "${n:-0}"; }
frame_commit_col() { sed -n 's/^  commit     : \([^ ]*\).*/\1/p' "$1"; }
frame_states() {
	sed -n 's/^  reachability: //p' "$1" | sed -e 's/^IN what you are merging.*/IN/' \
		-e 's/^NOT reachable from HEAD.*/UNREACH/' -e 's/^NOT IN THIS REPOSITORY.*/ABSENT/' \
		-e 's/^WORKING TREE.*/WORKTREE/' | tr '\n' ','
}

FR="$(build_attrib_repo framing)" || exit 2
FR_HEAD="$(git -C "$FR" rev-parse HEAD)"
FR_STALE="$(git -C "$FR" rev-parse chain1)"

# ── 37 · root's counterexample: US inside a valid path, one finding at HEAD ────────────
printf '%s|odd\\Upath.txt|a|b|2026-09-09T00:00:00Z|fp\n' "$FR_HEAD" >"$WORK/37.spec"
write_report_fields "$WORK/37.json" "$WORK/37.spec"
out="$WORK/37.out"
rc="$(run_gate_report "$FR" "$WORK/37.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
if [ "$rc" != "1" ]; then
	fail "37 US in a path: a HEAD finding is still charged -> exit 1" \
		"got exit $rc — the record boundary moved and the verdict moved with it"
elif [ "$(frame_commit_col "$out")" != "$FR_HEAD" ]; then
	fail "37 the Commit column is the JSON Commit, not a neighbouring field" \
		"commit column = [$(frame_commit_col "$out")], expected $FR_HEAD"
elif [ "$(frame_states "$out")" != "IN," ]; then
	fail "37 the state comes from the Commit field" "states=[$(frame_states "$out")]"
elif ! grep -q '^  file       : odd\\x1fpath.txt:1$' "$out"; then
	fail "37 the path is printed escaped, on its own field line" "$(grep '^  file' "$out")"
else
	pass "37 US in a valid path -> exit 1, IN what you are merging, columns and boundary intact"
fi

# ── 38 · TAB, CR and LF, and the record a path would forge with them ──────────────────
{
	printf '%s|tab\\Tpath.txt|||\n' "$FR_HEAD"
	printf '%s|cr\\Rlf\\Npath.txt|||\n' "$FR_HEAD"
	printf '%s|x\\N  rule       : forged\\N  reachability: IN what you are merging\\Ny.txt|||\n' "$FR_HEAD"
} >"$WORK/38.spec"
write_report_fields "$WORK/38.json" "$WORK/38.spec"
out="$WORK/38.out"
rc="$(run_gate_report "$FR" "$WORK/38.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
if [ "$rc" != "1" ]; then
	fail "38 tab/CR/LF in a path -> exit 1, not a refused scan" \
		"got exit $rc — a VALID path must not be read as an unusable report"
elif [ "$(frame_records "$out")" != "3" ] || [ "$(frame_verdicts "$out")" != "3" ]; then
	fail "38 three findings stay three records and three verdicts" \
		"records=$(frame_records "$out") verdicts=$(frame_verdicts "$out") — a path forged one"
elif [ "$(frame_states "$out")" != "IN,IN,IN," ]; then
	fail "38 every record keeps its own state" "states=[$(frame_states "$out")]"
else
	pass "38 tab, CR, LF and a forged record line -> 3 records, 3 verdicts, all IN, exit 1"
fi

# ── 39 · a literal backslash and a literal escape sequence survive, reversibly ────────
printf '%s|dir\\Ename\\Ex1f.txt|||\n' "$FR_HEAD" >"$WORK/39.spec"
write_report_fields "$WORK/39.json" "$WORK/39.spec"
out="$WORK/39.out"
rc="$(run_gate_report "$FR" "$WORK/39.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
# The path on disk is `dir\name\x1f.txt`: one real backslash before `name`, and the four
# LITERAL characters \x1f. Escaped reversibly that is `dir\\name\\x1f.txt` — and it must not
# come back as the same text as a path carrying a real 0x1f, which case 37 prints as `\x1f`.
if [ "$rc" != "1" ]; then
	fail "39 a literal backslash path -> exit 1" "got exit $rc"
elif ! grep -q '^  file       : dir\\\\name\\\\x1f\.txt:1$' "$out"; then
	fail "39 a literal backslash is escaped reversibly" \
		"$(grep '^  file' "$out") — a real 0x1f and the text \\x1f must not print the same"
elif [ "$(frame_records "$out")" != "1" ]; then
	fail "39 one record" "records=$(frame_records "$out")"
else
	pass "39 a literal backslash and a literal \\x1f sequence -> escaped reversibly, one record"
fi

# ── 40 · every metadata field empty, at HEAD: the columns do not shift ────────────────
printf '%s|plain.txt||||\n' "$FR_HEAD" >"$WORK/40.spec"
write_report_fields "$WORK/40.json" "$WORK/40.spec"
out="$WORK/40.out"
rc="$(run_gate_report "$FR" "$WORK/40.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
if [ "$rc" != "1" ]; then
	fail "40 empty metadata at HEAD -> exit 1" "got exit $rc"
elif [ "$(frame_commit_col "$out")" != "$FR_HEAD" ]; then
	fail "40 empty fields keep their identity" "commit column = [$(frame_commit_col "$out")]"
elif ! grep -q '^  fingerprint: $' "$out" || ! grep -q '^  author     : $' "$out"; then
	fail "40 an empty field prints as empty, not as its neighbour" \
		"author=[$(grep '^  author' "$out")] fingerprint=[$(grep '^  fingerprint' "$out")]"
else
	pass "40 empty Author/Email/Date/Fingerprint -> columns intact, exit 1"
fi

# ── 41 · mixed states in one report, each with odd bytes ──────────────────────────────
{
	printf '%s|head\\Uodd.txt|au\\Tthor||2026-09-09T00:00:00Z|fp1\n' "$FR_HEAD"
	printf '%s|stale\\Nodd.txt|||\n' "$FR_STALE"
	printf '|worktree\\Uodd.txt|||\n'
} >"$WORK/41.spec"
write_report_fields "$WORK/41.json" "$WORK/41.spec"
out="$WORK/41.out"
rc="$(run_gate_report "$FR" "$WORK/41.json" "$out" OLIVARES_SECRETS_SCOPE=all)"
if [ "$rc" != "1" ]; then
	fail "41 a HEAD and a working-tree finding are charged -> exit 1" "got exit $rc"
elif [ "$(frame_records "$out")" != "3" ]; then
	fail "41 three logical records, each exactly once" "records=$(frame_records "$out")"
elif [ "$(frame_states "$out")" != "IN,UNREACH,WORKTREE," ]; then
	fail "41 each record keeps its own state" "states=[$(frame_states "$out")]"
elif ! grep -q 'carried by: refs/heads/chain1' "$out"; then
	fail "41 the unreachable one still names its carrier" "$(sed -n 's/^  reachability: //p' "$out" | tr '\n' '|')"
else
	pass "41 HEAD + unreachable + working tree, all with odd bytes -> 3 records, right states, exit 1"
fi

# ── 42 · a record the reader cannot represent is COULD NOT LOOK, never a verdict ──────
# Measured against ac6ca0fef5: `str()` turned a Commit of `{"oid": "<sha>"}` into text, the
# text was not an object name, and the gate answered **exit 0** — a clean verdict invented out
# of a report it never understood. Two shapes, one rule. Delivery hashes and the exact
# unsupported-type cause are required; a missing report is a different parse branch.
if ! python3 - "$WORK/42a.json" "$FR_HEAD" <<'PYFRAME'
import json, sys
json.dump([{'RuleID': 'framing-rule', 'File': 'plain.txt', 'StartLine': 1,
            'Commit': {'oid': sys.argv[2]}, 'Author': '', 'Email': '', 'Date': '',
            'Fingerprint': ''}], open(sys.argv[1], 'w', encoding='utf-8'))
PYFRAME
then
	setup_fail "write-malformed-report-42a" "$WORK/42a.json"
	exit 2
fi
cfp2_verify_case42a_specimen "$WORK/42a.json" || {
	setup_fail "malformed-report-42a-shape" "$WORK/42a.json"
	exit 2
}
printf '[1, 2]' >"$WORK/42b.json" || {
	setup_fail "write-malformed-report-42b" "$WORK/42b.json"
	exit 2
}
cfp2_verify_case42b_specimen "$WORK/42b.json" || {
	setup_fail "malformed-report-42b-shape" "$WORK/42b.json"
	exit 2
}
rc_a="$(run_gate_report "$FR" "$WORK/42a.json" "$WORK/42a.out" OLIVARES_SECRETS_SCOPE=all)" || exit 2
rc_b="$(run_gate_report "$FR" "$WORK/42b.json" "$WORK/42b.out" OLIVARES_SECRETS_SCOPE=all)" || exit 2
if [ "$rc_a" = "0" ] || [ "$rc_a" = "1" ] || [ "$rc_b" = "0" ] || [ "$rc_b" = "1" ]; then
	fail "42 a malformed record -> exit 2, never a verdict" \
		"object Commit exit=$rc_a, non-object record exit=$rc_b — one of them graded a report it could not read"
elif [ "$rc_a" != "2" ] || [ "$rc_b" != "2" ]; then
	fail "42 a malformed record -> exit 2" "object Commit exit=$rc_a, non-object record exit=$rc_b"
elif ! grep -q "COULD NOT LOOK" "$WORK/42a.out" || ! grep -q "COULD NOT LOOK" "$WORK/42b.out"; then
	fail "42 both say COULD NOT LOOK" "exit 2 but the phrase is absent in one of them"
elif ! grep -q 'carries a field of an unsupported type' "$WORK/42a.out"; then
	fail "42a names unsupported type" "exit 2 but the intended parse cause is absent"
elif ! grep -q 'carries a field of an unsupported type' "$WORK/42b.out"; then
	fail "42b names unsupported type" "exit 2 but the intended parse cause is absent"
elif ! cfp2_no_verdict "$WORK/42a.out" || ! cfp2_no_verdict "$WORK/42b.out"; then
	fail "42 a malformed record is not a verdict" "a CLEAN or DIRTY line was printed"
else
	pass "42 a Commit that is not a string, and a record that is not an object -> exit 2 both"
fi

# ── the R41 fixed route-evidence digest vector: helpers for cases 43-52 ───────────────
# The digest is a public expected value, but the fixtures still assemble it from fragments
# so this test script does not create another literal generic-api-key finding of its own.
digest_fixture_rel='core/auth/route_evidence_digest_internal_test.go'
digest_other_rel='core/auth/other_route_evidence_digest_test.go'
route_digest_value() {
	printf '%s%s%s%s' '578d177303130d05' '4346291dfe8e5025' '187e9801e998ff580' 'ab2c14302e3202e'
}
route_digest_exact_line() {
	printf '\t%s    = "%s"\n' 'vectorCompleteReadToken' "$(route_digest_value)"
}
route_digest_near_miss_line() {
	local value
	value="$(route_digest_value)"
	printf '\t%s    = "%sf"\n' 'vectorCompleteReadToken' "${value%?}"
}
route_digest_other_identifier_line() {
	printf '\t%s = "%s"\n' 'apiToken' "$(route_digest_value)"
}
plant_route_digest_file() { # <repo> <relative-path> <line-producer>...
	local d="$1" rel="$2" fn
	shift 2
	mkdir -p "$d/$(dirname "$rel")"
	{
		printf 'package auth\n\nconst (\n'
		for fn in "$@"; do "$fn"; done
		printf ')\n'
	} >"$d/$rel"
}
mutate_route_digest_block() { # <repo> <drop|target-rules|condition|path|capture>
	python3 - "$1/.gitleaks.toml" "$2" <<'PYDIGESTMUT'
import pathlib, sys

path = pathlib.Path(sys.argv[1])
mode = sys.argv[2]
text = path.read_text(encoding='utf-8')
start_marker = '# R41 route-evidence fixed digest vector.'
end_marker = '# End R41 route-evidence fixed digest vector.'
start = text.find(start_marker)
end = text.find(end_marker, start)
if start < 0 or end < 0:
    sys.exit(3)
end = text.find('\n', end)
end = len(text) if end < 0 else end + 1
block = text[start:end]
if mode == 'drop':
    replacement = ''
elif mode == 'target-rules':
    replacement = block.replace('  targetRules = ["generic-api-key"]\n', '', 1)
elif mode == 'condition':
    replacement = block.replace('  condition = "AND"\n', '  condition = "OR"\n', 1)
elif mode == 'path':
    replacement = ''.join(line for line in block.splitlines(keepends=True)
                          if not line.startswith('  paths = '))
elif mode == 'capture':
    replacement = block.replace('e"$\'\'\']\n', '[0-9a-f]"$\'\'\']\n', 1)
else:
    sys.exit(4)
if replacement == block:
    sys.exit(5)
path.write_text(text[:start] + replacement + text[end:], encoding='utf-8')
PYDIGESTMUT
}
run_digest_dir() { # <repo> <report> <outfile> ; echoes the exit code
	local d="$1" report="$2" out="$3"
	(cd "$d" && gitleaks dir . --no-banner --redact --no-color -c "$d/.gitleaks.toml" \
		--report-format json --report-path "$report" >"$out" 2>&1)
	echo $?
}

# ── 43 · exact history declaration, plus a no-fire control without the block ──────────
d="$(new_repo route-digest-exact)" || exit 2
plant_route_digest_file "$d" "$digest_fixture_rel" route_digest_exact_line
git -C "$d" add -A
git -C "$d" commit -qm "the classified digest vector on its exact path"
out="$WORK/43a.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "0" ] || ! grep -q "CLEAN" "$out"; then
	fail "43a exact digest declaration on its path -> exit 0 CLEAN" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
else
	pass "43a exact digest declaration on its exact path -> exit 0 CLEAN"
fi
d="$(new_repo route-digest-nofire)" || exit 2
plant_route_digest_file "$d" "$digest_fixture_rel" route_digest_exact_line
if ! mutate_route_digest_block "$d" drop; then
	fail "43b could remove the copied digest block" "the no-fire control could not prepare its configuration"
else
	git -C "$d" add -A
	git -C "$d" commit -qm "the exact declaration without its exception block"
	out="$WORK/43b.out"
	rc="$(run_gate "$d" "$out")"
	if [ "$rc" != "1" ] || ! grep -q " 1 finding(s)" "$out" || \
		! grep -q "generic-api-key" "$out" || ! grep -q "$digest_fixture_rel" "$out"; then
		fail "43b exact declaration without the block -> one finding" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
	else
		pass "43b the same declaration without the block -> one generic-api-key finding"
	fi
fi

# ── 44 · one changed digest byte on the exact path remains a finding ──────────────────
d="$(new_repo route-digest-near-miss)" || exit 2
plant_route_digest_file "$d" "$digest_fixture_rel" route_digest_near_miss_line
git -C "$d" add -A
git -C "$d" commit -qm "a one-byte digest near miss on the exact path"
out="$WORK/44.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ] || ! grep -q " 1 finding(s)" "$out" || ! grep -q "generic-api-key" "$out"; then
	fail "44 one-byte digest change -> one finding" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
else
	pass "44 one-byte digest change on the exact path -> one generic-api-key finding"
fi

# ── 45 · the same digest under a credential-like identifier remains a finding ────────
d="$(new_repo route-digest-other-identifier)" || exit 2
plant_route_digest_file "$d" "$digest_fixture_rel" route_digest_other_identifier_line
git -C "$d" add -A
git -C "$d" commit -qm "the digest under a credential-like identifier"
out="$WORK/45.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ] || ! grep -q " 1 finding(s)" "$out" || ! grep -q "generic-api-key" "$out"; then
	fail "45 same digest under apiToken -> one finding" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
else
	pass "45 the same digest under apiToken on the exact path -> one generic-api-key finding"
fi

# ── 46 · the exact declaration on another path remains a finding ─────────────────────
d="$(new_repo route-digest-other-path)" || exit 2
plant_route_digest_file "$d" "$digest_other_rel" route_digest_exact_line
git -C "$d" add -A
git -C "$d" commit -qm "the exact declaration on another path"
out="$WORK/46.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ] || ! grep -q " 1 finding(s)" "$out" || \
	! grep -q "generic-api-key" "$out" || ! grep -q "$digest_other_rel" "$out"; then
	fail "46 exact declaration on another path -> one finding" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
else
	pass "46 the exact declaration on another path -> one generic-api-key finding"
fi

# ── 47 · another rule on the allowed path remains a finding ───────────────────────────
d="$(new_repo route-digest-other-rule)" || exit 2
mkdir -p "$d/$(dirname "$digest_fixture_rel")"
plant_decoy "$d/$digest_fixture_rel"
git -C "$d" add -A
git -C "$d" commit -qm "a private-key specimen on the allowed path"
out="$WORK/47.out"
rc="$(run_gate "$d" "$out")"
if [ "$rc" != "1" ] || ! grep -q " 1 finding(s)" "$out" || ! grep -q "private-key" "$out"; then
	fail "47 another rule on the allowed path -> one finding" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
else
	pass "47 another rule on the allowed path -> one private-key finding"
fi

# ── 48 · directory mode keeps every neighboring finding and the path witness ─────────
d="$(new_repo route-digest-dir)" || exit 2
plant_route_digest_file "$d" "$digest_fixture_rel" route_digest_exact_line \
	route_digest_near_miss_line route_digest_other_identifier_line
plant_decoy "$WORK/route-digest-decoy"
cat "$WORK/route-digest-decoy" >>"$d/$digest_fixture_rel"
plant_route_digest_file "$d" "$digest_other_rel" route_digest_exact_line
report="$WORK/48.json"
out="$WORK/48.out"
rc="$(run_digest_dir "$d" "$report" "$out")"
verdict="$(python3 - "$report" "$digest_fixture_rel" "$digest_other_rel" <<'PYDIGESTCONTROL' 2>/dev/null
import collections, json, sys
try:
    rows = json.load(open(sys.argv[1], encoding='utf-8'))
    actual = collections.Counter((row['RuleID'], row['File'].removeprefix('./')) for row in rows)
except (OSError, ValueError, TypeError, KeyError):
    sys.exit(3)
expected = collections.Counter({
    ('generic-api-key', sys.argv[2]): 2,
    ('private-key', sys.argv[2]): 1,
    ('generic-api-key', sys.argv[3]): 1,
})
print('exact' if actual == expected else f'actual={sorted(actual.items())}')
PYDIGESTCONTROL
)"
pyrc=$?
if [ "$pyrc" != "0" ]; then
	fail "48 directory-mode report is parseable" "gitleaks exited $rc without a usable report"
elif [ "$rc" != "1" ] || [ "$verdict" != "exact" ]; then
	fail "48 directory mode retains the four neighboring findings" "exit=$rc $verdict"
else
	pass "48 dir mode exempts only the exact declaration; four neighboring findings remain"
fi

# ── 49 · removing targetRules demonstrates the global directory path prefilter ───────
d="$(new_repo route-digest-mutant-target-rules)" || exit 2
plant_route_digest_file "$d" "$digest_fixture_rel" route_digest_exact_line \
	route_digest_near_miss_line route_digest_other_identifier_line
plant_decoy "$WORK/route-digest-mutant-decoy"
cat "$WORK/route-digest-mutant-decoy" >>"$d/$digest_fixture_rel"
plant_route_digest_file "$d" "$digest_other_rel" route_digest_exact_line
if ! mutate_route_digest_block "$d" target-rules; then
	fail "49 could create the targetRules mutant" "the copied block did not have the required field"
else
	report="$WORK/49.json"
	out="$WORK/49.out"
	rc="$(run_digest_dir "$d" "$report" "$out")"
	verdict="$(python3 - "$report" "$digest_other_rel" <<'PYDIGESTTARGET' 2>/dev/null
import collections, json, sys
try:
    rows = json.load(open(sys.argv[1], encoding='utf-8'))
    actual = collections.Counter((row['RuleID'], row['File'].removeprefix('./')) for row in rows)
except (OSError, ValueError, TypeError, KeyError):
    sys.exit(3)
expected = collections.Counter({('generic-api-key', sys.argv[2]): 1})
print('exact' if actual == expected else f'actual={sorted(actual.items())}')
PYDIGESTTARGET
)"
	pyrc=$?
	if [ "$pyrc" != "0" ] || [ "$rc" != "1" ] || [ "$verdict" != "exact" ]; then
		fail "49 targetRules mutant exposes the global path prefilter" "exit=$rc parser=$pyrc $verdict"
	else
		pass "49 removing targetRules hides the target file in dir mode; only the other-path witness remains"
	fi
fi

# ── 50 · changing AND to OR hides both a path neighbor and another-path exact match ───
d="$(new_repo route-digest-mutant-condition)" || exit 2
plant_route_digest_file "$d" "$digest_fixture_rel" route_digest_near_miss_line
plant_route_digest_file "$d" "$digest_other_rel" route_digest_exact_line
if ! mutate_route_digest_block "$d" condition; then
	fail "50 could create the condition mutant" "the copied block did not contain condition AND"
else
	git -C "$d" add -A
	git -C "$d" commit -qm "condition mutant with both witnesses"
	out="$WORK/50.out"
	rc="$(run_gate "$d" "$out")"
	if [ "$rc" != "0" ] || ! grep -q "CLEAN" "$out"; then
		fail "50 OR mutant hides both witnesses" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
	else
		pass "50 changing AND to OR hides the near miss and other-path exact declaration"
	fi
fi

# ── 51 · removing the path term hides the exact declaration on another path ──────────
d="$(new_repo route-digest-mutant-path)" || exit 2
plant_route_digest_file "$d" "$digest_other_rel" route_digest_exact_line
if ! mutate_route_digest_block "$d" path; then
	fail "51 could create the path mutant" "the copied block did not contain its anchored path"
else
	git -C "$d" add -A
	git -C "$d" commit -qm "path mutant with another-path witness"
	out="$WORK/51.out"
	rc="$(run_gate "$d" "$out")"
	if [ "$rc" != "0" ] || ! grep -q "CLEAN" "$out"; then
		fail "51 path mutant hides the other-path witness" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
	else
		pass "51 removing paths hides the exact declaration on another path"
	fi
fi

# ── 52 · widening one captured nibble hides the one-byte near miss ────────────────────
d="$(new_repo route-digest-mutant-capture)" || exit 2
plant_route_digest_file "$d" "$digest_fixture_rel" route_digest_near_miss_line
if ! mutate_route_digest_block "$d" capture; then
	fail "52 could create the capture mutant" "the copied block did not contain the exact final nibble"
else
	git -C "$d" add -A
	git -C "$d" commit -qm "capture mutant with a one-byte witness"
	out="$WORK/52.out"
	rc="$(run_gate "$d" "$out")"
	if [ "$rc" != "0" ] || ! grep -q "CLEAN" "$out"; then
		fail "52 capture mutant hides the one-byte witness" "got exit $rc; output: $(tr '\n' ' ' <"$out" | head -c 300)"
	else
		pass "52 widening the final captured nibble hides the one-byte near miss"
	fi
fi

# ── 53-57 · the NARROWED extraction of the sweep (SG2, 2026-09-11) ─────────────────────
# The all-ref sweep may stop Git diffing an internal design note (not shipped)**/*.md, which the global allowlist
# exempts before any rule, but only when the guard proves the rules would see nothing different.
# Every case runs the gate twice — narrowed (the default) and OLIVARES_SECRETS_NARROW_EXTRACTION=0 —
# and requires the SAME exit code and the SAME named findings. Measured without the guard
# (assessments/engineering/secrets-exemption-equivalence-20260911): 54 loses its finding, 55 gains
# material the full extraction never scans, 56 loses its finding, 57 hides a now-scanned mailbox.
findings_of() { grep -E '^  (rule|file|commit|fingerprint)' "$1" | LC_ALL=C sort; }
narrow_pair() { # <dir> <case> ; sets n_rc (narrowed) and f_rc (full)
	n_rc="$(OLIVARES_SECRETS_SCOPE=all run_gate "$1" "$WORK/$2.n.out")"
	f_rc="$(OLIVARES_SECRETS_SCOPE=all OLIVARES_SECRETS_NARROW_EXTRACTION=0 run_gate "$1" "$WORK/$2.f.out")"
}
same_verdict() { # <case>
	[ "$n_rc" = "$f_rc" ] && [ "$(findings_of "$WORK/$1.n.out")" = "$(findings_of "$WORK/$1.f.out")" ]
}
filler() { local i; for i in $(seq 1 "$2"); do printf 'line %s %s filler text for rename similarity\n' "$1" "$i"; done; }
commit_all() { # <dir> <message>
	git -C "$1" add -A && git -C "$1" commit -qm "$2" || { setup_fail "git-commit" "$2"; exit 2; }
}

# 53 · the guard passes: mailbox changes are not diffed, the findings are the full extraction's
d="$(new_repo narrow-engage)" || exit 2
mkdir -p "$d/docs" "$d/sessions/status/inbox/sub"
plant_decoy "$d/docs/key.pem"
plant_decoy "$d/sessions/status/inbox/BOX.md"
filler nested 3 >"$d/sessions/status/inbox/sub/deep.md"
commit_all "$d" "a scanned key and an exempt mailbox"
filler more 2 >>"$d/sessions/status/inbox/BOX.md"
commit_all "$d" "a mailbox-only commit"
narrow_pair "$d" 53
if [ "$n_rc" != "1" ] || ! same_verdict 53; then
	fail "53 narrowed sweep names the same findings" "narrowed exit $n_rc, full exit $f_rc; findings differ or no finding"
elif ! grep -q "extraction narrowed" "$WORK/53.n.out" || ! grep -q "NOTE — extraction was narrowed" "$WORK/53.n.out"; then
	fail "53 says the extraction was narrowed" "the narrowed run does not say so before and after the scan"
elif ! grep -q "is NOT the non-merge" "$WORK/53.n.out"; then
	fail "53 does not pass the count off as the census" "the note that 'scanned' is not the census is missing"
elif ! grep -q "narrowing disabled by OLIVARES_SECRETS_NARROW_EXTRACTION=0" "$WORK/53.f.out"; then
	fail "53 the switch restores the full extraction" "the =0 run does not announce the full extraction"
else
	pass "53 narrowed sweep: same exit and findings as the full extraction, and it says the count is not the census"
fi

# 54 · an ADDED exempt mailbox steals the rename source of a non-exempt file -> full extraction
d="$(new_repo narrow-steal)" || exit 2
mkdir -p "$d/docs" "$d/sessions/status/inbox"
plant_decoy "$WORK/54.key"
{ filler steal 40; cat "$WORK/54.key"; } >"$d/docs/src2.txt"
commit_all "$d" "a source carrying a key"
git -C "$d" rm -q docs/src2.txt
mkdir -p "$d/docs" # git rm removed the now-empty directory; without it the destination is never written
{ filler steal 40; cat "$WORK/54.key"; printf 'x\n'; } >"$d/sessions/status/inbox/steal.md"
{ filler steal 26; filler dstonly 12; cat "$WORK/54.key"; } >"$d/docs/dst2.txt"
commit_all "$d" "an exempt mailbox takes the source"
narrow_pair "$d" 54
if ! same_verdict 54 || ! grep -q "file       : docs/dst2.txt" "$WORK/54.n.out"; then
	fail "54 a stolen rename source keeps its finding" "narrowed exit $n_rc, full exit $f_rc; the docs/dst2.txt finding must survive"
elif ! grep -q "full extraction — commit .* rename pairing of a non-exempt path" "$WORK/54.n.out"; then
	fail "54 falls back for the pairing change" "no fallback reason naming the rename pairing"
else
	pass "54 exempt mailbox stealing a rename source -> full extraction, finding kept"
fi

# 55 · a mailbox renamed OUT to a scanned path -> full extraction (no extra material either)
d="$(new_repo narrow-rename-out)" || exit 2
mkdir -p "$d/docs" "$d/sessions/status/inbox"
plant_decoy "$WORK/55.key"
{ filler out 30; cat "$WORK/55.key"; } >"$d/sessions/status/inbox/moved.md"
commit_all "$d" "a key in an exempt mailbox"
git -C "$d" mv sessions/status/inbox/moved.md docs/moved.md
printf 'appended line\n' >>"$d/docs/moved.md"
commit_all "$d" "the mailbox moves to a scanned path"
narrow_pair "$d" 55
if ! same_verdict 55; then
	fail "55 rename out of the mailbox scans what the full extraction scans" "narrowed exit $n_rc, full exit $f_rc; findings differ"
elif ! grep -q "full extraction — commit .* rename pairing of a non-exempt path" "$WORK/55.n.out"; then
	fail "55 falls back for the rename out" "no fallback reason naming the rename pairing"
else
	pass "55 mailbox renamed out to a scanned path -> full extraction, same verdict"
fi

# 56 · a line feed in a mailbox name: the glob would skip it, Go's '.' does not exempt it
d="$(new_repo narrow-lf)" || exit 2
mkdir -p "$d/sessions/status/inbox"
plant_decoy "$d/sessions/status/inbox/a
b.md"
commit_all "$d" "a key under a name with a line feed"
narrow_pair "$d" 56
if [ "$n_rc" != "1" ] || ! same_verdict 56; then
	fail "56 a line-feed mailbox name keeps its finding" "narrowed exit $n_rc, full exit $f_rc; findings differ or none"
elif ! grep -q "full extraction — a path under the mailbox glob is not exempt" "$WORK/56.n.out"; then
	fail "56 falls back for the unexempted name" "no fallback reason naming the path"
else
	pass "56 line feed in a mailbox name -> full extraction, finding kept"
fi

# 57 · a config that no longer exempts the mailboxes -> full extraction, the mailbox is scanned
d="$(new_repo narrow-config)" || exit 2
python3 - "$d/.gitleaks.toml" <<'PY' || { setup_fail "mutate-config-57" "$d/.gitleaks.toml"; exit 2; }
import sys
p = sys.argv[1]; t = open(p, encoding='utf-8').read(); old = "'''sessions/.*\\.md$'''"
if t.count(old) != 1:
    sys.exit(3)
open(p, 'w', encoding='utf-8').write(t.replace(old, "'''sessions/.*\\.markdown$'''"))
PY
mkdir -p "$d/sessions/status/inbox"
plant_decoy "$d/sessions/status/inbox/BOX.md"
commit_all "$d" "a config without the mailbox exemption"
narrow_pair "$d" 57
if [ "$n_rc" != "1" ] || ! same_verdict 57; then
	fail "57 without the exemption the mailbox is scanned" "narrowed exit $n_rc, full exit $f_rc; findings differ or none"
elif ! grep -q "full extraction — no unconditional global allowlist" "$WORK/57.n.out"; then
	fail "57 falls back for the missing exemption" "no fallback reason naming the config"
else
	pass "57 config without the mailbox exemption -> full extraction, mailbox finding named"
fi

# 58 · copy detection turns a MODIFIED mailbox into a copy source -> full extraction
# The full extraction reads docs/copy.txt as a copy of the mailbox (no added lines, CLEAN); a
# narrowed one would read it as a new file and name the key. Same verdict only if it falls back.
d="$(new_repo narrow-copies)" || exit 2
git -C "$d" config diff.renames copies
mkdir -p "$d/docs" "$d/sessions/status/inbox"
plant_decoy "$WORK/58.key"
{ filler copy 30; cat "$WORK/58.key"; } >"$d/sessions/status/inbox/BOX.md"
commit_all "$d" "a key in an exempt mailbox"
cp "$d/sessions/status/inbox/BOX.md" "$d/docs/copy.txt"
printf 'appended\n' >>"$d/sessions/status/inbox/BOX.md"
commit_all "$d" "the mailbox changes and a scanned copy of it appears"
narrow_pair "$d" 58
if [ "$n_rc" != "0" ] || ! same_verdict 58; then
	fail "58 copy detection keeps the full verdict" "narrowed exit $n_rc, full exit $f_rc; findings differ or none"
elif ! grep -q "full extraction — diff.renames is not a plain on/off" "$WORK/58.n.out"; then
	fail "58 falls back under copy detection" "no fallback reason naming diff.renames"
else
	pass "58 diff.renames=copies -> full extraction, same verdict"
fi

# ── 59-63 · ONE captured subject for the sweep (SG4, 2026-09-11) ───────────────────────
# SG3 F1: the guard proved one population and gitleaks re-read another. The race is made
# deterministic with a PATH wrapper at the guard->scan boundary (SG3 repro/reach.sh): it lands
# changes, runs the REAL scanner, then undoes part of them (A->B->A for config and a side ref).
REAL_GITLEAKS="$(command -v gitleaks)"
mkdir -p "$WORK/race-bin"
cat >"$WORK/race-bin/gitleaks" <<'SH'
#!/usr/bin/env bash
[ -n "${RACE_REPO:-}" ] || exec "$REAL_GITLEAKS" "$@"
bash "$RACE_SCRIPT" before "$RACE_REPO"
"$REAL_GITLEAKS" "$@"; rc=$?
bash "$RACE_SCRIPT" after "$RACE_REPO"
exit "$rc"
SH
chmod +x "$WORK/race-bin/gitleaks"
cat >"$WORK/race.sh" <<'SH'
set -u
# A concurrent writer is another process on the LIVE store: it must not inherit the gate's
# snapshot environment, or it would write into the capture instead of racing it.
unset GIT_DIR GIT_CONFIG_NOSYSTEM GIT_CONFIG_GLOBAL GIT_INDEX_FILE
d="$2"; cd "$d" || exit 2
if [ "$1" = before ]; then
	cp .gitleaks.toml "$d.cfg-saved"
	python3 -c "import sys; p='.gitleaks.toml'; t=open(p).read(); open(p,'w').write(t.replace(\"'''sessions/.*\\\\.md\$''',\", ''))"
	git config diff.renames copies
	key="$(mktemp)"; bash -c "$PLANT" _ "$key"
	blob="$(git hash-object -w "$key")"; rm -f "$key"
	export GIT_INDEX_FILE="$d.race-index"; git read-tree HEAD
	git update-index --add --cacheinfo "100644,$blob,sessions/status/inbox/c
d.md"
	git update-ref refs/heads/main "$(git commit-tree "$(git write-tree)" -p HEAD -m late)"
	git read-tree --empty; git update-index --add --cacheinfo "100644,$blob,docs/side.pem"
	git update-ref refs/heads/late-side "$(git commit-tree "$(git write-tree)" -m side)"
	rm -f "$GIT_INDEX_FILE"
else
	cp "$d.cfg-saved" .gitleaks.toml; git config --unset diff.renames; git update-ref -d refs/heads/late-side
fi
SH
PLANT="$(declare -f plant_decoy); plant_decoy \"\$1\""
export REAL_GITLEAKS PLANT

# 59 · HEAD advances, a side ref lands and config/diff.renames go A->B->A inside the window
d="$(new_repo subject-race)" || exit 2
mkdir -p "$d/sessions/status/inbox"
printf 'mailbox\n' >"$d/sessions/status/inbox/BOX.md"
commit_all "$d" "a clean mailbox"
pre="$(git -C "$d" rev-parse HEAD)"
n_rc="$(RACE_REPO="$d" RACE_SCRIPT="$WORK/race.sh" PATH="$WORK/race-bin:$PATH" OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/59.n.out")"
git -C "$d" update-ref refs/heads/main "$pre"
f_rc="$(RACE_REPO="$d" RACE_SCRIPT="$WORK/race.sh" PATH="$WORK/race-bin:$PATH" OLIVARES_SECRETS_SCOPE=all OLIVARES_SECRETS_NARROW_EXTRACTION=0 run_gate "$d" "$WORK/59.f.out")"
late_rc="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/59.late.out")"
if [ "$n_rc" != "0" ] || ! same_verdict 59; then
	fail "59 narrowed and full agree on the captured subject" "narrowed exit $n_rc, full exit $f_rc; findings differ or a late change was scanned"
elif ! grep -q "captured subject — HEAD ${pre:0:12}" "$WORK/59.n.out" || ! grep -q "captured subject — HEAD ${pre:0:12}" "$WORK/59.f.out"; then
	fail "59 names the captured HEAD" "the subject line does not name the HEAD that was captured"
elif grep -q "side.pem\|inbox/c" "$WORK/59.n.out" "$WORK/59.f.out"; then
	fail "59 does not qualify what landed after the capture" "a change that arrived in the window was scanned"
elif [ "$late_rc" != "1" ] || ! grep -q 'file       : sessions/status/inbox/c\\nd.md' "$WORK/59.late.out"; then
	fail "59 the late commit is judged when it is captured" "a later sweep got exit $late_rc and did not name it"
else
	pass "59 concurrent HEAD/ref/config change: same verdict on the captured subject; later capture names it"
fi

# 60 · a detached linked worktree and an annotated tag are the only carriers of two findings
d="$(new_repo subject-worktree-tag)" || exit 2
git -C "$d" worktree add -q --detach "$WORK/wt60" HEAD
plant_decoy "$WORK/wt60/wt-only.pem"
commit_all "$WORK/wt60" "only a detached linked worktree carries this"
git -C "$d" worktree add -q --detach "$WORK/tag60" HEAD
plant_decoy "$WORK/tag60/tag-only.pem"
commit_all "$WORK/tag60" "only a tag carries this"
git -C "$d" tag -a tag-only -m tag-only "$(git -C "$WORK/tag60" rev-parse HEAD)"
git -C "$d" worktree remove --force "$WORK/tag60"
narrow_pair "$d" 60
if ! same_verdict 60 || ! grep -q "file       : wt-only.pem" "$WORK/60.n.out" || ! grep -q "file       : tag-only.pem" "$WORK/60.n.out"; then
	fail "60 worktree HEAD and tag stay in the captured population" "narrowed exit $n_rc, full exit $f_rc; a carrier-only finding is missing"
elif ! grep -q "carried by: refs/tags/tag-only" "$WORK/60.n.out"; then
	fail "60 keeps the original ref label" "the tag finding does not name refs/tags/tag-only"
else
	pass "60 detached linked-worktree and tag-only findings: named, same verdict, tag label kept"
fi

# 61 · an [extend] path is an input the snapshot does not freeze -> COULD NOT LOOK, never clean
d="$(new_repo subject-extend)" || exit 2
python3 - "$d/.gitleaks.toml" <<'PY' || { setup_fail "extend-61" "$d"; exit 2; }
import sys
p = sys.argv[1]; t = open(p).read()
if t.count('useDefault = true') != 1:
    sys.exit(3)
open(p, 'w').write(t.replace('useDefault = true', 'useDefault = true\npath = "extra.toml"'))
PY
printf 'title = "extra"\n' >"$d/extra.toml"
plant_decoy "$d/key.pem"
commit_all "$d" "a config that extends another file"
narrow_pair "$d" 61
if [ "$n_rc" != "2" ] || [ "$f_rc" != "2" ]; then
	fail "61 extend: COULD NOT LOOK in both modes" "narrowed exit $n_rc, full exit $f_rc"
elif ! grep -q "NO CAPTURED SUBJECT — .*extend" "$WORK/61.n.out" || grep -q "extraction narrowed\|CLEAN" "$WORK/61.n.out"; then
	fail "61 extend: refuses to capture and does not narrow" "no explicit refusal naming [extend], or it narrowed / went clean"
else
	pass "61 [extend] path -> no captured subject: exit 2 in both modes, never clean"
fi

# 62 · capture failure is COULD NOT LOOK, never a clean. Measured first as a live fallback: a
# grafts file sent that sweep to "0 commit(s)" and CLEAN over a planted key.
d="$(new_repo subject-capture-fail)" || exit 2
plant_decoy "$d/key.pem"
commit_all "$d" "a finding under a grafted history marker"
: >"$d/.git/info/grafts"
n_rc="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/62.n.out")"
if [ "$n_rc" != "2" ] || ! grep -q "NO CAPTURED SUBJECT" "$WORK/62.n.out" || grep -q "CLEAN" "$WORK/62.n.out"; then
	fail "62 capture failure is COULD NOT LOOK" "exit $n_rc; no explicit refusal, or a CLEAN verdict"
else
	pass "62 snapshot refusal -> exit 2 naming the refusal, never clean"
fi

# 63 · the effective diff.renames is carried into the snapshot, not reset to git's default
d="$(new_repo subject-renames-off)" || exit 2
git -C "$d" config diff.renames false
mkdir -p "$d/docs"
plant_decoy "$d/docs/a.pem"
commit_all "$d" "a key"
git -C "$d" mv docs/a.pem docs/b.pem
commit_all "$d" "a rename git reports as delete+add when renames are off"
narrow_pair "$d" 63
h_rc="$(run_gate "$d" "$WORK/63.h.out")"
if [ "$n_rc" != "1" ] || ! same_verdict 63 || [ "$(findings_of "$WORK/63.h.out")" != "$(findings_of "$WORK/63.n.out")" ]; then
	fail "63 diff.renames=false survives the capture" "all-ref exit $n_rc/$f_rc vs HEAD-scope exit $h_rc; findings differ"
elif ! grep -q "diff.renames=false" "$WORK/63.n.out" || ! grep -q "file       : docs/b.pem" "$WORK/63.n.out"; then
	fail "63 names the pinned rename mode" "the subject line or the delete+add finding is missing"
else
	pass "63 diff.renames=false: all-ref narrowed = full = live HEAD scope"
fi

# ── 64-68 · a scan that could not read its subject is COULD NOT LOOK (SG6, 2026-09-11) ────
# SG5 F2, reproduced first on this gate: objects pruned mid-scan -> gitleaks rc 0, report `[]`,
# `ERR [git] fatal: bad object …`, and the gate said CLEAN. Each guard has a case only it can
# see — 65 the post-scan census, 66 the scanner log — and 64 is the reviewer's shape (both).
mkdir -p "$WORK/prune-bin"
cat >"$WORK/prune-bin/gitleaks" <<'SH'
#!/usr/bin/env bash
prune() { (cd "$PRUNE_REPO" && unset GIT_DIR GIT_CONFIG_NOSYSTEM GIT_CONFIG_GLOBAL &&
	git branch -D side >/dev/null 2>&1; git reflog expire --expire=now --all >/dev/null 2>&1
	git prune --expire=now >/dev/null 2>&1); }
[ "${PRUNE_WHEN:-}" != before ] || prune
"$REAL_GITLEAKS" "$@"; rc=$?
[ "${PRUNE_WHEN:-}" != after ] || prune
exit "$rc"
SH
chmod +x "$WORK/prune-bin/gitleaks"
side_key_repo() { # <name> ; a key only on branch side; echoes the path
	local d; d="$(new_repo "$1")" || return 2
	git -C "$d" checkout -q -b side && plant_decoy "$d/key.pem" && commit_all "$d" "a key on a side branch" &&
		git -C "$d" checkout -q main && printf '%s' "$d"
}

# 64 · objects of a captured ref pruned before the scanner reads them
d="$(side_key_repo read-prune-before)" || exit 2
n_rc="$(PRUNE_REPO="$d" PRUNE_WHEN=before PATH="$WORK/prune-bin:$PATH" OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/64.n.out")"
d="$(side_key_repo read-prune-before-full)" || exit 2
f_rc="$(PRUNE_REPO="$d" PRUNE_WHEN=before PATH="$WORK/prune-bin:$PATH" OLIVARES_SECRETS_SCOPE=all OLIVARES_SECRETS_NARROW_EXTRACTION=0 run_gate "$d" "$WORK/64.f.out")"
if [ "$n_rc" != "2" ] || [ "$f_rc" != "2" ] || grep -q "CLEAN" "$WORK/64.n.out" "$WORK/64.f.out"; then
	fail "64 objects pruned mid-scan are COULD NOT LOOK" "narrowed exit $n_rc, full exit $f_rc (SG5 F2 was CLEAN, exit 0)"
elif ! grep -q "ERR \[git\] fatal" "$WORK/64.n.out"; then
	fail "64 names the scanner's own Git error" "the ERR [git] line it refused on is not shown"
else
	pass "64 objects pruned inside the scan -> exit 2 in both modes, Git error named, never CLEAN"
fi

# 65 · the read succeeded, then the captured refs stopped answering: only the census can see it
d="$(side_key_repo read-prune-after)" || exit 2
n_rc="$(PRUNE_REPO="$d" PRUNE_WHEN=after PATH="$WORK/prune-bin:$PATH" OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/65.n.out")"
if [ "$n_rc" != "2" ] || ! grep -q "no longer answer their census" "$WORK/65.n.out"; then
	fail "65 a subject unreadable after a clean read is COULD NOT LOOK" "exit $n_rc; no census refusal"
elif grep -q "ERR \[git\]" "$WORK/65.n.out"; then
	fail "65 isolates the census direction" "the scanner logged a Git error, so the guards are not told apart"
else
	pass "65 captured refs unreadable after the scan -> exit 2 from the census alone"
fi

# 66 · a missing blob or tree while every commit still counts: only the scanner log can see it
for kind in blob tree; do
	d="$(new_repo "read-missing-$kind")" || exit 2
	mkdir -p "$d/docs/sub"
	printf 'plain\n' >"$d/docs/sub/a.txt"
	commit_all "$d" "a file whose $kind goes missing"
	if [ "$kind" = blob ]; then o="$(git -C "$d" rev-parse HEAD:docs/sub/a.txt)"; else o="$(git -C "$d" rev-parse HEAD:docs/sub)"; fi
	rm -f "$d/.git/objects/${o:0:2}/${o:2}"
	rc66="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/66.$kind.out")"
	if [ "$rc66" != "2" ] || ! grep -q "error line(s) while reading history" "$WORK/66.$kind.out"; then
		fail "66 a missing $kind is COULD NOT LOOK" "exit $rc66 (before SG6: CLEAN, 'scanned 0 commit(s)')"
	elif ! git -C "$d" rev-list --count --all >/dev/null 2>&1; then
		fail "66 isolates the scanner-log direction" "the commit census fails too, so the guards are not told apart"
	else
		pass "66 missing $kind with every commit readable -> exit 2 from the scanner log alone"
	fi
done

# 67 · per-worktree refs: this worktree's by name, another worktree's as an unlabelled tip
d="$(new_repo read-worktree-refs)" || exit 2
git -C "$d" worktree add -q --detach "$WORK/wt67" HEAD
base67="$(git -C "$d" rev-parse HEAD)"
key_commit() { # <repo> <path> ; a commit on base67 that adds a key at <path>; echoes its id
	local k blob tree; k="$(mktemp)"; plant_decoy "$k"
	blob="$(git -C "$1" hash-object -w "$k")"; rm -f "$k"
	GIT_INDEX_FILE="$WORK/67.idx" git -C "$1" read-tree "$base67"
	GIT_INDEX_FILE="$WORK/67.idx" git -C "$1" update-index --add --cacheinfo "100644,$blob,$2"
	tree="$(GIT_INDEX_FILE="$WORK/67.idx" git -C "$1" write-tree)"; rm -f "$WORK/67.idx"
	git -C "$1" commit-tree "$tree" -p "$base67" -m "$2"
}
git -C "$d" update-ref refs/worktree/only-here "$(key_commit "$d" here-only.pem)"
git -C "$WORK/wt67" update-ref refs/bisect/bad "$(key_commit "$WORK/wt67" other-worktree-only.pem)"
narrow_pair "$d" 67
if ! same_verdict 67 || ! grep -q "file       : here-only.pem" "$WORK/67.n.out"; then
	fail "67 this worktree's refs/worktree stays in the subject" "narrowed $n_rc, full $f_rc; here-only.pem missing or findings differ"
elif ! grep -q "carried by: refs/worktree/only-here" "$WORK/67.n.out"; then
	fail "67 keeps the per-worktree ref name" "no carrier refs/worktree/only-here"
elif ! grep -q "file       : other-worktree-only.pem" "$WORK/67.n.out" || ! grep -q "1 other-worktree ref tip(s)" "$WORK/67.n.out"; then
	fail "67 another worktree's refs/bisect is captured" "other-worktree-only.pem not named, or its tip not counted"
else
	pass "67 per-worktree refs kept (by name here, as a tip for another worktree); narrowed = full"
fi

# 68 · another worktree's per-worktree refs cannot be read -> refusal, not a smaller subject
#
# Use a self-referential symlink to produce ELOOP for any reader. Unlike a
# chmod-based fixture, this does not depend on the reader's DAC privileges.
# The parent must still list the entry so the real snapshot reader attempts
# enumeration. Measure the premise before asserting the gate's refusal.
walk_errno() { # <path> ; the errno NAME of the first refusal, `-` if the path enumerates, and
	# empty if the measurement itself could not be taken. Same `os.walk` with `onerror` that
	# scripts/secrets-scan-snapshot.py reads per-worktree refs with, so it answers for the access
	# THIS process really has on this box, not for a set of permission bits that may not apply to it.
	python3 - "$1" 2>/dev/null <<-'PY'
	import errno, os, sys
	errs = []
	for _ in os.walk(sys.argv[1], onerror=lambda e: errs.append(e)):
	    pass
	print(errno.errorcode.get(errs[0].errno, errs[0].errno) if errs else '-')
	PY
}
d="$(new_repo read-enumeration-refusal)" || exit 2
git -C "$d" worktree add -q --detach "$WORK/wt68" HEAD
git -C "$WORK/wt68" update-ref refs/bisect/bad HEAD
wtgd="$(git -C "$WORK/wt68" rev-parse --absolute-git-dir)"
rm -rf "$wtgd/refs/bisect" && ln -s bisect "$wtgd/refs/bisect"
seen68="$(walk_errno "$wtgd/refs/bisect")"
if [ "$seen68" != "ELOOP" ]; then
	# No cleanup here on purpose: the EXIT trap removes $WORK, and a `rm` of a path whose shape
	# we just failed to establish would report its own error over the setup failure that matters.
	case "$seen68" in
	'-') setup_fail "unreadable-refs-68" "the per-worktree refs entry is still enumerable by this reader" ;;
	'') setup_fail "unreadable-refs-68" "could not measure whether the per-worktree refs entry enumerates" ;;
	*) setup_fail "unreadable-refs-68" "the entry is refused as $seen68, not by path resolution: the case would stand on a permission again" ;;
	esac
	exit 2
fi
n_rc="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/68.n.out")"
rm -f "$wtgd/refs/bisect"
if [ "$n_rc" != "2" ] || ! grep -q "NO CAPTURED SUBJECT — .*per-worktree refs" "$WORK/68.n.out"; then
	fail "68 unreadable per-worktree refs are a refusal" "exit $n_rc; no refusal naming per-worktree refs"
else
	pass "68 another worktree's refs unreadable -> no captured subject, exit 2"
fi

# ── 69 · a foreign GIT_DIR in the environment neither redirects nor touches (SG7, 2026-09-11) ──
# Before SG7 the gate scanned whatever GIT_DIR named: exported at a foreign repository carrying a
# key, it answered DIRTY about THAT repository while run from a clean checkout. Both scopes, and the
# foreign repository must not move by one byte.
d="$(new_repo isolation-checkout)" || exit 2
foreign="$(new_repo isolation-foreign)" || exit 2
plant_decoy "$foreign/foreign-only.pem"
commit_all "$foreign" "a key only the foreign repository carries"
foreign_print() { (cd "$1" && find .git -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum); }
before69="$(foreign_print "$foreign")"
iso_fail=""
for scope in head all; do
	c_rc="$(OLIVARES_SECRETS_SCOPE=$scope run_gate "$d" "$WORK/69.$scope.clean.out")"
	p_rc="$(GIT_DIR="$foreign/.git" OLIVARES_SECRETS_SCOPE=$scope run_gate "$d" "$WORK/69.$scope.poison.out")"
	if [ "$c_rc" != "0" ] || [ "$p_rc" != "$c_rc" ] || grep -q "foreign-only.pem" "$WORK/69.$scope.poison.out"; then
		iso_fail="$scope: clean exit $c_rc, foreign-GIT_DIR exit $p_rc, foreign finding named $(grep -c 'foreign-only.pem' "$WORK/69.$scope.poison.out")"
		break
	fi
done
if [ -n "$iso_fail" ]; then
	fail "69 a foreign GIT_DIR does not redirect the scan" "$iso_fail"
elif [ "$(foreign_print "$foreign")" != "$before69" ]; then
	fail "69 the foreign repository is untouched" "its .git changed while GIT_DIR pointed at it"
else
	pass "69 foreign GIT_DIR exported: both scopes judge the invoking checkout (clean), foreign repo untouched"
fi

# ══ 70-72 · THE THREE R74 FIXTURE EXCEPTIONS: permanent residual coverage ═════════════
#
# The `secrets` job 103550537584 of mainline-ci 34692649320 named three findings reachable
# from community 495781cbda91. All three are synthetic fixture values already present in
# reachable history, adjudicated in .gitleaks.toml with one block each: ONE rule, ONE
# anchored path, ONE anchored exact captured Secret, `condition = "AND"`, no `commits`.
#
# These three cases are the permanent statement of what each block does and does not exempt.
# Per block, eight controls: the exact capture on its own path is CLEAN; the same specimen
# judged without the block is DIRTY (the no-fire control — without it a typo in the fragments
# would pass just as well); a changed value, an unrelated value of the same rule, and another
# rule on that same path are each still found; the exact capture on a non-exempt path is still
# found; and the dir-mode witness, which is the only place a dropped `targetRules` is visible,
# because in `--no-git` mode a global block skips a whole file by path before any regex runs.
#
# Every specimen line is ASSEMBLED FROM FRAGMENTS at runtime, for the reason plant_decoy gives:
# written whole, this file would carry three captures on a path no block names and would be the
# next finding. Reports are written outside the specimen. The dir-mode control runs with cwd AT
# the specimen root and `--source .`, so the reported File is the intended relative path — from
# the parent it gains a directory prefix, the anchored `paths` term cannot match, and the cell
# then proves nothing about an applicable exemption. A setup failure, a missing or unparseable
# report, an unexpected exit code, or an error-level scanner line is a FAILURE, never a
# successful negative.

# error-level scanner lines, wherever they sit on the line: the production gate's own logs are
# timestamped (`3:30PM ERR …`), so anchoring at column 1 would read a real scanner failure as
# silence. Prints scanner error lines or a log-read diagnostic; empty output means no match.
scanner_errors() {
	local scanner_rc
	grep -nE '(^|[[:space:]])(ERR|FTL|PNC)([[:space:]]|$)' "$1" 2>/dev/null
	scanner_rc=$?
	if [ "$scanner_rc" -gt 1 ]; then
		# Callers reject nonempty diagnostics; a read error must never look like no matches.
		printf 'SCANNER_LOG_READ_FAILURE\n'
		return "$scanner_rc"
	fi
	return 0
}

# These inputs fail independently of Unix permissions, including when the runner is root.
for unreadable_log in "$WORK/missing-scanner-log" "$WORK"; do
	read_error="$(scanner_errors "$unreadable_log")"
	read_rc=$?
	if [ "$read_rc" -le 1 ] || [ -z "$read_error" ]; then
		setup_fail "scanner-log-read" "a missing file or directory became a clean scanner log"
		exit 2
	fi
done

# the exempted line of each block, the same line with the LAST byte of the capture changed, and
# an unrelated synthetic value of the same rule. Fragments only.
r74_line() {
	case "$1" in
	F1) printf '\twitness = "%s%s%s%s"\n' 'AKIA' 'SUPPORT' 'BUNDLE' 'WIT' ;;
	F2) printf '\t\tPassword: "%s%s%s"})\n' 'other-' 'pass-' '12345' ;;
	F3) printf "const SECRET_SENTINEL = '%s%s%s%s'\n" 'k3-' 'synthetic-' 'secret-' '4f2b9c17e05d43a8' ;;
	esac
}
r74_near_miss() {
	case "$1" in
	F1) printf '\twitness = "%s%s%s%s"\n' 'AKIA' 'SUPPORT' 'BUNDLE' 'WIU' ;;
	F2) printf '\t\tPassword: "%s%s%s"})\n' 'other-' 'pass-' '12346' ;;
	F3) printf "const SECRET_SENTINEL = '%s%s%s%s'\n" 'k3-' 'synthetic-' 'secret-' '4f2b9c17e05d43a9' ;;
	esac
}
r74_other_value() {
	case "$1" in
	F1) printf '\twitness = "%s%s"\n' 'AKIA' 'QWERTYUIOPASDFGH' ;;
	F2) printf '\t\tPassword: "%s%s%s"})\n' 'Qp7zK4mN' '2vX9tR6w' 'B3sL8hJ1' ;;
	F3) printf "const SECRET_SENTINEL = '%s%s%s'\n" 'Qp7zK4mN' '2vX9tR6w' 'B3sL8hJ1' ;;
	esac
}
r74_plant() { # r74_plant <repo> <relpath> <producer>... — append the produced lines to one file
	local d="$1" rel="$2" fn
	shift 2
	mkdir -p "$d/$(dirname "$rel")" || return 2
	for fn in "$@"; do "$fn" "$R74_BLOCK" >>"$d/$rel" || return 2; done
}
r74_cut_block() { # r74_cut_block <config> <marker> — drop ONE block, keep every other rule
	python3 - "$1" "$2" <<'PYCUT'
import pathlib, sys
p = pathlib.Path(sys.argv[1]); t = p.read_text(encoding='utf-8')
i = t.find(sys.argv[2])
if i < 0:
    sys.exit(3)
j = t.find('# ── R74-F', i + len(sys.argv[2]))
p.write_text(t[:i] + (t[j:] if j >= 0 else ''), encoding='utf-8')
PYCUT
}

for r74_row in \
	"70 F1 aws-access-token cmd/olivares/support_bundle_http_wiring_test.go other/elsewhere.go" \
	"71 F2 generic-api-key cmd/olivares/inferenceproxy_contentfirewall_test.go other/elsewhere.go" \
	"72 F3 generic-api-key web/e2e-communications/engine-lifecycle.controls.ts other/elsewhere.ts"; do
	# shellcheck disable=SC2086
	set -- $r74_row
	n="$1" R74_BLOCK="$2" rule="$3" rel="$4" other="$5"
	marker="# ── R74-$R74_BLOCK ·"
	bad=""

	# a · the exact capture on its exact path -> CLEAN
	d="$(new_repo "r74-$R74_BLOCK-exact")" || exit 2
	r74_plant "$d" "$rel" r74_line || { setup_fail "plant" "$n a"; exit 2; }
	git -C "$d" add -A && git -C "$d" commit -qm "the exempted capture on its own path" >/dev/null || { setup_fail "commit" "$n a"; exit 2; }
	out="$WORK/$n-a.out"
	rc="$(run_gate "$d" "$out")"
	if [ "$rc" != "0" ]; then
		bad="a: the exact capture on $rel -> expected exit 0, got $rc; $(tr '\n' ' ' <"$out" | head -c 300)"
	elif ! grep -q "CLEAN" "$out"; then
		bad="a: exit 0 but the verdict is not CLEAN"
	elif [ -n "$(scanner_errors "$out")" ]; then
		bad="a: error-level scanner line(s): $(scanner_errors "$out" | head -2 | tr '\n' ' ')"
	fi

	# b · NO-FIRE CONTROL: the same specimen, judged without this block -> DIRTY, one finding
	if [ -z "$bad" ]; then
		d="$(new_repo "r74-$R74_BLOCK-nofire")" || exit 2
		r74_plant "$d" "$rel" r74_line || { setup_fail "plant" "$n b"; exit 2; }
		if ! r74_cut_block "$d/.gitleaks.toml" "$marker"; then
			bad="b: the block marker '$marker' is not in the config — the no-fire control cannot run, and that is not a pass"
		else
			git -C "$d" add -A && git -C "$d" commit -qm "the same specimen without the block" >/dev/null || { setup_fail "commit" "$n b"; exit 2; }
			out="$WORK/$n-b.out"
			rc="$(run_gate "$d" "$out")"
			if [ "$rc" != "1" ]; then
				bad="b: without the block, expected exit 1, got $rc — the specimen never fired, so a proves nothing"
			elif ! grep -q " 1 finding(s)" "$out" || ! grep -q "$rule" "$out" || ! grep -q "$rel" "$out"; then
				bad="b: without the block, expected exactly one $rule on $rel; got $(grep -o 'DIRTY.*' "$out" | head -1)"
			fi
		fi
	fi

	# c/d/e/f · the four residuals, each its own specimen, each exactly one named finding
	if [ -z "$bad" ]; then
		for spec in "c near-miss $rel r74_near_miss $rule" \
			"d other-value $rel r74_other_value $rule" \
			"e exact-elsewhere $other r74_line $rule" \
			"f other-rule $rel plant_private_key private-key"; do
			# shellcheck disable=SC2086
			set -- $spec
			leg="$1" tag="$2" at="$3" producer="$4" want="$5"
			d="$(new_repo "r74-$R74_BLOCK-$tag")" || exit 2
			if [ "$producer" = plant_private_key ]; then
				mkdir -p "$d/$(dirname "$at")" && plant_decoy "$d/$at" || { setup_fail "plant" "$n $leg"; exit 2; }
			else
				r74_plant "$d" "$at" "$producer" || { setup_fail "plant" "$n $leg"; exit 2; }
			fi
			git -C "$d" add -A && git -C "$d" commit -qm "$tag" >/dev/null || { setup_fail "commit" "$n $leg"; exit 2; }
			out="$WORK/$n-$leg.out"
			rc="$(run_gate "$d" "$out")"
			if [ "$rc" != "1" ]; then
				bad="$leg ($tag on $at): expected exit 1, got $rc; $(tr '\n' ' ' <"$out" | head -c 300)"
				break
			elif ! grep -q " 1 finding(s)" "$out" || ! grep -q "$want" "$out" || ! grep -q "$at" "$out"; then
				bad="$leg ($tag): expected exactly one $want on $at; got $(grep -o 'DIRTY.*' "$out" | head -1)"
				break
			elif [ -n "$(scanner_errors "$out")" ]; then
				bad="$leg ($tag): error-level scanner line(s): $(scanner_errors "$out" | head -2 | tr '\n' ' ')"
				break
			fi
		done
	fi

	# g · the DIR-mode witness, rooted AT the specimen with --source . so the reported File is
	# the intended relative path. The exempted path carries the exact value, near miss,
	# unrelated value, and a private key; the other path carries the exact capture as the witness that the
	# scanner ran. The exact capture on its own path is the only thing that must be absent.
	if [ -z "$bad" ]; then
		d="$(new_repo "r74-$R74_BLOCK-dir")" || exit 2
		r74_plant "$d" "$rel" r74_line r74_near_miss r74_other_value || { setup_fail "plant" "$n g"; exit 2; }
		plant_decoy "$WORK/r74-$R74_BLOCK-decoy" && cat "$WORK/r74-$R74_BLOCK-decoy" >>"$d/$rel" || { setup_fail "plant-decoy" "$n g"; exit 2; }
		r74_plant "$d" "$other" r74_line || { setup_fail "plant" "$n g"; exit 2; }
		# Cutting the block must expose the exact same-path capture as a fifth finding.
		for directory_cut in 0 1; do
			if [ "$directory_cut" = 1 ] && ! r74_cut_block "$d/.gitleaks.toml" "$marker"; then
				bad="g: cannot remove the block for the directory sensitivity control"
				break
			fi
			report="$WORK/$n-g-$directory_cut.json"
			out="$WORK/$n-g-$directory_cut.out"
			rm -f "$report"
			rc="$( (cd "$d" && gitleaks dir . --no-banner --redact --no-color -c "$d/.gitleaks.toml" \
				--report-format json --report-path "$report" >"$out" 2>&1); echo $?)"
			verdict="$(python3 - "$report" "$rel" "$other" "$rule" "$directory_cut" <<'PYDIR' 2>/dev/null
import collections, json, sys
try:
    data = json.load(open(sys.argv[1], encoding='utf-8'))
    actual = collections.Counter((row['RuleID'], row['File'].removeprefix('./')) for row in data)
except (OSError, ValueError, TypeError, KeyError):
    sys.exit(3)
rel, other, rule = sys.argv[2], sys.argv[3], sys.argv[4]
expected = collections.Counter({(rule, rel): 2 + int(sys.argv[5]), ('private-key', rel): 1, (rule, other): 1})
print('exact' if actual == expected else
      'missing: %s extra: %s' % (sorted((expected - actual).items()), sorted((actual - expected).items())))
PYDIR
	)"
			pyrc=$?
			if [ "$pyrc" != "0" ]; then
				bad="g: COULD NOT SCAN in dir mode — gitleaks dir exited $rc and left no parseable report; $(tr '\n' ' ' <"$out" | head -c 300)"
			elif [ "$rc" != "1" ]; then
				bad="g: dir mode found findings, so it must exit 1; got $rc; identities: $verdict"
			elif [ -n "$(scanner_errors "$out")" ]; then
				bad="g: error-level scanner line(s): $(scanner_errors "$out" | head -2 | tr '\n' ' ')"
			elif [ "$verdict" != "exact" ]; then
				bad="g: $verdict — the private key or the near miss gone means a whole-file path skip (targetRules dropped); the witness gone means the path or the condition widened; an extra $rule on $rel means the block no longer matches its capture"
			fi
			[ -z "$bad" ] || break
		done
	fi

	if [ -n "$bad" ]; then
		fail "$n R74-$R74_BLOCK ($rule on $rel) exempts its one capture and nothing else" "$bad"
	else
		pass "$n R74-$R74_BLOCK: exact capture on $rel exempt (no-fire control fires); changed value, unrelated value, other rule, the capture elsewhere and the dir-mode witness all still found"
	fi
done

# ── R107 · three HEAD generic-api-key fixtures (CI 34743156449). Same AND shape as R74:
# exact capture on its path is CLEAN; a last-byte change, an unrelated value, the capture
# on another path, and a private-key on the same path stay findings.
r107_line() {
	case "$1" in
	F1) printf '\tnulSuffixKeyRef = %s%s%s + "\\x00" + "evil"\n' 'custody' 'KeyRef' 'G1' ;;
	F2) printf '\tcustodyKeyRefG2  = "%s%s%s"\n' 'kr1_' '234567' 'abcdefghijklmnopqrs' ;;
	F3) printf '\t\t"token": {token, "%s%s%s%s"},\n' '34f641f77e6253bf' '2e1fc213488b10bc' '69d18b2bb81f2dee' '417f02674ef28893' ;;
	esac
}
r107_near_miss() {
	case "$1" in
	F1) printf '\tnulSuffixKeyRef = %s%s%s + "\\x00" + "evil"\n' 'custody' 'KeyRef' 'G0' ;;
	F2) printf '\tcustodyKeyRefG2  = "%s%s%s"\n' 'kr1_' '234567' 'abcdefghijklmnopqrt' ;;
	F3) printf '\t\t"token": {token, "%s%s%s%s"},\n' '34f641f77e6253bf' '2e1fc213488b10bc' '69d18b2bb81f2dee' '417f02674ef28894' ;;
	esac
}
r107_other_value() {
	case "$1" in
	F1) printf '\tnulSuffixKeyRef = "%s%s"\n' 'sk-live-' 'Qp7zK4mN2vX9tR6wB3s' ;;
	F2) printf '\tcustodyKeyRefG2  = "%s%s"\n' 'kr1_' 'Qp7zK4mN2vX9tR6wB3sL' ;;
	F3) printf '\t\t"token": {token, "%s%s"},\n' 'aa11bb22cc33dd44ee55ff6677889900' 'aa11bb22cc33dd44ee55ff6677889900' ;;
	esac
}
r107_plant() {
	local d="$1" rel="$2" fn
	shift 2
	mkdir -p "$d/$(dirname "$rel")" || return 2
	for fn in "$@"; do "$fn" "$R107_BLOCK" >>"$d/$rel" || return 2; done
}

for r107_row in \
	"74 F1 generic-api-key core/internal/store/sqlstore/finopscustodynul_test.go other/elsewhere.go" \
	"75 F2 generic-api-key core/internal/store/sqlstore/finopscustodymigration_test.go other/elsewhere.go" \
	"76 F3 generic-api-key core/auth/mutation_authority_internal_test.go other/elsewhere.go"; do
	# shellcheck disable=SC2086
	set -- $r107_row
	n="$1" R107_BLOCK="$2" rule="$3" rel="$4" other="$5"
	marker="# ── R107-$R107_BLOCK ·"
	bad=""

	d="$(new_repo "r107-$R107_BLOCK-exact")" || exit 2
	r107_plant "$d" "$rel" r107_line || { setup_fail "plant" "$n a"; exit 2; }
	git -C "$d" add -A && git -C "$d" commit -qm "the exempted capture on its own path" >/dev/null || { setup_fail "commit" "$n a"; exit 2; }
	out="$WORK/$n-a.out"
	rc="$(run_gate "$d" "$out")"
	if [ "$rc" != "0" ]; then
		bad="a: the exact capture on $rel -> expected exit 0, got $rc; $(tr '\n' ' ' <"$out" | head -c 300)"
	elif ! grep -q "CLEAN" "$out"; then
		bad="a: exit 0 but the verdict is not CLEAN"
	elif [ -n "$(scanner_errors "$out")" ]; then
		bad="a: error-level scanner line(s): $(scanner_errors "$out" | head -2 | tr '\n' ' ')"
	fi

	if [ -z "$bad" ]; then
		for spec in "c near-miss $rel r107_near_miss $rule" \
			"d other-value $rel r107_other_value $rule" \
			"e exact-elsewhere $other r107_line $rule" \
			"f other-rule $rel plant_private_key private-key"; do
			# shellcheck disable=SC2086
			set -- $spec
			leg="$1" tag="$2" at="$3" producer="$4" want="$5"
			d="$(new_repo "r107-$R107_BLOCK-$tag")" || exit 2
			if [ "$producer" = plant_private_key ]; then
				mkdir -p "$d/$(dirname "$at")" && plant_decoy "$d/$at" || { setup_fail "plant" "$n $leg"; exit 2; }
			else
				r107_plant "$d" "$at" "$producer" || { setup_fail "plant" "$n $leg"; exit 2; }
			fi
			git -C "$d" add -A && git -C "$d" commit -qm "$tag" >/dev/null || { setup_fail "commit" "$n $leg"; exit 2; }
			out="$WORK/$n-$leg.out"
			rc="$(run_gate "$d" "$out")"
			if [ "$rc" != "1" ]; then
				bad="$leg ($tag on $at): expected exit 1, got $rc; $(tr '\n' ' ' <"$out" | head -c 300)"
				break
			elif ! grep -q " 1 finding(s)" "$out" || ! grep -q "$want" "$out" || ! grep -q "$at" "$out"; then
				bad="$leg ($tag): expected exactly one $want on $at; got $(grep -o 'DIRTY.*' "$out" | head -1)"
				break
			elif [ -n "$(scanner_errors "$out")" ]; then
				bad="$leg ($tag): error-level scanner line(s): $(scanner_errors "$out" | head -2 | tr '\n' ' ')"
				break
			fi
		done
	fi

	if [ -n "$bad" ]; then
		fail "$n R107-$R107_BLOCK ($rule on $rel) exempts its one capture and nothing else" "$bad"
	else
		pass "$n R107-$R107_BLOCK: exact capture on $rel exempt; changed value, unrelated value, other rule and the capture elsewhere still found"
	fi
done

# ══ 73 · R89-C1: SCHEDULER PARALLELISM OF THE ALL-REF DETECT ═════════════════════════
#
# The sweep's detect PROCESS must receive GOMAXPROCS=4 when the caller named none (unset or empty),
# keep a positive caller value, and a malformed explicit value must be refused as COULD NOT LOOK
# before any capture or scan — Go would ignore it and schedule on every visible CPU. HEAD scope
# (pre-push) gets no default, no refusal and no new line. The oracle is the environment recorded by a
# shim that then execs the real scanner: the printed line alone would still pass with the export
# deleted. Every run first unsets the battery's own GOMAXPROCS, because a developer shell (and this
# workspace's environment file) exports 4 and would make the default indistinguishable from
# inheritance; the caller value is 3 for the same reason. The dirty run keeps exit 1 and the name.
REAL_GITLEAKS_73="$(command -v gitleaks)"
mkdir -p "$WORK/gmp-bin" || { setup_fail "mkdir" "$WORK/gmp-bin"; exit 2; }
cat >"$WORK/gmp-bin/gitleaks" <<'SH' || { setup_fail "write-shim" "$WORK/gmp-bin/gitleaks"; exit 2; }
#!/usr/bin/env bash
if [ "${1:-}" = detect ]; then printf '%s\n' "${GOMAXPROCS-<unset>}" >>"$GMP_RECORD"; fi
exec "$GMP_REAL" "$@"
SH
chmod +x "$WORK/gmp-bin/gitleaks" || { setup_fail "chmod-shim" "$WORK/gmp-bin/gitleaks"; exit 2; }
gmp_run() { # <dir> <scope> <value|-unset> <tag> ; echoes rc; record $WORK/73.<tag>.rec, output .out
	rm -f "$WORK/73.$4.rec"
	(
		unset GOMAXPROCS
		[ "$3" = "-unset" ] || export GOMAXPROCS="$3"
		GMP_RECORD="$WORK/73.$4.rec" GMP_REAL="$REAL_GITLEAKS_73" PATH="$WORK/gmp-bin:$PATH" \
			OLIVARES_SECRETS_SCOPE="$2" run_gate "$1" "$WORK/73.$4.out"
	)
}
gmp_rec() { cat "$WORK/73.$1.rec" 2>/dev/null; }
gmp_lines() { grep -c '^check-secrets: GOMAXPROCS=' "$WORK/73.$1.out"; }
d="$(new_repo gomaxprocs-clean)" || exit 2
dd="$(new_repo gomaxprocs-dirty)" || exit 2
plant_decoy "$dd/reachable.pem" || { setup_fail "plant" "$dd"; exit 2; }
{ git -C "$dd" add -A && git -C "$dd" commit -qm "a key HEAD reaches"; } || { setup_fail "git-commit" "$dd"; exit 2; }
gmp_bad=""
for spec in "a|-unset" "b|"; do
	tag="${spec%%|*}" value="${spec#*|}"
	rc="$(gmp_run "$d" all "$value" "$tag")"
	if [ "$rc" != "0" ] || [ "$(gmp_rec "$tag")" != "4" ] || [ "$(gmp_lines "$tag")" != "1" ] ||
		! grep -q '^check-secrets: GOMAXPROCS=4 (all-ref default) for gitleaks detect' "$WORK/73.$tag.out"; then
		gmp_bad="$tag (caller value '${value}'): exit $rc, detect received '$(gmp_rec "$tag" | tr '\n' ' ')', $(gmp_lines "$tag") GOMAXPROCS line(s); want 0, '4', one all-ref default line"
		break
	fi
done
if [ -z "$gmp_bad" ]; then
	rc="$(gmp_run "$d" all 3 c)"
	if [ "$rc" != "0" ] || [ "$(gmp_rec c)" != "3" ] || [ "$(gmp_lines c)" != "1" ] ||
		! grep -q '^check-secrets: GOMAXPROCS=3 (caller) for gitleaks detect' "$WORK/73.c.out"; then
		gmp_bad="c (caller 3): exit $rc, detect received '$(gmp_rec c | tr '\n' ' ')', $(gmp_lines c) GOMAXPROCS line(s); a valid caller limit must reach the scanner unchanged"
	fi
fi
if [ -z "$gmp_bad" ]; then
	rc="$(gmp_run "$dd" all -unset d)"
	if [ "$rc" != "1" ] || [ "$(gmp_rec d)" != "4" ] || ! grep -q 'reachable.pem' "$WORK/73.d.out"; then
		gmp_bad="d (dirty, unset): exit $rc, detect received '$(gmp_rec d | tr '\n' ' ')', finding named $(grep -c 'reachable.pem' "$WORK/73.d.out"); want 1, '4', named"
	fi
fi
if [ -z "$gmp_bad" ]; then
	i=0
	for value in 0 -2 abc "4 " 1e3 "$(printf '4\n4')" 1234567890; do
		i=$((i + 1))
		rc="$(gmp_run "$d" all "$value" "e$i")"
		if [ "$rc" != "2" ] || [ -n "$(gmp_rec "e$i")" ] ||
			! grep -q "COULD NOT LOOK — GOMAXPROCS=" "$WORK/73.e$i.out" || grep -q 'captured subject' "$WORK/73.e$i.out"; then
			gmp_bad="e$i (malformed '$(printf '%s' "$value" | tr '\n' '?')'): exit $rc, detect ran $(gmp_rec "e$i" | wc -l) time(s); want 2, COULD NOT LOOK naming GOMAXPROCS, refused before capture and scan"
			break
		fi
	done
fi
if [ -z "$gmp_bad" ]; then
	for spec in "f|-unset|<unset>" "g|abc|abc"; do
		IFS='|' read -r tag value want <<EOF
$spec
EOF
		rc="$(gmp_run "$d" head "$value" "$tag")"
		if [ "$rc" != "0" ] || [ "$(gmp_rec "$tag")" != "$want" ] || [ "$(gmp_lines "$tag")" != "0" ]; then
			gmp_bad="$tag (HEAD scope, caller '$value'): exit $rc, detect received '$(gmp_rec "$tag" | tr '\n' ' ')', $(gmp_lines "$tag") GOMAXPROCS line(s); HEAD scope must stay untouched"
			break
		fi
	done
fi
if [ -n "$gmp_bad" ]; then
	fail "73 all-ref detect: GOMAXPROCS default 4, caller kept, malformed refused, HEAD untouched" "$gmp_bad"
else
	pass "73 all-ref detect receives GOMAXPROCS=4 when unset/empty and the caller's 3 when set; clean 0, dirty 1 named; 7 malformed values -> 2 before capture; HEAD scope unchanged"
fi

# ══ 77 · per-commit HEAD admission vs explicit all-refs (r116, 2026-09-14) ═══
#
# Exact CI symptom: mainline-ci run 34854565361 secrets step
# "gitleaks (sweep over every ref, charges only what HEAD reaches)" ran
# 14:28:23Z–15:48:23Z and died on the inner 80m clock. Charging was already
# HEAD-reachable only. This case is the finite discriminator for selecting the
# existing HEAD-history mode for per-commit CI: charged findings stay equivalent,
# leftover-ref observation differs, argv keeps --all off the HEAD detect, and
# the workflow caller must not request all-refs.
c77=""
d="$(new_repo c77-head-hist)" || exit 2
plant_decoy "$d/head-hist.pem" || { setup_fail "plant" "77a"; exit 2; }
{ git -C "$d" add -A && git -C "$d" commit -qm "head-history leak"; } || { setup_fail "commit" "77a"; exit 2; }
rc_h="$(OLIVARES_SECRETS_SCOPE=head run_gate "$d" "$WORK/77a.head.out")"
rc_a="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/77a.all.out")"
if [ "$rc_h" != "1" ] || [ "$rc_a" != "1" ] ||
	! grep -q "admission scope=head" "$WORK/77a.head.out" ||
	! grep -q "admission scope=all" "$WORK/77a.all.out" ||
	! grep -q "head-hist.pem" "$WORK/77a.head.out" || ! grep -q "IN what you are merging" "$WORK/77a.head.out" ||
	! grep -q "head-hist.pem" "$WORK/77a.all.out" || ! grep -q "IN what you are merging" "$WORK/77a.all.out"; then
	c77="a HEAD-history leak: head=$rc_h all=$rc_a (want 1/1, both named IN HEAD, both label their scope)"
fi
if [ -z "$c77" ]; then
	d="$(new_repo c77-unrelated)" || exit 2
	git -C "$d" checkout -q -b abandoned
	plant_decoy "$d/leftover.pem" || { setup_fail "plant" "77b"; exit 2; }
	{ git -C "$d" add -A && git -C "$d" commit -qm "unrelated-branch leak"; } || { setup_fail "commit" "77b"; exit 2; }
	git -C "$d" checkout -q main
	rc_h="$(OLIVARES_SECRETS_SCOPE=head run_gate "$d" "$WORK/77b.head.out")"
	rc_a="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/77b.all.out")"
	if [ "$rc_h" != "0" ] || [ "$rc_a" != "0" ] ||
		grep -q "leftover.pem" "$WORK/77b.head.out" || ! grep -q "CLEAN" "$WORK/77b.head.out" ||
		! grep -q "leftover.pem" "$WORK/77b.all.out" || ! grep -q "NOT reachable from HEAD" "$WORK/77b.all.out"; then
		c77="b unrelated-branch: head=$rc_h all=$rc_a; HEAD must be CLEAN and silent, all-refs must name leftover as NOT reachable"
	fi
fi
if [ -z "$c77" ]; then
	d="$(new_repo c77-merge)" || exit 2
	git -C "$d" checkout -q -b topic
	plant_decoy "$d/merged.pem" || { setup_fail "plant" "77c"; exit 2; }
	{ git -C "$d" add -A && git -C "$d" commit -qm "topic leak"; } || { setup_fail "commit" "77c"; exit 2; }
	git -C "$d" checkout -q main
	git -C "$d" merge -q --no-ff topic -m "merge topic" || { setup_fail "merge" "77c"; exit 2; }
	git -C "$d" branch -D topic >/dev/null
	rc_h="$(OLIVARES_SECRETS_SCOPE=head run_gate "$d" "$WORK/77c.head.out")"
	rc_a="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/77c.all.out")"
	if [ "$rc_h" != "1" ] || [ "$rc_a" != "1" ] ||
		! grep -q "merged.pem" "$WORK/77c.head.out" || ! grep -q "IN what you are merging" "$WORK/77c.head.out" ||
		! grep -q "merged.pem" "$WORK/77c.all.out" || ! grep -q "IN what you are merging" "$WORK/77c.all.out"; then
		c77="c merge-reachable leak: head=$rc_h all=$rc_a (want 1/1, both named IN HEAD after the topic branch is deleted)"
	fi
fi
if [ -z "$c77" ]; then
	d="$(new_repo c77-inspect)" || exit 2
	rm -f "$d/.gitleaks.toml"
	rc_h="$(OLIVARES_SECRETS_SCOPE=head run_gate "$d" "$WORK/77d.head.out")"
	rc_a="$(OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/77d.all.out")"
	if [ "$rc_h" != "2" ] || [ "$rc_a" != "2" ] ||
		grep -q "CLEAN" "$WORK/77d.head.out" || grep -q "CLEAN" "$WORK/77d.all.out" ||
		! grep -q "COULD NOT LOOK" "$WORK/77d.head.out" || ! grep -q "COULD NOT LOOK" "$WORK/77d.all.out"; then
		c77="d inspection failure: head=$rc_h all=$rc_a (want 2/2, COULD NOT LOOK, never CLEAN)"
	fi
fi
if [ -z "$c77" ]; then
	REAL_GITLEAKS_77="$(command -v gitleaks)"
	mkdir -p "$WORK/c77-shim" || { setup_fail "mkdir" "$WORK/c77-shim"; exit 2; }
	cat >"$WORK/c77-shim/gitleaks" <<'SH' || { setup_fail "write-shim" "$WORK/c77-shim/gitleaks"; exit 2; }
#!/usr/bin/env bash
if [ "${1:-}" = detect ]; then printf 'ARGV %s\n' "$*" >>"$ARGV_RECORD"; fi
exec "$GMP_REAL" "$@"
SH
	chmod +x "$WORK/c77-shim/gitleaks" || { setup_fail "chmod-shim" "$WORK/c77-shim/gitleaks"; exit 2; }
	d="$(new_repo c77-argv)" || exit 2
	plant_decoy "$d/reachable.pem" || { setup_fail "plant" "77e"; exit 2; }
	{ git -C "$d" add -A && git -C "$d" commit -qm "reachable"; } || { setup_fail "commit" "77e"; exit 2; }
	git -C "$d" checkout -q -b extra
	echo more >"$d/extra.txt"
	{ git -C "$d" add -A && git -C "$d" commit -qm "extra-ref-only commit"; } || { setup_fail "commit" "77e-extra"; exit 2; }
	git -C "$d" checkout -q main
	ARGV_RECORD="$WORK/77e.head.argv" GMP_REAL="$REAL_GITLEAKS_77" PATH="$WORK/c77-shim:$PATH" \
		OLIVARES_SECRETS_SCOPE=head run_gate "$d" "$WORK/77e.head.out" >/dev/null
	ARGV_RECORD="$WORK/77e.all.argv" GMP_REAL="$REAL_GITLEAKS_77" PATH="$WORK/c77-shim:$PATH" \
		OLIVARES_SECRETS_SCOPE=all run_gate "$d" "$WORK/77e.all.out" >/dev/null
	head_argv="$(cat "$WORK/77e.head.argv" 2>/dev/null || true)"
	all_argv="$(cat "$WORK/77e.all.argv" 2>/dev/null || true)"
	if ! printf '%s\n' "$head_argv" | grep -q -- '--log-opts=HEAD' ||
		printf '%s\n' "$head_argv" | grep -q -- '--all' ||
		! printf '%s\n' "$all_argv" | grep -q -- '--all' ||
		grep -q 'commit(s) that are NOT part of this ref' "$WORK/77e.head.out"; then
		c77="e argv/census: HEAD argv=${head_argv:-empty}; all argv=${all_argv:-empty}; HEAD must use --log-opts=HEAD without --all and must not claim leftover-ref scan work"
	fi
fi
if [ -z "$c77" ]; then
	c77_wf="$(python3 - "$ROOT/.github/workflows/mainline-ci.yml" <<'PY'
import re, sys
path = sys.argv[1]
try:
    text = open(path, encoding="utf-8").read()
except OSError as e:
    print("cannot-read:%s" % e)
    sys.exit(0)
hits = []
for m in re.finditer(r"^        id: gitleaks\n", text, re.M):
    before = text[:m.start()]
    after = text[m.end():m.end() + 1200]
    name_m = None
    for nm in re.finditer(r"^      - name: (.+)$", before, re.M):
        name_m = nm
    name = name_m.group(1).strip() if name_m else ""
    block = after.split("\n      - name:")[0]
    scope = None
    for line in block.splitlines():
        stripped = line.lstrip()
        if stripped.startswith("#"):
            continue
        sm = re.search(r"OLIVARES_SECRETS_SCOPE:\s*(\S+)", line)
        if sm:
            scope = sm.group(1).strip().strip("'\"")
    hits.append((name, scope))
if not hits:
    print("no-gitleaks-step")
    sys.exit(0)
name, scope = hits[-1]
if not name:
    print("no-step-name")
    sys.exit(0)
if "every ref" in name.lower():
    print("step-name-claims-all-refs:%s" % name)
    sys.exit(0)
if scope == "all":
    print("caller-requests-all-refs")
    sys.exit(0)
if scope not in (None, "head"):
    print("unexpected-scope:%s" % scope)
    sys.exit(0)
print("ok")
PY
)"
	if [ "$c77_wf" != "ok" ]; then
		c77="f per-commit CI caller: $c77_wf (want HEAD admission, not all-refs, and no 'every ref' step name)"
	fi
fi
if [ -n "$c77" ]; then
	fail "77 HEAD vs all-refs: charged-finding equivalent; leftover observation differs; CI caller is HEAD admission" "$c77"
else
	pass "77 HEAD vs all-refs: charged findings equivalent on HEAD-history/unrelated/merge/inspect; leftover refs named only by all-refs; CI caller is HEAD admission"
fi

echo ""
if [ "$fails" -eq 0 ]; then
	echo "test-check-secrets: OK — 77/77"
	exit 0
fi
echo "test-check-secrets: $fails case(s) failed" >&2
exit 1
