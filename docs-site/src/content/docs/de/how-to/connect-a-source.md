---
title: "Eine Quelle verbinden"
description: "Eine echte Beobachtungsquelle ins Control Plane verkabeln, das Connector-Modell verstehen und das richtige Signal pro System wählen."
---

Diese Seite erklärt das allgemeine Connector-Modell und wie man eine echte Quelle in die Engine verkabelt. Wenn du nur einen Coding-Agenten verbinden willst, beginne mit [Claude Code verbinden](/de/how-to/connect-claude-code/) — das ist eine spezifische Quelle auf dem kooperativen Pfad, und diese Seite ist das Modell darunter.

## Das Connector-Modell

Eine Quelle hat genau eine Aufgabe: Sie **beobachtet** ein externes System und **emittiert normalisierte Beobachtungen**. Sie sitzt niemals im Datenpfad, proxyt niemals Traffic und liest niemals Payloads. Die R/RW-Access-Map wird aus dem aufgebaut, was die Quelle meldet, nicht aus dem Abfangen dessen, was fließt.

Konkret implementiert eine Quelle ein kleines Interface — `Open` (einmal konfigurieren), `Gather` (laufen, emittierend), `Close` (freigeben) — und während `Gather` übergibt sie der Engine durch einen Sink jeweils eine Beobachtung. Die Engine besitzt das Scheduling: Eine Streaming-Quelle (ein Log-Tail, ein Receiver) blockiert in `Gather` und emittiert, bis sie abgebrochen wird; eine Batch-Quelle erledigt ihre Arbeit und kehrt zurück, und die Engine entscheidet, wann sie wieder laufen soll. Der Connector besitzt niemals seinen eigenen Timer.

Es gibt genau drei Arten von Beobachtung, die eine Quelle emittieren kann:

| Beobachtung | Was sie trägt | Verwendet von |
|---|---|---|
| `edge` | Ein Ursprung (Agent / Identität / Sitzung) hat eine Ressource berührt, mit einem Read/Write-Modus | Die R/RW-Access-Map |
| `cost` | Modell-/Provider-Nutzungskosten | FinOps |
| `finding` | Ein Guardrail-/Red-Team-/Forensik-Finding | Security |

Die Menge ist absichtlich geschlossen — ein Dritter kann keine neue Beobachtungsart einführen. Die Engine **hebt** jede emittierte Beobachtung auf den In-Process-Event-Bus, wo Module sie konsumieren, ohne an die Quelle gekoppelt zu sein, die sie erzeugt hat. Speziell für die Access Map löst die Engine die String-Referenzen des Connectors zu Entitäten auf und merged die Beobachtung in eine persistierte Access-Edge.

:::note[Minimale Daten, per Vertrag]
Eine Edge-Beobachtung trägt nur Identifier und eine Read/Write-Klassifikation — niemals SQL-Bodies, Request-Payloads, Secrets oder PII. Ein Finding trägt einen Hash jedes sensitiven Details, niemals das Detail selbst. Das ist eine Eigenschaft des Wire-Vokabulars, das der Connector spricht, keine Konfigurationsoption, die du abschalten kannst. Siehe die [Architektur-Übersicht](/de/explanation/architecture/overview/) dafür, wo das im Read-First-Design sitzt.
:::

### Connectoren sind Apache-2.0 und importieren niemals den Core

Ein Connector importiert das Connector-SDK und sonst nichts aus dem Produkt. Er importiert niemals `/core` (die AGPL-Engine). Diese Grenze wird in CI durchgesetzt, und sie ist es, die Connectoren erlaubt, unter Apache-2.0 auszuliefern, und Dritten erlaubt, ihre eigenen ohne Copyleft-Reibung zu bauen. Dasselbe Connector-Binary läuft in-process oder out-of-process über gRPC identisch. Siehe [Open Core und Lizenzierung](/de/explanation/open-core-and-licensing/) für die vollständige Grenze.

## Provenienz und Konfidenz: warum die Quelle zählt

