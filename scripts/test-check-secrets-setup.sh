#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-check-secrets-setup.sh — focused fixture-setup regression for
# scripts/test-check-secrets.sh. Invoked by lint:secrets-gate before the
# complete 52-case battery. Does not source the candidate into this shell.
#
# Exit 0 = every setup control passed. Exit 1 = a named control failed.
# Exit 2 = could not run (missing tools, isolation, or scratch). Not a pass.
set -u

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"
CALLER="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "test-check-secrets-setup: not inside a git repository — could not run" >&2
	exit 2
}
CANDIDATE="$HERE/test-check-secrets.sh"
[ -r "$CANDIDATE" ] || {
	echo "test-check-secrets-setup: $CANDIDATE not found — could not run" >&2
	exit 2
}
command -v gitleaks >/dev/null 2>&1 || {
	echo "test-check-secrets-setup: gitleaks is not on PATH — could not run." >&2
	echo "test-check-secrets-setup: this is NOT a pass." >&2
	exit 2
}
REAL_GIT="$(command -v git)" || {
	echo "test-check-secrets-setup: git is not on PATH — could not run" >&2
	exit 2
}
REAL_CP="$(command -v cp)" || {
	echo "test-check-secrets-setup: cp is not on PATH — could not run" >&2
	exit 2
}
REAL_PYTHON="$(command -v python3)" || {
	echo "test-check-secrets-setup: python3 is not on PATH — could not run" >&2
	exit 2
}
REAL_CHMOD="$(command -v chmod)" || {
	echo "test-check-secrets-setup: chmod is not on PATH — could not run" >&2
	exit 2
}

_olivares_exec_workdir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/exec-workdir.sh"
# shellcheck source=/dev/null
. "$_olivares_exec_workdir" || {
	echo "FATAL: cannot source $_olivares_exec_workdir" >&2
	exit 2
}
unset _olivares_exec_workdir
WORK="$(olivares_pick_exec_workdir check-secrets-setup)" || {
	echo "test-check-secrets-setup: no scratch directory allows execve — could not run." >&2
	exit 2
}
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

fails=0
passes=0
pass() { printf '  ok   %s\n' "$1"; passes=$((passes + 1)); }
fail() {
	printf '  FAIL %s\n' "$1" >&2
	printf '       %s\n' "$2" >&2
	fails=$((fails + 1))
}

SHIMDIR="$WORK/shim"
mkdir -p "$SHIMDIR" "$WORK/receipts" "$WORK/out" || {
	echo "test-check-secrets-setup: cannot create scratch — could not run" >&2
	exit 2
}

cat >"$SHIMDIR/git" <<'SHIM'
#!/usr/bin/env bash
set -u
op="${CFP1_INJECT_OP:-}"
receipt="${CFP1_INJECT_RECEIPT:-}"
real="${CFP1_REAL_GIT:-}"
[ -n "$real" ] || { echo "cfp1-git-shim: missing CFP1_REAL_GIT" >&2; exit 127; }
dir=""
msg=""
is_commit=0
prev=""
for a in "$@"; do
	if [ "$prev" = "-C" ]; then
		dir="$a"
	fi
	if [ "$a" = "commit" ]; then
		is_commit=1
	fi
	if [ "$prev" = "-m" ] || [ "$prev" = "--message" ] || [ "$prev" = "-qm" ]; then
		msg="$a"
	fi
	prev="$a"
done
base=""
if [ -n "$dir" ]; then
	base="$(basename -- "$dir")"
fi
fire=0
subject=""
if [ "$op" = "git-commit-base" ] && [ "$is_commit" = 1 ] && [ "$msg" = "base" ]; then
	fire=1
	subject="$dir"
fi
if [ "$op" = "git-commit-base-attribution" ] && [ "$is_commit" = 1 ] && [ "$msg" = "base" ] && [ "$base" = "attribution" ]; then
	fire=1
	subject="$dir"
fi
if [ "$op" = "classifier-commit" ] && [ "$is_commit" = 1 ] && [ "$msg" = "current classifier in independent history" ]; then
	fire=1
	subject="$dir"
fi
if [ "$fire" = 1 ]; then
	if [ -n "$receipt" ]; then
		{
			printf 'FIRED=1\n'
			printf 'op=%s\n' "$op"
			printf 'path=%s\n' "$subject"
		} >>"$receipt"
	fi
	printf 'cfp1-git-shim: injected failure for %s\n' "$op" >&2
	exit 1
fi
exec "$real" "$@"
SHIM
cat >"$SHIMDIR/cp" <<'SHIM'
#!/usr/bin/env bash
set -u
op="${CFP1_INJECT_OP:-}"
receipt="${CFP1_INJECT_RECEIPT:-}"
real="${CFP1_REAL_CP:-}"
[ -n "$real" ] || { echo "cfp1-cp-shim: missing CFP1_REAL_CP" >&2; exit 127; }
if [ "$#" -lt 1 ]; then
	exec "$real" "$@"
