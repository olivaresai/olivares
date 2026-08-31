// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { KeyRound, Pencil, Plus, ShieldAlert, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  consoleKeys,
  type SecretDTO,
  type SecretInput,
} from './api'

// A secret name/handle: letters, digits and the separators `. _ - /`. The store
// rejects anything else; we mirror the rule so the create button explains itself.
const NAME_RE = /^[A-Za-z0-9._/-]{1,128}$/

/**
 * SecretsTab is the FASE X console panel over the SEALED RUNTIME SECRET
 * STORE — the single place an operator manages the named secrets that connector
 * configs reference as `store:<name>` and that resolve at Open WITHOUT a restart-
 * to-reconfigure of the file. The store NEVER returns a value: each secret surfaces
 * only a non-secret `hint` (a short fingerprint) so an admin can tell a secret is
 * set / changed without ever seeing it. By construction the value input is blank on
 * edit (blank = keep the stored value). Like the SSO panel it is deployment-wide,
 * superadmin-only, and every write is step-up-protected (AAL3) and self-audited.
 */
export function SecretsTab() {
  const { t } = useTranslation(['console', 'common'])
  const { isSuperadmin } = useAuth()
  const [editing, setEditing] = useState<SecretDTO | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [del, setDel] = useState<SecretDTO | null>(null)

  const query = useQuery({
    queryKey: consoleKeys.secrets(),
    queryFn: () => consoleApi.listSecrets(),
    enabled: isSuperadmin,
  })

  const deleteMutation = usePrivilegedMutation<string, void>({
    mutationFn: (name) => consoleApi.deleteSecret(name),
    invalidateKeys: () => [consoleKeys.secrets()],
    successMessage: t('console:secrets.deleted'),
    onDone: () => setDel(null),
  })

  if (!isSuperadmin) {
    return (
      <div className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-sm text-muted-foreground">
        <ShieldAlert
          className="mt-0.5 size-4 shrink-0 text-warning"
          aria-hidden
        />
        {t('console:secrets.superadminOnly')}
      </div>
    )
  }

  const secrets = query.data?.secrets ?? []
  const sealerAvailable = query.data?.sealer_available ?? true

  return (
    <div className="flex flex-col gap-4 pt-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('console:secrets.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:secrets.caption')}
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus />
          {t('console:secrets.create')}
        </Button>
      </div>

      {query.data && !sealerAvailable && (
        <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-sm text-warning">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          {t('console:secrets.sealerUnavailable')}
        </p>
      )}

      {query.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : query.isError ? (
        <ErrorState retry={() => void query.refetch()} />
      ) : secrets.length === 0 ? (
        <EmptyState
          title={t('console:secrets.none')}
          description={t('console:secrets.noneHint')}
          icon={<KeyRound />}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:secrets.colName')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:secrets.colHint')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:secrets.colDescription')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {secrets.map((s) => (
                <tr key={s.name} className="border-t border-border align-top">
                  <td className="px-3 py-2">
                    <span className="font-mono text-xs text-foreground">
                      {s.name}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    {/* The hint is a NON-secret fingerprint, never the value. */}
                    <Badge variant="neutral">
                      <KeyRound
                        className="size-3 shrink-0 text-accent-text"
                        aria-hidden
                      />
                      <span className="font-mono">{s.hint}</span>
                    </Badge>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {s.description || '—'}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setEditing(s)}
                      >
                        <Pencil />
                        {t('console:secrets.rotate')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setDel(s)}
                      >
                        <Trash2 />
                        {t('console:secrets.delete')}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {createOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <SecretForm onClose={() => setCreateOpen(false)} />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(o) => !o && setEditing(null)}
      >
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {editing && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <SecretForm existing={editing} onClose={() => setEditing(null)} />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={del !== null}
        onOpenChange={(o) => !o && setDel(null)}
        title={t('console:secrets.deleteTitle')}
        description={t('console:secrets.deleteBody')}
        confirmLabel={t('console:secrets.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => del && deleteMutation.mutate(del.name)}
      />
    </div>
  )
}

function SecretForm({
  existing,
  onClose,
}: {
  existing?: SecretDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const isEdit = !!existing
  const [name, setName] = useState(existing?.name ?? '')
  // The value input is ALWAYS blank on open — we never receive the stored value, and
  // on edit a blank value means "keep the stored secret" (description-only edit).
  const [value, setValue] = useState('')
  const [description, setDescription] = useState(existing?.description ?? '')

  const mutation = usePrivilegedMutation<void, SecretDTO>({
    mutationFn: () => {
      const body: SecretInput = {
        name: name.trim(),
        value,
        description: description.trim(),
      }
      return consoleApi.putSecret(body)
    },
    invalidateKeys: () => [consoleKeys.secrets()],
    successMessage: isEdit
      ? t('console:secrets.rotated')
      : t('console:secrets.created'),
    onDone: onClose,
  })

  const nameValid = isEdit || NAME_RE.test(name.trim())
  // A new secret requires a value; an existing one may be edited with a blank value
  // (keeps the stored secret).
  const valid = nameValid && (isEdit || value !== '')

  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {isEdit
            ? t('console:secrets.editTitle')
            : t('console:secrets.createTitle')}
        </DialogTitle>
        <DialogDescription>{t('console:secrets.caption')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('console:secrets.name')}
          htmlFor="secret-name"
          description={t('console:secrets.nameHint')}
          required
        >
          <Input
            id="secret-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            mono
            disabled={isEdit}
            placeholder="gdrive/token"
          />
        </Field>
        <Field
          label={t('console:secrets.value')}
          htmlFor="secret-value"
          description={
            isEdit
              ? t('console:secrets.valueSet', { hint: existing.hint })
              : t('console:secrets.valueHint')
          }
          required={!isEdit}
        >
          {/* type="password" + never prefilled: the value is write-only by design. */}
          <Input
            id="secret-value"
            type="password"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            autoComplete="new-password"
          />
        </Field>
        <Field label={t('console:secrets.description')} htmlFor="secret-desc">
          <Textarea
            id="secret-desc"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            rows={2}
          />
        </Field>
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => mutation.mutate()}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {isEdit ? t('console:secrets.rotate') : t('console:secrets.save')}
        </Button>
      </DialogFooter>
    </>
  )
}
