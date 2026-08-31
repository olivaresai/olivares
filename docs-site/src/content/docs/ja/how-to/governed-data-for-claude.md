---
title: "Claude 向けのガバナンス対象データ"
description: "Drive または S3 のコンテンツを、セマンティック KB と MCP retrieval endpoint を通じて Claude Code に公開し、ID、clearance、ACL、source scope でガバナンスします。"
sidebar:
  order: 7
---

この手順により、Olivares を AI gateway にすることなく、Claude Code から**自組織の**
Google Drive または S3 コンテンツに対して質問できるようになります。コントロール
プレーンはコンテンツをガバナンス対象ナレッジベースへ取り込み、文書ごとに来歴を
記録し、MCP 経由では retrieval tool だけを公開します。

| デフォルト | 意味 |
|---|---|
| セマンティック KB | `embed_policy=model_backed`。ingest の前に `/status` が `retrieval_semantic=true` を示している必要があります。 |
| 明示的な fallback | セマンティック embedder が設定されていない場合、KB の作成/ingest は、local-hash vector をセマンティックであるかのように装うのではなく拒否されます。 |
| ACL 対応 guard | 要求元の agent は、十分な `attr_clearance` と一致する group ACL を持つ、binding 済みの ID に解決されなければなりません。 |
| Source scope | KB を Claude Code agent に binding します。scope 外の subject はデニークローズドで拒否されます。 |
| 正確な live mode | live connector の応答には `source_mode=live` が含まれます。静的 export は `source_mode=export` のままで、live として提示されることは決してありません。 |

## 1. source credential を保存する

live source の credential は runtime の secret store に保存します。source config では
inline に記述せず、`store:<name>` として参照します。

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name s3/prod-runbooks-read \
  --value-file /run/secrets/s3-prod-runbooks-read
```

Google Drive の場合は、読み取り専用の Drive アクセスにデプロイが使用する OAuth
bearer/refresh 情報を、別の secret 名で保存してください。

## 2. ガバナンス対象 RAG config を生成する

S3 の場合:

```sh
olivares quickstart governed-rag \
  --data-dir /var/lib/olivares \
  --tenant-id ten_... \
  --source s3 \
  --source-name prod-runbooks-live \
  --bucket prod-runbooks \
  --prefix claude/ \
  --credential-ref store:s3/prod-runbooks-read \
  --mcp-issuer https://idp.example.com/ \
  --mcp-jwks-url https://idp.example.com/.well-known/jwks.json
```

Google Drive の場合は、`--source gdrive --drive-id <shared-drive-id>` と Drive credential
の参照を使用します。

このコマンドは次のファイルを書き出します。

| ファイル | 用途 |
|---|---|
| `sources.json` | `documents[]` の下に `mode=live` でコンテンツ source を登録します。 |
| `agent-gateway.json` | `retrieval.enabled=true` として MCP resource server を有効にします。 |
| `bootstrap-after-login.sh` | セマンティック KB を作成し、live source を ingest し、agent を binding して source-scope binding を追加します。 |

コマンドが `retrieval_semantic=false` と警告した場合は、先に
`OLIVARES_EMBEDDINGS_*` を設定してください。model-backed KB は、local-hash fallback
しかない状態での ingest を意図的に拒否します。

## 3. 生成した config で起動する

```sh
OLIVARES_SOURCES_CONFIG=/var/lib/olivares/quickstart/governed-rag/sources.json \
OLIVARES_AGENT_GATEWAY_CONFIG=/var/lib/olivares/quickstart/governed-rag/agent-gateway.json \
olivares quickstart --data-dir /var/lib/olivares
```

新規インストールの場合は、初回のコンソール設定を完了します。その後、admin token
を使って bootstrap script を実行します。

```sh
OLIVARES_TOKEN=<admin-token> \
OLIVARES_TENANT=ten_... \
/var/lib/olivares/quickstart/governed-rag/bootstrap-after-login.sh
```

## 4. ID の前提条件

retrieval guard は roster/SCIM graph から ID の属性を読み取ります。Claude Code が
制限付きコンテンツを取得するには、binding された ID が事前に存在していなければ
なりません。

| ID 属性 | 例 |
|---|---|
| Agent token subject / `agent_ref` | `claude-code-governed` |
| binding 済み NHI ID | `agent:claude-code-governed` |
| Clearance metadata | `attr_clearance=confidential` 以上 |
| Group membership | 文書の ACL と一致する `group:engineering` |

agent に ID、clearance、または一致する group がなければ、制限付き chunk は返され
ません。agent が source scope によって KB に binding されていない場合、MCP
retrieval call はデニークローズドで拒否されます。

## 5. Claude Code を MCP に接続する

quickstart が表示する保護対象 resource URL を Claude Code に設定します。通常は次の
URL です。

```text
http://127.0.0.1:8446/mcp
```

その MCP resource server に提示する access token には、次の値が必要です。

| Claim/control | 必須値 |
|---|---|
| `iss` | `--mcp-issuer` で設定した issuer。 |
| `sub` | agent の external id。例: `claude-code-governed`。 |
| Scope | `knowledge:retrieval:read`。 |
| Audience/resource | `agent-gateway.json` に設定した MCP resource URL。 |

## 6. 検証する

リファレンス E2E demo を実行します。

```sh
task demo:governed-rag
```

この demo は、semantic status、live-source provenance、許可された scoped retrieval、
低い clearance で取得されないこと、scope 外の拒否、MCP result 内の
`source_mode=live` を確認します。

既存のデプロイでは、実際の文書も確認してください。

```sh
curl -sk "$OLIVARES_BASE_URL/v1/m/knowledge/kbs/$KB_ID/documents" \
  -H "Authorization: Bearer $OLIVARES_TOKEN" \
  -H "X-Olivares-Tenant: $OLIVARES_TENANT"
```

live ingest されたすべての文書には `source_mode: "live"` が表示されるはずです。
`export` と表示される場合、その KB は export file から ingest されたものであり、
運用者にもそのように説明する必要があります。
