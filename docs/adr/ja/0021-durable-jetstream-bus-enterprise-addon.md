> 機械翻訳です。正式な情報源は英語版です。

# ADR-0021: 閉じた enterprise アドオンとしての永続 JetStream イベントバス・バックエンド（at-least-once + バス境界での重複排除）

- **Status:** accepted (extends ADR-0017's "JetStream remains the upgrade path")
- **Date:** 2026-06-24
- **Deciders:** Fran Olivares (scale/reliability lever); design re-anchored against HEAD + a subscriber-idempotency re-census
- **References:** ADR-0017 (the at-most-once Core-NATS bridge), ADR-0020 (enterprise private-repo distribution),
  `LICENSING.md`, `enterprise/durablebus`, `core/eventbus/natsbus`

## 背景と課題

ADR-0017 は、分散バスを in-proc のローカル fan-out + **Core-NATS、at-most-once** bridge として
提供し、2026-06-12 の subscriber 調査で大半の subscriber が重複安全ではなく、at-least-once では
重複を誤って処理する handler に届けてしまうことが判明したため、**v1 で JetStream を採用することを
明示的に却下した**（選択肢 C）。JetStream は「at-least-once へのアップグレード経路。ただし
**subscriber の冪等性確認を条件とする**」として残された。

ガバナンス control plane は、DECISION を引き起こすイベントを黙って失ってはならない。オープンな
bridge では、HA node 間で finding.reported / cost.sampled が失われると（server restart、
reconnect-buffer overflow、slow-consumer drop）、強制シグナルが通知なく見逃される。enterprise の
scale/reliability tier（lever #4）は、ADR-0017 が想定した subscriber ごとの冪等性確認なしで、
enforcement-event class に対してこれを解消する必要がある（再調査でも subscriber は依然として
「**十分に**冪等」にすぎないことが確認された。たとえば `modules/security` は finding を
**bounded best-effort scan** で重複排除しており、厳密な保証ではない — `observed.go`, `anomaly.go`）。

## 意思決定の要因

- **handler を信頼せず、非冪等性を BUS で解決する。** ADR-0017 は、すべての subscriber を冪等にする
  ことを JetStream の条件とした。これは脆弱な分散不変条件（約 17 handler にまたがり、将来の編集で
  再び破られる）であり、完了しなかった。バス境界で単一の所有された重複排除を行うことが永続的な解決で、
  各 subscriber が永遠に正しくなくても永続性を得られる。
- **ラグプルも hot-path regression もない。** ADR-0017 の重要な制約を維持する。ローカル in-proc
  hot path とオープンな Core-NATS bridge は、コミュニティバイナリで byte-for-byte 変更しない。
  アップグレードは ADDITIVE でなければならない。
- **収益化の時機（ADR-0020）。** durability/HA は enterprise tier の lever である。非公開リポジトリ
  分割によってタグが実効的な境界になった後、`enterprise` build tag の背後に閉じたコードとして提供する。

## 検討した選択肢

- **A. すべての type について bridge を JetStream に置き換える。** 却下。損失許容の高ボリューム
  observation（edge/metric）を RAFT storage に通し、オープン bridge の挙動を変える（ラグプル）。
- **B. ENFORCEMENT class のみ永続 JetStream とし、残りにはオープン bridge を埋め込む（採用）。**
- **C. store 内に subscriber ごとの永続重複排除 table を設ける。** Fase 1 では却下。enterprise 専用
  table は open≡enterprise schema-parity gate を壊し、オープン table は保証に必要なものより重い変更で
  ある。代わりに重複排除 state を JetStream KV に置く（store なし、schema 変更なし）。

## 決定の結果

採用: **B.** オープンな `*natsbus.Bus` を**埋め込み**、**enforcement set**
（`finding.reported`, `cost.sampled`, `guardrail.observed`, `approval.requested`,
`policy.changed` — operator が上書き可能）に JetStream path を追加する閉じたアドオン
`enterprise/durablebus`（`//go:build enterprise`, `LicenseRef-Olivares-Commercial`）。仕組み:

- **兄弟の subject namespace。** 永続イベントは `<durable_prefix>.<type>`（JetStream stream、
  RAFT、replicas ≥ 3）へ publish する。Core bridge の `<subject_prefix>.>` とは DISJOINT であり、
  各 type は両方ではなく、厳密に一方の transport だけで配信される。埋め込まれた bridge には durable
  set を Core bridging から EXCLUDE するよう指定する（`natsbus.Options.BridgeExclude`、オープン
  バイナリでは inert）。non-enforcement type はオープン bridge の at-most-once reach を維持する
  （regression なし）。
- **publish は PubAck を確認する**（`Nats-Msg-Id = event.ID`）。永続イベントは、永続的に保存されるか
  failure が表面化するかのいずれかで、黙って drop されない。stream の duplicate window は retry / failover
  による二重 publish を保存済みの 1 copy にまとめる。
- **leader-gated durable consumer**（ack-explicit）。`Active()` watcher を介して promotion 時に bind、
  demotion 時に停止する（elector は OnDemote を公開しない）。server-side position は failover 後も残る。
  enforcement は cluster-wide で一度実行される。
- **inject boundary で event.ID により重複排除**する 2 tier。in-memory time window（高速、same-node）と
  **JetStream KV** bucket（RAFT-replicated、TTL-bounded、crash/restart 後も残り node 間で重複排除）。
  READ-before-inject（重複を抑止）+ RECORD-after-inject（crash 時に失わず再 inject する）。

**正直なセマンティクス: at-least-once、決して exactly-once ではない。** 通常および中程度に劣化した
運用では LOSS は起きない（record-after-inject、確認済み publish は永続、consumer は ack 済み位置から再開）。
残る唯一の loss path は retention-bounded である。stream は message を最大 `MaxAge`（既定 72h、
`LimitsPolicy`）保持するため、`MaxAge` より長く leader が一切 drain しないと stored event が drop される。
これは total-quorum-loss / multi-day leaderless または partitioned outage である。この window は
`olivares_durablebus_stream_pending` SLI により観測可能になる（`MaxAge` に近づく backlog は alertable）。
したがって silent drop ではなく、operator は `MaxAge` を増やすか leader を復旧して zero に保つ。
DUPLICATE が起こり得るのは、≤2s の leadership overlap と inject 後 dedup record 前の hard crash という
2 つの bounded window だけであり、どちらも downstream に吸収される（eventing capture の
`(tenant_id, event_id)` index と security の bounded-scan dedup）。オープン bridge は at-most-once のまま
変更されない。

### 帰結

- **良い点:** enforcement event は node 間配信を at-least-once で生き残り、単一の所有された dedup
  guarantee を持つ。コミュニティバイナリは byte-identical（アドオンは存在せず、唯一の open seam
  `BridgeExclude` は inert）。store schema の変更なし（dedup は JetStream KV）⇒ schema parity は
  影響なし。fail-boot-closed（宣言された durable backend を確立できなければ boot を中止する。
  unlicensed enterprise binary は VISIBLY にオープンな Core-NATS bridge へ degrade し、決して黙って
  single-node にはならない）。
- **悪い点 / トレードオフ:** durable delivery は publish 時に JetStream round-trip（PubAck）、inject 時に
  KV read が必要。中程度の volume の enforcement class には許容可能で、operator は durable set を狭められる。
  durable event が subscriber に到達するのは leader 上だけ（consumer 経由）なので、node 自身の durable
  publish はローカル fan-out されない（「enforcement は leader 上のみ」と整合）。bus license gate は boot-time
  であり、durability を有効化する license の導入には restart が必要（hot-applied な add-on entitlement とは異なる）。
- **中立:** lever の Fase 2+（DR ladder、multi-region、per-tenant silo/CMEK）は文書化された roadmap
  （`enterprise/durablebus/doc.go`）であり、構築されていない。

## 代替案を却下した理由

A はオープン bridge をラグプルし hot path に負担を課す。C は小さな KV の代わりに core schema を変更し、
parity gate を壊す。B は変更を閉じた追加コードに限定し、未完了の subscriber ごとの pass ではなくバス境界で
ADR-0017 の duplicate-safety concern を解決する。
