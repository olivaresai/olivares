// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Plus, ScrollText, Trash2 } from 'lucide-react'
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
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { looksLikeCredential } from '@/lib/credentials'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { knowledgeApi, knowledgeKeys } from './api'
import './i18n'
import {
  CLASSIFICATIONS,
  EMBED_POLICIES,
  RESIDENCIES,
  type Classification,
  type EmbedPolicy,
  type KbDTO,
  type KbInput,
  type Residency,
} from './types'

export interface KbEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Existing KB to edit; omit/undefined to create. */
  kb?: KbDTO | null
}

/**
 * KbEditorDialog is the privileged create/edit (governance) form for a knowledge
 * base. The form itself is the confirmation surface: it carries the audit-ledger
 * notice and a deliberate submit, then runs the privileged mutation (invalidate →
 * toast → close). ACL entries are permission REFERENCES (handles), never secret
 * values; the same inline-credential guard warns if a handle looks like a credential.
 *
 * The form lives in a child that mounts fresh each time the dialog opens (Radix
 * unmounts closed content), so its initial state is seeded from props with plain
 * useState initializers — no resetting effect (react-hooks/set-state-in-effect).
 */
export function KbEditorDialog({
  open,
  onOpenChange,
  kb,
}: KbEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && <KbForm kb={kb ?? null} onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

let aclKeySeq = 0

function KbForm({ kb, onClose }: { kb: KbDTO | null; onClose: () => void }) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!kb?.id

  const [name, setName] = useState(kb?.name ?? '')
  const [classification, setClassification] = useState<Classification>(
    kb?.classification ?? 'internal',
  )
  const [residency, setResidency] = useState<Residency>(
    kb?.residency_region ?? 'global',
  )
  const [embedPolicy, setEmbedPolicy] = useState<EmbedPolicy>(
    kb?.embed_policy ?? 'auto',
  )
  const [acl, setAcl] = useState<{ _k: string; value: string }[]>(() =>
    (kb?.default_acl ?? []).map((value) => ({ _k: `a${aclKeySeq++}`, value })),
  )

  const aclWarn = acl.some((a) => looksLikeCredential(a.value))
  const valid = name.trim().length > 0 && !aclWarn

  const mutation = usePrivilegedMutation<KbInput, KbDTO>({
    mutationFn: (input) =>
      isEdit
        ? knowledgeApi.updateKb(kb!.id, input)
        : knowledgeApi.createKb(input),
    invalidateKeys: () => [
      knowledgeKeys.kbs(activeTenant),
      ...(isEdit ? [knowledgeKeys.kb(activeTenant, kb!.id)] : []),
    ],
    successMessage: isEdit ? t('kbEditor.updated') : t('kbEditor.created'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    const defaultAcl = acl.map((a) => a.value.trim()).filter(Boolean)
    const payload: KbInput = {
      name: name.trim(),
      classification,
      residency_region: residency,
      embed_policy: embedPolicy,
      default_acl: defaultAcl,
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit ? t('kbEditor.editTitle') : t('kbEditor.createTitle')}
        </DialogTitle>
        <DialogDescription>
          {isEdit
            ? t('kbEditor.editBody', { name: kb?.name })
            : t('kbEditor.createBody')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field label={t('kbEditor.name')} htmlFor="kb-name" required>
          <Input
            id="kb-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label={t('common.classification')}
            htmlFor="kb-classification"
            description={t('kbEditor.classificationHint')}
          >
            <Select
              value={classification}
              onValueChange={(v) => setClassification(v as Classification)}
            >
              <SelectTrigger id="kb-classification">
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
          <Field
            label={t('common.residency')}
            htmlFor="kb-residency"
            description={t('kbEditor.residencyHint')}
          >
            <Select
              value={residency}
              onValueChange={(v) => setResidency(v as Residency)}
            >
              <SelectTrigger id="kb-residency">
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
        </div>

        <Field
          label={t('common.embedPolicy')}
          htmlFor="kb-embed-policy"
          description={t('kbEditor.embedPolicyHint')}
        >
          <Select
            value={embedPolicy}
            onValueChange={(v) => setEmbedPolicy(v as EmbedPolicy)}
          >
            <SelectTrigger id="kb-embed-policy">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {EMBED_POLICIES.map((p) => (
                <SelectItem key={p} value={p}>
                  {t(`common.embedPolicies.${p}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        {/* ACL — permission references (handles), never secret values. */}
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <Label>{t('common.acl')}</Label>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() =>
                setAcl((a) => [...a, { _k: `a${aclKeySeq++}`, value: '' }])
              }
            >
              <Plus />
              {t('common.addRef')}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">{t('common.aclHint')}</p>
          {acl.length === 0 ? (
            <p className="rounded-md border border-dashed border-border px-3 py-2 text-xs text-muted-foreground">
              {t('common.aclNone')}
            </p>
          ) : (
            <div className="flex flex-col gap-2">
              {acl.map((a, i) => {
                const warn = looksLikeCredential(a.value)
                return (
                  <div key={a._k} className="flex flex-col gap-1">
                    <div className="flex items-center gap-2">
                      <Input
                        aria-label={t('common.acl')}
                        value={a.value}
                        onChange={(e) =>
                          setAcl((arr) =>
                            arr.map((x, j) =>
                              j === i ? { ...x, value: e.target.value } : x,
                            ),
                          )
                        }
                        aria-invalid={warn || undefined}
                        mono
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        aria-label={t('common.removeRef')}
                        onClick={() =>
                          setAcl((arr) => arr.filter((_, j) => j !== i))
                        }
                      >
                        <Trash2 />
                      </Button>
                    </div>
                    {warn && (
                      <p role="alert" className="text-xs text-danger">
                        {t('common.credentialWarning')}
                      </p>
                    )}
                  </div>
                )
              })}
            </div>
          )}
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
          {isEdit ? t('kbEditor.save') : t('kbEditor.create')}
        </Button>
      </DialogFooter>
    </>
  )
}
