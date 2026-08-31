---
title: "Fine-tuning de modelos propios e inferencia local — ejecución (planificado)"
description: >-
  Lo que sigue planificado en el lado de modelos propios: que la plataforma ejecute
  trabajos de fine-tuning y sirva inferencia local por sí misma. El registro de modelos
  propios, la admisión de modelos firmados, los registros de linaje y la evidencia AIBOM
  ya se entregan como operaciones de modelos; esta página es honesta sobre la mitad
  ejecutora que no.
---

La historia de modelos propios — gobernar **modelos que la empresa entrena u hospeda por
sí misma** — se divide en dos mitades, y solo una de ellas sigue planificada.

La **mitad gobernante se entrega hoy** como
[Módulo XXIII — operaciones de modelos](/es/reference/modules/xxiii-model-operations/): un
**registro versionado de modelos propios** (`hosted`, `fine_tuned`, `imported`), la puerta
de **admisión** de modelos firmados, **registros de linaje de datasets y de trabajos de
fine-tuning**, **registros gobernados de despliegues de inferencia local** (vLLM, Ollama,
llama.cpp, otros) con re-verificación enforce-signed en el momento del despliegue, y
generación de **AIBOM / model card** con sellado anclado al ledger. Sus entidades y
endpoints están declarados y servidos bajo las rutas beta de módulo
(`/v1/m/models/owned-models`, `/v1/m/models/model-versions`,
`/v1/m/models/finetune-jobs`, `/v1/m/models/inference-deployments`,
`/v1/m/models/aiboms`, …) — ver la [referencia de rutas de módulo](/reference/api-beta/).

Esta página cubre la **mitad ejecutora, que está planificada y deliberadamente sin
construir**: que la plataforma misma *ejecute* ese trabajo.

## Qué se entrega hoy (en otra página)

La gobernanza de modelos propios es real y está documentada en la página de
[operaciones de modelos](/es/reference/modules/xxiii-model-operations/):

- un **registro de modelos propios** con versiones inmutables, de modo que un modelo
  fine-tuned o autohospedado es una entidad gobernada de primera clase y no un endpoint
  sin gestionar;
- **trabajos de fine-tuning como registros de linaje** — inventario del trabajo de
  entrenamiento ejecutado externamente y de la versión de modelo que cada uno produjo;
- **despliegues de inferencia local como registros gobernados** — los runtimes de
  servicio que tú operas, bajo la aplicación de admisión (`require_signed`) y auditoría.

## Qué sigue planificado

- **Ejecutar trabajos de fine-tuning.** El módulo entregado registra el estado y el
  linaje de trabajos de fine-tuning ejecutados en otro lugar; la plataforma nunca inicia,
  cancela ni ejecuta un trabajo de entrenamiento, y no almacena pesos ni contenidos de
  datasets. Un pipeline que *ejecute* fine-tuning desde la plataforma es trabajo
  planificado.
- **Servir inferencia local.** Los despliegues son registros gobernados de runtimes que
  opera el operador; la plataforma no hospeda ni sirve inferencia por sí misma. El
  servicio de inferencia local de primera parte es trabajo planificado.

Para esta mitad ejecutora no hay declarado ningún esquema de trabajos, contrato de
scheduler ni contrato de runtime de servicio, y esta página deliberadamente no inventa
ninguno.

## Por qué está planificado y no entregado

La plataforma está construida para que cualquier capacidad se acople sin rearquitecturar
el resto, así que la ejecución puede añadirse después sobre las superficies de gobernanza
ya entregadas. Se colocó **después** de la v1 por una decisión explícita de producto: la
prioridad de la primera versión es gobernar los modelos y agentes que una organización ya
ejecuta, y ejecutar entrenamiento/servicio no cambia ese valor central lo suficiente como
para competir por el esfuerzo de la v1.

Cuando se construya, su costura natural ya está entregada: un fine-tune ejecutado
produciría una **versión** de modelo en el registro de
[operaciones de modelos](/es/reference/modules/xxiii-model-operations/) y pasaría la misma
puerta de **admisión** de modelos firmados que cualquier artefacto producido
externamente, con la política de la pila de proveedores permaneciendo en
[gestión de modelos y proveedores](/es/reference/modules/x-models/).

:::caution[Límites honestos]
- **Las superficies gobernantes están entregadas; las ejecutoras, no.** No leas esta
  página como una negación del registro, la admisión, los registros de linaje, la
  gobernanza de despliegues o la evidencia AIBOM — existen y están documentados en
  [operaciones de modelos](/es/reference/modules/xxiii-model-operations/).
- **Hoy no existe ninguna superficie de ejecución.** No hay pipeline de entrenamiento, ni
  scheduler de trabajos de fine-tune, ni servicio de inferencia de primera parte en el
  binario entregado, y no hay declarada ninguna entidad, endpoint ni evento para ellos —
  ni siquiera una interfaz que rechace.
- **Nada aquí es una promesa de fecha ni de profundidad.** El alcance de arriba es la
  dirección planificada; el esquema de trabajos y los contratos de runtime se diseñarán
  cuando se construya. Se dejan deliberadamente sin especificar en lugar de fabricarse.
:::

## Relacionado

- [Módulo XXIII — operaciones de modelos](/es/reference/modules/xxiii-model-operations/) — la superficie de gobernanza de modelos propios ya entregada: registro, admisión, linaje, despliegues, AIBOM.
- [Catálogo de módulos](/es/reference/modules/overview/) — los 30 módulos entregados y dónde encaja el trabajo de modelos propios.
- [Módulo X — gestión de modelos y proveedores](/es/reference/modules/x-models/) — el vecino entregado que gobierna la pila de modelos de proveedor.
- [Honestidad y límites](/es/start/honesty-and-limits/) — el contrato observar-ampliamente / actuar-sobre-un-subconjunto y qué significa "planificado".
