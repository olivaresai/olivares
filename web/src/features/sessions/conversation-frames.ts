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

/** A question the agent asks, a plan it proposes or the list it keeps: the three tool calls
 * the conversation paints as a card the person reads (and, for a question, answers). */
export interface ToolQuestion {
  question: string
  header: string
  multiSelect: boolean
  options: { label: string; description: string }[]
}
export interface ToolTodo {
  content: string
  status: string
  activeForm: string
}
export type ToolCard =
  | { kind: 'question'; questions: ToolQuestion[] }
  | { kind: 'plan'; plan: string }
  | { kind: 'todos'; todos: ToolTodo[] }

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
  /** The path a file tool or a search acts on, whole (from the working folder when it is in
   * it). `toolArgsSummary` ends with it; the console shows it through PathText. */
  toolPath?: string
  /** A command tool's (Bash) command, whole: the row shows it, not its JSON input. */
  toolCommand?: string
  /** The description the agent gave a command, when it gave one. */
  toolDescription?: string
  toolResultSummary?: string
  /** The call is a question, a plan or a todo list, read from its input. */
  toolCard?: ToolCard
  toolId?: string
  /** How long the tool has been running, from Claude Code's `tool_progress`. */
  toolElapsedSeconds?: number
  /** When the tool answered (RFC 3339), where the engine said; `at` is when it was called. */
  toolEndAt?: string
  /** The call runs in the background (Claude Code `is_backgrounded`): its answer is a
   * receipt, and its outcome comes with the task's notification. */
  toolBackground?: boolean
  /** Where the call stands when the tool says so: still running (an ACP or Codex call
   * in progress), completed, failed or stopped (Claude Code's task frames). */
  toolStatus?: 'running' | 'completed' | 'failed' | 'stopped'
  /** Claude Code's id of the task the call started: a task update names only it. */
  taskId?: string
  /** What an edit changed: lines added and removed, and the first lines of its diff, each
   * marked `+`, `-` or ` ` (unchanged). */
  toolDiff?: { added: number; removed: number; lines: string[] }
  /** The Agent call a subagent's call or reply belongs to (Claude Code
   * `parent_tool_use_id`). */
  parentToolId?: string
  /** The limit that stopped the turn (systemKind `rate_limit`): its window
   * (`five_hour`, `seven_day`…), when it resets (RFC 3339) and how much of it was used (0-1). */
  limitWindow?: string
  limitResetsAt?: string
  limitUtilization?: number
  model?: string
  /** The model a change of model left (systemKind `model`); `model` is the new one. */
  fromModel?: string
  /** When the engine read the frame (RFC 3339), where it said. */
  at?: string
  /** The attach sequence number of the frame that started this item; absent for an item
   * of the stored history, which comes before every frame. */
  seq?: number
  /** A subagent's reply (Claude Code `parent_tool_use_id`), not the session's own turn. */
  subagent?: boolean
  inputTokens?: number
  outputTokens?: number
  costUsd?: number
  durationMs?: number
  /** Init frame's working directory, when the driver reported one. */
  cwd?: string
  /** Init frame's tool count: `systemText` words the session-start line from it. */
  toolCount?: number
  systemKind?: string
  /** The message a reply belongs to: ACP `agent_message_chunk.messageId`, Claude Code
   * `message.id`. */
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
  /** The images the person attached to this message, by the id the engine keeps them
   * under (operator rows only). */
  attachments?: { id: string; mediaType: string }[]
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
// Codex logs its errors in colour (acceptance: the console showed
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

/** A tool call's summary, and its path whole when it acts on one. */
function toolFields(
  input: unknown,
  cwd: string | undefined,
): Pick<ConversationItem, 'toolArgsSummary' | 'toolPath'> {
  const { summary, path } = toolTarget(input, cwd)
  return path
    ? { toolArgsSummary: summary, toolPath: path }
    : { toolArgsSummary: summary }
}

/** A path inside the working folder is read from there; any other path is read whole. */
function insideFolder(path: string, cwd: string | undefined): string {
  const folder = cwd?.replace(/\/+$/, '')
  return folder && path.startsWith(`${folder}/`)
    ? path.slice(folder.length + 1)
    : path
}

