// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// Attack paths for one selected subject. Reachability and escalation start at an agent;
// exfiltration starts at a sensitive resource. That distinction is part of the engine contract.
//
// `modules/access-map/module.go:158-161` has served `/attack-paths/{reachability,escalation,
// exfil,summary}` under `accessmap:graph:read` since. Previously the console requested
// only `/graph`, `/neighbors`, and `/drift`: data rather than computed analyses.
//
// Three engine facts govern this panel and must be visible:
//
// `agent_id` is mandatory for reachability/escalation (`attackpath.go:372-405`), while
// `resource_id` is mandatory for exfiltration (`:407-430`). Mixing them produces a 400 on
// every click, so the panel renders only analyses defined for the selected subject.
//
// `attribution` and `min_confidence` describe the weakest link, not the path. The engine uses
// `weakestAttribution`/`weakerConfidence` (`:328-360`): four `firm` hops and one `unknown` hop
// produce `unknown`. Labeling these values beside each path prevents an inference from
// being presented as a fact on a security surface.
//
// `unknown` is the default when edge metadata is missing (`:330`). It means the attribution
// is unknown, so it appears as a warning rather than a neutral state.
//
// The estate summary is deliberately omitted. Measured on 2026-08-20, `out.EscalationPaths`
// and `out.ExfilRoutes` had no assignments in `handleAttackPathSummary`; the calculating
// functions were used only by per-agent handlers. Displaying zero exfiltration routes would
// publish a value nobody computed. Escalated to the `access-map` owner.
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useAuth } from '@/lib/auth/context'
import { accessMapApi, accessMapKeys } from './api'
import type { AttackPath, AttackPathKind } from './types'

/** `unknown` NO es neutro: es el valor por defecto de un borde sin metadato. Va en
 *  `warning`, no en `outline`, para que no se lea como «sin novedad». */
