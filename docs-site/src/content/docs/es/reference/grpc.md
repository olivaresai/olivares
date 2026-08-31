---
title: Referencia gRPC — servicios, métodos y tipos de mensaje
description: >-
  Cada rpc que registran el motor de Olivares AI y el host de plugins, con su forma de
  streaming, mensajes de petición y respuesta y la cadena de método completa bajo la
  que viaja. Generado a partir de las tablas de registro de los propios servidores.
---

Olivares AI habla gRPC en dos lugares que apuntan en direcciones opuestas:

- **La API del control plane del motor** (`olivares.api.v1.ControlPlane`): un pequeño
  reflejo de la superficie REST para callers que prefieran un stub tipado. El contrato REST
  de la [referencia de la API](/reference/api/) sigue siendo el más amplio de los dos.
- **El contrato de cable de plugins** (`olivares.sdk.v1.*`): el contrato versionado que
  habla cada connector y módulo out-of-process. Es el que implementas cuando
  [construyes un connector](/es/how-to/build-a-connector/) en un lenguaje distinto de Go.

Esta página se **genera a partir de las tablas de registro que los servidores entregan a
gRPC**, no desde los archivos `.proto`. Esa distinción es intencional: un `.proto` editado
sin regenerar describe un servicio que el binario no sirve, y la comprobación que respalda
esta página informa de la discrepancia en vez de publicar la opción más vistosa. Los clientes
pueden llamar a todos los métodos enumerados aquí.

:::note[Estabilidad]
El contrato de plugins `olivares.sdk.v1` está versionado y protegido por el detector de
cambios incompatibles de buf: un cambio incompatible exige un nuevo paquete major. Nuestro
compromiso y su duración se explican en [Estabilidad de la
API](/es/reference/api-stability/).
:::

## Transporte y autenticación

Todos los métodos de los servicios siguientes salvo `GetServerInfo` requieren un principal
autenticado y autorizado. Hay dos excepciones deliberadas, nombradas aquí para que no tengas
que descubrirlas: `GetServerInfo` responde de forma anónima, y el servicio estándar
`grpc.health.v1.Health` (`Check`, `List`, `Watch`) se sirve en el mismo listener sin principal,
porque una sonda o una service mesh debe poder alcanzarlo en cada pod igual que un kubelet
alcanza `/livez`. La ausencia de un bearer token deja una petición como anónima en lugar de
rechazarla; un token presente pero no válido sí se rechaza. Se llega al servicio del control
plane por el listener gRPC del motor; los servicios de plugins se conectan por el broker de
go-plugin (connectors dentro del host) o por gRPC con TLS mutuo (un collector remoto).
Configura el listener mediante las variables `OLIVARES_*` de la
[referencia de configuración](/es/reference/configuration/).

<!-- BEGIN GENERATED olivares-grpc-reference — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

El motor y el host de plugins registran **28 rpc** repartidos en **7 servicios**. Las tablas
siguientes se leen de las tablas de registro generadas que los servidores entregan a gRPC;
si un método aparece aquí, un cliente puede llamarlo.

### `olivares.api.v1.ControlPlane`

Definido en `apiv1/api.proto`; 5 rpc.

| Método | Método completo | Tipo | Petición | Respuesta | Qué hace |
|---|---|---|---|---|---|
| `CreateAgent` | `/olivares.api.v1.ControlPlane/CreateAgent` | unary | `CreateAgentRequest` | `Agent` | Registra un agente nuevo en el inventario y devuelve el registro almacenado, incluido el identificador que usa el resto de la API. |
| `GetAgent` | `/olivares.api.v1.ControlPlane/GetAgent` | unary | `GetAgentRequest` | `Agent` | Devuelve un agente por identificador, con los mismos campos que sirve el endpoint REST de inventario. |
| `GetServerInfo` | `/olivares.api.v1.ControlPlane/GetServerInfo` | unary | `Empty` | `ServerInfo` | Informa de la versión, edición y readiness. Es el único método de este servicio que no requiere un principal autenticado. |
| `ListAgents` | `/olivares.api.v1.ControlPlane/ListAgents` | unary | `ListAgentsRequest` | `ListAgentsResponse` | Enumera, página a página, los agentes visibles para el principal que llama. |
| `VerifyAudit` | `/olivares.api.v1.ControlPlane/VerifyAudit` | unary | `VerifyAuditRequest` | `VerifyAuditResponse` | Vuelve a verificar la cadena de auditoría en un rango e informa de si los hashes siguen enlazados, incluido el estado del checkpoint. |

