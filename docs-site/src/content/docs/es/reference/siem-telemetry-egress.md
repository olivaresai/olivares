---
title: Salida a SIEM y telemetría
description: >-
  Todos los formatos de cable que emite el plano de control — CEF, LEEF 2.0,
  syslog RFC 5424, logs OTLP, OCSF 1.8.0, SARIF 2.1.0 —, el mapeo de severidad
  sobre el que se escriben las reglas, los límites de receptor que aplican a cada
  transporte y los dos sitios donde una proyección no es un sobre completo.
---

Esta página es el **contrato de salida**: qué sale del plano de control, en qué
dialecto, sobre qué transporte y qué hace un receptor con ello. Está escrita para
quien tiene que hacer que una regla de ArcSight, un DSM de QRadar, una DCR de
Sentinel o una subida a code scanning funcionen a la primera.

Todo lo que hay aquí está contrastado con las especificaciones de los propios
fabricantes, con la fecha de la comprobación. Donde un fabricante **no**
especifica algo, la página lo dice en vez de adivinar: esos huecos van marcados
como *no definido por el fabricante*, y el encoder toma el lado conservador.

## Los dos flujos

Hay dos orígenes de registros independientes, y comparten un mismo encoder para
que los dialectos no puedan divergir:

| Flujo | Qué lleva | Pull | Push |
|---|---|---|---|
| **Ledger de auditoría** | El ledger append-only encadenado por hash, con sus campos de integridad (secuencia, hash previo, hash, firma) | `GET /v1/audit/export?format=…` (NDJSON, un registro por línea) | El reenviador del ledger, por cualquier conector de salida |
| **Notificaciones y hallazgos** | Hallazgos de gobierno, decisiones de política, eventos de salud y ciclo de vida | — | Cualquier conector de salida |

Los campos de integridad del ledger viajan **verbatim** en todos los formatos, así
que un SOC puede re-verificar la cadena desde la copia de su propio SIEM, no solo
desde el producto.

## Formatos

| Formato | Estándar | Versión fijada | Dónde se selecciona |
|---|---|---|---|
| CEF | ArcSight Common Event Format | V27 (julio 2024) | export del ledger, conectores |
| LEEF | IBM QRadar Log Event Extended Format | 2.0 | export del ledger, conectores |
| syslog | RFC 5424 (+ RFC 5426 UDP, framing TCP RFC 6587, TLS RFC 5425) | — | export del ledger, conectores |
| Petición OTLP (`otlp`) | Petición de export OTLP/HTTP JSON (`ExportLogsServiceRequest`) | ver *Proyecciones* abajo | export del ledger, conectores |
| Petición OTLP (`otlp_envelope`) | Alias exacto, byte a byte, de `otlp` | ver *Proyecciones* abajo | export del ledger, conectores |
| LogRecord OTLP (`otlp_log_record`) | OpenTelemetry logs, un LogRecord por línea | ver *Proyecciones* abajo | export del ledger |
| OCSF | Open Cybersecurity Schema Framework, perfil `ai_operation` | 1.8.0 | export del ledger, conectores |
| ASIM | Microsoft Sentinel Advanced SIEM Information Model | — | conectores |
| ECS | Elastic Common Schema | 9.4.0 | conector de Elastic |
| UDM | Google SecOps Unified Data Model | — | conector de Chronicle |
| SARIF | OASIS Static Analysis Results Interchange Format | 2.1.0 Errata 01 | export de hallazgos |

Cada superficie de selección acepta su propio subconjunto ordenado de estos tokens,
derivado de un único catálogo para que las listas no puedan volver a divergir:

| Superficie | Tokens aceptados | Por defecto |
|---|---|---|
| Export del ledger (`GET /v1/audit/export?format=…`) | `cef\|leef\|syslog\|otlp\|otlp_envelope\|otlp_log_record\|ocsf` | `cef` |
| Sink de eventing (`sink_format` de una suscripción push) | `ocsf\|cef\|leef\|syslog\|otlp\|otlp_envelope\|json` | `ocsf` |
| Conectores de notificación (`filelog`, `splunkhec`, `s3archive`, `siem`) | `json\|cef\|leef\|syslog\|otlp\|otlp_envelope\|ocsf\|asim` | `json` |
| Conector syslog | `syslog\|cef\|leef` | `syslog` |

