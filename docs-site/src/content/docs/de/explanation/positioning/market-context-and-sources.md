---
title: Marktkontext & Quellen
description: >-
  Die Marktsignale hinter Olivares AI — Agent Sprawl, scheiternde Pilotprojekte,
  fehlende Zugriffskontrollen — jeweils mit verifizierter Primärquelle, exakter Zahl
  und einem ehrlichen Vorbehalt. Der einzige Ort, von dem jede andere Seite ihre
  Zahlen zitiert.
sidebar:
  order: 1
---

Diese Seite ist die **einzige Quelle der Wahrheit für jede Marktstatistik**, die auf
der Olivares-AI-Website, in der README und in den Docs verwendet wird. Sie existiert,
weil der Markt für AI-Governance von Zahlen überschwemmt ist, deren Attributierung bei
der Weitergabe verstümmelt wurde — und der Analyst eines Käufers wird das prüfen. Lieber
verlieren wir eine prägnante Zeile, als eine Zahl zu zitieren, hinter der wir nicht
stehen können.

:::note[Die Attributierungsregel]
Wir zitieren **ausschließlich Primärquellen**, benennen sie exakt und geben die Zahl so
wieder, wie die Quelle sie nennt. Wir **wäschen** keine Zahl durch einen Blog, der die
Attributierung verloren hat, und wir **stapeln** keine Aggregator-Statistiken („70 % der
Fortune 100…“), die kein Käufer zurückverfolgen kann. Wo ein Befund **vorläufig oder
nicht peer-reviewed** ist, sagen wir das in derselben Zeile. Das spiegelt wider, wie das
Produkt selbst mit Nachweisen umgeht:
[Attributierungskonfidenz](/de/reference/glossary/#attribution-konfidenz) ist ein
erstklassiges Feld, und eine Kontrolle mit nur Design-Stage-Nachweisen meldet
`by_design`, niemals `satisfied`.
:::

## Die Zahlen, die wir verwenden, und woher sie stammen

| Behauptung | Zahl (wie die Quelle sie nennt) | Primärquelle | Vorbehalt / wie wir sie verwenden |
|---|---|---|---|
| Kompromittierten AI-Organisationen fehlten Zugriffskontrollen | **97 %** der Organisationen, die einen AI-bezogenen Sicherheitsvorfall erlitten, fehlten angemessene AI-Zugriffskontrollen; **13 %** der Organisationen meldeten eine Kompromittierung ihrer AI-Modelle oder -Anwendungen | **IBM, *Cost of a Data Breach Report 2025*** (Forschung durchgeführt vom **Ponemon Institute**), IBM Newsroom | Die Attribution lautet **IBM / Ponemon — nicht Forrester**, eine weit verbreitete Fehlzuordnung. Wir verwenden sie für die *Zugriffskontroll-Lücke*, die genau das ist, was die [R/RW Access Map](/de/explanation/#die-access-map-read-first-minimal-data-permitted-vs-observed) und das Permitted-vs-Observed-Diff adressieren. |
| Agentische Projekte werden verworfen | **Über 40 %** der agentischen AI-Projekte werden **bis Ende 2027 eingestellt**, aufgrund eskalierender Kosten, unklaren Geschäftswerts oder unzureichender Risikokontrollen | **Gartner**, Pressemitteilung (2025) | Wir verwenden sie für den *Governance-Schulden*-Punkt — Projekte sterben aus Mangel an Kontrollen und nachweisbarem Wert, nicht an Modellqualität. |
| Guardian Agents werden ein Markt | **Guardian-Agent**-Technologien werden **bis 2030 10–15 % des Marktes für agentische AI** ausmachen | **Gartner**, Pressemitteilung (2025) | Etabliert „Guardian Agents“ als eine von Analysten anerkannte Kategorie. Wir sind explizit, dass wir *kein* Laufzeit-Agent sind, der andere Agenten bewacht — siehe [Analysten-Vokabular](/de/explanation/positioning/analyst-vocabulary/). |
| Die meisten Pilotprojekte zeigen keine P&L-Auswirkung | **~95 %** der generativen-AI-Pilotprojekte liefern **keine messbare P&L-Auswirkung**; extern **gekaufte/partnerschaftlich beschaffte** Tools sind ungefähr **doppelt so oft** erfolgreich wie intern gebaute | **MIT Media Lab, Project NANDA — *The GenAI Divide: State of AI in Business 2025*** (berichtet via *Fortune*, Aug. 2025) | **Vorläufig, nicht peer-reviewed.** Wir kennzeichnen es stets als solches. Wir verwenden den *Buy-vs-Build*-Befund, um das Argument „eine gepflegte Control Plane einführen statt Governance selbst zu stricken“ zu stützen — niemals als gesicherte Statistik. |
| Hochschulbildung nutzt AI schneller, als sie sie regelt | Eine große Mehrheit (**~80 %**) des Hochschulpersonals nutzt AI-Tools, während **weniger als ein Viertel (<25 %)** mit den AI-Richtlinien ihrer Einrichtung vertraut sind | **EDUCAUSE** AI Landscape / Community-Umfragen (2025–2026) | Umfrageschätzungen; verifizieren Sie die exakte Studie/das Jahr vor externer Zitierung. Wir verwenden die *Richtlinienbewusstseins-Lücke* auf der [Hochschulbildungs-Seite](/de/explanation/positioning/higher-education-and-research/). |

## Qualitative Nachweise, auf die wir uns stützen

Dies sind keine Prozentsätze; es sind Positionen aus benannten, zitierfähigen Quellen,
die einordnen, *warum die Kategorie existiert*.

- **Bessemer Venture Partners** (*Atlas — „Securing AI Agents: the defining
  cybersecurity challenge of 2026“*): in-flight, chirurgisches Eingreifen in
  Agentenverhalten ist **„where the market is most underdeveloped and where the clearest
  infrastructure opportunity lies“** und **„most enterprises do not have a precise
  inventory of the agents operating in their environment.“** Dies ist die externe
  Aussage der Lücke, die unsere [Access Map](/de/explanation/) schließt.
- **Anthropic** (Engineering-Posts zu Claude-Code-Sandboxing und Managed Agents):
  self-hosted Sandboxes verlagern die Ausführung in vom Kunden kontrollierte
  Infrastruktur, aber Anthropic **weist Audit-Logging, Policy/RBAC,
  Multi-Host-Orchestrierung und Traffic-Inspektion dem Kunden zu**. Diese delegierte
  Verantwortung ist die Naht, die Olivares AI füllt — siehe
  [vs. Control Towers](/de/explanation/positioning/vs-control-towers/).

## Umfrage-Signale (richtungsweisend — vor externer Zitierung verifizieren)

Unabhängige und Community-Umfragen berichten durchgängig dieselbe Form: Agenten
vermehren sich schneller, als Organisationen sie inventarisieren oder attribuieren
können. Wir behandeln die konkreten Prozentsätze unten als **richtungsweisenden
Kontext**, synthetisiert aus benannten Umfragen; sie sind **nicht** Teil unseres oben
genannten verifizierten Primärsatzes und sollten vor jeder externen Verwendung gegen das
Originalinstrument erneut geprüft werden.

- Umfragen von Cloud Security Alliance / Token Security (n≈418), Protiviti und Optro
  berichten unterschiedlich: ein großer Anteil der Organisationen hat
  **unbekannte/unverwaltete Agenten** in seiner Umgebung, nur eine Minderheit führt ein
  **Echtzeit-Inventar**, eine Mehrheit erlebte im Vorjahr einen **agentenbezogenen
  Vorfall**, und nur eine Minderheit kann eine **Agentenaktion auf einen Menschen oder
  ein System zurückführen**.

Der Punkt, den diese Umfragen in der Summe machen, ist das Einzige, was wir öffentlich
behaupten: **Organisationen verlieren den Überblick über ihre Agenten und können nicht
attribuieren, was diese Agenten tun.** Das ist eine Behauptung, die unser Produkt für
seine Nutzer falsch machen soll — und es ist der ehrliche Kern jeder
Positionierungsseite hier.

## Dinge, die wir bewusst **nicht** behaupten

- Keine Kundenzahlen, Logo-Wände oder „vertraut von N Unternehmen“ — das Produkt ist
  pre-1.0 und pre-launch (siehe [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/)).
- Keine Zertifizierung oder Attestierung, die wir nicht innehaben (SOC 2, ISO
  27001/42001 sind **Readiness**, keine Zertifikate — siehe das Trust- &
  Procurement-Paket, das mit dem Quellcode ausgeliefert wird).
- Keine erfundenen Benchmarks, Durchsatz-Behauptungen oder Genauigkeitszahlen.
  Kapazitätszahlen stammen ausschließlich aus dem reproduzierbaren Benchmark-Harness, mit
  Hardware-Provenienz.
