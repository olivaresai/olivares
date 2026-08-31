---
title: Интеграция Codex
description: >-
  Подключите Codex к control plane управления: коннектор, managed config,
  управляемый хук и данные, которые показывает консоль после запуска.
---

Olivares AI интегрирует Codex через три взаимодополняющих контура. В режиме только для чтения
источник `codex` читает Analytics, Compliance, Audit Logs и выставленные затраты, используя
enterprise-учётные данные автоматизации. Коннектор `codex-managed-config` инвентаризирует и
проверяет развёрнутую системную политику. Наконец, `olivares codex-hook` направляет сессии и
решения по инструментам в локальный PEP. Сессия, аутентифицированная через личную подписку
ChatGPT, сама по себе не даёт доступа к enterprise API.

## Добавление Codex

### Предварительные требования

- Enterprise-тенант Olivares AI и учётная запись superadmin с повышением AAL3 для операций с
  реестром.
- Для enterprise-ingestion — ключ API платформы или access token workspace с нужными read-scope,
  а также `workspace_id`. Вход в Codex CLI через ChatGPT не предоставляет коннектору учётные данные.
- Административный доступ к системному уровню хоста для распространения
  `/etc/codex/requirements.toml`, `/etc/codex/managed_config.toml` и доверенного хука.
- Отдельный loopback-сокет для PEP Codex. Его значение по умолчанию — `127.0.0.1:8448`; не
  используйте его совместно с Claude или Grok, поскольку каждый агент ожидает свой формат ответа.

1. Откройте **Control console** (`/console`) и выберите **Connectors**.
2. Добавьте источник типа `codex` со стабильным именем, тенантом и пакетным интервалом. `300`
   секунд — разумная начальная точка для пилота; настройте частоту по бюджету API и цели свежести.
3. Для enterprise-источника введите учётные данные в секретное поле `api_key`, выберите
   `auth_mode` (`api_key` или `access_token`) и укажите `workspace_id`. Консоль запечатывает
   значение и никогда его не возвращает. Сохраните, протестируйте и перезагрузите источник.

Можно также добавить `codex` без учётных данных для локальной инвентаризации каталога. Этот режим
не запрашивает Analytics, Compliance, Audit Logs или Costs, а `Gather` не выдаёт удалённых наблюдений.

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="Настройте, кто получает доступ и чем может администрировать: подключайте пользователей, настраивайте SSO и формируйте рабочие области и группы агентов.">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="Настройте, кто получает доступ и чем может администрировать: подключайте пользователей, настраивайте SSO и формируйте рабочие области и группы агентов.">

## Настройка Codex

### 1. Enterprise-источник только для чтения

Следующие настройки определяют покрытие:

| Настройка | По умолчанию | Назначение |
|---|---:|---|
| `api_key` | пусто | Ссылка на учётные данные автоматизации. Пустое значение включает только офлайн-каталог. |
| `auth_mode` | `api_key` | Определяет учётные данные как `api_key` или `access_token`; оба передаются как Bearer-токены. |
| `workspace_id` | пусто | Требуется для Analytics и Compliance в рамках workspace. |
| `analytics` | `true` | Использование и внедрение Codex; создаёт структурированные образцы и находки. |
| `compliance` | `true` | Логи Codex Compliance как свидетельства активности. |
| `audit` | `true` | Audit Logs организации как свидетельства. |
| `costs` | `false` | Ежедневная выставленная стоимость. Включайте с `project_id`, чтобы не приписывать Codex посторонние расходы. |
| `attribute_email` | `false` | Сохраняет `user_id` как стабильного субъекта и не использует e-mail как PII для атрибуции. |
| `compliance_prompt_scan` | `false` | При включении временно ищет шаблоны риска и сохраняет только структурированные находки. |
| `otlp_http` | `false` | Экспериментальный приёмник логов, отключённый из-за открытия порта. Сейчас считает и дренирует события, но не превращает их в сессии. |