/**
 * A TOOL CALL IN THE TOOL'S OWN SHORT FORM: what it acts on, never its JSON
 * (0.3 read `Write {"file_path":"/home/…`). A command is the command, a file tool its
 * path inside the working folder, a search its pattern, a subagent its description; any
 * other tool its plain fields.
 */
function toolTarget(
  input: unknown,
  cwd: string | undefined,
): { summary: string; path?: string } {
  const rec = asRecord(input)
  if (!rec) return { summary: summariseUnknown(input) }
  // A path stays whole: the console cuts it in its middle (PathText) and keeps all of it.
  const path = (key: string) => {
    const value = asString(rec[key])
    return value ? insideFolder(value, cwd) : undefined
  }
  const command = Array.isArray(rec.command)
    ? rec.command.filter((part) => typeof part === 'string').join(' ')
    : asString(rec.command)
  if (command) return { summary: truncateSummary(command) }
  const file = path('file_path') ?? path('notebook_path')
  if (file) return { summary: file, path: file }
  const pattern = asString(rec.pattern)
  if (pattern) {
    const where = path('path')
    return where
      ? { summary: `${truncateSummary(pattern)} · ${where}`, path: where }
      : { summary: truncateSummary(pattern) }
  }
  for (const key of ['url', 'query', 'description', 'skill']) {
    const value = asString(rec[key])
    if (value) return { summary: truncateSummary(value) }
  }
  const where = path('path')
  if (where) return { summary: where, path: where }
  return {
    summary: truncateSummary(
      Object.entries(rec)
        .flatMap(([key, value]) =>
          ['string', 'number', 'boolean'].includes(typeof value)
            ? [`${key} ${String(value)}`]
            : [],
        )
        .join(' · '),
    ),
  }
}

/** The lines of a diff excerpt: an edit is shown by its first few changed lines. */
const EXCERPT_LINES = 6

type ToolDiff = NonNullable<ConversationItem['toolDiff']>

function linesOf(text: string): string[] {
  return text === '' ? [] : text.replace(/\n$/, '').split('\n')
}

function diffOf(removed: string[], added: string[]): ToolDiff {
  return {
    added: added.length,
    removed: removed.length,
    lines: [
      ...removed.map((line) => `-${line}`),
      ...added.map((line) => `+${line}`),
    ].slice(0, EXCERPT_LINES),
  }
}

/** The card a question, plan or todo call stands for, read from its input; undefined when
 * the call is another tool or its input is not the shape the tool sends (the call then
 * stays an ordinary tool row). */
function inputCard(
  name: string,
  input: Record<string, unknown> | null,
): ToolCard | undefined {
  if (name === 'AskUserQuestion' && Array.isArray(input?.questions)) {
    const questions: ToolQuestion[] = []
    for (const q of input.questions) {
      const rec = asRecord(q)
      const question = asString(rec?.question)
      if (!rec || !question || !Array.isArray(rec.options)) return undefined
      questions.push({
        question,
        header: asString(rec.header) ?? '',
        multiSelect: rec.multiSelect === true,
        options: rec.options.flatMap((o) => {
          const label = asString(asRecord(o)?.label)
          return label
            ? [{ label, description: asString(asRecord(o)?.description) ?? '' }]
            : []
        }),
      })
    }
    return questions.length > 0 ? { kind: 'question', questions } : undefined
  }
  if (name === 'ExitPlanMode' && asString(input?.plan))
    return { kind: 'plan', plan: input!.plan as string }
  if (name === 'TodoWrite' && Array.isArray(input?.todos)) {
    const todos = input.todos.flatMap((t) => {
      const content = asString(asRecord(t)?.content)
      return content
        ? [
            {
              content,
              status: asString(asRecord(t)?.status) ?? 'pending',
              activeForm: asString(asRecord(t)?.activeForm) ?? '',
            },
          ]
        : []
    })
    return { kind: 'todos', todos }
  }
  return undefined
}

/** An edit told by its own input, until the tool answers with its diff. */
function inputDiff(
  name: string,
  input: Record<string, unknown> | null,
): ToolDiff | undefined {
  const tool = name.toLowerCase()
  if (tool === 'write' && typeof input?.content === 'string')
    return diffOf([], linesOf(input.content))
  if (
    tool === 'edit' &&
    typeof input?.old_string === 'string' &&
    typeof input.new_string === 'string'
  )
    return diffOf(linesOf(input.old_string), linesOf(input.new_string))
  return undefined
}

