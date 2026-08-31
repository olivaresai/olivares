// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Model Operations (module XXIII) — the governed own-model registry, signed-model
// admission and local inference deployments the engine already exposes but the console
// could not operate (the P0 gap). This is a DISTINCT surface from Models
// (catalog/estate/routing/keys): that view answers "what models exist and how do
// requests route"; this one answers "which models do we own, is their supply chain
// admitted, and where are they deployed". Slice 1 shipped the registry + enforcement
// path (owned models + versions, admission policy/admit/verdicts, deployments incl. the
// deny-closed 422). Slice 2 adds lineage & evidence: the Datasets and Fine-tune-jobs
// registries, the cross-model AIBOM seal ledger (Model evidence), and per-model AIBOM /
// model-card generate·export·seal in the Owned-models drawer. GPAI posture and the agent
// supply chain land in slice 3.
import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { BadgeCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { IntelPage } from '@/features/_intel'
import { OwnedModelsTab } from './owned-models'
import { AdmissionTab } from './admission'
import { DeploymentsTab } from './deployments'
import { DatasetsTab } from './datasets'
import { FinetuneTab } from './finetune'
import { ModelEvidenceTab } from './evidence'
import './i18n'

/** A tab and the READ permission that reveals it. Actions inside each tab gate on the
 *  narrower write/admin permission. */
interface TabSpec {
  value: string
  read: string
  Panel: () => React.ReactNode
}

const TABS: readonly TabSpec[] = [
  // Registry & lineage (the model and what went into it), then enforcement (admission,
  // deployments), then the durable evidence ledger. GPAI posture lives in /models (it is
  // per-provider, not per-owned-model) and the agent supply chain is its own view.
  { value: 'owned', read: 'models:registry:read', Panel: OwnedModelsTab },
  { value: 'datasets', read: 'models:registry:read', Panel: DatasetsTab },
  { value: 'finetune', read: 'models:registry:read', Panel: FinetuneTab },
  { value: 'admission', read: 'models:admission:read', Panel: AdmissionTab },
  {
    value: 'deployments',
    read: 'models:registry:read',
    Panel: DeploymentsTab,
  },
  { value: 'evidence', read: 'models:registry:read', Panel: ModelEvidenceTab },
] as const

function initialTab(accessible: readonly TabSpec[]): string {
  const fallback = accessible[0]?.value ?? 'owned'
  if (typeof window === 'undefined') return fallback
  const want = new URLSearchParams(window.location.search).get('tab')
  return want && accessible.some((tb) => tb.value === want) ? want : fallback
}

export function ModelOpsView() {
  const { t } = useTranslation('model-ops')
  const { can } = useAuth()
  const navigate = useNavigate()

  // Nav visibility is the coarse models:read (RBAC is verb-tier: any models reader
  // inherits it). Per-tab we still gate on the specific read so the surface stays
  // correct if the model ever becomes capability-scoped; the default tab is the FIRST
  // one the principal may see, never assumed to be Owned models.
  const accessible = TABS.filter((tb) => can(tb.read))
  const [tab, setTabState] = useState(() => initialTab(accessible))

  function setTab(value: string) {
    setTabState(value)
    // Reflect the tab in the URL so an audit link is shareable. Merge into the
    // existing search rather than replacing it, and REPLACE the history entry:
    // without it every tab click pushed one, so Back walked the tabs instead of
    // leaving the view — the opposite of the contract use-url-state.ts states
    // ("Back leaves the view, it does not undo a filter click").
    void navigate({
      search: (prev: Record<string, unknown>) => ({ ...prev, tab: value }),
      replace: true,
    } as never)
  }

  return (
    <IntelPage icon={BadgeCheck} title={t('title')} description={t('subtitle')}>
      {accessible.length === 0 ? null : (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            {accessible.map((tb) => (
              <TabsTrigger key={tb.value} value={tb.value}>
                {t(`tabs.${tb.value}`)}
              </TabsTrigger>
            ))}
          </TabsList>
          {accessible.map((tb) => (
            <TabsContent
              key={tb.value}
              value={tb.value}
              className="flex flex-col gap-4"
            >
              <tb.Panel />
            </TabsContent>
          ))}
        </Tabs>
      )}
    </IntelPage>
  )
}
