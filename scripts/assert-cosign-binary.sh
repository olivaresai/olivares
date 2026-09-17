#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# assert-cosign-binary.sh — the EXECUTION-TIME invariant: the cosign about to sign this
# release is byte-for-byte an upstream-published artifact of an approved version, whatever
# put it there.
#
# WHY THIS EXISTS, AND WHY IT IS THE REAL CONTROL. This repository also has a static gate
# (cmd/olivares/tools/checkcosignpins) that reads the workflow YAML and requires every
# cosign installation to be the reviewed `sigstore/cosign-installer` step at the approved
# version. That gate is useful and fast, but four rounds of adversarial review demonstrated
# what it can never be: complete. A workflow can obtain cosign through an inline `run:`, a
# package manager, a container image, a matrix-selected image, a service `entrypoint`, a
# repository script, or a third-party action whose implementation nobody here can read.
# `run:` is a shell — proving "no path in this repository can obtain an unapproved cosign"
# by reading YAML is proving a negative over a Turing-complete language, and every review
# round found another valid construct.
#
# At EXECUTION time the same property is decidable in four lines: resolve the binary, hash
# it, compare. It does not matter how it arrived. So this script PLUS
# scripts/cosign-verified.sh — which every signer runs through, and which re-checks the
# bytes immediately before each invocation — are together the control, and the static gate
# is demoted to fast feedback and defence in depth. This script alone is not the control:
# see "WHAT IT DOES NOT ESTABLISH" below.
#
# WHAT IT ESTABLISHES, AND WHEN
#   1. `cosign` resolves to a real file, and the ABSOLUTE path is printed and exported, so
#      later steps invoke exactly what was verified instead of trusting PATH again.
#   2. Its SHA-256 is one of the digests upstream published — i.e. it is the official
#      artifact, not a local build, a mirror, or a substituted binary. The DIGEST decides;
#      the version is then a cross-check that the table's labels are right.
#   3. The bytes are authenticated BEFORE they are executed. An earlier revision ran
#      `cosign version` first and hashed afterwards, so the check meant to decide whether a
#      binary should run had already run it.
#
# WHAT IT DOES NOT ESTABLISH — SAY IT PLAINLY. This is true AT THE MOMENT OF HASHING. A
# pathname is not an executable identity: exporting a verified path to later steps publishes
# a NAME, not an inode, and QEMU, Buildx, registry logins, GoReleaser and uploads all run in
# between. That is why nothing signs through this path directly — every signer goes through
# scripts/cosign-verified.sh, which re-authenticates immediately before each invocation. Even
# that leaves a hash-then-exec window that only descriptor-bound execution would close, and
# that residual is stated there rather than papered over.
#
# IT RUNS OFFLINE, BY CONSTRUCTION. The digest table is VERSIONED IN THIS REPOSITORY. This
# script makes no network call whatsoever — it uses only `command`, `readlink`, `sha256sum`,
# `awk`, `sed`, `date`, `printf` and `echo`. Fetching `cosign_checksums.txt` at signing time
# would have put a network dependency and a remote trust point ON THE SIGNING PATH, which is
# precisely the wrong place for either. Refreshing the table is a separate, deliberate,
# network-using MAINTENANCE operation: scripts/update-cosign-digests.sh.
#
# PROVENANCE OF THE TABLE BELOW — v2.6.4, established 2026-07-25, reproducible with
# scripts/update-cosign-digests.sh:
#   * downloaded https://github.com/sigstore/cosign/releases/download/v2.6.4/cosign_checksums.txt
#     together with cosign_checksums.txt-keyless.{sig,pem};
#   * verified with cosign v2.6.4 (itself the published cosign-linux-amd64 artifact):
#         cosign verify-blob --certificate …-keyless.pem --signature …-keyless.sig \
#           --certificate-identity 'keyless@projectsigstore.iam.gserviceaccount.com' \
#           --certificate-oidc-issuer 'https://accounts.google.com' cosign_checksums.txt
#         -> Verified OK
#     (the identity is sigstore's own release service account, NOT a GitHub Actions
#      identity — asserting the latter fails, and the failure is what identified it);
#   * transcribed every PLAIN raw binary line. The `-pivkey-pkcs11key-` variants are
#     deliberately NOT approved: they are a different build with a different feature set,
#     and "the reviewed artifact" means the one whose contract check-cosign-contract.sh
#     actually proved. The .deb/.rpm/.apk packages and .sbom.json files are not executables
#     and are out of scope here.
set -euo pipefail

