---
title: "Resolución de problemas (síntoma → diagnóstico → solución)"
description: >-
  La guía de modos de fallo para el operador, destilada de los propios runbooks
  del producto: problemas de arranque y de primera ejecución, fallos de
  readiness, contrapresión de ingesta, fallos de verificación del ledger y los
  avisos que el motor imprime a propósito.
---

Cada entrada sigue la misma forma: el síntoma que ves, cómo confirmar qué
es y la solución. Las líneas de log citadas son las cadenas reales del motor,
así que puedes hacer grep sobre ellas. Donde existe un runbook más profundo, la
entrada enlaza la página correspondiente en lugar de rederivarla.

## Primer arranque y configuración

### Me he perdido el token de configuración

Un reinicio **no** lo vuelve a imprimir (solo se almacena el hash del token, en
`setup.token` en el directorio de datos). Mientras no exista todavía ningún usuario, la recuperación es
segura: detén el motor, elimina `setup.token`, arráncalo — se acuña e imprime un
token nuevo. Esto funciona *solo* en una instalación sin usuarios, así que no es
una vía de toma de control. El token va a **stdout únicamente** (el journal bajo
systemd, el log del contenedor en Docker/Kubernetes) — nunca a ficheros de log.

### `=== FIRST-BOOT SETUP ===` nunca apareció

Ya existen usuarios en ese directorio de datos — no estás en el primer arranque.
O inicias sesión con el administrador existente o, para un arranque genuinamente
nuevo, usa un `--data-dir` nuevo.

### El motor advierte sobre claves en el primer arranque

```text
generated a new audit signing key; back it up path=/var/lib/olivares/audit-signing.key
generated a self-signed TLS certificate; clients must trust it, or pin it with --pin-sha256=<pin_sha256> (that value, verbatim) cert=/var/lib/olivares/tls.crt cert_fingerprint_sha256=d38567e8…378c4e7f pin_sha256=JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

Ambos son deliberados, y el primero es el que muerde más tarde: **no hay
escrow forzado** — copia `audit-signing.key` fuera de la máquina ahora, y fija la
clave pública (`GET /v1/audit/pubkey`) fuera de la máquina, o un futuro compromiso del host
te dejará sin poder demostrar la integridad de tu propio ledger
([copia de seguridad y restauración](/es/how-to/backup-and-restore/#las-dos-claves-que-lo-deciden-todo)).

La línea de TLS imprime **dos** resúmenes, y no son intercambiables:
`cert_fingerprint_sha256` es el resumen del certificado, el que muestra un
navegador; `pin_sha256` es el resumen del SPKI de la hoja, y es el único que
compara `--pin-sha256`. Copia ese valor literalmente:

```bash
olivares status --server https://127.0.0.1:8443 \
  --pin-sha256 JsdrhrY77Me8miAmobJsqamE3NDWIOSBrDTwbHkyCD0
```

Fijar en su lugar la huella del certificado no falla como valor de bandera
inválido — es un resumen de 32 bytes bien formado, así que se intenta la
conexión y se rechaza con `TLS SPKI pin mismatch`, que indica el valor que
deberías haber usado. Con `curl --pinnedpubkey sha256//…` añade el relleno `=`
final: el motor imprime base64 sin relleno a propósito, para que el valor salga
sin comillas en el log y sobreviva a un copiar y pegar, pero curl exige la forma
con relleno.

## Fuentes y el access map

### El mapa está vacío

Primero comprueba si hay algo cableado. El motor lo dice explícitamente en el
arranque:

```text
ingest: no observation sources configured (OLIVARES_SOURCES_CONFIG.sources is empty); no connector will ingest — the estate runs on no live traffic
```

Un fichero de fuentes ausente, ilegible o inválido **avisa y continúa** (el arranque
nunca falla por ello) — así que un motor de aspecto sano con un mapa vacío suele
significar que la configuración nunca se cargó. Corrige el fichero/ruta y reinicia; el
éxito tiene el aspecto de `ingest: wired source … kind=…` por cada fuente. Una fuente que
no consigue construirse registra `ingest: failed to register in-process source; not wired`
con el motivo — se informa, nunca se descarta en silencio.

### pgAudit está cableado pero no llegan edges

Tres causas cubren casi todos los casos, todas por diseño
([la guía de pgAudit](/es/how-to/connectors/pgaudit/)):

1. **El servidor no registra en UTC.** Los registros con una abreviatura de zona
   distinta de UTC se **omiten** en lugar de marcarse con una marca de tiempo errónea —
   define `log_timezone = 'UTC'`.
2. **csvlog es por lotes, no por seguimiento.** `follow` aplica solo a `jsonlog`; una
   fuente csvlog ingesta en cada pasada, no de forma continua.
3. **Las clases auditadas están desactivadas** — comprueba que `pgaudit.log` incluya
   `read, write`.

### Todo aparece como drift

Es lo esperado en una instalación nueva: sin grants declarados, todo acceso observado
es, con honestidad, "inesperado". Ese es el estado inicial, no un fallo —
[tríalo](/es/how-to/cookbook/drift-triage/) declarando los grants que pretendes.

## Disponibilidad

### `/readyz` devuelve 503

Lee el cuerpo — distingue los dos casos:

