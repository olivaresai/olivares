// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import nativeHistory from './fixtures/codex-native-history.json'
import claudeStream from './fixtures/claude-2.1.309-subagents.jsonl?raw'
import {
  conversationCwd,
  conversationModel,
  cutMiddle,
  isQuiet,
  mapConversationFrames,
  sameModel,
  stripAnsi,
  type ConversationItem,
  type ConversationKind,
} from './conversation-frames'

// Frame shapes taken from the golden-path attach capture (evidence/16-session-attach.txt).
// Stored history captured from real Codex 0.162.1 with a labelled loopback
// Responses provider by TestCodexNativeConversationAfterStoppedHandleIsGone.
describe('native persisted Codex conversation', () => {
  it('restores both turns in order and deduplicates live replay by turn/item identity', () => {
    const lines = [JSON.stringify(nativeHistory)]
    for (const turn of nativeHistory.turns) {
      for (const item of turn.items)
        lines.push(
          JSON.stringify({
            method: 'item/completed',
            params: { item, turnId: turn.id },
          }),
        )
      lines.push(JSON.stringify({ method: 'turn/completed', params: { turn } }))
    }
    const items = mapConversationFrames(lines)
    expect(items.map((item) => item.kind)).toEqual([
      'operator',
      'assistant',
      'result',
      'operator',
      'assistant',
      'result',
    ])
    expect(items.filter((item) => item.text).map((item) => item.text)).toEqual([
      'initial CLI prompt',
      'fixture answer',
      'earlier console prompt',
      'fixture answer',
    ])
    expect(new Set(items.map((item) => item.id)).size).toBe(items.length)
  })

  it('keeps identical text in distinct turns and does not complete an active or interrupted turn', () => {
    const turns = nativeHistory.turns.map((turn, index) => ({
      ...turn,
      status: index === 0 ? 'interrupted' : 'inProgress',
      items: turn.items.map((item) =>
        item.type === 'userMessage'
          ? { ...item, content: [{ type: 'text', text: 'same prompt' }] }
          : item,
      ),
    }))
    const items = mapConversationFrames([
      JSON.stringify({ ...nativeHistory, turns }),
    ])
    expect(items.filter((item) => item.kind === 'operator')).toHaveLength(2)
    expect(
      items
        .filter((item) => item.kind === 'result')
        .map((item) => item.summary),
    ).toEqual(['Turn interrupted'])
  })
})
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
      'operator',
      'assistant',
      'result',
    ])
    expect(items[2]?.systemKind).toBe('init')
    expect(items[2]?.cwd).toBe('/home/operator/project')
    expect(items[2]?.summary).toContain('claude-opus-5[1m]')
    expect(items[2]?.summary).toContain('4 tools')
    // A limit warning that did not stop the turn is no row (dev#3119): it joins the line
    // before it, for the inspector.
    expect(items[2]?.raw).toEqual([INIT, RATE_LIMIT])
    expect(items[3]?.text).toBe('Reply with the single word OK')
    expect(items[4]?.text).toBe('OK')
    expect(items[5]?.costUsd).toBe(0.0274295)
    expect(items[5]?.durationMs).toBe(1575)
    expect(items[5]?.inputTokens).toBe(2)
    expect(items[5]?.outputTokens).toBe(4)
    // The turn is named after its own reply's model, not the costliest usage key.
    expect(items[5]?.model).toBe('claude-opus-5')
    expect(items[5]?.text).toBe('OK')
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
  // Changed, stated (HU2-23): a non-JSON line is the tool's output, one quiet line.
  it('keeps a non-JSON line, as tool output', () => {
    const items = mapConversationFrames(['not a frame at all'])
    expect(kindsOf(['not a frame at all'])).toEqual(['system'])
    expect(items[0]?.systemKind).toBe('output')
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

  it('makes no row of progress for a call it has not seen (dev#3119)', () => {
    const items = mapConversationFrames([
      progress('toolu_other', 5),
      progress('toolu_other', 10),
    ])
    expect(items.every(isQuiet)).toBe(true)
    expect(items.flatMap((item) => item.raw)).toHaveLength(2)
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

// HU2-01: an OpenCode session on a local Ollama model, as the attach stream carried it
// (HU2/captures/B1-cli/09-follow-json.txt, OpenCode 1.18.34, verbatim): initialize and
// session/new results, the commands update, the first turn's result, then the second
// reply streamed as agent_message_chunk pieces of one messageId and its result.
const OPENCODE_ACP_FRAMES = [
  '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"mcpCapabilities":{"http":true,"sse":true},"promptCapabilities":{"embeddedContext":true,"image":true},"sessionCapabilities":{"close":{},"fork":{},"list":{},"resume":{}}},"authMethods":[{"description":"Run `opencode auth login` in the terminal","name":"Login with opencode","id":"opencode-login"}],"agentInfo":{"name":"OpenCode","version":"1.18.34"}}}',
  '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"ses_f022118a7ffe1347O0s2FSarZp","configOptions":[{"id":"model","name":"Model","category":"model","type":"select","currentValue":"olivares_ollama/qwen2.5:0.5b","options":[{"value":"olivares_ollama/qwen2.5:0.5b","name":"Olivares Ollama (local)/qwen2.5:0.5b"}]},{"id":"mode","name":"Session Mode","category":"mode","type":"select","currentValue":"build","options":[{"value":"build","name":"build","description":"The default agent. Executes tools based on configured permissions."},{"value":"plan","name":"plan","description":"Plan mode. Disallows all edit tools."}]}]}}',
  '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_f022118a7ffe1347O0s2FSarZp","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"customize-opencode","description":"Use ONLY when the user is editing or creating opencode\'s own configuration: opencode.json, opencode.jsonc, files under .opencode/, or files under ~/.config/opencode/. Also use when creating or fixing opencode agents, subagents, skills, plugins, MCP servers, or permission rules. Do not use for the user\'s own application code, or for any project that is not configuring opencode itself."},{"name":"init","description":"guided AGENTS.md setup"},{"name":"review","description":"review changes [commit|branch|pr], defaults to uncommitted"}]}}}',
  '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn","usage":{"inputTokens":2046,"outputTokens":53,"totalTokens":2103,"cachedReadTokens":4},"_meta":{}}}',
  '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_f022118a7ffe1347O0s2FSarZp","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_0fde14a03001ArQ6820o8MFW3r","content":{"type":"text","text":"Hello"}}}}',
  '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_f022118a7ffe1347O0s2FSarZp","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_0fde14a03001ArQ6820o8MFW3r","content":{"type":"text","text":","}}}}',
  '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_f022118a7ffe1347O0s2FSarZp","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_0fde14a03001ArQ6820o8MFW3r","content":{"type":"text","text":" how"}}}}',
  '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_f022118a7ffe1347O0s2FSarZp","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_0fde14a03001ArQ6820o8MFW3r","content":{"type":"text","text":" are"}}}}',
  '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_f022118a7ffe1347O0s2FSarZp","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_0fde14a03001ArQ6820o8MFW3r","content":{"type":"text","text":" you"}}}}',
  '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_f022118a7ffe1347O0s2FSarZp","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_0fde14a03001ArQ6820o8MFW3r","content":{"type":"text","text":" today"}}}}',
  '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_f022118a7ffe1347O0s2FSarZp","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_0fde14a03001ArQ6820o8MFW3r","content":{"type":"text","text":"?"}}}}',
  '{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn","usage":{"inputTokens":2046,"outputTokens":8,"totalTokens":2058,"cachedReadTokens":4},"_meta":{}}}',
]

describe('ACP frames (OpenCode, Grok)', () => {
  it('tells the streamed reply as one assistant message, and each turn as a result', () => {
    const items = mapConversationFrames(OPENCODE_ACP_FRAMES)
    expect(items.map((i) => i.kind)).toEqual<ConversationKind[]>([
      'system',
      'result',
      'assistant',
      'result',
    ])
    expect(items[0].systemKind).toBe('protocol')
    expect(items[2].text).toBe('Hello, how are you today?')
    expect(items[2].raw).toHaveLength(7)
    expect(items[1].outputTokens).toBe(53)
    expect(items[3]).toMatchObject({ inputTokens: 2046, outputTokens: 8 })
    // Every line is still kept, for the inspector.
    expect(items.flatMap((i) => i.raw)).toHaveLength(OPENCODE_ACP_FRAMES.length)
  })

  it("shows the person's own words when the agent sends them", () => {
    const user = (text: string) =>
      JSON.stringify({
        jsonrpc: '2.0',
        method: 'session/update',
        params: {
          sessionId: 'ses_1',
          update: {
            sessionUpdate: 'user_message_chunk',
            messageId: 'msg_user',
            content: { type: 'text', text },
          },
        },
      })
    const items = mapConversationFrames([user('Say '), user('hello')])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ kind: 'operator', text: 'Say hello' })
  })

  // SR2C on 0df0c254: a status-only "completed" replaced the output with "completed".
  it('keeps the output a tool sent when its completion carries only a status', () => {
    const frame = (update: Record<string, unknown>) =>
      JSON.stringify({
        jsonrpc: '2.0',
        method: 'session/update',
        params: { sessionId: 'ses_1', update },
      })
    const items = mapConversationFrames([
      frame({ sessionUpdate: 'tool_call', toolCallId: 'c', title: 'Read' }),
      frame({
        sessionUpdate: 'tool_call_update',
        toolCallId: 'c',
        status: 'in_progress',
        content: [
          { type: 'content', content: { type: 'text', text: 'a.txt' } },
        ],
      }),
      frame({
        sessionUpdate: 'tool_call_update',
        toolCallId: 'c',
        status: 'completed',
      }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0].toolResultSummary).toBe('a.txt')
  })

  // SR2C on e2083ea8: a status-only "failed" vanished with the output-only rule.
  it('marks a tool failed on a status-only failure, and keeps its output', () => {
    const frame = (update: Record<string, unknown>) =>
      JSON.stringify({
        jsonrpc: '2.0',
        method: 'session/update',
        params: { sessionId: 'ses_1', update },
      })
    const items = mapConversationFrames([
      frame({ sessionUpdate: 'tool_call', toolCallId: 'c', title: 'Read' }),
      frame({
        sessionUpdate: 'tool_call_update',
        toolCallId: 'c',
        status: 'in_progress',
        content: [
          { type: 'content', content: { type: 'text', text: 'a.txt' } },
        ],
      }),
      frame({
        sessionUpdate: 'tool_call_update',
        toolCallId: 'c',
        status: 'failed',
      }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      toolFailed: true,
      toolResultSummary: 'a.txt',
    })
  })

  it('reads a tool call and its update as one tool row with its output', () => {
    const frame = (update: Record<string, unknown>) =>
      JSON.stringify({
        jsonrpc: '2.0',
        method: 'session/update',
        params: { sessionId: 'ses_1', update },
      })
    const items = mapConversationFrames([
      frame({
        sessionUpdate: 'tool_call',
        toolCallId: 'call_1',
        title: 'bash',
        kind: 'execute',
        status: 'pending',
        rawInput: { command: 'ls', description: 'List files' },
      }),
      frame({
        sessionUpdate: 'tool_call_update',
        toolCallId: 'call_1',
        status: 'completed',
        content: [
          { type: 'content', content: { type: 'text', text: 'README.md' } },
        ],
      }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      kind: 'tool',
      toolName: 'bash',
      toolCommand: 'ls',
      toolDescription: 'List files',
      toolResultSummary: 'README.md',
    })
  })
})

// HU2-12: Codex's retries and a failed turn, from HU2's B2 runs. The four `error` frames are
// verbatim (B2-cli/01-codex-follow-json.txt); the failed turn carries the provider error the
// CLI printed for the Codex + Ollama run (B2-cli/03-codex-ollama-follow.txt) in the turn
// object the app-server sends (id, items, status, error), as turn/started shows it there.
const CODEX_RETRIES = [
  '{"method":"error","params":{"error":{"message":"Reconnecting... waiting for network","codexErrorInfo":{"responseStreamDisconnected":{"httpStatusCode":null}},"additionalDetails":"Connection failed: error sending request","misalignment":null},"willRetry":true,"threadId":"01a0fde9-5517-7593-aa62-ec658e9969d9","turnId":"01a0fde9-5583-7020-a75c-accccb82f376"},"emittedAtMs":1790966325522}',
  '{"method":"error","params":{"error":{"message":"Reconnecting... waiting for network","codexErrorInfo":{"responseStreamDisconnected":{"httpStatusCode":null}},"additionalDetails":"Connection failed: error sending request","misalignment":null},"willRetry":true,"threadId":"01a0fde9-5517-7593-aa62-ec658e9969d9","turnId":"01a0fde9-5583-7020-a75c-accccb82f376"},"emittedAtMs":1790966333487}',
  '{"method":"error","params":{"error":{"message":"Reconnecting... waiting for network","codexErrorInfo":{"responseStreamDisconnected":{"httpStatusCode":null}},"additionalDetails":"Connection failed: error sending request","misalignment":null},"willRetry":true,"threadId":"01a0fde9-5517-7593-aa62-ec658e9969d9","turnId":"01a0fde9-5583-7020-a75c-accccb82f376"},"emittedAtMs":1790966346598}',
  '{"method":"error","params":{"error":{"message":"Reconnecting... waiting for network","codexErrorInfo":{"responseStreamDisconnected":{"httpStatusCode":null}},"additionalDetails":"Connection failed: error sending request","misalignment":null},"willRetry":true,"threadId":"01a0fde9-5517-7593-aa62-ec658e9969d9","turnId":"01a0fde9-5583-7020-a75c-accccb82f376"},"emittedAtMs":1790966369560}',
]
const CODEX_FAILED_TURN = JSON.stringify({
  method: 'turn/completed',
  params: {
    threadId: 'thr_1',
    turn: {
      id: 'turn_1',
      items: [],
      status: 'failed',
      error: {
        message:
          '{"error":{"message":"input[0]: unknown input item type: \\"additional_tools\\"","type":"invalid_request_error","param":null,"code":null}}',
      },
    },
  },
})

describe('Codex failures (HU2-12)', () => {
  it("reads a failed turn as failed, with the provider's own sentence", () => {
    const items = mapConversationFrames([CODEX_FAILED_TURN])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      kind: 'system',
      systemKind: 'turnFailed',
      summary: 'input[0]: unknown input item type: "additional_tools"',
    })
  })

  it('folds the same retry into one line that counts its attempts', () => {
    const items = mapConversationFrames(CODEX_RETRIES)
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      kind: 'system',
      systemKind: 'retrying',
      summary: 'Reconnecting... waiting for network',
      attempts: 4,
    })
    expect(items[0].raw).toHaveLength(4)
  })

  it('a completed turn still reads as a result', () => {
    const done = JSON.stringify({
      method: 'turn/completed',
      params: {
        turn: { id: 't', items: [], status: 'completed', error: null },
      },
    })
    expect(mapConversationFrames([done])[0].kind).toBe('result')
  })
})

// HU2-13: Claude Code retrying a provider call. HU2 recorded these fields on every frame of
// a session with a refused API key (error_status 401, error authentication_failed, seven
// retries); nothing else of the frame drives the reading, so nothing else is assumed here.
const apiRetry = (error: string, status: number) =>
  JSON.stringify({
    type: 'system',
    subtype: 'api_retry',
    error_status: status,
    error,
  })

describe('Claude Code api_retry (HU2-13)', () => {
  it('folds the retries of a refused key into one line that keeps the status', () => {
    const frames = Array.from({ length: 7 }, () =>
      apiRetry('authentication_failed', 401),
    )
    const items = mapConversationFrames(frames)
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      kind: 'system',
      systemKind: 'apiRetry',
      summary: 'authentication_failed',
      httpStatus: 401,
      attempts: 7,
    })
    expect(items[0].raw).toHaveLength(7)
  })
})

