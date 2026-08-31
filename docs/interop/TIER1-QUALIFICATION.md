<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Tier-1 interop qualification

**A connector count is not a compatibility claim.** The test corpus is large and the
deterministic wire fixtures are valuable, but the measured *absence* of live qualification
does not prove compatibility with a real implementation. This page — and its
machine-readable source `connectors/interop/tier1-matrix.json` — declare the small,
explicit subset of connectors we make a *supportable* compatibility statement about, and
each one's **real** verification level. Everything not listed, or listed as `fixture-only`,
ships fixture-tested only and must never carry an equivalent "verified" badge.

## Verification badges

| Badge | Meaning |
|-------|---------|
| `fixture-only` | Unit-test wire fixtures only; no live qualification job. No compatibility guarantee beyond the pinned wire shape. This is the default for every connector not in the matrix. |
| `conformance-defined` | An `//go:build integration` conformance job exists and is designed to run against a reference/vendor endpoint per release. `last_verified` reflects the last **actual** run and is `null` until the first one. |
| `continuously-verified` | A conformance job exists **and** ran green against a live reference/vendor endpoint on `last_verified`, within the deprecation SLA. |

The escalation is one-directional and evidence-gated: a badge above `fixture-only` requires
a real integration job to exist, and `continuously-verified` additionally requires a dated
run. `connectors/interop/matrix_test.go` enforces this in the default gate (`task test`), so
the matrix can never claim a job that does not exist.

## Running the qualification jobs

Conformance jobs are **not** in the default gate — Actions is OFF in this repo, so they run
per-release, locally:

```
task test:integration            # compiles + runs the integration-tagged jobs
```

With no endpoint configured every job **skips cleanly** (proving the jobs compile and do not
rot). To actually qualify, provide the endpoint(s) from the matrix `conformance.env` and run
the job; then record the run date in the entry's `last_verified` and, once a job runs green
against a live endpoint, promote the badge to `continuously-verified`.

Credential-safe by design: no endpoint or secret is ever committed; SaaS canaries run only
against vendor **sandboxes** where the terms permit, and skip when no sandbox credential is
present.

## Breakage & deprecation policy

- A Tier-1 surface whose conformance job detects breakage against its reference/vendor
  endpoint is **downgraded to `fixture-only`** and its `last_verified` cleared within one
  release, announced in the release notes.
- Deprecating a supported surface gives at least **`deprecation_sla_days`** (see the matrix
  policy) notice, naming the removal release.

## Current matrix

The canonical, machine-readable matrix is `connectors/interop/tier1-matrix.json`. As of this
writing the qualified open-protocol subset is **A2A** (agent-card discovery) and **MCP**
(server introspection), both `conformance-defined` with the jobs in
`connectors/a2a/conformance_integration_test.go` and
`connectors/mcp/conformance_integration_test.go`; `slack` and `pagerduty` are listed
explicitly as `fixture-only` so their real level is not mistaken for Tier-1. The measured
count of integration-tagged conformance jobs is derived from the same census the
conformance matrix gates on (`connectors/interop/matrix_test.go`).
