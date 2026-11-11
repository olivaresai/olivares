// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Ask Olivares, the client side (docs/design/ask-olivares-agent-v1.md). The engine owns the turn,
// the closed tool table and every tool call, run under a principal narrower than the person. A write
// is only a proposal: an approval request bound to the exact action. A person decides it with their
// own session, and the person then runs it once. Nothing here holds logic the CLI (`olivares ask`)
// lacks: the tools, the permission strings and the equivalent commands all come from the engine.
import { http } from '@/lib/api/client'

export interface AskTool {
  name: string
  kind: 'read' | 'write'
  permission: string
  summary: string
  /** The person holds the permission now; a tool they do not hold is listed but unusable. */
  allowed: boolean
}

/** Whether Ask is on, the models an administrator allowed (the chosen one first) and its tools. */
export interface AskView {
  enabled: boolean
  model: string
  models: { id: string; name: string }[]
  tools: AskTool[]
}

/** A write waiting for a person, bound to its exact action. */
export interface AskProposal {
  approval_id: string
  status: string
  tool: string
  permission: string
  args: Record<string, string>
  command: string
}

export interface AskReply {
  kind: 'answer' | 'refused' | 'limit' | 'proposal'
  text?: string
  reason?: string
  tool?: string
  /** The reads the engine ran, each with the command that does the same thing. */
  steps: { tool: string; permission: string; command: string; status: number }[]
  proposal?: AskProposal
}

export interface AskMessage {
  role: 'user' | 'assistant'
  content: string
}

/** The approval a proposal waits on, as the person reads it. */
export interface AskApproval {
  status: string
  may_decide?: boolean
}

const ASK = '/v1/m/models/ask'
const APPROVALS = '/v1/m/governance/approvals'

export const askKeys = {
  view: (tenant: string | null) => ['ask', tenant, 'view'] as const,
  approval: (tenant: string | null, id: string) =>
    ['ask', tenant, 'approval', id] as const,
}

export const askApi = {
  view: (signal?: AbortSignal) => http.get<AskView>(ASK, { signal }),
  turn: (model: string, messages: AskMessage[]) =>
    http.post<AskReply>(`${ASK}/turns`, { model, messages }),
  approval: (id: string, signal?: AbortSignal) =>
    http.get<AskApproval>(`${APPROVALS}/${encodeURIComponent(id)}`, {
      signal,
    }),
  /** The person's own approval, with their own session. The engine refuses it unless they may decide. */
  approve: (id: string) =>
    http.post<AskApproval>(`${APPROVALS}/${encodeURIComponent(id)}/decisions`, {
      decision: 'approve',
    }),
  /**
   * Runs an approved proposal once, with exactly the action it is bound to. The engine answers 200
   * with the action's own status; a refused action throws, as `olivares ask` exits non-zero.
   */
  run: async (p: AskProposal) => {
    const res = await http.post<{ status: number; output: string }>(
      `${ASK}/proposals/${encodeURIComponent(p.approval_id)}/run`,
      { tool: p.tool, args: p.args },
    )
    if (res.status >= 400) {
      throw new Error(`HTTP ${res.status}: ${res.output}`)
    }
    return res
  },
}