fi
dest="${@: -1}"
base="$(basename -- "$dest")"
src="${1:-}"
base_src=""
if [ -n "$src" ]; then
	base_src="$(basename -- "$src")"
fi
fire=0
if [ "$op" = "copy-config" ] && [ "$base" = ".gitleaks.toml" ]; then
	fire=1
fi
if [ "$op" = "classifier-copy" ] && [ "$base" = "access_evidence_migration.go" ]; then
	fire=1
fi
if [ "$op" = "report-copy-42a" ] && [ "$base_src" = "42a.json" ]; then
	fire=1
fi
if [ "$fire" = 1 ]; then
	if [ -n "$receipt" ]; then
		{
			printf 'FIRED=1\n'
			printf 'op=%s\n' "$op"
			printf 'path=%s\n' "$dest"
		} >>"$receipt"
	fi
	printf 'cfp1-cp-shim: injected failure for %s\n' "$op" >&2
	exit 1
fi
exec "$real" "$@"
SHIM
cat >"$SHIMDIR/python3" <<'SHIM'
#!/usr/bin/env bash
set -u
op="${CFP1_INJECT_OP:-}"
receipt="${CFP1_INJECT_RECEIPT:-}"
real="${CFP1_REAL_PYTHON:-}"
[ -n "$real" ] || { echo "cfp2-python-shim: missing CFP1_REAL_PYTHON" >&2; exit 127; }
fire=0
subject=""
if [ "$op" = "write-malformed-report-42a" ]; then
	for a in "$@"; do
		base="$(basename -- "$a")"
		if [ "$base" = "42a.json" ]; then
			fire=1
			subject="$a"
		fi
	done
fi
if [ "$fire" = 1 ]; then
	if [ -n "$receipt" ]; then
		{
			printf 'FIRED=1\n'
			printf 'op=%s\n' "$op"
			printf 'path=%s\n' "$subject"
		} >>"$receipt"
	fi
	printf 'cfp2-python-shim: injected failure for %s\n' "$op" >&2
	exit 1
fi
exec "$real" "$@"
SHIM
cat >"$SHIMDIR/chmod" <<'SHIM'
#!/usr/bin/env bash
set -u
op="${CFP1_INJECT_OP:-}"
receipt="${CFP1_INJECT_RECEIPT:-}"
real="${CFP1_REAL_CHMOD:-}"
[ -n "$real" ] || { echo "cfp2-chmod-shim: missing CFP1_REAL_CHMOD" >&2; exit 127; }
dest=""
if [ "$#" -ge 1 ]; then
	dest="${@: -1}"
fi
base=""
if [ -n "$dest" ]; then
	base="$(basename -- "$dest")"
fi
fire=0
if [ "$op" = "gitleaks-shim-chmod" ] && [ "$base" = "gitleaks" ]; then
	fire=1
fi
if [ "$fire" = 1 ]; then
	if [ -n "$receipt" ]; then
		{
			printf 'FIRED=1\n'
			printf 'op=%s\n' "$op"
			printf 'path=%s\n' "$dest"
		} >>"$receipt"
	fi
	printf 'cfp2-chmod-shim: injected failure for %s\n' "$op" >&2
	exit 1
fi
exec "$real" "$@"
SHIM
"$REAL_CHMOD" +x "$SHIMDIR/git" "$SHIMDIR/cp" "$SHIMDIR/python3" "$SHIMDIR/chmod" || {
	echo "test-check-secrets-setup: cannot make shims executable — could not run" >&2
	exit 2
}

POISON="$WORK/poison"
mkdir -p "$POISON" || exit 2
git init -q -b main "$POISON" || {
	echo "test-check-secrets-setup: cannot create poison repository — could not run" >&2
	exit 2
}
git -C "$POISON" config user.email 'poison@example.invalid' || exit 2
git -C "$POISON" config user.name 'poison' || exit 2
printf 'poison-marker\n' >"$POISON/marker" || exit 2
git -C "$POISON" add -A || exit 2
git -C "$POISON" commit -qm poison-base || exit 2
POISON_HEAD="$(git -C "$POISON" rev-parse HEAD)" || exit 2

CALLER_HEAD="$(git -C "$CALLER" rev-parse HEAD)" || exit 2
CALLER_STATUS="$(git -C "$CALLER" status --porcelain)" || exit 2

caller_unchanged() {
	local head status
	head="$(git -C "$CALLER" rev-parse HEAD)" || return 1
	status="$(git -C "$CALLER" status --porcelain)" || return 1
	[ "$head" = "$CALLER_HEAD" ] || return 1
	[ "$status" = "$CALLER_STATUS" ] || return 1
	return 0
}

poison_unchanged() {
	local head
	head="$(git -C "$POISON" rev-parse HEAD)" || return 1
	[ "$head" = "$POISON_HEAD" ] || return 1
	[ -f "$POISON/marker" ] || return 1
	return 0
}

receipt_fired() {
	local rec="$1" op="$2"
	[ -s "$rec" ] || return 1
	grep -qx 'FIRED=1' "$rec" || return 1
	grep -qx "op=$op" "$rec" || return 1
	grep -q '^path=' "$rec" || return 1
	return 0
}

