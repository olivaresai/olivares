// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Models/providers (module X) — the container. Tabs over catalog / estate / routing /
// keys / GPAI supplier posture. The routing-policy editor (react-hook-form + zod,
// the foundation form
// pattern) configures the policy that the gateway connector executes; "Resolve"
// previews the decision. The key dialog NEVER accepts a secret — only a reference.
import { useState } from 'react'

import { currentLanguage } from '@/lib/i18n'
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Cpu, Plus, Wand2 } from 'lucide-react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'
import { Badge } from '@/components/ui/badge'
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
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import {
  AsyncSection,
  ListTruncationBadge,
  IntelNotice,
  IntelPage,
  SectionCard,
} from '@/features/_intel'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from './api'
import {
  CapabilityMatrix,
  DecisionPanel,
  KeyRefsTable,
  ModelsTable,
  PricingTable,
} from './components'
import { GpaiTab } from './gpai'
import type { Decision, RoutingPolicy, RoutingStrategy } from './types'
import './i18n'

const STRATEGIES: RoutingStrategy[] = [
  'cost',
  'latency',
  'capability',
  'pinned',
]

// --- residencia por workspace (C07-04) ---------------------------------------
//
// ⛔ POR QUÉ, y es una decisión de SOBERANÍA DE DATOS que estaba sólo en `curl`:
//    `modules/models/api.go:118-119` sirve la residencia por workspace —dónde se procesa la
//    inferencia y dónde vive el dato en reposo— y la consola no la llamaba. Va en la pestaña de
//    claves porque su permiso es el de claves (`permKeysRead`/`permKeysWrite`), no el de catálogo.
//
// ⛔ Y LAS DOS AUSENCIAS QUE NO SIGNIFICAN «NO», dichas por el propio DTO
//    (`modules/models/residency.go:77-84`):
//      · `allowed_geos` VACÍO = «el proveedor no reporta restricción» — **nunca «denegado»**.
//        Pintar «ninguna geografía permitida» diría lo contrario de lo que el motor afirma, sobre
//        la pregunta donde equivocarse es más caro.
//      · `default_geo` vacío = «no reportado», no «sin defecto».
function ResidencyCard() {
  const { t } = useTranslation('models')
  const { activeTenant } = useAuth()

  // ⛔ EL TECHO SE PIDE Y EL RECORTE SE DICE. `handleListWorkspaceResidency` publica `has_more`
  //    y sin `limit` el repositorio genérico pagina a 100: una decisión de SOBERANÍA DE DATOS
  //    —dónde se procesan los datos de cada workspace— se leía completa estando recortada.
  const params = { limit: EVIDENCE_PAGE }
  const q = useQuery({
    queryKey: modelsKeys.workspaceResidency(activeTenant, params),
    queryFn: () => modelsApi.workspaceResidency(params),
  })

  return (
    <SectionCard
      title={t('residency.title')}
      description={t('residency.description')}
    >
      <ListTruncationBadge
        query={q}
        label={t('residency.truncated', {
          n: (q.data as { items?: unknown[] })?.items?.length ?? 0,
        })}
        hint={t('residency.truncatedHint')}
      />
      <AsyncSection query={q} skeletonHeight={140}>
        {(res) => {
          const filas = ((res as { items?: unknown[] })?.items ?? []) as Array<{
            workspace_ref: string
            allowed_geos?: string[]
            default_geo?: string
            workspace_geo?: string
            as_of?: string
          }>
          return filas.length === 0 ? (
            <EmptyState title={t('residency.empty')} />
          ) : (
            <div className="flex flex-col gap-2">
              {filas.map((r) => (
                <div
                  key={r.workspace_ref}
                  className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border p-3"
                >
                  <div className="flex min-w-0 flex-col gap-1">
                    <span className="font-mono text-sm">{r.workspace_ref}</span>
                    <span className="text-xs text-muted-foreground">
                      {/* Vacío NO es «denegado»: es que el proveedor no reporta restricción. */}
                      {(r.allowed_geos ?? []).length === 0
                        ? t('residency.unrestricted')
                        : t('residency.allowed', {
                            geos: (r.allowed_geos ?? []).join(', '),
                          })}
                    </span>
                  </div>
                  <div className="flex shrink-0 flex-wrap items-center gap-2">
                    <Badge variant="outline">
                      {t('residency.defaultGeo', {
                        geo: r.default_geo || t('residency.unreported'),
                      })}
                    </Badge>
                    <Badge variant="neutral">
                      {t('residency.atRest', {
                        geo: r.workspace_geo || t('residency.unreported'),
                      })}
                    </Badge>
                  </div>
                </div>
              ))}
            </div>
          )
        }}
      </AsyncSection>
    </SectionCard>
  )
}

