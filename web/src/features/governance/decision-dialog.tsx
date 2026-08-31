// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { governanceApi, governanceKeys } from './api'
import './i18n'
import type { ApprovalDTO, DecisionInput, DecisionVerb } from './types'

export interface DecisionDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  approvalId: string
  /** 'approve' | 'reject' — both are HIGH risk (danger tone) and recorded permanently. */
  verb: DecisionVerb | null
  onDone?: () => void
}

/**
 * DecisionDialog records ONE human decision (approve|reject) on a pending approval
 * request — the APPROVE / REJECT action of the HITL queue. Both verbs are HIGH risk
 * (ConfirmDialog tone="danger") and irreversible in the immutable decision trail, so
 * the operator confirms deliberately; the optional note is collected here and sent in
 * the decision body. usePrivilegedMutation invalidates the queue + this request +
 * its decision trail, then toasts and closes (a 403 — e.g. separation-of-duties —
 * surfaces as a calm "not authorized" warning).
 *
 * The note state lives in this component, which Radix unmounts when closed, so it
 * resets each open without a resetting effect.
 */
export function DecisionDialog({
  open,
  onOpenChange,
  approvalId,
  verb,
  onDone,
}: DecisionDialogProps) {
  if (!open || !verb) return null
  return (
    <DecisionBody
      open={open}
      onOpenChange={onOpenChange}
      approvalId={approvalId}
      verb={verb}
      onDone={onDone}
    />
  )
}

function DecisionBody({
  open,
  onOpenChange,
  approvalId,
  verb,
  onDone,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  approvalId: string
  verb: DecisionVerb
  onDone?: () => void
}) {
  const { t } = useTranslation('governance')
  const { activeTenant } = useAuth()
  const [note, setNote] = useState('')

  const mutation = usePrivilegedMutation<DecisionInput, ApprovalDTO>({
    mutationFn: (input) => governanceApi.decide(approvalId, input),
    invalidateKeys: () => [
      governanceKeys.approvals(activeTenant),
      governanceKeys.approval(activeTenant, approvalId),
      governanceKeys.decisions(activeTenant, approvalId),
    ],
    successMessage:
      verb === 'approve' ? t('decide.approved') : t('decide.rejected'),
    onDone: () => {
      onOpenChange(false)
      onDone?.()
    },
  })

  const isApprove = verb === 'approve'

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={isApprove ? t('decide.approveTitle') : t('decide.rejectTitle')}
      description={isApprove ? t('decide.approveBody') : t('decide.rejectBody')}
      tone="danger"
      confirmLabel={
        isApprove ? t('decide.approveConfirm') : t('decide.rejectConfirm')
      }
      pending={mutation.isPending}
      onConfirm={() =>
        mutation.mutate({
          decision: verb,
          ...(note.trim() ? { note: note.trim() } : {}),
        })
      }
    >
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
