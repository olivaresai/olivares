// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// the NIS 2 incident surface, tested against the CONTRACT THE ENGINE
// ACTUALLY ENFORCES rather than against a double that agrees with the console.
//
// WHY THIS FILE DOES NOT MOCK `./api`. The sibling surface got that wrong and the
// cost is measured: `capabilities.test.tsx:189-192` is GREEN while both
// tool-pinning writes answer 400 in production, because the test's double accepts
// the body the engine rejects (toolpins_evidence_test.go:166-191). A cell that
// asserts "the mutationFn was called" has verified the console agrees with itself.
//
// So every request here goes through the REAL client (lib/api/client.ts) into a
// stubbed `fetch`, and the assertions are on the REQUEST THE CLIENT HANDS TO FETCH:
// method, URL, query string, Content-Type and the exact body string. Each one is
// pinned to the line of Go that enforces it, so it fails when either side moves.
//
// ⚠ AND THAT IS WHERE THE OBSERVATION STOPS — the Codex sol max contrast was right
// to narrow the claim (F5). This is the pre-fetch RequestInit, not the bytes on the
// socket and not the bytes Go reads: no browser encodes the body here and no handler
// parses it. What is proved is exact forwarding from React state to `fetch`. The
// end-to-end byte identity needs a composed binary and a browser, and this session
// ran neither -- it is declared in the session file, not implied away here.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DEFAULT_AUTH,
  fireEvent,
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
} from '@/test/intel'
import '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { complianceApi, confirmedDeleted, isOpenCoreSeam } from './api'
import {
  isKnownNis2Phase,
  NIS2_MAX_REFERENCE_RUNES,
  NIS2_PHASES,
  nis2PhasesAfter,
  nis2ReferenceTooLong,
} from './types'

// The auth mock is per-file and hoisted, so RBAC is exercised by rendering the tab
// with explicit canAdmin/canRead props (what the container derives from `can`).
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH }),
}))

const toastSpy = {
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
  info: vi.fn(),
}
vi.mock('@/components/ui/toaster', () => ({ toast: toastSpy }))

const { Nis2Tab } = await import('./nis2-view')
await import('./i18n')

/** The operator's impact document, as it is typed into the textarea. */
const IMPACT = JSON.stringify({
  awareness_at: '2026-06-04T12:00:00Z',
  affected_services: ['checkout'],
  users_affected: 12000,
  operational_disruption: 'severe',
})

const INCIDENT = {
  id: 'ni-1',
  reference: 'INC-001',
  significant: true,
  provisional: true,
  cross_border: true,
  suspected_crime: false,
  criteria_met: ['23(3)(a)', '23(3)(b)'],
  rationale: 'severe operational disruption AND persons affected',
  phase: 'early_warning',
  doc_sha256: 'a'.repeat(64),
  classified_by: 'dpo@example.com',
  classified_at: '2026-06-04T13:00:00Z',
  disclaimer:
    'Provisional NIS 2 Directive significant-incident classification. The verdict is DECISION SUPPORT, not the legal classification.',
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** Stub `fetch` and hand back the mock so a test can read the request it built. */
function stubFetch(
  handler: (url: string, init: RequestInit) => Response | Promise<Response>,
) {
  const mock = vi.fn((url: unknown, init: unknown) =>
    Promise.resolve(handler(String(url), (init ?? {}) as RequestInit)),
  )
  vi.stubGlobal('fetch', mock)
  return mock
}

/** The request the client actually built, parsed. */
function sentRequest(mock: ReturnType<typeof stubFetch>, call = 0) {
  const [url, init] = mock.mock.calls[call] as [string, RequestInit]
  const parsed = new URL(url, 'https://console.invalid')
  return {
    path: parsed.pathname,
    query: parsed.searchParams,
    method: init.method,
    body: init.body,
    headers: init.headers as Headers,
  }
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  Object.values(toastSpy).forEach((fn) => fn.mockReset())
})

