---
title: "Eventing & webhooks platform"
description: >-
  The integrator-facing subscription surface over the engine's event bus:
  typed event subscriptions with signed webhook delivery, durable
  at-least-once semantics, retry/backoff, a dead-letter queue and cursor
  replay. It is the durability boundary the in-process bus does not provide.
---

Eventing (`modules/eventing`, **LIVE**) turns the engine's in-process
event bus into an **external subscription surface**. Where the bus itself is
at-most-once and drops on shutdown, this module is the **durability boundary**:
once an event is captured in the capture transaction, delivery is durable and
auditable. Its routes mount under `/v1/m/eventing/`.

## What you subscribe to

A **subscription** records the event types it wants, an optional source filter,
a consumer endpoint URL, the role its deliveries are authorized under, and a
server-generated HMAC signing secret (returned exactly once, then held only
through the sealed-at-rest seam). The subscribable types come from a typed
catalog — `GET /event-types` returns each type with its stability tier and the
permission that gates it. Managing subscriptions is privileged and audited:
create/update/rotate-secret is write-tier; delete, replay, redeliver and test
deliveries are admin-tier.

## Delivery guarantees

Delivery is **at-least-once with consumer idempotency keys** — exactly-once was
rejected as a false promise. Each captured event becomes one durable delivery
row per matching subscription, enqueued in the same transaction. Workers claim
rows by optimistic version (safe under HA), POST the signed event envelope, and
either acknowledge (2xx) or schedule the next attempt:

- **Retry/backoff** — 408/425/429/5xx and network errors retry on a backoff
  schedule; any other status is terminal. Redirects are never followed.
- **Dead-letter queue** — exhausted deliveries land in the `dead` status; a
  `denied` status records a per-event RBAC refusal.
- **Cursor replay** — a per-tenant monotonic sequence (allocated from a cursor
  row, not `max(seq)`) lets you replay from a point in the durable log, bounded
  by the retention window.

Every attempt carries the Stripe-style timestamped HMAC-SHA256 signature plus a
stable event id as the idempotency key. Before each attempt the dispatcher runs
the full deny-closed RBAC+ABAC pipeline against the subscription's role, so an
outbound event is filtered exactly as a live read would be.

## Bounded context, stated plainly

- The **in-process bus is at-most-once** with drop-on-shutdown; durability
  begins at the capture transaction, not at publish. Events published while no
  enabled subscription matches are not captured (storage-frugal), so replay
  reaches back only to capture.
- The multi-node NATS bridge is honestly **at-most-once** — this platform is
  the durable layer above it, not a guarantee about the distributed bus itself.
- It is the **integrator-facing** surface; [notify](/reference/modules/xv-notify/)
  remains the operator-facing alert router. See
  [honesty and limits](/start/honesty-and-limits/) for the live / on-demand /
  deny-closed conventions.

## Related

- [SIEM forwarding](/reference/modules/siemforward/) — ships sealed ledger and
  findings to SIEM towers; built directly on this platform.
- [Notify](/reference/modules/xv-notify/) — the operator-facing alert router to
  provisioned destinations.
- [Events reference](/reference/events/) — the event vocabulary you subscribe
  to and the envelope shape delivered.
