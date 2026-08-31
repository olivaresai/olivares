---
title: Analysten-Vokabular, ehrlich zugeordnet
description: >-
  Das Analysten-Vokabular 2026 für AI-Governance — Agent Sprawl, Guardian Agents,
  AI TRiSM, discover/observe/govern/secure — definiert, dort attribuiert, wo es eine
  Quelle hat, und dem zugeordnet, was Olivares AI tatsächlich tut und nicht tut.
sidebar:
  order: 2
---

Wenn Sie AI-Tooling evaluieren, sind Ihnen diese Begriffe begegnet: **Agent Sprawl**,
**Guardian Agents**, **AI TRiSM**, **discover / observe / govern / secure**. Sie sind
nützliche Kürzel, und ein Käufer im Jahr 2026 erwartet, dass ein Anbieter sie spricht.
Sie lassen sich aber auch leicht missbrauchen — um zu suggerieren, ein Produkt *sei*
eine Kategorie, wenn es lediglich in ihrer Nähe liegt.

Diese Seite tut drei Dinge: sie **definiert** jeden Begriff, sie **attribuiert** ihn
dort, wo er einen echten Urheber hat, und sie **sagt unverblümt**, welche Begriffe
Olivares AI beschreiben und zu welchen wir nur eine Beziehung haben. Für die Zahlen,
die den zugrunde liegenden Markt belegen, siehe
[Marktkontext & Quellen](/de/explanation/positioning/market-context-and-sources/).

## Agent Sprawl

**Was es bedeutet.** Die unkontrollierte Ausbreitung von AI-Agenten, Copilots,
MCP-Servern und Automatisierungen in einer Organisation — von verschiedenen Teams
erstellt, mit verschiedenen Anmeldedaten, verschiedene Systeme berührend, schneller als
irgendwer ein Inventar führt. Das Ergebnis sind unbekannte Agenten mit unbekanntem
Zugriff.

