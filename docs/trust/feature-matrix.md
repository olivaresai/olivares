<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Feature matrix — open core (AGPL) vs commercial add-ons

The AGPL build is the complete platform. The commercial add-ons add
scale-operation and risk-mitigation capabilities as additive code — each an
optional, separately-licensed add-on — never features removed from the open
product. The `enterprise/` directory ships only in the `-tags enterprise`
build and is never present in the public binary.

> **The three promises:**
>
> 1. What is open never moves to a paid tier.
> 2. The core AGPL has no artificial performance or size limits.
> 3. The criterion is published: "security-core and the complete
>    observe-map-govern-audit loop are free; commercial is scale-operation +
>    legal exception."

---

## Identity and access

| Capability | Open core (AGPL) | Commercial add-on |
|---|---|---|
| Single-IdP SSO | OIDC (Authorization Code + PKCE) and SAML 2.0 (signed responses, anti-replay) — one active IdP, fully functional | — |
| Multi-IdP federation | Single-IdP cap enforced (`multi_idp_requires_enterprise`) | Per-tenant multi-IdP resolution: more than one active OIDC/SAML identity provider, routing by tenant or domain (`enterprise/federation`) |
| SSO enforcement policy | Stored posture (require-SSO, IP allow-list) but not enforced (`enforced_by=unavailable`) | Enforced over the engine's login surface: block password login, network/IP allow-list (`enterprise/ssoenforce`) |
| SAML SP-metadata endpoint | SAML provider linked and functional for single-IdP login; metadata endpoint returns 404 | Unauthenticated SP-metadata endpoint published for IdP configuration |
| User accounts | **Unlimited** — no cap in any edition (licensing decision of 2026-07-27) | **Unlimited** — commercial entitlements cover add-ons, never seats |
| CyberArk Conjur vault | Not available (`conjur` is an unknown source kind) | In-process source connector, identity roster provider, and NHI lifecycle actuator for CyberArk Conjur (`enterprise/connectors/conjur`) |
| WebAuthn/FIDO2, PIV/CAC, AAL step-up | Full support | — |
| Non-human identity lifecycle | Full lifecycle: credential rotation, expiry, NHI audit | — |
| Agent-identity federation | Entra Agent ID, AWS AgentCore, Google, SPIFFE/SPIRE | — |
| Roster reconciliation (SCIM) | SCIM server (open); AD/LDAP/Okta/Entra/Vault/Infisical identity sources | — |

## Content and data security

| Capability | Open core (AGPL) | Commercial add-on |
|---|---|---|
| Inline guardrails | PII scanning, prompt-injection detection, jailbreak detection — deterministic, on every governed request | — |
| DLP egress gate | Deny-closed DLP egress with persistent sensitivity labels | — |
| Content firewall / DLP | Core text DLP and deny-closed unscanned posture run; no deep content inspection | Deep prompt-injection, exfiltration and unsafe-action inspection across message, retrieval and MCP render channels (`enterprise/contentfirewall`) |
| Hook content inspector | Tool_input reduced to sanitized resource ref for decisions; no deep inspection of arguments | DLP firewall for Claude Code hook `tool_input`: deep inspection of hook arguments for sensitive values and dangerous structure (`enterprise/hookhardening`) |
| RTBF crypto-shred coordinator | Per-subject crypto-shredding, legal-hold gating, ledger verification (open-core RTBF workflow) | Policy readiness checks, WORM coordination and enhanced verification for right-to-erasure compliance (`enterprise/rtbf`) |
| Computer-use gate | Response-side audit (action extraction + typed-text DLP) runs unconditionally; computer-use tool declarations pass ungoverned | OCR, timeline and deep-DLP governance for computer-use tool declarations (`enterprise/computeruse`) |
| BYOK/CMEK envelope encryption | Three KMS backends (AWS KMS, Google Cloud KMS, Azure Key Vault); fail-closed sealed configs | — |
| Privileged-session recording | Full support | — |
| Post-quantum TLS | X25519MLKEM768 hybrid key establishment is the default | — |

