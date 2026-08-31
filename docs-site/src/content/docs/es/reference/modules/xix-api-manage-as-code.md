---
title: "Módulo XIX — la API propia y la superficie manage-as-code"
description: >-
  La superficie fundacional del motor: cada acción del control plane sobre una única
  API REST/gRPC, más un provider de Terraform para que el propio control plane se
  declare y se versione. Cuál es el contrato de la API, qué gestiona el provider, y
  los límites honestos de cada uno.
---

El módulo XIX no es una funcionalidad atornillada al motor — **es** la superficie del motor.
Cualquier otro módulo alcanza el mundo exterior a través de la misma API de primera parte, y
la UI web es una capa de presentación sobre ese mismo contrato exacto, no uno paralelo. Esta
página es la referencia de lo que esa superficie expone hoy y de cómo gestionar el control
plane como código, con sus fronteras reales.

## El contrato de la API

El motor habla una única API REST bajo `/v1` (router chi, `http.Server` endurecido) y un
**mirror gRPC enfocado y congelado** de ella (`olivares.api.v1`: info del servidor, lectura
/creación de agentes, verificación de auditoría, más el servicio de salud estándar). gRPC es
un subconjunto deliberado, no paridad completa — los endpoints nuevos aterrizan primero en
REST. Ambos cables ejecutan la **misma** cadena `authenticate → resolve-tenant → authorize` y
mapean los errores de forma idéntica, de modo que un not-found es indistinguible de un recurso
de otro tenant en cualquiera de los dos cables.

La superficie REST se publica como un **contrato OpenAPI 3.1** renderizado en la
[referencia de la API](/reference/api/) directamente desde el esquema escrito del producto.
Ese documento es el contrato de registro de la superficie estable del núcleo; las rutas de
módulo se publican por separado en un documento **beta** — la
[referencia de rutas de módulos](/reference/api-beta/) (consulta los límites honestos más
abajo). La misma funcionalidad también puede manejarse desde la terminal — consulta la
[referencia de la CLI](/es/reference/cli/) — porque la CLI es el motor, no un envoltorio
sobre él.

## Autenticación y el cable

La autenticación son **tokens bearer opacos del lado del servidor**, no JWT.
Un token lleva un prefijo de
propósito (sesión vs. API key); el servidor persiste solo un selector público y un SHA-256 del
secreto, y compara el secreto en tiempo constante. Las consecuencias que importan para un flujo
manage-as-code: los tokens son **revocables al instante**, no llevan **claims ni secretos**, y
no añaden superficie de ataque de crypto-parsing. Un token de API está enlazado a un
`(tenant, role)` o es una credencial de nivel de sistema sin enlazar; una petición cuyo header
de tenant discrepa con un token enlazado se rechaza, nunca se ensancha silenciosamente.

## Manage-as-code: el provider de Terraform

El provider `terraform-provider-olivares` es un **módulo Go separado** y un cliente REST puro
— nunca importa el núcleo del motor ni el SDK de conectores, manteniendo el gran árbol de
dependencias del provider fuera de la cadena de suministro del núcleo. Configurado con un
endpoint, un token de API sensible y un tenant opcional, gestiona un conjunto de objetos
deliberadamente pequeño y declarado:

| Kind | Nombre | Gestiona |
|---|---|---|
| resource | `olivares_agent` | la definición de catálogo de un agente (CRUD completo + import) |
| resource | `olivares_policy` | una declaración de política de gobernanza |
| resource | `olivares_agent_identity_binding` | el binding de un agente a una identidad no humana |
| resource | `olivares_deployment` | una **definición** de despliegue (estado deseado, declarativo) |
| data source | `olivares_policies` / `olivares_identities` | vistas de solo lectura del roster gobernado |
| data source | `olivares_access_edges` | el mapa de acceso R/RW y su drift permitted-vs-observed |
| data source | `olivares_deployment` / `olivares_server_info` | una definición de despliegue; metadatos del motor |

Estos son los **únicos** recursos y data sources que sirve el provider. Declarar un
`olivares_deployment` registra el estado deseado en el control plane — **no** toca la
infraestructura; la ruta de apply pertenece al [módulo VII](/es/reference/modules/vii-deploy/)
y es una costura deny-closed.

