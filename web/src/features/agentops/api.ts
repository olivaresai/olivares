// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  http,
  type RequestOptions,
  type TenantRequestOptions,
} from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  LaunchReadinessIsolation,
  LaunchReadinessTransport,
  SessionLaunchReadiness,
} from './launch-readiness'
import {
  parseHostToolObservation,
  type HostToolObservation,
} from './host-tools'
import type {
  AdoptAccountRequest,
  CreateAccountRequest,
  CreateBindingRequest,
  CreateProfileRequest,
  CreateRunRequest,
  CreateWorkspaceRequest,
  FileEntry,
  FileListResponse,
  FileReadResponse,
  PatchProfileRequest,
  ProviderAccountDTO,
  ProviderAccountMetadataPatch,
  ProviderBindingDTO,
  ProviderProfileConfigurationDTO,
  ProviderProfileDTO,
  ResolvedProfileDTO,
  RunChangedFileDTO,
  RunChangesDTO,
  RunGitStatusDTO,
  RunGitAction,
  RunGitRequest,
  RunWorktreeDiffDTO,
  RunWorktreeDiffFileDTO,
  RunDTO,
  RunEventDTO,
  ToolReadinessDTO,
  WorkspaceDTO,
  WriteResponse,
} from './types'

/**
 * Claude Code OPERATE endpoints (module II) — under /v1/m/sessions/,
 * gated by `sessions:run:{read,write,admin}` (runs/live) and
 * `sessions:workspace:{read,write,admin}` (files). The web consumes the engine's
 * governed API and renders it; it adds no logic (ARCHITECTURE.md). The SSE attach stream
 * (`/runs/{ref}/attach`) is consumed via the dedicated cursor-aware `useRunAttach` hook
 * (attach.ts), NOT a path here.
 *
 * Runs and workspaces are newest-first by default: cursor is ignored and limit widens the
 * page. Runs also accept `pagination: 'cursor'` for a complete ID-ordered traversal.
 * Workspace root_path/state filters apply before the limit. File listings
 * are keyset-paginated (cursor + has_more).
 *
 * The two run filters are NOT the same kind of filter, and the difference is visible
 * from here (modules/sessions/runtime_api.go:80):
 *  - `state` narrows the PAGE after it is read, so it answers "…among the N most
 *    recent runs". For a facet over a list the operator is already scrolling, that is
 *    a narrowing they can see.
 *  - `claude_session_id` narrows the STORE, so it answers "…among all runs".
 *    It has to: it is how a session card learns whether Olivares LAUNCHED this session
 *    or merely FOUND it, and a page-bound answer would report "discovered" for a
 *    session whose run has simply scrolled off.
 */
const RUNS = '/v1/m/sessions/runs'
const WORKSPACES = '/v1/m/sessions/workspaces'
const PROFILES = '/v1/m/sessions/provider-profiles'
const BINDINGS = '/v1/m/sessions/provider-source-bindings'
const ACCOUNTS = '/v1/m/sessions/provider-accounts'
const ref = (r: string) => encodeURIComponent(r)

/** The default page of the two profile-plane lists. A caller may narrow or widen it,
 * but a call that names no ceiling still asks for ONE page — the client never asks
 * the engine for "everything", and a screen that shows a page says so. */
export const PROFILE_PAGE = 100

export interface RunListParams {
  /** Complete traversal in stable ID order; absent keeps the recency page. */
  pagination?: 'cursor'
  state?: string
  /**: the LEGACY observed session a run drives — an EXACT store lookup, not a
   * page narrowing, and answered with legacy (unprofiled) runs only: a profiled run
   * is found by `live_ref`. Empty/absent lists every run. */
  claude_session_id?: string
  /** The run the plane PROVED owns a live row (the managed row's run, written by
   * the bridge in the transaction that bound the id). An observed, source or legacy
   * row has no proven run: the answer is empty, never "the first match". */
  live_ref?: string
  limit?: number
  cursor?: string
}

