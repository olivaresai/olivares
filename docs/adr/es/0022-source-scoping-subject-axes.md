> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0022: Scoping de fuentes por eje de subject (sesión / agente / usuario / grupo de usuarios / rol), con effect a nivel de fila y una postura de enforcement versionada y de control dual

- **Status:** accepted
- **Date:** 2026-07-07
- **Deciders:** Fran Olivares
- **References:** ADR-0003 (RRW map — permitted vs observed), ADR-0019 (Cedar scoped grants).

## Contexto y planteamiento del problema

La vinculación de fuentes (`modules/sourcescope`) vincula una fuente conectada —un servidor
MCP, un modelo, un proveedor, una base de conocimiento, una fuente de datos— a exactamente
uno de tres árboles de ámbito de **contención**: `workspace`, `agent_group` o `folder`
(`schema.go:52-62`, `binding.go:33`). Responde a «un actor **dentro de** este ámbito puede
acceder a esta fuente».

La visión del producto exige cuatro ejes adicionales que el modelo de contención no puede
expresar de forma ergonómica:

- **«esta SESIÓN ve la fuente X»**: una única sesión en ejecución.
- **«este USUARIO / grupo de usuarios accede a las fuentes Y»**: una persona identificada y
  un grupo de personas del directorio.
- **«este AGENTE concreto (no su grupo) solo ve Z»**: un agente, no el agent-group al que
  pertenece.

Actualmente, estos ejes solo se *aproximan* redactando un grant Cedar en bruto: sin
ergonomía de vinculación, sin fila listable/auditable, sin proyección al access map y, para
la pregunta inversa «¿a qué fuentes puede acceder el subject S?», con el problema de
consulta inversa sin resolver (`accessmap.go:44`). Mientras tanto, la gobernanza de
**modelos** ya incorpora un rico modelo de SUBJECT:
`subject_kind ∈ {user, role, agent_group}` con filas allow/forbid y un álgebra
`forbid-overrides-allow`
(`modelgovernance.go:98-100`, `modelaccessgate.go:204`). Existe una asimetría de gobernanza:
**los modelos se gobiernan de forma rica por subject; las fuentes se gobiernan de forma
limitada por contención.** Esta decisión la elimina.

Un requisito de segundo orden procede del análisis de los productos existentes (verificado
con la documentación de los proveedores el 2026-07-07): AWS Q Business convierte la
*relajación* de una ACL en una operación IAM específica, unidireccional y auditada
(`qbusiness:DisableAclOnDataSource`); la postura de ACL del data store de Google es
**inmutable tras su creación**. Nuestro diferenciador es una postura **mutable, versionada
y auditada**, pero relajarla debe ser una operación **privilegiada, de control dual y
auditada**, nunca un toggle silencioso. Ningún producto existente expresa scoping de fuentes
por agente o por sesión: es un espacio en blanco verificado, no una hipótesis.

## Motivadores de la decisión

- **Coherencia con el acceso a modelos.** El mismo vocabulario de subjects y el mismo
  álgebra `forbid-overrides-allow`, para que los operadores razonen sobre «quién puede
  acceder a una fuente» exactamente como lo hacen sobre «quién puede usar un modelo».
- **Coste de la ruta caliente.** El resolver se ejecuta en la ruta EXECUTE de models
  (`ScopeGate`) y en la ruta de retrieval de knowledge (`RetrievalScopeGate`). Los ejes de
  identidad no deben añadir un round-trip de política por resolución.
- **Auditabilidad y consulta inversa.** «Listar todas las fuentes asignadas a la sesión S / al
  usuario U / al grupo G» debe ser una única consulta indexada, no un recorrido inverso de
  Cedar (la consulta inversa no está resuelta).
- **UI.** Una única forma de vinculación que la consola (un seguimiento) pueda renderizar y
  redactar.
- **Retrocompatibilidad y seguridad.** Una instalación sin nuevas vinculaciones decide
  exactamente como antes; los ejes de identidad deben vincularse al **principal
  autenticado**, no a una cadena declarada por el llamante, siempre que sea posible; el
  plano de control nunca debe adquirir un segundo motor de autorización, para mantener
  pequeña la superficie de ataque.

## Resultado de la decisión

