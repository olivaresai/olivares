---
title: "Módulo IV — comunicación entre agentes y orquestación"
description: >-
  El plano de observación y gobierno de cómo se coordinan los agentes: un grafo
  derivado de comunicación y delegación, agentes programados gobernados y un
  disparo en dos fases con gate de human-in-the-loop. El dispatch en vivo es un
  seam deny-closed, dicho con honestidad.
---

El módulo IV es el plano de **observación y gobierno** de cómo se coordinan los
agentes. **No** reimplementa un framework de agentes (sin
LangGraph/CrewAI/AutoGen), no ejecuta un agente y nunca genera un proceso.
Deriva un grafo de comunicación y delegación en vivo a partir de señales que ya
están en el bus, gobierna agentes programados/autónomos como declaraciones de
estado deseado y marca la evasión de cadencia — mientras que el acto de
*ejecutar* un agente solo sale a través de un seam deny-closed.

## Qué es

Dos cosas conviven una junto a otra. Primero, un **grafo derivado de
comunicación y delegación** — quién delega en quién (supervisor→worker) y quién
habla con quién — construido como una vista sobre aristas de acceso ya
observadas, hermano del access map ([módulo III](/es/reference/modules/iii-access-map/)),
nunca una segunda copia reingerida. Segundo, un registro de **agendas
gobernadas**: un agente programado o dirigido por eventos es una *declaración de
estado deseado*, y dispararlo es la única acción que afecta a producción que el
módulo expone.

## Contrato y entidades

El módulo posee tres tipos de entidad, declarados en el modelo de datos
compartido:

- **`orchestration.relation`** (upsert) — la arista derivada del grafo: un enlace
  `delegation`, `mcp_server` o `mcp_tool` entre dos referencias, con una fuente
  de señal, un `mode` de lectura/escritura, una `confidence`, recuentos y marcas
  temporales de primera/última vez visto.
- **`orchestration.schedule`** (ciclo de vida) — una declaración gobernada:
  sujeto, tipo de disparador (`cron`/`event`/`manual`), una **especificación de
  cadencia opaca que nunca se parsea para autodispararse**, un intervalo
  esperado, un factor de gracia, un estado deseado y el principal declarante
  registrado como propietario de cualquier disparo autónomo.
- **`orchestration.decision`** (**append-only**) — un ledger inmutable de cada
  petición de disparo, disparo y pérdida de cadencia, que porta el `plan_hash`,
  el estado del gate, el `op_status` y el **principal real** (nunca `system`,
  salvo para la detección de pérdida de cadencia).

Las rutas del módulo son alcanzables pero deliberadamente **no** forman parte del
contrato OpenAPI servido; sus formas a nivel de campo viven en las interfaces
tipadas del producto. **El disparo es en dos fases y con gate de HITL**: la fase
uno solicita aprobación; la fase dos revalida la aprobación y una coincidencia
estricta de `plan_hash` (anti-TOCTOU — un recambio de destino o de cadencia
invalida una aprobación obsoleta) antes de cualquier dispatch. Leer el grafo y
disparar son acciones **privilegiadas, con ámbito de tenant y plenamente
auditadas**, divididas por nivel de verbo (lectura para viewers,
declarar/redirigir para editors, **disparar** solo para admins) — véase
[gobernar y aprobar](/es/how-to/govern-and-approve/).

## Qué consume y produce en el bus

Consume exactamente un canal: [`edge.observed`](/es/reference/events/). Una arista
sesión→Task se convierte en una relación de delegación; las aristas de topología
MCP se convierten en relaciones server/tool; todo lo demás se ignora. La
vivacidad observada de un sujeto para la comprobación de cadencia se deriva de
las propias relaciones, así que no se consulta ninguna agenda por arista.
Produce findings en [`finding.reported`](/es/reference/events/):
`orchestration_cadence_miss` cuando una agenda **activa y recurrente** deja de
emitir frente a su cadencia declarada (una agenda de un solo disparo o en pausa
que simplemente terminó es silencio normal y no emite nada), y
`orchestration_ungoverned_fire` cuando un intento de disparo no encuentra ningún
gate de aprobación cableado — la brecha de gobierno se hace visible mientras el
disparo permanece denegado. La comprobación es en tiempo de lectura y con ámbito
del tenant fijado de la petición; el módulo nunca ejecuta un escaneo de fondo
entre tenants.

:::caution[Límites honestos]
- **El disparo en vivo es un seam deny-closed.** El módulo *gobierna y programa*;
  nunca actúa por sí mismo. Un disparo sale a través de un seam Dispatcher. Con
  el dispatcher sin configurar (el binario por defecto), un disparo aprobado
  devuelve un `200` honesto con estado `declared_not_fired` — el estado seguro es
  "declarado, no disparado". Un dispatcher construido y configurado por el
  operador enruta un disparo aprobado y con plan coincidente al mismo ejecutor de
  despliegue o a una tarea A2A verificada por tarjeta firmada; un error del
  dispatcher devuelve `502` y nunca avanza el last-fired. La delegación A2A en
  vivo añade su propio punto de aplicación de política deny-by-default (tarjeta
  firmada → allowlist → plan hash → aprobación) y se sujeta con gate de la misma
  forma.
- **La cobertura del grafo es parcial, y lo dice.** Cada respuesta del grafo
  porta un descriptor de cobertura. El grafo derivado cubre la delegación de
  Task, la topología MCP y — donde hay un connector A2A cableado — A2A
  peer-to-peer observado; el cross-talk de enjambre y los frameworks no-Task sin
  un connector emisor están **ausentes, no a cero**. El módulo nunca presenta el
  grafo como comunicaciones de agente completas.
- **Datos mínimos en el cable.** El módulo persiste únicamente relaciones y
  evidencia de gobierno — quién↔quién, recuentos, marcas temporales, referencias
  expurgadas — **nunca** payloads de mensajes, prompts, argumentos de
  herramientas ni secretos. No existe tal columna; las referencias sensibles se
  hashean antes de persistir. Eso es una propiedad del cable, no un ajuste.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — la capa del módulo IV y su estado honesto de actuación.
- [Referencia del bus de eventos](/es/reference/events/) — `edge.observed` de entrada, `finding.reported` de salida.
- [Mapa de acceso y recursos](/es/reference/modules/iii-access-map/) — el grafo hermano que este deriva en paralelo.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — el disparo en dos fases con human-in-the-loop.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué actúa hoy y qué sigue siendo un seam.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — dónde se sitúa el módulo IV en la capa de Inteligencia.
