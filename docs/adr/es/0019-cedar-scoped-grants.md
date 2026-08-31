> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0019: Cedar como motor de grants positivos y con ámbito (no una capa solo de denegación)

- **Status:** accepted
- **Date:** 2026-06-15
- **Deciders:** Fran Olivares
- **References:** ADR-0013 (PDP — Cedar + OPA)

## Contexto y planteamiento del problema

ADR-0013 colocó Cedar tras la costura `auth.PolicyEvaluator` como una **capa solo de denegación**:
se compilaba un `permit(principal, action, resource)` base implícito por delante de la política del
operador, de modo que una decisión de Cedar solo podía llegar a ser un `forbid` (una restricción).
Por tanto, la autorización era **plana a nivel de tenant** — el RBAC integrado concedía un permiso a
lo largo de todo el tenant, y la política solo podía estrecharlo. No había forma de expresar un
**grant positivo y con ámbito**: "este administrador puede gestionar agentes solo en el workspace X",
"los viewers pueden leer recursos solo bajo la carpeta Y", "este rol puede escribir en el agent-group
`payments`". El plano de scoping (workspace → agent-group → agente → recurso/carpeta) modelaba el
árbol, pero nada *aplicaba* grants a lo largo de él; el access map se limitaba a *observar*
(`AccessEdge.Permitted` = "no se sabe que esté permitido").

## Motivadores de la decisión

- Expresar grants positivos con ámbito limitado al árbol (workspace, agent-group, subárbol de
  recursos) y a condiciones (modelo, sensibilidad, AAL) — aplicados en la ruta real.
- Mantener las garantías de capa de denegación y de denegación por defecto que estableció ADR-0013
  (forbid sigue prevaleciendo; un grant ausente sigue denegando).
- No reimplementar a mano la resolución de jerarquía/pertenencia — usar el motor formalmente
  verificado que la modela de forma nativa.
- Retrocompatibilidad: un despliegue sin grants redactados debe decidir exactamente igual que antes, y
  no pagar nada en la ruta caliente.

## Resultado de la decisión

**Elevar el motor Cedar embebido de una capa solo de denegación a un motor de grants trivaluado y
consciente del ámbito, tras una NUEVA costura `auth.ScopedAuthorizer` junto a (no dentro de) la capa
de denegación.**

1. **Decisión trivaluada, sin el truco del permit base.** El `Authorize` de cedar-go deniega por
   defecto y forbid prevalece sobre permit, y su `Diagnostic.Reasons` nombra las políticas
   determinantes. Eso recupera el efecto que necesita el Authorizer a partir de una única evaluación:
   `Allow` → **Grant**; `Deny` con razones → **Forbid**; `Deny` sin
   razones → **Abstain** (denegación por defecto). El permit base de ADR-0013 se elimina — un
   `permit` ahora concede de verdad, un `forbid` sigue restringiendo, y una política vacía/irrelevante
   se abstiene para que la decisión RBAC prevalezca (la invariante de retrocompatibilidad).

2. **El álgebra del Authorizer pasa a ser** `Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay`.
   El motor con ámbito se ejecuta primero (un forbid cortocircuita, prevaleciendo sobre el RBAC y
   cualquier grant); la base es el grant RBAC de ámbito tenant O un grant positivo con ámbito; el
   motor ABAC nativo + cualquier PDP externo lo estrechan después (defensa en profundidad). Se
   preservan la denegación por defecto y el fallo cerrado; un motor con ámbito nil reduce el Authorizer
   a su comportamiento de ADR-0013.

3. **La autoridad del grant ES la política Cedar redactada por tenant** (la superficie de redacción
   de políticas), compilada ahora en modo grant. El motor resuelve el linaje del recurso *verdadero*
   de la petición (leído del store por id de entidad — no manipulable) en un grafo de entidades Cedar
   cuyos `Parents` codifican la contención, de modo que el `in` transitivo de Cedar recorre la
   jerarquía. **No añadimos un store separado de filas de grant estructuradas**: la redacción de grants
   estructurados/de consola que *se proyecta a* Cedar es asunto de la capa de redacción estructurada;
   el motor con ámbito consume la política y la aplica.

4. **Los grants son solo por tenant; el Cedar de entorno global y OPA siguen siendo solo de
   denegación.** Un *forbid* global demasiado amplio solo deniega (seguro); un *permit* global
   demasiado amplio concedería entre tenants (inseguro). Esa asimetría es decisiva: los grants
   positivos viven en la política redactada por tenant (que el motor indexa por tenant), mientras que
   el Cedar de entorno a nivel de despliegue (`OLIVARES_PDP_*`) y OPA siguen siendo capas solo de
   restricción.

### Consecuencias

- **Bueno:** autorización con ámbito de nivel empresarial (workspace/agent-group/carpeta/modelo/
  sensibilidad/AAL) aplicada en el cuello de botella REST + gRPC; el motor verificado resuelve la
  jerarquía/pertenencia; retrocompatibilidad y denegación por defecto intactas; la ruta caliente no
  paga nada hasta que un tenant opta por los grants (el motor se abstiene antes de cualquier lectura
  del store).
- **Malo / compromisos:** un tenant con grants habilitados paga una pequeña lectura del store, sujeta a
  gate, para resolver el ámbito de una entidad en peticiones a nivel de entidad (una caché de ámbito
  por tenant es el seguimiento documentado); las condiciones del árbol de ámbito solo se resuelven
  contra la jerarquía viva en el motor activado, no en el dry-run de la redacción.
- **Cambio de comportamiento (documentado):** una regla `permit` de operador que la capa de ADR-0013
  neutralizaba en silencio ahora CONCEDE. Las políticas redactadas solo con forbid no se ven afectadas.

## Por qué se rechazaron las alternativas

- **Un esquema separado de filas de grant estructuradas en el motor con ámbito** — duplica el propio modelo de
  políticas y la resolución de jerarquía de Cedar; el motor verificado ya expresa grants como políticas
  sobre un grafo de entidades. La redacción estructurada pertenece a la capa de redacción estructurada, proyectándose al Cedar que
  el motor ya consume.
- **Una política Cedar generada por cada grant** — no escala (crecimiento del conjunto de políticas,
  rotación en cada edición de grant); las políticas con plantilla sobre un grafo de entidades resuelto
  permiten que una sola regla cubra un workspace/grupo/subárbol completo.
- **Hacer que el Cedar de entorno global pueda conceder grants** — un guard de tenant olvidado en un
  permit global concede entre tenants. Los grants quedan confinados a la política por tenant.
