// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { readFileSync } from 'node:fs'

const stable = JSON.parse(
  readFileSync(new URL('../openapi/openapi.json', import.meta.url), 'utf8'),
)
const beta = JSON.parse(
  readFileSync(
    new URL('../openapi/openapi.beta.json', import.meta.url),
    'utf8',
  ),
)

for (const [path, item] of Object.entries(beta.paths ?? {})) {
  const target = (stable.paths[path] ??= {})
  for (const [method, operation] of Object.entries(item)) {
    if (Object.hasOwn(target, method)) {
      throw new Error(
        `OpenAPI codegen union collision: ${method.toUpperCase()} ${path}`,
      )
    }
    target[method] = operation
  }
}

process.stdout.write(`${JSON.stringify(stable)}\n`)
