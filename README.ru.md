<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Достоверная картина корпоративного AI" width="720"></a>

**Языки:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · **Русский** · [日本語](./README.ja.md) · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**Запускайте и управляйте AI, которым вы уже пользуетесь, на своей инфраструктуре.**

[Что это такое](#что-это-такое) · [Что оно делает](#что-оно-делает) · [Установка](#установка) · [Быстрый старт](#быстрый-старт) · [Консоль](#взгляд-внутрь-консоли) · [Редакции](#редакции-и-цены) · [Документация](#документация) · [Безопасность](#безопасность) · [olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Release: 26.10.0](https://img.shields.io/badge/release-26.10.0-28282B)](https://github.com/olivaresai/olivares/releases/tag/26.10.0)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

</div>

> **Бета.** 26.10.0 поставляется в виде подписанных архивов, нативных пакетов и образов контейнеров. В разделе [Честность и ограничения](docs-site/src/content/docs/start/honesty-and-limits.md) указано, что работает сегодня, что работает по запросу и что пока остаётся проектом.

## Что это такое

Olivares AI — самостоятельно размещаемая плоскость управления для AI-агентов: один бинарный файл на Go со встроенной консолью. Агентам он даёт контекст, доступ к ресурсам и управляемые сессии, а вам — разрешения, политики, бюджеты и доказательства. Обязательной телеметрии нет, изолированные (air-gapped) установки поддерживаются.

Claude Code подключается через хук `PreToolUse`/`PostToolUse`, управляемые настройки, а также запуск и остановку из консоли. Официальные CLI Codex и Grok работают как управляемые сессии. gemini-cli, Cursor, opencode, goose, cline, OpenHands, OpenClaw, Hermes и самостоятельно размещаемые конечные точки, такие как Ollama, являются коннекторами; каждый указывает, что он принуждает, а что только наблюдает.

## Что оно делает

<div align="center">
<img src=".github/assets/motion-access-map.gif" width="840" alt="Анимированная схема карты доступа на чтение/запись: агенты, сессии и идентичности слева, ресурсы, к которым они обращаются, справа, чтения синим, записи оранжевым, одна наблюдаемая запись, которая никогда не была разрешена, отмечена как находка дрейфа.">
<br><sub><b>Карта доступа</b> — что каждый агент читает и записывает во всём estate, разрешённое против наблюдаемого.</sub>
</div>

- **Видеть.** Инвентарь агентов, сессий, моделей, MCP-серверов, инструментов и идентичностей, которые наблюдают коннекторы; **карта доступа** на чтение и запись с видом **дрейфа** между разрешённым и наблюдаемым; активные сессии, граф оркестрации, состояние и SLA. Доступ, который невозможно классифицировать, отображается как `unknown`.
- **Вести работу.** Рабочие элементы с ответственными, зависимостями, критериями приёмки и решениями; огороженные аренды, чтобы два агента не держали один элемент; сессии Claude Code, Codex и Grok запускаются, подключаются, прерываются и останавливаются из консоли; делегирование авторизованным партнёрам по A2A.
- **Управлять и принуждать.** Движок авторизации Cedar и **четыре закрытые по умолчанию (deny-closed) точки принуждения**: хук Claude Code, встроенный прокси инференса `/v1/messages`, шлюз MCP `tools/call` и шлюз делегирования A2A. Неразрешённое действие блокируется, ставится на одобрение двумя людьми или переписывается до выполнения. Бюджеты запрещают или ограничивают расходы, break-glass требует двух человек, а **kill-switch** при сбое закрывает доступ.
- **Подавать данные под контролем.** SharePoint, Confluence, Google Drive, Notion, Salesforce, Snowflake, S3, Azure AI Search, SAP OData, PostgreSQL и файловая система, ограниченная своим корнем, питают управляемый поиск; допуск проверяется в момент поиска.
- **Доказывать.** Журнал аудита, связанный цепочкой хешей и подписанный Ed25519; запечатанные доказательства, сопоставленные с **26 каталогами фреймворков** (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR и другими) как самооценённые семейства контролей, а не сертификаты; отправка в SIEM и ITSM (CEF, LEEF, syslog, OTLP, OCSF); WebAuthn/FIDO2, PIV/CAC, SSO, SCIM, BYOK/CMEK и проверяемое право на удаление, настраиваемые для каждого развёртывания.

**31 модуль**, одна консоль, **159 интеграций**; их подсчитывает по коду [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh). Разбивка — в [`connectors/README.md`](connectors/README.md), каждый модуль и его зрелость — в [каталоге модулей](docs-site/src/content/docs/reference/modules/overview.md).

## Установка

Выберите один способ. Затем `olivares quickstart` выведет URL консоли и одноразовый токен настройки. Выпуски подписаны cosign и сопровождаются происхождением SLSA и SBOM; каждый способ ниже проверяет файлы перед установкой, а `scripts/verify-release.sh` проверяет ручную загрузку (cosign и SHA-256, [как](INSTALL.md#verifying-a-release)). Движок запускается с HTTPS, без учётных данных по умолчанию и с одноразовым токеном настройки.

**1 · Одна команда, Linux и macOS.** Установщик определяет ОС и архитектуру, проверяет подписанные контрольные суммы и SHA-256 архива, устанавливает только бинарный файл и никогда не запускает `sudo`.

```sh
curl -fsSL https://olivares.ai/olivares/install.sh | sh
olivares quickstart        # prints the console URL and the one-time setup token
```

Добавьте `--user` для пользовательской службы (пользовательский юнит systemd или LaunchAgent) или запустите скрипт из привилегированной оболочки с `--system --start` для системной службы. Ручной способ и матрица по ОС: [`INSTALL.md`](INSTALL.md).

**2 · Docker.** Мультиархитектурный, distroless, без root. Порты публикуются на всех интерфейсах хоста; чтобы оставить их локальными, добавьте `127.0.0.1:` перед сопоставлениями `-p`.

```sh
docker run -d --name olivares -p 8443:8443 -p 8444:8444 \
  -v olivares-data:/var/lib/olivares \
  docker.io/olivaresai/olivares \
  serve --listen :8443 --grpc-listen :8444 --data-dir /var/lib/olivares
```

`ghcr.io/olivaresai/olivares` — тот же образ; в продакшене закрепляйте его по дайджесту. Варианты FIPS и STIG: [`INSTALL.md`](INSTALL.md#docker).

**3 · Docker Compose.** Защищённый стек: SQLite на одном узле, с Postgres и резервным копированием по желанию.

```sh
git clone --depth 1 https://github.com/olivaresai/olivares.git && cd olivares
docker compose -f deploy/compose/docker-compose.yml up --wait --wait-timeout 120
```

**4 · Kubernetes.** Helm-чарт из этого репозитория или плоский манифест без Helm. Чарт пока не опубликован в OCI (`publication-unverified`).

```sh
helm install olivares deploy/helm/olivares -n olivares-system --create-namespace
# or, Helm-free
kubectl create namespace olivares-system && kubectl apply -n olivares-system -f deploy/manifests/install.yaml
```

**5 · Пакеты Linux.** `.deb`, `.rpm` и `.apk` на [странице выпуска](https://github.com/olivaresai/olivares/releases/tag/26.10.0): бинарный файл, пример env-файла, пользователь `olivares` без входа в систему и защищённый юнит. Служба не запускается при установке.

```sh
sudo dpkg -i olivares_*_linux_amd64.deb        # Debian / Ubuntu   (sudo rpm -i … on RHEL / Fedora / SUSE; sudo apk add --allow-untrusted … on Alpine)
sudo systemctl enable --now olivares           # OpenRC hosts: sudo rc-service olivares start
```

**6 · Homebrew.** macOS и Linux, с проверкой по подписанным контрольным суммам.

```sh
brew install olivaresai/tap/olivares && olivares quickstart
```

**7 · Из исходного кода.** Go 1.26+, [Task](https://taskfile.dev) и pnpm.

```sh
task build && ./bin/olivares quickstart
```

**Изолированная среда (air-gapped):** соберите подписанный образ, чарт и материалы для проверки и проверьте их офлайн с помощью `scripts/verify-release.sh --key … --offline` ([руководство](docs-site/src/content/docs/how-to/air-gap-install.md)). У **Windows** пока нет нативной сборки: используйте Linux-контейнер или WSL2 ([план](INSTALL.md#windows)). Обновление и откат: [руководство](docs-site/src/content/docs/how-to/upgrade-and-rollback.md).

## Быстрый старт

```sh
# a deterministic demo estate — loopback-only (the demo password is public), no real data
olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps

# the real thing — TLS on, reachable from your network; create the first administrator with the printed token
olivares quickstart
```

Пароль демо публичный: не используйте демо с реальными данными. [Полный быстрый старт](docs-site/src/content/docs/start/quickstart.md) подключает реальный источник pgAudit.

## Взгляд внутрь консоли

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png"><img src="docs-site/public/console/access-map-light.png" alt="Карта доступа: что каждый агент читает и записывает во всём estate, источники слева, ресурсы справа."></picture><br><sub><b>Карта доступа</b> — источники слева, ресурсы справа, чтение и запись по цвету.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="Дрейф наименьших привилегий: неожиданные доступы и неиспользуемые выдачи поверх карты доступа."></picture><br><sub><b>Дрейф наименьших привилегий</b> — наблюдаемое, но не разрешённое, и выдачи, которыми никто не пользуется.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/agentops-dark.png"><img src="docs-site/public/console/agentops-light.png" alt="Сессии Claude Code, созданные, к которым подключаются и которыми управляют из консоли."></picture><br><sub><b>Сессии</b> — создавайте, подключайтесь и управляйте сессиями из консоли, без SSH.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-dark.png"><img src="docs-site/public/console/work-light.png" alt="Работа: долговечный межсессионный бэклог рабочих элементов и решений."></picture><br><sub><b>Работа</b> — долговечный межсессионный бэклог: элементы, владение, приёмка, решения.</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="Безопасность и криминалистика: находки ограждений, очередь аномалий и криминалистика с признаками вмешательства."></picture><br><sub><b>Безопасность и криминалистика</b> — находки ограждений, аномалии, криминалистика с признаками вмешательства.</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/finops-dark.png"><img src="docs-site/public/console/finops-light.png" alt="FinOps: расходы по моделям, использование токенов, бюджеты и прогноз run-rate."></picture><br><sub><b>FinOps</b> — расходы по модели и агенту, бюджеты, которые запрещают или ограничивают, run-rate.</sub> |

Экраны взяты из демо на работающем бинарном файле. Все экраны: [справочник по консоли](docs-site/src/content/docs/reference/console.md).

## Редакции и цены

Community — это полный продукт под AGPL-3.0: неограниченное число пользователей и четыре deny-closed точки принуждения. Business и Enterprise добавляют коммерческий код, собираемый только с `-tags enterprise`; из Community ничего не удаляется и не ограничивается.

| Редакция | Цена | Что входит |
|---|---|---|
| **Community** | Бесплатно, AGPL-3.0 | Полный самостоятельно размещаемый продукт. Неограниченное число пользователей, один активный поставщик удостоверений. |
| **Business** | 129 USD в месяц или 1 290 USD в год | Коммерческая лицензия, подписанный канал выпусков, поддержка по электронной почте в рабочее время, а также **Regulated Operations**, **AI Runtime Security**, **Compliance Packs** и **Identity & Scale**. Неограниченное число пользователей; одно юридическое лицо; до двух продакшен-развёртываний, каждое с одним staging-развёртыванием; до пяти активных поставщиков удостоверений. |
| **Enterprise** | Договор | Больше юридических лиц, развёртываний и поставщиков удостоверений, изолированные зеркала, индивидуальный LTS и условия поддержки, по годовому заказу. |

Условия покупки: [olivares.ai/pricing](https://olivares.ai/pricing). Что открыто, а что коммерческое: [`LICENSING.md`](LICENSING.md).

## Архитектура

Один статический бинарный файл на Go включает консоль и предоставляет четыре интерфейса: REST API, gRPC-зеркало стабильного ядра, CLI `olivares` и провайдер Terraform. Коллекторы работают внутри вашей инфраструктуры. Хранилище — SQLite или Postgres с защитой на уровне строк, которая применяется в API хранилища и повторно в Postgres. Подробности, включая рабочую плоскость: [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Документация

[docs.olivares.ai](https://docs.olivares.ai) — проверенные учебники по установке (один узел, Docker Compose, Kubernetes/Helm, air-gapped), руководства по коннекторам с реальными снимками консоли, поваренная книга (политики deny-closed, бюджеты, согласования, учения kill-switch, выгрузка в SIEM), справочник API и глоссарий. Начните с [Что такое Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md). На сайте: [продукт](https://olivares.ai/product) · [решения](https://olivares.ai/solutions) · [как это работает](https://olivares.ai/how-it-works) · [архитектура](https://olivares.ai/architecture) · [безопасность](https://olivares.ai/security) · [доверие](https://olivares.ai/trust) · [сравнение](https://olivares.ai/compare) · [демо](https://olivares.ai/demo) · [changelog](https://olivares.ai/changelog) · [статус](https://olivares.ai/status) · [дорожная карта](https://olivares.ai/roadmap) · [бренд](https://olivares.ai/brand) · [пресса](https://olivares.ai/press). Релизы: [GitHub](https://github.com/olivaresai/olivares/releases) · [`CHANGELOG.md`](CHANGELOG.md).

## Безопасность

Сообщайте об уязвимостях конфиденциально через [`SECURITY.md`](SECURITY.md), а не в публичных issue. Карта доступа хранит связи, а не содержимое, и её открытие фиксируется в аудите. Проверка лицензии выполняется офлайн; ядро AGPL не обращается к серверу лицензий. Бюллетени: [`docs/security-advisories.md`](docs/security-advisories.md); доказательства цепочки поставок: [`docs/openssf-badge.md`](docs/openssf-badge.md).

## Сообщество

[`CONTRIBUTING.md`](CONTRIBUTING.md) (настройка, DCO/CLA, SPDX, граница коннекторов) · [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) · [`SUPPORT.md`](SUPPORT.md) · [`GOVERNANCE.md`](GOVERNANCE.md) · [`CHANGELOG.md`](CHANGELOG.md) (Keep a Changelog, CalVer `YY.M.PATCH`).

## Лицензия

`core/`, `modules/` и `web/` распространяются под **AGPL-3.0-only**; `sdk/`, `connectors/` и `clients/` — под **Apache-2.0**, и коннектор никогда не импортирует движок. Коммерческий код собирается только с `-tags enterprise` и не входит в этот репозиторий. Коммерческое лицензирование: `enterprise@olivares.ai` — [`LICENSING.md`](LICENSING.md). Для вклада нужны подпись DCO (`git commit -s`) и [CLA](CLA.md).

> **Без гарантий.** Программное обеспечение предоставляется **как есть**, **без каких-либо гарантий** и **без ответственности за потерю данных, прерывание бизнеса или упущенную выгоду**. Применяются AGPL-3.0-only §§15–16, Apache-2.0 §§7–8 и дополнительное условие этого проекта — [`DISCLAIMER.md`](DISCLAIMER.md).

## Поддержать проект

Поддержите проект через GitHub Sponsors — [github.com/sponsors/olivaresai](https://github.com/sponsors/olivaresai) или [github.com/sponsors/fran-olivares](https://github.com/sponsors/fran-olivares) — или разово через Ko-fi. Спонсорство не является договором поддержки ([`SUPPORT.md`](SUPPORT.md)); спонсоры, пожелавшие быть названными, перечислены в [`SUPPORTERS.md`](SUPPORTERS.md).

[![ko-fi](https://ko-fi.com/img/githubbutton_sm.svg)](https://ko-fi.com/Z1R625SAD2)

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>Достоверная картина корпоративного AI.</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
