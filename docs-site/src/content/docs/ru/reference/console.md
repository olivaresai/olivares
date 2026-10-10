---
title: Справочник консоли — все экраны и требуемые разрешения
description: >-
  Все маршруты консоли Olivares AI, сгруппированные по областям консоли, с
  требуемым разрешением RBAC и справочной страницей, которую открывает
  встроенная ссылка помощи. Сгенерировано из собственной переписи маршрутов консоли.
---

Эта страница — карта консоли. Здесь перечислен **каждый маршрут, монтируемый приложением**,
а не выборка или только те маршруты, которые кто-то вспомнил описать, с разрешением,
необходимым субъекту для входа, и ссылкой на дополнительные сведения.

У консоли **одна структура навигации: девять областей с секциями**. Каждый экран находится
в одной области и в одной её секции, и всё, что показывает, где находится экран, называет
одно и то же место: «Все области» (в боковой панели и «Ещё» на телефоне), страницы каталогов
областей, цепочка навигации, палитра команд и ряд ссылок над экраном, в котором перечислены
другие экраны его секции. Боковая панель закрепляет короткий список экранов, первая задача —
первой (Главная, Сессии, Инструменты ИИ, Одобрения), затем Работа, а закреплённый экран остаётся отмеченным на других экранах своей секции.
Фильтр навигации заменяет сгруппированное дерево ранжированным списком разрешённых
совпадений; палитра команд использует тот же индекс, тот же порядок и ту же проекцию прав.
Каталог области — страница ссылок (записей, которые разрешают ваши права), а не живое
чтение возможности, доступности или готовности.

Новая установка показывает только первую задачу: Главная, Сессии, Инструменты ИИ
с Провайдерами, Одобрения, мастер настройки и Настройки. Любой другой экран в ней —
предварительная версия: его адрес, API и CLI продолжают работать, а навигация, палитра
команд и сочетания клавиш его не показывают. Установка, существовавшая до обновления,
показывает все экраны, которые показывала раньше; так же ведёт себя установка, в которой
администратор выбрал модули (со следующего запуска), и установка с демонстрационными данными.

Страница **сгенерирована**. Перечень берётся из `web/src/features/route-census.json` —
только дополняемой переписи, которую `registry.route-conservation.test.ts` сверяет с
собранным роутером. Экран нельзя добавить, переместить или потерять без изменения этой
страницы. Название и однострочное описание каждого экрана — **собственные строки консоли**
из того же каталога переводов, который отображает боковая панель; здесь вы читаете то же,
что видите в продукте. Таблицы ниже группируют эти строки по областям: после «Главная» (обзора) и перед входом,
настройкой и учётной записью. Девять страниц каталогов областей стоят вместе с маршрутами,
смонтированными вне реестра функций.

:::note[Разрешения применяет движок, а не эта таблица]
Столбец `Требуется` указывает разрешение, которое консоль проверяет перед отображением
маршрута, используя действующие разрешения, полученные от движка. Движок независимо
авторизует запросы к API, включая запросы вне консоли. Видимость пункта не подтверждает,
что модуль настроен или готов к работе.
См. [Роли и разрешения](/ru/reference/modules/vi-governance/).
:::

## Как читать эту страницу

- **Экран** — название в боковой панели, каталогах областей и палитре команд.
- **Путь** — URL относительно origin консоли вашего развёртывания. Это опубликованный
  контракт: закладка, прямая ссылка из runbook и перекрёстная ссылка документации используют
  именно эту строку.
- **Требуется** — разрешение RBAC. `любой вошедший пользователь` означает, что маршрут открыт
  каждому аутентифицированному субъекту; **вход не требуется** означает, что он обслуживается
  ещё до появления сессии.
- **Справка** — страница, которую открывает собственная ссылка помощи консоли для этого экрана.

Следующие заголовки — это области в том порядке, в котором их перечисляет «Все области»:
Инфраструктура, ИИ, Данные и контекст, Работа и коммуникации, Автоматизация,
Безопасность и идентичность, Развёртывание, Наблюдаемость и свидетельства, затем
Система и настройки.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

