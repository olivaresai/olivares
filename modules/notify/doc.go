// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package notify is module XV — output integrations & notifications (README.md
// §2 XV). It is the notification ROUTER of the whole control plane: it decides
// WHAT signal goes to WHOM, by WHICH channel and WHEN; the output connectors
// (Slack/Teams, PagerDuty/Opsgenie, signed webhook, SIEM Splunk/Elastic with
// CEF/LEEF/syslog/OTLP) do the HOW. It consumes that transport — it never
// reimplements it (ARCHITECTURE.md/§5; the "decide vs deliver" split).
//
// Inbound. Every module in the product turns an alert into a minimal-data
// FindingReport on the bus (finding.reported) with a namespaced Kind:
// health_subject_down, finops_budget, security_guardrail, eval_regression,
// compliance_residency_violation, orchestration_cadence_miss, voice_*, … (S02).
// finding.reported is therefore the single product-wide alert channel, and this
// module subscribes to it and routes by Kind/Severity/Source/Subject. It also
// subscribes to approval.requested: an opened governance approval, routed
// the same way and rendered as an INTERACTIVE approve/deny card so the approver
// decides from chat — the origination half of the HITL round-trip whose inbound
// half is the receiver in cmd/olivares/hitl.go (the approval's action drives the
// route glob/dedup; its risk tier maps onto the severity scale). It also
// subscribes to approval.resolved and routes the terminal outcome as a
// non-interactive notice under the same approval read model. It does NOT
// subscribe to cost.sampled or edge.observed — those are raw telemetry, not
// alerts (a spend ALERT arrives as a finops_budget finding, not a cost sample).
//
// Routing. A tenant defines NotificationRoutes (schema.go): a predicate over
// {event types, finding-kind globs, minimum severity, source modules, subject
// kinds} → a named destination, with per-route dedup and throttle windows. On each
// finding the module evaluates the tenant's enabled routes in priority order,
// builds a redacted sdk.Notification, suppresses duplicates/storms, and delivers
// through the Dispatcher seam (ports.go) — whose composition-root adapter is
// backed by the real connectors. Every delivery attempt is recorded in the
// append-only notify_delivery ledger (the "what was sent, to whom, why, outcome"
// evidence trail). Network delivery NEVER runs inside a store transaction.
//
// Privileged & audited. Creating/changing/deleting a route or sending a test
// notification is a privileged, self-audited action (docs/SECURITY-HARDENING.md: "configurar
// destinos y rutas de notificación es acción privilegiada y auditada").
//
// Minimal data (docs/SECURITY-HARDENING.md). A notification carries only the finding's
// already-safe display fields — Title, Kind, Severity, Subject ref, a correlation
// hash — never a payload, prompt, secret or PII. The route/delivery rows hold no
// destination credential: the secret (a Slack webhook URL, a PagerDuty routing
// key) lives only in the connector configuration the composition root provisions,
// referenced here by a non-secret destination NAME.
package notify
