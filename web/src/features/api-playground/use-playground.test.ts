// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { ParsedEndpoint } from './openapi-parser'
import { usePlayground } from './use-playground'

const endpoint = {
  method: 'GET',
  path: '/v1/agents',
  secured: true,
  parameters: [],
} as unknown as ParsedEndpoint

describe('usePlayground request generation', () => {
  it('switching endpoint invalidates the in-flight generation and clears flags', () => {
    const st = usePlayground.getState()
    const gen = st.beginRequest()
    st.setLoading(true)
    st.setStreaming(true)

    st.selectEndpoint(endpoint)

    const after = usePlayground.getState()
    // The aborted request's guard (`requestGeneration === gen`) must now fail…
    expect(after.requestGeneration).not.toBe(gen)
    // …and the new endpoint starts with clean loading/streaming state.
    expect(after.isLoading).toBe(false)
    expect(after.isStreaming).toBe(false)
    expect(after.response).toBeNull()
  })

  it('each beginRequest supersedes the previous one', () => {
    const first = usePlayground.getState().beginRequest()
    const second = usePlayground.getState().beginRequest()
    expect(second).toBeGreaterThan(first)
    expect(usePlayground.getState().requestGeneration).toBe(second)
  })
})
