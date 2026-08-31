<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# PSIRT runbook — security response, end to end

**Status:** operational pipeline (the tools run and are tested; a real CVE drill uses
a fixture advisory, never a fabricated public CVE). This runbook is the *procedure*;
`SECURITY.md` is the *policy* (reporting channel, remediation targets, disclosure). It extends the CVD
playbook of `docs/CRA-READINESS.md` — it does not replace it.

> **Thesis.** A published remediation target (`SECURITY.md`) is only credible if the machinery to
> hit it exists: an out-of-band security release, an advisory the *product itself*
> reads to tell an operator they are affected, and a way to push a security rule
> WITHOUT waiting for the next binary. This runbook is that machinery. The binary is
> updated with a handover, **not** live-patched (`docs/UPGRADE-AND-ROLLBACK.md`);
> what IS hot is data and rules.

---

## 0. Roles and the clock

The remediation clock and severity→target table live in `SECURITY.md`
(§*Vulnerability remediation targets*): Critical **7 days**, High **14 days**, Medium
**30 days**, Low next release, from the moment a fix (or workaround) exists for a
*reachable* vulnerability. Actively-exploited (CISA KEV / credible in-the-wild) is
treated as Critical and **ships out of band**. This runbook is how we meet those.

---

## 1. Triage → fix (private)

1. Intake per `SECURITY.md` (`security@` / GitHub private advisory). Draft a **GHSA**
   privately.
2. Confirm **reachability** with `govulncheck` (a dependency CVE not on the call path
   is `not_affected` in OpenVEX — no clock; the reachable ones start the clock).
3. Fix on a **backport branch** `security/<id>` cut from the affected release line — the
   tag that version shipped on — not from `main`. Keep the diff minimal and reviewed.
   There is no second `lts/*` branch to cut from as well: no LTS line is produced
   (`UPGRADE-AND-ROLLBACK.md` §8). This step used to name one, which sent the responder
   hunting for a branch that does not exist in the middle of an embargo.

## 2. Out-of-band security release

Cut a patch release `vYY.M.PATCH+1` from the backport branch, then build and sign the
OTA manifest **exactly as the `stable` channel does** — the `security` channel is the
HIGHEST-risk OTA route, not a shortcut. It ships the fix for an exploited CVE, and it
is the channel the opt-in unattended timer targets (`olivares upgrade --install-timer
--channel security`, `docs/UPGRADE-AND-ROLLBACK.md`), so a manifest substituted on this
channel installs an attacker's binary on the fleet with no human in the loop.

> **NEVER sign a security manifest blind, and never issue one without `--expires-in`.**
> Two rules, for the same reasons:
> the unsigned manifest sits on a draft release where anyone with `contents: write`
> can overwrite it, and a manifest with no freshness bound can be replayed by a hostile
> mirror forever — which on THIS channel means pinning the fleet to the vulnerable
> version the release exists to fix.

```sh
# 1. Generate the manifest. --expires-in is carried by default (2160h); state it
#    explicitly here so the value is on the record for the incident.
olivares release manifest --channel security --version 26.8.1 --dir ./dist \
  --security --advisory GHSA-xxxx-yyyy-zzzz --min-version 26.5.0 \
  --expires-in 2160h --out ./dist/security-manifest.json
#    (no --sign-key: the OTA private key stays off-box; the ceremony below signs it)

# 2. Authenticate checksums.txt — the CI-signed link an attacker cannot forge.
cosign verify-blob \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp '^https://github\.com/<public-owner>/<public-repository>/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# 3. Cross-check the manifest against it AND read the policy block it prints.
olivares release verify-manifest \
  --manifest ./dist/security-manifest.json --checksums ./dist/checksums.txt --dir ./dist \
  --expect-channel security --expect-version 26.8.1

# 4. ONLY once step 3 prints `OK:` and the policy block is what you intended:
# --checksums is REQUIRED here too: sign-manifest re-runs the cross-check and
# re-prints the policy, so even a custodian who skipped step 3 under incident
# pressure cannot sign a substituted manifest blind.
olivares release sign-manifest --manifest ./dist/security-manifest.json \
  --checksums ./dist/checksums.txt --sign-key @/run/keys/ota.key
```

