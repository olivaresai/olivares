// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
export {
  CommunicationsView,
  type CommunicationsEntrance,
} from './communications-view'
// The work cockpit mounts this host beside its item sheet. It is the ONLY thing this
// feature exposes to another feature, and the direction is one-way on purpose: work
// may import communications, communications never imports work.
export {
  HandoffOfferHost,
  type HandoffOfferTarget,
  type HandoffWorkItemReader,
  type HandoffWorkItemView,
} from './handoff-offer-host'
