---
title: "Modul IV — Agentenübergreifende Kommunikation & Orchestrierung"
description: >-
  Die Observe-and-Govern-Ebene dafür, wie Agenten sich koordinieren: ein
  abgeleiteter Kommunikations- & Delegationsgraph, kontrollierte geplante
  Agenten und ein zweiphasiges, HITL-gestütztes Auslösen. Live-Dispatch ist ein
  deny-closed Seam, ehrlich benannt.
---

Modul IV ist die **Observe-and-Govern**-Ebene dafür, wie Agenten sich
koordinieren. Es implementiert **kein** Agenten-Framework neu (kein
LangGraph/CrewAI/AutoGen), es betreibt keinen Agenten und es startet niemals
einen Prozess. Es leitet einen Live-Kommunikations- & Delegationsgraphen aus
Signalen ab, die bereits auf dem Bus liegen, regiert geplante/autonome Agenten
als Desired-State-Deklarationen und markiert Kadenz-Umgehung — während der Akt
des *Ausführens* eines Agenten nur durch einen deny-closed Seam austritt.

## Was es ist

Zwei Dinge stehen nebeneinander. Erstens ein **abgeleiteter Kommunikations- &
Delegationsgraph** — wer an wen delegiert (Supervisor→Worker) und wer mit wem
spricht — aufgebaut als View über bereits beobachtete Zugriffskanten, ein
Geschwister der Access Map ([Modul III](/de/reference/modules/iii-access-map/)),
niemals eine erneut eingelesene zweite Kopie. Zweitens ein Register von
**kontrollierten Schedules**: ein geplanter oder ereignisgesteuerter Agent ist
eine *Desired-State-Deklaration*, und das Auslösen eines solchen ist die einzige
produktionswirksame Aktion, die das Modul exponiert.

## Vertrag & Entitäten

Das Modul besitzt drei Entitätsarten, deklariert im gemeinsamen Datenmodell:

- **`orchestration.relation`** (upsert) — die abgeleitete Graphkante: eine
  `delegation`-, `mcp_server`- oder `mcp_tool`-Verbindung zwischen zwei
  Referenzen, mit einer Signalquelle, einem read/write-`mode`, einer
  `confidence`, Zählern und First-/Last-Seen-Zeitstempeln.
- **`orchestration.schedule`** (lifecycle) — eine kontrollierte Deklaration:
  Subjekt, Trigger-Art (`cron`/`event`/`manual`), eine **opake Kadenz-Spezifikation,
  die niemals zur Selbstauslösung geparst wird**, ein erwartetes Intervall, ein
  Toleranzfaktor, ein gewünschter Status und der deklarierende Principal,
  festgehalten als Eigentümer jeder autonomen Auslösung.
- **`orchestration.decision`** (**append-only**) — ein unveränderliches Ledger
  jeder Auslöseanfrage, Auslösung und verfehlten Kadenz, das den `plan_hash`, den
  Gate-Status, `op_status` und den **echten Principal** trägt (niemals `system`,
  außer bei der Erkennung verfehlter Kadenz).

Die Modulrouten sind erreichbar, aber bewusst **nicht** Teil des ausgelieferten
OpenAPI-Vertrags; ihre feldgenauen Formen leben in den typisierten Interfaces des
Produkts. **Das Auslösen ist zweiphasig und HITL-gestützt**: Phase eins fordert
eine Freigabe an; Phase zwei verifiziert die Freigabe erneut und prüft einen
strikten `plan_hash`-Abgleich (Anti-TOCTOU — ein Re-Targeting oder eine
Re-Kadenz invalidiert eine veraltete Freigabe), bevor irgendein Dispatch
erfolgt. Das Lesen des Graphen und das Auslösen sind **privilegierte,
mandantenbezogene, vollständig auditierte** Aktionen, aufgeteilt nach Verb-Tier
(Lesen für Viewer, Deklarieren/Re-Targeting für Editoren, **Auslösen** nur für
Admins) — siehe [Regieren und freigeben](/de/how-to/govern-and-approve/).

