<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Languages:** **English** · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Integrate, manage and secure the AI you actually run — from one self-hosted binary.**

[Install](#install) · [Quickstart](#quickstart) · [Examples](examples/) · [Documentation](#documentation) · [Security](#security) · [Contributing](CONTRIBUTING.md) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta**, in active development. The first tagged release, **v26.8.0**, ships signed archives, native packages and container images. APIs and the module surface may still change before 1.0; what runs today, what is on-demand and what is design-stage is stated in [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md) and, per module, in the [modules catalog](docs-site/src/content/docs/reference/modules/overview.md).

## What it is

What you run now is an estate — coding agents, MCP servers, model endpoints, service accounts, scheduled jobs — spread across machines that were never one system. Olivares AI is the single self-hosted Go binary, console included, that holds it together: it gives the AI what it needs to work (context, access to resources, managed sessions) and gives you the permissions, policies, budgets and evidence to know what is running, who launched it, what it reached, what it cost, and who agreed to it.

**Multi-provider by design.** Claude Code is integrated at the deepest level — the `PreToolUse`/`PostToolUse` hook, managed settings, console launch and stop, per-subject model access — with Codex and Grok Build as first-class command surfaces alongside, and gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw and Hermes as their own connectors, each stating what it can enforce and what it can only observe. Ollama and other self-hosted endpoints are inventoried through the local connector, which is read-only by design.

**Who runs it.** The same build at every size: a home server (one binary, SQLite, loopback-bound); a freelancer with a tenant per client and budgets that deny before the invoice does; an engineering team with shared work items, SSO and an audit trail nobody assembles by hand; a regulated enterprise with Postgres row-level security, HA, air-gapped installs and WORM archival. The open build is the whole platform, and the commercial add-ons are additive code on top of it, never features removed; SSO, HA, WORM and budgets that actually deny are things you provision, not first-boot defaults.

There is no mandatory telemetry and no control-plane egress by default: what crosses your perimeter is what you configure to cross it — calls to your model APIs, the SIEM/webhook outputs you wire, an embedding provider if you provision one. Collectors read from the systems you already run, so a failing collector never stands in the data path of production.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840" alt="One binary at every size, from a home server to a regulated enterprise; where it runs and what it reaches.">
</picture>
<sub>The same open build from a homelab to a regulated enterprise.</sub>
</div>

## What it does

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right.">
</picture>
<sub><b>Access map</b> — what each agent reads and writes across your estate, read and write by colour.</sub>
</div>

- **See it.** Inventory of every discovered agent, session, model, MCP server, tool and identity; a read/write **access map** of what each one actually reaches, with a Permitted-vs-Observed **drift** view; live sessions, the orchestration graph, health and SLA. What it cannot see is marked `unknown`, never guessed.
- **Run the work.** Durable work items with ownership, dependencies, acceptance criteria and decisions; fenced leases, so two agents — or two people — cannot hold the same work at once; sessions launched, attached to and stopped from the console; delegation to authorized peers over A2A. Shadow mode and final authority are not built and are listed as absent: [The work plane](docs-site/src/content/docs/explanation/work-plane.md).
- **Govern and enforce it.** A Cedar authorization engine and **four deny-closed enforcement points** — the Claude Code hook, an inline `/v1/messages` inference proxy, an MCP `tools/call` gate and an A2A delegation gate — so an unauthorized action is blocked, held for two-person approval or, in the hook, rewritten before it runs; a point counts only while a test drives its unconfigured path and asserts the refusal. Budgets that deny or throttle spend, break-glass with dual control, and an estate **kill-switch** that fails closed.
- **Feed it, governed.** Content sources (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, a root-confined filesystem) into governed retrieval: zero-egress lexical retrieval out of the box, model-backed semantic retrieval when you provision an embedder, clearance enforced deny-closed at retrieval time.
- **Prove it.** A hash-chained, Ed25519-signed audit ledger; sealed evidence mapped to **26 framework catalogs** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…) — self-assessed control families, not certifications; SIEM/ITSM push (CEF/LEEF/syslog/OTLP/OCSF). Configured per deployment: human and non-human identity (WebAuthn/FIDO2, PIV/CAC, single-IdP SSO, SCIM reconciliation, agent-identity federation), inline guardrails, DLP, BYOK/CMEK encryption and right-to-erasure with verified key-shredding.

