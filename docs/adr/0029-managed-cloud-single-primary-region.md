<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# ADR-0029: Managed-cloud regions — one primary region, residency answered by self-hosting

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0027 (managed-cloud ingress), ADR-0028 (managed-cloud database),
  ADR-0020 (enterprise private-repo distribution), ADR-0024 (DDIL offline semantics and
  signed bundles); the platform decision record for the managed cloud.

## Context and problem statement

Two questions have to be answered together, because answering one badly forces a bad
answer to the other: **where does the managed plane run**, and **what is said to a
customer who asks where their data lives**.

The temptation is to pick the region that makes the second question easy — a region whose
jurisdiction reads well in a compliance section — and accept whatever latency that
implies for the actual customers. That is the wrong order. It also rests on a
misconception worth writing down once, in a durable place, so nobody re-derives it:
**where the bytes are stored does not determine which data-protection law applies.**
Serving data subjects in a jurisdiction brings that jurisdiction's law with it,
regardless of hosting location.

## Decision drivers

- Latency to the customers the product is actually sold to.
- The compliance evidence an enterprise buyer asks for, which is largely evidence about
  the **infrastructure provider**, not about the region.
- Not paying the fixed cost of a second region — and the permanent complexity of
  cross-region data handling — before a customer requires it.
- Having a truthful, non-evasive answer for a customer with a hard residency requirement.

## Considered options

- **A — a single primary region in the target market**, with a second region as a
  demand-gated project.
- **B — two regions from launch**, one per major market.
- **C — a primary region chosen for regulatory narrative** rather than for customer
  latency.

## Decision outcome

Chosen option: **A — a single primary region, sited in the target market (United States
East)**. A second region is a project that a paying requirement opens, not a launch item.
Per-tenant region pinning and cross-region replication are deliberately out of scope of
the first managed release.

A customer with a **contractual or regulatory residency requirement that the primary
region does not satisfy** is served by the **self-hosted edition** — which is the
product's primary shape, runs in the customer's own infrastructure, and answers the
residency question completely rather than partially. This is not a workaround; it is the
stronger answer, and it is available on day one.

### Consequences

- **Good:** the deployment is one region, one database, one failure domain to reason
  about, and the latency budget is spent where the customers are.
- **Good:** the residency answer is honest and immediate — self-host — instead of a
  roadmap promise.
- **Bad / trade-offs:** a customer who wants *managed* **and** non-US residency cannot be
  served until a second region exists. That is a known, accepted gap, and it should be
  stated plainly in sales material rather than papered over.
- **Bad:** a single region is a single regional failure domain. Multi-AZ (ADR-0028)
  covers the loss of an availability zone, **not** the loss of a region. The recovery
  story for a regional outage is restore-elsewhere from backups, with a recovery time
  measured in hours, and it must be **rehearsed** before it is quoted to anyone.
- **Neutral, and the point of writing this down:** choosing a US primary region means
  personal data of non-US data subjects is **transferred**, which requires a valid
  transfer mechanism and a processing agreement naming the infrastructure provider as a
  sub-processor. This record does not create either. It records that **the region choice
  does not remove the obligation** — so no future reader mistakes "we host in region X"
  for a compliance answer. This is an engineering record, not legal advice; the
  instruments themselves belong to the compliance track.

## Why the alternatives were rejected

- **B (two regions at launch)** — rejected as paying twice, permanently, for a customer
  who does not exist yet. A second region doubles the fixed infrastructure floor and adds
  a class of problem that never goes away: which region owns a tenant, what crosses
  between them, and how a residency claim is proven per tenant rather than per platform.
  All of it is worth doing when a signed requirement funds it.
- **C (region chosen for regulatory narrative)** — rejected because it buys a paragraph
  and pays for it with every request. It also does not deliver what it appears to: as
  above, hosting location does not decide applicable law, so the narrative would be
  weaker than it sounds while the latency cost would be exactly as large as it sounds.
