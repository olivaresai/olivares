---
title: "Own-model fine-tuning & local inference execution (planned)"
description: >-
  What remains planned on the own-model side: the platform running fine-tuning jobs and
  serving local inference itself. The own-model registry, signed-model admission, lineage
  records and AIBOM evidence already ship as model operations; this page is honest about
  the executing half that does not.
---

The own-model story — governing **models the company trains or hosts itself** — splits
into two halves, and only one of them is still planned.

The **governing half ships today** as
[Module XXIII — model operations](/reference/modules/xxiii-model-operations/): a versioned
**registry of owned models** (`hosted`, `fine_tuned`, `imported`), the signed-model
**admission** gate, **dataset and fine-tune-job lineage records**, governed
**local-inference deployment records** (vLLM, Ollama, llama.cpp, other) with
enforce-signed re-checks at deploy time, and **AIBOM / model-card** generation with
ledger-anchored sealing. Its entities and endpoints are declared and served under the beta
module routes (`/v1/m/models/owned-models`, `/v1/m/models/model-versions`,
`/v1/m/models/finetune-jobs`, `/v1/m/models/inference-deployments`,
`/v1/m/models/aiboms`, …) — see the [module-route reference](/reference/api-beta/).

This page tracks the **executing half, which is planned and deliberately not built**: the
platform itself *running* that work.

## What ships today (elsewhere)

Governance of own models is real and documented on the
[model operations](/reference/modules/xxiii-model-operations/) page:

- a **registry of own models** with immutable versions, so a fine-tuned or self-hosted
  model is a first-class, governed entity rather than an unmanaged endpoint;
- **fine-tuning jobs as lineage records** — inventory of externally run training work and
  the model version each produced;
- **local inference deployments as governed records** — the serving runtimes you operate,
  brought under admission enforcement (`require_signed`) and audit.

## What remains planned

- **Running fine-tuning jobs.** The shipped module records the status and lineage of
  fine-tuning work run elsewhere; the platform never starts, cancels or executes a
  training job, and stores no weights or dataset contents. A pipeline that *executes*
  fine-tuning from the platform is planned work.
- **Serving local inference.** Deployments are governed records of runtimes the operator
  runs; the platform does not host or serve inference itself. First-party local-inference
  serving is planned work.

For this executing half no job schema, scheduler contract or serving-runtime contract is
declared, and this page intentionally invents none.

## Why it is planned, not shipped

The platform is built so any capability attaches without re-architecting the rest, so
execution can be added later on top of the shipped governance surfaces. It was placed
**after** v1 by an explicit product decision: the priority for the first release is
governing the models and agents an organisation already runs, and executing
training/serving does not change that core value enough to compete for v1 effort.

When it is built, its natural seam is already shipped: an executed fine-tune would produce
a model **version** in the [model operations](/reference/modules/xxiii-model-operations/)
registry and pass the same signed-model **admission** gate as any externally produced
artifact, with the vendor-stack policy remaining in
[model & provider management](/reference/modules/x-models/).

:::caution[Honest limits]
- **The governing surfaces are shipped, the executing surfaces are not.** Do not read
  this page as denying the registry, admission, lineage records, deployment governance or
  AIBOM evidence — they exist and are documented in
  [model operations](/reference/modules/xxiii-model-operations/).
- **No execution surface exists today.** There is no training pipeline, no fine-tune job
  scheduler and no first-party inference serving in the shipped binary, and no entity,
  endpoint or event is declared for them — not even a refusing interface.
- **Nothing here is a promise of a date or a depth.** The scope above is the planned
  direction; the job schema and runtime contracts will be designed when it is built. They
  are deliberately left unspecified rather than fabricated.
:::

## Related

- [Module XXIII — model operations](/reference/modules/xxiii-model-operations/) — the shipped own-model governance surface: registry, admission, lineage, deployments, AIBOM.
- [Modules catalog](/reference/modules/overview/) — the 30 shipped modules and where the own-model work sits.
- [Module X — model & provider management](/reference/modules/x-models/) — the shipped neighbour governing the vendor model stack.
- [Honesty & limits](/start/honesty-and-limits/) — the observe-broadly / actuate-on-a-subset contract and what "planned" means.