// HU2-23: OpenCode with Ollama stopped. Its stderr dump is the 18 lines the CLI printed
// (HU2/captures/F1-cli/02-start-help-opencode.txt, verbatim); the session/prompt reply is
// the JSON-RPC error with the code, message and data that dump shows.
const OPENCODE_DOWN_STDERR = [
  'Error handling request {',
  '  jsonrpc: "2.0",',
  '  id: 3,',
  '  method: "session/prompt",',
  '  params: {',
  '    sessionId: "ses_f01fd05cfffe9gqKFib3T5mAdJ",',
  '    prompt: [',
  '      [Object ...]',
  '    ],',
  '  },',
  '} {',
  '  code: -32603,',
  '  message: "Internal error: Cannot connect to API: Unable to connect. Is the computer able to access the url?",',
  '  data: {',
  '    service: "session",',
  '    errorName: "APIError",',
  '  },',
  '}',
]
const OPENCODE_DOWN_REPLY = JSON.stringify({
  jsonrpc: '2.0',
  id: 3,
  error: {
    code: -32603,
    message:
      'Internal error: Cannot connect to API: Unable to connect. Is the computer able to access the url?',
    data: { service: 'session', errorName: 'APIError' },
  },
})

describe("a tool's own output (HU2-23)", () => {
  it('folds the stderr dump into one line and keeps the failed reply', () => {
    const items = mapConversationFrames([
      ...OPENCODE_DOWN_STDERR,
      OPENCODE_DOWN_REPLY,
    ])
    expect(items.map((i) => i.systemKind)).toEqual(['output', 'protocolError'])
    expect(items[0].raw).toHaveLength(18)
    expect(items[0].summary).toBe('Error handling request {')
    expect(items[1].summary).toContain('Cannot connect to API')
  })
})

