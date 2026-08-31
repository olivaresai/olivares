// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  desajustesPorFichero,
  leer,
  nombresDeLista,
  rutasSinTecho,
} from '@/test/list-ceiling-ratchet'

/**
 * These handlers deliberately return complete collections. Compliance's shared
 * `listAll` follows every keyset cursor (`modules/compliance/helpers.go`), so a
 * client-side limit would be decorative and a truncation badge unreachable.
 */
const NO_PAGINAN: Record<string, string> = {
  risk: 'handleListRisk uses listAll (modules/compliance/risk.go)',
  residency:
    'handleListResidency uses listAll (modules/compliance/residency.go)',
  retentionPolicies:
    'handleListRetentionPolicies uses listAll (modules/compliance/retention.go)',
  retentionRuns:
    'handleListRetentionRuns uses listAll (modules/compliance/retention.go)',
  holds: 'handleListHolds uses listAll (modules/compliance/holds.go)',
  holdEvents: 'handleListHoldEvents uses listAll (modules/compliance/holds.go)',
  erasures: 'handleListErasure uses listAll (modules/compliance/erasure.go)',
  erasureEvents:
    'handleListErasureEvents uses listAll (modules/compliance/erasure.go)',
  doraRegisters:
    'handleListDORARegisters uses listAll (modules/compliance/regpackage.go)',
  doraIncidents:
    'handleListIncidents uses listAll (modules/compliance/doraincident.go)',
  nis2Incidents:
    'handleListNIS2Incidents uses listAll (modules/compliance/nis2incident.go)',
  oscalProfiles:
    'handleListOSCALProfiles uses listAll (modules/compliance/oscalprofile.go)',
  usLawPacks:
    'handleListUSStatePacks uses listAll (modules/compliance/depthhandlers.go)',
  sectorPacks:
    'handleListSectorPacks uses listAll (modules/compliance/depthhandlers.go)',
  fedrampPacks:
    'handleListFedRAMPKSIs uses listAll (modules/compliance/depthhandlers.go)',
  aimsPacks:
    'handleListAIMSPacks uses listAll (modules/compliance/aimspack.go)',
  ccmSnapshots:
    'handleListCCMSnapshots uses listAll (modules/compliance/depthhandlers.go)',
  ccmDrift:
    'handleListDriftFindings uses listAll (modules/compliance/depthhandlers.go)',
}

const VISTAS = [
  'features/compliance/compliance-view.tsx',
  'features/compliance/retention-view.tsx',
  'features/compliance/holds-view.tsx',
  'features/compliance/erasure-view.tsx',
  'features/compliance/nis2-view.tsx',
  'features/compliance/regops-view.tsx',
  'features/executive/executive-view.tsx',
]

describe('compliance · complete lists stay explicitly outside the truncation contract', () => {
  it('every GET ListResponse is backed by a measured complete handler', () => {
    const { rutas, sinTecho } = rutasSinTecho(
      leer('features/compliance/api.ts'),
      NO_PAGINAN,
    )
    expect(rutas).toHaveLength(18)
    expect(Object.keys(NO_PAGINAN).sort()).toEqual([...rutas].sort())
    expect(sinTecho).toEqual([])
  })

  it('there is no paginated compliance query that needs a badge', () => {
    const listas = nombresDeLista(leer('features/compliance/api.ts')).filter(
      (n) => !(n in NO_PAGINAN),
    )
    const { total, desajustes } = desajustesPorFichero(
      VISTAS.map((nombre) => ({ nombre, src: leer(nombre) })),
      listas,
      'complianceApi',
    )
    expect(listas).toEqual([])
    expect(total).toBe(0)
    expect(desajustes).toEqual([])
  })
})
