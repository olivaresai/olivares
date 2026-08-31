---
title: "Мониторинг с Prometheus (SLO, метрики, алерты)"
description: >-
  Собирайте /metrics движка, примите опубликованные цели SLO и загрузите
  поставляемые правила алертов по скорости расходования бюджета — те же SLI,
  на которые опираются собственные runbook'и продукта, с честно указанными
  цифрами для конфигурации с единственным писателем.
---

Движок предоставляет три операционных эндпоинта на HTTP-слушателе, все
удобные для проб (probe-friendly):

| Эндпоинт | Авторизация | Назначение |
|---|---|---|
| `/livez` | нет | живость процесса — **без проверки зависимостей**, поэтому сбой хранилища никогда не вызывает цикл перезапусков |
| `/readyz` | нет | готовность — пинг хранилища (и лидерство HA): `200 {"status":"ok","store":"up","leader":true,…}`, `503 {"status":"unavailable","store":"down"}` или `503 {"status":"standby",…,"leader":false}` на резервном узле HA |
| `/metrics` | нет | экспозиция Prometheus. Намеренно без аутентификации: несёт операционные ряды, никогда — данные тенантов |

Достижимость `/readyz` **и есть** SLI доступности.

## Набор метрик, который имеет значение

Все ряды регистрируются движком (проверено по текущему коду);
несущие основную нагрузку:

| Ряд | О чём он сообщает |
|---|---|
| `olivares_store_up` | хранилище отвечает на пинг — первое, что проверяет каждый runbook |
| `olivares_http_requests_total{code}` | SLI успешности запросов (`code!~"5.."`) |
| `olivares_http_request_duration_seconds` | задержка API (цель p99 ниже) |
| `olivares_ingest_duration_seconds` | **SLI противодавления (backpressure)** — p99 приёма растёт, когда подписчик насыщается |
| `olivares_ingest_observations_total` / `olivares_ingest_rejected_total` | пропускная способность приёма и отклонения |
| `olivares_eventbus_queue_depth` / `_queue_capacity` (на подписчика) | какой модуль является медленным потребителем |
| `olivares_eventbus_publish_blocked_total` | события противодавления (шина блокирует; она не отбрасывает) |
| `olivares_eventbus_bridge_*` | здоровье моста NATS, когда включена распределённая шина — `_connected`, `_pending_messages`, `_dropped_total` (межузловая доставка — at-most-once; отбрасывания подсчитываются, никогда не молча) |
| `olivares_audit_checkpoint_age_seconds` | свежесть свидетельства о неизменности (tamper-evidence) — алерт, когда превышает 2× интервала контрольной точки |
| `olivares_auth_login_attempts_total{outcome}` | успех / отказ / блокировка входа |
| `olivares_http_ratelimit_decisions_total{decision}` | давление ограничения скорости |
| `olivares_grpc_requests_total` / `olivares_grpc_request_duration_seconds` | плоскость приёма коллектор→ядро |

## Цели SLO (опубликованные, честные)

Цели для одиночного узла — то, что реально поддерживает топология по умолчанию — и
уровень HA:

| SLI | Одиночный узел | Уровень HA (Postgres) |
|---|---|---|
| Доступность (`/readyz`) | **99.5% / 28 дней** | 99.9% / 28 дней |
| Успешность запросов (не-5xx) | **99.9%** | 99.95% |
| Задержка API p99 | **< 300 мс** | < 200 мс |
| Задержка приёма p99 | **< 250 мс** | < 150 мс |
| Успешность приёма | **99.9%** | 99.95% |

Честность в цифрах: единственный писатель на одном узле не может обещать три
девятки доступности, поэтому документация и не обещает — 99.5% (≈ 3 ч 39 м
бюджета на 28 дней) — это правда одиночного узла, а уровень 99.9% достигается
[топологией HA](/ru/tutorials/getting-started/kubernetes/#3-active-passive-ha),
а не оптимизмом.

## Загрузка поставляемых правил алертов

`deploy/monitoring/olivares-slo.rules.yaml` поставляет 14 алертов, готовых для вашего
Prometheus: многооконные алерты по скорости расходования бюджета успешности запросов
(быстрый 14.4× page / средний 6× page / медленный 1× ticket), абсолютные срабатывания
по задержке и доступности (`OlivaresIngestP99High`, `OlivaresApiLatencyP99High`,
`OlivaresStoreDown`, `OlivaresControlPlaneUnscrapeable`), насыщение
(`OlivaresEventBusSaturated` при >90% очереди в течение 10 м), здоровье моста
(`OlivaresEventBusBridgeDropping`, `OlivaresEventBusBridgeDisconnected`) и
свежесть журнала (`OlivaresAuditCheckpointStale` при возрасте > 2 ч).

```yaml
# prometheus.yml
rule_files:
  - olivares-slo.rules.yaml
scrape_configs:
  - job_name: olivares
    scheme: https
    tls_config: { insecure_skip_verify: true }   # or pin the real cert
    static_configs: [{ targets: ["olivares.internal:8443"] }]
```

В Kubernetes опция `ServiceMonitor` чарта настраивает сбор метрик для
оператора Prometheus. Конфигурация страницы статуса Gatus для внешней пробы `/readyz`
поставляется вместе с правилами (`deploy/monitoring/status-page.gatus.yaml`).

## Когда срабатывает алерт

Диагностика симптом за симптомом — хранилище недоступно, высокий p99 приёма, насыщена шина,
устаревшая контрольная точка — находится на [странице устранения неполадок](/ru/how-to/troubleshooting/),
извлечённой из тех же runbook'ов, на которые ссылаются аннотации алертов.
