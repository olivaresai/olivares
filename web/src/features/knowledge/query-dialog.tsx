// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ScrollText, Search } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { IntelNotice } from '@/features/_intel'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import {
  normalizeSourceMode,
  SourceModeBadge,
  type NormalizedSourceMode,
} from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { ClassificationBadge, EgressBadge, EmbedModelBadge } from './chips'
import { knowledgeApi, knowledgeKeys } from './api'
import './i18n'
import type { KbDTO, QueryInput, QueryResponse } from './types'

export interface QueryDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  kb: KbDTO
}

/**
 * QueryDialog runs a GOVERNED retrieval. It is privileged (every call appends a
 * lineage row) so the deliberate submit + audit notice is the confirmation surface.
 * Results are already permission-filtered server-side; we only render the redacted
 * chunk text + score + classification + provenance + the lineage link. A 403 is a
 * governance/residency denial — we surface its reason calmly (still recorded in
 * lineage), never a generic red error.
 */
export function QueryDialog({ open, onOpenChange, kb }: QueryDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
        {open && <QueryForm kb={kb} onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function QueryForm({ kb, onClose }: { kb: KbDTO; onClose: () => void }) {
  const { t } = useTranslation(['knowledge', 'common', 'shared'])
  const { activeTenant } = useAuth()

  const [query, setQuery] = useState('')
  const [agentRef, setAgentRef] = useState('')
  const [sessionRef, setSessionRef] = useState('')
  const [topK, setTopK] = useState('10')
  const [result, setResult] = useState<QueryResponse | null>(null)
  const [deniedReason, setDeniedReason] = useState<string | null>(null)
  const [modeFilter, setModeFilter] = useState<NormalizedSourceMode | 'all'>(
    'all',
  )

  const valid = query.trim().length > 0
  const visibleResults =
    result?.results.filter(
      (r) =>
        modeFilter === 'all' ||
        normalizeSourceMode(r.source_mode) === modeFilter,
    ) ?? []

  const mutation = usePrivilegedMutation<QueryInput, QueryResponse>({
    mutationFn: (input) => knowledgeApi.query(kb.id, input),
    // A governed read writes a lineage row — refresh the lineage list.
    invalidateKeys: () => [knowledgeKeys.lineage(activeTenant)],
    successMessage: t('query.done'),
    onDone: (data) => {
      setResult(data)
      setDeniedReason(null)
    },
  })

  function run() {
    if (!valid || mutation.isPending) return
    setResult(null)
    setDeniedReason(null)
    const parsedTopK = Number.parseInt(topK, 10)
    const input: QueryInput = {
      query: query.trim(),
      ...(Number.isFinite(parsedTopK) && parsedTopK > 0
        ? { top_k: parsedTopK }
        : {}),
      ...(agentRef.trim() ? { agent_ref: agentRef.trim() } : {}),
      ...(sessionRef.trim() ? { session_ref: sessionRef.trim() } : {}),
    }
    mutation.mutate(input, {
      onError: (err) => {
        // ⛔ LA CEREMONIA NO ES UNA DENEGACIÓN, y aquí se veían las dos a la vez.
        // `usePrivilegedMutation` ya abre el step-up (use-privileged-mutation.ts:37-47),
        // pero este `onError` es el de la LLAMADA de react-query y corre igualmente: como
        // `isForbidden` es SÓLO el status 403 (lib/api/errors.ts:59), un `step_up_required`
        // lo satisfacía y el diálogo pintaba «denegado por …» junto al panel que ofrece la
        // salida. Decirle al operador que le han denegado algo que sólo tiene que confirmar
        // es peor que no decir nada: le hace abandonar una acción que sí puede completar.
        if (err instanceof ApiError && err.isStepUpRequired) return
        // 403 = governance/residency denial — show the reason, not a red error.
        if (err instanceof ApiError && err.isForbidden) {
          setDeniedReason(err.message)
        }
      },
    })
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('query.title')}</DialogTitle>
        <DialogDescription>
          {t('query.body', { name: kb.name })}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('query.queryLabel')}
          htmlFor="q-query"
          description={t('query.queryHint')}
          required
        >
          <Textarea
            id="q-query"
            value={query}
            rows={2}
            onChange={(e) => setQuery(e.target.value)}
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label={t('common.agentRef')} htmlFor="q-agent">
            <Input
              id="q-agent"
              value={agentRef}
              onChange={(e) => setAgentRef(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('query.sessionRef')} htmlFor="q-session">
            <Input
              id="q-session"
              value={sessionRef}
              onChange={(e) => setSessionRef(e.target.value)}
              mono
            />
          </Field>
          <Field label={t('query.topK')} htmlFor="q-topk">
            <Input
              id="q-topk"
              type="number"
              min={1}
              max={100}
              value={topK}
              onChange={(e) => setTopK(e.target.value)}
              mono
            />
          </Field>
        </div>

        {deniedReason && (
          <div
            role="alert"
            className="flex flex-col gap-1 rounded-md border border-warning-line bg-warning-soft px-3 py-2"
          >
            <span className="text-sm font-medium text-warning">
              {t('query.deniedTitle')}
            </span>
            <span className="text-xs text-muted-foreground">
              {deniedReason}
            </span>
          </div>
        )}

        {result && (
          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-medium text-foreground">
                  {t('query.resultsTitle')}
                </span>
                <EgressBadge value={result.egress} />
                <EmbedModelBadge value={result.embed_model} />
                <Badge variant="outline">
                  {t('query.lineageLink', { id: result.lineage_id })}
                </Badge>
              </div>
              <div className="flex items-center gap-2">
                <span className="text-xs text-muted-foreground">
                  {t('query.modeFilter')}
                </span>
                <Select
                  value={modeFilter}
                  onValueChange={(v) =>
                    setModeFilter(v as NormalizedSourceMode | 'all')
                  }
                >
                  <SelectTrigger
                    className="h-8 w-32"
                    aria-label={t('query.modeFilter')}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">{t('query.modeAll')}</SelectItem>
                    <SelectItem value="export">
                      {t('shared:sourceModes.export')}
                    </SelectItem>
                    <SelectItem value="live">
                      {t('shared:sourceModes.live')}
                    </SelectItem>
                    <SelectItem value="direct">
                      {t('shared:sourceModes.direct')}
                    </SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <p className="text-xs text-muted-foreground">
              {t('query.resultsCaption')}
            </p>

            {/*
              ⛔ LO QUE NO ESTÁ EN ESTA LISTA, dicho antes de leerla. `queryResponse`
              (`modules/knowledge/retrieval.go:60-77`) reporta tres cosas que este diálogo no
              pintaba, y sin ellas la respuesta parece construida sobre todo lo recuperado:

                · `context_truncated` + `context_dropped_chunks` — el contexto NO cabía. La
                  respuesta se produjo sobre menos evidencia de la que la lista sugiere.

                · `excluded_chunks` / `excluded_sources` y `redacted_items` — el EFECTO de los dos
                  suelos de contexto. El motor los añadió con estas palabras (`:72-74`):
                  «Reporting the flag without reporting its effect is how a control that applies
                  nothing looks identical to one that applies something and finds nothing».
                  Por eso se separan los tres estados: no se aplicó · se aplicó y quitó N · se
                  aplicó y no quitó nada. El tercero es información, no silencio.
            */}
            {result.context_truncated ? (
              <IntelNotice tone="warning">
                {t('query.contextTruncated', {
                  n: result.context_dropped_chunks ?? 0,
                })}
              </IntelNotice>
            ) : null}

            <div className="flex flex-wrap items-center gap-2 text-xs">
              {(result.excluded_chunks ?? 0) > 0 ? (
                <Badge variant="warning">
                  {t('query.excludedChunks', {
                    n: result.excluded_chunks ?? 0,
                    sources: (result.excluded_sources ?? []).join(', '),
                  })}
                </Badge>
              ) : (result.excluded_sources ?? []).length > 0 ? (
                // El suelo actuó sobre fuentes pero no quitó trozos: es el tercer estado.
                <Badge variant="neutral">
                  {t('query.sourceFloorNoEffect')}
                </Badge>
              ) : null}

              {/* La redacción: exigida y con efecto · exigida y sin efecto · no exigida. */}
              {result.redaction_required ? (
                (result.redacted_items ?? 0) > 0 ? (
                  <Badge variant="warning">
                    {t('query.redactedItems', {
                      n: result.redacted_items ?? 0,
                    })}
                  </Badge>
                ) : (
                  <Badge variant="neutral">
                    {t('query.redactionNoEffect')}
                  </Badge>
                )
              ) : null}
            </div>

            {result.results.length === 0 ? (
              <p className="rounded-md border border-dashed border-border px-3 py-3 text-sm text-muted-foreground">
                {t('query.noResults')}
              </p>
            ) : visibleResults.length === 0 ? (
              <p className="rounded-md border border-dashed border-border px-3 py-3 text-sm text-muted-foreground">
                {t('query.noModeResults')}
              </p>
            ) : (
              <ul className="flex flex-col gap-2">
                {visibleResults.map((r) => (
                  <li
                    key={r.chunk_id}
                    className="rounded-md border border-border bg-surface p-3"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="truncate text-sm font-medium text-foreground">
                        {r.title || r.document_id}
                      </span>
                      <span className="font-mono text-xs tabular-nums text-muted-foreground">
                        {t('query.score')} {r.score.toFixed(3)}
                      </span>
                    </div>
                    <div className="mt-1 flex flex-wrap items-center gap-1.5">
                      <ClassificationBadge value={r.classification} />
                      <Badge variant="neutral">{r.source_kind}</Badge>
                      <SourceModeBadge value={r.source_mode} />
                    </div>
                    <p className="mt-2 line-clamp-4 text-xs text-muted-foreground">
                      {r.text}
                    </p>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </div>

      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <ScrollText className="size-3.5 shrink-0" aria-hidden />
        {t('common:privileged.auditedNotice')}
      </p>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.close')}
        </Button>
        <Button
          variant="primary"
          onClick={run}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending ? (
            <Spinner size="sm" aria-hidden />
          ) : (
            <Search aria-hidden />
          )}
          {t('query.run')}
        </Button>
      </DialogFooter>
    </>
  )
}