// HU2-27: Codex with a refused OpenAI key. The console showed this line with its escape
// codes. Its start up to the target is J1's page text (HU2/captures/J1-web2-hu2b); the
// rest is J1-cli/01-codex-follow.txt, joined by the dim ":" tracing writes after a target.
const CODEX_401_STDERR =
  '\u001b[2m2026-10-02T19:30:41.144666Z\u001b[0m \u001b[31mERROR\u001b[0m ' +
  '\u001b[2mcodex_api::endpoint::responses_websocket\u001b[0m\u001b[2m:\u001b[0m ' +
  'failed to connect to websocket: HTTP error: 401 Unauthorized, url: wss://api.openai.com/v1/responses'

describe('a tool line in colour (HU2-27)', () => {
  it('reads without its escape codes and keeps the line as it came', () => {
    const items = mapConversationFrames([CODEX_401_STDERR])
    expect(items).toHaveLength(1)
    expect(items[0].systemKind).toBe('output')
    expect(items[0].summary).not.toContain('\u001b')
    expect(items[0].summary).toMatch(
      /^2026-10-02T19:30:41\.144666Z ERROR codex_api::endpoint::responses_websocket: failed/,
    )
    expect(items[0].raw).toEqual([CODEX_401_STDERR])
  })

  it('drops a line that is nothing but escape codes', () => {
    expect(
      mapConversationFrames(['\u001b[0m', '\u001b]0;title\u0007']),
    ).toEqual([])
  })

  it('removes colour, links and the short sequences', () => {
    expect(stripAnsi('\u001b[1;31mred\u001b[0m')).toBe('red')
    expect(
      stripAnsi('\u001b]8;;https://example.test\u001b\\link\u001b]8;;\u001b\\'),
    ).toBe('link')
    expect(stripAnsi('a\u001b(Bb\u001bMc')).toBe('abc')
  })
})

