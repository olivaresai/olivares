---
title: "Reporting — professionelle HTML-/PDF-Berichte"
description: >-
  Erzeugt herunterladbare HTML- und PDF-Berichte aus den Compliance-, Audit-
  und FinOps-Daten der Plattform. Fünf integrierte Berichtstypen stehen on-demand
  bereit; geplante Berichte gehören zu Business.
---

Reporting (`modules/reporting`) erzeugt Berichte, wenn das Modul aktiviert ist.
Das Modul formt die Compliance-, Audit- und FinOps-Daten der Plattform zu einem
einzigen Dokument, damit ein Auditor Evidenz herunterladen kann, statt JSON aus mehreren APIs zu
kopieren.

**Edition:** Business Compliance Packs bietet Framework-Kataloge, Bewertungen, den regulatorischen Kalender, DORA/HIPAA-Ansichten, das Versiegeln von Nachweisen, OSCAL-Exporte und HTML/PDF-Berichte auf Abruf. Community antwortet für diese Funktionen mit `501` und behält Risiko, Datenresidenz, Records Management und JSON/CSV-Exporte gespeicherter Nachweise. Updates erhalten bestehende Datensätze.

## Integrierte Berichte

Business Compliance Packs stellt fünf Berichtstypen on-demand bereit:

- `compliance-evidence` — Compliance-Stand pro Framework mit Kontrollstatus und Evidenz.
- `audit-summary` — Summen der Audit-Events und Prüfung der Ledger-Integrität.
- `finops-report` — KI-Ausgaben nach Modell und Provider.
- `access-review` — Benutzer- und Zugriffsdaten für regelmäßige Prüfungen.
- `executive-summary` — kompakte Sicht auf Governance, Risiko, Kosten und Adoption.

`GET /v1/m/reporting/reports` listet Typen und Formate auf. Ein Bericht wird mit
`GET /v1/m/reporting/reports/{type}` erzeugt; HTML ist der Standard,
`?format=pdf` lädt ein PDF herunter. Die Routen erfordern
`reporting:report:read`.

## Aktivieren und einen Bericht herunterladen

Nach der CLI-Anmeldung an Ihrer Engine kann ein Administrator Reporting aktivieren:

```sh
olivares modules on reporting
```

Die Engine startet neu, wenn die Aktivierung die laufenden Module ändert. Warten
Sie, bis die Konsole wieder verbunden ist oder die Readiness-Prüfung des installierten
Dienstes erfolgreich ist, bevor Sie einen Bericht anfordern. Der Compose-Healthcheck
verwendet `olivares readyz`.

```sh
olivares reporting reports ls -o json
olivares reporting reports get finops-report --format html --out spend.html
```

Reporting aktiviert auch das erforderliche Compliance-Modul und dessen
Abhängigkeiten. Der Katalog listet die auf der laufenden Engine verfügbaren Formate;
prüfen Sie ihn vor einer PDF-Anfrage. Die Erzeugung liest gespeicherte Daten des
ausgewählten Mandanten. Eine leere Installation hat keine Ausgaben zu berichten.

Für einen gezielten Zeitraum übergeben Sie sowohl `--from` als auch `--to` mit
ISO-Daten oder RFC-3339-Zeitstempeln. In Skripten schreibt `--out -` das Dokument
nach stdout und den Download-Beleg nach stderr. Das Dokument ist HTML oder PDF,
kein JSON.

`olivares modules off reporting` entfernt es aus der Modulauswahl. Benötigt kein
aktiviertes Modul oder Editions-Add-on Reporting, werden seine Routen deaktiviert,
ohne gespeicherte Daten zu löschen. Währenddessen liefern Reporting-Anfragen `404`
mit `module_not_enabled`; Aktivieren stellt die Routen wieder her. Modulauswahl und
Berichtseingabedaten überleben einen Engine-Neustart. On-demand erzeugte Dokumente
sind Downloads; bewahren Sie die Datei auf, wenn Sie diesen Bericht behalten wollen.

## Grenzen, klar benannt

- Für PDF startet Chromium im Headless-Modus. Ohne `chromium`,
  `chromium-browser` oder `google-chrome`/`chrome` im `PATH` antworten PDF-Anfragen mit
  `501`; HTML bleibt verfügbar.
- Ein Compliance-Evidenzbericht benötigt die Compliance-Datenquelle. Ist sie
  nicht verdrahtet, enthält das Dokument den ausdrücklichen Hinweis „Data source
  not configured“, statt Evidenz zu erfinden.
- Das Modul rendert Dokumente aus bereits in der Plattform vorhandenen Daten. Es
  ersetzt weder Audit-Ledger noch Compliance-Bewertung oder FinOps-Quelle.

- HTML-Erzeugung und Katalogzugriff benötigen einen authentifizierten Aufrufer mit
  `reporting:report:read` im ausgewählten Mandanten. Verweigerte Authentifizierung
  liefert `401`; fehlende Berechtigung für diesen Mandanten liefert `403`.
- Katalogverfügbarkeit bescheinigt nicht die Datenabdeckung jedes Berichts. Im
  aktuellen Access-Review-Bericht sind Identitätsnamen vorhanden; E-Mail, Rollen,
  Berechtigungen und letzter Zugriff werden vom Datenadapter nicht befüllt.
- FinOps-Berichtsgruppen identifizieren Modelle und Provider anhand gespeicherter
  IDs statt Anzeigenamen.

## Verwandt

- [Compliance & Regulatorik](/de/reference/modules/xiii-compliance/) — Quelle
  für Compliance-Stand und Evidenz.
- [Kosten & AI FinOps](/de/reference/modules/xi-finops/) — maßgebliche
  Ausgabenoberfläche.
- [Modulkatalog](/de/reference/modules/overview/) — Modulverfügbarkeit und Reife.
