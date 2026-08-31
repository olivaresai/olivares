---
title: Управление Claude Code и Codex с подписочной аутентификацией
description: >-
  Как Olivares AI управляет кодинг-агентами, которые аутентифицируются через
  подписку — Claude Code на Pro/Max, Codex в ChatGPT, — никогда не вставая в
  середину этой подписки. Три механизма (наблюдение, managed-settings + хуки,
  шлюз на API-ключе), одна красная черта: мы никогда не маршрутизируем учётные
  данные вашей подписки.
sidebar:
  order: 6
---

Сложнее всего управлять тем агентом, в который разработчик вошёл со своей личной
или корпоративной **подпиской**: Claude Code, авторизованный через Pro/Max, или
Codex, авторизованный через ChatGPT. Та же схема применима к Grok Build и к любому
CLI-агенту, который аутентифицирует человека, а не рабочую нагрузку: описанные ниже
механизмы относятся к *форме* такого входа, а не к конкретному поставщику. Он работает
на ноутбуке, аутентифицируется
по учётным данным OAuth, и это ровно та поверхность, которую ограждение
облачного провайдера в пути инференса никогда не видит (см.
[клин (wedge)](/ru/explanation/positioning/where-olivares-fits-vs-your-gateway/)).
Соблазнительное «решение» — поставить перед ним сервис, который держит подписку и
маршрутизирует её трафик, — Olivares AI строить **не будет**, потому что
провайдеры моделей это запрещают и потому что это превратило бы нашу плоскость
управления (control plane) в единую точку компрометации учётных данных.

Эта страница — честный рассказ о том, как мы управляем этими агентами, **никогда
не выступая брокером подписки**: что мы наблюдаем, где принуждаем и единственный
узкий путь, где шлюз уместен (и это никогда не подписка).

