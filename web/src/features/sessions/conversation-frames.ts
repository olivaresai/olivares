// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE DRIVER'S FRAMES, TOLD AS A CONVERSATION.
//
// An operated session's attach stream is one NDJSON line per I/O frame. The default
// view is what those frames ARE — an operator turn, an assistant reply, a tool call,
// a quiet system line, a result footer — never the wire. The raw line is kept on
// every item so the inspector can show it; an unknown shape is never dropped.

export const CONVERSATION_KINDS = [
  'operator',
  'assistant',
  'tool',
  'system',
  'result',
  'unknown',
] as const

export type ConversationKind = (typeof CONVERSATION_KINDS)[number]

export interface ConversationItem {
  kind: ConversationKind
  id: string
  /** One-line reading of the item. */
  summary: string
  /** Full text for operator and assistant messages. */
  text?: string
  toolName?: string
  /** The agent reported the tool failed (ACP `status: "failed"`). */
  toolFailed?: boolean
  toolArgsSummary?: string
  /** A command tool's (Bash) command, whole: the row shows it, not its JSON input. */
  toolCommand?: string
  /** The description the agent gave a command, when it gave one. */
  toolDescription?: string
  toolResultSummary?: string
  toolId?: string
  /** How long the tool has been running, from Claude Code's `tool_progress`. */
  toolElapsedSeconds?: number
  model?: string
  inputTokens?: number
  outputTokens?: number
  costUsd?: number
  durationMs?: number
  /** Init frame's working directory, when the driver reported one. */
  cwd?: string
  systemKind?: string
  /** The ACP message a streamed reply belongs to (`agent_message_chunk.messageId`). */
  messageId?: string
  /** How an ACP turn stopped, when it did not simply finish (`stopReason` other than
   * end_turn): cancelled, refusal, max_tokens, max_turn_requests, or what the agent sent. */
  stopReason?: string
  /** How many retry frames one retrying line stands for. */
  attempts?: number
  /** The HTTP status a retried provider call answered (Claude Code's `error_status`). */
  httpStatus?: number
  /** A result the tool marked as an error (Claude Code `is_error`, an `error_*`
   * subtype): its text is the failure, and it is shown (F1C). */
  resultFailed?: boolean
  /** Every source line that built this item, in order. Never empty. */
  raw: string[]
}

const SUMMARY_CAP = 96

export function truncateSummary(value: string, cap = SUMMARY_CAP): string {
  const compact = value.replace(/\s+/g, ' ').trim()
  if (compact.length <= cap) return compact
  return `${compact.slice(0, Math.max(0, cap - 1)).trimEnd()}…`
}

// The escape sequences a terminal tool writes: colour and cursor (CSI), links and titles
// (OSC, ended by BEL or ST), and the short ones (a character set, a reverse index).
// Codex logs its errors in colour (HU2-27: the console showed
// "\u001b[2m…\u001b[31mERROR\u001b[0m" as text).
const ANSI =
  // eslint-disable-next-line no-control-regex -- the point is to remove them.
  /\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[ -/]*[0-~]/g

/** A tool's line as a person reads it: without terminal escape sequences. */
export function stripAnsi(value: string): string {
  return value.replace(ANSI, '')
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null
}

function asString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function asNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function summariseUnknown(value: unknown): string {
  if (typeof value === 'string') return truncateSummary(value)
  if (typeof value === 'number' || typeof value === 'boolean')
    return truncateSummary(String(value))
  if (value === null || value === undefined) return ''
  try {
    return truncateSummary(JSON.stringify(value))
  } catch {
    return truncateSummary(String(value))
  }
}

function summariseArgs(value: unknown): string {
  const rec = asRecord(value)
  if (!rec) return summariseUnknown(value)
  const entries = Object.entries(rec)
  if (entries.length === 0) return ''
  if (entries.length === 1) {
    const [key, val] = entries[0]
    const shown = typeof val === 'string' ? val : summariseUnknown(val)
    return truncateSummary(`${key} ${shown}`)
  }
  return summariseUnknown(rec)
}

