#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation matrix for the EXECUTION-TIME INVARIANT (scripts/assert-cosign-binary.sh).
#
# WHY THIS FILE EXISTS AT ALL. Four rounds of adversarial review moved the cosign control
# from a static YAML gate to this script — and for a while the control had a 52-case test
# battery protecting the thing that was DEMOTED and no battery at all protecting the thing
# that became the guarantee. That asymmetry is exactly how a control rots: the demoted half
# is the one everybody keeps editing.
#
# HERMETIC BY CONSTRUCTION. Every case builds a stub `cosign` that reports a chosen version,
# computes its real SHA-256, and patches a COPY of the script's digest table to match. So
# the LOGIC is under test on any machine, with no network and no real cosign. Where the
# officially published binary happens to be present, one extra case asserts the real thing
# end-to-end; it is skipped, loudly, when it is not.
#
# Patching a copy is deliberate: the constants in the real script are reviewed POLICY, and a
# script whose policy could be overridden from the environment would not be a control. The
# tests therefore edit the policy in a throwaway copy rather than the script reading it from
# somewhere an attacker could reach.
# NO `set -e` HERE, DELIBERATELY. This file REPORTS failures through check(); `set -e`
# turns a failing assertion into a silent STOP instead, so the run ends after the last
# success and looks like a clean tail. That is exactly the failure mode these batteries
# exist to catch, and it bit this repository twice on 2026-07-25 — once truncating a
# 23-case battery at case 11, once truncating this one at case 2. Critical SETUP commands
# below carry an explicit `|| exit`; assertions must not abort the run.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/scripts/assert-cosign-binary.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-assert-cosign.XXXXXX")" || exit 1
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

# ${TMPDIR:-/tmp} is not guaranteed EXECUTABLE. The dev container mounts /tmp
# "tmpfs (rw,nosuid,nodev,noexec)": every stub created there dies at execve with
# EACCES, and the battery reads "31 passed, 16 failed" — red on exactly the cases
# that RUN a stub, green on the ones that only read output. That signature was
# misread as gate-contention flakiness four times before being measured
# (2026-07-31). Probe the workdir; when it cannot exec, fall back to a repo-local
# tempdir — the repo checkout lives on an exec filesystem by construction.
printf '#!/bin/sh\nexit 0\n' >"$WORK/.execprobe" && chmod +x "$WORK/.execprobe"
if ! "$WORK/.execprobe" >/dev/null 2>&1; then
	rm -rf "$WORK"
	WORK="$(mktemp -d "$ROOT/.tmpexec.XXXXXX")" || exit 1
fi
rm -f "$WORK/.execprobe"

BASH_BIN="$(command -v bash)"   # an emptied PATH must break the SUBJECT, not the harness

pass=0
fail=0
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-58s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-58s %s\n' "$1" "$2"
	fi
}

# make_cosign <dir> <version> [extra-bytes]  -> prints the stub's sha256
make_cosign() {
	local dir="$1" ver="$2" salt="${3:-}"
	mkdir -p "$dir"
	{
		echo '#!/usr/bin/env bash'
		echo "# ${salt}"
		echo "[ \"\${1:-}\" = version ] || { echo 'stub: only version'; exit 1; }"
		echo "echo 'GitVersion:    ${ver}'"
	} >"$dir/cosign"
	chmod 0755 "$dir/cosign"
	sha256sum "$dir/cosign" | awk '{print $1}'
}

# script_with <name> <sed-expressions...> -> path to a patched copy
script_with() {
	local name="$1"
	shift
	local out="$WORK/$name.sh"
	cp "$SRC" "$out"
	local e
	for e in "$@"; do
		sed -i "$e" "$out"
	done
	printf '%s' "$out"
}

set_table() { # set_table <script> <var> <digest> <artifact-name>
	python3 - "$1" "$2" "$3" "$4" <<'PY'
import re, sys
path, var, digest, name = sys.argv[1:5]
s = open(path).read()
block = f"{digest}  {name}\n"
if var == "APPROVED_DIGESTS":
    s = re.sub(r"(read -r -d '' APPROVED_DIGESTS <<'DIGESTS' \|\| true\n).*?(DIGESTS\n)",
               lambda m: m.group(1) + block + m.group(2), s, flags=re.S)
else:
    s = s.replace('MIGRATION_DIGESTS=""',
                  "read -r -d '' MIGRATION_DIGESTS <<'MDIGESTS' || true\n" + block + "MDIGESTS\n", 1)
open(path, "w").write(s)
PY
}

