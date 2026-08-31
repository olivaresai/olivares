---
title: "Использование клиентских SDK (Go, Java, Python, TypeScript)"
description: >-
  Вызывайте REST API control plane с помощью официальных клиентов Go, Java, Python и
  TypeScript — аутентификация по непрозрачному токену, многоарендность,
  пагинация, поведение повторов и сигнализация об устаревании обрабатываются за вас.
---

Control plane поставляется с четырьмя **официальными клиентскими SDK** для своего
опубликованного REST-контракта (`/v1`), сгенерированными из того же документа
OpenAPI, который движок отдаёт, а [справочник API](/reference/api/) отображает:

| SDK | Пакет | Требования рантайма |
|---|---|---|
| Go | `github.com/olivaresai/olivares/clients/go` (пакет `olivares`) | только stdlib |
| Java | `ai.olivares:olivares-client` (пакет `ai.olivares.client`) | Java ≥ 17, только `java.net.http` из JDK |
| Python | `olivares-client` (импорт `olivares_client`) | Python ≥ 3.10, только stdlib |
| TypeScript | `@olivaresai/client` | глобальный `fetch` (Node ≥ 20, Deno, браузеры) |

:::note[Статус распространения]
SDK находятся в репозитории продукта в каталоге `clients/` и версионируются вместе
с ним. Публикация в публичные реестры (pkg.go.dev, Maven Central, PyPI, npm)
происходит вместе с публичным релизом — до тех пор используйте их из репозитория
(путь модуля Go выше, `mvn -f clients/java install`, `pip install ./clients/python`,
`npm install ./clients/typescript`).
:::

Все четыре используют один дизайн. Написанное вручную ядро реализует контрактное
поведение — непрозрачные bearer-токены (сессия `olvs_` / API-ключ `olvk_`),
заголовок `X-Olivares-Tenant`, единый конверт ошибок API, курсорную пагинацию
(`items`/`cursor`/`has_more`), повторы, которые учитывают `Retry-After` для
вызовов с ограничением частоты (429 всегда; 503 только для идемпотентных GET), и
заголовки устаревания из [политики стабильности](/ru/reference/api-stability/),
выводимые один раз на конечную точку. Поверх этого расположен сгенерированный метод
на каждую опубликованную операцию, названный по маршруту (`GET /v1/agents` →
`GetV1Agents` / `get_v1_agents` / `getV1Agents`), с телами запроса/ответа в виде
обобщённого JSON — опубликованный контракт намеренно держит тела непрозрачными.

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

Ошибки представляют собой `*olivares.APIError` (сопоставляйте через `errors.As`);
`Code` несёт стабильные коды ошибок контракта (`not_found`, `forbidden`,
`rate_limited`, …). Сигналы устаревания приходят один раз на конечную точку как
предупреждение `slog` или ваш собственный коллбэк `WithDeprecationHandler`.

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

Ошибки выбрасывают `OlivaresApiException` с `getStatus()`, `getCode()`,
`getApiMessage()` и `getRequestId()`. Сигналы устаревания приходят один раз на
конечную точку через коллбэк `onDeprecation`. Ядро без зависимостей — только
`java.net.http` из JDK и написанный вручную JSON-кодек.

## Python

```python
from olivares_client import Client, APIError

c = Client("https://olivares.example:8443", token="olvk_…", tenant="9be0…")

info = c.get_v1_server_info()
for agent in c.paginate("/v1/agents", limit="100"):
    print(agent["id"])
```

Ошибки порождают `APIError` с `.status`, `.code`, `.message`, `.request_id`.
Устаревшие конечные точки выдают одно `DeprecationWarning` на конечную точку (или
ваш коллбэк `on_deprecation=`). Для готового самоподписанного TLS движка
передавайте `verify=False` в лабораторных условиях — в продакшене закрепляйте
реальный CA.

## TypeScript

```ts
import { Client, APIError } from "@olivaresai/client";

const c = new Client({ endpoint: "https://olivares.example:8443", token: "olvk_…" });

const info = await c.getV1ServerInfo();
for await (const agent of c.paginate("/v1/agents", { query: { limit: "100" } })) {
  console.log(agent.id);
}
```

Ошибки — это экземпляры `APIError`; сигналы устаревания приходят один раз на
конечную точку через `console.warn` или ваш коллбэк `onDeprecation`.

## Версионирование и перегенерация

Каждый SDK экспортирует `API_VERSION` (мажорная версия контракта API, из которой он
был сгенерирован) и `SPEC_HASH` (SHA-256 точного снимка OpenAPI) — `APIVersion` и
`SpecHash` в Go. Слои операций перегенерируются через `task sdk:generate` и
проверяются на расхождение через `task sdk:check`, который запускается в пре-пуш
гейте и в CI — изменение контракта не может молча разойтись с поставляемыми
клиентами. Обязательство по совместимости для всего, чего касаются SDK, — это
[политика стабильности API](/ru/reference/api-stability/).

## Связанное

- [Стабильность API, версионирование, устаревание и вывод из эксплуатации](/ru/reference/api-stability/)
- [Справочник REST API](/reference/api/)
- [Управление control plane как кодом](/ru/how-to/manage-as-code/) — провайдер
  Terraform для декларативного управления вместо программных вызовов.
