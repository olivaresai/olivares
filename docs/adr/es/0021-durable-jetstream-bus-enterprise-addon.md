> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0021: Backend duradero JetStream para el bus de eventos (al menos una vez + deduplicación en la frontera del bus) como add-on enterprise cerrado

- **Status:** accepted (extends ADR-0017's "JetStream remains the upgrade path")
- **Date:** 2026-06-24
- **Deciders:** Fran Olivares (scale/reliability lever); design re-anchored against HEAD + a subscriber-idempotency re-census
- **References:** ADR-0017 (the at-most-once Core-NATS bridge), ADR-0020 (enterprise private-repo distribution),
  `LICENSING.md`, `enterprise/durablebus`, `core/eventbus/natsbus`

## Contexto y planteamiento del problema

ADR-0017 entregó el bus distribuido como fan-out local en proceso + un bridge
**Core-NATS, como máximo una vez**, y **rechazó explícitamente JetStream para v1** (opción C)
porque el censo de suscriptores del 2026-06-12 determinó que la mayoría no toleraba
duplicados: al menos una vez habría enviado duplicados a handlers que los gestionan mal.
Dejó JetStream como «la vía de actualización a al menos una vez, **condicionada a una
revisión de idempotencia de los suscriptores**».

Un plano de control de gobernanza no puede perder silenciosamente un evento que desencadena
una DECISIÓN. Con el bridge abierto, un finding.reported / cost.sampled perdido entre nodos
HA (reinicio del servidor, desbordamiento del búfer de reconexión, descarte por consumidor
lento) es una señal de enforcement omitida en silencio. El nivel enterprise de
escala/fiabilidad (palanca n.º 4) debe cerrar esta brecha para la clase de eventos de
enforcement, sin depender de la revisión de idempotencia por suscriptor prevista por
ADR-0017 (un nuevo censo confirmó que los suscriptores solo siguen siendo idempotentes
«**lo suficiente**»: por ejemplo, `modules/security` deduplica findings mediante un
**escaneo acotado de mejor esfuerzo**, no una garantía estricta: `observed.go`,
`anomaly.go`).

## Motivadores de la decisión

- **Resolver la no idempotencia en el BUS, no confiando en los handlers.** ADR-0017
  condicionó JetStream a hacer idempotente cada suscriptor. Es frágil (una invariante
  distribuida entre unos 17 handlers, que cualquier edición futura puede volver a romper) y
  nunca se completó. Una única deduplicación bajo control en la frontera del bus es la
  solución duradera: los suscriptores ganan durabilidad sin que cada uno tenga que ser
  correcto para siempre.
- **Sin rug-pull ni regresión en la ruta caliente.** Se mantiene la restricción esencial de
  ADR-0017: la ruta caliente local en proceso y el bridge Core-NATS abierto deben permanecer
  idénticos byte a byte en el binario community. La mejora debe ser ADITIVA.
- **Momento de monetización (ADR-0020).** La durabilidad/HA es una palanca del nivel
  enterprise. Se entrega como código cerrado tras el build tag `enterprise`, después de que
  la separación del repositorio privado convirtiera el tag en una frontera real.

## Opciones consideradas

- **A. Sustituir el bridge por JetStream para TODOS los tipos.** Rechazada: encamina
  observaciones de gran volumen tolerantes a pérdidas (edge/metric) por almacenamiento RAFT
  y cambiaría el comportamiento del bridge abierto (rug-pull).
- **B. JetStream duradero solo para la clase de ENFORCEMENT, integrando el bridge abierto
  para el resto (ELEGIDA).**
- **C. Tabla de deduplicación persistente por suscriptor en el store.** Rechazada para la
  Fase 1: una tabla solo enterprise rompe el gate de paridad de esquema open≡enterprise y
  una tabla abierta supone un cambio más pesado de lo que exige la garantía. En su lugar,
  el estado de deduplicación reside en JetStream KV (sin store ni cambio de esquema).

## Resultado de la decisión

Opción elegida: **B.** Un add-on cerrado `enterprise/durablebus`
(`//go:build enterprise`, `LicenseRef-Olivares-Commercial`) que **integra** el
`*natsbus.Bus` abierto y añade una ruta JetStream para el **conjunto de enforcement**
(`finding.reported`, `cost.sampled`, `guardrail.observed`, `approval.requested`,
`policy.changed`; modificable por el operador). Mecánica:

- **Espacios de nombres de subjects hermanos.** Los eventos duraderos se publican en
  `<durable_prefix>.<type>` (un stream JetStream, RAFT, réplicas ≥ 3), DISJUNTO del
  `<subject_prefix>.>` del bridge Core, de modo que cada tipo se entrega por exactamente un
  transporte, nunca por ambos. Se indica al bridge integrado que EXCLUYA el conjunto duradero
  del bridge Core (`natsbus.Options.BridgeExclude`, inerte en el binario abierto). Los tipos
  que no son de enforcement conservan el alcance como máximo una vez del bridge abierto (sin
  regresión).
- **La publicación confirma el PubAck** (`Nats-Msg-Id = event.ID`): un evento duradero se
  almacena de forma duradera o se expone el fallo; nunca se descarta en silencio. La ventana
  de duplicados del stream reduce un reintento / una publicación doble por failover a una
  única copia almacenada.
- **Consumidor duradero condicionado al líder** (ack explícito), vinculado al ascenso y
  detenido al descenso mediante un watcher `Active()` (el elector no expone OnDemote); su
  posición en el servidor sobrevive al failover. El enforcement se ejecuta una sola vez en
  todo el clúster.
- **Deduplicación por event.ID en la frontera de inyección**, en dos niveles: una ventana
  temporal en memoria (rápida, en el mismo nodo) y un bucket **JetStream KV** (replicado por
  RAFT, acotado por TTL, sobrevive a caídas/reinicios y deduplica entre nodos).
  LECTURA-antes-de-inyectar (suprime un duplicado) + REGISTRO-después-de-inyectar (para que
  una caída vuelva a inyectar en lugar de perder).

**Semántica honesta: al menos una vez, NUNCA exactamente una vez.** En condiciones normales
y moderadamente degradadas no se producen PÉRDIDAS (registro después de inyectar; una
publicación confirmada es duradera; el consumidor reanuda desde su posición confirmada). La
ÚNICA vía residual de pérdida está acotada por la retención: el stream conserva un mensaje
durante un máximo de `MaxAge` (72 h por defecto, `LimitsPolicy`), por lo que un evento
almacenado se descarta si NINGÚN líder lo drena durante más de `MaxAge`: pérdida total de
quórum / interrupción sin líder o con partición durante varios días. Esa ventana se hace
observable mediante el SLI `olivares_durablebus_stream_pending` (se puede alertar cuando un
backlog se aproxima a `MaxAge`), por lo que nunca es un descarte silencioso; el operador
aumenta `MaxAge` o restablece un líder para mantenerlo a cero. Un DUPLICADO solo es posible
en dos ventanas acotadas: el solapamiento de liderazgo ≤2 s y una caída abrupta entre la
inyección y el registro de deduplicación; ambas se absorben aguas abajo (el índice
`(tenant_id, event_id)` de la captura de eventos y la deduplicación por escaneo acotado de
security). El bridge abierto permanece como máximo una vez y sin cambios.

### Consecuencias

- **Bueno:** los eventos de enforcement sobreviven a la entrega entre nodos (al menos una
  vez) con una única garantía de deduplicación bajo control; el binario community es idéntico
  byte a byte (el add-on está ausente; la única costura abierta, `BridgeExclude`, es inerte);
  no hay cambio en el esquema del store (la deduplicación reside en JetStream KV) ⇒ la
  paridad de esquema permanece intacta; arranque con fallo cerrado (si no puede establecerse
  un backend duradero declarado, el arranque se aborta; un binario enterprise sin licencia
  se degrada de forma VISIBLE al bridge Core-NATS abierto, nunca silenciosamente a un único
  nodo).
- **Malo / compromisos:** la entrega duradera cuesta un round-trip a JetStream al publicar
  (PubAck) y una lectura KV al inyectar, aceptable para la clase de enforcement de volumen
  moderado; el operador puede reducir el conjunto duradero. Los eventos duraderos solo
  llegan a los suscriptores del líder (mediante el consumidor), por lo que las publicaciones
  duraderas de un nodo no se distribuyen localmente por fan-out (coherente con «enforcement
  solo en el líder»). El gate de licencia del bus se aplica al arrancar (instalar una
  licencia para activar la durabilidad exige reiniciar, a diferencia del límite de plazas
  aplicado en caliente).
- **Neutral:** la Fase 2+ de la palanca (escalera de DR, multirregión, silo/CMEK por tenant)
  es una hoja de ruta documentada (`enterprise/durablebus/doc.go`), NO implementada.

## Por qué se rechazaron las alternativas

A aplica un rug-pull al bridge abierto y penaliza la ruta caliente; C intercambia un pequeño
KV por un cambio en el esquema del núcleo que rompe el gate de paridad. B limita el cambio a
código cerrado y aditivo, y resuelve el problema de seguridad ante duplicados de ADR-0017 en
la frontera del bus en lugar de hacerlo mediante la revisión por suscriptor que nunca se
completó.
