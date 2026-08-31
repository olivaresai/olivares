> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0025: El ledger reserve→commit/release de FinOps cierra el TOCTOU de presupuesto/límite de gasto

- **Status:** accepted
- **Date:** 2026-07-17
- **Deciders:** Fran Olivares
- **References:** ADR-0023 (per-group window and spend ceilings — its FinOps budget dimensions are what this reservation ledger admits against), ADR-0001 (store abstraction — SQLite + Postgres, one descriptor), ADR-0009 (append-only hash-chained audit).

## Contexto y planteamiento del problema

`finops.CheckBudget` y `finops.CheckSpendLimit` son comprobaciones de admisión previas y de
solo lectura: agregan el read-model de costes y responden a «¿está esta petición dentro de los
presupuestos/límites aplicados que la delimitan?». Entre esa respuesta y el momento en que se
registra el gasto real (la ingesta `CostSampled` → `onCost` del conector) hay una ventana.
**N peticiones concurrentes leen todas el mismo estado previo al gasto, todas pasan y en
conjunto revientan el límite**: un doble gasto check→act (TOCTOU). Una pasada previa de
endurecimiento fail-closed cerró la degradación `Truncated` y la postura de disponibilidad,
pero la carrera en sí siguió abierta.

Una solución correcta debe hacer **atómico** «comprobar el límite y luego consumir el margen»,
y debe serlo **entre réplicas en Postgres**, no solo dentro de un proceso; por eso un mutex a
nivel de proceso no es aceptable.

## Motivadores de la decisión

- **El límite debe consumirse en la admisión, no en la liquidación.** La única forma de que N
  peticiones concurrentes no puedan pasar todas es que cada admisión reste de forma duradera
  su propio margen antes de que la siguiente lea.
- **Entre stores, un único contrato.** El mismo mecanismo debe sostenerse en SQLite (embebido,
  un único writer) y en Postgres HA (múltiples conexiones, READ COMMITTED). Usar las
  primitivas de atomicidad del propio store, nunca un lock en memoria.
- **El coste real solo se conoce a posteriori.** Los tokens de salida (y por tanto el coste) se
  desconocen antes de la llamada. La admisión debe reservar una *estimación* y reconciliar al
  completarse.
- **Caducidad honesta.** Un llamante que ha caído no debe retener margen para siempre, y
  recuperarlo nunca debe contar dos veces.
- **Sin un nuevo motor de esquemas.** Reutilizar el descriptor `ExtensionRegistry` del módulo +
  la concurrencia optimista del repo genérico.

## Resultado de la decisión

Un **ledger de reservas dinámico** (`finops.budget_reservation`, tabla
`finops_budget_reservation`) con un ciclo de vida reserve→commit/release. `ReserveBudget` /
`ReserveSpendLimit` reservan atómicamente la estimación frente a cada política aplicada que
delimita la petición; `CommitReservation` la liquida con el coste real; `ReleaseReservation`
devuelve el margen en caso de fallo. El límite en todas partes (`CheckBudget`, `budgetStatus`,
`evaluateBudgets`) es ahora
`committed_spend + static ReservedMicroUSD + Σ(active, unexpired reservations)`.

Esto es **distinto del** `budgetSpec.ReservedMicroUSD` **estático** preexistente (un compromiso
de capacidad de Priority-Tier que cuenta para el límite). Ambos se suman en `effective`; este
ADR añade la línea *dinámica y por petición*.

### 1. Atomicidad: un `seq` monótono por ámbito bajo un índice UNIQUE (sin lock de proceso)

Cada reserva lleva un `seq` monótono por **(policy, period_start, scope_key)**, bajo el índice
UNIQUE `finops_budget_reservation_seq_uniq (tenant_id, policy_ref, period_start, dim_key, seq)`.
Reservar = leer `max(seq)`, leer el gasto actual + las reservas activas y, si hay espacio,
`INSERT` con `seq = max+1`.

- Dos reservadores concurrentes calculan el **mismo** `seq` siguiente; el índice UNIQUE deja
  que se confirme exactamente **un** `INSERT` y asigna al otro `store.ErrConflict`
  (`mapWriteErr`). El perdedor **reintenta la transacción completa** y vuelve a leer el estado
  ya confirmado. Esto serializa reservar-comprobar-insertar **sin ningún lock de proceso**.
- **SQLite:** `MaxOpenConns=1` ya serializa cada transacción sobre el único writer, por lo que
  la reserva es atómica por sí misma; el índice de seq es la red de seguridad redundante.
- **Postgres READ COMMITTED (el caso que soporta la carga):** conexiones distintas no ven las
  filas no confirmadas de las demás, así que es la colisión de seq la que fuerza el reintento.
  **Invariante de orden:** la reserva lee `max(seq)` **antes** que la suma reservada e inserta
  con *ese* seq, de modo que una inserción correcta (sin colisión) demuestra que el seq leído
  era el verdadero máximo confirmado y, por tanto, que la suma (leída estrictamente después)
  vio todas las reservas anteriores. Invertir las dos lecturas reabriría la carrera (una suma
  obsoleta emparejada con un seq nuevo y sin colisión admitiría de más). Demostrado por
  inducción: la k-ésima inserción correcta vio las k-1 reservas anteriores, de modo que se
  admiten exactamente `floor(headroom/estimate)`.

