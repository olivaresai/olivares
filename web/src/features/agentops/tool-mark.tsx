// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TOOL BY ITS MARK. Every place that names a tool (AI tools, New session, the
// sessions list, the session header) draws the same mark from the engine's own tool
// table (GET /v1/m/sessions/tools): its vendor's mark where the console has one, else its
// letters and color, so a tool reads the same everywhere.
import { MonoMark } from '@/components/ui/mono-mark'
import { cn } from '@/lib/utils'
import { engineTool, toolName } from './engine-tools'

export function ToolMark({
  driver,
  labelled = false,
  className,
}: {
  driver: string | null
  /** Named for a screen reader: for a mark that stands without the tool's name. */
  labelled?: boolean
  className?: string
}) {
  if (!driver)
    return <span aria-hidden className={cn('size-5 shrink-0', className)} />
  const tool = engineTool(driver)
  const name = toolName(driver)
  return (
    <MonoMark
      name={name}
      letters={tool?.monogram}
      vendor={tool?.vendor}
      color={tool?.color}
      size="sm"
      labelled={labelled}
      title={labelled ? name : undefined}
      data-testid="tool-mark"
      className={className}
    />
  )
}
