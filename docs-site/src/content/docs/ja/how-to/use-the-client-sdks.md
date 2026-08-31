---
title: "クライアント SDK を使う (Go, Java, Python, TypeScript)"
description: >-
  ファーストパーティの Go、Java、Python、TypeScript クライアントで control plane の REST API
  を呼び出す — opaque トークン認証、テナンシー、ページネーション、リトライ動作、
  非推奨シグナリングを代わりに処理してくれる。
---

control plane は、公開された REST 契約 (`/v1`) に対する 4 つの **ファーストパーティ
クライアント SDK** を同梱しています。これらは、エンジンが提供し
[API リファレンス](/reference/api/) がレンダリングするのと同じ OpenAPI ドキュメントから
生成されます:

| SDK | パッケージ | ランタイム要件 |
|---|---|---|
| Go | `github.com/olivaresai/olivares/clients/go` (パッケージ `olivares`) | stdlib のみ |
| Java | `ai.olivares:olivares-client` (パッケージ `ai.olivares.client`) | Java ≥ 17、JDK の `java.net.http` のみ |
| Python | `olivares-client` (import `olivares_client`) | Python ≥ 3.10、stdlib のみ |
| TypeScript | `@olivaresai/client` | グローバルの `fetch` (Node ≥ 20、Deno、ブラウザ) |

:::note[配布状況]
SDK は製品リポジトリの `clients/` 配下に存在し、それとともにバージョン管理されます。
公開レジストリ (pkg.go.dev、Maven Central、PyPI、npm) への公開は、製品の一般公開とともに行われます —
それまでは、リポジトリから利用してください (上記の Go モジュールパス、
`mvn -f clients/java install`、`pip install ./clients/python`、`npm install ./clients/typescript`)。
:::

4 つすべてが 1 つの設計を共有します。手書きのコアが契約上の動作を実装します — opaque
ベアラートークン (`olvs_` セッション / `olvk_` API キー)、`X-Olivares-Tenant` ヘッダー、
API の単一のエラーエンベロープ、カーソルページネーション (`items`/`cursor`/`has_more`)、
レート制限された呼び出しに対して `Retry-After` を尊重するリトライ (429 は常に。503 は
冪等な GET のみ)、そして [安定性ポリシー](/ja/reference/api-stability/) の非推奨ヘッダーを
エンドポイントごとに 1 回だけ表面化させること、です。その上に、公開された操作ごとに
生成されたメソッドが乗ります。これはルートにちなんで命名されます
(`GET /v1/agents` → `GetV1Agents` / `get_v1_agents` / `getV1Agents`)。リクエスト/レスポンス
ボディは汎用の JSON です — 公開された契約は意図的にボディを opaque に保ちます。

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

エラーは `*olivares.APIError` です (`errors.As` でマッチします)。`Code` は契約の安定した
エラーコード (`not_found`、`forbidden`、`rate_limited`、…) を保持します。非推奨シグナルは
エンドポイントごとに 1 回、`slog` の警告として、あるいは独自の
`WithDeprecationHandler` コールバックとして届きます。

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

エラーは `OlivaresApiException` をスローし、`getStatus()`、`getCode()`、
`getApiMessage()`、`getRequestId()` を持ちます。非推奨シグナルはエンドポイントごとに
1 回、`onDeprecation` コールバックとして届きます。コアは依存ゼロで、JDK の
`java.net.http` と手書きの JSON コーデックだけを使います。

## Python

```python
from olivares_client import Client, APIError

c = Client("https://olivares.example:8443", token="olvk_…", tenant="9be0…")

info = c.get_v1_server_info()
for agent in c.paginate("/v1/agents", limit="100"):
    print(agent["id"])
```

エラーは `.status`、`.code`、`.message`、`.request_id` を伴って `APIError` を送出します。
非推奨のエンドポイントは、エンドポイントごとに 1 つの `DeprecationWarning` を発します
(あるいは独自の `on_deprecation=` コールバック)。エンジンのすぐ使える自己署名 TLS に
対しては、ラボでは `verify=False` を渡してください — 本番では本物の CA をピン留めして
ください。

## TypeScript

```ts
import { Client, APIError } from "@olivaresai/client";

const c = new Client({ endpoint: "https://olivares.example:8443", token: "olvk_…" });

const info = await c.getV1ServerInfo();
for await (const agent of c.paginate("/v1/agents", { query: { limit: "100" } })) {
  console.log(agent.id);
}
```

エラーは `APIError` のインスタンスです。非推奨シグナルは、エンドポイントごとに 1 回、
`console.warn` または独自の `onDeprecation` コールバック経由で届きます。

## バージョニングと再生成

各 SDK は `API_VERSION` (生成元となった API 契約のメジャー) と `SPEC_HASH` (正確な
OpenAPI スナップショットの SHA-256) をエクスポートします — Go では `APIVersion` と
`SpecHash` です。操作レイヤーは `task sdk:generate` によって再生成され、`task sdk:check`
によってドリフトがチェックされます。これは pre-push ゲートと CI で実行されます — 契約の
変更が、出荷されたクライアントから黙って乖離することはできません。SDK が触れるすべての
ものに対する互換性のコミットメントは [API 安定性ポリシー](/ja/reference/api-stability/) です。

## 関連

- [API の安定性、バージョニング、非推奨と廃止 (sunset)](/ja/reference/api-stability/)
- [REST API リファレンス](/reference/api/)
- [control plane をコードとして管理する](/ja/how-to/manage-as-code/) — プログラムによる
  呼び出しの代わりに宣言的な管理を行うための Terraform プロバイダー。
