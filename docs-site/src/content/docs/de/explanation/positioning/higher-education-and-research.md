---
title: Hochschulbildung & Forschung
description: >-
  Warum eine self-hosted Control Plane zu Universitäten und Forschungseinrichtungen
  passt — Durchsetzung von Acceptable-Use-Policy über eine föderierte Estate,
  Isolation riskanter Arbeit in Sandboxes und Erstellung von
  Attributierungsberichten, ohne Studierenden- oder Forschungsdaten an eine
  Anbieter-Cloud zu senden.
sidebar:
  order: 5
---

Universitäten und Forschungseinrichtungen haben AI schneller eingeführt, als sie sie
regelten. **EDUCAUSE**-Umfragen berichten, dass eine große Mehrheit (**~80 %**) des
Hochschulpersonals inzwischen AI-Tools nutzt, während **weniger als ein Viertel
(<25 %)** mit den AI-Richtlinien ihrer Einrichtung vertraut sind (EDUCAUSE AI
Landscape / Community-Umfragen, 2025–2026 — Umfrageschätzungen; siehe
[Marktkontext & Quellen](/de/explanation/positioning/market-context-and-sources/)).
Diese Lücke — verbreitete Nutzung, dünnes Richtlinienbewusstsein — ist das
Governance-Problem der Hochschulbildung in einer Zeile.

Der Sektor hat zudem Beschränkungen, die eine **US-SaaS-Control-Plane schwer
verkäuflich** machen: Forschungsdaten unter Förder- oder IRB-Bedingungen,
Studierendenakten unter Datenschutzrecht (FERPA in den USA, DSGVO in der EU) und eine
Kultur dezentraler, föderierter IT, in der jede Abteilung ihren eigenen Stack betreibt.
Eine self-hosted, source-available Control Plane passt gerade wegen dieser
Beschränkungen natürlich.

## Drei Aufgaben, die die Control Plane für die Hochschulbildung erfüllt

### 1. Acceptable-Use-Policy über eine föderierte Estate durchsetzen

Acceptable-Use-Policies (AUPs) für AI sind meist ein PDF, das niemand liest. Die Control
Plane verwandelt die Teile, die *technisch* sind, in etwas Beobachtbares und
Durchsetzbares:

- **Discover** der tatsächlich abteilungsübergreifend genutzten Agenten, Copilots und
  MCP-Server — einschließlich der Schatten-Instanzen, die die Richtlinie nie antizipiert
  hat.
- **Map** dessen, was jedes lesen oder schreiben kann, und **diff Permitted vs.
  Observed**, sodass der Agent einer Forschungsgruppe, der ein nie gewährtes System
  erreicht, als Drift auftaucht.
- **Enforce** der technischen Grenzen deny-closed dort, wo die Plattform in einem
  Entscheidungspfad sitzt — Freigaben/HITL, der
  [Claude Code Hooks PEP](/de/how-to/connectors/claude-code-hooks-pep/), MCP-Tool-Gating —
  anstatt sich darauf zu verlassen, dass alle die AUP gelesen haben.

Der ehrliche Geltungsbereich: die Plattform setzt durch, was *als Policy über
Agentenaktionen und -zugriff ausdrückbar* ist. Sie entscheidet keine Fragen der
akademischen Integrität und liest keine Absicht — sie macht die technischen Guardrails
real und den Rest auditierbar.

### 2. Riskante Arbeit in Sandboxes isolieren

Forschung und Lehrveranstaltungen beinhalten routinemäßig nicht vertrauenswürdigen Code,
adversariale Prompts und experimentelle Agenten. Die Module **Agent-Simulations-/
Test-Sandbox** und **Red-Teaming** der Plattform lassen riskantes Verhalten isoliert
ausüben, abseits von Produktionssystemen, mit aufgezeichneten Ergebnissen.

:::caution[Was die Sandbox ist und was nicht]
Die Ausführungsisolations-Garantie ist das **Sandbox-Modul** — Red-Team-Probes laufen
nur dort, niemals gegen die Live-Control-Plane oder Produktionsagenten. Die Plattform
**erkennt** Code-Ausführungs- und Exfiltrationsmuster und **testet Verweigerung
(refusal)**; sie ist keine universelle Betriebssystem-Sandbox um das Laptop jedes
Studierenden herum. Passen Sie die Behauptung an die Fähigkeit an.
:::

### 3. Attributierungsberichte erstellen

Wenn etwas schiefgeht — eine Beschwerde über Datenverarbeitung, eine
Förder-Compliance-Prüfung, eine Missbrauchsmeldung — lautet die Frage immer *wer hat was
mit welchem System wann getan*. Die Control Plane beantwortet sie aus dem
**append-only, hash-chained, Ed25519-signierten** Ledger, mit per-Edge-
[Attributierungskonfidenz](/de/reference/glossary/#attribution-konfidenz) und
Off-Box-Verifikation. Attributierungsberichte werden aus tatsächlich aufgezeichneter
Aktivität abgeleitet, und der Bericht selbst ist manipulationserkennbar — was wichtig ist,
wenn das Finding Konsequenzen für eine Person hat.

## Warum Self-Hosting hier der ausschlaggebende Faktor ist

- **Keine Anbieter-Cloud im Datenpfad.** Collectors laufen auf der eigenen Infrastruktur der
  Einrichtung; die Access Map speichert nur die *Relation* (Agent → Ressource,
  Lese-/Schreibzugriff) mit einer Quelle und Konfidenz — **keine Payloads, keine PII,
  keine Studierenden- oder Forschungsinhalte**. Für die Governance muss nichts eine
  Anbieter-Cloud durchqueren. Es gibt keine verpflichtende Telemetrie und standardmäßig
  keinen Egress der Control Plane. Den Campus-Perimeter überschreitet nur, was die
  Einrichtung dafür konfiguriert: Aufrufe an ihre Modell-APIs, die von ihr eingerichteten
  SIEM-/Webhook-Ausgaben und ein externer Embedding-Anbieter, falls sie einen bereitstellt.
- **Föderiert von Natur aus.** Eine Control Plane, die multi-tenant, self-hosted und
  identitäts-föderiert ist, spiegelt wider, wie Universitäten IT bereits betreiben —
  Autonomie pro Abteilung, zentrale Sichtbarkeit — statt alles durch einen einzigen
  SaaS-Tenant zu zwingen.
- **Air-Gap- und Souveränitätsoptionen** passen zu sicheren Forschungsenklaven und
  EU-residenten Daten, mit Residenz-Attestierung
  (`GET /v1/m/compliance/residency`).
- **AGPL, source-available, keine Kostenuntergrenze zum Start.** Ein
  Plattform-Engineer oder ein Research-Computing-Team kann sie aufsetzen und jede Zeile
  lesen — der Bottom-up-Adoptionspfad, den der Sektor tatsächlich nutzt, nicht ein
  beschaffungsgebundener SaaS-Vertrag.

## Verwandt

- [EU-AI-Act-Nachweise aus Laufzeitdaten](/de/explanation/eu-ai-act-evidence/) — für
  EU-Einrichtungen unter dem Act.
- [Wo Olivares AI mit Ihrem IdP zusammenpasst](/de/explanation/architecture/where-it-fits-with-your-idp/)
  — Föderation von Campus-Identität und Agenten-Identität.
- [Die Control Plane selbst hosten](/de/how-to/self-hosting/) — loslegen.
