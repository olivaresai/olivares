---
title: Module XVIII — red-teaming & adversarial testing
description: "A defensive robustness harness: a consent-gated battery of
  published adversarial probes mapped to OWASP Agentic and MITRE ATLAS, scored
  into a tamper-evident scorecard. What it tests, the consent red line, and its
  honest limits."
slug: 2026-06/reference/modules/xviii-redteam
---

Module XVIII is a **defensive robustness harness**. It probes the client's **own**
governed agents with a battery of published adversarial test cases — prompt
injection, jailbreak, exfiltration, tool poisoning — and scores their resistance,
mapped to the **OWASP Top 10 for Agentic Applications**, the **OWASP LLM Top 10
(2025)** and **MITRE ATLAS**. It is a test suite, not a weapon: a compliance or leak
is a finding, not an exploit handed to anyone.

## What it is

The battery is a catalog of **probes** across four families (`injection`,
`jailbreak`, `exfil`, `tool_poisoning`). Each probe is a *known, published* robustness
test mapped to an OWASP/ATLAS reference, with the expectation that a well-defended
agent **refuses** it or its guardrail **blocks** it. Payloads are benign canaries —
they ask the agent to emit an inert marker, or describe a dangerous operation without
executing it — so the battery probes the *refusal*, not the breach. A deterministic
**Judge** classifies each result: `blocked`/`refused` is a **pass**, `complied`/`leaked`
is a **fail**, `error` is an execution fault, `skipped` is not-executed.

Results roll up into a **scorecard**: `score = passed / (passed + failed) × 100`,
with `errors` and `skipped` deliberately **excluded** from the denominator — a probe
that never ran is never counted as a pass. The scorecard breaks down per family and
tracks OWASP-Agentic failure coverage, and is an **append-only, tamper-evident**
record so a later run can compare against it as a regression baseline.

## The red line, its contract and entities

The dual-use boundary is **enforced in code**, not just stated in docs. A run executes
**only** against an agent the client governs that has been explicitly **registered and
authorized** as a target — and registering is not consenting: a target is born
`registered` with authorization withheld, and a separate authorize step is the
explicit grant. Launching a run against an unauthorized or unknown target is refused
at the gate. Registering, authorizing and launching are all **admin-tier, audited,
privileged** actions; each leaves a self-audit attributed to the real principal.

The module owns three tenant-scoped entities: the **target** (a mutable consent record
through its register → authorize → revoke lifecycle), the **run** (an append-only
evaluation record carrying the aggregates and score), and per-probe **results**
(append-only, one row per probe). It is **minimal-data by construction**: the target
endpoint is an opaque handle the sandbox dereferences — never a credential — and a
result stores only a one-way hash of its detail, never the raw payload or the agent's
raw response. The read-side API serves the catalog as **taxonomy only** (id, family,
title, OWASP/ATLAS reference, severity, surface); the probe payloads are internal and
are never exposed on the wire.

## What it consumes and produces

The module owns the battery and the scoring; it does **not** reach any agent itself.
Execution is delegated to the isolated runtime over a `Sandbox` seam — the sandbox is
the only component that touches the target, inside the client's perimeter, with egress
segmented to exactly the authorized target and everything else denied. Each failed
probe is persisted as a core `Finding` (`kind = "redteam"`) inside the run's
transaction, and a minimal-data `finding.reported` event (`kind = "redteam_failure"`)
is published to the [event bus](/2026-06/reference/events/) for delivery and compliance
consumers — both carry a subject reference, title and detail hash only.

## Honest limits

:::caution[Honest limits]
* **Without a wired sandbox, a run is DEGRADED, never a false pass.** The default
  execution seam reaches no agent: every probe is reported `skipped`, the run status
  is `degraded`, and the score reflects that nothing was tested. The harness ships the
  full battery and scoring today; live execution depends on a provisioned isolated
  runtime, and an unprovisioned deployment is honest about it rather than scoring an
  untested target.
* **It only tests agents you govern and authorize.** It never targets third-party
  systems, never scans others' credentials, and ships no purely-offensive capability.
  An unauthorized or unknown target is refused — this is not a configuration knob.
* **The probes are a published, defensive battery — not novel exploits.** Each maps to
  an OWASP/ATLAS reference. The ATLAS coverage view is a **dated snapshot** stamped to
  a specific matrix version, not continuous parity with the live matrix.
* **A sandbox execution fault is not a verdict.** An `error` result keeps the run going
  and is excluded from the score; it never counts as a vulnerability or a pass.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — where module XVIII sits and its actuation status.
* [Module IX — security, guardrails & audit](/2026-06/reference/modules/ix-security/) — the consumer of the `redteam` findings.
* [Event bus reference](/2026-06/reference/events/) — the `finding.reported` event and its minimal-data payload.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — how the engine and the isolated runtime compose.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — authorizing a target and acting on findings.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — the product-wide actuation contract.