no_case17_pass() {
	! grep -q '  ok   17 ' "$1"
}

no_case5_pass() {
	! grep -q '  ok   5 ' "$1"
}

no_case42_pass() {
	! grep -q '  ok   42 ' "$1"
}

no_generic_scanner_unavailable() {
	# A PATH miss is not proof that the named setup operation failed.
	! grep -q 'gitleaks is not on PATH' "$1"
}

setup_diag() {
	local err="$1" op="$2"
	grep -q "test-check-secrets: invalid fixture setup: $op failed" "$err"
}

run_candidate() {
	local cwd="$1" stdout="$2" stderr="$3" rc=0
	: >"${CFP1_INJECT_RECEIPT:-$WORK/receipts/empty}"
	(
		cd "$cwd" || exit 2
		env GIT_DIR="$POISON/.git" \
			CFP1_INJECT_OP="${CFP1_INJECT_OP:-}" \
			CFP1_INJECT_RECEIPT="${CFP1_INJECT_RECEIPT:-}" \
			CFP1_REAL_GIT="$REAL_GIT" \
			CFP1_REAL_CP="$REAL_CP" \
			CFP1_REAL_PYTHON="$REAL_PYTHON" \
			CFP1_REAL_CHMOD="$REAL_CHMOD" \
			PATH="$SHIMDIR:$PATH" \
			bash "$CANDIDATE"
	) >"$stdout" 2>"$stderr" || rc=$?
	printf '%s' "$rc"
}

make_min_root() {
	local dest="$1" with_classifier="${2:-}"
	mkdir -p "$dest/scripts/lib" || return 2
	cp "$CALLER/scripts/check-secrets.sh" "$dest/scripts/" || return 2
	# SG7: the gate sources its git-env sanitiser before any git call; a root without it is exit 2.
	cp "$CALLER/scripts/lib/git-env.sh" "$dest/scripts/lib/" || return 2
	cp "$CALLER/scripts/secrets-report-attribution.py" "$dest/scripts/" || return 2
	cp "$CALLER/.gitleaks.toml" "$dest/" || return 2
	if [ "$with_classifier" = "with-classifier" ]; then
		mkdir -p "$dest/core/internal/store/sqlstore" || return 2
		cp "$CALLER/core/internal/store/sqlstore/access_evidence_migration.go" \
			"$dest/core/internal/store/sqlstore/" || return 2
	fi
	git init -q -b main "$dest" || return 2
	git -C "$dest" config user.email 'minroot@example.invalid' || return 2
	git -C "$dest" config user.name 'minroot' || return 2
	git -C "$dest" add -A || return 2
	git -C "$dest" commit -qm minroot || return 2
	return 0
}

echo "test-check-secrets-setup: fixture-setup controls"

# ── specificity: cp shim fires only on the named destination ─────────────────────────
rec="$WORK/receipts/cp-specificity"
: >"$rec"
specdir="$WORK/spec-cp"
mkdir -p "$specdir" || exit 2
printf 'x\n' >"$specdir/readme.md" || exit 2
printf 'y\n' >"$specdir/src.toml" || exit 2
if ! env CFP1_INJECT_OP=copy-config CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_CP="$REAL_CP" "$SHIMDIR/cp" "$specdir/src.toml" "$specdir/readme.md"; then
	fail "cp shim is specific" "copy of readme.md failed under copy-config injection"
elif [ -s "$rec" ]; then
	fail "cp shim is specific" "non-matching copy wrote a fire receipt"
elif env CFP1_INJECT_OP=copy-config CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_CP="$REAL_CP" "$SHIMDIR/cp" "$specdir/src.toml" "$specdir/.gitleaks.toml"; then
	fail "cp shim is specific" "matching .gitleaks.toml copy succeeded"
elif ! receipt_fired "$rec" copy-config; then
	fail "cp shim is specific" "matching copy did not record a fire receipt"
elif ! grep -q '/.gitleaks.toml$' "$rec"; then
	fail "cp shim is specific" "receipt path is not the named destination"
else
	pass "cp shim fires only for .gitleaks.toml and records that path"
fi

# ── specificity: git shim fires only for the named disposable commit ─────────────────
rec="$WORK/receipts/git-specificity"
: >"$rec"
clean_repo="$WORK/spec-git/clean"
attr_repo="$WORK/spec-git/attribution"
mkdir -p "$clean_repo" "$attr_repo" || exit 2
git init -q -b main "$clean_repo" && git init -q -b main "$attr_repo" || exit 2
git -C "$clean_repo" config user.email 's@example.invalid'
git -C "$clean_repo" config user.name s
git -C "$attr_repo" config user.email 's@example.invalid'
git -C "$attr_repo" config user.name s
printf 'a\n' >"$clean_repo/a" && git -C "$clean_repo" add -A
printf 'b\n' >"$attr_repo/b" && git -C "$attr_repo" add -A
if ! env CFP1_INJECT_OP=git-commit-base-attribution CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_GIT="$REAL_GIT" "$SHIMDIR/git" -C "$clean_repo" commit -qm "base"; then
	fail "git shim is specific" "base commit in clean failed under attribution injection"
