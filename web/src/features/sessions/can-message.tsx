// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// "Can message": which sessions this one may message or hand work to (ARCH COMMS-PATH #5).
// The engine keeps the list on the run (`peers`, or `peers_rule: same-template`) and
// enforces it where a session creates or assigns work for another (MC); this control
// only chooses. The candidates are the other live sessions this person can read in the
// same workspace (the run's authz_workspace_id, MC 850beb3d), whatever their folders, by
// the canonical session id their managed row carries. The engine rechecks each one.
import { useState } from 'react'
import { MessagesSquare } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import {
  isLiveRun,
  operatorName,
  primaryRun,
  sessionShortId,
  type UnifiedSession,
} from './provenance'

/** A session this run may be allowed to message: live, in the same workspace, with a
 * canonical id. */
export interface PeerCandidate {
  sid: string
  name: string
}

export function peerCandidates(
  self: UnifiedSession,
  sessions: readonly UnifiedSession[],
): PeerCandidate[] {
  const workspace = primaryRun(self.runs)?.authz_workspace_id
  if (!workspace) return []
  const ownSid = self.live?.canonical_sid
  return sessions.flatMap((s) => {
    const run = primaryRun(s.runs)
    const sid = s.live?.canonical_sid
    if (!run || !sid || sid === ownSid || !isLiveRun(run)) return []
    if (run.authz_workspace_id !== workspace) return []
    return [{ sid, name: operatorName(s) ?? sessionShortId(s) }]
  })
}

export function CanMessage({
  session,
  sessions,
}: {
  session: UnifiedSession
  sessions: readonly UnifiedSession[]
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const run = primaryRun(session.runs)
  const [open, setOpen] = useState(false)
  const [chosen, setChosen] = useState<string[]>(run?.peers ?? [])
  const [template, setTemplate] = useState(run?.peers_rule === 'same-template')
  // The engine decides; its refusal stays beside Save instead of in a passing toast.
  const [refusal, setRefusal] = useState<string | null>(null)
  const save = usePrivilegedMutation<void, RunDTO>({
    mutationFn: () =>
      agentOpsApi.setPeers(
        run!.run_ref,
        template ? { peers_rule: 'same-template' } : { peers: chosen },
      ),
    invalidateKeys: [agentOpsKeys.runs(activeTenant)],
    successMessage: t('peers.saved'),
    onDone: () => setOpen(false),
    onError: (err) => {
      if (!(err instanceof ApiError) || !err.message) return false
      setRefusal(err.message)
      return true
    },
  })
  // Shown where the engine keeps peers (MC's run field, `[]` when none): on an engine
  // without it the control would only fail.
  if (!run || !can('sessions:run:write')) return null
  if (run.peers === undefined && run.peers_rule === undefined) return null
  const candidates = peerCandidates(session, sessions)
  const count =
    run.peers_rule === 'same-template' ? null : (run.peers?.length ?? 0)
  // Keep existing choices reachable, but omit the empty default when there is
  // nobody to choose. This is a session-to-session control, not a status badge.
  if (count === 0 && candidates.length === 0) return null
  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        if (next) {
          setChosen(run.peers ?? [])
          setTemplate(run.peers_rule === 'same-template')
          setRefusal(null)
        }
        setOpen(next)
      }}
    >
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm" data-testid="can-message">
          <MessagesSquare className="size-3.5" aria-hidden />
          {count === null
            ? t('peers.template')
            : count === 0
              ? t('peers.buttonNone')
              : t('peers.button', { count })}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80">
        <p className="text-body font-medium text-foreground">
          {t('peers.title')}
        </p>
        <p className="mb-3 text-caption text-muted-foreground">
          {t('peers.hint')}
        </p>
        <label className="mb-2 flex items-center gap-2 text-body">
          <Checkbox
            checked={template}
            onCheckedChange={(v) => setTemplate(v === true)}
          />
          {t('peers.sameTemplate')}
        </label>
        {candidates.length === 0 ? (
          <p className="text-caption text-muted-foreground">
            {t('peers.none')}
          </p>
        ) : (
          <ul className="flex max-h-56 flex-col gap-1 overflow-y-auto">
            {candidates.map((c) => (
              <li key={c.sid}>
                <label className="flex items-center gap-2 text-body">
                  <Checkbox
                    disabled={template}
                    checked={template || chosen.includes(c.sid)}
                    onCheckedChange={(v) =>
                      setChosen((old) =>
                        v === true
                          ? [...old, c.sid]
                          : old.filter((x) => x !== c.sid),
                      )
                    }
                  />
                  <span className="min-w-0 truncate">{c.name}</span>
                </label>
              </li>
            ))}
          </ul>
        )}
        {refusal && (
          <p role="alert" className="mt-3 text-caption text-danger">
            {refusal}
          </p>
        )}
        <div className="mt-3 flex justify-end">
          <Button
            size="sm"
            disabled={save.isPending}
            onClick={() => {
              setRefusal(null)
              save.mutate()
            }}
          >
            {t('peers.save')}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  )
}