echo "cosign execution-time invariant — mutation matrix"

# --- the property: the reviewed ARTIFACT, not merely the reviewed VERSION ----------------
GOOD="$WORK/good"
good_sha="$(make_cosign "$GOOD" v2.6.4 official)"
S1="$(script_with accept)"
set_table "$S1" APPROVED_DIGESTS "$good_sha" cosign-linux-amd64
out="$(PATH="$GOOD:$PATH" bash "$S1" 2>&1)" && rc=0 || rc=$?
if [ "$rc" -eq 0 ] && grep -q "OK (v2.6.4, cosign-linux-amd64"; then check "a published artifact of the approved version is accepted" "rc=0" 0; else check "a published artifact of the approved version is accepted" "rc=0" 1; fi <<<"$out"
if grep -q "lane: approved"; then check "and reports which lane approved it" "lane named" 0; else check "and reports which lane approved it" "lane named" 1; fi <<<"$out"
if grep -q "verified binary is $GOOD/cosign"; then check "and prints the ABSOLUTE path it verified" "path echoed" 0; else check "and prints the ABSOLUTE path it verified" "path echoed" 1; fi <<<"$out"

# THE case: same version, different bytes. This is what a version-only check cannot see.
BAD="$WORK/selfbuilt"
make_cosign "$BAD" v2.6.4 "locally-built-different-bytes" >/dev/null
set +e
out_bad="$(PATH="$BAD:$PATH" bash "$S1" 2>&1)"
rc_bad=$?
if [ "$rc_bad" -ne 0 ]; then check "the APPROVED version with UNPUBLISHED bytes is REJECTED" "rc!=0" 0; else check "the APPROVED version with UNPUBLISHED bytes is REJECTED" "rc!=0" 1; fi
if grep -q "is not a published artifact of any approved version"; then check "and the diagnostic says exactly that" "semantic message" 0; else check "and the diagnostic says exactly that" "semantic message" 1; fi <<<"$out_bad"

# --- a different version ------------------------------------------------------------------
# Since the round-5 reorder the DIGEST decides, so an unapproved version is refused because
# its bytes are in no table — before it is ever executed. That is the stronger property: it
# does not depend on believing what the binary says about itself.
OLD="$WORK/old"
make_cosign "$OLD" v2.5.2 vulnerable >/dev/null
set +e
out_old="$(PATH="$OLD:$PATH" bash "$S1" 2>&1)"
rc_old=$?
if [ "$rc_old" -ne 0 ] && grep -q "is not a published artifact of any approved version"; then check "a different version is refused by its bytes, not its claim" "rc!=0" 0; else check "a different version is refused by its bytes, not its claim" "rc!=0" 1; fi <<<"$out_old"

# --- the escape hatch is real, loud, and does not lie -------------------------------------
out_esc="$(PATH="$BAD:$PATH" OLIVARES_COSIGN_ALLOW_UNOFFICIAL=1 bash "$S1" 2>&1)"
if grep -q "UNOFFICIAL BINARY ACCEPTED"; then check "the documented escape accepts an unofficial build" "banner present" 0; else check "the documented escape accepts an unofficial build" "banner present" 1; fi <<<"$out_esc"
if grep -q "must NEVER be set in a job that publishes"; then check "and says where it must never be used" "warning present" 0; else check "and says where it must never be used" "warning present" 1; fi <<<"$out_esc"