// --- the wire contract of classify -------------------------------------------

describe('classifyNis2Incident — the request the engine actually parses', () => {
  it('puts reference and finding_id in the QUERY STRING, never in the body', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyNis2Incident('INC-001', IMPACT, 'find-9')

    const req = sentRequest(mock)
    // nis2incident.go:127 reads r.URL.Query().Get("reference") and answers 400
    // "reference is required" when it is absent; :136 reads finding_id the same way.
    expect(req.method).toBe('POST')
    expect(req.path).toBe('/v1/m/compliance/nis2/incidents/classify')
    expect(req.query.get('reference')).toBe('INC-001')
    expect(req.query.get('finding_id')).toBe('find-9')
    // A reference smuggled into the body would be invisible to the handler.
    expect(String(req.body)).not.toContain('INC-001')
  })

  it('omits finding_id entirely when the operator left it blank', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyNis2Incident('INC-001', IMPACT)

    // Absent, not an empty string: the handler clamps and stores whatever arrives
    // (nis2incident.go:136), so "" would persist as an empty linked finding.
    expect(sentRequest(mock).query.has('finding_id')).toBe(false)
  })

  it('sends the impact document VERBATIM — the bytes that get hashed', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyNis2Incident('INC-001', IMPACT)

    const req = sentRequest(mock)
    // readBoundedBody (oscalprofile.go:475) takes the body as raw bytes and
    // nis2incident.go:143 hashes exactly those into doc_sha256, then hands them to
    // the add-on to parse. Byte-identical or the anchor attests something else.
    expect(req.body).toBe(IMPACT)
    expect(req.headers.get('Content-Type')).toBe('application/json')
  })

  it('does NOT double-encode it — the defect this shape exists to avoid', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT, 201))

    await complianceApi.classifyNis2Incident('INC-001', IMPACT)

    // THE NEGATIVE CONTROL, and it is the whole reason `rawBody` is used here.
    // Passing the document as the `body` argument runs it through JSON.stringify
    // (client.ts:139), so a STRING comes back out re-quoted and escaped: the engine
    // would hash `"{\"awareness_at\":…}"` — a different document — and hand the
    // classifier a JSON string where it expects the impact object.
    //
    // This assertion is what tells the two shapes apart. It fails the moment the
    // call reverts to `http.post(path, impact, …)`, which is how the DORA sibling
    // one entry above was found broken.
    expect(sentRequest(mock).body).not.toBe(JSON.stringify(IMPACT))
    expect(String(sentRequest(mock).body).startsWith('"')).toBe(false)
  })

  it('classifyIncident (DORA) carries the same fix — same handler shape', async () => {
    const mock = stubFetch(() => jsonResponse({ id: 'di-1' }, 201))

    await complianceApi.classifyIncident('DORA-1', IMPACT)

    const req = sentRequest(mock)
    expect(req.query.get('reference')).toBe('DORA-1')
    expect(req.body).toBe(IMPACT)
    expect(req.body).not.toBe(JSON.stringify(IMPACT))
  })

  it('surfaces the engine 501 as an ApiError the seam test recognises', async () => {
    stubFetch(() =>
      jsonResponse(
        {
          error: {
            message:
              'NIS 2 significant-incident classification requires the Olivares enterprise add-on (nis2incident); not linked in this build',
          },
        },
        501,
      ),
    )

    await expect(
      complianceApi.classifyNis2Incident('INC-001', IMPACT),
    ).rejects.toBeInstanceOf(ApiError)
  })
})

// --- the wire contract of the phase update ------------------------------------