// --- acceso a modelos: quién puede usar qué (C07-04) -------------------------
//
// ⛔ ESTA PANTALLA DECIDE QUIÉN PUEDE USAR QUÉ MODELO, y su semántica es CONTRAINTUITIVA en la
//    dirección peligrosa. `modules/models/modelgovernance.go:412-419`, palabra por palabra:
//
//      «An allow is a positive grant: a subject NAMED by any allow is CONFINED to its allows
//       (deny-closed), a subject named by NONE is UNRESTRICTED.»
//
//    ⇒ **Crear el PRIMER `allow` para alguien le QUITA todo lo demás.** Quien añade «permitir a
//    Alice claude-opus-5» creyendo que concede acceso está, de hecho, **retirándole el resto del
//    catálogo**. Una lista titulada «concesiones» presenta eso como puramente aditivo, y es la
//    lectura que produce un incidente de acceso el lunes siguiente.
//
// ⛔ Y TRES AUSENCIAS QUE SIGNIFICAN SU CONTRARIO, las tres del mismo comentario:
//      · `effect` vacío ⇒ **allow** (no «sin efecto»).
//      · `workspace_ref` vacío ⇒ **todo el tenant** (no «ningún workspace»).
//      · `surfaces` vacío ⇒ **todas las superficies** (no «ninguna»).
//
// ⛔ Un `forbid` **RESTA**: anula cualquier allow de los sujetos que nombra
//    (forbid-overrides-allow, deny-closed). No es una fila más de la lista y no se pinta como tal.
//
// ⛔ El permiso de escritura es `models:model-access:admin`, no el de grupos ni el de rutas: el
//    propio motor lo prueba —«an editor CAN write a routing policy but cannot widen who may use a
//    model»—, así que un editor no debe ver estos botones.
function ModelAccessTab() {
  const { t } = useTranslation('models')
  const { activeTenant, can } = useAuth()

  // ⛔ DOS ARREGLOS EN LA MISMA CONSULTA, y los dos hacen falta:
  //    1 · la clave NOMBRA AL INQUILINO (es también el cambio de la PR #1544, que va aparte:
  //        aquí se escribe la forma FINAL para que ninguna de las dos ramas pierda la otra);
  //    2 · se pide el techo y se declara el recorte. Ésta es la pantalla que decide QUIÉN PUEDE
  //        USAR QUÉ MODELO: un `allow` CONFINA y un `forbid` RESTA, y el conjunto `confinados`
  //        que se calcula abajo sale de las filas CARGADAS.
  //
  //        ⛔ Y LA DIRECCIÓN DEL ERROR NO ES UNA SOLA — lo escribí así y el contraste lo refutó
  //        con el código delante. Una fila `allow` visible NUNCA queda sin marca por faltar otro
  //        allow: es ella misma la que mete al sujeto en el conjunto. Lo que de verdad pasa:
  //          · falta un `forbid` posterior  ⇒ la resta no se ve  ⇒ parece MÁS ancho;
  //          · falta un `allow` posterior del mismo sujeto ⇒ faltan destinos ⇒ parece MÁS ESTRECHO;
  //          · el sujeto no tiene ninguna fila cargada ⇒ no aparece en absoluto.
  //        Y hay un tercer límite que no es del recorte: la vista agrupa por sujeto LITERAL y el
  //        motor resuelve un principal por usuario, rol y grupo a la vez, así que la marca de una
  //        fila nunca es un veredicto de acceso efectivo. El motor sí recorre todas las páginas
  //        antes de decidir: el defecto es de REPRESENTACIÓN, no de enforcement.
  const accesoParams = { limit: EVIDENCE_PAGE }
  const q = useQuery({
    queryKey: modelsKeys.modelAccess(activeTenant, accesoParams),
    queryFn: () => modelsApi.modelAccess({ tenant: activeTenant }, accesoParams),
  })

  const grupos = useQuery({
    queryKey: modelsKeys.modelGroups(activeTenant, accesoParams),
    queryFn: () =>
      modelsApi.modelGroups({ tenant: activeTenant, query: accesoParams }),
  })

  const puedeAdministrar = can('models:model-access:admin')

  return (
    <div className="flex flex-col gap-4">
      {/* ⛔ VA ARRIBA: condiciona cómo se lee TODA la lista de abajo. */}
      <IntelNotice tone="warning">{t('access.confinementNotice')}</IntelNotice>

      <SectionCard
        title={t('access.title')}
        description={t('access.description')}
      >
        <ListTruncationBadge
          query={q}
          label={t('access.truncated', {
            n: (q.data as { items?: unknown[] })?.items?.length ?? 0,
          })}
          hint={t('access.truncatedHint')}
        />
        <AsyncSection query={q} skeletonHeight={180}>
          {(res) => {
            const reglas = ((res as { items?: unknown[] })?.items ??
              []) as Array<{
              id: string
              subject_kind: string
              subject_ref: string
              target_kind: string
              target_ref: string
              workspace_ref?: string
              surfaces?: string[]
              effect?: string
              description?: string
            }>
            if (reglas.length === 0)
              return <EmptyState title={t('access.empty')} />

            // Los sujetos CONFINADOS por al menos un allow: para ellos, lo no listado está negado.
            const confinados = new Set(
              reglas
                .filter((r) => (r.effect ?? 'allow') === 'allow')
                .map((r) => `${r.subject_kind}:${r.subject_ref}`),
            )

            return (
              <div className="flex flex-col gap-2">
                {reglas.map((r) => {
                  // ⛔ El vacío es un ALLOW, no «sin efecto».
                  const efecto = r.effect ?? 'allow'
                  const sujeto = `${r.subject_kind}:${r.subject_ref}`
                  return (
                    <div
                      key={r.id}
                      className="flex flex-col gap-1 rounded-md border border-border p-3 text-sm"
                    >
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge
                          variant={efecto === 'forbid' ? 'danger' : 'success'}
                        >
                          {t(`access.effect.${efecto}`)}
                        </Badge>
                        <span className="font-mono text-xs">{sujeto}</span>
                        <span className="text-muted-foreground">→</span>
                        <span className="font-mono text-xs">
                          {r.target_kind}:{r.target_ref}
                        </span>
                      </div>
                      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                        {/* Vacío = TODO el tenant, no «ningún workspace». */}
                        <span>
                          {r.workspace_ref
                            ? t('access.inWorkspace', { ws: r.workspace_ref })
                            : t('access.tenantWide')}
                        </span>
                        {/* Vacío = TODAS las superficies, no «ninguna». */}
                        <span>
                          {(r.surfaces ?? []).length > 0
                            ? t('access.onSurfaces', {
                                list: (r.surfaces ?? []).join(', '),
                              })
                            : t('access.allSurfaces')}
                        </span>
                      </div>
                      {/* Un forbid no es una fila más: RESTA de los allows del mismo sujeto. */}
                      {efecto === 'forbid' ? (
                        <span className="text-xs text-danger">
                          {t('access.forbidSubtracts')}
                        </span>
                      ) : confinados.has(sujeto) ? (
                        <span className="text-xs text-muted-foreground">
                          {t('access.confinedSubject', { subject: sujeto })}
                        </span>
                      ) : null}
                    </div>
                  )
                })}
              </div>
            )
          }}
        </AsyncSection>
      </SectionCard>

      <SectionCard
        title={t('access.groupsTitle')}
        description={t('access.groupsDescription')}
      >
        <ListTruncationBadge
          query={grupos}
          label={t('access.groupsTruncated', {
            n: (grupos.data as { items?: unknown[] })?.items?.length ?? 0,
          })}
          hint={t('access.groupsTruncatedHint')}
        />
        <AsyncSection query={grupos} skeletonHeight={120}>
          {(res) => {
            const items = ((res as { items?: unknown[] })?.items ??
              []) as Array<{ id: string; name?: string; members?: string[] }>
            return items.length === 0 ? (
              <EmptyState title={t('access.groupsEmpty')} />
            ) : (
              <div className="flex flex-col gap-1">
                {items.map((g) => (
                  <div
                    key={g.id}
                    className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border px-3 py-2 text-sm"
                  >
                    <span className="font-mono text-xs">{g.name}</span>
                    <span className="text-xs text-muted-foreground">
                      {t('access.members', {
                        n: (g.members ?? []).length,
                      })}
                    </span>
                  </div>
                ))}
              </div>
            )
          }}
        </AsyncSection>
      </SectionCard>

      {!puedeAdministrar ? (
        <p className="text-xs text-muted-foreground">
          {t('access.readOnlyNote')}
        </p>
      ) : null}
    </div>
  )
}