# --- silent-abort regression ---------------------------------------------------------------
# `version="$(cosign version | awk ...)"` under `set -euo pipefail` aborted the whole script
# at the assignment, printing NOTHING. Since the reorder the version call happens only AFTER
# the digest matched, so the subject here is a binary that IS in the table and still cannot
# run — the case where silence would be most misleading.
BROKEN="$WORK/broken"
mkdir -p "$BROKEN"
printf '#!/usr/bin/env bash\necho "boom: no cosign behind this shim" >&2\nexit 127\n' >"$BROKEN/cosign"
chmod 0755 "$BROKEN/cosign"
broken_sha="$(sha256sum "$BROKEN/cosign" | awk '{print $1}')"
S_broken="$(script_with broken)"
set_table "$S_broken" APPROVED_DIGESTS "$broken_sha" cosign-linux-amd64
set +e
out_br="$(PATH="$BROKEN:$PATH" bash "$S_broken" 2>&1)"
rc_br=$?
if [ "$rc_br" -ne 0 ]; then check "a cosign that cannot run fails the check" "rc!=0" 0; else check "a cosign that cannot run fails the check" "rc!=0" 1; fi
if [ -n "$out_br" ] && grep -q "version' failed"; then check "and says WHY instead of dying silently" "diagnostic present" 0; else check "and says WHY instead of dying silently" "diagnostic present" 1; fi <<<"$out_br"
if grep -q "boom: no cosign behind this shim"; then check "and surfaces what the binary itself said" "root cause shown" 0; else check "and surfaces what the binary itself said" "root cause shown" 1; fi <<<"$out_br"

# --- absent cosign is a hard failure, never a skip ----------------------------------------
set +e
# The ambient PATH, which on this container has coreutils and NO cosign — the real shape of
# "cosign is not installed". Blanking PATH entirely removed awk/sha256sum too, so the script
# died on a missing tool before it could report the missing cosign, and the case was
# measuring the fixture rather than the subject.
out_none="$("$BASH_BIN" "$S1" 2>&1)"
rc_none=$?
if [ "$rc_none" -ne 0 ] && grep -q "hard failure, not a skip"; then check "no cosign at all is a hard failure" "rc!=0" 0; else check "no cosign at all is a hard failure" "rc!=0" 1; fi <<<"$out_none"

# --- GITHUB_ENV handoff: later steps must be able to use the VERIFIED path ----------------
env_file="$WORK/github_env"
: >"$env_file"
PATH="$GOOD:$PATH" GITHUB_ENV="$env_file" bash "$S1" >/dev/null 2>&1
if grep -q "^OLIVARES_COSIGN_BIN=$GOOD/cosign$" "$env_file"; then check "it exports the verified absolute path for later steps" "GITHUB_ENV written" 0; else check "it exports the verified absolute path for later steps" "GITHUB_ENV written" 1; fi

# --- MIGRATION WINDOW: it must never degenerate into a lax allowlist ----------------------
MIG="$WORK/mig"
mig_sha="$(make_cosign "$MIG" v3.1.2 next-generation)"

# Open, in date: the migration version is accepted against ITS OWN table.
today="$(date -u +%Y-%m-%d)"
soon="$(date -u -d '+30 days' +%Y-%m-%d)"
S_open="$(script_with mig-open \
	"s|^MIGRATION_COSIGN=\"\"|MIGRATION_COSIGN=\"v3.1.2\"|" \
	"s|^MIGRATION_OPENED=\"\"|MIGRATION_OPENED=\"$today\"|" \
	"s|^MIGRATION_EXPIRES=\"\"|MIGRATION_EXPIRES=\"$soon\"|")"
set_table "$S_open" APPROVED_DIGESTS "$good_sha" cosign-linux-amd64
set_table "$S_open" MIGRATION_DIGESTS "$mig_sha" cosign-linux-amd64
out_mig="$(PATH="$MIG:$PATH" bash "$S_open" 2>&1)" && rc_mig=0 || rc_mig=$?
if [ "$rc_mig" -eq 0 ] && grep -q "lane: migration"; then check "an OPEN window accepts the migration version" "rc=0, lane reported" 0; else check "an OPEN window accepts the migration version" "rc=0, lane reported" 1; fi <<<"$out_mig"

out_still="$(PATH="$GOOD:$PATH" bash "$S_open" 2>&1)" && rc_still=0 || rc_still=$?
if [ "$rc_still" -eq 0 ] && grep -q "lane: approved"; then check "and the primary version still works during the window" "both lanes live" 0; else check "and the primary version still works during the window" "both lanes live" 1; fi <<<"$out_still"

# The migration version must still be judged by DIGEST, not waved through by version.
BADMIG="$WORK/badmig"
make_cosign "$BADMIG" v3.1.2 "unpublished-bytes" >/dev/null
set +e
PATH="$BADMIG:$PATH" bash "$S_open" >/dev/null 2>&1
rc_badmig=$?
if [ "$rc_badmig" -ne 0 ]; then check "a migration version with unpublished bytes is still rejected" "digest still enforced" 0; else check "a migration version with unpublished bytes is still rejected" "digest still enforced" 1; fi

