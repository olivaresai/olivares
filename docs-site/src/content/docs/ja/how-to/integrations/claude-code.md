---
title: Claude Code を統合する
description: >-
  Claude Code をガバナンス control plane の管理下に置きます。コネクター、managed settings、
  ガバナンス対象の PEP hook、稼働後にコンソールで確認できる内容を説明します。
---

この統合は、Olivares AI を必須の proxy にすることなく、Claude Code をガバナンス control
plane の管理下に置きます。`claude` コネクターは OTLP テレメトリと hook イベントを受信し、
session を関連付け、R/RW アクセス、コスト、検出事項を記録します。予防的な制御が必要な場合、
管理対象の `olivares claude-hook` hook が、各ツール使用前にローカルの Olivares PEP を照会します。
この 2 つの plane は独立しています。テレメトリを受信していることは、policy が強制されている
ことを意味しません。

## Claude Code を追加する

### 前提条件

- first-party の `claude` コネクターを含む Olivares AI バイナリ。
- 観測結果の帰属先となる enterprise tenant の UUID。
- ガバナンス対象の endpoint に Claude Code がインストールされていること。ローカル receiver
  に Anthropic API key は不要です。
- Claude Code から Olivares receiver へのローカル接続。既定値は、OTLP/gRPC が
  `127.0.0.1:4317`、OTLP/HTTP と協調 hook が `127.0.0.1:4318` です。
- Olivares service 用の実行可能な一時パス。`claude` は分離された plugin として動作します。
  `/tmp` が `noexec` でマウントされているシステムでは、service unit の `TMPDIR` を Olivares
  service account が所有する専用ディレクトリに設定してください。

OTLP receiver または協調 endpoint を loopback の外部に公開しないでください。送信元を認証しない
ため、到達できるホストはテレメトリを偽造できます。ガバナンス対象の PEP は別の surface です。
独自のローカル socket を使用し、すべてのリクエストを認証して各決定を記録します。

1. **Control console**（`/console`）を開き、**Connectors** タブを選択します。コネクターの
   roster はグローバルです。superadmin account が必要で、保存、テスト、再読み込みには AAL3
   elevation が必要です。
2. 種別 `claude`、`claude-code-prod` のような安定した運用名、該当する tenant、`live` mode、
   interval `0`、有効状態で source を追加します。interval が 0 で正しい設定です。このコネクター
   は batch polling ではなく receiver を維持します。
3. source を保存して **Reload** を選択します。行には名前、種別、mode、状態が表示されます。
   `claude` は out-of-process connector なので、コンソールの test action は利用できません。
   保存時に validation が行われ、完全な open test には plugin を起動する
   `olivares sources test` を使用します。

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="誰がアクセスでき、何を管理できるかを設定します。ユーザーのオンボーディング、SSO の接続、ワークスペースとエージェントグループの構成を行います。">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="誰がアクセスでき、何を管理できるかを設定します。ユーザーのオンボーディング、SSO の接続、ワークスペースとエージェントグループの構成を行います。">

## Claude Code を設定する

観測 source と管理対象の agent policy という 2 つの設定を一緒に配布します。

### 1. Receiver とデータ最小化

安全な初期設定は既定値です。

| Source 設定 | 初期値 | 効果 |
|---|---:|---|
| `enable_grpc` | `true` | `grpc_addr`（`127.0.0.1:4317`）で OTLP/gRPC を提供します。 |
| `enable_http` | `true` | `http_addr`（`127.0.0.1:4318`）で OTLP/HTTP と協調 hook を提供します。 |
| `hook_path` | `/hooks` | HTTP receiver 内の協調 hook のパスです。 |
| `content_capture` | 空 | 構造は保持しますが、prompt、ツール body、API body は保持しません。extended reasoning は常に redact されます。 |
| `enforcement` | 空 | hook を観測します。この source は予防的な決定を返しません。 |
| `allow_public_bind` | `false` | loopback 外での bind を拒否します。 |

1 台のホストで複数の OTLP receiver を実行する場合、それぞれに異なる loopback address を割り当て、
agent 設定にも同じ値を使用してください。Claude、Codex、Grok は一部の mode で `4318` を既定値に
使用するため、同じ socket を同時に bind できません。

### 2. Managed settings とガバナンス対象の PEP

Olivares バイナリで Claude Code のシステムレベルファイルを生成します。

