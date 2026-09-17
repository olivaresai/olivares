---
title: "Справочник конфигурации"
description: "Проверенная поверхность конфигурации control plane Olivares AI: флаги serve, переменные окружения, выбор хранилища и безопасные значения по умолчанию из коробки."
---

Эта страница документирует поверхность конфигурации движка control plane — единого Go-бинарника с именем `olivares`. Она охватывает флаги, принимаемые подкомандой `serve`, переменные окружения, которые движок читает при загрузке, то, как выбираются хранилище и policy decision point, и безопасные значения по умолчанию, которые действуют вообще без какой-либо конфигурации.

Всё перечисленное здесь взято из собственных определений команд движка и composition root. Там, где настройку нельзя подтвердить в исходном коде, она не указана. О концептуальной модели безопасности, стоящей за этими значениями по умолчанию, см. [модель безопасности](/ru/explanation/security/security-model/); о запускаемом end-to-end-пути см. [self-hosting](/ru/how-to/self-hosting/).

:::note[Философия конфигурации]
Движок конфигурируется флагами и переменными окружения, а не разросшимся файлом конфигурации. Все читаемые им переменные перечислены ниже и генерируются из самих исходников. Секреты, подключающие реальные источники, остаются в файлах, удерживаемых оператором и упоминаемых переменной окружения — никогда в хранилище. Значения по умолчанию выбраны так, чтобы падать закрыто: loopback-привязки, TLS включён, нет учётных данных по умолчанию.
:::

## Подкоманда `serve`

`olivares serve` запускает REST/web-HTTP-сервер и gRPC-сервер в одном процессе, причём веб-интерфейс выдаётся с того же origin, что и API. Флаги ниже — это проверенные входы конфигурации для этой команды.

| Флаг | По умолчанию | Назначение |
| --- | --- | --- |
| `--listen` | `127.0.0.1:8443` | Адрес прослушивания HTTP (REST API + встроенный веб-интерфейс). |
| `--grpc-listen` | `127.0.0.1:8444` | Адрес прослушивания gRPC (control-plane / ingest API коллектора). |
| `--data-dir` | `$OLIVARES_DATA_DIR`, существующая установка в `./olivares-data`, иначе `$XDG_DATA_HOME/olivares` или `~/.local/share/olivares` | Каталог данных: ключ подписи аудита, TLS-материал и (для SQLite) файл хранилища. |
| `--engine` | `sqlite` | Движок хранилища: `sqlite` или `postgres`. |
| `--dsn` | пусто (файл SQLite в каталоге данных) | Строка подключения к хранилищу. |
| `--checkpoint-interval` | `1h` | Как часто записывается подписанный чекпойнт аудита поверх цепочки каждого тенанта. `0` отключает. |
| `--insecure` | выкл | Выдавать HTTP/gRPC в открытом виде. Опасно; только для локальной разработки. |
| `--seed-demo` | выкл | Загрузить синтетический пример estate для демо/E2E. Отказывается запускаться на не-loopback-привязке. |

TLS включён по умолчанию. Когда `--tls-cert`/`--tls-key` не предоставлены, движок один раз, заранее, обеспечивает наличие самоподписанного сертификата в каталоге данных до того, как какой-либо слушатель примет соединение, так что и HTTP-, и gRPC-серверы используют один и тот же сертификат, и ни один не откатывается к открытому тексту. Когда движок генерирует самоподписанный сертификат, он логирует `cert_fingerprint_sha256` (хэш сертификата — то, что показывает браузер) и `pin_sha256` (хэш SPKI конечного сертификата). `--pin-sha256` принимает второй, в base64 или hex; отпечаток сертификата — другой хэш: он разбирается без ошибки, 32 байта в любой записи, и затем падает на handshake с `TLS SPKI pin mismatch`, где указано, какое значение использовать.

:::caution[`--insecure` по замыслу только для loopback]
`--insecure` выдаёт HTTP и gRPC в открытом виде, что выставило бы bearer-токены в сети. Путь gRPC **падает закрыто** (fail closed): вне `--insecure` сервер отказывается конструировать незашифрованный слушатель, а не молча деградирует. Используйте `--insecure` только против `127.0.0.1` во время локальной разработки, никогда на опубликованном адресе.
:::

:::danger[`--seed-demo` синтетический и самозащитный]
`--seed-demo` провижинит демо-администратора с **публичным паролем из дерева исходного кода** и сфабрикованными данными estate. Он только для демо и E2E. Движок отказывается запускать его на не-loopback-слушателе: если либо `--listen`, либо `--grpc-listen` не является loopback-адресом, команда завершается с ошибкой. Используйте одноразовый каталог данных; никогда не направляйте его на реальные данные.
:::

Полный список флагов — включая флаги только-для-Postgres и mutual-TLS, используемые в распределённых развёртываниях — находится в [Справочнике CLI](/ru/reference/cli/). Эта страница документирует общую поверхность конфигурации; некоторые продвинутые флаги управляют мультиузловыми топологиями, описанными в [обзоре архитектуры](/ru/explanation/architecture/overview/).

## Переменные окружения

Три группы ниже — те, с которыми оператор сталкивается в первую очередь; для них подробно описано поведение. После них следует полный перечень, сгенерированный из исходного кода движка, поэтому он не может отстать от бинарника.

### Каталог данных

| Переменная | Эффект |
| --- | --- |
| `OLIVARES_DATA_DIR` | Каталог данных по умолчанию, когда `--data-dir` не задан. Без него движок использует существующую установку в `./olivares-data`, иначе `$XDG_DATA_HOME/olivares` или `~/.local/share/olivares` — но никогда текущий рабочий каталог, где он оставил бы приватные ключи. |

Каталог данных хранит ключ подписи аудита, TLS-сертификат и ключ, и — для движка SQLite — файл хранилища. Сохраняйте его между перезапусками.

### Подключение реальных источников

| Переменная | Эффект |
| --- | --- |
| `OLIVARES_SOURCES_CONFIG` | Путь к JSON-файлу, который подключает реальные источники наблюдений и провайдеров реестра идентичностей до старта движка. |

`OLIVARES_SOURCES_CONFIG` — это единственный вход, через который разрешаются не-демо-источники сигналов и провайдеры реестра идентичностей. Это несущая секреты конфигурация оператора, и она намеренно держится вне хранилища. Движок читает её при загрузке и регистрирует каждый источник **до** старта рантайма.

Обработка честна, а не fail-fast:

- **Отсутствующая** переменная даёт пустую конфигурацию, и движок предупреждает, что ничего реального не подключено.
- **Нечитаемый или невалидный JSON**-файл предупреждает и даёт пустую конфигурацию — он никогда не прерывает загрузку.
- Настроенный, но **пустой** список источников предупреждает, что ни один коннектор не будет принимать данные, так что estate работает без живого трафика, вместо того чтобы молча выглядеть здоровым.
- Пустой список **идентичностей** предупреждает, что реестр остаётся пустым, а синхронизация реестра становится no-op.

Это по замыслу: ненастроенный источник выдаёт предупреждение, а не падает control plane и не притворяется работающим. Чтобы реально заполнить карту доступа, настройте хотя бы один источник — см. [подключение источника](/ru/how-to/connect-a-source/) и, для кооперативного пути Claude Code поверх OpenTelemetry и MCP, [подключение Claude Code](/ru/how-to/connect-claude-code/).

### Authorization decision point (PDP)

Policy decision point авторизации выбирается в composition root по окружению. Нативный движок attribute-based access control (ABAC) и role-based access control (RBAC) управляют всегда; внешний PDP, когда он выбран, является дополнительным слоем **только-ограничения** (restrict-only), который никогда не может расширить доступ.

| Переменная | Эффект |
| --- | --- |
| `OLIVARES_PDP_ENGINE` | Выбирает внешний PDP: `cedar`, `opa` или `none` (пусто или `none` означает только нативный ABAC). |
| `OLIVARES_PDP_CEDAR_FILE` | Только движок Cedar: путь к файлу политики Cedar оператора. |
| `OLIVARES_PDP_OPA_URL` | Только движок OPA: базовый URL эндпойнта Open Policy Agent. |
| `OLIVARES_PDP_OPA_PATH` | Только движок OPA: путь решения, запрашиваемый под этим эндпойнтом. |
| `OLIVARES_PDP_OPA_TOKEN` | Только движок OPA: bearer-токен для эндпойнта OPA. |