# EXPIRED: the failure mode of a forgotten migration must be a red gate.
past_open="$(date -u -d '-60 days' +%Y-%m-%d)"
past_exp="$(date -u -d '-1 day' +%Y-%m-%d)"
S_exp="$(script_with mig-expired \
	"s|^MIGRATION_COSIGN=\"\"|MIGRATION_COSIGN=\"v3.1.2\"|" \
	"s|^MIGRATION_OPENED=\"\"|MIGRATION_OPENED=\"$past_open\"|" \
	"s|^MIGRATION_EXPIRES=\"\"|MIGRATION_EXPIRES=\"$past_exp\"|")"
set_table "$S_exp" APPROVED_DIGESTS "$good_sha" cosign-linux-amd64
set_table "$S_exp" MIGRATION_DIGESTS "$mig_sha" cosign-linux-amd64
set +e
out_exp="$(PATH="$MIG:$PATH" bash "$S_exp" 2>&1)"
rc_exp=$?
if [ "$rc_exp" -ne 0 ] && grep -q "migration window that CLOSED"; then check "an EXPIRED window refuses the migration version" "forgotten = red gate" 0; else check "an EXPIRED window refuses the migration version" "forgotten = red gate" 1; fi <<<"$out_exp"

# A window longer than the cap is refused outright — a window that can be extended by doing
# nothing is not a window.
long_exp="$(date -u -d '+200 days' +%Y-%m-%d)"
S_long="$(script_with mig-toolong \
	"s|^MIGRATION_COSIGN=\"\"|MIGRATION_COSIGN=\"v3.1.2\"|" \
	"s|^MIGRATION_OPENED=\"\"|MIGRATION_OPENED=\"$today\"|" \
	"s|^MIGRATION_EXPIRES=\"\"|MIGRATION_EXPIRES=\"$long_exp\"|")"
set_table "$S_long" APPROVED_DIGESTS "$good_sha" cosign-linux-amd64
set_table "$S_long" MIGRATION_DIGESTS "$mig_sha" cosign-linux-amd64
set +e
out_long="$(PATH="$GOOD:$PATH" bash "$S_long" 2>&1)"
rc_long=$?
if [ "$rc_long" -ne 0 ] && grep -q "over the 90-day maximum"; then check "a window over the cap fails CLOSED, even for the primary" "cap enforced" 0; else check "a window over the cap fails CLOSED, even for the primary" "cap enforced" 1; fi <<<"$out_long"

# A malformed window must fail closed rather than approve a version forever.
S_nodate="$(script_with mig-nodate "s|^MIGRATION_COSIGN=\"\"|MIGRATION_COSIGN=\"v3.1.2\"|")"
set_table "$S_nodate" APPROVED_DIGESTS "$good_sha" cosign-linux-amd64
set +e
out_nd="$(PATH="$GOOD:$PATH" bash "$S_nodate" 2>&1)"
rc_nd=$?
if [ "$rc_nd" -ne 0 ] && grep -q "a window with no end is not a window"; then check "a window with no dates fails CLOSED" "malformed refused" 0; else check "a window with no dates fails CLOSED" "malformed refused" 1; fi <<<"$out_nd"

S_notable="$(script_with mig-notable \
	"s|^MIGRATION_COSIGN=\"\"|MIGRATION_COSIGN=\"v3.1.2\"|" \
	"s|^MIGRATION_OPENED=\"\"|MIGRATION_OPENED=\"$today\"|" \
	"s|^MIGRATION_EXPIRES=\"\"|MIGRATION_EXPIRES=\"$soon\"|")"
set_table "$S_notable" APPROVED_DIGESTS "$good_sha" cosign-linux-amd64
set +e
out_nt="$(PATH="$GOOD:$PATH" bash "$S_notable" 2>&1)"
rc_nt=$?
if [ "$rc_nt" -ne 0 ] && grep -q "approved by version number alone"; then check "a window with no digest table fails CLOSED" "no version-only approval" 0; else check "a window with no digest table fails CLOSED" "no version-only approval" 1; fi <<<"$out_nt"

