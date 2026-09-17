#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cosign-contract.sh — prove the installed cosign speaks the signing contract
# this repository's release pipeline is configured for, WITHOUT publishing anything.
#
# WHY THIS EXISTS. On 2026-07-25 the release pipeline was one tag away from a silent
# failure. `.github/workflows/release.yml` installed cosign through an action revision
# whose DEFAULT is v3.0.6, while `.goreleaser.yaml` signs checksums with the split
# `--output-signature` / `--output-certificate` contract and the OTA job installed a
# different revision defaulting to v2.5.2 — two generations in one pipeline. Measured
# against both binaries:
#
#   * v3 does not reject the flags. It deprecates them and IGNORES them whenever
#     --new-bundle-format is on, and that defaults to TRUE in v3 — so the .sig/.pem
#     files GoReleaser is configured to collect are never written. GoReleaser then
#     fails when it tries to upload artifacts that do not exist: LOUD, but only after
#     earlier pipeline stages have already pushed images and created the draft, i.e. a
#     half-published release that has to be cleaned up by hand. This check moves that
#     failure to before the first mutation.
#   * v3 rejects `--tlog-upload=false` outright ("not supported with --signing-config"),
#     which is precisely the flag the release rehearsal relies on to sign without
#     creating an immutable public transparency-log record.
#   * v2.6.4 performs both: a signature file, and `verify-blob --key` → "Verified OK".
#
# CORRECTION (2026-07-25) — READ BEFORE MIGRATING. The bullet above is true, but an
# earlier revision of this header and of sessions-rehearsal-mode.md drew a
# FALSE conclusion from it: that no safe rehearsal is possible under v3. v3 changes the
# MECHANISM, it does not remove it, and its own error message names the replacement — a
# signing configuration carrying no transparency-log service:
#
#     cosign signing-config create --no-default-rekor --no-default-tsa \
#       --no-default-fulcio --no-default-oidc --out sc.json
#     cosign sign-blob --key K --signing-config sc.json --bundle out.json --yes FILE
#
# Measured on v3.0.6: no "tlog entry created" line, a bundle whose `tlogEntries` is empty,
# and offline `verify-blob --bundle --new-bundle-format --trusted-root <minimal>
# --insecure-ignore-tlog` → "Verified OK". Three consequences for whoever migrates this
# fixture to the bundle contract:
#   1. `--rekor-url`/`--fulcio-url` CANNOT be combined with a signing config ("cannot
#      specify service URLs and use signing config"), so the loopback containment below
#      cannot simply be kept alongside it.
#   2. That is a PROBLEM, not a detail, and it must not be waved away. The signing
#      configuration is read by the same binary whose behaviour is under test, so
#      "a config with no transparency-log service" is the SAME circularity the
#      CONTAINMENT note below rejects — relocated, not removed. A cosign that ignored
#      `--tlog-upload=false` could equally ignore the configuration. When this fixture
#      moves to v3 it therefore needs an INDEPENDENT barrier that does not depend on
#      cosign honouring anything: deny egress to Rekor/Fulcio for the process (network
#      namespace, firewall rule, or a proxy that refuses those hosts). Do not migrate
#      this file by deleting the loopback flags and calling the config the containment.
#   3. Under v3 the transparency log is ON BY DEFAULT and `--use-signing-config=false`
#      does NOT disable it — it only stops the service URLs being fetched from TUF.
#      Measured: with that flag, both `--bundle` and `--output-signature` uploaded to the
#      real public Rekor. Anyone probing v3 by hand must use the config AND an egress
#      barrier, or they will create permanent public records. (That is exactly how
#      created two.)
#
# So this check runs the REAL commands and asserts the outputs exist and verify,
# BEFORE anything is built, pushed or published.
#
# CONTAINMENT — READ THIS BEFORE EDITING. The thing under test is whether cosign
# HONOURS `--tlog-upload=false`. That flag therefore cannot also be the only barrier
# preventing a public record: a binary that silently ignored it would upload to the
# real transparency log, under `--yes`, before any assertion below could fail. So the
# fixture additionally points every Sigstore endpoint at an unroutable loopback port
# and keeps the TUF cache inside the throwaway directory. If a rogue binary tries to
# reach Rekor or Fulcio anyway, the connection fails instead of publishing. Note that
# `verify-blob` fetches TUF trusted-root material even with `--insecure-ignore-tlog`,
# so this is not a "no network calls" script — it is a script whose network calls
# cannot reach Sigstore.
set -euo pipefail

