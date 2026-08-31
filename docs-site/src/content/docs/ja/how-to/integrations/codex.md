---
title: Codex を統合する
description: >-
  Codex をガバナンス control plane の管理下に置きます。コネクター、managed config、
  ガバナンス対象の hook、稼働後にコンソールで確認できる内容を説明します。
---

Olivares AI は 3 つの補完的な plane を介して Codex を統合します。read-only mode では、`codex`
source が enterprise automation credential を使用して Analytics、Compliance、Audit Logs、請求済み
cost を読み取ります。`codex-managed-config` コネクターは、配備済みの system policy を inventory
して確認します。最後に `olivares codex-hook` が session と tool decision をローカル PEP に送ります。
個人の ChatGPT subscription で認証された session だけでは、enterprise API へのアクセスは得られません。

## Codex を追加する

### 前提条件

- Olivares AI enterprise tenant と、roster 操作用の AAL3 elevation を持つ superadmin account。
- enterprise ingestion では、必要な read scope を持つ platform API key または workspace access token
  と `workspace_id`。ChatGPT 経由で Codex CLI にサインインしても、コネクターの credential にはなりません。
- `/etc/codex/requirements.toml`、`/etc/codex/managed_config.toml`、trusted hook を配布するための
  host system layer への管理者アクセス。
- Codex PEP 専用の loopback socket。既定値は `127.0.0.1:8448` です。各 agent が異なる response
  format を期待するため、Claude または Grok と共有しないでください。

1. **Control console**（`/console`）を開き、**Connectors** を選択します。
2. 種別 `codex`、安定した名前、tenant、batch interval を指定して source を追加します。pilot の
   初期値として `300` 秒が妥当です。API budget と freshness objective に応じて頻度を調整します。
3. enterprise source では、secret `api_key` field に credential を入力し、`auth_mode`（`api_key`
   または `access_token`）を選択して `workspace_id` を指定します。コンソールは値を seal し、決して
   返しません。source を保存、テスト、再読み込みします。

credential なしで `codex` を追加し、ローカル catalog inventory として使用することもできます。
この mode は Analytics、Compliance、Audit Logs、Costs を照会せず、`Gather` は remote observation
を出力しません。

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="誰がアクセスでき、何を管理できるかを設定します。ユーザーのオンボーディング、SSO の接続、ワークスペースとエージェントグループの構成を行います。">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="誰がアクセスでき、何を管理できるかを設定します。ユーザーのオンボーディング、SSO の接続、ワークスペースとエージェントグループの構成を行います。">

## Codex を設定する

### 1. Read-only の enterprise source

次の設定で coverage を定義します。

| 設定 | 既定値 | 用途 |
|---|---:|---|
| `api_key` | 空 | automation credential への参照です。空の値では offline catalog のみが有効になります。 |
| `auth_mode` | `api_key` | credential を `api_key` または `access_token` として識別します。どちらも Bearer token として送信されます。 |
| `workspace_id` | 空 | workspace scope の Analytics と Compliance に必要です。 |
| `analytics` | `true` | Codex の利用と adoption。構造化 sample と finding を生成します。 |
| `compliance` | `true` | activity evidence としての Codex Compliance log。 |
| `audit` | `true` | evidence としての organization Audit Logs。 |
| `costs` | `false` | 日次の請求済み cost。Codex と無関係な支出を帰属させないよう、`project_id` とともに有効にします。 |
| `attribute_email` | `false` | `user_id` を安定した actor として保持し、attribution PII に email を使用しません。 |
| `compliance_prompt_scan` | `false` | 有効にすると、risk pattern を一時的に scan し、構造化 finding のみ保持します。 |
| `otlp_http` | `false` | port を開くため無効にしている実験的 log receiver。現在は event を count して drain しますが、session には変換しません。 |

初期統合では `otlp_http` を無効のままにします。ガバナンス対象の hook が完全な session plane を
提供します。この version で OTLP を有効にしても、そのインストールの代わりにはなりません。

CLI では credential を shell history の外に保存し、名前で参照します。

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name codex/enterprise \
  --value-file /run/secrets/codex-enterprise-token \
  --actor platform-operator \
  --reason codex-enterprise-onboarding

olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-enterprise \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --config api_key=store:codex/enterprise \
  --config auth_mode=access_token \
  --config workspace_id=ws_eng \
  --actor platform-operator \
  --reason codex-enterprise-onboarding
