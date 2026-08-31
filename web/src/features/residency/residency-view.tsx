// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// data-residency administration. The org region pin was writable only
// by the API; compliance showed it read-only. This superadmin surface lists every
// org with its current pin and lets an operator set/clear it through a two-step
// confirm (form → review), gated by an AAL3 step-up and self-audited server-side.
// The engine validates the region — an unknown region / non-region-scoped instance
// returns 400 naming the known regions, surfaced honestly (never a fabricated list).
import './i18n'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Globe } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { ListTruncationBadge, SelfAuditNotice } from '@/features/_intel'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { residencyApi, residencyKeys, type OrgDTO } from './api'

const CLEAR_PIN_VALUE = '__clear_residency_pin__'

export function ResidencyView() {
  const { t } = useTranslation(['residency', 'common'])
  const { can } = useAuth()
  const isAdmin = can('system:admin')
  const [editing, setEditing] = useState<OrgDTO | null>(null)

  const registryQ = useQuery({
    queryKey: residencyKeys.registry(),
    queryFn: () => residencyApi.getRegistry(),
    enabled: isAdmin,
    // Deployment configuration does not change through this view. Fetch it once
    // and keep org-pin invalidations from needlessly refetching it.
    staleTime: Infinity,
  })
  const orgsQ = useQuery({
    queryKey: residencyKeys.orgs(),
    queryFn: () => residencyApi.listOrgs(),
    enabled: isAdmin,
  })

  if (!isAdmin) return <ForbiddenState />

  const orgs = orgsQ.data?.items ?? []

  return (
    <div className="flex flex-col gap-6 p-6">
      <PageHeader
        icon={Globe}
        title={t('title')}
        description={t('description')}
        actions={
          registryQ.data ? (
            <div
              className="flex flex-wrap items-center justify-end gap-2 text-xs text-muted-foreground"
              aria-label={t('registry.summary')}
            >
              <span>{t('registry.homeRegion')}</span>
              <Badge variant="outline" className="font-mono">
                {registryQ.data.home_region || t('registry.notConfigured')}
              </Badge>
              <span>{t('registry.enforcement')}</span>
              <Badge variant={registryQ.data.enforces ? 'success' : 'neutral'}>
                {registryQ.data.enforces
                  ? t('registry.enforced')
                  : t('registry.notEnforced')}
              </Badge>
            </div>
          ) : undefined
        }
      />
      {registryQ.isError ? (
        <ErrorState retry={() => void registryQ.refetch()} />
      ) : null}
      <Card>
        <CardHeader>
          <CardTitle>{t('orgs.title')}</CardTitle>
        </CardHeader>
        <CardContent>
          {/* El recorte, declarado. El TRIAJE DEL MOTOR —sondas, veredicto y file:line— vive en
              `./api.ts`, y NO aqui a proposito: el censo de recorte
              (`scripts/check-list-truncation-witness.sh`) lee los `.tsx` y solo descarta las lineas de
              comentario que empiezan por `*`, `//` o `/*`. Una explicacion en prosa dentro de una vista
              le colaria los literales que busca y daria la feature por cubierta aunque nadie pintara
              nada — el falso negativo que su propia cabecera nombra. Aqui, un puntero y nada mas. */}
          <ListTruncationBadge
            query={orgsQ}
            label={t('truncation.label', { n: orgs.length })}
            hint={t('truncation.hint')}
            className="px-0 pt-0 pb-3"
          />
          {orgsQ.isLoading ? (
            <div role="status" className="flex justify-center py-8">
              <span className="sr-only">{t('common:states.loading')}</span>
              <Spinner />
            </div>
          ) : orgsQ.isError ? (
            <ErrorState retry={() => void orgsQ.refetch()} />
          ) : orgs.length === 0 ? (
            <EmptyState title={t('orgs.empty')} />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
                    <th className="py-2 pr-4 font-medium">
                      {t('orgs.colOrg')}
                    </th>
                    <th className="py-2 pr-4 font-medium">
                      {t('orgs.colRegion')}
                    </th>
                    <th className="py-2 pl-4 text-right font-medium">
                      <span className="sr-only">{t('orgs.colActions')}</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {orgs.map((o) => (
                    <tr key={o.tenant_id} className="border-b last:border-0">
                      <td className="py-2 pr-4">
                        <div className="font-medium text-foreground">
                          {o.name}
                        </div>
                        <div className="font-mono text-xs text-muted-foreground">
                          {o.slug}
                        </div>
                      </td>
                      <td className="py-2 pr-4">
                        {o.data_region ? (
                          <Badge variant="outline" className="font-mono">
                            {o.data_region}
                          </Badge>
                        ) : (
                          <Badge variant="neutral">{t('orgs.unpinned')}</Badge>
                        )}
                      </td>
                      <td className="py-2 pl-4 text-right">
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => setEditing(o)}
                          disabled={!registryQ.data}
                        >
                          {t('orgs.setRegion')}
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
      {editing ? (
        <RegionDialog
          key={editing.tenant_id}
          org={editing}
          regions={registryQ.data?.regions ?? []}
          onClose={() => setEditing(null)}
        />
      ) : null}
    </div>
  )
}

// The two-step region change: a form (step 1) then a review with the exact effect,
// the honest implication and the audit notice (step 2). Both the write and the audit
// happen server-side; the AAL3 gate replaces the whole body until the session steps up.
function RegionDialog({
  org,
  regions,
  onClose,
}: {
  org: OrgDTO
  regions: string[]
  onClose: () => void
}) {
  const { t } = useTranslation(['residency', 'common'])
  const [step, setStep] = useState<'form' | 'confirm'>('form')
  const [region, setRegion] = useState(org.data_region ?? '')

  const trimmed = region.trim()
  const clearing = trimmed === ''
  const changed = trimmed !== (org.data_region ?? '')
  const hasKnownRegions = regions.length > 0

  const mut = usePrivilegedMutation({
    mutationFn: () => residencyApi.setOrgRegion(org.tenant_id, trimmed),
    invalidateKeys: [residencyKeys.orgs()],
    successMessage: clearing
      ? t('dialog.cleared', { name: org.name })
      : t('dialog.pinned', { name: org.name, region: trimmed }),
    onDone: onClose,
  })

  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o && !mut.isPending) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('dialog.title', { name: org.name })}</DialogTitle>
          <DialogDescription>{t('dialog.subtitle')}</DialogDescription>
        </DialogHeader>
        <RequireAssurance minAal={AAL.HARDWARE} action="residency">
          {step === 'form' ? (
            <form
              className="flex flex-col gap-3"
              onSubmit={(e) => {
                e.preventDefault()
                if (changed) setStep('confirm')
              }}
            >
              <Field
                label={t('dialog.regionLabel')}
                description={t(
                  hasKnownRegions
                    ? 'dialog.knownRegionHint'
                    : 'dialog.fallbackRegionHint',
                )}
              >
                {({ id }) =>
                  hasKnownRegions ? (
                    <Select
                      value={region || CLEAR_PIN_VALUE}
                      onValueChange={(value) =>
                        setRegion(value === CLEAR_PIN_VALUE ? '' : value)
                      }
                    >
                      <SelectTrigger
                        id={id}
                        aria-label={t('dialog.regionLabel')}
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={CLEAR_PIN_VALUE}>
                          {t('dialog.clearPin')}
                        </SelectItem>
                        {regions.map((knownRegion) => (
                          <SelectItem key={knownRegion} value={knownRegion}>
                            {knownRegion}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  ) : (
                    <Input
                      id={id}
                      value={region}
                      onChange={(e) => setRegion(e.target.value)}
                      placeholder={t('dialog.regionPlaceholder')}
                      mono
                      autoComplete="off"
                    />
                  )
                }
              </Field>
              <p className="text-xs text-muted-foreground">
                {t(
                  hasKnownRegions
                    ? 'dialog.selectClearHint'
                    : 'dialog.clearHint',
                )}
              </p>
              <DialogFooter>
                <Button variant="ghost" type="button" onClick={onClose}>
                  {t('common:actions.cancel')}
                </Button>
                <Button variant="primary" type="submit" disabled={!changed}>
                  {t('dialog.review')}
                </Button>
              </DialogFooter>
            </form>
          ) : (
            <div className="flex flex-col gap-3">
              <div className="rounded-md border border-warning-line bg-warning-soft/40 p-3 text-sm text-foreground">
                {clearing
                  ? t('dialog.summaryClear', { name: org.name })
                  : t('dialog.summaryPin', {
                      name: org.name,
                      region: trimmed,
                    })}
              </div>
              <p className="text-sm text-muted-foreground">
                {t('dialog.implications')}
              </p>
              <SelfAuditNotice />
              <DialogFooter>
                <Button
                  variant="ghost"
                  type="button"
                  onClick={() => setStep('form')}
                  disabled={mut.isPending}
                >
                  {t('dialog.back')}
                </Button>
                <Button
                  variant="primary"
                  onClick={() => mut.mutate()}
                  disabled={mut.isPending}
                >
                  {mut.isPending && <Spinner size="sm" aria-hidden />}
                  {t('dialog.apply')}
                </Button>
              </DialogFooter>
            </div>
          )}
        </RequireAssurance>
      </DialogContent>
    </Dialog>
  )
}

export default ResidencyView
