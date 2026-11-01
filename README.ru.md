<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**Языки:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · **Русский** · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Запускайте AI, которым уже пользуется ваша команда, с тем же контролем, что и над остальной инфраструктурой.**

[Что оно делает](#что-оно-делает) · [Установка](#установка) · [Редакции](#редакции-и-цены) · [Документация](#документация) · [Сообщество](#сообщество) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md) <!-- release -->
[![Next release: 0.5](https://img.shields.io/badge/release-0.5-28282B)](https://github.com/olivaresai/olivares/releases/tag/0.5)<!-- /release -->
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>


Ваши разработчики работают с Claude Code и Codex. Агенты обращаются к MCP-серверам, моделям и внутренним API, а задания по расписанию выполняются сами. У каждого компонента свои журналы и разрешения, поэтому даже на простые вопросы нет быстрого ответа: какой агент изменил этот файл, кто это одобрил, сколько нам стоил AI в этом месяце?

Olivares AI собирает ответы в одном месте. Он подключается к агентам и инструментам, которыми вы уже пользуетесь, показывает действия каждого, применяет ваши правила перед выполнением операции и сохраняет подписанную запись обо всём. Это одна программа на ваших собственных серверах. Полный продукт бесплатен и имеет открытый исходный код.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/console-cockpit-dark.webp">
  <img src=".github/assets/console-cockpit-light.webp" width="840" alt="Экран «Кокпит» консоли Olivares AI">
</picture>
<br><sub><b>Кокпит</b> — что сейчас делает каждый агент, какие одобрения ждут вас и лимиты использования каждой учётной записи.</sub>
<br><br>
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/console-session-dark.webp">
  <img src=".github/assets/console-session-light.webp" width="840" alt="Экран «Сессия» консоли Olivares AI">
</picture>
<br><sub><b>Сессия</b> — разговор рядом с планом, агентами, фоновыми задачами, изменёнными файлами и лимитами использования.</sub>
<br><br>
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/console-parallel-sessions-dark.webp">
  <img src=".github/assets/console-parallel-sessions-light.webp" width="840" alt="Экран «Параллельные сессии» консоли Olivares AI">
</picture>
<br><sub><b>Параллельные сессии</b> — шесть агентов работают рядом, каждый в своей ветке, с последним выводом, стоимостью и лимитом использования.</sub>
<br><br>
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/console-access-map-dark.webp">
  <img src=".github/assets/console-access-map-light.webp" width="840" alt="Экран «Карта доступа» консоли Olivares AI">
</picture>
<br><sub><b>Карта доступа</b> — что каждый агент читает и записывает, в сравнении с тем, что ему разрешено.</sub>
</div>

<div align="center">
<img src=".github/assets/console-catalog-light.png" width="840" alt="The Catalog page: approved agents, MCP servers, skills, connectors, templates and models, each with its version, status and signing.">
<br><sub><b>Каталог</b>: утверждённые агенты, MCP-серверы, навыки и коннекторы, у каждого статус и подпись.</sub>
<br><br>
<img src=".github/assets/console-work-light.png" width="840" alt="The Work page: shared work items with their type, owner and status.">
<br><sub><b>Работа</b>: задачи, ответственные и решения, которые остаются после завершения сессии.</sub>
</div>

## Что оно делает

- **Знать, что работает.** Все агенты, сессии, модели, MCP-серверы и инструменты в одном реестре. Карта доступа показывает, что каждый читает и записывает, и отмечает доступ, который не разрешён ни одним правилом.
- **Остановить действие до того, как оно причинит вред.** В Olivares AI есть **четыре закрытые по умолчанию (deny-closed) точки принуждения**, которые проверяют каждое действие перед выполнением: внутри Claude Code, в прокси моделей, при каждом вызове инструмента MCP и между агентами. Рискованное действие ждёт второго человека, запрещённое не выполняется. Один переключатель останавливает всех агентов сразу. Если проверка не может принять решение, действие не выполняется.
- **Контролировать расходы на AI.** Бюджеты для команды, агента или модели предупреждают, замедляют или останавливают расходы до того, как придёт счёт.
- **Безопасно открывать агентам знания компании.** Подключите Google Drive, Confluence, Notion, Snowflake, S3 и PostgreSQL; Business добавляет SharePoint, SAP и Salesforce. Каждый агент видит только то, к чему имеет доступ человек, который им пользуется.
- **Продолжать работу между сессиями.** Задачи, ответственные и решения сохраняются после завершения сессии. Запускайте, подключайтесь и останавливайте сессии Claude Code, Codex и Grok из браузера, без SSH.
- **Предоставлять доказательства по запросу.** Каждое решение попадает в подписанный журнал, в котором любое изменение задним числом можно обнаружить. Business Compliance Packs сопоставляет доказательства с 26 каталогами фреймворков и создаёт отчёты для команды безопасности и аудиторов. Community сохраняет доступ к сохранённым доказательствам и их экспорту в JSON/CSV.

Работает с вашими инструментами: Claude Code, Codex, Grok, Cursor, gemini-cli, opencode, OpenHands и локальными моделями через Ollama. **32 модуля** и **136 интеграций**: [все модули](docs-site/src/content/docs/reference/modules/overview.md) · [все коннекторы](connectors/README.md).

Community сохраняет локальную наблюдаемость, настройки и экспорт резервных копий. Отправка в SIEM/ITSM, внешняя телеметрия и экспорт состояния безопасности входят в базовую редакцию Business.

**Сетевая изоляция сессий распространяется на сессии Claude Code, Codex и OpenCode, привязанные к ключу провайдера, и на сессии OpenCode со входом через собственную учётную запись инструмента.** Такая сессия достигает только конечной точки своего провайдера или хостов своего входа через прокси отдельной сессии в собственном сетевом пространстве имён — иначе она не запускается. Для OpenCode с ключом провайдера список хостов опирается на более раннюю инвентаризацию, а не на ход, показанный за прокси. Сессии Grok Build и сессии других инструментов со входом в учётную запись по-прежнему могут достичь любого хоста, который разрешает сеть. Прочитайте раздел [сетевая изоляция сессий](SECURITY.md#session-network-isolation), прежде чем запускать нагрузки, которым нужен исключительный доступ к одному провайдеру.

## Установка

Выберите способ и скопируйте его блок. В конце `olivares quickstart` выведет адрес консоли и одноразовый токен для создания первого администратора. Каждый релиз подписан, а каждый способ установки проверяет скачанные файлы перед установкой ([проверить скачанный файл самостоятельно](INSTALL.md#verifying-a-release)).

**Linux и macOS, одна команда.** Определяет систему, проверяет релиз, устанавливает только бинарный файл и никогда не использует `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart
```

**Docker.** Мультиархитектурный. Образы контейнеров основаны на Debian 13 slim (с Node.js 24 для инструментов агентов) и запускаются от имени пользователя без прав root. Слушает на всех интерфейсах хоста; добавьте `127.0.0.1:` перед каждым `-p`, чтобы ограничить доступ локальной машиной.

<!-- release -->
```sh
docker pull docker.io/olivaresai/olivares:0.5 &&
img=$(docker image inspect --format '{{index .RepoDigests 0}}' docker.io/olivaresai/olivares:0.5) &&
cosign verify "$img" \
  --certificate-identity https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/0.5 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null &&
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  "$img" \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```
<!-- /release -->

**Docker Compose.** SQLite на одном узле, с необязательными Postgres и резервным копированием.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

Сессии работают в одной папке хоста: задайте `OLIVARES_PROJECT_DIR` с её абсолютным путём до `up`, и Compose смонтирует её в `/project`. На хостах, где политика AppArmor ограничивает пользовательские пространства имён (Ubuntu 24.04 и новее), сначала загрузите профиль сессий: см. [Docker Compose](INSTALL.md#docker-compose).

**Debian и Ubuntu.** Пакет добавляет пользователя `olivares` без возможности входа и службу с усиленной защитой; вы запускаете её сами.

<!-- release -->
```sh
curl -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/olivares_0.5_linux_amd64.deb" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.sig" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.pem"
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/0.5 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt &&
grep ' olivares_0.5_linux_amd64.deb$' checksums.txt | sha256sum -c &&
sudo dpkg -i olivares_0.5_linux_amd64.deb && sudo systemctl enable --now olivares
```
<!-- /release -->

**RHEL, Fedora и SUSE.**

<!-- release -->
```sh
curl -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/olivares_0.5_linux_amd64.rpm" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.sig" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.pem"
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/0.5 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt &&
grep ' olivares_0.5_linux_amd64.rpm$' checksums.txt | sha256sum -c &&
sudo rpm -i olivares_0.5_linux_amd64.rpm && sudo systemctl enable --now olivares
```
<!-- /release -->

**Alpine.**

<!-- release -->
```sh
curl -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/olivares_0.5_linux_amd64.apk" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.sig" \
  -fsSLO "https://github.com/olivaresai/olivares/releases/download/0.5/checksums.txt.pem"
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity https://github.com/olivaresai/olivares/.github/workflows/release.yml@refs/tags/0.5 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt &&
grep ' olivares_0.5_linux_amd64.apk$' checksums.txt | sha256sum -c &&
sudo apk add --allow-untrusted olivares_0.5_linux_amd64.apk && sudo rc-service olivares start
```
<!-- /release -->

На ARM-серверах используйте `arm64` вместо `amd64`. Все файлы релиза: <!-- release -->[страница релиза](https://github.com/olivaresai/olivares/releases/tag/0.5)<!-- /release -->.

**Homebrew.** macOS и Linux.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**Из исходного кода.** Go 1.26+, [Task](https://taskfile.dev) и pnpm.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
task build && ./bin/olivares quickstart
```

**Изолированные сети:** соберите подписанный образ, chart и материалы для проверки, затем [установите в изолированной среде](docs-site/src/content/docs/how-to/air-gap-install.md). Для **Windows** пока нет нативной сборки: используйте образ Docker или WSL2. Обновление и откат: [инструкция](docs-site/src/content/docs/how-to/upgrade-and-rollback.md). Все варианты подробно: [`INSTALL.md`](INSTALL.md).

**Сначала попробуйте с демоданными**, только на своей машине (пароль демо общедоступен):

```sh
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
```

Затем откройте http://127.0.0.1:8901.

Helm, оператор Kubernetes, Terraform, appliance и артефакты развёртывания FIPS/STIG поставляются с Business; установка без сети требует Enterprise. Развёртывание агентов и MCP-серверов на инфраструктуре компании (`olivares deploy`) — функция Business. См. [редакции](docs/editions.md).

## Редакции и цены

Community — полный продукт, бесплатный и с открытым исходным кодом. Business добавляет то, что компании нужно для работы в производственной среде. Enterprise предназначен для групп компаний с более крупной инфраструктурой или требованиями регуляторов.

| | **Community** | **Business** | **Enterprise** |
|---|---|---|---|
| **Цена** | Бесплатно, AGPL-3.0 | 99 USD в месяц или 990 USD в год | Годовой договор |
| **Что входит** | Полный продукт: неограниченное число пользователей и все четыре deny-closed точки принуждения | Всё из Community, а также Regulated Operations, AI Runtime Security, Compliance Packs и Identity & Scale, коммерческая лицензия, подписанные обновления и поддержка по электронной почте | Всё из Business, а также больше компаний, развёртываний и провайдеров идентификации, автономные зеркала и согласованные с вами условия поддержки |
| **Область использования** | Один активный провайдер идентификации | Одна компания, один активный экземпляр одновременно | Согласована в договоре |

**Regulated Operations** добавляет запреты на удаление, стирание данных с уничтожением ключа, нормативные минимальные сроки хранения, сверку запретов на удаление в архивах и WORM-архивы в Azure и GCS. **AI Runtime Security** добавляет более глубокую проверку того, что агенты отправляют, получают и выполняют. **Compliance Packs** готовит черновики реестра информации DORA и пакета ISO/IEC 42001 для вашего аудитора. **Identity & Scale** подключает несколько провайдеров идентификации одновременно и поддерживает более крупные развёртывания.

[olivares.ai/pricing](https://olivares.ai/pricing) · [Что входит в каждую редакцию](docs/editions.md) · [Что открытое, а что коммерческое](LICENSING.md)

## Архитектура

Один бинарный файл Go со встроенной консолью. Он предоставляет REST API, gRPC API, командную строку `olivares` и провайдер Terraform. Граничные коллекторы (Business) работают внутри вашей сети, а данные остаются в SQLite или PostgreSQL на ваших серверах. [Как всё устроено](ARCHITECTURE.md).

## Документация

На [docs.olivares.ai](https://docs.olivares.ai) есть инструкции по установке, руководство для каждого коннектора, рецепты типовых политик и справочник API. Начните с [Что такое Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). Что работает сегодня и что ещё запланировано: [Честность и ограничения](docs-site/src/content/docs/start/honesty-and-limits.md). Релизы: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

На сайте: [продукт](https://olivares.ai/product) · [решения](https://olivares.ai/solutions) · [как это работает](https://olivares.ai/how-it-works) · [архитектура](https://olivares.ai/architecture) · [безопасность](https://olivares.ai/security) · [доверие](https://olivares.ai/trust) · [сравнение](https://olivares.ai/compare) · [демо](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [статус](https://olivares.ai/status) · [дорожная карта](https://olivares.ai/roadmap) · [бренд](https://olivares.ai/brand) · [пресса](https://olivares.ai/press).

## Безопасность

Нашли уязвимость? Сообщите о ней конфиденциально по инструкции в [`SECURITY.md`](SECURITY.md). Olivares AI записывает, какой агент обращался к какому ресурсу, а не содержимое; просмотр этой записи тоже попадает в журнал. Лицензии проверяются автономно; ядро с открытым исходным кодом никогда не связывается с нами.

## Сообщество

Мы приветствуем вклад в проект. [`CONTRIBUTING.md`](CONTRIBUTING.md) описывает настройку среды, sign-off и устройство коннекторов. [Кодекс поведения](CODE_OF_CONDUCT.md) · [Поддержка](SUPPORT.md) · [Управление проектом](GOVERNANCE.md) · [История изменений](CHANGELOG.md).

## Поддержать проект

Olivares AI разрабатывается открыто. Если он вам помогает, поддержите разработку через GitHub Sponsors — [olivaresai](https://github.com/sponsors/olivaresai) или [fran-olivares](https://github.com/sponsors/fran-olivares) — или угостите нас кофе на Ko-fi. Спонсоры, пожелавшие указать своё имя, перечислены в [`SUPPORTERS.md`](SUPPORTERS.md). Спонсорство не является договором поддержки ([`SUPPORT.md`](SUPPORT.md)).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

## Лицензия

Движок, модули и консоль распространяются по **AGPL-3.0-only**; SDK, коннекторы и клиенты — по **Apache-2.0**. Коммерческий код собирается отдельно и не входит в этот репозиторий; коммерческие лицензии: `enterprise@olivares.ai`. Для вклада нужны подпись DCO (`git commit -s`) и [CLA](CLA.md).

> Предоставляется **как есть**, без каких-либо гарантий и без ответственности за потерю данных, прерывание бизнеса или упущенную выгоду. См. [`DISCLAIMER.md`](DISCLAIMER.md).

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Достоверная картина корпоративного AI.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
