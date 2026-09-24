---
title: API среды выполнения сеанса (официальные CLI)
description: >-
  HTTP-поверхность Community: список, подключение, ввод и останов собственных
  процессов Claude Code, Codex и Grok CLI. Права, PTY, возобновление и повторное подключение.
---

Плоскость управления **запускает CLI поставщика**. Она не заменяет Claude Code,
Codex или Grok Build. Сеансы и терминалы — один модуль этого продукта, а не
продукт.

Эта страница описывает маршруты Community operate под `/v1/m/sessions/runs`.
Они уже есть в [бета-документе OpenAPI](/reference/api-beta/). Выпуск v26.10, который ещё не вышел, добавит
контракт драйвера, локальный PTY-runner и маршруты J01–J08 как тесты.

## Граница редакции

| Редакция | Что делает | Чего не делает |
|---|---|---|
| **Community (эта страница)** | Собственный локальный потомок: запуск, stdin/stdout/stderr, attach с курсором, возобновление точного разговора, повторное подключение живого потока, останов с наблюдаемым кодом выхода. Строки сеанса и доказательства остаются в модуле II. | Движок Identity & Scale с несколькими панелями, mTLS-слушатель, коммерческие сеансы ввода, chunk xterm |
| **Оверлей Identity & Scale** | Коммерческий движок session-cockpit (слушатель, панели, журнал). Маршруты под `/v1/m/session-cockpit/`, когда надстройка присутствует. | Не заменяет `/v1/m/sessions/runs` |

Сборка Community отвечает на пространство имён оверлея **отсутствием** (404).
Она не монтирует заглушку 501.

Путь hook Claude Code остаётся `olivares claude-hook` (PEP PreToolUse). Это
наблюдение и принуждение, не замена CLI.

## Права

Существующее принуждение на маршрутах модуля:

| Право | Маршруты |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`, `DELETE /runs/{ref}` |

Viewer может читать список. Создание, ввод и останов требуют write. Authorizer
маршрута — точка принуждения; отсутствующее право даёт 403.

## Маршруты, которые читает консоль

База: `/v1/m/sessions`. Аутентифицируйтесь. Отправьте `X-Olivares-Tenant`.

| Метод | Путь | Результат |
|---|---|---|
| `GET` | `/runs` | Страница управляемых запусков |
| `GET` | `/runs/{ref}` | Один запуск. `state` выводится. `exit_code` наблюдается. Нет выдуманного поля успеха |
| `GET` | `/runs/{ref}/events` | Строки доказательств жизненного цикла |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE: кадры `output`, `lag` если кольцо вытеснило ниже курсора, `end` или `notice` «не жив» |
| `POST` | `/runs/{ref}/input` | stdin. Stream-json использует `line`/`message`. Codex/Grok — `text`. 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | SIGTERM, затем SIGKILL группы. Наблюдаемый код выхода в строке |
| `POST` | `/runs/{ref}/resume` | Новое поколение процесса. Точный сохранённый разговор |
| `POST` | `/runs/{ref}/interrupt` | Отменяет активный ход. Процесс остаётся |

Повторное подключение после оборванного attach — `GET …/attach?from={last+1}` к
**тому же** живому процессу. После потери процесса attach сообщает, что сеанс
не жив. Resume начинает новое поколение. Reconnect не выдумывает процесс-замену
(SDD R04).

## Транспорт (Community)

Каждая официальная CLI запускается на том транспорте, который нужен её
собственной форме operate; какой именно — объявляет
`cliruntime.LaunchTransport`. Сегодня все три формы — протоколы поверх stdio,
поэтому корень композиции подключает `sessions.NewProcRunner()`: stdin, stdout
и stderr — каналы и остаются раздельными потоками.

**Claude Code отклоняет терминал на stdin.** Форма `--print` со stream-json
отвечает `Error: Input must be provided either through stdin or as a prompt
argument when using --print` и выходит с 1, не выдав ни одного кадра протокола.
Терминал нужен интерактивной форме; `sessions.NewPTYRunner()` остаётся для неё
доступен в Linux. Изоляция container и sandbox отклоняется в обоих случаях.

Контракт драйвера живёт в `modules/sessions/cliruntime`. Виды: `claude`,
`codex`, `grok`. Соответствие всегда гоняется против внутрипроцессного fake и
локального PTY-пира. Если `claude` / `codex` / `grok` есть в PATH, отдельный
тест владеет настоящим двоичным файлом, останавливает его и записывает выход.
Он не отправляет ход модели.

## Связанное

- [Эксплуатация сеанса поставщика](/how-to/operate-provider-sessions/)
- [Модуль II — живая эксплуатация](/reference/modules/ii-sessions/)
- [Подключить Claude Code](/how-to/connect-claude-code/)
- [Бета OpenAPI](/reference/api-beta/)
