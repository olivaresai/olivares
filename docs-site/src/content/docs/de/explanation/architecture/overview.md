---
title: "Architekturüberblick"
description: "Wie Olivares AI aufgebaut ist: eine Engine, Module und Konnektoren — das Plattformmodell, die acht Kern-Subsysteme, die Access Map und die Deployment-Topologien."
---

Diese Seite erklärt, wie Olivares AI strukturiert ist und warum. Es handelt sich um eine *Erläuterung*, nicht um eine Anleitung: Sie vermittelt Ihnen das mentale Modell, das Sie brauchen, um über die Control Plane nachzudenken, bevor Sie sie installieren, konfigurieren oder erweitern. Für Schritt-für-Schritt-Anweisungen folgen Sie den [How-to-Anleitungen](/de/how-to/self-hosting/); für die genauen Verträge siehe die [API-Referenz](/reference/api/) und die [Events-Referenz](/de/reference/events/).

:::note[Entwurfsphase]
Vieles vom Folgenden beschreibt ein System, das sich **in der Beta** befindet und in Teilen in der Entwurfsphase ist. Das Plattformmodell, das Datenmodell, der kooperative Ingest-Pfad und das Access-Map-Differenzierungsmerkmal sind spezifiziert und werden inkrementell gebaut; einige Fähigkeiten auf Modulebene sind geplant statt ausgeliefert. Wo eine Fähigkeit noch nicht gebaut ist, sagt diese Seite das. Betrachten Sie dies als die beabsichtigte Architektur, nicht als Behauptung, dass jede Schicht heute produktionsreif ist.
:::

<img class="light:sl-hidden" src="/diagrams/02-architecture-dark.svg" alt="Architektur: Agentenoberflächen, Audit-Quellen, MCP- und A2A-Gegenstellen sowie Inhaltsquellen werden auf drei Wegen in eine einzige selbst gehostete Go-Binärdatei mit eingebetteter Konsole erfasst, die die Produktmodule, die Richtlinien- und Durchsetzungsschicht und das signierte Nachweisjournal über einem mandantenbezogenen Speicher trägt; sie bedient die Konsole, die REST-API, eine fokussierte gRPC-Teilmenge, die CLI und den Terraform-Provider, wobei die Cloud-Steuerungsebene (gebaut, nicht bereitgestellt) und das Lizenzportal (bereitgestellt, Auslieferung aus) eigene Ebenen sind." />
<img class="dark:sl-hidden" src="/diagrams/02-architecture-light.svg" alt="Architektur: Agentenoberflächen, Audit-Quellen, MCP- und A2A-Gegenstellen sowie Inhaltsquellen werden auf drei Wegen in eine einzige selbst gehostete Go-Binärdatei mit eingebetteter Konsole erfasst, die die Produktmodule, die Richtlinien- und Durchsetzungsschicht und das signierte Nachweisjournal über einem mandantenbezogenen Speicher trägt; sie bedient die Konsole, die REST-API, eine fokussierte gRPC-Teilmenge, die CLI und den Terraform-Provider, wobei die Cloud-Steuerungsebene (gebaut, nicht bereitgestellt) und das Lizenzportal (bereitgestellt, Auslieferung aus) eigene Ebenen sind." />

## Das Plattformmodell: eine Engine, Module, Konnektoren

Olivares AI ist kein Einzweck-Werkzeug. Es ist eine **modulare Plattform** in der Tradition von Grafana, Backstage und der Kubernetes-Control-Plane: **eine Engine (Core) plus Module plus Konnektoren**. Das Produkt umfasst einen Katalog von Modulen — Inventar, Sessions, die Access Map, Governance, FinOps, Evaluierungen, Guardrails und mehr —, aber sie alle sitzen auf einer einzigen gemeinsamen Engine auf.

Die maßgebliche Randbedingung der Architektur ist die **„No-Re-Architecture“-Regel**: Die Engine ist so entworfen, dass *jedes* Modul aus dem Katalog hinzugefügt werden kann, ohne den Core oder die anderen Module anzufassen. Konkret gilt für jedes neue Modul:

1. Es **konsumiert** normalisierte Events und Daten aus der Engine;
2. Es **deklariert** seine eigenen Entitäten im gemeinsamen Datenmodell;
3. Es **exponiert** seine eigenen API-Endpunkte und UI-Ansichten.