Оставьте `otlp_http` отключённым для начальной интеграции. Управляемый хук предоставляет полный
контур сессий; включение OTLP в этой версии не заменяет его установку.

Из CLI сохраните учётные данные вне истории shell и ссылайтесь на них по имени:

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name codex/enterprise \
  --value-file /run/secrets/codex-enterprise-token \
  --actor platform-operator \
  --reason codex-enterprise-onboarding

olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-enterprise \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --config api_key=store:codex/enterprise \
  --config auth_mode=access_token \
  --config workspace_id=ws_eng \
  --actor platform-operator \
  --reason codex-enterprise-onboarding
```

Если включаете `costs=true`, также добавьте `project_id=<project-id>`. Без этого ограничения API
Costs охватывает организацию и может смешать расходы, не относящиеся к Codex.

### 2. Системные требования и управляемые значения

Olivares разделяет два уровня:

- `requirements.toml` содержит ограничения, которые пользователи не могут расширить: политики
  утверждения, режимы sandbox, веб-поиск, удалённое управление, доверие к хукам, запрещённое чтение
  и разрешённые серверы MCP.
- `managed_config.toml` содержит управляемые начальные значения. Это значения по умолчанию; любое
  неизменяемое ограничение должно находиться в `requirements.toml`.

Следующий документ политики корректен и по умолчанию запрещает доступ к сети, веб-поиск,
удалённое управление и MCP, одновременно ограничивая запись workspace:

```json
{
  "requirements": {
    "allowed_approval_policies": ["untrusted", "on-request"],
    "allowed_sandbox_modes": ["read-only", "workspace-write"],
    "allowed_web_search_modes": [],
    "allow_remote_control": false,
    "allow_managed_hooks_only": true,
    "deny_read": ["~/.ssh"],
    "allowed_mcp_servers": []
  },
  "managed_config": {
    "approval_policy": "on-request",
    "sandbox_mode": "workspace-write",
    "web_search": "disabled",
    "network_access": false
  }
}
```

Проверьте политику до распространения, затем создайте оба артефакта одной командой:

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate

olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --requirements-out /etc/codex/requirements.toml \
  --managed-config-out /etc/codex/managed_config.toml
```

Рендеринг завершается ошибкой до записи, если политика содержит неизвестный enum, сервер MCP без
идентичности или неверный TOML. Для последующей проверки активного состояния и дрейфа
зарегистрируйте дополнительный источник типа `codex-managed-config`; он читает оба системных файла,
не изменяя их.

### 3. Хук сессии и PEP

Codex читает проверенный хук из `$CODEX_HOME/hooks.json`. `command` должен быть строкой, а не
массивом: массив может разобратьcя, хотя хук никогда не запустится. Inline-таблица `[hooks]` в
`config.toml` также не читалась измеренной версией.

```json
{
  "description": "olivares governed hooks",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PostToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ]
  }
}
```

Сервер монтируется при запуске Olivares, когда `OLIVARES_CODEX_HOOK_PEP_CONFIG` указывает на
корректный JSON:

