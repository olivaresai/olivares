---
title: Module XVI — voice & realtime agents
description: The observe-and-govern plane for conversational/realtime agents. It
  governs who may open a voice session, with which model and provider, under a
  default-DENY policy — and tracks session metadata with a hard ban on any audio
  or transcript content.
slug: 2026-06/reference/modules/xvi-voice
---

Module XVI governs **conversational and realtime agents**. It is an
**observe-and-govern** plane: it does **not** reimplement a voice SDK (Realtime API,
WebRTC, ASR or TTS) and it never opens a media stream itself. It decides *who* may
open a voice session, with *which* model and provider, under *which* policy, and
tracks that session's metadata — never its content.

## What it is

Opening a voice interface is treated as a **privileged action**, not a free
operation. Policy is **default-DENY**: a session with no allowing policy is refused.
An open is **two-phase** and **human-in-the-loop gated** through the
[approval gate](/2026-06/how-to/govern-and-approve/); it is bound to a `plan_hash` so an
approval cannot be silently upgraded to a stronger model (anti-TOCTOU), audited to
the **real principal** (never `system`), and evidenced **append-only**. The module
itself never calls a provider — actuation leaves through a separate dispatch seam.

The other half is **observation**: the module tracks session metadata only —
derived state (live/idle/ended, computed at read time from activity recency, with no
stored lifecycle column), turn counts, duration, latency (honest avg and max from
real samples), and BCP-47 language. From this it raises governance **findings**: a
policy violation when telemetry names an agent/model/provider no policy allows, a
degraded-latency finding when latency crosses a policy SLA, and an ungoverned-open
finding when an open is attempted with no gate wired — the gap is surfaced and the
open is still denied.

## Contract & entities

The module declares three entities in the shared data model:

| Entity | Mutability | Purpose |
|---|---|---|
| **session** | mutable (upsert) | session metadata; **zero content** |
| **policy** | mutable | governance declaration — who may open with which model/provider (default-DENY) |
| **decision** | **append-only** | immutable ledger of open/close decisions |

A policy matches on agent, allowed model and allowed provider (each specific or
wildcard), with optional session-minute and latency-SLA bounds. **No matching policy
means DENY.** The decision ledger records each `open_request`, `open` and `close`
with its policy verdict, gate status and outcome status. Read access is the viewer
role and up; declaring a policy and opening a session are administrative,
tenant-scoped and audited. These module routes are reachable but deliberately not
part of the served OpenAPI document — their field-level shapes live in the product's
typed interfaces. Dollar amounts are **not** here; FinOps (module XI) owns cost.

## What it consumes & produces

The module owns a deny-closed ingestion seam — its own `voice.telemetry.observed`
event — through which an **in-process** probe would feed session metadata. The wire
is **minimal-data by construction**: the telemetry parser carries an allow-list and
**rejects the whole event** if it sees a forbidden key, so no audio, transcript text,
ASR/TTS text, prompt/response content or speaker PII can ever be persisted. The only
transcript signal kept is a one-way hash of an *external* transcript **locator** —
proof a transcript exists, never the transcript. Governance findings are emitted as
[`finding.reported`](/2026-06/reference/events/) with hashed detail, after commit.

## Actuate status

A governed open dispatches **live**: once a voice dispatcher is provisioned by
the operator, an approved open mints a **server-side ephemeral credential** and
returns only that credential plus connection coordinates — model, voice, tools and
turn-detection are fixed **from the policy**, never from the client, and the
provider's master key never leaves the server. Without that provisioning the
dispatch seam is **deny-closed**: an approved open is honestly recorded as
"declared, not opened" rather than faked.

:::caution[Honest limits]
* **Observation is dormant in this build.** There is no voice connector or probe
  shipped yet, so the observe half stays **honestly empty** until an in-process probe
  publishes telemetry. The module warns at startup when nothing is feeding it. An
  out-of-process plugin **cannot** feed it (the gRPC control-plane proto carries no
  event RPC) — the probe must be in-process.
* **No content, ever.** This is a hard property of the wire, not a setting: the
  schema has no content column and the parser rejects unknown keys. Latency is shown
  as honest avg/max from real samples — never a fabricated p50/p95.
* **No "stall" finding.** A voice session ending is normal silence (like a finished
  agent). With no honest baseline, a stall finding would be a false positive, so it is
  deliberately omitted.
* **Pre-1.0.** Like much of the platform, this module is design-stage in depth — see
  [Honesty & limits](/2026-06/start/honesty-and-limits/).
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — where module XVI sits and its actuate status.
* [Event bus reference](/2026-06/reference/events/) — `finding.reported` carries the voice findings.
* [Module IV — orchestration](/2026-06/reference/modules/iv-orchestration/) — the sibling dispatch seam (live fire).
* [Module X — model & provider routing](/2026-06/reference/modules/x-models/) — which models a policy may allow.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — the two-phase open gate in practice.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — the observe/govern/actuate split.
