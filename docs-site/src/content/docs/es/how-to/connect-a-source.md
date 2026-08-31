---
title: "Conectar una fuente"
description: "Cablea una fuente de observación real al control plane, comprende el modelo de connectors y elige la señal correcta por sistema."
---

Esta página explica el modelo general de connectors y cómo cablear una fuente real al motor. Si solo quieres conectar un agente de programación, empieza por [Conectar Claude Code](/es/how-to/connect-claude-code/) — esa es una fuente concreta en la ruta cooperativa, y esta página es el modelo que la sustenta.

## El modelo de connectors

Una fuente hace un único trabajo: **observa** un sistema externo y **emite observaciones normalizadas**. Nunca se sitúa en la ruta de datos, nunca hace de proxy del tráfico y nunca lee payloads. El access map R/RW se construye a partir de lo que la fuente reporta, no de interceptar lo que circula.

En concreto, una fuente implementa una interfaz pequeña — `Open` (configurar una vez), `Gather` (ejecutar, emitiendo), `Close` (liberar) — y durante `Gather` entrega al motor una observación cada vez a través de un sink. El motor es dueño de la planificación: una fuente de streaming (un tail de log, un receptor) se bloquea en `Gather` y emite hasta que se cancela; una fuente por lotes hace su trabajo y retorna, y el motor decide cuándo volver a ejecutarla. El connector nunca posee su propio temporizador.

Hay exactamente tres tipos de observación que una fuente puede emitir:

| Observación | Qué transporta | Usada por |
|---|---|---|
| `edge` | Un origen (agente / identidad / sesión) tocó un recurso, con un modo de lectura/escritura | El access map R/RW |
| `cost` | Coste de uso de modelo/proveedor | FinOps |
| `finding` | Un hallazgo de guardrail / red-team / forense | Seguridad |

El conjunto está cerrado por diseño — un tercero no puede introducir un nuevo tipo de observación. El motor **eleva** cada observación emitida al event bus en proceso, donde los módulos la consumen sin acoplarse a la fuente que la produjo. Para el access map en concreto, el motor resuelve las referencias en cadena del connector a entidades y fusiona la observación en una arista de acceso persistida.

:::note[Datos mínimos, por contrato]
Una observación de tipo edge transporta solo identificadores y una clasificación de lectura/escritura — nunca cuerpos SQL, payloads de petición, secretos ni PII. Un finding transporta un hash de cualquier detalle sensible, nunca el detalle en sí. Esto es una propiedad del vocabulario de cable que habla el connector, no una opción de configuración que puedas desactivar. Consulta la [visión general de la arquitectura](/es/explanation/architecture/overview/) para ver dónde encaja esto en el diseño read-first.
:::

### Los connectors son Apache-2.0 y nunca importan el núcleo

Un connector importa el SDK de connectors y nada más del producto. Nunca importa `/core` (el motor AGPL). Esa frontera se aplica en CI, y es lo que permite que los connectors se publiquen bajo Apache-2.0 y que terceros construyan los suyos propios sin fricción de copyleft. El mismo binario de connector se ejecuta en proceso o fuera de proceso sobre gRPC de forma idéntica. Consulta [Open core y licencias](/es/explanation/open-core-and-licensing/) para la frontera completa.

## Procedencia y confianza: por qué importa la fuente

Cada arista registra **qué fuente la produjo** y un nivel de **confianza**, y el producto muestra ambos en lugar de colapsarlos. Una lectura de `pg_audit` y una pista de `mcp_annotation` no son la misma evidencia y nunca se tratan como la misma.

Los dos niveles de confianza son honestos, no cosméticos:

- **`attributed`** — el acceso está firmemente ligado a su origen (por ejemplo, una identidad por agente presente en el rastro de auditoría).
- **`approximate`** — la atribución es inferida o imprecisa (una cuenta de servicio compartida, o un almacén cuya auditoría no puede separar limpiamente a los llamantes).

El modo de acceso es uno de `unknown`, `read`, `write`, `readwrite`. `unknown` es explícito y nunca se adivina — el producto prefiere mostrar "no pudimos clasificar esto" antes que fabricar una etiqueta de lectura/escritura.

## Categorías de fuente first-party, por señal

Las fuentes first-party difieren según la **señal** que transportan. Elige la fuente según lo que el sistema que estás observando puede decirte honestamente.

### `pg_audit` — READ/WRITE de PostgreSQL

