// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AccessEdge, DiffResponse, GraphResponse } from './types';
import {
  attributionMeta,
  ccStateMeta,
  confidenceIsFirm,
  driftEntryLabel,
  driftEntryTone,
  formatDuration,
  formatMicroUsd,
  formatTokens,
  groupEdgesByOrigin,
  isWriteMode,
  kindLabel,
  modeToken,
  summarizeDrift,
  summarizeGraph,
} from './transform';

test('modeToken / isWriteMode classify R/RW/W/unknown', () => {
  assert.equal(modeToken('read'), 'R');
  assert.equal(modeToken('readwrite'), 'RW');
  assert.equal(modeToken('write'), 'W');
  assert.equal(modeToken('whatever'), '?');
  assert.equal(isWriteMode('read'), false);
  assert.equal(isWriteMode('readwrite'), true);
  assert.equal(isWriteMode('write'), true);
});

test('ccStateMeta surfaces silent_evasion as an error tone (worth the eye)', () => {
  assert.deepEqual(ccStateMeta('active'), { label: 'Active', tone: 'ok' });
  assert.deepEqual(ccStateMeta('idle'), { label: 'Idle', tone: 'pending' });
  assert.equal(ccStateMeta('silent_evasion').tone, 'error');
  assert.equal(ccStateMeta('ended').tone, 'default');
});

test('attributionMeta never marks approximate/unknown as firm', () => {
  assert.equal(attributionMeta('firm').firm, true);
  assert.equal(attributionMeta('approximate').firm, false);
  assert.equal(attributionMeta('unknown').firm, false);
  assert.equal(attributionMeta(undefined).firm, false);
  assert.equal(confidenceIsFirm('attributed'), true);
  assert.equal(confidenceIsFirm('approximate'), false);
});

test('formatMicroUsd keeps sub-cent precision and rounds larger to cents', () => {
  assert.equal(formatMicroUsd(0), '$0.00');
  assert.equal(formatMicroUsd(1_230_000), '$1.23');
  assert.equal(formatMicroUsd(50_000), '$0.05');
  assert.equal(formatMicroUsd(500), '$0.0005');
});

test('formatTokens renders compact magnitudes', () => {
  assert.equal(formatTokens(999), '999');
  assert.equal(formatTokens(1_200), '1.2k');
  assert.equal(formatTokens(2_000_000), '2M');
});

test('formatDuration renders at most two units and clamps non-positive', () => {
  assert.equal(formatDuration(0), '0s');
  assert.equal(formatDuration(-5), '0s');
  assert.equal(formatDuration(45), '45s');
  assert.equal(formatDuration(65), '1m 5s');
  assert.equal(formatDuration(3_720), '1h 2m');
  assert.equal(formatDuration(183_600), '2d 3h');
});

test('kindLabel humanizes known kinds and passes through unknowns', () => {
  assert.equal(kindLabel('mcp_server'), 'MCP servers');
  assert.equal(kindLabel('agent'), 'Agents');
  assert.equal(kindLabel('widget'), 'widget');
});

function edge(partial: Partial<AccessEdge>): AccessEdge {
  return {
    id: 'e',
    origin_kind: 'agent',
    origin_id: 'a1',
    resource_id: 'r1',
    mode: 'read',
    signal_source: 'otel',
    confidence: 'attributed',
    bridged: true,
    observed: true,
    permitted: true,
    occurrence_count: 1,
    first_seen: 't0',
    last_seen: 't1',
    ...partial,
  };
}

test('summarizeGraph counts nodes, edges and the write/observed/permitted facets', () => {
  const graph: GraphResponse = {
    nodes: [
      { id: 'a1', kind: 'agent' },
      { id: 'r1', kind: 'resource' },
      { id: 'r2', kind: 'resource' },
    ],
    edges: [
      edge({ id: 'e1', resource_id: 'r1', mode: 'read', observed: true, permitted: true }),
      edge({ id: 'e2', resource_id: 'r2', mode: 'readwrite', observed: true, permitted: false }),
    ],
    has_more: false,
  };
  assert.deepEqual(summarizeGraph(graph), {
    nodeCount: 3,
    edgeCount: 2,
    writeEdgeCount: 1,
    observedCount: 2,
    permittedCount: 1,
  });
});

test('groupEdgesByOrigin clusters by origin and flags write access, preserving order', () => {
  const groups = groupEdgesByOrigin([
    edge({ id: 'e1', origin_id: 'a1', resource_id: 'r1', mode: 'read' }),
    edge({ id: 'e2', origin_id: 'a2', resource_id: 'r2', mode: 'readwrite' }),
    edge({ id: 'e3', origin_id: 'a1', resource_id: 'r3', mode: 'write' }),
  ]);
  assert.equal(groups.length, 2);
  assert.equal(groups[0].originId, 'a1');
  assert.equal(groups[0].edges.length, 2);
  assert.equal(groups[0].hasWrite, true); // e3 is a write
  assert.equal(groups[1].originId, 'a2');
  assert.equal(groups[1].hasWrite, true);
});

test('summarizeDrift splits reconciliation-pending out of the firm headline', () => {
  const diff: DiffResponse = {
    unexpected_accesses: [
      { kind: 'unexpected_access', edge: edge({ id: 'u1' }) },
      { kind: 'unexpected_access', reconciliation_pending: true, edge: edge({ id: 'u2' }) },
    ],
    unused_grants: [{ kind: 'unused_grant', edge: edge({ id: 'g1', permitted: true, observed: false }) }],
    unexpected_count: 2,
    unused_count: 1,
  };
  assert.deepEqual(summarizeDrift(diff), {
    unexpected: 2,
    unused: 1,
    pending: 1,
    firmUnexpected: 1,
  });
});

test('driftEntryTone shows pending as amber, never as a firm violation', () => {
  assert.equal(
    driftEntryTone({ kind: 'unexpected_access', reconciliation_pending: true, edge: edge({}) }),
    'pending',
  );
  assert.equal(driftEntryTone({ kind: 'unexpected_access', edge: edge({}) }), 'error');
  assert.equal(driftEntryTone({ kind: 'unused_grant', edge: edge({}) }), 'warning');
});

test('driftEntryLabel qualifies a pending unexpected access honestly', () => {
  assert.equal(
    driftEntryLabel({ kind: 'unexpected_access', reconciliation_pending: true, edge: edge({}) }),
    'Unexpected (pending)',
  );
  assert.equal(driftEntryLabel({ kind: 'unexpected_access', edge: edge({}) }), 'Unexpected access');
  assert.equal(driftEntryLabel({ kind: 'unused_grant', edge: edge({}) }), 'Unused grant');
});
