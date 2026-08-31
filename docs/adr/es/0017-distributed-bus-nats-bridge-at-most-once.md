> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0017: Bus de eventos distribuido = fan-out local in-proc + puente NATS, NATS core con entrega como mucho una vez (sin JetStream en v1)

- **Status:** accepted (modifica la línea de semántica de entrega de la ADR-0006) —
  **extendida por la ADR-0021**, que entrega el backend JetStream at-least-once como
  complemento enterprise cerrado y resuelve el problema de seguridad ante duplicados
  mediante deduplicación en la frontera del bus (no mediante la revisión de idempotencia
  por suscriptor prevista más abajo); este puente NATS Core ABIERTO se mantiene
  at-most-once y sin cambios.
- **Fecha:** 2026-06-12
- **Responsables de la decisión:** diseño sometido a presión por un panel adversarial de 3 lentes antes de la implementación
- **Referencias:** `docs/contracts/S02-sdk-runtime-eventbus.md §4`,
  `core/eventbus/natsbus`, censo de idempotencia de suscriptores (recon, 2026-06-12)

## Contexto y planteamiento del problema

La ADR-0006 dejó el bus in-proc con una ranura para NATS. La HA llegó, así que el multinodo
existe — y el bus no cruza nodos: un evento publicado en un standby (fuentes en segundo plano,
barridos de identidad) nunca llega al procesamiento del leader; la captura de la plataforma de
eventing —la frontera de durabilidad— lo pierde en silencio. Había que responder dos preguntas
con evidencia, no con valores por defecto: **(a)** ¿el backend distribuido reemplaza la ruta de
entrega local o la puentea?, y **(b)** ¿NATS core (como mucho una vez, at-most-once) o JetStream
(al menos una vez, at-least-once)?

La ADR-0006 registró el bus como "al menos una vez; los consumidores deduplican". **Esa línea
era incorrecta como descripción de la implementación**: el bus in-proc es como-mucho-una-vez
(los errores de handler se registran, no se reintentan; los eventos en cola se descartan al
cerrar — `core/eventbus/inproc.go`), el contrato S02 §4 documenta backpressure bloqueante SIN
reentrega, y `modules/eventing/capture.go` afirma que "el bus en sí es como-mucho-una-vez (S02)
y la repetición (replay) empieza EN la captura". La frase "al menos una vez" de la ADR-0006
describía la reemisión a nivel de fuente (`Gather` se reejecuta), no la entrega del bus.

## Factores de la decisión

- El censo de suscriptores del 2026-06-12: la mayoría de los ~17 suscriptores del bus NO son
  seguros ante duplicados (eventing captura dos veces, security/notify persisten o envían
  duplicados, los pliegues (folds) de count/aggregate se inflan). La entrega al-menos-una-vez
  HOY sería una regresión semántica disfrazada de mejora.
- La garantía escrita de S02 §4 — Publish bloquea bajo saturación, "perder eventos en silencio
  sería peor que estrangular a un publicador" — es portante: `olivares_ingest_duration_seconds`
  está documentado como EL SLI de backpressure (docs/17 §1.4) y el runbook de
  collector-backpressure dice "no se pierde ningún evento — el bus bloquea en lugar de
  descartar". Enrutar la ruta caliente local a través de un servidor invertiría ese contrato en
  el 100% del tráfico de producción (el LB drena los standbys).
- El tráfico que el backend existe para rescatar (eventos originados en standby) es la ruta de
  BAJO volumen; la ruta local es la caliente. El diseño no debe sacrificar la ruta caliente por
  la fría.

## Opciones consideradas

- **A. Transporte NATS puro** — cada publish/subscribe atraviesa el servidor; una sola ruta de código.
  Rechazada: invierte S02 §4 en la ruta local (descartes silenciosos por consumidor lento donde
  el contrato promete bloqueo, sin pérdida), añade ventanas de pérdida por reinicio/reconexión
  del servidor a la entrega del mismo nodo, y degrada el significado del SLI de ingesta.
- **B. Híbrida: fan-out local in-proc + puente NATS con NoEcho (ELEGIDA).**
- **C. JetStream (al menos una vez)** — rechazada para v1: el censo muestra que los suscriptores
  no son seguros ante duplicados; JetStream se convierte en trabajo viable solo DESPUÉS de una
  pasada de idempotencia por los suscriptores (registrada como la ruta de mejora explícita más
  abajo).

## Resultado de la decisión

Opción elegida: **B + NATS core**. `core/eventbus/natsbus` integra el bus in-proc: Publish
hace fan-out localmente primero (toda garantía de S02 §4 intacta — backpressure bloqueante,
cero pérdida local, aislamiento de pánicos, sin códec en la ruta caliente), y luego puentea el
evento a NATS en modo best-effort. La conexión del puente activa **NoEcho**, de modo que su
única suscripción comodín recibe solo eventos de ORIGEN REMOTO, que rematerializa (oneof del
proto `Event` congelado para las tres cargas de observación, JSON + registro de decodificadores
para los tipos definidos por módulo) e inyecta en el fan-out local — sin doble
entrega, preservando el orden por publicador entre tipos (una conexión por nodo, una
suscripción ordenada).

**Semántica entre nodos, documentada con honestidad: como mucho una vez.** Ventanas de pérdida:
reinicio del servidor NATS (sin persistencia), desbordamiento del búfer de reconexión / nunca
reconectado ("en búfer ≠ entregado"), y descartes por consumidor lento cuando el búfer pendiente
de la suscripción del puente se llena — cada una contabilizada
(`olivares_eventbus_bridge_*`) y alertable, nunca silenciosa. HA: los eventos remotos se
**inyectan solo en el leader** (`SetInjectGate(store.Leader().Active)`), lo que elimina la clase
de efectos secundarios del lado standby (notificaciones duplicadas, hallazgos derivados
duplicados, tormentas de logs ErrNotLeader) en la frontera del bus; el solapamiento de failover
de ≤2s puede inyectar por duplicado, absorbido por el índice único `(tenant_id, event_id)` de la
captura de eventing. La configuración (`OLIVARES_BUS_CONFIG`) es fail-boot-closed (falla
cerrando el arranque): un nodo que cayera en silencio a in-proc correría particionado.

### Consecuencias

- **Bueno:** las observaciones originadas en standby llegan al leader (cerrando la brecha entre nodos); el binario por defecto de un solo nodo
  no se ve afectado byte a byte; la semántica de entrega local no cambia; la pérdida entre nodos
  se contabiliza, no es silenciosa.
- **Malo / compromisos:** la ruta entre nodos solo la ejercita el tráfico originado en standby —
  su ruta de códec/inyección lleva tests de integración dedicados (nats-server integrado)
  precisamente porque producción la ejercita rara vez; los eventos puenteados añaden una
  codificación por publicación en los nodos CON el puente configurado.
- **Neutral:** JetStream sigue siendo la ruta de mejora a al-menos-una-vez, condicionada a una
  pasada de idempotencia de suscriptores (el censo es la lista de trabajo); la interfaz `Bus` no
  ganó nada — Stats y las suscripciones con nombre son interfaces de extensión opcionales.

## Por qué se rechazaron las alternativas

Véanse los factores: A invierte un contrato escrito en la ruta caliente para simplificar la
fría; C introduce duplicados en suscriptores que demostrablemente los manejan mal.