function contentBlocks(value: unknown): Record<string, unknown>[] {
  if (Array.isArray(value)) {
    return value.map(asRecord).filter((b): b is Record<string, unknown> => !!b)
  }
  const rec = asRecord(value)
  if (rec?.content !== undefined) return contentBlocks(rec.content)
  return []
}

function textFromContent(value: unknown): string {
  if (typeof value === 'string') return value
  const rec = asRecord(value)
  if (typeof rec?.content === 'string') return rec.content
  const blocks = contentBlocks(value)
  const parts: string[] = []
  for (const block of blocks) {
    if (block.type === 'text' && typeof block.text === 'string') {
      parts.push(block.text)
    }
  }
  if (parts.length > 0) return parts.join('')
  if (typeof rec?.text === 'string') return rec.text
  return ''
}

function toolUses(value: unknown): Record<string, unknown>[] {
  return contentBlocks(value).filter((b) => b.type === 'tool_use')
}

function toolResults(value: unknown): Record<string, unknown>[] {
  return contentBlocks(value).filter((b) => b.type === 'tool_result')
}

function messageOf(frame: Record<string, unknown>): unknown {
  return frame.message ?? frame
}

function nextId(kind: ConversationKind, index: number, hint?: string): string {
  return hint && hint.trim() ? `${kind}-${hint}` : `${kind}-${index}`
}

function systemSummary(frame: Record<string, unknown>): {
  summary: string
  systemKind: string
  cwd?: string
  model?: string
} {
  const subtype = asString(frame.subtype) ?? ''
  if (subtype === 'init') {
    const tools = Array.isArray(frame.tools) ? frame.tools.length : 0
    const model = asString(frame.model)
    const cwd = asString(frame.cwd)
    const parts = ['Session started']
    if (model) parts.push(model)
    if (tools > 0) parts.push(`${tools} tools`)
    return {
      summary: parts.join(' · '),
      systemKind: 'init',
      cwd,
      model,
    }
  }
  if (subtype === 'hook_started' || subtype === 'hook_response') {
    const hook =
      asString(frame.hook_name) ?? asString(frame.hook_event) ?? 'hook'
    return {
      summary:
        subtype === 'hook_started'
          ? `Hook started · ${hook}`
          : `Hook · ${hook}`,
      systemKind: subtype,
    }
  }
  if (subtype === 'permission' || subtype === 'permission_prompt') {
    const tool = asString(frame.tool) ?? asString(frame.tool_name)
    return {
      summary: tool ? `Permission · ${tool}` : 'Permission prompt',
      systemKind: 'permission',
    }
  }
  if (subtype) {
    return {
      summary: truncateSummary(subtype.replace(/_/g, ' ')),
      systemKind: subtype,
    }
  }
  return { summary: 'System', systemKind: 'system' }
}

function rateLimitSummary(frame: Record<string, unknown>): string {
  const info = asRecord(frame.rate_limit_info)
  const status = asString(info?.status) ?? 'rate limit'
  const type = asString(info?.rateLimitType)
  const utilization = asNumber(info?.utilization)
  const parts = [status.replace(/_/g, ' ')]
  if (type) parts.push(type.replace(/_/g, ' '))
  if (utilization !== undefined) parts.push(`${Math.round(utilization * 100)}%`)
  return truncateSummary(parts.join(' · '))
}

function resultFields(
  frame: Record<string, unknown>,
): Pick<
  ConversationItem,
  | 'model'
  | 'inputTokens'
  | 'outputTokens'
  | 'costUsd'
  | 'durationMs'
  | 'text'
  | 'summary'
  | 'resultFailed'