// SR4C on 906647d5: two server_error retries answered 503 then 529, and the folded line
// kept only 503. Identical retries still fold.
describe('retries with different HTTP statuses', () => {
  const retry = (status: number) =>
    JSON.stringify({
      type: 'system',
      subtype: 'api_retry',
      error: 'server_error',
      error_status: status,
    })

  it('keeps each status, and folds the same status', () => {
    const items = mapConversationFrames([retry(503), retry(503), retry(529)])
    expect(items.map((i) => [i.httpStatus, i.attempts])).toEqual([
      [503, 2],
      [529, 1],
    ])
    expect(items.flatMap((i) => i.raw)).toHaveLength(3)
  })
})

// F1C: a failed start showed only "Result"; the run's stored reason and the CLI had the
// sentence. The frame is the one F1C's Claude Code stub printed before exiting 1.
const FAILED_START_RESULT = JSON.stringify({
  type: 'result',
  subtype: 'error',
  is_error: true,
  result: 'F1 protocol fixture: deliberate provider start failure',
})

describe('a result the tool marked as an error (F1C)', () => {
  it('is marked failed and keeps its text', () => {
    const [item] = mapConversationFrames([FAILED_START_RESULT])
    expect(item.kind).toBe('result')
    expect(item.resultFailed).toBe(true)
    expect(item.text).toBe(
      'F1 protocol fixture: deliberate provider start failure',
    )
  })

  it('leaves a successful result unmarked', () => {
    const [item] = mapConversationFrames([
      JSON.stringify({
        type: 'result',
        subtype: 'success',
        is_error: false,
        result: 'done',
      }),
    ])
    expect(item.resultFailed).toBeUndefined()
  })
})

// #1784 (real use of the released 0.1, Claude Code 2.1.296): the conversation showed two
// "thinking tokens" rows before the answer and an "allowed · five hour" line after it. Both
// are the tool's own bookkeeping: a progress counter while the model thinks, and a rate
// limit report whose status is plain `allowed`. The shapes are the ones that binary's
// stream-json schema declares. Neither is a turn, so neither is a row; the inspector keeps
// every raw line.
const THINKING_TOKENS = (estimated: number) =>
  JSON.stringify({
    type: 'system',
    subtype: 'thinking_tokens',
    estimated_tokens: estimated,
    estimated_tokens_delta: estimated,
    user_message_uuid: '3b3cbd79-7c2a-47f5-8d52-7e7d0a1b5c11',
    uuid: `u-think-${estimated}`,
    session_id: '73e15f11-74bc-4174-8593-0193c9d1ad87',
  })
const RATE_LIMIT_ALLOWED = JSON.stringify({
  type: 'rate_limit_event',
  rate_limit_info: {
    status: 'allowed',
    resetsAt: 1760270400,
    rateLimitType: 'five_hour',
  },
  uuid: 'c2f0b6a4-5a8e-4e3c-9d07-2f0b3e3b1c44',
  session_id: '73e15f11-74bc-4174-8593-0193c9d1ad87',
})
const RATE_LIMIT_REJECTED = JSON.stringify({
  type: 'rate_limit_event',
  rate_limit_info: {
    status: 'rejected',
    resetsAt: 1760270400,
    rateLimitType: 'five_hour',
  },
  uuid: 'a7d1e0f2-0b0c-4b0e-8f55-6a6d6a7a2b10',
  session_id: '73e15f11-74bc-4174-8593-0193c9d1ad87',
})

