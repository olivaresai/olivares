// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT A SCREEN SAYS WHEN THE ANSWER IS NOT "YES".
//
// The engine publishes four kinds of non-positive and this console must not flatten them
// into one, because three of the four are NOT statements about the operator's authority:
//
//   · ESTABLISHED REFUSAL (`denied` / `not_reachable`) — the engine decided, and said so.
//     The existing calm Forbidden notice is exactly right, and it is the ONLY case where a
//     refusal may be shown.
//
//   · CONCEALED NON-VERDICT (`undisclosed`) — the engine looked and declined to say. It
//     asserts NEITHER a denial, NOR a missing target, NOR a policy outage, NOR an evidence
//     failure, NOR a missing READ permission, and the whole point of the concealment is
//     that the console cannot tell which. So the text names none of them.
//
//     ⛔ AND IT DOES NOT REUSE THE EXISTING `unavailable` COPY, which says the engine could
//        not look. Here it DID look. That sentence would be a false statement about the
//        engine and a hint about the cause — the exact leak the non-disclosure exists to
//        close, reintroduced in the one place nobody audits: the string table.
//
//   · PENDING — no answer yet. It says so and keeps its place; it never renders as a
//     refusal, because "I have not asked yet" and "you may not" are different sentences.
//
//   · LOCAL UNKNOWN — expired, moved, malformed, unreachable. This console could not
//     establish an answer. Retryable, and equally undiagnosed.
//
// The last three share one shape and ONE cadence, which is the property that matters at
// this layer: `lib/auth/capabilities.ts` refreshes every completed non-positive on the same
// visible interval, so the retry rhythm cannot be read as a hint about the cause. This
// component's job is to make the DOM and the words match that.
import { useTranslation } from 'react-i18next'
import { Spinner } from '@/components/ui/spinner'
import type { CapabilityAccess } from '@/lib/auth/capabilities'
import { FailureNotice } from './failure-notice'

export function CapabilityNotice({ access }: { access: CapabilityAccess }) {
  const { t } = useTranslation('communications')

  if (access === 'denied' || access === 'not_reachable')
    return <FailureNotice failure={{ kind: 'forbidden' }} />

  if (access === 'checking')
    return (
      <div
        data-slot="capability-checking"
        className="flex items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2 text-body text-muted-foreground"
        role="status"
        aria-live="polite"
      >
        <Spinner className="size-3.5" />
        <span>{t('capability.checking')}</span>
      </div>
    )

  const undisclosed = access === 'undisclosed'
  return (
    <div
      data-slot={
        undisclosed ? 'capability-undisclosed' : 'capability-unavailable'
      }
      className="space-y-1 rounded-md border border-border bg-muted/30 px-3 py-2"
      role="status"
      aria-live="polite"
    >
      <p className="text-body font-medium">
        {t(
          undisclosed
            ? 'capability.undisclosedTitle'
            : 'capability.unavailableTitle',
        )}
      </p>
      <p className="text-body text-muted-foreground">
        {t(
          undisclosed
            ? 'capability.undisclosedBody'
            : 'capability.unavailableBody',
        )}
      </p>
      <p
        data-slot="capability-retry"
        className="text-caption text-muted-foreground"
      >
        {t('capability.retrying')}
      </p>
    </div>
  )
}
