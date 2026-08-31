#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for the MODE half of scripts/cosign-verified.sh (release-rehearsal design
# §A.2.7): key mode must inject `--key <per-run>` and `--tlog-upload=false` into every
# signing-capable subcommand — APPENDED LAST, because pflag booleans are
# last-occurrence-wins — and must refuse any partial configuration instead of signing
# with whatever half-state it finds. Non-signing subcommands must pass through untouched
# in both modes, and keyless mode must remain byte-for-byte the historical behaviour.
#
# Hermetic: the wrapper copy runs against a stub assert script and a stub cosign that
# records its argv — no network, no real cosign, nothing signed.
#
# NO `set -e` (battery reports through check(); see test-pg-test-env.sh).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-cvmode.XXXXXX")" || exit 1
# ${TMPDIR:-/tmp} may be mounted noexec (the dev container's /tmp is), and this battery
# runs PATH-stubbed binaries: a stub there dies at execve with EACCES, so the rows that
# exercise the stub fail while the rows that do not, pass. Measured 2026-08-01: this file
# reported "9 passed, 5 failed" on /tmp and "14 passed, 0 failed" with an exec TMPDIR —
# and it had never passed on this host, so `lint:release-mechanics` blocked every push
# the moment it reached main. Same probe test-cosign-guard.sh already carried; this
# battery landed without it.
printf '#!/bin/sh\nexit 0\n' >"$WORK/.execprobe" && chmod +x "$WORK/.execprobe"
if ! "$WORK/.execprobe" >/dev/null 2>&1; then
	rm -rf "$WORK"
	WORK="$(mktemp -d "$ROOT/.tmpexec.XXXXXX")" || exit 1
fi
rm -f "$WORK/.execprobe"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-62s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-62s %s\n' "$1" "$2"
	fi
}

# Fixture ROOT: the real wrapper next to a stub invariant (so the mode logic is isolated
# from the digest table) and a stub cosign that records exactly what would have run.
mkdir -p "$WORK/scripts" "$WORK/bin" || exit 1
cp "$ROOT/scripts/cosign-verified.sh" "$WORK/scripts/" || exit 1
cat >"$WORK/scripts/assert-cosign-binary.sh" <<'EOF'
#!/usr/bin/env bash
exit "${STUB_ASSERT_RC:-0}"
EOF
cat >"$WORK/bin/cosign" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >"${OLIVARES_TEST_ARGV}"
EOF
chmod +x "$WORK/scripts/assert-cosign-binary.sh" "$WORK/bin/cosign" || exit 1
KEY="$WORK/rehearsal.key"
echo "stub-private-key" >"$KEY" || exit 1