> {
  const usage = asRecord(frame.usage)
  const inputTokens =
    asNumber(usage?.input_tokens) ?? asNumber(usage?.inputTokens)
  const outputTokens =
    asNumber(usage?.output_tokens) ?? asNumber(usage?.outputTokens)
  const costUsd =
    asNumber(frame.total_cost_usd) ??
    asNumber(frame.costUSD) ??
    asNumber(frame.cost_usd)
  const durationMs =
    asNumber(frame.duration_ms) ?? asNumber(frame.duration_api_ms)
  const modelUsage = asRecord(frame.modelUsage)
  const modelFromUsage = modelUsage
    ? Object.keys(modelUsage).sort((a, b) => {
        const ca = asNumber(asRecord(modelUsage[a])?.costUSD) ?? 0
        const cb = asNumber(asRecord(modelUsage[b])?.costUSD) ?? 0
        return cb - ca
      })[0]
    : undefined
  const model =
    asString(frame.model) ??
    asString(asRecord(messageOf(frame) as Record<string, unknown>)?.model) ??
    modelFromUsage
  const text = asString(frame.result) ?? textFromContent(messageOf(frame))
  const parts = ['Result']
  if (model) parts.push(model)
  if (asString(frame.subtype)) parts.push(asString(frame.subtype) as string)
  const subtype = asString(frame.subtype) ?? ''
  const failed = frame.is_error === true || subtype.startsWith('error')
  return {
    model,
    inputTokens,
    outputTokens,
    costUsd,
    durationMs,
    text: text || undefined,
    summary: parts.join(' · '),
    ...(failed ? { resultFailed: true } : {}),
  }
}

function streamDeltaText(frame: Record<string, unknown>): string {
  const event = asRecord(frame.event) ?? frame
  const delta = asRecord(event.delta)
  if (
    asString(delta?.type) === 'text_delta' &&
    typeof delta?.text === 'string'
  ) {
    return delta.text
  }
  if (typeof event.text === 'string') return event.text
  return ''
}

function isJsonRpc(frame: Record<string, unknown>): boolean {
  return (
    (typeof frame.method === 'string' &&
      (frame.id !== undefined || frame.params !== undefined)) ||
    ((frame.result !== undefined || frame.error !== undefined) &&
      frame.id !== undefined &&
      !frame.type)
  )
}

function jsonRpcSummary(frame: Record<string, unknown>): string {
  if (typeof frame.method === 'string') {
    return truncateSummary(frame.method)
  }
  return 'Protocol frame'
}

/**
 * Fold one attach line (the `line` field of an output frame) into conversation
 * items. Unknown input is kept as an `unknown` item: the mapper never drops a
 * line it cannot name.
 */
/** A Codex error's sentence. Its `message` is sometimes the provider's own JSON error
 * (`{"error":{"message":…}}`); that inner message is the sentence. */
function codexErrorText(error: unknown): string | undefined {
  const message = asString(asRecord(error)?.message)
  if (!message?.trimStart().startsWith('{')) return message
  try {
    const inner = asRecord(asRecord(JSON.parse(message))?.error)
    return asString(inner?.message) ?? message
  } catch {
    return message
  }
}

