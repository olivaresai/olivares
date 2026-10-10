// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useId, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { http } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import { useStepUpOwner, type StepUpOwner } from '@/stores/step-up'

export function ChangePasswordDialog({
  onOpenChange,
}: {
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation(['auth', 'common'])
  const id = useId()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const captureOwner = useStepUpOwner()
  const owner = useRef<StepUpOwner | null>(null)

  function changeOpen(open: boolean) {
    if (!open) owner.current?.retire()
    onOpenChange(open)
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (pending) return
    if (next !== confirm) {
      setError(t('auth:invite.mismatch'))
      return
    }
    const requestOwner = captureOwner()
    owner.current = requestOwner
    const attempt = requestOwner.begin()
    setError(null)
    setPending(true)
    try {
      await http.post<void>(
        '/v1/account/password',
        { current_password: current, new_password: next },
        attempt,
      )
      if (!attempt.current()) return
      setCurrent('')
      setNext('')
      setConfirm('')
      toast.success(t('auth:passwordChange.success'))
      changeOpen(false)
    } catch (cause) {
      if (!attempt.current()) return
      setError(
        cause instanceof ApiError
          ? cause.message
          : t('auth:passwordChange.failed'),
      )
    } finally {
      if (attempt.current()) setPending(false)
      requestOwner.retire()
    }
  }

  return (
    <Dialog open onOpenChange={changeOpen}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t('auth:passwordChange.title')}</DialogTitle>
          <DialogDescription>
            {t('auth:passwordChange.description')}
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => void submit(event)}
          className="grid gap-4"
          aria-describedby={error ? `${id}-error` : undefined}
        >
          <div className="grid gap-2">
            <Label htmlFor={`${id}-current`}>
              {t('auth:passwordChange.current')}
            </Label>
            <Input
              id={`${id}-current`}
              type="password"
              autoComplete="current-password"
              required
              value={current}
              onChange={(event) => setCurrent(event.target.value)}
              disabled={pending}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor={`${id}-new`}>{t('auth:passwordChange.new')}</Label>
            <Input
              id={`${id}-new`}
              type="password"
              autoComplete="new-password"
              required
              value={next}
              onChange={(event) => setNext(event.target.value)}
              disabled={pending}
            />
            <p className="text-caption text-muted-foreground">
              {t('auth:invite.passwordHint')}
            </p>
          </div>
          <div className="grid gap-2">
            <Label htmlFor={`${id}-confirm`}>
              {t('auth:passwordChange.confirm')}
            </Label>
            <Input
              id={`${id}-confirm`}
              type="password"
              autoComplete="new-password"
              required
              value={confirm}
              onChange={(event) => setConfirm(event.target.value)}
              disabled={pending}
            />
          </div>
          {error && (
            <p
              id={`${id}-error`}
              role="alert"
              className="text-body text-destructive"
            >
              {error}
            </p>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => changeOpen(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={pending}>
              {pending
                ? t('auth:passwordChange.saving')
                : t('auth:passwordChange.title')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
