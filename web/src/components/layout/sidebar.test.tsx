// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ComponentProps } from 'react'
import { renderIntel } from '@/test/intel'

const routerState = vi.hoisted(() => ({ pathname: '/' }))
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: ({
    select,
  }: {
    select?: (s: { location: { pathname: string } }) => unknown
  } = {}) =>
    select ? select({ location: { pathname: routerState.pathname } }) : '',
  Link: ({
    children,
    to,
    activeProps,
    activeOptions,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    activeProps?: Record<string, string>
    activeOptions?: { exact?: boolean }
  }) => {
    const active = activeOptions?.exact
      ? routerState.pathname === to
      : routerState.pathname === to || routerState.pathname.startsWith(`${to}/`)
    return (
      <a
        href={to}
        data-status={active ? 'active' : undefined}
        {...(active ? activeProps : {})}
        {...props}
      >
        {children}
      </a>
    )
  },
}))

const canMock = vi.fn((_permission: string) => true)
vi.mock('@/lib/auth/context', () => ({
  // `grants: []` and `isSuperadmin: false` — the rail carries the organisation and
  // workspace switchers since the work-first pass, and with no membership to choose
  // between
  // both render `null`. These tests are about the NAVIGATION, so the honest mock is the
  // one that makes the scope block empty rather than the one that populates it.
  useAuth: () => ({
    can: (p: string) => canMock(p),
    grants: [],
    isSuperadmin: false,
    activeTenant: null,
    setActiveTenant: () => {},
  }),
}))

import i18n from 'i18next'
import { AreasSheet, AreasTree } from './sidebar'
import { useAreasSheet } from './areas-sheet-store'
import { NAV_AREAS } from '@/features/registry'
import { BUSINESS_BUILD } from '@/features/business-build'
import { FEATURE_EXTENSIONS } from '@/features/extensions'
import { useModulesStore } from '@/stores/modules'
import {
  authorizedEntries,
  buildNavSearchIndex,
  rankNavMatches,
} from '@/features/navigation/model'
import {
  DEFAULT_AREA_EXPANSION,
  usePreferencesStore,
} from '@/stores/preferences'

afterEach(() => {
  routerState.pathname = '/'
  canMock.mockReset()
  canMock.mockReturnValue(true)
  usePreferencesStore.setState({
    collapsedNavGroups: [],
    navAreas: DEFAULT_AREA_EXPANSION,
  })
})

const areaRow = (id: string, root: ParentNode = document) =>
  root.querySelector(`[data-nav-area="${id}"]`) as HTMLElement
// Queried by attribute, not by role: while the drawer (a modal dialog) is open the desktop
// sidebar is aria-hidden, and the simultaneous-instance controls below must still reach
// the desktop toggles to prove they name the desktop panels.
const toggleOf = (id: string, root: ParentNode = document) =>
  areaRow(id, root).querySelector('button[aria-expanded]') as HTMLElement
/** The panel a toggle controls — resolved through ITS OWN aria-controls, which is what an
 *  assistive technology does; the id carries the rendering instance's namespace. */
const areaPanel = (id: string, root: ParentNode = document) =>
  document.getElementById(
    toggleOf(id, root).getAttribute('aria-controls') as string,
  ) as HTMLElement
const hrefs = () =>
  screen.getAllByRole('link').map((a) => a.getAttribute('href'))

