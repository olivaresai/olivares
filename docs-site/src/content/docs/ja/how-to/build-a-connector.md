---
title: コネクターをビルドして配布する
description: >-
  公開された Apache-2.0 のコネクター SDK で、サードパーティコネクターをスキャフォールド、
  実装、テスト、署名、配布する —— そして、デニークローズドな署名済みアドミッションで
  コントロールプレーンに組み込む。
---

本ガイドは、オペレーターがコントロールプレーンに組み込める**署名済みのサードパーティ
コネクター**を、ゼロから作り上げるところまで案内します。コネクター SDK は Apache-2.0 であり、
AGPL のエンジンからは何もインポートしません。したがって、あなたのコネクターは**あなたの**
リポジトリでビルドされた、**あなたの**ライセンス下の**あなたの**コードです。

あなたが作るのは通常の Go プログラムです。すなわち、`sdk.SourceConnector`（事実を収集し、
観測を発行する）、`sdk.OutputConnector`（通知を配信する）、または `sdk.ContentSource`
（ガバナンス対象の知識に文書と ACL 参照を提供する）を実装する型であり、エンジンが
プロセス外で起動して gRPC で通信する [go-plugin](https://github.com/hashicorp/go-plugin)
バイナリ（相互認証されたループバック、AutoMTLS）としてパッケージ化されます。コネクターの
*モデル* —— observe-only、最小データ、三つの観測種別 —— については、まず
[ソースを接続する](/ja/how-to/connect-a-source/) をお読みください。

:::note[安定性]
SDK の契約（`Descriptor/Open/Gather/Close`、ワイヤー、プラグインのハンドシェイク）は
**stable v1** です —— [API 安定性](/ja/reference/api-stability/) とリポジトリの
`sdk/VERSIONING.md` を参照してください。最初の公開 semver タグが公開されるまでは、
リポジトリのチェックアウトに対してビルドしてください（後述の `-sdk-path`）。
:::

## 1. スキャフォールド

推奨される高レベル CLI：

```sh
# from the repository checkout root
go run ./cmd/olivares connector init acme.widget-audit \
  --dir ~/olivares-connector-widget \
  --module github.com/acme/olivares-connector-widget \
  --template access-edge-source \
  --plugin \
  --sdk-path "$PWD/sdk"
```

5 つのアーキタイプから 1 つ選択します。これらは安定した SDK サーフェスに対する
プリセットであり、新たな作成者契約ではありません。

| テンプレート | 宣言されるサーフェス | 用途 |
|---|---|---|
| `content-source` | `knowledge.document` | プロセス外のコンテンツソースを含む、ガバナンス対象の知識取り込み用文書。 |
| `access-edge-source` | `observation.edge` | アクセスグラフ、アイデンティティ、SaaS、インフラ関係の事実。 |
| `output-sink` | `notify.sink` | 通知またはチケットシンク。 |
| `agent-surface` | `observation.edge`, `observation.finding` | アクセスエッジとファインディングを報告するエージェントランタイムアダプター。 |
| `model-provider` | `observation.cost`, `observation.edge` | プロバイダーのインベントリ、使用状況、コストの観測。モデルガバナンスはエンジン側に残る。 |

旧来の単独スキャフォールドも引き続き有効であり、同じ安定した作成者契約を生成します。

これをリポジトリのチェックアウトから実行してください（最初の公開 SDK タグが公開されるまでは、
パッケージはワークスペースを通じて解決され、`-sdk-path` はそのチェックアウトの `sdk/` を
指します）。

```sh
# from the repository checkout root
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ~/olivares-connector-widget \
  -name acme.widget-audit \
  -module github.com/acme/olivares-connector-widget \
  -kind source -plugin \
  -sdk-path "$PWD/sdk"
```

完全なリポジトリが得られます。コネクターのスケルトン、ライフサイクルテスト、プラグインの
`main`、このライフサイクル全体を記した README、そして `scripts/check-boundary.sh` ——
**私たちの CI が実行するのと同じライセンス境界チェック**を、あなた用に。`-name` は
あなたの `Descriptor.Name` です。グローバルに一意で、ドット区切り、`<vendor>.<connector>` の
形式です。

## 2. 実装

契約を簡潔に（`sdk.SourceConnector` の godoc が規範的です）:

- **`Open`** は設定を読み取ります（`Descriptor.ConfigFields` で宣言され、シークレットは
  *参照*であり、`Secret: true` でマークされ、決してインライン化されません）。失敗は
  `Gather` ではなくここで起こすこと。
- **`Gather`** はエンジンの `Sink` に観測を発行します。**エンジンがスケジューリングを所有します**。
  バッチソースは自身の仕事を行って戻り、ストリーミングソースは `ctx` がキャンセルされるまで
  ブロックします。自前のティッカーを決して所有しないこと。
- 配信は**at-least-once**です。コンシューマーは観測の自然キーで重複排除します。配信状態を
  追跡しないこと。
- **最小データ**: 参照とメタデータを発行し、ペイロード、プロンプト、シークレット値は
  決して発行しないこと。
- `content-source` では、**`List`** は安価に列挙できる参照を返し、
  **`Fetch`** は 1 つの文書本文を返します。オプションの `DeltaContentSource` は
  ライブデルタと ACL 更新を追加します。このオプションインターフェースを実装する
  コンテンツソースプラグインは `content.delta` を自動宣言し、ホストはその機能が
  宣言された場合にのみデルタメソッドを呼び出します。

テストを実行し、次に CI でライセンス境界を証明します。

```sh
go test ./...
./scripts/check-boundary.sh   # fails if anything links github.com/olivaresai/olivares/core
```

## 3. パッケージ化と署名

プラグインバイナリをビルドし、そのダイジェストを固定し、サプライチェーンの
アテステーションを **Sigstore バンドル**として添付します。コントロールプレーンは SLSA
プロベナンスまたは SBOM アテステーション（SPDX / CycloneDX のプレディケート）を検証します
—— 自前の鍵（ここで示すもの）で署名するか、CI アイデンティティでキーレスに署名します。

```sh
go build -trimpath -o widget-audit ./cmd/acme-widget-audit
sha256sum widget-audit

# keyed (the dev loop: trust your own public key)
cosign generate-key-pair
cosign attest-blob --key cosign.key \
  --type slsaprovenance1 --predicate provenance.json \
  --bundle widget-audit.sigstore.json widget-audit

# keyless alternative (CI): same command with --yes and an OIDC identity,
# or GitHub artifact attestations (gh attestation download produces the bundle).
```

## 4. 配布

バイナリ、その `sha256`、および `.sigstore.json` バンドルとともに **GitHub リリース**を
公開します —— あるいは、同じアーティファクトを `oras push` で OCI レジストリにプッシュします
（リファラーとしてのアテステーション）。semver でバージョン管理し、ビルド対象とした
`ProtocolVersion`（今日は v1）を README で宣言してください。

## 5. 運用（あなたのユーザーが行うこと）

オペレーターはホスト上にバイナリとバンドルを配置し、ソース設定（`OLIVARES_SOURCES_CONFIG`）で
**ダイジェストとトラストの両方**を固定します。

```json
{
  "connector_trust": {
    "trusted_keys": ["-----BEGIN PUBLIC KEY-----\n…acme's cosign.pub…\n-----END PUBLIC KEY-----\n"],
    "allowed_predicates": ["https://slsa.dev/provenance/v1"]
  },
  "sources": [
    {
      "name": "widget-prod",
      "tenant": "<tenant-id>",
      "config": { "endpoint_ref": "…" },
      "plugin": {
        "path": "/opt/olivares/plugins/widget-audit",
        "sha256": "<the released digest>",
        "bundle": "/opt/olivares/plugins/widget-audit.sigstore.json"
      }
    }
  ]
}
```

アドミッションは**デニークローズドで、抜け道はありません**。トラストアンカーがない、バンドルが
ない、ダイジェストの不一致、信頼されていない署名者、誤ったプレディケート型 —— これらはいずれも、
ソースが**組み込まれない**ことを意味します（起動時に理由が示されます）。成功すると、エンジンは
exec 時にバイナリを再ハッシュし（go-plugin の `SecureConfig`）、検証されたバイトが実行される
バイトとなり、サブプロセスのチャネルは AutoMTLS で固定されます。

コンテンツソースプラグインは、`documents` 設定ブロック内で同じルート
`connector_trust` と、ソースごとの同じ `plugin { path, sha256, bundle }` 形式を
使用します。それらは知識取り込み用の第一級のプロセス外コンテンツソースです。

トラストアンカーは**必須**です —— `trusted_roots` も `trusted_keys` も持たない
`connector_trust` は、はっきりと拒否されます。**キーレス**署名の場合、アンカーは Fulcio
（またはプライベート CA）のルートであるため、オペレーターは `trusted_roots`（ルート PEM、
たとえば `cosign initialize` から）に**加えて** `allowed_identities` と `allowed_issuers`
（両方を、ともに —— 署名が持たなければならない SAN アイデンティティと OIDC 発行者）を設定します。
置き換わるのは `trusted_keys` だけです。上記の素の鍵の例が、最もシンプルなアンカーです。

## 6. 認証を受ける（任意だが推奨）

二つの補完的な記録:

- **製品内認証** —— あなたのユーザーが、あなたのコネクターをカタログエントリ
  （種別 `connector`、モジュール XIV）としてキュレートし、あなたのリリース済みダイジェストに
  対して、検証済みのプロベナンス/SBOM アドミッション判定を記録します
  （`POST /entries/{id}/admit`）。`require_signed` が有効な場合、承認はその判定に基づいて
  デニークローズドになります。[モジュール XIV](/ja/reference/modules/xiv-catalog/) を参照してください。
- **検証済みコネクターインデックス** —— あなたのコネクターを
  [検証済みコネクター](/ja/reference/verified-connectors/) への掲載のために提出します。
  メンテナーがあなたのリリースを再検証し（境界、署名、プロベナンス、最小データのレビュー）、
  掲載します。このインデックスは検証を文書化するものであり、トラストルートでは**ありません**
  —— オペレーターは依然として*あなたの*アイデンティティ/鍵を自分自身で固定します。

## 構造上ガバナンス対象

強制は構造上エンジン側にあります。コネクターはガバナンスコードをリンクせず、
オプトアウトできません。エンジンは、設定されたソースアイデンティティ
（`source_type`, `source_ref`）をキーとして制御を適用し、ソーススコーピング、ACL 交差、
DLP/検索スキャン、アドミッション、監査を適用します。`Descriptor.Surfaces` は参考メタデータとしてのみ
扱われ、強制の入力には決して使われません。

プライベートコネクターも第一級です。コネクターを組織内に保ち、公開も公開リストへの
掲載もしなくても、オペレーターがバイナリダイジェストと信頼ルートを固定すれば、
引き続きガバナンス対象となります。検証済みコネクターインデックスは認証を文書化するものであり、
信頼ルートではありません。

## 正直な限界（v1）

- 外部の組み込みは**観測ソース**と**コンテンツソース**を対象とします。出力コネクターも同一の方法でビルドして
  配布できますが、notify の構成はまだ外部の出力プラグインを読み込みません。
- プロセス外の**モジュール**は利用できません（プロトは凍結されており、ホストのグルーは
  意図的に未接続です）。
- 観測の sum type は**封緘されています**。エッジ、コストサンプル、ファインディングを
  発行できます（開かれた文字列の語彙を伴います）が、新しい観測種別を定義することはできません。
