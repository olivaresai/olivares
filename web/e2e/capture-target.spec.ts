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
import fs from 'node:fs'
import { expect, test } from '@playwright/test'

import {
  encabezadoDeDialogo,
  encabezadoDelDialogoAbierto,
  encabezadoDePagina,
  esperarEncabezado,
  exigirInstanteLimpio,
  exigirMarcadores,
  exigirPantallaTerminada,
  ocultarFechas,
  ocultarMarcasDeRelease,
  VISTAS_DE_SESION,
  restaurarFechas,
} from './capture-target'

// Short enough that a negative control does not spend the real 60 s proving an
// absence. The capture cells never pass it.
const PLAZO_CONTROL = 1_500

const VERSION = '0.1'

const monta = (page: import('@playwright/test').Page, cuerpo: string) =>
  page.setContent(
    `<!DOCTYPE html><html lang="en"><body>${cuerpo}</body></html>`,
  )

// The shell, which is what an oracle looking for "some heading" would find first.
const SHELL = `
  <header data-slot="topbar">
    <h2>Favorites</h2>
    <h2>Recent</h2>
  </header>`

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
    await exigirInstanteLimpio(page, 'identity', { version: VERSION })
  })

  test('rechaza el error boundary posterior a la carga', async ({ page }) => {
    await monta(
      page,
      '<main><h1>Identity &amp; NHI</h1><p>This view crashed</p></main>',
    )
    await expect(
      exigirInstanteLimpio(page, 'identity', { version: VERSION }),
    ).rejects.toThrow(/error boundary/)
  })

  test('accepts a neutral server path such as a folder the product suggests', async ({
    page,
  }) => {
    await monta(page, '<main><h1>Workspace</h1><p>/srv/reference</p></main>')
    await exigirInstanteLimpio(page, 'workspace', { version: VERSION })
  })

  test('rejects a path of the capture host, in the page or in a dialog', async ({
    page,
  }) => {
    for (const ruta of [
      '/mnt/externaldisk/tmp.QOzg4FEZQY',
      '/workspace/scratch/tmp.QOzg4FEZQY',
      '/home/someone/.config',
      '/Users/someone/Library',
      '/root/.config',
    ]) {
      await monta(
        page,
        `<main><h1>Workspace</h1></main><div role="dialog"><p>${ruta}</p></div>`,
      )
      await expect(
        exigirInstanteLimpio(page, 'workspace', { version: VERSION }),
      ).rejects.toThrow(/path of the capture host/)
    }
  })

  test('rejects a host path that sits in a field value or is split across elements', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Workspace</h1><input value="/workspace/x" /></main>',
    )
    await expect(
      exigirInstanteLimpio(page, 'workspace', { version: VERSION }),
    ).rejects.toThrow(/path of the capture host/)
    await monta(
      page,
      '<main><h1>Workspace</h1><div style="display:flex"><span>/home</span><span>/someone</span></div></main>',
    )
    await expect(
      exigirInstanteLimpio(page, 'workspace', { version: VERSION }),
    ).rejects.toThrow(/path of the capture host/)
  })

  test('rechaza un indicador de carga visible', async ({ page }) => {
    await monta(
      page,
      '<main><h1>Identity &amp; NHI</h1>' +
        '<div role="progressbar" style="width:40px;height:8px"></div></main>',
    )
    await expect(
      exigirInstanteLimpio(page, 'identity', { version: VERSION }),
    ).rejects.toThrow(/indicador de carga/)
  })
})

