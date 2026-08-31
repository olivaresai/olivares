// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the AgentCore Cedar export console.
//
// These cells exist for three invariants that a "just render the diff" screen
// would lose, each of which the engine can only defend if the console speaks to
// it correctly:
//
//  1. APPLY CARRIES THE HASH OF THE PLAN ON SCREEN. The engine re-plans and 409s
//     on a mismatch (agentcoreexport.go:177-188), so the console sending any other
//     hash — recomputed, remembered, or from a plan the operator replaced — would
//     turn that seam into a spurious conflict, and a console that recovered by
//     re-planning would apply a diff nobody reviewed.
//  2. 202 IS NEITHER SUCCESS NOR FAILURE, and neither is a 200 whose results carry
//     per-policy errors. Three 2xx meanings, three renderings.
//  3. 501 IS A CAPABILITY THIS DEPLOYMENT DID NOT WIRE, detected by status, and
//     the console names the variable that enables it — never a credential.
//
// The mocks stop at the HTTP CLIENT, not at ./api: the api module is the code that
// builds the request body and maps status onto the outcome union, so mocking it
// would delete the half of the contract these cells exist to check.
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import type { AgentCoreExportPlan } from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 'tenant-one' as string | null,
  can: (_permission: string): boolean => true,
  principal: { actor: 'user:operator', kind: 'user' } as {
    actor: string
    kind: string
  } | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const http = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  postWithMeta: vi.fn(),
  put: vi.fn(),
  putWithMeta: vi.fn(),
  patch: vi.fn(),
  patchWithMeta: vi.fn(),
  delete: vi.fn(),
  deleteWithMeta: vi.fn(),
  getWithMeta: vi.fn(),
  putRaw: vi.fn(),
}))
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return { ...actual, http }
})

import { AgentCoreExportView } from './agentcore-export-view'
import AgentCoreExportRoute from './agentcore-export-route'
import GovernanceView from './governance-view'
import { FEATURE_VIEWS } from '@/features/registry'

/**
 * The bytes the engine's own tests POST at the real route, read from the SAME
 * files rather than re-typed here. modules/governance/agentcoreexport_consolecontract_test.go
 * feeds these through the real decodeJSON (DisallowUnknownFields) and asserts the
 * engine accepts them; this side asserts the console actually emits them. Copying
 * the payload into each half instead would leave both green while they drifted
 * apart — the shape of the #690 defect.
 */
const FIXTURES = (() => {
  // Resolved from cwd rather than import.meta.url: under this runner the module
  // URL is not a file: URL. Both candidates are named so a miss fails with "the
  // fixtures moved" instead of a confusing ENOENT deep inside a cell.
  const candidates = [
    resolve(process.cwd(), '../modules/governance/testdata'),
    resolve(process.cwd(), 'modules/governance/testdata'),
  ]
  const hit = candidates.find((c) => existsSync(c))
  if (!hit)
    throw new Error(
      `AgentCore console-payload fixtures not found. Looked in:\n  ${candidates.join('\n  ')}`,
    )
  return hit
})()
function fixture(name: string): unknown {
  return JSON.parse(readFileSync(`${FIXTURES}/${name}`, 'utf8'))
}

/** The hash the apply fixtures carry — the plan the operator reviewed. */
const REVIEWED_HASH = 'plan-hash-the-operator-reviewed'

/**
 * `mode` defaults to ACTIVE but is a parameter because the plan's rendered mode
 * and the mode the operator selected CANNOT disagree in production — the engine
 * renders the plan with the mode in the request (agentcoreexport.go:224-228). A
 * fixture that hardcoded ACTIVE while the cell selected LOG_ONLY would be a
 * double accepting a pair production never produces, which is the contrast
 * finding C5.1 and the same class as #690.
 */
