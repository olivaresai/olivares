// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import census from '@/features/route-census.json'
import { NotFoundPage } from './not-found'

async function renderAt(path: string) {
  const rootRoute = createRootRoute({
    component: () => <Outlet />,
    notFoundComponent: NotFoundPage,
  })
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  render(<RouterProvider router={router} />)
  await screen.findByRole('main')
}

describe('a bookmarked page this edition does not include', () => {
  it.each(census.business_paths)(
    '%s says it is part of Business and links to it, with no action button',
    async (path) => {
      await renderAt(path.replace(/\$\w+/g, 'x1'))
      expect(
        screen.getByRole('heading', { name: 'Part of Business' }),
      ).toBeInTheDocument()
      expect(
        screen.getByText(/part of Olivares AI Business/),
      ).toBeInTheDocument()
      const link = screen.getByRole('link', { name: 'About Business' })
      expect(link).toHaveAttribute('href', 'https://olivares.ai/business')
      expect(link).toHaveAttribute('target', '_blank')
      expect(link).toHaveAttribute('rel', 'noopener noreferrer')
      expect(screen.queryByRole('button')).toBeNull()
    },
  )

  it.each(['/deploy/', '/Deploy', '/session-viewer/abc/'])(
    'answers %s like its published address',
    async (path) => {
      await renderAt(path)
      expect(
        screen.getByRole('heading', { name: 'Part of Business' }),
      ).toBeInTheDocument()
    },
  )

  it.each(['/deploy/x', '/session-viewer', '/session-viewer/a/b'])(
    'keeps the plain 404 for %s, which is not a published address',
    async (path) => {
      await renderAt(path)
      expect(
        screen.getByRole('heading', { name: 'Page not found' }),
      ).toBeInTheDocument()
    },
  )

  it('keeps the plain 404 for a path nobody published', async () => {
    await renderAt('/no-such-page')
    expect(
      screen.getByRole('heading', { name: 'Page not found' }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Business/)).toBeNull()
  })
})
