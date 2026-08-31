---
title: "Conectar Claude Code (la ruta cooperativa)"
description: "Apunta el exportador de OpenTelemetry de Claude Code al motor y cabléalo como fuente para que su telemetría de herramientas — más la introspección MCP no confiable — alimente el access map R/RW."
---

Claude Code es la **fuente cooperativa canónica** para Olivares AI. Emite telemetría
OpenTelemetry (OTLP) sobre las herramientas que ejecuta, y los servidores MCP con los que
habla exponen pistas de introspección (`readOnlyHint` / `destructiveHint`) sobre si una
herramienta lee o escribe. Juntas, estas alimentan el **módulo III — el access map R/RW**
con aristas de alta fidelidad, atribuidas al agente, la mitad cooperativa de la imagen
permitido-vs-observado.

Esta página cablea esa ruta: apunta el exportador OTLP de Claude Code al receptor del
motor, y luego declara la fuente para que su telemetría se convierta en aristas de acceso.
Para el mecanismo general de cableado de fuentes y dónde encaja esto, consulta
[Conectar una fuente](/es/how-to/connect-a-source/) y la
[visión general de la arquitectura](/es/explanation/architecture/overview/). Para la forma de
los eventos normalizados que esto produce, consulta la
[referencia de eventos](/es/reference/events/).

:::note[Cooperativa, no autoritativa]
La ruta cooperativa es de **alta fidelidad pero escalonada por confianza**. La telemetría
de herramientas OTLP se atribuye a una sesión de agente concreta; las anotaciones MCP son
una *señal* R/RW útil pero **no confiables según la especificación MCP** y se corroboran,
nunca se confía en ellas solas (ver [Honestidad y límites](/es/start/honesty-and-limits/)).
Para actividad fuera de la cooperación del agente — o para detectar un agente que deja de
emitir — combina esto con un respaldo no cooperativo (kernel/eBPF) y auditoría nativa del
almacén (pgAudit, CloudTrail). Esta página es solo la fuente cooperativa.
:::

## Qué obtienes de esta fuente

Una vez cableada, la telemetría de Claude Code se normaliza al modelo de datos del motor y
se alimenta al módulo III:

| Salida | Procedencia | Notas |
|---|---|---|
| **Arista de acceso** `agent session → resource (read/write)` | fuente de señal `otel` | confianza `attributed` — el origen es una sesión concreta, no una cuenta de servicio compartida |
| **Arista de servidor MCP** `session → MCP server` | fuente de señal `otel` | modo `unknown` (una conexión no es en sí un acceso; esto es topología/inventario) |
| **Pista R/RW de la introspección MCP** | fuente de señal `mcp_annotation` | **no confiable** — una señal corroborante, nunca una arista por sí sola |
| **Muestra de coste** (uso de modelo por petición) | la telemetría de api-request | alimenta FinOps, no el access map |
| **Finding** (anti-evasión) | huecos de telemetría / herramientas denegadas | una sesión que deja de emitir mientras sigue activa se marca |

El connector es **read-first y de datos mínimos**: registra la *relación* (qué sesión tocó
qué recurso, lectura o escritura), nunca el payload. Una entrada de herramienta en bruto o
un comando de shell — que pueden transportar un secreto o PII — se reducen a una referencia
de recurso expurgada antes de convertirse siquiera en una observación. Esa postura es la
predeterminada; retener cualquier contenido es una suscripción explícita acotada por
categoría.

## Cómo funciona el cableado

Hay dos mitades, y se encuentran en un socket de loopback en el host donde se ejecuta
Claude Code.

1. **El motor expone un receptor OTLP como ingesta del núcleo.** El connector cooperativo
   ejecuta un receptor OTLP (gRPC y HTTP) para la salida OpenTelemetry propia de Claude
   Code, más un endpoint para sus hooks de herramienta. Se **enlaza a loopback por
   defecto** — la ingesta cooperativa es sin autenticar, así que no debe ser alcanzable
   fuera del host. Mantenlo en loopback; el respaldo fuera del host es el collector de
   kernel, no un puerto OTLP público.
