// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Public invite-acceptance page. The invite email links here with
// `#token=…`; the invitee chooses a password and POSTs /v1/invites/accept
// (unauthenticated — the single-use token is the gate). Security posture:
//  - the token is read once from the URL and kept in memory only — it is never
//    logged, never rendered, and never sent anywhere but the accept endpoint and
//    the preview that says who invites (which never spends it);
//  - error copy is deliberately COARSE (one message for unknown/expired/used —
//    mirroring the backend's single ErrInviteInvalid, no user-enumeration oracle);
//  - the password rule mirrors the server's MinPasswordLen (core/auth,
//    length >= 8) so the client never promises what the engine would reject;
//  - on success the protected cookie session is adopted and the user is routed
//    to the internal / (never a URL-supplied destination — no open redirect).
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { CircleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'
import { AuthSheet, AuthShell } from '@/components/layout/auth-shell'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Notice } from '@/components/ui/notice'
import { http } from '@/lib/api/client'
import { authApi } from '@/lib/api/endpoints'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { networkErrorText } from '@/lib/api/network-error-text'
import { useAuth } from '@/lib/auth/context'

/** Server-side MinPasswordLen (core/auth/accounts.go) mirrored client-side. */
const MIN_PASSWORD_LEN = 8

const schema = z
  .object({
    password: z.string().min(MIN_PASSWORD_LEN),
    confirm: z.string(),
  })
  .refine((v) => v.password === v.confirm, { path: ['confirm'] })
type AcceptValues = z.infer<typeof schema>

/** POST /v1/invites/preview: who invites, to which organization, with which role, for
 * which email. No session: the token in the body is the gate, and the preview never
 * spends it. `invited_by` is empty when the engine knows no name. */
interface InvitePreview {
  email: string
  role: string
  organization: string
  invited_by: string
}

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
  const { adoptSession } = useAuth()
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

  // Who invites, asked once when the page opens with a token. The token stays in this
  // closure: never in the query key, never in storage. An invalid or spent invitation
  // is refused at once, in the notice accepting would show; any other failure (an
  // engine without the route among them) leaves the page as it was.
  // The token is a credential until it is spent, so the preview is asked once
  // per page open in production, whatever it answers; development's double
  // mount asks twice, the first ask aborted. 'static' stops the refetch on
  // focus and reconnect even after a failure (a query without data is
  // otherwise always stale), no retry sends it again, and a later mount finds
  // the address cleaned and no token to send.
  const preview = useQuery({
    queryKey: ['invite-preview'],
    queryFn: ({ signal }) =>
      http.post<InvitePreview>(
        '/v1/invites/preview',
        { token },
        { anonymous: true, signal },
      ),
    enabled: token !== '',
    staleTime: 'static',
    retry: false,
    gcTime: 0,
  })
  const refused =
    preview.error instanceof ApiError && preview.error.code === 'invite_invalid'
  const invite = preview.data?.organization ? preview.data : undefined
  // The role by the console's own name for it; one it has no name for goes unsaid.
  const role = invite ? t(`roles.${invite.role}`, { defaultValue: '' }) : ''
  const names = {
    inviter: invite?.invited_by,
    organization: invite?.organization,
  }
  const invitation = !invite
    ? null
    : t(invite.invited_by ? 'invite.from' : 'invite.to', names)

  const mutation = useMutation({
    mutationFn: (values: AcceptValues) =>
      authApi.acceptInvite({ token, password: values.password }),
    onSuccess: async (session) => {
      await adoptSession(session)
      void navigate({ to: '/', replace: true })
    },
  })

  // One coarse message for every invalid-token shape (unknown/expired/used) —
  // identical wording keeps this public page a non-oracle. The preview's refusal is
  // the same notice, said before anyone types.
  const submitError = mutation.error
    ? mutation.error instanceof ApiError
      ? mutation.error.code === 'weak_password'
        ? t('invite.weakPassword')
        : t('invite.invalid')
      : mutation.error instanceof NetworkError
        ? networkErrorText(t).line
        : t('errors:network.title')
    : refused
      ? t('invite.invalid')
      : null

  return (
    <AuthShell>
      <AuthSheet>
        <div className="flex flex-col gap-1">
          <h1 className="text-[1.375rem]/7 font-extrabold text-balance text-text">
            {t('invite.title')}
          </h1>
          {token !== '' ? (
            <p className="text-body text-pretty text-text-2">
              {invitation ?? t('invite.subtitle')}
            </p>
          ) : null}
        </div>

        {token === '' ? (
          <div className="flex flex-col gap-4.5">
            <Notice tone="bad" icon={<CircleAlert />} role="alert">
              {t('invite.missingToken')}
            </Notice>
            <Button asChild variant="secondary" className="min-h-10 w-full">
              <Link to="/login">{t('invite.goToLogin')}</Link>
            </Button>
          </div>
        ) : (
          <form
            onSubmit={form.handleSubmit((v) => mutation.mutate(v))}
            className="flex flex-col gap-4.5"
            noValidate
          >
            {/* Whose account this is and the role it joins as. The initials
                are the account menu's rule (the first two characters) and only
                decorate: the email is what is read. */}
            {invite ? (
              <BoxedList>
                <BoxedListRow>
                  <Avatar size="sm" aria-hidden>
                    <AvatarFallback>
                      {invite.email.slice(0, 2).toUpperCase()}
                    </AvatarFallback>
                  </Avatar>
                  <div className="min-w-0">
                    <p className="break-words text-body font-medium text-text">
                      {invite.email}
                    </p>
                    {role ? (
                      <p className="text-caption text-text-2">
                        {t('invite.joinAs', { role })}
                      </p>
                    ) : null}
                  </div>
                </BoxedListRow>
              </BoxedList>
            ) : null}
            {/* Account name for password managers; some skip `hidden`. */}
            {invite?.email ? (
              <input
                type="email"
                autoComplete="username"
                value={invite.email}
                readOnly
                className="sr-only"
                tabIndex={-1}
                aria-hidden
              />
            ) : null}
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
              <Notice tone="bad" icon={<CircleAlert />} role="alert">
                {submitError}
              </Notice>
            )}

            <Button
              type="submit"
              variant="primary"
              size="lg"
              disabled={mutation.isPending}
              className="mt-0.5 w-full"
            >
              {mutation.isPending ? t('invite.submitting') : t('invite.submit')}
            </Button>
            <Button asChild variant="ghost" className="min-h-10 w-full">
              <Link to="/login">{t('invite.goToLogin')}</Link>
            </Button>
          </form>
        )}
      </AuthSheet>
    </AuthShell>
  )
}