describe('mapConversationFrames — the tool’s own bookkeeping is no row (#1784)', () => {
  it('shows no row for thinking-token counters or an allowed rate limit', () => {
    const lines = [
      INIT,
      USER,
      THINKING_TOKENS(120),
      THINKING_TOKENS(412),
      ASSISTANT,
      RATE_LIMIT_ALLOWED,
      RESULT,
    ]
    const items = mapConversationFrames(lines)
    expect(items.map((i) => i.kind)).toEqual([
      'system',
      'operator',
      'assistant',
      'result',
    ])
    expect(items.map((i) => i.systemKind)).toEqual([
      'init',
      undefined,
      undefined,
      undefined,
    ])
    expect(items.some((i) => /thinking tokens|allowed/i.test(i.summary))).toBe(
      false,
    )
  })

  it('keeps every raw line reachable from some item', () => {
    const lines = [
      THINKING_TOKENS(120),
      INIT,
      USER,
      THINKING_TOKENS(412),
      ASSISTANT,
      RATE_LIMIT_ALLOWED,
      RESULT,
    ]
    const raws = mapConversationFrames(lines).flatMap((i) => i.raw)
    expect([...raws].sort()).toEqual([...lines].sort())
  })

  it('does not split a streamed answer when a counter arrives inside it', () => {
    const delta = (text: string) =>
      JSON.stringify({
        type: 'stream_event',
        event: {
          type: 'content_block_delta',
          index: 0,
          delta: { type: 'text_delta', text },
        },
      })
    const items = mapConversationFrames([
      delta('Hel'),
      THINKING_TOKENS(40),
      delta('lo'),
    ])
    expect(items).toHaveLength(1)
    expect(items[0]?.kind).toBe('assistant')
    expect(items[0]?.text).toBe('Hello')
  })

  it('keeps only a refusal, the limit that stopped the turn, as a row (dev#3119)', () => {
    const items = mapConversationFrames([RATE_LIMIT, RATE_LIMIT_REJECTED])
    expect(items.map((i) => i.systemKind)).toEqual(['quiet', 'rate_limit'])
    expect(items[1]).toMatchObject({
      limitWindow: 'five_hour',
      limitResetsAt: new Date(1760270400 * 1000).toISOString(),
    })
  })

  it('leaves every limit state that did not stop the turn off the conversation (dev#3119)', () => {
    const allowed = (info: Record<string, unknown>) =>
      JSON.stringify({
        type: 'rate_limit_event',
        rate_limit_info: {
          status: 'allowed',
          rateLimitType: 'five_hour',
          ...info,
        },
      })
    for (const info of [
      { utilization: 0.95 },
      { isUsingOverage: true },
      { overageStatus: 'allowed_warning' },
      { rateLimitType: 'overage' },
      // the top-level figure is the limiting window; another window may be the news
      { unifiedWindows: { seven_day: { utilization: 0.97, resetsAt: 1 } } },
    ])
      expect(kindsOf([USER, allowed(info)])).toEqual(['operator'])
    // a weekly bucket named "overage_included" is not overage, and far from its limit
    expect(
      kindsOf([
        USER,
        allowed({
          rateLimitType: 'seven_day_overage_included',
          utilization: 0.2,
          unifiedWindows: { five_hour: { utilization: 0.1, resetsAt: 1 } },
        }),
      ]),
    ).toEqual(['operator'])
  })

  it('starts its own quiet item rather than writing into the tool’s printed output', () => {
    const items = mapConversationFrames([
      'error: first',
      THINKING_TOKENS(40),
      'error: second',
    ])
    expect(items.map((i) => i.systemKind)).toEqual([
      'output',
      'quiet',
      'output',
    ])
    expect(items[0]?.raw).toEqual(['error: first'])
    expect(items[1]?.raw).toEqual([THINKING_TOKENS(40)])
    expect(items[2]?.raw).toEqual(['error: second'])
  })

  it('holds a leading counter in a quiet item that the next frame does not join', () => {
    const items = mapConversationFrames([
      THINKING_TOKENS(1),
      THINKING_TOKENS(2),
      USER,
    ])
    expect(items.map((i) => i.systemKind ?? i.kind)).toEqual([
      'quiet',
      'operator',
    ])
    expect(items[0]?.raw).toEqual([THINKING_TOKENS(1), THINKING_TOKENS(2)])
  })
})

// An image attached to a message (#2158): the engine replaces the tool's echo of the
// message, which holds the picture, with a frame that names the stored attachments.
describe('a message with attached images', () => {
  it('is an operator row that carries the attachment ids and media types', () => {
    const items = mapConversationFrames([
      JSON.stringify({
        type: 'user',
        isReplay: true,
        message: {
          role: 'user',
          content: [{ type: 'text', text: 'what is wrong here?' }],
        },
        attachments: [
          { id: 'att_1', media_type: 'image/png' },
          { id: 'att_2', media_type: 'image/webp' },
        ],
      }),
    ])
    expect(items).toHaveLength(1)
    expect(items[0].kind).toBe('operator')
    expect(items[0].text).toBe('what is wrong here?')
    expect(items[0].attachments).toEqual([
      { id: 'att_1', mediaType: 'image/png' },
      { id: 'att_2', mediaType: 'image/webp' },
    ])
  })

  it('a message without images has no attachments key at all', () => {
    const [item] = mapConversationFrames([
      JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'plain' },
      }),
    ])
    expect(item.attachments).toBeUndefined()
  })
})

