#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for the SLSA half of scripts/verify-release.sh (R1 contrast P2-04). The
# prov_checked fix was correct and UNPROTECTED: no gate consumed verify-release.sh at
# all (check-verifier-truth.sh walks Go files only), so restoring the old summary
# condition — "+ SLSA provenance" printed with ZERO archives verified — left every
# battery and lint green. A verifier overclaim with no regression is precisely the
# defect class the fix addressed, so it gets its own hermetic fixture:
#
#   - provenance present + verifier available + 0 archives matched -> the final summary
#     must NOT claim SLSA, and step 5 must say what actually happened;
#   - 1 archive verified -> the summary claims exactly that, with the count;
#   - 2 provenance files -> refuse (ambiguity, never first-match);
#   - --provenance names one explicitly -> accepted.
#
# It also covers --strict-publication (QA07), which is the same honesty property read from
# the publisher's side: the consumer mode is allowed to skip what is absent and say so, and
# a publisher is not. Every case below asserts BOTH directions — that strict mode refuses
# the shape a publisher must never ship, and that it still accepts a complete one, because a
# mode that reddens on everything passes every "it refuses" test.
#
# WIRING ONLY, AND LABELLED AS SUCH. The `cosign` and `slsa-verifier` on PATH here are
# adapters that return a configured exit code. They prove which checks this script RUNS,
# which one it reports, and how it behaves when a verifier is absent or rejects — they prove
# nothing cryptographic, and a passing row here is never evidence that a signature is valid.
# Real Ed25519 verification by the product binary lives in
# scripts/test-release-finalize-stable.sh and in cmd/olivares' own Go tests. sha256sum is
# real.
#
# NO `set -e` (battery reports through check(); see test-pg-test-env.sh).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/verify-release.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-vrs.XXXXXX")" || exit 1
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

mkdir -p "$WORK/bin" "$WORK/rel" || exit 1
# RECORDING ADAPTERS. They still only return a configured exit code — nothing here verifies a
# signature — but they now append their own argv to a log when one is named, because the
# identity these calls are made UNDER is the thing root return 02 F4 is about, and an adapter
# that ignores its arguments cannot see a wrong anchor.
cat >"$WORK/bin/cosign" <<'EOF'
#!/usr/bin/env bash
if [ -n "${COSIGN_ARGS_LOG:-}" ]; then
	{ printf 'COSIGN'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; } >>"$COSIGN_ARGS_LOG"
fi
case "${1:-}" in
version) printf 'GitVersion:    %s\n' "v2.6.4"; exit 0 ;;
esac
exit 0
EOF
# Wiring adapter: SLSA_STUB_RC selects the outcome so the reject path is exercisable.
cat >"$WORK/bin/slsa-verifier" <<'EOF'
#!/usr/bin/env bash
if [ -n "${SLSA_ARGS_LOG:-}" ]; then
	{ printf 'SLSA'; for a in "$@"; do printf ' %s' "$a"; done; printf '\n'; } >>"$SLSA_ARGS_LOG"
fi
exit "${SLSA_STUB_RC:-0}"
EOF
chmod +x "$WORK/bin/cosign" "$WORK/bin/slsa-verifier" || exit 1

cd "$WORK/rel" || exit 1
echo "not an archive" >notes.bin
echo "archive bytes" >olivares_1.2.3_linux_amd64.tar.gz
sha256sum notes.bin olivares_1.2.3_linux_amd64.tar.gz >checksums.txt || exit 1
echo "stub-sig" >checksums.txt.sig
echo "stub-key" >key.pub
echo "{}" >multiple.intoto.jsonl

n=0
run_verify() { # run_verify checksums-file extra-args…
	n=$((n + 1))
	cp "$1" checksums.txt || return 1
	shift
	env PATH="$WORK/bin:$PATH" bash "$SCRIPT" --key key.pub --offline "$@" \
		>"$WORK/out.$n" 2>"$WORK/err.$n"
	rc=$?
}

# Two checksum manifests: one with no archive entries, one with the archive.
sha256sum notes.bin >"$WORK/no-archive.txt" || exit 1
sha256sum notes.bin olivares_1.2.3_linux_amd64.tar.gz >"$WORK/with-archive.txt" || exit 1

