---
title: API de runtime de sesión (CLI oficiales)
description: >-
  Superficie HTTP Community que lista, adjunta, alimenta y detiene procesos
  propios de Claude Code, Codex y Grok CLI. Permisos, PTY, reanudación y reconexión.
---

El plano de control **lanza la CLI del proveedor**. No sustituye Claude Code,
Codex ni Grok Build. Las sesiones y los terminales son un módulo de este
producto, no el producto.

Esta página documenta las rutas Community de operate bajo `/v1/m/sessions/runs`.
Ya existen en el [documento OpenAPI beta](/reference/api-beta/). 26.10 añade
el contrato de driver, un runner PTY local y los recorridos J01–J08 como tests.

## Corte de edición

| Edición | Qué hace | Qué no hace |
|---|---|---|
| **Community (esta página)** | Hijo local propio: lanzar, stdin/stdout/stderr, attach con cursor, reanudar la conversación exacta, reconectar un stream vivo, detener con código de salida observado. Las filas de sesión y la evidencia siguen en el módulo II. | Motor Identity & Scale de varios paneles, listener mTLS, entrada de paneles del cockpit, chunk xterm |
| **Overlay Identity & Scale** | Motor comercial session-cockpit (listener, paneles, ledger). Las rutas viven bajo `/v1/m/session-cockpit/` cuando el módulo está presente. | No sustituye `/v1/m/sessions/runs` |

Una build Community responde el namespace del overlay por **ausencia** (404).
No monta un stub 501.

La ruta del hook de Claude Code sigue siendo `olivares claude-hook` (PEP
PreToolUse). Es observación y enforcement, no una CLI de recambio.

## Permisos

Enforcement existente en las rutas del módulo:

| Permiso | Rutas |
|---|---|
| `sessions:run:read` | `GET /runs`, `GET /runs/{ref}`, `GET /runs/{ref}/events`, `GET /runs/{ref}/attach`, `GET /runs/{ref}/handoff`, `GET /runs/{ref}/activity`, `GET /runs/{ref}/activity/agents/{agent}`, `GET /runs/{ref}/messages` |
| `sessions:run:write` | `POST /runs`, `POST /runs/{ref}/input`, `POST /runs/{ref}/interrupt`, `POST /runs/{ref}/stop`, `POST /runs/{ref}/resume`, `POST /runs/{ref}/move`, `POST /runs/{ref}/handoff` |
| `sessions:run:admin` | `POST /runs/{ref}/cleanup`, `DELETE /runs/{ref}` |

Un viewer puede listar. Crear, input y stop exigen write. El autorizador de la
ruta es el punto de enforcement; un permiso ausente es 403.

## Rutas que lee la consola

Base: `/v1/m/sessions`. Autentica. Envía `X-Olivares-Tenant`.

