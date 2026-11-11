// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CodeDiff — side-by-side read-only diff of two policy revisions (version/diff).
// Built on @codemirror/merge (same CSP/Trusted-Types-clean engine as CodeEditor).
// Both panes are read-only; this is a viewer, never an authoring surface.
import { MergeView, unifiedMergeView, diff } from '@codemirror/merge'
import { EditorState, Prec } from '@codemirror/state'
import { EditorView, lineNumbers } from '@codemirror/view'
import { useEffect, useRef } from 'react'
import { usePreferencesStore } from '@/stores/preferences'
import { DiffControls } from './diff-controls'
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
  /** Makes each pane's line numbers clickable: called with the pane (the older revision
   * is `original`, the newer `modified`) and the 1-based line. The panes stay read-only. */
  onLineClick?: (side: 'original' | 'modified', line: number) => void
  height?: string
  className?: string
}

export function CodeDiff({
  original,
  modified,
  language,
  originalLabel,
  modifiedLabel,
  onLineClick,
  height = '20rem',
  className,
}: CodeDiffProps) {
  const { layout, wrap, whitespace } = usePreferencesStore((s) => s.diffDisplay)
  const hostRef = useRef<HTMLDivElement | null>(null)
  const mergeRef = useRef<MergeView | EditorView | null>(null)
  // The editors are built once per revision; they call whichever handler is current, so a
  // parent that passes a new function each render does not rebuild them.
  const lineClickRef = useRef(onLineClick)
  useEffect(() => {
    lineClickRef.current = onLineClick
  })
  const clickable = onLineClick !== undefined

  useEffect(() => {
    if (!hostRef.current || mergeRef.current) return
    const readOnly = (label: string, side: 'original' | 'modified') => [
      lineNumbers(
        clickable
          ? {
              domEventHandlers: {
                click: (view, block) => {
                  lineClickRef.current?.(
                    side,
                    view.state.doc.lineAt(block.from).number,
                  )
                  return true
                },
              },
            }
          : undefined,
      ),
      ...(clickable
        ? [
            EditorView.theme({
              '.cm-lineNumbers .cm-gutterElement': { cursor: 'pointer' },
            }),
          ]
        : []),
      baseHighlightExtensions,
      // The diff sits in a well: the editor and its gutter let the well show through. The
      // added and removed lines take the diff tints, their gutter marks and changed words
      // the status inks, in place of the editor library's own colors. Each selector has the
      // specificity of the library's rule it replaces, and this theme comes after it.
      Prec.highest(
        EditorView.theme({
          '&, .cm-gutters': { backgroundColor: 'transparent' },
          '&.cm-merge-b .cm-changedLine': {
            backgroundColor: 'var(--diff-add)',
          },
          '&.cm-merge-a .cm-changedLine, .cm-deletedChunk': {
            backgroundColor: 'var(--diff-del)',
          },
          '&.cm-merge-b .cm-changedLineGutter': { background: 'var(--ok)' },
          '&.cm-merge-a .cm-changedLineGutter, .cm-deletedLineGutter': {
            background: 'var(--bad)',
          },
          '&.cm-merge-b .cm-changedText': {
            background:
              'linear-gradient(var(--ok), var(--ok)) bottom / 100% 2px no-repeat',
          },
          '&.cm-merge-a .cm-changedText, .cm-deletedChunk .cm-deletedText': {
            background:
              'linear-gradient(var(--bad), var(--bad)) bottom / 100% 2px no-repeat',
          },
        }),
      ),
      languageExtension(language),
      EditorState.readOnly.of(true),
      EditorView.editable.of(false),
      ...(wrap ? [EditorView.lineWrapping] : []),
      EditorView.contentAttributes.of({ 'aria-label': label }),
    ]
    const diffConfig = whitespace
      ? {
          override: (a: string, b: string) =>
            diff(a, b, { scanLimit: 500 }).filter(
              (change) =>
                a.slice(change.fromA, change.toA).replace(/[ \t]/g, '') !==
                b.slice(change.fromB, change.toB).replace(/[ \t]/g, ''),
            ),
        }
      : undefined
    mergeRef.current =
      layout === 'unified'
        ? new EditorView({
            doc: modified,
            extensions: [
              ...readOnly(modifiedLabel, 'modified'),
              unifiedMergeView({
                original,
                diffConfig,
                mergeControls: false,
                collapseUnchanged: { margin: 3, minSize: 4 },
              }),
            ],
            parent: hostRef.current,
          })
        : new MergeView({
            a: {
              doc: original,
              extensions: readOnly(originalLabel, 'original'),
            },
            b: {
              doc: modified,
              extensions: readOnly(modifiedLabel, 'modified'),
            },
            parent: hostRef.current,
            collapseUnchanged: { margin: 3, minSize: 4 },
            diffConfig,
            highlightChanges: true,
            gutter: true,
          })
    return () => {
      mergeRef.current?.destroy()
      mergeRef.current = null
    }
    // Rebuild the merge view when the revisions or language change.
  }, [
    original,
    modified,
    language,
    originalLabel,
    modifiedLabel,
    clickable,
    layout,
    wrap,
    whitespace,
  ])

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <DiffControls />
      <div
        data-slot="code-diff"
        ref={hostRef}
        style={{ maxHeight: height }}
        className={cn(
          'overflow-auto rounded-ctl bg-[var(--well)] text-[0.8125rem] shadow-[var(--shadow-well)]',
          className,
        )}
      />
    </div>
  )
}
