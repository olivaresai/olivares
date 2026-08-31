// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// FilesPanel — sidebar panel that extracts file paths from resource_ref on
// Read/Write/Edit tool events in the timeline. Builds a flat list of unique
// file paths with operation-type icons.
import { Eye, File, FilePen, FilePlus, FileText } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import type { LucideIcon } from 'lucide-react'
import type { FileNode, TimelineEntry } from './types'
import './i18n'

export interface FilesPanelProps {
  /** The full timeline entries to extract file references from. */
  timeline: TimelineEntry[]
}

/** Tool name patterns that imply file operations. */
const FILE_TOOLS: Record<string, FileNode['type']> = {
  Read: 'read',
  Write: 'write',
  Edit: 'edit',
  Create: 'create',
}

const TYPE_ICON: Record<FileNode['type'], LucideIcon> = {
  read: Eye,
  write: FileText,
  edit: FilePen,
  create: FilePlus,
}

const TYPE_VARIANT: Record<FileNode['type'], 'neutral' | 'info' | 'accent' | 'success'> = {
  read: 'neutral',
  write: 'info',
  edit: 'accent',
  create: 'success',
}

export function FilesPanel({ timeline }: FilesPanelProps) {
  const { t } = useTranslation('session-viewer')

  const files = useMemo<FileNode[]>(() => {
    const seen = new Map<string, FileNode>()
    for (const entry of timeline) {
      if (entry.kind !== 'tool' || !entry.tool_ref || !entry.resource_ref)
        continue

      // Match tool_ref against known file-operation tools.
      const opType = FILE_TOOLS[entry.tool_ref]
      if (!opType) continue

      const path = entry.resource_ref
      // Use the last path segment as the display name.
      const segments = path.split('/')
      const name = segments[segments.length - 1] ?? path

      // Keep the first operation type seen for each unique path.
      if (!seen.has(path)) {
        seen.set(path, { name, path, type: opType })
      }
    }

    return Array.from(seen.values()).sort((a, b) => a.path.localeCompare(b.path))
  }, [timeline])

  return (
    <section className="flex flex-col gap-2">
      <h2 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        <File className="size-3.5" aria-hidden />
        {t('panels.files')}
      </h2>

      {files.length === 0 ? (
        <p className="text-xs text-muted-foreground">—</p>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {files.map((f) => {
            const Icon = TYPE_ICON[f.type]
            return (
              <li
                key={f.path}
                className="flex items-center gap-1.5 rounded-md px-2 py-1 text-xs"
                title={f.path}
              >
                <Icon className="size-3 shrink-0 text-muted-foreground" aria-hidden />
                <span className="min-w-0 truncate font-mono text-foreground">
                  {f.name}
                </span>
                <Badge variant={TYPE_VARIANT[f.type]} className="ml-auto shrink-0 text-[10px]">
                  {t(`files.${f.type}`)}
                </Badge>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