**If step 3 does not print `OK:`, DO NOT SIGN.** Treat it as a suspected substitution
of the draft asset: keep the artifacts, do not publish, and investigate who wrote to
the release. Under an active incident the pressure to skip this step is at its highest
and the cost of skipping it is at its worst.

**Read the policy block before signing** (step 3 prints every field the signature will
cover). On a security release these three are the ones an attacker moves while leaving
every digest honest, and `verify-manifest` refuses them by default:

| Field | Hostile value | What it does to an incident |
|---|---|---|
| `rollout.percentage` | `0` (or a future `start_at`) | the unattended timer installs the fix for **nobody** |
| `min_version` | above the release | **no** deployment may take the fix, ever |
| `expires` | far future | the vulnerable state can be frozen in place indefinitely |

A deliberate staged rollout of a security fix is still possible — it just has to be
stated: `--allow-paused-rollout` (and `--max-expires-in` for a longer window). Both are
on the record in the incident log, which is the point.

The `security` channel + `--security` flag mark it so clients on auto-update pick it
up ahead of a normal release; `--advisory` records the id(s) fixed and is now
MANDATORY on this channel (a security manifest naming no advisory is refused — it is
how a substituted manifest hides what it claims to fix). Publish the signed artifacts
(cosign + SBOM + OpenVEX + SLSA Build L3 (SLSA v1.2) provenance, as every release).

## 3. Publish the advisory feed (machine-readable)

Draft the advisory in OSV shape and sign the feed with the dedicated **OTA key** (the
signature is domain-separated from the update manifest and the license, so one key is
safe — `core/secadvisory`):

```jsonc
// draft-advisories.json
{ "advisories": [ {
  "id": "GHSA-xxxx-yyyy-zzzz",
  "summary": "…", "severity": "HIGH",
  "affected": [ { "package": "olivares",
                  "ranges": [ { "introduced": "26.5.0", "fixed": "26.8.1" } ] } ],
  "references": [ { "type": "ADVISORY", "url": "https://github.com/…/security/advisories/GHSA-…" } ]
} ] }
```

```sh
olivares security advisories --in draft-advisories.json --out advisories.json \
  --sign-key @/run/keys/release.key      # writes advisories.json + advisories.json.sig
```

Publish `advisories.json{,.sig}` on the release channel (and the embargoed enterprise
copy). Also fold it into the **air-gap bundle** (`scripts/export-update-bundle.sh`)
so offline sites get it over the same transport.

### Packaging the backport feed (task / CI)

Package and verify the feed locally before carrying out the manual publication steps
above. The task produces `advisories.json{,.sig}`, checks the signed feed with the
product consumer, proves a one-byte-tampered copy is refused, and writes checksums and
feed metadata. It never publishes:

```sh
task security:feed:package DRAFT=draft-advisories.json \
  SIGN_KEY=/run/keys/release.key PUBKEY=@/run/keys/release.pub
task security:feed:selftest   # ephemeral TEST keys + the drill fixture
```

The dispatch-only `security-feed` workflow runs the same packaging-and-verification
path and uploads the result as an internal workflow artifact in `package` mode;
publishing remains the release-channel, embargoed-enterprise-copy, and air-gap-bundle
steps above.

## 4. The customer finds out — `olivares security check`

The deployment checks itself against the signed feed. The check reads the feed and its
detached signature **from disk**, so it is fully offline — in an air-gap, point
`--feed` at the `advisories.json` carried inside the update/DDIL bundle:

```sh
olivares security check --feed advisories.json          # --sig defaults to advisories.json.sig
olivares security check --feed /media/olivares-update/advisories.json   # air-gap: file from a bundle
olivares security check --feed advisories.json --quiet  # print nothing when unaffected (probes)
olivares security check --feed advisories.json --product-version 26.8.0  # what-if / fleet check
```

