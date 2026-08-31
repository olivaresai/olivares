---
title: "Posture export to control towers"
description: >-
  A read-only, outbound projection of the engine's ground-truth posture —
  discovered inventory, least-privilege drift and security findings — that a
  control tower pulls to enrich its own view. A neutral-JSON projection, not a
  verified native push.
---

Posture export (`modules/posture-export`) is the engine's **outbound posture
surface**: a single read-only endpoint a control tower polls to enrich its own
inventory with the engine's ground-truth [access graph](/reference/modules/iii-access-map/),
least-privilege drift, discovered inventory and security posture. It is the
"integrate, not compete" side of the platform — it never emits identity (that is
inbound, owned by [governance](/reference/modules/vi-governance/)), only
posture, and it changes nothing.

## What it exposes

One route, `GET /v1/m/posture/export`, gated by `posture:export:read` and pinned
to a single tenant scope. The response is a neutral JSON document assembled
inside **one audited transaction** with three projections:

- **`inventory`** — active discovered entities (kind, ref, status, signal
  sources, hosts, first/last seen, occurrence count), optionally filtered by
  `?kind=`.
- **`posture_drift`** — the reconciled least-privilege drift: observed-but-not-
  permitted accesses, plus unused-grant and inventory-grant counts.
- **`findings`** — security findings projected as refs and a `detail_hash` only,
  filterable by `?severity=` floor and `?category=`.

Every export is **minimal-data** — refs, hashes and relations only, never a raw
payload or secret — and a defensive redact pass scrubs every free-form field.
The export itself moves data off-box, so it **self-audits** to the ledger with
the real principal in the same transaction as the reads.

## Maturity and bounded context

**PARTIAL.** The export action is live and audited; what is *not* verified is the
other end. The ingest formats of the named towers — **Microsoft Agent 365** and
**ServiceNow AI Control Tower** — have no primary-source API the engine could
validate against, so this is an **honest neutral-JSON projection a tower pulls
(or an operator routes through a configured sink), explicitly NOT a working
native push**. Each response carries that provenance note inline.

Per-request caps bound inventory, drift and findings; a partial export reports
its own truncation flags and is never labelled authoritative.

## Related

- [Forward audit to Splunk](/how-to/forward-audit-to-splunk/) — the
  `siemforward` plane, the *push* counterpart that ships the sealed ledger and
  findings to a SIEM tower.
- [Module XIII — compliance & regulatory](/reference/modules/xiii-compliance/) —
  the sealed evidence this posture shares its ground truth with.
- [Module III — access & resource map](/reference/modules/iii-access-map/) — the
  reconciled drift the export projects.
- [Honesty & limits](/start/honesty-and-limits/) — why this is a projection, not
  a verified push.
- [Modules catalog](/reference/modules/overview/) — where posture export sits
  among the 30 shipped modules.
