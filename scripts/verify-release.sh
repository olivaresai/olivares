#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Verify an Olivares AI release the way a distrustful sysadmin should. For a
# security product the build pipeline is part of the trust model (docs/SECURITY-HARDENING.md) —
# never run an unverified binary. This checks, in order, whatever is present:
#
#   1. cosign signature over checksums.txt  (always)
#   2. SHA-256 of every downloaded artifact  (always)
#   3. SBOM in-toto attestation per archive   (SCP-03, if *.sbom.sigstore.json present)
#   4. OpenVEX attestation per archive        (SCP-04, if *.vex.sigstore.json present)
#   5. SLSA build provenance per archive      (SCP-01, if *.intoto.jsonl present)
#
# Steps 3–5 are skipped with a clear note when their files (or the verifier tool)
# are absent, so this works on a minimal release AND fully verifies a complete one.
#
# ⛔ …AND THAT CONSUMER BEHAVIOUR IS EXACTLY WRONG FOR A PUBLISHER, which is what
# --strict-publication is for. A person who downloaded three files wants to know that those
# three verify; the ceremony that is about to make a release public must know that NOTHING
# was skipped. In strict mode every artifact the signed checksums list must be PRESENT and
# match, every archive must carry both attestation bundles and both must verify, exactly one
# provenance document must be present, and the identity-bound keyless path is mandatory —
# `--key` proves possession of a key, not that this repository's release workflow signed on
# this tag. The ordinary behaviour above is unchanged when the flag is absent.
#
# Corrected 2026-09-11 (root return 01). An earlier revision of this mode accepted a missing
# slsa-verifier, printed a LIMIT line and still exited zero. That waived a ratified
# requirement and was not authorized. The provenance signature is checked only by
# slsa-verifier: a present envelope with matching subject digests is not an authenticated
# provenance. Strict mode therefore fails when the verifier is unavailable, when it rejects
# an archive, and when it verifies no archive at all. Installing slsa-verifier on the runner
# is a deployment prerequisite, named by the failure message; it is never satisfied by
# downgrading the check.
#
# Usage (run from the directory holding the downloaded release files):
#   verify-release.sh                      # keyless / Sigstore (default; network for Rekor)
#   verify-release.sh --key cosign.pub     # key-based
#   verify-release.sh --key cosign.pub --offline   # key-based, transparency log ignored
#   verify-release.sh --source-tag v1.2.3  # pin the SLSA provenance source tag
#   verify-release.sh --provenance FILE    # name the SLSA provenance explicitly (required
#                                          # when several *.intoto.jsonl files are present:
#                                          # an ambiguous selection FAILS, it never guesses)
#   verify-release.sh --strict-publication # publisher mode: no skipped verification passes
#   verify-release.sh --cert-identity RE   # the keyless certificate identity to require
#   verify-release.sh --cert-oidc-issuer U # the OIDC issuer to require
#   verify-release.sh --source-uri URI     # the SLSA source repository to require
#
# WHERE THE IDENTITY COMES FROM, AND WHY STRICT MODE WILL NOT TAKE IT FROM THE ENVIRONMENT.
# A consumer has no publisher context, so consumer mode keeps its defaults and its
# OLIVARES_CERT_IDENTITY / OLIVARES_CERT_OIDC_ISSUER / OLIVARES_SOURCE_URI overrides for a
# fork. Those are repository variables: settable by anyone with repository admin and never
# reviewed in a pull request. A publisher must not anchor to one, and it does not have to —
# scripts/release-finalize-stable.sh has already derived the exact repository, workflow path
# and tag from the authoritative workflow inputs it verified. Strict mode therefore REQUIRES
# --cert-identity and --source-uri from its caller, takes the issuer from --cert-oidc-issuer
# or from DEFAULT_CERT_OIDC_ISSUER, and reads none of those variables.
#
# Corrected 2026-09-11 (root return 02, F4). The delegated verification used to fall back to
# a production literal, so the preprod profile this chain supports could not pass it at all:
# real cosign would reject every attestation against a production anchor, and the SLSA source
# URI named the wrong repository. The anchor also accepted ANY SemVer tag of the repository,
# which is the right identity for "a release of ours" and the wrong one for "the release we
# are about to publish".
#
# WHAT "OFFLINE" HERE DOES AND DOES NOT MEAN (corrected 2026-07-25). `--offline` sets
# `--insecure-ignore-tlog` and, on the keyless path, cosign's own `--offline`. That removes
# the REKOR lookup. It is not by itself a promise that no socket is opened:
#   * KEY-BASED (`--key`) verification is genuinely disconnected, and this is MEASURED —
#     scripts/check-cosign-contract.sh performs exactly this `verify-blob --key` round trip
#     behind a proxy that refuses every connection, and it passes.
#   * KEYLESS verification still needs Sigstore TUF trusted-root material, which cosign
#     fetches unless it is already cached. On a genuinely air-gapped machine use `--key`.
#     (This script has NO `--trusted-root` option — an earlier draft of this note told
#     readers to pass one, which would simply have been rejected as an unknown argument.
#     Seeding TUF and driving cosign directly is the workaround; wiring `--trusted-root`
#     through here is an open follow-up.)
#   * The keyless disconnected path has NOT been measured here and is UNCERTAIN.
# The earlier wording called this "air-gap: no Rekor/tlog network at all", which was an
# unverified claim about someone else's tool.
#
# Expected in the current directory:
#   checksums.txt(.sig/.pem)             signed SHA-256 manifest (.pem keyless only)
#   <artifacts listed in checksums.txt>
#   <archive>.sbom.sigstore.json         (optional) signed SPDX SBOM attestation
#   <archive>.vex.sigstore.json          (optional) signed OpenVEX attestation
#   multiple.intoto.jsonl                (optional) SLSA provenance (slsa-github-generator)
#
# Requires: cosign, sha256sum. Optional in consumer mode and REQUIRED under
# --strict-publication: slsa-verifier (step 5). Keyless needs network
# (Rekor, and TUF trusted-root material); `--key` is the measured disconnected path — see
# the note above, which supersedes any shorter summary of this.
#
# HOW COSIGN IS INVOKED — TWO LAYOUTS, ONE RULE: never execute unauthenticated bytes.
# Corrected 2026-09-11 (root return 02, F1). The phase-2 publication job runs
# `scripts/assert-cosign-binary.sh --isolate` before the ceremony: that authenticates the
# binary against the upstream published digests, MOVES it to a private directory and refuses
# to report success while the bare name still resolves, so an unguarded `cosign …` fails in
# the job instead of signing with something nobody checked. This script is a consumer inside
# that job, and it required a bare `cosign` on PATH — so a complete and correct candidate was
# refused and nothing could ever be published. It now uses the same established wrapper every
# other consumer in that job uses:
#
#   OLIVARES_COSIGN_BIN set  -> scripts/cosign-verified.sh, which re-authenticates the bytes
#                               immediately before each invocation. The variable NAMES the
#                               binary; the wrapper is what makes it trustworthy, so its
#                               absence is a failure and never a fall back to the bare name.
#   OLIVARES_COSIGN_BIN unset-> `cosign` on PATH, byte-for-byte the ordinary consumer path.
#
# Nothing here puts cosign back on PATH or executes the named binary directly.
set -euo pipefail

