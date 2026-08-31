// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it } from 'vitest'
import { useWorkspaceStore } from '@/stores/workspace'

beforeEach(() => {
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
})

describe('workspace filter logic', () => {
  it('returns undefined workspaceId when no workspace selected', () => {
    const ws = useWorkspaceStore.getState().activeWorkspace
    const workspaceId = ws ?? undefined
    const queryKey = ws ?? '__all__'
    expect(workspaceId).toBeUndefined()
    expect(queryKey).toBe('__all__')
  })

  it('returns the workspace id when one is selected', () => {
    useWorkspaceStore.setState({ activeWorkspace: 'ws-abc' })
    const ws = useWorkspaceStore.getState().activeWorkspace
    const workspaceId = ws ?? undefined
    const queryKey = ws ?? '__all__'
    expect(workspaceId).toBe('ws-abc')
    expect(queryKey).toBe('ws-abc')
  })
})
