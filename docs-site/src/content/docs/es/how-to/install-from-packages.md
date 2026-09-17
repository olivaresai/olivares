---
title: Instalar desde un paquete
description: >-
  Instala Olivares AI desde el .deb, .rpm o .apk en un host Linux endurecido: verifica
  la release antes de confiar en ella, ejecútala bajo la unidad systemd empaquetada o,
  en Alpine por defecto, en primer plano, conecta tu primera fuente y actualiza — en línea,
  con versión fijada o totalmente aislado de red.
draft: false
---

:::note[Nombres de paquete publicados]
La release v26.9.0 de GitHub publica artefactos `.deb`, `.rpm` y `.apk` para `amd64` y
`arm64`, con `checksums.txt`, `checksums.txt.sig` y `checksums.txt.pem`. Los comandos
de abajo usan los nombres literales `amd64` de esa release; sustituye `amd64` por
`arm64` en un host ARM de 64 bits. Instala desde esos artefactos de release verificados.
Los productores de metadatos de repositorio en un árbol de fuentes no son instrucciones
de instalación para esta guía.

**Cualificación DIST-24-05.** Lo que CI cualifica es el **instalador de shell** verificado
y su contrato de servicio/doctor, no `dpkg`, `rpm` ni `apk`: una matriz de dispatch/pull
request lo ejecuta contra la release publicada en userlands de contenedor de
Debian stable, Ubuntu 24.04 LTS, Fedora, openSUSE Leap y Alpine y en un runner alojado de
macOS 14, y falla como no medible cuando la release pública no es alcanzable, en vez de
contar un dry-run como cobertura. Esa matriz no certifica la instalación nativa por gestor
de paquetes. El ciclo de vida nativo de los paquetes construidos desde este árbol fuente —
instalación, arranque, reinicio, actualización de un servicio OpenRC en marcha y retirada —
se ha ejercido localmente en un invitado Alpine desechable; eso es evidencia para este
árbol, no una cualificación firmada, alojada ni de preproducción, que sigue pendiente.

**Repositorios propuestos DIST-24-06 (no es una superficie de instalación viva).** El
árbol fuente contiene productores deterministas de repositorios apt, rpm-md y APK, un
verificador de índices firmados, una cualificación de cliente limpio y un flujo de
publicación por etapas cuyo dispatch permanece inerte hasta que un revisor lo aprueba.
**Ninguna URL de repositorio de paquetes está viva**, no hay ningún nombre DNS delegado ni
ninguna clave de firma de repositorio de producción aprovisionada. Nada de esta propuesta
es una fuente para el gestor de paquetes; sigue usando los artefactos de release
verificados de abajo.
:::

Esta es la vía para un host Linux normal en el que quieres el motor como servicio, no
en un contenedor. Debian/Ubuntu y RHEL/Fedora/SUSE usan **systemd** por defecto. El init
por defecto de Alpine es **OpenRC**, no systemd. Para contenedores, véase
[Despliega con Docker](/es/how-to/docker-deployment/); para un host sin ruta de salida,
[Instala en un entorno aislado de red](/es/how-to/air-gap-install/), a la que esta
página vuelve en el paso de actualización.

## 1. Verifica la release antes de confiar en ella

En un producto de seguridad la cadena de construcción forma parte del modelo de
confianza, así que nada aquí te pide que te fíes de la descarga. Pon el paquete,
`checksums.txt` y la firma en un mismo directorio y ejecuta el verificador **desde ese
directorio**:

```bash
# keyless / Sigstore (default; reaches Rekor over the network)
./verify-release.sh
```

Las releases se firman sin clave y no publican ninguna clave pública de cosign, así que usa el
comando sin clave para los paquetes descargados de una release. Necesita el material de raíz de
confianza de Sigstore, que cosign descarga salvo que ya esté en caché. `--offline` quita solo la
consulta a Rekor; no hace que la verificación funcione sin red. `--key` solo sirve para archivos
firmados con una clave privada que controlas, y esa clave pública debe obtenerse por separado de
los archivos que verifica.

Qué comprueba cada paso, cómo se comporta ante una release parcial y cómo verificar la
imagen de contenedor está en
[Verifica lo que has descargado](/es/how-to/verify-a-release/).

