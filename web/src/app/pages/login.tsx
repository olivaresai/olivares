// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { zodResolver } from '@hookform/resolvers/zod'
import {
  useMutation,
  useQueryClient,
  type QueryClient,
} from '@tanstack/react-query'
import { useState } from 'react'
import { Link, Navigate, useNavigate, useSearch } from '@tanstack/react-router'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'
import { isEngineEmail } from '@/lib/email'
import { AuthShell } from '@/components/layout/auth-shell'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import type { Whoami } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { consoleReturnPath } from '@/lib/auth/return-path'
import { can as rbacCan } from '@/lib/auth/rbac'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { SecondFactorPanel } from '@/features/identity/totp-login'
import { SsoButtons } from './login-sso'
import type { LoginChallenge } from '@/lib/api/types'
import { viewById } from '@/features/navigation/model'
import type { FeatureView } from '@/features/registry'
import { ANONYMOUS_VIEWS } from '@/features/anonymous-registry'
import {
  START_PAGE_IDS,
  useClientSettings,
} from '@/features/settings/preferences'
import { useTenantStore } from '@/stores/tenant'

/**
 * The tenant `can()` will settle on once sign-in has stored whoami. A persisted
 * choice is kept when it still belongs to this principal; otherwise the first
 * grant is the one the session effect selects. Superadmin authority does not
 * need a grant.
 */
function tenantFor(principal: Whoami): string | null {
  const active = useTenantStore.getState().activeTenant
  const ids = principal.grants
    .map((grant) => grant.tenant)
    .filter((id) => id.length > 0)
  if (active && (principal.superadmin || ids.includes(active))) return active
  return ids[0] ?? null
}

/**
 * Sign-in writes whoami before this callback runs, and the hook's `can` is still
 * the anonymous closure from the render that submitted the form. Prefer that
 * stored principal. When the cache is empty (the page is only rendering), the
 * hook is already current.
 */
function permitsNow(
  client: QueryClient,
  hookCan: (permission: string) => boolean,
): (view: FeatureView) => boolean {
  const principal = client.getQueryData<Whoami>(queryKeys.whoami) ?? null
  if (!principal) return (view) => !view.permission || hookCan(view.permission)
  const tenant = tenantFor(principal)
  return (view) =>
    !view.permission || rbacCan(view.permission, { principal, tenant })
}

/** Where sign-in lands. An unknown or refused destination stays on the home page. */
function startPath(permits: (view: FeatureView) => boolean): string {
  const choice = useClientSettings.getState().startPage
  if (!(START_PAGE_IDS as readonly string[]).includes(choice)) return '/'
  const view = viewById(choice)
  if (!view) return '/'
  if (!permits(view)) return '/'
  return view.path
}

const schema = z.object({
  email: z.string().refine(isEngineEmail),
  password: z.string().min(1),
})
type LoginValues = z.infer<typeof schema>

