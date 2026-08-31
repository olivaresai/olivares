---
title: "Claude Code verbinden (der kooperative Pfad)"
description: "Den OpenTelemetry-Exporter von Claude Code auf die Engine richten und als Quelle verkabeln, sodass seine Tool-Telemetrie — plus nicht vertrauenswürdige MCP-Introspektion — die R/RW-Access-Map speist."
---

Claude Code ist die **kanonische kooperative Quelle** für Olivares AI. Es emittiert
OpenTelemetry-(OTLP-)Telemetrie über die Tools, die es ausführt, und die MCP-Server, mit denen es
spricht, exponieren Introspektionshinweise (`readOnlyHint` / `destructiveHint`) darüber,
ob ein Tool liest oder schreibt. Zusammen speisen diese **Modul III — die R/RW-Access-
Map** mit hochgenauen, agentenattribuierten Edges, der kooperativen Hälfte des
Permitted-vs-Observed-Bildes.

Diese Seite verkabelt diesen Pfad: den OTLP-Exporter von Claude Code auf den
Receiver der Engine richten, dann die Quelle deklarieren, sodass ihre Telemetrie zu Access-Edges wird. Für den
allgemeinen Quellenverkabelungsmechanismus und wo das hineinpasst, siehe
[Eine Quelle verbinden](/de/how-to/connect-a-source/) und die
[Architektur-Übersicht](/de/explanation/architecture/overview/). Für die Form der
normalisierten Events, die das erzeugt, siehe die [Events-Referenz](/de/reference/events/).

:::note[Kooperativ, nicht autoritativ]
Der kooperative Pfad ist **hochgenau, aber Trust-gestuft**. OTLP-Tool-Telemetrie wird
einer konkreten Agentensitzung attribuiert; MCP-Annotationen sind ein nützliches R/RW-*Signal*,
aber sind **gemäß MCP-Spec nicht vertrauenswürdig** und werden bestätigt, niemals allein vertraut
(siehe [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/)). Für Aktivität außerhalb der
Kooperation des Agenten — oder um einen Agenten zu erwischen, der aufhört zu emittieren — paare dies mit einem
nicht-kooperativen Backstop (Kernel/eBPF) und Store-nativem Audit (pgAudit,
CloudTrail). Diese Seite behandelt nur die kooperative Quelle.
:::

## Was du aus dieser Quelle bekommst

Einmal verkabelt, wird die Telemetrie von Claude Code in das Datenmodell der Engine normalisiert und
an Modul III gespeist:

| Output | Provenienz | Anmerkungen |
|---|---|---|
| **Access-Edge** `agent session → resource (read/write)` | Signalquelle `otel` | Konfidenz `attributed` — der Ursprung ist eine konkrete Sitzung, kein geteilter Service-Account |
| **MCP-Server-Edge** `session → MCP server` | Signalquelle `otel` | Modus `unknown` (eine Verbindung ist selbst kein Zugriff; das ist Topologie/Inventar) |
| **R/RW-Hinweis aus MCP-Introspektion** | Signalquelle `mcp_annotation` | **nicht vertrauenswürdig** — ein bestätigendes Signal, niemals eine Edge für sich |
| **Cost Sample** (Per-Request-Modellnutzung) | die api-request-Telemetrie | speist FinOps, nicht die Access Map |
| **Finding** (Anti-Umgehung) | Telemetrie-Lücken / abgelehnte Tools | eine Sitzung, die aufhört zu emittieren, während sie noch aktiv ist, wird geflaggt |

Der Connector ist **read-first und minimal-data**: Er erfasst die *Beziehung*
(welche Sitzung welche Ressource berührt hat, lesend oder schreibend), niemals das Payload. Eine rohe
Tool-Eingabe oder ein Shell-Befehl — der ein Secret oder PII tragen kann — wird auf eine
bereinigte Resource-Referenz reduziert, bevor er überhaupt zur Beobachtung wird. Diese Haltung ist
der Default; das Behalten von Content ist ein expliziter, kategoriebegrenzter Opt-in.

## Wie die Verkabelung funktioniert

Es gibt zwei Hälften, und sie treffen sich an einem Loopback-Socket auf dem Host, wo Claude
Code läuft.

1. **Die Engine exponiert einen OTLP-Receiver als Core-Ingest.** Der kooperative
   Connector betreibt einen OTLP-Receiver (gRPC und HTTP) für die eigene
   OpenTelemetry-Ausgabe von Claude Code, plus einen Endpoint für seine Tool-Hooks. Er **bindet
   standardmäßig an Loopback** — die kooperative Ingestion ist unauthentifiziert, daher darf sie
   nicht off-host erreichbar sein. Halte ihn auf Loopback; das Off-Host-Backstop ist der
   Kernel-Collector, kein öffentlicher OTLP-Port.
