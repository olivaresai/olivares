---
title: "Referenz"
description: "Die informationsorientierte Referenz: die REST-API, der Event-Bus, der Modul-Katalog, das CLI und die Konfiguration — präzise und erschöpfend, nichts abgeleitet."
---

Referenz ist **informationsorientiert**. Ihre Aufgabe ist es, präzise und vollständig zu sein, nicht
zu lehren oder zu überzeugen: sie stellt fest, was die Interfaces sind, was ihre Inputs und
Outputs sind und was die Defaults sind — und hört da auf. Die Prosa ist absichtlich trocken.
Wenn du das System durch Tun lernen willst, beginne mit dem
[Tutorial](/de/tutorials/zero-to-graph/); wenn du eine bestimmte Aufgabe erledigen willst,
nutze einen [How-To-Guide](/de/how-to/connect-a-source/); wenn du verstehen willst, *warum*
das System so gebaut ist, wie es ist, lies die
[Erklärung](/de/explanation/architecture/overview/). Dieser Abschnitt ist für den Fall, dass du
gegen das Produkt baust und den exakten Contract brauchst.

Das meiste, was folgt, ist generiert oder handgeleitet **direkt aus den eigenen
Source-Artefakten des Produkts**, sodass die Referenz nicht still von dem abdriften kann, was die Engine
tatsächlich ausliefert. Wo eine Fähigkeit Design-Stage oder post-v1 ist, sagt die relevante Seite
das klar; siehe [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) für den
gesamten Contract.

## Die Referenzbereiche

| Bereich | Was er dokumentiert | Source of Truth |
|---|---|---|
| **[REST-API](/reference/api/)** | Die Control-Plane-HTTP-API: Auth, Setup, Tenancy, Agents, die R/RW-Access-Map, Tokens und das Audit-Ledger. | Der **OpenAPI 3.1**-Contract des Produkts (53 Core-Paths), zur Build-Zeit aus der realen Datei gerendert — keine Kopie. |
| **[Modulrouten (Beta)](/reference/api-beta/)** | Die Modulrouten des Produkts (`/v1/m/<ns>/…`) — FinOps, Compliance, Governance, Sessions, Models, Knowledge, … — als separates **Beta**-OpenAPI-Dokument. | Derselbe OpenAPI-3.1-Vertrag, zur Build-Zeit aus den von den Modulen registrierten Routen reflektiert. |
| **[Stabilitätspolicy](/de/reference/api-stability/)** | Versionierung, Stabilitätsstufen, Deprecation-/Sunset-Signalisierung und die Mindest-Support-Fenster für die API, den Provider und die Client-SDKs. | Die In-Code-Deprecation-Tabelle und ihre build-failing-Fenster-Tests. |
| **[gRPC](/de/reference/grpc/)** | Der gRPC-Mirror der Engine und der versionierte Plugin-Wire-Vertrag, den jeder Out-of-process-Connector und jedes Out-of-process-Modul spricht. | Die `grpc.ServiceDesc`-Registrierungstabellen, die die Server an gRPC übergeben. |
| **[Event-Bus](/de/reference/events/)** | Der interne Event-Bus: der Event-Envelope, die First-Party-Event-Typen und die Beobachtungs-Payloads, die Connectors darauf heben. | Ein **AsyncAPI 3.0**-Contract, handgeleitet aus dem Go-SDK. |
| **[Konsolenansichten](/de/reference/console/)** | Jede von der Konsole veröffentlichte Route samt erforderlicher RBAC-Permission und der Referenzseite, die ihr produktinterner Hilfe-Link öffnet. | Der Routenzensus der Konsole, gegen den gebauten Router gepinnt. |
| **[Modul-Katalog](/de/reference/modules/overview/)** | Die 30 Produktmodule — was jedes ist, sein Status und welche Routes (falls vorhanden) es außerhalb der Core-API exponiert. | Der Produkt-Capability-Katalog und die typed Modul-Interfaces. |
| **[CLI](/de/reference/cli/)** | Das `olivares`-Binary und seine Subcommands — `serve`, `collector`, `audit`, `license`, `openapi`, `version` — und ihre Flags. | Die kompilierten Command-Definitionen. |
| **[Konfiguration](/de/reference/configuration/)** | Umgebungsvariablen und Runtime-Optionen: das Datenverzeichnis, die Source-Verdrahtung, die Authorization-Engine und das Ledger-Signing. | Die Konfigurations-Loader der Engine. |

## REST-API

Die [REST-API-Referenz](/reference/api/) wird zur Build-Zeit aus dem
**OpenAPI 3.1**-Contract des Produkts gerendert — demselben Dokument, das die Engine an ihrem
eigenen `/openapi.json`-Endpoint ausliefert. Nichts wird von Hand transkribiert, sodass die gerenderte
Referenz der Contract ist. Sie deckt den credential-freien First-Boot-Flow ab
(`POST /v1/setup` mit dem One-Time-Setup-Token, dann `POST /v1/auth/login`),
Identity und Tenancy, Agents, die Read/Write-Access-Map
(`GET /v1/access-edges`; ihr rekonzilierter Least-Privilege-*Drift* wird vom
Access-Map-Modul statt von der Core-Oberfläche ausgeliefert), Token-Management und das Audit-
Ledger.

Der Contract beschreibt **53 Core-Paths**. Das ist bewusst: es ist die stabile,
versionierte Oberfläche der Control Plane, nicht jede Route, die die Engine beantworten kann.
Worauf sich „stabil“ verpflichtet — Versionierung, Deprecation-Signalisierung und Mindest-
Support-Fenster — ist die [API-Stabilitätspolicy](/de/reference/api-stability/).

