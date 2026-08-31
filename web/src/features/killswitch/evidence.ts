// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Evidence-pack download helpers (kept out of components.tsx so that file stays
// component-only for fast refresh).
import type { EvidencePack } from './types'

/** Evidence-pack filename for one stop. */
export function evidenceFilename(id: string): string {
  return `killswitch-${id}-evidence.json`
}

/** Trigger the browser download of the JSON evidence pack (blob pattern shared
 *  with the FinOps FOCUS / compliance evidence exports). The bytes ARE the
 *  evidence — serialize the pack verbatim, pretty-printed for the IR reader. */
export function downloadEvidencePack(id: string, pack: EvidencePack): void {
  const blob = new Blob([JSON.stringify(pack, null, 2)], {
    type: 'application/json',
  })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = evidenceFilename(id)
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}
