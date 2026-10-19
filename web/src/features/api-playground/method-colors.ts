// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** The method badge colours: status tokens on their soft fills, AA in both themes
 * (tokens.test.ts). Orange is not a method colour: it marks the one thing that needs you. */
export const METHOD_COLORS: Record<string, string> = {
  GET: 'bg-success-soft text-success',
  POST: 'bg-info-soft text-info',
  PUT: 'bg-warning-soft text-warning',
  PATCH: 'bg-warning-soft text-warning',
  DELETE: 'bg-danger-soft text-danger',
}
