---
title: Estabilidad de la API, versionado, deprecación y retirada
description: >-
  El esquema de versionado, los niveles de estabilidad, la señalización de
  deprecación (cabeceras RFC 9745 / RFC 8594) y las ventanas mínimas de soporte
  para la API REST, el espejo gRPC, el contrato de ingesta en vivo (wire), el
  proveedor de Terraform y los SDK de cliente.
---

Esta página es el **contrato de estabilidad** para todo lo que programa contra el
control plane. Establece qué es estable, cómo se señaliza un cambio incompatible y
cuánto tiempo sigue funcionando una superficie deprecada. La aplicación reside en el
código, no en la prosa: la tabla de deprecaciones, las cabeceras de respuesta, los
marcadores OpenAPI y las comprobaciones de ventana de más abajo se derivan todos de una única
declaración en código (`core/api/stability.go`), y una retirada programada antes
de lo que permite la política **rompe la build**.

:::note[Estado pre-1.0]
Olivares AI es pre-1.0 (consulta [Honestidad y límites](/es/start/honesty-and-limits/)).
Los mecanismos de señalización de esta página ya están en vivo; las **ventanas mínimas
de soporte vinculan a partir de la release 1.0/GA**. Hasta entonces la superficie publicada
se mantiene estable en la práctica, pero las ventanas formales de más abajo son el compromiso
que podrás exigirnos a partir de GA.
:::

## Superficies cubiertas y niveles

