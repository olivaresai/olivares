---
title: "Módulo XV — integraciones de salida y notificaciones"
description: >-
  El enrutador de notificaciones del control plane: decide QUÉ señal llega
  a QUIÉN, por QUÉ canal y CUÁNDO, y despacha el resultado expurgado a través de los
  conectores de salida — Slack/Teams, PagerDuty/Opsgenie, webhook firmado, SIEM. El
  seam de actuación de extremo a extremo probado, con un valor por defecto deny-closed y un audit ledger.
---

El módulo XV es el **enrutador de notificaciones** del control plane: cuando cualquier módulo convierte una
alerta en un finding en el bus de eventos, este módulo decide a qué ruta del tenant
corresponde, construye una notificación expurgada, suprime duplicados y avalanchas, y
**la despacha en vivo** a los canales que la empresa ya tiene en marcha. Es el dueño del *decide
qué/quién/cuándo*; los conectores de salida son los dueños del *cómo* de la entrega — consume ese
transporte, nunca lo reimplementa.

## Qué es

Cada módulo del producto reporta una alerta como un finding de datos mínimos en el bus
([`finding.reported`](/es/reference/events/)) con un `Kind` con espacio de nombres — fiabilidad
(`health_subject_down`), gasto (`finops_budget`), seguridad (`security_guardrail`),
regresión de eval (`eval_regression`), residencia (`compliance_residency_violation`),
cadencia de orquestación, voz y más. El módulo XV se suscribe **únicamente** a ese
canal de alertas a nivel de todo el producto y enruta por `Kind`, severidad, módulo de origen y
sujeto. Deliberadamente **no** se suscribe a telemetría en crudo como
`cost.sampled` o `edge.observed`: una *alerta* de gasto llega como un finding `finops_budget`,
no como una muestra de coste. Este es el seam que convierte los findings de todo el producto
en notificaciones accionables.

## Contrato y entidades

El módulo declara dos entidades con ámbito de tenant en el modelo de datos compartido:

| Entidad | Modo | Qué contiene |
|---|---|---|
| **route** | mutable, auditada | Una regla de enrutamiento: un predicado sobre tipos de evento, globs de finding-kind (p. ej. `health_*`), severidad mínima, módulos de origen y tipos de sujeto → un **destino** con nombre, con ventanas de dedup y throttle por ruta y una prioridad. **No contiene ninguna credencial de destino** — solo un nombre de destino no secreto. |
| **delivery** | append-only | El audit ledger de cada *intento* de entrega: ruta, destino, finding kind, severidad, referencia de sujeto, título breve, un hash de correlación y una clase de resultado (`delivered`, `failed`, `no_dispatcher`, `unknown_destination`). |

En cada finding el módulo evalúa las rutas habilitadas del tenant por orden de prioridad;
toda dimensión de predicado dejada vacía significa *cualquiera*, y la coincidencia por glob admite formas exactas o
`prefix*`. La coincidencia ocurre dentro de una vista de lectura, **la entrega por red se ejecuta estrictamente
fuera de cualquier transacción de almacén**, y el resultado se escribe entonces en el ledger append-only.
Crear, cambiar o borrar una ruta, y enviar una notificación de prueba, son
acciones **privilegiadas y autoauditadas** atribuidas al principal real. Las rutas route y
delivery son alcanzables pero deliberadamente no forman parte del contrato OpenAPI
servido; sus formas a nivel de campo viven en las interfaces tipadas del producto.

## Qué consume y produce

- **Consume** [`finding.reported`](/es/reference/events/) — el único canal de alertas a nivel de
  todo el producto. Es un enrutador, no una sonda ni un medidor: nunca sondea infraestructura
  y nunca mide.
- **Produce** notificaciones salientes a través de un seam de despacho, respaldado por los conectores
  de salida (Slack/Teams, PagerDuty/Opsgenie, webhook firmado y un destino SIEM
  que cubre Splunk/Elastic vía CEF/LEEF/syslog/OTLP). Una notificación lleva solo
  los campos de presentación ya seguros del finding — título, kind, severidad, referencia de sujeto y
  un hash de correlación — **nunca** un payload, prompt, secreto o PII. **Los datos mínimos son una
  propiedad del cable**, no un filtro a posteriori. El secreto del destino vive
  solo en la configuración del conector que aprovisiona el operador, referenciado aquí por un
  nombre no secreto.

:::caution[Límites honestos]
- **El binario por defecto incluye un dispatcher deny-closed.** Hasta que un operador aprovisiona
  destinos, el dispatcher está cableado pero vacío: una entrega sin coincidencia se registra
  como `no_dispatcher` y un destino mal configurado o de kind desconocido se resuelve como
  `unknown_destination` en el ledger. **Nunca finge un éxito** — una no-entrega
  es siempre visible.
- **El webhook saliente es un conector de destino, no un webhook OpenAPI.** Es
  un canal de salida al que el control plane hace push, no un callback que registras contra
  la API del producto.
- **Dedup y throttle suprimen el *envío*, no un resultado.** Una notificación deduplicada o
  throttled intencionadamente **no** se escribe en el delivery ledger (para que
  nunca se infle). Cada *intento* real de entrega, en cambio, se registra —
  `delivered`, `failed`, `no_dispatcher` y `unknown_destination` por igual — de modo que una
  no-entrega es siempre visible, nunca se descarta en silencio.
- **El error en crudo del conector nunca se persiste ni se registra** — solo una clase de
  resultado no sensible — porque un error de transporte puede llevar el secreto del destino en su URL.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo XV y la separación Govern/Actuate.
- [Hacer push a tu SIEM](/es/how-to/cookbook/push-to-siem/) — el driver de push S2S
  (`modules/siemforward`) que re-da forma a los findings y al audit ledger sellado al
  dialecto nativo de una torre (OCSF/CEF/LEEF/syslog/OTLP) y monta sobre la entrega
  durable de la plataforma de eventing — el complemento de push a los destinos de arriba.
- [Referencia del bus de eventos](/es/reference/events/) — el evento `finding.reported` y su payload `FindingReport`.
- [Mapa de acceso y recursos](/es/reference/modules/iii-access-map/) — una referencia hermana de Core/Intelligence.
- [Reenviar auditoría a Splunk](/es/how-to/forward-audit-to-splunk/) — cableado de un destino SIEM.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — actuar sobre los findings que enruta este módulo.
- [Honestidad y límites](/es/start/honesty-and-limits/) — la postura deny-closed por defecto a lo largo del producto.
