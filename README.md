<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Languages:** **English** · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Run and govern the AI you already use, on your own infrastructure.**

[What it is](#what-it-is) · [What it does](#what-it-does) · [Install](#install) · [Quickstart](#quickstart) · [Console](#a-look-inside-the-console) · [Editions](#editions-and-pricing) · [Documentation](#documentation) · [Security](#security) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10.0](https://img.shields.io/badge/release-26.10.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta.** 26.10.0 ships signed archives, native packages and container images. [Honesty & limits](docs-site/src/content/docs/start/honesty-and-limits.md) lists what runs today, what runs on demand and what is still design.

## What it is

Olivares AI is a self-hosted control plane for AI agents: one Go binary with the console built in. It gives agents context, access to resources and managed sessions, and gives you permissions, policies, budgets and evidence. There is no mandatory telemetry, and air-gapped installs are supported.

Claude Code connects through its `PreToolUse`/`PostToolUse` hook, managed settings and console launch and stop. The official Codex and Grok CLIs run as managed sessions. gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw, Hermes and self-hosted endpoints such as Ollama are connectors; each one states what it enforces and what it only observes.

## What it does

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Motion diagram of the read/write access map: agents, sessions and identities on the left, the resources they reach on the right, reads in blue, writes in orange, one observed write that was never permitted flagged as a drift finding.">
<br><sub><b>The access map</b> — what each agent reads and writes across your estate, permitted against observed.</sub>
</div>

- **See it.** An inventory of the agents, sessions, models, MCP servers, tools and identities that connectors observe; a read/write **access map** with a permitted-versus-observed **drift** view; live sessions, the orchestration graph, health and SLA. Access it cannot classify shows as `unknown`.
- **Run the work.** Work items with owners, dependencies, acceptance criteria and decisions; fenced leases, so two agents cannot hold the same item; Claude Code, Codex and Grok sessions launched, attached to, interrupted and stopped from the console; delegation to authorized peers over A2A.
- **Govern and enforce it.** A Cedar authorization engine and **four deny-closed enforcement points**: the Claude Code hook, an inline `/v1/messages` inference proxy, an MCP `tools/call` gate and an A2A delegation gate. An unauthorized action is blocked, held for two-person approval or rewritten before it runs. Budgets deny or throttle spend, break-glass needs two people, and the estate **kill-switch** fails closed.
- **Feed it, governed.** SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL and a root-confined filesystem feed governed retrieval; clearance is checked at retrieval time.
- **Prove it.** A hash-chained, Ed25519-signed audit ledger; sealed evidence mapped to **26 framework catalogs** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR and others) as self-assessed control families, not certifications; SIEM and ITSM push (CEF, LEEF, syslog, OTLP, OCSF); WebAuthn/FIDO2, PIV/CAC, SSO, SCIM, BYOK/CMEK and verified right to erasure, configured per deployment.

**31 modules**, one console, **159 integrations**, counted from the code by [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). The breakdown is in [`connectors/README.md`](connectors/README.md), and each module with its maturity in the [modules catalog](docs-site/src/content/docs/reference/modules/overview.md).

## Install

