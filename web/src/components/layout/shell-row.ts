// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cn } from '@/lib/utils'

/** Row chrome of a sidebar destination, shared by the journeys and the areas entry. The
 * current row is a neutral fill and full-strength text: orange marks the one primary
 * action of a view, never where the person is (console 1.0). */
export const SHELL_ROW_CLASS = cn(
  'relative flex min-h-8 min-w-0 items-center gap-2.5 rounded-ctl px-2.5 text-body font-medium text-text-2 outline-none transition-colors duration-fast',
  'hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus',
  '[&_svg]:size-4 [&_svg]:shrink-0',
  'aria-[current=page]:bg-active aria-[current=page]:text-text',
)

/** A destination in the rail: a 40 px square icon target with the same states. */
export const RAIL_ITEM_CLASS = cn(
  'relative grid size-10 shrink-0 place-items-center rounded-ctl text-text-2 outline-none transition-colors duration-fast',
  'hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus',
  '[&_svg]:size-[18px]',
  'aria-[current=page]:bg-active aria-[current=page]:text-text',
)
