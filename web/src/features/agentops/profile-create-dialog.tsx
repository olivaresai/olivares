// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useId, useState, type FormEvent } from 'react'
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
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { agentOpsApi, agentOpsKeys } from './api'
import { AuthorityLostError, useAuthBoundary } from './auth-boundary'
import {
  ProfileAuthenticationFields,
  useProfileProviders,
} from './profile-authentication'
import type { CreateProfileRequest, ProviderProfileDTO } from './types'
import './i18n'

/**
 * Driver keys offered as SUGGESTIONS. The set is open on the engine — any lowercase
 * key of the right shape is accepted — and nothing here reads launchability off the
 * name. The server's `operable` is the legacy enablement flag, reported after
 * registration; launch requirements are a later point read.
 */
// The drivers this engine launches, by the tool's own name. Claude Code is the default.
const DRIVERS: [string, string][] = [
  ['claude', 'Claude Code'],
  ['codex', 'Codex'],
  ['grok', 'Grok'],
  ['opencode', 'OpenCode'],
  ['gemini-cli', 'Gemini CLI'],
]
const selectClass =
  'h-9 w-full min-w-0 rounded-md border border-border bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
// The tool's own login, kept in its own folders, unless the operator picks otherwise.
const DEFAULT_AUTHENTICATION = { source: 'provider_account_home', provider: '' }

/**
 * ProfileCreateDialog — registers a provider profile for homes that ALREADY EXIST on
 * this node's execution environment. The browser sends a driver key, homes,
 * label and explicit authentication references: the server canonicalises and validates the paths on the
 * node that owns them (absolute, symlinks resolved, existing, a directory), never
 * creates a missing home, installs nothing and logs nothing in. No environment_ref
 * is sent — the node registers on its own environment, and a profile for another
 * environment is registered from that environment, the only one that can validate
 * its paths. No credential travels here, and none is asked for.
 */
export function ProfileCreateDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  localEnvironment?: string
}) {
  const boundary = useAuthBoundary()
  const { can } = useAuth()
  return can('sessions:profile:write') ? (
    <ProfileCreateForm key={boundary.key} {...props} />
  ) : null
}

function ProfileCreateForm({
  open,
  onOpenChange,
  localEnvironment,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** This node's execution environment as a local profile reported it, when one is
   * known — shown so the operator knows WHICH machine validates the paths. */
  localEnvironment?: string
}) {
  const { t } = useTranslation('agentops')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const driverId = useId()

  const [driver, setDriver] = useState('claude')
  const [configHome, setConfigHome] = useState('')
  const [userHome, setUserHome] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [authentication, setAuthentication] = useState(DEFAULT_AUTHENTICATION)
  const providers = useProfileProviders(
    driver,
    open && authentication.source === 'managed_injection',
  )
  const bindingReady =
    authentication.source !== 'managed_injection' ||
    providers.records.some((r) => r.provider_ref === authentication.provider)

  const reset = () => {
    setDriver('claude')
    setConfigHome('')
    setUserHome('')
    setDisplayName('')
    setAuthentication(DEFAULT_AUTHENTICATION)
  }

  const create = usePrivilegedMutation<void, ProviderProfileDTO>({
    mutationFn: () => {
      if (!can('sessions:profile:write') || !bindingReady)
        throw new AuthorityLostError()
      const name = displayName.trim()
      const body: CreateProfileRequest = {
        driver: driver.trim(),
        config_home: configHome.trim(),
        user_home: userHome.trim(),
        ...(name ? { display_name: name } : {}),
        ...(authentication.source
          ? { auth_source: authentication.source }
          : {}),
        ...(authentication.source === 'managed_injection'
          ? { provider_record_ref: authentication.provider }
          : {}),
      }
      return agentOpsApi.createProfile(body)
    },
    invalidateKeys: () => [agentOpsKeys.profiles(activeTenant, boundary.epoch)],
    successMessage: t('profiles.create.success'),
    onDone: () => {
      reset()
      onOpenChange(false)
    },
  })

  // With the tool's own login both homes may stay empty: the server uses the tool's
  // standard folders. With an API key from Providers they may too: the server makes
  // the profile's own (provider_profile.go managedProfileHomes, HU-R16). Otherwise,
  // or once one is named, both are named.
  const homesOptional =
    authentication.source === 'provider_account_home' ||
    authentication.source === 'managed_injection'
  const homesNamed = configHome.trim() !== '' && userHome.trim() !== ''
  const homesEmpty = configHome.trim() === '' && userHome.trim() === ''
  const homesReady = homesNamed || (homesOptional && homesEmpty)
  const ready = driver.trim() !== '' && homesReady && bindingReady
  // What keeps Register disabled, said next to it rather than left to be guessed.
  const missing = !bindingReady
    ? t('profiles.create.missingProvider')
    : !homesReady
      ? homesOptional
        ? t('profiles.create.missingHomesOrNone')
        : t('profiles.create.missingHomes')
      : null

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (ready && !create.isPending) create.mutate()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (create.isPending ? undefined : onOpenChange(o))}
    >
      <DialogContent className="max-h-[90svh] max-w-lg overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t('profiles.create.title')}</DialogTitle>
          <DialogDescription>
            {t('profiles.create.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-3">
          <Field label={t('profiles.create.driver')} htmlFor={driverId}>
            <select
              id={driverId}
              className={selectClass}
              value={driver}
              onChange={(e) => {
                setDriver(e.target.value)
                setAuthentication((value) => ({ ...value, provider: '' }))
              }}
            >
              {DRIVERS.map(([key, label]) => (
                <option key={key} value={key}>
                  {label}
                </option>
              ))}
            </select>
          </Field>
          <ProfileAuthenticationFields
            value={authentication}
            onChange={setAuthentication}
            providers={providers}
            disabled={create.isPending}
          />
          {homesOptional && (
            <p className="text-caption text-muted-foreground">
              {authentication.source === 'managed_injection'
                ? t('profiles.create.homesOptionalManaged')
                : t('profiles.create.homesOptional')}
            </p>
          )}
          <Field
            label={t('profiles.create.configHome')}
            description={t('profiles.create.configHomeHint')}
          >
            <Input
              value={configHome}
              onChange={(e) => setConfigHome(e.target.value)}
              placeholder={t('profiles.create.configHomePlaceholder')}
              autoComplete="off"
              mono
            />
          </Field>
          <Field
            label={t('profiles.create.userHome')}
            description={t('profiles.create.userHomeHint')}
          >
            <Input
              value={userHome}
              onChange={(e) => setUserHome(e.target.value)}
              placeholder={t('profiles.create.userHomePlaceholder')}
              autoComplete="off"
              mono
            />
          </Field>
          <Field label={t('profiles.create.displayName')}>
            <Input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder={t('profiles.create.displayNamePlaceholder')}
            />
          </Field>
          <p className="text-caption text-muted-foreground">
            {localEnvironment
              ? t('profiles.create.environmentKnown', { env: localEnvironment })
              : t('profiles.create.environmentUnknown')}
          </p>
          {missing && !create.isPending ? (
            <p role="status" className="text-caption text-muted-foreground">
              {missing}
            </p>
          ) : null}
          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={create.isPending}
            >
              {t('browser.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!ready || create.isPending}
            >
              {create.isPending && <Spinner className="size-3.5" />}
              {create.isPending
                ? t('profiles.create.submitting')
                : t('profiles.create.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