Консоль публикует **83 маршрута**. Каждый из них приведён в таблицах ниже вместе с требуемым
разрешением и справочной страницей, которую открывает встроенная ссылка помощи.

### Главная

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Главная | `/` | Обзор инфраструктуры и её здоровья | любой вошедший пользователь | [главная документации](/ru/) |

### Инфраструктура

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Окружение | `/estate` | Просматривайте настроенные ресурсы и их связи, включая зависимости рабочих элементов. | `sessions:run:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Инвентаризация | `/inventory` | Обнаружение и каталогизация агентов, MCP-серверов и моделей, которые наблюдали коннекторы. | `inventory:catalog:read` | [reference/modules/i-inventory](/ru/reference/modules/i-inventory/) |
| Рабочее пространство | `/workspace` | Агенты, сессии, ресурсы и активность в пределах одного рабочего пространства | `tenant:read` | [reference/modules/xx-multi-tenancy](/ru/reference/modules/xx-multi-tenancy/) |

### ИИ

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Инструменты агентов | `/agent-tools` | Находите, устанавливайте и обновляйте инструменты агентов на этом хосте и отслеживайте каждую установку; только для администраторов развёртывания | `system:admin` | [how-to/add-a-provider](/ru/how-to/add-a-provider/) |
| Сессии | `/agentops` | Запускайте сессии и следите за работой каждой, включая те, что находит Olivares | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/ru/how-to/run-claude-code-with-olivares/) |
| MCP-серверы | `/mcp-servers` | Подключайте удалённые MCP-серверы к этой организации, проверяйте их и выбирайте, какие инструменты могут использовать сессии | `tenant:admin` | [how-to/connectors/mcp-governance](/ru/how-to/connectors/mcp-governance/) |
| Операции с моделями | `/model-operations` | Собственные модели, допуск и развёртывания | `models:registry:read` | [reference/modules/xxiii-model-operations](/ru/reference/modules/xxiii-model-operations/) |
| Модели | `/models` | Модели, маршрутизация и ключи провайдеров | `models:catalog:read` | [reference/modules/x-models](/ru/reference/modules/x-models/) |
| Платформы | `/platforms` | Поверхности развёртывания, матрица соответствия и жизненный цикл моделей по платформам | `models:platforms:read` | [reference/modules/x-models](/ru/reference/modules/x-models/) |
| Аккаунты провайдеров | `/provider-accounts` | Список именованных аккаунтов провайдеров и принятие существующего профиля провайдера как аккаунта | `sessions:account:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Привязки источников | `/provider-bindings` | Выделение настроенных источников, в применённой этим узлом ревизии, профилям провайдеров | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Профили провайдеров | `/provider-profiles` | Регистрация и администрирование домашних каталогов провайдеров, под которыми запускаются сессии, и чтение их конфигурации по запросу | `sessions:profile:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Провайдеры | `/providers` | Регистрация API-ключей и конечных точек, с которыми запускаются сессии; их проверка, замена и отзыв | `sessions:provider:read` | [how-to/add-a-provider](/ru/how-to/add-a-provider/) |
| Ограничения частоты | `/rate-limits` | Инвентарь ограничений частоты Anthropic (только чтение) | `models:ratelimits:read` | [reference/modules/x-models](/ru/reference/modules/x-models/) |
| Песочница | `/sandbox` | Изолированное тестирование и воспроизведение агентов | `sandbox:run:read` | [reference/modules/xvii-sandbox](/ru/reference/modules/xvii-sandbox/) |
| Сессии | `/sessions` | Запускайте сессии и следите за работой каждой, включая те, что находит Olivares | `sessions:live:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Голос | `/voice` | Голосовые сессии и сессии реального времени | `voice:session:read` | [reference/modules/xvi-voice](/ru/reference/modules/xvi-voice/) |
| Шаблоны рабочих пространств | `/workspace-templates` | Повторно используемые снимки конфигурации сессии: hooks, настройки, коннекторы и политики. | `sessions:template:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |

### Данные и контекст

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Артефакты агентов | `/agent-artifacts` | Навыки, расширения MCP и файлы инструкций: реестр, состояние и BOM цепочки поставок | `models:registry:read` | [reference/modules/xxiii-model-operations](/ru/reference/modules/xxiii-model-operations/) |
| MCP и навыки | `/capabilities` | Управление серверами MCP, навыками и инструментами | `capabilities:catalog:read` | [reference/modules/v-capabilities](/ru/reference/modules/v-capabilities/) |
| Каталог | `/catalog` | Курируемые и одобренные агенты и возможности | `catalog:entry:read` | [reference/modules/xiv-catalog](/ru/reference/modules/xiv-catalog/) |
| Знания | `/knowledge` | Базы знаний, RAG и родословная данных | `knowledge:kb:read` | [reference/modules/viii-knowledge](/ru/reference/modules/viii-knowledge/) |
| Каталог навыков | `/skills` | Просмотр пакетов навыков и их назначение отделам, группам агентов и агентам | `skills:catalog:read` | [reference/console](/ru/reference/console/) |

### Работа и коммуникации

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Коммуникации | `/communications` | Каналы, прямые уведомления и личный ящик выбранного рабочего пространства | `sessions:channel:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Администрирование каналов | `/communications/administration` | Администрирование каналов: конфигурация и история выдач, каждое действие под текущим ETag канала | `sessions:channel:admin` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Передачи | `/communications/handoffs` | Адресованные вам предложения принять ответственность за работу: прочитайте контекст и примите или отклоните | `sessions:delivery:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Ящик коммуникаций | `/communications/inbox` | Ваш точный ящик: доставки, адресованные вам, читаются заново и подтверждаются явно | `sessions:delivery:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Новый канал | `/communications/new` | Создание канала с явными начальными правами | `sessions:channel:write` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Привязки протоколов | `/communications/protocol-bindings` | Компоновка и сверка управляемых привязок A2A и MCP | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Работа | `/work` | Общая работа: единицы, зависимости, приемка и решения | `sessions:work:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |

### Автоматизация

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Оповещения | `/alerting` | Маршрутизация находок к назначениям и проверка доставок | `notify:route:read` | [reference/modules/xv-notify](/ru/reference/modules/xv-notify/) |
| Автоматизации | `/automations` | Все три контура автоматизации и каталог их триггеров | `orchestration:schedule:read` | [reference/modules/iv-orchestration](/ru/reference/modules/iv-orchestration/) |
| Webhooks и события | `/eventing` | Исходящие webhook-подписки, журнал их доставки и очередь недоставленных сообщений. | `eventing:subscription:read` | [reference/modules/eventing](/ru/reference/modules/eventing/) |
| Оркестрация | `/orchestration` | Координация между агентами и расписания | `orchestration:graph:read` | [reference/modules/iv-orchestration](/ru/reference/modules/iv-orchestration/) |

### Безопасность и идентичность

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Карта доступа | `/access-map` | Что каждый агент читает и записывает (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/ru/reference/modules/iii-access-map/) |
| Экспорт в AgentCore | `/agentcore-export` | Планирование, проверка и применение проекции правил управления этого тенанта на AWS AgentCore в виде политик Cedar; планирование ничего не записывает | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Управление Claude Code | `/claude-policy` | Управляемая политика, hooks, MCP, песочница и policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/ru/how-to/connectors/claude-code-hooks-pep/) |
| Идентичности и NHI | `/identity` | SSO, SCIM, реестр NHI и граф WIF | `governance:identity:read` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Прокси инференса | `/inference-proxy` | Шлюзы прокси, правила DLP исходящего трафика и одобрения устройств | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/ru/reference/modules/inferenceproxy/) |
| Аварийный выключатель | `/killswitch` | Экстренная остановка, восстановление с двойным контролем и локализация guardian | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/ru/how-to/cookbook/kill-switch-drill/) |
| Разрешения | `/permissions` | Идентичности, роли и одобрения | `governance:identity:read` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Red team | `/red-team` | Состязательное тестирование ваших агентов | `redteam:target:read` | [reference/modules/xviii-redteam](/ru/reference/modules/xviii-redteam/) |
| Резидентность данных | `/residency` | Закрепление каждой организации за регионом или отсутствие закрепления | `system:admin` | [reference/modules/xiii-compliance](/ru/reference/modules/xiii-compliance/) |
| Регулярные политики | `/routine-policies` | Минимальная периодичность, ограничения параллелизма, требования одобрения и allowlist cron для процедур Claude Code. | `governance:routine:read` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Безопасность | `/security` | Ограничители, расследования и аномалии | `security:finding:read` | [reference/modules/ix-security](/ru/reference/modules/ix-security/) |

### Развёртывание

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Развёртывание | `/deploy` | Подготовка агентов и их подключение к инфраструктуре | `deploy:deployment:read` | [reference/modules/vii-deploy](/ru/reference/modules/vii-deploy/) |
| Публикация в Git | `/git-publication` | Отправка коммитов, открытие pull request и слияние через одобренные цели Git | `gitpublish:target:read` | [reference/modules/gitpublish](/ru/reference/modules/gitpublish/) |

### Наблюдаемость и свидетельства

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Внедрение Claude Code | `/adoption` | Продуктивность, принятие результатов и состав моделей | `adoption:metrics:read` | [reference/modules/claudeadoption](/ru/reference/modules/claudeadoption/) |
| Цепочка поставок | `/attestation` | Аттестация релиза: SLSA, SBOM, VEX и Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/ru/how-to/verify-a-release/) |
| Реестр аудита | `/audit` | Журнал свидетельств с обнаружением подмены | `audit:read` | [reference/modules/ix-security](/ru/reference/modules/ix-security/) |
| Соответствие | `/compliance` | Фреймворки, контроли и свидетельства | `compliance:framework:read` | [reference/modules/xiii-compliance](/ru/reference/modules/xiii-compliance/) |
| Панели | `/dashboards` | KPI руководства и отчётность | любой вошедший пользователь | [reference/modules/xxi-executive-dashboards](/ru/reference/modules/xxi-executive-dashboards/) |
| Оценки | `/evals` | Качество, оценки и регрессии | `evals:run:read` | [reference/modules/xii-evals](/ru/reference/modules/xii-evals/) |
| Стоимость и FinOps | `/finops` | Стоимость токенов, бюджеты и расходы | `finops:spend:read` | [reference/modules/xi-finops](/ru/reference/modules/xi-finops/) |
| Здоровье и SLA | `/health` | Время работы и SLA агентов и MCP | `health:status:read` | [reference/modules/xxii-health](/ru/reference/modules/xxii-health/) |
| Наблюдаемость | `/observability` | Здоровье приёма по стандартам и детализация трассировок | `health:status:read` | [reference/modules/observability](/ru/reference/modules/observability/) |
| Экспорт состояния | `/posture-export` | Экспорт фактического состояния для центра управления | `posture:export:read` | [reference/modules/posture-export](/ru/reference/modules/posture-export/) |
| Записи | `/recordings` | Запись и воспроизведение привилегированных сессий | `recording:session:admin` | [reference/modules/recording](/ru/reference/modules/recording/) |
| Отчёты | `/reporting` | Создание и загрузка отчётов об управлении | `reporting:report:read` | [reference/modules/reporting](/ru/reference/modules/reporting/) |
| Просмотр сессии | `/session-viewer/$id` (только прямая ссылка) | Полная временная шкала одной записанной сессии, доступная из строки раздела записей, а не из боковой панели. | `recording:session:admin` | [reference/modules/recording](/ru/reference/modules/recording/) |
| Затраты команд | `/team-costs` | Расходы по командам с детализацией по проектам и моделям. | `finops:spend:read` | [reference/modules/xi-finops](/ru/reference/modules/xi-finops/) |

### Система и настройки

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Песочница API | `/api-playground` | Интерактивное изучение и тестирование API control plane | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/ru/reference/modules/xix-api-manage-as-code/) |
| Резервные копии | `/backups` | Запуск, планирование, загрузка и восстановление резервных копий со вторым подтверждением разрушающего действия. | `system:admin` | [how-to/backup-and-restore](/ru/how-to/backup-and-restore/) |
| Администрирование | `/console` | Пользователи, SSO/IdP, рабочие пространства, группы агентов, роли, секреты, коннекторы, API-ключи и лицензия этой установки | `tenant:admin` | [reference/modules/xx-multi-tenancy](/ru/reference/modules/xx-multi-tenancy/) |
| Сравнение источника | `/console/sources/diff` | Сравнение базовой и head-ревизии подключённого Git-репозитория, файл за файлом | `system:admin` | [reference/console](/ru/reference/console/) |
| Журналы | `/logs` | Поток журнала движка в реальном времени с фильтрацией по уровню и модулю, поиском и паузой. | `system:admin` | [how-to/troubleshooting](/ru/how-to/troubleshooting/) |
| Мастер настройки | `/onboarding` | Пошаговая настройка развёртывания | `system:admin` | [start/quickstart](/ru/start/quickstart/) |
| Арендаторы | `/tenants` | Приостановка и восстановление обслуживания арендатора | `system:admin` | [how-to/troubleshooting](/ru/how-to/troubleshooting/) |

### Вход, настройка и учётная запись

Эти маршруты монтируются вне реестра функций. Маршруты с отметкой **вход не требуется**
обслуживаются до появления сессии; только эти маршруты консоли работают так.

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Принять приглашение | `/accept-invite` | Страница, на которую ведёт приглашение по электронной почте: приглашённый задаёт пароль и присоединяется к рабочему пространству без предварительной сессии. | **вход не требуется** | — |
| ИИ | `/areas/ai` | Каталог области «ИИ»: наблюдение за сеансами и управление ими, профили и окружения провайдеров, модели, специализированное выполнение и справочная информация о провайдерах. Показывает записи, разрешённые вашими правами. | любой вошедший пользователь | — |
| Автоматизация | `/areas/automation` | Каталог области «Автоматизация»: потоки и оркестрация, события и уведомления. Показывает записи, разрешённые вашими правами. | любой вошедший пользователь | — |
| Данные и контекст | `/areas/data-context` | Каталог области «Данные и контекст»: возможности, знания и артефакты. Показывает записи, разрешённые вашими правами. | любой вошедший пользователь | — |
| Развёртывание | `/areas/deployment` | Каталог области «Развёртывание»: подготовка и контроль развёртываний. Показывает записи, разрешённые вашими правами. | любой вошедший пользователь | — |
| Инфраструктура | `/areas/infrastructure` | Каталог области «Инфраструктура»: инвентаризация и рабочие пространства среды. Показывает записи, разрешённые вашими правами. | любой вошедший пользователь | — |
| Наблюдаемость и свидетельства | `/areas/observation` | Каталог области «Наблюдаемость и свидетельства»: состояние и активность, затраты и внедрение, аудит, оценка и свидетельства. Показывает записи, разрешённые вашими правами. | любой вошедший пользователь | — |
| Безопасность и идентичность | `/areas/security-identity` | Каталог области «Безопасность и идентичность»: идентификация и доступ, политики и границы управления, защита и реагирование. Показывает записи, разрешённые вашими правами. | любой вошедший пользователь | — |
| Система и настройки | `/areas/system` | Каталог области «Система и настройки»: администрирование, установка и обслуживание, инструменты разработчика и личные настройки. Показывает записи, разрешённые вашими правами. | любой вошедший пользователь | — |
| Работа и коммуникации | `/areas/work-communications` | Каталог области «Работа и коммуникации»: постоянно хранимый список работ между сеансами и управляемые коммуникации. Показывает записи, разрешённые вашими правами. | любой вошедший пользователь | — |
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
[Честность и ограничения](/ru/start/honesty-and-limits/). Список каталога области — не живое
чтение возможности или готовности.
