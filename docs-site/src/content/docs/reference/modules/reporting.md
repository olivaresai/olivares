---
title: "Reporting — professional HTML/PDF reports"
description: >-
  Generates downloadable HTML and PDF reports from the platform's compliance,
  audit and FinOps data. Five built-in report types are available on demand;
  scheduled reports are part of Business.
---

Reporting (`modules/reporting`) generates reports when the module is enabled.
It turns the platform's compliance, audit and FinOps data into a single document, so an auditor can
download evidence instead of copy-pasting JSON from several APIs.

**Edition:** Business Compliance Packs provides framework catalogs, assessments, the regulatory calendar, DORA/HIPAA views, evidence sealing, OSCAL exports and on-demand HTML/PDF reports. Community returns `501` for those capabilities and retains risk, residency, records management, and JSON/CSV exports of stored evidence. Upgrades preserve existing records.

## Built-in reports

Business Compliance Packs provides five on-demand report types:

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

## Enable and download a report

After signing the CLI in to your engine, an administrator can enable reporting:

```sh
olivares modules on reporting
```

The engine restarts when activation changes the running modules. Wait for the
console to reconnect or for the installed service's readiness check to pass
before requesting a report. The Compose healthcheck uses `olivares readyz`.

```sh
olivares reporting reports ls -o json
olivares reporting reports get finops-report --format html --out spend.html
```

Enabling reporting also activates its required compliance module and that
module's dependencies. The catalog lists the formats available on the running
engine; use it before requesting PDF. Generation reads the selected tenant's
stored data. An empty installation has no spend to report.

For a deliberate period, pass both `--from` and `--to` with ISO dates or
RFC 3339 timestamps. For a script, `--out -` writes the document to stdout and
the download receipt to stderr. The document is HTML or PDF, rather than JSON.

`olivares modules off reporting` removes it from the selected modules. If no
enabled module or edition add-on requires reporting, its routes are disabled
without deleting stored data. While disabled, reporting requests return `404` with
`module_not_enabled`; enabling the module restores the routes. Module selection
and report input data survive an engine restart. Generated on-demand documents
are downloads; keep the file if you need to retain that particular report.

## Boundaries, stated plainly

- PDF generation launches Chromium in headless mode. Without `chromium`,
  `chromium-browser`, `google-chrome` or `chrome` on `PATH`, PDF requests
  return `501`; HTML remains available.
- A compliance-evidence report needs the compliance data source. If that source
  is not wired, the document is generated with an explicit "Data source not
  configured" disclaimer rather than invented evidence.
- This module renders documents from data already held by the platform. It does
  not replace the audit ledger, compliance assessment or FinOps source of truth.
- HTML generation and catalog access require an authenticated caller with
  `reporting:report:read` in the selected tenant. Authentication refusal is
  `401`; a caller without authority for that tenant receives `403`.
- Catalog availability does not certify every report's data coverage. In the
  current access-review report, identity names are populated; email, roles,
  permissions and last-access fields are not populated by its data adapter.
- FinOps report buckets identify models and providers by their stored IDs,
  rather than their display names.

## Related

- [Compliance & regulatory](/reference/modules/xiii-compliance/) — the
  compliance posture and evidence source.
- [Cost & AI FinOps](/reference/modules/xi-finops/) — the authoritative spend
  surface.
- [Modules catalog](/reference/modules/overview/) — module availability and
  maturity.
