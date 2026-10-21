// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect, type MouseEvent } from 'react'
import { useTranslation } from 'react-i18next'
import type { RunCommandDTO } from '@/features/agentops/types'
import { cn } from '@/lib/utils'
import { commandLabel, slashOptionId } from './slash-menu-model'

/** The sources the tool names; anything else shows no source tag. */
const SOURCES = new Set(['built-in', 'user', 'project', 'plugin'])

/**
 * The "/" menu above the composer: what the session's tool reports, as a listbox. Focus
 * stays in the text field (the textarea points at the active option with
 * aria-activedescendant), so typing keeps narrowing the list; choosing an entry only
 * puts text in the field. Without entries it says why in one line (emptyText). The
 * composer owns the one live region that announces the menu to a screen reader.
 */
export function SlashMenu({
  listId,
  items,
  active,
  emptyText,
  notice,
  onPick,
}: {
  listId: string
  items: readonly RunCommandDTO[]
  active: number
  emptyText: string
  /** One line under the list when what it shows may be out of date; "" for none. Its own live region in the composer says it once. */
  notice: string
  onPick: (command: RunCommandDTO) => void
}) {
  const { t } = useTranslation('nav')
  useEffect(() => {
    document
      .getElementById(slashOptionId(listId, active))
      ?.scrollIntoView?.({ block: 'nearest' })
  }, [listId, active, items])

  // Pressing the mouse anywhere in the menu (padding, scrollbar) keeps the caret in the field.
  const keepFocus = (event: MouseEvent) => event.preventDefault()
  const frame =
    'absolute inset-x-0 bottom-full z-20 mb-1 rounded-md border border-border bg-elevated shadow-lg'
  if (items.length === 0) {
    return (
      <div data-testid="slash-menu" className={frame} onMouseDown={keepFocus}>
        {/* Heard once, from the composer's live region. */}
        <p aria-hidden className="px-3 py-2 text-caption text-muted-foreground">
          {emptyText}
        </p>
        <Notice text={notice} />
      </div>
    )
  }
  return (
    <div data-testid="slash-menu" className={frame} onMouseDown={keepFocus}>
      <ul
        id={listId}
        role="listbox"
        aria-label={t('launcher.slashLabel')}
        className="max-h-64 overflow-y-auto p-1"
      >
        {items.map((command, index) => (
          <li
            key={`${command.kind}:${command.source}:${command.name}`}
            id={slashOptionId(listId, index)}
            role="option"
            aria-selected={index === active}
            // mousedown, not click: the field keeps focus, so the caret and the list stay.
            // Only the primary button picks: a context menu must not insert.
            onMouseDown={(event) => {
              if (event.button === 0) onPick(command)
            }}
            className={cn(
              'flex min-w-0 cursor-pointer items-baseline gap-2 rounded-[6px] px-2 py-1.5 text-body-s',
              index === active && 'bg-muted ring-1 ring-inset ring-line-strong',
            )}
          >
            <span
              title={commandLabel(command)}
              className="min-w-0 truncate font-mono text-text"
            >
              {commandLabel(command)}
            </span>
            <span className="flex min-w-0 flex-1 items-baseline gap-2">
              {command.argument_hint ? (
                <span
                  title={command.argument_hint}
                  className="min-w-0 truncate font-mono text-caption text-text-3"
                >
                  {command.argument_hint}
                </span>
              ) : null}
              <span
                title={command.description}
                className="min-w-0 flex-1 truncate text-text-2"
              >
                {command.description}
              </span>
            </span>
            <SourceTag command={command} />
          </li>
        ))}
      </ul>
      <Notice text={notice} />
    </div>
  )
}

function Notice({ text }: { text: string }) {
  return text ? (
    <p
      aria-hidden
      data-testid="slash-notice"
      className="border-t border-border px-3 py-1.5 text-caption text-muted-foreground"
    >
      {text}
    </p>
  ) : null
}

function SourceTag({ command }: { command: RunCommandDTO }) {
  const { t } = useTranslation('nav')
  const word =
    command.kind === 'agent'
      ? t('launcher.slash.agent')
      : SOURCES.has(command.source)
        ? t(`launcher.slash.source.${command.source}`)
        : null
  return word ? (
    <span className="shrink-0 rounded-[6px] border border-line px-1.5 py-0.5 text-overline font-normal text-text-2">
      {word}
    </span>
  ) : null
}