El export del ledger no tiene passthrough de JSON crudo —sus formas JSON son las de
OTLP de arriba—. `json` significa dos entregas distintas: el sink de eventing
publica el sobre crudo del evento capturado (el passthrough estructurado, sin
transformación de dialecto), mientras que los conectores de notificación
renderizan solo una proyección mínima de notificación —los campos mostrables, no
la carga original—. Los cuatro conectores de notificación aceptan `asim`,
`s3archive` incluido. Un formato fuera de la lista de su superficie se rechaza:
una errata al redactar o configurar recibe un error que nombra los tokens
aceptados de esa superficie, y un valor almacenado corrupto se rechaza al
codificarlo (nombrando la grafía corrupta, no la lista); nada cae en silencio a
JSON.

## Severidad: la fuente de verdad

Toda regla que filtre por severidad se apoya en esta tabla. Es un único mapeo en
un único sitio, así que el número CEF, la prioridad syslog y la severidad OTLP
del mismo evento no pueden contradecirse.

| Severidad de producto | CEF (0-10) | syslog (0-7) | OTLP | ECS | UDM |
|---|---|---|---|---|---|
| info | 1 | 6 (info) | INFO | 1 | INFORMATIONAL |
| low | 3 | 5 (notice) | INFO2 | 3 | LOW |
| medium | 5 | 4 (warning) | WARN | 5 | MEDIUM |
| high | 7 | 3 (error) | ERROR | 7 | HIGH |
| critical | 10 | 2 (critical) | FATAL | 10 | CRITICAL |
| sin determinar | 0 (Unknown) | 6 (info) | UNSPECIFIED | 0 | *(se omite)* |

Hay dos propiedades que fijan los tests, porque las dos se pierden con facilidad
sin querer:

- **Las cinco severidades determinadas nunca comparten número.** Un selector de
  colector como `local0.notice`, una regla de ArcSight o una DCR de Sentinel
  filtran por el número emitido, y la trama RFC 5424 no lleva ninguna otra señal
  de severidad: dos severidades compartiendo prioridad destruirían una distinción
  en silencio y sin vuelta atrás.
- **Una severidad sin determinar no se inventa.** CEF V27 reetiquetó el `0` de
  *Low* a *Unknown*, y eso es lo que recibe un evento sin severidad determinada.
  (LEEF es la excepción: su rango es 1-10 y no tiene valor para «desconocido», así
  que se aplica el suelo. Ver abajo.)

:::note[Por qué la columna syslog es la que es]
Ni CEF ni RFC 5424 definen un mapeo de severidad CEF a prioridad syslog —
comprobado contra ambas especificaciones el 2026-07-24. La columna syslog es por
tanto **política de producto**, elegida para que cada severidad siga siendo
distinguible y para que «critical» caiga en la prioridad que RFC 5424 llama
precisamente *critical*. El único mapeo de fabricante que existe (un ajuste
configurable de un conector de ArcSight) también pone su banda más alta en
`crit`. Si has estandarizado otra distribución, mapéala en tu colector: estos
números no se moverán bajo tus pies sin una entrada `Changed` en el changelog.
:::

## Particularidades de CEF

- **Los tamaños de cabecera** se acotan a los máximos de V27: device vendor 63,
  device product 63, device version 31, event class id 1023, name 512.
- La especificación publica esos números pero nunca dice si cuentan **caracteres
  u octetos de cable**, ni define comportamiento para un campo que se pase (*no
  definido por el fabricante*, comprobado el 2026-07-24). Por eso se cumplen las
  dos lecturas: un valor se acota al número en caracteres decodificados **y** en
  octetos UTF-8 en el cable. Un nombre de dispositivo o de evento no ASCII cabe
  en menos caracteres de lo que el número sugiere: la dirección conservadora.
- El truncado afecta **solo a la cabecera**. La extensión, que lleva el contenido
  auditable, no se trunca nunca.
- Las claves de extensión con valor temporal (`rt`, `start`, `end`) van en
  **milisegundos epoch** decimales, como exige el diccionario CEF.

## Particularidades de LEEF

- `sev` es un entero en el rango **1-10** documentado por LEEF 2.0. Un evento
  cuya severidad nunca se determinó sale como `sev=1`: LEEF no tiene valor para
  «desconocido» y `sev=0` está fuera de rango.
