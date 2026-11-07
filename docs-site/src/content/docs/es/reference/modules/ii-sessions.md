---
title: "Módulo II — operación en vivo y sesiones"
description: >-
  La capa operativa en vivo por sesión de agente: acción actual, tokens/coste en
  vivo, un estado de Claude Code derivado y una línea temporal reproducible,
  transmitida sobre server-sent events. Qué deriva, qué se mantiene honestamente
  vacío y cuáles son los límites.
---

El módulo II es la vista de **operación en vivo** del estate: qué está haciendo
ahora mismo cada sesión de agente, sus totales de tokens y coste en vivo, un estado
de Claude Code derivado y una línea temporal reconstruible. Mientras que el módulo
I (inventario) materializa el estate durable, el módulo II mantiene una **capa
operativa en vivo** por sesión sobre el mismo flujo de observaciones — y muestra
solo lo que ese flujo lleva honestamente.

Olivares <!-- release -->0.8<!-- /release --> también **lanza** CLI oficiales de proveedor como hijos propios bajo un
[perfil de proveedor](/how-to/operate-provider-sessions/). Esa vía gestionada
es el mismo módulo. No sustituye la capa ni fusiona dos homes que anuncian el
mismo id de sesión del proveedor (`CHANGELOG.md` `[26.9.0]` B1/B2).

## Qué es

El módulo II es un módulo de la capa Core dirigido por el bus, hermano del
inventario. Mantiene un registro en vivo indexado por la referencia externa de cada
sesión, construido a partir del flujo cooperativo de observaciones — nunca
sondeado, nunca fabricado. Por sesión rastrea:

- la **acción actual** (la última herramienta usada) y el recurso/modo que tocó;
- los **totales de tokens y coste en vivo**, leídos de las muestras de coste (el
  ledger de coste canónico y FinOps son el módulo XI, no aquí — esto es solo la
  cifra en vivo);
- un **estado de Claude Code derivado** (`cc_state`); y
- una **línea temporal** a la que cada evento observado se añade en orden de
  ingesta.

## Su contrato y entidades

El módulo registra dos entidades acotadas al tenant. `sessions.live` contiene el
registro en vivo por sesión — acción/recurso/modo actual, referencia de modelo,
tokens de entrada/salida en vivo, coste en vivo, conteos de eventos y de llamadas a
herramientas, y marcas temporales de primer/último evento. `sessions.timeline`
contiene una fila reproducible por evento, ordenada por ingesta. **No hay columna
de ciclo de vida almacenada**: el flujo cooperativo no lleva ninguna señal de fin o
fallo, de modo que la única señal de vitalidad honesta es el `cc_state` derivado.

`cc_state` se deriva **en tiempo de lectura** a partir de la recencia de los eventos
— `active` / `idle` / `ended` — y cambia a un estado de evasión-silenciosa cuando el
conector eleva ese hallazgo (nunca lo escribe el propio módulo). Las lecturas se
sirven bajo rutas del módulo (lista en vivo, sesión única, línea temporal por
sesión) más un flujo SSE en vivo; cada lectura requiere el permiso de lectura de
sesión, y **abrir el flujo se audita automáticamente**. El canal SSE está
estrictamente **aislado por tenant** (un cliente recibe solo instantáneas de su
tenant autorizado) y es de **mejor esfuerzo** (un cliente lento descarta el frame
intermedio y recibe el siguiente — la ingesta nunca se bloquea).

## Provider profiles and `live_ref`

Un **perfil de proveedor** es la identidad durable de una instancia de
proveedor configurada en un entorno de ejecución: controlador, entorno y el
`config_home` / `user_home` canónicos. No es una cuenta autenticada del
proveedor. Registrar, renombrar, desactivar/activar y retirar viven bajo
`/v1/m/sessions/provider-profiles`. Las rutas aparecen solo en la lectura
admin `configuration`. Un lanzamiento nombra `provider_profile_ref`; el
servidor resuelve los homes y persiste una instantánea no secreta en el run
antes del spawn (`CHANGELOG.md` `[26.9.0]` B1;
`web/src/features/agentops/types.ts`).