```

`costs=true` を有効にする場合は、`project_id=<project-id>` も追加します。この制限がない場合、
Costs API は organization scope となり、Codex と無関係な支出が混在する可能性があります。

### 2. System requirement と managed value

Olivares は 2 つの層を分離します。

- `requirements.toml` には、ユーザーが拡張できない制限を含めます。approval policy、sandbox mode、
  web search、remote control、hook trust、禁止する read、許可する MCP server が対象です。
- `managed_config.toml` には管理対象の初期値を含めます。これらは default です。不変でなければならない
  制限は `requirements.toml` に置きます。

次の policy document は有効で、write を workspace に限定しつつ、network access、web search、
remote control、MCP を既定で拒否します。

```json
{
  "requirements": {
    "allowed_approval_policies": ["untrusted", "on-request"],
    "allowed_sandbox_modes": ["read-only", "workspace-write"],
    "allowed_web_search_modes": [],
    "allow_remote_control": false,
    "allow_managed_hooks_only": true,
    "deny_read": ["~/.ssh"],
    "allowed_mcp_servers": []
  },
  "managed_config": {
    "approval_policy": "on-request",
    "sandbox_mode": "workspace-write",
    "web_search": "disabled",
    "network_access": false
  }
}
```

配布前に policy を validation し、同じ command で両方の artifact を生成します。

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate

olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --requirements-out /etc/codex/requirements.toml \
  --managed-config-out /etc/codex/managed_config.toml
```

policy に未知の enum、identity のない MCP server、無効な TOML が含まれる場合、rendering は書き込み前に
失敗します。後で live state と drift を確認するには、`codex-managed-config` 種別の source を追加登録します。
これは両方の system file を読み取りますが、変更しません。

### 3. Session hook と PEP

Codex は `$CODEX_HOME/hooks.json` から検証対象の hook を読み取ります。`command` は array ではなく
string でなければなりません。array は parse できても hook がまったく動かない場合があります。
`config.toml` の inline `[hooks]` table も、検証した version では読み取られませんでした。

```json
{
  "description": "olivares governed hooks",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PostToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ]
  }
}
```

Olivares の起動時に `OLIVARES_CODEX_HOOK_PEP_CONFIG` が有効な JSON を指していると、server が
mount されます。

```json
{
  "listen": "127.0.0.1:8448",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

各 instance は 1 つの tenant をガバナンスし、decision は Olivares に設定済みの PDP から得られます。
client は `OLIVARES_CODEX_HOOK_URL`、`OLIVARES_CODEX_HOOK_TOKEN`、
`OLIVARES_CODEX_HOOK_TENANT`、`OLIVARES_CODEX_HOOK_AGENT`、`OLIVARES_CODEX_HOOK_ORG`、
`OLIVARES_CODEX_HOOK_ACCOUNT` を使用します。これらは process と secrets manager から提供し、
`hooks.json` に埋め込まないでください。

hook を fleet control として扱う前に `allow_managed_hooks_only=true` が必要です。trust enforcement が
ない場合、Codex は event も warning も生成せずに hook を省略できます。無反応なインストールは
enforcement evidence ではありません。

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="ステップアップ認証が必要です — AAL3（ハードウェア、フィッシング耐性）">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="ステップアップ認証が必要です — AAL3（ハードウェア、フィッシング耐性）">

## CLI の使用方法

出力例は 2026 年 8 月 30 日に測定しました。command response だけを残すため、一般的な startup log
は省略しています。

### 再現可能な offline 登録

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "codex-demo" (kind "codex", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → codex
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 300
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

SQLite では engine を停止して roster mutation を offline で実行します。PostgreSQL では engine と
並行して実行できます。SQLite の稼働中の変更にはコンソールを推奨します。

### 接続テストとその限界

2026 年 8 月 30 日にスクリーンショット用 host で行った再現可能な測定結果は次のとおりです。

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "codex-demo" (codex): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

process は code `0` で終了しました。host には ChatGPT で認証済みの Codex CLI session がありましたが、
`codex-demo` には `api_key` がありませんでした。この結果が証明するのは offline catalog と、`Open` が
設定を受け入れたことだけです。OpenAI authentication を証明せず、`Gather` を呼ばず、Analytics または
Compliance の行を 1 行も読みません。credential がある場合でも、`Open` は client を構築するだけなので
`sources test` は upstream request を行いません。最初の data test は、実際の engine poll とその後に
表示される observation です。

### Managed policy を validation する

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate
```

```text
ok: policy renders to valid Codex managed-config TOML
```

### Hook のローカル拒否をテストする

