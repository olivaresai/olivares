---
title: Die Arbeitsebene
description: >-
  Wie Agenten und Sessions die Arbeit in Olivares AI koordinieren — Arbeitselemente,
  Nachrichten, Bestätigungen und Übergaben —, was heute real und dauerhaft ist und was
  bewusst nicht verdrahtet bleibt. Die Hälfte des Produkts, die nicht die Zugriffskarte ist.
---

Der größte Teil dieser Dokumentation handelt davon, **was ein Agent erreichen kann**: die
Zugriffskarte, die Berechtigungen und die Abweichung zwischen *Permitted* und *Observed*.
Diese Seite handelt von der anderen Hälfte — **wie Agenten und Sessions die Arbeit selbst
koordinieren** —, dem Teil, den der Rest der Website bisher nur als Liste von Befehlen und
Ereignissen beschrieben hat.

Das Problem, für das sie existiert, ist nicht hypothetisch. Es ist genau das Problem, unter
dem dieses Projekt bei seiner eigenen Entwicklung gelitten hat: Sessions, die einander nicht
sehen können, voneinander abweichende Zustände, doppelt erledigte Arbeit und Entscheidungen,
die nur im Terminal einer Person leben und beim Schließen verloren gehen. Eine Control Plane,
die den *Zugriff* regelt und nichts über die *Arbeit* sagt, lässt diese Lücke genau dort, wo
sie war.

## Was ein Arbeitselement ist

Ein **Arbeitselement** ist eine Arbeitseinheit mit einem Owner, einem Zustand und einer
dauerhaften Aufzeichnung. Es ist weder eine Chatnachricht noch ein Ticket im Tracker eines
anderen: Es lebt im selben Store wie das Audit-Ledger. Was mit ihm geschehen ist, lässt sich
später daher mit denselben Mitteln beantworten wie alles andere, was die Control Plane
aufzeichnet.

Darum liegen drei Primitive:

| Primitiv | Funktion |
|---|---|
| **Nachricht** | Ein Teilnehmer teilt einem anderen dauerhaft etwas mit — kein Broadcast in ein Log, das niemand liest |
| **Bestätigung** | Der Empfänger zeichnet auf, dass er die Nachricht *übernommen* hat. „Gelesen“ und „beantwortet“ bezeichnen nicht mehr dasselbe |
| **Übergabe** | Der Owner eines Arbeitselements wechselt; der Grund für den Wechsel wird mitgeführt |

Bei der Bestätigung lohnt sich eine Pause. Koordination scheitert viel häufiger daran, dass
eine Nachricht gesehen, aber nicht bearbeitet wurde, als daran, dass sie nie zugestellt
wurde. Ein System, das diese Fälle nicht unterscheiden kann, kann Ihnen auch nicht sagen,
welcher davon eingetreten ist.

## Was heute real ist und was nicht

:::caution[Lesen Sie diesen Abschnitt, bevor Sie darauf aufbauen]
Die oben beschriebenen Primitive sind **implementiert und dauerhaft**. Ihre Reichweite ist
**bewusst enger** als die Idee, und die Grenze wird im Code durchgesetzt, statt in Prosa
versprochen zu werden. Drei Grenzen, unmissverständlich formuliert:
:::

**1 · Die Koordination ist auf einen Workflow begrenzt, und die öffentliche
Kommunikationsebene bleibt bewusst unverdrahtet.** Nachrichten, Bestätigungen und Übergaben sind innerhalb der
Ausführung des jeweiligen Workflows real. Die allgemeine, alles übergreifende
Kommunikationsebene ist *nicht* verbunden — und das ist kein Versehen, das noch entdeckt
werden müsste: Ein Boot-Test prüft, welche Autoritätsquellen `boot` verdrahten darf, und
**schlägt fehl, sobald eine andere erscheint**
(`cmd/olivares/communicationauthorityboot_test.go`,
`TestBootWiresExactCommunicationRequestAuthoritySourcesOnly`). Eine versehentliche
Verdrahtung erzeugt einen roten Test, keine Überraschung in Produktion.

**2 · Agent-zu-Agent-Dispatch wird nur mit einem autorisierten Ziel eingebunden.** Der
Remote-Work-Executor wird mit einer Genehmigungsschranke davor konstruiert
(`cmd/olivares/wire.go`); es gibt keinen Pfad, der Arbeit an einen beliebigen Peer
versendet, nur weil eine Konfigurationsdatei höflich darum gebeten hat.

**3 · Shadow-Modus und endgültige Arbeitsautorität EXISTIEREN NICHT.** Nicht „demnächst“,
nicht „teilweise“: Sie sind nicht vorhanden. Ein Deployment kann der Arbeitsebene heute
nicht das letzte Wort über eine Session geben, und nichts im Produkt darf so verstanden
werden, als biete es das an. Jede hypothetische Umsetzung müsste ihre Funktion belegen —
durch ein Vergleichsfenster gegenüber den bestehenden Quellen, nicht durch einen
Versionssprung.

## Warum die Grenzen hier stehen

Weil die Alternative für Sie schlechter wäre. Eine Seite, die das Design beschreibt und
Sie die Grenze erst bei der Integration entdecken lässt, kostet Sie den Nachmittag. Eine
Seite, die die fehlende Hälfte als „Roadmap“ bezeichnet, wäre genau die Art von Behauptung,
die dieses Projekt ablehnt. Die Seite [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/)
nennt die allgemeine Regel; hier wird sie auf die neueste Oberfläche des Produkts angewandt.

## Wohin als Nächstes

- [Modulübersicht](/de/reference/modules/overview/) — wo die Orchestrierung unter den
  anderen Modulen liegt.
- [Orchestrierungsreferenz](/de/reference/modules/iv-orchestration/) — das Modul, dem die
  Workflow-Ausführung gehört.
- [Event-Bus-Referenz](/de/reference/events/) — die Ereignisse, die die Arbeitsebene als
  AsyncAPI-Vertrag ausgibt.
- [Einen governten Workflow bauen](/de/how-to/build-a-workflow/) — der praktische Pfad,
  sobald Sie wissen, was die Ebene tut und was nicht.