// dev#2786: one session started on claude-opus-5-5 and went on on claude-opus-4-8 with no
// word of it. The conversation names the model of each turn and says when it changes.
describe('the model in use', () => {
  const init = JSON.stringify({
    type: 'system',
    subtype: 'init',
    model: 'claude-opus-5-5',
  })
  const reply = (model: string, text: string, parent: string | null = null) =>
    JSON.stringify({
      type: 'assistant',
      parent_tool_use_id: parent,
      message: { model, content: [{ type: 'text', text }] },
    })
  const result = JSON.stringify({
    type: 'result',
    subtype: 'success',
    duration_ms: 1200,
    // A subagent on another model can cost more than the turn's own model.
    modelUsage: {
      'claude-haiku-5-5': { costUSD: 0.9 },
      'claude-opus-5-5': { costUSD: 0.1 },
    },
  })

  it('names the model that produced each turn on its result', () => {
    const items = mapConversationFrames([
      init,
      reply('claude-opus-5-5', 'one'),
      result,
    ])
    expect(items.find((item) => item.kind === 'result')?.model).toBe(
      'claude-opus-5-5',
    )
  })

  it('tells a change of model inside the session once, from and to, with its time', () => {
    const items = mapConversationFrames(
      [
        init,
        reply('claude-opus-5-5', 'one'),
        result,
        reply('claude-opus-4-8', 'two'),
        reply('claude-opus-4-8', 'three'),
      ],
      [undefined, undefined, undefined, '2026-10-21T12:03:46Z', undefined],
    )
    const changes = items.filter((item) => item.systemKind === 'model')
    expect(changes).toHaveLength(1)
    expect(changes[0]).toMatchObject({
      kind: 'system',
      fromModel: 'claude-opus-5-5',
      model: 'claude-opus-4-8',
      at: '2026-10-21T12:03:46Z',
    })
    expect(items.map((item) => item.kind)).toEqual([
      'system',
      'assistant',
      'result',
      'system',
      'assistant',
    ])
  })

  it('does not count a subagent on another model as a change', () => {
    const items = mapConversationFrames([
      init,
      reply('claude-haiku-5-5', 'searching', 'toolu_1'),
      reply('claude-opus-5-5', 'done'),
    ])
    expect(items.some((item) => item.systemKind === 'model')).toBe(false)
  })
})

describe('the model in use — what is not a change', () => {
  const init = (model: string) =>
    JSON.stringify({ type: 'system', subtype: 'init', model })
  const reply = (model: string, text: string) =>
    JSON.stringify({
      type: 'assistant',
      parent_tool_use_id: null,
      message: { model, content: [{ type: 'text', text }] },
    })

  it('does not take a message the tool wrote itself for a model', () => {
    const items = mapConversationFrames([
      init('claude-opus-5-5'),
      reply('<synthetic>', 'API Error: overloaded'),
      reply('claude-opus-5-5', 'retried'),
    ])
    expect(items.some((item) => item.systemKind === 'model')).toBe(false)
    expect(conversationModel(items.slice(0, 2))).toBe('claude-opus-5-5')
  })

  it('tells a resume that starts on another model', () => {
    const items = mapConversationFrames([
      init('claude-opus-5-5'),
      reply('claude-opus-5-5', 'one'),
      init('claude-opus-4-8'),
      reply('claude-opus-4-8', 'two'),
    ])
    const changes = items.filter((item) => item.systemKind === 'model')
    expect(changes).toHaveLength(1)
    expect(changes[0]).toMatchObject({
      fromModel: 'claude-opus-5-5',
      model: 'claude-opus-4-8',
    })
  })

  it('reads one model through the 1M suffix and a dated snapshot', () => {
    expect(sameModel('claude-opus-5-5[1m]', 'claude-opus-5-5')).toBe(true)
    expect(sameModel('claude-sonnet-4-5', 'claude-sonnet-4-5-20250929')).toBe(
      true,
    )
    expect(sameModel('claude-sonnet-4-5', 'claude-sonnet-4-5-2025-09-29')).toBe(
      true,
    )
    expect(sameModel('claude-opus-5-5', 'claude-opus-4-8')).toBe(false)
  })
})

// A REAL CLAUDE CODE STREAM (dev#3119). Recorded 2026-10-23 from Claude Code 2.1.309 with
// the engine's own argv (cliruntime/argv.go: --input-format stream-json --output-format
// stream-json --verbose --print --replay-user-messages) on claude-haiku-5-5 in a scratch
// folder: a 35 s command, a background command, a background subagent with calls of its
// own, a Write, an Edit, a failing command and a 45 s wait. Paths were rewritten to
// /home/dev/shop, thinking signatures shortened, the init frame's skill, plugin and agent
// lists dropped, and the session, request and message ids and the account's usage figures
// replaced by neutral ones; no frame was added, removed or reordered.
const REAL = claudeStream.trim().split('\n')
const byCommand = (items: ConversationItem[], part: string) =>
  items.find((item) => item.toolCommand?.includes(part))

