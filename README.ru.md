<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Достоверная картина корпоративного AI" width="720"></a>

**Языки:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · **Русский** · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Интегрируйте, управляйте и защищайте AI, который вы действительно запускаете, — единым self-hosted бинарником.**

[Установка](#install) · [Быстрый старт](#quickstart) · [Примеры](examples/) · [Документация](#documentation) · [Безопасность](#security) · [Участие](CONTRIBUTING.md) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta**, проект активно развивается. Первый тегированный релиз, **v26.8.0**, поставляется с подписанными архивами, нативными пакетами и контейнерными образами. API и состав модулей всё ещё могут измениться до версии 1.0; то, что работает сегодня, что доступно по требованию, а что находится на стадии проектирования, указано в разделе [Честность и ограничения](docs-site/src/content/docs/start/honesty-and-limits.md) и, для каждого модуля, в [каталоге модулей](docs-site/src/content/docs/reference/modules/overview.md).

## Что это такое

То, что вы запускаете сейчас, — это estate: агенты для программирования, MCP-серверы, эндпоинты моделей, служебные учётные записи и запланированные задания, распределённые по машинам, которые никогда не были единой системой. Olivares AI — единый self-hosted бинарник на Go со встроенной консолью, который связывает всё это воедино: он даёт вашему AI то, что нужно для работы (контекст, доступ к ресурсам, управляемые сессии), а вам — права, политики, бюджеты и доказательства, позволяющие знать, что запущено, кто это запустил, к чему обращался AI, сколько это стоило и кто это согласовал.

**Мультипровайдерность по замыслу.** Claude Code интегрирован на самом глубоком уровне — хук `PreToolUse`/`PostToolUse`, managed settings, запуск и остановка из консоли, доступ к моделям по субъектам, — а Codex и Grok Build представлены рядом как первоклассные командные поверхности; gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw и Hermes имеют собственные коннекторы, каждый из которых указывает, какое принуждение он поддерживает, а что способен лишь наблюдать. Ollama и другие self-hosted эндпоинты инвентаризируются через локальный коннектор, который по замыслу работает только в режиме чтения.

**Кто его запускает.** Одна и та же сборка для любого масштаба: домашний сервер (один бинарник, SQLite, привязка к loopback); фрилансер с отдельным тенантом для каждого клиента и бюджетами, которые запрещают расходы до того, как приходит счёт; инженерная команда с общими рабочими элементами, SSO и журналом аудита, который никому не нужно собирать вручную; регулируемое предприятие с защитой на уровне строк в Postgres, HA, air-gapped-установками и WORM-архивированием. Открытая сборка — это вся платформа, а коммерческие надстройки — аддитивный код поверх неё, но никогда не функции, удалённые из неё; SSO, HA, WORM и бюджеты, которые действительно запрещают расходы, нужно провизионировать — это не настройки по умолчанию при первом запуске.

Обязательной телеметрии нет, а исходящий трафик управляющей плоскости по умолчанию отсутствует: за ваш периметр выходит только то, что вы настроили для передачи наружу, — обращения к API ваших моделей, подключённые вами выходы SIEM/webhook и поставщик эмбеддингов, если вы его провизионировали. Коллекторы читают из систем, которые вы уже эксплуатируете, поэтому неисправный коллектор никогда не оказывается на пути производственных данных.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840" alt="Один бинарник для любого масштаба: от домашнего сервера до регулируемого предприятия; где он работает и к чему обращается.">
</picture>
<sub>Одна и та же открытая сборка от homelab до регулируемого предприятия.</sub>
</div>

## Что он делает

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840" alt="Карта доступа: что каждый агент читает и записывает во всём estate; инициаторы слева, ресурсы справа.">
</picture>
<sub><b>Карта доступа</b> — что каждый агент читает и записывает во всём estate; чтение и запись различаются цветом.</sub>
</div>

- **Наблюдайте.** Инвентаризация всех обнаруженных агентов, сессий, моделей, MCP-серверов, инструментов и идентичностей; **карта доступа** на чтение/запись того, к чему каждый из них действительно обращается, с представлением **дрейфа «Разрешено против Наблюдаемого»**; живые сессии, граф оркестрации, состояние и SLA. То, чего система не видит, помечается как `unknown`, а не угадывается.
- **Запускайте работу.** Долговечные рабочие элементы с владельцем, зависимостями, критериями приёмки и решениями; огороженные аренды, чтобы два агента — или два человека — не могли одновременно удерживать одну и ту же работу; сессии, которые можно запускать, к которым можно подключаться и которые можно останавливать из консоли; делегирование авторизованным узлам через A2A. Shadow-режим и окончательные полномочия не реализованы и указаны как отсутствующие: [Рабочая плоскость](docs-site/src/content/docs/explanation/work-plane.md).
- **Управляйте и принуждайте.** Движок авторизации Cedar и **четыре закрытые по умолчанию (deny-closed) точки принуждения** — хук Claude Code, встроенный инференс-прокси `/v1/messages`, шлюз MCP `tools/call` и шлюз делегирования A2A, — поэтому неавторизованное действие блокируется, удерживается до согласования двумя людьми или, в хуке, переписывается до выполнения; точка учитывается только при условии, что тест проходит её сценарий без конфигурации и подтверждает отказ. Бюджеты запрещают или ограничивают расходы, аварийный доступ (break-glass) защищён двойным контролем, а **аварийный выключатель** estate отказывает закрыто.
- **Питайте его данными под управлением.** Источники контента (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, файловая система, ограниченная корневым каталогом) подключаются к управляемому извлечению: лексическое извлечение без исходящего трафика работает из коробки, семантическое извлечение на основе модели — после провизионирования эмбеддера, а уровень допуска принудительно проверяется в deny-closed режиме во время извлечения.
- **Докажите.** Журнал аудита с хеш-цепочкой и подписью Ed25519; запечатанные доказательства с привязкой к фреймворкам — **26 каталогов фреймворков** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…) — это самостоятельно оцениваемые семейства мер контроля, а не сертификации; выгрузка в SIEM/ITSM (CEF/LEEF/syslog/OTLP/OCSF). Для каждого развёртывания отдельно настраиваются: человеческие и нечеловеческие идентичности (WebAuthn/FIDO2, PIV/CAC, SSO с одним IdP, сверка SCIM, федерация идентичности агентов), встроенные ограждения, DLP, шифрование BYOK/CMEK и право на удаление с проверенным уничтожением ключей.