// A published capture outlives the release that took it, and it must not read as a defect of the
// product: no build number, no live counts, no date, no name that only the harness knows, no page
// that is nothing but an empty state.
test.describe('capture target — a capture that outlives its release', () => {
  const TOPBAR = `
    <header><span class="font-mono">${VERSION}</span>
      <a href="/sessions">Sessions <span data-slot="count-badge">56</span></a></header>`

  test('hides the version mark and the shell counts, and keeps the counts a page owns', async ({
    page,
  }) => {
    await monta(
      page,
      `${TOPBAR}<main><h1>Work</h1><span data-slot="count-badge">3</span>
        <p class="font-mono">${VERSION}%</p></main>`,
    )
    await ocultarMarcasDeRelease(page, VERSION)
    const visible = await page.evaluate(() => document.body.innerText)
    expect(visible).not.toMatch(/\b56\b/)
    expect(visible).not.toMatch(/^0\.1$/m)
    expect(visible).toContain('3')
    expect(visible).toContain('0.1%')
    await exigirInstanteLimpio(page, 'work', { version: VERSION })
  })

  test('hides the separator that stood before the version, so no dangling dot is left', async ({
    page,
  }) => {
    await monta(
      page,
      `<main><h1>Sign in</h1></main>
       <p><span class="font-mono">localhost:8466</span><span aria-hidden="true"> · </span><span class="font-mono">${VERSION}</span></p>`,
    )
    await ocultarMarcasDeRelease(page, VERSION)
    const visible = await page.evaluate(() => document.body.innerText)
    expect(visible).toContain('localhost:8466')
    expect(visible).not.toContain('·')
    expect(visible).not.toContain(VERSION)
  })

  test('rejects a build number that is still on screen', async ({ page }) => {
    await monta(page, `${TOPBAR}<main><h1>Work</h1></main>`)
    await expect(
      exigirInstanteLimpio(page, 'work', { version: VERSION }),
    ).rejects.toThrow(/release number/)
  })

  test('rejects a date that dates the image, in text or in a chart label', async ({
    page,
  }) => {
    for (const fecha of [
      'Oct 12, 2026, 10:17',
      'Aug 20',
      '2026-10-12',
      '10/12/2026',
      'decided 2026-10-12T11:54:25.153000000Z',
      '12 Oct 2026',
      '12th October',
      'October 2026',
      '2026/10/12',
      '12.10.2026',
      'Oct\u00a012',
    ]) {
      await monta(page, `<main><h1>Security</h1><p>${fecha}</p></main>`)
      await expect(
        exigirInstanteLimpio(page, 'security', { version: VERSION }),
      ).rejects.toThrow(/dates the image/)
    }
    await monta(
      page,
      '<main><h1>Spend</h1><svg width="80" height="20"><text x="0" y="12">Sep 19</text></svg></main>',
    )
    await expect(
      exigirInstanteLimpio(page, 'spend', { version: VERSION }),
    ).rejects.toThrow(/dates the image/)
  })

  test('accepts relative times, percentages and prose that only looks like a date', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Health</h1><p>2 minutes ago · 99.9% · 40 ms · you may 5 times retry · 127.0.0.1:8466 · 2.4.0 · 1.11.0 · 3/4/10 · $0.1 · 10.1 · demo-estate</p></main>',
    )
    await exigirInstanteLimpio(page, 'health', { version: VERSION })
  })

  test('rejects a name that only the harness or the seeder knows', async ({
    page,
  }) => {
    for (const nombre of [
      'docs-capture-k3-editor-826f57b35c3a@olivares.local',
      'demo-vol-guardian-rules-00',
      'demo-capture-decision-001',
      'Claude Code · tmp.gp5WaB9x',
    ]) {
      await monta(page, `<main><h1>Identity</h1><p>${nombre}</p></main>`)
      await expect(
        exigirInstanteLimpio(page, 'identity', { version: VERSION }),
      ).rejects.toThrow(/only the capture harness knows/)
    }
  })

  test('rejects a page that is only an empty state and accepts one beside data', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Budgets</h1><div data-slot="empty-state">No budgets yet</div></main>',
    )
    await expect(
      exigirInstanteLimpio(page, 'stored-budgets', { version: VERSION }),
    ).rejects.toThrow(/empty state/)
    await monta(
      page,
      `<main><h1>Kill switch</h1>
        <table><tbody><tr><td>rule</td></tr></tbody></table>
        <div data-slot="empty-state">No stops recorded</div></main>`,
    )
    await exigirInstanteLimpio(page, 'killswitch', { version: VERSION })
  })

  test('refuses an uppercase date, which text-transform shows but the DOM does not spell', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Audit</h1><p style="text-transform:uppercase">oct 12</p></main>',
    )
    await expect(
      exigirInstanteLimpio(page, 'audit', { version: VERSION }),
    ).rejects.toThrow(/dates the image/)
  })

  test('refuses a date, a harness name or a version that sits in a field value', async ({
    page,
  }) => {
    for (const valor of [
      'Oct 12, 2026',
      'docs-capture-k3-editor-1@olivares.local',
      'V0.1',
    ]) {
      await monta(
        page,
        `<main><h1>Settings</h1><input value="${valor}" /></main>`,
      )
      await expect(
        exigirInstanteLimpio(page, 'settings', { version: VERSION }),
      ).rejects.toThrow(
        /dates the image|only the capture harness knows|release number/,
      )
    }
  })

  test('refuses the release number however it is written', async ({ page }) => {
    for (const texto of ['Released in 0.1.', 'V0.1', 'olivares v0.1 (linux)']) {
      await monta(page, `<main><h1>About</h1><p>${texto}</p></main>`)
      await expect(
        exigirInstanteLimpio(page, 'about', { version: VERSION }),
      ).rejects.toThrow(/release number/)
    }
  })

  test('refuses a harness name written in capitals or in plural, and the random demo folder', async ({
    page,
  }) => {
    for (const nombre of [
      'docs-captures-k3-editor',
      'DOCS-CAPTURE-K3-EDITOR',
      '/tmp/demo-aB3dE9/acme-platform',
    ]) {
      await monta(page, `<main><h1>Identity</h1><p>${nombre}</p></main>`)
      await expect(
        exigirInstanteLimpio(page, 'identity', { version: VERSION }),
      ).rejects.toThrow(/only the capture harness knows/)
    }
  })

  test('does not count the rows of a hidden table as data', async ({
    page,
  }) => {
    await monta(
      page,
      `<main><h1>Budgets</h1><div data-slot="empty-state">No budgets yet</div>
        <table style="display:none"><tbody><tr><td>hidden row</td></tr></tbody></table></main>`,
    )
    await expect(
      exigirInstanteLimpio(page, 'stored-budgets', { version: VERSION }),
    ).rejects.toThrow(/empty state/)
  })
})