## Threat and incident

| Capability | Open core (AGPL) | Commercial add-on |
|---|---|---|
| Guardian loops | Finding detection, reporting, anti-spiral dedup, HITL escalation | — |
| Guardrail findings | OWASP Agentic ASI01-ASI10 catalog; estate kill switch with two-person re-enable | — |
| Threat-intel catalog ingestion | No threat-intel catalog surface; the detection engine runs on its own signals and named rule families (the open build can load operator-signed rulepacks, `connectors/threatfeed/rulepack.go`) | A base catalog compiled into the add-on, plus optional signed, versioned feed artifacts the operator pins a key for and applies — anti-rollback, last-known-good retained, an expired artifact ignored (`enterprise/threatintel`). Olivares operates no curated distribution and publishes no cadence. |
| Incident close-loop | Passive notify sinks (PagerDuty/Opsgenie) create alerts from findings | Governance-to-incident bidirectional sync + Teams bot connector (`enterprise/incidentloop`) |
| Circuit-breaker engine | Kill switch, guardian, tier-floor enforcement all functional; no threshold-based auto-suspension | Threshold-based automatic agent suspension with auto-reset, cooldown and kill-switch escalation (`enterprise/circuitbreaker`) |
| Attack-graph scanner | Access-path queries over the access graph (build-independent reads) | Not in this release. Planned post-release — `enterprise/attackgraph` does not exist in either tree (product decision of 2026-08-26). |

## Compliance and regulatory

| Capability | Open core (AGPL) | Commercial add-on |
|---|---|---|
| Compliance evidence | Sealed, append-only evidence mapped to 26 framework catalogs; OSCAL export (component-definition + assessment-results + control-mapping) | — |
| OSCAL profile resolver + POA&M | Evidence OSCAL export with three models; no SSP ingestion or POA&M | FedRAMP-adjacent SSP ingestion and plan-of-action-and-milestones builder (`enterprise/oscalingest`) |
| DORA regulatory register | ICT-risk view export (`GET /dora`); register/incident table storage | Register-of-Information generator structured to Commission Implementing Regulation (EU) 2024/2956 + major-incident classifier and report drafter (`enterprise/doraregister`) |
| ISO 42001 AIMS packager | Framework catalog `iso_42001` (14 Annex A controls), crosswalk frameworks, evidence engine, assessment engine, risk classifier | Statement of Applicability, AI policy, risk register, impact assessments, lifecycle controls, supplier governance (`enterprise/iso42001`) |
| Compliance-depth overlays | 26 framework catalogs as verified data with per-framework pins and disclaimers | TX TRAIGA, CA SB 53, IL HB 3773, CO SB 26-189, HIPAA, PCI DSS 4.0.1, FINRA GenAI overlays, CCM, FedRAMP 20x KSIs (`enterprise/compliancedepth`) |
| SIEM/ITSM push | Pull export (CEF/LEEF/syslog/OTLP/OCSF) + push forwarder to external sinks | — |

## Operations and resilience

