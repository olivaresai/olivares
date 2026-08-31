// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Properties of the Cedar/OPA publish + activation lifecycle. Each test pins ONE
// named honesty property of the console — the ones a green backend suite cannot
// catch, because they are about what the screen CLAIMS, not about what the engine
// computed. The property is written above the test as a comment.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  pdpValidate: vi.fn(),
  pdpExplain: vi.fn(),
  pdpDryRun: vi.fn(),
  pdpVersions: vi.fn(),
  pdpGetVersion: vi.fn(),
  pdpActive: vi.fn(),
  pdpPublish: vi.fn(),
  pdpRollback: vi.fn(),
  pdpTestStatus: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, claudePolicyApi: api }
})

import { CedarOpaView } from './cedar-opa-view'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const CEDAR_ACTIVE = {
  engine: 'cedar' as const,
  authored: {
    present: true,
    revision: 2,
    content: 'permit(principal, action, resource);',
    sha256: 'sha256:aaaa1111bbbb2222cccc',
  },
  managed: {
    present: true,
    revision: 5,
    sha256: 'sha256:dddd3333eeee4444ffff',
  },
  adopted: { present: false },
  union_sha256: 'sha256:9999888877776666',
}

const OPA_ACTIVE = {
  engine: 'opa' as const,
  authored: {
    present: true,
    revision: 1,
    content: 'package olivares.authz\n',
    sha256: 'sha256:1111222233334444',
  },
  managed: { present: false },
  adopted: { present: false },
}

const GATE_DETAIL =
  'stored result of the compile/validation gate run before this immutable revision was committed'

function gateOk(revision: number) {
  return {
    engine: 'cedar' as const,
    revision,
    available: true,
    passed: 1,
    failed: 0,
    total: 1,
    results: [
      { name: 'publish_compile_validate', passed: true, detail: GATE_DETAIL },
    ],
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = () => true
  api.pdpActive.mockImplementation((engine: string) =>
    Promise.resolve(engine === 'opa' ? OPA_ACTIVE : CEDAR_ACTIVE),
  )
  api.pdpVersions.mockResolvedValue({ items: [], has_more: false })
  api.pdpTestStatus.mockResolvedValue(gateOk(2))
})

afterEach(() => {
  vi.restoreAllMocks()
})

/** Open the engine Select and pick OPA / Rego. */
async function selectOpa(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('combobox', { name: /^Engine$/i }))
  await user.click(await screen.findByRole('option', { name: /OPA/i }))
}

