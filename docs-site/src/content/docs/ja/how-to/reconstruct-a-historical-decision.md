---
title: "過去の認可決定を再構成する"
description: >-
  再構成が証明すること、証明できないこと、および火曜の取消の後に月曜の
  allow を監査人が再生する方法。
sidebar:
  order: 22
---

監査人は、ある過去の日付にポリシーが**何を決めたか**を尋ねられます。
制御プレーンは**証拠台帳**から答えます。記録されたポリシー版と、その決定が
使った入力です。**今日有効な**ポリシーは評価しません。

## コマンド

```bash
olivares policy replay \
  --at 2026-09-15T12:00:00Z \
  --principal agent-7 \
  --resource public.customers \
  --resource-kind postgres.table \
  --action SELECT
```

保存行を指定する場合:

```bash
olivares policy replay --decision-id <decision-id> -o json
```

HTTP は `POST /v1/m/governance/decisions/replay` と
`GET /v1/m/governance/decisions/{id}/reconstruct` です。この版に
コンソールの再構成ボタンはありません。CLI または HTTP を使います。

## 再構成が証明すること

状態 `reconstructed` は次を意味します。その質問の
`live_authorization` が記録されている。指名された成果物が**保持**
されている。このバイナリがその評価器（Cedar）を実行できる。再評価は
**元の**成果物を使う。ライブ PDP は読んでいない。

`policy_version_id` は保持成果物の id です。経路証人の
`PolicyVersion` でも、作成リビジョン番号でもありません。

## 証明できないこと

- 当時の執行点の誠実さ。保存された allow は効果の再実行許可ではありません。
- 生産者の真正性や台帳の完全性。
- Olivares が決めていない外部活動。記録が無ければ
  `COULD NOT RECONSTRUCT` です。
- このバイナリが実行できないエンジン（OPA/Rego は作成のみ）。

## COULD NOT RECONSTRUCT

```
COULD NOT RECONSTRUCT
missing: authorization_decision
```

`missing` は欠けた事実の名前です。旧行は **unknown** のままです。
ストアは現行ポリシーから埋めません。

## 月曜・火曜・水曜

月曜に allow を記録し、火曜に取消し、水曜に `--at <月曜>` は
月曜の成果物からなお **allow** と答えます。
