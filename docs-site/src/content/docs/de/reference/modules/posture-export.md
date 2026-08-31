---
title: "Posture-Export an Control-Towers"
description: >-
  Eine schreibgeschützte, ausgehende Projektion der Ground-Truth-Posture der
  Engine — entdecktes Inventar, Least-Privilege-Drift und Security-Findings —,
  die ein Control-Tower abruft, um seine eigene Sicht anzureichern. Eine
  Neutral-JSON-Projektion, kein verifizierter nativer Push.
---

Posture-Export (`modules/posture-export`) ist die **ausgehende
Posture-Oberfläche** der Engine: ein einzelner schreibgeschützter Endpunkt, den
ein Control-Tower abfragt, um sein eigenes Inventar mit dem
Ground-Truth-[Access-Graphen](/de/reference/modules/iii-access-map/) der Engine,
dem Least-Privilege-Drift, dem entdeckten Inventar und der Security-Posture
anzureichern. Es ist die „integrieren, nicht konkurrieren"-Seite der Plattform —
es emittiert nie Identität (das ist eingehend, im Besitz der
[Governance](/de/reference/modules/vi-governance/)), nur Posture, und es ändert
nichts.

## Was es bereitstellt

Eine Route, `GET /v1/m/posture/export`, gegatet durch `posture:export:read` und
auf einen einzigen Tenant-Scope fixiert. Die Antwort ist ein neutrales
JSON-Dokument, zusammengesetzt innerhalb **einer auditierten Transaktion** mit
drei Projektionen:

- **`inventory`** — aktive entdeckte Entitäten (Kind, Ref, Status,
  Signalquellen, Hosts, First/Last seen, Vorkommenszähler), optional gefiltert
  durch `?kind=`.
- **`posture_drift`** — der abgeglichene Least-Privilege-Drift:
  beobachtete-aber-nicht-erlaubte Zugriffe, plus Zähler für ungenutzte Grants und
  Inventar-Grants.
- **`findings`** — Security-Findings projiziert nur als Refs und ein
  `detail_hash`, filterbar nach `?severity=`-Untergrenze und `?category=`.

Jeder Export ist **minimal-data** — nur Refs, Hashes und Relationen, nie ein
roher Payload oder ein Secret — und ein defensiver Maskierungsdurchlauf bereinigt
jedes Freitextfeld. Der Export selbst bewegt Daten aus der Box heraus, daher
**auditiert er sich selbst** ins Ledger mit dem realen Principal in derselben
Transaktion wie die Lesevorgänge.

## Reife und Bounded Context

**PARTIAL.** Die Export-Aktion ist live und auditiert; was *nicht* verifiziert
ist, ist das andere Ende. Die Ingest-Formate der genannten Towers — **Microsoft
Agent 365** und **ServiceNow AI Control Tower** — haben keine Primärquellen-API,
gegen die die Engine validieren könnte, daher ist dies eine **ehrliche
Neutral-JSON-Projektion, die ein Tower abruft (oder die ein Operator durch einen
konfigurierten Sink leitet), ausdrücklich KEIN funktionierender nativer Push**.
Jede Antwort trägt diesen Provenance-Hinweis inline.

Pro-Request-Caps begrenzen Inventory, Drift und Findings; ein partieller Export
meldet seine eigenen Truncation-Flags und wird nie als maßgeblich gekennzeichnet.

## Verwandt

- [Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/) — die
  `siemforward`-Ebene, das *Push*-Gegenstück, das das versiegelte Ledger und die
  Findings an einen SIEM-Tower versendet.
- [Modul XIII — Compliance & Regulatorik](/de/reference/modules/xiii-compliance/) —
  die versiegelte Evidenz, mit der diese Posture ihre Ground Truth teilt.
- [Modul III — Access- & Resource-Map](/de/reference/modules/iii-access-map/) —
  der abgeglichene Drift, den der Export projiziert.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — warum dies eine
  Projektion ist, kein verifizierter Push.
- [Modulkatalog](/de/reference/modules/overview/) — wo der Posture-Export unter
  den 30 ausgelieferten Modulen sitzt.
