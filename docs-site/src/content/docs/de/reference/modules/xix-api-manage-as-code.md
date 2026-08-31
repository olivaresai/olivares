---
title: "Modul XIX — die eigene API und die Manage-as-Code-Fläche"
description: >-
  Die grundlegende Fläche der Engine: jede Control-Plane-Aktion über eine
  REST/gRPC-API, plus einen Terraform-Provider, sodass der Control Plane selbst
  deklariert und versionskontrolliert wird. Was der API-Vertrag ist, was der Provider
  verwaltet und die ehrlichen Grenzen jedes Teils.
---

Modul XIX ist kein Feature, das an die Engine geschraubt ist — es **ist** die Fläche der Engine.
Jedes andere Modul erreicht die Außenwelt über dieselbe First-Party-API, und die Web-UI ist eine
Präsentationsschicht über genau diesem Vertrag, keine parallele. Diese Seite ist die Referenz dafür,
was diese Fläche heute exponiert und wie man den Control Plane als Code verwaltet, mit ihren realen
Grenzen.

## Der API-Vertrag

Die Engine spricht eine REST-API unter `/v1` (chi-Router, gehärteter `http.Server`) und einen
**fokussierten, eingefrorenen gRPC-Spiegel** davon (`olivares.api.v1`: Server-Info, Agent-Lesen/-Erstellen,
Audit-Verifikation, plus den standardmäßigen Health-Service). gRPC ist ein bewusster Teilausschnitt,
keine vollständige Parität — neue Endpunkte landen zuerst in REST. Beide Leitungen durchlaufen die
**gleiche** Kette `authenticate → resolve-tenant → authorize` und mappen Fehler identisch, sodass ein
Not-Found auf beiden Leitungen nicht von einer mandantenübergreifenden Ressource unterscheidbar ist.

Die REST-Fläche wird als **OpenAPI-3.1-Vertrag** veröffentlicht, der in der
[API-Referenz](/reference/api/) direkt aus dem verfassten Schema des Produkts gerendert wird. Dieses
Dokument ist der maßgebliche Vertrag für die stabile Kernfläche; die Modulrouten werden separat als
**Beta**-Dokument veröffentlicht — in der [Modulrouten-Referenz](/reference/api-beta/)
(siehe die ehrlichen Grenzen unten). Dieselbe Funktionalität ist auch
vom Terminal aus steuerbar — siehe die [CLI-Referenz](/de/reference/cli/) —, weil die CLI die Engine ist,
kein Wrapper darüber.

## Authentifizierung und die Leitung

Die Authentifizierung erfolgt über **opake serverseitige Bearer-Tokens**, nicht JWT.
Ein Token ist zweck-präfigiert
(Session vs. API-Key); der Server persistiert nur einen öffentlichen Selektor und einen SHA-256 des
Secrets und vergleicht das Secret in konstanter Zeit. Die Konsequenzen, die für einen
Manage-as-Code-Workflow zählen: Tokens sind **sofort widerrufbar**, tragen **keine Claims oder
Secrets** und fügen keine Crypto-Parsing-Angriffsfläche hinzu. Ein API-Token ist an ein
`(tenant, role)` gebunden oder ist ein ungebundenes Credential auf System-Ebene; eine Anfrage, deren
Tenant-Header einem gebundenen Token widerspricht, wird abgewiesen, niemals still ausgeweitet.

## Manage-as-Code: der Terraform-Provider

Der Provider `terraform-provider-olivares` ist ein **separates Go-Modul** und ein reiner
REST-Client — er importiert niemals den Engine-Kern oder das Connector-SDK, was den großen
Abhängigkeitsbaum des Providers aus der Supply-Chain des Kerns heraushält. Konfiguriert mit einem
Endpunkt, einem sensiblen API-Token und einem optionalen Tenant verwaltet er eine bewusst kleine,
deklarierte Menge von Objekten:

| Art | Name | Verwaltet |
|---|---|---|
| resource | `olivares_agent` | die Katalog-Definition eines Agenten (volles CRUD + Import) |
| resource | `olivares_policy` | eine Governance-Policy-Deklaration |
| resource | `olivares_agent_identity_binding` | die Bindung eines Agenten an eine non-human Identity |
| resource | `olivares_deployment` | eine Deployment-**Definition** (Soll-Zustand, deklarativ) |
| data source | `olivares_policies` / `olivares_identities` | read-only Sichten auf den gesteuerten Roster |
| data source | `olivares_access_edges` | die R/RW-Access-Map und ihr Permitted-vs-Observed-Drift |
| data source | `olivares_deployment` / `olivares_server_info` | eine Deployment-Definition; Engine-Metadaten |

Dies sind die **einzigen** Resources und Data-Sources, die der Provider bedient. Das Deklarieren eines
`olivares_deployment` zeichnet einen Soll-Zustand im Control Plane auf — es berührt die Infrastruktur
**nicht**; der Apply-Pfad gehört zu [Modul VII](/de/reference/modules/vii-deploy/) und ist eine
deny-closed Naht.

