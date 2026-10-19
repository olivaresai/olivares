// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Switch } from '@/components/ui/switch'
import { useEditionBadge } from './use-edition-badge'

export function EditionBadgeSetting() {
  const { t } = useTranslation('settings')
  const badge = useEditionBadge()
  if (!badge.edition) return null
  return (
    <div className="flex flex-col gap-1 py-2">
      <div className="flex items-center justify-between gap-4">
        <label
          htmlFor="show-edition-badge"
          className="text-body text-foreground"
        >
          {t('edition.badge.show')}
        </label>
        <Switch
          id="show-edition-badge"
          checked={badge.shown}
          disabled={!badge.canSave}
          onCheckedChange={(shown) => {
            void badge.setShown(shown).catch(() => {
              /* The mutation error is rendered below. */
            })
          }}
        />
      </div>
      {badge.loadError ? (
        <p role="status" className="text-caption text-muted-foreground">
          {t('edition.badge.loadError')}
        </p>
      ) : null}
      {badge.saveError ? (
        <p role="alert" className="text-caption text-danger">
          {t('edition.badge.saveError')}
        </p>
      ) : null}
    </div>
  )
}
