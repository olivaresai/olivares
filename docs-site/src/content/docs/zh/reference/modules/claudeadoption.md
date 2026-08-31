---
title: "Claude Code adoption — the read-model of how Claude Code is used"
description: >-
  A read-only model of Claude Code adoption and productivity: sessions, lines of
  code, commits, PRs, tool accept-reject and per-model tokens, rolled up by
  team/developer/day. Per-team by default, per-developer drill-down opt-in.
  Claude-API-only boundary; it never carries cost.
---

Claude Code adoption (`modules/claudeadoption`) is one of the 30 modules. It is a
pure **read-model of how much Claude Code is being used, and how much of what it
proposes developers keep** — the adoption/ROI question the Claude-centric estate
asks, served alongside the [FinOps](/zh/reference/modules/xi-finops/) cost surface
rather than inside it. It **observes only**: there is no actuation surface, and it
**never carries cost** (cost is the authoritative FinOps surface, so a measure
here can never double-count it).

## What it ingests

It consumes the `metric.sampled` bus signal both Claude connectors emit — the
OTLP receiver's per-session productivity datapoints and the admin Analytics
feed's per-developer/day totals — and folds them into a per-`(subject, metric,
day, dimension)` read-model. The recognized metrics are the Claude Code names:
sessions, lines of code (added/removed), commits, pull requests, token usage
(by model), tool accept-reject decisions, and active time. A sample whose name is
outside that set is ignored — the module never persists a measure it cannot
interpret. Re-ingestion is idempotent: a re-pulled day or a re-delivered delta
folds onto the same natural-key row instead of double-counting.

## The two lenses (never summed)

The same Claude Code activity is reported from two planes, kept distinct and
**never added together**:

- **`analytics`** — the admin Analytics feed, the authoritative per-developer/day
  view (carries the developer email as the ROI subject).
- **`telemetry`** — the OTLP plane, per-session and real-time, carrying active
  time and operator-supplied team labels.

They are two vantage points on the same activity, so the surfaces present them
side by side rather than as one total.

## The four surfaces

| Route | Answers | Permission |
|---|---|---|
| `GET /summary` | the headline productivity roll-up for both lenses over a window, plus distinct developer/team counts | `adoption:metrics:read` |
| `GET /trend` | a per-day series for one lens (default `analytics`) | `adoption:metrics:read` |
| `GET /teams` | the per-team breakdown (from the telemetry lens, the only one that carries team labels) | `adoption:metrics:read` |
| `GET /developers` | the per-developer ROI drill-down, which exposes the developer email | `adoption:developer:read` |

Routes mount under `/v1/m/adoption/`. The team/org aggregates ride the ordinary
viewer-read tier — **per-team by default**, with no individual developer exposed.
The per-developer drill-down is a **privileged, deny-closed read** behind a
separate permission (per-developer **opt-in**), and an org can scope it further
via custom roles.

## Boundary, stated plainly

- **Claude-API-only.** The read-model covers only what flows over the Claude API
  plane — the admin Analytics feed and the OTLP exporter. A Claude Code estate
  served by Claude Platform on AWS, Microsoft Foundry, Amazon Bedrock or Vertex
  AI that does not export this telemetry is **invisible here**, so absence of
  adoption is never proof of absence.
- **It never carries cost.** Cost is the authoritative FinOps / `api_request`
  surface; this module measures activity, not spend.
- **Observe-only.** There is no actuation half — it has nothing to deploy,
  dispatch, send or enforce.

## Related

- [Cost & AI FinOps](/zh/reference/modules/xi-finops/) — the authoritative cost
  surface this module sits beside.
- [Events reference](/zh/reference/events/) — the `metric.sampled` signal it
  consumes.
- [Modules catalog](/zh/reference/modules/overview/) — the 30 modules and their
  honest maturity.
