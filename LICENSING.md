# Licensing

Olivares AI is **open core**. The complete product is available for free under
the **GNU Affero General Public License, version 3 (`AGPL-3.0-only`)** — the AGPL
build is the whole governance platform, never crippled from within to push you
toward a paid edition. On top of it sits a small, **additive** commercial line in
`enterprise/` (built only with `-tags enterprise`, never in the public binary):
multi-IdP federation, content firewall/DLP, hook hardening, the threat-intel
add-on (a base catalog compiled into the binary, plus optional signed, versioned
feed artifacts the operator pins a key for and applies — Olivares operates no
curated feed distribution and publishes no release cadence), server-tool egress
control, the CyberArk Conjur connector, the incident close-loop, long-horizon WORM
retention and legal-hold depth (named regulatory retention floors with a
compliance-mode lock, examiner-grade evidence bundles, and the Azure/GCS WORM
sinks alongside the open S3 Object Lock one), named-regulation depth (the DORA
Register of Information and major-incident reporting, plus the OSCAL POA&M
emitted beside the open export), the ISO/IEC 42001 AIMS certification-readiness
pack, and a durable JetStream event-bus backend that lifts cross-node delivery of
the enforcement-event class to at-least-once with dedup. It never caps your
users: self-hosted user accounts are unlimited in every edition.

Those are the **families**, and this is how they are **sold**. Four self-hosted business
add-ons, each a paid term on top of the commercial base — these are the names that appear on
your invoice, so this is where to look up what you bought:

| Add-on | What it groups |
|---|---|
| **Regulated Operations** | Long-horizon WORM archive, named regulatory retention floors, legal-hold reconciliation, right-to-be-forgotten depth, and the incident close-loop. |
| **AI Runtime Security** | The content firewall, hook firewall, computer-use gate, elicitation mediator, render inspector, retrieval scanning, server-tool egress control, the circuit breaker and CAEP transmit. |
| **Compliance Packs** | The DORA Register of Information, OSCAL ingest, the ISO/IEC 42001 AIMS pack, and compliance depth. |
| **Identity & Scale** | Multi-IdP federation, group mapping, login enforcement, the CyberArk Conjur connector, and the durable JetStream event-bus backend. |

The grouping above is derived from the commercial canon, not hand-copied: `commerce-lint`
refuses a release in which any of those four names is missing from every public surface, and
the module membership of each add-on is derived by the same tool rather than transcribed.

