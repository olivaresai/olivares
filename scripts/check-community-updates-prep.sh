#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# CFG-06 — the community update channel is PREPARED against the signed carrier, and NOT
# published from this tree.
#
# ⛔ WHAT THIS GATE USED TO CHECK, AND WHY THAT HAD TO CHANGE (2026-08-27).
# It froze one literal: `defaultCommunityEndpoint  = "https://olivares.ai/updates"`, failing
# "if the endpoint moves". That was the right shape for the FIRMA B of 2026-08-15, where the
# manifest was to be served from the web repo and the client was to change by zero lines.
# signed a DIFFERENT carrier on 2026-08-21 (an internal design note (not shipped):
# 314-326): the GitHub Releases of the public repository. Under that signature the frozen
# literal was not protecting anything — it was PINNING THE CLIENT TO A PATH WITH NO PRODUCER
# AND NO SERVER (measured 2026-08-20 and again 2026-08-27: 404 both times, and no workflow in
# .github/ writes anything under it), and it would have gone red on the very change that
# carried the signature out. A gate that must be deleted to obey an order is a gate pointing
# at the wrong fact.
#
# WHAT IT CHECKS NOW, and every line of it is a fact that can drift apart on its own:
#
#   1. THE CARRIER. The client's community default names the public repository, so a silent
#      revert to a host nobody serves is a finding.
#   2. THE ASSET-NAME COUPLING, which is the one this gate exists for. The client asks for
#      `<channel>-manifest.json` under a release; the PRODUCER (release.yml) uploads
#      `stable-manifest.json` and the phase-2 ceremony
#      (scripts/release-attach-stable-pair.sh) attaches `stable-manifest.json.sig`. Those are
#      two files, edited by different people for different reasons, and nothing else compares
#      them. If they drift, the channel 404s and NOBODY finds out until a user runs
#      `olivares upgrade` — which is exactly the failure this whole lot exists to prevent.
#   3. NOT PUBLISHED FROM THIS TREE. The prepare doc must still say so; publication is
#      act under TWO-PHASE.
#
# Hub-safe by construction: the live GET stays opt-in (OLIVARES_CFG06_LIVE=1) so an offline
# runner cannot turn a network miss into a red for lint:addon-sets.
#
# Three answers: 0 CLEAN · 1 FINDING · 2 COULD NOT LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-community-updates-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-community-updates-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

UPG="cmd/olivares/cmd_upgrade.go"
# The client half of the coupling lives where the LAYOUT is resolved, and that moved on
# 2026-08-27: it used to be spelled out in cmd/olivares, and it is now resolved once, in
# core/release, for BOTH readers of a public channel (`olivares upgrade` and the console's
# update indicator). This gate followed it there rather than keeping a second opinion about
# where the client looks — which is the same duplication the move existed to remove.
SRC="core/release/channelurl.go"
PRODUCER=".github/workflows/release.yml"
CEREMONY="scripts/release-attach-stable-pair.sh"
for f in "$UPG" "$SRC"; do
  [ -r "$f" ] || cannot "missing $f"
done

