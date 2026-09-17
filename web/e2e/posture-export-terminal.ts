// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import type { Page } from '@playwright/test'

/**
 * El oráculo terminal de una exportación de postura, extraído de la escena `posture-export` de
 * `docs-captures.spec.ts` para poder probarlo. Un único helper compartido por escena y banco.
 *
 * Los tres finales NO son intercambiables: el éxito deja continuar, el fallo levanta un error que
 * lo nombra y la ausencia de terminal levanta otro. Ninguno es «hacer la foto de todos modos».
 *
 * Límite: es el oráculo de ESPERA de una captura. No prueba el export del producto —ni su API, ni
 * el fichero descargado, ni la minimización de datos—; sólo distingue esos tres finales.
 */

/** Los dos terminales, leídos de la traducción EN vigente y no copiados a mano. */
export type MensajesExport = { readonly done: string; readonly failed: string }

function mensajesEN(): MensajesExport {
  const ruta = fileURLToPath(
    new URL('../src/features/posture-export/i18n/en.json', import.meta.url),
  )
  const json = JSON.parse(readFileSync(ruta, 'utf8')) as {
    export?: { done?: unknown; failed?: unknown }
  }
  const done = json.export?.done
  const failed = json.export?.failed
  if (
    typeof done !== 'string' ||
    typeof failed !== 'string' ||
    !done ||
    !failed
  )
    throw new Error(
      `posture-export: ${ruta} no trae \`export.done\` y \`export.failed\` como cadenas; ` +
        'sin los dos literales del producto este oráculo no puede distinguir éxito de fallo.',
    )
  // Si un terminal contuviera al otro, una sonda por texto los confundiría. Comprobarlo aquí lo
  // dice al arrancar y no en la foto.
  if (done.includes(failed) || failed.includes(done))
    throw new Error(
      `posture-export: los dos terminales EN no son distinguibles por texto ` +
        `(«${done}» / «${failed}»): uno contiene al otro.`,
    )
  return { done, failed }
}

export const MENSAJES_EXPORT: MensajesExport = mensajesEN()

/** Plazo por defecto: el mismo que la escena de captura usaba antes de extraer este helper. */
export const PLAZO_TERMINAL_MS = 15_000

/**
 * Espera a que la exportación llegue a un estado TERMINAL y sólo vuelve si fue un ÉXITO.
 *
 * @throws si aparece el terminal de fallo, o si en `plazoMs` no aparece ninguno de los dos.
 */
export async function esperarExportacionDePostura(
  page: Page,
  opciones: { plazoMs?: number } = {},
): Promise<void> {
  const plazoMs = opciones.plazoMs ?? PLAZO_TERMINAL_MS
  const { done, failed } = MENSAJES_EXPORT
  const exito = page.getByText(done, { exact: true })
  const fallo = page.getByText(failed, { exact: true })

  try {
    // `or` espera a los dos a la vez: el primero que aparezca decide, sin agotar el plazo del
    // éxito antes de mirar el fallo.
    await exito
      .or(fallo)
      .first()
      .waitFor({ state: 'visible', timeout: plazoMs })
  } catch {
    throw new Error(
      `posture-export: en ${plazoMs} ms la exportación no llegó a NINGÚN estado terminal ` +
        `(ni «${done}» ni «${failed}»). No se captura un estado intermedio: una foto tomada ` +
        'aquí enseñaría la pantalla a medio exportar como si fuese el resultado.',
    )
  }

  if (await fallo.first().isVisible()) {
    // El producto pone el mensaje del error en la descripción del toast (`posture-export-view.tsx`,
    // `onError`); si está, viaja en el error en vez de reconstruirse desde una traza.
    const tarjeta = page
      .locator('[data-sonner-toast]')
      .filter({ hasText: failed })
      .first()
    const desc = tarjeta.locator('[data-description]')
    const detalle =
      (await desc.count()) > 0 ? (await desc.first().innerText()).trim() : ''
    throw new Error(
      `posture-export: la exportación FALLÓ («${failed}»)${detalle ? ` — ${detalle}` : ''}. ` +
        'Un fallo NO se fotografía como éxito: la escena documenta una exportación lograda.',
    )
  }

  if (!(await exito.first().isVisible()))
    throw new Error(
      `posture-export: se observó un terminal que no es ni «${done}» ni «${failed}» visible al ` +
        'comprobarlo. No se sigue a la captura sin el éxito a la vista.',
    )
}
