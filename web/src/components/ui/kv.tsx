// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * KvList / KvRow — the definition-list primitive for detail sheets and metadata
 * panels (an MCP server's config, a deployment revision, a KB's governance). A
 * KvRow lays out a muted label on the left and a right-aligned value; `mono` uses
 * the tabular monospace face for ids / hashes / counts. Rows divide with a hairline.
 */
export function KvList({
  className,
  children,
}: {
  className?: string
  children: ReactNode
}) {
  return <dl className={cn('divide-y divide-border', className)}>{children}</dl>
}

export function KvRow({
  label,
  children,
  mono = false,
  align = 'right',
  className,
}: {
  label: ReactNode
  children: ReactNode
  /** Render the value in the tabular monospace face (ids / hashes / counts). */
  mono?: boolean
  /** Value alignment; `start` for wrapping multi-line content. */
  align?: 'start' | 'right'
  className?: string
}) {
  return (
    <div
      className={cn(
        'flex items-baseline justify-between gap-4 py-2.5',
        className,
      )}
    >
      <dt className="shrink-0 text-sm text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          'min-w-0 text-sm text-foreground',
          align === 'right' ? 'text-right' : 'flex-1 text-left',
          mono && 'font-mono text-xs tabular-nums',
        )}
      >
        {children}
      </dd>
    </div>
  )
}