La fuente pgAudit hace tail del propio log de auditoría estructurado de PostgreSQL y emite una arista por cada acceso a datos auditado. El modo de lectura/escritura se toma **literalmente del campo CLASS de pgAudit** (READ, WRITE, DDL) — nunca se infiere del texto SQL. El origen es el rol o `application_name` al que el log atribuye el acceso. El connector es de solo lectura sobre el fichero de log; nunca se conecta a la base de datos ni escribe en ella. Este es el nivel limpio: un almacén objeto/relacional que clasifica el acceso en su rastro nativo.

### `cloudtrail` — readOnly de AWS S3

La fuente CloudTrail lee los ficheros de log de CloudTrail y emite una arista por cada evento de S3. El modo de lectura/escritura se toma **literalmente del campo `readOnly` de CloudTrail**, nunca se infiere. El origen es el principal de IAM al que CloudTrail atribuye la llamada. Un rol asumido compartido entre muchos llamantes se marca `approximate`, deliberadamente, porque el rastro no puede separar a los llamantes reales que hay detrás.

### `otel` — agentes cooperativos

Esta es la ruta cooperativa: un agente que emite telemetría de herramientas OpenTelemetry reporta lo que hizo, y el motor lo ingiere. Claude Code es la fuente first-party canónica aquí, combinando telemetría OTLP con introspección MCP — ver [Conectar Claude Code](/es/how-to/connect-claude-code/). La telemetría cooperativa es la señal de mayor fidelidad cuando está presente, pero depende de que el agente sea cooperativo, razón por la cual existe un respaldo a nivel de kernel.

### `ebpf` — respaldo de kernel Tetragon (ruta no cooperativa)

La fuente eBPF es la mitad anti-evasión del map: donde la ruta cooperativa ve lo que un agente *reporta*, esta ve lo que el kernel realmente hizo — lecturas/escrituras de ficheros y conexiones de red — incluso cuando un agente desactiva su propia telemetría. Se ejecuta **fuera del control del agente**.

Dos restricciones honestas la definen:

- **No** carga programas eBPF por sí misma. La captura del kernel la hace Tetragon, desplegado como un servicio endurecido aparte; esta fuente es un consumidor de solo lectura del flujo de eventos de Tetragon y no necesita capacidades de kernel propias.
- Es **ciega al cuerpo TLS**. Observa relaciones de acceso, nunca payloads.

Sus aristas son siempre `approximate`, por una razón concreta: el kernel atribuye un acceso a un proceso o contenedor — una identidad de runtime — no a un agente resuelto. El acceso en sí es ground truth (la syscall ocurrió); la confianza cualifica la *atribución*, que el módulo de access-map mejora una vez que liga la identidad a un agente.

:::caution[El respaldo de kernel está en fase de diseño en su profundidad no cooperativa]
La ruta cooperativa (auditoría nativa del almacén, OTEL) es el caso verificado y de alta fidelidad. El respaldo de kernel es sólido en diseño pero su atribución de extremo a extremo es la parte que aún se está demostrando. Trátalo como un respaldo que eleva el suelo, no como una fuente primaria terminada. Consulta [Honestidad y límites](/es/start/honesty-and-limits/).
:::

### `mcp_annotation` — no confiable

La fuente de introspección MCP lista las herramientas, recursos y prompts de un servidor y deriva una *pista* de lectura/escritura de los `readOnlyHint` / `destructiveHint` de cada herramienta. Según la especificación MCP un cliente **DEBE considerar estas anotaciones como no confiables** a menos que el propio servidor sea de confianza, y los valores por defecto son asimétricos. Por eso esta señal es una **pista de capacidad declarada, nunca un acceso observado**: cada una de esas aristas es `approximate` y se marca ni observada ni permitida. Aporta la *superficie de capacidad* para contrastar — no evidencia de que algo se hiciera realmente. Debe corroborarse con una fuente observada, nunca confiarse en ella sola.

## La dependencia dura: identidad por agente

La atribución solo es tan buena como la identidad que el sistema subyacente registra. La auditoría nativa atribuye un acceso a una **credencial o rol**, no a un agente. Si muchos agentes comparten una cuenta de servicio o un pool de conexiones, cada acceso observado colapsa sobre esa única identidad y la atribución pasa a ser `approximate` — el producto lo dirá en lugar de fingir que puede distinguir a los agentes.

Para obtener aristas `attributed`, da a cada agente su propia identidad. Este es el puente hacia la gobernanza: emitir o aplicar identidad por agente es lo que afila el access map.

