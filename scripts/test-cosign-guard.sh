#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Proves the properties of scripts/cosign-guard.sh without touching the network and
# without a real cosign: a stub named `cosign` stands in for the binary and reports the
# environment and arguments it was handed.
#
# The property under test is the one whose absence created two permanent public Rekor
# records on 2026-07-25: a developer who types an ordinary `cosign sign-blob`, with no
# containment flags and no memory of any containment script, MUST NOT be able to reach the
# public transparency log.
#
# The guard was itself reviewed adversarially and this file grew accordingly: the first
# version could loop when two shims were on PATH, could truncate an unrelated `cosign`
# binary, printed the whole argument vector in its escape banner, and allowed contained
# signing while describing a proxy as containment. Each of those is a case below.
# NO `set -e` HERE, DELIBERATELY. This file REPORTS failures through check(); `set -e`
# turns a failing assertion into a silent STOP instead, so the run ends after the last
# success and looks like a clean tail. That is exactly the failure mode these batteries
# exist to catch, and it bit this repository twice on 2026-07-25 — once truncating a
# 23-case battery at case 11, once truncating this one at case 2. Critical SETUP commands
# below carry an explicit `|| exit`; assertions must not abort the run.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GUARD="$ROOT/scripts/cosign-guard.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-cosign-guard.XXXXXX")" || exit 1
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

# ${TMPDIR:-/tmp} may be mounted noexec (the dev container's /tmp is): stubs then
# die at execve with EACCES — see test-assert-cosign-binary.sh for the measured
# signature. Probe; fall back to a repo-local (exec) tempdir.
printf '#!/bin/sh\nexit 0\n' >"$WORK/.execprobe" && chmod +x "$WORK/.execprobe"
if ! "$WORK/.execprobe" >/dev/null 2>&1; then
	rm -rf "$WORK"
	WORK="$(mktemp -d "$ROOT/.tmpexec.XXXXXX")" || exit 1
fi
rm -f "$WORK/.execprobe"

pass=0
fail=0
check() { # check <name> <detail> <result-code>
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-58s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		printf '  FAIL  %-58s %s\n' "$1" "$2"
	fi
}

# A stub cosign that reports what it received.
STUB="$WORK/real"
mkdir -p "$STUB"
cat >"$STUB/cosign" <<'SH'
#!/usr/bin/env bash
echo "STUB_ARGS=$*"
echo "STUB_HTTPS_PROXY=${HTTPS_PROXY:-unset}"
echo "STUB_HTTP_PROXY=${HTTP_PROXY:-unset}"
echo "STUB_ALL_PROXY=${ALL_PROXY:-unset}"
echo "STUB_NO_PROXY=${NO_PROXY:-unset}"
echo "STUB_GUARD=${OLIVARES_COSIGN_GUARD:-unset}"
SH
chmod 0755 "$STUB/cosign"

SHIMDIR="$WORK/shim"
# Critical setup carries `|| exit`, as this file's own header requires. Without it a
# failed install left $SHIMDIR/cosign absent and EVERY row below measured something else
# — most of them invoke "$SHIMDIR/cosign" directly, so they would fail with "No such
# file or directory" and count as the guard correctly denying. A battery cannot prove a
# containment property using a shim that was never installed.
PATH="$STUB:$PATH" bash "$GUARD" --install "$SHIMDIR" >/dev/null || {
	echo "test-cosign-guard: could not install the shim into $SHIMDIR; nothing below is meaningful." >&2
	exit 2
}
[ -x "$SHIMDIR/cosign" ] || {
	echo "test-cosign-guard: the installer returned success but wrote no executable $SHIMDIR/cosign." >&2
	exit 2
}

echo "cosign-guard — properties"

# --- the incident: publication-capable subcommands are DENIED, not contained ------------
set +e
out_deny="$(PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" sign-blob --key k --yes file 2>"$WORK/deny.err")"
rc_deny=$?
[ "$rc_deny" -ne 0 ]
check "sign-blob is DENIED outright" "nonzero exit" $?

