// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The emergency stop (POST /killswitch). Engage is deliberately CHEAP server-side
// (admin-tier + reason; no approval gate, no AAL bar — see modules/governance/killswitch.go
// header), so the console offers ONE verb, "Stop all agents", behind one danger-toned
// confirmation; stopping a single agent sits under More. The reason is optional here: the
// engine requires one for the audit record, so an empty reason sends NO_REASON. A 409 means
// the scope is already stopped — the existing stop is surfaced (state refetch + the
// engine's message naming it), never swallowed.
import { MoreHorizontal, OctagonAlert } from 'lucide-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { usePendingCommandAction } from '@/features/navigation/command-actions'
import { agentOpsKeys } from '@/features/agentops/api'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { killswitchApi, killswitchKeys } from './api'
import './i18n'
import type { EngageKillSwitchRequest } from './types'

/** What the stop records when the operator gave no reason. One English sentence, like the
 * engine's own default re-enable reason (killswitch.go), not the operator's UI language:
 * the audit record, the post-review and the evidence pack are searched across operators. */
export const NO_REASON = 'No reason given (stopped from the console).'

/** The engage mutation both verbs share: refresh the stops and the session list on
 * success, relay an existing stop on 409, and report anything else. A failure closes the
 * confirmation and keeps what the operator typed; only a stop that landed clears it. */
function useEngage(close: () => void, clear: () => void) {
  const { t } = useTranslation(['killswitch', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()

  // The stop's own state is awaited; the session list refreshes in the background, so a
  // slow runs read never holds the confirmation open after the stop landed.
  const invalidate = () => {
    void queryClient.invalidateQueries({
      queryKey: agentOpsKeys.runs(activeTenant),
    })
    return queryClient.invalidateQueries({
      queryKey: killswitchKeys.all(activeTenant),
    })
  }

  const engage = useMutation({
    mutationFn: (input: EngageKillSwitchRequest) => killswitchApi.engage(input),
    onSuccess: async (dto) => {
      await invalidate()
      toast.success(t('engage.done'), {
        description: t('engage.doneBody', { id: dto.id }),
      })
      close()
      clear()
    },
    onError: async (err, input) => {
      close()
      if (err instanceof ApiError && err.status === 409) {
        // The scope is already stopped: refetch the live posture so the existing
        // stop shows in the banner/table, and relay the engine's message (it
        // names the stop id and the dual-control path out).
        await invalidate()
        toast.warning(t('engage.alreadyStopped'), { description: err.message })
        return
      }
      report(err, () => engage.mutate(input))
    },
  })

  /** The engine requires a reason for the audit record; none typed says so. */
  const reasonOrDefault = (reason: string) => reason.trim() || NO_REASON

  return { engage, reasonOrDefault }
}

/** The optional reason, the same field in both confirmations. */
function ReasonField({
  value,
  onChange,
}: {
  value: string
  onChange: (v: string) => void
}) {
  const { t } = useTranslation('killswitch')
  return (
    <Field
      label={t('engage.reason')}
      htmlFor="ks-reason"
      description={t('engage.reasonHint')}
    >
      <Textarea
        id="ks-reason"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        rows={2}
      />
    </Field>
  )
}

/** THE verb of the kill-switch screen: stop every agent, after one confirmation. */
export function StopAllAction({ estateStopped }: { estateStopped: boolean }) {
  const { t } = useTranslation(['killswitch', 'common'])
  const { activeTenant } = useAuth()
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState('')
  const { engage, reasonOrDefault } = useEngage(
    () => setOpen(false),
    () => setReason(''),
  )
  // HU 047 (09b, one-admin install): starting the estate again needs two OTHER admin
  // accounts to approve and one to review. With fewer than three active admin accounts
  // the confirmation says so before the stop; a roster that cannot be read says it too.
  const roster = useQuery({
    queryKey: consoleKeys.members(activeTenant),
    queryFn: () => consoleApi.listMembers(),
    enabled: open,
    retry: false,
  })
  const activeAdmins = (roster.data?.items ?? []).filter(
    (m) => m.status === 'active' && (m.role === 'admin' || m.role === 'owner'),
  ).length
  const fewAdmins = !roster.isSuccess || activeAdmins < 3
  // ⌘K "Stop all agents" (registry `killswitch.engage`, governance:killswitch:admin):
  // open the confirmation. Nothing is engaged here — the stop still needs its confirm.
  usePendingCommandAction('killswitch', 'engage', () => setOpen(true))

  return (
    <>
      <Button
        variant="destructive"
        disabled={estateStopped || engage.isPending}
        title={estateStopped ? t('banner.estateTitle') : undefined}
        onClick={() => setOpen(true)}
      >
        <OctagonAlert aria-hidden />
        {t('engage.stopAll')}
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={(o) => !engage.isPending && setOpen(o)}
        title={t('engage.confirmTitleEstate')}
        description={
          // With the two-admin line below, the body drops its own restart sentence: one
          // clear sentence about starting again, not two (Root, 047 review).
          fewAdmins
            ? t('engage.confirmBodyEstateNoRestart')
            : t('engage.confirmBodyEstate')
        }
        tone="danger"
        confirmLabel={t('engage.stopAll')}
        pending={engage.isPending}
        onConfirm={() =>
          engage.mutate({
            scope_kind: 'estate',
            reason: reasonOrDefault(reason),
          })
        }
      >
        {fewAdmins ? (
          <p
            className="text-body text-warning"
            data-slot="restart-needs-admins"
          >
            {t('engage.restartNeedsAdmins')}
          </p>
        ) : null}
        <ReasonField value={reason} onChange={setReason} />
      </ConfirmDialog>
    </>
  )
}

/** More: the single-agent stop, which needs the agent and is the rarer case. */
export function StopAgentMenu() {
  const { t } = useTranslation(['killswitch', 'common'])
  const [open, setOpen] = useState(false)
  const [agentRef, setAgentRef] = useState('')
  const [reason, setReason] = useState('')
  const { engage, reasonOrDefault } = useEngage(
    () => setOpen(false),
    () => {
      setAgentRef('')
      setReason('')
    },
  )

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t('engage.more')}
            title={t('engage.more')}
          >
            <MoreHorizontal />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => setOpen(true)}>
            {t('engage.stopOne')}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ConfirmDialog
        open={open}
        onOpenChange={(o) => !engage.isPending && setOpen(o)}
        title={t('engage.confirmTitleAgent')}
        description={t('engage.confirmBodyAgent')}
        tone="danger"
        confirmLabel={t('engage.stopAgent')}
        // The engine 400s an agent stop without its ref.
        confirmDisabled={agentRef.trim() === ''}
        pending={engage.isPending}
        onConfirm={() =>
          engage.mutate({
            scope_kind: 'agent',
            scope_ref: agentRef.trim(),
            reason: reasonOrDefault(reason),
          })
        }
      >
        <Field
          label={t('engage.agentRef')}
          htmlFor="ks-agent-ref"
          description={t('engage.agentRefHint')}
          required
        >
          <Input
            id="ks-agent-ref"
            value={agentRef}
            onChange={(e) => setAgentRef(e.target.value)}
            mono
          />
        </Field>
        <ReasonField value={reason} onChange={setReason} />
      </ConfirmDialog>
    </>
  )
}