describe('updateNis2Incident — a body the engine will not reject', () => {
  it('sends ONLY phase and note', async () => {
    const mock = stubFetch(() =>
      jsonResponse({ ...INCIDENT, phase: 'notification' }),
    )

    await complianceApi.updateNis2Incident('ni-1', {
      phase: 'notification',
      note: 'CSIRT notified',
    })

    const req = sentRequest(mock)
    expect(req.method).toBe('PUT')
    expect(req.path).toBe('/v1/m/compliance/nis2/incidents/ni-1')
    // The handler decodes into struct{Phase, Note string} (nis2incident.go:270-273)
    // through decodeJSON, which sets DisallowUnknownFields (helpers.go:99). ANY
    // other key is 400 "invalid JSON body" — not an ignored field. So the set of
    // keys on the wire is the assertion, not just their values.
    expect(Object.keys(JSON.parse(String(req.body))).sort()).toEqual([
      'note',
      'phase',
    ])
  })

  it('drops an empty note instead of sending a key with no value', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT))

    await complianceApi.updateNis2Incident('ni-1', { phase: 'final' })

    expect(Object.keys(JSON.parse(String(sentRequest(mock).body)))).toEqual([
      'phase',
    ])
  })

  it('percent-encodes the id into the path', async () => {
    const mock = stubFetch(() => jsonResponse(INCIDENT))

    await complianceApi.updateNis2Incident('ni/1', { phase: 'final' })

    expect(sentRequest(mock).path).toBe(
      '/v1/m/compliance/nis2/incidents/ni%2F1',
    )
  })
})

// --- the export: the auditor's bytes ------------------------------------------

