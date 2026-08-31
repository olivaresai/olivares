// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ScrollText } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { knowledgeApi, knowledgeKeys } from './api'
import './i18n'
import {
  CONTEXT_STRATEGIES,
  SCOPE_KINDS,
  type ContextPolicyDTO,
  type ContextPolicyInput,
  type ContextStrategy,
  type ScopeKind,
} from './types'

export interface ContextEditorDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Existing policy to edit (upsert by scope); omit to create. */
  policy?: ContextPolicyDTO | null
}

export function ContextEditorDialog({
  open,
  onOpenChange,
  policy,
}: ContextEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && (
          <ContextForm
            policy={policy ?? null}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function ContextForm({
  policy,
  onClose,
}: {
  policy: ContextPolicyDTO | null
  onClose: () => void
}) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = !!policy?.id

  const [scopeKind, setScopeKind] = useState<ScopeKind>(
    policy?.scope_kind ?? 'agent',
  )
  const [scopeRef, setScopeRef] = useState(policy?.scope_ref ?? '')
  const [maxTokens, setMaxTokens] = useState(
    policy?.max_tokens != null ? String(policy.max_tokens) : '',
  )
  const [strategy, setStrategy] = useState<ContextStrategy>(
    policy?.strategy ?? 'truncate',
  )
  const [redactionRequired, setRedactionRequired] = useState(
    policy?.redaction_required ?? false,
  )

  const valid = scopeRef.trim().length > 0

  const mutation = usePrivilegedMutation<ContextPolicyInput, ContextPolicyDTO>({
    mutationFn: (input) => knowledgeApi.upsertContextPolicy(input),
    invalidateKeys: () => [knowledgeKeys.contextPolicies(activeTenant)],
    successMessage: t('contextEditor.done'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    const parsed = Number.parseInt(maxTokens, 10)
    const payload: ContextPolicyInput = {
      scope_kind: scopeKind,
      scope_ref: scopeRef.trim(),
      strategy,
      redaction_required: redactionRequired,
      ...(Number.isFinite(parsed) && parsed > 0 ? { max_tokens: parsed } : {}),
    }
    mutation.mutate(payload)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('contextEditor.title')}</DialogTitle>
        <DialogDescription>
          {t('contextEditor.body', {
            scopeKind,
            scopeRef: scopeRef || '…',
          })}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('contextEditor.scopeKind')} htmlFor="ctx-scope-kind">
            <Select
              value={scopeKind}
              onValueChange={(v) => setScopeKind(v as ScopeKind)}
            >
              <SelectTrigger id="ctx-scope-kind">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SCOPE_KINDS.map((s) => (
                  <SelectItem key={s} value={s}>
                    {t(`context.scopeKinds.${s}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('contextEditor.scopeRef')}
            htmlFor="ctx-scope-ref"
            required
          >
            <Input
              id="ctx-scope-ref"
              value={scopeRef}
              onChange={(e) => setScopeRef(e.target.value)}
              disabled={isEdit}
              mono
            />
          </Field>
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('contextEditor.maxTokens')} htmlFor="ctx-max-tokens">
            <Input
              id="ctx-max-tokens"
              type="number"
              min={0}
              value={maxTokens}
              onChange={(e) => setMaxTokens(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('contextEditor.strategy')} htmlFor="ctx-strategy">
            <Select
              value={strategy}
              onValueChange={(v) => setStrategy(v as ContextStrategy)}
            >
              <SelectTrigger id="ctx-strategy">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CONTEXT_STRATEGIES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {t(`context.strategies.${s}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>

        <div className="flex items-center gap-2">
          <Checkbox
            id="ctx-redaction"
            checked={redactionRequired}
            onCheckedChange={(c) => setRedactionRequired(c === true)}
          />
          <Label htmlFor="ctx-redaction">
            {t('contextEditor.redactionRequired')}
          </Label>
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
          {t('contextEditor.upsert')}
        </Button>
      </DialogFooter>
    </>
  )
}
