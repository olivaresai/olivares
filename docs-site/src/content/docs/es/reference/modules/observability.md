---
title: "Observabilidad — el modelo de lectura que el motor tiene de sí mismo"
description: >-
  Un modelo de lectura puro sobre lo que ya existe: qué estándares de
  interoperabilidad fija y sirve el motor, qué dice sobre una traza el audit
  ledger correlacionado con W3C, y qué es demostrablemente cierto sobre la cadena
  de suministro del binario en ejecución. No posee ninguna entidad y no persiste
  nada.
---

Observabilidad (`modules/observability`) es uno de los 32 módulos — al igual que
[live-ingest](/es/reference/modules/live-ingest/), cumple un papel arquitectónico
más que ocupar una ranura de capacidad. Es el **modelo de lectura que el motor
tiene de sí mismo**: tres superficies de solo lectura bajo
`/v1/m/observability/` que responden a las preguntas que renderiza la sección
System de la consola de administración, sin poseer una sola entidad del store.

## Las tres superficies

| Ruta | Responde |
|---|---|
| `GET /ingestion-health` | qué fluye hacia dentro y hacia fuera del motor **por estándar de interoperabilidad** — los estándares que el motor fija (OTel GenAI semconv, OCSF, ASIM, los formatos SIEM unificados, el push del ledger, Prometheus text, W3C Trace Context), cada uno con su versión verificada |
| `GET /traces`, `GET /traces/{id}` | qué dice el **audit ledger correlacionado con W3C** sobre una traza — la vista del lado de auditoría de una traza distribuida, unida por Trace Context |
| `GET /attestation` | qué es **demostrablemente cierto sobre la cadena de suministro del binario en ejecución** — la superficie de atestación que alimenta la [cadena de verificación de una release](/es/how-to/verify-a-release/) |

Las tres son lecturas con permisos acotados al módulo; nada aquí muta nada.

## Por qué es un módulo siquiera

La consola de administración necesitaba una respuesta autoritativa a "¿qué habla
realmente este motor, y en qué versión fijada?" — y la forma honesta de servir
eso es desde el propio motor, no desde documentación que puede desviarse. La
tabla de ingestion-health se genera a partir de los mismos pins contra los que
compilan los conectores y exportadores, así que cuando un pin se mueve, la
superficie se mueve con él.

## Contexto acotado, dicho con claridad

- **No posee entidades del store y no persiste nada** — un modelo de lectura puro
  sobre sustratos que ya existen (los pins, el ledger, la evidencia de
  atestación).
- **No** es el [módulo XXII (salud/SLA)](/es/reference/modules/xxii-health/), que
  está acotado a la fiabilidad de los agentes y servidores MCP del *estate*. Este
  módulo trata del *motor*.
- **No** es el endpoint de métricas: las series temporales operativas viven en
  [`/metrics`](/es/how-to/monitor-with-prometheus/); este módulo sirve respuestas
  estructuradas, no series.

## Reproducir el recorrido del modelo de lectura

En una **instalación Compose de Enterprise con SQLite, nueva y desechable**, ejecuta
`scripts/qualify-observability.py` con Python 3.11 o posterior. Aporta el binario
instalado, la dirección HTTPS, el nombre del contenedor, el commit completo del código
fuente y un directorio de evidencia nuevo:

```sh
python3 scripts/qualify-observability.py /path/to/olivares \
  https://127.0.0.1:8443 olivares <source-sha> /path/to/new-evidence
```

La sonda usa la gestión de organizaciones de Enterprise para aprovisionar un segundo tenant para sus comprobaciones de aislamiento. Las superficies de ingestion-health, los modelos de lectura de trazas y la atestación siguen disponibles en Community.

La sonda crea cuentas y agentes de fixture a través de la API, habilita los módulos
observability y security existentes, y reinicia físicamente el contenedor. Compara el
SHA-256 del ejecutable arrancado con el binario aportado, verifica la ingesta a partir
de un hallazgo de primera parte, comprueba los rechazos entre tenants y comprueba la
retención del detalle y de la exportación de trazas del ledger tras el reinicio y tras
desactivar y volver a activar el módulo. También verifica que los contadores de ingesta
se reinician y que la atestación separa la identidad medida del binario del estado
declarado de la canalización y de la release no publicada. Las comprobaciones de CLI
usan salida JSON. Las credenciales permanecen en memoria o en los entornos de los
procesos hijo; los registros conservados del motor expurgan los tokens de configuración.

Usa los pasos de instalación del README con una imagen candidata seleccionada de forma
explícita. No ejecutes nunca la sonda contra una instalación existente. Se rechaza la
optimización de Python porque desactiva las aserciones. Elimina la pila Compose
desechable y sus volúmenes tras la ejecución. La sonda deja esa limpieza a quien la
invoca y escribe su resultado y el registro del motor en el directorio de evidencia. No
ejercita acciones de consola, PostgreSQL, colectores de proveedores ni la verificación
de firmas de release.

## Relacionado

- [Monitorizar con Prometheus](/es/how-to/monitor-with-prometheus/) — las métricas
  operativas y los SLOs.
- [Referencia de eventos](/es/reference/events/) — el vocabulario del bus sobre el
  que informa la tabla de ingesta.
- [Verificar una release](/es/how-to/verify-a-release/) — la evidencia de cadena
  de suministro que refleja la superficie de atestación.
