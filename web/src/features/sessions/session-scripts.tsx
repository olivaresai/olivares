// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FOLDER'S SCRIPTS, in a session's Details: what the project declares (package.json,
// Taskfile, makefile; project-scripts.ts reads declarations only) and what this person
// added, each run with one click in the session's terminal, whose drawer opens to show it.
import { useQuery } from '@tanstack/react-query'
import { Play, Plus, X } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { agentOpsKeys } from '@/features/agentops/api'
import {
  scriptsFit,
  useClientSettings,
  type AddedScript,
} from '@/features/settings/preferences'
import {
  projectScripts,
  topLevelFiles,
  useAddedScripts,
} from './project-scripts'
import { unsafeCommand } from './run-in-terminal'
import { useTerminalGroup } from './session-terminal'
import { useTerminalDrawer } from './terminal-drawer'
import './i18n'

/**
 * `workspaceRef` is the folder a terminal of the session opens in: scripts run there.
 * `projectRef` is the session's project folder: what a person adds is kept under it,
 * so every session of the project has it, one in its own worktree too.
 */
export function SessionScripts({
  runRef,
  workspaceRef,
  projectRef,
}: {
  runRef: string
  workspaceRef: string
  projectRef: string
}) {
  const { t } = useTranslation('sessions')
  const group = useTerminalGroup(workspaceRef)
  const added = useAddedScripts(projectRef)
  const setAdded = useClientSettings((s) => s.setProjectScripts)
  const [adding, setAdding] = useState(false)
  const declared = useQuery({
    queryKey: [
      ...agentOpsKeys.files(group.tenant, workspaceRef, ''),
      'scripts',
    ],
    queryFn: async ({ signal }) =>
      projectScripts(
        workspaceRef,
        group.tenant,
        signal,
        await topLevelFiles(workspaceRef, group.tenant, signal),
      ),
    retry: false,
    refetchOnWindowFocus: false,
  })
  // As the conversation's Run does: the drawer opens on the terminal it types into.
  const run = (command: string) => {
    useTerminalDrawer.getState().setOpen(runRef, true)
    group.run(command)
  }
  const rows = [
    ...(declared.data ?? []).map((s) => ({ ...s, added: false })),
    ...added.map((s) => ({ ...s, source: '', added: true })),
  ]

  return (
    <section data-testid="context-scripts">
      <h3 className="text-caption font-medium text-foreground">
        {t('terminal.scripts')}
      </h3>
      {declared.isError ? (
        <QueryErrorState
          error={declared.error}
          retry={() => void declared.refetch()}
          title={t('terminal.scriptsFailed')}
          className="mt-1"
        />
      ) : null}
      <BoxedList className="mt-1">
        {declared.isPending ? (
          <BoxedListRow aria-busy="true">
            <Skeleton className="h-5 w-full" />
            <span role="status" className="sr-only">
              {t('scripts.loading')}
            </span>
          </BoxedListRow>
        ) : null}
        {declared.isSuccess && rows.length === 0 && !adding ? (
          <BoxedListRow>
            <p className="text-caption text-muted-foreground">
              {t('scripts.empty')}
            </p>
          </BoxedListRow>
        ) : null}
        {rows.map((script) => (
          <BoxedListRow
            key={`${script.added}:${script.source}:${script.name}`}
            data-testid="context-script"
          >
            <div className="min-w-0 flex-1">
              <p className="truncate text-caption text-foreground">
                {script.name}
              </p>
              <p
                className="truncate font-mono text-caption text-muted-foreground"
                title={script.command}
              >
                {script.command}
              </p>
            </div>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => run(script.command)}
              aria-label={t('terminal.runScript', { name: script.name })}
            >
              <Play aria-hidden />
              {t('scripts.run')}
            </Button>
            {script.added ? (
              <Button
                size="icon"
                variant="ghost"
                aria-label={t('scripts.remove', { name: script.name })}
                title={t('scripts.remove', { name: script.name })}
                onClick={() =>
                  setAdded(
                    projectRef,
                    added.filter((s) => s.name !== script.name),
                  )
                }
              >
                <X aria-hidden />
              </Button>
            ) : null}
          </BoxedListRow>
        ))}
        <BoxedListRow>
          {adding ? (
            <AddScript
              taken={rows.map((s) => s.name)}
              fits={(script) => scriptsFit(projectRef, [...added, script])}
              onCancel={() => setAdding(false)}
              onAdd={(script) => {
                setAdded(projectRef, [...added, script])
                setAdding(false)
              }}
            />
          ) : (
            <Button size="sm" variant="ghost" onClick={() => setAdding(true)}>
              <Plus aria-hidden />
              {t('scripts.add')}
            </Button>
          )}
        </BoxedListRow>
      </BoxedList>
    </section>
  )
}

// The person's settings travel as one synced preference the engine caps at 16 KiB: a
// script's fields are bounded, and a script that would pass the cap is refused.
// ponytail: about 25 long scripts fit; a workspace field when teams want shared scripts
// or more of them.
const NAME_MAX = 64
const COMMAND_MAX = 500

function AddScript({
  taken,
  fits,
  onAdd,
  onCancel,
}: {
  taken: string[]
  fits: (script: AddedScript) => boolean
  onAdd: (script: AddedScript) => void
  onCancel: () => void
}) {
  const { t } = useTranslation('sessions')
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')
  const script = { name: name.trim(), command: command.trim() }
  // The terminal refuses such a command when it runs; it is refused here, when it is kept.
  const problem =
    script.name && taken.includes(script.name)
      ? t('scripts.taken')
      : unsafeCommand(command)
        ? t('scripts.unsafe')
        : !fits(script)
          ? t('scripts.full')
          : null
  const ready = !problem && !!script.name && !!script.command
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (ready) onAdd(script)
  }
  return (
    <form onSubmit={submit} className="flex w-full min-w-0 flex-col gap-2">
      <Input
        autoFocus
        aria-label={t('scripts.name')}
        placeholder={t('scripts.name')}
        maxLength={NAME_MAX}
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      <Input
        aria-label={t('scripts.command')}
        placeholder={t('scripts.commandHint')}
        maxLength={COMMAND_MAX}
        className="font-mono"
        value={command}
        onChange={(e) => setCommand(e.target.value)}
      />
      {problem ? (
        <p role="alert" className="text-caption text-warning">
          {problem}
        </p>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button type="button" size="sm" variant="ghost" onClick={onCancel}>
          {t('scripts.cancel')}
        </Button>
        <Button type="submit" size="sm" variant="secondary" disabled={!ready}>
          {t('scripts.save')}
        </Button>
      </div>
    </form>
  )
}
