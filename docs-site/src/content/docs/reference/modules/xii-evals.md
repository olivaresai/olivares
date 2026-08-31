---
title: "Module XII — quality, evals & testing"
description: >-
  Quality measurement: scoring candidate outputs against versioned golden suites
  with pluggable scorers (including a fail-closed LLM judge), and turning the result
  into the canonical cross-module evidence other modules consume.
---

Module XII answers one question — *is my agent still doing the right thing?* — by
**scoring** candidate outputs against **versioned golden suites** and emitting the
result as canonical, cross-module evidence. It is an Intelligence-layer module: it
**measures**, it does not run the subject and it does not act on infrastructure. This
page is the reference for what the evals module does today, and its honest limits.

## What it measures (and what it never runs)

XII is a measurement layer, not an execution layer. A candidate output reaches it
already produced — from the testing sandbox (module XVII), from CI, inline in the
request, or as a sampled real-session signal — and XII scores it against the cases of
a suite. **The only model XII ever invokes is the judge** (for the `llm_judge` scorer);
it never runs the subject agent or model itself. Producing outputs is the sandbox's
job, not XII's.

The scorer set is **pluggable**. Deterministic, pure built-ins cover the common
contracts — `exact`, `contains`, `not_contains`, `regex`, `json_valid`, `json_equal`
and `numeric_range`. Alongside them sits an **`llm_judge`** scorer that invokes a model
through the Judge port to grade against a rubric.

## Suites, runs and the canonical artifact

A **suite** is a versioned golden dataset: it holds its cases, a default scorer, a pass
threshold and a regression threshold. Cases are **append-only and immutable per
version** — correcting a case mints a new `suite_version`, never an in-place edit, so
the dataset that produced any past verdict is always reconstructable.

A **run** scores each case in a suite, aggregates a `score` and `pass_rate`, and
persists three things: append-only per-case evidence, a mutable run aggregate, and one
core **`EvalResult`** — the canonical artifact (`Suite`, `SubjectKind`, `SubjectID`,
`Score`, `Passed`, `OccurredAt`, `Metrics`) that compliance (XIII) and the UI read
**without knowing XII's own tables**. Runs execute synchronously; the SSE stream on a
run *replays the persisted run* (per-case frames, then a summary), it does not actuate.
A regression against a baseline sets `regressed` and writes a core **`Finding`**
(`Kind = eval_regression`), best-effort emitted on the bus as
[`finding.reported`](/reference/events/) for delivery modules (health/notifications) to
route. On the read side, **scorecards** aggregate pass-rate, mean score and trend per
subject and export as CSV/JSON.

## Minimal-data, by construction

The candidate output is **never persisted** — from any source. A per-case result stores
only a one-way detail hash and a clamped, scrubbed label for the UI; redaction is done
by the handler before storage, never assumed of the store. The **monitor** scores
*behavioural signals* of a real session — its state, finding count, max severity and
token/cost figures (drawn from the core `Session`, `Finding` and `CostRecord` signals)
— and **never the raw output text**, which the platform does not persist at all. Golden
fixtures are the one bounded exception: operator-authorised, opt-in, non-production
content, clamped by the handler before write so a suite can actually be run.

## Judge calibration, bias mitigation and the CI regression gate

The judge's verdicts are **trusted only after being measured**. A human-labeled
calibration set (built with the guided `olivares evals label` session) feeds a
**calibration run** that measures the judge against the human reference: percent
agreement with its 95% Wilson interval, **Cohen's kappa** (agreement alone is not
defensible under class imbalance), sensitivity/specificity with their denominators, and
a verbosity-bias correlation. The report is append-only evidence; the target —
agreement ≥ 0.85 **and** a defined kappa ≥ 0.6 — can be raised per run but never
lowered. A set whose human labels are all-pass cannot measure chance-corrected
agreement and certifies nothing.

Bias mitigation is built in and *measured*: the judge prompt forces reasoning **before**
the verdict (the analysis is discarded in flight — minimal-data) and instructs against
rewarding length; the A/B comparison's opt-in pairwise mode judges every shared case
twice with the presentation order swapped, declares a winner **only when both orders
agree**, and reports the measured `position_consistency` rate.

The **regression gate** (`POST /gate`, CLI `evals gate`) turns all of this into a
blocking CI verdict: a regression vs baseline, a pass-rate below the suite threshold, or
an **uncalibrated judge** fails the gate (exit 1); a missing judge credential degrades
to a *declared* warn, never a silent pass. Judge cost in CI is controlled by a
deterministic seeded case sample, a verdict cache keyed by content + judge-model pin +
prompt version, and a FinOps budget pre-flight that refuses to spend past a cap. The
only escape from a failed gate is the **governed override** — admin-tier, written
reason, audited — which changes the *effective* verdict CI re-checks, never the
recorded one. Every reported rate ships with its denominator and 95% interval; see
`docs/EVAL-METHODOLOGY.md` in the repository for the full methodology and sources.

:::caution[Honest limits]
- **`llm_judge` is fail-closed, never a false pass.** Model invocation is a declared
  seam: with no judge wired, the `llm_judge` scorer returns `skipped` (excluded from the
  denominator), never a silent pass. The composition root injects the real judge
  adapter; until then, judged cases are honestly reported as not evaluated.
- **The gate blocks merges, not infrastructure.** The regression gate returns a
  verdict a CI pipeline maps to its exit code; XII still deploys nothing and fires
  nothing. An uncalibrated judge cannot pass its own gate — calibration is measured
  against human labels, never assumed.
- **XII does not run the subject.** It scores outputs handed to it; it never executes the
  agent or model under test. The only model call it makes is the judge.
- **Monitoring is signals, not text.** Real-session monitoring scores minimal-data
  outcome signals — never raw output, which is never persisted. The absence of a
  monitored signal is not proof of behaviour.
- **No actuation surface.** XII governs and observes quality; it deploys nothing, fires
  nothing and gates no infrastructure. The pre/post-deploy *verdict* it provides is
  evidence for the deploy module to act on — see [Honesty & limits](/start/honesty-and-limits/).
:::

## Related

- [Modules catalog](/reference/modules/overview/) — where XII sits and the Govern/Actuate split.
- [Event bus reference](/reference/events/) — the `finding.reported` event a regression emits.
- [Architecture overview](/explanation/architecture/overview/) — the Intelligence layer.
- [Govern and approve](/how-to/govern-and-approve/) — acting on a regression finding.
- [Honesty & limits](/start/honesty-and-limits/) — the deny-closed seams across the product.