# Fail CLOSED. This runs in the release job right before artifacts are signed; a
# missing binary there means the pin or the installer step is broken, and skipping
# would hand back a green check for a pipeline that is about to publish nothing
# verifiable.
# WHICH cosign is under test. In the release workflow the answer must be the binary
# scripts/assert-cosign-binary.sh authenticated against the upstream published digests
# earlier in the job — otherwise this fixture proves the contract of "whatever PATH resolves
# right now", which is not the thing that will sign. Locally, where no assertion has run,
# it falls back to PATH so `task cosign:contract` and the deliberate
# "test a future version" maintenance operation still work.
# It goes through scripts/cosign-verified.sh, NOT straight at the pathname: that launcher
# re-authenticates the bytes immediately before each invocation. Executing the path
# directly would have made this fixture the one place in the release that trusts a name
# hashed minutes earlier — the very gap the launcher exists to close.
# ⛔ TRES RESPUESTAS, NO DOS (C15-P6, 2026-08-18). Este fichero salía **1** cuando cosign no
# estaba, con el argumento —correcto como política— de que el pipeline de firma lo necesita y por
# tanto no es un «skip». El argumento es bueno y la CODIFICACIÓN era mala: un 1 dice «el contrato
# de firma está ROTO», y lo que ocurre es que **no se ha podido probar**. Son dos hechos distintos
# y el operador que lee el log no puede separarlos.
#
# El coste no es teórico en este repo: un gate que contesta «roto» a una ausencia enseña a
# ignorarlo, y un gate que se ignora deja de ser un control. La política no cambia — 2 sigue siendo
# NO CERO y sigue tumbando el job de release igual que el 1 —; lo que cambia es que el código dice
# CUÁL de las dos cosas pasó, y quien llame decide la severidad con el dato delante.
#
#   0 = el contrato se probó y se cumple
#   1 = el contrato se probó y NO se cumple (versión divergente, cosign inutilizable, aserción rota)
#   2 = NO SE HA PODIDO MIRAR (no hay binario que probar)
RC_CLEAN=0
RC_BROKEN=1
RC_BLIND=2

CONTRACT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
COSIGN=""
if [ -n "${OLIVARES_COSIGN_BIN:-}" ]; then
	[ -x "${OLIVARES_COSIGN_BIN}" ] || {
		echo "::error::cosign-contract: UNVERIFIED — OLIVARES_COSIGN_BIN=${OLIVARES_COSIGN_BIN} is not executable, so there is no binary to test. This is NOT a passing contract; in a release job it must still be fatal." >&2
		exit "$RC_BLIND"
	}
	COSIGN="bash ${CONTRACT_DIR}/cosign-verified.sh"
	echo "cosign-contract: testing the VERIFIED binary ${OLIVARES_COSIGN_BIN} through cosign-verified.sh" >&2
elif command -v cosign >/dev/null 2>&1; then
	COSIGN=cosign
	echo "cosign-contract: OLIVARES_COSIGN_BIN is unset — testing whatever PATH resolves. In a" >&2
	echo "cosign-contract: release job that is a defect: assert-cosign-binary.sh must run first." >&2
