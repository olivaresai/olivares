---
title: "Claude Code 向けエンタープライズ OpenTelemetry を構成する"
description: >-
  ガバナンス下の Claude Code フリート向けに推奨されるエンタープライズテレメトリ態勢:
  認可された OTel エクスポートを有効化する managed-settings env、FinOps の次元となる
  OTEL_RESOURCE_ATTRIBUTES によるオペレーターラベル、サブエージェント階層のための
  トレーシング beta、そしてプライバシー設定 — それぞれが伴う責務とともに明示する。
---

Claude Code の OpenTelemetry エクスポートは、ガバナンス下のフリートにとっての
**認可された観測経路**です。プラン制限の対象ではなく、セッションに帰属するテレメトリを
運び、managed settings 層によってすべての開発者に対して有効化できます — しかも何も
プロキシしません。本ページは [Connect Claude Code](/ja/how-to/connect-claude-code/) の
上に重ねる *エンタープライズ* 構成です。フリート全体に何を設定するか、各設定が何を
もたらすか、そしてそれがどのような責務を生むかを扱います。以下のキー名とセマンティクスは
Claude Code 自身のドキュメントに対して 2026-06-10 に検証済みです（クライアント 2.1.17x）。
新しいものをエンコードする前にそこで再確認してください — 急速に変化します。

:::note[managed env は Claude Code のみを制御する]
managed `env` ブロックは **Claude Code プロセス** を構成します。OTEL_* 変数は
サブプロセス（Bash コマンド、フック、MCP サーバー）には **伝播されません**。トレーシングが
有効な間、`TRACEPARENT` のみがシェルサブプロセスに継承されます。サブプロセスの観測性は
別途計画してください（カーネル/eBPF のバックストップ）。
:::

## 得られるもの

| 設定 | もたらすもの | 生じる責務 |
|---|---|---|
| managed テレメトリ `env` | すべてのセッションがあなたのコレクターへ OTLP をエクスポート — 開発者自身の構成に左右されない観測 | なし — 既定で構造的テレメトリ |
| `OTEL_RESOURCE_ATTRIBUTES` | **すべてのメトリックデータポイントおよびイベントレコード** に組織定義のラベル（チーム、プロジェクト、コストセンター）が付く。control plane がそれらを FinOps の支出次元へルーティングする | ラベル値を機微でないものに保つこと。コネクタが許可リスト化しスクラブする |
| トレーシング beta | `claude_code.llm_request` / `claude_code.tool` スパンが `agent_id` / `parent_agent_id` を運ぶ — アクセスグラフ内の **インスタンス単位のサブエージェント階層** | beta 面: アップグレード時に検証すること |
| `OTEL_LOG_TOOL_DETAILS=1` | ツールイベントに `tool_parameters` を付与 — 拒否されたツール判定で **どのコマンドが拒否されたか** を含む | ツール入力がホスト外へ出る: あなたが負うべきレジデンシー/秘匿化の責務 |
| `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` | `app.entrypoint`（cli / sdk-ts / claude-vscode …）— 各セッションを起動した面 | なし（低カーディナリティのラベル） |

## ステップ 1 — managed 層からエクスポートを有効化する

managed settings ポリシーでテレメトリ `env` を記述します（`managed-settings`
コネクタの `TelemetryEnv` ヘルパーがまさにこの態勢をレンダリングします）。テレメトリを
有効化し、OTLP エクスポーターを control-plane のコレクターに向け、メトリックとログの
両方をエクスポートします。完全な変数リファレンスは Claude Code 自身の監視ドキュメントに
委ねてください — ここから値を手書きでコピーしないこと。

:::caution[コレクターの認証情報を決してインライン化しない]
managed-settings ファイルはすべてのホスト上で平文です。記述層がまさにこの理由で
値を伴う `OTEL_EXPORTER_OTLP_HEADERS` を拒否します — コレクターの認証は mTLS または
シークレットマネージャー参照で行い、インラインのトークンは決して使わないでください。
:::

コンテンツのキャプチャ（プロンプト、ツール本体）はオプトインしない限り **オフ** のままです —
そして control-plane コネクタは、クライアントが何を発しようとも、独立して構造データのみを
保持します。

## ステップ 2 — FinOps 向けにフリートをラベル付けする

