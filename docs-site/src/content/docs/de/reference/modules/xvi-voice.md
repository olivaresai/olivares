---
title: "Modul XVI — Voice- & Echtzeit-Agenten"
description: >-
  Die Observe-and-Govern-Ebene für konversationelle/Echtzeit-Agenten. Sie regelt,
  wer eine Voice-Session öffnen darf, mit welchem Modell und Provider, unter einer
  default-DENY-Policy — und verfolgt Session-Metadaten unter striktem Verbot
  jeglichen Audio- oder Transkript-Inhalts.
---

Modul XVI regelt **konversationelle und Echtzeit-Agenten**. Es ist eine
**Observe-and-Govern**-Ebene: Es implementiert **kein** Voice-SDK neu (Realtime API,
WebRTC, ASR oder TTS) und öffnet selbst nie einen Media-Stream. Es entscheidet, *wer*
eine Voice-Session öffnen darf, mit *welchem* Modell und Provider, unter *welcher*
Policy, und verfolgt die Metadaten dieser Session — nie ihren Inhalt.

## Was es ist

Das Öffnen einer Voice-Schnittstelle wird als **privilegierte Aktion** behandelt,
nicht als freie Operation. Die Policy ist **default-DENY**: Eine Session ohne
erlaubende Policy wird verweigert. Ein Öffnen ist **zweiphasig** und
**human-in-the-loop-gegated** über das [Approval-Gate](/de/how-to/govern-and-approve/);
es ist an einen `plan_hash` gebunden, sodass eine Genehmigung nicht still auf ein
stärkeres Modell hochgestuft werden kann (Anti-TOCTOU), wird dem **realen Principal**
auditiert (nie `system`) und **append-only** belegt. Das Modul selbst ruft nie einen
Provider auf — die Actuation verlässt es über eine separate Dispatch-Naht.

Die andere Hälfte ist **Beobachtung**: Das Modul verfolgt ausschließlich
Session-Metadaten — abgeleiteter Zustand (live/idle/ended, zur Lesezeit aus der
Aktualität der Aktivität berechnet, ohne gespeicherte Lifecycle-Spalte),
Turn-Zählungen, Dauer, Latenz (ehrlicher Durchschnitt und Maximum aus realen Samples)
und BCP-47-Sprache. Daraus erhebt es Governance-**Findings**: eine Policy-Verletzung,
wenn die Telemetrie einen Agenten/ein Modell/einen Provider nennt, den keine Policy
erlaubt, ein Degraded-Latency-Finding, wenn die Latenz eine Policy-SLA überschreitet,
und ein Ungoverned-Open-Finding, wenn ein Öffnen ohne verdrahtetes Gate versucht wird
— die Lücke wird sichtbar gemacht und das Öffnen wird dennoch verweigert.

## Vertrag & Entitäten

Das Modul deklariert drei Entitäten im gemeinsamen Datenmodell:

| Entität | Veränderbarkeit | Zweck |
|---|---|---|
| **session** | veränderlich (Upsert) | Session-Metadaten; **null Inhalt** |
| **policy** | veränderlich | Governance-Deklaration — wer mit welchem Modell/Provider öffnen darf (default-DENY) |
| **decision** | **append-only** | unveränderliches Ledger der Öffnungs-/Schließungs-Entscheidungen |

Eine Policy matcht auf Agent, erlaubtes Modell und erlaubten Provider (jeweils
spezifisch oder Wildcard), mit optionalen Session-Minuten- und Latenz-SLA-Grenzen.
**Keine matchende Policy bedeutet DENY.** Das decision-Ledger erfasst jedes
`open_request`, `open` und `close` mit seinem Policy-Verdikt, Gate-Status und
Ergebnis-Status. Lesezugriff ist die Viewer-Rolle und höher; das Deklarieren einer
Policy und das Öffnen einer Session sind administrative, mandantengebundene und
auditierte Aktionen. Diese Modulrouten werden in der separaten **Beta**-
[Modulrouten-Referenz](/reference/api-beta/) veröffentlicht, nicht im stabilen Kernvertrag —
ihre feldgenauen Formen leben in den typisierten Interfaces des Produkts. Geldbeträge stehen **nicht** hier; FinOps (Modul XI) besitzt
Kosten.

