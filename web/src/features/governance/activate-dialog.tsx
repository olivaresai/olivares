// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ShieldAlert } from 'lucide-react'
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
import { governanceApi, governanceKeys } from './api'
import './i18n'
import type { ActivateBreakGlassInput, BreakGlassDTO } from './types'

type ScopeKind = 'all' | 'exact' | 'prefix'

const DEFAULT_DURATION_SECONDS = 3_600
const MIN_DURATION_SECONDS = 1
const MAX_DURATION_SECONDS = 86_400
const MAX_NOTE_LENGTH = 4_096
const MAX_ACTION_LENGTH = 128

export interface ActivateBreakGlassDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * Deliberate activation surface for the emergency escape valve. The form mirrors
 * the server's hard bounds and requires an accountable reason. Its warning is part
 * of the submit surface: an operator cannot activate without first seeing exactly
 * which control is bypassed and that a different user ACCOUNT must close the review
 * loop — the engine enforces distinct accounts, not provably distinct people.
 */
export function ActivateBreakGlassDialog({
  open,
  onOpenChange,
}: ActivateBreakGlassDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-xl overflow-y-auto">
        {open && <ActivateForm onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function ActivateForm({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant } = useAuth()
  const [reason, setReason] = useState('')
  const [scopeKind, setScopeKind] = useState<ScopeKind>('all')
  const [scopeValue, setScopeValue] = useState('')
  const [duration, setDuration] = useState(String(DEFAULT_DURATION_SECONDS))

  const durationNumber = Number(duration)
  const durationValid =
    Number.isInteger(durationNumber) &&
    durationNumber >= MIN_DURATION_SECONDS &&
    durationNumber <= MAX_DURATION_SECONDS
  const scopeTrimmed = scopeValue.trim()
  const scopeValid =
    scopeKind === 'all' ||
    (scopeTrimmed.length > 0 &&
      scopeTrimmed !== '*' &&
      scopeTrimmed.length <=
        (scopeKind === 'prefix' ? MAX_ACTION_LENGTH - 1 : MAX_ACTION_LENGTH) &&
      (scopeKind === 'prefix'
        ? !scopeTrimmed.slice(0, -1).includes('*')
        : !scopeTrimmed.includes('*')))
  const valid = reason.trim().length > 0 && durationValid && scopeValid

  const mutation = usePrivilegedMutation<
    ActivateBreakGlassInput,
    BreakGlassDTO
  >({
    mutationFn: (input) => governanceApi.activateBreakGlass(input),
    invalidateKeys: () => [governanceKeys.breakGlass(activeTenant)],
    successMessage: t('breakGlass.activate.done'),
    onDone: onClose,
  })

  function boundedDuration(): number {
    if (!Number.isFinite(durationNumber)) return DEFAULT_DURATION_SECONDS
    return Math.min(
      MAX_DURATION_SECONDS,
      Math.max(MIN_DURATION_SECONDS, Math.round(durationNumber)),
    )
  }

  function clampDuration() {
    if (duration === '') return
    setDuration(String(boundedDuration()))
  }

  function matchAction(): string | undefined {
    if (scopeKind === 'all') return undefined
    if (scopeKind === 'exact') return scopeTrimmed
    return `${scopeTrimmed.replace(/\*+$/, '')}*`
  }

  function submit() {
    if (!valid) return
    const match = matchAction()
    mutation.mutate({
      reason: reason.trim(),
      expires_in_seconds: boundedDuration(),
      ...(match ? { match_action: match } : {}),
    })
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('breakGlass.activate.title')}</DialogTitle>
        <DialogDescription>{t('breakGlass.activate.body')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div
          role="note"
          className="flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft p-3 text-sm text-foreground"
        >
          <ShieldAlert
            className="mt-0.5 size-4 shrink-0 text-danger"
            aria-hidden
          />
          <span>
            {t('breakGlass.activate.warning')}{' '}
            {/* The server also demands an actively recorded session (412 if
                not). The console cannot see that state, so it must at least
                DISCLOSE the requirement rather than let the operator discover
                it by being refused mid-incident. */}
            {t('breakGlass.activationRecordingRequired')}
          </span>
        </div>

        <Field
          label={t('breakGlass.activate.reason')}
          htmlFor="break-glass-reason"
          description={t('breakGlass.activate.reasonHint')}
          required
        >
          <Textarea
            id="break-glass-reason"
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            maxLength={MAX_NOTE_LENGTH}
            rows={3}
          />
        </Field>

        <Field
          label={t('breakGlass.activate.scope')}
          htmlFor="break-glass-scope"
          description={t('breakGlass.activate.scopeHint')}
        >
          <Select
            value={scopeKind}
            onValueChange={(value) => setScopeKind(value as ScopeKind)}
          >
            <SelectTrigger id="break-glass-scope">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">
                {t('breakGlass.activate.scopeAll')}
              </SelectItem>
              <SelectItem value="exact">
                {t('breakGlass.activate.scopeExact')}
              </SelectItem>
              <SelectItem value="prefix">
                {t('breakGlass.activate.scopePrefix')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        {scopeKind !== 'all' && (
          <Field
            label={
              scopeKind === 'exact'
                ? t('breakGlass.activate.exactAction')
                : t('breakGlass.activate.actionPrefix')
            }
            htmlFor="break-glass-action"
            description={
              scopeKind === 'exact'
                ? t('breakGlass.activate.exactActionHint')
                : t('breakGlass.activate.actionPrefixHint')
            }
            error={
              scopeValue.length > 0 && !scopeValid
                ? t('breakGlass.activate.invalidScope')
                : undefined
            }
            required
          >
            <Input
              id="break-glass-action"
              value={scopeValue}
              onChange={(event) => setScopeValue(event.target.value)}
              maxLength={
                scopeKind === 'prefix'
                  ? MAX_ACTION_LENGTH - 1
                  : MAX_ACTION_LENGTH
              }
              mono
            />
          </Field>
        )}

        <Field
          label={t('breakGlass.activate.duration')}
          htmlFor="break-glass-duration"
          description={t('breakGlass.activate.durationHint')}
          error={
            duration.length > 0 && !durationValid
              ? t('breakGlass.activate.invalidDuration')
              : undefined
          }
          required
        >
          <Input
            id="break-glass-duration"
            type="number"
            min={MIN_DURATION_SECONDS}
            max={MAX_DURATION_SECONDS}
            step={1}
            value={duration}
            onChange={(event) => setDuration(event.target.value)}
            onBlur={clampDuration}
            mono
          />
        </Field>
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="destructive-solid"
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('breakGlass.activate.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
