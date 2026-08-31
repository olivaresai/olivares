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
enterprise sin reinstalar nada.

:::note[Una licencia es una atestación, no un interruptor en tiempo de ejecución]
**No bloquea ninguna función del software que estás ejecutando.** Una licencia caducada o
ausente no desactiva funcionalidades, y ninguna licencia limita las cuentas de usuario: los
usuarios autoalojados son ilimitados en todos los niveles. Es una declaración firmada de
aquello a lo que tienes derecho, no una clave que desbloquee código que ya está en tu disco.

**Lo que sí bloquea es el ACCESO A LOS ARTEFACTOS**, y esa distinción es la base de todo el
modelo: se necesita una licencia vigente para descargar el binario enterprise y para instalar
desde un bundle local (`olivares upgrade --bundle`); la licencia se comprueba offline frente a
la clave integrada en tu binario. Por eso la edición enterprise es un binario diferente que
descargas con un token, en lugar de una feature flag que se activa en el que ya tienes, y por
eso decirte que «no bloquea nada» sería incorrecto.
:::

## Qué has recibido

| Has comprado | Qué recibes | Qué haces con ello |
|---|---|---|
| Community | nada que instalar | ya está en ejecución; nada de esta página se aplica |
| Business / Business Max, autoalojado | un **fichero de licencia** y un **token de descarga** | instala la licencia y cambia al binario enterprise |
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

Con una licencia instalada, el binario enterprise está a una descarga de distancia. No se
reinstala nada ni se mueve ningún dato:

```sh
olivares upgrade --enterprise --token <TOKEN>
```

Descarga la compilación enterprise firmada para tu plataforma y **verifica la firma
offline**; un artefacto manipulado aborta la actualización y deja intacto el binario en
ejecución. Después lo sustituye de forma atómica y conserva una copia de seguridad del
anterior. Usa primero `--check` si quieres ver el plan sin aplicarlo:

```sh
olivares upgrade --enterprise --token <TOKEN> --check
```

Reinicia el servicio y activa después los add-ons:

```sh
olivares enterprise enable <preset>     # starter | regulated | full
```

La activación está gobernada y auditada: primero muestra un diff y deja en preparación
cualquier add-on que necesite un secreto o una revisión, en lugar de activarlo a medias.
`olivares enterprise status` informa de qué está activo. Estos comandos existen **solo en el
binario enterprise**: si `olivares enterprise` no es un comando, todavía ejecutas la
compilación Community y el cambio anterior aún no se ha producido.

:::caution[Haz una copia de seguridad antes del cambio]
El cambio sustituye un binario, no tus datos, pero haz igualmente la misma copia de seguridad
que pide [Actualizar y revertir](/es/how-to/upgrade-and-rollback/). Esa página también explica
cómo volver al binario anterior.
:::

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
el binario enterprise sigue comportándose como tal hasta que vuelvas a cambiarlo.

## Qué *no* contiene esta página

- **Emitir licencias** (`license keygen` / `sign`) es la parte del proveedor del mismo comando.
  No la necesitas como cliente.
- **Qué incluye cada plan** está en las páginas de precios, no aquí.
- **Cómo funciona el modelo** —por qué una suscripción da acceso a artefactos en vez de ser un
  interruptor— se explica en [Open core y licencias](/es/explanation/open-core-and-licensing/).
