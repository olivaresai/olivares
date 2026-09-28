// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { useState, type FormEvent } from 'react'
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
import { PagePrimaryAction } from '@/components/ui/page-actions'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { agentOpsApi, agentOpsKeys } from './api'
import { AuthorityLostError, useAuthBoundary } from './auth-boundary'
import type { CreateAccountRequest, ProviderAccountDTO } from './types'

// Status alone cannot prove refusal: filesystem validation can return 400/422
// after reservation. Only a known-invalid input proves this attempt could not
// pass CreateProviderAccount's pre-reservation validation. The current API has
// no machine-readable distinction for 409 name/custody/intention conflicts.
function refusedBeforeReservation(
  error: unknown,
  request: CreateAccountRequest,
): boolean {
  if (!(error instanceof ApiError)) return false
  // Keep Unicode normalization unknown instead of equating JS casing with Go's.
  if ([...request.driver].some((c) => c.charCodeAt(0) > 127)) return false
  const driver = request.driver.trim().toLowerCase()
  const validDriver =
    driver.length <= 64 && /^[a-z0-9][a-z0-9_.-]*$/.test(driver)
  if (error.status === 400) return !validDriver
  const name = request.name || driver
  const validName =
    name.length <= 32 && /^[a-z]/.test(name) && !/[^a-z0-9-]/.test(name)
  return error.status === 422 && validDriver && !validName
}

/** A submitted request survives a room change, but never its authority boundary.
 * Replaying the same request recovers the server's reservation after a lost answer. */
export function ProviderAccountCreate({
  onCreated,
}: {
  onCreated: (account: ProviderAccountDTO) => void
}) {
  const { t } = useTranslation(['agentops', 'common'])
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const client = useQueryClient()
  const key = agentOpsKeys.accountCreation(activeTenant, boundary.epoch)
  const intent =
    useQuery<CreateAccountRequest | null>({
      queryKey: key,
      queryFn: () => null,
      enabled: false,
      staleTime: Infinity,
      gcTime: Infinity,
    }).data ?? null
  const setIntent = (request: CreateAccountRequest | null) =>
    client.setQueryData(key, request)
  const [open, setOpen] = useState(false)
  const [driver, setDriver] = useState('claude')
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const canWrite = can('sessions:account:write')
  const [seenWrite, setSeenWrite] = useState(canWrite)
  if (seenWrite !== canWrite) {
    setSeenWrite(canWrite)
    if (!canWrite) setOpen(false)
  }
  const create = usePrivilegedMutation<
    CreateAccountRequest,
    ProviderAccountDTO
  >({
    mutationKey: key,
    mutationFn: async (request, authority) => {
      if (!can('sessions:account:write')) throw new AuthorityLostError()
      // A cached request means an earlier attempt is still unresolved, including
      // across room remounts. A later refusal cannot settle that earlier attempt.
      const hadIntent = !!client.getQueryData<CreateAccountRequest | null>(key)
      setIntent(request)
      try {
        return await agentOpsApi.createAccount(request, {
          tenant: activeTenant,
          dispatchGuard: authority.dispatchGuard,
        })
      } catch (err) {
        if (
          !authority.signal.aborted &&
          !(err instanceof ApiError && err.isStepUpRequired)
        ) {
          if (!hadIntent && refusedBeforeReservation(err, request))
            setIntent(null)
          setError(
            err instanceof Error ? err.message : t('accounts.create.uncertain'),
          )
        }
        throw err
      }
    },
    successMessage: (account) =>
      t('accounts.create.success', { name: account.name }),
    invalidateKeys: [
      agentOpsKeys.accounts(activeTenant, boundary.epoch),
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
    ],
    onDone: (account) => {
      setIntent(null)
      setOpen(false)
      setError(null)
      onCreated(account)
    },
    onError: () => true,
  })
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!canWrite || create.isPending) return
    setError(null)
    create.mutate(
      intent ?? {
        driver: driver.trim(),
        ...(name ? { name } : {}),
        idempotency_key: crypto.randomUUID(),
      },
    )
  }
  const start = () => {
    if (!intent) {
      setDriver('claude')
      setName('')
      setError(null)
    }
    setOpen(true)
  }
  return (
    <>
      {canWrite && (
        <PagePrimaryAction>
          <Button
            variant="primary"
            size="sm"
            onClick={start}
            disabled={create.isPending}
          >
            <Plus className="size-3.5" />
            {t(intent ? 'accounts.create.retry' : 'accounts.create.button')}
          </Button>
        </PagePrimaryAction>
      )}
      {intent && !open && (
        <p
          role="status"
          className="rounded-md border border-border bg-muted px-3 py-2 text-caption"
        >
          {t(
            create.isPending
              ? 'accounts.create.pending'
              : 'accounts.create.uncertain',
          )}
        </p>
      )}
      <Dialog open={open && canWrite} onOpenChange={setOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t('accounts.create.title')}</DialogTitle>
            <DialogDescription>
              {t('accounts.create.description')}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={submit} className="flex flex-col gap-3">
            <Field
              label={t('accounts.create.driver')}
              description={t('accounts.create.driverHint')}
            >
              <Input
                required
                value={intent?.driver ?? driver}
                onChange={(event) => setDriver(event.target.value)}
                disabled={!!intent || create.isPending}
              />
            </Field>
            <Field
              label={t('accounts.adoptDialog.name')}
              description={t('accounts.adoptDialog.nameHint')}
            >
              <Input
                value={intent?.name ?? (intent ? '' : name)}
                onChange={(event) => setName(event.target.value)}
                pattern="[a-z][a-z0-9-]*"
                maxLength={32}
                disabled={!!intent || create.isPending}
              />
            </Field>
            {intent && !create.isPending && (
              <p className="text-caption text-muted-foreground">
                {t('accounts.create.uncertain')}
              </p>
            )}
            {error && (
              <p role="alert" className="text-caption text-danger">
                {error}
              </p>
            )}
            <DialogFooter className="flex-wrap">
              {intent && !create.isPending && (
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => {
                    setIntent(null)
                    setError(null)
                    setOpen(false)
                  }}
                >
                  {t('accounts.create.dismiss')}
                </Button>
              )}
              <Button
                type="button"
                variant="secondary"
                onClick={() => setOpen(false)}
              >
                {t('common:actions.close')}
              </Button>
              <Button
                type="submit"
                variant="primary"
                disabled={create.isPending || (!intent && !driver.trim())}
              >
                {t(
                  create.isPending
                    ? 'accounts.create.pending'
                    : intent
                      ? 'accounts.create.retry'
                      : 'accounts.create.submit',
                )}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}
