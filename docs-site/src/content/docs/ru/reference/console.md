---
title: Справочник консоли — все экраны и требуемые разрешения
description: >-
  Все маршруты консоли Olivares AI, сгруппированные по областям консоли, с
  требуемым разрешением RBAC и справочной страницей, которую открывает
  встроенная ссылка помощи. Сгенерировано из собственной переписи маршрутов консоли.
---

:::note[Редакция]
Источники каталогов (LDAP, Okta/Entra и IdP агентов/нагрузок), жизненный цикл управляемых идентичностей и входящий SCIM-сервер требуют дополнений Business/Enterprise. Community сохраняет общий приём наблюдений, каталог ресурсов, результаты проверок безопасности, Access Map, инвентаризацию идентичностей, SSO с одним IdP, локальные роли и привязки, политики и одобрения. Платные панели и команды CLI скрыты; их опубликованные API-маршруты возвращают HTTP `501`. Включение модуля не добавляет реализацию Business.
:::

Эта страница — карта консоли. Здесь перечислен **каждый маршрут, монтируемый приложением**,
а не выборка или только те маршруты, которые кто-то вспомнил описать, с разрешением,
необходимым субъекту для входа, и ссылкой на дополнительные сведения.

У консоли **одна навигация: левая боковая панель**. Она перечисляет места в четырёх
группах — **Работа** (Главная, Сессии, Файлы и рабочие области, Работа и коммуникации),
**Инструменты** (Инструменты ИИ, Модели, MCP и навыки, Источники), **Контроль** (Правила и
одобрения, Использование и стоимость, Активность и аудит, Карта доступа) и
**Администрирование** (Люди и группы, Безопасность, Эксплуатация, Лицензия), — и
«Настройки» внизу. Каждый экран находится в одном месте, а строка ссылок над экраном
перечисляет другие экраны этого места. Место появляется, только если ваши права разрешают
хотя бы один из его экранов, а экран Business — только в редакции Business. Панель
сворачивается до значков и снова раскрывается; на узком экране та же панель открывается
как выдвижная по кнопке меню. Верхняя строка содержит заголовок страницы, поиск (⌘K или
Ctrl+K), уведомления и меню учётной записи. Палитра команд ищет те же экраны с теми же
правами. Бывшие адреса каталогов областей (`/areas/…`) открывают место, где теперь
находятся их экраны, сохраняя запрос и фрагмент.

Новая установка показывает только первую задачу: Главная, Сессии, Инструменты ИИ,
Одобрения, мастер настройки и Настройки. Любой другой экран в ней —
предварительная версия: его адрес, API и CLI продолжают работать, а навигация, палитра
команд и сочетания клавиш его не показывают. Установка, существовавшая до обновления,
показывает все экраны, которые показывала раньше; так же ведёт себя установка, в которой
администратор выбрал модули (со следующего запуска), и установка с демонстрационными данными.

Страница **сгенерирована**. Перечень берётся из `web/src/features/route-census.json` —
только дополняемой переписи, которую `registry.route-conservation.test.ts` сверяет с
собранным роутером. Экран нельзя добавить, переместить или потерять без изменения этой
страницы. Название и однострочное описание каждого экрана — **собственные строки консоли**
из того же каталога переводов, который отображает консоль; здесь вы читаете то же,
что видите в продукте. Таблицы ниже группируют эти строки по областям: после «Главная» (обзора) и перед входом,
настройкой и учётной записью. Девять бывших адресов каталогов областей стоят вместе с маршрутами,
смонтированными вне реестра функций; каждый теперь перенаправляет в своё место.

:::note[Разрешения применяет движок, а не эта таблица]
Столбец `Требуется` указывает разрешение, которое консоль проверяет перед отображением
маршрута, используя действующие разрешения, полученные от движка. Движок независимо
авторизует запросы к API, включая запросы вне консоли. Видимость пункта не подтверждает,
что модуль настроен или готов к работе.
См. [Роли и разрешения](/ru/reference/modules/vi-governance/).
:::

## Как читать эту страницу

- **Экран** — название в строке ссылок над экраном и в палитре команд.
- **Путь** — URL относительно origin консоли вашего развёртывания. Это опубликованный
  контракт: закладка, прямая ссылка из runbook и перекрёстная ссылка документации используют
  именно эту строку.
- **Требуется** — разрешение RBAC. `любой вошедший пользователь` означает, что маршрут открыт
  каждому аутентифицированному субъекту; **вход не требуется** означает, что он обслуживается
  ещё до появления сессии.
