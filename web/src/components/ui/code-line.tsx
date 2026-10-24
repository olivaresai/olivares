// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { type HTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { CopyIcon, CopyStatus } from './copy-button'
import { useCopy } from './use-copy'

/**
 * CodeLine — CLI parity: the terminal command that does the same thing as the guided action
 * beside it ("Same thing in a terminal"). The command is the text of a `code` element; the
 * prompt sign sits outside it, so Copy writes exactly the command. The copy result is
 * announced in a polite status region.
 *
 * ⛔ NEVER A TOKEN OR A PASSWORD. A command shown in the console uses names and references.
 * ⛔ AN EMPTY COMMAND THROWS. A guided action whose command the CLI does not have yet is a
 *    recorded parity gap; a blank line would pretend otherwise.
 *
 * `inline` is the chip form for a sentence ("…or from a terminal with <command>").
 */
export interface CodeLineProps extends Omit<
  HTMLAttributes<HTMLDivElement>,
  'children'
> {
  command: string
  /** The chip form for a sentence: no heading, no prompt sign. */
  inline?: boolean
  /** The block's heading; `null` when the block sits under a heading of its own. */
  label?: string | null
}

export function CodeLine({
  command,
  inline = false,
  label,
  className,
  ...props
}: CodeLineProps) {
  const { t } = useTranslation('common')
  const { state, copy } = useCopy()
  if (command.trim() === '') {
    throw new Error(
      'CodeLine needs a command: a missing command is a CLI parity gap to record.',
    )
  }

  const button = (
    <CopyIcon
      state={state}
      label={t('ui.codeLine.copy')}
      onCopy={() => void copy(command)}
    />
  )
  // One polite region. A failed copy stays visible under the block form, so the operator
  // knows to select the command by hand.
  const status = (
    <CopyStatus
      state={state}
      copied={t('ui.codeLine.copied')}
      failed={t('ui.codeLine.copyFailed')}
      showFailure={!inline}
    />
  )

  if (inline) {
    return (
      <span
        data-slot="code-line"
        data-inline="true"
        className={cn(
          'inline-flex max-w-full items-center gap-0.5 rounded-[5px] border border-line bg-surface py-px ps-1.5 align-middle',
          className,
        )}
        {...props}
      >
        <code className="min-w-0 font-mono text-mono-s break-all text-text">
          {command}
        </code>
        {button}
        {status}
      </span>
    )
  }

  return (
    <div
      data-slot="code-line"
      className={cn('flex min-w-0 flex-col gap-1.5', className)}
      {...props}
    >
      {label === null ? null : (
        <span className="text-overline text-text-3">
          {label ?? t('ui.codeLine.label')}
        </span>
      )}
      <div className="flex min-w-0 items-start gap-2 rounded-card border border-line bg-frame py-2 ps-3.5 pe-1.5">
        <span
          data-slot="code-line-prompt"
          aria-hidden="true"
          className="font-mono text-mono text-text-3 select-none"
        >
          $
        </span>
        <code className="min-w-0 flex-1 py-px font-mono text-mono [overflow-wrap:anywhere] text-text">
          {command}
        </code>
        {button}
      </div>
      {status}
    </div>
  )
}
