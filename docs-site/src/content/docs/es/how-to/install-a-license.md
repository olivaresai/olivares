---
title: Instalar una licencia y pasar a Business
description: >-
  Dónde se guarda una licencia adquirida, cómo instalarla sin reiniciar el motor,
  cómo comprobar qué hay instalado y el cambio in-place de Community → Business.
  La verificación Ed25519 es offline: ninguna llamada de red establece el derecho.
---

Has comprado un plan y has recibido una licencia. Esta página explica qué hacer con ella:
dónde guardar el fichero, cómo aplicarla a un motor en ejecución, cómo leer qué hay instalado
y, si has comprado un plan Business, cómo cambiar el binario de Community por el
comercial sin reinstalar nada.

:::note[La licencia, no el binario, activa las funciones comerciales]
**El build Business ejecuta una función comercial solo mientras haya instalada una licencia
válida cuyos derechos la incluyan.** Comprueba la licencia offline, en la máquina, en cada uso.
Sin licencia, o con una caducada o revocada, el trabajo nuevo de esa función responde
`addon_requires_license`; leer, verificar y exportar tus datos sigue funcionando, y ninguna
licencia limita las cuentas de usuario: los usuarios autoalojados son ilimitados en todos los
niveles. El binario Community nunca lee una licencia para activar o desactivar nada.

**Una licencia vigente permite acceder a los artefactos comerciales.** La instalación offline (`olivares upgrade --bundle`) requiere el binario Enterprise. Enterprise instala un bundle Community firmado sin licencia; un bundle comercial necesita una licencia vigente, verificada offline. Community y Business admiten `--bundle --check` sin leer una licencia ni instalar el bundle.
:::

## Qué has recibido

| Has comprado | Qué recibes | Qué haces con ello |
|---|---|---|
| Community | nada que instalar | ya está en ejecución; nada de esta página se aplica |
| Business / Enterprise, autoalojado | un **fichero de licencia** y un **token de descarga** | instala la licencia y cambia al binario comercial |
| Cloud | credenciales para un tenant alojado | nada que instalar en un host tuyo |

La licencia es un único blob firmado. Guárdalo como fichero —`customer.license`, o con
cualquier otro nombre— y conserva el token de descarga del mismo correo: se usan en pasos
distintos y solo se instala la licencia.

## 1 · Instalar la licencia

```sh
olivares license install ./customer.license --data-dir /var/lib/olivares
```

El comando **verifica el blob antes de escribir nada** con la clave pública Ed25519 integrada
en tu compilación, de modo que un copia y pega truncado falla aquí y no en el siguiente
arranque. Si todo va bien, escribe `<data-dir>/license.key` con modo `0600`: la licencia
canónica en reposo que el motor lee de forma predeterminada.

Pasa `-` en lugar de una ruta para leer el blob desde la entrada estándar:

```sh
pbpaste | olivares license install - --data-dir /var/lib/olivares
```

Instalar sobre una licencia existente la **sustituye** de forma atómica e indica cuál ha
sustituido.

### Aplicarla a un motor en ejecución, sin reiniciar

Un motor en ejecución recoge la nueva licencia in-place. Cualquiera de estas opciones lo
hace:

```sh
kill -HUP "$(pidof olivares)"                 # signal the running process
curl -X POST .../v1/console/runtime/reload    # the API half
```

…o el propio control de recarga de la consola. Reiniciar también funciona; simplemente no es
necesario.

### Dónde busca el motor, en orden

Si ya inyectas la licencia de otra forma, ten en cuenta que el fichero del directorio de datos
es la fuente de **menor prioridad** de las cuatro. El motor las resuelve en este orden, de
mayor a menor prioridad:

1. `--license <path>` (o `LicenseFile` en el fichero de configuración)
2. `OLIVARES_LICENSE_PATH=<path>`
3. `OLIVARES_LICENSE=<blob>` — la licencia incluida directamente en el entorno
4. `<data-dir>/license.key` — lo que escribe `license install`

`license install` **se niega** cuando puede ver que un override tiene prioridad sobre el fichero
que va a escribir: instalar por debajo de uno dejaría un fichero que el motor nunca lee, y
verías una salida 0 sin ningún cambio. Indica qué override ha encontrado, y `--force` deja
preparado el fichero de todos modos; el caso legítimo es un override que estás a punto de
retirar.

:::caution[Qué puede y qué no puede ver esa negativa]
Lee `OLIVARES_LICENSE_PATH` y `OLIVARES_LICENSE` **del entorno de su propio proceso**. No puede ver
un flag `--license` (ni una entrada `LicenseFile` de la configuración) proporcionado a un motor que ya
se ejecuta como un proceso independiente: `install` y `uninstall` ni siquiera admiten un flag
`--license`. Por tanto, en un host donde el servicio se haya iniciado con una ruta explícita,
ambos comandos pueden salir con éxito sin cambiar nada de lo que lee el motor.

Ejecuta `olivares license status` después de cualquiera de los dos. Resuelve la licencia con la
misma precedencia que usa el motor e indica qué fuente está realmente en vigor, que es la
pregunta importante.
:::

