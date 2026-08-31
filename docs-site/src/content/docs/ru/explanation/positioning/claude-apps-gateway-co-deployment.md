---
title: Совместное развёртывание Claude apps gateway и Olivares AI
description: >-
  Как запустить self-hosted Claude apps gateway от Anthropic и позволить
  Olivares AI управлять им как ещё одной корпоративной поверхностью:
  инвентаризация, постура, ingest аудита, корреляция OTLP и endpoint
  gateway-протокола фазы 1.
sidebar:
  order: 9
---

## Что такое Claude apps gateway

[Claude apps gateway](https://code.claude.com/docs/en/claude-apps-gateway) от
Anthropic — self-hosted сервис, поставляемый внутри бинарника `claude`, начиная
с v2.1.195. Запустите его командой
`claude gateway --config gateway.yaml` и используйте PostgreSQL как backend.
Он ставит вход OIDC перед Amazon Bedrock, Claude Platform on AWS, Google Cloud
Agent Platform, Microsoft Foundry или Anthropic API, чтобы разработчики
использовали сессии корпоративного IdP вместо локальных учётных данных
провайдера. Его `gateway.yaml` сопоставляет группы IdP со списками разрешённых
моделей и управляемыми настройками, а Admin API лимитов расходов может
ограничивать расходы пользователя, группы или организации. Gateway
распределяет телеметрию через OTLP и выводит однострочные события аудита JSON.
В [объявлении](https://claude.com/blog/introducing-the-claude-apps-gateway) от
29 июня 2026 года Anthropic описывает его как инфраструктуру gateway для
Claude Code, поставляемую самой Anthropic.

## Запустите его. Olivares им управляет.

Если вы уже используете gateway Anthropic или планируете его использовать,
сохраните его. Доктрина — **«и», а не «или»**: gateway Anthropic отвечает за
gateway-сессию Claude Code, доступ к моделям и маршрут к upstream, а Olivares AI
делает это развёртывание управляемой поверхностью в более широком control plane.

Коннектор `claude-apps-gateway` инвентаризирует `gateway.yaml`: издателя, списки
разрешённых моделей для групп IdP, постуру администрирования расходов, назначения
OTLP и upstream. Он поднимает находки постуры для важных оператору governance
состояний конфигурации и загружает JSON-события аудита gateway, чтобы отказы,
mint сессий и записи вывода попадали в журнал аудита с обнаружением вмешательства
(tamper-evident).
Направьте OTLP fan-out gateway в приёмник OTLP Olivares, и сигнал `session.id`
можно будет коррелировать с управляемыми записями runtime сессий. При этом
Olivares хранит структурные данные, а не payload промптов.

## Документированные ограничения

Ниже процитированы решения об области из документации Anthropic по состоянию на
2026-07-03. Это заявления об области, а не дефекты; они определяют границу
совместного развёртывания.

| Возможность | Статус | Примечания |
|---|---|---|
| SAML, LDAP и другая аутентификация не через OIDC | Не поддерживается. | Только OIDC. При необходимости поставьте перед gateway мост OIDC |
| Multi-tenant (несколько издателей OIDC) | Не поддерживается. | Один издатель на gateway. Запускайте отдельные экземпляры |
| Admin UI | Отсутствует. | Конфигурация хранится в YAML; для изменения выполните повторное развёртывание |
| Helm chart | Отсутствует. | Gateway работает как стандартный stateless Deployment |
| CI pipelines | Нет потока service token для автоматических pipeline без оператора |  |
| OTLP/gRPC | Не поддерживается. | Только OTLP по HTTP |
| Windows server | Не поддерживается. | Развёртывайте на Linux |
| Каталог моделей | Только модели Claude | gateway переводит идентификаторы Claude для каждого upstream |

## Что Olivares добавляет рядом

Olivares не устраняет эти ограничения gateway Anthropic. Он добавляет рядом
отсутствующую плоскость governance.

| Ограничение gateway Anthropic | Соседняя возможность Olivares |
|---|---|
| SAML, LDAP и другая аутентификация не через OIDC | Для консоли и плоскости governance Olivares страница [идентичности SSO/SCIM](/ru/how-to/connectors/sso-scim-identity/) документирует федерацию OIDC/SAML, а [архитектура IdP](/ru/explanation/architecture/where-it-fits-with-your-idp/) отображает людей и агентов в roster SSO/SCIM и SPIFFE/WIF. Это не добавляет SAML в gateway Anthropic: оставьте его только с OIDC или поставьте перед ним мост OIDC. |
| Multi-tenant (несколько издателей OIDC) | [Multi-tenant control plane](/ru/reference/modules/xx-multi-tenancy/) Olivares ограничивает сущности, находки, сессии и журнал аудита тенантом, а в multi-tenant развёртываниях использует RLS PostgreSQL. Запускайте отдельный экземпляр gateway на издателя и управляйте каждым как самостоятельной поверхностью; не считайте один gateway Anthropic multi-issuer. |
| Admin UI | Web-консоль Olivares — слой представления над тем же API, который описан [модулем XIX](/ru/reference/modules/xix-api-manage-as-code/), а документы идентичности показывают live UI **Identity & NHI -> SSO & SCIM**. Это административная консоль control plane, а не UI-редактор `gateway.yaml` Anthropic. |
| Helm chart | Olivares поставляет собственное [развёртывание Kubernetes с Helm](/ru/tutorials/getting-started/kubernetes/) и отдельный оператор Kubernetes. Оно развёртывает control plane Olivares и не претендует на упаковку gateway Anthropic. |
| CI pipelines | Автоматизация Olivares может использовать opaque, отзывные и привязанные к тенанту API-токены через [manage-as-code](/ru/how-to/manage-as-code/). Для управляемых учётных данных runtime и развёртывания брокер WIF/SPIFFE создаёт короткоживущие учётные данные. Это отдельно от gateway Anthropic, чьи собственные рекомендации для CI остаются прямым доступом к провайдеру, если только вы намеренно не используете proxy endpoint Olivares ниже. |
| OTLP/gRPC | Приёмник `claude` Olivares принимает обычные пути приёмника OTLP, используемые [OpenTelemetry GenAI](/ru/how-to/connectors/otel-genai/), включая HTTP и gRPC. Gateway Anthropic по-прежнему отправляет OTLP/HTTP; другие управляемые агенты могут напрямую использовать gRPC, а полученные события — поступать в криптографический журнал аудита и [пакеты доказательств compliance](/ru/reference/modules/xiii-compliance/). |
| Windows server | Возможности Windows server здесь не заявляются. Запускайте серверные компоненты на Linux, в контейнерах или Kubernetes, а endpoints разработчиков управляйте через телеметрию, hooks и доказательства коннекторов. |
| Каталог моделей | [Модуль X](/ru/reference/modules/x-models/) управляет estate моделей/провайдеров разных производителей: Claude, OpenAI, Gemini и локальным выводом. Коннектор Bedrock добавляет наблюдаемость использования/стоимости Bedrock и Guardrails. Gateway Anthropic остаётся только для Claude, а Olivares управляет более широким estate, включая постуру Codex через [governance subscription-auth](/ru/explanation/positioning/governing-subscription-authed-agents/). |

## Надмножество протокола, фаза 1

Anthropic публикует протокол gateway и приглашает сторонние реализации. Proxy
вывода Olivares реализует надмножество фазы 1, описанное инженерным контрактом
протокола apps-gateway: discovery OAuth, авторизацию устройств RFC 8628, polling
токенов через seam учётных данных сессий после аутентифицированного согласования,
доставку управляемых настроек единым документом с ETag, форму списка лимитов
расходов только для чтения и `GET /protocol`.

Descriptor сам документирует расхождения: управляемые настройки работают в
режиме единого документа, заголовок версии — `x-olivares-version`, маршруты
write/effective/audit лимитов расходов возвращают соответствующие спецификации
ответы `501`, а Olivares сохраняет своё более богатое отображение отказов
бюджета и добавляет `x-should-retry: false`. Фаза 1 не поставляет callback OIDC
Anthropic и браузерную страницу `/device`, правила слияния управляемых настроек
по группам, пути записи лимитов расходов, `count_tokens` или атрибуцию заголовка
`x-claude-code-session-id`.

## Выбор топологии

- **Только gateway.** Достаточно для организации OIDC с одним издателем,
  использующей только Claude, готовой управлять YAML и повторными
  развёртываниями и удовлетворённой собственными лимитами расходов gateway,
  OTLP fan-out и выводом аудита JSON.
- **Gateway + Olivares.** Рекомендуемое совместное развёртывание, когда
  Claude Code входит в регулируемый estate: сохраните gateway Anthropic,
  добавьте коннектор `claude-apps-gateway`, направьте OTLP в Olivares и храните
  полученную картину постуры, runtime и доказательств в control plane.
- **Proxy Olivares как endpoint gateway-протокола.** Используйте, если вы
  намеренно хотите, чтобы proxy вывода Olivares предоставлял поверхность
  gateway-протокола фазы 1. Это полезно, когда поставляемого подмножества
  достаточно; оно не является полной заменой браузерного потока OIDC gateway
  Anthropic или администрирования расходов через пути записи.