- **Справка** — страница, которую открывает собственная ссылка помощи консоли для этого экрана.

Следующие заголовки — это области реестра, по которым перепись маршрутов группирует экраны:
Инфраструктура, ИИ, Данные и контекст, Работа и коммуникации, Автоматизация,
Безопасность и идентичность, Поставка, Надзор, затем
Система и настройки.

<!-- BEGIN GENERATED olivares-console-routes — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

Консоль публикует **66 маршрутов**. Каждый из них приведён в таблицах ниже вместе с требуемым
разрешением и справочной страницей, которую открывает встроенная ссылка помощи.

### Главная

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Главная | `/` | Обзор инфраструктуры и её здоровья | любой вошедший пользователь | [главная документации](/ru/) |

### Инфраструктура

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Парк | `/estate` | Просматривайте настроенные ресурсы и их связи, включая зависимости рабочих элементов. | `sessions:run:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Инвентаризация | `/inventory` | Обнаружение и каталогизация агентов, MCP-серверов и моделей, которые наблюдали коннекторы. | `inventory:catalog:read` | [reference/modules/i-inventory](/ru/reference/modules/i-inventory/) |
| Рабочая область | `/workspace` | Агенты, сессии, ресурсы и активность в пределах одной рабочей области | `tenant:read` | [reference/modules/xx-multi-tenancy](/ru/reference/modules/xx-multi-tenancy/) |

### ИИ

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Инструменты агентов | `/agent-tools` | Учётные записи и API-ключи, с которыми работают сессии; устанавливают и подключают инструменты администраторы развёртывания | `sessions:account:read` | [how-to/add-a-provider](/ru/how-to/add-a-provider/) |
| Управление сессиями | `/agentops` | Запускайте сессии и следите за работой каждой, включая те, что находит Olivares | `sessions:run:read` | [how-to/run-claude-code-with-olivares](/ru/how-to/run-claude-code-with-olivares/) |
| Операции с моделями | `/model-operations` | Собственные модели, допуск и развёртывания | `models:registry:read` | [reference/modules/xxiii-model-operations](/ru/reference/modules/xxiii-model-operations/) |
| Модели | `/models` | Модели, маршрутизация и управление | `models:catalog:read` | [reference/modules/x-models](/ru/reference/modules/x-models/) |
| Платформы | `/platforms` | Поверхности развёртывания, матрица соответствия и жизненный цикл моделей по платформам | `models:platforms:read` | [reference/modules/x-models](/ru/reference/modules/x-models/) |
| Аккаунты провайдеров | `/provider-accounts` | Список именованных аккаунтов провайдеров и принятие существующего профиля провайдера как аккаунта | `sessions:account:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Привязки источников | `/provider-bindings` | Связывание настроенных источников с профилями провайдеров | `sessions:profile-binding:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Профили провайдеров | `/provider-profiles` | Регистрация и администрирование домашних каталогов провайдеров, под которыми запускаются сессии, и чтение их конфигурации по запросу | `sessions:profile:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Провайдеры | `/providers` | Регистрация API-ключей и конечных точек, с которыми запускаются сессии; их проверка, замена и отзыв | `sessions:provider:read` | [how-to/add-a-provider](/ru/how-to/add-a-provider/) |
| Ограничения частоты | `/rate-limits` | Инвентарь ограничений частоты Anthropic (только чтение) | `models:ratelimits:read` | [reference/modules/x-models](/ru/reference/modules/x-models/) |
| Охват | `/reach` | Смотрите, до каких адресов может дотянуться каждый инструмент и какие ждут одобрения. | `accessmap:graph:read` | [reference/modules/iii-access-map](/ru/reference/modules/iii-access-map/) |
| Песочница | `/sandbox` | Изолированное тестирование и воспроизведение агентов | `sandbox:run:read` | [reference/modules/xvii-sandbox](/ru/reference/modules/xvii-sandbox/) |
| Сессии | `/sessions` | Запускайте сессии и следите за работой каждой, включая те, что находит Olivares | `sessions:live:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Голос | `/voice` | Голосовые сессии и сессии реального времени | `voice:session:read` | [reference/modules/xvi-voice](/ru/reference/modules/xvi-voice/) |
| Шаблоны рабочих областей | `/workspace-templates` | Повторно используемые снимки конфигурации сессии: hooks, настройки, коннекторы и политики. | `sessions:template:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |

