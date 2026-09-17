// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// N3-A — the strict generated web types publish the schema 2 capability contract
// (docs/contracts/CAPABILITY-PROJECTION.md) and nothing older.
//
// This is a fixture on `openapi.gen.ts`, not a consumer: no provider, no authorization
// wiring, no request leaves this file. Half of it is decided by `tsc -b`: the literal
// checks below fail the strict typecheck if the generated types stop saying `2`,
// `undisclosed` or `not_disclosed`, and every `@ts-expect-error` fails it if the
// schema 1 vocabulary (`1`, `not_available`) ever becomes assignable again. The
// runtime half round-trips one published response through JSON so the fixture is
// executed, not merely compiled.
import { describe, expect, it } from 'vitest'
import type { components, operations, paths } from '@/lib/api/openapi.gen'

type CapabilityQuestions = components['schemas']['CapabilityQuestions']
type CapabilityResult = components['schemas']['CapabilityResult']
type CapabilityResults = components['schemas']['CapabilityResults']
type Request =
  operations['authCapabilities']['requestBody']['content']['application/json']
type Response =
  operations['authCapabilities']['responses'][200]['content']['application/json']

// The path binds the same components the operation names; an operation typed apart
// from its path would let the two drift.
const _pathBindsOperation: paths['/v1/auth/capabilities']['post'] =
  null as unknown as operations['authCapabilities']
void _pathBindsOperation

// Schema 2 is the ONLY version either side accepts.
const version: CapabilityQuestions['schema_version'] = 2
const resultVersion: CapabilityResults['schema_version'] = 2
// @ts-expect-error schema 1 has no fallback: the request type refuses it
const _schemaOne: CapabilityQuestions['schema_version'] = 1
// @ts-expect-error a result cannot announce schema 1 either
const _resultSchemaOne: CapabilityResults['schema_version'] = 1
void _schemaOne
void _resultSchemaOne

// The concealment non-verdict is a state/code pair of its own.
const concealedState: CapabilityResult['state'] = 'undisclosed'
const concealedCode: CapabilityResult['code'] = 'not_disclosed'
// @ts-expect-error the schema 1 code was replaced, not kept beside the new one
const _retiredCode: CapabilityResult['code'] = 'not_available'
// @ts-expect-error nor did it become a state
const _retiredState: CapabilityResult['state'] = 'not_available'
void _retiredCode
void _retiredState

// docs/contracts/CAPABILITY-PROJECTION.md: one positive with its budget, one concealed
// non-verdict without one, one surface admission.
const published = {
  schema_version: 2,
  results: [
    {
      id: 'sheet',
      kind: 'operation',
      state: 'allowed',
      code: 'authorized',
      observed_at: '2026-09-07T10:00:00.250Z',
      refresh_after_ms: 30000,
    },
    {
      id: 'held',
      kind: 'operation',
      state: 'undisclosed',
      code: 'not_disclosed',
      observed_at: '2026-09-07T10:00:00Z',
    },
    {
      id: 'list',
      kind: 'surface',
      state: 'reachable',
      code: 'admitted',
      observed_at: '2026-09-07T10:00:00.100Z',
      refresh_after_ms: 12000,
    },
  ],
} satisfies Response

const request = {
  schema_version: version,
  questions: [
    {
      id: 'sheet',
      kind: 'operation',
      operation: 'GET /v1/m/sessions/channels/{id}/grants',
      workspace_id: '00000000-0000-4000-8000-000000000010',
      selectors: { path: { id: '00000000-0000-4000-8000-000000000001' } },
    },
    {
      id: 'held',
      kind: 'operation',
      operation: 'PATCH /v1/m/sessions/channels',
      workspace_id: '00000000-0000-4000-8000-000000000010',
      selectors: {
        body: { channel_id: '00000000-0000-4000-8000-000000000002' },
      },
    },
    {
      id: 'list',
      kind: 'surface',
      operation: 'GET /v1/m/sessions/channels',
      workspace_id: '00000000-0000-4000-8000-000000000010',
    },
  ],
} satisfies Request

describe('generated capability contract, schema 2', () => {
  it('publishes version 2 and the non-disclosure vocabulary as literal types', () => {
    expect(version).toBe(2)
    expect(resultVersion).toBe(2)
    expect(concealedState).toBe('undisclosed')
    expect(concealedCode).toBe('not_disclosed')
  })

  it('round-trips a published response and request through JSON unchanged', () => {
    const results: CapabilityResults = JSON.parse(JSON.stringify(published))
    expect(results).toEqual(published)
    expect(results.schema_version).toBe(2)
    expect(results.results.map((r) => [r.id, r.state, r.code])).toEqual([
      ['sheet', 'allowed', 'authorized'],
      ['held', 'undisclosed', 'not_disclosed'],
      ['list', 'reachable', 'admitted'],
    ])
    // The non-verdict carries no budget at all: absent, not zero.
    expect('refresh_after_ms' in results.results[1]).toBe(false)
    expect(results.results[0].refresh_after_ms).toBe(30000)

    const questions: CapabilityQuestions = JSON.parse(JSON.stringify(request))
    expect(questions).toEqual(request)
    expect(questions.schema_version).toBe(2)
    expect(questions.questions[2]).not.toHaveProperty('selectors')
  })
})
