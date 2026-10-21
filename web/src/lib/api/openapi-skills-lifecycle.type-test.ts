// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import type { paths } from './openapi.gen'

// Compile real client inputs so snapshot-only regeneration cannot omit these routes.
type Diff = paths['/v1/m/skills/packs/{id}/diff']['get']
type Update = paths['/v1/m/skills/packs/{id}/update']['post']

export const diffParameters = {
  query: { from: 'previous-revision', to: 'latest-revision' },
  path: { id: 'pack-id' },
  header: { 'X-Olivares-Tenant': 'tenant-id' },
} satisfies Diff['parameters']

export const updateParameters = {
  path: { id: 'pack-id' },
  header: {
    'Idempotency-Key': 'reviewed-update',
    'X-Olivares-Tenant': 'tenant-id',
  },
} satisfies Update['parameters']

export const checkUpdate =
  {} satisfies Update['requestBody']['content']['application/json']
export const applyUpdate = {
  apply: true,
  expected_digest: 'a'.repeat(64),
  ref: 'main',
  subdir: 'skills',
} satisfies Update['requestBody']['content']['application/json']

export const updateStatuses = [
  200, 201, 401, 403,
] satisfies (keyof Update['responses'])[]

type Assert<T extends true> = T
export type DiffHasNoRequestBody = Assert<
  Diff['requestBody'] extends undefined ? true : false
>
