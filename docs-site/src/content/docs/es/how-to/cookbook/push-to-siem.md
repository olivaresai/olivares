---
title: "Receta: enviar hallazgos y el ledger a tu SIEM"
description: >-
  Crea un sink de push — Splunk HEC, Microsoft Sentinel, Datadog o New Relic,
  o un webhook genérico firmado con HMAC — y suscríbelo a los hallazgos y al
  audit ledger sellado, entregados al menos una vez en OCSF, CEF o el formato
  que hable tu torre.
sidebar:
  order: 6
---

**Objetivo:** tu SIEM recibe los hallazgos del control plane *y* su audit
ledger con alteraciones detectables como push, sin que un forwarder vaya leyendo ficheros (tail).

Esta es la vía de push S2S (servicio a servicio) sobre la plataforma de eventing. Las
[posturas de pull export y file-tail](/es/how-to/forward-audit-to-splunk/) siguen
totalmente soportadas — el pull sigue siendo la forma correcta para archivado WORM y
re-verificación offline; el push es la forma correcta para la ingesta en vivo del SIEM.

## 1. Crea la suscripción del sink

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "splunk-prod",
    "event_types": ["finding.reported", "audit.recorded"],
    "endpoint": "https://splunk.internal:8088/services/collector",
    "sink_kind": "splunk_hec",
    "sink_format": "ocsf",
    "sink_cred": "<hec-token>"
  }'
```

- **`sink_kind`** selecciona el dialecto de la torre: `splunk_hec`, `sentinel_dcr`,
  `datadog`, `newrelic` — u omítelo por completo para el **webhook genérico**
  (un endpoint HTTPS que recibe el evento JSON, autenticado por la
  firma HMAC del motor; rota con `…/{id}/rotate-secret`).
- **`sink_format`**: `ocsf` (el valor por defecto para sinks de SIEM — el esquema
  consciente de IA), `cef`, `leef`, `syslog`, `otlp`, `otlp_envelope` o `json`.

  :::caution[`sink_format` necesita un `sink_kind`]
  El formato solo se aplica si hay un tipo de sink. **Omitir `sink_kind` NO es "la
  opción HTTPS"**: selecciona el webhook genérico, que envía el JSON de evento de
  Olivares y ni siquiera valida `sink_format`. Para enviar un dialecto SIEM a tu
  propio endpoint, pon `sink_kind: "https"` explícitamente:

  ```json
  {
    "event_types": ["audit.recorded"],
    "sink_kind": "https",
    "sink_format": "otlp_envelope",
    "endpoint": "https://collector.internal:4318/v1/logs"
  }
  ```

  Con `otlp` (y `otlp_envelope`, su alias exacto) el endpoint debe ser la ruta
  exacta `/v1/logs` del colector: el cuerpo se envía a la URL tal cual.
  :::
- **`sink_cred`** (el token HEC / bearer DCR / clave de API) se acepta una sola vez,
  **se sella en reposo, nunca se devuelve ni se registra**. Los tipos de proveedor lo exigen
  al crear; el webhook genérico no necesita ninguno.
- **`event_types`** es tu selección de stream: `finding.reported` para el
  raíl de hallazgos, `audit.recorded` para el ledger (más abajo), o ambos.

Prueba la entrega antes de confiar en ella:

```bash
curl -ks -X POST "$BASE/v1/m/eventing/subscriptions/$ID/test" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
```

## 2. El push del ledger, descrito con honestidad

Suscribirse a **`audit.recorded`** activa la bomba del ledger: el forwarder
recorre el audit ledger sellado de cada tenant desde un cursor por tenant y encola
cada registro en el motor de entrega duradero — **al menos una vez**, en orden,
reanudable. Cada registro lleva sus campos de integridad de cadena literalmente, de modo que la
copia del SIEM permite exactamente lo que permite el pull export: el
ENLACE de la cadena (`prev_hash` de n+1 igual a `hash` de n) y una firma de
checkpoint sobre `hash` se pueden comprobar offline, y el `hash` de un registro ya se
puede RE-DERIVAR a partir de UNA sola línea exportada — todas las entradas del hash
de cadena viajan en el cable, incluidos el texto canónico `occurred_at` y el
compromiso de metadatos. Hoy se ha demostrado la rederivación exacta byte a byte para
`syslog` y las tres grafías de OTLP, con los alfabetos de valores que emite este ledger
(UUID, actores `kind:id`, verbos con puntos, un timestamp de formato fijo y digests
hexadecimales): syslog sustituye CR y LF por un espacio, y OTLP reemplaza el UTF-8 no
válido, por lo que ninguna de las dos garantías es incondicional; `ocsf` (el valor
predeterminado del sink), `cef` y `leef` transportan los mismos campos, pero todavía no
permiten reconstruir los bytes, porque su escape y su mapeo de campos pierden
información en los valores de texto libre. Elige uno de los tokens demostrados si
pretendes rederivar. Ese compromiso va cegado por registro: completa la preimagen
sin revelar nada sobre los metadatos que hay detrás. Tres afirmaciones siguen siendo
distintas — re-derivar el hash no es comprobar la AUTENTICIDAD (eso exige una clave de
confianza externa) ni la COMPLETITUD (eso exige registros adyacentes y un checkpoint).
El *archive* de auditoría sigue siendo el artefacto más fuerte: lleva los metadatos
mismos junto a su blind, así que además puede responder QUÉ metadatos cubre un
compromiso.

Tres propiedades que conviene conocer:

- **Sin suscripción, no hay trabajo.** Sin ningún suscriptor de `audit.recorded`, la bomba
  no escribe nada — la vía no cuesta nada hasta que la pides.
- **«Al menos una vez» significa que son posibles los duplicados** en la reentrega; deduplica
  por el número de secuencia del registro por tenant.
- **La bomba está controlada por líder (leader-gated)** en HA — exactamente un nodo reenvía.

## 3. ITSM: hallazgos como tickets

El mismo mecanismo de suscripción dirige destinos ITSM a través del
raíl de notificaciones — incidentes de ServiceNow e issues de Jira a partir de hallazgos, con
la severidad mapeada a prioridad. Configúralos como **destinos** de notificación
(los connectors de salida `servicenow` / `jira`) en lugar de
sinks de SIEM; la [tabla de destinos de la página de Splunk](/es/how-to/forward-audit-to-splunk/)
muestra el patrón.

## Verifica de extremo a extremo

1. `…/test` devuelve entregado.
2. Provoca algo observable (un umbral de [alerta de presupuesto](/es/how-to/cookbook/budgets-and-finops-guardrails/),
   una herramienta denegada) y observa cómo llega el hallazgo.
3. Para el ledger: compara la marca de máximo nivel (high-water mark) de `seq` del lado del SIEM con
   `GET /v1/audit/export?from=<seq>` — los streams deben coincidir.

## Notas

- Los endpoints deben ser **HTTPS**; el motor rechaza los sinks en texto plano.
- Las instantáneas de postura (roll-ups de compliance/NHI/hallazgos) tienen su propio módulo
  de exportación circulando por los mismos raíles — véase el
  [módulo de compliance](/es/reference/modules/xiii-compliance/).
- La tabla de decisión completa — cuándo hacer pull, cuándo hacer tail, cuándo hacer push — está en
  la [página de reenvío a Splunk](/es/how-to/forward-audit-to-splunk/).
