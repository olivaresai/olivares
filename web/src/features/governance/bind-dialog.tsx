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
import { governanceApi, governanceKeys } from './api'
import './i18n'
import type { BindMode, BindRequest, BindResponse } from './types'

export interface BindDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Prefill the agent id (e.g. from a binding row). */
  agentId?: string
}

/**
 * BindDialog binds an agent to its NHI identity (POST /agents/{agentID}/identity) —
 * a HIGH-risk privileged action: it sets the access-map attribution bridge, and
 * binding to an already-bound identity collapses per-agent attribution. The form is
 * the deliberate confirmation surface (audit notice + explicit submit) and exposes
 * the three mutually-exclusive modes (existing internal id, directory ref, or mint a
 * fresh NHI), plus the allow_unknown gate. The privileged mutation invalidates the
 * bindings list, toasts, and closes.
 *
 * The form mounts fresh each open (Radix unmounts closed content), so plain useState
 * initializers seed it — no resetting effect.
 */
export function BindDialog({ open, onOpenChange, agentId }: BindDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
        {open && (
          <BindForm
            agentId={agentId ?? ''}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function BindForm({
  agentId: initialAgentId,
  onClose,
}: {
  agentId: string
  onClose: () => void
}) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant } = useAuth()

  const [agentId, setAgentId] = useState(initialAgentId)
  const [mode, setMode] = useState<BindMode>('identity_ref')
  const [identityId, setIdentityId] = useState('')
  const [identityRef, setIdentityRef] = useState('')
  const [allowUnknown, setAllowUnknown] = useState(false)

  const modeValueValid =
    mode === 'mint' ||
    (mode === 'identity_id' && identityId.trim().length > 0) ||
    (mode === 'identity_ref' && identityRef.trim().length > 0)
  const valid = agentId.trim().length > 0 && modeValueValid

  const mutation = usePrivilegedMutation<BindRequest, BindResponse>({
    mutationFn: (input) =>
      governanceApi.bindAgentIdentity(agentId.trim(), input),
    invalidateKeys: () => [governanceKeys.bindings(activeTenant)],
    successMessage: t('bind.done'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    const input: BindRequest = {
      ...(mode === 'identity_id' ? { identity_id: identityId.trim() } : {}),
      ...(mode === 'identity_ref' ? { identity_ref: identityRef.trim() } : {}),
      ...(mode === 'mint' ? { mint: true } : {}),
      ...(allowUnknown ? { allow_unknown: true } : {}),
    }
    mutation.mutate(input)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('bind.title')}</DialogTitle>
        <DialogDescription>{t('bind.body')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field label={t('bindings.agent')} htmlFor="bind-agent" required>
          <Input
            id="bind-agent"
            value={agentId}
            onChange={(e) => setAgentId(e.target.value)}
            disabled={!!initialAgentId}
            mono
          />
        </Field>

        <Field label={t('bindings.identity')} htmlFor="bind-mode">
          <Select value={mode} onValueChange={(v) => setMode(v as BindMode)}>
            <SelectTrigger id="bind-mode">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="identity_ref">
                {t('bindings.identityRef')}
              </SelectItem>
              <SelectItem value="identity_id">
                {t('bindings.identity')}
              </SelectItem>
              <SelectItem value="mint">
                {t('identities.principalTypeNhi')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        {mode === 'identity_ref' && (
          <Field label={t('bindings.identityRef')} htmlFor="bind-ref" required>
            <Input
              id="bind-ref"
              value={identityRef}
              onChange={(e) => setIdentityRef(e.target.value)}
              mono
            />
          </Field>
        )}
        {mode === 'identity_id' && (
          <Field label={t('bindings.identity')} htmlFor="bind-id" required>
            <Input
              id="bind-id"
              value={identityId}
              onChange={(e) => setIdentityId(e.target.value)}
              mono
            />
          </Field>
        )}

        <div className="flex items-center gap-2">
          <Checkbox
            id="bind-allow-unknown"
            checked={allowUnknown}
            onCheckedChange={(c) => setAllowUnknown(c === true)}
          />
          <Label htmlFor="bind-allow-unknown">
            {t('identities.principalTypeUnknown')}
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
          {t('bind.confirm')}
        </Button>
      </DialogFooter>
    </>
  )
}
