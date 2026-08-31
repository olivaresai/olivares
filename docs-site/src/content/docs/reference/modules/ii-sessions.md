---
title: "Module II — live operation & sessions"
description: >-
  The live operational overlay per agent session: current action, live
  tokens/cost, a derived Claude Code state and a replayable timeline, streamed
  over server-sent events. What it derives, what stays honestly empty, and the limits.
---

Module II is the **live operation** view of the estate: what every agent session
is doing right now, its live token and cost totals, a derived Claude Code state,
and a reconstructable timeline. Where module I (inventory) materializes the durable
estate, module II keeps a **live operational overlay** per session over the same
observation stream — and shows only what that stream honestly carries.

## What it is

Module II is a bus-driven Core-layer module, sibling to inventory. It maintains a
live record keyed by each session's external reference, built from the cooperative
observation stream — never polled, never fabricated. Per session it tracks:

- the **current action** (the last tool used) and the resource/mode it touched;
- the **live token and cost totals**, read from cost samples (the canonical cost
  ledger and FinOps are module XI, not here — this is the live figure only);
- a **derived Claude Code state** (`cc_state`); and
- a **timeline** to which every observed event is appended in ingest order.

## Its contract & entities

The module registers two tenant-scoped entities. `sessions.live` holds the live
record per session — current action/resource/mode, model reference, live
input/output tokens, live cost, event and tool-call counts, and first/last
event timestamps. `sessions.timeline` holds one replayable row per event, ordered
by ingest. There is **no stored lifecycle column**: the cooperative stream carries
no end-or-fail signal, so the only honest liveness signal is the derived `cc_state`.

`cc_state` is derived **at read time** from event recency — `active` / `idle` /
`ended` — and flips to a silent-evasion state when the connector raises that
finding (it is never written by the module itself). Reads are served under module
routes (live list, single session, per-session timeline) plus a live SSE stream;
every read requires the session read permission, and **opening the stream is
auto-audited**. The SSE channel is strictly **tenant-isolated** (a client receives
only snapshots for its authorized tenant) and **best-effort** (a slow client drops
the intermediate frame and gets the next — ingest never blocks).

## What it consumes (and what it derives)

Module II consumes the same minimal-data observation stream as inventory —
[`edge.observed`](/reference/events/), `cost.sampled` and `finding.reported`.
Only edges whose origin is a **session** produce live operation; cost samples tied
to a session add to the live token/cost figure (no `CostRecord` is written here);
session-subject findings are annotated, and an anti-evasion finding marks the
evasion state. Two fields are **derived live** from those same signals: `agent_ref`
from a session's attributed agent, and `summary` from a context-compaction
(forensic) finding whose title is summary-safe by contract — never an LLM-fabricated
summary.

:::caution[Honest limits]

- **`goal` stays empty — honestly.** The cooperative stream is minimal-data and
  does **not** carry a session's goal or task list; they are redacted at the
  connector and there is no in-process prompt text on the wire. The live record
  models the field so the contract and UI are ready and any future metadata channel
  can populate it, but the module **never invents it**.
- **No stored lifecycle.** The stream has no end/fail signal, so a session's
  liveness is the **derived** `cc_state` by recency — not a persisted status. An
  `ended` state means *no recent events*, not a confirmed clean shutdown.
- **The live figure is not the ledger.** Live tokens/cost are an operational
  reading from cost samples; the authoritative, reconcilable cost record is module
  XI's FinOps ledger. Do not treat the live figure as billing truth.
- **Minimal-data is a property of the wire.** Only references, classifications and
  liveness/cost counters are carried and persisted — never payloads, prompts,
  commands or PII.
:::

## Related

- [Event bus reference](/reference/events/) — the `edge.observed`, `cost.sampled`
  and `finding.reported` events this module consumes.
- [Modules catalog](/reference/modules/overview/) — where module II sits and the
  honest actuate split.
- [Access & resource map](/reference/modules/iii-access-map/) — the sibling Core
  module that owns the R/RW access graph.
- [Architecture overview](/explanation/architecture/overview/) — the engine and layers.
- [Connect Claude Code](/how-to/connect-claude-code/) — start producing the live stream.
- [Honesty & limits](/start/honesty-and-limits/) — what the product does and does not do today.
