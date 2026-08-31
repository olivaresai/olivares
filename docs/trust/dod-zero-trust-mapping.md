<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# DoD Zero Trust capability mapping — for the AI estate

> **Status: capability-outcome mapping, NOT an accreditation and NOT a claim of DoD use.**
> The DoD Zero Trust Strategy is explicit that it is won by outcomes, not by naming a product:
> *"This ZT strategy does not mandate or prescribe specific technologies or potential solutions.
> Rather, it describes all the ZT capabilities that must be implemented to reach both the Target
> and Advanced Level ZT. The Components are free to select their own solutions and solution
> architectures"* (DoD Zero Trust Strategy v1.0, October 21, 2022, §Course of Action; primary PDF
> at `dodcio.defense.gov/Portals/0/Documents/Library/DoD-ZTStrategy.pdf`, re-read 2026-07-09).
> This page maps each of the strategy's **45 capabilities** (Capability Execution Roadmap,
> `ZT-CapabilitiesActivities.pdf`, same library) to what Olivares verifiably contributes — **for
> the AI estate it governs** — with repository evidence per row, and says plainly where it
> contributes nothing.

**Scope honesty up front.** Olivares is the governance control plane for an organization's AI
estate: agents, AI coding surfaces, model providers, MCP servers, knowledge/RAG. It is *not* a
network switch, an EDR, or an ICAM system. Components meet Target Level ZT
(*"no later than the end of FY2027"*, ibid.) with a portfolio; Olivares' contribution is
concentrated where AI systems are the subject — the pillar the strategy centers
(*"the Data Pillar, which is central to the model"*) plus the policy-decision, automation and
analytics outcomes for AI activity. DoD defines ZT per **NIST SP 800-207** (August 2020, final):
*"no implicit trust granted to assets or user accounts based solely on their physical or network
location"* — which is exactly how the engine treats agents, sessions and sources
(the source-scoping model: deny-closed scoping keyed on the
authenticated subject, never on network position).

Legend: **Covers (AI estate)** = the outcome is materially delivered for AI subjects/resources ·
**Contributes** = real but partial input to the Component's outcome · **Not covered** = buy/build
elsewhere; we say so.

## Pillar 1 — User

| Cap. | Capability (v1.0 roadmap) | Verdict | Evidence in this repository |
|---|---|---|---|
| 1.1 | User Inventory | Contributes | Tenant user/membership model with directory sync: `core/model/auth.go`, SCIM provisioning `core/api/handlers_federation.go`, group closure `core/auth/groupmap.go` |
| 1.2 | Conditional User Access | Covers (AI estate) | Per-subject allow/forbid over sources/models/sessions: `modules/sourcescope` (subject axes), `modules/models/modelaccessgate.go` (model-access gate), Cedar scoped grants `core/auth/authorizer.go` |
| 1.3 | Multi-Factor Authentication | Contributes | WebAuthn/passkeys `core/auth/webauthn.go`; PIV/CAC x509 path `core/auth/piv.go`; step-up ceremonies `core/auth/assurance.go:21-31` (AAL1→AAL3 model) |
| 1.4 | Privileged Access Management | Covers (AI estate) | Scoped admin + custom roles; dual-control for relaxations `modules/governance/breakglass.go:25` (break-glass as audited escape valve), sourcescope posture requests (proposer ≠ approver) |
| 1.5 | Identity Federation & User Credentialing | Covers (AI estate) | SAML/OIDC multi-IdP federation `core/auth/federation.go`, issuer-bound subjects `core/auth/ema.go`, SPIFFE-based workload credentials `core/runtime/executor/credential.go` |
| 1.6 | Behavioral, Contextual ID & Biometrics | Not covered | No biometric or behavioral identity verification. Session anomaly signals exist (7.4 row) but do not authenticate anyone. |
| 1.7 | Least Privileged Access | Covers (AI estate) | Deny-closed defaults across gates: empty egress allowlist denies all `core/runtime/sandboxrt/proxy.go:288-289`; confined source ⇒ deny unless matched (`modules/sourcescope` resolver); admission without trust anchors refuses `cmd/olivares/externalplugins.go` |
| 1.8 | Continuous Authentication | Contributes | Session re-verification and revocation events: CAEP `core/auth/caep_events.go`; assurance decay to AAL1 `core/auth/assurance.go:31` |
| 1.9 | Integrated ICAM Platform | Not covered | Olivares consumes the Component's ICAM (federation above); it is not an ICAM platform. |

