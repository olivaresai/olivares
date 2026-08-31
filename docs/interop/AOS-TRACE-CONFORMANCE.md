<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# OWASP AOS trace profile — limited conformance statement

> **Status: session and guardian-decision trace subset implemented against AOS
> v0.1.0 Public Preview. AOS labels both its OpenTelemetry and OCSF trace
> specifications “Working draft”; it is not stable or GA.** Sources and wire
> shape were verified on 2026-07-20 against the official [AOS trace overview],
> [OCSF mapping], [supported events], and [OWASP repository version].

## Scope of the statement

`sdk/siemwire.OCSF` emits the AOS **Session > Turn > Step** context and an
optional guardian-agent decision for Olivares PEP outcomes. This is a limited
conformance statement for the **session/decision trace profile only**. It is not
a claim of full AOS/ASOP implementation, certification, support for every AOS
event family, or compatibility with a future stable AOS release.

When `OCSFInput.AOS` is present, the encoder fixes the AOS OCSF envelope to the
profile's API Activity mapping:

| OCSF attribute | Emitted value |
|---|---|
| `class_uid` / `class_name` | `6003` / `API Activity` |
| `category_uid` / `category_name` | `6` / `Application Activity` |
| `activity_id` | `1` |
| `type_uid` | `600301` |

The AOS extension is emitted only inside the existing OCSF `unmapped`
container:

| AOS trace concept | Wire path |
|---|---|
| Governed agent | `unmapped.aos.context.agent.{id,name,version}` |
| Session | `unmapped.aos.context.session.id` |
| Step and turn | `unmapped.aos.step.{id,type,turn_id}` |
| Operation classification | `unmapped.aos.step.operation.type` |
| PEP verdict | `unmapped.aos.decision.decision` (`allow`, `deny`, or `modify`) |
| Decision explanation | `unmapped.aos.decision.{reasoning,reasonCode,message}` |
| Modified request | `unmapped.aos.decision.modifiedRequest.{ref,sha256}`, only for `modify` |

The pre-existing AOS agent marker remains at `unmapped["actor.type_id"] = 99`
and `unmapped["actor.type"] = "AI Agent"`. It is deliberately **not** moved to
`actor.user`: that shape is not valid against the pinned OCSF 1.8.0 API Activity
schema used by this encoder.

## OCSF validity boundary

`unmapped.aos.*` fields are **not first-class OCSF attributes**. They do not
themselves validate as an OCSF AOS profile or schema extension. The OCSF 1.8.0
validator validates the API Activity envelope and permits the nested object only
because it is parked under `unmapped`; the AOS semantics are covered by explicit
shape tests in `sdk/siemwire/ocsf_test.go`.

This boundary is intentional. Adding `context`, `step`, or `decision` at the
OCSF event root would violate the vendored official schema's
`additionalProperties: false` constraint. The encoder therefore preserves a
valid OCSF event while carrying the draft AOS richness for AOS-aware consumers.

## Data-minimization profile

The session/decision binding does not expose user context, prompts, message
content, tool arguments/results, request bodies, credentials, API keys, or
modified request payloads. Agent/session/turn/step values are opaque references.
For a `modify` decision, `modifiedRequest` can contain only an opaque `ref`
and/or a SHA-256 fingerprint.

The required `reasoning` and `message` strings must contain sanitized policy
metadata, never copied input or output. The encoder constrains the wire shape; it
does not perform semantic DLP over those caller-supplied strings. Callers remain
responsible for applying Olivares redaction before constructing the event.

## Crosswalk to OWASP Agentic AI — Threats and Mitigations (T1–T15)

The canonical risk catalog is the `owasp_agentic_tm` entry in
`modules/compliance/frameworks.go` — "OWASP Agentic AI — Threats and Mitigations
(T1–T15)", pinned there to the official OWASP GenAI Security Project source. It
remains the single source for control titles, requirements, criteria, capability
mappings, version pin, source URL and disclaimer; this page does **not** duplicate
that catalog. Operators can retrieve the same entry from
`GET /v1/m/compliance/frameworks/owasp_agentic_tm`.

The trace fields add observability evidence relevant to that catalog as follows
(the control IDs are the catalog's own T1–T15):

| AOS evidence signal | Threats (T1–T15) informed | Evidence contribution only |
|---|---|---|
| Agent identity and version | T8 (Repudiation & Untraceability), T9 (Identity Spoofing & Impersonation) | Attributes the action to a versioned governed agent; it does not prove identity integrity. |
| Session/turn/step hierarchy | T8 (Repudiation & Untraceability), T13 (Rogue Agents in Multi-Agent Systems) | Reconstructs where an action occurred and supports cross-step/cross-agent investigation; it is not a detector. |
| Step operation type | T2 (Tool Misuse), T11 (Unexpected RCE and Code Attacks) | Classifies the attempted operation without logging its arguments or output. |
| PEP `allow`/`deny`/`modify` plus reason code | T2 (Tool Misuse), T3 (Privilege Compromise), T6 (Intent Breaking & Goal Manipulation), T10 (Overwhelming Human-in-the-Loop) | Records the guardian outcome and policy rationale; it does not by itself satisfy the catalog criterion. |
| Modified-request reference/fingerprint | T2 (Tool Misuse), T6 (Intent Breaking & Goal Manipulation) | Correlates a policy-narrowed request without retaining its content or credentials. |

This is a telemetry crosswalk, not a certification or a claim that emitting a
trace satisfies a T1–T15 threat mitigation. Control status continues to come from
the capability/evidence engine behind the canonical framework entry.

## Verification

The shape tests cover a `deny` decision, the conditional `modify` reference,
the session-only shape, caller `unmapped` merging, absence of payload/PII-shaped
keys, preservation of the historical actor marker, and validation of every
emitted envelope against the vendored official OCSF 1.8.0 API Activity schema.

[AOS trace overview]: https://aos.owasp.org/spec/trace/
[OCSF mapping]: https://aos.owasp.org/spec/trace/extend_ocsf/
[supported events]: https://aos.owasp.org/spec/trace/events/
[OWASP repository version]: https://github.com/OWASP/www-project-agent-observability-standard/blob/dev/version.txt
