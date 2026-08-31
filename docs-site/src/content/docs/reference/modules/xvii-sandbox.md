---
title: "Module XVII — agent simulation & testing sandbox"
description: >-
  Isolated, ephemeral execution of agent scenarios against mocked tools and
  resources, deterministic replay of a historical session, and pre/post-deploy
  comparison of two variants — with an honest, attested isolation guarantee.
---

Module XVII is the **testing sandbox**: it runs an agent scenario in an isolated,
ephemeral environment, replays a historical session deterministically, and compares
two variants before a deployment. It is the sibling of module XII (evals) — XVII
**executes in isolation and produces outputs**, XII **measures their quality** — and
the two are decoupled: neither imports the other. This page is the reference for what
the sandbox does today and its honest limits.

## What it is

The sandbox catalogs operator-authored **scenarios**: a sequence of step inputs plus
the mocked responses of the tools and resources a run is allowed to touch. A scenario
is a synthetic fixture — no secrets, no production handles — clamped before it is
persisted. Three flows run on it:

- **Scenario simulation** — execute a scenario's steps against its mocks, producing
  per-step outputs (optionally scored against an evals suite).
- **Replay** — reconstruct the input timeline of a historical session and re-run it
  deterministically against mocks, so the same input yields the same output.
- **Pre/post-deploy comparison** — run the *same* scenario against a baseline and a
  candidate variant, score both, and record a verdict (`improved` / `regressed` /
  `unchanged` / `inconclusive`) with the delta.

## Entities and the isolation guarantee

The module owns four entities: a mutable **scenario**, a mutable **run** (`running` →
terminal), an append-only per-step **output**, and an append-only pre/post-deploy
**comparison**. Every run records *which* runner ran it, whether that runner was
`isolated`, whether ephemeral state was `destroyed`, the per-step counts, and — if a
scorer was wired — the suite, score and pass verdict.

Isolation is a property of the wire, attested per run, not a claim. The default
in-process runner is **isolated by construction**: it receives only the step-and-mock
spec and holds no handle to the store, the network or any secret; a step that asks for
a resource absent from the mocks yields a deterministic mock-miss marker and never
reaches a real resource; state lives in the call and is discarded on return, so the run
records `destroyed`. Under operator provision, an **OS-level runtime** stands behind the
same interface — an ephemeral, hardened, egress-controlled instance whose backend
(gVisor or Firecracker microVM) is chosen *by policy* and gated by preflight. Each run
records the real backend and its `isolated` flag, so a degraded or portable backend is
visible and auditable, never hidden.

## What it consumes and produces

The sandbox does not emit on the event bus; it produces **persisted evidence** that
other modules read without coupling to it. Its outputs are scored by module XII through
an adapter wired only in the composition root — the two siblings share a thin port
contract, not an import. Its pre/post-deploy comparison is the **decision evidence** the
deployment module reads to gate a promotion, and it feeds the regression baseline that
XII tracks. Launching a run, a replay or a comparison is a **privileged, tenant-scoped,
audited** action (editor and up to run; the deploy comparison is an admin decision).

:::caution[Honest limits]
- **Default runtime is synthetic-only.** Without an OS-level runtime provisioned by the
  operator, the in-process mock runner is the backend: it is isolated by construction
  but executes only against mocks, so it cannot reach a real target and cannot back an
  adversarial probe against live infrastructure (module XVIII keeps its own safe default
  until the runtime is provisioned). This is honest, not degraded — a default deployment
  is fully functional.
- **Provisioned-but-incapable fails closed.** When OS-level isolation is requested and
  the host lacks the primitive, the engine wires the same and **each run fails closed** —
  it never silently downgrades to the synthetic runner or fakes a microVM. A run on a
  host without isolation is recorded as not isolated, never as protected.
- **No scorer wired ⇒ "executed, not scored."** A run carrying a suite reference with no
  scorer adapter is recorded as executed but unscored — never a silent pass.
- **Replay is honest about gaps.** If the history source cannot reconstruct an ordered
  timeline, the replay is reported degraded with zero steps, never fabricated.
- **No synthetic-data generation.** This is a documented post-v1 extension point only;
  the module ships no generator, exposes no route for it, and produces zero samples.
:::

## Related

- [Module XII — quality, evals & testing](/reference/modules/xii-evals/) — the sibling that scores the outputs.
- [Modules catalog](/reference/modules/overview/) — where XVII sits and the Govern/Actuate split.
- [Architecture overview](/explanation/architecture/overview/) — the Intelligence layer.
- [Govern and approve](/how-to/govern-and-approve/) — acting on a pre/post-deploy verdict.
- [Honesty & limits](/start/honesty-and-limits/) — the deny-closed seams across the product.
