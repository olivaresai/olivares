---
title: Operar una sesión de proveedor
description: >-
  Registra un perfil de proveedor para un home existente de Claude, Codex o Grok
  en este nodo, fija el binario oficial del controlador, lanza una sesión
  gobernada desde la consola o la CLI, e interrumpe o detén el turno sin inventar
  un ejecutor.
---

Esta página es la vía de **operación** de las CLI oficiales de proveedor. El
plano de control lanza un proceso hijo propio bajo un **perfil de proveedor**.
No instala el proveedor, no crea su home ni inicia un inicio de sesión
interactivo en el navegador.

No es la vía del conector ni del hook. Para inventariar o gobernar los ficheros
de configuración de Grok Build o de Codex, usa
[Integrar Grok Build](/how-to/integrations/grok/) o
[Integrar Codex](/how-to/integrations/codex/). Para co-desplegar Claude Code en
el mismo host, usa
[Ejecutar Claude Code con Olivares](/how-to/run-claude-code-with-olivares/).

Fuente de este comportamiento: sección `[26.9.0]` de `CHANGELOG.md`
(perfiles de proveedor, controlador Codex, controlador Grok), las referencias
generadas de [consola](/reference/console/) y
[configuración](/reference/configuration/), `cmd/olivares/sessionruntime.go` y
`web/src/features/agentops/types.ts`.

## Precondiciones

Complétalas antes de un lanzamiento. Un elemento que falta es una denegación,
no un respaldo.

1. Olivares AI está instalado y existe el primer administrador.
   Consulta [Tu primera hora](/es/how-to/first-hour/) para el token de configuración.
   La verificación adicional administrativa (`admin_step_up`) está en `none`
   por defecto. Si un administrador activa `totp` o `passkey`, cumple esa política
   antes de las operaciones privilegiadas (`core/api/middleware.go` `requireStepUp`).
2. La CLI oficial del proveedor ya está instalada en **este nodo**. El perfil
   registra homes que ya existen. El servidor resuelve las rutas (absolutas,
   enlaces simbólicos resueltos, directorio existente) y no crea, instala ni
   inicia sesión (`web/src/features/agentops/types.ts` `CreateProfileRequest`).
3. Tienes `sessions:profile:read` para abrir **Provider profiles**
   (`/provider-profiles`) y `sessions:profile:write` para registrar.
   Vincular un origen necesita `sessions:profile-binding:write` más
   administración de orígenes. Lanzar una ejecución necesita `sessions:run:write`.
   Permisos: [referencia de consola](/reference/console/).
4. El controlador necesita un ejecutable en **este nodo**. Un binario fijado
   explícitamente tiene prioridad; si no se fija, el motor lo resuelve al
   iniciar. Siguen aplicándose las comprobaciones de preparación y política
   de cada controlador.

| Controlador | Fija esta variable de entorno | Si no está definida |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` | Instalación gestionada verificada más reciente; después, `claude` en el `PATH` del motor. |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Instalación gestionada verificada más reciente; después, `codex` en el `PATH` del motor. |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Instalación gestionada verificada más reciente; después, `grok` en el `PATH` del motor. |

El valor fija el ejecutable oficial que este nodo puede operar. Sin un valor
fijado, instalar una herramienta la hace disponible sin reiniciar el motor.
Si no hay instalación gestionada ni ejecutable en `PATH`, se rechaza el inicio.
`OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` sigue el mismo orden de resolución.

Para los perfiles de Claude con `managed_injection` que no nombran un proveedor,
`OLIVARES_SESSION_RUNTIME_WIF` o `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` aporta la
credencial de inferencia del host. Un perfil vinculado a un proveedor usa su
credencial; si falla, se rechaza el inicio sin recurrir a la del host.
Un perfil con `provider_account_home` usa el inicio de sesión autorizado de la
herramienta y no necesita ninguna de las dos variables. Consulta
[Añadir un proveedor](/es/how-to/add-a-provider/).
Codex y Grok usan solo el `auth_source` AUTORIZADO del perfil:
`provider_account_home` o `managed_injection`, sin respaldo entre ellos y sin
valor predeterminado (`CHANGELOG.md` `[26.9.0]`; `ProviderProfileDTO.auth_source`).

:::caution[Lo que esta página no afirma]
`CHANGELOG.md` `[26.9.0]` indica que el comportamiento del controlador Grok se
prueba contra un hijo ACP falso propio a través del HTTP, el runtime, el
almacén y el grupo de procesos reales. **La compatibilidad con una cuenta
oficial de Grok autenticada es trabajo aparte y no se afirma aquí.**
:::

## 1. Registrar un perfil de proveedor

Un perfil de proveedor es la identidad duradera de **una** instancia de
proveedor configurada en **un** entorno de ejecución: controlador, entorno
propietario y los `config_home` / `user_home` canónicos bajo los que corre el
hijo. Es identidad de configuración y de almacenamiento, no una cuenta de
proveedor autenticada (`CHANGELOG.md` `[26.9.0]` B1; texto de consola
`agentops.profiles.subtitle`).

### Consola

1. Abre **Provider profiles** (`/provider-profiles`).
2. Elige **Register profile**.
3. Indica el controlador (`claude`, `codex` o `grok`), el `config_home`
   existente y el `user_home` existente. `environment_ref` puede omitirse
   (este nodo).
4. Guarda. La lista muestra `profile_ref`, controlador, estado y si el perfil
   está habilitado en este entorno. Las rutas **no** están en la lista ordinaria.
5. Para ver los homes almacenados, usa **Reveal configuration**
   (`sessions:profile:admin`). Esa lectura es bajo demanda y se descarta al
   ocultarla. Sigue sin llevar ningún valor de credencial.

Renombrar, deshabilitar y habilitar conservan el mismo id y los mismos homes.
**Retire** es irreversible, se confirma escribiendo y libera el home para un
id **nuevo**.

### Lo que el motor deniega

- Un perfil cuyo controlador no está registrado en este nodo sigue visible y
  no se puede lanzar (`operable` no es una garantía de lanzamiento;
  `GET …/launch-readiness` es el panel de requisitos).
- Un perfil de otro entorno de ejecución se muestra como extranjero y nunca
  se lanza desde este nodo.
- Un lanzamiento que reenvía `HOME`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME` o
  `GROK_HOME` se deniega. Esos nombres pertenecen al perfil
  (`agentops.create.profileEnvConflict`).