export function ModelsView() {
  const { t } = useTranslation('models')
  const { activeTenant, can } = useAuth()

  const catalogQ = useQuery({
    queryKey: modelsKeys.catalog(activeTenant),
    queryFn: () => modelsApi.catalog(),
  })
  const listaParams = { limit: EVIDENCE_PAGE }
  const modelsQ = useQuery({
    queryKey: modelsKeys.models(activeTenant, listaParams),
    queryFn: () => modelsApi.models({ tenant: activeTenant, query: listaParams }),
  })
  const policiesQ = useQuery({
    queryKey: modelsKeys.routingPolicies(activeTenant, listaParams),
    queryFn: () => modelsApi.routingPolicies(listaParams),
  })
  const keysQ = useQuery({
    queryKey: modelsKeys.keys(activeTenant, listaParams),
    queryFn: () => modelsApi.keys(listaParams),
  })

  const canRouting = can('models:routing:write')
  const canKeys = can('models:keys:write')
  const [policyOpen, setPolicyOpen] = useState(false)
  const [keyOpen, setKeyOpen] = useState(false)

  return (
    <IntelPage icon={Cpu} title={t('title')} description={t('description')}>
      <Tabs defaultValue="catalog">
        <TabsList>
          <TabsTrigger value="catalog">{t('tabs.catalog')}</TabsTrigger>
          <TabsTrigger value="estate">{t('tabs.estate')}</TabsTrigger>
          <TabsTrigger value="routing">{t('tabs.routing')}</TabsTrigger>
          <TabsTrigger value="keys">{t('tabs.keys')}</TabsTrigger>
          <TabsTrigger value="access">{t('tabs.access')}</TabsTrigger>
          <TabsTrigger value="gpai">{t('tabs.gpai')}</TabsTrigger>
        </TabsList>

        <TabsContent value="catalog" className="flex flex-col gap-4">
          <AsyncSection query={catalogQ} skeletonHeight={260}>
            {(catalog) => (
              <>
                <CapabilityMatrix catalog={catalog} />
                <PricingTable catalog={catalog} />
              </>
            )}
          </AsyncSection>
        </TabsContent>

        <TabsContent value="estate" className="flex flex-col gap-4">
          <SectionCard
            title={t('estate.title')}
            description={t('estate.description')}
          >
            <ListTruncationBadge
              query={modelsQ}
              label={t('estate.truncated', {
                n: modelsQ.data?.items?.length ?? 0,
              })}
              hint={t('estate.truncatedHint')}
            />
            <AsyncSection query={modelsQ} skeletonHeight={240}>
              {(list) =>
                list.items.length === 0 ? (
                  <EmptyState title={t('estate.empty')} />
                ) : (
                  <ModelsTable models={list.items} />
                )
              }
            </AsyncSection>
          </SectionCard>
        </TabsContent>

        <TabsContent value="routing" className="flex flex-col gap-4">
          <SectionCard
            title={t('routing.title')}
            description={t('routing.description')}
            actions={
              canRouting ? (
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => setPolicyOpen(true)}
                >
                  <Plus />
                  {t('routing.new')}
                </Button>
              ) : null
            }
          >
            <ListTruncationBadge
              query={policiesQ}
              label={t('routing.truncated', {
                n: policiesQ.data?.items?.length ?? 0,
              })}
              hint={t('routing.truncatedHint')}
            />
            <AsyncSection query={policiesQ} skeletonHeight={200}>
              {(list) =>
                list.items.length === 0 ? (
                  <EmptyState
                    title={t('routing.empty')}
                    description={t('routing.emptyHint')}
                  />
                ) : (
                  <div className="flex flex-col gap-3">
                    {list.items.map((p) => (
                      <PolicyCard key={p.id} policy={p} />
                    ))}
                  </div>
                )
              }
            </AsyncSection>
          </SectionCard>
        </TabsContent>

        <TabsContent value="keys" className="flex flex-col gap-4">
          <ResidencyCard />

          <SectionCard
            title={t('keys.title')}
            description={t('keys.description')}
            actions={
              canKeys ? (
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => setKeyOpen(true)}
                >
                  <Plus />
                  {t('keys.new')}
                </Button>
              ) : null
            }
          >
            <IntelNotice tone="info" className="mb-3">
              {t('keys.maskedOnly')}
            </IntelNotice>
            <ListTruncationBadge
              query={keysQ}
              label={t('keys.truncated', {
                n: keysQ.data?.items?.length ?? 0,
              })}
              hint={t('keys.truncatedHint')}
            />
            <AsyncSection query={keysQ} skeletonHeight={180}>
              {(list) =>
                list.items.length === 0 ? (
                  <EmptyState title={t('keys.empty')} />
                ) : (
                  <KeyRefsTable keys={list.items} />
                )
              }
            </AsyncSection>
          </SectionCard>
        </TabsContent>

        <TabsContent value="access" className="flex flex-col gap-4">
          <ModelAccessTab />
        </TabsContent>

        <TabsContent value="gpai" className="flex flex-col gap-4">
          <GpaiTab />
        </TabsContent>
      </Tabs>

      {canRouting ? (
        <PolicyDialog open={policyOpen} onOpenChange={setPolicyOpen} />
      ) : null}
      {canKeys ? <KeyDialog open={keyOpen} onOpenChange={setKeyOpen} /> : null}
    </IntelPage>
  )
}

