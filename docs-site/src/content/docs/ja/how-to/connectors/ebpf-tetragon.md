---
title: "eBPF / Tetragon（カーネルバックストップ）"
description: >-
  access map の非協調的な側を配線する：Tetragon はエージェントの制御外でカーネルの
  ファイル・ネットワークイベントを取得し、コネクタはその JSON エクスポートを正直に近似的な
  アクセスエッジに変換する — さらにオプトインの回避検出器を備える。
sidebar:
  order: 3
---

`ebpf` ソースは R/RW マップの **回避対策（anti-evasion）側** である。協調的な経路は
エージェントが *報告する* ものを見るのに対し、こちらはカーネルが *実際に行った* ことを見る —
ファイルの read/write と外向きの接続 — エージェントが自身のテレメトリを無効化していても、
これは **エージェントの制御外** で動作するためである。

これを定義するのは 2 つの設計判断であり、両方ともがセキュリティ姿勢そのものである：

- **これ自体は eBPF プログラムをロードしない。** [Tetragon](https://tetragon.io) が
  カーネルのキャプチャを行い、`CAP_BPF` + `CAP_PERFMON` を保持する独立したハードンされた
  サービスとしてデプロイされる。コネクタは Tetragon の JSON イベントエクスポート
  （共有ファイル/FIFO、モード `0600`、または stdin）の **ケイパビリティを持たない read-only の
  コンシューマー** である。
- **TLS のボディやペイロードに対しては盲目である。** アクセスの関係性を観測する —
  決して内容ではない。

リポジトリは `connectors/ebpf/deploy/` の下にリファレンスデプロイメントを同梱する：
ハードンされた Tetragon DaemonSet、2 つの TracingPolicy（ファイルアクセス、ネットワーク）、
そして単一ホスト向けの Compose バリアントである。

## 出力するもの

| フィールド | 値 |
|---|---|
| Signal source | `ebpf` |
| Mode | ファイルの `read` / `write`、ネットワーク接続のエッジ |
| Origin | **ランタイム identity**（プロセス/コンテナ） — kind は `identity`、決して解決されたエージェントではない |
| Confidence | **常に `approximate`** — 下記参照 |
| Coverage tier | カーネルバックストップ |

`approximate` は控えめなのではなく正確である：*アクセス* はカーネルのグラウンドトゥルース
（syscall は実際に起きた）である。カーネルが与えられないのは *エージェント* である —
プロセスと cgroup は分かるが、それがガバナンス対象のどのエージェントだったかは分からない。
access-map モジュールは、identity ソースがランタイム identity をエージェントに束縛したとき、
帰属をアップグレードする。

## 1. Tetragon をデプロイする（センサー）

Kubernetes では、同梱の DaemonSet と TracingPolicy を適用する：

```bash
kubectl apply -f connectors/ebpf/deploy/tetragon-daemonset.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-file-access.yaml
kubectl apply -f connectors/ebpf/deploy/tracingpolicy-network.yaml
```

Tetragon はその JSON エクスポートを共有ボリューム
（`/var/run/olivares/tetragon.log`）に書き込み、コネクタは反対側からそれを読む。
単一ホストでは、`connectors/ebpf/deploy/docker-compose.yaml` が Kubernetes なしの
同じ分割である。完全なアーキテクチャとハードニングのノートは
`connectors/ebpf/deploy/README.md` にある。

## 2. ソースを宣言する

```json
{
  "sources": [{
    "name": "node-kernel-backstop",
    "kind": "ebpf",
    "tenant": "<tenant-id>",
    "config": {
      "events_path": "/var/run/olivares/tetragon.log",
      "detect_evasion": "true"
    }
  }]
}
```

| キー | デフォルト | 意味 |
|---|---|---|
| `events_path` | `-`（stdin） | Tetragon の JSON イベントストリーム — ファイル、FIFO、または stdin |
| `follow` | `true` | ストリームが伸びるにつれて読み続ける |
| `detect_evasion` | `false` | オプトイン：協調的テレメトリが沈黙する一方でカーネルが依然としてその動作を見ている、既知のエージェントプロセスにフラグを立てる |
| `evasion_window` | `5m` | 協調的な接続が欠落していることにフラグを立てるまでの猶予期間 |
| `agent_signatures` | `claude,claude-code` | 検出器が協調的エージェントとして分類する実行可能ファイル名 |
| `otlp_endpoints` | `127.0.0.1:4317,127.0.0.1:4318` | 検出器が接続を相関付ける、協調的テレメトリのエンドポイント |

コネクタは Tetragon の `ProcessKprobe` イベント（ファイル操作とネットワーク接続）と
`ProcessExit`（検出器の状態）を消費する。`ProcessExec` は帰属のコンテキストに使われ、
エッジとして出力されることは決してない。

## 3. コンソールで見えるもの

カーネルのエッジはランタイム identity に帰属して access map に加わり、常に `approximate` と
マークされる。検出器の出力は finding として **Security** に着地する — カーネルが依然として
活動を見ている一方で発信を止めるセッションこそ、このソースが存在する理由である：

<img class="light:sl-hidden" src="/console/security-dark.png" alt="estate の探索的ソースからの finding を一覧表示する Security ビュー。" />
<img class="dark:sl-hidden" src="/console/security-light.png" alt="estate の探索的ソースからの finding を一覧表示する Security ビュー。" />

## 正直な限界

- **そのエンドツーエンドの帰属の深さは、まだ実証の途上にある。** 協調的経路と
  ストアネイティブの監査が検証済みで高忠実度のシグナルである。カーネルバックストップは
  床上げの手段として扱い、完成した主要ソースとしてではない
  （[正直さと限界](/ja/start/honesty-and-limits/)）。
- **Tetragon のスコープはその TracingPolicy である。** 同梱のポリシーはファイルアクセスと
  ネットワーク接続をカバーする。それらがトレースしないものはエクスポートに存在しない。
- **プロセス ≠ エージェント。** identity の束縛がなければ、すべてのカーネルエッジは
  `approximate` のままである — 事故ではなく設計上のものである。

## 関連

- [Claude Code を接続する](/ja/how-to/connect-claude-code/) — これがバックストップする協調的な側。
- [SSO/SCIM と identity ソース](/ja/how-to/connectors/sso-scim-identity/) — 帰属がどうアップグレードされるか。
- [セキュリティハードニング](/ja/how-to/security-hardening/) — バックストップが防御姿勢のどこに収まるか。
