---
title: How this documentation is organized
description: >-
  These docs follow Diátaxis — four modes (tutorials, how-to guides, reference,
  explanation), each answering a different need. Here is how to navigate them.
---

This documentation is organized with the **[Diátaxis](https://diataxis.fr/start-here/)**
framework. Diátaxis observes that technical documentation serves four distinct
needs, and that mixing them makes docs worse for everyone. So the top of the
sidebar is **four modes**, not a list of product features:

| Mode | Orientation | Answers | When you are… |
|---|---|---|---|
| **[Tutorials](/tutorials/zero-to-graph/)** | learning | "Take me from nothing to a working result." | new, and want to learn by doing |
| **[How-to guides](/how-to/self-hosting/)** | a task | "How do I accomplish *this specific thing*?" | working, and need a recipe |
| **[Reference](/reference/)** | information | "What exactly are the API, events, modules, flags?" | building against it, and need precision |
| **[Explanation](/explanation/)** | understanding | "*Why* is it built this way?" | evaluating, and want the reasoning |

A quick map of where things live:

- **Tutorials** — the learning paths: [from zero to a read/write access
  graph](/tutorials/zero-to-graph/), and getting started per real scenario —
  [single node](/tutorials/getting-started/single-node/),
  [Docker Compose](/tutorials/getting-started/docker-compose/),
  [Kubernetes](/tutorials/getting-started/kubernetes/),
  [air-gapped](/tutorials/getting-started/air-gapped/).
- **How-to guides** — install & operate ([self-host](/how-to/self-hosting/),
  [backup & restore](/how-to/backup-and-restore/),
  [monitoring](/how-to/monitor-with-prometheus/),
  [troubleshooting](/how-to/troubleshooting/)), the
  [per-connector guides](/how-to/connectors/pgaudit/) (pgAudit, CloudTrail,
  eBPF, Claude Code, MCP, identity), and the
  [cookbook](/how-to/cookbook/deny-closed-policies/) of governance recipes
  (deny-closed policies, budgets, approvals, drift triage, the kill switch,
  SIEM push).
- **Reference** — the [REST API](/reference/api/) (rendered from the product's own
  OpenAPI 3.1 contract), the [API stability policy](/reference/api-stability/),
  the [event bus](/reference/events/) (an AsyncAPI 3.0
  contract), the [modules catalog](/reference/modules/overview/), the
  [CLI](/reference/cli/) and [configuration](/reference/configuration/).
- **Explanation** — the [architecture](/explanation/architecture/overview/), the
  [security model](/explanation/security/security-model/) and
  [threat model](/explanation/security/threat-model/), the
  [open-core licensing](/explanation/open-core-and-licensing/).

## Conventions

- **Search** is local and client-side (Pagefind). It runs entirely in your browser;
  nothing is sent to an external search service — consistent with the product's
  self-hosted design, where what crosses your perimeter is what you configure to
  cross it.
- **Versioned.** The documentation is versioned: when a new product version ships,
  the docs for the previous one are preserved. The version selector lives in the
  top bar.
- **Honest about limits.** Where a capability is design-stage, post-v1, or simply
  not built yet, the docs say so plainly. See
  [Honesty & limits](/start/honesty-and-limits/). Tutorial and how-to commands are
  meant to be **run as written**.
- **Languages.** The canonical documentation is in English; translations are
  available in Spanish, Simplified Chinese, Russian, Japanese, German and French
  (machine-translated, English-authoritative, falling back to English where not
  yet translated).
