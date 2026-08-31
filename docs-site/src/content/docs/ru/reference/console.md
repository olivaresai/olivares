---
title: Справочник консоли — все экраны и требуемые разрешения
description: >-
  Все маршруты консоли Olivares AI, сгруппированные по пяти разделам, с требуемым
  разрешением RBAC и справочной страницей, которую открывает встроенная ссылка помощи.
  Сгенерировано из собственной переписи маршрутов консоли.
---

Эта страница — карта консоли. Здесь перечислен **каждый маршрут, монтируемый приложением**,
а не выборка или только те маршруты, которые кто-то вспомнил описать, с разрешением,
необходимым субъекту для входа, и ссылкой на дополнительные сведения.

Страница **сгенерирована**. Перечень берётся из `web/src/features/route-census.json` —
только дополняемой переписи, которую `registry.route-conservation.test.ts` сверяет с
собранным роутером. Экран нельзя добавить, переместить или потерять без изменения этой
страницы. Название и однострочное описание каждого экрана — **собственные строки консоли**
из того же каталога переводов, который отображает боковая панель; здесь вы читаете то же,
что видите в продукте.

:::note[Разрешения применяет движок, а не эта таблица]
Столбец `Требуется` показывает разрешение, которое проверяет консоль перед предложением
маршрута, и отражает RBAC движка. Авторитетом остаётся движок: прямая ссылка на экран, для
которого у вас нет разрешения, отклоняется API, а не просто скрывается в боковой панели.
См. [Роли и разрешения](/ru/reference/modules/vi-governance/).
:::

## Как читать эту страницу

- **Экран** — название в боковой панели и палитре команд.
- **Путь** — URL относительно origin консоли вашего развёртывания. Это опубликованный
  контракт: закладка, прямая ссылка из runbook и перекрёстная ссылка документации используют
  именно эту строку.
- **Требуется** — разрешение RBAC. `любой вошедший пользователь` означает, что маршрут открыт
  каждому аутентифицированному субъекту; **вход не требуется** означает, что он обслуживается
  ещё до появления сессии.
- **Справка** — страница, которую открывает собственная ссылка помощи консоли для этого экрана.

Следующие пять заголовков — разделы консоли в порядке их отображения в боковой панели.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

Консоль публикует **59 маршрутов**. Каждый из них приведён в таблицах ниже вместе с требуемым
разрешением и справочной страницей, которую открывает встроенная ссылка помощи.