| Método | Ruta | Resultado |
|---|---|---|
| `GET` | `/runs` | Página de ejecuciones gestionadas |
| `GET` | `/runs/{ref}` | Una ejecución. `state` se deriva. `exit_code` se observa. No hay campo de éxito fabricado |
| `GET` | `/runs/{ref}/events` | Filas de evidencia de ciclo de vida |
| `GET` | `/runs/{ref}/attach?from={seq}` | SSE: marcos `output`, `lag` si el anillo desalojó por debajo del cursor, `end` o un `notice` de no vivo |
| `POST` | `/runs/{ref}/input` | stdin. Stream-json usa `line`/`message`. Codex/Grok usan `text`. 202 `{accepted:true}` |
| `POST` | `/runs/{ref}/stop` | SIGTERM y luego SIGKILL del grupo. Código de salida observado en la fila |
| `POST` | `/runs/{ref}/resume` | Nueva generación de proceso. Conversación almacenada exacta |
| `POST` | `/runs/{ref}/interrupt` | Cancela el turno activo. El proceso permanece |
| `POST` | `/runs/{ref}/move` | Cuerpo `{provider_profile_ref}`. Revincula una sesión de Claude Code detenida o fallida a otro perfil de la misma herramienta y lleva su conversación. No inicia nada. 200 con la ejecución |
| `GET` | `/runs/{ref}/handoff` | El resumen del que partiría un traspaso: `summary`, `source_driver`, `turns`, `transcript_bytes`. Solo lectura, registrado en el registro de auditoría. Una sesión en ejecución se lee tal como está; una que no ha empezado responde 409 |
| `POST` | `/runs/{ref}/handoff` | Cuerpo `{provider_profile_ref, summary?, name?}`. Inicia una sesión nueva en ese perfil, vinculada a esta. 201 con la ejecución más `summary_state` (y `first_turn` cuando no es `delivered`); 202 mientras el lanzamiento espera aprobación |
| `GET` | `/runs/{ref}/activity` | Lo que está haciendo la herramienta de una sesión de Claude Code o Codex, leído en cada petición de su propio registro nativo: `agents`, `tasks` (comandos y trabajo en segundo plano), `files` (ruta, `added`, `removed`, `created`), `todos`, `skills`, `mcp_servers`, `model`, `context_tokens`, `context_window` (nulo salvo que la herramienta lo informe) y `context_percent` (porcentaje para Claude Code, null cuando se desconoce), `compactions` y `transcript` (`read`, `pending` antes de que la herramienta lo escriba, `missing` cuando debería estar). Los agentes y las tareas están en `running`, `completed`, `failed`, `stopped` o `scheduled`. Codex usa su archivo rollout sin lanzar otro app-server; los tokens de contexto describen la última petición, no el uso acumulado. Solo lectura; se registra en el registro de auditoría una vez por lector y sesión cada 15 minutos. Una herramienta no admitida responde 422; una sesión a la que se dieron secretos de vault o una credencial de repositorio responde 409, y también una sesión que usó un token de registro de paquetes cuando está detenida o se ejecuta tras una reanudación que sigue a un lanzamiento que usó uno |
| `GET` | `/runs/{ref}/activity/agents/{agent}` | La transcripción de un subagente de Claude Code en turnos simples (`role`, `text`). Solo lectura, registrada en el registro de auditoría. 404 para un agente desconocido. Codex informa del estado de sus hijos en la respuesta Activity raíz; no se ofrece la lectura de transcripciones de hijos |
| `GET` | `/runs/{ref}/messages` | Los mensajes de una sesión de Claude Code o Codex, leídos en cada petición de la transcripción de la propia herramienta, en páginas: `items` (`id`, `role` `user` o `assistant`, `at`, `first_line`), `total`, `next`, `transcript` (`read`, `pending`, `missing`). Por defecto solo lo que envió la persona; `role=all` añade las respuestas de la herramienta. `q` conserva los mensajes que contengan esa frase en cualquier punto (sin distinguir mayúsculas, como máximo 256 bytes); `at` (RFC 3339) conserva los mensajes desde esa hora en adelante, de modo que el primer elemento es el ancla; `order` es `asc` (por defecto) o `desc`, `limit` como máximo 200 y `cursor` el id desde el que continúa una página (`next`); los elementos van siempre del más antiguo al más reciente. `id` lee un mensaje entero (`text`, y `later_same`, cuántos mensajes posteriores del mismo remitente repiten su primera línea; no se aplica ningún filtro y `total` es el número de mensajes de la sesión). El `id` de un mensaje es su posición entre los mensajes de la sesión y no cambia a medida que crece la transcripción. Solo lectura; se registra en el registro de auditoría una vez por lector, sesión y ventana de 15 minutos. Una herramienta no admitida responde 422; una sesión a la que se dieron secretos de vault o una credencial de repositorio responde 409; un `id` desconocido 404; un parámetro no válido 400 |
| `GET` | `/runs/{ref}/diff` | Rama de una sesión worktree frente a su inicio: `branch`, `base`, `head` y `files` modificados (`path`, `status`). 404 sin worktree |
| `GET` | `/runs/{ref}/diff/file?path=` | Texto de una ruta en `base` y `head` (`original`, `modified`, cada uno limitado al menor entre `max_read_bytes` del workspace y 64 KiB) |

Para las llamadas de Codex Code Mode cuyos argumentos de plan no se conservan en el rollout, Activity lee además la última notificación `turn/plan/updated` que ya recibió el puente en vivo. Recargar el navegador usa la misma sesión del lado del servidor; no se lanza ningún app-server para refrescar. Una vez desaparecido el handle en vivo, solo se pueden leer los datos que conserva el propio Codex.

