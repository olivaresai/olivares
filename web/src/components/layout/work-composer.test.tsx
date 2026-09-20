// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE PLACE TO SAY WHAT TO RUN, and the line that says what it will apply to.
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  isSuperadmin: false,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const listOrgs = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api/endpoints', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/lib/api/endpoints')>()
  return {
    ...real,
    systemApi: { ...real.systemApi, listOrgs },
  }
})

const navigateMock = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigateMock,
  useRouterState: () => '',
}))

const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  createRun: vi.fn(),
  input: vi.fn(),
  inputText: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('@/components/ui/toaster', () => ({ toast: toasts }))

import { WorkComposer } from './work-composer'
import type { RunDTO } from '@/features/agentops/types'

const PROFILE = {
  profile_ref: 'ppf_team',
  driver: 'claude',
  environment_ref: 'env-prod',
  display_name: 'Team account',
  state: 'active' as const,
  local_environment: true,
  operable: true,
}

const WORKSPACE = {
  workspace_ref: 'ws-main',
  name: 'main',
  state: 'active',
  mount_mode: 'ro',
  dlp_mode: 'off',
}

function stubPhone(phone: boolean) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      matches: phone && query.includes('max-width: 639px'),
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
      onchange: null,
    }),
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  stubPhone(false)
  auth.activeTenant = 'tnt-demo'
  auth.isSuperadmin = false
  auth.can = () => true
  listOrgs.mockResolvedValue({ items: [], has_more: false })
  api.listProfiles.mockResolvedValue({ items: [PROFILE], has_more: false })
  api.listWorkspaces.mockResolvedValue({ items: [WORKSPACE], has_more: false })
  api.createRun.mockResolvedValue({ run_ref: 'run-77' })
  api.input.mockResolvedValue({ accepted: true })
  api.inputText.mockResolvedValue({ accepted: true })
})

afterEach(() => {
  stubPhone(false)
})

async function open() {
  const user = userEvent.setup()
  renderIntel(<WorkComposer />)
  await screen.findByTestId('launcher-input')
  return user
}

/** Choose the one profile through its real Radix trigger. */
async function pickProfile(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('launcher-profile'))
  await user.click(await screen.findByRole('option', { name: /Team account/ }))
}

