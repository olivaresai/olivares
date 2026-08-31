<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Evaluation report template — proof-of-value results

Use this template with the [evaluation guide](./evaluation-guide.md). Enter `pass`,
`fail` or `blocked` in every Result cell and link or identify the retained evidence.

> **Criteria scope:** this report covers the numbered criteria in the evaluation
> guide. It does not refer to, or imply a count of, the compliance framework catalogs
> available in the product.

## Evaluator and environment metadata

| Field | Value |
|---|---|
| Evaluation organization |  |
| Evaluator name |  |
| Evaluator role |  |
| Evaluation sponsor |  |
| Evaluation start date |  |
| Evaluation end date |  |
| Report date |  |
| Product version |  |
| Source commit |  |
| License tier | Community / Enterprise evaluation |
| Deployment topology |  |
| Operating system and architecture |  |
| CPU and memory allocation |  |
| Data store | SQLite / Postgres |
| Identity provider |  |
| Connected sources |  |
| Evidence repository or location |  |
| Known scope exclusions |  |

## Measured time-to-hero

Measure from the start of installation through the first successfully verified audit
evidence export. Include operator actions and investigation time.

| Milestone | Started at | Completed at | Measured time | Evidence or notes |
|---|---|---|---|---|
| Install and boot |  |  |  |  |
| First drift finding |  |  |  |  |
| First deny |  |  |  |  |
| First evidence export |  |  |  |  |
| **Time-to-hero total** |  |  |  |  |

## Phase 1 — Deploy and verify supply chain

| ID | Criterion | Result (pass/fail/blocked) | Evidence | Notes |
|---|---|---|---|---|
| 1.1 | Build from source |  |  |  |
| 1.2 | Boot the demo estate |  |  |  |
| 1.2b | Secure first run (no demo) |  |  |  |
| 1.3 | Verify cosign signature |  |  |  |
| 1.4 | Check SBOM attestation |  |  |  |
| 1.5 | Inspect security.txt |  |  |  |
| 1.6 | FIPS build (optional) |  |  |  |

**Phase 1 verdict:** Pass / Fail / Blocked

**Phase 1 verdict rationale:**


## Phase 2 — Governance loop

| ID | Criterion | Result (pass/fail/blocked) | Evidence | Notes |
|---|---|---|---|---|
| 2.1 | Explore the access map |  |  |  |
| 2.2 | Write a Cedar deny policy |  |  |  |
| 2.3 | Configure Claude Code hooks PEP |  |  |  |
| 2.4 | Trigger HITL approval flow |  |  |  |
| 2.5 | Test estate kill switch |  |  |  |
| 2.6 | Export audit ledger |  |  |  |
| 2.7 | Wire a SIEM sink |  |  |  |
| 2.8 | Create a FinOps budget |  |  |  |
| 2.9 | Run a guardian loop |  |  |  |
| 2.10 | Configure managed settings for Claude Code |  |  |  |

**Phase 2 verdict:** Pass / Fail / Blocked

**Phase 2 verdict rationale:**


## Phase 3 — Compliance and enterprise readiness

| ID | Criterion | Result (pass/fail/blocked) | Evidence | Notes |
|---|---|---|---|---|
| 3.1 | Export OSCAL evidence bundle |  |  |  |
| 3.2 | Review compliance dashboard |  |  |  |
| 3.3 | Wire identity source (OIDC) |  |  |  |
| 3.4 | Backup/restore cycle |  |  |  |
| 3.5 | Verify there is NO user cap |  |  |  |
| 3.6 | SSO enforcement (enterprise) |  |  |  |
| 3.7 | Content firewall (enterprise) |  |  |  |
| 3.8 | Multi-IdP federation (enterprise) |  |  |  |
| 3.9 | Review honesty-and-limits page |  |  |  |

**Phase 3 verdict:** Pass / Fail / Blocked

**Phase 3 verdict rationale:**


## Findings

Use repository-relative `file:line` references where a finding concerns source or
documentation. Add rows as required.

| Finding ID | Severity | Reproduction | File:line | Related criterion | Disposition |
|---|---|---|---|---|---|
| F-001 |  |  |  |  |  |

## Final verdict

| Field | Value |
|---|---|
| Phase 1 verdict | Pass / Fail / Blocked |
| Phase 2 verdict | Pass / Fail / Blocked |
| Phase 3 verdict | Pass / Fail / Blocked |
| Final verdict | Approve / Approve with conditions / Reject / Evaluation blocked |
| Conditions or blockers |  |
| Risk acceptance owner |  |
| Follow-up date |  |

### Executive rationale


### Required remediation or procurement conditions


### Evaluator sign-off

| Field | Value |
|---|---|
| Name |  |
| Role |  |
| Decision |  |
| Date |  |

