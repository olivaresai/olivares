---
title: "Módulo I — inventario y descubrimiento"
description: >-
  Descubrimiento pasivo y catalogación de agentes, sesiones, servidores MCP,
  skills, herramientas, modelos, proveedores e identidades no humanas observados
  en el estate. Cómo se materializan las entidades a partir de observaciones,
  qué registran el catálogo y la procedencia, cómo funciona la frescura durable
  y cuáles son los límites.
---

El módulo I es el **catálogo de entidades observadas** del estate: un inventario
pasivo, dirigido por el bus, de agentes, sesiones, instancias de Claude Code,
servidores MCP, skills, herramientas, recursos, modelos, proveedores e
identidades no humanas que los conectores han nombrado de hecho. Descubre
*escuchando*, nunca sondeando. Registra relaciones, identificadores y vitalidad
— no payloads — y **no** es un censo de todo lo que existe. Esta página es la
referencia de lo que el catálogo contiene, de cómo funcionan la procedencia de
observación y la frescura durable en el desarrollo de la próxima versión, y de lo
que el módulo deliberadamente no afirma.

La procedencia de observación y la frescura durable están aceptadas para una
composición de desarrollo acotada. Son capacidades de desarrollo para
la próxima versión, no un release publicado, un RC completo ni un despliegue.

## Qué materializa

Los conectores emiten **observaciones**, no entidades. Publican hechos
normalizados [`edge.observed`](/es/reference/events/) y
[`cost.sampled`](/es/reference/events/) en el bus de eventos; las entidades que
estos implican nunca se envían. El módulo I **materializa** la entidad de núcleo
que cada observación nombra a partir de su referencia natural: una
`session`/`agent`/`identity` de origen, un servidor MCP, una herramienta, un
recurso, una skill y — a partir de las muestras de coste — un proveedor y un
modelo (descubiertos, **sin precios**; de eso se encarga FinOps). El inventario
se suscribe a esos dos tipos de evento; los hallazgos en vivo corresponden al
[módulo II](/es/reference/modules/ii-sessions/).

El find-or-create de la entidad núcleo por clave natural evita duplicar el
alias de catálogo bajo entrega de al-menos-una-vez en el modelo actual de un
solo escritor. Dos fuentes pueden conservar observaciones distintas que
comparten ese alias; compartirlo no prueba que sean la misma cosa física ni
transfiere propietario, workspace o grants. Un identificador de evento nuevo
con el mismo payload es un recibo **nuevo** — si eso es otra actividad queda
desconocido. El `occurrence_count` del catálogo cuenta **entregas**, incluido
el replay del mismo identificador de evento, no actividades distintas.

## Procedencia de observación

Además del alias de catálogo, el módulo guarda una proyección aditiva, acotada
al tenant, de cómo llegó una observación: un recibo con clave el identificador
de evento, una fila de miembro por entidad materializada (referencia nativa y,
cuando el snapshot de registro es válido, una identidad observacional estable)
y una fila de conflicto cuando el mismo identificador llega después con hechos
proyectados distintos. Se conserva el recibo original; el conflicto se registra
en lugar de sobrescribirlo. Un identificador ausente obtiene solo identidad de
almacenamiento y nunca se deduplica por payload. El replay del mismo
identificador y los mismos hechos refresca el contador legacy de entregas del
catálogo sin volver a resolver aliases. La consola puede listar los recibos
almacenados de una entidad de catálogo en
`GET /v1/m/inventory/entities/{kind}/{id}/observations` con el permiso
tenant-wide existente `inventory:catalog:read`: un item por recibo distinto en
orden ascendente de id de recibo, páginas de 25 como máximo. Cada item es una
instantánea histórica de registro en la recepción (no el registro o la salud
actuales de la fuente), el instante de ocurrencia declarado por la fuente
cuando lo declaró, la primera y la última recepción de los hechos conservados,
las entregas con hechos coincidentes y si se retiene una reentrega conflictiva.
Una página vacía no prueba que la entidad nunca se observara. `has_more`, una
lista vacía y el total del catálogo no son cobertura. La frescura del catálogo
en la ficha de entidad procede de un point read vigente con éxito; es una
lectura distinta del historial. La cobertura por fuente/familia y el estate de
referencia C4 siguen abiertos.

## Su contrato y entidades

El módulo registra `inventory.catalog_entry` — una capa de descubrimiento
adjunta a cada entidad de núcleo materializada. Registra *cómo* se encontró
algo, no *qué* hizo: fuentes de señal, hosts cuando se conocen, marcas de
primera y última observación, un `occurred_at` opcional declarado por la
fuente (se omite si no declaró ninguno; es distinto de `last_seen`, que es
cuándo **esta plataforma** lo vio), un contador de ocurrencias y un `status`
de vitalidad `active` o `stale`. La superficie de lectura es pequeña y de
solo lectura: un `summary` con conteo por tipo y fuente, un listado paginado
de `entities` filtrable por tipo y estado, una vista de detalle de entidad
única y el historial de observaciones por entidad descrito arriba. Cada lectura exige un permiso de lectura con namespace, acotado al
tenant (basta el nivel de visor más bajo). Las escrituras de catálogo y
procedencia son de alta frecuencia y no se auditan por escritura.

La frescura durable mantiene una segunda capa, `inventory.freshness_sweep`:
como máximo una fila perezosa por tenant, con el corte del ciclo abierto, el
cursor opaco del catálogo y el último corte cuyo ciclo terminó. Ese último
corte registra un **barrido** terminado, no que alguna fuente se enumerara por
completo. Las formas completas viven en la
[referencia del bus de eventos](/es/reference/events/) y en las interfaces
tipadas del producto.