**Enriquecer en el sitio la vinculación de fuentes existente (candidata A1): añadir árboles
de ámbito por subject y un `effect` a nivel de fila, proporcionando a `sourcescope` un
álgebra allow/forbid con ámbito de subject que refleje model-access sobre su propia tabla,
mientras se mantienen exactamente como están el modelo de contención y el override Cedar
entre ámbitos.** No redactar Cedar en bruto para los nuevos ejes (candidata B) ni levantar
un plano de decisión paralelo gemelo de model_access (candidata C). Un plano de control,
una superficie de consulta y un lugar donde acertar con la autorización.

### 1. Cinco nuevos árboles de ámbito por subject, uniformes con los árboles de contención existentes

`scope_tree` crece de `{workspace, agent_group, folder}` para incluir también:

| tree | `scope_ref` | coincide cuando… | fuente de identidad | ¿falsificable? |
|---|---|---|---|---|
| `session` | `external_id` de la sesión | la sesión actuante == ref | la ref del llamante consciente de la sesión, reforzada por identidad del agente | condicionada por ruta (véase §4) |
| `agent` | `external_id` del agente | el agente actuante == ref | `principal.AgentIdentity` ∨ el agente de la sesión ∨ la ref del agente | condicionada por ruta / autenticada |
| `user` | id de usuario | `principal.UserID` == ref | **principal autenticado** | no |
| `user_group` | `UserGroup.ID` | ref ∈ `principal.GroupsIn(tenant)` | **principal autenticado** (cierre anidado condicionado por grupo de directorio) | no |
| `role` | nombre del rol del tenant | `principal.RoleIn(tenant)` == ref | **principal autenticado** | no |

`user_group` es el **grupo de directorio**: se compara por **id de grupo** con
`principal.GroupsIn(tenant)`, que ya viaja en el principal autenticado y ya incorpora todo
el cierre de ancestros anidados (`principal.go:67-77,151-164`); no se añade ninguna lectura
de grupo por resolución. `UserGroup` no tiene slug (`model/auth.go:122`), por lo que el id
es el identificador estable. `role` se añade para obtener **paridad completa con
model-access** (Fran Olivares, 2026-07-07): gobernar una fuente por el rol del tenant es la
palanca gruesa de «grupo de usuarios» que también expone model-access.

Los tres ejes de identidad individual (`session`, `agent`, `user`) son contenciones
degeneradas (igualdad); `user_group` y `role` son pertenencias reales. Todos se evalúan como
un **predicado de ámbito** uniforme sobre el actor: no hay un motor de decisiones nuevo.

**La validación sigue una dicotomía contención frente a subject (restricción verificada).**
Los handlers de escritura del módulo poseen un `store.Scope` de tenant de negocio; los
subjects de autenticación (`model.User`, `model.UserGroup`, roles) residen en
`store.AuthScope` (el tenant del sistema) y **no son alcanzables** desde él
(`core/store/store.go` frente a `auth.go:24-36`). Por tanto:

- Los **árboles de contención** `workspace` / `agent_group` / `folder` **y** los árboles de
  subject residentes en el store se validan en cuanto a existencia al vincular, como hoy
  (deny-closed, «sin ámbito colgante»); pero para mantener una única regla uniforme y
  permitir vincular una fuente antes de una sesión efímera, esta decisión trata **los cinco
  árboles de subject como limitados a la forma** durante la redacción: un `scope_ref` no
  vacío del tipo correcto, sin consulta al store.
- La corrección no depende de validar la existencia: una ref de subject desconocida
  simplemente nunca coincide con el actor autenticado durante la resolución ⇒ deny-closed,
  **exactamente el patrón de model-access** (`modelaccessgate.go` solo valida la *forma* del
  subject; `validateGrantRefs` solo comprueba el TARGET residente en el store). Evitar
  errores tipográficos es responsabilidad de la consola (redactar desde un selector de
  directorio/agente), no de la capa de vinculación. Los árboles de contención conservan sin
  cambios su validación de existencia actual.

### 2. Un `effect` a nivel de fila (allow | forbid), con **forbid-overrides-allow** absoluto