/** One Codex app-server notification read as a conversation item, or null. */
function codexItem(
  frame: Record<string, unknown>,
  raw: string,
  n: number,
): ConversationItem | null {
  const method = asString(frame.method)
  const params = asRecord(frame.params)
  if (!method || !params) return null
  if (method === 'item/completed') {
    const item = asRecord(params.item)
    const kind = asString(item?.type)
    const id = asString(item?.id)
    if (kind === 'agentMessage' && typeof item?.text === 'string') {
      return {
        kind: 'assistant',
        id: nextId('assistant', n, id),
        summary: truncateSummary(item.text),
        text: item.text,
        raw: [raw],
      }
    }
    if (kind === 'userMessage') {
      const content = Array.isArray(item?.content) ? item.content : []
      const text = content
        .map((c) => asString(asRecord(c)?.text) ?? '')
        .join(' ')
        .trim()
      if (text)
        return {
          kind: 'operator',
          id: nextId('operator', n, id),
          summary: truncateSummary(text),
          text,
          raw: [raw],
        }
    }
    if (kind === 'commandExecution') {
      const command = asString(item?.command) ?? ''
      return {
        kind: 'tool',
        id: nextId('tool', n, id),
        summary: truncateSummary(command),
        toolName: 'command',
        toolArgsSummary: truncateSummary(command),
        raw: [raw],
      }
    }
    return null
  }
  if (method === 'turn/completed') {
    // A failed turn is not a completed one (HU2-12): it says so, with the reason.
    const turn = asRecord(params.turn)
    if (asString(turn?.status) === 'failed') {
      return {
        kind: 'system',
        id: nextId('system', n),
        summary: truncateSummary(codexErrorText(turn?.error) ?? 'Turn failed'),
        systemKind: 'turnFailed',
        raw: [raw],
      }
    }
    return {
      kind: 'result',
      id: nextId('result', n),
      summary: 'Turn completed',
      raw: [raw],
    }
  }
  if (method === 'error') {
    // `willRetry` is the client retrying on its own; without it the error is final.
    const retrying = params.willRetry === true
    return {
      kind: 'system',
      id: nextId('system', n),
      summary: truncateSummary(codexErrorText(params.error) ?? 'Error'),
      systemKind: retrying ? 'retrying' : 'error',
      ...(retrying ? { attempts: 1 } : {}),
      raw: [raw],
    }
  }
  return null
}

/** The output an ACP `tool_call_update` carries (its content blocks, else rawOutput), or ''
 * when it carries only a status: a status is not output, and must not replace output the
 * tool already sent (SR2C on 0df0c254). */
function acpToolOutput(update: Record<string, unknown>): string {
  const blocks = Array.isArray(update.content) ? update.content : []
  const text = blocks
    .map((b) => textFromContent(asRecord(b)?.content))
    .filter(Boolean)
    .join('\n')
  if (text) return text
  if (update.rawOutput !== undefined) return summariseUnknown(update.rawOutput)
  return ''
}

/**
 * ACP (OpenCode, Grok) told like Claude's conversation (HU2-01). The reply streams as
 * `session/update` `agent_message_chunk` pieces that share a `messageId`; the person's
 * own words come as `user_message_chunk` when the agent sends them (a loaded session
 * replays them); a tool is `tool_call`, then `tool_call_update`; the `session/prompt`
 * response (`stopReason`, `usage`) ends the turn. Commands, modes and plans return null
 * and stay in the quiet protocol line. Before this, every one of these frames was folded
 * into "Protocol frames: N" and the answer was never shown.
 */