function planWithHash(hash: string, mode = 'ACTIVE'): AgentCoreExportPlan {
  return {
    PlanHash: hash,
    EngineID: 'pe-123',
    Tenant: 'tenant-one',
    Creates: [
      {
        Op: 'create',
        Name: 'olv_acme_g_1',
        PolicyID: '',
        Statement: 'permit(principal, action, resource);',
        Description: 'olivares:export:1',
        EnforcementMode: mode,
        RemoteEnforcementMode: '',
        RemoteFingerprint: '',
      },
    ],
    // null, not [] — ExportPlan has no omitempty and a nil Go slice marshals to
    // null (measured 2026-08-11). A `.map()` over these is a crash, not a bug in
    // the fixture.
    Updates: null,
    Deletes: null,
    Unchanged: null,
    Unmanaged: null,
    Unsupported: null,
  }
}

function wrap(ui: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
  )
}

/** Plan, then open the confirm dialog and confirm. Returns the user-event API. */
async function planAndApply(hash = REVIEWED_HASH) {
  const user = userEvent.setup()
  http.post.mockResolvedValue(planWithHash(hash))
  await user.click(screen.getByRole('button', { name: /compute plan/i }))
  await screen.findByText(new RegExp(hash))
  await user.click(screen.getByRole('button', { name: /apply this plan/i }))
  await user.click(screen.getByRole('button', { name: /^apply export$/i }))
  return user
}

/** The body the console handed the HTTP client on the apply call. */
function appliedBody(): unknown {
  expect(http.postWithMeta).toHaveBeenCalledTimes(1)
  return http.postWithMeta.mock.calls[0][1]
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  // Every list read in the parent view resolves empty; only the export routes
  // matter here. Without this the shared http mock answers `undefined` and the
  // failure looks like a bug in a tab this file is not testing.
  http.get.mockResolvedValue({ items: [], has_more: false })
})