Cada vinculación incorpora `effect ∈ {allow (default; empty stored value = allow), forbid}`
(la misma convención que `normalizeEffect` de model-access). El álgebra del resolver pasa a
ser, para un `(actor, source)`:

```
1. If ANY enabled binding matching the actor has effect=forbid  → DENY   (absolute)
   — OR the cross-scope Cedar engine returns EffectForbid for a resource-anchored (workspace/folder) binding.
2. Else, if the source is UNCONFINED (no enabled ALLOW binding at all) → ALLOW   (global / back-compat),
   subject to the per-workspace connector-assignment gate for unbound connectors.
3. Else (confined), ALLOW iff the actor matches an ALLOW binding (its tree's containment),
   OR a cross-scope Cedar EffectGrant, OR tenant RBAC soft-isolation;
   the credential is taken from the MOST-SPECIFIC matching ALLOW (§3). Otherwise DENY-CLOSED.
```

**Cambio de comportamiento, documentado (como ADR-0019 documentó el suyo).** Hoy, el forbid
de la vinculación de fuentes se aplica *por vinculación*: un `EffectForbid` entre ámbitos en
una vinculación se omite con `continue` y otra vinculación *distinta* aún puede permitir
(`resolver.go:243-248`). Esta decisión hace que **todos** los forbids sean **absolutos**
(tanto `effect=forbid` a nivel de fila como `EffectForbid` entre ámbitos): cualquier forbid
coincidente deniega la fuente y prevalece sobre la contención, el grant entre ámbitos **y**
el RBAC del tenant; exactamente el álgebra de model-access (`modelaccessgate.go:204`) y del
núcleo Cedar (`EffectForbid` «PREVALECE sobre todo», `authorizer.go:101`). La dirección es
estrictamente más segura (un forbid solo puede denegar) y ninguna prueba de forbid
con una única vinculación sufre una regresión; el cambio solo resulta observable en
el caso de varias vinculaciones, antes sin especificar.

**Desencadenante del confinamiento.** Una fuente está *confinada* si y solo si tiene ≥1
vinculación **allow** habilitada. Todas las vinculaciones preexistentes son allows, por lo
que esto es idéntico al «vinculada ⇔ tiene vinculaciones» actual. Una fuente que solo tenga
**forbids** sigue siendo global salvo para los subjects que esos forbids designen: la
postura de model-access «restringir ciertos subjects», disponible ahora también para las
fuentes. El gate de asignación de conectores se basa en «sin vinculación allow» (antes «sin
vinculación»), de modo que una fuente que solo tenga forbids sigue respetando las
asignaciones de conectores.

### 3. Precedencia: forbid absoluto; credencial por el allow más específico

Forbid es absoluto (§2), por lo que la precedencia nunca decide entre permitir o denegar:
decide **qué credencial** recibe un actor permitido cuando coinciden varias vinculaciones
allow. El orden, de más a menos específico, es:

```
session > agent > user > user_group > role > agent_group > folder > workspace
```

Primero la identidad individual, después el grupo de directorio, el rol RBAC, el grupo del
agente actuante y, por último, la contención de recursos. Este orden total hace
determinista la selección de credenciales (sustituye la ordenación léxica de
`loadEnabledBindings`) y es la precedencia documentada
`session > agent > group > workspace`, refinada para los cinco ejes.

### 4. La disponibilidad de los ejes depende del punto de enforcement, y se declara con honestidad

El resolver tiene dos entrypoints que transportan contextos de actor diferentes:

| eje | `ResolveForSession` (models `ScopeGate`, runtime) | `ResolveForAgent` (knowledge `RetrievalScopeGate`) |
|---|---|---|
| session | ✅ la ref de la sesión actuante | ❌ no hay sesión en el contexto → nunca coincide |
| agent | ✅ el agente de la sesión (override por identidad del agente) | ✅ la ref del agente (override por identidad del agente) |
| user / user_group / role | ✅ principal autenticado | ✅ principal autenticado |
| workspace / agent_group / folder | ✅ (existente) | ✅ (existente) |

