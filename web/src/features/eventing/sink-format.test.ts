// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The subscription form's sink_format list is a closed set mirroring the
// engine's catalog. Like the audit export-format pin, the comparison is against
// the COMMITTED beta OpenAPI snapshot rather than a second TypeScript literal:
// the snapshot's enum is rendered by the engine from the sdk/siemwire catalog's
// eventing surface — the same registry the eventing module validates against —
// and `task openapi:check` fails on generator/snapshot drift. A format added to
// the catalog and forgotten here fails on this side; one invented here that the
// engine refuses fails too.
import { describe, expect, it } from 'vitest'
import openapiBeta from '../../../openapi/openapi.beta.json'
import { SINK_FORMATS } from './eventing-view'

/** The engine-owned sink_format enum on POST /v1/m/eventing/subscriptions. */
function engineSinkFormats(): string[] {
  const doc = openapiBeta as unknown as {
    paths: Record<
      string,
      {
        post: {
          requestBody?: {
            content: Record<
              string,
              {
                schema: {
                  properties?: Record<string, { enum?: string[] }>
                }
              }
            >
          }
        }
      }
    >
  }
  const body = doc.paths['/v1/m/eventing/subscriptions']?.post?.requestBody
  if (!body) {
    throw new Error(
      'the beta OpenAPI snapshot no longer declares the subscription request body',
    )
  }
  const enumValues =
    body.content['application/json']?.schema.properties?.['sink_format']?.enum
  if (!enumValues?.length) {
    throw new Error('the beta snapshot has no enum for sink_format')
  }
  return enumValues
}

describe('eventing sink formats', () => {
  it('offers exactly the formats the engine accepts, in the catalog order', () => {
    // The engine's enum leads with '' (unset = the surface default); the form
    // models that as the absence of a selection, so the mirror is the enum
    // minus the empty spelling, order preserved.
    const engine = engineSinkFormats()
    expect(engine[0]).toBe('')
    expect([...SINK_FORMATS]).toEqual(engine.slice(1))
  })
})
