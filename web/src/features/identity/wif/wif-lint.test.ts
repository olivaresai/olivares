// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { WifGraphData, WifRule } from '../types'
import {
  lintCelOverBroad,
  lintDrift,
  lintKeyShadow,
  lintScope,
  lintTokenLifetime,
  lintWifGraph,
  ruleScope,
} from './wif-lint'

/** A well-scoped, narrow rule — the clean baseline that must produce NO findings. */
function narrowRule(over: Partial<WifRule> = {}): WifRule {
  return {
    rule_id: 'fdrl_clean',
    issuer_id: 'fdis_corp',
    service_account_id: 'svac_ci',
    oauth_scope: 'workspace:developer',
    workspace_id: 'wrkspc_prod',
    subject_prefix: 'repo:acme/api:',
    audience: 'https://api.anthropic.com',
    claims: { ref: 'refs/heads/main' },
    cel_condition: "claims.ref == 'refs/heads/main'",
    token_lifetime_seconds: 900,
    jwks_mode: 'discovery',
    ca_cert_configured: false,
    ...over,
  }
}

function graph(over: Partial<WifGraphData> = {}): WifGraphData {
  return {
    issuers: [
      {
        id: 'fdis_corp',
        issuer_url: 'https://idp.acme.com',
        jwks_mode: 'discovery',
      },
    ],
    rules: [narrowRule()],
    service_accounts: [
      { id: 'svac_ci', name: 'ci', oauth_scope: 'workspace:developer' },
    ],
    ...over,
  }
}

describe('ruleScope', () => {
  it('defaults an empty scope to workspace:developer', () => {
    expect(ruleScope({ oauth_scope: '' })).toBe('workspace:developer')
    expect(ruleScope({ oauth_scope: undefined })).toBe('workspace:developer')
    expect(ruleScope({ oauth_scope: 'org:manage_tunnels' })).toBe(
      'org:manage_tunnels',
    )
  })
})

describe('lintCelOverBroad', () => {
  it('does NOT flag a rule with real narrowing criteria', () => {
    expect(lintCelOverBroad([narrowRule()])).toHaveLength(0)
  })

  it('flags a rule with no match criteria at all (accepts any subject)', () => {
    const open = narrowRule({
      rule_id: 'fdrl_open',
      subject_prefix: '',
      audience: '',
      claims: {},
      cel_condition: '',
    })
    const out = lintCelOverBroad([open])
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({
      rule: 'cel-over-broad',
      severity: 'warning',
      subjectRef: 'fdrl_open',
    })
    expect(out[0].meta?.reason).toBe('no-match-criteria')
  })

  it('flags a tautological CEL condition even when other fields are set', () => {
    const taut = narrowRule({ rule_id: 'fdrl_taut', cel_condition: 'true' })
    const out = lintCelOverBroad([taut])
    expect(out).toHaveLength(1)
    expect(out[0].meta?.reason).toBe('tautological-cel')
  })

  it('flags an empty subject_prefix as over-broad EVEN when another axis narrows', () => {
    // claims narrow the rule, so it is NOT "no-match-criteria", but an empty subject is
    // still an over-broad axis worth review.
    const out = lintCelOverBroad([
      narrowRule({ rule_id: 'fdrl_nosub', subject_prefix: '' }),
    ])
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ rule: 'cel-over-broad', severity: 'warning' })
    expect(out[0].meta?.reason).toBe('over-broad-subject')
    expect(out[0].meta?.subject).toBe('empty')
  })

  it('flags a bare "*" subject as a warning and a prefix wildcard as info', () => {
    const bare = lintCelOverBroad([
      narrowRule({ rule_id: 'fdrl_star', subject_prefix: '*' }),
    ])
    expect(bare[0]).toMatchObject({ severity: 'warning' })
    expect(bare[0].meta?.subject).toBe('wildcard')

    const prefix = lintCelOverBroad([
      narrowRule({ rule_id: 'fdrl_pfx', subject_prefix: 'repo:acme/*' }),
    ])
    expect(prefix[0]).toMatchObject({ severity: 'info' })
    expect(prefix[0].meta?.subject).toBe('prefix')
  })

  it('does NOT double-flag an all-empty rule (no-match-criteria only)', () => {
    const out = lintCelOverBroad([
      narrowRule({
        rule_id: 'fdrl_open',
        subject_prefix: '',
        audience: '',
        claims: {},
        cel_condition: '',
      }),
    ])
    expect(out).toHaveLength(1)
    expect(out[0].meta?.reason).toBe('no-match-criteria')
  })
})

