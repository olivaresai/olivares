// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// useUrlStateNotice — the second half of "a bad value falls back to the default AND SAYS
// SO" (plan 3.7).
//
// The first half is that every URL-state consumer validates what it reads, because a search
// param is untrusted input. The saying is a toast: it used to be a strip above the
// page that stayed there and pushed the work down, and a rejected filter is news, not a state
// the page lives in. A toast says it once and leaves.
//
// ⛔ AN EVENT, NOT A CONDITION. Each rejection is told when it happens, so none is remembered:
//    a pasted link that names the same bad key a second time is a second link, and a page that
//    said nothing the second time would show the recipient a different slice than the author
//    saw while looking exactly the same — the one thing an evidence link must never do. What
//    marks an event is the array INSTANCE `useValidatedUrlState` hands back; it keeps the same
//    instance while the URL is cleaned up, so the toast is not repeated by the cleanup. A caller
//    that builds the list inline must memoize it.
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { notify } from '@/lib/notify'
import './i18n'

export interface UrlStateNoticeOptions {
  /**
   * Where the rejected values came from. A saved view is a different fact than a pasted
   * link: the view is stored, someone else may have written it.
   */
  origin?: 'url' | 'saved-view'
  /** Optional human label for the saved view, when the origin is one. */
  savedViewName?: string
}

export function useUrlStateNotice(
  issues: readonly string[],
  { origin = 'url', savedViewName }: UrlStateNoticeOptions = {},
) {
  const { t } = useTranslation('shared')
  const told = useRef<readonly string[] | null>(null)
  useEffect(() => {
    if (issues.length === 0 || told.current === issues) return
    told.current = issues
    const rejected = { count: issues.length, keys: issues.join(', ') }
    notify({
      level: 'warning',
      message:
        origin === 'saved-view'
          ? t('urlState.savedViewRejected', {
              ...rejected,
              name: savedViewName ?? t('urlState.thisSavedView'),
            })
          : t('urlState.urlRejected', rejected),
    })
  }, [issues, origin, savedViewName, t])
}

export interface UrlStateNoticeProps extends UrlStateNoticeOptions {
  /** The keys the view's decoder refused, from useValidatedUrlState. */
  issues: readonly string[]
}

/**
 * The element form of the hook, for the pages that still render `<UrlStateNotice />`: it
 * tells by toast and draws nothing. The strip it used to draw is gone (the notice rule,
 * lib/notify.ts).
 *
 * ponytail: a bridge for Business pages that import this name; remove it when they call
 * `useUrlStateNotice` themselves.
 */
export function UrlStateNotice({ issues, ...options }: UrlStateNoticeProps) {
  useUrlStateNotice(issues, options)
  return null
}