else
	echo "::error::cosign-contract: UNVERIFIED — cosign is not on PATH, so the signing contract was NOT tested. The release pipeline signs with it, so this is still a hard failure and never a pass: it exits ${RC_BLIND} (could not look), not 0." >&2
	exit "$RC_BLIND"
fi

# NEVER `version="$(cosign version 2>/dev/null | awk ...)"`. Under `set -euo pipefail` a
# failing cosign aborts the whole script AT THE ASSIGNMENT, printing nothing at all —
# discovered 2026-07-25 when a `cosign` that resolves but cannot run (a containment shim
# with no binary behind it) made this fixture die with a bare exit 127 and no diagnostic.
# A supply-chain check whose failure mode is silence is worse than no check: capture the
# output, keep the status, and SHOW the operator what the binary actually said.
if ! version_raw="$($COSIGN version 2>&1)"; then
	echo "::error::cosign-contract: 'cosign version' failed — the cosign on PATH is not usable." >&2
	printf '%s\n' "$version_raw" | sed 's/^/  /' >&2
	exit 1
fi
version="$(printf '%s\n' "$version_raw" | awk '/GitVersion/ {print $2}')"
echo "cosign-contract: testing ${version:-unknown}"

# The contract is proven for ONE version; testing a different binary and reporting OK
# would make this fixture a rubber stamp. Keep it in step with scripts/check-cosign-pins.sh.
APPROVED_COSIGN="v2.6.4"
if [ "${version:-}" != "$APPROVED_COSIGN" ] && [ "${COSIGN_CONTRACT_ANY_VERSION:-0}" != "1" ]; then
	echo "::error::cosign-contract: installed cosign is ${version:-unknown}, approved is $APPROVED_COSIGN." >&2
	echo "The release workflow pins the approved version at every installer site; if this fires," >&2
	echo "the pin and the installed binary have diverged. To test another binary on purpose:" >&2
	echo "  COSIGN_CONTRACT_ANY_VERSION=1 scripts/check-cosign-contract.sh" >&2
	exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"

# Containment (see the note above). Port 1 on loopback refuses immediately.
DEAD_ENDPOINT="http://127.0.0.1:1"
export TUF_ROOT="$work/tuf"
export TUF_MIRROR="$DEAD_ENDPOINT"
mkdir -p "$TUF_ROOT"

# A stand-in for the checksums file GoReleaser signs.
printf 'deadbeef  olivares_linux_amd64.tar.gz\n' >checksums.txt

export COSIGN_PASSWORD="contract-check-$$"
$COSIGN generate-key-pair >/dev/null 2>&1

fail() {
	echo "::error::cosign-contract: $1" >&2
	echo >&2
	echo "The pinned cosign (${version:-unknown}) cannot honour this repository's signing" >&2
	echo "contract. Do NOT bump the pin without migrating ALL of: .goreleaser.yaml signs/" >&2
	echo "docker_signs, the archive SBOM + VEX attestations and image attestations in" >&2
	echo ".github/workflows/release.yml, the OTA checksum verification, and" >&2
	echo "scripts/verify-release.sh — plus this check." >&2
	exit 1
}

# (1) KEY-BASED SIGNING WITH NO TRANSPARENCY LOG.
# This is the rehearsal's whole safety property: sign real artifacts while creating
# no public, permanent record. If this breaks, a rehearsal cannot be run safely.
# --tlog-upload=false FIRST, immediately after the subcommand. That is not cosmetic: it is
# the only position in which the flag cannot be swallowed as another option's value and
# cannot be overridden by a later occurrence, which is exactly what scripts/cosign-guard.sh
# now requires before it will permit a publishing verb. Writing containment unambiguously is
# cheaper than reasoning about pflag precedence at every call site.
if ! $COSIGN sign-blob --tlog-upload=false \
	--key cosign.key \
	--output-signature=checksums.txt.sig \
	--rekor-url="$DEAD_ENDPOINT" --fulcio-url="$DEAD_ENDPOINT" \
	checksums.txt --yes >/dev/null 2>&1; then
	fail "key-based sign-blob with --tlog-upload=false failed (the rehearsal signing path)"
