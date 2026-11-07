// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The edition summaries vite.config.ts quotes from docs/editions.md at build time, one
// item per line. Keep the capability wording in the edition contract, rather than
// maintaining a second catalogue in the console. These are English documentation excerpts.
declare const __EDITION_SUMMARIES__: {
  business: string[]
  enterprise: string[]
}
