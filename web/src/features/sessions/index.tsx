// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Sessions (module II, observe + operate) — ONE
// surface for a session however it reached the plane: the realtime observed stream
// AND the runs Olivares launched, in one list, opening one card that states its
// PROVENANCE (discovered or launched) and its CONTROL LEVEL.
// Importing this module registers its i18n namespace as a side effect.
import './i18n'

export { SessionsWorkspaceView } from './sessions-workspace-view'
export { SessionCard, type SessionTarget } from './session-card'
