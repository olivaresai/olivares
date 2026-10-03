---
title: "Ваш первый час с Olivares AI"
description: >-
  Установите плоскость управления, откройте консоль, подключите одного
  кодирующего агента, выполните управляемую сессию, которая разрешает одно
  действие и запрещает другое, и прочитайте доказательства. Три формы:
  локальная (SQLite), командная (Postgres и Docker), гибридная.
---

Эта страница — первый час **в этом дереве**. Это не макет. Каждая команда
локальной формы — это команда, которую `task smoke:first-hour` выполняет
против `./bin/olivares`. Если форме нужны Docker или Postgres, страница это
говорит.

Продукт печатает следующий шаг. `olivares quickstart` называет регистрацию
passkey, `olivares agent tool detect`, инвентарь, hook PEP и
`olivares doctor` после токена установки. `olivares doctor` сообщает
`first-hour-coding-agent`, `first-hour-hook-pep` и `first-hour-next-step`.
Эти проверки необязательны. Они не делают исправную установку неуспешной.

## Что это за час

Установка → консоль → подключить **одного** кодирующего агента → увидеть в
инвентаре → управляемая сессия, которая **разрешает одно действие и запрещает
другое** → прочитать доказательства.

Эта страница использует **PEP хуков Claude Code**, который уже есть в этом
дереве (`olivares claude-hook`). Запуск официального CLI как процесса сессии
— шов рантайма Community. Этот час не дублирует тот драйвер. Он не отправляет
ход модели.

`--seed-demo` — не этот час.

## Форма 1 — Локальная (SQLite, этот контейнер)

В этом контейнере **нет Docker**. PostgreSQL **не запущен**. Локальная форма
использует встроенный SQLite и loopback HTTP. Именно эту форму повторяет
дымовой тест.

### 1. Собрать и запустить

```bash
task build
./bin/olivares version
DATA="$(mktemp -d)"
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --data-dir "$DATA"
```

Дымовой тест использует `serve --insecure` и явно передаёт
`--listen 127.0.0.1:8443`: эта привязка к loopback принадлежит дымовому тесту,
а не настройкам продукта по умолчанию. Интерактивный оператор выполняет
`olivares quickstart` (TLS включён, нет учётных данных по умолчанию,
одноразовый токен настройки). Его адрес прослушивания по умолчанию — `:8443`,
то есть **все интерфейсы**, потому что это сервер
(`cmd/olivares/binddefaults.go`); чтобы ограничить его этой машиной, передайте
`--listen 127.0.0.1:8443`. Измерено на этой машине 2026-09-17:
`quickstart --quiet` напечатал токен за **3 с**.

Панель приветствия печатает:

```text
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

Для привязки-джокера по умолчанию баннер печатает `https://localhost:8443` и
перечисляет под токеном все остальные адреса, на которых отвечает этот хост, —
они нужны, чтобы открыть консоль с другой машины. Если баннер уже прокрутился,
`olivares first-boot` снова печатает адрес(а) консоли и состояние первой
настройки. Откройте `https://localhost:PORT` до регистрации passkey: браузер
отклоняет IP-адрес как WebAuthn relying party, и продукт уже говорит об этом в
подсказке об адресе (`cmd/olivares/consoleaddr.go`).

### 2. Создать администратора и тенанта

Одна команда завершает настройку на работающем движке — та самая, которую печатает
стартовая панель. Токен читается со стандартного ввода, поэтому он никогда не попадает
в таблицу процессов:

```bash
# вставьте токен olst_…, который движок напечатал при запуске
./bin/olivares auth bootstrap --server http://127.0.0.1:8443 \
  --setup-token-file - \
  --email admin@local --password-file ./admin.pw \
  --organization "First hour" --save-context
```

`--save-context` выполняет вход и сохраняет сессию, поэтому следующая команда уже
аутентифицирована. Измерено 2026-09-18 на чистом каталоге данных: 263 мс.

То же самое через API, если вы обращаетесь к эндпоинтам напрямую:

```bash
BASE=http://127.0.0.1:8443
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d '{"token":"olst_…","email":"admin@local","password":"correct-horse-battery-staple"}'
TOKEN=$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
TENANT=$(curl -sf -X POST "$BASE/v1/system/orgs" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"First hour","slug":"first-hour"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')
```

### 3. Подключить кодирующего агента и увидеть его в инвентаре

```bash
./bin/olivares agent tool detect -o json
curl -sf -X POST "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"claude-code-local","kind":"claude-code"}'
curl -sf "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares agent managed-settings --out ./managed-settings.json
```

