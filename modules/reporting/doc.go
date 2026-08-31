// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package reporting implements module XXX: professional PDF/HTML report
// generation from the system's compliance, audit and FinOps data. An auditor
// downloads a single document instead of copy-pasting JSON.
//
// Open-core scope: on-demand HTML and PDF (when chromium is available) for five
// built-in report types (compliance-evidence, audit-summary, finops-report,
// access-review, executive-summary), served via GET /v1/m/reporting/reports.
//
// Enterprise add-on (enterprise/reporting, build-tag gated): scheduled report
// generation (cron), custom branding (logo + corporate colors), and operator-
// uploaded HTML templates.
package reporting