// What the 0.1 console got wrong on the surfaces that show sessions (#1784, #1785): an assistant reply left as
// raw markdown, bookkeeping rows in the conversation, and a Now page that lists more sessions as Working than
// its own tile counts as working. Other views keep the checks they had.
test.describe('capture target — session surfaces', () => {
  const VISTAS = [...VISTAS_DE_SESION]
  // The tile counts the live sessions (working, idle, gone silent) and captions the idle and silent ones.
  // Estate's summary sentence (#2546): the sessions open (not yet ended), then today's cost.
  const SENTENCE = (open: number) =>
    `<section><p>${open} sessions open · $6.84 today</p></section>`
  const FILA = (estado: string) =>
    `<li data-testid="home-recent-row"><span>${estado}</span> <span>dependency upgrade scan</span></li>`
  // A live session on Estate is a card: its tool first, its state in a cell of its own (#2245).
  const TARJETA = (estado: string) =>
    `<li><a data-testid="now-live-card"><span>Claude Code</span><span data-testid="now-live-card-state">${estado}</span></a></li>`

  test('accepts a conversation with a rendered reply, a glob, a masked value and relative times', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Session</h1><p>Identify the clients first.</p><p>src/**/test/**</p><p>key **** ****</p><p>Decision 3 of 5</p><p>4 minutes ago</p></main>',
    )
    await exigirInstanteLimpio(page, 'sessions', { version: VERSION })
  })

  test('rejects an assistant reply that shows raw markdown, on every session view', async ({
    page,
  }) => {
    for (const id of VISTAS) {
      for (const crudo of [
        '- **Identify clients:** list them',
        '- **Identify',
        'Use **bold** carefully',
        '```go\nfunc main() {}\n```',
      ]) {
        await monta(
          page,
          `<main><h1>Session</h1><pre style="white-space:pre-wrap">${crudo}</pre>${id === 'home' ? SENTENCE(0) : ''}</main>`,
        )
        await expect(
          exigirInstanteLimpio(page, id, { version: VERSION }),
          `${id} accepted raw markdown: ${crudo}`,
        ).rejects.toThrow(/raw markdown/)
      }
    }
  })

  test('rejects the rows the console never paints: thinking tokens and a raw rate-limit report (dev#3119)', async ({
    page,
  }) => {
    for (const fila of [
      'thinking tokens',
      'Thinking tokens',
      'allowed · five hour',
      'allowed · seven day',
      'allowed · seven day opus · 42%',
      'allowed warning · seven day · 93%',
      'allowed · five hour · 95%',
      'rejected · seven day',
      'allowed · overage',
    ]) {
      await monta(page, `<main><h1>Session</h1><p>${fila}</p></main>`)
      await expect(
        exigirInstanteLimpio(page, 'sessions', { version: VERSION }),
        `accepted the internal row: ${fila}`,
      ).rejects.toThrow(/internal row/)
    }
  })

  test('accepts the sentence of a limit that stopped the turn (dev#3119)', async ({
    page,
  }) => {
    for (const fila of [
      'Claude Code reached its weekly limit (93% used); it resets Mon 03:00',
      'Claude Code reached its 5-hour limit',
    ]) {
      await monta(page, `<main><h1>Session</h1><p>${fila}</p></main>`)
      await exigirInstanteLimpio(page, 'sessions', { version: VERSION })
    }
  })

  test('rejects a session row that names no session', async ({ page }) => {
    await monta(
      page,
      '<main><h1>Sessions</h1><ul><li>Working · Untitled session</li></ul></main>',
    )
    await expect(
      exigirInstanteLimpio(page, 'agentops', { version: VERSION }),
    ).rejects.toThrow(/untitled session/)
  })

  test('does not apply the conversation rules to the recording replay or the workspace sheet', async ({
    page,
  }) => {
    for (const id of ['session-viewer', 'video-10-workspace-browser']) {
      await monta(
        page,
        '<main><h1>Replay</h1><pre>```\n- **ls**\n```</pre></main>',
      )
      await exigirInstanteLimpio(page, id, { version: VERSION })
    }
  })

  test('rejects an Estate page that lists more rows Working than sessions open', async ({
    page,
  }) => {
    // Three open; five Working rows is the #1785 page.
    await monta(
      page,
      `<main><h1>Estate</h1>${SENTENCE(3)}<ul>${FILA('Working').repeat(5)}</ul></main>`,
    )
    await expect(
      exigirInstanteLimpio(page, 'home', { version: VERSION }),
    ).rejects.toThrow(
      /5 recent rows and live cards read Working beside a sentence that counts 3 sessions open/,
    )
  })

  test('accepts an Estate page whose rows agree with its sentence', async ({
    page,
  }) => {
    await monta(
      page,
      `<main><h1>Estate</h1>${SENTENCE(5)}<ul>${FILA('Working').repeat(3)}${FILA('Idle').repeat(2)}</ul></main>`,
    )
    await exigirInstanteLimpio(page, 'home', { version: VERSION })
    // More sessions are open than the recent list can hold.
    await monta(
      page,
      `<main><h1>Estate</h1>${SENTENCE(8)}<ul>${FILA('Working').repeat(5)}</ul></main>`,
    )
    await exigirInstanteLimpio(page, 'home', { version: VERSION })
    // A quiet estate lists nothing and says so.
    await monta(
      page,
      '<main><h1>Estate</h1><p>Nothing is running</p><ul></ul></main>',
    )
    await exigirInstanteLimpio(page, 'home', { version: VERSION })
  })

  test('reads a sentence cut short at a page of sessions ("7+ sessions open")', async ({
    page,
  }) => {
    // Now says "N+" when its live read stopped at a page; the gate still reads the count.
    await monta(
      page,
      `<main><h1>Estate</h1><section><p>7+ sessions open · $6.84 today</p></section><ul>${FILA('Working').repeat(8)}</ul></main>`,
    )
    await expect(
      exigirInstanteLimpio(page, 'home', { version: VERSION }),
    ).rejects.toThrow(
      /8 recent rows and live cards read Working beside a sentence that counts 7 sessions open/,
    )
  })

  test('reads a live card the way it reads a row', async ({ page }) => {
    await monta(
      page,
      `<main><h1>Estate</h1>${SENTENCE(1)}<ul>${TARJETA('Working').repeat(2)}</ul></main>`,
    )
    await expect(
      exigirInstanteLimpio(page, 'home', { version: VERSION }),
    ).rejects.toThrow(/2 recent rows and live cards read Working/)
    await monta(
      page,
      `<main><h1>Estate</h1>${SENTENCE(2)}<ul>${TARJETA('Working')}${TARJETA('Idle')}</ul></main>`,
    )
    await exigirInstanteLimpio(page, 'home', { version: VERSION })
  })

  test('refuses an Estate page whose sentence counts sessions open but lists none', async ({
    page,
  }) => {
    await monta(page, `<main><h1>Estate</h1>${SENTENCE(3)}<ul></ul></main>`)
    await expect(
      exigirInstanteLimpio(page, 'home', { version: VERSION }),
    ).rejects.toThrow(/no recent row or live card is on the page/)
  })

  test('refuses an Estate page without its summary sentence', async ({
    page,
  }) => {
    await monta(page, `<main><h1>Estate</h1><ul>${FILA('Idle')}</ul></main>`)
    await expect(
      exigirInstanteLimpio(page, 'home', { version: VERSION }),
    ).rejects.toThrow(/summary sentence is absent or unreadable/)
  })

  test('names only cells that exist in the capture spec', async () => {
    const spec = fs.readFileSync('e2e/docs-captures.spec.ts', 'utf8')
    for (const id of VISTAS) {
      expect(spec, `no capture cell has the id ${id}`).toMatch(
        new RegExp(`(^|[^_A-Za-z])id: '${id}',`),
      )
    }
  })

  test('leaves every other view alone: the same text is fine outside the session surfaces', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Cost breakdown</h1><div data-slot="empty-state"></div><table><tbody><tr><td>Thinking tokens</td></tr></tbody></table></main>',
    )
    await exigirInstanteLimpio(page, 'stored-budgets', { version: VERSION })
  })
})

test.describe('capture target — a screen that looks broken or unfinished', () => {
  const clean = (page: import('@playwright/test').Page, id: string) =>
    exigirPantallaTerminada(page, id)

  test('rejects an error banner and a feature the build does not serve', async ({
    page,
  }) => {
    const cases: [string, RegExp][] = [
      ['<div role="alert">Could not load the sessions</div>', /error/],
      ['<div data-slot="module-not-enabled">Module off</div>', /not serve/],
      ['<div data-slot="not-in-edition">Business feature</div>', /not serve/],
      [
        '<div data-slot="capability-unavailable">Unavailable</div>',
        /not serve/,
      ],
      ['<div role="status">Something went wrong</div>', /error/],
    ]
    for (const [html, message] of cases) {
      await monta(
        page,
        `<main><h1>Sessions</h1><table><tbody><tr><td>row</td></tr></tbody></table>${html}</main>`,
      )
      await expect(clean(page, 'sessions')).rejects.toThrow(message)
    }
  })

  test('accepts an empty or hidden live region and an ordinary status line', async ({
    page,
  }) => {
    await monta(
      page,
      `<main><h1>Sessions</h1><div role="alert"></div><div role="alert" hidden>Hidden failure</div>
        <div role="status">3 sessions running</div>
        <div data-slot="module-not-enabled" style="display:none">Module off</div></main>`,
    )
    await clean(page, 'sessions')
  })

  test('rejects raw markdown, in the page or in a field', async ({ page }) => {
    const raw = [
      '<p>This is **bold** text</p>',
      '<p>Run ```npm test``` first</p>',
      '<p>## Heading</p>',
      '<p>See [the guide](https://example.com/guide)</p>',
      '<p>Set the `model` option</p>',
      '<p>- first item<br>- second item</p>',
      '<input value="**bold**">',
    ]
    for (const html of raw) {
      await monta(page, `<main><h1>Template</h1>${html}</main>`)
      await expect(clean(page, 'templates')).rejects.toThrow(/markdown/)
    }
  })

  test('accepts a glob and a masked value, which the session checks accept too', async ({
    page,
  }) => {
    await monta(
      page,
      `<main><h1>Session</h1><p>src/**/test/**</p><p>**/node_modules and **/dist</p>
        <p>key **** ****</p></main>`,
    )
    await clean(page, 'sessions')
    // The guards are not a blanket pass: a real bold span on the same page is still refused.
    await monta(
      page,
      `<main><h1>Session</h1><p>src/**/test/**</p><p>a **bold** reply</p></main>`,
    )
    await expect(clean(page, 'sessions')).rejects.toThrow(/markdown/)
  })

  test('rejects a placeholder that leaked into the text', async ({ page }) => {
    const leaked = [
      '<p>Cost: undefined</p>',
      '<p>Tokens: NaN</p>',
      '<p>Owner: [object Object]</p>',
      '<p>Hello {{name}}</p>',
      '<p>TODO write the description</p>',
      '<p>Lorem ipsum dolor sit amet</p>',
    ]
    for (const html of leaked) {
      await monta(page, `<main><h1>Models</h1>${html}</main>`)
      await expect(clean(page, 'models')).rejects.toThrow(/placeholder/)
    }
  })

  test('accepts prose, arithmetic, a Todo column and a heading number', async ({
    page,
  }) => {
    await monta(
      page,
      `<main><h1>Work</h1>
        <p>2 * 3 requests per second, about 12 tokens</p>
        <p>Issue #12 is fixed. Review the plan at 3 p.m.</p>
        <ul><li>Todo</li></ul>
        <p>A policy applies to every folder it names - none is skipped.</p>
        <p>Export it first: <code>export TOKEN=\${TOKEN}</code></p></main>`,
    )
    await clean(page, 'work')
  })
})

test.describe('capture target — the dates a capture would carry', () => {
  const clean = (page: import('@playwright/test').Page, id: string) =>
    exigirInstanteLimpio(page, id, { version: VERSION })

  test('masks a date in a cell, with its year and time, and the shot passes', async ({
    page,
  }) => {
    await monta(
      page,
      `<main><h1>Profiles</h1><table><tbody><tr><td>claude-platform</td>
        <td>Oct 12, 2026, 3:15 PM</td><td>2026-10-12T11:54:25Z</td><td>12 Oct 2026</td></tr></tbody></table></main>`,
    )
    await expect(clean(page, 'profiles')).rejects.toThrow(/date/)
    expect(await ocultarFechas(page)).toBe(3)
    await clean(page, 'profiles')
    const text = await page.locator('main').innerText()
    expect(text).toContain('claude-platform')
    expect(text).not.toMatch(/Oct|2026|PM|T11/)
  })

  test('keeps the width of what it masks and draws it as a bar, not as a hole', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Audit</h1><p><span id="d">Oct 12, 2026</span></p></main>',
    )
    const before = (await page.locator('#d').boundingBox())!.width
    await ocultarFechas(page)
    const after = (await page.locator('#d').boundingBox())!.width
    expect(Math.abs(after - before)).toBeLessThan(1)
    const bar = await page
      .locator('[data-capture-mask]')
      .first()
      .evaluate((el) => getComputedStyle(el).backgroundColor)
    expect(bar).not.toBe('rgba(0, 0, 0, 0)')
  })

  test('masks a date of a chart label and leaves prose that only looks like one', async ({
    page,
  }) => {
    await monta(
      page,
      `<main><h1>Spend</h1><svg width="200" height="40"><text x="0" y="20">Oct 12</text></svg>
        <p>It may 5 times fail; 3/4/10 of the runs, version 2.4.0, 127.0.0.1, 2 minutes ago.</p></main>`,
    )
    expect(await ocultarFechas(page)).toBe(1)
    await clean(page, 'spend')
    expect(await page.locator('main').innerText()).toContain(
      'It may 5 times fail',
    )
  })

  test('is idempotent and reports nothing to mask on a clean page', async ({
    page,
  }) => {
    await monta(page, '<main><h1>Work</h1><p>Updated Oct 12</p></main>')
    expect(await ocultarFechas(page)).toBe(1)
    expect(await ocultarFechas(page)).toBe(0)
    await monta(page, '<main><h1>Work</h1><p>Nothing dated here</p></main>')
    expect(await ocultarFechas(page)).toBe(0)
  })

  test('cannot reach a value in a field, so the shot is still refused', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Settings</h1><input value="Oct 12, 2026"></main>',
    )
    await ocultarFechas(page)
    await expect(clean(page, 'settings')).rejects.toThrow(/date/)
  })

  test('refuses markdown whose payload is a date, because the content check runs before the mask', async ({
    page,
  }) => {
    await monta(page, '<main><h1>Audit</h1><p>`2026-10-12`</p></main>')
    await expect(exigirPantallaTerminada(page, 'audit')).rejects.toThrow(
      /markdown/,
    )
  })

  test('gives the page its own text nodes back after the shot', async ({
    page,
  }) => {
    await monta(
      page,
      '<main><h1>Audit</h1><p id="p">Created <b>Oct 12, 2026</b> by Morgan</p><svg><text id="t">Oct 12</text></svg></main>',
    )
    await page.evaluate(() => {
      ;(window as unknown as { __keep: Node | null }).__keep =
        document.querySelector('#p b')!.firstChild
    })
    expect(await ocultarFechas(page)).toBe(2)
    expect(
      await page
        .locator('#t')
        .evaluate((el) => (el as unknown as SVGElement).style.visibility),
    ).toBe('hidden')
    await restaurarFechas(page)
    const restored = await page.evaluate(
      () =>
        document.querySelector('#p b')!.firstChild ===
        (window as unknown as { __keep: Node | null }).__keep,
    )
    expect(restored).toBe(true)
    expect(await page.locator('#p').innerText()).toBe(
      'Created Oct 12, 2026 by Morgan',
    )
    expect(await page.locator('[data-capture-mask]').count()).toBe(0)
    expect(
      await page
        .locator('#t')
        .evaluate((el) => (el as unknown as SVGElement).style.visibility),
    ).toBe('')
    // and a second masking after the restore works again
    expect(await ocultarFechas(page)).toBe(2)
  })
})
