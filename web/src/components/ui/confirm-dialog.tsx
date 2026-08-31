// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ScrollText } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from './button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from './dialog'
import { Input } from './input'
import { Label } from './label'
import { Spinner } from './spinner'

/**
 * ConfirmDialog is THE privileged-action gate for the control plane: every mutating
 * operation (approve, deploy, rollback, grant, delete, …) routes through it so the
 * operator never triggers a state change by a single click. It always shows the
 * "recorded in the audit ledger" notice (docs/SECURITY-HARDENING.md), supports a `danger` tone for
 * irreversible actions, and a `confirmPhrase` the operator must type for the
 * highest-risk steps (rollback / retire / purge). Pair it with usePrivilegedMutation,
 * which runs the mutation, invalidates the cache, toasts the result and closes here.
 */
export interface ConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  description?: ReactNode
  /** Extra body content — e.g. a short summary of exactly what will change. */
  children?: ReactNode
  confirmLabel?: ReactNode
  cancelLabel?: ReactNode
  /** `danger` renders a solid destructive confirm for irreversible actions. */
  tone?: 'default' | 'danger'
  /** When set, the operator must type this exact phrase before confirm enables. */
  confirmPhrase?: string
  /** Lets the caller deny confirmation when its own action contract is unsatisfied. */
  confirmDisabled?: boolean
  /** Disables confirm + cancel and shows a spinner while the mutation runs. */
  pending?: boolean
  /** Hide the audit-ledger notice (default: shown). */
  hideAuditNotice?: boolean
  onConfirm: () => void
}

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  confirmLabel,
  cancelLabel,
  tone = 'default',
  confirmPhrase,
  confirmDisabled: callerDisabled = false,
  pending = false,
  hideAuditNotice = false,
  onConfirm,
}: ConfirmDialogProps) {
  const { t } = useTranslation('common')
  const [typed, setTyped] = useState('')

  // Reset the typed phrase whenever the dialog toggles, so a prior attempt can't
  // pre-satisfy the guard on the next, different action. Done in render (the
  // sanctioned "adjust state when a prop changes" pattern), not an effect.
  const [prevOpen, setPrevOpen] = useState(open)
  if (open !== prevOpen) {
    setPrevOpen(open)
    if (typed !== '') setTyped('')
  }

  const phraseSatisfied = !confirmPhrase || typed.trim() === confirmPhrase
  const confirmDisabled = callerDisabled || pending || !phraseSatisfied

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (pending ? undefined : onOpenChange(o))}
    >
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description != null && (
            <DialogDescription>{description}</DialogDescription>
          )}
        </DialogHeader>

        {children != null && (
          <div className="text-sm text-muted-foreground">{children}</div>
        )}

        {confirmPhrase != null && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="confirm-phrase">
              {t('privileged.typeToConfirm', { phrase: confirmPhrase })}
            </Label>
            <Input
              id="confirm-phrase"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              autoCorrect="off"
              spellCheck={false}
              mono
              aria-label={t('privileged.typeToConfirmAria')}
            />
          </div>
        )}

        {!hideAuditNotice && (
          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <ScrollText className="size-3.5 shrink-0" aria-hidden />
            {t('privileged.auditedNotice')}
          </p>
        )}

        {/* Announce the in-progress window: the Spinner is aria-hidden and the
            result is only toasted afterwards, so without this the mutation runs
            silently for a SR user after they confirm (4.1.3). */}
        {pending && (
          <span role="status" className="sr-only">
            {t('privileged.working')}
          </span>
        )}

        <DialogFooter>
          <Button
            variant="secondary"
            onClick={() => onOpenChange(false)}
            disabled={pending}
          >
            {cancelLabel ?? t('actions.cancel')}
          </Button>
          <Button
            variant={tone === 'danger' ? 'destructive-solid' : 'primary'}
            onClick={onConfirm}
            disabled={confirmDisabled}
          >
            {pending && <Spinner size="sm" aria-hidden />}
            {confirmLabel ?? t('privileged.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
