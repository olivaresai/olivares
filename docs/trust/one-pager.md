<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Olivares AI — Enterprise overview

Olivares AI integrates, manages and secures AI in enterprise and professional
environments — one ground truth. Context, resources, sessions and identities in one
self-hosted plane: Claude Code at the deepest level, Codex and Grok Build alongside —
and open to any other model or provider you run. Your agents get what they need to do
real work; you keep the granular permissions, policy, budgets and audit evidence to run
all of it. It complements your agents; it does not compete. Concretely: it inventories every agent, session, model, MCP server, tool
and identity in the estate; maps what each one actually reaches (read/write
access map with permitted-vs-observed drift); enforces policies through four
deny-closed enforcement points; and produces hash-chained, Ed25519-signed audit
evidence mapped to 26 compliance framework catalogs. It runs as a single Go
binary with the web console embedded — on Linux, Docker, Kubernetes, on-prem or
fully air-gapped. There is no mandatory telemetry and no control-plane egress by
default: what crosses the customer perimeter is what the customer configures to
cross it — calls to their model APIs, the SIEM/webhook outputs they wire, an
external embedding provider if they provision one. That is a property of the
architecture and of the deployment's configuration; it is a description, **not a
guarantee**.

---

## What you deploy

| Attribute | Detail |
|---|---|
| Artifact | Single static Go binary with embedded web console |
| Store | SQLite (evaluation, small estates, edge) or Postgres with RLS (production, HA) |
| Deployment | Docker, Kubernetes/Helm, systemd, air-gap bundle |
| HA | Active-passive leader election via Postgres advisory lock; automatic failover; CP semantics |
| Identity | Single-IdP SSO (OIDC + SAML 2.0), WebAuthn/FIDO2, PIV/CAC, AAL step-up; agent-identity federation |
| Crypto | TLS 1.3, mTLS collector-core, X25519MLKEM768 post-quantum default; BYOK/CMEK (AWS/GCP/Azure KMS); optional FIPS 140-3 build |

---

## Capability summary

| Theme | Open (AGPL) highlights | Commercial add-on highlights |
|---|---|---|
| **Identity and access** | Single-IdP SSO, WebAuthn/FIDO2, NHI lifecycle, agent-identity federation, SCIM, unlimited users | Multi-IdP federation, SSO enforcement, CyberArk Conjur |
| **Content and data security** | PII/injection/jailbreak guardrails, DLP egress, BYOK/CMEK, session recording, post-quantum TLS | Content firewall (deep inspection), hook DLP, computer-use gate, RTBF coordinator |
| **Threat and incident** | Guardian loops, kill switch (two-person re-enable), OWASP Agentic catalog | Compiled threat-intel catalog, circuit-breaker, incident close-loop |
| **Compliance and regulatory** | 26 framework catalogs, OSCAL evidence export, sealed evidence, SIEM push | OSCAL POA&M, DORA register, ISO 42001 AIMS pack, sector overlays (HIPAA/PCI/FINRA) |
| **Operations and resilience** | S3 WORM archival, backup/DR with chain verification, FinOps budgets, on-demand reports | WORM retention governor + regulatory floors, Azure/GCS WORM sinks, durable event bus, scheduled branded reports |
| **Integration** | CAEP receiver, Terraform provider, SDKs (Go/Java/Python/TS), typed webhooks | CAEP transmitter, token-exchange (RFC 8693), tool-pin verifier, MCP elicitation mediator |

The full matrix of the commercial add-ons is in
[feature-matrix.md](./feature-matrix.md).

---

## Three promises

1. What is open never moves to a paid tier.
2. The core AGPL has no artificial performance or size limits.
3. The criterion is published: security-core and the complete
   observe-map-govern-audit loop are free; commercial is scale-operation + legal
   exception.

---

## Trust and verification

| Artifact | Detail |
|---|---|
| Release signing | cosign (Sigstore) |
| SBOM | CycloneDX + SPDX, attested |
| Build provenance | SLSA Build L3 (SLSA v1.2) |
| Vulnerability disclosure | OpenVEX; CVE remediation targets (Critical 7d, High 14d); GHSA advisories; `/.well-known/security.txt` (RFC 9116) |
| Reproducible build | `task build:repro` — two builds, identical SHA-256 |
| License validation | Offline Ed25519 attestation, verified against the embedded key; verifying a licence never calls us and there is no remote kill switch. Issuance and the subscription downloads are vendor-side, by design (`LICENSING.md`) |
| Updates | `olivares upgrade` fetches from the public repository's GitHub releases (`licenses.olivares.ai` with `--enterprise`); `--endpoint` points it at your own mirror, and air-gapped estates install from a carried bundle |

---

## Evaluation path

Deploy the demo estate in 5 minutes: `task build`, then
`./bin/olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --data-dir "$(mktemp -d)"`
(loopback-only synthetic demo; `olivares quickstart` is the secure, TLS-on first
run for a real estate). Run the full proof-of-value in 10 business days — see
[evaluation-guide.md](./evaluation-guide.md).

Status disclosure: the product is beta, pre-1.0. The first tagged release is
`v26.8.0`; until it lands, evaluations build from source and the signed-release
artifacts (cosign, SLSA provenance, SBOM, OpenVEX) verify against the source
commit rather than a published tag.

---

## Pricing

Pricing on request — enterprise@olivares.ai

---

## Legal entity

Olivares.AI, Spain (EU). License: AGPL-3.0-only (core, modules, web console);
Apache-2.0 (SDK, connectors); LicenseRef-Olivares-Commercial (enterprise
add-ons). Full terms in [LICENSING.md](../../LICENSING.md).

---

Contact: enterprise@olivares.ai
