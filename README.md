<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Languages:** **English** · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Run the AI your team already uses, with the same control you have over the rest of your infrastructure.**

[What it does](#what-it-does) · [Install](#install) · [Console](#a-look-inside-the-console) · [Editions](#editions-and-pricing) · [Documentation](#documentation) · [Community](#community) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10](https://img.shields.io/badge/release-26.10-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

Your developers work with Claude Code and Codex. Agents call MCP servers, models and internal APIs, and scheduled jobs run on their own. Each piece keeps its own logs and its own permissions, so simple questions have no quick answer: which agent changed this file, who approved it, what did AI cost us this month?

Olivares AI answers them in one place. It connects to the agents and tools you already use, shows what each one does, applies your rules before an action runs, and keeps a signed record of everything. It is a single program that runs on your own servers, and the complete product is free and open source.

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Motion diagram of the read/write access map: agents, sessions and identities on the left, the resources they reach on the right, reads in blue, writes in orange, one observed write that was never permitted flagged as a drift finding.">
<br><sub><b>The access map</b> — what every agent reads and writes, and the one write nobody allowed.</sub>
</div>

## What it does

- **Know what is running.** Every agent, session, model, MCP server and tool in one inventory. The access map shows what each one reads and writes, and flags access that no rule allows.
- **Stop an action before it does damage.** Olivares AI has **four deny-closed enforcement points** that check each action before it runs: inside Claude Code, in the model proxy, at every MCP tool call and between agents. A risky action waits for a second person, a forbidden one does not run, and one switch stops every agent at once. When a check cannot decide, the action does not run.
- **Keep AI spending in check.** Budgets per team, agent or model warn, slow down or stop spending before the invoice arrives.
- **Give agents your company's knowledge, safely.** Connect SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3 and PostgreSQL. Each agent sees only what the person behind it is allowed to see.
- **Keep work going across sessions.** Tasks, owners and decisions stay when a session ends. Start, join and stop Claude Code, Codex and Grok sessions from the browser, without SSH.
- **Show proof when someone asks.** Every decision goes into a signed log that cannot be changed afterwards, and your security team and auditors read their reports from that record, with evidence mapped to 26 framework catalogs.

It works with the tools you already have: Claude Code, Codex, Grok, Cursor, gemini-cli, opencode, OpenHands and local models through Ollama. **31 modules** and **159 integrations**, all in the free edition: [every module](docs-site/src/content/docs/reference/modules/overview.md) · [every connector](connectors/README.md).

## Install

Pick one method and copy its block. At the end, `olivares quickstart` prints the console address and a one-time token to create the first administrator. Every release is signed, and each method verifies what it downloads before it installs it ([verify a download yourself](INSTALL.md#verifying-a-release)).

**Linux and macOS, one command.** Detects your system, verifies the release, installs only the binary and never uses `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart
```

**Docker.** Multi-arch, distroless, non-root. It listens on every host interface; put `127.0.0.1:` before each `-p` to keep it local.

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

**Docker Compose.** SQLite on one node, with optional Postgres and backups.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**Kubernetes.** The Helm chart from this repository (the chart has no OCI release yet: `publication-unverified`).

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
```

Without Helm:

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**Debian and Ubuntu.** The package adds a no-login `olivares` user and a hardened service; you start it.

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.0/olivares_26.10.0_linux_amd64.deb
sudo dpkg -i olivares_26.10.0_linux_amd64.deb && sudo systemctl enable --now olivares
```

**RHEL, Fedora and SUSE.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.0/olivares_26.10.0_linux_amd64.rpm
sudo rpm -i olivares_26.10.0_linux_amd64.rpm && sudo systemctl enable --now olivares
```

**Alpine.**

```sh
curl -fsSLO https://github.com/olivaresai/olivares/releases/download/26.10.0/olivares_26.10.0_linux_amd64.apk
sudo apk add --allow-untrusted olivares_26.10.0_linux_amd64.apk && sudo rc-service olivares start
```

On ARM servers, use `arm64` instead of `amd64`. Every file of the release: the [release page](https://github.com/olivaresai/olivares/releases/tag/26.10.0).

**Homebrew.** macOS and Linux.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**From source.** Go 1.26+, [Task](https://taskfile.dev) and pnpm.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
task build && ./bin/olivares quickstart
```

**Offline networks:** bundle the signed image, chart and verification material, then [install air-gapped](docs-site/src/content/docs/how-to/air-gap-install.md). **Windows** has no native build yet: use the Docker image or WSL2. Upgrades and rollback: [how-to](docs-site/src/content/docs/how-to/upgrade-and-rollback.md). Every option in detail: [`INSTALL.md`](INSTALL.md).

**Try it first with demo data**, on your machine only (the demo password is public):

```sh
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
```

Then open http://127.0.0.1:8901.

## A look inside the console

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right."></picture><br><sub><b>Access map</b> — who reads and writes what.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Drift</b> — access nobody allowed, and permissions nobody uses.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>Sessions</b> — start, join and stop agent sessions from the browser.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>Work</b> — tasks, owners and decisions that outlast a session.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>Security</b> — blocked actions, anomalies and a record that cannot be altered.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: model spend, token usage, budgets and a run-rate projection."></picture><br><sub><b>Spend</b> — cost by model and agent, budgets and forecast.</sub> |

Every screen: the [console reference](docs-site/src/content/docs/reference/console.md).

## Editions and pricing

Community is the complete product, free and open source. Business adds what a company needs to run it in production. Enterprise is for groups with larger or regulated estates.

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **Price** | Free, AGPL-3.0 | USD 129/month or USD 1,290/year | Annual contract |
| **What you get** | The complete product: unlimited users and all four deny-closed enforcement points | Everything in Community, plus Regulated Operations, AI Runtime Security, Compliance Packs and Identity & Scale, the commercial licence, signed updates and email support | Everything in Business, plus more companies, deployments and identity providers, offline mirrors and support terms agreed with you |
| **Scope** | One active identity provider | One company, two production deployments with one staging each, five identity providers | Agreed in the contract |

**Regulated Operations** keeps records for as long as the law asks, with legal hold and archives nobody can alter. **AI Runtime Security** filters what agents send, receive and run. **Compliance Packs** produce ready evidence for ISO 42001, DORA and NIS 2. **Identity & Scale** connects several identity providers at once and grows with larger deployments.

[Compare editions and buy](https://olivares.ai/pricing) · [What is open and what is commercial](LICENSING.md)

## Architecture

One Go binary with the console built in. It serves a REST API, a gRPC API, the `olivares` command line and a Terraform provider. Collectors run inside your network, and data stays in SQLite or PostgreSQL on your servers. [How it fits together](ARCHITECTURE.md).

## Documentation

[docs.olivares.ai](https://docs.olivares.ai) has install guides, a guide for every connector, recipes for common policies and the API reference. Start with [What is Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). What runs today and what is still planned: [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md). Releases: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

On the website: [product](https://olivares.ai/product) · [solutions](https://olivares.ai/solutions) · [how it works](https://olivares.ai/how-it-works) · [architecture](https://olivares.ai/architecture) · [security](https://olivares.ai/security) · [trust](https://olivares.ai/trust) · [compare](https://olivares.ai/compare) · [demo](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [status](https://olivares.ai/status) · [roadmap](https://olivares.ai/roadmap) · [brand](https://olivares.ai/brand) · [press](https://olivares.ai/press).

## Security

Found a vulnerability? Report it privately through [`SECURITY.md`](SECURITY.md). Olivares AI records which agent touched which resource, not the content, and opening that record is itself logged. Licences are checked offline; the open-source core never calls us.

## Community

Contributions are welcome. [`CONTRIBUTING.md`](CONTRIBUTING.md) explains the setup, the sign-off and how connectors fit in. [Code of conduct](CODE_OF_CONDUCT.md) · [Support](SUPPORT.md) · [Governance](GOVERNANCE.md) · [Changelog](CHANGELOG.md).

## Support the project

Olivares AI is built in the open. If it helps you, sponsor its development on GitHub Sponsors — [olivaresai](https://github.com/sponsors/olivaresai) or [fran-olivares](https://github.com/sponsors/fran-olivares) — or buy us a coffee on Ko-fi. Sponsors who want to be named appear in [`SUPPORTERS.md`](SUPPORTERS.md). Sponsorship is not a support contract ([`SUPPORT.md`](SUPPORT.md)).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

## License

The engine, modules and console are **AGPL-3.0-only**; the SDK, connectors and clients are **Apache-2.0**. Commercial code is built separately and is not in this repository; commercial licensing: `enterprise@olivares.ai`. Contributions need a DCO sign-off (`git commit -s`) and the [CLA](CLA.md).

> Provided **as is**, without warranty of any kind and without liability for loss of data, business interruption or lost profits. See [`DISCLAIMER.md`](DISCLAIMER.md).

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground truth for enterprise AI.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
