> Traducción automática. La versión en inglés es la fuente autoritativa.

# ADR-0013: PDP de autorización — Cedar embebido + adaptador OPA-over-HTTP

- **Status:** accepted (solo restricción, limitado a la costura `auth.PolicyEvaluator`
  que crea este registro) — **modificada por la ADR-0019 (2026-06-15)**, que elimina
  el permit base: una regla permit del operador que esta capa neutralizaba en silencio
  ahora concede, y trasladó Cedar a un motor positivo de grants acotados en una costura
  distinta. Las políticas de solo forbid no cambian; la formulación «nunca ampliar» del
  Contexto y de los Factores de decisión queda sustituida en esa lectura — consulta la
  nota de modificación al final.
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares
- **References:** contrato de NHI/MCP-auth; modificada por la ADR-0019 (grants Cedar acotados)

## Contexto y planteamiento del problema

Más allá de RBAC, la plataforma necesita un policy decision point (PDP) para
autorización basada en atributos. Las organizaciones difieren: unas quieren un motor
autocontenido, otras tienen un parque OPA existente. El PDP nunca debe *ampliar* el
acceso — solo restringir.

## Factores de decisión

- Funcionar autocontenido (binario único, air-gap) sin servicio de políticas externo.
- Encajar con un despliegue OPA existente cuando el operador lo tenga.
- Un invariante de solo-restringir: la política puede denegar, nunca conceder más allá de
  RBAC.

## Opciones consideradas

- **Ambos:** Cedar embebido (primario, Go puro) **y** un adaptador OPA-over-HTTP tras una
  única costura, seleccionado por el operador.
- **Solo Cedar.**
- **Solo OPA.**

## Resultado de la decisión

Opción elegida: **ambos, tras una única costura `PolicyEvaluator`**. **Cedar** es el PDP
primario embebido, en Go puro; hay disponible un adaptador **OPA-over-HTTP**; el operador
selecciona el motor mediante `OLIVARES_PDP_ENGINE = cedar | opa | none`. La costura ABAC
**solo restringe** (se combina con AND con RBAC y nunca amplía). El invariante de
solo-restringir se prueba de extremo a extremo.

### Consecuencias

- **Bueno:** autocontenido por defecto (Cedar, sin sidecar); encaja con un parque OPA
  cuando se desea; una costura, dos motores.
- **Malo / compromisos:** dos adaptadores que mantener; el endurecimiento del transporte
  de la vía OPA (p. ej. mTLS al sidecar) es una extensión documentada, aún no completa.
- **Neutral:** `none` deshabilita la capa ABAC, dejando RBAC deny-by-default.

## Por qué se rechazaron las alternativas

- **Solo Cedar** — excluye a las organizaciones estandarizadas en OPA.
- **Solo OPA** — fuerza un servicio de políticas externo en toda instalación, rompiendo
  el valor por defecto autocontenido / air-gap.

## Modificación (2026-06-15, ADR-0019)

*(La decisión modificadora está fechada el 2026-06-15; esta nota se añadió el
2026-08-17, cuando una revisión del registro de decisiones encontró los dos registros
firmados con once días de diferencia y sin un enlace de precedencia entre ellos. No se
reescribe nada de lo anterior.)*

**Lo que ya no se sostiene tal como está escrito.** La afirmación del Contexto «El PDP
nunca debe ampliar el acceso — solo restringir» y el factor «la política puede denegar,
nunca conceder más allá de RBAC» se leen, tal como están escritos, como afirmaciones
sobre **toda la decisión de autorización** y sobre **Cedar**. Desde la **ADR-0019**,
ninguna es cierta en esa lectura: Cedar pasó de ser una capa de solo denegación a un
motor de **grants** trivalente y consciente del alcance, y se eliminó el `permit`
base implícito que hacía que una decisión de Cedar solo pudiera restringir; por ello,
un `permit` escrito ahora concede de verdad.

**Lo que sí es cierto.** El invariante de solo restricción sobrevive **limitado a la
costura que creó este registro**: `auth.PolicyEvaluator` todavía se ejecuta después
de RBAC y solo puede restringir más (`core/auth/authorizer.go:100-104`). El grant
positivo vive en una costura **nueva y distinta**, `auth.ScopedAuthorizer`, cableada
**junto a** la capa de denegación, no dentro de ella, y el Authorizer las combina como
`Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay`
(`core/auth/authorizer.go:157-163`, álgebra en `:161` y `:200`). Se preservan la
denegación por defecto, la prevalencia de forbid sobre permit y el fallo cerrado ante
un error, y un despliegue sin grants escritos decide exactamente como lo hacía bajo
este registro. Todo lo demás se mantiene: los dos motores detrás de una costura y el
selector `OLIVARES_PDP_ENGINE = cedar | opa | none` son el comportamiento entregado
(`cmd/olivares/wire.go:994-1018`).

**Dónde vive la decisión actual.** `docs/adr/0019-cedar-scoped-grants.md` (accepted,
2026-06-15, Fran Olivares), que referencia explícitamente este registro. Quien cite
solo esta ADR — aquella a la que conduce el término *ABAC* — puede concluir que la
vía entregada de grants positivos infringe una decisión firmada. No es así: sigue la
ADR-0019.
