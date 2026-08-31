# ADR-0018: Realtime voice backend — documented dormant posture in v1, integration post-v1

- **Status:** accepted
- **Date:** 2026-06-12
- **Deciders:** Fran Olivares
- **References:** `modules/liveingest/voice.go:28`
  (`PublishVoiceTelemetry`), `modules/voice` (module XVI)

## Context and problem statement

The voice telemetry probe is built end-to-end and validated: `liveingest.PublishVoiceTelemetry`
publishes an allow-listed `voice.Telemetry` as `voice.telemetry.observed`, and module XVI folds
it into session metadata with a strict re-validating consumer. Nothing calls the producer in any
production path — there is no realtime voice backend in the build — so the observe half is empty.
It is a pure seam. The question: integrate a backend (e.g. LiveKit) now, or
declare the posture?

## Decision outcome

**v1 ships the probe dormant and SAYS so.** The honest posture is already enforced in code: the
producer declines droppable samples and fabricates nothing; liveingest's `Start` logs "voice
telemetry probe wired but dormant — no realtime voice backend in this build emits turn metadata";
the observe half stays visibly empty rather than falsely full (never a silent gap —
and equally, never fabricated fullness). Integrating a concrete realtime backend (LiveKit or
equivalent) is a **post-v1 session, if and when there is demand**.

The scale-out work made the seam multi-node-honest on the way through: a future dispatcher feeding
the probe on ANY node now reaches the leader's voice module over the NATS bridge (the composition
root registers the `voice.Telemetry` payload decoder), so the dormant seam did not silently become
a single-node-only seam under HA.

### Consequences

- **Good:** no speculative dependency; the seam is tested (producer + consumer + NATS bridge
  decoder) so a future integration is wiring, not design.
- **Bad / trade-offs:** the voice observe pane stays empty in v1 — documented in the UI contract
  as a declared seam, which is the truthful state.
- **Neutral:** the decision is demand-gated, not architecture-gated.
