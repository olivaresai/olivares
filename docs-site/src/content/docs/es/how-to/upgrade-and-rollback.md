---
title: Actualizar y revertir
description: >-
  Cómo mover un despliegue autoalojado de Olivares AI a una release más reciente:
  previsualiza el plan, realiza el cambio, verifícalo y vuelve atrás si es necesario.
  Cubre el comando autoservicio `olivares upgrade`, los bundles air-gap y el cambio de
  imagen de plataforma.
---

Una actualización sustituye el binario; no te migra a un producto diferente. El directorio
de datos, la clave de firma de auditoría y el material TLS permanecen donde están, y el motor
aplica por sí mismo las nuevas migraciones de esquema al arrancar. Esta página guía al
operador desde «¿debo instalar esta release?» hasta «necesito recuperar la anterior».

:::caution[Haz primero una copia de seguridad]
Haz una copia antes de cada actualización, también de las que parezcan rutinarias. Tanto la
pantalla **Backups** de la consola (`/backups`) como [Copias de seguridad y
restauración](/es/how-to/backup-and-restore/) lo hacen. Nada de esta página depende de que
tengas una copia, pero la querrás cuando algo te sorprenda.
:::

## Qué vía de actualización te corresponde

Hay dos formas de avanzar el binario y ambas llegan al mismo punto.

| Tu instalación | Vía |
|---|---|
| Un binario en un host, systemd o Docker Compose | `olivares upgrade`: esta página |
| Kubernetes / Helm | Define la imagen y deja que el operador haga el rolling update. No ejecutes `olivares upgrade` dentro de un pod: el despliegue es declarativo y la siguiente reconciliación lo desharía. |

## Antes de nada: lee el plan

`--check` descarga y verifica el manifiesto del canal, lo compara con lo instalado e imprime
lo que ocurriría. No sustituye nada.

```sh
olivares upgrade --check
```

Responde con la versión instalada, la disponible y una línea de estado que será `up to date`,
`upgrade available`, `DOWNGRADE (blocked unless --force-rollback)` o `UNKNOWN`. Lee esa línea
en vez de comparar por tu cuenta los dos números de versión.

**`UNKNOWN` no significa «probablemente está bien».** Significa que no se pudo medir la versión
instalada —por ejemplo, por un directorio de staging de otra arquitectura, un montaje `noexec`
o una compilación desde el código fuente— y tanto la protección antirretroceso como el requisito
de versión mínima afirman algo *sobre* esa versión instalada, por lo que ninguna puede evaluarse.
El comando se niega a adivinar. Declara la versión que sabes que está instalada y las
protecciones seguirán activas:

```sh
olivares upgrade --check --current-version 26.8.0
```

## Canales de release

<!-- BEGIN GENERATED olivares-upgrade-channels — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

`olivares upgrade` sigue un **canal** de release. Hay **3**, declarados en
`core/release/manifest.go` por orden creciente de estabilidad:

| Valor de `--channel` | Declarado como |
|---|---|
| `stable` | `release.ChannelStable` |
| `security` | `release.ChannelSecurity` |
| `lts` | `release.ChannelLTS` |

Los valores que no figuren en esta tabla se rechazan antes de descargar nada
(`release.ValidChannel`).

<!-- END GENERATED olivares-upgrade-channels -->

`stable` es la línea de disponibilidad general y la predeterminada. `security` lleva
correcciones fuera de banda y nada más, por lo que un despliegue que la siga recibe releases
de seguridad sin recibir releases de funcionalidades.

:::caution[`lts` se valida, pero nadie lo publica]
La tabla anterior se genera a partir de las constantes de canal que declara el código, por lo
que enumera todos los valores que acepta `--channel`, incluido `lts`. **No se produce ni se
publica ningún manifiesto `lts`**, así que un despliegue que lo siga pedirá al host de
actualizaciones un objeto que no existe. El soporte de seguridad dura solo el periodo
contratado, sin backports generales, y no hay una línea congelada: los derechos duran el plazo
pagado, sin fallback adquirido ni derecho perpetuo. Elige `stable` o `security`.
:::

