---
title: "Módulo XVIII — red-teaming y pruebas adversariales"
description: >-
  Un harness de robustez defensiva: una batería gateada por consentimiento de sondas adversariales
  publicadas mapeadas a OWASP Agentic y MITRE ATLAS, puntuada en un scorecard a prueba de
  manipulación. Qué prueba, la línea roja del consentimiento y sus límites honestos.
---

El módulo XVIII es un **harness de robustez defensiva**. Sondea los agentes gobernados **propios** del
cliente con una batería de casos de prueba adversariales publicados — inyección de
prompt, jailbreak, exfiltración, envenenamiento de herramientas — y puntúa su resistencia,
mapeada al **OWASP Top 10 para Aplicaciones Agénticas**, el **OWASP LLM Top 10
(2025)** y **MITRE ATLAS**. Es una suite de pruebas, no un arma: un cumplimiento o una fuga
es un finding, no un exploit entregado a nadie.

## Qué es

La batería es un catálogo de **sondas** a lo largo de cuatro familias (`injection`,
`jailbreak`, `exfil`, `tool_poisoning`). Cada sonda es una prueba de robustez *conocida y publicada*
mapeada a una referencia OWASP/ATLAS, con la expectativa de que un agente bien defendido
la **rechace** o su guardrail la **bloquee**. Los payloads son canarios benignos —
piden al agente que emita un marcador inerte, o describen una operación peligrosa sin
ejecutarla — de modo que la batería sondea el *rechazo*, no la brecha. Un **Judge**
determinista clasifica cada resultado: `blocked`/`refused` es un **pase**, `complied`/`leaked`
es un **fallo**, `error` es un fallo de ejecución, `skipped` es no-ejecutado.

Los resultados se agregan en un **scorecard**: `score = passed / (passed + failed) × 100`,
con `errors` y `skipped` deliberadamente **excluidos** del denominador — una sonda
que nunca se ejecutó nunca se cuenta como un pase. El scorecard desglosa por familia y
rastrea la cobertura de fallos de OWASP-Agentic, y es un registro **append-only y a prueba de
manipulación** para que una ejecución posterior pueda compararse contra él como línea base de regresión.

## La línea roja, su contrato y entidades

La frontera de doble uso se **aplica en el código**, no solo se enuncia en los docs. Una ejecución se ejecuta
**únicamente** contra un agente que el cliente gobierna y que ha sido explícitamente **registrado y
autorizado** como objetivo — y registrar no es consentir: un objetivo nace
`registered` con la autorización retenida, y un paso de autorización separado es la
concesión explícita. Lanzar una ejecución contra un objetivo no autorizado o desconocido se rechaza
en el gate. Registrar, autorizar y lanzar son todas acciones **de nivel admin, auditadas y
privilegiadas**; cada una deja una autoauditoría atribuida al principal real.

El módulo es dueño de tres entidades con ámbito de tenant: el **target** (un registro de consentimiento mutable
a lo largo de su ciclo de vida register → authorize → revoke), el **run** (un registro de
evaluación append-only que lleva los agregados y la puntuación) y los **results** por sonda
(append-only, una fila por sonda). Es **de datos mínimos por construcción**: el endpoint del target
es un handle opaco que el sandbox dereferencia — nunca una credencial — y un
result almacena solo un hash de un solo sentido de su detalle, nunca el payload en crudo ni la
respuesta en crudo del agente. La API del lado de lectura sirve el catálogo como **solo taxonomía** (id, familia,
título, referencia OWASP/ATLAS, severidad, superficie); los payloads de las sondas son internos y
nunca se exponen en el cable.

## Qué consume y produce

El módulo es dueño de la batería y la puntuación; **no** alcanza ningún agente por sí mismo.
La ejecución se delega al runtime aislado sobre un seam `Sandbox` — el sandbox es
el único componente que toca el target, dentro del perímetro del cliente, con el egress
segmentado exactamente al target autorizado y todo lo demás denegado. Cada sonda fallida
se persiste como un `Finding` de core (`kind = "redteam"`) dentro de la transacción de la ejecución,
y un evento `finding.reported` de datos mínimos (`kind = "redteam_failure"`)
se publica en el [bus de eventos](/es/reference/events/) para los consumidores de entrega y
cumplimiento — ambos llevan solo una referencia de sujeto, título y hash de detalle.

## Límites honestos

:::caution[Límites honestos]
- **Sin un sandbox cableado, una ejecución está DEGRADADA, nunca es un falso pase.** El seam de
  ejecución por defecto no alcanza ningún agente: cada sonda se reporta `skipped`, el estado de la ejecución
  es `degraded`, y la puntuación refleja que no se probó nada. El harness incluye la
  batería y la puntuación completas hoy; la ejecución en vivo depende de un runtime aislado
  aprovisionado, y un despliegue no aprovisionado es honesto al respecto en lugar de puntuar un
  objetivo no probado.
- **Solo prueba agentes que gobiernas y autorizas.** Nunca apunta a sistemas de
  terceros, nunca escanea credenciales ajenas y no incluye ninguna capacidad puramente ofensiva.
  Un objetivo no autorizado o desconocido se rechaza — esto no es un mando de configuración.
- **Las sondas son una batería publicada y defensiva — no exploits novedosos.** Cada una mapea a
  una referencia OWASP/ATLAS. La vista de cobertura de ATLAS es un **snapshot fechado** sellado a
  una versión específica de la matriz, no paridad continua con la matriz en vivo.
- **Un fallo de ejecución del sandbox no es un veredicto.** Un resultado `error` mantiene la ejecución en marcha
  y se excluye de la puntuación; nunca cuenta como una vulnerabilidad ni como un pase.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo XVIII y su estado de actuación.
- [Módulo IX — seguridad, guardrails y auditoría](/es/reference/modules/ix-security/) — el consumidor de los findings `redteam`.
- [Referencia del bus de eventos](/es/reference/events/) — el evento `finding.reported` y su payload de datos mínimos.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — cómo se componen el engine y el runtime aislado.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — autorizar un objetivo y actuar sobre los findings.
- [Honestidad y límites](/es/start/honesty-and-limits/) — el contrato de actuación a nivel de todo el producto.