:::note[Modulrouten sind ein separater Beta-Vertrag]
Die Modulrouten — zum Beispiel die `/v1/m/accessmap/graph`,
`/v1/m/accessmap/neighbors` und `/v1/m/accessmap/drift` des Access-Map-Moduls
— sind **nicht** Teil des stabilen Core-Dokuments mit 53 Pfaden. Sie werden als
separates **Beta**-OpenAPI-Dokument unter [`/reference/api-beta/`](/reference/api-beta/)
veröffentlicht (ausgeliefert unter `/openapi.beta.json`, reflektiert aus den Routen,
die die Module tatsächlich registrieren), sodass die stabile Oberfläche identifizierbar
bleibt, während die vollständige Produktoberfläche weiterhin programmierbar ist. Beta
bedeutet, dass sich die Formen mit Vorankündigung ändern können (ein kürzeres
Support-Fenster als bei Stable); der Detailgrad auf Feldebene liegt weiterhin in den
typisierten Go- und TypeScript-Schnittstellen. Das Least-Privilege-Ergebnis ist die
`drift`-Route; es gibt keinen separaten `diff`-Endpoint.
:::

### gRPC-Mirror (`olivares.api.v1`)

Die Control Plane exponiert zudem eine **gRPC**-Oberfläche — den `ControlPlane`-Service im
versionierten Proto-Package `olivares.api.v1`. Es ist ein **fokussierter, eingefrorener Mirror**
einer Teilmenge des obigen REST-Contracts (Server-Info, Agent-list/get/create, Audit-
verify), eingesetzt dort, wo ein typed Binary-Contract bevorzugt wird (zum Beispiel Collectors).
Er spiegelt den REST-Contract, statt ihn zu erweitern; das OpenAPI-Dokument bleibt
die kanonische Oberfläche für die volle API.

## Event-Bus

Die [Event-Bus-Referenz](/de/reference/events/) ist ein **AsyncAPI 3.0**-Contract. Der
Bus ist **standardmäßig in-process** — Connectors heben normalisierte Beobachtungen darauf
als typed Events, und Module und Output-Connectors abonnieren **nach Event-Typ**
und reagieren, ohne dass eines von ihnen ein anderes direkt aufruft. Ein verteiltes Binding
über NATS ist optional, nicht erforderlich.

Der Contract ist **handgeleitet aus dem Go-SDK**, nicht generiert: die autoritativen
Definitionen sind der Event-Envelope, die First-Party-Event-Typen und die
Beobachtungs-Payloads (die agent→resource-Zugriffsbeobachtungen, Cost-Samples und
Finding-Reports). Wo der Bus etwas noch nicht formalisiert, sagt die Referenz
das, statt es zu erfinden.

## Modul-Katalog

Der [Modul-Katalog](/de/reference/modules/overview/) zählt die **30 Module** auf,
die auf der Core-Engine sitzen, über neun Capability-Bereiche. Eines der
nützlichsten ist die **R/RW-Access-Map** mit ihrem **Permitted-vs-Observed**-Diff: sie
liest aus Logs, OTEL und (als non-cooperative-Backstop) eBPF, statt im
Data Path zu sitzen, und sie speichert nur die Relation *welcher Agent welche Resource lesen oder schreiben*
kann — niemals Payloads, Secrets oder PII.

Der Katalog ist ehrlich über Status und Coverage. Jedes Modul trägt seine eigene
Reife — die meisten live und end-to-end verdrahtet, einige partiell oder opt-in. Passive
Beobachtung ist **gestuft** nach Store-Typ — clean für SQL-, Object- und Warehouse-
Stores; lossy für Document- und Vector-Stores; impossible ohne Kooperation für
In-Memory- oder eingebettete Stores — und der Katalog markiert, wo ein Modul
Design-Stage ist. Own-Model-Registry und Fine-Tuning ist eine **geplante Fähigkeit**, nicht
eines der 30 ausgelieferten Module.

## CLI

Die [CLI-Referenz](/de/reference/cli/) dokumentiert das einzelne `olivares`-Binary
und seine Subcommands. Das, was du ausführst, um die Control Plane zu betreiben, ist `serve`,
das die HTTP- (REST + eingebettete Web-UI) und gRPC-Listener startet; **TLS ist
standardmäßig an**. Andere Subcommands decken den Collector, das Audit-Ledger (`verify`,
`checkpoint`, `export`), das License-Tooling und das Emittieren des OpenAPI-Dokuments ab.

:::caution[Erst bauen, dann ausführen]
Es gibt kein `task run` oder bloßen `docker run`-Shortcut. Du baust und rufst entweder
das Binary direkt auf — `task setup`, `task build`, dann `./bin/olivares serve`
— oder bringst es mit der bereitgestellten Compose-Datei hoch und liest den One-Time-Setup-Token
aus den Logs. Die CLI-Seite listet die verifizierten `serve`-Flags und ihre Defaults.
:::

## Konfiguration

Die [Konfigurations-Referenz](/de/reference/configuration/) listet die Umgebungs-
variablen und Runtime-Optionen, die ein Deployment formen. Die tragenden sind
das Datenverzeichnis (`OLIVARES_DATA_DIR`), die reale (non-demo) Source-Verdrahtung, gelesen
aus `OLIVARES_SOURCES_CONFIG`, bevor die Engine startet, und der Authorization-
Engine-Selektor `OLIVARES_PDP_ENGINE` (`cedar`, `opa` oder `none`).

Zwei Design-Regeln ziehen sich durch die Konfigurations-Oberfläche. Eine **unkonfigurierte Source
warnt ehrlich**, statt die Engine fehlschlagen zu lassen. Und der Authorization-Seam **schränkt
nur ein, weitet niemals aus**: RBAC ist deny-by-default, das Ansehen des Access-Graphs
ist eine privilegierte Action, und jeder solche Read wird auditiert.
