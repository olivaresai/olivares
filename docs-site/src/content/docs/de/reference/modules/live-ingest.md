---
title: "Live-Ingest — der In-Process-Observe-Produzent"
description: >-
  Eines der 30 Module: der „Live-Tap“-Produzent, der die Detektiv-Events
  publiziert, die ein Out-of-Process-Connector nicht emittieren kann. Deny-closed
  und minimal-data: er bewegt keinen Rohinhalt, und jede Observe-Hälfte, die er
  besitzt, ist ehrlich leer statt vorgetäuscht. Teilweise — er ist opt-in und
  env-gegatet.
---

Live-Ingest (`modules/liveingest`) ist eines der 30 verdrahteten Module — ein
**In-Process-Produzent** statt eines Capability-Slots. Es gehört nicht zur
historischen nummerierten Karte I–XXIII. Es existiert aus einem
einzigen architektonischen Grund: ein Out-of-Process-`SourceConnector` kann über
seinen gRPC-Vertrag nur die versiegelte Beobachtungssumme (edge / cost /
finding) streamen, der weder eine Event-RPC noch ein Textfeld hat — er **kann
also kein Detektiv-Event publizieren**. Nur ein In-Process-Modul hält die
Bus-Publish-Fähigkeit, daher ist Live-Ingest die „Live-Tap“-Hälfte, die jene
Events für die Module emittiert, die sie bereits konsumieren.

## Was es ist

Der Claude-Telemetrie-Connector der Control Plane läuft out-of-process als
eingebettetes Plugin; sein `Gather`-Stream trägt nur das eingefrorene
`Observation`-`oneof`. Dieser Wire-Vertrag ist bewusst eingefroren
(breaking-change-geprüft; siehe die
[API-Stabilitätsrichtlinie](/de/reference/api-stability/)) und trägt keinen
Auszug und keine Textoberfläche. Live-Ingest ist der In-Process-Produzent, der
die zwei Events liefert, die der Connector strukturell nicht kann:
`guardrail.observed` für [Modul IX](/de/reference/modules/ix-security/) und
`voice.telemetry.observed` für Modul XVI. Es besitzt keine Entitäten und keine
REST-Oberfläche; es ist ein Publisher auf den [Event-Bus](/de/reference/events/).

## Was es produziert — `guardrail.observed`

Dies ist der fehlende Produzent für die Security-Detektorkette, die
[`guardrail.observed`](/de/reference/events/) bereits konsumiert. Es ist
**deny-closed und opt-in**:

- **Default (Inspektion aus).** Das Modul abonniert nichts, publiziert nichts und
  protokolliert seine leere Hälfte sichtbar — niemals ein stiller No-Op.
- **Mit aktiviertem Betreiber-Opt-in.** Es abonniert `edge.observed` und leitet
  für eine Kante, deren Ressource eine aufgelöste Tool-Referenz ist, einen
  **begrenzten, bereits geschwärzten** `tool_args`-Auszug ab und publiziert ihn
  als `ObservedText`, der nur nicht-sensible Referenzfelder trägt. Der Auszug ist
  der Ressourcen-*Identifier*, den der Connector bereits an der Quelle bereinigt
  hat (ein bereinigter Pfad, ein Host+Pfad ohne Query oder Credentials, ein
  Bash-Programmname mit verworfenen Argumenten, eine MCP-Tool-Referenz).
  Live-Ingest begrenzt ihn und die Security-Kette klammert ihn erneut ein —
  dreifache Verteidigung. Der **Inhalt des Arguments wird am Connector verworfen
  und erreicht niemals den Bus.**

Die Detektorkette emittiert dann automatisch je Detektion ein Finding, über
realen Verkehr.

## Was es produziert — `voice.telemetry.observed`

Ein verdrahteter In-Process-Produzent ausschließlich für allow-gelistete
Voice-/Realtime-Turn-Metadaten — niemals Audio und niemals Transkripttext. Die
Payload ist ein typisierter Wert, der by construction kein Audio, kein Transkript
und keine PII tragen kann, und der Konsument weist jedes Sample mit einem
Schlüssel außerhalb der Allowlist oder einer fehlenden Session-/Agent-Referenz
zurück. Da es in diesem Build kein Voice-Realtime-Backend gibt, **ruft es
nichts auf**: die Observe-Hälfte ist ehrlich ruhend und fabriziert keine
Telemetrie, bis ein Backend sie speist.

:::caution[Ehrliche Grenzen]
- **Deny-closed per Default.** `guardrail.observed` publiziert nichts, sofern der
  Betreiber sich nicht ausdrücklich dafür entscheidet; die leere Hälfte wird
  protokolliert, nicht verborgen.
- **Die Detektionsabdeckung ist schmal und wird als solche benannt.** Weil
  in-process nur bereits bereinigte Argument-*Referenzen* verfügbar sind, sind
  die realistischen Detektionen auf dieser Oberfläche PII oder ein in eine
  Referenz eingebettetes Secret sowie anomale/sensible Ressourcenmuster.
  **Prompt-Injection und Jailbreak liegen außer Reichweite** — sie benötigen den
  Argument-*Inhalt*, den der Connector verwirft. Die Oberflächen `input` /
  `output` / `tool_result` erfordern eine In-Process-Inhaltsquelle, die dieser
  Build unter dem Out-of-Process-Transport und der eingefrorenen Leitung nicht
  hat.
- **Voice-Telemetrie ruht.** Es gibt in diesem Build kein Realtime-Backend, daher
  produziert diese Hälfte nichts, statt Samples zu erfinden.
- **Es bewegt niemals Rohinhalt und weitet niemals die Erfassung des Connectors
  aus.** Minimal-data ist eine Eigenschaft der Leitung selbst, keine darüber
  gelegte Einstellung.
:::

## Verwandt

- [Event-Bus-Referenz](/de/reference/events/) — die `guardrail.observed` /
  `ObservedText`-Payload (ein geschwärzter Auszug auf einem JSON-Fallback, nicht
  die versiegelte Summe) und `edge.observed`.
- [Modul IX — Sicherheit, Guardrails & Audit](/de/reference/modules/ix-security/) —
  die Detektorkette, die den Feed `guardrail.observed` konsumiert, den dieses
  Modul publiziert.
- [Modul XVI — Voice- & Realtime-Agenten](/de/reference/modules/xvi-voice/) — der
  Konsument der (ruhenden) `voice.telemetry.observed`-Hälfte.
- [Modul II — Live-Betrieb & Sessions](/de/reference/modules/ii-sessions/) — leitet
  sein eigenes `goal` / `agent_ref` / `summary` direkt aus Signalen ab, die es
  bereits konsumiert, statt über ein Live-Ingest-Event.
- [Modulkatalog](/de/reference/modules/overview/) — die 30 Module und der ehrliche
  Govern/Observe-vs-Actuate-Split, den dieser In-Process-Produzent stützt.
- [Architekturüberblick](/de/explanation/architecture/overview/) — wo
  In-Process-Module und Out-of-Process-Connectoren sitzen.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — warum leere Hälften
  deklariert und nicht vorgetäuscht werden.
