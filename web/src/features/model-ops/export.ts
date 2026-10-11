// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Model-ops document exports. The AIBOM (CycloneDX 1.6 / SPDX 3.0.1) and the model card
// (JSON) are fetched as JSON through the shared client and saved verbatim as files; the
// model-card Markdown is a text/markdown body read through the client's raw download.
// Nothing is rebuilt in the browser: the engine produces every byte; the web only saves
// what it received (ARCHITECTURE.md).
import { apiFetchRaw } from '@/lib/api/client'
import { downloadBlob } from '@/lib/api/download'

export { downloadBlob }

const BASE = '/v1/m/models'

/** Save an already-fetched JSON document as a pretty-printed .json file. */
export function downloadJson(doc: unknown, filename: string): void {
  downloadBlob(
    new Blob([JSON.stringify(doc, null, 2)], { type: 'application/json' }),
    filename,
  )
}

/** Fetch the model-card Markdown export (text/markdown) as a Blob of the engine's bytes. */
export async function fetchModelCardMarkdown(ownedId: string): Promise<Blob> {
  const res = await apiFetchRaw(
    `${BASE}/owned-models/${ownedId}/model-card?format=md`,
    { headers: { Accept: 'text/markdown' } },
  )
  return res.blob()
}
