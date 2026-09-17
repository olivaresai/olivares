// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// v3 §7.3 — confinamiento por departamento.
//
// Es un FORBID-UNLESS y no un permit, y ésa es toda la lección del hallazgo 07: bajo
// Allow = (RBAC ∨ Grant) ∧ ¬Forbid un permit es POSITIVO — puede autorizar, nunca
// estrecha a un editor al que RBAC ya autorizó. Todo confinamiento tiene que ser un
// forbid, o retirar el término RBAC (RequireScopedGrant).
//
// LADO DEL PRINCIPAL: `Group::` (grupo de DIRECTORIO). El principal recibe un
// parent Group:: por cada grupo del que es miembro; NUNCA recibe un AgentGroup, así que
// `principal in AgentGroup::…` —como escribe la v3— no casaría con nadie.
//
// LADO DEL RECURSO: `AgentGroup::`, que agrupa AGENTES. Alcanza una sesión porque el
// resolver le da a una Session los grupos de su agente como padres. Sin ese paso
// transitivo la regla no casa con NINGUNA sesión, el unless nunca se cumple y el forbid
// deniega a todo el mundo.
//
// <<DEPARTMENT_MEMBERS_GROUP_ID>> lo sustituye la batería por el id real del grupo: este
// motor referencia los grupos de directorio por ID, no por slug.
forbid(
  principal in Group::"<<DEPARTMENT_MEMBERS_GROUP_ID>>",
  action,
  resource
)
when { resource.kind == "session" }
unless { resource in AgentGroup::"finance-ops" };
