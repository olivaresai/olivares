// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Red-team (module XVIII) — the container. Tabs over Targets (the CONSENT surface) /
// Runs (scorecard + per-probe results) / Catalog (taxonomy + framework coverage). It
// wires the queries, the privileged writes (register/authorize a target; launch a run)
// and the RBAC gating, and composes the pure pieces. It computes NO score — the engine
// owns the math; this presents.
//
// Honesty wiring (docs/SECURITY-HARDENING.md): red-team lives under the double-use red line. Runs and
// results are PRIVILEGED, self-audited reads (a SelfAuditNotice is shown). "Launch run"
// is gated by `authorized:true` in the component; the launch dialog only ever targets
// an authorized target. Registering is not consent; a `degraded` run is never a pass.
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Swords } from 'lucide-react'
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
import { EmptyState } from '@/components/ui/empty-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  IntelPage,
  SectionCard,
  SelfAuditNotice,
  ListTruncationBadge,
} from '@/features/_intel'
import { redteamApi, redteamKeys } from './api'
import {
  CatalogTable,
  CoverageStats,
  FamilyFailureChart,
  OwaspFailures,
  ResultsTable,
  RunScorecard,
  RunsTable,
  TargetsTable,
} from './components'
import type { RedTeamSuite, Run, Target } from './types'
import './i18n'

const SUITES: RedTeamSuite[] = [
  'all',
  'injection',
  'jailbreak',
  'exfil',
  'tool_poisoning',
]

export function RedTeamView() {
  const { t } = useTranslation('redteam')

  return (
    <IntelPage
      icon={Swords}
      title={t('title')}
      description={t('description')}
      notices={<SelfAuditNotice />}
    >
      <Tabs defaultValue="targets">
        <TabsList>
          <TabsTrigger value="targets">{t('tabs.targets')}</TabsTrigger>
          <TabsTrigger value="runs">{t('tabs.runs')}</TabsTrigger>
          <TabsTrigger value="catalog">{t('tabs.catalog')}</TabsTrigger>
        </TabsList>

        <TabsContent value="targets" className="flex flex-col gap-4">
          <TargetsTab />
        </TabsContent>

        <TabsContent value="runs" className="flex flex-col gap-4">
          <RunsTab />
        </TabsContent>

        <TabsContent value="catalog" className="flex flex-col gap-4">
          <CatalogTab />
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

// --- targets tab (the consent surface) ---------------------------------------

function TargetsTab() {
  const { t } = useTranslation('redteam')
  const { activeTenant, can } = useAuth()
  const canAdmin = can('redteam:target:admin')
  const canScan = can('redteam:scan:admin')

  const [registerOpen, setRegisterOpen] = useState(false)
  const [authorizeTarget, setAuthorizeTarget] = useState<Target | null>(null)
  const [launchTarget, setLaunchTarget] = useState<Target | null>(null)

  const targetsQ = useQuery({
    queryKey: redteamKeys.targets(activeTenant),
    queryFn: () => redteamApi.targets(),
  })

  return (
    <>
      <SectionCard
        title={t('targets.title')}
        description={t('targets.description')}
        actions={
          canAdmin ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setRegisterOpen(true)}
            >
              {t('targets.register')}
            </Button>
          ) : null
        }
      >
        <CaveatNotice className="mb-3">{t('targets.consentNote')}</CaveatNotice>
        <ListTruncationBadge
          query={targetsQ}
          label={t('targets.truncated', {
            n: targetsQ.data?.items?.length ?? 0,
          })}
          hint={t('targets.truncatedHint')}
        />
        <AsyncSection query={targetsQ} skeletonHeight={220}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState
                title={t('targets.empty')}
                description={t('targets.emptyHint')}
              />
            ) : (
              <TargetsTable
                targets={list.items}
                canAdmin={canAdmin}
                onAuthorize={setAuthorizeTarget}
                onLaunch={canScan ? setLaunchTarget : undefined}
              />
            )
          }
        </AsyncSection>
      </SectionCard>

      {canAdmin ? (
        <RegisterDialog open={registerOpen} onOpenChange={setRegisterOpen} />
      ) : null}
      {canAdmin && authorizeTarget ? (
        <AuthorizeDialog
          target={authorizeTarget}
          onClose={() => setAuthorizeTarget(null)}
        />
      ) : null}
      {canScan && launchTarget ? (
        <LaunchDialog
          target={launchTarget}
          onClose={() => setLaunchTarget(null)}
        />
      ) : null}
    </>
  )
}

