// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { WorkDependency, WorkSnapshot } from '@/features/work/types'

/** Native snapshots retain dependency history after removal (work_mutation.go).
 * Reflect the owner's persisted active flag; older snapshots had no such flag. */
export function activeWorkDependencies(
  snapshot: WorkSnapshot,
): WorkDependency[] {
  return snapshot.dependencies.filter((row) => {
    if (!('active' in row)) return true
    if (typeof row.active !== 'boolean')
      throw new TypeError('Invalid dependency state')
    return row.active
  })
}
