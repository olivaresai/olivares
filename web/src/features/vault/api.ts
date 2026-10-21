// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The vault rides the organization's secrets routes in their tenant scope: list, put and
// delete are the secret store's own (consoleApi), and three verbs are the vault's:
// reveal an item to its owner, the current authenticator code, and a generated password.
// Every one of them asks the deployment's step-up; none is cached.
import { consoleApi } from '@/features/console/api'
import { http, type RequestOptions } from '@/lib/api/client'
import { serializeItem, type VaultItem } from './model'

const BASE = '/v1/console/secrets'
const TENANT_SCOPE = { scope: 'tenant' } as const

type Opts = Omit<RequestOptions, 'method' | 'body' | 'query'>

export interface VaultCode {
  code: string
  seconds_left: number
  period: number
  digits: number
}

export interface PasswordOptions {
  length: number
  lower: boolean
  upper: boolean
  digits: boolean
  symbols: boolean
}

export const DEFAULT_PASSWORD_OPTIONS: PasswordOptions = {
  length: 20,
  lower: true,
  upper: true,
  digits: true,
  symbols: true,
}

export const vaultApi = {
  list: (opts?: Opts) => consoleApi.listSecrets('tenant', opts),
  put: (name: string, item: VaultItem, description: string, opts?: Opts) =>
    consoleApi.putSecret(
      { name, value: serializeItem(item), description },
      'tenant',
      opts,
    ),
  remove: (name: string, opts?: Opts) =>
    consoleApi.deleteSecret(name, 'tenant', opts),
  reveal: (name: string, opts?: Opts) =>
    http.post<{ name: string; value: string }>(
      `${BASE}/reveal`,
      { name },
      { ...opts, query: TENANT_SCOPE },
    ),
  code: (name: string, opts?: Opts) =>
    http.post<VaultCode>(
      `${BASE}/totp`,
      { name },
      { ...opts, query: TENANT_SCOPE },
    ),
  generate: (options: PasswordOptions, opts?: Opts) =>
    http.post<{ password: string }>(`${BASE}/generate`, options, {
      ...opts,
      query: TENANT_SCOPE,
    }),
}

/** Tenant-scoped query key (query.ts contract: tenant id and epoch in every key). */
export const vaultKeys = {
  list: (tenant: string | null, epoch: number) =>
    ['vault', tenant, epoch, 'list'] as const,
}
