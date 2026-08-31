#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cfg05-key-anchors.sh — CFG-05. License and OTA are two trust
# domains. releasePublicKeyB64 is the LICENSE anchor. The OTA private
# ceremony is never automated. Three answers. Does not generate keys.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg05-key-anchors: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg05-key-anchors: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

GR=.goreleaser.yaml
TF=Taskfile.yml
CHK=scripts/check-release-pubkey.sh
SEP=scripts/test-key-domain-separation.sh
LIC=core/license/embedded.go
OTA=core/release/verify.go
DOC=design/CFG-05-KEY-ANCHORS-2026-08-19.md
REL=.github/workflows/release.yml
WF=.github/workflows

[ -r "$GR" ]  || cannot "missing $GR"
[ -r "$TF" ]  || cannot "missing $TF"
[ -r "$CHK" ] || cannot "missing $CHK"
[ -r "$SEP" ] || cannot "missing $SEP"
[ -r "$LIC" ] || cannot "missing $LIC"
[ -r "$OTA" ] || cannot "missing $OTA"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$REL" ] || cannot "missing $REL"
[ -d "$WF" ]  || cannot "missing $WF"

# Historical name, license domain. Crossing it with OTA is the defect.
grep -q 'var releasePublicKeyB64 string' "$LIC" \
  || fail "core/license lost var releasePublicKeyB64"
grep -q 'LICENSE verification key' "$LIC" \
  || fail "releasePublicKeyB64 is no longer documented as the LICENSE anchor"
grep -q 'var artifactVerifyKeyB64 string' "$OTA" \
  || fail "core/release lost var artifactVerifyKeyB64"
grep -q 'OTA release' "$OTA" \
  || fail "artifactVerifyKeyB64 is no longer documented as the OTA anchor"

lic_map='core/license.releasePublicKeyB64={{ .Env.OLIVARES_LICENSE_PUBKEY }}'
ota_map='core/release.artifactVerifyKeyB64={{ .Env.OLIVARES_OTA_PUBKEY }}'
n_lic="$(grep -cF "$lic_map" "$GR" || true)"
n_ota="$(grep -cF "$ota_map" "$GR" || true)"
[ "$n_lic" -ge 2 ] || fail "goreleaser lost LICENSE→releasePublicKeyB64 (want >=2, got $n_lic)"
[ "$n_ota" -ge 2 ] || fail "goreleaser lost OTA→artifactVerifyKeyB64 (want >=2, got $n_ota)"

if grep -Fq 'core/license.releasePublicKeyB64={{ .Env.OLIVARES_OTA_PUBKEY }}' "$GR"; then
  fail "goreleaser injects the OTA pubkey into the LICENSE symbol"
fi
if grep -Fq 'core/release.artifactVerifyKeyB64={{ .Env.OLIVARES_LICENSE_PUBKEY }}' "$GR"; then
  fail "goreleaser injects the LICENSE pubkey into the OTA symbol"
fi

grep -Fq 'core/license.releasePublicKeyB64=${OLIVARES_LICENSE_PUBKEY}' "$TF" \
  || fail "Taskfile build:repro lost LICENSE→releasePublicKeyB64"
grep -Fq 'core/release.artifactVerifyKeyB64=${OLIVARES_OTA_PUBKEY}' "$TF" \
  || fail "Taskfile build:repro lost OTA→artifactVerifyKeyB64"

grep -q 'OLIVARES_LICENSE_PUBKEY and OLIVARES_OTA_PUBKEY are identical' "$CHK" \
  || fail "check-release-pubkey no longer refuses identical anchors"
grep -q 'identical license and OTA anchors' "$SEP" \
  || fail "key-domain selftest no longer kills identical anchors"

grep -Eq 'NO ceremony|no se ejecuta la ceremonia' "$DOC" \
  || fail "prepare doc no longer says the ceremony is not run"

# Production release must consume published PUBLIC anchors, not mint them.
if grep -q 'license keygen' "$REL"; then
  fail "release.yml runs license keygen — production anchors are not minted in CI"
fi

