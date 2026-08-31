---
title: "モジュール III — read/write アクセスマップ"
description: >-
  差別化された主要機能の一つ。あらゆる origin→resource エッジの read/write アクセスマップと、
  Permitted-vs-Observed 差分(最小権限ドリフト)。エッジがどのように構築・分類・信頼されるか、
  そしてその限界。
---

モジュール III は **read/write アクセスマップ**である。どの origin(エージェント、ID、
セッション)がどのリソースに触れ、それが read か read-write に分類され、そして最小権限
ドリフトを浮かび上がらせる **Permitted-vs-Observed 差分**は何か、を示す。これは本製品の
最も有用で差別化された機能の一つである。30 モジュールのうちの一つであり、製品全体ではない。
本ページは、マップが何であるか、そしてそれを誠実に読む方法についてのリファレンスである。

## エッジ

マップは**エッジ**のグラフである。各エッジは正規化された minimal-data のファクト
`origin → resource` であり、以下を持つ。

| フィールド | 値 | 意味 |
|---|---|---|
| **mode** | `read` \| `write` \| `readwrite` \| `unknown` | read/write 分類(判定できない場合は `unknown` — 推測は決してしない) |
| **source** | `otel` \| `mcp_annotation` \| `pg_audit` \| `cloudtrail` \| `ebpf` \| `policy` \| `a2a` | どのシグナルがエッジを生成したか |
| **confidence** | `attributed` \| `approximate` | アクセスが origin にどれだけ確実に紐づいているか |

エッジは [`edge.observed`](/ja/reference/events/) イベントとしてイベントバスに到着し、
エンジンはそれらを永続化された `AccessEdge` エンティティにマージする。このエンティティ自体が
**permitted** 側と **observed** 側の両方を持つため、アクセスマップは別個のストアではなく、
**一般データモデル上のビュー**である。

## エッジはどのように構築されるか

モジュール III は 2 つの経路を横断する。

- **協調的経路** — OpenTelemetry(`otel`)を発行し、MCP サーバーを公開する
  エージェント。**ネイティブストア監査**と組み合わせることで、これは高忠実度となる。
  Postgres pgAudit(`pg_audit`)は READ/WRITE をそのまま分類し、AWS CloudTrail
  (`cloudtrail`)は S3 の `readOnly` を提供し、ウェアハウスも同様である。
- **非協調的経路** — カーネルレベルの **eBPF/Tetragon バックストップ**(`ebpf`)が、
  エージェントの制御外で(回避対策)、syscall レベルで `MAY_READ`/`MAY_WRITE` を
  記録する。暗号化された本体に対しては盲目である。

MCP ツールアノテーション(`readOnlyHint`/`destructiveHint`、source `mcp_annotation`)は
有用なシグナルだが、**MCP 仕様により信頼されていない**。本製品はそれらを**裏付け**、
単独で信頼することは決してない。

**permitted** 側(source `policy`)は宣言された付与から来る。**observed** 側は
上記のシグナルから来る。

## Permitted vs Observed(最小権限ドリフト)

決定的なビューは、ある origin が触れることを*許可されている(permitted)*ものと、実際に
触れていると*観測されている(observed)*ものとの間の**差分**である。これは以下を浮かび
上がらせる。

- **想定外のアクセス** — origin が付与されたことのないリソースを使用した。
- **未使用の付与** — どの origin も行使したことのないパーミッション。
- **照合保留中** — システムがまだ確実に帰属できないアクセス。

[ゼロからグラフへのチュートリアル](/ja/tutorials/zero-to-graph/)は、デモエステート上で
ポピュレートされたドリフト結果に到達する。

:::caution[誠実な限界]
- **エージェントごとの ID はハードな依存関係である。** 監査はアクティビティを資格情報や
  ロールに帰属させるのであり、本質的にエージェントに帰属させるわけではない。コネクション
  プールを持つ共有サービスアカウントは、帰属を `approximate` へと縮退させる。適切に
  ガバナンスするには、エージェントごとに ID を発行する必要がある(モジュール VI への橋渡し)。
- **カバレッジはティア化されている。** ネイティブ監査を持つストア(SQL、オブジェクト
  ストレージ、ウェアハウス)では*クリーン*、一部のストア(ドキュメント/ベクター)では
  *ロッシー*、その他(例: Redis、SQLite、D1)では**受動的には再構成不可能**である。
  カバレッジがロッシーまたは欠落している箇所では、エッジが存在しないことはアクセスが
  起きなかったことの証明には**ならない**。
- **`unknown` と `approximate` は隠されず表示される。** 本製品は、持っていない分類や
  確実性を決して捏造しない。
:::

## マップを読む

アクセスマップの結果(Permitted-vs-Observed ドリフトを含む)は、安定コア契約ではなく、
別の **beta** [module-route リファレンス](/reference/api-beta/) で公開されるモジュールルートに
よって提供される。そのフィールドレベルの形状は本製品の型付き Go/TypeScript インターフェイスに
存在し、Web UI は
それらの上にグラフとドリフトオーバーレイをレンダリングする。アクセスグラフの読み取りは
**特権的で、テナントスコープの、完全に監査される**操作である(editor ロール以上であり、
最下位の閲覧者では決してない)。[セキュリティモデル](/ja/explanation/security/security-model/)
および [脅威モデル](/ja/explanation/security/threat-model/) を参照。

## 関連

- [イベントバスリファレンス](/ja/reference/events/) — `edge.observed` イベントとそのペイロード。
- [アーキテクチャ概要](/ja/explanation/architecture/overview/) — モジュール III の位置づけ。
- [ガバナンスと承認](/ja/how-to/govern-and-approve/) — ドリフトへの対処。
