// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package finops is module XI of the control plane (README.md): cost and FinOps
// for AI — controlling AI spend, a strong enterprise driver.
//
// It is an AGPL module that consumes the model/provider cost stream
// (the cost.sampled event carrying an sdk/model.CostSample, whose monetary amount
// the connector already derived or read authoritatively from the provider). It
// does NOT re-implement provider integration; it accounts for what they report.
//
// What it owns:
//
//   - Ingestion (ingest.go): each cost.sampled is recorded into the core
//     CostRecord ledger (the entity), de-duplicated by a NATURAL key (the
//     bucket's identity — provider/model/session/instant plus every attribution
//     dimension and provenance — NOT a content hash that includes the value) so a
//     re-pulled open bucket or a late-settled report upserts in place and the
//     at-least-once stream never double-counts spend. It also writes a
//     denormalized FinOps read-model row (finops.cost_sample) keyed by the
//     natural attribution names (provider/model/agent/session/team/project) so
//     spend can be aggregated by ANY dimension efficiently — the CostRecord is the
//     canonical normalized ledger (by id); the read-model is the analytics
//     substrate (by name). Money is always integer micro-USD, never a float.
//
//   - Budgets (budgets.go): a budget is a core Policy (Kind="budget") with a
//     dimension (global/model/provider/agent/session/team/project), a limit, a
//     period and alert thresholds. On each ingest the module evaluates the budgets
//     the sample touches; when consumption crosses a threshold it has not crossed
//     this period, it RECORDS the alert (finops.budget_alert) and EMITS a
//     FindingReport on the bus. It emits the signal only — delivery to
//     Slack/SIEM/PagerDuty is the output connectors' / job.
//     Preventive per-group ceilings are approximate by design: user_group budget
//     keys are UserGroup.ID values (as carried by auth.Principal.GroupsIn) and
//     agent_group budget keys are AgentGroup.Slug values. The cost read-model has
//     no group column, so CheckBudget sums group spend by member fan-out over the
//     existing per-subject attribution columns (actor for users, agent_ref for
//     agents). The scan is capped and reports Truncated; in-flight concurrent spend
//     can still over-admit. Coverage scales with attribution quality: unattributed
//     spend is under-counted, which is the safe direction for this approximation
//     because it never creates a false deny. Group checks fail open by default with
//     a per-budget fail_closed opt-in. The detective ingest/finding path for
//     group budgets and any local-counter degradation mode remain follow-up work.
//     PROD ACTIVATION: both group dimensions are prod-active. agent_group reads agent
//     groups from the business store.Scope. user_group membership lives in the AUTH
//     partition (system tenant), which the least-privilege ModuleData withholds by
//     construction; so the composition root hands FinOps (and ONLY FinOps) a data
//     handle that also exposes an auth reader (boot: finopsData) — a deliberate,
//     read-only privilege grant for group-budget enforcement. SECURITY NOTE: that
//     handle currently exposes the full AuthView; narrowing it to a
//     group-membership-only reader (so FinOps cannot reach the wider auth partition)
//     is a recommended follow-up. When the reader is absent (nil), user_group budgets
//     are inert (fail-open) — do not set fail_closed on one without the reader wired.
//
//   - Analytics (analytics.go): spend breakdown by any dimension, totals, daily
//     trend, a simple run-rate forecast of the current period, and optimization
//     recommendations (cheaper-model savings estimates, budget-burn warnings),
//     grounded in recorded data and honest about their assumptions.
//
// Minimal data (docs/SECURITY-HARDENING.md): the module stores token counts, costs and
// attribution references, never a prompt, a completion or a secret. Cost is
// governance data — reads are RBAC-gated at the API.
package finops