# --- ROUND 7 (H3): a row that does not parse is an ERROR, not an absent row ---------------
# Both earlier versions filtered rows and compared the survivors, so a malformed row simply
# vanished from the set and the comparison stayed green — the same shape as the mawk defect,
# where a pattern that never matched was indistinguishable from a rule that passed.
for bad in "gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg  cosign-linux-amd64|not lowercase hex" \
	"309779b0c4e409186b0a80daba99041fe2cf65a920ce645013901df6211895a  cosign-linux-amd64|is 63 chars" \
	"309779b0c4e409186b0a80daba99041fe2cf65a920ce645013901df6211895a9|has 1 field"; do
	row="${bad%%|*}"
	want="${bad##*|}"
	S_bad="$(script_with "badrow-$(echo "$want" | tr -cd 'a-z')")"
	python3 - "$S_bad" "$row" <<'PYINNER'
import re, sys
path, row = sys.argv[1:3]
s = open(path).read()
s = re.sub(r"(read -r -d '' APPROVED_DIGESTS <<'DIGESTS' \|\| true\n).*?(DIGESTS\n)",
           lambda m: m.group(1) + row + "\n" + m.group(2), s, flags=re.S)
open(path, "w").write(s)
PYINNER
	out_bad_row="$(PATH="$GOOD:$PATH" bash "$S_bad" 2>&1)"
	rc_bad_row=$?
	if [ "$rc_bad_row" -ne 0 ] && grep -q "$want" <<<"$out_bad_row"; then
		check "a malformed digest row is REFUSED ($want)" "no silent disappearance" 0
	else
		check "a malformed digest row is REFUSED ($want)" "accepted or wrong reason" 1
	fi
done

# And the approved table is validated on EVERY run, not only when a migration window is
# open — the committed policy keeps the window closed, so validating only then would mean
# validating almost never.
if grep -q '^validate_table "APPROVED_DIGESTS"' "$SRC"; then
	check "the approved table is validated unconditionally" "hoisted out of the window block" 0
else
	check "the approved table is validated unconditionally" "still inside the window block" 1
fi

# --- ISOLATION (adjudicated 2026-07-26): take the binary OFF PATH after authenticating ----
# The reviewer's H2: no static reader can decide whether a `run:` script signs with an
# unauthenticated cosign, because `run:` is a shell. Isolation changes the failure mode from
# silent to loud. Every precondition below is from the approved spec.
ISO="$WORK/iso"
mkdir -p "$ISO/rt" "$ISO/bin"
cp "$GOOD/cosign" "$ISO/bin/cosign"
S_iso="$(script_with isolate)"
set_table "$S_iso" APPROVED_DIGESTS "$good_sha" cosign-linux-amd64

: >"$ISO/env"
out_iso="$(cd "$WORK" && RUNNER_TEMP="$ISO/rt" GITHUB_ENV="$ISO/env" PATH="$ISO/bin:$PATH" bash "$S_iso" --isolate 2>&1)"
rc_iso=$?
if [ "$rc_iso" -eq 0 ] && grep -q "ISOLATED to"; then check "isolation succeeds and says so" "rc=0" 0; else check "isolation succeeds and says so" "rc=$rc_iso" 1; printf '%s\n' "$out_iso" | sed 's/^/          /'; fi <<<"$out_iso"

if [ ! -e "$ISO/bin/cosign" ]; then check "the original is MOVED, not copied" "source gone" 0; else check "the original is MOVED, not copied" "source still present" 1; fi

iso_path="$(sed -n 's/^OLIVARES_COSIGN_BIN=//p' "$ISO/env")"
if [ -n "$iso_path" ] && [ -x "$iso_path" ]; then check "the isolated path is exported and executable" "GITHUB_ENV written" 0; else check "the isolated path is exported and executable" "missing" 1; fi

if [ "$(sha256sum "$iso_path" | awk '{print $1}')" = "$good_sha" ]; then check "the isolated bytes are the authenticated bytes" "digest preserved" 0; else check "the isolated bytes are the authenticated bytes" "digest changed" 1; fi

if [ "$(stat -c '%a' "$(dirname "$iso_path")")" = "700" ]; then check "the isolation directory is private (0700)" "mode 700" 0; else check "the isolation directory is private (0700)" "mode $(stat -c '%a' "$(dirname "$iso_path")")" 1; fi

