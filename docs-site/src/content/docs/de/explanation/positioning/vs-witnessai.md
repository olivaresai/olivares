---
title: Olivares AI vs WitnessAI
description: >-
  Ein ehrlicher, mit Quellen belegter Vergleich mit WitnessAI — dem direktesten
  Head-to-Head zur Regelung von AI-Agenten in IDEs und Entwickler-Tools. Echte
  Parität bei Agenten-Discovery und MCP-Allowlists; ein klarer, verteidigbarer
  Unterschied für den regulierten, self-hosted Käufer: In-Process-Durchsetzung,
  ein kryptografisches Evidenz-Ledger und eine Data Plane, die Ihre Grenze nie
  verlässt.
sidebar:
  order: 8
---

Die meisten „Wettbewerber“ von Olivares AI sitzen in einer benachbarten Bahn —
Control Towers, Gateways, Observability — und die
[anderen Positionierungsseiten](/de/explanation/positioning/market-context-and-sources/)
erklären, warum diese ein *und* sind, kein *oder*. **WitnessAI ist das echte
Head-to-Head.** Es regelt AI-Agenten innerhalb der Entwicklungsumgebung: es entdeckt
Coding-Agenten, setzt Listen freigegebener Tools durch und wendet Policy auf das an, was
Agenten tun. Diese Seite wird daher an einem höheren Maßstab gemessen — jede Aussage über
WitnessAI weiter unten ist ein wörtliches Zitat von deren eigener Website (abgerufen
2026-06-21), und wo deren Website schweigt, sagen wir *"not documented,"* niemals
*"absent."*

:::note[Wie diese Seite zu lesen ist]
Wir vergleichen anhand von **Architektur und Deployment-Modell**, nicht anhand einer
Feature-Checkliste, denn dort ist der Unterschied real und beständig. Bei den Features, wo
wir uns echt überschneiden, sagen wir das und beanspruchen **keine Überlegenheit**. Das
Differenzierungsmerkmal gilt für einen bestimmten Käufer: die regulierte oder
air-gapped Organisation, die ihre Governance-Daten nicht in die Cloud eines anderen
schicken kann.
:::

## Wo wir auf Parität sind (und nichts anderes behaupten)

WitnessAI leistet echte Arbeit in zwei Bereichen, die Olivares ebenfalls abdeckt. Wir
behandeln diese als **Parität** und behaupten nicht, besser zu sein:

- **Agenten- / Shadow-AI-Discovery.** WitnessAI bewirbt *"Find and catalog
  thousands of AI applications, agents, and MCP servers"* und, für Entwickler,
  *"Discover apps like GitHub Copilot, Cursor, and hundreds of other AI dev tools
  across your network"* ([witness.ai](https://witness.ai/)). Olivares entdeckt und
  inventarisiert ebenfalls Agenten, Modelle, MCP-Server und Tools. Anderer Aussichtspunkt —
  ihr Netzwerk, unsere read-first Telemetrie-plus-Audit — aber das *Discovery*-Ergebnis
  ist vergleichbar, und wir tun nicht so, als wäre unser Katalog kategorisch überlegen.
- **MCP-Allowlists / Governance freigegebener Tools.** WitnessAI: *"Enforce control of
  approved MCP servers and tools across every agent, IDE, and agentic app"* und
  *"Maintain an organization-wide approved-tool list of MCP servers and tools"*
  (witness.ai). Olivares regelt MCP-Tool-Zugriff ebenfalls
  ([MCP-Governance](/de/how-to/connectors/mcp-governance/)). Parität. Kein einziger
  Punkt auf dieser Seite lautet „wir allowlisten MCP besser als sie“.

Wenn Agenten-Discovery und MCP-Allowlisting Ihre gesamte Anforderung sind, ist dies bei
der Capability eine knappe Entscheidung, und andere Faktoren (Deployment-Modell, Preis,
bestehende Verbreitung) sollten sie entscheiden. Das sagen wir lieber, als zu
überzeichnen.

## Was WitnessAI ist, in ihren Worten

WitnessAIs Modell ist **netzwerkebene-basiert und cloud-delivered**, mit einer expliziten
*intent-based* Kontrollphilosophie:

