---
title: "コントロールタワーへの posture エクスポート"
description: >-
  エンジンのグランドトゥルースな posture の、読み取り専用・アウトバウンドの
  射影 —— 発見されたインベントリ、最小権限ドリフト、セキュリティ findings ——
  を、コントロールタワーが自身のビューを充実させるためにプルする。検証済みの
  ネイティブ・プッシュではなく、ニュートラル JSON の射影である。
---

Posture export（`modules/posture-export`）は、エンジンの **アウトバウンドな posture
サーフェス** である。コントロールタワーがポーリングして、エンジンのグランドトゥルースな
[access graph](/ja/reference/modules/iii-access-map/)、最小権限ドリフト、発見されたインベン
トリ、セキュリティ posture によって自身のインベントリを充実させるための、単一の読み取り
専用エンドポイントである。これはプラットフォームの「競争するのではなく統合する」側面で
ある —— アイデンティティを発行することは決してなく（それはインバウンドであり、
[governance](/ja/reference/modules/vi-governance/) が所有する）、posture のみを発行し、何も
変更しない。

## 何を公開するか

ルートはひとつ、`GET /v1/m/posture/export` であり、`posture:export:read` でゲートされ、
単一のテナントスコープにピン留めされる。レスポンスは、**1 つの監査対象トランザクション**
の内部で組み立てられる、3 つの射影からなるニュートラル JSON ドキュメントである。

- **`inventory`** —— アクティブな発見済みエンティティ（kind、ref、status、シグナルソース、
  ホスト、初回／最終確認、出現回数）。オプションで `?kind=` によるフィルタが可能。
- **`posture_drift`** —— 調整済みの最小権限ドリフト。すなわち observed-but-not-permitted な
  アクセス、加えて unused-grant と inventory-grant のカウント。
- **`findings`** —— セキュリティ findings を ref と `detail_hash` のみとして射影したもの。
  `?severity=` の下限と `?category=` でフィルタ可能。

すべてのエクスポートは **最小データ** である —— ref、ハッシュ、関係のみであり、生の
ペイロードやシークレットを含むことは決してない —— そして防御的な秘匿化パスがすべての
自由形式フィールドをスクラブする。エクスポートそのものはデータをボックス外に移動するため、
読み取りと同じトランザクション内で、実際の principal とともに台帳へ **自己監査** する。

## 成熟度と境界づけられたコンテキスト

**PARTIAL。** エクスポート・アクションはライブで監査される。*検証されていない* のは
相手側である。名指しされたタワー —— **Microsoft Agent 365** および
**ServiceNow AI Control Tower** —— の取り込みフォーマットには、エンジンが検証対象にできる
一次情報源の API が存在しない。したがってこれは、**タワーがプル（あるいはオペレータが
設定済みのシンク経由でルーティング）する、率直なニュートラル JSON 射影であり、明示的に
動作するネイティブ・プッシュではない**。各レスポンスはその来歴に関する注記をインラインで
含む。

リクエストごとの上限が inventory、drift、findings を制限する。部分的なエクスポートは自身の
切り捨てフラグを報告し、決して権威あるものとしてラベル付けされない。

## 関連

- [監査を Splunk に転送する](/ja/how-to/forward-audit-to-splunk/) —— `siemforward` プレーン。
  封印された台帳と findings を SIEM タワーに送出する *プッシュ* 側の対応物。
- [モジュール XIII —— コンプライアンスと規制](/ja/reference/modules/xiii-compliance/) ——
  この posture がグランドトゥルースを共有する、封印された証跡。
- [モジュール III —— アクセスとリソースマップ](/ja/reference/modules/iii-access-map/) ——
  エクスポートが射影する、調整済みのドリフト。
- [誠実さと限界](/ja/start/honesty-and-limits/) —— なぜこれが検証済みプッシュではなく射影
  なのか。
- [モジュールカタログ](/ja/reference/modules/overview/) —— posture export が 30 個の出荷済み
  モジュールのどこに位置するか。
