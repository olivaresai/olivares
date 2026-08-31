// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Model-ops document exports. The AIBOM (CycloneDX 1.6 / SPDX 3.0.1) and the model card
// (JSON) are fetched as JSON through the shared client and saved verbatim as files; the
// model-card Markdown is a text/markdown stream the JSON client cannot consume, so it
// uses a raw same-origin fetch that carries the SAME bearer/tenant headers and surfaces a
// typed ApiError on 4xx/5xx (the audit-export idiom). Nothing is rebuilt in the browser:
// the engine produces every byte; the web only saves what it received (ARCHITECTURE.md).
import { ensureFreshSession, notifyUnauthorized } from '@/lib/api/client'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'

const BASE = '/v1/m/models'

/** Trigger a browser download of a Blob (the shared anchor pattern). */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

/** Save an already-fetched JSON document as a pretty-printed .json file. */
export function downloadJson(doc: unknown, filename: string): void {
  downloadBlob(
    new Blob([JSON.stringify(doc, null, 2)], { type: 'application/json' }),
    filename,
  )
}

/** Fetch the model-card Markdown export (text/markdown) as a Blob — a raw same-origin
 *  fetch, since the JSON client cannot consume a non-JSON body. */
export async function fetchModelCardMarkdown(ownedId: string): Promise<Blob> {
  // La renovación va ANTES de la petición: este camino rodea `apiFetch`, así que sin esto
  // sería el único del console que sigue muriendo por caducidad. Comparte el vuelo único.
  // ⛔ EL INQUILINO SE LEE ANTES DE LA ESPERA. Este camino rodea `apiFetch`, así que no
  //    hereda la fijación de `apiFetchWithMeta`: si se leyera después del refresco, un
  //    cambio de inquilino durante la renovación mandaría la petición al inquilino nuevo.
  const tenant = useTenantStore.getState().activeTenant
  await ensureFreshSession()
  const headers = new Headers({ Accept: 'text/markdown' })
  const token = useSessionStore.getState().token
  if (token) headers.set('Authorization', `Bearer ${token}`)
  if (tenant) headers.set('X-Olivares-Tenant', tenant)

  let res: Response
  try {
    res = await fetch(`${BASE}/owned-models/${ownedId}/model-card?format=md`, {
      method: 'GET',
      headers,
      credentials: 'same-origin',
    })
  } catch (cause) {
    throw new NetworkError('The control plane is unreachable.', cause)
  }
  if (!res.ok) {
    // A 401 means the session expired/was revoked — route to login like the JSON client
    // does (this raw path skips the apiFetch onUnauthorized hook).
    if (res.status === 401) notifyUnauthorized()
    throw new ApiError(
      res.status,
      'model_card_export_failed',
      res.statusText || 'Model-card export failed',
      res.headers.get('X-Request-ID') ?? undefined,
    )
  }
  return res.blob()
}
