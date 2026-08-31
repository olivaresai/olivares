// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DR-passphrase policy, mirrored from the backend. The DR bundle carries
// the estate's signing keys and the passphrase is its ONLY protection at rest,
// so core/api/dr_handler.go enforces a ≥12-character floor (in runes) when a
// NEW bundle is encrypted. These helpers give the backup form the SAME rule
// client-side (a Field error before the request) plus an honest strength hint —
// the backend remains the source of truth. Restore must accept legacy bundles
// created with shorter passphrases.

/** The backend's floor (core/api/dr_handler.go minDRPassphraseLen). */
export const MIN_DR_PASSPHRASE_LENGTH = 12

/** Rune count (what the backend counts) — NOT UTF-16 units ('𝔞'.length === 2). */
export function passphraseLength(passphrase: string): number {
  return [...passphrase].length
}

/** True when a non-empty passphrase is under the backend floor (a Field error).
 *  Empty is NOT flagged here — "required" is the form's own, distinct error. */
export function passphraseBelowFloor(passphrase: string): boolean {
  return (
    passphrase.length > 0 &&
    passphraseLength(passphrase) < MIN_DR_PASSPHRASE_LENGTH
  )
}

export type PassphraseStrength = 'weak' | 'fair' | 'good' | 'strong'

/**
 * A deliberately modest strength heuristic (length bands + character variety) —
 * an honest hint, not an entropy claim (no dictionary/zxcvbn analysis). Anything
 * under the floor is 'weak' by definition.
 */
export function passphraseStrength(passphrase: string): PassphraseStrength {
  const len = passphraseLength(passphrase)
  if (len < MIN_DR_PASSPHRASE_LENGTH) return 'weak'
  const classes =
    Number(/[a-z]/.test(passphrase)) +
    Number(/[A-Z]/.test(passphrase)) +
    Number(/[0-9]/.test(passphrase)) +
    Number(/[^a-zA-Z0-9]/.test(passphrase))
  // Length is the dominant factor for a memorized secret; variety breaks ties.
  let score = 1 // at the floor: fair
  if (len >= 16) score++
  if (len >= 20 || (len >= 16 && classes >= 3)) score++
  if (classes >= 3 && score < 3) score = Math.min(score + 1, 3)
  return (['weak', 'fair', 'good', 'strong'] as const)[score]
}
