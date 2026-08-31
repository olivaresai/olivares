---
title: "Erläuterung"
description: "Verständnisorientierter Überblick über Olivares AI: wie es Enterprise-AI als eine Ground Truth: Claude Code auf der tiefsten Stufe, Codex und Grok Build daneben integriert, verwaltet und absichert — seine modulare Architektur über 30 Module, die read-first Access Map und das Open-Core-Modell."
---

Dieser Abschnitt ist verständnisorientiert. Er erklärt, *warum* Olivares AI so
geformt ist, wie es ist — die Entwurfsprinzipien, die Sicherheitslage und das
Lizenzmodell —, statt Sie durch eine Aufgabe zu führen. Wenn Sie etwas *tun*
möchten, beginnen Sie mit dem [Tutorial](/de/tutorials/zero-to-graph/) oder den
[How-to-Anleitungen](/de/how-to/connect-claude-code/); wenn Sie einen genauen Vertrag
brauchen, nutzen Sie die [Referenz](/de/reference/). Dazu, wo welche Art von Seite
liegt, siehe [Wie die Docs organisiert sind](/de/start/how-the-docs-are-organized/).

:::note[Produkt in der Entwurfsphase]
Vieles von der hier beschriebenen Tiefe ist pre-1.0 und in der Entwurfsphase. Diese
Seiten sind ehrlich darüber, was heute läuft versus was geplant oder post-v1 ist.
Wenn eine Fähigkeit nicht gebaut ist oder ihre Abdeckung teilweise ist, sagt die
Seite das. Siehe [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/) für die
fortlaufenden Offenlegungen des Projekts.
:::

## Eine modulare Plattform: Engine + Module + Konnektoren

Olivares AI hilft Unternehmen, **die AI, die sie bereits betreiben, zu
integrieren, zu verwalten und abzusichern** — eine Ground Truth: Claude Code auf der tiefsten Stufe, Codex und Grok Build
daneben, sie ergänzend statt mit ihnen konkurrierend. Es wird als ein einzelnes
statisches Go-Binary (`olivares`) ausgeliefert, mit eingebettetem Web-UI, das vom
selben Origin wie die API bereitgestellt wird. Die Architektur ist eine Plattform,
kein einzelnes Werkzeug: Eine **Core-Engine** stellt die gemeinsamen Subsysteme
bereit — Ingest und einen in-process Event Bus, das Connector SDK, die Module
Runtime, ein mandantenfähiges Datenmodell, die REST-/gRPC-API, Authentifizierung
und Autorisierung und das append-only Audit-Ledger — und jede Fähigkeit ist eines
von **30 Modulen**, das an diesen Subsystemen hängt, ohne den Core neu zu
architektieren. **Konnektoren** speisen die Engine von außen über ein stabiles
SDK; ein Konnektor importiert niemals aus dem Core, was die Lizenzgrenze sauber
hält.

Der Standard-Store ist SQLite (reines Go) für Single-Node- und air-gapped Nutzung
und wechselt zu Postgres mit Row-Level Security für Mandantenfähigkeit und
Skalierung. Der Event Bus ist standardmäßig in-process; NATS ist eine optionale
verteilte Anbindung, keine Voraussetzung. Die Plattform liefert heute **30 Module**
aus, jedes mit seinem eigenen ehrlichen Reifegrad — die meisten live und
end-to-end verdrahtet, einige teilweise oder opt-in — über neun Fähigkeitsbereiche;
eine Own-Model-Registry und Fine-Tuning ist eine **geplante Fähigkeit**, kein
ausgeliefertes Modul.

→ Lesen Sie den [Architekturüberblick](/de/explanation/architecture/overview/) für
die vollständige Engine, das Datenmodell und die Deployment-Topologien.

## Die Access Map: read-first, minimal-data, Permitted-vs-Observed

Zu den nützlichsten der 30 Fähigkeiten gehört die **R/RW Access Map**. Sie baut
einen Graphen darüber, welcher Agent welche Ressource liest oder schreibt, und tut
dies mit zwei bewussten Randbedingungen:

- **Read-first.** Die Map beobachtet durch Telemetrie, native Audit-Logs und eine
  eBPF-Kernel-Rückfallebene — sie sitzt außerhalb des Datenpfads, nie in ihm. Sie
  proxyt, fängt nicht ab und gated keinen Live-Traffic.
- **Minimal-data.** Sie speichert nur die Relation (Agent → Ressource, Lesen oder
  Schreiben) zusammen mit der Signalquelle und einem Konfidenzgrad. Sie speichert
  keine Payloads, Secrets oder PII.

