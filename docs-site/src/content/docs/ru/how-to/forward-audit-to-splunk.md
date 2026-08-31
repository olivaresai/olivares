---
title: Форвардинг в Splunk (поставьте Universal Forwarder + чтение хвоста)
description: >-
  Доставьте находки governance и журнал аудита control plane с обнаружением подделки
  в Splunk, читая хвост файла через Universal Forwarder — без нативного эмиттера
  Splunk-to-Splunk. Честно о том, какой поток какой.
---

Вы можете доставить данные Olivares AI в Splunk **уже сегодня**, не дожидаясь
нативного коннектора: запишите данные в файл и направьте на него **Splunk
Universal Forwarder (UF)**. UF берёт на себя переход Splunk-to-Splunk (S2S) до
вашего индексатора.

:::caution[Нативного эмиттера Splunk S2S нет]
Olivares AI **не** реализует проприетарный протокол форвардера S2S от Splunk.
Нативный эмиттер S2S отнесён в post-v1. Поддерживаемые постуры — это
**форвардинг чтением хвоста файла** (UF читает хвост файла, который пишет
Olivares), **pull-экспорт** (для WORM-архивации и офлайн-переверификации) и
**HTTP push через Splunk HEC** — включая, начиная с работ по SIEM-интеропу, push
**самого журнала** через приёмник событий
([отправка в вашу SIEM](/ru/how-to/cookbook/push-to-siem/)). Эта страница
документирует пути файла и pull; рецепт охватывает push.
:::

Существуют **два разных потока**, и это не одно и то же. Выбирайте осознанно:

| Поток | Что это | Способы в Splunk |
|---|---|---|
| **Governance / находки** | поток уведомлений, который маршрутизирует модуль IX (находки по здоровью, расходам, безопасности, compliance) | выходной коннектор `filelog` дописывает его в файл; или `splunkhec` его пушит; или [приёмник событий](/ru/how-to/cookbook/push-to-siem/), подписанный на `finding.reported` |
| **Журнал аудита с обнаружением подделки** | append-only, hash-chained, подписанный аудиторский след | **pull**-экспорт `GET /v1/audit/export` (эта страница); или **push**-насос — приёмник событий, подписанный на `audit.recorded`, доставка не менее одного раза. Нативного *файлового* приёмника нет; материализуйте файл запланированным экспортом ниже |

## Поток A — находки, через коннектор `filelog`

Выходной коннектор `filelog` дописывает поток уведомлений/находок **по одной
записи на строку** в файл (или `stdout`/`stderr`), хвост которого может читать
UF. Настройте назначение уведомлений вида `filelog` со следующими полями:

| Поле | Значение |
|---|---|
| `path` | цель дописывания: путь к файлу или `stdout`/`stderr`/`-` |
| `format` | построчный формат: `json` \| `cef` \| `leef` \| `syslog` \| `otlp` \| `otlp_envelope` \| `ocsf` \| `asim` (по умолчанию `json`) |
| `hostname` | поле `HOSTNAME` syslog (для формата `syslog`) |
| `fsync` | сброс каждой записи на диск (долговечность для WORM-копии; медленнее) |

Для Splunk подходят оба варианта: `format: json` (богатые поля) либо
`format: cef`/`syslog` (построчные форматы, которые Splunk разбирает нативно).
Файл открывается только на дозапись, поэтому тот же файл служит ещё и
неизменяемой внешней копией при размещении на WORM-хранилище.

:::note[`filelog` несёт находки, а не подписанный журнал]
Коннектор `filelog` форвардит поток **находок** — он никогда не видит
журнал аудита с обнаружением подделки. Чтобы форвардить проверяемый журнал,
используйте Поток B.
:::

### Готовая альтернатива: Splunk HEC

Если вы предпочитаете пушить по HTTP, а не читать хвост файла, коннектор
`splunkhec` отправляет тот же поток находок в HTTP Event Collector Splunk
(`/services/collector`) с заголовком `Authorization: Splunk <token>` — готовый
HTTP-путь, всё ещё не S2S и всё ещё поток находок, а не журнал.

## Поток B — журнал с обнаружением подделки, через pull-экспорт