Para Claude Code, la ventana procede de su `modelUsage[model].contextWindow` nativo del marco de resultado, conservado tras detener y reconectar. Un informe coincidente y sin ambigüedad aporta `context_percent` como `100 × context_tokens / context_window`; en otro caso ambos campos son `null` y la CLI y la consola muestran los tokens con un aviso de ventana desconocida. Un nuevo lanzamiento de la herramienta borra el informe de ventana anterior.

Las tareas de workflow pueden incluir `workflow`: `run_id`, `name`, `duration_ms`, `agents_total`, `agents_done`, `agents_failed` y `phases`. Cada fase tiene `index`, `title`, `detail` opcional y `state`, y los mismos recuentos de agentes. Los recuentos describen los agentes observados hasta el momento. Mientras está en vivo, el estado de una fase describe los agentes observados en ejecución o fallidos. Que un agente termine no demuestra que el script haya salido de su fase, así que no marca la fase como completada ni identifica un paso actual del script. El resumen final del workflow aporta los estados de fase completados.

Las instantáneas de workflow en vivo proceden de la salida `system/task_progress` de Claude Code, compartida con el stream de attach. El `workflows/<runId>.json` completado de la herramienta rellena el resumen final bajo las reglas de home, enlaces y tamaño de la transcripción. Las lecturas del resumen están limitadas a 8 MiB por archivo y 64 MiB por petición; los registros o resultados nativos mayores hacen ilegible el resumen y conservan el progreso en vivo disponible. Los resúmenes ilegibles y los huecos en el búfer en vivo acotado contribuyen a `unreadable`.

### Mover una sesión y traspasarla

Una sesión continúa en otra cuenta de dos maneras, y la consola y
`olivares session move` / `olivares session handoff` ofrecen ambas. `move` lleva la conversación
almacenada de una sesión de Claude Code a otro perfil de la misma herramienta, así que solo las sesiones de
Claude Code se pueden mover con él. `handoff` inicia una sesión nueva a partir de la conversación de una sesión de
Claude Code, Codex, Gemini CLI u OpenCode.

Una ejecución lleva `limit` cuando su herramienta terminó el último turno por un límite:
`{kind, resets_at, account_ref}`. `kind` es `usage` (se agotó la asignación del plan de
la cuenta) o `rate` (el proveedor está limitando las peticiones). `resets_at` es RFC 3339,
está presente cuando la herramienta informa de cuándo se levanta el límite y ausente en
otro caso, y `account_ref` es el perfil bajo el que se ejecutó el turno. Claude Code
aporta el hecho en su propio resultado (`api_error: usage_limit_reached` o HTTP 429) y la
hora de reinicio en su `rate_limit_event`; Codex lo aporta en un turno fallido de tipo
`usageLimitExceeded` o `rateLimitExceeded` y la hora de reinicio en sus ventanas
`account/rateLimits/updated`; cuando se agotan dos ventanas, se registra el reinicio más
tardío, y una hora de reinicio a más de un año desde ahora se omite. El ledger gana un
evento `limit_reached` cuando el hecho se registra por primera vez; el mismo hecho
encontrado de nuevo no añade ninguno. El siguiente turno que tenga éxito borra `limit`,
y también lo borra un traslado a otra cuenta. Un resultado de cualquier otro tipo nunca
lo establece.

`POST /runs/{ref}/move` conserva la misma sesión. Necesita una sesión `stopped` o
`failed` cuya conversación esté en el home de su cuenta, y un perfil de destino de la
misma herramienta que aceptaría un lanzamiento bajo él (activo, del entorno de este nodo,
una fuente autorizada). El motor copia esa única conversación, la transcripción y sus
archivos de subagentes y de resultados de herramientas, al home de destino, nunca un
inicio de sesión, un ajuste ni otra conversación, y revincula la fila de la ejecución, la
fila en vivo y el ledger en una sola transacción. La ejecución conserva su `run_ref` y su
id de conversación del proveedor; el ledger gana un evento `moved` cuyo detalle nombra
ambos perfiles por referencia y cuenta los archivos trasladados, y una sesión fallida
conserva su `reason`. El home de origen solo se lee. Un rechazo no escribe nada y elimina
lo que hizo el traslado:

- 409 para una sesión en ejecución, una sesión a la que se dieron secretos de vault, una
  credencial de repositorio o un token de registro de paquetes (no se puede demostrar que
  lo que imprimió esté libre de ellos), una fila cuyo home
  no es propiedad de su perfil, un id de conversación ya vinculado a otra sesión
  bajo el destino, un destino que guarda una versión distinta de la conversación,
  un destino de otra herramienta, o un archivo que cambió mientras se trasladaba
  (reintenta);
- 422 para un enlace, un segundo nombre para un archivo o un archivo especial dentro de la
  conversación, y para la sesión de otra herramienta.

Una copia de la misma conversación que dejó en el destino un traslado anterior se
sustituye por la más reciente. No se inicia nada: el siguiente `POST /runs/{ref}/resume`
es el ordinario, de modo que le aplican la política, los presupuestos y las aprobaciones
del perfil de destino. Una sesión que aún no tiene conversación se traslada sin nada que
llevar y vuelve a empezar en el destino.

`GET /runs/{ref}/handoff` y `POST /runs/{ref}/handoff` continúan en otra herramienta,
donde no existe reanudación nativa. Leen la conversación almacenada de una sesión de Claude Code, Codex, Gemini CLI u OpenCode (una sesión de
Grok responde 422). La sesión puede estar `stopped`, `failed`, `running` o `idle`; una que no ha empezado
responde 409. La vista previa lee una sesión en ejecución tal como está. El POST detiene primero una sesión
en ejecución, como lo hace `POST /runs/{ref}/stop`, y lee su conversación cuando su herramienta ha terminado;
toda negativa que no necesita la detención (el perfil de destino, los secretos de vault, una credencial de
repositorio) llega antes de ella, y una negativa posterior dice que la sesión se detuvo y puede reanudarse.
Codex se lee de su rollout, Gemini CLI de su registro de chat y OpenCode con su propio `opencode export`,
ejecutado una vez y sin conexión bajo el perfil de la sesión.
La vista previa se genera a partir de la conversación almacenada sin un modelo, de modo
que la persona pueda revisarla y editarla, y cada lectura de ella se registra en el
registro de auditoría como `sessions.run.handoff_preview`. El POST inicia una sesión
nueva en el perfil elegido, en el mismo workspace y con la misma plantilla, cuyo primer
turno es el `summary` revisado (generado si falta) y la ruta de la transcripción,
escrita en el home de configuración del perfil de destino con un nombre propio. La
transcripción lleva los mensajes completos hasta 256 KiB cada uno, las llamadas a
herramientas y los resultados acortados, e indica cuántos registros no pudo leer. La
ejecución nueva informa `handoff_of`, y el ledger de la original gana un evento
`handed_off`. La respuesta lleva `summary_state`:

| `summary_state` | Significado |
|---|---|
| `delivered` | El primer turno llegó a la sesión nueva. |
| `not_delivered` | No llegó. `first_turn` contiene el texto que enviar. |
| `unknown` | Se intentó y no se confirmó el resultado; comprueba antes de volver a enviar `first_turn`. |
| `waits_for_approval` | El lanzamiento queda retenido para una persona (202); no se envía ningún primer turno automáticamente. |

`first_turn` está presente solo cuando el estado no es `delivered`, porque nombra la
ruta del archivo adjunto. La conversación de origen se rechaza con 409 cuando a su
sesión se le dieron secretos de vault, una credencial de repositorio o un token de
registro de paquetes, porque un traspaso
la envía a otro proveedor, y con 422 por encima de 256 MiB o cuando contiene un
registro mayor de 32 MiB.

### Opción de worktree

`POST /runs` admite el booleano opcional `worktree`. Si es true y `workspace_ref`
nombra un workspace registrado que es la raíz de un repositorio Git, la sesión
trabaja en un worktree y una rama nuevos (`workspace_path` es el worktree y la
ejecución informa `worktree_branch`). Ausente, null o false conserva el comportamiento
actual. El motor responde 422 para un workspace que no sea un árbol de trabajo
Git o la raíz del repositorio, sin commit, de solo lectura o con carpetas de solo
lectura, con configuración Git que nombre un filtro o incluya un archivo, con
aislamiento no nativo o sin workspace; y 503 si el nodo no tiene directorio de
worktrees. `POST /runs/{ref}/resume` vuelve al mismo worktree.

