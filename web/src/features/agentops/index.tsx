// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Claude Code OPERATE portal (FASE V) — the SSH-free surface to create, attach
// to, govern and tear down operated Claude Code sessions (runtime), browse their
// governed workspaces, and see the per-session governance posture.
// Importing this module registers its i18n namespace as a side effect.
import './i18n'

//the operate portal no longer has a view of its own — `/agentops` and
// `/sessions` mount features/sessions/sessions-workspace-view.tsx, which composes the
// panels below. What this module still owns is those panels: the launch dialog, the
// governed workspace plane, the attach console and the per-run governance posture.
export { RunCreateDialog } from './run-create-dialog'
export { WorkspacesPanel } from './workspaces-panel'
export { GovernancePanel } from './governance-panel'
export { LiveConsole } from './live-console'