describe('exportNis2Incident — the server bytes, and the auth to get them', () => {
  it('GETs the export route with auth and tenant, and keeps the bytes intact', async () => {
    useSessionStore.setState({ token: 'tok' } as never)
    useTenantStore.setState({ activeTenant: 'acme' } as never)
    // PRETTY-PRINTED ON PURPOSE. The first version of this fixture was
    // `{"id":"ni-1","ledger_anchor":{"seq":12}}` — already exactly what
    // JSON.stringify emits — so the mutant that replaced `text` with
    // `JSON.stringify(JSON.parse(text))` SURVIVED: the assertion could not tell a
    // byte-exact passthrough from a parse-and-reserialize round trip. Indentation
    // and spacing are the difference the claim is about.
    const body = '{\n  "id": "ni-1",\n  "ledger_anchor": { "seq": 12 }\n}\n'
    const mock = stubFetch(
      () =>
        new Response(body, {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
    )

    const res = await complianceApi.exportNis2Incident('ni-1')

    const req = sentRequest(mock)
    expect(req.method).toBe('GET')
    expect(req.path).toBe('/v1/m/compliance/nis2/incidents/ni-1/export')
    // This route self-audits in the caller's transaction (nis2incident.go:346), so
    // it has to arrive as the real principal or the ledger entry names nobody.
    expect(req.headers.get('Authorization')).toBe('Bearer tok')
    expect(req.headers.get('X-Olivares-Tenant')).toBe('acme')
    // Byte-exact: what an auditor is handed is what the server sealed, never a
    // parse-and-reserialize round trip.
    expect(res.text).toBe(body)
    expect(res.filename).toBe('nis2-incident-ni-1.json')
  })

  it('raises the engine envelope instead of writing a half file', async () => {
    stubFetch(() => jsonResponse({ error: { message: 'not found' } }, 404))

    await expect(
      complianceApi.exportNis2Incident('ni-x'),
    ).rejects.toBeInstanceOf(ApiError)
  })
})

// --- forward-only phases: the console side of a 409 ---------------------------

describe('nis2PhasesAfter — the engine rule, before the request', () => {
  it('offers only the phases ahead of the current one', () => {
    expect(nis2PhasesAfter('early_warning')).toEqual([
      'notification',
      'intermediate',
      'final',
    ])
    expect(nis2PhasesAfter('intermediate')).toEqual(['final'])
  })

  it('offers nothing past the last phase — no move that could 409', () => {
    // nis2incident.go:295 answers 409 when the target ordinal is <= the current
    // one, and `final` is the last (nis2seam.go:99-101).
    expect(nis2PhasesAfter('final')).toEqual([])
  })

  it('never offers the current phase or an earlier one', () => {
    for (const [i, phase] of NIS2_PHASES.entries()) {
      const offered = nis2PhasesAfter(phase)
      expect(offered).not.toContain(phase)
      expect(offered).toEqual([...NIS2_PHASES].slice(i + 1))
    }
  })

  it('offers NOTHING for a phase this build cannot order', () => {
    // This cell previously required the OPPOSITE — the whole vocabulary — on the
    // reasoning that the engine rejects a bad move anyway. The Codex sol max
    // contrast refuted both halves (F6/F8) and the cell was codifying the defect:
    //
    //   a value from a NEWER vocabulary is ahead of everything this build knows, so
    //   every option offered was backwards, i.e. a guaranteed 409;
    //
    //   and the engine does NOT catch it — nis2PhaseIndex returns -1 for an unknown
    //   current phase (nis2seam.go:113-120), so `-1 < any` and the forward-only
    //   check at nis2incident.go:295 passes for every target, on a column that is
    //   unconstrained text (schema.go:982-998).
    //
    // Both sides fail open, so "the engine is the authority" was not a fallback.
    expect(nis2PhasesAfter('post_mortem')).toEqual([])
    expect(isKnownNis2Phase('post_mortem')).toBe(false)
    // …and the predicate still recognises the real vocabulary, or "unknown" would
    // swallow every phase and the action would vanish everywhere.
    for (const p of NIS2_PHASES) expect(isKnownNis2Phase(p)).toBe(true)
  })
})

// --- the reference guard: the same unit the engine counts ---------------------

describe('nis2ReferenceTooLong — runes, because the engine counts runes', () => {
  it('accepts the boundary and refuses one past it', () => {
    expect(nis2ReferenceTooLong('a'.repeat(NIS2_MAX_REFERENCE_RUNES))).toBe(
      false,
    )
    expect(nis2ReferenceTooLong('a'.repeat(NIS2_MAX_REFERENCE_RUNES + 1))).toBe(
      true,
    )
    expect(nis2ReferenceTooLong('')).toBe(false)
  })

  it('does not refuse an astral reference the engine would accept', () => {
    // 600 emoji: 600 runes to Go's len([]rune(s)) (helpers.go:212-214), but 1200
    // to JavaScript's `.length`, which counts UTF-16 code units. Counting the
    // wrong unit blocks a reference the engine takes — and the operator cannot
    // tell why, because the console never sent it.
    const astral = '🛡'.repeat(600)
    expect(astral.length).toBe(1200)
    expect([...astral].length).toBe(600)
    expect(nis2ReferenceTooLong(astral)).toBe(false)
  })

  it('still refuses an astral reference that is genuinely too long', () => {
    // The direction of non-firing: a guard that accepts everything passes any
    // "accepts" test. This one must still say yes.
    expect(nis2ReferenceTooLong('🛡'.repeat(NIS2_MAX_REFERENCE_RUNES + 1))).toBe(
      true,
    )
  })
})

// --- the two allowlists --------------------------------------------------------

describe('isOpenCoreSeam — by STATUS, not by prose', () => {
  it('recognises 501 as the add-on boundary', () => {
    expect(isOpenCoreSeam(new ApiError(501, 'x', 'not linked'))).toBe(true)
  })

  it('does NOT call a 500 a boundary just because it says "not implemented"', () => {
    // Matching the message is how a real fault gets drawn as a calm add-on notice.
    expect(
      isOpenCoreSeam(new ApiError(500, 'x', 'handler not implemented')),
    ).toBe(false)
    expect(isOpenCoreSeam(new ApiError(403, 'x', 'nope'))).toBe(false)
    expect(isOpenCoreSeam(new Error('501'))).toBe(false)
  })
})

describe('confirmedDeleted — a 2xx is not evidence a classification is gone', () => {
  it('accepts the engine answer', () => {
    // nis2incident.go:403 writes {"deleted": true} with 200.
    expect(confirmedDeleted({ status: 200, data: { deleted: true } })).toBe(
      true,
    )
  })

  it('refuses a 200 that does not say it deleted anything', () => {
    expect(confirmedDeleted({ status: 200, data: {} })).toBe(false)
    expect(confirmedDeleted({ status: 200, data: { deleted: 'true' } })).toBe(
      false,
    )
    expect(confirmedDeleted({ status: 200, data: null })).toBe(false)
  })

  it('refuses a 202 — accepted is not done', () => {
    expect(confirmedDeleted({ status: 202, data: { deleted: true } })).toBe(
      false,
    )
  })

  it('refuses a bodyless 204 — no body is no word', () => {
    // THE MUTANT THAT SURVIVED. An earlier version returned true for any 204, which
    // contradicted the rule one line above it: the engine's word is the evidence.
    // Codex named it (F7) and I removed the branch — but removing a branch without
    // pinning its absence leaves it one careless edit from coming back, and the
    // re-introduction mutant compiled and passed all 35 cells until this one existed.
    // Unreachable on today's route (it answers 200, nis2incident.go:403); that is
    // exactly why nothing else notices.
    expect(confirmedDeleted({ status: 204, data: undefined })).toBe(false)
    expect(confirmedDeleted({ status: 204, data: { deleted: true } })).toBe(
      false,
    )
  })
})

// --- the view -------------------------------------------------------------------

/** Route the tab's reads; every other call falls through to a 404 so an
 *  unexpected request is a failure rather than a silent empty render. */
function stubTab(overrides: Record<string, () => Response> = {}) {
  return stubFetch((url, init) => {
    const path = new URL(url, 'https://console.invalid').pathname
    const key = `${init.method ?? 'GET'} ${path}`
    if (overrides[key]) return overrides[key]()
    if (key === 'GET /v1/m/compliance/nis2/incidents')
      return jsonResponse({ items: [INCIDENT] })
    return jsonResponse({ error: { message: `unrouted ${key}` } }, 404)
  })
}

describe('Nis2Tab — what the screen claims', () => {
  it('renders the verdict as PROVISIONAL decision support, with the disclaimer', async () => {
    stubTab()
    renderIntel(<Nis2Tab canAdmin canRead />)

    expect(await screen.findByText('INC-001')).toBeInTheDocument()
    // `significant` starts the Art 23(4) clock; it is shown as a trigger, and the
    // engine's own provisional flag (hardcoded true, nis2incident.go:78) rides it.
    // The badge describes WHAT THE CLASSIFIER DID WITH THE SUPPLIED DATA. It used
    // to read "Significant — reporting triggered", which announces the entity's
    // legal duty — the exact conclusion the engine's own disclaimer reserves for a
    // competent person (nis2incident.go:33-40). Codex F9 caught it in the English
    // source, and five locales had strengthened it further into an obligation.
    expect(
      screen.getByText(/Meets Art 23\(3\) criteria — attest before reporting/i),
    ).toBeInTheDocument()
    expect(document.body.textContent ?? '').not.toMatch(/reporting triggered/i)
    expect(screen.getByText('Provisional')).toBeInTheDocument()
    // TWO distinct statements, anchored on text only one of them contains — the
    // tab-level rule and the engine's OWN per-row disclaimer, rendered verbatim.
    // A single /DECISION SUPPORT/ query matches both and would pass with either
    // one deleted.
    expect(screen.getByText(/duty to notify the CSIRT/i)).toBeInTheDocument()
    expect(screen.getByText(INCIDENT.disclaimer)).toBeInTheDocument()
    // It is never a legal finding, and never a compliance claim.
    expect(document.body.textContent ?? '').not.toMatch(/compliant|certified/i)
  })

  it('hides every write from a reader who cannot administer', async () => {
    stubTab()
    renderIntel(<Nis2Tab canAdmin={false} canRead />)

    expect(await screen.findByText('INC-001')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /Classify incident/i }),
    ).toBeNull()
    expect(screen.queryByRole('button', { name: /Advance phase/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Delete$/i })).toBeNull()
    // The reads stay: this plane is open-core on the read side.
    expect(screen.getByRole('button', { name: /Export/i })).toBeInTheDocument()
  })

  it('says so plainly when the caller cannot read the plane at all', () => {
    stubTab()
    renderIntel(<Nis2Tab canAdmin={false} canRead={false} />)

    expect(
      screen.getByText(/do not have permission to read NIS 2/i),
    ).toBeInTheDocument()
  })

  it('draws a 501 classify as the add-on boundary, not as a failure', async () => {
    const user = userEvent.setup()
    stubTab({
      'POST /v1/m/compliance/nis2/incidents/classify': () =>
        jsonResponse(
          { error: { message: 'requires the Olivares enterprise add-on' } },
          501,
        ),
    })
    renderIntel(<Nis2Tab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Classify incident/i }),
    )
    await user.type(screen.getByLabelText(/Incident reference/i), 'INC-002')
    await user.type(screen.getByLabelText(/Impact document/i), '{{"x":1}')
    await user.click(screen.getByRole('button', { name: /^Classify$/i }))

    // The calm, in-place explanation — and the dialog STAYS OPEN, so the impact
    // document the operator typed is not thrown away by a boundary.
    expect(
      await screen.findByText(/not linked in this build/i),
    ).toBeInTheDocument()
    expect(toastSpy.success).not.toHaveBeenCalled()
    // AND NO RED TOAST. This assertion is the one that was missing, and the defect
    // it now pins was mine: the dialog first ran through usePrivilegedMutation,
    // which raises the generic error toast for every non-403 rejection
    // (use-privileged-mutation.ts:62-77). The calm in-place explanation rendered
    // correctly the whole time — with "Something went wrong" sitting next to it.
    // A cell that only checks the notice appeared cannot see that.
    expect(toastSpy.error).not.toHaveBeenCalled()
    expect(toastSpy.warning).not.toHaveBeenCalled()
    // The dialog stays open, so the impact document is not thrown away.
    expect(screen.getByLabelText(/Impact document/i)).toBeInTheDocument()
  })

  it('relays the add-on refusal on a 403 instead of "not authorized"', async () => {
    const user = userEvent.setup()
    // writeStoreError maps license.ErrAddonRequiresLicense to 403 carrying this
    // exact shape of message (helpers.go:64-76, addonRefusalMessage :279-291). It is
    // a COMMERCIAL boundary, not a role problem — and the role problem is already
    // prevented by the canAdmin gate, so a generic "not authorized" here would send
    // an operator to ask for a permission nobody can grant.
    stubTab({
      'POST /v1/m/compliance/nis2/incidents/classify': () =>
        jsonResponse(
          {
            error: {
              message:
                'the "nis2incident" add-on is required for significant-incident classification; reading, verifying and exporting your data are unaffected',
            },
          },
          403,
        ),
    })
    renderIntel(<Nis2Tab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Classify incident/i }),
    )
    await user.type(screen.getByLabelText(/Incident reference/i), 'INC-004')
    await user.type(screen.getByLabelText(/Impact document/i), '{{"x":1}')
    await user.click(screen.getByRole('button', { name: /^Classify$/i }))

    await waitFor(() => expect(toastSpy.warning).toHaveBeenCalled())
    // Warning, not error: a boundary is not a fault. And the ENGINE's words.
    expect(String(toastSpy.warning.mock.calls[0][0])).toMatch(
      /add-on is required/i,
    )
    expect(toastSpy.error).not.toHaveBeenCalled()
    // Not the generic string the shared hook would have used.
    expect(String(toastSpy.warning.mock.calls[0][0])).not.toMatch(
      /^You are not authorized/i,
    )
  })

  it('still reports a REAL failure loudly — the boundary is not a blanket mute', async () => {
    const user = userEvent.setup()
    stubTab({
      'POST /v1/m/compliance/nis2/incidents/classify': () =>
        jsonResponse({ error: { message: 'internal error' } }, 500),
    })
    renderIntel(<Nis2Tab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Classify incident/i }),
    )
    await user.type(screen.getByLabelText(/Incident reference/i), 'INC-003')
    await user.type(screen.getByLabelText(/Impact document/i), '{{"x":1}')
    await user.click(screen.getByRole('button', { name: /^Classify$/i }))

    // The direction of non-firing: silence for a 501 is only correct if a 500
    // still speaks. A handler that swallowed everything would pass the cell above.
    await waitFor(() => expect(toastSpy.error).toHaveBeenCalled())
    expect(screen.queryByText(/not linked in this build/i)).toBeNull()
  })

  it('advances a phase with the exact body the engine accepts', async () => {
    const user = userEvent.setup()
    const seen: string[] = []
    const mock = stubFetch((url, init) => {
      const path = new URL(url, 'https://console.invalid').pathname
      if (init.method === 'PUT') {
        seen.push(String(init.body))
        return jsonResponse({ ...INCIDENT, phase: 'notification' })
      }
      if (path === '/v1/m/compliance/nis2/incidents')
        return jsonResponse({ items: [INCIDENT] })
      return jsonResponse({}, 404)
    })
    renderIntel(<Nis2Tab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Advance phase/i }),
    )
    // Scoped to the dialog: the row button carries the same label, and an
    // unscoped query would be satisfied by clicking the one that opened it.
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /^Advance phase$/i }),
    )

    await waitFor(() => expect(seen.length).toBe(1))
    // Straight from the dialog to the wire: only the keys the decoder allows, and
    // the phase the forward-only list defaulted to (the one after early_warning).
    expect(Object.keys(JSON.parse(seen[0]))).toEqual(['phase'])
    expect(JSON.parse(seen[0]).phase).toBe('notification')
    void mock
  })

  it('will not delete on a blank OR a wrong phrase, and does on the right one', async () => {
    const user = userEvent.setup()
    const mock = stubTab({
      'DELETE /v1/m/compliance/nis2/incidents/ni-1': () =>
        jsonResponse({ deleted: true }),
    })
    const deletes = () =>
      mock.mock.calls.filter(
        ([, init]) => (init as RequestInit).method === 'DELETE',
      )
    renderIntel(<Nis2Tab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^Delete$/i }))
    const confirm = await screen.findByRole('button', {
      name: /Delete classification/i,
    })
    const guard = document.getElementById('confirm-phrase') as HTMLInputElement

    // (1) blank
    expect(confirm).toBeDisabled()

    // (2) WRONG PHRASE — the half the first version of this cell never typed. Without
    // it, a mutant loosening the comparison from exact equality to "any non-empty
    // text" (confirm-dialog.tsx:75-76) survives: at the only observed instant the
    // field was empty, so both versions agree.
    await user.type(guard, 'delete')
    expect(confirm).toBeDisabled()
    expect(deletes()).toHaveLength(0)

    // (3) the right phrase — and the positive path, which the first version also
    // never exercised, so it stayed green with the delete disconnected entirely.
    await user.clear(guard)
    await user.type(guard, 'DELETE')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(deletes()).toHaveLength(1))
    const [url] = deletes()[0] as [string, RequestInit]
    expect(new URL(url, 'https://console.invalid').pathname).toBe(
      '/v1/m/compliance/nis2/incidents/ni-1',
    )
    await waitFor(() => expect(toastSpy.success).toHaveBeenCalled())
  })

  it('does not announce a deletion the engine did not confirm', async () => {
    const user = userEvent.setup()
    // 200 with no `deleted` field: the allowlist must refuse to call it done.
    stubTab({
      'DELETE /v1/m/compliance/nis2/incidents/ni-1': () => jsonResponse({}),
    })
    renderIntel(<Nis2Tab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /^Delete$/i }))
    const guard = document.getElementById('confirm-phrase') as HTMLInputElement
    await user.type(guard, 'DELETE')
    await user.click(
      screen.getByRole('button', { name: /Delete classification/i }),
    )

    await waitFor(() => expect(toastSpy.success).toHaveBeenCalled())
    // usePrivilegedMutation has one success channel, so the WORDING carries the
    // news: "gone" is the one thing this must never claim wrongly.
    expect(String(toastSpy.success.mock.calls[0][0])).toMatch(
      /did not confirm the deletion/i,
    )
  })

  it('fetches and renders the deadlines the list cannot carry', async () => {
    const user = userEvent.setup()
    // The list builds its DTOs with includeBody=false (nis2incident.go:224), so
    // `deadlines`/`report_drafts`/`basis` are absent from the row and the panel has
    // to issue the single-incident GET. No cell exercised that route before.
    const mock = stubTab({
      'GET /v1/m/compliance/nis2/incidents/ni-1': () =>
        jsonResponse({
          ...INCIDENT,
          deadlines: { early_warning_due: '2026-06-05T12:00:00Z' },
          report_drafts: { early_warning: { reference: 'INC-001' } },
          basis: [
            {
              provision: 'Art 23(3)',
              source_url: 'https://eur-lex.europa.eu/eli/dir/2022/2555/oj',
            },
          ],
        }),
    })
    renderIntel(<Nis2Tab canAdmin canRead />)

    await user.click(await screen.findByRole('button', { name: /Deadlines/i }))

    expect(await screen.findByText('early_warning_due')).toBeInTheDocument()
    expect(screen.getByText('2026-06-05T12:00:00Z')).toBeInTheDocument()
    // Scoped: "Art 23(3)" also appears in the tab-level honesty banner and in the
    // verdict badge, so an unscoped query is satisfied without the panel rendering
    // anything the engine returned.
    const panel = within(screen.getByRole('dialog'))
    expect(
      panel.getByText(/Art 23\(3\).*eur-lex\.europa\.eu/),
    ).toBeInTheDocument()
    // The route was actually called — the panel is not rendering the row it was
    // handed.
    expect(
      mock.mock.calls.filter(([u]) =>
        String(u).endsWith('/nis2/incidents/ni-1'),
      ),
    ).not.toHaveLength(0)
  })

  it('offers no phase move for a value this build cannot order, and says why', async () => {
    // A phase from a newer engine vocabulary. Offering the old list would be a menu
    // whose every option is backwards (409); offering nothing WITHOUT saying why
    // reads as "already final", which is the opposite news.
    stubFetch((url) => {
      const path = new URL(url, 'https://console.invalid').pathname
      if (path === '/v1/m/compliance/nis2/incidents')
        return jsonResponse({ items: [{ ...INCIDENT, phase: 'post_mortem' }] })
      return jsonResponse({}, 404)
    })
    renderIntel(<Nis2Tab canAdmin canRead />)

    expect(await screen.findByText('INC-001')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Advance phase/i })).toBeNull()
    expect(screen.getByText(/cannot place the phase/i)).toBeInTheDocument()
  })

  it('refuses to send an impact document over the engine cap', async () => {
    const user = userEvent.setup()
    const mock = stubTab()
    renderIntel(<Nis2Tab canAdmin canRead />)

    await user.click(
      await screen.findByRole('button', { name: /Classify incident/i }),
    )
    await user.type(screen.getByLabelText(/Incident reference/i), 'INC-BIG')
    // Typed via fireEvent: user.type of a megabyte is not a test, it is a hang.
    fireEvent.change(screen.getByLabelText(/Impact document/i), {
      target: { value: 'a'.repeat(1024 * 1024 + 1) },
    })

    expect(await screen.findByText(/exceeds .* bytes/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^Classify$/i })).toBeDisabled()
    // The engine now answers 413 rather than truncating (readBoundedBody), but the
    // console should not spend a megabyte to be told so.
    expect(
      mock.mock.calls.filter(
        ([, init]) => (init as RequestInit).method === 'POST',
      ),
    ).toHaveLength(0)
  })
})
