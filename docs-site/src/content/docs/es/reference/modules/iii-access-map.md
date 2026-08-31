---
title: "Módulo III — el access map de lectura/escritura"
description: >-
  Una capacidad diferenciada clave: un access map de lectura/escritura de cada
  edge origen→recurso, con el diff Permitido-vs-Observado (least-privilege drift).
  Cómo se construyen, clasifican y se confía en los edges, y cuáles son los límites.
---

El módulo III es el **access map de lectura/escritura**: qué origen (agente,
identidad, sesión) toca qué recurso, clasificado como lectura o lectura-escritura, y
el **diff Permitido-vs-Observado** que aflora el least-privilege drift. Es una de las
capacidades más útiles y diferenciadas del producto — uno de los 30 módulos, no el
producto entero. Esta página es la referencia de lo que el mapa es y de cómo leerlo
honestamente.

## El edge

El mapa es un grafo de **edges**. Cada edge es el hecho normalizado de datos mínimos
`origin → resource`, que lleva:

| Campo | Valores | Significado |
|---|---|---|
| **mode** | `read` \| `write` \| `readwrite` \| `unknown` | la clasificación de lectura/escritura (`unknown` cuando no puede determinarse — nunca se adivina) |
| **source** | `otel` \| `mcp_annotation` \| `pg_audit` \| `cloudtrail` \| `ebpf` \| `policy` \| `a2a` | qué señal produjo el edge |
| **confidence** | `attributed` \| `approximate` | con qué firmeza el acceso está ligado al origen |

Los edges llegan al bus de eventos como eventos [`edge.observed`](/es/reference/events/),
y el motor los fusiona en la entidad persistida `AccessEdge` — que a su vez lleva tanto
el lado **permitido** como el **observado**, de modo que el access map es una **vista
sobre el modelo de datos general**, no un almacén separado.

## Cómo se construyen los edges

El módulo III cruza dos caminos:

- **Camino cooperativo** — agentes que emiten OpenTelemetry (`otel`) y exponen
  servidores MCP. Combinado con **auditoría nativa del almacén**, esto es de alta
  fidelidad: pgAudit de Postgres (`pg_audit`) clasifica READ/WRITE de forma literal;
  AWS CloudTrail (`cloudtrail`) da el `readOnly` de S3; los warehouses, igual.
- **Camino no cooperativo** — un **backstop eBPF/Tetragon** a nivel de kernel
  (`ebpf`) registra `MAY_READ`/`MAY_WRITE` a nivel de syscall, fuera del control del
  agente (anti-evasión), ciego al cuerpo cifrado.

Las anotaciones de herramienta MCP (`readOnlyHint`/`destructiveHint`, fuente
`mcp_annotation`) son una señal útil pero **no fiables según la especificación MCP**
— el producto las **corrobora** y nunca confía en ellas por sí solas.

El lado **permitido** (fuente `policy`) proviene de los grants declarados; el lado
**observado** proviene de las señales anteriores.

## Permitido vs Observado (least-privilege drift)

La vista que lo define es el **diff** entre lo que un origen tiene *permitido* tocar y
lo que se le *observa* tocando. Aflora:

- **accesos inesperados** — un origen usó un recurso que nunca se le concedió;
- **grants sin usar** — un permiso que ningún origen ejerció jamás;
- **reconciliación-pendiente** — un acceso que el sistema aún no puede atribuir con
  firmeza.

El [tutorial de cero al grafo](/es/tutorials/zero-to-graph/) alcanza un resultado de
drift poblado sobre el estate de demostración.

:::caution[Límites honestos]
- **La identidad por agente es una dependencia dura.** La auditoría atribuye la
  actividad a una credencial o rol, no inherentemente a un agente. Una cuenta de
  servicio compartida con un pool de conexiones colapsa la atribución a
  `approximate`. Gobernar bien significa emitir identidad por agente (el puente al
  módulo VI).
- **La cobertura es escalonada.** *Limpia* en almacenes con auditoría nativa (SQL,
  almacenamiento de objetos, warehouses); *con pérdidas* en algunos almacenes
  (documento/vector); **imposible de reconstruir pasivamente** en otros (p. ej.
  Redis, SQLite, D1). Un edge ausente **no** es prueba de que un acceso no ocurriera
  allí donde la cobertura es con pérdidas o ausente.
- **`unknown` y `approximate` se muestran, no se ocultan.** El producto nunca
  fabrica una clasificación o certeza que no tiene.
:::

## Leer el mapa

Los resultados del access map — incluido el drift Permitido-vs-Observado — los sirven
rutas del módulo publicadas en la referencia separada y **beta** de
[rutas de módulos](/reference/api-beta/) (no en el contrato estable del núcleo); sus
formas a nivel de campo viven en las interfaces tipadas Go/TypeScript del producto, y
la UI web renderiza el grafo y la capa de drift sobre ellas. Leer el grafo de acceso es
una acción **privilegiada, acotada al tenant y
totalmente auditada** (el rol de editor en adelante, nunca el visor más bajo) —
consulta el [modelo de seguridad](/es/explanation/security/security-model/) y el
[modelo de amenazas](/es/explanation/security/threat-model/).

## Relacionado

- [Referencia del bus de eventos](/es/reference/events/) — el evento `edge.observed` y su payload.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — dónde encaja el módulo III.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — actuar sobre el drift.