| Superficie | Versionada por | Nivel hoy |
|---|---|---|
| Contrato core REST — las rutas del [documento OpenAPI servido](/reference/api/) | URL major (`/v1/…`) | **stable** |
| Espejo gRPC — `ControlPlane` en el paquete proto `olivares.api.v1` | major del paquete proto | **stable** (espejo congelado) |
| Ingesta en vivo / wire de conectores — paquete proto `olivares.sdk.v1` | major del paquete proto + `ProtocolVersion` del plugin | **stable** (congelado) |
| SDK de conectores (Go) — módulos `sdk`, `sdk/plugin` (superficie de autoría) | semver de módulo — tags `sdk/v*`, `sdk/plugin/v*` desde la primera release pública | **stable v1** (contrato Go; fila wire de arriba) |
| [Contrato del bus de eventos](/es/reference/events/) (AsyncAPI 3.0) — sus tipos de evento son también lo que la plataforma de eventos entrega a las [suscripciones de webhook externas](/es/reference/events/#suscripciones-externas-plataforma-de-eventing); las rutas de gestión de suscripciones son rutas de módulo (`/v1/m/eventing/`, fuera de contrato), pero cada **tipo de evento** lleva su propio nivel de estabilidad desde el catálogo en código | `info.version` (`1.0.0-preview`) | **beta** (documento); niveles por tipo para los tipos de evento |
| Proveedor de Terraform | su propio semver (tags `terraform-provider-v*`) | **stable**, el MAJOR sigue a la API v1 |
| SDK de cliente (Go / Java / Python / TypeScript) | su propio semver; el MAJOR sigue al major de la API desde GA | **beta** (paquetes pre-1.0) |
| Cualquier cosa no listada — rutas de módulo `/v1/m/<ns>/`, SCIM, federación, internos | — | **out of contract** |

**Niveles.** Una superficie *stable* no cambia de forma incompatible dentro de su versión
major; eliminarla o cambiarla requiere el proceso de deprecación de más abajo. Una
superficie *beta* todavía puede cambiar de forma, pero recibe la misma señalización y una
ventana más corta. Una superficie *out-of-contract* (en particular las rutas de módulo que
están deliberadamente fuera del documento OpenAPI — consulta la
[visión general de la referencia](/es/reference/)) no lleva promesa de compatibilidad; sus
contratos viven en las interfaces tipadas que se distribuyen con el producto.

Cada operación del documento OpenAPI lleva un marcador `x-stability` legible por
máquina, y el propio documento enlaza esta página en
`info.x-stability-policy`.

## Qué cuenta como cambio incompatible

Para una superficie stable, todo lo siguiente es incompatible y queda sujeto al proceso de más abajo:

- eliminar o renombrar una ruta, método, campo de petición, campo de respuesta o `code`
  de error;
- cambiar el tipo o el significado de un campo, o convertir un campo opcional de petición
  en obligatorio;
- endurecer la autenticación/autorización de modo que una llamada antes válida
  falle;
- para gRPC/protobuf: cualquier cosa que `buf breaking` (ruleset FILE) rechace.

Estos **no** son incompatibles: añadir endpoints, añadir parámetros opcionales de
petición, añadir campos de respuesta, añadir nuevos códigos de error para nuevos modos
de fallo y añadir cabeceras de respuesta. Los clientes deben tolerar campos JSON desconocidos.

## Versionado

- **REST** se versiona en la URL: todo el contrato stable vive bajo
  `/v1/`. Un cambio incompatible se distribuye bajo `/v2/` y `/v1/` entra en
  deprecación — nunca una ruptura en el sitio.
- **gRPC** se versiona por paquete proto: `olivares.api.v1` /
  `olivares.sdk.v1`. Un cambio incompatible requiere un nuevo major de paquete
  (`…v2`); ambos contratos están protegidos por `buf breaking` contra `main`
  (`task proto:breaking`).
- **El proveedor de Terraform** se publica de forma independiente
  (tags `terraform-provider-v*`); su MAJOR sigue al major de la API que habla.
- **Los SDK de cliente** incrustan `API_VERSION` (el major de contrato a partir del que
  se generaron) y `SPEC_HASH` (el snapshot OpenAPI exacto) — `APIVersion` y
  `SpecHash` en Go; a partir de GA su MAJOR sigue al major de la API.
- **El SDK de conectores** (el contrato Go contra el que construyen los conectores
  de terceros) se versiona mediante tags semver por módulo (`sdk/vX.Y.Z`,
  `sdk/plugin/vX.Y.Z`) y queda protegido por el mismo muro `buf breaking` sobre su wire.
  Las interfaces que implementa un autor nunca ganan métodos dentro de un major; la nueva
  capacidad llega como nuevas interfaces opcionales. La política completa se distribuye con
  el módulo (`sdk/VERSIONING.md`); el ciclo de vida de autoría está en
  [Construir y distribuir un conector](/es/how-to/build-a-connector/).

## Proceso de deprecación y señalización

Una deprecación es una entrada declarada en la tabla en código más una guía de
migración; todo lo demás se deriva de ella de forma mecánica.

1. **Anunciar.** La entrada aterriza con su fecha de anuncio y la URL de la guía de
   migración. A partir de ese momento cada respuesta de la ruta deprecada lleva
   la cabecera [RFC 9745](https://www.rfc-editor.org/rfc/rfc9745) y un enlace a
   la guía, y la operación OpenAPI gana `deprecated: true`,
   `x-deprecated-at` y `x-migration-guide`:

   ```http
   Deprecation: @1780272000
   Link: <https://olivares.ai/docs/how-to/migrate-example/>; rel="deprecation"
   ```

2. **Programar la retirada.** Cuando se compromete la fecha de retirada, las respuestas
   añaden la cabecera [RFC 8594](https://www.rfc-editor.org/rfc/rfc8594) (y la
   spec gana `x-sunset-at`):

   ```http
   Sunset: Thu, 01 Jun 2028 00:00:00 GMT
   Link: <https://olivares.ai/docs/how-to/migrate-example/>; rel="sunset"
   ```

3. **Eliminar** — como muy pronto en la fecha de retirada, normalmente con el siguiente
   major de la API.

**Ventanas mínimas de soporte** (anuncio de deprecación → retirada):

| Nivel | Ventana mínima |
|---|---|
| stable | **24 meses** |
| beta | **12 meses** |

Estas ventanas se aplican mediante tests contra la tabla de declaración: una entrada
cuya retirada viola la ventana de su nivel, o que apunta a una ruta que no
existe, no compila.

Para **gRPC**, la deprecación se expresa con la opción `deprecated` de protobuf
(que aflora en el código generado) más las mismas ventanas; por lo demás los contratos
wire están congelados y `buf breaking` rechaza de plano las ediciones incompatibles.

## Qué ven los clientes

- **Proveedor de Terraform** — emite un WARN de `tflog` (método, endpoint, fechas,
  guía) una vez por método y ruta de petición únicos por ejecución cuando una respuesta
  del control plane lleva una señal de deprecación (una ruta parametrizada deprecada
  avisa una vez por cada recurso que toca), y envía un `User-Agent` versionado para que
  el uso de cliente deprecado sea atribuible en el servidor.
- **SDK de Go** — aflora un `DeprecationNotice` una vez por endpoint (por defecto: un
  aviso `slog`; sobrescríbelo con `WithDeprecationHandler`). Las operaciones
  deprecadas llevan marcadores `// Deprecated:` de Go, de modo que los editores y `staticcheck`
  las marcan en tiempo de desarrollo.
- **SDK de Python** — un `DeprecationWarning` por endpoint (o tu callback
  `on_deprecation`); las operaciones deprecadas se marcan en los docstrings.
- **SDK de TypeScript** — un `console.warn` por endpoint (o tu callback
  `onDeprecation`); las operaciones deprecadas llevan JSDoc `@deprecated`.

## Relacionado

- [Referencia de la API REST](/reference/api/) — el propio contrato stable
- [Uso de los SDK de cliente](/es/how-to/use-the-client-sdks/)
- [Construir y distribuir un conector](/es/how-to/build-a-connector/) — el contrato y el ciclo
  de vida del SDK de conectores
- [Gestionar como código (Terraform)](/es/how-to/manage-as-code/)
- [Módulo XIX — API propia + gestión como código](/es/reference/modules/xix-api-manage-as-code/)
- [Bus de eventos (AsyncAPI 3.0)](/es/reference/events/)
- [Honestidad y límites](/es/start/honesty-and-limits/)