За одним швом сидят два адаптера: **встроенный Cedar**-вычислитель (основной, чисто-Go-путь) и адаптер **OPA-over-HTTP**. Оператор выбирает один движок; оба могут только ограничить, но никогда не расширить решение, которое уже принял встроенный RBAC.

:::note[Плохая политика никогда не лишает плоскость управления]
Если `OLIVARES_PDP_ENGINE` выбирает движок, но его конфигурация невалидна — нечитаемый файл Cedar, искажённая цель OPA — движок **отключает только внешний PDP**, продолжает принудительно применять нативный движок ABAC и RBAC и громко логирует. Сломанный файл политики никогда молча не оставляет запросы неуправляемыми и никогда не валит control plane.
:::

О модели deny-by-default, привилегированной природе просмотра графа доступа и о том, как каждое чтение авторизации аудируется, см. [модель безопасности](/ru/explanation/security/security-model/).

<!-- BEGIN GENERATED olivares-env-reference — regenerate with `bash scripts/check-config-env-docs.sh --write`; do not edit by hand -->

### Полный справочник переменных

Таблица ниже генерируется из собственных исходников продукта: 290 переменных и 17 семейств, создаваемых во время выполнения, охватывают движок, CLI, оператор Kubernetes, провайдер Terraform и коннекторы. При каждом изменении она заново генерируется из этих источников и сверяется с ними, поэтому не отстаёт от бинарного файла.

**Обязательно** означает, что читающая переменную функция без неё не запускается; большинство переменных необязательны, и движок работает, даже если ни одна из них не задана.