## Frescura durable

Un barrido periódico marca una entrada del catálogo como `stale` cuando esta
plataforma no la ha visto desde el corte del ciclo, y la vuelve a `active` en
el momento en que reaparece. Los umbrales por defecto son 30 minutos de
silencio y una cadencia de 5 minutos; son valores por defecto del módulo, no
una superficie YAML de operador en el binario actual (el composition root
registra el módulo con la configuración de host vacía).

El módulo **no** enumera tenants y **no** cae a los tenants que este proceso
haya observado. Un adaptador privado de composición lee el directorio durable
de organizaciones y devuelve solo **candidatos**: tenants de negocio activos
que la residencia de esta instancia sirve — nunca la partición system, nunca
un org suspendido, nunca un tenant anclado a una región que esta instancia no
sirve. Cada turno abre de todos modos la escritura tenant-scoped ordinaria del
store, de modo que residencia, retirada de servicio y liderazgo se vuelven a
comprobar; un tenant cuyo estado cambió entre el snapshot y su turno se
deniega ahí.

Sin un directorio autoritativo el barrido falla de forma visible y no muta
nada. En PostgreSQL esa autoridad necesita el pool de lectura admin
`NOSUPERUSER` `BYPASSRLS` ya usado para lecturas de directorio entre tenants
(`--admin-dsn`). Sin ese pool atestado el barrido no trata las filas que el
rol de aplicación alcanza a ver como un directorio completo. SQLite no tiene
ese requisito de pool. Las observaciones nuevas siguen persistiendo cuando el
handle de datos está cableado; un barrido que no puede correr no desuscribe la
ingesta.

Cada pase da a cada candidato **un turno**. Cada turno clasifica como máximo
una página de 1.000 entradas activas anteriores al corte y guarda el cursor
para que un reinicio continúe sin un evento nuevo. La enumeración y cada turno
de tenant tienen **presupuestos de tiempo separados**, de modo que un tenant
lento no gasta el del resto del pase. Un fallo local no impide que los tenants
posteriores reciban un turno **en un proceso vivo**. No hay cursor global de
tenants, ni fairness bajo reinicios repetidos, ni una afirmación de alta
disponibilidad.

Si un turno no termina con éxito, esa página no se cuenta como marcada. Página
y progreso permanecen juntos; el turno siguiente relee lo último almacenado —
la frontera anterior, o una ya avanzada — y continúa desde ahí. El producto no
reconstruye el progreso desde memoria.

## Qué consume y qué produce

El módulo I es un **consumidor**. Se suscribe a `edge.observed` y
`cost.sampled` y escribe su capa de catálogo, las entidades de núcleo que
deriva y las filas aditivas de procedencia y frescura anteriores. No emite
eventos propios y no expone ninguna superficie de actuación. Las referencias y los hechos de observación seleccionados que persiste se
almacenan tal como llegan; el módulo no vuelve a sanear esos valores. La
minimización de datos corresponde al productor y debe resolverse antes de
publicar observaciones; este módulo no puede certificarla para todos los
productores. Una referencia persistida puede conservar una query string,
credenciales u otros datos sensibles si un productor los publicó. El
inventario no recopila por iniciativa propia el contenido bruto completo de
la actividad, y no añade payload, secreto, prompt, comando ni SQL en crudo
propios.

:::caution[Límites honestos]
- **El inventario no es dueño del grafo de acceso.** Desde la decisión A
  (2026-06-03), el módulo III (el access map) es el **único escritor** del
  `AccessEdge` de lectura/escritura y el único dueño de la topología y del
  diff Permitido-vs-Observado. El inventario descubre y cataloga las
  *entidades* que un edge nombra; ya no registra el edge en sí, y no sirve
  ninguna ruta de topología. El grafo se puebla únicamente cuando el módulo
  III está cableado en el arranque.
- **El descubrimiento es solo tan completo como las señales.** Una entidad
  existe en el catálogo solo si algún conector la observó. La ausencia del
  catálogo **no** es prueba de ausencia en el estate. Completar un barrido de
  frescura no es cobertura de fuente, completitud del descubrimiento, salud
  formal ni prueba de que una entidad fue retirada.
- **La vitalidad es obsolescencia, no salud.** `stale` significa que esta
  plataforma no ha observado la entidad desde el corte, nada más. Reaparecer
  la vuelve a `active`. El silencio de una sesión es normal, y la salud/SLA
  formal corresponde al módulo XXII. El barrido nunca muta el propio ciclo de
  vida de la entidad de núcleo.
- **Sin detalles fabricados.** El módulo almacena identificadores, relaciones
  y contadores de vitalidad — nunca el contenido bruto completo de la
  actividad recopilado por iniciativa propia — y no añade payloads, secretos,
  prompts, comandos ni SQL propios. Las URI de recurso recibidas pueden
  persistirse como referencias. El inventario no vuelve a sanear esos valores
  ni certifica la ausencia de query strings, credenciales o PII en ellos.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde encaja el módulo I y la división honesta de Actuate.
- [Módulo III — el access map](/es/reference/modules/iii-access-map/) — el único dueño del grafo R/RW y del drift.
- [Referencia del bus de eventos](/es/reference/events/) — los eventos `edge.observed`, `cost.sampled` y `finding.reported` que consumen el inventario y las sesiones.
- [De cero al grafo](/es/tutorials/zero-to-graph/) — poblando el catálogo y el mapa sobre el estate de demostración.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — el motor, las capas y el bus.
