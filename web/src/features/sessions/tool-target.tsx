// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { PathText } from '@/components/ui/path-text'
import type { ConversationItem } from './conversation-frames'

/**
 * What a tool call acts on (#3135): its summary, with the path it names shown whole through
 * PathText, so the hover and the copy are all of it ("TODO · web/src/…/x.tsx" keeps the
 * pattern as text). `copy` is off where the line sits inside a control, such as a row's
 * <summary>; `wrap` shows the whole path where a person reads it in full.
 */
export function ToolTarget({
  item,
  copy = true,
  wrap = false,
}: {
  item: Pick<ConversationItem, 'toolArgsSummary' | 'toolPath'>
  copy?: boolean
  wrap?: boolean
}) {
  const summary = item.toolArgsSummary ?? ''
  const path = item.toolPath
  if (!path || !summary.endsWith(path)) return <>{summary}</>
  return (
    <>
      {summary.slice(0, summary.length - path.length)}
      <PathText
        value={path}
        copy={copy}
        wrap={wrap}
        tone="muted"
        size="small"
      />
    </>
  )
}
