// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createRouter } from '@tanstack/react-router'
import { NotFoundPage } from './pages/not-found'
import { routeTree } from './routes'

export const router = createRouter({
  routeTree,
  defaultPreload: 'intent',
  defaultNotFoundComponent: NotFoundPage,
  scrollRestoration: true,
})

// Register the router instance for type-safe Links/navigation across the app.
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
