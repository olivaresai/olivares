---
title: このドキュメントの構成
description: >-
  本ドキュメントは Diátaxis に従っています — 4 つのモード（チュートリアル、ハウツーガイド、
  リファレンス、解説）がそれぞれ異なるニーズに答えます。ここではその辿り方を説明します。
---

このドキュメントは **[Diátaxis](https://diataxis.fr/start-here/)** フレームワークで構成されています。
Diátaxis は、技術文書が 4 つの異なるニーズに応えること、そしてそれらを混在させると誰にとっても
ドキュメントが悪くなることを観察しています。そのため、サイドバーの最上部は製品機能のリストではなく
**4 つのモード**です。

| モード | 志向 | 答えるもの | あなたが…の時 |
|---|---|---|---|
| **[チュートリアル](/ja/tutorials/zero-to-graph/)** | 学習 | 「何もない状態から動く結果まで連れて行ってほしい。」 | 初めてで、手を動かして学びたい |
| **[ハウツーガイド](/ja/how-to/self-hosting/)** | タスク | 「*この特定のこと*をどうやって達成するか?」 | 作業中で、レシピが必要 |
| **[リファレンス](/ja/reference/)** | 情報 | 「API、イベント、モジュール、フラグは正確には何か?」 | これに対して構築中で、精密さが必要 |
| **[解説](/ja/explanation/)** | 理解 | 「*なぜ*こう作られているのか?」 | 評価中で、その理由を知りたい |

何がどこにあるかの簡単なマップ:

- **チュートリアル** — 学習パス: [ゼロから read/write アクセスグラフまで](/ja/tutorials/zero-to-graph/)、
  および実際のシナリオごとの入門 —
  [シングルノード](/ja/tutorials/getting-started/single-node/)、
  [Docker Compose](/ja/tutorials/getting-started/docker-compose/)、
  [Kubernetes](/ja/tutorials/getting-started/kubernetes/)、
  [エアギャップ](/ja/tutorials/getting-started/air-gapped/)。
- **ハウツーガイド** — インストールと運用（[self-host](/ja/how-to/self-hosting/)、
  [バックアップとリストア](/ja/how-to/backup-and-restore/)、
  [モニタリング](/ja/how-to/monitor-with-prometheus/)、
  [トラブルシューティング](/ja/how-to/troubleshooting/)）、
  [コネクタ別ガイド](/ja/how-to/connectors/pgaudit/)（pgAudit、CloudTrail、eBPF、
  Claude Code、MCP、アイデンティティ）、そしてガバナンスレシピの
  [クックブック](/ja/how-to/cookbook/deny-closed-policies/)（deny-closed ポリシー、予算、承認、
  ドリフトトリアージ、kill switch、SIEM プッシュ）。
- **リファレンス** — [REST API](/reference/api/)（製品自身の OpenAPI 3.1 コントラクトから
  レンダリング）、[API 安定性ポリシー](/ja/reference/api-stability/)、
  [イベントバス](/ja/reference/events/)（AsyncAPI 3.0 コントラクト）、
  [モジュールカタログ](/ja/reference/modules/overview/)、
  [CLI](/ja/reference/cli/) と[構成](/ja/reference/configuration/)。
- **解説** — [アーキテクチャ](/ja/explanation/architecture/overview/)、
  [セキュリティモデル](/ja/explanation/security/security-model/)と
  [脅威モデル](/ja/explanation/security/threat-model/)、
  [オープンコアのライセンス](/ja/explanation/open-core-and-licensing/)。

## 規約

- **検索**はローカルかつクライアントサイド（Pagefind）です。完全にあなたのブラウザ内で動作し、
  外部の検索サービスには何も送信されません。境界を越えるものをあなたが決める、製品の
  self-hosted 設計と一貫しています。
- **バージョン管理。** ドキュメントはバージョン管理されています。新しい製品バージョンが
  出荷されると、前のバージョンのドキュメントは保存されます。バージョンセレクターは上部バーに
  あります。
- **限界について誠実。** 機能が設計段階、v1 以降、または単に未構築の場合、ドキュメントは
  はっきりそう述べます。[誠実さと限界](/ja/start/honesty-and-limits/)を参照してください。
  チュートリアルとハウツーのコマンドは**書かれたとおりに実行できる**ことを意図しています。
- **言語。** 正規のドキュメントは英語です。スペイン語、簡体字中国語、ロシア語、日本語、
  ドイツ語、フランス語の翻訳が利用可能です（機械翻訳であり、英語が正典です。まだ翻訳されて
  いないページは英語にフォールバックします）。
