// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  conversationCwd,
  mapConversationFrames,
  type ConversationKind,
} from './conversation-frames'

// Frame shapes taken from the golden-path attach capture (evidence/16-session-attach.txt).
// Fields that do not drive the mapping are omitted; the discriminator and the
// values the conversation reads are the ones the capture carried.

const HOOK_STARTED = JSON.stringify({
  type: 'system',
  subtype: 'hook_started',
  hook_id: '70f362d7-2f1e-438f-b4dc-75216b7f61cc',
  hook_name: 'SessionStart:startup',
  hook_event: 'SessionStart',
  uuid: '075504f5-f2a8-4a68-9309-0d7ed77c971e',
  session_id: '73e15f11-74bc-4174-8593-0193c9d1ad87',
})

const HOOK_RESPONSE = JSON.stringify({
  type: 'system',
  subtype: 'hook_response',
  hook_id: '70f362d7-2f1e-438f-b4dc-75216b7f61cc',
  hook_name: 'SessionStart:startup',
  hook_event: 'SessionStart',
  output: 'session start hook',
  uuid: '0c561276-8e1c-40b0-83dc-fc58a4ac12df',
  session_id: '73e15f11-74bc-4174-8593-0193c9d1ad87',
})

const INIT = JSON.stringify({
  type: 'system',
  subtype: 'init',
  cwd: '/home/operator/project',
  session_id: '73e15f11-74bc-4174-8593-0193c9d1ad87',
  tools: ['Task', 'Bash', 'Read', 'Write'],
  model: 'claude-opus-5[1m]',
  permissionMode: 'default',
  uuid: '63eb7de3-80c1-4123-ab73-16fd414fcf60',
})

const RATE_LIMIT = JSON.stringify({
  type: 'rate_limit_event',
  rate_limit_info: {
    status: 'allowed_warning',
    rateLimitType: 'seven_day',
    utilization: 0.84,
  },
  uuid: '8761d0d7-0612-4b52-9ae7-c4732ae578c3',
  session_id: '73e15f11-74bc-4174-8593-0193c9d1ad87',
})

const ASSISTANT = JSON.stringify({
  type: 'assistant',
  message: {
    model: 'claude-opus-5',
    id: 'msg_011CfAw2XmQfeZM1Atr37sk8',
    type: 'message',
    role: 'assistant',
    content: [{ type: 'text', text: 'OK' }],
  },
  session_id: '73e15f11-74bc-4174-8593-0193c9d1ad87',
  uuid: '19688617-8687-40bb-8341-3571589d6ba2',
})

const RESULT = JSON.stringify({
  duration_api_ms: 2239,
  stop_reason: 'end_turn',
  session_id: '73e15f11-74bc-4174-8593-0193c9d1ad87',
  total_cost_usd: 0.0274295,
  usage: { input_tokens: 2, output_tokens: 4 },
  modelUsage: {
    'claude-haiku-4-5-20251001': { costUSD: 0.000943 },
    'claude-opus-5[1m]': { costUSD: 0.0264865 },
  },
  subtype: 'success',
  result: 'OK',
  type: 'result',
  duration_ms: 1575,
  uuid: 'abc7b52c-f1a9-4409-a64c-2d3900298fec',
})

const USER = JSON.stringify({
  type: 'user',
  message: { role: 'user', content: 'Reply with the single word OK' },
})

const TOOL_USE = JSON.stringify({
  type: 'assistant',
  message: {
    role: 'assistant',
    content: [
      {
        type: 'tool_use',
        id: 'toolu_read_1',
        name: 'Read',
        input: { file_path: 'README.md' },
      },
    ],
  },
})

const TOOL_RESULT = JSON.stringify({
  type: 'user',
  message: {
    role: 'user',
    content: [
      {
        type: 'tool_result',
        tool_use_id: 'toolu_read_1',
        content: '# Olivares AI\nself-hosted control plane',
      },
    ],
  },
})

const STREAM_DELTA = JSON.stringify({
  type: 'stream_event',
  event: {
    type: 'content_block_delta',
    delta: { type: 'text_delta', text: 'Hel' },
  },
})

const STREAM_DELTA_2 = JSON.stringify({
  type: 'stream_event',
  event: {
    type: 'content_block_delta',
    delta: { type: 'text_delta', text: 'lo' },
  },
})

const PERMISSION = JSON.stringify({
  type: 'system',
  subtype: 'permission',
  tool: 'Bash',
})

