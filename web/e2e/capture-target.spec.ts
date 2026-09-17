// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Permanent controls for the capture oracle. They drive the SAME functions the
// capture cells use — nothing is reimplemented here — over synthetic DOM, so each
// invariant is pinned without booting an engine or seeding an estate.
//
// Synthetic DOM is for these controls only. The real captures keep their engine,
// seed, login and API, with no mocked responses.
import { expect, test } from '@playwright/test'

import {
  encabezadoDeDialogo,
  encabezadoDelDialogoAbierto,
  encabezadoDePagina,
  esperarEncabezado,
  exigirInstanteLimpio,
  exigirMarcadores,
} from './capture-target'

// Short enough that a negative control does not spend the real 60 s proving an
// absence. The capture cells never pass it.
const PLAZO_CONTROL = 1_500

const monta = (page: import('@playwright/test').Page, cuerpo: string) =>
  page.setContent(
    `<!DOCTYPE html><html lang="en"><body>${cuerpo}</body></html>`,
  )

// The shell, which is what an oracle looking for "some heading" would find first.
const SHELL = `
  <aside aria-label="Primary">
    <h2>Favorites</h2>
    <h2>Recent</h2>
  </aside>`

test.describe('capture target — páginas', () => {
  test('espera al H1 de la celda aunque el shell ya tenga los suyos', async ({
    page,
  }) => {
    await monta(
      page,
      `${SHELL}
      <main id="vista"><div role="progressbar">cargando</div></main>
      <script>
        setTimeout(() => {
          document.getElementById('vista').innerHTML = '<h1>Identity &amp; NHI</h1>'
        }, 400)
      </script>`,
    )

    // La reproducción: en este instante el primer encabezado del documento es del shell, que es
    // exactamente lo que hacía fallar al oráculo anterior.
    expect(await page.getByRole('heading').first().textContent()).toBe(
      'Favorites',
    )

    expect(
      await esperarEncabezado(page, {
        id: 'identity',
        ruta: '/identity',
        objetivo: encabezadoDePagina(page, /^Identity & NHI$/),
      }),
    ).toBe('Identity & NHI')
  })

  test('acredita una superficie sin landmark main', async ({ page }) => {
    // El objetivo es global, no está acotado a `main`. Es el límite de compatibilidad del
    // resolver, no una afirmación sobre ninguna vista concreta del producto.
    await monta(page, '<h1>Sign in</h1>')
    expect(
      await esperarEncabezado(page, {
        id: 'login',
        ruta: '/login',
        objetivo: encabezadoDePagina(page, /^Sign in$/),
      }),
    ).toBe('Sign in')
  })

  test('rechaza la vista equivocada', async ({ page }) => {
    await monta(page, `${SHELL}<main><h1>MCP &amp; skills</h1></main>`)
    await expect(
      esperarEncabezado(page, {
        id: 'identity',
        ruta: '/identity',
        objetivo: encabezadoDePagina(page, /^Identity & NHI$/),
        plazo: PLAZO_CONTROL,
      }),
    ).rejects.toThrow(/no apareció el encabezado declarado/)
  })

  test('rechaza un objetivo ambiguo en vez de elegir el primero', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Identity &amp; NHI</h1><h1>Identity &amp; NHI</h1></main>',
    )
    await expect(
      esperarEncabezado(page, {
        id: 'identity',
        ruta: '/identity',
        objetivo: encabezadoDePagina(page, /^Identity & NHI$/),
        plazo: PLAZO_CONTROL,
      }),
    ).rejects.toThrow(/strict mode violation/)
  })
})

test.describe('capture target — página de error', () => {
  test('la nombra en vez de agotar el plazo', async ({ page }) => {
    await monta(page, `${SHELL}<main><h1>Page not found</h1></main>`)
    await expect(
      esperarEncabezado(page, {
        id: 'identity',
        ruta: '/identity',
        objetivo: encabezadoDePagina(page, /^Identity & NHI$/),
        plazo: PLAZO_CONTROL,
      }),
    ).rejects.toThrow(/sirvió la página de ERROR/)
  })

  test('con objetivo y error a la vez falla, no elige', async ({ page }) => {
    await monta(
      page,
      '<main><h1>Identity &amp; NHI</h1><h1>Page not found</h1></main>',
    )
    await expect(
      esperarEncabezado(page, {
        id: 'identity',
        ruta: '/identity',
        objetivo: encabezadoDePagina(page, /^Identity & NHI$/),
        plazo: PLAZO_CONTROL,
      }),
    ).rejects.toThrow(/strict mode violation/)
  })
})

