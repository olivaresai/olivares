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

// A published capture outlives the release that took it. The build number in the top bar and the
// live counts of the shell (open sessions, pending approvals) date it, so they are hidden before the
// shot. Counts a page owns, inside <main>, stay. The version is the string /v1/server-info reports.
export async function ocultarMarcasDeRelease(
  page: Page,
  version: string,
): Promise<void> {
  await page.evaluate((marca) => {
    if (!document.getElementById('capture-release-marks')) {
      const style = document.createElement('style')
      style.id = 'capture-release-marks'
      style.textContent =
        '[data-slot="count-badge"]:not(main *){visibility:hidden}'
      document.head.append(style)
    }
    for (const el of Array.from(document.querySelectorAll('span'))) {
      if (el.children.length === 0 && el.textContent?.trim() === marca) {
        el.style.visibility = 'hidden'
        // The dot that separated the version from what came before it goes with it.
        const antes = el.previousElementSibling as HTMLElement | null
        if (antes?.textContent?.trim() === '·')
          antes.style.visibility = 'hidden'
      }
    }
  }, version)
}

// Data rows of the page: visible table body rows that are not an empty-state row.
export const filasDeDatos = (page: Page): Promise<number> =>
  page.evaluate(() => {
    const raiz = document.querySelector('main') ?? document.body
    return Array.from(raiz.querySelectorAll('tbody tr')).filter(
      (tr) =>
        tr.getClientRects().length > 0 &&
        !tr.querySelector('[data-slot="empty-state"]'),
    ).length
  })

const MONTH =
  'Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|June?|July?|Aug(?:ust)?|Sep(?:t(?:ember)?)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?'
// A date that dates the image, in any order a locale writes it: "Oct 12", "Oct 12, 2026", "12 Oct 2026",
// "October 2026", "2026-10-12", "2026-10-12T11:54:25Z", "2026/10/12", "12.10.2026", "10/12/2026". Relative times
// ("2 minutes ago"), ratios ("3/4/10"), versions and addresses ("2.4.0", "127.0.0.1") do not date it. A numeric
// date needs a four-digit year. The digit lookarounds, not \b, because an ISO timestamp continues with "T".
// \s, not a space: the console writes a no-break space in some locales.
const dateFor = (month: string) =>
  new RegExp(
    `\\b(?:${month})\\.?\\s\\d{1,2}\\b|\\b\\d{1,2}(?:st|nd|rd|th)?\\s(?:${month})\\b|\\b(?:${month})\\.?\\s\\d{4}\\b` +
      `|(?<!\\d)\\d{4}[./-]\\d{1,2}[./-]\\d{1,2}(?!\\d)|(?<!\\d)\\d{1,2}[./-]\\d{1,2}[./-]\\d{4}(?!\\d)`,
  )
const DATE = dateFor(MONTH)
// text-transform: uppercase is shown by innerText but not spelled in the DOM, so the month names are checked in
// capitals too (not case-insensitively: "may 5 times" is prose).
const DATE_CAPS = dateFor(MONTH.toUpperCase())
// Names that exist only because a harness or a seeder made them: the capture's own member accounts and
// decisions (also in plural and in capitals), the volume seeder's rows and a mktemp folder.
const HARNESS_NAME =
  /\b(?:docs|demo)-capture[\w-]*|\bdemo-vol-[\w-]*|\btmp\.[a-z0-9]{6,}/
// The per-run demo workspace folder, demo-XXXXXX. A random mktemp suffix has a digit or a capital; a word such as
// "demo-estate" has neither.
const DEMO_FOLDER = /\bdemo-(?=[A-Za-z0-9]*[0-9A-Z])[A-Za-z0-9]{6}\b/

// Text that reads as unfinished: raw markdown the console did not render, or a placeholder that leaked from a
// missing value or an unfilled template. Prose, arithmetic, a "Todo" column and a shell variable in a guide
// (`${TOKEN}`) are none of these. A glob (`src/**/x`) and a masked value (`**** ****`) are not bold text: the
// guards are those of MARKDOWN_CRUDO below, so the two checks never disagree on the same page.
const RAW_MARKDOWN: RegExp[] = [
  /(?<![*/\w])\*\*(?=[^\s*/])[^*\n]{0,80}[^\s*/]\*\*(?![*/\w])/,
  /```/,
  /(^|\n)#{1,6} \S/,
  /\[[^\]\n]{1,80}\]\((?:https?:\/\/|\/)[^)\s]*\)/,
  /(^|\s)`[^`\n]{1,80}`/,
  /(^|\n)\s*[-*] \S[^\n]*\n\s*[-*] \S/,
]
const PLACEHOLDER: RegExp[] = [
  /\b(?:undefined|NaN)\b|\[object Object\]/,
  /\b(?:TODO|TBD|FIXME)\b/,
  /lorem ipsum/i,
  /\{\{[^}\n]+\}\}/,
]
export function unfinishedText(
  text: string,
): { kind: 'markdown' | 'placeholder'; found: string } | null {
  for (const re of RAW_MARKDOWN) {
    const hit = re.exec(text)
    if (hit) return { kind: 'markdown', found: hit[0].trim().slice(0, 80) }
  }
  for (const re of PLACEHOLDER) {
    const hit = re.exec(text)
    if (hit) return { kind: 'placeholder', found: hit[0].trim().slice(0, 80) }
  }
  return null
}