Kein Modul greift in das Innere eines anderen ein, und keines formt den Core um, um zu passen. Die Engine zahlt von Anfang an den Vorab-Preis dafür, mandantenfähig, ereignisgesteuert und API-first zu sein — genau deshalb, damit Breite später ohne Neuentwurf hinzugefügt werden kann. Dasselbe Prinzip erklärt die Reihenfolge des Aufbaus — zuerst die CLI-Engine, das Web darauf: Die CLI *ist* die Engine und exponiert die volle Funktionalität über CLI und API; das Web ist eine Präsentationsschicht über **derselben API**, ohne duplizierte Logik. Erst die Engine und dann das visuelle Gesicht darauf zu bauen, ist keine Neuarchitektur.

Die differenzierende Fähigkeit — die Read/Write Access Map mit dem Permitted-vs-Observed-Diff — ist selbst **ein Modul** (Modul III) über dem gemeinsamen Modell, keine maßgeschneiderte Pipeline. Das hält die Plattform ehrlich: Die Vorzeigefunktion gehorcht denselben Regeln wie alles andere.

## Die acht Engine-Subsysteme

Die Engine (der Core, „Layer 0“) ist die Menge gemeinsamer Subsysteme, an denen alles andere hängt. Es gibt acht davon.

| Subsystem | Was es tut | Warum es im Core lebt |
|---|---|---|
| **Ingest + Event Bus** | Empfängt OTLP- und Konnektor-Eingaben, normalisiert sie und verteilt Events an Module | Module reagieren auf Events, ohne aneinander gekoppelt zu sein |
| **Connector SDK** | Eine stabile Input/Output-Konnektor-Schnittstelle — das Rückgrat der Breite | Dritte erweitern die Plattform, ohne den Core zu forken |
| **Module Runtime** | Lädt und führt Module aus: in-process kompiliert plus Out-of-Process-Plugins | Fügt ein Modul hinzu, ohne den Core neu zu architektieren oder neu zu kompilieren |
| **Allgemeines Datenmodell** | Mandantenfähige Entitäten und Relationen, die den gesamten Katalog bedienen | Ein Schema, das alle Module teilen und erweitern |
| **API (REST/gRPC) + Manage-as-Code** | Alle Funktionalität über eine API, plus ein Terraform-Provider | Die CLI und das Web sprechen dieselbe API; das Panel ist GitOps-fähig |
| **AuthN/Z + Mandantenfähigkeit** | RBAC/ABAC, Orgs und Mandanten, Isolation | Berechtigungen und Mandantenfähigkeit nachzurüsten ist ruinös teuer — daher von Tag eins an |
| **Audit + Integrität** | Append-only, hash-chained Ledger | Manipulationsnachweis ist querschnittlich, niemals optional |
| **Lizenz / Entitlement** | Offline-Ed25519-Lizenzvalidierung | Self-Service-Kommerziell, funktioniert air-gapped |

Einige Details, die hervorzuheben sind:

- **Module Runtime.** Core-Module sind in das Binary kompiliert; Out-of-Process-Module und Konnektoren laufen als Plugins über gRPC mit `hashicorp/go-plugin`. Das ergibt Fehlerisolation und erlaubt, ein Modul hinzuzufügen, ohne den Core neu zu kompilieren.
- **Event Bus.** Standardmäßig in-process (Go-Channels). Die verteilte Anbindung über **NATS ist optional**, nicht erforderlich — Single-Node-Deployments berühren sie nie.
- **Manage-as-Code.** Die API ist der maßgebliche Vertrag; die Manage-as-Code-Oberfläche fügt einen Terraform-Provider hinzu, sodass die Control Plane selbst deklariert und versionskontrolliert werden kann.
- **Audit + Integrität.** Das Ledger ist **append-only und hash-chained**, mit **Ed25519-signierten Checkpoints**. Einträge tragen eine Sequenznummer, den vorherigen Hash, den aktuellen Hash und eine Signatur — und tragen nie PII. Das Ledger verlässt die Box auf zwei Wegen: Ein **Pull**-Export-Endpunkt gibt CEF, LEEF, syslog, OTLP (ein vollständiger, POST-fähiger Export-Request; `otlp_envelope` ist ein exaktes Alias, und die reine LogRecord-Projektion ist das separate Token `otlp_log_record`) oder OCSF aus, und ein **Push** — real, sobald ein `audit.recorded`-Eventing-Abonnement konfiguriert ist — stellt jeden versiegelten Datensatz mindestens einmal über den dauerhaften Transport zu. Siehe [So leiten Sie Audit an Splunk weiter](/de/how-to/forward-audit-to-splunk/).
- **Lizenz.** Die Validierung erfolgt **offline** mit Ed25519, und die Engine setzt keinen Lizenzaufruf ab — was den air-gapped Betrieb erst möglich macht. Der einzige Befehl, der nach außen geht, ist `olivares upgrade`: Standardmäßig lädt er von den GitHub-Releases des öffentlichen Repositorys, mit `--enterprise` vom Lizenz-Worker (`licenses.olivares.ai`) — sofern `--endpoint` ihn nicht auf Ihren eigenen Spiegel richtet oder `--bundle` aus einem mitgeführten Bundle installiert.

