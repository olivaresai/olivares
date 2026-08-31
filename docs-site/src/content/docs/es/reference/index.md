---
title: "Referencia"
description: "La referencia orientada a la información: la API REST, el bus de eventos, el catálogo de módulos, la CLI y la configuración — precisa y exhaustiva, nada inferido."
---

La referencia está **orientada a la información**. Su trabajo es ser precisa y completa, no
enseñar ni persuadir: enuncia qué son las interfaces, cuáles son sus entradas y
salidas, y cuáles son los defaults — y se detiene ahí. La prosa es seca a
propósito. Si quieres aprender el sistema haciendo, empieza con el
[tutorial](/es/tutorials/zero-to-graph/); si quieres realizar una tarea específica,
usa una [guía how-to](/es/how-to/connect-a-source/); si quieres entender el *porqué*
de que el sistema esté construido como está, lee la
[explicación](/es/explanation/architecture/overview/). Esta sección es para cuando
construyes contra el producto y necesitas el contrato exacto.

La mayor parte de lo que sigue se genera o se deriva a mano **directamente de los
propios artefactos de código fuente del producto**, así que la referencia no puede derivar en silencio de lo que el motor
sirve realmente. Donde una capacidad esté en fase de diseño o sea post-v1, la página
relevante lo dice claramente; ver [Honestidad y límites](/es/start/honesty-and-limits/) para el
contrato general.

## Las áreas de referencia

| Área | Qué documenta | Fuente de verdad |
|---|---|---|
| **[API REST](/reference/api/)** | La API HTTP del control plane: auth, setup, tenancy, agentes, el access map R/RW, tokens y el audit ledger. | El contrato **OpenAPI 3.1** del producto (53 paths core), renderizado en tiempo de build desde el fichero real — no una copia. |
| **[Rutas de módulos (beta)](/reference/api-beta/)** | Las rutas de módulos del producto (`/v1/m/<ns>/…`) — FinOps, compliance, gobernanza, sesiones, modelos, knowledge, … — como documento OpenAPI **beta** separado. | El mismo contrato OpenAPI 3.1, reflejado en tiempo de build a partir de las rutas que registran los módulos. |
| **[Política de estabilidad](/es/reference/api-stability/)** | Versionado, niveles de estabilidad, señalización de deprecación/sunset y las ventanas de soporte mínimas para la API, el proveedor y los SDKs cliente. | La tabla de deprecación en código y sus tests de ventana que hacen fallar el build. |
| **[gRPC](/es/reference/grpc/)** | El espejo gRPC del motor y el contrato de wire versionado para plugins que usa todo conector y módulo fuera de proceso. | Las tablas de registro `grpc.ServiceDesc` que los servidores entregan a gRPC. |
| **[Bus de eventos](/es/reference/events/)** | El bus de eventos interno: el envelope del evento, los tipos de evento de primera parte, y los payloads de observación que los conectores elevan a él. | Un contrato **AsyncAPI 3.0**, derivado a mano del SDK de Go. |
| **[Pantallas de la consola](/es/reference/console/)** | Cada ruta que publica la consola, con el permiso RBAC que exige y la página de referencia que abre su enlace de ayuda dentro del producto. | El censo de rutas de la consola, fijado contra el router compilado. |
| **[Catálogo de módulos](/es/reference/modules/overview/)** | Los 30 módulos del producto — qué es cada uno, su estado, y qué rutas (si las hay) expone fuera de la API core. | El catálogo de capacidades del producto y las interfaces de módulo tipadas. |
| **[CLI](/es/reference/cli/)** | El binario `olivares` y sus subcomandos — `serve`, `collector`, `audit`, `license`, `openapi`, `version` — y sus flags. | Las definiciones de comando compiladas. |
| **[Configuración](/es/reference/configuration/)** | Variables de entorno y opciones de runtime: el directorio de datos, el cableado de fuentes, el motor de autorización y la firma del ledger. | Los cargadores de configuración del motor. |

## API REST

La [referencia de la API REST](/reference/api/) se renderiza en tiempo de build desde el
contrato **OpenAPI 3.1** del producto — el mismo documento que el motor sirve en su
propio endpoint `/openapi.json`. Nada se transcribe a mano, así que la referencia
renderizada es el contrato. Cubre el flujo de primer arranque sin credenciales
(`POST /v1/setup` con el token de setup de un solo uso, luego `POST /v1/auth/login`),
identidad y tenancy, agentes, el access map de lectura/escritura
(`GET /v1/access-edges`; su *drift* de least-privilege reconciliado lo sirve el
módulo de access-map en lugar de la superficie core), gestión de tokens y el audit
ledger.

El contrato describe **53 paths core**. Eso es deliberado: es la superficie estable,
versionada, del control plane, no toda ruta que el motor pueda responder.
A qué se compromete "estable" — versionado, señalización de deprecación y ventanas de
soporte mínimas — es la [política de estabilidad de la API](/es/reference/api-stability/).

