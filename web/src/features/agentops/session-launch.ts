// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SESSION LAUNCH, ONE MODULE for both New session forms. The quick form names a tool, a
// folder and a permission choice; the advanced dialog names the profile and every field
// of the run. Either way this module resolves what the form did not choose (the profile,
// the folder record, the permission template), names the session, launches it under the
// form's authority, as the signed-in user or as an agent, and sends its first message.
import { http } from '@/lib/api'
import type { RequestOptions } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import i18n from '@/lib/i18n'
import {
  sentTurnsPartition,
  useSentTurns,
} from '@/features/sessions/sent-turns'
import { agentOpsApi } from './api'
import { launchRunAsAgent } from './launch-agent-api'
import { stoppedAtStartReason } from './launch-readiness'
import { sessionTurnBody } from './session-turn'
import { TOOL_NAMES, type SessionTool } from './tool-names'
import {
  DEFAULT_DLP_MODE,
  type CreateRunRequest,
  type RunDTO,
  type SecretEnvRef,
  type WorkspaceDTO,
} from './types'

/** The authority a launch was confirmed under: its organization and the step-up
 * attempt (sign-in, credential, organization) with the form's own checks. It is checked
 * before the launch and at every dispatch after it, the first message's retries included. */
export interface LaunchAuthority {
  tenant: string | null
  signal: AbortSignal
  dispatchGuard: () => void
}

/** The permission choices of the quick form, the same for every tool (N1: the engine
 * applies each preset to every driver, and refuses with one sentence a launch a tool cannot
 * honour). "Edit files and run commands" (the default) is the engine's built-in template:
 * an allowlist under dontAsk, so the named tools run without asking and every other tool
 * is refused. The others are plain permission modes. */
export type PermissionChoice =
  'editsAndCommands' | 'editsOnly' | 'readOnly' | 'ask'
export const DEFAULT_PERMISSION: PermissionChoice = 'editsAndCommands'
/** The engine keys its built-in templates by name (modules/sessions/templates.go
 * seedBuiltins), as the CLI does (cmd/olivares/cmd_session.go). */
export const EDITS_AND_COMMANDS_TEMPLATE = 'Edits and commands'
const PERMISSION_MODE: Record<PermissionChoice, string> = {
  editsAndCommands: '',
  editsOnly: 'acceptEdits',
  readOnly: 'plan',
  ask: 'default',
}

/** What the quick form chose: the engine resolves the profile for the tool (its own
 * login, or a key from Providers) unless a profile was chosen, and a folder path is registered when it is new. */
export interface QuickLaunch {
  driver: SessionTool
  /** '' gives the session a new folder of its own (an internal design note (not shipped)).*/
  folder: string
  /** The registration already chosen by the caller. Used as is, without a path lookup
   * or registration; the engine refuses one that is no longer registered or active.
   * When present, folder is only used to name the session. */
  workspace_ref?: string
  permission: PermissionChoice
  /** A model the user chose. Absent means the engine resolves it: the key's saved
   * default, else the tool's own. */
  model?: string
  /** Vault secrets the session receives as environment variables, by name. */
  secretEnv?: SecretEnvRef[]
  /** The profile the person chose when the tool has several: it is the session's, and the
   * engine's rule for the default login is not asked. Absent means that rule. */
  profileRef?: string
}

/** One launch from either form, with the first message ('' sends none). The quick form
 * passes its choices; the advanced dialog passes the run it built and, to launch as an
 * agent, that identity as `actor`. */
export type SessionLaunch = { message: string } & (
  { quick: QuickLaunch } | { run: CreateRunRequest; actor?: string }
)

/** Launches a session and sends its first message under `authority`. A launch whose
 * authority retires keeps its run and sends nothing under the next one. */
export async function launchSession(
  launch: SessionLaunch,
  authority: LaunchAuthority,
): Promise<RunDTO> {
  // The sign-in and organization this start belongs to, before anything is awaited.
  const partition = sentTurnsPartition()
  authority.dispatchGuard()
  const { model, ...run } =
    'quick' in launch ? await quickRun(launch.quick, authority) : launch.run
  const chosenModel = model?.trim()
  const body: CreateRunRequest = {
    ...run,
    // A session is listed by its own name, else by what it was asked first, else (the
    // quick form) by its tool and folder.
    name:
      run.name.trim() ||
      promptName(launch.message) ||
      ('quick' in launch ? toolFolderName(launch.quick) : ''),
    // HU2-37: no model chosen, so none is sent and the engine resolves it.
    ...(chosenModel ? { model: chosenModel } : {}),
  }
  const actor = 'actor' in launch ? (launch.actor ?? '') : ''
  authority.dispatchGuard()
  const created = actor
    ? await launchRunAsAgent(body, actor, authority.tenant, authority)
    : await agentOpsApi.createRun(body, authority)
  // The first message is retried while the child starts, and kept with the session
  // when the engine refuses it.
  if (created?.run_ref && !authority.signal.aborted)
    await sendFirstMessage(created, launch.message, partition, authority)
  return created
}

