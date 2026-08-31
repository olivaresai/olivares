---
title: "Live-ingest — el productor de observación en proceso"
description: >-
  Uno de los 30 módulos: el productor "live-tap" que publica los eventos
  detective que un connector fuera de proceso no puede emitir. Deny-closed y de
  datos mínimos: no mueve contenido crudo, y cada mitad de observación que posee
  está honestamente vacía en lugar de falseada. Parcial — es de adhesión
  explícita y se sujeta por variable de entorno.
---

Live-ingest (`modules/liveingest`) es uno de los 30 módulos conectados — un **productor en
proceso** más que una ranura de capacidad. No forma parte del mapa histórico
numerado I–XXIII. Existe por una razón arquitectónica:
un `SourceConnector` fuera de proceso solo puede transmitir la suma de
observación sellada (edge / cost / finding) sobre su contrato gRPC, que no tiene
RPC de eventos ni campo de texto — así que **no puede publicar un evento
detective**. Solo un módulo en proceso posee la capacidad de publicar en el bus,
así que live-ingest es la mitad "live-tap" que emite esos eventos para los
módulos que ya los consumen.

## Qué es

El connector de telemetría de Claude del control plane se ejecuta fuera de
proceso como un plugin embebido; su stream `Gather` porta únicamente el `oneof`
congelado de `Observation`. Ese contrato de cable está congelado deliberadamente
(con comprobación de cambios rompedores; véase la
[política de estabilidad de la API](/es/reference/api-stability/)) y no porta
ningún extracto ni superficie de texto. Live-ingest es el productor en proceso
que suministra los dos eventos que el connector estructuralmente no puede:
`guardrail.observed` para [el módulo IX](/es/reference/modules/ix-security/) y
`voice.telemetry.observed` para el módulo XVI. No posee entidades ni superficie
REST; es un publicador hacia el [bus de eventos](/es/reference/events/).

## Qué produce — `guardrail.observed`

Este es el productor que faltaba para la cadena de detectores de seguridad que ya
consume [`guardrail.observed`](/es/reference/events/). Es **deny-closed y de
adhesión explícita**:

- **Por defecto (inspección apagada).** El módulo no se suscribe a nada, no
  publica nada y registra su mitad vacía de forma visible — nunca un no-op
  silencioso.
- **Con la adhesión explícita del operador activada.** Se suscribe a
  `edge.observed` y, para una arista cuyo recurso es una referencia de
  herramienta resuelta, deriva un extracto `tool_args` **acotado y ya expurgado**
  y lo publica como un `ObservedText` que porta únicamente campos de referencia
  no sensibles. El extracto es el *identificador* de recurso que el connector ya
  expurgó en origen (una ruta saneada, un host+ruta sin query ni credenciales, un
  nombre de programa Bash con sus argumentos descartados, una referencia de
  herramienta MCP). Live-ingest lo acota y la cadena de seguridad lo recorta de
  nuevo — triple defensa. El **contenido del argumento se descarta en el connector
  y nunca llega al bus.**

La cadena de detectores emite entonces un finding por detección de forma
automática, sobre tráfico real.

## Qué produce — `voice.telemetry.observed`

Un productor en proceso cableado únicamente para metadatos de turno de
voz/tiempo real incluidos en allow-list — nunca audio y nunca texto de
transcripción. El payload es un valor tipado que por diseño no puede portar
audio, transcripción ni PII, y el consumidor rechaza cualquier muestra con una
clave fuera de la allow-list o con una referencia de sesión/agente ausente. Sin
ningún backend de voz en tiempo real en este build, **nada lo llama**: la mitad
de observación está honestamente dormida y no fabrica telemetría hasta que un
backend la alimenta.

:::caution[Límites honestos]
- **Deny-closed por defecto.** `guardrail.observed` no publica nada salvo que el
  operador se adhiera explícitamente; la mitad vacía se registra, no se oculta.
- **La cobertura de detección es estrecha, y se afirma como tal.** Como solo hay
  disponibles en proceso *referencias* de argumento ya expurgadas, las
  detecciones realistas en esta superficie son PII o un secreto embebido en una
  referencia, y patrones de recurso anómalos/sensibles. **Prompt-injection y
  jailbreak están fuera de alcance** — necesitan el *contenido* del argumento, que
  el connector descarta. Las superficies `input` / `output` / `tool_result`
  requieren una fuente de contenido en proceso que este build no tiene bajo el
  transporte fuera de proceso y el cable congelado.
- **La telemetría de voz está dormida.** No existe ningún backend de tiempo real
  en este build, así que esa mitad no produce nada en lugar de inventar muestras.
- **Nunca mueve contenido crudo y nunca amplía la captura del connector.** Los
  datos mínimos son una propiedad del propio cable, no un ajuste superpuesto.
:::

## Relacionado

- [Referencia del bus de eventos](/es/reference/events/) — el payload `guardrail.observed` / `ObservedText`
  (un extracto expurgado sobre un fallback JSON, no la suma sellada) y `edge.observed`.
- [Módulo IX — seguridad, guardrails y auditoría](/es/reference/modules/ix-security/) — la
  cadena de detectores que consume el feed `guardrail.observed` que este módulo publica.
- [Módulo XVI — agentes de voz y tiempo real](/es/reference/modules/xvi-voice/) — el consumidor
  de la mitad (dormida) `voice.telemetry.observed`.
- [Módulo II — operación en vivo y sesiones](/es/reference/modules/ii-sessions/) — deriva su
  propio `goal` / `agent_ref` / `summary` directamente de señales que ya consume, en lugar
  de hacerlo vía un evento de live-ingest.
- [Catálogo de módulos](/es/reference/modules/overview/) — los 30 módulos y la honesta
  división Gobierno/Observación-frente-a-Actuación que respalda este productor en proceso.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — dónde se sitúan los módulos en proceso
  y los connectors fuera de proceso.
- [Honestidad y límites](/es/start/honesty-and-limits/) — por qué las mitades vacías se declaran, no se falsean.
