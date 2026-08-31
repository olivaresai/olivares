<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Evaluation kit

The Olivares AI evaluation kit is a buyer-operated proof-of-value package for
platform engineering, security, compliance and procurement teams. It takes an
evaluator from the shortest verified product path through a structured assessment
and a decision-ready results report. The kit exercises the real binary; the demo
estate supplies synthetic data, while the engine, enforcement and audit trail remain
the production code paths.

## Order of use

1. [Quickstart](../../docs-site/src/content/docs/start/quickstart.md) — build the
   binary and reproduce the permitted-versus-observed drift result on the demo estate
   and a real pgAudit connector.
2. [Adopter checklist](./adopter-checklist.md) — record the operational path from
   install to the first drift finding, first deny and first verified evidence export.
3. [Evaluation guide](./evaluation-guide.md) — execute the phased proof of value and
   retain evidence for every numbered criterion.
4. [Evaluation report template](./evaluation-report-template.md) — record results,
   findings, measured time-to-hero and the procurement verdict.

## Prerequisites

| Requirement | Evaluation baseline |
|---|---|
| Hardware | 2 CPU cores and 4 GB RAM |
| Operating system | Linux amd64 or arm64 |
| Go | 1.26+ when building from source |
| Task | [taskfile.dev](https://taskfile.dev), used as the build runner |
| pnpm | Required for the embedded web console build |
| Operator tooling | A shell with `curl` and Python 3 for the quickstart commands |

Optional tooling and infrastructure depend on evaluation scope: `cosign` for release
signature verification, Docker for the container path, and Postgres for HA topology
evaluation. Enterprise criteria require an enterprise evaluation license.

## Expected duration

The quickstart documents an approximately five-minute human path to the first drift
result. Record the adopter path's actual time-to-hero rather than treating that target
as a result. The complete evaluation guide is structured as a 10-business-day proof
of value: deployment and supply-chain verification on days 1–2, the governance loop
on days 3–6, and compliance and enterprise readiness on days 7–10.

Do not advance past a failed supply-chain or startup gate without documenting the
blocker. Each later result should point to retained evidence in the completed report.

