---
title: Самостоятельный хостинг Olivares AI
description: >-
  Запускайте Olivares AI самостоятельно — единый бинарный файл, Docker Compose
  или Kubernetes — с безопасными настройками по умолчанию: без учётных данных по
  умолчанию, одноразовый токен установки и TLS включён по умолчанию, без обязательной
  телеметрии и исходящего трафика управляющей плоскости по умолчанию. За ваш периметр
  выходит только то, что вы настроили для передачи наружу, — от обращений к API ваших
  моделей до подключённых вами выходов SIEM/webhook.
---

> Пакеты развёртывания предоставляются через канал Business; их публикация здесь не подтверждена. Перед использованием локального чарта проверьте пакет и издателя по инструкции канала. Пример манифеста использует файл Business `business-install.yaml`. Изолированная установка требует Enterprise.


> Helm, Kubernetes operators, Terraform, appliance and FIPS/STIG images are Business deployment artifacts. The source paths below are in the Business distribution. Air-gapped installation requires Enterprise.

Следующий выпуск — <!-- release -->`0.1`<!-- /release -->; его релиз на GitHub ещё не опубликован. Команды ниже описывают планируемые артефакты. До публикации собирайте из исходников, а после публикации проверяйте каждый артефакт перед использованием. Наблюдаемый статус записан в <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release -->.

Olivares AI ориентирован на **самостоятельный хостинг в первую очередь**. Весь
продукт — это один статический бинарный файл со встроенным веб-интерфейсом, поэтому
самое простое развёртывание — это один файл; пути через Compose и Kubernetes
существуют для многоузловых и продакшен-сценариев. Все пути используют одни и те же
безопасные настройки по умолчанию — без учётных данных по умолчанию, одноразовый
токен установки, TLS включён по умолчанию —, без обязательной телеметрии и исходящего
трафика управляющей плоскости по умолчанию. За ваш периметр выходит только то, что **вы**
настроили для передачи наружу: обращения к API ваших моделей, подключённые вами выходы
SIEM/webhook и внешний поставщик эмбеддингов, если вы его настроили.

Это руководство представляет собой **страницу принятия решения** о развёртывании —
варианты и их безопасные настройки по умолчанию с первого взгляда. Пошаговую установку
для каждого сценария руководства по началу работы описывают от и до:
[один узел (systemd)](/tutorials/getting-started/single-node/) ·
[Docker Compose](/tutorials/getting-started/docker-compose/) ·
[Kubernetes/Helm](/tutorials/getting-started/kubernetes/) ·
[изолированная среда (air-gapped)](/tutorials/getting-started/air-gapped/). Чтобы
сначала криптографически проверить артефакты, см.
[Проверка того, что вы скачали](/how-to/verify-a-release/); для отключённых от сети
площадок см. [Установка в изолированной среде (air-gapped)](/how-to/air-gap-install/).

**Доставка событий NATS:** Мост Core NATS и NATS JetStream требуют Business Identity & Scale. Community доставляет события внутри процесса.

## Безопасные настройки по умолчанию (все пути)

| По умолчанию | Поведение |
|---|---|
| **Учётные данные** | отсутствуют. При первой загрузке выводится **одноразовый токен установки** (`olst_…`); с его помощью вы создаёте первого администратора. |
| **TLS** | включён по умолчанию. `--insecure` (открытый текст) предназначен только для локальной разработки на localhost. |
| **Привязка (bind)** | по умолчанию **все интерфейсы** (`:8443`, `:8444`) — это сервер. Чтобы ограничить его этим хостом, передайте `--listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444`. |
| **Лицензия** | В открытом (AGPL) бинарнике лицензия проверяется **офлайн** (Ed25519) и служит только для аттестации: она никогда не блокирует и не ухудшает открытый продукт, и это не изменится. Business включает Regulated Operations, AI Runtime Security, Compliance Packs и Identity & Scale в одну подписку. Клиенты могут включать или отключать каждое семейство. Business и Enterprise поставляются в виде бинарных файлов; исходный код платных редакций остаётся закрытым. |
| **Телеметрия-домой** | отключена. Движок не делает обязательных исходящих вызовов при загрузке. |

## Вариант 1 — единый бинарный файл

Соберите один статический артефакт (хранилище SQLite на чистом Go, поэтому
C-инструментарий не требуется) и запустите его:

```bash
task build                      # compiles ./bin/olivares with the web embedded
./bin/olivares serve \
  --listen 127.0.0.1:8443 \
  --grpc-listen 127.0.0.1:8444 \
  --data-dir /var/lib/olivares
```

При первой загрузке движок выводит баннер установки:

```text
=== FIRST-BOOT SETUP ===
No accounts exist yet. Open the console and create the first administrator
with this one-time token — setup also creates your first organization and
makes that administrator its owner:

  Console:  https://127.0.0.1:8443
  Token:    olst_…

The console serves HTTPS with a self-signed certificate on first boot — your
browser will warn once; that is expected.

Passkeys will not work at that address:
a browser will not run a passkey ceremony at an IP address. Reach the
console by a host name.
On this machine the same console also answers at
  https://localhost:8443
and at that address the relying party is derived from the name, which the
verifier accepts.

The token is shown ONCE and is
single-use. Prefer the API? POST /v1/setup {"token":"…","email":"…",
"password":"…"} — add "organization":"…" to name it (default: "Default
Organization"). The reply carries the new organization's tenant_id.
========================
```

Создайте первого администратора, затем войдите в систему:

```bash
curl --cacert /var/lib/olivares/tls.crt -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl --cacert /var/lib/olivares/tls.crt -fsS -X POST https://localhost:8443/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<strong-password>"}'
```