:::caution[Límites honestos]
- **`olivares_deployment` declara; no despliega.** El recurso escribe una *definición* de
  despliegue a través de las rutas del módulo VII. El `apply`/`retire` vivo contra
  infraestructura real es una **costura deny-closed que devuelve `503`** hasta que un operador
  aprovisiona un executor — declarar un despliegue en HCL nunca muta tu estate.
- **El OpenAPI estable no es todo el cable.** La superficie estable del núcleo está en el
  contrato publicado (`/openapi.json`); las rutas de módulo (por ejemplo las lecturas del
  mapa de acceso y del drift, y las rutas de gobernanza y despliegue que usa el provider)
  se publican en un documento **beta** separado (`/openapi.beta.json`, la
  [referencia de rutas de módulos](/reference/api-beta/)). Sus formas a nivel de campo
  viven en las interfaces tipadas del producto, no en el esquema estable.
- **gRPC es un subconjunto congelado, no la API completa.** Refleja unas pocas operaciones de
  lectura/creación y auditoría para automatización de primera parte; no asumas que un endpoint
  existe en gRPC porque exista en REST.
- **La superficie del provider es pequeña a propósito.** Cuatro recursos y cinco data sources —
  no toda la API como IaC. Cualquier cosa fuera de ese conjunto se gestiona por REST/CLI, no se
  declara en HCL hoy.
- **La licencia es atestación, nunca una puerta de funcionalidad.** El producto es íntegro bajo
  su licencia; la comprobación de licencia offline solo registra el titular y el estado y nunca
  deshabilita, degrada ni bloquea ninguna petición de API ni el arranque.
:::

## Seguro por defecto

El motor de servicio es seguro por defecto: TLS está activo (se genera un cert autofirmado en
el primer arranque si no se suministra ninguno), el bind toma por defecto localhost, y escuchar
localmente **no** es una exención de la autorización. Una instalación nueva no tiene
credenciales — acuña un token de setup de un solo uso a stdout y rechaza todo endpoint
protegido hasta que se crea el primer administrador. La auditoría es append-only y
hash-chained, con checkpoints firmados con Ed25519 que hacen criptográficamente detectable
reescribir la historia antes de un checkpoint.

## La plataforma de eventing (la mitad saliente del módulo XIX)

Desde que se publicó la plataforma de eventing (`modules/eventing`), la superficie del módulo
XIX también incluye **suscripciones a eventos self-service por tenant**: suscripciones tipadas
sobre el catálogo de eventos del bus (`edge.observed`, `cost.sampled`, `finding.reported`,
`audit.recorded`, …) con **entrega durable at-least-once** — reintentos con backoff, una cola
dead-letter, y replay desde un cursor — a un webhook firmado con HMAC o a un
[sink SIEM](/es/how-to/cookbook/push-to-siem/). El módulo notify
([XV](/es/reference/modules/xv-notify/)) sigue siendo el *router* de alertas a destinos
aprovisionados por el operador; eventing es la plataforma de cara al integrador. Un
**export de postura** de solo lectura complementario (`modules/posture-export`) permite a una
torre de control sondear la postura ground-truth del producto — grafo de acceso, drift,
inventario, findings — solo como refs/hashes/relaciones, con el propio export auditado.

## Relacionado

- [Referencia de la API](/reference/api/) — el contrato OpenAPI 3.1 renderizado para la superficie del núcleo.
- [Política de estabilidad de la API](/es/reference/api-stability/) — versionado, señalización de deprecación/sunset y ventanas de soporte para esta superficie.
- [Usar los SDKs cliente](/es/how-to/use-the-client-sdks/) — los clientes de primera parte Go/Python/TypeScript.
- [Referencia de la CLI](/es/reference/cli/) — la misma funcionalidad desde el binario `olivares`.
- [Gestionar el control plane como código](/es/how-to/manage-as-code/) — la guía del provider de Terraform.
- [Módulo VII — despliegue](/es/reference/modules/vii-deploy/) — dónde actúa `olivares_deployment` (la costura `503`).
- [Catálogo de módulos](/es/reference/modules/overview/) — la división Gobernar/Observar vs Actuar.
- [Honestidad y límites](/es/start/honesty-and-limits/) — qué actúa hoy y qué no.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — la capa del motor sobre la que se asienta esta superficie.