## 2. Instala el paquete

Los tres formatos llevan el binario en `/usr/bin/olivares`, un fichero de entorno en
`/etc/olivares/olivares.env` (`config|noreplace`), el directorio de datos
`/var/lib/olivares` y los textos de licencia bajo `/usr/share/doc/olivares/`.
`.deb` y `.rpm` entregan la unidad **systemd** endurecida. **Los paquetes de este
árbol fuente** ponen en el `.apk` una unidad **OpenRC** ejecutable en
`/etc/init.d/olivares`, con un sello `package-init` explícito para que los ganchos no
adivinen por la presencia de `systemctl` en el host.

El `.apk` **publicado anteriormente** entregaba esa misma unidad systemd y **no** una unidad
OpenRC. Aquel tarball linux publicado lleva los textos de licencia, el README y
`SECURITY.md`; no incluye `scripts/install-service.sh` ni `packaging/service/`. Esos
ficheros adaptadores están en el archivo firmado del árbol para la siguiente release.

```bash
# Debian / Ubuntu
sudo dpkg -i olivares_26.9.0_linux_amd64.deb

# RHEL / Fedora / SUSE
sudo rpm -Uvh olivares_26.9.0_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted olivares_26.9.0_linux_amd64.apk
```

La instalación **crea el usuario y el grupo de sistema `olivares`** (con
`/usr/sbin/nologin` como shell y `/var/lib/olivares` como directorio de inicio), crea
`/var/lib/olivares` con modo `0750` propiedad de ese usuario y crea `/etc/olivares`. Los
paquetes systemd recargan systemd cuando `systemctl` está presente; los paquetes OpenRC no
habilitan ni arrancan el servicio. **No** arranca nada — véase
[lo que el paquete no hace](#8-lo-que-el-paquete-no-hace).

### Arrancar el motor

En Debian/Ubuntu y RHEL/Fedora/SUSE:

```bash
sudo systemctl enable --now olivares
```

En **OpenRC** (`.apk` de este árbol fuente):

```bash
sudo rc-service olivares start
# opcional; el paquete no lo hace:
sudo rc-update add olivares default
```

El token de primer arranque está en `/var/log/olivares.log` (y en `logread` si syslogd
está en marcha). Los flags adicionales de `OLIVARES_EXTRA_ARGS` se añaden con el globbing
desactivado y separados por espacios; el entrecomillado anidado no se interpreta, y el
fichero de entorno no se evalúa como shell.

En el **Alpine publicado anteriormente**, `systemctl` no está y ese `.apk` no tiene unidad
OpenRC. Arranca el motor como el usuario de servicio; el token de primer arranque se
imprime en stdout:

```bash
sudo -u olivares olivares serve --data-dir=/var/lib/olivares \
  --listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444 --checkpoint-interval=1h
```

## 3. La unidad systemd endurecida

La unidad systemd empaquetada ejecuta el motor como el usuario no privilegiado
`olivares` con un conjunto de capacidades vacío — no retiene ninguna, ni ambiente ni de
acotación — y `NoNewPrivileges=true`, de modo que nada de lo que lance puede ganar
ninguna. Encima lleva `ProtectSystem=strict` (el sistema de ficheros es de solo lectura
salvo `ReadWritePaths=/var/lib/olivares`), `ProtectHome`, `PrivateTmp`,
`PrivateDevices`, las cuatro directivas `ProtectKernel*`/`ProtectClock`,
`RestrictNamespaces`, `RestrictSUIDSGID`, `RestrictRealtime`, `LockPersonality`,
`MemoryDenyWriteExecute`, `SystemCallArchitectures=native`, un filtro de syscalls
`@system-service` que además descarta `@privileged` y `@resources`, y `UMask=0027`.

En Alpine por defecto esas directivas systemd no aplican a un `.apk` **publicado
anteriormente**, porque la unidad systemd de ese payload no está en ejecución. Los paquetes
`.apk` construidos desde este árbol fuente corren bajo OpenRC en su lugar: usan la cuenta
`olivares`, escriben el token de primer arranque en `/var/log/olivares.log` y no
implementan las directivas de sandbox de systemd.

**Los listeners son solo loopback por defecto** — `--listen=127.0.0.1:8443` para HTTP
(REST más la consola embebida) y `--grpc-listen=127.0.0.1:8444` para gRPC. Ábrelos de
forma deliberada, mediante `OLIVARES_EXTRA_ARGS` en `/etc/olivares/olivares.env`, y
pon delante tu propia terminación TLS. Para loopback IPv6 usa `--listen=[::1]:8443`.

### Montaje de scratch ejecutable

Este es el fallo que conviene conocer de antemano en un host systemd, porque el síntoma
no nombra la causa.

Los conectores de primera parte fuera de proceso viajan **embebidos en el binario**. En
el arranque el motor extrae los que necesita a un scratch privado y los ejecuta como
subprocesos. Cuando `TMPDIR` no está definido, el scratch se crea bajo
`<data-dir>/tmp`; solo un directorio de datos no escribible hace que el motor caiga al
directorio temporal del sistema. Un `TMPDIR` explícito siempre gana.

El servicio systemd empaquetado usa por tanto `/var/lib/olivares/tmp`, no `/tmp`, por
defecto. Comprueba el montaje que realmente sostendrá el scratch ejecutable:

```bash
check_scratch_mount() {
  target=${1:-/var/lib/olivares}
  opts=$(findmnt -no OPTIONS --target "$target") || {
    printf '%s\n' "cannot read mount options for $target (missing path or permission)" >&2
    return 1
  }
  [ -n "$opts" ] || {
    printf '%s\n' "mount options for $target are unknown" >&2
    return 1
  }
  case ",$opts," in
    *,noexec,*) printf '%s\n' 'noexec — set TMPDIR' ;;
    *) printf '%s\n' 'exec-capable — nothing to do' ;;
  esac
}
check_scratch_mount /var/lib/olivares
```

Si dice `noexec`, apunta `TMPDIR` a un directorio escribible bajo
`ProtectSystem=strict` **y** que esté en un montaje capaz de ejecutar:

```bash
sudo install -d -o olivares -g olivares -m 0750 /run/olivares-exec-tmp
sudo systemctl edit olivares      # creates a drop-in; do not edit the shipped unit
```

```ini
[Service]
Environment=TMPDIR=/run/olivares-exec-tmp
ReadWritePaths=/run/olivares-exec-tmp
```

Luego reinicia. Si un exec se rechaza con `EACCES` o `ENOEXEC`, el error del motor
nombra el montaje de extracción y los dos controles de reubicación (`TMPDIR` y
data-dir); no informa un montaje `noexec` como un conector ausente.

Usa `systemctl edit`, nunca una edición directa de
`/usr/lib/systemd/system/olivares.service`: ese fichero pertenece al paquete y una
actualización lo sustituye.

## 4. Primer arranque: `olivares quickstart`

`quickstart` es `serve` con valores por defecto amistosos y un banner guiado. Nunca
inventa credenciales por defecto; te señala la consola embebida para crear el primer
administrador con un **token de un solo uso**.

Si ya estás sirviendo bajo la unidad systemd empaquetada, no ejecutes `quickstart` —
**el token de configuración de primer arranque se imprime en el journal**:

```bash
journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
```

Si arrancaste `serve` en primer plano (Alpine por defecto), el mismo banner está en stdout.

Abre la consola en `https://127.0.0.1:8443` (en el primer arranque se genera un
certificado autofirmado), presenta ese token y crea el administrador. El token es de un
solo uso.

En una estación de trabajo, para mirar sin instalar un servicio, `olivares quickstart`
hace lo mismo en primer plano con `--listen`/`--grpc-listen`/`--data-dir` si necesitas
moverlo de los valores por defecto.

## 5. Tu primera fuente: pgAudit

Una fuente es de donde el motor ingiere observaciones. Los verbos se parten por lo que
cuesta cada uno, y conviene usarlos en ese orden: `plan` dice qué cambiaría y no
escribe nada, `validate` dice que la configuración es coherente por sí misma **sin
tocar la red**, `test` abre la fuente de verdad para demostrar que responde, y `set`
aplica. La configuración lleva **referencias** a secretos (`store:<name>`), nunca
valores.

pgAudit lee el registro de auditoría de PostgreSQL, así que `log_path` es el único
campo obligatorio:

```bash
# coherent by itself? (writes nothing, opens no socket)
sudo -u olivares olivares sources validate --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# does it actually answer? (opens the source for real)
sudo -u olivares olivares sources test --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# apply it
sudo -u olivares olivares sources set --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --actor "$(id -un)" --reason "onboard the production audit log" \
  --data-dir /var/lib/olivares
```

Tres cosas de la línea de comandos anterior que no son relleno:

- **`--tenant` es obligatorio.** Una fuente debe nombrar el tenant de negocio al que
  pertenecen sus observaciones; sin él el comando se niega en vez de adivinar un dueño
  para tus datos de auditoría.
- **`--actor` y `--reason` los exige `set`, y solo `set`.** Una operación privilegiada
  fuera de línea tiene que registrar quién la hizo y por qué. `validate` no necesita
  ninguno, porque no escribe nada — la asimetría es el punto.
- **`format` vale por defecto `csvlog` y `follow` vale `true`**, así que un despliegue
  estándar de pgAudit no necesita ninguno de los dos.

Al aplicar se imprime lo que cambió, campo a campo, y se te dice cómo hacer que un motor
**en ejecución** lo recoja sin reiniciar — `POST /v1/console/runtime/reload`, o un
`SIGHUP`. Bajo la unidad systemd empaquetada eso es `sudo systemctl reload olivares`.
Si arrancaste `serve` en primer plano, envía `SIGHUP` a ese proceso.

El usuario de servicio necesita lectura sobre ese fichero de registro; en la mayoría de
distribuciones eso significa añadir `olivares` al grupo `adm` o `postgres` — una
concesión deliberada tuya, no algo que el paquete haga por ti.

## 6. Actualizar

`olivares upgrade` sustituye el binario en el sitio, y las propiedades de seguridad son
la razón para preferirlo a reinstalar el paquete a mano: **nunca sustituye el binario
hasta que el candidato descargado se ha sondeado con éxito mediante exec**, conserva
una copia de seguridad con marca de tiempo y **vuelve a esa copia si el sondeo
posterior al intercambio falla**.

```bash
sudo olivares upgrade --check    # what would change, without changing anything
sudo olivares upgrade --yes      # do it
```

Tres flags importan de forma específica en una instalación empaquetada:

- **`--endpoint`** — toma actualizaciones de un repositorio de GitHub que controlas en
  vez del predeterminado. Es la vía de escape para un espejo o una bifurcación.
- **`--bundle`** — instala desde un directorio de bundle local o un `.tar.gz` **sin
  red en absoluto**. Construir ese bundle y moverlo está en
  [Instala en un entorno aislado de red](/es/how-to/air-gap-install/).
- **`--install-timer`** — emite un temporizador y un servicio **systemd opcionales**
  que comprueban actualizaciones según una programación. Nada lo instala por ti; véase
  [lo que el paquete no hace](#8-lo-que-el-paquete-no-hace). Es un generador systemd.

Ten en cuenta que la puesta en escena y el sondeo exec ocurren **en el directorio de
instalación, junto al destino — no en `/tmp`**, así que el montaje `noexec` comentado
[arriba](#montaje-de-scratch-ejecutable) no rompe una actualización. Un montaje
`noexec` en el directorio de **instalación** es otro asunto y deja la versión
instalada como no medible; ese caso, los canales de release, el despliegue por etapas
y la reversión están en
[Actualizar y revertir](/es/how-to/upgrade-and-rollback/).

## 7. Desinstalar o migrar sin adivinar rutas

El paquete escribe `/var/lib/olivares/install-manifest.json`. El desinstalador valida
el manifiesto completo contra el índice de distribución firmado antes de tocar el
servicio o el sistema de ficheros; una ruta inesperada devuelve 2. Inspecciona primero
y elige la retención de forma explícita:

```bash
sudo olivares uninstall --plan --data-dir /var/lib/olivares
sudo olivares uninstall --preserve --data-dir /var/lib/olivares
sudo olivares uninstall --purge --data-dir /var/lib/olivares --yes
```

Preserve es la política de retirada del paquete: retiene configuración, datos,
registros, claves y su identidad de servicio. En los paquetes systemd el gancho de
retirada ejecuta `--preserve`. En los paquetes `.apk` OpenRC construidos desde este árbol
fuente, el gancho detiene el servicio si está activo, retira la entrada del runlevel por
defecto sin fallar si nunca se habilitó, valida el manifiesto completo con `--plan` y deja
que apk elimine los ficheros propiedad del paquete. Los paquetes Alpine publicados
anteriormente solo validaban con `--plan`, porque ese payload no tenía unidad OpenRC que detener.
Ejecuta `--purge` antes de quitar el paquete solo cuando la intención es el borrado.
Exige confirmación y borra únicamente rutas indexadas.

Para un traslado de finca, crea primero un `olivares dr backup`, instala el destino y
ejecuta allí `olivares dr restore`. Los bundles actuales usan `hmac-sha256-kek-v1` para
autenticar el manifiesto y cada carga bajo tu KEK. Una exportación de un motor más
nuevo se rechaza antes de escribir; un bundle pre-v26.9 autenticado por separado
necesita `--allow-legacy-unsigned` de forma explícita. La
[guía de copia de seguridad y restauración](/es/how-to/backup-and-restore/) cubre la
custodia del KEK y la prueba de continuidad posterior a la importación.

## 8. Lo que el paquete no hace

Dicho con claridad, porque un producto de seguridad vago aquí no merece la
instalación:

- **No añade un repositorio.** No se escribe nada en `/etc/apt/sources.list.d`,
  `/etc/yum.repos.d` ni `/etc/apk/repositories`. Instalaste un fichero; solo ese
  fichero se instaló. Las actualizaciones las disparas tú — con un paquete nuevo, o
  con `olivares upgrade`.
- **No arranca ni habilita el servicio.** Los paquetes systemd recargan systemd cuando
  `systemctl` está presente e imprimen `systemctl enable --now`. Los paquetes OpenRC
  imprimen `rc-service olivares start` y no ejecutan `rc-update add`. Arrancar sigue
  siendo decisión tuya. Una actualización de un servicio OpenRC que ya está en marcha lo
  detiene, sustituye los ficheros y lo arranca de nuevo; un servicio inactivo sigue
  inactivo.
- **Verificar una licencia nunca llama a nadie. Descargar lo que pagaste, sí.** La
  validación de licencia en la compilación abierta es Ed25519 fuera de línea, no hay
  interruptor remoto de apagado, y ninguna clave de licencia restringe ni degrada esa
  compilación — la compilación AGPL es la plataforma entera.
  **No hay telemetría obligatoria ni egreso del plano de control por defecto: lo que
  cruza tu perímetro es lo que configuras para que lo cruce** — llamadas a tus API de
  modelos, las salidas SIEM/webhook que cableas, un proveedor externo de embeddings si
  aprovisionas uno, y cualquier fuente que un conector consulte (su `addr`,
  `base_url` o `endpoint`) en el intervalo que fijes.
- **Pero `olivares upgrade` sí hace una llamada de red, de forma deliberada, cuando lo
  ejecutas** — ese es el punto de una comprobación de actualización, y `--check` te
  muestra el plan antes de que se mueva nada. La forma honesta de la promesa es:
  *verificar una licencia nunca llama a nadie; descargar lo que pagaste, sí.* Usa
  `--bundle` si quieres que la vía de actualización tampoco haga ninguna llamada.
- **No abre un puerto a la red.** La unidad enlaza solo loopback hasta que tú la
  amplías.

## Véase también

- [Verifica lo que has descargado](/es/how-to/verify-a-release/) — la vía completa de
  verificación
- [Actualizar y revertir](/es/how-to/upgrade-and-rollback/) — canales, despliegue por
  etapas, reversión
- [Endurece un despliegue](/es/how-to/security-hardening/) — más allá de lo que la
  unidad ya hace
- [Instala en un entorno aislado de red](/es/how-to/air-gap-install/)
- [Despliega con Docker](/es/how-to/docker-deployment/)
- [Conecta una fuente](/es/how-to/connect-a-source/)
- [Copia de seguridad y restauración](/es/how-to/backup-and-restore/)
