// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// v3 §7.3 — los dos permits positivos del terminal gobernado.
//
// Ningún rol, incluido tenant admin, obtiene terminal remoto por amplitud RBAC: las
// rutas de shell:open e input:write declaran RequireScopedGrant, que retira el término
// RBAC, de modo que la decisión queda Grant ∧ ¬Forbid ∧ ¬deny-overlay y HACE FALTA uno
// de estos permits.
//
// El target va en atributos del recurso porque el servidor los materializa desde
// bindings administrativos, nunca desde el cuerpo de la petición. `context.aal >= 3` es
// la precondición de nivel de autenticación; se escribe también aquí para que una policy
// mal copiada no sea el único sitio donde AAL3 se exige.
//
// `unix_uid` se compara como CADENA porque Resource.Extra es map[string]string y el
// resolver copia esos pares como cedar.String (baseResourceAttrs). Escribirlo como
// entero haría que la condición ERRE y Cedar SALTE la regla en silencio: el permit no
// concedería y nadie vería un error. Ese silencio es justo el modo de fallo que
// baseResourceAttrs documenta para los atributos ausentes.
permit(
  principal in Group::"<<TERMINAL_OPERATORS_GROUP_ID>>",
  action == Action::"session-cockpit:shell:write",
  resource
)
when {
  resource.kind == "shell_target" &&
  resource.host_id == "host-prod-01" &&
  resource.unix_uid == "1007" &&
  resource.classification == "internal" &&
  resource.shell_enum == "bash" &&
  context.aal >= 3
};

permit(
  principal in Group::"<<TERMINAL_OPERATORS_GROUP_ID>>",
  action == Action::"session-cockpit:input:write",
  resource
)
when {
  resource.kind == "input_target" &&
  resource.host_id == "host-prod-01" &&
  resource.unix_uid == "1007" &&
  resource.pane_generation == "pg_opaque_42" &&
  resource.classification == "internal" &&
  context.aal >= 3
};
