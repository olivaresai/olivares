// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The surface a capture accredits is declared by its caller, never inferred from
// what the DOM happens to contain. The app shell exposes headings of its own
// before a lazy route mounts, and a dialog can hold more than one heading, so a
// resolver that picks among them can accredit the wrong screen.
//
// `capture-target.spec.ts` drives these same functions over synthetic DOM.
import { expect, type Locator, type Page } from '@playwright/test'

export const PLAZO_OBJETIVO = 60_000

// The error route renders its own H1, which is how it stays distinguishable from
// any declared target.
export const paginaDeError = (page: Page): Locator =>
  page.getByRole('heading', { level: 1, name: /^Page not found$/i })

// A page take. Global, not scoped to `main`: login, setup and the status page
// render their H1 outside the app shell and do not use PageHeader.
export const encabezadoDePagina = (
  page: Page,
  nombre: RegExp | string,
): Locator => page.getByRole('heading', { level: 1, name: nombre })

// A dialog take whose title is a fixed string. Selected by exact name because a
// dialog can carry several headings — StepUpHost renders the DialogTitle and the
// panel's own CardTitle, both h2.
export const encabezadoDeDialogo = (page: Page, nombre: string): Locator =>
  page.getByRole('dialog').getByRole('heading', { name: nombre, exact: true })

// A dialog take whose title is dynamic (the session and workspace sheets). The
// heading is resolved through the dialog's `aria-labelledby`, so the surface is
// named by the accessibility tree instead of by heading order.
//
// The dialog must already be unique and labelled: that is waited on, and the
// attribute is only read afterwards.
export async function encabezadoDelDialogoAbierto(
  page: Page,
): Promise<Locator> {
  const dialogo = page.locator('[role="dialog"][aria-labelledby]')
  await expect(
    dialogo,
    'no hay un único diálogo abierto que declare aria-labelledby',
  ).toBeVisible({ timeout: PLAZO_OBJETIVO })
  const id = await dialogo.getAttribute('aria-labelledby')
  if (!id) throw new Error('el diálogo abierto no declara aria-labelledby')
  return page.locator(`[id="${id}"]`)
}

/**
 * Waits for the declared surface and returns its text.
 *
 * No `.first()` anywhere: an ambiguous target fails on strictness rather than
 * being resolved by DOM order. `or(paginaDeError)` keeps the Page-not-found floor
 * reachable within the same wait, and if both were visible the union would match
 * two elements and fail too — the error is never chosen over the target.
 */
export async function esperarEncabezado(
  page: Page,
  {
    id,
    ruta,
    objetivo,
    // Las celdas reales no lo pasan: usan el plazo de siempre. Solo los controles sinteticos lo
    // acortan, para que un caso negativo no gaste el plazo entero en demostrar que no aparece.
    plazo = PLAZO_OBJETIVO,
  }: { id: string; ruta: string; objetivo: Locator; plazo?: number },
): Promise<string> {
  const error = paginaDeError(page)
  await expect(
    objetivo.or(error),
    `${id}: no apareció el encabezado declarado para ${ruta}`,
  ).toBeVisible({ timeout: plazo })
  expect(
    await error.isVisible(),
    `${id}: la ruta ${ruta} sirvió la página de ERROR, no la vista`,
  ).toBe(false)
  return ((await objetivo.textContent()) ?? '').trim()
}

// Selection markers a caller needs on top of its heading: the tab that must be
// selected, an overlay that must be open. Retryable, so a slow panel is not a
// false accusation.
export async function exigirMarcadores(
  id: string,
  marcadores: Locator[],
): Promise<void> {
  for (const marcador of marcadores) {
    await expect(
      marcador,
      `${id}: no se cumplió un marcador de selección declarado por la celda`,
    ).toBeVisible({ timeout: PLAZO_OBJETIVO })
  }
}

/**
 * The shutter-time checks, run after settle and immediately before the shot.
 *
 * `count()` is correct here and is not the premature sampling this module
 * removes: these demand ABSENCE at the instant of the photo, and retrying would
 * give the indicator time to disappear.
 */
export async function exigirInstanteLimpio(
  page: Page,
  id: string,
): Promise<void> {
  const boundary = await page
    .getByText('This view crashed', { exact: false })
    .count()
  expect(
    boundary,
    `${id}: la vista cayó al error boundary DESPUÉS de cargar — la captura mostraría el fallo`,
  ).toBe(0)
  const cargando = await page.locator('[role="progressbar"]:visible').count()
  expect(
    cargando,
    `${id}: quedaba un indicador de carga visible al disparar — sube \`settle\` para esta vista`,
  ).toBe(0)
}