echo "verify-release summary honesty — the claim must equal what step 5 counted"

# --- 0 archives matched: the P2-04 case -------------------------------------------------
run_verify "$WORK/no-archive.txt"
[ "$rc" -eq 0 ] &&
	grep 'provenance present but no archives matched' "$WORK/out.$n" >/dev/null &&
	! grep 'SLSA provenance' <(tail -1 "$WORK/out.$n") >/dev/null
check "0 archives verified -> the summary does NOT claim SLSA" "no overclaim" $?

# --- 1 archive verified: the claim carries the count ------------------------------------
run_verify "$WORK/with-archive.txt"
[ "$rc" -eq 0 ] && tail -1 "$WORK/out.$n" | grep 'SLSA provenance (1 archive(s))' >/dev/null
check "1 archive verified -> the summary claims SLSA with the count" "counted claim" $?

# --- ambiguity refuses; explicit selection works ----------------------------------------
echo "{}" >second.intoto.jsonl
run_verify "$WORK/with-archive.txt"
[ "$rc" -ne 0 ] && grep 'ambiguous provenance' "$WORK/err.$n" >/dev/null
check "two provenance files refuse (never first-match)" "ambiguity" $?

run_verify "$WORK/with-archive.txt" --provenance multiple.intoto.jsonl
[ "$rc" -eq 0 ] && tail -1 "$WORK/out.$n" | grep 'SLSA provenance (1 archive(s))' >/dev/null
check "--provenance names the file and verification proceeds" "explicit selection" $?
rm -f second.intoto.jsonl

# --- verifier absent: skip note, and STILL no summary claim -----------------------------
# A restricted PATH, not just removing the stub: the HOST may carry a real
# slsa-verifier (this machine does, at ~/go/bin), and the case must be hermetic.
mkdir -p "$WORK/binmin" || exit 1
cp "$WORK/bin/cosign" "$WORK/binmin/cosign" || exit 1
ln -s "$(command -v sha256sum)" "$WORK/binmin/sha256sum" || exit 1
# bash itself must be findable under the restricted PATH (env and the stub's
# `#!/usr/bin/env bash` shebang both resolve it there).
ln -s "$(command -v bash)" "$WORK/binmin/bash" || exit 1
n=$((n + 1))
cp "$WORK/with-archive.txt" checksums.txt || exit 1
env PATH="$WORK/binmin" bash "$SCRIPT" --key key.pub --offline \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -eq 0 ] &&
	grep 'slsa-verifier not installed' "$WORK/out.$n" >/dev/null &&
	! grep 'SLSA provenance' <(tail -1 "$WORK/out.$n") >/dev/null
check "verifier absent -> honest skip note, no summary claim" "no tool, no claim" $?

# --- STRICT PUBLICATION MODE (QA07) -------------------------------------------------------
# The fixture above is deliberately a MINIMAL release — one archive, one loose file, a stub
# signature — which is exactly the shape strict mode exists to refuse. Building it up one
# missing member at a time is what shows the mode is measuring each requirement separately
# rather than reddening once and staying red for its own reasons.
# THE IDENTITY IS AN ARGUMENT NOW, AND EVERY STRICT ROW CARRIES IT (root return 02, F4).
# Strict mode no longer builds an anchor from a production literal or reads one from a
# repository variable: the caller has already derived the repository, workflow path and exact
# tag, so it passes them. These two stand in for what the finalizer derives.
STRICT_ID='^https://github\.com/acme/product/\.github/workflows/release\.yml@refs/tags/v1\.2\.3$'
STRICT_URI='github.com/acme/product'
run_strict() { # run_strict <extra args…>   (keyless-shaped: strict refuses --key)
	n=$((n + 1))
	env PATH="$WORK/bin:$PATH" bash "$SCRIPT" --strict-publication \
		--cert-identity "$STRICT_ID" --source-uri "$STRICT_URI" "$@" \
		>"$WORK/out.$n" 2>"$WORK/err.$n"
	rc=$?
}

cp "$WORK/with-archive.txt" checksums.txt || exit 1

