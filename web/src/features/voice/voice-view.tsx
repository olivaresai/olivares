// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Voice / realtime (module XVI) — the container. Tabs over governed Sessions and the
// default-DENY Policies. It wires the queries (tenant-scoped keys), the one privileged
// write (PUT policy, RBAC-gated), and composes the pure presentational pieces. It
// computes nothing about a session — Does; this presents. Reads of the session
// plane are privileged and self-audited, so the page carries a SelfAuditNotice; the
// findings (violations / latency degradation / ungoverned opens) live in Security.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AudioLines, Plus, RefreshCw } from 'lucide-react'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  IntelPage,
  SectionCard,
  SelfAuditNotice,
} from '@/features/_intel'
import { voiceApi, voiceKeys } from './api'
import {
  DecisionsTable,
  LatencyByCategory,
  PoliciesTable,
  SessionsTable,
  VoiceStats,
} from './components'
import { VoiceSessionSurface } from './session-surface'
import type { VoicePolicy } from './types'
import './i18n'

// El techo REAL del motor: `maxLimit = 1000` en
// `core/internal/store/sqlstore/generic.go`. Pedir más no trae más — lo recorta ahí—, y
// no pedir nada trae 100 (`defaultLimit`) sin decirlo. Se pide el máximo y se DECLARA el
// recorte cuando el motor contesta `has_more`.
const LEDGER_PAGE = 1000

export function VoiceView() {
  const { t } = useTranslation('voice')
  const { activeTenant, can } = useAuth()
  const qc = useQueryClient()

  const sessionsQ = useQuery({
    queryKey: voiceKeys.sessions(activeTenant),
    queryFn: () => voiceApi.sessions(),
  })
  const policiesQ = useQuery({
    queryKey: voiceKeys.policies(activeTenant),
    queryFn: () => voiceApi.policies(),
  })
  // ⛔ SIN GUARDA DE PERMISO EN CLIENTE, A PROPÓSITO. `GET /v1/m/voice/decisions` exige
  //    `voice:session:read` (`modules/voice/api.go:38`) y ESA es exactamente la
  //    `permission` con la que la vista entera está registrada
  //    (`web/src/features/registry.tsx`, id 'voice'). Quien llega aquí ya la tiene, así
  //    que un `can()` adicional sería una guarda que no puede disparar nunca: se leería
  //    como protección y no protegería de nada. Si algún día la ruta sube de nivel, la
  //    guarda se añade CON su testigo.
  const ledgerQ = useQuery({
    queryKey: voiceKeys.ledger(activeTenant, { limit: LEDGER_PAGE }),
    queryFn: () => voiceApi.allDecisions({ limit: LEDGER_PAGE }),
  })

  // PUT /v1/m/voice/policies requires voice:policy:admin (modules/voice/api.go:35). The
  // two strings this used to ask for — 'voice:policy:write' and 'voice:write' — exist
  // NOWHERE server-side, and check-console-perms named both as undeclared on every run.
  // The old client-side mirror answered the first one from the module VERB tier, so
  // editors were shown a button whose route requires admin and got a 403; the second
  // resolved to nobody and never contributed. Now that can() is set membership, an
  // undeclared string can be in no set, so the pair would have hidden the button from
  // the admins who do hold the grant. Ask for the permission the route requires.
  const canWritePolicy = can('voice:policy:admin')
  const [policyOpen, setPolicyOpen] = useState(false)
  // La sesion elegida manda: sin ella el flujo SSE no se abre y las decisiones no se
  // piden — el motor exige el ref y devuelve 400 sin el.
  const [selectedRef, setSelectedRef] = useState<string | null>(null)
  const [editing, setEditing] = useState<VoicePolicy | null>(null)

  return (
    <IntelPage
      icon={AudioLines}
      title={t('title')}
      description={t('description')}
      notices={<SelfAuditNotice />}
      actions={
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            void qc.invalidateQueries({ queryKey: voiceKeys.all(activeTenant) })
          }}
        >
          <RefreshCw />
          {t('refresh')}
        </Button>
      }
    >
      <Tabs defaultValue="sessions">
        <TabsList>
          <TabsTrigger value="sessions">{t('tabs.sessions')}</TabsTrigger>
          <TabsTrigger value="policies">{t('tabs.policies')}</TabsTrigger>
          <TabsTrigger value="ledger">{t('tabs.ledger')}</TabsTrigger>
        </TabsList>

        <TabsContent value="sessions" className="flex flex-col gap-4">
          <AsyncSection query={sessionsQ} skeletonHeight={96}>
            {(list) => <VoiceStats sessions={list.items} />}
          </AsyncSection>
          <SectionCard
            title={t('sessions.title')}
            description={t('sessions.description')}
            noPadding
          >
            <div className="p-4">
              <CaveatNotice className="mb-3">
                {t('honesty.noContent')} {t('honesty.findingsNote')}
              </CaveatNotice>
              <AsyncSection query={sessionsQ} skeletonHeight={220}>
                {(list) =>
                  list.items.length === 0 ? (
                    <EmptyState
                      title={t('sessions.empty')}
                      description={t('sessions.emptyHint')}
                    />
                  ) : (
                    <SessionsTable
                      sessions={list.items}
                      onRowClick={(sess) => setSelectedRef(sess.session_ref)}
                    />
                  )
                }
              </AsyncSection>
              <div className="mt-4">
                <VoiceSessionSurface sessionRef={selectedRef} />
              </div>
            </div>
          </SectionCard>
          <AsyncSection query={sessionsQ} skeletonHeight={200}>
            {(list) =>
              list.items.length === 0 ? null : (
                <LatencyByCategory sessions={list.items} />
              )
            }
          </AsyncSection>
        </TabsContent>

        <TabsContent value="policies" className="flex flex-col gap-4">
          <SectionCard
            title={t('policies.title')}
            description={t('policies.description')}
            actions={
              canWritePolicy ? (
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => {
                    setEditing(null)
                    setPolicyOpen(true)
                  }}
                >
                  <Plus />
                  {t('policies.new')}
                </Button>
              ) : null
            }
            noPadding
          >
            <div className="p-4">
              <AsyncSection query={policiesQ} skeletonHeight={180}>
                {(list) =>
                  list.items.length === 0 ? (
                    <EmptyState
                      title={t('policies.empty')}
                      description={t('policies.emptyHint')}
                    />
                  ) : (
                    <PoliciesTable
                      policies={list.items}
                      onRowClick={
                        canWritePolicy
                          ? (p) => {
                              setEditing(p)
                              setPolicyOpen(true)
                            }
                          : undefined
                      }
                    />
                  )
                }
              </AsyncSection>
            </div>
          </SectionCard>
        </TabsContent>

        <TabsContent value="ledger" className="flex flex-col gap-4">
          <SectionCard
            title={t('ledger.title')}
            description={t('ledger.description')}
            noPadding
          >
            <div className="p-4">
              <CaveatNotice className="mb-3">{t('ledger.caveat')}</CaveatNotice>
              <AsyncSection query={ledgerQ} skeletonHeight={220}>
                {(list) => (
                  <>
                    <DecisionsTable decisions={list.items} />
                    {/* ⛔ EL RECORTE SE DICE, NO SE DEDUCE DEL NÚMERO DE FILAS. El motor
                        hace UNA sola llamada a `repo.List` y no drena el cursor
                        (`modules/voice/sessions.go:262-286`), así que con más decisiones
                        que el techo la tabla se ve completa y no lo está. Un ledger
                        append-only recortado en silencio afirma que no hubo más
                        denegaciones, que es la afirmación más cara de esta pantalla. */}
                    {list.has_more ? (
                      <CaveatNotice className="mt-3">
                        {t('ledger.truncated', { count: LEDGER_PAGE })}
                      </CaveatNotice>
                    ) : null}
                  </>
                )}
              </AsyncSection>
            </div>
          </SectionCard>
        </TabsContent>
      </Tabs>

      {canWritePolicy ? (
        <PolicyDialog
          open={policyOpen}
          onOpenChange={setPolicyOpen}
          editing={editing}
        />
      ) : null}
    </IntelPage>
  )
}