`POST /runs/{ref}/cleanup` admite `{"discard_worktree": true}` opcional; solo true
explícito confirma. Sin cuerpo mantiene el contrato anterior. Libera la sesión
con worktree cuando su rama está fusionada en la rama actual del workspace,
el worktree está en esa rama y no tiene archivos sin commit. Si no, responde 409
por trabajo sin fusionar, HEAD separado o worktree inaccesible, y la ejecución
queda detenida. Repite con `discard_worktree` para borrar worktree y rama de todos
modos; si es inaccesible, libera la sesión y deja el worktree donde está. El evento
`cleaned` registra lo ocurrido y la punta de la rama.

### Aprobar el comando de preparación de una carpeta

Un comando de preparación pertenece a la carpeta registrada, en el almacenamiento del
motor. Los archivos del repositorio y las variables de entorno no pueden cambiarlo.
Por ahora nada ejecuta el comando en estas operaciones ni durante el inicio de la sesión.

- `PUT /workspaces/{ref}/setup-command` recibe `{"command":"npm ci"}`. Se conserva el
  texto UTF-8 exacto, incluidos los espacios en blanco; como máximo 8192 bytes, sin NUL.
  Una cadena vacía borra el comando. Requiere administración del workspace.
- `GET /workspaces/{ref}` devuelve `setup_command`, `setup_command_sha256` y, tras la
  aprobación, `setup_approval` con `sha256`, `actor` y `at`.
  `setup_approved` es true solo mientras esa aprobación cubra el texto actual.
  Tras una edición, `setup_approval` permanece como evidencia, así que comprueba
  `setup_approved`, nunca la presencia de `setup_approval`.
- `POST /workspaces/{ref}/setup-command/approve` recibe `{"sha256":"<reviewed hash>"}`.
  Requiere administración del workspace y rechaza una identidad de agente. El hash
  debe coincidir con el texto almacenado en ese momento; una edición concurrente
  devuelve 409. La aprobación y su entrada de auditoría `sessions.workspace.approve-setup`
  se confirman juntas.

Un inicio con worktree devuelve 409 cuando el comando no tiene una aprobación
coincidente, antes de crear un worktree. Su frase nombra el comando y el paso de
aprobación. Cambiar un solo carácter, incluidos los espacios en blanco, exige aprobar
de nuevo. La última aprobación permanece como evidencia; su hash debe ser igual a
`setup_command_sha256` para aplicarse, de modo que volver a fijar el comando a un texto
que ya se aprobó antes vuelve a aplicar esa aprobación. Quien pueda leer la carpeta puede
leer el comando, así que no pongas secretos en él.
`POST /runs` con `"no_setup":true` omite la preparación solo para ese inicio. Un traspaso
de una sesión que tiene un worktree inicia un worktree nuevo y necesita la misma
aprobación; no tiene opción para omitirla. Los demás inicios y reanudaciones conservan su
comportamiento actual.

Con la CLI, usa `olivares agent workspace setup <ref> --command 'npm ci'`,
léelo con `olivares agent workspace setup <ref>` y aprueba el hash mostrado con
`olivares agent workspace approve-setup <ref> --sha256 <hash>`.
`olivares session start --worktree --no-setup` omite la preparación una vez.

### Iniciar un worktree en un commit o una rama

Con `worktree`, `POST /runs` admite `worktree_from`: un id de commit completo
(40 o 64 dígitos hexadecimales en minúsculas) o una rama local del repositorio.
El nuevo worktree y su propia rama nueva parten de ahí; la rama nombrada y el
checkout del workspace no se mueven. Así se abre el trabajo de un traspaso cuyo
contenido incluya `branch` y `sha`. Git resuelve el valor a un id completo y solo
ese id se usa. El motor responde 422 antes de crear nada para un commit ausente,
una rama desconocida, una expresión de revisión, un rango, una opción o
`worktree_from` sin `worktree`. Ausente, parte del commit actual como antes.