# ⛔ TRES RESPUESTAS, NO DOS. Este guion declara arriba las herramientas por las que mira el
#    mundo, y sin ellas no puede mirar. El caso que más duele es `sha256sum`: el digest se calcula
#    con él, y bajo `pipefail` su ausencia aborta con el código del propio comando — y sin
#    `pipefail` dejaría el digest VACÍO, ninguna fila de la tabla casaría, y el guion diría
#    **«este binario de cosign no está aprobado»** acusando a un binario legítimo.
#
#    Ninguna de las dos salidas es honesta: «no he podido calcular el digest» no es «el binario es
#    otro». Se comprueba antes, se nombra la herramienta que falta y se sale 2 — nunca 1, que aquí
#    significa «binario no aprobado», y nunca 0.
for _tool in command readlink sha256sum awk sed date printf; do
	if ! command -v "$_tool" >/dev/null 2>&1; then
		echo "assert-cosign-binary: ⛔ NO HE PODIDO MIRAR: '$_tool' is not on this host, so the binary was never hashed. This says NOTHING about whether it is approved — install $_tool and re-run." >&2
		exit 2
	fi
done

# --- approved versions -------------------------------------------------------------------
# APPROVED_COSIGN is the version whose signing contract is proven by
# scripts/check-cosign-contract.sh. Do NOT bump it without running that fixture against the
# new binary first.
APPROVED_COSIGN="v2.6.4"

# MIGRATION WINDOW — how this survives v2 -> v3 without becoming a lax allowlist.
#
# A migration cannot be atomic: for some period both generations must be acceptable, or the
# cutover has to happen in one irreversible step across every job at once. So a SECOND
# version may be approved, under four constraints that are enforced HERE, not promised in a
# comment:
#
#   1. it needs its own complete digest table — no version is ever approved by number alone;
#   2. it needs an explicit OPEN date and EXPIRY date, both in this reviewed file;
#   3. the window may not exceed MAX_MIGRATION_DAYS. A window that can be extended by doing
#      nothing is not a window;
#   4. after the expiry the migration version is REFUSED with a hard error. The failure mode
#      of a forgotten migration is therefore a red gate, not a permanently widened allowlist.
#
# Both versions must have passed scripts/check-cosign-contract.sh before the window opens;
# scripts/update-cosign-digests.sh refuses to open one otherwise.
#
# Empty MIGRATION_COSIGN = window closed = exactly one approved version. That is the state
# this repository is in today.
MIGRATION_COSIGN=""
MIGRATION_OPENED="" # YYYY-MM-DD, UTC
MIGRATION_EXPIRES="" # YYYY-MM-DD, UTC
MAX_MIGRATION_DAYS=90

# platform-suffix -> sha256, from the upstream signed checksums file (see PROVENANCE above).
read -r -d '' APPROVED_DIGESTS <<'DIGESTS' || true
ec648fddfedf1dad59dff9fbab177284a618204e03126ea37a87ab3cec4e7cb1  cosign-darwin-amd64
b2987c1b55a1e2735c59ac5c3e140acbf7ba5c1ed0cc07dbbf1b85676595237e  cosign-darwin-arm64
309779b0c4e409186b0a80daba99041fe2cf65a920ce645013901df6211895a9  cosign-linux-amd64
37f8c0e775d56d69a86a1cb3d82818feed3047f989faa99823c326ce151c4d33  cosign-linux-arm
df408e5418129306fed7349ec46e27be0445d05c5127c07f435e9a566af67593  cosign-linux-arm64
fcbcc778ba1a56b24e13f1a668f68d0401b8a9af42c31f4a8baffb07a5929bce  cosign-linux-ppc64le
72211f4e268db6b7a19b669696b23fc5df9f0dcb1e4aea64b64c849c0c1c2fbc  cosign-linux-riscv64
0ba4b5b53f1f3ed761512d6021446e1cc1ab9a8d69512f4ccd0730ae3bd3d7c0  cosign-linux-s390x
561368261ef6bb532a112ac9f952a6e9fd227e2d970ed0c9186ef9ce8d37f639  cosign-windows-amd64.exe
DIGESTS