- `{"status":"unavailable","store":"down"}` — el almacén es inalcanzable. En
  SQLite: disco lleno, problemas de PVC, permisos de fichero. En Postgres:
  alcanzabilidad y credenciales. **La liveness sigue pasando deliberadamente** (el
  proceso está vivo), así que nada entra en bucle de reinicio por una caída del almacén;
  reinicia el pod/servicio manualmente tras arreglar el almacén si se queda atascado.
- `{"status":"standby","leader":false,…}` — un standby de HA respondiendo
  con honestidad. No es un error: el Service enruta al líder; los standbys se drenan
  por diseño. Si **todas** las réplicas reportan standby, la elección de líder está
  atascada — comprueba la conectividad de los advisory-lock de Postgres.

### El pod murió y nada tomó el relevo

En la topología **por defecto de una sola réplica** no hay failover automático —
la recuperación es la reprogramación del StatefulSet más la reconexión del volumen RWO
(vigila los errores de Multi-Attach; el volumen ata la recuperación a su AZ).
El failover automático es una propiedad de la
[topología de HA](/es/tutorials/getting-started/kubernetes/#3-ha-activo-pasivo)
(Postgres + réplicas + clave de firma compartida). Nunca ejecutes producción con
la persistencia desactivada: un `emptyDir` pierde la clave de firma en cada
reagendamiento.

## Rendimiento

### La latencia de ingesta p99 está subiendo (contrapresión)

El bus **bloquea en lugar de descartar** — una subida de
`olivares_ingest_duration_seconds` p99 es la señal diseñada de que un
suscriptor está saturado, no pérdida de datos. Nombra al culpable directamente:

```promql
olivares_eventbus_queue_depth / olivares_eventbus_queue_capacity > 0.9
```

Las etiquetas por suscriptor apuntan al módulo lento;
`olivares_eventbus_publish_blocked_total` cuenta los eventos de contrapresión.
La causa raíz habitual es el **throughput de escritura del almacén** (el techo de
escritor único de SQLite) — eso es un arreglo de capacidad (pasar a Postgres, o reducir la
amplificación de escritura), no un parámetro de ajuste. Los conectores de salida lentos (un webhook,
un SIEM) nunca deben ser suscriptores síncronos.

Con el bus distribuido activado (`OLIVARES_BUS_CONFIG`), recuerda que el
puente entre nodos es **como mucho una vez**: un puente saturado llena
`olivares_eventbus_bridge_pending_messages` y luego **descarta eventos remotos**,
contabilizados en `olivares_eventbus_bridge_dropped_total` — alerta ante cualquier aumento,
y haz page cuando `olivares_eventbus_bridge_connected == 0`.

### Los inicios de sesión fallan con "locked out"

Que `olivares_auth_login_attempts_total{outcome="locked_out"}` suba significa que el
throttle por cuenta/por IP se activó tras fallos repetidos. Se limpia
solo; investiga el origen de los fallos en lugar de subir los límites.

## Evidencia

### El ledger falla la verificación

Primero, ten claro qué ejecutaste: el `audit verify` por defecto **sale con 0 incluso ante una
cadena fallida** (el resultado está en el informe JSON) — la automatización debe usar
`--strict` o parsear el informe:

```bash
olivares audit verify --tenant $TENANT --data-dir /var/lib/olivares --strict \
  --pubkey <BASE64-PINNED-OFF-BOX>
```

Fija la clave pública **fuera de la máquina**: sin pins el verificador confía en claves leídas
del host (posiblemente comprometido) — válido como comprobación orientativa, no como
evidencia de manipulación. Luego clasifica por el campo `reason`:

| Motivo | Clase | Respuesta |
|---|---|---|
| `hash-mismatch`, `prev-mismatch`, `head-mismatch`, `tail-truncated` | manipulación o truncamiento | trátalo como un SEV1: preserva la máquina, reconcilia contra el checkpoint fuera de la máquina |
| `checkpoint-sig-invalid`, `checkpoint-link-mismatch`, `event-sig-invalid` | manipulación o clave equivocada | SEV1 salvo que puedas demostrar una confusión en la custodia de claves |
| `seq-gap` | borrado **o** una inconsistencia de restauración | compara contra el checkpoint fuera de la máquina antes de gritar manipulación |
| `event-sig-missing` | posiblemente registros heredados de antes de habilitar la firma | acótalo con `--from` en la frontera de habilitación; la ausencia previa a la frontera es esperada |

Una copia de seguridad restaurada que pasa un recorrido ingenuo pero discrepa de tu checkpoint
fijado fuera de la máquina es el caso de anomalía de restauración — esa comparación es para lo que
existe el pin.

### `olivares_audit_checkpoint_age_seconds` no para de crecer

Los checkpoints han dejado de escribirse (cadencia por defecto 1h;
`OlivaresAuditCheckpointStale` se dispara a las 2h). Revisa el log del motor en busca de
errores de checkpoint y la escribibilidad del almacén — mientras crece, tu ancla de
evidencia de manipulación envejece.

## Notificaciones y sinks

### Un destino nunca recibe nada

Un destino con un kind desconocido se **omite y se registra**
(`notify: destination has unknown connector kind; skipped` — comprueba la
escritura del `kind`). Para los sinks de eventing, `POST …/subscriptions/{id}/test`
envía una entrega que puedes observar, y los endpoints deben ser HTTPS
([push a SIEM](/es/how-to/cookbook/push-to-siem/)).

---

Si un síntoma no está aquí y el propio mensaje del motor no lo explica,
eso es un fallo de documentación — por favor, repórtalo con la línea de log.
