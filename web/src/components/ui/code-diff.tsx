// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CodeDiff — side-by-side read-only diff of two policy revisions (version/diff).
// Built on @codemirror/merge (same CSP/Trusted-Types-clean engine as CodeEditor).
// Both panes are read-only; this is a viewer, never an authoring surface.
import { MergeView } from '@codemirror/merge'
import { EditorState } from '@codemirror/state'
import { EditorView, lineNumbers } from '@codemirror/view'
import { useEffect, useRef } from 'react'
import { cn } from '@/lib/utils'
import {
  type CodeLanguage,
  baseHighlightExtensions,
  languageExtension,
} from './code-languages'

export interface CodeDiffProps {
  /** The older revision (left pane). */
  original: string
  /** The newer revision (right pane). */
  modified: string
  language: CodeLanguage
  /** Accessible name for the left (original) pane. */
  originalLabel: string
  /** Accessible name for the right (modified) pane. */
  modifiedLabel: string
  height?: string
  className?: string
}

export function CodeDiff({
  original,
  modified,
  language,
  originalLabel,
  modifiedLabel,
  height = '20rem',
  className,
}: CodeDiffProps) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const mergeRef = useRef<MergeView | null>(null)

  useEffect(() => {
    if (!hostRef.current || mergeRef.current) return
    const readOnly = (label: string) => [
      lineNumbers(),
      baseHighlightExtensions,
      languageExtension(language),
      EditorState.readOnly.of(true),
      EditorView.editable.of(false),
      EditorView.lineWrapping,
      EditorView.contentAttributes.of({ 'aria-label': label }),
    ]
    mergeRef.current = new MergeView({
      a: { doc: original, extensions: readOnly(originalLabel) },
      b: { doc: modified, extensions: readOnly(modifiedLabel) },
      parent: hostRef.current,
      collapseUnchanged: { margin: 3, minSize: 4 },
      highlightChanges: true,
      gutter: true,
    })
    return () => {
      mergeRef.current?.destroy()
      mergeRef.current = null
    }
    // Rebuild the merge view when the revisions or language change.
  }, [original, modified, language, originalLabel, modifiedLabel])

  return (
    <div
      data-slot="code-diff"
      ref={hostRef}
      style={{ maxHeight: height }}
      className={cn(
        'overflow-auto rounded-md border border-border-strong bg-surface text-[0.8125rem]',
        className,
      )}
    />
  )
}