describe('the area directory (N1)', () => {
  it('renders the nine areas in the ratified order and the pinned Settings, with no Overview row', () => {
    renderIntel(<AreasTree />)
    const ids = [...document.querySelectorAll('[data-nav-area]')].map((e) =>
      e.getAttribute('data-nav-area'),
    )
    expect(ids).toEqual(NAV_AREAS.map((a) => a.id))
    // Not a single retired hub heading survives.
    expect(document.querySelector('[id^="nav-group-"]')).toBeNull()
    const links = hrefs()
    // Now is a place of the top bar, so the directory opens on its first area.
    expect(links).not.toContain('/')
    expect(links[0]).toBe('/areas/infrastructure')
    expect(links[links.length - 1]).toBe('/settings')
    // Every area row is a real link to its directory page.
    for (const a of NAV_AREAS) expect(links).toContain(a.path)
  })

  it('gives each area a directory link AND a separate expand control, folded unless active', () => {
    routerState.pathname = '/agentops'
    renderIntel(<AreasTree />)
    const ai = areaRow('ai')
    const link = within(ai).getByRole('link', { name: 'AI' })
    expect(link).toHaveAttribute('href', '/areas/ai')
    const toggle = toggleOf('ai')
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    // The panel id is namespaced by the rendering instance and still names the area.
    expect(toggle.getAttribute('aria-controls')).toMatch(/nav-area-ai$/)
    expect(toggle).toHaveAccessibleName('Hide AI modules')
    expect(areaPanel('ai')).not.toHaveClass('hidden')
    // A sibling area is folded and says so.
    expect(toggleOf('system')).toHaveAttribute('aria-expanded', 'false')
    expect(toggleOf('system')).toHaveAccessibleName(
      'Show System & settings modules',
    )
    expect(areaPanel('system')).toHaveClass('hidden')
    // Two levels: the pages sit right under the area, in section order, with no section
    // heading between them (#3121).
    expect(within(areaPanel('ai')).queryAllByRole('group')).toEqual([])
    // The list of pages carries its area's name for a screen reader.
    expect(
      within(areaPanel('ai')).getByRole('list', { name: 'AI' }),
    ).toBeInTheDocument()
    const leaves = within(areaPanel('ai'))
      .getAllByRole('link')
      .map((a) => a.getAttribute('href'))
    // The places the top bar names are not listed again, nor the /agentops door into one.
    expect(leaves).not.toContain('/sessions')
    expect(leaves).not.toContain('/agent-tools')
    expect(leaves).not.toContain('/agentops')
    expect(leaves.indexOf('/mcp-servers')).toBeLessThan(
      leaves.indexOf('/models'),
    )
    expect(leaves.indexOf('/models')).toBeLessThan(leaves.indexOf('/voice'))
  })

  it('marks exactly one link as the current page and the containing area as the active branch', () => {
    routerState.pathname = '/mcp-servers'
    renderIntel(<AreasTree />)
    const current = document.querySelectorAll('[aria-current="page"]')
    expect(current).toHaveLength(1)
    expect(current[0]).toHaveAttribute('href', '/mcp-servers')
    expect(current[0]).toHaveAttribute('data-status', 'active')
    expect(areaRow('ai')).toHaveAttribute('data-branch', 'active')
    expect(areaRow('system')).not.toHaveAttribute('data-branch')
    // The area link itself is NOT the current page while a leaf is.
    expect(
      within(areaRow('ai')).getByRole('link', { name: 'AI' }),
    ).not.toHaveAttribute('aria-current')
  })

  it('on a directory page the area link is the current page and its area is open', () => {
    routerState.pathname = '/areas/security-identity'
    renderIntel(<AreasTree />)
    const current = document.querySelectorAll('[aria-current="page"]')
    expect(current).toHaveLength(1)
    expect(current[0]).toHaveAttribute('href', '/areas/security-identity')
    expect(toggleOf('security-identity')).toHaveAttribute(
      'aria-expanded',
      'true',
    )
  })

  it('folds and reopens one area with the button, persisting the choice, without changing the route', async () => {
    routerState.pathname = '/mcp-servers'
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    await user.click(toggleOf('ai'))
    expect(toggleOf('ai')).toHaveAttribute('aria-expanded', 'false')
    expect(areaPanel('ai')).toHaveClass('hidden')
    expect(usePreferencesStore.getState().navAreas.closed).toContain('ai')
    // Still on the same page: the current-page marker did not move.
    expect(document.querySelector('[aria-current="page"]')).toHaveAttribute(
      'href',
      '/mcp-servers',
    )
    await user.click(toggleOf('ai'))
    expect(toggleOf('ai')).toHaveAttribute('aria-expanded', 'true')
    expect(usePreferencesStore.getState().navAreas.open).toContain('ai')
    // Another area opened on purpose stays open beside it.
    await user.click(toggleOf('system'))
    expect(toggleOf('system')).toHaveAttribute('aria-expanded', 'true')
    expect(toggleOf('ai')).toHaveAttribute('aria-expanded', 'true')
  })

  it('offers an area only when one of its leaves is authorized (the union), never by a parent gate', () => {
    canMock.mockImplementation((p) => p === 'governance:routine:read')
    renderIntel(<AreasTree />)
    const ids = [...document.querySelectorAll('[data-nav-area]')].map((e) =>
      e.getAttribute('data-nav-area'),
    )
    // Routine policies live in Security & identity; /dashboards has no permission, so
    // Observability is open to everyone.
    //
    // ⛔ AND Work & communications IS HERE ON PURPOSE (G1-B). Its administration leaf no
    //    longer answers from the reflection: it asks the engine, and until that answer
    //    exists the access is UNKNOWN. The ratified rule is that an INSTALLED static link
    //    stays visible while unknown — identically in the sidebar, the palette and the
    //    directories, so the three cannot disagree about whether a module exists — while
    //    its protected child refuses to load. Hiding it would be treating "I have not been
    //    told" as "you may not", which is the one conversion this lot exists to prevent.
    //    An ESTABLISHED `not_reachable` does remove it; that is asserted in
    //    features/navigation/authorization.causal.test.tsx.
    // A Business console's console asks the engine too, and is in System.
    expect(ids).toEqual([
      'work-communications',
      'security-identity',
      'observation',
      ...(BUSINESS_BUILD ? ['system'] : []),
    ])
    routerState.pathname = '/routine-policies'
    // The administration link is offered under the same unknown-keeps-the-link rule; its
    // route still refuses to load anything protected until the engine has answered.
    expect(hrefs()).toEqual([
      '/areas/work-communications',
      '/communications/administration',
      '/areas/security-identity',
      '/routine-policies',
      '/areas/observation',
      '/dashboards',
      // A Business console's System area, open for its console on the same rule.
      ...(BUSINESS_BUILD ? ['/areas/system', '/console'] : []),
      '/settings',
    ])
  })

  it('offers the sibling door only with its own permission', () => {
    canMock.mockImplementation((p) => p === 'sessions:run:read')
    routerState.pathname = '/agentops'
    renderIntel(<AreasTree />)
    const ai = areaPanel('ai')
    expect(
      within(ai)
        .getAllByRole('link')
        .map((a) => a.getAttribute('href')),
    ).toEqual(['/agentops'])
  })
})