function kindsOf(lines: string[]): ConversationKind[] {
  return mapConversationFrames(lines).map((item) => item.kind)
}

describe('mapConversationFrames — kinds from the golden-path attach', () => {
  it('maps every frame kind the attach capture emitted', () => {
    const items = mapConversationFrames([
      HOOK_STARTED,
      HOOK_RESPONSE,
      INIT,
      RATE_LIMIT,
      USER,
      ASSISTANT,
      RESULT,
    ])
    expect(items.map((i) => i.kind)).toEqual([
      'system',
      'system',
      'system',
      'system',
      'operator',
      'assistant',
      'result',
    ])
    expect(items[2]?.systemKind).toBe('init')
    expect(items[2]?.cwd).toBe('/home/operator/project')
    expect(items[2]?.summary).toContain('claude-opus-5[1m]')
    expect(items[2]?.summary).toContain('4 tools')
    expect(items[3]?.systemKind).toBe('rate_limit')
    expect(items[3]?.summary).toMatch(/84%/)
    expect(items[4]?.text).toBe('Reply with the single word OK')
    expect(items[5]?.text).toBe('OK')
    expect(items[6]?.costUsd).toBe(0.0274295)
    expect(items[6]?.durationMs).toBe(1575)
    expect(items[6]?.inputTokens).toBe(2)
    expect(items[6]?.outputTokens).toBe(4)
    expect(items[6]?.model).toBe('claude-opus-5[1m]')
    expect(items[6]?.text).toBe('OK')
  })

  it('keeps the raw line on every item so the inspector can show the wire', () => {
    const items = mapConversationFrames([ASSISTANT])
    expect(items[0]?.raw).toEqual([ASSISTANT])
  })
})

describe('mapConversationFrames — tool calls', () => {
  it('collapses a tool use and its result onto one item', () => {
    const items = mapConversationFrames([TOOL_USE, TOOL_RESULT])
    expect(items).toHaveLength(1)
    expect(items[0]?.kind).toBe('tool')
    expect(items[0]?.toolName).toBe('Read')
    expect(items[0]?.toolArgsSummary).toContain('README.md')
    expect(items[0]?.toolResultSummary).toContain('Olivares AI')
    expect(items[0]?.raw).toHaveLength(2)
  })
})

describe('mapConversationFrames — streaming and quiet lines', () => {
  it('joins consecutive stream deltas into one assistant message', () => {
    const items = mapConversationFrames([STREAM_DELTA, STREAM_DELTA_2])
    expect(items).toHaveLength(1)
    expect(items[0]?.kind).toBe('assistant')
    expect(items[0]?.text).toBe('Hello')
  })

  it('renders a permission prompt as a quiet system line', () => {
    const items = mapConversationFrames([PERMISSION])
    expect(items[0]?.kind).toBe('system')
    expect(items[0]?.systemKind).toBe('permission')
    expect(items[0]?.summary).toContain('Bash')
  })
})

describe('mapConversationFrames — unknown frames never hide data', () => {
  it('keeps a non-JSON line', () => {
    const items = mapConversationFrames(['not a frame at all'])
    expect(kindsOf(['not a frame at all'])).toEqual(['unknown'])
    expect(items[0]?.summary).toContain('not a frame')
    expect(items[0]?.raw).toEqual(['not a frame at all'])
  })

  it('keeps a JSON object with no recognised type', () => {
    const line = JSON.stringify({ foo: 1, bar: ['x'] })
    const items = mapConversationFrames([line])
    expect(items[0]?.kind).toBe('unknown')
    expect(items[0]?.raw).toEqual([line])
    expect(items[0]?.summary).toContain('foo')
  })

  it('keeps a JSON-RPC protocol frame as unknown rather than dropping it', () => {
    const line = JSON.stringify({
      id: 1,
      method: 'turn/start',
      params: { input: [{ type: 'text', text: 'hello' }] },
    })
    const items = mapConversationFrames([line])
    expect(items[0]?.kind).toBe('unknown')
    expect(items[0]?.summary).toBe('turn/start')
    expect(items[0]?.raw).toEqual([line])
  })
})

describe('conversationCwd', () => {
  it('reads the init frame working directory', () => {
    const items = mapConversationFrames([HOOK_STARTED, INIT, ASSISTANT])
    expect(conversationCwd(items)).toBe('/home/operator/project')
  })
})