`GET /runs/{ref}/diff` lista las rutas cambiadas por la rama del worktree desde
su separación del commit actual del workspace (`base` es la base de fusión y
`head` la punta, ambos ids completos; máximo 200 rutas, `truncated` indica más).
`GET /runs/{ref}/diff/file?path=` devuelve el texto de la ruta en `base` y `head`,
vacío donde no exista. Lee objetos Git con comandos plumbing: no muestra cambios
sin commit y requiere el mismo permiso que leer la ejecución. Respeta las reglas
del workspace: las rutas fuera de las subrutas permitidas no se listan y responden
404; la denegación DLP responde 403 y se audita como lectura de archivo; un
archivo mayor de 16 MiB responde 413. Una sesión sin worktree o una ejecución de
otro tenant responde 404; una rama ausente o sin historia común responde 409.
`worktree_from` también responde 422 para un commit que no pertenezca a ninguna
rama, tag o rama remota del repositorio.

Reconectar tras un attach cortado es `GET …/attach?from={last+1}` sobre el
**mismo** proceso vivo. Tras pérdida de proceso, attach dice que no está vivo.
Resume arranca una generación nueva. Reconnect no inventa un proceso de
recambio (SDD R04).

### Configuración de agente que lleva un repositorio

Un repositorio clonado puede llevar configuración de agente que ejecuta un programa o
redirige el tráfico. El motor la trata como entrada no confiable cuando una sesión
comienza en una carpeta. Lee los archivos siguientes, nunca los ejecuta, y
recoge solo las claves indicadas. Un valor vacío (`{}`, `[]`, `""`, `false`,
`null`) no configura nada y no se marca.

| Herramienta | Archivo | Se marca |
| --- | --- | --- |
| Claude Code | `settings.json` y `settings.local.json` en la carpeta `.claude` | `hooks`, `apiKeyHelper`, `statusLine`, `mcpServers`, `enableAllProjectMcpServers`, `enabledMcpjsonServers`, `awsAuthRefresh`, `awsCredentialExport`, `otelHeadersHelper`, `fileSuggestion`, `enabledPlugins`, `extraKnownMarketplaces` y las entradas de `env` con un nombre de los listados en la última fila |
| Claude Code | `.mcp.json` | `mcpServers` |
| Codex | `.codex/config.toml` | `hooks`, `notify`, `mcp_servers`, `model_providers`, `otel`, cualquier `*_base_url` de nivel superior y las entradas de `shell_environment_policy.set` con un nombre de los listados en la última fila; las mismas claves bajo `profiles.<name>` |
| Gemini CLI | `.gemini/settings.json` | `hooks`, `mcpServers`, `toolDiscoveryCommand`, `toolCallCommand`, `proxy`, `tools.discoveryCommand`, `tools.callCommand`, `mcp.serverCommand`, `telemetry.otlpEndpoint` |
| Gemini CLI | `.gemini/.env`, `.env` | las entradas con un nombre de los listados en la última fila |
| Nombres de entorno | | `NODE_OPTIONS`, `NODE_EXTRA_CA_CERTS`, `NODE_TLS_REJECT_UNAUTHORIZED`, `LD_PRELOAD`, `LD_LIBRARY_PATH`, `LD_AUDIT`, `DYLD_INSERT_LIBRARIES`, `BASH_ENV`, `PATH`, `PYTHONPATH`, `GIT_SSH_COMMAND`, `GIT_EXTERNAL_DIFF`, `SSL_CERT_FILE`, `CLAUDE_CODE_SHELL_PREFIX`, `ANTHROPIC_CUSTOM_HEADERS`, `AWS_ENDPOINT_URL*` y cualquier nombre que termine en `_BASE_URL`, `_API_BASE`, `_ENDPOINT` o `_PROXY` (`NO_PROXY` solo restringe un proxy y no se marca) |

Un archivo que existe pero no se puede leer en su formato (JSON o TOML no válido,
de más de 1 MiB, anidado a más de 64 niveles, que no sea un archivo regular) cuenta como
marcado, porque no se puede demostrar que sea seguro. En TOML cada segmento de una clave
con puntos o de un encabezado de tabla es un nivel, como en el documento analizado, y se
suma a los corchetes que lo rodean: un encabezado de 30 segmentos sobre 35 tablas en línea
anidadas son 65 niveles. Una vez que un archivo tiene un encabezado `[[array]]`, los
segmentos de encabezado cuentan el doble, porque cada uno puede nombrar un array y su
elemento. Un archivo demasiado grande se
somete a hash completo en cada comprobación. Una carpeta sin nada marcado se inicia como antes.

