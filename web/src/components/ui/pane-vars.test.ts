// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it } from 'vitest'
import { renderHook } from '@testing-library/react'
import { BESIDE_PANE, LIST_PANE } from '@/features/sessions/surface-resize'
import { usePaneWidthsStore } from '@/stores/pane-widths'
import { MAX_PANE_PX, upTo, usePaneVars } from './pane-vars'

describe('usePaneVars', () => {
  beforeEach(() => {
    usePaneWidthsStore.setState({
      owner: null,
      loaded: false,
      widths: {},
      touched: new Set(),
      persist: null,
    })
  })

  it('hands a pane the width stored for it', () => {
    usePaneWidthsStore.setState({ widths: { 'sessions-list': 420 } })
    const { result } = renderHook(() => usePaneVars(LIST_PANE))
    expect(result.current).toEqual({ '--list-w': '420px' })
  })

  it("never narrows a pane below its own minimum, whatever the engine holds (an API write, another client's limit)", () => {
    usePaneWidthsStore.setState({ widths: { 'sessions-list': 1 } })
    const { result } = renderHook(() => usePaneVars(LIST_PANE))
    expect(result.current).toEqual({ '--list-w': '240px' })
  })
})

describe('upTo', () => {
  it('keeps its own limits on a wide surface', () => {
    expect(upTo(240, 560, 0.35, 2000)).toEqual({ min: 240, max: 560 })
  })

  it('stops at the share of the surface', () => {
    expect(upTo(240, 560, 0.35, 1000)).toEqual({ min: 240, max: 350 })
  })

  it('lets the share win over the minimum on a surface too narrow for it, as the CSS does', () => {
    expect(upTo(240, 560, 0.35, 600)).toEqual({ min: 210, max: 210 })
  })

  it('never goes past what the engine stores', () => {
    expect(upTo(240, 9000, 0.5, 20000).max).toBe(MAX_PANE_PX)
  })

  it('uses its own limits before the surface is measured', () => {
    expect(upTo(240, 560, 0.35, 0)).toEqual({ min: 240, max: 560 })
  })
})

describe('BESIDE_PANE', () => {
  it('splits 30% to 70% of the thread area', () => {
    expect(BESIDE_PANE.bounds(1000)).toEqual({ min: 300, max: 700 })
  })

  it('never goes past what the engine stores', () => {
    expect(BESIDE_PANE.bounds(20000).max).toBe(MAX_PANE_PX)
  })
})
