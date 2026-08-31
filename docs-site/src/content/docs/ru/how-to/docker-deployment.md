---
title: Развёртывание с Docker
description: >-
  Загрузите и проверьте образ из Docker Hub, затем запустите control plane в
  продакшене с Docker — усиленный однонодовый SQLite, мультиарендный Postgres,
  запланированные DR-бэкапы, терминация TLS на обратном прокси, обновления и
  закрепление по digest.
---

Это руководство для инженеров и SRE, выводящих control plane Olivares AI в
продакшен с Docker. Весь продукт — это один distroless-образ — движок со
встроенным веб-интерфейсом — поэтому один хост может запускать топологию SQLite
без внешних зависимостей, а переопределение для Postgres даёт мультиарендную
топологию, когда она вам нужна. Каждый путь сохраняет одни и те же безопасные
значения по умолчанию: никаких учётных данных по умолчанию, одноразовый токен
настройки, TLS, включённый по умолчанию, и порт хоста, привязанный к loopback.

:::note[Бета — релиз ещё не нарезан]
Olivares AI находится в **бете**. Координаты образа ниже разрешаются только
**после выхода первого релиза (CalVer `26.8.0`)**; до тех пор реестрам нечего
отдавать. Воспринимайте это как форму развёртывания, которую вы будете
использовать, а не как гарантию готовности к продакшену.
:::

Обзор всех вариантов развёртывания и их значений по умолчанию на странице
решений см. в разделе [Самостоятельный хостинг control plane](/ru/how-to/self-hosting/).
Для отключённых площадок см. [Установку в air-gapped окружении](/ru/how-to/air-gap-install/);
для масштабирования вширь см. путь Kubernetes/Helm ниже.

## 1. Загрузите и проверьте образ

Основная загрузка контейнера — **Docker Hub**:

```bash
docker pull docker.io/olivaresai/olivares:26.8.0
```

То же содержимое также публикуется в `ghcr.io/olivaresai/olivares` — идентичное
по digest, используется как резерв и как реестр сборки. Docker Hub ограничивает частоту
**анонимных** пулов; ghcr.io не ограничивает анонимные пулы публичных образов — поэтому
`docker login` или координата ghcr.io и есть выход, если узел CI или большой парк упирается
в лимит. Теги несут **без
ведущего `v`**: `:26.8.0` закрепляет релиз, `:latest` плавает, а
`:26.8.0-fips` / `:26.8.0-stig` — усиленные варианты. Базовый тег и `:latest`
мультиархитектурные (`linux/amd64`, `linux/arm64`); `fips`/`stig` — только
`amd64`.

Control plane — это продукт безопасности, поэтому проверяйте перед запуском.
Подпись **бесключевая** (Sigstore) против идентичности GitHub Actions проекта и
работает одинаково с любым из реестров — подписи и аттестации копируются в
Docker Hub через `cosign copy`, поэтому digest тот же:

```bash
IMAGE=docker.io/olivaresai/olivares          # fallback: ghcr.io/olivaresai/olivares (same digest)
DIGEST="$(crane digest "$IMAGE:26.8.0")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Полная цепочка — подпись контрольных сумм, SBOM, OpenVEX, происхождение SLSA —
в разделе [Проверьте то, что вы загрузили](/ru/how-to/verify-a-release/). После
проверки развёртывайте по **digest**, который вы проверили, а не по
изменяемому тегу (см. [§8](#8-закрепление-по-digest-для-продакшена)).

## 2. Один узел, SQLite

### Через `docker run` (усиленный)

Команда образа по умолчанию привязывается к `0.0.0.0` **внутри контейнера**,
чтобы вы могли поставить перед ним ingress; сопоставление портов на стороне
хоста ниже закрепляет экспозицию к loopback. Запускайте его не от root, в
режиме read-only, со сброшенными всеми capabilities:

```bash
docker volume create olivares-data

docker run -d --name olivares \
  --user 65532:65532 \
  --read-only \
  --tmpfs /tmp \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v olivares-data:/var/lib/olivares \
  -p 127.0.0.1:8443:8443 \
  -p 127.0.0.1:8444:8444 \
  docker.io/olivaresai/olivares:26.8.0 \
  serve \
    --listen=0.0.0.0:8443 \
    --grpc-listen=0.0.0.0:8444 \
    --data-dir=/var/lib/olivares \
    --checkpoint-interval=1h
