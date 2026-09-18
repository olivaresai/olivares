<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Достоверная картина корпоративного AI" width="720"></a>

**Языки:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · **Русский** · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Запускайте и управляйте AI, которым вы уже пользуетесь, — на своей инфраструктуре, с одной достоверной картиной.**

[Что это такое](#что-это-такое) · [Что оно делает](#что-оно-делает) · [Установка](#установка) · [Быстрый старт](#быстрый-старт) · [Консоль](#взгляд-внутрь-консоли) · [Редакции](#редакции-и-цены) · [Документация](#документация) · [Безопасность](#безопасность) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: v26.9.1](https://img.shields.io/badge/release-v26.9.1-28282B)](https://github.com/olivaresai/olivares/releases/tag/v26.9.1)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Beta**, проект активно развивается. **v26.9.1** поставляется с подписанными архивами, нативными пакетами и контейнерными образами. То, что работает сегодня, что доступно по требованию и что находится на стадии проектирования, указано в разделе [Честность и ограничения](docs-site/src/content/docs/start/honesty-and-limits.md).

## Что это такое

Ваш AI-estate сегодня — это агенты программирования, MCP-серверы, endpoint'ы моделей, служебные учётные записи и задания по расписанию, разбросанные по машинам, которые никогда не были одной системой. Никто не может из одного места сказать, что запущено, кто это запустил, к чему оно обратилось, сколько это стоило и кто на это согласился.

Olivares AI — **один self-hosted бинарник на Go, консоль в комплекте**, который держит этот estate вместе: даёт AI то, что нужно для работы (контекст, доступ к ресурсам, управляемые сессии), и даёт вам разрешения, политики, бюджеты и доказательства, чтобы им управлять. Self-hosted, без обязательной телеметрии, air-gapped установки поддерживаются.

Claude Code интегрирован на самом глубоком уровне (хук `PreToolUse`/`PostToolUse`, управляемые настройки, запуск и остановка из консоли); официальные CLI Codex и Grok — драйверы сессий первого класса; gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw, Hermes и self-hosted endpoint'ы вроде Ollama — коннекторы, каждый из которых указывает, что может принуждать, а что может только наблюдать. Сборка AGPL — это весь продукт, никогда не ограниченный функциями изнутри; ни один план не считает пользователей.

## Что оно делает

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Анимированная схема карты доступа на чтение/запись: агенты, сессии и идентичности слева, ресурсы, к которым они обращаются, справа, чтения синим, записи оранжевым, одна наблюдаемая запись, которая никогда не была разрешена, отмечена как находка дрейфа.">
<br><sub><b>Карта доступа</b> — что каждый агент читает и записывает во всём estate, разрешённое против наблюдаемого.</sub>
</div>

- **Увидьте.** Инвентаризация всех обнаруженных агентов, сессий, моделей, MCP-серверов, инструментов и идентичностей; **карта доступа** на чтение/запись с представлением **дрейфа** «Разрешено против Наблюдаемого»; живые сессии, граф оркестрации, состояние и SLA. То, чего система не видит, помечается как `unknown`, никогда не угадывается.
- **Запускайте работу.** Долговечные рабочие элементы с владельцем, зависимостями, критериями приёмки и решениями; огороженные аренды, чтобы два агента не могли одновременно удерживать одну и ту же работу; сессии Claude Code, Codex и Grok, которые запускают, к которым подключаются, которые прерывают и останавливают из консоли; делегирование авторизованным узлам через A2A.
- **Управляйте и принуждайте.** Движок авторизации Cedar и **четыре закрытые по умолчанию (deny-closed) точки принуждения** — хук Claude Code, встроенный инференс-прокси `/v1/messages`, шлюз MCP `tools/call` и шлюз делегирования A2A, — поэтому неавторизованное действие блокируется, удерживается до согласования двумя людьми или переписывается до выполнения. Бюджеты запрещают или ограничивают расходы, break-glass с двойным контролем, и **kill-switch** estate, который отказывает закрыто.
- **Питайте его, под управлением.** Источники контента (SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL, файловая система, ограниченная корневым каталогом) в управляемое извлечение; допуск принудительно проверяется deny-closed во время извлечения.
- **Докажите.** Журнал аудита с хеш-цепочкой и подписью Ed25519; запечатанные доказательства с привязкой к фреймворкам — **26 каталогов фреймворков** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR…) — самостоятельно оцениваемые семейства мер контроля, а не сертификации; выгрузка в SIEM/ITSM (CEF/LEEF/syslog/OTLP/OCSF); WebAuthn/FIDO2, PIV/CAC, SSO, SCIM, BYOK/CMEK и проверенное право на удаление, настраиваемые для каждого развёртывания.

**30 модулей**, одна консоль, **158 интеграций** — эти числа выводятся из кода и проверяются при каждом пуше скриптом [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh); разбивка — в [`connectors/README.md`](connectors/README.md), зрелость каждого модуля — в [каталоге модулей](docs-site/src/content/docs/reference/modules/overview.md).

