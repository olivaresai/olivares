# ADR-0002: Ship the complete product (28 modules), not a wedge

- **Status:** accepted
- **Date:** 2026-06-02
- **Deciders:** Fran Olivares
- **References:** product decisions register (P1); module catalog (the 28 modules)

## Context and problem statement

A common go-to-market for an infrastructure product is a narrow "wedge" — ship one
sharp capability, win a beachhead, expand later. For Olivares AI the candidate wedge
was the read/write access map alone. The question was whether to release the wedge or
the complete control plane.

## Decision drivers

- First impression: enterprise buyers (CTO/SOC/security) evaluate a control plane as a
  platform, not a feature.
- The R/RW map is more valuable *inside* a complete platform than as a standalone tool.
- Avoiding re-architecture: a modular platform admits new modules without rework.

## Considered options

- **Complete product** — release all 28 modules as one coherent platform (own-model
  management / fine-tuning is a planned capability, not one of the 28).
- **Narrow wedge** — release the R/RW map alone, expand later.

## Decision outcome

Chosen option: **complete product**. The initial release is the full platform, built
around Claude and Claude Code — inventory, live sessions, the R/RW map, governance,
source/credential scoping, deployment, knowledge, security, privileged-session
recording, model/provider management, the inline inference proxy, FinOps, evals,
compliance, the SIEM forwarder, catalog, output integrations, eventing, voice, sandbox,
red-teaming and health — with the engine's own API, multi-tenancy and dashboards as
core/console capabilities. The R/RW map is **a key differentiated capability within**
that product, not the product itself.

### Consequences

- **Good:** a complete, credible platform on day one; the access map lands in context.
- **Bad / trade-offs:** a much larger v1 surface to build and keep honest; depth varies
  by module and must be documented honestly (see *Honesty & limits*).
- **Neutral:** own-model management / fine-tuning is a planned capability, not one of the
  28 shipped modules.

## Why the alternatives were rejected

- **Narrow wedge** — rejected: it undersells a platform product and risks the
  R/RW map being perceived as a commodity "session viewer" rather than the
  least-privilege-drift engine it is.
