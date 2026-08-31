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
# Hermetic: PATH-stubbed cosign and slsa-verifier; sha256sum is real, the signature is
# stub-verified. Nothing verifies cryptography here — the property under test is the
# HONESTY of the summary relative to what step 5 counted.
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
cat >"$WORK/bin/cosign" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat >"$WORK/bin/slsa-verifier" <<'EOF'
#!/usr/bin/env bash
exit 0
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

echo ""
echo "verify-release summary battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
