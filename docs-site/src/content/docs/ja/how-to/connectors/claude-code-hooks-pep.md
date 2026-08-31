---
title: "Claude Code のフックと強制（PEP）"
description: >-
  Claude Code コネクタのガバナンス側：デフォルトで観測されるフックと、PreToolUse /
  PermissionRequest フックに deny または ask で応答するオプトインのポリシー強制ポイント。
  すべてのゲートは finding として記録される。
sidebar:
  order: 5
---

[Claude Code を接続する](/ja/how-to/connect-claude-code/) は *観測* 側を配線する
（OTLP テレメトリを取り込み、アクセスエッジを出力する）。このページは
**ガバナンス側** である。Claude Code の **フック** はツールの決定をコネクタに報告し、
オプトインの **ポリシー強制ポイント（PEP, policy enforcement point）** はそのチャネルを
ゲートに変える。コネクタは、合致する `PreToolUse` / `PermissionRequest` フックに対して
`permissionDecision` を `deny` または `ask` で応答し、すべてのゲートを finding として記録する。

デフォルトは意図的に **read-first（観測優先）** である。強制ポリシーが設定されていない場合、
フックは *観測されるだけで、決してゲートされない*。強制は名前付きの明示的なオプトインであり、
無効なポリシーは **起動時に失敗する** — コネクタは黙ってガバナンスなしに実行されることはない。

## フックチャネルの仕組み

コネクタの OTLP/HTTP レシーバー（デフォルトはループバック `127.0.0.1:4318`）は、
`hook_path`（デフォルト **`/hooks`**）でフックエンドポイントも提供する。開発者マシン上で、
Claude Code のフック設定がそのループバックエンドポイントへフックイベントを POST する —
正確なフック設定の構文は Claude Code 自身のドキュメントに属する。この製品が所有するのは
レシーバーと以下のポリシーである。

同一のツール呼び出しに関するフックイベントと OTLP テレメトリは **相関付けられる**
（`correlation_window`、デフォルト 5 秒が、一方をもう一方の到着を待たせる）。これにより、
ゲートされたアクションとそのテレメトリは、切り離された 2 つのレコードではなく、
一貫した 1 つのストーリーとして着地する。フックを送り続けるのに `silence_threshold`
（デフォルト 2 分）を超えて OTLP が沈黙するセッションは、テレメトリのギャップとして
フラグが立つ — これが回避（anti-evasion）のシグナルである。

## 強制を有効にする

ソースの設定（`OLIVARES_SOURCES_CONFIG`）に `enforcement` ポリシーを追加する：

```json
{
  "sources": [{
    "name": "claude",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "enforcement": "{\"rules\":[{\"tool\":\"Bash\",\"decision\":\"ask\",\"reason\":\"shell needs a human\"},{\"resource_kind\":\"file\",\"mode\":\"write\",\"decision\":\"deny\"}]}"
    }
  }]
}
```

ルールはツール名および/またはリソース種別とアクセスモードで合致する。決定は `deny` または
`ask`（セッション内の人間へエスカレーションする）である。合致する `PreToolUse` /
`PermissionRequest` フックは、その決定を Claude Code の `permissionDecision` として受け取る。
それ以外はすべて観測されたまま通過する。各ゲートは **finding** として記録されるため、
強制の証跡は言い伝えではなくクエリ可能である。

:::note[キルスイッチはすべてに優先する]
estate（または特定のエージェント）が
[緊急停止](/ja/how-to/cookbook/kill-switch-drill/)下にある場合、このポリシーに関わらず
`claude.tool.use` はガバナンス層で停止される — stop ゲートは個々のツールルールより前に
チェックされ、フェイルクローズドで動作する。
:::

## フリートの姿勢：managed settings を観測する

フックでの強制は 1 つの層である。フリート全体の層は Claude Code の
**managed settings** ファイルであり、`managed-settings` ソースがこれを read-only で観測する：

```json
{
  "sources": [{
    "name": "fleet-policy",
    "kind": "managed-settings",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/managed-settings.json",
      "expected_policy": "{…governance-authored intent…}"
    }
  }]
}
```

| キー | デフォルト | 意味 |
|---|---|---|
| `config_path` | `/etc/claude-code/managed-settings.json`（Linux） | ホスト上の稼働中の managed-settings ファイル（macOS: `/Library/Application Support/ClaudeCode/…`） |
| `scope` | OS ホスト名 | 帰属スコープ（ホスト ID / ディストリビューション名） |
| `expected_policy` | — | 任意の作成済み意図。設定すると、コネクタは **ドリフト**（permitted ポリシー対 observed 設定）を報告する。空 = 観測のみ |

`claude` ソース上の関連するオプトイン観測機能：`managed_mcp_path`（managed-MCP
allowlist の評価順序をモデル化し、name のみの allow エントリにフラグを立てる）と
`sandbox_path`（サンドボックスのロックダウン設定に関する姿勢 finding）— どちらも read-only で、
ファイルを指定するまではオフである。

## コンソールで見えるもの

**Claude Code governance** は作成と truth-loop の画面である。意図したポリシー、ホストが実際に
持っている設定、そしてその間のドリフトを表示する。ゲートとテレメトリギャップの finding は
**Security** に着地し、セッション自体は **Sessions** で引き続き見える：

<img class="light:sl-hidden" src="/console/claude-policy-dark.png" alt="Claude Code ガバナンスビュー — ポリシー作成とフリートの姿勢を 1 か所に。" />
<img class="dark:sl-hidden" src="/console/claude-policy-light.png" alt="Claude Code ガバナンスビュー — ポリシー作成とフリートの姿勢を 1 か所に。" />

## 正直な限界

- **PEP はフックが報告するものをゲートする。** フックが設定されていないホストはゲートされない —
  欠落が見えるようにフリートを
  [managed-settings 観測機能](#フリートの姿勢managed-settings-を観測する)と組み合わせ、
  盲点にならないよう
  [カーネルバックストップ](/ja/how-to/connectors/ebpf-tetragon/)と組み合わせること。
- **`ask` はセッション内の人間に委ねる** — これはロックではなく摩擦である。
  `deny` がロックである。
- **サブプロセスはここでは対象外である**（フックは Claude Code 自身のツール呼び出しに対して
  発火する）。テレメトリ env が何に到達し何に到達しないかは
  [エンタープライズ OTel ページ](/ja/how-to/claude-code-enterprise-otel/)を参照。

## 関連

- [Claude Code を接続する](/ja/how-to/connect-claude-code/) — 観測側。
- [Claude Code のエンタープライズ OTel](/ja/how-to/claude-code-enterprise-otel/) —
  フリートのテレメトリ、ラベル、トレーシング。
- [ガバナンスと承認](/ja/how-to/govern-and-approve/) — PEP が接続する認可モデル。
