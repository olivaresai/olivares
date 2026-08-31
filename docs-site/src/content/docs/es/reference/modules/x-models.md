---
title: "Módulo X — gestión de modelos y proveedores"
description: >-
  La capa de gobierno sobre todo el stack de modelos de IA — Claude, OpenAI, Gemini
  e inferencia local. Un catálogo de referencia versionado, una matriz de
  capacidades y una política de enrutamiento que resuelve una cadena primario +
  fallback; enruta pero todavía no ejecuta la llamada al modelo.
---

El módulo X gobierna **todo el stack de modelos y proveedores de IA** — Claude,
OpenAI, Gemini e inferencia local, no solo un único vendor. Es un módulo de la **capa
Core** que se sitúa *encima* de los conectores de modelo/proveedor: no reimplementa
ninguna integración de proveedor ni el gateway de inferencia. Lo que posee es la
**capa de gobierno** — un catálogo versionado, una matriz de capacidades
cross-vendor, y política de enrutamiento con nombre.

## Qué es

El módulo convierte las entidades `Provider`/`Model` desnudas que el inventario
(módulo I) descubre en un catálogo gobernado. Dos mitades:

- **Un catálogo de referencia declarado** — una tabla versionada-en-repo y
  sobreescribible por el operador de familias de modelos con sus capacidades
  declaradas de funcionalidades de API y sus **defaults de precio de lista**. Los
  precios se sellan con la fecha en que se declararon (`pricing_as_of`), son
  explícitamente *defaults a verificar contra la página de precios de cada
  proveedor*, y nunca son telemetría fabricada. Una familia sin entrada coincidente
  queda **sin precio** en lugar de recibir un precio inventado.
- **Enriquecimiento del estate en vivo** — el módulo escucha el stream
  [`cost.sampled`](/es/reference/events/) y enriquece las entidades `Model`/`Provider`
  descubiertas con familia, ventana de contexto, modalidad, precio por token y el
  conjunto de capacidades (los campos de precio que el inventario le delega).

El vocabulario de capacidades es una sola **matriz cross-vendor** — el stack completo
de Claude (prompt caching, batch, Files, citations, extended thinking, computer use,
la memory tool, gestión de contexto, vision/PDF, structured outputs) más los
análogos que cada otro vendor expone realmente — de modo que la UI renderiza una sola
matriz y una política de enrutamiento puede exigir una capacidad *a través de*
vendors. Las familias de Claude se catalogan por familia (`claude-opus`,
`claude-sonnet`, `claude-haiku`, `claude-fable`, `claude-mythos`), con las versiones deprecadas/legacy mantenidas bajo
prefijos más largos para que los ids actuales resuelvan al tier de precio actual.

## Su contrato y entidades

El enrutamiento es la superficie de actuación, y es **routing-only**:

- La **política de enrutamiento** se persiste en la entidad `Policy` del núcleo
  (`Kind="routing"`): políticas con nombre de selección / fallback / version-pinning
  (cheapest-first, lowest-latency, capability-ordered, o un modelo fijado).
  `POST …/routing-policies/{id}/resolve` resuelve una política contra el estate
  gobernado y devuelve una **cadena primario + fallback** con la razón de la elección.
  Esto es **solo lectura**: calcula una selección que el connector/gateway ejecuta
  después — el módulo no realiza **ninguna inferencia**.
- El **gobierno de API-keys / workspaces** es **solo metadatos minimal-data** — qué
  agente o equipo usa qué credencial, llevado como una pista enmascarada, nunca el
  valor del secreto.
- Un **inventario de rate-limits de Anthropic** de solo lectura (los techos que un
  gateway o proxy debe mantener sincronizados) se sirve como inventario consultable;
  nunca es un control que el módulo mute, y se degrada a una respuesta honesta
  *no-disponible-con-razón* cuando el connector de Admin de solo lectura no está
  aprovisionado.

Las lecturas de catálogo y funcionalidades no son sensibles y se gatean al tier de
viewer; las mutaciones de enrutamiento y gobierno de keys son un cambio de tier
editor, auditado; el camino de ejecución gobernada es una acción de tier admin
distinta del resolve de tier read. Las rutas se publican en la
[referencia de rutas de módulo](/reference/api-beta/) **beta** separada, no en el
contrato estable del núcleo; sus formas a nivel de campo viven en las interfaces
tipadas del producto.

## Qué consume y produce

El módulo **consume** `cost.sampled` del [bus de eventos](/es/reference/events/) para
enriquecer el catálogo con precio por token y uso reales; no introduce un nuevo tipo
de observación. En el camino de ejecución gobernada, una llamada exitosa
**produciría** un `CostSample` expurgado a FinOps — la salida del modelo va al caller,
pero aquí no se persiste en ningún sitio. El dinero nunca aparece en esta superficie:
no se devuelve ningún importe en USD, solo recuentos de tokens y el destino que
sirvió.

:::caution[Límites honestos]
- **Actuación routing-only.** El módulo **resuelve** una ruta (cadena primario +
  fallback) pero **no ejecuta la llamada al modelo**. El camino de ejecución
  gobernada es una **junta deny-closed**: sin ejecutor aprovisionado devuelve un `503`
  claro — el control plane puede *seleccionar* un modelo pero no *gastará* contra un
  proveedor. Cuando un ejecutor está conectado, un presupuesto de FinOps en su tope
  deniega el gasto *antes* de cualquier llamada al proveedor.
- **El precio declarado es un default, no una garantía.** Los precios de lista son
  defaults verificados por el operador sellados con una fecha; el coste autoritativo
  del uso real es siempre el `CostSample` derivado del connector, nunca la cifra de
  conveniencia por token. Las familias sin coincidencia se muestran sin precio — nunca
  con un precio inventado.
- **Los modelos recién anunciados se listan pero se marcan.** Un modelo preview cuyas
  capacidades no están aún verificadas contra una model card se cataloga con su
  conjunto de capacidades marcado *to-confirm* y queda sin precio, en lugar de
  inventar los datos.
- **El inventario de keys es metadato, nunca un secreto.** El módulo persiste las
  relaciones de gobierno y una pista enmascarada; el valor de la credencial nunca sale
  de la Admin API del proveedor y nunca se almacena. Algunos proveedores no exponen
  inventario de keys en absoluto — un límite documentado, no una omisión.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde se sitúa el módulo X y su estado de actuación.
- [Mapa de acceso y recursos](/es/reference/modules/iii-access-map/) — el mapa R/RW y el least-privilege drift.
- [Referencia del bus de eventos](/es/reference/events/) — el evento `cost.sampled` que este módulo consume.
- [Resumen de arquitectura](/es/explanation/architecture/overview/) — motor, capas y conectores.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — actuar sobre enrutamiento y gobierno.
- [Honestidad y límites](/es/start/honesty-and-limits/) — el contrato observar-en-amplitud / actuar-sobre-un-subconjunto.
