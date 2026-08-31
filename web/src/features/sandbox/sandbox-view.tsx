// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Sandbox (module XVII) — the container. Tabs over runs / comparisons / scenarios.
// It wires the queries (tenant-scoped keys), gates write/admin actions on RBAC, and
// composes the pure pieces. It computes NOTHING: the engine records `runner`,
// `isolated`, `destroyed`, `status`, `score` and the comparison verdict; this view
// presents them literally. Reads of runs/outputs are privileged + self-audited.
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { FlaskConical, Plus } from 'lucide-react'
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
import { toast } from '@/components/ui/toaster'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  AsyncSection,
  CaveatNotice,
  IntelPage,
  ListTruncationBadge,
  SectionCard,
  SeamBadge,
  SelfAuditNotice,
} from '@/features/_intel'
import { sandboxApi, sandboxKeys } from './api'
import {
  ComparisonCard,
  RunsTable,
  ScenariosTable,
  SyntheticDataSeam,
} from './components'
import { RunStreamPanel } from './run-stream'
import { ScenarioCreateDialog } from './scenario-create-dialog'
import type { Run, Scenario } from './types'
import './i18n'

// --- las tres acciones del sandbox (C07-04) ----------------------------------
//
// ⛔ HASTA AHORA LA CONSOLA SÓLO LEÍA: listaba escenarios, ejecuciones y comparaciones, y no podía
//    ejecutar, repetir ni comparar. El sandbox existe para probar ANTES de desplegar, y probar era
//    lo único que no se podía hacer desde aquí.
//
// ⛔ LOS PERMISOS NO SON EL MISMO, y el motor los escalona (`modules/sandbox/sandbox.go`):
//      · ejecutar un escenario y repetir una sesión → `sandbox:run:write`
//      · **COMPARAR → `sandbox:run:admin`**, porque una comparación es evidencia de decisión
//        pre/post-despliegue y se persiste como tal (append-only).
//    Reutilizar el permiso de escritura ofrecería el botón de la decisión a quien sólo ejecuta.
//
// ⛔ Y EL CONFINAMIENTO YA RAZONADO ARRIBA APLICA IGUAL: a un principal confinado la mutación con
//    destino indeterminado se le niega de plano, así que el botón se oculta en vez de prometer un
//    403 garantizado.
function AccionesSandbox({
  tenant,
  confined,
}: {
  tenant: string | null
  confined: boolean
}) {
  const { t } = useTranslation('sandbox')
  const qc = useQueryClient()
  const { can } = useAuth()
  const [replayOpen, setReplayOpen] = useState(false)
  const [compareOpen, setCompareOpen] = useState(false)
  const [sesion, setSesion] = useState('')
  const [escenario, setEscenario] = useState('')
  const [base, setBase] = useState('')
  const [candidato, setCandidato] = useState('')

  const puedeEjecutar = can('sandbox:run:write') && !confined
  const puedeComparar = can('sandbox:run:admin') && !confined

  const refrescar = () => {
    void qc.invalidateQueries({ queryKey: sandboxKeys.runs(tenant) })
    void qc.invalidateQueries({ queryKey: sandboxKeys.comparisons(tenant) })
  }

  const repetir = useMutation({
    mutationFn: () => sandboxApi.replay({ session_ref: sesion.trim() }),
    onSuccess: () => {
      setReplayOpen(false)
      refrescar()
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  const comparar = useMutation({
    mutationFn: () =>
      sandboxApi.compare({
        scenario_ref: escenario.trim() || undefined,
        baseline_variant: base.trim(),
        candidate_variant: candidato.trim(),
      }),
    onSuccess: () => {
      setCompareOpen(false)
      refrescar()
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  if (!puedeEjecutar && !puedeComparar) return null

  return (
    <div className="flex flex-wrap items-center gap-2">
      {puedeEjecutar ? (
        <Button size="sm" variant="outline" onClick={() => setReplayOpen(true)}>
          {t('actions.replay')}
        </Button>
      ) : null}
      {puedeComparar ? (
        <Button
          size="sm"
          variant="outline"
          onClick={() => setCompareOpen(true)}
        >
          {t('actions.compare')}
        </Button>
      ) : null}

      <Dialog open={replayOpen} onOpenChange={setReplayOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('actions.replay')}</DialogTitle>
            {/* ⛔ Sin línea temporal reconstruible el motor devuelve la repetición DEGRADADA con
                cero pasos, «never fabricated» (`runs.go`). Se dice ANTES, porque un resultado de
                cero pasos se lee como «la sesión no hizo nada» y no es eso. */}
            <DialogDescription>{t('actions.replayHint')}</DialogDescription>
          </DialogHeader>
          <Field label={t('actions.sessionRef')}>
            <Input value={sesion} onChange={(e) => setSesion(e.target.value)} />
          </Field>
          <DialogFooter>
            <Button
              disabled={repetir.isPending || sesion.trim() === ''}
              onClick={() => repetir.mutate()}
            >
              {t('actions.run')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={compareOpen} onOpenChange={setCompareOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('actions.compare')}</DialogTitle>
            {/* ⛔ Una comparación es EVIDENCIA DE DECISIÓN y se guarda append-only; por eso su
                permiso es admin y no el de ejecutar. */}
            <DialogDescription>{t('actions.compareHint')}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <Field label={t('actions.scenarioRef')}>
              <Input
                value={escenario}
                onChange={(e) => setEscenario(e.target.value)}
              />
            </Field>
            <Field label={t('actions.baseline')}>
              <Input value={base} onChange={(e) => setBase(e.target.value)} />
            </Field>
            <Field label={t('actions.candidate')}>
              <Input
                value={candidato}
                onChange={(e) => setCandidato(e.target.value)}
              />
            </Field>
          </div>
          <DialogFooter>
            <Button
              disabled={
                comparar.isPending ||
                base.trim() === '' ||
                candidato.trim() === ''
              }
              onClick={() => comparar.mutate()}
            >
              {t('actions.compareRun')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

export function SandboxView() {
  const { t } = useTranslation('sandbox')
  const { activeTenant, can, confinedWorkspace } = useAuth()
  const [openRun, setOpenRun] = useState<Run | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [archiving, setArchiving] = useState<Scenario | null>(null)

  // The EXACT permissions the two routes require (sandbox.go:166,168) — asked as
  // literals, answered by membership of the effective set the engine handed this
  // principal in /v1/auth/whoami. No verb arithmetic, no tier: authoring is
  // write-tier, archiving is admin-tier, and the client does not get to derive that.
  //
  // AND `confinedWorkspace`, which the set CANNOT carry, because this is one of the
  // rules AuthContext warns about: `role AND NOT confined`. Both routes mount with
  // Handle, not HandleEntity, so the authorization request carries no workspace; a
  // confined principal's mutation with an indeterminate target is FORBIDDEN outright
  // (modules/governance/grants.go:723-731), and the store would refuse the repo anyway
  // because sandbox.scenario declares no workspace lineage (schema.go:90-108). A
  // confined editor holds `sandbox:scenario:write` in its effective set, so asking the
  // set alone offers a button whose 403 is not a race — it is guaranteed.
  const confined = !!confinedWorkspace
  const canCreate = can('sandbox:scenario:write') && !confined
  const canArchive = can('sandbox:scenario:admin') && !confined

  const runsQ = useQuery({
    queryKey: sandboxKeys.runs(activeTenant),
    queryFn: () => sandboxApi.runs(),
  })
  const comparisonsQ = useQuery({
    queryKey: sandboxKeys.comparisons(activeTenant),
    queryFn: () => sandboxApi.comparisons(),
  })
  const scenariosQ = useQuery({
    queryKey: sandboxKeys.scenarios(activeTenant),
    queryFn: () => sandboxApi.scenarios(),
  })

  // Archiving is privileged and self-audited server-side; invalidating the scenarios
  // key is what makes the list show the new status without a manual reload.
  const archive = usePrivilegedMutation<string, Scenario>({
    mutationFn: (id) => sandboxApi.archiveScenario(id),
    invalidateKeys: () => [sandboxKeys.scenarios(activeTenant)],
    successMessage: t('scenarios.archive.success'),
    onDone: () => setArchiving(null),
  })

  return (
    <IntelPage
      icon={FlaskConical}
      title={t('title')}
      description={t('description')}
      actions={
        <div className="flex flex-wrap items-center gap-2">
          <AccionesSandbox tenant={activeTenant} confined={confined} />
          <SeamBadge label={t('seam.synthetic')} />
        </div>
      }
      notices={<SelfAuditNotice />}
    >
      <Tabs defaultValue="runs">
        <TabsList>
          <TabsTrigger value="runs">{t('tabs.runs')}</TabsTrigger>
          <TabsTrigger value="comparisons">{t('tabs.comparisons')}</TabsTrigger>
          <TabsTrigger value="scenarios">{t('tabs.scenarios')}</TabsTrigger>
        </TabsList>

        <TabsContent value="runs" className="flex flex-col gap-4">
          <SectionCard
            title={t('runs.title')}
            description={t('runs.description')}
            noPadding
          >
            <div className="p-4">
              <CaveatNotice className="mb-3">
                {t('runs.isolationNote')}
              </CaveatNotice>
              <ListTruncationBadge
                query={runsQ}
                label={t('truncation.label', {
                  n: runsQ.data?.items?.length,
                })}
                hint={t('truncation.hint')}
                className="px-0 pt-0 pb-3"
              />
              <AsyncSection query={runsQ} skeletonHeight={260}>
                {(list) =>
                  list.items.length === 0 ? (
                    <EmptyState title={t('runs.empty')} />
                  ) : (
                    <RunsTable runs={list.items} onRowClick={setOpenRun} />
                  )
                }
              </AsyncSection>
            </div>
          </SectionCard>
        </TabsContent>

        <TabsContent value="comparisons" className="flex flex-col gap-4">
          <SectionCard
            title={t('comparisons.title')}
            description={t('comparisons.description')}
          >
            <ListTruncationBadge
              query={comparisonsQ}
              label={t('truncation.label', {
                n: comparisonsQ.data?.items?.length,
              })}
              hint={t('truncation.hint')}
              className="px-0 pt-0 pb-3"
            />
            <AsyncSection query={comparisonsQ} skeletonHeight={200}>
              {(list) =>
                list.items.length === 0 ? (
                  <EmptyState
                    title={t('comparisons.empty')}
                    description={t('comparisons.emptyHint')}
                  />
                ) : (
                  <div className="grid gap-3 md:grid-cols-2">
                    {list.items.map((c) => (
                      <ComparisonCard key={c.id} comparison={c} />
                    ))}
                  </div>
                )
              }
            </AsyncSection>
          </SectionCard>
        </TabsContent>

        <TabsContent value="scenarios" className="flex flex-col gap-4">
          <SectionCard
            title={t('scenarios.title')}
            description={t('scenarios.description')}
            actions={
              // Not offered at all without the right, rather than offered and then
              // 403'd. The server door stays the real one either way.
              canCreate ? (
                <Button
                  type="button"
                  variant="primary"
                  size="sm"
                  onClick={() => setCreateOpen(true)}
                >
                  <Plus className="size-3.5" aria-hidden />
                  {t('scenarios.create.action')}
                </Button>
              ) : null
            }
          >
            <ListTruncationBadge
              query={scenariosQ}
              label={t('truncation.label', {
                n: scenariosQ.data?.items?.length,
              })}
              hint={t('truncation.hint')}
              className="px-0 pt-0 pb-3"
            />
            <AsyncSection query={scenariosQ} skeletonHeight={200}>
              {(list) =>
                list.items.length === 0 ? (
                  <EmptyState
                    title={t('scenarios.empty')}
                    // An empty list means something different to each principal, and
                    // there are THREE of them, not two: one can author the first
                    // scenario; one holds the right but is confined to a workspace, so
                    // the engine refuses a tenant-level fixture; one simply lacks the
                    // right. A single hint would be wrong for two thirds of them.
                    description={
                      canCreate
                        ? t('scenarios.emptyHintAuthor')
                        : confined
                          ? t('scenarios.emptyHintConfined')
                          : t('scenarios.emptyHintReadOnly')
                    }
                  />
                ) : (
                  <ScenariosTable
                    scenarios={list.items}
                    onArchive={canArchive ? setArchiving : undefined}
                  />
                )
              }
            </AsyncSection>
            <div className="mt-4 border-t border-border pt-3">
              <SyntheticDataSeam />
            </div>
          </SectionCard>
        </TabsContent>
      </Tabs>

      <RunStreamDialog
        run={openRun}
        onOpenChange={(v) => {
          if (!v) setOpenRun(null)
        }}
      />

      <ScenarioCreateDialog open={createOpen} onOpenChange={setCreateOpen} />

      {archiving ? (
        <ConfirmDialog
          open
          onOpenChange={(o) => {
            if (!o) setArchiving(null)
          }}
          title={t('scenarios.archive.confirmTitle')}
          description={t('scenarios.archive.confirmBody', {
            name: archiving.name,
          })}
          tone="danger"
          confirmLabel={t('scenarios.archive.action')}
          pending={archive.isPending}
          onConfirm={() => archive.mutate(archiving.id)}
        />
      ) : null}
    </IntelPage>
  )
}

// --- watching one run (row click) --------------------------------------------

/**
 * The per-run view is the STREAM, not a one-shot JSON read: the engine serves
 * `/runs/{id}/stream` and, until nothing in this console consumed it — an
 * operator could only wait for a run and then read its outputs. The panel owns the
 * connection, its honest state and the stored-outputs fallback (run-stream.tsx).
 */
function RunStreamDialog({
  run,
  onOpenChange,
}: {
  run: Run | null
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation('sandbox')

  return (
    <Dialog open={!!run} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('outputs.title')}</DialogTitle>
          <DialogDescription>
            {run ? (
              <span className="font-mono text-xs">{run.subject_ref}</span>
            ) : null}
          </DialogDescription>
        </DialogHeader>
        {run ? <RunStreamPanel run={run} /> : null}
      </DialogContent>
    </Dialog>
  )
}