// A feature the build or the edition does not serve says so in one of these slots.
const UNAVAILABLE_SLOTS = [
  'module-not-enabled',
  'not-in-edition',
  'capability-undisclosed',
  'capability-unavailable',
]
  .map((slot) => `[data-slot="${slot}"]:visible`)
  .join(', ')

// A date the product prints is the day the capture was taken, and no seeder can move it: the console formats the
// server's own timestamps. It is masked the way the version mark is hidden: the text keeps its box and is drawn as a
// soft bar, so a column of times reads as deliberately not shown and not as missing data. Nothing is rewritten and
// nothing is invented. The mask takes the year and the time that follow the day with it ("Oct 12, 2026, 3:15 PM",
// "2026-10-12T11:54:25Z"). A value in a field is not text and stays, so the shutter check still refuses it.
const DATE_TAIL =
  '(?:,?\\s*(?:19|20)\\d{2}\\b)?(?:T\\d{2}:\\d{2}(?::\\d{2}(?:\\.\\d+)?)?(?:Z|[+-]\\d{2}:?\\d{2})?|,?\\s*(?:at\\s*)?\\d{1,2}:\\d{2}(?::\\d{2})?(?:\\s?[AaPp]\\.?[Mm]\\.?)?)?'

export async function ocultarFechas(page: Page): Promise<number> {
  return page.evaluate(
    ({ date, caps, tail }) => {
      const re = new RegExp(`(?:${date})${tail}|(?:${caps})${tail}`, 'g')
      const skip = new Set([
        'SCRIPT',
        'STYLE',
        'NOSCRIPT',
        'TEXTAREA',
        'OPTION',
      ])
      const texts: Text[] = []
      const walker = document.createTreeWalker(
        document.body,
        NodeFilter.SHOW_TEXT,
      )
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        const node = n as Text
        const parent = node.parentElement
        if (
          !parent ||
          skip.has(parent.tagName) ||
          parent.closest('[data-capture-mask]')
        )
          continue
        re.lastIndex = 0
        if (re.test(node.data)) texts.push(node)
      }
      // What was replaced, so that `restaurarFechas` puts the page's own nodes back after the shot: the framework
      // that rendered them keeps a reference to each, and the light and dark shots share one page.
      const w = window as unknown as { __captureMasks?: unknown[] }
      const registry = (w.__captureMasks ??= [])
      let masked = 0
      for (const node of texts) {
        const parent = node.parentElement!
        if (parent instanceof SVGElement) {
          // An SVG label has no inline box to wrap: the label is a date, hide it whole.
          registry.push({ svg: parent, was: parent.style.visibility })
          parent.style.visibility = 'hidden'
          masked += 1
          continue
        }
        const pieces = document.createDocumentFragment()
        let last = 0
        re.lastIndex = 0
        for (let m = re.exec(node.data); m; m = re.exec(node.data)) {
          if (m.index > last) pieces.append(node.data.slice(last, m.index))
          const bar = document.createElement('span')
          bar.setAttribute('data-capture-mask', '')
          bar.style.cssText =
            'display:inline-block;border-radius:4px;background:rgba(127,127,127,.26)'
          const hidden = document.createElement('span')
          hidden.style.visibility = 'hidden'
          hidden.textContent = m[0]
          bar.append(hidden)
          pieces.append(bar)
          last = m.index + m[0].length
          masked += 1
        }
        if (last < node.data.length) pieces.append(node.data.slice(last))
        registry.push({ original: node, made: Array.from(pieces.childNodes) })
        node.replaceWith(pieces)
      }
      return masked
    },
    { date: DATE.source, caps: DATE_CAPS.source, tail: DATE_TAIL },
  )
}