Elige el canal que corresponda a tu forma de operar y mantenlo:

```sh
olivares upgrade --channel security
```

Una release de seguridad se marca como tal en el manifiesto y `--check` imprime los avisos que
corrige. Si utilizas el canal de seguridad, recibirás esas correcciones fuera de banda respecto
a la línea de disponibilidad general.

## Realizar la actualización

```sh
olivares upgrade
```

Esto es lo que hace el comando, en orden, y el motivo de cada paso:

1. **Descarga el manifiesto del canal y verifica su firma sin conexión** frente a la clave de
   release Ed25519 integrada en la compilación. El ancla de confianza es la firma, no el
   transporte. Una compilación sin clave integrada exige que proporciones una con `--pubkey`;
   no existe una vía sin verificar.
2. **Se niega a retroceder.** Instalar una versión más antigua que la que se está ejecutando se
   bloquea salvo que pases `--force-rollback`, lo que registra una entrada de auditoría.
3. **Vincula el artefacto al SHA-256 firmado del manifiesto** antes de ejecutar sus bytes.
4. **Sondea el candidato** y luego lo sustituye de forma atómica, conservando una copia con
   timestamp del binario reemplazado. Si el binario recién instalado no arranca, el comando
   vuelve por sí solo a esa copia.
5. **No altera el proceso en ejecución.** El cambio sustituye el archivo en disco. El código
   nuevo toma el control al reiniciar el servicio.

Añade `--yes` cuando lo ejecutes desde un script y no haya nadie para responder a la
confirmación.

:::note[No hay parcheo en caliente]
Un binario Go no se parchea in-process. «Cero tiempo de inactividad» significa aquí un drenado
y relevo ordenados o un rolling restart, nunca un parche dentro del proceso. Lo que sí se
aplica en vivo y sin reiniciar son los datos y la configuración: sources, connectors, secrets,
policy y licencia.
:::

## Instalaciones air-gap

Un despliegue air-gap nunca contacta con un host de actualizaciones. Introduce el bundle por el
medio que ya consideres fiable e instálalo desde el archivo local: la verificación es idéntica,
porque la red nunca fue aquello en lo que se confiaba.

**Instalar desde un bundle requiere una licencia vigente en la máquina.** Se comprueba sin
conexión frente a la clave de licencia integrada en el binario: no se realiza ninguna llamada,
por lo que funciona detrás del air gap. Si aún no has instalado la licencia en la máquina,
[Instalar una licencia y pasar a enterprise](/es/how-to/install-a-license/) es la página que
explica cómo hacerlo.
`--check` no está sujeto a esa condición, así que puedes
verificar un bundle antes de preparar nada:

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --check   # verify only; no license read
olivares upgrade --bundle ./olivares-release.tar.gz --yes     # install; needs a live license
```

Si tu compilación no incluye una clave de release o replicas las releases bajo tu propia clave
de firma, indica al comando la clave frente a la que verificas:

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --pubkey @/etc/olivares/release.pub
```

Consulta [Instalar en air-gap](/es/how-to/air-gap-install/) para saber cómo se produce y
transporta el bundle.

## Despliegue gradual y comprobaciones desatendidas

Un manifiesto puede nombrar una cohorte de despliegue gradual para que una release alcance
primero solo a una fracción del estate. `--if-eligible` hace que un nodo actúe únicamente si
pertenece a esa cohorte; de lo contrario, no hace nada:

```sh
olivares upgrade --if-eligible --yes
```

Esa es la forma que ejecuta el temporizador integrado. Para emitir un temporizador y un
servicio systemd que la invoquen dentro de una ventana de mantenimiento:

```sh
olivares upgrade --install-timer --timer-schedule 'Sun *-*-* 03:00:00'
```

De forma predeterminada imprime las unidades; `--timer-dir` las escribe donde indiques. Es
opt-in: nada se programa por sí solo.

