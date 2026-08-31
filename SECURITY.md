# Security Policy

Olivares AI is a security product that runs on customer hosts and maps what each agent can access. A flaw in it can become a flaw in the systems it observes, so we hold the bar high by design and we want to hear about problems quickly. Thank you for helping keep it safe.

## Reporting a vulnerability

Report security issues privately to **security@olivares.ai**. Please do **not** open a public issue, pull request, or discussion for a suspected vulnerability.

Include where you can:

- A clear description of the issue and its impact.
- Affected component(s) and version / commit (`olivares version`).
- Steps to reproduce, a minimal proof of concept, and relevant configuration.
- Any suggested remediation.

If you wish to encrypt your report, request our PGP key at the same address before sending details.

This contact is also published machine-readably as an [RFC 9116](https://www.rfc-editor.org/rfc/rfc9116) `security.txt`, served at `https://olivares.ai/.well-known/security.txt`. No PGP key is published yet, so that file advertises no `Encryption` field — request the key as above.

## Coordinated disclosure

We follow coordinated disclosure and operate under a good-faith safe harbor: to the extent permitted by applicable law, we will not pursue or support legal action against researchers who act in good faith, stay within the scope below, and give us a reasonable chance to fix the issue before going public. This safe harbor is a unilateral statement by Olivares AI about its own conduct: it binds no one else — not a customer whose deployment you probe, not a hosting or cloud provider, and not a public authority — and it authorises nothing against systems you do not own or lack permission to test.

- **Acknowledgement:** within 3 business days.
- **Initial assessment / triage:** within 10 business days.
- **Coordinated disclosure window:** up to 90 days from acknowledgement, shortened when a fix ships earlier or extended by mutual agreement for complex issues.

These timeframes are **targets of a single-maintainer project, not contractual commitments**: they are not an SLA, they create no right or remedy for anyone, and missing one is not a breach of any agreement.

We will keep you updated through triage and fix, and we are glad to credit reporters in the release notes unless you prefer to remain anonymous.

## Supported versions

The project is **beta and not yet released**. There are no supported release versions. Security fixes are applied to the `main` branch only.

| Version | Supported |
|---|---|
| `main` (development) | Yes |
| Any tagged release | None exist yet |

This table will be replaced with a real support matrix at the first release.

## Scope

In scope: the code in this repository — the `olivares` engine (`core/`), modules (`modules/`), the embedded web UI (`web/`), the SDK and first-party connectors (`sdk/`, `connectors/`). Issues in the security model itself are explicitly welcome: collector privilege/escape, mTLS handling, multi-tenant isolation, tamper-evidence of the audit ledger, license-key forgery, panel auth, and data-minimization / redaction failures (a persisted secret or payload that should have been dropped).

Out of scope: third-party dependencies (report upstream; tell us so we can pin/patch), social engineering, physical attacks, and findings that require an already-compromised host or root. Denial of service via unrealistic load is generally out of scope.

## Supply-chain and build integrity

For a security product, build integrity is part of the trust model, not an afterthought:

- **Signed releases** with cosign / Sigstore, a published **SBOM** (syft), and checksums — the release pipeline is built and exercised in CI, but **no tagged release exists yet** (see *Supported versions*), so this describes what a release will carry, not an artifact you can download and verify today.
- **Distroless** container images and a single static, memory-safe Go binary, which removes whole classes of C memory-corruption CVEs.
- **Minimal, pinned dependencies**; no `curl | bash` without checksums.
- **CI gates:** dependency scanning with `govulncheck` and secret scanning on every change.

When a vulnerability is fixed, we publish honestly: an advisory, affected versions, and remediation.

## Vulnerability remediation targets

Hardening is a programme, not a one-time build: a shipped image's CVE posture decays the day after release, so we run a **patch-velocity** cadence and publish remediation targets. These take effect at the first tagged release (the project is beta today; see *Supported versions*).

> **What these targets are, and are not.** They are the objectives of this project's engineering practice — **not an SLA, not a warranty, and not a contractual commitment**. No edition carries a contractual remediation SLA and nothing on this page creates one; they confer no right or remedy, and they are always subject to what an upstream fix, a reproducible test and a verified signed build actually allow. Where a commercial agreement exists, its own terms govern and prevail over this page.

We track **two clocks**, because they answer different questions and a small team can hold them honestly only if they are kept apart. The first is how fast we *tell you and give you a mitigation*; the second is how fast we *ship a patched, signed release*. Both run only for a vulnerability that is *reachable* in our shipped artifacts.

**Clock 1 — time to a signed advisory and a mitigation.** From when we confirm a reachable vulnerability, to a published machine-readable advisory (OSV format, see below) naming affected versions and a workaround or configuration mitigation where one exists:

| Severity (CVSS v3.1) | Target to advisory + mitigation |
|---|---|
| Critical (9.0–10.0) | **72 hours** |
| High (7.0–8.9) | **7 days** |
| Medium / Low | next scheduled release |

**Clock 2 — time to a patched, signed release.** From when a fix is available upstream (or a workaround exists), to a patched, signed release:

| Severity (CVSS v3.1) | Target to a patched, signed release |
|---|---|
| Critical (9.0–10.0) | **7 days** |
| High (7.0–8.9) | **14 days** |
| Medium (4.0–6.9) | **30 days** |
| Low (< 4.0) | next scheduled release |

**When the clock starts** depends on where the flaw is:

- **A dependency CVE** — the clock starts when an upstream fix or a workaround exists and `govulncheck` shows the vulnerable code is reachable in our artifacts.
- **A flaw in our own code** — there is no upstream to wait for, so the clock starts when we confirm the report at triage.

Actively-exploited vulnerabilities (CISA KEV, or credible in-the-wild reports) are treated as Critical regardless of base score and may ship out of band on the `security` release channel.

> Why the two clocks carry different numbers: Clock 1 is a document plus a VEX statement plus a workaround, which one maintainer can sustain in 72 hours; Clock 2 ships a verified, signed release, which needs longer and additionally depends on an upstream fix existing. That is why Clock 2's targets are wider — and why both remain targets rather than promises, per the note above. Note that the 72-hour figure here is our *own* advisory target — it is **not** the EU CRA Article 14 72-hour *reporting-to-authorities* deadline, which is a separate obligation (see *EU CRA reporting* below).

**How we keep the clock honest:**

- **Reachability first.** We gate every change on `govulncheck`, whose Go call-graph analysis tells us whether a dependency CVE is actually reachable. Unreachable CVEs are documented in a signed **OpenVEX** statement (`not_affected`, justification `vulnerable_code_not_in_execute_path`) so your scanner isn't lit up by noise — they do not start a remediation clock; reachable ones do.
- **Scheduled rebuilds.** A weekly job rebuilds the image from the latest patched base, re-scans the result (grype + trivy), refreshes the SBOM and VEX, and opens a tracking issue when remediation is due. The distroless base image is pinned by digest and bumped by automation.
- **Verifiable output.** Every release — including a patch release — ships signed (cosign), with an SBOM attestation, an OpenVEX document and SLSA Build L3 provenance (via the `slsa-github-generator` reusable workflows). Verify with `scripts/verify-release.sh`.

The reference bar for this cadence is a continuously-rebuilt image programme (e.g. Chainguard's nightly rebuilds with a 7/14-day SLA); we target the same shape, scaled to a single-maintainer project, and will tighten it as the project matures.

## Security advisories (GHSA → OSV)

When a reported vulnerability is fixed, we publish it as a **GitHub Security
Advisory (GHSA)** — drafted privately, published with the patched signed release
— and, because a `GHSA-…` ID is a valid OSV home-database identifier, the same
advisory becomes available in **OSV-schema** format for OSV-aware scanners. A
CVE can be requested through the advisory (GitHub is a CNA), and the fix is
recorded in the **Security** section of [`CHANGELOG.md`](CHANGELOG.md), crediting
the reporter. The full flow — triage → fix → GHSA → OSV → changelog, and the
open question of whether Olivares.AI itself should become a CNA (it is **not**
one today) — is documented in
[`docs/security-advisories.md`](docs/security-advisories.md). This is the public
output of the disclosure and remediation process above; it does not change the
reporting channel or the remediation targets.

We also publish the same advisories as a **signed, machine-readable feed** in
OSV-schema format, so an installed product can check itself. The feed is signed
offline with the release key (the same key that signs releases; a per-purpose
domain tag keeps an advisory signature from ever being reused as a release
signature), and the binary self-checks against it with **`olivares security
check`** — it verifies the feed offline, then reports whether the running version
is affected and which release to upgrade to. Air-gapped installs run the same
check against the advisories carried in an update or DDIL bundle, with no network.

When a response cannot wait for a binary — block a malicious MCP server, add an
injection signature, deny-list an indicator — we ship a **signed hot-reload
rule-pack** the engine applies at runtime without a restart (verify → anti-rollback →
atomic swap → audit, with instant rollback). The full operational pipeline —
out-of-band security release, advisory-feed publishing, and rule-pack rollout — is in
[`docs/PSIRT-RUNBOOK.md`](docs/PSIRT-RUNBOOK.md).

### EU CRA reporting

For EU Cyber Resilience Act readiness, Article 14 reporting for actively
exploited vulnerabilities and severe incidents applies from 2026-09-11, and
from launch day if Olivares AI is placed on the EU market after that date. The
project is beta and **not yet on the market**, so the obligation is dormant
today. The reporting playbook is documented in
[`docs/CRA-READINESS.md`](docs/CRA-READINESS.md), with the operational
authority-reporting procedure — clocks, channel, records, drill — in
[`docs/CRA-ART14-RUNBOOK.md`](docs/CRA-ART14-RUNBOOK.md).

## What not to include in a report

To avoid making the situation worse, do **not** include in your report:

- Real secrets, credentials, API keys, or tokens — redact them.
- Customer or third-party personal data (PII), or production data dumps.
- Live exploitation against systems or data you do not own or lack permission to test.

If demonstrating impact requires sensitive data, describe it rather than attaching it, and we will arrange a secure channel.
