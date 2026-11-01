// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** The page of a GitHub issue or pull request a session was started from, as the engine
 * stores it (modules/sessions session_source_item.go URL). */
const ITEM_PAGE =
  /^https:\/\/github\.com\/([A-Za-z0-9._-]+)\/([A-Za-z0-9._-]+)\/(?:issues|pull)\/([1-9][0-9]*)$/

/** `owner/repo#17` for a GitHub item page, else null: a link is made only for a page this
 * names, never for whatever a stored value happens to hold. */
export function sourceItemLabel(url: string | undefined): string | null {
  const m = url ? ITEM_PAGE.exec(url) : null
  return m ? `${m[1]}/${m[2]}#${m[3]}` : null
}
