// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { Link, Navigate, useNavigate } from '@tanstack/react-router'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'
import { AuthShell } from '@/components/layout/auth-shell'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { useServerInfo } from '@/lib/hooks/use-server-info'

const schema = z.object({
  email: z.string().email(),
  password: z.string().min(1),
})
type LoginValues = z.infer<typeof schema>

export function LoginPage() {
  const { t } = useTranslation(['auth', 'common', 'errors'])
  const { status, login } = useAuth()
  const navigate = useNavigate()
  const serverInfo = useServerInfo()
  const form = useForm<LoginValues>({
    resolver: zodResolver(schema),
    defaultValues: { email: '', password: '' },
  })

  const mutation = useMutation({
    mutationFn: (values: LoginValues) => login(values),
    onSuccess: () => navigate({ to: '/' }),
  })

  // First-boot has no users yet → the setup flow takes precedence.
  if (serverInfo.data?.setup_required) return <Navigate to="/setup" />
  if (status === 'authenticated') return <Navigate to="/" />

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
      <Card className="p-6">
        <div className="mb-5 flex flex-col gap-1">
          <h1 className="font-display text-lg font-semibold tracking-tight text-foreground">
            {t('login.title')}
          </h1>
          <p className="text-sm text-muted-foreground">{t('login.subtitle')}</p>
        </div>
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
              autoFocus
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
            {mutation.isPending ? t('login.signingIn') : t('login.submit')}
          </Button>
        </form>
      </Card>
      {/*surface the public status page (it needs no session) so an operator
       * facing a login failure can tell an outage from a credential problem. */}
      <p className="text-center text-xs text-muted-foreground">
        <Link
          to="/status-page"
          className="underline-offset-2 hover:underline"
        >
          {t('login.statusPage')}
        </Link>
      </p>
    </AuthShell>
  )
}
