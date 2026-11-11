// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE START COMPOSER'S WORKING COPY: the folder's current checkout, or a worktree and branch
// of the session's own, started at a branch or commit the person names. The engine makes the
// worktree (`worktree`, `worktree_from` of a new run) and refuses a start point that is not in
// the repository; this only asks. The classic form shows the choice in place, the
// composer's pill holds it in a popover.
import { GitBranch } from 'lucide-react'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { Pill } from './start-pills'

export interface StartWorktree {
  on: boolean
  /** A branch or commit id; empty starts at the folder's current commit. */
  from: string
}

/** The two choices and, for a new worktree, where it starts. */
export function WorktreeChoice({
  value,
  onChange,
}: {
  value: StartWorktree
  onChange: (next: StartWorktree) => void
}) {
  const { t } = useTranslation('firstHour')
  const group = useId()
  return (
    <div className="flex flex-col gap-2">
      {[false, true].map((on) => (
        <label key={String(on)} className="flex items-center gap-2 text-body">
          <input
            type="radio"
            name={group}
            checked={value.on === on}
            onChange={() => onChange({ ...value, on })}
            className="size-4 accent-accent"
          />
          {on ? t('start.worktreeNew') : t('start.worktreeCheckout')}
        </label>
      ))}
      {value.on ? (
        <>
          <Input
            value={value.from}
            onChange={(event) =>
              onChange({ ...value, from: event.target.value.trim() })
            }
            aria-label={t('start.worktreeFrom')}
            placeholder={t('start.worktreeFromHint')}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            mono
          />
          <p className="text-caption text-text-3">
            {t('start.worktreeNewHint')}
          </p>
        </>
      ) : null}
    </div>
  )
}

export function WorktreePill({
  value,
  onChange,
}: {
  value: StartWorktree
  onChange: (next: StartWorktree) => void
}) {
  const { t } = useTranslation('firstHour')
  const shown = value.on ? t('start.worktreeNew') : t('start.worktreeCheckout')
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Pill label={t('composer.worktreePill', { value: shown })}>
          <GitBranch aria-hidden className="size-3.5 shrink-0" />
          <span className="truncate">{shown}</span>
        </Pill>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 max-w-[calc(100vw-2rem)]">
        <WorktreeChoice value={value} onChange={onChange} />
      </PopoverContent>
    </Popover>
  )
}
