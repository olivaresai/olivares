---
title: Grok Build を統合する
description: >-
  Grok Build をガバナンス control plane の管理下に置きます。コネクター、ガバナンス対象の
  hook、稼働後にコンソールで確認できる内容を説明します。
---

`grok` 統合は、**terminal agent である Grok Build** を実行元の host からガバナンスします。
read-only mode で TOML 設定、sandbox profile、MCP server 名、system requirement、hook を無効にする
file を読み取ります。OTLP trace も受信できます。これは xAI API connector ではありません。
remote model を照会せず、provider secret も不要です。予防的な tool control には
`olivares grok-hook` と個別のローカル PEP を使用します。

## Grok Build を追加する

### 前提条件

- Olivares AI と Grok Build が同じ host にインストールされていること。または Grok configuration
  path が connector host に read-only で mount されていること。
- posture の帰属先となる tenant の UUID。
- Olivares service account が `~/.grok/config.toml`、`/etc/grok/requirements.toml`、
  `~/.grok/disabled-hooks`、および設定した場合は互換性のある `managed-settings.json` を
  読み取る権限。
- コンソールから source を作成する場合、AAL3 elevation を持つ superadmin account。

この source に xAI key を入力しないでください。secret field はなく、inference API call も行いません。

1. **Control console**（`/console`）を開き、**Connectors** タブを選択します。
2. 種別 `grok`、名前 `grok-demo`（または安定した host 名）、tenant、batch interval、有効状態を
   指定して source を追加します。`60` 秒であれば、ローカル file read を連続 loop にすることなく、
   pilot 中の posture change を確認できます。
3. source を保存して **Test** を選択し、roster を再読み込みします。行は roster entry を確認する
   だけです。その後の最初の `Gather` が file を読み取り、finding を出力します。

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="誰がアクセスでき、何を管理できるかを設定します。ユーザーのオンボーディング、SSO の接続、ワークスペースとエージェントグループの構成を行います。">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="誰がアクセスでき、何を管理できるかを設定します。ユーザーのオンボーディング、SSO の接続、ワークスペースとエージェントグループの構成を行います。">

## Grok Build を設定する

### 1. Host inventory と requirement

| Source 設定 | 既定値 | 測定対象 |
|---|---|---|
| `agent_ref` | `grok-build` | finding に含まれる安定した参照です。 |
| `config_path` | `~/.grok/config.toml` | user が宣言した sandbox profile と MCP server 名です。 |
| `requirements_path` | `/etc/grok/requirements.toml` | effective configuration を制約する system layer です。 |
| `disabled_hooks_path` | `~/.grok/disabled-hooks` | user が無効にした hook 名です。1 行につき 1 つです。 |
| `managed_settings_path` | 空 | Grok が互換性のために尊重する Claude Code の `managed-settings.json`。空は「未測定」を意味します。 |
| `otlp_http` | `false` | trace receiver。operator が port を予約するまで無効です。 |

Linux で sandbox を強制するための最低限の requirement は次のとおりです。

```toml
[sandbox]
profile = "strict"
```

これを管理者所有の `/etc/grok/requirements.toml` として配布します。`strict` は write を workspace、
`~/.grok/`、一時ディレクトリに限定し、文書化された Linux guarantee に従って network access を block
します。`~/.grok/config.toml` 内の同じ値は user preference にすぎません。command-line option と
environment は設定に影響できますが、`requirements.toml` が制約層です。

MCP を制限するには、fleet が使用できる `[mcp_servers.<nombre-aprobado>]` table だけを
`requirements.toml` に宣言します。Olivares が inventory するのは名前であり、table 内の command、
URL、credential ではありません。file がない場合、読めない場合、`[mcp_servers]` がない file がある
場合は、それぞれ異なる状態になります。「未測定」が「なし」と表示されることはありません。

Grok は互換性のため `/etc/claude-code/managed-settings.json` も読み取れます。Olivares でその surface
を測定する場合に限り `managed_settings_path` を設定します。Claude hook を検証せずに再利用しないで
ください。Grok payload は camelCase key と snake_case event を使用し、`olivares grok-hook`
が必要です。

### 2. ガバナンス対象の hook

配備した Grok version の native discovery mechanism を使用して `olivares grok-hook` をインストール
します。Grok が `hooks` key を読み取る settings JSON file、または `~/.grok/hooks/` のような hook
directory 内の `*.json` file を使用します。Grok はこれらを名前で読み込みます。Olivares は完全な
authoring wrapper を定義しておらず、この tree にも保持していません。インストール済み version の
schema を使用し、command を正確に次の値に設定してください。

```text
olivares grok-hook
```