/** A launch the gate holds for a second person's approval: the engine's 409
 * approval_required, read by its code, never by its sentence. */
export function needsApproval(err: unknown): boolean {
  return err instanceof ApiError && err.code === 'approval_required'
}

/** A session's name from what it was asked first, so the sidebar and Home can list it
 * by name; '' when nothing was asked. */
function promptName(message: string): string {
  const prompt = message.trim().replace(/\s+/g, ' ')
  return prompt.length > 60 ? `${prompt.slice(0, 59)}…` : prompt
}

/** The quick form's run: the resolved profile, the folder record and the permission. */
async function quickRun(
  quick: QuickLaunch,
  authority: LaunchAuthority,
): Promise<CreateRunRequest> {
  const profileRef =
    quick.profileRef ??
    (await agentOpsApi.resolveProfile(quick.driver, authority)).profile
      .profile_ref
  const folder = quick.folder.trim()
  const workspaceRef =
    quick.workspace_ref ??
    (folder ? (await ensureFolder(folder, authority)).workspace_ref : '')
  const template_id = await templateIdFor(quick.permission, authority)
  return {
    name: '',
    transport: 'stream-json',
    permission_mode: PERMISSION_MODE[quick.permission],
    effort: '',
    ...(quick.model ? { model: quick.model } : {}),
    workspace_ref: workspaceRef,
    isolation: 'native',
    env_allow: [],
    provider_profile_ref: profileRef,
    ...(template_id ? { template_id } : {}),
    ...(quick.secretEnv?.length ? { secret_env: quick.secretEnv } : {}),
  }
}

/** A quick session with no first message is named by its tool and folder. */
function toolFolderName(quick: QuickLaunch): string {
  const tool = TOOL_NAMES[quick.driver]
  const folder = quick.folder.trim().split('/').filter(Boolean).pop()
  return folder ? `${tool} in ${folder}` : tool
}

interface TemplateItem {
  id: string
  name: string
  builtin: boolean
}

async function templateIdFor(
  choice: PermissionChoice,
  authority: LaunchAuthority,
): Promise<string | undefined> {
  if (choice !== 'editsAndCommands') return undefined
  const list = await http.get<{ items: TemplateItem[] }>(
    '/v1/m/sessions/templates',
    {
      query: { builtin: 'true', limit: 100 },
      tenant: authority.tenant,
      signal: authority.signal,
    },
  )
  const id = list.items.find(
    (t) => t.builtin && t.name === EDITS_AND_COMMANDS_TEMPLATE,
  )?.id
  if (!id) throw new Error(i18n.t('errors:sessionTemplateUnavailable'))
  return id
}

