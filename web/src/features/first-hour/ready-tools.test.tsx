// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Which tools can start a session, and the one sentence for one that cannot: what Now,
// the setup steps and the New session form read (useReadyTools). One case per state the
// engine can report for a tool. Pinned on main against the browser's own readiness
// (previews, provider pages, sign-in status); the assertions did not change when the
// answer moved into the engine (ARCH.C3).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { agentOpsApi } from '@/features/agentops/api'
import { useTenantStore } from '@/stores/tenant'
import { SESSION_TOOLS, type SessionTool } from './api'
import { useReadyTools } from './first-hour'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    isSuperadmin: true,
    can: () => true,
    principal: { user_id: 'u1', aal: 1 },
    activeTenant: 'tnt-a',
  }),
}))

type Key = {
  provider_ref: string
  kind: string
  display_name: string
  default_model?: string
  models?: string[]
  refused?: boolean
}
type State =
  | { own_login: true }
  | { key: Key; model_required?: boolean }
  | { refused: { status: number; code: string; message: string } }

/** The engine's answer for every tool: what a session would run on, or why not. */
function given(states: Record<SessionTool, State>) {
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue({
    tools: SESSION_TOOLS.map((driver) => {
      const s = states[driver]
      if ('refused' in s)
        return {
          driver,
          ready: false,
          code: s.refused.code,
          message: s.refused.message,
        }
      if ('own_login' in s) return { driver, ready: true, reason: 'own_login' }
      return {
        driver,
        ready: !s.key.refused,
        reason: 'api_key',
        provider: {
          provider_ref: s.key.provider_ref,
          kind: s.key.kind,
          display_name: s.key.display_name,
          default_model: s.key.default_model,
          models: s.key.models,
        },
        model_required: s.model_required,
        ...(s.key.refused
          ? { code: 'key_refused', message: 'the engine sentence' }
          : {}),
      }
    }),
  })
}

function Probe() {
  const { ready, refusal, boundKey, isLoading } = useReadyTools()
  if (isLoading) return <p>deciding</p>
  return (
    <ul>
      <li>{`ready=[${ready.join(',')}]`}</li>
      {SESSION_TOOLS.map((d) => {
        const key = boundKey(d)
        return (
          <li key={d}>
            {`${d}: ${refusal(d) ?? '-'}` +
              (key
                ? ` | key=${key.record.provider_ref} default=${key.record.default_model ?? ''} models=${(key.record.models ?? []).join('+')} required=${key.modelRequired}`
                : '')}
          </li>
        )
      })}
    </ul>
  )
}

beforeEach(() => useTenantStore.setState({ activeTenant: 'tnt-a' }))
afterEach(() => vi.restoreAllMocks())

describe('which tools can start a session, and why not', () => {
  it('a login, a tested key, a refused key and nothing to run on', async () => {
    given({
      'gemini-cli': {
        refused: {
          status: 409,
          code: 'nothing_to_run_on',
          message: 'Gemini CLI has nothing to run on yet.',
        },
      },
      claude: { own_login: true },
      codex: {
        key: {
          provider_ref: 'prv-openai',
          kind: 'openai',
          display_name: 'OpenAI main',
          default_model: 'gpt-5',
          models: ['gpt-5', 'gpt-5-mini'],
        },
        model_required: true,
      },
      grok: {
        key: {
          provider_ref: 'prv-xai',
          kind: 'xai',
          display_name: 'xAI main',
          refused: true,
        },
      },
      opencode: {
        refused: {
          status: 409,
          code: 'nothing_to_run_on',
          message:
            'OpenCode has nothing to run on yet. Add a key or a local model (Ollama) in Providers.',
        },
      },
    })
    renderIntel(<Probe />)
    expect(await screen.findByText('ready=[claude,codex]')).toBeInTheDocument()
    expect(screen.getByText('claude: -')).toBeInTheDocument()
    expect(
      screen.getByText(
        'codex: - | key=prv-openai default=gpt-5 models=gpt-5+gpt-5-mini required=true',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'grok: The API key xAI main was refused. Replace it under API keys. | key=prv-xai default= models= required=false',
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'opencode: OpenCode has nothing to run on yet. Add a key or a local model (Ollama) in Providers.',
      ),
    ).toBeInTheDocument()
  })

  it('a tool whose sign-in the node cannot read says so', async () => {
    given({
      'gemini-cli': {
        refused: {
          status: 409,
          code: 'nothing_to_run_on',
          message: 'Gemini CLI has nothing to run on yet.',
        },
      },
      claude: {
        refused: {
          status: 503,
          code: 'tool_signin_unreadable',
          message:
            'the sign-in status of Claude Code could not be read on this node',
        },
      },
      codex: { own_login: true },
      grok: { own_login: true },
      opencode: { own_login: true },
    })
    renderIntel(<Probe />)
    expect(
      await screen.findByText('ready=[codex,grok,opencode]'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'claude: the sign-in status of Claude Code could not be read on this node',
      ),
    ).toBeInTheDocument()
  })
})
