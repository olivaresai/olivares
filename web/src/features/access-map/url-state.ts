// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ADDRESS OF AN ACCESS-MAP VIEW.
//
// Selection, filters, the drift overlay and the expanded neighbors are the view, so
// they live in the URL: a pasted link restores the same map, Back leaves it, and a
// reload keeps it. The keys, read by `useValidatedUrlState` in access-map-view.tsx:
//
//   q       free-text filter            (legacy `focus` seeds it: the identity roster
//                                        links here with ?focus=<ref>)
//   mode    comma list of read|write|readwrite|unknown
//   conf    `attributed` = only firm edges
//   source  one signal source
//   drift   `1` = drift overlay on
//   node    the selected node id        (`edge` wins when both are present)
//   edge    the selected edge id
//   expand  expanded nodes, `kind:id` entries joined by commas (id percent-encoded)
//
// A URL is untrusted input. The decoder is pure, never throws, and names every key it
// refused so the view can say so; a refused key falls back to its default.
import type { UrlState, UrlStateDecoded } from '@/lib/hooks/use-url-state'
import { type AccessFilterState, emptyFilter } from './graph-model'

export const ACCESS_URL_KEYS = [
  'q',
  'focus',
  'mode',
  'conf',
  'source',
  'drift',
  'node',
  'edge',
  'expand',
] as const

const MODES: readonly string[] = ['read', 'write', 'readwrite', 'unknown']
/** A ceiling on any one value: ids and refs here are UUIDs and short names. */
export const MAX_VALUE_LENGTH = 200
/** Every expanded node is one privileged, audited read fired by whoever opens the link. */
export const MAX_EXPANDED = 10
const KIND = /^[A-Za-z0-9._-]{1,64}$/

export interface ExpandedNode {
  id: string
  kind: string
}

export interface AccessUrlView {
  filter: AccessFilterState
  overlay: boolean
  node: string | null
  edge: string | null
  expanded: ExpandedNode[]
}

function bounded(v: string | undefined): v is string {
  return v !== undefined && v.length > 0 && v.length <= MAX_VALUE_LENGTH
}

function decodeExpanded(raw: string): ExpandedNode[] | null {
  const entries = raw.split(',')
  if (entries.length > MAX_EXPANDED) return null
  const out: ExpandedNode[] = []
  for (const entry of entries) {
    const cut = entry.indexOf(':')
    const kind = entry.slice(0, cut)
    if (cut < 0 || !KIND.test(kind)) return null
    let id: string
    try {
      id = decodeURIComponent(entry.slice(cut + 1))
    } catch {
      return null
    }
    if (!bounded(id)) return null
    out.push({ id, kind })
  }
  return out
}

export function decodeAccessUrl(raw: UrlState): UrlStateDecoded<AccessUrlView> {
  const issues: string[] = []
  const filter = emptyFilter()

  const q = raw.q ?? raw.focus
  if (q !== undefined) {
    if (bounded(q)) filter.search = q
    else issues.push(raw.q !== undefined ? 'q' : 'focus')
  }

  if (raw.mode !== undefined) {
    const modes = raw.mode.split(',')
    if (modes.every((m) => MODES.includes(m))) filter.modes = new Set(modes)
    else issues.push('mode')
  }

  if (raw.conf !== undefined) {
    if (raw.conf === 'attributed') filter.confidence = 'attributed'
    else issues.push('conf')
  }

  if (raw.source !== undefined) {
    if (bounded(raw.source)) filter.signalSource = raw.source
    else issues.push('source')
  }

  let overlay = false
  if (raw.drift !== undefined) {
    if (raw.drift === '1') overlay = true
    else issues.push('drift')
  }

  let edge: string | null = null
  if (raw.edge !== undefined) {
    if (bounded(raw.edge)) edge = raw.edge
    else issues.push('edge')
  }
  let node: string | null = null
  if (raw.node !== undefined) {
    if (bounded(raw.node) && edge === null) node = raw.node
    else issues.push('node')
  }

  let expanded: ExpandedNode[] = []
  if (raw.expand !== undefined) {
    const parsed = decodeExpanded(raw.expand)
    if (parsed) expanded = parsed
    else issues.push('expand')
  }

  return { value: { filter, overlay, node, edge, expanded }, issues }
}

/** The patch that makes the URL say `filter`. `focus` is cleared: `q` replaces it. */
export function filterPatch(filter: AccessFilterState): UrlState {
  return {
    q: filter.search || undefined,
    focus: undefined,
    mode:
      filter.modes.size > 0 ? [...filter.modes].sort().join(',') : undefined,
    conf: filter.confidence === 'attributed' ? 'attributed' : undefined,
    source: filter.signalSource ?? undefined,
  }
}

export function expandedValue(expanded: readonly ExpandedNode[]) {
  return expanded.length > 0
    ? expanded.map((e) => `${e.kind}:${encodeURIComponent(e.id)}`).join(',')
    : undefined
}
