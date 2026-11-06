// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A FILE OR FOLDER IN A REPLY IS A CHIP (#4011). An inline code span that reads as a path
// ("tools/dispatch.py:3446", "docs/") shows its name and line, opens in the editor the person
// chose in Settings at that line, carries its whole path on the hover, and its menu (right
// click, or Enter) copies the path relative to the session folder or whole. Only where the
// session's folder is known (PathRoot); elsewhere it stays code.
import { Folder } from 'lucide-react'
import { createContext, useContext, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { FileTypeIcon } from '@/components/ui/file-type-icon'
import { useCopy } from '@/components/ui/use-copy'
import { useClientSettings } from '@/features/settings/preferences'
import { EDITORS, editorLink, sshTarget } from '@/lib/editor-links'
import { pathRef } from './markdown'
import './i18n'

const RootContext = createContext<string | null>(null)

/** The session folder paths in replies are read against. */
export function PathRoot({
  root,
  children,
}: {
  root?: string | null
  children: ReactNode
}) {
  return (
    <RootContext.Provider value={root || null}>{children}</RootContext.Provider>
  )
}

/** The code span of a reply: a chip when it names a path and the session folder is known. */
export function CodeOrPath({ text, code }: { text: string; code: ReactNode }) {
  const root = useContext(RootContext)
  const ref = root ? pathRef(text) : null
  return ref && root ? <PathChip root={root} {...ref} /> : code
}

function PathChip({
  root,
  path,
  line,
}: {
  root: string
  path: string
  line?: number
}) {
  const { t } = useTranslation('sessions')
  const editor = useClientSettings((s) => s.editor)
  const editorSsh = useClientSettings((s) => s.editorSsh)
  const { copy } = useCopy()
  const [open, setOpen] = useState(false)
  const base = root.replace(/\/+$/, '')
  const folder = path.endsWith('/')
  const bare = path.replace(/\/+$/, '')
  const full = bare.startsWith('/')
    ? bare
    : `${base}/${bare.replace(/^\.\//, '')}`
  const relative = full.startsWith(`${base}/`)
    ? full.slice(base.length + 1)
    : full
  const href = full.startsWith(`${base}/`)
    ? editorLink(
        editor,
        { root: base, file: relative, line },
        sshTarget(editorSsh, window.location.hostname),
      )
    : null
  const name = `${bare.slice(bare.lastIndexOf('/') + 1)}${folder ? '/' : ''}`
  return (
    <DropdownMenu open={open} onOpenChange={setOpen} modal={false}>
      <DropdownMenuTrigger asChild>
        <a
          href={href ?? undefined}
          title={full}
          data-slot="path-chip"
          // A click follows the link; the menu opens on a right click or from the keyboard.
          onPointerDown={(e) => e.preventDefault()}
          onContextMenu={(e) => {
            e.preventDefault()
            setOpen(true)
          }}
          className="inline-flex max-w-full items-center gap-1 rounded-ctl border border-line bg-muted px-1 align-baseline font-mono text-mono-s text-text no-underline hover:border-line-strong focus-visible:outline-2 focus-visible:outline-focus"
        >
          {folder ? (
            <Folder
              className="size-3 shrink-0 text-text-3"
              aria-hidden="true"
            />
          ) : (
            <FileTypeIcon path={bare} className="size-3 shrink-0 text-text-3" />
          )}
          <span className="truncate">
            {name}
            {line !== undefined ? ` · L${line}` : ''}
          </span>
        </a>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {href ? (
          <DropdownMenuItem asChild>
            <a href={href}>
              {t('pathChip.open', { editor: EDITORS[editor].label })}
            </a>
          </DropdownMenuItem>
        ) : null}
        <DropdownMenuItem onSelect={() => void copy(relative)}>
          {t('pathChip.copyRelative')}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void copy(full)}>
          {t('pathChip.copyFull')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