# A second cosign left on PATH would make isolation theatre; the postcondition must catch it.
mkdir -p "$ISO/bin2" "$ISO/rt2"
cp "$GOOD/cosign" "$ISO/bin2/cosign"
cp "$GOOD/cosign" "$ISO/bin2b/cosign" 2>/dev/null || { mkdir -p "$ISO/bin2b"; cp "$GOOD/cosign" "$ISO/bin2b/cosign"; }
: >"$ISO/env2"
out_two="$(cd "$WORK" && RUNNER_TEMP="$ISO/rt2" GITHUB_ENV="$ISO/env2" PATH="$ISO/bin2:$ISO/bin2b:$PATH" bash "$S_iso" --isolate 2>&1)"
rc_two=$?
if [ "$rc_two" -ne 0 ] && grep -q "still resolves on PATH after isolation"; then check "a SECOND cosign on PATH fails isolation" "theatre refused" 0; else check "a SECOND cosign on PATH fails isolation" "accepted with a leftover" 1; fi <<<"$out_two"

# Preconditions, each fail-closed.
mkdir -p "$ISO/bin3" && cp "$GOOD/cosign" "$ISO/bin3/cosign"
: >"$ISO/env3"
for spec in "RUNNER_TEMP unset|GITHUB_ENV=$ISO/env3|needs RUNNER_TEMP" \
	"RUNNER_TEMP relative|RUNNER_TEMP=rel GITHUB_ENV=$ISO/env3|is not absolute" \
	"GITHUB_ENV unset|RUNNER_TEMP=$ISO/rt|needs a writable GITHUB_ENV" \
	"unofficial escape|RUNNER_TEMP=$ISO/rt GITHUB_ENV=$ISO/env3 OLIVARES_COSIGN_ALLOW_UNOFFICIAL=1|refuses OLIVARES_COSIGN_ALLOW_UNOFFICIAL"; do
	name="${spec%%|*}"
	rest="${spec#*|}"
	envs="${rest%%|*}"
	want="${rest##*|}"
	# env only ADDS variables: unset the runner-ambient ones explicitly first, or the
	# "unset" cases pass on a laptop and fail inside a real Actions job (where
	# RUNNER_TEMP and GITHUB_ENV always exist in the environment).
	out_pre="$(cd "$WORK" && env -u RUNNER_TEMP -u GITHUB_ENV -u OLIVARES_COSIGN_ALLOW_UNOFFICIAL $envs PATH="$ISO/bin3:$PATH" bash "$S_iso" --isolate 2>&1)"
	rc_pre=$?
	if [ "$rc_pre" -ne 0 ] && grep -q "$want" <<<"$out_pre"; then
		check "isolation refuses: $name" "fail-closed" 0
	else
		check "isolation refuses: $name" "accepted or wrong reason" 1
	fi
done

if ! bash "$S_iso" --isolate --check-path /bin/true >/dev/null 2>&1; then check "--isolate and --check-path are mutually exclusive" "refused" 0; else check "--isolate and --check-path are mutually exclusive" "both accepted" 1; fi

# The default mode must NOT mutate anything: only --isolate moves files.
mkdir -p "$ISO/bin4" && cp "$GOOD/cosign" "$ISO/bin4/cosign"
PATH="$ISO/bin4:$PATH" bash "$S_iso" >/dev/null 2>&1
if [ -e "$ISO/bin4/cosign" ]; then check "the DEFAULT mode never moves the binary" "no side effect" 0; else check "the DEFAULT mode never moves the binary" "MUTATED without --isolate" 1; fi

# --- the shipped policy itself ------------------------------------------------------------
# The real table must cover every platform the release could run on, and the file as
# committed must have its window CLOSED.
if grep -q '^MIGRATION_COSIGN=""' "$SRC"; then check "the committed policy has NO open migration window" "single approved version" 0; else check "the committed policy has NO open migration window" "single approved version" 1; fi

n_digests="$(sed -n "/read -r -d '' APPROVED_DIGESTS/,/^DIGESTS$/p" "$SRC" | grep -cE '^[0-9a-f]{64}  cosign-')"
if [ "$n_digests" -eq 9 ]; then check "all 9 published plain binaries are pinned, not just linux-amd64" "9 digests" 0; else check "all 9 published plain binaries are pinned, not just linux-amd64" "9 digests" 1; fi