| Переменная | Обязательно | По умолчанию | Что настраивает |
| --- | --- | --- | --- |
| `OLIVARES_ACTOR` | No | — | Default `--actor` for the decision-bearing eventing verbs, so a scripted change still records who made it. |
| `OLIVARES_ADMIN_DSN` | No | — | Privileged connection string the Kubernetes operator uses for schema migration, separate from the least-privilege runtime role. |
| `OLIVARES_AGENTCORE_EXPORT_CONFIG` | No | — | Path to the JSON configuration of the AgentCore usage export. |
| `OLIVARES_AGENT_GATEWAY_CONFIG` | No | — | Path to the JSON configuration of the MCP agent gateway. |
| `OLIVARES_ALLOW_CLEARTEXT` | No | — | Dangerous opt-in: lets a request carrying a credential reach a NON-loopback host over plain HTTP, for surfaces with no --allow-cleartext flag of their own. |
| `OLIVARES_API_TOKEN` | No | — | API token the Terraform provider authenticates with, when the provider block does not set one. |
| `OLIVARES_APPROVAL_BRIDGE_CONFIG` | No | — | Path to the JSON configuration of the bridge that routes approvals to an external system. |
| `OLIVARES_AUDIT_ARCHIVE_CONFIG` | No | — | Path to the JSON settings for the `s3archive` sink. Secret-bearing, so it is a file rather than a value. |
| `OLIVARES_AUDIT_ARCHIVE_DIR` | No | — | Root directory for the `dir` archive sink. |
| `OLIVARES_AUDIT_ARCHIVE_INTERVAL` | No | `24h` | How often sealed audit segments are archived, as a Go duration. |
| `OLIVARES_AUDIT_ARCHIVE_RETAIN_DAYS` | No | `2555` | How long archived audit segments are retained, in days. |
| `OLIVARES_AUDIT_ARCHIVE_SEGMENT_EVENTS` | No | — | How many events a sealed archive segment holds before the next one is started. |
| `OLIVARES_AUDIT_ARCHIVE_SINK` | No | — | Where sealed audit segments are archived: unset for off, `dir` for a local directory, `s3archive` for object storage. |
| `OLIVARES_AUDIT_LEGALHOLD_INTERVAL` | No | — | How often the long-horizon legal-hold sweep runs, as a Go duration. |
| `OLIVARES_AUDIT_LEGALHOLD_RECONCILE` | No | — | Булев переключатель, а не путь к файлу, для долгосрочного механизма сверки legal hold, который устанавливает legal hold Object Lock на архивные сегменты аудита тенантов с активным legal hold. Читается только сборками, скомпилированными с тегами `enterprise` и `addon_reg`, при запуске движка; изменение вступает в силу после перезапуска. Включает его только значение, которое `strconv.ParseBool` принимает как true, например `true` или `1`; если переменная не задана, равна false или не разбирается, он остаётся выключенным без ошибки. Кроме того, он остаётся выключенным, пока архив аудита не настроен на sink, способный устанавливать legal hold Object Lock. |
| `OLIVARES_AUDIT_META_BLINDING` | No | — | Whether audit metadata commitments are written blinded, and how strictly that is required. |
| `OLIVARES_AUDIT_SIGNING_KEY` | No | — | Audit checkpoint signing key, inline. Prefer the file form so the key never sits in a process environment. |
| `OLIVARES_AUDIT_SIGNING_KEY_FILE` | No | — | Path to the audit checkpoint signing key. This is the operator-held form. |
| `OLIVARES_AUDIT_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the audit signing key wrapped by a key management service, unwrapped at boot. |
| `OLIVARES_AUDIT_SPOOL_MAX_BYTES` | No | — | Upper bound on the on-disk audit spool before the full-spool rule applies. |
| `OLIVARES_AUDIT_SPOOL_ON_FULL` | No | — | What happens when the audit spool is full: the deny-closed posture refuses the write rather than dropping the record. |
| `OLIVARES_AUTHZEN_ALLOWED_CIDRS` | No | — | Comma-separated CIDR ranges allowed to reach the AuthZEN endpoints. Unset leaves the endpoint reachable wherever the listener is. |
| `OLIVARES_AUTHZEN_DISABLED` | No | — | Set to a true value to turn the AuthZEN decision endpoint off. |
| `OLIVARES_AUTHZEN_EXPORT_DISABLED` | No | — | Set to a true value to turn the AuthZEN export endpoint off. |
| `OLIVARES_AUTHZEN_SEARCH_DISABLED` | No | — | Set to a true value to turn the AuthZEN search endpoints off while leaving decisions on. |
| `OLIVARES_BASE_URL` | No | — | Public base URL of this control plane, used where an absolute link back to it has to be produced. |
| `OLIVARES_BUS_CONFIG` | No | — | Path to the JSON configuration of the message bus the engine publishes on. |
| `OLIVARES_CAEP_TRANSMITTER_CONFIG` | No | — | Path to the JSON configuration of the CAEP transmitter that pushes shared-signal events. |
| `OLIVARES_CATALOG_SIGNING_KEY` | No | — | Catalog signing key, inline. Prefer the file form. |
| `OLIVARES_CATALOG_SIGNING_KEY_FILE` | No | — | Path to the catalog signing key. |
| `OLIVARES_CATALOG_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the catalog signing key wrapped by a key management service. |
| `OLIVARES_CIRCUIT_BREAKER_CONFIG` | No | — | Булев переключатель, а не путь к файлу, несмотря на суффикс `_CONFIG`, для circuit breaker среды выполнения; его правила для каждого тенанта здесь не задаются. Читается только сборками, скомпилированными с тегами `enterprise` и `addon_airs`, при запуске движка; изменение вступает в силу после перезапуска. Если переменная не задана или её значение `strconv.ParseBool` принимает как false, circuit breaker остаётся выключенным. Значение, не являющееся булевым, записывается в журнал как ошибка и тоже оставляет его выключенным. Значение true включает его, когда доступны данные модуля; иначе в журнал записывается предупреждение, и он остаётся выключенным. |
| `OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG` | No | — | Path to the JSON configuration of the administrative actuator that applies changes at the model provider. |
| `OLIVARES_CLAUDE_ADMIN_KEY` | No | — | Administrative API key used to read identity posture from the model provider. |
| `OLIVARES_CLAUDE_ERASER_CONFIG` | No | — | Path to the JSON configuration of the erasure actuator that carries out deletion requests. |
| `OLIVARES_CLAUDE_FILES_CONFIG` | No | — | Path to the JSON configuration of the provider file inventory scan. |
| `OLIVARES_CLAUDE_INFERENCE_KEY` | No | — | API key the engine uses for its own inference calls. Unset leaves the inference-backed features off. |
| `OLIVARES_CLAUDE_WORKSPACE_ID` | No | — | Workspace whose identity posture is read, when the administrative key spans several. |
| `OLIVARES_CLI_CONFIG` | No | — | Path to the CLI configuration file, replacing the default per-user location. Used by hermetic automation. |
| `OLIVARES_CLI_TRAMPOLINE` | No | — | Set to `1` inside a re-executed child process so the binary runs the requested subcommand instead of the outer test harness. |
| `OLIVARES_CODEX_HOOK_ACCOUNT` | No | — | Account the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_AGENT` | No | — | Agent identity the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_ORG` | No | — | Organization the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the Codex hook enforcement point server. |
| `OLIVARES_CODEX_HOOK_TENANT` | No | — | Tenant the Codex hook client reports. |
| `OLIVARES_CODEX_HOOK_TOKEN` | No | — | Token the Codex hook client presents to the enforcement point. |
| `OLIVARES_CODEX_HOOK_URL` | No | — | Base URL of the enforcement point the Codex hook client calls. |
| `OLIVARES_COMMUNICATION_ACTIVATION` | No | — | REQUESTED K3 communication activation (`on` or `off`; default off), parsed at boot by cmd/olivares/communicationcomposition.go. `on` binds the local outbox pump witness and enables the dual runtime credential posture before the first leadership election; whether a communication credential can actually be minted is decided per launch from the EFFECTIVE readiness conjunction (store proof, sealer, directory resolver, permissions, pump), never from this flag alone. `on` needs both keyring files below; when either is not declared, cannot be opened or does not load, boot continues with K3 OFF and the cause visible (composition log, pump lane verdict `custody_unavailable`, non-effective readiness) while core, K1 and K2 serve. Only an unrecognized value is a configuration error. |
| `OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE` | Yes | — | Path to the JSON keyring the communication content sealer loads at boot (cmd/olivares/boot.go). Secret-bearing, so it is a file rather than a value: sealed message bodies are verified against the keys it carries, and an engine started without it cannot open content sealed by a peer that had one. |
| `OLIVARES_COMMUNICATION_CURSOR_KEYRING_FILE` | Yes | — | Path to the JSON keyring (`olivares.communication-cursor-keyring.v1`) that signs and verifies inbox cursor navigation tokens, loaded at boot through the same custody mechanism as the content keyring (cmd/olivares/communicationcursorkeyring.go). Secret-bearing, so it is a file rather than a value; a rotated-out key marked `retired_at` keeps verifying for the token retention window and is dropped afterwards, and a restart never mints a fresh key. |
| `OLIVARES_COMMUNICATION_TOKEN` | Yes | — | NOT an operator setting, and documented here precisely so nobody sets it. The engine MINTS this bearer and injects it into a conducted session's child process exactly once (modules/sessions/runtime_bridge.go); its tuple travels inside the authenticated principal. It is RESERVED on the launch path: validateLaunchInjectedEnv (modules/sessions/runtime.go) refuses any launch whose injected environment carries it, so a caller-supplied value is rejected rather than honoured. It appears in the roster because that reserved-name check mentions it, not because the engine reads it. |
| `OLIVARES_COMPUTER_USE_CONFIG` | No | — | Путь к JSON-файлу политик шлюза computer use. Читается только сборками, скомпилированными с тегами `enterprise` и `addon_airs`. Если переменная не задана, шлюз остаётся выключенным; файл, который не удаётся прочитать или разобрать, заставляет шлюз запрещать весь computer use, пока файл не будет исправлен, а движок перезапущен. |
| `OLIVARES_CONFIG_STRICT` | No | — | Set to `1` to make `olivares config effective` and `config validate` reject any unrecognized `OLIVARES_*` key. |
| `OLIVARES_CONTENT_FIREWALL_CONFIG` | No | — | Путь к JSON-файлу политик content firewall, который проверяет трафик инференса во встроенном (inline) прокси. Читается только сборками, скомпилированными с тегами `enterprise` и `addon_airs`. Если переменная не задана, эта проверка остаётся выключенной; файл, который не удаётся прочитать или разобрать, заставляет firewall отклонять каждый проверяемый им запрос, пока файл не будет исправлен, а движок перезапущен. |
| `OLIVARES_CONTEXT_MAX_TOKENS` | No | — | Upper bound on the context a governed session may assemble, in tokens. |
| `OLIVARES_CONTEXT_STRATEGY` | No | — | Which strategy assembles a governed session's context when the bound is reached. |
| `OLIVARES_CREDENTIAL_MINTER_CONFIG` | No | — | Путь к JSON-конфигурации посерверного модуля выпуска upstream-учётных данных (обмен токенов по RFC 8693) шлюза MCP. Содержит секреты, поэтому задаётся файлом, а не значением. Читается сборками, скомпилированными с тегом `enterprise`, когда движок запускается и строит шлюз для upstream URL. Если переменная не задана, сохраняются статические upstream-учётные данные; файл, который не удаётся прочитать или разобрать, приводит к отказу в любых upstream-учётных данных, пока файл не будет исправлен, а движок перезапущен. |
| `OLIVARES_DATA_DIR` | No | — | Data directory used when `--data-dir` is not given: audit signing key, TLS material and, for SQLite, the store file. |
| `OLIVARES_DB_MAX_CONNS` | No | — | Upper bound on pooled database connections. Unset leaves the driver default. |
| `OLIVARES_DEPLOY_EXECUTOR_CONFIG` | No | — | Path to the JSON configuration of the executor that applies deployment changes. |
| `OLIVARES_DR_KEK_FILE` | No | — | Path to a raw 32-byte key-encryption key for backups, for the path where a key management service does the unwrapping. |
| `OLIVARES_DR_OFFSITE_ACCESS_KEY_ID_FILE` | No | — | Path to the file holding the offsite access key id, so the credential stays out of the environment. |
| `OLIVARES_DR_OFFSITE_BUCKET` | No | — | Offsite bucket for disaster-recovery bundles. Setting it turns offsite replication on. |
| `OLIVARES_DR_OFFSITE_ENDPOINT` | No | — | S3-compatible endpoint for offsite replication. Unset means AWS S3 in the configured region. |
| `OLIVARES_DR_OFFSITE_PREFIX` | No | — | Key prefix for bundles inside the offsite bucket. |
| `OLIVARES_DR_OFFSITE_REGION` | No | — | Region for offsite replication. |
| `OLIVARES_DR_OFFSITE_SECRET_ACCESS_KEY_FILE` | No | — | Path to the file holding the offsite secret access key. |
| `OLIVARES_DR_OFFSITE_SESSION_TOKEN_FILE` | No | — | Path to the file holding an offsite session token, for temporary credentials. |
| `OLIVARES_DR_PASSPHRASE_FILE` | No | — | Path to the backup passphrase file, from which the backup key-encryption key is derived. |
| `OLIVARES_DR_SCHEDULE_INTERVAL` | No | — | How often the scheduled backup runs, as a Go duration. |
| `OLIVARES_DSN` | No | — | Store connection string injected by the Kubernetes operator into the engine it manages. |
| `OLIVARES_DURABLE_BUS_CONFIG` | No | — | Path to the JSON configuration of the durable bus, for at-least-once delivery across replicas. |
| `OLIVARES_ELICITATION_MEDIATOR_CONFIG` | No | — | Путь к JSON-файлу политик медиатора MCP elicitation. Читается только сборками, скомпилированными с тегами `enterprise` и `addon_airs`. Если переменная не задана, медиация остаётся выключенной; файл, который не удаётся прочитать или разобрать, заставляет медиатор отклонять каждый запрос elicitation, пока файл не будет исправлен, а движок перезапущен. |
| `OLIVARES_EMBEDDINGS_BASE_URL` | No | — | Endpoint the openai-compatible embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_DIM` | No | — | Vector dimension the openai-compatible provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_GEO` | No | — | Region or data-residency hint sent to the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_KEY` | No | — | Api key for the openai-compatible embeddings provider. |
| `OLIVARES_EMBEDDINGS_MODEL` | No | — | Embedding model requested from the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_BASE_URL` | No | — | Endpoint the openai embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_BASE_URL` | No | — | Endpoint the openai-compatible embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_DIM` | No | — | Vector dimension the openai-compatible provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_GEO` | No | — | Region or data-residency hint sent to the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_KEY` | No | — | Api key for the openai-compatible embeddings provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_COMPAT_MODEL` | No | — | Embedding model requested from the openai-compatible provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_DIM` | No | — | Vector dimension the openai provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_OPENAI_GEO` | No | — | Region or data-residency hint sent to the openai provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_KEY` | No | — | Api key for the openai embeddings provider. |
| `OLIVARES_EMBEDDINGS_OPENAI_MODEL` | No | — | Embedding model requested from the openai provider. |
| `OLIVARES_EMBEDDINGS_PROVIDER` | No | — | Which embeddings provider is used, pinning one instead of taking the first that is configured. |
| `OLIVARES_EMBEDDINGS_REQUIRE` | No | — | Set to a true value to make a missing or unusable embeddings provider a refusal rather than a degraded index. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_BASE_URL` | No | — | Endpoint the self-hosted embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_DIM` | No | — | Vector dimension the self-hosted provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_GEO` | No | — | Region or data-residency hint sent to the self-hosted provider. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_KEY` | No | — | Api key for the self-hosted embeddings provider. |
| `OLIVARES_EMBEDDINGS_SELF_HOSTED_MODEL` | No | — | Embedding model requested from the self-hosted provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_BASE_URL` | No | — | Endpoint the voyage embeddings provider is called at. |
| `OLIVARES_EMBEDDINGS_VOYAGE_DIM` | No | — | Vector dimension the voyage provider returns, which has to match the index. |
| `OLIVARES_EMBEDDINGS_VOYAGE_GEO` | No | — | Region or data-residency hint sent to the voyage provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_KEY` | No | — | Api key for the voyage embeddings provider. |
| `OLIVARES_EMBEDDINGS_VOYAGE_MODEL` | No | — | Embedding model requested from the voyage provider. |
| `OLIVARES_ENDPOINT` | No | — | Control-plane base URL the Terraform provider talks to, when the provider block does not set one. |
| `OLIVARES_ENGINE` | No | — | Store engine the Kubernetes operator selects for the engine it manages: `sqlite` or `postgres`. |
| `OLIVARES_EVALS_MONITOR_WINDOW` | No | — | Time window the evaluation monitor scores, as a Go duration. |
| `OLIVARES_EVENTING_ALLOW_LOOPBACK` | No | — | Set to a true value to allow loopback destinations. Single-box development only, because the default refusal is what blocks server-side request forgery. |
| `OLIVARES_EVENTING_DISPATCH_INTERVAL` | No | `15s` | How often queued events are dispatched, as a Go duration. `0` disables the pump. |
| `OLIVARES_EVENTING_EGRESS_POLICY` | No | — | Path to the JSON policy that decides which destinations outbound events may reach. A policy that does not parse leaves eventing unwired rather than open. |
| `OLIVARES_EVENTING_RETENTION` | No | `168h` | How long delivered events are kept for replay, as a Go duration. |
| `OLIVARES_EVENTING_SECRET_KEY` | No | — | Key that encrypts eventing subscription signing secrets at rest. |
| `OLIVARES_EXECUTION_ENVIRONMENT_ID` | No | — | Explicit execution-environment reference for this node, read at boot by cmd/olivares/providerprofiles.go. Unset, the engine generates one identity once into `execution-environment-id` in the data directory (0600, atomic exclusive create) and reuses it; set, the value must be 1..256 printable bytes with no whitespace, colon or vertical bar, and a malformed value refuses boot instead of degrading in silence. It is REQUIRED on a topology with only shared state and no node-local data directory: there, without it, profiled session launches stay deny-closed. |
| `OLIVARES_EXTRA_ARGS` | No | — | Extra `serve` arguments appended by the packaged service unit, for operators who configure the daemon through an environment file. |
| `OLIVARES_GROK_HOOK_ACCOUNT` | No | — | Account the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_AGENT` | No | — | Agent identity the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_ORG` | No | — | Organization the Grok Build hook client reports. |
| `OLIVARES_GROK_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the Grok Build hook enforcement point server. Absent mounts nothing; a path given must be readable and valid or startup fails closed. |
| `OLIVARES_GROK_HOOK_TENANT` | No | — | Tenant the Grok Build hook client acts in. |
| `OLIVARES_GROK_HOOK_TOKEN` | No | — | Bearer credential the Grok Build hook client presents to the enforcement point. |
| `OLIVARES_GROK_HOOK_URL` | No | — | Endpoint of the enforcement point the Grok Build hook client calls; unset denies, deny-closed. |
| `OLIVARES_GUARDIAN_SWEEP_INTERVAL` | No | — | How often the guardian sweep runs, as a Go duration. `0` disables it. |
| `OLIVARES_HA_LEADER_GATE` | No | — | Set to `1` to make background loops run only on the elected leader, so a multi-replica deployment does not run them twice. |
| `OLIVARES_HA_LEADER_LABEL` | No | — | Label this replica publishes when it holds leadership, so an operator can route to the leader. |
| `OLIVARES_HITL_CONFIG` | No | — | Path to the JSON configuration of the human-in-the-loop review path. |
| `OLIVARES_HOOK_FIREWALL_CONFIG` | No | — | Path to the JSON configuration of the data-loss firewall that runs inside the hook. Unset leaves that half off. |
| `OLIVARES_HOOK_PEP_ACCOUNT` | No | — | Account the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_AGENT` | No | — | Agent identity the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_CONFIG` | No | — | Path to the JSON configuration of the hook enforcement point server. |
| `OLIVARES_HOOK_PEP_ORG` | No | — | Organization the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_TENANT` | No | — | Tenant the Claude Code hook client reports. |
| `OLIVARES_HOOK_PEP_TOKEN` | No | — | Token the Claude Code hook client presents to the enforcement point. |
| `OLIVARES_HOOK_PEP_URL` | No | — | Base URL of the enforcement point the Claude Code hook client calls. |
| `OLIVARES_INCIDENTLOOP_CONFIG` | No | — | Path to the JSON configuration of the incident close-the-loop subscriber. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_INFERENCE_PROXY_CONFIG` | No | — | Path to the JSON configuration of the governed inference proxy. |
| `OLIVARES_INGEST_TOKEN` | No | — | Bearer token the collector ingest endpoint requires from telemetry senders. |
| `OLIVARES_INSECURE` | No | — | Set to `1` to let the CLI talk to a plaintext or untrusted-TLS endpoint. Local development only. |
| `OLIVARES_KEY_CUSTODY` | No | — | Custody posture required of the audit signing key: whether a raw on-disk key is accepted or a wrapped one is demanded. |
| `OLIVARES_KEY_WRAP_AWS_KEY_ID` | No | — | Key identifier in AWS KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AWS_REGION` | No | — | Region of the AWS KMS key. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_KEY_NAME` | No | — | Key name in Azure Key Vault. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_KEY_VERSION` | No | — | Key version in Azure Key Vault. Unset uses the current version. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_AZURE_VAULT_URL` | No | — | Azure Key Vault URL. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_KEY` | No | — | Fully qualified key version name in Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_TOKEN` | No | — | Token used against Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_GCP_TOKEN_FILE` | No | — | Path to the file holding the token used against Google Cloud KMS. Used by the backend that wraps the signing keys. |
| `OLIVARES_KEY_WRAP_OLD` | No | — | Previous key management backend during a rewrap migration, so keys wrapped by it can still be unwrapped. |
| `OLIVARES_LEDGER_CUSTODY` | No | — | Custody posture required of the ledger checkpoint signer, the ledger counterpart of the audit key posture. |
| `OLIVARES_LEDGER_KMS_AWS_KEY_ID` | No | — | Key identifier in AWS KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AWS_REGION` | No | — | Region of the AWS KMS key. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AWS_SIGNING_ALG` | No | — | Signing algorithm requested from AWS KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_KEY_NAME` | No | — | Key name in Azure Key Vault. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_KEY_VERSION` | No | — | Key version in Azure Key Vault. Unset uses the current version. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_AZURE_VAULT_URL` | No | — | Azure Key Vault URL. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_KEY` | No | — | Fully qualified key version name in Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_TOKEN` | No | — | Token used against Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_KMS_GCP_TOKEN_FILE` | No | — | Path to the file holding the token used against Google Cloud KMS. Used by the backend that signs audit checkpoints. |
| `OLIVARES_LEDGER_SIGNER` | No | — | Off-box checkpoint signer to use: which key management backend signs audit checkpoints instead of a local key. |
| `OLIVARES_LICENSE` | No | — | License document itself, inline, for deployments that cannot mount a file. |
| `OLIVARES_LICENSE_PATH` | No | — | Path to the license document on disk. Takes effect before the inline form. |
| `OLIVARES_LICENSE_PUBKEY` | No | — | Public key the engine verifies the license signature against. |
| `OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS` | No | — | Set to `1` to make live ingest inspect observed references, which costs more per event. |
| `OLIVARES_LOGIN_ENFORCEMENT` | No | — | Аварийный переключатель (break-glass), а не путь к файлу, для принудительного применения правил входа (require-SSO и список разрешённых IP для входа). Читается любой сборкой при запуске движка; изменение вступает в силу после перезапуска. Значения `off`, `0`, `false`, `no` или `disabled` в любом регистре и без учёта окружающих пробелов выбирают аварийный режим оператора; если переменная не задана или содержит любое другое значение, применение следует posture, сохранённой через консоль. Сборка, в которую скомпонован компонент применения, в этом случае перестаёт применять сохранённую posture и записывает предупреждение. Сборка без этого компонента фиксирует тот же осознанный выбор: движок пропускает отказ в запуске, который иначе выдал бы при наличии истории применения и настроенной posture, и добавляет устойчивое событие восстановления оператором при повышении узла. |
| `OLIVARES_LOG_LEVEL` | No | — | Minimum log level the engine emits: `debug`, `info`, `warn` or `error`. |
| `OLIVARES_MCP_TASK_KILLSWITCH_SWEEP` | No | — | How often a running MCP task is re-checked against the kill switch, as a Go duration. |
| `OLIVARES_METRICS_ALLOWED_CIDRS` | No | — | Comma-separated CIDR ranges allowed to scrape the metrics endpoint. |
| `OLIVARES_METRICS_TOKEN` | No | — | Bearer token the metrics endpoint requires. Unset leaves the endpoint unauthenticated behind whatever the listener exposes. |
| `OLIVARES_MODEL_GATEWAY_CHAT_MODE` | Нет | `disabled` | Режим активации синхронной управляемой отправки Chat. Если переменная не задана или равна `disabled`, отправка остаётся выключенной; `development_precheck` явно включает существующую неатомарную предварительную проверку маршрутизации в режиме fail-open, а не жёсткий денежный бюджет. Любое другое значение останавливает запуск. |
| `OLIVARES_MODEL_GATEWAY_PROFILES_CONFIG` | Нет | — | Путь к локальному JSON-файлу с неизменяемыми профилями выполнения шлюза моделей. Если переменная не задана, профили не загружаются; недоступный для чтения или недопустимый файл останавливает запуск. |
| `OLIVARES_NHI_ACTUATORS_CONFIG` | No | — | Path to the JSON configuration of the actuators that act on non-human identities. |
| `OLIVARES_NIS2INCIDENT_CONFIG` | No | — | Path to the JSON configuration of NIS2 incident reporting. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_NOTIFY_CONFIG` | No | — | Path to the JSON list of notification destinations. Secret-bearing, so it stays out of the store. |
| `OLIVARES_NOTIFY_DISPATCH_INTERVAL` | No | — | How often queued notifications are dispatched, as a Go duration. `0` disables the pump. |
| `OLIVARES_OIDC_CLIENT_ID` | Yes | — | OIDC client id for this control plane. Required when the protocol is `oidc`. |
| `OLIVARES_OIDC_CLIENT_SECRET` | Yes | — | OIDC client secret for this control plane. Required when the protocol is `oidc`. |
| `OLIVARES_OIDC_GROUPS_CLAIM` | No | — | ID-token or UserInfo claim carrying group membership. Unset leaves group mapping off. |
| `OLIVARES_OIDC_ISSUER` | Yes | — | OIDC issuer URL. Required when the protocol is `oidc`. |
| `OLIVARES_ONBOARDING_CONFIG` | No | — | Путь к JSON-конфигурации модуля оценки готовности к онбордингу. Читается сборками, скомпилированными с тегом `enterprise`, при каждом выполнении `olivares enterprise readiness`, а не при запуске движка. `{}` выбирает стандартный профиль. Если переменная не задана или файл не удаётся прочитать или разобрать, эта команда сообщает, что готовность к онбордингу не настроена. |
| `OLIVARES_ORCH_CADENCE_INTERVAL` | No | — | How often the orchestration cadence loop runs, as a Go duration. `0` disables it. |
| `OLIVARES_ORCH_DISPATCH_CONFIG` | No | — | Path to the JSON configuration for orchestration dispatch targets. |
| `OLIVARES_ORCH_WORKFLOW_INTERVAL` | No | `15s` | How often the orchestration workflow loop advances waiting runs, as a Go duration. |
| `OLIVARES_ORCH_WORKFLOW_MAX` | No | — | Upper bound on concurrently advancing workflow runs. |
| `OLIVARES_ORCH_WORKFLOW_STEPS_MAX` | No | — | Upper bound on the steps one workflow run may take, which stops a loop from running forever. |
| `OLIVARES_OTA_PUBKEY` | No | — | Public key the engine verifies a downloaded update bundle against. |
| `OLIVARES_OTEL_ENABLED` | No | — | Set to a true value to export traces. Setting an endpoint turns export on as well. |
| `OLIVARES_OTEL_ENDPOINT` | No | — | OTLP endpoint traces are exported to. Falls back to the standard `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| `OLIVARES_OTEL_GENAI_COMPAT` | No | — | Set to a true value to also emit the generative-AI semantic-convention attributes on spans. |
| `OLIVARES_OTEL_INSECURE` | No | — | Set to a true value to export traces over plaintext. Local development only. |
| `OLIVARES_OTEL_PROTOCOL` | No | — | OTLP protocol used for export. Falls back to the standard `OTEL_EXPORTER_OTLP_PROTOCOL`. |
| `OLIVARES_OTEL_SAMPLE_RATIO` | No | — | Fraction of traces sampled, between 0 and 1. |
| `OLIVARES_OTEL_SERVICE_NAME` | No | — | Service name reported on exported traces. |
| `OLIVARES_PDP_CEDAR_FILE` | No | — | Path to the Cedar policy file, for the `cedar` decision point. |
| `OLIVARES_PDP_ENGINE` | No | — | External policy decision point to add on top of the native engine: `cedar`, `opa` or `none`. |
| `OLIVARES_PDP_OPA_PATH` | No | — | Decision path queried under the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_TOKEN` | No | — | Bearer token for the Open Policy Agent endpoint. |
| `OLIVARES_PDP_OPA_URL` | No | — | Base URL of the Open Policy Agent endpoint, for the `opa` decision point. |
| `OLIVARES_PIV_CONFIG` | No | — | Path to the JSON configuration for smart-card privileged login. |
| `OLIVARES_PLUGIN` | No | — | Handshake cookie an out-of-process connector plugin must present. Set by the engine when it launches the plugin, not by the operator. |
| `OLIVARES_POLICY_MAX_STALENESS` | No | — | How stale a cached policy decision may be before it is refused, as a Go duration. |
| `OLIVARES_POLICY_SIGNING_KEY` | No | — | Policy bundle signing key, inline. Prefer the file form. |
| `OLIVARES_POLICY_SIGNING_KEY_FILE` | No | — | Path to the policy bundle signing key. |
| `OLIVARES_POLICY_SIGNING_KEY_WRAPPED_FILE` | No | — | Path to the policy signing key wrapped by a key management service. |
| `OLIVARES_PQC_POSTURE_CONFIG` | No | — | Путь к JSON-конфигурации модуля оценки постквантовой позиции. Читается сборками, скомпилированными с тегом `enterprise`, при каждом выполнении `olivares enterprise pqc-posture`, а не при запуске движка. `{}` выбирает CNSA 2.0 с целевым годом 2033. Если переменная не задана или файл не удаётся прочитать или разобрать, эта команда сообщает, что позиция PQC не настроена. |
| `OLIVARES_PUBLIC_URL` | No | — | The address a browser reaches this console at, as scheme://host[:port]. It is what the startup panel prints and what the WebAuthn relying party is derived from, and it is independent of the listen address. The --public-url flag wins over this variable, and passing that flag empty clears it. Read at start-up only: a change takes a restart. Refused values are reported by field and failure class and are never echoed, and support bundles keep this value redacted. |
| `OLIVARES_RATELIMIT_CONFIG` | No | — | Path to the JSON rate-limit policy the engine applies to its own endpoints. |
| `OLIVARES_RATELIMIT_STORE` | No | — | Where rate-limit counters live, which decides whether limits are per replica or shared. |
| `OLIVARES_RENDER_INSPECTOR_CONFIG` | No | — | Путь к JSON-файлу политик инспектора отрисованного содержимого MCP App. Читается только сборками, скомпилированными с тегами `enterprise` и `addon_airs`. Если переменная не задана, эта проверка остаётся выключенной; файл, который не удаётся прочитать или разобрать, заставляет инспектор отклонять всё отрисованное содержимое, пока файл не будет исправлен, а движок перезапущен. |
| `OLIVARES_REPORTING_CONFIG` | No | — | Path to the JSON configuration of the reporting add-on. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_REPORTING_SCHEDULE_INTERVAL` | No | — | How often scheduled reports are generated, as a Go duration. |
| `OLIVARES_REPORT_CACHE_DIR` | No | — | Directory where generated report artifacts are cached. |
| `OLIVARES_RETENTION_GOVERNOR_CONFIG` | No | — | Путь к JSON-конфигурации модуля управления нормативными минимальными сроками хранения. Читается только сборками, скомпилированными с тегами `enterprise` и `addon_reg`, при запуске движка. Если переменная не задана, минимальный срок хранения не применяется; файл, который не удаётся прочитать или разобрать, устанавливает для каждого класса хранения минимальный срок 100 лет, пока файл не будет исправлен, а движок перезапущен. |
| `OLIVARES_RETENTION_SWEEP_INTERVAL` | No | — | How often the retention sweep deletes data past its retention window, as a Go duration. |
| `OLIVARES_RTBF_DEPTH_CONFIG` | No | — | Путь к JSON-конфигурации координатора криптографического уничтожения (crypto-shred) для RTBF. Читается только сборками, скомпилированными с тегами `enterprise` и `addon_reg`, при запуске движка. Если переменная не задана, криптографическое уничтожение open core остаётся без изменений; файл, который не удаётся прочитать или разобрать, блокирует каждое уничтожение, пока файл не будет исправлен, а движок перезапущен. |
| `OLIVARES_SAML_ACS_URL` | Yes | — | Assertion consumer service URL of this service provider, where the identity provider posts the assertion. |
| `OLIVARES_SAML_EMAIL_ATTRIBUTE` | No | — | Assertion attribute carrying the user's email. Unset tries the common attribute names. |
| `OLIVARES_SAML_GROUPS_ATTRIBUTE` | No | — | Multi-valued assertion attribute carrying group membership. Unset leaves group mapping off. |
| `OLIVARES_SAML_IDP_METADATA_URL` | No | — | Identity-provider metadata URL, from which the SAML endpoints and certificate are read. |
| `OLIVARES_SAML_IDP_SSO_URL` | No | — | Identity-provider single sign-on URL, for the path where metadata is not fetched. |
| `OLIVARES_SAML_SP_CERT_PEM` | No | — | Service-provider encryption certificate in PEM, published as the encryption key descriptor. |
| `OLIVARES_SAML_SP_ENTITY_ID` | Yes | — | Entity id this control plane presents as the SAML service provider. Required when the protocol is `saml`. |
| `OLIVARES_SAML_SP_KEY_PEM` | No | — | Service-provider encryption private key in PEM, which decrypts encrypted assertions. |
| `OLIVARES_SAML_SP_SIGN_CERT_PEM` | No | — | Service-provider signing certificate in PEM, published as the signing key descriptor. |
| `OLIVARES_SAML_SP_SIGN_KEY_PEM` | No | — | Service-provider signing private key in PEM, which signs authentication requests. |
| `OLIVARES_SANDBOX_RUNTIME_CONFIG` | No | — | Path to the JSON configuration of the sandbox runtime that isolates agent execution. |
| `OLIVARES_SECRETREF_AWS_REGION` | No | — | Region used for AWS Secrets Manager references. Falls back to `AWS_REGION` and `AWS_DEFAULT_REGION`. |
| `OLIVARES_SECRETREF_AZURE_API_VERSION` | No | — | Azure Key Vault API version requested. |
| `OLIVARES_SECRETREF_AZURE_TOKEN` | No | — | Token used against Azure Key Vault. |
| `OLIVARES_SECRETREF_AZURE_VAULT_URL` | No | — | Default Azure Key Vault URL for references that do not name one. |
| `OLIVARES_SECRETREF_GCP_ENDPOINT` | No | — | Endpoint override for Google Secret Manager. |
| `OLIVARES_SECRETREF_GCP_PROJECT` | No | — | Default Google Cloud project for Secret Manager references that do not name one. |
| `OLIVARES_SECRETREF_GCP_TOKEN` | No | — | Token used against Google Secret Manager. |
| `OLIVARES_SECRETREF_INFISICAL_ENV` | No | — | Default Infisical environment for references that do not name one. |
| `OLIVARES_SECRETREF_INFISICAL_TOKEN` | No | — | Token used against Infisical. |
| `OLIVARES_SECRETREF_INFISICAL_URL` | No | — | Base URL of the Infisical server secret references resolve against. |
| `OLIVARES_SECRETREF_INFISICAL_WORKSPACE_ID` | No | — | Default Infisical workspace for references that do not name one. |
| `OLIVARES_SECRETREF_K8S_APISERVER` | No | — | Kubernetes API server secret references resolve against. |
| `OLIVARES_SECRETREF_K8S_CA_FILE` | No | — | Path to the certificate authority bundle used to verify the Kubernetes API. |
| `OLIVARES_SECRETREF_K8S_TOKEN_FILE` | No | — | Path to the service-account token file used against the Kubernetes API. |
| `OLIVARES_SECRETREF_VAULT_ADDR` | No | — | Address of the HashiCorp Vault server secret references resolve against. Falls back to `VAULT_ADDR`. |
| `OLIVARES_SECRETREF_VAULT_NAMESPACE` | No | — | Vault namespace secret references resolve in. Falls back to `VAULT_NAMESPACE`. |
| `OLIVARES_SECRETREF_VAULT_TOKEN` | No | — | Token used against HashiCorp Vault. |
| `OLIVARES_SECRET_STORE_KEY` | No | — | Key that encrypts operator secrets held in the store. |
| `OLIVARES_SERVERTOOL_EGRESS_CONFIG` | No | — | Путь к JSON-файлу разрешений (grants) шлюза egress для серверных инструментов провайдера (веб-поиск, веб-загрузка, выполнение кода) во встроенном прокси. Читается только сборками, скомпилированными с тегами `enterprise` и `addon_airs`. Если переменная не задана, эти инструменты остаются в режиме только наблюдения; файл, который не удаётся прочитать или разобрать, запрещает каждый распознанный серверный инструмент с egress, пока файл не будет исправлен, а движок перезапущен. |
| `OLIVARES_SERVER_URL` | No | — | Base URL of the control plane the CLI talks to, when `--server` is not given. |
| `OLIVARES_SESSIONS_MANAGED_STOP_ADMISSION_TIMEOUT` | No | `10s` | Максимальное время допуска управляемого запроса Stop, заданное положительной длительностью Go с единицей измерения. Читается при запуске; недопустимое или неположительное значение не позволяет запустить приложение. Не ограничивает время завершения процесса. |
| `OLIVARES_SESSION_BUDGET_AVAILABILITY` | No | — | Whether session budget enforcement is required, and what happens when the budget service cannot answer. |
| `OLIVARES_SESSION_CONTEXT_AVAILABILITY` | No | — | Whether session context governance is required, and what happens when the context service cannot answer. |
| `OLIVARES_SESSION_KILLSWITCH_SWEEP` | No | `15s` | How often an active session is re-checked against the kill switch, as a Go duration. `0` leaves only the check at launch. |
| `OLIVARES_SESSION_PEP_TOKEN_FILE` | No | — | Path to the file holding the token the session enforcement point requires. |
| `OLIVARES_SESSION_PEP_URL` | No | — | Base URL of the policy enforcement point a governed agent session calls before acting. |
| `OLIVARES_SESSION_RUNTIME_BASE_URL` | No | — | Base URL the launched session runtime calls back to. |
| `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` | No | `claude` | Executable the session runtime launches. |
| `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | No | — | Pinned official Codex executable the session runtime may operate. Setting it REGISTERS that driver on this node; unset, codex profiles stay observable and not launchable. |
| `OLIVARES_SESSION_RUNTIME_GROK_BIN` | No | — | Pinned official Grok executable the session runtime may operate. Setting it REGISTERS that driver on this node; unset, grok profiles stay observable and not launchable. |
| `OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` | No | — | Явно заданный официальный исполняемый файл OpenCode, который может использовать среда выполнения сессий. При его указании на этом узле регистрируется драйвер OpenCode ACP. Если он не задан, профили OpenCode остаются доступными для наблюдения, но их нельзя запускать. Другие драйверы провайдеров не изменяются. |
| `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` | No | — | Path to the file holding the session runtime's credential, refreshed by rotation. |
| `OLIVARES_SESSION_RUNTIME_TOKEN_TTL` | No | `15m` | Lifetime of a minted session runtime credential, as a Go duration. |
| `OLIVARES_SESSION_RUNTIME_WIF` | No | — | Whether the session runtime takes its credential from workload identity federation instead of a token file. |
| `OLIVARES_SESSION_RUNTIME_WIF_RULE` | No | — | Which federation rule the session runtime exchanges its workload identity under. |
| `OLIVARES_SIEM_FORWARD_INTERVAL` | No | — | How often signed ledger records are forwarded to the configured SIEM, as a Go duration. |
| `OLIVARES_SOURCES_CONFIG` | No | — | Path to the JSON file that wires real observation sources and identity roster providers before the engine starts. |
| `OLIVARES_SSO_PROTOCOL` | No | — | Single sign-on protocol to wire: `oidc` or `saml`. Unset means no federation, and the endpoints report it rather than half-wiring one. |
| `OLIVARES_SSO_SECRET_KEY` | No | — | Key that encrypts the federation client secret and service-provider private keys at rest. |
| `OLIVARES_TARGET_BINDING_KEY` | No | — | Key that binds an orchestration target to this deployment, inline. Prefer the file form. |
| `OLIVARES_TARGET_BINDING_KEY_FILE` | No | — | Path to the orchestration target binding key. |
| `OLIVARES_TENANT` | No | — | Default tenant for CLI commands, when `--tenant` is not given. |
| `OLIVARES_THREATINTEL_CONFIG` | No | — | Path to the JSON configuration of threat-intelligence ingest. Read by builds compiled with the `enterprise` tag. |
| `OLIVARES_THREATINTEL_SIGNING_KEY` | No | — | Signing key for threat-intelligence bundles the engine publishes. |
| `OLIVARES_TOKEN` | No | — | API token the CLI authenticates with, when `--token` is not given. |
| `OLIVARES_TOOL_PIN_CONFIG` | No | — | Путь к необязательной JSON-конфигурации (`require_pin_approval`) хранилища закреплений инструментов MCP, которое сборки, скомпилированные с тегом `enterprise`, запускают независимо от того, задана ли эта переменная. Читается при запуске движка. Если переменная не задана, сохраняется доверие при первом использовании (trust on first use); файл, который не удаётся прочитать или разобрать, требует одобрения оператора для неизвестных инструментов, пока файл не будет исправлен, а движок перезапущен. |
| `OLIVARES_UPDATE_CHANNEL` | No | — | Release channel the update check asks for, such as `stable`. |
| `OLIVARES_UPDATE_ENDPOINT` | No | — | Base URL the update check queries. Unset leaves the update check off. |
| `OLIVARES_UPGRADE_TOKEN` | No | — | Download token `olivares upgrade` presents when fetching a build from a credentialed repository. |
| `OLIVARES_VECTOR_API_KEY` | No | — | API key for the external vector index. |
| `OLIVARES_VECTOR_BACKEND` | No | — | Which vector index backs knowledge search. Unset keeps the in-process index, which is the air-gapped default. |
| `OLIVARES_VECTOR_DIM` | No | — | Vector dimension of the index, which has to match the embeddings model. |
| `OLIVARES_VECTOR_DSN` | No | — | Connection string for the external vector index. |
| `OLIVARES_VECTOR_NAMESPACE` | No | `knowledge_ann` | Table or collection the vector index writes to. |
| `OLIVARES_VECTOR_TIMEOUT` | No | — | Per-request timeout for the vector index, as a Go duration. |
| `OLIVARES_VOICE_CALL_CONFIG` | No | — | Path to the JSON configuration of the inbound voice webhook. |
| `OLIVARES_VOICE_DISPATCH_CONFIG` | No | — | Path to the JSON configuration of outbound voice dispatch. |
| `OLIVARES_WEBAUTHN_ORIGINS` | No | — | Comma-separated origins accepted for WebAuthn ceremonies. |
| `OLIVARES_WEBAUTHN_RPID` | No | — | WebAuthn relying-party id for the privileged-login flow. It has to match the site's registrable domain. |
| `OLIVARES_WEBAUTHN_RP_NAME` | No | — | Display name of the WebAuthn relying party, as shown by the authenticator. |
| `OLIVARES_WIF_BASE_URL` | No | — | Endpoint the workload identity exchange is performed against. |
| `OLIVARES_WIF_REFRESH_SLACK` | No | `60s` | How long before expiry a federated credential is refreshed, as a Go duration. |
| `OLIVARES_WIF_SPIFFE_SOCKET` | No | — | Path to the SPIFFE workload API socket the engine fetches its identity from. |
| `OLIVARES_WIF_TRUST_DOMAIN` | No | — | SPIFFE trust domain accepted for workload identity. |
| `OLIVARES_WORK_OUTBOX_INTERVAL` | No | — | How often the work-kernel outbox is drained, as a Go duration. `0` disables the pump. |
| `OLIVARES_WORK_RUN_REF` | No | — | Run reference the engine passes to a launched work session. Set by the engine per run, not by the operator. |
| `OLIVARES_WORK_SESSION_ID` | No | — | Session reference the engine passes to a launched work session. Set by the engine per run, not by the operator. |
| `OLIVARES_WORK_TOKEN` | No | — | Scoped token the engine passes to a launched work session. Set by the engine per run, not by the operator. |

### Variable families

These prefixes name families whose member variables are built at runtime — the per-provider and per-backend keys the engine composes from a provider name. The concrete members it composes are in the table above.

| Prefix | Required | Default | What it configures |
| --- | --- | --- | --- |
| `OLIVARES_AUDIT_ARCHIVE_` | No | — | Family prefix for the audit archive settings listed above. |
| `OLIVARES_CODEX_HOOK_` | No | — | Family prefix for the Codex hook client and server settings listed above. |
| `OLIVARES_DR_OFFSITE_` | No | — | Family prefix for the offsite replication settings listed above. |
| `OLIVARES_EMBEDDINGS` | No | — | Family stem for the unprefixed embeddings settings, which configure the OpenAI-compatible provider. |
| `OLIVARES_EMBEDDINGS_` | No | — | Family prefix from which the per-provider embeddings keys are built, by appending the provider name and then the setting. |
| `OLIVARES_GROK_HOOK_` | No | — | Family prefix for the Grok Build hook client and server settings listed above. |
| `OLIVARES_HOOK_PEP_` | No | — | Family prefix for the Claude Code hook client and server settings listed above. |
| `OLIVARES_KEY_WRAP` | No | — | Family stem naming the key management backend that wraps signing keys. |
| `OLIVARES_KEY_WRAP_` | No | — | Family prefix from which the per-backend key-wrapping keys are built. |
| `OLIVARES_LEDGER_KMS_` | No | — | Family prefix from which the per-backend ledger signer keys are built. |
| `OLIVARES_OIDC_` | No | — | Family prefix for the OIDC federation settings listed above. |
| `OLIVARES_OTEL_` | No | — | Family prefix for the trace export settings listed above. |
| `OLIVARES_SAML_` | No | — | Family prefix for the SAML federation settings listed above. |
| `OLIVARES_SESSION_RUNTIME_` | No | — | Family prefix for the session runtime settings listed above. |
| `OLIVARES_VECTOR_` | No | — | Family prefix for the vector index settings listed above. |
| `OLIVARES_WIF_` | No | — | Family prefix for the workload identity federation settings listed above. |
| `OLIVARES_WORK_` | No | — | Family prefix for the per-run values the engine passes into a launched work session. |

<!-- END GENERATED olivares-env-reference -->

## Выбор хранилища

Движок выбирает движок своего хранилища из `--engine`.

| Движок | Когда использовать | Примечания |
| --- | --- | --- |
| `sqlite` (по умолчанию) | Единый бинарник, один узел, air-gapped-установки. | Чисто-Go встроенное хранилище; ноль внешних зависимостей. Без `--dsn` файл хранилища лежит в каталоге данных. |
| `postgres` | Мульти-тенантные и scale-out-развёртывания. | Добавляет изоляцию тенантов через row-level-security. Требует роль приложения с минимальными привилегиями. |

SQLite — это значение по умолчанию, и ему не нужен внешний сервис. Выбор `postgres` включает страховку row-level-security, изолирующую тенантов: движок **отказывается запускаться** против роли Postgres-суперпользователя или роли `BYPASSRLS`, если эта защита явно не переопределена, потому что такая роль отключила бы страховку изоляции тенантов. Оверлей Compose Postgres провижинит роль приложения с минимальными привилегиями при первой инициализации, так что эта страховка реальна.

:::tip[Хранилище по умолчанию намеренно скучное]
SQLite здесь не игрушечное значение по умолчанию. Это готовое-к-air-gap, без-зависимостей хранилище для одноузловой топологии, и это то хранилище, которое запускает развёртывание Docker Compose одной командой. Переходите на Postgres, когда вам нужна мульти-тенантная изоляция или горизонтальное масштабирование, не раньше. См. [self-hosting](/ru/how-to/self-hosting/) и [обзор архитектуры](/ru/explanation/architecture/overview/).
:::

## Интервал чекпойнтов аудита

Журнал аудита является append-only, hash-chained и заякорен подписанными Ed25519 чекпойнтами. `--checkpoint-interval` управляет тем, как часто записывается подписанный чекпойнт поверх цепочки каждого тенанта (по умолчанию `1h`; `0` отключает чекпойнты). Финальный чекпойнт при завершении записывается до закрытия хранилища, так что цепочка заякорена как при чистом завершении, так и по интервалу. Подписанный путь экспорта и пересылки описан в [пересылка аудита в Splunk](/ru/how-to/forward-audit-to-splunk/).

## Безопасные значения по умолчанию

Это позиции, действующие без какой-либо конфигурации, кроме `serve`. Это стойка безопасности продукта по умолчанию, а не опциональное ужесточение.

| Область | По умолчанию | Что это значит |
| --- | --- | --- |
| Учётные данные | Не поставляются | Имени пользователя или пароля по умолчанию не существует. При первой загрузке без пользователей движок выпускает одноразовый setup-токен и печатает его только в стандартный вывод — никогда в логи. |
| Настройка при первом запуске | Одноразовый токен | Администратор создаёт первого пользователя этим токеном, затем входит в систему. Токен показывается один раз и одноразов. |
| Транспорт | TLS включён | HTTP и gRPC по умолчанию обслуживаются поверх TLS; самоподписанный сертификат генерируется в каталоге данных, если ни один не предоставлен, и логируются как его отпечаток сертификата, так и его значение `--pin-sha256`. |
| Адрес привязки | Loopback | `--listen` и `--grpc-listen` по умолчанию привязаны к `127.0.0.1`. Движок привязывается к локальному хосту, пока вы намеренно не опубликуете его. |
| Режим открытого текста | Выкл | `--insecure` — единственный способ выдавать открытый текст, и путь gRPC падает закрыто, а не деградирует. Предназначен только для локальной разработки. |
| Демо-сидинг | Выкл | `--seed-demo` выключен по умолчанию и отказывается от любой не-loopback-привязки, потому что он выпускает демо-администратора с публичным паролем. |
| Дом телеметрии | Выкл | Движок не звонит домой: канала телеметрии-к-вендору нет, и ничего не отправляется как побочный эффект работы. Исходящие соединения существуют к источникам, которые вы настраиваете, плюс `olivares upgrade`, когда вы его запускаете, — он обращается к каналу обновлений, если `--endpoint` или `--bundle` не указывают в другое место. Именно это делает возможной [air-gapped-установку](/ru/how-to/air-gap-install/) с нулевым egress. |

:::caution[Loopback по умолчанию, выставлен по намерению]
Loopback-привязки по умолчанию означают, что движок недостижим вне хоста, пока вы их не измените. Когда вы публикуете его — например, отобразив хост-порт в Docker Compose — это намеренное решение оператора, и TLS уже включён, чтобы защитить его. Не сочетайте опубликованную привязку с `--insecure`.
:::

### Первая загрузка на практике

При свежей установке движок печатает блок `FIRST-BOOT SETUP` в стандартный вывод, содержащий одноразовый setup-токен. Администратор использует его, чтобы создать первого пользователя, затем аутентифицируется. Под Docker Compose токен читается из логов контейнера:

```sh
docker compose -f deploy/compose/docker-compose.yml up -d
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
# затем откройте https://localhost:8443 (самоподписанный TLS по умолчанию)
```

Setup-эндпойнт и login-эндпойнт являются частью контракта OpenAPI продукта; см. [Справочник API](/reference/api/). Непрозрачная (opaque) модель токенов сессии и API-ключей, стоящая за ними, описана в [модели безопасности](/ru/explanation/security/security-model/).

## Что эта страница не покрывает

Это проверенная, общая поверхность конфигурации. Она **не** перечисляет каждый продвинутый флаг для мультиузловых и mutual-TLS-топологий — они относятся к распределённым и air-gapped-развёртываниям, описанным в [обзоре архитектуры](/ru/explanation/architecture/overview/) и перечисленным полностью в [Справочнике CLI](/ru/reference/cli/). Там, где настройка находится на стадии проектирования или специфична для топологии, она документирована там, а не представлена здесь как стабильная ручка.

О границах того, что продукт наблюдает, и о том, где покрытие разбито на уровни, читайте [честность и ограничения](/ru/start/honesty-and-limits/).
