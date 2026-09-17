// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import {
  MENSAJES_EXPORT,
  esperarExportacionDePostura,
} from './posture-export-terminal'

/**
 * El banco del oráculo terminal de `posture-export`, sobre un fixture DOM local con los tres
 * finales posibles.
 *
 * Qué NO acredita: un fixture no prueba un servicio. No valida la exportación del producto —ni su
 * API, ni el fichero descargado, ni la minimización de datos, ni que el toast real aparezca cuando
 * debe—; sólo que la espera decide distinto delante de cada final.
 *
 * El fixture imita el marcado de `sonner` (`[data-sonner-toast]`, `[data-title]`,
 * `[data-description]`) que el producto monta vía `@/components/ui/toaster`, y sus literales salen
 * de `MENSAJES_EXPORT`, o sea de la traducción EN vigente.
 */

// El mismo pin que `playwright.communications.config.ts` define para esta caja; sin la variable,
// Playwright usa su propio Chromium.
test.use(
  process.env.K3_E2E_CHROMIUM
    ? { launchOptions: { executablePath: process.env.K3_E2E_CHROMIUM } }
    : {},
)

type Escena = 'exito' | 'fallo' | 'sin-terminal' | 'intermedio-y-exito'

const DETALLE_FALLO = 'HTTP 503: posture snapshot unavailable'

/** Una página con el botón de la escena y el final que se le pida, tras `retardoMs`. */
function fixture(escena: Escena, retardoMs: number): string {
  const datos = JSON.stringify({
    escena,
    retardoMs,
    exito: MENSAJES_EXPORT.done,
    fallo: MENSAJES_EXPORT.failed,
    detalle: DETALLE_FALLO,
  })
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>posture-export fixture</title></head>
<body>
  <h1>Posture export</h1>
  <button id="exportar">Export posture</button>
  <ol id="toasts"></ol>
  <script>
    const C = ${datos}
    function toast(tipo, titulo, descripcion) {
      const li = document.createElement('li')
      li.setAttribute('data-sonner-toast', '')
      li.setAttribute('data-type', tipo)
      const contenido = document.createElement('div')
      const t = document.createElement('div')
      t.setAttribute('data-title', '')
      t.textContent = titulo
      contenido.appendChild(t)
      if (descripcion) {
        const d = document.createElement('div')
        d.setAttribute('data-description', '')
        d.textContent = descripcion
        contenido.appendChild(d)
      }
      li.appendChild(contenido)
      document.getElementById('toasts').appendChild(li)
    }
    document.getElementById('exportar').addEventListener('click', function () {
      // El estado INTERMEDIO: la pantalla ya no está como nació, y sigue sin haber resultado.
      const p = document.createElement('p')
      p.id = 'intermedio'
      p.textContent = 'Exporting…'
      document.body.appendChild(p)
      if (C.escena === 'sin-terminal') return
      setTimeout(function () {
        if (C.escena === 'fallo') toast('error', C.fallo, C.detalle)
        else toast('success', C.exito)
      }, C.retardoMs)
    })
  </script>
</body></html>`
}

/** Devuelve el error que la espera levantó, o `null` si volvió sin levantar ninguno. */
async function capturar(promesa: Promise<void>): Promise<Error | null> {
  try {
    await promesa
    return null
  } catch (e) {
    return e instanceof Error ? e : new Error(String(e))
  }
}

test.describe('la espera terminal de posture-export', () => {
  test('una exportación con ÉXITO deja continuar a la captura', async ({
    page,
  }) => {
    await page.setContent(fixture('exito', 120))
    await page.getByRole('button', { name: 'Export posture' }).click()
    const error = await capturar(
      esperarExportacionDePostura(page, { plazoMs: 5_000 }),
    )
    expect(error?.message ?? '(sin error)').toBe('(sin error)')
    await expect(
      page.getByText(MENSAJES_EXPORT.done, { exact: true }).first(),
    ).toBeVisible()
  })

  test('un FALLO real se rechaza con un error que lo nombra, y no pasa por éxito', async ({
    page,
  }) => {
    await page.setContent(fixture('fallo', 120))
    await page.getByRole('button', { name: 'Export posture' }).click()
    const error = await capturar(
      esperarExportacionDePostura(page, { plazoMs: 5_000 }),
    )
    expect(
      error,
      'la espera tiene que levantar un error con el fallo delante',
    ).not.toBeNull()
    expect(error?.message).toContain(MENSAJES_EXPORT.failed)
    // El diagnóstico del producto viaja en el error, en vez de reconstruirse desde la traza.
    expect(error?.message).toContain(DETALLE_FALLO)
    expect(error?.message).not.toContain(MENSAJES_EXPORT.done)
  })

  test('sin ningún terminal, se agota el plazo y se rechaza — no se captura el intermedio', async ({
    page,
  }) => {
    await page.setContent(fixture('sin-terminal', 0))
    await page.getByRole('button', { name: 'Export posture' }).click()
    await expect(page.locator('#intermedio')).toBeVisible()
    const t0 = Date.now()
    const error = await capturar(
      esperarExportacionDePostura(page, { plazoMs: 700 }),
    )
    const gastado = Date.now() - t0
    expect(error, 'un plazo agotado NO es un resultado').not.toBeNull()
    expect(error?.message).toContain('700 ms')
    // El plazo es de verdad configurable: si se ignorase, esto tardaría los 15 s por defecto.
    expect(gastado).toBeLessThan(10_000)
  })

  test('un intermedio que TERMINA en éxito se espera hasta el final', async ({
    page,
  }) => {
    await page.setContent(fixture('intermedio-y-exito', 900))
    await page.getByRole('button', { name: 'Export posture' }).click()
    await expect(page.locator('#intermedio')).toBeVisible()
    const error = await capturar(
      esperarExportacionDePostura(page, { plazoMs: 5_000 }),
    )
    expect(error?.message ?? '(sin error)').toBe('(sin error)')
  })

  /**
   * Control causal: compara la sonda que había con la que salía de «quitarle un espacio», contra
   * los literales EN vigentes. Es lo que muestra que el arreglo no es cosmético, y no necesita
   * navegador porque la sonda era una regex sobre un texto.
   */
  test('la sonda anterior no veía el fallo, y quitarle un espacio lo habría aceptado como terminal', () => {
    const { done, failed } = MENSAJES_EXPORT
    // Los dos espacios van como ` {2}`: la misma expresión en la forma que `no-regex-spaces` exige.
    // El defecto era esperar dos espacios donde el producto pone uno, no cómo se deletrean.
    const anterior = /Posture export(ed| {2}failed)/i
    const cosmetica = /Posture export(ed| failed)/i

    expect(failed, 'el literal EN del fallo lleva UN espacio').toContain(
      'export failed',
    )
    expect(
      anterior.test(failed),
      'con dos espacios, un fallo real no se observaba: la escena moría por timeout',
    ).toBe(false)
    expect(anterior.test(done)).toBe(true)

    expect(
      cosmetica.test(failed),
      'con el espacio corregido, el fallo cumple la espera y la captura seguiría adelante',
    ).toBe(true)
    expect(cosmetica.test(done)).toBe(true)
  })
})
