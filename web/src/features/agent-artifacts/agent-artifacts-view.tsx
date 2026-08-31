// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Agent Artifacts — tenant-estate registry + its dedicated agent-supply-chain BOM.
// The view records identity/provenance/posture metadata only; it never accepts or
// renders artifact content, and never presents a recorded grade as a console scan.
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Download, FileJson2, PackageSearch, Plus, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { DataTable, type TableColumn } from '@/components/data/data-table'
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
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import {
  ListTruncationBadge,
  CaveatNotice,
  HashChip,
  IntelPage,
  SectionCard,
} from '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { currentLanguage } from '@/lib/i18n'
import { ARTIFACT_PAGE, agentArtifactsApi, agentArtifactsKeys } from './api'
import { downloadJson } from './export'
import type {
  AgentArtifact,
  AgentArtifactClass,
  AgentArtifactInput,
  AibomSeal,
  AibomSealReceipt,
  PostureGrade,
} from './types'
import './i18n'

const ARTIFACT_CLASSES: readonly AgentArtifactClass[] = [
  'skill',
  'mcpb_extension',
  'mcp_app_template',
  'agents_md',
]
const POSTURE_GRADES: readonly PostureGrade[] = ['A', 'B', 'C', 'D', 'F']
const ALL_CLASSES = '__all__'
const NOT_SCANNED = '__not_scanned__'

