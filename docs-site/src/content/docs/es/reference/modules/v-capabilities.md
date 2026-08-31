---
title: "Módulo V — MCP, skills y gestión de capacidades"
description: >-
  La capa de gestión de capacidades: qué servidor MCP expone qué herramienta,
  cuáles son su transporte y sus referencias de secreto, qué agente está cableado
  a qué capacidad, su historial de versiones y su salud de conexión básica —
  gobernado, auditado y no fiable por defecto allí donde la spec MCP dice que debe
  serlo.
---

El módulo V es la **capa de gestión de capacidades**: gobierna las herramientas y
capacidades de tus agentes — qué servidor MCP expone qué herramienta, cuáles son
su transporte, alcance y configuración, qué origen está cableado a qué capacidad,
su historial de versiones y su salud de conexión básica. Se sitúa en la **capa de
gestión** y **no tiene superficie de actuación**: cataloga, gobierna y audita,
pero nunca ejecuta una herramienta ni muta un runtime MCP en vivo.

## Qué es

El módulo es una capa construida **sobre** el descubrimiento pasivo del módulo I y
la introspección de los connectors. **No** reimplementa el cliente MCP, y
deliberadamente **no** vuelve a materializar las entidades núcleo que el
inventario ya posee (los registros de servidor MCP, skill, herramienta y
recurso). En su lugar lee esas entidades núcleo y almacena solo sus **propias**
capas, indexadas por las referencias naturales ya expurgadas de los connectors y
resueltas a entidades núcleo en tiempo de lectura — una disciplina de escritor
único que lo mantiene libre de competir con el materializador del inventario.

Esto es distinto del módulo III. El módulo V responde *"¿a qué capacidad está
conectado un agente?"*; el [módulo III](/es/reference/modules/iii-access-map/)
responde *"¿qué recurso leyó o escribió un origen?"*. Son vistas separadas y el
producto nunca las confunde.

## Su contrato y entidades

El módulo V posee cuatro entidades de capa (cada una prefijada con
`capabilities.`):

| Entidad | Qué contiene |
|---|---|
| **`mcp_config`** | La configuración gestionada de un servidor MCP — transporte, alcance, una **referencia** de endpoint y **referencias de secreto**. No hay ninguna columna que pueda contener una credencial usable. |
| **`config_revision`** | Una instantánea append-only por versión de config — el historial de versiones inmutable, que sobrevive a la eliminación de la config. |
| **`wiring`** | El grafo de conexión de capacidades: una arista `origin → capability` almacenada por referencia natural, nunca por id de entidad núcleo. |
| **`health`** | La última señal de conexión observada de una capacidad (`connected` / `degraded` / `down` / `unknown`) — una señal básica, **no** un SLA. |

Dos propiedades del contrato son innegociables. **Las anotaciones de herramientas
MCP no son fiables**: los `readOnlyHint`/`destructiveHint` de una herramienta son
una pista *declarada* del servidor, que la especificación MCP dice que los
clientes deben tratar como no fiable — cada proyección de herramienta lleva una
bandera explícita de no fiable, nunca una insignia de seguridad. **Sin valores de
secreto en el cable**: una config referencia secretos por nombre, tipo y una
pista enmascarada; el backend rechaza credenciales inline en un endpoint o spec
en lugar de almacenarlas. Los datos mínimos son una propiedad del cable, no algo
de última hora.

Leer el catálogo está gobernado por RBAC y acotado por tenant. Cambiar una config
— y los secretos que referencia — es un **cambio privilegiado** registrado en el
ledger append-only y encadenado por hash, y atribuido al principal real.

## Qué consume y produce

El módulo V se alimenta del [bus de eventos](/es/reference/events/), no de su propio
polling. Reacciona a dos canales:

- **`edge.observed`** — el uso de capacidades en runtime se convierte en aristas
  `wiring`. El campo `Source` distingue las señales **observadas** (`otel`) de las
  **declaradas** (`mcp_annotation`), y un alimentador de descubrimiento de config
  más reciente etiqueta las capacidades declaradas estáticamente con una fuente
  `config`.
- **`finding.reported`** — los hallazgos de salud de conexión de los connectors
  alimentan el estado de última señal de la capa `health`.

No produce eventos propios y no despacha nada a infraestructura en vivo; su salida
la lee la UI de gestión y otros módulos a través de sus rutas tipadas.

:::caution[Límites honestos]
- **Sin actuación.** El módulo V gobierna y cataloga; nunca ejecuta una
  herramienta, marca a un servidor MCP ni muta un runtime en vivo. Es una capa de
  gestión por naturaleza.
- **Techo de confianza de las anotaciones.** `readOnlyHint`/`destructiveHint` son
  *declarados* y se exponen como **no fiables** — corroborar la intención de
  lectura/escritura contra señales reales es trabajo del módulo III, no de este
  módulo.
- **La salud de conexión no es un SLA.** La capa `health` es solo la última señal
  de conexión; el reporte formal de uptime, SLA y tendencia corresponden al módulo
  XXII.
- **El descubrimiento es tan profundo como lo sean los connectors.** Las
  capacidades observadas en runtime afloran solo una vez que un agente las ejerce;
  las superficies de Claude Code declaradas estáticamente (subagentes, Skills,
  plugins, output styles) ahora se descubren antes de la ejecución mediante un
  alimentador de config dedicado, pero emite **solo metadatos estructurales** —
  nombres, nunca cuerpos de prompt, contenidos de skill/plugin ni secretos.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde se sitúa el módulo V y su estado de actuación.
- [Módulo III — mapa de acceso y recursos](/es/reference/modules/iii-access-map/) — la vista R/RW de la que este módulo es deliberadamente distinto.
- [Referencia del bus de eventos](/es/reference/events/) — los payloads `edge.observed` y `finding.reported` que consume.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — la composición de motor más módulos.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — actuar sobre lo que el catálogo expone.
- [Honestidad y límites](/es/start/honesty-and-limits/) — el contrato honesto de gobernar-vs-actuar del producto.
