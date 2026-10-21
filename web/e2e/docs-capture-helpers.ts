// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createHash } from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { expect } from '@playwright/test'
import {
  esperarEncabezado,
  exigirInstanteLimpio,
  exigirMarcadores,
  exigirPantallaTerminada,
  filasDeDatos,
  ocultarFechas,
  ocultarMarcasDeRelease,
  restaurarFechas,
} from './capture-target'

// Index English locale keys with and without namespaces to identify untranslated text.
const CLAVES_I18N: string[] = (() => {
  const keys: string[] = []
  const recorre = (o: unknown, pre: string[]) => {
    if (o && typeof o === 'object' && !Array.isArray(o)) {
      for (const [k, v] of Object.entries(o as Record<string, unknown>)) {
        recorre(v, [...pre, k])
      }
    } else {
      keys.push(pre.join('.'))
    }
  }
  const dirs = [
    ...(fs.existsSync('src/features')
      ? fs
          .readdirSync('src/features')
          .map((f) => `src/features/${f}/i18n/en.json`)
      : []),
    ...(fs.existsSync('src/lib/i18n/locales/en')
      ? fs
          .readdirSync('src/lib/i18n/locales/en')
          .map((f) => `src/lib/i18n/locales/en/${f}`)
      : []),
  ]
  for (const f of dirs) {
    if (!fs.existsSync(f) || !f.endsWith('.json')) continue
    try {
      const d = JSON.parse(fs.readFileSync(f, 'utf8'))
      const ns = f.includes('/i18n/')
        ? f.split('/')[2]
        : path.basename(f, '.json')
      recorre(d, [])
      recorre(d, [ns])
    } catch {
      // Warn when a locale cannot be read: the resulting key index is incomplete.
      console.warn(
        `[i18n] could not read ${f}; the untranslated-key probe is incomplete`,
      )
    }
  }
  return keys
})()

// Write each image and its evidence separately to avoid concurrent workers overwriting
// a shared manifest. The runner later combines them.
export async function tomar(
  page: import('@playwright/test').Page,
  {
    id,
    theme,
    ruta,
    objetivo,
    marcadores,
    settle,
    live,
    recorte,
    actor,
    testigo,
  }: {
    id: string
    theme: string
    ruta: string
    // The declared target identifies the captured surface.
    objetivo: import('@playwright/test').Locator
    // Markers identify state that the heading cannot distinguish.
    marcadores?: import('@playwright/test').Locator[]
    settle?: number
    live?: boolean
    recorte?: (
      page: import('@playwright/test').Page,
    ) => Promise<{ x: number; y: number; width: number; height: number }>
    // Non-secret provenance of the principal, when the cell is not the demo superadmin.
    actor?: Record<string, unknown>
    // What the cell's `despues` proved about the data it photographs (the HTTP 200 read).
    testigo?: Record<string, unknown>
  },
) {
  // Wait for the declared route or modal heading; the shell heading can appear before
  // the view mounts.
  const texto = await esperarEncabezado(page, { id, ruta, objetivo })
  if (marcadores) await exigirMarcadores(id, marcadores)

  if (!live) await page.waitForLoadState('networkidle')
  if (settle) await page.waitForTimeout(settle)

  // The image outlives this release: hide the build number and the shell's live counts, then check
  // errors, dates, harness names and empty pages after loading and settling, immediately before the shot.
  const info = await page.request.get('/v1/server-info')
  expect(info.status(), `${id}: /v1/server-info`).toBe(200)
  const { version } = (await info.json()) as { version?: string }
  if (!version)
    throw new Error(`${id}: /v1/server-info reports no version to hide`)
  await ocultarMarcasDeRelease(page, version)
  // Errors, unavailable features, raw markdown and placeholders are checked on the text as the product wrote it;
  // then the dates, which are the day of the capture, are masked, and the check below refuses any left.
  await exigirPantallaTerminada(page, id)
  await ocultarFechas(page)
  await exigirInstanteLimpio(page, id, { version })

  // Record empty-panel counts and main-text size without assigning a cause to empty
  // content.
  const vacios = await page.locator('[data-slot="empty-state"]:visible').count()
  // Count empty tables separately from table-free forms; data rows come from the shared counter.
  const filas = await filasDeDatos(page)
  const tablasVacias = await page.evaluate(() => {
    const raiz = document.querySelector('main') ?? document.body
    const esDato = (tr: Element) =>
      !tr.querySelector('[data-slot="empty-state"]')
    return Array.from(raiz.querySelectorAll('table')).filter((tabla) => {
      const cabeceras = tabla.querySelectorAll('thead th').length
      const datos = Array.from(tabla.querySelectorAll('tbody tr')).filter(
        esDato,
      ).length
      return cabeceras > 0 && datos === 0
    }).length
  })
  // Match untranslated text against known locale keys rather than treating dotted
  // domain names as keys.
  const clavesCrudas = await page.evaluate((conocidas: string[]) => {
    const raiz = document.querySelector('main') ?? document.body
    const texto = (raiz as HTMLElement).innerText ?? ''
    const set = new Set(conocidas)
    // Require both a dot and a known locale key; ordinary translated words can equal
    // undotted keys.
    return texto.split(/\s+/).filter((t) => t.includes('.') && set.has(t))
  }, CLAVES_I18N)

  const hayMain = (await page.locator('main').count()) > 0
  const textoMain = hayMain
    ? (
        (await page
          .locator('main')
          .first()
          .innerText()
          .catch(() => '')) ?? ''
      ).trim().length
    : 0

  // Blur keyboard focus separately from moving the pointer.
  await page.evaluate(() =>
    (document.activeElement as HTMLElement | null)?.blur(),
  )

  const png = `playwright-report/docs/${id}-${theme}.png`
  // A declared crop preserves the viewport layout; record its rectangle with the image.
  const clip = recorte ? await recorte(page) : undefined
  await page.screenshot(clip ? { path: png, clip } : { path: png })
  // The page is reused for the other theme: give the framework its own text nodes back.
  await restaurarFechas(page)
  const sha = createHash('sha256').update(fs.readFileSync(png)).digest('hex')
  fs.writeFileSync(
    path.join('playwright-report', 'docs', `${id}-${theme}.evidence.json`),
    `${JSON.stringify(
      {
        id,
        theme,
        route: ruta,
        h1: texto,
        vacios,
        filas,
        tablas_vacias: tablasVacias,
        hay_main: hayMain,
        texto_main: textoMain,
        claves_crudas: clavesCrudas.length,
        claves_crudas_vistas: clavesCrudas.slice(0, 8),
        // Record the crop rectangle so the saved image can be interpreted.
        recorte_declarado: clip ?? null,
        ...(actor ? { actor } : {}),
        ...(testigo ? { testigo_coleccion: testigo } : {}),
        sha256: sha,
      },
      null,
      2,
    )}\n`,
  )

  // The light and dark images must have different hashes.
  if (theme === 'dark') {
    const claro = path.join(
      'playwright-report',
      'docs',
      `${id}-light.evidence.json`,
    )
    if (fs.existsSync(claro)) {
      const otro = JSON.parse(fs.readFileSync(claro, 'utf8')) as {
        sha256?: string
      }
      expect(
        sha,
        `The dark image for \`${id}\` is byte-identical to its light image: the theme did not change. ` +
          'The published pair contains two copies of the same image. Check the view’s theme ' +
          'control and whether its colors use CSS variables or fixed values.',
      ).not.toBe(otro.sha256)
    }
  }
}
