<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

<a id="feature-matrix--open-core-agpl-vs-commercial-add-ons"></a>

# Feature matrix — Community (AGPL) vs Business and Enterprise

The AGPL build is the complete platform. Business and Enterprise add
scale-operation and risk-mitigation capabilities as additive code, never
features removed from the open product. One Business subscription includes the
Business base line and four capability families (Regulated Operations, AI
Runtime Security, Compliance Packs and Identity & Scale); none of them is sold
separately, and an administrator turns each family on or off. Enterprise
includes everything in Business, plus terms agreed in a contract and the
capabilities that depend on them. [Editions](../editions.md) places every
capability; this matrix adds the detail. The `enterprise/` directory ships only
in the commercial build and is never present in the public binary.

> **The three promises:**
>
> 1. What is open never moves to a paid tier.
> 2. The core AGPL has no artificial performance or size limits.
> 3. The criterion is published: "security-core and the complete
>    observe-map-govern-audit loop are free; commercial is scale-operation +
>    legal exception."

---

## Identity and access

| Capability | Community (AGPL) | Business / Enterprise |
|---|---|---|
| Single-IdP SSO | OIDC (Authorization Code + PKCE) and SAML 2.0 (signed responses, anti-replay) — one active IdP, fully functional | — |
| Multi-IdP federation | Single-IdP cap enforced (`multi_idp_requires_enterprise`) | **Business: Identity & Scale.** Per-tenant multi-IdP resolution: more than one active OIDC/SAML identity provider, routing by tenant or domain (`enterprise/federation`) |
| SSO enforcement policy | Stored posture (require-SSO, IP allow-list) but not enforced (`enforced_by=unavailable`) | **Business: Identity & Scale.** Enforced over the engine's login surface: block password login, network/IP allow-list (`enterprise/ssoenforce`) |
| SAML SP-metadata endpoint | Unauthenticated `GET /v1/auth/federation/saml/metadata` publishes the SP metadata of the active SAML IdP for IdP configuration, after first-boot setup (`501 sso_not_configured` while no SAML IdP is active; `500 sso_provider_unavailable`, with the cause in the server log, when a configured IdP cannot be built or its metadata cannot be produced) | — |
| User accounts | **Unlimited** — no cap in any edition (licensing decision of 2026-07-27) | **Unlimited** — Business and Enterprise entitlements never count seats |
| CyberArk Conjur vault | Not available (`conjur` is an unknown source kind) | **Business: Identity & Scale.** In-process source connector, identity roster provider, and NHI lifecycle actuator for CyberArk Conjur (`enterprise/connectors/conjur`) |
| WebAuthn/FIDO2, AAL step-up | Full support | Community |
| PIV/CAC smart-card sign-in | Not available (`501 piv_not_configured`) | **Business: Identity & Scale.** PIV/CAC certificate verification and smart-card sign-in |
| Non-human identity lifecycle | Full lifecycle: credential rotation, expiry, NHI audit | — |
| Agent-identity federation | Entra Agent ID, AWS AgentCore, Google, SPIFFE/SPIRE | — |
| Roster reconciliation (SCIM) | SCIM server (open); AD/LDAP/Okta/Entra/Vault/Infisical identity sources | — |

## Content and data security

| Capability | Community (AGPL) | Business / Enterprise |
|---|---|---|
| Inline guardrails | PII scanning, prompt-injection detection, jailbreak detection — deterministic, on every governed request | — |
| DLP egress gate | Deny-closed DLP egress with persistent sensitivity labels | — |
| Content firewall / DLP | Core text DLP and deny-closed unscanned posture run; no deep content inspection | **Business: AI Runtime Security.** Deep prompt-injection, exfiltration and unsafe-action inspection across message, retrieval and MCP render channels (`enterprise/contentfirewall`) |
| Hook content inspector | Tool_input reduced to sanitized resource ref for decisions; no deep inspection of arguments | **Business: AI Runtime Security.** DLP firewall for Claude Code hook `tool_input`: deep inspection of hook arguments for sensitive values and dangerous structure (`enterprise/hookhardening`) |
| RTBF crypto-shred coordinator | Per-subject crypto-shredding, legal-hold gating, ledger verification (open-core RTBF workflow) | **Business: Regulated Operations.** Policy readiness checks, WORM coordination and enhanced verification for right-to-erasure compliance (`enterprise/rtbf`) |
| Computer-use gate | Response-side audit (action extraction + typed-text DLP) runs unconditionally; computer-use tool declarations pass ungoverned | **Business: AI Runtime Security.** OCR, timeline and deep-DLP governance for computer-use tool declarations (`enterprise/computerusegate`) |
| Operator-provided signing keys (BYOK) | Shared values or mounted files for audit, catalog and policy keys; off-box checkpoint signing (HYOK); `keys status` | — |
| CMEK envelope encryption | Refuses a configured CMEK installation without changing its keys or store | **Business: base line.** AWS KMS, Google Cloud KMS and Azure Key Vault; sealed signing keys and operator configs. Data recovery works without an active license. |
| Privileged-session recording | Full support | — |
| Post-quantum TLS | X25519MLKEM768 hybrid key establishment is the default | — |