export function LoginPage() {
  const { t } = useTranslation(['auth', 'common', 'errors'])
  const { status, login, adoptSession, can } = useAuth()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { returnTo } = useSearch({ from: '/login' })
  const requestedPath = consoleReturnPath(returnTo, window.location.origin)
  const destination = () =>
    requestedPath
      ? {
          to: requestedPath.split(/[?#]/)[0] as '/',
          href: requestedPath,
          replace: true,
        }
      : { to: startPath(permitsNow(queryClient, can)) as '/' }
  const serverInfo = useServerInfo()
  const form = useForm<LoginValues>({
    resolver: zodResolver(schema),
    defaultValues: { email: '', password: '' },
  })

  // The second-factor challenge a verified password returned: the form swaps
  // for the code/enrolment leg, and nothing is stored until a code (or a
  // first-factor activation) completes the login.
  const [challenge, setChallenge] = useState<LoginChallenge | null>(null)

  const mutation = useMutation({
    mutationFn: (values: LoginValues) => login(values),
    onSuccess: (res) => {
      if ('mfa_required' in res && res.mfa_required) {
        setChallenge(res)
        return
      }
      navigate(destination())
    },
  })

  const finishSecondFactor = async (session: {
    csrf_token: string
    session_id: string
    expires_at: string
  }) => {
    await adoptSession(session)
    navigate(destination())
  }

  // First-boot has no users yet → the setup flow takes precedence.
  if (serverInfo.data?.setup_required) return <Navigate to="/setup" />
  if (status === 'authenticated') return <Navigate {...destination()} />

  const submitError =
    mutation.error instanceof ApiError && mutation.error.isLockedOut
      ? t('login.lockedOut')
      : mutation.error instanceof ApiError
        ? t('login.invalid')
        : mutation.error
          ? t('errors:network.title') // a NetworkError (or non-API failure), not bad creds
          : null

  return (
    <AuthShell>
      <Card className="p-6 sm:p-7">
        <div className="mb-6 flex flex-col gap-1.5">
          <h1 className="font-display text-title text-foreground">
            {t('login.title')}
          </h1>
          {/* ⛔ THE SUBTITLE USED TO RESTATE THE TITLE. "Sign in to the control
              plane." under a heading that says "Sign in" tells an operator nothing
              they did not read half a second earlier, and in Spanish it read
              "Accede al control plane." — an English term inside a Spanish sentence
              on screen one. It now says what the credentials ARE. */}
          <p className="text-body text-muted-foreground">
            {t('login.subtitle')}
          </p>
        </div>
        {challenge ? (
          <SecondFactorPanel
            challenge={challenge}
            onDone={(session) => void finishSecondFactor(session)}
            onRestart={() => {
              setChallenge(null)
              mutation.reset()
            }}
          />
        ) : (
          <>
            <SsoButtons
              providers={serverInfo.data?.sso_providers}
              returnTo={requestedPath}
            />
            <form
              onSubmit={form.handleSubmit((v) => mutation.mutate(v))}
              className="flex flex-col gap-4"
              noValidate
            >
              <Field
                label={t('login.email')}
                htmlFor="email"
                error={
                  form.formState.errors.email
                    ? t('common:validation.email')
                    : undefined
                }
              >
                <Input
                  id="email"
                  type="email"
                  autoComplete="username"
                  placeholder={t('login.emailPlaceholder')}
                  aria-invalid={!!form.formState.errors.email}
                  {...form.register('email')}
                />
              </Field>
              <Field
                label={t('login.password')}
                htmlFor="password"
                error={
                  form.formState.errors.password
                    ? t('common:validation.required')
                    : undefined
                }
              >
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  aria-invalid={!!form.formState.errors.password}
                  {...form.register('password')}
                />
              </Field>

              {submitError && (
                <p className="text-body text-danger" role="alert">
                  {submitError}
                </p>
              )}

              <Button
                type="submit"
                variant="primary"
                disabled={mutation.isPending}
                className="w-full"
              >
                {mutation.isPending ? t('login.signingIn') : t('login.submit')}
              </Button>
            </form>
          </>
        )}
        {ANONYMOUS_VIEWS.some((view) => view.loginLabel) && (
          <div className="mt-4 flex flex-col gap-2">
            {ANONYMOUS_VIEWS.filter((view) => view.loginLabel).map((view) => (
              <Button
                key={view.id}
                asChild
                variant="secondary"
                className="h-auto min-h-11 w-full whitespace-normal py-2"
              >
                <Link to={view.path as '/'}>{view.loginLabel?.()}</Link>
              </Button>
            ))}
          </div>
        )}
      </Card>
      {/* Passkeys are optional (Settings > Security), so the sign-in page says
          nothing about them: the form is the page. */}
      <div className="mt-5 flex flex-col items-center gap-3">
        {/*surface the public status page (it needs no session) so an operator
         * facing a login failure can tell an outage from a credential problem. */}
        <p className="text-caption text-muted-foreground">
          <Link
            to="/status-page"
            className="underline-offset-2 hover:underline"
          >
            {t('login.statusPage')}
          </Link>
        </p>
      </div>
    </AuthShell>
  )
}
