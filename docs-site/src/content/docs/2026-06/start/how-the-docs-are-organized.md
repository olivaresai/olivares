---
title: How this documentation is organized
description: These docs follow Diátaxis — four modes (tutorials, how-to guides,
  reference, explanation), each answering a different need. Here is how to
  navigate them.
slug: 2026-06/start/how-the-docs-are-organized
---

This documentation is organized with the **[Diátaxis](https://diataxis.fr/start-here/)**
framework. Diátaxis observes that technical documentation serves four distinct
needs, and that mixing them makes docs worse for everyone. So the top of the
sidebar is **four modes**, not a list of product features:

| Mode | Orientation | Answers | When you are… |
|---|---|---|---|
| **[Tutorials](/2026-06/tutorials/zero-to-graph/)** | learning | "Take me from nothing to a working result." | new, and want to learn by doing |
| **[How-to guides](/2026-06/how-to/self-hosting/)** | a task | "How do I accomplish *this specific thing*?" | working, and need a recipe |
| **[Reference](/2026-06/reference/)** | information | "What exactly are the API, events, modules, flags?" | building against it, and need precision |
| **[Explanation](/2026-06/explanation/)** | understanding | "*Why* is it built this way?" | evaluating, and want the reasoning |

A quick map of where things live:

* **Tutorials** — the learning paths: [from zero to a read/write access
  graph](/2026-06/tutorials/zero-to-graph/), and getting started per real scenario —
  [single node](/2026-06/tutorials/getting-started/single-node/),
  [Docker Compose](/2026-06/tutorials/getting-started/docker-compose/),
  [Kubernetes](/2026-06/tutorials/getting-started/kubernetes/),
  [air-gapped](/2026-06/tutorials/getting-started/air-gapped/).
* **How-to guides** — install & operate ([self-host](/2026-06/how-to/self-hosting/),
  [backup & restore](/2026-06/how-to/backup-and-restore/),
  [monitoring](/2026-06/how-to/monitor-with-prometheus/),
  [troubleshooting](/2026-06/how-to/troubleshooting/)), the
  [per-connector guides](/2026-06/how-to/connectors/pgaudit/) (pgAudit, CloudTrail,
  eBPF, Claude Code, MCP, identity), and the
  [cookbook](/2026-06/how-to/cookbook/deny-closed-policies/) of governance recipes
  (deny-closed policies, budgets, approvals, drift triage, the kill switch,
  SIEM push).
* **Reference** — the [REST API](/reference/api/) (rendered from the product's own
  OpenAPI 3.1 contract), the [API stability policy](/2026-06/reference/api-stability/),
  the [event bus](/2026-06/reference/events/) (an AsyncAPI 3.0
  contract), the [modules catalog](/2026-06/reference/modules/overview/), the
  [CLI](/2026-06/reference/cli/) and [configuration](/2026-06/reference/configuration/).
* **Explanation** — the [architecture](/2026-06/explanation/architecture/overview/), the
  [security model](/2026-06/explanation/security/security-model/) and
  [threat model](/2026-06/explanation/security/threat-model/), the
  [open-core licensing](/2026-06/explanation/open-core-and-licensing/).

## Conventions

* **Search** is local and client-side (Pagefind). It runs entirely in your browser;
  nothing is sent to an external search service — consistent with the product's
  self-hosted, data-stays-home design.
* **Versioned.** The documentation is versioned: when a new product version ships,
  the docs for the previous one are preserved. The version selector lives in the
  top bar.
* **Honest about limits.** Where a capability is design-stage, post-v1, or simply
  not built yet, the docs say so plainly. See
  [Honesty & limits](/2026-06/start/honesty-and-limits/). Tutorial and how-to commands are
  meant to be **run as written**.
* **Languages.** The canonical documentation is in English; a Spanish locale is
  available and falls back to English for pages not yet translated.