2. **Du richtest den OTLP-Exporter von Claude Code auf diesen Receiver** und **deklarierst
   die Quelle**, sodass die Engine weiß, sie für deinen Tenant auszuführen.

```
  Claude Code (agent host)                 Olivares AI engine
  ┌──────────────────────────┐             ┌─────────────────────────────┐
  │ OTLP exporter            │── loopback ─▶│ cooperative OTLP receiver   │
  │ (OTEL_* env on the CLI)  │   (4317/4318)│ → normalize → access edges  │
  │ MCP servers (R/RW hints) │             │ → module III (R/RW map)     │
  └──────────────────────────┘             └─────────────────────────────┘
```

:::caution[Der Receiver ist standardmäßig unauthentifiziert und loopback-only]
Weil die kooperative Ingestion Telemetrie akzeptiert, ohne den Sender zu authentifizieren,
kann jeder, der den Socket erreichen kann, Edges fälschen. Der Receiver bindet standardmäßig
genau aus diesem Grund an Loopback. Ihn an eine Nicht-Loopback-Adresse zu binden, ist ein
gefährliches, explizites Opt-in; exponiere ihn nicht in einem geteilten Netzwerk. Off-Host-Agenten
sollten stattdessen mit dem nicht-kooperativen Backstop beobachtet werden.
:::

## Schritt 1 — Claude Code auf den Receiver richten

Claude Code wird über seine eigenen OpenTelemetry-Umgebungsvariablen konfiguriert. Auf
dem Agent-Host aktiviere seinen OTLP-Export und richte ihn auf den Loopback-Receiver
der Engine. Der Receiver der Engine folgt den Standard-OpenTelemetry-Ports (gRPC und
HTTP); setze den Exporter-Endpoint von Claude Code auf die passende Loopback-Adresse und
das passende Protokoll.

:::note[Exakte OTEL-Variablennamen gehören Claude Code, nicht diesem Produkt]
Der Exporter wird mit den eigenen Einstellungen von Claude Code / OpenTelemetry konfiguriert
(Telemetrie aktivieren, das OTLP-Protokoll wählen, den Endpoint setzen). Diese Namen werden
von Claude Code und dem OTel-SDK definiert — konsultiere die Telemetrie-Dokumentation von Claude
Code für die aktuellen Variablennamen, statt hier eine Liste zu kopieren. Was
dieses Produkt besitzt, ist der **Receiver**, auf den sie zeigen, und die **Quellendeklaration**
unten.
:::

Standardmäßig behält der Connector nur **strukturelle** Telemetrie — Sitzungs- und
Identitätsattribute, Tool-Namen, R/RW-Modus, Timing — und niemals Prompt-Text, Tool-
Bodies oder rohe API-Bodies, selbst wenn Claude Code so konfiguriert ist, sie zu emittieren. Lass es
so, sofern du keinen spezifischen, auditierten Grund hast, eine Content-Kategorie zu behalten.

## Schritt 2 — Die Quelle deklarieren

Echte (Nicht-Demo-)Quellen werden aus einer einzigen operatorbesitzten Konfigurationsdatei verkabelt,
benannt durch die Umgebungsvariable `OLIVARES_SOURCES_CONFIG`, die die Engine
**bevor sie startet** liest. Secrets leben per Wert in dieser Operator-Datei, niemals im
Store. Jeder Eintrag benennt die Quelle, ihren `kind`, den Tenant, zu dem sie gehört, und einen
Per-Source-`config`-Block:

```json
{
  "sources": [
    {
      "name": "claude",
      "kind": "claude",
      "tenant": "<tenant-ref>",
      "config": {
        "grpc_addr": "127.0.0.1:4317"
      }
    }
  ]
}
```

- **`name`** ist dein Label für diese Quelleninstanz.
- **`kind`** wählt den kooperativen Claude-Code-Connector.
- **`tenant`** begrenzt jede Edge, die er erzeugt, auf einen Tenant (Modul-III-Reads sind
  tenant-begrenzt und privilegiert).
- **`config`** hält die eigenen Einstellungen des Connectors — zum Beispiel die Loopback-
  Adresse, an die der OTLP-Receiver bindet. Der Connector bindet seinen Receiver selbst, statt
  den des Agenten zu borgen, sodass das Deaktivieren einer Claude-Code-OTEL-Variable den Collector nicht
  stillschweigend abschalten kann.

