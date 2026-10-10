// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Browser export kept separate so tests can pin which opaque BOM object is saved.

import { downloadBlob } from '@/lib/api/download'

/** Download an opaque JSON document without rebuilding or normalizing it. */
export function downloadJson(documentValue: unknown, filename: string): void {
  const blob = new Blob([JSON.stringify(documentValue, null, 2)], {
    type: 'application/json',
  })
  downloadBlob(blob, filename)
}
