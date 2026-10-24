// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { governanceApi, governanceKeys } from './api'
import { ApprovalDetails, ApprovalPreview } from './approval-preview'
import './i18n'
import type { ApprovalDTO, DecisionInput, DecisionVerb } from './types'

/**
 * DenyDialog records a denial of the open request. Approve once needs no dialog: the
 * command, the rule and the reach are already on screen above it (#2548). A denial is
 * confirmed here and can carry a note, which is sent in the decision body and kept in the
 * immutable trail; red is for Deny, Stop and delete (Root, 09b capture review).
 *
 * The note state lives in this component, which Radix unmounts when closed, so it
 * resets each open without a resetting effect.
 */
export function DenyDialog({
  open,
  onOpenChange,
  approval,
  onDecided,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The request on screen: what will run is shown above the note (SC sweep, 09b). */
  approval: ApprovalDTO
  /** Runs once the denial is recorded or refused as no longer pending (`useDecision`). */
  onDecided?: () => void
}) {
  if (!open) return null
  return (
    <DenyBody
      open={open}
      onOpenChange={onOpenChange}
      approval={approval}
      onDecided={onDecided}
    />
  )
}

function DenyBody({
  open,
  onOpenChange,
  approval,
  onDecided,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  approval: ApprovalDTO
  onDecided?: () => void
}) {
  const [note, setNote] = useState('')
  const { t } = useTranslation('governance')
  const mutation = useDecision(approval.id, 'reject', () => {
    onOpenChange(false)
    onDecided?.()
  })

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('decide.rejectTitle')}
      description={t('decide.rejectBody')}
      tone="danger"
      confirmLabel={t('decide.rejectConfirm')}
      pending={mutation.isPending}
      onConfirm={() =>
        mutation.mutate({
          decision: 'reject',
          ...(note.trim() ? { note: note.trim() } : {}),
        })
      }
    >
      <div className="flex min-w-0 flex-col gap-3">
        <ApprovalPreview approval={approval} />
        <ApprovalDetails approval={approval} />
      </div>
      <Field
        label={t('decide.note')}
        htmlFor="decision-note"
        description={t('decide.noteHint')}
      >
        <Textarea
          id="decision-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder={t('decide.notePlaceholder')}
          rows={3}
        />
      </Field>
    </ConfirmDialog>
  )
}

/**
 * Records ONE decision on a request: the panel's one-click Approve once and the Deny
 * dialog both send it here, so both refresh the same reads and both treat a request that
 * stopped waiting the same way. `onEnd` runs once the decision is recorded or refused
 * as no longer pending; any other failure keeps the caller where it is.
 */
export function useDecision(
  approvalId: string,
  verb: DecisionVerb,
  onEnd?: () => void,
) {
  const { t } = useTranslation('governance')
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  return usePrivilegedMutation<DecisionInput, ApprovalDTO>({
    mutationFn: (input) => governanceApi.decide(approvalId, input),
    // A request that expired or was decided meanwhile answers 409. There is nothing left
    // to decide: it says so and the queue is read again (SC 59 item 1: the dialog stayed
    // open with its Approve button after the refusal).
    onError: (err) => {
      if (!(err instanceof ApiError) || err.status !== 409) return false
      toast.warning(t('decide.notPending'), {
        description: err.message || undefined,
      })
      // The queue and the open request are read again, so neither keeps offering it.
      for (const queryKey of [
        governanceKeys.approvals(activeTenant),
        governanceKeys.approval(activeTenant, approvalId),
        governanceKeys.decisions(activeTenant, approvalId),
      ])
        void queryClient.invalidateQueries({ queryKey })
      onEnd?.()
      return true
    },
    invalidateKeys: () => [
      governanceKeys.approvals(activeTenant),
      governanceKeys.approval(activeTenant, approvalId),
      governanceKeys.decisions(activeTenant, approvalId),
    ],
    successMessage:
      verb === 'approve' ? t('decide.approved') : t('decide.rejected'),
    onDone: () => onEnd?.(),
  })
}