```sh
olivares agent managed-settings \
  --otel-endpoint http://127.0.0.1:4317 \
  --out /etc/claude-code/managed-settings.json
```

generator は `allowManagedHooksOnly: true`、`olivares claude-hook` を実行する `PreToolUse`
hook、`PostToolUse` redact hook をインストールします。また `grpc` protocol で OTLP を有効にするため、
上記 endpoint は HTTP receiver `4318` ではなく receiver `4317` を使用します。このファイルは
session の `HOME` ではなく、管理対象のシステム層に配置します。

Olivares の起動時に `OLIVARES_HOOK_PEP_CONFIG` でファイルを指定すると PEP server が有効になります。
次は 1 つの tenant に対する有効な policy の例です。

```json
{
  "listen": "127.0.0.1:8447",
  "tenants": [
    {
      "tenant": "11111111-1111-4111-8111-111111111111",
      "require_firm_identity": true,
      "enforcement": "enforce",
      "policy": {
        "version": "claude-prod-v1",
        "default": "allow",
        "rules": [
          {
            "tool": "Bash",
            "decision": "ask",
            "reason": "Shell commands require human confirmation"
          }
        ]
      }
    }
  ]
}
```

Olivares が起動した session には、`OLIVARES_HOOK_PEP_URL`、`OLIVARES_HOOK_PEP_TOKEN`、
`OLIVARES_HOOK_PEP_TENANT`、agent attribution の一時的な値が渡されます。独立して起動した
session では、operator が secrets channel を介してこれらの値を渡す必要があります。
`managed-settings.json` に書き込まないでください。endpoint が未設定または利用不能な場合、
`olivares claude-hook` は `deny` を返します。

初期の非ブロッキング rollout では、将来の RFC3339 値を `observe_until` に指定して `observe`
mode を使用します。この許可は一時的です。timestamp がない、無効、または期限切れの場合は
`enforce` になります。identity、tenant、kill switch、firewall、fail-closed error を含む platform
invariant は、business rule の観測中も引き続き強制されます。

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="ステップアップ認証が必要です — AAL3（ハードウェア、フィッシング耐性）">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="ステップアップ認証が必要です — AAL3（ハードウェア、フィッシング耐性）">

## CLI の使用方法

以下の出力例は、2026 年 8 月 30 日にこの worktree からビルドしたバイナリで測定しました。
engine の一般的な起動メッセージは省略しています。

### Source を登録する

