// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Hooks — verified set (ANT2-10). Verbatim against code.claude.com/docs/en/hooks
// on 2026-06-06. The complete event list (30) and the two governance decision
// schemas are encoded below. VERIFIED CORRECTION the UI must honor:
// applyPermissionRule lives ONLY in PermissionRequest (NOT PreToolUse); PreToolUse
// uses permissionDecision allow|deny|ask|defer.
//
// POSTURE (docs/SECURITY-HARDENING.md): hook decision/enforcement is OPT-IN and OFF by
// default and is wired to the gate when enabled. The editor authors the hook
// CONFIG (emission of policy); it must NEVER present enforcement as a default the
// system applies on its own.
import { type SchemaIssue, validateWith, z } from './types'

const HOOKS_DOC = 'https://code.claude.com/docs/en/hooks'

export type HookDecisionKind =
  | 'pre-tool-use'
  | 'permission-request'
  | 'block'
  | 'none'

export interface HookEventDescriptor {
  name: string
  fires: string
  governance?: boolean
  /** Which decision schema (if any) this event supports. */
  decision: HookDecisionKind
}

/** All 30 hook events, in documentation order (verified verbatim). */
export const HOOK_EVENTS: readonly HookEventDescriptor[] = [
  {
    name: 'SessionStart',
    fires: 'When a session begins or resumes.',
    decision: 'none',
  },
  {
    name: 'Setup',
    fires: 'On --init-only, or --init/--maintenance in -p mode.',
    decision: 'none',
  },
  {
    name: 'UserPromptSubmit',
    fires: 'When you submit a prompt, before Claude processes it.',
    decision: 'block',
    governance: true,
  },
  {
    name: 'UserPromptExpansion',
    fires:
      'When a user command expands into a prompt, before it reaches Claude.',
    decision: 'block',
  },
  {
    name: 'PreToolUse',
    fires: 'Before a tool call executes.',
    decision: 'pre-tool-use',
    governance: true,
  },
  {
    name: 'PermissionRequest',
    fires: 'When a permission dialog appears.',
    decision: 'permission-request',
    governance: true,
  },
  {
    name: 'PermissionDenied',
    fires: 'When a tool call is denied by the auto-mode classifier.',
    decision: 'none',
    governance: true,
  },
  {
    name: 'PostToolUse',
    fires: 'After a tool call succeeds.',
    decision: 'block',
  },
  {
    name: 'PostToolUseFailure',
    fires: 'After a tool call fails.',
    decision: 'block',
  },
  {
    name: 'PostToolBatch',
    fires:
      'After a batch of parallel tool calls resolves, before the next model call.',
    decision: 'block',
  },
  {
    name: 'Notification',
    fires: 'When Claude Code sends a notification.',
    decision: 'none',
  },
  {
    name: 'MessageDisplay',
    fires: 'While assistant message text is displayed.',
    decision: 'none',
  },
  {
    name: 'SubagentStart',
    fires: 'When a subagent is spawned.',
    decision: 'none',
    governance: true,
  },
  {
    name: 'SubagentStop',
    fires: 'When a subagent finishes.',
    decision: 'block',
    governance: true,
  },
  {
    name: 'TaskCreated',
    fires: 'When a task is being created via TaskCreate.',
    decision: 'none',
  },
  {
    name: 'TaskCompleted',
    fires: 'When a task is being marked completed.',
    decision: 'none',
  },
  {
    name: 'Stop',
    fires: 'When Claude finishes responding.',
    decision: 'block',
  },
  {
    name: 'StopFailure',
    fires: 'When the turn ends due to an API error.',
    decision: 'none',
  },
  {
    name: 'TeammateIdle',
    fires: 'When an agent-team teammate is about to go idle.',
    decision: 'none',
  },
  {
    name: 'InstructionsLoaded',
    fires:
      'When a CLAUDE.md or .claude/rules/*.md file is loaded into context.',
    decision: 'none',
    governance: true,
  },
  {
    name: 'ConfigChange',
    fires: 'When a configuration file changes during a session.',
    decision: 'block',
    governance: true,
  },
  {
    name: 'CwdChanged',
    fires: 'When the working directory changes.',
    decision: 'none',
  },
  {
    name: 'FileChanged',
    fires: 'When a watched file changes on disk.',
    decision: 'none',
  },
  {
    name: 'WorktreeCreate',
    fires:
      'When a worktree is being created (--worktree / isolation: "worktree").',
    decision: 'none',
  },
  {
    name: 'WorktreeRemove',
    fires: 'When a worktree is being removed.',
    decision: 'none',
  },
  {
    name: 'PreCompact',
    fires: 'Before context compaction.',
    decision: 'block',
    governance: true,
  },
  {
    name: 'PostCompact',
    fires: 'After context compaction completes.',
    decision: 'none',
    governance: true,
  },
  {
    name: 'Elicitation',
    fires: 'When an MCP server requests user input during a tool call.',
    decision: 'none',
  },
  {
    name: 'ElicitationResult',
    fires: 'After a user responds to an MCP elicitation.',
    decision: 'none',
  },
  { name: 'SessionEnd', fires: 'When a session terminates.', decision: 'none' },
]

