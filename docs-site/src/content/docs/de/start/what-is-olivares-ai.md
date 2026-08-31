---
title: Was ist Olivares AI?
description: >-
  Integrieren, verwalten und sichern Sie die von Ihnen betriebene KI, von einem einzelnen
  Rechner bis zu einer gesamten IT-Landschaft — eine Ground Truth: Claude Code auf der
  tiefsten Stufe, Codex und Grok Build daneben. Ein einzelnes self-hosted Binary, das Ihrer
  KI Kontext, Ressourcenzugriff und verwaltete Sessions gibt und Ihnen die Berechtigungen,
  Richtlinien, Budgets und Audit-Belege liefert, um sie über Ihre Infrastruktur hinweg zu
  betreiben — ohne verpflichtende Telemetrie und standardmäßig ohne Egress der Control
  Plane. Ihren Perimeter überschreitet nur, was Sie dafür konfigurieren, etwa Aufrufe an
  Ihre Modell-APIs und die von Ihnen eingerichteten SIEM-/Webhook-Ausgaben.
---

Olivares AI **integriert, verwaltet und sichert die von Ihnen betriebene KI** — auf einem
Rechner oder über eine gesamte IT-Landschaft hinweg, eine Ground Truth: Claude Code auf
der tiefsten Stufe, Codex und Grok Build daneben, als Ergänzung statt als Konkurrenz.
Während Sie mehr Modelle, Agenten, MCP-Server und Tools über echte, heterogene
Infrastruktur hinweg einsetzen, werden zwei Dinge gleichzeitig schwierig: KI wirklich
nützlich zu machen und sie unter Kontrolle zu halten. Das gilt für einen einzelnen
self-hosted Rechner genauso wie für eine regulierte IT-Landschaft; der Unterschied liegt
im Maßstab, nicht in der Art.

Olivares AI tut beides. Auf der einen Seite gibt es Ihrer KI, was sie zum Arbeiten
braucht — Kontext, Zugriff auf die richtigen Ressourcen, verwaltete Sessions. Auf
der anderen Seite gibt es Ihnen die **granularen Berechtigungen, Richtlinien,
Budgets und Audit-Belege**, um all das zu betreiben: welches Modell und welcher
Agent was erreichen darf, die Daten, die sie berühren, was sie ausführen dürfen,
was sie ausgeben, und den Nachweis, den Sie einem Regulierer vorlegen können.

Alles läuft als ein **einzelnes self-hosted Binary** auf Ihren eigenen Hosts. Es gibt
keine verpflichtende Telemetrie und standardmäßig keinen Egress der Control Plane. Ihren
Perimeter überschreitet nur, was **Sie** dafür konfigurieren: Aufrufe an Ihre Modell-APIs,
die von Ihnen eingerichteten SIEM-/Webhook-Ausgaben und ein externer Embedding-Anbieter,
falls Sie einen bereitstellen. Das ist eine Eigenschaft der Architektur und Ihrer
Konfiguration; es ist eine Beschreibung und **keine Garantie**.

## Eine Fähigkeit: die Read/Write-Zugriffskarte

Zu diesen Fähigkeiten gehört die **R/RW-Zugriffskarte**. Für jeden Ursprung (einen
Agenten, eine nicht-menschliche Identität, eine Session) baut sie eine Kante zu
jeder Ressource, die er berührt, klassifiziert als **read**, **write**,
**read-write** oder **unknown**, und markiert mit:

- **woher das Signal kam** (`SignalSource`) — OpenTelemetry von einem kooperativen
  Agenten, eine Postgres-pgAudit-READ/WRITE-Klassifizierung, ein
  AWS-CloudTrail-Eintrag, ein kernel-level-eBPF/Tetragon-Backstop, eine
  MCP-Annotation (als **untrusted** behandelt und korroboriert, niemals allein
  vertraut), ein deklarierter Policy-Grant oder ein Agent-zu-Agent-Signal (A2A);
  und
- **wie sehr der Attribution zu trauen ist** (`Confidence`) — `attributed`, wenn
  sie fest an eine Pro-Agent-Identität gebunden ist, `approximate`, wenn sie
  abgeleitet ist (ein geteiltes Service-Konto oder ein verlustbehafteter Store).

In ihrem Zentrum steht der Diff: **Permitted vs Observed**. Permitted-Kanten
stammen aus deklarierten Grants; observed-Kanten stammen aus echter Telemetrie und
Audit. Ihr Vergleich deckt *unerwartete Zugriffe* auf (ein Agent, der eine Tabelle
liest, die ihm nie gewährt wurde), *ungenutzte Grants* (eine Berechtigung, die nie
ein Agent genutzt hat) und *reconciliation-pending*-Kanten (ein Zugriff, den das
System noch nicht fest zuschreiben kann).

