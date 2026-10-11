// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import type { PanelExtensions } from './panels'
import type { FeatureView } from './registry'

/** Build-time composition seam. The default console carries no extensions. */
export const FEATURE_EXTENSIONS: readonly FeatureView[] = Object.freeze([])

/** Default-empty panels inside public pages (./panels). */
export const PANEL_EXTENSIONS: PanelExtensions = Object.freeze({
  identityLoginCards: Object.freeze([]),
  identityStepUpMethods: Object.freeze([]),
  sessionPanels: Object.freeze([]),
  governanceTabs: Object.freeze([]),
  capabilitiesTabs: Object.freeze([]),
  complianceTabs: Object.freeze([]),
  reportingCards: Object.freeze([]),
  licenseCards: Object.freeze([]),
  scopesCards: Object.freeze([]),
})

/** Independent route and heading witnesses consumed by console qualification. */
export const EXTENSION_ROUTES: readonly {
  id: string
  path: string
  heading: string
}[] = Object.freeze([])

/** A build-time public page. Its requests still enforce their own server policy. */
export interface AnonymousFeatureView {
  readonly id: string
  /** Static absolute route path, mounted outside the authenticated app shell. */
  readonly path: string
  readonly element: () => ReactNode
  /** Optional sign-in link label, evaluated on render for the current language. */
  readonly loginLabel?: () => string
}

/** Default-empty anonymous routes and optional choices on the native login page. */
export const ANONYMOUS_FEATURE_EXTENSIONS: readonly AnonymousFeatureView[] =
  Object.freeze([])

/** Independent witnesses for anonymous route, capture and accessibility coverage. */
export const ANONYMOUS_EXTENSION_ROUTES: readonly {
  readonly id: string
  readonly path: string
  readonly heading: string
}[] = Object.freeze([])