[ -z "$out_deny" ]
check "the real cosign is never reached" "no stub output" $?

grep -q "DENIED on this machine" "$WORK/deny.err"
check "the denial says so explicitly" "diagnostic present" $?

grep -q "OLIVARES_COSIGN_ALLOW_PUBLIC_LOG=1" "$WORK/deny.err"
check "the denial names the only way past" "escape documented" $?

# ONE verdict for the set, and it must reflect what happened: the previous shape emitted an
# unconditional PASS after the loop, so an allowed verb produced a FAIL *and* a PASS and the
# summary line stayed reassuring.
any_allowed=0
for sub in sign attest attest-blob copy upload attach; do
	PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" "$sub" x >/dev/null 2>&1
	if [ $? -eq 0 ]; then
		any_allowed=1
		printf '        ALLOWED: %s\n' "$sub"
	fi
done
check "every publication-capable subcommand is denied" "sign/attest/attest-blob/copy/upload/attach" "$any_allowed"

# --- the escape works, is loud, and does not leak the argument vector -------------------
out_e="$(PATH="$SHIMDIR:$STUB:$PATH" OLIVARES_COSIGN_ALLOW_PUBLIC_LOG=1 \
	bash "$SHIMDIR/cosign" sign-blob --key hashivault://super-secret-token file 2>"$WORK/esc.err")"
grep -q "STUB_ARGS=sign-blob --key hashivault://super-secret-token file" <<<"$out_e"
check "opt-out reaches cosign with arguments intact" "argv preserved" $?

grep -q "STUB_HTTPS_PROXY=unset" <<<"$out_e"
check "opt-out removes the proxy as well" "no proxy forced" $?

grep -q "CONTAINMENT DISABLED" "$WORK/esc.err"
check "opt-out prints a loud banner" "banner present" $?

grep -q "PERMANENT" "$WORK/esc.err"
check "the banner names the consequence" "'PERMANENT' present" $?

! grep -q "super-secret-token" "$WORK/esc.err"
check "the banner does NOT echo the argument vector" "no secret leak" $?

# --- everything else runs contained (defence in depth) ----------------------------------
out_v="$(PATH="$SHIMDIR:$STUB:$PATH" NO_PROXY=rekor.sigstore.dev \
	bash "$SHIMDIR/cosign" verify-blob --key k file 2>/dev/null)"
grep -q "STUB_HTTPS_PROXY=http://127.0.0.1:1" <<<"$out_v"
check "verify-blob runs behind a refused proxy" "HTTPS_PROXY set" $?

grep -q "STUB_HTTP_PROXY=http://127.0.0.1:1" <<<"$out_v"
check "HTTP_PROXY is set too" "HTTP_PROXY set" $?

grep -q "STUB_ALL_PROXY=http://127.0.0.1:1" <<<"$out_v"
check "ALL_PROXY is set" "ALL_PROXY set" $?

grep -q "STUB_NO_PROXY=unset" <<<"$out_v"
check "an inherited NO_PROXY is cleared, not honoured" "NO_PROXY unset" $?

grep -q "STUB_GUARD=3" <<<"$out_v"
check "the guard marks the invocation" "OLIVARES_COSIGN_GUARD=3" $?

# --- installation safety ----------------------------------------------------------------
OTHER="$WORK/other"
mkdir -p "$OTHER"
printf '#!/bin/sh\necho i am a real cosign\n' >"$OTHER/cosign"
chmod 0755 "$OTHER/cosign"
before="$(cat "$OTHER/cosign")"
set +e
PATH="$STUB:$PATH" bash "$GUARD" --install "$OTHER" >/dev/null 2>&1
rc_over=$?
[ "$rc_over" -ne 0 ]
check "refuses to overwrite a non-shim cosign" "nonzero exit" $?
[ "$(cat "$OTHER/cosign")" = "$before" ]
check "the unrelated binary is left untouched" "bytes unchanged" $?

