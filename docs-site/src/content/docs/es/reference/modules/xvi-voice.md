---
title: "Módulo XVI — agentes de voz y en tiempo real"
description: >-
  El plano de observar-y-gobernar para agentes conversacionales/en tiempo real. Gobierna quién
  puede abrir una sesión de voz, con qué modelo y proveedor, bajo una política default-DENY
  — y rastrea metadatos de sesión con una prohibición tajante de cualquier audio o contenido
  de transcripción.
---

El módulo XVI gobierna **agentes conversacionales y en tiempo real**. Es un
plano de **observar-y-gobernar**: **no** reimplementa un SDK de voz (Realtime API,
WebRTC, ASR o TTS) y nunca abre un stream de medios por sí mismo. Decide *quién* puede
abrir una sesión de voz, con *qué* modelo y proveedor, bajo *qué* política, y
rastrea los metadatos de esa sesión — nunca su contenido.

## Qué es

Abrir una interfaz de voz se trata como una **acción privilegiada**, no una operación
libre. La política es **default-DENY**: una sesión sin política que la permita se rechaza.
Una apertura es **de dos fases** y está **sujeta a human-in-the-loop** a través de la
[approval gate](/es/how-to/govern-and-approve/); está ligada a un `plan_hash` para que una
aprobación no pueda elevarse en silencio a un modelo más fuerte (anti-TOCTOU), auditada al
**principal real** (nunca `system`), y evidenciada **append-only**. El módulo
mismo nunca llama a un proveedor — la actuación sale por un seam de despacho separado.

La otra mitad es **observación**: el módulo rastrea solo metadatos de sesión —
estado derivado (live/idle/ended, computado en tiempo de lectura a partir de la recencia de actividad, sin
columna de ciclo de vida almacenada), recuento de turnos, duración, latencia (avg y max honestos a partir de
muestras reales) e idioma BCP-47. A partir de esto eleva **findings** de gobernanza: una
violación de política cuando la telemetría nombra un agente/modelo/proveedor que ninguna política permite, un
finding de latencia degradada cuando la latencia cruza un SLA de política, y un
finding de apertura no gobernada cuando se intenta una apertura sin gate cableado — la brecha se
expone y la apertura aun así se deniega.

## Contrato y entidades

El módulo declara tres entidades en el modelo de datos compartido:

| Entidad | Mutabilidad | Propósito |
|---|---|---|
| **session** | mutable (upsert) | metadatos de sesión; **cero contenido** |
| **policy** | mutable | declaración de gobernanza — quién puede abrir con qué modelo/proveedor (default-DENY) |
| **decision** | **append-only** | ledger inmutable de decisiones de apertura/cierre |

Una política coincide por agente, modelo permitido y proveedor permitido (cada uno específico o
comodín), con límites opcionales de minutos de sesión y de SLA de latencia. **Ninguna política coincidente
significa DENY.** El decision ledger registra cada `open_request`, `open` y `close`
con su veredicto de política, estado de gate y estado de resultado. El acceso de lectura es el rol
de viewer y superiores; declarar una política y abrir una sesión son acciones administrativas,
con ámbito de tenant y auditadas. Estas rutas del módulo se publican en la
[referencia de rutas de módulos](/reference/api-beta/) **beta** separada, no en el contrato
estable del núcleo — sus formas a nivel de campo viven en las interfaces tipadas del producto.
Los importes en dólares **no** están aquí; FinOps (módulo XI) es el dueño del coste.

## Qué consume y produce

El módulo es dueño de un seam de ingesta deny-closed — su propio evento `voice.telemetry.observed`
— a través del cual una sonda **in-process** alimentaría metadatos de sesión. El cable es
**de datos mínimos por construcción**: el parser de telemetría lleva una allow-list y
**rechaza el evento entero** si ve una clave prohibida, de modo que nunca puede persistirse audio, texto de
transcripción, texto ASR/TTS, contenido de prompt/respuesta ni PII de hablante. La única
señal de transcripción que se conserva es un hash de un solo sentido de un *localizador* de transcripción **externo** —
prueba de que existe una transcripción, nunca la transcripción. Los findings de gobernanza se emiten como
[`finding.reported`](/es/reference/events/) con detalle hasheado, tras el commit.

## Estado de Actuate

Una apertura gobernada despacha **en vivo**: una vez que el operador aprovisiona un dispatcher de voz,
una apertura aprobada acuña una **credencial efímera del lado del servidor** y
devuelve solo esa credencial más las coordenadas de conexión — modelo, voz, herramientas y
detección de turnos quedan fijados **desde la política**, nunca desde el cliente, y la
clave maestra del proveedor nunca sale del servidor. Sin ese aprovisionamiento el
seam de despacho es **deny-closed**: una apertura aprobada se registra honestamente como
"declarada, no abierta" en lugar de fingirse.

:::caution[Límites honestos]
- **La observación está latente en esta build.** Aún no se incluye ningún conector de voz ni sonda,
  así que la mitad de observación queda **honestamente vacía** hasta que una sonda in-process
  publique telemetría. El módulo avisa al arrancar cuando nada lo alimenta. Un
  plugin out-of-process **no puede** alimentarlo (el proto del control plane gRPC no lleva
  RPC de eventos) — la sonda debe ser in-process.
- **Sin contenido, jamás.** Es una propiedad tajante del cable, no un ajuste: el
  esquema no tiene columna de contenido y el parser rechaza claves desconocidas. La latencia se muestra
  como avg/max honestos de muestras reales — nunca un p50/p95 fabricado.
- **Sin finding de "estancamiento".** El fin de una sesión de voz es silencio normal (como un agente
  terminado). Sin una línea base honesta, un finding de estancamiento sería un falso positivo, así que se
  omite deliberadamente.
- **Pre-1.0.** Como gran parte de la plataforma, este módulo está en profundidad en fase de diseño — ver
  [Honestidad y límites](/es/start/honesty-and-limits/).
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo XVI y su estado de actuate.
- [Referencia del bus de eventos](/es/reference/events/) — `finding.reported` lleva los findings de voz.
- [Módulo IV — orquestación](/es/reference/modules/iv-orchestration/) — el seam de despacho hermano (disparo en vivo).
- [Módulo X — enrutamiento de modelo y proveedor](/es/reference/modules/x-models/) — qué modelos puede permitir una política.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — la apertura gateada de dos fases en la práctica.
- [Honestidad y límites](/es/start/honesty-and-limits/) — la separación observar/gobernar/actuar.