Jede Edge erfasst, **welche Quelle sie erzeugt hat**, und ein **Konfidenz**-Niveau, und das Produkt zeigt beides, statt sie zu kollabieren. Ein `pg_audit`-READ und ein `mcp_annotation`-Hinweis sind nicht dieselbe Evidenz und werden niemals als dieselbe behandelt.

Die zwei Konfidenz-Niveaus sind ehrlich, nicht kosmetisch:

- **`attributed`** — der Zugriff ist fest an seinen Ursprung gebunden (zum Beispiel eine Per-Agent-Identität, die im Audit-Trail vorhanden ist).
- **`approximate`** — die Attribution ist abgeleitet oder verlustbehaftet (ein geteilter Service-Account oder ein Store, dessen Audit die Aufrufer nicht sauber trennen kann).

Der Access-Modus ist einer von `unknown`, `read`, `write`, `readwrite`. `unknown` ist explizit und wird niemals geraten — das Produkt zeigt lieber "wir konnten das nicht klassifizieren", als ein Read/Write-Label zu fabrizieren.

## Kategorien von First-Party-Quellen, nach Signal

First-Party-Quellen unterscheiden sich durch das **Signal**, das sie tragen. Wähle die Quelle nach dem, was das System, das du beobachtest, dir ehrlich sagen kann.

### `pg_audit` — PostgreSQL READ/WRITE

Die pgAudit-Quelle tailt PostgreSQLs eigenes strukturiertes Audit-Log und emittiert eine Edge pro auditiertem Datenzugriff. Der Read/Write-Modus wird **wortwörtlich aus dem CLASS-Feld von pgAudit** übernommen (READ, WRITE, DDL) — niemals aus dem SQL-Text abgeleitet. Der Ursprung ist die Rolle oder der `application_name`, dem das Log den Zugriff zuschreibt. Der Connector ist nur-lesend über die Log-Datei; er verbindet sich niemals mit der Datenbank und schreibt niemals in sie. Das ist die saubere Stufe: ein Objekt-/Relational-Store, der Zugriff in seinem nativen Trail klassifiziert.

### `cloudtrail` — AWS S3 readOnly

Die CloudTrail-Quelle liest CloudTrail-Log-Dateien und emittiert eine Edge pro S3-Event. Der Read/Write-Modus wird **wortwörtlich aus dem `readOnly`-Feld von CloudTrail** übernommen, niemals abgeleitet. Der Ursprung ist das IAM-Principal, dem CloudTrail den Aufruf zuschreibt. Eine über viele Aufrufer geteilte Assumed Role wird bewusst als `approximate` markiert, weil der Trail die echten Aufrufer dahinter nicht trennen kann.

### `otel` — kooperative Agenten

Das ist der kooperative Pfad: Ein Agent, der OpenTelemetry-Tool-Telemetrie emittiert, meldet, was er getan hat, und die Engine ingestiert sie. Claude Code ist hier die kanonische First-Party-Quelle, die OTLP-Telemetrie mit MCP-Introspektion kombiniert — siehe [Claude Code verbinden](/de/how-to/connect-claude-code/). Kooperative Telemetrie ist, wenn vorhanden, das Signal mit der höchsten Fidelity, aber sie hängt davon ab, dass der Agent kooperativ ist, weshalb ein Kernel-Backstop existiert.

### `ebpf` — Tetragon-Kernel-Backstop (nicht-kooperativer Pfad)

Die eBPF-Quelle ist die Anti-Umgehungs-Hälfte der Map: Wo der kooperative Pfad sieht, was ein Agent *meldet*, sieht diese, was der Kernel tatsächlich getan hat — Datei-Reads/-Writes und Netzwerkverbindungen — selbst wenn ein Agent seine eigene Telemetrie deaktiviert. Sie läuft **außerhalb der Kontrolle des Agenten**.

Zwei ehrliche Einschränkungen definieren sie:

- Sie lädt **nicht** selbst eBPF-Programme. Die Kernel-Erfassung wird von Tetragon erledigt, das als separater gehärteter Dienst deployt wird; diese Quelle ist ein nur-lesender Konsument des Event-Streams von Tetragon und benötigt keine eigenen Kernel-Capabilities.
- Sie ist **blind für den TLS-Body**. Sie beobachtet Zugriffsbeziehungen, niemals Payloads.