KEY=""
OFFLINE=0
SOURCE_TAG=""
PROVENANCE=""
STRICT=0
# The keyless identity the release workflow signs as (GitHub OIDC). Override with
# OLIVARES_CERT_IDENTITY / OLIVARES_CERT_OIDC_ISSUER for a fork.
# The keyless Sigstore identity the release workflow signs as. FULLY ANCHORED: cosign
# matches --certificate-identity-regexp UNANCHORED, so the previous
# '^https://github.com/olivaresai/olivares' also accepted `.../olivares-anything/...`
# and any workflow file on any branch -- i.e. far more identities than the one that
# actually signs a release.
DEFAULT_CERT_IDENTITY='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
DEFAULT_CERT_OIDC_ISSUER='https://token.actions.githubusercontent.com'
CERT_IDENTITY_REGEXP="${OLIVARES_CERT_IDENTITY:-$DEFAULT_CERT_IDENTITY}"
CERT_OIDC_ISSUER="${OLIVARES_CERT_OIDC_ISSUER:-$DEFAULT_CERT_OIDC_ISSUER}"
SOURCE_URI="${OLIVARES_SOURCE_URI:-github.com/olivaresai/olivares}"
# Supplied by the caller, kept separate from the resolved values above so strict mode can
# require them instead of inheriting a repository variable.
CERT_IDENTITY_ARG=""
CERT_OIDC_ISSUER_ARG=""
SOURCE_URI_ARG=""

