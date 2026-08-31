// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// spec:asyncapi — validate the hand-authored event-bus contract against the
// AsyncAPI 3.0 specification. Fails on any error-level diagnostic. (DOC-04)

import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { Parser } from '@asyncapi/parser'

const SPEC = fileURLToPath(new URL('../public/asyncapi/asyncapi.yaml', import.meta.url))

const source = await readFile(SPEC, 'utf8')
const parser = new Parser()
const { document, diagnostics } = await parser.parse(source)

const errors = diagnostics.filter((d) => d.severity === 0) // 0 = error
const warnings = diagnostics.filter((d) => d.severity === 1)

for (const w of warnings) console.warn(`  warn: ${w.message} @ ${w.path?.join('/')}`)

if (!document || errors.length > 0) {
  console.error(`\n✗ AsyncAPI spec INVALID (${errors.length} error(s)):`)
  for (const e of errors) console.error(`  - ${e.message} @ ${e.path?.join('/')}`)
  process.exit(1)
}

const version = document.version()
if (version !== '3.0.0') {
  console.error(`✗ Expected AsyncAPI 3.0.0, got ${version}`)
  process.exit(1)
}

// Keep the security-significant K2 live-authority override projection explicit.
// These fields remain optional because ordinary acquire, renew and non-force
// takeover facts share this schema and must not be forced to fabricate them.
const workEventFact = document.components().schemas().get('WorkEventFact')
const forceTakeoverFields = {
  forced: { type: 'boolean', const: true },
  severity: { type: 'string', enum: ['high'] },
  decision_id: { type: 'string', format: 'uuid' },
  takeover_reason_hash: { type: 'string', pattern: '^[a-f0-9]{64}$' },
}
const workEventErrors = []

if (!workEventFact) {
  workEventErrors.push('components.schemas.WorkEventFact is missing')
} else {
  const required = new Set(workEventFact.required() ?? [])
  for (const [name, expected] of Object.entries(forceTakeoverFields)) {
    const property = workEventFact.property(name)
    if (!property) {
      workEventErrors.push(`WorkEventFact.${name} is missing`)
      continue
    }
    if (property.type() !== expected.type) {
      workEventErrors.push(`WorkEventFact.${name} must have type ${expected.type}`)
    }
    if (expected.format && property.format() !== expected.format) {
      workEventErrors.push(`WorkEventFact.${name} must have format ${expected.format}`)
    }
    if (expected.pattern && property.pattern() !== expected.pattern) {
      workEventErrors.push(`WorkEventFact.${name} must have pattern ${expected.pattern}`)
    }
    if (expected.const !== undefined && property.const() !== expected.const) {
      workEventErrors.push(`WorkEventFact.${name} must have const ${expected.const}`)
    }
    if (expected.enum && JSON.stringify(property.enum()) !== JSON.stringify(expected.enum)) {
      workEventErrors.push(`WorkEventFact.${name} must have enum ${expected.enum.join(',')}`)
    }
    if (required.has(name)) {
      workEventErrors.push(`WorkEventFact.${name} must stay optional for non-force events`)
    }
  }
  for (const rawReasonField of ['reason', 'takeover_reason', 'end_reason']) {
    if (workEventFact.property(rawReasonField)) {
      workEventErrors.push(`WorkEventFact must not publish raw field ${rawReasonField}`)
    }
  }
}

if (workEventErrors.length > 0) {
  console.error(`\n✗ AsyncAPI WorkEventFact security contract INVALID:`)
  for (const error of workEventErrors) console.error(`  - ${error}`)
  process.exit(1)
}

console.log(`✓ AsyncAPI ${version} valid: ${document.channels().length} channels, ${document.operations().length} operations, ${document.messages().length} messages.`)
