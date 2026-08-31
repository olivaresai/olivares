// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ScrollText } from 'lucide-react'
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
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { catalogApi, catalogKeys } from './api'
import './i18n'
import type { EntryDTO, InstanceDTO, InstantiateInput } from './types'

export interface InstantiateDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The approved entry to instantiate. */
  entry: EntryDTO
  /** Called with the created instance so the caller can surface/link it. */
  onCreated?: (instance: InstanceDTO) => void
}

/**
 * InstantiateDialog launches a governed self-service instantiation REQUEST from an
 * approved entry (MEDIUM risk). The copy is explicit that this is NOT a provisioning
 * action: it only creates a governance request (status 'requested') — the approval
 * DECISION is governance's and provisioning is deployment's. The form is
 * the confirmation surface (deliberate submit + audit notice), then runs the
 * privileged mutation. Mounts fresh in the dialog so its state seeds from defaults.
 */
export function InstantiateDialog({
  open,
  onOpenChange,
  entry,
  onCreated,
}: InstantiateDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        {open && (
          <InstantiateForm
            entry={entry}
            onClose={() => onOpenChange(false)}
            onCreated={onCreated}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function InstantiateForm({
  entry,
  onClose,
  onCreated,
}: {
  entry: EntryDTO
  onClose: () => void
  onCreated?: (instance: InstanceDTO) => void
}) {
  const { t } = useTranslation(['catalog', 'common'])
  const { activeTenant } = useAuth()

  const [name, setName] = useState('')
  const [targetRef, setTargetRef] = useState('')
  const [note, setNote] = useState('')

  const valid = name.trim().length > 0

  const mutation = usePrivilegedMutation<InstantiateInput, InstanceDTO>({
    mutationFn: (input) => catalogApi.instantiateEntry(entry.id!, input),
    invalidateKeys: () => [catalogKeys.instances(activeTenant)],
    successMessage: t('instantiate.requested'),
    onDone: (instance) => {
      onCreated?.(instance)
      onClose()
    },
  })

  function submit() {
    if (!valid) return
    const payload: InstantiateInput = {
      name: name.trim(),
      ...(targetRef.trim() ? { target_ref: targetRef.trim() } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('instantiate.title')}</DialogTitle>
        <DialogDescription>{t('instantiate.body')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('instantiate.name')}
          htmlFor="inst-name"
          description={t('instantiate.nameHint')}
          required
        >
          <Input
            id="inst-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field
          label={t('instantiate.targetRef')}
          htmlFor="inst-target"
          description={t('instantiate.targetRefHint')}
        >
          <Input
            id="inst-target"
            value={targetRef}
            onChange={(e) => setTargetRef(e.target.value)}
            mono
          />
        </Field>
        <Field label={t('instantiate.note')} htmlFor="inst-note">
          <Textarea
            id="inst-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
          />
        </Field>
      </div>

      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <ScrollText className="size-3.5 shrink-0" aria-hidden />
        {t('common:privileged.auditedNotice')}
      </p>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('instantiate.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