同じ managed env 内で `OTEL_RESOURCE_ATTRIBUTES` を、厳密な W3C Baggage 形式で
設定します（値をパーセントエンコードし、スペースや引用符を入れない）:

```
OTEL_RESOURCE_ATTRIBUTES=team=payments,project=atlas,cost_center=cc-42
```

クライアント 2.1.161 以降、これらの値は OTLP リソースブロックだけでなく
**すべてのメトリックデータポイントおよびイベントレコード** に乗ります — そしてカスタム
キーは標準属性を決して上書きしません。control plane 側では、honor するキーを claude
コネクタの `resource_labels` 許可リストに列挙します。コネクタは値をスクラブし、セッションの
アイデンティティエッジおよびすべてのコストサンプルにラベルとして付与します。FinOps は
`team` と `project` を第一級の支出次元に昇格させるため、「Claude Code の支出をチーム単位で
切り分ける」がエンドツーエンドで機能します。許可リストにないキーは破棄されます —
既定で最小データです。

## ステップ 3 — サブエージェント階層（トレーシング beta）

スパンを得るには、managed env で拡張テレメトリ beta とトレースエクスポーターを有効化します。
サブエージェントのアイデンティティ属性（`agent_id`、`parent_agent_id`）は **スパン専用** です —
いかなるメトリックにもログイベントにも現れません — そして `claude_code.llm_request`
（2.1.139 以降）と `claude_code.tool`（2.1.145 以降）のスパンに存在します。コネクタは
それらをアクセスグラフへ次のようにマッピングします:

- `session → identity.subagent` — 行為した **インスタンス** のサブエージェント、および
- `parent agent → identity.subagent` — **誰がそれを生成したか**（メインセッションが直接
  生成したエージェントでは欠落）。

これが、同じ型の 2 つの並行サブエージェントを区別可能にするものです — `Agent` ツールの
`subagent_type` だけでは型ラベルであり、インスタンスではありません。

## ステップ 4 — 任意の忠実度設定

- `OTEL_LOG_TOOL_DETAILS=1` はツールイベントに `tool_parameters` を追加します —
  拒否されたツール判定でも（2.1.157 以降）。そのため拒否の finding が、ブロックされた
  サニタイズ済みコマンドを名指しできます。コネクタは取り込み時に入力を秘匿化された
  リソース参照に縮約し、決して生のまま保存しません。ただし値は開発者のホスト外へ
  出るため、これを有効化することは意図的なレジデンシー判断です。
- `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` はすべてのメトリックとイベントに
  `app.entrypoint` を追加します（既定でオフ）。コネクタはこれをセッショントポロジーとして
  記録します — SDK 埋め込みのフリートは、対話的な CLI 利用とは異なるリスク態勢を持ちます。

## この経路の正直な限界

- **未認証のループバック取り込み。** 協調レシーバーは既定でループバックにバインドし、
  そこに留まらなければなりません。到達可能なものは何でもテレメトリを偽造できます
  （[Connect Claude Code](/ja/how-to/connect-claude-code/) を参照）。
- **サブプロセスはカバーされない。** OTEL_* は Bash/フック/MCP サブプロセスに届きません。
  トレーシング下では `TRACEPARENT` のみが継承されます。
- **admin-plane フィードはサードパーティプロバイダーを見られない。** Claude Code
  Analytics API は Claude API 上の使用量のみを追跡します — Claude Platform on AWS、
  Microsoft Foundry、Amazon Bedrock、Gemini Enterprise Agent Platform (formerly Vertex AI) は含まれません。これらの面のフリートでは、
  **この OTel 経路があなたの持つ唯一の観測** であり、admin フィードのシャドウ認証
  ディテクターはそれらをクリアできません。
- **ここでのコスト数値は推定値。** リクエスト単位のコストテレメトリは、権威ある
  コストレポートに対して照合されます。セッションあたりのコストの出典は 1 つであり、
  決して両方ではありません。

## 次のステップ

- [Connect Claude Code](/ja/how-to/connect-claude-code/) — 本ページが基盤とする
  ベース配線。
- [Govern and approve](/ja/how-to/govern-and-approve/) — 強制の半分
  （managed settings、フック、PEP）。
- [Forward audit to Splunk](/ja/how-to/forward-audit-to-splunk/) — このテレメトリが
  生む finding を SIEM へ送る。
