---
title: Module XV — output integrations & notifications
description: "The notification router of the control plane: it decides WHAT
  signal reaches WHOM, on WHICH channel and WHEN, and dispatches the redacted
  result through the output connectors — Slack/Teams, PagerDuty/Opsgenie, signed
  webhook, SIEM. The proven end-to-end actuation seam, with a deny-closed
  default and an evidence ledger."
slug: 2026-06/reference/modules/xv-notify
---

Module XV is the control plane's **notification router**: when any module turns an
alert into a finding on the event bus, this module decides which tenant route it
matches, builds a redacted notification, suppresses duplicates and storms, and
**dispatches it live** to the channels the company already runs. It owns the *decide
what/whom/when*; the output connectors own the *how* of delivery — it consumes that
transport, it never reimplements it.

## What it is

Every module in the product reports an alert as a minimal-data finding on the bus
([`finding.reported`](/2026-06/reference/events/)) with a namespaced `Kind` — reliability
(`health_subject_down`), spend (`finops_budget`), security (`security_guardrail`),
eval regression (`eval_regression`), residency (`compliance_residency_violation`),
orchestration cadence, voice, and more. Module XV subscribes to **only** that one
product-wide alert channel and routes by `Kind`, severity, source module and
subject. It deliberately does **not** subscribe to raw telemetry such as
`cost.sampled` or `edge.observed`: a spend *alert* arrives as a `finops_budget`
finding, not as a cost sample. This is the seam that turns the whole product's
findings into actionable notifications.

## Contract & entities

The module declares two tenant-scoped entities in the shared data model:

| Entity | Mode | What it holds |
|---|---|---|
| **route** | mutable, audited | A routing rule: a predicate over event types, finding-kind globs (e.g. `health_*`), minimum severity, source modules and subject kinds → a named **destination**, with per-route dedup and throttle windows and a priority. Holds **no destination credential** — only a non-secret destination name. |
| **delivery** | append-only | The evidence ledger of every delivery *attempt*: route, destination, finding kind, severity, subject reference, short title, a correlation hash, and an outcome class (`delivered`, `failed`, `no_dispatcher`, `unknown_destination`). |

On each finding the module evaluates the tenant's enabled routes in priority order;
every predicate dimension left empty means *any*, and glob matching supports exact or
`prefix*` forms. Matching happens inside a read view, **network delivery runs strictly
outside any store transaction**, and the outcome is then written to the append-only
ledger. Creating, changing or deleting a route, and sending a test notification, are
**privileged, self-audited** actions attributed to the real principal. The route and
delivery routes are reachable but deliberately not part of the served OpenAPI
contract; their field-level shapes live in the product's typed interfaces.

## What it consumes & produces

* **Consumes** [`finding.reported`](/2026-06/reference/events/) — the single product-wide
  alert channel. It is a router, not a probe or a meter: it never polls infrastructure
  and never measures.
* **Produces** outbound notifications through a dispatch seam, backed by the output
  connectors (Slack/Teams, PagerDuty/Opsgenie, signed webhook, and a SIEM destination
  covering Splunk/Elastic via CEF/LEEF/syslog/OTLP). A notification carries only the
  finding's already-safe display fields — title, kind, severity, subject reference and
  a correlation hash — **never** a payload, prompt, secret or PII. **Minimal-data is a
  property of the wire**, not an after-the-fact filter. The destination secret lives
  only in the connector configuration the operator provisions, referenced here by a
  non-secret name.

:::caution[Honest limits]
- **The default binary ships a deny-closed dispatcher.** Until an operator provisions
  destinations, the dispatcher is wired but empty: an unmatched delivery is recorded
  as `no_dispatcher` and a misconfigured or unknown-kind destination resolves to
  `unknown_destination` in the ledger. It **never fakes a success** — a non-delivery
  is always visible.
- **The outbound webhook is a destination connector, not an OpenAPI webhook.** It is
  an output channel the control plane pushes to, not a callback you register against
  the product's API.
- **Dedup and throttle suppress the *send*, not an outcome.** A deduplicated or
  throttled notification is intentionally **not** written to the delivery ledger (so
  it is never inflated). Every actual delivery *attempt*, by contrast, is recorded —
  `delivered`, `failed`, `no_dispatcher` and `unknown_destination` alike — so a
  non-delivery is always visible, never silently dropped.
- **The connector's raw error is never persisted or logged** — only a non-sensitive
  outcome class — because a transport error can carry the destination secret in its URL.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — where module XV sits and the Govern/Actuate split.
* [Push to your SIEM](/2026-06/how-to/cookbook/push-to-siem/) — the S2S push driver
  (`modules/siemforward`) that re-shapes findings and the sealed audit ledger into a
  tower's native dialect (OCSF/CEF/LEEF/syslog/OTLP) and rides the eventing
  platform's durable delivery — the push complement to the destinations above.
* [Event bus reference](/2026-06/reference/events/) — the `finding.reported` event and its `FindingReport` payload.
* [Access & resource map](/2026-06/reference/modules/iii-access-map/) — a sibling Core/Intelligence reference.
* [Forward audit to Splunk](/2026-06/how-to/forward-audit-to-splunk/) — wiring a SIEM destination.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — acting on the findings this module routes.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — the deny-closed-by-default posture across the product.
