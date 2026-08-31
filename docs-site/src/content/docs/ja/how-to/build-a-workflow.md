---
title: "ガバナンス対象ワークフロー（DAG）を構築する"
description: "既存のガバナンス対象アクションを依存関係グラフに構成し、副作用なしで実行計画をレビューして、レビューしたグラフそのものに結び付いた人間の承認を経て実行します。"
---

**ワークフロー**は、スケジュールの発火、他のモジュールへのシグナル送信、テスト
通知の送信、待機など、プラットフォームがすでにガバナンスしているアクションを
依存関係グラフ（DAG）として連結します。ワークフローの実行は、人間が承認する
単一の特権アクションです。何らかの対象に作用する各ステップは、スケジュールを
1 回発火させた場合と同じ追記専用の意思決定台帳に行を残します。

ワークフローは**既存アクションの構成であり、新たな権限ではありません**。コマンド
を実行する、任意の URL を呼び出す、または payload を運ぶステップ種別は意図的に
存在しません。グラフができるのは、エステートがすでに公開している動詞を、すでに
存在するゲートの下で並べ替えることだけです。ワークフローの実行には管理者層の
権限*と*人間の承認の両方が必要なので、直接アクセスできない対象へ到達する手段には
決してなりません。

## グラフの構造

ワークフローは**ステップ**の集合です。各ステップには、ワークフロー内で一意の短い
`ref`、`kind`、型付きの `config`、依存先の ref を示す `depends_on` があります。
グラフは非巡回でなければなりません。サーバーは何かを保存する前に、非巡回性、
参照先の存在、fan-in/fan-out の上限を検証します。

| 種別 | 動作 | 通過するゲート |
|---|---|---|
| `schedule-fire` | 既存のガバナンス対象スケジュールを dispatch する | kill switch、budget、dispatcher seam |
| `eventing-emit` | 他のモジュールが subscribe できる `workflow.signal` イベントを publish する | — |
| `notify-test` | alert route を通じて synthetic test を送る | notify actuator seam |
| `wait` | 実行を制限時間（1 秒～24 時間）だけ待機させる | — |
| `approval-gate` | グラフの**途中**で人間の承認を開き、決定されるまで待機する | approval gate |

`eventing-emit` が publish するイベント型は**固定**です。ステップの config が提供
するのはラベルだけなので、ワークフロー作成者が `edge.observed` のような
ファーストパーティイベントを偽造し、別モジュールの ingestion に送り込むことは
できません。

## 1. ワークフローを宣言する

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{
    "name": "release-train",
    "steps": [
      {"ref":"announce","kind":"eventing-emit","config":{"label":"starting"},"depends_on":[]},
      {"ref":"hold","kind":"approval-gate","config":{"reason":"release window"},"depends_on":["announce"]},
      {"ref":"deploy","kind":"schedule-fire","config":{"schedule_id":"<id>"},"depends_on":["hold"]}
    ]}'
```

作成には **write-tier** の権限が必要です。グラフが拒否されると、問題のあるステップ
を示す `400` が返ります。

```json
{"error":{"message":"step deploy: schedule <id> is retired","step_ref":"deploy"}}
```

コンソールは、その `step_ref` をキャンバス上のノードにアンカーします。後でグラフを
置き換える操作は、単一のアトミックな `PUT .../steps` です。グラフはステップごと
ではなく、全体としてレビューされ、承認されます。

変更のたびに完全なスナップショットがリビジョン台帳へ追記されます。以前の
リビジョンはいずれも、live の動詞と同じ検証を通じて復元できます。

## 2. 計画をレビューする — 副作用なし

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

dry-run はステップをトポロジカル順に返し、各ステップの予定動作、通過するゲート、
グラフの保存後に参照が古くなった場合（先週廃止されたスケジュールなど）の警告を
示します。何も書き込まず、何も dispatch せず、承認も開きません。そのため、これは
**読み取り**であり、ワークフローを読める主体なら誰でも利用できます。

また、正確なグラフのフィンガープリントである `plan_hash` も返します。この値が
次の手順で重要になります。

## 3. 実行する — 人間が見た内容に結び付く 2 フェーズ

実行には管理者層の権限が必要で、**かつ**ゲートされます。第 1 フェーズでは承認を
開きます。

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
# 202 {"op":"run_request","approval_ref":"…","gate_status":"pending", …}
```

人間がガバナンス判断 API を通じて決定します。次に、第 2 フェーズでその参照を
渡し、決定を消費します。

```bash
curl -sS -X POST "$OLIVARES/v1/m/orchestration/workflows/$ID/run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' -d '{"approval_ref":"…"}'
```

承認は **plan hash に結び付けられます**。2 つのフェーズの間にグラフを編集すると
hash が変わるため、その承認は何も認可しなくなり、実行は拒否されます。人間の
「承認」はレビューしたグラフにだけ適用され、後から差し替えたグラフには決して
適用されません。その後、実行はそのグラフの**スナップショット**を使うので、実行
途中の編集によって、すでに実行中の内容が変わることもありません。

全体を通してデニーバイデフォルトです。approval gate が配線されていなければ実行を
拒否し、暗黙に許可する代わりに、そのガバナンス上の欠落を finding として提起します。

## 4. 実行を監視する

```bash
curl -sS "$OLIVARES/v1/m/orchestration/workflows/$ID/runs/$RUN" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

各ステップはそれぞれの状態を報告します。上流が失敗したステップは `skipped` となり、
実行は失敗を越えて続行することも、得られなかった成功を報告することもありません。
`wait` は再開時刻を示し、`approval-gate` は待機中の承認を示します。emergency stop
が作動すると、実行全体が目に見える `paused_reason` とともに**凍結**し、stop が解除
されると再開します。stop が暗黙に無視されることも、それだけで実行全体が失敗する
ことも決してありません。

ステップはバックグラウンド処理で進むため、リクエストを開いたままにする主体が
いなくても、wait やグラフ途中の承認は進行します。

### 台帳に記録される内容

作動を伴う各ステップは、実行を開始した人間に帰属する不変の行を追記します。次の
2 つの性質は重要です。

- **拒否された**実行も記録されます。拒否も証拠です。
- runner が作動をすでに断念した後で結果が届いた場合、その結果は実際の dispatch
  参照とともに台帳へ**リコンサイル**されます。ステップの表示は「結果不明」のまま
  かもしれませんが、台帳は発生しなかった作動が発生したとは決して主張せず、実際に
  発生した作動を隠すことも決してありません。

## 意図的に対象外としているもの

- **自動トリガー。** ワークフローは、人間が承認したときに実行されます。cron や
  イベントから実行を開始する配線は、無人の作動経路を追加するため、独立した変更
  として既存のスケジュール用レールの背後に置かれます。
- **任意の副作用を持つステップ**（HTTP、exec）。そのようなステップは構成用
  サーフェスを汎用実行エンジンに変え、ワークフローが既存のガバナンス対象動詞だけ
  を並べ替えられるという性質を損ないます。

## 関連項目

- [ガバナンスと承認](/ja/how-to/govern-and-approve/) — 実行とグラフ途中のゲートが
  通過する承認エンジン。
- [イベントリファレンス](/ja/reference/events/) — `workflow.signal` と、subscriber が
  受信に必要とする権限。