Für die Details zu Authentifizierung und Autorisierung (opake Bearer-Token, First-Boot-Setup-Token, der Policy Decision Point) siehe das [Sicherheitsmodell](/de/explanation/security/security-model/); sie werden hier nur dort zusammengefasst, wo die Architektur von ihnen abhängt.

### Das allgemeine Datenmodell

Ein einziges mandantenfähiges Schema bedient den gesamten Katalog. Jede Core-Entität trägt eine `tenant_id`, und Isolation wird auf Query-/Zeilenebene durchgesetzt. Die Core-Entitäten umfassen Orgs und Mandanten, Agents, Sessions, Modelle und Provider, MCP-Server, Skills und Tools, Ressourcen (Datenbanken, Server, Stores, APIs), Identitäten, Policies, Kostendatensätze, Evaluierungsergebnisse, Findings, Audit-Events, Health-Status und Deployments — und, zentral, die **`AccessEdge`**.

Jedes Modul registriert seine eigenen Entitäten und Relationen über eine Type-Registry und modulspezifische Tabellen, ohne den Core zu brechen. Das ist der Mechanismus hinter der „No-Re-Architecture“-Regel auf der Datenebene.

Der Store beginnt als **SQLite** (der reine Go-Treiber `modernc`, sodass das Binary kein CGO benötigt und air-gapped läuft) für Single-Node-Deployments und wechselt zu **Postgres mit Row-Level Security** für Mandantenfähigkeit und Skalierung.

## Modul III: die Access Map als Sicht über dem Modell

Das Vorzeigemodul ist die **Read/Write Access Map** und ihr **Permitted-vs-Observed-Diff** — Least-Privilege-Drift. Der entscheidende architektonische Punkt ist, dass dies eine **Sicht über dem allgemeinen Datenmodell ist, kein separates Schema**. Die Map wird aus `AccessEdge`-Entitäten materialisiert, und die `AccessEdge` selbst **trägt sowohl die erlaubte Seite als auch die beobachtete Seite**, zusammen mit der Signalquelle und einem Konfidenzgrad. Der Diff ist daher eine Query über demselben mandantenfähigen Modell, das jedes andere Modul verwendet.

### Read-first und Minimal-Data

Die Map ist **read-first**: Sie beobachtet aus Logs, OpenTelemetry und (als Rückfallebene) eBPF — sie befindet sich nie im Datenpfad der Aufrufe des Agents. Sie ist außerdem **minimal-data**: Sie speichert die *Relation* (ein Agent liest/schreibt eine Ressource), niemals Payloads, Secrets oder PII. Die Asymmetrie ist gewollt — hohes Signal, niedriges Risiko.

### Kooperativer Pfad gekreuzt mit nativem Store-Audit

Genauigkeit entsteht durch das Kreuzen zweier unabhängiger Arten von Evidenz:

- **Der kooperative Pfad** — Claude Code und Agents emittieren Telemetrie über **OpenTelemetry (OTLP)**, ergänzt durch **MCP-Introspektion** der Tools und Ressourcen, die ein Server exponiert. Der OTLP-Empfänger ist Teil des Core-Ingest und lauscht standardmäßig auf Loopback. Siehe [Claude Code verbinden](/de/how-to/connect-claude-code/).
- **Natives Store-Audit** — der Store sagt Ihnen, was tatsächlich passiert ist. **pgAudit klassifiziert `READ` versus `WRITE`** wortgetreu auf Postgres; **CloudTrail legt `readOnly` offen** für S3; äquivalentes natives Audit existiert für andere Engines.