describe('a real Claude Code 2.1.309 stream (dev#3119)', () => {
  it('keeps task and background-task frames off the conversation, every line still reachable', () => {
    const items = mapConversationFrames(REAL)
    expect(
      items.filter((i) => /^(task_|background_tasks)/.test(i.systemKind ?? '')),
    ).toEqual([])
    expect(
      items.some((i) =>
        /task (started|progress|updated|notification)|background tasks/.test(
          i.summary,
        ),
      ),
    ).toBe(false)
    expect(items.flatMap((i) => i.raw).sort()).toEqual([...REAL].sort())
  })

  it('folds the 30 s heartbeat into its running call: no row of its own', () => {
    const items = mapConversationFrames(REAL)
    expect(items.some((i) => i.systemKind === 'tool_progress')).toBe(false)
    expect(byCommand(items, 'sleep(35)')?.toolElapsedSeconds).toBe(30)
    expect(byCommand(items, 'sleep(45)')?.toolElapsedSeconds).toBe(30)
  })

  it('shows no row for a frame that holds only thinking', () => {
    const items = mapConversationFrames(REAL)
    expect(items.filter((i) => i.kind === 'assistant' && !i.text)).toEqual([])
    expect(items.some((i) => i.summary === 'Assistant')).toBe(false)
  })

  it("tells the person's turn once: a replayed task notification is not theirs", () => {
    const turns = mapConversationFrames(REAL).filter(
      (i) => i.kind === 'operator',
    )
    expect(turns).toHaveLength(1)
    expect(turns[0]?.text).toMatch(/^Follow these steps/)
  })

  it("ties a subagent's calls and reply to the Agent call that started it", () => {
    const items = mapConversationFrames(REAL)
    const agent = items.find((i) => i.kind === 'tool' && i.toolName === 'Agent')
    expect(agent?.toolArgsSummary).toBe('Count entries')
    expect(agent?.toolResultSummary ?? '').not.toMatch(/Async agent launched/)
    expect(agent?.toolStatus).toBe('completed')
    const children = items.filter((i) => i.parentToolId === agent?.toolId)
    expect(children.map((i) => i.toolName ?? i.kind)).toEqual([
      'Bash',
      'Bash',
      'Read',
      'assistant',
    ])
    expect(children[3]?.text).toMatch(/^There are 4 entries/)
    // The session's own last reply stays its own, after the subagent's.
    const replies = items.filter((i) => i.kind === 'assistant')
    expect(replies.at(-1)?.parentToolId).toBeUndefined()
    expect(replies.at(-1)?.text).toMatch(/^I ran all seven steps/)
  })

  it('reads a background command by its outcome, not by its receipt', () => {
    const background = byCommand(mapConversationFrames(REAL), 'sleep(4)')
    expect(background?.toolBackground).toBe(true)
    expect(background?.toolResultSummary ?? '').not.toMatch(/in background/)
    expect(background?.toolStatus).toBe('completed')
  })

  it('marks a call the tool answered with an error failed, and only that one', () => {
    const items = mapConversationFrames(REAL)
    expect(byCommand(items, '&& false')?.toolFailed).toBe(true)
    expect(
      items.filter((i) => i.kind === 'tool' && i.toolFailed).map((i) => i.id),
    ).toEqual([byCommand(items, '&& false')?.id])
  })

  it("carries an edit's own diff: lines added and removed, and a short excerpt", () => {
    const items = mapConversationFrames(REAL)
    expect(items.find((i) => i.toolName === 'Edit')?.toolDiff).toEqual({
      added: 3,
      removed: 2,
      lines: [' alpha', '-beta', '-gamma', '+delta', '+zeta', '+eta'],
    })
    expect(items.find((i) => i.toolName === 'Write')?.toolDiff).toEqual({
      added: 5,
      removed: 0,
      lines: ['+alpha', '+beta', '+gamma', '+epsilon', '+omega'],
    })
  })

  it('names a file tool by its path inside the working folder, never by JSON', () => {
    const items = mapConversationFrames(REAL)
    expect(items.find((i) => i.toolName === 'Write')?.toolArgsSummary).toBe(
      'notes.txt',
    )
    expect(items.find((i) => i.toolName === 'Read')?.toolArgsSummary).toBe(
      'plan.md',
    )
    expect(
      items.some((i) => i.toolArgsSummary?.trimStart().startsWith('{')),
    ).toBe(false)
  })

  it('keeps when each call started and when it answered', () => {
    const at = (i: number) =>
      new Date(Date.UTC(2026, 9, 23, 12, 0, i)).toISOString()
    const items = mapConversationFrames(
      REAL,
      REAL.map((_, i) => at(i)),
    )
    const first = byCommand(items, 'sleep(35)')
    expect(first?.at).toBe(at(6))
    expect(first?.toolEndAt).toBe(at(10))
  })

  it('leaves the warning the 0.3 screenshot showed off the conversation', () => {
    const recorded = JSON.parse(REAL[1]!) as {
      rate_limit_info: Record<string, unknown>
    }
    const warning = JSON.stringify({
      ...recorded,
      rate_limit_info: {
        ...recorded.rate_limit_info,
        status: 'allowed_warning',
        rateLimitType: 'seven_day',
        utilization: 0.93,
      },
    })
    expect(mapConversationFrames([USER, warning]).map((i) => i.kind)).toEqual([
      'operator',
    ])
  })
})

describe('a tool call in its own short form (dev#3119)', () => {
  const toolCall = (name: string, input: Record<string, unknown>) =>
    JSON.stringify({
      type: 'assistant',
      message: {
        content: [{ type: 'tool_use', id: `t-${name}`, name, input }],
      },
    })
  const target = (name: string, input: Record<string, unknown>) =>
    mapConversationFrames([
      JSON.stringify({
        type: 'system',
        subtype: 'init',
        cwd: '/home/dev/shop',
      }),
      toolCall(name, input),
    ])[1]?.toolArgsSummary

  it('reads each tool by what it acts on', () => {
    expect(
      target('Grep', { pattern: 'TODO', path: '/home/dev/shop/src' }),
    ).toBe('TODO · src')
    expect(
      target('WebFetch', { url: 'https://example.com', prompt: 'x' }),
    ).toBe('https://example.com')
    expect(target('Skill', { skill: 'pdf' })).toBe('pdf')
    expect(target('Read', { file_path: '/etc/hosts' })).toBe('/etc/hosts')
  })

  it('reads an unknown tool by its plain fields, not as JSON', () => {
    const shown = target('mcp__db__query', {
      sql: 'select 1',
      limit: 5,
      opts: {},
    })
    expect(shown).toBe('sql select 1 · limit 5')
  })

  it('cuts a long path in the middle and keeps its first folder and its file', () => {
    const cut = cutMiddle(
      'web/src/features/sessions/very/deep/folder/session-conversation.tsx',
      44,
    )
    expect(cut.length).toBeLessThanOrEqual(44)
    expect(cut.startsWith('web/')).toBe(true)
    expect(cut.endsWith('/session-conversation.tsx')).toBe(true)
    expect(cut).toContain('…')
    expect(cutMiddle('src/a.ts', 44)).toBe('src/a.ts')
  })
})