## Threat and incident

| Capability | Community (AGPL) | Business / Enterprise |
|---|---|---|
| Guardian loops | Finding detection, reporting, anti-spiral dedup, HITL escalation | — |
| Guardrail findings | OWASP Agentic ASI01-ASI10 catalog; estate kill switch with two-person re-enable | — |
| Threat-intel catalog ingestion | No threat-intel catalog surface; the detection engine runs on its own signals and named rule families (the open build can load operator-signed rulepacks, `connectors/threatfeed/rulepack.go`) | **Business: base line.** A base catalog compiled into the Business build, plus optional signed, versioned feed artifacts the operator pins a key for and applies — anti-rollback, last-known-good retained, an expired artifact ignored (`enterprise/threatintel`). Olivares operates no curated distribution and publishes no cadence. |
| Incident close-loop | Passive notify sinks (PagerDuty/Opsgenie) create alerts from findings | **Business: Regulated Operations.** Governance-to-incident bidirectional sync + Teams bot connector (`enterprise/incidentloop`) |
| Circuit-breaker engine | Kill switch, guardian, tier-floor enforcement all functional; no threshold-based auto-suspension | **Business: AI Runtime Security.** Threshold-based automatic agent suspension with auto-reset, cooldown and kill-switch escalation (`enterprise/circuitbreaker`) |
| Attack-graph scanner | Access-path queries over the access graph (build-independent reads) | Not in this release. Planned post-release — `enterprise/attackgraph` does not exist in either tree (product decision of 2026-08-26). |

## Compliance and regulatory

| Capability | Community (AGPL) | Business / Enterprise |
|---|---|---|
| Compliance evidence | Sealed, append-only evidence mapped to 26 framework catalogs; OSCAL export (component-definition + assessment-results + control-mapping) | — |
| OSCAL profile resolver + POA&M | Evidence OSCAL export with three models; no SSP ingestion or POA&M | **Business: Compliance Packs.** FedRAMP-adjacent SSP ingestion and plan-of-action-and-milestones builder (`enterprise/oscalingest`) |
| DORA regulatory register | ICT-risk view export (`GET /dora`); register/incident table storage | **Business: Compliance Packs.** Register-of-Information generator structured to Commission Implementing Regulation (EU) 2024/2956 + major-incident classifier and report drafter (`enterprise/doraregister`) |
| ISO 42001 AIMS packager | Framework catalog `iso_42001` (14 Annex A controls), crosswalk frameworks, evidence engine, assessment engine, risk classifier | **Business: Compliance Packs.** Statement of Applicability, AI policy, risk register, impact assessments, lifecycle controls, supplier governance (`enterprise/iso42001`) |
| Compliance-depth overlays | 26 framework catalogs as verified data with per-framework pins and disclaimers | **Business: Compliance Packs.** TX TRAIGA, CA SB 53, IL HB 3773, CO SB 26-189, HIPAA, PCI DSS 4.0.1, FINRA GenAI overlays, CCM, FedRAMP 20x KSIs (`enterprise/compliancedepth`) |
| SIEM/ITSM push | Pull export (CEF/LEEF/syslog/OTLP/OCSF) + push forwarder to external sinks | — |

## Operations and resilience