```json
{
  "listen": "127.0.0.1:8448",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

Каждый экземпляр управляет одним тенантом, а решение приходит от уже настроенного в Olivares PDP.
Клиент использует `OLIVARES_CODEX_HOOK_URL`, `OLIVARES_CODEX_HOOK_TOKEN`,
`OLIVARES_CODEX_HOOK_TENANT`, `OLIVARES_CODEX_HOOK_AGENT`, `OLIVARES_CODEX_HOOK_ORG` и
`OLIVARES_CODEX_HOOK_ACCOUNT`. Передавайте эти значения через процесс и менеджер секретов; не
встраивайте их в `hooks.json`.

`allow_managed_hooks_only=true` необходим, прежде чем представлять хук как контроль парка. Без
принудительного доверия Codex может пропустить хук без события или предупреждения; молчаливая
установка не является свидетельством enforcement.

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="Требуется усиленная аутентификация — AAL3 (аппаратная, устойчивая к фишингу)">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="Требуется усиленная аутентификация — AAL3 (аппаратная, устойчивая к фишингу)">

## Использование CLI

Примеры вывода измерены 30 августа 2026 года. Общие логи запуска опущены, чтобы остались только
ответы команд.

### Воспроизводимая офлайн-регистрация

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "codex-demo" (kind "codex", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → codex
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 300
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

С SQLite выполняйте изменения реестра офлайн при остановленном движке; с PostgreSQL они могут
выполняться параллельно. Для изменений SQLite на ходу рекомендуется консоль.

### Проверка соединения и её пределы

Воспроизводимое измерение на хосте снимков экрана 30 августа 2026 года дало такой результат:

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "codex-demo" (codex): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

Процесс завершился с кодом `0`. На хосте была сессия Codex CLI, аутентифицированная через ChatGPT,
но у `codex-demo` не было `api_key`: результат доказывает только наличие офлайн-каталога и то, что
`Open` принял конфигурацию. Он не доказывает аутентификацию OpenAI, не вызывает `Gather` и не
читает ни одной строки Analytics или Compliance. Даже с учётными данными `sources test` не делает
upstream-запрос, поскольку `Open` только создаёт клиенты. Первый тест данных — реальный опрос
движка и последующие видимые наблюдения.

### Проверка управляемой политики

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate
```

```text
ok: policy renders to valid Codex managed-config TOML
```

### Тест локального отказа хука

