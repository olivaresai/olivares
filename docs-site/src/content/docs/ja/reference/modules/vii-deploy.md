---
title: "モジュール VII — デプロイと統合"
description: >-
  インフラストラクチャに対して作用する唯一のモジュール。エージェントと MCP
  サーバーの宣言的ライフサイクル、およびそれらの estate への配線を計画・統制する。
  変更操作は HITL でゲートされ、適用前にドライランされ、可逆である。さらにライブ適用は、
  executor がプロビジョニングされるまで deny-closed（503）のままとなる。
---

モジュール VII は、顧客のインフラストラクチャを変更（mutate）する**唯一**のモジュールである。
製品の他のすべての部分は read-first である。本モジュールは、エージェントと MCP サーバーを
**宣言的・バージョン管理・可逆**な操作としてプロビジョニング・更新・廃止し、エージェントが
エンタープライズリソースに到達するために使用する接続性と参照する identity を宣言する。
作用するがゆえに、そのセキュリティ基準は製品中で最も高く、ライブのアクチュエーション
（actuation）は、オペレーターが明示的にプロビジョニングするまで deny-closed のシーム背後に
保持される。

## まず計画と統制、その後に（場合により）適用

ライフサイクルは `plan → apply → verify → retire` であり、**desired（望ましい）**状態を
**real（現実の）**状態に対して調整（reconcile）する。重要な区別は**宣言 ≠ 変更**である。

- 望ましい状態の**宣言** — 定義の作成・更新・ロールバック（manage-as-code の
  `olivares_deployment` リソース経由も含む）— は control plane のみで完結し、**インフラ
  ストラクチャには一切触れない**。
- **`plan`** は純粋なドライランの差分であり、**`verify`** はドリフトを確認しスナップ
  ショットを更新する。いずれも変更を行わない。
- **`apply` と `retire`** は唯一の変更操作である。これらは**二段階（two-phase）**かつ
  **deny-by-default** である。第一段階は差分を計算し、何も変更せずに plan ハッシュに
  バインドされた人間の承認を*要求*する。第二段階は、承認が `approved` で**かつ** plan
  ハッシュが依然として一致する場合にのみ進行する。それ以外のいかなる状態（pending、
  expired、rejected、ゲートなし、古い plan）も拒否され記録される。再指定はハッシュを変更し
  承認を無効化する（anti-TOCTOU）。

変更を伴う apply/retire は**デフォルトではライブではない**。アクチュエーションシーム
（[`Executor`](/ja/reference/modules/overview/)）は deny-closed である。executor が
プロビジョニングされていない状態では、apply/retire/plan/verify は **`503` で fail closed
する** — control plane は望ましい状態を宣言できるが、現実のインフラストラクチャへ調整する
ことはできない。実エンジン（Tofu/Terraform、GitOps、Kubernetes、Docker、Nomad、
Crossplane）に加え、短命・操作ごと・attested な認証情報ソースが配線されるのは**オペレーター
の構成時のみ**である。それが無ければ、本モジュールが暗黙のうちに作用することは決してない。

## エンティティと宣言される契約

本モジュールは 4 つの名前空間付きエンティティと、適用済みスナップショットとしてのコア
`Deployment` を宣言する。

| エンティティ | 役割 |
|---|---|
| **definition** | 望ましい状態 — desired 対 applied のバージョン、spec ハッシュ、コア `Deployment` へのリンク |
| **revision** | append-only かつ不変な spec 履歴 — ロールバックのための可逆的なソース |
| **wiring** | 宣言する**許可された（PERMITTED）**接続性 `agent → resource`（モジュール III が対比する契約） |
| **operation** | append-only な変更管理 ledger — バージョン、plan ハッシュ、誰が承認したか、結果 |

望ましい spec は**型付けされ、struct から再シリアライズされる**（オペレーターの JSON
ラウンドトリップではない）。未知のフィールドは拒否され、インライン認証情報ガードが実行され、
認証情報をクリアテキストで保持する spec は**宣言時点で拒否される**。認証情報は**参照のみ
で渡される**（`<scheme>:<locator>`、allow-list 化された scheme）— これはワイヤーの特性で
あり、保存された秘密情報ではない。

## バス上に生成するもの（モジュール III の PERMITTED 側）

モジュール VII は access map を書き込むことは決してなく、そのエッジを書き込む唯一の存在は
モジュール III である。コミットされた `apply` において、各 wiring ごとに本モジュールは
policy-grant の [`edge.observed`](/ja/reference/events/) イベント（`Source = policy`）を発行し、
参照とモードのみを運ぶ。モジュール III はそれを permitted-vs-observed 差分の **PERMITTED**
側へと調整する。したがって本モジュールが宣言するものは、モジュール III が観測対象と対比する
ものそのものである。Identity は governance を通じてエージェントごとにバインドされる。確固
たる一意の non-human identity は `attributed` なエッジを生み、共有または不在の identity は
`approximate` として報告される — **印が付けられるのであって、決して偽装されない**。

:::caution[正直な制限]
- **ライブ適用は deny-closed のシームである。** executor がプロビジョニングされていない状態
  では、`apply`/`retire`（および `plan`/`verify`）は明確な `503` を返す。本モジュールは今日、
  望ましい状態を計画・統制・バージョン管理・宣言する。現実のインフラストラクチャへ調整する
  のは、オペレーターが executor を配線したときのみであり、デフォルトでは決して行わず、
  暗黙の no-op でも決してない。
- **承認と attribution も安全側に倒れる。** 承認ゲートが無ければすべての変更は拒否される。
  identity binder が無ければ wiring の attribution は劣化するのであって、捏造されることは
  ない。`Start()` は配線されていないシームごとに一度警告するため、壊れたデプロイは可視化
  される。
- **wiring を廃止しても、発行済みの PERMITTED エッジは撤回されない。** エッジモデルには
  撤回の動詞が無い。wiring は revoked として印が付けられ、モジュール III がその古さ
  （staleness）を調整する。隠されるのではなく、宣言される。
- **バックエンドの深さはまちまちである。** アクチュエーションバックエンド間で、観測パスが
  他より浅いものもある（例：特定のランタイムにおける表層的なヘルス）。これらは正直なギャップ
  として注記され、捏造された同期済み状態として報告されることは決してない。
:::

## 関連

- [モジュールカタログ](/ja/reference/modules/overview/) — Govern/Observe 対 Actuate の区別と `503` シーム。
- [モジュール III — access map](/ja/reference/modules/iii-access-map/) — 本モジュールが宣言する PERMITTED wiring を消費する。
- [イベントバスリファレンス](/ja/reference/events/) — `edge.observed` イベントとその minimal-data ペイロード。
- [統制と承認](/ja/how-to/govern-and-approve/) — あらゆる変更の背後にある HITL 承認フロー。
- [正直さと制限](/ja/start/honesty-and-limits/) — 今日アクチュエートするものと、しないもの。
- [アーキテクチャ概要](/ja/explanation/architecture/overview/) — Management レイヤーにおけるモジュール VII の位置。