describe('AgentCore export — the plan the operator reviewed is the plan applied', () => {
  it('applies the hash of the plan ON SCREEN', async () => {
    http.postWithMeta.mockResolvedValue({
      status: 200,
      data: { plan_hash: REVIEWED_HASH, results: [] },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)
    await planAndApply()

    await waitFor(() => expect(http.postWithMeta).toHaveBeenCalled())
    expect(appliedBody()).toMatchObject({ plan_hash: REVIEWED_HASH })
  })

  it('applies the hash of the SECOND plan after a re-plan, never the first', async () => {
    // The stale-hash mutant in its most realistic form: the operator plans, the
    // world moves, they re-plan and apply. A console caching the first hash sends
    // a hash for a diff that is no longer on screen.
    const user = userEvent.setup()
    http.postWithMeta.mockResolvedValue({
      status: 200,
      data: { plan_hash: 'second-plan', results: [] },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)

    http.post.mockResolvedValue(planWithHash('first-plan'))
    await user.click(screen.getByRole('button', { name: /compute plan/i }))
    await screen.findByText(/first-plan/)

    http.post.mockResolvedValue(planWithHash('second-plan'))
    await user.click(screen.getByRole('button', { name: /re-plan/i }))
    await screen.findByText(/second-plan/)

    await user.click(screen.getByRole('button', { name: /apply this plan/i }))
    await user.click(screen.getByRole('button', { name: /^apply export$/i }))

    await waitFor(() => expect(http.postWithMeta).toHaveBeenCalled())
    expect(appliedBody()).toMatchObject({ plan_hash: 'second-plan' })
  })

  it('an in-flight re-plan cannot swap the subject of an OPEN confirmation', async () => {
    //Codex contrast, C1.2 — the sharpest counterexample to the whole
    // screen: confirm a dialog describing plan A while plan B is landing behind
    // it, and a console reading live state applies B. The dialog is driven by a
    // snapshot and the landing plan closes it, so the operator is never holding a
    // confirmation for a diff that is no longer the one it describes.
    const user = userEvent.setup()
    http.postWithMeta.mockResolvedValue({
      status: 200,
      data: { plan_hash: 'plan-A', results: [] },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)

    http.post.mockResolvedValue(planWithHash('plan-A'))
    await user.click(screen.getByRole('button', { name: /compute plan/i }))
    await screen.findByText(/plan-A/)

    // Plan B is in flight and will not resolve until we release it.
    let releaseB: (p: AgentCoreExportPlan) => void = () => {}
    http.post.mockReturnValue(
      new Promise<AgentCoreExportPlan>((res) => {
        releaseB = res
      }),
    )
    await user.click(screen.getByRole('button', { name: /re-plan/i }))

    // While B is in flight the apply path is closed: the panel on screen is
    // about to be replaced.
    expect(
      screen.getByRole('button', { name: /apply this plan/i }),
    ).toBeDisabled()

    releaseB(planWithHash('plan-B'))
    await screen.findByText(/plan-B/)

    // No apply happened behind the modal, and nothing was submitted at all.
    expect(http.postWithMeta).not.toHaveBeenCalled()
  })

  it('pins the mode the plan was REQUESTED with, not the selector’s later value', async () => {
    //Codex contrast, C1.3. react-query re-binds a pending mutation's
    // options on every render, so an onSuccess that closes over `mode` pairs the
    // plan that was actually requested with whatever the selector says by the
    // time it lands. Apply would then carry a mode the displayed plan was not
    // computed with, the engine would re-plan under that mode, and the hashes
    // would not match — a 409 on a plan nobody touched.
    const user = userEvent.setup()
    http.postWithMeta.mockResolvedValue({
      status: 200,
      data: { plan_hash: REVIEWED_HASH, results: [] },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)

    // Request the plan under the TENANT DEFAULT (no override field)…
    let release: (p: AgentCoreExportPlan) => void = () => {}
    http.post.mockReturnValue(
      new Promise<AgentCoreExportPlan>((res) => {
        release = res
      }),
    )
    await user.click(screen.getByRole('button', { name: /compute plan/i }))

    // …then move the selector while that request is still in flight.
    await user.click(screen.getByRole('combobox', { name: /enforcement mode/i }))
    await user.click(await screen.findByRole('option', { name: /LOG_ONLY/ }))

    release(planWithHash(REVIEWED_HASH))
    await screen.findByText(new RegExp(REVIEWED_HASH))

    await user.click(screen.getByRole('button', { name: /apply this plan/i }))
    await user.click(screen.getByRole('button', { name: /^apply export$/i }))
    await waitFor(() => expect(http.postWithMeta).toHaveBeenCalled())

    // The plan came back for the DEFAULT request, so apply must carry no mode
    // override at all — LOG_ONLY was never the mode this plan was computed with.
    const sent = JSON.parse(JSON.stringify(appliedBody()))
    expect(sent).toEqual(fixture('agentcore_console_apply_default.json'))
    expect(Object.keys(sent)).toEqual(['plan_hash'])
  })

  it('discards the plan on screen when the enforcement mode changes', async () => {
    // The plan was computed for the OLD mode, so it no longer describes what the
    // control says. Leaving it applyable is how an operator applies a diff that
    // does not match the mode in front of them.
    const user = userEvent.setup()
    wrap(<AgentCoreExportView />)
    http.post.mockResolvedValue(planWithHash(REVIEWED_HASH))
    await user.click(screen.getByRole('button', { name: /compute plan/i }))
    expect(
      await screen.findByRole('button', { name: /apply this plan/i }),
    ).toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: /enforcement mode/i }))
    await user.click(await screen.findByRole('option', { name: /LOG_ONLY/ }))

    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: /apply this plan/i }),
      ).not.toBeInTheDocument(),
    )
  })
})