describe('how a call ended, from every tool (dev#3119)', () => {
  it('reads an ACP call that completed without output as ended', () => {
    const [item] = mapConversationFrames([
      JSON.stringify({
        method: 'session/update',
        params: {
          update: { sessionUpdate: 'tool_call', toolCallId: 'c1', title: 'ls' },
        },
      }),
      JSON.stringify({
        method: 'session/update',
        params: {
          update: {
            sessionUpdate: 'tool_call_update',
            toolCallId: 'c1',
            status: 'completed',
          },
        },
      }),
    ])
    expect(item?.toolStatus).toBe('completed')
    expect(item?.toolFailed).toBeUndefined()
  })

  it('marks a background task that failed as failed, and one that was stopped as stopped', () => {
    const recorded = REAL.slice(0, 15)
    const ended = (status: string) =>
      JSON.stringify({
        type: 'system',
        subtype: 'task_notification',
        task_id: 'bxsdzgtr2',
        tool_use_id: 'toolu_01H5RHZSgWd7JBf6MsEWkGD2',
        status,
      })
    const failed = byCommand(
      mapConversationFrames([...recorded, ended('failed')]),
      'sleep(4)',
    )
    expect(failed).toMatchObject({ toolStatus: 'failed', toolFailed: true })
    const stopped = byCommand(
      mapConversationFrames([...recorded, ended('killed')]),
      'sleep(4)',
    )
    expect(stopped?.toolStatus).toBe('stopped')
    expect(stopped?.toolFailed).toBeUndefined()
  })

  it('cuts a long path inside the folder when it names the call', () => {
    const [, item] = mapConversationFrames([
      JSON.stringify({
        type: 'system',
        subtype: 'init',
        cwd: '/home/dev/shop',
      }),
      JSON.stringify({
        type: 'assistant',
        message: {
          content: [
            {
              type: 'tool_use',
              id: 't1',
              name: 'Read',
              input: {
                file_path:
                  '/home/dev/shop/web/src/features/sessions/very/deep/folder/session-conversation.tsx',
              },
            },
          ],
        },
      }),
    ])
    expect(item?.toolArgsSummary).toBe(
      'web/…/sessions/very/deep/folder/session-conversation.tsx',
    )
  })
})

describe('what the reviews of dev#3119 found', () => {
  const codex = (item: Record<string, unknown>) =>
    mapConversationFrames([
      JSON.stringify({
        method: 'item/completed',
        params: {
          turnId: 't',
          item: { type: 'commandExecution', id: 'c1', command: 'ls', ...item },
        },
      }),
    ])[0]
  it('reads a declined Codex command as failed and one in progress as running', () => {
    expect(codex({ status: 'declined' })).toMatchObject({
      toolFailed: true,
      toolStatus: 'failed',
    })
    expect(codex({ status: 'inProgress' })?.toolStatus).toBe('running')
    expect(codex({ status: 'completed', exitCode: 0 })?.toolStatus).toBe(
      'completed',
    )
  })

  it('reads an ACP call that is still in progress as running, whatever output it sent', () => {
    const [item] = mapConversationFrames([
      JSON.stringify({
        method: 'session/update',
        params: {
          update: {
            sessionUpdate: 'tool_call_update',
            toolCallId: 'c1',
            status: 'in_progress',
            content: [
              { type: 'content', content: { type: 'text', text: 'partial' } },
            ],
          },
        },
      }),
    ])
    expect(item).toMatchObject({
      toolStatus: 'running',
      toolResultSummary: 'partial',
    })
  })

  it('keeps a reply with no words but content it does not read as a row', () => {
    const [item] = mapConversationFrames([
      JSON.stringify({
        type: 'assistant',
        message: {
          content: [{ type: 'server_tool_use', id: 's1', name: 'web_search' }],
        },
      }),
    ])
    expect(item).toMatchObject({ kind: 'unknown', summary: 'server_tool_use' })
  })

  it('learns how a call ended from the replayed notification when the task frame is missing', () => {
    const recorded = REAL.slice(0, 15)
    const replay = JSON.stringify({
      type: 'user',
      message: {
        role: 'user',
        content:
          '<task-notification>\n<task-id>bxsdzgtr2</task-id>\n<tool-use-id>toolu_01H5RHZSgWd7JBf6MsEWkGD2</tool-use-id>\n<status>failed</status>\n</task-notification>',
      },
    })
    const items = mapConversationFrames([...recorded, replay])
    expect(byCommand(items, 'sleep(4)')).toMatchObject({
      toolFailed: true,
      toolStatus: 'failed',
    })
    expect(items.filter((i) => i.kind === 'operator')).toHaveLength(1)
  })

  it('reads any end of a task it does not know as failed, never as done', () => {
    const items = mapConversationFrames([
      ...REAL.slice(0, 15),
      JSON.stringify({
        type: 'system',
        subtype: 'task_updated',
        task_id: 'bxsdzgtr2',
        patch: { status: 'timed_out' },
      }),
    ])
    expect(byCommand(items, 'sleep(4)')?.toolStatus).toBe('failed')
  })

  it('tells a limit that stopped the turn whatever its status is called, and survives a reset it cannot date', () => {
    const limit = (info: Record<string, unknown>) =>
      mapConversationFrames([
        JSON.stringify({ type: 'rate_limit_event', rate_limit_info: info }),
      ])[0]
    expect(
      limit({ status: 'blocked', rateLimitType: 'seven_day' })?.systemKind,
    ).toBe('rate_limit')
    const huge = limit({ status: 'rejected', resetsAt: 1e20 })
    expect(huge?.systemKind).toBe('rate_limit')
    expect(huge?.limitResetsAt).toBeUndefined()
  })
})
