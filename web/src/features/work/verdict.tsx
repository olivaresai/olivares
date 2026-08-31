// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { CircleAlert, CircleCheck, CircleHelp } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { Verdict, WorkCheck } from './types'

/**
 * THE THREE-OUTCOME RENDERER. It exists so that NO_HE_PODIDO_MIRAR can never be drawn
 * as an empty state, as "no results", or as a success anywhere in this feature.
 *
 * Rule 5 of the operating canon: "Tres respuestas, nunca dos: limpio / roto / NO HE
 * PODIDO MIRAR. Confundir la tercera con la primera es el defecto más caro de este
 * repositorio." The kernel returns the third for corrupt or unreadable evidence and for
 * an unwired port, and it can arrive on a 200 (see api.ts).
 *
 * Deliberate visual choices, because "grey means nothing happened" is the whole trap:
 *  - unknown is WARNING-coloured and carries its own icon. It is not muted, not neutral
 *    and not a spinner. An operator glancing at the screen must not be able to mistake
 *    it for calm.
 *  - it never renders as an absence. There is no branch in this feature where an
 *    unknown verdict produces an EmptyState.
 */
const VERDICT_STYLE: Record<
  Verdict,
  { variant: 'success' | 'danger' | 'warning'; Icon: typeof CircleCheck }
> = {
  LIMPIO: { variant: 'success', Icon: CircleCheck },
  ROTO: { variant: 'danger', Icon: CircleAlert },
  NO_HE_PODIDO_MIRAR: { variant: 'warning', Icon: CircleHelp },
}

export function VerdictBadge({
  verdict,
  className,
}: {
  verdict: Verdict
  className?: string
}) {
  const { t } = useTranslation('work')
  const style = VERDICT_STYLE[verdict] ?? VERDICT_STYLE.NO_HE_PODIDO_MIRAR
  const { Icon } = style
  return (
    <Badge
      variant={style.variant}
      className={cn('gap-1', className)}
      // The verdict is the answer, so it is announced rather than left to colour.
      title={t(`verdict.${verdict}.help`)}
    >
      <Icon aria-hidden className="size-3.5" />
      {t(`verdict.${verdict}.label`)}
    </Badge>
  )
}

/**
 * The panel shown when the engine could not look. It states the outcome in words, names
 * the engine's code, and — crucially — says what is NOT known, because the operator's
 * next action depends on it.
 *
 * `retryHint` is shown for an interrupted APPLY, where the write may or may not have
 * landed. That is the case where reusing the same idempotency key is the correct and
 * safe move, and the operator has to be told so explicitly: without it, the natural
 * instinct is to start over, which is how one intention becomes two.
 */
export function UnavailableNotice({
  code,
  retryHint = false,
  className,
  children,
}: {
  code: string
  retryHint?: boolean
  className?: string
  children?: React.ReactNode
}) {
  const { t } = useTranslation('work')
  return (
    <div
      role="status"
      className={cn(
        'flex flex-col gap-2 rounded-lg border border-warning-line bg-warning-soft p-4 text-sm text-warning',
        className,
      )}
    >
      <div className="flex items-center gap-2 font-medium">
        <CircleHelp aria-hidden className="size-4 shrink-0" />
        {t('unavailable.title')}
      </div>
      <p className="text-warning">{t('unavailable.body')}</p>
      {retryHint ? (
        <p className="text-warning">{t('unavailable.retryHint')}</p>
      ) : null}
      <p className="font-mono text-xs text-warning">
        {t('unavailable.code', { code })}
      </p>
      {children}
    </div>
  )
}

/** The per-check breakdown an Assessment carries. Each check has its own verdict, so a
 * mixed result cannot be summarised away into the best one. */
export function ChecksList({ checks }: { checks: WorkCheck[] }) {
  const { t } = useTranslation('work')
  if (!checks?.length) return null
  return (
    <ul className="flex flex-col gap-1.5">
      {checks.map((c, i) => (
        <li
          key={`${c.name}-${i}`}
          className="flex items-center justify-between gap-3 text-sm"
        >
          <span className="font-mono text-xs text-muted-foreground">
            {c.name}
          </span>
          <span className="flex items-center gap-2">
            {c.evidence_ref ? (
              <span className="font-mono text-xs text-muted-foreground">
                {t('checks.evidence', { ref: c.evidence_ref })}
              </span>
            ) : null}
            <VerdictBadge verdict={c.verdict} />
          </span>
        </li>
      ))}
    </ul>
  )
}