:::danger[Красная черта: мы никогда не маршрутизируем вашу подписку]
Olivares AI **никогда не держит, не проксирует и не маршрутизирует учётные данные
сторонней подписки.** Собственная политика Anthropic гласит: *"Anthropic does not
permit third-party developers to offer Claude.ai login or to route requests
through Free, Pro, or Max plan credentials on behalf of their users"*
([Claude Code legal & compliance](https://code.claude.com/docs/en/legal-and-compliance),
получено 2026-06-21 — запрет называет три потребительских плана **Free, Pro,
Max**). Условия OpenAI работают точно так же для потребительского входа в
ChatGPT/Codex. Наша позиция строже самой черты: мы не маршрутизируем **никакой**
подписочный OAuth **любого** плана. Управление происходит *вокруг* агента,
никогда *внутри* его учётных данных.
:::

## Почему брокерство подписки исключено

Стоит быть точным насчёт правила, потому что юрист покупателя его проверит.
Политика Anthropic проводит два списка, которые нельзя смешивать:

- **Кто вообще может пользоваться OAuth** — пять планов: *"OAuth authentication is
  intended exclusively for purchasers of Claude Free, Pro, Max, Team, and
  Enterprise subscription plans and is designed to support ordinary use of Claude
  Code and other native Anthropic applications."*
- **Что сторонняя сторона делать не вправе** — маршрутизировать от имени
  пользователей: *"Anthropic does not permit third-party developers to offer
  Claude.ai login or to route requests through Free, Pro, or Max plan credentials
  on behalf of their users."*

Запрет явно называет **потребительские** планы (Free, Pro, Max). При этом
страница, наоборот, никому не даёт разрешения маршрутизировать места Team или
Enterprise — об этом она молчит, а молчание мы не читаем как лицензию. Для
*разработчиков, создающих инструменты*, собственное руководство Anthropic уводит
от подписочного OAuth полностью: *"Developers building products or services that
interact with Claude's capabilities, including those using the Agent SDK, should
use API key authentication through Claude Console or a supported cloud provider."*
([источник](https://code.claude.com/docs/en/legal-and-compliance); разбивка планов
по условиям: Team/Enterprise/API под Commercial Terms, Free/Pro/Max под Consumer
Terms.)

Наш коннектор Codex кодирует ту же дисциплину прямо в коде, по замыслу: учётные
данные для автоматизации — это **API-ключ** OpenAI или **токен доступа рабочего
пространства (workspace access token)**, никогда не личная подписка ChatGPT —
*"proxying it for third-party/programmatic use violates OpenAI's terms exactly as
a consumer Claude subscription does for Anthropic. There is no subscription config
field by design"* (`connectors/codex/codex.go`). Так что красная черта — это не
маркетинговое обещание, прикрученное задним числом; это форма самого продукта.

## Три механизма, и ни один из них — не подписка

Мы управляем агентом с подписочной аутентификацией через три независимых канала.
Первые два вообще никогда не касаются инференса; третий касается его только для
трафика, который аутентифицируется по **API-ключу**, никогда — по подписке.

### 1. Наблюдение — телеметрия, использование и позиция (posture)

Claude Code испускает OpenTelemetry, и администратор может включить его для всего
парка с управляемого уровня (managed tier): *"Administrators can configure
OpenTelemetry settings for all users through the managed settings file"*
([Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)). Мы
поглощаем этот **сигнал gen-ai** — сессии, токены, стоимость, активность
инструментов — и превращаем его в карту доступа и находки о позиции. Что важно,
это **по построению минимальные данные и на стороне самого Claude Code**:
содержимое промптов *"redacted by default"*, а детали инструментов, содержимое
инструментов и сырые тела API-ответов — каждое из них *"(default: disabled)"* (тот
же источник). Мы потребляем использование и метаданные, а не разговоры.

Для Codex тот же канал наблюдения — это поглощение коннектором API Analytics и
Compliance/Audit — использование, внедрение (adoption) и неизменяемые записи
аудита, превращённые в выборки стоимости (cost samples) и доказательства с обнаружением
подделки, несущие *"never prompt/diff content or key values"*
(`connectors/codex/codex.go`).

→ [Поглощение OpenTelemetry GenAI](/ru/how-to/connectors/otel-genai/) ·
[Корпоративный OTel для Claude Code](/ru/how-to/claude-code-enterprise-otel/)

### 2. Managed settings + хуки — внутрипроцессный PEP

Наблюдение — это не принуждение. Канал принуждения для Claude Code — это его файл
**managed settings** на уровне политики ОС, который несёт непереопределяемый хук
`PreToolUse`, обращающийся к точке принятия решений Olivares перед запуском каждого
инструмента. Anthropic документирует свойство, на которое мы опираемся:
*"Environment variables defined in the managed settings file have high precedence
and cannot be overridden by users"*, а managed settings *"can be distributed via
MDM"* ([monitoring](https://code.claude.com/docs/en/monitoring-usage)).

Olivares рендерит этот файл (`olivares agent managed-settings`) с
`allowManagedHooksOnly`, так что собственный хук разработчика никогда не может
предшествовать управляемому или подорвать его, а посессионная конечная точка и
bearer внедряются при запуске — не вписываются в статический файл. Само решение
**deny-closed на каждом краю**: вызов инструмента разрешается только тогда, когда
разрешается прочная идентичность, диспозиция политики не равна `deny`, живой
движок политик его не запрещает и — для `ask` — одобрение человеком привязано к
точному хешу плана. Аварийная остановка
([аварийный выключатель / kill switch](/ru/reference/glossary/#kill-switch-аварийный-выключатель))
перебивает всё, включая активный грант break-glass.

Это тот механизм, который страница
[PEP хуков Claude Code](/ru/how-to/connectors/claude-code-hooks-pep/) документирует
операционно, и именно он даёт нам возможность *управлять* локальным dev-агентом, а
не просто наблюдать за ним, — второе из
[трёх направлений](/ru/explanation/positioning/analyst-vocabulary/#три-направления-на-которые-указывает-этот-словарь).

### 3. Шлюз для API-ключа — никогда для OAuth

Есть ровно один путь, где Olivares встаёт в линию запроса инференса, и он
существует только для вызывающих, которые **не** используют канал managed-settings
у Claude Code: сырой трафик SDK или `curl`, аутентифицированный по **API-ключу**
(или эквиваленту Bedrock/Vertex). Claude Code маршрутизирует такие запросы через
`ANTHROPIC_BASE_URL` — *"To route requests through a custom API endpoint, set the
`ANTHROPIC_BASE_URL` environment variable instead"* — и аутентифицирует шлюз
посредством bearer через `ANTHROPIC_AUTH_TOKEN`, *"when routing through an LLM
gateway or proxy that authenticates with bearer tokens rather than Anthropic API
keys"* ([Claude Code IAM](https://code.claude.com/docs/en/iam)). Направленный на
встроенный inference-прокси Olivares, этот трафик получает управляемый конвейер —
резидентность, доступ к моделям, контекстное окно, DLP, бюджет, запись — прежде
чем он будет переслан дальше.

Граница абсолютна: **этот путь несёт трафик API-ключа / bearer, никогда — учётные
данные OAuth подписки.** Это шов принуждения для вызывающих SDK/`curl`, до которых
managed settings дотянуться не может, и ничего более.

## Коробка честности: верифицированно-развёрнуто, не неотменяемо

:::caution[Принуждение, которое мы можем доказать как *развёрнутое*, а не принуждение, которое *нельзя* обойти]
PEP на managed-settings + хуке **deny-closed** и **непереопределяем
пользователем через настройки** — но это не магия. Разработчик, который направит
`ANTHROPIC_BASE_URL` на собственную конечную точку, отправит инференс совсем в
другое место; наша собственная инженерная заметка говорит это прямо: *"a custom
`ANTHROPIC_BASE_URL` bypasses server-managed-settings entirely"*
(`modules/inferenceproxy/doc.go`). Поэтому мы никогда не утверждаем, что PEP
невозможно обойти. Вместо этого мы утверждаем две вещи, за которые можем
поручиться:

1. **Он верифицированно-развёрнут.** Olivares аттестует, что managed settings и
   хук PEP действительно присутствуют на хосте — неподготовленный (un-provisioned)
   хост работает неуправляемым-но-наблюдаемым, и это видно, а не скрыто.
2. **Обход сам по себе является находкой.** Нестандартный `ANTHROPIC_BASE_URL` на
   хосте всплывает как находка о позиции, а управляемая среда, закрепляющая
   base URL, расходящийся с авторизованным шлюзом Olivares, поднимает находку о
   **дрейфе** (`connectors/claude-config`, `connectors/managedsettings`). Уклонение
   не уходит в тишину; оно загорается.
:::

«Верифицированно-развёрнуто, уклонение-как-находка» — честная история принуждения
для любого агента, работающего на машине, которую контролирует разработчик. Мы не
будем продавать вам «неотменяемое».

## Асимметрия Codex, изложенная честно

Claude Code и Codex несимметричны, и эта разница важна. Для Codex,
аутентифицированного через ChatGPT, **нет документированного эквивалента
`ANTHROPIC_BASE_URL`** — страница
[managed-configuration](https://developers.openai.com/codex/enterprise/managed-configuration)
OpenAI не документирует ни настройки, ни переменной окружения для маршрутизации
инференса через кастомный base URL или шлюз (проверено получением, 2026-06-21;
отсутствие на этой странице, а не доказательство, что её нет где-либо ещё). Поэтому
мы **не** управляем Codex путём перехвата его инференса.

Вместо этого мы управляем им там, где OpenAI *действительно* даёт администраторам
принуждаемые средства контроля. Управляемая конфигурация Codex позволяет
предприятию задать *"Requirements: admin-enforced constraints that users can't
override"*, которые *"constrain security-sensitive settings (approval policy,
approvers reviewer, automatic review policy, sandbox mode, permission profiles,
web search mode, managed hooks, and optionally which MCP servers users can
enable)"* (тот же источник). Olivares составляет и аттестует эти требования
(`connectors/codex-managed-config`) — политику одобрений, режим песочницы,
allowlist MCP, телеметрию с маскированием (`log_user_prompt = false`) — и
поглощает доказательства Analytics и Compliance у Codex. Управление через
конфигурацию и доказательства, а не через «человека посередине» (man-in-the-middle)
на вызове модели.

## В одной таблице

| Канал | Что он делает | Касается инференса? | Учётные данные |
|---|---|---|---|
| **Наблюдение** | Использование, стоимость, активность инструментов → карта доступа + позиция; Codex Analytics/Compliance → журнал | Нет | Нет — только телеметрия, содержимое маскируется по умолчанию |
| **Managed settings + хуки** | Deny-closed PEP `PreToolUse` на Claude Code, непереопределяемый через настройки | Нет | Собственные у агента; мы их никогда не видим |
| **Шлюз (только API-ключ)** | Управляемый конвейер для сырых вызывающих SDK/`curl` через `ANTHROPIC_BASE_URL` | Да | **API-ключ / bearer — никогда OAuth подписки** |
| **Codex managed-config** | Требования, принуждаемые администратором (approval/sandbox/MCP) + поглощение доказательств | Нет | Организации; конфигурация, а не перехват |

## Связанное

- [Где находится Olivares относительно вашего шлюза / Guardrails](/ru/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  — почему ничто из этого не конкурирует с вашим ИИ-шлюзом.
- [Olivares AI и WitnessAI](/ru/explanation/positioning/vs-witnessai/) — лобовое
  сравнение по управлению агентами в IDE.
- [Хуки Claude Code и PEP](/ru/how-to/connectors/claude-code-hooks-pep/) и
  [Запуск Claude Code с Olivares](/ru/how-to/run-claude-code-with-olivares/) —
  операционное how-to.
- [Честность и ограничения](/ru/start/honesty-and-limits/) — постоянное
  обязательство, под которым написана эта страница.