## Was es konsumiert & produziert

Das Modul besitzt eine deny-closed-Ingestion-Naht — sein eigenes
`voice.telemetry.observed`-Event — über das eine **In-Process**-Sonde
Session-Metadaten einspeisen würde. Die Leitung ist **datenminimal per Konstruktion**:
Der Telemetrie-Parser trägt eine Allow-List und **verwirft das gesamte Event**, wenn er
einen verbotenen Schlüssel sieht, sodass niemals Audio, Transkript-Text, ASR/TTS-Text,
Prompt-/Response-Inhalt oder Sprecher-PII persistiert werden kann. Das einzige
gehaltene Transkript-Signal ist ein Einweg-Hash eines *externen*
Transkript-**Locators** — Beleg, dass ein Transkript existiert, niemals das Transkript.
Governance-Findings werden als [`finding.reported`](/de/reference/events/) mit gehashtem
Detail nach Commit emittiert.

## Actuate-Status

Ein Governed-Open versendet **live**: Sobald ein Voice-Dispatcher vom Betreiber
bereitgestellt ist, prägt ein genehmigtes Öffnen ein **serverseitiges, ephemeres
Credential** und gibt nur dieses Credential plus Verbindungskoordinaten zurück —
Modell, Stimme, Tools und Turn-Detection werden **aus der Policy** festgelegt, nie vom
Client, und der Master-Key des Providers verlässt nie den Server. Ohne diese
Bereitstellung ist die Dispatch-Naht **deny-closed**: Ein genehmigtes Öffnen wird
ehrlich als „deklariert, nicht geöffnet" festgehalten, statt vorgetäuscht zu werden.

:::caution[Ehrliche Grenzen]
- **Die Beobachtung ist in diesem Build inaktiv.** Es wird noch kein Voice-Konnektor
  oder keine Sonde ausgeliefert, daher bleibt die Observe-Hälfte **ehrlich leer**, bis
  eine In-Process-Sonde Telemetrie veröffentlicht. Das Modul warnt beim Start, wenn
  nichts es einspeist. Ein Out-of-Process-Plugin **kann** es nicht einspeisen (das
  gRPC-Control-Plane-Proto trägt keinen Event-RPC) — die Sonde muss In-Process sein.
- **Kein Inhalt, niemals.** Dies ist eine harte Eigenschaft der Leitung, keine
  Einstellung: Das Schema hat keine Inhaltsspalte und der Parser verwirft unbekannte
  Schlüssel. Latenz wird als ehrlicher Durchschnitt/Maximum aus realen Samples gezeigt —
  nie ein fabrizierter p50/p95.
- **Kein „Stall"-Finding.** Das Enden einer Voice-Session ist normale Stille (wie ein
  fertiger Agent). Ohne ehrliche Baseline wäre ein Stall-Finding ein False Positive,
  daher wird es bewusst ausgelassen.
- **Pre-1.0.** Wie ein Großteil der Plattform befindet sich dieses Modul in der Tiefe
  im Design-Stadium — siehe [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/).
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul XVI sitzt und sein Actuate-Status.
- [Event-Bus-Referenz](/de/reference/events/) — `finding.reported` trägt die Voice-Findings.
- [Modul IV — Orchestrierung](/de/reference/modules/iv-orchestration/) — die verwandte Dispatch-Naht (Live-Fire).
- [Modul X — Modell- & Provider-Routing](/de/reference/modules/x-models/) — welche Modelle eine Policy erlauben darf.
- [Govern and approve](/de/how-to/govern-and-approve/) — das zweiphasige Open-Gate in der Praxis.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — die Observe/Govern/Actuate-Aufteilung.
