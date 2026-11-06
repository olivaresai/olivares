// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { StaticTable } from '@/components/data/static-table'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { knowledgeApi, knowledgeKeys } from '@/features/knowledge/api'
import { useAuth } from '@/lib/auth/context'
import { useModuleOn } from '@/stores/modules'
import type {
  ConnectorInfo,
  ConnectorOnboardInput,
  PreviewDocument,
  SourceRosterEntry,
} from './api'

/** The deletion safeguard a new document source starts with (percent). */
export const DEFAULT_MAX_DELETE_PERCENT = '20'
const NO_KB = '__none__'

/** The settings the engine itself reads on a document source. */
export const DOCUMENT_KEYS = new Set(['kb', 'max_delete_percent'])

export function isDocumentKind(info?: ConnectorInfo): boolean {
  return info?.class === 'document'
}

/** Step 4: the first documents the source would ingest, with their ACL. */
export function DocumentPreview({ docs }: { docs: PreviewDocument[] }) {
  const { t } = useTranslation('console')
  if (docs.length === 0) {
    return (
      <p className="text-caption text-muted-foreground">
        {t('connectors.previewEmpty')}
      </p>
    )
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-border">
      <StaticTable aria-label={t('connectors.previewTitle')}>
        <thead>
          <tr>
            <th>{t('connectors.previewDocument')}</th>
            <th>{t('connectors.previewAcl')}</th>
          </tr>
        </thead>
        <tbody>
          {docs.map((d) => (
            <tr key={d.id}>
              <td className="break-all font-mono text-caption">
                {d.title || d.id}
              </td>
              <td className="text-caption">
                {d.note === 'skipped'
                  ? t('connectors.previewSkipped')
                  : d.note === 'unreadable'
                    ? t('connectors.previewUnreadable')
                    : d.acl.length > 0
                      ? d.acl.join(', ')
                      : t('connectors.previewNoAcl')}
              </td>
            </tr>
          ))}
        </tbody>
      </StaticTable>
    </div>
  )
}

/** Step 5: the knowledge base it syncs into and the deletion safeguard. The
 *  schedule itself is the poll interval field. */
export function DocumentSchedule({
  kb,
  onKb,
  maxDelete,
  onMaxDelete,
}: {
  kb: string
  onKb: (v: string) => void
  maxDelete: string
  onMaxDelete: (v: string) => void
}) {
  const { t } = useTranslation('console')
  const { activeTenant } = useAuth()
  // Knowledge bases are the knowledge module's: not read while that module is off.
  const knowledgeOn = useModuleOn('knowledge')
  const kbs = useQuery({
    queryKey: knowledgeKeys.kbs(activeTenant),
    queryFn: () => knowledgeApi.listKbs(),
    enabled: !!activeTenant && knowledgeOn,
  })
  const items = kbs.data?.items ?? []
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field
        label={t('connectors.kb')}
        htmlFor="conn-kb"
        description={t('connectors.kbHint')}
      >
        <Select
          value={kb || NO_KB}
          onValueChange={(v) => onKb(v === NO_KB ? '' : v)}
        >
          <SelectTrigger id="conn-kb">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NO_KB}>{t('connectors.kbNone')}</SelectItem>
            {kb && !items.some((k) => k.id === kb) && (
              <SelectItem value={kb}>{kb}</SelectItem>
            )}
            {items.map((k) => (
              <SelectItem key={k.id} value={k.id}>
                {k.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field
        label={t('connectors.maxDelete')}
        htmlFor="conn-max-delete"
        description={t('connectors.maxDeleteHint')}
      >
        <Input
          id="conn-max-delete"
          type="number"
          min={0}
          max={100}
          value={maxDelete}
          onChange={(e) => onMaxDelete(e.target.value)}
        />
      </Field>
    </div>
  )
}

/** The exported definition: everything but credentials. */
export interface SourceDefinition {
  name: string
  kind: string
  tenant: string
  poll_seconds?: number
  enabled: boolean
  config: Record<string, string>
}

/** A source's definition without its credentials: secret-declared fields and any
 *  value that references a stored secret are left out. */
export function exportDefinition(
  s: SourceRosterEntry,
  info?: ConnectorInfo,
): SourceDefinition {
  const secret = new Set(
    (info?.fields ?? []).filter((f) => f.secret).map((f) => f.key),
  )
  const config: Record<string, string> = {}
  for (const [k, v] of Object.entries(s.config ?? {})) {
    // A knowledge base id only means something on this server.
    if (!secret.has(k) && !v.startsWith('store:') && k !== 'kb') config[k] = v
  }
  return {
    name: s.name,
    kind: s.kind ?? '',
    tenant: s.tenant,
    poll_seconds: s.poll_seconds,
    enabled: s.enabled,
    config,
  }
}

/** Reads an exported definition; null when the text is not one. */
export function parseDefinition(text: string): SourceDefinition | null {
  let v: unknown
  try {
    v = JSON.parse(text)
  } catch {
    return null
  }
  if (typeof v !== 'object' || v === null) return null
  const d = v as Record<string, unknown>
  if (typeof d.name !== 'string' || typeof d.kind !== 'string') return null
  const config: Record<string, string> = {}
  if (typeof d.config === 'object' && d.config !== null) {
    for (const [k, val] of Object.entries(d.config)) {
      if (typeof val === 'string') config[k] = val
    }
  }
  return {
    name: d.name,
    kind: d.kind,
    tenant: typeof d.tenant === 'string' ? d.tenant : '',
    poll_seconds: typeof d.poll_seconds === 'number' ? d.poll_seconds : 0,
    enabled: d.enabled !== false,
    config,
  }
}

/** The save payload for an imported definition: no credential, so the engine
 *  stores it and reports it "credential missing" until one is set here. A
 *  document source belongs to this server's active tenant, and its knowledge
 *  base is chosen here. */
export function importInput(
  d: SourceDefinition,
  document: boolean,
  activeTenant: string | null,
): ConnectorOnboardInput {
  if (!document || !activeTenant) return { ...d, secrets: {} }
  const config = { ...d.config }
  delete config.kb
  return { ...d, tenant: activeTenant, config, secrets: {} }
}

/** Downloads a definition as name.json. */
export function downloadDefinition(d: SourceDefinition) {
  const blob = new Blob([JSON.stringify(d, null, 2) + '\n'], {
    type: 'application/json',
  })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${d.name.replace(/[^A-Za-z0-9._-]/g, '_')}.json`
  a.click()
  URL.revokeObjectURL(url)
}