That is the catalogue by NAME. The per-capability split — what the
AGPL build does and what each add-on adds, side by side — is the edition matrix in
[the edition matrix below](#what-is-open-what-is-commercial-what-is-planned-by-area), and the
reasoning behind each cut is
[Open core & licensing](docs-site/src/content/docs/explanation/open-core-and-licensing.md)
(*What is open vs enterprise* and *Why this model*). Read those before quoting this
paragraph as a complete list: this file has been the short one before. In every
case the open substrate stays open and the add-on is new code layered on top — the
open build answers honestly instead of degrading (an absent subcommand, an unknown
sink kind, or a `501` that names the add-on).

We offer a **commercial license** that provides a private *exception* to the
AGPL's obligations (for organizations that cannot comply with them). The
`enterprise/` capabilities are offered as **separate, optional add-ons** under
their own commercial terms — packaging and pricing on request. So the open and
commercial editions are **not** identical — the add-ons are new code that was
never in the open build (the GitLab `ee/` model) — but nothing is taken away
from what ships open: no published feature is moved behind the wall. The
AGPL/Apache split itself is the classic dual-licensing frontier (MySQL, Qt,
MinIO, Grafana).

## What is open, what is commercial, what is planned — by area

This table maps each capability area to where it ships — the open (AGPL) build, or one of the separate, optional commercial add-ons — and what is planned; maturity per capability is stated honestly in [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md). The full list of reserved seams is declared in the public tree itself ([`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go)): a capability the open binary reserves answers `501` or no-ops, and its comment says so — nothing is hidden and nothing open is removed.

| Area | Open (AGPL) | Commercial add-ons | Planned |
|---|---|---|---|
| Work & orchestration | durable work items (brief, dependencies, acceptance, decisions, events), fenced leases with takeover and revoke, orchestrated launch of sessions against a work item, with work-fenced input and stop in the sessions API, A2A delegation to authorized peers with durable receipts, workflow-scoped messages/acks/handoffs, console Work and Orchestration views | — | shadow dual-report and the authority switch that makes this plane the system of record |
| Visibility | inventory of agents/sessions/models/MCP servers/tools/identities, read/write access map with Permitted-vs-Observed drift, live sessions, orchestration graph, health/SLA | — | — |
| Policy & enforcement | Cedar authorization engine (RBAC + deny-overlay + scoped grants), four deny-closed enforcement points (Claude Code hook, inline `/v1/messages` proxy, MCP `tools/call` gate, A2A delegation gate), two-person approvals, break-glass with dual control, estate kill-switch | hook hardening, server-tool egress control, computer-use governance gate, MCP tool-definition pins (deny-closed on a changed definition), automatic circuit breaker with kill-switch escalation | — |
| Claude & the agent ecosystem | Claude Code governed in the hook, console launch/attach/govern/stop of Claude Code sessions, enterprise managed-settings delivery, per-subject/per-surface model access, MCP (OAuth-gated resource server, posture, registry, `.mcpb`), A2A v1, surfaces for gemini-cli/Cursor/Codex CLI/opencode/goose/cline/OpenHands/OpenClaw/Hermes (enforcement where the surface exposes it, posture observation where it doesn't), Teams notifications with approval deep-links | MCP App render content inspection, elicitation/sampling mediation | — |
| Context & knowledge | ten live content sources (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL) plus a root-confined filesystem source (local/NFS/SMB mounts), governed RAG (lexical retrieval by default, model-backed semantic with a provisioned embedder — fails closed under `embed_policy=model_backed`) with deny-closed clearance at retrieval time, per-source provenance, data-product catalog with versioned contracts and quality gates | — | — |
| Identity & access | single-IdP SSO (OIDC + SAML 2.0), WebAuthn/FIDO2, PIV/CAC, AAL step-up, non-human identity lifecycle, agent-identity federation (Entra Agent ID, AWS AgentCore, Google, SPIFFE/SPIRE), roster reconciliation (AD/LDAP/Okta/Entra/Vault/Infisical) with SCIM, CAEP event receiver | multi-IdP federation, SSO-enforcement, managed SCIM, CyberArk Conjur NHI rotation, CAEP transmitter (signed SETs to SSF receivers) | — |
| Data security | inline guardrails (PII, prompt-injection, jailbreak), DLP egress, BYOK/CMEK across three KMS backends (AWS KMS, Google Cloud KMS, Azure Key Vault), privileged-session recording, right-to-erasure with verified key-shred, retention and legal-hold, residency attestation, TLS 1.3 hybrid PQC key establishment (X25519MLKEM768) | content firewall/DLP | — |
| Evidence & compliance | hash-chained Ed25519-signed audit ledger, sealed append-only evidence, 26 framework catalogs, dir/S3 archive with export/verify (dir is WORM only on an immutable substrate; S3 uses Object Lock), OSCAL export (three open models), open DORA ICT-risk view, SIEM/ITSM push (CEF/LEEF/syslog/OTLP/OCSF) | OSCAL profile/SSP ingestion + POA&M builder, regulatory retention floors + compliance-mode lock (SEC 17a-4/FINRA 4511/CFTC 1.31), DORA Register-of-Information + major-incident reports, long-horizon WORM legal holds + examiner-grade evidence bundles, Azure/GCS WORM sinks, ISO 42001 AIMS pack, compliance-depth + NIS2 classification packs, enterprise reporting | — |
| Operations | FinOps budgets that deny or throttle spend, calibrated LLM-judge evals with blocking CI gate (on-demand: judge credential required, else `SKIPPED`), OS-isolated red-team sandboxes (gVisor/Firecracker; unprovisioned runs report `DEGRADED`), connector-health dashboard with public status page, console-managed backups and restore, open attack-path queries | compiled threat-intel catalog, incident close-loop | — |
| Platform & deploy | single static binary with embedded console, SQLite or Postgres with row-level security, Docker/Kubernetes/Helm/air-gapped, Terraform provider, generated client SDKs (Go, Java, Python, TypeScript), open in-proc bus + Core-NATS bridge | durable JetStream bus (at-least-once + dedup) | Windows packages (today: Linux container or build from source), model fine-tuning post-v1, voice telemetry probe (declared deny-closed seam today) |

The AGPL build is the whole platform and is never feature-capped from within. The commercial add-ons are additive new code, never features removed from the open product. A subscription is the credential you download signed module packs with — a distribution-style model of signed module packs — not a key that unlocks code already sitting on your disk. User accounts are unlimited in the self-hosted engine: no edition of it enforces a seat cap, and the binary's seat seam is an unconditional no-op. The hosted Cloud tier is the one exception — its control plane admits seats per tenant, which is a property of that service and not of this binary.

## License by directory (the frontier)

| Path           | License                        | SPDX identifier                  |
|----------------|--------------------------------|----------------------------------|
| `/core`        | GNU AGPL v3.0                  | `AGPL-3.0-only`                  |
| `/modules`     | GNU AGPL v3.0                  | `AGPL-3.0-only`                  |
| `/web`         | GNU AGPL v3.0                  | `AGPL-3.0-only`                  |
| `/sdk`         | Apache License 2.0             | `Apache-2.0`                    |
| `/connectors`  | Apache License 2.0             | `Apache-2.0`                    |
| `/clients`     | Apache License 2.0             | `Apache-2.0`                    |
| `/enterprise` (separate private repository — not in this repo) | Olivares.AI Commercial License | `LicenseRef-Olivares-Commercial`|

Why two open-source licenses:

- **Core, modules and web are AGPL** so the network-use copyleft (AGPL §13)
  closes the SaaS free-rider gap: anyone who modifies Olivares AI and offers it
  as a network service must publish their modified source.
- **The SDK and connector interfaces are Apache-2.0** so the community can build
  and ship connectors and integrations with zero copyleft friction. The moat is
  the breadth of the connector ecosystem; a copyleft SDK would suppress it.

The full license texts live in the repository:

- `LICENSE` (repo root), `core/LICENSE`, `modules/LICENSE`, `web/LICENSE`
  — GNU AGPL v3.0 text.
- `sdk/LICENSE`, `connectors/LICENSE` — Apache License 2.0 text, plus the repository-root `NOTICE`.
- `LICENSES/LicenseRef-Olivares-Commercial.txt` — the commercial terms notice/stub
  (the operative contract is the agreement executed at purchase; the `enterprise/`
  tree itself is maintained in a separate private repository).
- `LICENSES/` — canonical copies of every license text used in the repo, as
  required by the [REUSE specification](https://reuse.software/).

Every source file carries an SPDX header from the first commit:

```go
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
```

CI verifies the header on every source file (`scripts/check-spdx.sh`); a missing
or wrong header fails the build.

### Why `AGPL-3.0-only` (not `-or-later`)

All contributions are licensed to Olivares.AI under the CLA (see *Contributing*),
whose outbound-license clause — Harmony outbound option five, “Any License”, the
selection this project requires on every signed form — lets Olivares.AI relicense
them, including adopting a future AGPL version if one is published. That
relicensing authority comes from the CLA's license grant (contributors retain
copyright in their work — see [`CLA.md`](CLA.md)), **not** from the public grant's
version clause. `-only`
therefore costs us nothing in flexibility while giving the dual-licensor more
control: downstream users are bound to AGPLv3 exactly and cannot unilaterally
move the public grant to an unvetted future AGPL version. It is also the literal
reading of the project's stated choice ("AGPLv3") and matches the dual-licensed
projects we model on (e.g. Grafana ships `AGPL-3.0-only`). The commercial
exception is a separate private contract and is unaffected by this choice.

## Warranty and liability

**The free software comes with no warranty of any kind, and nobody accepts liability for what
happens when you run it — including loss of your data.** That is not a formality on a control
plane: a misconfiguration can block legitimate work and interrupt production, or let through
exactly what you meant to stop.

- **AGPL-3.0-only material** is disclaimed by sections 15 and 16 of the licence
  (`LICENSES/AGPL-3.0-only.txt`, lines 587–596 and 598–608 — section 16 names *"LOSS OF DATA OR
  DATA BEING RENDERED INACCURATE"* at lines 603–605).
- **Apache-2.0 material** is disclaimed by sections 7 and 8
  (`LICENSES/Apache-2.0.txt`, lines 144–152 and 154–164).
- On top of both, the project states its own supplemental disclaimer — the term AGPL-3.0-only
  section 7(a) expressly permits (`LICENSES/AGPL-3.0-only.txt`, lines 349–354) — covering data
  loss and corruption, business interruption, lost profits and revenue, substitute-procurement
  costs, high-risk uses, compliance outcomes and third-party components.

**Read [`DISCLAIMER.md`](DISCLAIMER.md) before you deploy.** It is the full text, it protects
every contributor as well as Olivares AI, and it expressly does **not** restrict any right the
open licenses grant you.

**Commercial subscriptions are a separate contract.** Whatever warranty, support and liability
terms a paid offering carries are those of the agreement executed at purchase. They never attach
to code you obtained under the open licenses, and no commercial agreement modifies the open
licenses' own disclaimers or any right you hold under them.

## Buying the commercial exception

You need the commercial license if you cannot or do not want to meet
the AGPL obligations — e.g. an internal policy that forbids AGPL, embedding in a
closed-source product, or running a modified network service without publishing
your changes. Using any of the `enterprise/` add-ons also requires the
corresponding add-on entitlement, independently of AGPL compliance. Otherwise,
use the free AGPL build.

- **Commercial license, add-ons, custom terms, support — dedicated contact:**
  **enterprise@olivares.ai**

The support tiers and first-response model — best-effort response targets, not
penalty-backed SLAs — are published in [`SUPPORT.md`](SUPPORT.md).
The **Enterprise** relationship also covers commercial/legal terms — data-residency
*architecture support*, dedicated deployment, and **indemnification** — which are
contractual, never features of the binary (residency itself ships in the open product;
indemnification terms are pending a legal decision). None of these contractual terms
is a feature of the binary or gated by the license key. The **AGPL core** is never
gated by any license key; the terms that govern commercial add-ons are those of the
commercial agreement.

See `LICENSES/LicenseRef-Olivares-Commercial.txt` for the commercial terms summary.

### What the subscription does and does not call home for

Two different things get confused under "phone home", so this file states both. **Verifying a
licence never calls anyone. Downloading what you paid for does.**

- **Community (AGPL).** Licence validation is offline Ed25519. There is no remote kill switch.
  The AGPL kernel does not make a licence call — and no licence key gates it, ever.
- **Commercial.** The subscription is the credential with which modules, updates and patches are
  downloaded. Phone-home is approved for licence issuance and updates. There is no mandatory
  telemetry and no control-plane egress by default.

That is the shape of a subscription that grants access to the enterprise repositories — the model
this line was designed against — and not a licence that checks in on you while you work. What you
buy is the right to fetch and keep receiving the add-ons you paid for; what you run answers to
nobody at runtime.

## Trademarks

"Olivares AI"™, "Olivares.AI" and the project logos are trade marks of **Francisco
Olivares**. A registration has been applied for and is pending grant; until it is
granted the marks are used with **™** and never with **®**.

The software licenses govern the code, not the marks — with one precision stated
rather than hidden: **Apache-2.0 section 6** (`LICENSES/Apache-2.0.txt`, lines
139–142) grants no trade mark permission *"except as required for reasonable and
customary use in describing the origin of the Work and reproducing the content of
the NOTICE file"*, and that excepted use is granted by the licence itself — no
Olivares AI policy restricts it. AGPL-3.0-only grants no trade mark rights.
Beyond that, you may use, modify and redistribute the code under its license, but
you may not use the Olivares AI name or logos to brand a fork or a competing
service in a way that implies endorsement. (Contact `enterprise@olivares.ai` for
permitted uses.)

## Contributing

Contributions are accepted under the **Developer Certificate of Origin**
(`DCO`): sign off every commit with `git commit -s`. The DCO check on pull
requests rejects commits without a `Signed-off-by` trailer (a required status
check, provisioned with the public repository).

Because Olivares AI is dual-licensed, external contributors are additionally
asked to sign the **Contributor License Agreement** (`CLA.md`, based on the
Harmony Agreements) before their first contribution is merged. This keeps the
chain of licensing authority clean so the commercial exception can continue to be
offered. See `CLA.md` for the individual and entity terms and how to sign.
