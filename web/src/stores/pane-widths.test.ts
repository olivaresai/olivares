// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { usePaneWidthsStore } from './pane-widths'

const store = () => usePaneWidthsStore.getState()

beforeEach(() => {
  usePaneWidthsStore.setState({
    owner: null,
    loaded: false,
    widths: {},
    touched: new Set(),
    persist: null,
  })
})

describe('pane widths store', () => {
  it('starts every pane without a width, so each keeps its own default', () => {
    store().attach('t/u/1', null)
    expect(store().widths).toEqual({})
  })

  it("restores the engine's widths for the identity that read them", () => {
    store().attach('t/u/1', null)
    store().hydrate('t/u/1', { 'sessions-list': 420, 'sessions-context': 360 })
    expect(store().widths).toEqual({
      'sessions-list': 420,
      'sessions-context': 360,
    })
    expect(store().loaded).toBe(true)
  })

  it("ignores a read that arrives for an identity that is no longer the store's", () => {
    store().attach('t/new/1', null)
    store().hydrate('t/old/1', { 'sessions-list': 420 })
    expect(store().widths).toEqual({})
    expect(store().loaded).toBe(false)
  })

  it('does not let a late read undo a width the person already set', () => {
    store().attach('t/u/1', null)
    store().commit('sessions-list', 500)
    store().reset('sessions-context')
    store().hydrate('t/u/1', {
      'sessions-list': 420,
      'sessions-context': 360,
      'sessions-beside': 700,
    })
    expect(store().widths).toEqual({
      'sessions-list': 500,
      'sessions-beside': 700,
    })
  })

  it('reads the engine once per identity, so a later answer cannot undo a drag', () => {
    store().attach('t/u/1', null)
    store().hydrate('t/u/1', { 'sessions-list': 420 })
    store().commit('sessions-list', 480)
    store().hydrate('t/u/1', { 'sessions-list': 420 })
    expect(store().widths['sessions-list']).toBe(480)
  })

  it('starts empty for another identity so one person never sees the last one’s widths', () => {
    store().attach('t/alice/1', null)
    store().hydrate('t/alice/1', { 'sessions-list': 420 })
    store().attach('t/bob/1', null)
    expect(store().widths).toEqual({})
    expect(store().loaded).toBe(false)
  })

  it('keeps the widths when only the way to save changes', () => {
    store().attach('t/u/1', null)
    store().commit('sessions-list', 400)
    store().attach('t/u/1', vi.fn())
    expect(store().widths['sessions-list']).toBe(400)
  })

  it('saves a settled width as whole pixels and a reset as 0', () => {
    const persist = vi.fn()
    store().attach('t/u/1', persist)
    store().commit('sessions-list', 400.6)
    expect(persist).toHaveBeenLastCalledWith({ 'sessions-list': 401 })
    store().reset('sessions-list')
    expect(persist).toHaveBeenLastCalledWith({ 'sessions-list': 0 })
    expect(store().widths).toEqual({})
  })

  it('takes a width under one pixel, or none, for no width at all, as the engine reads 0 as a reset', () => {
    const persist = vi.fn()
    store().attach('t/u/1', persist)
    store().commit('sessions-list', 400)
    persist.mockClear()
    store().commit('sessions-list', 0)
    store().commit('sessions-list', 0.4)
    store().commit('sessions-list', Number.NaN)
    expect(store().widths['sessions-list']).toBe(400)
    expect(persist).not.toHaveBeenCalled()
  })

  it('still applies a width when there is nowhere to save it', () => {
    store().attach('t/u/1', null)
    store().commit('sessions-list', 400)
    expect(store().widths['sessions-list']).toBe(400)
  })
})
