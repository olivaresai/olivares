// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Database, Package, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import {
  IntelNotice,
  ListTruncationBadge,
  listaRecortada,
} from '@/features/_intel'
import { toast } from '@/components/ui/toaster'
import { Button } from '@/components/ui/button'
import { ForbiddenState } from '@/components/ui/error-state'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { useAuth } from '@/lib/auth/context'
import { RelTimeLabel } from '@/features/shared'
import {
  ClassificationBadge,
  EgressBadge,
  EmbedModelBadge,
  EmbedPolicyBadge,
  HashChip,
  ResidencyBadge,
} from './chips'
import { ContextEditorDialog } from './context-editor'
import { DataProductList } from './data-product-list'
import { KbDetailSheet } from './kb-detail'
import { KbEditorDialog } from './kb-editor'
import { LineageDetailSheet } from './lineage-detail'
import { MemoryEditorDialog } from './memory-editor'
import { MemoryList, MemoryPurgeButton } from './memory-list'
import { PromptDetailSheet } from './prompt-detail'
import { PromptEditorDialog } from './prompt-editor'
import {
  fetchMemoryExport,
  knowledgeApi,
  knowledgeKeys,
  type MemoryExportManifest,
} from './api'
import './i18n'
import type {
  ContextPolicyDTO,
  KbDTO,
  LineageDTO,
  MemoryDTO,
  PromptDTO,
} from './types'

type TabKey =
  'kbs' | 'lineage' | 'prompts' | 'memory' | 'context' | 'data-products' | 'dlp'

