// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { Checkbox } from '@/components/ui/checkbox'
import './i18n'

interface DiscardWorktreeOptionProps {
  /** The branch of the session's own git worktree. */
  branch: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}

/**
 * The confirmation that goes with releasing a session that has a git worktree. The
 * engine removes the worktree and its branch by itself when the branch is merged and
 * the worktree is clean, and refuses otherwise; ticking this is the person's
 * confirmation to discard unmerged or uncommitted work. Unticked, the release is the
 * request it always was.
 */
export function DiscardWorktreeOption({
  branch,
  checked,
  onCheckedChange,
}: DiscardWorktreeOptionProps) {
  const { t } = useTranslation('agentops')
  const id = useId()
  return (
    <div className="flex items-start gap-3">
      <Checkbox
        id={id}
        checked={checked}
        onCheckedChange={(value) => onCheckedChange(value === true)}
        aria-describedby={`${id}-hint`}
      />
      <div className="flex flex-col gap-1">
        <label htmlFor={id} className="text-body">
          {t('discardWorktree.label', { branch })}
        </label>
        <p id={`${id}-hint`} className="text-caption text-muted-foreground">
          {t('discardWorktree.hint')}
        </p>
      </div>
    </div>
  )
}