/** Puts back what `ocultarFechas` replaced: the page's own text nodes, in place, and the SVG labels' visibility. */
export async function restaurarFechas(page: Page): Promise<void> {
  await page.evaluate(() => {
    type Mask = {
      original?: Text
      made?: Node[]
      svg?: SVGElement
      was?: string
    }
    const w = window as unknown as { __captureMasks?: Mask[] }
    for (const m of (w.__captureMasks ?? []).reverse()) {
      if (m.svg) {
        m.svg.style.visibility = m.was ?? ''
        continue
      }
      const [first, ...rest] = m.made ?? []
      if (first?.parentNode && m.original)
        first.parentNode.insertBefore(m.original, first)
      for (const n of first ? [first, ...rest] : [])
        n.parentNode?.removeChild(n)
    }
    w.__captureMasks = []
  })
}

// What is on screen as text: the page's rendered text and the values of its fields, which are on screen but not in
// innerText.
const textoVisible = (page: Page): Promise<string> =>
  page.evaluate(() =>
    [
      document.body.innerText,
      ...Array.from(
        document.querySelectorAll('input,textarea'),
        (el) => (el as HTMLInputElement).value,
      ),
    ].join('\n'),
  )

/**
 * What a reader takes for broken or unfinished: an error screen, a feature the build does not serve, raw markdown
 * and a leaked placeholder. Run before `ocultarFechas`: a date inside backticks or a list item would otherwise
 * be masked to nothing and the markdown around it would pass. A view that cannot pass is held in
 * scripts/docs-captures-held.txt.
 */
export async function exigirPantallaTerminada(
  page: Page,
  id: string,
): Promise<void> {
  const alerta = await page
    .locator('[role="alert"]:visible, [role="status"]:visible')
    .evaluateAll(
      (els) =>
        els
          .map((el) => (el.textContent ?? '').replace(/\s+/g, ' ').trim())
          .find((text, i) =>
            els[i].getAttribute('role') === 'alert'
              ? text !== ''
              : /^(?:Not authorized|Something went wrong)/.test(text),
          ) ?? null,
    )
  expect(
    alerta,
    `${id}: the page shows an error (${alerta}); fix its cause or hold the capture`,
  ).toBeNull()
  const sinServir = await page.locator(UNAVAILABLE_SLOTS).count()
  expect(
    sinServir,
    `${id}: the page says the build does not serve this feature; hold the capture`,
  ).toBe(0)
  const sinTerminar = unfinishedText(await textoVisible(page))
  expect(
    sinTerminar?.found ?? null,
    `${id}: the page shows ${sinTerminar?.kind} (${sinTerminar?.found}); render it or seed the value`,
  ).toBeNull()
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
  { version }: { version: string },
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
  // A published capture must not carry a path of the machine that took it. Field values are on screen
  // but not in innerText, and a path split across elements reads as one with the whitespace removed.
  const visible = await textoVisible(page)
  const hostPath = /\/(?:workspace|home|Users|root)\/\S*|\S*externaldisk\S*/
  const rutaInterna =
    (hostPath.exec(visible) ??
      hostPath.exec(visible.replace(/\s+/g, '')))?.[0] ?? null
  expect(
    rutaInterna,
    `${id}: the page shows a path of the capture host (${rutaInterna}); take it with a neutral TMPDIR`,
  ).toBeNull()
  // The reported version, however it is written: V0.1, v0.1, "Released in 0.1." A "$0.1" or "10.1" is not it.
  const escaped = version.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const marca = new RegExp(
    `(?<![\\w.$])v?${escaped}(?!\\w|\\.\\d|%)`,
    'i',
  ).exec(visible)
  expect(
    marca?.[0] ?? null,
    `${id}: the page still shows the release number; hide it before the shot`,
  ).toBeNull()
  const fecha = DATE.exec(visible) ?? DATE_CAPS.exec(visible)
  expect(
    fecha?.[0] ?? null,
    `${id}: the page shows a date that dates the image (${fecha?.[0]}); the capture must stay valid for later releases`,
  ).toBeNull()
  const interno =
    HARNESS_NAME.exec(visible.toLowerCase()) ?? DEMO_FOLDER.exec(visible)
  expect(
    interno?.[0] ?? null,
    `${id}: the page shows a name that only the capture harness knows (${interno?.[0]})`,
  ).toBeNull()
  // A page that is nothing but an empty state shows no product. An empty panel beside data is fine.
  const vacios = await page.locator('[data-slot="empty-state"]:visible').count()
  expect(
    vacios > 0 && (await filasDeDatos(page)) === 0,
    `${id}: the page is an empty state with no data row; seed it or hold the capture`,
  ).toBe(false)
  if (VISTAS_DE_SESION.has(id))
    await exigirSesionesCoherentes(page, id, visible)
}