# (1) THE IDENTITY-BOUND PATH IS MANDATORY. `--key` proves possession of a key; it does not
# prove that THIS repository's release workflow signed on THIS tag, which is the only claim
# that makes publishing safe.
run_strict --key key.pub --offline
[ "$rc" -ne 0 ] && grep -- '--key is not accepted here' "$WORK/err.$n" >/dev/null
check "strict refuses --key" "possession is not identity" $?

run_strict --offline
[ "$rc" -ne 0 ] && grep -- '--offline is not accepted here' "$WORK/err.$n" >/dev/null
check "strict refuses --offline" "the tlog check is part of the identity" $?

# (2) THE KEYLESS CERTIFICATE IS A REQUIRED MEMBER.
run_strict
[ "$rc" -ne 0 ] && grep 'checksums.txt.pem is missing' "$WORK/err.$n" >/dev/null
check "strict refuses a release with no keyless certificate" "required member" $?
echo "stub-cert" >checksums.txt.pem

# (3) EVERY SIGNED MEMBER MUST BE PRESENT — the difference between the consumer's question
# and the publisher's. A release missing a platform passes ordinary mode without a word.
{ cat "$WORK/with-archive.txt"; printf '%064d  olivares_1.2.3_darwin_arm64.tar.gz\n' 0; } >"$WORK/missing-platform.txt"
cp "$WORK/missing-platform.txt" checksums.txt || exit 1
run_strict
[ "$rc" -ne 0 ] && grep 'olivares_1.2.3_darwin_arm64.tar.gz' "$WORK/err.$n" >/dev/null
check "strict refuses a signed member that is absent" "completeness, not presence" $?
# …and ordinary mode does NOT, on the SAME bytes, which is why strict had to exist at all.
run_verify "$WORK/missing-platform.txt"
[ "$rc" -eq 0 ]
check "ordinary consumer mode still accepts the same partial download" "behaviour preserved" $?
cp "$WORK/with-archive.txt" checksums.txt || exit 1

# (4) EVERY ARCHIVE NEEDS BOTH ATTESTATIONS, each refused on its own.
run_strict
[ "$rc" -ne 0 ] && grep 'no SBOM attestation for' "$WORK/err.$n" >/dev/null
check "strict refuses an archive with no SBOM attestation" "unattested is unpublishable" $?
echo "{}" >olivares_1.2.3_linux_amd64.tar.gz.sbom.sigstore.json
run_strict
[ "$rc" -ne 0 ] && grep 'no OpenVEX attestation for' "$WORK/err.$n" >/dev/null
check "strict refuses an archive with no VEX attestation" "both bundles, separately" $?
echo "{}" >olivares_1.2.3_linux_amd64.tar.gz.vex.sigstore.json

# (5) PROVENANCE IS REQUIRED, and its absence is not a skip.
mv multiple.intoto.jsonl "$WORK/held.intoto.jsonl" || exit 1
run_strict
[ "$rc" -ne 0 ] && grep 'no \*.intoto.jsonl provenance is present' "$WORK/err.$n" >/dev/null
check "strict refuses a release with no provenance" "RUN_SLSA is a profile constant" $?
mv "$WORK/held.intoto.jsonl" multiple.intoto.jsonl || exit 1

# (6) THE COMPLETE SHAPE PASSES. Without this the five rows above are satisfied by a mode
# that simply always fails.
run_strict
[ "$rc" -eq 0 ] && grep 'strict-publication: 2 signed member(s), all present' "$WORK/out.$n" >/dev/null
check "strict ACCEPTS a complete candidate" "the non-firing direction" $?

# (7) THE PROVENANCE SIGNATURE, IN FOUR DISTINCT OUTCOMES. Root return 01: an earlier
# revision accepted a missing slsa-verifier, printed a LIMIT line and exited zero. The
# envelope being present and its subject digests matching do not authenticate it, so each of
# the three ways the signature can fail to be established must FAIL, and only a real
# verification may pass.
#
# (7a) The verifier is not installed. `binmin` is a restricted PATH rather than a deleted
# stub, because the host may carry a real slsa-verifier and this row has to be hermetic.
n=$((n + 1))
env PATH="$WORK/binmin" bash "$SCRIPT" --strict-publication \
	--cert-identity "$STRICT_ID" --source-uri "$STRICT_URI" \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -ne 0 ] && grep 'slsa-verifier is not installed' "$WORK/err.$n" >/dev/null &&
	grep 'slsa-framework/slsa-verifier' "$WORK/err.$n" >/dev/null