elif [ -s "$rec" ]; then
	fail "git shim is specific" "non-matching commit wrote a fire receipt"
elif env CFP1_INJECT_OP=git-commit-base-attribution CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_GIT="$REAL_GIT" "$SHIMDIR/git" -C "$attr_repo" commit -qm "base"; then
	fail "git shim is specific" "attribution base commit succeeded"
elif ! receipt_fired "$rec" git-commit-base-attribution; then
	fail "git shim is specific" "matching commit did not record a fire receipt"
elif ! grep -q '/attribution$' "$rec"; then
	fail "git shim is specific" "receipt path is not the attribution fixture"
else
	pass "git shim fires only for attribution base commit and records that path"
fi

# ── specificity: python3 shim fires only for the 42a generator path ──────────────────
rec="$WORK/receipts/python3-specificity"
: >"$rec"
specpy="$WORK/spec-python"
mkdir -p "$specpy" || exit 2
printf 'x\n' >"$specpy/other.json" || exit 2
if ! env CFP1_INJECT_OP=write-malformed-report-42a CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_PYTHON="$REAL_PYTHON" "$SHIMDIR/python3" -c 'print("ok")' >/dev/null; then
	fail "python3 shim is specific" "unrelated python3 -c failed under 42a injection"
elif [ -s "$rec" ]; then
	fail "python3 shim is specific" "unrelated python3 wrote a fire receipt"
elif env CFP1_INJECT_OP=write-malformed-report-42a CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_PYTHON="$REAL_PYTHON" "$SHIMDIR/python3" "$specpy/42a.json"; then
	fail "python3 shim is specific" "matching 42a.json invocation succeeded"
elif ! receipt_fired "$rec" write-malformed-report-42a; then
	fail "python3 shim is specific" "matching 42a.json invocation did not record a fire receipt"
else
	pass "python3 shim fires only for 42a.json and records that path"
fi

# ── specificity: chmod shim fires only for a gitleaks executable ─────────────────────
rec="$WORK/receipts/chmod-specificity"
: >"$rec"
specchmod="$WORK/spec-chmod"
mkdir -p "$specchmod" || exit 2
printf '#!/bin/sh\n' >"$specchmod/other" || exit 2
printf '#!/bin/sh\n' >"$specchmod/gitleaks" || exit 2
if ! env CFP1_INJECT_OP=gitleaks-shim-chmod CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_CHMOD="$REAL_CHMOD" "$SHIMDIR/chmod" +x "$specchmod/other"; then
	fail "chmod shim is specific" "chmod of other failed under gitleaks injection"
elif [ -s "$rec" ]; then
	fail "chmod shim is specific" "non-matching chmod wrote a fire receipt"
elif env CFP1_INJECT_OP=gitleaks-shim-chmod CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_CHMOD="$REAL_CHMOD" "$SHIMDIR/chmod" +x "$specchmod/gitleaks"; then
	fail "chmod shim is specific" "matching gitleaks chmod succeeded"
elif ! receipt_fired "$rec" gitleaks-shim-chmod; then
	fail "chmod shim is specific" "matching chmod did not record a fire receipt"
elif ! grep -q '/gitleaks$' "$rec"; then
	fail "chmod shim is specific" "receipt path is not the named gitleaks destination"
else
	pass "chmod shim fires only for gitleaks and records that path"
fi

# ── specificity: cp shim fires only for a 42a.json source under report-copy ──────────
rec="$WORK/receipts/report-copy-specificity"
: >"$rec"
spec42="$WORK/spec-report-copy"
mkdir -p "$spec42" || exit 2
printf '[]\n' >"$spec42/25.json" || exit 2
printf '[]\n' >"$spec42/42a.json" || exit 2
if ! env CFP1_INJECT_OP=report-copy-42a CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_CP="$REAL_CP" "$SHIMDIR/cp" "$spec42/25.json" "$spec42/dest.json"; then
	fail "report-copy cp shim is specific" "copy of 25.json failed under 42a injection"
elif [ -s "$rec" ]; then
	fail "report-copy cp shim is specific" "non-matching copy wrote a fire receipt"
elif env CFP1_INJECT_OP=report-copy-42a CFP1_INJECT_RECEIPT="$rec" \
	CFP1_REAL_CP="$REAL_CP" "$SHIMDIR/cp" "$spec42/42a.json" "$spec42/dest2.json"; then
	fail "report-copy cp shim is specific" "matching 42a.json copy succeeded"
elif ! receipt_fired "$rec" report-copy-42a; then
	fail "report-copy cp shim is specific" "matching copy did not record a fire receipt"
else
	pass "cp shim fires only for 42a.json source under report-copy-42a"
fi