/** The registered folder for a path, created when it is new. */
async function ensureFolder(
  path: string,
  authority: LaunchAuthority,
): Promise<WorkspaceDTO> {
  const list = await agentOpsApi.listWorkspaces(
    { root_path: path, state: 'active', limit: 1 },
    { tenant: authority.tenant, signal: authority.signal },
  )
  const found = list.items.find(
    (w) => w.root_path === path && w.state === 'active',
  )
  if (found) return found
  const name = path.split('/').filter(Boolean).pop() || path
  return agentOpsApi.createWorkspace(
    {
      name,
      root_path: path,
      mount_mode: 'rw',
      container_target: '',
      allow_subpaths: [],
      max_read_bytes: 0,
      dlp_mode: DEFAULT_DLP_MODE,
    },
    authority,
  )
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

/** Sends a new session's first message and keeps it with the session. `partition` is
 * sentTurnsPartition() taken when the start began. The launch's authority guard travels
 * to every input dispatch, including retries. A launch waiting for an approval holds the
 * message and sends it once the session runs (sendWhenApproved), so the form opens the
 * session at once. */
async function sendFirstMessage(
  run: RunDTO,
  message: string,
  partition: string,
  options?: RequestOptions,
): Promise<void> {
  const prompt = message.trim()
  if (!prompt) return
  const { note } = useSentTurns.getState()
  // POST /runs answered 202 waiting_approval: no child takes input until the approval
  // (#500). The message waits with the launch instead of being refused by it.
  if (run.state === 'waiting_approval') {
    options?.signal?.throwIfAborted()
    options?.dispatchGuard?.()
    note(partition, run.run_ref, { text: prompt, waiting: true })
    void sendWhenApproved(run, prompt, partition, options?.tenant)
    return
  }
  try {
    await sendFirstPrompt(run, prompt, options)
    options?.signal?.throwIfAborted()
    options?.dispatchGuard?.()
    note(partition, run.run_ref, { text: prompt })
  } catch (err) {
    // Retirement is not a refused turn and must not resume a superseded launch.
    options?.signal?.throwIfAborted()
    options?.dispatchGuard?.()
    // A tool that stopped as it started did not start: the form says why.
    if (stoppedAtStartReason(err) !== null) throw err
    // The session runs and its first message did not reach it (HU2-27: a refused key,
    // "auth_required"). The session opens with the message and the engine's reason,
    // instead of a form saying it did not start while it runs on.
    note(partition, run.run_ref, {
      text: prompt,
      refused: err instanceof Error ? err.message : String(err),
    })
  }
}

/** How often a held first message reads its launch's state. */
const APPROVAL_POLL_MS = 2_000
/** Reads of a held message's launch that may fail in a row (a minute) before it is
 * kept as not sent, with the last error. */
const APPROVAL_READ_FAILURES = 30
/** The run states after which a held message can never be sent. */
const ENDED = new Set(['declined', 'expired', 'stopped', 'failed', 'cleaned'])

/** Sends a first message held for its launch's approval once the session runs, and keeps
 * it as not sent, with the engine's reason, when the launch is declined, expires or
 * stops first, or its state cannot be read. It goes on only while its held note is
 * shown: a sign-out or an organization change forgets every note (sent-turns), and with
 * it this one, so nothing is sent after one, even after switching back. The note lives
 * in this tab, so a reload before the approval forgets it, as it forgets every sent note.
 * The form's step-up attempt ends when the form closes; the owner of a held message is
 * the owner of its note, and the engine authorizes the input it finally posts. */
async function sendWhenApproved(
  run: RunDTO,
  prompt: string,
  partition: string,
  tenant: RequestOptions['tenant'],
) {
  const { note } = useSentTurns.getState()
  const held = () =>
    partition === sentTurnsPartition() &&
    (useSentTurns.getState().byRun[run.run_ref] ?? []).some(
      (t) => t.waiting && t.text === prompt && t.partition === partition,
    )
  const dispatchGuard = () => {
    if (!held())
      throw new DOMException('The held message was forgotten', 'AbortError')
  }
  const settle = (refused?: string) => {
    if (held())
      note(
        partition,
        run.run_ref,
        refused === undefined ? { text: prompt } : { text: prompt, refused },
      )
  }
  const reason = (err: unknown) =>
    err instanceof Error ? err.message : String(err)
  // An unexpected error is shown on the note instead of an unhandled rejection.
  try {
    let failures = 0
    for (;;) {
      await sleep(APPROVAL_POLL_MS)
      if (!held()) return
      let now: RunDTO
      try {
        now = await agentOpsApi.getRun(run.run_ref, { tenant, dispatchGuard })
        failures = 0
      } catch (err) {
        if (!held()) return
        // A passing network or server error is read again, for a while; a run this
        // sign-in can no longer read never runs for it.
        const forbidden =
          err instanceof ApiError && (err.status === 403 || err.status === 404)
        if (forbidden || ++failures >= APPROVAL_READ_FAILURES)
          return settle(reason(err))
        continue
      }
      if (!held()) return
      if (ENDED.has(now.state)) return settle(now.reason || now.state)
      // Approved and still starting (pending), or still waiting: read again.
      if (now.state !== 'running' && now.state !== 'idle') continue
      await sendFirstPrompt(now, prompt, { tenant, dispatchGuard })
      return settle()
    }
  } catch (err) {
    settle(reason(err))
  }
}

/** The child takes a moment to start; its first turn is retried briefly. */
async function sendFirstPrompt(
  run: RunDTO,
  prompt: string,
  options?: RequestOptions,
) {
  const body = sessionTurnBody(run, prompt)
  let lastError: unknown
  for (let i = 0; i < 20; i++) {
    options?.signal?.throwIfAborted()
    options?.dispatchGuard?.()
    try {
      if ('text' in body)
        await agentOpsApi.inputText(run.run_ref, body.text, undefined, options)
      else await agentOpsApi.input(run.run_ref, body.line, undefined, options)
      return
    } catch (err) {
      options?.signal?.throwIfAborted()
      options?.dispatchGuard?.()
      lastError = err
      await sleep(750)
    }
  }
  throw lastError
}