describe('lintScope (privileged + multi-scope)', () => {
  it('flags org:admin as org-wide', () => {
    const out = lintScope([
      narrowRule({ rule_id: 'fdrl_admin', oauth_scope: 'org:admin' }),
    ])
    expect(out[0]).toMatchObject({
      rule: 'scope-over-broad',
      severity: 'warning',
    })
    expect(out[0].meta?.reason).toBe('org-wide')
  })

  it('does NOT flag the new workspace:inference scope as unrecognised', () => {
    expect(
      lintScope([
        narrowRule({ rule_id: 'fdrl_inf', oauth_scope: 'workspace:inference' }),
      ]),
    ).toHaveLength(0)
  })

  it('splits a space-separated multi-scope and flags the org-wide token', () => {
    const out = lintScope([
      narrowRule({
        rule_id: 'fdrl_multi',
        oauth_scope: 'workspace:developer org:admin',
      }),
    ])
    expect(out).toHaveLength(1)
    expect(out[0].meta?.scope).toBe('org:admin')
    expect(out[0].meta?.reason).toBe('org-wide')
  })
})

describe('lintDrift (declared-vs-actual reconciliation)', () => {
  it('emits nothing on a graph that was not reconciled', () => {
    const g = graph({ reconciliation: undefined })
    expect(lintDrift(g)).toHaveLength(0)
    // even with source markers, an unreconciled graph stays quiet
    g.rules[0].source = 'live'
    expect(lintDrift(g)).toHaveLength(0)
  })

  it('flags an undeclared live rule as an error and a stale declared rule as a warning', () => {
    const g = graph({
      reconciliation: { reconciled: true, observed_at: '2026-06-19T00:00:00Z' },
      issuers: [{ id: 'fdis_corp', source: 'both' }],
      rules: [
        narrowRule({ rule_id: 'fdrl_live', source: 'live' }),
        narrowRule({ rule_id: 'fdrl_stale', source: 'declared' }),
      ],
      service_accounts: [{ id: 'svac_ci', source: 'both' }],
    })
    const out = lintDrift(g)
    const live = out.find((f) => f.subjectRef === 'fdrl_live')
    const stale = out.find((f) => f.subjectRef === 'fdrl_stale')
    expect(live).toMatchObject({ rule: 'drift', severity: 'error' })
    expect(live?.meta?.reason).toBe('undeclared-rule')
    expect(stale).toMatchObject({ rule: 'drift', severity: 'warning' })
    expect(stale?.meta?.reason).toBe('declared-not-live')
  })

  it('flags an orphan rule (missing issuer/service account) and an orphan issuer', () => {
    const g: WifGraphData = {
      reconciliation: { reconciled: true },
      issuers: [{ id: 'fdis_lonely', source: 'live' }],
      service_accounts: [],
      rules: [
        narrowRule({
          rule_id: 'fdrl_orphan',
          source: 'live',
          issuer_id: 'fdis_missing',
          service_account_id: 'svac_missing',
        }),
      ],
    }
    const out = lintDrift(g)
    const orphanRule = out.filter(
      (f) => f.subjectRef === 'fdrl_orphan' && f.meta?.reason === 'orphan-rule',
    )
    const orphanIssuer = out.find(
      (f) => f.subjectRef === 'fdis_lonely' && f.meta?.reason === 'orphan-issuer',
    )
    expect(orphanRule).toHaveLength(1)
    expect(orphanRule[0].severity).toBe('warning')
    expect(orphanIssuer).toMatchObject({ rule: 'drift', severity: 'info' })
  })
})