export interface ProfileListParams {
  state?: string
  limit?: number
  cursor?: string
}

/** The page of the account list. The list is keyset-paginated (cursor + has_more);
 * a screen that shows one page says whether more exist. */
export const ACCOUNT_PAGE = 100

export interface AccountListParams {
  cursor?: string
}

export interface BindingListParams {
  /** Narrow to the bindings of ONE profile (exact store filter). */
  profile_ref?: string
  limit?: number
  cursor?: string
}

export interface EventListParams {
  limit?: number
  cursor?: string
}

export interface WorkspaceListParams {
  limit?: number
  cursor?: string
  root_path?: string
  state?: string
}

export interface FileListParams {
  path?: string
  limit?: number
  cursor?: string
}

/**
 * THE SCOPE A READ IS PINNED TO — the twin of `SessionsReadScope` in
 * features/sessions/api.ts, and deliberately declared here rather than imported from
 * there: the run plane does not depend on the observed plane, and a shared alias would
 * invert that. A WHITELIST for the reason `TenantListOptions` gives in client.ts
 * (`Omit` would readmit `anonymous` into the literal whose point is a mandatory tenant).
 *
 * OPTIONAL: a caller that passes nothing behaves exactly as before.
 */
export type AgentOpsReadScope = TenantRequestOptions &
  Pick<RequestOptions, 'signal'>

