// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/**
 * Formatea un recuento del resumen de workspace diciendo si es un SUELO.
 *
 * ⛔ LA REGLA VIVE AQUÍ Y NO EN LAS SEIS LLAMADAS. El panel pinta los cuatro recuentos en seis
 *    sitios (cuatro fichas y dos cabeceras de tarjeta); escrita seis veces, basta con que una se
 *    quede sin actualizar para que la misma cifra salga honesta arriba y mintiendo abajo. Con una
 *    función, un solo mutante la mata en los seis.
 *
 * ⛔ Y EL «≥» SÓLO SI EL MOTOR LO HA DICHO. `capped === true` es la única puerta: `false`,
 *    `undefined` (motor anterior a `#1647`) o un campo ausente pintan el número EXACTO. Poner el
 *    «≥» sobre un recuento que sí es total es el error simétrico del que esto corrige, y engaña
 *    igual — sólo que hacia el otro lado.
 */
export function cuentaConSuelo(
  n: number,
  capped: boolean | undefined,
  formatea: (n: number) => string,
): string {
  return capped === true ? `≥ ${formatea(n)}` : formatea(n)
}
