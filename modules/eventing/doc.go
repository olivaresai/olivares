// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package eventing is module XIX's eventing half: the platform that
// turns the in-process event bus (S02) into an EXTERNAL subscription surface —
// typed event subscriptions with delivery guarantees — closing gap EXT-2
//. The notify module (module XV) remains the alert ROUTER
// to operator-provisioned destinations; this module is the integrator-facing
// eventing platform: tenant self-service subscriptions, durable at-least-once
// delivery, retries with backoff, a dead-letter queue, and replay from a
// cursor.
//
// # Shape
//
//   - SUBSCRIPTION (eventing_subscription, mutable): event types + an optional
//     source filter + a consumer endpoint URL + a sealed HMAC signing secret +
//     the ROLE under which deliveries are authorized. The secret is generated
//     server-side, returned exactly once, and persisted only through the
//     SecretSealer seam (encrypted at rest) — never in clear (docs/SECURITY-HARDENING.md).
//   - EVENT LOG (eventing_event): the durable per-tenant log the bus lacks. The
//     bus handler captures each cataloged event — when at least one enabled
//     subscription matches — assigning a per-tenant monotonic Seq from the
//     eventing_cursor allocator row (bumped in the same transaction; unique
//     (tenant_id, seq) backstop + bounded optimistic retry, the pattern).
//     The cursor — not max(seq) over the log — is the allocator, so pruning the
//     whole log never regresses the sequence. The log is the replay buffer; it
//     is deliberately NOT AppendOnly so the retention sweep can prune it
//     (application code never updates rows).
//   - DELIVERY (eventing_delivery, mutable): one row per (event, subscription)
//     enqueued in the SAME transaction as the captured event. Statuses:
//     queued → delivering → delivered | dead | denied. "dead" is the DLQ;
//     "denied" is the per-event RBAC filter saying no. Workers claim a row by
//     optimistic version (safe under concurrency and HA), attempt one signed
//     POST, and either finish it or schedule the next attempt on the backoff
//     schedule.
//
// # Delivery contract
//
// At-least-once with consumer idempotency keys (exactly-once was rejected as
// a false promise). Every attempt
// carries the Stripe-style signature (X-Olivares-Timestamp +
// X-Olivares-Signature "t=<ts>,v1=<hexsig>", HMAC-SHA256 over "<ts>.<body>",
// verified with connectors/webhook.VerifyWithin) plus X-Olivares-Event (the
// stable event id — the idempotency key), X-Olivares-Event-Type and
// X-Olivares-Delivery. The body is the event envelope (the documented
// sdk/event.Event shape, Go field names, plus Seq) with the typed payload under
// "Payload". 2xx acknowledges; 408/425/429/5xx and network errors retry on the
// schedule; any other status is terminal (dead). Redirects are never followed.
//
// # Authorization (deny-closed)
//
// A subscription only receives events its recorded role may see: before every
// attempt the dispatcher evaluates the full RBAC+ABAC pipeline
// (auth.ScopedPrincipal + Authorizer.Allowed, via the Authz seam) against the
// catalog's type→permission mapping. No mapping entry, no authorizer wired, an
// unknown role, or a policy denial all mean NO delivery. guardrail.observed is
// gated editor+ ("security:observed:read", a privileged read) because its
// payload is redacted observed agent text.
//
// # Seams (wired by the composition root, fail-closed by default)
//
//   - Authz: the engine Authorizer. Unwired → nothing is delivered (loud).
//   - SecretSealer: secret-at-rest encryption. Unwired → subscriptions cannot
//     be created (loud).
//   - The cross-tenant dispatch pump (cmd/olivares/eventingpump.go) calls
//     DispatchDue/PruneExpired per business tenant on the runtime scheduler —
//     tenant enumeration is a System operation a module cannot perform
//. An in-process nudge channel gives fresh events low-latency
//     first attempts on the node that captured them.
//
// The in-proc bus is at-most-once with drop-on-shutdown; durability begins at
// the capture transaction. Events published while no matching enabled
// subscription exists are NOT captured (storage-frugal; replay reaches back
// only to capture, bounded by the retention window). The distributed bus is
// SIEM and control-tower sinks build on this platform.
package eventing