### Эксплуатация

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Обзор | `/` | Обзор инфраструктуры и её здоровья | любой вошедший пользователь | [главная документации](/ru/) |
| Claude Code | `/agentops` | Создание, подключение и управление сессиями Claude Code без SSH | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/ru/how-to/run-claude-code-with-olivares/) |
| Резервные копии | `/backups` | Запуск, планирование, загрузка и восстановление резервных копий со вторым подтверждением разрушающего действия. | `system:admin` | [how-to/backup-and-restore](/ru/how-to/backup-and-restore/) |
| Здоровье и SLA | `/health` | Время работы и SLA агентов и MCP | `health:status:read` | [reference/modules/xxii-health](/ru/reference/modules/xxii-health/) |
| Аварийный выключатель | `/killswitch` | Экстренная остановка, восстановление с двойным контролем и локализация guardian | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/ru/how-to/cookbook/kill-switch-drill/) |
| Журналы | `/logs` | Поток журнала движка в реальном времени с фильтрацией по уровню и модулю, поиском и паузой. | `system:admin` | [how-to/troubleshooting](/ru/how-to/troubleshooting/) |
| Наблюдаемость | `/observability` | Здоровье приёма по стандартам и детализация трассировок | `health:status:read` | [reference/modules/observability](/ru/reference/modules/observability/) |
| Песочница | `/sandbox` | Изолированное тестирование и воспроизведение агентов | `sandbox:run:read` | [reference/modules/xvii-sandbox](/ru/reference/modules/xvii-sandbox/) |
| Сессии | `/sessions` | Текущая работа агентов и временные шкалы | `sessions:live:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Арендаторы | `/tenants` | Приостановка и восстановление обслуживания арендатора | `system:admin` | [how-to/troubleshooting](/ru/how-to/troubleshooting/) |
| Голос | `/voice` | Голосовые сессии и сессии реального времени | `voice:session:read` | [reference/modules/xvi-voice](/ru/reference/modules/xvi-voice/) |
| Работа | `/work` | Долговечный межсессионный backlog: элементы, зависимости, приёмка и решения | `sessions:work:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Рабочее пространство | `/workspace` | Агенты, сессии, ресурсы и активность в пределах одного рабочего пространства | `tenant:read` | [reference/modules/xx-multi-tenancy](/ru/reference/modules/xx-multi-tenancy/) |
| Шаблоны рабочих пространств | `/workspace-templates` | Повторно используемые снимки конфигурации сессии: hooks, настройки, коннекторы и политики. | `sessions:template:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |

### Автоматизация

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Оповещения | `/alerting` | Маршрутизация находок к назначениям и проверка доставок | `notify:route:read` | [reference/modules/xv-notify](/ru/reference/modules/xv-notify/) |
| Автоматизации | `/automations` | Все три контура автоматизации и каталог их триггеров | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/ru/reference/modules/iv-orchestration/) |
| Webhooks и события | `/eventing` | Исходящие webhook-подписки, журнал их доставки и очередь недоставленных сообщений. | `eventing:subscription:read` | [reference/modules/eventing](/ru/reference/modules/eventing/) |
| Оркестрация | `/orchestration` | Координация между агентами и расписания | `orchestration:graph:read` | [reference/modules/iv-orchestration](/ru/reference/modules/iv-orchestration/) |

### Подключение

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Песочница API | `/api-playground` | Интерактивное изучение и тестирование API control plane | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/ru/reference/modules/xix-api-manage-as-code/) |
| MCP и навыки | `/capabilities` | Управление серверами MCP, навыками и инструментами | `capabilities:catalog:read` | [reference/modules/v-capabilities](/ru/reference/modules/v-capabilities/) |
| Каталог | `/catalog` | Курируемые и одобренные агенты и возможности | `catalog:entry:read` | [reference/modules/xiv-catalog](/ru/reference/modules/xiv-catalog/) |
| Привязки протоколов | `/communications/protocol-bindings` | Компоновка и сверка управляемых привязок A2A и MCP | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Развёртывание | `/deploy` | Подготовка агентов и их подключение к инфраструктуре | `deploy:deployment:read` | [reference/modules/vii-deploy](/ru/reference/modules/vii-deploy/) |
| Инвентарь | `/inventory` | Обнаружение и каталогизация каждого агента, MCP и модели | `inventory:catalog:read` | [reference/modules/i-inventory](/ru/reference/modules/i-inventory/) |
| Знания | `/knowledge` | Базы знаний, RAG и родословная данных | `knowledge:kb:read` | [reference/modules/viii-knowledge](/ru/reference/modules/viii-knowledge/) |
| Операции с моделями | `/model-operations` | Собственные модели, допуск и развёртывания | `models:registry:read` | [reference/modules/xxiii-model-operations](/ru/reference/modules/xxiii-model-operations/) |
| Модели | `/models` | Модели, маршрутизация и ключи провайдеров | `models:catalog:read` | [reference/modules/x-models](/ru/reference/modules/x-models/) |
| Мастер настройки | `/onboarding` | Пошаговая настройка развёртывания | `system:admin` | [start/quickstart](/ru/start/quickstart/) |
| Платформы | `/platforms` | Поверхности развёртывания, матрица соответствия и жизненный цикл моделей по платформам | `models:platforms:read` | [reference/modules/x-models](/ru/reference/modules/x-models/) |

### Управление

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Карта доступа | `/access-map` | Что каждый агент читает и записывает (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/ru/reference/modules/iii-access-map/) |
| Экспорт AgentCore | `/agentcore-export` | Планирование и применение экспорта политики Cedar в AWS AgentCore с проверкой будущих изменений до их применения. | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Управление Claude Code | `/claude-policy` | Управляемая политика, hooks, MCP, песочница и policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/ru/how-to/connectors/claude-code-hooks-pep/) |
| Консоль управления | `/console` | Подключение пользователей, SSO/IdP и формирование рабочих пространств и групп агентов. | `tenant:admin` | [reference/modules/xx-multi-tenancy](/ru/reference/modules/xx-multi-tenancy/) |
| Идентичности и NHI | `/identity` | SSO, SCIM, реестр NHI и граф WIF | `governance:identity:read` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Прокси инференса | `/inference-proxy` | Шлюзы прокси, правила DLP исходящего трафика и одобрения устройств | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/ru/reference/modules/inferenceproxy/) |
| Разрешения | `/permissions` | Идентичности, роли и одобрения | `governance:identity:read` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Ограничения частоты | `/rate-limits` | Инвентарь ограничений частоты Anthropic (только чтение) | `models:ratelimits:read` | [reference/modules/x-models](/ru/reference/modules/x-models/) |
| Резидентность данных | `/residency` | Закрепление каждой организации за регионом или отсутствие закрепления | `system:admin` | [reference/modules/xiii-compliance](/ru/reference/modules/xiii-compliance/) |
| Регулярные политики | `/routine-policies` | Минимальная периодичность, ограничения параллелизма, требования одобрения и allowlist cron для процедур Claude Code. | `governance:routine:read` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |

### Доказательства

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Внедрение Claude Code | `/adoption` | Продуктивность, принятие результатов и состав моделей | `adoption:metrics:read` | [reference/modules/claudeadoption](/ru/reference/modules/claudeadoption/) |
| Артефакты агентов | `/agent-artifacts` | Навыки, расширения MCP и файлы инструкций: реестр, состояние и BOM цепочки поставок | `models:registry:read` | [reference/modules/xxiii-model-operations](/ru/reference/modules/xxiii-model-operations/) |
| Цепочка поставок | `/attestation` | Аттестация релиза: SLSA, SBOM, VEX и Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/ru/how-to/verify-a-release/) |
| Реестр аудита | `/audit` | Журнал свидетельств с обнаружением подмены | `audit:read` | [reference/modules/ix-security](/ru/reference/modules/ix-security/) |
| Соответствие | `/compliance` | Фреймворки, контроли и свидетельства | `compliance:framework:read` | [reference/modules/xiii-compliance](/ru/reference/modules/xiii-compliance/) |
| Панели | `/dashboards` | KPI руководства и отчётность | любой вошедший пользователь | [reference/modules/xxi-executive-dashboards](/ru/reference/modules/xxi-executive-dashboards/) |
| Оценки | `/evals` | Качество, оценки и регрессии | `evals:run:read` | [reference/modules/xii-evals](/ru/reference/modules/xii-evals/) |
| Стоимость и FinOps | `/finops` | Стоимость токенов, бюджеты и расходы | `finops:spend:read` | [reference/modules/xi-finops](/ru/reference/modules/xi-finops/) |
| Экспорт состояния | `/posture-export` | Экспорт фактического состояния для центра управления | `posture:export:read` | [reference/modules/posture-export](/ru/reference/modules/posture-export/) |
| Записи | `/recordings` | Запись и воспроизведение привилегированных сессий | `recording:session:admin` | [reference/modules/recording](/ru/reference/modules/recording/) |
| Red team | `/red-team` | Состязательное тестирование ваших агентов | `redteam:target:read` | [reference/modules/xviii-redteam](/ru/reference/modules/xviii-redteam/) |
| Отчёты | `/reporting` | Создание и загрузка отчётов об управлении | `reporting:report:read` | [reference/modules/reporting](/ru/reference/modules/reporting/) |
| Безопасность | `/security` | Ограничители, расследования и аномалии | `security:finding:read` | [reference/modules/ix-security](/ru/reference/modules/ix-security/) |
| Просмотр сессии | `/session-viewer/$id` (только прямая ссылка) | Полная временная шкала одной записанной сессии, доступная из строки раздела записей, а не из боковой панели. | `recording:session:admin` | [reference/modules/recording](/ru/reference/modules/recording/) |
| Затраты команд | `/team-costs` | Расходы по командам с детализацией по проектам и моделям. | `finops:spend:read` | [reference/modules/xi-finops](/ru/reference/modules/xi-finops/) |

### Вход, настройка и учётная запись

Эти маршруты монтируются вне реестра функций. Маршруты с отметкой **вход не требуется**
обслуживаются до появления сессии; только эти маршруты консоли работают так.

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Принять приглашение | `/accept-invite` | Страница, на которую ведёт приглашение по электронной почте: приглашённый задаёт пароль и присоединяется к рабочему пространству без предварительной сессии. | **вход не требуется** | — |
| Войти | `/login` | Страница входа по учётным данным и токену для уже подготовленной учётной записи. | **вход не требуется** | — |
| Настройки | `/settings` | Настройки рабочего пространства и учётной записи | любой вошедший пользователь | — |
| Первоначальная настройка | `/setup` | Одноразовая страница, превращающая новое развёртывание в готовое к работе: она поглощает setup-токен и создаёт первую учётную запись владельца. | **вход не требуется** | — |
| Публичное состояние | `/status-page` | Здоровье компонентов для пользователей без входа, автоматически обновляемое, пока страница открыта. | **вход не требуется** | — |

<!-- END GENERATED olivares-console-routes -->

## Чего эта страница не сообщает

Это карта, а не руководство. Она говорит, какие экраны существуют, где они находятся и кто
может их открыть, но не проводит через задачу. Для этого начните с
[Путей по ролям](/ru/start/paths-by-role/) или [практических руководств](/ru/how-to/self-hosting/).

Экраны, чей backend отказывает закрыто, пока оператор его не подготовит, приведены здесь как
обычно: маршрут существует, и разрешение реально. Активные и ограниченные модули перечислены
в [обзоре модулей](/ru/reference/modules/overview/), а общее правило сформулировано на странице
[Честность и ограничения](/ru/start/honesty-and-limits/).