**30 модулей**, одна консоль, **158 интеграций** — эти числа выводятся из кода и проверяются при каждом пуше скриптом [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). Интеграция — это каталог коннектора с кодом на Go, причём двенадцать из них являются общими библиотечными пакетами; разбивка приведена в [`connectors/README.md`](connectors/README.md). Степень зрелости каждого модуля указана в [каталоге модулей](docs-site/src/content/docs/reference/modules/overview.md), а подключённые коннекторы по уровням покрытия — в [справочнике коннекторов](docs-site/src/content/docs/reference/connectors.md).

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840" alt="Как агенты работают вместе: единая устойчивая рабочая плоскость с рабочими элементами, огороженными арендами и сообщениями ограниченной области; делегирование через шлюз принуждения; Shadow-режим и окончательные полномочия показаны пунктиром, потому что они не реализованы.">
</picture>
<sub>Агенты используют одну устойчивую рабочую плоскость. Непостроенное нарисовано как отсутствующее.</sub>
</div>

## Взгляд внутрь консоли

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Сессии Claude Code: создание, подключение и управление из консоли."></picture><br><sub><b>Claude Code</b> — создавайте сессии, подключайтесь к ним и управляйте ими из консоли — без SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Работа: долговечный межсессионный бэклог рабочих элементов и решений."></picture><br><sub><b>Работа</b> — долговечный межсессионный бэклог: элементы, владение, критерии приёмки, решения.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="Оркестрация и A2A: граф делегирования между агентами, построенный по наблюдаемым сигналам."></picture><br><sub><b>Оркестрация и A2A</b> — кто кому делегирует — по наблюдаемым сигналам.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="Инвентаризация: все агенты, сессии, MCP-серверы, модели и идентичности, обнаруженные во всём estate."></picture><br><sub><b>Инвентаризация</b> — все обнаруженные агенты, сессии, MCP-серверы, модели и идентичности.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Дрейф минимальных привилегий: неожиданный доступ и неиспользуемые гранты поверх карты доступа."></picture><br><sub><b>Дрейф минимальных привилегий</b> — наблюдаемый, но не разрешённый доступ и гранты, которыми никто не пользуется.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Безопасность и форензика: находки ограждений, очередь аномалий и форензика с защитой от подделки."></picture><br><sub><b>Безопасность и форензика</b> — находки ограждений, аномалии, форензика с защитой от подделки.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="Аварийный выключатель: экстренная остановка estate с восстановлением под двойным контролем."></picture><br><sub><b>Аварийный выключатель</b> — одно нажатие останавливает каждую управляемую поверхность актуации; для восстановления требуются две учётные записи.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="Просмотр записи сессии: активность агента и доказательства управления на единой временной шкале; цепочка проверена."></picture><br><sub><b>Запись сессии</b> — активность агента и доказательства управления на единой временной шкале; цепочка проверена.</sub> |

