---
title: "トラブルシューティング (症状 → 診断 → 修正)"
description: >-
  製品自身の runbook から抽出した、オペレーター向けの障害モードガイド:
  起動と初回実行の問題、readiness の失敗、ingest のバックプレッシャー、
  ledger 検証の失敗、そしてエンジンが意図的に出力する警告。
---

各エントリは同じ形式に従います: 目にする症状、それが何かを確認する方法、そして修正です。
引用されているログ行はエンジンの実際の文字列なので、grep で検索できます。より詳細な
runbook が存在する場合、エントリはそれを再導出するのではなく、関連ページへリンクします。

## 初回起動とセットアップ

### セットアップトークンを見逃した

再起動しても **再表示はされません** (トークンのハッシュのみが、データディレクトリの
`setup.token` に保存されます)。まだユーザーが存在しないあいだは、復旧は安全です:
エンジンを停止し、`setup.token` を削除して起動します — 新しいトークンが発行され、
表示されます。これはユーザーが存在しないインストールでの *み* 機能するため、乗っ取りの
経路にはなりません。トークンは **stdout のみ** に出力されます (systemd では journal、
Docker/Kubernetes ではコンテナログ) — ログファイルには決して出力されません。

### `=== FIRST-BOOT SETUP ===` が一度も表示されない

そのデータディレクトリにはすでにユーザーが存在します — 初回起動ではありません。
既存の管理者でログインするか、本当に新規で始めたい場合は、新しい `--data-dir` を
使ってください。

### エンジンが初回起動時に鍵について警告する

```text
generated a new audit signing key; back it up path=/var/lib/olivares/audit-signing.key
generated a self-signed TLS certificate; clients must trust it, or pin it with --pin-sha256=<pin_sha256> (that value, verbatim) cert=/var/lib/olivares/tls.crt cert_fingerprint_sha256=d38567e8…378c4e7f pin_sha256=JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

どちらも意図的なもので、最初のものが後で問題になります: **強制されたエスクローは
存在しません** — 今すぐ `audit-signing.key` をボックス外にコピーし、公開鍵
(`GET /v1/audit/pubkey`) をボックス外でピン留めしてください。さもないと、将来ホストが
侵害された際に、自分自身の ledger を証明できなくなります
([バックアップと復元](/ja/how-to/backup-and-restore/#すべてを決める二つの鍵))。

TLS の行は **2 つ** のダイジェストを出力し、両者は交換できません。
`cert_fingerprint_sha256` は証明書のダイジェストで、ブラウザーが表示するものです。
`pin_sha256` はリーフ証明書の SPKI のダイジェストであり、`--pin-sha256` が比較するのは
こちらだけです。その値をそのままコピーしてください:

```bash
olivares status --server https://127.0.0.1:8443 \
  --pin-sha256 JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

代わりに証明書フィンガープリントをピン留めしても、不正なフラグ値としては失敗しません。
形式として正しい 32 バイトのダイジェストなので、接続が試みられたうえで
`TLS SPKI pin mismatch` で拒否され、そのエラーが本来使うべき値を示します。
`curl --pinnedpubkey sha256//…` を使う場合は、末尾の `=` パディングを補ってください。
エンジンは意図的にパディングなしの base64 を出力します（ログ上で引用符が付かず、
コピー＆ペーストで壊れないため）が、curl はパディング付きの形式を要求します。

## ソースと access map

### map が空である

まず、何かが結線されているかを確認します。エンジンは起動時に明示的にそう述べます:

```text
ingest: no observation sources configured (OLIVARES_SOURCES_CONFIG.sources is empty); no connector will ingest — the estate runs on no live traffic
```

ソースファイルが存在しない、読み取れない、または無効な場合は **警告して続行します**
(起動がそれでクラッシュすることは決してありません) — したがって、健全に見えるエンジンで
map が空である場合、たいていは設定が読み込まれなかったことを意味します。ファイル/パスを
修正して再起動してください。成功するとソースごとに `ingest: wired source … kind=…` と
表示されます。構築に失敗したソースは、その理由とともに
`ingest: failed to register in-process source; not wired` を出力します — 報告されるのであり、
黙って破棄されることは決してありません。

### pgAudit は結線されているが edge が届かない

ほぼすべてのケースを 3 つの原因がカバーし、いずれも設計上のものです
([pgAudit ガイド](/ja/how-to/connectors/pgaudit/)):

1. **サーバーが UTC でログを記録していない。** UTC 以外のゾーン略称を持つレコードは、
   誤ったタイムスタンプを付けるのではなく **スキップ** されます — `log_timezone = 'UTC'`
   を設定してください。
2. **csvlog はバッチであり、tail ではない。** `follow` は `jsonlog` にのみ適用されます。
   csvlog ソースは継続的にではなく、各実行時に ingest します。
3. **監査対象のクラスがオフになっている** — `pgaudit.log` に `read, write` が含まれて
   いるか確認してください。

### すべてが drift として表示される

新規インストールでは想定どおりです: grant が一切宣言されていないため、観測された
すべてのアクセスは正直なところ「予期しない」ものです。これはバグではなく、開始状態です —
意図する grant を宣言して [トリアージ](/ja/how-to/cookbook/drift-triage/) してください。

## 可用性

### `/readyz` が 503 を返す

ボディを読んでください — 2 つのケースを区別します:

- `{"status":"unavailable","store":"down"}` — ストアに到達できません。SQLite の場合:
  ディスク満杯、PVC の問題、ファイルパーミッション。Postgres の場合: 到達性と認証情報。
  **liveness は意図的に通り続ける** ため (プロセスは生きている)、ストア障害で
  再起動ループに陥るものはありません。ストアを修正した後も詰まったままなら、
  pod/サービスを手動で再起動してください。