Wenn der kooperative Pfad und das eigene Audit des Stores über eine Edge übereinstimmen, haben Sie eine bestätigte Read/Write-Relation.

### Die eBPF-Rückfallebene, nicht vertrauenswürdige Annotationen und gestufte Abdeckung

Drei weitere Eigenschaften machen die Map vertrauenswürdig statt naiv:

- **eBPF / Tetragon ist die nicht-kooperative Rückfallebene.** Für Pfade, die nicht kooperieren, liefert ein kernel-level Observer Ground Truth über Read/Write-Absicht auf Prozess- und Host-Ebene. Er läuft außerhalb der Kontrolle des Agents (Anti-Umgehung), ist aber blind für TLS-Payloads — was in Ordnung ist, denn die Map braucht nur die *Relation*, nicht den Inhalt.
- **MCP-Annotationen sind nicht vertrauenswürdig.** Die MCP-Read-only-/Destructive-Hinweise sind ein nützliches Signal, aber die MCP-Spezifikation selbst besagt, dass Clients sie als nicht vertrauenswürdig behandeln müssen. Die Map **bestätigt** sie daher gegen andere Quellen und **vertraut einer Annotation niemals allein**.
- **Die Abdeckung ist gestuft, und das Produkt sagt das.** Manche Stores sind **sauber** passiv zu beobachten (SQL-Datenbanken, Object Stores, Warehouses); manche sind **verlustbehaftet** (Mongo, Vektordatenbanken); und manche sind **passiv unmöglich zu beobachten** (Redis, SQLite, D1). Die Map zeigt Konfidenzgrade (zugeordnet versus näherungsweise), statt eine Präzision vorzutäuschen, die sie nicht hat.

:::caution[Eine harte Abhängigkeit: Identität pro Agent]
Natives Audit ordnet Aktivität einer Credential oder Rolle zu, nicht einem Agent. Ein gemeinsam genutzter Service-Account plus ein Connection-Pool bricht die Zuordnung zusammen — Sie können nicht mehr sagen, welcher Agent was getan hat. Dies aufzulösen erfordert das Ausstellen oder Durchsetzen von **Identität pro Agent**, was die Brücke von der Access Map zum Governance-Modul ist. Dies ist in der Entwurfsphase, und ein Proof-of-Concept auf dem kooperativen Pfad (Claude Code OTEL + MCP in Postgres pgAudit) ist das Make-or-Break-Gate, bevor das Modul ausgebaut wird.
:::

### Zugriff auf die Map

Den Zugriffsgraphen anzusehen, ist eine **privilegierte Aktion**: mandantengebunden, verfügbar für die Editor-Rolle und höher (nie die niedrigste Viewer-Rolle), und **jeder Lesezugriff wird auditiert**. Die Routen der Map — der Graph und das Drift-Ergebnis — gehören nicht zum stabilen Core-Vertrag; sie werden in der separaten **Beta**-[Modulrouten-Referenz](/reference/api-beta/) veröffentlicht (ausgeliefert unter `/openapi.beta.json`), wobei ihre feldgenauen Formen in typisierten Go- und TypeScript-Schnittstellen liegen. Das Permitted-vs-Observed-Ergebnis wird an der `drift`-Route der Engine (`/v1/m/accessmap/drift`) exponiert; es gibt keinen separaten `diff`-Endpunkt. Die stabile Core-REST-Oberfläche — 53 Pfade, gerendert aus dem eigenen OpenAPI-3.1-Vertrag des Produkts — ist in der [API-Referenz](/reference/api/) dokumentiert. Für die vollständige Modulliste siehe den [Modulkatalog](/de/reference/modules/overview/).

## Deployment-Topologie

Dasselbe Binary unterstützt mehrere Topologien. Eine Randbedingung gilt über alle hinweg: Die **Data Plane — die Collectors — läuft immer auf der Infrastruktur des Kunden**. Das ermöglicht Datenschutz und den air-gapped Betrieb. Es gibt keine verpflichtende Telemetrie und standardmäßig keinen Egress der Control Plane. Den Kundenperimeter überschreitet nur, was der Kunde dafür konfiguriert: Aufrufe an seine Modell-APIs, die von ihm eingerichteten SIEM-/Webhook-Ausgaben und ein externer Embedding-Anbieter, falls er einen bereitstellt.

### Single Binary

