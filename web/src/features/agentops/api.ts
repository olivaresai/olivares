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
  CreateBindingRequest,
  CreateProfileRequest,
  CreateRunRequest,
  CreateWorkspaceRequest,
  FileEntry,
  FileListResponse,
  FileReadResponse,
  PatchProfileRequest,
  ProviderBindingDTO,
  ProviderProfileConfigurationDTO,
  ProviderProfileDTO,
  RunDTO,
  RunEventDTO,
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
 * Pagination note (contract): the `/runs` cursor is IGNORED — a most-recent sort means
 * raising `limit` widens the page. Workspaces and file listings ARE keyset-paginated
 * (cursor + has_more).
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

const ref = (r: string) => encodeURIComponent(r)

/** The default page of the two profile-plane lists. A caller may narrow or widen it,
 * but a call that names no ceiling still asks for ONE page — the client never asks
 * the engine for "everything", and a screen that shows a page says so. */
export const PROFILE_PAGE = 100

export interface RunListParams {
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
  getRun: (r: string) => http.get<RunDTO>(`${RUNS}/${ref(r)}`),
  createRun: (body: CreateRunRequest) => http.post<RunDTO>(RUNS, body),
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
  input: (r: string, line: string, workLeaseFence?: number) =>
    http.post<{ accepted: boolean }>(
      `${RUNS}/${ref(r)}/input`,
      workLeaseFence === undefined
        ? { line }
        : { line, work_lease_fence: workLeaseFence },
    ),
  /**
   * Send one TURN to a session driven by an owned provider protocol (202 accepted).
   * The driver encodes the text as that provider's own turn method; the console never
   * builds a protocol frame itself.
   *
   * Work-bound runs present their fence here too — the fenced plane is the ONLY one a
   * stamped run answers on, and it is reached with the same sibling key the CLI sends.
   */
  inputText: (r: string, text: string, workLeaseFence?: number) =>
    http.post<{ accepted: boolean }>(
      `${RUNS}/${ref(r)}/input`,
      workLeaseFence === undefined
        ? { text }
        : { text, work_lease_fence: workLeaseFence },
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
  stop: (r: string) => http.post<RunDTO>(`${RUNS}/${ref(r)}/stop`),
  resume: (r: string) => http.post<RunDTO>(`${RUNS}/${ref(r)}/resume`),
  cleanup: (r: string) => http.post<RunDTO>(`${RUNS}/${ref(r)}/cleanup`),
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
  patchProfile: (r: string, body: PatchProfileRequest) =>
    http.patch<ProviderProfileDTO>(`${PROFILES}/${ref(r)}`, body),
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

  // --- Workspaces (governed file plane) ----------------------------------------
  listWorkspaces: (params?: WorkspaceListParams) =>
    http.get<ListResponse<WorkspaceDTO>>(WORKSPACES, { query: { ...params } }),
  getWorkspace: (r: string) =>
    http.get<WorkspaceDTO>(`${WORKSPACES}/${ref(r)}`),
  createWorkspace: (body: CreateWorkspaceRequest) =>
    http.post<WorkspaceDTO>(WORKSPACES, body),
  deleteWorkspace: (r: string) =>
    http.delete<{ deleted: boolean }>(`${WORKSPACES}/${ref(r)}`),

  // --- Files (jailed, DLP-labelled) --------------------------------------------
  listFiles: (r: string, params?: FileListParams) =>
    http.get<FileListResponse>(`${WORKSPACES}/${ref(r)}/files`, {
      query: { ...params },
    }),
  statFile: (r: string, path: string) =>
    http.get<FileEntry>(`${WORKSPACES}/${ref(r)}/files/stat`, {
      query: { path },
    }),
  readFile: (r: string, path: string) =>
    http.get<FileReadResponse>(`${WORKSPACES}/${ref(r)}/files/raw`, {
      query: { path },
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
    http.delete<{ deleted: boolean }>(`${WORKSPACES}/${ref(r)}/files`, {
      query: { path, recursive: recursive ? 'true' : undefined },
    }),
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
}