describe('WorkComposer — what it offers, and to whom', () => {
  it('is not rendered at all without the permission that would run it', async () => {
    // An offer that ends in a 403 is a magic pushbutton, which the front door does not
    // offer. The SCOPE LINE stays: knowing what the next action applies to is not a
    // privilege.
    auth.can = (p: string) => p !== 'sessions:run:write'
    renderIntel(<WorkComposer />)
    expect(await screen.findByTestId('work-scope-line')).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(api.listProfiles).not.toHaveBeenCalled()
  })

  it('a read-only operator attached to a run reads the run\u2019s real scope, not "none"', async () => {
    // ⛔ THE REFUSAL IS THE CONTROLS, NOT THE FACTS. The `!canWrite` return used to sit
    //    ABOVE the attached branch, so this principal — permitted to read the session —
    //    was told "Workspace: No workspace · Environment: No environment" about a run
    //    that declares both, while the inspector two panes away named them. That is the
    //    console asserting something nobody sent, which is a heavier defect than a
    //    missing name.
    auth.can = (p: string) => p !== 'sessions:run:write'
    renderIntel(
      <WorkComposer
        attached={{
          run: {
            ...RUNNING,
            workspace_ref: 'ws-main',
            provider_environment_ref: 'xenv_prod',
          },
          group: 'active',
        }}
      />,
    )
    const line = await screen.findByTestId('work-scope-line')
    expect(line).toHaveTextContent('ws-main')
    expect(line).toHaveTextContent('xenv_prod')
    expect(line).not.toHaveTextContent(/No workspace/i)
    expect(line).not.toHaveTextContent(/No environment/i)
    // …and the grant still withholds every control, which is its actual subject.
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(screen.queryByTestId('composer-send')).toBeNull()
    expect(screen.queryByTestId('composer-advanced')).toBeNull()
  })

  it('says WHY there is nothing to type into, and offers the one action', async () => {
    // Never a disabled field with no reason: the engine makes the profile mandatory
    // server-side, so with none registered there is exactly one thing to do.
    api.listProfiles.mockResolvedValue({ items: [], has_more: false })
    const user = userEvent.setup()
    renderIntel(<WorkComposer />)
    expect(
      await screen.findByText(/No provider profile is registered/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    await user.click(screen.getByTestId('launcher-add-provider'))
    expect(navigateMock).toHaveBeenCalledWith(
      expect.objectContaining({ to: '/provider-profiles' }),
    )
  })

  it('will not start without the one field the server cannot default', async () => {
    const user = await open()
    await user.type(screen.getByTestId('launcher-input'), 'nightly index')
    expect(screen.getByTestId('launcher-start')).toBeDisabled()
    await pickProfile(user)
    expect(screen.getByTestId('launcher-start')).toBeEnabled()
  })
})

describe('WorkComposer — starting', () => {
  it('sends the reference only, and moves the address to the new session', async () => {
    const user = await open()
    await user.type(screen.getByTestId('launcher-input'), 'nightly index')
    await pickProfile(user)
    await user.click(screen.getByTestId('launcher-start'))

    await waitFor(() => expect(api.createRun).toHaveBeenCalledTimes(1))
    const body = api.createRun.mock.calls[0][0]
    expect(body.name).toBe('nightly index')
    expect(body.provider_profile_ref).toBe('ppf_team')
    // Only the REFERENCE leaves the browser: the server resolves the homes.
    expect(Object.keys(body)).not.toContain('key')

    // A run whose managed row is not proven yet is addressed BY ITS RUN — the third
    // shape `session-address` resolves — so the link is valid the instant it answers.
    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith(
        expect.objectContaining({
          to: '/sessions',
          search: { session: 'run:run-77' },
        }),
      ),
    )
  })

  it('Enter starts it', async () => {
    const user = await open()
    await pickProfile(user)
    await user.click(screen.getByTestId('launcher-input'))
    await user.keyboard('a{Enter}')
    await waitFor(() => expect(api.createRun).toHaveBeenCalledTimes(1))
  })

  it('Ctrl+Enter starts it and KEEPS the operator here, ready for the next one', async () => {
    // "Start in the background": launching must not block the next launch.
    const user = await open()
    await pickProfile(user)
    const field = screen.getByTestId('launcher-input')
    await user.click(field)
    await user.keyboard('first{Control>}{Enter}{/Control}')

    await waitFor(() => expect(api.createRun).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(field).toHaveValue(''))
    expect(navigateMock).not.toHaveBeenCalled()
    expect(toasts.success).toHaveBeenCalled()
    // The SCOPE is kept, so the next draft starts where the last one did.
    expect(screen.getByTestId('launcher-profile')).toHaveTextContent(
      'Team account',
    )
  })

  it('surfaces the ENGINE’s refusal, not a sentence composed here', async () => {
    api.createRun.mockRejectedValue(
      new Error('budget cap reached for this workspace'),
    )
    const user = await open()
    await pickProfile(user)
    await user.click(screen.getByTestId('launcher-start'))
    await waitFor(() =>
      expect(toasts.error).toHaveBeenCalledWith(
        'budget cap reached for this workspace',
      ),
    )
  })
})

describe('ScopeLine — the scope of the next action', () => {
  // The line names the organization the way the switcher does, from the same
  // source, so the scope of the next action and the organization on screen can
  // never be two different words for one tenant.
  it('names a known organization with the name the switcher uses, not its id', async () => {
    const tenant = '01a0b95a-ee26-724d-9db8-1f5423e25db3'
    auth.activeTenant = tenant
    auth.isSuperadmin = true
    listOrgs.mockResolvedValue({
      items: [
        {
          id: 'org-1',
          tenant_id: tenant,
          name: 'Golden Path',
          slug: 'golden-path',
          status: 'active',
          created_at: '2026-01-01T00:00:00Z',
        },
      ],
      has_more: false,
    })
    renderIntel(<WorkComposer />)
    const line = await screen.findByTestId('work-scope-line')
    await waitFor(() => expect(line).toHaveTextContent('Golden Path'))
    expect(line.textContent).not.toMatch(/01a0b95a/)
  })

  it('falls back to a short identifier when the organization name is not known', async () => {
    const tenant = '01a0b95a-ee26-724d-9db8-1f5423e25db3'
    auth.activeTenant = tenant
    auth.isSuperadmin = true
    listOrgs.mockResolvedValue({ items: [], has_more: false })
    renderIntel(<WorkComposer />)
    const line = await screen.findByTestId('work-scope-line')
    await waitFor(() => expect(line).toHaveTextContent('01a0b95a…'))
    expect(line.textContent).not.toContain(tenant)
  })

  it('does not paint a raw environment reference when the profile is not this node', async () => {
    api.listProfiles.mockResolvedValue({
      items: [
        {
          ...PROFILE,
          local_environment: false,
          environment_ref: 'xenv_01a0b95a-ec91-7014-875a-5732',
        },
      ],
      has_more: false,
    })
    const user = await open()
    await pickProfile(user)
    const line = screen.getByTestId('work-scope-line')
    await waitFor(() => expect(line).toHaveTextContent('another environment'))
    expect(line).not.toHaveTextContent('xenv_01a0b95a-ec91-7014-875a-5732')
    expect(line.getAttribute('title')).toContain(
      'xenv_01a0b95a-ec91-7014-875a-5732',
    )
  })

  it('names the organization, and says plainly when there is no workspace yet', async () => {
    renderIntel(<WorkComposer />)
    const line = await screen.findByTestId('work-scope-line')
    expect(line).toHaveTextContent('tnt-demo')
    expect(line).toHaveTextContent('No workspace')
    // The environment belongs to the PROFILE, so with none chosen there is none to
    // report — and the line says so instead of borrowing a value from the topbar.
    expect(line).toHaveTextContent('Not declared')
  })

  it('shows the environment the CHOSEN profile will run in by name, not by ref', async () => {
    const user = await open()
    await pickProfile(user)
    await waitFor(() =>
      expect(screen.getByTestId('work-scope-line')).toHaveTextContent(
        'This node',
      ),
    )
    expect(
      screen.getByTestId('work-scope-line').getAttribute('title'),
    ).toContain('env-prod')
    expect(screen.getByTestId('work-scope-line').textContent).not.toMatch(
      /env-prod/,
    )
  })
})

describe('WorkComposer — 64 px in both states', () => {
  it('the unattached box is one 64 px column with pickers on the control row', async () => {
    renderIntel(<WorkComposer />)
    const box = await screen.findByTestId('work-composer')
    await screen.findByTestId('launcher-input')
    expect(box.className).toMatch(/\bh-16\b/)
    expect(box.className).toMatch(/\bmax-h-16\b/)
    expect(screen.getByTestId('launcher-input').className).not.toMatch(
      /basis-full/,
    )
    expect(screen.getByTestId('launcher-profile')).toBeInTheDocument()
    expect(screen.getByTestId('launcher-start')).toBeInTheDocument()
    expect(screen.queryByTestId('composer-advanced')).toBeNull()
    expect(screen.getByTestId('work-scope-line')).toBeInTheDocument()
  })

  it('at phone width the unattached composer is one line: input, Advanced, Start', async () => {
    stubPhone(true)
    renderIntel(<WorkComposer />)
    await screen.findByTestId('launcher-input')
    const box = screen.getByTestId('work-composer')
    expect(box.className).toMatch(/\bh-16\b/)
    expect(screen.getByTestId('launcher-input').className).not.toMatch(
      /basis-full/,
    )
    expect(screen.getByTestId('composer-advanced')).toBeInTheDocument()
    expect(screen.getByTestId('launcher-start')).toBeInTheDocument()
    const disclosure = screen
      .getByTestId('composer-advanced')
      .closest('details')
    expect(
      disclosure?.querySelector('[data-testid="launcher-profile"]'),
    ).not.toBeNull()
    expect(
      disclosure?.querySelector('[data-testid="work-scope-line"]'),
    ).not.toBeNull()
  })

  // ⛔ THE PROP IS THE RUN, NOT A STATUS WORD, and that is the one assertion of this
  //    case that had to be rewritten. It arrived as `attached="running"` — a label to
  //    paint. But the composer attaches to the RUN, because a turn is refused with 409
  //    without the work-lease fence stamped on it and the fence is readable nowhere
  //    else. The requirement both rounds share decides it: after a launch the same
  //    composer becomes the session's INPUT, and a status word cannot be an input. The
  //    word is derived from the rail's group instead, so nothing this case measured —
  //    the 64 px box, the status, Advanced, Send, the scope on `title` — is lost.
  it('at phone width the disabled Start still says why it is disabled', async () => {
    // ⛔ ON A PHONE THE REASON IS BEHIND A DISCLOSURE, so the verb has to carry it. On a
    //    desktop the empty profile pill sits beside Start and says "Choose a profile" in
    //    place; at 390 that pill moved into `Advanced`, leaving a greyed verb with
    //    nothing on screen explaining it — the disabled control with no reason this
    //    component exists to replace.
    stubPhone(true)
    const user = await open()
    const start = screen.getByTestId('launcher-start')
    expect(start).toBeDisabled()
    expect(start).toHaveAttribute('title', 'Choose a profile')
    // …and once the reason is gone, so is the sentence.
    await user.click(screen.getByTestId('composer-advanced'))
    await user.click(screen.getByTestId('launcher-profile'))
    await user.click(
      await screen.findByRole('option', { name: /Team account/ }),
    )
    expect(screen.getByTestId('launcher-start')).toBeEnabled()
    expect(screen.getByTestId('launcher-start')).not.toHaveAttribute('title')
  })

  it('a blocked sentence gives way at its end instead of growing the box', async () => {
    // The box states its height, so a sentence that wraps is not a taller composer: it
    // is a sentence CLIPPED by the pane. `readFailed` is 108 characters in German.
    api.listProfiles.mockRejectedValue(new Error('boom'))
    renderIntel(<WorkComposer />)
    const blocked = await screen.findByTestId('launcher-blocked')
    expect(blocked.className).toMatch(/\btruncate\b/)
    expect(blocked.getAttribute('title')).toBe(blocked.textContent)
  })

  it('attached RUNNING keeps status, Advanced and scope inside the same 64 px box', async () => {
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const box = await screen.findByTestId('work-composer')
    await screen.findByTestId('composer-send')
    expect(box).toHaveAttribute('data-attached', 'true')
    expect(box.className).toMatch(/\bh-16\b/)
    expect(box.className).toMatch(/\bmax-h-16\b/)
    expect(screen.getByTestId('composer-session-state')).toHaveTextContent(
      'Running',
    )
    expect(screen.getByTestId('composer-advanced')).toBeInTheDocument()
    expect(screen.getByTestId('composer-send')).toBeInTheDocument()
    expect(box.getAttribute('title') ?? '').toMatch(/Organization|Workspace/)
    expect(
      screen.getByTestId('composer-session-state').getAttribute('title'),
    ).toMatch(/Running/)
  })

  it('Advanced is a native details/summary; Escape closes it and returns focus to the summary', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const summary = await screen.findByTestId('composer-advanced')
    expect(summary.tagName).toBe('SUMMARY')
    const disclosure = summary.closest('details')
    expect(disclosure).not.toBeNull()
    // Native details already expose open/closed to assistive technology; the
    // summary must not carry a second, custom expanded state.
    expect(summary).not.toHaveAttribute('aria-expanded')
    await user.click(summary)
    expect(disclosure).toHaveAttribute('open')
    expect(screen.getByTestId('composer-wire-input')).toBeVisible()
    await user.keyboard('{Escape}')
    expect(disclosure).not.toHaveAttribute('open')
    expect(summary).toHaveFocus()
  })

  // ONE ESCAPE, ONE LAYER. At phone width the two pickers live INSIDE this disclosure,
  // and a picker is a portalled listbox that answers Escape itself. The disclosure sees
  // the same key — the portal keeps the React tree — so an operator who opened the
  // profile list and changed their mind lost the list AND the panel around it in one
  // press, and had to reopen the panel to try again. The console's own rule for a
  // stack is the opposite: close the list, then the thing under it.
  it('an Escape spoken for by an open picker leaves the disclosure open', async () => {
    stubPhone(true)
    const user = await open()
    const summary = screen.getByTestId('composer-advanced')
    await user.click(summary)
    const disclosure = summary.closest('details')
    expect(disclosure).toHaveAttribute('open')

    await user.click(screen.getByTestId('launcher-profile'))
    await screen.findByRole('option', { name: /Team account/ })
    await user.keyboard('{Escape}')

    await waitFor(() =>
      expect(
        screen.queryByRole('option', { name: /Team account/ }),
      ).not.toBeInTheDocument(),
    )
    expect(disclosure).toHaveAttribute('open')
  })
})

