// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package coworkanalytics is the read-only Cowork engagement connector for the
// Claude Enterprise Analytics API (A1). It polls the
// org-analytics endpoints and emits the Cowork ACTIVITY signal the dashboards/
// FinOps surfaces consume — organization-wide Cowork DAU/WAU/MAU, the aggregate
// per-user engagement (distinct sessions, messages, actions, dispatch turns, and
// skill/connector invocations — all eight cowork_metrics fields verified verbatim,
// AsOf 2026-06-10), and the GA per-skill / per-connector Cowork breakdowns
// (/analytics/skills and /analytics/connectors) — plus an honest coverage posture
// finding.
//
// Why engagement and not cost here: Cowork's per-request COST already flows through
// the OTEL connector (cowork.costFromAPIRequest, the verified api_request.cost_usd
// path), so this connector deliberately does NOT re-ingest cost — counting it twice
// would double the estimated stream (the same discipline claude-api applies to the
// Claude Code feed vs usage_report). The Enterprise Analytics API's per-user cost
// endpoint (user_cost_report) IS now published at the field level (verified AsOf
// 2026-06-10: user_actor rows, amounts in fractional cents, a products[] filter
// including cowork); ingest remains deliberately excluded as a double-count guard
// until FinOps reconciles estimated-vs-billed provenance (see coverage.go, gap
// cost-via-otel-not-here).
//
// Sealed-contract honesty: the SDK observation set is Edge/Cost/Finding — there is
// no numeric-metric observation. Pure activity COUNTS (DAU/WAU/MAU, invocation
// counts) therefore ride a per-Gather engagement FindingReport (Info), whose
// non-sensitive aggregate counts land on the tamper-evident ledger and the
// dashboards; finer per-user/per-skill detail is available from the OTEL
// connector's per-session edges. user.email_address is never read (PII; docs/SECURITY-HARDENING.md).
//
// Read-only, Enterprise-gated, offline-degrading: every call is a GET via the
// shared GET-only modelprovider client (read:analytics scope); with no API key the
// connector is a silent no-op (an honest absence, never a fabricated metric). It
// imports only the SDK and the Apache connector contracts, never the engine.
package coworkanalytics