Una observación se pliega en la fila en vivo de su **canal**, calculado por el
servidor a partir del registro de fuente sellado por el host
(`CHANGELOG.md` `[26.9.0]` B2):

| Channel | Meaning |
|---|---|
| `legacy` | no registration |
| `observed` | a source dedicated to a profile by a binding approved at host admission for the exact applied revision |
| `source` | a known registration with no verifiable profile |
| `managed` | a run the plane launched; the only row carrying `canonical_sid` and `run_ref` |

Cada fila en vivo expone `live_ref` y `attribution`. Dos homes que anuncian el
mismo id de sesión del proveedor son dos filas con dos cronologías. Lee una
fila con `GET /v1/m/sessions/live/by-id/{live_ref}` (y su consulta de
cronología / stream / runs). Las rutas de id externo desnudo permanecen y son
**legacy**: responden solo para la fila legacy.

Los controladores se registran en cada nodo al arrancar. Al iniciar una sesión,
el motor usa la instalación gestionada verificada más reciente y después la CLI
en su `PATH`. `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`, `_CODEX_BIN`, `_GROK_BIN` y
`_OPENCODE_BIN` sustituyen explícitamente esa resolución
(véase [Configuración](/reference/configuration/)). Si no encuentra un ejecutable,
rechaza el lanzamiento. Las demás comprobaciones de preparación, incluida la
autenticación del perfil y la política de lanzamiento, siguen vigentes. Pasos del operador:
[Operar una sesión de proveedor](/how-to/operate-provider-sessions/).

Un mensaje puede llevar imágenes. `olivares session send <session> --attach shot.png "what
is wrong here?"` y el compositor de la consola (pegar, soltar o el clip) almacenan cada
PNG, JPEG, WebP o GIF con la sesión, hasta cuatro por mensaje y 3.75 MB cada una, y
luego las envían junto con el texto; un mensaje necesita su texto, así que una imagen
sola no se envía (la CLI y el compositor lo piden). Claude Code recibe un bloque de
contenido de imagen y Codex su propia entrada de imagen; una herramienta sin entrada de
imagen rechaza el mensaje por su nombre antes de enviar nada. La API es
`POST /v1/m/sessions/runs/{ref}/attachments` (la imagen, en base64),
`GET .../attachments/{id}` y `attachments` en `.../input`. Las imágenes viven bajo el
directorio de datos, por tenant y sesión, y se eliminan junto con la sesión.

## Carpetas de sesión en recursos compartidos NFS o SMB

Una carpeta de sesión es una ruta en el host del motor, así que un recurso compartido de red es una carpeta una vez que el host lo ha montado. `olivares netfolder add` lo hace con las unidades de montaje de systemd y registra el punto de montaje como carpeta; el montaje sigue siendo de systemd, y las unidades son ficheros normales que puedes leer y editar. Ejecútalo como root en el host del motor:

```sh
printf '%s\n' "$SMB_PASSWORD" | sudo olivares netfolder add files //fileserver/projects --user svc --password-stdin
sudo olivares netfolder add data fileserver:/export --mode write_no_delete
olivares netfolder ls
```

El origen es `//server/share` para SMB o `server:/export` para NFS, tal como lo escribe `mount(8)`. El nombre tiene letras minúsculas y dígitos, y da nombre al punto de montaje `/srv/olivares/mnt/<name>`. `--mode` es el modo de acceso de la carpeta (`rw` por defecto). Las unidades nombran la cuenta `olivares` como propietaria de cada fichero que muestra una carpeta SMB, así que el motor debe ejecutarse con esa cuenta. `ls` muestra cada carpeta como `mounted`, `unmounted` (se monta en el primer uso, y `ls` no lo activa) o `not responding`. Una carpeta que ya existe nunca se sobrescribe.

