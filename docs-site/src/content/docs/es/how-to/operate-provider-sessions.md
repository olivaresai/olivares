---
title: Operar una sesión de proveedor
description: >-
  Elige una herramienta de IA y su cuenta o proveedor, trabaja en una carpeta de
  proyecto, aprueba acciones, interrumpe un turno, detén y reanuda desde la
  consola o la CLI.
---

Completa [Tu primera hora](/how-to/first-hour/) antes de empezar aquí. Las
herramientas se ejecutan en el host del motor, incluido dentro del contenedor en
una instalación con Compose. El inicio de sesión y las carpetas de tu terminal
cliente no están disponibles allí automáticamente.

## Elige cómo se conecta la herramienta

**Herramientas de IA** (`/agent-tools`) instala herramientas y ejecuta sus propios
flujos de inicio de sesión. En un motor nativo, la instancia predeterminada usa
el inicio de sesión del propio usuario del motor; las cuentas con sesión iniciada
a través de Olivares siguen siendo instancias separadas. Selecciona la cuenta que
quieras usar. La instantánea de la cuenta muestra el correo, el plan, las
ventanas de uso y los modelos que informa la herramienta. Los valores que faltan
siguen sin estar disponibles, y las actualizaciones fallidas se marcan como
obsoletas.

**Claves de API** en **Herramientas** (`/agent-tools`) guarda las claves de API y
los endpoints de modelos locales. Añade y prueba allí el proveedor; después
úsalo desde **Nueva sesión**. Una clave de proveedor y una suscripción a una
herramienta son formas distintas de conectarse. Ollama necesita un endpoint
accesible y un modelo, sin clave de API. Consulta
[Añadir un proveedor](/how-to/add-a-provider/).