Ihre Edges sind immer `approximate`, aus einem spezifischen Grund: Der Kernel schreibt einen Zugriff einem Prozess oder Container zu — einer Runtime-Identität — nicht einem aufgelösten Agenten. Der Zugriff selbst ist Ground Truth (der Syscall ist passiert); die Konfidenz qualifiziert die *Attribution*, die das Access-Map-Modul aufwertet, sobald es die Identität an einen Agenten bindet.

:::caution[Das Kernel-Backstop ist in seiner nicht-kooperativen Tiefe im Designstadium]
Der kooperative Pfad (Store-natives Audit, OTEL) ist der verifizierte Fall mit hoher Fidelity. Das Kernel-Backstop ist im Design solide, aber seine durchgängige Attribution ist der Teil, der noch nachgewiesen wird. Behandle es als ein Backstop, das den Boden anhebt, nicht als eine fertige Primärquelle. Siehe [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/).
:::

### `mcp_annotation` — nicht vertrauenswürdig

Die MCP-Introspektionsquelle listet die Tools, Ressourcen und Prompts eines Servers auf und leitet aus dem `readOnlyHint` / `destructiveHint` jedes Tools einen Read/Write-*Hinweis* ab. Gemäß der MCP-Spezifikation **MUSS** ein Client diese Annotationen als nicht vertrauenswürdig betrachten, sofern der Server selbst nicht vertrauenswürdig ist, und die Defaults sind asymmetrisch. Dieses Signal ist also ein **deklarierter Capability-Hinweis, niemals ein beobachteter Zugriff**: Jede solche Edge ist `approximate` und wird weder als beobachtet noch als erlaubt markiert. Es liefert die *Capability-Oberfläche*, gegen die man diffen kann — nicht die Evidenz, dass tatsächlich etwas getan wurde. Es muss durch eine beobachtete Quelle bestätigt werden, niemals allein vertraut.

## Die harte Abhängigkeit: Per-Agent-Identität

Attribution ist nur so gut wie die Identität, die das zugrundeliegende System erfasst. Natives Audit schreibt einen Zugriff einem **Credential oder einer Rolle** zu, nicht einem Agenten. Wenn viele Agenten einen Service-Account oder einen Connection-Pool teilen, kollabiert jeder beobachtete Zugriff auf diese eine Identität, und die Attribution wird `approximate` — das Produkt sagt das, statt vorzugeben, die Agenten auseinanderhalten zu können.

Um `attributed`-Edges zu erhalten, gib jedem Agenten seine eigene Identität. Das ist die Brücke zur Governance: das Ausstellen oder Durchsetzen einer Per-Agent-Identität ist es, was die Access Map scharf macht.

:::tip[Wenn die Attribution grob aussieht, prüfe zuerst die Identität]
Bevor du den Connector verdächtigst, prüfe, ob die Agenten ein Credential teilen. Ein geteilter Service-Account ist der häufigste Grund, warum ein sauberer Store dennoch `approximate`-Edges liefert.
:::

## Gestufte Abdeckung — sei realistisch

Die Abdeckung ist gestuft nach dem, was die Audit-Oberfläche eines Systems ehrlich unterstützen kann:

- **Sauber** — SQL-Datenbanken, Objekt-Stores und Warehouses, die Zugriff nativ klassifizieren (Postgres, S3 und Verwandte). Read/Write wird wortwörtlich übernommen.
- **Verlustbehaftet** — Stores, deren Audit Read nicht sauber von Write oder Aufrufer nicht von Aufrufer trennen kann (Dokument- und Vector-Stores). Edges landen, aber oft `approximate`.
- **Passiv unmöglich** — Systeme ohne nutzbare passive Audit-Oberfläche (In-Memory-Caches, eingebettete Single-File-Datenbanken). Es gibt kein ehrliches Read-First-Signal zu erfassen; das Produkt gibt nichts anderes vor.

Wähle die Stufe bewusst. Ein Store der sauberen Stufe mit Per-Agent-Identität ist dort, wo die Map am schärfsten ist.