// #2240: Settings > Modules is where an off module reads as Off.
describe('the area directory search folds a page whose module is off', () => {
  afterEach(() => useModulesStore.setState({ off: new Set() }))

  it('in the ranked results as in the tree', async () => {
    const user = userEvent.setup()
    useModulesStore.setState({ off: new Set(['deploy']) })
    renderIntel(<AreasTree />)
    const shown = () =>
      screen.queryAllByRole('link').map((a) => a.getAttribute('href'))
    expect(shown()).toContain('/dashboards')
    expect(shown()).not.toContain('/deploy')
    await user.type(screen.getByRole('searchbox'), 'deploy')
    expect(shown()).not.toContain('/deploy')
  })
})

describe('the area directory search (P2-12, N1 index)', () => {
  const filter = () => screen.getByRole('searchbox')

  it('narrows the sidebar to matching views, shown as one ranked list with their area and section', async () => {
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    const before = screen.getAllByRole('link').length
    expect(areaPanel('security-identity')).toHaveClass('hidden')

    await user.type(filter(), 'claude code governance')
    const after = screen.getAllByRole('link')
    // The non-firing direction matters as much: a filter that hides EVERYTHING also
    // "narrows" the list, and only the surviving match tells the two apart.
    expect(after.length).toBeLessThan(before)
    expect(after.some((a) => a.getAttribute('href') === '/claude-policy')).toBe(
      true,
    )
    expect(after.some((a) => a.getAttribute('href') === '/models')).toBe(false)
    // The match is reachable although its area is folded: the grouped tree is replaced by
    // the ranked list, whose rows carry the area/section context; the fold state is not
    // touched by the query.
    expect(document.querySelector('[data-nav-ranked]')).not.toBeNull()
    expect(document.querySelector('[data-nav-area]')).toBeNull()
    expect(
      screen.getByRole('link', { name: /Managed policy/ }),
    ).toHaveTextContent(
      'Security & identity › Policies and governance boundaries',
    )
    expect(usePreferencesStore.getState().navAreas).toEqual(
      DEFAULT_AREA_EXPANSION,
    )
    // Clearing restores the grouped tree exactly as it was: still folded.
    await user.clear(filter())
    expect(areaPanel('security-identity')).toHaveClass('hidden')
  })

  // ⛔ THE REVIEW'S finding 1 CONTROL. "admin": Administration (System & settings, a label-prefix
  // hit) must come BEFORE AI tools (AI, a description hit) — the earlier build
  // walked the areas in canonical order and put the weak early hit first. The sidebar's
  // order must be the palette's: the shared ranking over the shared authorized projection.
  it('renders filtered results in the shared rank order: a later-area label hit before an earlier-area description hit', async () => {
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    await user.type(filter(), 'admin')
    const shown = hrefs()
    const t = i18n.getFixedT(null, 'nav') as never
    const expected = rankNavMatches(
      authorizedEntries(buildNavSearchIndex(t), () => true),
      'admin',
    ).map((e) => e.path)
    expect(shown).toEqual(expected)
    expect(shown.indexOf('/console')).toBeLessThan(
      shown.indexOf('/agent-tools'),
    )
    expect(shown[0]).toBe('/console')
    expect(shown).toEqual(expect.arrayContaining(['/console', '/agent-tools']))
    // The announced count is the number of links on screen.
    expect(document.querySelector('[aria-live="polite"]')).toHaveTextContent(
      `Matches: ${shown.length}`,
    )
  })

  it('never lists a module the principal may not open, whatever it scores', async () => {
    canMock.mockImplementation((p) => p !== 'tenant:admin')
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    await user.type(filter(), 'admin')
    const shown = hrefs()
    // The console is gated by tenant:admin in Community. A Business console asks the engine
    // instead, and its installed link stays offered until the engine answers (G1-B).
    expect(shown.includes('/console')).toBe(BUSINESS_BUILD)
    expect(shown.includes('/tenants')).toBe(
      FEATURE_EXTENSIONS.some((view) => view.id === 'tenants'),
    )
  })

  it('finds a view by a NOUN it manages, not just by its label', async () => {
    // The whole point of the second axis. "identities" is not the label of
    // /access-map, /permissions or /console — it is the noun they manage, and an
    // operator who thinks in those thirteen words must still land on them.
    const user = userEvent.setup()
    renderIntel(<AreasTree />)

    await user.type(filter(), 'identities')
    const links = hrefs()
    expect(links).toEqual(
      expect.arrayContaining([
        '/identity',
        '/permissions',
        '/console',
        '/access-map',
      ]),
    )
    expect(links).not.toContain('/finops')
  })

  it('finds a view by its path, by its former name and by its English label', async () => {
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    await user.type(filter(), '/stored-budgets')
    expect(hrefs()).toContain('/stored-budgets')
    await user.clear(filter())
    await user.type(filter(), '/red-team')
    expect(
      screen
        .queryAllByRole('link')
        .some((a) => a.getAttribute('href') === '/red-team'),
    ).toBe(FEATURE_EXTENSIONS.some((view) => view.id === 'redteam'))
    await user.clear(filter())
    await user.type(filter(), 'control console')
    expect(hrefs()).toContain('/console')
    await user.clear(filter())
    await user.type(filter(), 'claude code')
    // The former operate portal's name finds the one Sessions screen.
    expect(hrefs()).toEqual(
      expect.arrayContaining(['/sessions', '/claude-policy', '/adoption']),
    )
    expect(hrefs()).not.toContain('/agentops')
  })

  it('finds an area by its own name and keeps its directory link', async () => {
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    await user.type(filter(), 'automation')
    expect(hrefs()).toContain('/areas/automation')
  })

  it('says so when nothing matches, instead of showing an empty shell', async () => {
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    await user.type(filter(), 'zzz-no-such-view')
    // NO link survives — including the pinned Settings utility. It used to sit outside
    // the filter, which left one link on screen while the sr-only count announced zero.
    expect(screen.queryAllByRole('link')).toHaveLength(0)
    expect(screen.getByText(/zzz-no-such-view/)).toBeInTheDocument()
  })

  it('keeps the pinned Settings link findable by name', async () => {
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    await user.type(filter(), 'settings')
    expect(hrefs()).toContain('/settings')
  })

  it('announces the number of links the operator can actually see', async () => {
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    await user.type(filter(), 'claude code governance')
    const live = document.querySelector('[aria-live="polite"]') as HTMLElement
    const shown = screen.getAllByRole('link').length
    expect(live.textContent).toBe(`Matches: ${shown}`)
  })

  it('returns focus to the field after clearing', async () => {
    // The clear button unmounts on click, so focus would otherwise land on <body>.
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    await user.type(filter(), 'claude code governance')
    await user.click(screen.getByRole('button', { name: /clear/i }))
    expect(filter()).toHaveFocus()
  })

  it('restores the full list when the filter is cleared', async () => {
    const user = userEvent.setup()
    renderIntel(<AreasTree />)
    const before = screen.getAllByRole('link').length

    await user.type(filter(), 'claude code governance')
    await user.click(screen.getByRole('button', { name: /clear/i }))

    expect(screen.getAllByRole('link')).toHaveLength(before)
    expect(filter()).toHaveValue('')
  })
})

