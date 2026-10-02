// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// VAULT SECRETS AS A SESSION'S ENVIRONMENT (design FH 016). A tenant administrator
// picks session secrets (env/…) by name and the variable each is read from. Only the
// names leave the browser: the server opens the values for the child alone, and no
// read ever returns one.
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { SecretsTab } from '@/features/console/secrets-tab'
import { useAuth } from '@/lib/auth/context'
import type { SecretEnvChoice } from './api'
import './i18n'

const SESSION_SECRET_PREFIX = 'env/'

/** The variable a secret is read from by default: its name after env/, as a shell
 * variable (env/github-token → GITHUB_TOKEN). */
export function envNameFor(secret: string): string {
  const base = secret
    .slice(SESSION_SECRET_PREFIX.length)
    .toUpperCase()
    .replace(/[^A-Z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')
  return /^[0-9]/.test(base) ? `_${base}` : base
}

export function SessionSecrets({
  value,
  onChange,
}: {
  value: SecretEnvChoice[]
  onChange: (next: SecretEnvChoice[]) => void
}) {
  const { t } = useTranslation('firstHour')
  const { can } = useAuth()
  const boundary = useAuthBoundary()
  const [manage, setManage] = useState(false)
  // The same permission the vault's tenant scope asks for, and the server asks again.
  const admitted = !!boundary.tenant && !!can?.('tenant:admin')
  const query = useQuery({
    queryKey: [
      ...consoleKeys.secrets('tenant'),
      boundary.tenant,
      boundary.epoch,
    ],
    queryFn: ({ signal }) =>
      consoleApi.listSecrets('tenant', {
        signal,
        tenant: boundary.tenant ?? undefined,
      }),
    enabled: admitted,
  })
  if (!admitted) return null

  const secrets = (query.data?.secrets ?? []).filter((s) =>
    s.name.startsWith(SESSION_SECRET_PREFIX),
  )
  const toggle = (secret: string) =>
    onChange(
      value.some((v) => v.secret === secret)
        ? value.filter((v) => v.secret !== secret)
        : [...value, { env: envNameFor(secret), secret }],
    )
  const rename = (secret: string, env: string) =>
    onChange(value.map((v) => (v.secret === secret ? { ...v, env } : v)))

  return (
    <Field label={t('start.secrets')} description={t('start.secretsHint')}>
      <div className="flex flex-col gap-2">
        {secrets.length === 0 && !query.isLoading ? (
          <p className="text-caption text-text-2">{t('start.secretsNone')}</p>
        ) : null}
        {secrets.map((s) => {
          const chosen = value.find((v) => v.secret === s.name)
          return (
            <div key={s.name} className="flex flex-wrap items-center gap-2">
              <label className="flex items-center gap-2 text-body text-text">
                <input
                  type="checkbox"
                  checked={!!chosen}
                  onChange={() => toggle(s.name)}
                  aria-label={t('start.secretUse', { name: s.name })}
                />
                <span className="font-mono text-caption">{s.name}</span>
              </label>
              {chosen ? (
                <Input
                  className="max-w-56 font-mono"
                  value={chosen.env}
                  onChange={(e) => rename(s.name, e.target.value.trim())}
                  aria-label={t('start.secretVariable', { name: s.name })}
                  spellCheck={false}
                />
              ) : null}
            </div>
          )
        })}
        <div>
          <Button type="button" variant="link" onClick={() => setManage(true)}>
            {t('start.secretsManage')}
          </Button>
        </div>
      </div>
      <Dialog open={manage} onOpenChange={setManage}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('start.secretsManage')}</DialogTitle>
            <DialogDescription>{t('start.secretsHint')}</DialogDescription>
          </DialogHeader>
          {manage ? <SecretsTab scope="tenant" namespace="env/" /> : null}
        </DialogContent>
      </Dialog>
    </Field>
  )
}