Una vinculación `session` en una base de conocimiento **no** se aplica en la ruta de
retrieval solo de agente porque allí no existe ninguna sesión: no se «permite» en silencio,
sencillamente no forma parte del ámbito de ese actor; las demás vinculaciones/ejes de la
misma fuente siguen aplicándose. Esta asimetría está documentada en el contrato, no oculta.
Los ejes `session`/`agent` siguen condicionados por la ruta; las referencias influidas por
el llamante se refuerzan mediante la comprobación de identidad del agente
(`principal.AgentIdentity` prevalece sobre una ref declarada por el llamante). `user`/
`user_group`/`role` se vinculan al **principal autenticado no falsificable** y, por tanto,
son los ejes más robustos.

### 5. La postura de enforcement es mutable, versionada y auditada; relajarla tiene control dual

La *postura* de una fuente es el conjunto de sus vinculaciones habilitadas y sus effects.
Según Fran Olivares (2026-07-07, «robusto sin duplicación»): **`revision.go` y
`approvals.go` de governance son internos del módulo y NO reutilizables desde
`sourcescope`** (verificado: helpers no exportados, entidades propias, flujo de aprobación
REST). Bifurcarlos en `sourcescope` generaría deuda técnica duplicada, por lo que los
controles de postura son **autocontenidos** y reutilizan la única primitiva inmutable
compartida que ya existe: el ledger de auditoría:

- **Auditada y versionada mediante la cadena de auditoría.** Cada mutación de la postura
  registra el **delta** de postura en el ledger de auditoría append-only y encadenado por
  hash (ADR-0009): `sourcescope.binding.*` para crear/actualizar/eliminar (ampliando
  `auditBinding` con el `effect`) y `sourcescope.posture.{propose,approve,reject}` para el
  ciclo de vida de control dual. El ledger ES el historial de versiones inmutable y
  secuenciado; deliberadamente NO se añade una *tabla* específica de revisiones numeradas
  con rollback (duplicaría `governance/revision.go`). Las filas de **posture-request**
  pendientes/decididas son el registro de primera clase y consultable de cada *relajación*
  (quién la propuso, quién la aprobó).