function foldAcp(
  items: ConversationItem[],
  frame: Record<string, unknown>,
  raw: string,
): ConversationItem[] | null {
  if (frame.method === 'session/update') {
    const update = asRecord(asRecord(frame.params)?.update)
    const kind = asString(update?.sessionUpdate)
    if (!update || !kind) return null
    if (kind === 'agent_message_chunk' || kind === 'user_message_chunk') {
      const text = textFromContent(update.content)
      if (!text) return null
      const role = kind === 'agent_message_chunk' ? 'assistant' : 'operator'
      const messageId = asString(update.messageId)
      // A chunk joins its message: by id when the agent sends one (another frame may
      // sit between two chunks), else the reply it directly follows.
      let at = -1
      if (messageId) {
        for (let i = items.length - 1; i >= 0; i--) {
          if (items[i]?.kind === role && items[i]?.messageId === messageId) {
            at = i
            break
          }
        }
      } else if (
        items[items.length - 1]?.kind === role &&
        items[items.length - 1]?.messageId === undefined
      ) {
        at = items.length - 1
      }
      if (at >= 0) {
        const item = items[at]!
        const joined = `${item.text ?? ''}${text}`
        return [
          ...items.slice(0, at),
          {
            ...item,
            text: joined,
            summary: truncateSummary(joined),
            raw: [...item.raw, raw],
          },
          ...items.slice(at + 1),
        ]
      }
      return [
        ...items,
        {
          kind: role,
          id: nextId(role, items.length, messageId),
          text,
          summary: truncateSummary(text),
          messageId,
          raw: [raw],
        },
      ]
    }
    if (kind === 'tool_call' || kind === 'tool_call_update') {
      const toolId = asString(update.toolCallId)
      const at = toolId
        ? items.findIndex((i) => i.kind === 'tool' && i.toolId === toolId)
        : -1
      const input = asRecord(update.rawInput)
      const command = asString(input?.command)
      const name =
        asString(update.title) ??
        (at >= 0 ? items[at]!.toolName : undefined) ??
        asString(update.kind) ??
        'tool'
      const output = kind === 'tool_call_update' ? acpToolOutput(update) : ''
      const fields: Partial<ConversationItem> = {
        summary: name,
        toolName: name,
        ...(input
          ? {
              toolArgsSummary: command
                ? truncateSummary(command)
                : summariseArgs(input),
            }
          : {}),
        ...(command
          ? {
              toolCommand: command,
              toolDescription: asString(input?.description),
            }
          : {}),
        ...(output ? { toolResultSummary: truncateSummary(output) } : {}),
        // A status is not output, but a failure is never hidden (SR2C on e2083ea8).
        ...(asString(update.status) === 'failed' ? { toolFailed: true } : {}),
      }
      if (at >= 0) {
        const item = items[at]!
        return [
          ...items.slice(0, at),
          { ...item, ...fields, raw: [...item.raw, raw] },
          ...items.slice(at + 1),
        ]
      }
      return [
        ...items,
        {
          kind: 'tool',
          id: nextId('tool', items.length, toolId),
          summary: name,
          toolId,
          ...fields,
          raw: [raw],
        },
      ]
    }
    return null
  }
  const result = asRecord(frame.result)
  if (
    frame.id !== undefined &&
    result &&
    typeof result.stopReason === 'string'
  ) {
    const fields = resultFields(result)
    const stop = result.stopReason
    return [
      ...items,
      {
        kind: 'result',
        id: nextId('result', items.length),
        ...fields,
        summary:
          stop === 'end_turn' ? fields.summary : `${fields.summary} · ${stop}`,
        // The footer says how the turn stopped when it did not just finish (SR2C).
        ...(stop === 'end_turn' ? {} : { stopReason: stop }),
        raw: [raw],
      },
    ]
  }
  return null
}

/** The same retry, again: one line that counts its attempts, not one line per frame. */
function foldRetry(
  items: ConversationItem[],
  item: ConversationItem,
): ConversationItem[] {
  const last = items[items.length - 1]
  if (
    item.attempts !== undefined &&
    last?.kind === 'system' &&
    last.systemKind === item.systemKind &&
    last.summary === item.summary &&
    // Only identical facts fold: a 503 and then a 529 are two answers, and folding them
    // showed only the first status (SR4C on 906647d5).
    last.httpStatus === item.httpStatus &&
    last.attempts !== undefined
  ) {
    return [
      ...items.slice(0, -1),
      {
        ...last,
        attempts: last.attempts + item.attempts,
        raw: [...last.raw, ...item.raw],
      },
    ]
  }
  return [...items, item]
}

function progressSummary(name: string, seconds: number | undefined): string {
  return seconds === undefined
    ? `${name} running`
    : `${name} running · ${Math.round(seconds)} s`
}

/**
 * Claude Code reports a running tool every few seconds (`tool_progress`). It is the
 * tool's clock, not a new event: it folds into its tool call. Progress for a tool
 * this view has not seen (a sub-agent's) is one quiet line that keeps the latest time.
 */
