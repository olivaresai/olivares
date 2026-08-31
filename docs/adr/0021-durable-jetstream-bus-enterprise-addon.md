# ADR-0021: Durable JetStream event-bus backend (at-least-once + bus-boundary dedup) as a closed enterprise add-on

- **Status:** accepted (extends ADR-0017's "JetStream remains the upgrade path")
- **Date:** 2026-06-24
- **Deciders:** Fran Olivares (scale/reliability lever); design re-anchored against HEAD + a subscriber-idempotency re-census
- **References:** ADR-0017 (the at-most-once Core-NATS bridge), ADR-0020 (enterprise private-repo distribution),
  `LICENSING.md`, `enterprise/durablebus`, `core/eventbus/natsbus`

## Context and problem statement

ADR-0017 shipped the distributed bus as in-proc local fan-out + a **Core-NATS, at-most-once** bridge,
and explicitly **rejected JetStream for v1** (option C) because the 2026-06-12 subscriber census found
most subscribers were not duplicate-safe — at-least-once would have shipped duplicates into handlers
that mishandle them. It left JetStream as "the at-least-once upgrade path, **gated on a subscriber
idempotency pass**".

A governance control plane cannot silently lose an event that triggers a DECISION. Under the open
bridge, a finding.reported / cost.sampled lost between HA nodes (server restart, reconnect-buffer
overflow, slow-consumer drop) is a silently-missed enforcement signal. The enterprise scale/reliability
tier (lever #4) needs to close this for the enforcement-event class — without the per-subscriber
idempotency pass ADR-0017 anticipated (a re-census confirmed the subscribers are still only
"idempotent **enough**": e.g. `modules/security` dedups findings by a **bounded best-effort scan**, not
a hard guarantee — `observed.go`, `anomaly.go`).

## Decision drivers

- **Resolve non-idempotency at the BUS, not by trusting handlers.** ADR-0017 gated JetStream on making
  every subscriber idempotent. That is fragile (a distributed invariant across ~17 handlers, re-broken
  by any future edit) and was never completed. A single owned dedup at the bus boundary is the durable
  fix: subscribers gain durability without each having to be correct forever.
- **No rug-pull, no hot-path regression.** ADR-0017's load-bearing constraint stands: the local in-proc
  hot path and the open Core-NATS bridge must be byte-for-byte unchanged in the community binary. The
  upgrade must be ADDITIVE.
- **Monetization timing (ADR-0020).** Durability/HA is an enterprise-tier lever. It ships as closed
  code behind the `enterprise` build tag, after the private-repo split made the tag a real boundary.

## Considered options

- **A. Replace the bridge with JetStream for ALL types.** Rejected: routes loss-tolerant high-volume
  observations (edge/metric) through RAFT storage, and would change the open bridge's behaviour
  (rug-pull). 
- **B. Durable JetStream for the ENFORCEMENT class only, embedding the open bridge for the rest
  (CHOSEN).**
- **C. Persistent per-subscriber dedup table in the store.** Rejected for Fase 1: an enterprise-only
  table breaks the open≡enterprise schema-parity gate, and an open table is a heavier change than the
  guarantee needs. The dedup state lives in JetStream KV instead (no store, no schema change).

## Decision outcome

Chosen: **B.** A closed add-on `enterprise/durablebus` (`//go:build enterprise`,
`LicenseRef-Olivares-Commercial`) that **embeds** the open `*natsbus.Bus` and adds a JetStream path for
the **enforcement set** (`finding.reported`, `cost.sampled`, `guardrail.observed`, `approval.requested`,
`policy.changed` — operator-overridable). Mechanics:

- **Sibling subject namespaces.** Durable events publish to `<durable_prefix>.<type>` (a JetStream
  stream, RAFT, replicas ≥ 3), DISJOINT from the Core bridge's `<subject_prefix>.>` — so a type is
  delivered by exactly one transport, never both. The embedded bridge is told to EXCLUDE the durable
  set from Core-bridging (`natsbus.Options.BridgeExclude`, inert in the open binary). Non-enforcement
  types keep the open bridge's at-most-once reach (no regression).
- **Publish confirms the PubAck** (`Nats-Msg-Id = event.ID`): a durable event is either durably stored
  or the failure is surfaced — never silently dropped; the stream's duplicate window collapses a retry
  / failover double-publish to one stored copy.
- **Leader-gated durable consumer** (ack-explicit), bound on promotion / stopped on demotion via an
  `Active()` watcher (the elector exposes no OnDemote); its server-side position survives failover.
  Enforcement runs once cluster-wide.
- **Dedup by event.ID at the inject boundary**, two tiers: an in-memory time window (fast, same-node)
  and a **JetStream KV** bucket (RAFT-replicated, TTL-bounded, survives crash/restart and dedupes
  across nodes). READ-before-inject (suppress a duplicate) + RECORD-after-inject (so a crash re-injects
  rather than loses).

**Honest semantics: at-least-once, NEVER exactly-once.** LOSS never happens under normal and
moderately-degraded operation (record-after-inject; a confirmed publish is durable; the consumer
resumes from its acked position). The ONE residual loss path is retention-bounded: the stream keeps a
message for at most `MaxAge` (default 72h, `LimitsPolicy`), so a stored event is dropped if NO leader
drains it for longer than `MaxAge` — a total-quorum-loss / multi-day leaderless or partitioned outage.
That window is made observable by the `olivares_durablebus_stream_pending` SLI (a backlog approaching
`MaxAge` is alertable), so it is never a silent drop; an operator raises `MaxAge` or restores a leader to
keep it at zero. A DUPLICATE is possible only in two bounded windows — the ≤2s leadership overlap and a
hard crash between inject and the dedup record — both absorbed downstream (the eventing capture's
`(tenant_id, event_id)` index and the security bounded-scan dedup). The open bridge stays at-most-once
and unchanged.

### Consequences

- **Good:** enforcement events survive cross-node delivery (at-least-once) with one owned dedup
  guarantee; the community binary is byte-identical (the add-on is absent; the one open seam,
  `BridgeExclude`, is inert); no store schema change (dedup lives in JetStream KV) ⇒ schema parity
  untouched; fail-boot-closed (a declared durable backend that cannot be established aborts the boot;
  an unlicensed enterprise binary degrades VISIBLY to the open Core-NATS bridge, never silently to
  single-node).
- **Bad / trade-offs:** durable delivery costs a JetStream round-trip on publish (PubAck) and a KV
  read on inject — acceptable for the moderate-volume enforcement class, and an operator can narrow the
  durable set; durable events reach subscribers only on the leader (via the consumer), so a node's own
  durable publishes are not locally fanned out (consistent with "enforcement only on the leader"); the
  bus license gate is boot-time (installing a license to enable durability needs a restart, unlike the
  hot-applied add-on entitlements).
- **Neutral:** Fase 2+ of the lever (DR ladder, multi-region, per-tenant silo/CMEK) is a documented
  roadmap (`enterprise/durablebus/doc.go`), NOT built.

## Why the alternatives were rejected

A rugs-pull the open bridge and taxes the hot path; C trades a small KV for a core schema change that
breaks the parity gate. B confines the change to closed, additive code and resolves ADR-0017's
duplicate-safety concern at the bus boundary instead of via the never-completed per-subscriber pass.