### Данные и контекст

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Артефакты агентов | `/agent-artifacts` | Навыки, расширения MCP и файлы инструкций: реестр, состояние и BOM цепочки поставок | `models:registry:read` | [reference/modules/xxiii-model-operations](/ru/reference/modules/xxiii-model-operations/) |
| MCP и навыки | `/capabilities` | Управление серверами MCP, навыками и инструментами | `capabilities:catalog:read` | [reference/modules/v-capabilities](/ru/reference/modules/v-capabilities/) |
| Каталог | `/catalog` | Курируемые и одобренные агенты и возможности | `catalog:entry:read` | [reference/modules/xiv-catalog](/ru/reference/modules/xiv-catalog/) |
| Знания | `/knowledge` | Базы знаний, RAG и родословная данных | `knowledge:kb:read` | [reference/modules/viii-knowledge](/ru/reference/modules/viii-knowledge/) |

### Работа и коммуникации

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Коммуникации | `/communications` | Каналы, прямые уведомления и личный ящик выбранного рабочего пространства | `sessions:channel:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Администрирование каналов | `/communications/administration` | Администрирование каналов: конфигурация и история выдач | `sessions:channel:admin` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Передачи | `/communications/handoffs` | Адресованные вам предложения принять ответственность за работу: прочитайте контекст и примите или отклоните | `sessions:delivery:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Ящик коммуникаций | `/communications/inbox` | Сообщения, адресованные вам: откройте и подтвердите каждое | `sessions:delivery:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Новый канал | `/communications/new` | Создание канала с явными начальными правами | `sessions:channel:write` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Привязки протоколов | `/communications/protocol-bindings` | Компоновка и сверка управляемых привязок A2A и MCP | `sessions:protocol-binding:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |
| Работа | `/work` | Общая работа: единицы, зависимости, приемка и решения | `sessions:work:read` | [reference/modules/ii-sessions](/ru/reference/modules/ii-sessions/) |

### Автоматизация

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Оповещения | `/alerting` | Маршрутизация находок к назначениям и проверка доставок | `notify:route:read` | [reference/modules/xv-notify](/ru/reference/modules/xv-notify/) |
| Webhooks и события | `/eventing` | Исходящие webhook-подписки, журнал их доставки и очередь недоставленных сообщений. | `eventing:subscription:read` | [reference/modules/eventing](/ru/reference/modules/eventing/) |

### Безопасность и идентичность

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Карта доступа | `/access-map` | Что каждый агент читает и записывает (R/RW) | `accessmap:graph:read` | [reference/modules/iii-access-map](/ru/reference/modules/iii-access-map/) |
| Экспорт политик | `/agentcore-export` | Планирование, проверка и применение проекции правил управления этого тенанта на AWS AgentCore в виде политик Cedar; планирование ничего не записывает | `governance:agentcore-export:admin` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Управляемая политика | `/claude-policy` | Управляемая политика, hooks, MCP, песочница и policy-as-code | `governance:claude-policy:read` | [how-to/connectors/claude-code-hooks-pep](/ru/how-to/connectors/claude-code-hooks-pep/) |
| Личности и доступ | `/identity` | SSO, SCIM и личности, которые используют ваши агенты | `governance:identity:read` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Прокси инференса | `/inference-proxy` | Шлюзы прокси, правила DLP исходящего трафика и одобрения устройств | `inferenceproxy:config:read` | [reference/modules/inferenceproxy](/ru/reference/modules/inferenceproxy/) |
| Аварийный выключатель | `/killswitch` | Экстренная остановка, восстановление с двойным контролем и локализация guardian | `governance:killswitch:read` | [how-to/cookbook/kill-switch-drill](/ru/how-to/cookbook/kill-switch-drill/) |
| Разрешения | `/permissions` | Идентичности, роли и одобрения | `governance:identity:read` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Регулярные политики | `/routine-policies` | Минимальная периодичность, ограничения параллелизма, требования одобрения и allowlist cron для процедур Claude Code. | `governance:routine:read` | [reference/modules/vi-governance](/ru/reference/modules/vi-governance/) |
| Безопасность | `/security` | Ограничители, расследования и аномалии | `security:finding:read` | [reference/modules/ix-security](/ru/reference/modules/ix-security/) |
| Хранилище | `/vault` | Ваши пароли, заметки и коды аутентификатора | `tenant:read` | [reference/console](/ru/reference/console/) |

### Поставка

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Публикация в Git | `/git-publication` | Отправка коммитов, открытие pull request и слияние через одобренные цели Git | `gitpublish:target:read` | [reference/modules/gitpublish](/ru/reference/modules/gitpublish/) |