function displayTime(value: string | undefined, fallback: string): string {
  if (!value) return fallback
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return new Intl.DateTimeFormat(currentLanguage(), {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(parsed)
}

function gradeVariant(grade: PostureGrade): BadgeVariant {
  if (grade === 'A') return 'success'
  if (grade === 'B') return 'info'
  if (grade === 'C') return 'warning'
  return 'danger'
}

function PostureBadge({ artifact }: { artifact: AgentArtifact }) {
  const { t } = useTranslation('agent-artifacts')
  if (!artifact.posture_grade) {
    return <Badge variant="neutral">{t('registry.posture.notScanned')}</Badge>
  }
  return (
    <Badge variant={gradeVariant(artifact.posture_grade)}>
      {t('registry.posture.grade', { grade: artifact.posture_grade })}
    </Badge>
  )
}

function ProvenanceState({ verified }: { verified: boolean }) {
  const { t } = useTranslation('agent-artifacts')
  return (
    <Badge variant={verified ? 'success' : 'neutral'}>
      {t(
        verified ? 'registry.provenance.verified' : 'registry.provenance.claim',
      )}
    </Badge>
  )
}

export function AgentArtifactsView() {
  const { t } = useTranslation('agent-artifacts')
  const { can } = useAuth()

  if (!can('models:registry:read')) return <ForbiddenState />

  return (
    <IntelPage
      icon={PackageSearch}
      title={t('title')}
      description={t('subtitle')}
      notices={<CaveatNotice tone="info">{t('intro')}</CaveatNotice>}
    >
      <Tabs defaultValue="registry">
        <TabsList>
          <TabsTrigger value="registry">{t('tabs.registry')}</TabsTrigger>
          <TabsTrigger value="aibom">{t('tabs.aibom')}</TabsTrigger>
        </TabsList>
        <TabsContent value="registry" className="flex flex-col gap-4">
          <ArtifactRegistry />
        </TabsContent>
        <TabsContent value="aibom" className="flex flex-col gap-4">
          <AgentAibom />
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

function ArtifactRegistry() {
  const report = useFailedActionReporter()
  const { t } = useTranslation(['agent-artifacts', 'common', 'errors'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('models:registry:write')
  const [artifactClass, setArtifactClass] = useState<
    AgentArtifactClass | typeof ALL_CLASSES
  >(ALL_CLASSES)
  const [createOpen, setCreateOpen] = useState(false)
  const [selected, setSelected] = useState<AgentArtifact | null>(null)
  const [deleting, setDeleting] = useState<AgentArtifact | null>(null)

  const selectedClass =
    artifactClass === ALL_CLASSES ? undefined : artifactClass
  // ⛔ EL TECHO SE PIDE Y EL RECORTE SE DICE, aquí también: `handleListAgentArtifacts` publica
  //    `has_more` y sin `limit` el repositorio genérico pagina a 100. Un registro recortado se
  //    lee «éstos son los artefactos del parque», que es la frase con la que alguien concluye
  //    que un agente no tiene ninguno.
  const artifactParams = selectedClass
    ? { artifact_class: selectedClass, limit: ARTIFACT_PAGE }
    : { limit: ARTIFACT_PAGE }
  const query = useQuery({
    queryKey: agentArtifactsKeys.artifacts(
      activeTenant,
      selectedClass,
      artifactParams,
    ),
    queryFn: () => agentArtifactsApi.artifacts(artifactParams),
  })

  const columns = useMemo<TableColumn<AgentArtifact>[]>(
    () => [
      {
        accessorKey: 'artifact_class',
        header: t('registry.columns.class'),
        cell: ({ row }) => (
          <Badge variant="accent">
            {t(`classes.${row.original.artifact_class}`)}
          </Badge>
        ),
      },
      {
        accessorKey: 'name',
        header: t('registry.columns.name'),
        cell: ({ row }) => (
          <span className="font-medium">{row.original.name}</span>
        ),
      },
      {
        accessorKey: 'version',
        header: t('registry.columns.version'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.version || t('values.none')}
          </span>
        ),
      },
      {
        accessorKey: 'provenance',
        header: t('registry.columns.provenance'),
        cell: ({ row }) => (
          <div className="flex min-w-40 flex-col items-start gap-1">
            <span className="max-w-64 truncate text-xs text-muted-foreground">
              {row.original.provenance || t('values.none')}
            </span>
            <ProvenanceState verified={row.original.verified} />
          </div>
        ),
      },
      {
        accessorKey: 'posture_grade',
        header: t('registry.columns.posture'),
        cell: ({ row }) => <PostureBadge artifact={row.original} />,
      },
      {
        accessorKey: 'posture_issues',
        header: t('registry.columns.issues'),
        cell: ({ row }) => (
          <span className="font-mono text-xs tabular-nums">
            {row.original.posture_grade
              ? row.original.posture_issues
              : t('values.none')}
          </span>
        ),
      },
      {
        accessorKey: 'attested_at',
        header: t('registry.columns.attestedAt'),
        cell: ({ row }) => (
          <span className="whitespace-nowrap text-xs text-muted-foreground">
            {displayTime(row.original.attested_at, t('values.none'))}
          </span>
        ),
      },
    ],
    [t],
  )

  const deleteMutation = useMutation({
    mutationFn: (id: string) => agentArtifactsApi.deleteArtifact(id),
    onSuccess: async () => {
      await query.refetch()
      toast.success(t('registry.deleted'))
      setDeleting(null)
      setSelected(null)
    },
    onError: (error) => {
      report(error)
    },
  })

  return (
    <SectionCard
      title={t('registry.title')}
      description={t('registry.description')}
      actions={
        canWrite ? (
          <Button
            variant="primary"
            size="sm"
            onClick={() => setCreateOpen(true)}
          >
            <Plus />
            {t('registry.new')}
          </Button>
        ) : null
      }
      noPadding
    >
      {/* Fuera del bloque de datos: un refetch fallido no debe dejar el aviso flotando. */}
      <ListTruncationBadge
        query={query}
        label={t('registry.truncated', {
          n: query.data?.items?.length ?? 0,
        })}
        hint={t('registry.truncatedHint')}
        className="px-3 pt-3"
        /* ⛔ `filas` PORQUE ESTE AVISO Y EL ESTADO VACIO CUELGAN DE LA MISMA LISTA. El
           `empty` del DataTable de abajo se pinta sobre `query.data?.items`, el mismo que
           cuenta la etiqueta: con `{items: [], has_more: true}` salian a la vez «no hay
           artefactos» y «cargados 0; hay mas», que es un mensaje que se contradice solo.
           Lo nombro the reviewer al re-verificar (F-04). */
        filas={query.data?.items?.length ?? 0}
      />

      <DataTable<AgentArtifact>
        label={t('registry.title')}
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(artifact) => artifact.id}
        onRowClick={setSelected}
        searchable
        searchPlaceholder={t('registry.search')}
        empty={
          <EmptyState
            title={t('registry.empty')}
            description={t('registry.emptyHint')}
          />
        }
        toolbar={
          <Select
            value={artifactClass}
            onValueChange={(value) =>
              setArtifactClass(value as AgentArtifactClass | typeof ALL_CLASSES)
            }
          >
            <SelectTrigger
              className="h-8 w-48 text-xs"
              aria-label={t('registry.filter.label')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_CLASSES}>
                {t('registry.filter.all')}
              </SelectItem>
              {ARTIFACT_CLASSES.map((value) => (
                <SelectItem key={value} value={value}>
                  {t(`classes.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      />

      {canWrite ? (
        <CreateArtifactDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          onSaved={() => void query.refetch()}
        />
      ) : null}

      <ArtifactDrawer
        artifact={selected}
        canDelete={canWrite}
        onClose={() => setSelected(null)}
        onDelete={setDeleting}
      />

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={t('registry.delete.title')}
        description={t('registry.delete.description', {
          name: deleting?.name ?? '',
        })}
        confirmLabel={t('common:actions.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => deleting && deleteMutation.mutate(deleting.id)}
      />
    </SectionCard>
  )
}

function ArtifactDrawer({
  artifact,
  canDelete,
  onClose,
  onDelete,
}: {
  artifact: AgentArtifact | null
  canDelete: boolean
  onClose: () => void
  onDelete: (artifact: AgentArtifact) => void
}) {
  const { t } = useTranslation(['agent-artifacts', 'common'])
  const contentFingerprint = artifact?.content_hash
    ? `${artifact.content_alg || 'sha256'}:${artifact.content_hash}`
    : undefined

  return (
    <Sheet open={!!artifact} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="flex w-full flex-col gap-4 sm:max-w-xl">
        {artifact ? (
          <>
            <SheetHeader>
              <SheetTitle className="pr-8">{artifact.name}</SheetTitle>
              <SheetDescription>
                {t('registry.drawer.description')}
              </SheetDescription>
            </SheetHeader>
            <ScrollArea className="flex-1">
              <div className="flex flex-col gap-4 px-1 pb-4">
                <CaveatNotice tone="info">
                  {t('registry.drawer.identityNotice')}
                </CaveatNotice>
                <CaveatNotice tone="neutral">
                  {t('registry.drawer.postureNotice')}
                </CaveatNotice>
                {canDelete ? (
                  <div>
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={() => onDelete(artifact)}
                    >
                      <Trash2 />
                      {t('common:actions.delete')}
                    </Button>
                  </div>
                ) : null}
                <KvList>
                  <KvRow label={t('registry.drawer.id')} mono>
                    {artifact.id}
                  </KvRow>
                  <KvRow label={t('registry.drawer.class')}>
                    <Badge variant="accent">
                      {t(`classes.${artifact.artifact_class}`)}
                    </Badge>
                  </KvRow>
                  <KvRow label={t('registry.drawer.name')} align="start">
                    {artifact.name}
                  </KvRow>
                  <KvRow label={t('registry.drawer.version')} mono>
                    {artifact.version || t('values.none')}
                  </KvRow>
                  <KvRow label={t('registry.drawer.provenance')} align="start">
                    {artifact.provenance || t('values.none')}
                  </KvRow>
                  <KvRow
                    label={t('registry.drawer.sourceRef')}
                    mono
                    align="start"
                  >
                    {artifact.source_ref || t('values.none')}
                  </KvRow>
                  <KvRow label={t('registry.drawer.contentHash')}>
                    <HashChip hash={contentFingerprint} head={14} tail={10} />
                  </KvRow>
                  <KvRow label={t('registry.drawer.posture')}>
                    <PostureBadge artifact={artifact} />
                  </KvRow>
                  <KvRow label={t('registry.drawer.issues')} mono>
                    {artifact.posture_grade
                      ? artifact.posture_issues
                      : t('values.none')}
                  </KvRow>
                  <KvRow label={t('registry.drawer.verified')}>
                    <ProvenanceState verified={artifact.verified} />
                  </KvRow>
                  <KvRow label={t('registry.drawer.attestedBy')} mono>
                    {artifact.attested_by || t('values.none')}
                  </KvRow>
                  <KvRow label={t('registry.drawer.attestedAt')}>
                    {displayTime(artifact.attested_at, t('values.none'))}
                  </KvRow>
                  <KvRow label={t('registry.drawer.note')} align="start">
                    {artifact.note || t('values.none')}
                  </KvRow>
                </KvList>
                <CaveatNotice tone={artifact.verified ? 'info' : 'neutral'}>
                  {t(
                    artifact.verified
                      ? 'registry.provenance.verifiedHint'
                      : 'registry.provenance.claimHint',
                  )}
                </CaveatNotice>
                <CaveatNotice tone="neutral">
                  {t('registry.posture.hint')}
                </CaveatNotice>
              </div>
            </ScrollArea>
          </>
        ) : null}
      </SheetContent>
    </Sheet>
  )
}

function CreateArtifactDialog({
  open,
  onOpenChange,
  onSaved,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
        {open ? (
          <CreateArtifactForm
            onClose={() => onOpenChange(false)}
            onSaved={onSaved}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

function CreateArtifactForm({
  onClose,
  onSaved,
}: {
  onClose: () => void
  onSaved: () => void
}) {
  const report = useFailedActionReporter()
  const { t } = useTranslation(['agent-artifacts', 'common', 'errors'])
  const { activeTenant } = useAuth()
  const queryClient = useQueryClient()
  const [artifactClass, setArtifactClass] =
    useState<AgentArtifactClass>('skill')
  const [name, setName] = useState('')
  const [version, setVersion] = useState('')
  const [provenance, setProvenance] = useState('')
  const [sourceRef, setSourceRef] = useState('')
  const [contentHash, setContentHash] = useState('')
  const [contentAlg, setContentAlg] = useState('sha256')
  const [posture, setPosture] = useState<PostureGrade | typeof NOT_SCANNED>(
    NOT_SCANNED,
  )
  const [issues, setIssues] = useState(0)
  const [verified, setVerified] = useState(false)
  const [note, setNote] = useState('')
  const [inlineError, setInlineError] = useState<{
    title: string
    message: string
  } | null>(null)

  const valid = name.trim() !== ''
  const mutation = useMutation({
    mutationFn: (body: AgentArtifactInput) =>
      agentArtifactsApi.createArtifact(body),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: agentArtifactsKeys.all(activeTenant),
      })
      toast.success(t('registry.created'))
      onSaved()
      onClose()
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 409) {
        setInlineError({
          title: t('registry.create.duplicateTitle'),
          message: t('registry.create.duplicate'),
        })
        return
      }
      if (error instanceof ApiError && error.status === 400) {
        setInlineError({
          title: t('registry.create.errorTitle'),
          message: error.message,
        })
        return
      }
      report(error)
    },
  })

  function submit() {
    if (!valid) return
    setInlineError(null)
    const graded = posture !== NOT_SCANNED
    const body: AgentArtifactInput = {
      artifact_class: artifactClass,
      name: name.trim(),
      posture_scanned: graded,
      posture_issues: graded ? issues : 0,
      verified,
      ...(version.trim() ? { version: version.trim() } : {}),
      ...(provenance.trim() ? { provenance: provenance.trim() } : {}),
      ...(sourceRef.trim() ? { source_ref: sourceRef.trim() } : {}),
      ...(contentHash.trim()
        ? {
            content_hash: contentHash.trim(),
            content_alg: contentAlg.trim() || 'sha256',
          }
        : {}),
      ...(graded ? { posture_grade: posture } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    }
    mutation.mutate(body)
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('registry.create.title')}</DialogTitle>
        <DialogDescription>
          {t('registry.create.description')}
        </DialogDescription>
      </DialogHeader>

      <CaveatNotice tone="neutral">
        {t('registry.create.uniqueHint')}
      </CaveatNotice>

      {inlineError ? (
        <div
          role="alert"
          className="rounded-md border border-danger-line bg-danger-soft px-3 py-2"
        >
          <p className="text-xs font-medium text-danger">{inlineError.title}</p>
          <p className="mt-1 text-xs text-foreground">{inlineError.message}</p>
        </div>
      ) : null}

      <div className="flex flex-col gap-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('registry.create.class')} htmlFor="aa-class" required>
            <Select
              value={artifactClass}
              onValueChange={(value) =>
                setArtifactClass(value as AgentArtifactClass)
              }
            >
              <SelectTrigger id="aa-class">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ARTIFACT_CLASSES.map((value) => (
                  <SelectItem key={value} value={value}>
                    {t(`classes.${value}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('registry.create.name')} htmlFor="aa-name" required>
            <Input
              id="aa-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('registry.create.version')} htmlFor="aa-version">
            <Input
              id="aa-version"
              value={version}
              onChange={(event) => setVersion(event.target.value)}
              mono
            />
          </Field>
          <Field label={t('registry.create.sourceRef')} htmlFor="aa-source-ref">
            <Input
              id="aa-source-ref"
              value={sourceRef}
              onChange={(event) => setSourceRef(event.target.value)}
              mono
            />
          </Field>
        </div>
        <Field label={t('registry.create.provenance')} htmlFor="aa-provenance">
          <Input
            id="aa-provenance"
            value={provenance}
            onChange={(event) => {
              const value = event.target.value
              setProvenance(value)
              if (!value.trim()) setVerified(false)
            }}
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t('registry.create.contentHash')} htmlFor="aa-hash">
            <Input
              id="aa-hash"
              value={contentHash}
              onChange={(event) => setContentHash(event.target.value)}
              mono
            />
          </Field>
          <Field label={t('registry.create.contentAlg')} htmlFor="aa-hash-alg">
            <Input
              id="aa-hash-alg"
              value={contentAlg}
              onChange={(event) => setContentAlg(event.target.value)}
              disabled={!contentHash.trim()}
              mono
            />
          </Field>
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field
            label={t('registry.create.posture')}
            htmlFor="aa-posture"
            description={t('registry.posture.hint')}
          >
            <Select
              value={posture}
              onValueChange={(value) => {
                const next = value as PostureGrade | typeof NOT_SCANNED
                setPosture(next)
                if (next === NOT_SCANNED) setIssues(0)
              }}
            >
              <SelectTrigger id="aa-posture">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NOT_SCANNED}>
                  {t('registry.posture.notScanned')}
                </SelectItem>
                {POSTURE_GRADES.map((grade) => (
                  <SelectItem key={grade} value={grade}>
                    {t('registry.posture.grade', { grade })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('registry.create.issues')}
            htmlFor="aa-issues"
            description={t('registry.create.issuesHint')}
          >
            <Input
              id="aa-issues"
              type="number"
              min={0}
              step={1}
              value={issues}
              onChange={(event) =>
                setIssues(Math.max(0, Number(event.target.value) || 0))
              }
              disabled={posture === NOT_SCANNED}
              mono
            />
          </Field>
        </div>
        <Field
          label={t('registry.create.verified')}
          htmlFor="aa-verified"
          description={t('registry.create.verifiedHint')}
        >
          <Switch
            id="aa-verified"
            checked={verified}
            onCheckedChange={setVerified}
            disabled={!provenance.trim()}
          />
        </Field>
        <Field label={t('registry.create.note')} htmlFor="aa-note">
          <Textarea
            id="aa-note"
            value={note}
            onChange={(event) => setNote(event.target.value)}
            rows={3}
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
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending ? <Spinner size="sm" aria-hidden /> : null}
          {t('common:actions.create')}
        </Button>
      </DialogFooter>
    </>
  )
}

function AgentAibom() {
  const report = useFailedActionReporter()
  const { t } = useTranslation(['agent-artifacts', 'common', 'errors'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('models:registry:write')
  const [live, setLive] = useState<unknown>(null)
  const [sealConfirmOpen, setSealConfirmOpen] = useState(false)
  const [receipt, setReceipt] = useState<AibomSealReceipt | null>(null)

  // El mismo techo en el ledger: precintar CREA fila, así que crece sin tope.
  const historyParams = { limit: ARTIFACT_PAGE }
  const history = useQuery({
    queryKey: agentArtifactsKeys.aibomSeals(activeTenant, historyParams),
    queryFn: () => agentArtifactsApi.aibomSeals(historyParams),
  })

  const liveMutation = useMutation({
    mutationFn: agentArtifactsApi.liveAibom,
    onSuccess: setLive,
    onError: (error) => {
      toast.error(
        t('errors:generic'),
        error instanceof Error && error.message
          ? { description: error.message }
          : undefined,
      )
    },
  })

  const sealMutation = useMutation({
    mutationFn: agentArtifactsApi.sealAibom,
    onSuccess: async (nextReceipt) => {
      setSealConfirmOpen(false)
      setReceipt(nextReceipt)
      await history.refetch()
      toast.success(t('aibom.seal.success'))
    },
    onError: (error) => {
      report(error)
    },
  })

  return (
    <>
      <CaveatNotice tone="info">{t('aibom.difference')}</CaveatNotice>
      <CaveatNotice tone="neutral">{t('aibom.coverage')}</CaveatNotice>

      <SectionCard
        title={t('aibom.live.title')}
        description={t('aibom.live.description')}
        actions={
          <div className="flex flex-wrap gap-2">
            <Button
              variant="secondary"
              size="sm"
              onClick={() => liveMutation.mutate()}
              disabled={liveMutation.isPending}
            >
              {liveMutation.isPending ? (
                <Spinner size="sm" aria-hidden />
              ) : (
                <FileJson2 />
              )}
              {liveMutation.isPending
                ? t('aibom.live.loading')
                : t('aibom.live.preview')}
            </Button>
            {canWrite ? (
              <Button
                variant="primary"
                size="sm"
                onClick={() => setSealConfirmOpen(true)}
              >
                {t('aibom.seal.action')}
              </Button>
            ) : null}
          </div>
        }
      >
        <CaveatNotice tone="neutral">{t('aibom.live.notice')}</CaveatNotice>
      </SectionCard>

      {/* `truncated` NO es un aviso: es el dato crudo que SealHistory recibe para
          decidir, y ya lleva la regla aplicada (has_more && !error). Por eso no se
          convierte a ListTruncationBadge: no hay nada que pintar aqui. */}
      <SealHistory
        seals={history.data?.items ?? []}
        truncated={!!history.data?.has_more && !history.error}
        loading={history.isLoading}
        error={history.error}
        onRetry={() => void history.refetch()}
      />

      <Dialog
        open={live !== null}
        onOpenChange={(open) => !open && setLive(null)}
      >
        <DialogContent className="max-h-[90vh] max-w-4xl">
          <DialogHeader>
            <DialogTitle>{t('aibom.live.dialogTitle')}</DialogTitle>
            <DialogDescription>
              {t('aibom.live.dialogDescription')}
            </DialogDescription>
          </DialogHeader>
          <ScrollArea className="max-h-[60vh] rounded-md border border-border bg-muted/40 p-3">
            <pre
              aria-label={t('aibom.live.jsonLabel')}
              className="whitespace-pre-wrap break-all font-mono text-xs text-foreground"
            >
              {JSON.stringify(live, null, 2)}
            </pre>
          </ScrollArea>
          <DialogFooter>
            <Button variant="secondary" onClick={() => setLive(null)}>
              {t('common:actions.close')}
            </Button>
            <Button
              variant="primary"
              onClick={() =>
                downloadJson(live, 'agent-artifacts-aibom-live.json')
              }
            >
              <Download />
              {t('aibom.live.download')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={sealConfirmOpen}
        onOpenChange={setSealConfirmOpen}
        title={t('aibom.seal.confirmTitle')}
        description={t('aibom.seal.confirmDescription')}
        confirmLabel={t('aibom.seal.confirm')}
        pending={sealMutation.isPending}
        onConfirm={() => sealMutation.mutate()}
      >
        {t('aibom.seal.confirmBody')}
      </ConfirmDialog>

      <Dialog
        open={receipt !== null}
        onOpenChange={(open) => !open && setReceipt(null)}
      >
        <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t('aibom.seal.receiptTitle')}</DialogTitle>
            <DialogDescription>
              {t('aibom.seal.receiptDescription')}
            </DialogDescription>
          </DialogHeader>
          {receipt ? <SealDetails seal={receipt.seal} /> : null}
          <DialogFooter>
            <Button variant="secondary" onClick={() => setReceipt(null)}>
              {t('common:actions.close')}
            </Button>
            <Button
              variant="primary"
              onClick={() =>
                receipt &&
                downloadJson(
                  receipt.aibom,
                  `agent-artifacts-aibom-sealed-${receipt.seal.id}.json`,
                )
              }
            >
              <Download />
              {t('aibom.seal.download')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function SealHistory({
  seals,
  truncated,
  loading,
  error,
  onRetry,
}: {
  seals: AibomSeal[]
  /** Ya viene resuelto por el llamante como `has_more && !error`: un fallo de lectura no
   *  es «hay más», y el aviso no debe quedarse flotando sobre un estado de error. */
  truncated: boolean
  loading: boolean
  error: unknown
  onRetry: () => void
}) {
  const { t } = useTranslation('agent-artifacts')
  return (
    <SectionCard
      title={t('aibom.history.title')}
      description={t('aibom.history.description')}
    >
      {truncated ? (
        <div className="pb-3">
          <Badge variant="warning" title={t('aibom.history.truncatedHint')}>
            {t('aibom.history.truncated', { n: seals.length })}
          </Badge>
        </div>
      ) : null}

      {loading ? (
        <div
          role="status"
          className="flex items-center justify-center gap-2 py-10 text-sm text-muted-foreground"
        >
          <Spinner size="sm" aria-hidden />
          {t('aibom.live.loading')}
        </div>
      ) : error ? (
        <ErrorState title={t('aibom.history.loadError')} retry={onRetry} />
      ) : seals.length === 0 ? (
        <EmptyState
          title={t('aibom.history.empty')}
          description={t('aibom.history.emptyHint')}
        />
      ) : (
        <ol className="flex flex-col gap-3">
          {seals.map((seal) => (
            <li key={seal.id} className="rounded-lg border border-border p-4">
              <SealDetails seal={seal} />
            </li>
          ))}
        </ol>
      )}
    </SectionCard>
  )
}

function SealDetails({ seal }: { seal: AibomSeal }) {
  const { t } = useTranslation('agent-artifacts')
  return (
    <div className="flex flex-col gap-3">
      <KvList>
        <KvRow label={t('aibom.history.id')} mono>
          {seal.id}
        </KvRow>
        <KvRow label={t('aibom.history.subject')} mono>
          {seal.owned_ref}
        </KvRow>
        <KvRow label={t('aibom.history.serial')} mono align="start">
          {seal.serial_number}
        </KvRow>
        <KvRow label={t('aibom.history.contentHash')}>
          <HashChip hash={seal.content_hash} head={14} tail={10} />
        </KvRow>
        <KvRow label={t('aibom.history.specVersion')} mono>
          {seal.spec_version}
        </KvRow>
        <KvRow label={t('aibom.history.components')} mono>
          {seal.component_count}
        </KvRow>
        <KvRow label={t('aibom.history.ledgerSeq')} mono>
          {seal.ledger_seq === 0
            ? t('aibom.history.noPriorHead', { seq: seal.ledger_seq })
            : seal.ledger_seq}
        </KvRow>
        <KvRow label={t('aibom.history.ledgerHash')}>
          <HashChip hash={seal.ledger_hash} head={14} tail={10} />
        </KvRow>
        <KvRow label={t('aibom.history.generatedBy')} mono>
          {seal.generated_by || t('values.none')}
        </KvRow>
        <KvRow label={t('aibom.history.generatedAt')}>
          {displayTime(seal.generated_at, t('values.none'))}
        </KvRow>
      </KvList>
      {seal.scope_note ? (
        <CaveatNotice tone="neutral">
          <span className="font-medium">{t('aibom.history.scope')}</span>
          <span className="ml-1">{seal.scope_note}</span>
        </CaveatNotice>
      ) : null}
    </div>
  )
}

export default AgentArtifactsView
