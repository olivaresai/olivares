// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Public invite-acceptance page. The invite email links here with
// `#token=…`; the invitee chooses a password and POSTs /v1/invites/accept
// (unauthenticated — the single-use token is the gate). Security posture:
//  - the token is read once from the URL and kept in memory only — it is never
//    logged, never rendered, and never sent anywhere but the accept endpoint;
//  - error copy is deliberately COARSE (one message for unknown/expired/used —
//    mirroring the backend's single ErrInviteInvalid, no user-enumeration oracle);
//  - the password rule mirrors the server's MinPasswordLen (core/auth,
//    length >= 8) so the client never promises what the engine would reject;
//  - on success the minted session token is DISCARDED and the user is routed to
//    the internal /login (never a URL-supplied destination — no open redirect).
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'
import { AuthShell } from '@/components/layout/auth-shell'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { toast } from '@/components/ui/toaster'
import { authApi } from '@/lib/api/endpoints'
import { ApiError } from '@/lib/api/errors'

/** Server-side MinPasswordLen (core/auth/accounts.go) mirrored client-side. */
const MIN_PASSWORD_LEN = 8

const schema = z
  .object({
    password: z.string().min(MIN_PASSWORD_LEN),
    confirm: z.string(),
  })
  .refine((v) => v.password === v.confirm, { path: ['confirm'] })
type AcceptValues = z.infer<typeof schema>

function readInviteToken(): string {
  // New links use the fragment so the bearer never reaches HTTP access logs or
  // Referer headers. Keep query support only for already-issued pre-fix links;
  // the mount effect below removes either shape from browser history promptly.
  const fragment = window.location.hash.startsWith('#')
    ? window.location.hash.slice(1)
    : window.location.hash
  return (
    new URLSearchParams(fragment).get('token') ??
    new URLSearchParams(window.location.search).get('token') ??
    ''
  )
}

export function AcceptInvitePage() {
  const { t } = useTranslation(['auth', 'errors'])
  const navigate = useNavigate()
  // Read the token ONCE at mount; it lives in component memory only.
  const [token] = useState(readInviteToken)
  useEffect(() => {
    if (window.location.search || window.location.hash) {
      window.history.replaceState(window.history.state, '', '/accept-invite')
    }
  }, [])
  const form = useForm<AcceptValues>({
    resolver: zodResolver(schema),
    defaultValues: { password: '', confirm: '' },
  })

  const mutation = useMutation({
    mutationFn: (values: AcceptValues) =>
      authApi.acceptInvite({ token, password: values.password }),
    onSuccess: () => {
      // Deliberately drop the minted session: the account is active, and the
      // user signs in through the one normal path. Fixed internal target.
      toast.success(t('invite.success'))
      void navigate({ to: '/login' })
    },
  })

  // One coarse message for every invalid-token shape (unknown/expired/used) —
  // identical wording keeps this public page a non-oracle.
  const submitError = mutation.error
    ? mutation.error instanceof ApiError
      ? mutation.error.code === 'weak_password'
        ? t('invite.weakPassword')
        : t('invite.invalid')
      : t('errors:network.title')
    : null

  return (
    <AuthShell>
      <Card className="p-6">
        <div className="mb-5 flex flex-col gap-1">
          <h1 className="font-display text-lg font-semibold tracking-tight text-foreground">
            {t('invite.title')}
          </h1>
          {token !== '' ? (
            <p className="text-sm text-muted-foreground">
              {t('invite.subtitle')}
            </p>
          ) : null}
        </div>

        {token === '' ? (
          <div className="flex flex-col gap-4">
            <p className="text-sm text-danger" role="alert">
              {t('invite.missingToken')}
            </p>
            <Button asChild variant="secondary" className="w-full">
              <Link to="/login">{t('invite.goToLogin')}</Link>
            </Button>
          </div>
        ) : (
          <form
            onSubmit={form.handleSubmit((v) => mutation.mutate(v))}
            className="flex flex-col gap-4"
            noValidate
          >
            <Field
              label={t('invite.password')}
              htmlFor="invite-password"
              description={t('invite.passwordHint')}
              error={
                form.formState.errors.password
                  ? t('invite.weakPassword')
                  : undefined
              }
            >
              <Input
                id="invite-password"
                type="password"
                autoComplete="new-password"
                autoFocus
                aria-invalid={!!form.formState.errors.password}
                {...form.register('password')}
              />
            </Field>
            <Field
              label={t('invite.confirm')}
              htmlFor="invite-confirm"
              error={
                form.formState.errors.confirm ? t('invite.mismatch') : undefined
              }
            >
              <Input
                id="invite-confirm"
                type="password"
                autoComplete="new-password"
                aria-invalid={!!form.formState.errors.confirm}
                {...form.register('confirm')}
              />
            </Field>

            {submitError && (
              <p className="text-sm text-danger" role="alert">
                {submitError}
              </p>
            )}

            <Button
              type="submit"
              variant="primary"
              disabled={mutation.isPending}
              className="w-full"
            >
              {mutation.isPending ? t('invite.submitting') : t('invite.submit')}
            </Button>
            <p className="text-center text-xs text-muted-foreground">
              <Link to="/login" className="underline-offset-2 hover:underline">
                {t('invite.goToLogin')}
              </Link>
            </p>
          </form>
        )}
      </Card>
    </AuthShell>
  )
}