### `olivares.sdk.v1.ContentSourceService`

Definido en `olivaresv1/v1.proto`; 7 rpc.

| Método | Método completo | Tipo | Petición | Respuesta | Qué hace |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.ContentSourceService/Close` | unary | `Empty` | `Empty` | Finaliza la sesión abierta por Open y libera lo que el connector retuviera para ella. |
| `DeltaList` | `/olivares.sdk.v1.ContentSourceService/DeltaList` | server-streaming | `ContentDeltaRequest` | `ContentChange` (stream) | Transmite los cambios desde un cursor. Solo se llama cuando el connector anuncia la capacidad content.delta. |
| `Describe` | `/olivares.sdk.v1.ContentSourceService/Describe` | unary | `Empty` | `DescribeResponse` | Devuelve el descriptor del connector: su identidad, sus campos de configuración y las capacidades que anuncia. |
| `Fetch` | `/olivares.sdk.v1.ContentSourceService/Fetch` | unary | `ContentFetchRequest` | `ContentDocument` | Devuelve el cuerpo y los metadatos de un documento para la referencia que el host tomó del stream de List. |
| `FetchACL` | `/olivares.sdk.v1.ContentSourceService/FetchACL` | unary | `ContentFetchRequest` | `ContentACLResult` | Devuelve las referencias de permisos que gobiernan un documento. Un resultado vacío significa que se aplica el valor predeterminado de la base de conocimiento. |
| `List` | `/olivares.sdk.v1.ContentSourceService/List` | server-streaming | `ContentListRequest` | `ContentDocRef` (stream) | Transmite referencias de documentos página a página, acotadas por los máximos que pasa el host para impedir que un corpus completo se cargue en memoria en una sola llamada. |
| `Open` | `/olivares.sdk.v1.ContentSourceService/Open` | unary | `OpenRequest` | `Empty` | Inicia una sesión con la configuración proporcionada por el host, antes de cualquier llamada de contenido. |

### `olivares.sdk.v1.HostService`

Definido en `olivaresv1/v1.proto`; 3 rpc.

| Método | Método completo | Tipo | Petición | Respuesta | Qué hace |
|---|---|---|---|---|---|
| `Log` | `/olivares.sdk.v1.HostService/Log` | unary | `LogRecord` | `Empty` | Escribe un registro de log estructurado mediante el motor, de forma que un módulo out-of-process escribe donde lo hace uno in-process. |
| `Publish` | `/olivares.sdk.v1.HostService/Publish` | unary | `Event` | `Empty` | Publica un evento en el bus del motor en nombre de un módulo out-of-process. |
| `Subscribe` | `/olivares.sdk.v1.HostService/Subscribe` | server-streaming | `SubscribeRequest` | `Event` (stream) | Transmite eventos del bus al módulo, filtrados por los tipos de evento que solicita. Un filtro vacío incluye todos los tipos. |

### `olivares.sdk.v1.IngestService`

Definido en `olivaresv1/v1.proto`; 1 rpc.

| Método | Método completo | Tipo | Petición | Respuesta | Qué hace |
|---|---|---|---|---|---|
| `Push` | `/olivares.sdk.v1.IngestService/Push` | client-streaming | `IngestEnvelope` (stream) | `IngestSummary` | Acepta un stream de observaciones enviadas por un daemon collector, eleva cada una al bus de eventos y devuelve un resumen cuando termina el stream. |

### `olivares.sdk.v1.ModuleService`

Definido en `olivaresv1/v1.proto`; 4 rpc.

| Método | Método completo | Tipo | Petición | Respuesta | Qué hace |
|---|---|---|---|---|---|
| `Describe` | `/olivares.sdk.v1.ModuleService/Describe` | unary | `Empty` | `DescribeResponse` | Devuelve el descriptor del módulo: su identidad y la configuración que acepta. |
| `Init` | `/olivares.sdk.v1.ModuleService/Init` | unary | `InitRequest` | `Empty` | Entrega al módulo su configuración y le permite prepararse antes de arrancar nada. |
| `Start` | `/olivares.sdk.v1.ModuleService/Start` | unary | `Empty` | `Empty` | Inicia el trabajo del módulo después de un Init correcto. |
| `Stop` | `/olivares.sdk.v1.ModuleService/Stop` | unary | `Empty` | `Empty` | Detiene el módulo y le permite liberar lo que retiene. |

### `olivares.sdk.v1.OutputService`

Definido en `olivaresv1/v1.proto`; 4 rpc.

| Método | Método completo | Tipo | Petición | Respuesta | Qué hace |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.OutputService/Close` | unary | `Empty` | `Empty` | Finaliza la sesión abierta por Open y libera lo que el connector retuviera para ella. |
| `Describe` | `/olivares.sdk.v1.OutputService/Describe` | unary | `Empty` | `DescribeResponse` | Devuelve el descriptor del connector: su identidad, sus campos de configuración y las capacidades que anuncia. |
| `Notify` | `/olivares.sdk.v1.OutputService/Notify` | unary | `NotifyRequest` | `NotifyResponse` | Entrega una notificación al destino e informa de lo que este hizo con ella, lo que determina si el host reintenta. |
| `Open` | `/olivares.sdk.v1.OutputService/Open` | unary | `OpenRequest` | `Empty` | Inicia una sesión con la configuración proporcionada por el host, antes de cualquier entrega. |