// --- voice policy editor (the privileged PUT) --------------------------------

function PolicyDialog({
  open,
  onOpenChange,
  editing,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
  editing: VoicePolicy | null
}) {
  const { t } = useTranslation(['voice', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [agent, setAgent] = useState(editing?.agent_ref ?? '*')
  const [model, setModel] = useState(editing?.allowed_model_ref ?? '*')
  const [provider, setProvider] = useState(editing?.allowed_provider_ref ?? '*')
  const [minutes, setMinutes] = useState(
    String(editing?.max_session_minutes ?? 30),
  )
  const [latency, setLatency] = useState(String(editing?.max_latency_ms ?? 300))

  const save = useMutation({
    mutationFn: () =>
      voiceApi.putPolicy({
        agent_ref: agent.trim() || '*',
        allowed_model_ref: model.trim() || '*',
        allowed_provider_ref: provider.trim() || '*',
        max_session_minutes: Math.max(0, Math.round(Number(minutes))),
        max_latency_ms: Math.max(0, Math.round(Number(latency))),
      }),
    onSuccess: () => {
      toast.success(t('policies.dialog.saved'))
      void qc.invalidateQueries({ queryKey: voiceKeys.policies(activeTenant) })
      onOpenChange(false)
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  const valid =
    agent.trim().length > 0 && Number(minutes) > 0 && Number(latency) > 0

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('policies.dialog.title')}</DialogTitle>
          <DialogDescription>
            {t('policies.dialog.description')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) save.mutate()
          }}
        >
          <Field
            label={t('policies.dialog.agent')}
            description={t('policies.dialog.agentHint')}
            required
          >
            {({ id }) => (
              <Input
                id={id}
                value={agent}
                onChange={(e) => setAgent(e.target.value)}
              />
            )}
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field
              label={t('policies.dialog.model')}
              description={t('policies.dialog.modelHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                />
              )}
            </Field>
            <Field
              label={t('policies.dialog.provider')}
              description={t('policies.dialog.providerHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={provider}
                  onChange={(e) => setProvider(e.target.value)}
                />
              )}
            </Field>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t('policies.dialog.maxMinutes')} required>
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="1"
                  value={minutes}
                  onChange={(e) => setMinutes(e.target.value)}
                />
              )}
            </Field>
            <Field label={t('policies.dialog.maxLatency')} required>
              {({ id }) => (
                <Input
                  id={id}
                  type="number"
                  min="1"
                  value={latency}
                  onChange={(e) => setLatency(e.target.value)}
                />
              )}
            </Field>
          </div>
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
              disabled={!valid || save.isPending}
            >
              {t('policies.dialog.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
