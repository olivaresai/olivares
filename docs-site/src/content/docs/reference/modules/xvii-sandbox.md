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
- **Local data generation.** Generate reproducible scenario inputs through the sandbox API using bounded local templates. No model or network request is made.
:::

## Generate scenario inputs

The CLI can write generated inputs directly into the step format used by scenario creation:

```sh
olivares sandbox generate --count 2 --seed-file seed.txt -o json > steps.json
olivares sandbox scenarios create --name generated --steps-file steps.json
```

`--seed-file -` reads the template from stdin. Omit the flag to use the default
template. The file's newlines are preserved; use synthetic text only.

`POST /v1/m/sandbox/synthetic-data` requires the same permission as creating a
scenario. It returns `samples`, each with `key` and `input`, ready to use as a
scenario's `steps`. Generation itself does not save the inputs; its audit event
contains only the sample count. Use synthetic text, not secrets or production data.

```json
{"subject_kind":"agent","count":2,"seed":"{{subject_kind}}:user{{index}}@example.test"}
```

The two inputs are `agent:user1@example.test` and `agent:user2@example.test`.
The only substitutions are `{{index}}` (starting at 1) and `{{subject_kind}}`;
all other text is literal. This is local fixture generation, not model-generated
language or a statistical simulation. Defaults are subject `agent`, count `10`
and seed `{{subject_kind}}-sample-{{index}}`. Count is limited to 100, subject to
200 bytes, and both seed and each generated input to 8192 bytes. Oversized
requests fail without returning a partial sample set.
The encoded batch must also fit the scenario API's 1 MiB request limit, including
JSON escaping and room for the bounded name, description and subject. Additional
mocks and formatting still count toward that request limit.

## Run a generated scenario

Enable the selectable module with `olivares modules on sandbox`. Generation,
scenario creation and execution require an editor or administrator; a viewer
can inspect saved scenarios, runs and outputs.

Create a template without a trailing newline:

```sh
printf '%s' '{{subject_kind}}:user{{index}}@example.test' > seed.txt
```

Save this synthetic response in `mocks.json`:

```json
[{"resource":"agent:user1@example.test","response":"first synthetic account"}]
```

```sh
olivares sandbox generate --count 2 --seed-file seed.txt -o json > steps.json
olivares sandbox scenarios create --name generated --steps-file steps.json --mocks-file mocks.json -o json
olivares sandbox scenarios run <scenario-id> --variant candidate -o json
olivares sandbox runs get <run-id> -o json
olivares sandbox runs outputs <run-id> -o json
```

Use the scenario ID returned by creation, then the run ID returned by execution.
With the default `inproc-mock` runner, the first input resolves to the response
above; the second returns `[[mock-miss:agent:user2@example.test]]`. A mock miss
increments `steps_error` but is an expected synthetic result: the run still
records `status: completed`, `steps_total: 2`, `steps_ok: 1`, `steps_error: 1`,
`isolated: true` and `destroyed: true`. It makes no request to a real resource.
This example does not request scoring or qualify an OS-level runtime.
Matching uses the exact input text, including any newlines in a template file.

Scenarios, runs and outputs remain available after an engine restart. Turning
the module off removes access to its routes without deleting its saved data;
turning it back on restores access. These are tenant-scoped records: another
organization cannot read them.

## Related

- [Module XII — quality, evals & testing](/reference/modules/xii-evals/) — the sibling that scores the outputs.
- [Modules catalog](/reference/modules/overview/) — where XVII sits and the Govern/Actuate split.
- [Architecture overview](/explanation/architecture/overview/) — the Intelligence layer.
- [Govern and approve](/how-to/govern-and-approve/) — acting on a pre/post-deploy verdict.
- [Honesty & limits](/start/honesty-and-limits/) — the deny-closed seams across the product.