# ── CFP2 correction: exact-byte case42b helper ───────────────────────────────────────
# Canonical b'[1, 2]' must pass. Parsed equality would accept a boolean or float
# substitute and a compact spelling; those must refuse.
spec42b="$WORK/case42b-bytes"
mkdir -p "$spec42b" || exit 2
printf '[1, 2]' >"$spec42b/canonical" || exit 2
printf '[true, 2]' >"$spec42b/boolean" || exit 2
printf '[1.0, 2]' >"$spec42b/float" || exit 2
printf '[1,2]' >"$spec42b/compact" || exit 2
expected42b_results="$spec42b/results.expected"
printf 'canonical=0\nboolean=1\nfloat=1\ncompact=1\n' >"$expected42b_results" || exit 2

# One exact admission check controls both the real helper result and its
# malformed-result regressions below. Byte equality refuses missing, extra,
# duplicate, reordered, or unexpected-status rows.
cfp2_case42b_results_exact() {
	local results="$1"
	[ -f "$results" ] || return 1
	cmp -s "$expected42b_results" "$results"
}

bytes42b_script="$WORK/cfp2-42b-bytes.sh"
bytes42b_out="$WORK/out/case42b-bytes.stdout"
bytes42b_err="$WORK/out/case42b-bytes.stderr"
cat >"$bytes42b_script" <<'BYTES42B'
set -u
cd "$CFP2_CALLER" || exit 2
# shellcheck source=/dev/null
. "$CFP2_CANDIDATE" || exit 2
spec="$CFP2_42B_SPEC"
: >"$spec/results" || exit 2
if cfp2_verify_case42b_specimen "$spec/canonical"; then
	printf 'canonical=0\n' >>"$spec/results" || exit 2
else
	rc=$?
	printf 'canonical=%s\n' "$rc" >>"$spec/results" || exit 2
fi
if cfp2_verify_case42b_specimen "$spec/boolean"; then
	printf 'boolean=0\n' >>"$spec/results" || exit 2
else
	rc=$?
	printf 'boolean=%s\n' "$rc" >>"$spec/results" || exit 2
fi
if cfp2_verify_case42b_specimen "$spec/float"; then
	printf 'float=0\n' >>"$spec/results" || exit 2
else
	rc=$?
	printf 'float=%s\n' "$rc" >>"$spec/results" || exit 2
fi
if cfp2_verify_case42b_specimen "$spec/compact"; then
	printf 'compact=0\n' >>"$spec/results" || exit 2
else
	rc=$?
	printf 'compact=%s\n' "$rc" >>"$spec/results" || exit 2
fi
printf 'CFP2_42B_BYTES_DONE\n' || exit 2
BYTES42B
bytes42b_rc=0
(
	cd "$CALLER" || exit 2
	env GIT_DIR="$POISON/.git" \
		CFP2_CALLER="$CALLER" \
		CFP2_CANDIDATE="$CANDIDATE" \
		CFP2_42B_SPEC="$spec42b" \
		bash "$bytes42b_script"
) >"$bytes42b_out" 2>"$bytes42b_err" || bytes42b_rc=$?
bytes42b_ready=0
if [ "$bytes42b_rc" != "0" ]; then
	fail "case42b helper accepts exact [1, 2] bytes" "child exit $bytes42b_rc stderr=$(tr '\n' ' ' <"$bytes42b_err" | head -c 240)"
elif ! grep -qx 'CFP2_42B_BYTES_DONE' "$bytes42b_out"; then
	fail "case42b helper accepts exact [1, 2] bytes" "missing CFP2_42B_BYTES_DONE"
elif ! cfp2_case42b_results_exact "$spec42b/results"; then
	fail "case42b helper accepts exact [1, 2] bytes" "result record is not the exact 0/1/1/1 sequence"
elif [ "$(sed -n 's/^canonical=//p' "$spec42b/results")" != "0" ]; then
	fail "case42b helper accepts exact [1, 2] bytes" "canonical exit $(sed -n 's/^canonical=//p' "$spec42b/results")"
elif ! caller_unchanged; then
	fail "case42b helper accepts exact [1, 2] bytes" "calling checkout moved"
elif ! poison_unchanged; then
	fail "case42b helper accepts exact [1, 2] bytes" "poison GIT_DIR repository moved"
else
	pass "case42b helper accepts exact [1, 2] bytes"
	bytes42b_ready=1
fi
if [ "$bytes42b_ready" != "1" ]; then
	fail "case42b helper refuses [true, 2]" "helper child did not complete"
	fail "case42b helper refuses [1.0, 2]" "helper child did not complete"
	fail "case42b helper refuses compact [1,2]" "helper child did not complete"
else
	if [ "$(sed -n 's/^boolean=//p' "$spec42b/results")" = "0" ]; then
		fail "case42b helper refuses [true, 2]" "boolean substitute was accepted"
	else
		pass "case42b helper refuses [true, 2]"
	fi
	if [ "$(sed -n 's/^float=//p' "$spec42b/results")" = "0" ]; then
		fail "case42b helper refuses [1.0, 2]" "float substitute was accepted"
	else
		pass "case42b helper refuses [1.0, 2]"
	fi
	if [ "$(sed -n 's/^compact=//p' "$spec42b/results")" = "0" ]; then
		fail "case42b helper refuses compact [1,2]" "compact spelling was accepted"
	else
		pass "case42b helper refuses compact [1,2]"
	fi