Para el ejemplo SMB, el comando escribe estas unidades, medidas en Debian 13 (Linux 6.12) contra una exportación NFSv4.2 y un recurso Samba 4.22 (SMB 3.1.1). Los nombres de fichero son los que imprime `systemd-escape -p /srv/olivares/mnt/files` con `.mount` y `.automount` añadidos.

```ini
# /etc/systemd/system/srv-olivares-mnt-files.mount
[Unit]
Description=Olivares network folder files
After=network-online.target
Wants=network-online.target

[Mount]
What=//fileserver/projects
Where=/srv/olivares/mnt/files
Type=cifs
Options=credentials=/etc/olivares/netfolders/files.cred,uid=olivares,gid=olivares,vers=3.1.1,nodev,nosuid,soft,echo_interval=5
TimeoutSec=30
```

```ini
# /etc/systemd/system/srv-olivares-mnt-files.automount
[Unit]
Description=Olivares network folder files (mounted on first use)

[Automount]
Where=/srv/olivares/mnt/files
TimeoutIdleSec=0

[Install]
WantedBy=remote-fs.target
```

Para NFS escribe `Type=nfs4`, `What=fileserver:/export` y `Options=nodev,nosuid,vers=4.2,soft,timeo=50,retrans=2`, y no crea fichero de credenciales. En un host que aplica SELinux con la política del appliance añade `context=system_u:object_r:olivares_workspace_t:s0` a las opciones, la etiqueta que ya usan el motor y sus herramientas de sesión para una carpeta. Para eliminar una carpeta, ejecuta `systemctl disable --now` en sus dos unidades, bórralas junto con su fichero de credenciales y desregistra la carpeta.

- **Todos los que tienen la carpeta actúan como una sola cuenta.** Los ficheros se leen y escriben como la cuenta que presenta el montaje: `uid=` y `gid=` para SMB, el mapeo de usuarios de la exportación para NFS. El recurso compartido no distingue a las personas. Olivares decide quién recibe la carpeta.
- **La carpeta propia de una persona lee como esa persona.** `--owner <id de usuario>` registra una carpeta SMB como la propia de una persona, igual que una carpeta personal: su contenido es solo suyo y los administradores siguen encontrando la carpeta para gestionarla. Indique con `--user` y la contraseña el inicio de sesión SMB propio de esa persona y el servidor de ficheros aplica sus permisos: dos personas que montan el mismo recurso compartido ven cada una lo que su propia cuenta puede abrir. Añada una carpeta por persona, cada una con su nombre. NFS no distingue a las personas y no admite propietario. El administrador escribe la contraseña de la persona una sola vez y queda en el fichero de credenciales legible solo por root, como la de la cuenta compartida; la sesión de la persona nunca la tiene. Un ticket Kerberos por sesión, para que la persona no tenga que entregar su contraseña a nadie, no está construido. Comprobado con dos cuentas de un dominio Samba AD sobre un mismo recurso: la carpeta de cada persona listó su propio directorio y el común, leyó su propio fichero, y el servidor de ficheros rechazó el fichero de la otra persona.
- **La credencial queda en un fichero.** El comando escribe `username=` y `password=` en el fichero `credentials=`, propiedad de root con modo 0600, a partir de la contraseña de la entrada estándar. La contraseña no aparece en ninguna unidad, opción de montaje, argumento de proceso ni línea del journal: `mount.cifs` recibe solo la ruta. NFS con `sec=sys` no necesita credencial.
- **La unidad automount mantiene el arranque independiente del servidor de ficheros.** Nada espera al recurso compartido durante el arranque. El primer uso lo monta, y un servidor caído en ese momento hace fallar ese uso, no el arranque.
- **Usa `soft`.** Un montaje NFS `hard`, el valor por defecto, deja bloqueado un proceso que accede a un servidor caído, sin posibilidad de detenerlo. Con las opciones anteriores, una lectura o escritura en un recurso montado falla con un error de entrada/salida o "Host is down" tras 20 a 30 segundos, y el recurso vuelve a funcionar cuando el servidor responde.
- **Una carpeta que no responde se informa.** Registrar la carpeta, abrirla, listarla e iniciar una sesión en ella esperan 10 segundos al recurso compartido cada una, y luego se rechazan con el mensaje "The folder ... is not responding. If it is on a network share, check that its file server is up and reachable." y el estado 424. La sesión no arranca.
- **`write_no_delete` se mantiene.** Landlock lo aplica en la capa de sistema de ficheros del kernel, por encima del sistema de ficheros de red. En lanzamientos reales de sesión sobre ambos recursos, una sesión creó y cambió ficheros y no pudo renombrar, borrar ni eliminar directorios, igual que en un disco local, mientras que `rw` lo permitió. Un host que no puede confinar sigue rechazando el lanzamiento.
- **La carpeta es el límite.** En esos mismos lanzamientos, en ambos modos, una escritura en el padre de la carpeta, que está en el mismo recurso, fue rechazada y no dejó ningún fichero. La sesión escribe solo dentro de su carpeta.