check "strict FAILS when slsa-verifier is not installed" "no signature, no publication" $?
! grep '^LIMIT:' "$WORK/err.$n" >/dev/null
check "and it emits no LIMIT waiver" "the waiver path is gone" $?
! grep 'strict-publication: .* signed member' "$WORK/out.$n" >/dev/null
check "and it never reports strict publication as passed" "no success line before the failure" $?

# (7b) The verifier is installed and REJECTS an archive. Distinct from absent: the tool ran.
run_strict_rc() { # run_strict_rc <SLSA_STUB_RC>
	n=$((n + 1))
	env PATH="$WORK/bin:$PATH" SLSA_STUB_RC="$1" bash "$SCRIPT" --strict-publication \
		--cert-identity "$STRICT_ID" --source-uri "$STRICT_URI" \
		>"$WORK/out.$n" 2>"$WORK/err.$n"
	rc=$?
}
run_strict_rc 1
[ "$rc" -ne 0 ] && grep 'slsa-verifier rejected' "$WORK/err.$n" >/dev/null &&
	grep 'olivares_1.2.3_linux_amd64.tar.gz' "$WORK/err.$n" >/dev/null
check "strict FAILS when slsa-verifier rejects an archive" "and names which one" $?

# (7c) Zero archives verified. In strict mode the reachable form is a signed set with no
# archive in it: there is nothing for the provenance to cover, so nothing is attested.
cp "$WORK/no-archive.txt" checksums.txt || exit 1
run_strict
[ "$rc" -ne 0 ] && grep 'no archive to attest' "$WORK/err.$n" >/dev/null
check "strict FAILS when no archive is verified at all" "zero checks is not a pass" $?
cp "$WORK/with-archive.txt" checksums.txt || exit 1

# (7d) A real verification passes, and the summary now states the count it verified rather
# than carrying a caveat. Without this row the three above are satisfied by a mode that
# always fails.
run_strict
[ "$rc" -eq 0 ] && grep 'provenance verified for 1 archive(s)' "$WORK/out.$n" >/dev/null
check "strict ACCEPTS a verified provenance and states the count" "the non-firing direction" $?

# CONSUMER MODE IS UNCHANGED BY ALL OF THIS. The same absent verifier that now fails a
# publisher must still be an honest skip for someone checking a download.
n=$((n + 1))
env PATH="$WORK/binmin" bash "$SCRIPT" --key key.pub --offline \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -eq 0 ] && grep 'slsa-verifier not installed' "$WORK/out.$n" >/dev/null
check "consumer mode still skips an absent verifier and exits zero" "behaviour preserved" $?

# ============================================================================================
# (8) THE DELEGATED IDENTITY (root return 02, F4). Strict mode used to anchor to a production
# literal and to accept ANY SemVer tag of it, so the preprod profile this chain supports could
# not pass — real cosign rejects every attestation against the wrong repository, and the SLSA
# source URI named the wrong one too. The identity is now an ARGUMENT, and these rows measure
# the argv the adapters actually received rather than the exit code alone.
# ============================================================================================
strict_no_id() { # strict_no_id <args…> — strict WITHOUT the pair run_strict supplies
	n=$((n + 1))
	env PATH="$WORK/bin:$PATH" bash "$SCRIPT" --strict-publication "$@" \
		>"$WORK/out.$n" 2>"$WORK/err.$n"
	rc=$?
}

strict_no_id
[ "$rc" -ne 0 ] && grep -- '--cert-identity is required' "$WORK/err.$n" >/dev/null
check "strict refuses with no --cert-identity" "no default literal to fall back to" $?

strict_no_id --cert-identity "$STRICT_ID"
[ "$rc" -ne 0 ] && grep -- '--source-uri is required' "$WORK/err.$n" >/dev/null
check "strict refuses with no --source-uri" "the SLSA source is the same identity" $?