### Надзор

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Внедрение | `/adoption` | Продуктивность, принятие результатов и состав моделей | `adoption:metrics:read` | [reference/modules/claudeadoption](/ru/reference/modules/claudeadoption/) |
| Цепочка поставок | `/attestation` | Аттестация релиза: SLSA, SBOM, VEX и Scorecard | `observability:attestation:read` | [how-to/verify-a-release](/ru/how-to/verify-a-release/) |
| Реестр аудита | `/audit` | Журнал свидетельств с обнаружением подмены | `audit:read` | [reference/modules/ix-security](/ru/reference/modules/ix-security/) |
| Панели | `/dashboards` | KPI руководства и отчётность | любой вошедший пользователь | [reference/modules/xxi-executive-dashboards](/ru/reference/modules/xxi-executive-dashboards/) |
| Оценки | `/evals` | Качество, оценки и регрессии | `evals:run:read` | [reference/modules/xii-evals](/ru/reference/modules/xii-evals/) |
| Здоровье и SLA | `/health` | Время работы и SLA агентов и MCP | `health:status:read` | [reference/modules/xxii-health](/ru/reference/modules/xxii-health/) |
| Наблюдаемость | `/observability` | Здоровье приёма по стандартам и детализация трассировок | `health:status:read` | [reference/modules/observability](/ru/reference/modules/observability/) |
| Хранение | `/retention` | Срок хранения каждого класса записей, проход, который его применяет, и его запечатанные сертификаты | `compliance:retention:read` | [reference/modules/xiii-compliance](/ru/reference/modules/xiii-compliance/) |
| Бюджеты | `/stored-budgets` | Потребление относительно каждого сохранённого лимита. Зарезервированная ёмкость засчитывается в лимит. | `finops:budget:read` | [reference/modules/xi-finops](/ru/reference/modules/xi-finops/) |
| Использование | `/usage` | Официальные 5-часовые и недельные лимиты каждой учётной записи, ближайшая к лимиту первой, и токены и стоимость за сегодня. | `sessions:run:read` | [reference/console](/ru/reference/console/) |

### Система и настройки

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Песочница API | `/api-playground` | Интерактивное изучение и тестирование API control plane | `tenant:admin` | [reference/modules/xix-api-manage-as-code](/ru/reference/modules/xix-api-manage-as-code/) |
| Резервные копии | `/backups` | Запуск, планирование, загрузка и восстановление резервных копий со вторым подтверждением разрушающего действия. | `system:admin` | [how-to/backup-and-restore](/ru/how-to/backup-and-restore/) |
| Администрирование | `/console` | Пользователи, SSO/IdP, рабочие пространства, группы агентов, роли, секреты, коннекторы, API-ключи и лицензия этой установки | `tenant:admin` | [reference/modules/xx-multi-tenancy](/ru/reference/modules/xx-multi-tenancy/) |
| Сравнение источника | `/console/sources/diff` | Сравнение базовой и head-ревизии подключённого Git-репозитория, файл за файлом | `system:admin` | [reference/console](/ru/reference/console/) |
| Журналы | `/logs` | Поток журнала движка в реальном времени с фильтрацией по уровню и модулю, поиском и паузой. | `system:admin` | [how-to/troubleshooting](/ru/how-to/troubleshooting/) |
| Мастер настройки | `/onboarding` | Пошаговая настройка развёртывания | `system:admin` | [start/quickstart](/ru/start/quickstart/) |
| Обновления | `/updates` | Версия, которую выполняет этот движок, самый новый подписанный релиз его канала и одно действие для установки | `system:admin` | [how-to/upgrade-and-rollback](/ru/how-to/upgrade-and-rollback/) |

### Вход, настройка и учётная запись

Эти маршруты монтируются вне реестра функций. Маршруты с отметкой **вход не требуется**
обслуживаются до появления сессии; только эти маршруты консоли работают так.

| Экран | Путь | Назначение | Требуется | Справка |
|---|---|---|---|---|
| Принять приглашение | `/accept-invite` | Страница, на которую ведёт приглашение по электронной почте: приглашённый задаёт пароль и присоединяется к рабочему пространству без предварительной сессии. | **вход не требуется** | — |
| Войти | `/login` | Страница входа по учётным данным и токену для уже подготовленной учётной записи. | **вход не требуется** | — |
| Подключить устройство | `/pair` | Страница одноразовой ссылки для подключения устройства: новое устройство входит без предварительной сессии. | **вход не требуется** | — |
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
