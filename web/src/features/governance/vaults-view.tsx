// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { governanceApi, governanceKeys } from './api'
import './i18n'
import type {
  SharedVaultDTO,
  VaultRole,
  VaultShareDTO,
  VaultShareInput,
  VaultSubjectKind,
} from './types'

const ROLES: readonly VaultRole[] = ['viewer', 'editor', 'owner']
const SUBJECT_KINDS: readonly VaultSubjectKind[] = ['user', 'group']
const SELECT_CLASS =
  'h-9 rounded-md border border-input bg-background px-3 text-body text-foreground'

/**
 * VaultsView lists the shared vaults the caller can see, creates one, and opens a
 * vault's share panel. Read gates on governance:vault:read; create, share and revoke
 * on governance:vault:write. Community answers 501 business_required, which shows the
 * Business notice in place of the list.
 */
export function VaultsView() {
  const { activeTenant } = useAuth()
  return <TenantVaultsView key={activeTenant} />
}

function TenantVaultsView() {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:vault:read')
  const canWrite = can('governance:vault:write')
  const [selected, setSelected] = useState<SharedVaultDTO | null>(null)
  const [name, setName] = useState('')

  const vaults = useQuery({
    queryKey: governanceKeys.vaults(activeTenant),
    queryFn: () => governanceApi.listVaults(),
    enabled: canRead,
  })

  const create = usePrivilegedMutation({
    mutationFn: (vaultName: string) =>
      governanceApi.createVault({ name: vaultName }),
    invalidateKeys: () => [governanceKeys.vaults(activeTenant)],
    successMessage: t('vaults.created'),
    onDone: () => setName(''),
  })

  if (!canRead) return <ForbiddenState />
  if (vaults.error) {
    return (
      <QueryErrorState
        error={vaults.error}
        subject={t('vaults.heading')}
        retry={() => vaults.refetch()}
      />
    )
  }

  function handleCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const trimmed = name.trim()
    if (trimmed) create.mutate(trimmed)
  }

  const items = vaults.data?.vaults ?? []

  return (
    <div className="flex flex-col gap-5">
      <h2 className="text-heading font-semibold text-foreground">
        {t('vaults.heading')}
      </h2>

      {canWrite && (
        <form onSubmit={handleCreate} className="flex items-end gap-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor="vault-name">{t('vaults.name')}</Label>
            <Input
              id="vault-name"
              value={name}
              maxLength={64}
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <Button type="submit" disabled={!name.trim() || create.isPending}>
            {t('vaults.create')}
          </Button>
        </form>
      )}

      {vaults.isPending ? null : items.length === 0 ? (
        <p className="text-body text-muted-foreground">{t('vaults.empty')}</p>
      ) : (
        <ul className="flex flex-col divide-y divide-border">
          {items.map((vault) => (
            <li
              key={vault.id}
              className="flex items-center justify-between py-2"
            >
              <span className="text-body font-medium text-foreground">
                {vault.name}
              </span>
              <Button
                variant="outline"
                size="sm"
                aria-pressed={selected?.id === vault.id}
                onClick={() => setSelected(vault)}
              >
                {t('vaults.shares')}
              </Button>
            </li>
          ))}
        </ul>
      )}

      {selected && (
        <VaultSharesPanel
          key={selected.id}
          vault={selected}
          canWrite={canWrite}
        />
      )}
    </div>
  )
}

interface SharesPanelProps {
  vault: SharedVaultDTO
  canWrite: boolean
}