| Capability | Community (AGPL) | Business / Enterprise |
|---|---|---|
| Audit ledger | Append-only, hash-chained, Ed25519 per-event-signed; offline verification; 7-year default retention | — |
| WORM archival | Directory archive with chained manifests; offline verifier | **Business: Regulated Operations.** S3 Object Lock COMPLIANCE-mode delivery |
| WORM retention governor | Per-class retention schedules, hold-checked sweep, freely relaxable | **Business: Regulated Operations.** Named regulatory floors (SEC 17a-4, FINRA 4511, CFTC 1.31), compliance-mode lock (`enterprise/wormretention`) |
| WORM long-horizon hold | Dual-control legal-hold plane; GDPR crypto-shred | **Business: Regulated Operations.** Long-horizon legal-hold orchestration: object-lock legal holds on archived segments reconciled with engine holds (`enterprise/wormretention`) |
| WORM evidence bundle | Archive export and offline verification | **Business: Regulated Operations.** Examiner-grade evidence bundle: native records + verification verdict + human-readable report + manifest + chain-of-custody (`enterprise/wormretention`) |
| WORM archive sinks | Directory sink | **Business: Regulated Operations.** S3 Object Lock, Azure immutable-LOCKED and GCS Bucket-Lock WORM sinks (`enterprise/wormsinks`) |
| HA durable event bus | In-process bus | **Business: Identity & Scale.** Core-NATS bridge (at-most-once cross-node delivery); at-least-once + dedup over NATS JetStream for enforcement events; RAFT-replicated stream (`enterprise/durablebus`) |
| Report generation | On-demand report generation via API (5 built-in templates) | **Business: base line.** Scheduled periodic reports, custom logo/colours/footer, operator-uploaded HTML templates (`enterprise/reporting`) |
| Server-tool egress control | Observe-only: `req.Tools` visible but not enforced in the inference PEP | **Business: AI Runtime Security.** Enforce `req.Tools` in the inference PEP: deny-closed on undeclared server-side tools (`enterprise/servertoolegress`) |
| Backup/DR | Encrypted `.drbundle` backups, verify-on-restore, chain continuity preservation | — |
| FinOps budgets | Deny or throttle spend per workspace, model, surface | — |

## Integration

| Capability | Community (AGPL) | Business / Enterprise |
|---|---|---|
| CAEP/SSF SET transmitter | CAEP receiver (open); no outbound SET emission | **Business: AI Runtime Security.** Emit CAEP agent-risk events to external SSF receivers (signed SETs, RFC 8935) (`enterprise/caeptransmit`) |
| Upstream credential provider | Static operator-configured credential for each upstream target | **Enterprise.** RFC 8693 token-exchange: short-lived, audience-bound tokens instead of static credentials (`enterprise/credminter`). Scoped per contract; the credential minter is absent from the Business build |
| Tool-pin verifier | Tools/call gate proceeds without pin verification | **Business: base line.** Deny-closed on tool-definition change / rug-pull detection: stores and compares definition fingerprints (`enterprise/toolpinstore`) |
| MCP elicitation mediator | Elicitation capability advertisement inventoried; prompts/responses pass ungoverned | **Business: AI Runtime Security.** Runtime governance of MCP elicitation prompts, user responses and sampling injection (`enterprise/elicitationmediator`) |
| Terraform provider | Full support | — |
| Client SDKs | Go, Java, Python, TypeScript (generated) | — |
| Webhooks | Typed platform webhooks with retries/replay (cursor-based), SSRF-hardened | — |

---

## Notes

- **No user cap, in any edition.** Self-hosted user accounts are unlimited in
  Community, Business and Enterprise alike, whatever the license
  state — a valid license, an expired one, or none. The cap of 3 active accounts
  that shipped before 2026-07-27 was removed outright; the seat
  seam remains in the code as a compatibility no-op that refuses nothing. The
  commercial model is term-based, never per-seat.

- **Business and Enterprise are additive new code, never features removed from the
  open product.** A nil capability in the community build means the gate is
  absent (not that it denies) — the open binary behaves exactly as it did before
  the enterprise seam was introduced. No rug-pull.

- **Community runtime capabilities are never license-gated.** A license never
  enables, disables or caps Community features or user accounts. `MaxUsers`
  remains a display-only wire value in every edition. The current exception is
  installing a local update bundle (`olivares upgrade --bundle`), which needs a
  current license; bundle verification with `--check`, network upgrades, and
  installing a new package or container image remain available without one.
  [Editions](../editions.md#where-the-product-differs-from-this-page-today)
  records that exception. Commercially,
  Business and Enterprise are a **paid-term-limited right**, and the commercial
  build enforces the term on selected operations: once the paid term and any
  signed grace have ended, an entitlement gate refuses new work of the
  capabilities it covers (for example packaging a new compliance bundle,
  generating a new posture or risk report, minting a credential or a new LDAP/AD
  directory sign-in) with
  `403 addon_requires_license`. Coverage is per capability, not for every
  capability: the gate never covers reading, verifying or exporting what you
  already produced, and never the evaluation of a deny-closed security control,
  so an expired license does not drop require-SSO. Community features, your
  data and your export keep working.

See [Editions](../editions.md) for what each edition and each Business family
includes, and [evaluation-guide.md](./evaluation-guide.md) for the 10-day
proof-of-value procedure.