describe('AgentCore export — the console payload IS the engine contract', () => {
  it('posts exactly the apply bytes the engine test accepts (tenant default)', async () => {
    http.postWithMeta.mockResolvedValue({
      status: 200,
      data: { plan_hash: REVIEWED_HASH, results: [] },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)
    await planAndApply()

    await waitFor(() => expect(http.postWithMeta).toHaveBeenCalled())
    // Round-tripped through JSON.stringify because THAT is what the client sends
    // (client.ts:139) — an `undefined` field disappears on the wire, and this
    // assertion has to judge the bytes, not the object literal.
    const sent = JSON.parse(JSON.stringify(appliedBody()))
    expect(sent).toEqual(fixture('agentcore_console_apply_default.json'))
    // Named explicitly: the engine calls DisallowUnknownFields, so an extra key
    // here is a 400 in production. toEqual already fails on extras; this states
    // why it must.
    expect(Object.keys(sent)).toEqual(['plan_hash'])
  })

  it('posts exactly the apply bytes the engine test accepts (explicit mode)', async () => {
    const user = userEvent.setup()
    http.postWithMeta.mockResolvedValue({
      status: 200,
      data: { plan_hash: REVIEWED_HASH, results: [] },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)

    await user.click(screen.getByRole('combobox', { name: /enforcement mode/i }))
    await user.click(await screen.findByRole('option', { name: /LOG_ONLY/ }))

    // The plan the engine would return for THIS request: rendered in LOG_ONLY,
    // because that is the mode the request carried.
    http.post.mockResolvedValue(planWithHash(REVIEWED_HASH, 'LOG_ONLY'))
    await user.click(screen.getByRole('button', { name: /compute plan/i }))
    await screen.findByText(new RegExp(REVIEWED_HASH))
    await user.click(screen.getByRole('button', { name: /apply this plan/i }))
    // The confirmation names the mode taken from the PLAN, so a LOG_ONLY plan
    // must say LOG_ONLY — an operator confirming a weakening export has to read
    // it in the dialog, not infer it from the selector behind the modal.
    expect(await screen.findByText(/LOG_ONLY enforcement mode/i)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /^apply export$/i }))

    await waitFor(() => expect(http.postWithMeta).toHaveBeenCalled())
    const sent = JSON.parse(JSON.stringify(appliedBody()))
    expect(sent).toEqual(fixture('agentcore_console_apply_explicit_mode.json'))
  })

  it('posts the plan bytes the engine test accepts, for both mode choices', async () => {
    const user = userEvent.setup()
    http.post.mockResolvedValue(planWithHash(REVIEWED_HASH))
    wrap(<AgentCoreExportView />)

    await user.click(screen.getByRole('button', { name: /compute plan/i }))
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    expect(JSON.parse(JSON.stringify(http.post.mock.calls[0][1]))).toEqual(
      fixture('agentcore_console_plan_default.json'),
    )

    await user.click(screen.getByRole('combobox', { name: /enforcement mode/i }))
    await user.click(await screen.findByRole('option', { name: /LOG_ONLY/ }))
    await user.click(screen.getByRole('button', { name: /compute plan/i }))
    await waitFor(() => expect(http.post).toHaveBeenCalledTimes(2))
    expect(JSON.parse(JSON.stringify(http.post.mock.calls[1][1]))).toEqual(
      fixture('agentcore_console_plan_explicit_mode.json'),
    )
  })
})