# THE ANCHOR MUST BE WHAT IT CLAIMS TO BE. cosign matches an identity regexp UNANCHORED, so an
# unanchored pattern accepts far more identities than the one that signs a release; a
# wildcarded workflow segment does the same one level down.
strict_no_id --cert-identity 'https://github\.com/acme/product/\.github/workflows/release\.yml@refs/tags/v1\.2\.3' --source-uri "$STRICT_URI"
[ "$rc" -ne 0 ] && grep -- 'must be fully anchored' "$WORK/err.$n" >/dev/null
check "strict refuses an unanchored identity" "unanchored matches unanchored" $?

strict_no_id --cert-identity '^https://github\.com/acme/product/\.github/workflows/.*$' --source-uri "$STRICT_URI"
[ "$rc" -ne 0 ] && grep -- 'must not wildcard the workflow path' "$WORK/err.$n" >/dev/null
check "strict refuses a wildcarded workflow path" "any workflow is not this workflow" $?

strict_no_id --cert-identity '^https://github\.com/acme/product@refs/tags/v1\.2\.3$' --source-uri "$STRICT_URI"
[ "$rc" -ne 0 ] && grep -- 'must pin the workflow path' "$WORK/err.$n" >/dev/null
check "strict refuses an identity that names no workflow" "the path is part of the identity" $?

# (8b) THE ARGV, AND THE VARIABLE THAT MUST NOT REACH IT. OLIVARES_CERT_IDENTITY and
# OLIVARES_SOURCE_URI are repository variables — settable by any repository admin, never
# reviewed in a pull request — so strict mode must not consult them even when they are set.
# Both decoys below are well-formed, so a script that read them would pass its own grammar
# checks and quietly verify a different claim.
DECOY_ID='^https://github\.com/decoy/decoy/\.github/workflows/release\.yml@refs/tags/v9\.9\.9$'
DECOY_URI='github.com/decoy/decoy'
n=$((n + 1))
: >"$WORK/cosign.args"
: >"$WORK/slsa.args"
env PATH="$WORK/bin:$PATH" \
	COSIGN_ARGS_LOG="$WORK/cosign.args" SLSA_ARGS_LOG="$WORK/slsa.args" \
	OLIVARES_CERT_IDENTITY="$DECOY_ID" OLIVARES_SOURCE_URI="$DECOY_URI" \
	bash "$SCRIPT" --strict-publication --cert-identity "$STRICT_ID" --source-uri "$STRICT_URI" \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -eq 0 ]
check "strict accepts with the supplied identity and both decoys set" "the non-firing direction" $?
_idcalls="$(grep -c -- '--certificate-identity-regexp' "$WORK/cosign.args")" || _idcalls=0
[ "${_idcalls:-0}" -eq 3 ]
check "all three cosign identity calls were recorded" "checksums + SBOM + VEX" $?
[ "$(grep -cF -- "--certificate-identity-regexp $STRICT_ID" "$WORK/cosign.args")" -eq 3 ]
check "every delegated cosign call carries the SUPPLIED identity" "argv, not exit code" $?
! grep -qF -- 'decoy' "$WORK/cosign.args"
check "and no call carries the repository variable" "no admin-mutable identity" $?
! grep -qF -- 'olivaresai/olivares' "$WORK/cosign.args"
check "and none falls back to the production literal" "a second profile can pass" $?
! grep -qE -- 'refs/tags/v\[0-9\]\+' "$WORK/cosign.args"
check "and none uses the any-SemVer pattern" "this tag, not a release of ours" $?
grep -qF -- "--source-uri $STRICT_URI" "$WORK/slsa.args"
check "slsa-verifier receives the SUPPLIED source repository" "same derived identity" $?
! grep -qF -- 'decoy' "$WORK/slsa.args"
check "and not the repository variable" "no admin-mutable source" $?