/** The share list of one vault, with a share form and a revoke on each grant. */
function VaultSharesPanel({ vault, canWrite }: SharesPanelProps) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant } = useAuth()
  const [subjectKind, setSubjectKind] = useState<VaultSubjectKind>('user')
  const [subjectRef, setSubjectRef] = useState('')
  const [role, setRole] = useState<VaultRole>('viewer')
  const [confirmRevoke, setConfirmRevoke] = useState<VaultShareDTO | null>(null)

  const shares = useQuery({
    queryKey: governanceKeys.vaultShares(activeTenant, vault.id),
    queryFn: () => governanceApi.listVaultShares(vault.id),
  })

  const invalidate = () => [governanceKeys.vaultShares(activeTenant, vault.id)]

  const share = usePrivilegedMutation({
    mutationFn: (input: VaultShareInput) =>
      governanceApi.shareVault(vault.id, input),
    invalidateKeys: invalidate,
    successMessage: t('vaults.shared'),
    onDone: () => setSubjectRef(''),
  })

  const revoke = usePrivilegedMutation({
    mutationFn: (grant: VaultShareDTO) =>
      governanceApi.revokeVaultShare(vault.id, {
        subject_kind: grant.subject_kind,
        subject_ref: grant.subject_ref,
      }),
    invalidateKeys: invalidate,
    successMessage: t('vaults.revoked'),
    onDone: () => setConfirmRevoke(null),
  })

  function handleShare(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const ref = subjectRef.trim()
    if (ref) share.mutate({ subject_kind: subjectKind, subject_ref: ref, role })
  }

  const grants = shares.data?.shares ?? []

  return (
    <section className="flex flex-col gap-3">
      <h3 className="text-body font-semibold text-foreground">
        {t('vaults.sharesFor', { name: vault.name })}
      </h3>

      {shares.error ? (
        <QueryErrorState error={shares.error} retry={() => shares.refetch()} />
      ) : shares.isPending ? null : grants.length === 0 ? (
        <p className="text-body text-muted-foreground">
          {t('vaults.noShares')}
        </p>
      ) : (
        <ul className="flex flex-col divide-y divide-border">
          {grants.map((grant) => (
            <li
              key={`${grant.subject_kind}:${grant.subject_ref}`}
              className="flex items-center justify-between py-2 text-body"
            >
              <span>
                {grant.subject_ref} · {t(`vaults.kind.${grant.subject_kind}`)} ·{' '}
                {t(`vaults.roles.${grant.role}`)}
              </span>
              {canWrite && (
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={revoke.isPending}
                  onClick={() => setConfirmRevoke(grant)}
                >
                  {t('vaults.revoke')}
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}

      {canWrite && (
        <form onSubmit={handleShare} className="flex flex-wrap items-end gap-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor="vault-subject-kind">
              {t('vaults.subjectKind')}
            </Label>
            <select
              id="vault-subject-kind"
              className={SELECT_CLASS}
              value={subjectKind}
              onChange={(event) =>
                setSubjectKind(event.target.value as VaultSubjectKind)
              }
            >
              {SUBJECT_KINDS.map((kind) => (
                <option key={kind} value={kind}>
                  {t(`vaults.kind.${kind}`)}
                </option>
              ))}
            </select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="vault-subject-ref">{t('vaults.sharedWith')}</Label>
            <Input
              id="vault-subject-ref"
              value={subjectRef}
              onChange={(event) => setSubjectRef(event.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="vault-role">{t('vaults.role')}</Label>
            <select
              id="vault-role"
              className={SELECT_CLASS}
              value={role}
              onChange={(event) => setRole(event.target.value as VaultRole)}
            >
              {ROLES.map((r) => (
                <option key={r} value={r}>
                  {t(`vaults.roles.${r}`)}
                </option>
              ))}
            </select>
          </div>
          <Button
            type="submit"
            disabled={!subjectRef.trim() || share.isPending}
          >
            {t('vaults.share')}
          </Button>
        </form>
      )}
      {confirmRevoke && (
        <ConfirmDialog
          open
          onOpenChange={(open) => !open && setConfirmRevoke(null)}
          title={t('vaults.revoke')}
          description={`${vault.name} · ${confirmRevoke.subject_ref}`}
          confirmLabel={t('vaults.revoke')}
          tone="danger"
          pending={revoke.isPending}
          onConfirm={() => revoke.mutate(confirmRevoke)}
        />
      )}
    </section>
  )
}