# EVERY OPTION, WRITTEN OUT. This used to be `sed -n '2,40p' "$0"`, which printed whatever
# happened to be on those lines: the header grew, --help stopped inside the prose, and it no
# longer listed --source-tag, --provenance or --strict-publication. A help text derived from
# line numbers stops describing the program the moment anyone edits above it.
usage() {
  cat <<'USAGE'
usage: verify-release.sh [options]
Run from the directory holding the downloaded release files.

  --key FILE              key-based verification instead of keyless Sigstore
  --offline               ignore the transparency log (implied by --key)
  --source-tag TAG        pin the SLSA provenance source tag
  --provenance FILE       name the SLSA provenance explicitly; required when several
                          *.intoto.jsonl files are present, which otherwise FAILS
  --strict-publication    publisher mode: no skipped verification counts as a pass
  --cert-identity RE      require this keyless certificate identity (regexp);
                          required under --strict-publication
  --cert-oidc-issuer URL  require this OIDC issuer
  --source-uri URI        require this SLSA source repository;
                          required under --strict-publication
  -h, --help              this text

Consumer mode also reads OLIVARES_CERT_IDENTITY, OLIVARES_CERT_OIDC_ISSUER and
OLIVARES_SOURCE_URI. Strict publication mode reads none of them: it takes the identity from
its caller, which derived it from the authoritative workflow inputs.
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --key) KEY="$2"; shift 2 ;;
    --offline) OFFLINE=1; shift ;;
    --source-tag) SOURCE_TAG="$2"; shift 2 ;;
    --provenance) PROVENANCE="$2"; shift 2 ;;
    --strict-publication) STRICT=1; shift ;;
    --cert-identity) CERT_IDENTITY_ARG="$2"; shift 2 ;;
    --cert-oidc-issuer) CERT_OIDC_ISSUER_ARG="$2"; shift 2 ;;
    --source-uri) SOURCE_URI_ARG="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

# CONSUMER MODE: an explicit argument wins over a variable, and a fork's variables still work.
# STRICT MODE: the identity comes from the caller or from the reviewed default, and never from
# the environment. All three anchors are re-resolved rather than overridden, because an
# override only reaches the values whose argument was supplied — an omitted --cert-oidc-issuer
# would otherwise leave the issuer at whatever OLIVARES_CERT_OIDC_ISSUER said, which is the
# admin-settable source this mode exists to exclude.
if [ "$STRICT" -eq 1 ]; then
  CERT_IDENTITY_REGEXP="$CERT_IDENTITY_ARG"
  CERT_OIDC_ISSUER="${CERT_OIDC_ISSUER_ARG:-$DEFAULT_CERT_OIDC_ISSUER}"
  SOURCE_URI="$SOURCE_URI_ARG"
else
  [ -z "$CERT_IDENTITY_ARG" ] || CERT_IDENTITY_REGEXP="$CERT_IDENTITY_ARG"
  [ -z "$CERT_OIDC_ISSUER_ARG" ] || CERT_OIDC_ISSUER="$CERT_OIDC_ISSUER_ARG"
  [ -z "$SOURCE_URI_ARG" ] || SOURCE_URI="$SOURCE_URI_ARG"
fi

