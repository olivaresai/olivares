---
title: "Modul XXI — Executive-Dashboards & Reporting"
description: >-
  Die Führungssicht auf die Control Plane: Kosten, Nutzung, Risiko, Compliance und
  Zuverlässigkeit, zusammengefasst aus den Modulen, die die Berechnung besitzen,
  abgesichert durch dasselbe RBAC wie die technischen Sichten, mit bedarfsgesteuertem
  PDF-Export. Was es darstellt, was es niemals berechnet und seine ehrlichen Grenzen.
---

Modul XXI ist die Führungsoberfläche der **Web-Schicht** (Schicht 4): eine
übergeordnete Lesesicht auf das Estate — Ausgaben, Nutzung, Risikolage,
Compliance-Abdeckung und Zuverlässigkeit — neben der modulspezifischen technischen UI.
Es **aggregiert und stellt dar; es berechnet niemals neu** (die Module besitzen jede
Kennzahl), und es erbt dasselbe Mandanten-Scoping und RBAC wie die Sichten, die es
zusammenfasst.

## Was es ist

Zwei schreibgeschützte Oberflächen bilden dieses Modul:

- das **Executive-Dashboard** (`/dashboards`) — der vollständige modulübergreifende
  Rollup, mit auswählbarem Kostenzeitraum (7d / 30d / 90d / Monat bis dato), einer
  Ausgabenaufschlüsselung nach Team, Projekt, Agent, Modell oder Provider sowie einem
  druckbaren Berichtsdeckblatt;
- die **Startseitenübersicht** (`/`) — eine bewusst leichtere Eingangstür: ein einziges
  Raster aus Estate-Säulen (Inventar, aktive Sessions, Sicherheit, Compliance,
  Ausgaben-Run-Rate, Health/SLA), jede ein Drill-down-Link in ihr Modul.

Die Startseitenübersicht nutzt die Lese-Hooks, reinen Rollups und Tile-Primitive des
Dashboards wieder, statt sie zu duplizieren, und teilt sich denselben
mandantengebundenen Query-Cache, sodass die Eingangstür leicht bleibt (weniger Queries),
während sie mit der Detailsicht konsistent bleibt.

## Was es darstellt (und was es niemals berechnet)

Das Dashboard führt mit KPIs über fünf Säulen — **Kosten** (FinOps XI + Modelle X),
**Nutzung** (Inventar I + Sessions II), **Risiko** (Sicherheit IX + Red-Teaming XVIII +
Access Map III), **Compliance** (XIII) und **Zuverlässigkeit** (Health & SLA XXII). Die
Rollup-Schicht ist eine Menge **reiner Funktionen**, die nur zählen, summieren und
ranken, was die Module bereits entschieden haben: Kosten bleiben in den Ganzzahl-
Einheiten der Module, Finding-Schweregrad, Red-Team-Score, Kontrollstatus und
Health-Zustand werden unverändert durchgereicht.

Da es keine Berechnung besitzt, kann es die Ehrlichkeitsnaht einer Quelle nicht
weißwaschen, und es tut es nicht: Ein `truncated`-Aggregat bleibt als Untergrenze
markiert; ein Red-Team-Lauf, der seine Probes nicht abschließen konnte, wird **niemals**
als Bestehen gezählt; mit ungefährer oder undurchsichtiger Abdeckung beobachteter Zugriff
wird als Untergrenze ausgewiesen; Compliance liest sich als **Kontrollabdeckung**,
niemals als „compliant“-Behauptung, und behält ihren festen Haftungshinweis bei; ein
Health-Subjekt ohne Check liest `unknown`, nicht healthy.

## Export & die Leitung

Der Export ist **bedarfsgesteuert, client-seitig**: Das Dashboard druckt, was auf dem
Bildschirm ist, über das Save-as-PDF des Browsers (`window.print()`), mit einem
ausschließlich für den Druck bestimmten Berichtsdeckblatt (Organisation, Zeitraum,
Generierungszeitpunkt) und einer festen Fußzeile mit Haftungshinweis. Dies ist RBAC und
Mandanten-Scoping **per Konstruktion** treu — der Bericht kann immer nur die Abschnitte
enthalten, die die Rolle tatsächlich gerendert hat. Das exportierte Dokument trägt, wie
das Dashboard selbst, **nur aggregierte KPIs — keine Payloads, keine Secrets**:
Minimaldaten sind eine Eigenschaft dessen, was über die Leitung geht, kein Versprechen
über das Wohlverhalten eines Betrachters.

## Aktuierung

Modul XXI hat **keine Aktuierungsoberfläche** (`—` im
[Modulkatalog](/de/reference/modules/overview/)). Es ist eine Präsentationsschicht über
Lese-Endpunkten, die die Module bereits bedienen; es löst keinen Schreibvorgang aus,
feuert nichts und dispatcht nichts.

:::caution[Ehrliche Grenzen]
- **Keine geplanten oder ausgelieferten Berichte.** Die Designabsicht des Katalogs
  umfasst geplante, exportierbare Berichte; was heute ausgeliefert wird, ist
  **ausschließlich bedarfsgesteuerter, client-seitiger Print-to-PDF**. Es gibt keinen
  serverseitigen Reporting-Endpunkt, keinen wiederkehrenden Zeitplan und keine
  E-Mail-Zustellung — erwarten Sie nicht, dass ein Bericht von selbst ankommt.
- **Es ist nur so ehrlich wie seine Quellen.** Jede Abdeckungslücke, Trunkierung,
  ausstehende Zuordnung und jeder Haftungshinweis stammt aus den zugrunde liegenden
  Modulen und wird gezeigt, nicht geglättet; eine niedrige Zahl kann geringes Risiko
  *oder* begrenzte Abdeckung bedeuten. Lesen Sie jede Säule mit den Grenzen ihres Moduls
  (z. B. die Abdeckungsstufen der Access Map).
- **RBAC sichert jede Säule ab.** Eine Rolle, die eine Quelle nicht lesen kann, sieht
  deren KPI nie und kann ihn nicht drucken. Ein Leser ohne erlaubte Quelle sieht einen
  ehrlichen Leerzustand, kein erfundenes Dashboard.
- **Zeitpunktbezogen, einquellig.** Risiko, Compliance und Zuverlässigkeit sind
  Momentaufnahmen des aktuellen Zustands; nur Kosten umspannen den gewählten Zeitraum.
  Die Sicht ist ein Rollup der eigenen Daten dieser Control Plane, kein externes
  BI-Werkzeug.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — Schichten und die Aufteilung Govern/Actuate.
- [Modul XI — Kosten & AI-FinOps](/de/reference/modules/xi-finops/) — die Ausgabenzahlen, die es zusammenfasst.
- [Modul XIII — Compliance](/de/reference/modules/xiii-compliance/) — Kontrollabdeckung, niemals eine Compliance-Behauptung.
- [Modul III — Zugriffs- & Ressourcen-Map](/de/reference/modules/iii-access-map/) — der Drift hinter der Risiko-Säule.
- [Architekturüberblick](/de/explanation/architecture/overview/) — wo die Web-Schicht sitzt.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — wie das Produkt darlegt, was es tut und was nicht.