Auf diesem Graphen sitzt die unverwechselbarste Sicht: der
**Permitted-vs-Observed-Diff**, der Least-Privilege-Drift offenlegt, indem er
vergleicht, was die Policy *erlaubt*, gegen das, was Agents *beobachtet* tun. Der
kooperative, hochauflösende Pfad ist Claude Code über OpenTelemetry plus
MCP-Introspektion, bestätigt durch natives Store-Audit (zum Beispiel pgAudit, das
Lese- und Schreibzugriffe klassifiziert, oder CloudTrail, das read-only Zugriff auf
Object Storage offenlegt); die nicht-kooperative Rückfallebene ist eBPF im Kernel.
MCP-Annotationen werden gemäß der MCP-Spezifikation als nicht vertrauenswürdig
behandelt und bestätigt, niemals allein vertraut.

:::caution[Die Abdeckung ist gestuft]
Die Genauigkeit hängt von der Quelle ab. Sie ist sauber für SQL-Datenbanken,
Object Stores und Warehouses; verlustbehaftet für Systeme wie Dokument- und
Vektordatenbanken; und passiv nicht erreichbar für einige Stores (zum Beispiel
Redis, SQLite oder D1). Die Map zeigt ihren Konfidenzgrad, statt eine Zuordnung zu
fabrizieren, die sie nicht hat.
:::

→ Lesen Sie das [Sicherheitsmodell](/de/explanation/security/security-model/) für die
Haltung und das [Bedrohungsmodell](/de/explanation/security/threat-model/) für die
Annahmen und Grenzen.

## Self-hosted und Open-Core

Die Data Plane — die Collectors — **läuft immer auf der Kundeninfrastruktur**,
sodass Estate-Daten die Grenze des Kunden nicht verlassen müssen. Die Control
Plane kann als ein einzelnes self-hosted Binary, als verteiltes Deployment
(Collectors, die über gRPC mit mTLS an einen zentralen Core pushen, gestützt auf
Postgres) oder vollständig air-gapped mit null Egress und einer Offline-Lizenz
laufen; eine Managed-Option ist Zukunftsarbeit.

Die Lizenzierung ist Open-Core. Der Engine-Core, die Module und das Web-UI sind
AGPL-3.0-only; das SDK und die Konnektoren sind Apache-2.0; eine Enterprise-Stufe
ist kommerziell. Diese Trennung ist es, die Dritten erlaubt, Konnektoren zu bauen,
ohne dass die Copyleft-Grenze ihren Code erreicht.

→ Lesen Sie [Open Core und Lizenzierung](/de/explanation/open-core-and-licensing/) für
die Lizenz-Map pro Verzeichnis und was sie in der Praxis bedeutet.

## Architekturentscheidungen

Die Begründung hinter den tragenden Entscheidungen — opake Bearer-Token statt JWTs,
der austauschbare Autorisierungs-PDP hinter einer einzigen Naht,
SQLite-zu-Postgres, das hash-chained und signierte Audit-Ledger — ist als
Architecture Decision Records festgehalten.

## Regulierung, Positionierung & Passung

Zwei weitere verständnisorientierte Stränge sitzen neben der Architektur. Der erste
ist **regulatorisch**: wie die Control Plane das Live-Verhalten Ihres Estates in
die technische Evidenz verwandelt, die eine EU-AI-Act-Akte braucht, generiert aus
Laufzeitdaten und in der von Ihnen selbst betriebenen Control Plane gespeichert.

→ Lesen Sie [EU-AI-Act-Evidenz aus Laufzeitdaten](/de/explanation/eu-ai-act-evidence/).

Der zweite ist **wo das Produkt im Markt sitzt** — ehrlich definiert, mit jeder
Statistik zu einer Primärquelle zurückverfolgt. Diese Seiten erklären das
Analysten-Vokabular (Agent Sprawl, Guardian Agents, AI TRiSM), wie Olivares AI sich
zu benachbarten Werkzeugen verhält (LLM-Gateways/Observability, AI Control Towers —
wir integrieren, wir konkurrieren nicht), die Hochschul-Vertikale und woher die
Daten und Behauptungen stammen.

→ Durchstöbern Sie [Positionierung & Passung](/de/explanation/positioning/market-context-and-sources/),
beginnend mit dem verifizierten
[Marktkontext & Quellen](/de/explanation/positioning/market-context-and-sources/).
