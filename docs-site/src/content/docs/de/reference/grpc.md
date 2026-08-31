---
title: gRPC-Referenz — Services, Methoden und Nachrichtentypen
description: >-
  Jeder von Olivares-AI-Engine und Plugin-Host registrierte rpc, mit Streaming-Form,
  Request- und Response-Nachrichten sowie dem vollständigen Methodenstring.
  Aus den eigenen Registrierungstabellen der Server generiert.
---

Olivares AI spricht an zwei Stellen gRPC; sie zeigen in entgegengesetzte Richtungen:

- **Die Control-Plane-API der Engine** (`olivares.api.v1.ControlPlane`) — eine kleine
  Spiegelung der REST-Oberfläche für Aufrufer, die einen typisierten Stub bevorzugen.
  Der REST-Contract in der [API-Referenz](/reference/api/) bleibt der umfassendere.
- **Der Wire-Contract für Plugins** (`olivares.sdk.v1.*`) — der versionierte Contract,
  den jeder Out-of-Process-Connector und jedes Out-of-Process-Modul spricht. Diesen
  implementieren Sie, wenn Sie in einer anderen Sprache als Go
  [einen Connector bauen](/de/how-to/build-a-connector/).

Diese Seite wird **aus den Registrierungstabellen generiert, die die Server an gRPC
übergeben**, nicht aus den `.proto`-Dateien. Genau diese Unterscheidung ist wichtig:
Eine ohne Regenerierung bearbeitete `.proto` beschreibt einen Service, den das Binary
nicht bereitstellt. Die Prüfung hinter dieser Seite meldet diesen Widerspruch, statt
die schönere der beiden Fassungen zu veröffentlichen. Eine hier aufgeführte Methode
kann von einem Client aufgerufen werden.

:::note[Stabilität]
Der Plugin-Contract `olivares.sdk.v1` ist versioniert und durch die
Breaking-Change-Erkennung von buf geschützt: Eine inkompatible Änderung erfordert ein
neues Major-Package. Wozu und wie lange wir uns damit verpflichten, steht unter
[API-Stabilität](/de/reference/api-stability/).
:::

## Transport und Authentifizierung

Mit Ausnahme von `GetServerInfo` erfordert jede Methode der folgenden Services einen
authentifizierten und autorisierten Principal. Zwei Ausnahmen sind beabsichtigt und
werden hier ausdrücklich genannt: `GetServerInfo` antwortet anonym, und der
Standard-Service `grpc.health.v1.Health` (`Check`, `List`, `Watch`) wird auf
demselben Listener ohne Principal bereitgestellt. Ein Probe oder Service Mesh muss ihn
auf jedem Pod erreichen können, so wie ein kubelet `/livez` erreicht. Ohne
Bearer-Token bleibt ein Request anonym, statt abgelehnt zu werden; ein vorhandenes,
aber ungültiges Token wird abgelehnt. Der Control-Plane-Service ist über den
gRPC-Listener der Engine erreichbar. Plugin-Services werden über den go-plugin-Broker
(Connectors auf demselben Host) oder per gRPC mit Mutual TLS (Remote Collector)
angerufen. Konfigurieren Sie den Listener mit den `OLIVARES_*`-Variablen aus der
[Konfigurationsreferenz](/de/reference/configuration/).

<!-- BEGIN GENERATED olivares-grpc-reference — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

Engine und Plugin-Host registrieren **28 rpc** über **7 Services**. Die folgenden
Tabellen werden aus den generierten Registrierungstabellen gelesen, die die Server an
gRPC übergeben. Eine hier aufgeführte Methode kann daher von einem Client aufgerufen
werden.

### `olivares.api.v1.ControlPlane`

Definiert in `apiv1/api.proto`; 5 rpc.

| Methode | Vollständige Methode | Art | Request | Response | Funktion |
|---|---|---|---|---|---|
| `CreateAgent` | `/olivares.api.v1.ControlPlane/CreateAgent` | unary | `CreateAgentRequest` | `Agent` | Registriert einen neuen Agenten im Inventar und gibt den gespeicherten Datensatz samt der von der übrigen API verwendeten Kennung zurück. |
| `GetAgent` | `/olivares.api.v1.ControlPlane/GetAgent` | unary | `GetAgentRequest` | `Agent` | Gibt einen Agenten anhand seiner Kennung mit denselben Feldern wie der REST-Inventar-Endpunkt zurück. |
| `GetServerInfo` | `/olivares.api.v1.ControlPlane/GetServerInfo` | unary | `Empty` | `ServerInfo` | Meldet Version, Edition und Readiness. Dies ist die einzige Methode dieses Service, die keinen authentifizierten Principal erfordert. |
| `ListAgents` | `/olivares.api.v1.ControlPlane/ListAgents` | unary | `ListAgentsRequest` | `ListAgentsResponse` | Listet seitenweise die für den aufrufenden Principal sichtbaren Agenten auf. |
| `VerifyAudit` | `/olivares.api.v1.ControlPlane/VerifyAudit` | unary | `VerifyAuditRequest` | `VerifyAuditResponse` | Verifiziert die Audit-Kette über einen Bereich erneut und meldet einschließlich Checkpoint-Status, ob die Hashes weiterhin verknüpft sind. |

