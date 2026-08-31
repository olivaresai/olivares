// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The intelligence-layer shared kit. Importing this barrel registers the
// shared `intel` i18n namespace as a side effect, so every domain badge / notice /
// timeline has its strings the moment a view renders. Each of the five views also
// registers its OWN feature namespace; this one is the common vocabulary.
// The registration itself now lives in ./i18n (the shape every feature uses) so the
// modules below can register it WITHOUT importing this barrel — a deep import like
// `@/features/_intel/notices` gets the strings too.
import './i18n'

export {
  SeverityBadge,
  OutcomeBadge,
  VerdictBadge,
  IntegrityBadge,
  ConsentBadge,
  RiskTierBadge,
  ControlStatusBadge,
  GateBadge,
} from './badges'
export { HashChip } from './hash-chip'
export { MetricStat, StatGrid, type MetricStatProps } from './metric'
export {
  IntelNotice,
  ListTruncationBadge,
  TruncatedNotice,
  SelfAuditNotice,
  CaveatNotice,
  DisclaimerNote,
  SeamBadge,
  listaRecortada,
} from './notices'
export { ConsumptionBar } from './consumption'
export { IntelPage, SectionCard } from './intel-page'
export { EvidenceTimeline, type TimelineEvent } from './evidence-timeline'
export { AsyncSection } from './async'
export {
  EffectiveStateLinks,
  type EffectiveStateTarget,
} from './effective-state'
export { DeclaredSection, ContractPendingNotice } from './declared'
