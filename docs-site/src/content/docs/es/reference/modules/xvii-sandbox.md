---
title: "Módulo XVII — sandbox de simulación y pruebas de agentes"
description: >-
  Ejecución aislada y efímera de escenarios de agente contra herramientas y
  recursos simulados, replay determinista de una sesión histórica y comparación pre/post-deploy
  de dos variantes — con una garantía de aislamiento honesta y atestada.
---

El módulo XVII es el **sandbox de pruebas**: ejecuta un escenario de agente en un entorno aislado y
efímero, reproduce una sesión histórica de forma determinista y compara
dos variantes antes de un despliegue. Es el hermano del módulo XII (evals) — el XVII
**ejecuta en aislamiento y produce salidas**, el XII **mide su calidad** — y
los dos están desacoplados: ninguno importa al otro. Esta página es la referencia de lo que
hace el sandbox hoy y sus límites honestos.

## Qué es

El sandbox cataloga **escenarios** redactados por el operador: una secuencia de inputs de paso más
las respuestas simuladas de las herramientas y recursos que una ejecución tiene permitido tocar. Un escenario
es un fixture sintético — sin secretos, sin handles de producción — clampeado antes de
persistirse. Sobre él se ejecutan tres flujos:

- **Simulación de escenario** — ejecuta los pasos de un escenario contra sus mocks, produciendo
  salidas por paso (opcionalmente puntuadas contra una suite de evals).
- **Replay** — reconstruye la línea temporal de inputs de una sesión histórica y la re-ejecuta
  de forma determinista contra mocks, de modo que el mismo input produce la misma salida.
- **Comparación pre/post-deploy** — ejecuta el *mismo* escenario contra una variante de línea base y una
  candidata, puntúa ambas y registra un veredicto (`improved` / `regressed` /
  `unchanged` / `inconclusive`) con el delta.

## Entidades y la garantía de aislamiento

El módulo es dueño de cuatro entidades: un **scenario** mutable, un **run** mutable (`running` →
terminal), un **output** por paso append-only y una **comparison** pre/post-deploy append-only.
Cada ejecución registra *qué* runner la ejecutó, si ese runner fue
`isolated`, si el estado efímero fue `destroyed`, los recuentos por paso y — si se cableó
un scorer — la suite, la puntuación y el veredicto de pase.

El aislamiento es una propiedad del cable, atestada por ejecución, no una afirmación. El runner
in-process por defecto es **aislado por construcción**: recibe solo la spec de paso-y-mock
y no tiene ningún handle al almacén, la red ni ningún secreto; un paso que pide un
recurso ausente de los mocks produce un marcador determinista de mock-miss y nunca
alcanza un recurso real; el estado vive en la llamada y se descarta al retornar, de modo que la ejecución
registra `destroyed`. Bajo aprovisionamiento del operador, un **runtime a nivel de SO** se sitúa tras la
misma interfaz — una instancia efímera, endurecida y con egress controlado cuyo backend
(gVisor o microVM Firecracker) se elige *por política* y se gatea por preflight. Cada ejecución
registra el backend real y su flag `isolated`, de modo que un backend degradado o portable es
visible y auditable, nunca oculto.

## Qué consume y produce

El sandbox no emite en el bus de eventos; produce **evidencia persistida** que
otros módulos leen sin acoplarse a él. Sus salidas las puntúa el módulo XII a través de
un adaptador cableado solo en la raíz de composición — los dos hermanos comparten un contrato de puerto
fino, no un import. Su comparación pre/post-deploy es la **evidencia de decisión** que
lee el módulo de despliegue para gatear una promoción, y alimenta la línea base de regresión que
rastrea el XII. Lanzar una ejecución, un replay o una comparación es una acción **privilegiada, con ámbito de
tenant y auditada** (editor y superiores para ejecutar; la comparación de deploy es una decisión de admin).

:::caution[Límites honestos]
- **El runtime por defecto es solo-sintético.** Sin un runtime a nivel de SO aprovisionado por el
  operador, el runner de mocks in-process es el backend: está aislado por construcción
  pero ejecuta solo contra mocks, así que no puede alcanzar un objetivo real ni respaldar
  una sonda adversarial contra infraestructura en vivo (el módulo XVIII mantiene su propio default seguro
  hasta que se aprovisiona el runtime). Esto es honesto, no degradado — un despliegue por defecto
  es plenamente funcional.
- **Aprovisionado-pero-incapaz falla cerrado.** Cuando se pide aislamiento a nivel de SO y
  el host carece de la primitiva, el engine cablea lo mismo y **cada ejecución falla cerrado** —
  nunca degrada en silencio al runner sintético ni finge una microVM. Una ejecución en un
  host sin aislamiento se registra como no aislada, nunca como protegida.
- **Sin scorer cableado ⇒ "ejecutada, no puntuada."** Una ejecución que lleva una referencia de suite sin
  adaptador de scorer se registra como ejecutada pero no puntuada — nunca un pase silencioso.
- **El replay es honesto sobre las brechas.** Si la fuente de historial no puede reconstruir una línea
  temporal ordenada, el replay se reporta degradado con cero pasos, nunca fabricado.
- **Sin generación de datos sintéticos.** Es solo un punto de extensión documentado post-v1;
  el módulo no incluye generador, no expone ninguna ruta para ello y produce cero muestras.
:::

## Relacionado

- [Módulo XII — calidad, evals y pruebas](/es/reference/modules/xii-evals/) — el hermano que puntúa las salidas.
- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el XVII y la separación Govern/Actuate.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — la capa de Intelligence.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — actuar sobre un veredicto pre/post-deploy.
- [Honestidad y límites](/es/start/honesty-and-limits/) — los seams deny-closed a lo largo del producto.