2. **Apuntas el exportador OTLP de Claude Code a ese receptor**, y **declaras la fuente**
   para que el motor sepa que debe ejecutarla para tu tenant.

```
  Claude Code (agent host)                 Olivares AI engine
  ┌──────────────────────────┐             ┌─────────────────────────────┐
  │ OTLP exporter            │── loopback ─▶│ cooperative OTLP receiver   │
  │ (OTEL_* env on the CLI)  │   (4317/4318)│ → normalize → access edges  │
  │ MCP servers (R/RW hints) │             │ → module III (R/RW map)     │
  └──────────────────────────┘             └─────────────────────────────┘
```

:::caution[El receptor es sin autenticar y solo de loopback por defecto]
Como la ingesta cooperativa acepta telemetría sin autenticar al emisor, cualquiera que
pueda alcanzar el socket puede falsificar aristas. El receptor se enlaza a loopback por
defecto precisamente por esta razón. Enlazarlo a una dirección que no sea de loopback es
una suscripción peligrosa y explícita; no lo expongas en una red compartida. Los agentes
fuera del host deberían observarse con el respaldo no cooperativo en su lugar.
:::

## Paso 1 — Apunta Claude Code al receptor

Claude Code se configura a través de sus propias variables de entorno de OpenTelemetry. En
el host del agente, activa su exportación OTLP y dirígela al receptor de loopback del
motor. El receptor del motor sigue los puertos estándar de OpenTelemetry (gRPC y HTTP);
establece el endpoint del exportador de Claude Code a la dirección de loopback y protocolo
coincidentes.

:::note[Los nombres exactos de las variables OTEL pertenecen a Claude Code, no a este producto]
El exportador se configura con los propios ajustes de Claude Code / OpenTelemetry (activar
telemetría, elegir el protocolo OTLP, establecer el endpoint). Esos nombres los definen
Claude Code y el SDK de OTel — consulta la documentación de telemetría de Claude Code para
los nombres de variable actuales en lugar de copiar una lista aquí. Lo que este producto
posee es el **receptor** al que apuntan y la **declaración de fuente** de más abajo.
:::

Por defecto el connector retiene solo telemetría **estructural** — atributos de sesión e
identidad, nombres de herramienta, modo R/RW, timing — y nunca texto de prompt, cuerpos de
herramienta ni cuerpos de API en bruto, aunque Claude Code esté configurado para emitirlos.
Déjalo así a menos que tengas una razón específica y auditada para retener una categoría de
contenido.

## Paso 2 — Declara la fuente

Las fuentes reales (no de demo) se cablean desde un único fichero de configuración propiedad
del operador, nombrado por la variable de entorno `OLIVARES_SOURCES_CONFIG`, que el motor
lee **antes de arrancar**. Los secretos viven por valor en ese fichero de operador, nunca en
el almacén. Cada entrada nombra la fuente, su `kind`, el tenant al que pertenece y un bloque
`config` por fuente:

```json
{
  "sources": [
    {
      "name": "claude",
      "kind": "claude",
      "tenant": "<tenant-ref>",
      "config": {
        "grpc_addr": "127.0.0.1:4317"
      }
    }
  ]
}
```

- **`name`** es tu etiqueta para esta instancia de fuente.
- **`kind`** selecciona el connector cooperativo de Claude Code.
- **`tenant`** acota cada arista que produce a un único tenant (las lecturas del módulo III
  son acotadas por tenant y privilegiadas).
- **`config`** contiene los ajustes propios del connector — por ejemplo la dirección de
  loopback a la que se enlaza el receptor OTLP. El connector enlaza su receptor por sí mismo
  en lugar de tomar prestado el del agente, de modo que desactivar una variable OTEL de
  Claude Code no puede apagar el collector en silencio.

