// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LEDGER QUERY FIELD, drawn in the top bar where search is (TopbarSearch), or at the
// head of the page where the bar has no room for it (audit-view.tsx). The filters
// already applied are removable tokens; what is typed is read on Enter (query.ts) and
// joins them. In the bar the field is the shell search's width and grows with its
// tokens: the text being typed gives way, the filters do not. Backspace in an empty
// field takes the last token back. The palette's key (⌘K on a Mac, Ctrl+K elsewhere)
// still opens it; the hint names that key as the shell search does.
import { Command, Search, X } from 'lucide-react'
import { useId, useRef, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Kbd } from '@/components/ui/kbd'
import { chordHint } from '@/lib/keybindings/model'
import { PALETTE_KEYS } from '@/lib/keybindings/table'
import { cn } from '@/lib/utils'
import { useCommandStore } from '@/stores/command'
import {
  EMPTY_DRAFT,
  parseQuery,
  tokensOf,
  type FilterKey,
  type LedgerDraft,
  type QueryParams,
} from './query'
import { tokenText } from './ledger'

export function LedgerQuery({
  params,
  onChange,
  draft,
  onDraft,
  resolvePerson,
  personName,
  className,
}: {
  className?: string
  params: QueryParams
  onChange: (next: QueryParams) => void
  draft: LedgerDraft
  onDraft: (next: LedgerDraft) => void
  resolvePerson: (name: string) => string[] | null
  personName: (actor: string) => string | null
}) {
  const { t } = useTranslation('audit')
  const setCommandOpen = useCommandStore((s) => s.setOpen)
  const { text, problems } = draft
  const problemId = useId()
  const field = useRef<HTMLInputElement>(null)
  const tokens = tokensOf(params)

  const remove = (filter: FilterKey) => onChange({ ...params, [filter]: '' })

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    // Enter that ends an IME composition (ja, ko, zh) commits the word, not the query.
    if (e.nativeEvent.isComposing) return
    if (e.key === 'Enter') {
      e.preventDefault()
      const read = parseQuery(text, resolvePerson)
      if (read.problems.length > 0) {
        onDraft({ text, problems: read.problems })
        return
      }
      onDraft(EMPTY_DRAFT)
      if (Object.keys(read.params).length > 0)
        onChange({ ...params, ...read.params })
    } else if (e.key === 'Backspace' && text === '' && tokens.length > 0) {
      remove(tokens[tokens.length - 1]!.filter)
    }
  }

  return (
    <div
      role="search"
      className={cn(
        'relative flex h-9 w-max max-w-[28rem] min-w-50 shrink items-center gap-1 rounded-full bg-hover pr-1 pl-3 text-body focus-within:ring-2 focus-within:ring-focus',
        className,
      )}
    >
      <Search aria-hidden className="size-4 shrink-0 text-text-2" />
      <ul className="flex min-w-0 shrink items-center gap-1 overflow-hidden">
        {tokens.map((token) => {
          const label = tokenText(token, personName)
          const key = token.key ? `${token.key}:` : ''
          return (
            <li
              key={token.filter}
              className="flex h-6 min-w-0 shrink items-center gap-0.5 rounded-full border border-line-strong bg-canvas pr-0.5 pl-2 font-mono text-mono-s text-text"
            >
              {/* Short of room the key gives way first: the value is what was asked. */}
              <span className="flex min-w-0" title={label}>
                {key ? (
                  <span className="min-w-0 shrink-[100] truncate text-text-2">
                    {key}
                  </span>
                ) : null}
                <span className="min-w-0 truncate">
                  {label.slice(key.length)}
                </span>
              </span>
              <button
                type="button"
                onClick={() => {
                  remove(token.filter)
                  // The token's own button goes with it: the keyboard stays in the query.
                  field.current?.focus()
                }}
                aria-label={t('ledger.remove', { token: label })}
                className="grid size-5 shrink-0 place-items-center rounded-full text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus"
              >
                <X aria-hidden className="size-3" />
              </button>
            </li>
          )
        })}
      </ul>
      <input
        ref={field}
        type="text"
        value={text}
        onChange={(e) => {
          onDraft({ text: e.currentTarget.value, problems: [] })
        }}
        onKeyDown={onKeyDown}
        aria-label={t('ledger.query')}
        aria-invalid={problems.length > 0 || undefined}
        aria-describedby={problems.length > 0 ? problemId : undefined}
        placeholder={
          tokens.length > 0 ? t('ledger.addFilter') : t('ledger.placeholder')
        }
        spellCheck={false}
        autoComplete="off"
        className="h-full w-0 min-w-10 flex-1 bg-transparent text-text outline-none placeholder:text-text-2"
      />
      <button
        type="button"
        onClick={() => setCommandOpen(true)}
        aria-label={t('ledger.commands')}
        aria-keyshortcuts="Control+K Meta+K"
        className="inline-flex shrink-0 items-center justify-center rounded-full text-text-2 outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-focus max-[1100px]:size-7"
      >
        {/* Narrow, the shortcut folds to its icon, as the shell's search does. */}
        {PALETTE_KEYS ? (
          <Kbd
            aria-hidden
            data-keys={chordHint(PALETTE_KEYS)}
            className="before:content-[attr(data-keys)] max-[1100px]:hidden"
          />
        ) : null}
        <Command aria-hidden className="hidden size-4 max-[1100px]:block" />
      </button>
      {problems.length > 0 ? (
        <p
          id={problemId}
          role="alert"
          className="absolute top-full right-0 z-30 mt-1 w-max max-w-[min(32rem,90vw)] rounded-ctl border border-line bg-raised px-3 py-2 text-caption text-text shadow-pop"
        >
          {problems
            .map((p) => t(`ledger.problem.${p.reason}`, { token: p.token }))
            .join(' ')}
        </p>
      ) : null}
    </div>
  )
}
