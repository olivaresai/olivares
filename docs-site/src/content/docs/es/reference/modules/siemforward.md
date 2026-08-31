---
title: "Reenviador SIEM/ITSM"
description: >-
  Envía el audit ledger sellado y encadenado por hash y los hallazgos de
  gobernanza a tus torres SIEM e ITSM en su dialecto nativo — OCSF 1.8, CEF,
  LEEF, syslog u OTLP — sobre la plataforma de eventing durable, con un recorrido
  de cursor gobernado por líder y entrega al menos una vez. Renderiza y reenvía;
  nunca vuelve a derivar la integridad.
---

El reenviador SIEM/ITSM (`modules/siemforward`) toma la evidencia que el
motor ya sella y la lleva a la torre que tu SOC ya opera. Está **LIVE**. No
posee ninguna evidencia nueva: recorre el
audit ledger con alteraciones detectables
y el flujo de hallazgos de gobernanza, reconvierte cada registro al dialecto
nativo del destino y lo entrega a la [plataforma de eventing](/es/reference/modules/eventing/)
para una entrega durable. Los campos de integridad viajan literalmente — nunca
se vuelven a derivar en tránsito.

## Qué reenvía, y cómo

Cooperan dos mitades. Un **`SinkRenderer`** (implementa `eventing.SinkRenderer`)
reconvierte un evento capturado al formato de cable de la torre:

- `audit.recorded` — un registro sellado del ledger, renderizado a través de
  `core/audit`.
- `finding.reported` — un hallazgo de gobernanza (datos mínimos: hash más
  extracto expurgado).
- cualquier otra cosa en el bus — un sobre neutral en formato que un colector
  genérico puede parsear por sí mismo.

Dialectos soportados: **OCSF 1.8**, **CEF**, **LEEF**, **syslog**, **OTLP** y un
passthrough JSON estructurado. El renderer es **deny-closed**: un tipo de sink
desconocido o un formato no renderizable devuelve un error, y el motor reintenta
y luego envía la entrega a la dead-letter — nunca un envío no autenticado o con
formato erróneo.

Una **bomba de reenvío gobernada por líder** impulsa el resto. Cada pasada lee un
cursor por tenant, recorre el ledger desde la siguiente secuencia en lotes
acotados y encola cada registro. El cursor solo avanza más allá de los registros
que se encolaron con éxito, de modo que un fallo o reinicio reanuda desde donde
se detuvo — **al menos una vez** desde el ledger, la fuente autoritativa. Los
registros recorridos de nuevo se deduplican aguas abajo.

## Destinos

A dónde va el ledger es una **suscripción de sink** de eventing por tenant, no
una API self-service en este módulo — no monta rutas. Los destinos son
**aprovisionados por el operador**: Splunk HEC, Microsoft Sentinel (Logs
Ingestion / DCR), Datadog Logs, New Relic o un colector HTTPS genérico. El motor
abre la credencial sellada y posee el transporte; el renderer no mantiene estado
ni credenciales, así que una sola instancia sirve a cada tenant y sink.

## Contexto acotado, dicho con claridad

- **Reenvía**, no almacena. Un tenant sin suscripción de sink es un no-op: no se
  encola nada, el cursor sigue avanzando, no se pierde nada.
- El reenvío corre desde el recorrido del cursor, **fuera de la transacción de
  sellado del ledger** — una escritura de red nunca queda en la ruta de sellado.
- Esto es un **push hacia tu torre**, distinto del pull de solo lectura del
  [posture export](/es/reference/modules/posture-export/). La ingesta del lado de la
  torre queda fuera de alcance; renderizamos al dialecto publicado y entregamos.

## Relacionado

- [Eventing](/es/reference/modules/eventing/) — la superficie de suscripción durable
  (retry/backoff, DLQ, replay de cursor) sobre la que este módulo renderiza.
- [Compliance](/es/reference/modules/xiii-compliance/) — el paquete de evidencia
  sellado y derivado del ledger que este flujo complementa.
- [Reenviar auditoría a Splunk](/es/how-to/forward-audit-to-splunk/) — la ruta de
  file-tail cuando no puedes aprovisionar un sink nativo.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué significan "al menos
  una vez" y "aprovisionado por el operador" para esta superficie.
