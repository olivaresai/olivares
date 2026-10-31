// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LEDGER'S ENTRIES, AS SENTENCES. "Session started by Marc Vidal", grouped by the
// session they belong to (an entry on a `sessions.run`) with the entries of no session in
// their own group, and the requests that still need a person first, under their own
// heading, with the one orange edge however many there are. The edge belongs to the
// requests, never to the selection: selection is the tone step.
//
// Every value under a sentence (who acted, what it acted on) is a button that adds itself to the
// query, so a person narrows the ledger by pointing at it.
import { Check } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { RelTimeLabel } from '@/features/shared'
import type { AuditEventDTO } from '@/lib/api/types'
import { cn } from '@/lib/utils'
import {
  groupEntries,
  needsPerson,
  SESSION_KIND,
  tokenText,
  type EntryNames,
} from './ledger'
import type { QueryParams } from './query'

export function LedgerEntries({
  entries,
  pending,
  fresh,
  bySession,
  names,
  selectedId,
  onSelect,
  onAdd,
}: {
  entries: readonly AuditEventDTO[]
  pending: ReadonlySet<string>
  /** Entries that just arrived at the head of the feed: they slide in once (useNewRowIds). */
  fresh: ReadonlySet<string>
  bySession: boolean
  names: EntryNames
  selectedId: string | null
  onSelect: (e: AuditEventDTO) => void
  onAdd: (filters: QueryParams) => void
}) {
  const { t } = useTranslation('audit')
  const groups = groupEntries(entries, pending, bySession, (key, e) =>
    key === 'needs'
      ? t('ledger.needsYou')
      : key === 'other'
        ? t('ledger.outside')
        : key === 'all'
          ? ''
          : (names.target(e) ?? ''),
  )
  // The button names the token it adds; its tooltip keeps the ledger's own reference.
  const add = (label: string, filters: QueryParams, reference: string) => (
    <button
      type="button"
      onClick={() => onAdd(filters)}
      aria-label={t('ledger.add', { token: label })}
      title={reference}
      className="min-h-6 max-w-48 min-w-6 truncate rounded-sm outline-none hover:text-text hover:underline focus-visible:ring-2 focus-visible:ring-focus"
    >
      {label.slice(label.indexOf(':') + 1)}
    </button>
  )

  return (
    <ul aria-label={t('ledger.entries')} className="flex flex-col gap-4">
      {groups.map((g) => (
        <li key={g.key} className="flex flex-col gap-1">
          {g.heading ? (
            <h2 className="flex items-baseline gap-2 px-3 text-heading text-text">
              {g.session ? (
                <button
                  type="button"
                  onClick={() =>
                    onAdd({ target_kind: SESSION_KIND, target_id: g.session })
                  }
                  title={t('ledger.add', { token: `target:${g.session}` })}
                  className="rounded-sm text-left outline-none hover:underline focus-visible:ring-2 focus-visible:ring-focus"
                >
                  {g.heading}
                </button>
              ) : (
                g.heading
              )}
              <span className="text-caption font-normal text-text-2">
                {t('ledger.count', { count: g.entries.length })}
              </span>
            </h2>
          ) : null}
          <ul
            className={cn(
              'relative flex flex-col',
              g.key === 'needs' &&
                'before:absolute before:inset-y-1.5 before:left-0 before:w-[3px] before:rounded-full before:bg-accent',
            )}
          >
            {g.entries.map((e) => {
              const flagged = needsPerson(e, pending)
              const selected = e.id === selectedId
              const target = names.target(e)
              const person = tokenText(
                { key: 'person', filter: 'actor', value: e.actor },
                () => names.who(e),
              )
              return (
                <li
                  key={e.id}
                  data-seq={e.seq}
                  className={cn(
                    'flex flex-col gap-0.5 rounded-ctl px-3 py-2',
                    selected ? 'bg-active' : 'hover:bg-hover',
                    fresh.has(e.id) && 'feed-enter',
                  )}
                >
                  <button
                    type="button"
                    data-slot="entry"
                    aria-current={selected || undefined}
                    onClick={() => onSelect(e)}
                    className="rounded-sm text-left text-body font-medium text-text outline-none focus-visible:ring-2 focus-visible:ring-focus"
                  >
                    {names.sentence(e)}
                  </button>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-caption text-text-2">
                    <span className="whitespace-nowrap tabular-nums">
                      <RelTimeLabel ts={e.occurred_at} />
                    </span>
                    {e.actor ? add(person, { actor: e.actor }, e.actor) : null}
                    {target && e.target_kind && e.target_id
                      ? add(
                          `target:${target}`,
                          {
                            target_kind: e.target_kind,
                            target_id: e.target_id,
                          },
                          `${e.target_kind}: ${e.target_id}`,
                        )
                      : null}
                    <span className="font-mono text-mono-s" title={e.hash}>
                      {e.hash.slice(0, 6)}
                    </span>
                    {flagged ? (
                      <span className="font-medium text-text">
                        {t('ledger.needsYou')}
                      </span>
                    ) : e.sig ? (
                      <span className="inline-flex items-center gap-1">
                        <Check aria-hidden className="size-3" />
                        {t('ledger.signed')}
                      </span>
                    ) : null}
                  </div>
                </li>
              )
            })}
          </ul>
        </li>
      ))}
    </ul>
  )
}
