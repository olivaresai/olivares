// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { CodeDiff } from '@/components/ui/code-diff'
import type { CodeLanguage } from '@/components/ui/code-languages'
import './i18n'

/**
 * A text file beside what git HEAD holds for it: a File | Changes switch over the same
 * CodeDiff the policy revisions use. `committed` is HEAD's text; it is undefined when
 * there is nothing to compare against (no repository, a file never committed, a file too
 * large to read), and then only the file view (children) is shown, with no switch to
 * nothing.
 */
export function HeadDiff({
  committed,
  current,
  language = 'text',
  height,
  unavailable = false,
  children,
}: {
  committed: string | undefined
  current: string
  language?: CodeLanguage
  /** The height of the diff pane, so it matches the file view it replaces. */
  height?: string
  /** HEAD's text could not be read (see headUnavailable): say so under the file. */
  unavailable?: boolean
  /** The plain view of the file, shown under "File". */
  children: ReactNode
}) {
  const { t } = useTranslation('shared')
  const [view, setView] = useState<'file' | 'changes'>('file')
  const showDiff = committed !== undefined && view === 'changes'
  // One wrapper in every state, so the file view keeps its identity (an editor keeps its
  // cursor, a <pre> its scroll) when HEAD's text arrives or goes away.
  return (
    <div className="flex min-w-0 flex-col gap-2">
      {committed !== undefined && (
        <div
          role="group"
          aria-label={t('headDiff.view')}
          className="flex gap-1"
        >
          <Button
            type="button"
            size="sm"
            variant={view === 'file' ? 'secondary' : 'ghost'}
            aria-pressed={view === 'file'}
            onClick={() => setView('file')}
          >
            {t('headDiff.file')}
          </Button>
          <Button
            type="button"
            size="sm"
            variant={view === 'changes' ? 'secondary' : 'ghost'}
            aria-pressed={view === 'changes'}
            onClick={() => setView('changes')}
          >
            {t('headDiff.changes')}
          </Button>
        </div>
      )}
      {!showDiff ? (
        children
      ) : committed === current ? (
        <p className="text-caption text-muted-foreground">
          {t('headDiff.same')}
        </p>
      ) : (
        <CodeDiff
          original={committed}
          modified={current}
          language={language}
          originalLabel={t('headDiff.committed')}
          modifiedLabel={t('headDiff.current')}
          height={height}
        />
      )}
      {committed === undefined && unavailable && (
        <p role="status" className="text-caption text-muted-foreground">
          {t('headDiff.unavailable')}
        </p>
      )}
    </div>
  )
}
