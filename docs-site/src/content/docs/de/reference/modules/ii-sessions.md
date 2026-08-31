---
title: "Modul II — Live-Betrieb & Sessions"
description: >-
  Das Live-Betriebs-Overlay pro Agent-Session: aktuelle Aktion, Live-Tokens/
  -Kosten, ein abgeleiteter Claude-Code-State und eine abspielbare Timeline,
  gestreamt über Server-sent Events. Was es ableitet, was ehrlich leer bleibt
  und die Grenzen.
---

Modul II ist die **Live-Betriebs**-Sicht des Estate: was jede Agent-Session
gerade tut, ihre Live-Token- und Kostensummen, ein abgeleiteter Claude-Code-State
und eine rekonstruierbare Timeline. Während Modul I (Inventar) das dauerhafte
Estate materialisiert, hält Modul II ein **Live-Betriebs-Overlay** pro Session
über demselben Beobachtungsstrom — und zeigt nur, was dieser Strom ehrlich
trägt.

## Was es ist

Modul II ist ein busgetriebenes Modul der Core-Schicht, Geschwister des
Inventars. Es pflegt einen Live-Datensatz, der nach der externen Referenz jeder
Session gekeyt ist, aufgebaut aus dem kooperativen Beobachtungsstrom — nie
gepollt, nie erfunden. Pro Session verfolgt es:

- die **aktuelle Aktion** (das zuletzt verwendete Tool) und die Ressource/den
  Modus, die sie berührte;
- die **Live-Token- und Kostensummen**, gelesen aus Kostenproben (das kanonische
  Kosten-Ledger und FinOps sind Modul XI, nicht hier — dies ist nur die
  Live-Zahl);
- einen **abgeleiteten Claude-Code-State** (`cc_state`); und
- eine **Timeline**, an die jedes beobachtete Event in Ingest-Reihenfolge
  angehängt wird.

## Sein Vertrag & seine Entitäten

Das Modul registriert zwei mandantenbezogene Entitäten. `sessions.live` hält den
Live-Datensatz pro Session — aktuelle Aktion/Ressource/Modus, Modellreferenz,
Live-Input/-Output-Tokens, Live-Kosten, Event- und Tool-Call-Zähler sowie
First/Last-Event-Zeitstempel. `sessions.timeline` hält eine abspielbare Zeile pro
Event, geordnet nach Ingest. Es gibt **keine gespeicherte
Lebenszyklus-Spalte**: Der kooperative Strom trägt kein Ende-oder-Fehler-Signal,
sodass das einzige ehrliche Lebendigkeitssignal der abgeleitete `cc_state` ist.

`cc_state` wird **zur Lesezeit** aus der Aktualität der Events abgeleitet —
`active` / `idle` / `ended` — und wechselt in einen Silent-Evasion-State, wenn
der Connector dieses Finding meldet (es wird nie vom Modul selbst geschrieben).
Reads werden unter Modulrouten bedient (Live-Liste, einzelne Session,
Session-Timeline) plus einem Live-SSE-Stream; jeder Read erfordert die
Session-Leseberechtigung, und **das Öffnen des Streams wird automatisch
auditiert**. Der SSE-Kanal ist streng **mandantenisoliert** (ein Client erhält
nur Snapshots für seinen autorisierten Mandanten) und **Best-Effort** (ein
langsamer Client verwirft den Zwischenframe und erhält den nächsten — die
Ingestion blockiert nie).

## Was es konsumiert (und was es ableitet)

Modul II konsumiert denselben Minimal-Data-Beobachtungsstrom wie das Inventar —
[`edge.observed`](/de/reference/events/), `cost.sampled` und `finding.reported`. Nur
Edges, deren Herkunft eine **Session** ist, erzeugen Live-Betrieb; an eine
Session gebundene Kostenproben tragen zur Live-Token-/Kostenzahl bei (hier wird
kein `CostRecord` geschrieben); Findings mit Session-Subjekt werden annotiert,
und ein Anti-Evasion-Finding markiert den Evasion-State. Zwei Felder werden
**live abgeleitet** aus denselben Signalen: `agent_ref` aus dem einer Session
zugeordneten Agent und `summary` aus einem Context-Compaction-Finding
(forensisch), dessen Titel per Vertrag summary-safe ist — niemals eine vom LLM
erfundene Zusammenfassung.

:::caution[Ehrliche Grenzen]

- **`goal` bleibt leer — ehrlich.** Der kooperative Strom ist Minimal-Data und
  trägt das Ziel oder die Aufgabenliste einer Session **nicht**; sie werden am
  Connector bereinigt, und es gibt keinen In-Process-Prompt-Text auf der
  Leitung. Der Live-Datensatz modelliert das Feld, damit Vertrag und UI bereit
  sind und ein künftiger Metadatenkanal es befüllen kann, aber das Modul
  **erfindet es nie**.
- **Kein gespeicherter Lebenszyklus.** Der Strom hat kein Ende/Fehler-Signal,
  sodass die Lebendigkeit einer Session der **abgeleitete** `cc_state` nach
  Aktualität ist — kein persistierter Status. Ein `ended`-State bedeutet *keine
  kürzlichen Events*, kein bestätigtes sauberes Herunterfahren.
- **Die Live-Zahl ist nicht das Ledger.** Live-Tokens/-Kosten sind eine
  betriebliche Ablesung aus Kostenproben; der maßgebliche, abstimmbare
  Kostendatensatz ist das FinOps-Ledger von Modul XI. Behandeln Sie die
  Live-Zahl nicht als Abrechnungswahrheit.
- **Minimal-Data ist eine Eigenschaft der Leitung.** Nur Referenzen,
  Klassifizierungen und Lebendigkeits-/Kostenzähler werden getragen und
  persistiert — niemals Payloads, Prompts, Befehle oder PII.
:::

## Verwandt

- [Event-Bus-Referenz](/de/reference/events/) — die Events `edge.observed`,
  `cost.sampled` und `finding.reported`, die dieses Modul konsumiert.
- [Modulkatalog](/de/reference/modules/overview/) — wo Modul II einzuordnen ist und
  die ehrliche Actuate-Aufteilung.
- [Access- & Ressourcen-Map](/de/reference/modules/iii-access-map/) — das
  Geschwister-Core-Modul, das den R/RW-Access-Graphen besitzt.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine und die Schichten.
- [Claude Code anbinden](/de/how-to/connect-claude-code/) — den Live-Stream zu erzeugen beginnen.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — was das Produkt heute tut und was nicht.