Каждый стоп-кадр — снимок демонстрационного estate с начальными данными, отдаваемого работающим бинарником (`bash scripts/docs-captures.sh` пересоздаёт исходный набор). Полная карта экранов приведена в [справочнике консоли](docs-site/src/content/docs/reference/console.md).

<a name="install"></a>
## Установка

Каждый релиз поставляется по цепочке доверия с подписью cosign, проверяемой по типу артефакта: подписанный cosign манифест контрольных сумм охватывает перечисленные в нём архивы, пакеты и SBOM для каждого архива, каждый архив сопровождается отдельным SPDX SBOM с in-toto-аттестацией, контейнерный образ имеет подписи cosign и собственную SBOM-аттестацию, а весь набор — заявления OpenVEX и сведения о происхождении сборки SLSA. Для продукта безопасности цепочка поставок — часть модели доверия: [проверьте её](docs/RELEASE-VERIFICATION.md), прежде чем запускать.

**Удобный путь через HTTPS.** Текст скрипта поступает по HTTPS, но конвейер (pipe) не проверяет его заранее; после запуска скрипт определяет вашу ОС и архитектуру, требует `cosign`, проверяет подписанный манифест контрольных сумм и SHA-256 архива, устанавливает только бинарник и никогда не вызывает `sudo`. Передавая скрипт в оболочку через конвейер, закрепите версию:

```sh
curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh -s -- --version v26.8.0
olivares quickstart        # TLS on, loopback-only, no default credentials; prints the console URL + a one-time setup token
```

