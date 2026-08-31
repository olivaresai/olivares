> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0009: Audit ledger append-only, encadenado por hash (hash-chained) y firmado

- **Status:** accepted
- **Fecha:** 2026-06
- **Decisores:** Olivares AI
- **Referencias:** contrato API/authz/auditoría (§6, decisión §13.4); modelo de amenazas (ledger)

## Contexto y planteamiento del problema

El audit ledger es uno de los activos más sensibles del producto: si se puede alterar en silencio,
el producto miente. Debe hacer el manipulado detectable y soportar copias externas y
verificables, siendo a la vez honesto sobre lo que la integridad en el propio host puede y no puede
garantizar.

## Factores de decisión

- Evidencia de manipulación (tamper-evidence): una reescritura de la historia debe ser detectable.
- Verificabilidad fuera de la caja para cumplimiento y respuesta a incidentes.
- Sin un nuevo subsistema de almacenamiento para los checkpoints.

## Opciones consideradas

- **Append-only + hash-chain + checkpoints firmados con Ed25519**, con exportación a una copia externa
  WORM/SIEM.
- **Una tabla de auditoría simple** con controles a nivel de aplicación.

## Resultado de la decisión

Opción elegida: un **ledger append-only, encadenado por hash (hash-chained)**; un checkpoint es en sí mismo un
evento de auditoría firmado (Ed25519, firma separada), de modo que reescribir la historia previa al checkpoint es
criptográficamente detectable. El ledger exporta a formatos externos SIEM/WORM (CEF,
LEEF, syslog, OTLP — una petición de exportación completa y lista para POST, con la
proyección simple de LogRecord como token de exportación propio `otlp_log_record` — OCSF),
llevando cada registro los campos de la cadena para que un SIEM pueda
reverificar offline; nunca se exporta PII.

### Consecuencias

- **Bueno:** evidencia de manipulación sin una tabla de checkpoints aparte; reverificación offline;
  exportación lista para SIEM.
- **Malo / compromisos:** la clave de firma en disco no detiene a un root del host / superusuario de
  la base de datos — por eso la **exportación externa WORM/SIEM es el verdadero control anti-manipulación**, y
  la documentación lo dice así.
- **Neutral:** cuando se tomó esta decisión la exportación era por pull; existía una
  costura (seam) de reenvío automático por push pero aún no estaba implementada.

  > **Enmienda de estado, 2026-07-25.** El forwarder de push está implementado y
  > cableado: `modules/siemforward` satisface `audit.Forwarder` y `cmd/olivares/boot.go`
  > arranca una bomba de ledger por tenant cuando existe una suscripción de eventing
  > `audit.recorded`. `NopForwarder` aplica cuando no hay forwarding configurado. La
  > exportación pull sigue disponible sin cambios. La decisión en sí queda intacta.

  > **Enmienda de estado, 2026-07-28.** Cuando se tomó esta decisión, la afirmación
  > anterior «un SIEM pueda reverificar offline» solo era cierta para el ENLACE de la
  > cadena y la firma de un checkpoint: las proyecciones no portaban el compromiso de
  > los metadatos de un evento, por lo que el hash propio de un registro no podía
  > volver a derivarse de una línea exportada. Ahora sí: cada entrada que consume el
  > hash de la cadena viaja en todos los dialectos, incluido el compromiso de los
  > metadatos, y ese compromiso se CIEGA por registro, de modo que completar la
  > preimagen no revela nada de los metadatos subyacentes. Hay tres afirmaciones
  > distintas, y la frase de esta ADR solo cubre la primera: recomputación de la
  > preimagen, NO autenticidad (una clave de confianza externa), NO completitud
  > (registros adyacentes y un checkpoint).
  >
  > Deben quedar registradas dos consecuencias. Ambas reglas de hash de metadatos
  > quedan activas permanentemente, discriminadas por fila mediante un valor de
  > cegado almacenado, porque un ledger append-only no puede reformular la regla de
  > hash de filas que ya ha sellado sin hacer que un historial legítimo sea
  > indistinguible de uno falsificado. Y el formato de archivo obtuvo una versión
  > para portar el valor de cegado, mientras que la versión anterior se acepta de
  > forma permanente por la misma razón: a un artefacto escrito para leerse años
  > después no se le puede retirar la versión que tiene debajo. La decisión en sí
  > queda intacta.

## Por qué se rechazaron las alternativas

- **Tabla de auditoría simple** — no aporta ninguna evidencia criptográfica de manipulación; inaceptable para un
  producto de seguridad cuya integridad del ledger lo es "todo".
