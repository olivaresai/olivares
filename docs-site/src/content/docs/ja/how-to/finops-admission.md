---
title: "FinOps 入場の拒否と突合を読む"
description: >-
  予算拒否が回線上でどう見えるか、到達不能時の姿勢の設定、
  予約と確定のずれの読み方。
sidebar:
  order: 21
---

課金対象の効果は、実行前に見積もり支出を **予約** します。制御プレーンは
その後、実測コストを **確定** するか、保留を **解放** します。

## 拒否の見え方

硬い上限 (`action=block`) は **HTTP 402** です。柔らかい上限
(`action=throttle`) は **HTTP 429** です。本文はアクション、予算、不足した余裕を示します。このエンドポイントは予算の書き込み権限を要求し、答える相手は管理者です。推論プロキシはこの理由をそのまま返さず、上限は `budget limit reached`、座席単位の上限は `spend limit reached` で拒否し、予算名も金額も含みません。

```bash
olivares finops admission reserve --data @reserve.json -o json
```

## ストア到達不能（既定は拒否）

予算ストアを読めないとき、入場は **拒否** します。理由は
`budget store unreachable (deny-closed)` です。HTTP **503**。監査行
`finops.admission.denied` も残します。既定は **deny** です。

## 突合の読み方

```bash
olivares finops admission reconciliation -o json
```

`drift` が真なら、期限切れで未確定の予約があります。`reconciliation` は
読み取りだけで何も変更しません。`reconcile` が処理であり、それらの予約を
掃除し、予算の書き込み権限を必要とし、`finops_reservation_drift` の所見を
出します。姿勢として扱い、支出の静かな書き換えとしては扱いません。