`POST /v1/agents` не требует дополнительной административной проверки. Для
источников, коннекторов, рабочих пространств и секретов она определяется
`admin_step_up`; значение по умолчанию — `none`. Как потребовать AAL3,
описано ниже в разделе политики.

### 4. Управляемая сессия: разрешить Read, запретить Bash

Остановите движок из шага 1. Выполните следующие команды в том же рабочем
каталоге, сохранив `DATA` и `TENANT` из шагов 1 и 2. Это создаёт политику с
отказом по умолчанию и перезапускает движок с подключённым PEP:

```bash
# TENANT is the tenant_id returned in step 2.
: "${TENANT:?Set TENANT to the tenant_id from step 2}"
cat > ./hook-pep.json <<JSON
{
  "listen": "127.0.0.1:8447",
  "tenants": [
    {
      "tenant": "$TENANT",
      "require_firm_identity": false,
      "policy": {
        "version": "first-hour/v1",
        "default": "deny",
        "rules": [
          { "tool": "Read", "decision": "allow", "reason": "reads are permitted in the first hour" },
          { "tool": "Bash", "decision": "deny", "reason": "shell execution is blocked in the first hour" }
        ]
      }
    }
  ]
}
JSON
OLIVARES_HOOK_PEP_CONFIG=./hook-pep.json \
  ./bin/olivares serve --insecure --data-dir "$DATA" \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444
```

```bash
export OLIVARES_HOOK_PEP_URL=http://127.0.0.1:8447/
export OLIVARES_HOOK_PEP_TOKEN="$TOKEN"
export OLIVARES_HOOK_PEP_TENANT="$TENANT"
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' \
  | ./bin/olivares claude-hook
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}' \
  | ./bin/olivares claude-hook
```

### 5. Прочитать доказательства

```bash
curl -sf "$BASE/v1/audit?action=hook.tool.allow&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
curl -sf "$BASE/v1/audit?action=hook.tool.deny&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares doctor --mode user
task smoke:first-hour
```

## Форма 2 — Команда (Postgres и Docker)

В этом контейнере **нет Docker**. PostgreSQL **здесь не запущен**. Не считайте
команды ниже измеренными на этой машине.

```bash
cp deploy/compose/.env.example deploy/compose/.env
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up --wait --wait-timeout 120
```

Без пула `olivares_admin` `POST /v1/setup` отвечает
`501 cross_tenant_admin_pool_not_configured`. См.
[Развёртывание с Docker](/how-to/docker-deployment/).

## Форма 3 — Гибрид (локальная плоскость, агент на рабочей станции)

Оставьте плоскость управления на локальном SQLite. Запустите агента на
станции, где уже есть `claude`. Живая сессия официального CLI (PTY) — это
дело рантайма Community, не этой страницы.

## Политика дополнительной административной проверки

Значение `admin_step_up` по умолчанию — `none`: вошедший администратор
работает с текущим уровнем своей сессии. Поэтому создание источников,
коннекторов, рабочих пространств и секретов по умолчанию не требует AAL3.
Аутентификация, разрешения, изоляция tenants и аудит продолжают действовать;
API-токены не удовлетворяют этой проверке.

Чтобы потребовать свежую AAL3 для этих действий, зарегистрируйте passkey на
`https://localhost:PORT` и выполните свежую проверку passkey/PIV по этому
адресу консоли. В **Settings → Sign-in → Extra check for administrative
actions** выберите **Passkey**. Эквивалент через API —
`PUT /v1/auth/step-up-policy` с `{"admin_step_up":"passkey"}`. Движок
отклоняет усиление политики, пока администратор не докажет, что выбранный
фактор работает. `POST /v1/agents` остаётся вне этой дополнительной проверки.


## 3. Запуск сессии Claude Code из консоли

Без источника учётных данных вывода запуски stream-json deny-closed:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go`). Задайте **один** из
`OLIVARES_SESSION_RUNTIME_WIF` или `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`.
С выпуска 26.10 это больше не единственный путь: зарегистрируйте учётные данные в консоли
и привяжите их к профилю. См.
[Добавить провайдера и запустить агента](/ru/how-to/add-a-provider/) и
[Эксплуатация сессии провайдера](/how-to/operate-provider-sessions/).

## Связанные страницы

- [Честность и пределы](/start/honesty-and-limits/)
- [Самостоятельный хостинг Olivares AI](/how-to/self-hosting/)
- [Эксплуатация сессии провайдера](/how-to/operate-provider-sessions/)
- [PEP хуков Claude Code](/how-to/connectors/claude-code-hooks-pep/)
- [Развёртывание с Docker](/how-to/docker-deployment/)