// --- policy card with inline resolve -----------------------------------------

function PolicyCard({ policy }: { policy: RoutingPolicy }) {
  const { t } = useTranslation('models')
  const [decision, setDecision] = useState<Decision | null>(null)
  const resolve = useMutation({
    mutationFn: () => modelsApi.resolve(policy.id),
    onSuccess: setDecision,
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium text-foreground">
              {policy.name}
            </span>
            <Badge variant="neutral">
              {t(`routing.strategies.${policy.strategy}`, {
                defaultValue: policy.strategy,
              })}
            </Badge>
            <Badge variant={policy.enabled ? 'success' : 'neutral'}>
              {policy.enabled ? t('routing.enabled') : t('routing.disabled')}
            </Badge>
          </div>
          <dl className="mt-2 flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
            {policy.strategy === 'pinned' && policy.pinned_model ? (
              <Meta
                label={t('routing.pinnedModel')}
                value={policy.pinned_model}
                mono
              />
            ) : null}
            {policy.required_capabilities.length > 0 ? (
              <Meta
                label={t('routing.requiredCapabilities')}
                value={policy.required_capabilities.join(', ')}
              />
            ) : null}
            {policy.preferred_providers.length > 0 ? (
              <Meta
                label={t('routing.preferredProviders')}
                value={policy.preferred_providers.join(', ')}
              />
            ) : null}
            {policy.min_context_window > 0 ? (
              <Meta
                label={t('routing.minContext')}
                value={policy.min_context_window.toLocaleString(
                  currentLanguage(),
                )}
                mono
              />
            ) : null}
            {policy.gateway_endpoint ? (
              <Meta
                label={t('routing.gateway')}
                value={policy.gateway_endpoint}
                mono
              />
            ) : null}
          </dl>
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => resolve.mutate()}
          disabled={resolve.isPending}
        >
          <Wand2 />
          {resolve.isPending ? t('routing.resolving') : t('routing.resolve')}
        </Button>
      </div>
      {decision ? <DecisionPanel decision={decision} /> : null}
      <ExecutePanel policy={policy} />
    </div>
  )
}

