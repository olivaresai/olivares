// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { ProviderSnapshot } from '@/features/agent-tools/api'
import { accountsNearestLimit } from './accounts'

function snap(over: Partial<ProviderSnapshot>): ProviderSnapshot {
  return {
    instance: 'claude/default',
    driver: 'claude',
    default: true,
    config_dir: '',
    state: 'ready',
    installed: true,
    limits: [],
    models: [],
    checked_at: '2026-10-28T08:00:00Z',
    source: 'claude /status',
    ...over,
  }
}

describe('accountsNearestLimit: the account nearest its limit leads', () => {
  it('orders by the fullest window, highest first, and names the reset of that window', () => {
    const rows = accountsNearestLimit([
      snap({
        instance: 'claude/default',
        limits: [
          { label: '5-hour', percent: 42, resets_at: '2026-10-28T10:00:00Z' },
          { label: 'Weekly', percent: 31, resets_at: '2026-11-01T00:00:00Z' },
        ],
      }),
      snap({
        instance: 'codex/default',
        driver: 'codex',
        limits: [
          { label: '5-hour', percent: 12, resets_at: '2026-10-28T09:00:00Z' },
          { label: 'Weekly', percent: 77, resets_at: '2026-10-30T00:00:00Z' },
        ],
      }),
    ])
    expect(rows.map((r) => r.instance)).toEqual([
      'codex/default',
      'claude/default',
    ])
    expect(rows[0]!.peak).toBe(77)
    // The reset that matters is the one of the window nearest its limit.
    expect(rows[0]!.resetsAt).toBe('2026-10-30T00:00:00Z')
    expect(rows[1]!.windows.map((w) => w.label)).toEqual(['5-hour', 'Weekly'])
  })

  it('leaves out a login that is not ready or reported no window: unknown is never 0%', () => {
    const rows = accountsNearestLimit([
      snap({ instance: 'claude/default', limits: [] }),
      snap({
        instance: 'gemini/default',
        driver: 'gemini',
        state: 'not_signed_in',
        limits: [{ label: '5-hour', percent: 99 }],
      }),
      snap({
        instance: 'codex/olivares',
        driver: 'codex',
        default: false,
        limits: [{ label: 'Weekly', percent: 5 }],
      }),
    ])
    expect(rows.map((r) => r.instance)).toEqual(['codex/olivares'])
    expect(rows[0]!.resetsAt).toBeUndefined()
  })

  it('breaks a tie by the instance, so the order never flickers between reads', () => {
    const limits = [{ label: '5-hour', percent: 50 }]
    const rows = accountsNearestLimit([
      snap({ instance: 'codex/default', driver: 'codex', limits }),
      snap({ instance: 'claude/default', limits }),
    ])
    expect(rows.map((r) => r.instance)).toEqual([
      'claude/default',
      'codex/default',
    ])
  })
})
