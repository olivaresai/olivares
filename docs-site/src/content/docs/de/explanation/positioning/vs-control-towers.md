---
title: Olivares AI vs. AI Control Towers
description: >-
  Wie sich Olivares AI zu AI Control Towers und Ökosystem-Governance-Dashboards
  verhält (ServiceNow AI Control Tower, Agent-Admin-Planes der Hyperscaler). Wir
  integrieren, wir konkurrieren nicht — wir sind die Ground-Truth-Quelle unter dem Tower.
sidebar:
  order: 4
---

Ein **AI Control Tower** ist die organisationsweite Dashboard- und Workflow-Schicht für
AI-Governance: ein zentraler Ort, um registrierte Agenten zu sehen, Freigaben zu
steuern, Tickets zu erstellen und der Führungsebene über die Posture zu berichten.
Beispiele sind der **ServiceNow AI Control Tower** und die Agent-Admin-Planes der
Hyperscaler (Microsofts Entra-Agent-ID-/Agent-365-Oberflächen, die Governance-Funktionen
von AWS AgentCore).

Wenn Sie in einen solchen investiert haben, lautet die richtige Frage nicht „Tower oder
Olivares?“. Sie lautet „Was speist den Tower mit der Wahrheit?“. Unsere Antwort ist
bewusst: **Wir integrieren; wir konkurrieren nicht.**

:::tip[Die Kurzfassung]
Control Towers sind stark bei **Workflow, Ticketing, organisationsweiten Dashboards und
der Governance von Agenten innerhalb ihres eigenen Ökosystems**. Sie sind schwach bei
**heterogenen, selbst gehosteten, Multi-Cloud-Estates** und bei der **Ground Truth** —
dem, was ein Agent tatsächlich berührt hat, abgeglichen gegen die Data Plane. Olivares AI
ist die **Quellschicht unter dem Tower**: Es erzeugt das attribuierte Inventar, den
Permitted-vs-Observed-Drift und die manipulationserkennbare Evidenz und **schiebt diese nach oben**.
:::

## Was Control Towers gut können

- **Workflow und ITSM**: Freigaben, Änderungseinträge, Incident-Tickets, Ownership —
  der bestehende Prozess der Organisation, in den sich AI-Governance einklinken sollte,
  statt ein paralleles Silo zu starten.
- **Executive Reporting**: eine einzige Ansicht für die Führungsebene über viele
  AI-Initiativen hinweg.
- **Ökosystem-native Governance**: der Tower eines Hyperscalers verwaltet die Agenten *in
  der Cloud dieses Hyperscalers* gut — dessen Identitäten, dessen Policies, dessen Runtime.

Das sind echte Stärken, und wir bilden sie nicht nach. Olivares AI ist kein ITSM-Produkt
und versucht nicht, das Reporting-Dashboard Ihres CISO zu sein.

## Wo die Towers eine Lücke lassen

| Lücke | Warum es zählt | Was Olivares AI bietet |
|---|---|---|
| **Heterogenes Estate** | Agenten laufen über Clouds, On-Prem, Laptops und CI hinweg — nicht nur in der Runtime eines einzigen Anbieters | Estate-weites Inventar und Access Map über SQL-/Object-/Warehouse-Stores, MCP, Tools und den lokalen Dev-Agenten hinweg |
| **Ground Truth** | Ein Tower zeigt, was *registriert* ist; er bestätigt selten, was Agenten *getan* haben | Selbstberichtete Telemetrie abgeglichen gegen pgAudit / CloudTrail / eBPF — Permitted-vs-Observed als Fakt |
| **Enforcement am Dev-Agenten** | Towers beobachten; nur wenige können die Aktion eines lokalen Agenten deny-closed stoppen | Der [Claude-Code-Hooks-PEP](/de/how-to/connectors/claude-code-hooks-pep/) und deny-closed Actuation-Gates |
| **Manipulationserkennbare Evidenz** | Dashboards sind veränderbar; Prüfer wollen unveränderlichen Nachweis | Append-only, Ed25519-signiertes Ledger; OSCAL-Evidenzpakete; Off-Box-Verifikation |
| **Souveränität** | SaaS-Towers verarbeiten Ihre Governance-Daten in ihrer Cloud | Self-hosted / Air-Gapped; die Data Plane verlässt nie Ihre Grenze |

## Wie wir uns einklinken (in beide Richtungen)

Olivares AI ist dafür gebaut, **unter** Ihrem Tower zu sitzen und ihn zu speisen, und
**aus** den Towers zu lesen, die ein Roster bereitstellen.

- **Posture und Evidenz nach oben schieben.** Exportieren Sie das Inventar und die Posture,
  damit ein Control Tower sie konsumieren kann (`GET /v1/m/posture/export`), und leiten Sie
  das Audit-Ledger und die Findings in Ihr **SIEM/ITSM** weiter, damit sie im bereits
  betriebenen Workflow landen.
  → [Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/)
- **Identitäts-Roster nach unten lesen, schreibgeschützt.** Die Identitäts-Federation-Connectoren
  synchronisieren Agenten-Roster von **Microsoft Entra Agent ID**, **AWS AgentCore Identity**,
  **Google Agent Identity** und schreibgeschützt von **Microsoft Agent 365** und
  **ServiceNow AI Control Tower** — und bilden sie auf das SPIFFE/WIF-Roster ab, sodass die
  Access Map Kanten realen, governten Identitäten zuordnet. Siehe
  [Wo Olivares AI zu Ihrem IdP passt](/de/explanation/architecture/where-it-fits-with-your-idp/).

Die Beziehung ist **per Design komplementär**: Der Tower besitzt den Workflow und die
Vorstandsansicht; Olivares AI besitzt die Ground Truth und die unveränderliche Evidenz,
die die Zahlen des Towers vertrauenswürdig machen.

## Wann der Tower genügt

Wenn Ihr gesamtes Agenten-Estate innerhalb **eines** Hyperscaler- oder SaaS-Ökosystems
lebt, der native Tower dieses Anbieters es verwaltet und Sie **keine Souveränitätsanforderung
und keinen heterogenen/selbst gehosteten Footprint** haben, brauchen Sie womöglich keine
separate Control Plane — der native Tower plus dessen Audit-Export kann Sie abdecken.
Olivares AI wird notwendig, wenn das Estate **gemischt** ist, wenn Sie **bestätigte Ground
Truth statt einer Registry** benötigen oder wenn **eine von einem Anbieter gehostete
Control Plane für Ihre Governance-Evidenz nicht infrage kommt**.
