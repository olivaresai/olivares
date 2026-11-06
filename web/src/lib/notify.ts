// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE NOTICE.
//
// THE RULE: a notice is either a toast with at most one action, or an inline state in the
// place it belongs (an empty state, an error state, field help). It is never a permanent
// banner above content: a strip that stays there pushes the work down on every visit, and
// each page that grew its own decided its own look and lifetime. The only exceptions are
// conditions that block use or change what the page can do (a stop, an expired license,
// offline). They show once per page, in one line of user words, with their one action.
// `lib/notify.test.tsx` holds the rule and lists every banner-like component by category.
//
// This is the toast half. Lifetime lives in `components/ui/toaster.tsx` (6 seconds, an error
// until it is dismissed). What this adds is the memory of a CONDITION: "the engine cannot be
// reached" is a state that outlives one toast, so a person who closed it does not get it back
// on every render or every poll, only when the condition changes. The memory is per person,
// in the same server-kept preferences as the language and the onboarding dismissal.
//
// An EVENT (a link value was ignored, a copy was made) passes no `condition` and is never
// remembered: each one is news.
import { toast } from '@/components/ui/toaster'
import { preferenceStorage } from '@/lib/preference-storage'

const DISMISSED_KEY = 'olivares.notices.dismissed'
/** The newest dismissals kept. A condition older than these is shown once more. */
const REMEMBERED = 24

export interface Notice {
  message: string
  /** `error` stays until it is dismissed; the others last 6 seconds. */
  level?: 'info' | 'warning' | 'error'
  /** The one thing the person can do about it. */
  action?: { label: string; onClick: () => void }
  /**
   * A standing condition this notice reports. Once the person dismisses it, it is not shown
   * again until `signature` changes or `resolveNotice(condition)` is called.
   */
  condition?: string
  /** What the condition is right now: a different value is a different condition. */
  signature?: string
}

function dismissed(): Record<string, string> {
  try {
    const stored: unknown = JSON.parse(
      preferenceStorage.getItem(DISMISSED_KEY) ?? '{}',
    )
    return stored && typeof stored === 'object' && !Array.isArray(stored)
      ? (stored as Record<string, string>)
      : {}
  } catch {
    // An unreadable memory forgets; the worst it can do is show a notice once more.
    return {}
  }
}

function store(next: Record<string, string>) {
  const newest = Object.entries(next).slice(-REMEMBERED)
  preferenceStorage.setItem(
    DISMISSED_KEY,
    JSON.stringify(Object.fromEntries(newest)),
  )
}

function forget(condition: string) {
  const { [condition]: _gone, ...rest } = dismissed()
  store(rest)
}

/**
 * The toast showing a condition, so that taking it down because the condition is gone is
 * not mistaken for the person closing it (sonner reports both through `onDismiss`).
 */
const showing = new Map<string, { resolved: boolean }>()

export function notify({
  message,
  level = 'info',
  action,
  condition,
  signature = '',
}: Notice): void {
  if (condition === undefined) {
    toast[level](message, { action })
    return
  }
  if (dismissed()[condition] === signature) {
    // Already dismissed in this state: a toast still up was told for another one.
    const stale = showing.get(condition)
    if (stale) {
      stale.resolved = true
      toast.dismiss(condition)
    }
    return
  }
  const mine = { resolved: false }
  // A toast outlives a user switch: closing it later must not write its condition into the
  // preferences of whoever is signed in by then.
  const owner = preferenceStorage.getOwner()
  showing.set(condition, mine)
  toast[level](message, {
    id: condition,
    // sonner closes a toast on its action. A condition is not over because the person asked
    // again: Retry that fails again must leave the notice up. Its owner ends it (resolveNotice).
    action: action && {
      label: action.label,
      onClick: (event) => {
        event.preventDefault()
        action.onClick()
      },
    },
    onDismiss: () => {
      if (showing.get(condition) === mine) showing.delete(condition)
      if (mine.resolved || preferenceStorage.getOwner() !== owner) return
      const { [condition]: _old, ...rest } = dismissed()
      store({ ...rest, [condition]: signature })
    },
  })
}

/**
 * The condition is over: take its toast down and let it be shown again if it returns.
 * `keepDismissal` is for a page that is only being left: the condition may still hold, so a
 * person who had closed it is not told again, and one who had not is told on return.
 */
export function resolveNotice(
  condition: string,
  { keepDismissal = false }: { keepDismissal?: boolean } = {},
): void {
  const live = showing.get(condition)
  if (live) live.resolved = true
  toast.dismiss(condition)
  if (!keepDismissal && condition in dismissed()) forget(condition)
}

/**
 * Every toast comes down because the person who saw them is gone (sign-out). None of it was
 * closed by anyone, so none is remembered as dismissed.
 */
export function dismissAllNotices(): void {
  for (const live of showing.values()) live.resolved = true
  toast.dismiss()
}
