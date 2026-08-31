---
title: "Рецепт: бюджеты и ограждения FinOps"
description: >-
  Поставьте жёсткий долларовый лимит на расходы на ИИ — на модель, команду,
  рабочее пространство или одну личность: оповещение на порогах, затем
  троттлинг или блокировка на лимите. Плюс стоимость-на-результат, чтобы у
  расходов был знаменатель.
sidebar:
  order: 2
---

**Цель:** "агенты этой команды перестают тратить при $500/месяц" — объявлено
один раз, применяется вживую, с порогами оповещений по пути вверх.

Применение бюджета — одна из актуаций, которая **жива в бинарнике по умолчанию**:
применяющий бюджет на своём лимите отказывает в расходе без дополнительного
провижининга ([каталог модулей](/ru/reference/modules/overview/) помечает её как
`v1 | v1`).

## Создать бюджет

```bash
curl -ks -X POST "$BASE/v1/m/finops/budgets" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "dimension": "team",
    "key": "payments",
    "limit_micro_usd": 500000000,
    "period": "monthly",
    "thresholds": [0.5, 0.8, 1.0],
    "action": "block"
  }'
```

- **Деньги — в микро-USD** (`limit_micro_usd: 500000000` = $500), так что в
  контракте нет неоднозначности с плавающей точкой.
- **`dimension` + `key`** определяют область бюджета. Областные измерения
  включают `global`, `model`, `provider`, `agent`, `session`, `team`, `project`,
  `workspace`, `api_key`, `actor`, `service_tier`, `context_window`,
  `inference_geo`, `gateway` и `identity`.
- **`action`** — это режим применения:

| `action` | На лимите |
|---|---|
| `alert` (по умолчанию) | только показ — оповещения срабатывают, ничего не отклоняется |
| `throttle` | шов актуации замедляет новые расходы |
| `block` | шов актуации отклоняет новые расходы |

## Бюджет на одну личность

`dimension: "identity"` задаёт область по **внешнему идентификатору твёрдой
личности реестра** — личности рабочей нагрузки или агента, зарегистрированной
вашими [источниками личности](/ru/how-to/connectors/sso-scim-identity/):

```json
{ "dimension": "identity", "key": "spiffe://corp/agent/billing-reconciler",
  "limit_micro_usd": 50000000, "period": "monthly", "action": "throttle" }
```

Личность разрешается при приёме затрат из привязки агента сэмпла, API-ключа или
актора — так что бюджет следует за личностью через поверхности, а не за одним
API-ключом.

## Посмотрите, как это работает

```bash
# Live consumption vs limit, with run-rate projection:
curl -ks "$BASE/v1/m/finops/budgets/$BUDGET_ID/status" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"

# Threshold crossings (your 50% / 80% / 100% alerts):
curl -ks "$BASE/v1/m/finops/alerts" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

На лимите проверка применяющего бюджета возвращает `allowed: false` с действием
(`throttle` или `block`) и сработавшим бюджетом — отказ называет свою причину.
Оповещения также идут по потоку уведомлений, так что
[назначение](/ru/how-to/forward-audit-to-splunk/) Slack или PagerDuty услышит
пересечение 80% до отказа на 100%.

В консоли **Cost & FinOps** показывает расходы по измерениям с состоянием бюджета
встроенно:

<img class="light:sl-hidden" src="/console/finops-dark.png" alt="Представление Cost & FinOps с трендами расходов и состоянием бюджета." />
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="Представление Cost & FinOps с трендами расходов и состоянием бюджета." />

## Дайте расходам знаменатель: результаты

Стоимость-на-результат — это то, что делает бюджет деловым разговором.
Сообщайте о результатах (решённая заявка, влитый PR, закрытое дело) и читайте
панели ценности:

```bash
curl -ks -X POST "$BASE/v1/m/finops/outcomes" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"kind":"ticket.resolved","subject_ref":"agent:support-triage","count":1}'

curl -ks "$BASE/v1/m/finops/value" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

Сводка ценности включает **риск отмены** — сжигание без результатов — что
является честной обратной стороной метрики успеха.

## Примечания

- **Fail-open, намеренно:** если сама проверка бюджета даёт ошибку (сбой чтения
  FinOps), вывод (inference) разрешается, а не блокируется молчаливо — сломанный
  счётчик не должен превращаться в простой. Сбой логируется и виден.
- Зарезервированная ёмкость (`reserved_micro_usd`) засчитывается в лимит, так что
  бюджет нельзя обойти предварительным бронированием.
- `cost_type` намеренно **не** является измерением бюджета — строки с оценочным
  откатом (estimated-fallback) идут по тому измерению, которому принадлежат, а не
  образуют параллельный пул.