# 1 · THE CARRIER. Read as a whole line so a comment mentioning the old URL cannot satisfy
# or break it — the constant is what ships, prose is not.
CARRIER="https://github.com/olivaresai/olivares"
grep -qE "^\s*defaultCommunityEndpoint  = \"${CARRIER}\"$" "$UPG" ||
  fail "the client's community default is no longer ${CARRIER}. FIRMA B (2026-08-21) puts
  the community channel on the public repository's GitHub Releases; the previous default
  (https://olivares.ai/updates) answered 404 with no producer and no server."

# 2 · THE ASSET-NAME COUPLING.
#
# ⛔ ANCHORED, NOT SUBSTRING. Every expectation below is matched as a WHOLE LINE. A plain
# `grep -F` is satisfied by a COMMENTED-OUT copy of the line it looks for, so a client or a
# producer could be genuinely mutated while the gate stayed green on the corpse of the old
# line sitting in a comment above it. The contrast raised it; it costs an anchor.
#
# ⛔ THE TWO WAYS OF NOT AGREEING ARE NOT THE SAME ANSWER, and the first cut of this block
# collapsed them. It extracted the suffix with a sed and called an empty extraction "COULD
# NOT LOOK" — so RENAMING the client's asset, the exact defect this block exists to catch,
# reported a blind spot instead of a finding, and the battery caught it. The file being
# ABSENT is "I could not look"; the file being PRESENT and not saying what it must is a
# FINDING. So the function is located first (absent -> 2) and then its whole line is
# matched exactly (present and different -> 1).
grep -qE '^func \(l ChannelLayout\) ManifestURL\(\) string' "$SRC" ||
  cannot "$SRC has no ChannelLayout.ManifestURL: the client half of the coupling cannot be read"
grep -qE '^\s*return l\.assetURL\(l\.channel \+ "-manifest\.json"\)$' "$SRC" ||
  fail "the client no longer asks for '<channel>-manifest.json' under a release. The producer
  uploads stable-manifest.json / security-manifest.json, so any other name 404s for every
  community binary. See $SRC (ChannelLayout.ManifestURL)."

if [ -r "$PRODUCER" ]; then
  grep -qE '^\s*gh release upload "\$\{RELEASE_TAG\}" dist/stable-manifest\.json$' "$PRODUCER" ||
    fail "$PRODUCER no longer uploads dist/stable-manifest.json, so the client's
  '<repo>/releases/<...>/stable-manifest.json' would 404 on every community binary"
  grep -qE '^\s*gh release upload "\$\{RELEASE_TAG\}" dist/security-manifest\.json$' "$PRODUCER" ||
    fail "$PRODUCER no longer uploads dist/security-manifest.json; the security channel the
  client can ask for would have no asset behind it"
else
  cannot "missing $PRODUCER — the producer half of the asset-name coupling cannot be read"
fi

if [ -r "$CEREMONY" ]; then
  grep -qE '^\s*ota-dist/stable-manifest\.json ota-dist/stable-manifest\.json\.sig[^#]*$' "$CEREMONY" ||
    fail "$CEREMONY no longer attaches stable-manifest.json.sig beside the manifest; the client
  fetches '<manifest-url>.sig' and would refuse every upgrade with a missing signature"
else
  cannot "missing $CEREMONY — the signature half of the asset-name coupling cannot be read"
fi

# 3 · NOT PUBLISHED FROM THIS TREE.
DOC="${OLIVARES_CFG06_DOC:-design/CFG-06-COMMUNITY-CHANNEL-PREP-2026-08-20.md}"
[ -r "$DOC" ] || cannot "missing prepare doc $DOC"
grep -q 'NO publicado' "$DOC" ||
  fail "prepare doc no longer says it does not publish"

# 4 · The live channel, opt-in. Under the new carrier the honest state is still a 404 (the
# public repository carries no release yet), and a 200 pair is the state after act —
# both are CLEAN. What is NOT clean is a split pair: a manifest without its signature is a
# channel every conforming client refuses.
if [ "${OLIVARES_CFG06_LIVE:-}" = "1" ]; then
  # ⛔ NO `|| echo "000"`, AND THAT IS THE WHOLE POINT OF THIS FUNCTION. On a transport failure
  # curl ALREADY prints `000` and exits non-zero, so the fallback appended a second one and the
  # substitution produced `000000` — which the case below does not recognise, so "I could not
  # look" fell through to "FINDING". The external contrast measured it. The status is normalised
  # to exactly three digits here, and anything else IS the could-not-look answer.
  probe() {
    local url="$1" out
    out="$(curl -sSL -o /dev/null -w '%{http_code}' --max-time 25 -A 'olivares-cfg06-prep' "$url" 2>/dev/null)" || true
    case "$out" in
    [0-9][0-9][0-9]) printf '%s' "$out" ;;
    *) printf '000' ;;
    esac
  }
  MAN="${CARRIER}/releases/latest/download/stable-manifest.json"
  code=$(probe "$MAN")
  sig=$(probe "${MAN}.sig")
  case "$code" in
  000) cannot "could not GET ${MAN}" ;;
  404)
    [ "$sig" = "404" ] || fail "manifest 404 but .sig is HTTP $sig — pair is split"
    say "check-community-updates-prep: CLEAN — carrier wired to ${CARRIER}; no release published yet (404)."
    exit 0
    ;;
  200)
    [ "$sig" = "200" ] || fail "live manifest 200 without .sig 200 (unsigned channel)"
    say "check-community-updates-prep: CLEAN — live signed pair exists on the release carrier."
    exit 0
    ;;
  *) fail "unexpected HTTP $code for ${MAN}" ;;
  esac
fi

say "check-community-updates-prep: CLEAN — carrier ${CARRIER}; client and producer agree on"
say "  '<channel>-manifest.json[.sig]'; NO publicado; live GET not in this gate."
exit 0
