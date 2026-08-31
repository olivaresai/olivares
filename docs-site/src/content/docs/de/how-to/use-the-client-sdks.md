---
title: "Die Client-SDKs verwenden (Go, Java, Python, TypeScript)"
description: >-
  Rufen Sie die REST-API der control plane mit den hauseigenen Go-, Java-, Python- und
  TypeScript-Clients auf — Authentifizierung mit opaken Tokens, Mandantenfähigkeit,
  Pagination, Retry-Verhalten und Deprecation-Signalisierung werden für Sie übernommen.
---

Die control plane liefert vier **hauseigene Client-SDKs** für ihren veröffentlichten
REST-Vertrag (`/v1`), generiert aus demselben OpenAPI-Dokument, das die Engine ausliefert
und das die [API-Referenz](/reference/api/) rendert:

| SDK | Paket | Laufzeitanforderungen |
|---|---|---|
| Go | `github.com/olivaresai/olivares/clients/go` (package `olivares`) | nur stdlib |
| Java | `ai.olivares:olivares-client` (package `ai.olivares.client`) | Java ≥ 17, nur `java.net.http` des JDK |
| Python | `olivares-client` (import `olivares_client`) | Python ≥ 3.10, nur stdlib |
| TypeScript | `@olivaresai/client` | globales `fetch` (Node ≥ 20, Deno, Browser) |

:::note[Distributionsstatus]
Die SDKs liegen im Produkt-Repository unter `clients/` und werden mit ihm versioniert. Die
Veröffentlichung in die öffentlichen Registries (pkg.go.dev, Maven Central, PyPI, npm)
erfolgt mit dem öffentlichen Release — bis dahin beziehen Sie sie aus dem Repo
(Go-Modulpfad oben, `mvn -f clients/java install`, `pip install ./clients/python`,
`npm install ./clients/typescript`).
:::

Alle vier teilen ein Design. Ein handgeschriebener Kern implementiert das vertragliche
Verhalten — opake Bearer-Tokens (`olvs_` Session / `olvk_` API-Schlüssel), der
`X-Olivares-Tenant`-Header, die einheitliche Fehler-Hülle der API, Cursor-Pagination
(`items`/`cursor`/`has_more`), Retries, die `Retry-After` bei ratenbegrenzten Aufrufen
beachten (429 immer; 503 nur für idempotente GETs), und die Deprecation-Header der
[Stabilitätsrichtlinie](/de/reference/api-stability/), die einmal pro Endpunkt
herausgereicht werden. Darüber sitzt eine generierte Methode pro veröffentlichter
Operation, benannt nach der Route (`GET /v1/agents` → `GetV1Agents` / `get_v1_agents` /
`getV1Agents`), mit Request-/Response-Bodies als generisches JSON — der veröffentlichte
Vertrag hält die Bodies bewusst opak.

## Go

```go
import olivares "github.com/olivaresai/olivares/clients/go"

c, err := olivares.New("https://olivares.example:8443", os.Getenv("OLIVARES_API_TOKEN"),
    olivares.WithTenant("9be0…"))
if err != nil { … }

info, err := c.GetV1ServerInfo(ctx)

for agent, err := range c.ListPages(ctx, "/v1/agents", olivares.Query("limit", "100")) {
    if err != nil { … }
    fmt.Println(agent["id"])
}
```

Fehler sind `*olivares.APIError` (mit `errors.As` abgleichen); `Code` trägt die stabilen
Fehlercodes des Vertrags (`not_found`, `forbidden`, `rate_limited`, …).
Deprecation-Signale treffen einmal pro Endpunkt als `slog`-Warnung ein, oder über Ihren
eigenen `WithDeprecationHandler`-Callback.

## Java

```java
import ai.olivares.client.Client;
import ai.olivares.client.ClientOptions;
import ai.olivares.client.OlivaresApiException;
import ai.olivares.client.RequestOptions;

Client c = new Client(ClientOptions.builder()
    .endpoint("https://olivares.example:8443")
    .token(System.getenv("OLIVARES_API_TOKEN"))
    .tenant("9be0…")
    .build());

var info = c.getV1ServerInfo();

for (var agent : c.paginate("/v1/agents",
        RequestOptions.builder().query("limit", "100").build())) {
    System.out.println(agent.get("id"));
}
```

Fehler werfen `OlivaresApiException` mit `getStatus()`, `getCode()`,
`getApiMessage()` und `getRequestId()`. Deprecation-Signale treffen einmal pro
Endpunkt über den `onDeprecation`-Callback ein. Der Kern ist abhängigkeitsfrei — nur
das `java.net.http` des JDK und ein handgeschriebener JSON-Codec.

## Python

```python
from olivares_client import Client, APIError

c = Client("https://olivares.example:8443", token="olvk_…", tenant="9be0…")

info = c.get_v1_server_info()
for agent in c.paginate("/v1/agents", limit="100"):
    print(agent["id"])
```

Fehler lösen `APIError` aus, mit `.status`, `.code`, `.message`, `.request_id`. Veraltete
Endpunkte geben eine `DeprecationWarning` pro Endpunkt aus (oder Ihren
`on_deprecation=`-Callback). Für das standardmäßig selbstsignierte TLS der Engine geben Sie
im Labor `verify=False` an — pinnen Sie in der Produktion eine echte CA.

## TypeScript

```ts
import { Client, APIError } from "@olivaresai/client";

const c = new Client({ endpoint: "https://olivares.example:8443", token: "olvk_…" });

const info = await c.getV1ServerInfo();
for await (const agent of c.paginate("/v1/agents", { query: { limit: "100" } })) {
  console.log(agent.id);
}
```

Fehler sind `APIError`-Instanzen; Deprecation-Signale treffen einmal pro Endpunkt über
`console.warn` oder Ihren `onDeprecation`-Callback ein.

## Versionierung und Neugenerierung

Jedes SDK exportiert `API_VERSION` (die Major-Version des API-Vertrags, aus der es
generiert wurde) und `SPEC_HASH` (das SHA-256 des exakten OpenAPI-Snapshots) — `APIVersion`
und `SpecHash` in Go. Die Operationsschichten werden durch `task sdk:generate` neu
generiert und durch `task sdk:check` auf Drift geprüft, was im Pre-Push-Gate und in CI
läuft — eine Vertragsänderung kann nicht stillschweigend von den ausgelieferten Clients
abweichen. Die Kompatibilitätszusage für alles, was die SDKs berühren, ist die
[API-Stabilitätsrichtlinie](/de/reference/api-stability/).

## Verwandt

- [API-Stabilität, Versionierung, Deprecation & Sunset](/de/reference/api-stability/)
- [REST-API-Referenz](/reference/api/)
- [Die control plane als Code verwalten](/de/how-to/manage-as-code/) — der
  Terraform-Provider, für deklarative Verwaltung statt programmatischer Aufrufe.
