// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createContext } from 'react'

/**
 * The name of the sidebar place whose link opened the page on screen, or null (a page the
 * person reached from inside a place, or one shown on its own). The shell provides it; the
 * top bar and the page heading read it, so the place you click is the place you land on.
 */
export const PagePlaceContext = createContext<string | null>(null)
