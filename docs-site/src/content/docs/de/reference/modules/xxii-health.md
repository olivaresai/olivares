---
title: "Modul XXII — Health, SLA & Uptime"
description: >-
  Zuverlässigkeit der Agenten und MCP-Server im AI-Estate: was gesund ist, was
  degradiert oder ausgefallen ist und was wovon abhängt. Wie Health aus Signalen
  abgeleitet wird, die das Produkt beweisen kann, was es materialisiert und die
  ehrlichen Grenzen.
---

Modul XXII beantwortet drei Fragen zu den AI-Komponenten des Estate — **was gesund ist,
was degradiert oder ausgefallen ist und was wovon abhängt**. Es ist auf die
**Zuverlässigkeit von Agenten und MCP-Servern** begrenzt, nicht auf Host- oder
Infrastruktur-Health im Allgemeinen. Diese Seite ist die Referenz dafür, was das Modul
misst, was es materialisiert und wo seine ehrlichen Kanten liegen.

## Was es ist

XXII ist ein **Konsument des Kerns**, kein Prober: Sockets in die Kundeninfrastruktur zu
öffnen ist eine Connector-Angelegenheit, und das versiegelte Beobachtungsset hat keine
Health-Art. Daher wird Health aus Signalen **abgeleitet**, die das Modul beweisen kann:

- **Liveness (passiv).** Eine Session oder ein Agent, die einen MCP-Server berühren —
  oder ein handelnder Agent — sind ein Beleg dafür, dass das Subjekt lebt. Das
  aktualisiert den Last-Seen-Marker des Subjekts und faltet eine Abhängigkeitskante ein.
- **Aktive Probe-Ergebnisse.** Ein externer Health-Checker oder der Agent selbst postet
  ein Ergebnis an einen Report-Endpunkt pro Check — der ehrliche Ingest-Pfad für
  „Health-Checks / OTEL-Metriken“.
- **Veraltung.** Ein bekanntes Subjekt, das innerhalb seiner erwarteten Kadenz nicht
  mehr gesehen wird, ist selbst ein Signal. Ein Hintergrund-Sweep überführt es nach
  `degraded`, dann `down`, und eröffnet einen Incident. Der Sweep **degradiert oder
  markiert nur als down**; die Wiederherstellung kommt ausschließlich aus echter
  Liveness, sodass ein frisch angelegter Check niemals eine fälschliche Wiederherstellung
  ausgibt.

## Sein Vertrag & seine Entitäten

Das Modul besitzt vier Entitäten. Ein **Health-Check** ist ein vom Operator deklariertes,
überwachtes Subjekt (ein Agent oder ein MCP-Server) mit einer erwarteten Kadenz und einem
SLA-Ziel; er trägt den aktuellen Snapshot-Zustand des Subjekts — `healthy`, `degraded`,
`down` oder `unknown`. Ein **Health-Event** ist ein nur-anhängendes Übergangs-Ledger,
aus dem Uptime und SLA *rekonstruiert* werden — niemals als laufender Zähler gespeichert.
Ein **Health-Incident** ist der Lebenszyklus open→resolved einer degradierten oder
ausgefallenen Periode, wobei pro Subjekt genau ein offener Incident erzwungen wird. Eine
**Health-Dependency** ist eine automatisch entdeckte `origin → target`-Kante — die
Abhängigkeitskarte, idempotent akkumuliert.

Health wird **nur für deklarierte Checks materialisiert**. Ein als lebendig beobachtetes
Subjekt **ohne deklarierten Check** wird auf der Abhängigkeitskarte ehrlich als
`observed` ausgewiesen — *lebendig gesehen, Health nicht gemessen* — ein eigener Zustand,
verschieden von `healthy` (ein deklarierter Check hat signalisiert) und von `unknown`
(benannt, kein Liveness-Beleg). Das Produkt fabriziert niemals einen Zustand „measured-
healthy“, den es nicht berechnet hat. XXII spiegelt zudem den aktuellen Zustand eines
Subjekts in die Kern-Entität `HealthStatus`, wenn das Subjekt eine Kern-ID ist, damit
andere Ebenen die Health eines Agenten oder MCP lesen können.

## Was es konsumiert & produziert

XXII konsumiert [`edge.observed`](/de/reference/events/) vom Bus für passive Liveness und die
Abhängigkeitskarte sowie die aktiven Probe-Reports, die über seine API eintreffen. Es
**produziert, es liefert nicht aus**: Signale für down, degraded, recovered und
SLA-Verstoß werden als Minimaldaten-`FindingReport`s auf dem Kanal
[`finding.reported`](/de/reference/events/) ausgegeben — dem produktweiten Alert-Stream, den
[Modul XV (Benachrichtigungen)](/de/reference/modules/xv-notify/) an Slack, PagerDuty oder
ein SIEM leitet. XXII liefert niemals aus und abonniert niemals seine eigenen Findings.

:::caution[Ehrliche Grenzen]
- **Es misst nur, was deklariert ist.** Health wird nur für deklarierte Checks
  materialisiert. Ein lebendiges, aber nicht deklariertes Subjekt liest `observed`
  (lebendig gesehen, nicht gemessen) — niemals `healthy`. Zuverlässigkeit ist nur so
  vollständig wie die Checks, die ein Operator deklariert.
- **Es ist kein Prober.** XXII öffnet niemals Sockets in Ihre Infrastruktur. Es leitet
  Zuverlässigkeit aus Liveness, geposteten Probe-Ergebnissen und Stille ab — daher wird
  für ein Subjekt, das keine Telemetrie aussendet und keinen externen Checker hat, die
  Abwesenheit eines Signals als Signal (Veraltung) behandelt, nicht als Beweis für
  Health.
- **Uptime und SLA werden aus einem nur-anhängenden Ledger rekonstruiert**, nicht als
  Live-Anzeige gehalten; die Zahlen spiegeln die für das angeforderte Fenster
  aufgezeichneten Übergänge wider.
- **Keine Aktuierung.** Dieses Modul governt und beobachtet von Natur aus — es hat keine
  Aktuierungsoberfläche (siehe [Modulübersicht](/de/reference/modules/overview/)). Es
  erkennt und meldet; Behebung ist eine menschliche oder nachgelagerte Angelegenheit.
- **Minimaldaten auf der Leitung.** Gespeicherter Zustand sind Status,
  Zuverlässigkeitsmetriken und Abhängigkeitsbeziehungen — niemals Payloads, Prompts,
  Secrets oder PII. Das eine sensible Detail, das eine Probe tragen kann (eine
  Fehlermeldung), wird auf einen Einweg-Hash reduziert; angezeigt wird nur eine kurze,
  nicht-sensible Zusammenfassung.
:::

## Verwandt

- [Event-Bus-Referenz](/de/reference/events/) — `edge.observed` (Liveness) und `finding.reported` (die Signale, die XXII ausgibt).
- [Modul XV — Output-Integrationen & Benachrichtigungen](/de/reference/modules/xv-notify/) — leitet die Health-Findings von XXII an Ziele.
- [Modulübersicht](/de/reference/modules/overview/) — wo XXII sitzt und die Aktuierungsaufteilung.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine, der Bus und die Kernschicht.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — was das Produkt heute beobachtet im Vergleich zu dem, was es aktuiert.
