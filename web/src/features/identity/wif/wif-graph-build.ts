// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Pure builder: WIF objects → React-Flow node/edge arrays for the WebGL graph
// primitive (SigmaGraph). The graph is fdis_ → fdrl_ → svac_ → scope, laid out
// in layers (issuers, rules, service accounts, scopes). Read-only visualisation —
// it never creates/edits objects (writes are Console-only). Nodes carrying a lint
// finding are coloured by worst severity, so over-broad rules AND declared-vs-actual
// drift (undeclared/stale/orphan, via the 'drift' rule) stand out.
import type { Edge as RFEdge, Node as RFNode } from '@xyflow/react'
import { layeredLayout } from '@/features/shared'
import type { WifGraphData, WifLintFinding, WifLintSeverity } from '../types'
import { ruleScope } from './wif-lint'

export type WifNodeKind = 'issuer' | 'rule' | 'svac' | 'scope'

export interface WifNodeData extends Record<string, unknown> {
  kind: WifNodeKind
  label: string
  ref: string
  lint?: WifLintSeverity
}

const LAYER: Record<WifNodeKind, number> = {
  issuer: 0,
  rule: 1,
  svac: 2,
  scope: 3,
}

/** Worst lint severity per subject ref (error > warning > info). */
export function lintByRef(
  findings: WifLintFinding[],
): Map<string, WifLintSeverity> {
  const rank: Record<WifLintSeverity, number> = {
    error: 0,
    warning: 1,
    info: 2,
  }
  const out = new Map<string, WifLintSeverity>()
  for (const f of findings) {
    const prev = out.get(f.subjectRef)
    if (!prev || rank[f.severity] < rank[prev])
      out.set(f.subjectRef, f.severity)
  }
  return out
}

/** Resolve a node's colour token by lint severity, falling back to its kind. */
export function wifNodeColor(node: RFNode): string {
  const d = node.data as WifNodeData
  if (d.lint === 'error') return 'var(--color-danger)'
  if (d.lint === 'warning') return 'var(--color-warning)'
  switch (d.kind) {
    case 'issuer':
      return 'var(--color-accent-text)'
    case 'rule':
      return 'var(--color-graphite-400)'
    case 'svac':
      return 'var(--color-info)'
    default:
      return 'var(--color-muted-foreground)'
  }
}

/** Build the WIF object graph (nodes + edges) with a layered layout. */
export function buildWifGraph(
  data: WifGraphData,
  findings: WifLintFinding[] = [],
): { nodes: RFNode[]; edges: RFEdge[] } {
  const lints = lintByRef(findings)
  const nodeMap = new Map<
    string,
    { kind: WifNodeKind; label: string; ref: string }
  >()
  const add = (id: string, kind: WifNodeKind, label: string) => {
    if (!nodeMap.has(id)) nodeMap.set(id, { kind, label, ref: id })
  }

  for (const iss of data.issuers)
    add(iss.id, 'issuer', iss.issuer_url ?? iss.id)
  for (const sa of data.service_accounts) add(sa.id, 'svac', sa.name ?? sa.id)

  const edges: RFEdge[] = []
  const pushEdge = (source: string, target: string, kind: string) => {
    edges.push({
      id: `${source}->${target}`,
      source,
      target,
      data: { kind },
    })
  }

  for (const rule of data.rules) {
    add(rule.rule_id, 'rule', rule.rule_id)
    if (rule.issuer_id) {
      add(rule.issuer_id, 'issuer', rule.issuer_id)
      pushEdge(rule.issuer_id, rule.rule_id, 'issues')
    }
    if (rule.service_account_id) {
      add(
        rule.service_account_id,
        'svac',
        rule.service_account_name ?? rule.service_account_id,
      )
      pushEdge(rule.rule_id, rule.service_account_id, 'binds')
    }
    const scope = ruleScope(rule)
    const scopeId = `scope:${scope}`
    add(scopeId, 'scope', scope)
    if (rule.service_account_id)
      pushEdge(rule.service_account_id, scopeId, 'grants')
  }
  // Service accounts that carry a scope but no rule still show their scope edge.
  for (const sa of data.service_accounts) {
    if (sa.oauth_scope) {
      const scopeId = `scope:${sa.oauth_scope}`
      add(scopeId, 'scope', sa.oauth_scope)
      pushEdge(sa.id, scopeId, 'grants')
    }
  }

  const layoutNodes = [...nodeMap.entries()].map(([id, n]) => ({
    id,
    layer: LAYER[n.kind],
  }))
  const { positions } = layeredLayout(
    layoutNodes,
    edges.map((e) => ({ source: e.source, target: e.target })),
  )

  const nodes: RFNode[] = [...nodeMap.entries()].map(([id, n]) => ({
    id,
    position: positions[id] ?? { x: LAYER[n.kind] * 320, y: 0 },
    data: {
      kind: n.kind,
      label: n.label,
      ref: n.ref,
      lint: lints.get(id),
    } as WifNodeData,
  }))

  return { nodes, edges }
}
