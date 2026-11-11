// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Providers: the API keys sessions run with, and the governed references to keys held at
// the provider beside them. One page per concept: the tools and their profiles are AI
// tools. Its route reads under the key tier.
import { Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/ui/page-header'
import { ProviderKeyReferences } from '@/features/models/key-references'
import { useAuth } from '@/lib/auth/context'
import { useModuleOn } from '@/stores/modules'
import { useProviderBoundary } from './auth-boundary'
import { ApiKeys } from './providers-view'

export function ProvidersPage() {
  const boundary = useProviderBoundary()
  return <Providers key={boundary.key} />
}

function Providers() {
  const { t } = useTranslation('providers')
  const { can } = useAuth()
  // The page's verb, for whoever may read and add keys: one without the read would get
  // a button to a list they cannot see.
  const [adding, setAdding] = useState(false)
  const canAdd = can('sessions:provider:read') && can('sessions:provider:write')
  // Their tier is the models module's own: off, they are not offered.
  const keyReferences = useModuleOn('models') && can('models:keys:read')
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <PageHeader
        title={t('title')}
        primaryAction={
          canAdd ? (
            <Button variant="primary" onClick={() => setAdding(true)}>
              <Plus aria-hidden />
              {t('section.add')}
            </Button>
          ) : undefined
        }
      />
      <ApiKeys adding={adding} onAddingChange={setAdding} />
      {keyReferences ? <ProviderKeyReferences /> : null}
    </div>
  )
}
