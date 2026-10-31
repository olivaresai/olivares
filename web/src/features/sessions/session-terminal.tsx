// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A SHELL IN THE BROWSER, inside a registered folder.
//
// The shell runs on the server under the folder's confinement (modules/sessions/terminal.go);
// this is its screen and keyboard. xterm.js draws it and loads only when a person opens a
// terminal, so the console's first paint carries none of it. Mount it with `key={folder}`:
// another folder is another terminal.
//
// ⛔ OPENING A TAB STARTS NOTHING. A terminal is an act (it starts a process and is written
//    to the audit ledger), so the tab offers a button, and a reload takes back only the
//    shell this tab already had. Reading changes nothing.
import { Terminal as TerminalIcon, X } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { terminalTheme } from './terminal-theme'
import { useTerminalSession } from './use-terminal-session'
import './i18n'

type XTerm = import('@xterm/xterm').Terminal

/** The keys a touch keyboard lacks. */
const TOUCH_KEYS: readonly { id: string; seq: string }[] = [
  { id: 'esc', seq: '\x1b' },
  { id: 'tab', seq: '\t' },
  { id: 'ctrlC', seq: '\x03' },
  { id: 'up', seq: '\x1b[A' },
  { id: 'down', seq: '\x1b[B' },
]

export function SessionTerminal({ workspaceRef }: { workspaceRef: string }) {
  const { t } = useTranslation('sessions')
  const host = useRef<HTMLDivElement>(null)
  const term = useRef<XTerm | null>(null)
  const write = useCallback(
    (bytes: Uint8Array) => term.current?.write(bytes),
    [],
  )
  const session = useTerminalSession(workspaceRef, write)
  const [mode, setMode] = useState<'off' | 'open' | 'resume'>(
    session.canResume ? 'resume' : 'off',
  )
  const [hasSelection, setHasSelection] = useState(false)
  const live = useRef(session)
  useEffect(() => {
    live.current = session
  })
  // The input's name follows the language without restarting the terminal.
  const inputLabel = useRef(t('terminal.input'))
  useEffect(() => {
    inputLabel.current = t('terminal.input')
    term.current?.textarea?.setAttribute('aria-label', inputLabel.current)
  }, [t])

  // A start that was refused, or a shell the server no longer holds, returns to the idle
  // panel, which says why when there is a reason.
  if (
    mode !== 'off' &&
    session.state === 'idle' &&
    (session.problem !== null || mode === 'resume')
  )
    setMode('off')

  useEffect(() => {
    if (mode === 'off' || !host.current) return
    const el = host.current
    let disposed = false
    let dispose = () => {}
    void (async () => {
      const [{ Terminal }, { FitAddon }] = await Promise.all([
        import('@xterm/xterm'),
        import('@xterm/addon-fit'),
        import('@xterm/xterm/css/xterm.css'),
      ])
      if (disposed) return
      const xterm = new Terminal({
        cursorBlink: true,
        fontFamily:
          'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
        fontSize: 13,
        scrollback: 5000,
        theme: terminalTheme(),
      })
      const fit = new FitAddon()
      xterm.loadAddon(fit)
      xterm.open(el)
      fit.fit()
      xterm.textarea?.setAttribute('aria-label', inputLabel.current)
      term.current = xterm
      const subs = [
        xterm.onData((data) => live.current.send(data)),
        xterm.onResize(({ rows, cols }) => live.current.resize(rows, cols)),
        xterm.onSelectionChange(() => setHasSelection(xterm.hasSelection())),
      ]
      const resized = new ResizeObserver(() => fit.fit())
      resized.observe(el)
      const recolor = new MutationObserver(() => {
        xterm.options.theme = terminalTheme()
      })
      recolor.observe(document.documentElement, {
        attributes: true,
        attributeFilter: ['class', 'data-theme'],
      })
      // The sheet that hosts it may be taller than the window: bring the screen into view.
      el.scrollIntoView?.({ block: 'nearest' })
      xterm.focus()
      if (mode === 'resume') {
        if (!live.current.resume()) setMode('off')
      } else void live.current.open(xterm.rows, xterm.cols)
      dispose = () => {
        resized.disconnect()
        recolor.disconnect()
        subs.forEach((s) => s.dispose())
        xterm.dispose()
        term.current = null
      }
    })()
    return () => {
      disposed = true
      dispose()
    }
  }, [mode])

  const copy = () => {
    const text = term.current?.getSelection()
    if (text) void navigator.clipboard.writeText(text)
  }
  const paste = () => {
    void navigator.clipboard
      .readText()
      .then((text) => term.current?.paste(text))
      .catch(() => {})
  }

  if (mode === 'off') {
    return (
      <section
        data-testid="session-terminal"
        className="flex flex-col items-start gap-2"
      >
        <p className="text-caption text-muted-foreground">
          {t('terminal.hint')}
        </p>
        {session.problem ? (
          <p role="alert" className="text-caption text-warning">
            {t(`terminal.problem.${session.problem.kind}`)}
          </p>
        ) : null}
        <Button
          onClick={() => {
            session.dismiss()
            setMode('open')
          }}
        >
          <TerminalIcon aria-hidden />
          {t('terminal.open')}
        </Button>
      </section>
    )
  }

  const ended = session.state === 'ended'
  let status = ''
  if (session.state === 'reconnecting') status = t('terminal.reconnecting')
  else if (ended)
    status =
      session.exitCode === null
        ? t('terminal.ended')
        : t('terminal.endedWith', { code: session.exitCode })
  else if (session.problem)
    status = t(`terminal.problem.${session.problem.kind}`)

  return (
    <section
      data-testid="session-terminal"
      className="flex min-w-0 flex-col gap-2"
    >
      <div className="flex min-w-0 items-center gap-1">
        <p
          role="status"
          className="min-w-0 flex-1 truncate text-caption text-muted-foreground"
        >
          {status}
        </p>
        <Button
          variant="ghost"
          size="sm"
          onClick={copy}
          disabled={!hasSelection}
        >
          {t('terminal.copy')}
        </Button>
        <Button variant="ghost" size="sm" onClick={paste} disabled={ended}>
          {t('terminal.paste')}
        </Button>
        {ended ? (
          <Button size="sm" onClick={() => setMode('off')}>
            {t('terminal.again')}
          </Button>
        ) : null}
        <Button
          variant="ghost"
          size="sm"
          aria-label={t('terminal.closeLabel')}
          onClick={() => {
            void session.close()
            setMode('off')
          }}
        >
          <X aria-hidden />
          {t('terminal.close')}
        </Button>
      </div>
      <div
        ref={host}
        role="group"
        aria-label={t('terminal.label')}
        data-testid="terminal-screen"
        className="h-[min(28rem,60dvh)] min-w-0 overflow-hidden rounded-md border border-border bg-background p-1"
      />
      <div
        data-testid="terminal-keys"
        className="hidden gap-1 [@media(pointer:coarse)]:flex"
      >
        {TOUCH_KEYS.map((key) => (
          <Button
            key={key.id}
            size="sm"
            disabled={ended}
            onClick={() => {
              session.send(key.seq)
              term.current?.focus()
            }}
          >
            {t(`terminal.keys.${key.id}`)}
          </Button>
        ))}
      </div>
    </section>
  )
}
