// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { PathText } from '@/components/ui/path-text'
import { cn } from '@/lib/utils'

/**
 * The engine's identifier of a session's current action (`web.search`, `create_issue`).
 * It is machine text, shown exactly as the engine sends it in every language, so it renders
 * as code in the machine face, with the full value as its title where a cell truncates it.
 *
 * A path goes through the shared PathText (#3135): cut in its middle, the whole value as the
 * hover and the copy. `copy` is off inside a control, such as a clickable table row.
 */
export function ActionName({
  value,
  className,
  copy = true,
}: {
  value: string
  className?: string
  copy?: boolean
}) {
  if (value.replace(/\/+$/, '').includes('/'))
    return <PathText value={value} copy={copy} />
  return (
    <code
      className={cn('font-mono text-mono text-foreground', className)}
      title={value}
    >
      {value}
    </code>
  )
}