# Re-installing over our OWN shim must be allowed (idempotent bootstrap).
set +e
PATH="$STUB:$PATH" bash "$GUARD" --install "$SHIMDIR" >/dev/null 2>&1
rc_re=$?
[ "$rc_re" -eq 0 ]
check "re-installing over its own shim is allowed" "idempotent" $?

# --- two shims on PATH must not loop -----------------------------------------------------
SECOND="$WORK/shim2"
PATH="$STUB:$PATH" bash "$GUARD" --install "$SECOND" >/dev/null
set +e
out_two="$(timeout 20 env PATH="$SHIMDIR:$SECOND:$STUB:$PATH" bash "$SHIMDIR/cosign" version 2>/dev/null)"
rc_two=$?
[ "$rc_two" -ne 124 ]
check "two shims on PATH do not loop" "no timeout" $?
grep -q "STUB_ARGS=version" <<<"$out_two"
check "the real cosign is still reached with two shims" "resolved once" $?

# --- --status reports honestly ------------------------------------------------------------
set +e
PATH="$STUB:$PATH" bash "$GUARD" --status >/dev/null 2>&1
rc_inactive=$?
PATH="$SHIMDIR:$STUB:$PATH" bash "$GUARD" --status >/dev/null 2>&1
rc_installed=$?
[ "$rc_inactive" -ne 0 ]
check "--status fails when PATH does not resolve to the shim" "nonzero" $?
[ "$rc_installed" -eq 0 ]
check "--status succeeds when it does" "zero" $?

# --- direct invocation without the shim must not silently act as cosign -------------------
set +e
PATH="$STUB:$PATH" bash "$GUARD" version >/dev/null 2>"$WORK/direct.err"
rc_direct=$?
[ "$rc_direct" -ne 0 ]
check "invoking the guard directly is refused" "nonzero" $?
grep -q "did not supply a usable real cosign path" "$WORK/direct.err"
check "and says why" "diagnostic present" $?

# --- DEFAULT DENY: leading flags and unknown verbs must not fail open -------------------
set +e
PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" --verbose sign-blob --key k file >/dev/null 2>&1
rc_flag=$?
PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" --output-file /tmp/x sign --key k img >/dev/null 2>&1
rc_flagval=$?
PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" publish-everything >/dev/null 2>&1
rc_unknown=$?
out_ro="$(PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" verify-blob-attestation --key k f 2>/dev/null)"
rc_ro=$?
[ "$rc_flag" -ne 0 ]
check "a leading boolean flag does not hide sign-blob" "denied" $?
[ "$rc_flagval" -ne 0 ]
check "a leading flag WITH a value does not hide sign" "denied" $?
[ "$rc_unknown" -ne 0 ]
check "an unknown verb is denied, not allowed" "default-deny" $?
[ "$rc_ro" -eq 0 ] && grep -q "STUB_ARGS=verify-blob-attestation" <<<"$out_ro"
check "a read-only verb still runs" "allowlisted" $?

# --- PROVABLY CONTAINED SIGNING IS ALLOWED, AND STILL CONTAINED --------------------------
# scripts/check-cosign-contract.sh must sign with a throwaway key and the log disabled, or
# it cannot prove the release contract at all. A guard that forces that fixture off is a
# guard nobody leaves on, so the invariant is not "never sign" but "never sign in a way
# that can reach the log".
out_c="$(PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" sign-blob --tlog-upload=false --key k --yes f 2>"$WORK/contained.err")"
grep -q "STUB_ARGS=sign-blob --tlog-upload=false --key k --yes f" <<<"$out_c"
check "sign-blob WITH --tlog-upload=false is allowed" "reaches cosign" $?

grep -q "STUB_HTTPS_PROXY=http://127.0.0.1:1" <<<"$out_c"
check "and it still runs behind the refused proxy" "contained anyway" $?

grep -q "permitted" "$WORK/contained.err"
check "the allowance is announced, not silent" "notice present" $?

