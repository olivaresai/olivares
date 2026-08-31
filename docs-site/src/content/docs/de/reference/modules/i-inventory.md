---
title: "Modul I — Inventar & Discovery"
description: >-
  Passive Discovery und Katalogisierung von allem im Estate — Agents, Sessions,
  MCP-Server, Skills, Tools, Modelle, Provider und nicht-menschliche Identitäten.
  Wie Entitäten aus Beobachtungen materialisiert werden, was der Katalog erfasst
  und die Grenzen.
---

Modul I ist der **Katalog des Estate**: ein passives, busgetriebenes Inventar
von allem, was existiert — Agents, Sessions, Claude-Code-Instanzen, MCP-Server,
Skills, Tools, Ressourcen, Modelle, Provider und nicht-menschliche Identitäten.
Es betreibt Discovery durch *Zuhören*, nie durch Sondieren, und erfasst nur
Beziehungen, Identifikatoren und Lebendigkeit — niemals Payloads. Diese Seite ist
die Referenz dafür, was der Katalog enthält und was er bewusst nicht enthält.

## Was es materialisiert

Connectoren emittieren **Beobachtungen**, keine Entitäten. Sie veröffentlichen
normalisierte [`edge.observed`](/de/reference/events/)- und
[`cost.sampled`](/de/reference/events/)-Fakten auf dem Event-Bus; die Entitäten, die
sie implizieren, werden nie gesendet. Modul I **materialisiert** die Kernentität,
die jede Beobachtung anhand ihrer natürlichen Referenz benennt: eine Herkunft
`session`/`agent`/`identity`, einen MCP-Server, ein Tool, eine Ressource, einen
Skill und — aus Kostenproben — einen Provider und ein Modell (entdeckt, **ohne
Preisgestaltung**; FinOps besitzt das). Die Materialisierung ist **idempotent**
unter At-least-once-Zustellung: find-or-create auf dem natürlichen Schlüssel,
sodass dieselbe zweimal gesehene Beobachtung nie eine Entität dupliziert.

## Sein Vertrag & seine Entitäten

Das Modul registriert eine eigene Entität, `inventory.catalog_entry` — ein
Discovery-Overlay, das an jede materialisierte Kernentität angehängt wird. Es
erfasst, *wie* etwas gefunden wurde, nicht, *was* es tat: eine Liste von
Signalquellen, die Hosts, auf denen es gesehen wurde, First- und
Last-seen-Zeitstempel, einen Vorkommenszähler und einen Lebendigkeits-`status`
von `active` oder `stale`. Ein periodischer **Staleness-Sweep** markiert einen
Eintrag als `stale`, wenn er nicht innerhalb des konfigurierten Fensters gesehen
wurde, und setzt ihn in dem Moment auf `active` zurück, in dem er wieder
auftaucht; der Sweep läuft nur über die Mandanten, die das Modul tatsächlich
beobachtet hat (es kann Mandanten nicht aufzählen und tut dies auch nicht). Die
Lese-Oberfläche ist klein und schreibgeschützt: eine `summary`-Zählung nach Art
und Quelle, eine paginierte `entities`-Auflistung, filterbar nach Art und Status,
und eine Detailansicht für eine einzelne Entität. Jeder Read erfordert eine
mandantenbezogene, namespaced Leseberechtigung (die niedrigste Viewer-Stufe
genügt); die Ingestion ist hochfrequent und wird nicht pro Write auditiert. Die
vollständigen Formen liegen in der [Event-Bus-Referenz](/de/reference/events/) und
den typisierten Schnittstellen des Produkts.

## Was es konsumiert und produziert

Modul I ist ein reiner **Consumer**. Es abonniert `edge.observed`, `cost.sampled`
und `finding.reported` und schreibt nur sein eigenes Katalog-Overlay sowie die
Kernentitäten, die es ableitet. Es emittiert keine eigenen Events und exponiert
keine Aktuierungsoberfläche — Discovery ist naturgemäß beobachten-und-
katalogisieren. Die Referenzen, die es persistiert, kommen **bereits bereinigt**
von den Connectoren an; das Modul speichert sie wortgetreu und fügt keine eigenen
Rohdetails hinzu, sodass die Minimal-Data-Eigenschaft eine Eigenschaft der
Leitung ist, durchgängig gewahrt.

:::caution[Ehrliche Grenzen]
- **Das Inventar besitzt nicht den Access-Graphen.** Seit Entscheidung A
  (2026-06-03) ist Modul III (die Access Map) der **alleinige Schreiber** der
  Read/Write-`AccessEdge` und der einzige Eigentümer der Topologie und des
  Permitted-vs-Observed-Diffs. Das Inventar entdeckt und katalogisiert die
  *Entitäten*, die ein Edge benennt; es erfasst den Edge selbst nicht mehr und
  bedient keine Topologie-Route. Der Graph wird nur befüllt, wenn Modul III beim
  Boot verdrahtet ist.
- **Discovery ist nur so vollständig wie die Signale.** Eine Entität existiert
  im Katalog nur, wenn ein Connector sie beobachtet hat. Das Fehlen im Katalog
  ist **kein** Beweis für das Fehlen im Estate, wo die Abdeckung partiell ist.
- **Lebendigkeit ist Staleness, nicht Gesundheit.** `stale` bedeutet „kürzlich
  nicht gesehen“, nicht mehr; die Stille einer Session ist normal, und formale
  Gesundheit/SLA gehören zu Modul XXII. Der Sweep mutiert nie den eigenen
  Lebenszyklus der Kernentität.
- **Keine erfundenen Details.** Das Modul speichert nur Identifikatoren,
  Beziehungen und Lebendigkeitszähler — niemals Payloads, Secrets, PII, Befehle,
  Queries oder URLs.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul I einzuordnen ist und die ehrliche Actuate-Aufteilung.
- [Modul III — die Access Map](/de/reference/modules/iii-access-map/) — der alleinige Eigentümer des R/RW-Graphen und der Drift.
- [Event-Bus-Referenz](/de/reference/events/) — die Events `edge.observed`, `cost.sampled` und `finding.reported`, die es konsumiert.
- [Von Null zum Graphen](/de/tutorials/zero-to-graph/) — den Katalog und die Map auf dem Demo-Estate befüllen.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine, die Schichten und der Bus.