:::tip[Si la atribución parece tosca, comprueba primero la identidad]
Antes de sospechar del connector, comprueba si los agentes comparten una credencial. Una cuenta de servicio compartida es la razón más común de que un almacén limpio siga produciendo aristas `approximate`.
:::

## Cobertura por niveles — sé realista

La cobertura se escalona según lo que la superficie de auditoría de un sistema puede sostener honestamente:

- **Limpio** — bases de datos SQL, almacenes objeto y warehouses que clasifican el acceso de forma nativa (Postgres, S3 y similares). La lectura/escritura se toma literalmente.
- **Con pérdidas** — almacenes cuya auditoría no puede separar limpiamente lectura de escritura ni llamante de llamante (almacenes documentales y vectoriales). Las aristas aterrizan, pero a menudo `approximate`.
- **Imposible de forma pasiva** — sistemas sin superficie de auditoría pasiva utilizable (cachés en memoria, bases de datos embebidas de fichero único). No hay señal read-first honesta que capturar; el producto no finge lo contrario.

Elige el nivel deliberadamente. Un almacén de nivel limpio con identidad por agente es donde el map está más afilado.

## Cablear una fuente real

Las fuentes reales (no de demo) se cablean desde un único fichero de configuración de operador nombrado por la variable de entorno `OLIVARES_SOURCES_CONFIG`, leído **antes de que el motor arranque**. La configuración es un documento JSON; los secretos viven en ese fichero (referenciados por valor) y el motor nunca los persiste.

El documento declara una lista de fuentes. Cada entrada de fuente selecciona un connector por kind, nombra el tenant al que pertenecen sus observaciones y transporta los ajustes propios del connector. La forma general es:

```json
{
  "sources": [
    {
      "name": "prod-postgres",
      "kind": "pgaudit",
      "tenant": "acme",
      "config": {
        "...": "connector-specific settings"
      }
    }
  ]
}
```

Los campos por encima del bloque `config` por connector — un nombre de fuente, el `kind` del connector, el `tenant` propietario y un intervalo de poll opcional para fuentes por lotes — son el contrato de cableado estable.

:::caution[Las claves de config por connector se describen genéricamente aquí a propósito]
Las claves exactas dentro del bloque `config` de cada connector (rutas de log, endpoints, referencias de credenciales) son propiedad de cada connector y no se reproducen aquí, porque publicar una clave no verificada sería peor que omitirla. Lee la propia documentación del connector para sus ajustes, o descríbelo genéricamente hasta que hayas confirmado las claves contra el connector que estás desplegando. No copies esquema que no hayas verificado. Consulta [Honestidad y límites](/es/start/honesty-and-limits/).
:::

### Una fuente no configurada avisa honestamente

El motor falla de forma segura, no ruidosa, cuando no hay nada cableado:

- Si `OLIVARES_SOURCES_CONFIG` está **sin establecer**, el motor arranca sin fuentes.
- Si el fichero **falta, no es legible o no es JSON válido**, el motor **avisa y continúa** sin fuentes — no se cae en el arranque.
- Si la lista de fuentes está **vacía**, el motor avisa de que ningún connector ingerirá y de que el estate está corriendo sin tráfico en vivo.

En todos los casos el log de arranque te dice con claridad que no hay nada real cableado, en lugar de aparentar salud en silencio con un map vacío. Un aviso honesto es el diseño: un access map vacío nunca debería parecer uno limpio.

## Dónde se ejecuta esto

El data plane — los collectors que ejecutan estas fuentes — **siempre se ejecuta en la infraestructura del cliente**, ya sea el control plane un único binario self-hosted, un despliegue distribuido o air-gapped. La fuente observa localmente y el motor ingiere. No hay telemetría obligatoria ni egreso del plano de control de forma predeterminada. Solo cruza tu perímetro lo que **tú** configuras para que lo cruce: llamadas a tus API de modelos, las salidas SIEM/webhook que conectas y un proveedor externo de embeddings si aprovisionas uno. Consulta [Self-hosting](/es/how-to/self-hosting/) y [Instalación air-gap](/es/how-to/air-gap-install/) para las topologías de despliegue.

## Relacionado

- [Conectar Claude Code](/es/how-to/connect-claude-code/) — la ruta cooperativa `otel`, de extremo a extremo.
- [Visión general de módulos](/es/reference/modules/overview/) — los módulos que consumen estas observaciones (inventario, el access map R/RW, FinOps, seguridad).
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — dónde encajan el SDK de connectors, el event bus y el access map en el diseño.
