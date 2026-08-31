<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Languages:** **English** · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**The control plane for the AI you actually run.** Integrate it, put it to work, connect it to your systems, and govern every part of it — one self-hosted binary, from a home server to a regulated enterprise.

[Install](#install) ·
[Quickstart](#quickstart) ·
[Examples](examples/) ·
[Architecture](#architecture) ·
[Documentation](#documentation) ·
[Security](SECURITY.md) ·
[Contributing](CONTRIBUTING.md) ·
[olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

<!-- OpenSSF Best Practices Badge (self-certification).
     Registration at https://www.bestpractices.dev is pending (a maintainer action); the
     evidence map is in docs/openssf-badge.md. Once a project ID is assigned, uncomment:
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/PROJECT_ID/badge)](https://www.bestpractices.dev/projects/PROJECT_ID)
-->

</div>

> Status: **beta**, in active development. The engine runs end-to-end — a single static binary with the embedded console, ingesting real signals from the systems where your AI runs. APIs, schemas and the module surface may still change before 1.0, and some actuation seams (declared, deny-closed integration points) stay closed until provisioned (see [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md)). Releases are cut from this repository; the [install paths](#install) below publish with the first tagged release.

> Supply chain: releases are built on GitHub Actions with a signed trust chain per artifact type — archives ship with SPDX SBOMs and in-toto attestations, container images are cosign-signed with an image SBOM attestation, and every artifact (packages and chart included) is covered by the cosign-signed checksums manifest, plus an OpenVEX document and SLSA build provenance for the set. Verify any release with [`scripts/verify-release.sh`](scripts/verify-release.sh); the exact chain per artifact type, the air-gapped path and the Helm chart are documented in [`docs/RELEASE-VERIFICATION.md`](docs/RELEASE-VERIFICATION.md) and [`deploy/`](deploy/).

## What Olivares AI is

AI stopped being one chat window a while ago. What you actually run now is a small estate: coding agents in terminals, MCP servers, model endpoints, service accounts and scheduled jobs, spread across machines that were never designed to be one system. Nothing holds it together, so the ordinary questions get expensive to answer: what is running, who launched it, what it reached, what it cost, and who agreed to any of it.

**Olivares AI is the plane that holds it together.** It has two halves, and they ship in the same binary:

- **Run and connect it** — a durable plane for the work itself. Work items with ownership, dependencies, acceptance criteria and decisions; leases that make ownership an authority a stale holder cannot keep using; sessions launched, attached to and stopped from the console, with input to a live run; delegation to a remote peer over A2A; MCP as the tool surface; and governed content sources feeding retrieval. This is the half described in [The work plane](#the-work-plane) below, with each piece's state stated plainly.
- **See and govern it** — inventory of everything discovered, a read/write access map of what each agent and identity actually reaches, Cedar policy, deny-closed enforcement, budgets that can refuse spend, and a hash-chained signed ledger to prove all of it afterwards.

Neither half is decoration for the other. Governance without a work plane is a dashboard with nothing to act on; a work plane without governance is work nobody can account for afterwards.

**Multi-provider by design.** Claude Code is integrated at the deepest level — the `PreToolUse`/`PostToolUse` hook, managed settings, console launch and stop, per-subject model access — with Codex and Grok Build alongside as first-class command surfaces, and gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw and Hermes carried as their own connectors. Each one states what it can enforce and what it can only observe; none of them is the product's centre of gravity. Ollama and other self-hosted endpoints are inventoried and attributed through the local connector, which is read-only by design; the policy and budget rules bind where inference crosses the governed proxy, which is the only place they can bind at all.

**Who runs it.** The open build is the whole platform at every one of these sizes — the commercial add-ons are additive code on top of it, never a different product:

| You are | What that looks like |
|---|---|
| **Running a home server or a homelab network** | one binary, SQLite, a Docker volume, loopback-bound, no external service — the shipped Compose topology runs non-root and read-only inside 1 CPU and 1 GiB ([`deploy/compose/docker-compose.yml`](deploy/compose/docker-compose.yml)) |
| **A freelancer or independent consultant** | a tenant per client — every module operation is pinned to one — budgets that can deny or throttle before the invoice does, and a posture export you can hand over |
| **A professional or an advanced user** | the same engine an enterprise runs, with nothing withheld: the open build is the whole platform, so what you learn on your own box is what you operate at work |
| **An engineering team or a small business** | shared work items and leases, so two agents — or two people — cannot hold the same work item at once; SSO, roles, and an audit trail nobody has to assemble by hand |
| **A regulated enterprise** | Postgres with row-level security, HA with a single writer and standbys, air-gapped installs, evidence mapped to **26 framework catalogs**, and WORM archival on an immutable substrate |

Every row is the same build. Several of those capabilities — SSO, HA, WORM archival, budgets that actually deny — are things you **provision**, not defaults you get on first boot; the matrix below and [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md) say which is which, per capability.

It runs as a **single self-hosted Go binary** with the console embedded — on Linux, Docker, Kubernetes, on-prem or fully air-gapped. There is no mandatory telemetry and no control-plane egress by default: what crosses your perimeter is what you configure to cross it — calls to your model APIs, the SIEM/webhook outputs you wire, an external embedding provider if you provision one. Collectors read from the systems you already run (pgAudit, CloudTrail, eBPF, MCP, your IdP), so a failing collector never stands in the data path of production.

Coverage and attribution carry explicit tiers (`firm`/`approximate`/`unknown`, `clean`/`lossy`/`opaque`), enforcement is deny-closed where it's wired and a declared seam where it isn't, and the docs say plainly what runs today versus what is design-stage. The product will not fabricate certainty it cannot prove — see [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md).

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840"
       alt="Access map: What each agent reads and writes across your estate — origins on the left, the resources they touch on the right, R/RW by color.">
</picture>

<sub><b>Access map</b> — What each agent reads and writes across your estate — origins on the left, the resources they touch on the right, R/RW by color.</sub>

</div>

**See it yourself in two commands** (Go 1.26+, [Task](https://taskfile.dev), pnpm — [prerequisites](#quickstart-prerequisites)):

```sh
task build
./bin/olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 \
  --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps
```

CI walks the same path: `task smoke:quickstart` boots this demo estate against the real binary and asserts its access-map and drift counts. For installation paths and their operational defaults, use [Install](#install) and [Quickstart](#quickstart).

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840"
       alt="One binary at every size: a home server or homelab, a freelancer with a tenant per client, an engineering team or small business, and a regulated enterprise. It runs on Linux, Docker, Kubernetes, Helm and air-gapped, with managed cloud at launch, and it reaches model providers, clouds and directories, governed content sources and output connectors — with the access map as one capability among them rather than the centre.">
</picture>

<sub>The same build from a homelab to a regulated enterprise.</sub>
</div>

## The work plane

The plane that carries the work is the part of Olivares AI that agents and people share, and it is the part most often described as if it were finished everywhere. It isn't, so here is each piece with what actually holds it up and how far it reaches today.

| Piece | State | Where it lives |
|---|---|---|
| **Work items** — brief, provenance, dependencies, acceptance criteria, decisions, owner and event history, durable, with one command document shared by REST, CLI and in-process callers | **live, public API** | [`modules/sessions/work_model.go`](modules/sessions/work_model.go), routes in [`modules/sessions/work_api.go`](modules/sessions/work_api.go) |
| **Leases** — ownership as a fenced, expiring authority: acquire, renew, release, take over, revoke; a stale holder cannot keep acting, and concurrent acquisition yields exactly one winner | **live, public API** | [`modules/sessions/work_lease.go`](modules/sessions/work_lease.go) |
| **Messages, acks and handoffs** — durable conversation bound to a work item, with replay and stale-epoch rejection | **live behind an orchestration workflow; the general public inbox is deliberately not wired** | [`modules/sessions/communication_model.go`](modules/sessions/communication_model.go); the boot test that forbids wiring the public plane is [`cmd/olivares/communicationauthorityboot_test.go`](cmd/olivares/communicationauthorityboot_test.go) |
| **Launch for work** — reserve, take the lease, *then* spawn the session, persisting work/epoch/fence/execution so a retry is safe | **live through orchestration** | [`modules/sessions/runtime_work_launch.go`](modules/sessions/runtime_work_launch.go) |
| **Remote execution over A2A** — plan, test, start, observe and cancel work on an authorized peer, with durable receipts | **live, and only when a destination is configured**; with no authorized target the seam is not mounted at all | [`cmd/olivares/wire.go`](cmd/olivares/wire.go), [`cmd/olivares/orchremote.go`](cmd/olivares/orchremote.go) |
| **Shadow mode and final authority** — dual-report against the existing system and a comparator before the plane becomes authoritative | **not built** | design only |

Read that table as the honest version of "agents that talk to each other": work items and leases are ordinary API surface you can drive today; conversation between agents is real and durable but scoped to an orchestration workflow, and there is no general message bus for arbitrary agents; remote delegation works and refuses unknown peers. What does not exist is not listed as coming soon in the interface — it is listed here, as absent.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840"
       alt="How agents work together: agent surfaces feed a durable work plane of work items, fenced leases where one holder acts at a time, launch for work, and messages and acknowledgements scoped to a workspace. Delegation reaches an authorized peer through its enforcement gate. The plane emits an orchestration graph, an event bus, an access map with drift, and a signed ledger that reaches your SIEM. Shadow mode and final authority are drawn as a dashed box because they are not built.">
</picture>

<sub>Agents share one durable work plane. What is not built is drawn as absent.</sub>
</div>

## What it covers

One binary, **30 modules**, one console — across the whole footprint of your AI, not a single feature. Every capability carries an explicit maturity state — live, on-demand, observed, or a declared deny-closed seam — stated per item in [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md).

- **Run the work.** Durable work items, leases, orchestrated launch and A2A delegation as described in [The work plane](#the-work-plane); the console's Work view is the operator surface for the same store, and the Orchestration view draws the delegation topology from observed signals.
- **See it.** Inventory of every **discovered** agent, session, model, MCP server, tool and identity — coverage follows what you connect, carries explicit indicators, and marks what it cannot see as `unknown` instead of guessing; a read/write **access map** of what each one actually reaches, with a Permitted-vs-Observed **drift** view; live sessions, the orchestration graph, health and SLA.
- **Govern & enforce it.** A Cedar authorization engine (RBAC + deny-overlay + positive scoped grants) and **four deny-closed enforcement points** — the Claude Code `PreToolUse`/`PostToolUse` hook, an inline `/v1/messages` inference proxy, an MCP `tools/call` gate and an A2A delegation gate — so unauthorized actions do not execute: they are blocked, sent to two-person approval, or rewritten before they run. That adjective is measured, not asserted: a point counts only while a test drives its *unconfigured* path — no gate wired, an empty policy document, a policy store that will not answer — and asserts the refusal. The seam-to-proof census is [`scripts/enforcement-seams.tsv`](scripts/enforcement-seams.tsv); remove a proof and the count falls and the build fails. Policy reaches into the session itself: per-path and per-subtree allow/ask/deny rules in the hook, context-window budgets per surface and per group, and source scoping down to session, agent, user, group or role. Plus scoped admin and custom roles, break-glass with dual control, and an estate **kill-switch** that fails closed.
- **Claude & the agent ecosystem.** Govern Claude Code in the hook; launch, attach to, govern and stop Claude Code sessions and their workspace from the console; deliver enterprise managed-settings; govern which model each subject can use, on which surface; MCP (OAuth-gated resource server, posture, registry, `.mcpb`); A2A v1 between authorized peers; and surfaces for the agents your teams actually run — gemini-cli, Cursor, Codex CLI, opencode, goose, cline, OpenHands, OpenClaw and Hermes (enforcement where each surface exposes it, read-only posture observation where it doesn't; every connector states which) — plus Teams notifications with approval deep-links.
- **Feed it, governed.** The context side of the same coin: content sources (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, plus a root-confined filesystem source for local/NFS/SMB mounts) feed a governed RAG pipeline with working defaults — zero-egress lexical retrieval out of the box, model-backed semantic retrieval when you provision an embedding provider (Voyage, OpenAI-compatible or self-hosted; `embed_policy=model_backed` fails closed instead of silently degrading), per-source provenance, clearance and scoping enforced deny-closed at retrieval time — plus a data-product catalog with versioned contracts and quality gates. See [Governed data for Claude](docs-site/src/content/docs/how-to/governed-data-for-claude.md).
- **Identity & access.** Human identity (WebAuthn/FIDO2, PIV/CAC, AAL step-up) and **non-human identity** lifecycle; agent-identity federation (Entra Agent ID, AWS AgentCore, Google, SPIFFE/SPIRE); roster reconciliation from AD/LDAP/Okta/Entra/Vault/Infisical with SCIM.
- **Secure the data.** Inline guardrails (PII, prompt-injection, jailbreak), DLP egress, BYOK/CMEK envelope encryption across three KMS backends (AWS KMS, Google Cloud KMS, Azure Key Vault), privileged-session recording, right-to-erasure with verified key-shredding, retention and legal-hold, residency attestation, and TLS 1.3 hybrid post-quantum key establishment (X25519MLKEM768 when the peer supports it; signatures remain classical today).
- **Prove it.** A hash-chained, Ed25519-signed audit ledger; sealed, append-only compliance evidence mapped to **26 framework catalogs** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…); SIEM/ITSM push (CEF/LEEF/syslog/OTLP/OCSF).
- **Run it well.** FinOps budgets that can deny or throttle spend; calibrated LLM-judge evals with a blocking CI gate (on-demand — without a judge credential, runs report `SKIPPED`, never a silent pass); OS-isolated red-team sandboxes (gVisor/Firecracker; without a provisioned sandbox, runs report `DEGRADED`, never a fabricated pass); a connector-health dashboard with a public status page; backups and restore managed from the console.

Across **158 integrations** with the clouds, directories, secrets stores, model providers, agent surfaces, SIEMs and pipelines you already run — a count derived from code and enforced on every push by [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). The unit is the connector directory that carries Go code: of the 159 directories in the tree, 158 qualify, and the gate derives the figure that way on every push. Twelve of those are shared contract/library packages rather than capabilities — they are counted, and [`connectors/README.md`](connectors/README.md) carries the full breakdown of what each directory is. The full map of every capability and its maturity is in [`docs-site/`](docs-site/), and its own test suite gates it.

## What's open, what's enterprise, what's planned

This table maps each capability area to where it ships — the open (AGPL) build, or one of the separate, optional commercial add-ons; maturity per capability is stated honestly in [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md). The full list of reserved seams is declared in the public tree itself ([`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go)): a capability the open binary reserves answers `501` or no-ops, and its comment says so — nothing is hidden and nothing open is removed.

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

The AGPL build is the whole platform and is never feature-capped from within. The commercial add-ons are additive new code, never features removed from the open product. A subscription is the credential you download signed artifacts with — the SUSE model — not a key that unlocks code already sitting on your disk. User accounts are unlimited in the self-hosted engine: no edition of it enforces a seat cap, and the binary's seat seam is an unconditional no-op. The hosted Cloud tier is the one exception — its control plane admits seats per tenant, which is a property of that service and not of this binary. See [`LICENSING.md`](LICENSING.md) and [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md).

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840"
       alt="What each edition contains: the AGPL core is the whole platform and the add-ons are additive code on top of it. Community is the full AGPL product with unlimited users. Business adds commercial depth on reporting, onboarding, threat intelligence, PQC posture and NIS2. Regulated Operations adds a retention governor, a WORM audit archive, legal hold and erasure depth. Business Max is Business with all four add-ons. Cloud Standard is the managed service, with plan quotas that include service seats. A subscription is the credential you download signed artifacts with.">
</picture>

<sub>Editions by composition. Packaging and pricing on request.</sub>
</div>

## A look inside the console

<div align="center">

<img src=".github/assets/olivares-reel.gif" width="720" alt="A short reel cycling through real views of the Olivares AI console: access map, sessions, policies, FinOps and compliance.">

<sub>A few seconds of the real console. Every still below is a capture of the seeded demo estate served by the running binary — regenerate the raw captures yourself with <code>bash scripts/docs-captures.sh</code> (the curated set here is selected from its output).</sub>

</div>

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: Overlay the least-privilege diff: highlight unexpected access (observed, not permitted) and unused grants."></picture><br><sub><b>Least-privilege drift</b> — Overlay the least-privilege diff: highlight unexpected access (observed, not permitted) and unused grants.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="Orchestration &amp; A2A: Agent-to-agent topology — who delegates to whom, the live delegation flows, and declared cadences. Reads of the communication graph are privileged and self-audited."></picture><br><sub><b>Orchestration &amp; A2A</b> — Agent-to-agent topology — who delegates to whom, the live delegation flows, and declared cadences. Reads of the communication graph are privileged and self-audited.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="Inventory: Every agent, session, MCP, model and identity discovered across your estate."></picture><br><sub><b>Inventory</b> — Every agent, session, MCP, model and identity discovered across your estate.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/observability-dark.png"><img src="docs-site/public/console/observability-light.png" alt="Observability &amp; interop: Standards-based ingestion health and ledger-correlated trace drill-down. Figures are engine-wide (process-global), not per-tenant; standards are pinned to the versions and maturities the upstream bodies declare."></picture><br><sub><b>Observability &amp; interop</b> — Standards-based ingestion health and ledger-correlated trace drill-down. Figures are engine-wide (process-global), not per-tenant; standards are pinned to the versions and maturities the upstream bodies declare.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/dashboards-dark.png"><img src="docs-site/public/console/dashboards-light.png" alt="Executive overview: Cost, usage, risk and compliance at a glance — drill down to the operational view for the detail."></picture><br><sub><b>Executive overview</b> — Cost, usage, risk and compliance at a glance — drill down to the operational view for the detail.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/home-dark.png"><img src="docs-site/public/console/home-light.png" alt="Overview: Your AI estate at a glance — inventory, activity, risk, compliance, spend and health."></picture><br><sub><b>Overview</b> — Your AI estate at a glance — inventory, activity, risk, compliance, spend and health.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security &amp; forensics: Guardrail findings, the enforcement posture, the anomaly queue and tamper-evident incident forensics. The plane is detective by default — it records, it does not block on its own unless enforcement is enabled and governed."></picture><br><sub><b>Security &amp; forensics</b> — Guardrail findings, the enforcement posture, the anomaly queue and tamper-evident incident forensics. The plane is detective by default — it records, it does not block on its own unless enforcement is enabled and governed.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="Session Recording Viewer: Unified timeline of agent activity and governance evidence for a single session."></picture><br><sub><b>Session Recording Viewer</b> — Unified timeline of agent activity and governance evidence for a single session.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/identity-dark.png"><img src="docs-site/public/console/identity-light.png" alt="Identity &amp; NHI: SSO, SCIM, identity inventory, the NHI lifecycle, the WIF graph and privileged login — observed, governed and audited."></picture><br><sub><b>Identity &amp; NHI</b> — SSO, SCIM, identity inventory, the NHI lifecycle, the WIF graph and privileged login — observed, governed and audited.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/knowledge-dark.png"><img src="docs-site/public/console/knowledge-light.png" alt="Data, knowledge &amp; context: Governed knowledge bases, retrieval lineage, the prompt registry, agent memory and context policies."></picture><br><sub><b>Data, knowledge &amp; context</b> — Governed knowledge bases, retrieval lineage, the prompt registry, agent memory and context policies.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-apply-refused-dark.png"><img src="docs-site/public/console/work-apply-refused-light.png" alt="Plan: Planning the change. Nothing is written in this step."></picture><br><sub><b>Plan</b> — Planning the change. Nothing is written in this step.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="Kill switch: The estate emergency stop: one click halts every governed actuation surface. Engaging is deliberately cheap; recovery requires two distinct user accounts and a forced post-review."></picture><br><sub><b>Kill switch</b> — The estate emergency stop: one click halts every governed actuation surface. Engaging is deliberately cheap; recovery requires two distinct user accounts and a forced post-review.</sub> |

## Install

Every release ships under a **cosign-signed trust chain** — a cosign-signed checksums manifest covering every artifact, with the archives and static binaries covered transitively by it, an SBOM in-toto attestation per archive, cosign signatures directly on the container image — with an SBOM attestation for the container image — and on the Helm chart, and SLSA build provenance for the set. For a security product the supply chain is part of the trust model, so [verify it](docs/RELEASE-VERIFICATION.md) before you run it. The full per-OS matrix and production setup live in [`INSTALL.md`](INSTALL.md); the deployment tutorials (Compose, Kubernetes/Helm, air-gapped) live in [`docs-site/`](docs-site/).

The engine is **secure by default**: it binds to loopback, serves HTTPS with a self-signed certificate on first boot, ships with no default credentials, and prints a single-use setup token to the console. The first command you run is the secure one.

**From source** (the supported path until the first tagged release):

```sh
# Build the single binary (Go 1.26+, Task, pnpm — the web console is embedded).
task build

# Start it — one guided, secure-by-default command (TLS on, loopback-only, no
# default credentials). It prints your console URL and a one-time setup token.
./bin/olivares quickstart
```

**With the first release**, the recommended path becomes a single verified install — `.deb`/`.rpm`/`.apk` packages with a hardened systemd unit, a multi-arch Docker image, a Homebrew cask and a Helm chart, each covered by the release's cosign-signed checksums manifest (images signed directly), each installable in one step and still secure by default. These are not published yet; until the tag lands, build from source as above. **Windows** is not built yet — run the Linux container or build from source ([plan in `INSTALL.md`](INSTALL.md#windows)).

> Want to look around first, without wiring real sources? A synthetic estate runs on loopback in one command — see [Quickstart](#quickstart) below.

## Quickstart

Two ways in: explore a synthetic estate immediately, or point the engine at a real source. Both run the same real binary.

### Evaluate it in five minutes

1. Build with `task build` (Go 1.26+, Task, pnpm; see [prerequisites](#quickstart-prerequisites)).
2. Boot the demo estate with the exact command in step 2a below.
3. In the console, inspect the access map and its Permitted-vs-Observed drift (20 nodes / 13 edges, with 8 unexpected accesses and 2 unused grants), a Cedar policy and an approval flow, the compliance evidence view (26 framework catalogs), and a FinOps budget.
4. Then read what is real versus planned: the feature matrix above, [The work plane](#the-work-plane), and [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md).

<a name="quickstart-prerequisites"></a>
Prerequisites for building from source: Go 1.26+, [Task](https://taskfile.dev) (go-task) and pnpm (the web UI is embedded). See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the full development setup.

**1. Build:**

```sh
task build && ./bin/olivares version
```

**2a. Explore the demo estate** — synthetic observations through the real engine, loopback-only (it refuses non-loopback addresses), no real data:

```sh
./bin/olivares serve --seed-demo --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$(mktemp -d)"
```

Open `http://127.0.0.1:8901`, log in with the demo credentials in the boot banner, and walk the console — inventory, the access map and its drift, sessions, orchestration, policies, FinOps, compliance. The demo seed is for learning only (public source-tree password); never point it at real data.

**2b. Or start it for real** — one guided, secure-by-default command:

```sh
./bin/olivares quickstart        # TLS on, loopback; prints the console URL + a one-time setup token
```

Open the console at the printed URL and create your first administrator with the token — no curl, no extra steps. (`olivares serve` is the same engine with explicit flags, for production and containers.) Then connect a source. The [full quickstart](docs-site/src/content/docs/start/quickstart.md) wires a **real pgAudit connector** against a PostgreSQL audit log — no demo seed — and links the production install paths (systemd, Docker Compose, Kubernetes via [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml), air-gapped).

The demo estate is deterministic. The numbers are not aspirational — `task smoke:quickstart` walks this same path against the real binary (its own ports and data dir) and asserts the access-map and drift counts listed above, so this section cannot quietly drift from the code.

## Architecture

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840"
       alt="Architecture: agent surfaces, audit sources, MCP and A2A peers and content sources are collected in three modes into one self-hosted Go binary with the console embedded, which carries the product modules, the policy and enforcement layer and the signed evidence ledger over a tenant-scoped store; it serves the console, the REST API, a focused gRPC subset, the CLI and the Terraform provider, with the cloud control plane (built, not deployed) and the license portal (deployed, fulfilment off) as separate planes.">
</picture>
</div>

The engine is a single static Go binary (`olivares`) that embeds the web UI and exposes its capabilities over four surfaces, each with documented coverage: a REST API (the primary surface), a focused, frozen gRPC mirror of the stable core, the `olivares` CLI itself — 68 grouped top-level commands, from `quickstart` and `serve` to `work`, `orchestration`, `agent`, `mcp` and `compliance`, with a test that keeps the help groups total so a new command cannot land ungrouped — and a Terraform provider for the manage-as-code resources. Collectors run inside the customer's infrastructure in three modes: in-process fast-path sources, out-of-process plugins the engine supervises over an authenticated per-launch channel (AutoMTLS), and an opt-in remote collector→core deployment over verified-client-cert mutual TLS. The core stores data in SQLite (single-node, air-gap) or Postgres with row-level security, where every module operation is pinned to a tenant in the store API and Postgres enforces it again with FORCE row-level security. The application role is refused at boot if it is privileged enough to bypass that silently (superuser or `BYPASSRLS`), and the only way past the refusal is an explicit opt-in flag that names what it costs. Cross-tenant system reads go through a separate, least-privilege `BYPASSRLS` admin pool that is never used for tenant-scoped work — a declared door, not an absent one.

Overview: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Open core, by directory

Licensing is settled from the first commit: **open core** — the complete product under AGPL, a permissive SDK and connectors so the ecosystem can grow without copyleft friction, and a small set of **additive** commercial add-ons — built only with `-tags enterprise`, each licensed separately under commercial terms and absent from the public binary — for the reserved capabilities. The AGPL build is the whole governance platform and is never crippled to upsell; the commercial add-ons *add* new code that was never in the open product — so an enterprise build is not identical to the open one, while nothing is taken away from what ships open. Every source file carries an `SPDX-License-Identifier` header, enforced in CI.

| Directory | License | Contents |
|---|---|---|
| `core/` | `AGPL-3.0-only` | Engine: ingest, event bus, data model, module runtime, API, authn/z, audit, multi-tenancy |
| `modules/` | `AGPL-3.0-only` | The 30 product modules (inventory, access map, work and leases, identity, FinOps, evals, guardrails, …) |
| `web/` | `AGPL-3.0-only` | React UI, embedded into the binary via `go:embed` |
| `sdk/` | `Apache-2.0` | Stable `SourceConnector` / `OutputConnector` / `Module` interfaces + gRPC contract + types |
| `connectors/` | `Apache-2.0` | First-party and community connectors (Claude, MCP, pg-audit, eBPF, cloud, SIEM, …) |
| `clients/` | `Apache-2.0` | Generated client SDKs (Go, Java, Python, TypeScript) |
| Commercial add-ons *(separate private repo)* | `LicenseRef-Olivares-Commercial` | Additive, separately-licensed add-on families across enforcement, MCP, identity, data security, compliance depth, operations and platform — enumerated per area in [the matrix above](#whats-open-whats-enterprise-whats-planned), every one a declared seam in [`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go) — built only with `-tags enterprise`, never in this repository or the public binary |
| `docs/`, `docs-site/` | — | Design documents and the product documentation site |

A connector may import only from `sdk/`, never from `core/`. This keeps the AGPL / Apache boundary clean and lets third parties write connectors without copyleft obligations — enforced by [`scripts/check-boundary.sh`](scripts/check-boundary.sh) in CI.

## Security & supply chain

Olivares AI runs on customer hosts and maps what each agent can touch, so the security bar is high by design: read-first; minimal data in the observation plane (the access map stores edges, not payloads — the governed Knowledge store holds only the content you explicitly ingest); least privilege; mTLS; append-only hash-chained audit with signed checkpoints; signed releases. The access map itself is a privileged, audited surface — opening it is a recorded action, and so is reading the agent-to-agent communication graph.

To report a vulnerability or read the disclosure policy, see [`SECURITY.md`](SECURITY.md) (private reporting — never a public issue). The advisory flow is documented in [`docs/security-advisories.md`](docs/security-advisories.md); supply-chain readiness evidence lives in the Best Practices map in [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Documentation

Product documentation lives in [`docs-site/`](docs-site/) — a Diátaxis site with tested install tutorials (single node, Docker Compose, Kubernetes/Helm, air-gapped), per-connector guides with real console captures, a cookbook (deny-closed policies, budgets, approvals, kill-switch drills, SIEM push), API reference and a glossary. Start at [What is Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md) and [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md) — the page that says plainly what runs today, what is design-stage, and what the product deliberately does not do.

## Community & governance

The community-health and governance files an adopter expects are present and current:

- **How decisions are made:** [`GOVERNANCE.md`](GOVERNANCE.md) (maintainer-led / open-core, honest about the project's stage) and [`.github/CODEOWNERS`](.github/CODEOWNERS) (review routing mapped to the license frontier).
- **Contributing:** [`CONTRIBUTING.md`](CONTRIBUTING.md) (setup, DCO/CLA, SPDX, the connector boundary) — every change is sent via the [pull-request template](.github/PULL_REQUEST_TEMPLATE.md).
- **Conduct:** [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) (Contributor Covenant 2.1).
- **Getting help:** [`SUPPORT.md`](SUPPORT.md) — and where **not** to report security issues.
- **Changes:** [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog 1.1 + CalVer `vYY.M.PATCH`; beta).

## License

The product (`core/`, `modules/`, `web/`) is licensed under the **GNU Affero General Public License, version 3** (`AGPL-3.0-only`). The connector SDK, connectors and client SDKs (`sdk/`, `connectors/`, `clients/`) are licensed under **Apache-2.0**. Which license governs a given file is stated by its SPDX header, and for a release by its SBOM.

> **No warranty, no liability — read this before you deploy.** The free software is provided **as is**, with **no warranty of any kind** and **no liability for loss of data, corruption, business interruption or lost profits**. That is not a formality on a control plane: a misconfiguration can block legitimate work and interrupt production, or let through exactly what you meant to stop. AGPL-3.0-only §§15–16 and Apache-2.0 §§7–8 apply, plus this project's own supplemental term under AGPL §7(a) — the full text, including high-risk uses, compliance outcomes and third-party components, is in [`DISCLAIMER.md`](DISCLAIMER.md).

A **commercial license** provides a private exception to the AGPL for organizations that cannot operate under its terms. The additive `enterprise/` capabilities — the add-on families enumerated per area in [the matrix above](#whats-open-whats-enterprise-whats-planned), each a declared seam in the public tree — are offered as **separate, optional add-ons** under their own commercial terms: closed code built only with `-tags enterprise`, never present in the open binary. Packaging and pricing on request. The AGPL core itself is complete and is never feature-capped from within. For commercial licensing or enterprise inquiries, contact `enterprise@olivares.ai`. See [`LICENSING.md`](LICENSING.md).

Contributions require a DCO sign-off (`git commit -s`) and a Contributor License Agreement; see [`CONTRIBUTING.md`](CONTRIBUTING.md) and [`CLA.md`](CLA.md).

## Support the project

Olivares AI is AGPL-3.0 and self-hosted: the core is free and stays free. If it is useful to you and you would like to support the work directly, you can sponsor it through this repository's **Sponsor** button.

Sponsorship is **not** a support contract and buys no priority: for how questions and bug reports are handled, see [`SUPPORT.md`](SUPPORT.md); for commercial terms and the enterprise add-ons, see [`LICENSING.md`](LICENSING.md).

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground truth for enterprise AI.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
