// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it } from 'vitest'
import { useWorkspaceStore } from './workspace'

beforeEach(() => {
  localStorage.clear()
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
})

describe('workspace store', () => {
  it('defaults to null (all workspaces)', () => {
    const s = useWorkspaceStore.getState()
    expect(s.activeWorkspace).toBeNull()
    expect(s.activeWorkspaceName).toBeNull()
  })

  it('setActiveWorkspace stores id and name', () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-123', 'Engineering')
    const s = useWorkspaceStore.getState()
    expect(s.activeWorkspace).toBe('ws-123')
    expect(s.activeWorkspaceName).toBe('Engineering')
  })

  it('setActiveWorkspace with null clears the selection', () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-123', 'Eng')
    useWorkspaceStore.getState().setActiveWorkspace(null)
    const s = useWorkspaceStore.getState()
    expect(s.activeWorkspace).toBeNull()
    expect(s.activeWorkspaceName).toBeNull()
  })

  it('clear resets the workspace selection', () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-456', 'Sales')
    useWorkspaceStore.getState().clear()
    const s = useWorkspaceStore.getState()
    expect(s.activeWorkspace).toBeNull()
    expect(s.activeWorkspaceName).toBeNull()
  })

  it('persists to localStorage under olivares.workspace', () => {
    useWorkspaceStore.getState().setActiveWorkspace('ws-789', 'Ops')
    const stored = localStorage.getItem('olivares.workspace')
    expect(stored).toBeTruthy()
    const parsed = JSON.parse(stored!)
    expect(parsed.state.activeWorkspace).toBe('ws-789')
    expect(parsed.state.activeWorkspaceName).toBe('Ops')
  })
})
