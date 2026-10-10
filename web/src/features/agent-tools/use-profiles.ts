// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The profiles AI tools lists: a profile is what the engine calls a provider account (a
// name, its own home, one way to sign in). One read of the whole list, so a tool's
// rows and the next free name come from the same answer.
import { useQuery } from '@tanstack/react-query'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type {
  ProviderAccountDTO,
  ProviderProfileDTO,
} from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'

/** The most pages one read follows: 20 x 100 accounts is far past any real install. */
const MAX_PAGES = 20

export interface ProfileList {
  /** Every active account: the set both AI tools and New session count. */
  accounts: ProviderAccountDTO[]
  /** The references of accounts that are not active (disabled, removed). */
  inactive: ReadonlySet<string>
  /** Every name the engine has reserved, removed ones included (a name is unique). */
  taken: ReadonlySet<string>
  /** The read stopped at its bound with more accounts left. */
  truncated: boolean
  /** The engine names its profiles: some profile carries `account_name`. */
  named: boolean
}

/**
 * Whether the default login is its own row. When the engine names its profiles (`named`),
 * the default login is the profile WITHOUT an account name and the named ones are the
 * accounts, so it is a row of its own whatever the accounts look like. An engine before
 * names gives none: then the account it adopted from the default login's home, the one
 * marked `adopted`, is the default login, and no second row stands for it.
 */
export const needsDefaultRow = (
  accounts: readonly ProviderAccountDTO[],
  named: boolean,
) => named || !accounts.some((a) => a.home_mode === 'adopted')

/** Every profile, read page by page up to the bound; none when the read is not allowed or
 * fails (the accounts alone then say what they can). */
async function readProfiles(
  signal: AbortSignal,
): Promise<ProviderProfileDTO[]> {
  const all: ProviderProfileDTO[] = []
  try {
    let cursor: string | undefined
    for (let page = 0; page < MAX_PAGES; page++) {
      const answer = await agentOpsApi.listProfiles({ cursor }, { signal })
      all.push(...answer.items)
      if (!answer.has_more || !answer.cursor) break
      cursor = answer.cursor
    }
  } catch {
    return signal.aborted ? [] : all
  }
  return all
}

export function useProfileList(enabled = true) {
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const canReadProfiles = can('sessions:profile:read')
  return useQuery<ProfileList>({
    queryKey: [
      ...agentOpsKeys.accounts(activeTenant, boundary.epoch),
      'tool-profiles',
    ],
    queryFn: async ({ signal }) => {
      const all: ProviderAccountDTO[] = []
      let cursor: string | undefined
      let more = false
      for (let page = 0; page < MAX_PAGES; page++) {
        const answer = await agentOpsApi.listAccounts({ cursor }, { signal })
        all.push(...answer.items)
        more = answer.has_more && !!answer.cursor
        if (!more) break
        cursor = answer.cursor
      }
      const profiles = canReadProfiles ? await readProfiles(signal) : []
      const names = profiles.flatMap((p) =>
        p.account_name ? [p.account_name] : [],
      )
      return {
        accounts: all.filter((a) => a.state === 'active'),
        inactive: new Set(
          all.filter((a) => a.state !== 'active').map((a) => a.account_ref),
        ),
        // A profile holds its name in whatever state, whether or not the accounts read
        // listed it.
        taken: new Set([...all.map((a) => a.name), ...names]),
        truncated: more,
        named: names.length > 0,
      }
    },
    enabled: enabled && can('sessions:account:read'),
  })
}