# Populated by scripts/update-cosign-digests.sh when a window is opened; empty otherwise.
MIGRATION_DIGESTS=""

die() {
	echo "::error::cosign-binary: $*" >&2
	exit 1
}

# A ROW THAT DOES NOT PARSE IS AN ERROR, NOT AN ABSENT ROW. Both earlier versions
# filtered rows and compared what survived, so a malformed row simply vanished from the
# set and the comparison stayed green — the same shape as the mawk defect below, where a
# pattern that never matched was indistinguishable from a rule that passed.
#
# NO ERE INTERVALS ({64}) HERE either. mawk 1.3.4 — the awk on this container and on many
# minimal images — does not implement them, so `/^[0-9a-f]{64}  /` never fires and the
# whole rule evaluates to "nothing is missing". That is how it behaved when first
# written. Length and alphabet are asserted in code, where they cannot depend on the awk
# implementation.
validate_table() { # validate_table <label> <table-text>
	local label="$1" table="$2" bad
	bad="$(printf '%s\n' "$table" | awk -v L="$label" '
		NF == 0 { next }
		NF != 2 { printf "%s: line %d has %d field(s), want 2: %s\n", L, NR, NF, $0; next }
		length($1) != 64 { printf "%s: line %d digest is %d chars, want 64: %s\n", L, NR, length($1), $1; next }
		$1 ~ /[^0-9a-f]/ { printf "%s: line %d digest is not lowercase hex: %s\n", L, NR, $1; next }
	')"
	[ -z "$bad" ] || die "malformed digest table — every row must be <64 lowercase hex>  <artifact>:
$bad"
}

validate_table "APPROVED_DIGESTS" "$APPROVED_DIGESTS"
validate_table "MIGRATION_DIGESTS" "$MIGRATION_DIGESTS"

# --- migration-window validation (before anything is accepted) ---------------------------
# A malformed window must fail CLOSED. Approving a version whose dates cannot be parsed
# would be the same as approving it forever.
if [ -n "$MIGRATION_COSIGN" ]; then
	[ -n "$MIGRATION_OPENED" ] && [ -n "$MIGRATION_EXPIRES" ] ||
		die "MIGRATION_COSIGN=$MIGRATION_COSIGN is set without both MIGRATION_OPENED and MIGRATION_EXPIRES; a window with no end is not a window."
	[ -n "$MIGRATION_DIGESTS" ] ||
		die "MIGRATION_COSIGN=$MIGRATION_COSIGN has no digest table; no version is ever approved by version number alone."
	opened_s="$(date -u -d "$MIGRATION_OPENED" +%s 2>/dev/null)" ||
		die "MIGRATION_OPENED=$MIGRATION_OPENED is not a parseable UTC date (want YYYY-MM-DD)."
	expires_s="$(date -u -d "$MIGRATION_EXPIRES" +%s 2>/dev/null)" ||
		die "MIGRATION_EXPIRES=$MIGRATION_EXPIRES is not a parseable UTC date (want YYYY-MM-DD)."
	[ "$expires_s" -gt "$opened_s" ] ||
		die "MIGRATION_EXPIRES ($MIGRATION_EXPIRES) is not after MIGRATION_OPENED ($MIGRATION_OPENED)."
	span_days=$(((expires_s - opened_s) / 86400))
	[ "$span_days" -le "$MAX_MIGRATION_DAYS" ] ||
		die "the migration window is ${span_days} days, over the ${MAX_MIGRATION_DAYS}-day maximum. Finish the migration or re-open a shorter window deliberately."
	# "Its own COMPLETE table" has to be ENFORCED, not merely asserted in a comment: a
	# migration lane carrying one platform would refuse every other runner mid-migration —
	# a release outage discovered at the worst possible moment.
	#
	# Compare the PLATFORM SETS, not the row counts. Counting accepts a table with the right
	# number of wrong platforms (nine rows all naming linux-amd64 would have passed), and it
	# rejects a legitimate SUPERSET — upstream adding a platform in the newer version is
	# exactly what happens during a migration. So: every approved platform must be present
	# in the migration table; extra platforms there are fine.

	approved_platforms="$(printf '%s\n' "$APPROVED_DIGESTS" | awk 'NF==2 {print $2}' | sort -u)"
	migration_platforms="$(printf '%s\n' "$MIGRATION_DIGESTS" | awk 'NF==2 {print $2}' | sort -u)"
	[ -n "$approved_platforms" ] ||
		die "the approved digest table has no rows; refusing to evaluate a migration window against nothing."
	missing="$(comm -23 <(printf '%s\n' "$approved_platforms") <(printf '%s\n' "$migration_platforms") | tr '\n' ' ')"
	[ -z "${missing// /}" ] ||
		die "the migration table is missing platform(s) the approved table covers: ${missing}— those runners would be refused mid-migration. Re-derive it with scripts/update-cosign-digests.sh ${MIGRATION_COSIGN}."