Para los perfiles de Claude con `managed_injection` que no nombran **ningún**
proveedor, `OLIVARES_SESSION_RUNTIME_WIF` o `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`
aporta la credencial de inferencia de todo el host. Un perfil vinculado a un
registro de Proveedores usa ese registro en su lugar; si no se puede abrir su
credencial, el lanzamiento se rechaza sin recurrir a la credencial del host. Un
perfil `provider_account_home` usa su inicio de sesión autorizado de la
herramienta y no necesita ninguna de las dos variables. Consulta
[dónde se aplican las variables de entorno](/how-to/add-a-provider/#the-environment-variables-and-where-they-still-apply).

## Inicia y conversa

Abre **Sesiones → Nueva sesión** y elige una herramienta lista. El motor rellena
el perfil y la carpeta. Para la instalación de Compose del README, elige
**Cambiar carpeta** e introduce `/project` para trabajar en tu proyecto del host
montado. Dejar la carpeta predeterminada no requiere escribir ninguna ruta. Deja
**Primer mensaje** vacío para empezar y escribe después en la conversación, o
escríbelo antes de pulsar **Iniciar**.

Una herramienta que usa una clave de proveedor utiliza su modelo guardado o te
deja seleccionar un modelo devuelto por la prueba de conexión. Si el modelo es
obligatorio pero la prueba no ha devuelto ninguno, prueba primero la conexión en
**Claves de API** de **Herramientas**. Claude Code puede usar su modelo
predeterminado nativo.

Para la instalación de Compose del README, ejecuta la CLI dentro del contenedor
del motor. Allí encuentra el motor local y su certificado. Inicia sesión con la
cuenta de administrador de Olivares que creaste durante la configuración, no con
tu suscripción a la herramienta.

Al usar una clave de proveedor con una herramienta que requiere un modelo, como
Codex, guarda un valor predeterminado en **Claves de API** de **Herramientas**
antes de ejecutar `session start` más abajo. Si ni el proveedor ni el perfil de
la herramienta tienen un modelo configurado, añade `--model <tested-model-id>` al
comando `session start`, sustituyendo el marcador por un ID de modelo devuelto
por la prueba de conexión. Un modelo seleccionado en **Nueva sesión** se aplica
solo a ese lanzamiento desde la consola; no establece el modelo de un lanzamiento
desde la CLI.

Define el archivo de Compose una sola vez en tu terminal del host:

```sh
export COMPOSE_FILE="$HOME/olivares/deploy/compose/docker-compose.yml"
docker compose exec olivares olivares login
docker compose exec olivares olivares tool ls
docker compose exec olivares olivares tool providers
docker compose exec olivares olivares session start /project --name first-hour
docker compose exec olivares olivares session send first-hour "Explain this project."
```

Para una instalación nativa, ejecuta los mismos comandos de Olivares directamente
en el host del motor y sustituye `/project` por la ruta de tu proyecto. El cliente
imprime qué herramienta y qué conexión ha elegido. Si hay varias herramientas,
selecciona una con `--tool`; `--profile` selecciona un perfil existente. Cerrar la
terminal del cliente deja la sesión en marcha en el motor. Para volver a conectar
con su salida:

```sh
docker compose exec olivares olivares session follow first-hour
```

## Aprueba, interrumpe, detén y reanuda

En **Más opciones**, **Pedir confirmación antes de cada acción** hace que la
herramienta pida confirmación antes de actuar. En **Reglas**, la solicitud que
espera tu respuesta aparece abierta junto a la cola, con su comando, a qué afecta
y la regla que la pidió. Elige **Aprobar una vez** (`A`), o **Rechazar** (`D`) y
añade una nota. Un lanzamiento que requiere aprobación de la organización también
espera ahí; envía tu mensaje una vez aprobado el lanzamiento.

Usa **Interrumpir** para terminar el turno actual dejando la conversación abierta.
Usa **Detener** para terminar el proceso, confirmando si se te pide. **Reanudar**
reinicia esa misma sesión con el mismo perfil, cuenta y carpeta; vuelve a
comprobar credenciales y política. Envía un mensaje nuevo después de reanudar.

```sh
docker compose exec olivares olivares session interrupt first-hour
docker compose exec olivares olivares session stop first-hour
docker compose exec olivares olivares session resume first-hour
docker compose exec olivares olivares session send first-hour "Continue explaining this project."
```

Una cuenta cambiada o no disponible es un rechazo de lanzamiento, no permiso para
cambiar a otro inicio de sesión. Un turno de Grok interrumpido usa cancelación ACP
sin acuse de recibo; espera al resultado correlacionado del turno antes de
considerarlo terminado.

## Ajustes opcionales de perfil y git

Para un home de herramienta configurado que ya exista, la pestaña **Perfiles de
proveedor** de **Sesiones** (`/sessions?tab=profiles`) permite a un administrador
registrar un perfil e inspeccionar si está listo para lanzar. El perfil y los
enlaces de origen de una cuenta con nombre también aparecen en su detalle en
**Herramientas**. Los homes deben existir en el host de ejecución. Usa **Más
opciones → Opciones avanzadas de lanzamiento** en **Nueva sesión** para
seleccionar ese perfil. Las sustituciones de binario eligen un ejecutable; si no,
el motor prueba primero la instalación gestionada verificada más reciente y
después la herramienta en su `PATH`. Consulta [Configuración](/reference/configuration/).

Para la carpeta raíz de un repositorio Git con al menos un commit, **Trabajar en
un nuevo worktree Git** crea una rama y un directorio de trabajo para la sesión.
Reanudar los reutiliza. Un traspaso con rama y commit ofrece **Abrir en un nuevo
worktree de sesión**; elige una carpeta registrada cuyo repositorio contenga ese
commit. El motor rechaza antes de crear el worktree los ajustes de repositorio no
soportados o inseguros.

Una carpeta puede guardar un comando de preparación para los nuevos worktrees. Una
persona debe aprobar su texto exacto, por SHA-256, antes de que se acepte un inicio
con worktree; cambiar un solo carácter exige aprobarlo de nuevo. Por ahora nada
ejecuta el comando. Consulta
[Aprobar el comando de preparación de una carpeta](/reference/session-runtime-api/#approving-a-folders-setup-command).

La limpieza se niega a descartar trabajo sin confirmar o sin fusionar.
**Descartar también el worktree y la rama** autoriza explícitamente esa pérdida; los
archivos ignorados se eliminan junto con el worktree. Los worktrees comparten los
metadatos de Git del repositorio. Consulta la
[referencia de CLI de sesión](/reference/cli/#command-olivares-session-start) para
las opciones de worktree y la [API de runtime de sesión](/reference/session-runtime-api/)
para los permisos de perfil, las llamadas de ciclo de vida y los límites de
aislamiento.
