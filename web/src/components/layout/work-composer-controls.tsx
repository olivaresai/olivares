// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SMALL CONTROLS OF THE COMPOSER'S FOOTER that act on the turn: Plan first, Stop, and
// setting a draft aside. Each only draws and reports a press; the composer owns what it does.
import { Archive, ArchiveRestore, Square } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type { TurnInterrupt } from '@/features/agentops/turn-interrupt'

/** A picker of the composer's footer: a flat combo, a quiet word and a chevron, its menu
 * on click; 30 px high, 44 px at phone width, where the chevron goes. Plan first is
 * pressed in the same shape. */
export const PICKER =
  'inline-flex h-7.5 min-w-0 max-w-48 shrink items-center gap-1 whitespace-nowrap rounded-sm px-2.25 text-caption font-normal text-text-2 outline-none hover:bg-hover hover:text-text focus-visible:ring-2 focus-visible:ring-focus max-[760px]:min-h-11'

/** Plan first: the tool reads and proposes, and changes nothing until the person says
 * go. It is the session's plan permission mode, applied from the next message. */
export function PlanToggle({
  planning,
  disabled,
  reason,
  onToggle,
}: {
  planning: boolean
  disabled: boolean
  /** Why it cannot be pressed, when a policy says so. */
  reason?: string
  onToggle: () => void
}) {
  const { t } = useTranslation('nav')
  return (
    <button
      type="button"
      data-testid="composer-plan"
      aria-pressed={planning}
      disabled={disabled}
      title={reason || t('launcher.planHint')}
      onClick={onToggle}
      className={cn(
        PICKER,
        'shrink-0 disabled:cursor-not-allowed disabled:opacity-50',
        planning && 'bg-hover text-text',
      )}
    >
      {t('launcher.plan')}
    </button>
  )
}

/** Stops the turn that is running and keeps the session, beside Send. */
export function StopButton({ turn }: { turn: TurnInterrupt }) {
  const { t } = useTranslation('agentops')
  if (!turn.offered || !turn.allowed) return null
  return (
    <Button
      type="button"
      variant="secondary"
      size="icon"
      className="rounded-full"
      disabled={turn.pending}
      aria-label={t('live.interrupt')}
      title={t('live.interruptHint')}
      data-testid="composer-stop"
      onClick={turn.interrupt}
    >
      <Square aria-hidden className="fill-current" />
    </Button>
  )
}

/** Sets the draft (words and attachments) aside, or brings the last one back into an
 * empty field: Ctrl+S does the same. */
export function StashButton({
  hasDraft,
  stashed,
  disabled,
  onClick,
}: {
  hasDraft: boolean
  stashed: number
  disabled: boolean
  onClick: () => void
}) {
  const { t } = useTranslation('nav')
  // An empty field with nothing set aside has nothing to offer.
  if (!hasDraft && stashed === 0) return null
  const label = hasDraft ? t('launcher.stash') : t('launcher.stashRestore')
  const Icon = hasDraft ? Archive : ArchiveRestore
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className="rounded-full"
      disabled={disabled}
      aria-label={label}
      title={label}
      data-testid="composer-stash"
      onClick={onClick}
    >
      <Icon aria-hidden />
    </Button>
  )
}