Das Produkt ist **ehrlich über die Genauigkeit**. Die Abdeckung ist **gestaffelt**:
clean bei Stores mit nativem Audit (SQL, Object Storage, Warehouses), lossy bei
einigen Stores (Dokument/Vektor) und passiv nicht rekonstruierbar bei anderen (z. B.
Redis, SQLite, D1). Wo die Read/Write-Natur nicht bestimmt werden kann, ist der
Modus `unknown` — das Produkt erfindet niemals eine Klassifizierung.

## Eine Plattform, kein einzelnes Feature

Die Zugriffskarte ist eine Fähigkeit unter vielen. Das Produkt ist eine **modulare
Plattform** (im Geiste von Grafana oder Backstage): eine Engine plus Module plus
Connectors, so entworfen, dass sich jedes Modul anbinden lässt, ohne den Rest neu
zu architektieren. Es liefert **30 Module** — Inventar und Live-Sessions, die
R/RW-Karte, Agenten-Orchestrierung (A2A, in Entwicklung), MCP- und Skill-Management, Identität und
nicht-menschliche Identität, Deployment, Wissen und Kontext, Security und
Guardrails, Modell- und Provider-Management, Cost/FinOps, Evals und eine
Test-Sandbox, Red-Teaming, Compliance und Belege, einen internen Katalog,
Output-Integrationen und SIEM-Push, Voice/Realtime und Health/SLA — plus
Plattform-Fähigkeiten, die nicht zu den 30 gezählt werden (seine eigene API und
Manage-as-Code, Mandantenfähigkeit, Executive-Dashboards) — über
**158 Integrationen** hinweg (eine Zahl, die von `scripts/check-public-counts.sh` aus dem
Code gemessen wird). Einige wenige Fähigkeiten sind pre-v1 oder
deny-closed-Nahtstellen, bis sie bereitgestellt sind; die Dokumentation ist
explizit darüber, welche.

Siehe den [Modul-Katalog](/de/reference/modules/overview/) für die vollständige Liste
und die [Architektur-Übersicht](/de/explanation/architecture/overview/) dazu, wie die
Engine und die Module zusammenpassen.

## Wie es beobachtet: read-first, minimal-data

Olivares AI ist **read-first**: die Engine beobachtet über Logs, OpenTelemetry und
eBPF; sie sitzt **nicht** im Datenpfad des Agenten, sodass ein Ausfall des
Collectors niemals Ihren Produktiv-Traffic unterbricht. Und sie ist **minimal-data
by design**: der Zugriffsgraph speichert **Relationen** — Ursprung → Ressource,
read/write, Quelle, Confidence, Zeitstempel — **niemals Payloads, SQL-Bodies,
Secrets oder PII**. Was nicht gespeichert wird, kann nicht leaken.

Das ist auch der Grund, warum es self-hostbar und air-gap-freundlich ist: Es gibt keine
verpflichtende Telemetrie und standardmäßig keinen Egress der Control Plane. Ihren
Perimeter überschreitet nur, was **Sie** dafür konfigurieren — Aufrufe an Ihre
Modell-APIs, die von Ihnen eingerichteten SIEM-/Webhook-Ausgaben und ein externer
Embedding-Anbieter, falls Sie einen bereitstellen. Olivares AI gehört nicht zu dieser
Liste: Der Anbieter liegt nie im Datenpfad. Er wird nur erreicht, wenn Sie etwas von ihm
anfordern — `olivares upgrade` oder ein Abo-Download kommerzieller Add-ons und ihrer
Updates — nie als Nebeneffekt des Betriebs. Und `olivares upgrade --endpoint` richtet selbst das auf Ihren eigenen Mirror. Das ist ein starkes Argument für
Datenresidenz, DSGVO und air-gapped-Umgebungen.

## Wohin als Nächstes

- **Probieren Sie es aus:** das [Zero-to-Graph-Tutorial](/de/tutorials/zero-to-graph/)
  bootet das einzelne Binary und erreicht einen befüllten
  Permitted-vs-Observed-Graphen.
- **Verstehen Sie es:** die [Architektur-Übersicht](/de/explanation/architecture/overview/)
  und das [Security- & Bedrohungsmodell](/de/explanation/security/threat-model/).
- **Betreiben Sie es:** [Self-Hosting](/de/how-to/self-hosting/) und
  [Air-Gapped-Installation](/de/how-to/air-gap-install/).

:::note[Status]
Olivares AI ist **pre-1.0**. Das einzelne Binary baut, bootet und erreicht heute
einen befüllten Zugriffsgraphen (dies wird von der Test-Suite end-to-end
durchexerziert), aber mehrere Fähigkeiten sind im Design-Stadium oder post-v1. Die
Dokumentation ist explizit darüber, was jetzt läuft, gegenüber dem, was geplant ist
— siehe [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/).
:::