### `olivares.sdk.v1.SourceService`

Definido en `olivaresv1/v1.proto`; 4 rpc.

| Método | Método completo | Tipo | Petición | Respuesta | Qué hace |
|---|---|---|---|---|---|
| `Close` | `/olivares.sdk.v1.SourceService/Close` | unary | `Empty` | `Empty` | Finaliza la sesión abierta por Open y libera lo que el connector retuviera para ella. |
| `Describe` | `/olivares.sdk.v1.SourceService/Describe` | unary | `Empty` | `DescribeResponse` | Devuelve el descriptor del connector: su identidad, sus campos de configuración y las capacidades que anuncia. |
| `Gather` | `/olivares.sdk.v1.SourceService/Gather` | server-streaming | `Empty` | `Observation` (stream) | Transmite observaciones al host, que eleva cada una al bus de eventos. El stream termina cuando finaliza una ejecución por lotes o cuando el host la cancela. |
| `Open` | `/olivares.sdk.v1.SourceService/Open` | unary | `OpenRequest` | `Empty` | Inicia una sesión con la configuración proporcionada por el host, antes de recopilar ninguna observación. |

<!-- END GENERATED olivares-grpc-reference -->

## Formas de los mensajes

Las tablas nombran cada mensaje de petición y respuesta; sus campos se declaran en los archivos
`.proto` indicados para cada servicio, que se distribuyen en el repositorio y son la fuente de
la que se generan los stubs. Conviene conocer dos convenciones antes de leerlos:

- **Los campos de vocabulario son strings, no enums cerrados**: modo de acceso, fuente de
  señal, confianza, severidad y tipo de evento. Un connector de terceros puede introducir su
  propia fuente de señal sin esperar a una release del SDK.
- **Las formas de payload son cerradas.** El payload de un `Observation` o un `Event` es un
  `oneof` de los tipos de mensaje conocidos más un fallback JSON para payloads de eventos
  definidos por módulos. Un payload no reconocido es un error de contrato; no se descarta en
  silencio.

## Generar un cliente

Los archivos `.proto` son el contrato. Apunta el toolchain protobuf de tu lenguaje a
`sdk/plugin/proto/olivaresv1/v1.proto` para el contrato de plugins, o a
`core/api/proto/apiv1/api.proto` para el reflejo del control plane. Los clientes listos para Go
y TypeScript se describen en [Usar los SDK de cliente](/es/how-to/use-the-client-sdks/).