- **Control dual solo en la dirección de relajación, autocontenido.** Una mutación que pueda
  **ampliar** quién accede a una fuente es una *relajación*: el actor NO la aplica; se
  registra como `sourcescope_posture_request` pendiente y solo se aplica cuando la aprueba
  un **SEGUNDO principal DISTINTO** (la comprobación `proposer != approver` aporta la
  integridad de dos personas), y el aprobador posee el permiso de nivel admin
  `sourcescope:posture:admin` (separación de funciones respecto al proponente de nivel
  editor).

  > **Enmienda de estado, 2026-08-07.** La enumeración de abajo queda CORREGIDA. Tal
  > como se escribió listaba *ampliar un allow* y *mover un allow*, no nombraba ninguna
  > operación de scope sobre un `forbid`, y colocaba «estrechar a un árbol más específico»
  > entre las escrituras ordinarias de un solo actor **sin cualificarla por efecto**. El
  > código implementaba eso fielmente, así que un `forbid` que seguía siendo un `forbid`
  > habilitado y solo cambiaba a qué población cubría se aplicaba en el acto, por un solo
  > actor — mientras que ELIMINAR ese mismo forbid exigía dos personas. La puerta de dos
  > personas se rodeaba editando en vez de borrando. Invertir los clasificadores a listas
  > blancas destapó tres fugas más de la misma clase: mover un `allow` a un árbol «más
  > específico»; convertir en `forbid` el ÚLTIMO `allow` habilitado; y crear un `allow` sobre
  > una fuente YA confinada (la creación no la clasificaba nada en absoluto). La norma general del principio de este punto nunca cambió
  > y es lo que autoriza la corrección: la enumeración siempre fue más estrecha que la norma
  > que decía precisar.

  **Los clasificadores son LISTAS BLANCAS.** Enumeran las escrituras que demostrablemente no
  pueden ampliar el acceso y tratan **todo lo demás — incluida cualquier forma que no
  reconozcan — como una relajación**. Una lista negra de formas relajantes tiene fugas por
  construcción, y ésta tuvo cuatro. Tres eran ediciones de un binding existente —un `forbid`
  que encoge su scope, un `allow` movido a un árbol «más específico» y el ÚLTIMO `allow`
  habilitado convertido en `forbid`—; la cuarta era la creación, que no clasificaba nada. Las
  dos primeras salen de leer una operación de scope con la polaridad de un `allow`. La tercera
  sale de leer el EFECTO de la fila olvidando el CONFINAMIENTO que esa misma fila sostenía: una
  fuente sólo está confinada mientras tenga un `allow` habilitado, así que la escritura que se
  lee como «esta fila ya sólo puede denegar» es también la que deja la fuente global.

  **Un `forbid` INVIERTE LA POLARIDAD de toda operación de scope, y esa es la trampa.** Para
  un `allow`, un scope más pequeño alcanza a menos actores: endurecimiento. Para un `forbid`,
  un scope más pequeño PROTEGE a menos actores: todos los que deja de cubrir quedan
  des-denegados por esa única escritura.

  **Dos scopes solo son comparables cuando son el MISMO scope.** `specificityRank`
  (`resolver.go`) **ordena árboles para elegir CREDENCIAL** entre los bindings allow que
  casan; **no es una relación de contención** y jamás debe usarse como tal. `role:admin` y
  `user_group:g1`, `workspace:eng` y `agent_group:core`, una carpeta y su hija son
  POBLACIONES distintas y ninguna contiene a la otra — y un binding de carpeta no tiene
  dimensión de contención alguna (va por el grant Cedar cross-scope). La pertenencia tampoco es fija: un
  superconjunto demostrado leyendo filas hoy no es un superconjunto mañana. Por eso el
  certificado de «esta escritura no puede ampliar el acceso» es la **identidad del scope y
  nada más débil**, y «no sé comparar estos dos scopes» se resuelve como *relajación*: un
  falso positivo cuesta una aprobación de más, un falso negativo es rodear una puerta de dos
  personas.

  **Relajaciones**, con precisión (`classifyCreate`/`classifyUpdate`/`classifyDelete`):
  eliminar o deshabilitar un **forbid** habilitado; cambiar `forbid→allow`; **cualquier
  cambio de scope sobre un forbid habilitado** (des-deniega a parte de su población);
  **habilitar** un allow; deshabilitar o eliminar el **último** allow habilitado (deja la
  fuente sin confinar → global); **cualquier cambio de scope sobre un allow habilitado** —
  más amplio, más estrecho o lateral, da igual; **crear un allow sobre una fuente YA
  confinada** (una concesión para una población que no podía alcanzarla); y la operación
  unidireccional específica **`POST /sources/disable-scoping`** (el reflejo de
  `qbusiness:DisableAclOnDataSource` de AWS).

  Las mutaciones de **endurecimiento / neutrales** son escrituras ordinarias de un solo actor
  —auditadas, pero sin gate—: añadir un **forbid**; `allow→forbid`; crear el **PRIMER** allow
  habilitado sobre una fuente sin confinar (pone la fuente bajo gobierno: el mayor
  endurecimiento del módulo, deliberadamente sin puerta para que el movimiento seguro nunca
  sea el caro); crear una fila **deshabilitada**; habilitar un **forbid** aparcado; eliminar o
  deshabilitar un allow que **no** sea el último; y editar nota/credencial dejando intactos
  efecto, enabled y scope (el localizador de credencial elige QUÉ referencia recibe un actor
  ya autorizado, nunca SI está autorizado). Una fila deshabilitada antes y después no impone
  nada, así que cualquier escritura sobre ella es neutral.

  Esta asimetría coincide con la de AWS (relajar es la operación privilegiada) y supera la
  postura inmutable de Google: la nuestra es mutable *y* gobernada. Endpoints: la
  creación/actualización/eliminación relajante se PROPONE mediante los `POST /bindings` y
  `PUT`/`DELETE /bindings/{id}` existentes (devuelven `202` con la petición pendiente);
  `POST /posture-requests/{id}/{approve,reject}` decide; `GET /posture-requests` es la cola
  del revisor.

### 6. El access map proyecta los nuevos orígenes (ADR-0003)