- Verifies the feed against the **embedded OTA key** (`--pubkey` to override) BEFORE
  parsing; a tampered or wrong-key feed is **refused** (fail-closed — never "clean").
- Matches the running module+version against the OSV ranges and prints, per affected
  advisory: id, severity, summary, and the fixed release. **Exits non-zero** when
  affected (0 when clean), so a periodic job / the console health surface can act.
- The fix path is the update framework: `olivares upgrade` to a patched, signed release.

Run it on a timer or from the console health check. With no feed on hand there is no
network call — the check is a local, offline verification, air-gap-clean by design.

## 5. Hot security rules — no restart

Some responses cannot wait for a binary: block a malicious MCP server, add an
injection signature, deny-list an IOC. These ship as a **signed rule-pack**
(`connectors/threatfeed`), applied at runtime:

```sh
# author + sign a pack (deny-lists / blocked MCP / attack patterns):
olivares security rulepack sign --in draft-rulepack.json --out rulepack.json \
  --sign-key @/run/keys/release.key
# verify before rollout:
olivares security rulepack verify --in rulepack.json --pubkey <base64-trusted-key>
```

The engine's rule-pack manager **verifies the signature against a pinned key →
validates → refuses an older version (anti-rollback) or an expired pack → compiles
(RE2) → atomically swaps the active pack → audits the change**, keeping the previous
pack for an **instant rollback** if the pack proves bad. Lookups never block on the
swap, so the new rules take effect the moment the apply returns — no restart. The OSS
engine consumes locally-signed packs (no subscription); the enterprise engine adds
its compiled-in base catalog and any signed feed artifact applied on top.

## 6. Close out

- Publish the GHSA (lifts the embargo), request a CVE if warranted (GitHub is a CNA),
  record the fix in `CHANGELOG.md` §Security crediting the reporter.
- The advisory feed already tells every deployment. Confirm the KEV/CRA reporting
  obligations in `SECURITY.md` §*EU CRA reporting* where they apply.

## 7. The drill — prove the pipeline before you need it

Run the whole advisory pipeline on a cadence and in CI, before an incident makes the
first real execution the expensive one:

```sh
task security:drill
# or, with an installed binary:
olivares security drill
```

The drill runs the real CLI producer and consumer end to end in a throwaway directory:
draft → signed feed → offline verification → affected, patched, and below-introduced
boundary checks → tamper refusal → wrong-key refusal. It prints every step's elapsed
time and the measured end-to-end time. `--keep-artifacts` retains the scratch directory
for diagnosis; `--draft` exercises an operator-supplied draft under the same anti-drift
guard.

Its signing key is deterministically derived for repeatability and is **TEST-ONLY** —
never trust it or use it outside the drill. Drill advisories use the fixture namespace
(`OLIVARES-DRILL-…`, currently `OLIVARES-DRILL-0001`), never a real CVE or GHSA id.
A failing drill is an advisory-pipeline incident: stop and repair the producer,
signature, verification, or range-reporting path before declaring PSIRT readiness.

## 8. Honest limits

- **No live binary patching.** A security *binary* fix is an `upgrade` (handover,
  zero-downtime; not a live patch) — the update framework. Only *rules and data* are hot (§5).
- **Reachability, not noise.** Unreachable dependency CVEs are `not_affected`
  (OpenVEX) and do not start the clock; `security check` reports what the signed feed
  says affects your version/SBOM, and is only as complete as that feed.
- **The commercial add-on** ships an agentic-signature/MCP-reputation base catalog
  and can apply signed feed artifacts on top; the OSS channel is operator-signed
  local packs. Both use the same verified, anti-rollback, hot-swap mechanism, and
  no curated distribution is operated today.
- **Drills use a fixture advisory/CVE id**, never a fabricated real CVE in docs.