# OTA signing: PERMITTED, under the mitigation. SUPERSEDED 2026-08-29 by the owner's
# orden 37 («no iba a estar firmando manualmente cada release … hay que
# automatizarlo»). CFG-05 and CFG-15 said the OTA private half was never a CI
# secret and the ceremony was never automated; that was one session's reading of
# the standard, and is the opposite. See go-live §0-bis (F).
#
# So this stopped being «is it forbidden?» and became «is the mitigation in
# place?». What the mitigation is: the private half lives ONLY as a secret of a
# protected ENVIRONMENT (required reviewer), never a repository secret, so it is
# unreadable until a human approves that run; the job verifies the signature
# against the tree anchor before attaching.
#
# ⚠ WHAT THIS CHECK DOES NOT PROVE. It is grep over YAML, so it reasons at FILE
# level, not job level: it proves release.yml declares an environment somewhere,
# not that the SIGNING job is the one that declares it, and it cannot see whether
# the environment carries required reviewers — that lives in repository settings.
# check-release-anchor-identity and the branch-protection gate cover parts of it.
# Saying so is the point: a gate that implies more than it measures is worse than
# none, because the next reader trusts a guarantee nobody made.
LICENSE_PRIV_RE='OLIVARES_LICENSE_PRIV(ATE)?(_KEY)?'
OTA_PRIV_RE='OLIVARES_OTA_PRIV(ATE)?(_KEY)?'

# The LICENSE private half keeps the absolute ban: orden 37 was about releases,
# and nothing about it touches licence issuance, which signs in its own Worker.
if grep -R --include='*.yml' --include='*.yaml' -nE "$LICENSE_PRIV_RE" "$WF" >/dev/null; then
  fail "a workflow names the LICENSE private key — licences are signed in the Worker, never in CI"
fi

# Signing the manifest is allowed only in release.yml, and only in a file that
# declares an environment. Any OTHER workflow doing it is the old violation.
others="$(grep -Rl --include='*.yml' --include='*.yaml' -E 'release[[:space:]]+sign-manifest' "$WF" 2>/dev/null \
  | grep -v '^'"$WF"'/release\.yml$' || true)"
if [ -n "$others" ]; then
  fail "a workflow other than release.yml runs sign-manifest: $(printf '%s' "$others" | tr '\n' ' ')"
fi
if grep -Eq 'release[[:space:]]+sign-manifest' "$REL"; then
  grep -Eq '^[[:space:]]*environment:' "$REL" \
    || fail "release.yml signs the manifest but declares no environment — the mitigation for orden 37 is the protected environment, and without it the private half is readable by any run"
fi

# The OTA private half may be named ONLY in release.yml and ONLY as a secrets.*
# reference. A literal, or a var.*, or any other workflow, is still a violation.
otafiles="$(grep -Rl --include='*.yml' --include='*.yaml' -E "$OTA_PRIV_RE" "$WF" 2>/dev/null \
  | grep -v '^'"$WF"'/release\.yml$' || true)"
if [ -n "$otafiles" ]; then
  fail "a workflow other than release.yml names the OTA private key: $(printf '%s' "$otafiles" | tr '\n' ' ')"
fi
# What matters is where the VALUE comes from, not where the NAME appears. A
# diagnostic that names the missing secret so an operator can fix it is not a
# leak — release.yml:2105 does exactly that, and an earlier draft of this check
# failed it. So the rule is stated over the dangerous FORMS:
#
#   a) a repository VARIABLE holding the private half (vars.*) — readable by any
#      run and editable in settings without review: that is the thing the
#      environment secret replaces;
#   b) an assignment of the key to anything that is not ${{ secrets.… }} —
#      a literal, an input, an event payload.
if grep -REq 'vars\.'"$OTA_PRIV_RE" "$WF"; then
  fail "the OTA private key is read from a repository VARIABLE — the mitigation is an environment SECRET, which a variable is not"
fi
bad_ota="$(grep -nE '^[[:space:]]*[A-Z_]*OTA_PRIV(ATE)?(_KEY)?[A-Z_]*:' "$REL" \
  | grep -vE ':[[:space:]]*\$\{\{[[:space:]]*secrets\.' || true)"
if [ -n "$bad_ota" ]; then
  fail "release.yml assigns the OTA private key from something that is not a secret: $(printf '%s' "$bad_ota" | head -3 | tr '\n' ' ')"
fi
if [ -e "$WF/ota-channel-publish.yml" ]; then
  if grep -q 'license keygen' "$WF/ota-channel-publish.yml"; then
    fail "ota-channel-publish.yml mints keys — it may only consume an off-box .sig"
  fi
fi

say "check-cfg05-key-anchors: CLEAN — LICENSE→releasePublicKeyB64, OTA→artifactVerifyKeyB64, identical refused, licence private absent from CI, OTA signing behind a declared environment."
exit 0
