// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Pure presentation + aggregation helpers shared by the plugin's components. They
// hold the plugin's only real logic (formatting, status mapping, drift/graph
// summarization), so the React components stay thin renderers and this is where
// the unit tests bite. Every honesty rule from the web access-map/sessions
// contracts lives here: never render `approximate`/`unknown` attribution as firm,
// never show `reconciliation_pending` drift as a firm (red) violation, and never
// fabricate a goal/cost the engine did not send.

import type {
  AccessEdge,
  CcState,
  DiffResponse,
  DriftEntry,
  GraphResponse,
} from './types';

/** A semantic tone the components map onto Backstage Status* / theme colors. */
export type Tone = 'ok' | 'warning' | 'error' | 'pending' | 'default';

// --- access mode --------------------------------------------------------------

/** True for the risk-bearing modes that deserve prominence. */
export function isWriteMode(mode: string): boolean {
  return mode === 'readwrite' || mode === 'write';
}

/** Short, stable token for an edge chip/legend ("R" / "RW" / "W" / "?"). */
export function modeToken(mode: string): 'R' | 'RW' | 'W' | '?' {
  if (mode === 'read') return 'R';
  if (mode === 'readwrite') return 'RW';
  if (mode === 'write') return 'W';
  return '?';
}

// --- session state ------------------------------------------------------------

/**
 * ccStateMeta maps the engine-derived session state to a label + tone. The state
 * is the engine's signal, never inferred here. `silent_evasion` is surfaced as an
 * error tone because a silence inside the expected cadence is exactly what an
 * operator should look at (docs/SECURITY-HARDENING.md) — it is not a UI fault.
 */
export function ccStateMeta(state: CcState): { label: string; tone: Tone } {
  switch (state) {
    case 'active':
      return { label: 'Active', tone: 'ok' };
    case 'idle':
      return { label: 'Idle', tone: 'pending' };
    case 'ended':
      return { label: 'Ended', tone: 'default' };
    case 'silent_evasion':
      return { label: 'Silent (evasion?)', tone: 'error' };
    default:
      return { label: state || 'unknown', tone: 'default' };
  }
}

// --- attribution / confidence (honesty rules) ---------------------------------

/**
 * attributionMeta renders the per-edge attribution firmness. `firm` is the only
 * firm tier; `approximate` and `unknown` MUST be visually de-emphasized so the UI
 * never overstates how firmly an origin ties to a concrete agent/NHI (G8). `firm` is the contract here — the caller uses it to decide emphasis.
*/
export function attributionMeta(tier?: string): {
  label: string;
  firm: boolean;
  tone: Tone;
} {
  switch (tier) {
    case 'firm':
      return { label: 'Firm', firm: true, tone: 'ok' };
    case 'approximate':
      return { label: 'Approximate', firm: false, tone: 'warning' };
    case 'unknown':
      return { label: 'Unknown', firm: false, tone: 'pending' };
    default:
      // No attribution tier on the edge: treat as not-firm, neutral.
      return { label: tier ? tier : '—', firm: false, tone: 'default' };
  }
}

/** Confidence is firm only when `attributed`; `approximate` is inferred (dotted). */
export function confidenceIsFirm(confidence: string): boolean {
  return confidence === 'attributed';
}

// --- formatting ---------------------------------------------------------------

/**
 * formatMicroUsd renders a micro-USD integer (1e-6 USD) as a dollar string. Small
 * costs keep precision (an agent run can be fractions of a cent) and larger costs
 * round to cents. 0 renders as "$0.00", never blank.
 */
export function formatMicroUsd(micro: number): string {
  if (!Number.isFinite(micro) || micro === 0) {
    return '$0.00';
  }
  const dollars = micro / 1_000_000;
  const abs = Math.abs(dollars);
  if (abs >= 0.01) {
    return `$${dollars.toFixed(2)}`;
  }
  // Sub-cent: show up to 6 decimals, trimming trailing zeros (but keep one digit).
  const trimmed = dollars.toFixed(6).replace(/0+$/, '').replace(/\.$/, '.0');
  return `$${trimmed}`;
}

/** formatTokens renders a token count compactly (1234 → "1.2k", 2_000_000 → "2M"). */
export function formatTokens(n: number): string {
  if (!Number.isFinite(n)) return '0';
  const abs = Math.abs(n);
  if (abs < 1_000) return String(n);
  if (abs < 1_000_000) return `${trim(n / 1_000)}k`;
  return `${trim(n / 1_000_000)}M`;
}

function trim(x: number): string {
  return x.toFixed(1).replace(/\.0$/, '');
}

/**
 * formatDuration renders whole seconds as a compact, at-most-two-unit duration
 * ("0s", "45s", "1m 5s", "1h 2m", "2d 3h"). Negative/NaN clamps to "0s".
 */
