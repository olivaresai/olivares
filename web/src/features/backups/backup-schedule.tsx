// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Schedule configuration form for automated backups. The engine owns the cron
// scheduler; this form reads/writes the {enabled, cron, retain_days} config and
// adds no logic (ARCHITECTURE.md SS8).
import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { drApi, drKeys } from './api'
import type { DRSchedule } from './types'

/** Best-effort human preview of a 5-field cron expression. Covers the common
 *  presets; anything exotic falls back to showing the raw expression. */
function cronPreview(
  cron: string,
  t: (key: string, options?: Record<string, unknown>) => string,
): string {
  const trimmed = cron.trim()
  if (!trimmed) return t('schedule.cronPreview.empty')

  const PRESETS: Record<string, string> = {
    '0 0 * * *': 'schedule.cronPreview.dailyMidnight',
    '0 2 * * *': 'schedule.cronPreview.daily0200',
    '0 3 * * *': 'schedule.cronPreview.daily0300',
    '0 0 * * 0': 'schedule.cronPreview.weeklySundayMidnight',
    '0 0 * * 1': 'schedule.cronPreview.weeklyMondayMidnight',
    '0 0 1 * *': 'schedule.cronPreview.monthlyFirstMidnight',
  }
  if (PRESETS[trimmed]) return t(PRESETS[trimmed])

  // Try to describe minute/hour fields for simple patterns.
  const parts = trimmed.split(/\s+/)
  if (parts.length !== 5) return trimmed
  const [min, hour] = parts
  if (min !== '*' && hour !== '*' && parts[2] === '*' && parts[3] === '*') {
    if (parts[4] === '*') {
      return t('schedule.cronPreview.dailyAt', {
        time: `${hour.padStart(2, '0')}:${min.padStart(2, '0')}`,
      })
    }
  }

  return trimmed
}

export function BackupSchedule() {
  const { t } = useTranslation('backups')
  const scheduleQ = useQuery({
    queryKey: drKeys.schedule(),
    queryFn: () => drApi.getSchedule(),
  })

  // Local form state, seeded from the query.
  const [enabled, setEnabled] = useState(false)
  const [cron, setCron] = useState('')
  const [retainDays, setRetainDays] = useState(30)
  const [dualControl, setDualControl] = useState(false)
  const [dirty, setDirty] = useState(false)

  // Seed local state when the query resolves (or re-resolves after a mutation).
  useEffect(() => {
    if (scheduleQ.data) {
      setEnabled(scheduleQ.data.enabled)
      setCron(scheduleQ.data.cron)
      setRetainDays(scheduleQ.data.retain_days)
      setDualControl(scheduleQ.data.require_dual_control_restore ?? false)
      setDirty(false)
    }
  }, [scheduleQ.data])

  const saveMutation = usePrivilegedMutation<void, DRSchedule>({
    mutationFn: () =>
      drApi.updateSchedule({
        enabled,
        cron: cron.trim(),
        retain_days: retainDays,
        require_dual_control_restore: dualControl,
      }),
    invalidateKeys: [drKeys.schedule()],
    successMessage: t('schedule.updated'),
    onDone: () => setDirty(false),
  })

  const markDirty = () => {
    if (!dirty) setDirty(true)
  }

  if (scheduleQ.isLoading) {
    return (
      <div className="flex items-center gap-2 p-6 text-muted-foreground">
        <Spinner size="sm" />
        {t('schedule.loading')}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-6 rounded-lg border border-border bg-surface p-6">
      <div>
        <h2 className="text-base font-medium">{t('schedule.title')}</h2>
        <p className="text-xs text-muted-foreground">
          {t('schedule.description')}
        </p>
      </div>

      <div className="flex items-center gap-3">
        <Switch
          id="schedule-enabled"
          checked={enabled}
          onCheckedChange={(v) => {
            setEnabled(v)
            markDirty()
          }}
          aria-label={t('schedule.enableAria')}
        />
        <label
          htmlFor="schedule-enabled"
          className="text-sm font-medium select-none"
        >
          {enabled ? t('schedule.enabled') : t('schedule.disabled')}
        </label>
      </div>

      <Field
        label={t('schedule.cronLabel')}
        htmlFor="schedule-cron"
        description={cronPreview(cron, t)}
      >
        <Input
          id="schedule-cron"
          value={cron}
          onChange={(e) => {
            setCron(e.target.value)
            markDirty()
          }}
          placeholder={t('schedule.cronPlaceholder')}
          mono
          disabled={!enabled}
        />
      </Field>

      <Field
        label={t('schedule.retentionLabel')}
        htmlFor="schedule-retain"
        description={t('schedule.retentionDescription')}
      >
        <Input
          id="schedule-retain"
          type="number"
          min={1}
          max={3650}
          value={retainDays}
          onChange={(e) => {
            setRetainDays(Number(e.target.value) || 0)
            markDirty()
          }}
          disabled={!enabled}
          className="w-32"
        />
      </Field>

      <div className="flex flex-col gap-2 border-t border-border pt-6">
        <div className="flex items-center gap-3">
          <Switch
            id="schedule-dual-control"
            checked={dualControl}
            onCheckedChange={(v) => {
              setDualControl(v)
              markDirty()
            }}
            aria-label={t('schedule.dualControlAria')}
          />
          <label
            htmlFor="schedule-dual-control"
            className="text-sm font-medium select-none"
          >
            {t('schedule.dualControlLabel')}
          </label>
        </div>
        <p className="text-xs text-muted-foreground">
          {t('schedule.dualControlDescription')}
        </p>
        {/* A requested disarm is not immediate, so the toggle snapping back to ON
            after a save would otherwise read as "the save failed". Say what is
            actually happening: the request is recorded, the gate still holds
            until the stated instant, and re-enabling cancels it. */}
        {scheduleQ.data?.dual_control_disarm_effective_at && (
          <p className="text-xs font-medium text-warning" role="status">
            {t('schedule.dualControlDisarmPending', {
              when: new Date(
                scheduleQ.data.dual_control_disarm_effective_at,
              ).toLocaleString(),
            })}{' '}
            {/* WHO asked is the load-bearing half of a two-person control, and the
                engine has been sending it (core/api/dr_schedule.go
                dual_control_disarm_requested_by) while nothing rendered it: the field
                existed in types.ts and in no component. The second administrator was
                being asked to let a removal stand without being told whose it is.

                Rendered ONLY when the field is non-empty, and that is not defensive
                nicety: an empty value means the reference carries NO ACCOUNT
                ATTRIBUTION — a legacy token actor, say — never "nobody requested it".
                Interpolating it anyway would print a blank name where the operator
                reads an identity, which is the failure this line exists to remove. */}
            {scheduleQ.data.dual_control_disarm_requested_by
              ? t('schedule.dualControlDisarmRequestedBy', {
                  who: scheduleQ.data.dual_control_disarm_requested_by,
                })
              : null}
          </p>
        )}
      </div>

      <div>
        <Button
          variant="primary"
          onClick={() => saveMutation.mutate()}
          disabled={saveMutation.isPending || !dirty}
        >
          {saveMutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('schedule.save')}
        </Button>
      </div>
    </div>
  )
}
