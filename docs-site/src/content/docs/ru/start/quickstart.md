---
title: Быстрый старт
description: >-
  От нуля до заполненного графа доступа на чтение/запись с реальным результатом
  расхождения «Разрешено против Наблюдаемого» примерно за пять минут — сначала на
  встроенном демонстрационном estate, затем на реальном коннекторе pgAudit, чтобы
  доказать, что это не демо.
---

Это быстрый путь к тому, чтобы увидеть, *для чего* нужен Olivares AI: **карта доступа
на чтение/запись** вашего estate и **расхождение «Разрешено против Наблюдаемого»**
поверх неё — разрыв между доступом, который агенту *предоставлен*, и доступом, который
он, как *наблюдается*, использует.

Вы достигнете этого результата дважды, в сумме примерно за пять минут:

1. **За одну минуту, на встроенном демонстрационном estate** — мгновенный заход «а как
   это вообще выглядит» (синтетические наблюдения, проходящие через реальный движок).
2. **Затем на реальном коннекторе** — тот же граф и расхождение, на этот раз разобранные
   дословно из журнала PostgreSQL **pgAudit**, чтобы доказать, что главная возможность
   работает на подлинных данных, а не на демо.

Каждая команда ниже выполняется в точности так, как написано, скриптом
`scripts/quickstart-smoke.sh`
([воспроизводимость](#5-воспроизведите-это-сами)) — поэтому эта страница не может тихо
разойтись с бинарником.

Это обучающий путь, а не продакшен-развёртывание. Для реальной установки (без учётных
данных по умолчанию, с одноразовым setup-токеном, с TLS) перейдите к разделу
[self-hosting](/ru/how-to/self-hosting/). Для пошагового знакомства с UI см.
[туториал «от нуля до графа»](/ru/tutorials/zero-to-graph/).

:::caution[Демо-режим только для обучения]
`--seed-demo` создаёт демонстрационного администратора с **публичным паролем из
дерева исходников** и синтетическими данными, и он **отказывается запускаться на
не-loopback адресе**. Никогда не используйте его для реальной установки — подлинный
путь первого запуска описан в шаге 3 ниже и в
[self-hosting](/ru/how-to/self-hosting/).
:::

## 1. Соберите единый бинарник

Из чекаута репозитория (нужны Go 1.26+, [Task](https://taskfile.dev) и
pnpm — `task build` собирает веб-UI перед компиляцией; хранилище — это чистый
Go-SQLite, поэтому C-тулчейн не нужен):

```bash
task build                      # compiles ./bin/olivares with the web UI embedded
./bin/olivares version
```

`task build` создаёт один самодостаточный артефакт в `./bin/olivares` — движок,
встроенный веб-UI и первичные плагины-коннекторы. **Установки в контейнере и в
Kubernetes оборачивают этот же бинарник**: опубликованный образ плюс Compose-файл
([self-hosting](/ru/how-to/self-hosting/)), либо плоский манифест, который вы применяете
через `kubectl apply -f deploy/manifests/install.yaml` (Helm не требуется). Главная
возможность, которую вы видите ниже, идентична во всех трёх случаях — различается лишь
демонстрационный seed (только loopback, никогда в реальной установке).

## 2. Запустите демонстрационный estate (только loopback)

```bash
DATA="$(mktemp -d)"
./bin/olivares serve --insecure --seed-demo \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$DATA"
```

`--insecure` обслуживает незашифрованный HTTP на loopback (нормально для локального
демо; **TLS включён по умолчанию** в остальных случаях). Вы увидите честные строки
`WARN` для тех точек, которые по умолчанию закрыты по принципу deny (нет judge, нет
embedder, нет шлюза согласований, нет реальных источников), затем баннер **DEMO MODE**
с учётными данными:

```text
demo@olivares.local / olivares-demo-estate
```

Синтетический estate проходит через **реальную** шину событий ровно так же, как это
делал бы живой коллектор pgAudit или OpenTelemetry — синтетическими являются только
наблюдения.

## 3. Достигните графа доступа и его расхождения (главная возможность)

Оставьте сервер запущенным; во втором терминале войдите в систему, разрешите
демонстрационный tenant и получите граф и его расхождение:

```bash
BASE=http://127.0.0.1:8901
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"

# The read/write access map — module III:
curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool

# The Permitted-vs-Observed drift:
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

Демонстрационный estate возвращает ровно **20 узлов и 13 рёбер**, а расхождение
выявляет **8 неожиданных доступов** и **2 неиспользуемых грант**. Каждое ребро несёт
оси честности продукта, поэтому вы можете прочитать каждый вывод без догадок:

- **`mode`** — `read` / `write` / `readwrite` / `unknown`: классификация R/W, взятая
  дословно из сигнала, никогда не выведенная.
- **`attribution_tier`** — `firm` / `approximate` / `unknown`: насколько твёрдо доступ
  привязан к *конкретной* идентичности агента или рабочей нагрузки. В демо **6 рёбер
  firm, а 7 approximate** — например, агент, читающий ресурс, который ему никогда не
  предоставляли (`appdb.public.secrets`, *firm*), против идентичности из общего пула,
  пишущей логи (`appdb.public.logs`, честно *approximate*).
- **`coverage_tier`** — `clean` / `lossy` / `opaque` / `mixed`: точность сигнала
  *ресурса*, ортогональная атрибуции.

:::tip[Ключевая дифференцирующая возможность]
**Разница между Разрешённым и Наблюдаемым** — это *расхождение наименьших привилегий
(least-privilege drift)* — то, что вы хотите найти раньше, чем это сделает аудитор или
злоумышленник. Seed доказывает, что оно реально, а не «всё есть расхождение»: 3
предоставленных **и** наблюдаемых ребра сверяются и выпадают из результата расхождения;
остаются лишь подлинные разрывы (8 неожиданных доступов + 2 гранта, которые объявлены,
но никогда не используются). И продукт никогда не фабрикует метку, которую не может
доказать — атрибуция, которая всего лишь `approximate`, так и говорит, вместо того
чтобы выдумывать `firm`-агента.
:::

Тот же граф отображается во встроенном веб-UI по адресу `http://127.0.0.1:8901`
(войдите с демонстрационными учётными данными и переключитесь на организацию
**Demo Estate**).

Остановите демо-сервер (`Ctrl-C`) перед следующим шагом.

## 4. Докажите это на реальном коннекторе (не на демо)

Главная возможность — не засеянная магия: она работает на том, что наблюдают ваши
источники. Здесь вы подключаете **реальный коннектор pgAudit** — тот же путь кода, что
использует продакшен-установка — к журналу аудита PostgreSQL, **без демонстрационного
seed**.

Сначала небольшой csvlog `pgAudit` (три реальные строки аудита: два чтения и одна
запись от одного приложения). В продакшене pgAudit пишет их в журнал Postgres; здесь
файл заменяет этот «хвост»:

```bash
WORK="$(mktemp -d)"
python3 - "$WORK/postgresql.csv" <<'PY'
import csv, sys
def row(ts, user, db, msg, app):
    r = [''] * 26
    r[0], r[1], r[2] = ts, user, db
    r[11] = 'LOG'; r[13] = msg; r[22] = app; r[23] = 'client backend'
    return r
rows = [
    row("2026-06-09 09:00:01.001 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers", "billing-agent"),
    row("2026-06-09 09:00:02.002 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders", "billing-agent"),
    row("2026-06-09 09:00:03.003 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,3,1,READ,SELECT,TABLE,public.secrets", "billing-agent"),
]
with open(sys.argv[1], 'w', newline='') as f:
    csv.writer(f).writerows(rows)
PY
```

Теперь выполните **реальный первый запуск**: запуститесь один раз без учётных данных по
умолчанию, заберите одноразовый setup-токен и создайте tenant, к которому привязать
коннектор.

```bash
BASE=http://127.0.0.1:8901
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$WORK/data" > "$WORK/server.log" 2>&1 &
SERVER=$!
sleep 2

# The one-time setup token is printed to stdout on first boot (look for `olst_…` on the
# server's console, or read it from the redirected log):
SETUP="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/server.log" | head -1)"

curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}"

TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
echo "tenant: $TENANT"

kill "$SERVER"                  # stop the first-run server; we restart it with pgAudit wired
```

Коннекторы подключаются из одного файла конфигурации оператора, по значению, и никогда
не сохраняются движком. Направьте pgAudit на журнал для вашего tenant и **перезапустите**
с этой конфигурацией:

```bash
cat > "$WORK/sources.json" <<JSON
{"sources":[{"name":"salesdb-pgaudit","kind":"pgaudit","tenant":"$TENANT",
  "config":{"log_path":"$WORK/postgresql.csv","format":"csvlog"}}]}
JSON

OLIVARES_SOURCES_CONFIG="$WORK/sources.json" ./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$WORK/data"
```

Журнал запуска печатает `ingest: wired source … kind=pgaudit`. Во втором терминале
войдите снова и прочитайте граф — на этот раз рёбра **подлинно разобраны**, а не
засеяны:

```bash
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

Вы получаете **3 ребра** — `salesdb.public.customers` (read), `…orders` (write),
`…secrets` (read) — каждое с `signal_source: pg_audit` и `coverage_tier: clean`
(pgAudit сообщает R/W дословно), и расхождение помечает все **3 как неожиданные
доступы** (грант ещё не подключён, поэтому каждый наблюдаемый доступ — расхождение).

:::note[Честно по умолчанию: approximate, пока вы не подключите идентичность]
Эти реальные рёбра попадают как `attribution_tier: approximate`, а не `firm` — сигнал
pgAudit называет роль/приложение базы данных, а не *управляемого агента*. Это и есть
честный дефолт: продукт не будет утверждать, что твёрдо атрибутировал доступ агенту,
которого не может доказать. Вы зарабатываете `firm`, подключив источник идентичности
(LDAP/IdP/SPIFFE), который связывает учётные данные с идентичностью агента или рабочей
нагрузки — см. [подключение источника](/ru/how-to/connect-a-source/). Демонстрационный
estate показывает `firm`-рёбра именно потому, что он заранее связывает своих агентов.
:::

:::note[Форма эндпоинта]
Результат «Разрешено против Наблюдаемого» обслуживается по адресу
`/v1/m/accessmap/drift` (никакого `/diff` нет). Маршруты `/v1/m/accessmap/*` не
входят в стабильный основной контракт из 53 путей; они опубликованы отдельным
**beta**-документом — [справочником маршрутов модулей](/reference/api-beta/).
[Справочник API](/reference/api/) документирует стабильную основную поверхность.
:::

## 5. Воспроизведите это сами

Всё вышеперечисленное проверяется, от начала до конца, против реального бинарника:

```bash
task smoke:quickstart          # or: scripts/quickstart-smoke.sh
```

Он запускает демонстрационный estate **и** реальный путь pgAudit, выполняет точные
команды с этой страницы и проверяет числа (20 узлов / 13 рёбер, 8 неожиданных + 2
неиспользуемых, 3 реальных ребра pgAudit). Если путь «установка→ценность» или результат
расхождения когда-либо перестанут быть истинными, smoke-тест упадёт — это и есть
контракт, который удерживает эту страницу честной. Он завершается за несколько секунд
по настенным часам; путь, пройденный человеком выше, — это задокументированные **пять
минут**.

## Дальнейшие шаги

- **Запустите по-настоящему:** туториалы getting-started проходят каждый сценарий
  установки от начала до конца —
  [один узел (systemd)](/ru/tutorials/getting-started/single-node/),
  [Docker Compose](/ru/tutorials/getting-started/docker-compose/),
  [Kubernetes/Helm](/ru/tutorials/getting-started/kubernetes/) и
  [air-gapped](/ru/tutorials/getting-started/air-gapped/);
  [self-hosting](/ru/how-to/self-hosting/) — это страница принятия решений по всем ним.
- **Подайте ему реальные сигналы:** [подключите источник](/ru/how-to/connect-a-source/) и
  [каталог коннекторов](/ru/reference/connectors/) — что наблюдает каждый источник, его
  честный уровень покрытия и как подключить идентичность, чтобы атрибуция стала `firm`.
- **Укрепите его:** [укрепление безопасности](/ru/how-to/security-hardening/) — безопасные
  значения по умолчанию, согласования human-in-the-loop и проверка релиза перед запуском.
- **Знайте границы:** [Честность и ограничения](/ru/start/honesty-and-limits/) — что
  работает сегодня, что находится на стадии проектирования и чего продукт намеренно не
  делает.