function foldToolProgress(
  items: ConversationItem[],
  frame: Record<string, unknown>,
  raw: string,
): ConversationItem[] {
  const toolId = asString(frame.tool_use_id)
  const seconds = asNumber(frame.elapsed_time_seconds)
  let at = -1
  for (let i = items.length - 1; toolId && i >= 0; i--) {
    if (items[i]?.kind === 'tool' && items[i]?.toolId === toolId) {
      at = i
      break
    }
  }
  if (at >= 0) {
    const item = items[at]!
    return [
      ...items.slice(0, at),
      {
        ...item,
        toolElapsedSeconds: seconds ?? item.toolElapsedSeconds,
        raw: [...item.raw, raw],
      },
      ...items.slice(at + 1),
    ]
  }
  const name = asString(frame.tool_name) ?? 'Tool'
  const last = items[items.length - 1]
  if (
    last?.kind === 'system' &&
    last.systemKind === 'tool_progress' &&
    last.toolId === toolId
  ) {
    return [
      ...items.slice(0, -1),
      {
        ...last,
        summary: progressSummary(name, seconds ?? last.toolElapsedSeconds),
        toolElapsedSeconds: seconds ?? last.toolElapsedSeconds,
        raw: [...last.raw, raw],
      },
    ]
  }
  return [
    ...items,
    {
      kind: 'system',
      id: nextId('system', items.length, toolId),
      summary: progressSummary(name, seconds),
      systemKind: 'tool_progress',
      toolId,
      toolElapsedSeconds: seconds,
      raw: [raw],
    },
  ]
}

