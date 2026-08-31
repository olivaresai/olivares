---
title: "Reporting — professional HTML/PDF reports"
description: >-
  Generates downloadable HTML and PDF reports from the platform's compliance,
  audit and FinOps data. Five built-in report types are available on demand;
  scheduled reports are an enterprise add-on.
---

Reporting (`modules/reporting`) is **LIVE**. It turns the platform's compliance,
audit and FinOps data into a single professional document, so an auditor can
download evidence instead of copy-pasting JSON from several APIs.

## Built-in reports

The open-core module provides five on-demand report types:

- `compliance-evidence` — compliance posture by framework, with control status
  and evidence.
- `audit-summary` — audit-event totals and ledger-integrity verification.
- `finops-report` — AI spend by model and provider.
- `access-review` — users and access data for periodic review.
- `executive-summary` — a compact view of governance posture, risk, cost and
  adoption.

`GET /v1/m/reporting/reports` lists the types and formats. Generate one with
`GET /v1/m/reporting/reports/{type}`; HTML is the default, while
`?format=pdf` downloads a PDF. The routes require `reporting:report:read`.

## Open-core and enterprise

On-demand HTML is included in the open-core binary. On-demand PDF is included
when a Chromium-compatible executable is available. **Enterprise add-on:**
scheduled report generation is build-tag gated and is not part of the community
runtime.

## Boundaries, stated plainly

- PDF generation launches Chromium in headless mode. Without `chromium`,
  `chromium-browser`, `google-chrome` or `chrome` on `PATH`, PDF requests
  return `501`; HTML remains available.
- A compliance-evidence report needs the compliance data source. If that source
  is not wired, the document is generated with an explicit "Data source not
  configured" disclaimer rather than invented evidence.
- This module renders documents from data already held by the platform. It does
  not replace the audit ledger, compliance assessment or FinOps source of truth.

## Related

- [Compliance & regulatory](/reference/modules/xiii-compliance/) — the
  compliance posture and evidence source.
- [Cost & AI FinOps](/reference/modules/xi-finops/) — the authoritative spend
  surface.
- [Modules catalog](/reference/modules/overview/) — all 30 wired modules and
  their honest maturity.