No hay captura de esta pantalla en el conjunto publicado. No trates una imagen
de la pestaña Connectors como este formulario.

## 2. Vincular un origen (opcional, para atribución observada)

Un origen puede dedicarse a un perfil en la revisión exacta del roster que
aplicó este nodo. La clave es el id persistente de la fila, nunca su nombre
editable (`CHANGELOG.md` `[26.9.0]` B1; **Source bindings** `/provider-bindings`).

1. Abre **Source bindings** (`/provider-bindings`).
2. Vincula el `id` persistente del origen y el `applied_revision` que cableó
   el reconciliador de este nodo. `GET /v1/console/sources` informa ambos.
3. Revoca el vínculo para detener la atribución **nueva** al perfil. Un
   sobre anterior conserva su vínculo histórico durante la reproducción.

Sin vínculo, un registro conocido sigue apareciendo como fila de observación
`source`. No se fusiona en una ejecución gestionada. Consulta
[Operación en vivo y sesiones](/reference/modules/ii-sessions/).

## 3. Lanzar

El extremo productivo de creación exige `provider_profile_ref`. Omitirlo
conserva el cuerpo de petición anterior, que esta API deniega
(`CHANGELOG.md` `[26.9.0]` B2; flag CLI `--provider-profile`).

El diálogo de lanzamiento ofrece los perfiles **activos**. Si solo hay un
perfil activo, viene preseleccionado; si hay varios, ninguno, y **Start** pide
elegir uno. Las selecciones de workspace y plantilla se pueden borrar; el
perfil no (`CHANGELOG.md` `[26.9.0]` Fixed).

### Consola

1. Abre **Sesiones** (`/sessions`). `/agentops` abre la misma pantalla
   ([referencia de consola](/reference/console/)).
2. Abre **New session** y luego **Advanced launch options** (dentro de
   **More options** cuando hay una herramienta lista).
3. Comprueba **Provider profile** (`agentops.create.profile`) y escribe el
   **First message** si quieres uno.
4. Opcionalmente indica workspace, plantilla, modelo y effort en
   **Advanced options**. Modelo y effort siguen siendo cadenas abiertas del
   proveedor en los flags oficiales del agente para Grok
   (`CHANGELOG.md` `[26.9.0]`).
5. Pulsa **Start**. Mientras no pueda iniciar, la línea bajo el botón dice qué
   falta. Solo se publica la **referencia** del perfil. El servidor resuelve
   los homes.

### Un worktree Git propio (opcional)

Por defecto la sesión trabaja en la carpeta elegida. Si esta es la raíz de un
repositorio Git, marca **Trabajar en un nuevo worktree Git** bajo **Carpeta**, o
ejecuta `olivares session start . --worktree`. La sesión trabaja en una rama nueva
(`olivares/` más ocho caracteres de su id) dentro de su propio worktree: dos
sesiones no comparten archivos. Fusiona la rama desde tu checkout habitual.

- Los worktrees viven bajo `<directorio de datos>/session-worktrees`.
  `OLIVARES_SESSION_WORKTREE_DIR` cambia la ubicación y
  `OLIVARES_SESSION_WORKTREE_BRANCH_PREFIX` el prefijo de rama.
- **Reanudar** continúa en el mismo worktree. Si solo se borró su directorio,
  el motor lo restaura en la misma rama.