describe('AreasSheet', () => {
  // ⛔ THE REVIEW'S finding 2 CONTROL. Two directories can be mounted at once (the bare tree and
  // the sheet): every aria-controls in the sheet must resolve to exactly one element,
  // INSIDE the sheet — under normal id resolution, not by counting classes.
  it('resolves every aria-controls to its own instance while the tree and the sheet are both mounted', async () => {
    const user = userEvent.setup()
    routerState.pathname = '/agentops'
    useAreasSheet.setState({ open: true, opener: null })
    renderIntel(
      <>
        <AreasTree />
        <AreasSheet />
      </>,
    )
    const dialog = screen.getByRole('dialog')
    const aside = document.querySelector(
      '[data-slot="areas-tree"]',
    ) as HTMLElement
    const drawerToggles = [
      ...dialog.querySelectorAll('button[aria-controls]'),
    ] as HTMLElement[]
    expect(drawerToggles).toHaveLength(9)
    for (const b of drawerToggles) {
      const id = b.getAttribute('aria-controls') as string
      const matches = document.querySelectorAll(`[id="${id}"]`)
      expect(matches, id).toHaveLength(1)
      expect(dialog.contains(matches[0]), id).toBe(true)
      expect(aside.contains(matches[0]), id).toBe(false)
    }
    // The bare tree's toggles name its own panels, never the drawer's.
    for (const b of aside.querySelectorAll('button[aria-controls]')) {
      const id = b.getAttribute('aria-controls') as string
      const matches = document.querySelectorAll(`[id="${id}"]`)
      expect(matches, id).toHaveLength(1)
      expect(aside.contains(matches[0]), id).toBe(true)
    }
    // Toggling in the drawer moves the drawer's own panel, and the two instances share
    // the preference (one store), so both reflect the fold.
    await user.click(toggleOf('ai', dialog))
    expect(areaPanel('ai', dialog)).toHaveClass('hidden')
    expect(dialog.contains(areaPanel('ai', dialog))).toBe(true)
    expect(areaPanel('ai', aside)).toHaveClass('hidden')
  })

  it('projects the same areas and closes on navigation', async () => {
    const user = userEvent.setup()
    useAreasSheet.setState({ open: true, opener: null })
    renderIntel(<AreasSheet />)
    const ids = [...document.querySelectorAll('[data-nav-area]')].map((e) =>
      e.getAttribute('data-nav-area'),
    )
    expect(ids).toEqual(NAV_AREAS.map((a) => a.id))
    await user.click(screen.getByRole('link', { name: 'Infrastructure' }))
    expect(useAreasSheet.getState().open).toBe(false)
  })

  it('gives focus back to the control that opened it', async () => {
    const user = userEvent.setup()
    renderIntel(
      <>
        <button
          type="button"
          onClick={(e) => useAreasSheet.getState().show(e.currentTarget)}
        >
          opener
        </button>
        <AreasSheet />
      </>,
    )
    const opener = screen.getByRole('button', { name: 'opener' })
    await user.click(opener)
    expect(screen.getByRole('dialog', { name: 'All areas' })).toBeVisible()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(opener).toHaveFocus())
    expect(useAreasSheet.getState().open).toBe(false)
  })
})