export function foldConversationLine(
  items: ConversationItem[],
  line: string,
): ConversationItem[] {
  const trimmed = line.trimEnd()
  if (!trimmed) return items
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed) as unknown
  } catch {
    // A line that is not JSON is the tool's own output (its stderr, a log). Consecutive
    // lines are one quiet block, every line kept for the inspector (HU2-23: an OpenCode
    // error dump was 18 "Unrecognised frame" rows). The summary is read without the
    // terminal's escape sequences (HU2-27); the inspector keeps the line as it came.
    const text = stripAnsi(trimmed).trim()
    if (!text) return items
    const last = items[items.length - 1]
    if (last?.kind === 'system' && last.systemKind === 'output')
      return [...items.slice(0, -1), { ...last, raw: [...last.raw, trimmed] }]
    return [
      ...items,
      {
        kind: 'system',
        id: nextId('system', items.length),
        summary: truncateSummary(text),
        systemKind: 'output',
        raw: [trimmed],
      },
    ]
  }
  const frame = asRecord(parsed)
  if (!frame) {
    return [
      ...items,
      {
        kind: 'unknown',
        id: nextId('unknown', items.length),
        summary: summariseUnknown(parsed),
        raw: [trimmed],
      },
    ]
  }

  const type = asString(frame.type)

  // Codex app-server frames are JSON-RPC notifications ({method, params}); the
  // ones that carry the conversation are told like Claude's. Everything else
  // stays an unknown item with its raw line, never dropped.
  const codex = codexItem(frame, trimmed, items.length)
  if (codex) return foldRetry(items, codex)

  const acp = foldAcp(items, frame, trimmed)
  if (acp) return acp

  if (type === 'tool_progress') return foldToolProgress(items, frame, trimmed)

  // `control_response` answers a control request the plane itself sent (the turn
  // interrupt). Its outcome is the turn's own next frame and the person's toast, so
  // it is no row of its own: it joins the line before it, and the inspector keeps it.
  if (type === 'control_response') {
    const last = items[items.length - 1]
    if (last)
      return [...items.slice(0, -1), { ...last, raw: [...last.raw, trimmed] }]
    return [
      {
        kind: 'system',
        id: nextId('system', 0),
        summary: 'Control reply',
        systemKind: 'control',
        raw: [trimmed],
      },
    ]
  }

  if (type === 'rate_limit_event') {
    return [
      ...items,
      {
        kind: 'system',
        id: nextId('system', items.length, asString(frame.uuid)),
        summary: rateLimitSummary(frame),
        systemKind: 'rate_limit',
        raw: [trimmed],
      },
    ]
  }

  // Claude Code retrying the provider call (HU2-13): one line that counts the retries,
  // with the category (`error`) and the HTTP status (`error_status`) each frame carries.
  if (type === 'system' && asString(frame.subtype) === 'api_retry') {
    return foldRetry(items, {
      kind: 'system',
      id: nextId('system', items.length, asString(frame.uuid)),
      summary: asString(frame.error) ?? 'api retry',
      systemKind: 'apiRetry',
      httpStatus: asNumber(frame.error_status),
      attempts: 1,
      raw: [trimmed],
    })
  }

  if (type === 'system') {
    const sys = systemSummary(frame)
    return [
      ...items,
      {
        kind: 'system',
        id: nextId(
          'system',
          items.length,
          asString(frame.uuid) ?? sys.systemKind,
        ),
        summary: sys.summary,
        systemKind: sys.systemKind,
        cwd: sys.cwd,
        model: sys.model,
        raw: [trimmed],
      },
    ]
  }

  if (type === 'result') {
    const fields = resultFields(frame)
    return [
      ...items,
      {
        kind: 'result',
        id: nextId('result', items.length, asString(frame.uuid)),
        ...fields,
        raw: [trimmed],
      },
    ]
  }

  if (type === 'stream_event') {
    const delta = streamDeltaText(frame)
    if (!delta) {
      return [
        ...items,
        {
          kind: 'system',
          id: nextId('system', items.length, asString(frame.uuid)),
          summary: 'Stream',
          systemKind: 'stream',
          raw: [trimmed],
        },
      ]
    }
    const last = items[items.length - 1]
    if (last?.kind === 'assistant') {
      const text = `${last.text ?? ''}${delta}`
      return [
        ...items.slice(0, -1),
        {
          ...last,
          text,
          summary: truncateSummary(text),
          raw: [...last.raw, trimmed],
        },
      ]
    }
    return [
      ...items,
      {
        kind: 'assistant',
        id: nextId('assistant', items.length, asString(frame.uuid)),
        text: delta,
        summary: truncateSummary(delta),
        raw: [trimmed],
      },
    ]
  }

  if (type === 'assistant') {
    const message = messageOf(frame)
    const uses = toolUses(message)
    if (uses.length > 0) {
      const added: ConversationItem[] = uses.map((use, i) => {
        const name = asString(use.name) ?? 'tool'
        const toolId = asString(use.id)
        const input = asRecord(use.input)
        const command = asString(input?.command)
        return {
          kind: 'tool' as const,
          id: nextId('tool', items.length + i, toolId ?? name),
          summary: name,
          toolName: name,
          toolId,
          toolArgsSummary: command
            ? truncateSummary(command)
            : summariseArgs(use.input),
          ...(command
            ? {
                toolCommand: command,
                toolDescription: asString(input?.description),
              }
            : {}),
          raw: [trimmed],
        }
      })
      const text = textFromContent(message)
      if (text.trim()) {
        return [
          ...items,
          {
            kind: 'assistant',
            id: nextId('assistant', items.length, asString(frame.uuid)),
            text,
            summary: truncateSummary(text),
            model: asString(asRecord(message)?.model),
            raw: [trimmed],
          },
          ...added,
        ]
      }
      return [...items, ...added]
    }
    const text = textFromContent(message)
    const last = items[items.length - 1]
    if (last?.kind === 'assistant' && text) {
      const merged = `${last.text ?? ''}${last.text ? '\n' : ''}${text}`
      return [
        ...items.slice(0, -1),
        {
          ...last,
          text: merged,
          summary: truncateSummary(merged),
          raw: [...last.raw, trimmed],
        },
      ]
    }
    return [
      ...items,
      {
        kind: 'assistant',
        id: nextId('assistant', items.length, asString(frame.uuid)),
        text: text || undefined,
        summary: text ? truncateSummary(text) : 'Assistant',
        model: asString(asRecord(message)?.model),
        raw: [trimmed],
      },
    ]
  }

  if (type === 'user') {
    const message = messageOf(frame)
    const results = toolResults(message)
    if (results.length > 0) {
      let next = items
      for (const result of results) {
        const toolId = asString(result.tool_use_id)
        const body =
          typeof result.content === 'string'
            ? result.content
            : textFromContent(result.content) ||
              summariseUnknown(result.content)
        const at = next.findIndex(
          (item) => item.kind === 'tool' && toolId && item.toolId === toolId,
        )
        if (at >= 0) {
          const item = next[at]
          next = [
            ...next.slice(0, at),
            {
              ...item,
              toolResultSummary: truncateSummary(body),
              raw: [...item.raw, trimmed],
            },
            ...next.slice(at + 1),
          ]
        } else {
          next = [
            ...next,
            {
              kind: 'tool',
              id: nextId('tool', next.length, toolId),
              summary: 'Tool result',
              toolId,
              toolResultSummary: truncateSummary(body),
              raw: [trimmed],
            },
          ]
        }
      }
      return next
    }
    const text =
      textFromContent(message) ||
      (typeof frame.message === 'string' ? frame.message : '')
    return [
      ...items,
      {
        kind: 'operator',
        id: nextId('operator', items.length, asString(frame.uuid)),
        text: text || undefined,
        summary: text ? truncateSummary(text) : 'Operator',
        raw: [trimmed],
      },
    ]
  }

  if (isJsonRpc(frame)) {
    // PROTOCOL BOOKKEEPING IS ONE QUIET LINE, NOT A CONVERSATION TURN. Replies and lifecycle
    // notifications (initialize, thread/start, turn/start, token usage) read as a run of
    // "Unrecognised frame" cards on every Codex session. Consecutive frames fold into one
    // system line that keeps every raw line for the inspector; an error says what failed.
    const error = asRecord(frame.error)
    if (error) {
      return [
        ...items,
        {
          kind: 'system',
          id: nextId('system', items.length, asString(String(frame.id ?? ''))),
          summary: truncateSummary(
            asString(error.message) ?? jsonRpcSummary(frame),
          ),
          systemKind: 'protocolError',
          raw: [trimmed],
        },
      ]
    }
    const last = items[items.length - 1]
    if (last?.kind === 'system' && last.systemKind === 'protocol') {
      return [...items.slice(0, -1), { ...last, raw: [...last.raw, trimmed] }]
    }
    return [
      ...items,
      {
        kind: 'system',
        id: nextId('system', items.length, asString(String(frame.id ?? ''))),
        summary: jsonRpcSummary(frame),
        systemKind: 'protocol',
        raw: [trimmed],
      },
    ]
  }

  return [
    ...items,
    {
      kind: 'unknown',
      id: nextId('unknown', items.length, type),
      summary: type
        ? truncateSummary(type.replace(/_/g, ' '))
        : summariseUnknown(frame),
      raw: [trimmed],
    },
  ]
}

/** Map every attach line into conversation items, in order. */
export function mapConversationFrames(
  lines: readonly string[],
): ConversationItem[] {
  return lines.reduce<ConversationItem[]>(
    (items, line) => foldConversationLine(items, line),
    [],
  )
}

/** Working directory the driver reported on init, if any. */
export function conversationCwd(
  items: readonly ConversationItem[],
): string | undefined {
  for (const item of items) {
    if (item.kind === 'system' && item.systemKind === 'init' && item.cwd) {
      return item.cwd
    }
  }
  return undefined
}