// --- integridad de la memoria (C07-04) ---------------------------------------
//
// ⛔ `POST /memory/verify` (`modules/knowledge/memory_integrity.go:293`) recalcula el hash de
//    cada fila viva y lo compara con su ANCLA en el ledger firmado. La consola no lo llamaba: la
//    comprobación que dice si lo almacenado sigue siendo lo que se escribió sólo existía en
//    `curl`.
//
// ⛔ SEIS ESTADOS, Y DOS QUE NO SE PUEDEN FUNDIR NUNCA:
//    · `unanchored` es una fila SIN historia en el ledger — una fila forjada.
//    · `legacy_unanchored` es una fila ANTERIOR al anclaje — dato viejo, no una acusación.
//    Fundirlos convierte «esto es de antes» en «alguien forjó una fila», que es una acusación
//    falsa contra una persona; y al revés, esconde una falsificación entre las filas viejas.
//    Los otros cuatro —`content_tampered`, `ledger_mismatch`, `deleted_resurrected`,
//    `verified`— también son distintos entre sí y se cuentan por separado.
//
// ⛔ Y `truncated`: **las CUENTAS son completas, la lista de detalle NO**. Una lista corta con
//    `truncated: true` no significa pocos problemas, y leerla así al valorar un incidente
//    subestima su alcance. Se dice donde se lee la lista.
function MemoryIntegrityPanel({ canAdmin }: { canAdmin: boolean }) {
  const { t } = useTranslation(['knowledge', 'common'])
  const [informe, setInforme] = useState<null | {
    checked: number
    verified: number
    content_tampered: number
    ledger_mismatch: number
    deleted_resurrected: number
    unanchored: number
    legacy_unanchored: number
    entries?: Array<{
      id: string
      agent_ref: string
      key: string
      status: string
    }>
    truncated?: boolean
  }>(null)

  const verificar = useMutation({
    mutationFn: () => knowledgeApi.verifyMemory(),
    onSuccess: (r) => setInforme(r as never),
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  if (!canAdmin) return <ForbiddenState />

  return (
    <div className="flex flex-col gap-4">
      <IntelNotice tone="info">{t('integrity.scope')}</IntelNotice>

      <Button
        variant="primary"
        size="sm"
        className="self-start"
        disabled={verificar.isPending}
        onClick={() => verificar.mutate()}
      >
        {t('integrity.run')}
      </Button>

      {informe ? (
        <>
          <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-4">
            {(
              [
                ['checked', informe.checked],
                ['verified', informe.verified],
                ['contentTampered', informe.content_tampered],
                ['ledgerMismatch', informe.ledger_mismatch],
                ['resurrected', informe.deleted_resurrected],
                // Los dos que NO se funden, con etiquetas que dicen lo que son.
                ['unanchored', informe.unanchored],
                ['legacy', informe.legacy_unanchored],
              ] as const
            ).map(([k, v]) => (
              <div key={k}>
                <dt className="text-xs text-muted-foreground">
                  {t(`integrity.${k}`)}
                </dt>
                <dd className="font-medium">{v}</dd>
              </div>
            ))}
          </dl>

          {/* La lista es acotada; las cuentas de arriba no. Dicho aquí, no en un pie. */}
          {informe.truncated ? (
            <IntelNotice tone="warning">{t('integrity.truncated')}</IntelNotice>
          ) : null}

          {(informe.entries ?? []).length === 0 ? (
            <EmptyState title={t('integrity.allHealthy')} />
          ) : (
            <div className="flex flex-col gap-2">
              {(informe.entries ?? []).map((e) => (
                <div
                  key={e.id}
                  className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border p-2"
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <Badge
                      variant={
                        e.status === 'legacy_unanchored' ? 'neutral' : 'danger'
                      }
                    >
                      {t(`integrity.status.${e.status}`, {
                        defaultValue: e.status,
                      })}
                    </Badge>
                    <span className="font-mono text-xs">{e.agent_ref}</span>
                    <span className="text-xs text-muted-foreground">
                      {e.key}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          )}
        </>
      ) : null}
    </div>
  )
}

// --- escaneos de descubrimiento (C07-04) -------------------------------------
//
// ⛔ AQUÍ HAY DOS OPERACIONES QUE SE LLAMAN IGUAL Y NO SON LA MISMA, y el motor las separa con
//    cuidado:
//      · `POST /kbs/{id}/scan` clasifica lo YA INGERIDO (`discovery.go:371-374`).
//      · `POST /sources/{name}/scan` mira una fuente **SIN ingerirla** — «pulls the source's
//        documents, classifies the RAW bodies in memory and persists labels + the scan evidence —
//        **the bodies themselves are never stored, logged or embedded (zero egress)**»
//        (`discovery.go:476-479`).
//
// ⛔ Y LA CONSECUENCIA QUE HAY QUE DECIR EN PANTALLA, que es la razón de que esto no sea un
//    listado más. El campo `basis` (`discovery.go:41-44`) vale:
//      · `stored` — «the persisted (post-scrub) form» ⇒ es lo que queda DESPUÉS de redactar.
//      · `raw`    — «the pre-redaction body … content not yet minimized».
//
//    ⇒ **CERO hallazgos sobre `stored` significa «la redacción funcionó», NO «no hay dato
//    sensible»**: el dato pudo existir y haber sido eliminado al ingerir. Leer un escaneo
//    `stored` como prueba de «este corpus no tiene PII» es un certificado de salud FALSO — y es
//    justo la frase que alguien escribiría en una auditoría mirando esta pantalla. Un cero sobre
//    `raw` sí dice algo de la fuente.
//
// ⛔ Y `recommended_classification` es **«advisory; NEVER auto-applied»** (`schema.go:193`).
//    Pintarlo como la clasificación del documento afirmaría que se aplicó algo que nadie aplicó.
function ScansPanel({
  canRead,
  canWrite,
}: {
  canRead: boolean
  canWrite: boolean
}) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [fuente, setFuente] = useState('')

  const q = useQuery({
    queryKey: knowledgeKeys.scans(activeTenant, { limit: 25 }),
    queryFn: () => knowledgeApi.scans({ limit: 25 }, { tenant: activeTenant }),
    enabled: canRead,
  })

  const escanearFuente = useMutation({
    mutationFn: ({
      source,
      tenant,
    }: {
      source: string
      tenant: string | null
    }) => knowledgeApi.scanSource(source, { tenant }),
    onSuccess: (_data, { tenant }) => {
      setFuente('')
      void qc.invalidateQueries({ queryKey: knowledgeKeys.scans(tenant) })
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  if (!canRead) return <ForbiddenState />

  const escaneos = ((q.data as { items?: unknown[] })?.items ?? []) as Array<{
    id: string
    scope_kind: string
    scope_ref: string
    basis: string
    docs_scanned: number
    docs_with_hits: number
    redacted_markers: number
    detector_version?: string
    occurred_at?: string
  }>

  return (
    <div className="flex flex-col gap-4">
      <IntelNotice tone="info">{t('scans.basisNotice')}</IntelNotice>

      {canWrite ? (
        <div className="flex flex-wrap items-end gap-2">
          <Field label={t('scans.sourceName')}>
            <Input value={fuente} onChange={(e) => setFuente(e.target.value)} />
          </Field>
          <Button
            size="sm"
            disabled={escanearFuente.isPending || fuente.trim() === ''}
            onClick={() =>
              escanearFuente.mutate({
                source: fuente.trim(),
                tenant: activeTenant,
              })
            }
          >
            {t('scans.scanSource')}
          </Button>
          <span className="text-xs text-muted-foreground">
            {t('scans.scanSourceHint')}
          </span>
        </div>
      ) : null}

      <ListTruncationBadge
        query={q}
        label={t('scans.truncated', { n: escaneos.length })}
        hint={t('scans.truncatedHint')}
        className="px-0 pt-0 pb-0"
      />

      {escaneos.length === 0 ? (
        <EmptyState title={t('scans.empty')} />
      ) : (
        <div className="flex flex-col gap-2">
          {escaneos.map((s) => (
            <div
              key={s.id}
              className="flex flex-col gap-1 rounded-md border border-border p-3"
            >
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="outline">{s.scope_kind}</Badge>
                <span className="font-mono text-xs">{s.scope_ref}</span>
                {/* La BASE, junto al alcance: es lo que decide qué significa el resultado. */}
                <Badge variant={s.basis === 'raw' ? 'warning' : 'neutral'}>
                  {t(`scans.basis.${s.basis}`, { defaultValue: s.basis })}
                </Badge>
              </div>
              <span className="text-xs text-muted-foreground">
                {t('scans.counts', {
                  docs: s.docs_scanned,
                  hits: s.docs_with_hits,
                  markers: s.redacted_markers,
                })}
              </span>
              {/* ⛔ Un CERO no dice lo mismo según la base, y se dice donde se lee el cero. */}
              {s.docs_with_hits === 0 ? (
                <span className="text-xs">
                  {s.basis === 'stored'
                    ? t('scans.zeroOnStored')
                    : t('scans.zeroOnRaw')}
                </span>
              ) : null}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// --- reglas DLP (C07-04) -----------------------------------------------------
//
// ⛔ POR QUÉ ESTA PANTALLA ES DELICADA: la política DLP es DENY-CLOSED y tiene TRES estados que
//    un listado de reglas a secas funde en uno. Las dos confusiones posibles van, las dos, en la
//    dirección peligrosa — hacen creer que se está protegido cuando no se está, o al revés.
//    Todo esto lo declara `modules/knowledge/dlp.go:28-40,84-92`:
//
//    1. CERO REGLAS = **la puerta está inerte**, DLP no configurado. NO es «todo bloqueado». Un
//       operador que vea una lista vacía y entienda «nada sale» tiene el bloqueo al revés.
//    2. CON REGLAS = activo. Una clase etiquetada sin regla exacta cae al `*`; **si no hay `*`,
//       DENIEGA**. Y una acción desconocida o basura también deniega.
//    3. CONTENIDO SIN ESCANEAR = denegado **salvo una regla explícita `unscanned: allow`**, y
//       **`*` NO lo cubre a propósito**: una sensibilidad que no se puede probar necesita su
//       propia exención. Es la línea que un listado plano invita a leer mal — «tengo un `*`
//       allow, luego todo sale» es falso justo donde más importa.
function DlpPanel({
  canRead,
  canAdmin,
}: {
  canRead: boolean
  canAdmin: boolean
}) {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [editor, setEditor] = useState(false)
  const [clase, setClase] = useState('')
  const [accion, setAccion] = useState<'allow' | 'deny'>('deny')

  const q = useQuery({
    queryKey: knowledgeKeys.dlpRules(activeTenant),
    queryFn: () => knowledgeApi.dlpRules({ tenant: activeTenant }),
    enabled: canRead,
  })

  const refrescar = (tenant: string | null) =>
    void qc.invalidateQueries({
      queryKey: knowledgeKeys.dlpRules(tenant),
    })

  const guardar = useMutation({
    mutationFn: ({
      rule,
      tenant,
    }: {
      rule: { class: string; action: 'allow' | 'deny' }
      tenant: string | null
    }) => knowledgeApi.setDlpRules(rule, { tenant }),
    onSuccess: (_data, { tenant }) => {
      setEditor(false)
      setClase('')
      refrescar(tenant)
    },
  })

  const borrar = useMutation({
    mutationFn: ({ id, tenant }: { id: string; tenant: string | null }) =>
      knowledgeApi.deleteDlpRule(id, { tenant }),
    onSuccess: (_data, { tenant }) => refrescar(tenant),
  })

  if (!canRead) return <ForbiddenState />

  const reglas = ((q.data as { items?: unknown[] })?.items ?? []) as Array<{
    id: string
    class: string
    action: string
    note?: string
    created_by?: string
  }>
  const configurado = reglas.length > 0
  const permiteSinEscanear = reglas.some(
    (r) => r.class === 'unscanned' && r.action === 'allow',
  )
  // ⛔ LA ASIMETRIA ES EL CONTRATO, y por eso esto no es un ternario de dos ramas.
  //    `reglas` es UNA PAGINA (el motor sirve `defaultLimit` = 100 y publica `has_more`).
  //    Encontrar la regla permisiva PRUEBA que lo no analizado se permite: el recorte no
  //    invalida una prueba positiva. NO encontrarla no prueba NADA si faltan filas — la
  //    regla puede estar mas alla de la pagina. Decir «denegado» ahi seria afirmar la
  //    postura SEGURA sin haberla verificado, que es la polaridad invertida que el
  //    contraste `sol max` de encontro en cinco paginas de datos gobernados.
  // ⛔ Y EL ERROR CUENTA COMO DUDA, no como ausencia. `listaRecortada` exige `!error`
  //    a proposito —no declara un recorte que no puede confirmar—, asi que con datos
  //    RANCIOS conservados y un refetch fallido devolveria `false` y el veredicto caeria
  //    a «denegado»: la misma polaridad invertida, entrando por la puerta de atras. Un
  //    fallo de lectura no es una prueba de que no exista la regla permisiva.
  const veredictoSinEscanear: 'allowed' | 'undetermined' | 'denied' =
    permiteSinEscanear
      ? 'allowed'
      : listaRecortada(q) || q.isError
        ? 'undetermined'
        : 'denied'

  return (
    <div className="flex flex-col gap-4">
      {/* El estado de la PUERTA, antes que la lista: es lo que decide qué significa la lista. */}
      <IntelNotice tone={configurado ? 'info' : 'warning'}>
        {configurado ? t('dlp.enabled') : t('dlp.inert')}
      </IntelNotice>

      {configurado ? (
        <IntelNotice
          tone={veredictoSinEscanear === 'denied' ? 'info' : 'warning'}
        >
          {veredictoSinEscanear === 'allowed'
            ? t('dlp.unscannedAllowed')
            : veredictoSinEscanear === 'undetermined'
              ? t('dlp.unscannedUndetermined')
              : t('dlp.unscannedDenied')}
        </IntelNotice>
      ) : null}

      <ListTruncationBadge
        query={q}
        label={t('dlp.truncated', { n: reglas.length })}
        hint={t('dlp.truncatedHint')}
        className="px-0 pt-0 pb-0"
      />

      {reglas.length === 0 ? (
        <EmptyState title={t('dlp.empty')} description={t('dlp.emptyHint')} />
      ) : (
        <div className="flex flex-col gap-2">
          {reglas.map((r) => (
            <div
              key={r.id}
              className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border p-3"
            >
              <div className="flex min-w-0 items-center gap-2">
                <Badge variant={r.action === 'allow' ? 'success' : 'danger'}>
                  {r.action}
                </Badge>
                <span className="font-mono text-sm">{r.class}</span>
                {/* El `*` se explica DONDE se lee, no en una nota al pie: su alcance es
                    justo lo que se malinterpreta. */}
                {r.class === '*' ? (
                  <span className="text-xs text-muted-foreground">
                    {t('dlp.anyScope')}
                  </span>
                ) : null}
              </div>
              <div className="flex items-center gap-2">
                {r.note ? (
                  <span className="text-xs text-muted-foreground">
                    {r.note}
                  </span>
                ) : null}
                {canAdmin ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t('dlp.deleteRule', { class: r.class })}
                    onClick={() =>
                      borrar.mutate({ id: r.id, tenant: activeTenant })
                    }
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                ) : null}
              </div>
            </div>
          ))}
        </div>
      )}

      {/* ⛔ EL PERMISO ES `knowledge:dlp:admin` (`knowledge.go:356-357`), NO el de lectura ni un
          `:write` intermedio: escribir política de egreso es gobernanza privilegiada y
          autoauditada. Con el permiso de lectura, un lector vería botones que el motor le niega. */}
      {canAdmin ? (
        <div className="flex justify-end">
          <Button size="sm" onClick={() => setEditor(true)}>
            {t('dlp.newRule')}
          </Button>
        </div>
      ) : null}

      <Dialog open={editor} onOpenChange={setEditor}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('dlp.newRule')}</DialogTitle>
            <DialogDescription>{t('dlp.newRuleHint')}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <Field label={t('dlp.class')}>
              <Input value={clase} onChange={(e) => setClase(e.target.value)} />
            </Field>
            <Field label={t('dlp.action')}>
              <Select
                value={accion}
                onValueChange={(v) => setAccion(v as 'allow' | 'deny')}
              >
                <SelectTrigger aria-label={t('dlp.action')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {/* Sólo las dos que el motor acepta: cualquier otra es un 400
                      (`dlp.go:204-207`). Un campo libre inventaría acciones que no existen. */}
                  <SelectItem value="deny">deny</SelectItem>
                  <SelectItem value="allow">allow</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            {/* ⛔ EL AVISO QUE HACE FALTA: `PUT /dlp/rules` es un UPSERT POR CLASE
                (`dlp.go:193,222-232`), no un alta. Escribir una clase que ya tiene regla
                SUSTITUYE la que había, sin preguntar y sin dejar rastro en la pantalla. Un
                diálogo titulado «nueva regla» que en realidad reemplaza una política de egreso
                vigente es la forma más silenciosa de abrir un perímetro cerrado. */}
            {(() => {
              // ⛔ MISMA ASIMETRIA QUE EL VEREDICTO DE ARRIBA, y aqui muerde al ESCRIBIR.
              //    `reglas` es una pagina. Encontrar la clase PRUEBA que guardar sustituye;
              //    no encontrarla no prueba nada si faltan filas, y `PUT /dlp/rules` hace
              //    upsert por clase igual (`dlp.go:193,222-232`). Callar el aviso porque la
              //    regla no cabia en la pagina es la forma silenciosa de sustituir una
              //    politica de egreso vigente. El motor no expone consulta por clase
              //    —solo `GET /dlp/rules`—, asi que el remedio es declarar la duda.
              const claseNorm = clase.trim().toLowerCase()
              if (claseNorm === '') return null
              if (reglas.some((r) => r.class === claseNorm)) {
                return (
                  <IntelNotice tone="warning">
                    {t('dlp.willReplace', { class: claseNorm })}
                  </IntelNotice>
                )
              }
              if (listaRecortada(q)) {
                return (
                  <IntelNotice tone="warning">
                    {t('dlp.mayReplace', { class: claseNorm })}
                  </IntelNotice>
                )
              }
              return null
            })()}
          </div>
          <DialogFooter>
            <Button
              disabled={guardar.isPending || clase.trim() === ''}
              onClick={() =>
                guardar.mutate({
                  rule: { class: clase.trim(), action: accion },
                  tenant: activeTenant,
                })
              }
            >
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

export default function KnowledgeView() {
  const { t } = useTranslation(['knowledge', 'common'])
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()

  const canReadKb = can('knowledge:kb:read')
  const canWriteKb = can('knowledge:kb:write')
  const canReadLineage = can('knowledge:lineage:read')
  const canReadPrompt = can('knowledge:prompt:read')
  const canWritePrompt = can('knowledge:prompt:write')
  const canReadMemory = can('knowledge:memory:read')
  const canWriteMemory = can('knowledge:memory:write')
  const canAdminMemory = can('knowledge:memory:admin')
  const canReadContext = can('knowledge:context:read')
  const canWriteContext = can('knowledge:context:write')
  const canReadDP = can('knowledge:data_product:read')
  const canWriteDP = can('knowledge:data_product:write')

  const [tab, setTab] = useState<TabKey>('kbs')

  // KB tab state.
  const [kbStatus, setKbStatus] = useState<string>('all')
  const [selectedKb, setSelectedKb] = useState<string | null>(null)
  const [kbDetailOpen, setKbDetailOpen] = useState(false)
  const [kbEditorOpen, setKbEditorOpen] = useState(false)

  // Lineage tab state.
  const [lineageDecision, setLineageDecision] = useState<string>('all')
  const [selectedLineage, setSelectedLineage] = useState<string | null>(null)
  const [lineageDetailOpen, setLineageDetailOpen] = useState(false)

  // Prompt tab state.
  const [selectedPrompt, setSelectedPrompt] = useState<string | null>(null)
  const [promptDetailOpen, setPromptDetailOpen] = useState(false)
  const [promptEditorOpen, setPromptEditorOpen] = useState(false)

  // Memory tab state.
  const [memoryAgent, setMemoryAgent] = useState('')
  const [memoryEditorOpen, setMemoryEditorOpen] = useState(false)

  // Context tab state.
  const [contextEditorOpen, setContextEditorOpen] = useState(false)
  const [editingPolicy, setEditingPolicy] = useState<ContextPolicyDTO | null>(
    null,
  )

  const kbParams = kbStatus === 'all' ? undefined : { status: kbStatus }
  const kbs = useQuery({
    queryKey: knowledgeKeys.kbs(activeTenant, kbParams ?? null),
    queryFn: () => knowledgeApi.listKbs(kbParams),
    enabled: tab === 'kbs' && canReadKb,
  })

  const lineageParams =
    lineageDecision === 'all' ? undefined : { decision: lineageDecision }
  const lineage = useQuery({
    queryKey: knowledgeKeys.lineage(activeTenant, lineageParams ?? null),
    queryFn: () => knowledgeApi.listLineage(lineageParams),
    enabled: tab === 'lineage' && canReadLineage,
    // Lineage is append-only and grows per query; poll while the tab is open.
    refetchInterval: tab === 'lineage' && canReadLineage ? 20_000 : false,
  })

  const prompts = useQuery({
    queryKey: knowledgeKeys.prompts(activeTenant),
    queryFn: () => knowledgeApi.listPrompts(),
    enabled: tab === 'prompts' && canReadPrompt,
  })

  const memoryParams = memoryAgent.trim()
    ? { agent_ref: memoryAgent.trim() }
    : undefined
  const memory = useQuery({
    queryKey: knowledgeKeys.memory(activeTenant, memoryParams ?? null),
    queryFn: () => knowledgeApi.listMemory(memoryParams),
    enabled: tab === 'memory' && canReadMemory,
  })

  const contextPolicies = useQuery({
    queryKey: knowledgeKeys.contextPolicies(activeTenant),
    queryFn: () => knowledgeApi.listContextPolicies(),
    enabled: tab === 'context' && canReadContext,
  })

  function refresh() {
    void queryClient.invalidateQueries({
      queryKey: knowledgeKeys.all(activeTenant),
    })
  }

  const kbColumns: TableColumn<KbDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('kbs.name'),
      cell: ({ row }) => (
        <span className="font-medium text-foreground">{row.original.name}</span>
      ),
    },
    {
      accessorKey: 'classification',
      header: t('common.classification'),
      cell: ({ row }) => (
        <ClassificationBadge value={row.original.classification} />
      ),
    },
    {
      accessorKey: 'residency_region',
      header: t('common.residency'),
      cell: ({ row }) => (
        <ResidencyBadge value={row.original.residency_region} />
      ),
    },
    {
      accessorKey: 'embed_policy',
      header: t('common.embedPolicy'),
      cell: ({ row }) => <EmbedPolicyBadge value={row.original.embed_policy} />,
    },
    {
      accessorKey: 'embed_model',
      header: t('common.embedModel'),
      cell: ({ row }) => <EmbedModelBadge value={row.original.embed_model} />,
    },
    {
      accessorKey: 'doc_count',
      header: t('kbs.docs'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">{row.original.doc_count}</span>
      ),
    },
    {
      accessorKey: 'chunk_count',
      header: t('kbs.chunks'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {row.original.chunk_count}
        </span>
      ),
    },
    {
      accessorKey: 'status',
      header: t('common.status'),
      cell: ({ row }) => <StatusBadge status={row.original.status} />,
    },
  ]

  const lineageColumns: TableColumn<LineageDTO, unknown>[] = [
    {
      accessorKey: 'kb_ref',
      header: t('lineage.kbRef'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-foreground">
          {row.original.kb_ref}
        </span>
      ),
    },
    {
      accessorKey: 'agent_ref',
      header: t('lineage.agentRef'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-muted-foreground">
          {row.original.agent_ref}
        </span>
      ),
    },
    {
      accessorKey: 'decision',
      header: t('lineage.decision'),
      cell: ({ row }) => (
        <Badge
          variant={row.original.decision === 'denied' ? 'danger' : 'success'}
        >
          {row.original.decision === 'denied'
            ? t('lineage.denied')
            : t('lineage.allowed')}
        </Badge>
      ),
    },
    {
      accessorKey: 'egress',
      header: t('lineage.egress'),
      cell: ({ row }) => <EgressBadge value={row.original.egress} />,
    },
    {
      id: 'query_hash',
      header: t('lineage.queryHash'),
      cell: ({ row }) => <HashChip value={row.original.query_hash} />,
    },
    {
      accessorKey: 'result_count',
      header: t('lineage.results'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {row.original.result_count}
        </span>
      ),
    },
    {
      accessorKey: 'occurred_at',
      header: t('lineage.occurredAt'),
      cell: ({ row }) => <RelTimeLabel ts={row.original.occurred_at} />,
    },
  ]

  const promptColumns: TableColumn<PromptDTO, unknown>[] = [
    {
      accessorKey: 'name',
      header: t('prompts.name'),
      cell: ({ row }) => (
        <span className="font-mono text-xs font-medium text-foreground">
          {row.original.name}
        </span>
      ),
    },
    {
      accessorKey: 'current_rev',
      header: t('prompts.currentRev'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {row.original.current_rev}
        </span>
      ),
    },
    {
      id: 'latest_hash',
      header: t('prompts.latestHash'),
      cell: ({ row }) => <HashChip value={row.original.latest_hash} />,
    },
    {
      accessorKey: 'status',
      header: t('prompts.status'),
      cell: ({ row }) => <StatusBadge status={row.original.status} />,
    },
  ]

  const contextColumns: TableColumn<ContextPolicyDTO, unknown>[] = [
    {
      accessorKey: 'scope_kind',
      header: t('context.scopeKind'),
      cell: ({ row }) => (
        <Badge variant="neutral">
          {t(`context.scopeKinds.${row.original.scope_kind}`, {
            defaultValue: row.original.scope_kind,
          })}
        </Badge>
      ),
    },
    {
      accessorKey: 'scope_ref',
      header: t('context.scopeRef'),
      cell: ({ row }) => (
        <span className="font-mono text-xs text-foreground">
          {row.original.scope_ref}
        </span>
      ),
    },
    {
      accessorKey: 'strategy',
      header: t('context.strategy'),
      cell: ({ row }) => (
        <Badge variant="outline">
          {t(`context.strategies.${row.original.strategy}`, {
            defaultValue: row.original.strategy,
          })}
        </Badge>
      ),
    },
    {
      accessorKey: 'max_tokens',
      header: t('context.maxTokens'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">
          {row.original.max_tokens}
        </span>
      ),
    },
    {
      accessorKey: 'redaction_required',
      header: t('context.redaction'),
      cell: ({ row }) => (
        <>
          <span aria-hidden="true">
            {row.original.redaction_required ? '✓' : '—'}
          </span>
          <span className="sr-only">
            {t(
              row.original.redaction_required
                ? 'common:states.yes'
                : 'common:states.no',
            )}
          </span>
        </>
      ),
    },
  ]

  return (
    <div className="flex flex-col gap-5 pb-10">
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={Database}
        actions={
          <Button variant="ghost" size="sm" onClick={refresh}>
            <RefreshCw />
            {t('common:actions.refresh')}
          </Button>
        }
      />

      <Tabs value={tab} onValueChange={(v) => setTab(v as TabKey)}>
        <TabsList>
          <TabsTrigger value="kbs">{t('tabs.kbs')}</TabsTrigger>
          <TabsTrigger value="lineage">{t('tabs.lineage')}</TabsTrigger>
          <TabsTrigger value="prompts">{t('tabs.prompts')}</TabsTrigger>
          <TabsTrigger value="memory">{t('tabs.memory')}</TabsTrigger>
          <TabsTrigger value="context">{t('tabs.context')}</TabsTrigger>
          <TabsTrigger value="dlp">{t('tabs.dlp')}</TabsTrigger>
          <TabsTrigger value="scans">{t('tabs.scans')}</TabsTrigger>
          <TabsTrigger value="integrity">{t('tabs.integrity')}</TabsTrigger>
          <TabsTrigger value="data-products">
            <Package className="mr-1 size-3.5" />
            {t('tabs.dataProducts')}
          </TabsTrigger>
        </TabsList>

        {/* Knowledge bases. */}
        <TabsContent value="kbs">
          {!canReadKb ? (
            <ForbiddenState />
          ) : (
            <>
              {/* El motor sirve UNA pagina (defaultLimit=100, sqlstore/generic.go:28) y publica
                  `has_more`. Sin este aviso, el numero de filas de la tabla se lee como un censo.
                  La regla vive en `_intel/notices.tsx` y exige `has_more === true`. */}
              <ListTruncationBadge
                query={kbs}
                label={t('kbs.truncated', {
                  n: kbs.data?.items?.length ?? 0,
                })}
                hint={t('kbs.truncatedHint')}
                className="px-0 pt-0 pb-3"
              />
              <DataTable
                columns={kbColumns}
                data={kbs.data?.items ?? []}
                isLoading={kbs.isLoading}
                error={kbs.error}
                onRetry={() => kbs.refetch()}
                searchable
                searchPlaceholder={t('kbs.search')}
                getRowId={(r) => r.id}
                onRowClick={(r) => {
                  setSelectedKb(r.id)
                  setKbDetailOpen(true)
                }}
                toolbar={
                  <div className="flex items-center gap-2">
                    <Select value={kbStatus} onValueChange={setKbStatus}>
                      <SelectTrigger
                        aria-label={t('kbs.statusFilter')}
                        className="w-[10rem]"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="all">
                          {t('kbs.statusAll')}
                        </SelectItem>
                        <SelectItem value="active">
                          {t('kbs.statusActive')}
                        </SelectItem>
                        <SelectItem value="archived">
                          {t('kbs.statusArchived')}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                    {canWriteKb && (
                      <Button
                        variant="primary"
                        size="sm"
                        onClick={() => setKbEditorOpen(true)}
                      >
                        <Plus />
                        {t('kbs.newKb')}
                      </Button>
                    )}
                  </div>
                }
                empty={
                  <EmptyState
                    title={t('empty.kb.title')}
                    description={t('empty.kb.description')}
                  />
                }
              />
            </>
          )}
        </TabsContent>

        {/* Lineage. */}
        <TabsContent value="lineage">
          {!canReadLineage ? (
            <ForbiddenState />
          ) : (
            <>
              <p className="mb-3 text-xs text-muted-foreground">
                {t('lineage.selfAudited')}
              </p>
              <ListTruncationBadge
                query={lineage}
                label={t('lineage.truncated', {
                  n: lineage.data?.items?.length ?? 0,
                })}
                hint={t('lineage.truncatedHint')}
                className="px-0 pt-0 pb-3"
              />
              <DataTable
                columns={lineageColumns}
                data={lineage.data?.items ?? []}
                isLoading={lineage.isLoading}
                error={lineage.error}
                onRetry={() => lineage.refetch()}
                searchable
                searchPlaceholder={t('lineage.search')}
                getRowId={(r) => r.id}
                onRowClick={(r) => {
                  setSelectedLineage(r.id)
                  setLineageDetailOpen(true)
                }}
                toolbar={
                  <Select
                    value={lineageDecision}
                    onValueChange={setLineageDecision}
                  >
                    <SelectTrigger
                      aria-label={t('lineage.decisionFilter')}
                      className="w-[10rem]"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">
                        {t('lineage.decisionAll')}
                      </SelectItem>
                      <SelectItem value="allowed">
                        {t('lineage.allowed')}
                      </SelectItem>
                      <SelectItem value="denied">
                        {t('lineage.denied')}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                }
                empty={
                  <EmptyState
                    title={t('empty.lineage.title')}
                    description={t('empty.lineage.description')}
                  />
                }
              />
            </>
          )}
        </TabsContent>

        {/* Prompts. */}
        <TabsContent value="prompts">
          {!canReadPrompt ? (
            <ForbiddenState />
          ) : (
            <>
              <ListTruncationBadge
                query={prompts}
                label={t('prompts.truncated', {
                  n: prompts.data?.items?.length ?? 0,
                })}
                hint={t('prompts.truncatedHint')}
                className="px-0 pt-0 pb-3"
              />
              <DataTable
                columns={promptColumns}
                data={prompts.data?.items ?? []}
                isLoading={prompts.isLoading}
                error={prompts.error}
                onRetry={() => prompts.refetch()}
                searchable
                searchPlaceholder={t('prompts.search')}
                getRowId={(r) => r.id}
                onRowClick={(r) => {
                  setSelectedPrompt(r.id)
                  setPromptDetailOpen(true)
                }}
                toolbar={
                  canWritePrompt ? (
                    <Button
                      variant="primary"
                      size="sm"
                      onClick={() => setPromptEditorOpen(true)}
                    >
                      <Plus />
                      {t('prompts.newPrompt')}
                    </Button>
                  ) : undefined
                }
                empty={
                  <EmptyState
                    title={t('empty.prompt.title')}
                    description={t('empty.prompt.description')}
                  />
                }
              />
            </>
          )}
        </TabsContent>

        {/* Memory. */}
        <TabsContent value="memory">
          {!canReadMemory ? (
            <ForbiddenState />
          ) : (
            <MemoryTab
              entries={memory.data?.items ?? []}
              integrityExcluded={memory.data?.integrity_excluded ?? 0}
              isLoading={memory.isLoading}
              error={memory.error}
              onRetry={() => memory.refetch()}
              agent={memoryAgent}
              onAgentChange={setMemoryAgent}
              canWrite={canWriteMemory}
              canAdmin={canAdminMemory}
              onNew={() => setMemoryEditorOpen(true)}
            />
          )}
        </TabsContent>

        {/* Context policies. */}
        <TabsContent value="context">
          {!canReadContext ? (
            <ForbiddenState />
          ) : (
            <>
              <p className="mb-3 text-xs text-muted-foreground">
                {t('context.caption')}
              </p>
              <ListTruncationBadge
                query={contextPolicies}
                label={t('contextPolicies.truncated', {
                  n: contextPolicies.data?.items?.length ?? 0,
                })}
                hint={t('contextPolicies.truncatedHint')}
                className="px-0 pt-0 pb-3"
              />
              <DataTable
                columns={contextColumns}
                data={contextPolicies.data?.items ?? []}
                isLoading={contextPolicies.isLoading}
                error={contextPolicies.error}
                onRetry={() => contextPolicies.refetch()}
                searchable
                searchPlaceholder={t('context.search')}
                getRowId={(r) => r.id}
                onRowClick={
                  canWriteContext
                    ? (r) => {
                        setEditingPolicy(r)
                        setContextEditorOpen(true)
                      }
                    : undefined
                }
                toolbar={
                  canWriteContext ? (
                    <Button
                      variant="primary"
                      size="sm"
                      onClick={() => {
                        setEditingPolicy(null)
                        setContextEditorOpen(true)
                      }}
                    >
                      <Plus />
                      {t('context.newPolicy')}
                    </Button>
                  ) : undefined
                }
                empty={
                  <EmptyState
                    title={t('empty.context.title')}
                    description={t('empty.context.description')}
                  />
                }
              />
            </>
          )}
        </TabsContent>

        {/* Data products. */}
        <TabsContent value="data-products">
          <DataProductList canRead={canReadDP} canWrite={canWriteDP} />
        </TabsContent>
        <TabsContent value="dlp">
          <DlpPanel
            canRead={can('knowledge:dlp:read')}
            canAdmin={can('knowledge:dlp:admin')}
          />
        </TabsContent>

        <TabsContent value="scans">
          <ScansPanel
            canRead={can('knowledge:scan:read')}
            canWrite={can('knowledge:scan:write')}
          />
        </TabsContent>

        <TabsContent value="integrity">
          <MemoryIntegrityPanel canAdmin={can('knowledge:memory:admin')} />
        </TabsContent>
      </Tabs>

      {/* KB detail + create. */}
      <KbDetailSheet
        kbId={selectedKb}
        open={kbDetailOpen}
        onOpenChange={setKbDetailOpen}
      />
      <KbEditorDialog open={kbEditorOpen} onOpenChange={setKbEditorOpen} />

      {/* Lineage detail. */}
      <LineageDetailSheet
        lineageId={selectedLineage}
        open={lineageDetailOpen}
        onOpenChange={setLineageDetailOpen}
      />

      {/* Prompt detail + create. */}
      <PromptDetailSheet
        promptId={selectedPrompt}
        open={promptDetailOpen}
        onOpenChange={setPromptDetailOpen}
      />
      <PromptEditorDialog
        open={promptEditorOpen}
        onOpenChange={setPromptEditorOpen}
      />

      {/* Memory write. */}
      <MemoryEditorDialog
        open={memoryEditorOpen}
        onOpenChange={setMemoryEditorOpen}
        agentRef={memoryAgent.trim() || undefined}
      />

      {/* Context upsert. */}
      <ContextEditorDialog
        open={contextEditorOpen}
        onOpenChange={setContextEditorOpen}
        policy={editingPolicy}
      />
    </div>
  )
}

/** The memory tab body: agent filter, purge (admin) + write (write), entry cards. */
// ⛔ `integrityExcluded` NO ES DECORACIÓN: en `GET /memory` las entradas que fallan la
//    comprobación de integridad **se RETIRAN de `items`** y sólo se cuentan aquí — lo dice el tipo
//    de esta misma consola (`types.ts:351-356`) y el motor (`memory.go:94-98`: «the count of
//    entries WITHHELD by the read-path integrity check»).
//
//    Sin enseñarlo, la lista parece completa y no lo es, y **lo que falta es justo lo que más
//    importa**: las entradas cuyo contenido no cuadra con su ancla, es decir las candidatas a
//    haber sido manipuladas. Quien audita la memoria de un agente se lleva la cuenta equivocada
//    y ni siquiera sabe que hay algo que mirar.
function MemoryTab({
  entries,
  integrityExcluded,
  isLoading,
  error,
  onRetry,
  agent,
  onAgentChange,
  canWrite,
  canAdmin,
  onNew,
}: {
  entries: MemoryDTO[]
  integrityExcluded: number
  isLoading: boolean
  error: unknown
  onRetry: () => void
  agent: string
  onAgentChange: (v: string) => void
  canWrite: boolean
  canAdmin: boolean
  onNew: () => void
}) {
  const { t } = useTranslation('knowledge')
  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">{t('memory.caption')}</p>
      <div className="flex flex-wrap items-center gap-2">
        <div className="max-w-xs flex-1">
          <Input
            value={agent}
            onChange={(e) => onAgentChange(e.target.value)}
            placeholder={t('memory.agentFilterPlaceholder')}
            aria-label={t('memory.agentFilter')}
            mono
          />
        </div>
        {canWrite && (
          <Button variant="primary" size="sm" onClick={onNew}>
            <Plus />
            {t('memory.newEntry')}
          </Button>
        )}
        {canAdmin && <MemoryPurgeButton agent={agent.trim() || undefined} />}
      </div>
      {/* Lo que NO está en la lista, dicho antes de la lista. */}
      {integrityExcluded > 0 ? (
        <IntelNotice tone="warning">
          {t('memory.withheld', { n: integrityExcluded })}
        </IntelNotice>
      ) : null}
      <MemoryList
        entries={entries}
        isLoading={isLoading}
        error={error}
        onRetry={onRetry}
        canWrite={canWrite}
      />
      <PortabilityPanel agent={agent.trim() || undefined} canWrite={canWrite} />
    </div>
  )
}

// --- portabilidad de la memoria (C07-04) -------------------------------------
//
// ⛔ ES EL ART. 20 —llevarse los datos— Y SÓLO EXISTÍA EN `curl`. Dos cosas que un botón de
//    «exportar / importar» a secas convierte en mentiras:
//
//    1. **El paquete NO es «toda tu memoria».** El manifiesto trae `count` **y
//       `integrity_excluded`**: el motor DEJA FUERA las filas que fallan la comprobación de
//       integridad y las cuenta. Entregar el paquete a un interesado sin decir cuántas filas
//       faltan lo presenta como completo sin serlo — y la cifra está ahí, en la línea 1.
//
//    2. **La importación puede tener ÉXITO PARCIAL.** `handleImportMemory` verifica la firma y
//       el digest ANTES de escribir nada (una firma mala rechaza el paquete ENTERO), pero luego
//       «a row that fails validation is REJECTED individually and reported». Un «importado ✔»
//       esconde las filas que no entraron, que es justo lo que hay que arreglar a mano después.
function PortabilityPanel({
  agent,
  canWrite,
}: {
  agent?: string
  canWrite: boolean
}) {
  const { t } = useTranslation(['knowledge', 'common'])
  const [manifiesto, setManifiesto] = useState<MemoryExportManifest | null>(
    null,
  )
  const [sinManifiesto, setSinManifiesto] = useState(false)
  const [pegado, setPegado] = useState('')
  const [resultado, setResultado] = useState<{
    imported?: number
    rejected?: Array<{ index: number; key?: string; reason: string }>
    integrity_verified?: boolean
  } | null>(null)

  const exportar = useMutation({
    mutationFn: () =>
      fetchMemoryExport(agent ? { agent_ref: agent } : undefined),
    onSuccess: (r) => {
      setManifiesto(r.manifest)
      setSinManifiesto(r.manifest === null)
      // El cuerpo es el artefacto portable: manifiesto + una línea JSON por fila.
      // Conservarlo byte por byte permite copiarlo y volverlo a importar.
      setPegado(r.raw)
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  const importar = useMutation({
    mutationFn: () => knowledgeApi.importMemory(pegado),
    onSuccess: (r) => setResultado(r as never),
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  const rechazadas = resultado?.rejected ?? []

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">{t('portability.title')}</span>
        <Button
          size="sm"
          variant="outline"
          disabled={exportar.isPending}
          onClick={() => exportar.mutate()}
        >
          {t('portability.export')}
        </Button>
      </div>

      {manifiesto ? (
        <div className="flex flex-col gap-1 text-xs">
          <span>{t('portability.exported', { n: manifiesto.count ?? 0 })}</span>
          {/* ⛔ LO QUE NO VIAJA, dicho igual de fuerte que lo que viaja. */}
          {(manifiesto.integrity_excluded ?? 0) > 0 ? (
            <IntelNotice tone="warning">
              {t('portability.excluded', {
                n: manifiesto.integrity_excluded ?? 0,
              })}
            </IntelNotice>
          ) : (
            <span className="text-muted-foreground">
              {t('portability.nothingExcluded')}
            </span>
          )}
        </div>
      ) : null}
      {/* Un manifiesto ilegible NO es un paquete sin manifiesto: se dice, porque sin él no se
          puede afirmar nada sobre la completitud de lo exportado. */}
      {sinManifiesto ? (
        <IntelNotice tone="warning">{t('portability.noManifest')}</IntelNotice>
      ) : null}

      {canWrite ? (
        <div className="flex flex-col gap-2">
          <Textarea
            aria-label={t('portability.bundle')}
            value={pegado}
            onChange={(e) => setPegado(e.target.value)}
            placeholder={t('portability.bundlePlaceholder')}
            rows={6}
            mono
          />
          <Button
            size="sm"
            variant="outline"
            className="self-start"
            disabled={importar.isPending || pegado.trim() === ''}
            onClick={() => importar.mutate()}
          >
            {t('portability.import')}
          </Button>
        </div>
      ) : null}

      {resultado ? (
        <div className="flex flex-col gap-1 text-xs">
          <span>
            {t('portability.imported', { n: resultado.imported ?? 0 })}
          </span>
          {/* ⛔ EL ÉXITO PARCIAL SE DICE, y con el motivo de cada fila: es lo único que permite
              arreglarlas. Un «importado ✔» a secas las esconde. */}
          {rechazadas.length > 0 ? (
            <>
              <IntelNotice tone="warning">
                {t('portability.rejectedCount', { n: rechazadas.length })}
              </IntelNotice>
              <ul className="flex flex-col gap-0.5">
                {rechazadas.map((r) => (
                  <li key={`${r.index}:${r.key ?? ''}`} className="font-mono">
                    #{r.index} {r.key ? `· ${r.key}` : ''} — {r.reason}
                  </li>
                ))}
              </ul>
            </>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}