:::caution[Bestätige die Config-Schlüssel des Connectors gegen den ausgelieferten Descriptor]
Der Connector veröffentlicht sein eigenes Konfigurationsschema (sein Descriptor listet jeden
Schlüssel, Typ, Default und jede Beschreibung). Der `config`-Block oben zeigt den
repräsentativen Receiver-Adress-Schlüssel; **erfinde keine zusätzlichen Schlüssel** von dieser
Seite. Lies den Descriptor, den der Connector meldet — oder
[die Konfigurationsreferenz](/de/reference/configuration/) — für die autoritative,
versionierte Liste (Receiver-Adressen, der Hook-Pfad, Korrelations-/Silence-Fenster,
die Content-Capture-Allowlist und die Opt-in-Governance-Felder). Ein Wert nach dem
anderen, verifiziert gegen das, was dein Build tatsächlich ausliefert.
:::

Eine **unkonfigurierte oder leere Quelle warnt ehrlich**, statt fehlzuschlagen: ein `kind`,
der unbekannt, nicht eingebettet ist oder nicht geladen werden kann, wird beim Start gemeldet, niemals
stillschweigend zu einem No-op verworfen. Nach dem Bearbeiten der Datei starte die Engine neu, sodass der
Composition Root sie erneut liest.

## Schritt 3 — Verifizieren, dass Edges ankommen

Während Claude Code exportiert und die Quelle deklariert ist, führe eine Claude-Code-Sitzung aus, die
eine Ressource berührt (eine Datei lesen, einen Befehl ausführen, ein MCP-Tool aufrufen), und schau dann auf die
Access Map. Das Betrachten des Access Graph ist eine **privilegierte, tenant-begrenzte, auditierte
Aktion** (Editor-Rolle und höher — niemals der niedrigste Viewer), also verwende ein Token mit der
richtigen Rolle:

- Der Access Graph wird auf der Modul-Route `/v1/m/accessmap/graph` ausgeliefert.
- Das Permitted-vs-Observed-Ergebnis — der Least-Privilege-**Drift** — ist unter
  `/v1/m/accessmap/drift`.

Diese Modul-Routen sind erreichbar, sind aber bewusst **nicht** im ausgelieferten
OpenAPI-Dokument; ihre Verträge leben in den typisierten Go/TS-Interfaces des Produkts.
Für den durchgängigen Durchlauf von einer frischen Engine zu einem befüllten Graph folge dem
[Zero-to-Graph-Tutorial](/de/tutorials/zero-to-graph/).

Du solltest Edges sehen, deren Signalquelle `otel` ist, attribuiert der Claude-Code-
Sitzung. Wenn die MCP-Introspektion einen R/RW-Hinweis beigetragen hat, kommt der als separates
`mcp_annotation`-Signal an, das den Modus der Edge bestätigt — aber nicht für sich allein
etabliert.

## Ehrliche Grenzen dieses Pfads

- **MCP-Annotationen sind nicht vertrauenswürdig.** `readOnlyHint` / `destructiveHint` sind
  beratende Hinweise, die ein Server über sich selbst deklariert; die MCP-Spec sagt, Clients müssen
  sie als nicht vertrauenswürdig behandeln. Das Produkt zeigt sie als bestätigendes Signal und
  zeigt die Konfidenz ehrlich — es wertet eine Edge niemals allein auf einen Hinweis hin zu "read-only"
  auf.
- **Attribution hängt von Per-Agent-Identität ab.** Edges werden einer Sitzungsidentität
  attribuiert. Ein Pool von Agenten, die einen Service-Account teilen, kollabiert die Attribution;
  das zu lösen ist ein Governance-Anliegen (Per-Agent-Identität ausstellen und durchsetzen),
  nicht etwas, das dieser Connector fabrizieren kann.
- **Es ist kooperativ.** Es sieht, was der Agent meldet. Ein Agent, der nie emittiert,
  oder Aktivität, die abseits des Pfads des Agenten passiert, ist für diese Quelle per
  Konstruktion unsichtbar — was genau der Grund ist, warum das nicht-kooperative Kernel-Backstop und
  Store-natives Audit daneben existieren.
- **Design-Stadium-Tiefe.** Vieles an der Plattform ist Pre-1.0. Behandle die Fähigkeiten hier
  als den verifizierten kooperativen Ingest-Pfad; wo ein nachgelagertes Modul oder Feld
  noch nicht gebaut ist, sagt das Produkt das, statt Abdeckung zu implizieren.

## Nächste Schritte

- [Eine Quelle verbinden](/de/how-to/connect-a-source/) — das allgemeine Quellenverkabelungsmodell
  (kooperativ und nicht-kooperativ).
- [Governance und Freigabe](/de/how-to/govern-and-approve/) — beobachteten Drift in eine
  Least-Privilege-Entscheidung verwandeln.
- [Events-Referenz](/de/reference/events/) — die normalisierten Beobachtungen, die diese Quelle
  emittiert.
- [Architektur-Übersicht](/de/explanation/architecture/overview/) — wo der
  kooperative Pfad in der Plattform sitzt.
