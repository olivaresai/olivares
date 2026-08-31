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
import { knowledgeApi, knowledgeKeys } from './api'
import './i18n'
import type {
  AddRevisionResponse,
  PromptDTO,
  PromptInput,
  RevisionInput,
} from './types'

/** Create a brand-new prompt (records rev 1). */
export interface PromptEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function PromptEditorDialog({
  open,
  onOpenChange,
}: PromptEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && <PromptForm onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function PromptForm({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()

  const [name, setName] = useState('')
  const [template, setTemplate] = useState('')
  const [label, setLabel] = useState('')
  const [note, setNote] = useState('')

  const valid = name.trim().length > 0 && template.trim().length > 0

  const mutation = usePrivilegedMutation<PromptInput, PromptDTO>({
    mutationFn: (input) => knowledgeApi.createPrompt(input),
    invalidateKeys: () => [knowledgeKeys.prompts(activeTenant)],
    successMessage: t('promptEditor.created'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    const payload: PromptInput = {
      name: name.trim(),
      template,
      ...(label.trim() ? { label: label.trim() } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('promptEditor.createTitle')}</DialogTitle>
        <DialogDescription>{t('promptEditor.createBody')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field label={t('promptEditor.name')} htmlFor="prompt-name" required>
          <Input
            id="prompt-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            mono
          />
        </Field>
        <Field
          label={t('promptEditor.template')}
          htmlFor="prompt-template"
          description={t('promptEditor.templateHint')}
          required
        >
          <Textarea
            id="prompt-template"
            value={template}
            rows={6}
            mono
            onChange={(e) => setTemplate(e.target.value)}
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('promptEditor.label')} htmlFor="prompt-label">
            <Input
              id="prompt-label"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
            />
          </Field>
          <Field label={t('promptEditor.note')} htmlFor="prompt-note">
            <Input
              id="prompt-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
            />
          </Field>
        </div>
      </div>

      <AuditNotice />

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
          {t('promptEditor.create')}
        </Button>
      </DialogFooter>
    </>
  )
}

/** Append a new immutable revision to an existing prompt (advances current_rev). */
export interface RevisionEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  prompt: PromptDTO
}

export function RevisionEditorDialog({
  open,
  onOpenChange,
  prompt,
}: RevisionEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && (
          <RevisionForm prompt={prompt} onClose={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function RevisionForm({
  prompt,
  onClose,
}: {
  prompt: PromptDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()

  const [template, setTemplate] = useState('')
  const [label, setLabel] = useState('')
  const [note, setNote] = useState('')

  const valid = template.trim().length > 0

  const mutation = usePrivilegedMutation<RevisionInput, AddRevisionResponse>({
    mutationFn: (input) => knowledgeApi.addRevision(prompt.id, input),
    invalidateKeys: () => [
      knowledgeKeys.prompt(activeTenant, prompt.id),
      knowledgeKeys.revisions(activeTenant, prompt.id),
      knowledgeKeys.prompts(activeTenant),
    ],
    successMessage: t('promptEditor.added'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    const payload: RevisionInput = {
      template,
      ...(label.trim() ? { label: label.trim() } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('promptEditor.addTitle')}</DialogTitle>
        <DialogDescription>
          {t('promptEditor.addBody', { name: prompt.name })}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('promptEditor.template')}
          htmlFor="rev-template"
          description={t('promptEditor.templateHint')}
          required
        >
          <Textarea
            id="rev-template"
            value={template}
            rows={6}
            mono
            onChange={(e) => setTemplate(e.target.value)}
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('promptEditor.label')} htmlFor="rev-label">
            <Input
              id="rev-label"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
            />
          </Field>
          <Field label={t('promptEditor.note')} htmlFor="rev-note">
            <Input
              id="rev-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
            />
          </Field>
        </div>
      </div>

      <AuditNotice />

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
          {t('promptEditor.add')}
        </Button>
      </DialogFooter>
    </>
  )
}

function AuditNotice() {
  const { t } = useTranslation('common')
  return (
    <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <ScrollText className="size-3.5 shrink-0" aria-hidden />
      {t('privileged.auditedNotice')}
    </p>
  )
}
