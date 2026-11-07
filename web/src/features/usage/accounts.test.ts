// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { accountsNearestLimit } from './accounts'

function account(
  ref: string,
  utilization: number[] | null,
): ProviderAccountDTO {
  return {
    account_ref: ref,
    name: ref,
    driver: 'claude',
    usage: utilization?.map((u, i) => ({
      window: i === 0 ? 'five_hour' : 'seven_day',
      label: i === 0 ? '5-hour' : 'Weekly',
      utilization: u,
      observed_at: '2026-10-28T08:00:00Z',
    })),
  } as ProviderAccountDTO
}

describe('accountsNearestLimit: the account nearest its limit leads', () => {
  it('orders by the fullest window, highest first', () => {
    const rows = accountsNearestLimit([
      account('a', [42, 31]),
      account('b', [12, 77]),
    ])
    expect(rows.map((r) => r.account_ref)).toEqual(['b', 'a'])
  })

  it('leaves out an account that reported no window: unknown is never 0%', () => {
    const rows = accountsNearestLimit([
      account('none', null),
      account('empty', []),
      account('seen', [5]),
    ])
    expect(rows.map((r) => r.account_ref)).toEqual(['seen'])
  })

  it('breaks a tie by the reference, so the order never flickers between reads', () => {
    const rows = accountsNearestLimit([
      account('codex', [50]),
      account('claude', [50]),
    ])
    expect(rows.map((r) => r.account_ref)).toEqual(['claude', 'codex'])
  })
})
