// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Reporting console. Makes the reporting module (modules/reporting) navigable:
// the five built-in reports are generated on demand and downloaded in the operator's
// chosen format + window, and the scheduler surface is shown when the enterprise
// build wires it (an honest "enterprise capability" notice otherwise — never a faked
// scheduler). Zero invented endpoints: the report catalog, generation and schedule
// CRUD all hit routes the backend already serves; PDF that the renderer can't produce
// surfaces the engine's 501 verbatim.
import './i18n'
import { useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { CalendarClock, FileBarChart, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
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
import { toast } from '@/components/ui/toaster'
import { IntelNotice } from '@/features/_intel'
import { isOpenCoreSeam } from '@/lib/api/errors'
import { CaveatNotice } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import type { TenantRequestOptions } from '@/lib/api/client'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  downloadBlob,
  fetchReport,
  isEnterprisePending,
  reportingApi,
  reportingKeys,
  type GenerateParams,
  type ReportFormat,
  type ReportMeta,
  type ScheduleConfig,
} from './api'

export function ReportingView() {
  const { t } = useTranslation(['reporting', 'common'])
  const { can } = useAuth()
  const canRead = can('reporting:report:read')

  const reportsQ = useQuery({
    queryKey: reportingKeys.reports(),
    queryFn: () => reportingApi.listReports(),
    enabled: canRead,
  })

  const [generating, setGenerating] = useState<ReportMeta | null>(null)

  if (!canRead) return <ForbiddenState />

  const reports = reportsQ.data?.items ?? []

  return (
    <div className="flex flex-col gap-6 p-6">
      <PageHeader
        icon={FileBarChart}
        title={t('title')}
        description={t('description')}
      />

      <Card>
        <CardHeader>
          <CardTitle>{t('catalog.title')}</CardTitle>
        </CardHeader>
        <CardContent>
          {reportsQ.isLoading ? (
            <div role="status" className="flex justify-center py-8">
              <span className="sr-only">{t('common:states.loading')}</span>
              <Spinner />
            </div>
          ) : reportsQ.isError ? (
            <ErrorState retry={() => void reportsQ.refetch()} />
          ) : reports.length === 0 ? (
            <EmptyState title={t('catalog.empty')} />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
                    <th className="py-2 pr-4 font-medium">
                      {t('catalog.colReport')}
                    </th>
                    <th className="py-2 pr-4 font-medium">
                      {t('catalog.colFormats')}
                    </th>
                    <th className="py-2 pl-4 text-right font-medium">
                      <span className="sr-only">{t('catalog.colActions')}</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {reports.map((r) => (
                    <tr
                      key={r.type}
                      className="border-b last:border-0 align-top"
                    >
                      <td className="py-3 pr-4">
                        <div className="font-medium text-foreground">
                          {r.title}
                        </div>
                        <div className="mt-0.5 max-w-prose text-xs text-muted-foreground">
                          {r.description}
                        </div>
                      </td>
                      <td className="py-3 pr-4">
                        <div className="flex flex-wrap gap-1">
                          {r.formats.map((f) => (
                            <Badge
                              key={f}
                              variant="outline"
                              className="uppercase"
                            >
                              {f}
                            </Badge>
                          ))}
                        </div>
                      </td>
                      <td className="py-3 pl-4 text-right">
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => setGenerating(r)}
                        >
                          {t('catalog.generate')}
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

      <EnterpriseReportsCard />

      <SchedulesCard canRead={canRead} />

      {generating ? (
        <GenerateDialog
          key={generating.type}
          report={generating}
          onClose={() => setGenerating(null)}
        />
      ) : null}
    </div>
  )
}

// The last month, as YYYY-MM-DD, matching the engine's default window so the form
// shows the same range the backend would apply if the fields were left blank.
// --- los tres informes enterprise (C07-04) -----------------------------------
//
// ⛔ POR QUÉ, y es la operación con más peso comercial que no tenía pantalla: uno de estos tres
//    es **el paquete de evidencia de auditoría FIRMADO** — lo que se le entrega a un auditor
//    externo. `modules/reporting/enterprise.go:89-91` lo sirve, el cliente lo llamaba desde hoy,
//    y no había forma de pedirlo desde la consola.
//
// ⛔ Y LA FRONTERA COMERCIAL NO SE PINTA COMO UNA AVERÍA. Los tres contestan **501** cuando el
//    motor comercial no está cableado (`enterprise.go:104-106`). El propio motor explica el coste
//    de confundirlo (`:141-148`): un add-on caducado llegaba como 500 «failed to build the
//    report», así que **a un cliente que había pagado todo menos ese add-on se le decía que el
//    servidor estaba roto**. Aquí un 501 se dice con calma y por su nombre; cualquier otro fallo
//    se pinta como lo que es.
const ENTERPRISE_REPORTS = [
  { id: 'posture', pedir: () => reportingApi.enterprisePosture() },
  { id: 'risk', pedir: () => reportingApi.enterpriseRisk() },
  { id: 'bundle', pedir: () => reportingApi.enterpriseBundle() },
] as const

function EnterpriseReportsCard() {
  const { t } = useTranslation(['reporting', 'common'])
  const [pidiendo, setPidiendo] = useState<string | null>(null)
  const [seam, setSeam] = useState<string | null>(null)

  async function pedir(id: string, fn: () => Promise<unknown>) {
    setPidiendo(id)
    setSeam(null)
    try {
      const out = await fn()
      descargarJson(`${id}-${new Date().toISOString().slice(0, 10)}.json`, out)
      toast.success(t('enterprise.done'))
    } catch (e) {
      // 501 NO es un fallo: es la frontera open-core, y se dice con su nombre.
      if (isOpenCoreSeam(e)) setSeam(id)
      else toast.error(String((e as Error).message ?? e))
    } finally {
      setPidiendo(null)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('enterprise.title')}</CardTitle>
        <CardDescription>{t('enterprise.description')}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {ENTERPRISE_REPORTS.map((r) => (
          <div
            key={r.id}
            className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border p-3"
          >
            <div className="flex min-w-0 flex-col gap-1">
              <span className="font-medium">
                {t(`enterprise.${r.id}.title`)}
              </span>
              <span className="text-xs text-muted-foreground">
                {t(`enterprise.${r.id}.description`)}
              </span>
              {seam === r.id ? (
                <span className="text-xs text-foreground">
                  {t('enterprise.seam')}
                </span>
              ) : null}
            </div>
            <Button
              variant="ghost"
              size="sm"
              disabled={pidiendo !== null}
              onClick={() => pedir(r.id, r.pedir)}
            >
              {t('enterprise.request')}
            </Button>
          </div>
        ))}
      </CardContent>
    </Card>
  )
}

/** Entrega el informe como fichero. El artefacto es el producto: enseñarlo en un <pre> haría
 *  que un paquete de evidencia firmado pareciera un volcado de depuración. */
function descargarJson(nombre: string, datos: unknown) {
  const blob = new Blob([JSON.stringify(datos, null, 2)], {
    type: 'application/json',
  })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = nombre
  a.click()
  URL.revokeObjectURL(url)
}

function defaultWindow(): { from: string; to: string } {
  const to = new Date()
  const from = new Date(to)
  from.setMonth(from.getMonth() - 1)
  const iso = (d: Date) => d.toISOString().slice(0, 10)
  return { from: iso(from), to: iso(to) }
}

function GenerateDialog({
  report,
  onClose,
}: {
  report: ReportMeta
  onClose: () => void
}) {
  const { t, i18n } = useTranslation(['reporting', 'common'])
  const win = useMemo(() => defaultWindow(), [])
  const [format, setFormat] = useState<ReportFormat>(
    report.formats[0] ?? 'html',
  )
  const [from, setFrom] = useState(win.from)
  const [to, setTo] = useState(win.to)
  const [framework, setFramework] = useState('')
  const [team, setTeam] = useState('')

  const showFramework = report.type === 'compliance-evidence'
  const showTeam = report.type === 'finops-report'

  const mut = useMutation({
    mutationFn: () => {
      const params: GenerateParams = {
        format,
        from,
        to,
        locale: i18n.language,
      }
      if (showFramework && framework.trim()) params.framework = framework.trim()
      if (showTeam && team.trim()) params.team = team.trim()
      return fetchReport(report.type, params)
    },
    onSuccess: (res) => {
      downloadBlob(res.blob, res.filename)
      toast.success(t('generate.done', { title: report.title }))
      onClose()
    },
    onError: (err) => {
      // A 501 = the PDF renderer is not installed in this build; say so honestly
      // and keep the dialog open so the operator can switch to HTML.
      if (err instanceof ApiError && err.status === 501) {
        toast.warning(t('generate.pdfUnavailable'))
        return
      }
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(
        t('generate.failed'),
        description ? { description } : undefined,
      )
    },
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
          <DialogTitle>
            {t('generate.title', { title: report.title })}
          </DialogTitle>
          <DialogDescription>{t('generate.subtitle')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (!mut.isPending) mut.mutate()
          }}
        >
          <Field label={t('generate.format')}>
            {({ id }) => (
              <Select
                value={format}
                onValueChange={(v) => setFormat(v as ReportFormat)}
              >
                <SelectTrigger id={id} aria-label={t('generate.format')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {report.formats.map((f) => (
                    <SelectItem key={f} value={f}>
                      {t(`formats.${f}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('generate.from')}>
              {({ id }) => (
                <Input
                  id={id}
                  type="date"
                  value={from}
                  onChange={(e) => setFrom(e.target.value)}
                />
              )}
            </Field>
            <Field label={t('generate.to')}>
              {({ id }) => (
                <Input
                  id={id}
                  type="date"
                  value={to}
                  onChange={(e) => setTo(e.target.value)}
                />
              )}
            </Field>
          </div>
          {showFramework ? (
            <Field
              label={t('generate.framework')}
              description={t('generate.frameworkHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={framework}
                  onChange={(e) => setFramework(e.target.value)}
                  placeholder={t('generate.frameworkPlaceholder')}
                  autoComplete="off"
                />
              )}
            </Field>
          ) : null}
          {showTeam ? (
            <Field
              label={t('generate.team')}
              description={t('generate.teamHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={team}
                  onChange={(e) => setTeam(e.target.value)}
                  placeholder={t('generate.teamPlaceholder')}
                  autoComplete="off"
                />
              )}
            </Field>
          ) : null}
          <DialogFooter>
            <Button
              variant="ghost"
              type="button"
              onClick={onClose}
              disabled={mut.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button variant="primary" type="submit" disabled={mut.isPending}>
              {mut.isPending && <Spinner size="sm" aria-hidden />}
              {t('generate.download')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// Scheduled reports. The routes 501 until the enterprise scheduler is wired;
// this shows the honest enterprise notice in the community build and the full CRUD
// when a build serves it — no faked scheduler, no invented endpoint.
// --- historial de una programación (C07-04) ----------------------------------
//
// ⛔ POR QUÉ: sin esto, **una programación que lleva semanas fallando se ve igual que una que va
//    bien**. `modules/reporting/enterprise.go:208` lo sirve y la consola no lo llamaba, así que
//    el único síntoma de un informe que no llega era que nadie lo recibía.
//
// ⚠ Y NO SE PRESENTA COMO UN ARCHIVO, porque no lo es: el propio motor lo dice
//    (`enterprise.go:196-198`) — «los proveedores acotan su tamaño y podan las ejecuciones
//    viejas; el historial es una comodidad operativa, no un archivo». Una lista corta NO
//    significa «sólo corrió dos veces», y leerla así al investigar un incidente lleva a una
//    conclusión falsa sobre cuándo empezó el fallo.
function ScheduleRunsDialog({
  scheduleId,
  onClose,
}: {
  scheduleId: string
  onClose: () => void
}) {
  const { t } = useTranslation(['reporting', 'common'])
  const { activeTenant } = useAuth()
  const tenantRequest: TenantRequestOptions = { tenant: activeTenant }

  const q = useQuery({
    queryKey: reportingKeys.scheduleRuns(activeTenant, scheduleId),
    queryFn: () => reportingApi.scheduleRuns(scheduleId, tenantRequest),
  })

  const runs = ((q.data as { items?: unknown[] })?.items ?? []) as Array<{
    id: string
    report_type: string
    format: string
    ran_at: string
    status: string
    error?: string
  }>

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('runs.title')}</DialogTitle>
          <DialogDescription>{t('runs.description')}</DialogDescription>
        </DialogHeader>
        {/* La poda, dicha DONDE se lee la lista: es lo que impide sacar la conclusión falsa. */}
        <IntelNotice tone="info">{t('runs.notAnArchive')}</IntelNotice>
        {q.isLoading ? null : runs.length === 0 ? (
          <EmptyState title={t('runs.empty')} />
        ) : (
          <div className="flex flex-col gap-2">
            {runs.map((r) => (
              <div
                key={r.id}
                className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border p-2"
              >
                <div className="flex min-w-0 items-center gap-2">
                  <Badge variant={r.status === 'ok' ? 'success' : 'danger'}>
                    {r.status}
                  </Badge>
                  <span className="font-mono text-xs">{r.report_type}</span>
                  <span className="text-xs text-muted-foreground">
                    {r.ran_at}
                  </span>
                </div>
                {/* El MOTIVO del fallo, no sólo que falló: sin él, «failed» obliga a ir al log. */}
                {r.error ? (
                  <span className="text-xs text-danger">{r.error}</span>
                ) : null}
              </div>
            ))}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function SchedulesCard({ canRead }: { canRead: boolean }) {
  const { t } = useTranslation(['reporting', 'common'])
  const [runsOf, setRunsOf] = useState<string | null>(null)
  const { activeTenant: tenant, can } = useAuth()
  const tenantRequest: TenantRequestOptions = { tenant }
  const canWrite = can('reporting:report:write')
  const [creating, setCreating] = useState(false)

  const schedulesQ = useQuery({
    queryKey: reportingKeys.schedules(tenant),
    queryFn: () => reportingApi.listSchedules(tenantRequest),
    enabled: canRead,
    retry: false,
  })

  const del = usePrivilegedMutation<
    { id: string; tenant: string | null },
    { deleted: boolean }
  >({
    mutationFn: ({ id, tenant: operationTenant }) =>
      reportingApi.deleteSchedule(id, { tenant: operationTenant }),
    invalidateKeys: (_data, { tenant: operationTenant }) => [
      reportingKeys.schedules(operationTenant),
    ],
    successMessage: t('schedules.deleted'),
  })

  const enterprisePending = isEnterprisePending(schedulesQ.error)
  const schedules = schedulesQ.data?.items ?? []

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <CalendarClock className="size-4 text-muted-foreground" aria-hidden />
          <CardTitle>{t('schedules.title')}</CardTitle>
        </div>
        {!enterprisePending && canWrite ? (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setCreating(true)}
          >
            {t('schedules.new')}
          </Button>
        ) : null}
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {schedulesQ.isLoading ? (
          <div role="status" className="flex justify-center py-6">
            <span className="sr-only">{t('common:states.loading')}</span>
            <Spinner />
          </div>
        ) : enterprisePending ? (
          <CaveatNotice tone="info">{t('schedules.enterprise')}</CaveatNotice>
        ) : schedulesQ.isError ? (
          <ErrorState retry={() => void schedulesQ.refetch()} />
        ) : schedules.length === 0 ? (
          <EmptyState title={t('schedules.empty')} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
                  <th className="py-2 pr-4 font-medium">
                    {t('schedules.colReport')}
                  </th>
                  <th className="py-2 pr-4 font-medium">
                    {t('schedules.colCron')}
                  </th>
                  <th className="py-2 pr-4 font-medium">
                    {t('schedules.colStatus')}
                  </th>
                  <th className="py-2 pl-4 text-right font-medium">
                    <span className="sr-only">{t('schedules.colActions')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {schedules.map((s) => (
                  <tr key={s.id} className="border-b last:border-0">
                    <td className="py-2 pr-4">
                      <span className="font-medium text-foreground">
                        {s.report_type}
                      </span>{' '}
                      <Badge variant="outline" className="uppercase">
                        {s.format}
                      </Badge>
                    </td>
                    <td className="py-2 pr-4 font-mono text-xs">{s.cron}</td>
                    <td className="py-2 pr-4">
                      <Badge variant={s.enabled ? 'success' : 'neutral'}>
                        {s.enabled
                          ? t('schedules.enabled')
                          : t('schedules.disabled')}
                      </Badge>
                    </td>
                    <td className="py-2 pl-4 text-right">
                      {/* El historial es de LECTURA: va con `canRead`, no con `canWrite`. */}
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setRunsOf(s.id)}
                      >
                        {t('runs.open')}
                      </Button>
                      {canWrite ? (
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={t('schedules.delete')}
                          disabled={del.isPending}
                          onClick={() => del.mutate({ id: s.id, tenant })}
                        >
                          <Trash2 className="size-4" aria-hidden />
                        </Button>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
      {runsOf ? (
        <ScheduleRunsDialog
          scheduleId={runsOf}
          onClose={() => setRunsOf(null)}
        />
      ) : null}

      {creating ? (
        <ScheduleDialog tenant={tenant} onClose={() => setCreating(false)} />
      ) : null}
    </Card>
  )
}

function ScheduleDialog({
  tenant,
  onClose,
}: {
  tenant: string | null
  onClose: () => void
}) {
  const { t } = useTranslation(['reporting', 'common'])
  // Server-driven selector: the schedule form offers exactly the types/formats
  // the backend's catalog publishes (GET /reports) — nothing re-coded in the
  // console, so a new or retired report can never drift from this list. The
  // catalog is usually already in the query cache (the view lists it above).
  const catalogQ = useQuery({
    queryKey: reportingKeys.reports(),
    queryFn: () => reportingApi.listReports(),
  })
  const reports = catalogQ.data?.items ?? []

  const [reportType, setReportType] = useState<string>('')
  const [format, setFormat] = useState<string>('')
  const [cron, setCron] = useState('0 8 * * 1')

  // '' = not chosen yet → default to the catalog head / the report's first
  // offered format (and clamp a stale pick after a report-type switch).
  const selected = reports.find((r) => r.type === reportType) ?? reports[0]
  const effectiveType = selected?.type ?? ''
  const formats = selected?.formats ?? []
  const effectiveFormat: ReportFormat | '' = formats.includes(
    format as ReportFormat,
  )
    ? (format as ReportFormat)
    : formats.length > 0
      ? formats[0]
      : ''

  const mut = usePrivilegedMutation<
    {
      config: Omit<ScheduleConfig, 'id'>
      tenant: string | null
    },
    { items: ScheduleConfig[] }
  >({
    mutationFn: ({ config, tenant: operationTenant }) =>
      reportingApi.createSchedule(config, { tenant: operationTenant }),
    invalidateKeys: (_data, { tenant: operationTenant }) => [
      reportingKeys.schedules(operationTenant),
    ],
    successMessage: t('schedules.created'),
    onDone: onClose,
  })

  const ready = effectiveType !== '' && effectiveFormat !== ''

  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o && !mut.isPending) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('schedules.newTitle')}</DialogTitle>
          <DialogDescription>{t('schedules.newSubtitle')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (ready && cron.trim() && !mut.isPending)
              mut.mutate({
                config: {
                  report_type: effectiveType,
                  format: effectiveFormat as ReportFormat,
                  cron: cron.trim(),
                  enabled: true,
                },
                tenant,
              })
          }}
        >
          {catalogQ.isLoading ? (
            <div role="status" className="flex justify-center py-6">
              <span className="sr-only">{t('common:states.loading')}</span>
              <Spinner />
            </div>
          ) : catalogQ.isError ? (
            // Honest fallback: without the server catalog the form does not
            // invent report types — it says so and offers a retry.
            <ErrorState retry={() => void catalogQ.refetch()} />
          ) : (
            <>
              <Field label={t('schedules.report')}>
                {({ id }) => (
                  <Select value={effectiveType} onValueChange={setReportType}>
                    <SelectTrigger id={id} aria-label={t('schedules.report')}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {reports.map((r) => (
                        <SelectItem key={r.type} value={r.type}>
                          {r.title}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </Field>
              <Field label={t('generate.format')}>
                {({ id }) => (
                  <Select value={effectiveFormat} onValueChange={setFormat}>
                    <SelectTrigger id={id} aria-label={t('generate.format')}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {formats.map((f) => (
                        <SelectItem key={f} value={f}>
                          {t(`formats.${f}`)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </Field>
            </>
          )}
          <Field
            label={t('schedules.cron')}
            description={t('schedules.cronHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                value={cron}
                onChange={(e) => setCron(e.target.value)}
                mono
                autoComplete="off"
              />
            )}
          </Field>
          <DialogFooter>
            <Button
              variant="ghost"
              type="button"
              onClick={onClose}
              disabled={mut.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="primary"
              type="submit"
              disabled={!ready || !cron.trim() || mut.isPending}
            >
              {mut.isPending && <Spinner size="sm" aria-hidden />}
              {t('schedules.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export default ReportingView
