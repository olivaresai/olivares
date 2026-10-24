// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TOOL BY ITS MARK. A list of sessions that paints one terminal glyph on every row says
// nothing about which tool runs it, so each row shows the tool's own mark. Olivares
// draws no vendor logo: the mark is the tool's initials, its name on the hover and for a
// screen reader.
import { cn } from '@/lib/utils'

/** The initials of a tool's name, in capitals: the first letters of its first two words
 * ("Claude Code" CC, "Gemini CLI" GC), else its two capitals ("OpenCode" OC), else its
 * first two letters ("Codex" CO). */
export function monogram(name: string): string {
  const words = name.split(/[\s_-]+/).filter(Boolean)
  if (words.length >= 2) return `${words[0]![0]}${words[1]![0]}`.toUpperCase()
  const capitals = name.match(/[A-Z]/g)
  if (capitals && capitals.length >= 2) return capitals.slice(0, 2).join('')
  return name.slice(0, 2).toUpperCase()
}

export function ToolMark({
  tool,
  className,
}: {
  tool: string | null
  className?: string
}) {
  if (!tool)
    return <span aria-hidden className={cn('size-6 shrink-0', className)} />
  return (
    <span
      role="img"
      aria-label={tool}
      title={tool}
      data-testid="tool-mark"
      className={cn(
        'flex size-6 shrink-0 select-none items-center justify-center rounded-[6px] bg-muted text-overline font-semibold text-text-2',
        className,
      )}
    >
      {monogram(tool)}
    </span>
  )
}
