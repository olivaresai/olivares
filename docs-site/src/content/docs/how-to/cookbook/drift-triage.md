---
title: "Recipe: triage least-privilege drift"
description: >-
  Work a Permitted-vs-Observed result to zero: classify unexpected accesses,
  unused grants and reconciliation-pending edges, decide each one (grant,
  revoke, or fix identity), and re-check — without trusting a single hint.
sidebar:
  order: 4
---

**Goal:** turn the drift result — the gap between what agents *may* do and
what they *are observed* doing — into decisions, on a cadence, until the diff
is quiet.

## 1. Pull the drift

```bash
curl -ks "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

(Or in HCL, for review in a PR: the Terraform data source
`olivares_access_edges` with `include_drift = true` —
[manage as code](/how-to/manage-as-code/).)

The result has three classes, and they are different problems:

| Class | Meaning | The question to ask |
|---|---|---|
| **Unexpected access** | observed, but no grant covers it | is this a missing grant, or a real violation? |
| **Unused grant** | granted, never observed exercised | why does this permission exist? |
| **Reconciliation pending** | observed, but the agent↔identity link is unresolved | an identity problem, not (yet) a security one |

## 2. Triage each class

**Unexpected access** — read the edge's honesty axes before acting:

- `attribution_tier: firm` + `coverage_tier: clean` is the highest-quality
  finding you will get: a specific identity touched a specific resource and
  the store's own audit classified it. Decide: if legitimate, declare the
  grant (policy or binding) so the map reflects intent; if not, revoke the
  underlying access and treat it as an incident.
- `approximate` attribution means the *access* happened but the *who* is a
  shared credential. Do not burn an investigation on "which agent was it" —
  the durable fix is
  [per-agent identity](/how-to/connectors/sso-scim-identity/), and until
  then the edge honestly says what it cannot prove.
- An edge resting only on an `mcp_annotation` hint is **not evidence** —
  the hint is untrusted by spec. Corroborate with an observed source before
  deciding anything.

**Unused grants** are over-provisioning found for free: each one is a
candidate revocation, with the caveat that absence of observation is only
meaningful where coverage exists — check the resource's coverage tier before
celebrating ([tiered coverage](/how-to/connect-a-source/#tiered-coverage--be-realistic)).

**Reconciliation pending** routes to the identity backlog: wire or fix the
roster source that should bind that credential, and the edge resolves on the
next pass.

## 3. Decide, record, re-check

Make the decision where it is governed: declare grants as code
([Terraform](/how-to/manage-as-code/)) or via the governed API, gate the
risky direction behind an [approval](/how-to/cookbook/hitl-approvals/), and
let the ledger record who decided what. Then re-pull the drift: reconciled
edges drop out of the diff — only genuine gaps remain. That convergence is
the whole point; the demo estate shows it in miniature
([quickstart](/start/quickstart/)).

In the console, the **Access map**'s *Permitted vs observed* panel is this
recipe rendered live.

## Cadence

Drift triage works as a short weekly loop plus an alert path for the
high-signal class (firm + clean unexpected writes). Route those findings to
your on-call via a [notification destination](/how-to/forward-audit-to-splunk/)
rather than waiting for the weekly pass.
