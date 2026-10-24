// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { FEATURE_EXTENSIONS } from './extensions'
import census from './route-census.json' with { type: 'json' }

/**
 * Whether this console is the Business console: it carries edition views. The one definition of
 * the build the console registers pages for (registry.tsx) and answers bookmarks for
 * (app/pages/not-found.tsx). The engine's own edition, which gates the requests a page makes, is
 * server-info's: lib/edition.ts.
 */
export const BUSINESS_BUILD = FEATURE_EXTENSIONS.length > 0

/** The census paths as patterns: a `$param` segment stands for any one segment. A Business
 * console serves them (or has a real 404 where it does not): only Community says "not here". */
const BUSINESS_PATTERNS = BUSINESS_BUILD
  ? []
  : census.business_paths.map(
      (path) => new RegExp(`^${path.replace(/\$\w+/g, '[^/]+')}$`, 'i'),
    )

/** True for a bookmark of a page this edition does not include (trailing slashes ignored). */
export function isBusinessOnlyPath(pathname: string): boolean {
  const path = pathname.replace(/\/+$/, '')
  return BUSINESS_PATTERNS.some((pattern) => pattern.test(path))
}