describe('AgentCore export — the three 2xx meanings stay three', () => {
  it('paints a 202 as WAITING FOR APPROVAL, not as success', async () => {
    http.postWithMeta.mockResolvedValue({
      status: 202,
      data: {
        status: 'pending',
        approval_ref: 'approval-77',
        plan_hash: REVIEWED_HASH,
      },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)
    await planAndApply()

    expect(await screen.findByText(/waiting for approval/i)).toBeInTheDocument()
    expect(screen.getByText(/approval-77/)).toBeInTheDocument()
    expect(
      screen.getByText(/neither a success nor a failure/i),
    ).toBeInTheDocument()
    // The two ways this response gets lost: a success toast, or an error toast.
    expect(toast.success).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
    // Nothing was written, so the reviewed plan is still exactly what will be
    // applied — it stays on screen for a retry once the approval lands.
    expect(
      screen.getByRole('button', { name: /apply this plan/i }),
    ).toBeInTheDocument()
  })

  it('paints a 200 carrying a per-policy error as a FAILURE, not as applied', async () => {
    // The engine attempts the remaining writes after one fails and still answers
    // 200 (agentcoreexport.go:204-208). The only failure signal is inside results.
    http.postWithMeta.mockResolvedValue({
      status: 200,
      data: {
        plan_hash: REVIEWED_HASH,
        results: [
          { name: 'olv_a', op: 'create', status: 'CREATING' },
          {
            name: 'olv_b',
            op: 'update',
            status: 'FAILED',
            error: 'AccessDeniedException',
          },
        ],
      },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)
    await planAndApply()

    expect(
      await screen.findByText(/some policy writes failed/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/AccessDeniedException/)).toBeInTheDocument()
    expect(screen.queryByText(/^Export applied$/)).not.toBeInTheDocument()
  })

  it('paints a clean 200 as applied and consumes the plan', async () => {
    http.postWithMeta.mockResolvedValue({
      status: 200,
      data: {
        plan_hash: REVIEWED_HASH,
        results: [{ name: 'olv_a', op: 'create', status: 'CREATING' }],
      },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)
    await planAndApply()

    expect(await screen.findByText(/export applied/i)).toBeInTheDocument()
    // The remote engine has moved: this diff no longer describes it, so the
    // operator must re-plan rather than press apply twice.
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: /apply this plan/i }),
      ).not.toBeInTheDocument(),
    )
  })
})