/** The diff the tool answered with (Claude Code `tool_use_result.structuredPatch`, or the
 * content of a file it created): its counts, and its lines from the first change on, with
 * the unchanged line before it. */
function answeredDiff(outcome: unknown): ToolDiff | undefined {
  const rec = asRecord(outcome)
  if (!rec) return undefined
  const lines = (Array.isArray(rec.structuredPatch) ? rec.structuredPatch : [])
    .flatMap((hunk) => {
      const hunkLines = asRecord(hunk)?.lines
      return Array.isArray(hunkLines) ? hunkLines : []
    })
    .filter(
      (line): line is string =>
        typeof line === 'string' && !line.startsWith('\\'),
    )
  const first = lines.findIndex((line) => line[0] === '+' || line[0] === '-')
  if (first < 0)
    return rec.type === 'create' && typeof rec.content === 'string'
      ? diffOf([], linesOf(rec.content))
      : undefined
  const start = Math.max(0, first - 1)
  return {
    added: lines.filter((line) => line[0] === '+').length,
    removed: lines.filter((line) => line[0] === '-').length,
    lines: lines.slice(start, start + EXCERPT_LINES),
  }
}

/** The answer to a call started in the background is a receipt (an id and where its output
 * goes), not the call's result. */
function isReceipt(outcome: unknown): boolean {
  const rec = asRecord(outcome)
  return (
    asString(rec?.backgroundTaskId) !== undefined ||
    asString(rec?.status) === 'async_launched'
  )
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
  toolCount?: number
} {
  const subtype = asString(frame.subtype) ?? ''
  if (subtype === 'init') {
    const tools = Array.isArray(frame.tools) ? frame.tools.length : 0
    const model = asString(frame.model)
    const cwd = asString(frame.cwd)
    // The English line is the fallback text; the rendered one comes from the catalog
    // (`systemText`, from `model` and `toolCount`).
    const parts = ['Session started']
    if (model) parts.push(model)
    if (tools > 0) parts.push(`${tools} tools`)
    return {
      summary: parts.join(' · '),
      systemKind: 'init',
      cwd,
      model,
      toolCount: tools,
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

/** The system lines a conversation view leaves out: the tool's bookkeeping with no row. */
export const isQuiet = (item: ConversationItem): boolean =>
  item.systemKind === 'quiet'

/**
 * THE TOOL'S OWN BOOKKEEPING IS NO CONVERSATION ROW (#1784). A thinking-token counter and a
 * rate limit report that says `allowed` are neither a turn the person took nor words the
 * assistant said: painted as rows they read "thinking tokens" before the answer and
 * "allowed · five hour" after it. The line joins the item before it, so the inspector keeps
 * it and a streamed answer around it stays one message. With nothing before it, or only
 * the tool's printed output (a terminal block shows every raw line it holds), it is a quiet
 * item of its own that both views leave out. That one line is therefore not reachable in
 * the inspector: it carries no words and no turn, and only a line that opens the
 * conversation or follows printed output ends up there.
 */
function foldQuiet(
  items: ConversationItem[],
  trimmed: string,
  summary: string,
): ConversationItem[] {
  const last = items[items.length - 1]
  if (last && last.systemKind !== 'output')
    return [...items.slice(0, -1), { ...last, raw: [...last.raw, trimmed] }]
  return [
    ...items,
    {
      kind: 'system',
      id: nextId('system', items.length),
      summary,
      systemKind: 'quiet',
      raw: [trimmed],
    },
  ]
}

/**
 * THE LIMIT THAT STOPPED THE TURN IS ONE SENTENCE; ANY OTHER LIMIT STATE IS NO ROW.
 * A refusal (`rejected`) stopped the turn: its row says which window, how much was used and
 * when it resets. A warning, a window nearly used or an overage did not: that is the usage
 * meter beside the account, and 0.3's "allowed warning · seven day · 93%" row
 * read as an event of the conversation.
 */
function rateLimitStop(
  items: ConversationItem[],
  frame: Record<string, unknown>,
  trimmed: string,
): ConversationItem[] {
  const info = asRecord(frame.rate_limit_info)
  const status = asString(info?.status) ?? ''
  if (status === 'allowed' || status === 'allowed_warning')
    return foldQuiet(items, trimmed, rateLimitSummary(frame))
  const window = asString(info?.rateLimitType)
  const reset = new Date((asNumber(info?.resetsAt) ?? Number.NaN) * 1000)
  const used =
    asNumber(info?.utilization) ??
    (window
      ? asNumber(
          asRecord(asRecord(info?.unifiedWindows)?.[window])?.utilization,
        )
      : undefined)
  return [
    ...items,
    {
      kind: 'system',
      id: nextId('system', items.length, asString(frame.uuid)),
      summary: rateLimitSummary(frame),
      systemKind: 'rate_limit',
      ...(window ? { limitWindow: window } : {}),
      ...(Number.isNaN(reset.getTime())
        ? {}
        : { limitResetsAt: reset.toISOString() }),
      ...(used !== undefined ? { limitUtilization: used } : {}),
      raw: [trimmed],
    },
  ]
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

/** The images the engine says a user message carried (it replaces the tool's own echo,
 * which holds the picture itself, with these references). */
function attachmentsOf(
  frame: Record<string, unknown>,
): { id: string; mediaType: string }[] {
  if (!Array.isArray(frame.attachments)) return []
  return frame.attachments.flatMap((a) => {
    const id = asString(asRecord(a)?.id)
    return id
      ? [{ id, mediaType: asString(asRecord(a)?.media_type) ?? '' }]
      : []
  })
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
    const itemID = asString(item?.id)
    const turnID = asString(params.turnId)
    const id = itemID && turnID ? `${turnID}:${itemID}` : itemID
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
      const exit = asNumber(item?.exitCode)
      return {
        kind: 'tool',
        id: nextId('tool', n, id),
        summary: truncateSummary(command),
        toolName: 'command',
        toolArgsSummary: truncateSummary(command),
        ...(command ? { toolCommand: command } : {}),
        // A command in progress (a stored running turn) still runs; one declined, failed or
        // with a non-zero exit code failed; any other has completed.
        ...(asString(item?.status) === 'inProgress'
          ? { toolStatus: 'running' as const }
          : (exit !== undefined && exit !== 0) ||
              ['failed', 'declined'].includes(asString(item?.status) ?? '')
            ? { toolFailed: true, toolStatus: 'failed' as const }
            : { toolStatus: 'completed' as const }),
        raw: [raw],
      }
    }
    return null
  }
  if (method === 'turn/completed') {
    // A failed turn is not a completed one: it says so, with the reason.
    const turn = asRecord(params.turn)
    if (asString(turn?.status) === 'failed') {
      return {
        kind: 'system',
        id: nextId('system', n, asString(turn?.id)),
        summary: truncateSummary(codexErrorText(turn?.error) ?? 'Turn failed'),
        systemKind: 'turnFailed',
        raw: [raw],
      }
    }
    if (asString(turn?.status) === 'interrupted') {
      return {
        kind: 'result',
        id: nextId('result', n, asString(turn?.id)),
        summary: 'Turn interrupted',
        stopReason: 'cancelled',
        raw: [raw],
      }
    }
    return {
      kind: 'result',
      id: nextId('result', n, asString(turn?.id)),
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
 * tool already sent (review on 0df0c254). */
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
 * ACP (OpenCode, Grok) told like Claude's conversation. The reply streams as
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
        ...(input ? toolFields(input, conversationCwd(items)) : {}),
        ...(command
          ? {
              toolCommand: command,
              toolDescription: asString(input?.description),
            }
          : {}),
        ...(output ? { toolResultSummary: truncateSummary(output) } : {}),
        // A status is not output, but a failure is never hidden (review on e2083ea8), and a
        // call that completed without output has ended all the same.
        ...(asString(update.status) === 'failed'
          ? { toolFailed: true, toolStatus: 'failed' as const }
          : {}),
        ...(asString(update.status) === 'completed'
          ? { toolStatus: 'completed' as const }
          : {}),
        ...(['pending', 'in_progress'].includes(asString(update.status) ?? '')
          ? { toolStatus: 'running' as const }
          : {}),
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
        // The footer says how the turn stopped when it did not just finish (review).
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
    // showed only the first status (review on 906647d5).
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

/**
 * Claude Code reports a running tool every 30 s (`tool_progress`). It is the tool's clock,
 * not a new event: it folds into its tool call, and is never a row (the regression: 0.3 painted
 * "Bash running · 30 s … 180 s", six rows for one command). Claude Code 2.1.3xx names the
 * beat `<call>-heartbeat-<n>` under the call as its parent. Progress for a call this view
 * has not seen joins the line before it.
 */
function foldToolProgress(
  items: ConversationItem[],
  frame: Record<string, unknown>,
  raw: string,
): ConversationItem[] {
  const toolId = asString(frame.tool_use_id)
  const parent = asString(frame.parent_tool_use_id)
  const beat =
    parent !== undefined &&
    (frame.heartbeat === true || !!toolId?.startsWith(`${parent}-heartbeat`))
  const seconds = asNumber(frame.elapsed_time_seconds)
  const at = items.findLastIndex(
    (item) =>
      item.kind === 'tool' &&
      item.toolId !== undefined &&
      (item.toolId === toolId || (beat && item.toolId === parent)),
  )
  if (at < 0) return foldQuiet(items, raw, 'tool progress')
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

const TASK_FRAMES = new Set([
  'task_started',
  'task_progress',
  'task_updated',
  'task_notification',
])

function taskOutcome(
  status: string | undefined,
): ConversationItem['toolStatus'] {
  switch (status) {
    case 'completed':
      return 'completed'
    case 'failed':
    case 'error':
      return 'failed'
    case 'killed':
    case 'stopped':
    case 'cancelled':
      return 'stopped'
    case undefined:
    case 'running':
    case 'pending':
    case 'in_progress':
      return undefined
    // An end this console does not know is not hidden as a success.
    default:
      return 'failed'
  }
}

/**
 * CLAUDE CODE'S TASK FRAMES ARE THE SIDE PANE'S ACTIVITY, NEVER A CONVERSATION ROW (the regression,
 * the regression: the engine reads them into Activity). 0.3 painted "task started", "task progress",
 * "task updated" and "task notification" between the turns. Each joins the call it belongs
 * to (by its call id, or a task update by its task id), which keeps the line for the
 * inspector and learns from it whether the call runs in the background and how it ended.
 */
function foldTask(
  items: ConversationItem[],
  frame: Record<string, unknown>,
  raw: string,
): ConversationItem[] {
  const subtype = asString(frame.subtype)
  const toolId = asString(frame.tool_use_id)
  const taskId = asString(frame.task_id)
  const at = items.findLastIndex(
    (item) =>
      item.kind === 'tool' &&
      (toolId
        ? item.toolId === toolId
        : taskId !== undefined && item.taskId === taskId),
  )
  if (at < 0)
    return foldQuiet(items, raw, (subtype ?? 'task').replace(/_/g, ' '))
  const item = items[at]!
  const started = subtype === 'task_started'
  const status = taskOutcome(
    asString(frame.status) ?? asString(asRecord(frame.patch)?.status),
  )
  return [
    ...items.slice(0, at),
    {
      ...item,
      ...(started && taskId ? { taskId } : {}),
      ...(started && frame.is_backgrounded === true
        ? { toolBackground: true }
        : {}),
      ...(status ? { toolStatus: status } : {}),
      ...(status === 'failed' ? { toolFailed: true } : {}),
      raw: [...item.raw, raw],
    },
    ...items.slice(at + 1),
  ]
}

/** One model, whatever the tool's `[1m]` context suffix or a dated snapshot of the id says. */
export function sameModel(a: string, b: string): boolean {
  const base = (id: string) =>
    id.replace(/\[1m\]$/i, '').replace(/-\d{4}-?\d{2}-?\d{2}$/, '')
  return base(a) === base(b)
}

/** The model the session's own turns run on now: its last change, its start, or its last
 * own reply, whichever came last. A subagent's reply does not move it. */
export function conversationModel(
  items: readonly ConversationItem[],
): string | undefined {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i]!
    // A message the tool wrote itself names `<synthetic>`, not a model.
    if (!item.model || item.model.startsWith('<')) continue
    if (
      item.kind === 'system' &&
      (item.systemKind === 'model' || item.systemKind === 'init')
    )
      return item.model
    if (item.kind === 'assistant' && !item.subagent) return item.model
  }
  return undefined
}

// the regression: a session that started on claude-opus-5-5 went on on claude-opus-4-8 and
// nothing said so. A reply of the session's own, or a resume, on another model is
// told before it.
function withModelChange(
  items: ConversationItem[],
  frame: Record<string, unknown>,
  model: string | undefined,
  raw: string,
  at: string | undefined,
): ConversationItem[] {
  // A message the tool wrote itself names `<synthetic>`, not a model.
  if (
    !model ||
    model.startsWith('<') ||
    typeof frame.parent_tool_use_id === 'string'
  )
    return items
  const from = conversationModel(items)
  if (!from || sameModel(from, model)) return items
  return [
    ...items,
    {
      kind: 'system',
      id: nextId(
        'system',
        items.length,
        asString(frame.uuid) && `model-${asString(frame.uuid)}`,
      ),
      summary: `Model changed: ${from} → ${model}`,
      systemKind: 'model',
      fromModel: from,
      model,
      ...(at ? { at } : {}),
      raw: [raw],
    },
  ]
}

/** Folds one line, and marks each item it starts with the line's frame `seq`. An item with no id of its own (its
 * position, `nextId(kind, index)`) takes one from that frame instead: the console maps its last frames again on every
 * frame and drops the oldest past its window, and a position would then move under every row. */
export function foldConversationLine(
  items: ConversationItem[],
  line: string,
  at?: string,
  seq?: number,
): ConversationItem[] {
  const next = foldLine(items, line, at, seq)
  if (seq === undefined || next === items) return next
  for (let i = items.length; i < next.length; i++) {
    const item = next[i]!
    const k = i - items.length
    next[i] = {
      ...item,
      seq: item.seq ?? seq,
      ...(item.id === `${item.kind}-${i}`
        ? { id: `${item.kind}-f${seq}${k ? `.${k}` : ''}` }
        : {}),
    }
  }
  return next
}

function foldLine(
  items: ConversationItem[],
  line: string,
  at?: string,
  seq?: number,
): ConversationItem[] {
  const trimmed = line.trimEnd()
  if (!trimmed) return items
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed) as unknown
  } catch {
    // A line that is not JSON is the tool's own output (its stderr, a log). Consecutive
    // lines are one quiet block, every line kept for the inspector (acceptance: an OpenCode
    // error dump was 18 "Unrecognised frame" rows). The summary is read without the
    // terminal's escape sequences; the inspector keeps the line as it came.
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

  if (type === 'olivares_conversation' && Array.isArray(frame.turns)) {
    let restored = items
    for (const value of frame.turns) {
      const turn = asRecord(value)
      if (!turn || !Array.isArray(turn.items)) continue
      for (const item of turn.items) {
        restored = foldConversationLine(
          restored,
          JSON.stringify({
            method: 'item/completed',
            params: { item, turnId: turn.id },
          }),
        )
      }
      // A running turn has no outcome yet. Never show it as completed.
      if (['completed', 'failed', 'interrupted'].includes(String(turn.status)))
        restored = foldConversationLine(
          restored,
          JSON.stringify({
            method: 'turn/completed',
            params: { turn },
          }),
        )
    }
    return restored
  }

  // The engine's own notice in the session's output (an approval Olivares refused, and
  // why): one row in the engine's words, whole, never folded into a quiet block. Any code
  // renders its message, so a new code needs no console change.
  if (type === 'olivares_notice') {
    const message = asString(frame.message)
    if (message)
      return [
        ...items,
        {
          kind: 'system',
          id: nextId('system', items.length),
          summary: message,
          systemKind: 'notice',
          raw: [trimmed],
        },
      ]
  }

  // Codex app-server frames are JSON-RPC notifications ({method, params}); the
  // ones that carry the conversation are told like Claude's. Everything else
  // stays an unknown item with its raw line, never dropped.
  const codex = codexItem(frame, trimmed, items.length)
  if (codex) {
    // Native item/turn identity, not matching words: repeated identical prompts
    // remain distinct; a persisted item replayed by the live ring appears once.
    const params = asRecord(frame.params)
    const entity = asRecord(params?.item ?? params?.turn)
    if (asString(entity?.id)) {
      const existing = items.findIndex((item) => item.id === codex.id)
      if (existing >= 0) {
        // It keeps the first frame that showed it; an item the stored history restored
        // takes this frame's (review of #2855).
        const first = items[existing]!.seq ?? seq
        const replay = first === undefined ? codex : { ...codex, seq: first }
        return items.map((item, index) => (index === existing ? replay : item))
      }
    }
    return foldRetry(items, codex)
  }

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

  if (type === 'rate_limit_event') return rateLimitStop(items, frame, trimmed)

  if (type === 'system' && asString(frame.subtype) === 'thinking_tokens')
    return foldQuiet(items, trimmed, 'thinking tokens')

  if (type === 'system' && TASK_FRAMES.has(asString(frame.subtype) ?? ''))
    return foldTask(items, frame, trimmed)

  // The list of tasks running in the background: the side pane's, not a row.
  if (
    type === 'system' &&
    asString(frame.subtype) === 'background_tasks_changed'
  )
    return foldQuiet(items, trimmed, 'background tasks')

  // Claude Code retrying the provider call: one line that counts the retries,
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
    if (sys.systemKind === 'init')
      items = withModelChange(items, frame, sys.model, trimmed, at)
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
        toolCount: sys.toolCount,
        raw: [trimmed],
      },
    ]
  }

  if (type === 'result') {
    const fields = resultFields(frame)
    // The turn is named after the model of its own replies: the costliest model of a
    // turn can be a subagent's.
    const model =
      asString(frame.model) ?? conversationModel(items) ?? fields.model
    return [
      ...items,
      {
        kind: 'result',
        id: nextId('result', items.length, asString(frame.uuid)),
        ...fields,
        ...(model ? { model } : {}),
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
    items = withModelChange(
      items,
      frame,
      asString(asRecord(message)?.model),
      trimmed,
      at,
    )
    // A subagent's reply and calls belong to the Agent call that started it.
    const parent = asString(frame.parent_tool_use_id)
    const subagent = parent ? { subagent: true, parentToolId: parent } : {}
    const messageId = asString(asRecord(message)?.id)
    const uses = toolUses(message)
    if (uses.length > 0) {
      const cwd = conversationCwd(items)
      const added: ConversationItem[] = uses.map((use, i) => {
        const name = asString(use.name) ?? 'tool'
        const toolId = asString(use.id)
        const input = asRecord(use.input)
        const command = asString(input?.command)
        const diff = inputDiff(name, input)
        const card = inputCard(name, input)
        return {
          kind: 'tool' as const,
          id: nextId('tool', items.length + i, toolId ?? name),
          summary: name,
          toolName: name,
          toolId,
          ...toolFields(use.input, cwd),
          ...(command
            ? {
                toolCommand: command,
                toolDescription: asString(input?.description),
              }
            : {}),
          ...(diff ? { toolDiff: diff } : {}),
          ...(card ? { toolCard: card } : {}),
          ...(parent ? { parentToolId: parent } : {}),
          ...(at ? { at } : {}),
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
            ...(messageId ? { messageId } : {}),
            ...subagent,
            raw: [trimmed],
          },
          ...added,
        ]
      }
      return [...items, ...added]
    }
    const text = textFromContent(message)
    // A FRAME THAT HOLDS ONLY THINKING IS NO ROW: Claude Code sends the thinking of
    // a reply as a frame of its own, with no words, and 0.3 painted a bare "Assistant"
    // heading for each. The line joins the one before it, for the inspector.
    if (!text.trim()) {
      const blocks = contentBlocks(message).map((b) => asString(b.type) ?? '')
      if (
        blocks.every((b) =>
          ['thinking', 'redacted_thinking', 'text'].includes(b),
        )
      )
        return foldQuiet(items, trimmed, 'thinking')
      // Content this console does not read (a server tool, an error with no words) is
      // never dropped: it stays a row that says what it holds.
      return [
        ...items,
        {
          kind: 'unknown',
          id: nextId('unknown', items.length, asString(frame.uuid)),
          summary: truncateSummary(
            blocks.filter(Boolean).join(', ') || 'assistant',
          ),
          ...subagent,
          raw: [trimmed],
        },
      ]
    }
    const last = items[items.length - 1]
    // A frame joins the reply it continues: the same speaker (the session, or the same
    // subagent) and the same message, where both name one.
    if (
      last?.kind === 'assistant' &&
      last.parentToolId === parent &&
      (!messageId || !last.messageId || last.messageId === messageId)
    ) {
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
        text,
        summary: truncateSummary(text),
        model: asString(asRecord(message)?.model),
        ...(messageId ? { messageId } : {}),
        ...subagent,
        raw: [trimmed],
      },
    ]
  }

  if (type === 'user') {
    const message = messageOf(frame)
    const results = toolResults(message)
    if (results.length > 0) {
      // What the tool did, beside its words (Claude Code `tool_use_result`; its own
      // transcript calls it `toolUseResult`): an edit's diff, a background receipt.
      const outcome = frame.tool_use_result ?? frame.toolUseResult
      let next = items
      for (const result of results) {
        const toolId = asString(result.tool_use_id)
        const body =
          typeof result.content === 'string'
            ? result.content
            : textFromContent(result.content) ||
              summariseUnknown(result.content)
        const failed = result.is_error === true ? { toolFailed: true } : {}
        const index = next.findIndex(
          (item) => item.kind === 'tool' && toolId && item.toolId === toolId,
        )
        if (index >= 0) {
          const item = next[index]!
          const diff = results.length === 1 ? answeredDiff(outcome) : undefined
          // The answer to a call started in the background is its receipt, not its result:
          // the outcome comes with the task's notification (the regression: 0.3 showed
          // "Async agent launched successfully… internal metadata, never quote").
          const receipt =
            item.toolBackground || (results.length === 1 && isReceipt(outcome))
          next = [
            ...next.slice(0, index),
            {
              ...item,
              ...(receipt ? { toolBackground: true } : {}),
              // A receipt is not shown; a failure is, with its reason.
              ...(!receipt || result.is_error === true
                ? { toolResultSummary: truncateSummary(body) }
                : {}),
              ...failed,
              ...(diff ? { toolDiff: diff } : {}),
              ...(at ? { toolEndAt: at } : {}),
              raw: [...item.raw, trimmed],
            },
            ...next.slice(index + 1),
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
              ...failed,
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
    // NOT THE PERSON'S TURN: a subagent's own prompt (it has a parent call), or a
    // notice the tool feeds itself and replays on stdout (a finished background task, as
    // `<task-notification>` markup). 0.3 painted the markup as the person's message.
    if (asString(frame.parent_tool_use_id) !== undefined)
      return foldQuiet(items, trimmed, 'subagent prompt')
    if (
      asString(asRecord(frame.origin)?.kind) === 'task-notification' ||
      text.trimStart().startsWith('<task-notification>')
    ) {
      // Its markup names the call and how it ended: the call learns it, as from the
      // task frame, should that frame be missing.
      const tag = (name: string) =>
        new RegExp(`<${name}>([^<]*)</${name}>`).exec(text)?.[1]?.trim()
      return foldTask(
        items,
        {
          subtype: 'task_notification',
          tool_use_id: tag('tool-use-id'),
          task_id: tag('task-id'),
          status: tag('status'),
        },
        trimmed,
      )
    }
    const attachments = attachmentsOf(frame)
    return [
      ...items,
      {
        kind: 'operator',
        id: nextId('operator', items.length, asString(frame.uuid)),
        text: text || undefined,
        summary: text ? truncateSummary(text) : 'Operator',
        ...(attachments.length > 0 ? { attachments } : {}),
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
  /** When the engine read each line, where it said; same order as `lines`. */
  times?: readonly (string | undefined)[],
  /** Each line's attach `seq`, where it has one; same order as `lines`. */
  seqs?: readonly (number | undefined)[],
): ConversationItem[] {
  return lines.reduce<ConversationItem[]>(
    (items, line, i) =>
      foldConversationLine(items, line, times?.[i], seqs?.[i]),
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
