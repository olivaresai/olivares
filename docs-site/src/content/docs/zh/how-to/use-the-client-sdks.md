---
title: "使用客户端 SDK（Go、Java、Python、TypeScript）"
description: >-
  使用官方的 Go、Java、Python 和 TypeScript 客户端调用控制平面 REST API — 不透明令牌认证、租户、分页、重试行为
  和弃用信号都已为你处理好。
---

控制平面（control plane）为其已发布的 REST 契约（`/v1`）提供四个 **官方客户端 SDK**，它们由引擎所服务、并由 [API 参考](/reference/api/) 渲染的同一份 OpenAPI 文档生成：

| SDK | 包 | 运行时要求 |
|---|---|---|
| Go | `github.com/olivaresai/olivares/clients/go`（包 `olivares`） | 仅需标准库 |
| Java | `ai.olivares:olivares-client`（包 `ai.olivares.client`） | Java ≥ 17，仅需 JDK 的 `java.net.http` |
| Python | `olivares-client`（导入 `olivares_client`） | Python ≥ 3.10，仅需标准库 |
| TypeScript | `@olivaresai/client` | 全局 `fetch`（Node ≥ 20、Deno、浏览器） |

:::note[分发状态]
这些 SDK 位于产品仓库的 `clients/` 下，并与其一同版本化。向公共注册表（pkg.go.dev、Maven Central、PyPI、npm）发布将随公开发布一同进行 — 在此之前，请从仓库消费它们（上面的 Go 模块路径、`mvn -f clients/java install`、`pip install ./clients/python`、`npm install ./clients/typescript`）。
:::

四者共享同一套设计。一个手写的核心实现了契约规定的行为 — 不透明的 bearer 令牌（`olvs_` 会话 / `olvk_` API 密钥）、`X-Olivares-Tenant` 头、API 的单一错误信封、游标分页（`items`/`cursor`/`has_more`）、对受限流调用尊重 `Retry-After` 的重试（429 总是重试；503 仅对幂等的 GET 重试），以及[稳定性策略](/zh/reference/api-stability/)的弃用头（每个端点呈现一次）。其上则是按每个已发布操作生成的方法，以路由命名（`GET /v1/agents` → `GetV1Agents` / `get_v1_agents` /
`getV1Agents`），请求/响应体为通用 JSON — 已发布的契约刻意保持响应体不透明。

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

错误为 `*olivares.APIError`（用 `errors.As` 匹配）；`Code` 携带契约稳定的错误码（`not_found`、`forbidden`、`rate_limited`、…）。弃用信号每个端点到达一次，形式为 `slog` 告警，或你自己的
`WithDeprecationHandler` 回调。

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

错误会抛出 `OlivaresApiException`，带有 `getStatus()`、`getCode()`、`getApiMessage()`
和 `getRequestId()`。弃用信号每个端点到达一次，通过 `onDeprecation` 回调送达。其核心
零依赖 — 仅用 JDK 的 `java.net.http` 和一个手写的 JSON 编解码器。

## Python

```python
from olivares_client import Client, APIError

c = Client("https://olivares.example:8443", token="olvk_…", tenant="9be0…")

info = c.get_v1_server_info()
for agent in c.paginate("/v1/agents", limit="100"):
    print(agent["id"])
```

错误会抛出 `APIError`，带有 `.status`、`.code`、`.message`、`.request_id`。已弃用的端点每个端点发出一次 `DeprecationWarning`（或你的
`on_deprecation=` 回调）。对于引擎开箱即用的自签名 TLS，在实验环境中传入 `verify=False` — 生产环境请固定一个真实的 CA。

## TypeScript

```ts
import { Client, APIError } from "@olivaresai/client";

const c = new Client({ endpoint: "https://olivares.example:8443", token: "olvk_…" });

const info = await c.getV1ServerInfo();
for await (const agent of c.paginate("/v1/agents", { query: { limit: "100" } })) {
  console.log(agent.id);
}
```

错误为 `APIError` 实例；弃用信号每个端点到达一次，通过 `console.warn` 或你的 `onDeprecation` 回调。

## 版本管理与重新生成

每个 SDK 都导出 `API_VERSION`（它由其生成的 API 契约主版本）和
`SPEC_HASH`（确切 OpenAPI 快照的 SHA-256）— 在 Go 中为 `APIVersion` 和
`SpecHash`。操作层由 `task sdk:generate` 重新生成，并由 `task sdk:check` 做偏移检查，后者运行于 pre-push 门禁和 CI 中 — 契约变更无法静默地与已发布的客户端产生偏离。SDK 所触及的一切，其兼容性承诺都由
[API 稳定性策略](/zh/reference/api-stability/)规定。

## 相关内容

- [API 稳定性、版本管理、弃用与日落](/zh/reference/api-stability/)
- [REST API 参考](/reference/api/)
- [以代码方式管理控制平面](/zh/how-to/manage-as-code/) — Terraform
  provider，用于声明式管理而非编程式调用。