fi
if [ -f "$spec42b/results" ]; then
	cp "$spec42b/results" "$WORK/out/case42b-bytes.results" || exit 2
fi

# ── CFP2 correction 2: malformed helper-result admission ────────────────────────────
missing42b="$spec42b/results-missing-negative"
printf 'canonical=0\nfloat=1\ncompact=1\n' >"$missing42b" || exit 2
if cfp2_case42b_results_exact "$missing42b"; then
	fail "case42b exact-results admission refuses a missing negative row" "missing boolean row was accepted"
else
	pass "case42b exact-results admission refuses a missing negative row"
fi

status2_42b="$spec42b/results-negative-status-2"
printf 'canonical=0\nboolean=2\nfloat=1\ncompact=1\n' >"$status2_42b" || exit 2
if cfp2_case42b_results_exact "$status2_42b"; then
	fail "case42b exact-results admission refuses unexpected negative status 2" "boolean status 2 was accepted"
else
	pass "case42b exact-results admission refuses unexpected negative status 2"
fi

# ── positive control: actual new_repo from a sourced child, not this shell ───────────
pos_out="$WORK/out/positive.stdout"
pos_err="$WORK/out/positive.stderr"
pos_got="$WORK/out/positive-config"
export CFP1_CALLER="$CALLER" CFP1_CANDIDATE="$CANDIDATE" CFP1_GOT="$pos_got"
pos_rc=0
(
	cd "$CALLER" || exit 2
	env GIT_DIR="$POISON/.git" bash -c '
		set -u
		cd "$CFP1_CALLER" || exit 2
		# shellcheck source=/dev/null
		. "$CFP1_CANDIDATE" || exit 2
		d="$(new_repo cfp1-positive)" || exit 2
		[ -n "$d" ] || exit 3
		[ -d "$d/.git" ] || exit 3
		git -C "$d" cat-file blob HEAD:.gitleaks.toml >"$CFP1_GOT" || exit 3
		cmp -s "$CFP1_CALLER/.gitleaks.toml" "$CFP1_GOT" || exit 3
		msg="$(git -C "$d" log -1 --format=%s)" || exit 3
		[ "$msg" = "base" ] || exit 3
		printf "CFP1_POSITIVE_OK\n"
	'
) >"$pos_out" 2>"$pos_err" || pos_rc=$?
if [ "$pos_rc" != "0" ]; then
	fail "valid-fixture positive control" "child exit $pos_rc stderr=$(tr '\n' ' ' <"$pos_err" | head -c 240)"
elif ! grep -qx 'CFP1_POSITIVE_OK' "$pos_out"; then
	fail "valid-fixture positive control" "missing CFP1_POSITIVE_OK"
elif ! caller_unchanged; then
	fail "valid-fixture positive control" "calling checkout moved"
elif ! poison_unchanged; then
	fail "valid-fixture positive control" "poison GIT_DIR repository moved"
else
	pass "valid new_repo commits config bytes equal to the source"
fi

# ── no-fault positive: actual helpers, real gate, delivery receipt, exact cause ──────
pos2_script="$WORK/cfp2-positive.sh"
pos2_out="$WORK/out/cfp2-positive.stdout"
pos2_err="$WORK/out/cfp2-positive.stderr"
pos2_rec5="$WORK/out/cfp2-positive-5.receipt"
pos2_rec42="$WORK/out/cfp2-positive-42a.receipt"
cat >"$pos2_script" <<'POS'
set -u
cd "$CFP2_CALLER" || exit 2
# shellcheck source=/dev/null
. "$CFP2_CANDIDATE" || exit 2
d="$(new_repo cfp2-positive)" || exit 2
printf 'not json at all' >"$WORK/5.intended" || exit 2
cfp2_verify_case5_specimen "$WORK/5.intended" || exit 2
make_report_shim "$WORK/report-shim" || exit 2
out5="$WORK/cfp2-pos5.out"
rc="$(run_gate_report "$d" "$WORK/5.intended" "$out5" OLIVARES_TEST_GITLEAKS_EXIT=0)" || exit 2
[ "$rc" = "2" ] || exit 3
grep -q 'COULD NOT LOOK' "$out5" || exit 3
grep -q 'is not a JSON array' "$out5" || exit 3
cfp2_no_verdict "$out5" || exit 3
last5="$(cat "$WORK/cfp2-delivery/LAST_PATH")" || exit 3
cfp2_verify_delivery_receipt "$last5" "$WORK/5.intended" || exit 3
cp "$last5" "$CFP2_GOT_RECEIPT5" || exit 3
head="$(git -C "$d" rev-parse HEAD)" || exit 2
python3 - "$WORK/42a.json" "$head" <<'PY' || exit 2
import json, sys
json.dump([{'RuleID': 'framing-rule', 'File': 'plain.txt', 'StartLine': 1,
            'Commit': {'oid': sys.argv[2]}, 'Author': '', 'Email': '', 'Date': '',
            'Fingerprint': ''}], open(sys.argv[1], 'w', encoding='utf-8'))
