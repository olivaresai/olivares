---
title: "Управление Olivares AI как кодом (Terraform)"
description: >-
  Объявляйте и согласовывайте объекты control plane — агентов, политики, привязки
  идентичностей и развёртывания — с помощью провайдера Terraform/OpenTofu для
  Olivares AI, аутентифицируемого непрозрачным API-токеном к REST API движка.
---

Olivares AI предоставляет **провайдер Terraform**, чтобы вы могли управлять control plane *как
кодом* — агентами, политиками управления (governance), привязками агент↔идентичность и
определениями развёртываний, объявленными в HCL и согласовываемыми с работающим движком через его
REST API. Это модуль XIX (собственный API + управление-как-кодом); провайдер представляет собой
тонкий клиент поверх той же REST-поверхности, что документирована в [справочнике API](/reference/api/),
поэтому всё, что можно сделать в HCL, можно сделать и через REST.

Провайдер и CLI распространяются под лицензией Apache-2.0 и никогда не импортируют внутренности
движка; HCL — это просто ещё один фронтенд к управляемому (governed) API.

## Настройка провайдера

```hcl
terraform {
  required_providers {
    olivares = {
      source = "olivaresai/olivares"
    }
  }
}

provider "olivares" {
  endpoint = "https://olivares.internal:8443" # or OLIVARES_ENDPOINT
  api_token = var.olivares_token                  # or OLIVARES_API_TOKEN (sensitive)
  # tenant   = "…"                                # optional; or OLIVARES_TENANT (sent as X-Olivares-Tenant)
  # insecure_skip_verify = true                   # dev self-signed cert only
}
```

| Параметр | Обязателен | Запасной env | Примечания |
|---|---|---|---|
| `endpoint` | да | `OLIVARES_ENDPOINT` | Базовый URL API control plane |
| `api_token` | да | `OLIVARES_API_TOKEN` | **Непрозрачный bearer-токен** (продукт использует непрозрачные, отзываемые токены, а не JWT) |
| `tenant` | нет | `OLIVARES_TENANT` | UUID тенанта; опускается, когда токен привязан к тенанту |
| `insecure_skip_verify` | нет | — | Пропуск проверки TLS для самоподписанного сертификата dev-среды; никогда в продакшене |

Аутентификация — это bearer-токен, отправляемый с каждым запросом, а тенант передаётся в заголовке
`X-Olivares-Tenant` — тот же RBAC с запретом по умолчанию (deny-by-default), разграничение по тенантам
и аудит каждого действия, что и у остального API. Создайте токен для сервисной идентичности с
минимальными привилегиями (least-privilege) и держите его вне состояния (используйте переменную и
секретный бэкенд).

## Ресурсы

| Ресурс | Чем управляет | Ключевые атрибуты |
|---|---|---|
| `olivares_agent` | Сущность агента в инвентаре | `name` (обязателен), `kind` (обязателен), `external_id` (опционален); вычисляемые `id`, `status`, `version` |
| `olivares_policy` | Политика управления (governance) | `name` (обязателен), `kind` (`abac` или `approval`, обязателен, неизменяем), `enabled`, `spec` (обязателен, JSON); вычисляемый `spec_canonical` |
| `olivares_agent_identity_binding` | Привязка агента к нечеловеческой идентичности (мост, уточняющий атрибуцию R/RW) | `agent_id`, `identity_id`/`identity_ref`, `mint`, `allow_unknown`; вычисляемые `minted`, `shared`, `agent_count` |
| `olivares_deployment` | **Определение** развёртывания (декларативное желаемое состояние) | `subject_kind`, `subject_ref`, `name`, `environment`, `runtime`, `target`, `source_ref`, `spec`, `desired_status`; вычисляемые `current_version`, `applied_version`, `spec_hash` |

## Источники данных

Представления только для чтения, чтобы модуль мог ссылаться на управляемое состояние, не переписывая
REST-вызовы заново: `olivares_policies`, `olivares_identities`, `olivares_deployment`,
`olivares_server_info` и `olivares_access_edges` — последний раскрывает рёбра R/RW и, при
`include_drift = true`, дрейф «разрешённое-против-наблюдаемого» (permitted-vs-observed) (включая
честный флаг `reconciliation_pending` для доступа, который ещё нельзя надёжно атрибутировать).

## Минимальный пример

```hcl
resource "olivares_agent" "billing_bot" {
  name = "billing-reconciler"
  kind = "service"
}

resource "olivares_policy" "require_approval_for_prod" {
  name    = "prod-deploys-need-approval"
  kind    = "approval"
  enabled = true
  spec    = jsonencode({
    # policy body — see the API reference for the schema of each kind
  })
}

# Read the current Permitted-vs-Observed drift as data:
data "olivares_access_edges" "estate" {
  include_drift = true
}
```

`terraform plan` согласует ваш HCL с движком; `terraform apply` создаёт или обновляет объекты через
управляемый API. Поскольку политики и привязки меняют поверхность авторизации, относитесь к плану как
к изменению, подлежащему ревью — движок фиксирует в аудите каждую мутацию с реальным актором.

:::caution[`olivares_deployment` объявляет желаемое состояние; реальное применение ограничено воротами]
`olivares_deployment` управляет **определением** развёртывания — декларативным, версионируемым
желаемым состоянием. Оно сопоставляется с модулем VII (deploy), реальное приведение в действие
которого — это **закрытый по умолчанию шов (deny-closed seam)**: пока не выделен исполнитель, движок
*планирует и управляет* развёртыванием, но **`apply`/`retire` возвращают `503`**, а не воздействуют
на инфраструктуру. Так что ресурс `olivares_deployment` сегодня записывает и регулирует намерение; сам
по себе он не согласовывает реальную инфраструктуру. См. [модуль VII](/ru/reference/modules/vii-deploy/) и
[Честность и ограничения](/ru/start/honesty-and-limits/).
:::

:::note[Провайдер — это подмножество API, и так задумано]
Провайдер покрывает перечисленные выше объекты управления-как-кодом. Полная управляемая поверхность —
и схема каждого `spec` на уровне полей — это REST API; некоторые маршруты модулей достижимы, но
намеренно находятся вне обслуживаемого документа OpenAPI. Проверяйте атрибуты ресурса по
`terraform providers schema -json` и [справочнику API](/reference/api/), прежде чем полагаться на них;
эта страница не воспроизводит схему, которую не может держать в полной синхронности с кодом.
:::

## Связанное

- [Справочник API](/reference/api/) — REST-поверхность, которой управляет провайдер.
- [Политика стабильности API](/ru/reference/api-stability/) — обязательство по версионированию/устареванию, на которое опирается провайдер (он предупреждает один раз за запуск, когда ответ несёт сигнал устаревания).
- [Модуль XIX — собственный API + управление-как-кодом](/ru/reference/modules/xix-api-manage-as-code/).
- [Модуль VII — развёртывание и интеграция](/ru/reference/modules/vii-deploy/) — оговорка о шве 503 выше.
- [Управление и согласование](/ru/how-to/govern-and-approve/) — как политика и согласования регулируют то, что вы объявляете.
