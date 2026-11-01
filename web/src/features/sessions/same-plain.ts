// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** Plain values (strings, numbers, booleans, arrays, plain objects) are equal when their contents are; anything else
 * (functions, maps) only when it is the same value. The conversation rows compare their props with it: each new frame maps the
 * whole conversation again into new objects, so identity alone would paint every row again. */
export function samePlain(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true
  if (
    typeof a !== 'object' ||
    typeof b !== 'object' ||
    a === null ||
    b === null
  )
    return false
  if (Array.isArray(a))
    return (
      Array.isArray(b) &&
      a.length === b.length &&
      a.every((v, i) => samePlain(v, b[i]))
    )
  const plain = (v: object) =>
    [Object.prototype, null].includes(Object.getPrototypeOf(v))
  if (Array.isArray(b) || !plain(a) || !plain(b)) return false
  const ka = Object.keys(a)
  const ra = a as Record<string, unknown>
  const rb = b as Record<string, unknown>
  return (
    ka.length === Object.keys(b).length &&
    ka.every((k) => Object.hasOwn(b, k) && samePlain(ra[k], rb[k]))
  )
}
