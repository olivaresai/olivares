// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The work cockpit — the console surface over the K1 durable cross-session work
// kernel (the cross-session work-kernel contract). Importing this module
// registers its i18n namespace as a side effect.
import './i18n'

export { WorkView } from './work-view'
export { DecisionsPanel } from './decisions-panel'
export { ItemDetailSheet } from './item-detail'
export { ApplyFlow } from './apply-flow'
export { VerdictBadge, UnavailableNotice, ChecksList } from './verdict'
export { StatusBadge } from './status-badge'
export { useWorkStream } from './stream'
