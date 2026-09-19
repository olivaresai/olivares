// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// "SOME NAMES COULD NOT BE READ" — the line a surface adds under a table whose rows had
// to fall back, and the one shape for saying it.
//
// ⛔ A ROW'S OWN FALLBACK CANNOT SAY THE CAUSE. A cell that resolves to nothing paints a
//    reference or a dash, and that looks identical whether the directory read was
//    REFUSED, FAILED, or simply answered a page this row is older than. The fallback is
//    honest about the VALUE and silent about the CAUSE: a reader seeing four bare rows
//    has no way to tell "these four are old" from "I am not allowed to see any name on
//    this screen". Only the surface knows which of those happened, so only the surface
//    can say it — once, for the table, instead of once per row.
//
// ⛔ AND IT IS ONE COMPONENT BECAUSE IT WAS ABOUT TO BE TWO. The findings table built
//    this line first (`security/components.tsx`); the sessions table needed the same
//    sentence for its profile directory read a round later. Two copies of a status line
//    drift the first time one of them changes its register, its role or its testid — and
//    an assistive technology that is told politely on one screen and not at all on the
//    next is a difference nobody decided.
//
// `role="status"` and not an alert: a name that could not be read is a caveat on what is
// already painted, not an interruption. The muted caption register keeps it below the
// data it qualifies.
import type { ReactNode } from 'react'

export interface NamesUnreadNoticeProps {
  /** The sentence, translated by the surface: it knows WHICH read could not be made. */
  children: ReactNode
  /** Stable hook for the surface's own test — one per cause, never one for all. */
  testId: string
}

export function NamesUnreadNotice({
  children,
  testId,
}: NamesUnreadNoticeProps) {
  return (
    <p
      role="status"
      data-testid={testId}
      className="text-caption text-muted-foreground"
    >
      {children}
    </p>
  )
}