- **Netzwerkebene, clientless.** *"See AI activity across your entire network
  without relying on browser extensions or endpoint clients"*, und eine Plattform, die
  *"operates at the network level—no new SDKs, additional clients, or added
  exposure"* (witness.ai).
- **Intent-based Policy.** *"Traditional security sees text; WitnessAI sees
  intent"*, mit *"intent-based ML engines that understand context, not just
  keywords"* (witness.ai). Dies ist eine reale und eigenständige Designentscheidung und
  eine Stärke für den inline, content-aware Anwendungsfall.
- **Human-attributed Agenten-Governance.** *"every agent action maps back to a human
  identity"*, unter *"a single policy engine [that] governs both human and agent
  workforces"* (witness.ai).
- **Eine SaaS-Souveränitätsgeschichte.** Sie adressieren Datenkontrolle durchaus — *"a
  secure, single-tenant environment that ensures data sovereignty"*, *"single-tenant
  environment with your own key encryption"* und *"regional sandboxes"*
  (witness.ai). Dies ist ein **cloud-seitiges, single-tenant, customer-key**-Modell. Es
  ist eine echte Antwort auf Datenresidenz — und es ist eine *andere* Antwort als unsere,
  was der springende Punkt weiter unten ist.

Dies sind Fähigkeiten, mit Quellen belegt und fair benannt. Der Vergleich lautet nicht
„sie sind schwach“; er lautet „wir sind auf einer anderen Architektur gebaut, für einen
anderen Käufer“.

## Wo Olivares strukturell anders ist

