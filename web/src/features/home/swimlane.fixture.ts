// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE DEMO ESTATE'S LAST HOUR, AS THE ENGINE SENDS IT (#2547). The console design's demo
// estate (Northwind, Friday 16:42: seven live sessions, three approvals waiting, Marc's
// the flagged one) written as the engine's own reads: the runs and live rows Estate lists,
// each session's timeline events from an hour ago on, and the pending approvals in the
// queue's order. The swimlane's model test and its view test read this one event log.
import type { RunDTO } from '@/features/agentops/types'
import type { ApprovalDTO } from '@/features/governance/types'
import type { LiveDTO, TimelineDTO } from '@/features/sessions/types'

export const NOW = Date.parse('2026-10-17T16:42:00Z')
export const at = (hhmm: string) => Date.parse(`2026-10-17T${hhmm}:00Z`)
export const iso = (ms: number) => new Date(ms).toISOString()

/** Tool calls every `every` minutes from `from` to `to` (both included). */
export function calls(from: string, to: string, every = 1): TimelineDTO[] {
  const out: TimelineDTO[] = []
  for (let t = at(from); t <= at(to); t += every * 60_000)
    out.push({ at: iso(t), kind: 'tool', tool_ref: 'Read' })
  return out
}

export function run(
  ref: string,
  state: string,
  extra: Partial<RunDTO> = {},
): RunDTO {
  return {
    run_ref: ref,
    name: ref,
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state,
    last_event_seq: 1,
    pep_provisioned: true,
    record_io: true,
    critical: false,
    started_at: iso(at('15:30')),
    live_ref: `L-${ref}`,
    canonical_sid: `osn_${ref}`,
    provider_profile_ref: `prof-${ref}`,
    ...extra,
  } as RunDTO
}

export function live(ref: string, cc: string, last: string): LiveDTO {
  return {
    session_ref: `ext-${ref}`,
    cc_state: cc,
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 0,
    tool_call_count: 0,
    first_event_at: iso(at('15:30')),
    last_event_at: iso(at(last)),
    duration_seconds: 0,
    live_ref: `L-${ref}`,
    attribution: 'managed',
    provider_profile_ref: `prof-${ref}`,
    run_ref: ref,
    canonical_sid: `osn_${ref}`,
  }
}

export function approval(
  id: string,
  sessionRef: string,
  asked: string,
): ApprovalDTO {
  return {
    id,
    session_ref: sessionRef,
    status: 'pending',
    required_approvals: 1,
    approve_count: 0,
    reject_count: 0,
    escalated: false,
    created_at: iso(at(asked)),
  }
}

// The demo estate: the runs and live rows the home already reads, and each session's
// timeline from an hour ago on.
const person = (name: string) => ({
  acts_for: { kind: 'user' as const, name },
})
export const runs = [
  run('lena', 'running', {
    name: 'Refactor cart totals in checkout',
    provider_driver: 'claude',
    ...person('Lena Fischer'),
  }),
  run('marc', 'running', {
    name: 'Reconcile invoice export with billing ledger',
    provider_driver: 'codex',
    pending_approval_ref: 'ap-marc',
    ...person('Marc Vidal'),
  }),
  run('sam', 'running', {
    name: 'Upgrade Terraform AWS provider to 6.x',
    provider_driver: 'claude',
    pending_approval_ref: 'ap-sam',
    ...person('Sam Okafor'),
  }),
  run('priya', 'running', {
    name: 'Write docs for the export API',
    provider_driver: 'opencode',
    ...person('Priya Nair'),
  }),
  run('tomas', 'idle', {
    name: 'Fix flaky checkout e2e test',
    provider_driver: 'claude',
    ...person('Tomas Gil'),
  }),
  run('audit', 'running', {
    name: 'Nightly dependency audit',
    provider_driver: 'codex',
    acts_for: { kind: 'automation' },
  }),
  run('ana', 'running', {
    name: 'Profile slow analytics query',
    provider_driver: 'claude',
    ...person('Ana Ruiz'),
  }),
]
export const lives = [
  live('lena', 'active', '16:42'),
  live('marc', 'active', '16:40'),
  live('sam', 'active', '16:36'),
  live('priya', 'active', '16:42'),
  live('tomas', 'idle', '16:31'),
  live('audit', 'active', '16:42'),
  live('ana', 'active', '16:42'),
]
export const events: Record<string, TimelineDTO[]> = {
  lena: [
    ...calls('15:44', '15:53'),
    ...calls('15:57', '16:06'),
    ...calls('16:10', '16:16'),
    ...calls('16:24', '16:42'),
  ],
  marc: [
    ...calls('15:48', '16:01'),
    ...calls('16:06', '16:13'),
    ...calls('16:33', '16:40'),
  ],
  sam: [
    ...calls('15:41', '15:49'),
    ...calls('15:53', '16:02'),
    ...calls('16:12', '16:24'),
    ...calls('16:28', '16:36'),
  ],
  priya: [
    ...calls('16:11', '16:22'),
    ...calls('16:27', '16:31'),
    ...calls('16:38', '16:42'),
  ],
  tomas: [
    ...calls('15:41', '15:47'),
    ...calls('15:51', '16:00'),
    ...calls('16:04', '16:16'),
    ...calls('16:20', '16:28'),
    { at: iso(at('16:31')), kind: 'cost', title: '120 in / 40 out tokens' },
  ],
  audit: calls('16:39', '16:42', 0.5),
  ana: calls('16:40', '16:42', 1),
}
export const approvals = [
  approval('ap-marc', 'osn_marc', '16:40'),
  approval('ap-sam', 'osn_sam', '16:36'),
  approval('ap-lena', 'osn_lena', '16:31'),
]
