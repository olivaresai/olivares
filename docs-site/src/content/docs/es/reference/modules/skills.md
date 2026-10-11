---
title: "Skills — el catálogo inmutable y las asignaciones fijadas"
description: >-
  Uno de los 32 módulos: el catálogo inmutable de skills y sus asignaciones
  fijadas. Los packs de skills se instalan como revisiones inmutables validadas
  por digest; lo que un destino puede usar es un pin registrado y auditado —
  nunca una edición en caliente de una carpeta.
---

Skills (`modules/skills`, `olivares.skills`) es uno de los 32 módulos. Es dueño del
**catálogo inmutable de skills** y de sus **asignaciones fijadas**: un pack de skills
se instala una vez, se valida entrada por entrada contra el digest de su manifiesto y
se publica como una revisión inmutable — nunca se edita en sitio después. Lo que un
destino (un agente, un grupo, un workspace) puede usar es una **asignación** que fija
una revisión del pack, y cada pin se escribe en el ledger de auditoría
(`skills.assignment.pin`).

## Qué entrega

- **El catálogo.** Packs instalados bajo `/v1/m/skills/packs`, cada revisión
  inmutable registra el digest de su manifiesto; retirar un pack que
  sigue asignado o usado por conversaciones registradas se rechaza, no se borra en
  cascada y en silencio.
- **El catálogo integrado.** Un conjunto vendored de packs de skills upstream fijados
  por commit y digest de archivo (`builtin/PIN.json`), validado al instalar igual que
  un archivo subido. Nada de él se ejecuta al instalar.
- **Las asignaciones.** `GET/POST /v1/m/skills/assignments` y
  `PUT/DELETE /v1/m/skills/assignments/{id}` fijan una revisión a un destino, con el
  ámbito del workspace activo cuando lo hay. Dos skills asignadas con el mismo nombre
  y contenido distinto se rechazan antes de iniciar una sesión, de modo que cada
  lanzamiento es determinista.
- **Importación desde Git.** Un importador con ámbito de operador puede traer un pack
  de skills desde una fuente Git bajo una política fijada; la importación aterriza en
  el catálogo como otra revisión inmutable, no como un checkout vivo que el motor lee.

## Superficies y permisos

Las rutas del módulo viven en su namespace beta (`/v1/m/skills/…`), documentadas en la
[referencia de rutas de módulos](/reference/api-beta/) y renderizadas en la vista
Skills de la consola. Las lecturas requieren el permiso de lectura; instalar un pack o
una revisión requiere escritura; retirar requiere admin; las asignaciones requieren el
permiso de asignación. Una sesión resuelve sus skills solo mediante asignaciones
registradas — una carpeta que cambie en disco nunca cambia lo que ejecutará un
lanzamiento ya fijado.

:::caution[Límites honestos]
- **Nuevo desde la versión 26.10.1<!-- release-fixed -->.** El binario oficial 26.10.1<!-- release-fixed --> no incluye este
  módulo; sale con la próxima versión, y esta página describe el módulo tal como
  existe en la línea de integración actual.
- **El catálogo gobierna; no ejecuta.** Instalar, fijar e importar nunca ejecutan
  código de skills — la ejecución queda en las herramientas y sesiones que consumen
  una revisión fijada.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — los 32 módulos y dónde se
  sitúa este entre ellos.
- [MCP, skills y capacidades](/es/reference/modules/v-capabilities/) — la vista de
  gobierno sobre herramientas y capacidades que este catálogo alimenta.
- [Catálogo y marketplace internos](/es/reference/modules/xiv-catalog/) — el
  marketplace curado de agentes, servidores MCP y skills aprobados.
- [Rutas de módulos (beta)](/reference/api-beta/) — las operaciones
  `/v1/m/skills/`.
- [Honestidad y límites](/es/start/honesty-and-limits/) — por qué la inmutabilidad se
  declara, no se sobrentiende.