# (The split spelling `--tlog-upload false` is covered further down, and it is DENIED:
# pflag gives it the opposite meaning. This case used to assert the reverse.)

# `--use-signing-config=false` was MEASURED on 2026-07-25 to upload to the real public
# Rekor. It must never be mistaken for a containment flag.
set +e
PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" sign-blob --key k --use-signing-config=false f >/dev/null 2>"$WORK/ucfg.err"
rc_ucfg=$?
[ "$rc_ucfg" -ne 0 ]
check "--use-signing-config=false does NOT count as containment" "denied" $?
grep -q "does NOT count" "$WORK/ucfg.err"
check "and the denial says why" "explained" $?

# --- THE TEXT IS NOT THE FLAG (round 5) --------------------------------------------------
# A first version scanned argv for the string `--tlog-upload=false` and accepted two shapes
# where it is not a flag at all. Both are harmless invocations that would have been read as
# "the transparency log is disabled" and let a publisher through.
set +e
PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" sign-blob --output-file --tlog-upload=false f >/dev/null 2>&1
rc_asvalue=$?
PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" sign-blob -- --tlog-upload=false >/dev/null 2>&1
rc_afterdd=$?
[ "$rc_asvalue" -ne 0 ]
check "the flag TEXT as another option's value is not containment" "denied" $?
[ "$rc_afterdd" -ne 0 ]
check "the flag TEXT after -- is an operand, not containment" "denied" $?

# The SPLIT spelling must be DENIED, because pflag gives it the opposite meaning.
# `--tlog-upload` is a boolean with NoOptDefVal="true", so it consumes no operand and
# `false` becomes the file to sign. Measured on the pinned v2.6.4:
#     Error: signing false: upload to tlog: user declined the prompt
# An earlier revision of this guard read that spelling as containment and permitted it.
set +e
PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" sign-blob --key k --tlog-upload false f >/dev/null 2>&1
rc_split=$?
set +e
[ "$rc_split" -ne 0 ]
check "the SPLIT spelling is denied (pflag reads it as tlog ON)" "denied" $?

set +e
PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" sign-blob --key k --tlog-upload f >/dev/null 2>&1
rc_bare=$?
set +e
[ "$rc_bare" -ne 0 ]
check "a bare --tlog-upload is denied (implicit true)" "denied" $?

set +e
PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" sign-blob --key k --tlog-upload=true f >/dev/null 2>&1
rc_true=$?
set +e
[ "$rc_true" -ne 0 ]
check "an explicit --tlog-upload=true is denied" "denied" $?

# --- ROUND 7 (H1): pflag applies the LAST occurrence, and options swallow the next token --
# Two shapes reached the real binary with tlog ENABLED under the previous scan:
#   sign-blob --tlog-upload=false --tlog-upload=true f   (last wins -> true)
#   sign-blob --oidc-client-id --tlog-upload=false f     (swallowed as the option's VALUE)
# The rule no longer models pflag arity: containment must be UNAMBIGUOUS BY POSITION — the
# first argument after the subcommand, inline form, mentioned exactly once.
guard_verdict() { # guard_verdict <args...> -> PERMITTED | DENIED
	local out
	out="$(PATH="$SHIMDIR:$STUB:$PATH" bash "$SHIMDIR/cosign" "$@" 2>&1 >/dev/null)"
	if grep -q "permitted" <<<"$out"; then
		printf 'PERMITTED'
	else
		printf 'DENIED'
	fi
}

[ "$(guard_verdict sign-blob --tlog-upload=false f)" = "PERMITTED" ]
check "the canonical first-position form is permitted" "unambiguous" $?

[ "$(guard_verdict sign-blob --tlog-upload=false --tlog-upload=true f)" = "DENIED" ]
check "false-then-true is denied (pflag applies the LAST)" "ambiguity refused" $?

[ "$(guard_verdict sign-blob --tlog-upload=false --tlog-upload f)" = "DENIED" ]
check "false-then-bare is denied" "ambiguity refused" $?

