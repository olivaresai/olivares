<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# ADR-0023: Context-policy enforcement at the three transit points, with per-group window and spend ceilings

- **Status:** accepted
- **Date:** 2026-07-08
- **Deciders:** Fran Olivares
- **References:** ADR-0022 (source scoping by subject axis — its subject resolver and `most-specific` precedence are mirrored here), ADR-0009 (append-only hash-chained audit), ADR-0003 (RRW map — permitted vs observed).

## Context and problem statement

The context-policy (window size and compaction strategy) was persisted as governed data, but **no consumer ever applied it** — the consumer a code comment promised did not exist, so the policy was dead metadata. Separately, the inference proxy's token ceilings were **per-tenant / per-request** only, and FinOps carries a `team` budget dimension that is **detective and fail-open**. There was no way to say "this group of users (or agents) may consume at most this much window / this much spend" and have it enforced.

The product vision requires two things the stored-but-unused policy could not deliver:

1. **The context-policy DECIDES at all three transit points** where the platform touches a model request — the session runtime, the inline inference proxy, and knowledge retrieval — rather than being inert data.
2. **Enforced ceilings per group** — `user_group` and `agent_group` — for both the **context window** and **spend**, deny-closed where the policy demands it, and with **honest degradation** (never a silent clamp or a silent allow).

## Decision drivers

- **Consistency with source scoping (ADR-0022).** Reuse the same subject vocabulary and the same `most-specific` precedence so operators reason about context governance exactly as they reason about source scoping — no second decision engine, small attack surface.
- **A ceiling must actually be a ceiling.** A numeric limit that a more-specific scope can *loosen* is not a ceiling; "enforced ceilings" is the whole point.
- **Honest degradation.** Where the platform cannot fully account for something (approximate group spend) it must fail in the *safe* direction and say so — never deny falsely, never allow silently.
- **Reuse existing primitives.** Prefer the audit ledger, the existing per-subject cost attribution, and the existing proxy deny path over new cross-cutting machinery.

## Decision outcome

### 1. `Apply` composition — qualitative most-specific, security floors restrictive, `max_tokens` by MIN

`Module.Apply` (`modules/knowledge/context.go:263`) resolves the effective policy for a request:

- **Qualitative** fields resolve **most-specific-wins** (`strategy`), consistent with ADR-0022.
- **Security floors** compose **restrictively**: `forbid` is absolute; `redaction_required` composes by OR; `excluded_sources` composes by union.
- **`max_tokens` composes by MIN** (most-restrictive; field at `context.go:62,73`, bounded at `context.go:124`). This is the deliberate refinement for the numeric limit: a ceiling a more-specific scope could raise would not be a ceiling. The behaviour is reversible in ~2 lines if a deployment ever prefers most-specific for the limit.

### 2. Agent identity in the proxy — close the reachable residual (E3-lite), defer the rest honestly

The session-inference WIF credential (`sk-ant-oat`) does **not** transit the inline inference proxy, which authenticates only the platform's own `olvs` / `olvk` tokens. Fully closing agent-identity federation for *session* traffic would mean re-architecting the inference credential (multi-day, part of the ephemeral-WIF mint posture) and is **deferred to a dedicated effort (E3-full)**.

The reachable part is closed now (**E3-lite**): `authToken` propagates `AgentRef` → `AgentIdentity`, and the models actor-scope resolver honours the **authenticated principal** rather than a caller-declared value (a bug fix), which enables the `agent_group` axis in the proxy for agent-on-behalf-of callers. The agent ref is always taken from the authenticated credential, never from the request body (`context.go:278-279`, `query.go:110-111`).

### 3. Per-group SPEND ceiling — preventive, fail-open by nature, with a granular fail-closed knob

Budget gains `user_group` / `agent_group` dimensions, enforced **preventively** via `CheckBudget`, with a group's spend summed by **member fan-out** over the existing per-subject cost attribution (there is no group column; summing every row indiscriminately would be a mis-attribution bug — `modules/finops/ingest.go:75,361`).

The posture is **fail-open** — the nature of a budget check, and consistent with the product's split of *security = deny-closed* vs *budget = fail-open* (`modules/models/api.go:639,656`) — with a per-budget **`fail_closed`** knob for deployments that want a hard stop (`modules/finops/budgets.go:102,166,182`). This is stated **honestly**: preventive group spend is *approximate*, not exact accounting. Coverage scales with attribution — spend that is not yet attributed simply under-counts the group, which is the safe direction (it never denies falsely). The detective ingest/finding FinOps backstop for groups and local degradation counters are a **documented follow-up**, deliberately not half-wired.

### 4. Over-window proxy denial — 413, never mutate the client payload

When a request exceeds the effective policy/group window, the proxy **denies with HTTP 413** and a detail (`cmd/olivares/inferenceproxy.go:449`); it **never mutates the client's opaque payload** — it denies rather than silently clamping (`inferenceproxy.go:550`). Compaction and signalled truncation live only where the platform itself assembles context (retrieval and the session runtime), never over the caller's prompt. There is no silent degradation.

The three enforcement points are wired: retrieval (`modules/knowledge/query.go:167` → `:354`), the session runtime (`modules/sessions/runtime.go:285,623`), and the inference proxy (above).

## Decide-and-state (within the approved direction)

- **Nine context-policy scope-kinds** — `session > agent > user > user_group > role > agent_group > kb > workspace > tenant` — validated at the write handler (`modules/knowledge/context.go:102-103`), with a nullable, expand-only `effect` (an established module-column reconcile, no numbered migration).
- **`surface` and `model` are not scope-kinds.** Retrieval has no surface, and the proxy already folds the per-surface window into the MIN, so adding them would be unused generality (YAGNI).
- **"OTel metric" for this feature = auditable events + native findings**, not an in-module meter. Product telemetry flows over the bus as findings into observability; a new meter would be a cross-cutting architecture change, out of scope here.

## Alternatives considered

- **Most-specific composition for `max_tokens`** (uniform with the qualitative fields): rejected — a numeric ceiling a more-specific scope can raise is not a ceiling, which defeats the goal. Kept trivially reversible if a deployment disagrees.
- **A dedicated in-module meter for context/group telemetry:** rejected as a cross-cutting architecture change; the audit-events + bus-findings path already carries the signal.
- **Summing all per-subject spend rows for a group without member fan-out:** rejected — it over-counts and mis-attributes; fan-out over the authenticated membership is the correct, safe attribution.

## Consequences

- The context-policy moves from dead metadata to a **live decision** at retrieval, the proxy, and the session runtime.
- Per-group **window** ceilings are **hard and MIN-composed**; per-group **spend** ceilings are **preventive and honestly approximate**, with an opt-in `fail_closed`.
- **Registered debt, none half-wired:** E3-full (re-routing session inference through governed identity), the detective group-spend backstop via FinOps plus local degradation counters, and threading the principal (`user` / `user_group`) to the launch gate. All are documented follow-ups.