- **Limpiar**, **Borrar** y `olivares session rm` eliminan worktree y rama cuando
  la rama está fusionada en la rama actual del workspace, el worktree está en
  esa rama y no tiene cambios sin commit. En otro caso se rechaza con 409 y se
  indica qué se perdería: trabajo sin fusionar, HEAD separado o en otra rama,
  o un worktree inaccesible. Marca **Descartar también el worktree y la rama**
  o añade `--discard-worktree` para continuar; si el worktree es inaccesible se
  libera la sesión y se deja el worktree donde está. Los archivos ignorados por
  Git, como las builds, se eliminan con el worktree.
- Se rechaza con 422 antes de crear nada para una carpeta que no sea la raíz de
  un repositorio Git, un repositorio sin commit, un workspace o carpetas de solo
  lectura, aislamiento no nativo o configuración Git con filtros
  (`filter.<name>.clean`, `smudge` o `process`) o inclusión de otros archivos.
  Las sesiones sin esta opción no cambian.
- El motor ejecuta Git sin hooks ni monitor de archivos, sin tu configuración
  Git (los archivos Git LFS aparecen como punteros) y con límites de tiempo y
  salida. El directorio Git compartido sigue siendo común: un worktree aísla
  archivos, no metadatos Git.

### Abrir el trabajo nombrado en un traspaso

El contenido de un traspaso admite `branch` y `sha` opcionales, este último un id
de commit completo. Ofrécelo por API o mediante
`olivares message handoff offer --context-file`, cuyo JSON puede incluir ambos.
Un traspaso que no nombre ninguno no cambia.

Al leerlo, el panel muestra la rama y el commit como texto. **Abrir en un nuevo
worktree de sesión** abre el lanzamiento con el worktree seleccionado y su punto
de partida visible. El inicio espera a que elijas el workspace cuyo repositorio
contiene el commit. Limpiar la selección de carpeta mantiene esa petición; desmarca
explícitamente el worktree para iniciar una sesión normal. Desde la CLI:

```sh
olivares session start . --worktree-from <commit or branch> --name review
```

`--worktree-from` implica `--worktree`. La sesión usa su propia rama nueva en ese
commit; la rama del emisor y tu checkout no se mueven. Si el commit no está en el
repositorio del workspace, se rechaza con 422 antes de crear nada: haz fetch allí
primero. También se rechaza un commit que no pertenezca a ninguna rama, tag o rama
remota. El panel **Cambios de rama** muestra los cambios de su rama respecto al commit actual del workspace y abre el texto de cada ruta en la base de
fusión y en la punta de la rama. Solo muestra trabajo con commit, dentro de las
subrutas permitidas y la postura DLP del workspace; los cambios sin commit están
en **Cambios**.

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

Añade `--server`, `--tenant` y `--token-file` (o el contexto de cliente activo)
como en la [referencia CLI](/reference/cli/). El aislamiento es `native` en
esta versión; `container` y `sandbox` devuelven HTTP 422 antes de crear una
ejecución. Selecciona `native` para usar el ejecutor integrado.

Resultado: un recurso de ejecución. La fila viva gestionada es única por
ámbito de observación e id externo. Las lecturas que nombran una fila usan
`live_ref`, no el id de sesión del proveedor que dos homes pueden compartir.

## 4. Interrumpir o detener

| Intención | Consola | CLI | Resultado |
|---|---|---|---|
| Terminar el turno activo, conservar el proceso y la conversación | control interrupt de la sesión viva | `olivares agent session interrupt <run-ref>` | el turno termina; el proceso sigue usable para el siguiente (`CHANGELOG.md` `[26.9.0]`) |
| Terminar la ejecución | control stop | `olivares agent session stop <run-ref>` | el recurso de ejecución; el runtime sigue recogiendo el hijo |

Las ejecuciones ligadas a trabajo envían su cerca de arrendamiento exacta. Los
resultados obsoletos o inciertos siguen explícitos. Reanudar continúa solo en
el mismo home demostrado.

Una interrupción Grok usa ACP `session/cancel`, que es una notificación sin
acuse. La interrupción resuelve aprobaciones pendientes, cancela y deja el
turno abierto hasta que vuelve el resultado correlacionado del propio prompt
(`CHANGELOG.md` `[26.9.0]`). No trates una cancelación silenciosa como un
recibo confirmado del proveedor.

## Relacionado

- [Tu primera hora](/es/how-to/first-hour/) — token de configuración, verificación adicional administrativa, fuente de credencial Claude.
- [Ejecutar Claude Code con Olivares](/how-to/run-claude-code-with-olivares/) — topologías de co-despliegue.
- [Integrar Codex](/how-to/integrations/codex/) / [Integrar Grok Build](/how-to/integrations/grok/) — conector y hook PEP.
- [API de runtime de sesión](/reference/session-runtime-api/) — listar, adjuntar, input, stop; PTY Community y corte de edición.
- [Operación en vivo y sesiones](/reference/modules/ii-sessions/) — `live_ref` y atribución.
- [Configuración](/reference/configuration/) — variables de fijación del controlador.
- [Referencia CLI](/reference/cli/) — `olivares agent session *` (generada desde el binario).
