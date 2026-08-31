// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WEB-DECLARED reference: the Bedrock id / residency honesty notes. Everything else
// this file used to declare (MODEL_LIFECYCLES, PARAM_DEPRECATION, the AsOf/source
// constants) is served LIVE since by GET /v1/m/models/platforms and lives only
// in ./fixtures for the tests. The notes REMAIN web-declared on purpose: they cite
// modules/models/bedrock.go honesty facts (CRIS id format, Global/Regional premium,
// US burndown) that the platforms endpoint does not serve — they are i18n-keyed
// copy with an explicit per-note confirm status, so a residency-relevant fact is
// never presented as authoritative when the authority did not confirm it
//.
import type { LifecycleNote } from './types'

export const LIFECYCLE_NOTES: LifecycleNote[] = [
  // Inference-profile id FORMAT confirmed: "<geo>.anthropic.<model>", geo ∈ {us,eu,apac,global}.
  { key: 'crisIdFormat', status: 'confirmed' },
  // The concrete opus Global-CRIS id is NOT verified — format confirmed, id to-confirm.
  { key: 'crisOpusId', status: 'to-confirm' },
  // Global-vs-Regional ±10% premium: no multiplier modeled; direction to-confirm.
  { key: 'globalRegionalPremium', status: 'to-confirm' },
  // US-only inference burndown 1.1×: confirmed (Anthropic service-tiers).
  { key: 'usBurndown', status: 'confirmed' },
]