fi
[ -s checksums.txt.sig ] ||
	fail "sign-blob reported success but wrote no signature — the flags were accepted and ignored"

# (2) VERIFICATION AGAINST THE PUBLIC KEY, tlog ignored.
if ! $COSIGN verify-blob --key cosign.pub \
	--signature checksums.txt.sig \
	--insecure-ignore-tlog \
	--rekor-url="$DEAD_ENDPOINT" \
	checksums.txt >/dev/null 2>&1; then
	fail "verify-blob could not verify what sign-blob just produced"
fi

# NOTE ON WHAT IS *NOT* CHECKED HERE. Assertion (1) already covers the split-output
# contract behaviourally: if a cosign accepts `--output-signature` and ignores it (as
# v3 does under the new bundle format), the file is absent and (1) fails. An earlier
# revision of this script also grepped `--help` for flag names and default renderings;
# that was dropped as brittle — help text is not a stable interface, and the file check
# is the real test. Keyless signing (the PRODUCTION path, which also produces the
# `.pem` certificate) cannot be exercised here: it needs an OIDC token from a CI
# identity. That path is covered by the release run itself, not by this fixture.

# (3) ATTESTATION IN THE EXACT SHAPE THE PIPELINE USES.
# release.yml attests each archive SBOM with
#   cosign attest-blob --type spdxjson --predicate <sbom> --new-bundle-format \
#     --bundle <out> --yes <archive>
# (see the "attest SBOMs to the binary archives" and OpenVEX steps). This runs the same
# command with a key and the tlog disabled, and asserts the bundle is written and is a
# real DSSE envelope — a cosign that changed this contract would otherwise ship a
# release whose SBOM/VEX attestations are empty or malformed.
printf 'archive-bytes\n' >archive.tar.gz
printf '{"spdxVersion":"SPDX-2.3"}\n' >sbom.json
if ! $COSIGN attest-blob --tlog-upload=false \
	--type spdxjson --predicate sbom.json \
	--new-bundle-format --bundle archive.sbom.sigstore.json \
	--key cosign.key \
	--rekor-url="$DEAD_ENDPOINT" --fulcio-url="$DEAD_ENDPOINT" \
	--yes archive.tar.gz >/dev/null 2>&1; then
	fail "attest-blob in the pipeline's own shape (--new-bundle-format --bundle) failed"
fi
[ -s archive.sbom.sigstore.json ] || fail "attest-blob wrote no bundle"

# CRYPTOGRAPHICALLY verify the bundle, offline. A new-format bundle needs a Sigstore
# TrustedRoot, but for a KEY-signed attestation the trust material is the public key we
# already hold, so a minimal on-disk root satisfies the requirement while the tlog and
# certificate paths stay disabled. This is a real signature check: it fails if the
# bundle's payload or signature is absent, empty or does not match the key. (An earlier
# revision only grepped for a payloadType string, which passes on a document that
# contains the text and no usable signature, and fails on pretty-printed JSON.)
cat >trusted-root.json <<'JSON'
{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1","tlogs":[],"certificateAuthorities":[],"ctlogs":[],"timestampAuthorities":[]}
JSON
if ! $COSIGN verify-blob-attestation --key cosign.pub \
	--bundle archive.sbom.sigstore.json --new-bundle-format \
	--trusted-root trusted-root.json --type spdxjson \
	--insecure-ignore-tlog --check-claims=false \
	--rekor-url="$DEAD_ENDPOINT" \
	archive.tar.gz >/dev/null 2>&1; then
	fail "verify-blob-attestation could not verify the bundle attest-blob just produced"
fi

echo "cosign-contract: OK (${version:-unknown}) — key sign-blob/verify-blob and attest-blob/verify-blob-attestation both round-trip with the transparency log disabled"
