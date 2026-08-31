---
title: "Módulo VI — identidad, permisos y gobernanza"
description: >-
  El control plane sobre el modelo de autorización: reconciliación del roster de
  identidades, el puente agente↔identidad, el motor ABAC solo-deny y el gate de
  aprobación human-in-the-loop con una traza de decisiones append-only. La raíz de
  la actuación gobernada.
---

El módulo VI es el **plano de gobernanza sobre el modelo de autorización existente
del motor** — **no** reimplementa el enforcer ni los connectors de identidad, los
consume. Vincula cinco subsistemas tras un único contexto acotado (la identidad y
su gobernanza): un reconciliador del **roster** de directorio, el **puente
agente↔identidad** que hace firme la atribución, un motor **ABAC** solo-deny, el
gate de aprobación **human-in-the-loop** y los **backends de autoría** de
política/identidad. Esta es la raíz de toda acción *gobernada* del producto.

## Qué es

El módulo se sitúa en la capa de gestión y es la autoridad de **decisión** del
control plane: quién y qué puede hacer qué, y qué acciones requieren primero a un
humano. Su contrato es la postura solo-deny y deny-by-default hecha aplicable —

- **La reconciliación del roster** converge un directorio conectado (las fuentes
  de identidad) en las entidades `Identity` canónicas del motor más el grafo de
  colección/membresía propiedad del módulo, con find-or-create indexado únicamente
  por external id, de modo que **actualiza la misma fila** que el mapa de acceso
  crea a partir de una referencia de auditoría. Esa convergencia de una sola fila
  es lo que hace posible la atribución firme.
- **El puente agente↔identidad** vincula un agente al id interno de la identidad
  no humana canónica que presenta su credencial, resolviendo la dependencia dura
  que permite al módulo III (el mapa de acceso) cancelar el falso
  least-privilege drift de permitido-vs-observado.
- **El motor ABAC** es un evaluador nativo que corre **después** del RBAC y solo
  puede *restringir más* — nunca amplía un grant.

## Su contrato y entidades

El módulo VI posee cuatro entidades en el modelo de datos compartido — una
**colección** y una arista **collection-member** (el grafo de grupo/rol derivado
de la fuente, resuelto transitivamente dentro de límites), una **aprobación** (una
solicitud HITL mutable) y una traza **approval-decision** append-only. Las
identidades **no** se duplican en una tabla del módulo; se reconcilian en la
entidad `Identity` canónica del motor.

El **evaluador ABAC** implementa el seam de evaluador de política del motor con
propiedades verificadas: toda regla es una regla **deny**; corre después del RBAC
dentro de un AND, de modo que una política nunca puede expandir el acceso; una
política *habilitada* malformada **falla cerrada** (deniega); la ruta caliente de
autorización se sirve desde una caché por tenant invalidada **después** de que una
escritura commitea, aislada estrictamente por tenant. Las specs de política son
**tipadas y re-serializadas** en la escritura (el JSON del operador nunca se
round-trippea literalmente), de modo que una credencial no puede entrar en una
spec. OPA/Rego es el seam de evaluador externo, nunca una dependencia arrastrada
al motor.

El **gate de aprobación** es la trazabilidad acción→humano que el audit ledger
ancla: la separación de funciones y el guard de decisor duplicado se indexan por
la **identidad de usuario estable** (un token de sistema no puede decidir), el
umbral de aprobación múltiple es **race-safe** sobre el store (un cruce
concurrente se resuelve en exactamente un ganador), y la expiración se deriva de
forma lazy en la lectura y luego se materializa mediante un barrido explícito y
acotado por tenant. Los backends de autoría (managed-settings/hooks, política como
código Cedar/OPA, el grafo de objetos WIF) añaden una ruta de escritura
**publish→immutable-revision→drift**; para Cedar una política publicada se activa
sobre la overlay solo-deny por tenant en vivo y se recarga en el arranque, de modo
que una afirmación `active` sobrevive a un reinicio.

## Qué consume y produce

El módulo **consume** la base de autorización y auditoría del motor y el roster de
identidad tipado de las fuentes de directorio configuradas; rellena el campo
`Agent.IdentityID` del que depende el mapa de acceso. **Produce** eventos
`FindingReport` en el [bus de eventos](/es/reference/events/) — una **identidad
compartida** vinculada a más de un agente, más **escalado** y **expiración** de
aprobación — cada uno emitido una sola vez, gobernado por un marcador persistido
de modo que un barrido repetido no pueda emitir por duplicado. Toda mutación
privilegiada, y las lecturas de identidad y binding relevantes para la
reconciliación, **se autoauditan al principal real** dentro de una transacción
commiteada; el actor de auditoría es siempre una referencia tipada de principal,
nunca un email.

:::caution[Límites honestos]
- **El motor ABAC está autorizado y auditado, pero la aplicación depende de la
  composición.** El estado de gobernanza se escribe y audita hoy; la raíz de
  composición del arranque cablea el evaluador e inyecta los proveedores de
  directorio. Allí donde no están cableados, el motor no está en vigor y una
  sincronización de roster no tiene proveedores — esto se **declara, nunca es un
  no-op silencioso**.
- **La atribución firme requiere identidad-por-agente.** Un binding ata un agente a
  una identidad *canónica*, nunca a una recién acuñada y usada para falsear la
  reconciliación de una entidad compartida. Una identidad vinculada a más de un
  agente **colapsa la atribución** al nivel de la identidad — expuesto
  honestamente como un hallazgo, nunca recuperado.
- **La gramática solo-deny está acotada por diseño.** Las reglas v1 hacen match
  solo de los atributos que realmente llegan al evaluador; las reglas de atributo
  de recurso (p. ej. sensibilidad) necesitan un seam núcleo y son un follow-up
  documentado — **no se envían como sintaxis inerte**, un campo desconocido se
  rechaza en la escritura. La política *restringe*; los grants aditivos quedan en
  el RBAC.
- **Un módulo no puede enumerar tenants.** La expiración/escalado de aprobación se
  materializa mediante un **barrido explícito y acotado por tenant** — no hay
  garantía de fondo cross-tenant, porque afirmar una sería mentir. La expiración
  efectiva se sigue honrando de forma lazy en la lectura.
:::

## Relacionado

- [Catálogo de módulos](/es/reference/modules/overview/) — dónde se sitúa el módulo VI y su estado honesto de actuación.
- [Mapa de acceso y recursos (III)](/es/reference/modules/iii-access-map/) — el consumidor cuya dependencia de atribución resuelve este módulo.
- [Referencia del bus de eventos](/es/reference/events/) — el evento `finding.reported` que emite este módulo.
- [Gobernar y aprobar](/es/how-to/govern-and-approve/) — usar las superficies de política y aprobación.
- [Visión general de la arquitectura](/es/explanation/architecture/overview/) — el motor y las capas sobre las que se compone este módulo.
- [Honestidad y límites](/es/start/honesty-and-limits/) — la postura deny-closed y detective-by-default.
