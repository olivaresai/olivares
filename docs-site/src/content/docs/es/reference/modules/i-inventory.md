---
title: "Módulo I — inventario y descubrimiento"
description: >-
  Descubrimiento pasivo y catalogación de todo lo que hay en el estate — agentes,
  sesiones, servidores MCP, skills, herramientas, modelos, proveedores e
  identidades no humanas. Cómo se materializan las entidades a partir de
  observaciones, qué registra el catálogo y cuáles son los límites.
---

El módulo I es el **catálogo del estate**: un inventario pasivo, dirigido por el
bus, de todo lo que existe — agentes, sesiones, instancias de Claude Code,
servidores MCP, skills, herramientas, recursos, modelos, proveedores e identidades
no humanas. Descubre *escuchando*, nunca sondeando, y registra solo relaciones,
identificadores y vitalidad — nunca payloads. Esta página es la referencia de lo
que el catálogo contiene y de lo que deliberadamente no contiene.

## Qué materializa

Los conectores emiten **observaciones**, no entidades. Publican hechos
normalizados [`edge.observed`](/es/reference/events/) y
[`cost.sampled`](/es/reference/events/) en el bus de eventos; las entidades que
estos implican nunca se envían. El módulo I **materializa** la entidad de núcleo
que cada observación nombra a partir de su referencia natural: una
`session`/`agent`/`identity` de origen, un servidor MCP, una herramienta, un
recurso, una skill y — a partir de las muestras de coste — un proveedor y un modelo
(descubiertos, **sin precios**; de eso se encarga FinOps). La materialización es
**idempotente** bajo entrega de al-menos-una-vez: find-or-create sobre la clave
natural, de modo que la misma observación vista dos veces nunca duplica una
entidad.

## Su contrato y entidades

El módulo registra una entidad propia, `inventory.catalog_entry` — una capa de
descubrimiento adjunta a cada entidad de núcleo materializada. Registra *cómo* se
encontró algo, no *qué* hizo: una lista de fuentes de señal, los hosts en los que
se vio, marcas temporales de primera y última observación, un contador de
ocurrencias y un `status` de vitalidad de `active` o `stale`. Un **barrido de
obsolescencia** periódico marca una entrada como `stale` cuando no se ha visto
dentro de la ventana configurada, y la vuelve a `active` en el momento en que
reaparece; el barrido se ejecuta solo sobre los tenants que el módulo ha observado
de hecho (no puede, ni lo hace, enumerar tenants). La superficie de lectura es
pequeña y de solo lectura: un `summary` con conteo por tipo y fuente, un listado
paginado de `entities` filtrable por tipo y estado, y una vista de detalle de
entidad única. Cada lectura requiere un permiso de lectura con namespace, acotado
al tenant (basta el nivel de visor más bajo); la ingesta es de alta frecuencia y no
se audita por escritura. Las formas completas viven en la [referencia del bus de
eventos](/es/reference/events/) y en las interfaces tipadas del producto.

## Qué consume y qué produce

El módulo I es un **consumidor** puro. Se suscribe a `edge.observed`,
`cost.sampled` y `finding.reported` y escribe solo su propia capa de catálogo y las
entidades de núcleo que deriva. No emite eventos propios y no expone ninguna
superficie de actuación — el descubrimiento es, por naturaleza, observar-y-
catalogar. Las referencias que persiste llegan **ya expurgadas** desde los
conectores; el módulo las almacena de forma literal y no añade ningún detalle en
crudo propio, de modo que la propiedad de datos mínimos es una propiedad del cable,
sostenida de extremo a extremo.

:::caution[Límites honestos]
- **El inventario no es dueño del grafo de acceso.** Desde la decisión A
  (2026-06-03), el módulo III (el access map) es el **único escritor** del
  `AccessEdge` de lectura/escritura y el único dueño de la topología y del diff
  Permitido-vs-Observado. El inventario descubre y cataloga las *entidades* que un
  edge nombra; ya no registra el edge en sí, y no sirve ninguna ruta de topología.
  El grafo se puebla únicamente cuando el módulo III está cableado en el arranque.
- **El descubrimiento es solo tan completo como las señales.** Una entidad existe
  en el catálogo solo si algún conector la observó. La ausencia del catálogo **no**
  es prueba de ausencia en el estate allí donde la cobertura es parcial.
- **La vitalidad es obsolescencia, no salud.** `stale` significa "no visto
  recientemente", nada más; el silencio de una sesión es normal, y la salud/SLA
  formal corresponde al módulo XXII. El barrido nunca muta el propio ciclo de vida
  de la entidad de núcleo.
- **Sin detalles fabricados.** El módulo almacena identificadores, relaciones y
  contadores de vitalidad únicamente — nunca payloads, secretos, PII, comandos,
  consultas o URLs.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo I y la división honesta de Actuate.
- [Módulo III — el access map](/es/reference/modules/iii-access-map/) — el único dueño del grafo R/RW y del drift.
- [Referencia del bus de eventos](/es/reference/events/) — los eventos `edge.observed`, `cost.sampled` y `finding.reported` que consume.
- [De cero al grafo](/es/tutorials/zero-to-graph/) — poblando el catálogo y el mapa sobre el estate de demostración.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — el motor, las capas y el bus.
