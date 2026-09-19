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
  toolArgsSummary?: string
  toolResultSummary?: string
  toolId?: string
  model?: string
  inputTokens?: number
  outputTokens?: number
  costUsd?: number
  durationMs?: number
  /** Init frame's working directory, when the driver reported one. */
  cwd?: string
  systemKind?: string
  /** Every source line that built this item, in order. Never empty. */
  raw: string[]
}

const SUMMARY_CAP = 96

export function truncateSummary(value: string, cap = SUMMARY_CAP): string {
  const compact = value.replace(/\s+/g, ' ').trim()
  if (compact.length <= cap) return compact
  return `${compact.slice(0, Math.max(0, cap - 1)).trimEnd()}…`
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
  return {
    model,
    inputTokens,
    outputTokens,
    costUsd,
    durationMs,
    text: text || undefined,
    summary: parts.join(' · '),
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
    (frame.result !== undefined && frame.id !== undefined && !frame.type)
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
    return [
      ...items,
      {
        kind: 'unknown',
        id: nextId('unknown', items.length),
        summary: truncateSummary(trimmed),
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
        return {
          kind: 'tool' as const,
          id: nextId('tool', items.length + i, toolId ?? name),
          summary: name,
          toolName: name,
          toolId,
          toolArgsSummary: summariseArgs(use.input),
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
    return [
      ...items,
      {
        kind: 'unknown',
        id: nextId('unknown', items.length, asString(String(frame.id ?? ''))),
        summary: jsonRpcSummary(frame),
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
