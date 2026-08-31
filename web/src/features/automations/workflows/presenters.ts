// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { useTranslation } from 'react-i18next'
import type { WorkflowStep } from './types'

type Translate = ReturnType<typeof useTranslation>['t']

export function stepSummary(step: WorkflowStep, t: Translate): string {
  switch (step.kind) {
    case 'schedule-fire':
      return t('automations-workflows:config.scheduleSummary', {
        value: step.config.schedule_id,
      })
    case 'eventing-emit':
      return step.config.label
    case 'notify-test':
      return t('automations-workflows:config.routeSummary', {
        value: step.config.route_id,
      })
    case 'wait':
      return t('automations-workflows:config.waitSummary', {
        value: step.config.seconds,
      })
    case 'approval-gate':
      return step.config.reason
        ? t('automations-workflows:config.approvalSummary', {
            value: step.config.reason,
          })
        : t('automations-workflows:config.approvalNoReason')
  }
}

export function validationMessage(message: string, t: Translate): string {
  const known = new Set([
    'refInvalid',
    'scheduleIdInvalid',
    'labelRequired',
    'labelTooLong',
    'routeRequired',
    'secondsRange',
    'reasonTooLong',
    'duplicateRef',
    'fanIn',
    'fanOut',
    'selfDependency',
    'unknownDependency',
    'duplicateDependency',
    'cycle',
  ])
  return known.has(message) ? t(`validation.${message}`) : message
}
