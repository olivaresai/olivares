// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** The extension routes a docs capture adds to its hand-written list. A route is dropped when its
 *  path is parametric (a `$param` would navigate to the literal) or when a hand entry already
 *  captures it. */
export function rutasExtraDeCaptura<R extends { id: string; path: string }>(
  rutas: readonly R[],
  mano: readonly { id: string; path: string }[],
): R[] {
  return rutas.filter(
    (ruta) =>
      !ruta.path.includes('$') &&
      !mano.some((vista) => vista.path === ruta.path),
  )
}