:::note[Las rutas de módulos son un contrato beta separado]
Las rutas de módulos — por ejemplo las `/v1/m/accessmap/graph`,
`/v1/m/accessmap/neighbors` y `/v1/m/accessmap/drift` del módulo de access-map —
**no** forman parte del documento estable de 53 paths del núcleo. Se publican como un
documento OpenAPI **beta** separado en [`/reference/api-beta/`](/reference/api-beta/)
(servido en `/openapi.beta.json` y reflejado a partir de las rutas que registran realmente
los módulos), de modo que la superficie estable siga siendo identificable mientras la
superficie completa del producto continúa siendo programable. Beta significa que las formas
pueden cambiar con aviso (una ventana de soporte más corta que stable); el detalle a nivel de
campo sigue viviendo en las interfaces tipadas de Go y TypeScript. El resultado de
least-privilege del access map es la ruta `drift`; no hay un endpoint `diff` separado.
:::

### Espejo gRPC (`olivares.api.v1`)

El control plane también expone una superficie **gRPC** — el servicio `ControlPlane` en
el paquete proto versionado `olivares.api.v1`. Es un **espejo enfocado y congelado**
de un subconjunto del contrato REST de arriba (info del servidor, list/get/create de agentes, audit
verify), usado donde se prefiere un contrato binario tipado (por ejemplo collectors).
Refleja el contrato REST en lugar de extenderlo; el documento OpenAPI sigue siendo
la superficie canónica para la API completa.

## Bus de eventos

La [referencia del bus de eventos](/es/reference/events/) es un contrato **AsyncAPI 3.0**. El
bus es **en proceso por defecto** — los conectores elevan observaciones normalizadas a
él como eventos tipados, y los módulos y conectores de salida se suscriben **por tipo de evento**
y reaccionan, sin que ninguno de ellos se llame directamente entre sí. Un binding distribuido
sobre NATS es opcional, no requerido.

El contrato está **derivado a mano del SDK de Go**, no generado: las definiciones
autoritativas son el envelope del evento, los tipos de evento de primera parte, y los
payloads de observación (las observaciones de acceso agente→recurso, las muestras de coste, y los
reportes de hallazgos). Donde el bus aún no formaliza algo, la referencia
lo dice en lugar de inventarlo.

## Catálogo de módulos

El [catálogo de módulos](/es/reference/modules/overview/) enumera los **30 módulos**
que se asientan sobre el motor core, a través de nueve áreas de capacidad. Uno de los más
útiles es el **access map R/RW** con su diff **Permitido-frente-a-Observado**: lee
de logs, OTEL y (como respaldo no cooperativo) eBPF en lugar de situarse
en el data path, y almacena solo la relación *qué agente puede leer o escribir
qué recurso* — nunca payloads, secretos o PII.

El catálogo es honesto sobre estado y cobertura. Cada módulo lleva su propia
madurez — la mayoría en vivo y cableados de extremo a extremo, algunos parciales u opt-in. La observación
pasiva está **escalonada** por tipo de almacén — limpia para almacenes SQL, de objetos y warehouse;
con pérdidas para almacenes de documentos y vectoriales; imposible sin cooperación para
almacenes en memoria o embebidos — y el catálogo marca dónde un módulo está
en fase de diseño. El registro de modelos propios y el fine-tuning es una **capacidad planificada**, no
uno de los 30 módulos entregados.

## CLI

La [referencia de la CLI](/es/reference/cli/) documenta el único binario `olivares`
y sus subcomandos. El que ejecutas para operar el control plane es `serve`,
que arranca los listeners HTTP (REST + UI web embebida) y gRPC; **TLS está activado por
defecto**. Otros subcomandos cubren el collector, el audit ledger (`verify`,
`checkpoint`, `export`), las herramientas de licencia, y la emisión del documento OpenAPI.

:::caution[Construye primero, luego ejecuta]
No hay atajo `task run` ni `docker run` a secas. O bien construyes e invocas
el binario directamente — `task setup`, `task build`, luego `./bin/olivares serve`
— o lo levantas con el fichero Compose provisto y lees el token de setup de un solo uso
de los logs. La página de la CLI lista los flags `serve` verificados y sus defaults.
:::

## Configuración

La [referencia de configuración](/es/reference/configuration/) lista las variables de entorno
y opciones de runtime que dan forma a un despliegue. Las que cargan el peso son
el directorio de datos (`OLIVARES_DATA_DIR`), el cableado de fuentes real (no-demo) leído
de `OLIVARES_SOURCES_CONFIG` antes de que el motor arranque, y el selector del motor de
autorización `OLIVARES_PDP_ENGINE` (`cedar`, `opa`, o `none`).

Dos reglas de diseño recorren la superficie de configuración. Una **fuente sin configurar
avisa honestamente** en lugar de hacer fallar el motor. Y el seam de autorización **solo
alguna vez restringe, nunca amplía**: el RBAC es deny-by-default, ver el access graph
es una acción privilegiada, y toda lectura de ese tipo se audita.
