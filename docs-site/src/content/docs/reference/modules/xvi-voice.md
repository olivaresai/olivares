---
title: "Module XVI — voice & realtime agents"
description: >-
  The observe-and-govern plane for conversational/realtime agents. It governs who
  may open a voice session, with which model and provider, under a default-DENY
  policy — and tracks session metadata with a hard ban on any audio or transcript
  content.
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
[approval gate](/how-to/govern-and-approve/); it is bound to a `plan_hash` so an
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
tenant-scoped and audited. These module routes are published in the separate **beta**
[module-route reference](/reference/api-beta/), not the stable core contract — their
field-level shapes live in the product's typed interfaces. Dollar amounts are **not**
here; FinOps (module XI) owns cost.

## What it consumes & produces

The module owns a deny-closed ingestion seam — its own `voice.telemetry.observed`
event — through which an **in-process** probe would feed session metadata. The wire
is **minimal-data by construction**: the telemetry parser carries an allow-list and
**rejects the whole event** if it sees a forbidden key, so no audio, transcript text,
ASR/TTS text, prompt/response content or speaker PII can ever be persisted. The only
transcript signal kept is a one-way hash of an *external* transcript **locator** —
proof a transcript exists, never the transcript. Governance findings are emitted as
[`finding.reported`](/reference/events/) with hashed detail, after commit.

## Actuate status

A governed open dispatches **live**: once a voice dispatcher is provisioned by
the operator, an approved open mints a **server-side ephemeral credential** and
returns that credential plus connection coordinates. The operator's session
configuration supplies voice and turn detection; a configured model overrides
the requested model. Without a configured model, the dispatcher uses the requested
model allowed by the tenant policy. The provider's master key never leaves the
server. Without that provisioning the
dispatch seam is **deny-closed**: an approved open is honestly recorded as
"declared, not opened" rather than faked.

## Configure and test a governed open

Enable the existing module with `olivares modules on voice`. The engine saves the
selection and restarts once if the running module set changes; `olivares modules ls`
shows whether it is running.
Voice requires FinOps and governance through the module spec. Turning voice off
keeps its policies, session metadata and decision ledger.

The dispatcher is provisioned on the engine host through
`OLIVARES_VOICE_DISPATCH_CONFIG`, the absolute path to an operator-owned JSON file.
Keep the file readable only by the engine account; provider master keys belong in
that file, never in CLI arguments, policy rows or a client connection bundle.
For an OpenAI adapter the shape is:

```json
{
  "providers": [
    {"ref": "openai", "kind": "openai", "api_key": "<server-held provider key>"}
  ],
  "policies": [
    {
      "agent_ref": "contact-agent",
      "provider_ref": "openai",
      "model": "<your permitted realtime model>",
      "voice": "marin",
      "max_duration_seconds": 60
    }
  ]
}
```

Set the environment variable for the engine service and restart it. A supplied
unreadable or invalid JSON file fails startup. An absent dispatcher configuration
keeps the declared-not-opened behavior. The operator file chooses provider adapters
and session settings; the tenant's voice policy separately authorizes the requested
agent, model and provider. Use the same model and provider references in both.

After signing in with `olivares login`, declare that policy and request approval:

```sh
olivares voice policies set --agent-ref contact-agent \
  --allowed-model-ref '<your permitted realtime model>' --allowed-provider-ref openai \
  --max-session-minutes 1 --max-latency-ms 300
olivares voice sessions open --session-ref contact-1 --agent-ref contact-agent \
  --model-ref '<your permitted realtime model>' --provider-ref openai -o json
```

The first request returns `op_status: requested`, an `approval_ref`, and CLI exit
7. It opens no media connection and mints no provider credential. Have the required
independent approvers approve that reference through the governance approval page
or `olivares governance approvals approve <approval-ref>`. For new requests through
the default local approval bridge, the requester cannot approve their own request,
including through another credential belonging to the same account. Repeat the
same open with `--approval-ref <approval-ref>`.
Policy refusal or an approval that is still pending returns 403 and CLI exit 3;
an adapter failure returns 502. Budget and estate-stop checks still apply.

A successful configured request returns `op_status: dispatched`. Its `dispatch_ref`
is a JSON string containing the short-lived `credential`, `connect` coordinates,
`transport`, model and expiry. Treat this response as a credential: do not paste it
into reports or logs. For OpenAI, the client exchanges an SDP offer at the returned
`connect` URL using the short-lived credential, then owns the WebRTC media
connection. Credential minting alone does not prove that media connected.
The decision ledger retains a SHA-256 fingerprint of a credential-bearing bundle,
not the connection credential. Older stored bundles are also fingerprinted when
read; existing append-only rows are not rewritten. Plain provider handles retain
their value.

Inspect the retained metadata and decisions with:

```sh
olivares voice sessions get contact-1 -o json
olivares voice sessions decisions contact-1 -o json
olivares voice policies ls -o json
```

Use the same data directory across engine restarts. The policy and append-only
decisions remain available after restart and after voice is turned off and back on;
provider credentials must still be provisioned separately. The JSON commands above
use the same tenant-scoped `/v1/m/voice` routes as the API.

:::caution[Honest limits]
- **Approval attribution has a scope.** The default local bridge retains the
  authenticated requester for new human-origin opens. Existing approvals retain
  their stored attribution. An explicitly configured service-token approval bridge
  attributes requests to its service credential; it does not provide the same
  initiating-person separation guarantee.
- **Observation needs a configured producer.** The optional OpenAI Realtime SIP
  call plane uses `OLIVARES_VOICE_CALL_CONFIG` for webhook verification, tenant and
  project attribution, together with the provider credentials in the dispatcher
  config. Without that configuration or an in-process telemetry producer, the
  observation half stays empty. Minting a WebRTC credential does not populate turn
  counts or latency. An out-of-process plugin cannot publish the module's event
  through the gRPC control plane, which exposes no event RPC.
- **The client owns minted-session media.** This module neither implements a
  WebRTC client nor closes a client's audio connection. The optional SIP call
  controller is a separate path. A local protocol test with synthetic audio does
  not qualify vendor speech, billing, SIP observation or media teardown.
- **Console scope is separate.** The voice view edits policies and displays
  sessions, decisions and metadata streams. Dispatcher provisioning and the
  client media connection are separate from that view; an API or CLI journey
  does not qualify a browser action.
- **No content, ever.** This is a hard property of the wire, not a setting: the
  schema has no content column and the parser rejects unknown keys. Latency is shown
  as honest avg/max from real samples — never a fabricated p50/p95.
- **No "stall" finding.** A voice session ending is normal silence (like a finished
  agent). With no honest baseline, a stall finding would be a false positive, so it is
  deliberately omitted.
- **Pre-1.0.** Like much of the platform, this module is design-stage in depth — see
  [Honesty & limits](/start/honesty-and-limits/).
:::

## Related

- [Modules catalog](/reference/modules/overview/) — where module XVI sits and its actuate status.
- [Event bus reference](/reference/events/) — `finding.reported` carries the voice findings.
- [Module IV — orchestration](/reference/modules/iv-orchestration/) — the sibling dispatch seam (live fire).
- [Module X — model & provider routing](/reference/modules/x-models/) — which models a policy may allow.
- [Govern and approve](/how-to/govern-and-approve/) — the two-phase open gate in practice.
- [Honesty & limits](/start/honesty-and-limits/) — the observe/govern/actuate split.