fi

# --- resolve --------------------------------------------------------------------------
# --check-path <abs> verifies ONE given path instead of resolving PATH. It is what
# scripts/cosign-verified.sh uses to re-authenticate the bytes immediately before every
# single cosign invocation, so the window between this job-level assertion and the actual
# signing command is as narrow as a shell can make it. See the LIFETIME note below.
check_path=""
quiet=0
isolate=0
while [ "$#" -gt 0 ]; do
	case "$1" in
	--check-path)
		check_path="${2:-}"
		[ -n "$check_path" ] || die "--check-path needs a path."
		shift 2
		;;
	--quiet)
		quiet=1
		shift
		;;
	--isolate)
		isolate=1
		shift
		;;
	*) die "unknown argument: $1" ;;
	esac
done

if [ "$isolate" = "1" ] && [ -n "$check_path" ]; then
	die "--isolate and --check-path are mutually exclusive: one MOVES the binary, the other re-checks one that is already where it belongs."
fi

# ISOLATION PRECONDITIONS, CHECKED BEFORE ANY WORK. Failing after printing "OK" would tell
# an operator the binary was verified and isolated when only the first half happened.
if [ "$isolate" = "1" ]; then
	# The local escape must never build the release signer.
	[ "${OLIVARES_COSIGN_ALLOW_UNOFFICIAL:-0}" != "1" ] ||
		die "--isolate refuses OLIVARES_COSIGN_ALLOW_UNOFFICIAL: that escape exists for local experiments, and using it here would isolate bytes that are not an approved artifact."
	# No fallback to /tmp, the repository, $HOME or the cwd: a wrong destination would leave
	# the real binary on PATH while reporting success.
	[ -n "${RUNNER_TEMP:-}" ] ||
		die "--isolate needs RUNNER_TEMP (a per-job temporary directory); it is only meaningful inside a runner."
	case "$RUNNER_TEMP" in
	/*) ;;
	*) die "RUNNER_TEMP='$RUNNER_TEMP' is not absolute." ;;
	esac
	[ -d "$RUNNER_TEMP" ] || die "RUNNER_TEMP='$RUNNER_TEMP' is not a directory."
	# Later steps can only use the isolated binary if they are told where it is, so an
	# unwritable GITHUB_ENV is a hard failure here, not an opportunistic skip.
	{ [ -n "${GITHUB_ENV:-}" ] && [ -w "${GITHUB_ENV}" ]; } ||
		die "--isolate needs a writable GITHUB_ENV to hand the isolated path to later steps."
fi

if [ -n "$check_path" ]; then
	# ABSOLUTE only. A relative path is resolved against whatever directory the caller
	# happens to be in, so the same string could name different bytes in two steps of the
	# same job — which is the precise property this check exists to deny.
	case "$check_path" in
	/*) ;;
	*) die "--check-path needs an ABSOLUTE path (got: $check_path)." ;;
	esac
	abs="$check_path"
	[ -f "$abs" ] || die "$abs is not a regular file."
	[ -x "$abs" ] || die "$abs is not executable."
else
	resolved="$(command -v cosign 2>/dev/null || true)"
	[ -n "$resolved" ] || die "cosign is not on PATH. The release path signs with it, so this is a hard failure, not a skip."
	# Follow symlinks so the digest is of the file that actually executes.
	abs="$(readlink -f "$resolved" 2>/dev/null || echo "$resolved")"
	[ -f "$abs" ] || die "cosign resolved to $resolved -> $abs, which is not a regular file."
fi

# --- authenticate the BYTES BEFORE executing them ----------------------------------------
# Order matters and an earlier revision had it backwards: it ran `cosign version` first and
# hashed afterwards, so an unapproved binary was executed by the very check meant to decide
# whether it should be. Digests are version-specific, so the hash alone selects the lane;
# the version is then a CROSS-CHECK on the table, not the gate.
digest="$(sha256sum "$abs" | awk '{print $1}')"

lane=""
expected_version=""
matched=""
while read -r want name; do
	[ -n "${want:-}" ] || continue
	if [ "$digest" = "$want" ]; then
		matched="$name"
		lane="approved"
		expected_version="$APPROVED_COSIGN"
		break
	fi
done <<<"$APPROVED_DIGESTS"

if [ -z "$matched" ] && [ -n "$MIGRATION_COSIGN" ]; then
	while read -r want name; do
		[ -n "${want:-}" ] || continue
		if [ "$digest" = "$want" ]; then
			now_s="$(date -u +%s)"
			if [ "$now_s" -lt "$opened_s" ]; then
				die "cosign $MIGRATION_COSIGN is approved only from $MIGRATION_OPENED; a window may not be used before it opens."
			fi
			if [ "$now_s" -gt "$expires_s" ]; then
				die "cosign $MIGRATION_COSIGN was approved only for the migration window that CLOSED on $MIGRATION_EXPIRES. Complete the migration (promote it to APPROVED_COSIGN) or open a new window deliberately; it is not approved by default."
			fi
			matched="$name"
			lane="migration (window $MIGRATION_OPENED..$MIGRATION_EXPIRES)"
			expected_version="$MIGRATION_COSIGN"
			break
		fi
	done <<<"$MIGRATION_DIGESTS"
fi

table="$APPROVED_DIGESTS"
[ -z "$MIGRATION_COSIGN" ] || table="$APPROVED_DIGESTS
$MIGRATION_DIGESTS"

if [ -z "$matched" ]; then
	# Bytes that are not a published artifact are exactly what an execution-time check
	# exists to catch: a local `go install` build, a mirror, a patched binary. Refused
	# unless someone says otherwise, loudly and on purpose.
	if [ "${OLIVARES_COSIGN_ALLOW_UNOFFICIAL:-0}" = "1" ]; then
		{
			echo "############################################################################"
			echo "# cosign-binary: UNOFFICIAL BINARY ACCEPTED by OLIVARES_COSIGN_ALLOW_UNOFFICIAL=1"
			echo "#   path:   $abs"
			echo "#   sha256: $digest"
			echo "# These bytes are NOT a published artifact of any approved version."
			echo "# This must NEVER be set in a job that publishes anything."
			echo "############################################################################"
		} >&2
		lane="unofficial"
	else
		echo "::error::cosign-binary: the sha256 of $abs is not a published artifact of any approved version." >&2
		echo "  sha256: $digest" >&2
		echo "Expected one of the upstream release digests:" >&2
		printf '%s\n' "$table" | sed 's/^/  /' >&2
		echo "A binary built locally (go install) or fetched from a mirror will land here. That is the" >&2
		echo "point: this check does not care HOW cosign arrived, only that it is the reviewed artifact." >&2
		echo "For a local experiment only: OLIVARES_COSIGN_ALLOW_UNOFFICIAL=1" >&2
		exit 1
	fi
fi

# --- version CROSS-CHECK, after authentication -------------------------------------------
# The digest already decided the verdict. Running the binary now can only catch a table
# whose rows are mislabelled — a real risk when a row is pasted by hand — and it does so
# without ever having executed unauthenticated bytes.
version="unverified"
if [ -n "$expected_version" ]; then
	if ! version_raw="$("$abs" version 2>&1)"; then
		echo "::error::cosign-binary: '$abs version' failed even though its bytes are a published artifact." >&2
		printf '%s\n' "$version_raw" | sed 's/^/  /' >&2
		exit 1
	fi
	version="$(printf '%s\n' "$version_raw" | awk '/GitVersion/ {print $2}')"
	[ -n "$version" ] || die "could not read a GitVersion from '$abs version'; refusing to guess what is installed."
	if [ "$version" != "$expected_version" ]; then
		die "TABLE IS WRONG: digest $digest is listed as $matched for $expected_version, but the binary reports $version. Re-derive the table with scripts/update-cosign-digests.sh."
	fi
fi

if [ "$quiet" = "1" ]; then
	exit 0
fi

echo "cosign-binary: OK ($version, ${matched:-unofficial-accepted}, sha256 ${digest:0:16}…, lane: ${lane})"
echo "cosign-binary: verified binary is $abs"

# --- ISOLATION: after authenticating it, take the binary OFF PATH ------------------------
# WHY. Everything above proves WHICH bytes are approved; it cannot stop a workflow step from
# writing `cosign sign …` and getting whatever the installer left on PATH, unauthenticated
# by the launcher. Rounds 1-7 tried to catch that by reading the workflow YAML and an
# adversarial review broke every version, because `run:` is a shell: it can install,
# construct, rename or execute another binary, so no static reader can decide it.
#
# Moving the authenticated binary to a private directory changes the FAILURE MODE from
# silent to loud: after this, the ordinary unguarded spelling does not name an executable at
# all, so a hand-written publisher fails in the job instead of signing with something nobody
# checked. That is a runtime fact, not an inference over shell text.
#
# ADJUDICATED, NOT INVENTED: this design and every postcondition below were approved by the
# adversarial reviewer in an internal design note (not shipped), after
# the author's own judgement was ruled out of scope. Two qualifications came with it and are
# recorded here because they bound what this may be claimed to achieve:
#   1. it is NOT a sandbox against hostile same-UID code, which can still fetch its own
#      binary; that needs a trusted-signer boundary, not a bigger checker;
#   2. the SLSA container reusable workflow signs in a SEPARATE job with its own cosign
#      v2.2.3 and is therefore OUTSIDE this control — see the residual in the session file.
if [ "$isolate" = "1" ]; then
	[ "$lane" != "unofficial" ] || die "--isolate refuses an unofficial binary."

	isodir="$(mktemp -d "$RUNNER_TEMP/olivares-cosign.XXXXXXXX")" || die "cannot create an isolation directory under $RUNNER_TEMP."
	chmod 0700 "$isodir" || die "cannot restrict $isodir to 0700."
	dest="$isodir/cosign"

	# A real move. Never `mv -n`, never `|| true`: "not moved" must not become success.
	mv -- "$abs" "$dest" || die "could not move $abs to $dest; refusing to report isolation that did not happen."

	# --- postconditions, all fail-closed ---------------------------------------------------
	[ -f "$dest" ] && [ -x "$dest" ] || die "isolation destination $dest is not a regular executable file."
	moved_digest="$(sha256sum "$dest" | awk '{print $1}')"
	[ "$moved_digest" = "$digest" ] ||
		die "the isolated binary's sha256 changed during the move (was $digest, now $moved_digest)."
	[ ! -f "$abs" ] || [ ! -x "$abs" ] ||
		die "$abs is STILL a regular executable after the move; the original was copied, not moved."
	hash -r 2>/dev/null || true
	if leftover="$(command -v cosign 2>/dev/null)" && [ -n "$leftover" ]; then
		die "'cosign' still resolves on PATH after isolation, to $leftover. Another copy is installed; isolation would be theatre."
	fi
	abs="$dest"
	echo "cosign-binary: ISOLATED to $abs — 'cosign' no longer resolves on PATH, so an unguarded"
	echo "cosign-binary: publisher now FAILS in this job instead of signing unauthenticated."
fi

# Later steps must invoke THIS path — and scripts/cosign-verified.sh re-authenticates its
# bytes on every invocation, because a pathname is not an executable identity.
if [ "$isolate" = "1" ]; then
	echo "OLIVARES_COSIGN_BIN=$abs" >>"$GITHUB_ENV"
elif [ -n "${GITHUB_ENV:-}" ] && [ -w "${GITHUB_ENV}" ]; then
	echo "OLIVARES_COSIGN_BIN=$abs" >>"$GITHUB_ENV"
fi
if [ -n "${GITHUB_STEP_SUMMARY:-}" ] && [ -w "${GITHUB_STEP_SUMMARY}" ]; then
	{
		echo "### cosign binary verified"
		echo ""
		echo "| field | value |"
		echo "|---|---|"
		echo "| version | \`$version\` |"
		echo "| lane | \`$lane\` |"
		echo "| artifact | \`${matched:-unofficial}\` |"
		echo "| sha256 | \`$digest\` |"
		echo "| path | \`$abs\` |"
	} >>"$GITHUB_STEP_SUMMARY"
fi