:::caution[Confirma las claves de config del connector contra el descriptor publicado]
El connector publica su propio esquema de configuración (su descriptor lista cada clave,
tipo, valor por defecto y descripción). El bloque `config` de arriba muestra la clave
representativa de dirección de receptor; **no inventes claves adicionales** a partir de esta
página. Lee el descriptor que reporta el connector — o
[la referencia de configuración](/es/reference/configuration/) — para la lista autoritativa y
versionada (direcciones de receptor, la ruta del hook, ventanas de correlación/silencio, la
allowlist de captura de contenido y los campos de gobernanza opt-in). Un valor cada vez,
verificado contra lo que tu build realmente entrega.
:::

Una **fuente no configurada o vacía avisa honestamente** en lugar de fallar: un `kind`
desconocido, no embebido o que no logra cargar se reporta en el arranque, nunca se descarta
en silencio a un no-op. Tras editar el fichero, reinicia el motor para que la composition
root lo vuelva a leer.

## Paso 3 — Verifica que están llegando aristas

Con Claude Code exportando y la fuente declarada, ejecuta una sesión de Claude Code que toque
un recurso (lee un fichero, ejecuta un comando, llama a una herramienta MCP), y luego mira el
access map. Ver el grafo de accesos es una **acción privilegiada, acotada por tenant y
auditada** (rol editor en adelante — nunca el viewer más bajo), así que usa un token con el
rol correcto:

- El grafo de accesos se sirve en la ruta de módulo `/v1/m/accessmap/graph`.
- El resultado permitido-vs-observado — el **drift** de mínimo privilegio — está en
  `/v1/m/accessmap/drift`.

Estas rutas de módulo son alcanzables pero deliberadamente **no** están en el documento
OpenAPI servido; sus contratos viven en las interfaces tipadas Go/TS del producto. Para el
recorrido de extremo a extremo desde un motor nuevo hasta un grafo poblado, sigue el
[tutorial De cero a grafo](/es/tutorials/zero-to-graph/).

Deberías ver aristas cuya fuente de señal es `otel`, atribuidas a la sesión de Claude Code.
Si la introspección MCP aportó una pista R/RW, esa llega como una señal `mcp_annotation`
separada que corrobora — pero no establece por sí sola — el modo de la arista.

## Límites honestos de esta ruta

- **Las anotaciones MCP no son confiables.** `readOnlyHint` / `destructiveHint` son pistas
  orientativas que un servidor declara sobre sí mismo; la especificación MCP dice que los
  clientes deben tratarlas como no confiables. El producto las muestra como una señal
  corroborante y muestra la confianza honestamente — nunca eleva una arista a "solo lectura"
  basándose en una pista sola.
- **La atribución depende de la identidad por agente.** Las aristas se atribuyen a una
  identidad de sesión. Un pool de agentes que comparte una cuenta de servicio colapsa la
  atribución; resolver eso es una cuestión de gobernanza (emitir y aplicar identidad por
  agente), no algo que este connector pueda fabricar.
- **Es cooperativa.** Ve lo que el agente reporta. Un agente que nunca emite, o actividad
  que ocurre fuera de la ruta del agente, es invisible para esta fuente por construcción —
  que es exactamente por qué el respaldo de kernel no cooperativo y la auditoría nativa del
  almacén existen junto a ella.
- **Profundidad en fase de diseño.** Buena parte de la plataforma es pre-1.0. Trata las
  capacidades aquí como la ruta de ingesta cooperativa verificada; allí donde un módulo o
  campo downstream aún no esté construido, el producto lo dice en lugar de insinuar
  cobertura.

## Próximos pasos

- [Conectar una fuente](/es/how-to/connect-a-source/) — el modelo general de cableado de
  fuentes (cooperativo y no cooperativo).
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — convierte el drift observado en una
  decisión de mínimo privilegio.
- [Referencia de eventos](/es/reference/events/) — las observaciones normalizadas que esta
  fuente emite.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — dónde encaja la
  ruta cooperativa en la plataforma.
