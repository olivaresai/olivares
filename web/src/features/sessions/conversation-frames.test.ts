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

describe('mapConversationFrames — a command tool reads as its command (Root review, 09)', () => {
  it('a Bash call reads as its command and description, not JSON', () => {
    // R1 09, real Claude Code 2.1.287: the Bash input as the conversation received it.
    const bash = JSON.stringify({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [
          {
            type: 'tool_use',
            id: 'toolu_bash',
            name: 'Bash',
            input: {
              command: 'echo cli-key-works > cli-key.txt',
              description: 'Run the command the person asked for',
            },
          },
        ],
      },
    })
    const [item] = mapConversationFrames([bash])
    expect(item?.toolCommand).toBe('echo cli-key-works > cli-key.txt')
    expect(item?.toolDescription).toBe('Run the command the person asked for')
    expect(item?.toolArgsSummary).toBe('echo cli-key-works > cli-key.txt')
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

  it('keeps JSON-RPC protocol frames as one quiet line rather than dropping them', () => {
    const start = JSON.stringify({
      id: 1,
      method: 'turn/start',
      params: { input: [{ type: 'text', text: 'hello' }] },
    })
    const reply = JSON.stringify({ id: 1, result: { turn: { id: 'u' } } })
    const items = mapConversationFrames([start, reply])
    expect(items).toHaveLength(1)
    expect(items[0]?.kind).toBe('system')
    expect(items[0]?.systemKind).toBe('protocol')
    expect(items[0]?.raw).toEqual([start, reply])
    const failed = mapConversationFrames([
      JSON.stringify({
        id: 2,
        error: { code: -1, message: 'thread not found' },
      }),
    ])
    expect(failed[0]?.systemKind).toBe('protocolError')
    expect(failed[0]?.summary).toBe('thread not found')
  })
})

// HU 025 (refresh 05, real Claude Code 2.1.287): a long Bash turn printed
// "Unrecognised frame" twice. The shapes below are the ones that binary emits
// (read from the build the product installs): `tool_progress` while a tool runs,
// and `control_response` answering a control request the plane sent (the interrupt).
const BASH_USE = JSON.stringify({
  type: 'assistant',
  message: {
    role: 'assistant',
    content: [
      {
        type: 'tool_use',
        id: 'toolu_bash_1',
        name: 'Bash',
        input: { command: 'python3 -c "import time; time.sleep(60)"' },
      },
    ],
  },
})
const TASK_STARTED = JSON.stringify({
  type: 'system',
  subtype: 'task_started',
  task_id: 'b1',
  tool_use_id: 'toolu_bash_1',
})
const progress = (toolUseId: string, seconds: number) =>
  JSON.stringify({
    type: 'tool_progress',
    tool_use_id: toolUseId,
    tool_name: 'Bash',
    parent_tool_use_id: null,
    elapsed_time_seconds: seconds,
    task_id: 'b1',
    session_id: 'sess-1',
    uuid: `u-${seconds}`,
  })
const INTERRUPT_REPLY = JSON.stringify({
  type: 'control_response',
  response: { subtype: 'success', request_id: 'olv-interrupt-1' },
})
const TASK_NOTIFICATION = JSON.stringify({
  type: 'system',
  subtype: 'task_notification',
  task_id: 'b1',
  status: 'killed',
})
const INTERRUPTED = JSON.stringify({
  type: 'user',
  message: {
    role: 'user',
    content: [
      { type: 'text', text: '[Request interrupted by user for tool use]' },
    ],
  },
})

describe('mapConversationFrames — Claude Code tool progress and control replies', () => {
  it('reads the interrupted long turn without an unrecognised frame', () => {
    const lines = [
      BASH_USE,
      TASK_STARTED,
      progress('toolu_bash_1', 31),
      INTERRUPT_REPLY,
      TASK_NOTIFICATION,
      INTERRUPTED,
    ]
    expect(kindsOf(lines)).not.toContain('unknown')
    // Every line still reaches the inspector through some item.
    const raws = mapConversationFrames(lines).flatMap((item) => item.raw)
    expect(raws.sort()).toEqual([...lines].sort())
  })

  it('folds tool progress into its tool call and keeps the latest elapsed time', () => {
    const items = mapConversationFrames([
      BASH_USE,
      progress('toolu_bash_1', 15),
      progress('toolu_bash_1', 31),
    ])
    expect(kindsOf([BASH_USE, progress('toolu_bash_1', 15)])).toEqual(['tool'])
    expect(items).toHaveLength(1)
    expect(items[0]?.toolElapsedSeconds).toBe(31)
    expect(items[0]?.raw).toHaveLength(3)
  })

  it('tells progress for a tool it has not seen as one quiet line', () => {
    const items = mapConversationFrames([
      progress('toolu_other', 5),
      progress('toolu_other', 10),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]?.kind).toBe('system')
    expect(items[0]?.summary).toContain('Bash')
    expect(items[0]?.raw).toHaveLength(2)
  })

  it("keeps the reply to the plane's own control request off the conversation", () => {
    const items = mapConversationFrames([BASH_USE, INTERRUPT_REPLY])
    expect(items).toHaveLength(1)
    expect(items[0]?.kind).toBe('tool')
    expect(items[0]?.raw).toEqual([BASH_USE, INTERRUPT_REPLY])
  })
})

describe('conversationCwd', () => {
  it('reads the init frame working directory', () => {
    const items = mapConversationFrames([HOOK_STARTED, INIT, ASSISTANT])
    expect(conversationCwd(items)).toBe('/home/operator/project')
  })
})

describe('Codex app-server frames', () => {
  it('reads an agent message, a command and a completed turn as a conversation', () => {
    const lines = [
      JSON.stringify({
        method: 'item/completed',
        params: {
          threadId: 't',
          turnId: 'u',
          item: { type: 'agentMessage', id: 'm1', text: 'The tests pass.' },
        },
      }),
      JSON.stringify({
        method: 'item/completed',
        params: {
          item: {
            type: 'commandExecution',
            id: 'c1',
            command: 'go test ./...',
          },
        },
      }),
      JSON.stringify({
        method: 'turn/completed',
        params: { threadId: 't', turn: { id: 'u', status: 'completed' } },
      }),
      JSON.stringify({
        method: 'thread/tokenUsage/updated',
        params: { threadId: 't' },
      }),
    ]
    const items = mapConversationFrames(lines)
    expect(items.map((i) => i.kind)).toEqual([
      'assistant',
      'tool',
      'result',
      'system',
    ])
    expect(items[0].text).toBe('The tests pass.')
    expect(items[1].toolArgsSummary).toBe('go test ./...')
  })
})
