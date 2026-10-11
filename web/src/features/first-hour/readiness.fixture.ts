// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Test stand-in for the engine's readiness answer (GET provider-profiles/readiness): one
// entry per session tool. A tool not named has nothing to run on.
import type {
  ResolveProviderSource,
  ToolReadinessDTO,
} from '@/features/agentops/types'
import { SESSION_TOOLS, TOOL_NAMES, type SessionTool } from './api'

export type ToolAnswer =
  | 'own_login'
  | {
      provider: ResolveProviderSource
      model_required?: boolean
      /** The provider refused the key at its last test. */
      refused?: boolean
    }
  | { code: string; message: string }

export function readinessOf(
  answers: Partial<Record<SessionTool, ToolAnswer>>,
): { tools: ToolReadinessDTO[] } {
  return {
    tools: SESSION_TOOLS.map((driver): ToolReadinessDTO => {
      const a = answers[driver] ?? {
        code: 'nothing_to_run_on',
        message: `${TOOL_NAMES[driver]} has nothing to run on yet.`,
      }
      if (a === 'own_login') return { driver, ready: true, reason: 'own_login' }
      if ('code' in a) return { driver, ready: false, ...a }
      return {
        driver,
        ready: !a.refused,
        reason: 'api_key',
        provider: a.provider,
        model_required: a.model_required,
        ...(a.refused
          ? {
              code: 'key_refused',
              message: `${TOOL_NAMES[driver]} would run on a refused key.`,
            }
          : {}),
      }
    }),
  }
}
