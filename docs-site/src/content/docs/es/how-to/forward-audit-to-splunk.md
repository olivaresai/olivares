---
title: Reenvía a Splunk (despliega un Universal Forwarder + tail)
description: >-
  Lleva los hallazgos de gobierno del control plane y su audit ledger con alteraciones detectables
  a Splunk haciendo tail de un fichero con un Universal Forwarder — sin un emisor nativo
  Splunk-a-Splunk. Honesto sobre qué stream es cuál.
---

Puedes llevar datos de Olivares AI a Splunk **hoy**, sin esperar a un connector nativo:
escribe los datos en un fichero y apunta a él un **Universal Forwarder (UF) de Splunk**.
El UF gestiona el salto Splunk-a-Splunk (S2S) hacia tu indexer.

:::caution[No hay emisor S2S nativo de Splunk]
Olivares AI **no** implementa el protocolo propietario de forwarder S2S de Splunk. Un
emisor S2S nativo es post-v1. Las posturas soportadas son **reenvío por file-tail**
(un UF hace tail de un fichero que Olivares escribe), el **pull export** (para archivado WORM y
re-verificación offline), y un **push HTTP sobre Splunk HEC** — incluyendo, desde
el trabajo de interoperabilidad con SIEM, un push del **propio ledger** mediante un sink de eventing
([envía a tu SIEM](/es/how-to/cookbook/push-to-siem/)). Esta página documenta las
vías de fichero y de pull; la receta cubre el push.
:::

Hay **dos streams diferentes**, y no son la misma cosa. Elige
deliberadamente:

| Stream | Qué es | Vías hacia Splunk |
|---|---|---|
| **Gobierno / hallazgos** | el stream de notificaciones que enruta el módulo IX (hallazgos de salud, gasto, seguridad, compliance) | el connector de salida `filelog` lo añade a un fichero; o `splunkhec` lo envía con push; o un [sink de eventing](/es/how-to/cookbook/push-to-siem/) suscrito a `finding.reported` |
| **Audit ledger con alteraciones detectables** | el rastro de auditoría append-only, hash-chained y firmado | el **pull** export `GET /v1/audit/export` (esta página); o la **bomba** de push — un sink de eventing suscrito a `audit.recorded`, entregado al menos una vez. No hay sink de *fichero* nativo; materializa un fichero con el export programado de abajo |

## Stream A — hallazgos, vía el connector `filelog`

El connector de salida `filelog` añade el stream de notificaciones/hallazgos **un registro
por línea** a un fichero (o `stdout`/`stderr`), del que un UF puede hacer tail. Configura un
destino de notificación de tipo `filelog` con estos campos:

| Campo | Significado |
|---|---|
| `path` | destino de añadido: una ruta de fichero, o `stdout`/`stderr`/`-` |
| `format` | formato por línea: `json` \| `cef` \| `leef` \| `syslog` \| `otlp` \| `otlp_envelope` \| `ocsf` \| `asim` (por defecto `json`) |
| `hostname` | campo `HOSTNAME` de syslog (para el formato `syslog`) |
| `fsync` | vuelca cada registro a disco (durabilidad para una copia WORM; más lento) |

Para Splunk, `format: json` (campos ricos) o `format: cef`/`syslog` (formatos de línea que Splunk
analiza de forma nativa) funcionan ambos. El fichero se abre solo en modo añadir, así que el mismo fichero
sirve además como copia externa inmutable cuando se coloca en almacenamiento WORM.

:::note[`filelog` lleva hallazgos, no el ledger firmado]
El connector `filelog` reenvía el stream de **hallazgos** — nunca ve el
audit ledger con alteraciones detectables. Para reenviar el ledger verificable, usa el Stream B.
:::

### Alternativa llave en mano: Splunk HEC

Si prefieres hacer push sobre HTTP en lugar de hacer tail de un fichero, el connector `splunkhec` envía
el mismo stream de hallazgos al HTTP Event Collector de Splunk (`/services/collector`)
con una cabecera `Authorization: Splunk <token>` — una vía HTTP llave en mano, que sigue sin ser S2S
y sigue siendo el stream de hallazgos, no el ledger.

## Stream B — el ledger con alteraciones detectables, vía el pull export

El audit ledger se expone como un **pull export autenticado**, no como un fichero que el
motor escribe por su cuenta. Cada registro lleva los campos de integridad de cadena
(`seq`, `prev_hash`, `hash`, `sig`) para que tu SIEM pueda **re-verificar la cadena hash
offline**; el PII nunca se exporta.

```bash
# One-shot full export (CEF). Requires a token with the audit:read permission.
curl -fsS "https://localhost:8443/v1/audit/export?format=cef" \
  -H "Authorization: Bearer $OLVK_TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" >> /var/log/olivares/audit.cef
```

Los valores de `format` soportados son `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope`,
`otlp_log_record` y `ocsf`. `otlp` es una petición de exportación OTLP/HTTP completa y
posteable por registro, `otlp_envelope` es un alias exacto suyo, y
`otlp_log_record` es la proyección simple de un LogRecord por línea. Los formatos de
línea (`cef`/`leef`/`syslog`) se transmiten como `text/plain`; `otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` se transmiten como
NDJSON (`application/x-ndjson`), un objeto JSON por línea.

:::note[`ocsf` es OCSF v1.8.0 API Activity]
Las ediciones anteriores de esta página señalaban que el texto de error del motor omitía
`ocsf` de la lista anunciada — esa carencia se corrigió aguas arriba; el resumen y
el mensaje de bad-request se construyen ambos desde el registro de formatos del motor, así que siempre nombran todos los aceptados.
:::

### Tailing incremental con un cursor

El export pagina la cadena sin huecos por número de secuencia vía `?from=`. Para mantener un fichero
continuamente añadido para que el UF haga tail, ejecuta un pequeño job programado que reanude desde
la última secuencia que vio:

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

Cada export termina con un terminador de finalización — un
comentario `# olivares-audit-export-complete count=N last_seq=M` para los formatos de texto,
o una línea JSON `{"export_complete":true,...}` para
`otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf`. **Su ausencia significa
que el stream se truncó** — no avances el cursor si falta.

## Apunta el Universal Forwarder al fichero

Sea cual sea el stream que elegiste, instala un UF de Splunk en el host y añade una
entrada `monitor://`. Olivares AI no incluye ningún `inputs.conf` — esta es la stanza que
añades:

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

El UF reenvía sobre S2S a tu indexer; Olivares AI nunca habla S2S por sí mismo.

## Resumen de lo que está y no está soportado

- **Soportado:** Reenvío por file-tail (el UF hace tail de un fichero) — para ambos streams.
- **Soportado:** Push por Splunk HEC — para el stream de hallazgos (destino `splunkhec`)
  **y** para el ledger y los hallazgos mediante un **sink** de eventing
  (`sink_kind: splunk_hec`, eventos `audit.recorded` / `finding.reported`,
  al menos una vez) — véase [envía a tu SIEM](/es/how-to/cookbook/push-to-siem/).
- **Soportado:** Re-verificación offline del ledger — tanto el pull export como la bomba de push
  llevan los campos de cadena hash literalmente, de modo que un SIEM puede re-verificar la integridad.
- **No soportado:** Emisor S2S nativo de Splunk — no implementado (post-v1).
- **No soportado:** Sink automático de *fichero* del ledger — para llevar el ledger a un fichero local lo
  materializas con el pull export programado de arriba (la bomba de push apunta a sinks HTTP,
  no a ficheros).