```

| Флаг | Зачем |
|---|---|
| `--user 65532:65532` | запуск под не-root UID `nonroot`, встроенным в distroless-образ |
| `--read-only` | корневая файловая система неизменяема; записываемы только том данных и `/tmp` |
| `--tmpfs /tmp` | записываемый временный tmpfs, требуемый из-за того, что rootfs только для чтения |
| `--cap-drop ALL` | движку не нужны Linux-capabilities |
| `--security-opt no-new-privileges` | блокировка повышения привилегий через setuid-бинарники |
| `-v olivares-data:/var/lib/olivares` | сохранение каталога данных (см. [§5](#5-операционные-заметки)) |
| `-p 127.0.0.1:8443:8443` | публикация HTTPS (REST + веб-интерфейс) **только на loopback** |
| `-p 127.0.0.1:8444:8444` | публикация gRPC (приём / API ControlPlane) только на loopback |

Прочитайте одноразовый токен настройки из логов и создайте первого
администратора:

```bash
docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

curl -fsS -k -X POST https://127.0.0.1:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'
```

`-k` принимает самоподписанный сертификат, который движок чеканит при первой
загрузке; замените его настоящим сертификатом через обратный прокси
([§6](#6-обратный-прокси--терминация-tls)) или собственным TLS-материалом. Токен
показывается **один раз** и одноразовый.

### Через Docker Compose

В репозитории поставляется стек Compose, который связывает том, сопоставление
портов на loopback и те же флаги усиления, что и выше:

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

Базовый файл по умолчанию задаёт образ `docker.io/olivaresai/olivares:latest` (Docker
Hub); для проверяемого продакшен-развёртывания задайте `OLIVARES_IMAGE` в
`deploy/compose/.env` на ссылку, закреплённую по digest (см.
[§8](#8-закрепление-по-digest-для-продакшена)). Данные сохраняются в томе
`olivares-data`.

## 3. Мультиарендный Postgres

Для мультиарендной топологии наложите переопределение Postgres поверх базового
файла. Сначала задайте два пароля, затем поднимите стек:

```bash
cp deploy/compose/.env.example deploy/compose/.env   # set POSTGRES_SUPERUSER_PASSWORD + OLIVARES_DB_PASSWORD
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

Переопределение поднимает `postgres:16-alpine`, при первой инициализации
провижинит **least-privilege** роль `olivares_app` и базу данных `olivares`
(запуская канонический `deploy/postgres/01-app-role.sql` через
`initdb/10-app-role.sh`) и направляет движок на эту не-суперпользовательскую
роль через `--engine=postgres`. Это делает реальным арендный заслон FORCE-RLS:
движок **отказывается стартовать** против роли суперпользователя/`BYPASSRLS`.

