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

## Безопасные настройки по умолчанию (все пути)

| По умолчанию | Поведение |
|---|---|
| **Учётные данные** | отсутствуют. При первой загрузке выводится **одноразовый токен установки** (`olst_…`); с его помощью вы создаёте первого администратора. |
| **TLS** | включён по умолчанию. `--insecure` (открытый текст) предназначен только для локальной разработки на localhost. |
| **Привязка (bind)** | бинарный файл по умолчанию привязывается к **loopback**; открывайте его наружу осознанно. |
| **Лицензия** | В открытом (AGPL) бинарнике лицензия проверяется **офлайн** (Ed25519) и служит только для аттестации: она никогда не блокирует и не ухудшает открытый продукт, и это не изменится. Коммерческие надстройки — это право на оплаченный срок, предоставляемое как **доступ по подписке к репозиториям enterprise-редакции** (модель SUSE/Novell): чтобы получить надстройки и получать их обновления — включая обновления безопасности, — необходимо это право. Изолированные (air-gapped) среды обслуживаются так же, как в SUSE: через локальное зеркало, на которое по-прежнему распространяется это право. |
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
browser will warn once; that is expected. The token is shown ONCE and is
single-use. Prefer the API? POST /v1/setup {"token":"…","email":"…",
"password":"…"} — add "organization":"…" to name it (default: "Default
Organization"). The reply carries the new organization's tenant_id.
========================
```

Создайте первого администратора, затем войдите в систему:

```bash
curl -fsS -X POST https://localhost:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'

curl -fsS -X POST https://localhost:8443/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"<strong-password>"}'
```

Каталог данных содержит базу данных SQLite, ключ подписи аудита и материал TLS —
делайте его резервную копию и защищайте его.

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

Подписанный Helm-чарт развёртывает control plane как **core StatefulSet**
(единственный писатель; его каталог данных содержит ключ подписи аудита и материал TLS)
и, для распределённой топологии, **DaemonSet коллекторов**, которые отправляют
наблюдения в ядро по **gRPC + mTLS**. При релизе чарт публикуется в OCI-реестр и
подписывается cosign, так что вы проверяете его при установке и закрепляете по дайджесту.
(Первый релиз пока остаётся **черновиком**: пока не вырезан тег `chart-v*`, путь реестра
пуст, поэтому команда ниже — это путь, который вы будете использовать, как только релиз
будет опубликован.)

```bash
helm install olivares \
  oci://ghcr.io/olivaresai/charts/olivares \
  --version <chart-version> \
  --set image.repository=docker.io/olivaresai/olivares \
  --set image.digest=<sha256-digest>
```

> Опубликованный чарт **подписан cosign поверх OCI-манифеста**, а не GPG: пайплайн релиза не
> выпускает слой `.prov`, поэтому `helm --verify` его не проверит. Проверяйте через `cosign verify`
> против identity `release-chart.yml@refs/tags/chart-v*` — см. `deploy/helm/README.md`.

Чарт получает образ контейнера из Docker Hub (`docker.io/olivaresai/olivares`); тот же образ
также находится в `ghcr.io/olivaresai/olivares`, идентичный по дайджесту; направьте
`image.repository` туда, если мешает лимит **анонимных** пулов Docker Hub (ghcr.io не
применяет его к публичным образам). Сам артефакт
**чарта** остаётся в `oci://ghcr.io/olivaresai/charts/olivares`.

Всегда развёртывайте **по дайджесту**, никогда по изменяемому тегу. Для полностью
отключённого от сети кластера сначала зеркалируйте бандл — см.
[установку в изолированной среде](/how-to/air-gap-install/).

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