describe('P10 — el borde inferior del nav no se lee como un item pisado', () => {
  // ⛔ EL DEFECTO, medido en navegador a 1440x900 sobre el `dist` commiteado: el
  // area de scroll y el pie `Settings` NO se solapan (solape = 0 px EXACTOS), pero
  // el viewport acaba en y=851 justo donde empieza el `border-t` del pie, y ahi el
  // ultimo item visible —«Setup wizard»— queda cortado a 22 de sus 32 px. Con una
  // linea dura en el corte, un item partido se lee como PISADO.
  //
  // ⚠ «Que el scroll no parta ningun item» NO es alcanzable: con 754 px de viewport
  // y un paso de 32 px casi cualquier altura parte alguno (a 768 y 1080 no ocurre, a
  // 900 si). Lo alcanzable —y lo que este caso fija— es que un item parcial SE LEA
  // como parcial.
  //
  // EL INVARIANTE, que es lo unico que un test puede sostener aqui: la mascara
  // desvanece los ultimos N px, y el nav DEBE dejar al menos N px de hueco debajo
  // del ultimo item. Si alguien quita el padding y deja la mascara, «Supply chain»
  // queda atenuado PARA SIEMPRE al final del scroll — se cambia un defecto por otro,
  // y en jsdom no hay layout que lo cace, asi que se fija sobre la fuente.
  it('el padding inferior del nav cubre la altura de la mascara', async () => {
    const fs = await import('node:fs')
    const src = fs.readFileSync('src/components/layout/sidebar.tsx', 'utf8')

    const mask = /calc\(100%-(\d+)px\)|calc\(100%\s*-\s*(\d+)px\)/.exec(src)
    expect(
      mask,
      'la mascara de desvanecido ya no esta en sidebar.tsx',
    ).not.toBeNull()
    const fadePx = Number(mask![1] ?? mask![2])

    // The rail's keyboard support put a ref, a keydown handler and `aria-keyshortcuts` on
    // this element, so the className is no longer the next attribute. Match forward to
    // it instead of pinning the attribute ORDER, which was never the property under test.
    const nav =
      /aria-label=\{t\('common:a11y.mainNavigation'\)\}[\s\S]{0,400}?className="([^"]+)"/.exec(
        src,
      )
    expect(nav, 'no encuentro el className del nav principal').not.toBeNull()
    const pb = /\bpb-(\d+)\b/.exec(nav![1])
    expect(pb, `el nav perdio su padding inferior: "${nav![1]}"`).not.toBeNull()
    const padPx = Number(pb![1]) * 4 // escala de Tailwind: 1 = 0.25rem = 4 px

    expect(
      padPx,
      `el desvanecido es de ${fadePx}px y el nav solo deja ${padPx}px debajo del ultimo item: al final del scroll quedaria atenuado`,
    ).toBeGreaterThanOrEqual(fadePx)
  })
})
