// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cn } from '@/lib/utils'

/**
 * The engine's identifier of a session's current action (`web.search`, `create_issue`).
 * It is machine text, shown exactly as the engine sends it in every language, so it renders
 * as code in the machine face, with the full value as its title where a cell truncates it.
 *
 * A path is cut in its MIDDLE (dev#3120): the folders shrink to an ellipsis and the file
 * name stays whole, so a long resource never pushes its pane sideways. The text in the
 * page is the whole path, so selecting and copying it takes all of it.
 */
export function ActionName({
  value,
  className,
}: {
  value: string
  className?: string
}) {
  // The last segment of the path, a trailing slash kept with it.
  const cut = value.replace(/\/+$/, '').lastIndexOf('/')
  return (
    <code
      className={cn(
        'font-mono text-mono text-foreground',
        className,
        // After the caller's classes: a cell's `block truncate` would cut the file name.
        cut > 0 && 'inline-flex min-w-0 max-w-full align-bottom',
      )}
      title={value}
    >
      {cut > 0 ? (
        <>
          <span className="min-w-0 truncate">{value.slice(0, cut)}</span>
          <span className="shrink-0 whitespace-nowrap">{value.slice(cut)}</span>
        </>
      ) : (
        value
      )}
    </code>
  )
}
