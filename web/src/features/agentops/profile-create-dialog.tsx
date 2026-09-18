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
import { useAuthBoundary } from './auth-boundary'
import type { CreateProfileRequest, ProviderProfileDTO } from './types'
import './i18n'

/**
 * Driver keys offered as SUGGESTIONS. The set is open on the engine — any lowercase
 * key of the right shape is accepted — and nothing here reads launchability off the
 * name. The server's `operable` is the legacy enablement flag, reported after
 * registration; launch requirements are a later point read.
 */
const DRIVER_SUGGESTIONS = ['claude', 'codex', 'grok']

/**
 * ProfileCreateDialog — registers a provider profile for homes that ALREADY EXIST on
 * this node's execution environment (B1). What leaves the browser is a driver key,
 * two paths and a label: the server canonicalises and validates the paths on the
 * node that owns them (absolute, symlinks resolved, existing, a directory), never
 * creates a missing home, installs nothing and logs nothing in. No environment_ref
 * is sent — the node registers on its own environment, and a profile for another
 * environment is registered from that environment, the only one that can validate
 * its paths. No credential travels here, and none is asked for.
 */
export function ProfileCreateDialog({
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
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const listId = useId()

  const [driver, setDriver] = useState('')
  const [configHome, setConfigHome] = useState('')
  const [userHome, setUserHome] = useState('')
  const [displayName, setDisplayName] = useState('')

  const reset = () => {
    setDriver('')
    setConfigHome('')
    setUserHome('')
    setDisplayName('')
  }

  const create = usePrivilegedMutation<void, ProviderProfileDTO>({
    mutationFn: () => {
      const name = displayName.trim()
      const body: CreateProfileRequest = {
        driver: driver.trim(),
        config_home: configHome.trim(),
        user_home: userHome.trim(),
        ...(name ? { display_name: name } : {}),
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

  const ready =
    driver.trim() !== '' && configHome.trim() !== '' && userHome.trim() !== ''

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (ready && !create.isPending) create.mutate()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => (create.isPending ? undefined : onOpenChange(o))}
    >
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('profiles.create.title')}</DialogTitle>
          <DialogDescription>
            {t('profiles.create.description')}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={onSubmit} className="flex flex-col gap-3">
          <Field
            label={t('profiles.create.driver')}
            description={t('profiles.create.driverHint')}
          >
            <Input
              value={driver}
              onChange={(e) => setDriver(e.target.value)}
              placeholder={t('profiles.create.driverPlaceholder')}
              list={listId}
              autoComplete="off"
              mono
            />
          </Field>
          <datalist id={listId}>
            {DRIVER_SUGGESTIONS.map((d) => (
              <option key={d} value={d} />
            ))}
          </datalist>
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