test.describe('capture target — diálogos', () => {
  // El h2 que NO etiqueta al diálogo va primero a propósito: así el control distingue el resolver
  // declarado de uno que tomase el primer encabezado del diálogo.
  const MODAL = `
    <h1 aria-hidden="true">Control console</h1>
    <div role="dialog" aria-labelledby="titulo-dialogo">
      <h2>Step-up authentication required</h2>
      <h2 id="titulo-dialogo">This action needs an elevated session</h2>
    </div>`

  test('elige el título que nombra al diálogo, no el primer h2', async ({
    page,
  }) => {
    await monta(page, MODAL)

    // El fondo queda fuera del árbol de accesibilidad, que es la razón de que un modal no pueda
    // acreditarse por el H1 de la vista.
    expect(await page.getByRole('heading', { level: 1 }).count()).toBe(0)
    // La reproducción: el primer encabezado del diálogo es el del panel, no el del diálogo.
    expect(
      await page.getByRole('dialog').getByRole('heading').first().textContent(),
    ).toBe('Step-up authentication required')

    expect(
      await esperarEncabezado(page, {
        id: 'guias-config-step-up',
        ruta: '/console?tab=connectors',
        objetivo: encabezadoDeDialogo(
          page,
          'This action needs an elevated session',
        ),
      }),
    ).toBe('This action needs an elevated session')
  })

  test('resuelve un título dinámico por aria-labelledby', async ({ page }) => {
    await monta(
      page,
      `<div role="dialog" aria-labelledby="t-sheet">
         <h2>Session details</h2>
         <h2 id="t-sheet">acme-platform governed session</h2>
         <div role="tablist">
           <button role="tab" aria-selected="true">Live</button>
           <button role="tab" aria-selected="false">Governance</button>
         </div>
       </div>`,
    )
    const objetivo = await encabezadoDelDialogoAbierto(page)
    expect(
      await esperarEncabezado(page, {
        id: 'video-07-live',
        ruta: '/agentops (sheet)',
        objetivo,
      }),
    ).toBe('acme-platform governed session')

    // El marcador acredita la pestaña que el título no distingue.
    await exigirMarcadores('video-07-live', [
      page
        .getByRole('dialog')
        .getByRole('tab', { name: 'Live', selected: true }),
    ])
  })

  test('rechaza dos diálogos abiertos en vez de elegir uno', async ({
    page,
  }) => {
    await monta(
      page,
      `<div role="dialog" aria-labelledby="a"><h2 id="a">Uno</h2></div>
       <div role="dialog" aria-labelledby="b"><h2 id="b">Dos</h2></div>`,
    )
    await expect(encabezadoDelDialogoAbierto(page)).rejects.toThrow(
      /strict mode violation/,
    )
  })
})

test.describe('capture target — el instante de la foto', () => {
  test('acepta una pantalla quieta', async ({ page }) => {
    await monta(page, '<main><h1>Identity &amp; NHI</h1><table></table></main>')
    await exigirInstanteLimpio(page, 'identity')
  })

  test('rechaza el error boundary posterior a la carga', async ({ page }) => {
    await monta(
      page,
      '<main><h1>Identity &amp; NHI</h1><p>This view crashed</p></main>',
    )
    await expect(exigirInstanteLimpio(page, 'identity')).rejects.toThrow(
      /error boundary/,
    )
  })

  test('rechaza un indicador de carga visible', async ({ page }) => {
    await monta(
      page,
      '<main><h1>Identity &amp; NHI</h1>' +
        '<div role="progressbar" style="width:40px;height:8px"></div></main>',
    )
    await expect(exigirInstanteLimpio(page, 'identity')).rejects.toThrow(
      /indicador de carga/,
    )
  })
})
