// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION RAIL's shared pieces: the status the reads report and the glyph of a row's
// state. The grouping is `session-rail-model.ts`; the reads are `use-session-rail.ts`.
// The glyph is also drawn by the Business cockpit.
import { CircleCheck, Pause } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { RailState } from './session-rail-model'

export interface RailStatus {
  loading: boolean
  error: boolean
}

/** The glyph of a state, with its word as the accessible name. Colour never carries the
 * state alone: the word is always there for a screen reader, and the shape differs. */
export function RailGlyph({ state }: { state: RailState }) {
  const { t } = useTranslation('nav')
  const word = t(`shell.rail.state.${state}`)
  if (state === 'need')
    return (
      <span
        role="img"
        aria-label={word}
        className="m-0.5 inline-block size-2.5 shrink-0 rotate-45 rounded-[2px] bg-warn"
      />
    )
  if (state === 'live')
    return (
      <span
        role="img"
        aria-label={word}
        className="inline-block size-3.5 shrink-0 rounded-full border-2 border-accent-soft border-t-accent border-r-accent motion-safe:animate-spin motion-safe:[animation-duration:1.2s]"
      />
    )
  const Icon = state === 'ended' ? CircleCheck : Pause
  return (
    <Icon
      role="img"
      aria-label={word}
      className="size-3.5 shrink-0 text-text-3"
    />
  )
}