- `{"status":"standby","leader":false,…}` — HA standby が正直に応答しています。
  エラーではありません: Service はリーダーへルーティングし、standby は設計どおり drain
  します。**すべての** レプリカが standby を報告する場合、リーダー選出が詰まっています —
  Postgres の advisory-lock 接続を確認してください。

### pod が死んだのに何も引き継がれない

**デフォルトの単一レプリカ** トポロジーでは自動フェイルオーバーはありません — 復旧は
StatefulSet の再スケジューリングと RWO ボリュームの再アタッチです (Multi-Attach
エラーに注意。ボリュームは復旧をその AZ に固定します)。自動フェイルオーバーは
[HA トポロジー](/ja/tutorials/getting-started/kubernetes/#3-active-passive-ha)
(Postgres + レプリカ + 共有署名鍵) の特性です。永続化を無効にして本番を運用しては
いけません: `emptyDir` は再スケジューリングのたびに署名鍵を失います。

## パフォーマンス

### Ingest レイテンシの p99 が上昇している (バックプレッシャー)

バスは **ドロップせずブロックします** — `olivares_ingest_duration_seconds` の p99 上昇は、
データ損失ではなく、サブスクライバーが飽和していることを示す設計どおりのシグナルです。
原因を直接特定します:

```promql
olivares_eventbus_queue_depth / olivares_eventbus_queue_capacity > 0.9
```

サブスクライバーごとのラベルが遅いモジュールを指し示します。
`olivares_eventbus_publish_blocked_total` はバックプレッシャーイベントをカウントします。
よくある根本原因は **ストアの書き込みスループット** (SQLite の単一ライター上限) です —
これはチューニングのつまみではなく、容量の修正です (Postgres に移行するか、書き込み
増幅を減らす)。遅い出力コネクター (webhook、SIEM) を同期サブスクライバーにしては
いけません。

分散バスを有効にしている場合 (`OLIVARES_BUS_CONFIG`)、ノード間ブリッジが **最大 1 回
(at-most-once)** であることを忘れないでください: 飽和したブリッジは
`olivares_eventbus_bridge_pending_messages` を満たし、その後 **リモートイベントを
ドロップ** します。これは `olivares_eventbus_bridge_dropped_total` でカウントされます —
増加があればアラートを発し、`olivares_eventbus_bridge_connected == 0` になったら
呼び出してください。

### ログインが「locked out」で失敗する

`olivares_auth_login_attempts_total{outcome="locked_out"}` の上昇は、繰り返しの失敗の後に
アカウント単位/IP 単位のスロットルが作動したことを意味します。これは自動的に解除されます。
上限を引き上げるのではなく、失敗の発生源を調査してください。

## 証跡

### ledger が検証に失敗する

まず、何を実行したかを把握してください: デフォルトの `audit verify` は **チェーンが
失敗しても 0 で終了します** (結果は JSON レポートに含まれます) — 自動化では
`--strict` を使うか、レポートをパースしなければなりません:

```bash
olivares audit verify --tenant $TENANT --data-dir /var/lib/olivares --strict \
  --pubkey <BASE64-PINNED-OFF-BOX>
```

**ボックス外** の公開鍵をピン留めしてください: ピンがない場合、検証器は (侵害された
可能性のある) ホストから読み取った鍵を信頼します — 参考のチェックとしては問題ありませんが、
改ざんの証拠としては不十分です。次に `reason` フィールドで分類します:

| Reason | クラス | 対応 |
|---|---|---|
| `hash-mismatch`, `prev-mismatch`, `head-mismatch`, `tail-truncated` | 改ざんまたは切り詰め | SEV1 として扱う: ボックスを保全し、ボックス外のチェックポイントと突き合わせる |
| `checkpoint-sig-invalid`, `checkpoint-link-mismatch`, `event-sig-invalid` | 改ざんまたは誤った鍵 | 鍵管理の取り違えを証明できない限り SEV1 |
| `seq-gap` | 削除 **または** 復元の不整合 | 改ざんと騒ぐ前にボックス外のチェックポイントと比較する |
| `event-sig-missing` | 署名が有効化される前のレガシーレコードの可能性 | 有効化境界で `--from` を使って範囲を限定する。境界より前の不在は想定どおり |

素朴な walk は通過するが、ピン留めしたボックス外のチェックポイントと一致しない復元済み
バックアップは、復元アノマリーのケースです — その比較こそがピンが存在する理由です。

### `olivares_audit_checkpoint_age_seconds` が増え続ける

チェックポイントの書き込みが停止しています (デフォルトの間隔は 1h。
`OlivaresAuditCheckpointStale` は 2h で発火します)。エンジンログでチェックポイントの
エラーを、ストアの書き込み可能性を確認してください — 増えているあいだ、あなたの
改ざん証跡のアンカーは古くなっていきます。

## 通知とシンク

### 宛先が何も受信しない

不明な kind を持つ宛先は **スキップされ、ログに記録されます**
(`notify: destination has unknown connector kind; skipped` — `kind` のスペルを確認して
ください)。イベンティングのシンクについては、`POST …/subscriptions/{id}/test` が観察
できる配信を送り、エンドポイントは HTTPS でなければなりません
([SIEM へプッシュする](/ja/how-to/cookbook/push-to-siem/))。

---

症状がここになく、エンジン自身のメッセージでも説明がつかない場合、それはドキュメントの
バグです — そのログ行を添えて報告してください。