Der Standard. Ein statisches Go-Binary trägt die CLI-Engine, das **per `go:embed` eingebettete Web-UI** (vom selben Origin wie die API bereitgestellt) und **SQLite** als Store. Sie liefern ein Artefakt aus und hosten es selbst. Das ist die Topologie hinter dem [Zero-to-Graph-Tutorial](/de/tutorials/zero-to-graph/) und der [Self-Hosting-Anleitung](/de/how-to/self-hosting/).

### Verteilt

Für Multi-Host-, Skalierungs- und mandantenfähige Estates: Collectors am Rand **pushen über gRPC mit mutual TLS an einen zentralen Core**, der Store wird zu **Postgres** (mit Row-Level Security), und der Event Bus läuft auf **NATS**. Collectors haben keinen eingehenden Listener — sie pushen, sie bedienen nicht —, was die Angriffsfläche am Rand minimal hält.

### Air-gapped

In dieser Topologie läuft alles lokal mit **null Egress**: Der Store ist lokal und die Lizenz wird **offline** validiert. `olivares upgrade` — der einzige Befehl, der uns sonst kontaktieren würde — installiert hier aus einem mitgebrachten Bundle (`--bundle`) statt aus dem Update-Kanal. Siehe [Air-Gap-Installation](/de/how-to/air-gap-install/).

### Managed (Zukunft)

Eine gehostete Control Plane steht auf der Roadmap. Selbst dann gilt die Randbedingung: **Die Collectors laufen weiterhin auf der Infrastruktur des Kunden**, und nur die Control Plane wird gehostet. Dies ist in der Entwurfsphase.

:::tip[Die Topologie in einer Zeile]
Die Control Plane (die Engine) kann als ein Binary selbst gehostet oder künftig managed betrieben werden; die Data Plane (die Collectors) ist immer auf der Kundeninfrastruktur. Das Web ist immer eine Sicht über die eigene API der Engine — nie ein separater Dienst mit eigener Logik.
:::

## Vertrauensgrenzen und Lizenzierung

Über die Laufzeit-Topologie hinaus prägen zwei Grenzen die Architektur:

- **Die Konnektor-Grenze.** Ein Konnektor **importiert niemals aus dem Core** — er hängt nur vom SDK ab. Das verhindert, dass Drittanbieter-Konnektoren den Core kontaminieren, und hält die Lizenzgrenze sauber.
- **Die Lizenzgrenze.** Der Core, die Module und das Web sind **AGPL-3.0-only**; das SDK und die Konnektoren sind **Apache-2.0**; die Enterprise-Stufe ist kommerziell. Die obige Konnektor-Grenze ist das, was die Apache/AGPL-Trennung im Code durchsetzbar macht. Siehe [Open Core und Lizenzierung](/de/explanation/open-core-and-licensing/).

## Sicherheitslage, kurz gefasst

Die Architektur ist secure-by-design: read-first Beobachtung (niedriges, asymmetrisches Risiko), push-only Collectors ohne eingehenden Listener, mutual TLS zwischen Collector und Core, minimal-data (Edges, nie Payloads), Manipulationsnachweis durch das append-only hash-chained Ledger, mandantenfähige Isolation verwurzelt im Datenmodell und Self-Hosting ohne verpflichtende Telemetrie und standardmäßig ohne Egress der Control Plane. Den Kundenperimeter überschreitet nur, was der Kunde dafür konfiguriert: Aufrufe an seine Modell-APIs, die von ihm eingerichteten SIEM-/Webhook-Ausgaben und ein externer Embedding-Anbieter, falls er einen bereitstellt. Die vollständige Analyse — einschließlich wie jede Vertrauensgrenze verteidigt wird und was explizit außerhalb des Geltungsbereichs liegt — findet sich im [Sicherheitsmodell](/de/explanation/security/security-model/) und im [Bedrohungsmodell](/de/explanation/security/threat-model/).

## Wie es weitergeht

- [Modulkatalog](/de/reference/modules/overview/) — die vollständige Menge an Modulen und wie sie auf die obigen Schichten abgebildet werden.
- [Events-Referenz](/de/reference/events/) — die normalisierten Events, die die Ingest-Schicht an Module verteilt.
- [Bedrohungsmodell](/de/explanation/security/threat-model/) — die Angreifer, die Vertrauensgrenzen und die Gegenmaßnahmen.
- [Ehrlichkeit und Grenzen](/de/start/honesty-and-limits/) — was heute läuft versus was geplant ist.