Las peticiones con varias políticas reservan todos los objetivos en **una** transacción (todo
o nada): la denegación de un objetivo posterior revierte las inserciones anteriores; block
prevalece sobre throttle.

### 2. Granularidad de la reserva: por política aplicada, con clave por ámbito

Una **fila de reserva por cada política aplicada con la que coincide la petición**, con clave
`(policy_ref, period_start, scope_key)`:

- **Presupuestos:** `scope_key` = la clave de dimensión del presupuesto (`""` para el global):
  un único ámbito por política. Se reserva en las 17 dimensiones no de grupo con las que
  coincide la petición (el caso habitual por petición:
  model/provider/agent/workspace/identity/api_key/…).
- **Límites de gasto por seat:** `scope_key` = el **actor**, de modo que un tope procedente de
  una política de org/grupo reserva el margen de cada seat de forma **independiente**, en
  coherencia con la semántica por actor de `CheckSpendLimit`.
- **Los presupuestos de dimensión de grupo (`user_group`/`agent_group`) NO se reservan aquí.**
  Su gasto es un fan-out de miembros sobre `actor`/`agent_ref` sin columna de grupo en el
  read-model; una reserva con fan-out es un diseño mayor. Siguen aplicándose mediante la ruta
  preventiva existente de `CheckBudget`. (Seguimiento abierto: véase más abajo.)

### 3. Estimación: reservar una estimación y reconciliar en el commit

La admisión reserva `estimateMicroUSD` (la estimación a priori de la costura; por ejemplo, a
partir de `count_tokens` sobre el prompt más una asignación de salida `max_tokens`). Al
completarse, `CommitReservation(handle, actualMicroUSD)` marca el valor real y pasa la fila a
`committed`, lo que la elimina de la suma activa; el gasto real se registra por separado
mediante `onCost`. Si la estimación fue **demasiado baja**, el presupuesto puede superarse
transitoriamente en `actual − estimate` para esa única petición: acotado y autocorregible en
cuanto se registra el gasto real. **La política de estimación por defecto es una decisión de
producto (véase más abajo); el mecanismo es agnóstico respecto a la estimación.**

**Orden:** ingerir el gasto real y *después* confirmar la reserva, para que el límite nunca
cuente de menos transitoriamente durante la liquidación.

### 4. Caducidad: un predicado, nunca un decremento

La suma de lo reservado activo filtra `state = active AND expires_at > now`. Por tanto, una
reserva caducada **deja de contar en el instante en que expira**: no hay contador que
decrementar, así que **contar dos veces es estructuralmente imposible**.
`SweepExpiredReservations` solo marca el estado terminal `expired` para observabilidad/GC; la
corrección no depende de que se ejecute. El TTL (`reservationTTL`, **5 min** por defecto) es el
respaldo ante caídas para un llamante que murió entre la reserva y el commit/release; debe
superar la actuación gobernada más lenta para que nunca se descarte una petición aún en curso.

### Consecuencias

- **Positivo:** el doble gasto queda cerrado atómicamente en ambos motores; la corrección es
  aditiva (una nueva tabla de descriptor: `applyModuleTables` la crea en BD nuevas y en BD
  existentes in situ; no se toca ninguna migración existente); `CheckBudget`/estado/alertas
  reflejan ahora las reservas en vuelo, de modo que la denegación previa, la señal de tope
  estricto y el DTO de estado coinciden.
- **Coste:** una reserva son dos escrituras (reservar + liquidar) frente a una comprobación de
  solo lectura; en la ruta caliente esto supone unas pocas transacciones pequeñas adicionales,
  insignificantes frente a la llamada de inferencia protegida por la reserva.
- **Latente hasta que se cablee:** el ledger solo actúa cuando las costuras de actuación llaman
  a `ReserveBudget`/`Commit`/`Release` (con una estimación) en lugar del `CheckBudget` de solo
  lectura. Hasta entonces, lo reservado dinámicamente es 0 y el comportamiento no cambia.
  Cablear el proxy de inferencia / el gate HITL y elegir la estimación por defecto es la
  integración pendiente.

## Preguntas abiertas (producto)

1. **Estimación por defecto.** ¿Cuál es la estimación a priori cuando la costura no tiene
   ninguna? Opciones: `count_tokens(prompt)` + la asignación de salida `max_tokens`
   configurada a la tarifa del modelo; un mínimo fijo por petición; o el coste histórico p95
   por modelo. Subestimar debilita la garantía; sobreestimar aplica throttle antes de tiempo.
2. **TTL.** ¿Son 5 min el respaldo ante caídas adecuado, o debería seguir el tiempo máximo de
   completado del modelo / ser por surface?
3. **Reserva de presupuestos de grupo.** ¿Deberían reservarse también los presupuestos
   `user_group`/`agent_group` (fan-out de miembros), o es aceptable un enforcement solo
   preventivo para los límites de grupo?
4. **Postura ante el agotamiento de reintentos.** Al agotarse `maxReserveRetries` (64), la
   reserva falla en **abierto** (conforme al contrato de `CheckBudget`). Para un presupuesto
   `block` estricto, ¿debería la contención extrema fallar en **cerrado**?