// --- register a candidate (admin) — registering is NOT consent ----------------

function RegisterDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['redteam', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [agentRef, setAgentRef] = useState('')
  const [name, setName] = useState('')
  const [endpoint, setEndpoint] = useState('')
  const [scope, setScope] = useState('')

  const create = useMutation({
    mutationFn: () =>
      redteamApi.registerTarget({
        agent_ref: agentRef.trim(),
        name: name.trim() || undefined,
        endpoint: endpoint.trim() || undefined,
        scope: scope.trim() || undefined,
      }),
    onSuccess: () => {
      toast.success(t('targets.dialog.registered'))
      void qc.invalidateQueries({ queryKey: redteamKeys.targets(activeTenant) })
      onOpenChange(false)
      setAgentRef('')
      setName('')
      setEndpoint('')
      setScope('')
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  const valid = agentRef.trim().length > 0

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('targets.dialog.registerTitle')}</DialogTitle>
          <DialogDescription>
            {t('targets.dialog.registerHint')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) create.mutate()
          }}
        >
          <CaveatNotice>{t('targets.dialog.notConsentNote')}</CaveatNotice>
          <Field label={t('targets.dialog.agentRef')} required>
            {({ id }) => (
              <Input
                id={id}
                value={agentRef}
                onChange={(e) => setAgentRef(e.target.value)}
              />
            )}
          </Field>
          <Field label={t('targets.dialog.name')}>
            {({ id }) => (
              <Input
                id={id}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <Field label={t('targets.dialog.endpoint')}>
            {({ id }) => (
              <Input
                id={id}
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
              />
            )}
          </Field>
          <Field
            label={t('targets.dialog.scope')}
            description={t('targets.dialog.scopeHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                value={scope}
                onChange={(e) => setScope(e.target.value)}
              />
            )}
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || create.isPending}
            >
              {t('targets.dialog.registerCta')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- authorize / revoke consent (admin, self-audited) ------------------------

function AuthorizeDialog({
  target,
  onClose,
}: {
  target: Target
  onClose: () => void
}) {
  const { t } = useTranslation(['redteam', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const grant = !target.authorized
  const [scope, setScope] = useState(target.scope)

  const mutate = useMutation({
    mutationFn: () =>
      redteamApi.authorizeTarget(target.id, {
        authorized: grant,
        scope: grant ? scope.trim() || undefined : undefined,
      }),
    onSuccess: () => {
      toast.success(
        grant ? t('targets.dialog.granted') : t('targets.dialog.revoked'),
      )
      void qc.invalidateQueries({ queryKey: redteamKeys.targets(activeTenant) })
      onClose()
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <Dialog open onOpenChange={(v) => (!v ? onClose() : undefined)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {grant
              ? t('targets.dialog.authorizeTitle')
              : t('targets.dialog.revokeTitle')}
          </DialogTitle>
          <DialogDescription>
            {grant
              ? t('targets.dialog.authorizeHint', {
                  name: target.name || target.agent_ref,
                })
              : t('targets.dialog.revokeHint', {
                  name: target.name || target.agent_ref,
                })}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            mutate.mutate()
          }}
        >
          {grant ? (
            <Field
              label={t('targets.dialog.scope')}
              description={t('targets.dialog.scopeHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={scope}
                  onChange={(e) => setScope(e.target.value)}
                />
              )}
            </Field>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant={grant ? 'primary' : 'destructive'}
              disabled={mutate.isPending}
            >
              {grant ? t('targets.authorize') : t('targets.revoke')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- launch a run (admin) — only against an AUTHORIZED target ------------------

function LaunchDialog({
  target,
  onClose,
}: {
  target: Target
  onClose: () => void
}) {
  const { t } = useTranslation(['redteam', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [suite, setSuite] = useState<RedTeamSuite>('all')

  const launch = useMutation({
    mutationFn: () =>
      // The run handler parses target_ref as the consent TARGET id and loads that
      // record by primary key (modules/redteam/scorecard.go:98-116). The governed
      // agent ref is a different identity and would make every real launch 404.
      redteamApi.launchRun({ target_ref: target.id, suite }),
    onSuccess: () => {
      toast.success(t('runs.dialog.launched'))
      void qc.invalidateQueries({ queryKey: redteamKeys.runs(activeTenant) })
      onClose()
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <Dialog open onOpenChange={(v) => (!v ? onClose() : undefined)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('runs.dialog.title')}</DialogTitle>
          <DialogDescription>
            {t('runs.dialog.hint', { name: target.name || target.agent_ref })}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            launch.mutate()
          }}
        >
          <Field label={t('runs.dialog.suite')}>
            <Select
              value={suite}
              onValueChange={(v) => setSuite(v as RedTeamSuite)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SUITES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {t(`suites.${s}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" variant="primary" disabled={launch.isPending}>
              {t('runs.launch')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- runs tab (scorecard + per-probe results) --------------------------------

function RunsTab() {
  const { t } = useTranslation('redteam')
  const { activeTenant } = useAuth()
  const [selected, setSelected] = useState<string | null>(null)

  const runsQ = useQuery({
    queryKey: redteamKeys.runs(activeTenant),
    queryFn: () => redteamApi.runs(),
  })

  return (
    <>
      <SectionCard
        title={t('runs.title')}
        description={t('runs.description')}
        noPadding
      >
        <div className="p-4">
          <ListTruncationBadge
            query={runsQ}
            label={t('runs.truncated', { n: runsQ.data?.items?.length ?? 0 })}
            hint={t('runs.truncatedHint')}
          />
          <AsyncSection query={runsQ} skeletonHeight={200}>
            {(list) =>
              list.items.length === 0 ? (
                <EmptyState
                  title={t('runs.empty')}
                  description={t('runs.emptyHint')}
                />
              ) : (
                <RunsTable
                  runs={list.items}
                  onRowClick={(r) => setSelected(r.id)}
                />
              )
            }
          </AsyncSection>
        </div>
      </SectionCard>

      {selected ? (
        <RunDetail runId={selected} onClose={() => setSelected(null)} />
      ) : null}
    </>
  )
}

function RunDetail({ runId, onClose }: { runId: string; onClose: () => void }) {
  const { t } = useTranslation('redteam')
  const { activeTenant } = useAuth()

  const runQ = useQuery({
    queryKey: redteamKeys.run(activeTenant, runId),
    queryFn: () => redteamApi.run(runId),
  })
  const resultsQ = useQuery({
    queryKey: redteamKeys.results(activeTenant, runId),
    queryFn: () => redteamApi.results(runId),
  })

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h2 className="font-display text-lg font-semibold text-foreground">
          {t('runs.detailTitle')}
        </h2>
        <Button variant="ghost" size="sm" onClick={onClose}>
          {t('runs.closeDetail')}
        </Button>
      </div>
      <AsyncSection query={runQ} skeletonHeight={220}>
        {(run: Run) => (
          <>
            <RunScorecard run={run} />
            <div className="grid gap-4 lg:grid-cols-2">
              <FamilyFailureChart run={run} />
              <OwaspFailures run={run} />
            </div>
          </>
        )}
      </AsyncSection>
      <SectionCard
        title={t('results.title')}
        description={t('results.description')}
        noPadding
      >
        <div className="p-4">
          <AsyncSection query={resultsQ} skeletonHeight={200}>
            {(list) =>
              list.items.length === 0 ? (
                <EmptyState title={t('results.empty')} />
              ) : (
                <ResultsTable results={list.items} />
              )
            }
          </AsyncSection>
        </div>
      </SectionCard>
    </div>
  )
}

// --- catalog tab (taxonomy + framework coverage) -----------------------------

function CatalogTab() {
  const { t } = useTranslation('redteam')
  const { activeTenant } = useAuth()
  const [suite, setSuite] = useState<RedTeamSuite>('all')
  const suiteParam = useMemo(
    () => (suite === 'all' ? undefined : suite),
    [suite],
  )

  const catalogQ = useQuery({
    queryKey: redteamKeys.catalog(activeTenant, suite),
    queryFn: () => redteamApi.catalog(suiteParam),
  })

  return (
    <SectionCard
      title={t('catalog.title')}
      description={t('catalog.description')}
      actions={
        <Select
          value={suite}
          onValueChange={(v) => setSuite(v as RedTeamSuite)}
        >
          <SelectTrigger className="w-44" aria-label={t('catalog.suiteLabel')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SUITES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`suites.${s}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      }
    >
      <CaveatNotice className="mb-3">{t('catalog.metadataNote')}</CaveatNotice>
      <AsyncSection query={catalogQ} skeletonHeight={300}>
        {(catalog) => (
          <div className="flex flex-col gap-4">
            <CoverageStats catalog={catalog} />
            <CatalogTable catalog={catalog} />
          </div>
        )}
      </AsyncSection>
    </SectionCard>
  )
}