# (8b-bis) THE ISSUER IS PART OF THE SAME CONTRACT (root return 03, closure 1). Strict mode
# stated that it reads none of the OLIVARES_* anchors, and for the identity and the source URI
# that held because both are required arguments. The issuer's argument is OPTIONAL, and the
# resolution only OVERRODE a value already initialised from OLIVARES_CERT_OIDC_ISSUER — so
# omitting the argument left the environment's issuer in place. The finalizer always passes it,
# so its path was protected; standalone strict mode was not, and a contract that is true only
# because of one caller is not the contract.
DECOY_ISSUER='https://decoy.invalid/oidc'
DEFAULT_ISSUER='https://token.actions.githubusercontent.com'
n=$((n + 1))
: >"$WORK/cosign.args"
env PATH="$WORK/bin:$PATH" COSIGN_ARGS_LOG="$WORK/cosign.args" \
	OLIVARES_CERT_OIDC_ISSUER="$DECOY_ISSUER" \
	bash "$SCRIPT" --strict-publication --cert-identity "$STRICT_ID" --source-uri "$STRICT_URI" \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -eq 0 ]
check "strict accepts with the issuer decoy set and no issuer argument" "the non-firing direction" $?
! grep -qF -- "$DECOY_ISSUER" "$WORK/cosign.args"
check "and the environment issuer reaches no cosign call" "no admin-mutable issuer" $?
[ "$(grep -cF -- "--certificate-oidc-issuer $DEFAULT_ISSUER" "$WORK/cosign.args")" -eq 3 ]
check "every call carries the reviewed DEFAULT issuer instead" "argument or default, never the variable" $?

# AND AN EXPLICIT ISSUER ARGUMENT IS STILL HONOURED, which is how the finalizer passes the one
# it derived. Without this row the three above are satisfied by a mode that ignores the flag.
ALT_ISSUER='https://token.actions.example.invalid'
n=$((n + 1))
: >"$WORK/cosign.args"
env PATH="$WORK/bin:$PATH" COSIGN_ARGS_LOG="$WORK/cosign.args" \
	OLIVARES_CERT_OIDC_ISSUER="$DECOY_ISSUER" \
	bash "$SCRIPT" --strict-publication --cert-identity "$STRICT_ID" --source-uri "$STRICT_URI" \
	--cert-oidc-issuer "$ALT_ISSUER" \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -eq 0 ] && [ "$(grep -cF -- "--certificate-oidc-issuer $ALT_ISSUER" "$WORK/cosign.args")" -eq 3 ]
check "an explicit --cert-oidc-issuer is used" "the caller's derived issuer" $?
! grep -qF -- "$DECOY_ISSUER" "$WORK/cosign.args"
check "and it still beats the environment" "argument over variable" $?

# CONSUMER MODE KEEPS THE ISSUER OVERRIDE. A fork signs under its own issuer and has no
# publisher context to derive one from.
n=$((n + 1))
: >"$WORK/cosign.args"
env PATH="$WORK/bin:$PATH" COSIGN_ARGS_LOG="$WORK/cosign.args" \
	OLIVARES_CERT_OIDC_ISSUER="$DECOY_ISSUER" bash "$SCRIPT" \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -eq 0 ] && grep -qF -- "--certificate-oidc-issuer $DECOY_ISSUER" "$WORK/cosign.args"
check "consumer mode still honours OLIVARES_CERT_OIDC_ISSUER" "fork behaviour preserved" $?

# (8c) CONSUMER MODE KEEPS ITS OVERRIDES. A fork with no publisher context has nothing to
# derive from, so the variables must still work there — the whole point of scoping the change.
n=$((n + 1))
: >"$WORK/cosign.args"
env PATH="$WORK/bin:$PATH" COSIGN_ARGS_LOG="$WORK/cosign.args" \
	OLIVARES_CERT_IDENTITY="$DECOY_ID" bash "$SCRIPT" \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -eq 0 ] && grep -qF -- "--certificate-identity-regexp $DECOY_ID" "$WORK/cosign.args"
check "consumer mode still honours OLIVARES_CERT_IDENTITY" "fork behaviour preserved" $?

# ============================================================================================
# (9) THE ISOLATED COSIGN LAYOUT (root return 02, F1). The phase-2 job authenticates cosign
# and MOVES it off PATH; this script required the bare name, so a complete candidate could
# never be verified there. These rows run it in that exact layout — no `cosign` on PATH,
# OLIVARES_COSIGN_BIN naming the same bytes — and through the established wrapper, which
# re-authenticates them.
# ============================================================================================
# ONLY COSIGN IS REMOVED, which is exactly what --isolate does: it moves that one binary and
# leaves the runner otherwise intact. A PATH narrowed further than the job narrows it would
# measure a different environment — and it did, on the first run of these rows: the wrapper
# chain resolves `dirname`, and without it the rows failed for a reason the job does not have.
mkdir -p "$WORK/noc" || exit 1
for _t in sha256sum awk sed date readlink dirname bash; do
	ln -sf "$(command -v "$_t")" "$WORK/noc/$_t" || exit 1
