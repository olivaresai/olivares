# Security advisories: the GHSA → OSV flow

This document describes **how Olivares AI turns a privately-reported
vulnerability into a public advisory** once a fix is ready. It is the
machine-readable, public output of the disclosure process; it sits alongside —
and does **not** restate — the reporting policy, coordinated-disclosure
timeline, and the vulnerability **remediation targets**, all of which live in
[`SECURITY.md`](../SECURITY.md).

- **Intake / private channel:** see [`SECURITY.md`](../SECURITY.md) → *Reporting
  a vulnerability* (`security@olivares.ai`; GitHub Private Vulnerability
  Reporting, enabled with the public repository), advertised machine-readably via
  the RFC 9116
  [`/.well-known/security.txt`](https://olivares.ai/.well-known/security.txt).
- **Remediation targets:** see [`SECURITY.md`](../SECURITY.md) →
  *Vulnerability remediation targets* (defined there; not duplicated here).
- **Public changelog output:** the *Security* section of
  [`CHANGELOG.md`](../CHANGELOG.md).

> **Status:** beta — **no advisories have been published yet**. This
> documents the process we will follow; it does not claim a track record that
> does not exist.

## The flow

```
private report ──▶ triage ──▶ fix on affected release line ──▶ draft GHSA (private)
   (SECURITY.md)       │                           │
                       └──▶ actively exploited? ──▶ CRA/ENISA reporting clock
                            severe incident?        (docs/CRA-READINESS.md)
                                                   ▼
                          patched, signed release       ──▶ publish GHSA
                                                   │
                       ┌───────────────────────────┼───────────────────────────┐
                       ▼                            ▼                           ▼
            GitHub Advisory Database        OSV record (OSV-schema)      CHANGELOG "Security"
              (+ optional CVE)               via the GHSA home DB           + credit reporter
```

1. **Private report → triage.** A report arrives through the private channel in
   [`SECURITY.md`](../SECURITY.md). We acknowledge and triage within the windows
   stated there. Nothing is discussed in a public issue or pull request.

2. **Draft a GitHub Security Advisory (GHSA), privately.** Repository security
   advisories let maintainers *privately discuss and fix* a vulnerability before
   publication. The draft captures the affected component(s), version ranges,
   severity (CVSS vector), and — where appropriate — a private fork to develop
   the fix. The advisory stays **private** until a fix is available.

3. **Fix and ship.** Follow [`PSIRT-RUNBOOK.md`](PSIRT-RUNBOOK.md): while no tagged
   release exists, fix on `main`; once release lines exist, use a `security/<id>` backport
   from every affected line. Deliver a patched, **signed** release through the release
   pipeline (cosign / SBOM attestation / OpenVEX / SLSA provenance — see
   [`SECURITY.md`](../SECURITY.md) → *Supply-chain and build integrity*). Reachability is
   assessed with `govulncheck`; an unreachable dependency CVE is recorded in OpenVEX
   rather than starting a remediation clock.

4. **Branch to CRA / ENISA reporting when required.** If triage determines that
   the issue is an actively exploited vulnerability or a severe incident with
   product-security impact, start the CRA reporting clock and follow
   [`CRA-READINESS.md`](CRA-READINESS.md). This branch runs alongside the GHSA
   flow; it does not replace the advisory, OSV, changelog, or release-note
   outputs.

5. **Publish the GHSA.** On (or coordinated with) the release, the advisory is
   published. Optionally we **request a CVE** through the advisory: GitHub is a
   CVE Numbering Authority (CNA) and is authorized to assign CVE IDs, so a CVE
   can be obtained without Olivares.AI being a CNA itself (see *Becoming a CNA?*
   below). Published advisories for supported ecosystems appear in the **GitHub
   Advisory Database**.

6. **OSV record.** A `GHSA-…` identifier is a valid **OSV home-database ID** (the
   GitHub Advisory Database is one of the [OSV](https://ossf.github.io/osv-schema/)
   data sources), so each published advisory is available in **OSV-schema**
   format and is consumable by OSV-aware scanners (`osv-scanner`, OSV.dev) with
   no extra work on our side. See the OSV-schema mapping below.

7. **Public notice + credit.** The advisory is summarised in the *Security*
   section of [`CHANGELOG.md`](../CHANGELOG.md) (linking the `GHSA-…` / CVE ID),
   and the reporter is credited in the advisory and release notes unless they
   ask to remain anonymous.

## OSV-schema mapping

We publish in the [OSV schema](https://ossf.github.io/osv-schema/) so the
advisory is portable across scanners. The fields we populate:

| OSV field | Source for an Olivares AI advisory |
|---|---|
| `id` | The `GHSA-…` identifier (OSV home-database ID). |
| `aliases` | The `CVE-…` ID, if one was assigned. |
| `summary` / `details` | Human description and impact. |
| `severity` | CVSS vector — `type` one of `CVSS_V3` / `CVSS_V4` with the vector string as `score`. |
| `affected[].package` | The affected module/artifact (engine, a module, a connector, an image). |
| `affected[].ranges[].events` | `introduced` and `fixed` versions delimiting the affected window (`last_affected` / `limit` where a fix version is not expressible). |
| `references` | The GHSA, the release, the fix commit/PR, this policy. |
| `credits` | The reporter (with consent). |
| `published` / `modified` | Timestamps, maintained by the advisory database. |

The schema also defines `schema_version`, `withdrawn`, `upstream`, `related`,
and `database_specific`; we use them only when applicable (e.g. `withdrawn` if an
advisory is retracted).

## Becoming a CNA? — an open question, not a commitment

GitHub already lets us obtain a CVE for an advisory (GitHub is a CNA), so a CVE
ID is available **today without Olivares.AI becoming a CNA**. Running our own CVE
Numbering Authority — assigning and publishing CVE records for our own product
scope under the CVE Program — is a **possible future step**, not something we do
now.

> **Honest status:** Olivares.AI **is not a CNA.** Whether to apply is an open
> decision for the maintainer. The current, authoritative requirements and
> process for the CNA program are published by the CVE Program at
> <https://www.cve.org/ProgramOrganization/CNAs> and the CNA Rules — those
> sources, not this file, are the reference, and we will not characterise the
> program's requirements here from memory. Until and unless that decision is
> taken, we rely on GitHub's CNA-of-last-resort / advisory CVE path.

## Why this shape

- **Private until fixed** protects users of a security product: a public issue
  is not a confidential channel (see [`SECURITY.md`](../SECURITY.md) and the
  issue-template `config.yml`, which redirects vulnerability reports to the
  private channel).
- **GHSA + OSV** gives both a human advisory and a machine-readable record that
  downstream scanners already understand, with the GitHub Advisory Database
  doing the OSV distribution for us.
- **The CHANGELOG Security section** is the durable, in-repo public trail, so an
  operator upgrading can see exactly what was fixed and when.
