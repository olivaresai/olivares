---
title: "Управляемые данные для Claude"
description: "Предоставьте Claude Code содержимое Drive или S3 через семантическую базу знаний и endpoint извлечения MCP под управлением идентичности, уровня допуска, ACL и области источника."
sidebar:
  order: 7
---

Этот путь позволяет Claude Code задавать вопросы по **вашему** содержимому Google
Drive или S3, не превращая Olivares в AI gateway. Control plane загружает
содержимое в управляемую базу знаний, записывает происхождение каждого документа
и предоставляет через MCP только инструменты извлечения:

| По умолчанию | Значение |
|---|---|
| Семантическая база знаний | `embed_policy=model_backed`; до ingest `/status` должен показывать `retrieval_semantic=true`. |
| Явный fallback | Если семантический embedder не настроен, создание/ingest базы знаний отклоняется вместо того, чтобы выдавать векторы локальных хешей за семантические. |
| Гейт с учётом ACL | Запрашивающий агент должен разрешаться в привязанную идентичность с достаточным `attr_clearance` и совпадающими ACL групп. |
| Область источника | Привяжите базу знаний к агенту Claude Code; субъекты вне области получают отказ deny-closed. |
| Честный live-режим | Ответ live-коннектора содержит `source_mode=live`; статические экспорты остаются `source_mode=export` и никогда не представляются как live. |

## 1. Сохраните учётные данные источника

Храните учётные данные live-источника в runtime-хранилище секретов. Конфигурация
источника должна ссылаться на них как `store:<name>`, но никогда не содержать их
inline.

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name s3/prod-runbooks-read \
  --value-file /run/secrets/s3-prod-runbooks-read
```

Для Google Drive сохраните bearer/refresh-данные OAuth, которые развёртывание
использует для доступа к Drive только для чтения, под другим именем секрета.

## 2. Создайте конфигурацию управляемого RAG

Для S3:

```sh
olivares quickstart governed-rag \
  --data-dir /var/lib/olivares \
  --tenant-id ten_... \
  --source s3 \
  --source-name prod-runbooks-live \
  --bucket prod-runbooks \
  --prefix claude/ \
  --credential-ref store:s3/prod-runbooks-read \
  --mcp-issuer https://idp.example.com/ \
  --mcp-jwks-url https://idp.example.com/.well-known/jwks.json
```

Для Google Drive используйте `--source gdrive --drive-id <shared-drive-id>` и
ссылку на учётные данные Drive.

Команда записывает:

| Файл | Назначение |
|---|---|
| `sources.json` | Регистрирует источник содержимого в `documents[]` с `mode=live`. |
| `agent-gateway.json` | Включает сервер ресурсов MCP с `retrieval.enabled=true`. |
| `bootstrap-after-login.sh` | Создаёт семантическую базу знаний, загружает live-источник, привязывает агента и добавляет привязку области источника. |

Если команда предупреждает, что `retrieval_semantic=false`, сначала настройте
`OLIVARES_EMBEDDINGS_*`. База знаний на основе модели намеренно отказывается от
ingest, когда доступен только fallback локального хеша.

## 3. Запустите с созданной конфигурацией

```sh
OLIVARES_SOURCES_CONFIG=/var/lib/olivares/quickstart/governed-rag/sources.json \
OLIVARES_AGENT_GATEWAY_CONFIG=/var/lib/olivares/quickstart/governed-rag/agent-gateway.json \
olivares quickstart --data-dir /var/lib/olivares
```

Если это новая установка, завершите первоначальную настройку консоли. Затем
запустите bootstrap-скрипт с токеном администратора:

```sh
OLIVARES_TOKEN=<admin-token> \
OLIVARES_TENANT=ten_... \
/var/lib/olivares/quickstart/governed-rag/bootstrap-after-login.sh
```

## 4. Требование к идентичности

Гейт извлечения читает сведения об идентичности из графа roster/SCIM. Прежде чем
Claude Code сможет извлекать ограниченное содержимое, привязанная идентичность
должна существовать:

| Сведение об идентичности | Пример |
|---|---|
| Subject токена агента / `agent_ref` | `claude-code-governed` |
| Привязанная идентичность NHI | `agent:claude-code-governed` |
| Метаданные уровня допуска | `attr_clearance=confidential` или выше |
| Членство в группе | `group:engineering`, совпадающая с ACL документа |

Если у агента нет идентичности, уровня допуска или совпадающей группы,
ограниченные фрагменты не возвращаются. Если агент не привязан к базе знаний
областью источника, вызов извлечения MCP отклоняется deny-closed.

## 5. Подключите Claude Code к MCP

Настройте Claude Code на URL защищённого ресурса, который выводит quickstart,
обычно:

```text
http://127.0.0.1:8446/mcp
```

Токен доступа, предъявляемый этому серверу ресурсов MCP, должен содержать:

| Claim/control | Требуемое значение |
|---|---|
| `iss` | Издатель, настроенный через `--mcp-issuer`. |
| `sub` | Внешний id агента, например `claude-code-governed`. |
| Scope | `knowledge:retrieval:read`. |
| Audience/resource | URL ресурса MCP, настроенный в `agent-gateway.json`. |

## 6. Проверьте

Запустите эталонную E2E-демонстрацию:

```sh
task demo:governed-rag
```

Она проверяет семантический статус, происхождение live-источника, разрешённое
извлечение в области, отсутствие извлечения при низком уровне допуска, отказ
вне области и `source_mode=live` в результате MCP.

Для существующего развёртывания также проверьте настоящий документ:

```sh
curl -sk "$OLIVARES_BASE_URL/v1/m/knowledge/kbs/$KB_ID/documents" \
  -H "Authorization: Bearer $OLIVARES_TOKEN" \
  -H "X-Olivares-Tenant: $OLIVARES_TENANT"
```

Каждый документ, загруженный из live-источника, должен показывать
`source_mode: "live"`. Если указано `export`, база знаний была загружена из
файла экспорта, и операторам её следует описывать именно так.
