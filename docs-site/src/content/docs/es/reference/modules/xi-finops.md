---
title: "Módulo XI — coste y FinOps de IA"
description: >-
  Contabiliza el gasto en IA desde el stream de coste, segméntalo por cualquier
  dimensión de atribución, prevé el período, y aplica presupuestos que deniegan
  el gasto en el tope — sin dinero en el cable, opt-in, y denegando cuando no se
  puede leer el libro mayor. Qué hace, y sus límites.
---

Los presupuestos y el análisis de gasto de FinOps son funciones de **[Business](https://olivares.ai/pricing)**. Community conserva el seguimiento de costes por sesión y la exportación de datos. Los presupuestos guardados antes de 0.1 se pueden consultar y eliminar, y se aplican mientras el módulo FinOps esté activo; Community no puede crearlos ni modificarlos. Las evaluaciones y los entornos de prueba siguen en Community.


El módulo XI es la capa de **coste / FinOps** para IA: contabiliza lo que reportan
los conectores de modelo y proveedor, te deja segmentar el gasto por cualquier
dimensión de atribución, prevé el período actual, y convierte un presupuesto en
aplicación real que **deniega el gasto** en el tope en lugar de solo señalarlo. Esta
página es la referencia de lo que FinOps hace hoy y dónde terminan sus garantías.

## Qué es

FinOps **no** reimplementa la integración de proveedores — consume el stream de coste
de modelo/proveedor y **contabiliza lo que los conectores derivaron o leyeron
autoritativamente**. El dinero es siempre un valor entero en micro-USD (millonésimas
de dólar), nunca un float, así que los totales nunca derivan. Es un módulo de la capa
Intelligence: posee la ingesta, los presupuestos y la analítica, y los expone a
través de su propio namespace de API gateado por RBAC y vistas de UI sin tocar el
núcleo ni a sus vecinos.

El módulo es **minimal-data por construcción**: almacena recuentos de tokens, costes
derivados y *referencias* de atribución — nunca un prompt, una completion, ni un
secreto. El coste es dato de gobierno, así que las lecturas se gatean por rol en la
API, y **ningún importe en USD se expone jamás a un usuario final** (eso es una
propiedad del cable, no un ajuste de UI).

## Sus entidades y contrato

Cada evento `cost.sampled` (un `CostSample` — ver el [bus de eventos](/es/reference/events/))
se registra de dos formas:

- el **ledger CostRecord** canónico y normalizado (una entidad del núcleo, indexada
  por id), **deduplicado por una clave natural** — la *identidad* del bucket
  (proveedor / modelo / sesión / instante más cada dimensión de atribución y
  procedencia), nunca su *valor* — de modo que un bucket abierto re-extraído o un
  reporte liquidado tarde **hace upsert in situ** en lugar de doble-contar en el
  stream at-least-once;
- una fila del **read-model de FinOps** desnormalizada e indexada por los nombres de
  atribución naturales (provider, model, agent, session, team, project), de modo que
  el gasto se agrega eficientemente por **cualquiera** de esas dimensiones —
  incluyendo el `service_tier` del proveedor.

Un **presupuesto** es una `Policy` del núcleo de tipo `budget`: una dimensión (global
/ model / provider / agent / session / team / project), un límite, un período, y
umbrales de alerta. Su `action` es uno de tres — `alert` (solo showback, el default
seguro que nunca aplica), `throttle`, o `block`. La analítica sirve el desglose del
gasto por cualquier dimensión, totales, una serie de tendencia diaria, un run-rate y
previsión de tendencia del período actual (con una banda de confianza explícita), una
vista de eficiencia de prompt-cache, y recomendaciones de optimización — cada una
fundamentada en datos registrados y **honesta sobre sus supuestos**.

## Qué consume y produce

FinOps **consume** `cost.sampled` del [bus de eventos](/es/reference/events/) y
**produce** dos efectos. En la ingesta, cuando el consumo cruza un umbral de
presupuesto que no había cruzado este período, registra la alerta y **emite un
`FindingReport`** (`finding.reported`) — *solo la señal*; la entrega a Slack / SIEM /
PagerDuty es trabajo del módulo de conectores de salida, no de FinOps.

El segundo efecto es la **aplicación**. Un presupuesto cuya `action` es `throttle` o
`block` deniega el gasto en el tope a través de una **junta `BudgetGate`** declarada
en los términos propios de cada módulo que actúa (el *fire* de la orquestación, el
*open* de la voz, el *resolve* del router de modelos); ningún módulo importa FinOps.
La junta corre **ortogonalmente al gate de aprobación** — una acción puede estar
aprobada por un humano y aun así denegarse por presupuesto — y responde sobre el gasto
efectivo-en-el-tope con una **razón sin dinero** (sin USD, sin nombre de presupuesto
en la ruta de solo lectura). Un `block` duro deniega con **HTTP 402**, un `throttle`
suave con **HTTP 429**, y la denegación se escribe en el ledger append-only y se
audita. Ver [Gobernar y aprobar](/es/how-to/govern-and-approve/).

:::caution[Límites honestos]
- **La aplicación es opt-in, no deny-closed por defecto.** Mientras FinOps pueda leer su
  libro mayor, una petición sobre la que no tenga scope ningún presupuesto que aplique nunca
  se deniega — esa ausencia es el estado normal, no un agujero de seguridad. Un
  presupuesto deniega solo cuando está *definitivamente* en su límite. Esto es deliberado
  y la inversa de la postura deny-closed del gate de aprobación.
- **Un libro mayor que el gate no puede leer deniega.** Cuando FinOps no puede leer ni
  escribir su libro mayor de presupuestos, la admisión no se puede establecer y el gasto se
  deniega: se rechazan el fire de orquestación, el open de voz, la ruta de modelo, la
  ejecución de la puerta de evals y la tarea MCP durable, y el proxy de inferencia responde
  **HTTP 503**. El lanzamiento de sesión aplica su propia postura de disponibilidad
  (`OLIVARES_SESSION_BUDGET_AVAILABILITY`): sin definir, la sesión se rechaza con
  **HTTP 503** en todas las ediciones; con `fail-open`, se lanza. En ambos casos el fallo
  queda registrado. El finding de tope-de-presupuesto emitido en la ingesta sigue
  registrando el gasto que llegó a un tope.
- **El router solo aplica los scopes que conoce pre-ejecución** (global / provider /
  model); los scopes más finos (agent, session, team, project) se aplican en las
  juntas fire/open y en el gateway de modelos, no en la resolución de ruta.
- **FinOps contabiliza; no factura.** Registra lo que reportan los conectores — se
  lleva la procedencia `billed` vs `estimated`, no se reconcilia en una factura — y un
  sample con campos cero/vacíos significa *"no reportado"*, nunca *"cero"*.
- **Sin actuación más allá de la denegación.** FinOps ni ejecuta una llamada al modelo
  ni mueve dinero; observa el stream de coste y gatea el gasto que está configurado
  para gatear.
:::

## Relacionado

- [Referencia del bus de eventos](/es/reference/events/) — los payloads `cost.sampled` / `CostSample` y `finding.reported`.
- [Catálogo de módulos](/es/reference/modules/overview/) — dónde se sitúa el módulo XI y su estado de actuación honesto.
- [Resumen de arquitectura](/es/explanation/architecture/overview/) — el motor, las capas y el stream de coste.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — actuar sobre una acción denegada por presupuesto.
- [Honestidad y límites](/es/start/honesty-and-limits/) — la política de junta deny-closed entre módulos.