**30 modules**, one console, **158 integrations** — counts derived from code and enforced on every push by [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). An integration is a connector directory with Go code, and twelve of them are shared library packages: [`connectors/README.md`](connectors/README.md) has the breakdown. Every module with its maturity: the [modules catalog](docs-site/src/content/docs/reference/modules/overview.md); the wired connectors by fidelity tier: the [connectors reference](docs-site/src/content/docs/reference/connectors.md).

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840" alt="How agents work together: one durable work plane of work items, fenced leases and scoped messages; delegation through an enforcement gate; shadow mode and final authority drawn dashed because they are not built.">
</picture>
<sub>Agents share one durable work plane. What is not built is drawn as absent.</sub>
</div>

## A look inside the console

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>Claude Code</b> — create, attach to and govern sessions from the console, no SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>Work</b> — the durable cross-session backlog: items, ownership, acceptance, decisions.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="Orchestration and A2A: the agent-to-agent delegation graph derived from observed signals."></picture><br><sub><b>Orchestration &amp; A2A</b> — who delegates to whom, derived from observed signals.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="Inventory: every agent, session, MCP server, model and identity discovered across the estate."></picture><br><sub><b>Inventory</b> — every agent, session, MCP server, model and identity discovered.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Least-privilege drift</b> — observed but not permitted, and grants nobody uses.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>Security &amp; forensics</b> — guardrail findings, anomalies, tamper-evident forensics.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="Kill switch: the estate emergency stop with dual-control recovery."></picture><br><sub><b>Kill switch</b> — one click halts every governed actuation surface; recovery takes two accounts.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="Session recording viewer: agent activity and governance evidence on one timeline, chain verified."></picture><br><sub><b>Session recording</b> — agent activity and governance evidence on one timeline, chain verified.</sub> |

Every still is a capture of the seeded demo estate served by the running binary (`bash scripts/docs-captures.sh` regenerates the raw set). The full map of screens: the [console reference](docs-site/src/content/docs/reference/console.md).

## Install

Every release ships under a cosign-signed trust chain, verified by artifact type: a cosign-signed checksums manifest covering the archives, packages and per-archive SBOMs it lists, an SPDX SBOM sidecar with an in-toto attestation per archive, cosign signatures on the container image with its own SBOM attestation, and OpenVEX statements and SLSA build provenance for the set. For a security product the supply chain is part of the trust model: [verify it](docs/RELEASE-VERIFICATION.md) before you run it.