export const agentOpsApi = {
  // --- Runs (lifecycle) ---------------------------------------------------------
  listRuns: (params?: RunListParams, scope?: AgentOpsReadScope) =>
    http.get<ListResponse<RunDTO>>(RUNS, {
      // Rebuilt, never spread (client.ts, TenantListOptions).
      query: { ...params },
      tenant: scope?.tenant,
      signal: scope?.signal,
    }),
  getRun: (r: string, options?: RequestOptions) =>
    http.get<RunDTO>(`${RUNS}/${ref(r)}`, options),
  createRun: (
    body: CreateRunRequest,
    opts?: Pick<RequestOptions, 'tenant' | 'signal' | 'dispatchGuard'>,
  ) => http.post<RunDTO>(RUNS, body, opts),
  runEvents: (r: string, params?: EventListParams) =>
    http.get<ListResponse<RunEventDTO>>(`${RUNS}/${ref(r)}/events`, {
      query: { ...params },
    }),
  /**
   * Write one raw NDJSON line to a live stream-json session's stdin (202 accepted).
   *
   * ⛔ RAW FRAMES ONLY, AND ONLY FOR THE FRAME-DRIVEN CHILD. This is the historical
   * Claude path. A run driven by an owned provider protocol refuses it with 400
   * before any child effect, and rightly: its child is a JSON-RPC peer that would
   * read the line as a method call. Use `inputText` for those — the two are separate
   * operations on purpose, and neither is derived from the other's payload.
   *
   * A work-bound run presents the fence observed on that exact run, exactly as
   * `olivares agent session input --line … --work-lease-fence N` does: one sibling
   * key, omitted entirely when there is none.
   */
  input: (
    r: string,
    line: string,
    workLeaseFence?: number,
    options?: RequestOptions,
  ) =>
    http.post<{ accepted: boolean }>(
      `${RUNS}/${ref(r)}/input`,
      workLeaseFence === undefined
        ? { line }
        : { line, work_lease_fence: workLeaseFence },
      options,
    ),
  /**
   * Send one TURN to a session driven by an owned provider protocol (202 accepted).
   * The driver encodes the text as that provider's own turn method; the console never
   * builds a protocol frame itself.
   *
   * Work-bound runs present their fence here too — the fenced plane is the ONLY one a
   * stamped run answers on, and it is reached with the same sibling key the CLI sends.
   */
  inputText: (
    r: string,
    text: string,
    workLeaseFence?: number,
    options?: RequestOptions,
  ) =>
    http.post<{ accepted: boolean }>(
      `${RUNS}/${ref(r)}/input`,
      workLeaseFence === undefined
        ? { text }
        : { text, work_lease_fence: workLeaseFence },
      options,
    ),
  /** Cancel the active turn while retaining the owned process and conversation.
   * Work-bound runs must present the fence observed on that exact run. */
  interrupt: (r: string, workLeaseFence?: number) =>
    http.post<RunDTO>(
      `${RUNS}/${ref(r)}/interrupt`,
      workLeaseFence === undefined
        ? undefined
        : { work_lease_fence: workLeaseFence },
    ),
  /** Local Git status; every action runs inside the session OS boundary. */
  gitStatus: (r: string, opts?: { signal?: AbortSignal }) =>
    http.get<RunGitStatusDTO>(`${RUNS}/${ref(r)}/git`, opts),
  gitAction: (
    r: string,
    action: RunGitAction,
    body: RunGitRequest,
    options?: TenantRequestOptions &
      Pick<RequestOptions, 'signal' | 'dispatchGuard' | 'sessionEffects'>,
  ) => {
    switch (action) {
      case 'stage':
        return http.post<{ ok: boolean }>(
          `${RUNS}/${ref(r)}/git/stage`,
          body,
          options,
        )
      case 'unstage':
        return http.post<{ ok: boolean }>(
          `${RUNS}/${ref(r)}/git/unstage`,
          body,
          options,
        )
      case 'commit':
        return http.post<{ ok: boolean }>(
          `${RUNS}/${ref(r)}/git/commit`,
          body,
          options,
        )
      case 'branch':
        return http.post<{ ok: boolean }>(
          `${RUNS}/${ref(r)}/git/branch`,
          body,
          options,
        )
    }
  },
  /** The loopback ports the session's own processes listen on (none when not live). */
  previewPorts: (r: string, opts?: { signal?: AbortSignal }) =>
    http.get<{ ports: number[] }>(`${RUNS}/${ref(r)}/preview`, {
      signal: opts?.signal,
    }),
  /** Opens a browser preview of one of those ports: a same-origin URL whose token is
   * its only credential, valid within the hour and while the session runs. */
  openPreview: (r: string, port: number) =>
    http.post<{ url: string; port: number; expires_at: string }>(
      `${RUNS}/${ref(r)}/preview`,
      { port },
    ),
  /** The files the session changed in its folder since it started (read-only). */
  changes: (r: string, opts?: { signal?: AbortSignal }) =>
    http.get<RunChangesDTO>(`${RUNS}/${ref(r)}/changes`, {
      signal: opts?.signal,
    }),
  /** One changed file's current text (at most 256 KiB), or with `rev: 'HEAD'` the text
   * git HEAD holds for it (404: no readable repository, or the file is not in HEAD). */
  changedFile: (
    r: string,
    path: string,
    opts?: { signal?: AbortSignal; rev?: 'HEAD' },
  ) =>
    http.get<RunChangedFileDTO>(`${RUNS}/${ref(r)}/changes/file`, {
      query: { path, rev: opts?.rev },
      signal: opts?.signal,
    }),
  /** What the session's worktree branch changed since it left the workspace's current
   * commit (committed work only, read-only). 404 for a session without a worktree. */
  worktreeDiff: (r: string, opts?: { signal?: AbortSignal }) =>
    http.get<RunWorktreeDiffDTO>(`${RUNS}/${ref(r)}/diff`, {
      signal: opts?.signal,
    }),
  /** One path of that branch at the base and at the branch tip (at most 64 KiB each). */
  worktreeDiffFile: (
    r: string,
    path: string,
    opts?: { signal?: AbortSignal },
  ) =>
    http.get<RunWorktreeDiffFileDTO>(`${RUNS}/${ref(r)}/diff/file`, {
      query: { path },
      signal: opts?.signal,
    }),
  /** Which sessions this run may message or hand work to (ARCH COMMS-PATH #1, MC): a list
   * of canonical session ids, or every live session of the run's template. */
  setPeers: (
    r: string,
    body: { peers: string[] } | { peers_rule: 'same-template' },
  ) => http.put<RunDTO>(`${RUNS}/${ref(r)}/peers`, body),
  /** Stop the run. A work-bound run presents its fence until its lease has ended
   * (`currentControlFence`); then the engine applies ordinary control. */
  stop: (r: string, workLeaseFence?: number) =>
    http.post<RunDTO>(
      `${RUNS}/${ref(r)}/stop`,
      workLeaseFence === undefined
        ? undefined
        : { work_lease_fence: workLeaseFence },
    ),
  resume: (r: string) => http.post<RunDTO>(`${RUNS}/${ref(r)}/resume`),
  // discardWorktree is the person's confirmation to remove the session's worktree and
  // branch although its work is not merged; without it no body is sent.
  cleanup: (r: string, discardWorktree = false) =>
    http.post<RunDTO>(
      `${RUNS}/${ref(r)}/cleanup`,
      discardWorktree ? { discard_worktree: true } : undefined,
    ),
  deleteRun: (r: string) =>
    http.delete<{ deleted: boolean }>(`${RUNS}/${ref(r)}`),

  // --- Provider profiles (references and labels, never a path) ----------
  // Gated by `sessions:profile:{read,write,admin}`: read lists/gets, write creates
  // and renames/enables/disables, admin retires (irreversible) and reads the
  // configuration — the ONLY call that carries a path, so it is issued on demand
  // by an explicit control, never on render.
  // The reads take an optional AbortSignal (react-query's queryFn context): when the
  // authority boundary moves, the previous boundary's queries are cancelled and the
  // network request goes with them, so a late answer cannot land anywhere.
  listProfiles: (params?: ProfileListParams, opts?: { signal?: AbortSignal }) =>
    http.get<ListResponse<ProviderProfileDTO>>(PROFILES, {
      query: { limit: PROFILE_PAGE, ...params },
      signal: opts?.signal,
    }),
  getProfile: (r: string, opts?: { signal?: AbortSignal }) =>
    http.get<ProviderProfileDTO>(`${PROFILES}/${ref(r)}`, {
      signal: opts?.signal,
    }),
  createProfile: (body: CreateProfileRequest) =>
    http.post<ProviderProfileDTO>(PROFILES, body),
  /** Which profile a new session of a tool uses is the engine's one rule (its own
   * login when signed in, otherwise a key or local model from Providers): the
   * readiness reads its answer for every tool, the resolve acts on it. Clients never
   * choose. */
  toolsReadiness: (opts?: { signal?: AbortSignal }) =>
    http.get<{ tools: ToolReadinessDTO[] }>(`${PROFILES}/readiness`, {
      signal: opts?.signal,
    }),
  resolveProfile: (
    driver: string,
    opts?: Pick<RequestOptions, 'tenant' | 'signal' | 'dispatchGuard'>,
  ) => http.post<ResolvedProfileDTO>(`${PROFILES}/resolve`, { driver }, opts),
  patchProfile: (
    r: string,
    body: PatchProfileRequest,
    opts?: Pick<RequestOptions, 'signal' | 'dispatchGuard'>,
  ) => http.patch<ProviderProfileDTO>(`${PROFILES}/${ref(r)}`, body, opts),
  retireProfile: (r: string) =>
    http.post<ProviderProfileDTO>(`${PROFILES}/${ref(r)}/retire`),
  /** The authorized configuration read. It takes an AbortSignal because its caller is
   * an EXPLICIT reveal cycle (useFreshRead), never a cached query: Hide, unmount or a
   * change of permission/identity/tenant/credential aborts it, and a response that
   * lands afterwards is discarded rather than painted or cached. */
  profileConfiguration: (r: string, opts?: { signal?: AbortSignal }) =>
    http.get<ProviderProfileConfigurationDTO>(
      `${PROFILES}/${ref(r)}/configuration`,
      { signal: opts?.signal },
    ),
  /**
   * Point read of ONE profile's local launch requirements. It is not an
   * inventory, not a launchable boolean and not an authorization. The query is
   * closed: only transport and isolation. Cache-Control: no-store on the
   * engine; the caller must not reuse another selection's answer.
   */
  profileLaunchReadiness: (
    r: string,
    query: {
      transport?: LaunchReadinessTransport
      isolation?: LaunchReadinessIsolation
    },
    opts?: { signal?: AbortSignal },
  ) =>
    http.get<SessionLaunchReadiness>(`${PROFILES}/${ref(r)}/launch-readiness`, {
      query,
      signal: opts?.signal,
    }),
  /** HC1: the official CLI candidates the service observed for ONE selected profile.
   * No query field exists (the engine refuses any). The body is parsed against the
   * closed contract before a caller sees it; the result is advisory and never a launch,
   * readiness or installation decision. */
  profileHostTools: async (
    r: string,
    opts?: { signal?: AbortSignal },
  ): Promise<HostToolObservation> =>
    parseHostToolObservation(
      await http.get<unknown>(`${PROFILES}/${ref(r)}/host-tools`, {
        signal: opts?.signal,
      }),
    ),

  // --- Source → profile bindings ------------------------------------------
  // Gated by `sessions:profile-binding:{read,write,admin}` — independent of the
  // profile tiers. Creating one ALSO needs source administration (`system:admin`),
  // checked by the engine's composition port.
  listBindings: (params?: BindingListParams, opts?: { signal?: AbortSignal }) =>
    http.get<ListResponse<ProviderBindingDTO>>(BINDINGS, {
      query: { limit: PROFILE_PAGE, ...params },
      signal: opts?.signal,
    }),
  /** The point read of ONE binding, as the engine holds it NOW. Its caller is the
   * explicit Details cycle of the bindings table (useFreshRead): the read starts when
   * the dialog opens or on an explicit Refresh, and the signal ends it when the dialog
   * closes, the row changes, or the permission, principal, tenant or credential moves —
   * a late answer is discarded, never painted, never cached. */
  getBinding: (r: string, opts?: { signal?: AbortSignal }) =>
    http.get<ProviderBindingDTO>(`${BINDINGS}/${ref(r)}`, {
      signal: opts?.signal,
    }),
  createBinding: (body: CreateBindingRequest) =>
    http.post<ProviderBindingDTO>(BINDINGS, body),
  revokeBinding: (r: string) =>
    http.post<ProviderBindingDTO>(`${BINDINGS}/${ref(r)}/revoke`),

  // --- Provider accounts (named profiles; references and labels, never a path) ----
  // Gated by `sessions:account:{read,write}`: read lists and gets, write adopts an
  // existing profile as an account. The reads take the AbortSignal of their query, so
  // a moved authority boundary cancels them at the network.
  listAccounts: (params?: AccountListParams, opts?: { signal?: AbortSignal }) =>
    http.get<ListResponse<ProviderAccountDTO>>(ACCOUNTS, {
      query: { limit: ACCOUNT_PAGE, cursor: params?.cursor },
      signal: opts?.signal,
    }),
  getAccount: (r: string, opts?: { signal?: AbortSignal }) =>
    http.get<ProviderAccountDTO>(`${ACCOUNTS}/${ref(r)}`, {
      signal: opts?.signal,
    }),
  patchAccountMetadata: (
    r: string,
    body: ProviderAccountMetadataPatch,
    scope: TenantRequestOptions & Pick<RequestOptions, 'dispatchGuard'>,
  ) =>
    http.patch<ProviderAccountDTO>(`${ACCOUNTS}/${ref(r)}`, body, {
      tenant: scope.tenant,
      dispatchGuard: scope.dispatchGuard,
    }),
  /**
   * Adopt ONE existing profile as an account. The body is always a JSON object — `{}`
   * asks the server to generate the name, and the engine refuses an empty body. The
   * tenant is the one the operator submitted under, and the dispatch guard runs
   * immediately before the request leaves, so a draft confirmed under one authority
   * is never sent under the next.
   */
  createAccount: (
    body: CreateAccountRequest,
    scope: TenantRequestOptions & Pick<RequestOptions, 'dispatchGuard'>,
  ) =>
    http.post<ProviderAccountDTO>(ACCOUNTS, body, {
      tenant: scope.tenant,
      dispatchGuard: scope.dispatchGuard,
    }),
  adoptAccount: (
    profileRef: string,
    body: AdoptAccountRequest,
    scope: TenantRequestOptions & Pick<RequestOptions, 'dispatchGuard'>,
  ) =>
    http.post<ProviderAccountDTO>(
      `${ACCOUNTS}/${ref(profileRef)}/adopt`,
      body,
      {
        tenant: scope.tenant,
        dispatchGuard: scope.dispatchGuard,
      },
    ),

  // --- Workspaces (governed file plane) ----------------------------------------
  listWorkspaces: (params?: WorkspaceListParams, scope?: AgentOpsReadScope) =>
    http.get<ListResponse<WorkspaceDTO>>(WORKSPACES, {
      query: { ...params },
      tenant: scope?.tenant,
      signal: scope?.signal,
    }),
  getWorkspace: (r: string, scope?: AgentOpsReadScope) =>
    http.get<WorkspaceDTO>(`${WORKSPACES}/${ref(r)}`, {
      tenant: scope?.tenant,
      signal: scope?.signal,
    }),
  createWorkspace: (
    body: CreateWorkspaceRequest,
    opts?: Pick<RequestOptions, 'tenant' | 'signal' | 'dispatchGuard'>,
  ) => http.post<WorkspaceDTO>(WORKSPACES, body, opts),
  deleteWorkspace: (r: string) =>
    http.delete<{ deleted: boolean }>(`${WORKSPACES}/${ref(r)}`),
  /** Replaces the whole list; [] removes every folder. The answer is the workspace as saved. */
  setWorkspaceReadOnlyFolders: (r: string, folders: string[]) =>
    http.patch<WorkspaceDTO>(`${WORKSPACES}/${ref(r)}`, {
      read_only_folders: folders,
    }),

  // --- Files (jailed, DLP-labelled) --------------------------------------------
  listFiles: (r: string, params?: FileListParams) =>
    http.get<FileListResponse>(`${WORKSPACES}/${ref(r)}/files`, {
      query: { ...params },
    }),
  statFile: (r: string, path: string) =>
    http.get<FileEntry>(`${WORKSPACES}/${ref(r)}/files/stat`, {
      query: { path },
    }),
  /** A file's content, or with `rev: 'HEAD'` the content git HEAD holds for it (404: no
   * readable repository, or the file is not in HEAD). */
  readFile: (r: string, path: string, rev?: 'HEAD') =>
    http.get<FileReadResponse>(`${WORKSPACES}/${ref(r)}/files/raw`, {
      query: { path, rev },
    }),
  /** Write file content as RAW bytes (the API reads the body verbatim, never JSON). */
  writeFile: (r: string, path: string, content: string) =>
    http.putRaw<WriteResponse>(`${WORKSPACES}/${ref(r)}/files/raw`, content, {
      query: { path },
      contentType: 'text/plain; charset=utf-8',
    }),
  mkdir: (r: string, path: string) =>
    http.post<FileEntry>(`${WORKSPACES}/${ref(r)}/files/dir`, undefined, {
      query: { path },
    }),
  moveFile: (r: string, from: string, to: string) =>
    http.post<{ moved: boolean }>(`${WORKSPACES}/${ref(r)}/files/move`, {
      from,
      to,
    }),
  deleteFile: (r: string, path: string, recursive = false) =>
    http.delete<{ deleted: boolean }>(
      `${WORKSPACES}/${ref(r)}/files`,
      undefined,
      { query: { path, recursive: recursive ? 'true' : undefined } },
    ),
}

