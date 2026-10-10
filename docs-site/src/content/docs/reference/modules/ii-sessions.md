---
title: "Module II — live operation & sessions"
description: >-
  The live operational overlay per agent session: current action, live
  tokens/cost, a derived state and a replayable timeline, plus the managed
  runs the plane launches under a provider profile. What it derives, what
  stays honestly empty, and the limits.
---

Module II is the **live operation** view of the estate: what every agent session
is doing right now, its live token and cost totals, a derived liveness state,
and a reconstructable timeline. Where module I (inventory) materializes the durable
estate, module II keeps a **live operational overlay** per session over the same
observation stream — and shows only what that stream honestly carries.

Olivares <!-- release -->0.1<!-- /release --> also **launches** official provider CLIs as owned children under a
[provider profile](/how-to/operate-provider-sessions/). That managed path is
the same module. It does not replace the overlay, and it does not merge two
homes that announce the same provider session id (`CHANGELOG.md` `[26.9.0]` B1/B2).

## What it is

Module II is a bus-driven Core-layer module, sibling to inventory. It maintains a
live record keyed by each session's external reference, built from the cooperative
observation stream — never polled, never fabricated. Per session it tracks:

- the **current action** (the last tool used) and the resource/mode it touched;
- the **live token and cost totals**, read from cost samples (the canonical cost
  ledger and FinOps are module XI, not here — this is the live figure only);
- a **derived liveness state** (`cc_state`, still named for the original Claude
  Code derivation); and
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

## Provider profiles and `live_ref`

A **provider profile** is the durable identity of one configured provider
instance on one execution environment: driver, environment, and the canonical
`config_home` / `user_home`. It is not an authenticated provider account.
Register, rename, disable/enable and retire live under
`/v1/m/sessions/provider-profiles`. Paths appear only on the admin
`configuration` read. A launch names `provider_profile_ref`; the server
resolves the homes and persists a non-secret snapshot on the run before spawn
(`CHANGELOG.md` `[26.9.0]` B1; `web/src/features/agentops/types.ts`).

An observation folds into the live row of its **channel**, computed by the
server from the host-stamped source registration (`CHANGELOG.md` `[26.9.0]` B2):

| Channel | Meaning |
|---|---|
| `legacy` | no registration |
| `observed` | a source dedicated to a profile by a binding approved at host admission for the exact applied revision |
| `source` | a known registration with no verifiable profile |
| `managed` | a run the plane launched; the only row carrying `canonical_sid` and `run_ref` |

Every live row exposes `live_ref` and `attribution`. Two homes that announce
the same provider session id are two rows with two timelines. Read one row
with `GET /v1/m/sessions/live/by-id/{live_ref}` (and its timeline / stream /
runs query). Bare external-id routes stay and are **legacy**: they answer for
the legacy row only.

Drivers are registered on each node at boot. At launch, the engine uses the
newest verified managed install, then the CLI on the engine's `PATH`.
`OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`, `_CODEX_BIN`, `_GROK_BIN` and `_OPENCODE_BIN`
explicitly override this resolution (see [Configuration](/reference/configuration/)).
If no executable resolves, launch is refused. Other readiness checks, including
profile authentication and launch policy, still apply. Operator steps:
[Operate a provider session](/how-to/operate-provider-sessions/).

## Managed-run lifecycle and restart boundaries

Sessions is a kernel module: it keeps running even when it is not in the saved
module selection. Turning off an optional module does not reset session data.

The managed-run API under `/v1/m/sessions/runs` and `olivares session` use the
same runtime. Run reads require `sessions:run:read`; launch, input, stop and
resume require `sessions:run:write`. A viewer can read a run without being
allowed to stop it. Tenant and workspace authority still apply to each run.

```sh
olivares session ls -o json
olivares session show <session> -o json
olivares session send <session> "continue the task"
olivares session stop <session>
olivares session resume <session>
```

Stopped runs, their provider-profile and workspace references, and lifecycle
events survive an engine restart. Resume starts a new owned child for the
existing run. Live attach output is an in-memory bounded ring; retained run
metadata and lifecycle events do not promise a persisted conversation transcript.
Recording policy and the tool's own resume support remain separate requirements.

## Session filesystem confinement

Managed sessions and local stdio MCP servers require Linux Landlock. If the
engine host cannot provide it, the launch is refused before the tool starts;
no permission preset opts out. Enable Landlock on the engine host and run
`olivares doctor` there to check availability. A doctor check on another machine
only describes that machine, not the remote engine.

Confined children receive access to their session folder, profile homes and a
private temporary directory. The engine database, secret-store key and engine
configuration stay outside those grants. This filesystem boundary does not
provide network isolation or container isolation.

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
- [Connect Claude Code](/how-to/connect-claude-code/) — start producing the cooperative live stream.
- [Operate a provider session](/how-to/operate-provider-sessions/) — register a profile and launch Codex, Grok or Claude.
- [Honesty & limits](/start/honesty-and-limits/) — what the product does and does not do today.