describe('Cedar/OPA publish + activation lifecycle', () => {
  // V1 — an OPA action NEVER claims enforcement: its primary action is "Version",
  // not "Publish", and the confirm body names the sidecar as the enforcement owner.
  it('never claims enforcement for an OPA action', async () => {
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)
    await selectOpa(user)

    expect(
      await screen.findByRole('button', { name: /^Version$/ }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /^Publish$/ }),
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /^Version$/ }))
    const dialog = await screen.findByRole('dialog')
    const body = dialog.textContent ?? ''

    expect(body).toMatch(/your own OPA sidecar owns Rego enforcement/i)
    expect(body).toMatch(/Nothing is enforced from this action/i)
    // No claim that anything was activated / recompiled / put in force here.
    expect(body).not.toMatch(/activat/i)
    expect(body).not.toMatch(/recompiled/i)
  })

  // V2 — the activation confirm states the POINTER MOVE: no new revision, no
  // deletion of later ones, history preserved. And it never uses a word that
  // implies mutating history (revert / restore / undo / discard).
  it('states that activating a revision is a pointer move, not a history rewrite', async () => {
    api.pdpVersions.mockResolvedValue({
      items: [
        { revision: 2, surface: 'cedar', validated: true, active: true },
        { revision: 1, surface: 'cedar', validated: true },
      ],
      has_more: false,
    })
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)

    await user.click(
      await screen.findByRole('button', { name: /Activate this revision/i }),
    )
    const dialog = await screen.findByRole('dialog')
    const body = dialog.textContent ?? ''

    expect(body).toMatch(/does not create a new revision/i)
    expect(body).toMatch(/does not delete any later revision/i)
    expect(body).toMatch(/immutable history is preserved in full/i)
    expect(body).not.toMatch(/revert/i)
    expect(body).not.toMatch(/restore/i)
    expect(body).not.toMatch(/undo/i)
    expect(body).not.toMatch(/discard/i)
  })

  // V3 — live_activation "deferred" renders as a WARNING, never a success: the
  // revision is stored and selected but the PREVIOUS policy is still enforcing.
  it('renders a deferred activation as a warning, never as a success', async () => {
    api.pdpPublish.mockResolvedValue({
      engine: 'cedar',
      revision: 3,
      active: true,
      live_activation: 'deferred',
      note: 'committed and selected in the store, but the live grant engine was NOT swapped',
    })
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)

    await user.click(await screen.findByRole('button', { name: /^Publish$/ }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /^Publish$/ }))

    expect(
      await screen.findByText(
        /the PREVIOUS policy is still deciding requests/i,
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/Stored and selected — but NOT enforcing/i),
    ).toBeInTheDocument()
    // The real recovery paths are named; no reload button is offered.
    expect(
      screen.getByText(/There is no endpoint that reloads the PDP on demand/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /reload/i }),
    ).not.toBeInTheDocument()

    // No success is presented — not as a toast, and not as an "activated" panel.
    expect(toast.success).not.toHaveBeenCalled()
    expect(toast.warning).toHaveBeenCalled()
    expect(
      screen.queryByText(/Compiled and activated/i),
    ).not.toBeInTheDocument()
  })

  // V4 — the stored gate result is NEVER dressed up as a test suite: its name and
  // detail render verbatim, and an unavailable artifact shows its reason with no
  // counters at all.
  it('renders the gate artifact verbatim and never as a test suite', async () => {
    wrap(<CedarOpaView active />)
    expect(await screen.findByText('Compile/validate gate')).toBeInTheDocument()
    expect(await screen.findByText(GATE_DETAIL)).toBeInTheDocument()
    expect(screen.getByText('publish_compile_validate')).toBeInTheDocument()
    // The absence half is the half that matters: presence assertions alone would
    // still pass if a "1 / 1 tests passed" counter were added beside them, which is
    // exactly the fabrication this property exists to forbid.
    expect(screen.queryByText(/tests? passed/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/\b1\s*\/\s*1\b/)).not.toBeInTheDocument()
    expect(
      screen.queryByRole('heading', { name: /^tests$/i }),
    ).not.toBeInTheDocument()
  })

  it('renders an unavailable gate artifact as its reason, with no counters', async () => {
    api.pdpTestStatus.mockResolvedValue({
      engine: 'cedar',
      revision: 2,
      available: false,
      reason: 'no stored test artifact is available for cedar revision 2',
    })
    wrap(<CedarOpaView active />)

    expect(
      await screen.findByText(
        'no stored test artifact is available for cedar revision 2',
      ),
    ).toBeInTheDocument()
    // No "1/1", no pass/fail badge — the reason is the whole truth we have.
    expect(screen.queryByText(/\d+\s*\/\s*\d+/)).not.toBeInTheDocument()
    expect(screen.queryByText(/^passed$/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/^failed$/i)).not.toBeInTheDocument()
  })

  // V5 — cedar and opa can BOTH have an active revision, so there is one active
  // badge PER ENGINE group; and because revision numbers collide across engines the
  // React key must be composite (no duplicate-key warning for cedar r1 + opa r1).
  it('shows one active badge per engine and keys rows by engine:revision', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    api.pdpVersions.mockResolvedValue({
      items: [
        { revision: 1, surface: 'cedar', validated: true, active: true },
        { revision: 1, surface: 'opa', validated: true, active: true },
      ],
      has_more: false,
    })
    wrap(<CedarOpaView active />)

    const cedarList = await screen.findByRole('list', {
      name: /Cedar revisions/i,
    })
    const opaList = screen.getByRole('list', { name: /OPA \/ Rego revisions/i })

    expect(
      within(cedarList).getByText('Active · selected in the store'),
    ).toBeInTheDocument()
    expect(
      within(opaList).getByText('Selected (not enforced here)'),
    ).toBeInTheDocument()
    // The OPA group never claims to be enforcing/active.
    expect(
      within(opaList).queryByText(/Active · selected in the store/),
    ).not.toBeInTheDocument()

    const duplicateKeyWarnings = consoleError.mock.calls.filter((call) =>
      call.some(
        (arg) => typeof arg === 'string' && /same key|duplicate/i.test(arg),
      ),
    )
    expect(duplicateKeyWarnings).toEqual([])
  })

  // V6 — the diff never falls through to a silently empty pane: when the active
  // revision cannot be loaded the console says exactly that and BLOCKS publish, so
  // a change is never made against an unknown baseline.
  it('says it cannot show the diff and disables publish when the active revision fails to load', async () => {
    api.pdpActive.mockRejectedValue(
      new ApiError(500, 'internal', 'store unavailable'),
    )
    wrap(<CedarOpaView active />)

    expect(
      await screen.findByText(
        /Cannot show the diff — the active revision could not be loaded/i,
      ),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^Publish$/ })).toBeDisabled()
  })

  // V7 — a read-only operator still sees the truth: version history, the active
  // surfaces and the gate all render; only the write actions are withheld.
  it('renders every read panel without admin, and no write action', async () => {
    authState.can = (p: string) => !p.endsWith(':admin')
    api.pdpVersions.mockResolvedValue({
      items: [
        { revision: 2, surface: 'cedar', validated: true, active: true },
        { revision: 1, surface: 'cedar', validated: true },
      ],
      has_more: false,
    })
    wrap(<CedarOpaView active />)

    // Reads.
    expect(
      await screen.findByRole('list', { name: /Cedar revisions/i }),
    ).toBeInTheDocument()
    expect(screen.getByText('Compile/validate gate')).toBeInTheDocument()
    expect(await screen.findByText(GATE_DETAIL)).toBeInTheDocument()
    // The union disclosure is a read panel too: it names what else is in force.
    expect(
      screen.getByText(
        /The enforced Cedar policy is the UNION of three surfaces/i,
      ),
    ).toBeInTheDocument()
    expect(screen.getByText(/r5 · sha256 dddd3333eeee/)).toBeInTheDocument()

    // Writes.
    expect(
      screen.queryByRole('button', { name: /^Publish$/ }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Activate this revision/i }),
    ).not.toBeInTheDocument()
  })

  // V8 — a 403 on publish is a permission boundary, shown as a calm warning; it is
  // never a red generic error.
  it('surfaces a 403 on publish as a warning, not a red error', async () => {
    api.pdpPublish.mockRejectedValue(
      new ApiError(403, 'forbidden', 'not authorized'),
    )
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)

    await user.click(await screen.findByRole('button', { name: /^Publish$/ }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /^Publish$/ }))

    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(toast.error).not.toHaveBeenCalled()
  })

  // V9 — activating an older revision is not done blind: the dialog reads that
  // revision's stored source so the operator sees what would come into force.
  // Publishing blind and activating blind are the same hazard, and the
  // pre-publish diff only closes one half of it.
  it('shows what an older revision contains before activating it', async () => {
    api.pdpVersions.mockResolvedValue({
      items: [
        { revision: 2, surface: 'cedar', validated: true, active: true },
        { revision: 1, surface: 'cedar', validated: true },
      ],
      has_more: false,
    })
    api.pdpGetVersion.mockResolvedValue({
      revision: 1,
      surface: 'cedar',
      validated: true,
      content: 'forbid(principal, action, resource);',
    })
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)

    await user.click(
      await screen.findByRole('button', { name: /Activate this revision/i }),
    )
    // The target revision is READ — the dialog cannot describe what it never fetched.
    await waitFor(() =>
      expect(api.pdpGetVersion).toHaveBeenCalledWith('cedar', 1),
    )
    expect(
      await screen.findByLabelText(/r1 \(would become active\)/i),
    ).toBeInTheDocument()
  })

  // V10 — a revision whose source cannot be read says so. An empty comparison and
  // an unreadable revision look identical on screen and mean opposite things, so
  // the failure is named rather than rendered as "nothing changes".
  it('names an unreadable revision instead of showing an empty comparison', async () => {
    api.pdpVersions.mockResolvedValue({
      items: [
        { revision: 2, surface: 'cedar', validated: true, active: true },
        { revision: 1, surface: 'cedar', validated: true },
      ],
      has_more: false,
    })
    api.pdpGetVersion.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)

    await user.click(
      await screen.findByRole('button', { name: /Activate this revision/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await waitFor(() =>
      expect(dialog.textContent ?? '').toMatch(
        /Could not read the source of r1/i,
      ),
    )
  })

  // V11 — the activation preview compares against the TARGET's engine, never the
  // engine selected in the dropdown. The history lists both engines, so a reader on
  // Cedar can activate an OPA revision; diffing that against the live CEDAR policy
  // would render a fabricated Cedar-vs-Rego changeset presented as the effect of the
  // activation. This is the property, not an implementation detail.
  it('diffs an activation against its own engine, not the selected one', async () => {
    api.pdpVersions.mockResolvedValue({
      items: [
        { revision: 4, surface: 'cedar', validated: true, active: true },
        { revision: 2, surface: 'opa', validated: true },
        { revision: 1, surface: 'opa', validated: true, active: true },
      ],
      has_more: false,
    })
    api.pdpGetVersion.mockResolvedValue({
      revision: 2,
      surface: 'opa',
      validated: true,
      content: 'package olivares.authz\n\ndefault allow := false\n',
    })
    const user = userEvent.setup()
    // Engine stays on the default (cedar) while an OPA revision is activated.
    wrap(<CedarOpaView active />)

    const opaGroup = await screen.findByRole('list', {
      name: 'OPA / Rego revisions',
    })
    await user.click(
      within(opaGroup).getAllByRole('button', {
        name: /Activate this revision/i,
      })[0]!,
    )

    // The baseline is re-read for OPA — not reused from the Cedar dropdown state.
    await waitFor(() => expect(api.pdpActive).toHaveBeenCalledWith('opa'))
    // ...and the Cedar policy text never appears as the comparison baseline.
    const dialog = await screen.findByRole('dialog')
    expect(dialog.textContent ?? '').not.toMatch(/permit\(principal/)
  })

  // V12 — "we could not read which revision is active" is never rendered as "no
  // revision is active". The gate result is per-revision, so an unreadable active
  // revision means there is nothing to report — not an empty history.
  it('separates an unknown active revision from an absent one', async () => {
    api.pdpActive.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    wrap(<CedarOpaView active />)

    expect(
      await screen.findByText(/Could not read which revision is active/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/No revision is active yet/i),
    ).not.toBeInTheDocument()
  })
  // V13 — THE POSITIVE. Until now the ONLY live_activation fixture in the whole web
  // suite was 'deferred' (V3), so nothing pinned the other side: the console could
  // have reported "enforcing" for a policy that is not, and every test stayed green.
  // This is also the fact that was unreadable at all before — it arrived only in the
  // response to a publish, so it lived in component state and died on reload. Here
  // it comes from a plain GET, with no publish in the test at all.
  it('reports a measured applied state on a plain read, with no publish', async () => {
    api.pdpActive.mockResolvedValue({
      ...CEDAR_ACTIVE,
      live_activation: 'applied',
    })
    wrap(<CedarOpaView active />)

    expect(await screen.findByText('enforcing here')).toBeInTheDocument()
    expect(
      screen.getByText(
        /This process is deciding requests with the revision the store selects/i,
      ),
    ).toBeInTheDocument()
    // The claim is SCOPED. "applied" is a fact about one process — the swap is an
    // in-memory map write with no event and no propagation — so the panel must not
    // let it read as an estate-wide guarantee.
    expect(
      screen.getByText(/another replica answers for itself/i),
    ).toBeInTheDocument()
    // And it never announces the dangerous state while reporting the safe one.
    expect(screen.queryByText('not enforcing here')).not.toBeInTheDocument()
  })

  // V14 — the deferred state SURVIVES A RELOAD. This is the P0 the audit named: the
  // store commits and selects revision N while the PREVIOUS policy keeps deciding,
  // and before this every later read answered authored:{present:true, revision:N}
  // with nothing at all about enforcement. A second operator, or the same one after
  // F5, saw a screen that looked entirely healthy.
  it('keeps reporting a deferred activation on a reload, with no publish', async () => {
    api.pdpActive.mockResolvedValue({
      ...CEDAR_ACTIVE,
      live_activation: 'deferred',
    })
    wrap(<CedarOpaView active />)

    expect(await screen.findByText('not enforcing here')).toBeInTheDocument()
    expect(
      screen.getByText(
        /a previously compiled policy is still in force here/i,
      ),
    ).toBeInTheDocument()
    // Both facts are on screen at once: the store selects r2, and it is not what
    // is deciding requests. Reporting only the first is what made this dangerous.
    expect(screen.getByText(/r2 · sha256 aaaa1111bbbb/)).toBeInTheDocument()
    expect(screen.queryByText('enforcing here')).not.toBeInTheDocument()
  })

  // V15 — an engine that does not report the field is a THIRD answer, not a benign
  // one. The old dispatch was by exclusion with the calmest branch last, so anything
  // unrecognised was announced as "Revision stored — nothing is enforced".
  it('treats an unreported activation state as unknown, never as harmless', async () => {
    // CEDAR_ACTIVE deliberately carries NO live_activation — an older engine.
    wrap(<CedarOpaView active />)

    expect(
      await screen.findByText(
        /Could not find out whether this process is enforcing the selected revision/i,
      ),
    ).toBeInTheDocument()
    // Not dressed as either of the two states it is not.
    expect(screen.queryByText('enforcing here')).not.toBeInTheDocument()
    expect(screen.queryByText('not enforcing here')).not.toBeInTheDocument()
  })

  // V16 — THE OTHER P0. A FAILED READ must not be rendered as a measured absence.
  // `s?.present ? … : "none"` collapsed "the engine said present:false" with "there
  // is no object because the read failed", so a down backend printed
  // "Managed — none / Adopted — none" and told the operator that what they edit is
  // all that decides. The engine emits `present` without omitempty precisely so a
  // false is measured; the console was throwing that guarantee away.
  it('never prints "none" for a contributing surface it could not read', async () => {
    api.pdpActive.mockRejectedValue(
      new ApiError(500, 'internal', 'store unavailable'),
    )
    wrap(<CedarOpaView active />)

    expect(
      await screen.findByText(
        /the contributing surfaces are unknown/i,
      ),
    ).toBeInTheDocument()
    // The absence assertions are the load-bearing ones: the rows themselves must
    // not render at all, because a row is what carries the fabricated "none".
    expect(
      screen.queryByText('Managed (RBAC projection)'),
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Adopted (signed bundle)')).not.toBeInTheDocument()
    expect(
      screen.queryByText(
        /The enforced Cedar policy is the UNION of three surfaces/i,
      ),
    ).not.toBeInTheDocument()
  })

  // V17 — the OPA dry-run is a CONSTANT (the route returns allow:true for every
  // Rego document, because nothing can be evaluated in-process), and it was painted
  // as a green "Allowed". A probe that answers the same for every input has not
  // measured anything, and the engine's own comment forbids implying a grant.
  it('never paints the constant OPA dry-run as a measured Allowed', async () => {
    api.pdpDryRun.mockResolvedValue({
      evaluated: false,
      allow: true,
      engine: 'opa',
      reason:
        'OPA candidate evaluation requires the OPA sidecar (the authored Rego is not deployed there yet); the PDP layer imposes no restriction here — RBAC still governs. Nothing was evaluated: this answer is the same for every Rego source.',
    })
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)
    await selectOpa(user)

    await user.click(screen.getByRole('button', { name: /^Dry-run$/ }))

    expect(await screen.findByText('Not evaluated')).toBeInTheDocument()
    expect(screen.queryByText('Allowed')).not.toBeInTheDocument()
    expect(screen.getByText(/Nothing was evaluated/i)).toBeInTheDocument()
  })

  // V18 — NON-FIRING DIRECTION for V17: a badge that always says "Not evaluated"
  // would pass V17 and be just as useless. A cedar dry-run IS an evaluation.
  it('still shows a real cedar decision as Allowed or Denied', async () => {
    api.pdpDryRun.mockResolvedValue({
      evaluated: true,
      allow: false,
      engine: 'cedar',
      reason: 'a forbid rule matched — the policy RESTRICTS this request',
    })
    const user = userEvent.setup()
    wrap(<CedarOpaView active />)

    await user.click(await screen.findByRole('button', { name: /^Dry-run$/ }))

    expect(await screen.findByText('Denied')).toBeInTheDocument()
    expect(screen.queryByText('Not evaluated')).not.toBeInTheDocument()
  })

  // V19 — a 404 from a MOUNTED route is a real failure, not "the backend is not
  // built yet". All nine /pdp/ routes are registered on the engine
  // (modules/governance/governance.go), so the calm info-toned seam — "the backend
  // endpoint is not live yet … Nothing is faked" — was a roadmap note pasted over a
  // rejection, and it left the operator nothing to retry.
  it('does not dress a live route 404 as an unbuilt backend', async () => {
    api.pdpVersions.mockRejectedValue(
      new ApiError(404, 'not_found', 'not found'),
    )
    wrap(<CedarOpaView active />)

    expect(await screen.findByText('Something went wrong')).toBeInTheDocument()
    expect(screen.queryByText('Backend pending')).not.toBeInTheDocument()
    expect(screen.queryByText(/Nothing is faked/i)).not.toBeInTheDocument()
    expect(
      screen.queryByText(/the backend endpoint is not live yet/i),
    ).not.toBeInTheDocument()
  })
  // V20 — A TENANT THAT HAS PUBLISHED NOTHING IS NOT "ENFORCING". The first cut of
  // this feature compared sha256(loaded source) with the store's union digest, and
  // `contentDigest("")` matches an empty union, so every brand-new tenant — the
  // commonest state there is — got the green "enforcing here" badge with all three
  // surfaces absent. An adversarial contrast reproduced it against a real engine.
  it('never badges a tenant with no selected policy as enforcing', async () => {
    api.pdpActive.mockResolvedValue({
      engine: 'cedar',
      authored: { present: false },
      managed: { present: false },
      adopted: { present: false },
      live_activation: 'no_policy',
    })
    wrap(<CedarOpaView active />)

    expect(await screen.findByText('no policy selected')).toBeInTheDocument()
    expect(
      screen.getByText(/there is nothing in force from here/i),
    ).toBeInTheDocument()
    expect(screen.queryByText('enforcing here')).not.toBeInTheDocument()
    expect(screen.queryByText('not enforcing here')).not.toBeInTheDocument()
  })

  // V21 — grants_expired is a SEPARATE axis and must be able to contradict a green
  // badge: past the offline-staleness bound the engine still holds exactly the
  // selected policy, so `applied` is true, while its POSITIVE grants have degraded
  // to abstain. Reporting only "enforcing here" would be a half-truth about which
  // half of the policy is actually deciding.
  it('says grants have expired even while the policy is applied', async () => {
    api.pdpActive.mockResolvedValue({
      ...CEDAR_ACTIVE,
      live_activation: 'applied',
      grants_expired: true,
    })
    wrap(<CedarOpaView active />)

    expect(await screen.findByText('enforcing here')).toBeInTheDocument()
    expect(
      screen.getByText(/positive grants have degraded to abstain/i),
    ).toBeInTheDocument()
    expect(screen.getByText(/forbid rules stay enforced/i)).toBeInTheDocument()
  })

  // V22 — NON-FIRING DIRECTION for V21: a warning that is always shown warns about
  // nothing. A connected node reports grants_expired:false and must stay quiet.
  it('stays quiet about expiry when the grants have not expired', async () => {
    api.pdpActive.mockResolvedValue({
      ...CEDAR_ACTIVE,
      live_activation: 'applied',
      grants_expired: false,
    })
    wrap(<CedarOpaView active />)

    expect(await screen.findByText('enforcing here')).toBeInTheDocument()
    expect(
      screen.queryByText(/positive grants have degraded to abstain/i),
    ).not.toBeInTheDocument()
  })
})