При намеренно отсутствующем эндпоинте:

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_use_id":"exec-1"}' | olivares codex-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed endpoint not configured (deny-closed)"}}
```

Процесс завершается с кодом `0`, поскольку отказ передаётся в JSON, который интерпретирует Codex.
Эта проба проверяет fail-closed клиент; приём Codex события `PreToolUse` также нужно проверить на
хосте, где хук отмечен как доверенный.

## Control console

| Расположение | Что показано | Условие отображения |
|---|---|---|
| **Control console > Connectors** (`/console`) | Источник, режим, частота, несекретная конфигурация и действия Test/Save/Reload. | Сохранённый источник появляется сразу, его данные — нет. |
| **Health > Connectors** (`/health`) | Состояние коннектора, сообщение, тренд и последняя активность. | После перезагрузки реестра. |
| **Observability > Ingestion** (`/observability`) | Счётчики для `olivares.codex`, типы наблюдений и первое/последнее получение. | После выдачи данных из `Gather`. Эти общепроцессные счётчики начинаются при загрузке и сбрасываются при перезапуске. |
| **Cost & FinOps** (`/finops`) | Расчётное использование Analytics и, при включении, ежедневная выставленная стоимость. | Действительные учётные данные, `workspace_id` и авторизованные API; `costs` требует явного opt-in. |
| **Security** (`/security`) | Находки внедрения, недоступные enterprise-поверхности и структурный opt-in-анализ данных Compliance. | После сбора; ответы 403/404 от enterprise-поверхностей становятся свидетельствами позы, а не успехом. |
| **Sessions** (`/sessions`) | Сессии и временная шкала с действием, моделью, идентичностью, стоимостью и позой. | Поступает из управляемого хука. Один пакетный источник не создаёт живую сессию. |
| **Audit** (`/audit`) | Импортированные свидетельства активности и решения PEP, закреплённые в журнале. | После получения атрибутируемых логов или решений. |

Не считайте офлайн-каталог доказательством удалённого инвентаря в панели моделей. Коннектор
предоставляет каталог runtime, но в этом дереве нет потребителя модуля, публикующего его на этом экране.

<img class="light:sl-hidden" src="/console/health-dark.png" alt="Доступность, надёжность и зависимости по всему ИТ-ландшафту — выводятся из зафиксированной активности и проверки на устаревание, без зондирования инфраструктуры.">
<img class="dark:sl-hidden" src="/console/health-light.png" alt="Доступность, надёжность и зависимости по всему ИТ-ландшафту — выводятся из зафиксированной активности и проверки на устаревание, без зондирования инфраструктуры.">
<img class="light:sl-hidden" src="/console/finops-dark.png" alt="Стоимость токенов по всему ИТ-ландшафту — тренды, перераспределение затрат, сверка, бюджеты и прогноз. Цифры приведены ровно так, как их сообщает реестр FinOps.">
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="Стоимость токенов по всему ИТ-ландшафту — тренды, перераспределение затрат, сверка, бюджеты и прогноз. Цифры приведены ровно так, как их сообщает реестр FinOps.">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="Находки ограничителей, состояние принуждения, очередь аномалий и форензика инцидентов с защитой от подделки. По умолчанию плоскость работает в детективном режиме — она фиксирует, но сама по себе ничего не блокирует, пока принуждение не включено и не поставлено под управление.">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Находки ограничителей, состояние принуждения, очередь аномалий и форензика инцидентов с защитой от подделки. По умолчанию плоскость работает в детективном режиме — она фиксирует, но сама по себе ничего не блокирует, пока принуждение не включено и не поставлено под управление.">

## Использование в production

- **Пилот без учётных данных:** проверьте упаковку и реестр с `codex-demo`, но обозначьте его как
  офлайн-каталог. Не используйте его как индикатор enterprise-соединения.
- **Ingestion управления:** используйте автоматизированную идентичность только для чтения и
  минимальный набор API. Оставьте `attribute_email=false`, если нет утверждённой потребности в
  распределении затрат.
- **Контроль эндпоинтов:** создавайте TOML-файлы из версионированной политики, распространяйте их
  системой конфигурации парка и опрашивайте состояние через `codex-managed-config`, чтобы различать
  намерение, развёртывание и дрейф.
- **Контроль сессий:** сначала установите хуки в canary-группе. Подтвердите, что `PreToolUse`
  блокирует безвредное действие, прежде чем расширять кольцо. Хук без события нельзя считать
  управляемым.
- **Точный FinOps:** включайте `costs`, только когда `project_id` ограничивает данные расходами
  Codex. Используйте Analytics для внедрения, а Costs API — для выставленной суммы; не складывайте
  их, будто это два счёта.

## Что принудительно применяется, а что только наблюдается

| Поверхность | Фактическое поведение |
|---|---|
| Источник `codex` и enterprise API | **Наблюдаются, только для чтения.** Не меняют конфигурацию OpenAI и не перехватывают inference. |
| Режим без `api_key` | **Офлайн-каталог.** Не доказывает подписку ChatGPT, удалённый API или workspace. |
| `requirements.toml` | **Принудительно применяет системные ограничения**, которые пользователи не могут расширить, включая исключительное доверие управляемым хукам. |
| `managed_config.toml` | **Задаёт управляемые значения по умолчанию.** Не заменяет ограничение в `requirements.toml`. |
| `codex-managed-config` | **Наблюдает и сравнивает дрейф.** Никогда не исправляет файлы на хосте. |
| `olivares codex-hook` для `PreToolUse` или `PermissionRequest` | **Может предотвратить действие.** Codex не принимает `permissionDecision=allow`; Olivares представляет allow как невмешательство, а запрос `ask` преобразует в отказ. |
| `PostToolUse` и события жизненного цикла | **Свидетельства с неравными возможностями.** Поздняя блокировка не отменяет выполненный инструмент, а у `SessionEnd` нет выхода veto. |
| Приёмник OTLP Codex | **Частичный приём в этой версии.** Считает и дренирует события, но пока не преобразует их в сессии или находки. |

Завершение накопительное: источник должен быть перезагружен, первый `Gather` должен вернуть
enterprise-данные, системная политика должна быть проверена, доверенный хук должен наблюдаться,
а `PreToolUse` — доказуемо запрещён. `ANSWERED` охватывает только первую часть `Open`.