| Dimension | WitnessAI (laut ihrer Website) | Olivares AI |
|---|---|---|
| **Deployment** | Netzwerkebene, cloud-delivered; single-tenant mit Customer Keys und Regional Sandboxes. Self-hosted / on-prem / air-gapped **not documented** | Standardmäßig self-hosted; [air-gapped](/de/how-to/air-gap-install/) unterstützt; die Data Plane verlässt Ihre Grenze nie |
| **Lizenzierung** | Proprietäres SaaS; Open Source **not documented** | Open-Core **AGPL**, source-available — auditierbar, keine SaaS-Control-Plane in Ihrem Compliance-Pfad |
| **Durchsetzungspunkt** | Auf Netzwerkebene, mit *"enforcement at the tool call and MCP server level"* | In-Process an der Agenten-Laufzeit — ein deny-closed [PEP innerhalb von Claude Code](/de/how-to/connectors/claude-code-hooks-pep/), plus MCP- und Aktuierungs-Gates |
| **Evidenz** | *"detailed logging keeps you audit-ready"* — ein kryptografisches / unveränderliches Ledger ist **not documented** | Append-only, hash-chained, [Ed25519-signiertes Ledger](/de/reference/glossary/#audit-ledger), off-box verifizierbar, OSCAL-Export |
| **Live-Intervention** | Human-in-the-Loop-Freigaben / Break-Glass **not documented** | [HITL-Freigaben](/de/reference/glossary/#approval-hitl), [Break-Glass](/de/reference/glossary/#break-glass) und ein [Kill Switch](/de/reference/glossary/#kill-switch) über Live-Sessions, deny-closed |
| **Identitätsmodell** | *"every agent action maps back to a human identity"* — NHI-Lifecycle **not documented** | Agenten als erstklassige [Non-Human Identities](/de/reference/glossary/#identity--nhi) mit Provisionierung, Staleness-Block, Rotation und Offboarding |

Jedes *"not documented"* oben bedeutet genau das: Es erscheint nicht auf den
WitnessAI-Seiten, die wir gelesen haben. Es ist **keine** Behauptung, dass ihrem Produkt
die Fähigkeit fehlt — nur, dass wir in ihrem Namen nichts behaupten, was ihre eigene
Website nicht aussagt.

## Der verteidigbare Wedge: der regulierte, self-hosted Käufer

Streift man die Tabelle herunter, ist ein Unterschied tragend. WitnessAIs Datenkontrolle
ist eine **single-tenant Cloud** mit Ihren Keys; die von Olivares ist eine **self-hosted
Control Plane** auf Ihrer eigenen Infrastruktur — Linux, Docker, Kubernetes, on-prem oder
air-gapped — ohne verpflichtende Telemetrie und standardmäßig ohne Egress der Control Plane.
Ihren Perimeter überschreitet nur, was **Sie** dafür konfigurieren: Aufrufe an Ihre
Modell-APIs, die von Ihnen eingerichteten SIEM-/Webhook-Ausgaben und ein externer
Embedding-Anbieter, falls Sie einen bereitstellen. Für viele Käufer sind diese Modelle
gleichwertig. Für den Käufer, der **vertraglich oder rechtlich von einer
Drittanbieter-Cloud ausgeschlossen** ist — Verteidigung, Verschlusssachen, Sovereign
Cloud, bestimmte regulierte Finanz- und Gesundheitsbereiche — ist ein SaaS- oder
single-tenant-Cloud-Modell disqualifiziert, bevor der Feature-Vergleich überhaupt beginnt,
und eine source-available, self-hostbare Control Plane, die standardmäßig keinen Egress der
Control Plane hat, ist die einzige Art, die das Procurement passiert.

Das ist der ehrliche Wedge: nicht „wir regeln Agenten besser“, sondern **„wir regeln sie
auf Infrastruktur, die Sie vollständig kontrollieren, mit kryptografischer Evidenz und
In-Process-Durchsetzung, für den Käufer, der gar keine Cloud nutzen kann.“** Kombiniert
mit dem In-Process-PEP und dem manipulationserkennbaren Ledger ist das eine Position, die ein
SaaS auf Netzwerkebene nicht durch Hinzufügen eines Features einnehmen kann.

## Wann WitnessAI die bessere Passung ist

Uns ist lieber, Sie wählen gut, als dass Sie uns wählen. WitnessAI ist wahrscheinlich die
bessere Passung, wenn:

- Sie **Netzwerkebene-Sichtbarkeit ohne das Deployen oder Betreiben** einer Control
  Plane wollen und ein single-tenant SaaS Ihren Datenresidenz-Maßstab erfüllt.
- Ihre Priorität die **inline, intent-based Content-Klassifizierung** über den
  allgemeinen Enterprise-AI-Traffic ist (nicht speziell das Problem des
  governed-coding-agent und der manipulationserkennbaren Evidenz, auf das sich Olivares
  zentriert).
- Sie **keine Anforderung an Self-Hosting, AGPL-Source-Verfügbarkeit, ein
  kryptografisches Evidenz-Ledger oder Break-Glass/HITL über Live-Sessions** haben — die
  Dinge, die ihre Website nicht dokumentiert und um die herum Olivares gebaut ist.

Olivares verdient die Entscheidung, wenn das Estate **self-hosted oder air-gapped** ist,
wenn die Evidenz **manipulationserkennbar und off-box verifizierbar** sein muss und wenn die
Durchsetzung **innerhalb des Agenten** leben muss, deny-closed — ohne dass irgendetwas
davon in die Cloud eines anderen Unternehmens übergeht.

:::caution[Quellen und Grenzen]
Jede WitnessAI-Aussage hier ist von deren öffentlicher Website (Homepage, Product,
Developer, Compliance- und Control-Seiten) zitiert, abgerufen am 2026-06-21; wir haben
nicht jede Seite gelesen, die sie veröffentlichen, und *"not documented"* ist auf die
Seiten beschränkt, die wir gelesen haben. Marketing-Copy ist kein Architekturdokument, und
Produktfähigkeiten ändern sich. Wenn Sie beide evaluieren, verifizieren Sie den aktuellen
Stand direkt bei jedem Anbieter — das ist der Maßstab, an den sich dieser gesamte
[Positionierungsabschnitt](/de/explanation/positioning/market-context-and-sources/) selbst
hält.
:::

## Verwandt

- [Abonnement-authentifizierte Claude Code & Codex regeln](/de/explanation/positioning/governing-subscription-authed-agents/)
  — wie die In-Process-Durchsetzung tatsächlich funktioniert.
- [Wo Olivares vs. Ihr Gateway / Guardrails passt](/de/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  — dieselbe „wir konkurrieren nicht auf dem Request-Pfad“-Disziplin.
- [Wo Olivares zu Ihrem IdP passt](/de/explanation/architecture/where-it-fits-with-your-idp/)
  — die read-only Identity-Föderation hinter dem NHI-Modell.
