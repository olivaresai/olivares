---
title: "Módulo XXII — salud, SLA y disponibilidad"
description: >-
  Fiabilidad de los agentes y servidores MCP del estate de IA: qué está sano, qué está
  degradado o caído, y qué depende de qué. Cómo se deriva la salud a partir de señales
  que el producto puede demostrar, qué materializa y sus límites honestos.
---

El módulo XXII responde tres preguntas sobre los componentes de IA del estate: **qué está
sano, qué está degradado o caído, y qué depende de qué**. Está acotado a la **fiabilidad
de los agentes y servidores MCP**, no a la salud del host o la infraestructura en general.
Esta página es la referencia de lo que el módulo mide, lo que materializa y dónde están
sus bordes honestos.

## Qué es

XXII es un **consumidor del núcleo**, no un sondeador: abrir sockets hacia la
infraestructura del cliente es asunto de un connector, y el conjunto sellado de
observaciones no tiene un tipo de salud. Así que la salud se **deriva** a partir de
señales que el módulo puede demostrar:

- **Liveness (pasivo).** Una sesión o un agente tocando un servidor MCP —o un agente
  actuando— es evidencia de que el sujeto está vivo. Refresca el marcador de último-visto
  del sujeto y pliega un borde de dependencia.
- **Resultados de sondeo activo.** Un comprobador de salud externo, o el propio agente,
  publica un resultado en un endpoint de informe por comprobación: la vía de ingesta
  honesta para "health checks / métricas OTEL".
- **Obsolescencia (staleness).** Un sujeto conocido que deja de verse dentro de su cadencia
  esperada es en sí mismo una señal. Un barrido en segundo plano lo transiciona a
  `degraded`, luego a `down`, y abre un incidente. El barrido solo **degrada o marca como
  caído**; la recuperación viene exclusivamente de liveness real, de modo que una
  comprobación recién creada nunca emite una recuperación espuria.

## Su contrato y entidades

El módulo posee cuatro entidades. Una **health check** es un sujeto monitorizado declarado
por el operador (un agente o un servidor MCP) con una cadencia esperada y un objetivo de
SLA; lleva el estado de instantánea actual del sujeto: `healthy`, `degraded`, `down` o
`unknown`. Un **health event** es un ledger de transiciones de solo-añadir a partir del
cual la disponibilidad y el SLA se *reconstruyen*, nunca almacenado como un contador en
marcha. Un **health incident** es el ciclo de vida abierto→resuelto de un periodo
degradado o caído, con un incidente abierto impuesto por sujeto. Una **health dependency**
es un borde `origin → target` autodescubierto: el mapa de dependencias, acumulado de forma
idempotente.

La salud se **materializa solo para comprobaciones declaradas**. Un sujeto observado vivo
**sin comprobación declarada** se expone honestamente en el mapa de dependencias como
`observed` —*visto vivo, salud no medida*—, un estado distinto de `healthy` (una
comprobación declarada lo señaló) y de `unknown` (nombrado, sin evidencia de liveness). El
producto nunca fabrica un estado de salud-medida que no calculó. XXII además refleja el
estado actual de un sujeto en la entidad `HealthStatus` del núcleo cuando el sujeto es un
id del núcleo, de modo que otros planos puedan leer la salud de un agente o de un MCP.

## Qué consume y qué produce

XXII consume [`edge.observed`](/es/reference/events/) del bus para el liveness pasivo y el mapa
de dependencias, además de los informes de sondeo activo que llegan a su API. **Produce, no
entrega**: las señales de caída, degradación, recuperación e incumplimiento de SLA se
emiten como `FindingReport`s de dato mínimo en el canal
[`finding.reported`](/es/reference/events/) —el flujo de alertas de todo el producto que el
[módulo XV (notificaciones)](/es/reference/modules/xv-notify/) enruta a Slack, PagerDuty o un
SIEM. XXII nunca entrega, y nunca se suscribe a sus propios hallazgos.

:::caution[Límites honestos]
- **Solo mide lo que se declara.** La salud se materializa únicamente para comprobaciones
  declaradas. Un sujeto vivo pero no declarado se lee `observed` (visto vivo, no medido),
  nunca `healthy`. La fiabilidad es tan completa como las comprobaciones que un operador
  declara.
- **No es un sondeador.** XXII nunca abre sockets hacia tu infraestructura. Deriva la
  fiabilidad del liveness, los resultados de sondeo publicados y el silencio, de modo que
  para un sujeto que no emite telemetría y no tiene comprobador externo, una ausencia de
  señal se trata como una señal (staleness), no como prueba de salud.
- **La disponibilidad y el SLA se reconstruyen a partir de un ledger de solo-añadir**, no se
  mantienen como un medidor en vivo; las cifras reflejan las transiciones registradas para
  la ventana solicitada.
- **Sin actuación.** Este módulo gobierna y observa por naturaleza: no tiene superficie de
  actuación (consulta [visión general de módulos](/es/reference/modules/overview/)). Detecta y
  reporta; la remediación es un asunto humano o aguas abajo.
- **Dato mínimo en el cable.** El estado almacenado es el estado, las métricas de fiabilidad
  y las relaciones de dependencia, nunca payloads, prompts, secretos o PII. El único detalle
  sensible que un sondeo puede llevar (un mensaje de error) se reduce a un hash de un solo
  sentido; solo se muestra un resumen corto y no sensible.
:::

## Relacionado

- [Referencia del bus de eventos](/es/reference/events/) — `edge.observed` (liveness) y `finding.reported` (las señales que emite XXII).
- [Módulo XV — integraciones de salida y notificaciones](/es/reference/modules/xv-notify/) — enruta los hallazgos de salud de XXII a sus destinos.
- [Visión general de módulos](/es/reference/modules/overview/) — dónde se sitúa XXII y la separación de actuación.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — el motor, el bus y la capa de núcleo.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué observa el producto hoy frente a lo que actúa.
