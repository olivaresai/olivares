<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Languages:** **English** · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Run and govern the AI you already use — on your own infrastructure, with one ground truth.**

[What it is](#what-it-is) · [What it does](#what-it-does) · [Install](#install) · [Quickstart](#quickstart) · [Console](#a-look-inside-the-console) · [Editions](#editions-and-pricing) · [Documentation](#documentation) · [Security](#security) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: v26.9.0](https://img.shields.io/badge/release-v26.9.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/v26.9.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta**, in active development. **v26.9.0** ships signed archives, native packages and container images. What runs today, what is on-demand and what is design-stage is stated in [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md).

## What it is

Your AI estate today is coding agents, MCP servers, model endpoints, service accounts and scheduled jobs spread across machines that were never one system. Nobody can say, from one place, what is running, who launched it, what it reached, what it cost, and who agreed to it.

Olivares AI is **one self-hosted Go binary, console included**, that holds that estate together: it gives the AI what it needs to work (context, access to resources, managed sessions) and gives you the permissions, policies, budgets and evidence to run it. Self-hosted, no mandatory telemetry, air-gapped installs supported.

Claude Code is integrated at the deepest level (the `PreToolUse`/`PostToolUse` hook, managed settings, console launch and stop); the official Codex and Grok CLIs are first-class session drivers; gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw, Hermes and self-hosted endpoints such as Ollama are connectors, each stating what it can enforce and what it can only observe. The AGPL build is the whole product, never feature-capped from within; no plan counts users.

## What it does

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Motion diagram of the read/write access map: agents, sessions and identities on the left, the resources they reach on the right, reads in blue, writes in orange, one observed write that was never permitted flagged as a drift finding.">
<br><sub><b>The access map</b> — what each agent reads and writes across your estate, permitted against observed.</sub>
</div>

- **See it.** Inventory of every discovered agent, session, model, MCP server, tool and identity; a read/write **access map** with a Permitted-vs-Observed **drift** view; live sessions, the orchestration graph, health and SLA. What it cannot see is marked `unknown`, never guessed.
- **Run the work.** Durable work items with ownership, dependencies, acceptance criteria and decisions; fenced leases, so two agents cannot hold the same work at once; sessions of Claude Code, Codex and Grok launched, attached to, interrupted and stopped from the console; delegation to authorized peers over A2A.
- **Govern and enforce it.** A Cedar authorization engine and **four deny-closed enforcement points** — the Claude Code hook, an inline `/v1/messages` inference proxy, an MCP `tools/call` gate and an A2A delegation gate — so an unauthorized action is blocked, held for two-person approval or rewritten before it runs. Budgets that deny or throttle spend, break-glass with dual control, and an estate **kill-switch** that fails closed.
- **Feed it, governed.** Content sources (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, a root-confined filesystem) into governed retrieval, clearance enforced deny-closed at retrieval time.
- **Prove it.** A hash-chained, Ed25519-signed audit ledger; sealed evidence mapped to **26 framework catalogs** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…) — self-assessed control families, not certifications; SIEM/ITSM push (CEF/LEEF/syslog/OTLP/OCSF); WebAuthn/FIDO2, PIV/CAC, SSO, SCIM, BYOK/CMEK and verified right-to-erasure, configured per deployment.

**30 modules**, one console, **158 integrations** — counts derived from code and enforced on every push by [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh); the breakdown is in [`connectors/README.md`](connectors/README.md), every module with its maturity in the [modules catalog](docs-site/src/content/docs/reference/modules/overview.md).

## Install

