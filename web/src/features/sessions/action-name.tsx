// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cn } from '@/lib/utils'

/**
 * The engine's identifier of a session's current action (`web.search`, `create_issue`).
 * It is machine text, shown exactly as the engine sends it in every language, so it renders
 * as code in the machine face, with the full value as its title where a cell truncates it.
 */
export function ActionName({
  value,
  className,
}: {
  value: string
  className?: string
}) {
  return (
    <code
      className={cn('font-mono text-mono text-foreground', className)}
      title={value}
    >
      {value}
    </code>
  )
}