### `olivares.sdk.v1.ContentSourceService`

Definiert in `olivaresv1/v1.proto`; 7 rpc.

| Methode | Vollständige Methode | Art | Request | Response | Funktion |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.ContentSourceService/Close` | unary | `Empty` | `Empty` | Beendet die von Open geöffnete Session und gibt frei, was der Connector dafür hielt. |
| `DeltaList` | `/olivares.sdk.v1.ContentSourceService/DeltaList` | server-streaming | `ContentDeltaRequest` | `ContentChange` (stream) | Streamt die Änderungen seit einem Cursor. Wird nur aufgerufen, wenn der Connector die Capability content.delta ankündigt. |
| `Describe` | `/olivares.sdk.v1.ContentSourceService/Describe` | unary | `Empty` | `DescribeResponse` | Gibt den Descriptor des Connectors zurück: Identität, Konfigurationsfelder und angekündigte Capabilities. |
| `Fetch` | `/olivares.sdk.v1.ContentSourceService/Fetch` | unary | `ContentFetchRequest` | `ContentDocument` | Gibt Body und Metadaten eines Dokuments für die Referenz zurück, die der Host aus dem List-Stream ausgewählt hat. |
| `FetchACL` | `/olivares.sdk.v1.ContentSourceService/FetchACL` | unary | `ContentFetchRequest` | `ContentACLResult` | Gibt die Berechtigungsreferenzen zurück, die ein Dokument regeln. Ein leeres Ergebnis bedeutet, dass der Standard der Wissensbasis gilt. |
| `List` | `/olivares.sdk.v1.ContentSourceService/List` | server-streaming | `ContentListRequest` | `ContentDocRef` (stream) | Streamt Dokumentreferenzen seitenweise, begrenzt durch die vom Host übergebenen Obergrenzen, damit ein Corpus nicht mit einem Aufruf vollständig in den Host-Speicher geladen wird. |
| `Open` | `/olivares.sdk.v1.ContentSourceService/Open` | unary | `OpenRequest` | `Empty` | Startet vor jedem Content-Aufruf eine Session mit der vom Host bereitgestellten Konfiguration. |

### `olivares.sdk.v1.HostService`

Definiert in `olivaresv1/v1.proto`; 3 rpc.

| Methode | Vollständige Methode | Art | Request | Response | Funktion |
|---|---|---|---|---|---|
| `Log` | `/olivares.sdk.v1.HostService/Log` | unary | `LogRecord` | `Empty` | Schreibt einen strukturierten Log-Datensatz über die Engine, damit ein Out-of-Process-Modul dort loggt, wo es ein In-Process-Modul tut. |
| `Publish` | `/olivares.sdk.v1.HostService/Publish` | unary | `Event` | `Empty` | Veröffentlicht im Namen eines Out-of-Process-Moduls ein Event auf dem Bus der Engine. |
| `Subscribe` | `/olivares.sdk.v1.HostService/Subscribe` | server-streaming | `SubscribeRequest` | `Event` (stream) | Streamt Bus-Events an das Modul, gefiltert nach den angeforderten Event-Typen. Ein leerer Filter bedeutet jeden Typ. |

### `olivares.sdk.v1.IngestService`

Definiert in `olivaresv1/v1.proto`; 1 rpc.

| Methode | Vollständige Methode | Art | Request | Response | Funktion |
|---|---|---|---|---|---|
| `Push` | `/olivares.sdk.v1.IngestService/Push` | client-streaming | `IngestEnvelope` (stream) | `IngestSummary` | Akzeptiert einen von einem Collector-Daemon gepushten Stream von Beobachtungen, hebt jede auf den Event-Bus und gibt nach Stream-Ende eine Zusammenfassung zurück. |

### `olivares.sdk.v1.ModuleService`

Definiert in `olivaresv1/v1.proto`; 4 rpc.

| Methode | Vollständige Methode | Art | Request | Response | Funktion |
|---|---|---|---|---|---|
| `Describe` | `/olivares.sdk.v1.ModuleService/Describe` | unary | `Empty` | `DescribeResponse` | Gibt den Descriptor des Moduls zurück: seine Identität und die akzeptierte Konfiguration. |
| `Init` | `/olivares.sdk.v1.ModuleService/Init` | unary | `InitRequest` | `Empty` | Übergibt dem Modul seine Konfiguration und lässt es sich vorbereiten, bevor etwas gestartet wird. |
| `Start` | `/olivares.sdk.v1.ModuleService/Start` | unary | `Empty` | `Empty` | Startet die Arbeit des Moduls nach einem erfolgreichen Init. |
| `Stop` | `/olivares.sdk.v1.ModuleService/Stop` | unary | `Empty` | `Empty` | Stoppt das Modul und lässt es seine gehaltenen Ressourcen freigeben. |

### `olivares.sdk.v1.OutputService`

Definiert in `olivaresv1/v1.proto`; 4 rpc.

| Methode | Vollständige Methode | Art | Request | Response | Funktion |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.OutputService/Close` | unary | `Empty` | `Empty` | Beendet die von Open geöffnete Session und gibt frei, was der Connector dafür hielt. |
| `Describe` | `/olivares.sdk.v1.OutputService/Describe` | unary | `Empty` | `DescribeResponse` | Gibt den Descriptor des Connectors zurück: Identität, Konfigurationsfelder und angekündigte Capabilities. |
| `Notify` | `/olivares.sdk.v1.OutputService/Notify` | unary | `NotifyRequest` | `NotifyResponse` | Übermittelt eine Benachrichtigung an das Ziel und meldet, wie das Ziel sie behandelt hat; danach richtet sich ein Retry des Hosts. |
| `Open` | `/olivares.sdk.v1.OutputService/Open` | unary | `OpenRequest` | `Empty` | Startet vor jeder Zustellung eine Session mit der vom Host bereitgestellten Konfiguration. |

