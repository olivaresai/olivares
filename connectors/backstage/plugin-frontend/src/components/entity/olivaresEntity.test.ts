// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { AgentDTO, LiveDTO } from '../../api/types';
import {
  EntityLike,
  filterSessionsForAgent,
  isOlivaresEntity,
  matchAgentToEntity,
  olivaresExternalId,
  slug,
} from './olivaresEntity';

function agent(p: Partial<AgentDTO>): AgentDTO {
  return {
    id: 'agt-id',
    tenant_id: 't',
    name: 'Payments Assistant',
    kind: 'assistant',
    status: 'active',
    created_at: 't0',
    updated_at: 't1',
    version: 1,
    ...p,
  };
}

test('isOlivaresEntity matches managed or externally-identified entities only', () => {
  assert.equal(isOlivaresEntity({ metadata: { annotations: { 'olivares.ai/managed': 'true' } } }), true);
  assert.equal(
    isOlivaresEntity({ metadata: { annotations: { 'olivares.ai/external-id': 'agt_1' } } }),
    true,
  );
  assert.equal(isOlivaresEntity({ metadata: { annotations: { other: 'x' } } }), false);
  assert.equal(isOlivaresEntity({}), false);
});

test('olivaresExternalId reads the external-id annotation', () => {
  assert.equal(
    olivaresExternalId({ metadata: { annotations: { 'olivares.ai/external-id': 'agt_9' } } }),
    'agt_9',
  );
  assert.equal(olivaresExternalId({ metadata: {} }), undefined);
});

test('slug mirrors the entity provider name slug', () => {
  assert.equal(slug('Payments Assistant'), 'payments-assistant');
  assert.equal(slug('  weird@@name  '), 'weird-name');
  assert.equal(slug(''), 'unknown');
});

test('matchAgentToEntity prefers the firm external-id key', () => {
  const entity: EntityLike = {
    metadata: { name: 'something-else', annotations: { 'olivares.ai/external-id': 'agt_8f3c2a' } },
  };
  const agents = [agent({ id: 'a1', external_id: 'agt_8f3c2a' }), agent({ id: 'a2' })];
  assert.equal(matchAgentToEntity(entity, agents)?.id, 'a1');
});

test('matchAgentToEntity falls back to the slugged name when no external id', () => {
  const entity: EntityLike = { metadata: { name: 'payments-assistant' } };
  const agents = [agent({ id: 'a1', name: 'Other' }), agent({ id: 'a2', name: 'Payments Assistant' })];
  assert.equal(matchAgentToEntity(entity, agents)?.id, 'a2');
});

test('matchAgentToEntity returns undefined when nothing matches (no guessing)', () => {
  const entity: EntityLike = { metadata: { name: 'nope' } };
  assert.equal(matchAgentToEntity(entity, [agent({ name: 'Payments Assistant' })]), undefined);
});

function session(ref: string | undefined): LiveDTO {
  return {
    session_ref: `s-${ref ?? 'none'}`,
    agent_ref: ref,
    cc_state: 'active',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 0,
    tool_call_count: 0,
    first_event_at: 't0',
    last_event_at: 't1',
    duration_seconds: 0,
  };
}

test('filterSessionsForAgent matches by external id, name or id — and never returns all', () => {
  const a = agent({ id: 'agt-id', name: 'Payments Assistant', external_id: 'agt_8f3c2a' });
  const sessions = [
    session('agt_8f3c2a'), // by external id
    session('Payments Assistant'), // by name
    session('agt-id'), // by id
    session('unrelated'),
    session(undefined),
  ];
  const matched = filterSessionsForAgent(sessions, a);
  assert.deepEqual(
    matched.map(s => s.agent_ref),
    ['agt_8f3c2a', 'Payments Assistant', 'agt-id'],
  );
});

test('filterSessionsForAgent returns empty for an agent with no usable reference', () => {
  const a = agent({ id: '', name: '', external_id: '' });
  assert.deepEqual(filterSessionsForAgent([session('x')], a), []);
});
