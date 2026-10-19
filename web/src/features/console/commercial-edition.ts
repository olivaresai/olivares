// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
type EditionFacts = {
  edition?: string
  license?: { status: string; support_profile?: string }
}

/** A build is not a licence. Only current valid commercial facts get a badge. */
export function commercialEdition(
  info: EditionFacts | undefined,
): 'business' | 'enterprise' | null {
  if (
    !info ||
    !['business', 'enterprise'].includes(info.edition ?? '') ||
    info.license?.status !== 'valid'
  )
    return null
  return info.license.support_profile === 'enterprise'
    ? 'enterprise'
    : 'business'
}