Olivares の起動時に `OLIVARES_GROK_HOOK_PEP_CONFIG` が有効な設定を指していると、PEP が mount
されます。

```json
{
  "listen": "127.0.0.1:8449",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

各 instance は 1 つの tenant をガバナンスし、firm identity を要求します。client は
`OLIVARES_GROK_HOOK_URL`、`OLIVARES_GROK_HOOK_TOKEN`、`OLIVARES_GROK_HOOK_TENANT`、
`OLIVARES_GROK_HOOK_AGENT`、`OLIVARES_GROK_HOOK_ORG`、`OLIVARES_GROK_HOOK_ACCOUNT` を
読み取ります。これらは process と secrets manager から提供し、token を hook JSON に入れないでください。

hook に付ける名前は重要です。user がその名前を `~/.grok/disabled-hooks` に追加すると、managed layer
由来かどうかにかかわらず dispatcher は hook を省略します。`requirements.toml` も MDM もこの file を
制約しません。コネクターはこれを読み取り、無効化された名前を含む high-severity finding を出力しますが、
無効化自体は防げません。

### 3. オプションの OTLP trace

`otlp_http=true` の場合、receiver は既定で `127.0.0.1:4318` を listen し、Grok Build で測定した
path である `POST /v1/traces` を受け入れます。この未認証 input は loopback に限定する必要があります。
別のコネクターがすでに `4318` を使用している場合、未使用のローカル port を選び、`otlp_http_addr` と
agent の OTLP endpoint に同じ値を設定してください。

collection は trace を attribution、span 名、`session_id` に縮約し、content を保持しません。この
version では、次回の poll が span、session、drop count を含む aggregate finding を出力します。timeline
と tool ごとの control には hook を使用します。

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="ステップアップ認証が必要です — AAL3（ハードウェア、フィッシング耐性）">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="ステップアップ認証が必要です — AAL3（ハードウェア、フィッシング耐性）">

## CLI の使用方法

次の例は 2026 年 8 月 30 日に worktree のバイナリで実行しました。一般的な startup message は
省略しています。

### ローカル source を登録する

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --kind grok \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 60 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "grok-demo" (kind "grok", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → grok
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 60
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

SQLite では offline roster mutation の前に engine を停止するか、稼働中のコンソールを使用します。
PostgreSQL では command を engine と並行して実行できます。`--actor` と `--reason` は provenance change
を帰属させます。

既定以外の path には明示的な設定値を追加します。

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --config config_path=/srv/grok-home/.grok/config.toml \
  --config requirements_path=/etc/grok/requirements.toml \
  --config disabled_hooks_path=/srv/grok-home/.grok/disabled-hooks \
  --config managed_settings_path=/etc/claude-code/managed-settings.json \
  --actor platform-operator \
  --reason grok-paths-for-service-user
```

### 接続テストと実際の file read

2026 年 8 月 30 日にスクリーンショット用 host で行った再現可能な測定結果は次のとおりです。

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "grok-demo" (grok): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

process は code `0` で終了しました。その host には稼働中の Grok session と
`~/.grok/config.toml` があり、`/etc/grok/requirements.toml` と `~/.grok/disabled-hooks` は
ありませんでした。`sources test` はどれも読みませんでした。`Open` は設定を resolve するだけで、
`test` は `Gather` を呼ばずにすぐ閉じます。したがって `ANSWERED` は session、sandbox、finding を
証明しません。file read のテストには、engine を再読み込みして次の poll が出力する finding を確認します。

### Hook client の fail-closed 動作を検証する

endpoint が未設定の場合は次のとおりです。

```sh
printf '%s' '{"hookEventName":"pre_tool_use","sessionId":"s-1","toolName":"Bash"}' | olivares grok-hook
```

標準出力：

```json
{"decision":"deny","reason":"no governance endpoint is configured (deny-closed)"}
```

標準エラー：

```text
no governance endpoint is configured (deny-closed)
```

exit code は `2` で、Grok は `pre_tool_use` の veto として解釈します。その他の event では、拒否は
記録されますが action を防止できません。client は enforcement を主張せず、stderr にその旨を報告します。

## Control console