[ "$(guard_verdict sign-blob --oidc-client-id --tlog-upload=false f)" = "DENIED" ]
check "a value-bearing option cannot swallow the safety flag" "position required" $?

[ "$(guard_verdict sign-blob --key k --tlog-upload=false f)" = "DENIED" ]
check "the flag must be FIRST after the subcommand" "no arity model needed" $?

# --- the read-only network exception is narrow, and cannot become a way to sign ----------
# scripts/update-cosign-digests.sh must VERIFY an upstream signature, which genuinely needs
# the network. Verification cannot write to the transparency log, so the exception is safe
# — but only if it is refused for anything that can publish.
out_vn="$(PATH="$SHIMDIR:$STUB:$PATH" OLIVARES_COSIGN_ALLOW_VERIFY_NETWORK=1 \
	bash "$SHIMDIR/cosign" verify-blob --key k f 2>"$WORK/vn.err")"
grep -q "STUB_HTTPS_PROXY=unset" <<<"$out_vn"
check "read-only verbs may reach the network when asked" "proxy lifted" $?
grep -q "cannot write to the transparency log" "$WORK/vn.err"
check "and the reason is stated" "notice present" $?

set +e
PATH="$SHIMDIR:$STUB:$PATH" OLIVARES_COSIGN_ALLOW_VERIFY_NETWORK=1 \
	bash "$SHIMDIR/cosign" sign-blob --tlog-upload=false --key k f >/dev/null 2>"$WORK/vnsign.err"
rc_vnsign=$?
[ "$rc_vnsign" -ne 0 ]
check "it does NOT lift the network for a publishing verb" "refused" $?
grep -q "read-only verbs only" "$WORK/vnsign.err"
check "even one that disables the tlog" "narrow by construction" $?

# --- --status must report PATH, not an inherited variable --------------------------------
# An earlier revision returned success as soon as OLIVARES_COSIGN_GUARD was set, so a
# process that inherited it after PATH was reset called itself ACTIVE while unguarded.
set +e
OLIVARES_COSIGN_GUARD=3 PATH="$STUB:$PATH" bash "$GUARD" --status >/dev/null 2>&1
rc_inherit=$?
[ "$rc_inherit" -ne 0 ]
check "an inherited GUARD var does not fake activation" "still reports NOT ACTIVE" $?

out_st="$(PATH="$SHIMDIR:$STUB:$PATH" bash "$GUARD" --status 2>&1)"
grep -q "NOT covered by a PATH shim" <<<"$out_st"
check "--status states what a PATH shim cannot cover" "limit declared" $?

# --- the guard can be installed BEFORE cosign exists --------------------------------------
# Otherwise the window between installing cosign and re-running setup is unguarded.
# The inherited PATH is used deliberately, WITHOUT the stub directory: this container has
# no cosign of its own, so it is the honest "not installed yet" environment. Blanking PATH
# instead would remove mkdir/grep/chmod and break the installer rather than test it.
PRE="$WORK/shim-pre"
bash "$GUARD" --install "$PRE" >"$WORK/pre.out" 2>&1
grep -q "no cosign on PATH yet" "$WORK/pre.out"
check "installs with no cosign present" "shim written anyway" $?

out_lazy="$(PATH="$PRE:$STUB:$PATH" bash "$PRE/cosign" version 2>/dev/null)"
grep -q "STUB_ARGS=version" <<<"$out_lazy"
check "and resolves cosign lazily once it appears" "late binding works" $?

set +e
PATH="$PRE:$STUB:$PATH" bash "$PRE/cosign" sign-blob --key k f >/dev/null 2>&1
rc_lazy_deny=$?
[ "$rc_lazy_deny" -ne 0 ]
check "the lazily-resolved shim still denies" "policy applies" $?

echo
echo "cosign-guard: ${pass} passed, ${fail} failed"
[ "$fail" -eq 0 ] || exit 1
