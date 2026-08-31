---
title: "Modul VIII — Daten, Wissen & Kontext"
description: >-
  Die gesteuerte Datenebene für das, was Agenten wissen und nutzen:
  Wissensdatenbanken und semantisches RAG über einen austauschbaren Vektorindex,
  Retrieval gesteuert nach Identität/Klassifizierung/Residency und append-only
  Lineage, die belegt, welche Übergänge der Operator konfiguriert hat, und alle
  anderen verweigert.
---

Modul VIII ist die **gesteuerte Datenebene**: Es baut Wissensdatenbanken auf und führt
**semantisches RAG** über einen austauschbaren Vektorindex aus, steuert jeden Retrieval
nach Identität, Klassifizierung und Residency und protokolliert **append-only Lineage**
darüber, was den Perimeter überschritt und was das Residency-Gate verweigerte. Damit wird
eine Residency-Aussage belegt statt nur behauptet. Es hält außerdem
die versionierte Prompt-Registry, das gesteuerte Agentengedächtnis und die
Kontext-/Compaction-Policies als Daten vor — nicht als Versprechen.

## Was es ist

Das Modul **orchestriert** die Datenebene; es implementiert seine Nachbarn nicht neu. Es
zieht Inhalte aus **read-only Datenkonnektoren**, führt jeden Body, jede Prompt-Vorlage und
jeden Gedächtniseintrag durch seine **eigene Maskierungsroutine, bevor** irgendetwas gechunkt,
embedded, gehasht oder gespeichert wird, und steuert dann den Retrieval gegen die Grants,
die das Identitätsmodul deklariert. Das Embedding wird an eine Modell-Naht delegiert — das
Modul ruft niemals direkt einen Provider auf — und das Ranking wird an eine
Vektorindex-Naht delegiert, sodass der Governance-Vertrag identisch ist, ob der Retrieval
in-process oder gegen ein externes ANN-Backend läuft.

Die **rote Linie** ist nicht verhandelbar: Das Produkt steuert die Daten des Kunden und
verkauft oder exfiltriert sie niemals. Daten überschreiten den Perimeter nur an einer vom
Operator bereitgestellten Übergangsstelle — einem externen Embedding-Anbieter oder einer
SIEM-/Webhook-Ausgabe. Für jedes andere Ziel arbeitet das Residency-Gate deny-closed. Drei
Mechanismen halten das im Design fest — Maskierung vor der Indizierung, das Egress-Gate und
Lineage, die belegt, welche Übergänge stattgefunden haben.

## Vertrag & Entitäten

Modul VIII deklariert **acht tenant-scoped Entitäten** im gemeinsamen Datenmodell: die
Wissensdatenbank, das Dokument (Metadaten und Provenance, niemals der Body), den Chunk
(bereinigter Text plus eine vererbte Klassifizierung und ACL), den Prompt und seine
**append-only** unveränderlichen Revisionen, das gesteuerte Agentengedächtnis, die
Kontext-/Compaction-Policy und die **append-only** Lineage-Zeile. Seine Routen werden unter
dem eigenen Namespace des Moduls eingehängt, umhüllt mit Authentifizierung, Tenant-Scoping
und Autorisierung; das Lesen von Wissen und Lineage ist eine **privilegierte, auditierte**
Aktion.

Retrieval ist der Sicherheitsvertrag, und **die Reihenfolge ist der Vertrag**: die Grants
der Identität auflösen (fail-closed — ein Guard-Fehler verweigert, niemals ein
herabgestuftes Allow), das Residency-Gate anwenden, die Query embedden, dann **Kandidaten
nach Klassifizierung und ACL filtern, bevor gerankt wird**, sodass ein Chunk, den die
Identität nicht sehen kann, niemals in das gerankte Set gelangt, dann ranken, dann die
unveränderliche Lineage-Zeile anhängen. Das **Egress-Gate** ist darüber komponiert: Eine
residency-gesperrte Wissensdatenbank verweigert Ingest oder Retrieval mit einem Embedder,
der Egress verursachen würde, durchgesetzt bei Create, Update, Ingest und Retrieval (Defence
in Depth). Dokumentinhalte reisen per Design über einen typisierten Konnektorvertrag,
**nicht** über den Event-Bus — Massen-Referenzdaten dürfen nicht gebroadcastet werden.

## Auf dem Event-Bus

Modul VIII **erzeugt** [`finding.reported`](/de/reference/events/)-Events: ein gehashter
`FindingReport` pro Ingest, wenn ein Secret oder PII maskiert wird, und ein Finding, wenn
ein Residency- oder Egress-Gate verweigert — nur gehashtes Detail, niemals das Secret oder
der Body. Forensik und Compliance konsumieren die Lineage und diese Findings. Es
**konsumiert** für Inhalte nichts vom Bus: Per Design reisen Inhalte über einen typisierten
Pull-Vertrag, sodass Minimal-Data eine Eigenschaft der Leitung ist, kein im Nachhinein
angewandter Laufzeitfilter.

:::caution[Ehrliche Grenzen]
- **Semantische Qualität hängt von einem konfigurierten Embedder ab.** Der Standard-Embedder
  ist **lokal und zero-egress**, aber **nicht-semantisch** (ein deterministischer
  Feature-Hash-Fallback). Die Wissensdatenbank protokolliert ihr Embed-Modell, sodass der
  Fallback niemals mit semantischer Qualität verwechselt wird, und das Binary warnt einmal,
  wenn es herabgestuft läuft. Ein modellgestützter Embedder wird vom Operator konfiguriert
  (`OLIVARES_EMBEDDINGS_*`); setzen Sie `OLIVARES_EMBEDDINGS_REQUIRE=1`, und der Boot
  **verweigert den Start**, statt lexikalische Vektoren so zu liefern, als wären sie
  semantisch.
- **Residency ist ein fail-closed Egress-Gate, keine Inferenz-Einstellung.** Die Wahl einer
  Inferenzregion erfüllt für sich genommen keine residency-gesperrte Wissensdatenbank — der
  Embedder muss nachweislich in-region sein, sonst werden Ingest und Retrieval verweigert.
  Eine Identität ohne Clearance oder ohne Region normalisiert auf public / no-region, niemals
  auf einen weiteren Grant.
- **Standard-Ranking ist exakt und in-process** (ein linearer Scan, geeignet für einen
  self-hosted oder air-gapped Knoten bis zu rund 10⁵ Chunks pro Tenant). Ein externes
  ANN-Backend wird hinter der Vektorindex-Naht eingebunden, um zu skalieren; ein
  konfiguriertes-aber-ausgefallenes Backend **verweigert die Anfrage**, fällt niemals
  stillschweigend auf andere Ergebnisse zurück.
- **Live-Transport der Konnektoren ist ein dokumentiertes Follow-up.** Konnektoren parsen
  heute das nativ exportierte Format mit Fixtures hinter einer stabilen Schnittstelle; ohne
  konfigurierten Export ist eine Quelle schlicht leer. Ingest ist synchron;
  großvolumiger asynchroner Ingest ist ein Follow-up.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul VIII sitzt und sein ehrlicher Actuate-Status.
- [Referenz Event-Bus](/de/reference/events/) — das `finding.reported`-Event und seine Payload.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine, die Nähte und die Schichten.
- [Eine Quelle anbinden](/de/how-to/connect-a-source/) — Registrieren eines read-only Datenkonnektors.
- [Air-Gap-Installation](/de/how-to/air-gap-install/) — Betrieb der Datenebene mit Zero-Egress.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — der produktweite ehrliche Vertrag.