- `GET /workspaces/{ref}/project-config` lista las claves marcadas por archivo con
  sus valores (una contraseña dentro de una URL se enmascara) y el `sha256` exacto de
  esa parte marcada, sobre los archivos de las tres herramientas, además de `approved` y la
  última `approval`. Lee la propia carpeta; un worktree iniciado desde otro commit
  puede llevar archivos rastreados distintos, y su rechazo nombra su propio hash.
- `POST /workspaces/{ref}/project-config/approve` recibe `{"sha256":"<reviewed hash>"}`,
  requiere administración del workspace y rechaza una identidad de agente. La aprobación
  almacena quién, cuándo y el hash, con una entrada de auditoría
  `sessions.workspace.approve-project-config` en la misma transacción.

Un inicio o una reanudación de una herramienta que lee un archivo marcado devuelve 409
a menos que la aprobación almacenada sea igual al hash actual. Cambiar un solo carácter
de un valor marcado, o añadir o quitar una clave marcada, exige aprobar de nuevo; cambiar
claves que no están marcadas, no. No hay opción para omitir la comprobación.

Una sesión de Claude Code que lleva los hooks del motor se lanza con
`--setting-sources ""`; en Claude Code 2.1.299 esa sesión no carga ningún archivo de
ajustes de proyecto ni local ni ningún `.mcp.json` de proyecto, así que ese inicio no se
rechaza. Un lanzamiento de Claude Code sin los hooks del motor (control remoto, o un motor
sin endpoint de hooks) lee esos archivos y se comprueba. Codex respeta
`.codex/config.toml` solo para un proyecto de confianza en su home de cuenta e ignora
el archivo de un proyecto no confiable; el motor no puede preguntar a la herramienta, así
que comprueba ese archivo y una persona lo aprueba una vez. Gemini CLI 0.64.0 iniciado como
lo inicia el motor (`gemini --acp`) ignora `.gemini/settings.json` y `.gemini/.env`
de una carpeta en la que su home de cuenta no confía, y en una carpeta de confianza ejecuta
`tools.discoveryCommand`, los `hooks` y los comandos de `mcpServers`, y toma
la URL base de `.gemini/.env`; el motor comprueba esos archivos por la misma
razón. También carga archivos de carpetas superiores, que esta comprobación no lee.

Con la CLI, usa `olivares agent workspace project-config <ref>` para leerla y
`olivares agent workspace approve-project-config <ref> --sha256 <hash>` para aprobar.

## Transporte (Community)

Cada CLI oficial se lanza en el transporte que necesita su propia forma de
operate, y `cliruntime.LaunchTransport` declara cuál. Hoy las tres formas son
protocolos stdio, así que el composition root cablea
`sessions.NewProcRunner()`: stdin, stdout y stderr son tuberías y siguen siendo
flujos distintos.

**Claude Code rechaza un terminal en stdin.** Su forma `--print` con
stream-json responde `Error: Input must be provided either through stdin or as
a prompt argument when using --print` y sale 1 sin un solo marco de protocolo.
Un terminal es lo que necesita una forma interactiva; `sessions.NewPTYRunner()`
sigue disponible en Linux para eso. El aislamiento container y sandbox se
rechaza en ambos.

El contrato de driver vive en `modules/sessions/cliruntime`. Clases: `claude`,
`codex`, `grok`. La conformidad corre siempre contra un fake en proceso y
contra un peer PTY local. Si `claude` / `codex` / `grok` están en PATH, un test
aparte posee el binario real, lo detiene y registra la salida. No envía un
turno de modelo.

## Relacionado

- [Operar una sesión de proveedor](/how-to/operate-provider-sessions/)
- [Módulo II — operación en vivo](/reference/modules/ii-sessions/)
- [Conectar Claude Code](/how-to/connect-claude-code/)
- [OpenAPI beta](/reference/api-beta/)
