// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// MANAGED CREDENTIALS FOR A SESSION. A tenant administrator picks the
// managed credentials (credential/…) the session may use, by name. The value is
// sealed for the tenant: the engine's broker hands the session a helper that
// uses it (an SSH agent, a database proxy), and no read ever returns one.
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
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { SecretsTab } from '@/features/console/secrets-tab'
import { useAuth } from '@/lib/auth/context'
import { useCommunityBuild } from '@/lib/hooks/use-server-info'
import './i18n'

const CREDENTIAL_PREFIX = 'credential/'
/** The bound the engine states (modules/sessions CredentialMaxNames): one launch
 * names at most eight. */
const MAX_CREDENTIALS = 8

/** The name a launch's credentials field takes: the sealed name after
 * credential/. */
export function credentialNameFor(sealed: string): string {
  return sealed.slice(CREDENTIAL_PREFIX.length)
}

export function SessionCredentials({
  value,
  onChange,
}: {
  value: string[]
  onChange: (next: string[]) => void
}) {
  const { t } = useTranslation('firstHour')
  const { can } = useAuth()
  const boundary = useAuthBoundary()
  const [manage, setManage] = useState(false)
  // The credential/ names stay with the organization's administrators (the tenant
  // scope admits every member for their own vault), and the server asks again.
  const admitted = !!boundary.tenant && !!can?.('tenant:admin')
  // Managed credentials are Business: a Community engine answers the launch 501, so
  // the picker is not offered there (undefined, before server-info answers, keeps it).
  const community = useCommunityBuild()
  const offered = admitted && community !== true
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
    enabled: offered,
  })
  if (!offered) return null

  const credentials = (query.data?.secrets ?? [])
    .filter((s) => s.name.startsWith(CREDENTIAL_PREFIX))
    .map((s) => ({ sealed: s.name, name: credentialNameFor(s.name) }))
  const listed = new Set(credentials.map((c) => c.name))
  // A name chosen from an earlier list that this one no longer shows (deleted, or
  // another tenant's) stays in the choice, visible and removable: dropping it
  // silently would launch without what the person picked.
  const gone = value.filter((name) => !listed.has(name))
  const toggle = (name: string) =>
    onChange(
      value.includes(name) ? value.filter((v) => v !== name) : [...value, name],
    )
  // The engine refuses a launch that names more than eight; the picker stops at
  // the same bound instead of letting the person choose a refusal.
  const full = value.length >= MAX_CREDENTIALS

  return (
    <Field
      label={t('start.credentials')}
      description={t('start.credentialsHint')}
    >
      <div className="flex flex-col gap-2">
        {query.isError ? (
          <p className="text-caption text-text-2">
            {t('start.listUnavailable')}
          </p>
        ) : null}
        {!query.isError && credentials.length === 0 && !query.isLoading ? (
          <p className="text-caption text-text-2">
            {t('start.credentialsNone')}
          </p>
        ) : null}
        {[
          ...credentials.map((c) => ({ key: c.sealed, name: c.name })),
          ...gone.map((name) => ({ key: 'gone:' + name, name })),
        ].map(({ key, name }) => {
          const chosen = value.includes(name)
          return (
            <label
              key={key}
              className="flex items-center gap-2 text-body text-text"
            >
              <input
                type="checkbox"
                checked={chosen}
                onChange={() => toggle(name)}
                disabled={!chosen && full}
                aria-label={t('start.credentialUse', { name })}
              />
              <span className="font-mono text-caption">{name}</span>
            </label>
          )
        })}
        <div>
          <Button type="button" variant="link" onClick={() => setManage(true)}>
            {t('start.credentialsManage')}
          </Button>
        </div>
      </div>
      <Dialog open={manage} onOpenChange={setManage}>
        <DialogContent className="max-h-[85vh] grid-cols-1 overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('start.credentialsManage')}</DialogTitle>
            <DialogDescription>{t('start.credentialsHint')}</DialogDescription>
          </DialogHeader>
          {manage ? (
            <SecretsTab scope="tenant" namespace="credential/" />
          ) : null}
        </DialogContent>
      </Dialog>
    </Field>
  )
}
