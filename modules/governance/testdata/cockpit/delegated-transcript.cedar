// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// v3 §7.3 — delegación positiva de la lectura de transcript.
//
// Habilita a un principal explícitamente delegado (por ejemplo un viewer) sobre las
// sesiones de un agent-group. NO reduce a un editor: un permit no estrecha. El forbid
// de departamento, si aplica, sigue prevaleciendo — forbid-overrides-permit.
//
// `action ==` lleva el PERMISO DE RUTA porque hoy actionUID lo construye desde
// req.Permission. Cuando aterrice el seam de acción por ruta cambia este lado
// izquierdo y nada más.
permit(
  principal,
  action == Action::"session-cockpit:transcript:read",
  resource
)
when {
  resource.kind == "session" &&
  resource in AgentGroup::"finance-ops"
};
