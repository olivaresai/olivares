---
title: "OpenTelemetry GenAI（任意の計装済みエージェント）"
description: >-
  あらゆる OTel 計装済みエージェント（LangChain、LangGraph、CrewAI、AutoGen、
  Google ADK など）から、ベンダー中立な gen_ai.* 取り込みプロファイル経由で
  アクセスマップと FinOps にデータを供給する。オプトイン方式で semconv v1.41.1
  に固定され、実際のフリートで併存する 3 つの GenAI 方言を正規化する。
sidebar:
  order: 4
---

Claude Code は規範的な協調ソースだが、運用する唯一の協調エージェントではない。
Claude Code のテレメトリ（`kind: claude`）を受信するのと同じコネクタが、
**オプトインのベンダー中立な OpenTelemetry GenAI プロファイル**を提供する。
任意の OTel 計装済みエージェントやフレームワークを同じ OTLP レシーバーに向ければ、
その `gen_ai.*` テレメトリがアクセスマップとコストパイプラインに供給される
——LangChain、LangGraph、CrewAI、AutoGen、Google ADK、その他スパンやログ
イベント上で GenAI セマンティック規約を発行するあらゆるものが対象だ。

## なぜオプトインなのか

OpenTelemetry GenAI 規約はアップストリームでは **Development ステータス**
（プレ安定版）であり、2026 年のフリートでは実際に 3 つの方言が併存している。
そのためこのプロファイルはデフォルトでオフであり、OTel SDK がゲートするのと
まったく同じ方法——オプトイントークン——でゲートされる:

```json
{
  "sources": [{
    "name": "agents-otel",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "semconv_opt_in": "gen_ai_latest_experimental"
    }
  }]
}
```

`semconv_opt_in` は `OTEL_SEMCONV_STABILITY_OPT_IN` を反映する。これは
`gen_ai_latest_experimental` を含まなければならないカンマ区切りのリストだ。
プロファイルが**オフ**の場合、`gen_ai.*` レコードはセッション生存性
ウォッチドッグには依然として供給されるが、**マッピングはされない**——
これは正直な不在であって、無言の取り込みではない。

## 正規化器が受け付けるもの

このプロファイルは **semconv v1.41.1** に固定されており、実際の組織で併存する
3 つの GenAI 方言を正規化する。正規化された各イベントには方言の semconv ピンを
刻印するため、プロベナンスが保持される:

| 方言 | 形状 |
|---|---|
| レガシー OpenLLMetry | インデックス付き `gen_ai.prompt.{i}.*` 属性 |
| v1.36 以前 | 非推奨のメッセージ単位イベント |
| v1.37 以降 | `messages` 生成 |

メッセージ形状に加えて、**`mcp.*` 規約（v1.39）**および
**`invoke_agent` の client/internal 分割と `invoke_workflow`（v1.41）**を
マッピングする——これにより、フレームワークがオーケストレーションする
エージェントおよびワークフローの呼び出しが、ノイズではなく構造化された
トポロジーとして取り込まれる。スパンベースの発行（LangGraph、LangChain、
CrewAI、AutoGen、Google ADK の計装方式）とログベースの発行の両方が
取り込まれる。

コストサンプルは W3C スパン id で重複排除されるため、スパン経路とログ経路の
両方でテレメトリが到着するエージェントが二重請求されることはない。

## エージェントの配線

レシーバーはコネクタ自身の OTLP エンドポイント（デフォルトで gRPC
`127.0.0.1:4317`、HTTP `127.0.0.1:4318`）だ。エージェント側では、標準の
OTel SDK 設定が適用される——エクスポーターのエンドポイントをループバックの
レシーバーに向け、計装がゲートする場合は GenAI オプトインを指定する:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
```

:::caution[Claude Code と同じループバックのルール]
協調取り込みは**認証なし**で、デフォルトでループバックにバインドされる。
ソケットに到達できるものは何でもテレメトリを偽造できる——ループバック上に
保つこと（`allow_public_bind` は存在するが、意図的に DANGEROUS と
マークされている）。ホスト外のエージェントは、公開 OTLP ポートではなく
カーネルのバックストップの仕事だ。
:::

## コンソールで見えるもの

計装済みセッションは **Sessions** にライブアクティビティとして表示され、
発行元のエージェントに帰属される。そのモデル呼び出しは **Cost & FinOps** に
供給され、MCP およびツールのスパンは、他の協調ソースと同様に **Access map**
へエッジを寄与する:

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="協調テレメトリからのライブなエージェントセッションアクティビティを表示する Sessions ビュー。" />
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="協調テレメトリからのライブなエージェントセッションアクティビティを表示する Sessions ビュー。" />

## 正直な限界

- **プレ安定版の規約、固定された取り込み。** プロファイルは v1.41.1 に
  固定されている。アップストリームが動いたときは、無言のドリフトではなく
  意図的な更新によってピンを動かす。第 4 の方言を発行する計装については
  推測しない。
- **協調とは協調を意味する。** 発行しないエージェントはこの経路には不可視だ
  ——それこそが [eBPF/Tetragon](/ja/how-to/connectors/ebpf-tetragon/) と
  ストアネイティブ監査の役割だ。
- **フレームワークの span-kind の癖は実在する。** 一部のフレームワークは、
  その kind が v1.41 の client/internal ルールに一致しないスパンを発行する。
  正規化器は証明できるものをマッピングし、残りは誤帰属するのではなく
  未マッピングのまま残す。

## 関連

- [Claude Code を接続する](/ja/how-to/connect-claude-code/) — 同じレシーバーの
  Claude 固有の面。
- [Claude Code 向けエンタープライズ OTel](/ja/how-to/claude-code-enterprise-otel/) —
  フリート全体のテレメトリ態勢。
- [イベントリファレンス](/ja/reference/events/) — これが生成する正規化された
  観測。
