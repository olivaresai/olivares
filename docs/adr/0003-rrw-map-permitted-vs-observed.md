# ADR-0003: The R/RW map with a Permitted-vs-Observed diff is a key differentiated capability

- **Status:** accepted
- **Date:** 2026-06-02
- **Deciders:** Fran Olivares
- **References:** product decisions register (P2); architecture (module III)

## Context and problem statement

Many tools can *observe* agent activity, and many can *enumerate* granted permissions.
Neither alone answers the question that matters for governance: **is what an agent is
*permitted* to touch the same as what it is *observed* touching?** The product needed a
defensible, hard-to-commoditize capability that answers it — one of several it offers,
not the whole product.

## Decision drivers

- A capability that is hard to commoditize and directly useful to security/SOC.
- Built from signals the product can actually obtain (audit, telemetry, kernel).
- Honest about fidelity rather than overclaiming.

## Considered options

- **Permitted-vs-Observed diff** (least-privilege drift) over a read/write access map.
- **Observed-only** — show what agents did.
- **Permitted-only** — show granted permissions.
- **Session viewing** — show live agent sessions.

## Decision outcome

Chosen option: **the R/RW access map (module III) with the Permitted-vs-Observed
diff**. For every origin→resource edge the product classifies read/write, records the
signal source and confidence, and diffs declared grants against observed use to surface
**least-privilege drift**: unexpected accesses, unused grants, and reconciliation-
pending edges.

### Consequences

- **Good:** a distinctive, security-relevant artifact that the platform's governance
  builds on, alongside the other modules — not a feature in isolation.
- **Bad / trade-offs:** depends on per-agent identity for firm attribution (a shared
  service account collapses to *approximate* confidence); coverage is **tiered** by
  store; it must be honest about `unknown` and `approximate` rather than fabricate
  certainty.
- **Neutral:** the access map is a *view* over the general data model
  (see ADR-0005), not a separate schema.

## Why the alternatives were rejected

- **Observed-only / Permitted-only** — each is half the picture; the value is the *diff*.
- **Session viewing** — commoditized (vendors ship "agent view"); not a durable moat.