export function formatDuration(totalSeconds: number): string {
  if (!Number.isFinite(totalSeconds) || totalSeconds <= 0) {
    return '0s';
  }
  const s = Math.floor(totalSeconds);
  const days = Math.floor(s / 86_400);
  const hours = Math.floor((s % 86_400) / 3_600);
  const mins = Math.floor((s % 3_600) / 60);
  const secs = s % 60;
  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0) return `${hours}h ${mins}m`;
  if (mins > 0) return `${mins}m ${secs}s`;
  return `${secs}s`;
}

/** A stable, human label for a discovery entity kind (inventory summary + table). */
export function kindLabel(kind: string): string {
  const map: Record<string, string> = {
    agent: 'Agents',
    mcp_server: 'MCP servers',
    tool: 'Tools',
    session: 'Sessions',
    identity: 'Identities',
    resource: 'Resources',
    skill: 'Skills',
    model: 'Models',
    provider: 'Providers',
  };
  return map[kind] ?? kind;
}

// --- access-map aggregation ---------------------------------------------------

/** A high-level tally of the R/RW graph for the access-map header strip. */
export interface GraphSummary {
  nodeCount: number;
  edgeCount: number;
  writeEdgeCount: number;
  observedCount: number;
  permittedCount: number;
}

/** summarizeGraph counts nodes/edges and the risk-bearing (write) + diff facets. */
export function summarizeGraph(graph: GraphResponse): GraphSummary {
  let writeEdgeCount = 0;
  let observedCount = 0;
  let permittedCount = 0;
  for (const e of graph.edges) {
    if (isWriteMode(e.mode)) writeEdgeCount += 1;
    if (e.observed) observedCount += 1;
    if (e.permitted) permittedCount += 1;
  }
  return {
    nodeCount: graph.nodes.length,
    edgeCount: graph.edges.length,
    writeEdgeCount,
    observedCount,
    permittedCount,
  };
}

/** An origin node with the edges leaving it — the "map" grouping for the UI. */
export interface OriginGroup {
  originId: string;
  originKind: string;
  originRef?: string;
  edges: AccessEdge[];
  hasWrite: boolean;
}

/**
 * groupEdgesByOrigin clusters edges by their origin (agent/identity/session),
 * preserving first-seen order, so the access map renders as origin → resources
 * groups rather than a flat list. `hasWrite` lets the UI flag a risky origin.
 */
export function groupEdgesByOrigin(edges: AccessEdge[]): OriginGroup[] {
  const order: string[] = [];
  const byId = new Map<string, OriginGroup>();
  for (const e of edges) {
    let g = byId.get(e.origin_id);
    if (!g) {
      g = {
        originId: e.origin_id,
        originKind: e.origin_kind,
        originRef: e.origin_ref,
        edges: [],
        hasWrite: false,
      };
      byId.set(e.origin_id, g);
      order.push(e.origin_id);
    }
    g.edges.push(e);
    if (isWriteMode(e.mode)) {
      g.hasWrite = true;
    }
  }
  return order.map(id => byId.get(id)!);
}

// --- drift aggregation (honesty rules) ----------------------------------------

/** A drift tally that keeps `pending` separate from firm findings. */
export interface DriftSummary {
  unexpected: number;
  unused: number;
  /** Unexpected accesses that are reconciliation-pending (amber, not a violation). */
  pending: number;
  /** Firm unexpected accesses = unexpected − pending (the red headline). */
  firmUnexpected: number;
}

/**
 * summarizeDrift tallies the permitted-vs-observed diff, splitting out the
 * reconciliation-pending unexpected accesses so the UI can headline only the
 * FIRM violations in red and show pending ones amber — honest uncertainty, not a
 * fabricated finding (UI-CONTRACT-ACCESS-MAP).
 */
export function summarizeDrift(diff: DiffResponse): DriftSummary {
  const pending = diff.unexpected_accesses.filter(d => d.reconciliation_pending).length;
  const unexpected = diff.unexpected_count;
  return {
    unexpected,
    unused: diff.unused_count,
    pending,
    firmUnexpected: Math.max(0, unexpected - pending),
  };
}

/**
 * driftEntryTone maps one drift entry to a tone: pending → amber, an unexpected
 * access → error (the security headline), an unused grant → warning. Pending wins
 * over kind so a not-yet-decided access is never shown as a firm violation.
 */
export function driftEntryTone(entry: DriftEntry): Tone {
  if (entry.reconciliation_pending) {
    return 'pending';
  }
  return entry.kind === 'unexpected_access' ? 'error' : 'warning';
}

/** A short, human label for a drift entry (with the honest "pending" qualifier). */
export function driftEntryLabel(entry: DriftEntry): string {
  if (entry.kind === 'unexpected_access') {
    return entry.reconciliation_pending ? 'Unexpected (pending)' : 'Unexpected access';
  }
  if (entry.kind === 'unused_grant') {
    return 'Unused grant';
  }
  return entry.kind;
}
