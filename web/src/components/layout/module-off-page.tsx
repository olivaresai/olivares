// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PAGE OF A VIEW WHOSE MODULE IS OFF. Navigation lists such a view as a calm entry, so
// its page is calm too: the view's icon, its name, one sentence of what it does and one
// action. It is neither an error nor a refusal (that is a permission, and stays hidden).
//
//   · a person who may change modules: "Turn on", the same selection as Settings >
//     Edition & modules (the engine restarts to apply it, after the usual confirmation when
//     sessions run); the view appears here without a reload once the engine is back;
//   · anyone else: whom to ask;
//   · the communication plane is no module a switch turns on: only the sentence.
import { lazy, Suspense } from 'react'
import { useTranslation } from 'react-i18next'
import { viewDescription, viewLabel } from '@/features/navigation/model'
import type { FeatureView } from '@/features/registry'
import { useAuth } from '@/lib/auth/context'
import { moduleOfView } from '@/stores/modules'

// The turn-on action lives with the Modules screen; it loads only when the page offers it.
const TurnOnPage = lazy(() =>
  import('@/features/settings/modules-settings').then((m) => ({
    default: m.TurnOnPage,
  })),
)

export function ModuleOffPage({ view }: { view: FeatureView }) {
  const { t } = useTranslation(['nav', 'errors', 'settings'])
  const { isSuperadmin } = useAuth()
  const module = moduleOfView(view.permission, view.id)
  const Icon = view.icon
  const description =
    viewDescription(t, view.id) || t('errors:moduleNotEnabled.description')
  return (
    <div
      data-slot="module-not-enabled"
      className="flex min-h-[60vh] items-center justify-center"
    >
      <div className="flex max-w-sm flex-col items-center gap-3 text-center">
        {Icon ? (
          <Icon aria-hidden strokeWidth={1.5} className="size-8 text-text-3" />
        ) : null}
        <h1 className="text-title text-text">{viewLabel(t, view.id)}</h1>
        <p className="text-body text-text-2">{description}</p>
        <div className="mt-2 flex flex-col items-center gap-2">
          {module === 'communication' || !module ? (
            <p className="text-body text-text-2">
              {t('settings:modules.notSetUp')}
            </p>
          ) : isSuperadmin ? (
            <Suspense fallback={null}>
              <TurnOnPage module={module} />
            </Suspense>
          ) : (
            <p className="text-body text-text-2">
              {t('settings:modules.askAdmin')}
            </p>
          )}
        </div>
      </div>
    </div>
  )
}