done
ln -sf "$WORK/bin/slsa-verifier" "$WORK/noc/slsa-verifier" || exit 1
! (PATH="$WORK/noc" command -v cosign >/dev/null 2>&1)
check "the isolated PATH really has no cosign on it" "the layout under test is the job's" $?

n=$((n + 1))
: >"$WORK/cosign.args"
env PATH="$WORK/noc" COSIGN_ARGS_LOG="$WORK/cosign.args" \
	OLIVARES_COSIGN_BIN="$WORK/bin/cosign" OLIVARES_COSIGN_ALLOW_UNOFFICIAL=1 \
	bash "$SCRIPT" --strict-publication --cert-identity "$STRICT_ID" --source-uri "$STRICT_URI" \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -eq 0 ]
check "strict ACCEPTS a complete candidate with cosign OFF PATH" "the job's real layout" $?
[ "$(grep -c -- '--certificate-identity-regexp' "$WORK/cosign.args")" -eq 3 ]
check "and the isolated binary was the one that ran" "through the verified wrapper" $?

# THE WRAPPER IS WHAT AUTHENTICATES THE BYTES, so its absence must fail rather than fall back
# to the bare name that isolation deliberately removed.
n=$((n + 1))
cp "$SCRIPT" "$WORK/lonely-verify-release.sh" || exit 1
env PATH="$WORK/noc" OLIVARES_COSIGN_BIN="$WORK/bin/cosign" \
	bash "$WORK/lonely-verify-release.sh" --strict-publication \
	--cert-identity "$STRICT_ID" --source-uri "$STRICT_URI" \
	>"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -ne 0 ] && grep -q 'cosign-verified.sh is missing' "$WORK/err.$n"
check "a named binary with no wrapper beside it FAILS" "never an unverified execution" $?
! grep -q 'error: cosign not found' "$WORK/err.$n" "$WORK/out.$n"
check "and it does not fall back to a bare cosign" "isolation is not undone" $?

# AND THE ORDINARY CONSUMER MESSAGE IS UNTOUCHED when nothing was isolated at all.
n=$((n + 1))
env PATH="$WORK/noc" bash "$SCRIPT" >"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
[ "$rc" -ne 0 ] && grep -q 'error: cosign not found' "$WORK/out.$n"
check "with no OLIVARES_COSIGN_BIN, an absent cosign says so as before" "consumer preserved" $?

# ============================================================================================
# (10) --help DESCRIBES THE PROGRAM (root return 02, F8). It used to print lines 2-40 of the
# file, so the header growing pushed the option list out of view and --source-tag,
# --provenance and --strict-publication stopped being documented at all.
# ============================================================================================
n=$((n + 1))
bash "$SCRIPT" --help >"$WORK/out.$n" 2>"$WORK/err.$n"
rc=$?
_missing=""
for _opt in --key --offline --source-tag --provenance --strict-publication --cert-identity --cert-oidc-issuer --source-uri --help; do
	grep -qF -- "$_opt" "$WORK/out.$n" || _missing="$_missing $_opt"
done
[ "$rc" -eq 0 ] && [ -z "$_missing" ]
check "--help lists every option the parser accepts" "${_missing:-all present}" $?
# THE COUNTERPART, so the row above cannot be satisfied by printing the whole file: every
# option the `case` accepts is listed, and the help does not run past its own text.
_parsed="$(command grep -oE '^[[:space:]]+(--[a-z-]+\|)*--[a-z-]+\)' "$SCRIPT" | tr -d ' )' | tr '|' '\n' | grep -c -- '^--')"
[ "$_parsed" -eq 8 ]
check "the parser still accepts exactly the eight long options listed" "help and parser agree" $?

echo ""
echo "verify-release summary battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