describe('AgentCore export — the frontiers', () => {
  it('reports a 409 as "the plan changed" and does NOT re-plan by itself', async () => {
    http.postWithMeta.mockRejectedValue(
      new ApiError(409, 'conflict', 'plan changed; re-plan'),
    )
    wrap(<AgentCoreExportView />)
    await planAndApply()

    expect(await screen.findByText(/the plan changed/i)).toBeInTheDocument()
    // THE POINT OF THE WHOLE SCREEN: recovering from a 409 by planning again and
    // applying that would hand the engine a hash for a diff the operator never
    // saw. Exactly one plan call — the one the operator asked for.
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(http.postWithMeta).toHaveBeenCalledTimes(1)
  })

  it('reports a 501 as a capability this deployment did not wire, naming the variable', async () => {
    http.post.mockRejectedValue(
      new ApiError(501, 'not_implemented', 'AgentCore export is not configured'),
    )
    const user = userEvent.setup()
    wrap(<AgentCoreExportView />)
    await user.click(screen.getByRole('button', { name: /compute plan/i }))

    expect(await screen.findByText(/not wired in this deployment/i)).toBeInTheDocument()
    expect(
      screen.getByText('OLIVARES_AGENTCORE_EXPORT_CONFIG'),
    ).toBeInTheDocument()
    // Not an error, not a permission wall, not a blank screen.
    expect(toast.error).not.toHaveBeenCalled()
    expect(screen.queryByText(/not authorized/i)).not.toBeInTheDocument()
  })

  it('reports a 403 as the ENGINE’S REASON, not as "you lack the permission"', async () => {
    //Codex contrast, C2.2. The export gate answers 403 for a governance
    // DENIAL — dual-control unsatisfied, approval not bound to the plan, engine
    // off the allowlist (exporter.go:320-353). The console only got here because
    // can() already said the permission is held, so rendering "not authorized"
    // would report a human's rejection as a missing permission — the confusion
    // lib/api/client.ts:23-30 exists to warn about.
    http.postWithMeta.mockRejectedValue(
      new ApiError(
        403,
        'forbidden',
        'enforcement-weakening export requires dual-control (got 1 distinct approver(s), need 2)',
      ),
    )
    wrap(<AgentCoreExportView />)
    await planAndApply()

    expect(await screen.findByText(/refused this export/i)).toBeInTheDocument()
    expect(screen.getByText(/requires dual-control/i)).toBeInTheDocument()
    expect(screen.queryByText(/not authorized/i)).not.toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('treats a *_FAILED policy status as a failure even with no error field', async () => {
    //Codex contrast, C2.1. The exporter copies AWS's Status and sets Err
    // only when the CALL failed (exporter.go:427-437), so a policy AWS accepted
    // and then could not complete arrives inside a clean 200 with no `error` at
    // all. Keying only on `error` reports "Export applied" over a policy that
    // never reached the engine. The status vocabulary is the contract's
    // (the agentcore-export contract).
    http.postWithMeta.mockResolvedValue({
      status: 200,
      data: {
        plan_hash: REVIEWED_HASH,
        results: [
          { name: 'olv_a', op: 'create', status: 'ACTIVE' },
          {
            name: 'olv_b',
            op: 'create',
            status: 'CREATE_FAILED',
            status_reasons: ['cedar validation found 1 error'],
          },
        ],
      },
      headers: new Headers(),
    })
    wrap(<AgentCoreExportView />)
    await planAndApply()

    expect(
      await screen.findByText(/some policy writes failed/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/cedar validation found 1 error/)).toBeInTheDocument()
    expect(screen.queryByText(/^Export applied$/)).not.toBeInTheDocument()
  })

  it('NEVER offers a credential field — the AWS keys live on the host', async () => {
    // agentcoreexportwiring.go:16-21: the config file carries AWS write
    // credentials BY VALUE and is operator-only. A console that grew a "paste
    // your key here" field would be pulling them into the product; the only
    // control this surface owns is the enforcement mode.
    const user = userEvent.setup()
    const { container } = wrap(<AgentCoreExportView />)
    http.post.mockResolvedValue(planWithHash(REVIEWED_HASH))
    await user.click(screen.getByRole('button', { name: /compute plan/i }))
    await screen.findByText(new RegExp(REVIEWED_HASH))

    expect(container.querySelectorAll('input')).toHaveLength(0)
    expect(container.querySelectorAll('textarea')).toHaveLength(0)
    expect(screen.queryByText(/secret|access key|credential value/i)).toBeNull()
  })
})

describe('AgentCore export — the surface is REACHABLE', () => {
  // THE WIRING CELLS. Every cell above renders AgentCoreExportView directly, so
  // all of them stay green while the surface is unreachable from the shell —
  // which is how a whole feature ships invisible.
  it('is registered as its own route, gated on the permission the engine enforces', () => {
    const entry = FEATURE_VIEWS.find((v) => v.id === 'agentcoreExport')
    expect(entry).toBeDefined()
    expect(entry?.path).toBe('/agentcore-export')
    // NOT governance:identity:read. The export permission is independently
    // grantable (governance.go:397), so gating the route on the identity read of
    // the /permissions view would make it unreachable for exactly the operator it
    // was delegated to.
    expect(entry?.permission).toBe('governance:agentcore-export:admin')
  })

  it('renders the routed page and runs a plan from it', async () => {
    const user = userEvent.setup()
    http.post.mockResolvedValue(planWithHash(REVIEWED_HASH))
    wrap(<AgentCoreExportRoute />)

    await user.click(
      await screen.findByRole('button', { name: /compute plan/i }),
    )
    expect(await screen.findByText(new RegExp(REVIEWED_HASH))).toBeInTheDocument()
  })

  it('no longer hides behind the governance view’s identity gate', async () => {
    // The regression this replaces: the export used to be a tab under
    // /permissions. If it comes back as one, this fails.
    authState.can = (p: string) => p === 'governance:agentcore-export:admin'
    wrap(<GovernanceView />)

    expect(
      screen.queryByRole('tab', { name: /agentcore export/i }),
    ).not.toBeInTheDocument()
  })
})