**Путь с высокой гарантией.** Сначала скачайте, проверьте и только затем запускайте: архивы, пакеты и манифест контрольных сумм находятся на [странице релиза](https://github.com/olivaresai/olivares/releases/tag/v26.8.0), а [`scripts/verify-release.sh`](scripts/verify-release.sh) проверяет всё, что присутствует, и сообщает, что было пропущено, — по умолчанию в режиме keyless, на отключённом от сети хосте — с `--key … --offline`. [Контракт доверия установщика](docs/RELEASE-INSTALLER.md) описывает оба пути; подписанный версионированный установщик с подключаемым по желанию адаптером службы появляется начиная с первого релиза, выпущенного после его добавления, а v26.8.0 ему предшествует.

| Путь | Что вы получаете |
|---|---|
| **Пакеты Linux** — `.deb`, `.rpm`, `.apk` | бинарник, усиленный systemd-юнит, пример env-файла и служебный пользователь `olivares` без возможности входа; служба не запускается за вас |
| **Контейнер** — `docker.io/olivaresai/olivares:26.8.0` | distroless, без root, теги без префикса `v`; `ghcr.io/olivaresai/olivares` — тот же образ по digest. Основной образ мультиархитектурный (amd64/arm64); варианты `-fips` и `-stig` доступны только для amd64 |
| **Homebrew** — `brew install olivaresai/tap/olivares` | бинарник релиза для macOS и Linux, проверяемый по подписанным контрольным суммам, со снятым карантином Gatekeeper; darwin-сборки ещё не нотаризованы Apple |
| **Kubernetes** — [`deploy/helm/olivares`](deploy/helm/olivares) или [`deploy/manifests/install.yaml`](deploy/manifests/install.yaml) | исходный код Helm-чарта и плоский манифест без Helm в дереве; чарт **пока не опубликован в OCI-реестре** |
| **Из исходного кода** — `task build` (Go 1.26+, [Task](https://taskfile.dev), pnpm) | `./bin/olivares quickstart`, тот же первый запуск, безопасный по умолчанию |

Движок **безопасен по умолчанию**: он привязывается к loopback, обслуживает HTTPS с самоподписанным сертификатом при первом запуске, поставляется без учётных данных по умолчанию и выводит одноразовый токен настройки; в контейнере или pod процесс слушает в собственной сети, а сопоставление портов хоста или Service сохраняет его закрытым. **Windows** пока не собирается — запускайте Linux-контейнер или WSL2 ([план](INSTALL.md#windows)). Матрица по ОС и настройка для продакшена приведены в [`INSTALL.md`](INSTALL.md); руководства по развёртыванию (Compose, Kubernetes, air-gapped) и [обновлениям](docs-site/src/content/docs/how-to/upgrade-and-rollback.md) — в [`docs-site/`](docs-site/).

<a name="quickstart"></a>
## Быстрый старт

Исследуйте синтетический estate или запустите систему по-настоящему. В обоих случаях работает один и тот же бинарник.

```sh
# a deterministic demo estate — loopback-only, no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, loopback; create the first administrator with the printed token
olivares quickstart
```

Демонстрационный сид предназначен только для обучения (пароль находится в публичном дереве исходного кода): никогда не направляйте его на реальные данные. CI проходит тот же путь с `task smoke:quickstart` и проверяет показатели карты доступа и дрейфа (20 узлов / 13 рёбер, с 8 неожиданными обращениями и 2 неиспользуемыми грантами), поэтому эта страница не может незаметно разойтись с кодом. [Полный быстрый старт](docs-site/src/content/docs/start/quickstart.md) подключает реальный коннектор pgAudit и содержит ссылки на пути установки в продакшене.

## Редакции

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840" alt="Редакции по составу: ядро AGPL — вся платформа, надстройки — аддитивный код поверх него, Cloud Standard — управляемый сервис.">
</picture>
<sub>Редакции по составу. Комплектация и цены по запросу.</sub>
</div>

AGPL-сборка — вся платформа, и она никогда не ограничивается по функциям изнутри; коммерческие надстройки представляют собой аддитивный код, а не функции, удалённые из открытого продукта. Подписка — это учётные данные для скачивания подписанных пакетов модулей — дистрибутивная модель, а не ключ, открывающий код, который уже находится на вашем диске. Число пользовательских учётных записей в self-hosted движке не ограничено, и все **четыре deny-closed точки принуждения** входят в открытую версию. Матрица открытых, коммерческих и запланированных возможностей по областям приведена в [`LICENSING.md`](LICENSING.md) и разделе [Открытое ядро и лицензирование](docs-site/src/content/docs/explanation/open-core-and-licensing.md).

## Архитектура

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840" alt="Архитектура: поверхности агентов, источники аудита, узлы MCP и A2A и источники контента собираются в единый self-hosted бинарник, обслуживающий консоль, REST API, gRPC, CLI и провайдер Terraform; облачная управляющая плоскость и лицензионный портал показаны как отдельные плоскости: облачная плоскость управления построена, но не развёрнута; лицензионный портал развёрнут, продажи отключены.">
</picture>
</div>

Единый статический бинарник на Go встраивает консоль и предоставляет четыре поверхности с задокументированным покрытием: REST API (основная), сфокусированное gRPC-зеркало стабильного ядра, CLI `olivares` и провайдер Terraform. Коллекторы работают внутри вашей инфраструктуры в трёх режимах; хранилищем служит SQLite или Postgres с защитой на уровне строк, которая сначала принудительно применяется в API хранилища, а затем ещё раз в Postgres. Подробности, включая рабочую плоскость по частям: [`ARCHITECTURE.md`](ARCHITECTURE.md).

<a name="documentation"></a>
## Документация

[docs.olivares.ai](https://docs.olivares.ai) — протестированные руководства по установке (один узел, Docker Compose, Kubernetes/Helm, air-gapped), руководства по коннекторам с реальными снимками консоли, поваренная книга (deny-closed политики, бюджеты, согласования, учения с аварийным выключателем, выгрузка в SIEM), справочник API и глоссарий. Начните со страниц [Что такое Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md) и [Честность и ограничения](docs-site/src/content/docs/start/honesty-and-limits.md).

<a name="security"></a>
## Безопасность

Сообщайте об уязвимостях приватно через [`SECURITY.md`](SECURITY.md), но никогда не в публичном issue. Движок работает по принципам «сначала чтение» и минимума данных: карта доступа хранит рёбра, а не полезную нагрузку, а её открытие записывается как действие. Порядок работы с бюллетенями безопасности: [`docs/security-advisories.md`](docs/security-advisories.md); карта доказательств цепочки поставок: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Сообщество

[`CONTRIBUTING.md`](CONTRIBUTING.md) (настройка, DCO/CLA, SPDX, граница коннекторов) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) (Contributor Covenant 2.1) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog 1.1, CalVer `vYY.M.PATCH`).

## Лицензия

`core/`, `modules/` и `web/` распространяются под **AGPL-3.0-only**; `sdk/`, `connectors/` и `clients/` — под **Apache-2.0**, причём коннектор никогда не импортирует движок. Коммерческие надстройки являются отдельными, необязательными и закрытыми: они собираются только с `-tags enterprise` и никогда не находятся в этом репозитории или открытом бинарнике; по вопросам коммерческого лицензирования пишите на `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Вклады требуют подписи DCO (`git commit -s`) и [CLA](CLA.md).

> **Без гарантий, без ответственности.** Программное обеспечение предоставляется **как есть**, **без каких-либо гарантий** и **без ответственности за потерю данных, перерыв в работе или упущенную выгоду**. Для управляющей плоскости это не формальность: ошибка конфигурации может заблокировать легитимную работу или пропустить ровно то, что вы намеревались остановить. Применяются AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 и дополнительное условие этого проекта — [`DISCLAIMER.md`](DISCLAIMER.md).

## Поддержать проект

Ядро бесплатно и останется бесплатным; поддержание каждого релиза подписанным, проверенным и актуальным требует постоянной работы. Если Olivares AI вам полезен, вы можете поддержать его через GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) или [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — либо сделать разовый взнос на Ko-fi. Спонсорство не является контрактом на поддержку и не даёт приоритета ([`SUPPORT.md`](SUPPORT.md)); спонсоры, попросившие указать их имена, перечислены в [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Достоверная картина корпоративного AI.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + коммерческая</a></sub>

</div>