**Beschreibt es uns?** Es beschreibt das *Problem, für das wir existieren*. Die erste
Aufgabe von Olivares AI besteht darin, Sprawl sichtbar zu machen: es **entdeckt**
(discover) die Agenten, Modelle, MCP-Server und Tools in Ihrer Estate und baut eine
[Read/Write Access Map (Lese-/Schreib-Zugriffskarte)](/de/explanation/#die-access-map-read-first-minimal-data-permitted-vs-observed)
dessen auf, was jedes erreichen kann — read-first, datenminimal, auf **Ihrer**
Infrastruktur. Das
[Permitted-vs-Observed-Diff](/de/reference/glossary/#observed--permitted) verwandelt dann
„wir haben viele Agenten“ in „hier sind die, die Zugriff nutzen, den niemand gewährt
hat“. Sprawl ist die Krankheit; ein genaues, attribuiertes Inventar ist die erste
Behandlung.

## Guardian Agents

**Was es bedeutet.** **Gartners** Begriff für AI-Fähigkeiten, die *andere* AI-Agenten
überwachen, beaufsichtigen oder bei ihnen eingreifen. Gartner prognostiziert, dass
Guardian-Agent-Technologien **bis 2030 10–15 % des Marktes für agentische AI** ausmachen
werden (Gartner-Pressemitteilung, 2025; siehe
[Quellen](/de/explanation/positioning/market-context-and-sources/)).

**Beschreibt es uns? Mit Vorsicht.** Olivares AI liefert das *Governance- und
Aufsichtsergebnis*, um das es in der Kategorie geht — Agentenverhalten beobachten,
Permitted gegen Observed diffen, Aktionen deny-closed gaten und alles in einem
manipulationserkennbaren Ledger aufzeichnen. Aber wir sind **kein** autonomer
Laufzeit-Agent, der im Anfragepfad über andere Agenten räsoniert. Wir sind eine
**read-first Control Plane (Steuerungsebene)**, die *außerhalb* des Datenpfads sitzt:
wir beobachten über Telemetrie, native Audit-Logs und ein eBPF-Kernel-Backstop, und wir
setzen an klar definierten Gates durch (Freigaben, der
[Claude Code Hooks PEP](/de/how-to/connectors/claude-code-hooks-pep/), Kill Switches) —
nicht durch das Einfügen eines AI-Proxys in jeden Aufruf. Wenn „Guardian Agent“
*aufsichtsführende Governance über Ihre Agenten-Estate* bedeutet, dann ja. Wenn es *ein
LLM, das inline Wache steht* bedeutet, ist das eine andere Architektur, und wir werden
sie nicht für uns beanspruchen.

## AI TRiSM

**Was es bedeutet.** **AI TRiSM** — *AI Trust, Risk and Security Management* — ist ein
**von Gartner geprägtes und besessenes Framework** für das Management von Vertrauen,
Risiko und Sicherheit von AI über deren Lebenszyklus. Wie üblich zusammengefasst, umfasst
es **Governance** und **Laufzeit-Inspektion & -Durchsetzung** von AI, neben
Informationsgovernance und Infrastruktursicherheit.

:::caution[Attributierungshinweis]
Das AI-TRiSM-Framework, seine Schichten-Taxonomie und sämtliche Definitionen sind
**proprietäre Gartner-Forschung**. Öffentliche Wiedergaben (einschließlich
Schichtnamen und Diagramme) stammen typischerweise aus **lizenzierten Nachdrucken**.
Wir beschreiben AI TRiSM auf der *Themen*-Ebene und ordnen unsere Fähigkeiten diesen
Themen zu; wir reproduzieren **nicht** Gartners exaktes Modell, beanspruchen keine
Konformität damit und implizieren keine Gartner-Befürwortung.
:::

**Wie wir uns darauf abbilden (Themenebene).**

- **Governance** — Policy-Authoring, Risikoklassifizierung (EU-Stufe × NIST-Funktion),
  Freigaben/HITL, Manage-as-Code und der Framework-Katalog des
  [Compliance-Moduls](/de/reference/modules/xiii-compliance/).
- **Laufzeit-Inspektion** — die Access Map und der Permitted-vs-Observed-Drift,
  Guardrail-/Anomalie-Findings, Session-Timelines — alles read-first und out-of-band.
- **Laufzeit-Durchsetzung** — deny-closed Gates dort, wo wir tatsächlich in einem
  Entscheidungspfad sitzen: Freigaben, der Claude Code Hooks PEP, MCP-Tool-Gating, Kill
  Switches.
- **Informationsgovernance** — PII-/Sensitivitäts-Erkennung über governete
  Wissensbasen, Datenresidenz-Attestierung, Aufbewahrung und Legal Hold.

Wir nutzen AI TRiSM als *Karte des Problemraums, den ein Käufer bereits kennt*, um
Abdeckung zu zeigen — nicht als Abzeichen.

## Discover / observe / govern / secure

**Was es bedeutet.** Die Verb-Sequenz, mit der Analysten und Anbieter den
AI-Governance-Lebenszyklus beschreiben: zuerst **discover** (entdecken), was existiert,
dann **observe** (beobachten), was es tut, dann **govern** (regeln), was es tun darf,
dann **secure** (absichern) der gesamten Estate.

**Beschreibt es uns?** Ja — es liegt nahe an unserer eigenen Produkterzählung, was es
wert ist, in unseren exakten Begriffen festzuhalten, damit die Zuordnung ehrlich ist:

| Analysten-Verb | Was Olivares AI tatsächlich tut |
|---|---|
| **Discover** | Inventar von Agenten, Modellen, MCP-Servern und Tools über die gesamte Estate. |
| **Observe** | Die R/RW Access Map — read-first, datenminimal, mit per-Edge-Attributierungskonfidenz; kooperative Pfade (OTel, Hooks), bestätigt durch nativen Audit (pgAudit, CloudTrail) und ein eBPF-Backstop. |
| **Govern** | Permitted-vs-Observed-Drift, Policy + Freigaben/HITL, deny-closed Aktuierungs-Gates, Manage-as-Code. |
| **Secure** | Guardrails, das manipulationserkennbare Audit-Ledger, Kill Switches, Compliance-Nachweise — **und** Self-Hosting ohne verpflichtende Telemetrie und standardmäßig ohne Egress der Control Plane. Ihren Perimeter überschreitet nur, was Sie dafür konfigurieren: Aufrufe an Ihre Modell-APIs, die von Ihnen eingerichteten SIEM-/Webhook-Ausgaben und ein externer Embedding-Anbieter, falls Sie einen bereitstellen. |

Der ehrliche Vorbehalt, der sich durch alle vier zieht: **die Treue ist gestuft**. Die
Beobachtung ist sauber für SQL-Datenbanken, Object Stores und Warehouses; verlustbehaftet
für Dokument- und Vektor-Stores; und für einige Systeme passiv nicht erreichbar. Die Map
[zeigt ihre Konfidenz](/de/reference/glossary/#attribution-konfidenz) an, anstatt eine
Attributierung zu erfinden, die sie nicht hat.

## Die drei Bahnen, auf die dieses Vokabular zeigt

Streift man die Etiketten ab, bleiben dieselben drei Differenzierungsmerkmale — die
Bahnen, die der Markt offen gelassen hat und zu denen die Texte immer wieder zurückkehren
sollten:

1. **Ground Truth aus der Data Plane.** Wir nehmen einem Agenten nicht sein Wort
   dafür, was er berührt hat. Wir **korrelieren** das kooperative Signal (OTel, MCP,
   Hooks) gegen das eigene Ledger des Systems — pgAudit, das Reads vs. Writes
   klassifiziert, CloudTrail, das Object-Store-Zugriff offenlegt — und ein
   eBPF-Kernel-Backstop für den nicht-kooperativen Fall. Diese Korrelation ist es, die
   Permitted-vs-Observed zu einer *Tatsache* macht, nicht zu einer Selbstauskunft.
2. **Deny-closed Durchsetzung am lokalen Dev-Agenten.** Die meisten Tools *beobachten*
   Claude Code nur. Olivares AI **regelt** es auch: der Hooks PEP verwandelt Policy in
   eine deny-closed Entscheidung am Agenten, nicht in eine nachträgliche Log-Zeile.
3. **Souveränität.** Self-hosted, source-available **AGPL** — die Data Plane verlässt
   niemals Ihre Grenze, und es gibt keine SaaS-Control-Plane in Ihrem Compliance-Pfad.

Jeder Begriff oben dient diesen dreien. Wenn eine Seite hier ein Analysten-Wort
verwendet, dann um den Käufer dort abzuholen, wo er steht — und dann auf eines dieser
drei Dinge zurückzuverweisen, die das Produkt wirklich tut.