:::caution[`sslmode=disable` только для демо внутри сети]
DSN в переопределении использует `sslmode=disable`, потому что оба контейнера
разделяют одну Docker-сеть. **В продакшене используется TLS с
`sslmode=verify-full`.** Для усиленного развёртывания предпочитайте Helm-чарт с
DSN в Secret и управляемым (или собственным) Postgres — см.
[§8](#8-закрепление-по-digest-для-продакшена).
:::

## 4. Бэкапы для аварийного восстановления

Профиль бэкапа создаёт запланированные, безопасные для непрерывности журнала
DR-бандлы: снимок хранилища плюс ключи подписи, зашифрованные под вашим KEK, с
манифестом верхушек цепочек по каждому арендатору. Запишите свою кодовую фразу в
файл, хранимый **вне репозитория и образа**, затем запустите одноразовый профиль
`backup`:

```bash
printf 'a strong DR passphrase' > deploy/compose/dr-pass
# the host stamps the bundle name (the distroless image has no `date`):
BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               --profile backup run --rm backup
```

Задание разделяет том данных движка, пишет бандл в том `olivares-backups` и —
поскольку образ distroless — оставляет хранение хосту: подчищайте старые бандлы
через cron на хосте (`find <backups> -name '*.drbundle' -mtime +14 -delete`).
Оберните запуск в cron на хосте для запланированного RPO и **зеркальте том
`olivares-backups` за пределы площадки** — бэкап на том же хосте не является
аварийным восстановлением. Восстановите и проверьте через:

```bash
olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file dr-pass
```

Полная процедура RPO/RTO, ответственного хранения ключей и DR-учений живёт
вместе с DR-руководством репозитория; обзор более высокого уровня — в разделе
[Резервное копирование и восстановление](/ru/how-to/backup-and-restore/).

## 5. Операционные заметки

**Зондируйте здоровье с хоста, а не из контейнера.** Образ **distroless** — в
нём нет ни оболочки, ни `curl`, поэтому в контейнере намеренно нет
`HEALTHCHECK`. Движок выставляет `/livez` и `/readyz` на HTTPS-порту; зондируйте
их с хоста (или вашего оркестратора):

```bash
# liveness — process is up; no dependency checks, so a store outage never restart-loops:
curl -fsS -k https://127.0.0.1:8443/livez

# readiness — store ping (and HA leadership): 200 when serving, 503 when the store is down:
curl -fsS -k https://127.0.0.1:8443/readyz
```

Достижимость `/readyz` — это сигнал доступности; подключите его к внешнему
мониторингу (см. [Мониторинг с Prometheus](/ru/how-to/monitor-with-prometheus/)).

**Токен настройки появляется только один раз, в логах.** При первой загрузке в
вывод контейнера печатается одноразовый токен `olst_…`. Захватите его через
`docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'` (или эквивалент Compose) до
того, как буфер прокрутится; он расходуется при создании первого
администратора.

**Делайте бэкап каталога данных.** `/var/lib/olivares` (том `olivares-data`)
хранит **хранилище SQLite, ключ подписи аудита и TLS-материал**. Его потеря
теряет подписывающую идентичность журнала и нарушает непрерывность аудита,
поэтому защищайте и бэкапьте том — используйте DR-профиль из
[§4](#4-бэкапы-для-аварийного-восстановления), а не разовую копию живого
хранилища.

## 6. Обратный прокси / терминация TLS

Из коробки движок отдаёт собственный **самоподписанный** сертификат, что годится
для оценки, но не для клиентов, которые проверяют доверие. В продакшене
поставьте перед движком, привязанным к loopback, обратный прокси, который
терминирует TLS сертификатом, предоставленным оператором (из вашего CA или
ACME), и пусть прокси будет единственным, что выставлено в сеть.

Поскольку сам движок говорит на TLS, прокси подключается к нему по HTTPS на порту
loopback. Минимальный серверный блок nginx:

```nginx
server {
  listen 443 ssl;
  server_name olivares.example.com;

  ssl_certificate     /etc/ssl/olivares/fullchain.pem;   # operator-provided cert
  ssl_certificate_key /etc/ssl/olivares/privkey.pem;

  location / {
    proxy_pass         https://127.0.0.1:8443;   # engine's own TLS on loopback
    proxy_ssl_verify   off;                       # engine cert is self-signed
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
  }
}
```

Эквивалент на Caddy, который провижинит публичный сертификат автоматически:

```caddy
olivares.example.com {
  reverse_proxy https://127.0.0.1:8443 {
    transport http {
      tls_insecure_skip_verify   # engine cert is self-signed on loopback
    }
  }
}
```

Держите порты хоста движка привязанными к `127.0.0.1` (значения по умолчанию
выше), чтобы достижим был только прокси. Порт приёма gRPC (`8444`) предназначен
для коллекторов; выставляйте его осознанно, с собственным TLS-путём, только если
вы запускаете распределённую топологию.

## 7. Обновления

Том данных сохраняется при замене контейнеров, поэтому обновление таково:
сделать бэкап, загрузить новый закреплённый тег, пересоздать контейнер.

```bash
# 1. Back up first (see §4).
# 2. Pull the new release and re-verify it (see §1):
docker pull docker.io/olivaresai/olivares:26.8.1

# docker run:
docker stop olivares && docker rm olivares
# re-run the §2 command with the new tag — the olivares-data volume is reused.

# Compose: set OLIVARES_IMAGE to the new digest in .env, then:
docker compose -f deploy/compose/docker-compose.yml up -d
```

Пересоздание контейнера не затрагивает именованный том, поэтому хранилище, ключ
подписи и TLS-материал переносятся. Всегда **делайте бэкап перед обновлением** и
переверифицируйте новый образ перед пересозданием.

## 8. Закрепление по digest для продакшена

Изменяемые теги (`:26.8.0`, `:latest`) — для оценки. В продакшене закрепляйте
**digest**, который вы проверили — digest неизменяем и есть ровно то, что вы
утвердили:

```bash
docker run ... docker.io/olivaresai/olivares@sha256:<digest> serve ...
```

Для Compose задайте ссылку на digest в `deploy/compose/.env`:

```bash
OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
```

Для масштабирования вширь и мультинодовых конфигураций используйте Helm-чарт —
публикуемый как OCI-артефакт по адресу
`oci://ghcr.io/olivaresai/charts/olivares`, подписанный cosign и закреплённый по
digest образа. См. [Самостоятельный хостинг control plane](/ru/how-to/self-hosting/)
для команды чарта и [Установку в air-gapped окружении](/ru/how-to/air-gap-install/)
для полностью отключённых площадок.