**HTTPS convenience path.** The script body arrives over HTTPS and is not pre-verified by the pipe; once running, it detects your OS and architecture, requires `cosign`, verifies the signed checksum manifest and the archive SHA-256, installs only the binary, and never invokes `sudo`. Pin the version when piping it into a shell:

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh -s -- --version v26.8.0
olivares quickstart        # TLS on, loopback-only, no default credentials; prints the console URL + a one-time setup token
```

**High-assurance path.** Download first, verify, then execute: the archives, packages and the checksums manifest are on the [release page](https://github.com/olivaresai/olivares/releases/tag/v26.8.0), and [`scripts/verify-release.sh`](scripts/verify-release.sh) verifies whatever is present and says what it skipped — keyless by default, `--key … --offline` on a disconnected host. The [installer trust contract](docs/RELEASE-INSTALLER.md) states both paths; the signed, versioned installer with its opt-in service adapter starts with the first release cut after it landed, and v26.8.0 predates it.

| Path | What you get |
|---|---|
| **Linux packages** — `.deb`, `.rpm`, `.apk` | the binary, a hardened systemd unit, an example env file and a no-login `olivares` service user; the service is not started for you |
| **Container** — `docker.io/olivaresai/olivares:26.8.0` | distroless, non-root, tags without a `v` prefix; `ghcr.io/olivaresai/olivares` is the same image by digest. The default image is multi-arch (amd64/arm64); the `-fips` and `-stig` variants are amd64 only |
| **Homebrew** — `brew install olivaresai/tap/olivares` | the release binary on macOS and Linux, checked against the signed checksums, with the Gatekeeper quarantine cleared; the darwin builds are not Apple-notarized yet |
| **Kubernetes** — [`deploy/helm/olivares`](deploy/helm/olivares) or [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml) | the Helm chart source and a flat, Helm-free manifest in the tree; the chart is **not yet published to an OCI registry** |
| **From source** — `task build` (Go 1.26+, [Task](https://taskfile.dev), pnpm) | `./bin/olivares quickstart`, the same secure-by-default first run |

The engine is **secure by default**: it binds to loopback, serves HTTPS with a self-signed certificate on first boot, ships with no default credentials and prints a single-use setup token; in a container or a pod the process listens on its own network and the host mapping or the Service keeps it private. **Windows** is not built yet — run the Linux container or WSL2 ([plan](INSTALL.md#windows)). The per-OS matrix and production setup: [`INSTALL.md`](INSTALL.md); the deployment guides (Compose, Kubernetes, air-gapped) and [upgrades](docs-site/src/content/docs/how-to/upgrade-and-rollback.md): [`docs-site/`](docs-site/).

## Quickstart

Explore a synthetic estate, or start it for real. Both run the same binary.

```sh
# a deterministic demo estate — loopback-only, no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, loopback; create the first administrator with the printed token
olivares quickstart
```

The demo seed is for learning only (public source-tree password): never point it at real data. CI walks the same path with `task smoke:quickstart` and asserts the access-map and drift counts (20 nodes / 13 edges, with 8 unexpected accesses and 2 unused grants), so this page cannot quietly drift from the code. The [full quickstart](docs-site/src/content/docs/start/quickstart.md) wires a real pgAudit connector and links the production install paths.

## Editions

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840" alt="Editions by composition: the AGPL core is the whole platform, the add-ons are additive code on top, Cloud Standard is the managed service.">
</picture>
<sub>Editions by composition. Packaging and pricing on request.</sub>
</div>

The AGPL build is the whole platform and is never feature-capped from within; the commercial add-ons are additive code, never features removed from the open product. A subscription is the credential for downloading signed module packs — a distribution-style model, not a key that unlocks code already on your disk. User accounts are unlimited in the self-hosted engine, and all four deny-closed enforcement points are open. The area-by-area matrix of open, commercial and planned capabilities: [`LICENSING.md`](LICENSING.md) and [Open core & licensing](docs-site/src/content/docs/explanation/open-core-and-licensing.md).

## Architecture

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840" alt="Architecture: agent surfaces, audit sources, MCP and A2A peers and content sources collected into one self-hosted binary serving the console, the REST API, gRPC, the CLI and the Terraform provider; the cloud control plane (built, not deployed) and the license portal (deployed, fulfilment off) drawn as separate planes.">
</picture>
</div>

One static Go binary embeds the console and exposes four surfaces with documented coverage: the REST API (primary), a focused gRPC mirror of the stable core, the `olivares` CLI and a Terraform provider. Collectors run inside your infrastructure in three modes; the store is SQLite or Postgres with row-level security, enforced once in the store API and again by Postgres. Details, including the work plane piece by piece: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Documentation

[docs.olivares.ai](https://docs.olivares.ai) — tested install tutorials (single node, Docker Compose, Kubernetes/Helm, air-gapped), connector guides with real console captures, a cookbook (deny-closed policies, budgets, approvals, kill-switch drills, SIEM push), API reference and a glossary. Start at [What is Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md) and [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md).

## Security

Report a vulnerability privately through [`SECURITY.md`](SECURITY.md), never as a public issue. The engine is read-first and minimal-data: the access map stores edges, not payloads, and opening it is a recorded action. Advisory flow: [`docs/security-advisories.md`](docs/security-advisories.md); supply-chain evidence map: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Community

[`CONTRIBUTING.md`](CONTRIBUTING.md) (setup, DCO/CLA, SPDX, the connector boundary) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) (Contributor Covenant 2.1) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog 1.1, CalVer `vYY.M.PATCH`).

## License

`core/`, `modules/` and `web/` are **AGPL-3.0-only**; `sdk/`, `connectors/` and `clients/` are **Apache-2.0**, and a connector never imports the engine. The commercial add-ons are separate, optional and closed — built only with `-tags enterprise`, never in this repository or the open binary; for commercial licensing contact `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Contributions require a DCO sign-off (`git commit -s`) and the [CLA](CLA.md).

> **No warranty, no liability.** The software is provided **as is**, with **no warranty of any kind** and **no liability for loss of data, business interruption or lost profits**. On a control plane that is not a formality: a misconfiguration can block legitimate work or let through exactly what you meant to stop. AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 and this project's supplemental term apply — [`DISCLAIMER.md`](DISCLAIMER.md).

## Support the project

The core is free and stays free; keeping every release signed, verified and current is sustained work. If Olivares AI is useful to you, you can sponsor it through GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) or [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — or one-off on Ko-fi. Sponsorship is not a support contract and buys no priority ([`SUPPORT.md`](SUPPORT.md)); sponsors who ask to be named are listed in [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground truth for enterprise AI.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