### `olivares.sdk.v1.SourceService`

Definiert in `olivaresv1/v1.proto`; 4 rpc.

| Methode | Vollständige Methode | Art | Request | Response | Funktion |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.SourceService/Close` | unary | `Empty` | `Empty` | Beendet die von Open geöffnete Session und gibt frei, was der Connector dafür hielt. |
| `Describe` | `/olivares.sdk.v1.SourceService/Describe` | unary | `Empty` | `DescribeResponse` | Gibt den Descriptor des Connectors zurück: Identität, Konfigurationsfelder und angekündigte Capabilities. |
| `Gather` | `/olivares.sdk.v1.SourceService/Gather` | server-streaming | `Empty` | `Observation` (stream) | Streamt Beobachtungen an den Host, der jede auf den Event-Bus hebt. Der Stream endet nach einem Batch-Lauf oder wenn der Host ihn abbricht. |
| `Open` | `/olivares.sdk.v1.SourceService/Open` | unary | `OpenRequest` | `Empty` | Startet vor dem Sammeln einer Beobachtung eine Session mit der vom Host bereitgestellten Konfiguration. |

<!-- END GENERATED olivares-grpc-reference -->

## Nachrichtenformen

Die Tabellen nennen jede Request- und Response-Nachricht. Ihre Felder sind in den beim
jeweiligen Service genannten `.proto`-Dateien deklariert, die im Repository
ausgeliefert werden und die Quelle für die generierten Stubs bilden. Zuvor sollten Sie
zwei Konventionen kennen:

- **Vokabularfelder sind Strings, keine geschlossenen Enums** — Access Mode,
  Signalquelle, Confidence, Severity und Event-Typ. Ein Drittanbieter-Connector kann
  seine eigene Signalquelle einführen, ohne auf ein SDK-Release zu warten.
- **Payload-Formen sind geschlossen.** Ein `Observation`- oder `Event`-Payload ist
  ein `oneof` der bekannten Nachrichtentypen plus JSON-Fallback für moduldefinierte
  Event-Payloads. Ein unbekannter Payload ist ein Contract-Fehler; er wird nicht still
  verworfen.

## Client generieren

Die `.proto`-Dateien sind der Contract. Richten Sie die Protobuf-Toolchain Ihrer
Sprache für den Plugin-Contract auf `sdk/plugin/proto/olivaresv1/v1.proto` oder für
die Control-Plane-Spiegelung auf `core/api/proto/apiv1/api.proto`. Fertige Clients
für Go und TypeScript beschreibt
[Client-SDKs verwenden](/de/how-to/use-the-client-sdks/).