La consola ofrece la mitad de solo lectura de la misma información: **Settings → update
status** llama a `POST /v1/console/update-check`, que ejecuta bajo demanda una comprobación del
canal configurado. Un despliegue air-gap o sin canal configurado responde `501` y explica el
motivo, en lugar de afirmar que no hay actualización.

## Verificar la actualización

```sh
olivares version
olivares upgrade --check
```

`--check` debería indicar ahora `up to date`. Después, confirma que el propio servicio está
sano: la pantalla **Health** de la consola (`/health`) o el endpoint de readiness del motor
descrito en [Monitorizar con Prometheus](/es/how-to/monitor-with-prometheus/).

## Revertir

El binario anterior se conserva junto al que lo reemplazó y el comando imprime su ruta cuando
realiza el cambio. Revertir consiste en restaurar ese archivo y reiniciar el servicio.

La reversión es segura por diseño, no por suerte: cada cambio de esquema entrega primero una
expansión aditiva y deja su contrato destructivo para una release posterior, de modo que el
binario de la release anterior sigue funcionando con el esquema actualizado. Por eso revertir
significa «volver a colocar el binario antiguo», no «revertir la base de datos».

Si necesitas instalar una release más antigua en vez de restaurar la copia conservada, la
protección antirretroceso lo bloquea hasta que lo indiques expresamente:

```sh
olivares upgrade --force-rollback --yes
```

La anulación queda registrada en el audit log. El requisito de versión mínima **no** puede
anularse con esta opción: si un manifiesto declara un mínimo superior a tu versión instalada,
pasa por una release intermedia en lugar de intentar saltarlo.

## Cuando algo sale mal

| Síntoma | Qué significa | Qué hacer |
|---|---|---|
| `--check` imprime `UNKNOWN` | No se pudo medir la versión instalada, así que no puede afirmarse ningún orden | Pasa a `--current-version` la versión que sabes que está instalada |
| `min_ver` dice que tu versión es demasiado antigua | La release se niega a instalarse directamente sobre la tuya | Actualiza primero a la release intermedia indicada |
| El binario nuevo no arranca | Falló el sondeo posterior al cambio | Ya se ha vuelto a la copia; revisa los logs e informa sobre la release |
| `--install-timer` se activa pero no ocurre nada | El nodo no pertenece a la cohorte de despliegue gradual | Es lo esperado con `--if-eligible`; la cohorte se amplía conforme avanza el despliegue |
| "another olivares upgrade is already installing", exit **5** | Solo puede actualizar un proceso cada binario. El bloqueo se mantiene durante toda la secuencia de descarga y sustitución | Espera al que está en curso y vuelve a ejecutar el comando. Si no hay ninguno, el kernel ya ha liberado el bloqueo: ejecútalo de nuevo |
| "it CHANGED while this upgrade was downloading" | Otro proceso sustituyó el binario después de preparar el plan: un gestor de paquetes, un despliegue de imagen o una ejecución de gestión de configuración | Vuelve a ejecutarlo: las protecciones se reevalúan frente a lo que realmente está instalado. Si persiste, dos sistemas están gestionando el mismo binario |

**Un solo agente de actualización por binario.** `olivares upgrade` toma un bloqueo exclusivo
sobre el destino durante toda la secuencia de preparación, descarga y sustitución, por lo que
una segunda ejecución termina con código `5` en vez de instalar. Instala **un** temporizador y
cambia su `--channel`, en vez de ejecutar uno por canal: antes, dos instalaciones que
terminaban en el mismo segundo sobrescribían mutuamente su copia de reversión, y la reversión
automática de la perdedora restauraba entonces el *otro* binario y declaraba el éxito. Justo
antes de sustituirlo, el comando vuelve a leer los bytes del destino y se niega a continuar si
no son aquellos sobre los que preparó el plan, porque los veredictos antirretroceso y de
versión mínima afirman algo sobre un archivo instalado concreto.

Para cualquier otro problema, [Resolución de problemas](/es/how-to/troubleshooting/) es la vía
general, y la pantalla **Logs** de la consola (`/logs`) transmite el log del propio motor.
