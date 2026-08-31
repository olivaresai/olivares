---
title: Wie diese Dokumentation organisiert ist
description: >-
  Diese Docs folgen Diátaxis — vier Modi (Tutorials, How-to-Guides, Referenz,
  Erläuterung), die jeweils einen anderen Bedarf beantworten. Hier erfahren Sie,
  wie Sie sich darin zurechtfinden.
---

Diese Dokumentation ist mit dem **[Diátaxis](https://diataxis.fr/start-here/)**-Framework
organisiert. Diátaxis stellt fest, dass technische Dokumentation vier verschiedene
Bedürfnisse bedient und dass ihre Vermischung die Docs für alle schlechter macht.
Deshalb steht oben in der Seitenleiste **vier Modi**, keine Liste von
Produktfunktionen:

| Modus | Ausrichtung | Beantwortet | Wenn Sie… |
|---|---|---|---|
| **[Tutorials](/de/tutorials/zero-to-graph/)** | Lernen | "Bring mich von Null zu einem funktionierenden Ergebnis." | neu sind und durch Tun lernen wollen |
| **[How-to-Guides](/de/how-to/self-hosting/)** | eine Aufgabe | "Wie erledige ich *diese konkrete Sache*?" | arbeiten und ein Rezept brauchen |
| **[Referenz](/de/reference/)** | Information | "Was genau sind API, Events, Module, Flags?" | dagegen bauen und Präzision brauchen |
| **[Erläuterung](/de/explanation/)** | Verständnis | "*Warum* ist es so gebaut?" | evaluieren und die Begründung wollen |

Eine schnelle Übersicht, wo die Dinge liegen:

- **Tutorials** — die Lernpfade: [von Null zu einem
  Read/Write-Zugriffsgraphen](/de/tutorials/zero-to-graph/) und der Einstieg pro
  realem Szenario — [Single Node](/de/tutorials/getting-started/single-node/),
  [Docker Compose](/de/tutorials/getting-started/docker-compose/),
  [Kubernetes](/de/tutorials/getting-started/kubernetes/),
  [air-gapped](/de/tutorials/getting-started/air-gapped/).
- **How-to-Guides** — installieren & betreiben ([Self-Host](/de/how-to/self-hosting/),
  [Backup & Restore](/de/how-to/backup-and-restore/),
  [Monitoring](/de/how-to/monitor-with-prometheus/),
  [Troubleshooting](/de/how-to/troubleshooting/)), die
  [Guides pro Connector](/de/how-to/connectors/pgaudit/) (pgAudit, CloudTrail,
  eBPF, Claude Code, MCP, Identität) und das
  [Cookbook](/de/how-to/cookbook/deny-closed-policies/) der Governance-Rezepte
  (Deny-closed-Policies, Budgets, Genehmigungen, Drift-Triage, der Kill Switch,
  SIEM-Push).
- **Referenz** — die [REST-API](/reference/api/) (gerendert aus dem eigenen
  OpenAPI-3.1-Vertrag des Produkts), die [API-Stabilitätsrichtlinie](/de/reference/api-stability/),
  der [Event-Bus](/de/reference/events/) (ein AsyncAPI-3.0-Vertrag), der
  [Modulkatalog](/de/reference/modules/overview/), die [CLI](/de/reference/cli/) und die
  [Konfiguration](/de/reference/configuration/).
- **Erläuterung** — die [Architektur](/de/explanation/architecture/overview/), das
  [Sicherheitsmodell](/de/explanation/security/security-model/) und das
  [Threat Model](/de/explanation/security/threat-model/), die
  [Open-Core-Lizenzierung](/de/explanation/open-core-and-licensing/).

## Konventionen

- **Die Suche** ist lokal und clientseitig (Pagefind). Sie läuft vollständig in
  Ihrem Browser; nichts wird an einen externen Suchdienst gesendet — im Einklang
  mit dem self-hosted Design des Produkts, bei dem Sie bestimmen, was Ihren
  Perimeter überschreitet.
- **Versioniert.** Die Dokumentation ist versioniert: Wenn eine neue
  Produktversion erscheint, wird die Dokumentation der vorherigen bewahrt. Der
  Versionsumschalter befindet sich in der oberen Leiste.
- **Ehrlich über Grenzen.** Wo eine Fähigkeit im Design-Stadium, post-v1 oder
  schlicht noch nicht gebaut ist, sagen die Docs das unverblümt. Siehe
  [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/). Tutorial- und
  How-to-Befehle sind so gemeint, dass sie **wie geschrieben ausgeführt** werden.
- **Sprachen.** Die kanonische Dokumentation ist auf Englisch; Übersetzungen sind
  in Spanisch, Vereinfachtem Chinesisch, Russisch, Japanisch, Deutsch und
  Französisch verfügbar (maschinell übersetzt, Englisch ist maßgeblich, mit
  Rückfall auf Englisch für noch nicht übersetzte Seiten).
