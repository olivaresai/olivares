// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The first hour: install an agent tool, sign it in with its own login, start a
// session in a folder. Everything here composes engine routes that already
// exist; the only first-hour routes are the tool sign-in (/v1/m/agenttools/sign-in).
// Which profile a session runs under is the engine's answer
// (/v1/m/sessions/provider-profiles/resolve), never chosen here.
import { http } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import { agentToolsApi, type ToolJob } from '@/features/agent-tools/api'
import { agentOpsApi } from '@/features/agentops/api'
import { sessionTurnBody } from '@/features/agentops/session-turn'
import type {
  RunDTO,
  SecretEnvRef,
  WorkspaceDTO,
} from '@/features/agentops/types'
import { TOOL_NAMES, type SessionTool } from '@/features/agentops/tool-names'

export type ToolKey = 'claude' | 'codex'
export const FIRST_HOUR_TOOLS: readonly ToolKey[] = ['claude', 'codex']
/** The tools whose own login the engine relays: the first-hour tools, and Grok
 * Build, whose release is installed from AI tools (HU-R15). */
export type SignInTool = ToolKey | 'grok'
/** Every tool a session can start on: the engine's drivers (agentops/tool-names). */
export {
  SESSION_TOOLS,
  TOOL_NAMES,
  type SessionTool,
} from '@/features/agentops/tool-names'

export interface SignInStatus {
  driver: SignInTool
  installed: boolean
  signed_in: boolean
  method?: string
  account?: string
}

export interface SignInFlow {
  id: string
  driver: ToolKey
  state:
    'starting' | 'needs_code' | 'waiting' | 'checking' | 'signed_in' | 'failed'
  url?: string
  user_code?: string
  message?: string
}

const SIGN_IN = '/v1/m/agenttools/sign-in'

/** A tool's own login is the ORGANIZATION's (FH 036): the engine keeps it in the
 * tenant's own home, never the server user's. These are system routes, which
 * ignore the tenant selection, so the organization is named on every call. */
export const signInApi = {
  status: (driver: SignInTool, tenant: string | null, signal?: AbortSignal) =>
    http.get<SignInStatus>(SIGN_IN, {
      query: { driver, tenant_id: tenant ?? '' },
      signal,
    }),
  start: (driver: SignInTool, tenant: string | null) =>
    http.post<SignInFlow>(SIGN_IN, { driver, tenant_id: tenant ?? '' }),
  get: (id: string, signal?: AbortSignal) =>
    http.get<SignInFlow>(`${SIGN_IN}/${encodeURIComponent(id)}`, { signal }),
  code: (id: string, code: string) =>
    http.post<SignInFlow>(`${SIGN_IN}/${encodeURIComponent(id)}/code`, {
      code,
    }),
  cancel: (id: string) =>
    http.delete<{ ok: boolean }>(`${SIGN_IN}/${encodeURIComponent(id)}`),
}

