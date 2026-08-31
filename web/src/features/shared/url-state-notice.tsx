// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// UrlStateNotice — the second half of "a bad value falls back to the default
// AND SAYS SO" (plan 3.7).
//
// The first half already existed: every URL-state consumer validates what it
// reads, because a search param is untrusted input. What did NOT exist is the
// saying. audit-view and observability-view both dropped a rejected value in
// silence, so a shared deep-link could show the recipient a different slice
// than the author saw while looking exactly the same — which is the one thing
// an evidence link must never do.
//
// Deliberately NOT a toast: it belongs beside the filters it is about, it must
// survive long enough to be read, and it must not fire on a page load where
// nothing was wrong. role="status" + aria-live="polite" so a screen reader is
// told without being interrupted (a rejected filter is not an emergency).
import { X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import './i18n'

export interface UrlStateNoticeProps {
  /** The keys the view's decoder refused, from useValidatedUrlState. */
  issues: readonly string[]
  /**
   * Where the rejected values came from. A saved view is a different fact than
   * a pasted link: the view is stored, someone else may have written it, and
   * "your saved view no longer works" is the actionable half.
   */
  origin?: 'url' | 'saved-view'
  /** Optional human label for the saved view, when the origin is one. */
  savedViewName?: string
}

export function UrlStateNotice({
  issues,
  origin = 'url',
  savedViewName,
}: UrlStateNoticeProps) {
  const { t } = useTranslation('shared')
  const [dismissed, setDismissed] = useState(false)
  const signature = issues.join(',')

  // A NEW rejection after a dismissal must speak again: the operator dismissed
  // the previous one, not every future one.
  useEffect(() => {
    setDismissed(false)
  }, [signature, origin])

  // …including a new rejection that happens to name the SAME keys. Keying the
  // reset on the names alone silenced that case permanently: paste a bad link,
  // dismiss, paste another bad link with the same key, and the page says
  // nothing while quietly ignoring it.
  //
  // What re-arms it is the EVENT, and the hook marks an event by handing back a
  // new array instance. Waiting for an empty state in between would never fire:
  // the hook latches the complaint across its own URL cleanup precisely so the
  // notice survives long enough to be read.
  const lastEvent = useRef<readonly string[] | null>(null)
  useEffect(() => {
    if (issues.length === 0) return
    if (lastEvent.current !== issues) {
      if (lastEvent.current !== null) setDismissed(false)
      lastEvent.current = issues
    }
  }, [issues])

  if (issues.length === 0 || dismissed) return null

  return (
    <div
      role="status"
      aria-live="polite"
      data-testid="url-state-notice"
      className="flex items-start justify-between gap-3 rounded-md border border-warning bg-warning/10 px-3 py-2 text-sm text-foreground"
    >
      <p className="min-w-0">
        {origin === 'saved-view'
          ? t('urlState.savedViewRejected', {
              count: issues.length,
              keys: issues.join(', '),
              name: savedViewName ?? t('urlState.thisSavedView'),
            })
          : t('urlState.urlRejected', {
              count: issues.length,
              keys: issues.join(', '),
            })}
      </p>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t('urlState.dismiss')}
        data-testid="url-state-notice-dismiss"
        onClick={() => setDismissed(true)}
      >
        <X />
      </Button>
    </div>
  )
}
