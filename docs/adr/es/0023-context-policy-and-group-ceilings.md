> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0023: Enforcement de la política de contexto en los tres puntos de tránsito, con límites de ventana y gasto por grupo

- **Status:** accepted
- **Date:** 2026-07-08
- **Deciders:** Fran Olivares
- **References:** ADR-0022 (source scoping by subject axis — its subject resolver and `most-specific` precedence are mirrored here), ADR-0009 (append-only hash-chained audit), ADR-0003 (RRW map — permitted vs observed).

## Contexto y planteamiento del problema

La política de contexto (tamaño de ventana y estrategia de compactación) se persistía como
datos gobernados, pero **ningún consumidor la aplicaba jamás**: el consumidor prometido por
un comentario del código no existía, por lo que la política era metadatos muertos. Por otra
parte, los límites de tokens del proxy de inferencia eran solo **por tenant / por petición**,
y FinOps incorpora una dimensión de presupuesto `team` que es **detectiva y fail-open**. No
había forma de indicar «este grupo de usuarios (o agentes) puede consumir como máximo esta
ventana / este gasto» y conseguir que se aplicara.

La visión del producto exige dos cosas que la política almacenada pero sin usar no podía
ofrecer:

1. **La política de contexto DECIDE en los tres puntos de tránsito** donde la plataforma
   entra en contacto con una petición a un modelo: el runtime de sesión, el proxy de
   inferencia inline y la recuperación de conocimiento, en vez de permanecer como datos
   inertes.
2. **Límites aplicados por grupo** — `user_group` y `agent_group` — tanto para la **ventana
   de contexto** como para el **gasto**, con denegación cerrada donde la política lo exija y
   con **degradación honesta** (nunca un recorte silencioso ni un permiso silencioso).

## Motivadores de la decisión

- **Coherencia con el scoping de fuentes (ADR-0022).** Reutilizar el mismo vocabulario de
  subjects y la misma precedencia `most-specific` para que los operadores razonen sobre la
  gobernanza del contexto exactamente como lo hacen sobre el scoping de fuentes: sin un
  segundo motor de decisiones y con una superficie de ataque pequeña.
- **Un límite debe ser realmente un límite.** Un límite numérico que un ámbito más
  específico pueda *relajar* no es un límite; el objetivo central son los «límites
  aplicados».
- **Degradación honesta.** Cuando la plataforma no pueda contabilizar algo por completo
  (gasto aproximado por grupo), debe fallar en la dirección *segura* e indicarlo: nunca
  denegar erróneamente ni permitir en silencio.
- **Reutilizar las primitivas existentes.** Preferir el ledger de auditoría, la atribución
  de costes por subject existente y la ruta de denegación del proxy existente a nueva
  maquinaria transversal.

## Resultado de la decisión

### 1. Composición de `Apply`: campos cualitativos por el más específico, mínimos de seguridad restrictivos, `max_tokens` por MIN

`Module.Apply` (`modules/knowledge/context.go:263`) resuelve la política efectiva de una
petición:

- Los campos **cualitativos** se resuelven mediante **el más específico gana** (`strategy`),
  de forma coherente con ADR-0022.
- Los **mínimos de seguridad** se componen de forma **restrictiva**: `forbid` es absoluto;
  `redaction_required` se compone mediante OR; `excluded_sources`, mediante unión.
- **`max_tokens` se compone mediante MIN** (el más restrictivo; campo en
  `context.go:62,73`, acotado en `context.go:124`). Este es el refinamiento deliberado para
  el límite numérico: un límite que un ámbito más específico pudiera elevar no sería un
  límite. El comportamiento es reversible en unas dos líneas si alguna instalación
  prefiriera alguna vez el más específico para el límite.

### 2. Identidad del agente en el proxy: cerrar el residual alcanzable (E3-lite) y aplazar el resto con honestidad

La credencial WIF de inferencia de sesión (`sk-ant-oat`) **no** transita por el proxy de
inferencia inline, que solo autentica los tokens `olvs` / `olvk` propios de la plataforma.
Cerrar por completo la federación de identidad de agentes para el tráfico de *sesión*
exigiría rediseñar la credencial de inferencia (varios días, parte de la postura de emisión
WIF efímera) y se **aplaza a un esfuerzo específico (E3-full)**.

La parte alcanzable se cierra ahora (**E3-lite**): `authToken` propaga `AgentRef` →
`AgentIdentity`, y el resolver de actor-scope de models respeta el **principal autenticado**
en lugar de un valor declarado por el llamante (corrección de un bug), lo que habilita el
eje `agent_group` en el proxy para llamantes agent-on-behalf-of. La referencia del agente se
toma siempre de la credencial autenticada, nunca del cuerpo de la petición
(`context.go:278-279`, `query.go:110-111`).

