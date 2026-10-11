// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { GatedSection } from '@/features/navigation/model'

/**
 * A destination that is a SECTION of a registry view rather than the view itself: the
 * approval queue is the first tab of Permissions. It is offered only when the principal
 * may open the view AND holds the section's own permission, and it links with the
 * section's search so the view opens on it.
 */
export interface ShellSection extends GatedSection {
  /** The label key under `nav:shell.journeys` and the key its count is given under. */
  key: string
  /** The registry view that holds the section. */
  view: string
  search: Readonly<Record<string, string>>
}

export const APPROVALS_SECTION: ShellSection = {
  key: 'approvals',
  view: 'permissions',
  search: { tab: 'approvals' },
  requires: 'governance:approval:read',
}