## Qué consume (y qué deriva)

El módulo II consume el mismo flujo de observaciones de datos mínimos que el
inventario — [`edge.observed`](/es/reference/events/), `cost.sampled` y
`finding.reported`. Solo los edges cuyo origen es una **sesión** producen operación
en vivo; las muestras de coste ligadas a una sesión suman a la cifra de
tokens/coste en vivo (aquí no se escribe ningún `CostRecord`); los hallazgos cuyo
sujeto es una sesión se anotan, y un hallazgo anti-evasión marca el estado de
evasión. Dos campos se **derivan en vivo** a partir de esas mismas señales:
`agent_ref` del agente atribuido a una sesión, y `summary` de un hallazgo
(forense) de compactación de contexto cuyo título es seguro como resumen por
contrato — nunca un resumen fabricado por un LLM.

:::caution[Límites honestos]

- **`goal` se mantiene vacío — honestamente.** El flujo cooperativo es de datos
  mínimos y **no** lleva el objetivo ni la lista de tareas de una sesión; se
  expurgan en el conector y no hay texto de prompt en proceso sobre el cable. El
  registro en vivo modela el campo para que el contrato y la UI estén listos y
  cualquier futuro canal de metadatos pueda poblarlo, pero el módulo **nunca lo
  inventa**.
- **Sin ciclo de vida almacenado.** El flujo no tiene señal de fin/fallo, así que
  la vitalidad de una sesión es el `cc_state` **derivado** por recencia — no un
  estado persistido. Un estado `ended` significa *no hay eventos recientes*, no un
  apagado limpio confirmado.
- **La cifra en vivo no es el ledger.** Los tokens/coste en vivo son una lectura
  operativa de las muestras de coste; el registro de coste autoritativo y
  conciliable es el ledger FinOps del módulo XI. No trates la cifra en vivo como
  verdad de facturación.
- **Los datos mínimos son una propiedad del cable.** Solo se llevan y persisten
  referencias, clasificaciones y contadores de vitalidad/coste — nunca payloads,
  prompts, comandos o PII.
:::

## Relacionado

- [Referencia del bus de eventos](/es/reference/events/) — los eventos
  `edge.observed`, `cost.sampled` y `finding.reported` que consume este módulo.
- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo II
  y la división honesta de actuación.
- [Mapa de acceso y recursos](/es/reference/modules/iii-access-map/) — el módulo Core
  hermano que es dueño del grafo de acceso R/RW.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — el motor y las capas.
- [Conectar Claude Code](/es/how-to/connect-claude-code/) — empieza a producir el flujo en vivo.
- [Honestidad y límites](/es/start/honesty-and-limits/) — lo que el producto hace y no hace hoy.