## 2 · Comprobar qué hay instalado

```sh
olivares license status --data-dir /var/lib/olivares
```

`status` funciona offline y resuelve la licencia con la misma precedencia que usa el motor,
por lo que responde a la pregunta importante —*qué licencia está realmente en vigor*— en vez
de limitarse a decir «hay un fichero». Informa de la fuente resuelta, el titular, el plan y la
fecha de caducidad.

Ejecútalo después de cada instalación y después de retirar un override.

## 3 · Community → Business, in-place

Instalar una licencia Business o Enterprise válida en una compilación Community no
detiene esa compilación. El cockpit de sesiones y las demás páginas comerciales solo
aparecen después de que cambies esta instalación a Business. La vista **Edition & license**
de la consola nombra la licencia instalada por separado de la compilación. **Expires** es el
plazo de pago; **Lease until**, cuando se muestra, es el arrendamiento provisional de la
instancia, que es independiente. ¿Ejecutas en Docker? Ve a
[Docker y Compose](#docker-y-compose) más abajo.

:::note[Business y el canal de artefactos comerciales]
La opción `--enterprise` nombra el canal de artefactos comerciales, no la edición.
Las ediciones son Community, Business y Enterprise. Business incluye Regulated Operations,
AI Runtime Security, Compliance Packs e Identity & Scale en una suscripción.
Cada familia se puede activar o desactivar; ninguna se vende por separado.
:::

Con una licencia instalada, el binario comercial está a una descarga de distancia. No se
reinstala nada ni se mueve ningún dato:

```sh
export OLIVARES_UPGRADE_TOKEN=<TOKEN>   # not needed once this installation is connected
olivares upgrade --edition business
```

`olivares license install` y `olivares license status` muestran el comando exacto para la forma en que se instaló esta instalación, y la vista **Edition & license** de la consola ofrece el mismo paso como un botón, **Cambiar a Business**: el motor descarga el build, lo verifica, lo sustituye en su sitio y se reinicia con él. El token de descarga está en la página **Downloads** del portal de descargas con licencia ([https://licenses.olivares.ai/portal/downloads](https://licenses.olivares.ai/portal/downloads)), junto a estos comandos, y en tu correo de licencia; pásalo por su nombre, como arriba, para que no aparezca en la lista de procesos. Una instalación conectada (`olivares license connect start`) no necesita token. `--enterprise` es la grafía antigua de la misma ruta. El botón de la consola necesita una de las dos cosas, el token en el entorno del propio motor o esta conexión; sin ninguna de las dos, la vista muestra el comando.

:::caution[Instalaciones por paquete]
En una instalación deb, rpm, apk o AUR el gestor de paquetes es el dueño de `/usr/bin/olivares`, así que
este comando se rechaza: termina con código 2 antes de descargar nada. Usa el paquete Business cuando el
canal de paquetes con derecho de acceso lo sirva. Hasta entonces, añade `--target /usr/bin/olivares --data-dir /var/lib/olivares`
para sustituir ese archivo tú mismo; la base de datos de paquetes dejará de coincidir con él. `--check`
sigue mostrando el plan.
:::

Descarga la compilación comercial firmada para tu plataforma y **verifica la firma
offline**; un artefacto manipulado aborta la actualización y deja intacto el binario en
ejecución. Después lo sustituye de forma atómica y conserva una copia de seguridad del
anterior. Business usa el mismo número de versión que Community, así que también funciona cuando ya
ejecutas esa versión. Usa primero `--check` si quieres ver el plan sin aplicarlo; indica el cambio como
`edition: community -> enterprise`:

```sh
olivares upgrade --edition business --check
```

Reinicia el servicio y activa después los módulos:

```sh
olivares enterprise enable <preset>     # starter | regulated | full
```

El comando y los nombres de preset son lo que acepta el binario comercial. Los presets no son ediciones y no se corresponden uno a uno con las cuatro familias de capacidades de Business.
La activación está gobernada y auditada: primero muestra un diff y deja en preparación
cualquier módulo que necesite un secreto o una revisión, en lugar de activarlo a medias.
`olivares enterprise status` informa de qué está activo. Estos comandos existen **solo en el
binario comercial**: si `olivares enterprise` no es un comando, todavía ejecutas la
compilación Community y el cambio anterior aún no se ha producido.

:::caution[Haz una copia de seguridad antes del cambio]
El cambio sustituye un binario, no tus datos, pero haz igualmente la misma copia de seguridad
que pide [Actualizar y revertir](/es/how-to/upgrade-and-rollback/). Esa página también explica
cómo volver al binario anterior.
:::

### Docker y Compose

La imagen pública `docker.io/olivaresai/olivares` es Community. En un contenedor,
`olivares upgrade` instala la versión en el volumen de datos y no en la imagen, así que
recrear el contenedor la vuelve a iniciar. Esto aplica la versión Business cuando es más
reciente que la imagen o tiene la misma versión (`--check` indica el cambio como `edition: community -> enterprise`);
construir tú mismo la imagen, como se describe más abajo, es la alternativa. Con la licencia instalada
(sección 2), ejecuta la actualización en el contenedor en ejecución y reinícialo. El token viene
en tu correo de entrega; pásalo por nombre para que no aparezca en la lista de procesos:

```sh
export OLIVARES_UPGRADE_TOKEN=<TOKEN>
docker compose exec -e OLIVARES_UPGRADE_TOKEN olivares olivares upgrade --edition business --yes
docker compose restart olivares
```

La versión se guarda en `ota/` bajo el directorio de datos (`/var/lib/olivares/ota` en la
imagen publicada), junto con el manifiesto firmado y el artefacto con los que se verificó. El
binario de la imagen la inicia cuando es más reciente que la imagen, o cuando es la misma
versión compilada de otra forma (una imagen Community de la versión que aplicaste como
Business). Nada del volumen se da por bueno por sí solo: cada arranque comprueba la firma del
manifiesto con la clave OTA incrustada en la imagen y el artefacto con el digest que firma el
manifiesto. La prueba se guarda en el directorio temporal del contenedor (el Compose publicado
monta ahí un `tmpfs`), no en el volumen; al recrear el contenedor se vacía. Si la imagen es más
reciente que la versión aplicada, se ejecuta la imagen y el registro de arranque lo indica;
vuelve a ejecutar la actualización. La evidencia que no se verifica se ignora con un mensaje y
se ejecuta la imagen. El volumen no guarda copia del binario anterior: para volver al binario de
la imagen, elimina `ota/`. Hace falta un volumen que permita ejecutar archivos (un volumen con
nombre lo permite; en un montaje `noexec`, `olivares serve` se detiene y explica por qué, en
lugar de ejecutar en silencio el binario de la imagen).

También puedes construir la imagen tú mismo. Descarga el binario Business de la **misma versión y la misma arquitectura Linux** a través del
[portal de descargas con licencia](https://licenses.olivares.ai/portal) y verifica su firma y su
suma de verificación con las instrucciones de la descarga. Después puedes superponer ese binario
verificado sobre la imagen de ejecución Community correspondiente, en tu propio host. En un
directorio que contenga el binario verificado con el nombre `olivares`, usa este `Dockerfile`:

```dockerfile
ARG COMMUNITY_IMAGE
FROM ${COMMUNITY_IMAGE}
COPY --chmod=0755 olivares /usr/local/bin/olivares
```

Compila con el digest de la imagen Community de esa versión, verificado como en
[Verificar una release](/es/how-to/verify-a-release/). El resultado conserva el usuario no root, el
entrypoint y los puertos de esa imagen, así que el `Dockerfile` no necesita ninguna línea `USER`:

```sh
docker build --build-arg COMMUNITY_IMAGE=docker.io/olivaresai/olivares@sha256:<digest> \
  --tag olivares-business-local:X.Y .
```

Mantén esta imagen privada y conserva la licencia comercial y los avisos que acompañan a la
descarga. No pongas tu fichero de licencia ni el token de descarga en el contexto de compilación.
Establece el `image` de tu servicio de Compose en `olivares-business-local:X.Y` y recrea
ese servicio con `docker compose up -d --no-deps --force-recreate olivares`. Con Docker
independiente, recrea el contenedor con esta imagen y el mismo volumen y los mismos
argumentos. Conserva el volumen de datos: no uses `down --volumes`.

Comprueba **Edition & license** tras el reinicio: la edición debe indicar **Business**, y la
licencia instalada permanece en el volumen de datos. Los contratos Enterprise usan también esta
compilación comercial; cualquier artefacto específico del contrato sigue las instrucciones de su descarga.

## Retirar una licencia

```sh
olivares license uninstall --data-dir /var/lib/olivares --yes
```

El comando elimina `<data-dir>/license.key` e indica qué ha retirado. Al igual que `install`,
**se niega** mientras pueda ver un override `OLIVARES_LICENSE*`: ese fichero no es lo que está
en vigor, por lo que eliminarlo no cambiaría nada; y tiene el mismo punto ciego: un flag
`--license` pasado a un motor que se ejecuta en otro proceso le resulta invisible. Esta es la
mitad offline del propio `DELETE /v1/console/license` de la consola.

Retirar la licencia **no** desactiva nada de lo que estabas ejecutando. Retira la atestación;
el binario comercial sigue comportándose como tal hasta que vuelvas a cambiarlo.

## Qué *no* contiene esta página

- **Emitir licencias** (`license keygen` / `sign`) es la parte del proveedor del mismo comando.
  No la necesitas como cliente.
- **Qué incluye cada plan** está en las páginas de precios, no aquí.
- **Cómo funciona el modelo** —por qué una suscripción da acceso a artefactos en vez de ser un
  interruptor— se explica en [Open core y licencias](/es/explanation/open-core-and-licensing/).
- **El ensayo de compra → reembolso** (con fixtures de sandbox, no con una tarjeta real) es
  [Ensayar el ciclo comercial](/how-to/rehearse-commercial-cycle/).
