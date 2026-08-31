<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Product Modules

The 30 product modules that compose the Olivares AI governance platform. Each module is a self-contained unit that registers its own schema, API routes, permissions, event subscriptions and background workers through the SDK module interface.

**License:** AGPL-3.0-only.

## Modules

| Module | Purpose |
|---|---|
| `access-map` | Read/write access map with Permitted-vs-Observed drift detection |
| `capabilities` | Capability overlay — governance metadata over declared capabilities |
| `catalog` | Agent/model/provider discovery and reference catalog |
| `claudeadoption` | Claude Code adoption dashboard and usage analytics |
| `compliance` | 26 framework catalogs (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, …) + evidence |
| `consoleviews` | Saved console views — named, shareable view-state snapshots (server-side, audited) |
| `deploy` | Deployment lifecycle, canary, rollback |
| `evals` | Calibrated LLM-judge evaluations with a blocking CI gate |
| `eventing` | Durable event bus (at-least-once delivery, DLQ, replay) |
| `finops` | FinOps budgets, chargeback, cost centers, rate catalog |
| `governance` | Cedar authorization engine, scoped grants, custom roles, delegation |
| `health` | Health checks, liveness/readiness probes, SLA tracking |
| `inferenceproxy` | Inline `/v1/messages` inference proxy (deny-closed enforcement) |
| `inventory` | Agent, session, model, MCP server, tool and identity inventory |
| `knowledge` | Knowledge base governance with retrieval scope gates |
| `liveingest` | Live signal ingest (streaming connectors, voice probes) |
| `models` | Model access governance, model groups, workspace-scoped grants |
| `notify` | Notification channels (email, Slack, Teams, webhook) |
| `observability` | Traces, spans, ingestion health, OTLP export |
| `orchestration` | Multi-agent orchestration graph and session lineage |
| `posture-export` | Posture export (OCSF, STIX, vendor-neutral) |
| `recording` | Privileged session recording |
| `redteam` | Red-team sandbox executor (gVisor/Firecracker isolation) |
| `reporting` | Scheduled reports and dashboards |
| `sandbox` | OS-isolated sandbox management |
| `security` | Security event correlation and alerting |
| `sessions` | Session lifecycle, stream transport, workspace scoping |
| `siemforward` | SIEM/ITSM push (CEF, LEEF, syslog, OTLP, OCSF) |
| `sourcescope` | Source→workspace scoping, connector assignments, workspace connectors |
| `voice` | Voice probe ingestion and analysis |

## Adding a module

Modules implement the `sdk.Module` interface. See [`modules/example/`](example/) for a minimal scaffold and the [SDK documentation](../sdk/README.md).
