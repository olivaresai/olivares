// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** Dictated text joins what is already written, one space apart. */
export function joinDictation(draft: string, text: string): string {
  if (!draft.trim()) return text
  return /\s$/.test(draft) ? `${draft}${text}` : `${draft} ${text}`
}
