// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Switch } from '@/components/ui/switch'
import { useClientSettings } from './preferences'
import { SettingsRow } from './settings-row'

export function NotificationEventSettings() {
  const { t } = useTranslation('settings')
  const events = useClientSettings((s) => s.notificationEvents)
  const save = useClientSettings((s) => s.setNotificationEvents)
  return (
    <>
      {(['needsYou', 'finished', 'failed'] as const).map((event) => {
        const name = t(`notificationEvents.${event}`)
        return (
          <SettingsRow
            key={event}
            name={name}
            description=""
            changed={!events[event]}
            onReset={() => save({ [event]: true })}
            resetLabel={t('newSessions.reset', { name })}
            control={
              <Switch
                aria-label={name}
                checked={events[event]}
                onCheckedChange={(value) => save({ [event]: value })}
              />
            }
          />
        )
      })}
    </>
  )
}