PY
cfp2_verify_case42a_specimen "$WORK/42a.json" || exit 2
out42="$WORK/cfp2-pos42.out"
rc="$(run_gate_report "$d" "$WORK/42a.json" "$out42" OLIVARES_SECRETS_SCOPE=all)" || exit 2
[ "$rc" = "2" ] || exit 3
grep -q 'COULD NOT LOOK' "$out42" || exit 3
grep -q 'carries a field of an unsupported type' "$out42" || exit 3
cfp2_no_verdict "$out42" || exit 3
last42="$(cat "$WORK/cfp2-delivery/LAST_PATH")" || exit 3
cfp2_verify_delivery_receipt "$last42" "$WORK/42a.json" || exit 3
cp "$last42" "$CFP2_GOT_RECEIPT42" || exit 3
printf 'CFP2_POSITIVE_OK\n'
POS
pos2_rc=0
(
	cd "$CALLER" || exit 2
	env GIT_DIR="$POISON/.git" \
		CFP2_CALLER="$CALLER" \
		CFP2_CANDIDATE="$CANDIDATE" \
		CFP2_GOT_RECEIPT5="$pos2_rec5" \
		CFP2_GOT_RECEIPT42="$pos2_rec42" \
		bash "$pos2_script"
) >"$pos2_out" 2>"$pos2_err" || pos2_rc=$?
if [ "$pos2_rc" != "0" ]; then
	fail "no-fault report-delivery positive control" "child exit $pos2_rc stderr=$(tr '\n' ' ' <"$pos2_err" | head -c 240)"
elif ! grep -qx 'CFP2_POSITIVE_OK' "$pos2_out"; then
	fail "no-fault report-delivery positive control" "missing CFP2_POSITIVE_OK"
elif ! grep -qx 'DELIVERED=1' "$pos2_rec5" || ! grep -qx 'DELIVERED=1' "$pos2_rec42"; then
	fail "no-fault report-delivery positive control" "missing delivery receipt"
elif ! caller_unchanged; then
	fail "no-fault report-delivery positive control" "calling checkout moved"
elif ! poison_unchanged; then
	fail "no-fault report-delivery positive control" "poison GIT_DIR repository moved"
else
	pass "no-fault delivery binds intended bytes and the exact parse cause"
fi

run_injected() {
	local name="$1" op="$2" expect_op="$3" cwd="$4" exclude17="$5"
	local rec stdout stderr rc
	rec="$WORK/receipts/$name"
	stdout="$WORK/out/$name.stdout"
	stderr="$WORK/out/$name.stderr"
	: >"$rec"
	CFP1_INJECT_OP="$op"
	CFP1_INJECT_RECEIPT="$rec"
	rc="$(run_candidate "$cwd" "$stdout" "$stderr")"
	unset CFP1_INJECT_OP CFP1_INJECT_RECEIPT
	if [ "$rc" != "2" ]; then
		fail "$name" "candidate exit $rc, want setup 2"
		return
	fi
	if ! setup_diag "$stderr" "$expect_op"; then
		fail "$name" "missing setup diagnostic for $expect_op; stderr=$(tr '\n' ' ' <"$stderr" | head -c 240)"
		return
	fi
	if ! no_generic_scanner_unavailable "$stderr"; then
		fail "$name" "scanner-unavailable diagnostic is not the setup proof"
		return
	fi
	if [ "$exclude17" = "exclude17" ] && ! no_case17_pass "$stdout"; then
		fail "$name" "printed a case-17 successful verdict"
		return
	fi
	if [ "$exclude17" = "keep17" ]; then
		if ! grep -q '  ok   1 ' "$stdout"; then
			fail "$name" "injection failed before case 1; shim is not specific to attribution"
			return
		fi
		if grep -q '  ok   25 ' "$stdout"; then
			fail "$name" "printed an attribution behavior PASS after setup failure"
			return
		fi
	fi
	if grep -q 'test-check-secrets: OK —' "$stdout"; then
		fail "$name" "printed a complete-battery pass"
		return
	fi
	if [ "$op" != "" ] && ! receipt_fired "$rec" "$op"; then
		fail "$name" "named injection did not fire; receipt=$(tr '\n' ' ' <"$rec" | head -c 200)"
		return
	fi
	if ! caller_unchanged; then
		fail "$name" "calling checkout moved"
		return
	fi
	if ! poison_unchanged; then
		fail "$name" "poison GIT_DIR repository moved"
		return
	fi
	pass "$name (child exit 2, $expect_op, checkout preserved)"
}