## Pillar 2 — Device

| Cap. | Capability | Verdict | Evidence |
|---|---|---|---|
| 2.1 | Device Inventory | Not covered | No endpoint inventory. The estate inventory covers AI assets (agents, models, MCP servers, sources): `modules/inventory`. |
| 2.2 | Device Detection and Compliance | Contributes (narrow) | For AI developer workstations only: managed-settings posture of Claude Code / Codex surfaces (`connectors/claude-config`, `connectors/codex-managed-config`) verifies the org's policy actually reaches the tool. Not an endpoint-compliance product. |
| 2.3 | Device Authorization w/ Real-Time Inspection | Not covered | — |
| 2.4 | Remote Access | Not covered | (The engine's own exposure is loopback-by-default with TLS-on: `cmd/olivares/cmd_serve.go`; that is self-posture, not a remote-access capability for the Component.) |
| 2.5 | Automated Asset/Vulnerability/Patch Mgmt | Contributes (self) | For the product itself: signed releases + SBOM/SLSA provenance (`docs/CRA-READINESS.md`), OTA update framework with signed manifests (`cmd/olivares/cmd_upgrade.go`). Not a fleet patching system. |
| 2.6 | Unified Endpoint Management | Not covered | — |
| 2.7 | EDR & XDR | Not covered | eBPF/Envoy observation of *agent* runtime egress exists (`connectors/ebpf`, `connectors/envoy`) but is AI-runtime telemetry, not endpoint detection & response. |

## Pillar 3 — Applications & Workloads

| Cap. | Capability | Verdict | Evidence |
|---|---|---|---|
| 3.1 | Application Inventory | Covers (AI estate) | Registry/catalog of governed AI applications and connectors: `modules/catalog` (entry kinds incl. `mcp`, `connector`, model admissions), `modules/inventory` |
| 3.2 | Secure Software Development & Integration | Contributes | Governed AI coding surfaces (Claude Code session runtime `modules/sessions`, hook PEP `cmd/olivares/claudehookpep.go` with path/subtree deny/allow/ask); supply-chain posture for the product (SLSA Build L3 attestations) |
| 3.3 | Software Risk Management | Covers (AI estate) | Deny-closed signed admission of models (`core/secure/modelsign/`, AIBOM), external connector binaries (`cmd/olivares/externalplugins.go`, byte-pinned exec `core/runtime`), `.mcpb` PKCS#7 verification; named, framework-referenced detector rule families over AI assets (`modules/security/detect.go`). `modules/security` has **no external-intel loader**; the open build can apply operator-pinned signed rule-packs (`connectors/threatfeed/rulepack.go`), and the base threat catalog is the commercial `enterprise/threatintel` add-on. Olivares operates no curated source. |
| 3.4 | Resource Authorization & Integration | Covers (AI estate) | The PEP mesh: inline inference proxy `modules/inferenceproxy`, MCP gateway `cmd/olivares/mcpgateway.go`, A2A PEP `connectors/a2a/pep.go`, hook PEP `cmd/olivares/claudehookpep.go`; every AI resource access is policy-decided per request |
| 3.5 | Continuous Monitoring & Ongoing Authorizations | Contributes | Live posture endpoints + machine-readable evidence API `modules/compliance/evidence.go:23` (head + live hash-chain verify), approvals re-driver (`approval.resolved` events), OSCAL export `modules/posture-export` |

## Pillar 4 — Data (the strategy's central pillar)

| Cap. | Capability | Verdict | Evidence |
|---|---|---|---|
| 4.1 | Data Catalog Risk Alignment | Covers (AI estate) | Governed source catalog with per-source scoping surface and provenance: `modules/sourcescope`, source_mode provenance, knowledge-base catalog `modules/knowledge` |
| 4.2 | Enterprise Data Governance | Covers (AI estate) | The product's reason to exist: source→scope·effect bindings with dual-control relaxation, retention & legal hold `core/audit/archive.go`, RTBF with DEK erasure (`modules/compliance` — the audit chain verifies after erasure) |
| 4.3 | Data Labeling and Tagging | Contributes | Document ACL refs + classification travel with content into retrieval (`connectors/contentsource/contentsource.go:91-96`); PII discovery tags findings `modules/compliance/pii.go`. No enterprise-wide tagging fabric. |
| 4.4 | Data Monitoring and Sensing | Covers (AI estate) | Content firewall on ingest and retrieval: `modules/security/contentfilter.go`, injection scan `modules/security/anthropic_injection.go`, deny-closed HIGH-severity retrieval block (`modules/knowledge` retrieval scanner) |
| 4.5 | Data Encryption & Rights Management | Contributes | Envelope encryption with key custody seams `core/secure/envelope.go` (BYOK path), TLS-by-default `core/secure/tls.go`, FIPS build variant (`Dockerfile.fips`, see [impact-levels.md](./impact-levels.md)). No DRM. |
| 4.6 | Data Loss Prevention | Covers (AI estate) | DLP at the AI boundary — prompts, tool I/O, retrieval: `modules/security/contentfilter.go`, DLP-in-hook firewall (`cmd/olivares/claudehookpep.go`), minimal-data notification contract `sdk/connector.go:63-92` |
| 4.7 | Data Access Control | Covers (AI estate) | Per-subject, deny-closed source scoping with forbid-absolute algebra (`modules/sourcescope`); document-ACL ∩ principal at retrieval (`modules/knowledge/sync.go:512` ACL change path) |

## Pillar 5 — Network & Environment

| Cap. | Capability | Verdict | Evidence |
|---|---|---|---|
| 5.1 | Data Flow Mapping | Covers (AI estate) | The RRW access map — who/what reached which resource, live from observations: `modules/access-map`, eBPF/Envoy/mesh edges (`connectors/ebpf/network.go`, `connectors/internal/meshobs`) |
| 5.2 | Software Defined Networking | Not covered | — |
| 5.3 | Macro Segmentation | Not covered | Deployment guidance only (`deploy/`, NetworkPolicy examples); the Component's network does this. |
| 5.4 | Micro Segmentation | Contributes (AI runtime) | Per-job egress gate: isolated agent runs have **no** network except a deny-by-default, allowlist-scoped forward proxy with resolve-once IP pinning `core/runtime/sandboxrt/proxy.go:17-34` — micro-segmentation of the AI workload's egress, not of the LAN. |

## Pillar 6 — Automation & Orchestration

| Cap. | Capability | Verdict | Evidence |
|---|---|---|---|
| 6.1 | Policy Decision Point & Policy Orchestration | Covers (AI estate) | Central PDP (Cedar engine + scoping resolvers) consumed by every PEP listed in 3.4; policy truth loop; AuthZEN interop `core/api/authzen_config.go` |
| 6.2 | Critical Process Automation | Covers (AI estate) | Governed eventing with HITL round-trips: `modules/eventing`, approvals via Slack/Teams origination, kill switch automation `modules/sessions/runtime_killswitch.go`, guardian auto-block `modules/governance/guardian.go:711` |
| 6.3 | Machine Learning | Not covered (honest) | Security analytics are deterministic/heuristic (`modules/security/anomaly.go`, `detect.go`) — we do not claim ML detection. |
| 6.4 | Artificial Intelligence | Contributes (inverted) | The strategy asks Components to *employ* AI for security; Olivares' role is the governance OF AI (model governance gate, evals `modules/evals`) — an input to doing 6.4 safely, not an AI-SecOps engine. |
| 6.5 | SOAR | Contributes | Incident close-loop from finding → notification → decision → enforcement (`modules/notify`, `modules/eventing/dispatch.go`); webhook/event egress to the Component's SOAR. Not a general SOAR platform. |
| 6.6 | API Standardization | Covers (AI estate) | Single versioned REST/gRPC surface with stability policy (`docs-site` API stability page, `clients/` generated SDKs); OpenAPI `cmd/olivares/cmd_openapi.go` |
| 6.7 | SOC & Incident Response | Contributes | Feeds the Component's SOC (7.2 row); in-product triage surfaces (findings, forensics timeline `modules/security/forensic.go`, trace viewer) |

## Pillar 7 — Visibility & Analytics

| Cap. | Capability | Verdict | Evidence |
|---|---|---|---|
| 7.1 | Log All Traffic | Covers (AI estate) | Every AI-plane decision and observation lands in the append-only, hash-chained ledger `core/store/audit.go:12`; agent network egress observed via eBPF/Envoy/proxy connectors; session recording `modules/recording` |
| 7.2 | SIEM | Covers (interop) | Native push to Splunk/Sentinel/Chronicle/Elastic + syslog/OCSF: `modules/siemforward/forwarder.go`, `sdk/siemwire/ocsf.go`; WORM archival with legal hold `core/audit/archivecaps.go` |
| 7.3 | Common Security & Risk Analytics | Covers (AI estate) | Risk scoring & posture: `modules/governance/agentrisk.go`, AIVSS `modules/security/aivss.go`, workspace dashboards, adoption/discrepancy analytics |
| 7.4 | User & Entity Behavior Analytics | Contributes | Access-map drift + egress anomaly heuristics `modules/security/anomaly.go:398-417` (external-egress detection), forensic enrichment — identity attribution, least-privilege drift, data lineage — `modules/security/enrich.go`. Deterministic, not ML-UEBA (see 6.3). |
| 7.5 | Threat Intelligence Integration | Contributes | The open build ingests **operator-pinned, signed rule-packs** — deny-list IOCs, blocked MCP servers, agentic-attack patterns — verified against pinned publisher keys and hot-applied (`connectors/threatfeed/rulepack.go`, consumed e.g. by `connectors/openclaw`). Its own detection runs on named rule families (`modules/security/detect.go`). The CoSAI/OASIS-MCP and OWASP files (`modules/security/cosai_mcp.go`, `owasp_mcp.go`) are **control mappings for positioning and compliance, not runtime detectors**. `modules/security` has no external-intel loader, and Olivares operates no curated intel source; the commercial `enterprise/threatintel` add-on adds a compiled base catalog plus optional signed feed artifacts. |
| 7.6 | Automated Dynamic Policies | Contributes | Policy-based automatic responses exist (kill switch, guardian auto-block, request ceilings); policies are operator-authored, not self-tuning. |

## Summary (counted from the rows above)

Of the 45 Target/Advanced capabilities: **Covers 19 · Contributes 16 · Not covered 10** —
counted mechanically from the verdict column of the rows above, grouping the qualified verdicts
under their family (Covers = 18 "Covers (AI estate)" + 1 "Covers (interop)"; Contributes = 12
plain + "narrow"/"self"/"AI runtime"/"inverted"; Not covered = 9 plain + 1 "honest").
The "not covered" set is concentrated
where a network/endpoint/ICAM product belongs
(pillar 2 nearly entirely; 5.2-5.3; 1.6/1.9) — by design. For a Component's ZT implementation
plan, Olivares is the piece that makes the **AI estate** meet the User/Application/Data/
Automation/Visibility outcomes; it composes with, and does not replace, the Component's network,
endpoint and ICAM programs.

## Sources (all re-verified against the living primary on 2026-07-09)

- DoD Zero Trust Strategy v1.0, Oct 21 2022 — `dodcio.defense.gov/Portals/0/Documents/Library/DoD-ZTStrategy.pdf`
- DoD Zero Trust Capability Execution Roadmap / Capabilities & Activities —
  `dodcio.defense.gov/Portals/0/Documents/Library/ZT-CapabilitiesActivities.pdf` (45 capabilities;
  91 Target-Level activities by FY2027, 61 Advanced by FY2032)
- NIST SP 800-207, *Zero Trust Architecture*, Aug 2020 (final; supplement SP 800-207A exists) —
  `csrc.nist.gov/pubs/sp/800/207/final`