`publishBindingEdges` proyecta el lado permitido del mapa RRW. `EdgeObservation` ya admite
`OriginKind ∈ {agent, session, identity}` (`sdk/model/observation.go:55`), por lo que cada
uno de los tres ejes de identidad individual proyecta UN borde: una vinculación `session` →
un borde con origen `session` (una vinculación por sesión aparece como borde de **esa**
sesión); `agent` → un borde con origen `agent`; `user` → un borde con origen `identity`.
Los ejes de subject de GRUPO (`user_group`, `role`) necesitarían enumerar sus MIEMBROS para
proyectar bordes, pero los miembros son entidades de auth-scope (grupos de directorio,
usuarios) no alcanzables desde el `store.Scope` de tenant del módulo; por ello, exactamente
como la proyección de grants inversos de una vinculación de folder (el aplazamiento de la
consulta inversa), se **APLAZAN**: se registra en el log y no se proyecta nada. Las
vinculaciones forbid no proyectan nada (un forbid no es un borde permitido). El enforcement
siempre es la decisión activa del resolver frente al principal activo; el mapa es
observabilidad de drift de mejor esfuerzo, y un borde aplazado/ausente nunca lo debilita.

## Consecuencias

- **Bueno:** los cuatro ejes de la visión (cinco con `role`) se pueden expresar, se aplican
  con deny-closed en ambos PEP reales y son visibles en la resolución de ámbito y el access
  map; una única forma de vinculación auditable/listable para la consola; los ejes de
  identidad se vinculan al principal autenticado (no falsificable); sin un segundo motor de
  autorización (superficie de ataque pequeña); la ruta caliente paga una comprobación de
  pertenencia barata y **cero** nuevos round-trips de política para los ejes de identidad;
  una postura mutable pero gobernada que es un diferenciador verificado frente a AWS
  (unidireccional) y Google (inmutable).
- **Malo / compromisos:** `scope_tree` incorpora ahora semánticas de «ámbito de contención» y
  de «identidad de subject» (mitigación: el contrato las encuadra como un *predicado de
  ámbito* uniforme); la maquinaria de postura/control dual añade superficie real que una
  instalación mínima no ejercita hasta redactar una relajación; hacer forbid absoluto es
  un cambio de comportamiento documentado (dirección segura).
- **Neutral:** `role` se solapa conceptualmente con el bypass de aislamiento suave de RBAC
  del tenant existente (`rbacAllows`); se componen (una vinculación `role` es un ámbito
  positivo; el bypass RBAC es la regla de visibilidad del operador del tenant), y un forbid
  prevalece sobre **ambos**.

## Por qué se rechazaron las alternativas

- **(B) Una API de alto nivel que genere políticas Cedar para los nuevos ejes.** Rechazada:
  (1) sería el *único* plano que redacta Cedar en bruto, mientras que model-access, el
  objetivo de coherencia, **no** genera Cedar; decide sobre sus propias filas
  (`modelaccessgate.go:11-14`). (2) Paga un round-trip a Cedar por resolución en la ruta
  caliente. (3) La pregunta inversa que necesita la consola («¿a qué fuentes puede acceder
  el subject S?») es la consulta inversa de Cedar sin resolver, por lo que la UI y el access
  map quedarían bloqueados o serían aproximados. (4) Auditar «quién asignó qué» exige leer
  texto de políticas, no filas.
- **(C) Una tabla paralela gemela de model_access para grants de source-subject, compuesta con
  la vinculación de contención existente.** Rechazada por ser sobreingeniería que *reduce*
  la robustez: hay que componer dos planos de decisión en cada PEP y mantenerlos coherentes;
  una fuente clásica de drift de seguridad (uno actualizado, el otro no; precedencia
  ambigua entre planos). «Más completo/enterprise» se consigue mediante **profundidad en un
  único plano** (todos los ejes + effect + postura versionada de control dual + matriz
  completa de pruebas), no duplicando el plumbing. Un solo plano de control con un álgebra
  uniforme es más fácil de auditar («todo lo que gobierna la fuente X» = una consulta) y de
  demostrar correcto.
- **Ampliar el vocabulario scopeSpec de los roles personalizados en lugar de un enum local.**
  Rechazada: el `scope_tree` de `sourcescope` es una constante local del módulo que solo
  *refleja* el catálogo de roles personalizados (`schema.go:49`); ampliar un catálogo
  compartido filtraría los ejes de fuentes a aquello que los roles personalizados pueden
  seleccionar. Los nuevos árboles permanecen locales a `sourcescope`.
