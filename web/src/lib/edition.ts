// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/**
 * Whether a server-info `edition` names a Community build: a build fact, not a licence. A
 * Community engine answers the Business routes with 501, so a page asks `isCommunityEdition`
 * instead of probing for the refusal. Only an answer that says "community" counts: an absent
 * edition (an embedder that names none) is unknown, and the page then asks as it always did.
 */
export function isCommunityEdition(edition: string | undefined): boolean {
  return edition === 'community'
}