// The views that show sessions and their conversation, which the 0.1 console painted wrong (#1784, #1785).
// The recording replay (session-viewer) and the workspace sheet (video-10) are not here: recorded terminal
// output may hold a code fence, and a file list holds no conversation. `exigirPantallaTerminada` still reads
// every view, so a replay that shows a code fence is judged when it is retaken (#1843).
export const VISTAS_DE_SESION = new Set([
  'home',
  'estate',
  'agentops',
  'sessions',
  'video-07-live',
  'video-08-governance',
])
// What #1784 showed: a list item or a bold span the reply left unrendered, and a code fence. Other markdown
// (headings, tables, inline code) is not looked for, so "no raw markdown" is not proven beyond these.
// A glob (`src/**/x`) and a masked value (`****`) are not markdown.
const MARKDOWN_CRUDO =
  /(?:^|\n)\s*[-*] \*\*|(?<![*/\w])\*\*(?=[^\s*/])[^*\n]*[^\s*/]\*\*(?![*/\w])|```/
// The rows #1784 and dev#3119 showed. The console paints no rate-limit report as a row: only a refusal that
// stopped the turn, as a sentence ("Claude Code reached its weekly limit…", conversation-frames.ts
// `rateLimitStop`). So any raw report, "allowed · <window> [· 42%]", "allowed warning · seven day · 93%",
// "rejected · five hour" or "allowed · overage", is a defect.
const THINKING_TOKENS = /\bthinking tokens\b/i
const LIMITE_CRUDO =
  /\b(?:allowed(?: warning)?|rejected)\s*·\s*(?:five hour|seven day(?: opus| sonnet)?|overage)(?:\s*·\s*\d{1,3}%)?/i
// A row that names no session at all: an observed session the demo never titled.
const SIN_TITULO = /\bUntitled session\b/

function filaInterna(visible: string): string | null {
  const tokens = THINKING_TOKENS.exec(visible)?.[0]
  if (tokens) return tokens
  return LIMITE_CRUDO.exec(visible)?.[0] ?? null
}

async function exigirSesionesCoherentes(
  page: Page,
  id: string,
  visible: string,
): Promise<void> {
  const crudo = MARKDOWN_CRUDO.exec(visible)?.[0].trim() ?? null
  expect(
    crudo,
    `${id}: the page shows raw markdown (${crudo}); the reply must render`,
  ).toBeNull()
  const fila = filaInterna(visible)
  expect(
    fila,
    `${id}: the page shows an internal row (${fila}); bookkeeping belongs in the inspector`,
  ).toBeNull()
  const sinTitulo = SIN_TITULO.exec(visible)?.[0] ?? null
  expect(
    sinTitulo,
    `${id}: the page shows an untitled session; the demo estate names its sessions`,
  ).toBeNull()
  if (id === 'home') await exigirEstateCoherente(page, id, visible)
}

// #1785: Now marked every session Working while the page counted them idle. On Estate (#2546) the
// summary sentence counts the sessions open (working, idle or gone silent: not yet ended; #3190), so a
// recent row or a live card can read Working only for one of those, and a sentence that counts open
// sessions needs a card or a row on the page. The cards and rows are a recent page of the sessions, so
// fewer of them than sessions open is fine.
// ponytail: the sentence carries no idle count, so a page that marks every open session Working
// passes, and the check cannot fire past six working sessions (Estate holds six cards). The upgrade is
// the swimlane (#2547): compare each lane's state with the session the page fetched.
async function exigirEstateCoherente(
  page: Page,
  id: string,
  visible: string,
): Promise<void> {
  const open = /(\d[\d,]*)\+? sessions? open/i.exec(visible)
  expect(
    open !== null || /Nothing is running/i.test(visible),
    `${id}: the summary sentence is absent or unreadable`,
  ).toBe(true)
  const vivas = Number((open?.[1] ?? '0').replace(/,/g, ''))
  // A row starts with its state word; a card names its tool first and carries the state in its own cell.
  const filas = [
    ...(await page.getByTestId('home-recent-row').allInnerTexts()),
    ...(await page.getByTestId('now-live-card-state').allInnerTexts()),
  ]
  expect(
    filas.length > 0 || vivas === 0,
    `${id}: the sentence counts ${vivas} sessions open but no recent row or live card is on the page`,
  ).toBe(true)
  const filasTrabajando = filas.filter((f) => /^\s*Working\b/.test(f)).length
  expect(
    filasTrabajando,
    `${id}: ${filasTrabajando} recent rows and live cards read Working beside a sentence that counts ${vivas} sessions open`,
  ).toBeLessThanOrEqual(vivas)
}
