// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Dialog to trigger a new backup. After the mutation succeeds the dialog pivots to
// the SSE-connected JobProgress display so the operator watches the backup run to
// completion (or failure) without leaving the dialog.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { PassphraseStrength } from '@/components/ui/passphrase-strength'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { passphraseBelowFloor } from '@/lib/passphrase'
import { drApi, drKeys } from './api'
import { JobProgress } from './job-progress'

export interface BackupTriggerDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function BackupTriggerDialog({
  open,
  onOpenChange,
}: BackupTriggerDialogProps) {
  const { t } = useTranslation('backups')
  const [notes, setNotes] = useState('')
  const [passphrase, setPassphrase] = useState('')
  const [jobId, setJobId] = useState<string | null>(null)

  const trigger = usePrivilegedMutation({
    mutationFn: () =>
      drApi.createBackup({
        notes: notes.trim(),
        passphrase: passphrase.trim(),
      }),
    invalidateKeys: [drKeys.backups(), drKeys.jobs()],
    successMessage: t('trigger.started'),
    onDone: (data) => setJobId(data.job_id),
  })

  const close = () => {
    onOpenChange(false)
    // Reset state after the dialog closes so a re-open starts fresh.
    setNotes('')
    setPassphrase('')
    setJobId(null)
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('trigger.title')}</DialogTitle>
          <DialogDescription>{t('trigger.description')}</DialogDescription>
        </DialogHeader>

        {jobId ? (
          <JobProgress jobId={jobId} onFinished={() => {}} />
        ) : (
          <div className="flex flex-col gap-4">
            <Field label={t('trigger.notes')} htmlFor="backup-notes">
              <Textarea
                id="backup-notes"
                value={notes}
                onChange={(e) => setNotes(e.target.value)}
                placeholder={t('trigger.notesPlaceholder')}
                rows={2}
              />
            </Field>

            <Field
              label={t('trigger.passphrase')}
              htmlFor="backup-passphrase"
              description={t('trigger.passphraseDescription')}
            >
              <Input
                id="backup-passphrase"
                type="password"
                value={passphrase}
                onChange={(e) => setPassphrase(e.target.value)}
                autoComplete="off"
              />
              {/* Client mirror of the backend >=12-rune floor + honest
                  strength hint — the backend stays the source of truth. */}
              <PassphraseStrength value={passphrase} className="mt-1.5" />
            </Field>
          </div>
        )}

        <DialogFooter>
          <Button
            variant="secondary"
            onClick={close}
            disabled={trigger.isPending}
          >
            {jobId ? t('actions.close') : t('actions.cancel')}
          </Button>
          {!jobId && (
            <Button
              variant="primary"
              onClick={() => trigger.mutate()}
              disabled={
                trigger.isPending ||
                !passphrase.trim() ||
                passphraseBelowFloor(passphrase.trim())
              }
            >
              {trigger.isPending && <Spinner size="sm" aria-hidden />}
              {t('trigger.start')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
