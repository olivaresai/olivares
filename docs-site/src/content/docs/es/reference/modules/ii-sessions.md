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

v26.9.1 también **lanza** CLI oficiales de proveedor como hijos propios bajo un
[perfil de proveedor](/how-to/operate-provider-sessions/). Esa vía gestionada
es el mismo módulo. No sustituye la capa ni fusiona dos homes que anuncian el
mismo id de sesión del proveedor (`CHANGELOG.md` `[26.9.0]` B1/B2).

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

## Provider profiles and `live_ref`

Un **perfil de proveedor** es la identidad durable de una instancia de
proveedor configurada en un entorno de ejecución: controlador, entorno y el
`config_home` / `user_home` canónicos. No es una cuenta autenticada del
proveedor. Registrar, renombrar, desactivar/activar y retirar viven bajo
`/v1/m/sessions/provider-profiles`. Las rutas aparecen solo en la lectura
admin `configuration`. Un lanzamiento nombra `provider_profile_ref`; el
servidor resuelve los homes y persiste una instantánea no secreta en el run
antes del spawn (`CHANGELOG.md` `[26.9.0]` B1;
`web/src/features/agentops/types.ts`).

Una observación se pliega en la fila en vivo de su **canal**, calculado por el
servidor a partir del registro de fuente sellado por el host
(`CHANGELOG.md` `[26.9.0]` B2):

| Channel | Meaning |
|---|---|
| `legacy` | no registration |
| `observed` | a source dedicated to a profile by a binding approved at host admission for the exact applied revision |
| `source` | a known registration with no verifiable profile |
| `managed` | a run the plane launched; the only row carrying `canonical_sid` and `run_ref` |

Cada fila en vivo expone `live_ref` y `attribution`. Dos homes que anuncian el
mismo id de sesión del proveedor son dos filas con dos cronologías. Lee una
fila con `GET /v1/m/sessions/live/by-id/{live_ref}` (y su consulta de
cronología / stream / runs). Las rutas de id externo desnudo permanecen y son
**legacy**: responden solo para la fila legacy.

Los controladores se registran **por nodo** fijando un binario oficial
(`OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`, `_CODEX_BIN`, `_GROK_BIN` — véase
[Configuración](/reference/configuration/)). Sin fijar, los perfiles de ese
controlador siguen observables y no se pueden lanzar. Pasos del operador:
[Operar una sesión de proveedor](/how-to/operate-provider-sessions/).

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
