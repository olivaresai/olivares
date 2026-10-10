// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, expect, it, vi } from 'vitest'
import { FEATURE_VIEWS, type FeatureView } from '@/features/registry'
import type { FavoritesRemote, PersonalLink } from './personal-navigation-store'

const paidIds = ['finops', 'redteam', 'team-costs']
const community = FEATURE_VIEWS.filter((v) => !paidIds.includes(v.id))
// Registry metadata stands in for the private extension; the storage/session code is real.
const business = [
  ...community,
  ...paidIds.map((id) => ({
    ...community.find((v) => v.id === 'storedBudgets')!,
    id,
    path: id === 'redteam' ? '/red-team' : `/${id}`,
  })),
]
async function edition(views: readonly FeatureView[]) {
  vi.resetModules()
  vi.doMock('@/features/registry', async (original) => ({
    ...(await original<typeof import('@/features/registry')>()),
    FEATURE_VIEWS: views,
  }))
  return import('./personal-navigation-store')
}
afterEach(() => {
  vi.doUnmock('@/features/registry')
  vi.resetModules()
  localStorage.clear()
})

it('preserves Business favorites through Community load/toggle and Business reload', async () => {
  localStorage.clear()
  let account: readonly PersonalLink[] = []
  const remote: FavoritesRemote = {
    load: async () => ({ favorites: account, stored: account.length > 0 }),
    save: async (favorites) => {
      account = favorites
    },
  }
  const settle = () => new Promise((resolve) => setTimeout(resolve, 0))
  const key = 'edition-favorites'
  const open = (store: Awaited<ReturnType<typeof edition>>) =>
    store.createPersonalNavigationSession({ key, current: () => true, remote })
  const b = await edition(business)
  const first = open(b)
  await settle()
  const paid = paidIds.map((id) => b.personalLink(id)!)
  for (const link of paid) first.setFavorite(link, true)
  await settle()
  expect(account).toEqual(paid)
  first.revoke()

  const c = await edition(community)
  const middle = open(c)
  expect(middle.getSnapshot().favorites).toEqual(paid)
  await settle()
  expect(
    middle
      .getSnapshot()
      .favorites.filter((link) => c.resolvePersonalLink(link)),
  ).toEqual([])
  middle.setFavorite(c.personalLink('home')!, true)
  await settle()
  expect(account).toEqual([...paid, { kind: 'feature', id: 'home' }])
  middle.setFavorite(c.personalLink('home')!, false)
  await settle()
  expect(account).toEqual(paid)
  middle.revoke()

  const again = await edition(business)
  const last = open(again)
  await settle()
  expect(last.getSnapshot().favorites).toEqual(paid)
  expect(
    last
      .getSnapshot()
      .favorites.map((link) => again.resolvePersonalLink(link)?.id),
  ).toEqual(paidIds)
  expect(JSON.parse(localStorage.getItem(key)!).favorites).toEqual(paid)
  last.revoke()
})
