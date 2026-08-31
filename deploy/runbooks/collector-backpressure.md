<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Runbook — collector backpressure (ingest stalls)

**Severity:** SEV2 (degraded; ingest SLO at risk). The plane stays up and **no events are lost** — the bus blocks rather than drops — but ingest slows and the ingest-p99 SLO breaches.

## Symptom
Collectors push observations faster than the core can durably persist them, so ingest latency climbs and throughput plateaus.

## Detect
- `OlivaresIngestP99High` fires: `olivares:ingest_latency:p99_5m > 0.25` (the ingest-latency SLI; it **rises under backpressure** because a full subscriber queue blocks the publish *inside* `Ingest`).
- `olivares_ingest_observations_total` goes **flat** (stops rising) while collectors are active — the de-facto saturation tell.
- Collector logs show slow/stalled gRPC `Push` `Send`; core gRPC `Push` latency climbs.
- **Do NOT look for OOM.** Buffers are bounded (256 events per subscriber), so memory does **not** grow — "no OOM" is false reassurance here.

## Diagnose — it is always a slow SUBSCRIBER
The in-process bus gives each subscriber a 256-deep channel drained by its own goroutine. `Publish` does a **blocking** send into each matching subscriber's channel (`core/eventbus/inproc.go`), so a slow handler for an event type blocks *only* publishers of that type — which, for the ingest path, blocks `Runtime.Ingest` → the gRPC `Push` `Recv` loop → HTTP/2 flow control → the collector's `Send`. Backpressure is intentional and end-to-end (at-least-once, no silent loss).

Find the slow subscriber:
- **A direct signal exists:** `olivares_eventbus_queue_depth{subscriber}` vs `olivares_eventbus_queue_capacity{subscriber}` names the saturated module, and `olivares_eventbus_publish_blocked_total` counts the backpressure events themselves (the `OlivaresEventBusSaturated` alert tickets on >90% sustained). Logs (`eventbus: handler returned error` / `eventbus: handler panicked`, with `event_type` and `subscriber`) remain the detail trail — note that on an HA standby an expected `ErrNotLeader` handler error logs at Debug, not Warn, so steady-state standby noise no longer pollutes this grep.
- The most common culprit is a module/output handler doing **slow store writes** — i.e. you are hitting the single-writer store ceiling. Confirm: store-write latency / the SQLite `busy_timeout=5000ms` tail (writes blocking up to 5s) and the events/sec ceiling in `docs/SIZING-AND-CAPACITY.md`. Check `olivares_store_up` and store CPU/IO.

## Mitigate (honest about today's levers)
1. **Fix or restart the slow subscriber** (the output connector / module whose handler is slow or erroring). This is the primary lever.
2. **Reduce offered load** at the collector (lower push rate / batch) until the subscriber recovers.
3. **If the bottleneck is store writes** (the usual case), the fix is **capacity**, not tuning: this is the SQLite single-writer ceiling. Move SQLite → Postgres (`docs/SIZING-AND-CAPACITY.md §3`) or reduce write amplification. Throwing concurrency at SQLite makes it *worse* (measured).
4. **Buffer-size knob:** with the distributed-bus config, `OLIVARES_BUS_CONFIG`'s `buffer` field sizes the per-subscriber queue on the bridged bus; the in-proc default deployment still uses the fixed 256 (no env knob — by design, the queue is a smoothing buffer, not a fix for a slow subscriber). The queue-depth gauge + blocked-publish counter are DELIVERED (docs/17 §5).

## NATS bridge caveat (only when `OLIVARES_BUS_CONFIG` is set)
The "no events are lost — the bus blocks rather than drops" statement above is the LOCAL path and
still holds. The cross-node bridge is **at-most-once**: a saturated bridge subscription fills its
pending buffer (`olivares_eventbus_bridge_pending_messages`) and then **drops remote events,
counted** in `olivares_eventbus_bridge_dropped_total` (`OlivaresEventBusBridgeDropping` tickets on
any increase). A bridge drop means a STANDBY-origin observation did not reach the leader —
re-gather will re-emit it eventually (source-level at-least-once); sustained drops mean the leader
node is the slow consumer: treat as this runbook's main flow. `olivares_eventbus_bridge_connected
== 0` means cross-node delivery is suspended entirely (`OlivaresEventBusBridgeDisconnected` pages;
check the NATS server and the `OLIVARES_BUS_CONFIG` URL).

## Verify
`olivares:ingest_latency:p99_5m` back under 0.25s and `olivares_ingest_observations_total` rising again at the collector's offered rate.

## Prevent
- Size for the sustained write rate on your storage class (`docs/SIZING-AND-CAPACITY.md §4`); write throughput is fsync-bound — prefer local NVMe, or Postgres for scale/HA.
- Keep output connectors fast / async at their own edge; a slow downstream (SIEM, webhook) must not become a synchronous bus subscriber on the ingest hot path.
- Watch the ingest-p99 burn before it breaches (the alert `for: 10m` gives lead time).