:::caution[Ehrliche Grenzen]
- **`olivares_deployment` deklariert; es deployt nicht.** Die Resource schreibt eine
  Deployment-*Definition* über die Routen von Modul VII. Ein Live-`apply`/`retire` gegen reale
  Infrastruktur ist eine **deny-closed Naht, die `503` zurückgibt**, bis ein Betreiber einen Executor
  bereitstellt — ein Deployment in HCL zu deklarieren mutiert niemals Ihr Estate.
- **Die stabile OpenAPI ist nicht die ganze Leitung.** Die stabile Kernfläche steht im
  veröffentlichten Vertrag (`/openapi.json`); die Modulrouten (etwa die Access-Map- und Drift-Reads
  sowie die Governance- und Deployment-Routen, die der Provider nutzt) werden als separates
  **Beta**-Dokument veröffentlicht (`/openapi.beta.json`, die
  [Modulrouten-Referenz](/reference/api-beta/)). Ihre Formen auf Feldebene leben in den typisierten
  Interfaces des Produkts, nicht im stabilen Schema.
- **gRPC ist ein eingefrorener Teilausschnitt, nicht die volle API.** Es spiegelt einige
  Lese-/Erstell- und Audit-Operationen für First-Party-Automatisierung; gehen Sie nicht davon aus,
  dass ein Endpunkt auf gRPC existiert, weil er auf REST existiert.
- **Die Fläche des Providers ist absichtlich klein.** Vier Resources und fünf Data-Sources — nicht
  die gesamte API als IaC. Alles außerhalb dieser Menge wird heute über REST/CLI verwaltet, nicht in
  HCL deklariert.
- **Die Lizenz ist Attestierung, niemals ein Feature-Gate.** Das Produkt ist unter seiner Lizenz
  vollständig; die Offline-Lizenzprüfung zeichnet nur den Inhaber und Status auf und deaktiviert,
  degradiert oder blockiert niemals eine API-Anfrage oder einen Boot.
:::

## Secure by default

Die bedienende Engine ist secure-by-default: TLS ist an (ein selbstsigniertes Zertifikat wird beim
ersten Boot generiert, falls keines bereitgestellt ist), der Bind defaultet auf localhost, und lokal
zu lauschen ist **keine** Ausnahme von der Autorisierung. Eine frische Installation hat keine
Credentials — sie prägt ein einmaliges Setup-Token nach stdout und weist jeden geschützten Endpunkt
ab, bis der erste Administrator erstellt ist. Audit ist append-only und hash-chained, mit
Ed25519-signierten Checkpoints, die das Umschreiben von Historie vor einem Checkpoint
kryptografisch erkennbar machen.

## Die Eventing-Plattform (die ausgehende Hälfte von Modul XIX)

Seitdem die Eventing-Plattform ausgeliefert wurde (`modules/eventing`), umfasst die Fläche von
Modul XIX auch **mandanten-self-service Event-Subscriptions**: typisierte Subscriptions über den
Katalog der Bus-Events (`edge.observed`, `cost.sampled`, `finding.reported`,
`audit.recorded`, …) mit **durabler At-least-once-Zustellung** — Retries mit Backoff,
eine Dead-Letter-Queue und Replay ab einem Cursor — an einen HMAC-signierten Webhook oder einen
[SIEM-Sink](/de/how-to/cookbook/push-to-siem/). Das Notify-Modul
([XV](/de/reference/modules/xv-notify/)) bleibt der Alert-*Router* zu betreiberbereitgestellten Zielen;
Eventing ist die integratorseitige Plattform.
Ein begleitender read-only **Posture-Export** (`modules/posture-export`) lässt einen Control-Tower
die Ground-Truth-Posture des Produkts pollen — Access-Graph, Drift, Inventar,
Findings — nur als Refs/Hashes/Relationen, wobei der Export selbst auditiert wird.

## Verwandt

- [API-Referenz](/reference/api/) — der gerenderte OpenAPI-3.1-Vertrag für die Kernfläche.
- [API-Stabilitätspolitik](/de/reference/api-stability/) — Versionierung, Deprecation-/Sunset-Signalisierung und Support-Fenster für diese Fläche.
- [Die Client-SDKs nutzen](/de/how-to/use-the-client-sdks/) — die First-Party-Clients für Go/Python/TypeScript.
- [CLI-Referenz](/de/reference/cli/) — dieselbe Funktionalität vom `olivares`-Binary.
- [Den Control Plane als Code verwalten](/de/how-to/manage-as-code/) — die Anleitung zum Terraform-Provider.
- [Modul VII — Deployment](/de/reference/modules/vii-deploy/) — wo `olivares_deployment` aktuiert (die `503`-Naht).
- [Modulkatalog](/de/reference/modules/overview/) — die Trennung von Govern/Observe vs. Actuate.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — was heute aktuiert und was nicht.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine-Schicht, auf der diese Fläche sitzt.
