// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE STRIP AT THE TOP OF THE LIST: "2 need you · 5 working · $14 today", each part a link.
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  useSearch,
} from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { decodeShow } from './session-address'
import { SessionStrip } from './session-strip'
import './i18n'

type Props = Parameters<typeof SessionStrip>[0]

async function renderStrip(over: Partial<Props> = {}, at = '/sessions') {
  const props: Props = {
    needYou: 2,
    running: 5,
    costMicroUsd: 14_000_000,
    usagePath: '/usage',
    ...over,
  }
  const root = createRootRoute()
  const sessions = createRoute({
    getParentRoute: () => root,
    path: '/sessions',
    // The view decodes `?show=` and hands it down; so does this page.
    component: function Page() {
      const search = useSearch({ strict: false }) as Record<string, string>
      return <SessionStrip {...props} show={decodeShow(search).value} />
    },
  })
  const usage = createRoute({
    getParentRoute: () => root,
    path: '/usage',
    component: () => <p>Usage page</p>,
  })
  const router = createRouter({
    routeTree: root.addChildren([sessions, usage]),
    history: createMemoryHistory({ initialEntries: [at] }),
  })
  render(<RouterProvider router={router} />)
  await router.load()
  return router
}

describe('SessionStrip', () => {
  it('reads "2 need you · 5 working · $14.00 today" as a named, polite nav of links', async () => {
    await renderStrip()
    const nav = await screen.findByRole('navigation', {
      name: 'Sessions at a glance',
    })
    expect(nav).toHaveAttribute('aria-live', 'polite')
    expect(nav).toHaveTextContent('2 need you · 5 working · $14.00 today')
    const links = screen.getAllByRole('link')
    expect(links.map((a) => a.textContent)).toEqual([
      '2 need you',
      '5 working',
      '$14.00 today',
    ])
  })

  it('filters the list by need you and running, and a second press clears it', async () => {
    const user = userEvent.setup()
    const router = await renderStrip()
    await user.click(await screen.findByRole('link', { name: '2 need you' }))
    expect(router.state.location.search).toMatchObject({ show: 'attention' })
    expect(screen.getByRole('link', { name: '2 need you' })).toHaveAttribute(
      'aria-current',
      'true',
    )
    await user.click(screen.getByRole('link', { name: '2 need you' }))
    expect(router.state.location.search).not.toHaveProperty('show')
    await user.click(screen.getByRole('link', { name: '5 working' }))
    expect(router.state.location.search).toMatchObject({ show: 'active' })
  })

  it('keeps the open session, and brings the list in front when asked', async () => {
    const user = userEvent.setup()
    const router = await renderStrip({ toList: true }, '/sessions?session=r1')
    await user.click(await screen.findByRole('link', { name: '5 working' }))
    expect(router.state.location.search).toMatchObject({
      session: 'r1',
      show: 'active',
      pane: 'rail',
    })
  })

  it('opens Usage from the cost', async () => {
    const user = userEvent.setup()
    await renderStrip()
    await user.click(await screen.findByRole('link', { name: '$14.00 today' }))
    expect(await screen.findByText('Usage page')).toBeInTheDocument()
  })

  it('omits a zero part and an unknown cost, never printing $0', async () => {
    await renderStrip({ needYou: 0, costMicroUsd: null })
    const nav = await screen.findByRole('navigation')
    expect(nav).toHaveTextContent(/^5 working$/)
    expect(nav).not.toHaveTextContent('$')
  })

  it('omits a known cost of zero: no "$0.00 today"', async () => {
    await renderStrip({ needYou: 0, costMicroUsd: 0 })
    const nav = await screen.findByRole('navigation')
    expect(nav).toHaveTextContent(/^5 working$/)
  })

  it('tells a person who may not open Usage no cost, and keeps the other parts', async () => {
    await renderStrip({ usagePath: null })
    const nav = await screen.findByRole('navigation')
    expect(nav).toHaveTextContent(/^2 need you · 5 working$/)
    expect(nav).not.toHaveTextContent('$')
    const links = screen.getAllByRole('link')
    expect(links.map((a) => a.textContent)).toEqual(['2 need you', '5 working'])
  })

  it('is absent for a person who may not open Usage when the cost is all it would say', async () => {
    await renderStrip({ needYou: 0, running: 0, usagePath: null })
    expect(screen.queryByRole('navigation')).toBeNull()
    expect(document.body).not.toHaveTextContent('$')
  })

  it('is absent with nothing to say: no one waiting, nothing running, no known cost', async () => {
    await renderStrip({ needYou: 0, running: 0, costMicroUsd: null })
    expect(screen.queryByRole('navigation')).toBeNull()
  })

  it('is absent when the running count is unknown and nothing else is said', async () => {
    await renderStrip({ needYou: 0, running: null, costMicroUsd: null })
    expect(screen.queryByRole('navigation')).toBeNull()
  })
})
