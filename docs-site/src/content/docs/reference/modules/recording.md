---
title: "Privileged-session recording"
description: >-
  An immutable, ledger-anchored, replayable record of what a privileged
  operator session actually did on the engine's most sensitive surfaces:
  append-only frames, redacted on write, hash-chained per session and anchored
  into the signed audit ledger by PayloadHash. PAM-aligned, LIVE.
---

Recording (`modules/recording`) is the **privileged-session recording**
plane — the PAM-aligned control high-assurance buyers expect for consoles and
emergency access. It captures, as structured evidence, what a privileged
operator session actually did on the most sensitive module surfaces, and binds
that evidence to the tamper-evident audit ledger so it cannot be rewritten
after the fact. **Maturity: LIVE.**

## What it records

A **recording session** is the privileged window of one credential — a human
operator's login session, or a service token on the break-glass floor — inside
one tenant. Its **frames** are an append-only trail (DB-level immutability
guards), one frame per module-route action on a recorded surface: who, when,
the route shape and permission, redacted target identifiers, delegation, the
outcome, and a one-way SHA-256 of the request body. Frames are **structured
action events, never transcripts or bodies** — parameter values pass a bounded
redactor on write, so an email- or credential-shaped value never persists.

Capture sits at the engine's module-route wrapper and is **deny-closed**: on a
recorded surface, no appendable evidence means no privileged action. The
recorded scope is every break-glass route for every principal (the mandatory,
non-configurable floor) plus the per-tenant configured privileged namespaces.

## Integrity and replay

Each session's frames are **hash-chained**, and the chain tip is **anchored
into the signed audit ledger** by `PayloadHash` — an open event when the
session starts, periodic anchors as it runs, and a seal when it closes.
Rewriting any frame breaks both the session chain and its sealed ledger
anchors. `GET /sessions/{id}/verify` recomputes the chain and checks every
anchor; `GET /sessions/{id}/replay` reconstructs the human-readable timeline
correlated with the session's ledger window. The surface roots at
`/v1/m/recording/` (`sessions`, `replay`, `verify`, `seal`, `config`, `ack`).

## Bounded context, stated plainly

- It records **module routes** (`/v1/m/<ns>`); the core `/v1` surfaces are
  ledger-audited but not frame-recorded — replay correlates them through the
  session's ledger window instead.
- On an **active** session, frames past the last periodic anchor are bound only
  by the chain tip until the next anchor or seal; `verify` reports
  `anchored_through` so the boundary is explicit, never implied.
- It implements **no purge and no legal hold** — retention/legal-hold owns
  deletion; ledger anchors survive any purge.
- This is the recording subsystem the **agentops governance panel** uses for
  per-session I/O recording: each bridged Claude Code frame is folded into the
  same hash-chained, ledger-anchored pattern.

## Related

- [Security](/reference/modules/ix-security/) — the surrounding security and
  data-protection plane (guardrails, DLP, retention, residency).
- [Sessions](/reference/modules/ii-sessions/) — hosts the governed Claude Code
  session runtime whose per-session I/O this subsystem records.
- [Honesty and limits](/start/honesty-and-limits/) — the live / on-demand /
  deny-closed posture across the engine.
