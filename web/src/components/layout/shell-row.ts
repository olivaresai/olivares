// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cn } from '@/lib/utils'

/** Row chrome of a sidebar destination, shared by the journeys and the areas entry. */
export const SHELL_ROW_CLASS = cn(
  'relative flex min-h-[34px] min-w-0 items-center gap-2.5 rounded-ctl px-2.5 text-body font-medium text-text-2 outline-none transition-colors duration-fast',
  'hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus',
  '[&_svg]:size-4 [&_svg]:shrink-0',
  'aria-[current=page]:bg-active aria-[current=page]:text-text',
  "aria-[current=page]:before:absolute aria-[current=page]:before:top-[9px] aria-[current=page]:before:bottom-[9px] aria-[current=page]:before:-left-0.5 aria-[current=page]:before:w-[3px] aria-[current=page]:before:rounded-[3px] aria-[current=page]:before:bg-accent aria-[current=page]:before:content-['']",
)
