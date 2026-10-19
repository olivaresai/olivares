// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { FEATURE_EXTENSIONS } from './extensions'

/**
 * Whether this console is the Business console: it carries edition views. The one definition of
 * the build the console registers pages for (registry.tsx) and answers bookmarks for
 * (app/pages/not-found.tsx). The engine's own edition, which gates the requests a page makes, is
 * server-info's: lib/edition.ts.
 */
export const BUSINESS_BUILD = FEATURE_EXTENSIONS.length > 0
