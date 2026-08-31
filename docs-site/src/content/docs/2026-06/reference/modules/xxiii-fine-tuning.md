---
title: Module XXIII — own-model management / fine-tuning
description: The post-v1 module for governing models the company trains or hosts
  itself — a model registry with versioning, fine-tuning jobs and local
  inference. It is intentionally not built for v1; this page states that scope
  and limit honestly.
slug: 2026-06/reference/modules/xxiii-fine-tuning
---

Module XXIII is the planned governance surface for **models the company trains or hosts
itself** — an own-model registry, fine-tuning jobs and local inference — extending the
governed model stack into infrastructure the organisation owns rather than buys. It is
the **only module not in the v1 set**: a deliberate scope decision, not a gap. This page
describes what it is meant to govern and is honest that it is not yet built.

## What it will govern

The module's intended scope, as catalogued in the master product plan, is the governance
of company-owned and company-hosted models alongside the vendor stack module X already
governs:

* a **registry of own models** with versioning, so a fine-tuned or self-hosted model is a
  first-class, governed entity rather than an unmanaged endpoint;
* **fine-tuning jobs** — the pipeline of training/adapting a model, tracked and governed
  like any other governed asset;
* **local inference** — self-hosted serving runtimes (vLLM, Ollama) brought under the same
  catalog, capability and routing governance as hosted providers.

> Correction (2026-08-01): parts of this scope had in fact already shipped when this
> snapshot was cut (model admission, AIBOM, owned-models endpoints); the live
> [model operations](/reference/modules/xxiii-model-operations/) page carries the
> current state.

This is the **target scope only**. None of these capabilities is implemented in the
shipped binary. The entities, endpoints and events for them are not declared, and this
page intentionally invents no registry shape, job schema or runtime contract for them.

## Why it is post-v1

The 23-module platform is built so any module attaches without re-architecting the rest,
so XXIII can be added later as a peer of the others. It was placed **after** v1 by an
explicit product decision: the priority for the first release is governing the models and
agents an organisation already runs, and own-model/fine-tuning governance does not change
that core value enough to compete for v1 effort. It is therefore listed as **post-v1** in
the modules catalog, with **no** Govern/Observe or Actuate surface shipped today — distinct
from the deny-closed *seams* of other modules, which are declared interfaces that exist but
refuse to act. XXIII is simply **not built yet**.

When it is built, its natural seam is module X: it would extend the same governed
model/provider catalog and routing policy to own models, rather than introducing a parallel
model abstraction.

:::caution[Honest limits]
* **This module is intentionally not built.** It is **post-v1 by an explicit product
  decision**, not an unfinished v1 module and not a gap. There is no registry, no
  fine-tuning pipeline and no local-inference governance in the current product.
* **No surface exists today.** Unlike the deny-closed seams of other modules, XXIII exposes
  no entities, endpoints, events or even a refusing interface in the shipped binary.
* **Nothing here is a promise of a date or a depth.** The scope above is the planned
  direction recorded in the product catalog; the registry shape, job schema and runtime
  integrations will be designed when the module is built. They are deliberately left
  unspecified rather than fabricated.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — the 28 modules and why XXIII is the only post-v1 one.
* [Module X — model & provider management](/2026-06/reference/modules/x-models/) — its v1 neighbour and natural seam, governing the vendor model stack today.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — the observe-broadly / actuate-on-a-subset contract and what "post-v1" means.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — how modules attach without re-architecting the engine.
