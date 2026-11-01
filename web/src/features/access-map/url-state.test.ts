// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { emptyFilter } from './graph-model'
import {
  decodeAccessUrl,
  expandedValue,
  filterPatch,
  MAX_EXPANDED,
} from './url-state'

describe('decodeAccessUrl', () => {
  it('is the pristine view for an empty address', () => {
    const { value, issues } = decodeAccessUrl({})
    expect(issues).toEqual([])
    expect(value.filter).toEqual(emptyFilter())
    expect(value).toMatchObject({
      overlay: false,
      node: null,
      edge: null,
      expanded: [],
    })
  })

  it('reads the legacy ?focus= link as the search, and q wins over it', () => {
    expect(decodeAccessUrl({ focus: 'agent-1' }).value.filter.search).toBe(
      'agent-1',
    )
    expect(
      decodeAccessUrl({ focus: 'agent-1', q: 'db' }).value.filter.search,
    ).toBe('db')
  })

  it('restores every filter, the overlay and the selection', () => {
    const { value, issues } = decodeAccessUrl({
      q: 'secrets',
      mode: 'write,readwrite',
      conf: 'attributed',
      source: 'pgaudit',
      drift: '1',
      node: 'R2',
    })
    expect(issues).toEqual([])
    expect(value.filter).toEqual({
      search: 'secrets',
      modes: new Set(['write', 'readwrite']),
      confidence: 'attributed',
      signalSource: 'pgaudit',
    })
    expect(value.overlay).toBe(true)
    expect(value.node).toBe('R2')
  })

  it('names and drops what it cannot use instead of guessing', () => {
    const { value, issues } = decodeAccessUrl({
      mode: 'read,root',
      conf: 'maybe',
      drift: 'yes',
      q: 'x'.repeat(201),
    })
    expect(issues.sort()).toEqual(['conf', 'drift', 'mode', 'q'])
    expect(value.filter).toEqual(emptyFilter())
    expect(value.overlay).toBe(false)
  })

  it('keeps the edge when both an edge and a node are named', () => {
    const { value, issues } = decodeAccessUrl({ node: 'A1', edge: 'e1' })
    expect(value).toMatchObject({ node: null, edge: 'e1' })
    expect(issues).toEqual(['node'])
  })

  it('round-trips expanded nodes whose ids carry separators', () => {
    const expanded = [
      { id: 'A1', kind: 'agent' },
      { id: 'a,b:c/d', kind: 'postgres.table' },
    ]
    const { value, issues } = decodeAccessUrl({
      expand: expandedValue(expanded),
    })
    expect(issues).toEqual([])
    expect(value.expanded).toEqual(expanded)
  })

  it('refuses a malformed or oversized expand list whole', () => {
    for (const expand of [
      'agent',
      ':A1',
      'bad kind:A1',
      'agent:%E0%A4%A',
      Array.from({ length: MAX_EXPANDED + 1 }, (_, i) => `agent:${i}`).join(
        ',',
      ),
    ]) {
      const { value, issues } = decodeAccessUrl({ expand })
      expect(issues).toEqual(['expand'])
      expect(value.expanded).toEqual([])
    }
  })
})

describe('filterPatch', () => {
  it('writes the canonical form decodeAccessUrl reads back, and clears focus', () => {
    const filter = {
      search: 'secrets',
      modes: new Set(['write', 'read']),
      confidence: 'attributed' as const,
      signalSource: 'pgaudit',
    }
    const patch = filterPatch(filter)
    expect(patch).toMatchObject({
      q: 'secrets',
      mode: 'read,write',
      focus: undefined,
    })
    expect(decodeAccessUrl(patch).value.filter).toEqual(filter)
  })

  it('leaves a pristine filter with an empty address', () => {
    expect(
      Object.values(filterPatch(emptyFilter())).every((v) => v === undefined),
    ).toBe(true)
  })
})
