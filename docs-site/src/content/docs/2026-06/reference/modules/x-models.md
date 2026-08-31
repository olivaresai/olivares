---
title: Module X — model & provider management
description: The governance layer over the whole AI model stack — Claude,
  OpenAI, Gemini and local inference. A versioned reference catalog, capability
  matrix and routing policy that resolves a primary + fallback chain; it routes
  but does not yet execute the model call.
slug: 2026-06/reference/modules/x-models
---

Module X governs the **whole AI model and provider stack** — Claude, OpenAI, Gemini and
local inference, not just one vendor. It is a **Core-layer** module that sits *on top of*
the model/provider connectors: it does not re-implement any provider integration nor the
inference gateway. What it owns is the **governance layer** — a versioned catalog,
a cross-vendor capability matrix, and named routing policy.

## What it is

The module turns the bare `Provider`/`Model` entities that inventory (module I) discovers
into a governed catalog. Two halves:

* **A declared reference catalog** — a versioned-in-repo, operator-overridable table of
  model families with their declared API-feature capabilities and **list-price defaults**.
  Prices are stamped with the date they were declared (`pricing_as_of`), are explicitly
  *defaults to verify against each provider's pricing page*, and are never fabricated
  telemetry. A family with no matching entry stays **unpriced** rather than getting an
  invented price.
* **Enrichment of the live estate** — the module listens to the
  [`cost.sampled`](/2026-06/reference/events/) stream and enriches the discovered `Model`/`Provider`
  entities with family, context window, modality, per-token pricing and the capability set
  (the pricing fields inventory defers to it).

The capability vocabulary is one **cross-vendor matrix** — the full Claude stack (prompt
caching, batch, Files, citations, extended thinking, computer use, the memory tool,
context management, vision/PDF, structured outputs) plus the analogs each other vendor
actually exposes — so the UI renders one matrix and a routing policy can require a
capability *across* vendors. The Claude families are catalogued by family (`claude-opus`,
`claude-sonnet`, `claude-haiku`, `claude-fable`, `claude-mythos`), with deprecated/legacy versions kept under longer
prefixes so current ids resolve to the current price tier.

## Its contract & entities

Routing is the actuation surface, and it is **routing-only**:

* **Routing policy** is persisted on the core `Policy` entity (`Kind="routing"`): named
  selection / fallback / version-pinning policies (cheapest-first, lowest-latency,
  capability-ordered, or a pinned model). `POST …/routing-policies/{id}/resolve` resolves a
  policy against the governed estate and returns a **primary + fallback chain** with the
  reason for the choice. This is **read-only**: it computes a selection that the
  connector/gateway then executes — the module performs **no inference**.
* **API-key / workspace governance** is **minimal-data metadata only** — which agent or
  team uses which credential, carried as a masked hint, never the secret value.
* A read-only **Anthropic rate-limit inventory** (the ceilings a gateway or proxy must keep
  in sync) is served as a consultable inventory; it is never a control the module mutates,
  and it degrades to an honest *unavailable-with-reason* response when the read-only Admin
  connector is not provisioned.

Catalog and feature reads are not sensitive and are gated at the viewer tier; routing and
key-governance mutations are an editor-tier, audited change; the governed-execution path is
an admin-tier action distinct from the read-tier resolve. Most routes are reachable but
deliberately not part of the served OpenAPI contract; their field-level shapes live in the
product's typed interfaces.

## What it consumes & produces

The module **consumes** `cost.sampled` from the [event bus](/2026-06/reference/events/) to enrich
the catalog with real per-token pricing and usage; it does not introduce a new observation
type. On the governed-execution path, a successful call would **produce** a redacted
`CostSample` to FinOps — the model output goes to the caller, but is persisted nowhere here.
Money never appears on this surface: no USD amount is returned, only token counts and the
target that served.

:::caution[Honest limits]
* **Routing-only actuation.** The module **resolves** a route (primary + fallback chain) but
  does **not execute the model call**. The governed-execution path is a **deny-closed seam**:
  with no executor provisioned it returns a clear `503` — the control plane can *select* a
  model but will not *spend* against a provider. When an executor is wired, a FinOps budget
  at its cap denies the spend *before* any provider call.
* **Declared pricing is a default, not a guarantee.** List prices are operator-verified
  defaults stamped with a date; the authoritative cost of real usage is always the
  connector-derived `CostSample`, never the convenience per-token figure. Unmatched families
  are shown unpriced — never with an invented price.
* **Freshly-announced models are listed but flagged.** A preview model whose capabilities are
  not yet verified against a model card is catalogued with its capability set marked
  *to-confirm* and left unpriced, rather than inventing the data.
* **Key inventory is metadata, never a secret.** The module persists governance relationships
  and a masked hint; the credential value never leaves the provider's Admin API and is never
  stored. Some providers expose no key inventory at all — a documented limit, not an omission.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — where module X sits and its actuation status.
* [Access & resource map](/2026-06/reference/modules/iii-access-map/) — the differentiating pillar.
* [Event bus reference](/2026-06/reference/events/) — the `cost.sampled` event this module consumes.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — engine, layers and connectors.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — acting on routing and governance.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — the observe-broadly / actuate-on-a-subset contract.
