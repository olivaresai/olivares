---
title: "Gespeicherte Konsolenansichten"
description: >-
  Benannte, teilbare Snapshots des Zustands einer Konsolenansicht — Filter,
  Zeiträume, Scopes — serverseitig pro Mandant gespeichert. Speichern Sie eine
  Untersuchung und teilen Sie sie mit dem Team. Was das Modul speichert, seine
  Eigentums- und Freigaberegeln sowie seine ehrlichen Grenzen.
---

Das Modul `consoleviews` stellt der Konsole **gespeicherte Ansichten** bereit:
einen benannten Snapshot des Zustands einer Ansicht — dieselben Filter,
Zeiträume und Scopes, die die Konsole in der URL codiert — **serverseitig pro
Mandant** gespeichert. So überdauert eine Untersuchung wie *„fehlgeschlagene
Zulassungen, letzte 24 Stunden“* den Browser, begleitet den Operator auf andere
Rechner und ist (wenn sie geteilt wird) für das gesamte Team nur einen Klick
entfernt.

## Was gespeichert wird — und was niemals gespeichert wird

Eine gespeicherte Ansicht besteht **nur aus Parametern**: einem
größenbegrenzten JSON-Objekt (max. 4 KB) mit dem URL-Zustand der Ansicht sowie
einem Namen, einer optionalen Beschreibung, dem besitzenden Principal und einem
`shared`-Flag. Das Modul speichert **niemals Abfrageergebnisse, Ledger-Zeilen
oder irgendwelche Daten, die durch die Parameter ausgewählt würden** — beim
Laden einer gespeicherten Ansicht wird die zugrunde liegende Abfrage mit den
eigenen Berechtigungen des Aufrufers erneut ausgeführt. Die Konsole behandelt
gespeicherte Parameter strikt als Daten.

## Eigentum, Freigabe und Berechtigungen

- **Erstellen/Aktualisieren** — jedes Mitglied mit
  `consoleviews:view:write` (Editor-Tier). Eine Ansicht gehört dem Principal,
  der sie erstellt hat; nur der Eigentümer darf sie bearbeiten.
- **Sichtbarkeit** — der Eigentümer sieht die eigenen Ansichten immer; eine als
  `shared` markierte Ansicht ist für jedes Mandantenmitglied mit
  `consoleviews:view:read` (Viewer-Tier) sichtbar. Eine Ansicht, die Sie nicht
  sehen dürfen, antwortet mit `404`, niemals mit `403` — ihre Existenz wird
  durch die Sichtbarkeitsprüfung nicht offengelegt.
- **Löschen** — der Eigentümer oder eine **Admin-/Owner-Rolle** des Mandanten
  darf jede Ansicht löschen (um Ansichten ausgeschiedener Benutzer
  aufzuräumen).
- **Obergrenzen** — 200 Ansichten pro Eigentümer und 2000 pro Mandant; beim
  Erreichen wird die Aktion mit einer klaren Meldung abgelehnt.
  `(feature, owner, name)` ist ein natürlicher Schlüssel: Ein doppelter Name
  für dasselbe Feature antwortet mit `409`.

Jedes Erstellen, Aktualisieren und Löschen wird im Audit-Ledger des Mandanten
aufgezeichnet und dem tatsächlichen Principal zugeschrieben — die
aufgezeichneten Metadaten identifizieren die Ansicht (Feature, Name,
`shared`-Flag), niemals ihre Parameter.

## Routen

| Methode | Route | Berechtigung |
|---|---|---|
| `GET` | `/v1/m/consoleviews/views?feature_id=` | `consoleviews:view:read` |
| `GET` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:read` |
| `POST` | `/v1/m/consoleviews/views` | `consoleviews:view:write` |
| `PUT` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |
| `DELETE` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |

Modulrouten gehören zur **Beta**-Oberfläche — siehe die
[Referenz für Modulrouten](/reference/api-beta/).

## Ehrliche Grenzen

- Der Server validiert den `feature_id` einer Ansicht als Slug, pinnt aber
  **nicht** die Feature-Liste der Konsole — das Konsolenregister ist
  maßgeblich und ändert sich je Release; die Konsole ignoriert gespeicherte
  Ansichten für Features, die nicht mehr vorhanden sind.
- Eine freigegebene Ansicht teilt **Parameter**, keine Ergebnisse: Zwei
  Operatoren können beim Laden derselben gespeicherten Ansicht unterschiedliche
  Daten sehen, wenn ihre Berechtigungen verschieden sind. Das ist
  beabsichtigt — die Freigabe erweitert niemals den Zugriff.
- Gespeicherte Ansichten sind Konsoleneinrichtung, keine Evidence: Sie befinden
  sich außerhalb der Ledger-Chain (nur ihre Lifecycle-Ereignisse werden
  evidenziert).
- Ein **auf einen Workspace beschränkter** Operator kann gespeicherte Ansichten
  lesen, aber nicht erstellen, bearbeiten oder löschen: Die Scoped-Grant-Engine
  verbietet Writes auf Collection-Ebene für beschränkte Principals
  (fail-closed), und der mandantenweite Admin-Override zum Löschen schließt
  beschränkte Admins ausdrücklich aus.
- Die Obergrenzen pro Eigentümer/Mandant sind bei gleichzeitigen Schreibern
  unter Postgres weich (begrenztes geringfügiges Überschreiten); doppelte Namen
  werden immer strikt abgelehnt.