SQLite は single-writer profile を使用するため、CLI から roster を変更する前に engine を停止します。
PostgreSQL では engine と並行して実行できます。SQLite の稼働中の変更にはコンソールを使用してください。

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --kind claude \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 0 \
  --config mode=live \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "claude-code-prod" (kind "claude", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → claude
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 0
  enabled: - → true
  config.mode: - → live
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

この変更はデータの provenance を変更し audit ledger に記録されるため、`--actor` と `--reason`
は必須です。

### コネクターを validation して開く

```sh
olivares sources validate \
  --data-dir /var/lib/olivares \
  --name claude-code-prod

olivares sources test \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --timeout 20s
```

```text
source "claude-code-prod"
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
source "claude-code-prod" (claude): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

`validate` は socket を開きません。`test` は `Open` と `Close` を呼び出しますが、`Gather` を
呼び出さず、source を engine に接続せず、Claude Code がテレメトリを送信していることも証明しません。
plugin に実行 bit が設定されているにもかかわらず `permission denied` で失敗する場合、process の
`TMPDIR` が `noexec` volume 上にないか確認してください。

### Hook の fail-closed 動作を確認する

endpoint を意図的に未設定にすると、client は Claude Code が期待する形式で拒否を返します。

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' | olivares claude-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed PEP endpoint not configured (deny-closed)"}}
```

この probe はローカル client を確認するもので、remote policy decision を確認するものではありません。
本番では rollout を拡大する前に、許可 rule、拒否 rule、firm identity を伴う `ask` request もテストします。

## Control console

source を追加しても過去のデータは作成されません。roster を再読み込みして最初の event を受信した後、
operator は以下の view を使用できます。

| 場所 | 表示内容 | 状態の解釈 |
|---|---|---|
| **Control console > Connectors**（`/console`） | 名前、`claude` 種別、mode、secret ではない設定、roster 状態、保存・再読み込み action。 | 「保存済み」は永続化を証明します。event の到着は証明しません。 |
| **Health > Connectors**（`/health`） | コネクターの health、運用メッセージ、傾向、最新の既知の poll または activity。 | 開いている receiver が正常でも agent は無通信の場合があります。 |
| **Observability > Ingestion**（`/observability`） | source 別 record、`edge`、`cost`、`finding` 種別、signal、最初と最後の event。 | 起動後の process 全体の counter です。再起動でリセットされ、tenant 固有ではありません。 |
| **Sessions**（`/sessions`） | session、状態、action、model、token、cost、最新 activity、`enforced` または `observed` posture。 | posture は event evidence を要約します。コネクター登録から推測されるものではありません。 |
| **Access map**（`/access-map`） | 観測された tool、MCP、resource から帰属した R/RW edge。 | 観測 edge は activity を表し、事前 authorization と同義ではありません。 |
| **Cost & FinOps**（`/finops`） | 受信テレメトリから導出した cost と token sample。 | coverage は fleet が export した範囲に限られます。OTLP を出さなかった call は再構成できません。 |
| **Security**（`/security`） | テレメトリ gap、sandbox/MCP posture、その他の出力済み finding。 | finding がないことは、未観測の surface が compliant であることを意味しません。 |
| **Claude Policy**（`/claude-policy`） | 管理対象 Claude Code surface の authoring、配布、version、check-in 状態。 | 配布と drift verification は別の事実であり、別々に表示されます。 |

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="エージェントのライブ運用 — 各セッションが今まさに何を実行しているか、そのトークン、コスト、ケイデンスをライブストリームで随時更新します。">
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="エージェントのライブ運用 — 各セッションが今まさに何を実行しているか、そのトークン、コスト、ケイデンスをライブストリームで随時更新します。">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="ガードレールの検出結果、エンフォースメント態勢、異常キュー、改ざん検知可能なインシデントフォレンジック。このプレーンはデフォルトで検出型です。記録は行いますが、エンフォースメントが有効化されガバナンス下にない限り、それ自体がブロックすることはありません。">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="ガードレールの検出結果、エンフォースメント態勢、異常キュー、改ざん検知可能なインシデントフォレンジック。このプレーンはデフォルトで検出型です。記録は行いますが、エンフォースメントが有効化されガバナンス下にない限り、それ自体がブロックすることはありません。">

## 本番での使用

- **段階的 rollout：** 有効期限付きの observed mode で構造的 content と rule から開始します。
  false positive を確認し、その後 tenant ごとに `enforce` に移行します。
- **Fleet 管理：** `/etc/claude-code/managed-settings.json` を RPM、immutable image、Ansible、
  Salt、または同等の enterprise configuration manager で配布します。2 つ目の
  `managed-settings` source で稼働中のファイルを確認し、欠落または drift を検出します。
- **職務分離：** platform team は receiver と可用性を維持し、security team は rule を version
  管理し、tenant owner は `ask` request と finding を確認します。すべての privileged change は
  引き続き帰属可能です。
- **データ最小化：** residency と retention が定義された承認済みの forensic need がない限り、
  `content_capture` は空にします。通常、adoption と cost analysis には構造データで十分です。
- **強化された host：** receiver を loopback に限定し、plugin に最小限の実行可能な一時ディレクトリ
  を用意し、policy を read-only にします。コネクターを起動するために `noexec` を全体で緩和しないでください。

## 強制されるものと観測のみのもの

| Surface | 実際の動作 |
|---|---|
| `claude` コネクターからの OTLP テレメトリと協調 hook | **観測されます。** 送信元は協調しますが、loopback receiver は認証せず、ローカル process は signal を省略または偽造できます。 |
| source の空の `enforcement` 設定 | **観測されます。** 既定値であり、tool を block しません。 |
| `olivares claude-hook` + PEP + managed settings | Claude Code が veto できる event に対して `allow`、`ask`、`deny` を**強制**し、決定を記録します。endpoint failure は deny-closed で拒否します。 |
| 管理対象層の `allowManagedHooksOnly` | PEP と競合しうる user hook または project hook に対して**インストールを強化します**。 |
| `PostToolUse` | **action 後に観測し redact します。** tool がすでに生じさせた効果は元に戻せません。 |
| Claude Code process と hook 外の action | **この配線では対象外です。** OS control、native auditing、network policy を補完的な防御として使用してください。 |

運用検証には、永続化された roster、開いたコネクター、**Ingestion** に表示される event、PEP により
実際に block された tool という 4 つの独立した確認が必要です。いずれも他の 3 つを代替しません。