// --- ejecución gobernada de una política (C07-04) ----------------------------
//
// ⛔ ESTE BOTÓN GASTA DINERO DE VERDAD, y ésa es la razón de que se cablee con cuidado en vez de
//    junto a «Resolve». `modules/models/execute.go:114-120`: `/execute` «resolves a stored routing
//    policy AND EXECUTES the resolved target chain through the governed Executor, emitting the
//    result's CostSample». `/resolve`, el que ya existía, es **selección pura** y no cuesta nada.
//    Presentar los dos como «probar la política» funde una consulta gratis con una llamada de
//    pago al proveedor.
//
// ⛔ Y EL PERMISO NO ES EL DE LA PANTALLA. La vista gatea todo con `models:routing:write`; el
//    motor exige **`models:routing:admin`** para ejecutar y lo justifica en `api.go:33-37`: es
//    ADMIN «matching the actuation convention of the other modules — distinct from the read-tier
//    resolve. **A viewer/editor cannot spend against a provider**». Con el permiso de la pantalla,
//    un editor vería un botón de gasto que el motor le va a negar con un 403.
//
// ⛔ LAS DOS RESPUESTAS QUE NO SON AVERÍAS, y que un manejador de errores genérico convertiría en
//    «algo ha ido mal»:
//
//    - **503 = no hay ejecutor provisionado.** No es una caída: es el estado deny-closed de
//      fábrica — «the control plane can resolve a routing decision but never spends against a
//      provider until an operator provisions an executor» (`models.go:79-82`). Pintarlo como
//      indisponibilidad manda a alguien a investigar una avería que no existe, cuando lo que hay
//      que hacer es aprovisionar.
//    - **402/429 = el presupuesto de FinOps denegó el gasto ANTES de llamar al proveedor**
//      (Denial-of-Wallet). Es un veredicto de gobierno funcionando, no un fallo: decirlo como
//      error empuja a reintentar justo lo que el tope acaba de frenar.
function ExecutePanel({ policy }: { policy: RoutingPolicy }) {
  const { t } = useTranslation('models')
  const { can } = useAuth()
  const [abierto, setAbierto] = useState(false)
  const [entrada, setEntrada] = useState('')
  const [resultado, setResultado] = useState<ExecuteResult | null>(null)
  const [negado, setNegado] = useState<'unwired' | 'budget' | null>(null)

  const ejecutar = useMutation({
    mutationFn: () =>
      modelsApi.executeRoutingPolicy(policy.id, { input: entrada }),
    onSuccess: (r) => {
      setResultado(r as ExecuteResult)
      setNegado(null)
      setAbierto(false)
    },
    onError: (e: unknown) => {
      const status = e instanceof ApiError ? e.status : 0
      // Se clasifica por ESTADO, nunca por el texto del mensaje: el mensaje cambia con el
      // proveedor y con el idioma, el contrato de estados no.
      if (status === 503) setNegado('unwired')
      else if (status === 402 || status === 429) setNegado('budget')
      else {
        setNegado(null)
        toast.error(String((e as Error).message ?? e))
      }
      setAbierto(false)
    },
  })

  // ⛔ El permiso del MOTOR, no el de la pantalla.
  if (!can('models:routing:admin')) return null

  return (
    <div className="flex flex-col gap-2 border-t border-border pt-3">
      <div className="flex items-center justify-between gap-3">
        <span className="text-xs text-muted-foreground">
          {t('routing.executeHint')}
        </span>
        <Button size="sm" variant="outline" onClick={() => setAbierto(true)}>
          {t('routing.execute')}
        </Button>
      </div>

      {negado === 'unwired' ? (
        <div
          role="note"
          className="rounded-md border border-border p-2 text-xs"
        >
          {t('routing.noExecutor')}
        </div>
      ) : null}
      {negado === 'budget' ? (
        <div
          role="note"
          className="rounded-md border border-warning/40 bg-warning/5 p-2 text-xs"
        >
          {t('routing.budgetDenied')}
        </div>
      ) : null}

      {resultado ? (
        <div className="flex flex-col gap-1 rounded-md border border-border p-2 text-xs">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="neutral">{resultado.served?.model ?? '—'}</Badge>
            {/* Servido ≠ primera opción: sin esto, una cadena que cayó al respaldo se lee
                como si la política hubiese elegido eso a la primera. */}
            {resultado.fallback_used ? (
              <Badge variant="warning">{t('routing.fallbackUsed')}</Badge>
            ) : null}
            {resultado.refusal ? (
              <Badge variant="danger">{t('routing.refusal')}</Badge>
            ) : null}
            <span className="text-muted-foreground">
              {t('routing.tokens', {
                in: resultado.input_tokens ?? 0,
                out: resultado.output_tokens ?? 0,
              })}
            </span>
          </div>
          {/* `execute.go:119-120`: la salida se devuelve al llamante y NO se persiste — al ledger
              sólo llega el CostSample redactado. Quien la necesite, que la copie ahora. */}
          <p className="text-muted-foreground">
            {t('routing.outputNotStored')}
          </p>
        </div>
      ) : null}

      <Dialog open={abierto} onOpenChange={setAbierto}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('routing.execute')}</DialogTitle>
            <DialogDescription>{t('routing.executeWarning')}</DialogDescription>
          </DialogHeader>
          <Field label={t('routing.input')}>
            <Input
              value={entrada}
              onChange={(e) => setEntrada(e.target.value)}
            />
          </Field>
          <DialogFooter>
            <Button
              disabled={ejecutar.isPending || entrada.trim() === ''}
              onClick={() => ejecutar.mutate()}
            >
              {t('routing.executeConfirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

type ExecuteResult = {
  served?: { model?: string }
  fallback_used?: boolean
  refusal?: boolean
  input_tokens?: number
  output_tokens?: number
}

function Meta({
  label,
  value,
  mono,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div className="flex flex-col">
      <dt className="text-[11px] tracking-wide uppercase">{label}</dt>
      <dd className={mono ? 'font-mono text-foreground' : 'text-foreground'}>
        {value}
      </dd>
    </div>
  )
}

// --- routing policy editor (react-hook-form + zod) ---------------------------

const policySchema = z.object({
  name: z.string().min(1),
  strategy: z.enum(['cost', 'latency', 'capability', 'pinned']),
  required_capabilities: z.string(),
  preferred_providers: z.string(),
  min_context_window: z.coerce.number().min(0),
  pinned_model: z.string(),
  allow_deprecated: z.boolean(),
  gateway_endpoint: z.string(),
})
type PolicyForm = z.input<typeof policySchema>

function csv(s: string): string[] {
  return s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean)
}

function PolicyDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['models', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const form = useForm<PolicyForm>({
    resolver: zodResolver(policySchema),
    defaultValues: {
      name: '',
      strategy: 'cost',
      required_capabilities: '',
      preferred_providers: '',
      min_context_window: 0,
      pinned_model: '',
      allow_deprecated: false,
      gateway_endpoint: '',
    },
  })
  const strategy = form.watch('strategy')

  const create = useMutation({
    mutationFn: (values: PolicyForm) =>
      modelsApi.createRoutingPolicy({
        name: values.name,
        strategy: values.strategy,
        required_capabilities: csv(values.required_capabilities),
        preferred_providers: csv(values.preferred_providers),
        min_context_window: Number(values.min_context_window),
        pinned_model: values.pinned_model,
        allow_deprecated: values.allow_deprecated,
        gateway_endpoint: values.gateway_endpoint,
      }),
    onSuccess: () => {
      toast.success(t('routing.dialog.created'))
      void qc.invalidateQueries({
        queryKey: modelsKeys.routingPolicies(activeTenant),
      })
      onOpenChange(false)
      form.reset()
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('routing.dialog.title')}</DialogTitle>
          <DialogDescription>{t('routing.description')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={form.handleSubmit((v) => create.mutate(v))}
        >
          <Field
            label={t('routing.dialog.name')}
            required
            error={form.formState.errors.name?.message}
          >
            {({ id }) => <Input id={id} {...form.register('name')} />}
          </Field>
          <Field label={t('routing.strategy')}>
            <Select
              value={form.watch('strategy')}
              onValueChange={(v) =>
                form.setValue('strategy', v as RoutingStrategy)
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {STRATEGIES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {t(`routing.strategies.${s}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {strategy === 'pinned' ? (
            <Field label={t('routing.pinnedModel')}>
              {({ id }) => <Input id={id} {...form.register('pinned_model')} />}
            </Field>
          ) : (
            <>
              <Field
                label={t('routing.requiredCapabilities')}
                description={t('routing.dialog.listHint')}
              >
                {({ id }) => (
                  <Input id={id} {...form.register('required_capabilities')} />
                )}
              </Field>
              <Field
                label={t('routing.preferredProviders')}
                description={t('routing.dialog.listHint')}
              >
                {({ id }) => (
                  <Input id={id} {...form.register('preferred_providers')} />
                )}
              </Field>
              <Field label={t('routing.minContext')}>
                {({ id }) => (
                  <Input
                    id={id}
                    type="number"
                    min="0"
                    {...form.register('min_context_window')}
                  />
                )}
              </Field>
            </>
          )}
          <Field label={t('routing.gateway')}>
            {({ id }) => (
              <Input id={id} {...form.register('gateway_endpoint')} />
            )}
          </Field>
          <label className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
            <span className="text-sm text-foreground">
              {t('routing.allowDeprecated')}
            </span>
            <Switch
              checked={form.watch('allow_deprecated')}
              onCheckedChange={(v) => form.setValue('allow_deprecated', v)}
            />
          </label>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" variant="primary" disabled={create.isPending}>
              {t('routing.dialog.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- key reference dialog (NEVER a secret) -----------------------------------

function KeyDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['models', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [provider, setProvider] = useState('')
  const [kind, setKind] = useState('api_key')
  const [extId, setExtId] = useState('')
  const [owner, setOwner] = useState('')

  const create = useMutation({
    mutationFn: () =>
      modelsApi.createKey({
        ref_kind: kind,
        provider_ref: provider.trim(),
        ext_id: extId.trim(),
        name: name.trim(),
        owner_ref: owner.trim() || undefined,
      }),
    onSuccess: () => {
      toast.success(t('keys.dialog.created'))
      void qc.invalidateQueries({ queryKey: modelsKeys.keys(activeTenant) })
      onOpenChange(false)
      setName('')
      setProvider('')
      setExtId('')
      setOwner('')
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  const valid = name.trim() && provider.trim() && extId.trim()

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('keys.dialog.title')}</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) create.mutate()
          }}
        >
          <IntelNotice tone="info">{t('keys.dialog.noSecretNote')}</IntelNotice>
          <Field label={t('keys.dialog.name')} required>
            {({ id }) => (
              <Input
                id={id}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('keys.dialog.provider')} required>
              {({ id }) => (
                <Input
                  id={id}
                  value={provider}
                  onChange={(e) => setProvider(e.target.value)}
                />
              )}
            </Field>
            <Field label={t('keys.dialog.kind')}>
              <Select value={kind} onValueChange={setKind}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="api_key">
                    {t('keys.kind.api_key')}
                  </SelectItem>
                  <SelectItem value="workspace">
                    {t('keys.kind.workspace')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field
            label={t('keys.dialog.extId')}
            description={t('keys.dialog.extIdHint')}
            required
          >
            {({ id }) => (
              <Input
                id={id}
                value={extId}
                onChange={(e) => setExtId(e.target.value)}
              />
            )}
          </Field>
          <Field label={t('keys.dialog.owner')}>
            {({ id }) => (
              <Input
                id={id}
                value={owner}
                onChange={(e) => setOwner(e.target.value)}
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
              {t('keys.dialog.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