export const firstHourKeys = {
  /** Everything first-hour reads for a tenant: a Providers change refreshes it. */
  all: (tenant: string | null) => ['first-hour', tenant] as const,
  signIn: (tenant: string | null, driver: SignInTool) =>
    ['first-hour', tenant, 'sign-in', driver] as const,
  /** Under signIn(tenant, driver), so a sign-in or an install that settles asks again. */
  runsOn: (tenant: string | null, driver: SessionTool) =>
    ['first-hour', tenant, 'sign-in', driver, 'runs-on'] as const,
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

/** Install the latest official release of a tool in one action: the engine
 * resolves and verifies the release, then installs it; this waits for the job.
 * The engine installs one tool at a time, so a second Install waits its turn. */
export async function installLatest(driver: ToolKey): Promise<ToolJob> {
  const plan = await agentToolsApi.plan(driver, 'latest')
  const request = { plan_digest: plan.digest, request_id: crypto.randomUUID() }
  let job: ToolJob | undefined
  for (let i = 0; !job; i++) {
    try {
      job = await agentToolsApi.install(request)
    } catch (err) {
      if (!(err instanceof ApiError && err.code === 'install_busy') || i >= 600)
        throw err
      await sleep(1000)
    }
  }
  for (let i = 0; job.state === 'running' && i < 600; i++) {
    await sleep(1000)
    job = await agentToolsApi.job(job.id)
  }
  return job
}

/** The permission choices of a session, the same for every tool (N1: the engine applies
 * each preset to every driver, and refuses with one sentence a launch a tool cannot honour).
 * "Edit files and run commands" (the default) is the engine's built-in template: an
 * allowlist under dontAsk, so the named tools run without asking and every other tool is
 * refused. The others are plain permission modes. */
export type PermissionChoice =
  'editsAndCommands' | 'editsOnly' | 'readOnly' | 'ask'
export const DEFAULT_PERMISSION: PermissionChoice = 'editsAndCommands'
const EDITS_AND_COMMANDS_TEMPLATE = 'Edits and commands'
const PERMISSION_MODE: Record<PermissionChoice, string> = {
  editsAndCommands: '',
  editsOnly: 'acceptEdits',
  readOnly: 'plan',
  ask: 'default',
}

interface TemplateItem {
  id: string
  name: string
  builtin: boolean
}

async function templateIdFor(
  choice: PermissionChoice,
): Promise<string | undefined> {
  if (choice !== 'editsAndCommands') return undefined
  const list = await http.get<{ items: TemplateItem[] }>(
    '/v1/m/sessions/templates',
    {
      query: { builtin: 'true', limit: 100 },
    },
  )
  return list.items.find(
    (t) => t.builtin && t.name === EDITS_AND_COMMANDS_TEMPLATE,
  )?.id
}

/** A launch the gate holds for a second person's approval. */
export function needsApproval(err: unknown): boolean {
  return err instanceof Error && /human approval/i.test(err.message)
}

/** The registered folder for a path, created when it is new. */
async function ensureFolder(path: string): Promise<WorkspaceDTO> {
  const list = await agentOpsApi.listWorkspaces({ limit: 200 })
  const found = list.items.find(
    (w) => w.root_path === path && w.state === 'active',
  )
  if (found) return found
  const name = path.split('/').filter(Boolean).pop() || path
  return agentOpsApi.createWorkspace({
    name,
    root_path: path,
    mount_mode: 'rw',
    container_target: '',
    allow_subpaths: [],
    max_read_bytes: 0,
    dlp_mode: 'off',
  })
}

export interface StartSessionInput {
  driver: SessionTool
  folder: string
  prompt: string
  permission: PermissionChoice
  name?: string
  /** Vault secrets the session receives as environment variables, by name. */
  secretEnv?: SecretEnvChoice[]
}

/** One vault secret (env/…) chosen for a session, and the variable it is read from. */
export type SecretEnvChoice = SecretEnvRef

/** Start a session: the profile the engine resolves for the tool (its own login,
 * or a key from Providers), the folder, the permission choice and, when given,
 * the first prompt. Returns the run. */
export async function startSession(input: StartSessionInput): Promise<RunDTO> {
  const { profile } = await agentOpsApi.resolveProfile(input.driver)
  const folder = input.folder.trim()
  const workspace = folder ? await ensureFolder(folder) : undefined
  const template_id = await templateIdFor(input.permission)
  const run = await agentOpsApi.createRun({
    name: input.name ?? sessionName(input),
    transport: 'stream-json',
    permission_mode: PERMISSION_MODE[input.permission],
    effort: '',
    model: '',
    workspace_ref: workspace?.workspace_ref ?? '',
    isolation: 'native',
    env_allow: [],
    provider_profile_ref: profile.profile_ref,
    ...(template_id ? { template_id } : {}),
    ...(input.secretEnv?.length ? { secret_env: input.secretEnv } : {}),
  })
  const prompt = input.prompt.trim()
  if (prompt) await sendFirstPrompt(run, prompt)
  return run
}

/** A session is named by what it was asked first, or by its tool and folder, so
 * the sidebar and Home can list it by name. */
function sessionName(input: StartSessionInput): string {
  const prompt = input.prompt.trim().replace(/\s+/g, ' ')
  if (prompt) return prompt.length > 60 ? `${prompt.slice(0, 59)}…` : prompt
  const tool = TOOL_NAMES[input.driver]
  const folder = input.folder.trim().split('/').filter(Boolean).pop()
  return folder ? `${tool} in ${folder}` : tool
}

/** The child takes a moment to start; its first turn is retried briefly. */
async function sendFirstPrompt(run: RunDTO, prompt: string) {
  const body = sessionTurnBody(run, prompt)
  let lastError: unknown
  for (let i = 0; i < 20; i++) {
    try {
      if ('text' in body) await agentOpsApi.inputText(run.run_ref, body.text)
      else await agentOpsApi.input(run.run_ref, body.line)
      return
    } catch (err) {
      lastError = err
      await sleep(750)
    }
  }
  throw lastError
}