### 3. Límite de GASTO por grupo: preventivo, fail-open por naturaleza, con un control granular fail-closed

Budget incorpora las dimensiones `user_group` / `agent_group`, aplicadas de forma
**preventiva** mediante `CheckBudget`, y el gasto del grupo se suma mediante **fan-out de
miembros** sobre la atribución de costes por subject existente (no hay una columna de grupo;
sumar indiscriminadamente cada fila produciría un error de atribución:
`modules/finops/ingest.go:75,361`).

La postura es **fail-open**, por la naturaleza de una comprobación de presupuesto y en
coherencia con la separación del producto entre *seguridad = deny-closed* y *presupuesto =
fail-open* (`modules/models/api.go:639,656`), con un control **`fail_closed`** por presupuesto
para las instalaciones que deseen un bloqueo estricto
(`modules/finops/budgets.go:102,166,182`). Esto se expresa con **honestidad**: el gasto
preventivo por grupo es *aproximado*, no una contabilización exacta. La cobertura aumenta
con la atribución; el gasto aún no atribuido simplemente hace que el total del grupo quede
por debajo, que es la dirección segura (nunca deniega erróneamente). El mecanismo detective
de respaldo de ingest/finding de FinOps para grupos y los contadores locales de degradación
son un **seguimiento documentado**, deliberadamente no cableado a medias.

### 4. Denegación del proxy al superar la ventana: 413, sin mutar nunca el payload del cliente

Cuando una petición supera la ventana efectiva de la política/del grupo, el proxy
**deniega con HTTP 413** y un detalle (`cmd/olivares/inferenceproxy.go:449`); **nunca muta el
payload opaco del cliente**: deniega en lugar de recortar silenciosamente
(`inferenceproxy.go:550`). La compactación y el truncamiento señalizado solo existen donde
la propia plataforma ensambla el contexto (retrieval y el runtime de sesión), nunca sobre el
prompt del llamante. No hay degradación silenciosa.

Los tres puntos de enforcement están cableados: retrieval
(`modules/knowledge/query.go:167` → `:354`), el runtime de sesión
(`modules/sessions/runtime.go:285,623`) y el proxy de inferencia (arriba).

## Decisiones y estado (dentro de la dirección aprobada)

- **Nueve tipos de ámbito de la política de contexto** —
  `session > agent > user > user_group > role > agent_group > kb > workspace > tenant` — validados en el handler de
  escritura (`modules/knowledge/context.go:102-103`), con un `effect` anulable y solo
  expansivo (una reconciliación establecida de columnas del módulo, sin migración numerada).
- **`surface` y `model` no son tipos de ámbito.** Retrieval no tiene surface y el proxy ya
  integra la ventana por surface en el MIN, por lo que añadirlos sería generalidad sin uso
  (YAGNI).
- **«Métrica OTel» para esta funcionalidad = eventos auditables + findings nativos**, no un
  medidor dentro del módulo. La telemetría del producto fluye por el bus como findings hacia
  observability; un nuevo medidor supondría un cambio transversal de arquitectura, fuera de
  este ámbito.

## Alternativas consideradas

- **Composición por el más específico para `max_tokens`** (uniforme con los campos
  cualitativos): rechazada; un límite numérico que un ámbito más específico puede elevar no
  es un límite, lo que frustra el objetivo. Se mantiene trivialmente reversible si una
  instalación discrepa.
- **Un medidor dedicado dentro del módulo para telemetría de contexto/grupo:** rechazado por
  ser un cambio transversal de arquitectura; la ruta de eventos de auditoría + findings del
  bus ya transporta la señal.
- **Sumar todas las filas de gasto por subject para un grupo sin fan-out de miembros:**
  rechazada; sobredimensiona y atribuye erróneamente. El fan-out sobre la pertenencia
  autenticada es la atribución correcta y segura.

## Consecuencias

- La política de contexto pasa de metadatos muertos a una **decisión activa** en retrieval,
  el proxy y el runtime de sesión.
- Los límites de **ventana** por grupo son **estrictos y se componen por MIN**; los límites
  de **gasto** por grupo son **preventivos y honestamente aproximados**, con un `fail_closed`
  opt-in.
- **Deuda registrada, nada cableado a medias:** E3-full (reencaminar la inferencia de sesión
  mediante identidad gobernada), el mecanismo detective de respaldo para gasto por grupo a
  través de FinOps más los contadores locales de degradación, y propagar el principal
  (`user` / `user_group`) al gate de lanzamiento. Todos son seguimientos documentados.