/** The SSE attach path for a run (consumed by useRunAttach with a `from` cursor). */
export function runAttachPath(r: string): string {
  return `${RUNS}/${ref(r)}/attach`
}

/** Tenant-scoped query keys (CONTRACT: tenant-scoped data MUST carry the active tenant
 * id; invalidate the narrowest prefix that changed). */
export const agentOpsKeys = {
  all: (tenant: string | null) => ['agentops', tenant] as const,
  runs: (tenant: string | null, params?: RunListParams) =>
    params === undefined
      ? (['agentops', tenant, 'runs'] as const)
      : (['agentops', tenant, 'runs', params] as const),
  /**
   * The run list UNDER an authority boundary, for a caller that must not hand a new
   * principal the runs the previous one read. It extends `boundaryScope` (below) like
   * every other scoped key, so the cancel + remove `useAuthBoundary` already performs
   * for the boundary that left reaches this list too — the run half of a scoped screen
   * needs no cleanup of its own.
   *
   * `runs(t, …)` above keeps its meaning and its callers: a screen whose reads are not
   * partitioned by boundary (the session card's exact-store lookups) still uses it.
   */
  runsScoped: (tenant: string | null, epoch: number, params?: RunListParams) =>
    params === undefined
      ? (['agentops', tenant, 'b', epoch, 'runs'] as const)
      : (['agentops', tenant, 'b', epoch, 'runs', params] as const),
  run: (tenant: string | null, r: string) =>
    ['agentops', tenant, 'run', r] as const,
  runChanges: (tenant: string | null, r: string) =>
    ['agentops', tenant, 'run', r, 'changes'] as const,
  runPreviewPorts: (tenant: string | null, r: string) =>
    ['agentops', tenant, 'run', r, 'preview'] as const,
  runWorktreeDiff: (tenant: string | null, r: string) =>
    ['agentops', tenant, 'run', r, 'diff'] as const,
  runWorktreeDiffFile: (
    tenant: string | null,
    r: string,
    path: string | null,
  ) => ['agentops', tenant, 'run', r, 'diff', 'file', path] as const,
  /**
   * THE PROVIDER-PROFILE PLANE IS PARTITIONED BY AUTHORITY BOUNDARY, not only by
   * tenant. `epoch` is the OPAQUE number useAuthBoundary derives for one
   * (principal, tenant, credential) combination — never a user id, a session id or
   * a token — so two principals reading the same tenant with the same permission
   * hold DIFFERENT cache entries, and a remount under a new boundary starts with
   * nothing to paint until that boundary's own answer arrives. The tenant stays in
   * the key (the tenant contract of query.ts), the scope sits under it, and every
   * narrower key extends `boundaryScope`, so prefix invalidation keeps working:
   * `profiles(t, e)` ⊂ `profiles(t, e, params)`, `all(t)` ⊂ everything.
   */
  boundaryScope: (tenant: string | null, epoch: number) =>
    ['agentops', tenant, 'b', epoch] as const,
  profiles: (
    tenant: string | null,
    epoch: number,
    params?: ProfileListParams,
  ) =>
    params === undefined
      ? (['agentops', tenant, 'b', epoch, 'profiles'] as const)
      : (['agentops', tenant, 'b', epoch, 'profiles', params] as const),
  profile: (tenant: string | null, epoch: number, r: string) =>
    ['agentops', tenant, 'b', epoch, 'profile', r] as const,
  /**
   * Point readiness of ONE selected tuple. Extends `profile(t, e, r)` so
   * invalidating that profile drops the observation; the selection object keeps
   * two tuples from sharing a cache entry. There is deliberately no inventory
   * key and no key without a profile ref.
   */
  launchReadiness: (
    tenant: string | null,
    epoch: number,
    r: string,
    selection: {
      transport: LaunchReadinessTransport
      isolation: LaunchReadinessIsolation
      state?: string
      auth_source?: string
      updated_at?: string
    },
  ) =>
    [
      'agentops',
      tenant,
      'b',
      epoch,
      'profile',
      r,
      'launch-readiness',
      selection,
    ] as const,
  /**
   * HC1 host-tool observation of ONE selected profile snapshot. Extends
   * `profile(t, e, r)`; the identity is the readiness snapshot it belongs to (version,
   * driver, profile environment and answering environment), so a newer snapshot never
   * shares an entry with an older one. No key exists without a profile ref.
   */
  hostTools: (
    tenant: string | null,
    epoch: number,
    r: string,
    identity: {
      profile_version: number
      driver: string
      environment_ref: string
      evaluated_environment_ref: string
    },
  ) =>
    [
      'agentops',
      tenant,
      'b',
      epoch,
      'profile',
      r,
      'host-tools',
      identity,
    ] as const,
  // There is deliberately NO key for the configuration read: the stored homes are
  // never placed in the QueryCache. They live in the explicit reveal cycle of the
  // sheet that asked for them (useFreshRead) and leave with it.
  bindings: (
    tenant: string | null,
    epoch: number,
    params?: BindingListParams,
  ) =>
    params === undefined
      ? (['agentops', tenant, 'b', epoch, 'bindings'] as const)
      : (['agentops', tenant, 'b', epoch, 'bindings', params] as const),
  binding: (tenant: string | null, epoch: number, r: string) =>
    ['agentops', tenant, 'b', epoch, 'binding', r] as const,
  /** The account list and the account point reads, under the same boundary scope as
   * the profile plane, so the boundary's cancel-and-remove reaches them too. Without a
   * ref, `account` is the prefix of every point read. */
  accounts: (tenant: string | null, epoch: number) =>
    ['agentops', tenant, 'b', epoch, 'accounts'] as const,
  account: (tenant: string | null, epoch: number, r?: string) =>
    r === undefined
      ? (['agentops', tenant, 'b', epoch, 'account'] as const)
      : (['agentops', tenant, 'b', epoch, 'account', r] as const),
  /** A submitted adoption whose answer the page has not shown (the profile reference
   *  only, never an account). No request answers this key: the page writes it. It is
   *  under the boundary scope, so a boundary move removes it. */
  accountCreation: (tenant: string | null, epoch: number) =>
    ['agentops', tenant, 'b', epoch, 'account-creation'] as const,
  accountAdoption: (tenant: string | null, epoch: number) =>
    ['agentops', tenant, 'b', epoch, 'account-adoption'] as const,
  runEvents: (tenant: string | null, r: string, params?: EventListParams) =>
    params === undefined
      ? (['agentops', tenant, 'run', r, 'events'] as const)
      : (['agentops', tenant, 'run', r, 'events', params] as const),
  workspaces: (tenant: string | null, params?: WorkspaceListParams) =>
    params === undefined
      ? (['agentops', tenant, 'workspaces'] as const)
      : (['agentops', tenant, 'workspaces', params] as const),
  workspace: (tenant: string | null, r: string) =>
    ['agentops', tenant, 'workspace', r] as const,
  files: (tenant: string | null, r: string, path: string) =>
    ['agentops', tenant, 'workspace', r, 'files', path] as const,
  file: (tenant: string | null, r: string, path: string) =>
    ['agentops', tenant, 'workspace', r, 'file', path] as const,
  /** What git HEAD holds for a file. A sibling of `file`, not a child: saving the file
   * invalidates `file`, and HEAD's text does not change when it is saved. */
  fileHead: (tenant: string | null, r: string, path: string) =>
    ['agentops', tenant, 'workspace', r, 'file-head', path] as const,
}
