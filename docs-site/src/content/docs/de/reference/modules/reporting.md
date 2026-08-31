---
title: "Reporting — professionelle HTML-/PDF-Berichte"
description: >-
  Erzeugt herunterladbare HTML- und PDF-Berichte aus den Compliance-, Audit-
  und FinOps-Daten der Plattform. Fünf integrierte Berichtstypen stehen on-demand
  bereit; geplante Berichte sind ein Enterprise-Add-on.
---

Reporting (`modules/reporting`) ist **LIVE**. Das Modul formt die Compliance-,
Audit- und FinOps-Daten der Plattform zu einem einzigen professionellen Dokument,
damit ein Auditor Evidenz herunterladen kann, statt JSON aus mehreren APIs zu
kopieren.

## Integrierte Berichte

Das Open-Core-Modul stellt fünf Berichtstypen on-demand bereit:

- `compliance-evidence` — Compliance-Stand pro Framework mit Kontrollstatus und Evidenz.
- `audit-summary` — Summen der Audit-Events und Prüfung der Ledger-Integrität.
- `finops-report` — KI-Ausgaben nach Modell und Provider.
- `access-review` — Benutzer- und Zugriffsdaten für regelmäßige Prüfungen.
- `executive-summary` — kompakte Sicht auf Governance, Risiko, Kosten und Adoption.

`GET /v1/m/reporting/reports` listet Typen und Formate auf. Ein Bericht wird mit
`GET /v1/m/reporting/reports/{type}` erzeugt; HTML ist der Standard,
`?format=pdf` lädt ein PDF herunter. Die Routen erfordern
`reporting:report:read`.

## Open Core und Enterprise

HTML on-demand ist im Open-Core-Binary enthalten. PDF on-demand ist verfügbar,
wenn ein Chromium-kompatibles Programm vorhanden ist. **Enterprise-Add-on:**
Die geplante Berichtserzeugung ist per Build-Tag gegatet und gehört nicht zur
Community-Runtime.

## Grenzen, klar benannt

- Für PDF startet Chromium im Headless-Modus. Ohne `chromium`,
  `chromium-browser` oder `google-chrome`/`chrome` im `PATH` antworten PDF-Anfragen mit
  `501`; HTML bleibt verfügbar.
- Ein Compliance-Evidenzbericht benötigt die Compliance-Datenquelle. Ist sie
  nicht verdrahtet, enthält das Dokument den ausdrücklichen Hinweis „Data source
  not configured“, statt Evidenz zu erfinden.
- Das Modul rendert Dokumente aus bereits in der Plattform vorhandenen Daten. Es
  ersetzt weder Audit-Ledger noch Compliance-Bewertung oder FinOps-Quelle.

## Verwandt

- [Compliance & Regulatorik](/de/reference/modules/xiii-compliance/) — Quelle
  für Compliance-Stand und Evidenz.
- [Kosten & AI FinOps](/de/reference/modules/xi-finops/) — maßgebliche
  Ausgabenoberfläche.
- [Modulkatalog](/de/reference/modules/overview/) — alle 30 verdrahteten Module
  und ihre ehrliche Reife.