## Eine echte Quelle verkabeln

Echte (Nicht-Demo-)Quellen werden aus einer einzigen Operator-Konfigurationsdatei verkabelt, benannt durch die Umgebungsvariable `OLIVARES_SOURCES_CONFIG`, gelesen **bevor die Engine startet**. Die Konfiguration ist ein JSON-Dokument; Secrets leben in dieser Datei (per Wert referenziert) und werden niemals von der Engine persistiert.

Das Dokument deklariert eine Liste von Quellen. Jeder Quelleneintrag wählt einen Connector per Kind, benennt den Tenant, zu dem seine Beobachtungen gehören, und trägt die eigenen Einstellungen des Connectors. Die allgemeine Form ist:

```json
{
  "sources": [
    {
      "name": "prod-postgres",
      "kind": "pgaudit",
      "tenant": "acme",
      "config": {
        "...": "connector-specific settings"
      }
    }
  ]
}
```

Die Felder oberhalb des Per-Connector-`config`-Blocks — ein Quellenname, der Connector-`kind`, der besitzende `tenant` und ein optionales Poll-Intervall für Batch-Quellen — sind der stabile Verkabelungsvertrag.

:::caution[Per-Connector-Config-Schlüssel werden hier absichtlich generisch beschrieben]
Die genauen Schlüssel innerhalb des `config`-Blocks jedes Connectors (Log-Pfade, Endpoints, Credential-Referenzen) gehören jedem Connector und werden hier nicht reproduziert, weil das Veröffentlichen eines unverifizierten Schlüssels schlimmer wäre als das Weglassen. Lies die eigene Dokumentation des Connectors für seine Einstellungen, oder beschreibe es generisch, bis du die Schlüssel gegen den Connector verifiziert hast, den du deployst. Kopiere kein Schema, das du nicht verifiziert hast. Siehe [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/).
:::

### Eine unkonfigurierte Quelle warnt ehrlich

Die Engine schlägt sicher fehl, nicht laut, wenn nichts verkabelt ist:

- Wenn `OLIVARES_SOURCES_CONFIG` **nicht gesetzt** ist, startet die Engine ohne Quellen.
- Wenn die Datei **fehlt, nicht lesbar oder kein gültiges JSON** ist, **warnt die Engine und fährt fort** ohne Quellen — sie stürzt beim Boot nicht ab.
- Wenn die Quellenliste **leer** ist, warnt die Engine, dass kein Connector ingestieren wird und dass das Estate auf keinem Live-Traffic läuft.

In jedem Fall sagt das Boot-Log dir klar, dass nichts Echtes verkabelt ist, statt mit einer leeren Map stillschweigend gesund zu wirken. Eine ehrliche Warnung ist das Design: Eine leere Access Map sollte niemals wie eine saubere aussehen.

## Wo das läuft

Das Data Plane — die Collectoren, die diese Quellen ausführen — **läuft immer auf Kundeninfrastruktur**, ob das Control Plane ein einzelnes self-hosted Binary, ein verteiltes Deployment oder air-gapped ist. Die Quelle beobachtet lokal und die Engine ingestiert. Es gibt keine verpflichtende Telemetrie und standardmäßig keinen Egress der Control Plane. Deinen Perimeter überschreitet nur, was **du** dafür konfigurierst: Aufrufe an deine Modell-APIs, die von dir eingerichteten SIEM-/Webhook-Ausgaben und ein externer Embedding-Anbieter, falls du einen bereitstellst. Siehe [Self-Hosting](/de/how-to/self-hosting/) und [Air-Gap-Installation](/de/how-to/air-gap-install/) für die Deployment-Topologien.

## Verwandt

- [Claude Code verbinden](/de/how-to/connect-claude-code/) — der kooperative `otel`-Pfad, durchgängig.
- [Module-Übersicht](/de/reference/modules/overview/) — die Module, die diese Beobachtungen konsumieren (Inventory, die R/RW-Access-Map, FinOps, Security).
- [Architektur-Übersicht](/de/explanation/architecture/overview/) — wo das Connector-SDK, der Event-Bus und die Access Map im Design sitzen.