endpoint を意図的に未設定にした場合は次のとおりです。

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_use_id":"exec-1"}' | olivares codex-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed endpoint not configured (deny-closed)"}}
```

拒否は Codex が解釈する JSON 内に含まれるため、process は code `0` で終了します。この probe は
fail-closed client を検証します。Codex が `PreToolUse` event を受け入れることも、hook が trusted と
mark された host でテストする必要があります。

## Control console

| 場所 | 表示内容 | 表示条件 |
|---|---|---|
| **Control console > Connectors**（`/console`） | source、mode、頻度、secret ではない設定、Test/Save/Reload action。 | 永続化した source はすぐ表示されますが、データは表示されません。 |
| **Health > Connectors**（`/health`） | コネクター状態、メッセージ、傾向、最新 activity。 | roster の再読み込み後。 |
| **Observability > Ingestion**（`/observability`） | `olivares.codex` の counter、observation type、最初と最後の受信。 | `Gather` がデータを出力した後。process 全体の counter は boot 時に開始し、再起動でリセットされます。 |
| **Cost & FinOps**（`/finops`） | 推定 Analytics usage と、有効時の日次請求 cost。 | 有効な credential、`workspace_id`、authorized API が必要です。`costs` は明示的な opt-in が必要です。 |
| **Security**（`/security`） | adoption finding、利用できない enterprise surface、Compliance data の opt-in 構造分析。 | collection 後。enterprise surface の 403/404 response は成功ではなく posture evidence になります。 |
| **Sessions**（`/sessions`） | action、model、identity、cost、posture を含む session と timeline。 | ガバナンス対象の hook から得られます。batch source だけでは live session を作りません。 |
| **Audit**（`/audit`） | import した activity evidence と ledger に anchor された PEP decision。 | 帰属可能な log または decision の受信後。 |

offline catalog を、model panel に remote inventory があることの証明として扱わないでください。
コネクターは runtime に catalog を提供しますが、この tree にはその画面へ公開する module consumer がありません。

<img class="light:sl-hidden" src="/console/health-dark.png" alt="エステート全体の稼働状況、信頼性、依存関係。観測されたアクティビティと陳腐化スイープから導出され、インフラを能動的に探査することはありません。">
<img class="dark:sl-hidden" src="/console/health-light.png" alt="エステート全体の稼働状況、信頼性、依存関係。観測されたアクティビティと陳腐化スイープから導出され、インフラを能動的に探査することはありません。">
<img class="light:sl-hidden" src="/console/finops-dark.png" alt="エステート全体のトークンコスト — トレンド、チャージバック、突合、予算、予測。数値はFinOps台帳が報告するとおりに正確に表示されます。">
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="エステート全体のトークンコスト — トレンド、チャージバック、突合、予算、予測。数値はFinOps台帳が報告するとおりに正確に表示されます。">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="ガードレールの検出結果、エンフォースメント態勢、異常キュー、改ざん検知可能なインシデントフォレンジック。このプレーンはデフォルトで検出型です。記録は行いますが、エンフォースメントが有効化されガバナンス下にない限り、それ自体がブロックすることはありません。">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="ガードレールの検出結果、エンフォースメント態勢、異常キュー、改ざん検知可能なインシデントフォレンジック。このプレーンはデフォルトで検出型です。記録は行いますが、エンフォースメントが有効化されガバナンス下にない限り、それ自体がブロックすることはありません。">

## 本番での使用

- **Credential なしの pilot：** `codex-demo` で packaging と roster を validation しますが、offline
  catalog と明示します。enterprise connectivity indicator として使用しないでください。
- **Governance ingestion：** read-only automation identity と最小限の API set を使用します。
  承認済みの chargeback requirement がない限り `attribute_email=false` を維持します。
- **Endpoint control：** version 管理された policy から TOML file を生成し、fleet configuration
  system で配布し、`codex-managed-config` で状態を poll して intent、deployment、drift を区別します。
- **Session control：** まず canary group に hook をインストールします。ring を拡大する前に、
  `PreToolUse` が無害な action を block することを確認します。event を生成しなかった hook を
  ガバナンス済みとして数えてはいけません。
- **正確な FinOps：** `project_id` がデータを Codex 支出に限定する場合のみ `costs` を有効にします。
  adoption には Analytics、請求額には Costs API を使用し、2 つの請求であるかのように合算しないでください。

## 強制されるものと観測のみのもの

| Surface | 実際の動作 |
|---|---|
| `codex` source と enterprise API | **観測のみ、read-only です。** OpenAI 設定を変更せず、inference を intercept しません。 |
| `api_key` なしの mode | **Offline catalog です。** ChatGPT subscription、remote API、workspace のいずれも証明しません。 |
| `requirements.toml` | 管理対象 hook だけを信頼することを含め、ユーザーが拡張できない**システム制限を強制します**。 |
| `managed_config.toml` | **管理対象の default を設定します。** `requirements.toml` の制限を代替しません。 |
| `codex-managed-config` | **drift を観測して比較します。** host 上の file を修正しません。 |
| `PreToolUse` または `PermissionRequest` の `olivares codex-hook` | **action を防止できます。** Codex は `permissionDecision=allow` を受け入れません。Olivares は allow を不介入として表し、`ask` request を拒否に変換します。 |
| `PostToolUse` と lifecycle event | **能力が等しくない evidence です。** 後からの block は実行済み tool を元に戻せず、`SessionEnd` には veto output がありません。 |
| Codex OTLP receiver | **この version では部分的な受信です。** event を count して drain しますが、session または finding にはまだ変換しません。 |

完了条件は累積的です。source の再読み込み、最初の `Gather` による enterprise data の返却、system
policy の検証、trusted hook の観測、`PreToolUse` の明示的な veto がすべて必要です。`ANSWERED`
が対象とするのは `Open` の最初の部分だけです。
