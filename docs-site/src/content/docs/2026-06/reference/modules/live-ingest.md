---
title: Live-ingest — the in-process observe producer
description: 'The supporting "live-tap" module that publishes the detective
  events an out-of-process connector cannot emit. Deny-closed and minimal-data:
  it moves no raw content, and every observe half it owns is honestly empty
  rather than faked.'
slug: 2026-06/reference/modules/live-ingest
---

Live-ingest (`modules/liveingest`) is a **supporting module**, not one of the numbered
23\. It exists for one architectural reason: an out-of-process `SourceConnector` can
stream only the sealed observation sum (edge / cost / finding) over its gRPC contract,
which has no event RPC and no text field — so it **cannot publish a detective event**.
Only an in-process module holds the bus publish capability, so live-ingest is the
"live-tap" half that emits those events for the modules that already consume them.

## What it is

The control plane's Claude telemetry connector runs out-of-process as an embedded
plugin; its `Gather` stream carries only the frozen `Observation` `oneof`. That wire
contract is deliberately frozen (breaking-change-checked; see the
[API stability policy](/2026-06/reference/api-stability/)) and carries no excerpt or
text surface. Live-ingest is the in-process producer that supplies the two events the
connector structurally cannot: `guardrail.observed` for [module IX](/2026-06/reference/modules/ix-security/)
and `voice.telemetry.observed` for module XVI. It owns no entities and no REST surface;
it is a publisher onto the [event bus](/2026-06/reference/events/).

## What it produces — `guardrail.observed`

This is the missing producer for the security detector chain that already consumes
[`guardrail.observed`](/2026-06/reference/events/). It is **deny-closed and opt-in**:

* **Default (inspection off).** The module subscribes to nothing, publishes nothing,
  and logs its empty half visibly — never a silent no-op.
* **With the operator opt-in on.** It subscribes to `edge.observed` and, for an edge
  whose resource is a resolved tool reference, derives a **bounded, already-redacted**
  `tool_args` excerpt and publishes it as an `ObservedText` carrying only non-sensitive
  reference fields. The excerpt is the resource *identifier* the connector already
  redacted at source (a sanitized path, a host+path with no query or credentials, a
  Bash program name with its arguments dropped, an MCP tool reference). Live-ingest
  bounds it and the security chain clamps it again — triple defense. The argument's
  **content is discarded at the connector and never reaches the bus.**

The detector chain then emits a finding per detection automatically, over real traffic.

## What it produces — `voice.telemetry.observed`

A wired in-process producer for allow-listed voice/realtime turn metadata only — never
audio and never transcript text. The payload is a typed value that by construction
cannot carry audio, transcript or PII, and the consumer rejects any sample with a key
outside the allow-list or a missing session/agent reference. With no voice realtime
backend in this build, **nothing calls it**: the observe half is honestly dormant and
fabricates no telemetry until a backend feeds it.

:::caution[Honest limits]
* **Deny-closed by default.** `guardrail.observed` publishes nothing unless the operator
  explicitly opts in; the empty half is logged, not hidden.
* **Detection coverage is narrow, and stated as such.** Because only already-redacted
  argument *references* are available in-process, the realistic detections on this
  surface are PII or a secret embedded in a reference, and anomalous/sensitive-resource
  patterns. **Prompt-injection and jailbreak are out of reach** — they need the argument
  *content*, which the connector discards. The `input` / `output` / `tool_result`
  surfaces require an in-process content source that this build does not have under the
  out-of-process transport and frozen wire.
* **Voice telemetry is dormant.** No realtime backend exists in this build, so that half
  produces nothing rather than inventing samples.
* **It never moves raw content and never widens the connector's capture.** Minimal-data
  is a property of the wire itself, not a setting layered on top.
:::

## Related

* [Event bus reference](/2026-06/reference/events/) — the `guardrail.observed` / `ObservedText`
  payload (a redacted excerpt on a JSON fallback, not the sealed sum) and `edge.observed`.
* [Module IX — security, guardrails & audit](/2026-06/reference/modules/ix-security/) — the
  detector chain that consumes the `guardrail.observed` feed this module publishes.
* [Module XVI — voice & realtime agents](/2026-06/reference/modules/xvi-voice/) — the consumer
  of the (dormant) `voice.telemetry.observed` half.
* [Module II — live operation & sessions](/2026-06/reference/modules/ii-sessions/) — derives its
  own `goal` / `agent_ref` / `summary` directly from signals it already consumes, rather
  than via a live-ingest event.
* [Modules catalog](/2026-06/reference/modules/overview/) — the 28 modules and the honest
  Govern/Observe-vs-Actuate split this supporting module backs.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — where in-process
  modules and out-of-process connectors sit.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — why empty halves are declared, not faked.