# NO `dirname` HERE. It is an external command, and this script already runs in environments
# whose PATH was deliberately narrowed; a `dirname` that is not installed makes the inner
# substitution empty, `cd ""` stays where it is, and the wrapper would then be looked for in
# the DOWNLOAD directory. Measured while writing the battery for this change. Parameter
# expansion needs nothing on PATH.
case "$0" in
*/*) SELF_DIR="${0%/*}" ;;
*) SELF_DIR="." ;;
esac
SELF_DIR="$(cd -- "$SELF_DIR" && pwd -P)" ||
  { echo "error: cannot resolve this script's own directory" >&2; exit 1; }
COSIGN_WRAPPER="$SELF_DIR/cosign-verified.sh"
if [ -n "${OLIVARES_COSIGN_BIN:-}" ]; then
  # The isolated, authenticated layout. See the header note: the variable names the binary,
  # the wrapper is what authenticates it, and a missing wrapper is a failure rather than a
  # fall back to a bare name that isolation deliberately removed.
  [ -f "$COSIGN_WRAPPER" ] || {
    echo "error: OLIVARES_COSIGN_BIN is set but $COSIGN_WRAPPER is missing." >&2
    echo "That variable names a binary this script must not execute directly; the wrapper is" >&2
    echo "what re-authenticates its bytes. Refusing to fall back to a bare 'cosign'." >&2
    exit 1
  }
  cosign_run() { bash "$COSIGN_WRAPPER" "$@"; }
else
  command -v cosign >/dev/null || { echo "error: cosign not found (https://docs.sigstore.dev/cosign/installation)"; exit 1; }
  cosign_run() { cosign "$@"; }
fi
command -v sha256sum >/dev/null || { echo "error: sha256sum not found"; exit 1; }
[ -f checksums.txt ] || { echo "error: checksums.txt not found in $(pwd)"; exit 1; }
[ -f checksums.txt.sig ] || { echo "error: checksums.txt.sig not found"; exit 1; }

# STRICT PRECONDITIONS, stated before anything is verified, so a publisher never discovers
# halfway through that the mode it asked for could not apply.
strict_fail() { echo "strict-publication: $*" >&2; exit 1; }
if [ "$STRICT" -eq 1 ]; then
  echo "==> strict publication mode: no skipped verification counts as a pass"
  [ -z "$KEY" ] || strict_fail "--key is not accepted here. A key proves possession; publication needs the keyless identity that binds this repository's release workflow to this tag."
  [ "$OFFLINE" -eq 0 ] || strict_fail "--offline is not accepted here: the transparency-log check is part of what makes that identity mean anything."
  [ -f checksums.txt.pem ] || strict_fail "checksums.txt.pem is missing; the keyless certificate is a required member."
  # THE IDENTITY IS AN ARGUMENT HERE, NOT A VARIABLE. See the header note.
  [ -n "$CERT_IDENTITY_ARG" ] || strict_fail "--cert-identity is required in this mode.
  The publisher has already derived the repository, workflow path and tag it is publishing;
  anchoring to a default literal or to an admin-settable repository variable would verify a
  different claim than the one being made."
  [ -n "$SOURCE_URI_ARG" ] || strict_fail "--source-uri is required in this mode.
  The SLSA source repository is part of the same derived identity: a default literal names
  the production repository and no other profile can satisfy it."
  # Reject an override that is not what it claims to be, with the same three rules the
  # release workflow applies to its own anchor: fully anchored, pinning the workflow path,
  # and not wildcarding it. An unanchored pattern matches unanchored in cosign.
  case "$CERT_IDENTITY_REGEXP" in
  '^'*'$') ;;
  *) strict_fail "--cert-identity must be fully anchored (^…\$); got '${CERT_IDENTITY_REGEXP}'." ;;
  esac
  # `workflows/` and not `/.github/workflows/`: a real anchor escapes the dot for the regex
  # engine, so the literal spelling appears as `/\.github/workflows/` and matching on the
  # unescaped form rejects every correct identity. This is the same predicate the release
  # workflow applies to its own anchor.
  case "$CERT_IDENTITY_REGEXP" in
  *'workflows/'*) ;;
  *) strict_fail "--cert-identity must pin the workflow path; got '${CERT_IDENTITY_REGEXP}'." ;;
  esac
  case "${CERT_IDENTITY_REGEXP##*workflows/}" in
  *'.*'* | *'.+'*) strict_fail "--cert-identity must not wildcard the workflow path; got '${CERT_IDENTITY_REGEXP}'." ;;
  esac
fi

# Key-based (air-gap) signatures are produced WITHOUT a Rekor transparency-log
# entry, so their verification must ignore the tlog. Keyless signatures always
# carry a tlog entry; --offline tells cosign to use the entry bundled offline.
TLOG_IGNORE=0
{ [ -n "$KEY" ] || [ "$OFFLINE" -eq 1 ]; } && TLOG_IGNORE=1

# cosign attestation-verify flags shared by steps 3/4, parameterised by mode.
att_id_flags() {
  if [ -n "$KEY" ]; then
    printf -- '--key\n%s\n' "$KEY"
  else
    printf -- '--certificate-identity-regexp\n%s\n--certificate-oidc-issuer\n%s\n' "$CERT_IDENTITY_REGEXP" "$CERT_OIDC_ISSUER"
  fi
  [ "$TLOG_IGNORE" -eq 1 ] && printf -- '--insecure-ignore-tlog\n'
}

echo "==> 1/5 verifying the cosign signature over checksums.txt"
if [ -n "$KEY" ]; then
  cosign_run verify-blob --key "$KEY" --signature checksums.txt.sig \
    $([ "$TLOG_IGNORE" -eq 1 ] && echo --insecure-ignore-tlog) checksums.txt
else
  [ -f checksums.txt.pem ] || { echo "error: checksums.txt.pem (cert) required for keyless verify; or pass --key"; exit 1; }
  cosign_run verify-blob \
    --certificate checksums.txt.pem \
    --signature checksums.txt.sig \
    --certificate-identity-regexp "$CERT_IDENTITY_REGEXP" \
    --certificate-oidc-issuer "$CERT_OIDC_ISSUER" \
    $([ "$OFFLINE" -eq 1 ] && echo --offline) \
    checksums.txt
fi
echo "    signature OK"

echo "==> 2/5 verifying artifact checksums"
present=0
listed=0
missing=""
archives=""
while read -r sum name; do
  [ -n "${name:-}" ] || continue
  listed=$((listed + 1))
  if [ -f "$name" ]; then
    echo "$sum  $name" | sha256sum -c -
    present=$((present + 1))
    case "$name" in *.tar.gz|*.zip) archives="$archives $name" ;; esac
  else
    missing="$missing $name"
  fi
done < checksums.txt
[ "$present" -gt 0 ] || { echo "error: none of the artifacts in checksums.txt are present here"; exit 1; }
# ⛔ "WHATEVER IS PRESENT" IS A CONSUMER'S QUESTION. A publisher's is "is anything the
# signed set promises absent", and a release missing a platform, a package or the installer
# passes the loop above without a word.
if [ "$STRICT" -eq 1 ] && [ -n "$missing" ]; then
  echo "strict-publication: the signed checksums list $listed member(s); these are not here:" >&2
  printf '  %s\n' $missing >&2
  exit 1
fi
if [ "$STRICT" -eq 1 ] && [ -z "$archives" ]; then
  strict_fail "the signed set contains no archive to attest."
fi

# --- 3/5 SBOM attestations (SCP-03) ---------------------------------------------
echo "==> 3/5 verifying SBOM in-toto attestations (SPDX)"
sbom_checked=0
for a in $archives; do
  b="$a.sbom.sigstore.json"
  [ -f "$b" ] || continue
  cosign_run verify-blob-attestation --type spdxjson --bundle "$b" --new-bundle-format \
    --check-claims $(att_id_flags) "$a" >/dev/null
  echo "    SBOM attestation OK: $a"
  sbom_checked=$((sbom_checked + 1))
done
[ "$sbom_checked" -gt 0 ] || echo "    (skipped: no *.sbom.sigstore.json present)"
if [ "$STRICT" -eq 1 ]; then
  for a in $archives; do
    [ -f "$a.sbom.sigstore.json" ] || strict_fail "no SBOM attestation for $a. An unattested archive is not a publishable artifact."
  done
fi

# --- 4/5 OpenVEX attestations (SCP-04) ------------------------------------------
echo "==> 4/5 verifying OpenVEX attestations"
vex_checked=0
for a in $archives; do
  b="$a.vex.sigstore.json"
  [ -f "$b" ] || continue
  cosign_run verify-blob-attestation --type openvex --bundle "$b" --new-bundle-format \
    --check-claims $(att_id_flags) "$a" >/dev/null
  echo "    VEX attestation OK: $a"
  vex_checked=$((vex_checked + 1))
done
[ "$vex_checked" -gt 0 ] || echo "    (skipped: no *.vex.sigstore.json present)"
if [ "$STRICT" -eq 1 ]; then
  for a in $archives; do
    [ -f "$a.vex.sigstore.json" ] || strict_fail "no OpenVEX attestation for $a."
  done
fi

# --- 5/5 SLSA build provenance (SCP-01) -----------------------------------------
echo "==> 5/5 verifying SLSA build provenance"
# EXACT selection, never first-match. The old `ls | head -1` silently picked whichever
# *.intoto.jsonl sorted first: with two provenance files in the directory (say, one from
# a previous download next to the current one) the verifier could "pass" every archive
# against the WRONG release's provenance — the same defect class as the release
# workflow's old first-Docker-artifact attestation target. Zero files skips loudly, one
# file is used, several files REFUSE and demand --provenance.
if [ -n "$PROVENANCE" ]; then
  [ -f "$PROVENANCE" ] || { echo "error: --provenance '$PROVENANCE' does not exist"; exit 1; }
  prov="$PROVENANCE"
else
  prov=""
  prov_n=0
  for f in ./*.intoto.jsonl; do
    [ -f "$f" ] || continue
    prov="$f"
    prov_n=$((prov_n + 1))
  done
  if [ "$prov_n" -gt 1 ]; then
    echo "error: $prov_n *.intoto.jsonl files present — ambiguous provenance. Pass --provenance FILE to name the one for THIS release; refusing to guess." >&2
    exit 1
  fi
fi
prov_checked=0
if [ "$STRICT" -eq 1 ] && [ -z "$prov" ]; then
  strict_fail "no *.intoto.jsonl provenance is present. RUN_SLSA is a profile constant this repository's preflight refuses to disable, so an absent provenance means the release did not follow the reviewed recipe."
fi
if [ -z "$prov" ]; then
  echo "    (skipped: no *.intoto.jsonl provenance present)"
elif ! command -v slsa-verifier >/dev/null 2>&1; then
  # Consumer mode skips and says so. Strict mode fails: the signature over the provenance is
  # the claim being made, and no other check in this script establishes it.
  if [ "$STRICT" -eq 1 ]; then
    strict_fail "slsa-verifier is not installed, so the SLSA provenance signature cannot be verified.
  Install it on this runner before publishing: https://github.com/slsa-framework/slsa-verifier
  A present envelope with matching subject digests is not an authenticated provenance."
  fi
  echo "    (skipped: slsa-verifier not installed — https://github.com/slsa-framework/slsa-verifier)"
else
  slsa_args="--source-uri $SOURCE_URI"
  [ -n "$SOURCE_TAG" ] && slsa_args="$slsa_args --source-tag $SOURCE_TAG"
  for a in $archives; do
    # The exit code is captured so strict mode can name the archive that was rejected.
    # Consumer mode keeps its previous behaviour: it exits with the verifier's own code.
    slsa_rc=0
    slsa-verifier verify-artifact "$a" --provenance-path "$prov" $slsa_args >/dev/null || slsa_rc=$?
    if [ "$slsa_rc" -ne 0 ]; then
      [ "$STRICT" -eq 0 ] || strict_fail "slsa-verifier rejected $a against $prov (exit $slsa_rc)."
      exit "$slsa_rc"
    fi
    echo "    SLSA provenance OK: $a"
    prov_checked=$((prov_checked + 1))
  done
  [ "$prov_checked" -gt 0 ] || echo "    (provenance present but no archives matched)"
  # Reachable only with an empty archive list, which strict mode already refuses earlier.
  # Kept as a second fence so a change to the archive selection cannot pass zero checks.
  if [ "$STRICT" -eq 1 ] && [ "$prov_checked" -eq 0 ]; then
    strict_fail "slsa-verifier is installed and verified zero archives against $prov."
  fi
fi

# The summary claims ONLY what step 5 counted: `prov_checked` verifications. The old
# condition (provenance file present + slsa-verifier installed) claimed "+ SLSA
# provenance" even when zero archives were verified against it — a verifier asserting
# success it did not check, the exact shape scripts/check-verifier-truth.sh exists for.
if [ "$STRICT" -eq 1 ]; then
  # Every count here was verified. Strict mode cannot reach this line with a skipped check,
  # so the summary states results and carries no caveat.
  printf 'strict-publication: %s signed member(s), all present; %s SBOM and %s VEX attestation(s); provenance verified for %s archive(s)\n' \
    "$listed" "$sbom_checked" "$vex_checked" "$prov_checked"
fi
echo "✅ release verified: signature + $present checksum(s)$([ "$sbom_checked" -gt 0 ] && echo " + $sbom_checked SBOM attestation(s)")$([ "$vex_checked" -gt 0 ] && echo " + $vex_checked VEX attestation(s)")$([ "$prov_checked" -gt 0 ] && echo " + SLSA provenance ($prov_checked archive(s))")"
