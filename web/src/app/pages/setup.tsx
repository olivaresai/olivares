// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Navigate, useNavigate } from '@tanstack/react-router'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'
import { isEngineEmail } from '@/lib/email'
import { AuthShell } from '@/components/layout/auth-shell'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { authApi } from '@/lib/api/endpoints'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { useAuth } from '@/lib/auth/context'
import { useTenantStore } from '@/stores/tenant'

const schema = z.object({
  token: z.string().min(1),
  email: z.string().refine(isEngineEmail),
  password: z.string().min(1),
})
type SetupValues = z.infer<typeof schema>

export function SetupPage() {
  const { t } = useTranslation(['auth', 'common', 'errors'])
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const serverInfo = useServerInfo()
  const setActiveTenant = useTenantStore((s) => s.setActiveTenant)
  const form = useForm<SetupValues>({
    resolver: zodResolver(schema),
    defaultValues: { token: '', email: '', password: '' },
  })

  const { login } = useAuth()
  const mutation = useMutation({
    mutationFn: (values: SetupValues) => authApi.setup(values),
    onSuccess: async (created, values) => {
      // Setup creates the first organization together with the superadmin that
      // owns it (core/api/handlers_auth.go handleSetup). SELECT it here: the
      // tenant store is persisted, so by the time the operator finishes signing
      // in, the client is already sending X-Olivares-Tenant and the console
      // opens on a working panel instead of a screen of "tenant required".
      // Signing in as a principal WITHOUT that grant is safe — AuthProvider's
      // tenant effect drops a selection the principal cannot act in.
      if (created?.organization?.tenant_id)
        setActiveTenant(created.organization.tenant_id)
      // Sign the new administrator in with what they just typed (it stays in
      // memory only) and open the setup wizard. Anything short of a session —
      // a second-factor demand, a refusal, a network error — falls back to the
      // ordinary sign-in page with the same message as before. Navigate BEFORE
      // refreshing the server info: a refreshed "setup done" re-renders this
      // page's own redirect to the sign-in page.
      let signedIn = false
      try {
        const res = await login({
          email: values.email,
          password: values.password,
        })
        // The sign-in page's own test: whatever carries the session (a bearer
        // or the browser cookie), only a second-factor demand is not one.
        signedIn = !!res && !('mfa_required' in res && res.mfa_required)
      } catch {
        // fall through to the sign-in page
      }
      if (signedIn) {
        navigate({ to: '/onboarding' as '/' })
      } else {
        toast.success(t('setup.done'))
        navigate({ to: '/login' })
      }
      await queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    },
  })

  if (serverInfo.isPending) {
    return (
      <AuthShell>
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      </AuthShell>
    )
  }
  // Setup is a one-time door: once an admin exists, it is closed. Not for the person
  // who just submitted it: their own navigation decides, even while the next page is
  // still loading and the refreshed server info already says setup is done.
  if (
    serverInfo.data &&
    !serverInfo.data.setup_required &&
    !mutation.isPending &&
    !mutation.isSuccess
  )
    return <Navigate to="/login" />

  const submitError =
    mutation.error instanceof ApiError
      ? mutation.error.code === 'weak_password'
        ? t('setup.weakPassword')
        : // The engine's plain refusal of the token (handleSetup); a 403 with its
          // own code, such as residency_violation, says what it is instead.
          mutation.error.code === 'forbidden'
          ? t('setup.invalidToken')
          : t(`errors:codes.${mutation.error.code}`, {
              defaultValue: t('errors:generic'),
            })
      : mutation.error
        ? t('errors:generic')
        : null

  return (
    <AuthShell>
      <Card className="p-6">
        <div className="mb-5 flex flex-col gap-1">
          <h1 className="font-display text-title text-foreground">
            {t('setup.title')}
          </h1>
          <p className="text-body text-muted-foreground">
            {t('setup.subtitle')}
          </p>
        </div>
        <form
          onSubmit={form.handleSubmit((v) => mutation.mutate(v))}
          className="flex flex-col gap-4"
          noValidate
        >
          <Field
            label={t('setup.token')}
            htmlFor="token"
            description={t('setup.tokenHint')}
            error={
              form.formState.errors.token
                ? t('common:validation.required')
                : undefined
            }
          >
            <Input
              id="token"
              mono
              autoFocus
              placeholder={t('setup.tokenPlaceholder')}
              aria-invalid={!!form.formState.errors.token}
              {...form.register('token')}
            />
          </Field>
          <Field
            label={t('setup.email')}
            htmlFor="setup-email"
            error={
              form.formState.errors.email
                ? t('common:validation.email')
                : undefined
            }
          >
            <Input
              id="setup-email"
              type="email"
              autoComplete="username"
              aria-invalid={!!form.formState.errors.email}
              {...form.register('email')}
            />
          </Field>
          <Field
            label={t('setup.password')}
            htmlFor="setup-password"
            error={
              form.formState.errors.password
                ? t('common:validation.required')
                : undefined
            }
          >
            <Input
              id="setup-password"
              type="password"
              autoComplete="new-password"
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
            {mutation.isPending ? t('setup.creating') : t('setup.submit')}
          </Button>
        </form>
      </Card>
    </AuthShell>
  )
}