# --- the real published artifact, when it is present --------------------------------------
OFFICIAL="/workspace/.tools/bin-official"
if [ -x "$OFFICIAL/cosign" ]; then
	PATH="$OFFICIAL:$PATH" bash "$SRC" >/dev/null 2>&1
	check "the REAL published cosign passes the shipped policy" "end-to-end" $?
else
	printf '  SKIP  %-58s %s\n' "real published cosign not present at $OFFICIAL" "hermetic cases still ran"
fi

# --- ROUND 5: authenticate the BYTES BEFORE executing them -------------------------------
# An earlier revision ran `cosign version` first and hashed afterwards, so the check meant
# to decide whether a binary should run had already run it. The canary proves the order.
CANARY="$WORK/canary-was-executed"
EVIL="$WORK/evil"
mkdir -p "$EVIL"
{
	echo '#!/usr/bin/env bash'
	echo "touch '$CANARY'"
	echo "echo 'GitVersion:    v2.6.4'"
} >"$EVIL/cosign"
chmod 0755 "$EVIL/cosign"
S_order="$(script_with order)"
set_table "$S_order" APPROVED_DIGESTS "$good_sha" cosign-linux-amd64
set +e
PATH="$EVIL:$PATH" bash "$S_order" >/dev/null 2>&1
rc_evil=$?
if [ "$rc_evil" -ne 0 ]; then check "unauthenticated bytes are refused" "rc!=0" 0; else check "unauthenticated bytes are refused" "rc!=0" 1; fi
if [ ! -e "$CANARY" ]; then check "and were NEVER executed by the check itself" "canary absent" 0; else check "and were NEVER executed by the check itself" "canary absent" 1; fi

# The version cross-check exists to catch a hand-pasted table row labelled with the wrong
# version — the digest decides the verdict, the version confirms the label.
MISLABEL="$WORK/mislabel"
mis_sha="$(make_cosign "$MISLABEL" v9.9.9 mislabelled)"
S_mis="$(script_with mislabel)"
set_table "$S_mis" APPROVED_DIGESTS "$mis_sha" cosign-linux-amd64
set +e
out_mis="$(PATH="$MISLABEL:$PATH" bash "$S_mis" 2>&1)"
rc_mis=$?
if [ "$rc_mis" -ne 0 ] && grep -q "TABLE IS WRONG"; then check "a mislabelled table row is caught by the version cross-check" "table error" 0; else check "a mislabelled table row is caught by the version cross-check" "table error" 1; fi <<<"$out_mis"

# --- ROUND 5: --check-path, the mode the launcher uses ------------------------------------
if bash "$S1" --check-path "$GOOD/cosign" --quiet >/dev/null 2>&1; then check "--check-path accepts verified bytes quietly" "rc=0" 0; else check "--check-path accepts verified bytes quietly" "rc=0" 1; fi
if ! bash "$S1" --check-path "$BAD/cosign" --quiet >/dev/null 2>&1; then check "--check-path refuses unverified bytes" "rc!=0" 0; else check "--check-path refuses unverified bytes" "rc!=0" 1; fi

# --- ROUND 5: a future-dated window may not be used before it opens -----------------------
fut_open="$(date -u -d '+10 days' +%Y-%m-%d)"
fut_exp="$(date -u -d '+40 days' +%Y-%m-%d)"
S_fut="$(script_with mig-future \
	"s|^MIGRATION_COSIGN=\"\"|MIGRATION_COSIGN=\"v3.1.2\"|" \
	"s|^MIGRATION_OPENED=\"\"|MIGRATION_OPENED=\"$fut_open\"|" \
	"s|^MIGRATION_EXPIRES=\"\"|MIGRATION_EXPIRES=\"$fut_exp\"|")"
set_table "$S_fut" APPROVED_DIGESTS "$good_sha" cosign-linux-amd64
set_table "$S_fut" MIGRATION_DIGESTS "$mig_sha" cosign-linux-amd64
set +e
out_fut="$(PATH="$MIG:$PATH" bash "$S_fut" 2>&1)"
rc_fut=$?
if [ "$rc_fut" -ne 0 ] && grep -q "may not be used before it opens"; then check "a window may not be used BEFORE it opens" "future-dated refused" 0; else check "a window may not be used BEFORE it opens" "future-dated refused" 1; fi <<<"$out_fut"