- `devTime` es un **epoch de 13 dígitos**, que QRadar acepta sin `devTimeFormat`.
  Se **omite** —nunca se fabrica— para un evento sin hora registrada, y entonces
  QRadar recurre a la hora de recepción, como está documentado.
- `sev`, `devTime` y `devTimeFormat` son **propiedad del encoder**. Si un evento
  trae un campo con uno de esos nombres (en cualquier caja), se emite
  re-etiquetado como `olvSev` / `olvDevTime` / `olvDevTimeFormat`: el valor te
  sigue llegando, pero no puede sobrescribir la severidad normalizada ni
  re-fechar el evento. IBM documenta que un `devTime` reconocido tiene precedencia
  sobre el timestamp del syslog, y por eso esto no se deja al azar.

:::caution[No definido por IBM]
IBM no documenta qué hace QRadar con `sev=0`, con un `devTime` no parseable, ni si
las claves de atributo distinguen mayúsculas (comprobado el 2026-07-24). Lo de
arriba es la lectura conservadora de cada caso. Si tienes evidencia de receptor en
sentido contrario, merece un issue.
:::

## Transporte syslog y límites de receptor

El conector syslog lleva un registro RFC 5424 nativo, o un registro CEF / LEEF
como MSG de una trama RFC 5424 correcta, que es como ArcSight y QRadar ingieren
esos formatos por syslog.

- **TLS en 6514 (RFC 5425) es el valor por defecto**, con framing por conteo de
  octetos como exige el RFC. TCP o UDP en claro son una renuncia explícita del
  operador; no hay ruta de código que degrade a claro un destino TLS.
- **Presupuesto de carga del receptor** (`max_payload_bytes`, por defecto `0` =
  desactivado). Un receptor que parte un registro sobredimensionado convierte un
  evento auditable en dos mitades imparseables. Cuando declaras el presupuesto del
  destino que operas, un registro que lo supere **falla la entrega** —se reintenta
  y acaba en la DLQ, donde lo ves— en vez de enviarse para que lo partan. El
  registro nunca se trunca.

Valores de referencia para ese ajuste, con lo que dice realmente cada fuente
(comprobado el 2026-07-24):

| Receptor | Bytes | Qué dice la fuente |
|---|---|---|
| Cualquier receptor RFC 5424 | 480 | El mínimo que un receptor **DEBE** soportar (§6.1) |
| Cualquier receptor RFC 5424 | 2048 | El tamaño que las implementaciones **DEBERÍAN** soportar |
| Demonio syslog de ArcSight | 1024 | Sus guías dicen que un mensaje mayor **«podría partirse»** — una advertencia de despliegue, no una regla del receptor, y no aplica a las rutas de fichero o pipe |
| QRadar TCP | 4096 | La carga máxima **por defecto**; ampliable (IBM documenta 8192, con 32000 como techo) |

Ninguna de esas fuentes define si la cuenta incluye la cabecera syslog, así que el
presupuesto se mide sobre el **registro completo** en octetos UTF-8.

## OCSF

Los eventos se emiten como OCSF **1.8.0** con el perfil `ai_operation`, en las
tres clases que lo registran: API Activity (6003, la de por defecto), Process
Activity (1007) y Datastore Activity (6005). La salida se valida en la suite de
tests contra los esquemas oficiales de clase 1.8.0, que prohíben campos
desconocidos: un atributo fuera de perfil o un objeto de perfil incompleto rompen
la build en vez de llegarte.

:::caution[AWS Security Lake acepta OCSF ≤ 1.3]
Una fuente personalizada de Security Lake tope en **OCSF 1.3 en Parquet**, así que
los eventos `ai_operation` 1.8.0 **no** aterrizan ahí tal cual (comprobado el
2026-07-24). Hasta que exista un emisor de bajada a 1.3, enruta hacia Security
Lake con una transformación propia o usa otro destino. Es un hueco declarado, no
un descuido.
:::

## Proyecciones que no son sobres

Dos limitaciones honestas, ambas conviene conocerlas antes de apuntar un colector:

