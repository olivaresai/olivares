// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Side-effect-free route inventories shared by the standalone AT gate, the
// Playwright axe checks, and the accessibility coverage guard.
//
// AUTH_ROUTES is derived from route-census.json, not written out: the census is the
// one full route list Node can read without the app's bundler, and
// registry.route-conservation.test.ts pins it to the built router in both directions.
// A path retired through ROUTE_ALIASES stays in the census, so the gate scans it through
// its redirect (ponytail: ROUTE_ALIASES is empty; exclude aliases here when the first lands).
import census from '../src/features/route-census.json' with { type: 'json' }
import {
  ANONYMOUS_EXTENSION_ROUTES,
  EXTENSION_ROUTES,
} from '../src/features/extensions.ts'

export const SESSION_VIEWER_ROUTE = '/session-viewer/sess-a11y'

// ⛔ `/accept-invite` FALTABA — añadida el 2026-08-18. El motor manda ese enlace por correo
//    (`core/api/handlers_onboarding.go`) y es literalmente la primera pantalla de producto que ve
//    una persona invitada, así que no visitarla dejaba sin medir el arranque de todo cliente nuevo
//    que no sea el que instala. Renderiza sin sesión y sin token: es un estado válido y capturable.
export const PUBLIC_ROUTES = [
  ...ANONYMOUS_EXTENSION_ROUTES.map(({ path }) => path),
  '/login',
  '/setup',
  '/accept-invite',
  '/status-page',
]

/** A path the browser can open: the one parametric route gets its seeded fixture id. */
function concretePath(path: string): string {
  return path === '/session-viewer/$id' ? SESSION_VIEWER_ROUTE : path
}

export const AUTH_ROUTES = [
  ...new Set([
    ...EXTENSION_ROUTES.map(({ path }) => path),
    ...census.paths.filter(
      (path) =>
        !PUBLIC_ROUTES.includes(path) && !census.business_paths.includes(path),
    ),
  ]),
].map(concretePath)

export const ROUTES = [
  ...new Set([
    ...EXTENSION_ROUTES.map(({ path }) => concretePath(path)),
    '/dashboards',
    '/access-map',
    '/inventory',
    '/security',
    '/compliance',
    '/health',
    // The onboarding wizard.
    '/onboarding',
    //the mutation-dense admin surfaces (tab panels, forms, dialogs):
    // the control console, identity/NHI, and the Claude policy editors. These
    // carry the most interactive controls per page, so the quick axe pass must
    // cover them, not only the read-mostly dashboards above.
    '/console',
    '/identity',
    '/claude-policy',
  ]),
]
