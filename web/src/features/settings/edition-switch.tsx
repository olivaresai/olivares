// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A Community build with a live Business or Enterprise license: the one next step to the licensed
// edition, for the way this engine was installed. Where the engine can take the step itself it is
// one button (the engine runs `olivares upgrade --edition business`, restarts on the licensed
// build and keeps its data); where it cannot (a package or a pod) the exact command is shown
// instead. The same words as `olivares license install`, because the engine writes them once.
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { CodeLine } from '@/components/ui/code-line'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Spinner } from '@/components/ui/spinner'
import { UpdateProgress } from './update-progress'
import { useEngineUpdate } from './use-engine-update'

/** Renders nothing unless the engine names an edition this build can switch to. */
export function EditionSwitch() {
  return <EditionSwitchView update={useEngineUpdate()} />
}

/**
 * The switch on state its caller already holds: the Updates page runs one update, so its Install
 * and this Switch share one phase, one refusal and one mutation instead of two of each. That page
 * shows the progress itself (`progress={false}`); alone, the switch shows its own.
 */
export function EditionSwitchView({
  update: { query, apply, phase, refusal, clearRefusal, busy },
  progress = true,
}: {
  update: ReturnType<typeof useEngineUpdate>
  progress?: boolean
}) {
  const { t } = useTranslation('settings')
  const [confirming, setConfirming] = useState(false)
  const data = query.data
  const step = data?.edition_switch
  if (!data || !step) return null
  // Business and Enterprise are names, not words: every language keeps them in Latin script.
  const edition = step.to === 'enterprise' ? 'Enterprise' : 'Business'

  return (
    <section
      className="flex flex-col gap-2"
      aria-label={t('updates.switchTitle', { edition })}
      data-slot="edition-switch"
    >
      {step.console_action ? (
        <div>
          <Button
            variant="primary"
            size="sm"
            disabled={busy}
            onClick={() => setConfirming(true)}
          >
            {apply.isPending ? <Spinner size="sm" aria-hidden /> : null}
            {t('updates.switch', { edition })}
          </Button>
        </div>
      ) : null}
      {step.command ? (
        <CodeLine
          command={step.command}
          label={t(
            step.console_action ? 'updates.orTerminal' : 'updates.terminal',
          )}
        />
      ) : null}
      {/* The engine's own sentence, in the words the CLI prints after the command. */}
      <p lang="en" className="text-caption text-text-2">
        {step.then}
      </p>
      {data.check_error ? (
        <p className="text-caption text-text-2">{data.check_error}</p>
      ) : null}
      {progress ? <UpdateProgress phase={phase} refusal={refusal} /> : null}
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t('updates.confirm.switchTitle', { edition })}
        description={
          data.running_sessions
            ? t('updates.confirm.stop', { count: data.running_sessions })
            : t('updates.confirm.switchNone')
        }
        confirmLabel={t('updates.confirm.switchConfirm')}
        onConfirm={() => {
          setConfirming(false)
          clearRefusal()
          apply.mutate()
        }}
      />
    </section>
  )
}
