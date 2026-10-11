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
Antes de cada actualización, crea una copia DR con `dr backup` de la versión instalada.
La pantalla **Backups** (`/backups`) y [Copias de seguridad y restauración](/es/how-to/backup-and-restore/)
describen el procedimiento. **Tras avanzar el esquema, necesitas la copia anterior a la
actualización y su frase de paso privada o KEK para volver a la versión anterior.**
`olivares upgrade` conserva el ejecutable; no crea una copia de la base de datos.
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

<!-- release -->
```sh
olivares upgrade --check --current-version 0.1
```
<!-- /release -->

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

La reversión automática del paso 4 prueba `version`, no el arranque del servicio ni la
compatibilidad de la base de datos. No recupera un almacén migrado al reiniciar.

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

La instalación sin conexión requiere Enterprise. Community verifica un bundle con `--bundle --check`, sin leer una licencia ni instalarlo.

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --check
```


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

El ejecutable anterior se conserva junto al nuevo y el comando imprime su ruta. Esa copia
no es un punto de recuperación de los datos.

**Un binario antiguo rechaza una versión del esquema core superior a la que admite**,
incluso después de migraciones aditivas. Reinstalar el binario o la imagen anterior no deshace
el avance del esquema. No edites el historial de migraciones ni evites el rechazo.

1. Detén todos los motores que usan el almacén. Conserva los datos actualizados, la configuración
   del servicio, el material TLS y las claves externas de sellado.
2. Usa el **binario de la versión anterior** para restaurar el bundle DR tomado **antes** de
   actualizar: [Copias de seguridad y restauración](/es/how-to/backup-and-restore/). SQLite:
   directorio nuevo o `dr restore --in-place` con `--operator` y `--reason`; conserva los archivos
   preservados hasta confirmar la recuperación. PostgreSQL: destino vacío con `olivares db init`,
   sus `--dsn`, `--owner-dsn` y `--admin-dsn`, y un directorio nuevo para la clave de firma restaurada.
3. Exige que la verificación del ledger y la clave de auditoría termine correctamente. Apunta
   el directorio de datos, los volúmenes y los DSN PostgreSQL del servicio al almacén restaurado
   y a sus claves de firma correspondientes antes de arrancar la versión anterior.
4. Inicia sesión y comprueba los datos recuperados y la salud del servicio.

La recuperación vuelve al punto guardado. Las escrituras posteriores a la copia no están en
el almacén restaurado; conserva el actualizado para reconciliarlas. Sin el bundle y su frase
de paso o KEK, sustituir el ejecutable no permite esta recuperación.

`--force-rollback` permite instalar un ejecutable anterior y registra la anulación en el audit log.
No evita la comprobación del esquema core ni el requisito de versión mínima y no restaura datos.
Si la versión instalada está por debajo del mínimo, pasa por una versión intermedia.

### Prueba la recuperación antes de actualizar producción

Arranca la versión anterior verificada con un directorio SQLite temporal, completa el setup,
inicia sesión y detenla. Crea y verifica un bundle con sus `dr backup` y `dr verify`. Arranca
el candidato sobre el mismo almacén, inicia sesión y detenlo. Si el esquema ha superado el límite
de la versión anterior, esta debe fallar con `core schema version newer than this binary supports`.
Restaura el bundle en un directorio nuevo con el `dr restore` anterior. Exige código de salida
cero y verificación correcta del ledger; arranca allí la versión anterior y comprueba el acceso,
la clave pública de auditoría original y los datos guardados. Un fallo de restauración o de
inicio de sesión significa que la prueba de recuperación ha fallado.

Comprobación SQLite medida (2026-10-08): la versión oficial 26.10.1<!-- release-fixed --> creó el esquema core 18
y un candidato posterior lo avanzó a 27. El binario antiguo rechazó el almacén con código 1
(`database=27 binary=18`). Sus `dr backup`, `dr verify` y `dr restore` terminaron con código 0.
Tras restaurar la copia anterior a la actualización en un directorio nuevo, funcionaron
la cuenta original y la clave pública de auditoría original.

## Cuando algo sale mal

| Síntoma | Qué significa | Qué hacer |
|---|---|---|
| `--check` imprime `UNKNOWN` | No se pudo medir la versión instalada, así que no puede afirmarse ningún orden | Pasa a `--current-version` la versión que sabes que está instalada |
| `min_ver` dice que tu versión es demasiado antigua | La release se niega a instalarse directamente sobre la tuya | Actualiza primero a la release intermedia indicada |
| El ejecutable instalado falla la prueba `version` tras el cambio | Falló la comprobación del ejecutable | El comando restaura el ejecutable guardado; revisa los logs |
| El servicio falla al reiniciar o el binario antiguo detecta un esquema core más nuevo | La prueba del ejecutable no cubre el servicio ni el almacén | Detén el servicio y restaura el bundle DR anterior a la actualización según Revertir |
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