## Установка

Выберите один метод: одна команда устанавливает, затем `olivares quickstart` печатает URL консоли и одноразовый токен настройки. Каждый релиз подписан cosign, с происхождением SLSA и SBOM; каждый путь ниже проверяет перед установкой, а `scripts/verify-release.sh` проверяет ручную загрузку (cosign + SHA-256, [как](INSTALL.md#verifying-a-release)). Движок **безопасен по умолчанию**: привязка к loopback, HTTPS при первом запуске, без учётных данных по умолчанию, одноразовый токен настройки, напечатанный при первом старте.

**1 · Одна команда, Linux и macOS** — проверенный установщик: определяет ОС и архитектуру, проверяет подписанные контрольные суммы и SHA-256 архива, устанавливает только бинарник, никогда не выполняет `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

Добавьте `--user` для пользовательской службы (systemd user unit или LaunchAgent) или запустите проверенный скрипт из привилегированной оболочки с `--system --start` для системной службы. Предпочитаете скачать, проверить и запустить вручную? Путь ручного бинарника и матрица по ОС: [`INSTALL.md`](INSTALL.md).

**2 · Docker** — multi-arch, distroless, без root; публикуется на всех интерфейсах хоста (добавьте `127.0.0.1:` перед привязками `-p`, чтобы оставить его локальным).

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` — тот же образ по digest; в производстве фиксируйте по digest. Варианты образов FIPS и STIG: [`INSTALL.md`](INSTALL.md#docker).

**3 · Docker Compose** — укреплённый стек, SQLite на одном узле с необязательными Postgres и резервным копированием.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes** — Helm-чарт из дерева или плоский манифест без Helm; чарт ещё не опубликован в реестре OCI.

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Пакеты Linux** — `.deb`, `.rpm`, `.apk` со [страницы релиза](https://github.com/olivaresai/olivares/releases/tag/v26.9.1): бинарник, пример файла env, пользователь `olivares` без входа и укреплённый unit; служба за вас не запускается.

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew** — macOS и Linux, проверяется по подписанным контрольным суммам.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · Из исходного кода** — Go 1.26+, [Task](https://taskfile.dev), pnpm.

```sh
task build && ./bin/olivares quickstart
```

**Air-gapped**: соберите подписанный образ, чарт и материалы проверки и проверьте офлайн с `scripts/verify-release.sh --key … --offline` ([инструкция](docs-site/src/content/docs/how-to/air-gap-install.md)). **Windows** ещё не собирается: запускайте Linux-контейнер или WSL2 ([план](INSTALL.md#windows)). Обновления и откат: [инструкция](docs-site/src/content/docs/how-to/upgrade-and-rollback.md).

## Быстрый старт

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

Демо-семя только для обучения (публичный пароль в дереве исходников): никогда не направляйте его на реальные данные. CI проходит тот же путь с `task smoke:quickstart` и проверяет счётчики карты доступа и дрейфа (20 узлов / 13 рёбер, 8 неожиданных доступов и 2 неиспользуемых выдачи). [Полный быстрый старт](docs-site/src/content/docs/start/quickstart.md) подключает настоящий коннектор pgAudit.

## Взгляд внутрь консоли

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Карта доступа: что каждый агент читает и записывает во всём estate, источники слева, ресурсы справа."></picture><br><sub><b>Карта доступа</b> — источники слева, ресурсы справа, чтение и запись по цвету.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Дрейф наименьших привилегий: неожиданные доступы и неиспользуемые выдачи поверх карты доступа."></picture><br><sub><b>Дрейф наименьших привилегий</b> — наблюдаемое, но не разрешённое, и выдачи, которыми никто не пользуется.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Сессии Claude Code, созданные, к которым подключаются и которыми управляют из консоли."></picture><br><sub><b>Сессии</b> — создавайте, подключайтесь и управляйте сессиями из консоли, без SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Работа: долговечный межсессионный бэклог рабочих элементов и решений."></picture><br><sub><b>Работа</b> — долговечный межсессионный бэклог: элементы, владение, приёмка, решения.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Безопасность и криминалистика: находки ограждений, очередь аномалий и криминалистика с признаками вмешательства."></picture><br><sub><b>Безопасность и криминалистика</b> — находки ограждений, аномалии, криминалистика с признаками вмешательства.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: расходы по моделям, использование токенов, бюджеты и прогноз run-rate."></picture><br><sub><b>FinOps</b> — расходы по модели и агенту, бюджеты, которые запрещают или ограничивают, run-rate.</sub> |

Каждый кадр — снимок посеянного демо-estate, который отдаёт работающий бинарник. Полная карта экранов: [справочник консоли](docs-site/src/content/docs/reference/console.md).

## Редакции и цены

Сборка AGPL — это вся платформа, никогда не ограниченная функциями изнутри. Коммерческие add-on — аддитивный код сверху, никогда не убранные функции; подписка — удостоверение для скачивания подписанных пакетов модулей. Учётные записи пользователей в self-hosted движке не ограничены, и все **четыре deny-closed точки принуждения** открыты.

| Редакция | Для кого | Что добавляет |
|---|---|---|
| **Community** | Любой. Бесплатно, AGPL-3.0, неограниченное число пользователей. | Полный продукт, self-hosted. Нет лицензионного шлюза на ядро. |
| **Business** | Организация, которая его внедряет. Цена за развёртывание, никогда за место. | Сервисы и необязательные пакеты, не функции ядра: коммерческая лицензия, поддерживаемый подписанный канал релизов, поддержка по электронной почте в рабочие часы и четыре необязательных add-on: **Regulated Operations**, **Compliance Packs**, **AI Runtime Security** и **Identity & Scale** (включает session cockpit для официальных инструментов). Все четыре вместе — это **Business Max**. |
| **Cloud** | Команды, которые хотят, чтобы ту же плоскость вели за них, предоплата, на общей инфраструктуре. | Управляемая control plane с опубликованными потолками. Пробного периода Cloud нет; бесплатный вариант остаётся self-hosted Community. |
| **Enterprise** | Регулируемые, многосубъектные, крупномасштабные estate. | Контракт, согласованный по электронной почте и подписанный на годовой форме заказа. |

Цены, матрица add-on и условия покупки: [olivares.ai/pricing](https://olivares.ai/pricing). Матрица открытое/коммерческое/планируемое: [`LICENSING.md`](LICENSING.md).

## Архитектура

Один статический бинарник на Go встраивает консоль и открывает четыре поверхности: REST API (основная), сфокусированное gRPC-зеркало стабильного ядра, CLI `olivares` и провайдер Terraform. Коллекторы работают внутри вашей инфраструктуры; хранилище — SQLite или Postgres с безопасностью на уровне строк, принуждаемой один раз в API хранилища и снова Postgres. Полная картина, включая рабочую плоскость: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Документация

[docs.olivares.ai](https://docs.olivares.ai) — проверенные учебники по установке (один узел, Docker Compose, Kubernetes/Helm, air-gapped), руководства по коннекторам с реальными снимками консоли, поваренная книга (политики deny-closed, бюджеты, согласования, учения kill-switch, выгрузка в SIEM), справочник API и глоссарий. Начните с [Что такое Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). На сайте: [продукт](https://olivares.ai/product) · [решения](https://olivares.ai/solutions) · [как это работает](https://olivares.ai/how-it-works) · [архитектура](https://olivares.ai/architecture) · [безопасность](https://olivares.ai/security) · [доверие](https://olivares.ai/trust) · [сравнение](https://olivares.ai/compare) · [демо](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [статус](https://olivares.ai/status) · [дорожная карта](https://olivares.ai/roadmap) · [бренд](https://olivares.ai/brand) · [пресса](https://olivares.ai/press). Релизы: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

## Безопасность

Сообщайте об уязвимости конфиденциально через [`SECURITY.md`](SECURITY.md), никогда как об общедоступной issue. Движок сначала читает и минимален по данным: карта доступа хранит рёбра, а не полезную нагрузку, и её открытие — регистрируемое действие. Проверка лицензии никогда нам не звонит; ядро AGPL не делает вызов лицензии. Поток рекомендаций: [`docs/security-advisories.md`](docs/security-advisories.md); доказательства цепочки поставок: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Сообщество

[`CONTRIBUTING.md`](CONTRIBUTING.md) (настройка, DCO/CLA, SPDX, граница коннекторов) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog, CalVer `vYY.M.PATCH`).

## Лицензия

`core/`, `modules/` и `web/` — **AGPL-3.0-only**; `sdk/`, `connectors/` и `clients/` — **Apache-2.0**, и коннектор никогда не импортирует движок. Коммерческие add-on отдельны, необязательны и закрыты — собираются только с `-tags enterprise`, никогда в этом репозитории; коммерческое лицензирование: `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Вклады требуют подписи DCO (`git commit -s`) и [CLA](CLA.md).

> **Без гарантий, без ответственности.** Программное обеспечение предоставляется **как есть**, **без гарантий любого рода** и **без ответственности за потерю данных, перерыв в работе или упущенную выгоду**. На control plane это не формальность: ошибка конфигурации может заблокировать законную работу или пропустить ровно то, что вы хотели остановить. Применяются AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 и дополнительное условие этого проекта — [`DISCLAIMER.md`](DISCLAIMER.md).

## Поддержать проект

Ядро бесплатно и останется бесплатным; держать каждый релиз подписанным, проверенным и актуальным — постоянная работа. Поддержите через GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) или [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — или разово на Ko-fi. Спонсорство не является договором поддержки ([`SUPPORT.md`](SUPPORT.md)); спонсоры, которые просят быть названными, перечислены в [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Достоверная картина корпоративного AI.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