describe('WorkComposer — the three ways to have nothing to type into', () => {
  it('with no organization, says which one to choose first', async () => {
    auth.activeTenant = null
    renderIntel(<WorkComposer />)
    expect(
      await screen.findByText(/Select an organization/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(api.listProfiles).not.toHaveBeenCalled()
  })

  it('with no permission to READ profiles, says that, and offers no door it cannot open', async () => {
    // This principal may start a run and may not choose the one field the server
    // cannot default. Offering "add a provider profile" would send them to a screen
    // they may not read either.
    auth.can = (p: string) => p !== 'sessions:profile:read'
    renderIntel(<WorkComposer />)
    expect(
      await screen.findByText(/does not include permission to read them/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(screen.queryByTestId('launcher-add-provider')).toBeNull()
  })

  it('keeps the scope line in every one of them', async () => {
    auth.activeTenant = null
    renderIntel(<WorkComposer />)
    expect(await screen.findByTestId('work-scope-line')).toBeInTheDocument()
  })
})

describe('WorkComposer — it does not offer a control it is about to remove', () => {
  it('renders NO field while the provider-profile plane is still answering', async () => {
    // Measured by the live spec, not imagined: the first version treated "not answered
    // yet" as "there are some", so a deployment with none painted a usable field and
    // replaced it a moment later. The browser caught it as a focus() on an element that
    // had just been removed.
    let settle: (value: {
      items: never[]
      has_more: boolean
    }) => void = () => {}
    api.listProfiles.mockReturnValue(
      new Promise((resolve) => {
        settle = resolve
      }),
    )
    renderIntel(<WorkComposer />)
    await screen.findByTestId('work-scope-line')
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    expect(screen.queryByTestId('launcher-add-provider')).toBeNull()

    settle({ items: [], has_more: false })
    // …and once it HAS answered, the honest state appears.
    expect(
      await screen.findByTestId('launcher-add-provider'),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-input')).toBeNull()
    const truncated = [
      ...screen.getByTestId('work-composer').querySelectorAll('.truncate'),
    ]
    expect(truncated.length).toBeGreaterThan(0)
    for (const el of truncated) {
      expect(el.getAttribute('title')).toBeTruthy()
    }
  })

  it('says a FAILED read failed, rather than calling it an empty plane', async () => {
    api.listProfiles.mockRejectedValue(new Error('boom'))
    renderIntel(<WorkComposer />)
    expect(await screen.findByText(/could not be read/i)).toBeInTheDocument()
    expect(screen.queryByTestId('launcher-add-provider')).toBeNull()
  })
})

const RUNNING: RunDTO = {
  run_ref: 'run_live',
  name: 'nightly',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'claude',
  provider_profile_ref: 'ppf_team',
}

describe('WorkComposer — attached to the session it started', () => {
  it('sends a sentence as a user frame on a Claude run', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const field = await screen.findByTestId('launcher-input')
    expect(screen.getByTestId('composer-session-state')).toHaveTextContent(
      'Running',
    )
    await user.type(field, 'Reply with the single word OK')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    expect(api.input).toHaveBeenCalledWith(
      'run_live',
      JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'Reply with the single word OK' },
      }),
      // No work stamp on this run, so no fence: the argument is named rather than
      // left out, because `toHaveBeenCalledWith` is exact about arity and a silent
      // third argument is how one would appear here unnoticed.
      undefined,
    )
    expect(api.inputText).not.toHaveBeenCalled()
  })

  it('sends a Codex run as {text}', async () => {
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, provider_driver: 'codex' },
          group: 'active',
        }}
      />,
    )
    await user.type(await screen.findByTestId('launcher-input'), 'hello')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.inputText).toHaveBeenCalledTimes(1))
    expect(api.inputText).toHaveBeenCalledWith('run_live', 'hello', undefined)
  })

  // ⛔ A WORK-BOUND SESSION REFUSES AN UNFENCED TURN, and this composer is the only
  //    place most operators will ever type one. Without the fence the engine answers
  //    409 before the child sees a byte (`refuseLegacyControlUnderWork`), so the
  //    session the composer started could not be spoken to from the composer.
  it('carries the run work-lease fence on a sentence, as the CLI does', async () => {
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          run: {
            ...RUNNING,
            provider_driver: 'codex',
            work_item_id: 'work-a',
            work_lease_fence: 7,
            work_owner_epoch: 2,
            work_dispatch_key: 'dispatch-a',
          },
          group: 'active',
        }}
      />,
    )
    await user.type(await screen.findByTestId('launcher-input'), 'continue')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.inputText).toHaveBeenCalledTimes(1))
    expect(api.inputText).toHaveBeenCalledWith('run_live', 'continue', 7)
  })

  it('carries the fence on a wrapped user frame too', async () => {
    const user = userEvent.setup()
    renderIntel(
      <WorkComposer
        attached={{
          run: { ...RUNNING, work_item_id: 'work-a', work_lease_fence: 7 },
          group: 'active',
        }}
      />,
    )
    await user.type(await screen.findByTestId('launcher-input'), 'continue')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    expect(api.input).toHaveBeenCalledWith(
      'run_live',
      JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'continue' },
      }),
      7,
    )
  })

  // The mirror mistake: a fence sent on a run that has none is a 400 from the engine.
  it('sends no fence for a run with no work stamp', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await user.type(await screen.findByTestId('launcher-input'), 'continue')
    await user.click(screen.getByTestId('composer-send'))
    await waitFor(() => expect(api.input).toHaveBeenCalledTimes(1))
    expect(api.input.mock.calls[0][2]).toBeUndefined()
  })

  // ⛔ THE PANEL IS ANCHORED TO THE BOX, NOT TO ITS SUMMARY, and the browser is what
  //    said so: docked in the session pane and hung off the summary with `right-0`, the
  //    open panel grew to its content's 467 px and its left edge landed 38 px outside a
  //    pane that is `overflow-hidden` — the hint and the scope line were cut mid-word.
  //    jsdom has no layout, so what is pinned here is the contract that made it fit: the
  //    positioned ancestor is the 64 px box and the panel spans it.
  it('anchors the advanced panel to the box, so a clipping pane cannot cut it', async () => {
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    const box = await screen.findByTestId('work-composer')
    expect(box.className).toMatch(/\brelative\b/)
    const disclosure = screen
      .getByTestId('composer-advanced')
      .closest('details') as HTMLElement
    expect(disclosure.className).not.toMatch(/\brelative\b/)
    const panel = disclosure.querySelector('div') as HTMLElement
    expect(panel.className).toMatch(/\babsolute\b/)
    expect(panel.className).toMatch(/\binset-x-0\b/)
    // A minimum width is what let it outgrow its pane; the box's width is the cap.
    expect(panel.className).not.toMatch(/min-w-\[/)
  })

  it('keeps NDJSON behind the advanced disclosure', async () => {
    renderIntel(<WorkComposer attached={{ run: RUNNING, group: 'active' }} />)
    await screen.findByTestId('launcher-input')
    expect(screen.getByTestId('launcher-input')).toHaveAttribute(
      'placeholder',
      'Send a sentence…',
    )
    expect(screen.getByTestId('composer-advanced')).toBeInTheDocument()
    expect(
      screen.getByTestId('composer-wire-input').getAttribute('placeholder'),
    ).toMatch(/NDJSON/i)
  })
})
