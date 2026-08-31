// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  DOMAIN_FRONTING_CAVEAT,
  EGRESS_ALLOWLIST,
  HOOK_EVENTS,
  MANAGED_ONLY_KEYS,
  MANAGED_SETTINGS_PATHS,
  PERMISSION_REQUEST_DECISION,
  PRE_TOOL_USE_DECISION,
  makeJsonLintSource,
  validateHooks,
  validateManagedMcp,
  validateManagedSettings,
  validateSandbox,
} from './index'

describe('managed-settings schema (ANT2-11)', () => {
  it('encodes exactly the 14 verified managed-only keys and not the unverified ones', () => {
    expect(MANAGED_ONLY_KEYS).toHaveLength(14)
    const keys = MANAGED_ONLY_KEYS.map((k) => k.key)
    expect(keys).toContain('allowManagedHooksOnly')
    expect(keys).toContain('strictKnownMarketplaces')
    // The spec's unverified keys must NOT appear (no inventes claves).
    expect(keys).not.toContain('policyHelper')
    expect(keys).not.toContain('forceLoginMethod')
    expect(keys).not.toContain('parentSettingsBehavior')
    expect(keys).not.toContain('skillOverrides')
    expect(keys).not.toContain('disableRemoteControl')
  })

  it('uses the current Windows path, never the dead ProgramData one', () => {
    expect(MANAGED_SETTINGS_PATHS.windows).toBe(
      'C:\\Program Files\\ClaudeCode\\managed-settings.json',
    )
    expect(MANAGED_SETTINGS_PATHS.windowsLegacyRemovedSince).toBe('v2.1.75')
  })

  it('accepts a valid object, warns on unknown keys, errors on wrong types', () => {
    expect(
      validateManagedSettings({ allowManagedHooksOnly: true }),
    ).toHaveLength(0)
    const unknown = validateManagedSettings({ totallyMadeUpKey: 1 })
    expect(
      unknown.some(
        (i) => i.severity === 'warning' && i.path === 'totallyMadeUpKey',
      ),
    ).toBe(true)
    const wrongType = validateManagedSettings({ allowManagedHooksOnly: 'yes' })
    expect(wrongType.some((i) => i.severity === 'error')).toBe(true)
  })
})

describe('hooks schema (ANT2-10)', () => {
  it('encodes the 30 verified events', () => {
    expect(HOOK_EVENTS).toHaveLength(30)
    const names = HOOK_EVENTS.map((e) => e.name)
    expect(names).toContain('PreToolUse')
    expect(names).toContain('PermissionRequest')
    expect(names).toContain('ConfigChange')
    expect(names).toContain('InstructionsLoaded')
  })

  it('places applyPermissionRule ONLY in PermissionRequest, not PreToolUse', () => {
    expect(JSON.stringify(PRE_TOOL_USE_DECISION)).not.toContain(
      'applyPermissionRule',
    )
    expect(PERMISSION_REQUEST_DECISION.applyPermissionRule).toBeTruthy()
    expect(PRE_TOOL_USE_DECISION.permissionDecisionValues).toContain('defer')
  })

  it('errors on an unknown hook event (it would never fire)', () => {
    const issues = validateHooks({ NotARealEvent: [] })
    expect(
      issues.some((i) => i.severity === 'error' && i.path === 'NotARealEvent'),
    ).toBe(true)
    expect(
      validateHooks({
        PreToolUse: [{ hooks: [{ type: 'command', command: 'x' }] }],
      }),
    ).toHaveLength(0)
  })
})

describe('managed-mcp schema (ANT2-12)', () => {
  it('warns that a serverName-only allowlist is not a security control', () => {
    const issues = validateManagedMcp({ allowlist: [{ serverName: 'github' }] })
    expect(
      issues.some(
        (i) => i.severity === 'warning' && /security control/i.test(i.message),
      ),
    ).toBe(true)
  })
})

describe('sandbox schema (ANT2-13)', () => {
  it('exposes the egress allowlist and the domain-fronting caveat', () => {
    expect(EGRESS_ALLOWLIST.map((e) => e.host)).toContain('api.anthropic.com')
    expect(DOMAIN_FRONTING_CAVEAT).toMatch(/does NOT terminate or inspect TLS/i)
  })

  it('warns on an unknown sandbox key', () => {
    const issues = validateSandbox({ madeUp: true })
    expect(issues.some((i) => i.severity === 'warning')).toBe(true)
  })
})

describe('makeJsonLintSource', () => {
  it('maps a schema warning to an inline diagnostic at the offending key', () => {
    const lint = makeJsonLintSource('managed-settings')
    const doc = '{\n  "totallyMadeUpKey": 1\n}'
    const diags = lint(doc)
    expect(diags.length).toBeGreaterThan(0)
    const d = diags[0]!
    expect(doc.slice(d.from, d.to)).toContain('totallyMadeUpKey')
    expect(d.to).toBeGreaterThan(d.from)
  })

  it('returns nothing for invalid JSON (syntax is the editor jsonLint job)', () => {
    expect(makeJsonLintSource('hooks')('{ not json')).toHaveLength(0)
  })
})
