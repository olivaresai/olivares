// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CITE IN COMPOSER. Words selected inside one of the tool's replies get a small button
// beside the selection; it puts them in the composer as a quote, the way "Quote reply"
// does for a whole reply (session-conversation.tsx hands both the same function). The
// keyboard reaches it too: the table's chord presses the button while words are selected.
import { Quote } from 'lucide-react'
import { useEffect, useRef, useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Kbd } from '@/components/ui/kbd'
import {
  chordHint,
  isTypingTarget,
  modalIsOpen,
  resolveBinding,
} from '@/lib/keybindings/model'
import { CITE_KEYS, KEYBINDINGS } from '@/lib/keybindings/table'
import './i18n'

/** The reply a node is in: the content element of an assistant message. */
const REPLY = '[data-kind="assistant"]'

function replyOf(node: Node | null): Element | null {
  const element = node instanceof Element ? node : (node?.parentElement ?? null)
  return element?.closest(REPLY) ?? null
}

type Selected = { text: string; left: number; top: number }

/** The words selected inside one reply of `root`, and where they are on screen. */
function selectedInReply(root: HTMLElement | null): Selected | null {
  const selection = window.getSelection()
  if (!root || !selection || selection.isCollapsed || !selection.rangeCount)
    return null
  const reply = replyOf(selection.anchorNode)
  if (!reply || reply !== replyOf(selection.focusNode) || !root.contains(reply))
    return null
  const text = selection.toString().trim()
  if (!text) return null
  const range = selection.getRangeAt(0)
  // A test document has no layout; a browser always does.
  const box =
    typeof range.getBoundingClientRect === 'function'
      ? range.getBoundingClientRect()
      : { left: 0, width: 0, top: 0 }
  return { text, left: box.left + box.width / 2, top: box.top }
}

export function SelectionCite({
  root,
  onCite,
}: {
  root: RefObject<HTMLElement | null>
  /** Absent where the conversation has no composer to cite into. */
  onCite?: (text: string) => void
}) {
  const { t } = useTranslation('sessions')
  const [selected, setSelected] = useState<Selected | null>(null)
  useEffect(() => {
    if (!onCite) return
    const read = () => setSelected(selectedInReply(root.current))
    document.addEventListener('selectionchange', read)
    // The button sits on the screen, so it follows the selection when the view moves.
    window.addEventListener('scroll', read, true)
    window.addEventListener('resize', read)
    return () => {
      document.removeEventListener('selectionchange', read)
      window.removeEventListener('scroll', read, true)
      window.removeEventListener('resize', read)
    }
  }, [onCite, root])
  // While words are selected, the table's chord presses the button: one way to cite.
  const button = useRef<HTMLButtonElement>(null)
  const shown = !!onCite && selected !== null
  useEffect(() => {
    if (!shown) return
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing) return
      // A field being typed in keeps its key (Option+Q types "œ" on a Mac), and an
      // open dialog owns the keyboard.
      if (isTypingTarget(event.target) || modalIsOpen()) return
      const context = { replySelection: true }
      if (resolveBinding(KEYBINDINGS, event, context) !== 'surface.cite') return
      event.preventDefault()
      button.current?.click()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [shown])
  if (!onCite || !selected) return null
  return (
    <div
      className="fixed z-30 -translate-x-1/2 -translate-y-full pb-1"
      style={{ left: selected.left, top: selected.top }}
    >
      <Button
        ref={button}
        type="button"
        variant="secondary"
        size="sm"
        data-testid="selection-cite"
        aria-keyshortcuts={CITE_KEYS && chordHint(CITE_KEYS)}
        // The press must not move the selection it is about to read.
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => {
          onCite(selected.text)
          window.getSelection()?.removeAllRanges()
          setSelected(null)
        }}
      >
        <Quote aria-hidden className="size-3.5" />
        {t('conversation.cite')}
        {/* Drawn, not text, so the button's text stays its name (WCAG 2.5.3);
            aria-keyshortcuts gives the chord. */}
        {CITE_KEYS ? (
          <Kbd
            aria-hidden
            data-keys={chordHint(CITE_KEYS)}
            className="before:content-[attr(data-keys)]"
          />
        ) : null}
      </Button>
    </div>
  )
}