# --- ROUND 5: the migration table must be as COMPLETE as the approved one -----------------
# A one-platform lane would refuse every other runner mid-migration.
S_partial="$(script_with mig-partial \
	"s|^MIGRATION_COSIGN=\"\"|MIGRATION_COSIGN=\"v3.1.2\"|" \
	"s|^MIGRATION_OPENED=\"\"|MIGRATION_OPENED=\"$today\"|" \
	"s|^MIGRATION_EXPIRES=\"\"|MIGRATION_EXPIRES=\"$soon\"|")"
set_table "$S_partial" MIGRATION_DIGESTS "$mig_sha" cosign-linux-amd64
set +e
out_part="$(PATH="$GOOD:$PATH" bash "$S_partial" 2>&1)"
rc_part=$?
if [ "$rc_part" -ne 0 ] && grep -q "missing platform(s) the approved table covers"; then check "a partial migration table fails CLOSED" "completeness enforced" 0; else check "a partial migration table fails CLOSED" "completeness enforced" 1; fi <<<"$out_part"

# Completeness is compared as a SET, not a count. Counting rows accepted a table with the
# right NUMBER of wrong platforms, and rejected a legitimate superset — and upstream adding
# a platform in the newer version is exactly what happens during a migration.
S_wrongset="$(script_with mig-wrongset \
	"s|^MIGRATION_COSIGN=\"\"|MIGRATION_COSIGN=\"v3.1.2\"|" \
	"s|^MIGRATION_OPENED=\"\"|MIGRATION_OPENED=\"$today\"|" \
	"s|^MIGRATION_EXPIRES=\"\"|MIGRATION_EXPIRES=\"$soon\"|")"
python3 - "$S_wrongset" "$mig_sha" <<'PYINNER'
import re, sys
path, digest = sys.argv[1:3]
s = open(path).read()
# Nine rows, all naming the SAME platform: the right count, the wrong set.
rows = "".join(f"{digest[:-1]}{i}  cosign-linux-amd64\n" for i in "0123456789"[:9])
s = s.replace('MIGRATION_DIGESTS=""',
              "read -r -d '' MIGRATION_DIGESTS <<'MDIGESTS' || true\n" + rows + "MDIGESTS\n", 1)
open(path, "w").write(s)
PYINNER
out_ws="$(PATH="$GOOD:$PATH" bash "$S_wrongset" 2>&1)"
rc_ws=$?
if [ "$rc_ws" -ne 0 ] && grep -q "missing platform(s)"; then check "the RIGHT COUNT of the WRONG platforms is refused" "set, not count" 0; else check "the RIGHT COUNT of the WRONG platforms is refused" "set, not count" 1; fi <<<"$out_ws"

# A superset is legitimate: upstream may publish a platform the older version lacked.
S_super="$(script_with mig-superset \
	"s|^MIGRATION_COSIGN=\"\"|MIGRATION_COSIGN=\"v3.1.2\"|" \
	"s|^MIGRATION_OPENED=\"\"|MIGRATION_OPENED=\"$today\"|" \
	"s|^MIGRATION_EXPIRES=\"\"|MIGRATION_EXPIRES=\"$soon\"|")"
python3 - "$S_super" "$mig_sha" "$good_sha" <<'PYINNER'
import re, sys
path, migd, goodd = sys.argv[1:4]
s = open(path).read()
s = re.sub(r"(read -r -d '' APPROVED_DIGESTS <<'DIGESTS' \|\| true\n).*?(DIGESTS\n)",
           lambda m: m.group(1) + f"{goodd}  cosign-linux-amd64\n" + m.group(2), s, flags=re.S)
rows = f"{migd}  cosign-linux-amd64\n" + f"{migd[:-1]}0  cosign-linux-loong64\n"
s = s.replace('MIGRATION_DIGESTS=""',
              "read -r -d '' MIGRATION_DIGESTS <<'MDIGESTS' || true\n" + rows + "MDIGESTS\n", 1)
open(path, "w").write(s)
PYINNER
out_su="$(PATH="$MIG:$PATH" bash "$S_super" 2>&1)"
rc_su=$?
if [ "$rc_su" -eq 0 ]; then check "a SUPERSET migration table is accepted" "new platforms allowed" 0; else check "a SUPERSET migration table is accepted" "legitimate cutover blocked" 1; printf '%s\n' "$out_su" | sed 's/^/          /'; fi

echo
echo "assert-cosign-binary: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