| 場所 | 表示内容 | 運用上の制限 |
|---|---|---|
| **Control console > Connectors**（`/console`） | `grok` roster、設定済み path、interval、mode、Test/Save/Reload action。 | test はコネクターを開閉しますが、TOML file を読みません。 |
| **Health > Connectors**（`/health`） | source 状態、message、傾向、最新 poll。 | process health は、欠落した file がガバナンスされていることを証明しません。 |
| **Observability > Ingestion**（`/observability`） | `olivares.grok` が出力した finding、最初と最後の record、有効時の aggregate OTLP activity。 | 起動後の process 全体の counter です。リセットされ、tenant 固有ではありません。 |
| **Security**（`/security`） | 観測・強制された sandbox profile、MCP 名、requirement の存在と妥当性、managed-settings compatibility、無効化 hook 名。 | 「読めない」は、欠落に変換されず unknown のままです。 |
| **Sessions**（`/sessions`） | session、action、identity、permission mode、最新 activity、`enforced` または `observed` posture。 | hook event が必要です。local inventory は session を作りません。 |
| **Audit**（`/audit`） | 帰属可能な PEP decision と chained evidence。 | PEP に到達した call にのみ存在します。無効化 hook は gap を残します。 |

model catalog、xAI spend、prompt は表示されません。この source は xAI API を使用せず、OTLP receiver は
content を破棄します。

<img class="light:sl-hidden" src="/console/observability-counters-dark.png" alt="標準準拠の取り込み健全性と、台帳と相関させたトレースのドリルダウンです。数値はエンジン全体（プロセスグローバル）のものであり、テナント単位ではありません。標準は上流団体が宣言したバージョンと成熟度に固定されています。">
<img class="dark:sl-hidden" src="/console/observability-counters-light.png" alt="標準準拠の取り込み健全性と、台帳と相関させたトレースのドリルダウンです。数値はエンジン全体（プロセスグローバル）のものであり、テナント単位ではありません。標準は上流団体が宣言したバージョンと成熟度に固定されています。">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="ガードレールの検出結果、エンフォースメント態勢、異常キュー、改ざん検知可能なインシデントフォレンジック。このプレーンはデフォルトで検出型です。記録は行いますが、エンフォースメントが有効化されガバナンス下にない限り、それ自体がブロックすることはありません。">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="ガードレールの検出結果、エンフォースメント態勢、異常キュー、改ざん検知可能なインシデントフォレンジック。このプレーンはデフォルトで検出型です。記録は行いますが、エンフォースメントが有効化されガバナンス下にない限り、それ自体がブロックすることはありません。">

## 本番での使用

- **Linux endpoint baseline：** `requirements.toml` を root-owned file として配布し、すべての
  host を poll します。欠落は緑の default ではなく、actionable finding になります。
- **MCP control：** user が宣言した名前と administrator が固定した名前を比較します。
  `GROK_CONFIG` variable は MCP、authentication、egress などの sensitive table を追加できません。
  この保護は Grok が提供し、Olivares は重複実装せずに報告します。
- **Hook canary：** 無害な tool から始め、event、decision、effect を確認します。その後、control が
  名前単位で消える可能性があるため、`disabled-hooks` を継続的に監視します。
- **共有 endpoint：** Grok を実行する account の実際の `HOME` への absolute path を設定します。
  Olivares service の `~` は別の user に resolve され、誤った host profile を正確に測定しうります。
- **最小限のテレメトリ：** aggregate signal が必要な場合のみ OTLP を有効にし、専用のローカル socket
  を予約します。予防的なガバナンスでは、hook の確実な実行を優先します。

## 強制されるものと観測のみのもの

| Surface | 実際の動作 |
|---|---|
| `grok` source | **観測のみ、read-only です。** file を読み finding を出力します。Grok Build を変更せず、xAI を呼び出しません。 |
| `/etc/grok/requirements.toml` | constrained sandbox 値と MCP 値を**agent 内で強制します**。Olivares は存在と宣言された効果を検証します。 |
| `~/.grok/config.toml` | **観測された preference です。** それ自体は administrative policy ではありません。 |
| `pre_tool_use` の `olivares grok-hook` | command が実行され `2` で終了すると、**tool を防止できます**。PEP がないか失敗した場合、client は deny-closed で拒否します。 |
| その他の Grok event | **観測されます。** 拒否は evidence として残りますが、event に同等の veto はありません。 |
| timeout、crash、または実行されなかった hook | **agent は fail-open です。** Grok は継続します。`olivares grok-hook` 内部の fail-closed 動作は process が呼ばれた場合にのみ適用されます。 |
| `~/.grok/disabled-hooks` | **managed hook も無効化できます。** Olivares は後から検出しますが、requirement layer は防止しません。 |
| OTLP receiver | **aggregate を観測します。** 認証せず、content を保持せず、hook timeline を代替しません。 |

sandbox が固定されているだけで deployment を「enforced」と宣言してはいけません。完了には、effective
requirement、実際に動作する hook、その名前が `disabled-hooks` にないことを継続監視すること、表示される event、
実証済みの `pre_tool_use` veto が必要です。