| Capability | Open core (AGPL) | Commercial add-on |
|---|---|---|
| Audit ledger | Append-only, hash-chained, Ed25519 per-event-signed; offline verification; 7-year default retention | — |
| WORM archival | S3 Object Lock (COMPLIANCE mode) WORM sink; segmented archive with chained manifests; offline verifier | — |
| WORM retention governor | Per-class retention schedules, hold-checked sweep, freely relaxable | Named regulatory floors (SEC 17a-4, FINRA 4511, CFTC 1.31), compliance-mode lock (`enterprise/wormretention`) |
| WORM long-horizon hold | Dual-control legal-hold plane; GDPR crypto-shred | Long-horizon legal-hold orchestration: object-lock legal holds on archived segments reconciled with engine holds (`enterprise/wormretention`) |
| WORM evidence bundle | Archive export and offline verification | Examiner-grade evidence bundle: native records + verification verdict + human-readable report + manifest + chain-of-custody (`enterprise/wormretention`) |
| WORM archive sinks | Directory sink + S3 Object Lock | Azure immutable-LOCKED and GCS Bucket-Lock WORM sinks (`enterprise/wormsinks`) |
| HA durable event bus | In-process bus + Core-NATS bridge (at-most-once cross-node delivery) | At-least-once + dedup over NATS JetStream for enforcement events; RAFT-replicated stream (`enterprise/durablebus`) |
| Report generation | On-demand report generation via API (5 built-in templates) | Scheduled periodic reports, custom logo/colours/footer, operator-uploaded HTML templates (`enterprise/reporting`) |
| Server-tool egress control | Observe-only: `req.Tools` visible but not enforced in the inference PEP | Enforce `req.Tools` in the inference PEP: deny-closed on undeclared server-side tools (`enterprise/servertoolegress`) |
| Backup/DR | Encrypted `.drbundle` backups, verify-on-restore, chain continuity preservation | — |
| FinOps budgets | Deny or throttle spend per workspace, model, surface | — |

## Integration

| Capability | Open core (AGPL) | Commercial add-on |
|---|---|---|
| CAEP/SSF SET transmitter | CAEP receiver (open); no outbound SET emission | Emit CAEP agent-risk events to external SSF receivers (signed SETs, RFC 8935) (`enterprise/caeptransmit`) |
| Upstream credential provider | Static operator-configured credential for each upstream target | RFC 8693 token-exchange: short-lived, audience-bound tokens instead of static credentials (`enterprise/tokenexchange`) |
| Tool-pin verifier | Tools/call gate proceeds without pin verification | Deny-closed on tool-definition change / rug-pull detection: stores and compares definition fingerprints (`enterprise/toolpin`) |
| MCP elicitation mediator | Elicitation capability advertisement inventoried; prompts/responses pass ungoverned | Runtime governance of MCP elicitation prompts, user responses and sampling injection (`enterprise/elicitation`) |
| Terraform provider | Full support | — |
| Client SDKs | Go, Java, Python, TypeScript (generated) | — |
| Webhooks | Typed platform webhooks with retries/replay (cursor-based), SSRF-hardened | — |

---

## Notes

- **No user cap, in any edition.** Self-hosted user accounts are unlimited in
  Community, Business, the add-ons and Enterprise alike, whatever the licence
  state — a valid licence, an expired one, or none. The cap of 3 active accounts
  that shipped before 2026-07-27 was removed outright; the seat
  seam remains in the code as a compatibility no-op that refuses nothing. The
  commercial model is term-based, never per-seat.

- **The commercial add-ons are additive new code, never features removed from the
  open product.** A nil capability in the community build means the gate is
  absent (not that it denies) — the open binary behaves exactly as it did before
  the enterprise seam was introduced. No rug-pull.

- **License validation is attestation-only.** The open binary never reads a
  license to enable, disable or block anything. Since the licensing decision of
  2026-07-27, neither
  does the enterprise build read one to cap users: the seat-lift code path that
  used to read `MaxUsers` to raise a community cap is gone, and `MaxUsers` is kept for
  wire compatibility as a display-only figure. In the shipped builds, every
  enterprise gate is build-tag + operator config; the never-a-license-claim
  guarantee (ADR-0010) is permanent for the **open (AGPL) binary**. Commercially,
  the add-ons are a **paid-term-limited right** — the per-module enforcement of
  that term is not implemented yet; it is planned entitlement-enforcement work,
  gated before first sale.

See [why-enterprise.md](./why-enterprise.md) for the enterprise value
proposition, and [evaluation-guide.md](./evaluation-guide.md) for the 10-day
proof-of-value procedure.
