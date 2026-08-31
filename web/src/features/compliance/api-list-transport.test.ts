// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { complianceApi } from './api'

const COMPLETE: Array<[string, () => Promise<unknown>]> = [
  ['risk', () => complianceApi.risk()],
  ['residency', () => complianceApi.residency()],
  ['retention policies', () => complianceApi.retentionPolicies()],
  ['retention runs', () => complianceApi.retentionRuns()],
  ['holds', () => complianceApi.holds()],
  ['hold events', () => complianceApi.holdEvents('hold-1')],
  ['erasures', () => complianceApi.erasures()],
  ['erasure events', () => complianceApi.erasureEvents('erase-1')],
  ['DORA registers', () => complianceApi.doraRegisters()],
  ['DORA incidents', () => complianceApi.doraIncidents()],
  ['NIS2 incidents', () => complianceApi.nis2Incidents()],
  ['OSCAL profiles', () => complianceApi.oscalProfiles()],
  ['US law packs', () => complianceApi.usLawPacks()],
  ['sector packs', () => complianceApi.sectorPacks()],
  ['FedRAMP packs', () => complianceApi.fedrampPacks()],
  ['AIMS packs', () => complianceApi.aimsPacks()],
  ['CCM snapshots', () => complianceApi.ccmSnapshots()],
  ['CCM drift', () => complianceApi.ccmDrift()],
]

describe('compliance complete routes do not pretend to paginate', () => {
  it.each(COMPLETE)('%s sends no decorative limit', async (_name, call) => {
    let sent = ''
    globalThis.fetch = vi.fn(async (url: string) => {
      sent = String(url)
      return new Response(JSON.stringify({ items: [], has_more: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }) as never
    await call()
    expect(new URL(sent, 'http://test').searchParams.get('limit')).toBeNull()
  })
})
