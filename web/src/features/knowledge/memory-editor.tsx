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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { knowledgeApi, knowledgeKeys } from './api'
import './i18n'
import {
  CLASSIFICATIONS,
  RESIDENCIES,
  type Classification,
  type MemoryDTO,
  type MemoryInput,
  type Residency,
} from './types'

export interface MemoryEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Prefill the agent ref (e.g. from the active filter). */
  agentRef?: string
}

export function MemoryEditorDialog({
  open,
  onOpenChange,
  agentRef,
}: MemoryEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && (
          <MemoryForm agentRef={agentRef} onClose={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function MemoryForm({
  agentRef,
  onClose,
}: {
  agentRef?: string
  onClose: () => void
}) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()

  const [agent, setAgent] = useState(agentRef ?? '')
  const [key, setKey] = useState('')
  const [content, setContent] = useState('')
  const [classification, setClassification] =
    useState<Classification>('internal')
  const [residency, setResidency] = useState<Residency>('global')
  const [ttl, setTtl] = useState('')

  const valid = agent.trim().length > 0 && key.trim().length > 0

  const mutation = usePrivilegedMutation<MemoryInput, MemoryDTO>({
    mutationFn: (input) => knowledgeApi.writeMemory(input),
    invalidateKeys: () => [knowledgeKeys.memory(activeTenant)],
    successMessage: t('memoryEditor.done'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    const parsedTtl = Number.parseInt(ttl, 10)
    const payload: MemoryInput = {
      agent_ref: agent.trim(),
      key: key.trim(),
      classification,
      residency_region: residency,
      ...(content.trim() ? { content } : {}),
      ...(Number.isFinite(parsedTtl) && parsedTtl > 0
        ? { ttl_seconds: parsedTtl }
        : {}),
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('memoryEditor.title')}</DialogTitle>
        <DialogDescription>{t('memoryEditor.body')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('memoryEditor.agentRef')}
            htmlFor="mem-agent"
            required
          >
            <Input
              id="mem-agent"
              value={agent}
              onChange={(e) => setAgent(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('memoryEditor.key')} htmlFor="mem-key" required>
            <Input
              id="mem-key"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              mono
            />
          </Field>
        </div>

        <Field
          label={t('memoryEditor.content')}
          htmlFor="mem-content"
          description={t('memoryEditor.contentHint')}
        >
          <Textarea
            id="mem-content"
            value={content}
            rows={4}
            onChange={(e) => setContent(e.target.value)}
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t('common.classification')} htmlFor="mem-class">
            <Select
              value={classification}
              onValueChange={(v) => setClassification(v as Classification)}
            >
              <SelectTrigger id="mem-class">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CLASSIFICATIONS.map((c) => (
                  <SelectItem key={c} value={c}>
                    {t(`common.classifications.${c}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('common.residency')} htmlFor="mem-residency">
            <Select
              value={residency}
              onValueChange={(v) => setResidency(v as Residency)}
            >
              <SelectTrigger id="mem-residency">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {RESIDENCIES.map((r) => (
                  <SelectItem key={r} value={r}>
                    {t(`common.residencies.${r}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('memoryEditor.ttl')}
            htmlFor="mem-ttl"
            description={t('memoryEditor.ttlHint')}
          >
            <Input
              id="mem-ttl"
              type="number"
              min={0}
              value={ttl}
              onChange={(e) => setTtl(e.target.value)}
              mono
            />
          </Field>
        </div>
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
          {t('memoryEditor.write')}
        </Button>
      </DialogFooter>
    </>
  )
}
