// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The forced post-review (POST /killswitch/{id}/review): a human DIFFERENT from
// whoever engaged / requested / executed the re-enable closes the incident loop
// with a mandatory note. Until it lands, the next re-enable of the same scope is
// blocked server-side. The separation-of-duties 403 surfaces as a calm warning
// (usePrivilegedMutation), never a red error.
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
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { killswitchApi, killswitchKeys } from './api'
import './i18n'
import type { KillSwitchDTO } from './types'

/** The engine refused the review (not a step-up): its own sentence is shown. */
class ReviewRefused extends Error {}

export interface ReviewDialogProps {
  stop: KillSwitchDTO
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ReviewDialog({ stop, open, onOpenChange }: ReviewDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        {open && <ReviewForm stop={stop} onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function ReviewForm({
  stop,
  onClose,
}: {
  stop: KillSwitchDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['killswitch', 'common'])
  const { activeTenant } = useAuth()
  const [note, setNote] = useState('')

  // HU 047: a 403 (whoever engaged, requested or executed may not review) left the
  // dialog open with nothing said. The shared hook reports any 403 as a missing role
  // before a dialog can answer, so the refusal becomes this dialog's own error and the
  // engine's sentence is shown here (a step-up still goes to the hook).
  const [refused, setRefused] = useState('')
  const review = usePrivilegedMutation<string, KillSwitchDTO>({
    mutationFn: async (n) => {
      setRefused('')
      try {
        return await killswitchApi.review(stop.id, { note: n })
      } catch (err) {
        if (
          err instanceof ApiError &&
          err.isForbidden &&
          !err.isStepUpRequired &&
          err.message
        )
          throw new ReviewRefused(err.message)
        throw err
      }
    },
    onError: (err) => {
      if (!(err instanceof ReviewRefused)) return false
      setRefused(err.message)
      return true
    },
    invalidateKeys: () => [killswitchKeys.all(activeTenant)],
    successMessage: t('review.done'),
    onDone: onClose,
  })

  const valid = note.trim().length > 0

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('review.title')}</DialogTitle>
        <DialogDescription>{t('review.body')}</DialogDescription>
      </DialogHeader>

      <Field
        label={t('review.note')}
        htmlFor="ks-review-note"
        description={t('review.noteHint')}
        required
      >
        <Textarea
          id="ks-review-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          rows={3}
        />
      </Field>

      {refused ? (
        <p role="alert" className="text-body text-warning">
          {refused}
        </p>
      ) : null}

      <p className="flex items-center gap-1.5 text-caption text-muted-foreground">
        <ScrollText className="size-3.5 shrink-0" aria-hidden />
        {t('common:privileged.auditedNotice')}
      </p>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={review.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => review.mutate(note.trim())}
          disabled={!valid || review.isPending}
        >
          {review.isPending && <Spinner size="sm" aria-hidden />}
          {t('review.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
