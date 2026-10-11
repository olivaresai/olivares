// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package paperclip is the Olivares AI observe-only connector for Paperclip
// (github.com/paperclipai/paperclip, MIT), a task-manager orchestrator that starts
// AI tools itself and keeps its own org chart, budgets and run history.
//
// Olivares governs Paperclip as a userland tool one level above the AI tools, the
// way it governs OpenClaw and Hermes: it observes, it does not re-implement.
//
// # Integration level: OBSERVE + COST METERING (advisory)
//
// The connector reads the Paperclip REST API with a board API key. Every request
// goes through the shared read-only httpx client, so a write to Paperclip is not
// expressible. It never decodes agent adapterConfig or runtimeConfig, which can
// hold environment values.
//
//   - ROSTER (identitysource.GraphProvider): a company becomes a group collection
//     (paperclip/company/<id>); an agent becomes an NHI row (paperclip/agent/<id>,
//     Kind paperclip/<adapterType>) with role, title, status, company and reports_to
//     attributes. A paused or terminated agent is Disabled. A reporting line is an
//     org-chart fact: it is carried as reports_to and never as lifecycle ownership.
//   - RUNS (Gather): per agent, per completed UTC day, per terminal status, one metric
//     sample paperclip.heartbeat_runs. A run still queued, retrying or running is not
//     counted: it would be bucketed under a status it later leaves. It is counted once
//     it finishes, while its day is still inside lookback_days.
//   - COST (Gather): Paperclip-reported spend per agent, provider and model for each
//     completed UTC day, as an estimated cost sample with CostType "paperclip".
//     FinOps can filter on that type; spend that Paperclip agents also route through
//     an Olivares inference proxy is counted by the proxy, so allocate the two apart.
//   - COVERAGE: a leg Paperclip refuses (403/404) or a run list that reached its cap
//     is reported as a coverage finding, never read as "no activity".
//
// # Limitations (honest)
//
//   - ADVISORY: Olivares sees what Paperclip reports. It does not start, confine or
//     stop the agents; there is no inline PEP.
//   - NO RAW COST EVENTS: Paperclip exposes only aggregated cost reads, so the
//     connector asks once per completed day per company. Cache token counts are not
//     carried: Paperclip's cached-token meaning differs per adapter.
//   - RUN WINDOW: the runs listing has no date filter; the newest 1000 are read. A
//     company that reaches the cap is flagged as possibly truncated, and the oldest
//     day the listing reaches (possibly cut mid-day) is not reported.
//   - SHAPE: an id, a cost number or a run field outside the Paperclip shape fails the
//     pass with an error. It is never skipped, because a skipped row reads as "no
//     activity" or "zero cost".
//   - TRUST: with no api_key the connector sends no credential, which only a
//     local_trusted Paperclip accepts. Give a production instance a read-scoped board
//     key held in the vault.
//
// The connector imports only the SDK, connectors/identitysource and
// connectors/internal, never the engine (/core). Paperclip code is not copied: the
// API shapes are read from its public routes.
package paperclip