Pick one method. After it, `olivares quickstart` prints the console URL and a one-time setup token. Releases are cosign-signed with SLSA provenance and SBOMs; every method below verifies before it installs, and `scripts/verify-release.sh` checks a manual download (cosign and SHA-256, [how](INSTALL.md#verifying-a-release)). The engine starts with HTTPS, no default credentials and a single-use setup token.

**1 · One command, Linux and macOS.** The installer detects the OS and architecture, verifies the signed checksums and the archive SHA-256, installs only the binary and never runs `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

Add `--user` for a user service (systemd user unit or LaunchAgent), or run the script from a privileged shell with `--system --start` for a system service. The manual path and the per-OS matrix: [`INSTALL.md`](INSTALL.md).

**2 · Docker.** Multi-arch, distroless, non-root. The ports are published on every host interface; prefix the `-p` mappings with `127.0.0.1:` to keep them local.

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` is the same image; in production, pin it by digest. FIPS and STIG variants: [`INSTALL.md`](INSTALL.md#docker).

**3 · Docker Compose.** A hardened stack: SQLite on one node, with optional Postgres and backup.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes.** The Helm chart from this repository, or a flat manifest without Helm. The chart has no OCI release yet (`publication-unverified`).

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Linux packages.** `.deb`, `.rpm` and `.apk` on the [release page](https://github.com/olivaresai/olivares/releases/tag/26.10.0): the binary, an example env file, a no-login `olivares` user and a hardened unit. The service does not start on install.

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew.** macOS and Linux, checked against the signed checksums.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · From source.** Go 1.26+, [Task](https://taskfile.dev) and pnpm.

```sh
task build && ./bin/olivares quickstart
```

**Air-gapped:** bundle the signed image, chart and verification material, and verify offline with `scripts/verify-release.sh --key … --offline` ([how-to](docs-site/src/content/docs/how-to/air-gap-install.md)). **Windows** has no native build yet: use the Linux container or WSL2 ([plan](INSTALL.md#windows)). Upgrade and rollback: [how-to](docs-site/src/content/docs/how-to/upgrade-and-rollback.md).

## Quickstart

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

The demo password is public: do not use the demo with real data. The [full quickstart](docs-site/src/content/docs/start/quickstart.md) connects a real pgAudit source.

## A look inside the console

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Access map: what each agent reads and writes across your estate, origins on the left, resources on the right."></picture><br><sub><b>Access map</b> — origins on the left, resources on the right, read and write by colour.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Least-privilege drift: unexpected accesses and unused grants overlaid on the access map."></picture><br><sub><b>Least-privilege drift</b> — observed but not permitted, and grants nobody uses.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Claude Code sessions created, attached to and governed from the console."></picture><br><sub><b>Sessions</b> — create, attach to and govern sessions from the console, no SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Work: the durable cross-session backlog of work items and decisions."></picture><br><sub><b>Work</b> — the durable cross-session backlog: items, ownership, acceptance, decisions.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Security and forensics: guardrail findings, the anomaly queue and tamper-evident forensics."></picture><br><sub><b>Security &amp; forensics</b> — guardrail findings, anomalies, tamper-evident forensics.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: model spend, token usage, budgets and a run-rate projection."></picture><br><sub><b>FinOps</b> — spend by model and agent, budgets that deny or throttle, run-rate.</sub> |

The screens come from the demo estate on the running binary. All screens: the [console reference](docs-site/src/content/docs/reference/console.md).

## Editions and pricing

Community is the complete product under AGPL-3.0, with unlimited users and all four deny-closed enforcement points. Business and Enterprise add commercial code, built only with `-tags enterprise`; nothing in Community is removed or limited.

| Edition | Price | Includes |
|---|---|---|
| **Community** | Free, AGPL-3.0 | The complete self-hosted product. Unlimited users, one active identity provider. |
| **Business** | USD 129/month or USD 1,290/year | The commercial licence, the signed release channel, business-hours email support, and **Regulated Operations**, **AI Runtime Security**, **Compliance Packs** and **Identity & Scale**. Unlimited users; one legal entity; up to two production deployments, each with one staging deployment; up to five active identity providers. |
| **Enterprise** | Contract | More legal entities, deployments and identity providers, air-gapped mirrors, custom LTS and support terms, on an annual order form. |

Buying terms: [olivares.ai/pricing](https://olivares.ai/pricing). What is open and what is commercial: [`LICENSING.md`](LICENSING.md).

## Architecture

One static Go binary embeds the console and serves four interfaces: the REST API, a gRPC mirror of the stable core, the `olivares` CLI and a Terraform provider. Collectors run inside your infrastructure. The store is SQLite or Postgres with row-level security, enforced in the store API and again by Postgres. Details, including the work plane: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Documentation

[docs.olivares.ai](https://docs.olivares.ai) — tested install tutorials (single node, Docker Compose, Kubernetes/Helm, air-gapped), connector guides with real console captures, a cookbook (deny-closed policies, budgets, approvals, kill-switch drills, SIEM push), API reference and a glossary. Start at [What is Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). On the site: [product](https://olivares.ai/product) · [solutions](https://olivares.ai/solutions) · [how it works](https://olivares.ai/how-it-works) · [architecture](https://olivares.ai/architecture) · [security](https://olivares.ai/security) · [trust](https://olivares.ai/trust) · [compare](https://olivares.ai/compare) · [demo](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [status](https://olivares.ai/status) · [roadmap](https://olivares.ai/roadmap) · [brand](https://olivares.ai/brand) · [press](https://olivares.ai/press). Releases: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

## Security

Report a vulnerability privately through [`SECURITY.md`](SECURITY.md), not as a public issue. The access map stores edges, not payloads, and opening it is audited. Licence verification is offline; the AGPL core makes no licence call. Advisories: [`docs/security-advisories.md`](docs/security-advisories.md); supply-chain evidence: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Community

[`CONTRIBUTING.md`](CONTRIBUTING.md) (setup, DCO/CLA, SPDX, the connector boundary) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog, CalVer `YY.M.PATCH`).

## License

`core/`, `modules/` and `web/` are **AGPL-3.0-only**; `sdk/`, `connectors/` and `clients/` are **Apache-2.0**, and a connector never imports the engine. The commercial code is built only with `-tags enterprise` and is not in this repository. Commercial licensing: `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Contributions need a DCO sign-off (`git commit -s`) and the [CLA](CLA.md).

> **No warranty.** The software is provided **as is**, with **no warranty of any kind** and **no liability for loss of data, business interruption or lost profits**. AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 and this project's supplemental term apply — [`DISCLAIMER.md`](DISCLAIMER.md).

## Support the project

Sponsor the project through GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) or [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — or once on Ko-fi. Sponsorship is not a support contract ([`SUPPORT.md`](SUPPORT.md)); sponsors who ask to be named are listed in [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Ground truth for enterprise AI.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