run_cfp2_injected() {
	local name="$1" op="$2" expect_op="$3" cwd="$4" rule="$5"
	local rec stdout stderr rc
	rec="$WORK/receipts/$name"
	stdout="$WORK/out/$name.stdout"
	stderr="$WORK/out/$name.stderr"
	: >"$rec"
	CFP1_INJECT_OP="$op"
	CFP1_INJECT_RECEIPT="$rec"
	rc="$(run_candidate "$cwd" "$stdout" "$stderr")"
	unset CFP1_INJECT_OP CFP1_INJECT_RECEIPT
	if [ "$rc" != "2" ]; then
		fail "$name" "candidate exit $rc, want setup 2"
		return
	fi
	if ! setup_diag "$stderr" "$expect_op"; then
		fail "$name" "missing setup diagnostic for $expect_op; stderr=$(tr '\n' ' ' <"$stderr" | head -c 240)"
		return
	fi
	if ! no_generic_scanner_unavailable "$stderr"; then
		fail "$name" "scanner-unavailable diagnostic is not the setup proof"
		return
	fi
	if grep -q 'test-check-secrets: OK —' "$stdout"; then
		fail "$name" "printed a complete-battery pass"
		return
	fi
	if [ "$rule" = "fail-before-5" ]; then
		if ! grep -q '  ok   1 ' "$stdout"; then
			fail "$name" "injection failed before case 1; shim is not specific to gitleaks chmod"
			return
		fi
		if ! no_case5_pass "$stdout"; then
			fail "$name" "printed a case-5 successful verdict"
			return
		fi
		if ! no_case42_pass "$stdout"; then
			fail "$name" "printed a case-42 successful verdict"
			return
		fi
	fi
	if [ "$rule" = "through-5-no-42" ]; then
		if ! grep -q '  ok   5 ' "$stdout"; then
			fail "$name" "injection failed before case 5; shim is not specific to the 42a seam"
			return
		fi
		if ! no_case42_pass "$stdout"; then
			fail "$name" "printed a case-42 successful verdict"
			return
		fi
	fi
	if [ "$op" != "" ] && ! receipt_fired "$rec" "$op"; then
		fail "$name" "named injection did not fire; receipt=$(tr '\n' ' ' <"$rec" | head -c 200)"
		return
	fi
	if ! caller_unchanged; then
		fail "$name" "calling checkout moved"
		return
	fi
	if ! poison_unchanged; then
		fail "$name" "poison GIT_DIR repository moved"
		return
	fi
	pass "$name (child exit 2, $expect_op, checkout preserved)"
}

# ── candidate process: configuration copy failure during new_repo ────────────────────
run_injected "copy-config" "copy-config" "copy-config" "$CALLER" "exclude17"

# ── candidate process: base Git commit failure during new_repo ───────────────────────
run_injected "git-commit-base" "git-commit-base" "git-commit" "$CALLER" "exclude17"

# ── candidate process: nested attribution new_repo commit failure ────────────────────
run_injected "git-commit-base-attribution" "git-commit-base-attribution" "git-commit" "$CALLER" "keep17"

# ── candidate process: missing classifier source ─────────────────────────────────────
minroot="$WORK/minroot-missing"
if ! make_min_root "$minroot" ""; then
	fail "missing-classifier-source" "could not build disposable root"
else
	run_injected "missing-classifier-source" "" "missing-classifier-source" "$minroot" "exclude17"
fi

# ── candidate process: classifier copy failure ───────────────────────────────────────
run_injected "classifier-copy" "classifier-copy" "classifier-copy" "$CALLER" "exclude17"

# ── candidate process: classifier fixture commit failure ─────────────────────────────
run_injected "classifier-commit" "classifier-commit" "classifier-commit" "$CALLER" "exclude17"

# ── CFP2: malformed-report generation failure ────────────────────────────────────────
run_cfp2_injected "write-malformed-report-42a" "write-malformed-report-42a" \
	"write-malformed-report-42a" "$CALLER" "through-5-no-42"

# ── CFP2: shim executable preparation failure ────────────────────────────────────────
run_cfp2_injected "gitleaks-shim-chmod" "gitleaks-shim-chmod" \
	"gitleaks-shim-chmod" "$CALLER" "fail-before-5"

# ── CFP2: report copy failure after valid case-42 preparation ────────────────────────
run_cfp2_injected "report-copy-42a" "report-copy-42a" \
	"report-delivery-receipt" "$CALLER" "through-5-no-42"

if [ -n "${CFP1_EVIDENCE_DIR:-}" ]; then
	mkdir -p "$CFP1_EVIDENCE_DIR/out" "$CFP1_EVIDENCE_DIR/receipts" || {
		echo "test-check-secrets-setup: cannot write CFP1_EVIDENCE_DIR" >&2
		exit 2
	}
	cp -a "$WORK/out/." "$CFP1_EVIDENCE_DIR/out/" || exit 2
	cp -a "$WORK/receipts/." "$CFP1_EVIDENCE_DIR/receipts/" || exit 2
fi

echo ""
if [ "$fails" -eq 0 ]; then
	echo "test-check-secrets-setup: OK — $passes/$passes"
	exit 0
fi
echo "test-check-secrets-setup: $fails case(s) failed ($passes passed)" >&2
exit 1
