// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHICH ACCOUNT, AT A GLANCE. Eleven accounts of two tools look alike in a list; a row that
// names its account by a colored dot and a short label is told apart without opening it.
// The label is the account's display name, else its name, and the dot is its accent: both
// are metadata the account already carries (`display_name`, `accent`), shown and never
// derived. One read of the accounts, the list AI tools and New session already share,
// feeds every badge through a context: a row costs no request, and outside the provider a
// badge says nothing, so a surface can wear one without owning a read.
import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { ProviderAccent } from '@/features/agentops/provider-accent'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { useProfileList } from './use-profiles'

export interface AccountMark {
  /** The short label: the display name, else the account name. */
  label: string
  /** The stored accent; the dot paints only a color the palette knows. */
  accent?: string
}

/** What an account is called wherever it is shown. */
export const accountLabel = (
  account: Pick<ProviderAccountDTO, 'name' | 'display_name'>,
) => account.display_name?.trim() || account.name

/** The active accounts by profile reference (an account has no second id). */
export const AccountMarksContext = createContext<
  ReadonlyMap<string, AccountMark>
>(new Map())

/** Reads the accounts once for the console. A reader who may not list them gets no marks,
 * and neither does a page with no active tenant (the engine refuses that read). A changed
 * label or accent arrives with the next read: every write of an account invalidates the
 * accounts key this list lives under. */
export function AccountMarksProvider({ children }: { children: ReactNode }) {
  const { activeTenant } = useAuth()
  const list = useProfileList(!!activeTenant)
  const marks = useMemo(
    () =>
      new Map<string, AccountMark>(
        (list.data?.accounts ?? []).map((a) => [
          a.account_ref,
          { label: accountLabel(a), accent: a.accent },
        ]),
      ),
    [list.data],
  )
  return (
    <AccountMarksContext.Provider value={marks}>
      {children}
    </AccountMarksContext.Provider>
  )
}

/** The accent dot and the short label of the account behind a profile reference, or
 * nothing when the console cannot place it (not active, or not readable by this person). */
export function AccountBadge({
  profileRef,
  className,
}: {
  profileRef?: string
  className?: string
}) {
  const mark = useContext(AccountMarksContext).get(profileRef ?? '')
  if (!mark) return null
  return (
    <span
      data-testid="account-badge"
      title={mark.label}
      className={cn(
        'inline-flex min-w-0 max-w-[8rem] items-center text-caption text-text-2',
        className,
      )}
    >
      <ProviderAccent accent={mark.accent} />
      <span className="truncate">{mark.label}</span>
    </span>
  )
}
