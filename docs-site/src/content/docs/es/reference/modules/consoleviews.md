---
title: "Vistas guardadas de la consola"
description: >-
  Instantáneas con nombre y compartibles del estado de una vista de consola — filtros,
  intervalos y ámbitos — almacenadas en el servidor por tenant. Guarda una investigación,
  compártela con el equipo. Qué almacena el módulo, sus reglas de propiedad y uso compartido,
  y sus límites honestos.
---

El módulo `consoleviews` ofrece a la consola **vistas guardadas**: una instantánea con nombre
del estado de una vista — los mismos filtros, intervalos de tiempo y ámbitos que la consola
codifica en la URL — almacenada **en el servidor por tenant**, para que una investigación como
*"admisiones fallidas, últimas 24 h"* sobreviva al navegador, acompañe al operador entre
máquinas y (al compartirse) quede a un clic de todo el equipo.

## Qué almacena — y qué no almacena nunca

Una vista guardada contiene **solo parámetros**: un objeto JSON con límite de tamaño (máx. 4 KB)
que guarda el estado de URL de la vista, además de un nombre, una descripción opcional, el
principal propietario y una marca `shared`. El módulo **nunca almacena resultados de consulta,
filas del ledger ni ningún dato que seleccionarían los parámetros** — cargar una vista guardada
vuelve a ejecutar la consulta subyacente con los propios permisos del llamante. La consola trata
los parámetros almacenados estrictamente como datos.

## Propiedad, uso compartido y quién puede hacer qué

- **Crear/actualizar** — cualquier miembro con `consoleviews:view:write` (nivel editor).
  Una vista pertenece al principal que la creó; solo el propietario puede editarla.
- **Visibilidad** — el propietario siempre ve sus propias vistas; una vista marcada como
  `shared` es visible para cada miembro del tenant con `consoleviews:view:read` (nivel visor).
  Una vista que no puedes ver responde `404`, nunca `403` — la visibilidad no revela su
  existencia.
- **Eliminar** — el propietario, o un rol de **admin/owner** del tenant para cualquier vista
  (el poder de limpieza de las vistas dejadas por usuarios que se marcharon).
- **Límites** — 200 vistas por propietario, 2000 por tenant; se rechazan con un mensaje claro
  al alcanzarlos. `(feature, owner, name)` es una clave natural: guardar un nombre duplicado
  en la misma feature responde `409`.

Cada creación, actualización y eliminación queda registrada en el audit ledger del tenant,
atribuida al principal real — los metadatos registrados identifican la vista (feature, nombre,
marca de uso compartido), nunca sus parámetros.

## Rutas

| Método | Ruta | Permiso |
|---|---|---|
| `GET` | `/v1/m/consoleviews/views?feature_id=` | `consoleviews:view:read` |
| `GET` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:read` |
| `POST` | `/v1/m/consoleviews/views` | `consoleviews:view:write` |
| `PUT` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |
| `DELETE` | `/v1/m/consoleviews/views/{id}` | `consoleviews:view:write` |

Las rutas del módulo forman parte de la superficie **beta** — consulta la
[referencia de rutas de módulo](/reference/api-beta/).

## Límites honestos

- El servidor valida el `feature_id` de una vista como slug, pero **no** fija la lista de
  features de la consola — el registro de la consola es la autoridad y cambia en cada release;
  la consola ignora las vistas guardadas de features que ya no tiene.
- Una vista compartida comparte **parámetros**, no resultados: dos operadores que cargan la
  misma vista guardada pueden ver datos distintos si sus permisos difieren. Es por diseño —
  compartir nunca amplía el acceso.
- Las vistas guardadas son mobiliario de la consola, no evidencia: viven fuera de la cadena del
  ledger (solo sus eventos de ciclo de vida quedan evidenciados).
- Un operador **confinado a un workspace** puede leer vistas guardadas pero no puede crearlas,
  editarlas ni eliminarlas: el motor de grants acotados prohíbe las escrituras a nivel de
  colección para principales confinados (fail-closed), y la anulación de eliminación de admin
  de todo el tenant excluye explícitamente a los admins confinados.
- Los límites por propietario/tenant son blandos con escritores concurrentes en Postgres (exceso
  marginal acotado); los nombres duplicados siempre se rechazan de forma estricta.
