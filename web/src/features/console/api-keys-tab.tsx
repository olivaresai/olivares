// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Check,
  Copy,
  Key,
  Plus,
  RefreshCw,
  ShieldAlert,
  Trash2,
} from 'lucide-react'
import { useCallback, useState } from 'react'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { RelTimeLabel } from '@/features/shared'
import { ListTruncationBadge } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  consoleKeys,
  type IssueTokenInput,
  type IssueTokenResult,
  type RotateTokenResult,
  type TokenDTO,
} from './api'

/** El máximo que el repositorio genérico acepta (`maxLimit`, sqlstore/generic.go:29). */
const CONSOLE_PAGE = 1000

const ROLES = ['viewer', 'editor', 'admin', 'owner'] as const

export function ApiKeysTab() {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [createOpen, setCreateOpen] = useState(false)
  const [revoking, setRevoking] = useState<TokenDTO | null>(null)
  const [rotating, setRotating] = useState<TokenDTO | null>(null)
  const [newToken, setNewToken] = useState<string | null>(null)

  // ⛔ ESTA LISTA ES DE CREDENCIALES VIVAS, y el recorte silencioso no oculta filas: oculta
  //    TOKENS QUE SIGUEN AUTENTICANDO y a los que no se puede llegar para revocarlos desde la
  //    UI. `handleListTokens` usa `parseListQuery` y publica `has_more`; sin `limit` el store
  //    paginaba a 100 y la pantalla se leía «éstas son nuestras claves».
  const tokenParams = { limit: CONSOLE_PAGE }
  const tokensQuery = useQuery({
    queryKey: consoleKeys.tokens(activeTenant, tokenParams),
    queryFn: () => consoleApi.listTokens(tokenParams),
  })

  const revokeMutation = usePrivilegedMutation<string, void>({
    mutationFn: (id) => consoleApi.revokeToken(id),
    invalidateKeys: () => [consoleKeys.tokens(activeTenant)],
    successMessage: t('console:apiKeys.revoked'),
    onDone: () => setRevoking(null),
  })

  const qc = useQueryClient()
  const handleCreated = useCallback(
    (result: IssueTokenResult) => {
      setNewToken(result.token)
      setCreateOpen(false)
      void qc.invalidateQueries({ queryKey: consoleKeys.tokens(activeTenant) })
    },
    [qc, activeTenant],
  )

  const handleRotated = useCallback(
    (result: RotateTokenResult) => {
      setNewToken(result.token)
      setRotating(null)
      void qc.invalidateQueries({ queryKey: consoleKeys.tokens(activeTenant) })
    },
    [qc, activeTenant],
  )

  const tokens = tokensQuery.data?.items ?? []

  return (
    <div className="flex flex-col gap-4 pt-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('console:apiKeys.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:apiKeys.caption')}
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus />
          {t('console:apiKeys.create')}
        </Button>
      </div>

      {/* ⛔ UN SOLO AVISO POR LISTA, con el texto especifico. Aqui habia DOS —este y un
          `<Badge>` heredado arriba— que decian lo mismo con palabras distintas, asi que ningun
          `findByText` los delataba: la pantalla enseñaba el recorte dos veces y la bateria salia
          verde. Contraste (F-01), reproducido con `getAllByText(/there are more/i)` = 2.
          Se conserva el label de siempre y se gana el componente comun, que es el que el
          trinquete sabe auditar. */}
      <ListTruncationBadge
        query={tokensQuery}
        label={t('console:apiKeys.truncated', { n: tokens.length })}
        hint={t('console:apiKeys.truncatedHint')}
        className="px-0 pt-0 pb-3"
        filas={tokens.length}
      />
      {tokensQuery.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : tokensQuery.isError ? (
        <ErrorState retry={() => void tokensQuery.refetch()} />
      ) : tokens.length === 0 ? (
        <EmptyState
          title={t('console:apiKeys.none')}
          description={t('console:apiKeys.noneHint')}
          icon={<Key />}
        />
      ) : (
        <div className="overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('console:apiKeys.colName')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:apiKeys.colRole')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:apiKeys.colLastUsed')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:apiKeys.colCreated')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('console:apiKeys.colExpires')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {tokens.map((tok) => (
                <tr key={tok.id} className="border-t border-border align-top">
                  <td className="px-3 py-2">
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-xs text-foreground">
                        {tok.name}
                      </span>
                      {tok.is_superadmin && (
                        <Badge variant="danger">
                          {t('console:apiKeys.superadmin')}
                        </Badge>
                      )}
                    </div>
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    <Badge variant="outline">
                      {tok.is_superadmin
                        ? t('console:apiKeys.superadmin')
                        : tok.role || '—'}
                    </Badge>
                  </td>
                  <td className="px-3 py-2">
                    <RelTimeLabel ts={tok.last_used_at ?? undefined} />
                  </td>
                  <td className="px-3 py-2">
                    <RelTimeLabel ts={tok.created_at} />
                  </td>
                  <td className="px-3 py-2">
                    {tok.expires_at ? (
                      <RelTimeLabel ts={tok.expires_at} />
                    ) : (
                      <span className="text-xs text-muted-foreground">
                        {t('console:apiKeys.noExpiry')}
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setRotating(tok)}
                      >
                        <RefreshCw />
                        {t('console:apiKeys.rotate')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setRevoking(tok)}
                      >
                        <Trash2 />
                        {t('console:apiKeys.revoke')}
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
        <DialogContent className="max-w-md">
          {createOpen && (
            <CreateTokenForm
              onCreated={handleCreated}
              onClose={() => setCreateOpen(false)}
            />
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={t('console:apiKeys.revokeTitle')}
        description={t('console:apiKeys.revokeBody', {
          name: revoking?.name ?? '',
        })}
        confirmLabel={t('console:apiKeys.revoke')}
        tone="danger"
        pending={revokeMutation.isPending}
        onConfirm={() => revoking && revokeMutation.mutate(revoking.id)}
      />

      <ConfirmDialog
        open={rotating !== null}
        onOpenChange={(o) => !o && setRotating(null)}
        title={t('console:apiKeys.rotateTitle')}
        description={t('console:apiKeys.rotateBody', {
          name: rotating?.name ?? '',
        })}
        confirmLabel={t('console:apiKeys.rotate')}
        tone="danger"
        pending={false}
        onConfirm={() => {
          if (!rotating) return
          consoleApi
            .rotateToken(rotating.id)
            .then(handleRotated)
            .catch((err) => {
              toast.error(
                err instanceof Error ? err.message : 'Failed to rotate token',
              )
            })
        }}
      />

      <TokenRevealDialog token={newToken} onClose={() => setNewToken(null)} />
    </div>
  )
}

function CreateTokenForm({
  onCreated,
  onClose,
}: {
  onCreated: (result: IssueTokenResult) => void
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, isSuperadmin } = useAuth()
  const [name, setName] = useState('')
  const [role, setRole] = useState<string>('viewer')
  const [asSuperadmin, setAsSuperadmin] = useState(false)
  const [pending, setPending] = useState(false)

  const valid = name.trim().length > 0

  async function handleCreate() {
    setPending(true)
    try {
      const input: IssueTokenInput = {
        name: name.trim(),
        tenant: asSuperadmin ? '' : (activeTenant ?? ''),
        role: asSuperadmin ? '' : role,
        superadmin: asSuperadmin,
      }
      const result = await consoleApi.issueToken(input)
      onCreated(result)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to create token')
    } finally {
      setPending(false)
    }
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('console:apiKeys.createTitle')}</DialogTitle>
        <DialogDescription>{t('console:apiKeys.createHint')}</DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-4">
        <Field
          label={t('console:apiKeys.fieldName')}
          htmlFor="tok-name"
          required
        >
          <Input
            id="tok-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="ci-deploy"
            mono
          />
        </Field>
        {isSuperadmin && (
          <Field
            label={t('console:apiKeys.fieldScope')}
            htmlFor="tok-scope"
            description={t('console:apiKeys.scopeHint')}
          >
            <Select
              value={asSuperadmin ? 'superadmin' : 'tenant'}
              onValueChange={(v) => setAsSuperadmin(v === 'superadmin')}
            >
              <SelectTrigger id="tok-scope">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="tenant">
                  {t('console:apiKeys.scopeTenant')}
                </SelectItem>
                <SelectItem value="superadmin">
                  {t('console:apiKeys.scopeSuperadmin')}
                </SelectItem>
              </SelectContent>
            </Select>
          </Field>
        )}
        {!asSuperadmin && (
          <Field label={t('console:apiKeys.fieldRole')} htmlFor="tok-role">
            <Select value={role} onValueChange={setRole}>
              <SelectTrigger id="tok-role">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ROLES.map((r) => (
                  <SelectItem key={r} value={r}>
                    {t(`console:apiKeys.roles.${r}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        )}
      </div>
      <DialogFooter>
        <Button variant="secondary" onClick={onClose}>
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => void handleCreate()}
          disabled={!valid || pending}
        >
          {pending && <Spinner size="sm" aria-hidden />}
          {t('console:apiKeys.create')}
        </Button>
      </DialogFooter>
    </>
  )
}

function TokenRevealDialog({
  token,
  onClose,
}: {
  token: string | null
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const [copied, setCopied] = useState(false)

  const handleCopy = useCallback(async () => {
    if (!token) return
    await navigator.clipboard.writeText(token)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }, [token])

  return (
    <Dialog open={token !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('console:apiKeys.revealTitle')}</DialogTitle>
          <DialogDescription>
            {t('console:apiKeys.revealHint')}
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-2 rounded-lg border border-warning/40 bg-warning/5 p-3">
          <ShieldAlert className="size-4 shrink-0 text-warning" aria-hidden />
          <span className="text-sm text-warning">
            {t('console:apiKeys.revealWarning')}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <code className="flex-1 overflow-x-auto rounded-md bg-muted px-3 py-2 font-mono text-xs text-foreground">
            {token}
          </code>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void handleCopy()}
          >
            {copied ? <Check /> : <Copy />}
            {copied ? t('common:actions.copied') : t('common:actions.copy')}
          </Button>
        </div>
        <DialogFooter>
          <Button variant="primary" onClick={onClose}>
            {t('common:actions.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