- **`otlp` es la petición enviable en todas las superficies; `otlp_log_record` es la
  proyección suelta.** Desde el remapeo del catálogo de formatos, una línea de EVENTO
  `otlp` es una petición de export OTLP/HTTP JSON completa
  (`ExportLogsServiceRequest`) allí donde el token se acepta —export del ledger,
  conectores de salida, push de eventing—, con la identidad de recurso y el scope de
  instrumentación que el colector necesita. `otlp_envelope` es un alias exacto, byte
  a byte, de `otlp` en todas las superficies, conservado porque esa grafía fue la
  primera en traer el sobre: los dos no difieren nunca. La proyección de un LogRecord
  por línea —un objeto JSON por línea, para consumo en fichero/NDJSON— sigue
  existiendo bajo su nombre honesto, `otlp_log_record`, y solo en el export pull del
  ledger: una línea LogRecord suelta no es un cuerpo enviable a `/v1/logs`, así que
  las superficies push deliberadamente no la ofrecen. Tres detalles, porque si no
  cuestan una tarde: la ÚLTIMA línea del pull es el marcador
  `{"export_complete":true,…}` de Olivares y **no** es una petición, así que un bucle
  que envíe todas las líneas debe saltarla — filtra por ESTRUCTURA, p. ej.
  `jq -c 'select(has("resourceLogs"))'`, nunca por substring: un evento cuyo actor u
  objetivo contenga `export_complete` desaparecería con un `grep -v`, y eso es borrar
  evidencia, no saltarse un marcador; un sink de push debe apuntar a la URL exacta
  `/v1/logs` del colector, porque el endpoint se envía tal cual; y el sink HTTPS
  genérico da por entregado cualquier 2xx sin leer la respuesta de éxito parcial del
  colector — el **conector de logs OTLP** sí la lee. `otlp_log_record` conserva los
  bytes exactos que producía el token `otlp` de antes del remapeo en el dominio
  normal de tiempo — el tiempo cero y cualquier instante desde la época hasta
  `2262-04-11T23:47:16.854775807Z`. Fuera de ahí la compatibilidad de bytes NO está
  garantizada, y donde difieren es una corrección: una fecha anterior a la época
  producía antes un valor negativo en un campo que OTLP declara sin signo, una entre
  los techos con y sin signo ahora lleva su verdadero valor sin signo, y una fecha
  posterior a `2554-07-21T23:34:33.709551615Z` ahora se codifica como desconocida
  (`0`) en vez de un valor desbordado — entre ellos los positivos pequeños que se
  leen como principios de 1970. En entradas aisladas que desbordan a cero, los bytes
  viejos y nuevos coinciden. Dos notas de actualización dichas claras: el *fichero*
  del pull sigue siendo NDJSON (una petición por línea más un marcador de fin), no
  una sola petición; y una suscripción de eventing almacenada cuyo formato se
  escribió exactamente `otlp` antes del remapeo entrega ahora el sobre donde antes
  entregaba una línea suelta — el motor registra un aviso estructurado por cada
  suscripción así, y los metadatos de auditoría anteriores al remapeo se leen con el
  significado antiguo del token.
- **La extensión de traza de OWASP Agentic AI Security** viaja bajo el contenedor
  `unmapped` de OCSF, que es la ubicación que prescribe su especificación (v0.1,
  public preview). No es un conjunto de atributos OCSF de primera clase, y la
  validación de esquema cubre solo su ubicación.

## Hallazgos como SARIF

Los hallazgos de gobierno se exportan como **SARIF 2.1.0 Errata 01** para un
consumidor de code scanning:

- `GET /v1/m/security/findings/export?format=sarif` — los mismos filtros que el
  listado de hallazgos, con un tope de resultados y una cabecera honesta de
  truncación cuando se alcanza.
- `olivares findings export` — el mismo export desde la CLI, escrito de forma
  atómica con permisos `0600`.

El run declara la base de URI contra la que resuelven sus localizaciones, lleva un
`partialFingerprints.primaryLocationLineHash` estable por hallazgo para que un
consumidor deduplique en vez de re-alertar, y se niega a emitir un resultado con
un rule id vacío o un level fuera del enum: esas son las dos cosas por las que un
consumidor rechaza el fichero entero, y enterarse al subirlo es peor que enterarse
aquí.

Los hallazgos cuyo sujeto no es un fichero versionado reciben una URI sintética de
localización. El run sigue siendo válido e ingerible, pero GitHub solo renderiza
alertas para URIs que casan con un fichero del checkout, así que un detector que
quiera anclaje en GitHub debería fijar la URI de artefacto explícitamente.
