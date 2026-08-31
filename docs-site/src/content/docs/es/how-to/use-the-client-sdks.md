---
title: "Usa los SDK de cliente (Go, Java, Python, TypeScript)"
description: >-
  Llama a la API REST del control plane con los clientes de primera parte de Go,
  Java, Python y TypeScript — autenticación con token opaco, tenancy, paginación,
  comportamiento de reintentos y señalización de obsolescencia resueltos por ti.
---

El control plane incluye cuatro **SDK de cliente de primera parte** para su contrato
REST publicado (`/v1`), generados a partir del mismo documento OpenAPI que el motor
sirve y que la [referencia de la API](/reference/api/) renderiza:

| SDK | Paquete | Requisitos de runtime |
|---|---|---|
| Go | `github.com/olivaresai/olivares/clients/go` (paquete `olivares`) | solo stdlib |
| Java | `ai.olivares:olivares-client` (paquete `ai.olivares.client`) | Java ≥ 17, solo `java.net.http` del JDK |
| Python | `olivares-client` (import `olivares_client`) | Python ≥ 3.10, solo stdlib |
| TypeScript | `@olivaresai/client` | `fetch` global (Node ≥ 20, Deno, navegadores) |

:::note[Estado de distribución]
Los SDK viven en el repositorio del producto bajo `clients/` y se versionan
con él. La publicación en los registros públicos (pkg.go.dev, Maven Central, PyPI,
npm) ocurre con la publicación pública — hasta entonces, consúmelos desde el repo
(ruta del módulo Go de arriba, `mvn -f clients/java install`,
`pip install ./clients/python`, `npm install ./clients/typescript`).
:::

Los cuatro comparten un mismo diseño. Un núcleo escrito a mano implementa el comportamiento
contractual — tokens bearer opacos (`olvs_` de sesión / `olvk_` de clave de API), la
cabecera `X-Olivares-Tenant`, el sobre de error único de la API, la paginación por cursor
(`items`/`cursor`/`has_more`), reintentos que respetan `Retry-After` para las llamadas
limitadas por rate (429 siempre; 503 solo para GET idempotentes), y las cabeceras de
obsolescencia de la [política de estabilidad](/es/reference/api-stability/) expuestas
una vez por endpoint. Encima se sitúa un método generado por operación publicada,
nombrado según la ruta (`GET /v1/agents` → `GetV1Agents` / `get_v1_agents` /
`getV1Agents`), con los cuerpos de petición/respuesta como JSON genérico — el contrato
publicado mantiene los cuerpos opacos deliberadamente.

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

Los errores son `*olivares.APIError` (coincide con `errors.As`); `Code` lleva los
códigos de error estables del contrato (`not_found`, `forbidden`, `rate_limited`, …).
Las señales de obsolescencia llegan una vez por endpoint como un aviso de `slog`, o tu propio
callback `WithDeprecationHandler`.

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

Los errores lanzan `OlivaresApiException` con `getStatus()`, `getCode()`,
`getApiMessage()` y `getRequestId()`. Las señales de obsolescencia llegan una vez
por endpoint vía el callback `onDeprecation`. El núcleo es sin dependencias — solo
el `java.net.http` del JDK y un códec JSON escrito a mano.

## Python

```python
from olivares_client import Client, APIError

c = Client("https://olivares.example:8443", token="olvk_…", tenant="9be0…")

info = c.get_v1_server_info()
for agent in c.paginate("/v1/agents", limit="100"):
    print(agent["id"])
```

Los errores lanzan `APIError` con `.status`, `.code`, `.message`, `.request_id`.
Los endpoints obsoletos emiten un `DeprecationWarning` por endpoint (o tu
callback `on_deprecation=`). Para el TLS autofirmado que el motor trae de fábrica,
pasa `verify=False` en laboratorios — fija una CA real en producción.

## TypeScript

```ts
import { Client, APIError } from "@olivaresai/client";

const c = new Client({ endpoint: "https://olivares.example:8443", token: "olvk_…" });

const info = await c.getV1ServerInfo();
for await (const agent of c.paginate("/v1/agents", { query: { limit: "100" } })) {
  console.log(agent.id);
}
```

Los errores son instancias de `APIError`; las señales de obsolescencia llegan una vez por endpoint
vía `console.warn` o tu callback `onDeprecation`.

## Versionado y regeneración

Cada SDK exporta `API_VERSION` (el major del contrato de la API a partir del que se generó)
y `SPEC_HASH` (el SHA-256 del snapshot exacto de OpenAPI) — `APIVersion` y
`SpecHash` en Go. Las capas de operaciones se regeneran con `task sdk:generate`
y se comprueba su drift con `task sdk:check`, que se ejecuta en el gate de pre-push y en
CI — un cambio del contrato no puede divergir en silencio de los clientes publicados. El
compromiso de compatibilidad para todo lo que tocan los SDK es la
[política de estabilidad de la API](/es/reference/api-stability/).

## Relacionado

- [Estabilidad, versionado, obsolescencia y retirada de la API](/es/reference/api-stability/)
- [Referencia de la API REST](/reference/api/)
- [Gestiona el control plane como código](/es/how-to/manage-as-code/) — el proveedor
  de Terraform, para gestión declarativa en lugar de llamadas programáticas.