describe('lintKeyShadow (footgun)', () => {
  it('flags ANTHROPIC_API_KEY shadowing when federation is in use', () => {
    const out = lintKeyShadow(
      graph({ key_shadow: { present: true, var: 'ANTHROPIC_API_KEY' } }),
    )
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ rule: 'key-shadow', severity: 'error' })
    expect(out[0].subjectRef).toBe('ANTHROPIC_API_KEY')
  })

  it('does NOT flag when no static key is present', () => {
    expect(
      lintKeyShadow(graph({ key_shadow: { present: false } })),
    ).toHaveLength(0)
    expect(lintKeyShadow(graph())).toHaveLength(0)
  })

  it('does NOT flag when a key is present but federation is not in use', () => {
    const out = lintKeyShadow({
      issuers: [],
      rules: [],
      service_accounts: [],
      key_shadow: { present: true, var: 'ANTHROPIC_API_KEY' },
    })
    expect(out).toHaveLength(0)
  })
})

describe('lintTokenLifetime', () => {
  it('ignores an undeclared (0/undefined) lifetime', () => {
    expect(
      lintTokenLifetime([narrowRule({ token_lifetime_seconds: 0 })]),
    ).toHaveLength(0)
    expect(
      lintTokenLifetime([narrowRule({ token_lifetime_seconds: undefined })]),
    ).toHaveLength(0)
  })

  it('flags a lifetime below the 60s floor as out-of-band', () => {
    const out = lintTokenLifetime([narrowRule({ token_lifetime_seconds: 30 })])
    expect(out[0]).toMatchObject({
      rule: 'token-lifetime',
      severity: 'warning',
    })
    expect(out[0].meta?.reason).toBe('out-of-band')
  })

  it('flags a lifetime above the 86400s ceiling as out-of-band', () => {
    const out = lintTokenLifetime([
      narrowRule({ token_lifetime_seconds: 90_000 }),
    ])
    expect(out[0].meta?.reason).toBe('out-of-band')
  })

  it('flags an in-band but long-lived (≥12h) token as info', () => {
    const out = lintTokenLifetime([
      narrowRule({ token_lifetime_seconds: 80_000 }),
    ])
    expect(out[0]).toMatchObject({ rule: 'token-lifetime', severity: 'info' })
    expect(out[0].meta?.reason).toBe('long-lived')
  })

  it('does NOT flag a short, sensible lifetime', () => {
    expect(
      lintTokenLifetime([narrowRule({ token_lifetime_seconds: 900 })]),
    ).toHaveLength(0)
  })
})

describe('lintScope', () => {
  it('flags org-wide org:manage_tunnels as a warning', () => {
    const out = lintScope([
      narrowRule({ rule_id: 'fdrl_org', oauth_scope: 'org:manage_tunnels' }),
    ])
    expect(out[0]).toMatchObject({
      rule: 'scope-over-broad',
      severity: 'warning',
    })
    expect(out[0].meta?.reason).toBe('org-wide')
  })

  it('does NOT flag the least-privilege workspace:developer scope', () => {
    expect(lintScope([narrowRule()])).toHaveLength(0)
  })

  it('flags an unrecognised scope as info', () => {
    const out = lintScope([
      narrowRule({ rule_id: 'fdrl_x', oauth_scope: 'org:superpower' }),
    ])
    expect(out[0].meta?.reason).toBe('unrecognised')
  })

  it('also lints service-account scopes', () => {
    const out = lintScope(
      [],
      [{ id: 'svac_admin', oauth_scope: 'org:manage_tunnels' }],
    )
    expect(out[0].subjectRef).toBe('svac_admin')
  })
})

describe('lintWifGraph', () => {
  it('returns no findings for a clean, narrow, well-scoped graph', () => {
    expect(lintWifGraph(graph())).toHaveLength(0)
  })

  it('surfaces the key-shadow error FIRST (worst severity) when multiple rules fire', () => {
    const dirty = graph({
      rules: [
        narrowRule({
          rule_id: 'fdrl_open',
          subject_prefix: '',
          audience: '',
          claims: {},
          cel_condition: '',
        }),
        narrowRule({ rule_id: 'fdrl_org', oauth_scope: 'org:manage_tunnels' }),
      ],
      key_shadow: { present: true, var: 'ANTHROPIC_API_KEY' },
    })
    const out = lintWifGraph(dirty)
    expect(out.length).toBeGreaterThanOrEqual(3)
    expect(out[0].rule).toBe('key-shadow')
    expect(out[0].severity).toBe('error')
  })
})
