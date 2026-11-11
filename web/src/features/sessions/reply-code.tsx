// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A CODE BLOCK OF A REPLY: its language, coloured; Run in terminal (a shell block, where the
// session has a terminal: the drawer provides it and refuses a command with hidden characters),
// Wrap lines and Copy. The text is the model's and is painted only as text; Copy writes it
// exactly.
import { Play, WrapText } from 'lucide-react'
import {
  Fragment,
  useEffect,
  useReducer,
  useRef,
  useState,
  type RefObject,
} from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { CopyButton } from '@/components/ui/copy-button'
import { cn } from '@/lib/utils'
import { highlightPieces, isShellFence, loadGrammar } from './code-highlight'
import { useRunInTerminal } from './run-in-terminal'
import './i18n'

// A token's ink is the editors' (`code-highlight.ts` reads their map, so the block
// follows the theme). These are the kinds only a reply's code shows, which that map has
// no ink for: a diff's added and removed lines, a literal, a type's name, a fence's meta.
const TOKENS =
  '[&_.tok-inserted]:text-success [&_.tok-literal]:text-success ' +
  '[&_.tok-typeName]:text-info [&_.tok-className]:text-info ' +
  '[&_.tok-deleted]:text-danger [&_.tok-meta]:text-text-3'

/** Whether the block has come near the screen: its grammar, and CodeMirror with it, loads
 * only then. It is watched from the thread's own scroller (the conversation's log), so near
 * is ahead in the thread; from the page where it is in none. Without an
 * IntersectionObserver it is near at once. */
function useNear(box: RefObject<HTMLElement | null>): boolean {
  const [near, setNear] = useState(
    () => typeof IntersectionObserver !== 'function',
  )
  useEffect(() => {
    const el = box.current
    if (near || !el) return
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) setNear(true)
      },
      {
        root: el.closest<HTMLElement>('[role="log"]'),
        rootMargin: '600px 0px',
      },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [near, box])
  return near
}

/** The block's pieces: plain while its grammar is not loaded (the same text in the same
 * face, so nothing moves), then coloured in the same render as each new text, so a
 * streamed block never shows plain in between. The grammar loads once the block is near. */
function useCodePieces(text: string, lang: string | undefined, near: boolean) {
  const [, grammarLoaded] = useReducer((n: number) => n + 1, 0)
  const pieces = highlightPieces(text, lang)
  const loading = pieces === undefined
  useEffect(() => {
    if (!loading || !near) return
    let current = true
    void loadGrammar(lang).then(() => current && grammarLoaded())
    return () => {
      current = false
    }
  }, [loading, near, lang])
  return pieces ?? null
}

export function ReplyCode({ text, lang }: { text: string; lang?: string }) {
  const { t } = useTranslation('sessions')
  const { t: tc } = useTranslation('common')
  const [wrap, setWrap] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const pieces = useCodePieces(text, lang, useNear(box))
  const terminal = useRunInTerminal()
  return (
    <div
      ref={box}
      className="overflow-hidden rounded-ctl bg-[var(--well)] shadow-[var(--shadow-well)]"
      data-slot="reply-code-box"
    >
      <div
        data-md-skip=""
        className="flex min-h-8 items-center justify-end gap-0.5 ps-3.5 pe-1 pt-1"
      >
        <span
          className="me-auto min-w-0 truncate font-mono text-caption text-text-3"
          data-slot="reply-code-lang"
        >
          {lang}
        </span>
        {terminal && isShellFence(lang) ? (
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={t('terminal.runInTerminal')}
            title={t('terminal.runInTerminal')}
            onClick={() => terminal.run(text)}
          >
            <Play aria-hidden />
          </Button>
        ) : null}
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label={t('conversation.code.wrap')}
          aria-pressed={wrap}
          title={t('conversation.code.wrap')}
          onClick={() => setWrap((on) => !on)}
        >
          <WrapText aria-hidden />
        </Button>
        <CopyButton
          text={() => text}
          label={tc('ui.copy.code')}
          copiedLabel={tc('ui.copy.copied')}
          failedLabel={tc('ui.copy.failed')}
        />
      </div>
      <pre
        tabIndex={0}
        data-slot="reply-code"
        data-lang={lang}
        className={cn(
          'm-0 px-3.5 pt-1 pb-2.5 font-mono text-mono text-text focus-visible:ring-2 focus-visible:ring-focus focus-visible:ring-inset',
          wrap ? 'whitespace-pre-wrap break-words' : 'overflow-x-auto',
          TOKENS,
        )}
      >
        <code>
          {pieces
            ? pieces.map((piece, i) =>
                piece.cls || piece.ink ? (
                  <span
                    key={i}
                    className={piece.cls ?? undefined}
                    style={piece.ink}
                  >
                    {piece.text}
                  </span>
                ) : (
                  <Fragment key={i}>{piece.text}</Fragment>
                ),
              )
            : text}
        </code>
      </pre>
    </div>
  )
}