export const HOOK_EVENT_NAMES: ReadonlySet<string> = new Set(
  HOOK_EVENTS.map((e) => e.name),
)

/** PreToolUse decision (hookSpecificOutput). NOTE: NO applyPermissionRule here. */
export const PRE_TOOL_USE_DECISION = {
  field: 'hookSpecificOutput',
  permissionDecisionValues: ['allow', 'deny', 'ask', 'defer'] as const,
  note: 'permissionDecision: allow | deny | ask | defer (+ permissionDecisionReason, optional modifiedInput/additionalContext). "defer" = use the normal permission flow.',
  source: HOOKS_DOC,
} as const

/** PermissionRequest decision (hookSpecificOutput.decision). applyPermissionRule is
 *  ONLY available here — the editor must not offer it under PreToolUse. */
export const PERMISSION_REQUEST_DECISION = {
  field: 'hookSpecificOutput.decision',
  behaviorValues: ['allow', 'deny'] as const,
  applyPermissionRule: {
    ruleModeValues: ['allow', 'deny'] as const,
    note: 'applyPermissionRule { ruleMode: allow|deny, rule: string } — persists a rule so the user is not prompted again. PermissionRequest ONLY.',
  },
  source: HOOKS_DOC,
} as const

/** Events that support the top-level {"decision":"block","reason"} schema. */
export const BLOCK_DECISION_EVENTS: readonly string[] = HOOK_EVENTS.filter(
  (e) => e.decision === 'block',
).map((e) => e.name)

/** Exit-code semantics (command hooks). Exit 1 is NON-blocking — only 2 blocks. */
export const HOOK_EXIT_CODES = [
  {
    code: '0',
    meaning: 'Success. stdout is parsed for JSON output (only on exit 0).',
  },
  {
    code: '2',
    meaning:
      'Blocking error. stdout/JSON ignored; stderr fed back to Claude. Effect depends on the event.',
  },
  {
    code: 'other',
    meaning:
      'Non-blocking error — execution continues. NOTE: exit 1 is non-blocking; use exit 2 to enforce.',
  },
] as const

const hookCommandSchema = z
  .object({
    type: z.enum(['command', 'http']),
    command: z.string().optional(),
    url: z.string().optional(),
    timeout: z.number().optional(),
  })
  .passthrough()

const hookEntrySchema = z
  .object({
    matcher: z.string().optional(),
    hooks: z.array(hookCommandSchema),
  })
  .passthrough()

/** The `hooks` settings object: { <EventName>: HookEntry[] }. */
export const hooksConfigSchema = z.record(z.string(), z.array(hookEntrySchema))

/**
 * Validate a hooks config object: structural check + ERROR on unknown event names
 * (they would silently never fire), with the verified set as the allowlist.
 */
export function validateHooks(value: unknown): SchemaIssue[] {
  const issues = validateWith(hooksConfigSchema, value)
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const key of Object.keys(value as Record<string, unknown>)) {
      if (!HOOK_EVENT_NAMES.has(key)) {
        issues.push({
          path: key,
          message: `Unknown hook event "${key}" — not one of the ${HOOK_EVENTS.length} Claude Code events. It will never fire.`,
          severity: 'error',
        })
      }
    }
  }
  return issues
}
