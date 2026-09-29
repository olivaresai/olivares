// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { colors, isProviderAccent } from './provider-accent-palette'
/** A decorative identity cue. Adjacent text always identifies the instance;
 * palette choice conveys no lifecycle, authentication or health state. */
export function ProviderAccent({ accent }: { accent?: string }) {
  if (!accent || !isProviderAccent(accent)) return null
  return (
    <span
      aria-hidden="true"
      data-provider-accent={accent}
      className="me-1.5 inline-block size-2.5 shrink-0 rounded-full border border-current forced-colors:bg-[CanvasText]"
      style={{ backgroundColor: colors[accent], color: colors[accent] }}
    />
  )
}