Журнал аудита выставляется как **аутентифицированный pull-экспорт**, а не файл,
который движок пишет сам по себе. Каждая запись несёт поля целостности цепочки
(`seq`, `prev_hash`, `hash`, `sig`), чтобы ваша SIEM могла **переверифицировать
хеш-цепочку офлайн**; PII никогда не экспортируется.

```bash
# One-shot full export (CEF). Requires a token with the audit:read permission.
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

Поддерживаемые значения `format` — `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope`,
`otlp_log_record` и `ocsf`. `otlp` — это полный, готовый к POST запрос экспорта
OTLP/HTTP на каждую запись, `otlp_envelope` — его точный алиас, а
`otlp_log_record` — простая проекция LogRecord (по одному LogRecord на строку).
Построчные форматы (`cef`/`leef`/`syslog`) стримятся как `text/plain`;
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` стримятся как NDJSON
(`application/x-ndjson`), по одному JSON-объекту на строку.

:::note[`ocsf` — это OCSF v1.8.0 API Activity]
В прежних редакциях этой страницы отмечалось, что текст ошибки движка опускал
`ocsf` из объявленного списка — этот пробел исправлен в апстриме; теперь и
сводка, и сообщение о неверном запросе строятся из реестра форматов движка, поэтому всегда называют каждый принимаемый формат.
:::

### Инкрементальное чтение хвоста с курсором

Экспорт пагинирует беспробельную цепочку по порядковому номеру через `?from=`.
Чтобы файл непрерывно дописывался для чтения хвоста UF, запустите небольшое
запланированное задание, возобновляющееся с последнего увиденного номера:

```bash
#!/bin/sh
# cron: every minute. Appends only new ledger records since last run.
STATE=/var/lib/olivares-export/last_seq
OUT=/var/log/olivares/audit.cef
FROM=$(cat "$STATE" 2>/dev/null || echo 1)

curl -fsS "https://localhost:8443/v1/audit/export?format=cef&from=$FROM" \
  -H "Authorization: Bearer $OLVK_TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  | tee -a "$OUT" \
  | sed -n 's/.*olivares-audit-export-complete .*last_seq=\([0-9]*\).*/\1/p' \
  | tail -1 > "$STATE.next" && [ -s "$STATE.next" ] && mv "$STATE.next" "$STATE"
```

Каждый экспорт завершается терминатором завершения — комментарием
`# olivares-audit-export-complete count=N last_seq=M` для текстовых форматов или
JSON-строкой `{"export_complete":true,...}` для
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf`. **Его отсутствие
означает, что поток был усечён** — не продвигайте курсор, если он отсутствует.

## Направьте Universal Forwarder на файл

Какой бы поток вы ни выбрали, установите Splunk UF на хост и добавьте вход
`monitor://`. С Olivares AI не поставляется никакого `inputs.conf` — вот
строфа, которую вы добавляете:

```ini
# $SPLUNK_HOME/etc/system/local/inputs.conf
[monitor:///var/log/olivares/audit.cef]
disabled = false
sourcetype = cef
index = olivares_audit

# For the findings file written by the filelog connector:
[monitor:///var/log/olivares/findings.json]
disabled = false
sourcetype = _json
index = olivares_findings
```

UF форвардит по S2S до вашего индексатора; сам Olivares AI никогда не говорит на
S2S.

## Сводка того, что поддерживается, а что нет

- **Поддерживается:** форвардинг чтением хвоста файла (UF читает хвост файла) —
  для обоих потоков.
- **Поддерживается:** Push через Splunk HEC — для потока находок (назначение
  `splunkhec`) **и** для журнала и находок через **приёмник** событий
  (`sink_kind: splunk_hec`, события `audit.recorded` / `finding.reported`, не
  менее одного раза) — см. [отправку в вашу SIEM](/ru/how-to/cookbook/push-to-siem/).
- **Поддерживается:** офлайн-переверификация журнала — и pull-экспорт, и push-насос несут
  поля хеш-цепочки дословно, поэтому SIEM может переверифицировать целостность.
- **Не поддерживается:** нативный эмиттер Splunk S2S — не реализован (post-v1).
- **Не поддерживается:** автоматический *файловый* приёмник журнала — чтобы получить журнал в
  локальный файл, материализуйте его запланированным pull-экспортом выше
  (push-насос нацелен на HTTP-приёмники, а не на файлы).