const ATTRIBUTION_VARIANT: Record<string, 'success' | 'warning' | 'info'> = {
  firm: 'success',
  approximate: 'info',
  unknown: 'warning',
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isAttackStep(value: unknown): boolean {
  if (!isRecord(value)) return false
  return (
    typeof value.node_kind === 'string' &&
    typeof value.node_id === 'string' &&
    (value.node_name === undefined || typeof value.node_name === 'string') &&
    (value.mode === undefined || typeof value.mode === 'string') &&
    (value.tool_id === undefined || typeof value.tool_id === 'string')
  )
}

function isAttackPath(value: unknown): value is AttackPath {
  if (!isRecord(value)) return false
  return (
    (value.kind === 'reachability' ||
      value.kind === 'escalation' ||
      value.kind === 'exfil') &&
    Array.isArray(value.steps) &&
    value.steps.length > 0 &&
    value.steps.every(isAttackStep) &&
    typeof value.attribution === 'string' &&
    typeof value.min_confidence === 'string' &&
    (value.max_sensitivity === undefined ||
      typeof value.max_sensitivity === 'string')
  )
}

function attackPathsFrom(
  value: unknown,
  expectedKind: AttackPathKind,
): AttackPath[] | null {
  // Keep the null case explicit: a mutating test proves that a 200 JSON `null`
  // cannot be rounded into an authoritative empty result.
  if (value === null) return null
  if (!isRecord(value)) return null
  if (!Array.isArray(value.paths)) return null
  if (!value.paths.every(isAttackPath)) return null
  // Each handler fixes the kind it computes. Accepting a different valid kind
  // would put (for example) an exfil path under a Reachability heading and turn
  // a contract mismatch into an authoritative-looking analysis.
  if (!value.paths.every((path) => path.kind === expectedKind)) return null
  return value.paths
}

function PathRow({ path }: { path: AttackPath }) {
  const { t } = useTranslation('accessMap')
  const attribution = path.attribution || 'unknown'
  return (
    <li className="border-border border-b py-2 last:border-b-0">
      <ol className="flex flex-wrap items-center gap-1 text-body">
        {path.steps.map((s, i) => (
          <li
            key={`${s.node_kind}-${s.node_id}-${i}`}
            className="flex items-center gap-1"
          >
            {i > 0 ? <span className="text-muted-foreground">→</span> : null}
            <Badge variant="outline">{s.node_kind}</Badge>
            <span className="font-mono text-caption break-all">
              {s.node_name || s.node_id}
            </span>
          </li>
        ))}
      </ol>
      <div className="text-muted-foreground mt-1 flex flex-wrap items-center gap-2 text-caption">
        <span>{t('attackPaths.weakestLink')}</span>
        <Badge variant={ATTRIBUTION_VARIANT[attribution] ?? 'warning'}>
          {t(`attackPaths.attribution.${attribution}`, {
            defaultValue: attribution,
          })}
        </Badge>
        <Badge variant="outline">
          {t(`attackPaths.confidence.${path.min_confidence}`, {
            defaultValue: path.min_confidence,
          })}
        </Badge>
        <span>
          {t('attackPaths.sensitivity')}:{' '}
          {path.max_sensitivity ? (
            <strong>{path.max_sensitivity}</strong>
          ) : (
            <em title={t('attackPaths.sensitivityAbsentHint')}>
              {t('attackPaths.sensitivityAbsent')}
            </em>
          )}
        </span>
      </div>
    </li>
  )
}

function Analysis({
  subjectId,
  kind,
}: {
  subjectId: string
  kind: AttackPathKind
}) {
  const { t } = useTranslation('accessMap')
  const { activeTenant } = useAuth()
  const fetcher =
    kind === 'reachability'
      ? accessMapApi.attackReachability
      : kind === 'escalation'
        ? accessMapApi.attackEscalation
        : accessMapApi.attackExfil
  const query = useQuery({
    queryKey: accessMapKeys.attackPaths(activeTenant, kind, subjectId),
    queryFn: () => fetcher(subjectId),
    // Every execution appends an audit entry. Retries and background refreshes would
    // create reads the operator did not request; closing the panel drops the cache so
    // the next explicit click is a fresh, separately audited read.
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
    refetchOnMount: 'always',
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
  // Runtime shape is a fact, not a TypeScript promise. A 200 response without an
  // array did not say "zero paths"; rendering the empty state would turn a broken
  // contract into a reassuring result.
  const paths = attackPathsFrom(query.data, kind)

  return (
    <section aria-labelledby={`attack-${kind}`}>
      <h4 id={`attack-${kind}`}>{t(`attackPaths.kind.${kind}`)}</h4>
      {query.isPending ? (
        <p className="text-muted-foreground text-body">
          {t('attackPaths.loading')}
        </p>
      ) : query.isError || (query.isSuccess && paths === null) ? (
        <p role="alert" className="text-danger text-body">
          {t('attackPaths.error')}
        </p>
      ) : paths?.length === 0 ? (
        <p className="text-muted-foreground text-body">
          {t('attackPaths.none')}
        </p>
      ) : (
        <ul>
          {(paths ?? []).map((p, i) => (
            <PathRow key={`${p.kind}-${i}`} path={p} />
          ))}
        </ul>
      )}
    </section>
  )
}

type AttackSubject = { id: string; kind: 'agent' | 'resource' }

export function AttackPathsPanel({
  subject,
}: {
  subject: AttackSubject | null
}) {
  const { t } = useTranslation('accessMap')
  // ⛔ NO SE CONSULTA AL SELECCIONAR, Y ESO NO ES PEREZA: cada análisis SELLA UNA
  //    FILA DE AUDITORÍA en el motor —`sc.Audit().Append` dentro de la propia función,
  //    no del handler (`attackpath.go:151,248`)—. Colgarlos del clic en un nodo escribiría
  //    DOS entradas para un agente o UNA para un recurso cada vez que se mira, y el libro
  //    dejaría de distinguir «un operador investigó» de «alguien pasó el ratón por el
  //    grafo». Una lectura privilegiada y auto-auditada se PIDE.
  const [asked, setAsked] = useState(false)
  if (!subject) {
    // Without a subject there is no valid query. The engine's 400 must never be
    // rendered as an empty analysis.
    return (
      <p
        className="text-muted-foreground text-body"
        data-testid="attack-paths-idle"
      >
        {t('attackPaths.chooseAgent')}
      </p>
    )
  }
  return (
    <div className="flex flex-col gap-3">
      <p className="text-muted-foreground text-caption">
        {t('attackPaths.auditedNote')}
      </p>
      {asked ? (
        <>
          {subject.kind === 'agent' ? (
            <>
              <Analysis subjectId={subject.id} kind="reachability" />
              <Analysis subjectId={subject.id} kind="escalation" />
            </>
          ) : (
            <Analysis subjectId={subject.id} kind="exfil" />
          )}
        </>
      ) : (
        <Button
          variant="secondary"
          size="sm"
          className="w-fit"
          onClick={() => setAsked(true)}
        >
          {t('attackPaths.analyse')}
        </Button>
      )}
    </div>
  )
}
