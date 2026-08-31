// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ComponentProps } from 'react'
import { renderIntel } from '@/test/intel'

vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  Link: ({ children, ...props }: ComponentProps<'a'> & { to?: string }) => (
    <a href={props.to} {...props}>
      {children}
    </a>
  ),
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true }),
}))

import { Sidebar } from './sidebar'
import { usePreferencesStore } from '@/stores/preferences'

afterEach(() => {
  usePreferencesStore.setState({
    collapsedNavGroups: [],
    sidebarCollapsed: false,
  })
})

describe('Sidebar hub collapse', () => {
  it('collapses and expands one hub, persisting the preference', async () => {
    const user = userEvent.setup()
    renderIntel(<Sidebar />)

    const operateToggle = screen
      .getAllByRole('button', { expanded: true })
      .find((b) => b.getAttribute('aria-controls') === 'nav-group-operate')
    expect(operateToggle).toBeTruthy()

    await user.click(operateToggle!)
    expect(operateToggle).toHaveAttribute('aria-expanded', 'false')
    expect(usePreferencesStore.getState().collapsedNavGroups).toContain(
      'operate',
    )
    // The hub's items are hidden; other hubs keep theirs visible.
    expect(document.getElementById('nav-group-operate')).toHaveClass('hidden')
    expect(document.getElementById('nav-group-govern')).not.toHaveClass(
      'hidden',
    )

    await user.click(operateToggle!)
    expect(operateToggle).toHaveAttribute('aria-expanded', 'true')
    expect(usePreferencesStore.getState().collapsedNavGroups).not.toContain(
      'operate',
    )
  })

  it('ignores hub collapse in the icon rail (no headers there)', () => {
    usePreferencesStore.setState({
      sidebarCollapsed: true,
      collapsedNavGroups: ['operate'],
    })
    renderIntel(<Sidebar />)
    // No hub toggle buttons render in rail mode.
    expect(document.querySelector('[aria-controls^="nav-group-"]')).toBeNull()
  })

  it('renders all five hubs, and no retired layer group', () => {
    renderIntel(<Sidebar />)
    const ids = [...document.querySelectorAll('[id^="nav-group-"]')].map(
      (e) => e.id,
    )
    expect(ids).toEqual([
      'nav-group-operate',
      'nav-group-automate',
      'nav-group-connect',
      'nav-group-govern',
      'nav-group-prove',
    ])
  })
})

describe('Sidebar search (P2-12)', () => {
  const filter = () => screen.getByRole('searchbox')

  it('narrows the sidebar to matching views', async () => {
    const user = userEvent.setup()
    renderIntel(<Sidebar />)
    const before = screen.getAllByRole('link').length

    await user.type(filter(), 'residency')
    const after = screen.getAllByRole('link')
    // The non-firing direction matters as much: a filter that hides EVERYTHING also
    // "narrows" the list, and only the surviving match tells the two apart.
    expect(after.length).toBeLessThan(before)
    expect(after.some((a) => a.getAttribute('href') === '/residency')).toBe(
      true,
    )
    expect(after.some((a) => a.getAttribute('href') === '/models')).toBe(false)
  })

  it('finds a view by a NOUN it manages, not just by its label', async () => {
    // The whole point of the second axis. "identities" is not the label of
    // /access-map, /permissions or /console — it is the noun they manage, and an
    // operator who thinks in thirteen words must still land on them.
    const user = userEvent.setup()
    renderIntel(<Sidebar />)

    await user.type(filter(), 'identities')
    const hrefs = screen.getAllByRole('link').map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(
      expect.arrayContaining([
        '/identity',
        '/permissions',
        '/console',
        '/access-map',
      ]),
    )
    expect(hrefs).not.toContain('/finops')
  })

  it('finds a view by its path, for anyone who thinks in urls', async () => {
    const user = userEvent.setup()
    renderIntel(<Sidebar />)
    await user.type(filter(), '/red-team')
    expect(
      screen.getAllByRole('link').map((a) => a.getAttribute('href')),
    ).toContain('/red-team')
  })

  it('reveals matches that sit inside a COLLAPSED hub', async () => {
    // Otherwise search reports hits the operator cannot see, which reads as broken.
    usePreferencesStore.setState({ collapsedNavGroups: ['govern'] })
    const user = userEvent.setup()
    renderIntel(<Sidebar />)
    expect(document.getElementById('nav-group-govern')).toHaveClass('hidden')

    await user.type(filter(), 'residency')
    expect(document.getElementById('nav-group-govern')).not.toHaveClass(
      'hidden',
    )
  })

  it('says so when nothing matches, instead of showing an empty shell', async () => {
    const user = userEvent.setup()
    renderIntel(<Sidebar />)
    await user.type(filter(), 'zzz-no-such-view')
    // NO link survives — including the pinned Settings utility. It used to sit outside
    // the filter, which left one link on screen while the sr-only count announced zero.
    expect(screen.queryAllByRole('link')).toHaveLength(0)
    expect(screen.getByText(/zzz-no-such-view/)).toBeInTheDocument()
  })

  it('keeps the pinned Settings link findable by name', async () => {
    const user = userEvent.setup()
    renderIntel(<Sidebar />)
    await user.type(filter(), 'settings')
    expect(
      screen.getAllByRole('link').map((a) => a.getAttribute('href')),
    ).toContain('/settings')
  })

  it('returns focus to the field after clearing', async () => {
    // The clear button unmounts on click, so focus would otherwise land on <body>.
    const user = userEvent.setup()
    renderIntel(<Sidebar />)
    await user.type(filter(), 'residency')
    await user.click(screen.getByRole('button', { name: /clear/i }))
    expect(filter()).toHaveFocus()
  })

  it('restores the full list when the filter is cleared', async () => {
    const user = userEvent.setup()
    renderIntel(<Sidebar />)
    const before = screen.getAllByRole('link').length

    await user.type(filter(), 'residency')
    await user.click(screen.getByRole('button', { name: /clear/i }))

    expect(screen.getAllByRole('link')).toHaveLength(before)
    expect(filter()).toHaveValue('')
  })

  it('offers no filter in the icon rail, where results could not be shown', () => {
    usePreferencesStore.setState({ sidebarCollapsed: true })
    renderIntel(<Sidebar />)
    expect(screen.queryByRole('searchbox')).toBeNull()
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
    expect(mask, 'la mascara de desvanecido ya no esta en sidebar.tsx').not.toBeNull()
    const fadePx = Number(mask![1] ?? mask![2])

    const nav = /aria-label=\{t\('common:a11y.mainNavigation'\)\}\s*className="([^"]+)"/.exec(src)
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
