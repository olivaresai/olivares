// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Check, Copy } from 'lucide-react'
import { useEffect, useRef, useState, type HTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

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
}

type Copied = 'idle' | 'copied' | 'failed'

export function CodeLine({
  command,
  inline = false,
  className,
  ...props
}: CodeLineProps) {
  const { t } = useTranslation('common')
  const [copied, setCopied] = useState<Copied>('idle')
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current)
    },
    [],
  )
  if (command.trim() === '') {
    throw new Error(
      'CodeLine needs a command: a missing command is a CLI parity gap to record.',
    )
  }

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      setCopied('copied')
    } catch {
      setCopied('failed')
    }
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => setCopied('idle'), 4000)
  }

  const button = (
    <button
      type="button"
      onClick={() => void copy()}
      aria-label={t('ui.codeLine.copy')}
      className={cn(
        'inline-flex size-7 shrink-0 items-center justify-center rounded-ctl text-text-3',
        'transition-colors duration-100 ease-out hover:bg-hover hover:text-text',
        'outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
      )}
    >
      {copied === 'copied' ? (
        <Check className="size-3.5 text-ok" aria-hidden="true" />
      ) : (
        <Copy className="size-3.5" aria-hidden="true" />
      )}
    </button>
  )
  // One polite region. A failed copy stays visible under the block form, so the operator
  // knows to select the command by hand.
  const status = (
    <span
      role="status"
      className={
        copied === 'failed' && !inline ? 'text-caption text-bad' : 'sr-only'
      }
    >
      {copied === 'copied'
        ? t('ui.codeLine.copied')
        : copied === 'failed'
          ? t('ui.codeLine.copyFailed')
          : ''}
    </span>
  )

  if (inline) {
    return (
      <div
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
      </div>
    )
  }

  return (
    <div
      data-slot="code-line"
      className={cn('flex min-w-0 flex-col gap-1.5', className)}
      {...props}
    >
      <span className="text-overline text-text-3">
        {t('ui.codeLine.label')}
      </span>
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
