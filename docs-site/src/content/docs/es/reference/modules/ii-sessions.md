---
title: "Módulo II — operación en vivo y sesiones"
description: >-
  La capa operativa en vivo por sesión de agente: acción actual, tokens/coste en
  vivo, un estado de Claude Code derivado y una línea temporal reproducible,
  transmitida sobre server-sent events. Qué deriva, qué se mantiene honestamente
  vacío y cuáles son los límites.
---

El módulo II es la vista de **operación en vivo** del estate: qué está haciendo
ahora mismo cada sesión de agente, sus totales de tokens y coste en vivo, un estado
de Claude Code derivado y una línea temporal reconstruible. Mientras que el módulo
I (inventario) materializa el estate durable, el módulo II mantiene una **capa
operativa en vivo** por sesión sobre el mismo flujo de observaciones — y muestra
solo lo que ese flujo lleva honestamente.

## Qué es

El módulo II es un módulo de la capa Core dirigido por el bus, hermano del
inventario. Mantiene un registro en vivo indexado por la referencia externa de cada
sesión, construido a partir del flujo cooperativo de observaciones — nunca
sondeado, nunca fabricado. Por sesión rastrea:

- la **acción actual** (la última herramienta usada) y el recurso/modo que tocó;
- los **totales de tokens y coste en vivo**, leídos de las muestras de coste (el
  ledger de coste canónico y FinOps son el módulo XI, no aquí — esto es solo la
  cifra en vivo);
- un **estado de Claude Code derivado** (`cc_state`); y
- una **línea temporal** a la que cada evento observado se añade en orden de
  ingesta.

## Su contrato y entidades

El módulo registra dos entidades acotadas al tenant. `sessions.live` contiene el
registro en vivo por sesión — acción/recurso/modo actual, referencia de modelo,
tokens de entrada/salida en vivo, coste en vivo, conteos de eventos y de llamadas a
herramientas, y marcas temporales de primer/último evento. `sessions.timeline`
contiene una fila reproducible por evento, ordenada por ingesta. **No hay columna
de ciclo de vida almacenada**: el flujo cooperativo no lleva ninguna señal de fin o
fallo, de modo que la única señal de vitalidad honesta es el `cc_state` derivado.

`cc_state` se deriva **en tiempo de lectura** a partir de la recencia de los eventos
— `active` / `idle` / `ended` — y cambia a un estado de evasión-silenciosa cuando el
conector eleva ese hallazgo (nunca lo escribe el propio módulo). Las lecturas se
sirven bajo rutas del módulo (lista en vivo, sesión única, línea temporal por
sesión) más un flujo SSE en vivo; cada lectura requiere el permiso de lectura de
sesión, y **abrir el flujo se audita automáticamente**. El canal SSE está
estrictamente **aislado por tenant** (un cliente recibe solo instantáneas de su
tenant autorizado) y es de **mejor esfuerzo** (un cliente lento descarta el frame
intermedio y recibe el siguiente — la ingesta nunca se bloquea).

## Qué consume (y qué deriva)

El módulo II consume el mismo flujo de observaciones de datos mínimos que el
inventario — [`edge.observed`](/es/reference/events/), `cost.sampled` y
`finding.reported`. Solo los edges cuyo origen es una **sesión** producen operación
en vivo; las muestras de coste ligadas a una sesión suman a la cifra de
tokens/coste en vivo (aquí no se escribe ningún `CostRecord`); los hallazgos cuyo
sujeto es una sesión se anotan, y un hallazgo anti-evasión marca el estado de
evasión. Dos campos se **derivan en vivo** a partir de esas mismas señales:
`agent_ref` del agente atribuido a una sesión, y `summary` de un hallazgo
(forense) de compactación de contexto cuyo título es seguro como resumen por
contrato — nunca un resumen fabricado por un LLM.

:::caution[Límites honestos]

- **`goal` se mantiene vacío — honestamente.** El flujo cooperativo es de datos
  mínimos y **no** lleva el objetivo ni la lista de tareas de una sesión; se
  expurgan en el conector y no hay texto de prompt en proceso sobre el cable. El
  registro en vivo modela el campo para que el contrato y la UI estén listos y
  cualquier futuro canal de metadatos pueda poblarlo, pero el módulo **nunca lo
  inventa**.
- **Sin ciclo de vida almacenado.** El flujo no tiene señal de fin/fallo, así que
  la vitalidad de una sesión es el `cc_state` **derivado** por recencia — no un
  estado persistido. Un estado `ended` significa *no hay eventos recientes*, no un
  apagado limpio confirmado.
- **La cifra en vivo no es el ledger.** Los tokens/coste en vivo son una lectura
  operativa de las muestras de coste; el registro de coste autoritativo y
  conciliable es el ledger FinOps del módulo XI. No trates la cifra en vivo como
  verdad de facturación.
- **Los datos mínimos son una propiedad del cable.** Solo se llevan y persisten
  referencias, clasificaciones y contadores de vitalidad/coste — nunca payloads,
  prompts, comandos o PII.
:::

## Relacionado

- [Referencia del bus de eventos](/es/reference/events/) — los eventos
  `edge.observed`, `cost.sampled` y `finding.reported` que consume este módulo.
- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo II
  y la división honesta de actuación.
- [Mapa de acceso y recursos](/es/reference/modules/iii-access-map/) — el módulo Core
  hermano que es dueño del grafo de acceso R/RW.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — el motor y las capas.
- [Conectar Claude Code](/es/how-to/connect-claude-code/) — empieza a producir el flujo en vivo.
- [Honestidad y límites](/es/start/honesty-and-limits/) — lo que el producto hace y no hace hoy.