n=0
run_wrap() { # run_wrap extra-env… -- wrapper-args…
	n=$((n + 1))
	argv="$WORK/argv.$n"
	err="$WORK/err.$n"
	: >"$argv"
	envs=()
	while [ $# -gt 0 ] && [ "$1" != "--" ]; do
		envs+=("$1")
		shift
	done
	shift
	env OLIVARES_COSIGN_BIN="$WORK/bin/cosign" OLIVARES_TEST_ARGV="$argv" "${envs[@]}" \
		bash "$WORK/scripts/cosign-verified.sh" "$@" >"$WORK/stdout.$n" 2>"$err"
	rc=$?
}

echo "cosign-verified mode logic — keyless passthrough, key-mode injection, refusals"

# --- keyless: the historical behaviour, untouched ---------------------------------------
run_wrap -- sign-blob --output-signature=x.sig f --yes
[ "$rc" -eq 0 ] && printf 'sign-blob\n--output-signature=x.sig\nf\n--yes\n' | cmp -s - "$WORK/argv.$n"
check "default (no COSIGN_MODE) passes argv through UNCHANGED" "keyless" $?

run_wrap COSIGN_MODE=keyless -- sign-blob f --yes
[ "$rc" -eq 0 ] && ! grep -q -- '--key' "$WORK/argv.$n"
check "explicit keyless injects nothing" "no --key" $?

run_wrap COSIGN_MODE=sideways -- sign-blob f --yes
[ "$rc" -ne 0 ] && [ ! -s "$WORK/argv.$n" ]
check "an unknown mode refuses and cosign never runs" "closed enum" $?

# --- key mode: refusals before anything executes ----------------------------------------
run_wrap COSIGN_MODE=key COSIGN_TLOG_UPLOAD=false -- sign-blob f --yes
[ "$rc" -ne 0 ] && [ ! -s "$WORK/argv.$n" ]
check "key mode without COSIGN_KEY refuses" "no half-config" $?

run_wrap COSIGN_MODE=key COSIGN_KEY=relative.key COSIGN_TLOG_UPLOAD=false -- sign-blob f --yes
[ "$rc" -ne 0 ] && [ ! -s "$WORK/argv.$n" ]
check "a relative COSIGN_KEY refuses" "absolute only" $?

run_wrap COSIGN_MODE=key COSIGN_KEY="$WORK/absent.key" COSIGN_TLOG_UPLOAD=false -- sign-blob f --yes
[ "$rc" -ne 0 ] && [ ! -s "$WORK/argv.$n" ]
check "a missing key file refuses" "no phantom key" $?

run_wrap COSIGN_MODE=key COSIGN_KEY="$KEY" -- sign-blob f --yes
[ "$rc" -ne 0 ] && [ ! -s "$WORK/argv.$n" ]
check "key mode without COSIGN_TLOG_UPLOAD refuses" "tlog intent explicit" $?

run_wrap COSIGN_MODE=key COSIGN_KEY="$KEY" COSIGN_TLOG_UPLOAD=true -- sign-blob f --yes
[ "$rc" -ne 0 ] && [ ! -s "$WORK/argv.$n" ]
check "key mode with tlog upload 'true' refuses" "never a public record" $?

# --- key mode: the injection, appended LAST ---------------------------------------------
run_wrap COSIGN_MODE=key COSIGN_KEY="$KEY" COSIGN_TLOG_UPLOAD=false -- \
	sign-blob --output-signature=x.sig --output-certificate= f --yes
[ "$rc" -eq 0 ] &&
	printf 'sign-blob\n--output-signature=x.sig\n--output-certificate=\nf\n--yes\n--key\n%s\n--tlog-upload=false\n' "$KEY" |
	cmp -s - "$WORK/argv.$n"
check "sign-blob gets --key + --tlog-upload=false APPENDED LAST" "last wins" $?

for sub in sign attest attest-blob; do
	run_wrap COSIGN_MODE=key COSIGN_KEY="$KEY" COSIGN_TLOG_UPLOAD=false -- "$sub" --yes ref
	[ "$rc" -eq 0 ] &&
		[ "$(tail -3 "$WORK/argv.$n" | head -1)" = "--key" ] &&
		[ "$(tail -2 "$WORK/argv.$n" | head -1)" = "$KEY" ] &&
		[ "$(tail -1 "$WORK/argv.$n")" = "--tlog-upload=false" ]
	check "$sub gets the same injection" "signing-capable set" $?
done

# A caller-supplied earlier --tlog-upload=true LOSES to the appended =false (pflag
# last-occurrence-wins — the reason the injection appends instead of prepending).
run_wrap COSIGN_MODE=key COSIGN_KEY="$KEY" COSIGN_TLOG_UPLOAD=false -- sign-blob --tlog-upload=true f --yes
[ "$rc" -eq 0 ] && [ "$(tail -1 "$WORK/argv.$n")" = "--tlog-upload=false" ]
check "an earlier --tlog-upload=true is overridden by the appended =false" "append order" $?

# --- key mode: non-signing subcommands pass through untouched ---------------------------
run_wrap COSIGN_MODE=key COSIGN_KEY="$KEY" COSIGN_TLOG_UPLOAD=false -- verify-blob --key pub.key f
[ "$rc" -eq 0 ] && ! grep -qx -- '--tlog-upload=false' "$WORK/argv.$n"
check "verify-blob is NOT injected (verification is caller-explicit)" "passthrough" $?

run_wrap COSIGN_MODE=key -- copy --force a b
[ "$rc" -eq 0 ] && printf 'copy\n--force\na\nb\n' | cmp -s - "$WORK/argv.$n"
check "copy is NOT injected and needs no key config" "passthrough" $?

run_wrap COSIGN_MODE=key -- generate-key-pair --output-key-prefix p
[ "$rc" -eq 0 ] && ! grep -q -- '--key' "$WORK/argv.$n"
check "generate-key-pair is NOT injected (the key does not exist yet)" "passthrough" $?

# --- the invariant still gates the exec in both modes -----------------------------------
run_wrap STUB_ASSERT_RC=1 COSIGN_MODE=key COSIGN_KEY="$KEY" COSIGN_TLOG_UPLOAD=false -- sign-blob f --yes
[ "$rc" -ne 0 ] && [ ! -s "$WORK/argv.$n" ]
check "a failed byte re-authentication still refuses to exec" "invariant first" $?

echo ""
echo "cosign-verified mode battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