## Was es auf dem Bus konsumiert & produziert

Es konsumiert genau einen Kanal: [`edge.observed`](/de/reference/events/). Eine
Session→Task-Kante wird zu einer Delegationsrelation; MCP-Topologie-Kanten werden
zu Server-/Tool-Relationen; alles andere wird ignoriert. Die beobachtete
Lebendigkeit eines Subjekts für den Kadenz-Check wird aus den Relationen selbst
abgeleitet, sodass kein Schedule je Kante abgefragt wird. Es produziert Findings
auf [`finding.reported`](/de/reference/events/): `orchestration_cadence_miss`, wenn
ein **aktives, wiederkehrendes** Schedule gegenüber seiner deklarierten Kadenz
aufhört zu emittieren (ein einmaliges oder pausiertes Schedule, das schlicht
fertig ist, ist normale Stille und emittiert nichts), und
`orchestration_ungoverned_fire`, wenn ein Auslöseversuch kein verdrahtetes
Freigabe-Gate findet — die Governance-Lücke wird sichtbar gemacht, während die
Auslösung verweigert bleibt. Der Check erfolgt zur Lesezeit und ist auf den
gepinnten Mandanten der Anfrage beschränkt; das Modul führt niemals einen
mandantenübergreifenden Hintergrund-Scan aus.

:::caution[Ehrliche Grenzen]
- **Live-Auslösung ist ein deny-closed Seam.** Das Modul *regiert und plant*; es
  aktuiert niemals von selbst. Eine Auslösung tritt durch einen Dispatcher-Seam
  aus. Bei nicht konfiguriertem Dispatcher (das Standard-Binary) liefert eine
  freigegebene Auslösung ein ehrliches `200` mit Status `declared_not_fired`
  zurück — der sichere Zustand ist „deklariert, nicht ausgelöst“. Ein vom
  Betreiber gebauter und konfigurierter Dispatcher leitet eine freigegebene,
  plan-abgeglichene Auslösung an denselben Deployment-Executor oder an einen
  durch eine signierte Card verifizierten A2A-Task; ein Dispatcher-Fehler liefert
  `502` und setzt last-fired niemals voran. Live-A2A-Delegation bringt ihren
  eigenen deny-by-default Policy-Enforcement-Point mit (signierte Card →
  Allowlist → Plan-Hash → Freigabe) und ist genauso gegated.
- **Die Graphabdeckung ist partiell, und sie sagt es.** Jede Graphantwort trägt
  einen Coverage-Deskriptor. Der abgeleitete Graph deckt Task-Delegation,
  MCP-Topologie und — wo ein A2A-Connector verdrahtet ist — beobachtete
  Peer-to-Peer-A2A ab; Swarm-Querverkehr und Nicht-Task-Frameworks ohne
  emittierenden Connector sind **abwesend, nicht null**. Das Modul präsentiert
  den Graphen niemals als vollständige Agentenkommunikation.
- **Minimale Daten auf der Leitung.** Das Modul persistiert nur Relationen und
  Governance-Evidenz — wer↔wer, Zähler, Zeitstempel, bereinigte Refs —
  **niemals** Nachrichteninhalte, Prompts, Tool-Argumente oder Secrets. Eine
  solche Spalte existiert nicht; sensible Referenzen werden vor der Persistenz
  gehasht. Das ist eine Eigenschaft der Leitung, keine Einstellung.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — die Ebene und der ehrliche
  Aktuationsstatus von Modul IV.
- [Event-Bus-Referenz](/de/reference/events/) — `edge.observed` rein,
  `finding.reported` raus.
- [Zugriffs- & Ressourcen-Map](/de/reference/modules/iii-access-map/) — der
  Geschwistergraph, den dieser daneben ableitet.
- [Regieren und freigeben](/de/how-to/govern-and-approve/) — die zweiphasige,
  human-in-the-loop Auslösung.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — was heute aktuiert und
  was noch ein Seam ist.
- [Architekturüberblick](/de/explanation/architecture/overview/) — wo Modul IV in
  der Intelligence-Ebene sitzt.