Pick one method: one command installs, then `olivares quickstart` prints the console URL and the one-time setup token. Every release is cosign-signed with SLSA provenance and SBOMs; every path below verifies before it installs, and `scripts/verify-release.sh` checks a manual download (cosign + SHA-256, [how](INSTALL.md#verifying-a-release)). The engine is **secure by default**: loopback bind, HTTPS on first boot, no default credentials, a single-use setup token printed at first start.

**1 · One command, Linux and macOS** — the verified installer: detects OS and architecture, verifies the signed checksums and the archive SHA-256, installs only the binary, never runs `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

Add `--user` for a user service (systemd user unit or LaunchAgent), or run the verified script from a privileged shell with `--system --start` for a system service. Prefer to download, verify and run by hand? The manual binary path and the per-OS matrix: [`INSTALL.md`](INSTALL.md).

**2 · Docker** — multi-arch, distroless, non-root; published on every host interface (prefix the `-p` mappings with `127.0.0.1:` to keep it local).

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` is the same image; for production pin by digest. FIPS and STIG image variants: [`INSTALL.md`](INSTALL.md#docker).

**3 · Docker Compose** — a hardened stack, SQLite single-node with optional Postgres and backup.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes** — the Helm chart from the tree, or a flat Helm-free manifest; the chart is not published to an OCI registry yet.

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Linux packages** — `.deb`, `.rpm`, `.apk` from the [release page](https://github.com/olivaresai/olivares/releases/tag/v26.9.0): the binary, an example env file, a no-login `olivares` user and a hardened unit; the service is not started for you.

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew** — macOS and Linux, checked against the signed checksums.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · From source** — Go 1.26+, [Task](https://taskfile.dev), pnpm.

```sh
task build && ./bin/olivares quickstart
```

**Air-gapped**: bundle the signed image, chart and verification material and verify offline with `scripts/verify-release.sh --key … --offline` ([how-to](docs-site/src/content/docs/how-to/air-gap-install.md)). **Windows** is not built yet: run the Linux container or WSL2 ([plan](INSTALL.md#windows)). Upgrades and rollback: [how-to](docs-site/src/content/docs/how-to/upgrade-and-rollback.md).

## Quickstart

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

The demo seed is for learning only (public source-tree password): never point it at real data. CI walks the same path with `task smoke:quickstart` and asserts the access-map and drift counts (20 nodes / 13 edges, with 8 unexpected accesses and 2 unused grants). The [full quickstart](docs-site/src/content/docs/start/quickstart.md) wires a real pgAudit connector.

## A look inside the console

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right."></picture><br><sub><b>Access map</b> — origins on the left, resources on the right, read and write by colour.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Least-privilege drift</b> — observed but not permitted, and grants nobody uses.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>Sessions</b> — create, attach to and govern sessions from the console, no SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>Work</b> — the durable cross-session backlog: items, ownership, acceptance, decisions.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>Security &amp; forensics</b> — guardrail findings, anomalies, tamper-evident forensics.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: model spend, token usage, budgets and a run-rate projection."></picture><br><sub><b>FinOps</b> — spend by model and agent, budgets that deny or throttle, run-rate.</sub> |

Every still is a capture of the seeded demo estate served by the running binary. The full map of screens: the [console reference](docs-site/src/content/docs/reference/console.md).

## Editions and pricing

The AGPL build is the whole platform, never feature-capped from within. Commercial add-ons are additive code on top, never features removed; a subscription is the credential for downloading signed module packs. User accounts are unlimited in the self-hosted engine, and all four deny-closed enforcement points are open.

| Edition | Who it is for | What it adds |
|---|---|---|
| **Community** | Anyone. Free, AGPL-3.0, unlimited users. | The complete product, self-hosted. No licence gate on the core. |
| **Business** | An organization adopting it. Priced per deployment, never per seat. | Services and optional packs, not core features: the commercial licence, a maintained signed release channel, business-hours email support, and four optional add-ons: **Regulated Operations**, **Compliance Packs**, **AI Runtime Security** and **Identity & Scale** (which includes the session cockpit for the official tools). All four together is **Business Max**. |
| **Cloud** | Teams that want the same plane run for them, prepaid, on shared infrastructure. | A managed control plane with published caps. No cloud trial; the free option stays self-hosted Community. |
| **Enterprise** | Regulated, multi-entity, large-scale estates. | A contract, agreed by email and signed on an annual order form. |

Prices, the add-on matrix and the buying terms: [olivares.ai/pricing](https://olivares.ai/pricing). The open/commercial/planned matrix: [`LICENSING.md`](LICENSING.md).

## Architecture

One static Go binary embeds the console and exposes four surfaces: the REST API (primary), a focused gRPC mirror of the stable core, the `olivares` CLI and a Terraform provider. Collectors run inside your infrastructure; the store is SQLite or Postgres with row-level security, enforced once in the store API and again by Postgres. The full picture, work plane included: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Documentation

[docs.olivares.ai](https://docs.olivares.ai) — tested install tutorials (single node, Docker Compose, Kubernetes/Helm, air-gapped), connector guides with real console captures, a cookbook (deny-closed policies, budgets, approvals, kill-switch drills, SIEM push), API reference and a glossary. Start at [What is Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). On the site: [product](https://olivares.ai/product) · [solutions](https://olivares.ai/solutions) · [how it works](https://olivares.ai/how-it-works) · [architecture](https://olivares.ai/architecture) · [security](https://olivares.ai/security) · [trust](https://olivares.ai/trust) · [compare](https://olivares.ai/compare) · [demo](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [status](https://olivares.ai/status) · [roadmap](https://olivares.ai/roadmap) · [brand](https://olivares.ai/brand) · [press](https://olivares.ai/press). Releases: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

## Security

Report a vulnerability privately through [`SECURITY.md`](SECURITY.md), never as a public issue. The engine is read-first and minimal-data: the access map stores edges, not payloads, and opening it is a recorded action. Verifying a licence never calls us; the AGPL core makes no licence call. Advisory flow: [`docs/security-advisories.md`](docs/security-advisories.md); supply-chain evidence: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Community

[`CONTRIBUTING.md`](CONTRIBUTING.md) (setup, DCO/CLA, SPDX, the connector boundary) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog, CalVer `vYY.M.PATCH`).

## License

`core/`, `modules/` and `web/` are **AGPL-3.0-only**; `sdk/`, `connectors/` and `clients/` are **Apache-2.0**, and a connector never imports the engine. The commercial add-ons are separate, optional and closed — built only with `-tags enterprise`, never in this repository; commercial licensing: `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Contributions require a DCO sign-off (`git commit -s`) and the [CLA](CLA.md).

> **No warranty, no liability.** The software is provided **as is**, with **no warranty of any kind** and **no liability for loss of data, business interruption or lost profits**. On a control plane that is not a formality: a misconfiguration can block legitimate work or let through exactly what you meant to stop. AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 and this project's supplemental term apply — [`DISCLAIMER.md`](DISCLAIMER.md).

## Support the project

The core is free and stays free; keeping every release signed, verified and current is sustained work. Sponsor it through GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) or [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — or one-off on Ko-fi. Sponsorship is not a support contract ([`SUPPORT.md`](SUPPORT.md)); sponsors who ask to be named are listed in [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground truth for enterprise AI.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
