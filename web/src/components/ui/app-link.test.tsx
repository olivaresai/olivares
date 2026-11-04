// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A LINK TO ANOTHER PAGE OF THE CONSOLE IS A ROUTER LINK. A plain `<a href="/providers">`
// loads the document again: the session is restored from the cookie, and every open dialog
// and every in-memory draft is gone. The scan below reads the sources the way the router
// census does and fails on any such anchor.
/// <reference types="node" />
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import census from '@/features/route-census.json'
import { AppLink } from './app-link'

afterEach(cleanup)

const SRC = join(__dirname, '..', '..')

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    if (statSync(path).isDirectory()) return sources(path)
    const shipped = /\.tsx$/.test(name) && !/\.(test|spec)\./.test(name)
    return shipped && !path.includes(`${join(SRC, 'test')}/`) ? [path] : []
  })
}

/** The console's own pages: every censused path, the server's `/v1/...` is not one. */
const PAGES = new Set([...census.paths, ...census.business_paths])

/** `<a ... href="/page...">` and `<a ... href={`/page...`}`: the first path segment. */
const ANCHOR = /<a\b[^<]*?\bhref=(?:"|\{`|\{")\/([^"`/?#$]*)/g

describe('in-app links', () => {
  it('are router links, never a plain anchor that reloads the document', () => {
    const plain: string[] = []
    for (const file of sources(SRC)) {
      const text = readFileSync(file, 'utf8')
      for (const hit of text.matchAll(ANCHOR)) {
        if (!PAGES.has(`/${hit[1]}`)) continue
        const line = text.slice(0, hit.index).split('\n').length
        plain.push(`${relative(SRC, file)}:${line} href=/${hit[1]}`)
      }
    }
    expect(plain).toEqual([])
  })
})

describe('AppLink', () => {
  async function mounted() {
    const root = createRootRoute({
      component: () => (
        <>
          <AppLink href="/identity?tab=sso#keys">Single sign-on</AppLink>
          <Outlet />
        </>
      ),
    })
    const router = createRouter({
      routeTree: root.addChildren([
        createRoute({
          getParentRoute: () => root,
          path: '/',
          component: () => null,
        }),
        createRoute({
          getParentRoute: () => root,
          path: '/identity',
          component: () => null,
        }),
      ]),
      history: createMemoryHistory({ initialEntries: ['/'] }),
    })
    render(<RouterProvider router={router} />)
    return {
      router,
      link: await screen.findByRole('link', { name: 'Single sign-on' }),
    }
  }

  /** What the page's own click handling left for the browser: false = the browser loads nothing. */
  function leftToBrowser(link: HTMLElement, init: MouseEventInit) {
    const seen: boolean[] = []
    const probe = (e: Event) => {
      seen.push(!e.defaultPrevented)
      e.preventDefault() // jsdom cannot load a page; the probe only reads
    }
    document.addEventListener('click', probe)
    fireEvent.click(link, init)
    document.removeEventListener('click', probe)
    return seen[0]
  }

  it('keeps its address as a real href and moves by the router on a plain click', async () => {
    const { router, link } = await mounted()
    expect(link).toHaveAttribute('href', '/identity?tab=sso#keys')
    expect(leftToBrowser(link, { button: 0 })).toBe(false)
    expect(router.state.location.pathname).toBe('/identity')
    expect(router.state.location.search).toEqual({ tab: 'sso' })
    expect(router.state.location.hash).toBe('keys')
  })

  it('leaves Ctrl-click, Cmd-click and middle-click to the browser: a new tab', async () => {
    const { router, link } = await mounted()
    expect(leftToBrowser(link, { button: 0, ctrlKey: true })).toBe(true)
    expect(leftToBrowser(link, { button: 0, metaKey: true })).toBe(true)
    expect(leftToBrowser(link, { button: 1 })).toBe(true)
    expect(router.state.location.pathname).toBe('/')
  })

  it('renders its address as a plain link when no router is mounted', () => {
    render(<AppLink href="/identity?tab=sso#keys">Single sign-on</AppLink>)
    expect(
      screen.getByRole('link', { name: 'Single sign-on' }),
    ).toHaveAttribute('href', '/identity?tab=sso#keys')
  })
})
