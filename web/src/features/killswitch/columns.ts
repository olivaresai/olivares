// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Column class for the kill switch tables, applied through `DataTable`'s own
// `meta.className` (it reaches both the header and the cells of the column).

/** A column whose header is much longer than its values (a count, a badge): the header wraps
 *  to two lines so the column is as wide as its values, not as its title. Measured in es at
 *  1440 px: the minimum-severity title held 173 px over a one-word badge. `meta.className` also reaches
 *  the cells, which lifts DataTable's one-line cell rule for this column; its values are
 *  single words. */
export const WRAP_HEADER = { className: 'whitespace-normal' }