Каталог данных содержит базу данных SQLite, ключ подписи аудита и материал TLS —
делайте его резервную копию и защищайте его.

### Пользовательский каталог данных (`layout: custom`)

Раскладка native по умолчанию — `/var/lib/olivares`. Подписанный адаптер службы
(`install.sh --data-dir`, `scripts/install-service.sh`) допускает
**пользовательский** каталог данных по **форме**, а не по списку разрешённых.
Манифест владения записывает `"layout": "custom"` (`CHANGELOG.md` `[26.9.0]`
Added; `INSTALL.md`).

SDD 04 §6: каждое настраиваемое поле объявляет владельца, схему, допустимые
источники и валидатор. Здесь адаптер владеет выделенным каталогом; оператор
владеет родителем. Это собственные строки отказа адаптера
(`scripts/install-service.sh`):

| Condition | What the adapter prints and exits 1 |
|---|---|
| Path is not `/*/*` (a top-level directory) | `custom data directory must be a dedicated directory at least two levels deep (for example /srv/olivares), not a top-level directory: $data_dir` |
| The data directory would contain the binary, config or unit | `custom data directory $data_dir must not contain the installed path $path` |
| Any path component is a symbolic link | `path component is a symbolic link ($prefix -> …); pass the resolved path instead of provisioning through a link: $1` |
| Parent of a new custom directory does not exist | `parent of the custom data directory does not exist; create it with the intended owner first: $(dirname -- "$data_target")` |
| Existing system directory mode is not 0700 or 0750 | `existing system data directory mode is $data_mode; require 0700 or 0750` |
| Path is under `/dev`, `/proc` or `/sys` | `data directory $data_dir is under an API file system (/dev, /proc, /sys): choose a real directory` |

`install-agentops.sh` использует то же правило двух уровней для `OLIVARES_DATA_DIR`:
`OLIVARES_DATA_DIR must name a dedicated directory at least two levels deep
(for example /srv/olivares), not a top-level directory`. Он учитывает `OLIVARES_DATA_DIR` и явно выбранный
`OLIVARES_WORKSPACE_DIR`.

Юнит выводит каталог как `ReadWritePaths=<data-dir>`. Он задаёт `ProtectHome=false` и
`PrivateTmp=false`, поэтому каталогу под `/home`, `/root`, `/run/user`, `/tmp` или
`/var/tmp` не нужно дополнительного монтирования. Установщик предупреждает, что `/tmp` и
`/var/tmp` могут очищаться при загрузке или по таймеру.

`olivares uninstall` допускает этот пользовательский каталог только когда юнит
по индексированному пути запускает движок с ним, или когда preserve уже оставил
свидетель удаления рядом с конфигурацией службы. Диагностируйте записанную
раскладку AgentOps командой `olivares doctor` — см.
[Устранение неполадок](/how-to/troubleshooting/#agentops-layout-check).

Для пакетных установок значение по умолчанию остаётся `/var/lib/olivares`. См.
[Установка из пакета](/how-to/install-from-packages/). На macOS см.
[Установка через Homebrew](/how-to/install-from-homebrew/).

## Вариант 2 — Docker Compose (один узел, SQLite)

Репозиторий поставляется со стеком Compose:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token from the logs:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

Для многоарендного бэкенда Postgres задайте пароли и наложите оверрайд Postgres:

```bash
cp deploy/compose/.env.example deploy/compose/.env     # set the two passwords
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

:::note[Команда по умолчанию в контейнере привязывается внутри контейнера]
Команда по умолчанию контейнера привязывается к `0.0.0.0` *внутри контейнера*, чтобы
вы могли поставить перед ним свой ingress; стек Compose отображает порт хоста на
`127.0.0.1`. Нет голого рецепта `docker run` — используйте Compose (или Helm-чарт),
чтобы том данных, порты и поток первой загрузки были подключены корректно.
:::

## Вариант 3 — Kubernetes (Helm)

Пакеты развёртывания предоставляются через канал Business; их публикация здесь не подтверждена. Перед использованием локального чарта проверьте пакет и издателя по инструкции канала. Пример манифеста использует файл Business `business-install.yaml`. Изолированная установка требует Enterprise.

```bash
# Set these inputs from the authenticated Business channel after verification.
helm upgrade --install olivares "$BUSINESS_CHART_PACKAGE" \
  --set image.repository="$BUSINESS_IMAGE_REPOSITORY" \
  --set image.digest="$BUSINESS_IMAGE_DIGEST"
```

## Выбор топологии

| Топология | Когда | Хранилище | Шина событий |
|---|---|---|---|
| **Единый бинарный файл** | один узел, лаборатория, небольшой estate, изолированная среда | SQLite (встроенный) | внутрипроцессная |
| **Распределённая** | несколько хостов, масштабирование, многоарендность | Postgres + RLS | внутрипроцессная + **мост NATS** (`OLIVARES_BUS_CONFIG`; межузловая доставка честно реализована не более одного раза (at-most-once)) |
| **Изолированная (air-gapped)** | исходящий трафик запрещён | SQLite или Postgres | внутрипроцессная (мост NATS опционален внутри периметра) |

**Плоскость данных (коллекторы) всегда работает на вашей инфраструктуре** — control
plane — это единственное, для чего вы выбираете, где его размещать.
[Обзор архитектуры](/explanation/architecture/overview/) объясняет компромиссы.

## Подключение реальных источников

Свежая установка имеет пустой estate. Подключите реальные источники (Postgres pgAudit,
CloudTrail, OpenTelemetry от агентов, eBPF), чтобы access map заполнялась — см.
[подключение источника](/how-to/connect-a-source/) и
[подключение Claude Code](/how-to/connect-claude-code/). Для поверхности конфигурации
см. [справочник по конфигурации](/reference/configuration/).
