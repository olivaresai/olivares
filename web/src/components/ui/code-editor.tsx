// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CodeEditor — the governance code surface. A thin, accessible React wrapper
// over CodeMirror 6 for JSON / Cedar / Rego authoring and read-only viewing.
//
// CSP/Trusted-Types contract (do NOT regress — §d, docs/SECURITY-HARDENING.md): CM6 needs no
// `unsafe-eval`, no workers, and no string-to-HTML sink, so the L3 CSP +
// `require-trusted-types-for 'script'` posture stays intact. We add NO Trusted
// Types exception for the editor. Any HTML we ever render around it must still go
// through `trustedHTML()`.
//
// Accessibility: CM's contentDOM is `role="textbox" aria-multiline="true"`; we add
// an accessible name (+ optional description). We deliberately do NOT bind Tab to
// indentation, so Tab/Shift+Tab move focus out — no keyboard trap (WCAG 2.1.2).
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { type Diagnostic, linter, lintGutter } from '@codemirror/lint'
import { Compartment, EditorState, Annotation } from '@codemirror/state'
import {
  EditorView,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
  placeholder as placeholderExt,
} from '@codemirror/view'
import { useEffect, useId, useRef } from 'react'
import { cn } from '@/lib/utils'
import {
  type CodeLanguage,
  baseHighlightExtensions,
  jsonParseLinter,
  languageExtension,
} from './code-languages'

/** A schema/lint finding the editor renders inline (offsets into the document). */
export interface CodeDiagnostic {
  from: number
  to: number
  severity: 'error' | 'warning' | 'info'
  message: string
}

export interface CodeEditorProps {
  value: string
  onChange?: (value: string) => void
  language: CodeLanguage
  /** Read-only view (history viewer, dry-run preview). Default false. */
  readOnly?: boolean
  /** Accessible name for the editor textbox. REQUIRED for a11y. */
  ariaLabel: string
  /** Optional help/description id wired via aria-describedby. */
  describedById?: string
  placeholder?: string
  /** Attach JSON parse-error linting (only meaningful for language="json"). */
  jsonLint?: boolean
  /**
   * Caller-supplied semantic linter (schema validation etc). MEMOIZE it — the
   * editor reconfigures only when its identity changes. Offsets index the doc.
   */
  lintSource?: (doc: string) => CodeDiagnostic[]
  /** Danger ring for an invalid editor (mirrors the Input/Textarea idiom). */
  invalid?: boolean
  /** Editor body height (CSS length). Default 16rem. */
  height?: string
  className?: string
  /** Forwarded to the outer wrapper for label association via the content id. */
  id?: string
}

/** Marks transactions we dispatch to sync the external value, so we don't echo
 *  them back through onChange (avoids an update loop). */
const External = Annotation.define<boolean>()

function toCmDiagnostics(
  items: CodeDiagnostic[],
  docLength: number,
): Diagnostic[] {
  return items
    .map((d) => ({
      from: Math.max(0, Math.min(d.from, docLength)),
      to: Math.max(0, Math.min(d.to, docLength)),
      severity: d.severity,
      message: d.message,
    }))
    .sort((a, b) => a.from - b.from)
}

export function CodeEditor({
  value,
  onChange,
  language,
  readOnly = false,
  ariaLabel,
  describedById,
  placeholder,
  jsonLint = false,
  lintSource,
  invalid = false,
  height = '16rem',
  className,
  id,
}: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const viewRef = useRef<EditorView | null>(null)
  const onChangeRef = useRef(onChange)
  useEffect(() => {
    onChangeRef.current = onChange
  }, [onChange])

  // Compartments let us reconfigure pieces without tearing down the view.
  const languageComp = useRef(new Compartment())
  const readOnlyComp = useRef(new Compartment())
  const lintComp = useRef(new Compartment())
  const fallbackId = useId()
  const editorId = id ?? fallbackId

  // Mount once. (value/readOnly/language sync via effects below.)
  useEffect(() => {
    if (!hostRef.current || viewRef.current) return
    const lintExts = []
    if (jsonLint && language === 'json')
      lintExts.push(linter(jsonParseLinter()))
    if (lintSource) {
      lintExts.push(
        linter((view) =>
          toCmDiagnostics(
            lintSource(view.state.doc.toString()),
            view.state.doc.length,
          ),
        ),
      )
    }
    if (lintExts.length) lintExts.push(lintGutter())

    const state = EditorState.create({
      doc: value,
      extensions: [
        lineNumbers(),
        highlightActiveLine(),
        highlightActiveLineGutter(),
        drawSelection(),
        history(),
        keymap.of([...defaultKeymap, ...historyKeymap]),
        baseHighlightExtensions,
        languageComp.current.of(languageExtension(language)),
        readOnlyComp.current.of([
          EditorState.readOnly.of(readOnly),
          EditorView.editable.of(!readOnly),
        ]),
        lintComp.current.of(lintExts),
        placeholder ? placeholderExt(placeholder) : [],
        EditorView.contentAttributes.of({
          'aria-label': ariaLabel,
          ...(describedById ? { 'aria-describedby': describedById } : {}),
        }),
        EditorView.updateListener.of((update) => {
          if (
            update.docChanged &&
            !update.transactions.some((tr) => tr.annotation(External))
          ) {
            onChangeRef.current?.(update.state.doc.toString())
          }
        }),
        EditorView.lineWrapping,
      ],
    })
    viewRef.current = new EditorView({ state, parent: hostRef.current })
    return () => {
      viewRef.current?.destroy()
      viewRef.current = null
    }
    // Mount-only: subsequent prop changes are handled by the focused effects below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Sync external value → editor (without echoing back through onChange).
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    const current = view.state.doc.toString()
    if (value !== current) {
      view.dispatch({
        changes: { from: 0, to: current.length, insert: value },
        annotations: External.of(true),
      })
    }
  }, [value])

  useEffect(() => {
    viewRef.current?.dispatch({
      effects: languageComp.current.reconfigure(languageExtension(language)),
    })
  }, [language])

  useEffect(() => {
    viewRef.current?.dispatch({
      effects: readOnlyComp.current.reconfigure([
        EditorState.readOnly.of(readOnly),
        EditorView.editable.of(!readOnly),
      ]),
    })
  }, [readOnly])

  // Reconfigure the lint stack when its inputs change.
  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    const lintExts = []
    if (jsonLint && language === 'json')
      lintExts.push(linter(jsonParseLinter()))
    if (lintSource) {
      lintExts.push(
        linter((v) =>
          toCmDiagnostics(
            lintSource(v.state.doc.toString()),
            v.state.doc.length,
          ),
        ),
      )
    }
    if (lintExts.length) lintExts.push(lintGutter())
    view.dispatch({ effects: lintComp.current.reconfigure(lintExts) })
  }, [lintSource, jsonLint, language])

  return (
    <div
      id={editorId}
      data-slot="code-editor"
      data-language={language}
      ref={hostRef}
      style={{ maxHeight: height }}
      className={cn(
        'overflow-auto rounded-md border border-border-strong bg-surface',
        'focus-within:border-ring focus-within:ring-2 focus-within:ring-ring focus-within:ring-offset-1 focus-within:ring-offset-background',
        readOnly && 'bg-muted/40',
        invalid && 'border-danger ring-1 ring-danger',
        className,
      )}
    />
  )
}
